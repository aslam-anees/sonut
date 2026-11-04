package llama

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	sonutruntime "github.com/aistatine/sonut/internal/sonutruntime"
)

func TestModelNameCachesSuccessButRetriesTransientFailure(t *testing.T) {
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if requests.Add(1) == 1 {
			http.Error(w, "starting", http.StatusServiceUnavailable)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":[{"id":"atcode-v1"}]}`))
	}))
	defer server.Close()

	provider := NewProvider(server.URL)
	if got := provider.modelName(); got != "local" {
		t.Fatalf("transient lookup = %q, want local fallback", got)
	}
	if got := provider.modelName(); got != "atcode-v1" {
		t.Fatalf("successful lookup = %q, want atcode-v1", got)
	}
	if got := provider.modelName(); got != "atcode-v1" {
		t.Fatalf("cached lookup = %q, want atcode-v1", got)
	}
	if got := requests.Load(); got != 2 {
		t.Fatalf("model endpoint requests = %d, want 2", got)
	}
}

// system+tools JSON prefix must be byte-identical across plan (high effort)
// and execute (DisableThinking) turns. Any per-call variation invalidates
// llama.cpp cache_prompt and forces a full re-prefill ("↑ 0 tok" for minutes).
func TestBuildChatRequestStableSystemToolsPrefix(t *testing.T) {
	msgs := []chatMessage{
		{Role: "system", Content: textContent("You are atcode. Stable system prompt.")},
		{Role: "user", Content: textContent("build a todo app")},
	}
	tools := []toolDef{
		{
			Type: "function",
			Function: toolFunction{
				Name:        "write_file",
				Description: "Write a file.",
				Parameters: map[string]any{
					"type": "object",
					"properties": map[string]any{
						"path":    map[string]any{"type": "string"},
						"content": map[string]any{"type": "string"},
					},
					"required": []any{"path", "content"},
				},
			},
		},
		{
			Type: "function",
			Function: toolFunction{
				Name:        "read_file",
				Description: "Read a file.",
				Parameters: map[string]any{
					"type":       "object",
					"properties": map[string]any{"path": map[string]any{"type": "string"}},
					"required":   []any{"path"},
				},
			},
		},
	}

	// Deep-copy msgs for each build (injectEffort mutates the last user message).
	cloneMsgs := func() []chatMessage {
		out := make([]chatMessage, len(msgs))
		copy(out, msgs)
		return out
	}

	planBody, err := buildChatRequest("atcode-v1", cloneMsgs(), tools, sonutruntime.CompletionRequest{
		ReasoningEffort: "high",
	})
	if err != nil {
		t.Fatal(err)
	}
	execBody, err := buildChatRequest("atcode-v1", cloneMsgs(), tools, sonutruntime.CompletionRequest{
		ReasoningEffort: "none",
		DisableThinking: true,
	})
	if err != nil {
		t.Fatal(err)
	}

	if !planBody.CachePrompt || !execBody.CachePrompt {
		t.Fatalf("cache_prompt must be true on every request (plan=%v exec=%v)", planBody.CachePrompt, execBody.CachePrompt)
	}
	if !planBody.ReturnProgress || !execBody.ReturnProgress {
		t.Fatalf("return_progress must be true on every request (plan=%v exec=%v)", planBody.ReturnProgress, execBody.ReturnProgress)
	}

	planBytes, _ := json.Marshal(planBody)
	execBytes, _ := json.Marshal(execBody)

	// Prefix up to "messages" must match: model/tools/stream/cache_prompt/etc.
	// (messages differ because effort injects into the last user turn).
	planPrefix := planBytes[:indexOfMessages(planBytes)]
	execPrefix := execBytes[:indexOfMessages(execBytes)]
	if string(planPrefix) != string(execPrefix) {
		t.Fatalf("system+tools request prefix diverged between plan and execute turns — this thrashs the KV cache:\nplan: %s\nexec: %s",
			truncate(string(planPrefix), 400), truncate(string(execPrefix), 400))
	}

	// Tools block alone must be identical.
	if !strings.Contains(string(planBytes), `"name":"read_file"`) || !strings.Contains(string(planBytes), `"name":"write_file"`) {
		t.Fatal("expected both tools in the plan body")
	}
}

