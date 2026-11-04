package agent

import (
	"regexp"
	"strings"
	"time"

	"github.com/aistatine/sonut/internal/sonutruntime"
)

// TurnShape classifies the shape of a user prompt so the harness can route it
// without a model round-trip. Greeting/acknowledgment turns run at "none"
// regardless of pinned effort; question turns skip file edits; continuation
// turns skip the planner.
type TurnShape string

const (
	TurnShapeNone         TurnShape = "none"         // greeting / chit-chat / acknowledgment
	TurnShapeQuestion     TurnShape = "question"     // interrogative, no task verbs
	TurnShapeContinuation TurnShape = "continuation" // "continue" / "yes" / "go on"
	TurnShapeTask         TurnShape = "task"         // real work request
)

// RouteRunMode classifies a request into the two execution workflows without
// spending a model round trip. Interactive surfaces use this before launching
// the provider so quick answers are tool-less and build tasks get the planner.
func RouteRunMode(prompt string) RunMode {
	// Explicit browser requests are still quick turns, but they must retain the
	// minimal browser tools so "open Airbnb" is executed instead of answered as
	// a URL suggestion.
	if needsQuickBrowser(prompt) {
		return RunModeQuick
	}
	shape := classifyTurnShape(prompt)
	if !isWorkspaceFollowup(prompt) && (isFlashQA(shape, prompt) || shape == TurnShapeNone ||
		(shape == TurnShapeQuestion && !looksTechnicalShape(prompt))) {
		return RunModeQuick
	}
	// Short non-technical statements without an action verb are answer-shaped
	// even when they do not end in a question mark (for example, "track usage"
	// or "retry with more detail"). Do not pay for the planner/tool prefill on
	// these lightweight turns.
	trimmed := strings.TrimSpace(prompt)
	if !taskVerbPattern.MatchString(trimmed) && !looksTechnicalShape(trimmed) && len(strings.Fields(trimmed)) <= 8 && len(trimmed) <= 96 {
		return RunModeQuick
	}
	// A short, single-action request still needs tools, but does not benefit
	// from a second planner request ("write notes", "rename the file"). Auto
	// mode keeps the normal executor/tool path while letting the planner gate
	// make the cheap-turn decision.
	if len(strings.Fields(trimmed)) <= 4 && !strings.ContainsAny(strings.ToLower(trimmed), "&;\n") {
		return RunModeAuto
	}
	return RunModeBuilder
}

// needsQuickBrowser recognizes explicit requests to open or use a browser.
// These are deliberately narrower than general web/current-fact detection:
// ordinary questions keep the cheap no-tool path or web_search path, while a
// direct browser instruction always gets navigate/open + snapshot tools.
func needsQuickBrowser(prompt string) bool {
	lower := strings.ToLower(strings.TrimSpace(prompt))
	// People often dictate or type domains with a space before the TLD
	// ("airbnb .com"). Normalize only that domain-shaped whitespace so an
	// explicit browser request does not fall through to the expensive coding
	// route and prefill the full workspace/tool prompt.
	lower = quickBrowserSpacedHostPattern.ReplaceAllString(lower, "$1.$2")
	if lower == "" || identityQuestionPattern.MatchString(lower) {
		return false
	}
	if strings.Contains(lower, "http://") || strings.Contains(lower, "https://") ||
		strings.Contains(lower, "www.") || quickBrowserHostPattern.MatchString(lower) {
		return true
	}
	if looksTechnicalShape(lower) {
		return false
	}
	if quickBrowserActionPattern.MatchString(lower) {
		return true
	}
	return containsAny(lower, []string{
		"use browser", "in the browser", "with the browser", "browser to",
		"open webpage", "open web page", "open website", "open site",
		"visit webpage", "visit web page", "visit website", "visit site",
		"browse to", "go to", "navigate to", "open url", "open a url",
	})
}

var quickBrowserHostPattern = regexp.MustCompile(`(?i)\b(?:www\.)?[a-z0-9][a-z0-9-]*\.(?:com|org|net|io|co|ai|dev|app|edu|gov)(?:[/\s]|$)`)
var quickBrowserSpacedHostPattern = regexp.MustCompile(`(?i)\b([a-z0-9][a-z0-9-]*)\s+\.\s*([a-z]{2,})(?:[/\s]|$)`)
var quickBrowserActionPattern = regexp.MustCompile(`(?i)\b(?:open|visit|browse|navigate|go\s+to|load|show)\b.{0,80}\b(?:browser|web\s*page|web\s*site|site|url)\b`)

