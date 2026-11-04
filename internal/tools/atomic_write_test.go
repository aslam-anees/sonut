package tools

import (
	"os"
	"path/filepath"
	"testing"
)

func TestAtomicWriteFileReplacesWithoutLeavingTempFiles(t *testing.T) {
	root := t.TempDir()
	target := filepath.Join(root, "nested", "file.txt")
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(target, []byte("old\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := atomicWriteFile(target, []byte("new\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(target)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "new\n" {
		t.Fatalf("content = %q, want new content", got)
	}
	entries, err := os.ReadDir(filepath.Dir(target))
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if len(entry.Name()) >= len(".sonut-write-") && entry.Name()[:len(".sonut-write-")] == ".sonut-write-" {
			t.Fatalf("temporary write file leaked: %s", entry.Name())
		}
	}
}
