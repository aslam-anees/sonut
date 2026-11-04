package knowledge

import (
	"hash/fnv"
	"math"
	"sort"
	"strings"

	"github.com/aistatine/sonut/internal/graphalgo"
)

// Neighbors returns the deterministic local neighborhood around id, including
// id. Traversal is breadth-first and bounded by depth rather than graph size.
func (g Graph) Neighbors(id string, depth int) []GraphNode {
	if depth < 0 {
		depth = 0
	}
	byID := g.nodeMap()
	if _, ok := byID[id]; !ok {
		return nil
	}
	adjacency := graphalgo.Adjacency(g.edgePairs())
	dist := map[string]int{id: 0}
	queue := []string{id}
	for len(queue) > 0 {
		current := queue[0]
		queue = queue[1:]
		if dist[current] >= depth {
			continue
		}
		for _, next := range adjacency[current] {
			if _, seen := dist[next]; seen {
				continue
			}
			dist[next] = dist[current] + 1
			queue = append(queue, next)
		}
	}
	ids := make([]string, 0, len(dist))
	for nodeID := range dist {
		ids = append(ids, nodeID)
	}
	sort.Strings(ids)
	out := make([]GraphNode, 0, len(ids))
	for _, nodeID := range ids {
		out = append(out, *byID[nodeID])
	}
	return out
}

func (g Graph) ForwardLinks(id string) []GraphLink {
	var out []GraphLink
	for _, edge := range g.Links {
		if edge.Source == id {
			out = append(out, edge)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Target < out[j].Target })
	return out
}

func (g Graph) Backlinks(id string) []GraphLink {
	var out []GraphLink
	for _, edge := range g.Links {
		if edge.Target == id {
			out = append(out, edge)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Source < out[j].Source })
	return out
}

// ShortestPath returns node IDs, including both endpoints. Edges are treated
// as navigable relationships because backlinks are first-class in the atlas.
func (g Graph) ShortestPath(from, to string) []string {
	return graphalgo.ShortestPath(g.sortedNodeIDs(), g.edgePairs(), from, to)
}

// CommunityDetection uses deterministic connected components. It is
// intentionally explainable: a community means the notes share an explicit
// graph route, never that an opaque model guessed a theme.
func (g Graph) CommunityDetection() map[string]int {
	communities := map[string]int{}
	ids := g.sortedNodeIDs()
	byID := g.nodeMap()
	adjacency := graphalgo.Adjacency(g.edgePairs())
	// Explicit tags are the strongest local community evidence. Assign them
	// first so a link between differently tagged notes can be surfaced as a
	// surprising cross-community route; untagged nodes fall back to connected
	// components below.
	tags := map[string][]string{}
	for _, id := range ids {
		node := byID[id]
		if len(node.Tags) > 0 {
			tag := strings.ToLower(strings.TrimSpace(node.Tags[0]))
			if tag != "" {
				tags[tag] = append(tags[tag], id)
			}
		} else if node.Kind == NodeTag && strings.HasPrefix(node.ID, "tag:") {
			tags[strings.TrimPrefix(node.ID, "tag:")] = append(tags[strings.TrimPrefix(node.ID, "tag:")], id)
		}
	}
	tagNames := make([]string, 0, len(tags))
	for tag := range tags {
		tagNames = append(tagNames, tag)
	}
	sort.Strings(tagNames)
	community := 0
	for _, tag := range tagNames {
		for _, id := range tags[tag] {
			communities[id] = community
		}
		community++
	}
	for _, id := range ids {
		if _, seen := communities[id]; seen {
			continue
		}
		queue := []string{id}
		communities[id] = community
		for len(queue) > 0 {
			current := queue[0]
			queue = queue[1:]
			for _, next := range adjacency[current] {
				if _, seen := communities[next]; seen {
					continue
				}
				communities[next] = community
				queue = append(queue, next)
			}
		}
		community++
	}
	return communities
}

type RankedNode struct {
	Node  GraphNode
	Score float64
}

func (g Graph) Hubs(limit int) []RankedNode {
	byID := g.nodeMap()
	ids := g.sortedNodeIDs()
	out := make([]RankedNode, 0, len(ids))
	for _, id := range ids {
		node := *byID[id]
		score := float64(node.Degree)
		if score == 0 {
			score = float64(node.Size)
		}
		out = append(out, RankedNode{Node: node, Score: score})
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Score != out[j].Score {
			return out[i].Score > out[j].Score
		}
		return out[i].Node.ID < out[j].Node.ID
	})
	if limit > 0 && len(out) > limit {
		out = out[:limit]
	}
	return out
}

func (g Graph) Orphans() []GraphNode {
	byID := g.nodeMap()
	var out []GraphNode
	for _, id := range g.sortedNodeIDs() {
		if byID[id].Size == 0 && byID[id].Degree == 0 {
			out = append(out, *byID[id])
		}
	}
	return out
}

