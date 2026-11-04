package agent

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/aistatine/sonut/internal/sonutruntime"
	"github.com/aistatine/sonut/internal/tools"
)

// Local hybrid-attention models (atcode / Bonsai / Qwopus / OmniCoder) re-prefill
// the full prompt every turn — llama.cpp cache_prompt cannot roll back their
// recurrent state. Measured on M-series 16 GiB: ~60 tok/s prompt processing, so
// every extra kilobyte of tool dump or a re-encoded vision image costs seconds
// to minutes of "Catching sparks / ↑ 0 tok" after Explore finishes.
//
// These helpers keep follow-up turns lean without changing the transcript the
// user sees (mutations apply only to the request copy).

const (
	// localToolResultMaxBytes caps each tool body on the wire for hybrid local
	// follow-ups. Measured live (2026-07-21): 4×24 KB reads → 8k tokens → 134 s
	// TTFT with cache_hit 0% (hybrid re-prefills full history). 2 KiB keeps a
	// useful head of each dump; model re-reads with start_line/end_line.
	// 1.5 KiB head+tail per tool: measured supermario builder runs still hit
	// ~90s TTFT with 2 KiB × keep-last-3 after schema tax. Shave the body
	// further; model re-reads with start_line/end_line when it needs more.
	localToolResultMaxBytes = 1536
	// localToolBatchBudgetBytes is the shared budget across ALL tool bodies in
	// the request copy (qwen-code final-tool-response-budget). Per-tool caps
	// alone still let N under-cap results dominate prefill; fair allocation
	// shrinks the large ones together while leaving small results intact.
	// 6 KiB total ≈ ~1.5k tokens of tool dumps at most.
	localToolBatchBudgetBytes = 6 * 1024
	// localKeepLastToolResults: newest N tool bodies stay; older ones become
	// one-line placeholders. 2 is enough for "read then edit" without stacking
	// multi-file dumps that hybrid re-prefills from zero every turn.
	localKeepLastToolResults = 2
	// localKeepRecentToolExchanges bounds message/template overhead as a session
	// grows. Older completed assistant-tool pairs are represented by one compact
	// summary; the newest pairs remain structurally exact.
	localKeepRecentToolExchanges = 3
	// localPruneProtectRecentTokens keeps only the freshest tool output verbatim
	// (secondary pass after keep-last).
	localPruneProtectRecentTokens = 600
	// localPruneMinReclaimTokens: reclaim as soon as older dumps exceed this.
	localPruneMinReclaimTokens = 150
	// localPruneMinBodyTokens: bodies smaller than this are left alone.
	localPruneMinBodyTokens = 40
	// localPrunePreserveLast messages (any role) at the tail stay intact.
	localPrunePreserveLast = 2
)

// isLocalHybridRun reports whether this run should pay the hybrid prefill tax
// (full re-prefill every turn). Provider names are unreliable — the TUI used to
// overwrite AgentOptions.ProviderName with a cloud Preferences name while
// inference stayed on llama.cpp, which disabled image strip + tool clamps and
// caused multi-minute "↑ 0 tok" hangs. Match liberally.
func isLocalHybridRun(options Options) bool {
	if tools.IsLocalLlamaProviderName(options.ProviderName) {
		return true
	}
	m := strings.ToLower(strings.TrimSpace(options.Model))
	if m == "" {
		return false
	}
	for _, needle := range []string{"atcode", "bonsai", "qwopus", "omnicoder", "dspark"} {
		if strings.Contains(m, needle) {
			return true
		}
	}
	return false
}

// prepareLocalHybridFollowUp mutates a REQUEST copy of messages for a post-tool
// turn on a hybrid local backend:
//  1. Drops image bytes (CPU mmproj re-encodes them every request — measured
//     multi-minute stalls after Explore when the image stayed on the user turn).
//  2. Clamps oversized tool bodies.
//  3. Aggressively prunes older large tool dumps so only recent output is re-paid.
//
// The caller's transcript / session log is unaffected (callers pass a copy).
func prepareLocalHybridFollowUp(messages []sonutruntime.Message) []sonutruntime.Message {
	if len(messages) == 0 {
		return messages
	}
	out := copyMessages(messages)
	out = stripImagesForLocalFollowUp(out)
	out = squashOldLocalToolExchanges(out, localKeepRecentToolExchanges)
	out = compactLocalToolCallArguments(out)
	out = keepLastToolResultsOnly(out, localKeepLastToolResults)
	out = clampToolResultsForLocal(out)
	out, _ = pruneStaleToolOutputLocal(out, localPrunePreserveLast)
	return out
}

