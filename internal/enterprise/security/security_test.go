package security

import (
	"strings"
	"testing"
)

func TestRedactHeadersJSON(t *testing.T) {
	raw := `{"Authorization":"Bearer secret","X-Api-Key":"abc","X-Custom":"keep-me"}`
	got := RedactHeadersJSON(raw)
	if got == raw {
		t.Fatalf("expected redaction to change payload")
	}
	if !containsAll(got, `"X-Custom":"keep-me"`, `"********"`) {
		t.Fatalf("unexpected redacted payload: %s", got)
	}
	if containsAll(got, "secret") || containsAll(got, "abc") {
		t.Fatalf("sensitive value leaked: %s", got)
	}
}

func TestValidateOutboundHTTPURL_BlocksPrivateIPInProduction(t *testing.T) {
	if err := ValidateOutboundHTTPURL("http://127.0.0.1:8080/", "production", true); err == nil {
		t.Fatalf("expected loopback URL to be rejected in production with SSRF protection enabled")
	}
	if err := ValidateOutboundHTTPURL("http://10.0.0.5/", "production", true); err == nil {
		t.Fatalf("expected private IP to be rejected in production with SSRF protection enabled")
	}
}

func TestValidateOutboundHTTPURL_AllowsPublicHost(t *testing.T) {
	if err := ValidateOutboundHTTPURL("https://example.com/path", "production", true); err != nil {
		t.Fatalf("expected public host to be allowed, got: %v", err)
	}
}

func TestValidateOutboundHTTPURL_SkipsEnforcementOutsideProduction(t *testing.T) {
	if err := ValidateOutboundHTTPURL("http://127.0.0.1:8080/", "development", true); err != nil {
		t.Fatalf("expected non-production env to skip SSRF enforcement, got: %v", err)
	}
}

func containsAll(haystack string, needles ...string) bool {
	for _, n := range needles {
		if !strings.Contains(haystack, n) {
			return false
		}
	}
	return true
}
