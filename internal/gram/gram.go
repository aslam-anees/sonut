// Package gram provides persistent, long-term memory for sonut: a small
// store/recall API that lets sessions save facts or notes and retrieve them
// again later, including across process restarts.
//
// It is adapted from the storage/recall concepts in the "engram" project
// (see internal/gram/WIRING.md for exactly what was ported and what was
// skipped). Unlike engram's SQLite+FTS5 backend, gram's default
// implementation is a dependency-free, file-backed store built on the Go
// standard library only, so it can be vendored into sonut without pulling in
// a database driver.
//
// gram is off by default: nothing in sonut constructs a Store unless a
// caller explicitly opts in (see internal/sessions/gram_adapter.go).
package gram

import "time"

// Memory is a single persisted fact or note.
type Memory struct {
	// ID uniquely identifies this memory within a Store.
	ID string `json:"id"`
	// Project scopes the memory to a project/cwd, mirroring engram's
	// project-scoped observations. Empty means "global".
	Project string `json:"project,omitempty"`
	// Title is a short human-readable summary.
	Title string `json:"title"`
	// Body is the full content of the memory.
	Body string `json:"body"`
	// Tags are free-form labels used to narrow recall queries.
	Tags []string `json:"tags,omitempty"`
	// CreatedAt is when the memory was first saved.
	CreatedAt time.Time `json:"createdAt"`
	// UpdatedAt is when the memory was last modified.
	UpdatedAt time.Time `json:"updatedAt"`
}

// SaveInput describes a memory to persist.
type SaveInput struct {
	// ID optionally names an existing memory to update in place. If empty,
	// a new ID is generated and a new memory is created.
	ID      string
	Project string
	Title   string
	Body    string
	Tags    []string
}

// Query narrows a recall request.
type Query struct {
	// Text is matched (case-insensitively) against title, body, and tags.
	// Empty means "match everything".
	Text string
	// Project restricts results to a single project scope. Empty means "all
	// projects".
	Project string
	// Limit caps the number of results returned. Zero or negative means
	// "no limit".
	Limit int
}

// Store is the interface sonut code depends on for long-term memory. The
// default implementation, FileStore, is local and file-backed; other
// implementations (e.g. talking to a remote/cloud service) can satisfy the
// same interface without sonut's callers needing to change.
type Store interface {
	// Save persists a new memory or updates an existing one (when
	// input.ID matches an existing memory) and returns the resulting
	// Memory.
	Save(input SaveInput) (Memory, error)
	// Recall returns memories matching query, most recently updated first.
	Recall(query Query) ([]Memory, error)
	// Get returns a single memory by ID.
	Get(id string) (Memory, bool, error)
	// Delete removes a memory by ID. Deleting a non-existent ID is not an
	// error.
	Delete(id string) error
	// Close releases any resources held by the store (e.g. open file
	// handles). It is safe to call Close more than once.
	Close() error
}
