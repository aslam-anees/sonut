package sonutruntime

import (
	"context"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
)

// Default caps on a single streamed tool call's argument generation. A model
// that runs away generating an enormous write_file payload (observed: 11+
// minutes at 4B speeds for a 174-line file) would otherwise stall the turn
// indefinitely with no feedback. ~4 chars/token is a rough estimate, so 16K
// tokens is approximated as 64K bytes of raw JSON argument text.
//
// A tool call is not productive until its JSON is complete and dispatched. An
// active token trickle must therefore not bypass the safety budget: a malformed
// write previously displayed a few lines while consuming forty minutes of GPU
// time. Large files are written in bounded chunks (write_file + append_file),
// so a short per-call deadline gives the model a recovery path instead of
// letting a single call monopolize the session.
const (
	defaultMaxToolArgBytes = 64 * 1024
	// write_file/append_file arguments ARE the product. A complete HTML/JS game
	// (JSON-escaped) routinely exceeds 64–256 KiB. Aborting mid-file shows the
	// user "Large file write stopped safely" and used to discard or thrash the
	// salvage. Prefer a high ceiling so healthy local generation finishes in one
	// call; line-stall + idle progress still bound dead/looping generation.
	// When the cap is still hit, the agent loop auto-saves complete lines and
	// forces append continuation so content is never lost or regenerated.
	defaultMaxFileToolArgBytes = 768 * 1024
	defaultMaxToolArgDuration  = 90 * time.Second
	// File content is real model output, not a hung tool call. At the measured
	// 12–18 tok/s of the bundled local model, a useful 6–10k-token source file
	// needs several minutes. The previous 5-minute hard kill aborted healthy
	// progressing writes mid-file (live: decode_ms=300000 → partial salvage →
	// multi-hour rewrite thrash). File calls still have byte + stall + line-stall
	// caps; the absolute deadline is a last-resort only for dead/looping generation.
	defaultMaxFileToolArgDuration = 20 * time.Minute
)

// resolveMaxToolArgDuration applies the explicit collector value, then the
// SONUT_TOOL_ARG_TIMEOUT override, then the production default. This keeps the
// default bounded while allowing deliberately slower local hardware to opt in
// to a longer budget without a source change.
func resolveMaxToolArgDuration(configured time.Duration) time.Duration {
	if configured > 0 {
		return configured
	}
	if raw := os.Getenv("SONUT_TOOL_ARG_TIMEOUT"); raw != "" {
		if parsed, err := time.ParseDuration(strings.TrimSpace(raw)); err == nil && parsed > 0 {
			return parsed
		}
	}
	return defaultMaxToolArgDuration
}

func resolveMaxFileToolArgDuration(configured time.Duration) time.Duration {
	if configured > 0 {
		return configured
	}
	if raw := os.Getenv("SONUT_FILE_TOOL_ARG_TIMEOUT"); raw != "" {
		if parsed, err := time.ParseDuration(strings.TrimSpace(raw)); err == nil && parsed > 0 {
			return parsed
		}
	}
	// The existing global override remains authoritative when supplied.
	if raw := os.Getenv("SONUT_TOOL_ARG_TIMEOUT"); raw != "" {
		if parsed, err := time.ParseDuration(strings.TrimSpace(raw)); err == nil && parsed > 0 {
			return parsed
		}
	}
	return defaultMaxFileToolArgDuration
}

// MaxFileToolArgDuration is the production idle/absolute ceiling for
// write_file/append_file argument generation. The agent loop's outer
// generation watchdog uses this so a healthy multi-minute local file write
// is not cancelled by the short text-generation budget.
func MaxFileToolArgDuration() time.Duration {
	return resolveMaxFileToolArgDuration(0)
}

// defaultToolArgStallTimeout aborts an in-flight tool call when NO argument
// fragment has arrived for this long — the actual dead-generation signal. A
// progressing call is never killed by time. Generous by design for local
// inference: llama.cpp's --jinja tool-call parser buffers argument fragments
// until they parse, and a swapping 16 GiB machine pauses generation for tens
// of seconds while staying healthy (observed: 20s killed a mid-flight
// write_file that was progressing fine). SONUT_TOOL_ARG_STALL_TIMEOUT
// overrides it.
const defaultToolArgStallTimeout = 75 * time.Second

// resolveToolArgStall returns the tool-arg stall window: the
// SONUT_TOOL_ARG_STALL_TIMEOUT env override when set, else the default.
func resolveToolArgStall() time.Duration {
	if raw := os.Getenv("SONUT_TOOL_ARG_STALL_TIMEOUT"); raw != "" {
		if parsed, err := time.ParseDuration(raw); err == nil && parsed > 0 {
			return parsed
		}
	}
	return defaultToolArgStallTimeout
}

// defaultFileLineStallTimeout detects a different failure from transport
// silence: the model is still emitting argument bytes/tokens, but a generated
// source file has stopped completing new lines. This is typically a malformed
// JSON string or a model loop inside one enormous line. Treating raw bytes as
// progress let the UI freeze at (for example) 62 lines for five minutes while
// thousands of unusable tokens accumulated. Healthy multi-line source usually
// completes a line well inside this window; long single-line JS/CSS still
// advances contentLen, and absolute/byte caps remain the backstop. 90s avoids
// killing slow local models mid-function while still catching true freezes.
const defaultFileLineStallTimeout = 90 * time.Second

