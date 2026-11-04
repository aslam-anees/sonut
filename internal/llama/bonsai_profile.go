package llama

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// Optimized @code model profile.
//
// Sources of truth:
//   - https://docs.prismml.com/run/llamacpp
//   - https://docs.prismml.com/run/server
//   - https://docs.prismml.com/models/bonsai-27b
//
// atcode-v1.gguf is the user-facing rename of Bonsai-27B (1-bit / dspark Q4_1
// family) plus its vision projector, running on the optimized @code engine.
// Bonsai 27B is a hybrid-attention reasoning model with 262k native context.
// Coding sessions (system prompt + tools + repo context) routinely exceed 32k,
// so atcode uses a 128k default with q4_0 KV: it leaves enough room for real
// repository work without paying the multi-minute cold-prefill cost of a
// 262k KV allocation on everyday Macs. The native 262k window remains an
// explicit operator choice. Separate dspark/DFlash files are optional and are
// not loaded by default.

// Product sampling defaults. These are
// what published Bonsai benchmarks assume — not Qwen-generic 0.2/0.6 defaults.
const (
	bonsaiTemp = 0.5
	bonsaiTopP = 0.85
	bonsaiTopK = 20
	bonsaiMinP = 0.0
	// Server-side thinking budget (tokens). Thinking remains ON by default, but
	// the old 512-token ceiling made post-tool executor turns spend roughly
	// 25-30s rehashing the same plan on a 15 tok/s local model. 256 tokens keeps
	// meaningful reasoning while putting a tighter bound on repetition and idle
	// gaps. Override with SONUT_REASONING_BUDGET for unusually hard tasks.
	bonsaiDefaultReasoningBudget = 256
	// Prefill micro-batch for Bonsai on Metal without a draft model. Larger
	// ubatch cuts cold TTFT. 1024 is the safe floor; applyBonsaiServerProfile
	// lifts to 2048 when free RAM allows (2048 was OOM only with draft resident).
	bonsaiPrefillBatch  = 1024
	bonsaiPrefillUBatch = 1024
	// Fast prefill ceiling when RAM is free (no draft, swap quiet).
	bonsaiPrefillBatchFast  = 2048
	bonsaiPrefillUBatchFast = 2048
	// bonsaiNativeCtx is the model's trained maximum window (docs: 262k). It is
	// available for explicit native-context runs, not the normal launch target.
	bonsaiNativeCtx = 262144
	// bonsaiMinCtx remains the validation floor for explicit overrides and stale
	// server profiles.
	bonsaiMinCtx = 65536
	// bonsaiProductCtx is the fast product default. It is large enough for
	// multi-file coding turns while avoiding the 262k prefill/KV penalty.
	bonsaiProductCtx = 131072
)

// bonsaiRecommendedCtx returns a usable context for the host.
//
// Product rule (measured on M-series 16 GiB, 2026-07-30 perf.log): use 128k
// by default on every host, then apply static model/RAM fitting when hardware
// data is available. Starting at 128k avoids the native 262k KV allocation
// that caused multi-minute "↑ 0 tok" prefills, while retaining 64k as an
// emergency explicit override and 262k as an explicit maximum.
//
// Escape hatches:
//   - SONUT_FORCE_NATIVE_CTX=1 — always 262k (benchmarks / big workstations).
//   - SONUT_FORCE_64K=1 — pin the smaller emergency rung.
//   - SONUT_FORCE_128K=1 — keep the 128k product rung when live pressure gates
//     are queried by diagnostics.
//   - /memory and SONUT_CONTEXT_SIZE — applied by OptimalLlamaArgs, not here.
func bonsaiRecommendedCtx(modelPath, mmProjPath string, ramBytes int64) int {
	if atcodeEnv("FORCE_NATIVE_CTX") == "1" {
		return bonsaiNativeCtx
	}
	if atcodeEnv("FORCE_64K") == "1" {
		return bonsaiMinCtx
	}
	ctx := bonsaiProductCtx
	if ramBytes > 0 {
		ctx = capContextForModel(ctx, modelPath, mmProjPath, ramBytes)
	}
	if ctx < bonsaiMinCtx {
		ctx = bonsaiMinCtx
	}
	if ctx > bonsaiProductCtx {
		ctx = bonsaiProductCtx
	}
	return ctx
}

