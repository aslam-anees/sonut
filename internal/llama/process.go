package llama

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// SaveContextOverride persists the user's chosen context size to ~/.atcode/context_override.
// OptimalLlamaArgs reads this on every launch so the setting survives restarts.
func SaveContextOverride(ctxSize int) error {
	home, err := os.UserHomeDir()
	if err != nil {
		return err
	}
	path := filepath.Join(home, ".atcode", "context_override")
	return os.WriteFile(path, []byte(strconv.Itoa(ctxSize)), 0o644)
}

// LoadContextOverride reads the user's persisted context size from
// ~/.atcode/context_override. Returns 0 when no override is set.
func LoadContextOverride() int {
	home, _ := os.UserHomeDir()
	data, err := os.ReadFile(filepath.Join(home, ".atcode", "context_override"))
	if err != nil {
		return 0
	}
	v, err := strconv.Atoi(strings.TrimSpace(string(data)))
	if err != nil || v <= 0 {
		return 0
	}
	return v
}

// KillLlamaOnPort terminates any process listening on the given TCP port.
// Sends SIGINT first, then polls for 10 seconds waiting for it to exit.
// If still running after 10 seconds, force-kills it.
// Uses lsof (available on macOS and most Linux distributions).
func KillLlamaOnPort(port int) {
	KillLlamaOnPortWithTimeout(port, 10*time.Second)
}

