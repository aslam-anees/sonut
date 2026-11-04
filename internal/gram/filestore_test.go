package gram

import (
	"path/filepath"
	"testing"
)

// TestFileStore_RoundTripAcrossReopen verifies that memories saved to a
// FileStore are still recallable after the store is closed and a new
// FileStore is opened against the same root directory — i.e. that gram's
// default backend actually persists across process restarts, not just
// within a single in-memory session.
func TestFileStore_RoundTripAcrossReopen(t *testing.T) {
	root := filepath.Join(t.TempDir(), "memory")

	store, err := NewFileStore(root)
	if err != nil {
		t.Fatalf("NewFileStore: %v", err)
	}

	saved, err := store.Save(SaveInput{
		Project: "sonut",
		Title:   "prefers tabs",
		Body:    "The user prefers tabs over spaces in Go code.",
		Tags:    []string{"style", "go"},
	})
	if err != nil {
		t.Fatalf("Save: %v", err)
	}
	if saved.ID == "" {
		t.Fatalf("Save returned empty ID")
	}

	if _, err := store.Save(SaveInput{
		Project: "other-project",
		Title:   "unrelated fact",
		Body:    "This belongs to a different project.",
	}); err != nil {
		t.Fatalf("Save (second memory): %v", err)
	}

	if err := store.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	// Re-open a fresh store against the same root directory, simulating a
	// new sonut process/session starting up later.
	reopened, err := NewFileStore(root)
	if err != nil {
		t.Fatalf("NewFileStore (reopen): %v", err)
	}
	defer reopened.Close()

	got, ok, err := reopened.Get(saved.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if !ok {
		t.Fatalf("Get: memory %q not found after reopen", saved.ID)
	}
	if got.Title != "prefers tabs" || got.Body != saved.Body {
		t.Fatalf("Get returned unexpected memory: %+v", got)
	}
	if got.CreatedAt.IsZero() || got.UpdatedAt.IsZero() {
		t.Fatalf("Get returned memory with zero timestamps: %+v", got)
	}

	results, err := reopened.Recall(Query{Text: "tabs", Project: "sonut"})
	if err != nil {
		t.Fatalf("Recall: %v", err)
	}
	if len(results) != 1 || results[0].ID != saved.ID {
		t.Fatalf("Recall(%q) = %+v, want exactly the saved memory", "tabs", results)
	}

	// Recall scoped to another project must not leak this memory back.
	other, err := reopened.Recall(Query{Text: "tabs", Project: "other-project"})
	if err != nil {
		t.Fatalf("Recall (scoped to other project): %v", err)
	}
	if len(other) != 0 {
		t.Fatalf("Recall leaked cross-project results: %+v", other)
	}

	// Update in place: saving again with the same ID must not create a
	// duplicate and must preserve CreatedAt.
	updated, err := reopened.Save(SaveInput{
		ID:      saved.ID,
		Project: "sonut",
		Title:   "prefers tabs (confirmed twice)",
		Body:    saved.Body,
		Tags:    saved.Tags,
	})
	if err != nil {
		t.Fatalf("Save (update): %v", err)
	}
	if updated.CreatedAt != got.CreatedAt {
		t.Fatalf("Save (update) changed CreatedAt: got %v, want %v", updated.CreatedAt, got.CreatedAt)
	}

	all, err := reopened.Recall(Query{})
	if err != nil {
		t.Fatalf("Recall (all): %v", err)
	}
	if len(all) != 2 {
		t.Fatalf("Recall(all) = %d memories, want 2", len(all))
	}

	if err := reopened.Delete(saved.ID); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if _, ok, err = reopened.Get(saved.ID); err != nil || ok {
		t.Fatalf("Get after Delete: ok=%v err=%v, want not found", ok, err)
	}

	// Deleting an already-deleted (or never-existing) ID must not error.
	if err := reopened.Delete(saved.ID); err != nil {
		t.Fatalf("Delete (already deleted): %v", err)
	}

	if err := reopened.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if _, _, err := reopened.Get(saved.ID); err == nil {
		t.Fatalf("Get after Close: want error, got nil")
	}
}
