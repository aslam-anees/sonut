package skills

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestBundledFrontendDesignAvailableFromEmptyDir(t *testing.T) {
	dir := t.TempDir()
	loaded, err := LoadAll(dir)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	var skill Skill
	ok := false
	for _, candidate := range loaded {
		if candidate.Name == "frontend-design" {
			skill, ok = candidate, true
		}
	}
	if !ok {
		t.Fatalf("bundled frontend-design not found; got %d skills", len(loaded))
	}
	if skill.Description == "" || !strings.Contains(skill.Content, "Typography") {
		t.Fatalf("bundled skill missing description or content: %+v", skill.Name)
	}
	if skill.Path != "builtin:frontend-design" {
		t.Fatalf("unexpected bundled path %q", skill.Path)
	}
}

func TestBundledDesignEngAndFrontendVerify(t *testing.T) {
	loaded, err := LoadAll(t.TempDir())
	if err != nil {
		t.Fatalf("LoadAll: %v", err)
	}
	want := map[string]string{
		"design-eng":      "ease-out",
		"frontend-verify": "headful",
		"frontend-design": "Typography",
	}
	found := map[string]bool{}
	for _, skill := range loaded {
		needle, ok := want[skill.Name]
		if !ok {
			continue
		}
		found[skill.Name] = true
		if !strings.Contains(skill.Content, needle) {
			t.Errorf("skill %q missing %q in content", skill.Name, needle)
		}
	}
	for name := range want {
		if !found[name] {
			t.Errorf("bundled skill %q not found", name)
		}
	}
}

func TestDiskSkillShadowsBundled(t *testing.T) {
	dir := t.TempDir()
	skillDir := filepath.Join(dir, "frontend-design")
	if err := os.MkdirAll(skillDir, 0o755); err != nil {
		t.Fatal(err)
	}
	body := "---\nname: frontend-design\ndescription: user override\n---\ncustom body\n"
	if err := os.WriteFile(filepath.Join(skillDir, "SKILL.md"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	loaded, err := LoadAll(dir)
	if err != nil {
		t.Fatalf("LoadAll: %v", err)
	}
	var skill Skill
	ok := false
	for _, candidate := range loaded {
		if candidate.Name == "frontend-design" {
			skill, ok = candidate, true
		}
	}
	if !ok {
		t.Fatal("frontend-design not found")
	}
	if skill.Description != "user override" {
		t.Fatalf("disk skill did not shadow bundled: %q", skill.Description)
	}
}
