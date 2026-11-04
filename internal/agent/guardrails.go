package agent

import (
	"encoding/json"
	"fmt"
	"io"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"unicode"

	"github.com/aistatine/sonut/internal/sonutruntime"
	"github.com/aistatine/sonut/internal/tools"
)

var browserConsoleErrorCountPattern = regexp.MustCompile(`(?:^|[^0-9])[1-9][0-9]*\s+errors?\b`)

// Guardrail thresholds for the agent loop. These keep a runaway model from
// burning turns/tokens and nudge it toward keeping the plan current. They are
// deliberately conservative so trivial single-step tasks never trip them.
const (
	// maxEmptyTurns stops the run after this many consecutive turns that
	// produced no visible text AND no tool calls. A turn that produces either
	// resets the counter. Dropped-tool-call turns are handled by the existing
	// retry path and are not counted here.
	maxEmptyTurns = 3

	// staleToolCallThreshold injects a one-shot reminder once this many tool
	// calls have executed since the last update_plan call.
	staleToolCallThreshold = 10

	// toolOnlyProgressReminderAt injects a one-shot progress nudge after this
	// many consecutive turns contain tool calls but no visible assistant text.
	// It does not stop the run; it tells the model to synthesize what it already
	// knows before spending more tool turns.
	toolOnlyProgressReminderAt = 6

	// planReminderTurn is the turn (1-based) by the end of which a multi-step
	// task should have called update_plan; if it hasn't, a one-time reminder is
	// injected. Set to 3 (not 2) so short, legitimate two-step tasks finish
	// without a spurious planning nag.
	planReminderTurn = 3

	// planToolName is the planning tool the loop watches for by name.
	planToolName = "update_plan"

	// toolFailureHintAt injects a one-shot corrective hint (the tool's schema +
	// the exact error) after a tool fails this many times in a row with the same
	// error, so the model self-corrects instead of repeating the mistake.
	toolFailureHintAt = 2
	// toolFailureStopAt halts the run after a tool fails this many times in a row
	// with the same error, so NO model (weak or strong) burns turns looping on a
	// bad call.
	toolFailureStopAt = 4

	// maxContinueNudges bounds how many times the headless completion gate
	// (Options.RequireCompletionSignal) re-prompts a model that stopped without a
	// tool call while work clearly remained. Once spent, the run finalizes as
	// INCOMPLETE rather than nudging forever (and it is still bounded by maxTurns
	// and the run deadline).
	maxContinueNudges = 3
)

// continueNudgeMarker is a stable substring for tests.
const continueNudgeMarker = "the task is not finished"

// continueNudge tells a model that stopped without a tool call — while work
// clearly remained — to keep going, or to mark the plan complete and summarize if
// it is genuinely done. The second path gives it a clean route to a legitimate
// completion (a finished plan + no continuation cue then exits as success).
func continueNudge(reason string) string {
	return "You stopped without calling a tool, but " + continueNudgeMarker + " (" + reason + "). " +
		"Do not stop here: take the next concrete action with a tool now. " +
		"If you are genuinely finished, first mark the plan complete with update_plan, then give your final summary."
}

// planPendingNudgeMarker is a stable substring for tests.
const planPendingNudgeMarker = "call update_plan to mark it done NOW"

// planPendingNudge is the prescriptive (not merely descriptive) continue-nudge
// used when the ONLY signal that work may be unfinished is stale plan
// bookkeeping (no continuation cue). A nudge that just says "items pending"
// invites the model to re-narrate the plan instead of acting; this one names
// the exact next call.
func planPendingNudge() string {
	return "Plan items are pending. If a pending step is actually already complete, " +
		planPendingNudgeMarker + " before doing anything else. " +
		"Otherwise execute the next pending step with a tool call. " +
		"Do not restate the plan or summarize progress."
}

// endsWithContinuationCue reports whether an assistant message ends mid-thought —
// the model announcing its OWN next action and then stopping on a colon
// ("…Let me check the SSH configuration:") rather than concluding. It requires
// BOTH a trailing colon AND an action lead-in on the last line, so genuine closers
// are NOT flagged: a forward-looking recommendation ("Next, I suggest reviewing
// the changes."), a sign-off ("Let me know if you need anything"), or a summary
// that merely ends in a colon ("Here is the summary:"). A bare trailing colon alone
// is too common in legitimate final answers to use as a signal.
func endsWithContinuationCue(text string) bool {
	// Local models frequently emit typographic apostrophes (I’ll / I’m) even
	// when the prompt uses ASCII. Normalize them before matching action cues so
	// an unfinished turn is never mistaken for a final answer.
	trimmed := strings.TrimSpace(strings.NewReplacer("’", "'", "‘", "'", "＇", "'").Replace(text))
	if trimmed == "" {
		return false
	}
	lines := strings.Split(trimmed, "\n")
	last := ""
	for i := len(lines) - 1; i >= 0; i-- {
		if s := strings.TrimSpace(lines[i]); s != "" {
			last = strings.ToLower(s)
			break
		}
	}
	// Inspect the final clause (after the last sentence break) so a mid-line action
	// announcement is caught ("…configure the server. Let me check the config:")
	// while a plain summary colon ("Here is the summary:") or a recommendation is not.
	clause := last
	if idx := strings.LastIndex(last, ". "); idx >= 0 {
		clause = strings.TrimSpace(last[idx+2:])
	}
	if !strings.HasSuffix(last, ":") {
		// Promise-without-colon: "I will now call edit_file with these parameters.
		// This is the next concrete step." — an announced action with no tool call.
		for _, cue := range []string{
			"i will now ", "i'll now ", "now i'll ", "now i will ",
			"this is the next", "that is the next", "the next step is",
		} {
			if strings.HasPrefix(clause, cue) {
				return true
			}
		}
		return false
	}
	if strings.HasPrefix(clause, "let me know") { // sign-off, not a mid-step action
		return false
	}
	for _, cue := range []string{
		"let me ", "let's ", "now i'll ", "now i will ", "now let me ", "let me now ",
		"i'll now ", "i will now ", "i'll continue ", "i will continue ", "next i'll ", "next, i'll ", "first, i'll ", "first let me ",
	} {
		if strings.HasPrefix(clause, cue) {
			return true
		}
	}
	return false
}

// planStatusRemaining reports whether a raw update_plan status string represents
// unfinished work. Anything not clearly completed/failed (incl. empty/unknown,
// which the update_plan tool coerces to "pending") counts as remaining, matching
// that tool's own status normalization.
func planStatusRemaining(status string) bool {
	switch strings.ToLower(strings.TrimSpace(status)) {
	case "completed", "complete", "done", "finished", "resolved", "✓", "x", "[x]",
		"failed", "fail", "error", "errored", "blocked", "cancelled", "canceled", "abandoned", "skipped":
		return false
	default:
		return true
	}
}

// acceptanceNudgeMarker is a stable substring for tests; it is embedded verbatim
// in acceptanceVerificationNudge.
const acceptanceNudgeMarker = "verify your work against the task's stated success criterion"

// selfReportPhrases are high-signal admissions of guessing/fabrication or stated
// uncertainty about the produced result. They are matched by plain substring with
// NO context guard, so every entry must be FIRST-PERSON or unambiguously about the
// model's own output — NOT a phrase that also describes implemented behavior.
// (Earlier versions included "fall back to" / "placeholder value" / "as a
// fallback" / bare "best guess" / "without proper", which match legitimate final
// answers like "the parser will fall back to UTF-8" or "I replaced the placeholder
// value", wrongly downgrading completed runs — so those are removed. Admissions of
// INABILITY are handled separately by inabilityStems, which carry a guard.)
var selfReportPhrases = []string{
	// first-person admissions of guessing / fabrication / assumption
	"i guessed", "my best guess", "i'm guessing", "this is a guess", "just a guess",
	"i made it up", "i fabricated", "i assumed a", "i had to assume",
	// first-person lack of capability (inability stems also cover cannot/could-not)
	"i do not have the ability", "i don't have the ability", "i lack the ability",
	"no way for me to", "not possible for me to",
	// stated uncertainty about the correctness of the produced result
	"may not be correct", "might not be correct", "may be incorrect", "might be incorrect",
	"this may not work", "this might not work", "not fully functional", "not fully working",
}

// inabilityStems are first-person "I cannot / can't / could not / am unable to /
// do not have" stems. Matching the STEM (not a fixed verb) generalizes over
// whatever action the model claims it could not perform — "analyze", "determine",
// "do", "complete", "verify", "see", etc. — so the detector is not defeated by
// re-phrasing (the chess case slipped a fixed "cannot analyze" list by writing
// "…which I cannot do without proper image analysis capabilities").
// Every stem must be FIRST-PERSON: a bare "unable to " would also match
// third-person descriptions of the bug being fixed ("users were unable to log
// in; I fixed the cookie TTL") and wrongly finalize a completed run as
// INCOMPLETE, so only the "I …" variants are listed.
var inabilityStems = []string{
	"i cannot ", "i can't ", "i can not ", "i could not ", "i couldn't ",
	"i am unable to", "i'm unable to", "i was unable to", "i wasn't able to",
	"i was not able to", "i do not have", "i don't have",
}

// successNegationTails are negated phrasings that indicate SUCCESS, not an
// admission ("I could not find any remaining issues", "I cannot reproduce the
// bug"). When an inability stem is immediately followed by one of these, it is not
// treated as an admission, so a clean result is not misreported as INCOMPLETE.
var successNegationTails = []string{
	"find any", "found any", "find a ", "see any", "detect any", "identify any",
	"reproduce", "spot any", "locate any",
}

