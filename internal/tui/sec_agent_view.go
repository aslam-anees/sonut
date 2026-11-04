package tui

import (
	"fmt"
	"sort"
	"strings"

	"charm.land/lipgloss/v2"

	"github.com/aistatine/sonut/internal/security"
	"github.com/aistatine/sonut/internal/sessions"
)

// secAgentTelemetry is the small, redacted session projection used by the
// Signals view. It deliberately keeps counts and tool names, not prompts,
// arguments, or tool output. This mirrors ADR Sensor's normalized event model
// without importing a second log store or a heavyweight parser framework.
type secAgentTelemetry struct {
	Turns              int
	ToolCalls          int
	ToolResults        int
	PermissionRequests int
	Denied             int
	Errors             int
	Specialists        int
	SensitiveReads     int
	NetworkIntent      int
	Tools              []secToolCount
}

type secToolCount struct {
	Name  string
	Count int
}

type secAgentSignal struct {
	Tactic     string
	Title      string
	Severity   string
	Confidence string
	Evidence   string
	Action     string
}

func secAgentOverviewSummary(m model) string {
	telemetry := secAgentTelemetryFromEvents(m.sessionEvents)
	if telemetry.ToolCalls == 0 && telemetry.Turns == 0 {
		return sonutTheme.ink.Bold(true).Render("Agent") + " " + sonutTheme.faint.Render("telemetry idle · press 7 for Signals")
	}
	signals := secAgentSignals(telemetry, m.sessionEvents, security.Default().Summary())
	style := sonutTheme.green
	if len(signals) > 0 {
		style = sonutTheme.amber
	}
	return sonutTheme.ink.Bold(true).Render("Agent") + " " +
		sonutTheme.faint.Render(fmt.Sprintf("%d turns · %d tool calls · ", telemetry.Turns, telemetry.ToolCalls)) +
		style.Render(fmt.Sprintf("%d triage signal(s)", len(signals)))
}

// renderSecSignals brings ADR's useful high-level behavior concepts into the
// native workspace: normalized telemetry, a transparent triage pass, and the
// repository policy excerpt that should inform human review. It is evidence
// led; no signal is emitted without a concrete local observation.
func (m model) renderSecSignals(width int) []string {
	telemetry := secAgentTelemetryFromEvents(m.sessionEvents)
	signals := secAgentSignals(telemetry, m.sessionEvents, security.Default().Summary())
	lines := []string{
		"",
		secSection("Agent signals", width, "session telemetry + triage"),
		sonutTheme.faint.Render("A redacted view of what this agent session did; counts and tool names only."),
		"",
		secSection("Activity", width, "normalized events"),
		secAgentMetricLine(telemetry.Turns, "turns", sonutTheme.blue) + "   " +
			secAgentMetricLine(telemetry.ToolCalls, "tool calls", sonutTheme.ink) + "   " +
			secAgentMetricLine(telemetry.ToolResults, "results", sonutTheme.ink),
		secAgentMetricLine(telemetry.PermissionRequests, "permission prompts", sonutTheme.amber) + "   " +
			secAgentMetricLine(telemetry.Denied, "denied", secDeniedStyle(telemetry.Denied)) + "   " +
			secAgentMetricLine(telemetry.Errors, "errors", secErrorStyle(telemetry.Errors)),
	}
	if telemetry.Specialists > 0 {
		lines = append(lines, secAgentMetricLine(telemetry.Specialists, "specialist delegations", sonutTheme.blue))
	}
	if len(telemetry.Tools) > 0 {
		lines = append(lines, "", sonutTheme.ink.Bold(true).Render("Tool coverage"))
		for _, tool := range telemetry.Tools {
			lines = append(lines, "  "+sonutTheme.ink.Render(tool.Name)+"  "+sonutTheme.faint.Render(fmt.Sprintf("%d call(s)", tool.Count)))
		}
	}
	if telemetry.Turns == 0 && telemetry.ToolCalls == 0 {
		lines = append(lines, "", sonutTheme.muted.Render("No agent events in this session yet."), sonutTheme.faint.Render("Run a turn and reopen /sec; this view updates from the live session."))
	}

	lines = append(lines, "", secSection("Triage", width, pluralCount(len(signals), "signal")))
	if len(signals) == 0 {
		lines = append(lines,
			sonutTheme.green.Render("✓ No high-signal behavior pattern observed."),
			sonutTheme.faint.Render("This is a fast local triage pass, not proof that a session is safe."),
		)
	} else {
		for index, signal := range signals {
			lines = append(lines, renderSecAgentSignal(signal, index, width)...)
		}
	}

	if policy := strings.TrimSpace(m.secView.report.Policy); policy != "" {
		lines = append(lines, "", secSection("Repository policy", width, "untrusted context"))
		for _, line := range wrapPlainText(policy, maxInt(18, width-2)) {
			lines = append(lines, "  "+sonutTheme.muted.Render(line))
		}
	} else {
		lines = append(lines, "", secSection("Repository policy", width, "not found"), sonutTheme.faint.Render("Add SECURITY.md to give responders a local reporting and handling reference."))
	}
	lines = append(lines,
		"",
		sonutTheme.faint.Render("Pipeline: observe → triage → evidence → optional advisor review."),
		sonutTheme.faint.Render("Signals are local heuristics; verify before treating them as an incident or compliance result."),
	)
	return lines
}

