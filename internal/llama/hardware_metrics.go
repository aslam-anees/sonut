package llama

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/aistatine/sonut/internal/observability"
)

// HardwareMetrics is saved to ~/.atcode/hardware.json after detection.
// The llama-server launcher reads this to pick optimal flags.
type HardwareMetrics struct {
	Version        int    `json:"version"` // schema version, currently 1
	OS             string `json:"os"`
	Arch           string `json:"arch"`
	CPUCores       int    `json:"cpu_cores"`
	RAMBytes       int64  `json:"ram_bytes"`
	RAMGiB         string `json:"ram_gib"`
	HasGPU         bool   `json:"has_gpu"`
	GPUType        string `json:"gpu_type,omitempty"`
	GPUMemoryBytes int64  `json:"gpu_memory_bytes,omitempty"`
	GPUMemoryGiB   string `json:"gpu_memory_gib,omitempty"`
	IsAppleSilicon bool   `json:"is_apple_silicon,omitempty"`

	// Recommended llama-server flags derived from hardware
	RecommendedNGL         int    `json:"recommended_ngl"`
	RecommendedContextSize int    `json:"recommended_context_size"`
	RecommendedBatchSize   int    `json:"recommended_batch_size"`
	RecommendedThreads     int    `json:"recommended_threads"`
	ContextTier            string `json:"context_tier"`
}

// hardwareMetricsPath returns the path to ~/.atcode/hardware.json
func hardwareMetricsPath() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".atcode", "hardware.json")
}

// DetectAndSaveHardwareMetrics runs full hardware detection and writes
// the result to ~/.atcode/hardware.json. Returns the metrics.
func DetectAndSaveHardwareMetrics() (*HardwareMetrics, error) {
	dc := DetectCapabilities()
	cfg := RecommendConfig(dc)

	ctxSize := cfg.ContextTier.Value()
	if override := ParseContextTierOverride(); override != "" {
		ctxSize = override.Value()
	}
	ngl := cfg.GPULayers
	if override := ParseGPULayersOverride(); override >= 0 {
		ngl = override
	}

	m := &HardwareMetrics{
		Version:        1,
		OS:             runtime.GOOS,
		Arch:           runtime.GOARCH,
		CPUCores:       dc.CPUCores,
		RAMBytes:       dc.RAMBytes,
		RAMGiB:         fmt.Sprintf("%.1f", float64(dc.RAMBytes)/(1024*1024*1024)),
		HasGPU:         dc.HasGPU,
		GPUType:        dc.GPUType,
		GPUMemoryBytes: dc.GPUMemoryBytes,
		IsAppleSilicon: dc.IsAppleSilicon,

		RecommendedNGL:         ngl,
		RecommendedContextSize: ctxSize,
		RecommendedBatchSize:   cfg.BatchSize,
		RecommendedThreads:     cfg.Threads,
		ContextTier:            string(cfg.ContextTier),
	}
	if dc.GPUMemoryBytes > 0 {
		m.GPUMemoryGiB = fmt.Sprintf("%.1f", float64(dc.GPUMemoryBytes)/(1024*1024*1024))
	}

	data, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return m, fmt.Errorf("failed to encode hardware metrics: %w", err)
	}

	path := hardwareMetricsPath()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return m, err
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		return m, fmt.Errorf("failed to write hardware.json: %w", err)
	}
	return m, nil
}

// LoadHardwareMetrics reads ~/.atcode/hardware.json.
// Returns nil if the file does not exist (first run).
func LoadHardwareMetrics() *HardwareMetrics {
	data, err := os.ReadFile(hardwareMetricsPath())
	if err != nil {
		return nil
	}
	var m HardwareMetrics
	if err := json.Unmarshal(data, &m); err != nil {
		return nil
	}
	if m.Version < 1 {
		return nil
	}
	return &m
}

