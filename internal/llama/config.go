package llama

import (
	"bufio"
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"strconv"
	"strings"
)

// ContextTier defines available context size options
type ContextTier string

const (
	ContextTier32K  ContextTier = "32k"
	ContextTier64K  ContextTier = "64k"
	ContextTier128K ContextTier = "128k"
	ContextTier256K ContextTier = "256k"
)

// ContextTierValue returns the token count for a tier
func (ct ContextTier) Value() int {
	switch ct {
	case ContextTier256K:
		return 262144
	case ContextTier128K:
		return 131072
	case ContextTier64K:
		return 65536
	default:
		return 32768
	}
}

// DeviceCapabilities detects hardware capabilities
type DeviceCapabilities struct {
	OSType         string
	CPUCores       int
	CPUThreads     int
	RAMBytes       int64
	HasGPU         bool
	GPUType        string
	GPUMemoryBytes int64
	IsAppleSilicon bool
}

// OptimalConfig suggests optimal configuration based on hardware
type OptimalConfig struct {
	ContextTier    ContextTier
	GPULayers      int
	BatchSize      int
	Threads        int
	ThreadsBatch   int
	RecommendedRAM string
	FallbackTier   ContextTier
}

// DetectCapabilities detects the system's hardware capabilities using real OS calls.
func DetectCapabilities() *DeviceCapabilities {
	dc := &DeviceCapabilities{
		OSType:     runtime.GOOS,
		CPUCores:   runtime.NumCPU(),
		CPUThreads: runtime.NumCPU(),
		RAMBytes:   getTotalRAM(),
	}

	switch runtime.GOOS {
	case "darwin":
		dc.HasGPU = true
		dc.IsAppleSilicon = isAppleSilicon()
		if dc.IsAppleSilicon {
			dc.GPUType = "Apple Metal (unified memory)"
		} else {
			dc.GPUType = "Metal"
		}
		// Apple Silicon uses unified memory — GPU can use all RAM
		dc.GPUMemoryBytes = dc.RAMBytes
	case "linux":
		if hasNvidiaGPU() {
			dc.HasGPU = true
			dc.GPUType = "NVIDIA CUDA"
			dc.GPUMemoryBytes = getNvidiaMemory()
		} else if hasAMDGPU() {
			dc.HasGPU = true
			dc.GPUType = "AMD ROCm"
			dc.GPUMemoryBytes = getAMDMemory()
		}
	case "windows":
		if hasNvidiaGPU() {
			dc.HasGPU = true
			dc.GPUType = "NVIDIA CUDA"
			dc.GPUMemoryBytes = getNvidiaMemory()
		}
	}

	return dc
}

// SelectContextTier returns the speed-first product default context tier.
// 128k is the stable default across hosts: it is large enough for coding
// sessions and avoids the cold-prefill cost of the native 262k KV window.
// SONUT_AUTO_CONTEXT=1 keeps the legacy RAM-tiered escape policy available for
// operators who explicitly want it.
func SelectContextTier(ramBytes int64) ContextTier {
	if atcodeEnv("AUTO_CONTEXT") != "1" {
		return ContextTier128K
	}
	ramGB := float64(ramBytes) / (1024 * 1024 * 1024)
	if ramGB >= 16 {
		return ContextTier128K
	} else if ramGB >= 8 {
		return ContextTier64K
	}
	return ContextTier32K
}

// RecommendConfig returns optimal llama-server config based on detected capabilities.
func RecommendConfig(dc *DeviceCapabilities) *OptimalConfig {
	oc := &OptimalConfig{
		ContextTier:  SelectContextTier(dc.RAMBytes),
		Threads:      dc.CPUCores,
		ThreadsBatch: 1,
	}

	if oc.ContextTier == ContextTier256K {
		oc.FallbackTier = ContextTier128K
	} else if oc.ContextTier == ContextTier128K {
		oc.FallbackTier = ContextTier32K
	}

	if dc.HasGPU {
		if dc.IsAppleSilicon {
			// Unified memory — all layers fit; Metal handles the 4B model entirely on-chip.
			oc.GPULayers = 999
		} else {
			gpuGB := float64(dc.GPUMemoryBytes) / (1024 * 1024 * 1024)
			switch {
			case gpuGB >= 20:
				oc.GPULayers = 999
			case gpuGB >= 12:
				oc.GPULayers = 40
			case gpuGB >= 8:
				oc.GPULayers = 25
			case gpuGB >= 4:
				oc.GPULayers = 10
			default:
				oc.GPULayers = 0
			}
		}
	}

	ramGB := float64(dc.RAMBytes) / (1024 * 1024 * 1024)
	switch {
	case ramGB >= 32:
		oc.BatchSize = 512
	case ramGB >= 16:
		oc.BatchSize = 256
	case ramGB >= 8:
		oc.BatchSize = 128
	default:
		oc.BatchSize = 64
	}

	oc.RecommendedRAM = fmt.Sprintf("%.1fGB", ramGB)
	return oc
}

