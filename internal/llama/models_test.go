package llama

import (
	"os"
	"path/filepath"
	"testing"
)

func TestExpandPathPreservesPathsWithoutHomePrefix(t *testing.T) {
	absolute := filepath.Join(string(filepath.Separator), "tmp", "atcode-models")
	if got := ExpandPath(absolute); got != absolute {
		t.Fatalf("ExpandPath(%q) = %q, want unchanged absolute path", absolute, got)
	}

	relative := filepath.Join("models", "local")
	if got := ExpandPath(relative); got != relative {
		t.Fatalf("ExpandPath(%q) = %q, want unchanged relative path", relative, got)
	}
}

func TestExpandPathExpandsEnvironmentAndHome(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("SONUT_TEST_MODEL_ROOT", filepath.Join("tmp", "models"))

	if got, want := ExpandPath("~/atcode-models"), filepath.Join(home, "atcode-models"); got != want {
		t.Fatalf("home expansion = %q, want %q", got, want)
	}
	if got, want := ExpandPath("$SONUT_TEST_MODEL_ROOT/current"), filepath.Join("tmp", "models", "current"); got != want {
		t.Fatalf("environment expansion = %q, want %q", got, want)
	}
}

func TestGetModelPathUsesCurrentDirectoryScan(t *testing.T) {
	modelDir := t.TempDir()
	t.Setenv("SONUT_MODEL_DIR", modelDir)
	modelPath := filepath.Join(modelDir, "atcode-v1.gguf")
	if err := os.WriteFile(modelPath, []byte("model"), 0o600); err != nil {
		t.Fatal(err)
	}

	got, err := GetModelPath()
	if err != nil {
		t.Fatal(err)
	}
	if got != modelPath {
		t.Fatalf("GetModelPath() = %q, want %q", got, modelPath)
	}
}

func TestBonsaiDSparkMainIsNotMisclassifiedAsDraft(t *testing.T) {
	modelDir := t.TempDir()
	t.Setenv("SONUT_MODEL_DIR", modelDir)
	mainPath := filepath.Join(modelDir, "Bonsai-27B-dspark-Q4_1.gguf")
	draftPath := filepath.Join(modelDir, "dspark.gguf")
	for path, content := range map[string]string{
		mainPath:  "main",
		draftPath: "draft",
		filepath.Join(modelDir, "qwopus-old.gguf"): "old",
	} {
		if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	gotMain, _, present := CheckModelsPresent()
	wantMain := filepath.Join(modelDir, "atcode-model.gguf")
	if !present || gotMain != wantMain {
		t.Fatalf("main model = %q, present=%v; want %q", gotMain, present, wantMain)
	}
	wantDraft := filepath.Join(modelDir, "atcode-accelerator.gguf")
	if gotDraft := FindDraftModel(); gotDraft != wantDraft {
		t.Fatalf("draft model = %q, want %q", gotDraft, wantDraft)
	}
}
