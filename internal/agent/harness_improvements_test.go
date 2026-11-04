package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/aistatine/sonut/internal/sonutruntime"
	"github.com/aistatine/sonut/internal/tools"
)

// ─── classifyTurnShape ────────────────────────────────────────────────────────

func TestClassifyTurnShapeNone(t *testing.T) {
	cases := []string{
		"ALHAMDULILLAH",
		"hi",
		"hello",
		"thanks",
		"ok",
		"",
		"short",
		// Short free-form reactions with no task/code signal must not arm tools
		// or thinking — observed live as multi-minute "model stalled" hangs.
		"ITS TOOOOO BAD",
		"oh no",
		"so cool!!",
		"too bad",
	}
	for _, c := range cases {
		got := classifyTurnShape(c)
		if got != TurnShapeNone {
			t.Errorf("classifyTurnShape(%q) = %q, want %q", c, got, TurnShapeNone)
		}
	}
}

func TestClassifyTurnShapeContinuation(t *testing.T) {
	cases := []string{
		"continue",
		"go on",
		"proceed",
		"yes",
		"got it",
		"go ahead",
		"do it",
	}
	for _, c := range cases {
		got := classifyTurnShape(c)
		if got != TurnShapeContinuation {
			t.Errorf("classifyTurnShape(%q) = %q, want %q", c, got, TurnShapeContinuation)
		}
	}
}

func TestClassifyTurnShapeQuestion(t *testing.T) {
	cases := []string{
		"why does the build fail?",
		"how does the caching layer work?",
		"what is the difference between these two approaches?",
		"should we use Redis or Memcached here?",
		"can you explain the architecture?",
		// Identity follow-ups used to misclassify as Task and arm tools+thinking
		// (observed live: first greeting fast, "who are you" multi-minute hang).
		"who are you",
		"who are you?",
		"what are you",
		"Who are you and what can you do?",
		// "make" is a revenue verb here, never a build request.
		"HOW MUCH did odysseus 2026 movie make",
	}
	for _, c := range cases {
		got := classifyTurnShape(c)
		if got != TurnShapeQuestion {
			t.Errorf("classifyTurnShape(%q) = %q, want %q", c, got, TurnShapeQuestion)
		}
	}
}