func renderSecAgentSignal(signal secAgentSignal, index, width int) []string {
	style := sonutTheme.amber
	switch strings.ToLower(signal.Severity) {
	case "critical", "high":
		style = sonutTheme.red
	case "medium":
		style = sonutTheme.amber
	case "low":
		style = sonutTheme.blue
	}
	label := style.Bold(true).Render(strings.ToUpper(signal.Severity))
	header := fmt.Sprintf("%02d  %s  %s", index+1, label, sonutTheme.ink.Bold(true).Render(signal.Title))
	lines := []string{header}
	lines = append(lines, "    "+sonutTheme.faint.Render(signal.Tactic)+" · "+style.Render(signal.Confidence+" confidence"))
	for _, line := range wrapPlainText("Observed: "+signal.Evidence, maxInt(18, width-6)) {
		lines = append(lines, "    "+sonutTheme.muted.Render(line))
	}
	for _, line := range wrapPlainText("Next: "+signal.Action, maxInt(18, width-6)) {
		lines = append(lines, "    "+sonutTheme.green.Render(line))
	}
	return lines
}

func secAgentTelemetryFromEvents(events []sessions.Event) secAgentTelemetry {
	telemetry := secAgentTelemetry{}
	toolCounts := map[string]int{}
	for _, event := range events {
		payload := sessionPayload(event)
		switch event.Type {
		case sessions.EventMessage:
			if role := strings.ToLower(payloadString(payload, "role")); role == "user" {
				telemetry.Turns++
			}
		case sessions.EventToolCall:
			telemetry.ToolCalls++
			name := payloadString(payload, "name")
			if name == "" {
				name = "unknown"
			}
			toolCounts[name]++
			args := payloadString(payload, "arguments")
			if secSensitiveRead(name, args) {
				telemetry.SensitiveReads++
			}
			if secNetworkTool(name, args) {
				telemetry.NetworkIntent++
			}
		case sessions.EventToolResult:
			telemetry.ToolResults++
			if strings.EqualFold(payloadString(payload, "status"), "error") {
				telemetry.Errors++
			}
		case sessions.EventPermissionRequest:
			telemetry.PermissionRequests++
		case sessions.EventPermissionDecision:
			if strings.Contains(strings.ToLower(payloadString(payload, "action")), "deny") ||
				strings.Contains(strings.ToLower(payloadString(payload, "decision")), "deny") {
				telemetry.Denied++
			}
		case sessions.EventError:
			telemetry.Errors++
		case sessions.EventSpecialistStart:
			telemetry.Specialists++
		}
	}
	telemetry.Tools = make([]secToolCount, 0, len(toolCounts))
	for name, count := range toolCounts {
		telemetry.Tools = append(telemetry.Tools, secToolCount{Name: name, Count: count})
	}
	sort.Slice(telemetry.Tools, func(i, j int) bool {
		if telemetry.Tools[i].Count != telemetry.Tools[j].Count {
			return telemetry.Tools[i].Count > telemetry.Tools[j].Count
		}
		return telemetry.Tools[i].Name < telemetry.Tools[j].Name
	})
	if len(telemetry.Tools) > 8 {
		telemetry.Tools = telemetry.Tools[:8]
	}
	return telemetry
}

