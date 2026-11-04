package no

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestDefaultConfigIsPermissiveButSane(t *testing.T) {
	cfg := DefaultConfig()
	if !cfg.blockForcePush() {
		t.Fatal("expected force-push guard enabled by default")
	}
	if !cfg.isProtectedBranch("main") || !cfg.isProtectedBranch("master") {
		t.Fatal("expected main/master protected by default")
	}
	if cfg.isProtectedBranch("feature/x") {
		t.Fatal("did not expect feature branch to be protected")
	}
}

func TestLoadConfigMissingFileReturnsDefault(t *testing.T) {
	dir := t.TempDir()
	cfg, err := LoadConfig(dir)
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	if !cfg.isProtectedBranch("main") {
		t.Fatal("expected default protected branches when no config file exists")
	}
}

func TestLoadConfigHonorsOverrides(t *testing.T) {
	dir := t.TempDir()
	content := "protected_branches:\n  - release\nblock_force_push_data_loss: false\n"
	if err := os.WriteFile(filepath.Join(dir, ConfigFileName), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg, err := LoadConfig(dir)
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	if cfg.isProtectedBranch("main") {
		t.Fatal("main should no longer be protected when overridden")
	}
	if !cfg.isProtectedBranch("release") {
		t.Fatal("release should be protected per override")
	}
	if cfg.blockForcePush() {
		t.Fatal("expected force-push guard disabled per override")
	}
}

func TestLoadConfigMalformedFileErrors(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, ConfigFileName), []byte("not: [valid: yaml"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadConfig(dir); err == nil {
		t.Fatal("expected error for malformed config")
	}
}

func TestCheckCommitOnProtectedBranchWarns(t *testing.T) {
	cfg := DefaultConfig()
	findings, err := Check(context.Background(), cfg, nil, Operation{Kind: KindCommit, Branch: "main"})
	if err != nil {
		t.Fatalf("Check: %v", err)
	}
	if len(findings) != 1 || findings[0].Rule != RuleProtected || findings[0].Severity != SeverityWarn {
		t.Fatalf("expected one protected-branch warning, got %+v", findings)
	}
	if findings.Blocking() {
		t.Fatal("a warn-level finding should not be blocking")
	}
}

func TestCheckCommitOnFeatureBranchClean(t *testing.T) {
	cfg := DefaultConfig()
	findings, err := Check(context.Background(), cfg, nil, Operation{Kind: KindCommit, Branch: "feature/x"})
	if err != nil {
		t.Fatalf("Check: %v", err)
	}
	if len(findings) != 0 {
		t.Fatalf("expected no findings for a feature branch commit, got %+v", findings)
	}
}

func TestCheckForcePushWithoutRunnerBlocksFailSafe(t *testing.T) {
	cfg := DefaultConfig()
	findings, err := Check(context.Background(), cfg, nil, Operation{Kind: KindForcePush, Branch: "feature/x"})
	if err != nil {
		t.Fatalf("Check: %v", err)
	}
	if !findings.Blocking() {
		t.Fatal("expected a blocking finding when no GitRunner is available to verify remote state")
	}
}

func TestCheckForcePushDisabledSkipsRemoteCheck(t *testing.T) {
	f := false
	cfg := Config{BlockForcePushDataLoss: &f}
	called := false
	run := func(ctx context.Context, dir string, args ...string) (string, error) {
		called = true
		return "", nil
	}
	findings, err := Check(context.Background(), cfg, run, Operation{Kind: KindForcePush, Branch: "feature/x"})
	if err != nil {
		t.Fatalf("Check: %v", err)
	}
	if len(findings) != 0 {
		t.Fatalf("expected no findings when force-push guard disabled, got %+v", findings)
	}
	if called {
		t.Fatal("expected no git calls when force-push guard is disabled")
	}
}

func TestResolveForcePushDecisionNewBranch(t *testing.T) {
	run := func(ctx context.Context, dir string, args ...string) (string, error) {
		if args[0] == "ls-remote" {
			return "", nil
		}
		t.Fatalf("unexpected git call: %v", args)
		return "", nil
	}
	decision, err := ResolveForcePushDecision(context.Background(), run, "origin", "refs/heads/x", "abc123", "", "")
	if err != nil {
		t.Fatalf("ResolveForcePushDecision: %v", err)
	}
	if !decision.NewBranch {
		t.Fatalf("expected NewBranch decision, got %+v", decision)
	}
}

func TestResolveForcePushDecisionUpToDate(t *testing.T) {
	sha := "abcdef1234567890abcdef1234567890abcdef12"
	run := func(ctx context.Context, dir string, args ...string) (string, error) {
		if args[0] == "ls-remote" {
			return sha + "\trefs/heads/x", nil
		}
		t.Fatalf("unexpected git call: %v", args)
		return "", nil
	}
	decision, err := ResolveForcePushDecision(context.Background(), run, "origin", "refs/heads/x", sha, "", "")
	if err != nil {
		t.Fatalf("ResolveForcePushDecision: %v", err)
	}
	if !decision.UpToDate {
		t.Fatalf("expected UpToDate decision, got %+v", decision)
	}
}

func TestResolveForcePushDecisionRefusesToDiscardUnseenCommits(t *testing.T) {
	remoteSHA := "1111111111111111111111111111111111111a"
	newHead := "2222222222222222222222222222222222222b"
	run := func(ctx context.Context, dir string, args ...string) (string, error) {
		switch args[0] {
		case "ls-remote":
			return remoteSHA + "\trefs/heads/x", nil
		case "fetch":
			return "", nil
		case "rev-list":
			// Simulate one commit on the remote that never made it into newHead.
			return "3333333333333333333333333333333333333c", nil
		}
		t.Fatalf("unexpected git call: %v", args)
		return "", nil
	}
	_, err := ResolveForcePushDecision(context.Background(), run, "origin", "refs/heads/x", newHead, "", "")
	if err == nil {
		t.Fatal("expected refusal when the remote carries commits never incorporated")
	}
	var discardErr *ForcePushWouldDiscardError
	if !asDiscardError(err, &discardErr) {
		t.Fatalf("expected ForcePushWouldDiscardError, got %T: %v", err, err)
	}
}

func TestCheckForcePushBlocksOnDataLoss(t *testing.T) {
	remoteSHA := "1111111111111111111111111111111111111a"
	newHead := "2222222222222222222222222222222222222b"
	run := func(ctx context.Context, dir string, args ...string) (string, error) {
		switch args[0] {
		case "ls-remote":
			return remoteSHA + "\trefs/heads/x", nil
		case "fetch":
			return "", nil
		case "rev-list":
			return "3333333333333333333333333333333333333c", nil
		}
		return "", nil
	}
	cfg := DefaultConfig()
	findings, err := Check(context.Background(), cfg, run, Operation{
		Kind:       KindForcePush,
		Branch:     "feature/x",
		Remote:     "origin",
		Ref:        "refs/heads/x",
		NewHeadSHA: newHead,
	})
	if err != nil {
		t.Fatalf("Check: %v", err)
	}
	if !findings.Blocking() {
		t.Fatal("expected Check to block a data-losing force-push")
	}
	if got := findings.FirstBlocking().Rule; got != RuleForcePush {
		t.Fatalf("expected rule %q, got %q", RuleForcePush, got)
	}
}
