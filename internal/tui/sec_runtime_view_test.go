package tui

import (
	"strings"
	"testing"

	"github.com/aistatine/sonut/internal/security"
)

func useEmptySecurityGuard(t *testing.T) *security.Guard {
	t.Helper()
	previous := security.Default()
	guard := security.NewGuard(security.DefaultPolicy(), "")
	security.SetDefault(guard)
	t.Cleanup(func() { security.SetDefault(previous) })
	return guard
}

func TestSecActivityRendersCompactQuietState(t *testing.T) {
	useEmptySecurityGuard(t)
	rendered := ansiPlain(strings.Join((model{}).renderSecActivity(100), "\n"))
	for _, want := range []string{"Live egress", "0 requests", "Quiet session", "Press 7"} {
		if !strings.Contains(rendered, want) {
			t.Fatalf("/sec activity missing %q:\n%s", want, rendered)
		}
	}
}

func TestSecActivityRendersClearDecisions(t *testing.T) {
	guard := useEmptySecurityGuard(t)
	_, _ = guard.CheckOutbound("web_fetch", "example.com", 443, "https", "query", false)
	_, _ = guard.CheckOutbound("exec(curl)", "example.com", 8080, "https", "curl example.com:8080", true)

	rendered := ansiPlain(strings.Join((model{}).renderSecActivity(100), "\n"))
	for _, want := range []string{"Recent decisions", "ALLOWED", "BLOCKED", "example.com:443", "best-effort observation"} {
		if !strings.Contains(rendered, want) {
			t.Fatalf("/sec activity missing %q:\n%s", want, rendered)
		}
	}
}

func TestSecSurfacesListsReadableCapabilities(t *testing.T) {
	rendered := ansiPlain(strings.Join((model{}).renderSecSurfaces(100), "\n"))
	for _, want := range []string{"Outbound surfaces", "4 capable tools", "web_fetch", "GET ONLY", "exec_command", "BEST EFFORT", "Press 5"} {
		if !strings.Contains(rendered, want) {
			t.Fatalf("/sec surfaces missing %q:\n%s", want, rendered)
		}
	}
}
