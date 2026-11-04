package tui

import (
	"fmt"
	"path/filepath"
	"strings"

	"github.com/aistatine/sonut/internal/graph"
	"github.com/aistatine/sonut/internal/knowledge"
)

// handleGraphCommand implements /graph — map the project into a knowledge
// graph (graphify-style: deterministic code parsing, EXTRACTED/INFERRED
// edges, community detection) and query it instead of grepping:
//
//	/graph [path]          — build the graph, write graphify-out/, show highlights
//	/graph explain <node>  — one node: source, community, degree, connections
//	/graph path <a> <b>    — shortest path between two concepts, hop by hop
//	/graph open            — open the interactive graph.html in the browser
func (m model) handleGraphCommand(argText string) string {
	fields := strings.Fields(argText)
	sub := ""
	if len(fields) > 0 {
		sub = strings.ToLower(fields[0])
	}

	load := func() (*graph.Graph, string) {
		g, err := graph.Load(m.cwd)
		if err != nil {
			return nil, "No graph found for this workspace yet — run /graph first to build it."
		}
		return g, ""
	}

	switch sub {
	case "explain":
		if len(fields) < 2 {
			return "Usage: /graph explain <node>  (a file path or symbol name)"
		}
		g, msg := load()
		if g == nil {
			return msg
		}
		return g.Explain(strings.Join(fields[1:], " "))

	case "path":
		if len(fields) != 3 {
			return "Usage: /graph path <from> <to>"
		}
		g, msg := load()
		if g == nil {
			return msg
		}
		return g.PathText(fields[1], fields[2])

	case "open":
		htmlPath := filepath.Join(m.cwd, graph.OutDirName, "graph.html")
		if _, msg := load(); msg != "" {
			return msg
		}
		if err := knowledge.OpenInBrowser(htmlPath); err != nil {
			return "Could not open the browser: " + err.Error() + "\nOpen it manually: " + htmlPath
		}
		return "Project graph opened in your browser: " + htmlPath

	default:
		root := m.cwd
		if sub != "" {
			if filepath.IsAbs(argText) {
				root = strings.TrimSpace(argText)
			} else {
				root = filepath.Join(m.cwd, strings.TrimSpace(argText))
			}
		}
		g, err := graph.Build(root)
		if err != nil {
			return "Graph build failed: " + err.Error()
		}
		dir, err := g.WriteAll()
		if err != nil {
			return "Graph built but writing " + graph.OutDirName + "/ failed: " + err.Error()
		}
		// Open the interactive graph right away — the whole point of building
		// it is to look at it. A headless/SSH failure degrades to the hint.
		htmlLine := "graph.html       opened in your browser"
		if err := knowledge.OpenInBrowser(filepath.Join(dir, "graph.html")); err != nil {
			htmlLine = "graph.html       open in any browser (or /graph open)"
		}
		return fmt.Sprintf(
			"Mapped %s — %d nodes, %d edges → %s\n  %s\n  GRAPH_REPORT.md  key concepts, communities, surprising connections\n  graph.json       query anytime: /graph explain <node> · /graph path <a> <b>",
			root, len(g.Nodes), len(g.Edges), dir, htmlLine)
	}
}
