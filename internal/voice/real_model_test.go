package voice

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/aistatine/sonut/internal/config"
)

// TestRealModelVoiceSmoke is deliberately opt-in. Starting the helper loads
// the installed Qwen/Supertonic sessions but never downloads models or writes
// microphone audio. Full multilingual accuracy and latency measurements use
// the fixture manifest and the benchmark procedure in docs/voice-benchmarks.md.
func TestRealModelVoiceSmoke(t *testing.T) {
	if os.Getenv("ATCODE_VOICE_REAL_MODEL_TESTS") != "1" {
		t.Skip("set ATCODE_VOICE_REAL_MODEL_TESTS=1 to load the installed voice pack")
	}
	helper := DiscoverHelperPath()
	if _, err := os.Stat(helper); err != nil {
		t.Skipf("atcode-voice is not installed at %s", helper)
	}
	ready := make(chan Event, 8)
	manager := NewManager(ManagerOptions{
		Config:     config.DefaultVoiceConfig(),
		HelperPath: helper,
		OnEvent:    func(event Event) { ready <- event },
	})
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	if err := manager.Start(ctx); err != nil {
		t.Fatal(err)
	}
	defer manager.Close()
	deadline := time.NewTimer(2 * time.Minute)
	defer deadline.Stop()
	for {
		select {
		case event := <-ready:
			if event.Type == EventReady {
				return
			}
			if event.Type == EventError && event.Error != nil {
				t.Fatal(event.Error)
			}
		case <-deadline.C:
			t.Fatal("timed out waiting for the warm native helper")
		}
	}
}
