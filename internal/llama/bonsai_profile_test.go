package llama

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestIsBonsaiModelNames(t *testing.T) {
	cases := map[string]bool{
		"/x/atcode-v1.gguf":                     true,
		"/x/atcode-v1.0-mmproj.gguf":            true, // mmproj name still matches atcode
		"/x/Bonsai-27B-dspark-Q4_1.gguf":        true,
		"/x/Bonsai-27B-mmproj-BF16.gguf":        true,
		"/x/Ternary-Bonsai-27B-Q2_0.gguf":       true,
		"/x/dspark-27b-release.gguf":            true,
		"/x/Qwopus3.5-4B-Coder-MTP-Q4_K_M.gguf": false,
		"/x/OmniCoder-9B-Q4_K_M.gguf":           false,
	}
	for path, want := range cases {
		if got := isBonsaiModel(path); got != want {
			t.Errorf("isBonsaiModel(%q) = %v, want %v", path, got, want)
		}
	}
}

func TestBonsaiRecommendedCtxFits16GiB(t *testing.T) {
	// Synthetic 3.5 GiB main + 0.9 GiB mmproj on 16 GiB RAM must NOT pick 256k
	// (measured thrash: multi-minute prefills). Product target is 128k by
	// default; 64k is an explicit emergency override.
	//
	// No SONUT_AUTO_CONTEXT is required: 128k is the normal speed-first target.
	dir := t.TempDir()
	main := filepath.Join(dir, "atcode-v1.gguf")
	mmproj := filepath.Join(dir, "atcode-v1.0-mmproj.gguf")
	truncateFile(t, main, int64(3.5*1024)*1024*1024)
	truncateFile(t, mmproj, int64(900)*1024*1024)

	const ram16 = int64(16) * 1024 * 1024 * 1024
	t.Setenv("SONUT_AUTO_CONTEXT", "")
	t.Setenv("SONUT_FORCE_NATIVE_CTX", "")
	t.Setenv("SONUT_FORCE_64K", "")
	got := bonsaiRecommendedCtx(main, mmproj, ram16)
	if got < bonsaiMinCtx {
		t.Fatalf("recommended ctx %d < floor %d", got, bonsaiMinCtx)
	}
	if got >= bonsaiNativeCtx {
		t.Fatalf("recommended ctx %d on 16 GiB must be < native %d (thrash risk)", got, bonsaiNativeCtx)
	}
	// Hybrid-aware KV estimate + 6.5 GiB mmproj headroom → product 128k on 16 GiB.
	if got != bonsaiProductCtx {
		t.Fatalf("recommended ctx on 16 GiB with mmproj = %d, want product %d", got, bonsaiProductCtx)
	}

	// Explicit pressure override must still be able to drop to the coding floor.
	t.Setenv("SONUT_FORCE_64K", "1")
	got = bonsaiRecommendedCtx(main, mmproj, ram16)
	if got != bonsaiMinCtx {
		t.Fatalf("SONUT_FORCE_64K: got %d, want floor %d", got, bonsaiMinCtx)
	}

	// Operator escape hatch still forces native 262k when explicitly requested.
	t.Setenv("SONUT_FORCE_64K", "")
	t.Setenv("SONUT_FORCE_NATIVE_CTX", "1")
	got = bonsaiRecommendedCtx(main, mmproj, ram16)
	if got != bonsaiNativeCtx {
		t.Fatalf("SONUT_FORCE_NATIVE_CTX: got %d, want native %d", got, bonsaiNativeCtx)
	}
}

func TestMemoryBudgetRejectsSwapThrashBeforeContextSelection(t *testing.T) {
	const plentyAvailable = int64(8) << 30
	if !memoryBudgetHealthy(plentyAvailable, 2500<<20, swapMaxUsedBytes) {
		t.Fatal("memory exactly at the swap limit should remain usable")
	}
	if memoryBudgetHealthy(plentyAvailable, 2500<<20, swapMaxUsedBytes+1) {
		t.Fatal("swap above the production limit must force the smaller context")
	}
	if memoryBudgetHealthy(2<<30, 2500<<20, 0) {
		t.Fatal("insufficient available memory must force the smaller context")
	}
}

