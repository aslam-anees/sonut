package knowledge

import (
	"encoding/json"
	"sort"
	"strings"
	"time"
)

// NodeKind is intentionally a string: future metadata can be read without a
// migration and old graph.json consumers can continue to ignore new fields.
type NodeKind string

const (
	NodeNote           NodeKind = "note"
	NodeSession        NodeKind = "session"
	NodeWebSource      NodeKind = "web-source"
	NodeTag            NodeKind = "tag"
	NodeGhost          NodeKind = "ghost"
	NodeProjectConcept NodeKind = "project-concept"
)

type EdgeOrigin string

const (
	OriginExtracted EdgeOrigin = "EXTRACTED"
	OriginInferred  EdgeOrigin = "INFERRED"
	OriginAmbiguous EdgeOrigin = "AMBIGUOUS"
)

// GraphNode is one node in the exported graph.
type GraphNode struct {
	ID    string `json:"id"`
	Title string `json:"title"`
	Tag   string `json:"tag"`   // primary tag, drives color grouping
	Size  int    `json:"size"`  // degree (in+out links)
	Ghost bool   `json:"ghost"` // linked-to but no file yet
	Body  string `json:"body"`  // full markdown, shown in the side panel

	Kind       NodeKind  `json:"kind,omitempty"`
	Tags       []string  `json:"tags,omitempty"`
	Aliases    []string  `json:"aliases,omitempty"`
	Created    time.Time `json:"created,omitempty"`
	Modified   time.Time `json:"modified,omitempty"`
	Community  int       `json:"community,omitempty"`
	Degree     int       `json:"degree,omitempty"`
	Centrality float64   `json:"centrality,omitempty"`
	Source     string    `json:"source,omitempty"`
	Provenance string    `json:"provenance,omitempty"`
	Contested  bool      `json:"contested,omitempty"`
	Stale      bool      `json:"stale,omitempty"`
}

// GraphLink is one edge (wikilink) in the exported graph.
type GraphLink struct {
	Source     string     `json:"source"`
	Target     string     `json:"target"`
	Type       string     `json:"type,omitempty"`
	Origin     EdgeOrigin `json:"origin,omitempty"`
	Confidence float64    `json:"confidence,omitempty"`
	Evidence   string     `json:"evidence,omitempty"`
	Timestamp  time.Time  `json:"timestamp,omitempty"`
}

// Graph is the JSON payload embedded into the HTML view.
type Graph struct {
	Nodes []GraphNode `json:"nodes"`
	Links []GraphLink `json:"links"`
}

// BuildGraph converts parsed notes into a graph. Wikilinks that point at
// notes which don't exist yet become ghost nodes, Obsidian-style.
func BuildGraph(notes []Note) Graph {
	byID := make(map[string]*GraphNode)
	for _, n := range notes {
		node := &GraphNode{ID: n.Name, Title: n.Title, Body: n.Content, Kind: nodeKindForNote(n), Tags: append([]string(nil), n.Tags...), Aliases: append([]string(nil), n.Aliases...), Created: n.Created, Modified: n.Updated, Source: firstNonEmpty(n.SourceURL, n.Path), Provenance: n.Provenance, Contested: n.Contested, Stale: n.Stale}
		if len(n.Tags) > 0 {
			node.Tag = n.Tags[0]
		}
		byID[n.Name] = node
	}

	var links []GraphLink
	seen := make(map[[2]string]bool)
	for _, n := range notes {
		for _, target := range n.Links {
			if target == "" || target == n.Name {
				continue
			}
			if _, ok := byID[target]; !ok {
				byID[target] = &GraphNode{ID: target, Title: target, Kind: NodeGhost, Ghost: true}
			}
			key := [2]string{n.Name, target}
			if seen[key] {
				continue
			}
			seen[key] = true
			links = append(links, GraphLink{Source: n.Name, Target: target, Type: "links_to", Origin: OriginExtracted, Confidence: 1, Evidence: "wikilink"})
			byID[n.Name].Size++
			byID[target].Size++
			byID[n.Name].Degree = byID[n.Name].Size
			byID[target].Degree = byID[target].Size
		}
	}

	g := Graph{Links: links}
	for _, node := range byID {
		g.Nodes = append(g.Nodes, *node)
	}
	sort.Slice(g.Nodes, func(i, j int) bool { return g.Nodes[i].ID < g.Nodes[j].ID })
	return g
}