// isFastStartTask identifies short, concrete work requests where a private
// planning monologue is counterproductive. The unified workflow still owns
// planning, verification, permissions, and loop guards; this only lets the
// model reach its first useful tool call immediately.
func isFastStartTask(prompt string) bool {
	trimmed := strings.TrimSpace(prompt)
	if trimmed == "" || classifyTurnShape(trimmed) != TurnShapeTask ||
		isWorkspaceFollowup(trimmed) || strings.ContainsAny(trimmed, "?\n") {
		return false
	}
	words := len(strings.Fields(trimmed))
	if words < 2 || words > 8 || len(trimmed) > 96 {
		return false
	}
	return taskVerbPattern.MatchString(trimmed)
}

// questionStartPattern covers the common English interrogatives. "who" is
// required — without it "who are you" falls through as TurnShapeTask and
// arms tools + full thinking on a local hybrid model (multi-minute hang).
// "do"/"have" are only interrogative with a subject pronoun right after
// ("do you…", "have they…"); bare "do the task" / "have the tests pass" are
// imperatives and must stay task-shaped (observed: "do the multi-step task"
// routed to flash Q&A, which stripped tools and clamped the turn budget).
var questionStartPattern = regexp.MustCompile(`(?i)^\s*(why|what|how|when|where|who|whom|whose|which|should|can|could|would|will|is|are|was|were|does|did|has|had|(?:do|have)\s+(?:you|we|i|they|it)\b)\b`)

// embeddedQuestionPattern catches natural speech that gives context before
// asking the actual question ("I need to wash my car ... should I drive or
// walk"). Users often omit the final question mark, so requiring a leading
// interrogative or trailing punctuation would incorrectly open the builder.
var embeddedQuestionPattern = regexp.MustCompile(`(?i)\b(?:should|could|would|can|will|is|are|was|were|do|does|did)\s+(?:i|we|you|they|it)\b`)

// factualQuestionPattern wins over incidental action verbs. For example,
// "How much did Odysseus 2026 movie make" is a current-fact question, not a
// request to make a movie. Keeping it out of the builder avoids an unnecessary
// planner pass and full workspace prefill.
var factualQuestionPattern = regexp.MustCompile(`(?i)^\s*(how\s+(?:much|many|long|old|far)|what\s+(?:is|are|was|were)\b|when\s+(?:is|are|was|were|did|does|will)\b|where\s+(?:is|are|was|were|did|does)\b|who\s+(?:is|are|was|were|did|does)\b)`)

// currentRoleLookupPattern catches terse current-fact lookups that omit an
// interrogative, such as "ceo of openai". These must still receive fresh
// evidence rather than an answer from stale model memory.
var currentRoleLookupPattern = regexp.MustCompile(`(?i)^\s*(?:ceo|chief\s+executive\s+officer|president|prime\s+minister|founder|owner|director|head|leader)\s+(?:of|at)\s+\S+`)
var continuationPattern = regexp.MustCompile(`(?i)^\s*(continue|go on|go|proceed|yes|yep|yeah|got it|alright|do it|go ahead|sounds good|ok|okay|sure|next|keep going)\W*$`)
var taskVerbPattern = regexp.MustCompile(`(?i)\b(fix|build|implement|create|add|remove|delete|refactor|update|change|make|write|edit|deploy|run|install|configure|rename|move)\b`)

// identityQuestionPattern is pure "who are you / what is this" chit-chat that
// must never arm tools or deep thinking — answers come from the system prompt.
var identityQuestionPattern = regexp.MustCompile(`(?i)^\s*(who\s+are\s+you|what\s+are\s+you|what\s+is\s+(this|atcode|@code|sonut)|who\s+is\s+(this|atcode)|tell\s+me\s+about\s+yourself|are\s+you\s+(a\s+)?(boy|girl|male|female|man|woman|human|ai|an?\s+ai|robot|real)|do\s+you\s+have\s+(a\s+)?gender)\b`)

