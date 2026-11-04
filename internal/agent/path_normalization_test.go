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

func TestCanonicalToolPathForCwdRemovesRedundantCurrentDirectory(t *testing.T) {
	root := t.TempDir()
	cwd := filepath.Join(root, "dummy")
	if err := os.MkdirAll(cwd, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(cwd, "index.html"), []byte("<html></html>"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := canonicalToolPathForCwd(cwd, "dummy/index.html"); got != "index.html" {
		t.Fatalf("canonical path = %q, want index.html", got)
	}
}

// Live supermario thrash: after /cd into .../sonut/test the model wrote
// path=sonut/test/supermario.html and created a nested shadow tree, then
// looped identical edit_file until hard-stop. Multi-segment ancestry must strip.
func TestCanonicalToolPathForCwdStripsMultiSegmentAncestry(t *testing.T) {
	parent := t.TempDir()
	cwd := filepath.Join(parent, "sonut", "test")
	if err := os.MkdirAll(cwd, 0o755); err != nil {
		t.Fatal(err)
	}
	if got := canonicalToolPathForCwd(cwd, "sonut/test/supermario.html"); got != "supermario.html" {
		t.Fatalf("canonical(sonut/test/supermario.html) = %q, want supermario.html", got)
	}
	if got := canonicalToolPathForCwd(cwd, "sonut/test"); got != "." {
		t.Fatalf("canonical(sonut/test) = %q, want .", got)
	}
	// Unrelated multi-segment path stays put.
	if got := canonicalToolPathForCwd(cwd, "assets/sprites.png"); got != "assets/sprites.png" {
		t.Fatalf("unrelated path rewritten: %q", got)
	}
}

func TestCanonicalToolPathForCwdDoesNotCollapseRealNestedDirectory(t *testing.T) {
	root := t.TempDir()
	cwd := filepath.Join(root, "dummy")
	if err := os.MkdirAll(filepath.Join(cwd, "dummy"), 0o755); err != nil {
		t.Fatal(err)
	}
	if got := canonicalToolPathForCwd(cwd, "dummy/index.html"); got != "dummy/index.html" {
		t.Fatalf("real nested path was rewritten to %q", got)
	}
}

func TestCanonicalizeToolCallPathForCwdFeedsReadGuardOneSpelling(t *testing.T) {
	call := ToolCall{ID: "r1", Name: "read_file", Arguments: `{"path":"dummy/index.html"}`}
	got := canonicalizeToolCallPathForCwd(call, "/workspace/dummy")
	if got.Arguments != `{"path":"index.html"}` {
		t.Fatalf("arguments = %s, want canonical path", got.Arguments)
	}
}

// After /cd into .../test the model invents path="/test". Rewrite before the
// sandbox evaluates so list_directory never dies as outside_workspace.
func TestCanonicalToolPathForCwdRewritesBareAbsoluteBasename(t *testing.T) {
	root := t.TempDir()
	cwd := filepath.Join(root, "test")
	if err := os.MkdirAll(cwd, 0o755); err != nil {
		t.Fatal(err)
	}
	if got := canonicalToolPathForCwd(cwd, "/test"); got != "." {
		t.Fatalf("canonical(/test) = %q, want .", got)
	}
	if got := canonicalToolPathForCwd(cwd, "/test/mario.html"); got != "mario.html" {
		t.Fatalf("canonical(/test/mario.html) = %q, want mario.html", got)
	}
	// Full real cwd path collapses to "."
	if got := canonicalToolPathForCwd(cwd, cwd); got != "." {
		t.Fatalf("canonical(full cwd) = %q, want .", got)
	}
	// Unrelated absolute host path stays absolute.
	if got := canonicalToolPathForCwd(cwd, "/etc/hosts"); got != "/etc/hosts" {
		t.Fatalf("canonical(/etc/hosts) = %q, want unchanged", got)
	}
}

func TestCanonicalizeToolCallRewritesListDirectoryAbsoluteBasename(t *testing.T) {
	call := ToolCall{ID: "l1", Name: "list_directory", Arguments: `{"path":"/test"}`}
	got := canonicalizeToolCallPathForCwd(call, "/Users/aslam/Desktop/aistatine/sonut/test")
	if got.Arguments != `{"path":"."}` {
		t.Fatalf("arguments = %s, want path=.", got.Arguments)
	}
}

// This is the production-shaped regression: the session is already inside
// dummy/, the model alternates between dummy/index.html and index.html, then
// writes with the duplicated spelling. The run must execute one read, write
// the current-cwd file, and never create dummy/dummy/index.html.
func TestSuperMarioWorkflowDoesNotReReadOrLoseCurrentCwdFile(t *testing.T) {
	root := t.TempDir()
	cwd := filepath.Join(root, "dummy")
	if err := os.MkdirAll(cwd, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(cwd, "index.html"), []byte("<!doctype html>\n<html>\n<body>old</body>\n</html>\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	registry := tools.NewRegistry()
	for _, tool := range tools.CoreTools(root) {
		registry.Register(tool)
	}
	provider := &mockProvider{turns: [][]sonutruntime.StreamEvent{
		toolTurn("r1", "read_file", `{"path":"dummy/index.html"}`),
		toolTurn("r2", "read_file", `{"path":"index.html"}`),
		toolTurn("w1", "write_file", `{"path":"dummy/index.html","content":"<!doctype html>\n<html>\n<body>platformer</body>\n</html>\n","overwrite":true}`),
		textTurn("Platformer ready."),
	}}
	var results []ToolResult
	result, err := Run(context.Background(), "create a fast Super Mario-style platformer game", provider, Options{
		Registry:        registry,
		Cwd:             cwd,
		RunMode:         RunModeBuilder,
		UnifiedWorkflow: true,
		MaxTurns:        4,
		ProviderName:    "openai",
		FileTracker:     tools.NewFileTracker(),
		OnToolResult: func(r ToolResult) {
			results = append(results, r)
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.FinalAnswer != "Platformer ready." {
		t.Fatalf("final answer = %q, want completion without an idle/retry loop", result.FinalAnswer)
	}
	if len(results) != 3 {
		t.Fatalf("tool results = %d, want read + suppressed reread + write: %#v", len(results), results)
	}
	if results[1].Meta["internal_dedup"] != "true" {
		t.Fatalf("second read was not suppressed: %#v", results[1])
	}
	got, err := os.ReadFile(filepath.Join(cwd, "index.html"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(got), "platformer") {
		t.Fatalf("current-cwd file was not updated: %q", got)
	}
	if _, err := os.Stat(filepath.Join(cwd, "dummy", "index.html")); !os.IsNotExist(err) {
		t.Fatalf("duplicated cwd file was created, stat err=%v", err)
	}
}

func TestUnifiedWorkflowPromotesPlanAndContinuesAcrossBuildSteps(t *testing.T) {
	root := t.TempDir()
	registry := tools.NewRegistry()
	for _, tool := range tools.CoreTools(root) {
		registry.Register(tool)
	}
	provider := &mockProvider{turns: [][]sonutruntime.StreamEvent{
		toolTurn("p1", "update_plan", `{"plan":[{"content":"Create index.html shell","criteria":"index.html exists","status":"in_progress"},{"content":"Create game.js loop","criteria":"game.js exists"},{"content":"Verify the game","criteria":"focused check passes"}]}`),
		toolTurn("w1", "write_file", `{"path":"index.html","content":"<!doctype html>\n<html>\n<body><canvas id=\"game\"></canvas><script src=\"game.js\"></script></body>\n</html>\n"}`),
		toolTurn("w2", "write_file", `{"path":"game.js","content":"const canvas = document.querySelector('canvas');\nconst ctx = canvas.getContext('2d');\nfunction loop(){ ctx.clearRect(0,0,canvas.width,canvas.height); requestAnimationFrame(loop); }\nloop();\n"}`),
		textTurn("Game files created and ready for verification."),
	}}
	var planUpdates [][]tools.PlanItem
	result, err := Run(context.Background(), "create a Super Mario-style platformer game", provider, Options{
		Registry:        registry,
		Cwd:             root,
		RunMode:         RunModeBuilder,
		UnifiedWorkflow: true,
		MaxTurns:        4,
		ProviderName:    "openai",
		FileTracker:     tools.NewFileTracker(),
		OnPlanProgress: func(items []tools.PlanItem) {
			planUpdates = append(planUpdates, append([]tools.PlanItem(nil), items...))
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.FinalAnswer != "Game files created and ready for verification." {
		t.Fatalf("final answer = %q", result.FinalAnswer)
	}
	if len(planUpdates) == 0 || len(planUpdates[0]) != 3 {
		t.Fatalf("model plan was not captured: %#v", planUpdates)
	}
	if len(provider.requests) < 3 || provider.requests[1].DisableThinking || provider.requests[2].DisableThinking {
		t.Fatalf("post-plan turns did not switch to concise execution: requests=%d", len(provider.requests))
	}
	for _, name := range []string{"index.html", "game.js"} {
		if _, err := os.Stat(filepath.Join(root, name)); err != nil {
			t.Fatalf("planned deliverable %s missing after continuation: %v", name, err)
		}
	}
}

func TestUnifiedWorkflowContinuesAfterInterruptedPrompt(t *testing.T) {
	root := t.TempDir()
	registry := tools.NewRegistry()
	for _, tool := range tools.CoreTools(root) {
		registry.Register(tool)
	}
	provider := &mockProvider{turns: [][]sonutruntime.StreamEvent{
		toolTurn("p1", "update_plan", `{"plan":[{"content":"Create index.html","criteria":"index.html exists"},{"content":"Create game.js","criteria":"game.js exists"}]}`),
		toolTurn("w1", "write_file", `{"path":"index.html","content":"<!doctype html>\n<html><body><canvas></canvas></body></html>\n"}`),
		textTurn("I’ll continue with game.js:"),
		toolTurn("w2", "write_file", `{"path":"game.js","content":"requestAnimationFrame(function loop(){ requestAnimationFrame(loop); });\n"}`),
		textTurn("The platformer files are ready."),
	}}
	result, err := Run(context.Background(), "create a Super Mario-style platformer game", provider, Options{
		Registry:                registry,
		Cwd:                     root,
		RunMode:                 RunModeBuilder,
		UnifiedWorkflow:         true,
		RequireCompletionSignal: true,
		MaxTurns:                5,
		ProviderName:            "openai",
		FileTracker:             tools.NewFileTracker(),
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Incomplete {
		t.Fatalf("interrupted prompt incorrectly stopped run: reason=%q answer=%q", result.IncompleteReason, result.FinalAnswer)
	}
	if result.FinalAnswer != "The platformer files are ready." {
		t.Fatalf("final answer = %q", result.FinalAnswer)
	}
	if _, err := os.Stat(filepath.Join(root, "game.js")); err != nil {
		t.Fatalf("remaining plan step did not continue after interruption: %v", err)
	}
}
