package agent

import (
	"strings"
	"testing"

	"github.com/aistatine/sonut/internal/sonutruntime"
)

func TestExtractSkillReferencesIgnoresCommands(t *testing.T) {
	got := ExtractSkillReferences("/build on and /help please")
	if len(got) != 0 {
		t.Fatalf("slash commands must not count as skills: %v", got)
	}
}

func TestExtractSkillReferencesFindsSkills(t *testing.T) {
	got := ExtractSkillReferences("Use /frontend-design and /debugging for this UI bug")
	if len(got) != 2 {
		t.Fatalf("got %v", got)
	}
	if got[0] != "frontend-design" || got[1] != "debugging" {
		t.Fatalf("unexpected order/names: %v", got)
	}
}

func TestMaybeInjectSkillCompositionThreshold(t *testing.T) {
	base := []sonutruntime.Message{
		{Role: sonutruntime.MessageRoleSystem, Content: "sys"},
		{Role: sonutruntime.MessageRoleUser, Content: "do work with /frontend-design only"},
	}
	out := maybeInjectSkillComposition(base, base[1].Content)
	if len(out) != len(base) {
		t.Fatalf("single skill must not inject, got %d messages", len(out))
	}

	prompt := "combine /frontend-design and /debugging please"
	base[1].Content = prompt
	out = maybeInjectSkillComposition(base, prompt)
	if len(out) != len(base)+1 {
		t.Fatalf("two skills should inject one reminder, got %d", len(out))
	}
	if !strings.Contains(out[len(out)-1].Content, "skill_composition_reminder") {
		t.Fatalf("missing reminder: %q", out[len(out)-1].Content)
	}
	if !strings.Contains(out[len(out)-1].Content, "frontend-design") {
		t.Fatalf("reminder should name skills: %q", out[len(out)-1].Content)
	}
}