func (g Graph) Ghosts() []GraphNode {
	byID := g.nodeMap()
	var out []GraphNode
	for _, id := range g.sortedNodeIDs() {
		if byID[id].Ghost || byID[id].Kind == NodeGhost {
			out = append(out, *byID[id])
		}
	}
	return out
}

// SurprisingConnections surfaces extracted links that bridge explicit
// communities. It never upgrades them to inferred or authored meaning.
func (g Graph) SurprisingConnections(limit int) []GraphLink {
	communities := g.CommunityDetection()
	out := make([]GraphLink, 0)
	for _, edge := range g.Links {
		if communities[edge.Source] != communities[edge.Target] {
			out = append(out, edge)
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Source != out[j].Source {
			return out[i].Source < out[j].Source
		}
		return out[i].Target < out[j].Target
	})
	if limit > 0 && len(out) > limit {
		out = out[:limit]
	}
	return out
}

func (g Graph) DuplicateAliasCandidates() [][2]string {
	byAlias := map[string][]string{}
	for _, node := range g.Nodes {
		for _, alias := range append(append([]string{}, node.Aliases...), node.Title) {
			alias = strings.ToLower(strings.TrimSpace(alias))
			if alias != "" {
				byAlias[alias] = append(byAlias[alias], node.ID)
			}
		}
	}
	var out [][2]string
	for _, ids := range byAlias {
		sort.Strings(ids)
		for i := 1; i < len(ids); i++ {
			out = append(out, [2]string{ids[0], ids[i]})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i][0]+out[i][1] < out[j][0]+out[j][1] })
	return out
}

func (g Graph) StaleSources() []GraphNode {
	var out []GraphNode
	for _, node := range g.Nodes {
		if node.Stale && node.Source != "" {
			out = append(out, node)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

type LayoutPoint struct {
	X int
	Y int
}

// DeterministicLayout places nodes on a stable radial/community layout. It is
// deliberately O(nodes + edges) and bounded by the caller's dimensions; no
// per-frame force simulation is involved.
func DeterministicLayout(g Graph, width, height int) map[string]LayoutPoint {
	width = max(width, 1)
	height = max(height, 1)
	ids := g.sortedNodeIDs()
	if len(ids) == 0 {
		return map[string]LayoutPoint{}
	}
	communities := g.CommunityDetection()
	groups := map[int][]string{}
	for _, id := range ids {
		groups[communities[id]] = append(groups[communities[id]], id)
	}
	communityIDs := make([]int, 0, len(groups))
	for id := range groups {
		communityIDs = append(communityIDs, id)
	}
	sort.Ints(communityIDs)
	points := make(map[string]LayoutPoint, len(ids))
	for groupIndex, communityID := range communityIDs {
		members := groups[communityID]
		angle := 2 * math.Pi * float64(groupIndex) / float64(max(len(communityIDs), 1))
		cx := float64(width-1)/2 + float64(width-1)*0.29*math.Cos(angle)
		cy := float64(height-1)/2 + float64(height-1)*0.30*math.Sin(angle)
		radius := float64(max(2, min(width, height)/6))
		for index, id := range members {
			phase := stableFloat(id)
			localAngle := phase*2*math.Pi + float64(index)*2*math.Pi/float64(max(len(members), 1))
			distance := radius * (0.35 + 0.65*float64(index+1)/float64(len(members)))
			x := int(math.Round(cx + distance*math.Cos(localAngle)))
			y := int(math.Round(cy + distance*0.55*math.Sin(localAngle)))
			points[id] = LayoutPoint{X: clamp(x, 0, width-1), Y: clamp(y, 0, height-1)}
		}
	}
	return points
}

func assignCommunities(g *Graph) {
	communities := g.CommunityDetection()
	for i := range g.Nodes {
		g.Nodes[i].Community = communities[g.Nodes[i].ID]
		g.Nodes[i].Degree = max(g.Nodes[i].Degree, g.Nodes[i].Size)
	}
}

func (g Graph) nodeMap() map[string]*GraphNode {
	byID := make(map[string]*GraphNode, len(g.Nodes))
	for i := range g.Nodes {
		byID[g.Nodes[i].ID] = &g.Nodes[i]
	}
	return byID
}

func (g Graph) sortedNodeIDs() []string {
	ids := make([]string, 0, len(g.Nodes))
	for _, node := range g.Nodes {
		ids = append(ids, node.ID)
	}
	sort.Strings(ids)
	return ids
}

func (g Graph) adjacent(id string) []string {
	return graphalgo.Adjacency(g.edgePairs())[id]
}

func (g Graph) edgePairs() []graphalgo.Edge {
	edges := make([]graphalgo.Edge, 0, len(g.Links))
	for _, edge := range g.Links {
		edges = append(edges, graphalgo.Edge{From: edge.Source, To: edge.Target})
	}
	return edges
}

func stableFloat(value string) float64 {
	h := fnv.New32a()
	_, _ = h.Write([]byte(value))
	return float64(h.Sum32()) / float64(math.MaxUint32)
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}

func clamp(value, low, high int) int {
	if value < low {
		return low
	}
	if value > high {
		return high
	}
	return value
}
