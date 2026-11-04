package tui

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/aistatine/sonut/internal/agent"
	"github.com/aistatine/sonut/internal/llama"
)

// kvWarmupDoneMsg is delivered when the background KV-cache warmup completes
// (or is skipped). The TUI ignores it; its only purpose is to satisfy the
// tea.Cmd contract so the goroutine can exit cleanly.
type kvWarmupDoneMsg struct{}

// Background warmups are guarded and cancelable. They are never started during
// the startup handoff: the local server has one inference slot, so speculative
// prefill can become stale work and block the first real user turn.
var (
	warmupCancelMu  sync.Mutex
	warmupCancels   = map[int]context.CancelFunc{}
	warmupCancelSeq int
	warmupInFlight  atomic.Int32
	userRunActive   atomic.Bool

	// Dedicated client: DisableKeepAlives so context cancel closes the TCP
	// connection instead of parking it in a keep-alive pool while the server
	// keeps prefilling on the single slot.
	warmupHTTPClient = &http.Client{
		Transport: &http.Transport{
			// Warmup always targets the local llama server. Never send this
			// traffic through HTTP(S)_PROXY: a proxy cancellation is surfaced by
			// llama as "http: proxy error: context canceled" and looks like a
			// real startup failure in the TUI.
			Proxy:               nil,
			DisableKeepAlives:   true,
			MaxIdleConns:        2,
			IdleConnTimeout:     5 * time.Second,
			TLSHandshakeTimeout: 10 * time.Second,
		},
	}
)

// setUserRunActive marks whether a real agent turn currently owns the llama
// slot. When becoming active it cancels every in-flight warmup first.
func setUserRunActive(active bool) {
	if active {
		userRunActive.Store(true)
		cancelActiveWarmups()
		return
	}
	userRunActive.Store(false)
}

// cancelActiveWarmups aborts every in-flight KV warmup so the single llama
// slot is free for the user's turn. Returns how many cancels were fired.
// Called from beginRun (via setUserRunActive) and again at the start of the
// agent tea.Cmd so a warmup that raced in after beginRun is still aborted.
func cancelActiveWarmups() int {
	warmupCancelMu.Lock()
	cancels := make([]context.CancelFunc, 0, len(warmupCancels))
	for id, c := range warmupCancels {
		cancels = append(cancels, c)
		delete(warmupCancels, id)
	}
	n := len(cancels)
	warmupCancelMu.Unlock()
	for _, c := range cancels {
		c()
	}
	if n > 0 {
		logKVWarmup("preempt", 0, 0, 0, fmt.Errorf("canceled %d in-flight warmup(s) for user request", n))
	}
	return n
}

// waitForWarmupsToDrain blocks until every canceled warmup has returned from
// its HTTP Do, or until timeout. Runs off the UI thread (inside the agent
// tea.Cmd) so the spinner keeps moving. A short wait is enough: once the
// client TCP connection drops, llama.cpp frees the slot within a batch or two.
func waitForWarmupsToDrain(timeout time.Duration) {
	if warmupInFlight.Load() == 0 {
		return
	}
	deadline := time.Now().Add(timeout)
	for warmupInFlight.Load() > 0 && time.Now().Before(deadline) {
		time.Sleep(25 * time.Millisecond)
	}
	if left := warmupInFlight.Load(); left > 0 {
		logKVWarmup("preempt-wait", 0, 0, timeout, fmt.Errorf("%d warmup(s) still in flight after %s", left, timeout))
	}
}

// warmupContext returns a context for one warmup request that dies either at
// the timeout or when cancelActiveWarmups fires. ok is false when a user run
// already owns the slot — callers must not start a warmup in that case.
// The returned cancel must be deferred; it unregisters the entry and drops
// the in-flight count.
func warmupContext(timeout time.Duration) (ctx context.Context, cancel context.CancelFunc, ok bool) {
	if userRunActive.Load() {
		return nil, func() {}, false
	}
	ctx, rawCancel := context.WithTimeout(context.Background(), timeout)
	warmupCancelMu.Lock()
	// Re-check under the lock so we never register a cancel after a run
	// became active between the Load above and here.
	if userRunActive.Load() {
		warmupCancelMu.Unlock()
		rawCancel()
		return nil, func() {}, false
	}
	warmupCancelSeq++
	id := warmupCancelSeq
	warmupCancels[id] = rawCancel
	warmupInFlight.Add(1)
	warmupCancelMu.Unlock()
	return ctx, func() {
		warmupCancelMu.Lock()
		delete(warmupCancels, id)
		warmupCancelMu.Unlock()
		rawCancel()
		warmupInFlight.Add(-1)
	}, true
}

