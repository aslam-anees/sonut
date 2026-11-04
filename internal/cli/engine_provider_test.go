package cli

import (
	"strings"
	"testing"

	"github.com/aistatine/sonut/internal/llama"
)

func TestLocalProviderOffUsesLlamaRollback(t *testing.T) {
	t.Setenv("ATCODE_ENGINE", "off")
	provider, err := localProvider()
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := provider.(*llama.Provider); !ok {
		t.Fatalf("provider = %T, want llama rollback", provider)
	}
}

func TestLocalProviderRequiredReturnsActionableError(t *testing.T) {
	t.Setenv("ATCODE_ENGINE", "required")
	t.Setenv("ATENGINE_URL", "http://127.0.0.1:1")
	if _, err := localProvider(); err == nil || !strings.Contains(err.Error(), "@engine is required") {
		t.Fatalf("error = %v", err)
	}
}
