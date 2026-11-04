package tools

import "strings"

// Local hybrid-attention models (atcode / Bonsai / OmniCoder / Qwopus) do NOT
// reuse the KV-cache prompt prefix across requests — llama.cpp's cache_prompt
// and --cache-reuse cannot roll back their recurrent state. Measured live on
// M-series: every agent turn re-prefills system + tools from zero at ~60 tok/s,
// so shipping all ~18–24 core tool schemas (~2k+ tokens) costs 30–90 s of dead
// air on EVERY message — the "↑ 0 tok for 100s then a short reply" irritation.
//
// ApplyLocalHybridToolBudget keeps a small always-eager coding set and marks
// everything else Deferred() so it rides behind tool_search. Tools stay
// available (model loads them on demand); only the per-turn schema tax shrinks.
// The eager set is the minimum day-to-day coding surface; search, directory,
// batch-read, append, patch, web/LSP/skill/specialist schemas load via
// tool_search when needed and stay loaded for the run.

// LocalEagerToolNames are always advertised on local hybrid runs. Everything
// else is force-deferred so tool schemas stay off the cold-prefill path.
var LocalEagerToolNames = map[string]bool{
	"read_file":    true,
	"exec_command": true,
	"edit_file":    true,
	"write_file":   true,
	"update_plan":  true,
	// Keep one tool for each common operation on the cold path. Equivalent or
	// occasional tools (bash beside exec_command, glob beside grep/list, web,
	// ask-user, browser, LSP) remain reachable through tool_search without
	// charging every hybrid-model prefill. This mirrors Grok Build's narrow
	// default editing surface and avoids advertising aliases that weak local
	// models otherwise bounce between.
	ToolSearchToolName: true,
}

// forceDeferredTool wraps a Tool so IsDeferred reports true without changing
// any other behaviour. Used to put non-eager core tools behind tool_search on
// local hybrid backends.
type forceDeferredTool struct {
	Tool
}

// Deferred marks the wrapped tool as load-on-demand.
func (f forceDeferredTool) Deferred() bool { return true }

// ApplyLocalHybridToolBudget re-registers every non-eager tool as Deferred and
// ensures tool_search is present so the model can still reach hidden tools.
// Safe to call multiple times (already-deferred tools are left alone).
//
// Call this ONLY for local llama.cpp / hybrid providers — cloud models with
// prompt caching prefer the full eager schema list.
func ApplyLocalHybridToolBudget(registry *Registry) {
	if registry == nil {
		return
	}
	for _, tool := range registry.All() {
		name := tool.Name()
		if LocalEagerToolNames[name] {
			continue
		}
		if name == ToolSearchToolName {
			continue
		}
		// Already deferred (MCP, swarm, etc.) — leave as-is.
		if IsDeferred(tool) {
			continue
		}
		registry.Register(forceDeferredTool{Tool: tool})
	}
	// tool_search is required for the deferred set to be reachable.
	if _, ok := registry.Get(ToolSearchToolName); !ok {
		registry.Register(NewToolSearchTool(registry))
	}
}

// IsLocalLlamaProviderName reports whether name is the local llama.cpp backend.
// Empty is intentionally NOT local — cloud/default providers must keep full
// eager tool schemas for models that cache prompts server-side.
func IsLocalLlamaProviderName(name string) bool {
	switch strings.ToLower(strings.TrimSpace(name)) {
	case "llama", "llama.cpp", "local":
		return true
	default:
		return false
	}
}
