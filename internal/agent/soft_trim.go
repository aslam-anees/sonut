package agent

import (
	"fmt"
	"strings"

	"github.com/aistatine/sonut/internal/sonutruntime"
	"github.com/aistatine/sonut/internal/tools"
)

// Soft-trim is the MiMo-Code "unlimited context" middle stage: reclaim large
// tool dumps without destroying them. Hard prune (placeholder) and LLM
// summarization sit above this. Soft-trim keeps head+tail so errors, file
// headers, and stack tails survive while middle noise is dropped — zero model
// cost, high accuracy retention.

const (
	// softTrimMinBodyTokens: only soft-trim bodies larger than this (chars≈tokens*4).
	softTrimMinBodyTokens = 1024
	// softTrimKeepHeadTokens / softTrimKeepTailTokens: verbatim ends to keep.
	softTrimKeepHeadTokens = 384
	softTrimKeepTailTokens = 384
	// softTrimProtectRecentTokens: trailing tool-output window left intact.
	softTrimProtectRecentTokens = 40000
	// softTrimMinReclaimTokens: skip the pass if reclaimable mass is tiny.
	softTrimMinReclaimTokens = 8000
)

func softTrimMarker(originalTokens int) string {
	return fmt.Sprintf("\n\n[… soft-trimmed tool output — kept first/last ~%d tokens of ~%d; re-run tool if you need the middle …]\n\n",
		softTrimKeepHeadTokens+softTrimKeepTailTokens, originalTokens)
}

func isSoftTrimmed(content string) bool {
	return strings.Contains(content, "[… soft-trimmed tool output")
}

// softTrimStaleToolOutput walks messages newest-first, protects the recent
// tool-output window and preserveLast messages, then head+tail-trims older
// large tool bodies. Idempotent; never drops messages; never mutates input.
func softTrimStaleToolOutput(messages []sonutruntime.Message, preserveLast int) ([]sonutruntime.Message, int) {
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
		if recentToolTokens < softTrimProtectRecentTokens {
			recentToolTokens += bodyTokens
			continue
		}
		if bodyTokens < softTrimMinBodyTokens || isPrunedPlaceholder(msg.Content) || isSoftTrimmed(msg.Content) {
			continue
		}
		switch toolNameForResult(messages, i) {
		case toolNameSkill, toolNameToolSearch:
			continue
		}
		// Potential savings after keep-head+tail.
		savings := bodyTokens - softTrimKeepHeadTokens - softTrimKeepTailTokens
		if savings < softTrimMinBodyTokens/2 {
			continue
		}
		candidates = append(candidates, candidate{index: i, tokens: bodyTokens})
		reclaimable += savings
	}

	if reclaimable < softTrimMinReclaimTokens || len(candidates) == 0 {
		return messages, 0
	}

	out := make([]sonutruntime.Message, len(messages))
	copy(out, messages)
	reclaimed := 0
	for _, c := range candidates {
		body := out[c.index].Content
		// Convert token budgets to rough byte budgets (non-space/4 ≈ tokens).
		headBytes := softTrimKeepHeadTokens * 4
		tailBytes := softTrimKeepTailTokens * 4
		maxKeep := headBytes + tailBytes + 200 // room for marker
		if len(body) <= maxKeep {
			continue
		}
		marker := softTrimMarker(c.tokens)
		trimmed := tools.FitTextHeadTail(body, headBytes+tailBytes+len(marker), marker)
		if len(trimmed) >= len(body) {
			continue
		}
		reclaimed += c.tokens - ApproxTextTokens(trimmed)
		out[c.index].Content = trimmed
	}
	if reclaimed <= 0 {
		return messages, 0
	}
	return out, reclaimed
}

// reclaimContextCheap runs the free (no-LLM) unlimited-context ladder:
//  1. soft-trim large old tool bodies (MiMo stage 1)
//  2. hard-prune remaining stale dumps to placeholders (existing prune)
//
// Returns the rewritten messages and total tokens reclaimed.
func reclaimContextCheap(messages []sonutruntime.Message, preserveLast int) ([]sonutruntime.Message, int) {
	total := 0
	if soft, n := softTrimStaleToolOutput(messages, preserveLast); n > 0 {
		messages = soft
		total += n
	}
	if pruned, n := pruneStaleToolOutput(messages, preserveLast); n > 0 {
		messages = pruned
		total += n
	}
	return messages, total
}