func resolveFileLineStall() time.Duration {
	if raw := os.Getenv("SONUT_FILE_LINE_STALL_TIMEOUT"); raw != "" {
		if parsed, err := time.ParseDuration(strings.TrimSpace(raw)); err == nil && parsed > 0 {
			return parsed
		}
	}
	return defaultFileLineStallTimeout
}

// defaultStreamSilenceTimeout abandons a stream over which NO event of any
// kind has arrived for this long — the dead-air signal the other watchdogs
// can't see: they all key off events (tool-arg fragments, reasoning deltas),
// so a stream that never produces its FIRST token (dead pooled connection,
// hung prefill, provider outage) previously hung the turn forever while the
// TUI promised an auto-recovery that had no implementation. The error message
// matches the agent loop's isStreamTimeoutError needles ("no output for"), so
// the turn is safely reissued with backoff. Generous by design: a huge local
// prefill can legitimately be silent for a while — SONUT_STREAM_IDLE_TIMEOUT
// overrides it for slower rigs.
const defaultStreamSilenceTimeout = 120 * time.Second

// resolveStreamSilence returns the whole-stream silence budget: the explicit
// option, else the SONUT_STREAM_IDLE_TIMEOUT env override, else the default.
func resolveStreamSilence(configured time.Duration) time.Duration {
	if configured > 0 {
		return configured
	}
	if raw := os.Getenv("SONUT_STREAM_IDLE_TIMEOUT"); raw != "" {
		if parsed, err := time.ParseDuration(raw); err == nil && parsed > 0 {
			return parsed
		}
	}
	return defaultStreamSilenceTimeout
}

// defaultPrefillTimeout is an absolute ceiling for the prompt-processing
// phase. A llama server can emit prompt-progress heartbeats often enough to
// keep the ordinary silence watchdog alive while making no first-token
// progress; without an independent ceiling that recreates the old multi-
// minute "↑ 0 tok" hang. This allows one minute for slow local Macs,
// and can be raised for unusually large prompts with SONUT_PREFILL_TIMEOUT.
const defaultPrefillTimeout = 60 * time.Second

func resolvePrefillTimeout(configured time.Duration) time.Duration {
	if configured > 0 {
		return configured
	}
	if raw := os.Getenv("SONUT_PREFILL_TIMEOUT"); raw != "" {
		if parsed, err := time.ParseDuration(strings.TrimSpace(raw)); err == nil && parsed > 0 {
			return parsed
		}
	}
	return defaultPrefillTimeout
}

// edit_file shares the durable file ceiling with write_file/append_file. The
// tool description still asks the model to use surgical 1-3 line edits, but a
// local model occasionally emits a larger valid replacement. Aborting that
// stream at 4 KiB wasted minutes and forced the model to replay a stale anchor;
// the file ceiling plus the semantic watchdogs already provide the safe bound.
const editFileMaxArgBytes = defaultMaxFileToolArgBytes

// exec_command likewise: a correct command is one short line. Anything past
// this cap is an inline heredoc script (observed: 201s generating a python
// <<'EOF' one-liner) — scripts belong in write_file + run.
const execCommandMaxArgBytes = 6 * 1024

// Default caps on a turn's REASONING phase. Thinking that is actively
// streaming tokens is healthy work and must never be killed by wall-clock
// alone — that was the "stream stalled: model reasoning exceeded 512 bytes /
// 8s without acting" failure mode on local hybrid (live: 605-char think
// aborted mid-sentence → multi-minute idle retries).
//
// MaxReasoningBytes is a spiral BACKSTOP only (endless pure-reasoning with
// no text/tool). MaxReasoningDuration is an IDLE budget: how long reasoning
// may go silent without new tokens before we treat the stream as stuck.
// Active token flow resets the idle clock. Both are env-overridable.
const (
	defaultMaxReasoningBytes    = 12288 // ~12 KB spiral ceiling (~3k tokens)
	defaultMaxReasoningDuration = 90 * time.Second
)

// resolveMaxReasoningBytes / resolveMaxReasoningDuration apply the explicit
// CollectOptions value, else the SONUT_MAX_REASONING_* env override, else the
// default — matching the resolve pattern used by the stall/silence timeouts.
func resolveMaxReasoningBytes(configured int) int {
	if configured > 0 {
		return configured
	}
	if raw := os.Getenv("SONUT_MAX_REASONING_BYTES"); raw != "" {
		if parsed, err := strconv.Atoi(strings.TrimSpace(raw)); err == nil && parsed > 0 {
			return parsed
		}
	}
	return defaultMaxReasoningBytes
}

func resolveMaxReasoningDuration(configured time.Duration) time.Duration {
	if configured > 0 {
		return configured
	}
	if raw := os.Getenv("SONUT_MAX_REASONING_DURATION"); raw != "" {
		if parsed, err := time.ParseDuration(strings.TrimSpace(raw)); err == nil && parsed > 0 {
			return parsed
		}
	}
	return defaultMaxReasoningDuration
}