// memoryAllowsProductCtx reports whether the machine currently has enough free
// memory to run the product 128k window without swapping. It is retained for
// diagnostics and the prefill policy; context selection itself no longer
// silently drops the user's 128k default because of transient live pressure.
func memoryAllowsProductCtx() bool {
	if atcodeEnv("FORCE_128K") != "" {
		return true
	}
	if atcodeEnv("FORCE_64K") != "" {
		return false
	}
	avail, swap := availableMemoryBytes(), swapUsedBytes()
	if avail == 0 {
		return true // unknown platform: keep the math result
	}
	return productContextMemoryHealthy(avail, swap)
}

// productContextMemoryHealthy is deliberately stricter than the generic/fast-
// batch swap gate. It remains a diagnostic signal for prefill tuning; it does
// not change the normal 128k context selection.
func productContextMemoryHealthy(available, swapUsed int64) bool {
	const (
		// 128k product window needs headroom AFTER model mlock + mmproj.
		// Live supermario probe: free≈3.8–7 GiB pre-launch still chose 128k,
		// then thrash-risk once weights locked. Require ~5.5 GiB free pages
		// before advertising 128k; otherwise drop to the 64k coding floor.
		minFree        = 5500 << 20
		maxProductSwap = 768 << 20 // pressure observed before the old 1 GiB gate
	)
	return available >= minFree && swapUsed <= maxProductSwap
}

// isBonsaiModel reports whether the GGUF is an optimized @code model.
// Filenames we accept:
//   - atcode-v1.gguf, atcode-v1.0.gguf
//   - Bonsai-27B-*.gguf, Bonsai-27B-dspark-Q4_1.gguf
//   - Ternary-Bonsai-27B-*.gguf
func isBonsaiModel(modelPath string) bool {
	base := strings.ToLower(filepath.Base(modelPath))
	if strings.Contains(base, "atcode") || strings.Contains(base, "bonsai") {
		return true
	}
	// A predecessor training tag that sometimes appears without "bonsai" in the
	// basename after a rename slip (e.g. only "dspark" left in a symlink name).
	if strings.Contains(base, "dspark") && strings.Contains(base, "27b") {
		return true
	}
	return false
}

// applyBonsaiServerProfile mutates batch/draft and appends product-tuned
// server flags into extra. Context is floored only to bonsaiMinCtx (64k) here;
// OptimalLlamaArgs applies the 128k product target, with native 262k reserved
// for an explicit override, so we avoid a forced 256k KV allocation.
func applyBonsaiServerProfile(modelPath string, batchSize, uBatchSize, ctxSize *int, haveDraft *bool, extra *[]string, hw *HardwareMetrics) {
	if !isBonsaiModel(modelPath) {
		return
	}
	if ctxSize != nil && *ctxSize < bonsaiMinCtx {
		*ctxSize = bonsaiMinCtx
	}
	// Prefill throughput: force a large logical+physical batch when no draft.
	// hw.RecommendedBatchSize is often 256 (generic); that starves Metal prefill.
	// Lift to 2048 when free RAM is healthy — measured TTFT cut vs 1024 with no
	// draft resident. Stay at 1024 under swap pressure (command-buffer OOM risk).
	wantBatch, wantUBatch := bonsaiPrefillBatch, bonsaiPrefillUBatch
	if *haveDraft {
		wantBatch, wantUBatch = 512, 512
	}
	if memoryAllowsFastPrefill() {
		wantBatch, wantUBatch = bonsaiPrefillBatchFast, bonsaiPrefillUBatchFast
	}
	if *haveDraft {
		// The draft's verify buffers make the fast 2048 path unsafe on Metal.
		wantBatch, wantUBatch = 512, 512
	}
	if *batchSize < wantBatch {
		*batchSize = wantBatch
	}
	if *uBatchSize < wantUBatch {
		*uBatchSize = wantUBatch
	}
	if *batchSize < *uBatchSize {
		*batchSize = *uBatchSize
	}

	if extra == nil {
		return
	}

	// Product sampling defaults on the server (request body can still override).
	*extra = append(*extra,
		"--temp", formatFloat(bonsaiTemp),
		"--top-p", formatFloat(bonsaiTopP),
		"--top-k", strconv.Itoa(bonsaiTopK),
		"--min-p", formatFloat(bonsaiMinP),
	)

	// 27B is a reasoning model (docs: thinking on by default). Keep it on,
	// but cap the token budget so a stuck think can't pin the single slot.
	budget := bonsaiDefaultReasoningBudget
	if v := strings.TrimSpace(atcodeEnv("REASONING_BUDGET")); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			budget = n
		}
	}
	if atcodeEnv("NO_THINKING") != "" {
		*extra = append(*extra, "--reasoning", "off", "--reasoning-budget", "0")
	} else {
		*extra = append(*extra,
			"--reasoning", "on",
			"--reasoning-budget", strconv.Itoa(budget),
			"--reasoning-format", "deepseek",
		)
	}

	// CPU threads help prompt processing on hybrid models (linear layers).
	if hw != nil && hw.RecommendedThreads > 0 {
		t := strconv.Itoa(hw.RecommendedThreads)
		*extra = append(*extra, "--threads", t, "--threads-batch", t)
	}
}

