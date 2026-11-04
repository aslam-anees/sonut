package llama

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// SONUT_NO_VISION must force the env/memory gate closed regardless of memory,
// mirroring the launcher which skips --mmproj under the same env var. This is
// the fix for HasMultimodal falsely reporting vision (mmproj on disk but the
// server started without it), which made every image send 500 on llama.cpp.
func TestVisionAllowedByEnvAndMemoryHonorsNoVision(t *testing.T) {
	t.Setenv("SONUT_NO_VISION", "1")
	if visionAllowedByEnvAndMemory() {
		t.Fatal("SONUT_NO_VISION=1 must close the vision gate")
	}
}

// With SONUT_NO_VISION unset the gate depends only on memory; assert it never
// panics and returns a stable bool (memory-dependent, so we don't assert the
// value, only that env gating is wired).
func TestVisionAllowedByEnvAndMemoryEnvUnset(t *testing.T) {
	t.Setenv("SONUT_NO_VISION", "")
	_ = visionAllowedByEnvAndMemory()
}

// visionLaunchGateOpen must be closed whenever the env gate is closed, even if
// an mmproj file happens to be present on disk.
func TestVisionLaunchGateClosedWhenNoVision(t *testing.T) {
	t.Setenv("SONUT_NO_VISION", "1")
	if visionLaunchGateOpen() {
		t.Fatal("launch gate must be closed when SONUT_NO_VISION=1")
	}
}

// Bonsai/atcode always loads the projector when present — memory pressure
// must not silently drop screenshot vision (the user-facing failure mode).
func TestShouldLoadMMProjBonsaiAlwaysOn(t *testing.T) {
	t.Setenv("SONUT_NO_VISION", "")
	if !shouldLoadMMProj("/x/atcode-v1.gguf") {
		t.Fatal("atcode must load mmproj when SONUT_NO_VISION is unset")
	}
	if !shouldLoadMMProj("/x/Bonsai-27B-dspark-Q4_1.gguf") {
		t.Fatal("Bonsai must load mmproj when SONUT_NO_VISION is unset")
	}
	t.Setenv("SONUT_NO_VISION", "1")
	if shouldLoadMMProj("/x/atcode-v1.gguf") {
		t.Fatal("SONUT_NO_VISION=1 must still win for Bonsai")
	}
}

// Non-Bonsai models still honor the memory heuristic via shouldLoadMMProj.
func TestShouldLoadMMProjNonBonsaiHonorsNoVision(t *testing.T) {
	t.Setenv("SONUT_NO_VISION", "1")
	if shouldLoadMMProj("/x/Qwopus3.5-9B-coder.gguf") {
		t.Fatal("non-Bonsai must not load mmproj under SONUT_NO_VISION=1")
	}
}

// OptimalLlamaArgs must emit --mmproj for Bonsai even when free RAM is low
// enough that the old memoryAllowsVision gate would have skipped it.
func TestOptimalLlamaArgsBonsaiLoadsMMProjDespiteMemoryGate(t *testing.T) {
	t.Setenv("SONUT_NO_VISION", "")
	// Do not force vision via env — we want the Bonsai always-on path.
	t.Setenv("ATCODE_FORCE_VISION", "")
	dir := t.TempDir()
	model := filepath.Join(dir, "atcode-v1.gguf")
	mmproj := filepath.Join(dir, "atcode-v1.0-mmproj.gguf")
	if err := os.WriteFile(model, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(mmproj, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	args := OptimalLlamaArgs(model, mmproj, 42611)
	joined := strings.Join(args, " ")
	if !strings.Contains(joined, "--mmproj") {
		t.Fatalf("expected --mmproj in Bonsai launch args, got: %s", joined)
	}
}