// selfReportedIncompletion returns a short reason when the model's final text
// admits it guessed or could not meet the objective, else "". Case-insensitive.
func selfReportedIncompletion(text string) string {
	lower := strings.ToLower(text)
	for _, phrase := range selfReportPhrases {
		if strings.Contains(lower, phrase) {
			return selfReportReason(phrase)
		}
	}
	for _, stem := range inabilityStems {
		// Scan EVERY occurrence of the stem, not just the first: an earlier
		// success-negation use ("I could not find any examples, so I could not
		// implement it") must not mask a later genuine admission with the same stem.
		for start := 0; ; {
			rel := strings.Index(lower[start:], stem)
			if rel < 0 {
				break
			}
			abs := start + rel
			tail := strings.TrimSpace(lower[abs+len(stem):])
			if !hasAnyPrefix(tail, successNegationTails) {
				return selfReportReason(strings.TrimSpace(stem) + " …")
			}
			start = abs + len(stem)
		}
	}
	return ""
}

func selfReportReason(marker string) string {
	return `the final message admits the objective was not met ("` + marker + `")`
}

func hasAnyPrefix(s string, prefixes []string) bool {
	for _, p := range prefixes {
		if strings.HasPrefix(s, p) {
			return true
		}
	}
	return false
}

// acceptanceVerificationNudge forces a TASK-GROUNDED acceptance check before a run
// may finalize as success. It explicitly rejects the three false-success patterns
// the bug-hunt surfaced: well-formed output treated as correct; pre-existing tests
// passing treated as the objective being met; and a result that merely matches a
// baseline the task asked to beat or improve. General — no task-specific content.
func acceptanceVerificationNudge() string {
	return "Before this task can be marked complete, " + acceptanceNudgeMarker + " — " +
		"NOT the shape or format of your output, NOT that pre-existing tests pass, and NOT that your " +
		"result merely matches a baseline you were asked to beat or improve. " +
		"Re-read the original task, then run a concrete check that exercises the actual requirement: " +
		"execute the program or tests that demonstrate the required behavior, or directly probe the specific " +
		"thing the task asked you to produce, recover, fix, or optimize. " +
		"If that check passes, reply PASS and cite the evidence. " +
		"If it does not pass — or you cannot run such a check — say so plainly and keep working; do not claim success."
}

// toolFailureHintMarker is a stable substring for tests.
const toolFailureHintMarker = "kept failing with the same error"

type toolFailureRecord struct {
	count     int
	errSig    string
	hintShown bool
}

type toolFailureOutcome struct {
	InjectHint bool
	Stop       bool
	Count      int
}

// errorSignature normalizes a tool error to a short, comparable signature so
// repeated identical failures are detected while a genuinely different error
// resets the streak.
func errorSignature(output string) string {
	s := strings.ToLower(strings.Join(strings.Fields(output), " "))
	if len(s) > 80 {
		s = s[:80]
	}
	return s
}

// toolFailureHint tells the model exactly how a tool's arguments must look after
// it has repeated the same failing call. Injected at most once per failure streak.
func toolFailureHint(toolName, schemaJSON, errOutput string) string {
	return "Your calls to the `" + toolName + "` tool " + toolFailureHintMarker + ":\n" +
		strings.TrimSpace(errOutput) +
		"\n\nThe `" + toolName + "` tool expects arguments matching this schema — match it exactly:\n" +
		strings.TrimSpace(schemaJSON) +
		"\n\nFix the arguments and try once more, or take a different approach."
}

// toolFailureStopAnswer is the final answer when the repeated-failure guard halts
// a run.
func toolFailureStopAnswer(toolName string, count int) string {
	return "Agent stopped: the `" + toolName + "` tool failed " + strconv.Itoa(count) +
		" times in a row with the same error, so I halted instead of looping further. " +
		"Please check the request or adjust the tool arguments."
}

// The no-output stop answer is assembled from these fixed parts (only the turn
// count varies). IsNoProgressStop matches all three so a legitimate message that
// merely quotes the marker substring is not misclassified as a failed empty run.
const (
	noOutputStopPrefix = "Agent stopped after "
	noOutputStopMarker = "with no output (no visible text and no tool calls)"
	noOutputStopSuffix = "to avoid consuming tokens without making progress."
)

// noOutputStopAnswer is the final answer returned when the no-output guard
// stops the run. The turn count is interpolated at the call site.
func noOutputStopAnswer(turns int) string {
	return noOutputStopPrefix + strconv.Itoa(turns) + " turns " + noOutputStopMarker + " " + noOutputStopSuffix
}

// IsNoProgressStop reports whether content IS the no-output guardrail stop answer
// (a run that produced no visible text and no tool calls). It matches the EXACT
// structure noOutputStopAnswer emits — prefix + "<int> turns " + marker + " " +
// suffix, where only the integer turn count varies — rather than just looking for
// the three parts in order. A loose check (prefix && contains-marker && suffix)
// would misclassify a genuine assistant/tool message that merely quotes the
// marker amid other prose, which would wrongly hide a real session from /resume
// and skip its title generation.
func IsNoProgressStop(content string) bool {
	trimmed := strings.TrimSpace(content)
	if !strings.HasPrefix(trimmed, noOutputStopPrefix) {
		return false
	}
	rest := trimmed[len(noOutputStopPrefix):]
	const turnsSep = " turns "
	sep := strings.Index(rest, turnsSep)
	if sep < 0 {
		return false
	}
	// The text between the prefix and " turns " must be exactly the bare integer
	// count; anything else means this isn't the guard's own answer.
	if _, err := strconv.Atoi(rest[:sep]); err != nil {
		return false
	}
	// The marker must be immediately followed (one space) by the suffix and then
	// end — no arbitrary text wedged in between.
	return rest[sep+len(turnsSep):] == noOutputStopMarker+" "+noOutputStopSuffix
}

// Reminder markers are stable substrings used both to build the reminder text
// and to assert in tests that the right reminder was injected exactly once.
const (
	planNotCalledReminderMarker    = "you have not called update_plan"
	planStaleReminderMarker        = "haven't updated the plan via update_plan"
	toolOnlyProgressReminderMarker = "consecutive tool-only turns"
)

// planNotCalledReminder nudges the model to track a multi-step task with
// update_plan. Injected at most once per run.
func planNotCalledReminder() string {
	return "Reminder: this looks like a multi-step task and " + planNotCalledReminderMarker +
		". Use the update_plan tool to record the steps and keep progress visible. " +
		"Continue with your work after updating the plan."
}

// planStaleReminder nudges the model to refresh the plan after a stretch of
// tool calls without a plan update. Injected at most once per stale interval.
func planStaleReminder(callsSinceUpdate int) string {
	return "Reminder: you've made " + strconv.Itoa(callsSinceUpdate) +
		" tool calls but " + planStaleReminderMarker +
		" in a while. Update the plan to reflect completed and remaining steps, then continue."
}

func toolOnlyProgressReminder(turns int) string {
	return "Reminder: you've made " + strconv.Itoa(turns) + " " + toolOnlyProgressReminderMarker +
		" without visible progress. Before calling more tools, summarize what you already know, state the next concrete step, and finish if you have enough information."
}

// verboseNarrationThreshold is the pre-tool narration length (chars) above
// which the clamp fires. On a local model every generated token costs real
// wall-clock time, so a multi-paragraph preamble before a tool call is the
// single biggest source of slow turns. 280 chars ≈ 2 short sentences.
const verboseNarrationThreshold = 280

// maxVerboseNarrationNudges bounds the clamp per run.
const maxVerboseNarrationNudges = 2

// verboseNarrationMarker is a stable substring for tests.
const verboseNarrationMarker = "at most two short sentences before a tool call"

// checkVerboseNarration fires when a turn pairs tool calls with a long
// narration preamble. Returns the corrective nudge, or "" when within budget
// or already nudged enough this run.
func (state *guardState) checkVerboseNarration(text string, toolCalls int) string {
	if toolCalls == 0 || len(strings.TrimSpace(text)) <= verboseNarrationThreshold {
		return ""
	}
	if state.verboseNarrationNudges >= maxVerboseNarrationNudges {
		return ""
	}
	state.verboseNarrationNudges++
	return "Your last message spent a long paragraph narrating before the tool call. That wastes time and tokens. " +
		"Write " + verboseNarrationMarker + " — one line saying what you're doing, then the call. " +
		"Save analysis for the final answer, and only if the user needs it."
}

