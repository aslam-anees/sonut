package agent

import (
	"github.com/aistatine/sonut/internal/sonutruntime"
	"github.com/aistatine/sonut/internal/tools"
)

// BuildToolDefinitionsPreview returns the exact tool list a fresh run would
// advertise on its first turn (empty `loaded` set, default permission mode),
// so a KV-cache warmup can prefill the tool schemas alongside the system
// prompt using the SAME bytes the real first request will send.
func BuildToolDefinitionsPreview(options Options) []sonutruntime.ToolDefinition {
	registry := options.Registry
	if registry == nil {
		registry = tools.NewRegistry()
	}
	permissionMode := options.PermissionMode
	if permissionMode == "" {
		permissionMode = PermissionModeAuto
	}
	defs, _ := partitionTools(registry, permissionMode, options, map[string]bool{})
	return defs
}
