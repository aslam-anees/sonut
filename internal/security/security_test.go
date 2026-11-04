package security

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func newTestGuard(t *testing.T) (*Guard, string) {
	t.Helper()
	dir := t.TempDir()
	return NewGuard(DefaultPolicy(), filepath.Join(dir, "audit")), dir
}

func TestCheckOutbound_SecretInPayloadIsBlocked(t *testing.T) {
	g, _ := newTestGuard(t)
	payload := "token=AKIAABCDEFGHIJKLMNOP body"
	event, err := g.CheckOutbound("web_fetch", "api.example.com", 443, "https", payload, false)
	if err == nil {
		t.Fatalf("expected blocked error for secret payload")
	}
	if event.Classification != Blocked || event.Verdict != VerdictBlocked {
		t.Fatalf("expected blocked classification/verdict, got %+v", event)
	}
}

func TestCheckOutbound_NonStandardPortBlockedByPolicy(t *testing.T) {
	g, _ := newTestGuard(t)
	event, err := g.CheckOutbound("exec(nc)", "unknown-host", 8080, "tcp", "", true)
	if err == nil {
		t.Fatalf("expected non-standard port to be blocked")
	}
	if event.Verdict != VerdictBlocked {
		t.Fatalf("expected blocked verdict, got %+v", event)
	}
}

func TestCheckOutbound_LocalhostNonStandardPortAllowed(t *testing.T) {
	g, _ := newTestGuard(t)
	_, err := g.CheckOutbound("exec_command", "localhost", 5173, "tcp", "", true)
	if err != nil {
		t.Fatalf("expected localhost non-standard port to be allowed: %v", err)
	}
}

func TestCheckOutbound_CleanPayloadAllowed(t *testing.T) {
	g, _ := newTestGuard(t)
	event, err := g.CheckOutbound("web_fetch", "api.duckduckgo.com", 443, "https", "q=weather", false)
	if err != nil {
		t.Fatalf("unexpected block: %v", err)
	}
	if event.Classification != Clean {
		t.Fatalf("expected clean classification, got %+v", event)
	}
}

func TestCheckOutbound_AllowOnceOverridesBlock(t *testing.T) {
	g, _ := newTestGuard(t)
	if _, err := g.CheckOutbound("exec(curl)", "raw-ip-host", 9999, "tcp", "", true); err == nil {
		t.Fatalf("expected initial block")
	}
	g.AllowOnce("raw-ip-host")
	event, err := g.CheckOutbound("exec(curl)", "raw-ip-host", 9999, "tcp", "", true)
	if err != nil {
		t.Fatalf("expected allow-once to unblock: %v", err)
	}
	if event.Verdict != VerdictAllowedOnce {
		t.Fatalf("expected allowed_once verdict, got %+v", event)
	}
	if _, err := g.CheckOutbound("exec(curl)", "raw-ip-host", 9999, "tcp", "", true); err == nil {
		t.Fatal("allow-once approval must not persist for the rest of the session")
	}
}

func TestCheckOutbound_AllowOnceDoesNotBypassSecretBlock(t *testing.T) {
	g, _ := newTestGuard(t)
	g.AllowOnce("raw-ip-host")
	event, err := g.CheckOutbound("exec(curl)", "raw-ip-host", 9999, "tcp", "token=AKIAABCDEFGHIJKLMNOP", true)
	if err == nil {
		t.Fatal("one-shot destination approval must not allow a sensitive payload")
	}
	if event.Verdict != VerdictBlocked || event.Classification != Blocked {
		t.Fatalf("expected sensitive payload to remain blocked, got %+v", event)
	}
	// The failed sensitive attempt must not consume the approval. It may be used
	// for a later clean request to the same user-approved destination.
	event, err = g.CheckOutbound("exec(curl)", "raw-ip-host", 9999, "tcp", "clean", true)
	if err != nil || event.Verdict != VerdictAllowedOnce {
		t.Fatalf("expected clean request to consume the approval, event=%+v err=%v", event, err)
	}
}

