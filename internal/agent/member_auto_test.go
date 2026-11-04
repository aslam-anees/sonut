package agent

import (
	"context"
	"testing"

	"github.com/aistatine/sonut/internal/tools"
)

type safetyTool struct {
	name   string
	safety tools.Safety
}

func (t safetyTool) Name() string        { return t.name }
func (t safetyTool) Description() string { return "test tool" }
func (t safetyTool) Parameters() tools.Schema {
	return tools.Schema{Type: "object", AdditionalProperties: false}
}
func (t safetyTool) Safety() tools.Safety { return t.safety }
func (t safetyTool) Run(context.Context, map[string]any) tools.Result {
	return tools.Result{Status: tools.StatusOK}
}

// Auto advertises every grantable tool. member-auto remains narrower for
// headless specialists: write/edit + shell, but not network or denied tools.
func TestToolAdvertisedMemberAuto(t *testing.T) {
	write := safetyTool{name: "write_file", safety: tools.Safety{SideEffect: tools.SideEffectWrite, Permission: tools.PermissionPrompt}}
	shell := safetyTool{name: "bash", safety: tools.Safety{SideEffect: tools.SideEffectShell, Permission: tools.PermissionPrompt}}
	read := safetyTool{name: "read_file", safety: tools.Safety{SideEffect: tools.SideEffectRead, Permission: tools.PermissionAllow}}
	network := safetyTool{name: "net_tool", safety: tools.Safety{SideEffect: tools.SideEffectNetwork, Permission: tools.PermissionPrompt}}
	denied := safetyTool{name: "blocked", safety: tools.Safety{SideEffect: tools.SideEffectRead, Permission: tools.PermissionDeny}}

	for _, tool := range []tools.Tool{write, shell, read, network} {
		if !ToolAdvertised(tool, PermissionModeAuto) {
			t.Fatalf("auto-approve must advertise grantable tool %q", tool.Name())
		}
	}
	if ToolAdvertised(denied, PermissionModeAuto) {
		t.Fatal("auto-approve must not override an absolute denied-tool policy")
	}

	for _, tool := range []tools.Tool{write, shell, read} {
		if !ToolAdvertised(tool, PermissionModeMemberAuto) {
			t.Fatalf("member-auto must advertise %q", tool.Name())
		}
	}
	if ToolAdvertised(network, PermissionModeMemberAuto) {
		t.Fatal("member-auto must NOT advertise a network prompt tool")
	}
	if ToolAdvertised(denied, PermissionModeMemberAuto) {
		t.Fatal("member-auto must NOT advertise a denied tool")
	}
}

func TestAutoApproveRunsPromptToolWithoutCallback(t *testing.T) {
	tool := safetyTool{name: "grantable", safety: tools.Safety{
		SideEffect: tools.SideEffectWrite,
		Permission: tools.PermissionPrompt,
	}}
	registry := tools.NewRegistry()
	registry.Register(tool)
	result, err := executeToolCall(
		context.Background(),
		registry,
		ToolCall{ID: "auto-1", Name: tool.Name(), Arguments: `{}`},
		PermissionModeAuto,
		Options{
			OnPermissionRequest: func(context.Context, PermissionRequest) (PermissionDecision, error) {
				t.Fatal("auto-approve must not open a permission prompt")
				return PermissionDecision{}, nil
			},
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	if result.Status != tools.StatusOK {
		t.Fatalf("auto-approved tool failed: %#v", result)
	}
}

func TestAutoApproveKeepsAbsoluteDeniedToolUnavailable(t *testing.T) {
	tool := safetyTool{name: "absolute-deny", safety: tools.Safety{
		SideEffect: tools.SideEffectWrite,
		Permission: tools.PermissionDeny,
	}}
	if ToolAdvertised(tool, PermissionModeAuto) {
		t.Fatal("absolute policy denial must not be exposed in auto-approve mode")
	}
}
