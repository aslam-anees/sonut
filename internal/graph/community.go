package graph

import "sort"

// detectCommunities assigns each node a community via deterministic label
// propagation: every node starts as its own label, then repeatedly adopts
// the most common label among its neighbors (ties broken by smallest label)
// until stable or the iteration cap is hit. Labels are then renumbered
// densely by community size (0 = largest).
func (g *Graph) detectCommunities() {
	idx := make(map[string]int, len(g.Nodes))
	for i, n := range g.Nodes {
		idx[n.ID] = i
	}
	adj := make([][]int, len(g.Nodes))
	for _, e := range g.Edges {
		a, aok := idx[e.From]
		b, bok := idx[e.To]
		if !aok || !bok {
			continue
		}
		adj[a] = append(adj[a], b)
		adj[b] = append(adj[b], a)
	}

	labels := make([]int, len(g.Nodes))
	for i := range labels {
		labels[i] = i
	}
	for iter := 0; iter < 20; iter++ {
		changed := false
		for i := range g.Nodes {
			if len(adj[i]) == 0 {
				continue
			}
			counts := map[int]int{}
			for _, nb := range adj[i] {
				counts[labels[nb]]++
			}
			best, bestCount := labels[i], 0
			for label, c := range counts {
				if c > bestCount || (c == bestCount && label < best) {
					best, bestCount = label, c
				}
			}
			if best != labels[i] {
				labels[i] = best
				changed = true
			}
		}
		if !changed {
			break
		}
	}

	// Renumber by size: community 0 is the biggest.
	sizes := map[int]int{}
	for _, l := range labels {
		sizes[l]++
	}
	order := make([]int, 0, len(sizes))
	for l := range sizes {
		order = append(order, l)
	}
	sort.Slice(order, func(i, j int) bool {
		if sizes[order[i]] != sizes[order[j]] {
			return sizes[order[i]] > sizes[order[j]]
		}
		return order[i] < order[j]
	})
	rank := make(map[int]int, len(order))
	for r, l := range order {
		rank[l] = r
	}
	for i, n := range g.Nodes {
		n.Community = rank[labels[i]]
	}
}
