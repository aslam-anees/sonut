package graphalgo

import "testing"

func TestShortestPathIsStableAndUndirected(t *testing.T) {
	edges := []Edge{{From: "a", To: "b"}, {From: "b", To: "c"}, {From: "a", To: "d"}, {From: "d", To: "c"}}
	got := ShortestPath([]string{"a", "b", "c", "d"}, edges, "c", "a")
	want := []string{"c", "b", "a"}
	for i := range want {
		if i >= len(got) || got[i] != want[i] {
			t.Fatalf("path = %#v, want %#v", got, want)
		}
	}
}