// KillLlamaOnPortWithTimeout is KillLlamaOnPort with a caller-chosen grace
// window before SIGKILL. Exit/cleanup paths use a short timeout so quitting
// the TUI does not hang for 10s waiting on a stuck engine.
//
// Signals target the process group when the listener is a group leader (how
// startLlamaCmd launches llama-server), so the whole engine tree dies with
// atcode instead of leaving multi-GB orphans after quit.
func KillLlamaOnPortWithTimeout(port int, grace time.Duration) {
	if port <= 0 {
		return
	}
	if grace <= 0 {
		grace = 500 * time.Millisecond
	}
	// First pass: SIGINT for graceful stop (llama-server flushes KV / exits cleanly).
	for _, pid := range pidsOnPort(port) {
		signalPIDBestEffort(pid, os.Interrupt)
	}

	deadline := time.Now().Add(grace)
	for time.Now().Before(deadline) {
		if len(pidsOnPort(port)) == 0 {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}

	// Force-kill anything still listening on the port.
	for _, pid := range pidsOnPort(port) {
		signalPIDBestEffort(pid, os.Kill)
	}
	// Brief drain so the port is free before a possible relaunch in the same process.
	drainDeadline := time.Now().Add(300 * time.Millisecond)
	for time.Now().Before(drainDeadline) {
		if len(pidsOnPort(port)) == 0 {
			return
		}
		time.Sleep(25 * time.Millisecond)
	}
}

// ServerProfileIsCurrent reports whether the llama-server on port already runs
// with the optimized profile flags we require for fast @code turns.
//
// The adopt-if-healthy startup path used to keep ANY healthy process, so a
// leftover server started with -b 512, -c 32768 (or no --reasoning budget)
// starved prefill forever or rejected multi-turn coding prompts. When the
// active main GGUF is Bonsai/atcode, require:
//   - -c matches the speed-first target from bonsaiRecommendedCtx (128k by
//     default; 64k/262k only through explicit overrides). Oversized native
//     leftovers are rejected and restarted (measured thrash).
//   - -b / -ub at least bonsaiPrefillBatch (1024)
//   - --parallel 1
//
// Non-Bonsai models always pass (no forced restart).
func ServerProfileIsCurrent(port int) bool {
	modelPath, err := GetModelPath()
	if err != nil || modelPath == "" {
		modelPath = findBonsaiMainOnDisk()
	}
	if modelPath == "" || !isBonsaiModel(modelPath) {
		return true // nothing to enforce
	}
	cmdLine := cmdlineOnPort(port)
	if cmdLine == "" {
		// Can't inspect — treat as current so we don't thrash restart loops
		// on platforms without lsof/ps. Health already passed.
		return true
	}
	// Migrate servers with the old shared password or a different key file.
	if !strings.Contains(cmdLine, "--api-key-file") || !strings.Contains(cmdLine, EngineAuthFile()) {
		return false
	}
	// Context: must meet the coding floor AND not exceed the speed-first target
	// (a forced 256k on 16 GiB thrash-kills prefill).
	gotCtx := flagIntOnCmdline(cmdLine, "-c")
	if gotCtx < bonsaiMinCtx {
		return false
	}
	ramBytes := int64(0)
	if hw := LoadHardwareMetrics(); hw != nil {
		ramBytes = hw.RAMBytes
	}
	// mmproj path is best-effort for the RAM estimate; empty is fine (slightly
	// more optimistic budget). findBonsaiMMProjOnDisk keeps the estimate tight
	// when the projector is present next to the main GGUF.
	wantCtx := bonsaiRecommendedCtx(modelPath, findBonsaiMMProjOnDisk(), ramBytes)
	// Honour an explicit operator override the same way OptimalLlamaArgs does.
	if userCtx := LoadContextOverride(); userCtx > 0 {
		wantCtx = userCtx
		if wantCtx < bonsaiMinCtx {
			wantCtx = bonsaiMinCtx
		}
	}
	if gotCtx < wantCtx {
		return false
	}
	// Oversize relative to the auto target → thrash risk. Allow equality and
	// modest headroom (one doubling) only when the operator forced a larger
	// window via /memory; otherwise restart to the recommended size.
	if userCtx := LoadContextOverride(); userCtx <= 0 && gotCtx > wantCtx {
		return false
	}
	// A draft sidecar is intentionally opt-in. Do not adopt a stale engine that
	// was launched with dspark/DFlash just because its health endpoint responds.
	if atcodeEnv("FORCE_SPECDECODE") == "" && strings.Contains(cmdLine, "-md") {
		return false
	}
	// Batch sizes: -b 1024 -ub 1024 (or larger). Reject bare -b 512.
	if batchFlagTooSmall(cmdLine, "-b", bonsaiPrefillBatch) {
		return false
	}
	if batchFlagTooSmall(cmdLine, "-ub", bonsaiPrefillUBatch) {
		return false
	}
	// Single slot is mandatory for prefix reuse on the one agent.
	if !strings.Contains(cmdLine, "--parallel") {
		return false
	}
	// Prefer explicit 1; any other value is wrong for the agent. A stale
	// multi-slot server can accept the warmup on one slot and the real request
	// on another, forcing a full prefill even though both requests set
	// cache_prompt=true.
	if !parallelIsSingle(cmdLine) {
		return false
	}
	// Vision: when the launcher would pass --mmproj (projector on disk +
	// shouldLoadMMProj for this GGUF), the live process must also have it.
	// Otherwise a leftover server started under the old memory gate (or before
	// the mmproj was downloaded) is adopted forever and every image attach
	// either 500s or is refused as "no vision".
	if findBonsaiMMProjOnDisk() != "" && shouldLoadMMProj(modelPath) {
		if !strings.Contains(cmdLine, "--mmproj") {
			return false
		}
	}
	return true
}

// parallelIsSingle reports whether a server command line explicitly selects
// one slot. Do not rely on substring checks here: --parallel 2, --parallel=3,
// and a missing flag all defeat deterministic prefix-cache reuse.
func parallelIsSingle(cmdLine string) bool {
	return flagIntOnCmdline(cmdLine, "--parallel") == 1
}

// flagIntOnCmdline returns the integer value of flag ("-c", "-b", …) on
// cmdLine, or 0 if missing / unparsable.
func flagIntOnCmdline(cmdLine, flag string) int {
	fields := strings.Fields(cmdLine)
	for i, f := range fields {
		if f == flag && i+1 < len(fields) {
			n, err := strconv.Atoi(fields[i+1])
			if err != nil {
				return 0
			}
			return n
		}
		if strings.HasPrefix(f, flag+"=") {
			n, err := strconv.Atoi(strings.TrimPrefix(f, flag+"="))
			if err != nil {
				return 0
			}
			return n
		}
	}
	return 0
}

// batchFlagTooSmall reports whether cmdLine's -b / -ub value is below want.
// Missing flag is treated as too small (stale server without the profile).
func batchFlagTooSmall(cmdLine, flag string, want int) bool {
	fields := strings.Fields(cmdLine)
	for i, f := range fields {
		if f == flag && i+1 < len(fields) {
			n, err := strconv.Atoi(fields[i+1])
			if err != nil {
				return true
			}
			return n < want
		}
		if strings.HasPrefix(f, flag+"=") {
			n, err := strconv.Atoi(strings.TrimPrefix(f, flag+"="))
			if err != nil {
				return true
			}
			return n < want
		}
	}
	return true // flag missing → too small
}

// BatchFlagTooSmall is exported for tests.
func BatchFlagTooSmall(cmdLine, flag string, want int) bool {
	return batchFlagTooSmall(cmdLine, flag, want)
}
