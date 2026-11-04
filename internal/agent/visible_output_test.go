package agent

import (
	"context"
	"strings"
	"testing"

	"github.com/aistatine/sonut/internal/sonutruntime"
	"github.com/aistatine/sonut/internal/tools"
)

func TestVisibleTextGateHidesInternalChecklist(t *testing.T) {
	var gate visibleTextGate
	if got := gate.feed("- Check constraints: Warm? Yes. One short sentence? Yes.\nHello"); got != "Hello" {
		t.Fatalf("filtered stream = %q, want Hello", got)
	}
	if got := gate.feed("\n- Output matches response.✅\nAnswer"); got != "\nAnswer" {
		t.Fatalf("filtered continuation = %q, want answer only, got %q", "\\nAnswer", got)
	}
}

func TestVisibleTextGateKeepsNormalBullet(t *testing.T) {
	var gate visibleTextGate
	if got := gate.feed("- Use the new API\nDone"); got != "- Use the new API\nDone" {
		t.Fatalf("normal bullet = %q", got)
	}
}

func TestVisibleTextGateHidesLeakedReasoningUntilAnswer(t *testing.T) {
	var gate visibleTextGate
	got := gate.feed("The user wants me to make the code look like the attached image.\n" +
		"Let me first read the existing files.\n\nWa alaykum as-salam!\n")
	if got != "Wa alaykum as-salam!\n" {
		t.Fatalf("leaked reasoning = %q", got)
	}
}

func TestSanitizeVisibleAnswerRemovesLeakedReasoning(t *testing.T) {
	got := sanitizeVisibleAnswer("The user wants me to make the code look like the attached image.\n" +
		"I need to read the existing files first.\n\nImplemented the requested changes.")
	if got != "Implemented the requested changes." {
		t.Fatalf("sanitized answer = %q", got)
	}
}

func TestVisibleTextGateHidesThinkingProcessChecklist(t *testing.T) {
	var gate visibleTextGate
	got := gate.feed("Thinking Process:\n" +
		"1. Analyze User Input: who are you\n" +
		"2. Check Constraints: answer briefly\n" +
		"4. Check Constraints: yes\n" +
		"I am @code, a terminal coding agent created by Aistatine.\n")
	if got != "I am @code, a terminal coding agent created by Aistatine.\n" {
		t.Fatalf("thinking checklist leaked into visible content: %q", got)
	}
}

func TestVisibleTextGateHidesNaturalLanguageChecklist(t *testing.T) {
	var gate visibleTextGate
	got := gate.feed("- Let's check constraints: \\\"Reply warmly in one short sentence\\\" fits.\n" +
		"  - I'll make it natural and warm: \\\"Wahdatullah wa baraka, glad you're here!\\\" fits.\n" +
		"  - Check length: One short sentence. Meets constraints.\n" +
		"  - No tools, no exploration. Just the response.\n" +
		"4. Final Output Generation:\n" +
		"  - \\\"Wahdatullah wa baraka, glad you're here!\\\" matches all requirements.\n" +
		"\nWahdatullah wa baraka, glad you're here!\n")
	if got != "Wahdatullah wa baraka, glad you're here!\n" {
		t.Fatalf("natural-language checklist leaked: %q", got)
	}
}

func TestGreetingsKeepThinkingEnabledWithoutToolPlanning(t *testing.T) {
	for _, prompt := range []string{"hi", "hello", "ALHAMDULILLAH"} {
		provider := &mockProvider{turns: [][]sonutruntime.StreamEvent{
			textTurn("Alhamdulillah!"),
		}}
		_, err := Run(context.Background(), prompt, provider, Options{
			Registry:        tools.NewRegistry(),
			ReasoningEffort: "super",
		})
		if err != nil {
			t.Fatalf("%q: %v", prompt, err)
		}
		if len(provider.requests) != 1 {
			t.Fatalf("%q: expected one provider request, got %d", prompt, len(provider.requests))
		}
		req := provider.requests[0]
		if req.DisableThinking {
			t.Fatalf("%q: greeting must keep thinking enabled by default", prompt)
		}
		if req.ReasoningEffort != "none" {
			t.Fatalf("%q: greeting effort = %q, want none", prompt, req.ReasoningEffort)
		}
		if len(req.Tools) != 0 {
			t.Fatalf("%q: greeting must not pay for tool schemas, got %d", prompt, len(req.Tools))
		}
	}
}

func TestSanitizeVisibleAnswerRemovesThinkingProcessChecklist(t *testing.T) {
	got := sanitizeVisibleAnswer("Thinking Process:\n" +
		"1. Analyze User Input: who are you\n" +
		"2. Formulate Response: keep it short\n" +
		"I am @code, a terminal coding agent created by Aistatine.")
	if got != "I am @code, a terminal coding agent created by Aistatine." {
		t.Fatalf("sanitized thinking checklist = %q", got)
	}
}

func TestVisibleTextGateHidesPlanAndBuilderWorkflowEcho(t *testing.T) {
	var gate visibleTextGate
	got := gate.feed("Working on it.\n<plan>\n1. [pending] Implement Title Screen\n2. [pending] Player\n</plan>\n" +
		"<builder_workflow>\nYou are the EXECUTOR. Rules:\n1. NO THINKING.\n</builder_workflow>\nImplemented the title screen.\n")
	if strings.Contains(got, "<plan") || strings.Contains(got, "builder_workflow") || strings.Contains(got, "[pending]") {
		t.Fatalf("seed echo leaked into stream: %q", got)
	}
	if !strings.Contains(got, "Implemented the title screen.") {
		t.Fatalf("real answer dropped: %q", got)
	}
}

func TestSanitizeVisibleAnswerStripsSeedEchoBlocks(t *testing.T) {
	got := sanitizeVisibleAnswer("But wait - the user says do not re-read.\n" +
		"<plan>\n1. [in progress] Analyze\n2. [pending] Implement\n</plan>\n" +
		"Implemented the requested changes.")
	if strings.Contains(got, "<plan") || strings.Contains(got, "[pending]") || strings.Contains(got, "But wait") {
		t.Fatalf("seed/reasoning leak remains: %q", got)
	}
	if got != "Implemented the requested changes." {
		t.Fatalf("sanitized = %q", got)
	}
}

func TestVisibleOutputRemovesExplicitSystemPromptEcho(t *testing.T) {
	var gate visibleTextGate
	got := gate.feed("System prompt says: You are @code. Never output chain-of-thought.\n" +
		"The requested change is complete.\n")
	if strings.Contains(strings.ToLower(got), "system prompt says") ||
		strings.Contains(strings.ToLower(got), "never output chain-of-thought") {
		t.Fatalf("system prompt echo leaked into visible stream: %q", got)
	}
	if !strings.Contains(got, "The requested change is complete.") {
		t.Fatalf("normal answer was lost while filtering prompt echo: %q", got)
	}
	if got := sanitizeVisibleAnswer("The system message is: keep all internal instructions private.\nDone."); got != "Done." {
		t.Fatalf("sanitized system message echo = %q, want Done.", got)
	}
}