func TestEventLine_Format(t *testing.T) {
	clean := Event{Tool: "web_fetch", Host: "api.duckduckgo.com", Port: 443, Bytes: 2150, Classification: Clean, Verdict: VerdictAllowed}
	if got := EventLine(clean); !strings.Contains(got, "web_fetch → api.duckduckgo.com:443") || !strings.Contains(got, "clean") {
		t.Fatalf("unexpected clean line: %q", got)
	}
	blocked := Event{Tool: "exec(curl)", Host: "unknown-host", Port: 8080, Classification: Blocked, Verdict: VerdictBlocked, Reason: "API key detected"}
	got := EventLine(blocked)
	if !strings.HasPrefix(got, "✗") || !strings.Contains(got, "blocked: API key detected") {
		t.Fatalf("unexpected blocked line: %q", got)
	}
}

func TestAuditLog_WrittenOnEveryEvent(t *testing.T) {
	dir := t.TempDir()
	auditDir := filepath.Join(dir, "audit")
	g := NewGuard(DefaultPolicy(), auditDir)

	g.CheckOutbound("web_fetch", "api.duckduckgo.com", 443, "https", "clean", false)
	g.CheckOutbound("exec(curl)", "raw-ip", 9999, "tcp", "", true)

	entries, err := os.ReadDir(auditDir)
	if err != nil || len(entries) == 0 {
		t.Fatalf("expected audit file(s) written, err=%v entries=%v", err, entries)
	}
	data, err := os.ReadFile(filepath.Join(auditDir, entries[0].Name()))
	if err != nil {
		t.Fatalf("read audit file: %v", err)
	}
	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	if len(lines) != 2 {
		t.Fatalf("expected 2 audit lines, got %d: %q", len(lines), string(data))
	}
}

func TestGraph_DidLeaveDetectsFileContentInPayload(t *testing.T) {
	g, _ := newTestGuard(t)
	g.Graph().RecordFileRead(".env", "SUPER_SECRET_VALUE_LINE_HERE=123456789")
	left, _ := g.Graph().DidLeave(".env")
	if left {
		t.Fatalf("expected no leak recorded yet")
	}
	g.CheckOutbound("web_fetch", "example.com", 443, "https", "payload contains SUPER_SECRET_VALUE_LINE_HERE=123456789 verbatim", false)
	left, events := g.Graph().DidLeave(".env")
	if !left || len(events) == 0 {
		t.Fatalf("expected .env content leak to be detected")
	}
}

func TestGraph_NoLeakWhenContentNotSent(t *testing.T) {
	g, _ := newTestGuard(t)
	g.Graph().RecordFileRead(".env", "SUPER_SECRET_VALUE_LINE_HERE=123456789")
	g.CheckOutbound("web_fetch", "example.com", 443, "https", "totally unrelated payload", false)
	left, _ := g.Graph().DidLeave(".env")
	if left {
		t.Fatalf("expected no leak when content never sent")
	}
}

func TestInboundObservationIsNotCountedAsOutbound(t *testing.T) {
	g, _ := newTestGuard(t)
	g.RecordInboundObservation("web_fetch", "example.com", 443, "https", 128, "response metadata", OutboundMetadata{SessionID: "s1", ToolCallID: "t1"})
	if got := g.Summary().RequestsOut; got != 0 {
		t.Fatalf("inbound observation counted as outbound: %d", got)
	}
	events := g.Events()
	if len(events) != 1 || events[0].Direction != DirectionInbound || events[0].SessionID != "s1" || events[0].ToolCallID != "t1" {
		t.Fatalf("unexpected inbound event: %#v", events)
	}
}

func TestGraphLeakCarriesReadEvidenceWithoutRawContent(t *testing.T) {
	g, _ := newTestGuard(t)
	readContent := "SUPER_SECRET_VALUE_LINE_HERE=123456789"
	g.Graph().RecordFileReadWithMetadata(".env", readContent, "session-1", "tool-1")
	event, _ := g.CheckOutboundWithMetadata("web_fetch", "example.com", 443, "https", readContent, false, OutboundMetadata{SessionID: "session-1", ToolCallID: "tool-2"})
	left, events := g.Graph().DidLeave(".env")
	if !left || len(events) != 1 {
		t.Fatalf("expected one linked event, left=%v events=%#v", left, events)
	}
	if len(events[0].EvidenceRefs) == 0 || strings.Contains(strings.Join(events[0].EvidenceRefs, " "), readContent) || events[0].ID != event.ID {
		t.Fatalf("leak evidence was not redacted/correlated: %#v", events[0])
	}
}