// guardState tracks the per-run signals the guardrails need. It is observable
// purely from tool-call names and per-turn output, matching what the loop holds.
type guardState struct {
	// cwd canonicalizes relative and absolute spellings of the same read target.
	// Without it, "index.html" and "/workspace/index.html" bypassed the
	// unchanged-file guard and caused repeated full prefills.
	cwd                      string
	emptyTurns               int
	totalToolCalls           int
	toolCallsSincePlanUpdate int
	planEverCalled           bool
	notCalledReminderSent    bool
	// staleReminderSent records whether the stale reminder has already fired for
	// the current stale interval. It is cleared when a plan update opens a new
	// interval, making the reminder one-shot per interval rather than per turn.
	staleReminderSent    bool
	toolOnlyTurns        int
	toolOnlyReminderSent bool
	// planItemsPending is the number of remaining (pending/in_progress) items in
	// the most recent update_plan call, so the headless completion gate can tell
	// whether work is unfinished when the model stops without a tool call.
	planItemsPending int
	// toolFailures tracks consecutive same-error failures per tool, keyed by tool
	// name, so the loop can hint then halt instead of looping forever.
	toolFailures map[string]*toolFailureRecord
	// successfulToolCallsSincePlanUpdate counts SUCCESSFUL (non-error) non-plan
	// tool calls since the last update_plan call. Used to catch a step marked
	// "completed" with sonut verifying tool work done since the previous plan
	// snapshot — the "said it but didn't do it" case.
	successfulToolCallsSincePlanUpdate int
	// successfulBrowserVerifySincePlanUpdate counts SUCCESSFUL browser/playwright
	// inspection or interaction calls (snapshot, click, type, navigate, read) since
	// the last update_plan. A UI-touching step may only be marked completed after
	// one of these actually ran — a read_file/ls does NOT prove a rendered page
	// works. Reset alongside successfulToolCallsSincePlanUpdate on every plan update.
	successfulBrowserVerifySincePlanUpdate int
	// planItemsOrdered is the most recent update_plan snapshot, in step order,
	// so the loop can inject an authoritative "here is the real plan state"
	// block every turn instead of the model reconstructing status from its own
	// earlier narration.
	planItemsOrdered []planItem
	// writtenFiles records every path a file-mutating tool successfully changed
	// this run (from tool results' ChangedFiles), so an "obvious completion" —
	// a pending plan step naming a file that was actually written — can be
	// detected even when the model never calls update_plan.
	writtenFiles map[string]bool
	// Browser verification state. Every successful frontend write arms this
	// requirement, even when the model has not opened a browser yet. Only a
	// successful browser call after the write clears it.
	browserUsedThisRun             bool // set after a successful browser/playwright call
	frontendEditedPostBrowser      bool // a frontend write still needs browser verification
	browserCalledAfterFrontendEdit bool // set only by a successful post-write browser call
	browserVerifyNudgeFired        bool // one-shot guard, reset by each frontend write
	// recentAssistantTexts holds up to the last 3 trimmed text-only assistant
	// turns, used by checkDuplicateNarration to detect near-duplicate
	// (reworded, not just verbatim) narration.
	recentAssistantTexts []string
	// recentReasoningTexts holds the last private thought blocks so repeated
	// plans can be stopped even when the visible assistant text changes.
	recentReasoningTexts  []string
	reasoningRepeatNudges int
	// duplicateNarrationStreak counts consecutive turns flagged as near-duplicate
	// narration; a second consecutive trigger escalates the nudge.
	duplicateNarrationStreak int
	// prevPlanStatuses is the previous update_plan snapshot's statuses, keyed by
	// item content (a stable-enough key across calls since ids aren't reliably
	// reused by weak models), used to detect a pending/in_progress -> completed
	// transition with no verifying tool call in between.
	prevPlanStatuses map[string]string
	// pendingUnverifiedCompletion holds a one-shot corrective message set by
	// observePlanUpdate when a step was marked completed with zero successful
	// tool calls since the previous plan snapshot. Consumed (and cleared) by the
	// loop right after processing the tool-call batch.
	pendingUnverifiedCompletion string
	// browserActionKey and browserActionRepeat track repeats of the exact same
	// browser action+target (e.g. "browser_click:e69"), so 3 identical failures
	// force a snapshot + replan instead of a 4th blind retry.
	browserActionKey    string
	browserActionRepeat int
	// verboseNarrationNudges counts how many times the pre-tool narration clamp
	// has fired this run, so the reminder is bounded rather than nagging on
	// every long turn.
	verboseNarrationNudges int
	// browserFailureStreak counts CONSECUTIVE browser tool failures regardless
	// of action/target, reset by any successful browser tool call. After
	// browserFailureStopAt of these the run halts instead of burning the
	// remaining turn budget on a blocked browser session.
	browserFailureStreak int
	// argAbortStreak counts CONSECUTIVE tool calls aborted for oversized/slow
	// argument generation (reset by any successfully-dispatched call); from the
	// second abort in a row a strong redirect nudge is appended, bounded by
	// argAbortNudges so it cannot nag forever.
	argAbortStreak int
	argAbortNudges int
	// sandboxBlockStreak counts CONSECUTIVE sandbox/network denials across tool
	// results. The observed failure mode is the model re-running curl variants
	// against the same block for minutes; from the second denial a redirect
	// nudge (web_fetch / ask the user) is appended, bounded by sandboxBlockNudges.
	sandboxBlockStreak int
	sandboxBlockNudges int
	// repeatedCallCounts tracks exact (tool, arguments) repeats of read-only
	// calls; the third identical call sets pendingRepeatNudge — the result is
	// already in context, so re-fetching it only bloats the conversation.
	repeatedCallCounts map[string]int
	repeatedCallNudges int
	pendingRepeatNudge string
	// lastCallKey/lastCallStreak track CONSECUTIVE byte-identical calls of ANY
	// tool (including mutating ones): re-running `mkdir -p ...` thirteen times
	// in a row is a model loop, not legitimate state change. The third
	// consecutive identical call is blocked (fed back instead of dispatched);
	// past consecutiveCallStopAt the run halts.
	lastCallKey    string
	lastCallStreak int
	// callResultCache stores the most recent tool output per (name+args) key so
	// repeated identical calls can return the cached result without re-executing.
	callResultCache map[string]string
	// inspectedPathCounts tracks NEAR-duplicate file inspections: shell stat
	// commands (ls/stat/file/du/wc) keyed by target path rather than by exact
	// arguments. Observed failure mode: a small model re-verifying one file
	// with `ls -lh X`, `ls -la X`, `file X` across consecutive turns — never
	// byte-identical, so the exact-repeat guard stays silent while 20-30s of
	// prefill burns on facts already in context. Second inspection of a path
	// arms a nudge; the third and later are blocked without dispatching.
	inspectedPathCounts map[string]int
	inspectNudges       int
	// shellContentInspectionCounts tracks shell commands that reopen file
	// contents through cat/head/tail/wc. These commands are semantically the
	// same inspection even when their flags and pipelines differ, which is the
	// loop shape that previously evaded the exact-call and metadata guards.
	shellContentInspectionCounts map[string]int
	shellContentNudges           int
	// readFileCounts catches the common cross-turn loop where the model varies
	// read_file line ranges or argument ordering but keeps reopening the same
	// unchanged file. Exact-call guards cannot see that as a duplicate.
	readFileCounts map[string]int
	// batchReadCounts tracks repeated read_many_files batches separately. A
	// targeted read_file after a batch is often legitimate because the batch is
	// line-capped; it must not be mistaken for a duplicate direct read.
	batchReadCounts map[string]int
	// tryBest detects near-identical edit thrash, repeated failing bash, and
	// edit/verify streaks with no progress (MiMo Try-Best). Soft nudge only.
	tryBest      *TryBestMonitor
	tryBestFired int // bound how many try-best nudges fire per run
}

const maxInspectNudges = 2

const (
	maxShellContentInspectionNudges = 2
	shellContentInspectionBlockAt   = 3
	shellContentInspectionStopAt    = 6
	fileInspectionStopAt            = 4
	readFileStopAt                  = 7
)

// fileInspectStatPattern matches a shell segment that only inspects file
// metadata (not content): ls/stat/file/du + flags + target operands.
var fileInspectStatPattern = regexp.MustCompile(`^(?:ls|stat|file|du|wc)((?:\s+-[^\s]+)*)((?:\s+[^-\s][^\s]*)+)\s*$`)

// fileInspectTarget extracts a normalized "path being stat-ed" from a bash or
// exec_command call, or "" when the command is not a pure metadata inspection.
// Only the FINAL segment of a &&/;-chain is considered (a `cd X && ls Y`
// prefix is navigation, not the point of the call), and only when every
// operand parses as a plain path so compound commands are never blocked.
func fileInspectTarget(toolName, arguments string) string {
	switch toolName {
	case "bash", "exec_command":
	default:
		return ""
	}
	var args map[string]any
	if json.Unmarshal([]byte(arguments), &args) != nil {
		return ""
	}
	command := ""
	for _, key := range []string{"command", "cmd", "script", "shell"} {
		if value, ok := args[key].(string); ok && strings.TrimSpace(value) != "" {
			command = value
			break
		}
	}
	if command == "" || strings.ContainsAny(command, "|><`$") {
		return ""
	}
	segments := strings.FieldsFunc(command, func(r rune) bool { return r == ';' || r == '&' })
	if len(segments) == 0 {
		return ""
	}
	last := strings.TrimSpace(segments[len(segments)-1])
	match := fileInspectStatPattern.FindStringSubmatch(last)
	if match == nil {
		return ""
	}
	operands := strings.Fields(match[2])
	if len(operands) == 0 {
		return ""
	}
	// Key on the basename so `ls /abs/dir/x` and `cd dir && ls x` collide.
	target := strings.TrimRight(operands[len(operands)-1], "/")
	if idx := strings.LastIndexByte(target, '/'); idx >= 0 {
		target = target[idx+1:]
	}
	return target
}

// recordFileInspection counts stat-style inspections of the same path and
// returns a block outcome from the third one on. Returns false when the call
// is not a file inspection or is the first look at that path.
func (state *guardState) recordFileInspection(toolName, arguments string) repeatCallOutcome {
	target := fileInspectTarget(toolName, arguments)
	if target == "" {
		return repeatCallOutcome{}
	}
	if state.inspectedPathCounts == nil {
		state.inspectedPathCounts = make(map[string]int)
	}
	state.inspectedPathCounts[target]++
	count := state.inspectedPathCounts[target]
	if count >= fileInspectionStopAt {
		return repeatCallOutcome{
			Stop: true,
			StopNotice: "Stopped: the model repeatedly inspected '" + target + "' without making progress. " +
				"The run was halted instead of spending more turns on the same metadata read.",
		}
	}
	if count >= 3 {
		return repeatCallOutcome{
			Blocked: true,
			BlockNotice: "Error: blocked — you have already inspected '" + target + "' " +
				strconv.Itoa(count-1) + " times this run (ls/stat/file results are above: it exists and its size/type are known). " +
				"Re-checking metadata yields no new information. Proceed with the actual task using the facts already in context.",
		}
	}
	if count == 2 && state.inspectNudges < maxInspectNudges {
		state.inspectNudges++
		state.pendingRepeatNudge = "You just re-inspected '" + target + "' (ls/stat/file) after already checking it this run. " +
			"Its existence, size, and type are already in context above — do not verify it again. Take the next concrete step of the task."
	}
	return repeatCallOutcome{}
}

