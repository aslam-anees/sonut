package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/aistatine/sonut/internal/tui"
)

func writeSkillFixture(t *testing.T, dir string, name string, content string) {
	t.Helper()
	skillDir := filepath.Join(dir, name)
	if err := os.MkdirAll(skillDir, 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", skillDir, err)
	}
	if err := os.WriteFile(filepath.Join(skillDir, "SKILL.md"), []byte(content), 0o644); err != nil {
		t.Fatalf("write SKILL.md: %v", err)
	}
}

func TestRunSkillsListText(t *testing.T) {
	dir := t.TempDir()
	writeSkillFixture(t, dir, "confirmation-policy", "---\nname: confirmation-policy\ndescription: Ask before risky actions.\n---\nbody")

	var stdout, stderr bytes.Buffer
	exit := runWithDeps([]string{"skills", "list"}, &stdout, &stderr, appDeps{
		skillsDir: func() string { return dir },
	})
	if exit != 0 {
		t.Fatalf("exit = %d, stderr = %s", exit, stderr.String())
	}
	out := stdout.String()
	for _, want := range []string{"confirmation-policy", "Ask before risky actions."} {
		if !strings.Contains(out, want) {
			t.Fatalf("output missing %q:\n%s", want, out)
		}
	}
}

func TestRunSkillsListWarnsOnDuplicateNames(t *testing.T) {
	dir := t.TempDir()
	// Two directories declare the same frontmatter name; List keeps one and the
	// other is shadowed. The command must warn instead of silently dropping it.
	writeSkillFixture(t, dir, "alpha", "---\nname: shared\ndescription: First.\n---\nbody")
	writeSkillFixture(t, dir, "beta", "---\nname: shared\ndescription: Second.\n---\nbody")

	var stdout, stderr bytes.Buffer
	exit := runWithDeps([]string{"skills", "list"}, &stdout, &stderr, appDeps{
		skillsDir: func() string { return dir },
	})
	if exit != 0 {
		t.Fatalf("exit = %d, stderr = %s", exit, stderr.String())
	}
	if !strings.Contains(stderr.String(), `duplicate skill "shared"`) {
		t.Fatalf("expected a duplicate-skill warning on stderr, got: %q", stderr.String())
	}
}

func TestRunSkillsDefaultsToList(t *testing.T) {
	dir := t.TempDir()
	writeSkillFixture(t, dir, "demo", "body")

	var stdout, stderr bytes.Buffer
	exit := runWithDeps([]string{"skills"}, &stdout, &stderr, appDeps{
		skillsDir: func() string { return dir },
	})
	if exit != 0 {
		t.Fatalf("exit = %d, stderr = %s", exit, stderr.String())
	}
	if !strings.Contains(stdout.String(), "demo") {
		t.Fatalf("output missing demo:\n%s", stdout.String())
	}
}

func TestRunSkillsListJSON(t *testing.T) {
	dir := t.TempDir()
	writeSkillFixture(t, dir, "demo", "---\nname: demo\ndescription: a demo\n---\nbody")

	var stdout, stderr bytes.Buffer
	exit := runWithDeps([]string{"skills", "list", "--json"}, &stdout, &stderr, appDeps{
		skillsDir: func() string { return dir },
	})
	if exit != 0 {
		t.Fatalf("exit = %d, stderr = %s", exit, stderr.String())
	}
	var payload struct {
		Skills []struct {
			Name        string `json:"name"`
			Description string `json:"description"`
			Path        string `json:"path"`
			Content     string `json:"content"`
		} `json:"skills"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &payload); err != nil {
		t.Fatalf("invalid JSON: %v\n%s", err, stdout.String())
	}
	// Bundled skills (Path "builtin:...") ship in the binary and appear in
	// every listing; the disk skill written above must be among them.
	found := false
	for _, skill := range payload.Skills {
		if skill.Name == "demo" {
			found = true
			if skill.Description != "a demo" || skill.Path == "" {
				t.Fatalf("unexpected skill: %#v", skill)
			}
		}
	}
	if !found {
		t.Fatalf("demo skill missing from listing: %#v", payload.Skills)
	}
}

func TestRunSkillsEmptyDir(t *testing.T) {
	var stdout, stderr bytes.Buffer
	exit := runWithDeps([]string{"skills", "list"}, &stdout, &stderr, appDeps{
		skillsDir: func() string { return filepath.Join(t.TempDir(), "missing") },
	})
	if exit != 0 {
		t.Fatalf("exit = %d, stderr = %s", exit, stderr.String())
	}
	// Bundled skills mean a missing dir still lists the built-in set.
	if !strings.Contains(stdout.String(), "frontend-design") {
		t.Fatalf("expected bundled skills listed for empty dir, got:\n%s", stdout.String())
	}
}

func TestRunSkillsDoesNotLaunchTUI(t *testing.T) {
	var stdout, stderr bytes.Buffer
	launchCalled := false
	_ = runWithDeps([]string{"skills"}, &stdout, &stderr, appDeps{
		skillsDir: func() string { return t.TempDir() },
		runTUI: func(ctx context.Context, options tui.Options) int {
			launchCalled = true
			return 0
		},
	})
	if launchCalled {
		t.Fatalf("TUI launcher should not be called for skills command")
	}
}
