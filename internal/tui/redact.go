package tui

import "strings"

// System-prompt echo redaction: the model occasionally quotes its own system
// prompt back in its reasoning ("System prompt says: You are @code…"). That
// text must never reach the screen. Applied ONLY to thinking blocks — final
// answers are shown verbatim — and only at display time; the underlying
// transcript/model output is untouched.
//
// A line is dropped when its normalized form (lowercased, whitespace and
// quote/backtick noise collapsed) contains any signature phrase from the
// system prompts, so partial or re-worded quotes still match.
var systemPromptSignatures = []string{
	"you are @code",
	"you are atcode",
	"terminal coding agent",
	"built by aistatine",
	"created by aistatine",
	"you are an ai (no gender)",
	"you are an ai",
	"do not call tools",
	"do not explore files",
	"system prompt says",
	"answer in one or two short sentences",
	"be friendly and direct",
	"think for at most",
	"long reasoning is forbidden",
}

// fakeToolCodeSignatures identify the common local-model failure mode where a
// model writes a Python/XML mock of a tool call into its private reasoning. A
// real call arrives through the structured provider event, so this text has no
// user-facing value and must never occupy the thinking panel.
var fakeToolCodeSignatures = []string{
	"tool_code",
	"def web_search(",
	"mock implementation",
	"mock search call",
	"in a real environment this would call an actual search api",
	"i will use the web_search tool",
	"i will search now",
	"results = web_search(",
}

// normalizeForRedaction lowercases and collapses whitespace/quote characters
// so fuzzy variants of the prompt text still hit a signature.
func normalizeForRedaction(line string) string {
	var b strings.Builder
	b.Grow(len(line))
	lastSpace := false
	for _, r := range strings.ToLower(line) {
		switch r {
		case '"', '\'', '`', '“', '”', '‘', '’', '*', '_':
			continue
		case ' ', '\t', '\r', '\n':
			if !lastSpace {
				b.WriteByte(' ')
				lastSpace = true
			}
		default:
			b.WriteRune(r)
			lastSpace = false
		}
	}
	return b.String()
}

func lineEchoesSystemPrompt(line string) bool {
	normalized := normalizeForRedaction(line)
	if normalized == "" {
		return false
	}
	for _, signature := range systemPromptSignatures {
		if strings.Contains(normalized, signature) {
			return true
		}
	}
	return false
}

func lineLooksLikeFakeToolCode(line string) bool {
	normalized := normalizeForRedaction(line)
	raw := strings.ToLower(line)
	if normalized == "" {
		return false
	}
	for _, signature := range fakeToolCodeSignatures {
		if strings.Contains(normalized, normalizeForRedaction(signature)) || strings.Contains(raw, strings.ToLower(signature)) {
			return true
		}
	}
	return false
}

func reasoningContainsFakeToolCode(text string) bool {
	for _, line := range strings.Split(text, "\n") {
		if lineLooksLikeFakeToolCode(line) {
			return true
		}
	}
	return false
}

// redactSystemPromptEcho removes lines that quote the system prompt. Applied
// at render time only.
func redactSystemPromptEcho(text string) string {
	if text == "" || (!lineEchoesSystemPrompt(text) && !reasoningContainsChecklist(text) && !reasoningContainsFakeToolCode(text)) {
		return text
	}
	lines := strings.Split(text, "\n")
	kept := lines[:0]
	inFakeToolCode := false
	for _, line := range lines {
		normalized := normalizeForRedaction(line)
		if strings.Contains(normalized, "<toolcode>") {
			inFakeToolCode = true
			continue
		}
		if inFakeToolCode {
			if strings.Contains(normalized, "</toolcode>") {
				inFakeToolCode = false
			}
			continue
		}
		if lineEchoesSystemPrompt(line) || reasoningLooksLikePromptOrChecklist(line) || lineLooksLikeFakeToolCode(line) {
			continue
		}
		kept = append(kept, line)
	}
	return strings.TrimSpace(strings.Join(kept, "\n"))
}

func reasoningContainsChecklist(text string) bool {
	for _, line := range strings.Split(text, "\n") {
		if reasoningLooksLikePromptOrChecklist(line) {
			return true
		}
	}
	return false
}