// shellContentInspectionTargets returns paths whose contents are inspected by
// a shell command. It intentionally recognizes only the small family of
// commands that commonly replace read_file in a model's exploration loop.
// Pipelines and && chains are split so `cat file | head` still keys on file.
func shellContentInspectionTargets(toolName, arguments string) []string {
	switch toolName {
	case "bash", "exec_command":
	default:
		return nil
	}
	var args map[string]any
	if json.Unmarshal([]byte(arguments), &args) != nil {
		return nil
	}
	command := ""
	for _, key := range []string{"command", "cmd", "script", "shell"} {
		if value, ok := args[key].(string); ok && strings.TrimSpace(value) != "" {
			command = value
			break
		}
	}
	if command == "" || strings.ContainsAny(command, "<`$") {
		return nil
	}

	knownCommands := map[string]bool{
		"cat": true, "head": true, "tail": true, "nl": true, "sed": true, "wc": true,
	}
	seen := make(map[string]bool)
	var targets []string
	segments := strings.FieldsFunc(command, func(r rune) bool {
		return r == ';' || r == '&' || r == '|'
	})
	for _, segment := range segments {
		fields := strings.Fields(strings.TrimSpace(segment))
		if len(fields) < 2 {
			continue
		}
		name := strings.Trim(strings.ToLower(fields[0]), "'\"")
		if !knownCommands[name] {
			continue
		}
		target := ""
		for i := len(fields) - 1; i > 0; i-- {
			candidate := strings.Trim(fields[i], "'\"")
			candidate = strings.TrimRight(candidate, ",)")
			if candidate == "" || strings.HasPrefix(candidate, "-") || candidate == "--" {
				continue
			}
			if _, err := strconv.Atoi(candidate); err == nil {
				continue
			}
			target = strings.TrimRight(candidate, "/")
			break
		}
		if target == "" {
			continue
		}
		if idx := strings.LastIndexByte(target, '/'); idx >= 0 {
			target = target[idx+1:]
		}
		if target != "" && !seen[target] {
			seen[target] = true
			targets = append(targets, target)
		}
	}
	return targets
}

// recordShellContentInspection counts semantic content reads of the same path
// and returns a bounded recovery path. The first two reads remain available for
// legitimate orientation; the third and later variants are fed back without
// executing, and a model that ignores several such blocks ends the run.
func (state *guardState) recordShellContentInspection(toolName, arguments string) repeatCallOutcome {
	targets := shellContentInspectionTargets(toolName, arguments)
	if len(targets) == 0 {
		return repeatCallOutcome{}
	}
	if state.shellContentInspectionCounts == nil {
		state.shellContentInspectionCounts = make(map[string]int)
	}
	maxCount := 0
	target := targets[0]
	for _, path := range targets {
		state.shellContentInspectionCounts[path]++
		if state.shellContentInspectionCounts[path] > maxCount {
			maxCount = state.shellContentInspectionCounts[path]
			target = path
		}
	}
	if maxCount >= shellContentInspectionStopAt {
		return repeatCallOutcome{
			Stop: true,
			StopNotice: "Stopped: the model repeatedly reopened '" + target +
				"' with shell content-inspection commands without making progress. " +
				"The run was halted instead of spending more time repeating the same read.",
		}
	}
	if maxCount >= shellContentInspectionBlockAt {
		return repeatCallOutcome{
			Blocked: true,
			BlockNotice: "Error: blocked — '" + target + "' has already been inspected " +
				strconv.Itoa(maxCount-1) + " times with shell content commands. " +
				"Do not run cat/head/tail/wc again. Use the existing output, use one targeted read_file call only if a specific missing range is required, then implement the task with edit_file/write_file/append_file.",
		}
	}
	if maxCount == 2 && state.shellContentNudges < maxShellContentInspectionNudges {
		state.shellContentNudges++
		state.pendingRepeatNudge = "You just inspected '" + target +
			"' again through a shell content command. The file contents are already in context. " +
			"Do not use cat/head/tail/wc to reread it; take the next implementation step now."
	}
	return repeatCallOutcome{}
}

// readFileTargets extracts paths from read_file and read_many_files calls.
// Policy: different paths may be read freely; reopening the SAME unchanged
// path is suppressed until a successful write clears the count.
func readFileTargets(cwd, toolName, arguments string) []string {
	if toolName != "read_file" && toolName != "read_many_files" {
		return nil
	}
	var args map[string]any
	if json.Unmarshal([]byte(arguments), &args) != nil {
		return nil
	}
	var paths []string
	if toolName == "read_file" {
		if path, ok := args["path"].(string); ok {
			paths = append(paths, path)
		}
	} else if raw, ok := args["paths"].([]any); ok {
		for _, value := range raw {
			if path, ok := value.(string); ok {
				paths = append(paths, path)
			}
		}
	}
	targets := make([]string, 0, len(paths))
	seen := make(map[string]bool, len(paths))
	for _, path := range paths {
		path = strings.TrimSpace(path)
		if path == "" {
			continue
		}
		if !filepath.IsAbs(path) && strings.TrimSpace(cwd) != "" {
			path = filepath.Join(cwd, path)
		}
		target := strings.ToLower(filepath.ToSlash(filepath.Clean(path)))
		if !seen[target] {
			seen[target] = true
			targets = append(targets, target)
		}
	}
	return targets
}

func readFileTarget(cwd, toolName, arguments string) string {
	targets := readFileTargets(cwd, toolName, arguments)
	if len(targets) == 0 {
		return ""
	}
	return targets[0]
}

func (state *guardState) recordReadFile(toolName, arguments string) repeatCallOutcome {
	targets := readFileTargets(state.cwd, toolName, arguments)
	if len(targets) == 0 {
		return repeatCallOutcome{}
	}
	counts := state.readFileCounts
	if toolName == "read_many_files" {
		if state.batchReadCounts == nil {
			state.batchReadCounts = make(map[string]int)
		}
		counts = state.batchReadCounts
	} else if counts == nil {
		counts = make(map[string]int)
		state.readFileCounts = counts
	}
	stale := 0
	maxCount := 0
	target := targets[0]
	for _, candidate := range targets {
		counts[candidate]++
		count := counts[candidate]
		if count > maxCount {
			maxCount = count
			target = candidate
		}
		if count >= 2 {
			stale++
		}
	}
	// A mixed batch still contains a new path worth reading; let the tool run
	// rather than dropping the useful half. Fully stale batches are suppressed.
	if stale < len(targets) {
		return repeatCallOutcome{}
	}
	if maxCount >= readFileStopAt {
		return repeatCallOutcome{
			Stop: true,
			StopNotice: "Stopped: the model reopened `" + filepath.Base(target) + "` " + strconv.Itoa(maxCount) +
				" times without changing it. The earlier read result is already available; halting instead of repeating the same inspection.",
		}
	}
	return repeatCallOutcome{
		Blocked: true,
		BlockNotice: "Internal duplicate suppressed: `" + filepath.Base(target) + "` was already read earlier this run and has not changed. " +
			"DO NOT call read_file again. Proceed IMMEDIATELY to write_file, edit_file, or append_file to implement the next plan step with real source code.",
	}
}

// normalizeCallArguments makes guard/cache keys semantic for ordinary JSON
// tool arguments. Models frequently emit the same object with a different key
// order or harmless whitespace; treating those encodings as different calls
// lets a read/inspect loop evade the exact-repeat guard. Invalid or concatenated
// provider output intentionally falls back to its trimmed bytes rather than
// guessing at a lossy repair.
func normalizeCallArguments(arguments string) string {
	trimmed := strings.TrimSpace(arguments)
	if trimmed == "" {
		return ""
	}
	decoder := json.NewDecoder(strings.NewReader(trimmed))
	decoder.UseNumber()
	var value any
	if err := decoder.Decode(&value); err != nil {
		return trimmed
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		return trimmed
	}
	normalized, err := json.Marshal(value)
	if err != nil {
		return trimmed
	}
	return string(normalized)
}

func toolCallKey(name, arguments string) string {
	return name + "\x00" + normalizeCallArguments(arguments)
}

const maxRepeatedCallNudges = 2

// consecutiveCallStopAt is the consecutive-identical-call streak at which the
// run hard-stops (was 4; tightened after live mkdir/bash thrash).
const consecutiveCallStopAt = 3

// repeatCallOutcome is recordCallSignature's verdict on a dispatched call.
type repeatCallOutcome struct {
	// Blocked: do not dispatch; return BlockNotice as an error tool result.
	Blocked     bool
	BlockNotice string
	// Stop: the model is hard-looping on one call — halt the run.
	Stop       bool
	StopNotice string
}

// recordCallSignature notes a dispatched call. It arms a nudge when the exact
// same read-only call (same tool, byte-identical arguments) is made a third
// time (the result is already in context), and — for EVERY tool — blocks the
// third consecutive byte-identical call and halts the run when the streak
// reaches consecutiveCallStopAt (observed failure mode: a small model re-issuing
// the same successful mkdir/bash call dozens of times).
func (state *guardState) recordCallSignature(name, arguments string) repeatCallOutcome {
	key := toolCallKey(name, arguments)
	if key == state.lastCallKey {
		state.lastCallStreak++
	} else {
		state.lastCallKey = key
		state.lastCallStreak = 1
	}

	switch name {
	case "read_file", "read_minified_file", "grep", "glob", "list_directory", "web_fetch", "web_search":
		if state.repeatedCallCounts == nil {
			state.repeatedCallCounts = make(map[string]int)
		}
		state.repeatedCallCounts[key]++
		if state.repeatedCallCounts[key] >= 3 && state.repeatedCallNudges < maxRepeatedCallNudges {
			state.repeatedCallNudges++
			state.pendingRepeatNudge = "You just repeated the exact same " + name + " call for the third time. " +
				"Its result is ALREADY in this conversation — do not fetch it again. Reuse the earlier output and take the next concrete step."
		}
	}

	if state.lastCallStreak >= consecutiveCallStopAt {
		return repeatCallOutcome{Stop: true}
	}
	// Block from the SECOND consecutive identical call (first repeat). Return
	// the cached output so the model can see its prior result without needing to
	// scroll back, then move on. At the third repeat arm a stronger system nudge.
	if state.lastCallStreak >= 2 {
		notice := state.repeatedCallBlockNotice(name, arguments, state.lastCallStreak)
		// Arm a stronger nudge on the second repeat, but only when the
		// read-only path hasn't already set one this batch.
		if state.lastCallStreak >= 3 && state.pendingRepeatNudge == "" && state.repeatedCallNudges < maxRepeatedCallNudges {
			state.repeatedCallNudges++
			state.pendingRepeatNudge = "STOP LOOPING: you have now issued the exact same " + name + " call " +
				strconv.Itoa(state.lastCallStreak) + " times in a row. " +
				"The result is ALREADY IN CONTEXT. Do NOT call it again. Proceed to the next step immediately."
			if name == "edit_file" {
				state.pendingRepeatNudge += " For a truncated file use append_file; for a full rewrite use write_file overwrite:true."
			}
		}
		return repeatCallOutcome{Blocked: true, BlockNotice: notice}
	}
	// Near-duplicate content inspections (cat/head/tail/wc variants) and
	// metadata inspections evade the byte-identical guards above; catch them by
	// semantic target path.
	if outcome := state.recordShellContentInspection(name, arguments); outcome.Stop || outcome.Blocked {
		return outcome
	}
	return state.recordFileInspection(name, arguments)
}

