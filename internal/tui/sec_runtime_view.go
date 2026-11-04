package tui

import (
	"fmt"
	"strings"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/aistatine/sonut/internal/security"
	"github.com/aistatine/sonut/internal/security/sensor"
)

const secRuntimeEventLimit = 12

// renderSecActivity folds the live session egress guard into the unified /sec
// workspace. Reading the guard at render time keeps this tab current without a
// second command surface or a duplicate transcript card.
func (m model) renderSecActivity(width int) []string {
	guard := security.Default()
	counters := guard.Summary()
	events := guard.Events()
	lines := []string{
		"",
		secSection("Live egress", width, "this session"),
		secRuntimeMetric(counters.RequestsOut, "requests", sonutTheme.blue) + "   " +
			secRuntimeBytes(counters.BytesOut, "sent") + "   " +
			secRuntimeMetric(counters.Hosts, "hosts", sonutTheme.ink) + "   " +
			secRuntimeMetric(counters.Blocked, "blocked", secBlockedMetricStyle(counters.Blocked)),
		sonutTheme.faint.Render("Observed outbound decisions from network-capable tools. Secrets are never shown."),
		sonutTheme.faint.Render("Linux eBPF · " + string(sensor.NewManager(m.cwd).Status().Mode) + " · explicit /sec sensor lifecycle · no packet payloads"),
		"",
	}
	if len(events) == 0 {
		return append(lines,
			sonutTheme.green.Render("● Quiet session"),
			sonutTheme.muted.Render("  No outbound request has been observed in this session."),
			"",
			sonutTheme.faint.Render("Press 7 to inspect every outbound-capable tool and its boundary."),
		)
	}

	lines = append(lines, secSection("Recent decisions", width, pluralCount(len(events), "event")))
	visible := events
	if len(visible) > secRuntimeEventLimit {
		visible = visible[:secRuntimeEventLimit]
	}
	for _, event := range visible {
		lines = append(lines, renderSecEgressEvent(event, width)...)
	}
	if remaining := len(events) - len(visible); remaining > 0 {
		lines = append(lines, sonutTheme.faint.Render(fmt.Sprintf("+ %d older decision(s) retained in the session audit", remaining)))
	}
	lines = append(lines,
		"",
		sonutTheme.faint.Render("Live session evidence complements the agentless workspace scan; it does not claim complete OS-level traffic capture."),
	)
	return lines
}

func (m model) renderSecSurfaces(width int) []string {
	surfaces := security.ToolSurfaces()
	lines := []string{
		"",
		secSection("Outbound surfaces", width, pluralCount(len(surfaces), "capable tool")),
		sonutTheme.faint.Render("What can leave this session, through which tool, and with what boundary."),
		"",
	}
	for index, surface := range surfaces {
		tag, tagStyle := secSurfaceTag(surface.Coverage)
		name := sonutTheme.ink.Bold(true).Render(surface.Tool)
		right := tagStyle.Bold(true).Render(tag)
		lines = append(lines, joinHeaderLine(name, right, width))
		for _, line := range wrapPlainText(surface.Description, maxInt(18, width-3)) {
			lines = append(lines, "  "+sonutTheme.faint.Render(line))
		}
		if index < len(surfaces)-1 {
			lines = append(lines, "")
		}
	}
	lines = append(lines,
		"",
		sonutTheme.faint.Render("Press 5 to inspect the decisions these surfaces produced in this session."),
	)
	return lines
}

func secRuntimeSummary(counters security.Counters) string {
	statusStyle := sonutTheme.green
	if counters.Blocked > 0 {
		statusStyle = sonutTheme.red
	}
	return sonutTheme.ink.Bold(true).Render("Egress") + " " +
		sonutTheme.faint.Render(fmt.Sprintf("%d requests · %s sent · %d hosts · ", counters.RequestsOut, formatSecBytes(int64(counters.BytesOut)), counters.Hosts)) +
		statusStyle.Render(fmt.Sprintf("%d blocked", counters.Blocked))
}