// BuildAtlasGraph is the enriched graph consumed by the native atlas. The
// legacy BuildGraph intentionally keeps its historical note-only shape so
// graph.html and existing callers remain compatible; this function adds typed
// provenance edges and explicit tag nodes without requiring a migration.
func BuildAtlasGraph(notes []Note) Graph {
	g := BuildGraph(notes)
	byID := make(map[string]*GraphNode, len(g.Nodes))
	reindex := func() {
		byID = make(map[string]*GraphNode, len(g.Nodes))
		for i := range g.Nodes {
			byID[g.Nodes[i].ID] = &g.Nodes[i]
		}
	}
	for i := range g.Nodes {
		byID[g.Nodes[i].ID] = &g.Nodes[i]
	}
	seen := make(map[[2]string]bool)
	for _, edge := range g.Links {
		seen[[2]string{edge.Source, edge.Target}] = true
	}
	for _, n := range notes {
		for _, tag := range n.Tags {
			tag = strings.ToLower(strings.TrimSpace(tag))
			if tag == "" {
				continue
			}
			id := "tag:" + tag
			if _, ok := byID[id]; !ok {
				node := GraphNode{ID: id, Title: "#" + tag, Tag: tag, Kind: NodeTag}
				g.Nodes = append(g.Nodes, node)
				reindex()
			}
			key := [2]string{n.Name, id}
			if seen[key] {
				continue
			}
			seen[key] = true
			g.Links = append(g.Links, GraphLink{Source: n.Name, Target: id, Type: "tagged", Origin: OriginExtracted, Confidence: 1, Evidence: "markdown tag"})
			byID[n.Name].Size++
			byID[id].Size++
			byID[n.Name].Degree = byID[n.Name].Size
			byID[id].Degree = byID[id].Size
		}
		if n.SourceURL != "" {
			id := "source:" + n.SourceURL
			if _, ok := byID[id]; !ok {
				g.Nodes = append(g.Nodes, GraphNode{ID: id, Title: n.SourceURL, Kind: NodeWebSource, Source: n.SourceURL})
				reindex()
			}
			key := [2]string{n.Name, id}
			if !seen[key] {
				seen[key] = true
				g.Links = append(g.Links, GraphLink{Source: n.Name, Target: id, Type: "captured_from", Origin: OriginExtracted, Confidence: 1, Evidence: "note frontmatter", Timestamp: n.CapturedAt})
				byID[n.Name].Size++
				byID[id].Size++
				byID[n.Name].Degree = byID[n.Name].Size
				byID[id].Degree = byID[id].Size
			}
		}
	}
	assignCommunities(&g)
	sort.Slice(g.Nodes, func(i, j int) bool { return g.Nodes[i].ID < g.Nodes[j].ID })
	sort.SliceStable(g.Links, func(i, j int) bool {
		if g.Links[i].Source != g.Links[j].Source {
			return g.Links[i].Source < g.Links[j].Source
		}
		return g.Links[i].Target < g.Links[j].Target
	})
	return g
}

func nodeKindForNote(n Note) NodeKind {
	if strings.HasPrefix(strings.ToLower(strings.TrimSpace(n.Name)), "session-") {
		return NodeSession
	}
	switch strings.ToLower(strings.TrimSpace(n.Kind)) {
	case "session", "captured-session":
		return NodeSession
	case "web", "web-source", "source":
		return NodeWebSource
	default:
		return NodeNote
	}
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}

// JSON marshals the graph for embedding in the HTML template.
func (g Graph) JSON() ([]byte, error) {
	return json.Marshal(g)
}