// recordCallSignatureInBatch applies the repeat guard while accounting for
// how the call arrived. A weak model can emit several identical calls inside a
// single assistant response. In that case a hard-stop on the third call is
// premature: the model has not received the first block notice yet. Suppress
// each duplicate, keep its tool_result paired with the advertised tool call,
// and let the next model turn see the corrective feedback. A repeat that starts
// in a later turn still uses the normal hard-stop policy.
func (state *guardState) recordCallSignatureInBatch(name, arguments string, seen map[string]int) repeatCallOutcome {
	if seen == nil {
		return state.recordCallSignature(name, arguments)
	}
	key := toolCallKey(name, arguments)
	duplicateInBatch := seen[key] > 0
	seen[key]++
	outcome := state.recordCallSignature(name, arguments)
	if outcome.Stop && duplicateInBatch {
		return repeatCallOutcome{
			Blocked:     true,
			BlockNotice: state.repeatedCallBlockNotice(name, arguments, state.lastCallStreak),
		}
	}
	return outcome
}

func (state *guardState) repeatedCallBlockNotice(name, arguments string, streak int) string {
	key := toolCallKey(name, arguments)
	var notice string
	var cached string
	if state.callResultCache != nil {
		cached = state.callResultCache[key]
	}
	if cached != "" {
		// No-op edit thrash (live supermario): model re-issues edit_file with
		// identical old/new until hard-stop. Redirect to append/write instead
		// of replaying the no-op text alone.
		if name == "edit_file" && (strings.Contains(cached, "new_string is identical") ||
			strings.Contains(cached, "already contains new_string") ||
			strings.Contains(cached, "edit does nothing")) {
			notice = "BLOCKED: same no-op edit_file already ran. " + cached +
				"\nNEXT ACTION REQUIRED: append_file the NEXT code chunk, or write_file overwrite:true with a complete body. " +
				"Do NOT call edit_file with the same old_string/new_string again."
		} else {
			notice = "Identical command already run — previous result:\n" + cached +
				"\nDo not repeat; use this result or run a different command."
		}
	}
	if notice == "" {
		if streak < 2 {
			streak = 2
		}
		notice = "Error: blocked — this is the exact same " + name + " call as the previous " +
			strconv.Itoa(streak-1) + " call(s), and it already ran (its result is above). " +
			"Do NOT issue it again. Move on to the NEXT step of the task."
	}
	return notice
}

// repeatCallStopAnswer is the final answer when the consecutive-identical-call
// guard halts the run.
func repeatCallStopAnswer(name string, streak int) string {
	return fmt.Sprintf("Stopped: the model issued the exact same %s call %d times in a row and ignored the block notices. Halting instead of looping.", name, streak)
}

// takeRepeatNudge returns and clears any armed repeated-call nudge, so it is
// appended once after the batch's tool results.
func (state *guardState) takeRepeatNudge() string {
	nudge := state.pendingRepeatNudge
	state.pendingRepeatNudge = ""
	return nudge
}

// cacheToolCallResult stores the output of a dispatched call so that repeated
// identical calls can receive the cached result instead of re-executing.
func (state *guardState) cacheToolCallResult(name, arguments, output string) {
	key := toolCallKey(name, arguments)
	if state.callResultCache == nil {
		state.callResultCache = make(map[string]string)
	}
	state.callResultCache[key] = output
}

const maxSandboxBlockNudges = 2

// recordSandboxBlock tracks whether a tool result was a sandbox/permission
// network denial; any successful exec_command or web_fetch resets the streak.
func (state *guardState) recordSandboxBlock(toolName, output string, isError bool) {
	if isError && (strings.Contains(output, "Sandbox block") ||
		strings.Contains(output, "network access requires approval") ||
		strings.Contains(output, "Sandbox approval required for ")) {
		state.sandboxBlockStreak++
		return
	}
	if !isError && (toolName == "exec_command" || toolName == "web_fetch") {
		state.sandboxBlockStreak = 0
	}
}

// checkSandboxBlockStreak fires once two or more sandbox denials pile up
// without a successful command in between: retrying a policy denial can never
// succeed, so force a route change instead of another blocked curl variant.
func (state *guardState) checkSandboxBlockStreak() string {
	if state.sandboxBlockStreak < 2 || state.sandboxBlockNudges >= maxSandboxBlockNudges {
		return ""
	}
	state.sandboxBlockNudges++
	return "STOP retrying network commands: the sandbox has blocked them repeatedly and will block the next one too — no curl/pip variant will get through. " +
		"Either use web_fetch for HTTP(S) URLs (it handles network permission itself), or ask the user to approve network access, " +
		"or complete the task with the data you already have."
}

const maxArgAbortNudges = 2

// recordArgAbort tracks a runaway-arguments abort; recordArgDispatch resets
// the streak when a call actually reaches its tool.
func (state *guardState) recordArgAbort()    { state.argAbortStreak++ }
func (state *guardState) recordArgDispatch() { state.argAbortStreak = 0 }

// checkArgAbortStreak fires after any batch containing an aborted call:
// retrying the same giant call is the one failure mode that costs a minute or
// more each time, so force a strategy change after the FIRST abort rather than
// letting the model burn a second one proving the point.
func (state *guardState) checkArgAbortStreak() string {
	if state.argAbortStreak < 1 || state.argAbortNudges >= maxArgAbortNudges {
		return ""
	}
	state.argAbortNudges++
	return "STOP: your last tool call was aborted for oversized or too-slow arguments. Do NOT regenerate what is already on disk. " +
		"If a partial write was saved, call append_file with ONLY the remaining unwritten content (≤100 lines per call, non-empty). " +
		"Never restart write_file on that path — the harness will convert rewrites to append of the delta, but empty/duplicate content wastes turns. " +
		"For surgical edits use the smallest unique old_string (1-3 lines). " +
		"For commands, run one short command at a time. " +
		"Finish the incomplete file before any other plan step."
}

// planItem is one step of the most recent update_plan snapshot.
type planItem struct {
	Content string
	Status  string
}

var numberedPlanStepPattern = regexp.MustCompile(`^\s*\d+[.)]\s*(?:\[([^]]+)\]\s*)?(.+?)\s*$`)

func newGuardState(cwd ...string) *guardState {
	state := &guardState{
		toolFailures: map[string]*toolFailureRecord{},
		tryBest:      NewTryBestMonitor(),
	}
	if len(cwd) > 0 {
		state.cwd = strings.TrimSpace(cwd[0])
	}
	return state
}

const maxTryBestNudges = 3

// observeTryBest feeds a completed tool result into the Try-Best monitor and
// arms a strategy-change nudge on incident. Soft only — never hard-stops.
func (state *guardState) observeTryBest(tool string, args map[string]any, result ToolResult) {
	if state == nil || state.tryBest == nil || state.tryBestFired >= maxTryBestNudges {
		return
	}
	inc := state.tryBest.Observe(tool, args, result)
	if inc == nil {
		return
	}
	state.tryBestFired++
	// Prefer try-best over a weaker pending repeat nudge when both arm.
	state.pendingRepeatNudge = inc.Nudge()
	// Reset monitor so the same incident does not re-fire immediately.
	state.tryBest.Reset()
}

// seedPlannerPlan gives the executor guard the roadmap produced by the
// separate planner pass. The visible plan previously existed only in the TUI
// until the model happened to call update_plan, so concrete writes could not
// advance it. This seed is deliberately not counted as a model update_plan
// call: the executor must still keep the tool-backed record current.
func (state *guardState) seedPlannerPlan(plan string) {
	if len(state.planItemsOrdered) > 0 || strings.TrimSpace(plan) == "" {
		return
	}
	for _, line := range strings.Split(plan, "\n") {
		match := numberedPlanStepPattern.FindStringSubmatch(line)
		if match == nil || strings.TrimSpace(match[2]) == "" {
			continue
		}
		status := "pending"
		explicitStatus := strings.ToLower(strings.TrimSpace(match[1]))
		switch explicitStatus {
		case "pending", "in_progress", "completed", "failed":
			status = explicitStatus
		case "in progress":
			status = "in_progress"
		case "done", "complete":
			status = "completed"
		case "":
			// An initial planner emits no statuses, so start its first item.
			if len(state.planItemsOrdered) == 0 {
				status = "in_progress"
			}
		default:
			// Unknown annotations must not silently become completed.
			status = "pending"
		}
		if explicitStatus == "" && len(state.planItemsOrdered) == 0 {
			status = "in_progress"
		}
		state.planItemsOrdered = append(state.planItemsOrdered, planItem{Content: strings.TrimSpace(match[2]), Status: status})
	}
	state.planItemsPending = 0
	state.prevPlanStatuses = make(map[string]string, len(state.planItemsOrdered))
	for _, item := range state.planItemsOrdered {
		state.prevPlanStatuses[item.Content] = item.Status
		if planStatusRemaining(item.Status) {
			state.planItemsPending++
		}
	}
}

// replacePlannerPlan installs the validator's authoritative roadmap.  Unlike
// seedPlannerPlan it intentionally replaces the initial seed: validation owns
// task status and may add, remove, or reword steps after inspecting evidence.
func (state *guardState) replacePlannerPlan(plan string) {
	state.planItemsOrdered = nil
	state.planItemsPending = 0
	state.prevPlanStatuses = nil
	state.seedPlannerPlan(plan)
}

