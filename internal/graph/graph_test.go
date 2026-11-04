package graph

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func buildFixture(t *testing.T) *Graph {
	t.Helper()
	dir := t.TempDir()
	write := func(rel, content string) {
		path := filepath.Join(dir, rel)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("main.go", `package main

import "fmt"

func main() { fmt.Println(Greet("hi")) }

func Greet(s string) string { return s }

type Server struct{}
`)
	write("app/handler.py", "from flask import Flask\n\nclass Handler:\n    pass\n\ndef serve():\n    return Greet\n")
	write("README.md", "# Project\n\nSee [main](main.go). The `Greet` function says hello.\n\n## Greet\n")

	g, err := Build(dir)
	if err != nil {
		t.Fatal(err)
	}
	return g
}

func TestBuildExtractsNodesAndEdges(t *testing.T) {
	g := buildFixture(t)

	greet := g.FindNode("Greet")
	if greet == nil || greet.Kind != KindFunction || greet.File != "main.go" {
		t.Fatalf("Greet node wrong: %+v", greet)
	}
	if g.NodeByID("sym:main.go:Server") == nil {
		t.Fatal("missing type node Server")
	}
	if g.NodeByID("import:fmt") == nil || g.NodeByID("import:flask") == nil {
		t.Fatal("missing import nodes")
	}
	if g.NodeByID("sym:app/handler.py:Handler") == nil {
		t.Fatal("missing python class node")
	}

	hasEdge := func(from, to, rel, origin string) bool {
		for _, e := range g.Edges {
			if e.From == from && e.To == to && e.Rel == rel && e.Origin == origin {
				return true
			}
		}
		return false
	}
	if !hasEdge("main.go", "import:fmt", "imports", OriginExtracted) {
		t.Fatal("missing EXTRACTED import edge")
	}
	if !hasEdge("sym:main.go:main", "sym:main.go:Greet", "calls", OriginInferred) {
		t.Fatal("missing INFERRED call edge main->Greet")
	}
	if !hasEdge("README.md", "main.go", "links_to", OriginExtracted) {
		t.Fatal("missing doc link edge")
	}
	if !hasEdge("app/handler.py", "sym:main.go:Greet", "references", OriginInferred) {
		t.Fatal("missing cross-language reference edge")
	}
}

func TestQueriesAndOutputs(t *testing.T) {
	g := buildFixture(t)

	if path := g.ShortestPath("handler.py", "fmt"); len(path) < 2 {
		t.Fatalf("expected path handler.py->fmt, got %v", path)
	}
	if out := g.Explain("Greet"); !strings.Contains(out, "Source:    main.go") || !strings.Contains(out, "INFERRED") {
		t.Fatalf("explain output wrong:\n%s", out)
	}
	if rep := g.Report(); !strings.Contains(rep, "Key concepts") || !strings.Contains(rep, "EXTRACTED") {
		t.Fatal("report missing sections")
	}

	dir, err := g.WriteAll()
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range []string{"graph.json", "graph.html", "GRAPH_REPORT.md"} {
		if _, err := os.Stat(filepath.Join(dir, f)); err != nil {
			t.Fatalf("missing output %s: %v", f, err)
		}
	}
	loaded, err := Load(g.Root)
	if err != nil {
		t.Fatal(err)
	}
	if len(loaded.Nodes) != len(g.Nodes) || loaded.FindNode("Greet") == nil {
		t.Fatal("round-trip via graph.json lost data")
	}
}