// workspaceFollowupPattern catches indirect references to workspace items.
// These turns stay conversational in tone, but need read-only tools when the
// target is not explicit or no longer present in the short conversation tail.
var workspaceFollowupPattern = regexp.MustCompile(`(?i)(?:\b(?:this|that|the|same|mentioned|above|below)\s+(?:path|file|folder|directory|repo|repository|workspace|project|module|component|function)\b|\b(?:open|inspect|read|show|check|find|look\s+at|list)\s+(?:it|this|that|the\s+same)\b|\b(?:path|file|folder|directory|repo|repository)\s+(?:above|below|mentioned|again)\b)`)

// promptCarriesAttachments reports whether the agent-facing prompt includes
// staged document bodies or an attachment manifest. Flash mode must keep these
// when stripping session history — shapePrompt is only the raw user ask + chip
// refs and would otherwise drop PDF text (summarize-attached-doc thrash).
func promptCarriesAttachments(prompt string) bool {
	return strings.Contains(prompt, "Attached document:") ||
		strings.Contains(prompt, "[attachment_manifest]")
}

// isFlashQA reports whether this turn is pure chit-chat / identity / short
// non-code Q&A that must NOT pay the hybrid local prefill tax (full system
// prompt + tool schemas + session history ≈ 50–90s of dead air before the
// first token). Measured live: "who are you" thought 14s but "worked for 66s"
// — the missing ~50s was cold re-prefill of a 7k-char system+tools blob.
func isFlashQA(shape TurnShape, prompt string) bool {
	trimmed := normalizeShapePrompt(prompt)
	if trimmed == "" {
		return true
	}
	if isWorkspaceFollowup(trimmed) {
		return false
	}
	if shape == TurnShapeNone {
		return true
	}
	if identityQuestionPattern.MatchString(trimmed) {
		return true
	}
	// A standalone lookup/verification follow-up (for example, "check on
	// internet") is still quick Q&A, even without a question mark. Do not let
	// it fall through to the builder solely because it is imperative-shaped.
	if needsQuickWebSearch(trimmed) && !taskVerbPattern.MatchString(trimmed) && !looksTechnicalShape(trimmed) {
		return true
	}
	// Factual-question phrasing takes priority over the generic task-verb
	// heuristic: "How much did a movie make" contains "make" but is not a
	// request to create anything.
	if factualQuestionPattern.MatchString(trimmed) && !looksTechnicalShape(trimmed) {
		return true
	}
	// Non-technical questions never need workspace context or tools. A task
	// verb in the question ("why does the build fail?") means it is about the
	// project — keep tools. Raised word/char limits so casual Q&A ("explain
	// quantum computing simply") stays flash and never pays high thinking.
	// Multi-sentence prose with periods is still non-technical (see
	// looksTechnicalShape — sentence periods are not path signals).
	if shape == TurnShapeQuestion && !looksTechnicalShape(trimmed) && !taskVerbPattern.MatchString(trimmed) {
		words := len(strings.Fields(trimmed))
		if words <= 40 && len(trimmed) <= 280 {
			return true
		}
	}
	return false
}