func TestProductContextGateFallsBackBeforeOneGiBSwap(t *testing.T) {
	const plentyAvailable = int64(8) << 30
	if !productContextMemoryHealthy(plentyAvailable, 768<<20) {
		t.Fatal("product context should remain usable at the measured safe swap ceiling")
	}
	if productContextMemoryHealthy(plentyAvailable, (768<<20)+1) {
		t.Fatal("product context must fall back to 64k once swap exceeds the tighter ceiling")
	}
	// Free pages below ~5.5 GiB must refuse 128k (weights + KV will thrash).
	if productContextMemoryHealthy(5<<30, 0) {
		t.Fatal("product context must fall back when free memory is below the 5.5 GiB floor")
	}
	if productContextMemoryHealthy(2<<30, 0) {
		t.Fatal("product context must fall back when free memory is low")
	}
}

func TestBonsaiRecommendedCtxDefaults128kOnPlentyRAM(t *testing.T) {
	dir := t.TempDir()
	main := filepath.Join(dir, "atcode-v1.gguf")
	mmproj := filepath.Join(dir, "atcode-v1.0-mmproj.gguf")
	truncateFile(t, main, int64(3.5*1024)*1024*1024)
	truncateFile(t, mmproj, int64(900)*1024*1024)

	const ram64 = int64(64) * 1024 * 1024 * 1024
	got := bonsaiRecommendedCtx(main, mmproj, ram64)
	if got != bonsaiProductCtx {
		t.Fatalf("recommended ctx on 64 GiB = %d, want product %d", got, bonsaiProductCtx)
	}
}

func TestSelectContextTierDefaultsTo128K(t *testing.T) {
	t.Setenv("SONUT_AUTO_CONTEXT", "")
	for _, ramBytes := range []int64{
		8 * 1024 * 1024 * 1024,
		16 * 1024 * 1024 * 1024,
		64 * 1024 * 1024 * 1024,
	} {
		if got := SelectContextTier(ramBytes); got != ContextTier128K {
			t.Fatalf("default context tier for %d GiB = %q, want %q", ramBytes>>30, got, ContextTier128K)
		}
	}

	// The legacy opt-in policy remains available for constrained hosts, but it
	// also no longer selects an unnecessarily large 256k window at 16 GiB.
	t.Setenv("SONUT_AUTO_CONTEXT", "1")
	if got := SelectContextTier(16 << 30); got != ContextTier128K {
		t.Fatalf("legacy auto tier at 16 GiB = %q, want %q", got, ContextTier128K)
	}
	if got := SelectContextTier(8 << 30); got != ContextTier64K {
		t.Fatalf("legacy auto tier at 8 GiB = %q, want %q", got, ContextTier64K)
	}
}

