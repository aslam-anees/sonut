package knowledge

import (
	"context"
	"fmt"

	"github.com/aistatine/sonut/internal/know"
)

// SearchBackend provides chunk-level search over the knowledge base,
// layered on top of the note graph. It is an additive capability: existing
// callers (LoadNotes, BuildGraph, AddNote, ...) are unaffected — a
// SearchBackend must be built explicitly via NewSearchBackend and is not
// used anywhere else in this package by default.
//
// It is backed by internal/know, sonut's vendored-and-trimmed copy of
// WeKnora's chunking/retrieval engine (see internal/know/WIRING.md). Notes
// are chunked with know's adaptive splitter and indexed with its in-memory
// BM25 retriever, so multi-paragraph notes can be searched at
// sub-note granularity instead of returning the whole file.
type SearchBackend struct {
	svc *know.Service
}

// NewSearchBackend builds a SearchBackend by ingesting every note currently
// on disk (via LoadNotes) into a fresh in-process know.Service. Call
// Refresh to pick up changes made after construction.
func NewSearchBackend() (*SearchBackend, error) {
	b := &SearchBackend{svc: know.NewService()}
	if err := b.Refresh(); err != nil {
		return nil, err
	}
	return b, nil
}

// Refresh re-loads notes from disk and re-ingests them, replacing any
// previously indexed content for the same note (know.Service.Ingest is
// keyed by note slug, so re-ingestion overwrites rather than duplicates).
func (b *SearchBackend) Refresh() error {
	notes, err := LoadNotes()
	if err != nil {
		return fmt.Errorf("knowledge: load notes: %w", err)
	}
	ctx := context.Background()
	for _, n := range notes {
		doc := know.Document{
			ID:      n.Name,
			Title:   n.Title,
			Content: n.Content,
			Source:  n.Path,
		}
		if err := b.svc.Ingest(ctx, doc); err != nil {
			return fmt.Errorf("knowledge: ingest note %s: %w", n.Name, err)
		}
	}
	return nil
}

// SearchResult is one chunk-level match, identified back to its note slug.
type SearchResult struct {
	NoteName string
	Title    string
	Snippet  string
	Score    float64
}

// Search returns up to topK chunks most relevant to query across all
// ingested notes, best first.
func (b *SearchBackend) Search(query string, topK int) ([]SearchResult, error) {
	results, err := b.svc.Retrieve(context.Background(), query, topK)
	if err != nil {
		return nil, fmt.Errorf("knowledge: search: %w", err)
	}
	out := make([]SearchResult, 0, len(results))
	for _, r := range results {
		out = append(out, SearchResult{
			NoteName: r.Doc.ID,
			Title:    r.Doc.Title,
			Snippet:  r.Chunk.Content,
			Score:    r.Score,
		})
	}
	return out, nil
}

// ChunkCount reports how many chunks are indexed for search across all
// ingested notes.
func (b *SearchBackend) ChunkCount() (int, error) {
	n, ok := b.svc.ChunkCount()
	if !ok {
		return 0, fmt.Errorf("knowledge: retriever does not report chunk counts")
	}
	return n, nil
}
