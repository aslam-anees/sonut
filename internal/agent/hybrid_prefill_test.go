package agent

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/aistatine/sonut/internal/sonutruntime"
)

func TestIsLocalHybridRun(t *testing.T) {
	cases := []struct {
		provider, model string
		want            bool
	}{
		{"llama", "", true},
		{"local", "", true},
		{"openai", "atcode-v1", true},
		{"openai", "Bonsai-27B", true},
		{"openai", "gpt-4o", false},
		{"", "", false},
	}
	for _, tc := range cases {
		got := isLocalHybridRun(Options{ProviderName: tc.provider, Model: tc.model})
		if got != tc.want {
			t.Errorf("isLocalHybridRun(%q,%q)=%v want %v", tc.provider, tc.model, got, tc.want)
		}
	}
}

func TestPrepareLocalHybridFollowUpStripsImagesAndClamps(t *testing.T) {
	big := strings.Repeat("x", localToolResultMaxBytes+4096)
	// More tool results than localKeepLastToolResults so the oldest is pruned.
	msgs := []sonutruntime.Message{
		{Role: sonutruntime.MessageRoleSystem, Content: "sys"},
		{
			Role:    sonutruntime.MessageRoleUser,
			Content: "make this look like the screenshot",
			Images:  []sonutruntime.ImageBlock{{MediaType: "image/png", Data: []byte{1, 2, 3, 4}}},
		},
		{Role: sonutruntime.MessageRoleAssistant, Content: "", ToolCalls: []ToolCall{
			{ID: "c1", Name: "read_file"},
			{ID: "c2", Name: "read_file"},
			{ID: "c3", Name: "read_file"},
			{ID: "c4", Name: "read_file"},
		}},
		{Role: sonutruntime.MessageRoleTool, Content: big, ToolCallID: "c1"},
		{Role: sonutruntime.MessageRoleTool, Content: big, ToolCallID: "c2"},
		{Role: sonutruntime.MessageRoleTool, Content: big, ToolCallID: "c3"},
		{Role: sonutruntime.MessageRoleTool, Content: big, ToolCallID: "c4"},
	}
	out := prepareLocalHybridFollowUp(msgs)
	// Source untouched.
	if len(msgs[1].Images) != 1 {
		t.Fatal("prepare must not mutate caller's images")
	}
	// Find the (possibly compacted) user turn that carried the image.
	userIdx := -1
	for i, m := range out {
		if m.Role == sonutruntime.MessageRoleUser && strings.Contains(m.Content, "screenshot") {
			userIdx = i
			break
		}
	}
	if userIdx < 0 {
		t.Fatal("user turn missing after hybrid prepare")
	}
	if len(out[userIdx].Images) != 0 {
		t.Fatal("follow-up request must drop images (CPU mmproj re-encode)")
	}
	if !strings.Contains(out[userIdx].Content, "image attachment(s) WERE shown earlier") &&
		!strings.Contains(out[userIdx].Content, "image attachment(s) were shown earlier") {
		t.Fatalf("expected image-memory note, got %q", out[userIdx].Content)
	}
	// Collect tool bodies that remain on the wire after exchange squash + keep-last.
	var toolBodies []string
	for _, m := range out {
		if m.Role == sonutruntime.MessageRoleTool {
			toolBodies = append(toolBodies, m.Content)
		}
	}
	if len(toolBodies) == 0 {
		t.Fatal("expected at least one tool body to remain")
	}
	// At most keep-last full bodies; older ones are placeholders or dropped by squash.
	full := 0
	for _, body := range toolBodies {
		if !isPrunedPlaceholder(body) {
			full++
		}
	}
	if full > localKeepLastToolResults {
		t.Fatalf("kept %d full tool bodies, want <= keep-last=%d", full, localKeepLastToolResults)
	}
	newest := toolBodies[len(toolBodies)-1]
	if isPrunedPlaceholder(newest) {
		t.Fatal("newest tool body must not be fully pruned")
	}
	if len(newest) > localToolResultMaxBytes+200 {
		t.Fatalf("tool body still oversized: %d", len(newest))
	}
	if !strings.Contains(newest, "truncated for local prefill speed") {
		tail := newest
		if len(tail) > 80 {
			tail = tail[len(tail)-80:]
		}
		t.Fatalf("expected clamp marker, got %q", tail)
	}
}

