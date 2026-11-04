package graph

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// OutDirName is the output directory created inside the mapped project.
const OutDirName = "graphify-out"

// WriteAll writes graph.json, graph.html and GRAPH_REPORT.md into
// <root>/graphify-out and returns that directory.
func (g *Graph) WriteAll() (string, error) {
	dir := filepath.Join(g.Root, OutDirName)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	data, err := json.MarshalIndent(g, "", " ")
	if err != nil {
		return "", err
	}
	if err := os.WriteFile(filepath.Join(dir, "graph.json"), data, 0o644); err != nil {
		return "", err
	}
	if err := g.writeHTML(filepath.Join(dir, "graph.html")); err != nil {
		return "", err
	}
	if err := os.WriteFile(filepath.Join(dir, "GRAPH_REPORT.md"), []byte(g.Report()), 0o644); err != nil {
		return "", err
	}
	return dir, nil
}

// Load reads a previously written graph.json from <root>/graphify-out.
func Load(root string) (*Graph, error) {
	data, err := os.ReadFile(filepath.Join(root, OutDirName, "graph.json"))
	if err != nil {
		return nil, err
	}
	var g Graph
	if err := json.Unmarshal(data, &g); err != nil {
		return nil, err
	}
	g.byID = make(map[string]*Node, len(g.Nodes))
	for _, n := range g.Nodes {
		g.byID[n.ID] = n
	}
	return &g, nil
}

// Explain renders the graphify-style card for one node: source, community,
// degree, and every connection with its relation and origin tag.
func (g *Graph) Explain(query string) string {
	n := g.FindNode(query)
	if n == nil {
		return fmt.Sprintf("No node matches %q. Try a file path or symbol name from the graph.", query)
	}
	var b strings.Builder
	fmt.Fprintf(&b, "Node: %s\n", n.Label)
	if n.File != "" {
		fmt.Fprintf(&b, "  Source:    %s", n.File)
		if n.Line > 0 {
			fmt.Fprintf(&b, " L%d", n.Line)
		}
		b.WriteString("\n")
	}
	fmt.Fprintf(&b, "  Kind:      %s\n  Community: %d\n  Degree:    %d\n", n.Kind, n.Community, n.Degree)
	edges := g.Neighbors(n.ID)
	fmt.Fprintf(&b, "\nConnections (%d):\n", len(edges))
	const cap = 40
	for i, e := range edges {
		if i == cap {
			fmt.Fprintf(&b, "  … and %d more\n", len(edges)-cap)
			break
		}
		other, arrow := e.To, "-->"
		if e.To == n.ID {
			other, arrow = e.From, "<--"
		}
		label := other
		if on := g.byID[other]; on != nil {
			label = on.Label
		}
		fmt.Fprintf(&b, "  %s %s [%s] [%s]\n", arrow, label, e.Rel, e.Origin)
	}
	return strings.TrimRight(b.String(), "\n")
}

// PathText renders the shortest path between two queries hop by hop.
func (g *Graph) PathText(fromQ, toQ string) string {
	path := g.ShortestPath(fromQ, toQ)
	if path == nil {
		return fmt.Sprintf("No path found between %q and %q (unresolved name or disconnected).", fromQ, toQ)
	}
	var b strings.Builder
	fmt.Fprintf(&b, "Shortest path (%d hops):\n", len(path)-1)
	for i, id := range path {
		label := id
		if n := g.byID[id]; n != nil {
			label = fmt.Sprintf("%s (%s)", n.Label, n.Kind)
		}
		if i == 0 {
			fmt.Fprintf(&b, "  %s\n", label)
			continue
		}
		rel := relBetween(g, path[i-1], id)
		fmt.Fprintf(&b, "  --[%s]--> %s\n", rel, label)
	}
	return strings.TrimRight(b.String(), "\n")
}

func relBetween(g *Graph, a, b string) string {
	for _, e := range g.Edges {
		if (e.From == a && e.To == b) || (e.From == b && e.To == a) {
			return e.Rel
		}
	}
	return "?"
}

// Report writes the GRAPH_REPORT.md highlights: key concepts, community
// breakdown, surprising cross-community connections, suggested questions.
func (g *Graph) Report() string {
	var b strings.Builder
	b.WriteString("# Graph Report\n\n")
	fmt.Fprintf(&b, "Mapped `%s`: **%d nodes**, **%d edges**.\n\n", g.Root, len(g.Nodes), len(g.Edges))

	extracted, inferred := 0, 0
	for _, e := range g.Edges {
		if e.Origin == OriginExtracted {
			extracted++
		} else {
			inferred++
		}
	}
	fmt.Fprintf(&b, "Edges: %d EXTRACTED (explicit in source), %d INFERRED (resolved by name).\n\n", extracted, inferred)

	top := make([]*Node, len(g.Nodes))
	copy(top, g.Nodes)
	sort.Slice(top, func(i, j int) bool {
		if top[i].Degree != top[j].Degree {
			return top[i].Degree > top[j].Degree
		}
		return top[i].ID < top[j].ID
	})

	b.WriteString("## Key concepts (highest degree)\n\n")
	for i, n := range top {
		if i == 15 || n.Degree == 0 {
			break
		}
		loc := n.File
		if n.Line > 1 {
			loc = fmt.Sprintf("%s:%d", n.File, n.Line)
		}
		fmt.Fprintf(&b, "%2d. **%s** (%s, community %d, degree %d) — %s\n", i+1, n.Label, n.Kind, n.Community, n.Degree, loc)
	}

	commSize := map[int]int{}
	for _, n := range g.Nodes {
		commSize[n.Community]++
	}
	b.WriteString("\n## Communities\n\n")
	comms := make([]int, 0, len(commSize))
	for c := range commSize {
		comms = append(comms, c)
	}
	sort.Ints(comms)
	shown := 0
	for _, c := range comms {
		if commSize[c] < 2 || shown == 10 {
			continue
		}
		shown++
		fmt.Fprintf(&b, "- Community %d — %d nodes, e.g. %s\n", c, commSize[c], exampleLabels(g, c, 3))
	}

	b.WriteString("\n## Surprising connections\n\nEdges that cross community boundaries — places where otherwise separate parts of the project touch:\n\n")
	shown = 0
	for _, e := range g.Edges {
		a, bn := g.byID[e.From], g.byID[e.To]
		if a == nil || bn == nil || a.Community == bn.Community || e.Rel == "contains" {
			continue
		}
		fmt.Fprintf(&b, "- **%s** --[%s]--> **%s** [%s]\n", a.Label, e.Rel, bn.Label, e.Origin)
		if shown++; shown == 10 {
			break
		}
	}

	b.WriteString("\n## Suggested questions\n\n")
	if len(top) > 1 && top[0].Degree > 0 {
		fmt.Fprintf(&b, "- `/graph explain %s` — what is the most connected concept doing?\n", top[0].Label)
	}
	if len(top) > 2 && top[2].Degree > 0 {
		fmt.Fprintf(&b, "- `/graph path %s %s` — how do the top hubs relate?\n", top[0].Label, top[2].Label)
	}
	b.WriteString("- `/graph open` — explore the interactive graph in your browser.\n")
	return b.String()
}

func exampleLabels(g *Graph, community, limit int) string {
	var names []string
	for _, n := range g.Nodes {
		if n.Community == community {
			names = append(names, n.Label)
			if len(names) == limit {
				break
			}
		}
	}
	return strings.Join(names, ", ")
}
