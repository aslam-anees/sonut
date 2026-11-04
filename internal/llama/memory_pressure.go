package llama

import (
	"os"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
)

// Memory-pressure gating for optional server components.
//
// The DFlash draft (~0.8 GiB) and the F32 vision projector (~0.9 GiB) each
// speed up or extend the system ONLY while physical RAM is free. Measured on
// M4 16 GiB (2026-07-14): with RAM free, DFlash lifts generation 16 → 24
// tok/s; with Chrome/IDE pressure and ~2.5 GiB swap in use, the same config
// collapses to 2.5 tok/s (vs 16 bare) because the extra footprint evicts the
// working set. So the launcher samples actual availability at startup and
// loads extras only when they will pay for themselves.

// draftMinAvailBytes: the draft weights + its KV + headroom must fit without
// evicting the main model's working set.
const draftMinAvailBytes = 3 << 30 // 3 GiB

// mmprojMinAvailBytes: the CPU-resident F32 projector needs ~1 GiB; require
// double so loading it doesn't tip the machine into swap.
const mmprojMinAvailBytes = 2 << 30 // 2 GiB

// swapMaxUsedBytes: once a 16 GiB machine has crossed 1 GiB of swap, measured
// local generation can collapse from interactive speed to roughly 3 tok/s.
// Treat that as pressure before selecting a larger context or optional weights.
const swapMaxUsedBytes = 1 << 30 // 1 GiB

var vmStatLineRe = regexp.MustCompile(`^(.+?):\s+(\d+)\.?$`)

// availableMemoryBytes returns an estimate of memory that could be used
// without swapping (free + inactive + purgeable + speculative on macOS,
// MemAvailable on Linux). Returns 0 when it cannot tell.
func availableMemoryBytes() int64 {
	if out, err := exec.Command("vm_stat").Output(); err == nil {
		pageSize := int64(16384)
		var pages int64
		counted := map[string]bool{
			"Pages free":        true,
			"Pages inactive":    true,
			"Pages purgeable":   true,
			"Pages speculative": true,
		}
		for _, line := range strings.Split(string(out), "\n") {
			if strings.HasPrefix(line, "Mach Virtual Memory Statistics") {
				if m := regexp.MustCompile(`page size of (\d+)`).FindStringSubmatch(line); m != nil {
					pageSize, _ = strconv.ParseInt(m[1], 10, 64)
				}
				continue
			}
			m := vmStatLineRe.FindStringSubmatch(strings.TrimSpace(line))
			if m == nil || !counted[m[1]] {
				continue
			}
			n, _ := strconv.ParseInt(m[2], 10, 64)
			pages += n
		}
		if pages > 0 {
			return pages * pageSize
		}
	}
	// Linux fallback.
	if data, err := os.ReadFile("/proc/meminfo"); err == nil {
		for _, line := range strings.Split(string(data), "\n") {
			if strings.HasPrefix(line, "MemAvailable:") {
				fields := strings.Fields(line)
				if len(fields) >= 2 {
					kb, _ := strconv.ParseInt(fields[1], 10, 64)
					return kb << 10
				}
			}
		}
	}
	return 0
}

// swapUsedBytes returns how much swap is currently in use (0 if unknown).
func swapUsedBytes() int64 {
	out, err := exec.Command("sysctl", "-n", "vm.swapusage").Output()
	if err != nil {
		return 0
	}
	// "total = 4096.00M  used = 2524.88M  free = 1571.12M  (encrypted)"
	m := regexp.MustCompile(`used = ([\d.]+)M`).FindStringSubmatch(string(out))
	if m == nil {
		return 0
	}
	mb, _ := strconv.ParseFloat(m[1], 64)
	return int64(mb * (1 << 20))
}

// memoryBudgetHealthy centralizes the pressure decision so every optional
// component and context-size gate uses the same production threshold.
func memoryBudgetHealthy(available, minimumAvailable, swapUsed int64) bool {
	return available >= minimumAvailable && swapUsed <= swapMaxUsedBytes
}

// memoryAllowsDraft reports whether loading the speculative-decoding draft is
// likely to speed things up rather than push the machine into swap.
// SONUT_FORCE_SPECDECODE=1 bypasses the check (SONUT_NO_SPECDECODE still wins
// at the call site).
func memoryAllowsDraft() bool {
	if atcodeEnv("FORCE_SPECDECODE") != "" {
		return true
	}
	avail, swap := availableMemoryBytes(), swapUsedBytes()
	if avail == 0 {
		return true // unknown platform: keep prior behavior
	}
	return memoryBudgetHealthy(avail, draftMinAvailBytes, swap)
}

// memoryAllowsVision reports whether the vision projector fits comfortably.
// ATCODE_FORCE_VISION=1 bypasses the check (ATCODE_NO_VISION still wins at the
// call site).
func memoryAllowsVision() bool {
	if atcodeEnv("FORCE_VISION") != "" {
		return true
	}
	avail, swap := availableMemoryBytes(), swapUsedBytes()
	if avail == 0 {
		return true
	}
	return memoryBudgetHealthy(avail, mmprojMinAvailBytes, swap)
}
