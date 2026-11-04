// Package know is sonut's local, in-process knowledge-base engine: ingest
// plain-text/markdown documents, chunk them, and retrieve relevant chunks
// for a query. It is a scoped, dependency-free vendoring of the core
// document-domain logic from WeKnora (github.com/Tencent/WeKnora) — see
// WIRING.md in this directory for exactly what was ported and what was
// deliberately left out (postgres, vector DBs, the docreader gRPC service,
// the web/mcp frontends, etc).
package know

import (
	"time"

	"github.com/aistatine/sonut/internal/know/chunker"
)

// Document is a single ingested source document, identified by ID (caller
// supplied — e.g. a file path or note slug).
type Document struct {
	ID       string
	Title    string
	Content  string
	Source   string // e.g. file path or URL; informational only
	AddedAt  time.Time
	Metadata map[string]string
}

// Chunk is a slice of a Document produced by the chunker, plus the
// bookkeeping needed to map it back to its parent document for retrieval
// results.
type Chunk struct {
	DocID         string
	Seq           int
	Content       string
	Start         int // rune offset into Document.Content
	End           int
	ContextHeader string // heading breadcrumb, when the chunker found one
}

// fromSplitChunk adapts a chunker.Chunk (produced by chunker.Split) into a
// know.Chunk tied to a document.
func fromSplitChunk(docID string, c chunker.Chunk) Chunk {
	return Chunk{
		DocID:         docID,
		Seq:           c.Seq,
		Content:       c.Content,
		Start:         c.Start,
		End:           c.End,
		ContextHeader: c.ContextHeader,
	}
}

// Result is one hit from a retrieval query: a chunk plus its relevance
// score (retriever-defined scale; higher is more relevant) and the document
// it belongs to.
type Result struct {
	Chunk Chunk
	Doc   Document
	Score float64
}