// OptimalLlamaArgs builds llama-server arguments from saved hardware metrics.
// Falls back to safe defaults if no metrics are saved.
// mmProjPath is optional — pass "" if no vision model is present.
func OptimalLlamaArgs(modelPath, mmProjPath string, port int) []string {
	hw := LoadHardwareMetrics()
	if hw == nil {
		// No cached profile (fresh install, deleted cache): detect once and
		// persist so this launch — and every later one — gets GPU offload
		// instead of the ngl=0 CPU-only fallback. /hw refreshes it on demand.
		hw, _ = DetectAndSaveHardwareMetrics()
	}

	// Product defaults. MTP and other established profiles retain their native
	// window; the Bonsai-specific selector below changes atcode to the
	// speed-first 128k target before launch.
	ngl := 0
	ctxSize := bonsaiNativeCtx
	batchSize := 128
	yarnOrigCtxSize := nativeContextForModel(modelPath)

	if hw != nil {
		ngl = hw.RecommendedNGL
		// Use the saved RAM recommendation for non-MTP profiles. The
		// Bonsai-specific selector below is the final authority for atcode.
		const constrainedRAM = int64(18) * 1024 * 1024 * 1024
		autoCtx := atcodeEnv("AUTO_CONTEXT") == "1" ||
			(hw.RAMBytes > 0 && hw.RAMBytes <= constrainedRAM && atcodeEnv("FORCE_NATIVE_CTX") != "1")
		if autoCtx && !isMTPModel(modelPath) && hw.RecommendedContextSize > 0 {
			ctxSize = hw.RecommendedContextSize
		}
		if hw.RecommendedBatchSize > 0 {
			batchSize = hw.RecommendedBatchSize
		}
		// The static RAM tiers were sized for the 3.2 GiB 4B model. Larger
		// models (the 5.6 GiB @0.1-9b) leave less room for the KV cache, so
		// shrink the recommended context until weights + KV fit in memory.
		if autoCtx && !isMTPModel(modelPath) {
			ctxSize = capContextForModel(ctxSize, modelPath, mmProjPath, hw.RAMBytes)
		}
	}

	// Source-model-only is the default. A neighbouring dspark/DFlash file is
	// optional and must never be loaded just because it exists: it consumes RAM,
	// reduces the context window, and makes startup slower. Operators who have
	// measured a draft win can explicitly opt in with SONUT_FORCE_SPECDECODE=1.
	haveDraft := atcodeEnv("FORCE_SPECDECODE") != "" &&
		atcodeEnv("NO_SPECDECODE") == "" && FindDraftModel() != "" && memoryAllowsDraft()
	if haveDraft && ctxSize > 32768 {
		ctxSize = 32768
	}

	// Built-in MTP head (filename contains "mtp"): this is part of the source
	// GGUF, not a separate dspark/DFlash model, so it may still be used without
	// changing the source-model-only policy. It shares the normal 262k context.
	haveMTP := isMTPModel(modelPath) && !haveDraft && atcodeEnv("NO_SPECDECODE") == ""

	// Track whether the operator forced a context size. Without an explicit
	// override, Bonsai uses the speed-first 128k product default.
	userForcedCtx := false

	// User /memory override takes highest priority (persisted across restarts).
	if userCtx := LoadContextOverride(); userCtx > 0 {
		ctxSize = userCtx
		userForcedCtx = true
	}

	// Environment overrides (escape hatch; prefer /memory command instead).
	if v := atcodeEnv("CONTEXT_SIZE"); v != "" {
		if override := ParseContextTierOverride(); override != "" {
			ctxSize = override.Value()
			userForcedCtx = true
		}
	}
	if isBonsaiModel(modelPath) {
		ramBytes := int64(0)
		if hw != nil {
			ramBytes = hw.RAMBytes
		}
		if !userForcedCtx {
			// 128k on every host; see bonsaiRecommendedCtx for the measured
			// thrash rationale and FORCE_NATIVE_CTX escape hatch.
			ctxSize = bonsaiRecommendedCtx(modelPath, mmProjPath, ramBytes)
		} else if ctxSize < bonsaiMinCtx {
			// Forced but below coding floor — lift to 64k so multi-file turns work.
			ctxSize = bonsaiMinCtx
		}
	}
	if override := ParseGPULayersOverride(); override >= 0 {
		ngl = override
	}

	// Dynamic rope-scale: computed from the ratio of actual context to training context.
	// YARN extends the usable context beyond the model's native length.
	// scale=1 when ctx <= orig (no extension needed), ceil(ctx/orig) otherwise.
	ropeScale := 1
	if yarnOrigCtxSize > 0 && ctxSize > yarnOrigCtxSize {
		ropeScale = (ctxSize + yarnOrigCtxSize - 1) / yarnOrigCtxSize
	}

	// Physical (micro) batch: larger = faster prefill on Metal/CUDA.
	// llama.cpp default is the same as -b; doubling it materially cuts TTFT.
	uBatchSize := batchSize * 2
	if uBatchSize > 2048 {
		uBatchSize = 2048
	}
	// With the DFlash draft resident, large micro-batch compute buffers push the
	// Metal working set over budget (measured: mlock + draft + ub 2048 = command-
	// buffer OOM mid-decode; ub 512 runs clean). Prefill cost is minor next to a
	// dead server.
	if haveDraft && uBatchSize > 512 {
		uBatchSize = 512
	}
	// llama.cpp clamps the micro-batch to the logical batch, so -b must be at
	// least -ub or the doubling above silently does nothing (observed live:
	// "-b 256 -ub 512" prefills at an effective 256 and startup warmup crawls).
	if batchSize < uBatchSize {
		batchSize = uBatchSize
	}

	// Bonsai/atcode: lift prefill batch sizes before the base arg list is built
	// (flags like --temp / --reasoning are appended after the base slice).
	var bonsaiExtra []string
	if isBonsaiModel(modelPath) {
		applyBonsaiServerProfile(modelPath, &batchSize, &uBatchSize, &ctxSize, &haveDraft, &bonsaiExtra, hw)
		// An explicitly enabled draft must still use the stable small-context
		// profile; the normal source-model path never enters this branch.
		if haveDraft && ctxSize > 32768 {
			ctxSize = 32768
		}
	}

	args := []string{
		"-m", modelPath,
		"--port", fmt.Sprintf("%d", port),
		"--host", "127.0.0.1",
		"--api-key-file", EngineAuthFile(),
		"-c", fmt.Sprintf("%d", ctxSize),
		"-ngl", fmt.Sprintf("%d", ngl),
		"-b", fmt.Sprintf("%d", batchSize),
		"-ub", fmt.Sprintf("%d", uBatchSize),
		// Single slot, always. The prism fork defaults to 4 parallel slots, each
		// with an independent KV cache — the startup warmup prefills one slot and
		// the first real request lands on a cold one, re-prefilling the entire
		// system prompt from @code (observed live: warmup on slot 1, 5.6k tokens,
		// n_prompt_tokens_cache stayed 0). One agent = one slot = the prefix
		// cache and --cache-reuse actually pay off.
		"--parallel", "1",
		"--log-disable", // headless: suppress llama.cpp's own TUI/progress output
		// --jinja is required for llama.cpp to render the model's actual chat
		// template (including its native tool-call grammar). Bonsai-27B docs
		// require this for OpenAI-style tool_calls
		// (https://docs.prismml.com/run/llamacpp).
		"--jinja",
		// Disable the web UI. The actual port is only accessible via reverse proxy
		// on the public port, which masks the web UI and returns "atcode is serving"
		// for UI paths while proxying API requests through.
		"--no-webui",
	}
	args = append(args, bonsaiExtra...)

	// YARN rope-scaling only when the requested context actually exceeds the
	// model's native length. OmniCoder-9B is natively 256k, so it normally runs
	// with the model's own rope config untouched — forcing yarn at scale 1
	// degrades quality for models whose rope_type is not yarn (mrope here).
	if ropeScale > 1 {
		args = append(args,
			"--rope-scaling", "yarn",
			"--rope-scale", fmt.Sprintf("%d", ropeScale),
			"--yarn-orig-ctx", fmt.Sprintf("%d", yarnOrigCtxSize),
		)
	}

	// Pin model weights in RAM. Generation streams the full weight set every
	// token, so if the OS pages weights out under browser/IDE memory pressure,
	// throughput collapses (measured: 16 → 8 tok/s with llama-server at 2 GiB
	// resident of its 6 GiB and ~2 GiB swap in use). --mlock wires the mmap'd
	// weights so macOS evicts other apps instead of the model. Opt out with
	// SONUT_NO_MLOCK=1 on machines where the model shouldn't win that fight.
	if atcodeEnv("NO_MLOCK") == "" {
		args = append(args, "--mlock")
	}

	// Flash attention: required for large KV caches; generally available on Metal and CUDA.
	// Use "--flash-attn on" (not bare flag) — llama.cpp v9750+ requires an explicit value.
	if hw != nil && hw.HasGPU {
		args = append(args, "--flash-attn", "on")
	}

	// KV-cache quantization for contexts > 32k: reduces KV memory to ~25% of fp16.
	// Combined with capContextForModel above, this keeps weights + KV within
	// physical RAM (e.g. the 5.6 GiB @0.1-9b lands on 128k on 16 GB machines).
	// With a draft model resident, GPU memory is tight enough that the q4_0 KV
	// cache is forced at any context size (this is the measured-stable config).
	if ctxSize > 32768 || haveDraft || isBonsaiModel(modelPath) {
		args = append(args, "--cache-type-k", "q4_0", "--cache-type-v", "q4_0")
	} else if !isHybridAttentionModel(modelPath) {
		// KV-cache chunk reuse: when a request's prompt diverges mid-history
		// (guard nudges, edited messages, effort-instruction swaps), reuse cached
		// chunks >=256 tokens past the divergence point via KV shifting instead
		// of re-prefilling the whole tail. Cuts time-to-first-token every agent
		// turn — the delay that pushes tool-argument generation past the client
		// abort deadlines. Only with the f16 cache: KV shifting is unreliable on
		// a quantized K cache, so the q4_0 branch above stays without it.
		// Hybrid linear-attention models (OmniCoder-9B) never get cache-reuse:
		// KV shifting can't roll back their recurrent state. Their attention-
		// layer KV still quantizes fine, so the q4_0 branch applies to them —
		// measured on a 16 GiB M-series: f16 KV at 64k swaps (3 tok/s) while
		// q4_0 stays resident (16 tok/s).
		args = append(args, "--cache-reuse", "256")
	}

	// DFlash speculative decoding: when a draft GGUF sits next to the main
	// model, llama.cpp (b9960+) drafts token blocks the target verifies in one
	// pass. Measured on M4 16 GiB with OmniCoder-9B + Qwopus3.5-9B-Coder-DFlash:
	// 15.4 → up to 22 tok/s when RAM is free; roughly break-even under swap
	// pressure (the ~1.2 GiB draft footprint costs what the drafting saves).
	// n-max 16 / p-min 0.5 was the best measured trade-off (~80% acceptance).
	// SONUT_NO_SPECDECODE=1 disables it; normal launches never reach this branch
	// unless SONUT_FORCE_SPECDECODE=1 explicitly opts in.
	if haveDraft {
		draftPath := FindDraftModel()
		specType := "draft-dflash"
		if strings.Contains(strings.ToLower(filepath.Base(draftPath)), "dspark") {
			specType = "draft-dspark"
		}
		args = append(args,
			"-md", draftPath,
			"--spec-type", specType,
			"--spec-draft-n-max", "16",
			"--spec-draft-p-min", "0.5",
			"-ngld", fmt.Sprintf("%d", ngl),
		)
	}

	// MTP speculative decoding via the model's own nextn head. Measured on M4
	// 16 GiB with Qwopus3.5-4B-Coder-MTP-Q4_K_M: n-max 2 / p-min 0.75 gives
	// 30-33 tok/s at ~96% acceptance (raw ceiling 27.9); deeper drafts lose —
	// n-max 4 drops to 20, n-max 16 to 11, because Metal verify batches cost
	// more than the accepted tokens save on this GPU.
	if haveMTP {
		args = append(args,
			"--spec-type", "draft-mtp",
			"--spec-draft-n-max", "2",
			"--spec-draft-p-min", "0.75",
		)
	}

	// Thinking is ON by default for MTP coder models: without it the 4B fails
	// trick reasoning (answers fast but shallow). Cost: ~1-2k reasoning tokens
	// per reply. SONUT_NO_THINKING=1 disables it via the Qwen3 template kwarg —
	// the ONLY switch that works for this model: `/no_think` in the prompt sends
	// it into an unterminated reasoning stream with empty content at any token
	// budget, and --reasoning-budget 0 doesn't stop the template either.
	if haveMTP && atcodeEnv("NO_THINKING") != "" {
		args = append(args, "--chat-template-kwargs", `{"enable_thinking":false}`)
	}

	// Vision model (mmproj). SONUT_NO_VISION=1 skips it. Bonsai/atcode always
	// loads the projector when present (screenshot coding is a product path);
	// other families still gate on free RAM / swap — see shouldLoadMMProj.
	mmprojLoaded := false
	if mmProjPath != "" && shouldLoadMMProj(modelPath) {
		args = append(args, "--mmproj", mmProjPath)
		// Keep the vision projector on CPU: the ~1 GiB F32 projector on Metal
		// is exactly what pushed the main model + DFlash draft into GPU OOM on
		// 16 GiB machines. Vision still works; only image encode runs on CPU.
		args = append(args, "--no-mmproj-offload")
		mmprojLoaded = true
	}

	// Always-on launch diagnostic so post-run perf analysis can see the exact
	// engine profile (ctx / batch / vision / free RAM) without re-deriving it.
	observability.PerfLog("atcode_engine_launch",
		"model", filepath.Base(modelPath),
		"ctx", ctxSize,
		"ngl", ngl,
		"batch", batchSize,
		"ubatch", uBatchSize,
		"draft", haveDraft,
		"mtp", haveMTP,
		"mmproj", mmprojLoaded,
		"free_ram_mb", availableMemoryBytes()>>20,
		"swap_mb", swapUsedBytes()>>20,
		"mlock", atcodeEnv("NO_MLOCK") == "",
	)

	return args
}

