package llama

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// GetModelDir returns the directory where models are stored.
// SONUT_MODEL_DIR wins when set. Otherwise ~/.atcode/model is canonical, with
// the older ~/.atcode/models directory retained only for existing installs.
func GetModelDir() string {
	if dir := strings.TrimSpace(atcodeEnv("MODEL_DIR")); dir != "" {
		return ExpandPath(dir)
	}

	home, err := os.UserHomeDir()
	if err != nil {
		return "./model"
	}

	singular := filepath.Join(home, ".atcode", "model")
	if hasGGUF(singular) {
		return singular
	}
	legacy := filepath.Join(home, ".atcode", "models")
	if hasGGUF(legacy) {
		return legacy
	}
	return singular
}

func hasGGUF(dir string) bool {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return false
	}
	for _, entry := range entries {
		if !entry.IsDir() && strings.HasSuffix(strings.ToLower(entry.Name()), ".gguf") {
			return true
		}
	}
	return false
}

// GetModelPath returns the selected primary model using the same directory scan
// as startup. Keeping one discovery path avoids stale hard-coded model catalogs
// and recognizes renamed/current GGUFs such as atcode-v1.gguf.
func GetModelPath() (string, error) {
	mainPath, _, present := CheckModelsPresent()
	if !present {
		return "", fmt.Errorf("no model found in %s", GetModelDir())
	}
	return mainPath, nil
}

// ExpandPath expands environment variables and a leading home shorthand.
// Absolute and relative paths without "~" are preserved; the former
// implementation unconditionally removed their first character and joined the
// remainder under the user's home directory.
func ExpandPath(path string) string {
	path = os.ExpandEnv(path)
	if path == "~" {
		if home, err := os.UserHomeDir(); err == nil {
			return home
		}
		return path
	}
	if strings.HasPrefix(path, "~/") || strings.HasPrefix(path, `~\`) {
		if home, err := os.UserHomeDir(); err == nil {
			return filepath.Join(home, path[2:])
		}
	}
	return path
}
