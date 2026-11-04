// Package insights uploads privacy-preserving product metrics to Supabase.
//
// Trust contract (do not weaken):
//   - NEVER upload prompt text, model output, file contents, file paths, or
//     anything from the local knowledge store. Only derived, aggregate signals
//     leave the machine: coarse intent category, size buckets, platform,
//     version.
//   - Uploads require explicit opt-in via ATCODE_TELEMETRY=1. Signing in is not
//     consent to product analytics.
//   - Uploads happen only for signed-in users (their own Supabase account) and
//     are attributed via their own access token under row-level security.
//   - Supabase stores at most one aggregate row per user per UTC day. No local
//     session identifier or stable device identifier is uploaded.
//   - Everything is best-effort and asynchronous: a failed or slow upload can
//     never block, break, or slow a session.
//   - /hide session suppresses insights for that session too (the caller gates
//     on the same flag as knowledge capture).
package insights

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/aistatine/sonut/internal/auth"
	"github.com/aistatine/sonut/internal/config"
)

// Event is the exact RPC payload uploaded for one completed run. Supabase
// merges it into the signed-in user's daily aggregate rather than storing one
// row per run. Keep every field defensible as "not user content".
type Event struct {
	Intent      string `json:"p_intent"`
	PromptSize  string `json:"p_prompt_size"`
	OutcomeSize string `json:"p_outcome_size"`
	Platform    string `json:"p_platform"`
	Arch        string `json:"p_arch"`
	AppVersion  string `json:"p_app_version"`
}

// Version is stamped by the caller (cli embeds the release version); "dev"
// builds are still counted so internal usage is visible too.
var Version = "dev"

const uploadTimeout = 5 * time.Second

// Enabled reports whether the user explicitly opted in to privacy-preserving
// insights. Older aliases remain accepted for users who already configured
// them, but the first configured value wins and no unset value enables uploads.
func Enabled() bool {
	for _, key := range []string{"ATCODE_TELEMETRY", "SONUT_TELEMETRY", "ATCODE_INSIGHTS", "AT_INSIGHTS", "SONUT_INSIGHTS"} {
		value, configured := os.LookupEnv(key)
		if !configured || strings.TrimSpace(value) == "" {
			continue
		}
		switch strings.ToLower(strings.TrimSpace(value)) {
		case "1", "on", "true", "yes":
			return true
		default:
			return false
		}
	}
	return false
}

// Disabled is retained for callers that prefer an opt-out-shaped check.
func Disabled() bool {
	return !Enabled()
}

// BuildEvent distills a finished run into a privacy-preserving event. It is a pure
// function so tests can assert that no raw user content survives.
func BuildEvent(prompt, outcome string) Event {
	return Event{
		Intent:      ClassifyIntent(prompt),
		PromptSize:  sizeBucket(len(prompt)),
		OutcomeSize: sizeBucket(len(outcome)),
		Platform:    runtime.GOOS,
		Arch:        runtime.GOARCH,
		AppVersion:  Version,
	}
}

// RecordRun uploads one privacy-preserving run signal. Call it from a background
// goroutine/command; it never panics and never returns an error — insights
// must be invisible to the user.
func RecordRun(prompt, outcome string) {
	defer func() { _ = recover() }()
	if !Enabled() {
		return
	}
	token := accessToken()
	if token == "" {
		// Not signed in: no account relationship, so nothing leaves the machine.
		return
	}
	upload(BuildEvent(prompt, outcome), token)
}

// ClassifyIntent maps a prompt to a coarse usage category without retaining
// any of its text. Categories are ordered by specificity.
func ClassifyIntent(prompt string) string {
	p := strings.ToLower(prompt)
	switch {
	case containsAny(p, "fix", "fixes", "fixing", "bug", "bugs", "error", "errors", "broken", "crash", "fail", "failing", "fails"):
		return "fix"
	case containsAny(p, "test", "tests", "testing", "coverage", "assert"):
		return "test"
	case containsAny(p, "refactor", "clean up", "cleanup", "rename", "restructure"):
		return "refactor"
	case containsAny(p, "explain", "what is", "what does", "how does", "why", "understand"):
		return "explain"
	case containsAny(p, "build", "create", "add", "implement", "make", "write", "new "):
		return "build"
	case containsAny(p, "deploy", "release", "publish", "ship"):
		return "release"
	case containsAny(p, "review", "audit", "check"):
		return "review"
	default:
		return "other"
	}
}

// containsAny matches multi-word phrases as substrings and single keywords as
// whole words, so "latest" never counts as "test".
func containsAny(s string, subs ...string) bool {
	var words map[string]bool
	for _, sub := range subs {
		if strings.ContainsAny(sub, " ") {
			if strings.Contains(s, sub) {
				return true
			}
			continue
		}
		if words == nil {
			words = make(map[string]bool)
			for _, w := range strings.FieldsFunc(s, func(r rune) bool {
				return !('a' <= r && r <= 'z' || '0' <= r && r <= '9')
			}) {
				words[w] = true
			}
		}
		if words[sub] {
			return true
		}
	}
	return false
}

// sizeBucket coarsens a byte length so even message sizes are not precise
// fingerprints of user activity.
func sizeBucket(n int) string {
	switch {
	case n == 0:
		return "empty"
	case n <= 80:
		return "xs"
	case n <= 400:
		return "s"
	case n <= 2000:
		return "m"
	case n <= 10000:
		return "l"
	default:
		return "xl"
	}
}

// accessToken loads the signed-in user's Supabase access token, refusing
// expired tokens rather than uploading unauthenticated.
func accessToken() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	storage := auth.NewTokenStorage(filepath.Join(home, ".atcode"))
	session, err := storage.LoadSession()
	if err != nil || session == nil || session.AccessToken == "" {
		return ""
	}
	if !session.ExpiresAt.IsZero() && time.Now().After(session.ExpiresAt) {
		return ""
	}
	return session.AccessToken
}

// upload POSTs the event to a narrowly scoped Supabase RPC. The function takes
// no user ID: auth.uid() supplies it server-side, validates every category, and
// atomically increments one daily aggregate row.
func upload(ev Event, token string) {
	cfg := config.Load()
	if cfg.SupabaseURL == "" || cfg.SupabaseKey == "" {
		return
	}
	body, err := json.Marshal(ev)
	if err != nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), uploadTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		strings.TrimRight(cfg.SupabaseURL, "/")+"/rest/v1/rpc/record_product_usage", bytes.NewReader(body))
	if err != nil {
		return
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("apikey", cfg.SupabaseKey)
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Prefer", "return=minimal")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return
	}
	_ = resp.Body.Close()
}