// ParseContextTierOverride allows environment variable override.
func ParseContextTierOverride() ContextTier {
	if override := atcodeEnv("CONTEXT_SIZE"); override != "" {
		switch strings.ToLower(override) {
		case "256k", "262144":
			return ContextTier256K
		case "128k", "131072":
			return ContextTier128K
		case "64k", "65536":
			return ContextTier64K
		case "32k", "32768":
			return ContextTier32K
		}
	}
	return ""
}

// ParseGPULayersOverride allows manual GPU layer override.
func ParseGPULayersOverride() int {
	if override := atcodeEnv("GPU_LAYERS"); override != "" {
		if layers, err := strconv.Atoi(override); err == nil {
			return layers
		}
	}
	return -1
}

// --- Platform-specific helpers ---

// getTotalRAM returns the system's total physical RAM in bytes using real OS queries.
func getTotalRAM() int64 {
	const fallback = 8 * 1024 * 1024 * 1024 // 8GB safe default

	switch runtime.GOOS {
	case "darwin":
		out, err := exec.Command("sysctl", "-n", "hw.memsize").Output()
		if err != nil {
			return fallback
		}
		val, err := strconv.ParseInt(strings.TrimSpace(string(out)), 10, 64)
		if err != nil || val <= 0 {
			return fallback
		}
		return val

	case "linux":
		f, err := os.Open("/proc/meminfo")
		if err != nil {
			return fallback
		}
		defer f.Close()
		scanner := bufio.NewScanner(f)
		for scanner.Scan() {
			line := scanner.Text()
			if strings.HasPrefix(line, "MemTotal:") {
				fields := strings.Fields(line)
				if len(fields) >= 2 {
					kbs, err := strconv.ParseInt(fields[1], 10, 64)
					if err == nil && kbs > 0 {
						return kbs * 1024 // kB → bytes
					}
				}
			}
		}
		return fallback

	case "windows":
		out, err := exec.Command("wmic", "ComputerSystem", "get", "TotalPhysicalMemory", "/format:value").Output()
		if err != nil {
			return fallback
		}
		for _, line := range strings.Split(string(out), "\n") {
			line = strings.TrimSpace(line)
			if strings.HasPrefix(line, "TotalPhysicalMemory=") {
				val, err := strconv.ParseInt(strings.TrimPrefix(line, "TotalPhysicalMemory="), 10, 64)
				if err == nil && val > 0 {
					return val
				}
			}
		}
		return fallback

	default:
		return fallback
	}
}

// isAppleSilicon returns true when running on Apple M-series hardware.
func isAppleSilicon() bool {
	out, err := exec.Command("sysctl", "-n", "machdep.cpu.brand_string").Output()
	if err != nil {
		// arm64 on darwin is Apple Silicon
		return runtime.GOARCH == "arm64"
	}
	brand := strings.ToLower(string(out))
	return strings.Contains(brand, "apple") || runtime.GOARCH == "arm64"
}

// hasNvidiaGPU returns true when nvidia-smi is available and reports a GPU.
func hasNvidiaGPU() bool {
	paths := []string{
		"nvidia-smi",
		"/usr/bin/nvidia-smi",
		"/usr/local/bin/nvidia-smi",
		`C:\Windows\System32\nvidia-smi.exe`,
	}
	for _, p := range paths {
		if out, err := exec.Command(p, "--query-gpu=name", "--format=csv,noheader").Output(); err == nil {
			if strings.TrimSpace(string(out)) != "" {
				return true
			}
		}
	}
	return false
}

// hasAMDGPU returns true when rocm-smi is available.
func hasAMDGPU() bool {
	candidates := []string{"/opt/rocm/bin/rocm-smi", "rocm-smi"}
	for _, p := range candidates {
		if _, err := exec.LookPath(p); err == nil {
			return true
		}
		if _, err := os.Stat(p); err == nil {
			return true
		}
	}
	return false
}

// getNvidiaMemory returns total VRAM in bytes from nvidia-smi, or a safe default.
func getNvidiaMemory() int64 {
	out, err := exec.Command("nvidia-smi", "--query-gpu=memory.total", "--format=csv,noheader,nounits").Output()
	if err != nil {
		return 8 * 1024 * 1024 * 1024
	}
	var total int64
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		line = strings.TrimSpace(line)
		if mb, err := strconv.ParseInt(line, 10, 64); err == nil {
			total += mb * 1024 * 1024 // MiB → bytes
		}
	}
	if total == 0 {
		return 8 * 1024 * 1024 * 1024
	}
	return total
}

// getAMDMemory returns total VRAM in bytes from rocm-smi, or a safe default.
func getAMDMemory() int64 {
	out, err := exec.Command("rocm-smi", "--showmeminfo", "vram", "--csv").Output()
	if err != nil {
		return 8 * 1024 * 1024 * 1024
	}
	for _, line := range strings.Split(string(out), "\n") {
		fields := strings.Split(line, ",")
		if len(fields) >= 2 {
			if mb, err := strconv.ParseInt(strings.TrimSpace(fields[1]), 10, 64); err == nil && mb > 0 {
				return mb * 1024 * 1024
			}
		}
	}
	return 8 * 1024 * 1024 * 1024
}
