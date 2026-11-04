package know

import (
	"context"
	"fmt"
	"time"

	"github.com/aistatine/sonut/internal/know/chunker"
)

// Service is the top-level knowledge-base API: ingest documents, they get
// chunked and indexed, then Retrieve finds the most relevant chunks for a
// query. It composes a Store (document persistence) and a Retriever
// (chunk search); both are pluggable so callers can swap in durable or
// embedding-based implementations without changing this type.
type Service struct {
	store     Store
	retriever Retriever
	chunkCfg  chunker.SplitterConfig
}

// Option configures a Service at construction time.
type Option func(*Service)

// WithStore overrides the default in-memory Store.
func WithStore(s Store) Option { return func(svc *Service) { svc.store = s } }

// WithRetriever overrides the default BM25 Retriever.
func WithRetriever(r Retriever) Option { return func(svc *Service) { svc.retriever = r } }

// WithChunkConfig overrides the default chunker.SplitterConfig used by
// Ingest.
func WithChunkConfig(cfg chunker.SplitterConfig) Option {
	return func(svc *Service) { svc.chunkCfg = cfg }
}

// NewService builds a knowledge-base Service. With no options it is fully
// in-process: MemoryStore + BM25Retriever, no external services required.
func NewService(opts ...Option) *Service {
	svc := &Service{
		store:     NewMemoryStore(),
		retriever: NewBM25Retriever(),
		chunkCfg:  chunker.SplitterConfig{},
	}
	for _, opt := range opts {
		opt(svc)
	}
	return svc
}

// Ingest chunks doc.Content, indexes the chunks, and stores the document.
// Re-ingesting the same doc.ID replaces its previous chunks.
func (s *Service) Ingest(ctx context.Context, doc Document) error {
	if doc.ID == "" {
		return fmt.Errorf("know: document ID is required")
	}
	if doc.AddedAt.IsZero() {
		doc.AddedAt = time.Now()
	}
	split := chunker.Split(doc.Content, s.chunkCfg)
	chunks := make([]Chunk, 0, len(split))
	for _, c := range split {
		chunks = append(chunks, fromSplitChunk(doc.ID, c))
	}
	if err := s.retriever.Index(ctx, doc.ID, chunks); err != nil {
		return fmt.Errorf("know: index %s: %w", doc.ID, err)
	}
	if err := s.store.Put(ctx, doc); err != nil {
		return fmt.Errorf("know: store %s: %w", doc.ID, err)
	}
	return nil
}

// Delete removes a document and its chunks.
func (s *Service) Delete(ctx context.Context, id string) error {
	if err := s.retriever.Remove(ctx, id); err != nil {
		return err
	}
	return s.store.Delete(ctx, id)
}

// Retrieve returns up to topK chunks most relevant to query, best first,
// each paired with its parent Document.
func (s *Service) Retrieve(ctx context.Context, query string, topK int) ([]Result, error) {
	scored, err := s.retriever.Search(ctx, query, topK)
	if err != nil {
		return nil, fmt.Errorf("know: search: %w", err)
	}
	results := make([]Result, 0, len(scored))
	for _, sc := range scored {
		doc, ok, err := s.store.Get(ctx, sc.Chunk.DocID)
		if err != nil {
			return nil, fmt.Errorf("know: load doc %s: %w", sc.Chunk.DocID, err)
		}
		if !ok {
			continue // chunk indexed but document missing/deleted; skip
		}
		results = append(results, Result{Chunk: sc.Chunk, Doc: doc, Score: sc.Score})
	}
	return results, nil
}

// Documents returns every document currently in the store.
func (s *Service) Documents(ctx context.Context) ([]Document, error) {
	return s.store.List(ctx)
}

// ChunkCount reports the number of chunks currently indexed, when the
// retriever supports it (the built-in BM25 retriever does).
func (s *Service) ChunkCount() (int, bool) {
	if c, ok := s.retriever.(interface{ ChunkCount() int }); ok {
		return c.ChunkCount(), true
	}
	return 0, false
}
