package llama

import (
	"bufio"
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"github.com/aistatine/sonut/internal/observability"
	sonutruntime "github.com/aistatine/sonut/internal/sonutruntime"
)

// Provider implements sonutruntime.Provider against a running llama.cpp server.
// It streams completions via the /v1/chat/completions SSE endpoint (OpenAI-
// compatible) that llama-server exposes out of the box.
type Provider struct {
	baseURL    string
	httpClient *http.Client
	// modelNameCache avoids a /v1/models round trip before every completion.
	// A llama-server process cannot change its loaded model in place; transient
	// lookup failures are deliberately not cached so a later request can heal.
	modelNameCache atomic.Pointer[string]
	// disableThinkingOnce is armed by the overthink guard in readSSE when a
	// stream produced only reasoning past overthinkReasoningLimit: the next
	// request (the agent loop's stall-retry of the same turn) runs with
	// thinking disabled so it is guaranteed to terminate.
	disableThinkingOnce atomic.Bool
	// overthinkLimit is the per-stream pure-reasoning byte cap (0 → default).
	// Tool-less planner passes raise it so a real plan can finish thinking
	// without being aborted as a "reasoning overrun" stall.
	overthinkLimit atomic.Int64
}

// NewProvider creates a llama.cpp provider pointing at the given base URL.
// Example: NewProvider("http://127.0.0.1:2748")
func NewProvider(baseURL string) *Provider {
	return &Provider{
		baseURL: strings.TrimRight(baseURL, "/"),
		httpClient: &http.Client{
			Timeout: 0, // no timeout — streaming can take a long time
			Transport: &http.Transport{
				// Generous: with return_progress the first bytes (and thus headers)
				// arrive as soon as prefill starts, but if a strict-server retry
				// drops that field, headers can block for the entire prefill of a
				// multi-KB prompt — minutes on a 27B. 30s here caused "connection
				// lost — reconnecting" cycles on the first message after /cd.
				ResponseHeaderTimeout: 5 * time.Minute,
			},
		},
	}
}

// NewProviderOnPort creates a provider for localhost on the given port.
func NewProviderOnPort(port int) *Provider {
	return NewProvider(fmt.Sprintf("http://127.0.0.1:%d", port))
}

// HasMultimodal reports whether the llama-server has an mmproj/vision model
// actually LOADED. Detection strategy (first match wins):
//  1. Mirror the launcher's gate: the server is started with --mmproj ONLY when
//     an mmproj file is present AND SONUT_NO_VISION is unset AND memory allows
//     it (see OptimalLlamaArgs). Disk presence alone is NOT enough — on a
//     memory-constrained machine the launcher skips --mmproj even though the
//     file exists, so a bare disk check falsely reports vision and every image
//     send 500s with "image input is not supported / provide the mmproj".
//  2. Fall back to querying /v1/models for a "multimodal" capability, for
//     forward-compatibility with servers that do expose it.
func (p *Provider) HasMultimodal() bool {
	// Ground truth (when the server is up): /props reports the modalities the
	// running server actually loaded. This wins over every heuristic below — it
	// reflects whether --mmproj was really passed, so it fixes both the false
	// negative (memory heuristic said no but the projector loaded) and the false
	// positive (mmproj on disk but the launcher skipped it). Silent no-op at
	// pre-server call sites (app startup), where it falls through to the gate.
	if p.ServerReportsVision() {
		return true
	}
	// Primary heuristic (server not up yet): mmproj is loaded only under the same
	// conditions the launcher uses.
	if visionLaunchGateOpen() {
		return true
	}
	// Secondary: query /v1/models for an explicit capabilities field.
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, "GET", p.baseURL+"/v1/models", nil)
	if err != nil {
		return false
	}
	setAuth(req)
	resp, err := p.httpClient.Do(req)
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return false
	}
	var result struct {
		Models []struct {
			Capabilities []string `json:"capabilities"`
		} `json:"models"`
		Data []struct {
			Capabilities []string `json:"capabilities"`
		} `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return false
	}
	check := func(caps []string) bool {
		for _, c := range caps {
			if strings.EqualFold(c, "multimodal") || strings.EqualFold(c, "vision") {
				return true
			}
		}
		return false
	}
	for _, m := range result.Models {
		if check(m.Capabilities) {
			return true
		}
	}
	for _, m := range result.Data {
		if check(m.Capabilities) {
			return true
		}
	}
	return false
}

// ServerReportsVision queries the RUNNING llama-server's /props endpoint for the
// modalities it actually loaded. llama.cpp reports {"modalities":{"vision":true}}
// when started with --mmproj, so this is ground truth about the live server —
// independent of the disk/memory launch heuristic, which is only a prediction of
// what the launcher did. Returns false when the server is unreachable (e.g.
// called before it starts) or reports no vision modality.
func (p *Provider) ServerReportsVision() bool {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, "GET", p.baseURL+"/props", nil)
	if err != nil {
		return false
	}
	setAuth(req)
	resp, err := p.httpClient.Do(req)
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return false
	}
	var props struct {
		Modalities struct {
			Vision bool `json:"vision"`
		} `json:"modalities"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&props); err != nil {
		return false
	}
	return props.Modalities.Vision
}

// mmProjPresent reports whether an mmproj GGUF file exists in the model directory.
func mmProjPresent() bool {
	_, mmPath, _ := CheckModelsPresent()
	return mmPath != ""
}

// VisionGatedOff reports the actionable case where the hardware COULD do vision
// — an mmproj GGUF is on disk — but the launch gate is closed, so the server was
// started without --mmproj. The TUI uses this to turn a dead-end "unsupported"
// refusal into "relaunch with ATCODE_FORCE_VISION=1 to enable it".
//
// For Bonsai/atcode the gate stays open whenever the projector is present
// (unless SONUT_NO_VISION=1), so this is mainly for non-product models under
// memory pressure — and for the stale-server case where the file exists but a
// leftover process was started without --mmproj.
func VisionGatedOff() bool {
	return mmProjPresent() && !visionLaunchGateOpen()
}

// VisionProjectorMissingFromServer reports that an mmproj is on disk and the
// launcher would load it, but the RUNNING server has no vision modality. That
// is a stale process (started under an older gate, or before the projector
// was downloaded) — attach should still be refused until relaunch, with a
// clear "restart @code" message rather than "model has no vision".
func VisionProjectorMissingFromServer(port int) bool {
	if port <= 0 || !visionLaunchGateOpen() {
		return false
	}
	return !NewProviderOnPort(port).ServerReportsVision()
}

// visionLaunchGateOpen reports whether the llama-server launcher would actually
// pass --mmproj: an mmproj file must exist AND shouldLoadMMProj allows it for
// the active main GGUF. Uses the SAME predicate the launcher applies (see
// OptimalLlamaArgs), so detection cannot drift from how the server was started.
func visionLaunchGateOpen() bool {
	if !mmProjPresent() {
		return false
	}
	mainPath, _, _ := CheckModelsPresent()
	if mainPath == "" {
		mainPath = findBonsaiMainOnDisk()
	}
	return shouldLoadMMProj(mainPath)
}

// shouldLoadMMProj decides whether OptimalLlamaArgs should pass --mmproj.
// SONUT_NO_VISION=1 always wins (off). Bonsai/atcode is a product multimodal
// model (screenshot coding); the projector loads whenever present — memory
// pressure is handled by shrinking -c (bonsaiRecommendedCtx), not by dropping
// vision. Other families still use the free-RAM / swap heuristic.
func shouldLoadMMProj(modelPath string) bool {
	if atcodeEnv("NO_VISION") != "" {
		return false
	}
	if isBonsaiModel(modelPath) {
		return true
	}
	return memoryAllowsVision()
}

// visionAllowedByEnvAndMemory is the env+memory portion of the --mmproj launch
// gate for non-product models. Kept for tests and call sites that only care
// about the heuristic, not the Bonsai always-on policy.
func visionAllowedByEnvAndMemory() bool {
	return atcodeEnv("NO_VISION") == "" && memoryAllowsVision()
}

