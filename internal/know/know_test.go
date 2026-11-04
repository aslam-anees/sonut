package know

import (
	"context"
	"strings"
	"testing"
)

func TestIngestRetrieveRoundTrip(t *testing.T) {
	ctx := context.Background()
	svc := NewService()

	docs := []Document{
		{
			ID:    "go-concurrency",
			Title: "Go Concurrency",
			Content: `# Goroutines

Goroutines are lightweight threads managed by the Go runtime. You start one
with the go keyword.

# Channels

Channels let goroutines communicate and synchronize. Use them to pass data
between concurrent goroutines safely.
`,
		},
		{
			ID:    "python-basics",
			Title: "Python Basics",
			Content: `# Lists

Python lists are ordered, mutable collections. They support append, pop,
and slicing.

# Dictionaries

Dictionaries map keys to values and are implemented as hash tables in
CPython.
`,
		},
	}

	for _, d := range docs {
		if err := svc.Ingest(ctx, d); err != nil {
			t.Fatalf("Ingest(%s): %v", d.ID, err)
		}
	}

	all, err := svc.Documents(ctx)
	if err != nil {
		t.Fatalf("Documents: %v", err)
	}
	if len(all) != 2 {
		t.Fatalf("expected 2 documents, got %d", len(all))
	}

	results, err := svc.Retrieve(ctx, "goroutines channels concurrency", 5)
	if err != nil {
		t.Fatalf("Retrieve: %v", err)
	}
	if len(results) == 0 {
		t.Fatalf("expected at least one result")
	}
	if results[0].Doc.ID != "go-concurrency" {
		t.Fatalf("expected top result from go-concurrency, got %s (score %f)", results[0].Doc.ID, results[0].Score)
	}
	if !strings.Contains(strings.ToLower(results[0].Chunk.Content), "goroutine") {
		t.Errorf("expected top chunk to mention goroutines, got: %q", results[0].Chunk.Content)
	}

	results, err = svc.Retrieve(ctx, "python dictionaries hash tables", 5)
	if err != nil {
		t.Fatalf("Retrieve: %v", err)
	}
	if len(results) == 0 || results[0].Doc.ID != "python-basics" {
		t.Fatalf("expected top result from python-basics, got %+v", results)
	}

	// Deleting a document should remove it from both the store and the index.
	if err := svc.Delete(ctx, "python-basics"); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	results, err = svc.Retrieve(ctx, "python dictionaries hash tables", 5)
	if err != nil {
		t.Fatalf("Retrieve after delete: %v", err)
	}
	for _, r := range results {
		if r.Doc.ID == "python-basics" {
			t.Errorf("deleted document still returned: %+v", r)
		}
	}

	all, err = svc.Documents(ctx)
	if err != nil {
		t.Fatalf("Documents after delete: %v", err)
	}
	if len(all) != 1 {
		t.Fatalf("expected 1 document after delete, got %d", len(all))
	}
}

func TestIngestRequiresID(t *testing.T) {
	svc := NewService()
	if err := svc.Ingest(context.Background(), Document{Content: "no id"}); err == nil {
		t.Fatal("expected error for missing document ID")
	}
}
