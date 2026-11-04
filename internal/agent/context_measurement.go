package agent

import (
	"github.com/aistatine/sonut/internal/sonutruntime"
)

// Context budget.
//
// MeasureContext breaks a request's estimated token footprint into the
// categories that actually compete for the window — the system prompt, the
// advertised tool definitions, and the conversation messages — so the agent can
// report context utilization (e.g. "62% used: 4k system, 6k tools, 30k history")
// and reason about what to compact. Counts are estimates (see estimateTokens),
// not a real tokenizer: good for proportions and budget bars, not billing.

// ContextBreakdown is a per-category estimate of a request's token footprint.
type ContextBreakdown struct {
	SystemTokens  int     // leading system messages (prompt + injected context)
	ToolTokens    int     // advertised tool definitions
	MessageTokens int     // non-system conversation messages
	TotalTokens   int     // SystemTokens + ToolTokens + MessageTokens
	ContextWindow int     // model context window; 0 when unknown
	UsedFraction  float64 // TotalTokens / ContextWindow; 0 when window unknown
}

// MeasureContext estimates the per-category token footprint of a request: the
// leading system messages, the advertised tool definitions, and the remaining
// conversation messages.
func MeasureContext(messages []sonutruntime.Message, tools []sonutruntime.ToolDefinition, contextWindow int) ContextBreakdown {
	systemEnd := 0
	for systemEnd < len(messages) && messages[systemEnd].Role == sonutruntime.MessageRoleSystem {
		systemEnd++
	}

	breakdown := ContextBreakdown{
		SystemTokens:  estimateTokens(messages[:systemEnd]),
		MessageTokens: estimateTokens(messages[systemEnd:]),
		// Same estimator compaction uses (estimateToolDefTokens), so the
		// report and the compaction threshold can never drift apart.
		ToolTokens:    estimateToolDefTokens(tools),
		ContextWindow: contextWindow,
	}
	breakdown.TotalTokens = breakdown.SystemTokens + breakdown.ToolTokens + breakdown.MessageTokens
	if contextWindow > 0 {
		breakdown.UsedFraction = float64(breakdown.TotalTokens) / float64(contextWindow)
	}
	return breakdown
}