// classifyTurnShape returns the structural shape of a prompt for harness
// routing. It runs on every turn regardless of pinned effort so a high pinned
// effort is the ceiling for real tasks, NOT the floor for greetings.
func classifyTurnShape(prompt string) TurnShape {
	trimmed := normalizeShapePrompt(prompt)
	if trimmed == "" || autoEffortGreetingPattern.MatchString(trimmed) {
		return TurnShapeNone
	}
	// Continuation check runs BEFORE the length gate because words like "yes"
	// and "continue" are under the threshold but are continuations, not greetings.
	if continuationPattern.MatchString(trimmed) {
		return TurnShapeContinuation
	}
	// Pure identity questions ("who are you") are Q&A, never tasks — without
	// this they armed the full tool list + thinking on follow-up turns.
	if identityQuestionPattern.MatchString(trimmed) {
		return TurnShapeQuestion
	}
	// Single-word prompts that aren't continuations or task verbs are
	// acknowledgments/praise (e.g. "ALHAMDULILLAH", "Perfect", "Wonderful").
	// Threshold 15 so short multi-char non-ASCII praise words are caught too.
	lower := strings.ToLower(trimmed)
	if len(trimmed) < 15 && !strings.Contains(trimmed, " ") && !taskVerbPattern.MatchString(lower) {
		return TurnShapeNone
	}
	// Strong question signal: starts with interrogative AND ends with "?" → always
	// a question regardless of incidental task nouns like "build" or "fix".
	startsWithQ := questionStartPattern.MatchString(trimmed)
	endsWithQ := strings.HasSuffix(trimmed, "?")
	hasTaskVerb := taskVerbPattern.MatchString(lower)
	if startsWithQ && endsWithQ {
		return TurnShapeQuestion
	}
	// Natural-language factual questions are often entered without a question
	// mark and may contain words such as "make" (box-office revenue). They are
	// quick-answer work unless they visibly target code or a file.
	if factualQuestionPattern.MatchString(trimmed) && !looksTechnicalShape(trimmed) {
		return TurnShapeQuestion
	}
	if (currentRoleLookupPattern.MatchString(trimmed) || looksLikeAttributionLookup(strings.ToLower(trimmed))) &&
		!taskVerbPattern.MatchString(trimmed) && !looksTechnicalShape(trimmed) {
		return TurnShapeQuestion
	}
	if (startsWithQ || endsWithQ) && !hasTaskVerb {
		return TurnShapeQuestion
	}
	if embeddedQuestionPattern.MatchString(trimmed) && !hasTaskVerb && !looksTechnicalShape(trimmed) {
		return TurnShapeQuestion
	}
	// Short free-form reactions with no task/question/code signal
	// (e.g. "ITS TOOOOO BAD", "oh no", "so cool!!") used to fall through as
	// TurnShapeTask, which armed tools + thinking for pure chit-chat and often
	// stalled the local model for minutes with zero tokens. Paths, file-ish
	// tokens, and URLs keep the technical gate so "read foo.go" still routes
	// as a task even without a listed task verb.
	//
	// Multi-word phrases need a chit-chat signal (!? / elongated / mostly caps /
	// known reaction) so terse work phrases like "from stdin" stay tasks.
	words := strings.Fields(trimmed)
	looksTechnical := looksTechnicalShape(trimmed)
	if !hasTaskVerb && !startsWithQ && !endsWithQ && !looksTechnical &&
		len(words) <= 5 && len(trimmed) <= 48 && looksLikeChitChat(trimmed, words) {
		return TurnShapeNone
	}
	// Knowledge / summarization / explanation asks without task verbs or code
	// paths are Q&A, not builder work. Without this, "summarize the attached
	// doc" and "explain that again" forced a high-think planner pass (2×
	// provider requests, multi-minute hybrid prefill) for a one-shot answer.
	if !hasTaskVerb && !looksTechnical && len(words) <= 20 && len(trimmed) <= 200 {
		if summarizeOrExplainPattern.MatchString(trimmed) || generalKnowledgePattern.MatchString(trimmed) || startsWithQ || endsWithQ {
			return TurnShapeQuestion
		}
	}
	return TurnShapeTask
}

// summarizeOrExplainPattern matches pure answer-shaped asks that must not open
// the builder/planner pipeline.
var summarizeOrExplainPattern = regexp.MustCompile(`(?i)^\s*(summarize|summarise|explain|describe|outline|list|compare|translate|define|tell me|remind me|recap)\b`)

// generalKnowledgePattern covers fact-shaped prompts without a conventional
// question mark. The local model's knowledge cutoff is 2024, so these asks
// should be grounded with web_search when network access is available.
var generalKnowledgePattern = regexp.MustCompile(`(?i)^\s*(?:explain|describe|tell\s+me(?:\s+about)?|give\s+me\s+(?:facts?|information)|history\s+of|historical\b|future\s+of|outlook\s+for|meaning\s+of|definition\s+of|information\s+about|facts?\s+about|general\s+knowledge\s+about)\b`)

// looksLikeChitChat reports whether a short free-form prompt is emotional /
// reactive rather than a terse instruction. Single-word non-task prompts are
// already handled above; this gate is for multi-word reactions.
func looksLikeChitChat(trimmed string, words []string) bool {
	if len(words) <= 1 {
		return true
	}
	if strings.ContainsAny(trimmed, "!?") {
		return true
	}
	// Mostly uppercase letters → shouty reaction ("ITS TOOOOO BAD").
	letters, upper := 0, 0
	for _, r := range trimmed {
		if r >= 'A' && r <= 'Z' {
			letters++
			upper++
		} else if r >= 'a' && r <= 'z' {
			letters++
		}
	}
	if letters > 0 && upper*100/letters >= 60 {
		return true
	}
	// Elongated runs ("tooooo", "sooo") are classic reaction orthography.
	run := 1
	prev := rune(0)
	for _, r := range strings.ToLower(trimmed) {
		if r == prev && r >= 'a' && r <= 'z' {
			run++
			if run >= 3 {
				return true
			}
		} else {
			run = 1
			prev = r
		}
	}
	switch strings.ToLower(strings.TrimSpace(trimmed)) {
	case "oh no", "oh well", "so cool", "too bad", "my bad", "all good",
		"sounds good", "no way", "hell yeah", "hell no", "not bad", "so bad",
		"fuck off", "go away", "leave me alone", "shut up":
		return true
	}
	return false
}