// CollectedStream is the non-streaming summary of provider events.
type CollectedStream struct {
	Text             string
	ToolCalls        []ToolCall
	Usage            Usage
	Error            string
	DroppedToolCalls int // malformed tool calls the provider could not dispatch
	// FinishReason is the provider's normalized terminal stop reason when the
	// response did not end normally (FinishReasonLength / FinishReasonContentFilter).
	// It is empty for a normal completion. Truncated reports whether it is set.
	FinishReason string
	// ReasoningBlocks are the response's preserved reasoning artifacts (Anthropic
	// thinking blocks) that must be replayed on the next turn. Empty for providers
	// or runs without extended thinking.
	ReasoningBlocks []ReasoningBlock
}

// Truncated reports whether the response ended for a non-normal reason (the
// output was cut at the token cap or withheld by a content filter), so callers
// can warn instead of treating a clipped answer as complete.
func (collected CollectedStream) Truncated() bool {
	return collected.FinishReason != ""
}

// CollectOptions provides callbacks for consumers that need live stream updates.
type CollectOptions struct {
	OnText      func(string)
	OnReasoning func(string)
	OnUsage     func(Usage)
	// OnToolCallStart fires when a tool call opens (id + tool name), and
	// OnToolCallDelta fires for each streamed argument fragment. Together they let
	// a surface render a tool call's arguments LIVE (e.g. a file being written)
	// instead of waiting for the whole call to accumulate. nil is a no-op.
	OnToolCallStart func(id, name string)
	OnToolCallDelta func(id, fragment string)

	// MaxToolArgBytes caps a single tool call's argument size; non-positive
	// falls back to defaultMaxToolArgBytes. MaxToolArgDuration is an optional
	// absolute wall-clock ceiling — disabled by default because it kills slow
	// but progressing local generations (the stall watchdog handles dead ones).
	// Once a cap is exceeded the call is marked ToolCall.Aborted with an
	// explanatory AbortReason instead of being dispatched with a (likely still
	// incomplete) giant payload.
	MaxToolArgBytes    int
	MaxToolArgDuration time.Duration

	// MaxReasoningBytes caps total pure-reasoning size before any text/tool
	// output (spiral backstop). MaxReasoningDuration is an IDLE budget only —
	// time since the last reasoning token, not wall-clock from first think.
	// Active thinking that is still streaming tokens is never killed by
	// duration; only true silence mid-think (or an enormous spiral by bytes)
	// aborts so the agent can retry. Non-positive values use package defaults.
	MaxReasoningBytes    int
	MaxReasoningDuration time.Duration

	// MaxStreamSilence abandons the stream when NO event of any kind arrives
	// for this long (dead connection / hung prefill / provider outage — the
	// "0 tokens, stuck before generating" case). Non-positive falls back to
	// defaultStreamSilenceTimeout (overridable via SONUT_STREAM_IDLE_TIMEOUT).
	// The resulting error is a stream-timeout the agent loop safely reissues.
	MaxStreamSilence time.Duration

	// MaxPrefillDuration bounds the absolute prompt-processing phase after the
	// provider starts emitting prefill-progress pings. It is independent of
	// MaxStreamSilence: heartbeats prove the connection is alive, but they must
	// not allow a stalled/ thrashing prefill to run forever. Non-positive values
	// use the production default (or SONUT_PREFILL_TIMEOUT).
	MaxPrefillDuration time.Duration

	// OnPing fires for each StreamEventPing (prefill-progress heartbeat). It
	// lets the agent loop treat a progressing prefill as liveness (e.g. re-arm
	// the pre-output generation budget) without any content side effects. It
	// receives prefill progress (processed/total tokens).
	OnPing func(processed, total int)
}

// SeedMessages creates the initial system and user turns for a request. It is a
// text-only convenience that delegates to SeedMessagesWithImages with no images
// (the user turn's Images stays nil, byte-identical to the prior behavior).
func SeedMessages(systemPrompt string, userPrompt string) []Message {
	return SeedMessagesWithImages(systemPrompt, userPrompt, nil)
}

// SeedMessagesWithImages creates the initial system and user turns and attaches
// any image attachments to the user turn. images may be nil (text-only). The
// images are deep-copied so the seeded message never aliases the caller's slice
// or the underlying Data bytes (a later mutation of the caller's bytes can never
// reach into the conversation history).
func SeedMessagesWithImages(systemPrompt string, userPrompt string, images []ImageBlock) []Message {
	return []Message{
		{Role: MessageRoleSystem, Content: systemPrompt},
		{Role: MessageRoleUser, Content: userPrompt, Images: CloneImageBlocks(images)},
	}
}

// CloneImageBlocks deep-copies a slice of ImageBlock, including each Data byte
// slice, so the returned blocks share no backing array with the input. It
// returns nil for a nil or empty input (preserving the text-only "no images"
// representation). Use it wherever image-carrying messages are seeded or copied
// so raw image bytes are never aliased across history/request/result copies.
func CloneImageBlocks(in []ImageBlock) []ImageBlock {
	if len(in) == 0 {
		return nil
	}
	out := make([]ImageBlock, len(in))
	for index, block := range in {
		out[index] = ImageBlock{
			MediaType: block.MediaType,
			Data:      append([]byte(nil), block.Data...),
		}
	}
	return out
}

