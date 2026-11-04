package knowledge

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func setupDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("SONUT_KNOWLEDGE_DIR", dir)
	// A pre-existing note suppresses the welcome-note seeding so counts below
	// only reflect what each test creates (plus this stub).
	if err := os.WriteFile(filepath.Join(dir, "stub.md"), []byte("# Stub\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	return dir
}

func TestSlugify(t *testing.T) {
	cases := map[string]string{
		"How Linking Works":   "how-linking-works",
		"  Go / llama.cpp!! ": "go-llama-cpp",
		"---":                 "",
	}
	for in, want := range cases {
		if got := Slugify(in); got != want {
			t.Errorf("Slugify(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestAddLoadAndGraph(t *testing.T) {
	setupDir(t)
	if _, err := AddNote("Alpha Note", "Links to [[Beta Note]] and [[ghost-target]]. #core"); err != nil {
		t.Fatal(err)
	}
	if _, err := AddNote("Beta Note", "Back to [[Alpha Note]]. #core"); err != nil {
		t.Fatal(err)
	}

	notes, err := LoadNotes()
	if err != nil {
		t.Fatal(err)
	}
	if len(notes) != 3 { // stub + alpha + beta
		t.Fatalf("got %d notes, want 3", len(notes))
	}

	g := BuildGraph(notes)
	if len(g.Links) != 3 {
		t.Fatalf("got %d links, want 3", len(g.Links))
	}
	var ghost, alpha *GraphNode
	for i := range g.Nodes {
		switch g.Nodes[i].ID {
		case "ghost-target":
			ghost = &g.Nodes[i]
		case "alpha-note":
			alpha = &g.Nodes[i]
		}
	}
	if ghost == nil || !ghost.Ghost {
		t.Fatal("expected ghost node for unresolved wikilink")
	}
	if alpha == nil || alpha.Tag != "core" {
		t.Fatalf("alpha node tag = %v, want core", alpha)
	}
	if alpha.Size != 3 { // out: beta, ghost; in: beta
		t.Fatalf("alpha degree = %d, want 3", alpha.Size)
	}
}

func TestAddNoteAppendsToExisting(t *testing.T) {
	setupDir(t)
	p1, _ := AddNote("Same Title", "first line")
	p2, _ := AddNote("Same Title", "second line")
	if p1 != p2 {
		t.Fatalf("expected same path, got %q vs %q", p1, p2)
	}
	raw, _ := os.ReadFile(p1)
	if !strings.Contains(string(raw), "first line") || !strings.Contains(string(raw), "second line") {
		t.Fatalf("append failed: %s", raw)
	}
}

func TestExportHTMLSeedsAndEscapes(t *testing.T) {
	dir := setupDir(t)
	if _, err := AddNote("Sneaky", "body with </script> inside"); err != nil {
		t.Fatal(err)
	}
	path, nodes, _, err := ExportHTML()
	if err != nil {
		t.Fatal(err)
	}
	if filepath.Dir(path) != dir || nodes != 2 { // stub + sneaky
		t.Fatalf("path=%s nodes=%d", path, nodes)
	}
	raw, _ := os.ReadFile(path)
	html := string(raw)
	if strings.Contains(html, `body with </script>`) {
		t.Fatal("note body </script> was not escaped in embedded JSON")
	}
	if !strings.Contains(html, "@code Knowledge Graph") {
		t.Fatal("template marker missing")
	}
}

func TestEnsureDirSeedsWelcome(t *testing.T) {
	t.Setenv("SONUT_KNOWLEDGE_DIR", t.TempDir())
	if _, err := EnsureDir(); err != nil {
		t.Fatal(err)
	}
	notes, err := LoadNotes()
	if err != nil {
		t.Fatal(err)
	}
	if len(notes) != 2 {
		t.Fatalf("seed created %d notes, want 2", len(notes))
	}
	g := BuildGraph(notes)
	if len(g.Links) == 0 {
		t.Fatal("welcome notes should link to each other")
	}
}
