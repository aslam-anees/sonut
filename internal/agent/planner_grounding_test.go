package agent

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/aistatine/sonut/internal/sonutruntime"
	"github.com/aistatine/sonut/internal/tools"
)

func TestWorkspaceHasVisibleEntriesDistinguishesEmptyGenerationDir(t *testing.T) {
	empty := t.TempDir()
	if workspaceHasVisibleEntries(empty) {
		t.Fatal("empty generation directory should not require planner grounding")
	}
	if err := os.WriteFile(filepath.Join(empty, "index.html"), []byte("<html></html>"), 0o644); err != nil {
		t.Fatal(err)
	}
	if !workspaceHasVisibleEntries(empty) {
		t.Fatal("workspace with a visible file should require grounding")
	}
}

func TestPlannerGroundingRoutingKeepsGreenfieldBuildsFast(t *testing.T) {
	for _, prompt := range []string{
		"build a browser game from this reference image",
		"make the code look like the screenshot",
		"create a polished landing page",
	} {
		if plannerNeedsGrounding(prompt) {
			t.Fatalf("greenfield prompt should skip the extra grounding pass: %q", prompt)
		}
	}
	for _, prompt := range []string{
		"fix the existing index.html layout",
		"debug game.js test failure",
		"update internal/tui/model.go",
	} {
		if !plannerNeedsGrounding(prompt) {
			t.Fatalf("maintenance prompt should gather workspace evidence: %q", prompt)
		}
	}
}

func TestVisualPlannerSkipsGroundingForEmptyGenerationDir(t *testing.T) {
	provider := &mockProvider{turns: [][]sonutruntime.StreamEvent{
		textTurn("1. Create index.html\n   - Do: write_file the page shell and visual layout\n   - Done when: the page structure exists\n2. Create game.js\n   - Do: write_file the interaction logic\n   - Done when: the page responds\n3. Verify the result\n   - Do: open the page in the browser\n   - Done when: it renders without errors"),
		textTurn("The game files are ready."),
	}}
	empty := t.TempDir()
	_, err := Run(context.Background(), "build a playable browser game from this reference image", provider, Options{
		Registry: tools.NewRegistry(),
		Evaluate: true,
		Cwd:      empty,
		Images:   []sonutruntime.ImageBlock{{MediaType: "image/png", Data: []byte("reference")}},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(provider.requests) != 2 {
		t.Fatalf("empty visual workspace should use planner + executor only, got %d provider requests", len(provider.requests))
	}
	if got := requestImageCount(provider.requests[0]); got != 1 {
		t.Fatalf("visual planner should receive the reference image, got %d images", got)
	}
	if got := requestImageCount(provider.requests[1]); got != 0 {
		t.Fatalf("executor should not resend the reference image, got %d images", got)
	}
}

func requestImageCount(request sonutruntime.CompletionRequest) int {
	count := 0
	for _, message := range request.Messages {
		count += len(message.Images)
	}
	return count
}

type mockTool struct {
	name string
}

func (m *mockTool) Name() string        { return m.name }
func (m *mockTool) Description() string { return "mock tool" }
func (m *mockTool) Parameters() tools.Schema {
	return tools.Schema{Type: "object", Properties: map[string]tools.PropertySchema{}}
}
func (m *mockTool) Safety() tools.Safety { return tools.Safety{Permission: tools.PermissionAllow} }
func (m *mockTool) Run(ctx context.Context, args map[string]any) tools.Result {
	return tools.Result{Status: tools.StatusOK, Output: "ok"}
}

// TestPlannerGroundingPassGathersContext tests that runPlannerGroundingTurn
// executes read-only tools and returns gathered facts for the text planner pass.
func TestPlannerGroundingPassGathersContext(t *testing.T) {
	provider := &mockProvider{turns: [][]sonutruntime.StreamEvent{
		// Turn 1: Grounding call to list_directory
		toolTurn("call_grounding_1", "list_directory", `{"path":"."}`),
		// Turn 2: Grounding call to finish_grounding with facts
		toolTurn("call_grounding_2", "finish_grounding", `{"context":"index.html has canvas 800x600, game.js has gameLoop"}`),
		// Turn 3: Planner text pass
		textTurn("1. Edit game.js\n   - Do: update gameLoop with canvas 800x600\n   - Done when: canvas renders"),
		// Turn 4: Generator turn
		textTurn("Done implementing."),
	}}

	reg := tools.NewRegistry()
	reg.Register(&mockTool{name: "list_directory"})

	ctx := context.Background()
	result, err := Run(ctx, "implement the canvas game in game.js and index.html", provider, Options{
		Registry: reg,
		Evaluate: true,
		Cwd:      ".",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.FinalAnswer != "Done implementing." {
		t.Fatalf("unexpected final answer: %q", result.FinalAnswer)
	}

	// Verify planner request received grounding context
	var foundGroundingContext bool
	for _, req := range provider.requests {
		for _, msg := range req.Messages {
			if strings.Contains(msg.Content, "grounding context") && strings.Contains(msg.Content, "index.html has canvas 800x600") {
				foundGroundingContext = true
				break
			}
		}
	}
	if !foundGroundingContext {
		t.Fatal("expected text planning pass to contain grounding context facts")
	}
}

// TestNeedsQuickWebSearchExpanded tests that budget/cost/factual questions trigger quick web search.
func TestNeedsQuickWebSearchExpanded(t *testing.T) {
	queries := []string{
		"what is budget of odysseus 2026",
		"what is the cost of living in Tokyo",
		"box office revenue of Avatar 2",
		"who directed Oppenheimer",
		"who was the first caliph of Islam",
		"explain the history of algebra",
		"future of artificial intelligence",
		"ceo of openai",
		"The Odyssey by Christopher Nolan",
	}

	for _, q := range queries {
		if !needsQuickWebSearch(q) {
			t.Errorf("needsQuickWebSearch(%q) = false, want true", q)
		}
	}
	if classifyTurnShape("history of algebra") != TurnShapeQuestion {
		t.Fatal("history-shaped knowledge ask should route as a question")
	}

	// Identity questions should NOT trigger web search
	if needsQuickWebSearch("who are you") {
		t.Error("needsQuickWebSearch(\"who are you\") = true, want false")
	}
}

func TestExplicitBrowserRequestsStayQuickButKeepBrowserIntent(t *testing.T) {
	for _, prompt := range []string{
		"open airbnb webpage",
		"use browser to open https://www.airbnb.com",
		"navigate to terminal-browser.com",
		"open airbnb .com",
	} {
		if !needsQuickBrowser(prompt) {
			t.Errorf("needsQuickBrowser(%q) = false, want true", prompt)
		}
		if RouteRunMode(prompt) != RunModeQuick {
			t.Errorf("RouteRunMode(%q) = %q, want quick", prompt, RouteRunMode(prompt))
		}
	}
	if needsQuickBrowser("what is a browser") {
		t.Fatal("a browser definition question should not become a browser action")
	}
}

func TestCodebaseQuestionStaysLocal(t *testing.T) {
	prompt := "what is in this codebase"
	if needsQuickWebSearch(prompt) {
		t.Fatalf("codebase question must not use web search")
	}
	if RouteRunMode(prompt) == RunModeQuick {
		t.Fatalf("codebase question must not use flash route")
	}
	if !looksTechnicalShape(prompt) {
		t.Fatalf("codebase question should be recognized as workspace-related")
	}
}