func (state *guardState) plannerPlanItems() []tools.PlanItem {
	items := make([]tools.PlanItem, len(state.planItemsOrdered))
	for i, item := range state.planItemsOrdered {
		items[i] = tools.PlanItem{ID: strconv.Itoa(i + 1), Content: item.Content, Status: item.Status}
	}
	return items
}

// firstPendingPlanStep returns the content of the first incomplete plan item,
// or "" when nothing is pending. Used by empty-append / act-now nudges so the
// model is told exactly which step to implement with real source code.
func (state *guardState) firstPendingPlanStep() string {
	if state == nil {
		return ""
	}
	for _, item := range state.planItemsOrdered {
		if planStatusRemaining(item.Status) {
			return item.Content
		}
	}
	return ""
}

// firstPendingPlanFile returns the first concrete path named by an incomplete
// plan step that has not been written this run, or "". Long multi-file tasks
// die when post-write nudges ban write_file entirely — the next step is often
// a brand-new path (game.js after index.html) that must use write_file.
func (state *guardState) firstPendingPlanFile() string {
	if state == nil {
		return ""
	}
	for _, item := range state.planItemsOrdered {
		if !planStatusRemaining(item.Status) {
			continue
		}
		file := planStepFile(item.Content)
		if file == "" || state.fileWasWritten(file) {
			continue
		}
		return file
	}
	return ""
}

// pendingPlanSummary lists remaining plan steps (capped) for stop/resume text.
func (state *guardState) pendingPlanSummary(max int) string {
	if state == nil || max <= 0 {
		return ""
	}
	var parts []string
	for _, item := range state.planItemsOrdered {
		if !planStatusRemaining(item.Status) {
			continue
		}
		parts = append(parts, item.Content)
		if len(parts) >= max {
			break
		}
	}
	return strings.Join(parts, "; ")
}

// nextFileProgressNudge tells the executor exactly which NEW path to write next.
// Unlike thrash messages that said "edit_file/append_file only" (which blocked
// creating the next deliverable), this allows write_file for unfinished paths
// while still banning rewrites of finished ones.
func nextFileProgressNudge(pendingStep, pendingFile string, finished bool) string {
	if finished && pendingStep == "" && pendingFile == "" {
		return "All plan steps that name files are done. Do NOT rewrite finished files. " +
			"Give a short final answer, or make a tiny edit_file only if something is still broken."
	}
	msg := "Do NOT rewrite finished files or create basename twins. "
	if pendingFile != "" {
		msg += "NEXT FILE (new path — use write_file for the first ≤100 lines, then append_file ≤100 lines until Done when is met): " +
			pendingFile + ". "
	} else if pendingStep != "" {
		msg += "NEXT PLAN STEP (use write_file for any NEW path, edit_file/append_file only for small deltas on an existing unfinished file): " +
			pendingStep + ". "
	} else {
		msg += "Implement the next unfinished deliverable with write_file (new path, first ≤100 lines) or edit_file/append_file (small delta). "
	}
	msg += "Empty bodies and full regenerations of files already on disk are forbidden."
	return msg
}

// observeToolResult tracks repeated identical failures of a tool. A successful
// result clears that tool's failure streak. Returns whether to inject a one-shot
// corrective hint and/or stop the run.
func (state *guardState) observeToolResult(name string, failed bool, output string) toolFailureOutcome {
	if state.toolFailures == nil {
		state.toolFailures = map[string]*toolFailureRecord{}
	}
	// Browser adapters can complete the transport call successfully while
	// reporting JavaScript failures in the page (for example, "Console: 2
	// errors"). That is not usable UI evidence, so keep the browser gate armed
	// and feed it through the same bounded retry circuit as other retriable
	// tool failures.
	if !failed && isBrowserVerifyToolName(name) && browserOutputHasConsoleErrors(output) {
		failed = true
	}
	if !failed {
		delete(state.toolFailures, name) // success resets the streak
		if name != planToolName {
			state.successfulToolCallsSincePlanUpdate++
			if isBrowserVerifyToolName(name) {
				state.successfulBrowserVerifySincePlanUpdate++
				state.browserUsedThisRun = true
				if state.frontendEditedPostBrowser {
					state.browserCalledAfterFrontendEdit = true
					state.frontendEditedPostBrowser = false
				}
			}
		}
		return toolFailureOutcome{}
	}
	sig := errorSignature(output)
	record := state.toolFailures[name]
	if record == nil || record.errSig != sig {
		record = &toolFailureRecord{count: 1, errSig: sig}
		state.toolFailures[name] = record
	} else {
		record.count++
	}
	outcome := toolFailureOutcome{Count: record.count}
	if record.count >= toolFailureStopAt {
		outcome.Stop = true
		return outcome
	}
	if record.count >= toolFailureHintAt && !record.hintShown {
		record.hintShown = true
		outcome.InjectHint = true
	}
	return outcome
}

const (
	// browserActionForceSnapshotAt is how many identical (action+target) browser
	// failures in a row force a snapshot + replan rather than a blind retry.
	browserActionForceSnapshotAt = 3
	// browserFailureStopAt is how many CONSECUTIVE browser tool failures (any
	// action/target) halt the run rather than continuing to burn turns.
	browserFailureStopAt = 6
)

type browserFailureOutcome struct {
	ForceSnapshot bool
	HardStop      bool
	StreakCount   int
}

// observeBrowserToolResult tracks browser-tool-specific loop budgets on top of
// the generic observeToolResult: repeating the exact same action+target (e.g.
// "browser_click:e69") forces a snapshot + replan after
// browserActionForceSnapshotAt tries, and any run of
// browserFailureStopAt consecutive browser failures (mixed actions/targets)
// halts the run outright, so a stuck browser session can't burn the whole
// turn budget on the same blocked path.
func (state *guardState) observeBrowserToolResult(actionKey string, failed bool) browserFailureOutcome {
	if !failed {
		state.browserActionKey = ""
		state.browserActionRepeat = 0
		state.browserFailureStreak = 0
		return browserFailureOutcome{}
	}
	state.browserFailureStreak++
	if actionKey == state.browserActionKey {
		state.browserActionRepeat++
	} else {
		state.browserActionKey = actionKey
		state.browserActionRepeat = 1
	}
	outcome := browserFailureOutcome{StreakCount: state.browserFailureStreak}
	if state.browserFailureStreak >= browserFailureStopAt {
		outcome.HardStop = true
		return outcome
	}
	if state.browserActionRepeat >= browserActionForceSnapshotAt {
		outcome.ForceSnapshot = true
		// Reset the repeat count so the forced-snapshot nudge is one-shot per
		// streak rather than firing on every subsequent retry of the same action.
		state.browserActionRepeat = 0
	}
	return outcome
}

// browserActionStopAnswer is the final answer when the browser consecutive-
// failure guard halts a run.
func browserActionStopAnswer(streakCount int) string {
	return fmt.Sprintf("Agent stopped: %d consecutive browser actions failed in a row, so I halted "+
		"instead of continuing to burn turns on a blocked browser session. Check the page state "+
		"(overlay, navigation, or selector) before retrying.", streakCount)
}

// observeTurn updates counters from a turn's collected stream. It returns
// whether the no-output guard should stop the run.
//
// Callers must NOT invoke this for turns handled by the dropped-tool-call retry
// path; those are not "empty" in the runaway sense and are handled separately.
func (state *guardState) observeTurn(collected sonutruntime.CollectedStream) (stop bool) {
	hasToolCalls := len(collected.ToolCalls) > 0
	hasVisibleText := strings.TrimSpace(collected.Text) != ""

	if hasToolCalls || hasVisibleText {
		state.emptyTurns = 0
	} else {
		state.emptyTurns++
	}
	if hasToolCalls && !hasVisibleText {
		state.toolOnlyTurns++
	} else {
		state.toolOnlyTurns = 0
		state.toolOnlyReminderSent = false
	}

	unverifiedCompletion := ""
	for _, call := range collected.ToolCalls {
		state.totalToolCalls++
		if call.Name == planToolName {
			state.planEverCalled = true
			state.toolCallsSincePlanUpdate = 0
			// A fresh plan update opens a new stale interval.
			state.staleReminderSent = false
			// Record how many items remain so the completion gate knows whether
			// work is unfinished if the model later stops without a tool call.
			if reason := state.observePlanUpdate(call.Arguments); reason != "" && unverifiedCompletion == "" {
				unverifiedCompletion = reason
			}
			state.successfulToolCallsSincePlanUpdate = 0
			state.successfulBrowserVerifySincePlanUpdate = 0
		} else {
			state.toolCallsSincePlanUpdate++
		}
	}
	state.pendingUnverifiedCompletion = unverifiedCompletion

	return state.emptyTurns >= maxEmptyTurns
}

// pendingPlanItems reports whether the most recent update_plan call still has
// unfinished (pending/in_progress) items. False when no plan was ever recorded.
func (state *guardState) pendingPlanItems() bool {
	return state.planItemsPending > 0
}

// planStateBlockHeader is a stable substring for tests.
const planStateBlockHeader = "Plan (authoritative — trust this over your memory):"

// planStateBlock renders the most recent update_plan snapshot as a compact,
// unambiguous status block to be injected fresh before every model call, so
// the model never has to reconstruct plan status from its own earlier
// narration. Returns "" when no plan has been recorded yet (nothing to show).
func (state *guardState) planStateBlock() string {
	if len(state.planItemsOrdered) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString(planStateBlockHeader)
	for i, item := range state.planItemsOrdered {
		label := "pending"
		switch {
		case !planStatusRemaining(item.Status):
			label = "done"
		case strings.EqualFold(strings.TrimSpace(item.Status), "in_progress") ||
			strings.EqualFold(strings.TrimSpace(item.Status), "in progress"):
			label = "in progress"
		}
		b.WriteString("\n")
		b.WriteString(strconv.Itoa(i + 1))
		b.WriteString(". [")
		b.WriteString(label)
		b.WriteString("] ")
		b.WriteString(item.Content)
	}
	return b.String()
}