// CollectStream drains provider events into text, tool calls, usage, and error state.
func CollectStream(ctx context.Context, events <-chan StreamEvent) CollectedStream {
	return CollectStreamWithOptions(ctx, events, CollectOptions{})
}

// CollectStreamWithOptions drains provider events and emits optional live callbacks.
func CollectStreamWithOptions(ctx context.Context, events <-chan StreamEvent, options CollectOptions) CollectedStream {
	collected := CollectedStream{}
	maxBytes := options.MaxToolArgBytes
	if maxBytes <= 0 {
		maxBytes = defaultMaxToolArgBytes
	}
	maxDuration := resolveMaxToolArgDuration(options.MaxToolArgDuration)
	collector := newToolCallCollector(maxBytes, maxDuration)
	collector.maxFileArgDuration = resolveMaxFileToolArgDuration(options.MaxToolArgDuration)
	maxReasoningBytes := resolveMaxReasoningBytes(options.MaxReasoningBytes)
	maxReasoningDuration := resolveMaxReasoningDuration(options.MaxReasoningDuration)
	reasoningBytes := 0
	var lastReasoningAt time.Time
	usageSeen := false
	finish := func() CollectedStream {
		collector.flush(&collected)
		if usageSeen && options.OnUsage != nil {
			options.OnUsage(collected.Usage)
		}
		return collected
	}

	// Stall watchdog: a dead generation stops emitting events entirely, so no
	// per-event check can catch it. This ticker aborts an in-flight tool call
	// whose argument fragments stopped arriving (progress-aware — a slow but
	// still-streaming call is never killed), abandons a stream that has gone
	// completely silent (including never producing a FIRST event), and abandons
	// pure-reasoning that went idle (no new think tokens) without acting.
	maxSilence := resolveStreamSilence(options.MaxStreamSilence)
	maxPrefill := resolvePrefillTimeout(options.MaxPrefillDuration)
	collector.fileLineStall = resolveFileLineStall()
	// The tick is clamped below the silence budget so a sub-5s budget (tests,
	// aggressive configs) is still honored promptly.
	tick := 5 * time.Second
	if maxSilence < tick {
		tick = maxSilence
	}
	if maxReasoningDuration > 0 && maxReasoningDuration < tick {
		tick = maxReasoningDuration
	}
	if maxPrefill > 0 && maxPrefill < tick {
		tick = maxPrefill
	}
	if collector.fileLineStall > 0 && collector.fileLineStall < tick {
		tick = collector.fileLineStall
	}
	stallCheck := time.NewTicker(tick)
	defer stallCheck.Stop()
	lastEventAt := time.Now()
	var prefillStartedAt time.Time
	toolArgStall := resolveToolArgStall()

	for {
		select {
		case <-ctx.Done():
			collected.Error = ctx.Err().Error()
			return finish()
		case <-stallCheck.C:
			if collector.markStalled(toolArgStall) {
				// The abort reason rides on the call itself (same as the byte-cap
				// path) so the agent loop feeds it back to the model as guidance.
				return finish()
			}
			if time.Since(lastEventAt) > maxSilence {
				// Dead air across the whole stream. The wording matches the agent
				// loop's stream-timeout needles so the turn is reissued (with
				// backoff) instead of hanging until the user gives up.
				collected.Error = fmt.Sprintf("stream stalled: no output for %s — the connection or generation went silent", maxSilence.Round(time.Second))
				return finish()
			}
			if !prefillStartedAt.IsZero() && time.Since(prefillStartedAt) > maxPrefill {
				collected.Error = fmt.Sprintf("stream stalled: prompt prefill exceeded %s before the first token", maxPrefill.Round(time.Second))
				return finish()
			}
			// Pure-reasoning IDLE (not wall-clock think time): model started
			// thinking, then produced no more tokens for maxReasoningDuration
			// without ever acting. Active think streams never hit this path
			// because each reasoning event refreshes lastReasoningAt.
			if collector.text.Len() == 0 && len(collector.calls) == 0 &&
				!lastReasoningAt.IsZero() &&
				time.Since(lastReasoningAt) > maxReasoningDuration {
				collected.Error = fmt.Sprintf("stream stalled: model reasoning exceeded %s without acting", maxReasoningDuration)
				return finish()
			}
		case event, ok := <-events:
			if !ok {
				return finish()
			}
			lastEventAt = time.Now()
			if event.Type == StreamEventPing {
				if prefillStartedAt.IsZero() {
					prefillStartedAt = lastEventAt
				}
			} else {
				// Any non-heartbeat event means prompt processing has handed off
				// to generation (or terminated), so the prefill wall clock no
				// longer applies.
				prefillStartedAt = time.Time{}
			}

			// A non-normal terminal stop reason can ride on any event (providers
			// attach it to their done/terminal event). Record it regardless of
			// type so a truncated/filtered response is never mistaken for a
			// normal completion.
			if event.FinishReason != "" {
				collected.FinishReason = event.FinishReason
			}
			// Reasoning blocks (Anthropic thinking) can ride on any terminal event;
			// accumulate them regardless of type so they survive for replay.
			if len(event.ReasoningBlocks) > 0 {
				collected.ReasoningBlocks = append(collected.ReasoningBlocks, event.ReasoningBlocks...)
			}

			switch event.Type {
			case StreamEventText:
				// Accumulate in a Builder rather than `collected.Text +=`, which
				// reallocated the whole string on every chunk (O(n^2) over a long
				// streamed response). flush materializes it into collected.Text.
				collector.text.WriteString(event.Content)
				if options.OnText != nil {
					options.OnText(event.Content)
				}
			case StreamEventReasoning:
				if options.OnReasoning != nil {
					options.OnReasoning(event.Content)
				}
				lastReasoningAt = time.Now()
				reasoningBytes += len(event.Content)
				// Byte spiral only: endless pure-reasoning with no text/tool.
				// Duration is checked on the ticker (idle), never here — so a
				// slow local model that is still streaming think tokens is kept.
				if collector.text.Len() == 0 && len(collector.calls) == 0 {
					if reasoningBytes > maxReasoningBytes {
						collected.Error = fmt.Sprintf("stream stalled: model reasoning exceeded %d bytes without acting", maxReasoningBytes)
						return finish()
					}
				}
			case StreamEventToolCallStart:
				collector.start(event.ToolCallID, event.ToolName, event.ToolCallSignature)
				if options.OnToolCallStart != nil {
					options.OnToolCallStart(event.ToolCallID, event.ToolName)
				}
			case StreamEventToolCallDelta:
				collector.delta(event.ToolCallID, event.ArgumentsFragment)
				if options.OnToolCallDelta != nil {
					options.OnToolCallDelta(event.ToolCallID, event.ArgumentsFragment)
				}
				if collector.runaway {
					// The call is already marked Aborted with its AbortReason; stop
					// draining so the loop can feed that back to the model NOW rather
					// than after minutes more of dead generation.
					return finish()
				}
			case StreamEventToolCallEnd:
				collector.end(event.ToolCallID)
			case StreamEventToolCallDropped:
				collected.DroppedToolCalls++
			case StreamEventUsage:
				collected.Usage = mergeUsageSnapshot(collected.Usage, event.Usage)
				usageSeen = true
			case StreamEventPing:
				// Liveness heartbeat only — lastEventAt was already reset above,
				// which is the whole point (a progressing prefill is not dead air).
				if options.OnPing != nil {
					options.OnPing(event.PrefillProcessed, event.PrefillTotal)
				}
			case StreamEventError:
				collected.Error = event.Error
				return finish()
			case StreamEventDone:
				return finish()
			}
		}
	}
}

