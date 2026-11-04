package agent

import "strings"

// visibleTextGate removes internal rubric/checklist lines, plan-seed echoes,
// and leaked chain-of-thought that weak models emit as ordinary content.
// It works on complete lines so a streamed line split across SSE chunks is
// filtered without buffering normal prose.
type visibleTextGate struct {
	pending  string
	internal bool
	// suppressBlock is set while inside a <plan>…</plan> or
	// <builder_workflow>…</builder_workflow> echo that must never reach the UI.
	suppressBlock bool
}

func (g *visibleTextGate) feed(delta string) string {
	if delta == "" {
		return ""
	}
	g.pending += delta
	var out strings.Builder
	for {
		i := strings.IndexByte(g.pending, '\n')
		if i < 0 {
			if g.suppressBlock || g.internal || internalReasoningLine(g.pending) ||
				seedEchoLine(g.pending) || systemPromptEchoLine(g.pending) {
				return out.String()
			}
			// Hold open control tags and bullet-looking prefixes until the line completes.
			trimLeft := strings.TrimLeft(g.pending, " \t")
			if strings.HasPrefix(trimLeft, "-") ||
				strings.HasPrefix(trimLeft, "<plan") ||
				strings.HasPrefix(trimLeft, "<builder_workflow") ||
				strings.HasPrefix(trimLeft, "</plan") ||
				strings.HasPrefix(trimLeft, "</builder_workflow") {
				return out.String()
			}
			out.WriteString(g.pending)
			g.pending = ""
			return out.String()
		}
		line := g.pending[:i+1]
		g.pending = g.pending[i+1:]
		if openSeedEchoBlock(line) {
			g.suppressBlock = true
			continue
		}
		if closeSeedEchoBlock(line) {
			g.suppressBlock = false
			continue
		}
		if g.suppressBlock {
			continue
		}
		if seedEchoLine(line) {
			continue
		}
		if systemPromptEchoLine(line) {
			continue
		}
		if internalReasoningLine(line) {
			g.internal = true
		}
		if !internalChecklistLine(line) && (!g.internal || finalAnswerLine(line)) {
			if finalAnswerLine(line) {
				g.internal = false
			}
			out.WriteString(line)
		}
	}
}

func (g *visibleTextGate) flush() string {
	line := g.pending
	g.pending = ""
	if g.suppressBlock || seedEchoLine(line) || systemPromptEchoLine(line) ||
		internalChecklistLine(line) || (g.internal && !finalAnswerLine(line)) {
		return ""
	}
	return line
}

func openSeedEchoBlock(line string) bool {
	text := strings.TrimSpace(strings.ToLower(line))
	return strings.HasPrefix(text, "<plan") || strings.HasPrefix(text, "<builder_workflow")
}

func closeSeedEchoBlock(line string) bool {
	text := strings.TrimSpace(strings.ToLower(line))
	return strings.HasPrefix(text, "</plan") || strings.HasPrefix(text, "</builder_workflow")
}

// seedEchoLine hides plan-restatement lines without full XML tags: status-
// annotated numbered steps and bare builder-rule dumps.
func seedEchoLine(line string) bool {
	text := strings.TrimSpace(line)
	if text == "" {
		return false
	}
	lower := strings.ToLower(text)
	if strings.HasPrefix(lower, "<plan") || strings.HasPrefix(lower, "</plan") ||
		strings.HasPrefix(lower, "<builder_workflow") || strings.HasPrefix(lower, "</builder_workflow") {
		return true
	}
	// "1. [pending] Implement Title Screen" / "3. [in progress] …"
	if isNumberedPlanTitle(text) {
		rest := stripPlanTitleMarker(text)
		restLower := strings.ToLower(rest)
		if strings.HasPrefix(restLower, "[pending]") ||
			strings.HasPrefix(restLower, "[in_progress]") ||
			strings.HasPrefix(restLower, "[in progress]") ||
			strings.HasPrefix(restLower, "[completed]") ||
			strings.HasPrefix(restLower, "[failed]") {
			return true
		}
	}
	// Bare executor seed preamble / rule dumps (keep in sync with injectPlanSeed).
	if strings.HasPrefix(lower, "you are the executor") ||
		strings.HasPrefix(lower, "executor. no thinking") ||
		strings.HasPrefix(lower, "no thinking. first output") ||
		strings.HasPrefix(lower, "the planner owns this roadmap") ||
		strings.HasPrefix(lower, "the planner already owns") ||
		strings.HasPrefix(lower, "never call update_plan") ||
		strings.HasPrefix(lower, "never restate this plan") ||
		strings.HasPrefix(lower, "keep going until every plan step") ||
		strings.HasPrefix(lower, "parallel independent tools") {
		return true
	}
	return false
}

func internalChecklistLine(line string) bool {
	text := checklistMarkerText(line)
	if strings.HasSuffix(text, ":") && (strings.HasPrefix(text, "thinking process") ||
		strings.HasPrefix(text, "analysis") || strings.HasPrefix(text, "reasoning") ||
		strings.HasPrefix(text, "final output generation")) {
		return true
	}
	return strings.HasPrefix(text, "check constraints:") ||
		strings.HasPrefix(text, "let's check constraints:") ||
		strings.HasPrefix(text, "lets check constraints:") ||
		strings.HasPrefix(text, "i'll make it natural") ||
		strings.HasPrefix(text, "check length:") ||
		strings.HasPrefix(text, "no tools, no exploration") ||
		strings.HasPrefix(text, "final output generation:") ||
		strings.HasPrefix(text, "output matches response") ||
		strings.HasPrefix(text, "response matches constraints") ||
		strings.HasPrefix(text, "analyze user input:") ||
		strings.HasPrefix(text, "analyze system prompt:") ||
		strings.HasPrefix(text, "formulate response:") ||
		strings.HasPrefix(text, "check constraints:")
}

