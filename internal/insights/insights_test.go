package insights

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestEventNeverContainsUserContent is the trust contract: whatever the user
// typed or the model produced, none of it may appear in the uploaded payload.
func TestEventNeverContainsUserContent(t *testing.T) {
	prompt := "fix the SECRET_TOKEN=abc123 leak in /Users/me/private/repo/main.go"
	outcome := "Rewrote credentials handling in main.go; token abc123 rotated."
	ev := BuildEvent(prompt, outcome)

	raw, err := json.Marshal(ev)
	if err != nil {
		t.Fatal(err)
	}
	payload := string(raw)
	for _, leak := range []string{"SECRET_TOKEN", "abc123", "/Users/me", "main.go", "session-xyz-123", "Rewrote"} {
		if strings.Contains(payload, leak) {
			t.Errorf("payload leaked user content %q: %s", leak, payload)
		}
	}
	var fields map[string]any
	if err := json.Unmarshal(raw, &fields); err != nil {
		t.Fatal(err)
	}
	if len(fields) != 6 {
		t.Fatalf("payload fields = %v; adding a field requires a privacy review", fields)
	}
	for _, key := range []string{"p_intent", "p_prompt_size", "p_outcome_size", "p_platform", "p_arch", "p_app_version"} {
		if _, ok := fields[key]; !ok {
			t.Errorf("missing approved payload field %q", key)
		}
	}
}

func TestBuildEventShape(t *testing.T) {
	ev := BuildEvent("please fix the login bug", "done")
	if ev.Intent != "fix" {
		t.Errorf("intent = %q, want fix", ev.Intent)
	}
	if ev.PromptSize != "xs" || ev.OutcomeSize != "xs" {
		t.Errorf("size buckets = %q/%q", ev.PromptSize, ev.OutcomeSize)
	}
}

func TestClassifyIntent(t *testing.T) {
	cases := map[string]string{
		"fix the crash on startup":       "fix",
		"add unit tests for the parser":  "test",
		"refactor the auth module":       "refactor",
		"explain how the sandbox works":  "explain",
		"build a REST API for todos":     "build",
		"release v2 and publish the tag": "release",
		"review my latest changes":       "review",
		"hello there":                    "other",
	}
	for prompt, want := range cases {
		if got := ClassifyIntent(prompt); got != want {
			t.Errorf("ClassifyIntent(%q) = %q, want %q", prompt, got, want)
		}
	}
}

func TestSizeBucket(t *testing.T) {
	cases := map[int]string{0: "empty", 10: "xs", 200: "s", 1500: "m", 9000: "l", 50000: "xl"}
	for n, want := range cases {
		if got := sizeBucket(n); got != want {
			t.Errorf("sizeBucket(%d) = %q, want %q", n, got, want)
		}
	}
}

func TestDisabled(t *testing.T) {
	for _, key := range []string{"ATCODE_TELEMETRY", "SONUT_TELEMETRY", "ATCODE_INSIGHTS", "AT_INSIGHTS", "SONUT_INSIGHTS"} {
		t.Setenv(key, "")
	}
	if !Disabled() {
		t.Error("insights should be disabled by default")
	}
	t.Setenv("ATCODE_TELEMETRY", "1")
	if Disabled() {
		t.Error("ATCODE_TELEMETRY=1 should enable insights")
	}
}

func TestEnabledAliasesRequireAffirmativeValue(t *testing.T) {
	for _, key := range []string{"ATCODE_TELEMETRY", "SONUT_TELEMETRY", "ATCODE_INSIGHTS", "AT_INSIGHTS", "SONUT_INSIGHTS"} {
		t.Setenv(key, "")
	}
	t.Setenv("AT_INSIGHTS", "off")
	if Enabled() {
		t.Error("an explicit off value must not enable insights")
	}
	t.Setenv("AT_INSIGHTS", "")
	t.Setenv("SONUT_INSIGHTS", "yes")
	if !Enabled() {
		t.Error("legacy affirmative alias should enable insights")
	}
	t.Setenv("ATCODE_TELEMETRY", "0")
	if Enabled() {
		t.Error("primary ATCODE_TELEMETRY=0 should override legacy aliases")
	}
}

func TestUploadUsesDailyAggregationRPC(t *testing.T) {
	requests := make(chan *http.Request, 1)
	bodies := make(chan []byte, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		requests <- r.Clone(r.Context())
		bodies <- body
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()

	t.Setenv("SUPABASE_URL", server.URL)
	t.Setenv("SUPABASE_ANON_KEY", "test-anon-key")
	upload(BuildEvent("fix it", "done"), "test-user-token")

	req := <-requests
	if req.URL.Path != "/rest/v1/rpc/record_product_usage" {
		t.Errorf("path = %q", req.URL.Path)
	}
	if got := req.Header.Get("Authorization"); got != "Bearer test-user-token" {
		t.Errorf("Authorization = %q", got)
	}
	if got := req.Header.Get("Prefer"); got != "return=minimal" {
		t.Errorf("Prefer = %q", got)
	}
	body := string(<-bodies)
	for _, forbidden := range []string{"user_id", "session", "fix it", "done"} {
		if strings.Contains(body, forbidden) {
			t.Errorf("RPC body contains forbidden value %q: %s", forbidden, body)
		}
	}
}
