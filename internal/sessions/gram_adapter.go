package sessions

import (
	"fmt"
	"strings"

	"github.com/aistatine/sonut/internal/gram"
)

// MemoryAdapter connects sonut's session lifecycle to a gram.Store so
// sessions can persist notes and recall prior ones scoped by project (cwd).
//
// MemoryAdapter is entirely opt-in: nothing in this package constructs one
// automatically, and no existing Store/session code path calls it. Callers
// (e.g. cmd/ wiring) that want persistent memory construct a gram.Store
// (typically gram.NewFileStore("")) and wrap it with NewMemoryAdapter; every
// other sessions code path is unaffected whether or not this type is ever
// used.
type MemoryAdapter struct {
	store gram.Store
}

// NewMemoryAdapter wraps store for use by session lifecycle hooks. store
// must not be nil.
func NewMemoryAdapter(store gram.Store) (*MemoryAdapter, error) {
	if store == nil {
		return nil, fmt.Errorf("sessions: NewMemoryAdapter: store must not be nil")
	}
	return &MemoryAdapter{store: store}, nil
}

// memoryProject derives the gram project scope for a session from its
// working directory, mirroring how engram scopes observations to a
// project. An empty Cwd yields the global ("") project scope.
func memoryProject(meta Metadata) string {
	return strings.TrimSpace(meta.Cwd)
}

// SaveNote persists a note associated with the given session's project
// scope (derived from Metadata.Cwd). It returns the saved gram.Memory.
func (a *MemoryAdapter) SaveNote(meta Metadata, title, body string, tags ...string) (gram.Memory, error) {
	if a == nil || a.store == nil {
		return gram.Memory{}, fmt.Errorf("sessions: MemoryAdapter is not configured")
	}
	return a.store.Save(gram.SaveInput{
		Project: memoryProject(meta),
		Title:   title,
		Body:    body,
		Tags:    tags,
	})
}

// Recall returns memories scoped to the given session's project whose
// title, body, or tags match text. An empty text matches every memory in
// scope. limit caps the number of results (0 = no limit).
func (a *MemoryAdapter) Recall(meta Metadata, text string, limit int) ([]gram.Memory, error) {
	if a == nil || a.store == nil {
		return nil, fmt.Errorf("sessions: MemoryAdapter is not configured")
	}
	return a.store.Recall(gram.Query{
		Text:    text,
		Project: memoryProject(meta),
		Limit:   limit,
	})
}

// Close releases the underlying gram.Store's resources.
func (a *MemoryAdapter) Close() error {
	if a == nil || a.store == nil {
		return nil
	}
	return a.store.Close()
}