// recordWrittenFile records a path a file-mutating tool successfully changed
// this run, so obviousStepCompletionReminder can match it against plan steps
// that name a file.
var frontendExtensions = map[string]bool{
	".html": true, ".htm": true, ".css": true,
	".js": true, ".jsx": true, ".ts": true, ".tsx": true,
	".vue": true, ".svelte": true,
}

func (state *guardState) recordWrittenFile(path string) {
	path = strings.TrimSpace(path)
	if path == "" {
		return
	}
	if state.writtenFiles == nil {
		state.writtenFiles = map[string]bool{}
	}
	path = normalizePlanPath(path)
	state.writtenFiles[path] = true
	// A write creates a new version, so one subsequent read is valid again.
	delete(state.readFileCounts, path)
	delete(state.batchReadCounts, path)
	delete(state.readFileCounts, readFileTarget(state.cwd, "read_file", `{"path":`+strconv.Quote(path)+`}`))
	// A write also makes prior shell inspections stale. Allow a fresh metadata
	// or content inspection of the new version instead of carrying old counts
	// into the next implementation step.
	inspectionTarget := strings.TrimRight(filepath.ToSlash(path), "/")
	if idx := strings.LastIndexByte(inspectionTarget, '/'); idx >= 0 {
		inspectionTarget = inspectionTarget[idx+1:]
	}
	delete(state.inspectedPathCounts, inspectionTarget)
	delete(state.shellContentInspectionCounts, inspectionTarget)
	// Every frontend write needs a fresh browser pass. Previously this was gated
	// on browserUsedThisRun, so an agent that never opened the browser was never
	// reminded to do so.
	ext := strings.ToLower(filepath.Ext(path))
	if frontendExtensions[ext] {
		state.frontendEditedPostBrowser = true
		state.browserCalledAfterFrontendEdit = false
		state.browserVerifyNudgeFired = false
	}
}

// needsBrowserVerification reports whether a frontend write has not yet been
// followed by a successful browser inspection or interaction.
func (state *guardState) needsBrowserVerification() bool {
	return state.frontendEditedPostBrowser && !state.browserCalledAfterFrontendEdit
}

// checkBrowserVerifyNudge fires once after each frontend write that has not
// subsequently been verified by a successful browser call.
func (state *guardState) checkBrowserVerifyNudge() string {
	if state.browserVerifyNudgeFired {
		return ""
	}
	if !state.needsBrowserVerification() {
		return ""
	}
	state.browserVerifyNudgeFired = true
	return "You changed frontend code but have not verified the rendered result. Use the loaded headful Playwright tools now: " +
		"mcp_playwright_browser_navigate (open/reload the local URL) → mcp_playwright_browser_console_messages " +
		"(fix errors) → mcp_playwright_browser_snapshot → interact with click/type/press_key → snapshot or evaluate again."
}

func normalizePlanPath(path string) string {
	path = strings.TrimSpace(strings.ReplaceAll(path, "\\", "/"))
	path = strings.Trim(path, "`'\".,;:)]}")
	path = strings.TrimPrefix(path, "./")
	if path == "" {
		return ""
	}
	return strings.ToLower(filepath.ToSlash(filepath.Clean(path)))
}

// fileWasWritten matches a named plan path to concrete changed-file evidence.
// Bare filenames intentionally match by basename; paths containing directories
// require an exact normalized path, avoiding unrelated suffix collisions.
func (state *guardState) fileWasWritten(name string) bool {
	name = normalizePlanPath(name)
	if name == "" {
		return false
	}
	for f := range state.writtenFiles {
		if f == name || (!strings.Contains(name, "/") && filepath.Base(f) == name) {
			return true
		}
	}
	return false
}

// planFileRefPattern extracts a concrete file path from plan-step text so an
// obviously-completed step can be reconciled from changed-file evidence.
var planFileRefPattern = regexp.MustCompile(`(?i)(?:^|[\s(\["'` + "`" + `])((?:\.{0,2}/)?[A-Za-z0-9_-]+(?:/[A-Za-z0-9_.-]+)*\.[A-Za-z0-9]{1,8})(?:$|[\s),\]"'` + "`" + `])`)

// planStepFile extracts the first concrete filename a plan step names, or "".
func planStepFile(content string) string {
	m := planFileRefPattern.FindStringSubmatch(content)
	if m == nil {
		return ""
	}
	return normalizePlanPath(m[1])
}

// obviousStepCompletionReminderMarker is a stable substring for tests.
const obviousStepCompletionReminderMarker = "mark step"

// obviousStepCompletionReminder checks pending plan steps whose text names a
// file; if that file was actually written this run AND the model's own text
// signals the step is finished (mentions the file, or uses a completion word),
// it returns an unmissable, step-specific reminder to call update_plan instead
// of the generic pending-plan nudge. Auto-reconciliation is deliberately NOT
// silent — the model still must make the update_plan call itself. Returns ""
// when no pending step qualifies.
func (state *guardState) obviousStepCompletionReminder(text string) string {
	lower := strings.ToLower(text)
	for i, item := range state.planItemsOrdered {
		if !planStatusRemaining(item.Status) {
			continue
		}
		file := planStepFile(item.Content)
		if file == "" || !state.fileWasWritten(file) {
			continue
		}
		base := file
		if idx := strings.LastIndexAny(file, "/\\"); idx >= 0 {
			base = file[idx+1:]
		}
		mentioned := strings.Contains(lower, strings.ToLower(base))
		doneWord := false
		for _, w := range []string{"done", "complete", "finished", "created", "wrote", "written", "added"} {
			if strings.Contains(lower, w) {
				doneWord = true
				break
			}
		}
		if mentioned || doneWord {
			return base + " was written — " + obviousStepCompletionReminderMarker + " " +
				strconv.Itoa(i+1) + " done via update_plan"
		}
	}
	return ""
}

// completeWrittenFileSteps reconciles the plan with concrete successful file
// writes and activates the next remaining item for the UI. It returns a full
// snapshot so planner-owned progress remains visible during execution.
func (state *guardState) completeWrittenFileSteps() ([]tools.PlanItem, bool) {
	changed := false
	for i := range state.planItemsOrdered {
		item := &state.planItemsOrdered[i]
		if !planStatusRemaining(item.Status) {
			continue
		}
		file := planStepFile(item.Content)
		if file == "" || !state.fileWasWritten(file) {
			continue
		}
		item.Status = "completed"
		changed = true
	}
	if changed {
		for i := range state.planItemsOrdered {
			if planStatusRemaining(state.planItemsOrdered[i].Status) {
				if state.planItemsOrdered[i].Status != "in_progress" {
					state.planItemsOrdered[i].Status = "in_progress"
				}
				break
			}
		}
	}
	if !changed {
		return nil, false
	}
	items := make([]tools.PlanItem, len(state.planItemsOrdered))
	for i, item := range state.planItemsOrdered {
		items[i] = tools.PlanItem{ID: strconv.Itoa(i + 1), Content: item.Content, Status: item.Status}
	}
	return items, true
}

// duplicateNarrationThreshold is the token-set Jaccard similarity above which
// two assistant messages are treated as near-duplicate narration.
const duplicateNarrationThreshold = 0.8

// duplicateNarrationMarker is a stable substring for tests.
const duplicateNarrationMarker = "you already said this"

// duplicateNarrationNudge tells the model it is repeating itself. escalate is
// true on the second consecutive trigger, demanding a tool-call-only reply
// instead of the softer "take the next action" phrasing.
func duplicateNarrationNudge(escalate bool) string {
	if escalate {
		return "You already said this again. Respond with a tool call only, no prose."
	}
	return "You already said this. Do not repeat prior analysis — take the next concrete action with a tool call."
}

// normalizeForSimilarity tokenizes text into a lowercase word set for Jaccard
// comparison: runs of letters/digits are tokens, standalone numeric tokens
// (e.g. step numbers "1", "2") are dropped so a recap that only changed which
// step number it cites still counts as a duplicate.
func normalizeForSimilarity(text string) map[string]bool {
	tokens := map[string]bool{}
	var b strings.Builder
	flush := func() {
		if b.Len() == 0 {
			return
		}
		word := b.String()
		b.Reset()
		isNumeric := true
		for _, r := range word {
			if r < '0' || r > '9' {
				isNumeric = false
				break
			}
		}
		if !isNumeric {
			tokens[word] = true
		}
	}
	for _, r := range strings.ToLower(text) {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			b.WriteRune(r)
		} else {
			flush()
		}
	}
	flush()
	return tokens
}

// jaccardSimilarity returns the token-set Jaccard similarity of a and b.
func jaccardSimilarity(a, b map[string]bool) float64 {
	if len(a) == 0 && len(b) == 0 {
		return 1
	}
	if len(a) == 0 || len(b) == 0 {
		return 0
	}
	intersection := 0
	for t := range a {
		if b[t] {
			intersection++
		}
	}
	union := len(a) + len(b) - intersection
	if union == 0 {
		return 0
	}
	return float64(intersection) / float64(union)
}

// checkDuplicateNarration compares a text-only turn against the last (up to)
// 3 assistant text-only turns using token-set Jaccard similarity, so a
// reworded recap (not just a verbatim repeat) is caught. Returns "" when text
// is not a near-duplicate of recent output; otherwise returns a nudge, and the
// nudge escalates to a tool-call-only demand on a second consecutive trigger.
// Runs regardless of Options.RequireCompletionSignal — it is the direct fix
// for a model that re-narrates the same plan recap turn after turn.
func (state *guardState) checkDuplicateNarration(text string) string {
	trimmed := strings.TrimSpace(text)
	if len(trimmed) < 30 {
		return ""
	}
	tokens := normalizeForSimilarity(trimmed)
	duplicate := false
	for _, prev := range state.recentAssistantTexts {
		if jaccardSimilarity(tokens, normalizeForSimilarity(prev)) >= duplicateNarrationThreshold {
			duplicate = true
			break
		}
	}
	state.recentAssistantTexts = append(state.recentAssistantTexts, trimmed)
	if len(state.recentAssistantTexts) > 3 {
		state.recentAssistantTexts = state.recentAssistantTexts[len(state.recentAssistantTexts)-3:]
	}
	if !duplicate {
		state.duplicateNarrationStreak = 0
		return ""
	}
	state.duplicateNarrationStreak++
	// Cap consecutive interceptions at 2 (soft nudge, then escalated demand):
	// if the model repeats a THIRD time despite the escalation, stop
	// intercepting and let the existing completion-gate / continuation-cue
	// handling (with its own bounded budget) take over, so a model that never
	// breaks out of the loop still finalizes as Incomplete rather than
	// spinning here forever.
	if state.duplicateNarrationStreak > 2 {
		state.duplicateNarrationStreak = 0
		return ""
	}
	return duplicateNarrationNudge(state.duplicateNarrationStreak >= 2)
}