// squashOldLocalToolExchanges removes completed assistant/tool protocol pairs
// older than the newest keep results from the provider-facing COPY. The durable
// transcript remains untouched. This is safe because those tool bodies were
// already replaced with placeholders by the old strategy; retaining dozens of
// empty calls/results only paid chat-template and prefill cost without context.
func squashOldLocalToolExchanges(messages []sonutruntime.Message, keep int) []sonutruntime.Message {
	if keep < 0 {
		keep = 0
	}
	var toolIndexes []int
	for i := len(messages) - 1; i >= 0; i-- {
		if messages[i].Role == sonutruntime.MessageRoleTool {
			toolIndexes = append(toolIndexes, i)
		}
	}
	if len(toolIndexes) <= keep {
		return messages
	}

	cutoff := len(messages)
	keepIDs := make(map[string]bool)
	for i := 0; i < keep && i < len(toolIndexes); i++ {
		index := toolIndexes[i]
		if index < cutoff {
			cutoff = index
		}
		id := messages[index].ToolCallID
		if id == "" {
			// Without an ID we cannot prove which assistant call pairs with this
			// result. Keep the original structure rather than risk an invalid
			// replay transcript.
			return messages
		}
		keepIDs[id] = true
	}
	if keep == 0 {
		cutoff = len(messages)
	}

	insertBefore := cutoff
	for i := 0; i < cutoff; i++ {
		if messages[i].Role != sonutruntime.MessageRoleAssistant {
			continue
		}
		for _, call := range messages[i].ToolCalls {
			if keepIDs[call.ID] {
				insertBefore = i
				break
			}
		}
		if insertBefore == i {
			break
		}
	}

	type countedName struct {
		name  string
		count int
	}
	var counts []countedName
	positions := make(map[string]int)
	for _, index := range toolIndexes[keep:] {
		name := toolNameForResult(messages, index)
		if name == "" {
			name = "tool"
		}
		if pos, ok := positions[name]; ok {
			counts[pos].count++
		} else {
			positions[name] = len(counts)
			counts = append(counts, countedName{name: name, count: 1})
		}
	}
	parts := make([]string, 0, len(counts))
	for _, item := range counts {
		if item.count == 1 {
			parts = append(parts, item.name)
		} else {
			parts = append(parts, fmt.Sprintf("%s ×%d", item.name, item.count))
		}
	}
	summary := sonutruntime.Message{
		Role: sonutruntime.MessageRoleUser,
		Content: "[system] Earlier completed tool exchanges were compacted for local speed: " +
			strings.Join(parts, ", ") + ". Their old bodies are omitted; use the current workspace and recent results as source of truth.",
	}

	out := make([]sonutruntime.Message, 0, len(messages))
	inserted := false
	for i, message := range messages {
		if !inserted && i == insertBefore {
			out = append(out, summary)
			inserted = true
		}
		if message.Role == sonutruntime.MessageRoleTool && i < cutoff {
			continue
		}
		if message.Role == sonutruntime.MessageRoleAssistant && len(message.ToolCalls) > 0 {
			filtered := make([]sonutruntime.ToolCall, 0, len(message.ToolCalls))
			for _, call := range message.ToolCalls {
				if keepIDs[call.ID] || (call.ID == "" && i >= insertBefore) {
					filtered = append(filtered, call)
				}
			}
			message.ToolCalls = filtered
			if len(filtered) == 0 && strings.TrimSpace(message.Content) == "" && len(message.Reasoning) == 0 {
				continue
			}
		}
		out = append(out, message)
	}
	if !inserted {
		out = append(out, summary)
	}
	return out
}

