package agent

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"testing"

	"github.com/aistatine/sonut/internal/llama"
	"github.com/aistatine/sonut/internal/tools"
)

// TestFirstRequestPayloadBudget builds the EXACT first-request body the TUI
// sends to the local llama.cpp backend — the compact system prompt with a
// realistic workspace (git repo + AGENTS.md), every core tool serialized with
// the SAME compaction StreamCompletion applies — and prints a byte breakdown
// so payload regressions are visible and bounded.
//
// The asserted budgets are intentionally generous ceilings, not tight targets:
// they exist to catch a tool schema or context blob quietly ballooning the
// cold-prefill cost (observed live as "still generating… 41s" with 0 tok on
// Bonsai-27B).
func TestFirstRequestPayloadBudget(t *testing.T) {
	cwd := t.TempDir()
	// Realistic workspace: git repo with a branch, project guidelines, files.
	mustWrite := func(rel, content string) {
		t.Helper()
		path := filepath.Join(cwd, rel)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	mustWrite("go.mod", "module example.test/payload\n")
	mustWrite("AGENTS.md", "Use go test ./... before claiming success.\nPrefer table-driven tests.\n")
	mustWrite(filepath.Join(".git", "HEAD"), "ref: refs/heads/main\n")
	mustWrite("cmd/app/main.go", "package main\n")
	mustWrite("internal/core/core.go", "package core\n")

	registry := tools.NewRegistry()
	for _, tool := range tools.CoreTools(cwd) {
		registry.Register(tool)
	}
	// The TUI applies the local-hybrid eager/deferred budget after the registry
	// is complete. Keep this benchmark on the production path; measuring all
	// core schemas here understated the prefill win and let the eager set drift.
	tools.ApplyLocalHybridToolBudget(registry)

	opts := Options{
		Cwd:          cwd,
		Model:        "local",
		ProviderName: "llama",
		Registry:     registry,
		// The interactive TUI defaults to unsafe mode (internal/tui/model.go),
		// which advertises every core tool — auto mode would hide the write and
		// shell tools and understate the real payload.
		PermissionMode: PermissionModeUnsafe,
		// Matches config's defaultDeferThreshold; the local budget makes only the
		// minimum coding set eager and sends the rest through tool_search.
		DeferThreshold: 3,
	}

	systemPrompt := BuildSystemPrompt(opts)
	systemBytes := len(systemPrompt)

	// Serialize each tool exactly as llama's StreamCompletion does: shortened
	// description, parameter tree stripped of nested descriptions, wrapped in
	// {"type":"function","function":{...}}.
	type toolFunction struct {
		Name        string         `json:"name"`
		Description string         `json:"description"`
		Parameters  map[string]any `json:"parameters"`
	}
	type toolDef struct {
		Type     string       `json:"type"`
		Function toolFunction `json:"function"`
	}
	defs := BuildToolDefinitionsPreview(opts)
	if len(defs) == 0 {
		t.Fatal("expected core tools to be advertised on the first turn")
	}
	type toolSize struct {
		name  string
		bytes int
	}
	sizes := make([]toolSize, 0, len(defs))
	totalToolBytes := 0
	for _, d := range defs {
		params := d.Parameters
		if params == nil {
			params = map[string]any{"type": "object", "properties": map[string]any{}}
		}
		b, err := json.Marshal(toolDef{
			Type: "function",
			Function: toolFunction{
				Name:        d.Name,
				Description: llama.CompactToolDescription(d.Description),
				Parameters:  llama.CompactToolParameters(params),
			},
		})
		if err != nil {
			t.Fatalf("marshal tool %s: %v", d.Name, err)
		}
		sizes = append(sizes, toolSize{name: d.Name, bytes: len(b)})
		totalToolBytes += len(b)
	}
	sort.Slice(sizes, func(i, j int) bool { return sizes[i].bytes > sizes[j].bytes })

	userMsg := "Fix the failing test in internal/core."
	totalBytes := systemBytes + totalToolBytes + len(userMsg)

	t.Logf("=== first-request payload breakdown (chars/4 ~= tokens) ===")
	t.Logf("system prompt: %d bytes (~%d tokens)", systemBytes, systemBytes/4)
	t.Logf("tool schemas (%d tools): %d bytes (~%d tokens)", len(sizes), totalToolBytes, totalToolBytes/4)
	for _, s := range sizes {
		t.Logf("  %-28s %6d bytes (~%d tokens)", s.name, s.bytes, s.bytes/4)
	}
	t.Logf("user message: %d bytes", len(userMsg))
	t.Logf("TOTAL: %d bytes (~%d tokens)", totalBytes, totalBytes/4)

	// Budget ceilings. The compact local-llama system prompt plus pinned
	// workspace context must stay small; tool schemas are the bulk and are
	// bounded per-tool and in aggregate.
	// Measured 2026-08 after the six-tool local budget: system 670 B, schemas
	// 3,060 B, total 3,768 B. Keep these ceilings tight enough to catch prompt
	// growth before it becomes a visible cold-prefill regression.
	const (
		maxSystemBytes  = 6 << 10 // compact prompt + workspace context/seed (real repos are bigger than this fixture)
		maxPerToolBytes = 1200    // one tool schema after llama compaction
		maxToolsBytes   = 4 << 10 // aggregate local eager-schema budget
		maxTotalBytes   = 8 << 10 // whole first request (~2k tokens ceiling)
	)
	if len(sizes) > 6 {
		t.Errorf("local first turn advertises %d tools, want <= 6", len(sizes))
	}
	if systemBytes > maxSystemBytes {
		t.Errorf("system prompt is %d bytes, want <= %d", systemBytes, maxSystemBytes)
	}
	for _, s := range sizes {
		if s.bytes > maxPerToolBytes {
			t.Errorf("tool %s schema is %d bytes, want <= %d", s.name, s.bytes, maxPerToolBytes)
		}
	}
	if totalToolBytes > maxToolsBytes {
		t.Errorf("tool schemas total %d bytes, want <= %d", totalToolBytes, maxToolsBytes)
	}
	if totalBytes > maxTotalBytes {
		t.Errorf("first-request payload is %d bytes, want <= %d", totalBytes, maxTotalBytes)
	}
}