func mergeUsageSnapshot(left Usage, right Usage) Usage {
	inputTokens := left.EffectiveInputTokens()
	if value := right.EffectiveInputTokens(); value != 0 {
		inputTokens = value
	}

	outputTokens := left.EffectiveOutputTokens()
	if value := right.EffectiveOutputTokens(); value != 0 {
		outputTokens = value
	}

	cachedInputTokens := left.CachedInputTokens
	if right.CachedInputTokens != 0 {
		cachedInputTokens = right.CachedInputTokens
	}

	cacheWriteTokens := left.CacheWriteTokens
	if right.CacheWriteTokens != 0 {
		cacheWriteTokens = right.CacheWriteTokens
	}

	reasoningTokens := left.ReasoningTokens
	if right.ReasoningTokens != 0 {
		reasoningTokens = right.ReasoningTokens
	}

	usage, err := NormalizeUsage(TokenUsage{
		InputTokens:       inputTokens,
		OutputTokens:      outputTokens,
		CachedInputTokens: cachedInputTokens,
		CacheWriteTokens:  cacheWriteTokens,
		ReasoningTokens:   reasoningTokens,
	})
	if err != nil {
		return right
	}
	return usage
}

// toolCallCollector accumulates streamed tool calls in start order. Calls are
// keyed by an internal key (the ToolCallID when non-empty, or a synthetic
// per-stream key for empty IDs) so distinct simultaneous calls that share an
// empty/duplicate ID never merge. Completed calls are NOT emitted at end time;
// flush emits every collected call in one ordered pass so output always follows
// model/start order regardless of the order calls finished.
type toolCallCollector struct {
	calls       map[string]*ToolCall
	order       []string
	openEmptyID []string // stack of synthetic keys for in-flight empty-id calls
	synthetic   int
	text        strings.Builder // accumulated assistant text, materialized in flush
	// pendingEmptyDelta is the synthetic key of an empty-id call that was opened
	// by a delta arriving before any start (so its buffered arguments aren't
	// orphaned). The next empty-id start adopts it instead of opening a new call.
	pendingEmptyDelta string

	maxArgBytes    int
	maxArgDuration time.Duration
	// maxFileArgDuration is longer than the ordinary active-call ceiling because
	// write_file/append_file arguments are the product being generated. Stalls
	// and bytes remain independently bounded.
	maxFileArgDuration time.Duration
	startedAt          map[string]time.Time
	// lastDeltaAt tracks per-call argument progress; the collect loop's stall
	// watchdog aborts a call whose fragments stop arriving (the real dead-
	// generation signal), while a slow-but-progressing call is left alone.
	lastDeltaAt map[string]time.Time
	// File calls need semantic progress too. Raw argument bytes can continue
	// while the content is trapped in one malformed/looping line, making token
	// counters rise while the visible line count and usable file prefix freeze.
	lastFileLineAt map[string]time.Time
	fileLineCount  map[string]int
	fileContentLen map[string]int
	fileLineStall  time.Duration
	// open tracks calls still generating (started/first-delta but not ended);
	// only these are stall-checked.
	open map[string]bool
	// runaway is set the moment any call trips an argument cap, so the collect
	// loop can stop draining the stream immediately (the abort reason is already
	// recorded on the call) instead of consuming a dead generation for minutes.
	runaway bool
}

