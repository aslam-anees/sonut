package processinsight

import (
	"context"
	"os"
	"strconv"
	"strings"
	"testing"
)

func TestInspectCurrentProcessPID(t *testing.T) {
	result, err := Inspect(context.Background(), PID(strconv.Itoa(os.Getpid())))
	if err != nil {
		t.Fatal(err)
	}
	if result.Target.Kind != TargetPID || result.ObservedAt.IsZero() {
		t.Fatalf("unexpected result: %+v", result)
	}
	if result.Process == nil && !result.Unsupported && !result.NotFound {
		t.Fatalf("expected process or an honest unavailable result: %+v", result)
	}
}

func TestInspectRejectsInvalidTarget(t *testing.T) {
	for _, target := range []Target{PID("not-a-pid"), PID("0"), Port("65536"), Port("0")} {
		if _, err := Inspect(context.Background(), target); err == nil {
			t.Fatalf("expected invalid target to fail: %+v", target)
		}
	}
}

func TestRedactArgumentsDoesNotExposeCredentialValues(t *testing.T) {
	got := redactArguments("atcode --api-key super-secret --workspace /tmp --token=another-secret")
	for _, secret := range []string{"super-secret", "another-secret"} {
		if strings.Contains(got, secret) {
			t.Fatalf("argument secret leaked: %q", got)
		}
	}
	if !strings.Contains(got, "[REDACTED]") {
		t.Fatalf("expected redaction marker: %q", got)
	}
}