// nativeContextForModel returns the model's native (training) context length,
// used as the YARN extension threshold. Known by filename:
//   - OmniCoder-9B (Qwen3.5 base): 262144 (config.json max_position_embeddings)
//   - @0.1-9b / legacy sonut models: 131072
func nativeContextForModel(modelPath string) int {
	if isHybridAttentionModel(modelPath) {
		return 262144
	}
	return 131072
}

// isHybridAttentionModel reports whether the model uses hybrid linear
// attention (Qwen3.5 family: Qwopus3.5, OmniCoder-9B). These models do not
// support KV shifting (--cache-reuse) in llama.cpp; their attention-layer KV
// cache still quantizes normally.
// isMTPModel reports whether the GGUF ships a built-in MTP (multi-token
// prediction / nextn) head usable as --spec-type draft-mtp, known by filename
// (e.g. Qwopus3.5-4B-Coder-MTP-Q4_K_M).
func isMTPModel(modelPath string) bool {
	return strings.Contains(strings.ToLower(filepath.Base(modelPath)), "mtp")
}

func isHybridAttentionModel(modelPath string) bool {
	base := strings.ToLower(filepath.Base(modelPath))
	return strings.Contains(base, "omnicoder") || strings.Contains(base, "qwopus") ||
		strings.Contains(base, "bonsai") || strings.Contains(base, "atcode")
}