func newToolCallCollector(maxArgBytes int, maxArgDuration time.Duration) *toolCallCollector {
	return &toolCallCollector{
		calls:          make(map[string]*ToolCall),
		maxArgBytes:    maxArgBytes,
		maxArgDuration: maxArgDuration,
		// Direct collector tests/callers keep the duration they explicitly pass.
		// CollectStreamWithOptions replaces this with the resolved file budget.
		maxFileArgDuration: maxArgDuration,
		startedAt:          make(map[string]time.Time),
		lastDeltaAt:        make(map[string]time.Time),
		lastFileLineAt:     make(map[string]time.Time),
		fileLineCount:      make(map[string]int),
		fileContentLen:     make(map[string]int),
		fileLineStall:      defaultFileLineStallTimeout,
		open:               make(map[string]bool),
	}
}

// start begins a tool call. A non-empty ID reuses any open call with that ID
// (some backends re-emit the same start); an empty ID always begins a fresh
// synthetic call so concurrent empty-id calls stay distinct.
func (collector *toolCallCollector) start(id string, name string, signature string) {
	key := id
	if id == "" {
		// Adopt an empty-id call that a delta opened before this start, so its
		// already-buffered arguments and this start's name land on one call.
		if collector.pendingEmptyDelta != "" {
			key = collector.pendingEmptyDelta
			collector.pendingEmptyDelta = ""
		} else {
			collector.synthetic++
			key = fmt.Sprintf("\x00synthetic-%d", collector.synthetic)
			collector.openEmptyID = append(collector.openEmptyID, key)
		}
	}
	call := collector.ensure(key, id)
	// Only set the name when non-empty and still unset, so a duplicate or
	// nameless follow-up start cannot clobber an already-resolved name.
	if name != "" && call.Name == "" {
		call.Name = name
	}
	// Preserve the reasoning signature (Gemini thoughtSignature) so it can be
	// replayed with the call on later turns.
	if signature != "" && call.Signature == "" {
		call.Signature = signature
	}
}

func (collector *toolCallCollector) delta(id string, fragment string) {
	key, ok := collector.resolveKey(id)
	if !ok {
		if id == "" {
			// An empty-id delta with no in-flight empty-id call: open one and
			// remember it so a following empty-id start adopts it instead of
			// orphaning these buffered arguments under a nameless call.
			collector.synthetic++
			key = fmt.Sprintf("\x00synthetic-%d", collector.synthetic)
			collector.openEmptyID = append(collector.openEmptyID, key)
			collector.pendingEmptyDelta = key
		} else {
			key = id
		}
		collector.ensure(key, id)
	}
	call := collector.calls[key]
	if call.Aborted {
		// Already flagged as a runaway call: stop growing Arguments so the
		// message is never dispatched with a still-larger, still-incomplete
		// payload. The rest of this call's deltas are silently dropped.
		return
	}
	call.Arguments += fragment
	now := time.Now()
	collector.lastDeltaAt[key] = now
	collector.recordFileLineProgress(key, call, now)
	maxBytes, maxDuration := collector.capsFor(call.Name)
	if maxBytes > 0 && len(call.Arguments) > maxBytes {
		call.Aborted = true
		collector.runaway = true
		call.AbortReason = fmt.Sprintf(
			"tool call arguments exceeded %d bytes before finishing; %s",
			maxBytes, abortAdvice(call.Name),
		)
		return
	}
	if maxDuration > 0 {
		if started, ok := collector.startedAt[key]; ok && time.Since(started) > maxDuration {
			// Progressing write_file/append_file calls that are still completing
			// new source lines are healthy model output, not a runaway. Killing
			// them mid-file forced multi-minute restarts (live mario session:
			// 5-min abort × many turns). Only hard-kill when line progress has
			// also stalled, or the call is a non-file tool.
			if isFileContentTool(call.Name) && collector.fileHasRecentLineProgress(key, now) {
				// Soft extend: keep generating while lines complete. Absolute
				// safety still comes from the byte cap + markStalled line-stall.
				return
			}
			call.Aborted = true
			collector.runaway = true
			call.AbortReason = fmt.Sprintf(
				"tool call arguments took longer than %s to generate; %s",
				maxDuration, abortAdvice(call.Name),
			)
		}
	}
}