// autoEffortHighKeywords are task words that indicate the turn benefits from
// deep thinking (multi-step debugging, design decisions, cross-file work).
var autoEffortHighKeywords = []string{
	"fix", "build", "implement", "plan",
}

// autoEffortDeepKeywords always warrant deep thinking regardless of prompt
// length — they signal analysis/design work, not a mechanical change.
var autoEffortDeepKeywords = []string{
	"refactor", "debug", "architecture", "design", "optimize", "migrate",
	"investigate", "why does", "root cause",
}

// autoEffortWebKeywords indicate the answer depends on facts the model can't
// reliably know from training data alone (current events, versions, prices).
var autoEffortWebKeywords = []string{
	"latest", "current version", "today", "news", "price", "release notes",
	"changelog", "documentation for", "docs for", "what version", "released",
	"up to date", "up-to-date", "budget", "cost", "revenue", "box office",
	"net worth", "salary", "who is", "who was", "who won", "when was", "where is",
}

// needsQuickWebSearch identifies factual asks where a current lookup is more
// reliable than model memory. It deliberately stays narrow: ordinary questions
// keep the zero-tool fast path, while time-sensitive figures (such as a current
// film's box office) get the single lightweight DuckDuckGo tool.
func needsQuickWebSearch(prompt string) bool {
	lower := strings.ToLower(strings.TrimSpace(prompt))
	if identityQuestionPattern.MatchString(lower) {
		return false
	}
	// Preserve conversational follow-ups such as "which position" for the
	// session history instead of searching an underspecified two-word query.
	// Explicit lookup commands remain eligible below.
	if len(strings.Fields(lower)) < 3 && !containsAny(lower, []string{"search", "look up", "verify", "latest", "current"}) {
		return false
	}
	if containsAny(lower, autoEffortWebKeywords) {
		if looksTechnicalShape(lower) || taskVerbPattern.MatchString(lower) {
			return false
		}
		return true
	}
	// Explicit verification requests and time-sensitive media facts should use
	// the keyless DuckDuckGo path before the model answers from memory. These
	// remain quick-answer turns: one search at most, no planner or workspace
	// tools.
	if containsAny(lower, []string{
		"check on internet", "check online", "check the internet", "search online",
		"look it up", "verify online", "fact check", "fact-check", "according to",
		"who directed", "who was the director", "directed by", "release date",
		"when did it release", "how much did it make", "box office", "budget", "cost",
		"revenue", "grossed", "net worth", "salary", "who is", "who was", "who won",
		"what is", "what was", "where is", "where was", "when is", "when was",
		"search", "find", "look up",
	}) {
		if looksTechnicalShape(lower) || taskVerbPattern.MatchString(lower) {
			return false
		}
		return true
	}
	// Search-shaped title/attribution lookups such as "The Odyssey by
	// Christopher Nolan" are often entered without a question mark. They are
	// factual asks, not build instructions, and must use current evidence.
	if currentRoleLookupPattern.MatchString(lower) || looksLikeAttributionLookup(lower) {
		return true
	}
	if generalKnowledgePattern.MatchString(lower) &&
		!looksTechnicalShape(lower) && !taskVerbPattern.MatchString(lower) {
		return true
	}
	if strings.Contains(lower, "box office") || strings.Contains(lower, "movie make") ||
		strings.Contains(lower, "film make") || strings.Contains(lower, "grossed") ||
		strings.Contains(lower, "revenue") || strings.Contains(lower, "budget") ||
		strings.Contains(lower, "worth") || strings.Contains(lower, "cost") {
		return true
	}
	if (factualQuestionPattern.MatchString(lower) || questionStartPattern.MatchString(lower) || embeddedQuestionPattern.MatchString(lower) || strings.HasSuffix(lower, "?")) &&
		!looksTechnicalShape(lower) && !taskVerbPattern.MatchString(lower) {
		return true
	}
	return factualQuestionPattern.MatchString(lower) && regexp.MustCompile(`\b(?:19|20)\d{2}\b`).MatchString(lower)
}