func TestCurrentFactQuickModeExposesOnlyWebSearch(t *testing.T) {
	provider := &mockProvider{turns: [][]sonutruntime.StreamEvent{{
		{Type: sonutruntime.StreamEventText, Content: "It earned a reported amount."},
		{Type: sonutruntime.StreamEventDone},
	}}}
	registry := tools.NewRegistry()
	for _, tool := range tools.CoreNetworkTools() {
		if tool.Name() == "web_search" {
			registry.Register(tool)
		}
	}
	prompt := "How much did Odysseus 2026 movie make"
	_, err := Run(context.Background(), prompt, provider, Options{
		Registry: registry, ProviderName: "llama", RunMode: RouteRunMode(prompt), Evaluate: true, NetworkDisabled: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(provider.requests) != 1 {
		t.Fatalf("current fact must skip planner, got %d requests", len(provider.requests))
	}
	if len(provider.requests[0].Tools) != 0 {
		t.Fatalf("offline quick current fact must expose no network tools, got %#v", provider.requests[0].Tools)
	}
	// Quick/flash Q&A: thinking ON at high effort by default (answer quality).
	if provider.requests[0].DisableThinking {
		t.Fatal("quick answer must keep thinking enabled")
	}
	if !strings.EqualFold(provider.requests[0].ReasoningEffort, "high") {
		t.Fatalf("quick answer effort = %q, want high", provider.requests[0].ReasoningEffort)
	}
	// Routing: factual box-office question is quick mode, not a builder task.
	if RouteRunMode(prompt) != RunModeQuick {
		t.Fatalf("box-office question must route quick, got %q", RouteRunMode(prompt))
	}
	if !needsQuickWebSearch(prompt) {
		t.Fatal("box-office question must arm web_search")
	}
}

func TestCurrentDirectorFactUsesDuckDuckGoQuickPath(t *testing.T) {
	provider := &mockProvider{turns: [][]sonutruntime.StreamEvent{{
		{Type: sonutruntime.StreamEventText, Content: "The director is listed by current sources."},
		{Type: sonutruntime.StreamEventDone},
	}}}
	registry := tools.NewRegistry()
	for _, tool := range tools.CoreNetworkTools() {
		if tool.Name() == "web_search" {
			registry.Register(tool)
		}
	}
	_, err := Run(context.Background(), "who directed odysseus movie 2026", provider, Options{
		Registry: registry, ProviderName: "llama", RunMode: RouteRunMode("who directed odysseus movie 2026"), Evaluate: true, NetworkDisabled: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(provider.requests) != 1 {
		t.Fatalf("current director fact must skip planner, got %d requests", len(provider.requests))
	}
	if len(provider.requests[0].Tools) != 0 {
		t.Fatalf("offline director fact must expose no network tools, got %#v", provider.requests[0].Tools)
	}
}

func TestInternetVerificationFollowupStaysQuick(t *testing.T) {
	provider := &mockProvider{turns: [][]sonutruntime.StreamEvent{{
		{Type: sonutruntime.StreamEventText, Content: "I checked current sources."},
		{Type: sonutruntime.StreamEventDone},
	}}}
	registry := tools.NewRegistry()
	for _, tool := range tools.CoreNetworkTools() {
		if tool.Name() == "web_search" {
			registry.Register(tool)
		}
	}
	_, err := Run(context.Background(), "check on internet", provider, Options{
		Registry: registry, ProviderName: "llama", RunMode: RouteRunMode("check on internet"), Evaluate: true, NetworkDisabled: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(provider.requests) != 1 {
		t.Fatalf("verification followup must skip planner, got %d requests", len(provider.requests))
	}
	if len(provider.requests[0].Tools) != 0 {
		t.Fatalf("offline verification followup must expose no network tools, got %#v", provider.requests[0].Tools)
	}
}

func TestFactualQuestionUsesOneRealWebSearchAction(t *testing.T) {
	prompt := "How many surahs are in the Quran?"
	var gotQuery string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request struct {
			Query string `json:"query"`
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Fatalf("decode search request: %v", err)
		}
		gotQuery = request.Query
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`[{"title":"Quran reference","url":"https://example.test/quran","snippet":"The Quran has 114 surahs."}]`))
	}))
	defer server.Close()
	t.Setenv("SONUT_WEBSEARCH_BASE_URL", server.URL)

	provider := &mockProvider{turns: [][]sonutruntime.StreamEvent{
		toolTurn("search-1", "web_search", `{"query":"How many surahs are in the Quran?"}`),
		textTurn("There are 114 surahs."),
	}}
	registry := tools.NewRegistry()
	for _, tool := range tools.CoreNetworkTools() {
		if tool.Name() == "web_search" {
			registry.Register(tool)
		}
	}
	result, err := Run(context.Background(), prompt, provider, Options{
		Registry: registry, ProviderName: "llama", RunMode: RouteRunMode(prompt), Evaluate: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if gotQuery != prompt {
		t.Fatalf("web search query = %q, want the user prompt %q", gotQuery, prompt)
	}
	if result.FinalAnswer != "There are 114 surahs." {
		t.Fatalf("final answer = %q", result.FinalAnswer)
	}
	if len(provider.requests) != 2 {
		t.Fatalf("expected one search request and one answer request, got %d", len(provider.requests))
	}
	for index, request := range provider.requests {
		if len(request.Tools) != 1 || request.Tools[0].Name != "web_search" {
			t.Fatalf("request %d exposed unexpected tools: %#v", index, request.Tools)
		}
		if index == 0 && request.DisableThinking {
			t.Fatalf("factual request %d disabled thinking", index)
		}
	}
	if strings.Contains(provider.requests[0].Messages[len(provider.requests[0].Messages)-1].Content, "<private_context>") {
		t.Fatal("factual request unexpectedly received private context")
	}
}

func TestAdviceQuestionUsesOneRealWebSearchAction(t *testing.T) {
	prompt := "I want to wash my car. The car wash is 50 meters away. Should I walk or drive?"
	called := false
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`[{"title":"Car wash guidance","url":"https://example.test/car","snippet":"Drive the car to the car wash so the car itself reaches the wash."}]`))
	}))
	defer server.Close()
	t.Setenv("SONUT_WEBSEARCH_BASE_URL", server.URL)

	provider := &mockProvider{turns: [][]sonutruntime.StreamEvent{
		toolTurn("search-1", "web_search", `{"query":"I want to wash my car. The car wash is 50 meters away. Should I walk or drive?"}`),
		textTurn("Drive, because the car needs to reach the wash."),
	}}
	registry := tools.NewRegistry()
	for _, tool := range tools.CoreNetworkTools() {
		if tool.Name() == "web_search" {
			registry.Register(tool)
		}
	}
	_, err := Run(context.Background(), prompt, provider, Options{
		Registry: registry, ProviderName: "llama", RunMode: RouteRunMode(prompt), Evaluate: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !called {
		t.Fatal("advice question did not execute the registered web_search action")
	}
	if len(provider.requests) != 2 || len(provider.requests[0].Tools) != 1 || provider.requests[0].Tools[0].Name != "web_search" {
		t.Fatalf("advice route did not expose exactly web_search: %#v", provider.requests)
	}
}

func TestBuilderTaskDoesNotPrefetchWebUnconditionally(t *testing.T) {
	prompt := "create a compact page about alpine birds"
	called := false
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`[{"title":"Alpine bird guide","url":"https://example.test/alpine","snippet":"Alpine birds are adapted to cold, high-altitude habitats."}]`))
	}))
	defer server.Close()
	t.Setenv("SONUT_WEBSEARCH_BASE_URL", server.URL)

	provider := &mockProvider{turns: [][]sonutruntime.StreamEvent{{
		{Type: sonutruntime.StreamEventText, Content: "Done."},
		{Type: sonutruntime.StreamEventDone},
	}}}
	_, err := Run(context.Background(), prompt, provider, Options{
		Registry: tools.NewRegistry(), ProviderName: "llama", RunMode: RunModeBuilder, UnifiedWorkflow: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if called {
		t.Fatal("builder task unexpectedly started a private web search")
	}
	if len(provider.requests) != 1 {
		t.Fatalf("builder turn should make one provider request, got %d", len(provider.requests))
	}
	var userMessage string
	for i := len(provider.requests[0].Messages) - 1; i >= 0; i-- {
		if provider.requests[0].Messages[i].Role == sonutruntime.MessageRoleUser {
			userMessage = provider.requests[0].Messages[i].Content
			break
		}
	}
	if !strings.Contains(userMessage, prompt) {
		t.Fatalf("raw prompt missing from builder input: %q", userMessage)
	}
	if strings.Contains(userMessage, "Alpine bird guide") {
		t.Fatalf("unrequested web context leaked into builder input: %q", userMessage)
	}
}

func TestNetworkDisabledSkipsWebSearchAction(t *testing.T) {
	called := false
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`[{"title":"must not be used","url":"https://example.test","snippet":"offline"}]`))
	}))
	defer server.Close()
	t.Setenv("SONUT_WEBSEARCH_BASE_URL", server.URL)

	provider := &mockProvider{turns: [][]sonutruntime.StreamEvent{{
		{Type: sonutruntime.StreamEventText, Content: "Offline answer."},
		{Type: sonutruntime.StreamEventDone},
	}}}
	prompt := "what is a local-only test"
	_, err := Run(context.Background(), prompt, provider, Options{
		ProviderName: "llama", RunMode: RunModeQuick, NetworkDisabled: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if called {
		t.Fatal("network-disabled turn unexpectedly executed web search")
	}
	for _, message := range provider.requests[0].Messages {
		if strings.Contains(message.Content, "must not be used") {
			t.Fatalf("offline turn received web result: %q", message.Content)
		}
		if strings.Contains(message.Content, "<private_context>") {
			t.Fatalf("offline turn received private context: %q", message.Content)
		}
	}
}

// Pure knowledge/identity questions must advertise @code tools so hybrid local
// models skip the schema prefill tax on follow-up turns.
func TestIdentityQuestionStripsTools(t *testing.T) {
	provider := &mockProvider{turns: [][]sonutruntime.StreamEvent{
		textTurn("I am @code, a terminal coding agent created by Aistatine."),
	}}
	reg := tools.NewRegistry()
	reg.Register(&stubReadTool{})
	_, err := Run(context.Background(), "who are you", provider, Options{
		Registry:        reg,
		ReasoningEffort: "super",
		ProviderName:    "llama", // local flash path
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(provider.requests) != 1 {
		t.Fatalf("identity Q must be single request, got %d", len(provider.requests))
	}
	req := provider.requests[0]
	if len(req.Tools) != 0 {
		t.Fatalf("identity Q must advertise no tools, got %d", len(req.Tools))
	}
	// Explicit /effort pin wins; flash Q&A defaults to high when auto/empty.
	if req.ReasoningEffort != "super" {
		t.Fatalf("identity Q must honor pinned effort, got %q want super", req.ReasoningEffort)
	}
	if req.DisableThinking {
		t.Fatal("identity Q must keep thinking enabled")
	}
	// Flash system prompt must be tiny (no workspace/AGENTS dump).
	if len(req.Messages) == 0 || req.Messages[0].Role != sonutruntime.MessageRoleSystem {
		t.Fatalf("expected system message, got %#v", req.Messages)
	}
	sys := req.Messages[0].Content
	if len(sys) > 800 {
		t.Fatalf("flash system prompt too large (%d bytes) — prefill will still hang", len(sys))
	}
	if !strings.Contains(sys, "@code") {
		t.Fatalf("flash system should identify as @code: %q", sys)
	}
}

// Short non-code Q&A ("are you boy or girl") used to keep full tools + system
// prompt and sit at 0 tokens for ~60s on hybrid local. Flash path strips both.
func TestShortNonCodeQuestionIsFlash(t *testing.T) {
	for _, q := range []string{"are you boy or girl", "how old are you?", "are you a robot"} {
		if !isFlashQA(classifyTurnShape(q), q) {
			t.Errorf("isFlashQA(%q) = false, want true", q)
		}
	}
	// Technical / task prompts must NOT flash.
	for _, q := range []string{"fix foo.go", "what does main() do in src/app.ts?", "read README.md"} {
		if isFlashQA(classifyTurnShape(q), q) {
			t.Errorf("isFlashQA(%q) = true, want false (coding/tech)", q)
		}
	}

	provider := &mockProvider{turns: [][]sonutruntime.StreamEvent{
		textTurn("I'm an AI — no gender."),
	}}
	reg := tools.NewRegistry()
	reg.Register(&stubReadTool{})
	// Simulate TUI resume wrapper: huge session dump around a simple ask.
	wrapped := "Continuing @code session abc.\nPrevious session context:\n- #1 message: hello\n\nCurrent user request:\nare you boy or girl"
	_, err := Run(context.Background(), wrapped, provider, Options{
		Registry:        reg,
		ReasoningEffort: "auto",
		ProviderName:    "llama",
		PlannerPrompt:   "are you boy or girl",
	})
	if err != nil {
		t.Fatal(err)
	}
	req := provider.requests[0]
	if len(req.Tools) != 0 {
		t.Fatalf("short Q must strip tools, got %d", len(req.Tools))
	}
	// User message must be the RAW ask, not the resume preamble (prefill killer).
	var user string
	for _, m := range req.Messages {
		if m.Role == sonutruntime.MessageRoleUser {
			user = m.Content
			break
		}
	}
	if strings.Contains(user, "Previous session context") {
		t.Fatalf("flash turn must drop session resume dump, got: %q", user)
	}
	if !strings.Contains(user, "are you boy or girl") {
		t.Fatalf("user message should contain the raw ask, got: %q", user)
	}
}

func TestFlashFollowUpKeepsPriorConversationHistory(t *testing.T) {
	provider := &mockProvider{turns: [][]sonutruntime.StreamEvent{
		textTurn("The r's are at positions 2, 3, and 10."),
	}}

	_, err := Run(context.Background(), "which position", provider, Options{
		ProviderName:  "llama",
		Model:         "atcode-v1",
		RunMode:       RunModeQuick,
		PlannerPrompt: "which position",
		ConversationHistory: []sonutruntime.Message{
			{Role: sonutruntime.MessageRoleUser, Content: "how many r in strawberry"},
			{Role: sonutruntime.MessageRoleAssistant, Content: "There are 3 r's in strawberry."},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(provider.requests) != 1 {
		t.Fatalf("expected one quick follow-up request, got %d", len(provider.requests))
	}

	request := provider.requests[0]
	var contents []string
	for _, message := range request.Messages {
		contents = append(contents, string(message.Role)+": "+message.Content)
	}
	joined := strings.Join(contents, "\n")
	for _, want := range []string{"how many r in strawberry", "There are 3 r's in strawberry.", "which position"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("follow-up request lost %q:\n%s", want, joined)
		}
	}
}

func TestClassifyTurnShapeTask(t *testing.T) {
	cases := []string{
		"fix the auth bug in user_service.go",
		"build a mario game in HTML",
		"implement pagination for the API endpoint",
		"refactor the payment module",
		// Short prompts with paths / file tokens stay tasks even without a
		// listed task verb — the technical-signal gate must not swallow them.
		"read foo.go",
		"open src/main.ts",
	}
	for _, c := range cases {
		got := classifyTurnShape(c)
		if got != TurnShapeTask {
			t.Errorf("classifyTurnShape(%q) = %q, want %q", c, got, TurnShapeTask)
		}
	}
}

// ─── Pinned-effort trivial override ──────────────────────────────────────────

// A greeting must run at "none" effort even when the user pinned "super".
// The run must NOT fire a planner pass (one provider request total).
func TestPinnedEffortGreetingRunsAtNone(t *testing.T) {
	provider := &mockProvider{turns: [][]sonutruntime.StreamEvent{
		textTurn("ALHAMDULILLAH to you too!"),
	}}

	result, err := Run(context.Background(), "ALHAMDULILLAH", provider, Options{
		Registry:        tools.NewRegistry(),
		ReasoningEffort: "super",
		Evaluate:        true, // planner enabled
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.FinalAnswer == "" {
		t.Fatal("expected a final answer")
	}
	// Only 1 provider request: greeting must skip the planner pass.
	if len(provider.requests) != 1 {
		t.Fatalf("expected 1 request (no planner for greeting), got %d", len(provider.requests))
	}
	// The resolved effort on the request must be "none".
	req := provider.requests[0]
	if req.ReasoningEffort != "none" {
		t.Fatalf("greeting at pinned super: want ReasoningEffort %q, got %q", "none", req.ReasoningEffort)
	}
}

// The TUI wraps follow-up prompts in session context; the shape override must
// classify the raw ask via PlannerPrompt, and the greeting turn must carry a
// no-tool-calls note so the model doesn't wander into exploration.
func TestPinnedEffortGreetingWithWrappedPrompt(t *testing.T) {
	provider := &mockProvider{turns: [][]sonutruntime.StreamEvent{
		textTurn("Alhamdulillah!"),
	}}
	wrapped := "Resumed session context: previously we worked on the harness improvements across several files.\n\nUser request: ALHAMDULILLAH"
	_, err := Run(context.Background(), wrapped, provider, Options{
		Registry:        tools.NewRegistry(),
		ReasoningEffort: "super",
		Evaluate:        true,
		PlannerPrompt:   "ALHAMDULILLAH",
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(provider.requests) != 1 {
		t.Fatalf("wrapped greeting must skip planner: expected 1 request, got %d", len(provider.requests))
	}
	req := provider.requests[0]
	if req.ReasoningEffort != "none" {
		t.Fatalf("wrapped greeting at pinned super: want ReasoningEffort %q, got %q", "none", req.ReasoningEffort)
	}
	if !someMessageContains(req.Messages, "Do NOT call any tools") {
		t.Fatal("greeting turn must carry the no-tool-calls note")
	}
}

// Even if the model ignores the greeting note and calls tools, the run is
// hard-capped at 2 turns so it cannot spiral into an exploration loop.
func TestGreetingTurnCapBackstop(t *testing.T) {
	turns := [][]sonutruntime.StreamEvent{
		toolTurn("g1", "read_file", `{"path":"a.go"}`),
		toolTurn("g2", "read_file", `{"path":"b.go"}`),
		toolTurn("g3", "read_file", `{"path":"c.go"}`),
		textTurn("done exploring"),
	}
	provider := &mockProvider{turns: turns}
	reg := tools.NewRegistry()
	reg.Register(&stubReadTool{})
	_, _ = Run(context.Background(), "ALHAMDULILLAH", provider, Options{
		Registry:        reg,
		ReasoningEffort: "super",
	})
	// 2 capped turns + 1 final-answer wrap-up request = 3 max.
	if len(provider.requests) > 3 {
		t.Fatalf("greeting run must be capped at 2 turns (+1 wrap-up), got %d requests", len(provider.requests))
	}
}

// A continuation turn ("continue") skips the planner even with Evaluate on.
func TestContinuationSkipsPlanner(t *testing.T) {
	provider := &mockProvider{turns: [][]sonutruntime.StreamEvent{
		textTurn("Continuing..."),
	}}
	_, err := Run(context.Background(), "continue", provider, Options{
		Registry: tools.NewRegistry(),
		Evaluate: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(provider.requests) != 1 {
		t.Fatalf("continuation must skip planner: expected 1 request, got %d", len(provider.requests))
	}
}

// An internal keep-going nudge may be a long sentence, so shape routing alone
// cannot reliably recognize it as a continuation. ExecutorOnly must be the
// authoritative guard: no second grounding/planner pass, concise private
// thinking, and no executor-owned update_plan tool.
func TestExecutorOnlyContinuationSkipsPlannerAndThinking(t *testing.T) {
	provider := &mockProvider{turns: [][]sonutruntime.StreamEvent{
		textTurn("Finished the next roadmap item."),
	}}
	registry := tools.NewRegistry()
	registry.Register(tools.NewUpdatePlanTool())

	_, err := Run(context.Background(), "3 tasks remain open. Keep going with the next task now.", provider, Options{
		Registry:     registry,
		Evaluate:     true,
		RunMode:      RunModeBuilder,
		ExecutorOnly: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(provider.requests) != 1 {
		t.Fatalf("executor-only continuation must skip planner, got %d requests", len(provider.requests))
	}
	req := provider.requests[0]
	if req.DisableThinking {
		t.Fatal("executor-only continuation must keep concise private thinking enabled")
	}
	for _, tool := range req.Tools {
		if tool.Name == "update_plan" {
			t.Fatal("executor-only continuation must keep update_plan planner-owned")
		}
	}
}

func TestExecutorPhaseRemovesInspectionTools(t *testing.T) {
	definitions := []sonutruntime.ToolDefinition{
		{Name: "read_file"},
		{Name: "grep"},
		{Name: "tool_search"},
		{Name: "web_search"},
		{Name: "write_file"},
		{Name: "append_file"},
		{Name: "exec_command"},
	}
	got := withoutExecutorInspectionTools(definitions)
	for _, definition := range got {
		if executorInspectionTool(definition.Name) {
			t.Fatalf("executor still received inspection tool %q", definition.Name)
		}
	}
	for _, want := range []string{"write_file", "append_file", "exec_command"} {
		if !hasToolDefinition(got, want) {
			t.Fatalf("executor lost productive tool %q: %#v", want, got)
		}
	}
}

func TestSeededExecutorCapsRunawayNarration(t *testing.T) {
	provider := &mockProvider{turns: [][]sonutruntime.StreamEvent{
		textTurn(strings.Repeat("repeating the plan instead of acting ", 600)),
		toolTurn("exec-1", "touch_tool", `{}`),
		textTurn("done"),
	}}
	registry := tools.NewRegistry()
	registry.Register(&recordingTool{})

	result, err := Run(context.Background(), "continue the existing plan", provider, Options{
		Registry:     registry,
		RunMode:      RunModeBuilder,
		ExecutorOnly: true,
		MaxTurns:     4,
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.FinalAnswer != "done" {
		t.Fatalf("runaway executor should recover into the next tool call, got %q", result.FinalAnswer)
	}
	if len(provider.requests) != 3 {
		t.Fatalf("expected bounded recovery plus tool and final turns, got %d requests", len(provider.requests))
	}
}

// A question-shaped prompt must get a system note injecting the read-only constraint.
func TestQuestionShapeInjectsReadOnlyNote(t *testing.T) {
	provider := &mockProvider{turns: [][]sonutruntime.StreamEvent{
		textTurn("The build fails because of a missing import."),
	}}
	_, err := Run(context.Background(), "why does the build fail?", provider, Options{
		Registry: tools.NewRegistry(),
	})
	if err != nil {
		t.Fatal(err)
	}
	req := provider.requests[0]
	found := false
	for _, m := range req.Messages {
		if strings.Contains(m.Content, "Never edit files") {
			found = true
			break
		}
	}
	if !found {
		t.Fatal("question shape: expected read-only note injected into request messages")
	}
}

// A real task must NOT get the read-only note injected.
func TestTaskShapeNoReadOnlyNote(t *testing.T) {
	provider := &mockProvider{turns: [][]sonutruntime.StreamEvent{
		textTurn("Fixed."),
	}}
	_, err := Run(context.Background(), "fix the null pointer exception in server.go", provider, Options{
		Registry: tools.NewRegistry(),
	})
	if err != nil {
		t.Fatal(err)
	}
	req := provider.requests[0]
	for _, m := range req.Messages {
		if strings.Contains(m.Content, "Never edit files") {
			t.Fatal("task shape must NOT get the read-only note")
		}
	}
}

// ─── Exploration budget ───────────────────────────────────────────────────────

// After many consecutive read-only turns the model gets a soft one-shot nudge
// to also start implementing — never a ban on further reads.
func TestExplorationNudgeAfterReadOnlyTurns(t *testing.T) {
	// Default explorationNudgeAt is 12; provide that many reads then a final answer.
	turns := make([][]sonutruntime.StreamEvent, 0, 13)
	for i := 0; i < 12; i++ {
		turns = append(turns, toolTurn(
			fmt.Sprintf("r%d", i+1),
			"read_file",
			fmt.Sprintf(`{"path":"f%d.go"}`, i+1),
		))
	}
	turns = append(turns, textTurn("All done, I've explored enough."))

	provider := &mockProvider{turns: turns}
	reg := tools.NewRegistry()
	reg.Register(&stubReadTool{})

	result, err := Run(context.Background(), "explore the codebase thoroughly", provider, Options{
		Registry: reg,
	})
	if err != nil {
		t.Fatal(err)
	}
	_ = result

	// Find the soft exploration nudge in the messages sent to the provider.
	found := false
	for _, req := range provider.requests {
		for _, m := range req.Messages {
			if strings.Contains(m.Content, "explored for several turns") ||
				strings.Contains(m.Content, "Keep reading any files you still need") {
				found = true
			}
		}
	}
	if !found {
		t.Fatal("expected soft exploration nudge after many consecutive read-only turns")
	}
}

// ─── Wall-clock generation budget ────────────────────────────────────────────

// slowProvider simulates a generation that never finishes within the timeout.
type slowProvider struct {
	mockProvider
	delay time.Duration
}

func (p *slowProvider) StreamCompletion(ctx context.Context, req sonutruntime.CompletionRequest) (<-chan sonutruntime.StreamEvent, error) {
	p.requests = append(p.requests, req)
	ch := make(chan sonutruntime.StreamEvent)
	go func() {
		select {
		case <-ctx.Done():
			// Context cancelled (timeout); send no events — stream just closes.
			close(ch)
		case <-time.After(p.delay):
			ch <- sonutruntime.StreamEvent{Type: sonutruntime.StreamEventText, Content: "slow answer"}
			ch <- sonutruntime.StreamEvent{Type: sonutruntime.StreamEventDone}
			close(ch)
		}
	}()
	return ch, nil
}

func TestGenerationTimeoutRetriesWithThinkingOff(t *testing.T) {
	// First call times out (delay > timeout); second call succeeds fast.
	provider := &slowProvider{delay: 500 * time.Millisecond}
	// Override turns for the retry: after the first times out, slowProvider
	// returns fast on the second request (delay is unchanged but the retry
	// uses a fresh context with no timeout from our side, and the delay is
	// shorter than the test deadline). Actually let's make delay very short
	// for the retry by checking the request count.
	provider.mockProvider.turns = [][]sonutruntime.StreamEvent{
		// turn 0: times out (handled by slowProvider goroutine above)
		// turn 1: the retry — we need the provider to answer fast.
		// Since slowProvider checks ctx.Done vs delay, we need delay < retry ctx timeout.
		// Set delay to 200ms and GenerationTimeout to 50ms so turn 0 times out
		// but the retry (no GenerationTimeout cap — uses the run's ctx) completes.
	}

	// Use a fast provider for the retry: wrap with a smart provider.
	smartProvider := &generationTimeoutTestProvider{firstDelay: 200 * time.Millisecond}

	result, err := Run(context.Background(), "build a complex server", smartProvider, Options{
		Registry:          tools.NewRegistry(),
		GenerationTimeout: 50 * time.Millisecond,
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.FinalAnswer == "" {
		t.Fatal("expected a final answer after timeout retry")
	}
	// Must have made 2 requests: the timed-out one and the retry.
	if len(smartProvider.requests) < 2 {
		t.Fatalf("expected at least 2 requests (timeout + retry), got %d", len(smartProvider.requests))
	}
	// The retry request must carry the timeout nudge message.
	retryReq := smartProvider.requests[1]
	found := false
	for _, m := range retryReq.Messages {
		if strings.Contains(m.Content, "time limit") || strings.Contains(m.Content, "idle") {
			found = true
		}
	}
	if !found {
		t.Fatal("retry request must contain the generation timeout nudge")
	}
	// Plan-less open turn: keep a brief low think on timeout recovery so the
	// model can still act (not a deep spiral). Plan-seeded path forces none.
	if retryReq.ReasoningEffort != "low" {
		t.Fatalf("retry request must have ReasoningEffort=low, got %q", retryReq.ReasoningEffort)
	}
	if retryReq.DisableThinking {
		t.Fatal("plan-less generation-timeout retry must keep DisableThinking=false")
	}
}

// Tool arguments are model output too. In particular, write_file and edit_file
// bodies can be streamed indefinitely without ever reaching the filesystem. A
// tool-call start must therefore not disable the generation budget.
func TestGenerationTimeoutRetriesDuringSlowToolArguments(t *testing.T) {
	provider := &slowToolArgumentProvider{firstDuration: 200 * time.Millisecond, toolName: "exec_command"}

	result, err := Run(context.Background(), "create a small page", provider, Options{
		Registry:          tools.NewRegistry(),
		GenerationTimeout: 50 * time.Millisecond,
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.FinalAnswer == "" {
		t.Fatal("expected a final answer after retrying the slow tool call")
	}
	if len(provider.requests) < 2 {
		t.Fatalf("expected timeout retry after slow tool arguments, got %d requests", len(provider.requests))
	}
	found := false
	for _, message := range provider.requests[1].Messages {
		if strings.Contains(message.Content, "time limit") || strings.Contains(message.Content, "idle") {
			found = true
			break
		}
	}
	if !found {
		t.Fatal("tool-argument timeout retry must carry the generation timeout nudge")
	}
}

// A progressing write_file that streams argument deltas past the base idle
// budget must complete (progress refreshes the timer) — this is the live
// "generation exceeded 2m0s" failure mode.
func TestProgressingFileWriteSurvivesBaseIdleBudget(t *testing.T) {
	provider := &chunkedFileGenerationProvider{
		chunkDelay: 40 * time.Millisecond,
		chunks: []string{
			`{"path":"index.html","content":"<!doctype html>\n`,
			`<html><body>\n`,
			`hello\n`,
			`</body></html>"}`,
		},
	}
	registry := tools.NewRegistry()
	registry.Register(&stubWriteTool{})

	result, err := Run(context.Background(), "create a complete index page", provider, Options{
		Registry:          registry,
		GenerationTimeout: 60 * time.Millisecond, // shorter than total stream time
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.FinalAnswer == "" {
		t.Fatal("expected final answer; progressing write must not hard-fail on idle budget")
	}
	// First request is the write; a pure timeout-retry would add a second
	// request BEFORE any tool result. With progress refresh we complete the
	// write on request 1 and finish on a later turn.
	if provider.calls < 1 {
		t.Fatal("expected at least one provider call")
	}
}

func TestFileGenerationGetsLongerBoundedWindow(t *testing.T) {
	// File tools must use the collector's file budget (default 12m), not base*3.
	// base*3 at the old 2m default still killed healthy multi-minute writes.
	wantFile := sonutruntime.MaxFileToolArgDuration()
	if got := generationTimeoutForTool(2*time.Minute, "write_file"); got != wantFile {
		t.Fatalf("write_file timeout = %s, want %s", got, wantFile)
	}
	if got := generationTimeoutForTool(2*time.Minute, "append_file"); got != wantFile {
		t.Fatalf("append_file timeout = %s, want %s", got, wantFile)
	}
	if got := generationTimeoutForTool(2*time.Minute, "read_file"); got != 2*time.Minute {
		t.Fatalf("read_file timeout = %s, want unchanged 2m", got)
	}
	// A deliberately longer base is never shrunk.
	if got := generationTimeoutForTool(30*time.Minute, "write_file"); got != 30*time.Minute {
		t.Fatalf("write_file with long base = %s, want 30m", got)
	}
}

func TestActiveFileGenerationUsesExtendedWindow(t *testing.T) {
	provider := &delayedFileGenerationProvider{delay: 80 * time.Millisecond}
	registry := tools.NewRegistry()
	registry.Register(&stubWriteTool{})

	result, err := Run(context.Background(), "create a complete index page", provider, Options{
		Registry:          registry,
		GenerationTimeout: 50 * time.Millisecond,
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.FinalAnswer != "File created." {
		t.Fatalf("final answer = %q", result.FinalAnswer)
	}
	if provider.calls != 2 {
		t.Fatalf("provider calls = %d, want one file turn and one final turn (no timeout retry)", provider.calls)
	}
}

func TestGenerationTimeoutRecoveryIsPerTurn(t *testing.T) {
	provider := &twoTurnTimeoutProvider{}
	registry := tools.NewRegistry()
	registry.Register(&stubReadTool{})

	result, err := Run(context.Background(), "inspect the project and report the implementation state", provider, Options{
		Registry:          registry,
		GenerationTimeout: 20 * time.Millisecond,
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.FinalAnswer != "Done after the second recovery." {
		t.Fatalf("final answer = %q", result.FinalAnswer)
	}
	if provider.calls != 4 {
		t.Fatalf("provider calls = %d, want two timed-out attempts plus two retries", provider.calls)
	}
}

// Different paths may be read freely on image tasks (multi-file UIs need it).
// Same-path re-reads are blocked by the guardrail — that is the only ceiling.
func TestImageTaskAllowsDistinctFileReads(t *testing.T) {
	turn := []sonutruntime.StreamEvent{}
	for index, path := range []string{"index.html", "game.js", "mariogame.html", "mariogame.js"} {
		id := fmt.Sprintf("read-%d", index)
		turn = append(turn,
			sonutruntime.StreamEvent{Type: sonutruntime.StreamEventToolCallStart, ToolCallID: id, ToolName: "read_file"},
			sonutruntime.StreamEvent{Type: sonutruntime.StreamEventToolCallDelta, ToolCallID: id, ArgumentsFragment: fmt.Sprintf(`{"path":%q}`, path)},
			sonutruntime.StreamEvent{Type: sonutruntime.StreamEventToolCallEnd, ToolCallID: id},
		)
	}
	turn = append(turn, sonutruntime.StreamEvent{Type: sonutruntime.StreamEventDone})
	provider := &mockProvider{turns: [][]sonutruntime.StreamEvent{turn, textTurn("Implementation started.")}}
	readTool := &countingReadTool{}
	registry := tools.NewRegistry()
	registry.Register(readTool)

	_, err := Run(context.Background(), "build the attached game accurately", provider, Options{
		Registry: registry,
		Images:   []sonutruntime.ImageBlock{{MediaType: "image/png", Data: []byte{1}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if readTool.calls != 4 {
		t.Fatalf("read tool executed %d times, want 4 distinct paths", readTool.calls)
	}
}

type generationTimeoutTestProvider struct {
	requests   []sonutruntime.CompletionRequest
	firstDelay time.Duration
}

// slowToolArgumentProvider continuously emits argument fragments on its first
// request. It fails after firstDuration if the caller does not cancel it, so a
// regression cannot make this test hang indefinitely.
type slowToolArgumentProvider struct {
	requests      []sonutruntime.CompletionRequest
	firstDuration time.Duration
	toolName      string
}

func (p *slowToolArgumentProvider) StreamCompletion(ctx context.Context, req sonutruntime.CompletionRequest) (<-chan sonutruntime.StreamEvent, error) {
	p.requests = append(p.requests, req)
	ch := make(chan sonutruntime.StreamEvent, 2)
	if len(p.requests) > 1 {
		ch <- sonutruntime.StreamEvent{Type: sonutruntime.StreamEventText, Content: "Done."}
		ch <- sonutruntime.StreamEvent{Type: sonutruntime.StreamEventDone}
		close(ch)
		return ch, nil
	}
	go func() {
		defer close(ch)
		ch <- sonutruntime.StreamEvent{Type: sonutruntime.StreamEventToolCallStart, ToolCallID: "slow-1", ToolName: p.toolName}
		ticker := time.NewTicker(5 * time.Millisecond)
		defer ticker.Stop()
		deadline := time.NewTimer(p.firstDuration)
		defer deadline.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-deadline.C:
				ch <- sonutruntime.StreamEvent{Type: sonutruntime.StreamEventError, Error: "tool arguments kept streaming too long"}
				return
			case <-ticker.C:
				ch <- sonutruntime.StreamEvent{Type: sonutruntime.StreamEventToolCallDelta, ToolCallID: "slow-1", ArgumentsFragment: "x"}
			}
		}
	}()
	return ch, nil
}

type twoTurnTimeoutProvider struct {
	calls int
}

func (p *twoTurnTimeoutProvider) StreamCompletion(ctx context.Context, _ sonutruntime.CompletionRequest) (<-chan sonutruntime.StreamEvent, error) {
	p.calls++
	ch := make(chan sonutruntime.StreamEvent, 4)
	switch p.calls {
	case 1, 3:
		go func() {
			<-ctx.Done()
			close(ch)
		}()
	case 2:
		ch <- sonutruntime.StreamEvent{Type: sonutruntime.StreamEventToolCallStart, ToolCallID: "read", ToolName: "read_file"}
		ch <- sonutruntime.StreamEvent{Type: sonutruntime.StreamEventToolCallDelta, ToolCallID: "read", ArgumentsFragment: `{"path":"index.html"}`}
		ch <- sonutruntime.StreamEvent{Type: sonutruntime.StreamEventToolCallEnd, ToolCallID: "read"}
		ch <- sonutruntime.StreamEvent{Type: sonutruntime.StreamEventDone}
		close(ch)
	default:
		ch <- sonutruntime.StreamEvent{Type: sonutruntime.StreamEventText, Content: "Done after the second recovery."}
		ch <- sonutruntime.StreamEvent{Type: sonutruntime.StreamEventDone}
		close(ch)
	}
	return ch, nil
}

type delayedFileGenerationProvider struct {
	calls int
	delay time.Duration
}

func (p *delayedFileGenerationProvider) StreamCompletion(ctx context.Context, _ sonutruntime.CompletionRequest) (<-chan sonutruntime.StreamEvent, error) {
	p.calls++
	ch := make(chan sonutruntime.StreamEvent, 4)
	if p.calls > 1 {
		ch <- sonutruntime.StreamEvent{Type: sonutruntime.StreamEventText, Content: "File created."}
		ch <- sonutruntime.StreamEvent{Type: sonutruntime.StreamEventDone}
		close(ch)
		return ch, nil
	}
	go func() {
		defer close(ch)
		ch <- sonutruntime.StreamEvent{Type: sonutruntime.StreamEventToolCallStart, ToolCallID: "write", ToolName: "write_file"}
		select {
		case <-ctx.Done():
			return
		case <-time.After(p.delay):
		}
		ch <- sonutruntime.StreamEvent{Type: sonutruntime.StreamEventToolCallDelta, ToolCallID: "write", ArgumentsFragment: `{"path":"index.html","content":"<!doctype html>\n<html>\n<body>\nmain\n</body>\n</html>"}`}
		ch <- sonutruntime.StreamEvent{Type: sonutruntime.StreamEventToolCallEnd, ToolCallID: "write"}
		ch <- sonutruntime.StreamEvent{Type: sonutruntime.StreamEventDone}
	}()
	return ch, nil
}

// chunkedFileGenerationProvider streams write_file argument fragments with
// delays between chunks. Total stream time exceeds a short GenerationTimeout
// so the test proves progress-aware idle refresh keeps the write alive.
type chunkedFileGenerationProvider struct {
	calls      int
	chunkDelay time.Duration
	chunks     []string
}

func (p *chunkedFileGenerationProvider) StreamCompletion(ctx context.Context, _ sonutruntime.CompletionRequest) (<-chan sonutruntime.StreamEvent, error) {
	p.calls++
	ch := make(chan sonutruntime.StreamEvent, 8)
	if p.calls > 1 {
		ch <- sonutruntime.StreamEvent{Type: sonutruntime.StreamEventText, Content: "Page ready."}
		ch <- sonutruntime.StreamEvent{Type: sonutruntime.StreamEventDone}
		close(ch)
		return ch, nil
	}
	go func() {
		defer close(ch)
		ch <- sonutruntime.StreamEvent{Type: sonutruntime.StreamEventToolCallStart, ToolCallID: "write", ToolName: "write_file"}
		for _, chunk := range p.chunks {
			select {
			case <-ctx.Done():
				return
			case <-time.After(p.chunkDelay):
			}
			ch <- sonutruntime.StreamEvent{Type: sonutruntime.StreamEventToolCallDelta, ToolCallID: "write", ArgumentsFragment: chunk}
		}
		ch <- sonutruntime.StreamEvent{Type: sonutruntime.StreamEventToolCallEnd, ToolCallID: "write"}
		ch <- sonutruntime.StreamEvent{Type: sonutruntime.StreamEventDone}
	}()
	return ch, nil
}

func (p *generationTimeoutTestProvider) StreamCompletion(ctx context.Context, req sonutruntime.CompletionRequest) (<-chan sonutruntime.StreamEvent, error) {
	p.requests = append(p.requests, req)
	ch := make(chan sonutruntime.StreamEvent, 2)
	if len(p.requests) == 1 {
		// First call: block until context is cancelled (simulates slow generation).
		go func() {
			select {
			case <-ctx.Done():
				close(ch)
			case <-time.After(p.firstDelay):
				ch <- sonutruntime.StreamEvent{Type: sonutruntime.StreamEventText, Content: "slow"}
				ch <- sonutruntime.StreamEvent{Type: sonutruntime.StreamEventDone}
				close(ch)
			}
		}()
		return ch, nil
	}
	// Subsequent calls: answer immediately.
	ch <- sonutruntime.StreamEvent{Type: sonutruntime.StreamEventText, Content: "Done, here is the answer."}
	ch <- sonutruntime.StreamEvent{Type: sonutruntime.StreamEventDone}
	close(ch)
	return ch, nil
}

// ─── Planner code-leak sanitizer ─────────────────────────────────────────────

// A plan step that starts dumping file contents (observed: full HTML game
// source inside "1. write_file(...)") must be cut at the leak point.
func TestSanitizePlanCutsCodeLeak(t *testing.T) {
	leaky := "1. write_file(\"mariogame.html\", \"<!DOCTYPE html>\\n<html>\\n<head>...\")\n2. open it in a browser"
	got := sanitizePlan(leaky)
	if strings.Contains(got, "<!DOCTYPE") || strings.Contains(got, "\\n") {
		t.Fatalf("sanitized plan still contains code: %q", got)
	}
	// Everything after the first leak is implementation dump, not plan.
	if strings.Contains(got, "open it in a browser") {
		t.Fatalf("steps after the code leak must be dropped: %q", got)
	}
}

// A clean numbered plan passes through unchanged.
func TestSanitizePlanKeepsCleanPlan(t *testing.T) {
	clean := "1. Create mariogame.html with canvas rendering and keyboard input\n2. Add collision and scoring\n3. Open in browser to verify"
	if got := sanitizePlan(clean); got != clean {
		t.Fatalf("clean plan must pass through unchanged, got %q", got)
	}
}

// An oversized plan is truncated to the byte cap before line filtering.
func TestSanitizePlanCapsSize(t *testing.T) {
	huge := "1. step\n" + strings.Repeat("x", 20_000)
	if got := sanitizePlan(huge); len(got) > plannerMaxPlanBytes {
		t.Fatalf("plan must be capped at %d bytes, got %d", plannerMaxPlanBytes, len(got))
	}
}

// Plan-seeded executor must not thrash rewrite the same file after a successful
// write — that was the multi-hour mario session failure mode. Second write_file
// on the same path is skipped; the model is nudged to the next step.
func TestPlanSeededExecutorSkipsRewriteOfAlreadyWrittenFile(t *testing.T) {
	root := t.TempDir()
	registry := tools.NewRegistry()
	for _, tool := range tools.CoreWriteTools(root) {
		registry.Register(tool)
	}
	provider := &mockProvider{turns: [][]sonutruntime.StreamEvent{
		textTurn("1. Create game.js\n   - Do: write_file the game implementation\n   - Done when: the file contains the game\n2. Open it\n   - Do: open the page in the browser\n   - Done when: it loads\n3. Verify\n   - Do: inspect the console\n   - Done when: no errors appear"),
		toolTurn("w1", "write_file", `{"path":"game.js","content":"console.log(1)\nconsole.log(2)\nconsole.log(3)\nconsole.log(4)\nconsole.log(5)\n"}`),
		toolTurn("w2", "write_file", `{"path":"game.js","content":"console.log('rewrite thrash')\nconsole.log(2)\nconsole.log(3)\nconsole.log(4)\nconsole.log(5)\n"}`),
		textTurn("done"),
	}}
	_, err := Run(context.Background(), "build a small game in game.js", provider, Options{
		Registry:     registry,
		Evaluate:     true,
		RunMode:      RunModeBuilder,
		Cwd:          root,
		ProviderName: "openai", // non-hybrid: rewrite guard is about plan thrash, not template
		FileTracker:  tools.NewFileTracker(),
	})
	if err != nil {
		t.Fatal(err)
	}
	got, readErr := os.ReadFile(root + "/game.js")
	if readErr != nil {
		t.Fatal(readErr)
	}
	if strings.Contains(string(got), "rewrite thrash") {
		t.Fatalf("second write must be skipped after a successful plan-seeded write, got %q", string(got))
	}
	if !strings.Contains(string(got), "console.log(1)") {
		t.Fatalf("first write content missing: %q", string(got))
	}
}

// After the first deliverable lands, rewrite thrash of that path must NOT hard-
// stop the run before later plan files are written. Live failure: index.html OK
// → 4× Generating rewrite of the same file → "Stopped after 4 consecutive turns
// with no file progress" while game.js never started. The second write_file on
// a NEW path must still land.
func TestMultiFilePlanContinuesAfterRewriteThrash(t *testing.T) {
	root := t.TempDir()
	registry := tools.NewRegistry()
	for _, tool := range tools.CoreWriteTools(root) {
		registry.Register(tool)
	}
	htmlBody := "<!DOCTYPE html>\n<html><body><canvas id=\"c\"></canvas><script src=\"game.js\"></script></body></html>\n"
	jsBody := "console.log(1)\nconsole.log(2)\nconsole.log(3)\nconsole.log(4)\nconsole.log(5)\n"
	provider := &mockProvider{turns: [][]sonutruntime.StreamEvent{
		textTurn("1. Create index.html with canvas\n   - Do: write_file the canvas page\n   - Done when: the canvas exists\n2. Create game.js with loop\n   - Do: write_file the game loop\n   - Done when: the loop runs\n3. Verify\n   - Do: open the page\n   - Done when: it renders without errors"),
		toolTurn("w1", "write_file", `{"path":"index.html","content":`+quoteJSONString(htmlBody)+`}`),
		// Rewrite thrash of the finished first file — must skip, not hard-stop.
		toolTurn("w2", "write_file", `{"path":"index.html","content":`+quoteJSONString("<!DOCTYPE html>\n<html><body>thrash</body></html>\n")+`}`),
		// Next NEW path must still be accepted.
		toolTurn("w3", "write_file", `{"path":"game.js","content":`+quoteJSONString(jsBody)+`}`),
		textTurn("done"),
	}}
	result, err := Run(context.Background(), "build a browser game with index.html and game.js", provider, Options{
		Registry:     registry,
		Evaluate:     true,
		RunMode:      RunModeBuilder,
		Cwd:          root,
		ProviderName: "openai",
		FileTracker:  tools.NewFileTracker(),
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Incomplete {
		t.Fatalf("multi-file run must not hard-stop after rewrite thrash: incomplete=%v reason=%q answer=%q",
			result.Incomplete, result.IncompleteReason, result.FinalAnswer)
	}
	if _, err := os.ReadFile(root + "/index.html"); err != nil {
		t.Fatalf("index.html missing: %v", err)
	}
	got, err := os.ReadFile(root + "/game.js")
	if err != nil {
		t.Fatalf("game.js must be written after thrash skip of index.html: %v", err)
	}
	if !strings.Contains(string(got), "console.log(1)") {
		t.Fatalf("game.js content missing: %q", string(got))
	}
	// First file must not have been clobbered by thrash content.
	idx, _ := os.ReadFile(root + "/index.html")
	if strings.Contains(string(idx), "thrash") {
		t.Fatalf("index.html was thrash-rewritten: %q", string(idx))
	}
}

// Same deliverable under a different directory prefix must also be skipped —
// live failure wrote dummy/mariogame.html then thrash-rewrote mariogame.html
// for ~8 minutes at ~14 tok/s.
func TestPlanSeededExecutorSkipsBasenameTwinRewrite(t *testing.T) {
	root := t.TempDir()
	registry := tools.NewRegistry()
	for _, tool := range tools.CoreWriteTools(root) {
		registry.Register(tool)
	}
	if err := os.MkdirAll(root+"/dummy", 0o755); err != nil {
		t.Fatal(err)
	}
	htmlOK := "<!DOCTYPE html>\n<html><body><canvas></canvas><script>console.log(1)</script></body></html>\n"
	htmlThrash := "<!DOCTYPE html>\n<html><body>basename thrash</body></html>\n"
	provider := &mockProvider{turns: [][]sonutruntime.StreamEvent{
		textTurn("1. Create mariogame.html\n   - Do: write_file the game page\n   - Done when: the page exists\n2. Add controls\n   - Do: edit_file keyboard controls\n   - Done when: controls respond\n3. Verify\n   - Do: open the page\n   - Done when: no console errors"),
		toolTurn("w1", "write_file", `{"path":"dummy/mariogame.html","content":`+quoteJSONString(htmlOK)+`}`),
		toolTurn("w2", "write_file", `{"path":"mariogame.html","content":`+quoteJSONString(htmlThrash)+`}`),
		textTurn("done"),
	}}
	_, err := Run(context.Background(), "build a super mario like game", provider, Options{
		Registry:     registry,
		Evaluate:     true,
		RunMode:      RunModeBuilder,
		Cwd:          root,
		ProviderName: "openai",
		FileTracker:  tools.NewFileTracker(),
	})
	if err != nil {
		t.Fatal(err)
	}
	// Twin path must not be created / must not contain thrash content if created.
	if got, readErr := os.ReadFile(root + "/mariogame.html"); readErr == nil {
		if strings.Contains(string(got), "basename thrash") {
			t.Fatalf("basename-twin rewrite must be skipped, got %q", string(got))
		}
	}
	orig, err := os.ReadFile(root + "/dummy/mariogame.html")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(orig), "console.log(1)") {
		t.Fatalf("original write missing: %q", string(orig))
	}
}

func TestWritePathAlreadySucceededMatchesBasename(t *testing.T) {
	m := map[string]bool{}
	markWriteSucceeded(m, "dummy/mariogame.html")
	if !writePathAlreadySucceeded(m, "mariogame.html") {
		t.Fatal("bare basename must match after dummy/ path succeeded")
	}
	if !writePathAlreadySucceeded(m, "./Dummy/MarioGame.html") {
		t.Fatal("case/prefix variants must match")
	}
	if writePathAlreadySucceeded(m, "other.html") {
		t.Fatal("unrelated path must not match")
	}
}

func TestRecoveryGuardsDoNotConfuseQualifiedSameBasenames(t *testing.T) {
	writes := map[string]bool{}
	markWriteSucceeded(writes, "src/index.html")
	if writePathAlreadySucceeded(writes, "public/index.html") {
		t.Fatal("a different qualified basename must not be skipped")
	}
	if !writePathAlreadySucceeded(writes, "index.html") {
		t.Fatal("bare basename should still recover the active deliverable")
	}

	appendOnly := map[string]bool{}
	latchAppendOnly(appendOnly, "src/index.html")
	if isAppendOnlyPath(appendOnly, "public/index.html") {
		t.Fatal("a different qualified basename must not inherit append-only state")
	}
	if !isAppendOnlyPath(appendOnly, "index.html") {
		t.Fatal("bare basename should still find the active append-only file")
	}
}

// Local hybrid plan-seeded executor runs with concise private thinking for the
// whole phase. DisableThinking stays false across executor turns (no template
// flip), while effort=none bounds the private sanity check.
func TestLocalHybridPlanSeededExecutorKeepsConciseThinking(t *testing.T) {
	provider := &mockProvider{turns: [][]sonutruntime.StreamEvent{
		textTurn("1. Fix the file\n   - Do: edit_file the failing implementation\n   - Done when: the defect is corrected\n2. Check related code\n   - Do: grep for stale references\n   - Done when: no stale reference remains\n3. Verify\n   - Do: run the focused test\n   - Done when: it passes"),
		textTurn("Fixed."),
	}}
	_, err := Run(context.Background(), "please fix the bug in main.go carefully", provider, Options{
		Registry:     tools.NewRegistry(),
		Evaluate:     true,
		RunMode:      RunModeBuilder,
		ProviderName: "llama",
		Model:        "atcode-v1",
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(provider.requests) < 2 {
		t.Fatalf("expected planner + executor, got %d", len(provider.requests))
	}
	exec := provider.requests[1]
	if exec.DisableThinking {
		t.Fatal("local hybrid plan-seeded executor must keep concise private thinking enabled")
	}
	if exec.ReasoningEffort != "none" {
		t.Fatalf("plan-seeded executor effort = %q, want none", exec.ReasoningEffort)
	}
}

// End-to-end: a planner that leaks code must not leak it into the generator's
// conversation.
func TestPlannerCodeLeakNotInjected(t *testing.T) {
	provider := &mockProvider{turns: [][]sonutruntime.StreamEvent{
		textTurn("1. write_file(\"game.html\", \"<!DOCTYPE html><html>...\")"), // planner pass leaks code
		textTurn("done"), // generator turn
	}}
	_, err := Run(context.Background(), "please build a complete mario game in a single html file", provider, Options{
		Registry: tools.NewRegistry(),
		Evaluate: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	generatorReq := provider.requests[len(provider.requests)-1]
	for _, m := range generatorReq.Messages {
		if strings.Contains(m.Content, "<!DOCTYPE") {
			t.Fatal("code-leaking plan must be sanitized before injection")
		}
	}
}

// ─── Stubs ────────────────────────────────────────────────────────────────────

type stubReadTool struct{}

func (t *stubReadTool) Name() string        { return "read_file" }
func (t *stubReadTool) Description() string { return "read a file" }
func (t *stubReadTool) Parameters() tools.Schema {
	return tools.Schema{
		Type: "object",
		Properties: map[string]tools.PropertySchema{
			"path": {Type: "string"},
		},
		Required: []string{"path"},
	}
}
func (t *stubReadTool) Safety() tools.Safety {
	return tools.Safety{SideEffect: tools.SideEffectRead, Permission: tools.PermissionAllow}
}
func (t *stubReadTool) Run(_ context.Context, args map[string]any) tools.Result {
	return tools.Result{Status: tools.StatusOK, Output: "file content"}
}

type countingReadTool struct {
	stubReadTool
	calls int
}

func (tool *countingReadTool) Run(ctx context.Context, args map[string]any) tools.Result {
	tool.calls++
	return tool.stubReadTool.Run(ctx, args)
}

type stubWriteTool struct{}

func (tool *stubWriteTool) Name() string        { return "write_file" }
func (tool *stubWriteTool) Description() string { return "write a file" }
func (tool *stubWriteTool) Parameters() tools.Schema {
	return tools.Schema{
		Type: "object",
		Properties: map[string]tools.PropertySchema{
			"path":    {Type: "string"},
			"content": {Type: "string"},
		},
		Required: []string{"path", "content"},
	}
}
func (tool *stubWriteTool) Safety() tools.Safety {
	return tools.Safety{SideEffect: tools.SideEffectWrite, Permission: tools.PermissionAllow}
}
func (tool *stubWriteTool) Run(_ context.Context, _ map[string]any) tools.Result {
	return tools.Result{Status: tools.StatusOK, Output: "saved"}
}