func isFileContentTool(name string) bool {
	switch name {
	case "write_file", "append_file":
		return true
	default:
		return false
	}
}

// fileHasRecentLineProgress reports whether a streaming file call completed a
// new logical line within the file-line stall window — the semantic signal that
// generation is still productive (not stuck in one giant line or a token loop).
func (collector *toolCallCollector) fileHasRecentLineProgress(key string, now time.Time) bool {
	lastLine, ok := collector.lastFileLineAt[key]
	if !ok || lastLine.IsZero() {
		return false
	}
	// Require some real content so a stuck open-quote does not count as healthy.
	if collector.fileLineCount[key] < minLinesBeforeFileLineStall && collector.fileContentLen[key] < 256 {
		return false
	}
	window := collector.fileLineStall
	if window <= 0 {
		window = defaultFileLineStallTimeout
	}
	// Slightly looser than the stall kill window so a brief GC pause between
	// lines does not immediately trip the absolute duration abort.
	return now.Sub(lastLine) <= window*2
}

const minLinesBeforeFileLineStall = 5

// recordFileLineProgress tracks completed logical lines inside a streaming
// write_file/append_file JSON content string. It deliberately does not treat
// raw byte growth as semantic progress; markStalled handles the case where
// bytes keep arriving but source lines do not.
func (collector *toolCallCollector) recordFileLineProgress(key string, call *ToolCall, now time.Time) {
	switch call.Name {
	case "write_file", "append_file":
	default:
		return
	}
	contentLen, lineCount, ok := streamingFileContentProgress(call.Arguments)
	if !ok {
		return
	}
	if _, seen := collector.fileContentLen[key]; !seen {
		collector.lastFileLineAt[key] = now
	}
	if lineCount > collector.fileLineCount[key] {
		collector.lastFileLineAt[key] = now
		collector.fileLineCount[key] = lineCount
	}
	collector.fileContentLen[key] = contentLen
}

// streamingFileContentProgress inspects a possibly-incomplete JSON object and
// returns the decoded content length and logical line count. It only needs the
// top-level "content" string used by write_file/append_file and tolerates an
// unterminated value because that is the normal streaming state.
func streamingFileContentProgress(arguments string) (contentLen, lineCount int, ok bool) {
	const marker = `"content"`
	index := strings.Index(arguments, marker)
	if index < 0 {
		return 0, 0, false
	}
	index += len(marker)
	for index < len(arguments) && strings.ContainsRune(" \t\r\n", rune(arguments[index])) {
		index++
	}
	if index >= len(arguments) || arguments[index] != ':' {
		return 0, 0, false
	}
	index++
	for index < len(arguments) && strings.ContainsRune(" \t\r\n", rune(arguments[index])) {
		index++
	}
	if index >= len(arguments) || arguments[index] != '"' {
		return 0, 0, false
	}
	index++
	lineCount = 1
	for index < len(arguments) {
		switch arguments[index] {
		case '"':
			return contentLen, lineCount, true
		case '\n':
			contentLen++
			lineCount++
		case '\\':
			if index+1 >= len(arguments) {
				return contentLen, lineCount, true
			}
			index++
			switch arguments[index] {
			case 'n':
				contentLen++
				lineCount++
			case 'r':
				// Ignore carriage returns for logical line counting.
			case 'u':
				contentLen++
				if index+4 < len(arguments) {
					index += 4
				}
			default:
				contentLen++
			}
		default:
			contentLen++
		}
		index++
	}
	return contentLen, lineCount, true
}

// capsFor returns the argument caps for a tool, clamping edit_file to much
// tighter limits than the collector-wide defaults: a legitimate edit is small,
// so a slow/huge edit_file call can be failed fast instead of after minutes.
func (collector *toolCallCollector) capsFor(name string) (int, time.Duration) {
	maxBytes, maxDuration := collector.maxArgBytes, collector.maxArgDuration
	clampBytes := 0
	switch name {
	case "edit_file":
		maxDuration = collector.maxFileArgDuration
		// The normal collector default is 64 KiB. Promote it to the shared file
		// ceiling for edit_file, while preserving explicit smaller test/operator
		// limits.
		if collector.maxArgBytes <= 0 || collector.maxArgBytes == defaultMaxToolArgBytes {
			maxBytes = defaultMaxFileToolArgBytes
		}
		clampBytes = editFileMaxArgBytes
	case "exec_command":
		clampBytes = execCommandMaxArgBytes
	case "write_file", "append_file":
		maxDuration = collector.maxFileArgDuration
		// File bodies need a higher ceiling than generic tool args (JSON-escaped
		// HTML/JS games routinely exceed 64 KiB). When the collector is on the
		// production default (or unset), use the file ceiling. An explicit
		// MaxToolArgBytes (tests, tight harnesses) is honored as-is.
		if collector.maxArgBytes <= 0 || collector.maxArgBytes == defaultMaxToolArgBytes {
			maxBytes = defaultMaxFileToolArgBytes
		} else if maxBytes > 0 && maxBytes < defaultMaxFileToolArgBytes &&
			collector.maxArgBytes > defaultMaxToolArgBytes {
			// Explicit raise between default and file ceiling still gets the
			// full file budget so a mild override does not re-introduce mid-file
			// aborts.
			maxBytes = defaultMaxFileToolArgBytes
		}
	}
	if clampBytes > 0 && (maxBytes == 0 || maxBytes > clampBytes) {
		maxBytes = clampBytes
	}
	return maxBytes, maxDuration
}