func looksLikeAttributionLookup(prompt string) bool {
	if taskVerbPattern.MatchString(prompt) || looksTechnicalShape(prompt) {
		return false
	}
	if !strings.Contains(prompt, " by ") {
		return false
	}
	words := len(strings.Fields(prompt))
	return words >= 3 && words <= 14
}

var autoEffortGreetingPattern = regexp.MustCompile(`(?i)^\s*(hi|hello|hey|thanks|thank you|ok|okay|cool|nice|great|sounds good)\W*$`)

// classifyAutoEffort is the fast-path heuristic router for ReasoningEffortAuto.
// It resolves a user turn to a concrete effort level ("none"/"low"/"high") and
// flags whether the turn likely needs current/external facts. It runs purely
// on the prompt text (no extra model round trip) so it costs nothing.
func classifyAutoEffort(prompt string) (effort string, needsWeb bool) {
	trimmed := strings.TrimSpace(prompt)
	lower := strings.ToLower(trimmed)

	needsWeb = containsAny(lower, autoEffortWebKeywords)

	// Greetings / empty / ultra-short acks: minimal private thinking. Real Q&A
	// (including flash quick mode) defaults to high thinking for answer quality — the
	// flash path already strips the workspace system blob so high think no
	// longer implies a 55s cold prefill.
	shape := classifyTurnShape(trimmed)
	if trimmed == "" || autoEffortGreetingPattern.MatchString(trimmed) ||
		shape == TurnShapeNone {
		return "none", needsWeb
	}
	// Single-token noise under a dozen chars with no question mark stays none.
	if len(trimmed) < 12 && !strings.Contains(trimmed, "?") && shape != TurnShapeQuestion {
		return "none", needsWeb
	}
	if isFlashQA(shape, trimmed) || (shape == TurnShapeQuestion && !looksTechnicalShape(trimmed)) {
		// Pure knowledge Q&A: high thinking ON by default. Web facts still use
		// the light tool when needed.
		return "high", needsWeb
	}
	if containsAny(lower, autoEffortDeepKeywords) {
		return "high", needsWeb
	}
	words := len(strings.Fields(trimmed))
	// Task verbs like "fix"/"build" only mean deep work when the ask has real
	// substance; a short "fix the typo in readme" should act, not deliberate.
	if containsAny(lower, autoEffortHighKeywords) && words > 20 {
		return "high", needsWeb
	}
	if words > 50 {
		return "high", needsWeb
	}
	// Default real work: low (not high). Planner upgrades to high when needed.
	return "low", needsWeb
}

// reasoningCapsForEffort returns per-turn caps on the thinking phase. These are
// spiral BACKSTOPS only — not targets. Thinking stays ON by default (quality);
// the instruction clamp in provider.go + the server --reasoning-budget keep
// healthy thinks short. Client caps MUST sit ABOVE the server budget so the
// model can finish a think block and emit a tool call / answer.
//
// Measured 2026-07-21 (atcode / Bonsai, thinking ON, effort=none):
//
//	reasoning_chars=605 → "stream stalled: model reasoning exceeded 512 bytes"
//	then 2 retries with the same 512 B cap → ~40 s of visible idle, zero action.
//
// 512–768 BYTE caps were far below the server --reasoning-budget (512 tokens ≈
// 2–2.5 KB of think text). The client killed healthy short thinks mid-sentence,
// the stall-retry re-prefaced the same request, and the user saw multi-minute
// "idle before tools". Never return 0 (unlimited).
func reasoningCapsForEffort(effort string) (maxBytes int, maxDuration time.Duration) {
	switch strings.ToLower(strings.TrimSpace(effort)) {
	case "none", "minimal":
		// Flash / executor: still think (quality), then act. Cap above the
		// server 256-token reasoning budget so the server ends thinking first
		// and the model can emit the tool call instead of a false stall.
		return 4096, 45 * time.Second
	case "low":
		return 6144, 60 * time.Second
	case "medium", "mid":
		return 8192, 75 * time.Second
	default:
		// high/super/auto: real deliberation room for planning/re-deepen.
		return 12288, 120 * time.Second
	}
}

