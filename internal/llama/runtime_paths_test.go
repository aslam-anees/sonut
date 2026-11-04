package llama

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestMigrateLegacyRuntimeNames(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	suffix := ""
	if runtime.GOOS == "windows" {
		suffix = ".exe"
	}
	legacyServer := filepath.Join(home, ".atcode", "prism-llama.cpp", "build", "bin", "llama-server"+suffix)
	if err := os.MkdirAll(filepath.Dir(legacyServer), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(legacyServer, []byte("engine"), 0o755); err != nil {
		t.Fatal(err)
	}

	if err := MigrateLegacyRuntimeNames(); err != nil {
		t.Fatalf("MigrateLegacyRuntimeNames() error = %v", err)
	}

	want := filepath.Join(home, ".atcode", "engine", "build", "bin", AtcodeServerBaseName+suffix)
	if _, err := os.Stat(want); err != nil {
		t.Fatalf("canonical engine missing at %s: %v", want, err)
	}
	if _, err := os.Stat(filepath.Join(home, ".atcode", "prism-llama.cpp")); !os.IsNotExist(err) {
		t.Fatalf("legacy branded directory still exists: %v", err)
	}
}

func TestCanonicalServerCandidatesUseOnlyAtcodeNames(t *testing.T) {
	for _, goos := range []string{"darwin", "linux", "windows"} {
		for _, candidate := range canonicalServerCandidates("/home/test", goos) {
			if filepath.Base(candidate) != serverExecutableName(goos) {
				t.Fatalf("%s candidate has non-canonical basename: %s", goos, candidate)
			}
		}
	}
}

func TestMigrateLegacyModelNames(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	modelDir := filepath.Join(home, ".atcode", "model")
	if err := os.MkdirAll(modelDir, 0o755); err != nil {
		t.Fatal(err)
	}
	legacy := filepath.Join(modelDir, "Bonsai-27B-dspark-Q4.gguf")
	if err := os.WriteFile(legacy, []byte("model"), 0o600); err != nil {
		t.Fatal(err)
	}

	if err := MigrateLegacyModelNames(); err != nil {
		t.Fatalf("MigrateLegacyModelNames() error = %v", err)
	}
	if _, err := os.Stat(filepath.Join(modelDir, "atcode-model.gguf")); err != nil {
		t.Fatalf("canonical model missing: %v", err)
	}
	if _, err := os.Stat(legacy); !os.IsNotExist(err) {
		t.Fatalf("legacy model name still exists: %v", err)
	}
}