// compactLocalToolCallArguments removes already-applied source bodies from
// replayed local tool calls. Keeping a 4KB write_file argument after its result
// is known forces the hybrid model to re-prefill that same source on every later
// turn. Paths and control flags remain intact; only large payload fields change
// in the ephemeral request copy.
func compactLocalToolCallArguments(messages []sonutruntime.Message) []sonutruntime.Message {
	for messageIndex := range messages {
		if messages[messageIndex].Role != sonutruntime.MessageRoleAssistant {
			continue
		}
		for callIndex := range messages[messageIndex].ToolCalls {
			call := &messages[messageIndex].ToolCalls[callIndex]
			var args map[string]any
			if json.Unmarshal([]byte(call.Arguments), &args) != nil {
				continue
			}
			var payloadKeys []string
			switch call.Name {
			case "write_file", "append_file":
				payloadKeys = []string{"content", "contents", "text", "body", "data", "file_content"}
			case "edit_file":
				payloadKeys = []string{"old_string", "new_string", "old", "new", "search", "replace"}
			case "apply_patch":
				payloadKeys = []string{"patch"}
			case "exec_command", "bash":
				payloadKeys = []string{"command", "cmd", "script"}
			default:
				continue
			}
			changed := false
			for _, key := range payloadKeys {
				value, ok := args[key].(string)
				if !ok || len(value) <= 256 {
					continue
				}
				args[key] = fmt.Sprintf("[omitted %d bytes after successful tool execution]", len(value))
				changed = true
			}
			if changed {
				if encoded, err := json.Marshal(args); err == nil {
					call.Arguments = string(encoded)
				}
			}
		}
	}
	return messages
}

// keepLastToolResultsOnly replaces every tool body except the newest keep
// results with a one-line placeholder. Hybrid local re-prefills the full
// history every turn — keeping 4× file dumps is the multi-minute inter-tool wait.
func keepLastToolResultsOnly(messages []sonutruntime.Message, keep int) []sonutruntime.Message {
	if keep < 0 {
		keep = 0
	}
	// Collect tool indices newest-first.
	var toolIdx []int
	for i := len(messages) - 1; i >= 0; i-- {
		if messages[i].Role == sonutruntime.MessageRoleTool {
			toolIdx = append(toolIdx, i)
		}
	}
	if len(toolIdx) <= keep {
		return messages
	}
	for _, i := range toolIdx[keep:] {
		if isPrunedPlaceholder(messages[i].Content) {
			continue
		}
		tokens := ApproxTextTokens(messages[i].Content)
		toolName := toolNameForResult(messages, i)
		messages[i].Content = prunedPlaceholder(toolName, tokens)
	}
	return messages
}

// requestMessagesForTurn builds the provider-facing message list. On hybrid
// local, any second+ model call (post-tool, redeepen after error, stall retry)
// strips images + clamps tool dumps. First call of a run keeps images so the
// model actually sees the screenshot once.
func requestMessagesForTurn(messages []sonutruntime.Message, guards *guardState, options Options, toolsAlreadyReturned bool) []sonutruntime.Message {
	msgs := requestMessages(messages, guards)
	if !isLocalHybridRun(options) {
		return msgs
	}
	if toolsAlreadyReturned || hasAssistantOrTool(msgs) {
		return prepareLocalHybridFollowUp(msgs)
	}
	return msgs
}

// hasAssistantOrTool reports whether history already contains a model/tool turn
// (i.e. this is not the first StreamCompletion of the run).
func hasAssistantOrTool(messages []sonutruntime.Message) bool {
	// Only inspect turns after the current user message. Persisted history can
	// contain assistant turns before it; treating those as an in-run follow-up
	// would strip the first request's image/prefill state on local hybrid runs.
	lastUser := -1
	for i := len(messages) - 1; i >= 0; i-- {
		if messages[i].Role == sonutruntime.MessageRoleUser {
			lastUser = i
			break
		}
	}
	for _, m := range messages[lastUser+1:] {
		switch m.Role {
		case sonutruntime.MessageRoleAssistant, sonutruntime.MessageRoleTool:
			return true
		}
	}
	return false
}

// stripImagesForLocalFollowUp removes image attachments from every message and
// leaves a one-line reminder on the first user turn that carried them so the
// model still knows a visual reference existed.
func stripImagesForLocalFollowUp(messages []sonutruntime.Message) []sonutruntime.Message {
	stripped := false
	for i := range messages {
		if len(messages[i].Images) == 0 {
			continue
		}
		n := len(messages[i].Images)
		messages[i].Images = nil
		stripped = true
		if messages[i].Role == sonutruntime.MessageRoleUser {
			note := fmt.Sprintf("\n\n[system] %d image attachment(s) WERE shown earlier this run — the user DID share a visual reference. Match it from memory; do NOT claim no image was attached. Prefer write_file/edit_file over re-describing. Re-read only the 1-2 files you still need.", n)
			if !strings.Contains(messages[i].Content, "image attachment(s) WERE shown earlier") &&
				!strings.Contains(messages[i].Content, "image attachment(s) were shown earlier") {
				messages[i].Content += note
			}
		}
	}
	if !stripped {
		return messages
	}
	return messages
}

