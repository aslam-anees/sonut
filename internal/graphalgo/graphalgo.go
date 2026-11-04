// Package graphalgo contains small deterministic graph primitives shared by
// project graphs and the Markdown knowledge graph. Domain packages retain
// ownership of node metadata, provenance, and higher-level ranking policy.
package graphalgo

import "sort"

type Edge struct {
	From string
	To   string
}

// Adjacency builds a stable undirected adjacency list once for a graph.
func Adjacency(edges []Edge) map[string][]string {
	sets := map[string]map[string]bool{}
	for _, edge := range edges {
		if sets[edge.From] == nil {
			sets[edge.From] = map[string]bool{}
		}
		if sets[edge.To] == nil {
			sets[edge.To] = map[string]bool{}
		}
		sets[edge.From][edge.To] = true
		sets[edge.To][edge.From] = true
	}
	adjacency := make(map[string][]string, len(sets))
	for node, neighbors := range sets {
		out := make([]string, 0, len(neighbors))
		for neighbor := range neighbors {
			out = append(out, neighbor)
		}
		sort.Strings(out)
		adjacency[node] = out
	}
	return adjacency
}

// Neighbors returns unique undirected neighbors in stable lexical order.
func Neighbors(edges []Edge, id string) []string {
	return Adjacency(edges)[id]
}

// ShortestPath returns a deterministic undirected BFS path, including both
// endpoints. It returns nil when either endpoint is absent or disconnected.
func ShortestPath(nodes []string, edges []Edge, from, to string) []string {
	present := make(map[string]bool, len(nodes))
	for _, node := range nodes {
		present[node] = true
	}
	if !present[from] || !present[to] {
		return nil
	}
	if from == to {
		return []string{from}
	}
	parent := map[string]string{from: ""}
	queue := []string{from}
	adjacency := Adjacency(edges)
	for len(queue) > 0 {
		current := queue[0]
		queue = queue[1:]
		for _, next := range adjacency[current] {
			if _, seen := parent[next]; seen {
				continue
			}
			parent[next] = current
			if next == to {
				path := []string{to}
				for path[len(path)-1] != from {
					path = append(path, parent[path[len(path)-1]])
				}
				for i, j := 0, len(path)-1; i < j; i, j = i+1, j-1 {
					path[i], path[j] = path[j], path[i]
				}
				return path
			}
			queue = append(queue, next)
		}
	}
	return nil
}