// modelName fetches the first model ID from /v1/models, falling back to "local".
func (p *Provider) modelName() string {
	if cached := p.modelNameCache.Load(); cached != nil {
		return *cached
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, "GET", p.baseURL+"/v1/models", nil)
	if err != nil {
		return "local"
	}
	setAuth(req)
	resp, err := p.httpClient.Do(req)
	if err != nil {
		return "local"
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "local"
	}
	var result struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil || len(result.Data) == 0 {
		return "local"
	}
	name := strings.TrimSpace(result.Data[0].ID)
	if name == "" {
		return "local"
	}
	p.modelNameCache.CompareAndSwap(nil, &name)
	return name
}

// --- request / response shapes (OpenAI-compatible) ---

// chatContent is either a plain string or a multipart array (text + images).
// We use json.RawMessage so both forms marshal correctly.
type chatContent = json.RawMessage

// textContent builds a plain-string content value.
func textContent(s string) chatContent {
	b, _ := json.Marshal(s)
	return b
}

// multipartContent builds a content array for a message that carries images.
// Each image is encoded as base64 data-URI; text is prepended as a text block.
func multipartContent(text string, images []sonutruntime.ImageBlock) chatContent {
	type textPart struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	type imageURL struct {
		URL string `json:"url"`
	}
	type imagePart struct {
		Type     string   `json:"type"`
		ImageURL imageURL `json:"image_url"`
	}
	parts := make([]any, 0, 1+len(images))
	if text != "" {
		parts = append(parts, textPart{Type: "text", Text: text})
	}
	for _, img := range images {
		enc := base64.StdEncoding.EncodeToString(img.Data)
		mt := img.MediaType
		if mt == "" {
			mt = "image/png"
		}
		url := "data:" + mt + ";base64," + enc
		parts = append(parts, imagePart{Type: "image_url", ImageURL: imageURL{URL: url}})
	}
	b, _ := json.Marshal(parts)
	return b
}

type chatMessage struct {
	Role             string         `json:"role"`
	Content          chatContent    `json:"content"`
	ReasoningContent string         `json:"reasoning_content,omitempty"`
	ToolCalls        []chatToolCall `json:"tool_calls,omitempty"`
	ToolCallID       string         `json:"tool_call_id,omitempty"`
}

type chatToolCall struct {
	ID       string               `json:"id"`
	Type     string               `json:"type"`
	Function chatToolCallFunction `json:"function"`
}

