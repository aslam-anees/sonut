package sec

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestScanCollectsTypedComposeAssetsAndConfiguredExposure(t *testing.T) {
	root := t.TempDir()
	writeTestFile(t, root, "docker-compose.yml", `services:
  api:
    ports:
      - "127.0.0.1:8080:8080"
    depends_on: [db]
    privileged: true
  db:
    environment:
      - PASSWORD_FILE=/run/secrets/db_password
`)
	report, err := Scan(root, DefaultOptions())
	if err != nil {
		t.Fatal(err)
	}
	if len(report.Assets) < 2 || len(report.Exposures) == 0 {
		t.Fatalf("typed compose extraction missing: assets=%#v exposures=%#v", report.Assets, report.Exposures)
	}
	if report.Exposures[0].State != ExposureConfiguredPublic {
		t.Fatalf("compose exposure state=%q, want configured-public", report.Exposures[0].State)
	}
	for _, edge := range report.Edges {
		if edge.Relation == "depends-on" && (edge.EvidenceID == "" || edge.ObservationType != "configured") {
			t.Fatalf("dependency edge lacks evidence metadata: %#v", edge)
		}
	}
}

func TestScanKubernetesJoinsExplicitIngressServiceAndSelector(t *testing.T) {
	root := t.TempDir()
	writeTestFile(t, root, "k8s/service.yaml", `apiVersion: v1
kind: Service
metadata:
  name: api
  namespace: prod
spec:
  type: LoadBalancer
  selector:
    app: api
`)
	writeTestFile(t, root, "k8s/deployment.yaml", `apiVersion: apps/v1
kind: Deployment
metadata:
  name: api
  namespace: prod
spec:
  template:
    metadata:
      labels:
        app: api
    spec:
      serviceAccountName: api
      containers:
        - name: api
          securityContext:
            privileged: true
`)
	writeTestFile(t, root, "k8s/ingress.yaml", `apiVersion: networking.k8s.io/v1
kind: Ingress
metadata:
  name: public
  namespace: prod
spec:
  rules:
    - http:
        paths:
          - backend:
              service:
                name: api
`)
	report, err := Scan(root, DefaultOptions())
	if err != nil {
		t.Fatal(err)
	}
	hasRoute, hasSelector := false, false
	for _, edge := range report.Edges {
		if edge.Relation == "routes-to" {
			hasRoute = true
		}
		if edge.Relation == "selects" {
			hasSelector = true
		}
		if edge.EvidenceID == "" || edge.Confidence == "" || edge.ObservationType == "" || edge.EvidenceSource == "" {
			t.Fatalf("edge missing evidence contract: %#v", edge)
		}
	}
	if !hasRoute || !hasSelector {
		t.Fatalf("expected explicit ingress and selector joins: edges=%#v", report.Edges)
	}
	for _, exposure := range report.Exposures {
		if exposure.State == ExposureRuntimeObserved {
			t.Fatal("static Kubernetes config must not be labelled runtime-observed")
		}
	}
}

func TestOwnershipLastMatchAndUnownedFallback(t *testing.T) {
	root := t.TempDir()
	writeTestFile(t, root, ".github/CODEOWNERS", `# broad default
* @security
services/** @platform @security
services/api/** @api
`)
	report := Report{Root: root, Findings: []Finding{{ID: "f1", Path: "services/api/main.go", Line: 12, Remediation: "fix", Verification: "test"}, {ID: "f2", Path: "docs/readme.md", Remediation: "fix", Verification: "test"}}}
	applyOwnership(&report)
	if report.Findings[0].Owner != "@api" || report.Findings[1].Owner != "@security" {
		t.Fatalf("unexpected CODEOWNERS resolution: %#v", report.Findings)
	}
	resolver := ownershipResolver{}
	if got := resolver.Resolve("unmatched/file.go"); got != "unowned" {
		t.Fatalf("empty resolver = %q, want unowned", got)
	}
}

func TestThreatIntelDisabledByDefault(t *testing.T) {
	root := t.TempDir()
	writeTestFile(t, root, "go.mod", "module example.com/app\n\nrequire github.com/example/dependency v1.2.3\n")
	report, err := Scan(root, DefaultOptions())
	if err != nil {
		t.Fatal(err)
	}
	if report.ThreatIntel.Status != ThreatIntelDisabled || report.ThreatIntel.Matches != 0 {
		t.Fatalf("default threat intel should be disabled: %#v", report.ThreatIntel)
	}
}

func writeTestFile(t *testing.T, root, relative, content string) {
	t.Helper()
	path := filepath.Join(root, filepath.FromSlash(relative))
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(strings.TrimSpace(content)+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
}