// clampToolResultsForLocal shortens every tool body over localToolResultMaxBytes,
// then fair-allocates a shared batch budget across all tool bodies (qwen-code
// tool-response finalizer). Head+tail keep preserves structure and errors.
func clampToolResultsForLocal(messages []sonutruntime.Message) []sonutruntime.Message {
	note := "\n\n[truncated for local prefill speed — re-read with a tighter start_line/end_line/max_lines if you need more]"
	for i := range messages {
		if messages[i].Role != sonutruntime.MessageRoleTool {
			continue
		}
		if len(messages[i].Content) <= localToolResultMaxBytes {
			continue
		}
		// Head+tail: stack traces and command summaries often end with the error.
		capBody := localToolResultMaxBytes - len(note)
		if capBody < 64 {
			messages[i].Content = truncateUTF8(messages[i].Content, localToolResultMaxBytes)
			continue
		}
		messages[i].Content = tools.FitTextHeadTail(messages[i].Content, capBody, note)
	}
	// Shared batch budget so N under-cap results cannot still starve prefill.
	indexes := make([]int, 0, 8)
	bodies := make([]string, 0, 8)
	for i := range messages {
		if messages[i].Role != sonutruntime.MessageRoleTool {
			continue
		}
		if isPrunedPlaceholder(messages[i].Content) {
			continue
		}
		indexes = append(indexes, i)
		bodies = append(bodies, messages[i].Content)
	}
	if tools.ApplyBatchOutputBudget(bodies, localToolBatchBudgetBytes, "re-read with a tighter range if you need more") > 0 {
		for j, i := range indexes {
			messages[i].Content = bodies[j]
		}
	}
	return messages
}

// pruneStaleToolOutputLocal is the aggressive sibling of pruneStaleToolOutput for
// hybrid local follow-ups. Same shape, much lower thresholds so post-Explore
// turns do not re-prefill every file dump from the batch.
func pruneStaleToolOutputLocal(messages []sonutruntime.Message, preserveLast int) ([]sonutruntime.Message, int) {
	// Temporarily swap constants by inlining the walk with local thresholds.
	if len(messages) == 0 {
		return messages, 0
	}
	if preserveLast < 0 {
		preserveLast = 0
	}

	type candidate struct {
		index  int
		tokens int
	}
	var candidates []candidate
	recentToolTokens := 0
	reclaimable := 0
	protectUntil := len(messages) - preserveLast

	for i := len(messages) - 1; i >= 0; i-- {
		msg := messages[i]
		if msg.Role != sonutruntime.MessageRoleTool {
			continue
		}
		bodyTokens := ApproxTextTokens(msg.Content)
		if i >= protectUntil {
			recentToolTokens += bodyTokens
			continue
		}
		if recentToolTokens < localPruneProtectRecentTokens {
			recentToolTokens += bodyTokens
			continue
		}
		if bodyTokens < localPruneMinBodyTokens || isPrunedPlaceholder(msg.Content) {
			continue
		}
		switch toolNameForResult(messages, i) {
		case toolNameSkill, toolNameToolSearch:
			continue
		}
		candidates = append(candidates, candidate{index: i, tokens: bodyTokens})
		reclaimable += bodyTokens
	}

	if reclaimable < localPruneMinReclaimTokens || len(candidates) == 0 {
		return messages, 0
	}

	out := make([]sonutruntime.Message, len(messages))
	copy(out, messages)
	reclaimed := 0
	for _, c := range candidates {
		toolName := toolNameForResult(out, c.index)
		newBody := prunedPlaceholder(toolName, c.tokens)
		reclaimed += c.tokens - ApproxTextTokens(newBody)
		out[c.index].Content = newBody
	}
	return out, reclaimed
}

// truncateUTF8 returns a valid UTF-8 prefix of s of at most max bytes.
func truncateUTF8(s string, max int) string {
	if max <= 0 || len(s) <= max {
		return s
	}
	cut := max
	for cut > 0 && s[cut]&0xC0 == 0x80 {
		cut--
	}
	return s[:cut]
}
