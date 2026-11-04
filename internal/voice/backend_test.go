package voice

import "testing"

func TestSelectedBackendHonorsExplicitOverride(t *testing.T) {
	t.Setenv("ATCODE_VOICE_BACKEND", "cpu")
	if got := SelectedBackend(); got != "cpu" {
		t.Fatalf("backend = %q, want cpu", got)
	}
}