func secAgentSignals(telemetry secAgentTelemetry, events []sessions.Event, egress security.Counters) []secAgentSignal {
	signals := make([]secAgentSignal, 0, 4)
	if telemetry.SensitiveReads > 0 && (telemetry.NetworkIntent > 0 || egress.RequestsOut > 0) {
		confidence := "medium"
		if egress.Blocked > 0 {
			confidence = "high"
		}
		signals = append(signals, secAgentSignal{
			Tactic:   "ADR.T0003 · control-flow hijacking / data movement",
			Title:    "Sensitive read followed by outbound intent",
			Severity: "high", Confidence: confidence,
			Evidence: fmt.Sprintf("%d sensitive-looking read(s) and %d outbound decision(s) are present in one session.", telemetry.SensitiveReads, egress.RequestsOut),
			Action:   "Review the matching Activity event and confirm the destination was necessary; rotate any credential that was exposed.",
		})
	}
	if telemetry.SensitiveReads > 0 && telemetry.ToolCalls >= 3 && telemetry.NetworkIntent > 0 {
		signals = append(signals, secAgentSignal{
			Tactic:   "ADR.T0007 · excessive tool permissions",
			Title:    "Multi-step sensitive workflow crossed a trust boundary",
			Severity: "medium", Confidence: "medium",
			Evidence: fmt.Sprintf("The session chained %d tool calls across sensitive reads and a network-capable tool.", telemetry.ToolCalls),
			Action:   "Break the workflow into least-privilege steps and require an explicit approval before external transfer.",
		})
	}
	if injection := secPromptInjectionEvidence(events); injection {
		signals = append(signals, secAgentSignal{
			Tactic:   "ADR.T0002 · indirect prompt injection",
			Title:    "Tool output contained instruction-like override language",
			Severity: "high", Confidence: "medium",
			Evidence: "A tool result matched a prompt-override pattern; the result is not rendered here.",
			Action:   "Treat the source as untrusted, do not follow its instructions, and inspect the tool/source before continuing.",
		})
	}
	if telemetry.PermissionRequests > 0 && telemetry.Denied > 0 {
		signals = append(signals, secAgentSignal{
			Tactic:   "ADR.T0007 · permission boundary",
			Title:    "A requested action crossed a permission boundary",
			Severity: "medium", Confidence: "high",
			Evidence: fmt.Sprintf("%d permission prompt(s), including %d denial(s), were recorded.", telemetry.PermissionRequests, telemetry.Denied),
			Action:   "Keep the denial in place unless the action is expected, scoped, and approved by the workspace owner.",
		})
	}
	return signals
}

// secAdvisorSessionContext is intentionally a plain, bounded summary. It is
// the only session data that crosses from the TUI into the model-backed
// advisor; prompts, arguments, and tool output are never serialized here.
func (m model) secAdvisorSessionContext() string {
	telemetry := secAgentTelemetryFromEvents(m.sessionEvents)
	guard := security.Default()
	egressSummary := guard.Summary()
	egressEvents := guard.Events()
	signals := secAgentSignals(telemetry, m.sessionEvents, egressSummary)
	parts := []string{
		fmt.Sprintf("turns=%d", telemetry.Turns),
		fmt.Sprintf("tool_calls=%d", telemetry.ToolCalls),
		fmt.Sprintf("tool_results=%d", telemetry.ToolResults),
		fmt.Sprintf("permission_prompts=%d", telemetry.PermissionRequests),
		fmt.Sprintf("denied=%d", telemetry.Denied),
		fmt.Sprintf("errors=%d", telemetry.Errors),
		fmt.Sprintf("sensitive_looking_reads=%d", telemetry.SensitiveReads),
		fmt.Sprintf("network_intent=%d", telemetry.NetworkIntent),
		fmt.Sprintf("egress_requests=%d", egressSummary.RequestsOut),
		fmt.Sprintf("egress_hosts=%d", egressSummary.Hosts),
		fmt.Sprintf("egress_blocked=%d", egressSummary.Blocked),
	}
	if m.secView.active && m.secView.tab == secTabAtlas {
		filtered := secAtlasFilteredEvents(egressEvents, m.secView.atlasFilter)
		if len(filtered) > 0 {
			selected := filtered[clampInt(m.secView.atlasSelected, 0, len(filtered)-1)]
			parts = append(parts, "atlas_selected="+secAdvisorEgressEvent(selected))
		}
	}
	for index, event := range egressEvents {
		if index == 3 {
			break
		}
		parts = append(parts, "recent_egress="+secAdvisorEgressEvent(event))
	}
	if len(signals) == 0 {
		parts = append(parts, "signals=none observed")
	} else {
		for _, signal := range signals {
			parts = append(parts, "signal="+signal.Tactic+" | "+signal.Title+" | "+signal.Severity+" | "+signal.Confidence+" confidence")
		}
	}
	return strings.Join(parts, "\n")
}

