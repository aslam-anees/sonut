package agent

import (
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/aistatine/sonut/internal/sonutruntime"
)

// Pruning must never touch skill / tool_search results: compaction_preserve
// reconstructs preserved state FROM those bodies, so a pruned placeholder
// would destroy the state compaction exists to carry.
func TestPruneExemptsSkillAndToolSearchResults(t *testing.T) {
	msgs := []sonutruntime.Message{
		toolCallMsg("s1", toolNameSkill),
		bigToolResult("s1", 30000),
		toolCallMsg("t1", toolNameToolSearch),
		bigToolResult("t1", 30000),
		toolCallMsg("r1", "read_file"),
		bigToolResult("r1", 30000),
		// Enough recent tool output to push the older results past the
		// protection window.
		toolCallMsg("r2", "read_file"),
		bigToolResult("r2", 50000),
	}
	out, reclaimed := pruneStaleToolOutput(msgs, 2)
	if reclaimed == 0 {
		t.Fatal("expected the read_file result to be pruned")
	}
	if isPrunedPlaceholder(out[1].Content) {
		t.Fatal("skill result must never be pruned")
	}
	if isPrunedPlaceholder(out[3].Content) {
		t.Fatal("tool_search result must never be pruned")
	}
	if !isPrunedPlaceholder(out[5].Content) {
		t.Fatal("old read_file result should have been pruned")
	}
}

// A pruned placeholder must never be extracted as a skill body (it would
// override the real body carried in a prior summary's preserved-state block).
func TestLoadedSkillsIgnoresPrunedPlaceholder(t *testing.T) {
	msgs := []sonutruntime.Message{
		{
			Role:      sonutruntime.MessageRoleAssistant,
			ToolCalls: []sonutruntime.ToolCall{{ID: "s1", Name: toolNameSkill, Arguments: `{"skill":"deploy"}`}},
		},
		{
			Role:       sonutruntime.MessageRoleTool,
			ToolCallID: "s1",
			Content:    prunedPlaceholder(toolNameSkill, 12345),
		},
	}
	if entries := loadedSkills(msgs); len(entries) != 0 {
		t.Fatalf("pruned placeholder must not become a skill body, got %+v", entries)
	}
	if entries := loadedToolEntriesFromOutput(prunedPlaceholder(toolNameToolSearch, 999)); len(entries) != 0 {
		t.Fatalf("pruned placeholder must not become tool entries, got %+v", entries)
	}
}

// Third-person "unable to" describes the bug being fixed, not an admission —
// it must not downgrade a completed run. First-person variants still must.
func TestSelfReportedIncompletionFirstPersonOnly(t *testing.T) {
	cases := []struct {
		text  string
		admit bool
	}{
		{"Users were unable to log in; I fixed the cookie TTL.", false},
		{"The service was unable to reach the database before my fix.", false},
		{"Fixed without being able to break existing callers.", false},
		{"I was unable to verify the fix.", true},
		{"I'm unable to complete the migration.", true},
	}
	for _, c := range cases {
		got := selfReportedIncompletion(c.text) != ""
		if got != c.admit {
			t.Errorf("selfReportedIncompletion(%q) admitted=%v, want %v", c.text, got, c.admit)
		}
	}
}

// clampEvidence must cut on a rune boundary so evaluator requests stay valid UTF-8.
func TestClampEvidenceKeepsValidUTF8(t *testing.T) {
	long := strings.Repeat("日本語テキスト🎯", 200)
	clamped := clampEvidence(long)
	if !utf8.ValidString(clamped) {
		t.Fatal("clamped evidence is not valid UTF-8")
	}
	if len(clamped) > evaluatorItemClamp+len(" …[truncated]") {
		t.Fatalf("clamp did not shorten: %d bytes", len(clamped))
	}
	if ascii := "short ascii evidence"; clampEvidence(ascii) != ascii {
		t.Fatal("short strings must pass through unchanged")
	}
}