func abortAdvice(name string) string {
	switch name {
	case "edit_file":
		return "this edit_file call was far too large — DO NOT retry edit_file. Immediately call write_file with path + full new content + overwrite:true. edit_file is only for 1-3 line surgical fixes"
	case "exec_command":
		return "this command was far too long — DO NOT retry it inline. Commands must be one short line; put the script in a file with write_file, then run that file"
	case "write_file":
		return "this write_file was too large. If partial content was saved, use append_file to add the rest. Otherwise split the file: write_file for the first ~100 lines, then append_file for each subsequent block"
	case "append_file":
		return "this append_file chunk was too large. Completed lines will be saved automatically; continue with ONLY the remaining content in chunks of at most 100 lines"
	}
	return "retry with much smaller arguments — for edit_file use the smallest unique old_string (a few lines, not a whole block); for write_file use write_file for the first chunk then append_file for each subsequent chunk"
}

// end closes an in-flight call. It does not emit anything; flush does, in start
// order. For empty IDs it pops the in-flight empty-id call off the stack so a
// following empty-id delta/end can't attach to an already-closed call.
func (collector *toolCallCollector) end(id string) {
	if key, ok := collector.resolveKey(id); ok {
		collector.open[key] = false
	}
	if id == "" {
		if len(collector.openEmptyID) > 0 {
			closed := collector.openEmptyID[len(collector.openEmptyID)-1]
			collector.openEmptyID = collector.openEmptyID[:len(collector.openEmptyID)-1]
			// If a delta-opened call is closed before any start adopts it, drop
			// the pending pointer so a later start can't attach to a closed call.
			if closed == collector.pendingEmptyDelta {
				collector.pendingEmptyDelta = ""
			}
		}
	}
}

// resolveKey maps an event ID to its internal key. Empty IDs route to the most
// recently started, not-yet-ended empty-id call.
func (collector *toolCallCollector) resolveKey(id string) (string, bool) {
	if id == "" {
		if len(collector.openEmptyID) == 0 {
			return "", false
		}
		return collector.openEmptyID[len(collector.openEmptyID)-1], true
	}
	if _, ok := collector.calls[id]; ok {
		return id, true
	}
	return "", false
}

func (collector *toolCallCollector) ensure(key string, id string) *ToolCall {
	if call, ok := collector.calls[key]; ok {
		return call
	}
	call := &ToolCall{ID: id}
	collector.calls[key] = call
	collector.order = append(collector.order, key)
	collector.startedAt[key] = time.Now()
	collector.lastDeltaAt[key] = time.Now()
	collector.open[key] = true
	return call
}

// markStalled aborts any open, non-aborted call whose argument fragments
// stopped arriving more than window ago. Returns true when a call was aborted
// (the abort reason is recorded on the call, mirroring the byte-cap path).
func (collector *toolCallCollector) markStalled(window time.Duration) bool {
	for key, isOpen := range collector.open {
		if !isOpen {
			continue
		}
		call, ok := collector.calls[key]
		if !ok || call.Aborted {
			continue
		}
		if collector.fileLineStall > 0 &&
			collector.fileLineCount[key] >= minLinesBeforeFileLineStall &&
			collector.fileContentLen[key] > 0 {
			lastLine := collector.lastFileLineAt[key]
			if !lastLine.IsZero() && time.Since(lastLine) > collector.fileLineStall {
				call.Aborted = true
				collector.runaway = true
				call.AbortReason = fmt.Sprintf(
					"file content stopped completing new lines for over %s while argument bytes kept arriving; %s",
					collector.fileLineStall, abortAdvice(call.Name),
				)
				return true
			}
		}
		last, ok := collector.lastDeltaAt[key]
		if !ok || time.Since(last) <= window {
			continue
		}
		call.Aborted = true
		collector.runaway = true
		call.AbortReason = fmt.Sprintf(
			"tool call argument generation went silent for over %s and was abandoned; %s",
			window, abortAdvice(call.Name),
		)
		return true
	}
	return false
}

// flush emits every collected call once, in start order. Malformed (nameless)
// calls are dropped so the agent never dispatches an empty tool name.
func (collector *toolCallCollector) flush(collected *CollectedStream) {
	for _, key := range collector.order {
		call, ok := collector.calls[key]
		if !ok {
			continue
		}
		delete(collector.calls, key)
		if call.Name != "" {
			collected.ToolCalls = append(collected.ToolCalls, *call)
		} else {
			collected.DroppedToolCalls++
		}
	}
	collector.order = collector.order[:0]
	collector.openEmptyID = collector.openEmptyID[:0]
	collector.pendingEmptyDelta = ""
	// flush is the single finalization point hit before every return, so
	// materialize the accumulated text here exactly once.
	collected.Text = collector.text.String()
}
