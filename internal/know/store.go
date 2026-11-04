package know

import "context"

// Store persists Documents (metadata + full content). Chunking output
// itself lives only in the Retriever's index; Store is what lets a Service
// re-hydrate a document (e.g. to show the full source around a matched
// chunk, or to re-index after a process restart).
//
// WeKnora's document/knowledgebase repositories are all backed by
// postgres/mysql/sqlite through gorm. Rather than vendor that stack, know
// defines this narrow interface and ships MemoryStore as the default; a
// caller that wants durability can implement Store against sonut's own
// storage (e.g. a JSON file or sqlite table) without touching the rest of
// the package.
type Store interface {
	Put(ctx context.Context, doc Document) error
	Get(ctx context.Context, id string) (Document, bool, error)
	Delete(ctx context.Context, id string) error
	List(ctx context.Context) ([]Document, error)
}

// MemoryStore is an in-memory Store. Not persisted across process
// restarts.
type MemoryStore struct {
	docs map[string]Document
}

// NewMemoryStore constructs an empty in-memory document store.
func NewMemoryStore() *MemoryStore {
	return &MemoryStore{docs: make(map[string]Document)}
}

func (s *MemoryStore) Put(_ context.Context, doc Document) error {
	s.docs[doc.ID] = doc
	return nil
}

func (s *MemoryStore) Get(_ context.Context, id string) (Document, bool, error) {
	d, ok := s.docs[id]
	return d, ok, nil
}

func (s *MemoryStore) Delete(_ context.Context, id string) error {
	delete(s.docs, id)
	return nil
}

func (s *MemoryStore) List(_ context.Context) ([]Document, error) {
	out := make([]Document, 0, len(s.docs))
	for _, d := range s.docs {
		out = append(out, d)
	}
	return out, nil
}
