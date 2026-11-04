package llama

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
)

const (
	// AtcodeServerBaseName is the stable, user-visible engine executable name.
	AtcodeServerBaseName = "atcode-server"
	// AtcodeServerLogName is the stable, user-visible engine log name.
	AtcodeServerLogName = "atcode-server.log"
)

// AtcodeServerLogPath returns the canonical engine log path.
func AtcodeServerLogPath() string {
	home, err := os.UserHomeDir()
	if err != nil || strings.TrimSpace(home) == "" {
		return filepath.Join(".atcode", AtcodeServerLogName)
	}
	return filepath.Join(home, ".atcode", AtcodeServerLogName)
}

// FindAtcodeServer resolves the branded engine executable. Canonical @code
// locations win; upstream executable names are accepted only as compatibility
// fallbacks for system package-manager installs.
func FindAtcodeServer() (string, bool) {
	for _, key := range []string{
		"ATCODE_SERVER_PATH",
		"ATCODE_ENGINE_PATH",
		"ATCODE_LLAMA_SERVER",
		"SONUT_LLAMA_SERVER",
	} {
		if path := strings.TrimSpace(os.Getenv(key)); isExecutableFile(path) {
			return path, true
		}
	}

	// Upgrades are self-healing: move predecessor runtime names out of the
	// user's ~/.atcode tree before resolving the executable.
	_ = MigrateLegacyRuntimeNames()

	executable := serverExecutableName(runtime.GOOS)
	if path, err := exec.LookPath(executable); err == nil {
		return path, true
	}

	home, _ := os.UserHomeDir()
	for _, path := range canonicalServerCandidates(home, runtime.GOOS) {
		if isExecutableFile(path) {
			return path, true
		}
	}

	// Package-manager compatibility. These names are outside ~/.atcode and
	// therefore remain owned by the external package manager.
	upstreamExecutable := "llama-server"
	if runtime.GOOS == "windows" {
		upstreamExecutable += ".exe"
	}
	if path, err := exec.LookPath(upstreamExecutable); err == nil {
		return path, true
	}
	for _, path := range upstreamServerCandidates(home, runtime.GOOS) {
		if isExecutableFile(path) {
			return path, true
		}
	}
	return "", false
}

func canonicalServerCandidates(home, goos string) []string {
	name := serverExecutableName(goos)
	candidates := []string{}
	if executable, err := os.Executable(); err == nil {
		candidates = append(candidates, filepath.Join(filepath.Dir(executable), name))
	}
	if home != "" {
		candidates = append(candidates,
			filepath.Join(home, ".atcode", "engine", name),
			filepath.Join(home, ".atcode", "engine", "bin", name),
			filepath.Join(home, ".atcode", "engine", "build", "bin", name),
			filepath.Join(home, ".atcode", "bin", name),
		)
	}
	return candidates
}

func upstreamServerCandidates(home, goos string) []string {
	switch goos {
	case "darwin":
		return []string{
			"/opt/homebrew/bin/llama-server",
			"/usr/local/bin/llama-server",
		}
	case "linux":
		return []string{
			"/usr/local/bin/llama-server",
			"/usr/bin/llama-server",
		}
	case "windows":
		return []string{`C:\Program Files\llama.cpp\llama-server.exe`}
	default:
		return nil
	}
}

func serverExecutableName(goos string) string {
	if goos == "windows" {
		return AtcodeServerBaseName + ".exe"
	}
	return AtcodeServerBaseName
}

func isExecutableFile(path string) bool {
	if strings.TrimSpace(path) == "" {
		return false
	}
	info, err := os.Stat(path)
	return err == nil && !info.IsDir()
}

// MigrateLegacyRuntimeNames removes predecessor implementation branding from
// ~/.atcode without deleting runtime data. Directory and executable renames are
// atomic when they stay on the same filesystem.
func MigrateLegacyRuntimeNames() error {
	home, err := os.UserHomeDir()
	if err != nil {
		return err
	}
	root := filepath.Join(home, ".atcode")
	engineRoot := filepath.Join(root, "engine")
	legacyRoot := filepath.Join(root, "prism-llama.cpp")

	movedRoot := engineRoot
	if isDirectory(legacyRoot) {
		if isDirectory(engineRoot) {
			movedRoot = availableCompatibilityPath(filepath.Join(engineRoot, "compat-runtime"))
		}
		if err := os.MkdirAll(filepath.Dir(movedRoot), 0o755); err != nil {
			return err
		}
		if err := os.Rename(legacyRoot, movedRoot); err != nil {
			return err
		}
	}

	var migrationErrors []error
	for _, runtimeRoot := range []string{engineRoot, movedRoot} {
		for _, relativeDir := range []string{"", "bin", filepath.Join("build", "bin")} {
			dir := filepath.Join(runtimeRoot, relativeDir)
			for _, suffix := range []string{"", ".exe"} {
				oldPath := filepath.Join(dir, "llama-server"+suffix)
				newPath := filepath.Join(dir, AtcodeServerBaseName+suffix)
				if err := renameIfTargetMissing(oldPath, newPath); err != nil {
					migrationErrors = append(migrationErrors, err)
				}
			}
		}
	}
	for _, suffix := range []string{"", ".exe"} {
		if err := renameIfTargetMissing(
			filepath.Join(root, "bin", "llama-server"+suffix),
			filepath.Join(root, "bin", AtcodeServerBaseName+suffix),
		); err != nil {
			migrationErrors = append(migrationErrors, err)
		}
	}
	return errors.Join(migrationErrors...)
}

func renameIfTargetMissing(oldPath, newPath string) error {
	if !pathExists(oldPath) {
		return nil
	}
	if pathExists(newPath) {
		newPath = availableCompatibilityPath(newPath)
	}
	return os.Rename(oldPath, newPath)
}

func availableCompatibilityPath(path string) string {
	extension := filepath.Ext(path)
	base := strings.TrimSuffix(path, extension)
	for index := 1; ; index++ {
		candidate := base + "-compat"
		if index > 1 {
			candidate += "-" + strconv.Itoa(index)
		}
		candidate += extension
		if !pathExists(candidate) {
			return candidate
		}
	}
}

func pathExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

func isDirectory(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.IsDir()
}

// atcodeEnv reads the canonical product environment first and retains the
// predecessor prefix only as a compatibility fallback.
func atcodeEnv(suffix string) string {
	if value, exists := os.LookupEnv("ATCODE_" + suffix); exists {
		return value
	}
	return os.Getenv("SONUT_" + suffix)
}
