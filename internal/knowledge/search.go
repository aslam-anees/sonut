package knowledge

import (
	"sort"
	"strings"
)

type SearchLens string

const (
	SearchLocal     SearchLens = "local"
	SearchConnected SearchLens = "connected"
	SearchOverview  SearchLens = "overview"
)

type SearchHit struct {
	NoteName     string
	Title        string
	Snippet      string
	Score        float64
	Tags         []string
	SourceType   string
	RelevantLink string
	Provenance   string
	Confidence   float64
	Lens         SearchLens
	Community    int
}

// SearchLens keeps the existing BM25 backend and adds explainable graph
// signals. It does not use embeddings or an LLM, so local search remains fast
// and useful when offline.
func (b *SearchBackend) SearchLens(query string, graph Graph, notes []Note, lens SearchLens, topK int) ([]SearchHit, error) {
	if lens == "" {
		lens = SearchLocal
	}
	if topK <= 0 {
		topK = 8
	}
	base, err := b.Search(query, max(topK*3, 12))
	if err != nil {
		return nil, err
	}
	noteByID := make(map[string]Note, len(notes))
	for _, note := range notes {
		noteByID[note.Name] = note
	}
	community := graph.CommunityDetection()
	if lens == SearchOverview {
		return overviewHits(query, graph, noteByID, topK), nil
	}
	hits := make(map[string]SearchHit)
	for _, result := range base {
		note := noteByID[result.NoteName]
		hit := SearchHit{NoteName: result.NoteName, Title: result.Title, Snippet: result.Snippet, Score: result.Score, Tags: append([]string(nil), note.Tags...), SourceType: note.Kind, Provenance: firstNonEmpty(note.Provenance, "EXTRACTED"), Confidence: 1, Lens: lens, Community: community[result.NoteName]}
		if hit.SourceType == "" {
			hit.SourceType = "manual"
		}
		hits[result.NoteName] = hit
	}
	if lens == SearchConnected {
		seedIDs := make([]string, 0, len(hits))
		for id := range hits {
			seedIDs = append(seedIDs, id)
		}
		sort.Strings(seedIDs)
		for _, id := range seedIDs {
			for _, neighbor := range graph.Neighbors(id, 2) {
				if neighbor.ID == id || neighbor.Kind == NodeTag {
					continue
				}
				if _, exists := hits[neighbor.ID]; exists {
					continue
				}
				neighborNote := noteByID[neighbor.ID]
				relevance := hits[id].Score * 0.36
				if neighbor.Title != "" && strings.Contains(strings.ToLower(query), strings.ToLower(neighbor.Title)) {
					relevance += 0.35
				}
				hits[neighbor.ID] = SearchHit{NoteName: neighbor.ID, Title: neighbor.Title, Snippet: bodyPreview(neighbor.Body, 220), Score: relevance, Tags: append([]string(nil), neighborNote.Tags...), SourceType: firstNonEmpty(neighborNote.Kind, "connected"), RelevantLink: id, Provenance: "INFERRED", Confidence: 0.36, Lens: lens, Community: community[neighbor.ID]}
			}
		}
	}
	out := mapValues(hits)
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Score != out[j].Score {
			return out[i].Score > out[j].Score
		}
		return out[i].NoteName < out[j].NoteName
	})
	if len(out) > topK {
		out = out[:topK]
	}
	return out, nil
}

func overviewHits(query string, graph Graph, notes map[string]Note, topK int) []SearchHit {
	query = strings.ToLower(strings.TrimSpace(query))
	community := graph.CommunityDetection()
	var out []SearchHit
	for _, ranked := range graph.Hubs(0) {
		node := ranked.Node
		if node.Kind == NodeTag {
			continue
		}
		score := ranked.Score / 10
		if strings.Contains(strings.ToLower(node.Title), query) || (query != "" && strings.Contains(strings.ToLower(node.Body), query)) {
			score += 1
		}
		note := notes[node.ID]
		out = append(out, SearchHit{NoteName: node.ID, Title: node.Title, Snippet: bodyPreview(node.Body, 220), Score: score, Tags: append([]string(nil), note.Tags...), SourceType: firstNonEmpty(note.Kind, string(node.Kind)), RelevantLink: "community " + formatCommunity(community[node.ID]), Provenance: "EXTRACTED", Confidence: 1, Lens: SearchOverview, Community: community[node.ID]})
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Score != out[j].Score {
			return out[i].Score > out[j].Score
		}
		return out[i].NoteName < out[j].NoteName
	})
	if len(out) > topK {
		out = out[:topK]
	}
	return out
}

func mapValues(values map[string]SearchHit) []SearchHit {
	out := make([]SearchHit, 0, len(values))
	for _, value := range values {
		out = append(out, value)
	}
	return out
}

func bodyPreview(body string, limit int) string {
	body = strings.Join(strings.Fields(strings.TrimSpace(body)), " ")
	if len(body) > limit {
		return body[:limit] + "…"
	}
	return body
}

func formatCommunity(community int) string {
	return "#" + fmtInt(community+1)
}

func fmtInt(value int) string {
	if value == 0 {
		return "0"
	}
	var digits [20]byte
	i := len(digits)
	for value > 0 {
		i--
		digits[i] = byte('0' + value%10)
		value /= 10
	}
	return string(digits[i:])
}