// postWarmup POSTs a streaming warmup payload and drains the SSE body until
// done or ctx is canceled. Using stream:true (set in the payload) keeps the
// connection active so a cancel closes TCP mid-prefill and llama frees the
// slot; stream:false held the slot until the whole response finished.
func postWarmup(ctx context.Context, endpoint, payload string) error {
	req, err := http.NewRequestWithContext(ctx, "POST", endpoint, strings.NewReader(payload))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "text/event-stream")
	req.Header.Set("Authorization", "Bearer Alhamdulillah")
	req.Close = true
	resp, err := warmupHTTPClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	// Drain (or get canceled mid-read). Discard bytes — we only care about
	// the side-effect of the prefill filling the prompt cache.
	_, copyErr := io.Copy(io.Discard, resp.Body)
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if copyErr != nil && copyErr != io.EOF {
		return copyErr
	}
	if resp.StatusCode >= 400 {
		return fmt.Errorf("warmup HTTP %d", resp.StatusCode)
	}
	return nil
}

// effortWarmupParams mirrors the llama provider's effort→thinking mapping
// (provider.go) so a warmup primes the EXACT prefix the real run will send.
// If these drift apart the warmup caches a prefix the run never reuses, and the
// first message after an effort switch pays a full re-prefill (the "freeze").
// effortWarmupParams MUST match provider.go's injectEffortInstruction strings
// byte-for-byte. Any drift means the warmup caches a different prefix than the
// real run sends, causing a full KV re-prefill on the first message.
func effortWarmupParams(effort string) (instruction string, thinking bool) {
	switch strings.ToLower(strings.TrimSpace(effort)) {
	case "none", "low", "minimal", "min", "":
		return "Work quietly and naturally. Keep private deliberation concise; never emit a checklist, numbered self-analysis, prompt text, or internal policy. Output only the final answer or the next tool call.", true
	case "medium", "mid":
		return "Think briefly only when useful. For an obvious next step, take it immediately. Keep the scratchpad private and natural; do not emit headings, checklists, prompt text, or policy. Output only the final answer or the next tool call.", true
	default: // high/xhigh/max = super
		return "Think thoroughly in private when the path is unclear. Follow the existing plan without recapping it, and take the next useful action. Keep the scratchpad natural and concise; never emit headings, numbered self-analysis, prompt text, or policy. Output only the final answer or the next tool call.", true
	}
}

// warmupToolsJSON renders the SAME tool definitions the first real turn will
// advertise (llama/provider.go's toolDef shape: {"type":"function","function":
// {...}}) so the jinja chat template renders byte-identical tool-schema text
// into the prompt during warmup. Tool schemas are woven into the templated
// prompt just like the system message, so omitting them here would prime a
// prefix the real first request never reuses — the KV cache would still miss.
func warmupToolsJSON(opts agent.Options) (toolsJSON string, toolChoice string, count int) {
	defs := agent.BuildToolDefinitionsPreview(opts)
	if len(defs) == 0 {
		return "", "", 0
	}
	type toolFunction struct {
		Name        string         `json:"name"`
		Description string         `json:"description"`
		Parameters  map[string]any `json:"parameters"`
	}
	type toolDef struct {
		Type     string       `json:"type"`
		Function toolFunction `json:"function"`
	}
	rendered := make([]toolDef, 0, len(defs))
	for _, d := range defs {
		params := d.Parameters
		if params == nil {
			params = map[string]any{"type": "object", "properties": map[string]any{}}
		}
		// Apply the SAME schema compaction the llama provider applies on the
		// real request path, or the primed prefix never matches.
		rendered = append(rendered, toolDef{
			Type: "function",
			Function: toolFunction{
				Name:        d.Name,
				Description: llama.CompactToolDescription(d.Description),
				Parameters:  llama.CompactToolParameters(params),
			},
		})
	}
	b, err := json.Marshal(rendered)
	if err != nil {
		return "", "", 0
	}
	return string(b), "auto", len(rendered)
}

