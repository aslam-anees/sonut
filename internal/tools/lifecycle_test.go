package tools

import (
	"context"
	"testing"
)

func TestNormalizeResultLifecyclePreservesSuccessfulOutput(t *testing.T) {
	result := Result{Status: StatusOK, Output: "read 12 lines"}
	NormalizeResultLifecycle(&result, nil)
	if result.Lifecycle != LifecycleSucceeded {
		t.Fatalf("lifecycle = %q, want succeeded", result.Lifecycle)
	}
	if result.Output != "read 12 lines" || result.Error != nil {
		t.Fatalf("normal success changed model-facing result: %#v", result)
	}
}

func TestNormalizeResultLifecycleClassifiesTerminalFailures(t *testing.T) {
	tests := []struct {
		name      string
		output    string
		lifecycle LifecycleStatus
		kind      ErrorKind
	}{
		{"permission", "Error: Sandbox approval required", LifecycleBlocked, ErrorSandboxBlocked},
		{"timeout", "Error: request timed out", LifecycleTimedOut, ErrorTimeout},
		{"cancel", "Error: command cancelled", LifecycleCancelled, ErrorCancelled},
		{"network", "Error: network connection refused", LifecycleFailed, ErrorNetwork},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			result := Result{Status: StatusError, Output: test.output}
			NormalizeResultLifecycle(&result, nil)
			if result.Lifecycle != test.lifecycle || result.Error == nil || result.Error.Kind != test.kind {
				t.Fatalf("got lifecycle=%q error=%#v", result.Lifecycle, result.Error)
			}
		})
	}
}

func TestNormalizeResultLifecycleUsesContextCancellation(t *testing.T) {
	result := Result{Status: StatusOK, Output: "partial"}
	NormalizeResultLifecycle(&result, context.Canceled)
	if result.Status != StatusError || result.Lifecycle != LifecycleCancelled || result.Error == nil || result.Error.Kind != ErrorCancelled {
		t.Fatalf("unexpected cancellation result: %#v", result)
	}
}

func TestScrubResultSecretsRedactsStructuredError(t *testing.T) {
	result := scrubResultSecrets(Result{
		Error: &StructuredError{
			Message: "request failed with api_key=super-secret",
			Details: "Authorization: Bearer sk-test-secret",
			Hint:    "rotate token=another-secret",
		},
	})
	if !result.Redacted {
		t.Fatal("expected structured error redaction to mark result")
	}
	if result.Error == nil {
		t.Fatal("expected structured error")
	}
	for _, value := range []string{result.Error.Message, result.Error.Details, result.Error.Hint} {
		if value == "" || value == "request failed with api_key=super-secret" || value == "Authorization: Bearer sk-test-secret" || value == "rotate token=another-secret" {
			t.Fatalf("secret was not redacted: %q", value)
		}
	}
}