func secAdvisorEgressEvent(event security.Event) string {
	destination := event.Host
	if event.Port != 0 {
		destination += fmt.Sprintf(":%d", event.Port)
	}
	return strings.Join([]string{
		"tool=" + event.Tool,
		"destination=" + destination,
		"protocol=" + event.Protocol,
		"classification=" + string(event.Classification),
		"verdict=" + string(event.Verdict),
	}, " | ")
}

func secPromptInjectionEvidence(events []sessions.Event) bool {
	for _, event := range events {
		if event.Type != sessions.EventToolResult {
			continue
		}
		rawOutput := payloadString(sessionPayload(event), "output")
		if len(rawOutput) > 32*1024 {
			rawOutput = rawOutput[:32*1024]
		}
		output := strings.ToLower(rawOutput)
		for _, phrase := range []string{
			"ignore previous instructions", "ignore all previous", "system message:",
			"send the secret", "reveal the token", "disable security", "bypass approval",
		} {
			if strings.Contains(output, phrase) {
				return true
			}
		}
	}
	return false
}

func secSensitiveRead(name, arguments string) bool {
	if strings.TrimSpace(arguments) == "" {
		return false
	}
	lowerName := strings.ToLower(name)
	readLike := lowerName == "read_file" || lowerName == "read_files" || lowerName == "read_many_files" ||
		lowerName == "grep" || lowerName == "glob" || lowerName == "search" || lowerName == "cat" || lowerName == "file"
	if lowerName == "exec_command" || lowerName == "bash" || lowerName == "shell" {
		command := strings.ToLower(arguments)
		readLike = strings.Contains(command, "cat ") || strings.Contains(command, "printenv") || strings.Contains(command, "env ") || strings.Contains(command, "grep ")
	}
	if !readLike {
		return false
	}
	lower := strings.ToLower(arguments)
	for _, token := range []string{".env", "secret", "credential", "password", "token", "private.key", "id_rsa", ".pem"} {
		if strings.Contains(lower, token) {
			return true
		}
	}
	return false
}

func secNetworkTool(name, arguments string) bool {
	lowerName := strings.ToLower(name)
	if lowerName == "web_fetch" || lowerName == "web_search" || lowerName == "web_browser" {
		return true
	}
	if lowerName == "exec_command" || lowerName == "bash" || lowerName == "shell" {
		lowerArgs := strings.ToLower(arguments)
		for _, token := range []string{"curl", "wget", "http://", "https://", "ssh ", "git push", "nc "} {
			if strings.Contains(lowerArgs, token) {
				return true
			}
		}
	}
	return false
}

func secAgentMetricLine(value int, label string, style lipgloss.Style) string {
	return style.Bold(true).Render(fmt.Sprintf("%d", value)) + " " + sonutTheme.faint.Render(label)
}

func secDeniedStyle(value int) lipgloss.Style {
	if value > 0 {
		return sonutTheme.red
	}
	return sonutTheme.green
}

func secErrorStyle(value int) lipgloss.Style {
	if value > 0 {
		return sonutTheme.amber
	}
	return sonutTheme.green
}
