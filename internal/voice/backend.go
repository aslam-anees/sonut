package voice

import (
	"os"
	"runtime"

	"github.com/aistatine/sonut/internal/llama"
)

const voiceGPUHeadroomFloor = int64(12) * 1024 * 1024 * 1024

// SelectedBackend reuses the startup hardware profile for the native helper.
// An explicit environment value is authoritative; automatic GPU use is limited
// to machines whose cached profile reports enough total GPU memory for the
// light pack plus an already-running agent. CPU is the safe fallback when the
// profile is absent or incomplete.
func SelectedBackend() string {
	if backend := os.Getenv("ATCODE_VOICE_BACKEND"); backend != "" {
		return backend
	}
	hardware := llama.LoadHardwareMetrics()
	if hardware == nil || !hardware.HasGPU || hardware.GPUMemoryBytes < voiceGPUHeadroomFloor {
		return "cpu"
	}
	if runtime.GOOS == "darwin" && hardware.IsAppleSilicon {
		return "metal"
	}
	return "best"
}