// explicitlyRequiresToolUse recognizes direct execution requirements without
// treating educational questions such as "how do I use this tool?" as commands.
// It is intentionally narrow and only powers a bounded no-tool correction.
func explicitlyRequiresToolUse(prompt string) bool {
	normalized := strings.ToLower(strings.Join(strings.Fields(prompt), " "))
	for _, phrase := range []string{
		"you must use the ",
		"must use the available ",
		"use the available mcp ",
		"you need to use the ",
		"must call the ",
	} {
		if strings.Contains(normalized, phrase) {
			return true
		}
	}
	return false
}

// checkRepeatedReasoning catches a model that emits the same private plan
// again after a tool result. Visible answer guards cannot see this because the
// repeated content lives in reasoning_content, yet it is still expensive: the
// local reasoning model spends another full think budget before acting.
func (state *guardState) checkRepeatedReasoning(text string) string {
	trimmed := strings.TrimSpace(text)
	if len(trimmed) < 120 {
		return ""
	}
	tokens := normalizeForSimilarity(trimmed)
	duplicate := false
	for _, previous := range state.recentReasoningTexts {
		if jaccardSimilarity(tokens, normalizeForSimilarity(previous)) >= 0.72 {
			duplicate = true
			break
		}
	}
	state.recentReasoningTexts = append(state.recentReasoningTexts, trimmed)
	if len(state.recentReasoningTexts) > 2 {
		state.recentReasoningTexts = state.recentReasoningTexts[len(state.recentReasoningTexts)-2:]
	}
	if !duplicate || state.reasoningRepeatNudges >= 2 {
		return ""
	}
	state.reasoningRepeatNudges++
	return "Your private reasoning repeated the previous plan. Do not think through that plan again. " +
		"Take the single next concrete action with a tool call immediately; no recap or extra analysis."
}

// observePlanUpdate parses an update_plan call's raw arguments, records how
// many items are still remaining, and detects a step being marked "completed"
// with zero successful tool calls since the previous plan snapshot — the
// "said it but didn't do it" case. Returns a non-empty corrective reason when
// that happens. Malformed arguments leave the prior state unchanged
// (best-effort — the plan panel itself tolerates the same).
func (state *guardState) observePlanUpdate(arguments string) string {
	var parsed struct {
		Plan []struct {
			Content string `json:"content"`
			Status  string `json:"status"`
		} `json:"plan"`
	}
	if json.Unmarshal([]byte(arguments), &parsed) != nil {
		return ""
	}
	pending := 0
	newlyCompletedWithNoVerification := ""
	newlyCompletedNeedsBrowser := false
	verified := state.successfulToolCallsSincePlanUpdate > 0
	browserVerified := state.successfulBrowserVerifySincePlanUpdate > 0
	for _, item := range parsed.Plan {
		if planStatusRemaining(item.Status) {
			pending++
			continue
		}
		// item is completed/failed in this snapshot
		wasRemaining := true
		if prev, ok := state.prevPlanStatuses[item.Content]; ok {
			wasRemaining = planStatusRemaining(prev)
		}
		if !(wasRemaining && newlyCompletedWithNoVerification == "" &&
			strings.EqualFold(strings.TrimSpace(item.Status), "completed")) {
			continue
		}
		// A UI-touching step needs proof the rendered page was actually exercised
		// (a browser snapshot/click/etc.), not merely that SOME tool ran — a
		// read_file/ls cannot verify that a page renders, interacts, or debugs.
		if looksLikeUIStep(item.Content) {
			if !browserVerified {
				newlyCompletedWithNoVerification = item.Content
				newlyCompletedNeedsBrowser = true
			}
			continue
		}
		if !verified {
			newlyCompletedWithNoVerification = item.Content
		}
	}
	state.planItemsPending = pending
	nextPrev := make(map[string]string, len(parsed.Plan))
	ordered := make([]planItem, 0, len(parsed.Plan))
	for _, item := range parsed.Plan {
		nextPrev[item.Content] = item.Status
		ordered = append(ordered, planItem{Content: item.Content, Status: item.Status})
	}
	state.prevPlanStatuses = nextPrev
	state.planItemsOrdered = ordered
	if newlyCompletedWithNoVerification != "" {
		if newlyCompletedNeedsBrowser {
			return unverifiedUIStepCompletionNudge(newlyCompletedWithNoVerification)
		}
		return unverifiedStepCompletionNudge(newlyCompletedWithNoVerification)
	}
	return ""
}

// unverifiedStepCompletionMarker is a stable substring for tests.
const unverifiedStepCompletionMarker = "marked complete with no successful tool call since it started"

// unverifiedStepCompletionNudge tells the model it marked a plan step complete
// without any successful tool call verifying it since the step was last seen
// pending/in_progress.
func unverifiedStepCompletionNudge(stepContent string) string {
	return "REJECTED plan update: the step \"" + stepContent + "\" was " + unverifiedStepCompletionMarker +
		". A step can only be marked completed after a tool call has actually run and succeeded to verify its criteria. " +
		"Call the necessary tool now to verify this step, then update the plan again."
}

// unverifiedUIStepCompletionMarker is a stable substring for tests.
const unverifiedUIStepCompletionMarker = "marked complete without a browser verification"

// unverifiedUIStepCompletionNudge is the completion-gate rejection for a UI step
// that was marked done without ever exercising the rendered page. It forces the
// model to actually open the browser, interact, and confirm — not just claim it.
func unverifiedUIStepCompletionNudge(stepContent string) string {
	return "REJECTED plan update: the UI step \"" + stepContent + "\" was " + unverifiedUIStepCompletionMarker +
		". A step that touches the UI can only be marked completed after you OPEN the page in the browser, " +
		"interact with it (click/type/navigate), and confirm it renders and behaves correctly via a browser " +
		"snapshot. Do that now (use the browser/playwright tools — headful), then update the plan again."
}

// uiStepKeywords mark a plan step as UI-touching, so completing it requires a
// browser verification rather than any successful tool call.
var uiStepKeywords = []string{
	".html", ".css", ".tsx", ".jsx", ".vue", ".svelte",
	"browser", "playwright", "headful", "render", "screenshot", "frontend",
	"ui ", " ui", "button", "click", "layout", "page", "canvas", "modal", "form",
}

// looksLikeUIStep reports whether a plan step's text describes work whose
// success can only be proven in a browser (a rendered page, an interaction).
func looksLikeUIStep(content string) bool {
	lower := strings.ToLower(content)
	return containsAny(lower, uiStepKeywords)
}

// isBrowserVerifyToolName reports whether a tool call is a browser/playwright
// inspection or interaction that can serve as proof a UI step works. Broader
// than isBrowserToolName (which only tracks interaction tools for the failure
// budget): here snapshots, navigation, and page reads all count as verification.
func isBrowserVerifyToolName(name string) bool {
	lower := strings.ToLower(name)
	return strings.Contains(lower, "browser") ||
		strings.Contains(lower, "playwright") ||
		strings.Contains(lower, "snapshot") ||
		strings.Contains(lower, "screenshot")
}

// browserOutputHasConsoleErrors detects the compact error summaries emitted by
// browser adapters as well as the common explicit console error forms. A
// StatusOK transport result is still unusable as verification when this is
// true; the page must be repaired and checked again.
func browserOutputHasConsoleErrors(output string) bool {
	normalized := strings.ToLower(strings.TrimSpace(output))
	if normalized == "" || normalized == "[]" ||
		strings.Contains(normalized, "no console messages") ||
		strings.Contains(normalized, "no errors") ||
		strings.Contains(normalized, "0 errors") ||
		strings.Contains(normalized, `"errors":[]`) {
		return false
	}
	if browserConsoleErrorCountPattern.MatchString(normalized) {
		return true
	}
	for _, marker := range []string{"uncaught", "unhandled", "exception", "console.error", "[error]", "error:"} {
		if strings.Contains(normalized, marker) {
			return true
		}
	}
	return false
}

func (state *guardState) progressReminder() string {
	if state.toolOnlyReminderSent || state.toolOnlyTurns < toolOnlyProgressReminderAt {
		return ""
	}
	state.toolOnlyReminderSent = true
	return toolOnlyProgressReminder(state.toolOnlyTurns)
}

// planReminder returns a one-shot reminder message to inject before the next
// turn, or an empty string when no reminder applies. `turn` is 1-based (the
// number of turns completed so far).
func (state *guardState) planReminder(turn int) string {
	// STALE reminder takes priority: a long run without a plan update is the
	// stronger signal. One-shot per stale interval.
	if state.planEverCalled &&
		!state.staleReminderSent &&
		state.toolCallsSincePlanUpdate >= staleToolCallThreshold {
		state.staleReminderSent = true
		return planStaleReminder(state.toolCallsSincePlanUpdate)
	}

	// NOT-CALLED reminder: by the end of planReminderTurn the model should have
	// called update_plan if it's doing a multi-step task (>=1 other tool call).
	// One-shot for the whole run.
	if !state.notCalledReminderSent &&
		!state.planEverCalled &&
		turn >= planReminderTurn &&
		state.totalToolCalls >= 1 {
		state.notCalledReminderSent = true
		return planNotCalledReminder()
	}

	return ""
}
