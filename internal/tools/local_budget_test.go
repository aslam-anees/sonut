package tools

import (
	"context"
	"testing"
)

type budgetProbeTool struct {
	baseTool
	deferred bool
}

func (t budgetProbeTool) Run(context.Context, map[string]any) Result {
	return Result{Status: StatusOK}
}

func (t budgetProbeTool) Deferred() bool { return t.deferred }

func TestApplyLocalHybridToolBudgetDefersNonEager(t *testing.T) {
	reg := NewRegistry()
	reg.Register(budgetProbeTool{baseTool: baseTool{name: "read_file"}})
	reg.Register(budgetProbeTool{baseTool: baseTool{name: "web_search"}})
	reg.Register(budgetProbeTool{baseTool: baseTool{name: "lsp_navigate"}})
	reg.Register(budgetProbeTool{baseTool: baseTool{name: "mcp_demo_x"}, deferred: true})

	ApplyLocalHybridToolBudget(reg)

	read, ok := reg.Get("read_file")
	if !ok || IsDeferred(read) {
		t.Fatalf("read_file must stay eager, deferred=%v present=%v", ok && IsDeferred(read), ok)
	}
	web, ok := reg.Get("web_search")
	if !ok || !IsDeferred(web) {
		t.Fatalf("web_search must stay reachable but defer its schema on the local cold path, deferred=%v present=%v", ok && IsDeferred(web), ok)
	}
	lsp, ok := reg.Get("lsp_navigate")
	if !ok || !IsDeferred(lsp) {
		t.Fatalf("lsp_navigate must be force-deferred, present=%v", ok)
	}
	mcp, ok := reg.Get("mcp_demo_x")
	if !ok || !IsDeferred(mcp) {
		t.Fatalf("already-deferred MCP tools must stay deferred, present=%v", ok)
	}
	if _, ok := reg.Get(ToolSearchToolName); !ok {
		t.Fatal("tool_search must be registered so deferred tools stay reachable")
	}
}

func TestApplyLocalHybridToolBudgetIdempotent(t *testing.T) {
	reg := NewRegistry()
	reg.Register(budgetProbeTool{baseTool: baseTool{name: "web_fetch"}})
	ApplyLocalHybridToolBudget(reg)
	ApplyLocalHybridToolBudget(reg)
	web, ok := reg.Get("web_fetch")
	if !ok || !IsDeferred(web) {
		t.Fatalf("second apply must leave web_fetch deferred, present=%v", ok)
	}
}

// Regression: supermario exec thrash (tools=21). CoreToolsScoped re-registration
// AFTER ApplyLocalHybridToolBudget used to wipe force-deferred wrappers and
// re-advertise every core schema on every hybrid turn. The fix is to re-apply
// the budget after scoped registration (exec.go); this test locks that contract.
func TestApplyLocalHybridToolBudgetSurvivesCoreReRegistration(t *testing.T) {
	reg := NewRegistry()
	reg.Register(budgetProbeTool{baseTool: baseTool{name: "read_file"}})
	reg.Register(budgetProbeTool{baseTool: baseTool{name: "bash"}})
	reg.Register(budgetProbeTool{baseTool: baseTool{name: "web_search"}})
	ApplyLocalHybridToolBudget(reg)
	if bash, ok := reg.Get("bash"); !ok || !IsDeferred(bash) {
		t.Fatalf("bash should be deferred before re-register: present=%v deferred=%v", ok, ok && IsDeferred(bash))
	}
	// Simulate CoreToolsScoped overwrite with fresh non-deferred instances.
	reg.Register(budgetProbeTool{baseTool: baseTool{name: "bash"}})
	reg.Register(budgetProbeTool{baseTool: baseTool{name: "web_search"}})
	reg.Register(budgetProbeTool{baseTool: baseTool{name: "read_file"}})
	if bash, ok := reg.Get("bash"); ok && IsDeferred(bash) {
		t.Fatal("fresh re-register should produce a non-deferred bash (simulating the bug)")
	}
	// Budget re-apply restores the hybrid cold-path contract.
	ApplyLocalHybridToolBudget(reg)
	if bash, ok := reg.Get("bash"); !ok || !IsDeferred(bash) {
		t.Fatal("bash must be deferred after scoped re-registration + budget re-apply")
	}
	if web, ok := reg.Get("web_search"); !ok || !IsDeferred(web) {
		t.Fatal("web_search must be deferred after budget re-apply")
	}
	if read, ok := reg.Get("read_file"); !ok || IsDeferred(read) {
		t.Fatal("read_file must stay eager")
	}
}

func TestIsLocalLlamaProviderName(t *testing.T) {
	for _, name := range []string{"llama", "llama.cpp", "local", "LLAMA"} {
		if !IsLocalLlamaProviderName(name) {
			t.Errorf("IsLocalLlamaProviderName(%q) = false, want true", name)
		}
	}
	for _, name := range []string{"", "openai", "anthropic", "spacexai"} {
		if IsLocalLlamaProviderName(name) {
			t.Errorf("IsLocalLlamaProviderName(%q) = true, want false", name)
		}
	}
}
