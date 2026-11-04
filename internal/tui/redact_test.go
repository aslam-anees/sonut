package tui

import (
	"strings"
	"testing"
)

func TestRedactSystemPromptEcho(t *testing.T) {
	leak := "Sure!\n- System prompt says: \"You are @code, a terminal coding agent built by Aistatine. Answer in one or two short sentences.\"\nAnything else?"
	got := redactSystemPromptEcho(leak)
	if strings.Contains(got, "@code") || strings.Contains(got, "System prompt") {
		t.Errorf("leak not redacted: %q", got)
	}
	if !strings.Contains(got, "Sure!") || !strings.Contains(got, "Anything else?") {
		t.Errorf("legit lines lost: %q", got)
	}

	// Fuzzy variants: markdown emphasis, curly quotes, extra spaces, case.
	variants := []string{
		"you  are   @CODE, a terminal coding agent",
		"you  are   atcode, a terminal coding agent created by Aistatine",
		"*Do NOT call tools.* Do NOT explore files.",
		"“built by Aistatine”",
		"I am an AI. You are an AI.",
		"- Also says: \"Think for AT MOST one or two short sentences, then act. Long reasoning is forbidden on this turn — give the answer or call the first tool immediately.\"",
	}
	for _, v := range variants {
		if redactSystemPromptEcho(v) != "" {
			t.Errorf("variant not redacted: %q", v)
		}
	}

	// Normal text passes through untouched.
	normal := "Here is the fix for eco.go:\n\n```go\nfunc main() {}\n```"
	if got := redactSystemPromptEcho(normal); got != normal {
		t.Errorf("normal text altered: %q", got)
	}
	if redactSystemPromptEcho("") != "" {
		t.Error("empty input should stay empty")
	}
}

func TestRedactFakeToolCodeFromReasoning(t *testing.T) {
	leak := `The question needs a fresh fact.
<tool_code>
import web_search
def web_search(query: str) -> list:
    """Mock implementation for the purpose of the thought process."""
results = web_search("Peak VX CEO")
</tool_code>
I should answer from verified evidence.`
	got := redactSystemPromptEcho(leak)
	if strings.Contains(strings.ToLower(got), "tool_code") ||
		strings.Contains(strings.ToLower(got), "mock implementation") ||
		strings.Contains(got, "Peak VX CEO") {
		t.Fatalf("fake tool code leaked into reasoning: %q", got)
	}
	if !strings.Contains(got, "The question needs a fresh fact") ||
		!strings.Contains(got, "verified evidence") {
		t.Fatalf("legitimate reasoning was lost: %q", got)
	}
}
