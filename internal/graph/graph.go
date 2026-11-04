// Package graph maps a project (code + docs) into a knowledge graph you can
// query instead of grepping. Code is parsed deterministically (Go via the
// stdlib AST, other languages via lightweight lexical extraction) — no LLM,
// nothing leaves the machine. Every edge is tagged with its origin:
// EXTRACTED (explicit in the source) or INFERRED (resolved by name).
package graph

import (
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/aistatine/sonut/internal/graphalgo"
)

// Edge origins.
const (
	OriginExtracted = "EXTRACTED"
	OriginInferred  = "INFERRED"
)

// Node kinds.
const (
	KindFile     = "file"
	KindPackage  = "package"
	KindFunction = "function"
	KindType     = "type"
	KindClass    = "class"
	KindDoc      = "doc"
)

type Node struct {
	ID        string `json:"id"`
	Label     string `json:"label"`
	Kind      string `json:"kind"`
	File      string `json:"file,omitempty"`
	Line      int    `json:"line,omitempty"`
	Community int    `json:"community"`
	Degree    int    `json:"degree"`
}

type Edge struct {
	From   string `json:"from"`
	To     string `json:"to"`
	Rel    string `json:"rel"`    // contains, imports, defines, calls, references, links_to
	Origin string `json:"origin"` // EXTRACTED | INFERRED
}

type Graph struct {
	Root  string  `json:"root"`
	Nodes []*Node `json:"nodes"`
	Edges []Edge  `json:"edges"`

	byID map[string]*Node
}

func (g *Graph) node(id, label, kind, file string, line int) *Node {
	if n, ok := g.byID[id]; ok {
		return n
	}
	n := &Node{ID: id, Label: label, Kind: kind, File: file, Line: line}
	g.byID[id] = n
	g.Nodes = append(g.Nodes, n)
	return n
}

func (g *Graph) edge(from, to, rel, origin string) {
	if from == to {
		return
	}
	g.Edges = append(g.Edges, Edge{From: from, To: to, Rel: rel, Origin: origin})
}

// NodeByID returns the node with the given id, or nil.
func (g *Graph) NodeByID(id string) *Node { return g.byID[id] }

// FindNode resolves a query to a node: exact id, exact label, then
// case-insensitive substring of label or id.
func (g *Graph) FindNode(query string) *Node {
	if n := g.byID[query]; n != nil {
		return n
	}
	q := strings.ToLower(query)
	var partial *Node
	for _, n := range g.Nodes {
		if n.Label == query {
			return n
		}
		if partial == nil && (strings.Contains(strings.ToLower(n.Label), q) || strings.Contains(strings.ToLower(n.ID), q)) {
			partial = n
		}
	}
	return partial
}

var skipDirs = map[string]bool{
	".git": true, "node_modules": true, "vendor": true, "dist": true,
	"build": true, "target": true, ".venv": true, "venv": true,
	"__pycache__": true, ".next": true, ".cache": true, "graphify-out": true,
	".atcode": true, ".idea": true, ".vscode": true,
}

const (
	maxFileSize = 1 << 20 // 1 MiB per file
	maxFiles    = 6000
)

// Build walks root and constructs the knowledge graph.
func Build(root string) (*Graph, error) {
	abs, err := filepath.Abs(root)
	if err != nil {
		return nil, err
	}
	g := &Graph{Root: abs, byID: map[string]*Node{}}
	ex := newExtractor(g)

	count := 0
	err = filepath.WalkDir(abs, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return nil //nolint:nilerr — unreadable entries are skipped, not fatal
		}
		name := d.Name()
		if d.IsDir() {
			if path != abs && (skipDirs[name] || strings.HasPrefix(name, ".")) {
				return filepath.SkipDir
			}
			return nil
		}
		if count >= maxFiles {
			return filepath.SkipAll
		}
		info, err := d.Info()
		if err != nil || info.Size() > maxFileSize {
			return nil
		}
		rel, _ := filepath.Rel(abs, path)
		rel = filepath.ToSlash(rel)
		if ex.ingest(rel, path) {
			count++
		}
		return nil
	})
	if err != nil {
		return nil, err
	}

	ex.resolve()
	g.computeDegrees()
	g.detectCommunities()
	return g, nil
}

func (g *Graph) computeDegrees() {
	for _, n := range g.Nodes {
		n.Degree = 0
	}
	for _, e := range g.Edges {
		if a := g.byID[e.From]; a != nil {
			a.Degree++
		}
		if b := g.byID[e.To]; b != nil {
			b.Degree++
		}
	}
}

// Neighbors returns edges touching id, sorted for stable output.
func (g *Graph) Neighbors(id string) []Edge {
	var out []Edge
	for _, e := range g.Edges {
		if e.From == id || e.To == id {
			out = append(out, e)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].To != out[j].To {
			return out[i].To < out[j].To
		}
		return out[i].From < out[j].From
	})
	return out
}

// ShortestPath returns node ids on the shortest undirected path between two
// queries, or nil if unconnected or unresolved.
func (g *Graph) ShortestPath(fromQ, toQ string) []string {
	from, to := g.FindNode(fromQ), g.FindNode(toQ)
	if from == nil || to == nil {
		return nil
	}
	nodes := make([]string, 0, len(g.Nodes))
	edges := make([]graphalgo.Edge, 0, len(g.Edges))
	for _, node := range g.Nodes {
		nodes = append(nodes, node.ID)
	}
	for _, edge := range g.Edges {
		edges = append(edges, graphalgo.Edge{From: edge.From, To: edge.To})
	}
	return graphalgo.ShortestPath(nodes, edges, from.ID, to.ID)
}
