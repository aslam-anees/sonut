package tui

import (
	"reflect"
	"strings"
	"testing"
)

// dataDestinations must map a tool call to the external places its data goes:
// URL hosts anywhere in the arguments (web_fetch, browser navigate, curl in a
// shell line), MCP servers by name, and the web-search provider — while a
// purely local call yields nothing.
func TestDataDestinationsExtraction(t *testing.T) {
	cases := []struct {
		name      string
		tool      string
		arguments string
		want      []string
	}{
		{"web_fetch url", "web_fetch", `{"url":"https://www.airbnb.com/s/homes"}`, []string{"www.airbnb.com"}},
		{"browser navigate", "web_browser", `{"action":"navigate","url":"https://example.org/page"}`, []string{"example.org"}},
		{"curl in shell", "exec_command", `{"command":"curl -Lo out.html https://api.github.com/repos"}`, []string{"api.github.com"}},
		{"dedupes repeated host", "web_fetch", `{"url":"https://a.com/x","fallback":"http://a.com/y"}`, []string{"a.com"}},
		{"mcp tool", "mcp_linear_create_issue", `{"title":"bug"}`, []string{"mcp: linear"}},
		{"web search", "web_search", `{"query":"golang generics"}`, []string{"web search"}},
		{"local tool", "read_file", `{"path":"main.go"}`, nil},
	}
	for _, tc := range cases {
		if got := dataDestinations(tc.tool, tc.arguments); !reflect.DeepEqual(got, tc.want) {
			t.Errorf("%s: dataDestinations(%q) = %v, want %v", tc.name, tc.tool, got, tc.want)
		}
	}
}

// The SAFETY sidebar lists the top destinations by send count, most-used
// first, capped, and deterministic on ties.
func TestSafetyPanelShowsTopDataDestinations(t *testing.T) {
	state := safetyState{}
	for i := 0; i < 3; i++ {
		state.addDestinations([]string{"api.github.com"})
	}
	state.addDestinations([]string{"web search", "www.airbnb.co.in"})
	state.addDestinations([]string{"www.airbnb.co.in"})

	tops := state.topDestinations(2)
	want := []string{"www.airbnb.co.in ×2", "web search ×1"}
	if len(tops) != 2 || tops[0] != "api.github.com ×3" {
		t.Fatalf("topDestinations(2) = %v, want [api.github.com ×3 %s]", tops, want[0])
	}
	if tops[1] != "www.airbnb.co.in ×2" {
		t.Fatalf("second destination = %q, want www.airbnb.co.in ×2", tops[1])
	}

	m := model{}
	m.safetyState = state
	joined := strings.Join(m.renderSafetyLines(40, 5), "\n")
	for _, needle := range []string{"data sent to:", "api.github.com ×3", "www.airbnb.co.in ×2"} {
		if !strings.Contains(joined, needle) {
			t.Fatalf("SAFETY lines missing %q:\n%s", needle, joined)
		}
	}
}
