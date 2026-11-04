package know

import (
	"context"
	"math"
	"sort"
	"strings"
	"unicode"
)

// Retriever ranks chunks against a query. sonut ships BM25Retriever as the
// pure-Go, no-external-service default; WeKnora's vector/hybrid retrievers
// (opensearch, qdrant, milvus, ...) all require a running external service
// and were deliberately not vendored — see WIRING.md. A caller that wants
// embedding-based retrieval can implement this interface against their own
// embedder/vector store and pass it to NewService.
type Retriever interface {
	// Index adds or replaces the chunks for a document.
	Index(ctx context.Context, docID string, chunks []Chunk) error
	// Remove drops all chunks belonging to a document.
	Remove(ctx context.Context, docID string) error
	// Search returns up to topK chunks most relevant to query, best first.
	Search(ctx context.Context, query string, topK int) ([]ScoredChunk, error)
}

// ScoredChunk pairs a Chunk with its retrieval score.
type ScoredChunk struct {
	Chunk Chunk
	Score float64
}

// BM25Retriever is an in-memory, keyword-based (Okapi BM25) retriever. It
// has no external dependencies and no persistence — callers that need
// durability should re-ingest on startup (know.Service does this via its
// Store) or wrap it.
type BM25Retriever struct {
	k1, b float64

	docs   map[string][]bm25Doc // docID -> chunks (in Seq order)
	avgLen float64
	nDocs  int
	df     map[string]int // term -> number of chunks containing it
}

type bm25Doc struct {
	chunk Chunk
	terms map[string]int
	len   int
}

// NewBM25Retriever constructs an empty in-memory BM25 index with standard
// parameters (k1=1.2, b=0.75).
func NewBM25Retriever() *BM25Retriever {
	return &BM25Retriever{
		k1:   1.2,
		b:    0.75,
		docs: make(map[string][]bm25Doc),
		df:   make(map[string]int),
	}
}

// ChunkCount reports how many chunks are currently indexed.
func (r *BM25Retriever) ChunkCount() int { return r.nDocs }

func tokenize(s string) []string {
	var toks []string
	var cur strings.Builder
	flush := func() {
		if cur.Len() > 0 {
			toks = append(toks, strings.ToLower(cur.String()))
			cur.Reset()
		}
	}
	for _, r := range s {
		if unicode.IsLetter(r) || unicode.IsNumber(r) {
			cur.WriteRune(r)
		} else {
			flush()
		}
	}
	flush()
	return toks
}

func (r *BM25Retriever) Index(_ context.Context, docID string, chunks []Chunk) error {
	r.removeLocked(docID)
	entries := make([]bm25Doc, 0, len(chunks))
	for _, c := range chunks {
		terms := make(map[string]int)
		for _, t := range tokenize(c.Content) {
			terms[t]++
		}
		entries = append(entries, bm25Doc{chunk: c, terms: terms, len: len(tokenize(c.Content))})
		for t := range terms {
			r.df[t]++
		}
		r.nDocs++
	}
	r.docs[docID] = entries
	r.recomputeAvgLen()
	return nil
}

func (r *BM25Retriever) Remove(_ context.Context, docID string) error {
	r.removeLocked(docID)
	r.recomputeAvgLen()
	return nil
}

func (r *BM25Retriever) removeLocked(docID string) {
	old, ok := r.docs[docID]
	if !ok {
		return
	}
	for _, d := range old {
		for t := range d.terms {
			r.df[t]--
			if r.df[t] <= 0 {
				delete(r.df, t)
			}
		}
		r.nDocs--
	}
	delete(r.docs, docID)
}

func (r *BM25Retriever) recomputeAvgLen() {
	total, n := 0, 0
	for _, entries := range r.docs {
		for _, d := range entries {
			total += d.len
			n++
		}
	}
	if n == 0 {
		r.avgLen = 0
		return
	}
	r.avgLen = float64(total) / float64(n)
}

func (r *BM25Retriever) Search(_ context.Context, query string, topK int) ([]ScoredChunk, error) {
	qTerms := tokenize(query)
	if len(qTerms) == 0 || r.nDocs == 0 {
		return nil, nil
	}
	var results []ScoredChunk
	for _, entries := range r.docs {
		for _, d := range entries {
			score := r.score(qTerms, d)
			if score > 0 {
				results = append(results, ScoredChunk{Chunk: d.chunk, Score: score})
			}
		}
	}
	sort.Slice(results, func(i, j int) bool { return results[i].Score > results[j].Score })
	if topK > 0 && len(results) > topK {
		results = results[:topK]
	}
	return results, nil
}

func (r *BM25Retriever) score(qTerms []string, d bm25Doc) float64 {
	var score float64
	for _, t := range qTerms {
		f := float64(d.terms[t])
		if f == 0 {
			continue
		}
		df := r.df[t]
		if df == 0 {
			continue
		}
		idf := math.Log(1 + (float64(r.nDocs)-float64(df)+0.5)/(float64(df)+0.5))
		denom := f + r.k1*(1-r.b+r.b*float64(d.len)/r.avgLenOrOne())
		score += idf * (f * (r.k1 + 1) / denom)
	}
	return score
}

func (r *BM25Retriever) avgLenOrOne() float64 {
	if r.avgLen == 0 {
		return 1
	}
	return r.avgLen
}