// isBonsaiModel is defined in bonsai_profile.go (optimized @code model).

// capContextForModel halves the recommended context until model weights plus
// the quantized KV cache fit within physical RAM. Empirical anchor: the
// 3.2 GiB 4B model uses ~7.3 GiB of q4_0 KV at 256k context (~29 KiB/token);
// KV grows roughly in proportion to model size, so scale from that anchor.
// Never drops below the 32k floor. The selector uses this for static safety
// fitting; user /memory and env overrides are applied after this cap, so an
// explicit override still wins.
//
// Hybrid (Bonsai/atcode/Qwen3.5): only ~1/4 layers keep full-attention KV.
// When we also force q4_0 KV (Bonsai always does; hybrid dense path does too
// above 32k), the dense-q4_0 anchor over-estimates by ~2×. Apply hybridKVScale
// so 16 GiB + mmproj can land on product 128k without the old 8 GiB headroom
// hammer that forced 64k (and still thrash-killed 256k on the same box).
func capContextForModel(ctxSize int, modelPath, mmProjPath string, ramBytes int64) int {
	mainSize := fileSizeBytes(modelPath)
	if mainSize == 0 || ramBytes == 0 {
		return ctxSize
	}
	const anchor4B = 3.2 * 1024 * 1024 * 1024
	kvPerToken := float64(mainSize) / anchor4B * 29 * 1024
	// Hybrid + q4_0: ~half the dense q4_0 KV footprint (attention layers only).
	// Conservative 0.5 (theoretical ~0.25–0.35) so we never under-budget Metal.
	if isHybridAttentionModel(modelPath) {
		kvPerToken *= 0.5
	}
	weights := mainSize + fileSizeBytes(mmProjPath)
	// OS/runtime headroom. 5 GiB keeps weights+KV resident on 16 GiB M-series
	// without forcing the product below 128k. Extra 1.5 GiB when mmproj is
	// present covers CPU encode scratch + Metal command buffers (not a full
	// 8 GiB wall — that incorrectly locked coding sessions at 64k).
	headroom := float64(5 * 1024 * 1024 * 1024)
	if mmProjPath != "" && fileSizeBytes(mmProjPath) > 0 {
		headroom = float64(6.5 * 1024 * 1024 * 1024)
	}
	budget := float64(ramBytes) - float64(weights) - headroom
	for ctxSize > 32768 && float64(ctxSize)*kvPerToken > budget {
		ctxSize /= 2
	}
	// Unified-memory soft cap: even when the math says 256k fits on 16–18 GiB,
	// measured Metal thrash at -c 262144 is multi-minute "↑ 0 tok". Prefer the
	// product 128k window; larger machines keep the math result.
	if ramBytes > 0 && ramBytes <= 18*1024*1024*1024 && ctxSize > 131072 {
		ctxSize = 131072
	}
	return ctxSize
}

// fileSizeBytes returns the size of a file, or 0 if it cannot be stat'd.
func fileSizeBytes(path string) int64 {
	if path == "" {
		return 0
	}
	info, err := os.Stat(path)
	if err != nil {
		return 0
	}
	return info.Size()
}