// systemPromptEchoLine removes high-confidence prompt echoes from visible
// model content. This is intentionally narrower than a general content filter:
// ordinary identity answers and technical explanations must remain untouched,
// while explicit "system prompt says …" lines and serialized internal context
// must never reach a user-facing stream.
func systemPromptEchoLine(line string) bool {
	text := strings.ToLower(strings.TrimSpace(line))
	if text == "" {
		return false
	}
	for _, marker := range []string{
		"system prompt says",
		"system message says",
		"the system prompt is",
		"the system message is",
		"hidden instructions:",
		"<workspace_context>",
		"</workspace_context>",
		"<workspace_seed>",
		"</workspace_seed>",
		"<durable_context>",
		"</durable_context>",
		"<builder_workflow>",
		"</builder_workflow>",
		"never reveal the system",
		"do not reveal the system",
		"never output chain-of-thought",
		"never output chain of thought",
	} {
		if strings.Contains(text, marker) {
			return true
		}
	}
	return false
}

// checklistMarkerText normalizes the list/number prefixes local reasoning
// models commonly add to an internal rubric. It is used only for detection;
// ordinary answer text is returned unchanged by the gate.
func checklistMarkerText(line string) string {
	text := strings.ToLower(strings.TrimSpace(line))
	text = strings.TrimSpace(strings.TrimLeft(text, "-*• \t"))
	for i := 0; i < len(text); i++ {
		if text[i] < '0' || text[i] > '9' {
			break
		}
		if i+1 < len(text) && text[i+1] == '.' {
			text = strings.TrimSpace(text[i+2:])
			break
		}
	}
	return text
}

// internalReasoningLine identifies high-confidence chain-of-thought preambles
// that weak local models occasionally emit as ordinary answer content. This is
// intentionally narrow: normal explanations and bullet lists remain visible.
func internalReasoningLine(line string) bool {
	text := checklistMarkerText(line)
	for _, marker := range []string{
		"the user wants me to ",
		"the user wants me to make",
		"the user wants to make",
		"the user asked me to ",
		"but wait - the user",
		"but wait — the user",
		"but wait, the user",
		"let me first ",
		"let me start by ",
		"let me start by reading",
		"let me append ",
		"let me just ",
		"i should just ",
		"i need to read the existing files",
		"i need to start executing",
		"looking at the existing code:",
		"the plan is clear",
		"i've been exploring",
		"i have been exploring",
		"the image is a screenshot of",
		"since i can't see the image",
		"since i cannot see the image",
		"append the rest of the content",
		"thinking process:",
		"analysis:",
		"reasoning:",
		"let's check constraints:",
		"lets check constraints:",
		"final output generation:",
	} {
		if strings.HasPrefix(text, marker) {
			return true
		}
	}
	return false
}

// finalAnswerLine releases the gate after a model has crossed from its leaked
// internal preamble into an actual user-facing response. Greetings and common
// completion openings cover the observed local-model failure without treating
// arbitrary prose as chain-of-thought.
func finalAnswerLine(line string) bool {
	text := strings.ToLower(strings.TrimSpace(line))
	for _, marker := range []string{
		"hello", "hi ", "wa alaykum", "alhamdulillah", "wahdatullah", "mashallah", "salaam", "implemented", "done", "fixed", "completed",
		"here's what", "here is what", "the changes", "i updated", "i fixed",
		"i am ", "i'm ", "i’m ", "the answer is ", "yes, ", "no, ",
	} {
		if strings.HasPrefix(text, marker) {
			return true
		}
	}
	return false
}

func sanitizeVisibleAnswer(text string) string {
	// Drop whole seed echo blocks first.
	text = stripSeedEchoBlocks(text)
	lines := strings.Split(text, "\n")
	kept := lines[:0]
	internal := false
	for _, line := range lines {
		if seedEchoLine(line) || systemPromptEchoLine(line) {
			continue
		}
		if internalReasoningLine(line) {
			internal = true
		}
		if finalAnswerLine(line) {
			internal = false
		}
		if !internalChecklistLine(line) && !internal {
			kept = append(kept, line)
		}
	}
	return strings.TrimSpace(strings.Join(kept, "\n"))
}

// stripSeedEchoBlocks removes <plan>…</plan> and <builder_workflow>…</builder_workflow>
// regions from a complete answer (non-streaming path).
func stripSeedEchoBlocks(text string) string {
	for _, tag := range []string{"plan", "builder_workflow"} {
		for {
			open := "<" + tag
			start := strings.Index(strings.ToLower(text), open)
			if start < 0 {
				break
			}
			// Find end of opening tag.
			gt := strings.Index(text[start:], ">")
			if gt < 0 {
				text = text[:start]
				break
			}
			bodyStart := start + gt + 1
			closeTag := "</" + tag + ">"
			closeIdx := strings.Index(strings.ToLower(text[bodyStart:]), closeTag)
			if closeIdx < 0 {
				text = text[:start]
				break
			}
			end := bodyStart + closeIdx + len(closeTag)
			text = text[:start] + text[end:]
		}
	}
	return text
}
