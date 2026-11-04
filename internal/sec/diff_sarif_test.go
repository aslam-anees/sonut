package sec

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestScanDiffModeOnlyChangedFiles(t *testing.T) {
	root := t.TempDir()
	// Init a git repo so diff mode has something to compare.
	run := func(args ...string) {
		t.Helper()
		cmd := exec.Command(args[0], args[1:]...)
		cmd.Dir = root
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("%v: %v\n%s", args, err, out)
		}
	}
	run("git", "init")
	run("git", "config", "user.email", "test@example.com")
	run("git", "config", "user.name", "test")
	writeFixture(t, root, "safe/config.yml", "ok: true\n", 0o644)
	writeFixture(t, root, "deploy/docker-compose.yml", "services:\n  api:\n    host: 0.0.0.0\n    privileged: true\n", 0o644)
	run("git", "add", ".")
	run("git", "commit", "-m", "init")

	// Change only the deploy file after the commit.
	writeFixture(t, root, "deploy/docker-compose.yml", "services:\n  api:\n    host: 0.0.0.0\n    privileged: true\n    # touch\n", 0o644)
	// Also add an untracked dangerous file that should be included.
	writeFixture(t, root, "new_secret_holder.env", "AWS_ACCESS_KEY_ID=AKIAABCDEFGHIJKLMNOP\n", 0o600)

	report, err := Scan(root, DiffOptions())
	if err != nil {
		t.Fatalf("Scan diff: %v", err)
	}
	if report.Mode != ScanModeDiff {
		t.Fatalf("mode=%q", report.Mode)
	}
	if report.Coverage.DiffFiles == 0 {
		t.Fatal("expected diff files > 0")
	}
	// Full tree would also see safe/config if we planted a finding there; we did not.
	// Diff should surface the privileged compose and/or secret.
	if len(report.Findings) == 0 {
		t.Fatalf("expected findings in changed files, coverage=%#v", report.Coverage)
	}
	for _, finding := range report.Findings {
		if finding.Priority == "" {
			t.Fatalf("finding missing priority: %#v", finding)
		}
		if finding.Path == "safe/config.yml" {
			t.Fatal("diff mode should not invent findings outside the change set")
		}
	}
}

func TestWriteArtifactsIncludesMarkdownAndSARIF(t *testing.T) {
	root := t.TempDir()
	writeFixture(t, root, "SECURITY.md", "# Policy\nReport vulns privately.\n", 0o644)
	writeFixture(t, root, "deploy/docker-compose.yml", "services:\n  api:\n    host: 0.0.0.0\n    privileged: true\n", 0o644)

	report, err := Scan(root, DefaultOptions())
	if err != nil {
		t.Fatalf("Scan: %v", err)
	}
	if report.Policy == "" || !strings.Contains(report.Policy, "Report vulns") {
		t.Fatalf("expected SECURITY.md policy on report, got %q", report.Policy)
	}
	artifacts, err := WriteArtifacts(report)
	if err != nil {
		t.Fatalf("WriteArtifacts: %v", err)
	}
	for _, path := range []string{artifacts.Report, artifacts.Markdown, artifacts.SARIF} {
		if path == "" {
			t.Fatal("empty artifact path")
		}
		if _, err := os.Stat(path); err != nil {
			t.Fatalf("missing artifact %s: %v", path, err)
		}
	}
	md, err := os.ReadFile(artifacts.Markdown)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(md), "Security posture report") {
		t.Fatalf("markdown missing title: %s", md[:min(80, len(md))])
	}
	sarifBytes, err := os.ReadFile(artifacts.SARIF)
	if err != nil {
		t.Fatal(err)
	}
	var doc map[string]any
	if err := json.Unmarshal(sarifBytes, &doc); err != nil {
		t.Fatalf("sarif json: %v", err)
	}
	if doc["version"] != "2.1.0" {
		t.Fatalf("sarif version=%v", doc["version"])
	}
}

func TestNewRulesDetectPickleAndShellTrue(t *testing.T) {
	root := t.TempDir()
	writeFixture(t, root, "svc/worker.py", "import pickle\ndata = pickle.loads(user_blob)\n", 0o644)
	writeFixture(t, root, "svc/run.py", "import subprocess\nsubprocess.run(cmd, shell=True)\n", 0o644)

	report, err := Scan(root, DefaultOptions())
	if err != nil {
		t.Fatalf("Scan: %v", err)
	}
	var sawPickle, sawShell bool
	for _, finding := range report.Findings {
		switch finding.RuleID {
		case "code.pickle-loads":
			sawPickle = true
		case "code.shell-true":
			sawShell = true
		}
	}
	if !sawPickle || !sawShell {
		t.Fatalf("expected pickle and shell-true findings, got %#v", report.Findings)
	}
}

func TestRenderSARIFStableRuleIDs(t *testing.T) {
	report := Report{
		Version: ReportVersion,
		Findings: []Finding{
			{ID: "a", RuleID: "code.pickle-loads", Title: "Pickle", Description: "d", Severity: SeverityHigh, RiskScore: 88, Path: "a.py", Line: 2, Priority: "P1"},
		},
	}
	data, err := RenderSARIF(report)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "code.pickle-loads") {
		t.Fatalf("missing rule id: %s", data)
	}
	// Ensure path is repo-relative style.
	if !strings.Contains(string(data), `"uri": "a.py"`) {
		t.Fatalf("missing uri: %s", data)
	}
	_ = filepath.Separator // silence unused if path helpers unused
}