func renderSecEgressEvent(event security.Event, width int) []string {
	marker, status, style := "●", "ALLOWED", sonutTheme.green
	switch {
	case event.Verdict == security.VerdictBlocked:
		marker, status, style = "×", "BLOCKED", sonutTheme.red
	case event.Classification == security.Sensitive:
		marker, status, style = "▲", "SENSITIVE", sonutTheme.amber
	case event.Verdict == security.VerdictAllowedOnce:
		marker, status, style = "●", "ALLOWED ONCE", sonutTheme.blue
	}
	destination := event.Host
	if event.Port != 0 {
		destination += fmt.Sprintf(":%d", event.Port)
	}
	left := style.Bold(true).Render(marker+" "+status) + "  " + sonutTheme.ink.Bold(true).Render(event.Tool)
	right := sonutTheme.faint.Render(event.Timestamp.Local().Format("15:04:05"))
	lines := []string{joinHeaderLine(left, right, width)}
	meta := destination + " · " + formatSecBytes(int64(event.Bytes))
	if event.Protocol != "" {
		meta += " · " + strings.ToUpper(event.Protocol)
	}
	if event.BestEffort {
		meta += " · best-effort observation"
	}
	if event.Source != "" {
		meta += " · " + event.Source
	}
	if event.Coverage != "" {
		meta += " · " + string(event.Coverage)
	}
	if event.SessionID != "" || event.ToolCallID != "" {
		meta += " · correlated"
	}
	if event.PID > 0 {
		meta += fmt.Sprintf(" · pid %d", event.PID)
	}
	if process := strings.TrimSpace(event.Process); process != "" {
		meta += " · " + process
	}
	lines = append(lines, "  "+sonutTheme.blue.Render(ansi.Truncate(meta, maxInt(10, width-2), "…")))
	if reason := strings.TrimSpace(event.Reason); reason != "" {
		for _, line := range wrapPlainText(reason, maxInt(18, width-4)) {
			lines = append(lines, "  "+style.Render(line))
		}
	}
	return lines
}

func secRuntimeMetric(value int, label string, style lipgloss.Style) string {
	return style.Bold(true).Render(fmt.Sprintf("%d", value)) + " " + sonutTheme.faint.Render(label)
}

func secRuntimeBytes(value int, label string) string {
	return sonutTheme.ink.Bold(true).Render(formatSecBytes(int64(value))) + " " + sonutTheme.faint.Render(label)
}

func secBlockedMetricStyle(blocked int) lipgloss.Style {
	if blocked > 0 {
		return sonutTheme.red
	}
	return sonutTheme.green
}

func secSurfaceTag(coverage security.SurfaceCoverage) (string, lipgloss.Style) {
	switch coverage {
	case security.CoverageEnforced:
		return "ENFORCED", sonutTheme.green
	case security.CoverageObserved:
		return "OBSERVED", sonutTheme.blue
	case security.CoverageBestEffort:
		return "BEST EFFORT", sonutTheme.amber
	case security.CoverageNotCovered:
		return "NOT COVERED", sonutTheme.red
	default:
		return "NETWORK", sonutTheme.faint
	}
}

func sensorStatusLines(status sensor.Status) []string {
	mode := string(status.Mode)
	if mode == "" {
		mode = string(sensor.ModeOff)
	}
	lines := []string{
		"state: " + string(status.State) + " · mode: " + mode,
		fmt.Sprintf("scope: %s · events: %d · blocked: %d · dropped: %d", nonEmpty(status.Cgroup, "not attached"), status.Events, status.Blocked, status.Dropped),
	}
	if status.Capabilities.Platform != "" {
		lines = append(lines, "platform: "+status.Capabilities.Platform)
	}
	if len(status.AttachedHooks) > 0 {
		lines = append(lines, "attached: "+strings.Join(status.AttachedHooks, ", "))
	}
	if len(status.DegradedHooks) > 0 {
		lines = append(lines, "degraded: "+strings.Join(status.DegradedHooks, ", "))
	}
	if status.LastError != "" {
		lines = append(lines, "reason: "+status.LastError)
	}
	return lines
}

func secSensorSummary(root string) string {
	status := sensor.NewManager(root).Status()
	style := sonutTheme.faint
	if status.State == sensor.StateRunning {
		style = sonutTheme.green
	}
	if status.State == sensor.StateError || status.State == sensor.StateUnavailable {
		style = sonutTheme.amber
	}
	return sonutTheme.ink.Bold(true).Render("eBPF") + " " + style.Render(string(status.Mode)) + " · " + sonutTheme.faint.Render(string(status.State)) + fmt.Sprintf(" · %d events · %d blocked · %d dropped", status.Events, status.Blocked, status.Dropped)
}

func nonEmpty(value, fallback string) string {
	if strings.TrimSpace(value) == "" {
		return fallback
	}
	return value
}
