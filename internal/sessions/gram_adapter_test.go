package sessions

import (
	"path/filepath"
	"testing"

	"github.com/aistatine/sonut/internal/gram"
)

func TestMemoryAdapter_SaveAndRecallScopedByProject(t *testing.T) {
	root := filepath.Join(t.TempDir(), "memory")
	store, err := gram.NewFileStore(root)
	if err != nil {
		t.Fatalf("NewFileStore: %v", err)
	}
	defer store.Close()

	adapter, err := NewMemoryAdapter(store)
	if err != nil {
		t.Fatalf("NewMemoryAdapter: %v", err)
	}

	meta := Metadata{SessionID: "s1", Cwd: "/repo/a"}
	other := Metadata{SessionID: "s2", Cwd: "/repo/b"}

	if _, err := adapter.SaveNote(meta, "build command", "use `go build ./...`", "build"); err != nil {
		t.Fatalf("SaveNote: %v", err)
	}
	if _, err := adapter.SaveNote(other, "unrelated", "belongs to repo b", "misc"); err != nil {
		t.Fatalf("SaveNote (other project): %v", err)
	}

	results, err := adapter.Recall(meta, "build", 0)
	if err != nil {
		t.Fatalf("Recall: %v", err)
	}
	if len(results) != 1 || results[0].Title != "build command" {
		t.Fatalf("Recall = %+v, want the single note scoped to repo/a", results)
	}

	crossProject, err := adapter.Recall(other, "build", 0)
	if err != nil {
		t.Fatalf("Recall (other project): %v", err)
	}
	if len(crossProject) != 0 {
		t.Fatalf("Recall leaked across project scope: %+v", crossProject)
	}
}

func TestMemoryAdapter_NilStoreErrorsInsteadOfPanicking(t *testing.T) {
	if _, err := NewMemoryAdapter(nil); err == nil {
		t.Fatalf("NewMemoryAdapter(nil): want error, got nil")
	}

	var a *MemoryAdapter
	if _, err := a.SaveNote(Metadata{}, "t", "b"); err == nil {
		t.Fatalf("SaveNote on nil adapter: want error, got nil")
	}
	if _, err := a.Recall(Metadata{}, "", 0); err == nil {
		t.Fatalf("Recall on nil adapter: want error, got nil")
	}
	if err := a.Close(); err != nil {
		t.Fatalf("Close on nil adapter: want nil, got %v", err)
	}
}
