package knowledge

import (
	"os"
	"path/filepath"
	"testing"
)

func TestSearchBackendRoundTrip(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("SONUT_KNOWLEDGE_DIR", dir)

	if err := os.WriteFile(filepath.Join(dir, "rust-ownership.md"), []byte(
		"# Ownership\n\nRust's ownership system enforces memory safety without a garbage collector via borrowing and lifetimes.\n"),
		0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "sql-joins.md"), []byte(
		"# Joins\n\nSQL joins combine rows from two or more tables based on a related column.\n"),
		0o644); err != nil {
		t.Fatal(err)
	}

	backend, err := NewSearchBackend()
	if err != nil {
		t.Fatalf("NewSearchBackend: %v", err)
	}

	results, err := backend.Search("rust ownership borrowing lifetimes", 3)
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(results) == 0 || results[0].NoteName != "rust-ownership" {
		t.Fatalf("expected top match rust-ownership, got %+v", results)
	}

	// Add a new note after construction and confirm Refresh picks it up.
	if err := os.WriteFile(filepath.Join(dir, "go-channels.md"), []byte(
		"# Channels\n\nGo channels are typed conduits for goroutine communication.\n"),
		0o644); err != nil {
		t.Fatal(err)
	}
	if err := backend.Refresh(); err != nil {
		t.Fatalf("Refresh: %v", err)
	}
	results, err = backend.Search("goroutine channel communication", 3)
	if err != nil {
		t.Fatalf("Search after refresh: %v", err)
	}
	if len(results) == 0 || results[0].NoteName != "go-channels" {
		t.Fatalf("expected top match go-channels after refresh, got %+v", results)
	}
}
