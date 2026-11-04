package sessions

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
)

// A tool-heavy turn must not push the assistant's latest decisions or the
// current plan out of the continuation context (the stale-approach bug: the
// continued run kept working on an API the previous turn had replaced).
func TestFormatExecPromptKeepsNarrativeOverToolNoise(t *testing.T) {
	var events []Event
	seq := 0
	add := func(eventType EventType, payload string) {
		seq++
		events = append(events, Event{Sequence: seq, Type: eventType, Payload: json.RawMessage(payload)})
	}

	add(EventMessage, `{"role":"user","content":"get AAPL stock data"}`)
	add(EventMessage, `{"role":"assistant","content":"yahoo api is down, switching to duckduckgo which works"}`)
	add(EventToolCall, `{"name":"update_plan","arguments":"{\"plan\":[{\"content\":\"fetch via duckduckgo\",\"status\":\"in_progress\"}]}"}`)
	add(EventToolCall, `{"name":"read_file","arguments":"{\"path\":\"src/app.go\"}"}`)
	// 30 trailing tool events — more than any flat last-N window.
	for i := 0; i < 30; i++ {
		add(EventToolCall, fmt.Sprintf(`{"name":"exec_command","arguments":"{\"command\":\"curl step %d\"}"}`, i))
		add(EventToolResult, fmt.Sprintf(`{"output":"result %d"}`, i))
	}

	out := FormatExecPrompt("continue", PreparedExec{
		Mode:          ModeResume,
		Session:       Metadata{SessionID: "s1"},
		ContextEvents: events,
	})

	if !strings.Contains(out, "duckduckgo which works") {
		t.Fatal("assistant decision was pushed out of the continuation context")
	}
	if !strings.Contains(out, "update_plan") {
		t.Fatal("latest plan state missing from continuation context")
	}
	if !strings.Contains(out, "get AAPL stock data") {
		t.Fatal("original user ask missing from continuation context")
	}
	if !strings.Contains(out, "Known workspace paths") || !strings.Contains(out, "src/app.go") {
		t.Fatalf("known workspace path missing from continuation context: %q", out)
	}
	if strings.Contains(out, "curl step 5") {
		t.Fatal("old tool noise should not crowd the context (only a small recent tail)")
	}
	if !strings.Contains(out, "curl step 29") {
		t.Fatal("most recent tool activity should still be present")
	}
}

func TestConversationHistoryPreservesFollowUpTurns(t *testing.T) {
	events := []Event{
		{Sequence: 1, Type: EventMessage, Payload: json.RawMessage(`{"role":"user","content":"how many r in strawberry"}`)},
		{Sequence: 2, Type: EventMessage, Payload: json.RawMessage(`{"role":"assistant","content":"There are 3 r's in strawberry."}`)},
		{Sequence: 3, Type: EventMessage, Payload: json.RawMessage(`{"role":"user","content":"which position"}`)},
	}

	history := ConversationHistory(events[:2])
	if len(history) != 2 {
		t.Fatalf("history length = %d, want 2: %#v", len(history), history)
	}
	if history[0].Role != "user" || history[0].Content != "how many r in strawberry" {
		t.Fatalf("first history turn = %#v", history[0])
	}
	if history[1].Role != "assistant" || !strings.Contains(history[1].Content, "3 r") {
		t.Fatalf("assistant history turn = %#v", history[1])
	}
	if strings.Contains(history[0].Content, "which position") || strings.Contains(history[1].Content, "which position") {
		t.Fatal("current follow-up must not be copied into prior history")
	}
}

func TestConversationHistoryKeepsCompactionSummary(t *testing.T) {
	events := []Event{
		{Sequence: 1, Type: EventCompaction, Payload: json.RawMessage(`{"summary":"The user is working on the strawberry letter count."}`)},
	}
	history := ConversationHistory(events)
	if len(history) != 1 || history[0].Role != "assistant" || !strings.Contains(history[0].Content, "strawberry") {
		t.Fatalf("compaction history = %#v", history)
	}
}

func TestConversationHistoryRecoversLegacyUnroledMessages(t *testing.T) {
	events := []Event{
		{Sequence: 1, Type: EventMessage, Payload: json.RawMessage(`{"content":"first question"}`)},
		{Sequence: 2, Type: EventMessage, Payload: json.RawMessage(`{"content":"first answer"}`)},
	}
	history := ConversationHistory(events)
	if len(history) != 2 || history[0].Role != "user" || history[1].Role != "assistant" {
		t.Fatalf("legacy history roles = %#v", history)
	}
}
