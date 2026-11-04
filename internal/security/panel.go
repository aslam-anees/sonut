package security

import (
	"fmt"
	"strings"
)

// ToolSurface is a one-line standing description of a network-capable tool's
// egress capability, shown in the /sec Surfaces tab so the user can audit
// the whole outbound surface at a glance.
type ToolSurface struct {
	Tool        string
	Description string
	Coverage    SurfaceCoverage
}

// ToolSurfaces lists every tool with outbound capability. Kept as static data
// alongside the tools it describes; update it if a tool's egress shape
// changes.
func ToolSurfaces() []ToolSurface {
	return []ToolSurface{
		{Tool: "web_fetch", Description: "GET ONLY request to any public HTTP/HTTPS host on port 80/443; request destination is checked before bytes leave.", Coverage: CoverageEnforced},
		{Tool: "web_search", Description: "Sends query text to the configured HTTPS backend; query classification and destination are checked before dispatch.", Coverage: CoverageEnforced},
		{Tool: "web_browser", Description: "Navigation destination is checked; explicit form/eval input is classified where available. Page-initiated subrequests are not fully captured here; eBPF may observe connections but not decrypted HTTPS payloads.", Coverage: CoverageObserved},
		{Tool: "exec_command", Description: "Runs arbitrary shell commands; curl/wget/nc/ssh/git push destinations are parsed best-effort and are not a complete network monitor.", Coverage: CoverageBestEffort},
	}
}

// EventLine renders one egress event as a single panel line, e.g.:
//
//	⚠ web_fetch → api.duckduckgo.com:443 · 2.1KB · clean
//	✗ exec(curl) → unknown-host:8080 · blocked: API key detected
func EventLine(e Event) string {
	marker := "✓"
	switch e.Verdict {
	case VerdictBlocked:
		marker = "✗"
	default:
		if e.Classification != Clean {
			marker = "⚠"
		}
	}
	dest := e.Host
	if e.Port != 0 {
		dest = fmt.Sprintf("%s:%d", e.Host, e.Port)
	}
	status := string(e.Classification)
	if e.Verdict == VerdictBlocked {
		status = "blocked"
		if e.Reason != "" {
			status += ": " + e.Reason
		}
	} else if e.Classification == Sensitive && e.Reason != "" {
		status += ": " + e.Reason
	}
	line := fmt.Sprintf("%s %s → %s · %s · %s", marker, e.Tool, dest, formatBytes(e.Bytes), status)
	if e.BestEffort {
		line += " (best-effort)"
	}
	return line
}

func formatBytes(n int) string {
	if n < 1024 {
		return fmt.Sprintf("%dB", n)
	}
	return fmt.Sprintf("%.1fKB", float64(n)/1024)
}

// RenderPanel renders the live SECURITY sidebar: grouped counters, then one
// line per event (most recent first).
func RenderPanel(counters Counters, events []Event) string {
	var b strings.Builder
	fmt.Fprintf(&b, "requests out: %d · bytes out: %s · hosts: %d · blocked: %d\n",
		counters.RequestsOut, formatBytes(counters.BytesOut), counters.Hosts, counters.Blocked)
	if len(events) == 0 {
		b.WriteString("(no egress yet)")
		return b.String()
	}
	for i, e := range events {
		if i > 0 {
			b.WriteByte('\n')
		}
		b.WriteString(EventLine(e))
	}
	return b.String()
}

// RenderToolSurfaces renders the /sec outbound-surface inventory: one line per
// network-capable tool naming what it can send where.
func RenderToolSurfaces() string {
	var b strings.Builder
	surfaces := ToolSurfaces()
	for i, s := range surfaces {
		if i > 0 {
			b.WriteByte('\n')
		}
		fmt.Fprintf(&b, "%s — %s", s.Tool, s.Description)
	}
	return b.String()
}
