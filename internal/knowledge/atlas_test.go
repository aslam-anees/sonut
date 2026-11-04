package knowledge

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestAtlasGraphTypedEdgesAndDeterministicAlgorithms(t *testing.T) {
	graph := BuildAtlasGraph([]Note{
		{Name: "alpha", Title: "Alpha", Content: "a", Links: []string{"beta"}, Tags: []string{"one"}, Kind: "note"},
		{Name: "beta", Title: "Beta", Content: "b", Tags: []string{"two"}, Aliases: []string{"B"}},
	})
	if len(graph.Ghosts()) != 0 {
		t.Fatal("unexpected ghost")
	}
	if len(graph.ForwardLinks("alpha")) != 2 { // beta + explicit tag node
		t.Fatalf("alpha forward links = %d", len(graph.ForwardLinks("alpha")))
	}
	path := graph.ShortestPath("alpha", "beta")
	if len(path) != 2 || path[0] != "alpha" || path[1] != "beta" {
		t.Fatalf("shortest path = %#v", path)
	}
	if len(graph.SurprisingConnections(10)) == 0 {
		t.Fatal("different explicit tags should make the link explainably surprising")
	}
	a := DeterministicLayout(graph, 50, 16)
	b := DeterministicLayout(graph, 50, 16)
	if len(a) != len(b) {
		t.Fatalf("layout lengths differ: %d vs %d", len(a), len(b))
	}
	for id, point := range a {
		if point != b[id] {
			t.Fatalf("layout moved for %q: %#v vs %#v", id, point, b[id])
		}
	}
}

func TestAtlasSessionGhostAndSourceEdges(t *testing.T) {
	graph := BuildAtlasGraph([]Note{
		{Name: "session-123", Title: "Captured run", Kind: "session", Links: []string{"missing-idea"}, SourceURL: "https://example.test/run", CapturedAt: time.Now().UTC()},
	})
	ghosts := graph.Ghosts()
	if len(ghosts) != 1 || ghosts[0].ID != "missing-idea" {
		t.Fatalf("ghosts = %#v", ghosts)
	}
	var session bool
	var captured bool
	for _, node := range graph.Nodes {
		if node.ID == "session-123" && node.Kind == NodeSession {
			session = true
		}
	}
	for _, edge := range graph.Links {
		if edge.Type == "captured_from" && edge.Origin == OriginExtracted && edge.Confidence == 1 {
			captured = true
		}
	}
	if !session || !captured {
		t.Fatalf("session/source metadata missing: session=%v captured=%v graph=%#v", session, captured, graph)
	}
}

func TestSaveNotePreservesUnknownFrontmatterAndTrashIsRecoverable(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("SONUT_KNOWLEDGE_DIR", dir)
	path := filepath.Join(dir, "fact.md")
	raw := "---\ntype: note\nowner: research\ncreated_at: 2026-01-01T00:00:00Z\n---\n\n# Fact\n\nOriginal body. #old\n"
	if err := os.WriteFile(path, []byte(raw), 0o644); err != nil {
		t.Fatal(err)
	}
	notes, err := LoadNotes()
	if err != nil || len(notes) != 1 {
		t.Fatalf("load notes: %v %#v", err, notes)
	}
	note := notes[0]
	if note.SourceURL != "" || !strings.Contains(note.FrontmatterRaw, "owner: research") {
		t.Fatalf("frontmatter parse lost unknown fields: %#v", note)
	}
	if _, err := SaveNote(NoteDraft{Path: note.Path, Name: note.Name, Title: "Fact updated", Tags: []string{"new"}, Body: "Edited body", Frontmatter: note.FrontmatterRaw}); err != nil {
		t.Fatal(err)
	}
	updated, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	text := string(updated)
	for _, want := range []string{"owner: research", "# Fact updated", "Edited body", "tags: [\"new\"]"} {
		if !strings.Contains(text, want) {
			t.Fatalf("saved note missing %q:\n%s", want, text)
		}
	}
	if _, err := MoveToTrash(path); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("note still exists after trash move: %v", err)
	}
	entries, err := filepath.Glob(filepath.Join(dir, ".trash", "*", "fact.md"))
	if err != nil || len(entries) != 1 {
		t.Fatalf("trash entries = %v, err=%v", entries, err)
	}
}

func TestWebNoteStoresProvenanceMetadata(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("SONUT_KNOWLEDGE_DIR", dir)
	if err := os.WriteFile(filepath.Join(dir, "stub.md"), []byte("# Stub\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	path, err := SaveNote(NoteDraft{
		Title:        "A source",
		Body:         "Quoted page text",
		SourceURL:    "https://docs.example.test/guide",
		SourceDomain: "docs.example.test",
		SearchQuery:  "native knowledge",
		Kind:         "web",
		CapturedAt:   time.Date(2026, 8, 4, 10, 0, 0, 0, time.UTC),
	})
	if err != nil {
		t.Fatal(err)
	}
	notes, err := LoadNotes()
	if err != nil || len(notes) != 2 {
		t.Fatalf("load web note: %v %#v", err, notes)
	}
	if notes[0].Kind != "web" || notes[0].SourceURL == "" || notes[0].SourceDomain != "docs.example.test" || notes[0].CapturedAt.IsZero() {
		t.Fatalf("web provenance not loaded: %#v", notes[0])
	}
	raw, err := os.ReadFile(path)
	if err != nil || !strings.Contains(string(raw), "search_query: native knowledge") {
		t.Fatalf("search query missing from saved source: %v\n%s", err, raw)
	}
}
