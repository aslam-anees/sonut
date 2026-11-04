package sonutgit

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/aistatine/sonut/internal/no"
)

func TestGuardCommitNoConfigIsPermissive(t *testing.T) {
	root := t.TempDir()
	findings, err := guardCommit(context.Background(), root, "main", nil)
	if err != nil {
		t.Fatalf("guardCommit returned error with no config present: %v", err)
	}
	// Default config only warns about protected branches; it must never
	// block a commit when the repo has no .no-mistakes.yaml.
	if findings.Blocking() {
		t.Fatalf("expected no blocking findings by default, got %+v", findings)
	}
}

func TestGuardCommitMalformedConfigFailsOpen(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, no.ConfigFileName), []byte("not: [valid"), 0o644); err != nil {
		t.Fatal(err)
	}
	// A broken config must not brick every commit in the repo.
	if _, err := guardCommit(context.Background(), root, "main", nil); err != nil {
		t.Fatalf("expected guardCommit to fail open on malformed config, got: %v", err)
	}
}

func TestGuardCommitSurfacesGuardErrorOnBlockingFinding(t *testing.T) {
	// force-push guard is the only current blocking rule; a commit on a
	// protected branch only warns. Exercise the block path directly through
	// no.Check to confirm sonutgit's GuardError wraps it correctly, using
	// the same wiring guardCommit uses.
	cfg := no.DefaultConfig()
	findings, err := no.Check(context.Background(), cfg, nil, no.Operation{
		Kind:   no.KindForcePush,
		Branch: "feature/x",
	})
	if err != nil {
		t.Fatalf("no.Check: %v", err)
	}
	if !findings.Blocking() {
		t.Fatal("expected a blocking finding to set up this test")
	}
	guardErr := &GuardError{Findings: findings}
	if !strings.Contains(guardErr.Error(), "blocked by no-mistakes guard") {
		t.Fatalf("unexpected GuardError message: %q", guardErr.Error())
	}
}

func TestCommitOnProtectedBranchStillSucceeds(t *testing.T) {
	// Regression guard: the wired-in commit hook must stay non-breaking.
	// Committing on "main" with no .no-mistakes.yaml must still succeed,
	// exactly as it did before internal/no was wired in.
	root := t.TempDir()
	runner := &fakeRunner{results: []CommandResult{
		{Stdout: root + "\n"},
		{Stdout: "main\n"},
		{Stdout: "abc1234\n"},
		{Stdout: " M f.go\x00"},
		{Stdout: "abc1234\n"},
		{},
		{},
		{Stdout: " 1 file changed, 1 insertion(+)\n"},
		{Stdout: "diff --git a/f.go b/f.go\n"},
		{},
		{Stdout: "[main def5678] Update f.go\n"},
		{Stdout: "def5678\n"},
	}}

	result, err := Commit(context.Background(), CommitOptions{
		Cwd:    root,
		RunGit: runner.Run,
	})
	if err != nil {
		t.Fatalf("Commit returned error: %v", err)
	}
	if !result.Committed {
		t.Fatalf("expected commit to succeed on protected branch with default config: %#v", result)
	}
	var guardErr *GuardError
	if errors.As(err, &guardErr) {
		t.Fatal("did not expect a GuardError for a permissive default config")
	}
}