// fileExtShapePattern matches path-like "name.ext" tokens (foo.go, src/app.ts).
// Sentence-ending periods ("car.") must NOT count — that false positive forced
// multi-sentence Q&A off the flash path (live: car-wash question → full 6k
// system prefill → ~55s TTFT idle before any token).
var fileExtShapePattern = regexp.MustCompile(`(?i)\b[\w.-]+\.[a-z][a-z0-9]{0,7}\b`)

// looksTechnicalShape reports whether a prompt references code/files/paths so
// a pure-question turn still gets tools when it is about the workspace.
func looksTechnicalShape(prompt string) bool {
	if isWorkspaceFollowup(prompt) {
		return true
	}
	lower := strings.ToLower(prompt)
	if strings.Contains(lower, "codebase") ||
		strings.Contains(lower, "source code") ||
		strings.Contains(lower, "repository") ||
		strings.Contains(lower, "repo") ||
		strings.Contains(lower, "workspace") {
		return true
	}
	if strings.Contains(prompt, "://") {
		return true
	}
	// Path separators and inline code ticks are strong code signals.
	if strings.ContainsAny(prompt, "/\\`") {
		return true
	}
	// snake_case / identifiers (rare in plain English questions).
	if strings.Contains(prompt, "_") {
		return true
	}
	// File-ish "name.ext" only — not every period in prose.
	return fileExtShapePattern.MatchString(prompt)
}

// isWorkspaceFollowup reports whether a user turn refers to a workspace item
// indirectly. It prevents path references from being misrouted as tool-less
// conversation and gives the model a chance to ground the target first.
func isWorkspaceFollowup(prompt string) bool {
	return workspaceFollowupPattern.MatchString(normalizeShapePrompt(prompt))
}

// normalizeShapePrompt strips trailing quote/punctuation noise so
// `drive?"` still classifies as a question ending in "?".
func normalizeShapePrompt(prompt string) string {
	trimmed := strings.TrimSpace(prompt)
	// Strip wrapping/trailing quotes the UI or paste sometimes leaves on.
	for {
		next := strings.TrimRight(trimmed, "\"'`")
		next = strings.TrimSpace(next)
		if next == trimmed {
			break
		}
		trimmed = next
	}
	return trimmed
}

// planningEffort maps a run's resolved effort to the effort used for the
// PLANNING turn (turn 0, and the re-deepen turn after a failed step). Harness
// rule: the planner runs at MAXIMUM thinking; executors run at zero. The
// upgrade applies only when the effort came from the auto classifier —
// an effort the user pinned explicitly (/effort low) is honored as-is — and
// never to trivial turns (none/minimal: greetings, one-liners).
func planningEffort(base string, fromAuto bool) string {
	if !fromAuto {
		return base
	}
	switch strings.ToLower(strings.TrimSpace(base)) {
	case "none", "minimal", "min", "":
		return base
	}
	return "high"
}

// executionEffort maps a run's resolved effort to the effort used for
// EXECUTION turns. The planner does the deep decomposition; the executor uses
// "none", which is a short private-thinking path for reasoning-capable
// providers so it can sanity-check the next action without reopening a long
// chain of thought.
func executionEffort(base string) string {
	_ = base
	return "none"
}

func containsAny(haystack string, needles []string) bool {
	for _, needle := range needles {
		if strings.Contains(haystack, needle) {
			return true
		}
	}
	return false
}

const webSearchNote = "\n\nFor fresh facts, versions, prices, current events, or API docs you cannot verify from memory, use web_search if useful; if it is unavailable, answer from local knowledge and state uncertainty."

// resolveAutoEffort turns ReasoningEffortAuto into a concrete effort string and,
// when the turn looks fact-dependent, appends a system note steering the model
// toward web tools (or toward stating uncertainty if offline). It mutates
// options.ReasoningEffort in place and returns the possibly-updated messages.
func resolveAutoEffort(options Options, prompt string, messages []sonutruntime.Message) (Options, []sonutruntime.Message) {
	effort, needsWeb := classifyAutoEffort(prompt)
	options.ReasoningEffort = effort
	options.resolvedFromAuto = true
	if !needsWeb || len(messages) == 0 {
		return options, messages
	}
	messages[0].Content += webSearchNote
	return options, messages
}