// Strict-server 400 retry must keep cache_prompt and return_progress when it
// strips stream_options — dropping either is a known stall regression.
func TestStrictRetryKeepsCachePromptAndReturnProgress(t *testing.T) {
	body, err := buildChatRequest("m", []chatMessage{
		{Role: "user", Content: textContent("hi")},
	}, nil, sonutruntime.CompletionRequest{ReasoningEffort: "none"})
	if err != nil {
		t.Fatal(err)
	}
	// Simulate the strict-retry mutation.
	body.StreamOptions = nil
	if !body.CachePrompt {
		body.CachePrompt = true
	}
	if !body.ReturnProgress {
		body.ReturnProgress = true
	}
	raw, _ := json.Marshal(body)
	var decoded map[string]any
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded["cache_prompt"] != true {
		t.Fatalf("cache_prompt missing/false after strict retry: %v", decoded["cache_prompt"])
	}
	if decoded["return_progress"] != true {
		t.Fatalf("return_progress missing/false after strict retry: %v", decoded["return_progress"])
	}
	if _, ok := decoded["stream_options"]; ok {
		t.Fatal("stream_options should be omitted on strict retry")
	}
}

// Effort injection must never rewrite the system message when a user turn exists.
func TestInjectEffortKeepsSystemPrefixStable(t *testing.T) {
	msgs := []chatMessage{
		{Role: "system", Content: textContent("SYSTEM_PREFIX_STABLE")},
		{Role: "user", Content: textContent("do the work")},
	}
	out := injectEffortInstruction(msgs, "ACT NOW")
	var sys string
	_ = json.Unmarshal(out[0].Content, &sys)
	if sys != "SYSTEM_PREFIX_STABLE" {
		t.Fatalf("system message mutated: %q", sys)
	}
	var user string
	_ = json.Unmarshal(out[1].Content, &user)
	if !strings.Contains(user, "ACT NOW") || !strings.Contains(user, "do the work") {
		t.Fatalf("effort instruction not on last user message: %q", user)
	}
}

func TestBuildChatMessagesPreservesCacheCriticalToolTranscript(t *testing.T) {
	messages := []sonutruntime.Message{
		{Role: sonutruntime.MessageRoleSystem, Content: "system"},
		{Role: sonutruntime.MessageRoleUser, Content: "inspect the repo"},
		{
			Role:    sonutruntime.MessageRoleAssistant,
			Content: "",
			Reasoning: []sonutruntime.ReasoningBlock{{
				Provider: "llama",
				Type:     "reasoning_content",
				Text:     "I should inspect the relevant file.",
			}},
			ToolCalls: []sonutruntime.ToolCall{{
				ID:        "call-7",
				Name:      "read_file",
				Arguments: `{"path":"README.md"}`,
			}},
		},
		{
			Role:       sonutruntime.MessageRoleTool,
			Content:    "file contents",
			ToolCallID: "call-7",
		},
	}

	raw, err := json.Marshal(buildChatMessages(messages))
	if err != nil {
		t.Fatal(err)
	}
	var decoded []map[string]any
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatal(err)
	}
	assistant := decoded[2]
	if assistant["reasoning_content"] != "I should inspect the relevant file." {
		t.Fatalf("assistant reasoning_content was not replayed: %#v", assistant)
	}
	calls, ok := assistant["tool_calls"].([]any)
	if !ok || len(calls) != 1 {
		t.Fatalf("assistant tool_calls were not replayed: %#v", assistant)
	}
	call := calls[0].(map[string]any)
	if call["id"] != "call-7" || call["type"] != "function" {
		t.Fatalf("assistant tool call identity changed: %#v", call)
	}
	function := call["function"].(map[string]any)
	if function["name"] != "read_file" || function["arguments"] != `{"path":"README.md"}` {
		t.Fatalf("assistant tool call payload changed: %#v", function)
	}
	if decoded[3]["tool_call_id"] != "call-7" {
		t.Fatalf("tool result lost tool_call_id: %#v", decoded[3])
	}
}

func TestReadSSEPreservesLocalReasoningForReplay(t *testing.T) {
	stream := strings.Join([]string{
		`data: {"choices":[{"delta":{"reasoning_content":"inspect first"}}]}`,
		`data: {"choices":[{"delta":{"tool_calls":[{"index":0,"id":"call-1","type":"function","function":{"name":"read_file","arguments":"{}"}}]},"finish_reason":"tool_calls"}]}`,
		`data: [DONE]`,
		"",
	}, "\n\n")
	events := make(chan sonutruntime.StreamEvent, 16)
	go func() {
		NewProvider("http://127.0.0.1:1").readSSE(t.Context(), strings.NewReader(stream), events)
		close(events)
	}()

	var blocks []sonutruntime.ReasoningBlock
	for event := range events {
		blocks = append(blocks, event.ReasoningBlocks...)
	}
	if got := localReasoningContent(blocks); got != "inspect first" {
		t.Fatalf("local reasoning replay = %q, want %q", got, "inspect first")
	}
}

func indexOfMessages(b []byte) int {
	idx := strings.Index(string(b), `"messages"`)
	if idx < 0 {
		return len(b)
	}
	return idx
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
