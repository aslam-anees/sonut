package tui

import (
	"fmt"
	"regexp"
	"sort"
	"strings"

	"github.com/aistatine/sonut/internal/agent"
)

// safetyState is a rolling log of permission/sandbox decisions shown in the
// sidebar's SAFETY section, so approvals and denials are visible at a glance
// instead of only scrolling by in the chat transcript.
type safetyState struct {
	events []string // newest last, capped at 20
	// destinations counts where session data has been SENT this session —
	// web hosts (web_fetch, browser navigation, curl in exec_command), MCP
	// servers, and the web-search provider — so the SAFETY section shows the
	// top data flows at a glance (Wiz-style exposure map, for AI safety).
	destinations map[string]int
}

// addDestinations counts destinations session data was just sent to.
func (s *safetyState) addDestinations(dests []string) {
	for _, dest := range dests {
		if s.destinations == nil {
			s.destinations = map[string]int{}
		}
		s.destinations[dest]++
	}
}

// urlHostPattern captures the host of any http(s) URL appearing in a tool
// call's raw arguments — this covers web_fetch/browser URLs and also URLs
// embedded in exec_command lines (curl/wget), which name-based rules miss.
var urlHostPattern = regexp.MustCompile(`(?i)https?://([a-z0-9][a-z0-9.\-]*[a-z0-9])`)

// dataDestinations derives the external destinations a tool call sends data
// to: URL hosts found in the arguments, the MCP server for mcp_* tools, and
// the search provider for web_search. Local-only tools yield nothing.
func dataDestinations(toolName string, arguments string) []string {
	var dests []string
	seen := map[string]bool{}
	add := func(dest string) {
		if dest != "" && !seen[dest] {
			seen[dest] = true
			dests = append(dests, dest)
		}
	}
	for _, match := range urlHostPattern.FindAllStringSubmatch(arguments, -1) {
		add(strings.ToLower(match[1]))
	}
	if rest, ok := strings.CutPrefix(toolName, "mcp_"); ok && rest != "" {
		server := rest
		if index := strings.Index(rest, "_"); index > 0 {
			server = rest[:index]
		}
		add("mcp: " + server)
	}
	if toolName == "web_search" {
		add("web search")
	}
	return dests
}

// topDestinations returns up to n "dest ×count" lines, most-used first (ties
// alphabetical, so the render is deterministic).
func (s *safetyState) topDestinations(n int) []string {
	if len(s.destinations) == 0 || n <= 0 {
		return nil
	}
	type flow struct {
		dest  string
		count int
	}
	flows := make([]flow, 0, len(s.destinations))
	for dest, count := range s.destinations {
		flows = append(flows, flow{dest, count})
	}
	sort.Slice(flows, func(i, j int) bool {
		if flows[i].count != flows[j].count {
			return flows[i].count > flows[j].count
		}
		return flows[i].dest < flows[j].dest
	})
	if len(flows) > n {
		flows = flows[:n]
	}
	lines := make([]string, 0, len(flows))
	for _, f := range flows {
		lines = append(lines, fmt.Sprintf("%s ×%d", f.dest, f.count))
	}
	return lines
}

func (s *safetyState) addEvent(text string) {
	text = strings.TrimSpace(text)
	if text == "" {
		return
	}
	s.events = append(s.events, text)
	if len(s.events) > 20 {
		s.events = s.events[len(s.events)-20:]
	}
}

// recordPermissionDecision logs a user permission decision against the tool it
// gated, e.g. "✗ exec_command denied" or "✓ exec_command approved (session)".
func (s *safetyState) recordPermissionDecision(request agent.PermissionRequest, decision permissionDecision) {
	subject := request.ToolName
	if request.Permission != "" {
		subject += " · " + request.Permission
	}
	switch decision {
	case permissionDecisionDeny:
		s.addEvent("✗ " + subject + " denied")
	case permissionDecisionCancel:
		s.addEvent("✗ " + subject + " cancelled")
	case permissionDecisionAllowForSession, permissionDecisionAllowPrefix:
		s.addEvent("✓ " + subject + " approved (session)")
	case permissionDecisionAlwaysAllow, permissionDecisionAlwaysAllowPrefix:
		s.addEvent("✓ " + subject + " approved (always)")
	default:
		s.addEvent("✓ " + subject + " approved")
	}
}

// permissionModeLabel is the always-visible SAFETY summary line.
func permissionModeLabel(mode agent.PermissionMode) string {
	label := strings.TrimSpace(string(mode))
	if label == "" {
		label = "default"
	}
	return "mode: " + label
}

// renderSafetyLines renders the SAFETY sidebar section: the permission mode
// summary plus up to maxEvents recent decisions (newest last).
func (m model) renderSafetyLines(width, maxEvents int) []string {
	lines := []string{sidebarHeader("SAFETY", width)}
	lines = append(lines, " "+sonutTheme.faint.Render(truncateBrowserStr(permissionModeLabel(m.permissionMode), width-2)))
	// Data-flow map: the top destinations session data was sent to (web
	// hosts, MCP servers, search) — exposure at a glance, before the
	// approval/denial event log.
	if tops := m.safetyState.topDestinations(5); len(tops) > 0 {
		lines = append(lines, " "+sonutTheme.faint.Render(truncateBrowserStr("data sent to:", width-2)))
		for _, top := range tops {
			lines = append(lines, "  "+sonutTheme.muted.Render(truncateBrowserStr(top, width-3)))
		}
	}
	events := m.safetyState.events
	if len(events) == 0 {
		lines = append(lines, sidebarPlaceholder("no approvals or denials yet", width))
		return lines
	}
	if maxEvents > 0 && len(events) > maxEvents {
		events = events[len(events)-maxEvents:]
	}
	for _, event := range events {
		style := sonutTheme.faint
		if strings.HasPrefix(event, "✗") {
			style = sonutTheme.red
		}
		lines = append(lines, " "+style.Render(truncateBrowserStr(event, width-2)))
	}
	return lines
}