// bonsaiSampling returns product-tuned sampling for @code requests.
// Always use these when the active main model is Bonsai — not the generic
// Qwen 0.2 (code) / 0.6 (think) defaults, which under/over-sample this model.
func bonsaiSampling() (temp, topP float64, topK int, minP float64, ok bool) {
	path, err := GetModelPath()
	if err != nil || path == "" {
		// Fall back to scanning the model dir for an atcode/bonsai main GGUF.
		path = findBonsaiMainOnDisk()
	}
	if path == "" || !isBonsaiModel(path) {
		return 0, 0, 0, 0, false
	}
	return bonsaiTemp, bonsaiTopP, bonsaiTopK, bonsaiMinP, true
}

func findBonsaiMainOnDisk() string {
	dir := GetModelDir()
	entries, err := os.ReadDir(dir)
	if err != nil {
		return ""
	}
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		lower := strings.ToLower(name)
		if !strings.HasSuffix(lower, ".gguf") {
			continue
		}
		if strings.Contains(lower, "mmproj") || strings.Contains(lower, "dflash") || strings.Contains(lower, "draft") {
			continue
		}
		full := filepath.Join(dir, name)
		if isBonsaiModel(full) {
			return full
		}
	}
	return ""
}

// findBonsaiMMProjOnDisk returns the path of an atcode/Bonsai vision projector
// next to the main GGUF, or "" if none is present. Used only for RAM-budget
// estimates (ServerProfileIsCurrent / recommended ctx).
func findBonsaiMMProjOnDisk() string {
	dir := GetModelDir()
	entries, err := os.ReadDir(dir)
	if err != nil {
		return ""
	}
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		lower := strings.ToLower(name)
		if !strings.HasSuffix(lower, ".gguf") || !strings.Contains(lower, "mmproj") {
			continue
		}
		if strings.Contains(lower, "atcode") || strings.Contains(lower, "bonsai") {
			return filepath.Join(dir, name)
		}
	}
	return ""
}

func formatFloat(f float64) string {
	// Avoid trailing noise (0.500000) in process listings.
	return strings.TrimRight(strings.TrimRight(fmt.Sprintf("%.4f", f), "0"), ".")
}

// memoryAllowsFastPrefill reports whether -b/-ub 2048 is safe (no draft, free
// pages, low swap). SONUT_FORCE_FAST_PREFILL=1 forces it; SONUT_SAFE_PREFILL=1
// keeps the 1024 floor.
func memoryAllowsFastPrefill() bool {
	if atcodeEnv("SAFE_PREFILL") != "" {
		return false
	}
	if atcodeEnv("FORCE_FAST_PREFILL") != "" {
		return true
	}
	avail, swap := availableMemoryBytes(), swapUsedBytes()
	if avail == 0 {
		return true
	}
	const minFree = 2 << 30 // 2 GiB free before model fully resident
	return avail >= minFree && swap <= swapMaxUsedBytes
}
