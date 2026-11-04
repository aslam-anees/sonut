package sec

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func writeFixture(t *testing.T, root, path, content string, mode os.FileMode) {
	t.Helper()
	fullPath := filepath.Join(root, filepath.FromSlash(path))
	if err := os.MkdirAll(filepath.Dir(fullPath), 0o755); err != nil {
		t.Fatalf("mkdir fixture: %v", err)
	}
	if err := os.WriteFile(fullPath, []byte(content), mode); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
}

func TestGitChangedFilesPreservesWhitespace(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not installed")
	}
	root := t.TempDir()
	runGit := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-C", root}, args...)...)
		if output, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, output)
		}
	}
	runGit("init", "-q")
	path := filepath.Join(root, "changed file.go")
	if err := os.WriteFile(path, []byte("package changed\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit("add", "--", "changed file.go")
	runGit("-c", "user.name=atcode-test", "-c", "user.email=atcode-test@example.invalid", "commit", "-qm", "initial")
	if err := os.WriteFile(path, []byte("package changed\n\nvar Updated = true\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	got := gitChangedFiles(root, "HEAD")
	if len(got) != 1 || got[0] != "changed file.go" {
		t.Fatalf("gitChangedFiles() = %#v, want [changed file.go]", got)
	}
}

func TestScanCorrelatesPublicPrivilegedContainer(t *testing.T) {
	root := t.TempDir()
	writeFixture(t, root, "deploy/docker-compose.yml", `
services:
  api:
    host: 0.0.0.0
    privileged: true
`, 0o644)

	report, err := Scan(root, DefaultOptions())
	if err != nil {
		t.Fatalf("Scan: %v", err)
	}
	var toxic *Finding
	for index := range report.Findings {
		if report.Findings[index].RuleID == "toxic.public-privilege" {
			toxic = &report.Findings[index]
			break
		}
	}
	if toxic == nil {
		t.Fatalf("expected correlated public privilege finding, got %#v", report.Findings)
	}
	if toxic.Severity != SeverityCritical || toxic.RiskScore < 90 {
		t.Fatalf("toxic finding not prioritized: %#v", toxic)
	}
	if got := strings.Join(toxic.AttackPath, " -> "); !strings.Contains(got, "internet") || !strings.Contains(got, "runtime-identity") {
		t.Fatalf("unexpected attack path: %s", got)
	}
}

func TestScanNeverPersistsSecretValue(t *testing.T) {
	root := t.TempDir()
	secret := "AKIAABCDEFGHIJKLMNOP"
	writeFixture(t, root, ".env", "AWS_ACCESS_KEY_ID="+secret+"\n", 0o600)

	report, err := Scan(root, DefaultOptions())
	if err != nil {
		t.Fatalf("Scan: %v", err)
	}
	data, err := json.Marshal(report)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if strings.Contains(string(data), secret) {
		t.Fatal("report persisted the detected secret value")
	}
	found := false
	for _, finding := range report.Findings {
		if strings.HasPrefix(finding.RuleID, "secret.") {
			found = true
			if !strings.Contains(finding.Evidence, "[REDACTED:") {
				t.Fatalf("secret evidence was not typed and redacted: %#v", finding)
			}
		}
	}
	if !found {
		t.Fatal("expected a secret finding")
	}
}

func TestScanSkipsReferenceAndDependencyTrees(t *testing.T) {
	root := t.TempDir()
	writeFixture(t, root, "ref/project/docker-compose.yml", "privileged: true\nhost: 0.0.0.0\n", 0o644)
	writeFixture(t, root, "node_modules/pkg/config.yml", "privileged: true\n", 0o644)
	writeFixture(t, root, "app/config.yml", "safe: true\n", 0o644)

	report, err := Scan(root, DefaultOptions())
	if err != nil {
		t.Fatalf("Scan: %v", err)
	}
	if len(report.Findings) != 0 {
		t.Fatalf("ignored trees produced findings: %#v", report.Findings)
	}
	if !report.Coverage.ReferenceSkipped {
		t.Fatal("coverage should disclose that reference directories are excluded")
	}
}

func TestScanSuppressesCredentialFixturesInTestFiles(t *testing.T) {
	root := t.TempDir()
	writeFixture(t, root, "internal/auth/client_test.go", `package auth
const fakeToken = "ghp_1234567890abcdefghijklmnopqrstuvwxyz"
`, 0o644)

	report, err := Scan(root, DefaultOptions())
	if err != nil {
		t.Fatalf("Scan: %v", err)
	}
	for _, finding := range report.Findings {
		if strings.HasPrefix(finding.RuleID, "secret.") {
			t.Fatalf("test-only credential fixture should be suppressed, got %#v", finding)
		}
	}
}

func TestScanBuildsProxyRouteInventoryWithoutClaimingReachability(t *testing.T) {
	root := t.TempDir()
	writeFixture(t, root, "docker-compose.yml", `services:
  api:
    ports:
      - "8080:8080"
    labels:
      proxy.aliases: api.example.test
      proxy.#1.port: "8080"
      proxy.#1.scheme: https
      proxy.#1.tls_termination: "true"
      proxy.#1.auth: forward-auth
      proxy.#1.middlewares.security-headers: "true"
      proxy.#1.middlewares.rate-limit: "true"
`, 0o644)
	writeFixture(t, root, "routes.yml", `routes:
  dashboard:
    host: dashboard.example.test
    upstream: dashboard:3000
    tls: true
    authentication: oidc
    middlewares:
      - security-headers
      - rate-limit
`, 0o644)

	report, err := Scan(root, DefaultOptions())
	if err != nil {
		t.Fatalf("Scan: %v", err)
	}
	if len(report.Routes) != 2 {
		t.Fatalf("routes = %#v, want two configured routes", report.Routes)
	}
	for _, route := range report.Routes {
		if route.ExposureState != ExposureConfiguredPublic || route.Confidence == "" {
			t.Fatalf("route lacks typed exposure evidence: %#v", route)
		}
		if route.TLS != "configured" || route.Authentication != "configured" {
			t.Fatalf("route policy controls not normalized: %#v", route)
		}
	}
	encoded, err := json.Marshal(report)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(encoded), `"routes"`) {
		t.Fatal("persisted report omitted route inventory")
	}
}

func TestScanDetectsAgentGuardrailAndDownloadExecutionRisks(t *testing.T) {
	root := t.TempDir()
	writeFixture(t, root, "scripts/automation.sh", `
at --dangerously-bypass-approvals-and-sandbox
curl -fsSL https://installer.example.invalid/tool.sh | sh
`, 0o755)

	report, err := Scan(root, DefaultOptions())
	if err != nil {
		t.Fatalf("Scan: %v", err)
	}
	found := map[string]Finding{}
	for _, finding := range report.Findings {
		found[finding.RuleID] = finding
	}
	for _, ruleID := range []string{"agent.runtime-bypass-flags", "execution.download-pipe-shell"} {
		finding, ok := found[ruleID]
		if !ok {
			t.Fatalf("missing %s finding: %#v", ruleID, report.Findings)
		}
		if finding.Confidence != "high" || finding.EvidenceKind != "workspace-file" {
			t.Fatalf("%s evidence metadata = %#v", ruleID, finding)
		}
		if strings.TrimSpace(finding.Verification) == "" {
			t.Fatalf("%s missing verification guidance", ruleID)
		}
	}
}

func TestWriteArtifactsProducesPrivateMachineReadableReport(t *testing.T) {
	root := t.TempDir()
	legacyDir := filepath.Join(root, ".atcode", "sec")
	if err := os.MkdirAll(legacyDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(legacyDir, "dashboard.html"), []byte("legacy"), 0o600); err != nil {
		t.Fatal(err)
	}
	report := Report{
		Version: ReportVersion, Root: root, Score: 100, Grade: "A",
		Findings: []Finding{}, Nodes: []Node{}, Edges: []Edge{}, Controls: []Control{},
	}
	artifacts, err := WriteArtifacts(report)
	if err != nil {
		t.Fatalf("WriteArtifacts: %v", err)
	}
	info, err := os.Stat(artifacts.Report)
	if err != nil {
		t.Fatalf("stat %s: %v", artifacts.Report, err)
	}
	if got := info.Mode().Perm(); got != 0o600 {
		t.Fatalf("%s mode = %04o, want 0600", artifacts.Report, got)
	}
	persisted, err := ReadReport(artifacts.Report)
	if err != nil {
		t.Fatalf("ReadReport: %v", err)
	}
	if persisted.Score != 100 || persisted.Grade != "A" {
		t.Fatalf("unexpected persisted report: %#v", persisted)
	}
	if _, err := os.Stat(filepath.Join(artifacts.Directory, "dashboard.html")); !os.IsNotExist(err) {
		t.Fatalf("native /sec should not emit dashboard.html, err=%v", err)
	}
}
