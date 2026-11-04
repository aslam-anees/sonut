package knowledge

import (
	"os"
	"path/filepath"
	"testing"
)

func TestSearchLensConnectedLabelsGraphExpansion(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("SONUT_KNOWLEDGE_DIR", dir)
	for name, body := range map[string]string{
		"alpha.md": "# Alpha\n\nAlpha explains the local atlas and links to [[Beta]].\n",
		"beta.md":  "# Beta\n\nBeta contains the implementation details.\n",
	} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	backend, err := NewSearchBackend()
	if err != nil {
		t.Fatal(err)
	}
	notes, err := LoadNotes()
	if err != nil {
		t.Fatal(err)
	}
	hits, err := backend.SearchLens("atlas", BuildAtlasGraph(notes), notes, SearchConnected, 8)
	if err != nil || len(hits) < 2 {
		t.Fatalf("connected search hits = %#v, err=%v", hits, err)
	}
	var sawInferred bool
	for _, hit := range hits {
		if hit.NoteName == "beta" && hit.Provenance == "INFERRED" && hit.Confidence < 1 {
			sawInferred = true
		}
	}
	if !sawInferred {
		t.Fatalf("connected expansion did not label inferred neighbor: %#v", hits)
	}
}