type chatToolCallFunction struct {
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
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

type streamOptions struct {
	IncludeUsage bool `json:"include_usage"`
}

type chatRequest struct {
	Model    string        `json:"model"`
	Messages []chatMessage `json:"messages"`
	Tools    []toolDef     `json:"tools,omitempty"`
	// ToolChoice tells llama.cpp's --jinja-driven template that tools are
	// live for this turn. Explicit "auto" (rather than relying on the
	// server's default) avoids ambiguity across llama.cpp versions.
	ToolChoice string `json:"tool_choice,omitempty"`
	Stream     bool   `json:"stream"`
	// stream_options requests token usage in the final SSE chunk (OpenAI spec).
	// Pointer so the strict-server retry can drop the field entirely: older
	// llama-server builds 400 on ANY unknown field, including an empty struct.
	StreamOptions *streamOptions `json:"stream_options,omitempty"`
	// llama.cpp extensions
	Temperature float64 `json:"temperature,omitempty"`
	TopP        float64 `json:"top_p,omitempty"`
	TopK        int     `json:"top_k,omitempty"`
	// MinP is the min-p sampler floor. The product profile sets this to 0
	// (disabled); the llama.cpp default of 0.05 is too aggressive for 27B.
	MinP               float64        `json:"min_p"`
	ChatTemplateKwargs map[string]any `json:"chat_template_kwargs,omitempty"`
	// cache_prompt instructs llama.cpp to save/reuse the KV cache for the
	// prompt prefix across requests. Without this, every request re-prefills
	// the full system prompt from scratch — causing long "↑ 0 tok" delays.
	CachePrompt bool `json:"cache_prompt"`
	// return_progress makes llama-server stream prompt_progress chunks WHILE
	// prefilling. Without it the server sends zero bytes (not even response
	// headers) until the first token, so a long prefill after /cd tripped the
	// 30s ResponseHeaderTimeout and surfaced as "connection lost — reconnecting"
	// cycles. Progress chunks carry no choices, so readSSE ignores them; their
	// bytes keep the header timeout and the stall watchdog fed.
	ReturnProgress bool `json:"return_progress,omitempty"`
}

type toolCallDelta struct {
	Index    int    `json:"index"`
	ID       string `json:"id"`
	Type     string `json:"type"`
	Function struct {
		Name      string `json:"name"`
		Arguments string `json:"arguments"`
	} `json:"function"`
}

type deltaContent struct {
	Role    string `json:"role,omitempty"`
	Content string `json:"content,omitempty"`
	// ReasoningContent is the private thinking field emitted by reasoning models
	// (e.g. QwQ, DeepSeek-R1, sonut-1). It is kept separate from Content so
	// frontends can account for it without leaking chain-of-thought into the
	// user-facing answer.
	ReasoningContent string `json:"reasoning_content,omitempty"`
	ToolCallID       string `json:"tool_call_id,omitempty"`
	// ToolCalls is inside the delta object per the OpenAI streaming spec.
	ToolCalls []toolCallDelta `json:"tool_calls,omitempty"`
}

type choiceDelta struct {
	Delta        deltaContent `json:"delta"`
	FinishReason string       `json:"finish_reason"`
}

type sseChunk struct {
	Choices []choiceDelta `json:"choices"`
	// PromptProgress rides on prefill-progress chunks when the request set
	// return_progress. Non-nil = the server is alive and working through the
	// prompt; readSSE forwards it as a ping so silence trackers don't fire.
	PromptProgress *struct {
		Total     int `json:"total"`
		Cache     int `json:"cache"`
		Processed int `json:"processed"`
	} `json:"prompt_progress"`
	Usage *struct {
		PromptTokens     int `json:"prompt_tokens"`
		CompletionTokens int `json:"completion_tokens"`
		TotalTokens      int `json:"total_tokens"`
	} `json:"usage"`
}

// injectEffortInstruction prepends a short instruction to the first system
// message, or inserts a new system message before the first user message, so
// the model adjusts its thinking depth even when chat_template_kwargs alone is
// not honoured by the running llama-server version.
// injectEffortInstruction appends the Qwen3 thinking soft-switch to the LAST
// plain-text user message (the officially supported location — the latest
// user-turn switch wins). Putting it there instead of prepending to the system
// message keeps the system+tools KV prefix byte-identical across efforts, so
// switching thinking depth mid-run (plan deep → execute fast) re-prefills only
// the small conversation tail, not the whole prompt.
func injectEffortInstruction(msgs []chatMessage, instruction string) []chatMessage {
	for i := len(msgs) - 1; i >= 0; i-- {
		if msgs[i].Role != "user" {
			continue
		}
		var existing string
		if err := json.Unmarshal(msgs[i].Content, &existing); err != nil {
			break // multimodal/array content — fall back to the system message
		}
		msgs[i].Content = textContent(existing + "\n\n" + instruction)
		return msgs
	}
	// No plain-text user message — merge into the system message (legacy path).
	for i, m := range msgs {
		if m.Role == "system" {
			var existing string
			_ = json.Unmarshal(m.Content, &existing)
			msgs[i].Content = textContent(instruction + "\n\n" + existing)
			return msgs
		}
	}
	sys := chatMessage{Role: "system", Content: textContent(instruction)}
	return append([]chatMessage{sys}, msgs...)
}

// compactToolText trims a tool or parameter description to its first sentence,
// capped at max bytes. Tool schemas are prefilled into EVERY request; on the
// hybrid-attention local models (no KV prefix reuse, ~60 tok/s prompt
// processing) the full 21 KB of core-tool schemas alone costs ~90 s of dead
// air per turn. Names, types, enums, and required lists are kept intact.
func compactToolText(s string, max int) string {
	s = strings.TrimSpace(s)
	if i := strings.IndexAny(s, ".\n"); i >= 0 && i+1 < len(s) {
		s = s[:i+1]
	}
	if len(s) > max {
		cut := max
		for cut > 0 && s[cut]&0xC0 == 0x80 {
			cut--
		}
		s = s[:cut]
	}
	return strings.TrimSpace(s)
}

// compactToolParams returns a copy of a JSON-schema parameter tree with every
// nested "description" string removed — parameter names, types, enums,
// defaults, and required lists carry enough signal for the local models, and
// the descriptions are the single largest compressible chunk of the per-turn
// prefill. The tool-level description (shortened) is kept.
//
// It also caps "properties" nesting at maxToolParamPropsDepth levels: past the
// cap a sub-object keeps its type but drops its property map (and the matching
// "required" list, now meaningless). Measured on the core tool set, the deep
// sandbox permission tree (bash / exec_command / request_permissions all embed
// file_system.entries[].path{...}) was ~600 bytes of prefill per tool that the
// local models never fill in correctly anyway.
func compactToolParams(params map[string]any) map[string]any {
	return compactToolParamsDepth(params, 0)
}

// maxToolParamPropsDepth is how many nested "properties" maps are kept. Two
// levels cover every normal tool (top-level args plus one nested object, e.g.
// ask_user's questions[].{question,options,...}); only the rarely-used sandbox
// permission trees go deeper.
const maxToolParamPropsDepth = 2

func compactToolParamsDepth(params map[string]any, depth int) map[string]any {
	out := make(map[string]any, len(params))
	propsDropped := false
	for k, v := range params {
		switch tv := v.(type) {
		case string:
			if k == "description" {
				continue
			}
			out[k] = tv
		case map[string]any:
			if k == "properties" {
				if depth >= maxToolParamPropsDepth {
					propsDropped = true
					continue
				}
				props := make(map[string]any, len(tv))
				for name, sub := range tv {
					if m, ok := sub.(map[string]any); ok {
						props[name] = compactToolParamsDepth(m, depth+1)
					} else {
						props[name] = sub
					}
				}
				out[k] = props
				continue
			}
			out[k] = compactToolParamsDepth(tv, depth)
		case []any:
			list := make([]any, len(tv))
			for i, item := range tv {
				if m, ok := item.(map[string]any); ok {
					list[i] = compactToolParamsDepth(m, depth)
				} else {
					list[i] = item
				}
			}
			out[k] = list
		default:
			out[k] = v
		}
	}
	if propsDropped {
		// A "required" list without the property map it points into is noise.
		delete(out, "required")
	}
	return out
}

// CompactToolDescription and CompactToolParameters expose the exact tool-schema
// compaction StreamCompletion applies, so the TUI's KV warmup can prime a
// byte-identical prefix. Any drift between these and the request path makes the
// warmup useless (full re-prefill on the first real message).
func CompactToolDescription(s string) string { return compactToolText(s, 150) }

func CompactToolParameters(p map[string]any) map[string]any { return compactToolParams(p) }

// buildChatRequest assembles the OpenAI-compatible chat body. Effort instructions
// are injected into the LAST user message (never the system message) so the
// system+tools JSON prefix stays byte-identical across plan/execute turns and
// llama.cpp's cache_prompt can reuse the KV prefix.
//
// CachePrompt and ReturnProgress are always true — dropping either causes the
// multi-minute "↑ 0 tok" stalls the agent is known for.
func buildChatRequest(model string, msgs []chatMessage, tools []toolDef, request sonutruntime.CompletionRequest) (chatRequest, error) {
	body := chatRequest{
		Model:          model,
		Messages:       msgs,
		Tools:          tools,
		Stream:         true,
		StreamOptions:  &streamOptions{IncludeUsage: true},
		Temperature:    0.2,  // generic coding default; Bonsai overrides below
		CachePrompt:    true, // reuse KV cache across turns when the backend supports it
		ReturnProgress: true, // stream bytes during prefill so long prompts don't look dead
	}
	if len(tools) > 0 {
		body.ToolChoice = "auto"
	}

	// Map effort to Qwen3/thinking-model controls.
	// Bonsai-27B (atcode) is a reasoning model — thinking stays ON for quality
	// unless DisableThinking is set (executor after a plan, stall recovery).
	// Effort only clamps the budget via instruction + caps.
	//
	// CRITICAL for cache_prompt:
	//  - Effort instructions go on the LAST user message only (never system).
	//  - ChatTemplateKwargs always uses the same two keys so encoding/json map
	//    order cannot thrash the rendered chat-template prefix.
	//  - When DisableThinking is set we skip effort injection entirely so the
	//    conversation tail stays free of contradictory "think briefly" text.
	if request.DisableThinking {
		body.ChatTemplateKwargs = map[string]any{"enable_thinking": false, "thinking": false}
		// Stable act-now instruction for plan-seeded executor / stall recovery.
		msgs = injectEffortInstruction(msgs, "Act on the next useful step now. Do not restate the plan or explain internal policy; call the tool or give the answer when ready.")
	} else {
		effort := strings.ToLower(strings.TrimSpace(request.ReasoningEffort))
		switch effort {
		case "none", "low", "minimal", "min", "":
			// Flash mode: thinking stays ENABLED but is clamped to a couple of
			// sentences. Fully disabling by default made plain Q&A dumber; the
			// plan-seeded executor path opts out via DisableThinking. Thinking is
			// private: the model must emit only the answer or a tool call.
			// NOTE: never inject the literal `/no_think` soft switch — measured
			// on Qwopus3.5-4B-Coder-MTP it sends the model into an unterminated
			// reasoning stream with EMPTY content at any token budget.
			body.ChatTemplateKwargs = map[string]any{"enable_thinking": true, "thinking": true}
			msgs = injectEffortInstruction(msgs, "Work quietly and naturally. Keep private deliberation concise; never emit a checklist, numbered self-analysis, prompt text, or internal policy. Output only the final answer or the next tool call.")
		case "medium", "mid":
			body.ChatTemplateKwargs = map[string]any{"enable_thinking": true, "thinking": true}
			msgs = injectEffortInstruction(msgs, "Think briefly only when useful. For an obvious next step, take it immediately. Keep the scratchpad private and natural; do not emit headings, checklists, prompt text, or policy. Output only the final answer or the next tool call.")
		default:
			// auto/high/xhigh/max (and anything unrecognized): thinking enabled but
			// bounded — deep reasoning for hard problems only. "auto" MUST land here.
			body.ChatTemplateKwargs = map[string]any{"enable_thinking": true, "thinking": true}
			msgs = injectEffortInstruction(msgs, "Think thoroughly in private when the path is unclear. Follow the existing plan without recapping it, and take the next useful action. Keep the scratchpad natural and concise; never emit headings, numbered self-analysis, prompt text, or policy. Output only the final answer or the next tool call.")
		}
	}
	// Sampling: product-tuned defaults take priority when the
	// active GGUF is atcode/Bonsai (https://docs.prismml.com/run/server —
	// temp 0.5, top-p 0.85, top-k 20, min-p 0). Otherwise fall back to Qwen
	// thinking sampling (0.6/0.95/20) when thinking is on, coding 0.2 when off.
	if temp, topP, topK, minP, ok := bonsaiSampling(); ok {
		body.Temperature = temp
		body.TopP = topP
		body.TopK = topK
		body.MinP = minP
	} else if enabled, ok := body.ChatTemplateKwargs["enable_thinking"].(bool); ok && enabled {
		// Measured on the 4B MTP: near-greedy sampling with thinking on causes
		// overthink spirals. At 0.6 every run terminates (finish=stop).
		body.Temperature = 0.6
		body.TopP = 0.95
		body.TopK = 20
	}
	body.Messages = msgs
	return body, nil
}

// buildChatMessages preserves the complete OpenAI-compatible conversation
// transcript. In particular, assistant tool calls, their matching tool result
// IDs, and local reasoning_content must be replayed verbatim. Omitting any of
// them changes the rendered chat-template tokens immediately after the first
// assistant turn, limiting llama.cpp's prefix cache to only system+user and
// forcing the growing tool history to be re-prefilled on every iteration.
func buildChatMessages(messages []sonutruntime.Message) []chatMessage {
	msgs := make([]chatMessage, 0, len(messages))
	for _, message := range messages {
		var content chatContent
		if len(message.Images) > 0 {
			content = multipartContent(message.Content, message.Images)
		} else {
			content = textContent(message.Content)
		}
		msg := chatMessage{
			Role:             string(message.Role),
			Content:          content,
			ReasoningContent: localReasoningContent(message.Reasoning),
			ToolCallID:       message.ToolCallID,
		}
		for _, call := range message.ToolCalls {
			msg.ToolCalls = append(msg.ToolCalls, chatToolCall{
				ID:   call.ID,
				Type: "function",
				Function: chatToolCallFunction{
					Name:      call.Name,
					Arguments: call.Arguments,
				},
			})
		}
		msgs = append(msgs, msg)
	}
	return msgs
}

func localReasoningContent(blocks []sonutruntime.ReasoningBlock) string {
	var reasoning strings.Builder
	for _, block := range blocks {
		if block.Provider == "llama" && block.Type == "reasoning_content" {
			reasoning.WriteString(block.Text)
		}
	}
	return reasoning.String()
}

// StreamCompletion implements sonutruntime.Provider.
func (p *Provider) StreamCompletion(
	ctx context.Context,
	request sonutruntime.CompletionRequest,
) (<-chan sonutruntime.StreamEvent, error) {
	// Startup launches the local engine asynchronously so the TUI is visible
	// within seconds even when a cold 262k model needs longer to map its weights
	// and allocate KV. Gate the first request on health instead of racing a
	// half-loaded server. This is a no-op for remote/custom providers.
	if isLocalLlamaURL(p.baseURL) {
		if err := waitForLocalHealth(ctx, p.baseURL); err != nil {
			return nil, fmt.Errorf("atcode engine not ready: %w", err)
		}
	}

	msgs := buildChatMessages(request.Messages)

	// Build tools list
	var tools []toolDef
	for _, t := range request.Tools {
		params := t.Parameters
		if params == nil {
			params = map[string]any{"type": "object", "properties": map[string]any{}}
		}
		tools = append(tools, toolDef{
			Type: "function",
			Function: toolFunction{
				Name:        t.Name,
				Description: compactToolText(t.Description, 150),
				Parameters:  compactToolParams(params),
			},
		})
	}

	// Tool-less planner turns need a higher pure-reasoning ceiling: Bonsai
	// deliberates in reasoning_content before emitting the numbered plan as
	// content. The default overthink limit aborts those as "reasoning overrun"
	// and the agent continues plannerless (empty plan UI).
	if len(tools) == 0 {
		p.overthinkLimit.Store(int64(plannerOverthinkReasoningLimit))
	} else {
		p.overthinkLimit.Store(int64(overthinkReasoningLimit))
	}

	// One-shot override from the overthink guard: force thinking off even when
	// the request didn't ask for it (stall recovery for a pure-reasoning spiral).
	if p.disableThinkingOnce.Swap(false) {
		request.DisableThinking = true
	}
	body, err := buildChatRequest(p.modelName(), msgs, tools, request)
	if err != nil {
		return nil, err
	}

	bodyBytes, err := json.Marshal(body)
	if err != nil {
		return nil, fmt.Errorf("atcode engine request encoding failed: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, "POST",
		p.baseURL+"/v1/chat/completions",
		bytes.NewReader(bodyBytes),
	)
	if err != nil {
		return nil, fmt.Errorf("atcode engine request creation failed: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "text/event-stream")
	setAuth(req)

	resp, err := p.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("atcode engine request failed: %w", err)
	}
	if resp.StatusCode == http.StatusBadRequest {
		// Older llama-server builds reject unknown fields (e.g. stream_options)
		// in strict mode. Retry once without stream_options ONLY — keep
		// cache_prompt and return_progress. Dropping return_progress made long
		// prefills look dead (no SSE bytes) so the stall watchdog fired false
		// "model stalled" during healthy prefill.
		resp.Body.Close()
		body.StreamOptions = nil
		// CachePrompt and ReturnProgress MUST stay set — they are the difference
		// between multi-minute re-prefills / false stalls and a live session.
		if !body.CachePrompt {
			body.CachePrompt = true
		}
		if !body.ReturnProgress {
			body.ReturnProgress = true
		}
		retryBytes, merr := json.Marshal(body)
		if merr == nil {
			retryReq, rerr := http.NewRequestWithContext(ctx, "POST",
				p.baseURL+"/v1/chat/completions",
				bytes.NewReader(retryBytes),
			)
			if rerr == nil {
				retryReq.Header.Set("Content-Type", "application/json")
				retryReq.Header.Set("Accept", "text/event-stream")
				setAuth(retryReq)
				resp, err = p.httpClient.Do(retryReq)
				if err != nil {
					return nil, fmt.Errorf("atcode engine retry failed: %w", err)
				}
			}
		}
	}
	if resp.StatusCode != http.StatusOK {
		errBody, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		return nil, fmt.Errorf("atcode engine returned %d: %s", resp.StatusCode, string(errBody))
	}

	// Request-size diagnostic: prompt bytes dominate hybrid re-prefill cost.
	promptBytes := 0
	imageBytes := 0
	for _, m := range request.Messages {
		promptBytes += len(m.Content)
		for _, img := range m.Images {
			imageBytes += len(img.Data)
		}
	}
	observability.PerfLog("inference_start",
		"tools", len(tools),
		"msgs", len(request.Messages),
		"prompt_bytes", promptBytes,
		"image_bytes", imageBytes,
		"effort", request.ReasoningEffort,
		"disable_thinking", request.DisableThinking,
		"body_bytes", len(bodyBytes),
	)

	ch := make(chan sonutruntime.StreamEvent, 32)
	go func() {
		defer close(ch)
		defer resp.Body.Close()
		p.readSSE(ctx, resp.Body, ch)
	}()

	return ch, nil
}

func isLocalLlamaURL(baseURL string) bool {
	return strings.HasPrefix(strings.ToLower(strings.TrimSpace(baseURL)), "http://127.0.0.1:") ||
		strings.HasPrefix(strings.ToLower(strings.TrimSpace(baseURL)), "http://localhost:")
}

func waitForLocalHealth(ctx context.Context, baseURL string) error {
	if ctx == nil {
		ctx = context.Background()
	}
	// Startup is intentionally asynchronous so the TUI can open quickly, but
	// the first request must not wait forever if llama-server crashed, rejected
	// its launch flags, or is stuck allocating an oversized KV cache. Keep the
	// generous cold-start window while turning an infinite prefill-looking wait
	// into a useful error.
	const localHealthWaitTimeout = 3 * time.Minute
	healthCtx, cancel := context.WithTimeout(ctx, localHealthWaitTimeout)
	defer cancel()
	client := &http.Client{Timeout: 2 * time.Second}
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	for {
		req, err := http.NewRequestWithContext(healthCtx, http.MethodGet, strings.TrimRight(baseURL, "/")+"/health", nil)
		if err == nil {
			setAuth(req)
			resp, requestErr := client.Do(req)
			if requestErr == nil && resp != nil {
				status := resp.StatusCode
				_ = resp.Body.Close()
				if status == http.StatusOK {
					return nil
				}
			}
		}
		select {
		case <-healthCtx.Done():
			if ctx.Err() != nil {
				return ctx.Err()
			}
			if healthCtx.Err() == context.DeadlineExceeded {
				return fmt.Errorf("timed out after %s; check %s", localHealthWaitTimeout, AtcodeServerLogPath())
			}
			return healthCtx.Err()
		case <-ticker.C:
		}
	}
}

// thinkStripper splits a streaming content delta into reasoning and regular text.
// It handles both <think>…</think> and <thinking>…</thinking> tags (and mixed
// close tags), including the streaming case where a tag is split across chunks.
// It is stateful: the state machine persists across SSE chunk boundaries.
type thinkStripper struct {
	buf     string
	inThink bool // true when inside a <think> or <thinking> block
	// Some local reasoning models ignore the structured reasoning field and
	// print a plain-text Thought:/Answer: transcript instead. Keep a small
	// probe buffer so that transcript is routed to the reasoning channel too.
	plainBuf  string
	plainMode uint8 // 0=probing, 1=plain reasoning, 2=plain answer
}

// openTags are the recognised opening tags, longest first so <thinking> is
// preferred over a bare <think> prefix match.
var openTags = []string{"<thinking>", "<think>"}

// closeTags are the recognised closing tags (either form closes either opener).
var closeTags = []string{"</thinking>", "</think>"}

// maxTagLen is the longest partial tag we might need to buffer. We hold back
// len(longestTag)-1 bytes at the end of the buffer so a tag split across chunks
// is never prematurely emitted as plain text.
const maxTagLen = len("<thinking>") // 10 — the longest of open/close tags

// firstTagIndex returns the start index and the matched tag for the first
// occurrence of any of the given tags in s, or (-1, "") if none match.
func firstTagIndex(s string, tags []string) (int, string) {
	best, bestTag := -1, ""
	for _, tag := range tags {
		if idx := strings.Index(s, tag); idx >= 0 {
			if best < 0 || idx < best {
				best, bestTag = idx, tag
			}
		}
	}
	return best, bestTag
}

// hasStrayCloseTag reports whether s contains any close tag (stripped before
// emitting text so stray leaked </think> bytes never reach the TUI).
func stripStrayCloseTags(s string) string {
	for _, tag := range closeTags {
		s = strings.ReplaceAll(s, tag, "")
	}
	return s
}

// endsWithPartialTag returns the length of the longest partial prefix of any
// open tag that s ends with (used to hold back those bytes in the buffer).
func endsWithPartialOpenTag(s string) int {
	for _, tag := range openTags {
		for pLen := min(len(s), len(tag)-1); pLen > 0; pLen-- {
			if strings.HasSuffix(s, tag[:pLen]) {
				return pLen
			}
		}
	}
	return 0
}

// feed consumes a content delta and returns (textOut, reasoningOut).
func (s *thinkStripper) feed(delta string) (text, reasoning string) {
	text, reasoning = s.feedTagged(delta)
	plainText, plainReasoning := s.feedPlainText(text)
	return plainText, reasoning + plainReasoning
}

// feedTagged handles the provider's structured <think> tags.
func (s *thinkStripper) feedTagged(delta string) (text, reasoning string) {
	s.buf += delta
	for {
		if s.inThink {
			end, closeTag := firstTagIndex(s.buf, closeTags)
			if end == -1 {
				// Still inside think block — flush everything except the last
				// maxTagLen-1 bytes (might be a partial closing tag).
				safe := len(s.buf) - (maxTagLen - 1)
				if safe > 0 {
					reasoning += s.buf[:safe]
					s.buf = s.buf[safe:]
				}
				return
			}
			reasoning += s.buf[:end]
			s.buf = s.buf[end+len(closeTag):]
			s.inThink = false
		} else {
			start, openTag := firstTagIndex(s.buf, openTags)
			if start == -1 {
				// No open tag — check if buf ends with a partial open-tag prefix.
				if pLen := endsWithPartialOpenTag(s.buf); pLen > 0 {
					text += stripStrayCloseTags(s.buf[:len(s.buf)-pLen])
					s.buf = s.buf[len(s.buf)-pLen:]
					return
				}
				// No tag at all — emit as text, stripping any stray close tags.
				text += stripStrayCloseTags(s.buf)
				s.buf = ""
				return
			}
			// Strip stray close tags that appear BEFORE the opening tag.
			before := stripStrayCloseTags(s.buf[:start])
			text += before
			s.buf = s.buf[start+len(openTag):]
			s.inThink = true
		}
	}
}

func plainMarkerIndex(s string, markers ...string) (int, string) {
	best, found := -1, ""
	for _, marker := range markers {
		if i := strings.Index(strings.ToLower(s), strings.ToLower(marker)); i >= 0 && (best < 0 || i < best) {
			best, found = i, marker
		}
	}
	return best, found
}

// feedPlainText recovers the common fallback format emitted by models that
// write reasoning into `content`: prose/Thought: ... Answer: .... Without this
// boundary, internal deliberation becomes part of the final assistant answer.
func (s *thinkStripper) feedPlainText(text string) (answer, reasoning string) {
	if text == "" {
		return "", ""
	}
	s.plainBuf += text
	for {
		if s.plainMode == 0 {
			if i, _ := plainMarkerIndex(s.plainBuf, "Thought:", "Let's refine the thought"); i >= 0 {
				reasoning += s.plainBuf
				s.plainBuf = ""
				s.plainMode = 1
				return answer, reasoning
			}
			// Do not delay ordinary content. A prose mention of "thought" is
			// the useful signal that this may be a leaked reasoning transcript;
			// otherwise preserve normal streaming latency and strip stray tags
			// exactly as the original parser did.
			lower := strings.ToLower(s.plainBuf)
			if !strings.Contains(lower, "thought") && !strings.Contains(lower, "answer") {
				answer, s.plainBuf = s.plainBuf, ""
				return answer, reasoning
			}
			// Keep enough tail to recognize a marker split across SSE chunks,
			// while allowing ordinary answers to stream with bounded latency.
			if len(s.plainBuf) > 4096 {
				n := len(s.plainBuf) - 128
				answer += s.plainBuf[:n]
				s.plainBuf = s.plainBuf[n:]
				continue
			}
			return answer, reasoning
		}
		if s.plainMode == 1 {
			if i, marker := plainMarkerIndex(s.plainBuf, "Final Answer:", "Answer:"); i >= 0 {
				reasoning += s.plainBuf[:i+len(marker)]
				s.plainBuf = s.plainBuf[i+len(marker):]
				s.plainMode = 2
				continue
			}
			safe := len(s.plainBuf) - 128
			if safe > 0 {
				reasoning += s.plainBuf[:safe]
				s.plainBuf = s.plainBuf[safe:]
			}
			return answer, reasoning
		}
		answer += s.plainBuf
		s.plainBuf = ""
		return answer, reasoning
	}
}

// flush drains any bytes still held back for partial-tag detection.
// Must be called when the stream ends — otherwise trailing bytes are lost.
func (s *thinkStripper) flush() (text, reasoning string) {
	tagText, tagReasoning := s.flushTagged()
	plainText, plainReasoning := s.feedPlainText(tagText)
	text, reasoning = plainText, tagReasoning+plainReasoning
	if s.plainBuf != "" {
		if s.plainMode == 1 {
			reasoning += s.plainBuf
		} else {
			text += s.plainBuf
		}
		s.plainBuf = ""
	}
	return
}

func (s *thinkStripper) flushTagged() (text, reasoning string) {
	if s.buf == "" {
		return "", ""
	}
	if s.inThink {
		reasoning = s.buf
	} else {
		text = s.buf
	}
	s.buf = ""
	return
}

// toolCallTagRe matches Hermes/Qwen-style inline tool-call syntax that some
// chat templates emit as plain text — <tool_call>{"name":...,"arguments":...}</tool_call>
// — when the server never populated the structured tool_calls delta.
var (
	toolCallTagRe     = regexp.MustCompile(`(?s)<tool_call>\s*(\{.*?\})\s*</tool_call>`)
	toolCodeTagRe     = regexp.MustCompile(`(?s)<tool_code>\s*(.*?)\s*</tool_code>`)
	toolCodeCall      = regexp.MustCompile(`(?s)^\s*([A-Za-z_][A-Za-z0-9_.:-]*)\s*\((.*)\)\s*$`)
	toolJSONFence     = regexp.MustCompile("(?s)```(?:json)?\\s*(\\{.*?\\})\\s*```")
	functionCallTagRe = regexp.MustCompile(`(?s)<function_call>\s*<function_name>\s*([^<]+?)\s*</function_name>\s*<function_parameters>\s*(\{.*?\})\s*</function_parameters>\s*</function_call>`)
)

// extractInlineToolCalls is a defense-in-depth fallback: if a turn ended
// with zero structured tool_calls but the raw text contains inline
// <tool_call>{...}</tool_call> blocks (a known leak when llama.cpp's
// template renders tool-call intent as text instead of a delta), parse
// them into synthetic tool calls so the intent isn't silently lost.
func extractInlineToolCalls(raw string) []struct{ name, args string } {
	matches := toolCallTagRe.FindAllStringSubmatch(raw, -1)
	var out []struct{ name, args string }
	for _, m := range matches {
		var call struct {
			Name      string          `json:"name"`
			Arguments json.RawMessage `json:"arguments"`
		}
		if err := json.Unmarshal([]byte(m[1]), &call); err != nil || call.Name == "" {
			continue
		}
		args := string(call.Arguments)
		if args == "" {
			args = "{}"
		}
		out = append(out, struct{ name, args string }{name: call.Name, args: args})
	}
	for _, match := range toolCodeTagRe.FindAllStringSubmatch(raw, -1) {
		for _, statement := range splitToolCodeStatements(match[1]) {
			parsed := toolCodeCall.FindStringSubmatch(statement)
			if len(parsed) != 3 {
				continue
			}
			args, ok := parseToolCodeArguments(parsed[2])
			if !ok {
				continue
			}
			out = append(out, struct{ name, args string }{
				name: parsed[1],
				args: args,
			})
		}
	}
	for _, tag := range extractInvokeTags(raw) {
		name, ok := quotedXMLAttribute(tag, "name")
		if !ok || !toolCodeCall.MatchString(name+"()") {
			continue
		}
		args, ok := quotedXMLAttribute(tag, "arguments")
		if !ok {
			args = "{}"
		}
		var object map[string]any
		if json.Unmarshal([]byte(args), &object) != nil {
			continue
		}
		encoded, _ := json.Marshal(object)
		out = append(out, struct{ name, args string }{name: name, args: string(encoded)})
	}
	for _, match := range toolJSONFence.FindAllStringSubmatch(raw, -1) {
		var call struct {
			Name      string          `json:"name"`
			Arguments json.RawMessage `json:"arguments"`
		}
		if json.Unmarshal([]byte(match[1]), &call) != nil || strings.TrimSpace(call.Name) == "" {
			continue
		}
		var object map[string]any
		if len(call.Arguments) == 0 {
			call.Arguments = json.RawMessage(`{}`)
		}
		if json.Unmarshal(call.Arguments, &object) != nil {
			continue
		}
		encoded, _ := json.Marshal(object)
		out = append(out, struct{ name, args string }{name: call.Name, args: string(encoded)})
	}
	for _, match := range functionCallTagRe.FindAllStringSubmatch(raw, -1) {
		name := strings.TrimSpace(match[1])
		var object map[string]any
		if name == "" || json.Unmarshal([]byte(match[2]), &object) != nil {
			continue
		}
		encoded, _ := json.Marshal(object)
		out = append(out, struct{ name, args string }{name: name, args: string(encoded)})
	}
	return out
}

func extractInvokeTags(raw string) []string {
	var tags []string
	for {
		start := strings.Index(raw, "<invoke")
		if start < 0 {
			return tags
		}
		raw = raw[start:]
		end := strings.Index(raw, "/>")
		if end < 0 {
			return tags
		}
		tags = append(tags, raw[:end+2])
		raw = raw[end+2:]
	}
}

func quotedXMLAttribute(tag, name string) (string, bool) {
	marker := name + "="
	index := strings.Index(tag, marker)
	if index < 0 {
		return "", false
	}
	index += len(marker)
	if index >= len(tag) || (tag[index] != '\'' && tag[index] != '"') {
		return "", false
	}
	quote := tag[index]
	start := index + 1
	for index = start; index < len(tag); index++ {
		if tag[index] == quote && (index == start || tag[index-1] != '\\') {
			return tag[start:index], true
		}
	}
	return "", false
}

// splitToolCodeStatements separates newline/semicolon-delimited calls while
// preserving delimiters inside quoted strings and nested JSON values.
func splitToolCodeStatements(raw string) []string {
	return splitToolCodeTopLevel(raw, func(r byte) bool { return r == '\n' || r == ';' })
}

// parseToolCodeArguments converts the Python-like keyword syntax emitted by
// Qwen/Bonsai templates (tool(arg="value", limit=3)) into JSON arguments.
// A raw JSON object is also accepted. Invalid syntax is deliberately rejected:
// recovery must never guess a mutation's arguments.
func parseToolCodeArguments(raw string) (string, bool) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "{}", true
	}
	if strings.HasPrefix(raw, "{") {
		var object map[string]any
		if json.Unmarshal([]byte(raw), &object) == nil {
			encoded, _ := json.Marshal(object)
			return string(encoded), true
		}
		return "", false
	}

	values := make(map[string]any)
	for _, field := range splitToolCodeTopLevel(raw, func(r byte) bool { return r == ',' }) {
		key, encoded, ok := strings.Cut(field, "=")
		key = strings.TrimSpace(key)
		if !ok || !regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`).MatchString(key) {
			return "", false
		}
		value, ok := parseToolCodeValue(strings.TrimSpace(encoded))
		if !ok {
			return "", false
		}
		values[key] = value
	}
	encoded, err := json.Marshal(values)
	return string(encoded), err == nil
}

func parseToolCodeValue(raw string) (any, bool) {
	if raw == "" {
		return nil, false
	}
	var value any
	if json.Unmarshal([]byte(raw), &value) == nil {
		return value, true
	}
	switch raw {
	case "True":
		return true, true
	case "False":
		return false, true
	case "None":
		return nil, true
	}
	if len(raw) >= 2 && raw[0] == '\'' && raw[len(raw)-1] == '\'' {
		// strconv.Unquote understands the escapes we need once the surrounding
		// Python single quotes are represented as a Go double-quoted string.
		escaped := strings.ReplaceAll(raw[1:len(raw)-1], `"`, `\"`)
		decoded, err := strconv.Unquote(`"` + escaped + `"`)
		return decoded, err == nil
	}
	return nil, false
}

func splitToolCodeTopLevel(raw string, delimiter func(byte) bool) []string {
	var parts []string
	start, depth := 0, 0
	var quote byte
	escaped := false
	for index := 0; index < len(raw); index++ {
		char := raw[index]
		if quote != 0 {
			if escaped {
				escaped = false
			} else if char == '\\' {
				escaped = true
			} else if char == quote {
				quote = 0
			}
			continue
		}
		switch char {
		case '\'', '"':
			quote = char
		case '(', '[', '{':
			depth++
		case ')', ']', '}':
			if depth > 0 {
				depth--
			}
		default:
			if depth == 0 && delimiter(char) {
				if part := strings.TrimSpace(raw[start:index]); part != "" {
					parts = append(parts, part)
				}
				start = index + 1
			}
		}
	}
	if quote != 0 || depth != 0 {
		return nil
	}
	if part := strings.TrimSpace(raw[start:]); part != "" {
		parts = append(parts, part)
	}
	return parts
}

// inlineToolStripper keeps textual fallback tool syntax out of the visible
// assistant stream. rawAll still retains it for extractInlineToolCalls.
type inlineToolStripper struct {
	buf      string
	closeTag string
}

var inlineToolOpenTags = []string{"<function_call>", "<tool_call>", "<tool_code>", "<invoke"}

func (stripper *inlineToolStripper) feed(fragment string) string {
	stripper.buf += fragment
	var visible strings.Builder
	for {
		if stripper.closeTag != "" {
			index := strings.Index(stripper.buf, stripper.closeTag)
			if index < 0 {
				if len(stripper.buf) > len(stripper.closeTag) {
					stripper.buf = stripper.buf[len(stripper.buf)-len(stripper.closeTag):]
				}
				return visible.String()
			}
			stripper.buf = stripper.buf[index+len(stripper.closeTag):]
			stripper.closeTag = ""
			continue
		}
		index, tag := firstTagIndex(stripper.buf, inlineToolOpenTags)
		if index >= 0 {
			visible.WriteString(stripper.buf[:index])
			stripper.buf = stripper.buf[index+len(tag):]
			switch tag {
			case "<function_call>":
				stripper.closeTag = "</function_call>"
			case "<tool_call>":
				stripper.closeTag = "</tool_call>"
			case "<tool_code>":
				stripper.closeTag = "</tool_code>"
			case "<invoke":
				stripper.closeTag = "/>"
			}
			continue
		}
		keep := partialTagSuffixLen(stripper.buf, inlineToolOpenTags)
		visible.WriteString(stripper.buf[:len(stripper.buf)-keep])
		stripper.buf = stripper.buf[len(stripper.buf)-keep:]
		return visible.String()
	}
}

func (stripper *inlineToolStripper) flush() string {
	if stripper.closeTag != "" {
		stripper.buf = ""
		return ""
	}
	out := stripper.buf
	stripper.buf = ""
	return out
}

func partialTagSuffixLen(value string, tags []string) int {
	best := 0
	for _, tag := range tags {
		for size := 1; size < len(tag) && size <= len(value); size++ {
			if strings.HasSuffix(value, tag[:size]) && size > best {
				best = size
			}
		}
	}
	return best
}

// stallTimeout is how long the SSE reader waits for any byte before treating
// the stream as stalled. llama.cpp can be slow generating large file writes,
// so we give it 90 seconds of silence before giving up. Generating one token
// per second on slow hardware is normal; 90 s catches genuine hangs while
// not interrupting real-but-slow generation.
const stallTimeout = 90 * time.Second

// overthinkReasoningLimit is the default pure-reasoning spiral ceiling for
// tool-using turns (see readSSE). plannerOverthinkReasoningLimit is used for
// tool-less planner passes that legitimately think before emitting a plan.
// Both sit well above the agent-loop effort caps and the server
// --reasoning-budget so healthy short thinks are never provider-killed.
const (
	overthinkReasoningLimit        = 24000
	plannerOverthinkReasoningLimit = 48000
)

// watchdogReader wraps an io.Reader and cancels via cancelFn if no bytes
// arrive within stallTimeout. It resets the timer on every successful read
// so slow-but-steady generation is never killed — only true stalls are caught.
type watchdogReader struct {
	r        io.Reader
	timer    *time.Timer
	cancelFn context.CancelFunc
}

func newWatchdogReader(r io.Reader, cancelFn context.CancelFunc) *watchdogReader {
	w := &watchdogReader{r: r, cancelFn: cancelFn}
	w.timer = time.AfterFunc(stallTimeout, func() {
		cancelFn()
	})
	return w
}

func (w *watchdogReader) Read(p []byte) (int, error) {
	n, err := w.r.Read(p)
	if n > 0 {
		w.timer.Reset(stallTimeout) // bytes arrived — reset stall clock
	}
	return n, err
}

func (w *watchdogReader) stop() {
	w.timer.Stop()
}

// readSSE reads the SSE stream from llama-server and emits normalized events.
func (p *Provider) readSSE(ctx context.Context, r io.Reader, ch chan<- sonutruntime.StreamEvent) {
	// Wrap reader with a watchdog: if no bytes arrive for stallTimeout, cancel
	// the context so the goroutine unblocks and the agent loop can retry.
	watchCtx, watchCancel := context.WithCancel(ctx)
	defer watchCancel()
	wd := newWatchdogReader(r, watchCancel)
	defer wd.stop()

	scanner := bufio.NewScanner(wd)
	// Increase scanner buffer to 4MB so large edit_file/write_file arguments
	// (thousands of lines) never silently overflow the default 64KB limit.
	const maxScanBuf = 4 * 1024 * 1024
	scanner.Buffer(make([]byte, 64*1024), maxScanBuf)

	// Track open tool calls by index so we can emit proper start/delta/end events
	type openCall struct {
		id   string
		name string
		args strings.Builder
	}
	openCalls := map[int]*openCall{}
	stripper := &thinkStripper{}
	inlineStripper := &inlineToolStripper{}
	// Overthink guard: reasoning models can spiral — drafting the entire answer
	// inside thinking until the token budget truncates with EMPTY content.
	// Abort as a stall so the agent loop reissues with an act-now nudge.
	// Thinking stays ON on the retry (quality + hybrid cache stability) —
	// disableThinkingOnce is kept only as a last-resort escape hatch and is
	// no longer armed here. Limit sits ABOVE CollectStream effort caps so
	// the agent loop remains the authoritative budget for normal turns.
	limit := int(p.overthinkLimit.Load())
	if limit <= 0 {
		limit = overthinkReasoningLimit
	}
	reasoningChars := 0
	var reasoningAll strings.Builder
	sawOutput := false
	// rawAll accumulates every content/reasoning_content byte exactly as the
	// server sent it (pre-think-stripping) so we can recover an inline
	// <tool_call>{...}</tool_call> block if the structured field never fired.
	var rawAll strings.Builder
	sawStructuredToolCall := false

	// Perf counters for ~/.atcode/perf.log (always-on unless SONUT_PERF=0).
	streamStart := time.Now()
	var firstByteAt, firstTokenAt time.Time
	var lastPrefillProcessed, lastPrefillTotal, lastPrefillCache int
	var promptTok, completionTok int
	textChars, toolCallCount := 0, 0
	defer func() {
		elapsed := time.Since(streamStart)
		ttftMs := int64(0)
		if !firstTokenAt.IsZero() {
			ttftMs = firstTokenAt.Sub(streamStart).Milliseconds()
		}
		prefillMs := int64(0)
		if !firstTokenAt.IsZero() {
			prefillMs = ttftMs
		} else if !firstByteAt.IsZero() {
			prefillMs = firstByteAt.Sub(streamStart).Milliseconds()
		}
		decodeMs := int64(0)
		tokPerSec := 0.0
		if !firstTokenAt.IsZero() {
			decodeMs = time.Since(firstTokenAt).Milliseconds()
			if decodeMs > 0 && completionTok > 0 {
				tokPerSec = float64(completionTok) / (float64(decodeMs) / 1000.0)
			}
		}
		cacheHitPct := 0.0
		if lastPrefillTotal > 0 && lastPrefillCache > 0 {
			cacheHitPct = 100.0 * float64(lastPrefillCache) / float64(lastPrefillTotal)
		}
		observability.PerfLog("inference_end",
			"elapsed_ms", elapsed.Milliseconds(),
			"ttft_ms", ttftMs,
			"prefill_ms", prefillMs,
			"decode_ms", decodeMs,
			"prompt_tok", promptTok,
			"completion_tok", completionTok,
			"prefill_total", lastPrefillTotal,
			"prefill_processed", lastPrefillProcessed,
			"prefill_cache", lastPrefillCache,
			"cache_hit_pct", fmt.Sprintf("%.1f", cacheHitPct),
			"tok_s", fmt.Sprintf("%.1f", tokPerSec),
			"reasoning_chars", reasoningChars,
			"text_chars", textChars,
			"tool_calls", toolCallCount,
			"saw_output", sawOutput,
		)
	}()

	emit := func(ev sonutruntime.StreamEvent) {
		select {
		case ch <- ev:
		case <-ctx.Done():
		}
	}

	// emitInlineFallback runs when a turn ended with zero structured tool
	// calls: it scans the raw text for Hermes/Qwen-style <tool_call> blocks
	// the template may have emitted as plain text and synthesizes proper
	// tool-call events from them so the intent isn't silently dropped.
	emitInlineFallback := func() {
		if sawStructuredToolCall {
			return
		}
		for index, call := range extractInlineToolCalls(rawAll.String()) {
			id := fmt.Sprintf("inline-%d-%s", index+1, call.name)
			emit(sonutruntime.StreamEvent{Type: sonutruntime.StreamEventToolCallStart, ToolCallID: id, ToolName: call.name})
			emit(sonutruntime.StreamEvent{Type: sonutruntime.StreamEventToolCallDelta, ToolCallID: id, ToolName: call.name, ArgumentsFragment: call.args})
			emit(sonutruntime.StreamEvent{Type: sonutruntime.StreamEventToolCallEnd, ToolCallID: id, ToolName: call.name, ArgumentsFragment: call.args})
		}
	}

	doneEvent := func(finishReason string) sonutruntime.StreamEvent {
		event := sonutruntime.StreamEvent{
			Type:         sonutruntime.StreamEventDone,
			FinishReason: finishReason,
		}
		if reasoningAll.Len() > 0 {
			event.ReasoningBlocks = []sonutruntime.ReasoningBlock{{
				Provider: "llama",
				Type:     "reasoning_content",
				Text:     reasoningAll.String(),
			}}
		}
		return event
	}

	// flushStripper drains any remaining buffered stripper bytes as final
	// text/reasoning events. Must run before every stream-terminating return.
	flushStripper := func() {
		textOut, reasoningOut := stripper.flush()
		textOut = inlineStripper.feed(textOut) + inlineStripper.flush()
		if textOut != "" {
			emit(sonutruntime.StreamEvent{Type: sonutruntime.StreamEventText, Content: textOut})
		}
		if reasoningOut != "" {
			reasoningAll.WriteString(reasoningOut)
			emit(sonutruntime.StreamEvent{Type: sonutruntime.StreamEventReasoning, Content: reasoningOut})
		}
	}

	for scanner.Scan() {
		if watchCtx.Err() != nil {
			// Either the parent context was cancelled, or the watchdog fired.
			// Emit a stall error so the agent loop's stall-retry path triggers.
			if ctx.Err() == nil {
				emit(sonutruntime.StreamEvent{
					Type:  sonutruntime.StreamEventError,
					Error: "stream stalled: no output for " + stallTimeout.String(),
				})
			}
			return
		}
		line := scanner.Text()
		if !strings.HasPrefix(line, "data: ") {
			continue
		}
		data := strings.TrimPrefix(line, "data: ")
		if data == "[DONE]" {
			// Close any open tool calls
			for idx, call := range openCalls {
				emit(sonutruntime.StreamEvent{
					Type:              sonutruntime.StreamEventToolCallEnd,
					ToolCallID:        call.id,
					ToolName:          call.name,
					ArgumentsFragment: call.args.String(),
				})
				delete(openCalls, idx)
			}
			flushStripper()
			emitInlineFallback()
			emit(doneEvent(""))
			return
		}

		var chunk sseChunk
		if err := json.Unmarshal([]byte(data), &chunk); err != nil {
			continue
		}

		// A prefill-progress chunk proves the server is alive mid-prompt-
		// processing: forward it as a ping so the event-level silence tracker
		// (CollectStream maxSilence) doesn't abort a long healthy prefill the
		// way the byte-level watchdog no longer does.
		if chunk.PromptProgress != nil {
			if firstByteAt.IsZero() {
				firstByteAt = time.Now()
			}
			lastPrefillProcessed = chunk.PromptProgress.Processed
			lastPrefillTotal = chunk.PromptProgress.Total
			// llama.cpp prompt_progress may include a cache field in some builds;
			// capture via raw JSON when present (see sseChunk.PromptProgress).
			if chunk.PromptProgress.Cache > 0 {
				lastPrefillCache = chunk.PromptProgress.Cache
			}
			emit(sonutruntime.StreamEvent{
				Type:             sonutruntime.StreamEventPing,
				PrefillProcessed: chunk.PromptProgress.Processed,
				PrefillTotal:     chunk.PromptProgress.Total,
			})
		}

		// Emit usage if present
		if chunk.Usage != nil {
			promptTok = chunk.Usage.PromptTokens
			completionTok = chunk.Usage.CompletionTokens
			emit(sonutruntime.StreamEvent{
				Type: sonutruntime.StreamEventUsage,
				Usage: sonutruntime.Usage{
					InputTokens:  chunk.Usage.PromptTokens,
					OutputTokens: chunk.Usage.CompletionTokens,
					// llama.cpp reports reasoning as part of completion_tokens.
					// Keep the subset visible to the usage/eco breakdown too;
					// TotalTokens remains unchanged because reasoning is not added.
					ReasoningTokens: min(chunk.Usage.CompletionTokens, reasoningChars/4),
				},
			})
		}

		for _, choice := range chunk.Choices {
			// Text delta — prefer Content; fall back to ReasoningContent for
			// thinking models (e.g. sonut-1/QwQ/DeepSeek-R1) that emit all
			// output in the reasoning_content field.
			if choice.Delta.Content != "" {
				if firstByteAt.IsZero() {
					firstByteAt = time.Now()
				}
				rawAll.WriteString(choice.Delta.Content)
				textOut, reasoningOut := stripper.feed(choice.Delta.Content)
				textOut = inlineStripper.feed(textOut)
				if textOut != "" {
					if firstTokenAt.IsZero() {
						firstTokenAt = time.Now()
					}
					sawOutput = true
					textChars += len(textOut)
					emit(sonutruntime.StreamEvent{Type: sonutruntime.StreamEventText, Content: textOut})
				}
				if reasoningOut != "" {
					if firstTokenAt.IsZero() {
						firstTokenAt = time.Now()
					}
					reasoningAll.WriteString(reasoningOut)
					reasoningChars += len(reasoningOut)
					emit(sonutruntime.StreamEvent{Type: sonutruntime.StreamEventReasoning, Content: reasoningOut})
				}
			}
			if choice.Delta.ReasoningContent != "" {
				if firstByteAt.IsZero() {
					firstByteAt = time.Now()
				}
				if firstTokenAt.IsZero() {
					firstTokenAt = time.Now()
				}
				rawAll.WriteString(choice.Delta.ReasoningContent)
				reasoningAll.WriteString(choice.Delta.ReasoningContent)
				reasoningChars += len(choice.Delta.ReasoningContent)
				emit(sonutruntime.StreamEvent{
					Type:    sonutruntime.StreamEventReasoning,
					Content: choice.Delta.ReasoningContent,
				})
			}
			if !sawOutput && len(choice.Delta.ToolCalls) > 0 {
				if firstTokenAt.IsZero() {
					firstTokenAt = time.Now()
				}
				sawOutput = true
			}
			if !sawOutput && reasoningChars > limit {
				// Reasoning spiral: nothing but thinking past the limit. Abort as
				// a stall (the needle "stream stalled" drives the agent loop's
				// safe reissue). Thinking stays ON on the retry — killing it
				// caused dumber answers and hybrid KV cache thrash.
				emit(sonutruntime.StreamEvent{
					Type:  sonutruntime.StreamEventError,
					Error: "stream stalled: reasoning overrun — retrying with an act-now nudge",
				})
				return
			}

			// Tool call deltas (tool_calls lives inside delta per OpenAI spec)
			for _, tc := range choice.Delta.ToolCalls {
				sawStructuredToolCall = true
				call, exists := openCalls[tc.Index]
				if !exists {
					call = &openCall{id: tc.ID, name: tc.Function.Name}
					openCalls[tc.Index] = call
					toolCallCount++
					if firstTokenAt.IsZero() {
						firstTokenAt = time.Now()
					}
					emit(sonutruntime.StreamEvent{
						Type:       sonutruntime.StreamEventToolCallStart,
						ToolCallID: tc.ID,
						ToolName:   tc.Function.Name,
					})
				}
				if tc.Function.Arguments != "" {
					call.args.WriteString(tc.Function.Arguments)
					emit(sonutruntime.StreamEvent{
						Type:              sonutruntime.StreamEventToolCallDelta,
						ToolCallID:        call.id,
						ToolName:          call.name,
						ArgumentsFragment: tc.Function.Arguments,
					})
				}
			}

			// Finish reason
			switch choice.FinishReason {
			case "stop", "eos":
				// Close open tool calls before done
				for idx, call := range openCalls {
					emit(sonutruntime.StreamEvent{
						Type:              sonutruntime.StreamEventToolCallEnd,
						ToolCallID:        call.id,
						ToolName:          call.name,
						ArgumentsFragment: call.args.String(),
					})
					delete(openCalls, idx)
				}
				flushStripper()
				emitInlineFallback()
				emit(doneEvent(""))
				return
			case "tool_calls":
				for idx, call := range openCalls {
					emit(sonutruntime.StreamEvent{
						Type:              sonutruntime.StreamEventToolCallEnd,
						ToolCallID:        call.id,
						ToolName:          call.name,
						ArgumentsFragment: call.args.String(),
					})
					delete(openCalls, idx)
				}
			case "length":
				flushStripper()
				emit(doneEvent(sonutruntime.FinishReasonLength))
				return
			}
		}
	}

	flushStripper()
	if err := scanner.Err(); err != nil && ctx.Err() == nil && watchCtx.Err() == nil {
		emit(sonutruntime.StreamEvent{
			Type:  sonutruntime.StreamEventError,
			Error: err.Error(),
		})
	}
}
