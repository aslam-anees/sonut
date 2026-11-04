package cli

import (
	"context"
	"fmt"
	"net/url"
	"sort"
	"strings"

	"github.com/aistatine/sonut/internal/config"
	"github.com/aistatine/sonut/internal/mcp"
	"github.com/aistatine/sonut/internal/redaction"
	"github.com/aistatine/sonut/internal/tools"
)

type mcpToolListItem struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	SideEffect  string `json:"sideEffect"`
	Permission  string `json:"permission"`
}

// mcpSkippedServerItem keeps the best-effort registration behavior useful from
// the CLI: a server can be skipped without aborting the other MCP tools, but it
// must still be visible so an incomplete mapping is not mistaken for a healthy one.
type mcpSkippedServerItem struct {
	Name  string `json:"name"`
	Error string `json:"error"`
}

func registerMCPToolsForWorkspace(ctx context.Context, workspaceRoot string, registry *tools.Registry, deps appDeps, autonomy mcp.PermissionAutonomy) (mcpToolRuntime, error) {
	cfg, err := deps.resolveMCPConfig(workspaceRoot)
	if err != nil {
		return nil, err
	}
	if len(cfg.Servers) == 0 {
		return noopMCPRuntime{}, nil
	}
	store, err := deps.newMCPStore()
	if err != nil {
		return nil, err
	}
	return deps.registerMCPTools(ctx, registry, cfg, mcp.RegisterOptions{
		PermissionStore: store,
		Autonomy:        autonomy,
	})
}

func execMCPAutonomy(options execOptions) mcp.PermissionAutonomy {
	if options.skipPermissionsUnsafe || strings.EqualFold(strings.TrimSpace(options.autonomy), "high") {
		return mcp.AutonomyHigh
	}
	if strings.EqualFold(strings.TrimSpace(options.autonomy), "medium") {
		return mcp.AutonomyMedium
	}
	return mcp.AutonomyLow
}

func mcpToolList(registry *tools.Registry) []mcpToolListItem {
	registered := registry.All()
	items := make([]mcpToolListItem, 0, len(registered))
	for _, tool := range registered {
		if !strings.HasPrefix(tool.Name(), "mcp_") {
			continue
		}
		safety := tool.Safety()
		items = append(items, mcpToolListItem{
			Name:        tool.Name(),
			Description: tool.Description(),
			SideEffect:  string(safety.SideEffect),
			Permission:  string(safety.Permission),
		})
	}
	sort.Slice(items, func(left int, right int) bool {
		return items[left].Name < items[right].Name
	})
	return items
}

func mcpSkippedServerList(runtime mcpToolRuntime) []mcpSkippedServerItem {
	if runtime == nil {
		return nil
	}
	skipped := runtime.Skipped()
	if len(skipped) == 0 {
		return nil
	}
	items := make([]mcpSkippedServerItem, 0, len(skipped))
	for _, server := range skipped {
		detail := "unknown registration error"
		if server.Err != nil {
			detail = redaction.ErrorMessage(server.Err, redaction.Options{})
		}
		items = append(items, mcpSkippedServerItem{Name: server.Name, Error: detail})
	}
	sort.Slice(items, func(left int, right int) bool {
		return items[left].Name < items[right].Name
	})
	return items
}

func formatMCPToolList(items []mcpToolListItem) string {
	if len(items) == 0 {
		return "No MCP tools configured."
	}
	lines := []string{"MCP Tools:"}
	for _, item := range items {
		lines = append(lines, fmt.Sprintf("  %s [%s/%s] - %s", item.Name, item.SideEffect, item.Permission, item.Description))
	}
	return strings.Join(lines, "\n")
}

func formatMCPToolOutput(items []mcpToolListItem, skipped []mcpSkippedServerItem) string {
	output := formatMCPToolList(items)
	if len(skipped) == 0 {
		return output
	}
	lines := []string{"MCP Servers skipped:"}
	for _, server := range skipped {
		lines = append(lines, fmt.Sprintf("  %s - %s", server.Name, server.Error))
	}
	return output + "\n\n" + strings.Join(lines, "\n")
}

func formatMCPServerList(servers map[string]config.MCPServerConfig) string {
	if len(servers) == 0 {
		return "No MCP servers configured."
	}
	names := make([]string, 0, len(servers))
	for name := range servers {
		names = append(names, name)
	}
	sort.Strings(names)
	lines := []string{"MCP Servers:"}
	for _, name := range names {
		server := servers[name]
		state := "enabled"
		if server.Disabled {
			state = "disabled"
		}
		identity := strings.TrimSpace(server.Command)
		if identity == "" {
			identity = redactMCPURL(server.URL, "[REDACTED]")
		}
		lines = append(lines, fmt.Sprintf("  %s [%s] %s %s", name, server.Type, state, identity))
	}
	return strings.Join(lines, "\n")
}

func redactMCPURL(raw string, marker string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	parsed, err := url.Parse(raw)
	if err != nil {
		return raw
	}
	if parsed.User != nil {
		parsed.User = nil
	}
	if parsed.RawQuery != "" {
		parsed.RawQuery = redactMCPRawQuery(parsed.RawQuery, marker)
	}
	if parsed.Fragment != "" {
		parsed.Fragment = redactMCPRawQuery(parsed.Fragment, marker)
	}
	out := parsed.String()
	if strings.TrimSpace(out) == "" {
		return raw
	}
	return out
}

func redactMCPRawQuery(rawQuery string, marker string) string {
	parts := strings.Split(rawQuery, "&")
	for index, part := range parts {
		if part == "" {
			continue
		}
		key, _, hasValue := strings.Cut(part, "=")
		decodedKey, err := url.QueryUnescape(key)
		if err != nil {
			decodedKey = key
		}
		if !isSensitiveMCPDisplayKey(decodedKey) {
			continue
		}
		if hasValue {
			parts[index] = key + "=" + marker
		} else {
			parts[index] = key
		}
	}
	return strings.Join(parts, "&")
}

func isSensitiveMCPDisplayKey(key string) bool {
	key = strings.ToLower(strings.TrimSpace(key))
	key = strings.ReplaceAll(key, "-", "_")
	if key == "key" {
		return true
	}
	for _, token := range []string{"token", "secret", "password", "passwd", "api_key", "apikey", "access_key", "auth", "credential"} {
		if strings.Contains(key, token) {
			return true
		}
	}
	return false
}