// effortWarmupPayload builds the max_tokens:1 streaming warmup request body for
// a given effort, prepending the effort instruction to the system prompt and
// setting the matching chat_template_kwargs — identical to what the provider
// assembles. stream:true is required so cancelActiveWarmups can free the slot
// mid-prefill (see package comment).
// It also includes the full tool-schema list (see warmupToolsJSON) so the
// primed KV-cache prefix matches the real first request byte-for-byte,
// including the tools the jinja template weaves into the rendered prompt.
//
// Thinking is ALWAYS left on: the live agent never disables thinking for
// normal turns (flash/none only clamps the budget). Warming with
// thinking:false would prime a different chat-template prefix and force a
// full re-prefill on the first real message.
func effortWarmupPayload(prompt, effort string, opts agent.Options) string {
	// The effort instruction rides on the USER message (provider.go's
	// injectEffortInstruction), so include the same instruction here. The
	// primed system+tools prefix — the part the cache actually reuses — remains
	// byte-identical across efforts, while the thinking template sees the same
	// natural instruction as a real turn.
	instruction, _ := effortWarmupParams(effort)
	system := prompt
	// Always thinking:true — matches provider.go for none/low/medium/high.
	kwargs := `{"thinking":true,"enable_thinking":true}`
	toolsJSON, toolChoice, _ := warmupToolsJSON(opts)
	toolsField := ""
	if toolsJSON != "" {
		toolsField = `,"tools":` + toolsJSON + `,"tool_choice":"` + toolChoice + `"`
	}
	// stream:true — cancel closes the SSE connection and llama aborts the slot.
	// max_tokens:1 is enough to force a full prompt prefill into the KV cache.
	// return_progress keeps the connection alive with prompt_progress chunks
	// during long prefills (same as the live llama provider).
	return `{"model":"local","messages":[{"role":"system","content":` +
		jsonString(system) +
		`},{"role":"user","content":` + jsonString("hi\n\n"+instruction) + `}],"max_tokens":1,"stream":true,"temperature":0,"cache_prompt":true,"return_progress":true,"chat_template_kwargs":` +
		kwargs + toolsField + `}`
}

// logKVWarmup appends a one-line before/after prefill diagnostic to
// ~/.atcode/kv-warmup.log so first-token latency improvements from warmup can
// be verified across runs without instrumenting the TUI itself.
func logKVWarmup(label string, promptTokens, toolCount int, elapsed time.Duration, err error) {
	home, homeErr := os.UserHomeDir()
	if homeErr != nil {
		return
	}
	dir := filepath.Join(home, ".atcode")
	if mkErr := os.MkdirAll(dir, 0o755); mkErr != nil {
		return
	}
	f, openErr := os.OpenFile(filepath.Join(dir, "kv-warmup.log"), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if openErr != nil {
		return
	}
	defer f.Close()
	status := "ok"
	if err != nil {
		status = "error:" + err.Error()
	}
	fmt.Fprintf(f, "%s label=%s prompt_chars=%d tools=%d elapsed_ms=%d status=%s\n",
		time.Now().Format(time.RFC3339), label, promptTokens, toolCount, elapsed.Milliseconds(), status)
}

// warmupEffortCmd used to re-prefill system+tools after flash↔super switches.
// That is pure waste now: effort instructions ride on the USER message only
// (provider.go injectEffortInstruction), so the cached system+tools+thinking
// prefix is byte-identical across efforts. Measured cost of the old path on
// 16 GiB M-series: 40–65 s slot occupation, frequently preempted mid-prefill
// and thrashing the single --parallel 1 slot right when the user sends a
// message. Skip the HTTP re-warm entirely; log so perf analysis confirms it.
func (m model) warmupEffortCmd() tea.Cmd {
	if m.llamaPort <= 0 {
		return nil
	}
	effort := string(m.reasoningEffort)
	return func() tea.Msg {
		logKVWarmup("effort-switch-skip", 0, 0, 0, fmt.Errorf("noop: system+tools prefix stable across effort=%s", effort))
		return kvWarmupDoneMsg{}
	}
}