func TestOptimalLlamaArgsUses128kByDefault(t *testing.T) {
	home := t.TempDir()
	model := filepath.Join(home, "atcode-v1.gguf")
	mmproj := filepath.Join(home, "atcode-v1-mmproj.gguf")
	if err := os.WriteFile(model, []byte("source"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(mmproj, []byte("vision"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", home)
	t.Setenv("SONUT_FORCE_NATIVE_CTX", "")
	t.Setenv("ATCODE_FORCE_NATIVE_CTX", "")
	t.Setenv("SONUT_FORCE_64K", "")
	t.Setenv("ATCODE_FORCE_64K", "")
	t.Setenv("SONUT_CONTEXT_SIZE", "")
	t.Setenv("ATCODE_CONTEXT_SIZE", "")

	args := OptimalLlamaArgs(model, mmproj, 42611)
	joined := strings.Join(args, " ")
	if got := flagIntOnCmdline(joined, "-c"); got != bonsaiProductCtx {
		t.Fatalf("default Bonsai context = %d, want %d\nfull: %s", got, bonsaiProductCtx, joined)
	}
}

func truncateFile(t *testing.T, path string, size int64) {
	t.Helper()
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.Truncate(size); err != nil {
		_ = f.Close()
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestOptimalLlamaArgsBonsaiProfile(t *testing.T) {
	home := t.TempDir()
	model := filepath.Join(home, "atcode-v1.gguf")
	mmproj := filepath.Join(home, "atcode-v1.0-mmproj.gguf")
	// Empty fake files — pin native 262k so this test checks sampling/reasoning
	// flags, not the default context selector (see TestBonsaiRecommendedCtxFits16GiB).
	if err := os.WriteFile(model, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(mmproj, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("SONUT_FORCE_NATIVE_CTX", "1")
	t.Setenv("ATCODE_FORCE_NATIVE_CTX", "1")

	args := OptimalLlamaArgs(model, mmproj, 42611)
	joined := strings.Join(args, " ")

	for _, want := range []string{
		"--parallel 1",
		"--jinja",
		"--cache-type-k q4_0",
		"--cache-type-v q4_0",
		"--temp 0.5",
		"--top-p 0.85",
		"--top-k 20",
		"--min-p 0",
		"--reasoning on",
		"--reasoning-budget 256",
		"--reasoning-format deepseek",
	} {
		if !strings.Contains(joined, want) {
			t.Errorf("Bonsai args missing %q\nfull: %s", want, joined)
		}
	}
	// Prefill batch is 1024 (safe) or 2048 (fast when free RAM allows).
	gotBatch := flagIntOnCmdline(joined, "-b")
	gotUBatch := flagIntOnCmdline(joined, "-ub")
	if gotBatch < bonsaiPrefillBatch {
		t.Errorf("Bonsai -b = %d, want >= %d\nfull: %s", gotBatch, bonsaiPrefillBatch, joined)
	}
	if gotUBatch < bonsaiPrefillUBatch {
		t.Errorf("Bonsai -ub = %d, want >= %d\nfull: %s", gotUBatch, bonsaiPrefillUBatch, joined)
	}
	// Context is the source model's native 262k when FORCE_NATIVE_CTX is set
	// (this test pins the product sampling/reasoning profile, not the RAM ladder).
	gotCtx := flagIntOnCmdline(joined, "-c")
	if gotCtx != bonsaiNativeCtx {
		t.Errorf("Bonsai -c = %d, want native %d (FORCE_NATIVE_CTX)\nfull: %s", gotCtx, bonsaiNativeCtx, joined)
	}
	// Product vision: Bonsai always loads mmproj when present (unless
	// SONUT_NO_VISION), even under memory pressure — the old swap gate
	// silently dropped --mmproj and made every image attach refuse.
	if !strings.Contains(joined, "--mmproj") {
		t.Errorf("Bonsai args must include --mmproj when projector path is set\nfull: %s", joined)
	}
	if !strings.Contains(joined, "--no-mmproj-offload") {
		t.Errorf("Bonsai args must keep projector on CPU (--no-mmproj-offload)\nfull: %s", joined)
	}
	// No draft is required for the source model; cache reuse remains disabled.
	for _, ban := range []string{"draft-dflash", "draft-dspark", "draft-mtp", "--cache-reuse", " -md "} {
		if strings.Contains(joined, ban) {
			t.Errorf("Bonsai args must not contain %q\nfull: %s", ban, joined)
		}
	}
}

func TestSourceModelDoesNotAttachDraftSidecarByDefault(t *testing.T) {
	home := t.TempDir()
	modelDir := filepath.Join(home, "model")
	if err := os.MkdirAll(modelDir, 0o755); err != nil {
		t.Fatal(err)
	}
	model := filepath.Join(modelDir, "atcode-v1.gguf")
	mmproj := filepath.Join(modelDir, "atcode-v1-mmproj.gguf")
	if err := os.WriteFile(model, []byte("source"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(mmproj, []byte("vision"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(modelDir, "dspark.gguf"), []byte("draft"), 0o644); err != nil {
		t.Fatal(err)
	}

	t.Setenv("HOME", home)
	t.Setenv("SONUT_MODEL_DIR", modelDir)
	t.Setenv("SONUT_FORCE_SPECDECODE", "")
	t.Setenv("SONUT_NO_SPECDECODE", "")
	// Pin native context so this draft-sidecar test is independent of host RAM.
	t.Setenv("SONUT_FORCE_NATIVE_CTX", "1")
	t.Setenv("ATCODE_FORCE_NATIVE_CTX", "1")
	joined := strings.Join(OptimalLlamaArgs(model, mmproj, 42611), " ")
	if got := flagIntOnCmdline(joined, "-c"); got != bonsaiNativeCtx {
		t.Fatalf("source model context = %d, want %d\nfull: %s", got, bonsaiNativeCtx, joined)
	}
	for _, forbidden := range []string{"-md", "draft-dspark", "draft-dflash"} {
		if strings.Contains(joined, forbidden) {
			t.Fatalf("default source-model launch must not attach %q\nfull: %s", forbidden, joined)
		}
	}
}

func TestFormatFloat(t *testing.T) {
	if got := formatFloat(0.5); got != "0.5" {
		t.Fatalf("formatFloat(0.5) = %q", got)
	}
	if got := formatFloat(0.85); got != "0.85" {
		t.Fatalf("formatFloat(0.85) = %q", got)
	}
}

func TestBatchFlagTooSmall(t *testing.T) {
	// Live stale server observed: -b 512 -ub 512 must be rejected for Bonsai.
	stale := "llama-server -m atcode-v1.gguf -b 512 -ub 512 --parallel 1"
	if !BatchFlagTooSmall(stale, "-b", 1024) {
		t.Fatal("stale -b 512 should be too small for want 1024")
	}
	if !BatchFlagTooSmall(stale, "-ub", 1024) {
		t.Fatal("stale -ub 512 should be too small for want 1024")
	}
	good := "llama-server -m atcode-v1.gguf -b 1024 -ub 1024 --parallel 1 --reasoning on"
	if BatchFlagTooSmall(good, "-b", 1024) {
		t.Fatal("-b 1024 should satisfy want 1024")
	}
	if BatchFlagTooSmall(good, "-ub", 1024) {
		t.Fatal("-ub 1024 should satisfy want 1024")
	}
	// Stale 32k engines must be rejected so adopt path relaunches at ≥64k.
	staleCtx := "llama-server -m atcode-v1.gguf -c 32768 -b 1024 -ub 1024 --parallel 1"
	if !BatchFlagTooSmall(staleCtx, "-c", bonsaiMinCtx) {
		t.Fatal("stale -c 32768 should be too small for bonsaiMinCtx (64k)")
	}
	goodCtx := "llama-server -m atcode-v1.gguf -c 131072 -b 1024 -ub 1024 --parallel 1"
	if BatchFlagTooSmall(goodCtx, "-c", bonsaiMinCtx) {
		t.Fatal("-c 131072 should satisfy bonsaiMinCtx")
	}
}

func TestParallelProfileRequiresSingleSlot(t *testing.T) {
	for _, tc := range []struct {
		name string
		cmd  string
		want bool
	}{
		{name: "one", cmd: "llama-server --parallel 1", want: true},
		{name: "equals-one", cmd: "llama-server --parallel=1", want: true},
		{name: "two", cmd: "llama-server --parallel 2", want: false},
		{name: "four", cmd: "llama-server --parallel=4", want: false},
		{name: "missing", cmd: "llama-server", want: false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := parallelIsSingle(tc.cmd); got != tc.want {
				t.Fatalf("parallelIsSingle(%q) = %v, want %v", tc.cmd, got, tc.want)
			}
		})
	}
}
