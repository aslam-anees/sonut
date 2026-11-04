package llama

import (
	"os"
	"strings"
	"testing"
)

func TestMTPArgsSmoke(t *testing.T) {
	home, _ := os.UserHomeDir()
	args := OptimalLlamaArgs(home+"/.atcode/model/Qwopus3.5-4B-Coder-MTP-Q4_K_M.gguf", home+"/.atcode/model/mmproj-F32.gguf", 8000)
	s := strings.Join(args, " ")
	t.Log(s)
	for _, want := range []string{"draft-mtp", "--spec-draft-n-max 2", "--spec-draft-p-min 0.75", "-c 262144"} {
		if !strings.Contains(s, want) {
			t.Errorf("missing %q", want)
		}
	}
	if !strings.Contains(s, "q4_0") || strings.Contains(s, "cache-reuse") {
		t.Errorf("expected large-context KV flags without cache reuse: %s", s)
	}
	// Thinking is on by default; SONUT_NO_THINKING=1 injects the disable kwarg.
	if strings.Contains(s, "enable_thinking") {
		t.Errorf("thinking should be enabled by default: %s", s)
	}
	t.Setenv("SONUT_NO_THINKING", "1")
	s = strings.Join(OptimalLlamaArgs(home+"/.atcode/model/Qwopus3.5-4B-Coder-MTP-Q4_K_M.gguf", home+"/.atcode/model/mmproj-F32.gguf", 8000), " ")
	if !strings.Contains(s, `{"enable_thinking":false}`) {
		t.Errorf("SONUT_NO_THINKING=1 should disable thinking: %s", s)
	}
}