func TestRequestMessagesForTurnLeanOnlyWhenActFastLocal(t *testing.T) {
	msgs := []sonutruntime.Message{
		{Role: sonutruntime.MessageRoleSystem, Content: "sys"},
		{
			Role:    sonutruntime.MessageRoleUser,
			Content: "hi",
			Images:  []sonutruntime.ImageBlock{{MediaType: "image/png", Data: []byte{9}}},
		},
	}
	opts := Options{ProviderName: "llama", Model: "atcode-v1"}
	// Pre-tool turns (including plan-seeded executor): keep images.
	kept := requestMessagesForTurn(msgs, newGuardState(), opts, false)
	if len(kept[1].Images) != 1 {
		t.Fatal("pre-tool turn must keep images")
	}
	// Post-tool follow-up: strip.
	lean := requestMessagesForTurn(msgs, newGuardState(), opts, true)
	if len(lean[1].Images) != 0 {
		t.Fatal("post-tool local hybrid must strip images")
	}
}

func TestPrepareLocalHybridFollowUpBoundsOldToolExchangeHistory(t *testing.T) {
	msgs := []sonutruntime.Message{
		{Role: sonutruntime.MessageRoleSystem, Content: "sys"},
		{Role: sonutruntime.MessageRoleUser, Content: "build"},
	}
	for i, name := range []string{"list_directory", "read_file", "write_file", "append_file", "read_file"} {
		id := fmt.Sprintf("call-%d", i)
		args := fmt.Sprintf(`{"path":"game.js","content":%q}`, strings.Repeat("source", 700))
		msgs = append(msgs,
			sonutruntime.Message{Role: sonutruntime.MessageRoleAssistant, ToolCalls: []ToolCall{{ID: id, Name: name, Arguments: args}}},
			sonutruntime.Message{Role: sonutruntime.MessageRoleTool, ToolCallID: id, Content: strings.Repeat("result", 500)},
		)
	}

	beforeBytes := localRequestPayloadBytes(msgs)
	out := prepareLocalHybridFollowUp(msgs)
	afterBytes := localRequestPayloadBytes(out)
	toolResults := 0
	toolCalls := 0
	for _, message := range out {
		if message.Role == sonutruntime.MessageRoleTool {
			toolResults++
		}
		if message.Role == sonutruntime.MessageRoleAssistant {
			toolCalls += len(message.ToolCalls)
		}
	}
	if toolResults != localKeepRecentToolExchanges || toolCalls != localKeepRecentToolExchanges {
		t.Fatalf("bounded exchanges: tool calls=%d results=%d, want %d each", toolCalls, toolResults, localKeepRecentToolExchanges)
	}
	if len(out) >= len(msgs) {
		t.Fatalf("history was not compacted: before=%d after=%d", len(msgs), len(out))
	}
	foundSummary := false
	for _, message := range out {
		if strings.Contains(message.Content, "Earlier completed tool exchanges were compacted") {
			foundSummary = true
		}
	}
	if !foundSummary {
		t.Fatal("missing compacted-exchange summary")
	}
	if afterBytes*2 >= beforeBytes {
		t.Fatalf("local replay was not reduced by at least 50%%: before=%d after=%d", beforeBytes, afterBytes)
	}
	t.Logf("bounded local replay payload: %d -> %d bytes (%.1f%% reduction)",
		beforeBytes, afterBytes, 100*(1-float64(afterBytes)/float64(beforeBytes)))
}

func TestCompactLocalToolCallArgumentsOmitsAppliedBodies(t *testing.T) {
	body := strings.Repeat("const x = 1;\n", 100)
	encoded, err := json.Marshal(map[string]any{"path": "game.js", "content": body, "overwrite": true})
	if err != nil {
		t.Fatal(err)
	}
	msgs := []sonutruntime.Message{{
		Role: sonutruntime.MessageRoleAssistant,
		ToolCalls: []ToolCall{{
			ID: "write-1", Name: "write_file", Arguments: string(encoded),
		}},
	}}
	out := compactLocalToolCallArguments(copyMessages(msgs))
	args := out[0].ToolCalls[0].Arguments
	if len(args) >= len(encoded)/2 {
		t.Fatalf("write body was not compacted: before=%d after=%d", len(encoded), len(args))
	}
	if !strings.Contains(args, `"path":"game.js"`) || !strings.Contains(args, `"overwrite":true`) {
		t.Fatalf("control arguments were lost: %s", args)
	}
	if strings.Contains(args, "const x = 1") {
		t.Fatal("already-applied source body remained in replay arguments")
	}
	// Durable source message must remain byte-identical.
	if msgs[0].ToolCalls[0].Arguments != string(encoded) {
		t.Fatal("compaction mutated the durable transcript")
	}
}

func localRequestPayloadBytes(messages []sonutruntime.Message) int {
	total := 0
	for _, message := range messages {
		total += len(message.Content)
		for _, call := range message.ToolCalls {
			total += len(call.Name) + len(call.Arguments)
		}
	}
	return total
}
