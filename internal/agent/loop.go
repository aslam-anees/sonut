package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/kaptinlin/jsonrepair"

	"github.com/aistatine/sonut/internal/evolution"
	"github.com/aistatine/sonut/internal/hooks"
	"github.com/aistatine/sonut/internal/observability"
	"github.com/aistatine/sonut/internal/redaction"
	"github.com/aistatine/sonut/internal/sandbox"
	"github.com/aistatine/sonut/internal/sonutruntime"
	"github.com/aistatine/sonut/internal/streamjson"
	"github.com/aistatine/sonut/internal/tools"
)

const maxTurnsAnswer = "Agent reached maximum number of turns without a final answer."
const maxTurnsFinalAnswerPrompt = "You have reached the tool-turn limit. Do not call tools. Give a concise final answer now: summarize what you completed, what you found, and any remaining blockers."

type stagedRewrite struct {
	target string
	stage  string
}

type repeatedFileGeneration struct {
	path    string
	content string
}

// newTurnStreamContext wraps context.WithCancel for the stall-retry path; the
// caller's deferred closure re-reads the returned cancel, guaranteeing release.
func newTurnStreamContext(parent context.Context) (context.Context, context.CancelFunc) {
	return context.WithCancel(parent)
}

// generationDeadline bounds every recovery attempt as well as the first
// generation. Without this, the initial request could time out and the retry
// could stream a tool payload forever.
func generationDeadline(parent context.Context, timeout time.Duration) (context.Context, context.CancelFunc) {
	if timeout <= 0 {
		return parent, func() {}
	}
	return context.WithTimeout(parent, timeout)
}

// generationTimeoutForTool keeps the ordinary generation watchdog strict while
// allowing source-file bodies to match the collector's progress-aware file
// budget. Live local sessions at ~15 tok/s need several minutes for a real
// game/HTML file; base*3 (6m at the default 2m base) still killed healthy
// writes mid-stream (perf.log: decode_ms≈367s → gen cancel → 2m retry fail).
// File tools therefore get the full file-argument ceiling; non-file tools keep
// the ordinary idle budget. The collector still enforces byte + stall + line
// progress caps independently.
func generationTimeoutForTool(base time.Duration, toolName string) time.Duration {
	if base <= 0 {
		return base
	}
	switch strings.ToLower(strings.TrimSpace(toolName)) {
	case "write_file", "append_file", "edit_file", "create_file", "overwrite_file":
		fileBudget := sonutruntime.MaxFileToolArgDuration()
		if fileBudget > base {
			return fileBudget
		}
		// Never shrink below base; if env set a longer base, honor it.
		return base
	default:
		return base
	}
}

// isFileGenerationTool reports tools whose arguments ARE the product being
// generated (source files). These must never be killed solely by wall-clock
// while tokens/lines are still progressing.
func isFileGenerationTool(name string) bool {
	switch strings.ToLower(strings.TrimSpace(name)) {
	case "write_file", "append_file", "edit_file", "create_file", "overwrite_file":
		return true
	default:
		return false
	}
}

// executorInspectionTool is the hard phase boundary for planner-owned runs.
// The planner grounds the repository and owns research; the executor receives
// only mutation/verification tools. Prompt text alone was not sufficient: a
// local model could still call read_file/grep/tool_search after the plan and
// spend the rest of the run re-discovering context.
func executorInspectionTool(name string) bool {
	switch strings.ToLower(strings.TrimSpace(name)) {
	case "read_file", "read_minified_file", "list_directory", "grep", "glob",
		"lsp_navigate", "skill", "ask_user", "request_permissions",
		"tool_search", "web_search", "web_fetch", "browser_install", "browser_launch",
		"browser_connect", "browser_open", "browser_snapshot", "browser_click", "browser_type",
		"browser_press", "browser_action", "browser_eval", "web_browser", "escalate_model":
		return true
	default:
		return false
	}
}

func withoutExecutorInspectionTools(definitions []sonutruntime.ToolDefinition) []sonutruntime.ToolDefinition {
	filtered := make([]sonutruntime.ToolDefinition, 0, len(definitions))
	for _, definition := range definitions {
		if !executorInspectionTool(definition.Name) {
			filtered = append(filtered, definition)
		}
	}
	return filtered
}

// maxStreamStallRetries bounds how many times a turn that timed out (idle/stall)
// WITH NO OUTPUT yet is re-issued on a fresh connection before giving up. Only
// the no-output case is retried (a partial turn would duplicate), so this is a
// safe recovery for a stalled/dead pooled connection.
const maxStreamStallRetries = 2

// maxExecutorVisibleTextBytes prevents a concise-thinking executor from
// spending many minutes streaming a source dump or repeated narration instead
// of making the next tool call. Tool arguments are governed separately by the
// file/tool caps; this limit applies only to ordinary assistant text in a
// seeded plan.
const maxExecutorVisibleTextBytes = 12 * 1024

const (
	toolResultMetaControl       = "control"
	toolResultControlSpecReview = "spec_review_required"
)

var errPermissionApprovalCanceled = errors.New("permission approval cancelled")

// isImageRejectionError reports whether err is a provider error that rejects
// image/multimodal content. This is checked BEFORE the compaction-retry path
// so an image-rejection doesn't loop endlessly (the user hit this with
// minimax-m3: text-only model, image attached, 400 loop until the process
// was killed).
//
// Two shapes are recognized:
//  1. A 400 whose message mentions image/vision/multimodal (cloud providers).
//  2. llama.cpp's unambiguous "image input is not supported" / mmproj 500 —
//     emitted when the server was started without --mmproj. The status code
//     differs (500, not 400), so it is matched by phrase regardless of code.
func isImageRejectionError(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	// llama.cpp's mmproj-missing rejection: match by phrase, any status code.
	if strings.Contains(msg, "image input is not supported") || strings.Contains(msg, "mmproj") {
		return true
	}
	if !strings.Contains(msg, "400") {
		return false
	}
	for _, keyword := range []string{"image", "vision", "multimodal", "unsupported content type", "does not support"} {
		if strings.Contains(msg, keyword) {
			return true
		}
	}
	return false
}

type permissionRunState struct {
	mu       sync.Mutex
	cleanups []func()
}

func (state *permissionRunState) add(cleanup func()) {
	if state == nil || cleanup == nil {
		return
	}
	state.mu.Lock()
	state.cleanups = append(state.cleanups, cleanup)
	state.mu.Unlock()
}

func (state *permissionRunState) cleanup() {
	if state == nil {
		return
	}
	state.mu.Lock()
	cleanups := state.cleanups
	state.cleanups = nil
	state.mu.Unlock()
	for i := len(cleanups) - 1; i >= 0; i-- {
		cleanups[i]()
	}
}

// abortedToolResultNotice is the placeholder tool result recorded for a tool
// call that was advertised by the assistant turn but never executed because the
// repeated-failure guard halted the run first. It keeps every tool_use paired
// with a tool_result so the transcript stays valid for a strict provider replay.
const abortedToolResultNotice = "aborted: run halted by the repeated-failure guard"

// droppedToolCallNotice tells the model a tool call it attempted was malformed
// (missing a tool name) and dropped before execution, so it re-issues a valid
// call instead of assuming the call ran. It is surfaced both when a turn yields
// ONLY a dropped call and when a turn mixes valid calls with a dropped one.
const droppedToolCallNotice = "Your previous tool call was malformed (it was missing a tool name) and was not executed. " +
	"Re-issue the tool call with a valid tool name and JSON arguments, or reply with your final answer."

// escalationFailedNoticePrefix introduces the brief, user-role note recorded
// when a requested mid-run model switch could not be performed (the
// ModelSwitcher returned an error). The run continues on the current model;
// escalation is best-effort, never fatal.
const escalationFailedNoticePrefix = "Note: could not switch to the requested model"

// The system prompt (core coding-craft instructions + workspace context + safety
// confirmation policy) is assembled in system_prompt.go via buildSystemPrompt.

func hasExecutorCapability(registry *tools.Registry, options Options) bool {
	if registry == nil {
		return false
	}
	for _, tool := range registry.All() {
		name := tool.Name()
		if name == "update_plan" || name == "ask_user" ||
			name == "read_file" || name == "read_minified_file" ||
			name == "list_directory" || name == "grep" || name == "glob" {
			continue
		}
		if tool.Safety().Permission == tools.PermissionDeny ||
			!ToolAllowedByFilters(name, options.EnabledTools, options.DisabledTools) {
			continue
		}
		return true
	}
	return false
}

func Run(ctx context.Context, prompt string, provider Provider, options Options) (Result, error) {
	if provider == nil {
		return Result{}, errors.New("agent provider is required")
	}
	if options.Sandbox != nil {
		options.Sandbox.SetNetworkEnabled(!options.NetworkDisabled)
	}

	maxTurns := options.MaxTurns
	if maxTurns <= 0 {
		maxTurns = 50
		// A continuation is one executor slice, not a fresh unbounded session.
		// If it still has work after eight action turns, the surface can launch
		// another explicit continuation; keeping 50 here was the main way a
		// stalled local executor burned an hour without new work.
		if options.ExecutorOnly {
			maxTurns = 8
		} else if options.RunMode == RunModeBuilder {
			maxTurns = 24
		}
	}

	registry := options.Registry
	if registry == nil {
		registry = tools.NewRegistry()
	}

	permissionMode := options.PermissionMode
	if permissionMode == "" {
		permissionMode = PermissionModeAuto
	}
	runPermissions := &permissionRunState{}
	options.runPermissions = runPermissions
	defer runPermissions.cleanup()

	// Shape-based routing runs BEFORE system-prompt assembly so flash Q&A
	// (greetings / identity / short non-code questions) can skip the ~7k-char
	// workspace+tools system blob. Hybrid local models re-prefill every turn
	// with no KV reuse — that blob alone is 50–90s of zero-token dead air.
	//
	// The TUI wraps follow-up prompts in session context; classify the raw
	// user ask (PlannerPrompt) when provided — same rule as the planner gate.
	shapePrompt := prompt
	if strings.TrimSpace(options.PlannerPrompt) != "" {
		shapePrompt = options.PlannerPrompt
	}
	turnShape := classifyTurnShape(shapePrompt)
	workspaceFollowup := isWorkspaceFollowup(shapePrompt)
	flash := isFlashQA(turnShape, shapePrompt) && permissionMode != PermissionModeSpecDraft
	// Explicit quick mode (TUI RouteRunMode) must always take the flash prefill
	// path — even if a false-positive technical signal slipped past isFlashQA.
	// Live: multi-sentence Q&A with periods stayed non-flash → 55s TTFT on a
	// 6k system+workspace blob while the UI sat at "····" with Answer already
	// planned.
	if options.RunMode == RunModeQuick && !workspaceFollowup && !looksTechnicalShape(shapePrompt) && permissionMode != PermissionModeSpecDraft {
		flash = true
	}
	// Load durable project/session context only for real workspace turns. Keeping
	// it out of flash Q&A preserves the fast path, while coding turns get the
	// latest bounded memory and checkpoint without each caller having to wire a
	// separate loader.
	if !flash && strings.TrimSpace(options.PersistentContext) == "" && strings.TrimSpace(options.Cwd) != "" {
		if durable, err := evolution.LoadContext(options.Cwd, options.SessionID, evolution.ContextOptions{IncludeNotes: true}); err == nil {
			options.PersistentContext = durable.Text
		}
	}
	// Web access is an intelligent fact/general-knowledge route, not a tax on
	// every turn. Coding/workspace tasks stay local unless the user explicitly
	// asks for a web lookup; factual and general questions may use web_search.
	quickWebSearch := !options.NetworkDisabled && turnShape == TurnShapeQuestion && needsQuickWebSearch(shapePrompt)
	quickBrowser := !options.NetworkDisabled && needsQuickBrowser(shapePrompt)
	// Flash turns: tiny identity system prompt + raw user text (no resume
	// preamble dump). Factual flash questions expose exactly one real action,
	// web_search; casual Q&A exposes no tools. Coding turns keep the full
	// system prompt and normal workspace tool surface. Do not assemble the full
	// workspace prompt before this branch: it scans project guidance, git state,
	// and the repo map even though flash turns immediately replace it. Keeping
	// that work off the input path is important for the sub-second local router.
	systemPrompt := ""
	userPrompt := prompt
	if flash {
		options.QuickWebSearch = quickWebSearch
		options.QuickBrowser = quickBrowser
		systemPrompt = BuildFlashSystemPrompt(options)
		// Drop the FormatExecPrompt "Continuing session… Previous context…"
		// wrapper for pure chit-chat — thousands of cold-prefill tokens for a
		// one-line answer. NEVER drop attachment preambles: PDF text is
		// injected as "Attached document:" in the full prompt, and shapePrompt
		// is only lastUserPrompt + chip refs (live bug: "summarize the attached
		// doc" answered without the document body).
		if promptCarriesAttachments(prompt) {
			userPrompt = prompt
		} else {
			userPrompt = shapePrompt
		}
		options.NoTools = !(quickWebSearch || quickBrowser)
		// Quick/flash Q&A: tiny system prompt (prefill) + high thinking ON by
		// default for hard answers. Factual turns use low effort below: this
		// still keeps thinking enabled in llama.cpp, but avoids a 40-second
		// private monologue for an obvious lookup. Explicit /effort pins still
		// win; greetings are forced to none later.
		effort := strings.TrimSpace(options.ReasoningEffort)
		if effort == "" || strings.EqualFold(effort, "auto") {
			if quickWebSearch {
				options.ReasoningEffort = "low"
			} else {
				options.ReasoningEffort = "high"
			}
		}
		// Hard wall on runaway Q&A generation (perf.log: 39k text chars / 792s
		// decode on a single hybrid_followup turn with zero tool calls).
		if options.GenerationTimeout <= 0 || options.GenerationTimeout > 90*time.Second {
			options.GenerationTimeout = 90 * time.Second
		}
		if maxTurns > 2 {
			maxTurns = 2
		}
	} else {
		systemPrompt = buildSystemPrompt(options)
		options.QuickBrowser = false
	}
	if !flash {
		options.QuickWebSearch = quickWebSearch
	}
	messages := seedConversationMessages(systemPrompt, userPrompt, options.ConversationHistory, options.Images)
	// Multi-skill composition (MiMo harness): when the user explicitly names
	// ≥2 skills via /skill-name tokens, inject an orchestration reminder after
	// the user message. Single-skill and bare slash commands are no-ops.
	messages = maybeInjectSkillComposition(messages, userPrompt)
	// Vision: stamp an explicit acknowledgement so models stop claiming "no
	// image was shared". Coding runs get the implement contract; flash Q&A
	// only gets a see-and-answer note (do not force write_file for "what color?").
	if len(options.Images) > 0 {
		for i := len(messages) - 1; i >= 0; i-- {
			if messages[i].Role != sonutruntime.MessageRoleUser {
				continue
			}
			if flash {
				messages[i].Content += "\n\n[system] IMAGE ATTACHED — you CAN see the image(s) in this message. Answer the user's question about them. Do NOT claim no image was shared."
			} else {
				messages[i].Content += "\n\n[system] IMAGE ATTACHED — you CAN see the screenshot/reference in this message. Match its layout, colors, and UI. Do NOT claim no image was shared. Read DIFFERENT project files as needed (read_file/list_directory/grep), then implement with write_file/edit_file/append_file. Prefer reading real code over guessing; do not re-read the same path unless you just changed it."
			}
			break
		}
	}

	if strings.EqualFold(strings.TrimSpace(options.ReasoningEffort), "auto") {
		options, messages = resolveAutoEffort(options, userPrompt, messages)
	}

	// Empty mode keeps the lower-level agent API's historical shape routing;
	// interactive surfaces pass RouteRunMode explicitly. This preserves callers
	// that intentionally use technical questions with read-only tools while the
	// TUI still gets the strict two-mode workflow.
	if options.RunMode == RunModeQuick && !workspaceFollowup {
		options.NoTools = !(quickWebSearch || quickBrowser)
		options.Evaluate = false
	}

	// Shape-based trivial override: always run regardless of pinned effort.
	// A high pinned effort is the ceiling for real tasks, not the floor for
	// every turn — a greeting at pinned "super" must not burn 3+ minutes.
	switch turnShape {
	case TurnShapeNone:
		// Greeting / acknowledgment: force "none" effort, skip planner, and
		// drop tools entirely — on hybrid local models every tool schema is a
		// multi-second cold prefill even when the model never calls a tool
		// (observed: first greeting fast, follow-up identity Q slow).
		// Spec-draft mode is planning-only and MUST keep its read/ask tools
		// even for short prompts like "draft".
		options.ReasoningEffort = "none"
		if permissionMode != PermissionModeSpecDraft {
			options.NoTools = true
		}
		for i := len(messages) - 1; i >= 0; i-- {
			if messages[i].Role == sonutruntime.MessageRoleUser {
				messages[i].Content += "\n\n[system] This is a greeting or acknowledgment, not a task. Reply warmly in one short sentence. Do NOT call any tools. Do NOT explore files."
				break
			}
		}
		// Hard backstop: even if the model ignores the note and calls a tool,
		// the run cannot spiral into an exploration loop.
		if maxTurns > 2 {
			maxTurns = 2
		}
	case TurnShapeQuestion:
		// Pure question without task verbs: answer directly, no file edits.
		// Flash Q&A (identity + short non-code) already stripped tools above and
		// defaults to high thinking. Longer/non-flash pure questions keep tools
		// (read-only note) with moderate effort.
		if flash {
			options.NoTools = !(quickWebSearch || quickBrowser)
			effort := strings.TrimSpace(options.ReasoningEffort)
			if effort == "" || strings.EqualFold(effort, "auto") {
				if quickWebSearch {
					options.ReasoningEffort = "low"
				} else {
					options.ReasoningEffort = "high"
				}
			}
		} else if !looksTechnicalShape(shapePrompt) {
			if len(strings.Fields(shapePrompt)) <= 10 {
				if effort := strings.TrimSpace(options.ReasoningEffort); effort == "" || strings.EqualFold(effort, "auto") {
					options.ReasoningEffort = "high"
				}
			} else if !strings.EqualFold(strings.TrimSpace(options.ReasoningEffort), "none") &&
				!strings.EqualFold(strings.TrimSpace(options.ReasoningEffort), "minimal") {
				// Keep high when already set; otherwise low for long non-flash Qs.
				if !strings.EqualFold(strings.TrimSpace(options.ReasoningEffort), "high") {
					options.ReasoningEffort = "low"
				}
			}
		}
		for i := len(messages) - 1; i >= 0; i-- {
			if messages[i].Role == sonutruntime.MessageRoleUser {
				if flash {
					if quickWebSearch {
						messages[i].Content += "\n\n[system] This is a factual or verification question. Call the registered web_search tool once now, then answer from its result. Do not use bash, exec, Python, XML, or a simulated/mock search. Do not narrate a search plan. Answer concisely after the tool result. Do not inspect files."
					} else {
						messages[i].Content += "\n\n[system] Answer directly and naturally. Give enough context to be useful, without padding or private reasoning. Do NOT call tools."
					}
				} else if quickWebSearch {
					messages[i].Content += "\n\n[system] This is a factual or general-knowledge question. The local model's knowledge cutoff is 2024. Use web_search once to verify current, historical, or otherwise factual information before answering when network access is available. Do not edit files or inspect the workspace."
				} else {
					messages[i].Content += "\n\n[system] Answer directly and naturally. If this refers to a workspace file, path, folder, or prior result, resolve it from the conversation or use one read-only inspection tool first (list_directory, glob, grep, or read_file). Never edit files for this question unless explicitly asked, and never edit a guessed target."
				}
				break
			}
		}
	}
	if workspaceFollowup {
		for i := len(messages) - 1; i >= 0; i-- {
			if messages[i].Role == sonutruntime.MessageRoleUser {
				messages[i].Content += "\n\n[system] WORKSPACE FOLLOW-UP: resolve references such as ‘that path’, ‘the file’, or ‘it’ from recent messages and tool results. If the exact target is not established, inspect the workspace with list_directory/glob/grep/read_file before changing anything. Do not invent a path or act on an unverified target."
				break
			}
		}
	}
	// Builder surfaces use one continuous agent loop. The model owns the whole
	// Fable-style sequence—private classification, update_plan, tools, and
	// verification—so there is no planner→executor handoff or second validation
	// identity. Keep the legacy planner harness available to lower-level callers.
	planSeeded := false
	if options.UnifiedWorkflow && options.RunMode != RunModeQuick && !options.ExecutorOnly {
		messages = injectUnifiedWorkflow(messages, len(options.Images) > 0)
		if options.OnPlanStart != nil {
			options.OnPlanStart()
		}
	} else {
		messages, planSeeded = maybeRunPlanner(ctx, provider, prompt, messages, options)
	}
	// Capture the exact point at which planner output is ready for executor
	// preparation. The first executor request logs the remaining handoff time;
	// this separates real harness idle from model prefill/decode in perf.log.
	plannerHandoffReadyAt := time.Time{}
	if planSeeded && !options.ExecutorOnly {
		plannerHandoffReadyAt = time.Now()
	}
	// A continuation launched by an interactive surface already has a
	// planner-owned roadmap in the session prompt. Treat it as seeded even
	// though this run did not perform a new planner pass: this keeps the
	// executor in concise-thinking mode and hides update_plan from its tool list.
	if options.ExecutorOnly {
		planSeeded = true
	}

	guards := newGuardState(options.Cwd)
	seededPlan := ""
	if planSeeded {
		seededPlan = extractSeededPlan(messages)
		guards.seedPlannerPlan(seededPlan)
	}
	preferWriteFile := planSeeded && prefersFullRewrite(prompt, options.PlannerPrompt, seededPlan)
	// Existing-file repairs should not even advertise whole-file writers: a
	// local model can spend minutes generating a replacement before the tool
	// layer gets a chance to reject it. append_file can still create a new path,
	// while edit_file/apply_patch preserve the named file's structure.
	surgicalExistingFile := !preferWriteFile && referencesExistingWorkspaceFile(
		options.Cwd, prompt+"\n"+options.PlannerPrompt+"\n"+seededPlan, options.FileTracker)
	// Hosted runs may use the optional nested-guideline layer. The built-in local
	// Aistatine runtime deliberately has no AGENTS.md integration anywhere in its
	// prompt or tool-result path.
	var intent *intentLayerState
	if !isLocalLlamaProviderName(options.ProviderName) {
		intent = newIntentLayerState(options.Cwd)
	}
	compactor := newCompactionState(options)

	// loaded tracks deferred-eligible tools the model has pulled via tool_search
	// during THIS run. It is consulted by partitionTools each turn to expose a
	// loaded tool's full schema; it lives only for the run (v1 within-run scope).
	loaded := map[string]bool{}
	// OpenCode's reference agent exposes MCP tools directly to its coding agent.
	// Keep @code's general deferred-tool budget, but eagerly expose the small
	// Playwright verification set on UI tasks so the model can use the browser
	// without first discovering a hidden schema.
	preloadBrowserToolsForUITask(loaded, registry, prompt+"\n"+options.PlannerPrompt)

	// continueNudges counts how many times the headless completion gate
	// (Options.RequireCompletionSignal) has re-prompted a no-tool-call turn that
	// stopped with work still unfinished. Bounded by maxContinueNudges.
	continueNudges := 0
	// A seeded builder must perform the roadmap through tools. Local models
	// occasionally answer with a fenced HTML/CSS/JS block instead of calling
	// write_file; give them a bounded correction before accepting that as the
	// final answer.
	executorNoToolNudges := 0
	explicitToolUse := explicitlyRequiresToolUse(prompt)
	// acceptanceRequested records that the one-time task-grounded acceptance check
	// has already been demanded this run, so it fires at most once.
	acceptanceRequested := false
	// totalToolCalls counts tool calls across the whole run; the acceptance
	// check only makes sense when there is completed work to verify.
	totalToolCalls := 0
	// lastAssistantText tracks the previous turn's assistant output so we can
	// detect and break repetition loops (hallucination where the model echoes the
	// same text on consecutive turns without making progress).
	lastAssistantText := ""
	// fileWriteToolCalledThisRun tracks whether a file-mutating tool (write_file,
	// edit_file, apply_patch, etc.) has been called in this run. Used by the
	// file-claim guard: if the model's text claims to have edited/written files
	// but no file tool was actually called, we inject a hard correction.
	fileWriteToolCalledThisRun := false
	// editSinceVerify is true while a file-mutating tool was the most recent tool
	// activity — nothing has run since the last write that could prove the change
	// works. verifyNudgeSent bounds the verify-before-done nudge to once per run.
	editSinceVerify := false
	verifyNudgeSent := false
	browserVerificationFinalNudgeSent := false
	// Plan-deep, execute-fast: the first turn keeps the resolved effort so the
	// model plans thoroughly; later turns drop to fast execution. A turn right
	// after a failed tool call re-deepens once so the model can rethink.
	redeepenNext := false
	// plannerValidationRounds counts the bounded return to the thinking planner
	// after execution. The executor stays fast; only the planner validates and,
	// when necessary, supplies a corrected roadmap.
	plannerValidationRounds := 0
	// validationReworkPending is set only after the planner rejects an execution
	// result.  The executor must perform fresh tool work before it can ask the
	// planner to validate again.  Without this phase latch a narration-only
	// executor answer bounced straight back into validation, producing repeated
	// plans without changing a file.
	validationReworkPending := false
	toolCallsSinceValidation := 0
	// A large edit_file abort leaves its target unchanged. Appending the
	// truncated replacement to that target is never a valid recovery, yet this
	// was a frequent next-turn model action and produced malformed HTML with
	// duplicated/partial CSS. Keep the target blocked from append until a real
	// successful write/edit replaces the state.
	appendBlockedAfterEditAbort := make(map[string]bool)
	// After a partial write_file recovery, force append_file-only for that path
	// so the executor never restarts a multi-minute rewrite of the same file
	// (live: 5-min aborted write → tiny salvage → rewrite loop for ~1 hour).
	appendOnlyAfterPartialWrite := make(map[string]bool)
	// stagedRewritePaths keeps an existing target atomic when a large replacement
	// is interrupted. The incomplete body is written to a hidden sibling and
	// subsequent append chunks are redirected there until the executor promotes it.
	stagedRewritePaths := make(map[string]stagedRewrite)
	stagedRewriteNudges := 0
	// successfulFullWrites tracks paths that landed a complete non-partial
	// write this run. Re-issuing write_file on those paths is almost always
	// thrashing; skip and nudge the model to the next plan step instead.
	successfulFullWrites := make(map[string]bool)
	// emptyAppendStreak counts consecutive empty/near-empty append_file calls.
	// Live mario thrash: model called append_file with no code while "thinking
	// about physics" for minutes. After the first empty call, hard-nudge with
	// the pending plan step; after two, force a non-append recovery path.
	emptyAppendStreak := 0
	// pendingPartialContinuations holds continue-append nudges collected during
	// this tool batch when a partial write was salvaged. Injected after every
	// tool_result so the model finishes the file instead of regenerating it.
	var pendingPartialContinuations []string
	// noProgressStreak counts consecutive turns that spent model time but made
	// zero productive tool progress (failed/empty/aborted mutations with no
	// salvage). Live perf.log burned ~7 minutes on 9 identical prefill→err
	// turns. Redirect once at 2, hard-stop at 4.
	// thrashSkipStreak is SEPARATE: rewrite skips of already-written paths are
	// intentional redirects, not "no progress" — counting them toward hard-stop
	// killed multi-file runs after the first file (live: index.html done → 4×
	// rewrite Generating → stop before game.js ever started).
	noProgressStreak := 0
	thrashSkipStreak := 0
	// Tighter than historical 5: live thrash burned multi-minute identical
	// prefill→err turns. Redirect once at 2, hard-stop at 4.
	const maxNoProgressStreak = 2
	const hardStopNoProgressStreak = 4
	const hardStopThrashSkipStreak = 3

	// Exploration budget: consecutive read-only tool calls with no file edit.
	// After explorationNudgeAt the model is lightly nudged to also edit — never
	// hard-blocked. Executors need many read_file/grep/list calls to understand
	// a real codebase; a tight cap (2–3) made them implement blind and thrash.
	// The nudge is one-shot and only suggests balancing reads with an edit.
	explorationNudgeAt := 12
	if isLocalHybridRun(options) {
		// Still generous: hybrid re-prefills, but understanding the tree is worth
		// a few extra turns vs writing the wrong file.
		explorationNudgeAt = 10
	}
	if len(options.Images) > 0 {
		// Screenshot tasks still need to inspect existing project files before
		// writing; 2 was far too low for multi-file UIs.
		explorationNudgeAt = 8
	}
	if planSeeded {
		explorationNudgeAt = 2
	}
	consecutiveReadOnly := 0
	explorationNudgeSent := false
	// toolsReturnedThisRun flips true after any tool result is appended. On local
	// hybrid models a plan-less first turn still pays thinking+prefill; every
	// follow-up after tools must act with a concise private think or the run
	// spends multi-minute "↑ 0 tok" stretches re-thinking over already-read files.
	toolsReturnedThisRun := false

	result := Result{Messages: copyMessages(messages)}
turnLoop:
	for turn := 0; turn < maxTurns; turn++ {
		result.Turns = turn + 1
		// Live steering: user messages typed while the run was working are
		// injected here, at the turn boundary (the previous turn's tool_results
		// are complete, so a user turn is always valid to append). Re-deepen so
		// the model reacts to the new instruction at full thinking effort.
		if steered := drainSteerMessages(options.Steer); len(steered) > 0 {
			messages = append(messages, steered...)
			redeepenNext = true
		}
		// Thinking policy (harness: planner thinks deeply, executor thinks briefly):
		//
		// - Planner pass: high thinking, streamed live (maybeRunPlanner).
		// - Plan-seeded / post-tool executor: effort=none with a short private
		//   think. One stable template for every executor turn — no mid-run
		//   enable_thinking flip (that was the multi-minute prefill thrash).
		//   Handoff planner→executor already requires a re-prefill (tools appear);
		//   keep the thinking template constant after that.
		// - Redeepen (failed tool / live steer): brief think is allowed only
		//   before a planner has handed work to the executor.
		// - Plan-less first turn: keep thinking for open-ended quality.
		// Once a plan is seeded, the executor contract is absolute: recovery
		// nudges must change the action, never reopen reasoning. The old code let
		// duplicate narration/tool failures set redeepenNext and silently flipped
		// DisableThinking back to false, which produced the multi-minute turn-14
		// failure seen in perf.log.
		redeepenThisTurn := redeepenNext && !planSeeded
		turnEffort := options.ReasoningEffort
		// Short concrete asks ("build temple run game", "fix login test") do
		// not need a private monologue before their first inspection or write.
		// The unified workflow and guards remain active; only this cold
		// first-turn reasoning phase is skipped.
		fastStart := turn == 0 && options.UnifiedWorkflow && !planSeeded &&
			!toolsReturnedThisRun && !redeepenThisTurn && isFastStartTask(shapePrompt)
		actFast := (planSeeded || toolsReturnedThisRun || fastStart) && !redeepenThisTurn
		if actFast {
			turnEffort = executionEffort(options.ReasoningEffort)
		}
		if redeepenThisTurn {
			turnEffort = planningEffort(options.ReasoningEffort, options.resolvedFromAuto)
		}
		redeepenNext = false
		// Thinking policy (product contract):
		// - Flash / RunModeQuick Q&A: thinking ON at high effort by default
		//   (quality answers); tiny system prompt still avoids 55s prefill.
		// - Plan-seeded / post-tool executor (actFast): concise private thinking —
		//   the planner carries the deep decomposition, while the executor still
		//   gets a bounded sanity check before acting.
		// - Redeepen (failed tool / steer): thinking ON briefly for recovery.
		// - Open coding first turn (non-flash): thinking ON for quality.
		// Keep DisableThinking=false for the whole actFast phase so the chat
		// template never flips mid-run after the handoff prefill and the model's
		// short-thinking path remains cache-stable.
		disableThinking := false
		// Plan-seeded executor stays at flash effort for every act-fast turn.
		// "none" is a concise private-thinking effort for reasoning-capable
		// providers; it is not a request to expose a long chain of thought.
		if planSeeded {
			// Plan-seeded and ExecutorOnly runs are always stable act-fast requests,
			// including after a failed tool or a live steer.
			actFast = true
			turnEffort = "none"
		}

		// Build the per-turn tool list first so proactive compaction can include
		// the tool-definition tokens (they ride on every request) in its estimate.
		// partitionTools depends only on registry/permissions/options/loaded, not on
		// the messages, so computing it before compaction is safe.
		exposed, _ := partitionTools(registry, permissionMode, options, loaded)
		if options.UnifiedWorkflow {
			// Ordinary builder work stays in one coherent context. Removing
			// delegation tools also makes the prompt contract enforceable instead
			// of merely asking the model not to spawn unnecessary agents.
			exposed = withoutDelegationTools(exposed)
		}
		// The separate planner pass has already created the visible roadmap. The
		// executor must receive work tools immediately; advertising only
		// update_plan here caused a redundant turn and the exact multi-minute idle
		// gap seen after a plan appeared. update_plan is planner-owned and is not
		// part of executor schemas at any point in a seeded builder run.
		if planSeeded {
			exposed = withoutPlanTool(exposed)
			exposed = withoutExecutorInspectionTools(exposed)
		}
		if preferWriteFile && hasToolDefinition(exposed, "write_file") {
			// A full rewrite must never spend minutes streaming through the
			// surgical edit schema. Keep write_file and append_file available for
			// a complete write or a lossless chunked write.
			exposed = withoutTools(exposed, "edit_file")
		} else if surgicalExistingFile {
			// The target is already on disk and the request is not an explicit
			// full rewrite. Remove complete-file writers before the model starts
			// emitting a potentially hundreds-of-lines-long argument.
			exposed = withoutTools(exposed, "write_file", "create_file", "overwrite_file")
		}

		// PROACTIVE compaction: if the history is approaching the model's
		// context window, summarize the oldest middle before building the
		// request. A no-op when ContextWindow == 0 (compaction disabled).
		messages = compactor.maybeCompact(ctx, provider, messages, exposed)
		// Hybrid local follow-ups: strip images + clamp/prune tool dumps so
		// "Explored" → next act is seconds, not multi-minute ↑ 0 tok.
		request := sonutruntime.CompletionRequest{
			Messages:        requestMessagesForTurn(messages, guards, options, toolsReturnedThisRun),
			Tools:           exposed,
			ReasoningEffort: turnEffort,
			// Executor (actFast) uses the provider's concise private-thinking path.
			// Casual Q&A and open first turns keep their normal answer-quality effort.
			DisableThinking: disableThinking,
		}

		// Report the per-category context budget for this turn so a surface can
		// show utilization. Opt-in: a no-op when OnContext is unset.
		if options.OnContext != nil {
			options.OnContext(MeasureContext(messages, request.Tools, options.ContextWindow))
		}

		// A transient upstream disconnect on the initial connect is retried with
		// backoff (before any content is forwarded, so no OnText is duplicated);
		// a context-limit / image-rejection / cancellation is NOT retried here —
		// those fall through to their own handlers below.
		// Per-turn stream context: when a runaway generation is abandoned (tool-arg
		// or reasoning budget tripped in CollectStreamWithOptions), cancelling this
		// closes the HTTP stream so the backend (llama.cpp) actually stops
		// generating instead of burning the GPU on a dead turn.
		turnStreamCtx, cancelTurnStream := context.WithCancel(ctx)
		// The defer covers every return path out of Run; the explicit cancel after
		// this turn's last collect handles the loop-again path promptly. Cancel is
		// idempotent, so the duplication is harmless. The closure re-reads the
		// variable because the stall-retry below swaps in a fresh context.
		defer func() { cancelTurnStream() }()
		// Include connect+prefill+decode in agent_turn ms (not only CollectStream).
		// Prior logs showed ms=0 for every turn while inference_end reported 55s
		// TTFT — the timer started after the idle wait, so perf.log hid the stall.
		turnWallStart := time.Now()
		if turn == 0 && !plannerHandoffReadyAt.IsZero() {
			observability.PerfLog("planner_executor_handoff",
				"ms", time.Since(plannerHandoffReadyAt).Milliseconds(),
				"tools", len(request.Tools),
				"messages", len(request.Messages),
			)
		}
		stream, err := streamWithReconnect(turnStreamCtx, provider, request, reconnectNoticeFor(options))
		if err != nil {
			if isImageRejectionError(err) {
				result.Messages = copyMessages(messages)
				return result, fmt.Errorf("model %s rejected the image: %s. The model may not support image input — try switching to a vision-capable model (claude, gpt-4o, gemini)", options.Model, err.Error())
			}
			// REACTIVE compaction: a context-limit failure on the call itself
			// can be recovered by compacting once and retrying the same turn.
			if compacted, retried, retryErr := compactor.recover(ctx, provider, messages, request.Tools, err.Error()); retried {
				messages = compacted
				if retryErr != nil {
					result.Messages = copyMessages(messages)
					return result, retryErr
				}
				// Rebuild from the compacted messages but reuse the SAME active-mode
				// partition computed for this turn: it depends
				// on registry+loaded, not on the messages, so they stay valid after
				// compaction. Using the bare toolDefinitions here would route through an
				// empty-loaded partition, re-hiding every already-loaded deferred tool.
				request = sonutruntime.CompletionRequest{
					Messages:        requestMessagesForTurn(messages, guards, options, toolsReturnedThisRun),
					Tools:           exposed,
					ReasoningEffort: turnEffort,
					DisableThinking: request.DisableThinking,
				}
				// Pre-content connect after a context-limit compaction: route through the
				// reconnect helper so a transient upstream hiccup here doesn't fail the
				// whole run and re-burn every token (AUDIT-L1).
				stream, err = streamWithReconnect(turnStreamCtx, provider, request, reconnectNoticeFor(options))
			}
			if err != nil {
				result.Messages = copyMessages(messages)
				return result, err
			}
		}

		// forwardedVisibleText flags forwarded final PROSE (OnText) specifically —
		// the only output whose re-stream on a stall retry would visibly DUPLICATE
		// answer text the user already read. Transient working indicators
		// (reasoning previews, tool-call "writing…" previews) are deliberately NOT
		// counted: a turn that streamed only those then stalled — the common
		// gpt-5.x / ollama "froze mid-write_file" case — is safe to re-issue, since
		// the TUI resets the tool-call preview on the next tool-call-start and a
		// stalled turn is never appended to `messages` (it returns on the error
		// before the append), so the retry re-sends clean context with no
		// conversation-state duplication.
		forwardedVisibleText := false
		// Generation budget is an IDLE watchdog, not absolute wall-clock.
		// Live local hybrid (perf.log 2026-07-25): a healthy write_file streamed
		// for ~6 minutes at ~15 tok/s, then the absolute 2m/6m timer cancelled it
		// mid-file; the one bounded retry used the short base again and the turn
		// died with "generation exceeded the 2m0s wall-clock limit" and zero
		// files on disk. Progress (prefill pings, reasoning tokens, text, tool
		// arg deltas) MUST refresh the timer. Only true silence for
		// GenerationTimeout cancels. File tools also switch to the longer file
		// idle window on start so a brief gap between chunks is not fatal.
		genCtx := turnStreamCtx
		var genCancel context.CancelFunc
		var genTimer *time.Timer
		var genDeadlineFired atomic.Bool
		// activeGenBudget is the idle window currently armed (base or file).
		activeGenBudget := options.GenerationTimeout
		var activeGenTool atomic.Value // string tool name, if any
		resetGenTimer := func(budget time.Duration) {
			if genTimer == nil || budget <= 0 {
				return
			}
			if budget > activeGenBudget {
				activeGenBudget = budget
			}
			genTimer.Reset(activeGenBudget)
		}
		touchGenProgress := func() {
			if genTimer != nil && activeGenBudget > 0 {
				genTimer.Reset(activeGenBudget)
			}
		}
		if options.GenerationTimeout > 0 {
			genCtx, genCancel = context.WithCancel(turnStreamCtx)
			cancelGen := genCancel
			genTimer = time.AfterFunc(options.GenerationTimeout, func() {
				genDeadlineFired.Store(true)
				cancelGen()
			})
			defer func() { genTimer.Stop(); genCancel() }()
		}
		forwardingOpts := sonutruntime.CollectOptions{OnUsage: options.OnUsage}
		// File mutation tools get a live, per-call staging sink. The sink is
		// enabled only for modes that already pre-authorize the write; ask mode
		// still waits for the ordinary completed-call approval before touching
		// disk. Commit closures are consumed later by executeToolCall, after its
		// full permission and beforeTool checks have passed.
		streamingSessions := make(map[string]tools.StreamingFileSession)
		streamingCommits := make(map[string]func() tools.Result)
		options.streamingCommits = streamingCommits
		closeStreamingSessions := func() {
			for _, session := range streamingSessions {
				if session != nil {
					session.Close()
				}
			}
		}
		// A timeout, transport failure, cancellation, or retry can leave this
		// generation path before the normal commit section below. Always flush and
		// close every staging file on exit so the already-generated bytes remain
		// durable and no descriptor keeps a recovery file half-written.
		defer closeStreamingSessions()
		streamedFileArgs := make(map[string]*strings.Builder)
		repeatedFileCalls := make(map[string]repeatedFileGeneration)
		var repeatedFileAbort atomic.Bool
		streamingPermissionGranted := permissionMode == PermissionModeAuto ||
			permissionMode == PermissionModeUnsafe || permissionMode == PermissionModeBypass
		var executorTextBudgetExceeded atomic.Bool
		executorTextBytes := 0
		// Prefill pings, reasoning, text, and tool-arg deltas all prove the
		// generation is alive. Absolute wall-clock is wrong for local models.
		forwardingOpts.OnPing = func(processed, total int) {
			touchGenProgress()
			if options.OnPrefillProgress != nil {
				options.OnPrefillProgress(processed, total)
			}
		}
		// Adaptive reasoning caps: a turn routed to low/no effort must not be
		// allowed to deliberate anyway. Deep-effort turns keep the defaults.
		forwardingOpts.MaxReasoningBytes, forwardingOpts.MaxReasoningDuration =
			reasoningCapsForEffort(turnEffort)
		if fastStart {
			// Some providers treat DisableThinking as advisory. Keep a bounded
			// fallback so an ignored flag cannot recreate the old long first-turn
			// analysis stall.
			forwardingOpts.MaxReasoningBytes = 4096
			forwardingOpts.MaxReasoningDuration = 20 * time.Second
		}
		// The fast executor still gets a bounded private sanity check. Keep the
		// cap above the provider's short-thinking budget so a healthy thought can
		// finish and produce its tool call instead of being retried as a stall.
		var visibleGate visibleTextGate
		forwardingOpts.OnText = func(s string) {
			s = visibleGate.feed(s)
			if s == "" {
				return
			}
			if planSeeded && executorTextBytes+len(s) > maxExecutorVisibleTextBytes {
				// A seeded executor must act through tools. Stop a runaway prose
				// dump before an active local model can spend many minutes emitting
				// source or re-narrating the plan; the bounded nudge below asks for
				// one concrete tool call on the next turn.
				executorTextBudgetExceeded.Store(true)
				cancelTurnStream()
				return
			}
			executorTextBytes += len(s)
			forwardedVisibleText = true
			touchGenProgress()
			if options.OnText != nil {
				options.OnText(s)
			}
		}
		var turnReasoning strings.Builder
		forwardingOpts.OnReasoning = func(s string) {
			turnReasoning.WriteString(s)
			touchGenProgress()
			// Executor phase: never stream thinking to the UI. Planner owns
			// high-think process display; residual executor reasoning is noise
			// and was measured not to improve quality.
			if actFast && !redeepenThisTurn {
				return
			}
			if options.OnReasoning != nil {
				options.OnReasoning(s)
			}
		}
		// Mid-stream rewrite thrash detection: once the path field is visible in
		// a write_file arg stream and that path (or same basename) already
		// succeeded this plan-seeded run, cancel generation immediately instead
		// of burning ~8 minutes regenerating a full game file (live perf.log).
		var streamToolArgs strings.Builder
		var thrashRewriteCancel atomic.Bool
		forwardingOpts.OnToolCallStart = func(id, name string) {
			activeGenTool.Store(name)
			streamToolArgs.Reset()
			if isFileGenerationTool(name) {
				streamedFileArgs[id] = &strings.Builder{}
			}
			resetGenTimer(generationTimeoutForTool(options.GenerationTimeout, name))
			if streamingPermissionGranted && registry != nil {
				resolvedName := registry.Resolve(name)
				if previous := streamingSessions[id]; previous != nil {
					previous.Close()
				}
				if tool, ok := registry.Get(resolvedName); ok {
					if streamable, ok := tool.(tools.StreamingTool); ok {
						session := streamable.StartStreaming(genCtx, tools.RunOptions{
							PermissionGranted: true,
							PermissionMode:    string(permissionMode),
							Sandbox:           options.Sandbox,
							ToolCallID:        id,
							SessionID:         options.SessionID,
							Cwd:               options.Cwd,
							FileTracker:       options.FileTracker,
						})
						if session != nil {
							streamingSessions[id] = session
						}
					}
				}
			}
			if options.OnToolCallStart != nil {
				options.OnToolCallStart(id, name)
			}
		}
		forwardingOpts.OnToolCallDelta = func(id, fragment string) {
			if session := streamingSessions[id]; session != nil {
				session.Feed(fragment)
			}
			if name, ok := activeGenTool.Load().(string); ok && isFileGenerationTool(name) {
				if args := streamedFileArgs[id]; args != nil && !repeatedFileAbort.Load() {
					args.WriteString(fragment)
					path, content := parsePartialWriteArgs(args.String())
					if path != "" && content != "" {
						if clean, repeated := repeatedFileContentPrefix(content); repeated {
							repeatedFileCalls[id] = repeatedFileGeneration{path: path, content: clean}
							repeatedFileAbort.Store(true)
							if genCancel != nil {
								genCancel()
							}
						}
					}
				}
			}
			// File bodies ARE the product: refresh the idle budget on every
			// fragment so a healthy multi-minute local file mutation is never cancelled
			// mid-stream. Non-file tools (exec_command, edit_file, …) keep the
			// absolute idle window from tool-start so a runaway arg stream is
			// still cut short; the collector also enforces byte/duration caps.
			if name, ok := activeGenTool.Load().(string); ok && isFileGenerationTool(name) {
				touchGenProgress()
				if planSeeded && !validationReworkPending && !thrashRewriteCancel.Load() &&
					(name == "write_file" || name == "create_file" || name == "overwrite_file") {
					// Path appears early in JSON; content is the multi-minute tail.
					// Cancel as soon as path is known when the file is already done
					// OR already has a saved partial prefix (append-only) — do not
					// burn 5 minutes regenerating a body that will be discarded.
					if path, _ := parsePartialWriteArgs(streamToolArgs.String()); path != "" &&
						(writePathAlreadySucceeded(successfulFullWrites, path) ||
							isAppendOnlyPath(appendOnlyAfterPartialWrite, path)) {
						thrashRewriteCancel.Store(true)
						if genCancel != nil {
							genCancel()
						}
					}
				}
			}
			if options.OnToolCallDelta != nil {
				options.OnToolCallDelta(id, fragment)
			}
		}
		// recoverStreamError applies the same non-stall recovery the initial stream
		// gets to ANY collected error — including one from a reissued (stall-retry)
		// stream: an image rejection gets the friendly wrapping, and a context limit
		// gets one compaction + reactive reissue (omitting visible callbacks, since
		// any pre-error output was already forwarded). It returns the possibly-updated
		// collected and a non-nil stop error when the run must end now.
		recoverStreamError := func(collected sonutruntime.CollectedStream) (sonutruntime.CollectedStream, error) {
			if isImageRejectionError(errors.New(collected.Error)) {
				return collected, fmt.Errorf("model %s rejected the image: %s. The model may not support image input — try switching to a vision-capable model (claude, gpt-4o, gemini)", options.Model, collected.Error)
			}
			// REACTIVE compaction: the streamed error may also be a context limit
			// (some providers surface it mid-stream). Compact and retry once.
			if compacted, retried, retryErr := compactor.recover(ctx, provider, messages, request.Tools, collected.Error); retried {
				messages = compacted
				if retryErr != nil {
					return collected, retryErr
				}
				// Reuse the SAME active-mode partition (exposed) from this turn rather
				// than the bare toolDefinitions: exposed depends on registry+loaded (not
				// the messages), so it stays valid after compaction.
				retryRequest := sonutruntime.CompletionRequest{
					Messages:        requestMessagesForTurn(messages, guards, options, toolsReturnedThisRun),
					Tools:           exposed,
					ReasoningEffort: turnEffort,
					DisableThinking: request.DisableThinking,
				}
				retryCtx, retryCancel := generationDeadline(turnStreamCtx, options.GenerationTimeout)
				retryStream, retryStreamErr := streamWithReconnect(retryCtx, provider, retryRequest, reconnectNoticeFor(options))
				if retryStreamErr != nil {
					retryCancel()
					return collected, retryStreamErr
				}
				collected = sonutruntime.CollectStreamWithOptions(retryCtx, retryStream, sonutruntime.CollectOptions{
					OnUsage: options.OnUsage,
				})
				retryCancel()
			}
			return collected, nil
		}

		collected := sonutruntime.CollectStreamWithOptions(genCtx, stream, forwardingOpts)
		if repeatedFileAbort.Load() {
			// Stop the stream as soon as a pathological source repetition is
			// observed, then route the clean prefix through the existing partial-file
			// recovery path. Without this normalization, context.Canceled would enter
			// the generic retry path and the model would regenerate the whole body.
			for index := range collected.ToolCalls {
				call := &collected.ToolCalls[index]
				if repeated, ok := repeatedFileCalls[call.ID]; ok {
					call.Aborted = true
					call.AbortReason = "generated file content repeated itself; generation stopped before the repeated tail — continue with append_file from the saved prefix"
					args, _ := json.Marshal(map[string]any{"path": repeated.path, "content": repeated.content})
					call.Arguments = string(args)
				}
			}
			collected.Error = ""
		}
		if tail := visibleGate.flush(); tail != "" {
			// Flush a held bullet line after the stream closes. This is deliberately
			// after collection so the model still receives the exact raw content.
			if options.OnText != nil {
				options.OnText(tail)
			}
		}
		if executorTextBudgetExceeded.Load() {
			// The canceled stream is intentionally not treated as a transport
			// failure and must not enter the expensive stall-retry path.
			closeStreamingSessions()
			cancelTurnStream()
			collected.Error = ""
			collected.Text = ""
			collected.ToolCalls = nil
			collected.DroppedToolCalls = 0
			forwardedVisibleText = false
			messages = append(messages,
				sonutruntime.Message{Role: sonutruntime.MessageRoleAssistant},
				sonutruntime.Message{Role: sonutruntime.MessageRoleUser, Content: "Your executor response exceeded the narration budget. Do not output source or re-plan. " +
					"Issue one concrete tool call for the current plan step now, then continue."},
			)
			continue
		}
		turnMS := time.Since(turnWallStart).Milliseconds()
		observability.PerfLog("agent_turn",
			"turn", turn+1,
			"ms", turnMS,
			"effort", turnEffort,
			"disable_thinking", request.DisableThinking,
			"tools_exposed", len(exposed),
			"tool_calls", len(collected.ToolCalls),
			"text_chars", len(collected.Text),
			"hybrid_followup", toolsReturnedThisRun,
			"flash", flash,
			"run_mode", string(options.RunMode),
			"err", collected.Error != "",
		)
		// Inefficiency signals (always-on in perf.log). These do not change
		// control flow — they make live thrash visible for tuning.
		if isLocalHybridRun(options) {
			if n := len(exposed); n > 14 {
				observability.PerfLog("inefficiency",
					"kind", "tools_schema_tax",
					"tools_exposed", n,
					"turn", turn+1,
					"hint", "defer_or_shrink_eager_tools",
				)
			}
			// Hybrid re-prefill tax: turns that take >45s with few completion
			// tokens are almost always prompt bloat, not model reasoning.
			if turnMS >= 45_000 && len(collected.ToolCalls) <= 1 && len(collected.Text) < 200 {
				observability.PerfLog("inefficiency",
					"kind", "slow_hybrid_turn",
					"ms", turnMS,
					"turn", turn+1,
					"tools_exposed", len(exposed),
					"hybrid_followup", toolsReturnedThisRun,
					"hint", "shrink_prompt_or_ctx",
				)
			}
		}
		// Detect generation timeout: the idle timer fired while the parent
		// ctx is still ok (i.e. not a user/run cancellation).
		genTimedOut := genDeadlineFired.Load() && ctx.Err() == nil
		if genCancel != nil {
			genCancel()
		}
		if genTimedOut || collected.Error != "" {
			// Do this before any recovery retry. The stage file is the durable
			// source of truth for a streamed body; leaving it open while a retry
			// starts risks stale buffered bytes and descriptor leaks.
			closeStreamingSessions()
		}
		// Prefer salvaging a mid-write file over discard+retry. Live failure
		// modes (perf.log 2026-07-25):
		//   • gen timer cancelled a multi-minute write mid-body → retry burned
		//     minutes and landed zero bytes
		//   • stream stall/error WITH a partial write_file/append_file already
		//     in collected.ToolCalls → stall-retry discarded the body and the
		//     next 8–10 turns re-prefaced the same 5k prompt (err thrash, no tools)
		// Mark incomplete file calls Aborted so the partial-recovery path saves
		// completed lines. Applies on gen timeout, thrash cancel, OR any stream
		// error that left file-tool arguments behind.
		//
		// Exception: mid-stream thrash cancel for an already-written path must
		// NOT salvage (would clobber the good file with a partial rewrite).
		if genTimedOut || collected.Error != "" || repeatedFileAbort.Load() {
			closeStreamingSessions()
			// The provider collector can lose the partially assembled tool call
			// itself on a transport failure. The streaming session still has the
			// exact staged bytes on disk, so synthesize the same aborted recovery
			// call from that durable source instead of silently discarding it.
			for id, session := range streamingSessions {
				recovery, ok := session.Recovery()
				if !ok || recovery.Path == "" || recovery.Content == "" || recovery.ToolName == "" {
					continue
				}
				args, _ := json.Marshal(map[string]any{"path": recovery.Path, "content": recovery.Content})
				found := false
				for index := range collected.ToolCalls {
					call := &collected.ToolCalls[index]
					if call.ID != id {
						continue
					}
					found = true
					// An existing aborted call may already have been normalized by
					// repeatedFileContentPrefix to remove malformed source. Only fill
					// genuinely missing arguments from the stage; never replace a
					// cleaner parser-derived recovery with the raw staged tail.
					if strings.TrimSpace(call.Arguments) == "" {
						call.Arguments = string(args)
						call.Name = recovery.ToolName
						call.Aborted = true
						call.AbortReason = "stream stopped after durable file staging; saved bytes will be recovered before retry"
					}
				}
				if !found {
					collected.ToolCalls = append(collected.ToolCalls, ToolCall{
						ID:          id,
						Name:        recovery.ToolName,
						Arguments:   string(args),
						Aborted:     true,
						AbortReason: "stream stopped after durable file staging; saved bytes will be recovered before retry",
					})
				}
			}
		}
		fileGenSalvaged := false
		thrashSkip := thrashRewriteCancel.Load()
		hasFileToolArgs := false
		for i := range collected.ToolCalls {
			if isFileGenerationTool(collected.ToolCalls[i].Name) &&
				strings.TrimSpace(collected.ToolCalls[i].Arguments) != "" {
				hasFileToolArgs = true
				break
			}
		}
		shouldSalvageFileCalls := genTimedOut || thrashSkip ||
			(collected.Error != "" && hasFileToolArgs)
		if shouldSalvageFileCalls && len(collected.ToolCalls) > 0 {
			for i := range collected.ToolCalls {
				call := &collected.ToolCalls[i]
				if thrashSkip && (call.Name == "write_file" || call.Name == "create_file" || call.Name == "overwrite_file") {
					path, _ := parsePartialWriteArgs(call.Arguments)
					if path != "" && (writePathAlreadySucceeded(successfulFullWrites, path) ||
						isAppendOnlyPath(appendOnlyAfterPartialWrite, path)) {
						call.Aborted = true
						if isAppendOnlyPath(appendOnlyAfterPartialWrite, path) {
							call.AbortReason = "rewrite thrash: path already has a saved partial prefix — append_file only for remaining content"
						} else {
							call.AbortReason = "rewrite thrash: path already written this run — skip and continue next plan step"
						}
						// Do not set fileGenSalvaged: dispatch path treats this as skip
						// (or auto-append of delta if the complete body was captured).
						continue
					}
				}
				if call.Aborted {
					if isFileGenerationTool(call.Name) && strings.TrimSpace(call.Arguments) != "" {
						// Thrash skips are not salvageable product.
						if !strings.Contains(call.AbortReason, "rewrite thrash") {
							fileGenSalvaged = true
						}
					}
					continue
				}
				// Incomplete calls never dispatch as-is. File tools with real
				// argument text are salvageable (partial lines on disk); others
				// get a clean abort notice so the model can re-issue smaller.
				call.Aborted = true
				if isFileGenerationTool(call.Name) && strings.TrimSpace(call.Arguments) != "" {
					if call.AbortReason == "" {
						reason := "stream interrupted"
						if genTimedOut {
							reason = fmt.Sprintf("generation idle limit (%s) hit", activeGenBudget)
						} else if collected.Error != "" {
							reason = "stream error mid-tool"
						}
						call.AbortReason = fmt.Sprintf(
							"%s mid-%s; completed lines will be saved — continue with append_file for the rest in ≤100-line chunks",
							reason, call.Name,
						)
					}
					fileGenSalvaged = true
				} else if call.AbortReason == "" {
					call.AbortReason = "generation interrupted before this tool call finished; re-issue a smaller, complete call"
				}
			}
			if fileGenSalvaged || thrashSkip {
				// Always clear the stream error when we can dispatch salvaged
				// file content or a thrash-skip. Leaving Error set made the
				// stall-retry path discard ToolCalls and burn multi-minute
				// re-prefills with no disk progress (perf.log turns 12–20).
				collected.Error = ""
				genTimedOut = false
			}
		}
		// Generation-timeout retry: only when NOTHING useful was produced
		// (no text, no salvageable file args). Incomplete non-file tool calls
		// are discarded — they never hit the filesystem. Thinking stays ON
		// (quality + cache-stable template on local hybrid). Retry inherits
		// the file budget when the timed-out turn was generating a file.
		if genTimedOut && !forwardedVisibleText && collected.Text == "" && !fileGenSalvaged {
			// Drop incomplete tool calls so the retry starts clean.
			collected.ToolCalls = nil
			cancelTurnStream()
			turnStreamCtx, cancelTurnStream = newTurnStreamContext(ctx)
			timeoutRetryMessages := requestMessagesForTurn(messages, guards, options, true)
			timeoutRetryMessages = append(timeoutRetryMessages, sonutruntime.Message{
				Role: sonutruntime.MessageRoleUser,
				Content: "[system] Your previous generation went idle past the time limit with no completed tool call. " +
					"Think at most one short sentence, then act immediately with the single most direct tool call " +
					"(prefer write_file with real source content, not narration) or give your best short answer now.",
			})
			// Keep the same concise-thinking template during timeout recovery so a
			// retry can sanity-check the next action without reopening a long plan.
			timeoutEffort := "low"
			timeoutDisable := request.DisableThinking
			if planSeeded || actFast {
				timeoutEffort = "none"
			}
			timeoutRetryRequest := sonutruntime.CompletionRequest{
				Messages:        timeoutRetryMessages,
				Tools:           exposed,
				ReasoningEffort: timeoutEffort,
				DisableThinking: timeoutDisable,
			}
			timeoutOpts := forwardingOpts
			timeoutOpts.MaxReasoningBytes, timeoutOpts.MaxReasoningDuration = reasoningCapsForEffort("low")
			// Retry budget: if a file tool was already streaming, keep the long
			// file ceiling. Otherwise stay on the ordinary idle budget so a pure
			// hang does not occupy the GPU for the full 12m file window.
			retryBudget := options.GenerationTimeout
			if name, ok := activeGenTool.Load().(string); ok && name != "" {
				retryBudget = generationTimeoutForTool(options.GenerationTimeout, name)
			} else if planSeeded {
				// Plan-seeded executors almost always need a write next; give the
				// first retry enough runway to emit a real file body.
				retryBudget = generationTimeoutForTool(options.GenerationTimeout, "write_file")
			}
			retryCtx, retryCancel := generationDeadline(turnStreamCtx, retryBudget)
			if retryStream, retryErr := streamWithReconnect(retryCtx, provider, timeoutRetryRequest, reconnectNoticeFor(options)); retryErr == nil {
				// Re-arm progress-aware idle timer for the retry collect as well
				// (generationDeadline is a hard outer bound; touchGenProgress still
				// keeps the soft idle watchdog fresh while tokens flow).
				activeGenBudget = retryBudget
				if genTimer != nil {
					genDeadlineFired.Store(false)
					genTimer.Reset(retryBudget)
				}
				collected = sonutruntime.CollectStreamWithOptions(retryCtx, retryStream, timeoutOpts)
			} else {
				collected.Error = fmt.Sprintf("generation retry failed after timeout: %v", retryErr)
			}
			retryCancel()
		}
		if genTimedOut && (collected.Error == context.Canceled.Error() || collected.Error == context.DeadlineExceeded.Error()) {
			collected.Error = fmt.Sprintf("generation exceeded the %s idle limit after one bounded retry", options.GenerationTimeout)
		}
		if collected.Error != "" {
			updated, stop := recoverStreamError(collected)
			collected = updated
			if stop != nil {
				result.Messages = copyMessages(messages)
				return result, stop
			}
		}
		// Check ctx first: on cancellation helpers.go sets collected.Error to
		// ctx.Err().Error(), so returning errors.New(collected.Error) would lose
		// the wrapped sentinel and break errors.Is(err, context.Canceled).
		if ctx.Err() != nil {
			result.Messages = copyMessages(messages)
			return result, ctx.Err()
		}
		// A stream idle/stall timeout is safely re-issued when the turn committed NO
		// answer text — no forwarded visible prose (forwardedVisibleText) and no
		// collected final text (collected.Text). This covers two cases:
		//   1. Nothing streamed at all before the connection died (the original
		//      macOS stale-pooled-connection hang past the response-header timeout).
		//   2. The model streamed transient reasoning and began a tool call (e.g. a
		//      large write_file) then froze mid-arguments — the common gpt-5.x /
		//      ollama heartbeat-pause stall. That partial tool call is NEVER executed
		//      (a turn with collected.Error returns before dispatch below) and NEVER
		//      appended to `messages`, so a retry re-issues clean context; the only
		//      re-render is transient previews, not duplicated answer text. This is
		//      why the gate no longer excludes collected.ToolCalls: an incomplete
		//      tool call from a timed-out stream is discard-and-retry, not output.
		// A turn that forwarded real prose is NOT retried (it would duplicate visible
		// answer text) and falls through to the error return below. Capped +
		// exponential backoff, with a user-visible notice per attempt.
		//
		// Recovery: inject an act-now nudge. Thinking stays ON (user quality +
		// local-hybrid cache stability). Re-issuing with a 512-byte cap was the
		// common "retrying 1/2" dead end — the model re-thought past the cap and
		// burned minutes of idle with still-zero tokens. Reasoning-exceeded
		// stalls skip the network backoff (this is not a connect failure).
		for attempt := 1; attempt <= maxStreamStallRetries &&
			isStreamTimeoutError(collected.Error) && !forwardedVisibleText &&
			collected.Text == ""; attempt++ {
			if notify := stallRetryNoticeFor(options); notify != nil {
				notify(attempt, maxStreamStallRetries)
			}
			reasoningStall := strings.Contains(collected.Error, "reasoning exceeded") ||
				strings.Contains(collected.Error, "reasoning overrun")
			// Network/silence stalls still back off so a restarting backend can
			// come up; pure reasoning stalls retry immediately (no 2s+ idle tax).
			if !reasoningStall {
				if err := sleepWithContext(ctx, backoffFor(attempt)); err != nil {
					result.Messages = copyMessages(messages)
					return result, err
				}
			}
			// Tear down the abandoned stream before reissuing: on a single-slot
			// local backend the old runaway generation would otherwise keep the
			// GPU busy and queue the retry behind it.
			cancelTurnStream()
			// Indirection keeps vet's lostcancel happy: the deferred closure above
			// re-reads cancelTurnStream, so the fresh context cannot leak.
			turnStreamCtx, cancelTurnStream = newTurnStreamContext(ctx)
			retryMessages := requestMessagesForTurn(messages, guards, options, true)
			stallNudge := "[system] Your previous attempt produced no usable output and timed out. " +
				"Think at most one short sentence, then act immediately with the single most direct tool call, " +
				"or give a short final answer if the work is already done."
			if reasoningStall {
				// More specific wording when the stall was a reasoning budget breach
				// so the model knows the think-block itself was the problem.
				stallNudge = "[system] Your previous attempt spent too long thinking and was cut off. " +
					"Think at most one short sentence, then issue the single most direct tool call immediately, " +
					"or give the final answer if the work is already done."
			}
			retryMessages = append(retryMessages, sonutruntime.Message{
				Role:    sonutruntime.MessageRoleUser,
				Content: stallNudge,
			})
			// Stall recovery keeps the same concise-thinking template for executor
			// turns. Re-issuing with deep thinking recreated the original idle spiral.
			stallEffort := "none"
			if !planSeeded && !actFast {
				stallEffort = "low"
			}
			retryRequest := sonutruntime.CompletionRequest{
				Messages:        retryMessages,
				Tools:           exposed,
				ReasoningEffort: stallEffort,
				DisableThinking: request.DisableThinking,
			}
			retryCtx, retryCancel := generationDeadline(turnStreamCtx, options.GenerationTimeout)
			retryStream, retryErr := streamWithReconnect(retryCtx, provider, retryRequest, reconnectNoticeFor(options))
			if retryErr != nil {
				retryCancel()
				result.Messages = copyMessages(messages)
				return result, retryErr
			}
			// Caps match the retry's effort so recovery is not re-killed mid-think.
			retryOpts := forwardingOpts
			retryOpts.MaxReasoningBytes, retryOpts.MaxReasoningDuration = reasoningCapsForEffort(stallEffort)
			collected = sonutruntime.CollectStreamWithOptions(retryCtx, retryStream, retryOpts)
			retryCancel()
		}
		if collected.Error != "" {
			// Route a reissued stream's non-stall error through the SAME recovery as
			// the initial stream (image-rejection wrapping / context-limit compaction)
			// rather than returning it raw.
			updated, stop := recoverStreamError(collected)
			collected = updated
			if stop != nil {
				result.Messages = copyMessages(messages)
				return result, stop
			}
			if collected.Error != "" {
				result.Messages = copyMessages(messages)
				return result, errors.New(collected.Error)
			}
		}
		// A complete call can now promote its staged sibling through the normal
		// executeToolCall path. Incomplete/aborted calls deliberately keep their
		// staging file on disk for recovery and are never committed here.
		for _, call := range collected.ToolCalls {
			if call.Aborted {
				continue
			}
			if session := streamingSessions[call.ID]; session != nil {
				if commit, ok := session.PrepareCommit(ctx, call.Arguments); ok {
					streamingCommits[call.ID] = commit
				}
			}
		}
		for _, session := range streamingSessions {
			session.Close()
		}
		// This turn's streaming is complete — tear down the request now so an
		// abandoned runaway generation stops on the backend immediately instead
		// of at the end of the whole run.
		cancelTurnStream()

		// Carry the turn's terminal stop reason so a final answer cut off at the
		// output token cap (or by a content filter) is reported as truncated. A
		// tool-call turn normalizes to "" and clears any prior reason.
		result.FinishReason = collected.FinishReason

		messages = append(messages, sonutruntime.Message{
			Role:      sonutruntime.MessageRoleAssistant,
			Content:   collected.Text,
			ToolCalls: historySafeToolCalls(collected.ToolCalls),
			// Preserve thinking blocks so the next turn can replay them; providers
			// that use extended thinking reject tool conversations that drop them.
			Reasoning: collected.ReasoningBlocks,
		})
		textClaimsImplementation := claimsFileWrite(collected.Text) ||
			strings.Contains(collected.Text, "```") ||
			strings.Contains(strings.ToLower(collected.Text), "i will ")
		noToolBuilderStall := (planSeeded || options.UnifiedWorkflow) && textClaimsImplementation
		if hasExecutorCapability(registry, options) &&
			(noToolBuilderStall || (explicitToolUse && totalToolCalls == 0)) &&
			len(collected.ToolCalls) == 0 &&
			strings.TrimSpace(collected.Text) != "" &&
			executorNoToolNudges < 2 {
			executorNoToolNudges++
			nudge := "Your response described a tool call but did not execute one. Invoke the required registered tool through the tool interface now. Do not print or simulate tool syntax and do not narrate another plan."
			if pending := guards.firstPendingPlanStep(); pending != "" {
				nudge = "Do not display source code as your answer. The implementation is not complete until it is written to the workspace. Call write_file, append_file, or edit_file now with the actual source for the pending step: " + pending
			}
			messages = append(messages, sonutruntime.Message{Role: sonutruntime.MessageRoleUser, Content: nudge})
			continue
		}
		if len(collected.ToolCalls) > 0 {
			executorNoToolNudges = 0
		}
		// Reasoning is not visible to the model as assistant prose, so the
		// narration guard cannot stop a repeated private plan by itself. Add the
		// correction only when this turn also made a tool call; text-only turns
		// already have the bounded visible-answer repetition gate below.
		if len(collected.ToolCalls) > 0 {
			if nudge := guards.checkRepeatedReasoning(turnReasoning.String()); nudge != "" {
				messages = append(messages, sonutruntime.Message{Role: sonutruntime.MessageRoleUser, Content: nudge})
			}
		}

		// Repetition / hallucination guard: if the model produced the same non-trivial
		// text as its previous turn (no tool calls either time), it is looping.
		// Inject a strong break nudge once and reset the baseline so the guard fires
		// at most once per repeated block, not on every subsequent repeat.
		// Skipped under RequireCompletionSignal: the completion gate below already
		// bounds no-tool-call retries via maxContinueNudges and finalizes
		// Incomplete on budget exhaustion, so this guard firing on identical
		// repeated stall text would double-consume that budget without ever
		// incrementing continueNudges (the two guards fighting over the same
		// turns before the gate can reach its own bounded decision).
		if !options.RequireCompletionSignal && len(collected.ToolCalls) == 0 && strings.TrimSpace(collected.Text) != "" {
			trimmed := strings.TrimSpace(collected.Text)
			if trimmed == lastAssistantText && len(trimmed) > 30 {
				lastAssistantText = ""
				messages = append(messages, sonutruntime.Message{
					Role: sonutruntime.MessageRoleUser,
					Content: "You just repeated your previous response exactly. " +
						"You are in a loop. Stop repeating and take a DIFFERENT action: " +
						"use a tool, ask a clarifying question, or state clearly what is blocking you.",
				})
				continue
			}
		}
		if strings.TrimSpace(collected.Text) != "" && len(collected.ToolCalls) == 0 {
			lastAssistantText = strings.TrimSpace(collected.Text)

			// Near-duplicate narration guard: catches a REWORDED repeat (not just
			// verbatim), unlike the exact-match guard above, and runs regardless of
			// RequireCompletionSignal -- the direct fix for a model that re-narrates
			// the same plan recap turn after turn instead of acting. Skipped when the
			// text ends with a continuation cue: that case already has its own
			// bounded handling below (endsWithContinuationCue / maxContinueNudges),
			// and letting both guards intercept the same repeated text would double
			// -bank retries against two separate, uncoordinated budgets.
			if !endsWithContinuationCue(collected.Text) {
				if nudge := guards.checkDuplicateNarration(collected.Text); nudge != "" {
					messages = append(messages, sonutruntime.Message{
						Role:    sonutruntime.MessageRoleUser,
						Content: nudge,
					})
					// A reworded repeat means the fast/no-think executor is stuck in a
					// loop — escalate the NEXT turn back to full thinking so the model
					// actually rethinks instead of re-narrating the same step.
					redeepenNext = true
					continue
				}
			}
			// Narration-only text (the model describing what it will do instead of
			// acting) is handled by the completion gate below when
			// RequireCompletionSignal is on (bounded continue-nudges via
			// endsWithContinuationCue), and is accepted as the final answer in the
			// legacy path — so it is not separately guarded here. An unbounded
			// standalone narration guard previously intercepted this text before the
			// completion gate could run, looping forever on a model that keeps
			// repeating the same narration (no test exercised it, and it broke both
			// the legacy "text-only turn is the answer" contract and the gate's own
			// bounded retry contract).
			//
			// File-claim guard: if the model's text asserts it edited/wrote/created
			// files but NO file-writing tool has been called this run, it is
			// hallucinating completed work. Force it to actually use the tool.
			if !fileWriteToolCalledThisRun && claimsFileWrite(collected.Text) {
				messages = append(messages, sonutruntime.Message{
					Role: sonutruntime.MessageRoleUser,
					Content: "STOP. You claimed to have edited or written files, but you have NOT called " +
						"write_file, edit_file, or apply_patch — no file has actually been changed. " +
						"This is a hallucination. Call write_file or edit_file NOW to make the actual change. " +
						"Do not output any more text until you have called a file tool and it has returned success.",
				})
				continue
			}
		} else if len(collected.ToolCalls) > 0 {
			lastAssistantText = "" // tool calls = progress, reset baseline
			// Track whether a file-writing tool ran this turn, and whether the most
			// recent tool activity is an unverified write (in-order: a non-write tool
			// after a write counts as the verification/read-back step).
			// Also maintain the exploration counter: consecutive read-only tool calls
			// with no file edit count toward the exploration nudge threshold.
			turnHadWrite := false
			for _, call := range collected.ToolCalls {
				if isFileWriteTool(call.Name) {
					fileWriteToolCalledThisRun = true
					editSinceVerify = true
					turnHadWrite = true
				} else {
					editSinceVerify = false
				}
			}
			if turnHadWrite {
				consecutiveReadOnly = 0
			} else {
				consecutiveReadOnly++
			}
		}

		if len(collected.ToolCalls) == 0 {
			// The model intended a tool call but it was malformed and dropped.
			// Tell it to retry rather than silently treating text as the answer.
			// This path is handled before the no-output guard so a dropped-call
			// turn is never counted as a runaway empty turn.
			if collected.DroppedToolCalls > 0 {
				messages = append(messages, sonutruntime.Message{
					Role:    sonutruntime.MessageRoleUser,
					Content: droppedToolCallNotice,
				})
				continue
			}
			// A reasoning model can put its private checklist in the content
			// channel. Treat the sanitized user-facing text as the real output so
			// a response containing only "Thinking Process" is retried for an
			// answer instead of being accepted as an empty success.
			visibleAnswer := sanitizeVisibleAnswer(collected.Text)
			visibleCollected := collected
			visibleCollected.Text = visibleAnswer
			// No-output guard: a turn with visible text is a real final answer.
			// A truly-empty turn (no text, no tool calls, no dropped calls) is
			// counted toward the runaway cap so we stop before burning maxTurns.
			if guards.observeTurn(visibleCollected) {
				result.FinalAnswer = noOutputStopAnswer(result.Turns)
				result.Messages = copyMessages(messages)
				return result, nil
			}
			if strings.TrimSpace(visibleAnswer) == "" {
				// Empty-but-under-cap turn: nudge the model to make progress
				// rather than treating the empty response as a final answer.
				messages = append(messages, sonutruntime.Message{
					Role: sonutruntime.MessageRoleUser,
					Content: "Your previous response had no visible output and no tool calls. " +
						"Continue the task by using a tool or reply with your final answer.",
				})
				continue
			}
			if len(stagedRewritePaths) > 0 {
				if stagedRewriteNudges < 2 {
					stagedRewriteNudges++
					for _, rewrite := range stagedRewritePaths {
						messages = append(messages, sonutruntime.Message{
							Role: sonutruntime.MessageRoleUser,
							Content: "The replacement for " + rewrite.target + " is still staged and the original file is intentionally preserved. " +
								"Do not give a final answer yet. Continue append_file chunks to " + rewrite.stage +
								"; when complete, run exec_command: mv -- " + rewrite.stage + " " + rewrite.target + ".",
						})
					}
					continue
				}
				result.Incomplete = true
				result.IncompleteReason = "interrupted replacement is still staged and was not promoted"
				result.FinalAnswer = visibleAnswer + " The original file was preserved; the replacement remains staged and was not promoted."
				result.Messages = copyMessages(messages)
				return result, nil
			}
			// Completion gate (headless): a turn with text but no tool call is the
			// model's final answer ONLY when the work is actually done. Default off
			// (RequireCompletionSignal), so interactive runs stay byte-identical.
			if options.RequireCompletionSignal {
				// (1) Self-report downgrade (strongest, unambiguous): the model's own
				// final message admits it guessed / could not meet the objective. Checked
				// FIRST so an admitted-impossible task is downgraded immediately (no wasted
				// continue-nudges) and reports the accurate reason.
				if reason := selfReportedIncompletion(visibleAnswer); reason != "" {
					result.Incomplete = true
					result.IncompleteReason = reason
					result.FinalAnswer = visibleAnswer
					result.Messages = copyMessages(messages)
					return result, nil
				}

				// (2) The model stopped without a tool call while work may be unfinished:
				//   - a continuation cue ("…Let me check the config:") is an unambiguous
				//     mid-step stop;
				//   - pending update_plan items are a WEAK, ambiguous signal (the model may
				//     have finished without re-marking the last step).
				// Nudge to continue (bounded). After the budget: a persisted continuation
				// cue finalizes INCOMPLETE; pending-plan WITHOUT a cue does NOT (that would
				// false-fail a completed run with stale bookkeeping) — fall through to the
				// acceptance check / success.
				cue := endsWithContinuationCue(visibleAnswer)
				// Seeded roadmaps are planner-owned. The executor has no update_plan
				// tool and must never stop to narrate or repair planner bookkeeping.
				planPending := !planSeeded && guards.pendingPlanItems()
				if cue || planPending {
					if continueNudges < maxContinueNudges {
						continueNudges++
						nudgeText := continueNudge("your message ended mid-step")
						if !cue {
							// Prescriptive, not descriptive: name the exact call the model
							// must make so it acts instead of re-narrating the plan. If an
							// already-written file obviously satisfies a pending step, say
							// so explicitly instead of a generic "items pending" nudge.
							if reminder := guards.obviousStepCompletionReminder(visibleAnswer); reminder != "" {
								nudgeText = reminder
							} else {
								nudgeText = planPendingNudge()
							}
						}
						messages = append(messages, sonutruntime.Message{
							Role:    sonutruntime.MessageRoleUser,
							Content: nudgeText,
						})
						continue
					}
					if cue {
						result.Incomplete = true
						result.IncompleteReason = "your message ended mid-step"
						result.FinalAnswer = visibleAnswer
						result.Messages = copyMessages(messages)
						return result, nil
					}
					// pending-plan only, budget spent: trust the model's completion claim
					// over stale plan bookkeeping; fall through.
				}

				// (3) Task-grounded acceptance: before accepting a "done" turn as success,
				// require ONE acceptance check grounded in the task's stated criterion
				// (only when self-correct is on). Rejects "well-formed == correct",
				// "existing-tests-pass == objective met", and "result == baseline" false
				// successes. Bounded to a single pass; a genuine post-check completion
				// (no admission, no cue) then finalizes as success on the next turn.
				// Only when the run actually did work: a greeting or plain Q&A
				// turn with zero tool calls has nothing to verify, and nudging
				// it sends the model hunting for a task that never existed.
				if options.SelfCorrect != nil && !acceptanceRequested && totalToolCalls > 0 {
					acceptanceRequested = true
					messages = append(messages, sonutruntime.Message{
						Role:    sonutruntime.MessageRoleUser,
						Content: acceptanceVerificationNudge(),
					})
					continue
				}
			}
			// A generic build/test/read-back does not prove a rendered UI works.
			// If frontend files changed, refuse one attempted final answer until
			// a successful headful browser call observes or exercises the page.
			if guards.needsBrowserVerification() &&
				hasUsableBrowserVerificationTool(registry, permissionMode, options) &&
				!browserVerificationFinalNudgeSent {
				browserVerificationFinalNudgeSent = true
				messages = append(messages, sonutruntime.Message{
					Role: sonutruntime.MessageRoleUser,
					Content: "Frontend verification is still pending. Do not finish yet. Use the loaded " +
						"mcp_playwright_browser_navigate and mcp_playwright_browser_snapshot tools in the visible " +
						"(headful) browser, interact with the changed UI, and confirm the post-interaction state.",
				})
				continue
			}
			// Verify-before-done gate (interactive path): the model is concluding
			// with a completion claim while the most recent tool activity was a file
			// write with nothing run since to prove it works. Nudge once for a
			// verification step (build/test/run/read-back); bounded so a model that
			// insists it is done still terminates on the next turn.
			// (acceptanceRequested means the headless self-correct gate already asked
			// for a task-grounded check this run — don't stack a second nudge.)
			if options.VerifyBeforeDone && editSinceVerify && !verifyNudgeSent && !acceptanceRequested && claimsCompletion(visibleAnswer) {
				verifyNudgeSent = true
				messages = append(messages, sonutruntime.Message{
					Role: sonutruntime.MessageRoleUser,
					Content: "You edited files but have not verified the change. Before concluding, run ONE " +
						"command or read-back that proves it works (build, test, run the code, or re-read " +
						"the edited file), then give your final answer.",
				})
				continue
			}
			// A live user message arrived while this final answer was being
			// produced — don't end the run (and don't burn a validation pass on
			// an answer that is about to be superseded). Loop again so the
			// top-of-turn drain injects it and the model answers the newest
			// instruction too. The assistant text above is already in
			// `messages`, so nothing is lost.
			if options.Steer != nil && !options.Steer.Empty() {
				continue
			}
			// Planner validation: the same deep planning role reviews concrete
			// execution evidence before the answer is accepted. There is no
			// separate evaluator agent and no validation text is shown to users.
			if validationReworkPending && toolCallsSinceValidation == 0 {
				if continueNudges < maxContinueNudges {
					continueNudges++
					messages = append(messages, sonutruntime.Message{
						Role: sonutruntime.MessageRoleUser,
						Content: "The planner already identified unfinished work. Do not explain or re-plan it. " +
							"Execute the first pending corrected-plan step now with one concrete tool call.",
					})
					continue
				}
				result.Incomplete = true
				result.IncompleteReason = "planner-requested rework made no tool progress"
				result.FinalAnswer = visibleAnswer
				result.Messages = copyMessages(messages)
				return result, nil
			}
			if options.Evaluate && !options.UnifiedWorkflow && totalToolCalls > 0 && toolCallsSinceValidation > 0 && plannerValidationRounds < maxPlannerValidationRounds && turnShape != TurnShapeNone {
				plannerValidationRounds++
				if options.OnValidationStart != nil {
					options.OnValidationStart()
				}
				mainSystemPrompt := buildSystemPrompt(options)
				validationTask := prompt
				if strings.TrimSpace(options.PlannerPrompt) != "" {
					validationTask = options.PlannerPrompt
				}
				passed, feedback, validationPlan, validationErr := validateWithPlanner(ctx, provider, mainSystemPrompt, validationTask, messages, visibleAnswer, options)
				if validationErr != nil {
					if errors.Is(validationErr, context.Canceled) && ctx.Err() != nil {
						return result, ctx.Err()
					}
					passed = false
					feedback = "Planner validation could not complete: " + validationErr.Error() + ". Re-run the relevant verification before concluding."
				}
				if validationPlan != "" {
					seededPlan = strings.TrimSpace(validationPlan)
					guards.replacePlannerPlan(seededPlan)
					if options.OnPlanProgress != nil {
						// Validation updates the existing sticky roadmap in place. Calling
						// OnPlan here used to append a second "Plan:" transcript row and
						// the TUI parser discarded [completed]/[failed] statuses.
						options.OnPlanProgress(guards.plannerPlanItems())
					} else if options.OnPlan != nil {
						// Compatibility for headless integrations that only implement the
						// older text callback.
						options.OnPlan(validationPlan)
					}
				}
				// Keep the legacy hook source-compatible for integrations; it now
				// reports planner validation rather than a separate evaluator.
				if options.OnEvaluator != nil {
					options.OnEvaluator(passed, feedback)
				}
				toolCallsSinceValidation = 0
				if !passed {
					validationReworkPending = true
					// Planner asked for fixes — allow rewriting paths that were
					// marked done during the first executor pass.
					for k := range successfulFullWrites {
						delete(successfulFullWrites, k)
					}
					for k := range appendOnlyAfterPartialWrite {
						delete(appendOnlyAfterPartialWrite, k)
					}
					correctedPlan := strings.TrimSpace(validationPlan)
					planDirective := ""
					if correctedPlan != "" {
						planDirective = "\n\nCORRECTED PLAN (planner-owned):\n" + correctedPlan
					}
					messages = append(messages, sonutruntime.Message{
						Role: sonutruntime.MessageRoleUser,
						Content: "Planner validation found unfinished or unverified work:\n\n" +
							feedback + planDirective +
							"\n\nThis is executor phase: do not think through or restate the roadmap and do not call update_plan. " +
							"Fix the first pending item with the smallest necessary tool call (prefer a small edit_file over rewriting whole files), then answer. " +
							"Do not re-implement steps the planner already marked completed.",
					})
					continue
				}
				validationReworkPending = false
			}
			// Re-check steering after validation too: a message pushed while the
			// planner reviewed the run must also keep it alive.
			if options.Steer != nil && !options.Steer.Empty() {
				continue
			}
			result.FinalAnswer = visibleAnswer
			result.Messages = copyMessages(messages)
			return result, nil
		}

		// A turn with tool calls is progress: update guard counters before
		// executing so the empty-turn counter resets and plan-tracking signals
		// stay current.
		guards.observeTurn(collected)
		totalToolCalls += len(collected.ToolCalls)

		failureHint := ""
		// turnRequestedModel records the FIRST mid-run escalation target requested
		// during this turn's tool batch. The actual provider switch happens once,
		// after the batch, so every tool_result is recorded first and at most one
		// switch occurs per turn.
		turnRequestedModel := ""
		// changedFilesThisBatch aggregates every file the turn's mutating tools
		// touched, so post-edit self-correction runs ONCE over the union after the
		// batch — one AfterEdit call keeps the per-run attempt budget accurate and
		// avoids verifying an intermediate edit a later call in the same turn already
		// superseded. Its feedback is appended after the loop so every advertised
		// tool call keeps its tool_result contiguous (a user message interleaved
		// between tool_results breaks strict provider replay) — same after-batch
		// rationale as turnRequestedModel above.
		var changedFilesThisBatch []string
		// intentNotesThisBatch collects newly discovered directory guidelines;
		// appended after the batch so tool_results stay contiguous.
		var intentNotesThisBatch []string
		// A local model can emit a whole batch of reads that were already served
		// on an earlier turn. Those calls are suppressed, but without an explicit
		// transition the next inference simply proposes the same batch again.
		duplicateReadSuppressed := 0
		executedTool := 0
		wroteFileThisBatch := false
		// thrashSkipThisBatch: rewrite of an already-written path was skipped.
		// emptyMutationThisBatch: empty append / zero-content mutation.
		// hardMutationFailThisBatch: aborted without salvage or tool error on write.
		thrashSkipThisBatch := false
		emptyMutationThisBatch := false
		hardMutationFailThisBatch := false
		// A weak model can emit several identical calls in one assistant
		// response. Keep those duplicates suppressible without hard-stopping
		// before the model has received the first block notice.
		batchCallKeys := make(map[string]int, len(collected.ToolCalls))
		for index, call := range collected.ToolCalls {
			// Canonicalize near-miss tool names (str_replace → edit_file, shell →
			// exec_command, …) before any guard/permission logic, so an aliased
			// call is treated exactly like its canonical tool end to end.
			call.Name = registry.Resolve(call.Name)
			// Normalize a redundant current-directory prefix before any read/write
			// guard sees the call. Native tools already resolve this spelling, but
			// the agent must do so too or index.html and dummy/index.html become
			// separate guard keys, causing duplicate reads and lost write recovery.
			call = canonicalizeToolCallPathForCwd(call, options.Cwd)
			// Keep the canonical form in the durable assistant tool-call batch too.
			// The range variable above is a copy; leaving the original untouched made
			// later progress/mutation guards see the pre-normalized path and tool name.
			collected.ToolCalls[index] = call
			executorPlanCall := planSeeded && call.Name == "update_plan"
			// Stub-content guard: reject write_file/create_file calls whose content
			// is suspiciously short (< 5 lines) or contains placeholder markers.
			// The tool description says "FULL final file — never a stub", but models
			// sometimes ignore it. Catching it here forces a retry before the file
			// is actually written on disk.
			if call.Name == "write_file" || call.Name == "create_file" || call.Name == "overwrite_file" {
				var callArgs map[string]any
				if call.Arguments != "" {
					_ = decodeToolArguments(call.Arguments, &callArgs)
				}
				if content, ok := callArgs["content"].(string); ok {
					trimmed := strings.TrimSpace(content)
					lineCount := 0
					if trimmed != "" {
						lineCount = strings.Count(trimmed, "\n") + 1
					}
					lower := strings.ToLower(content)
					isStub := lineCount < 5 && (strings.Contains(content, "...") ||
						strings.Contains(content, "<!-- ") ||
						strings.Contains(lower, "todo") ||
						strings.Contains(lower, "placeholder") ||
						strings.Contains(lower, "skeleton") ||
						strings.Contains(lower, "stub") ||
						strings.Contains(lower, "// rest of") ||
						strings.Contains(lower, "# rest of") ||
						strings.Contains(lower, "/* rest"))
					if isStub {
						stubMsg := sonutruntime.Message{
							Role: sonutruntime.MessageRoleUser,
							Content: "REJECTED: write_file content is a stub/skeleton (only " +
								fmt.Sprintf("%d", lineCount) + " lines with placeholder markers). " +
								"The content field MUST be the FULL final file with every line. " +
								"Do NOT abbreviate, summarize, or use '...' placeholders. " +
								"If the content is large, use exec_command with curl to download " +
								"the real content instead of generating it. Retry with the complete file.",
						}
						messages = append(messages, sonutruntime.Message{
							Role:      sonutruntime.MessageRoleAssistant,
							Content:   collected.Text,
							ToolCalls: collected.ToolCalls[:index+1],
						}, stubMsg)
						continue turnLoop
					}
				}
			}
			if options.OnToolCall != nil && !executorPlanCall {
				options.OnToolCall(call)
			}
			toolDispatchStart := time.Now()
			emitToolLifecycleProgress(options, call, tools.LifecycleRunning, "running", "running "+call.Name, nil, nil)
			var toolResult ToolResult
			var abortErr error
			if executorPlanCall {
				// Defense in depth for a model that emits a hidden tool name from
				// prior context. The roadmap was already authored by the planner;
				// keep this result internal and move straight back to execution.
				toolResult = ToolResult{
					ToolCallID: call.ID,
					Name:       call.Name,
					Status:     tools.StatusOK,
					Output:     "Ignored: update_plan is planner-owned in executor phase. Continue with the next pending work step immediately.",
					Meta:       map[string]string{"internal_dedup": "true", "executor_plan_call": "true"},
				}
			} else if call.Name == "append_file" && func() bool {
				path, _ := parsePartialAppendArgs(call.Arguments)
				return appendBlockedAfterEditAbort[recoveryPathKey(path)]
			}() {
				toolResult = ToolResult{
					ToolCallID: call.ID,
					Name:       call.Name,
					Status:     tools.StatusError,
					Output: "Error: append_file was rejected because the previous oversized edit for this target was incomplete and not applied. " +
						"The existing file remains unchanged. Do not append the discarded edit fragment; use a small anchored edit/apply_patch, " +
						"or create a separate CSS/JS file and link it.",
					Meta: map[string]string{"recovery": "true", "recovery_tool": "edit_file", "append_after_edit_abort": "true"},
				}
			} else if call.Name == "append_file" && func() bool {
				path, _ := parsePartialAppendArgs(call.Arguments)
				_, ok := stagedRewritePaths[recoveryPathKey(path)]
				return ok
			}() {
				// An interrupted replacement is staged beside the existing target.
				// Redirect future append chunks to that staging file so the old
				// target remains valid until the replacement is complete.
				path, content := parsePartialAppendArgs(call.Arguments)
				stage := stagedRewritePaths[recoveryPathKey(path)].stage
				recoveredCall := call
				recoveredArgs, _ := json.Marshal(map[string]any{"path": stage, "content": content})
				recoveredCall.Arguments = string(recoveredArgs)
				recoveredCall.Aborted = false
				recoveredCall.AbortReason = ""
				recovery, recoveryAbort := executeToolCall(ctx, registry, recoveredCall, permissionMode, options)
				if recoveryAbort != nil {
					abortErr = recoveryAbort
				}
				if recovery.Meta == nil {
					recovery.Meta = make(map[string]string)
				}
				recovery.Meta["staged_rewrite"] = "true"
				recovery.Meta["staged_path"] = stage
				if recovery.Status != tools.StatusError {
					executedTool++
					guards.recordArgDispatch()
					recovery.Output = "Appended replacement chunk to staging file " + stage + ". " +
						"The existing target is still preserved. Continue appending to the staging file; " +
						"when complete, promote it with exec_command: mv -- " + stage + " " + path + "."
				}
				toolResult = recovery
			} else if planSeeded && (call.Name == "write_file" || call.Name == "create_file" || call.Name == "overwrite_file") && !call.Aborted && func() bool {
				path, _ := parsePartialWriteArgs(call.Arguments)
				_, ok := stagedRewritePaths[recoveryPathKey(path)]
				return ok
			}() {
				// If the model ignores the append-only instruction and resends a
				// complete write, append only its new suffix to the saved stage.
				path, content := parsePartialWriteArgs(call.Arguments)
				rewrite := stagedRewritePaths[recoveryPathKey(path)]
				existing := readWorkspaceFile(options.Cwd, rewrite.stage)
				delta := contentDeltaForAppend(existing, content)
				if strings.TrimSpace(delta) == "" {
					toolResult = ToolResult{
						ToolCallID: call.ID,
						Name:       call.Name,
						Status:     tools.StatusOK,
						Output:     "The replacement content is already staged for " + path + ". Continue with the remaining append chunk; do not rewrite the target.",
						Meta:       map[string]string{"staged_rewrite": "true", "staged_path": rewrite.stage, "internal_dedup": "true"},
					}
				} else {
					recoveredCall := call
					recoveredCall.Name = "append_file"
					recoveredArgs, _ := json.Marshal(map[string]any{"path": rewrite.stage, "content": delta})
					recoveredCall.Arguments = string(recoveredArgs)
					recoveredCall.Aborted = false
					recoveredCall.AbortReason = ""
					recovery, recoveryAbort := executeToolCall(ctx, registry, recoveredCall, permissionMode, options)
					if recoveryAbort != nil {
						abortErr = recoveryAbort
					}
					if recovery.Meta == nil {
						recovery.Meta = make(map[string]string)
					}
					recovery.Meta["staged_rewrite"] = "true"
					recovery.Meta["staged_path"] = rewrite.stage
					if recovery.Status != tools.StatusError {
						executedTool++
						guards.recordArgDispatch()
						recovery.Output = "Converted the repeated write into an append to staged replacement " + rewrite.stage + ". " +
							"The original target remains preserved; continue from the saved edit."
					}
					toolResult = recovery
				}
			} else if (call.Name == "write_file" || call.Name == "create_file" || call.Name == "overwrite_file") && !call.Aborted &&
				func() bool {
					path, _ := parsePartialWriteArgs(call.Arguments)
					return isAppendOnlyPath(appendOnlyAfterPartialWrite, path)
				}() {
				// Partial prefix is already on disk. Do NOT discard the newly
				// generated body — convert write_file → append of only the delta
				// so minutes of local generation are never thrown away.
				path, content := parsePartialWriteArgs(call.Arguments)
				existing := readWorkspaceFile(options.Cwd, path)
				delta := contentDeltaForAppend(existing, content)
				if strings.TrimSpace(delta) == "" {
					toolResult = ToolResult{
						ToolCallID: call.ID,
						Name:       call.Name,
						Status:     tools.StatusOK,
						Output: "Skipped: " + path + " already contains the generated content (or only re-sent the saved prefix). " +
							"Continue with append_file of ONLY the remaining unwritten source, or give a short final answer if the file is complete.",
						Meta: map[string]string{"internal_dedup": "true", "append_only": "true", "path": path},
					}
				} else {
					recoveredCall := call
					recoveredCall.Name = "append_file"
					recoveredCall.Aborted = false
					recoveredCall.AbortReason = ""
					recoveredArgs, _ := json.Marshal(map[string]any{"path": path, "content": delta})
					recoveredCall.Arguments = string(recoveredArgs)
					recovery, recoveryAbort := executeToolCall(ctx, registry, recoveredCall, permissionMode, options)
					if recoveryAbort != nil {
						abortErr = recoveryAbort
					}
					if recovery.Status != tools.StatusError {
						executedTool++
						guards.recordArgDispatch()
						if recovery.Meta == nil {
							recovery.Meta = make(map[string]string)
						}
						// Complete write_file body (not mid-stream abort) → model
						// intended a finished file; unlatch so we don't thrash.
						unlatchAppendOnly(appendOnlyAfterPartialWrite, path)
						markWriteSucceeded(successfulFullWrites, path)
						recovery.Meta["auto_append_delta"] = "true"
						recovery.Meta["recovery_tool"] = "append_file"
						recovery.Output = "Auto-continued from saved prefix (no rewrite): " + recovery.Output +
							" The generated write_file body was converted to an append of only the new lines. " +
							"Do NOT regenerate this file. Move to the next plan step or append any truly missing tail."
						toolResult = recovery
					} else {
						toolResult = ToolResult{
							ToolCallID: call.ID,
							Name:       call.Name,
							Status:     tools.StatusError,
							Output: "Error: " + path + " has a saved partial prefix; auto-append of the new content failed: " +
								recovery.Output + " Retry with append_file path=" + path + " content=<ONLY the remaining lines>.",
							Meta: map[string]string{"recovery": "true", "append_only": "true", "path": path},
						}
					}
				}
			} else if planSeeded && (call.Name == "write_file" || call.Name == "create_file" || call.Name == "overwrite_file") && !call.Aborted &&
				!validationReworkPending && func() bool {
				path, _ := parsePartialWriteArgs(call.Arguments)
				return writePathAlreadySucceeded(successfulFullWrites, path)
			}() {
				// Plan-seeded executor thrashing: rewriting a file it already
				// completed this run (same path OR same basename under another
				// directory). Planner validation owns "is it good enough?";
				// the executor must move on. Unplanned multi-write runs (session
				// allow, iterative polish without a plan) may still rewrite.
				// Exception: validation rework explicitly asked for fixes.
				path, _ := parsePartialWriteArgs(call.Arguments)
				toolResult = ToolResult{
					ToolCallID: call.ID,
					Name:       call.Name,
					Status:     tools.StatusOK,
					Output: "Skipped: " + path + " (or the same basename) was already written successfully this run. " +
						nextFileProgressNudge(guards.firstPendingPlanStep(), guards.firstPendingPlanFile(),
							guards.firstPendingPlanStep() == "" && guards.firstPendingPlanFile() == ""),
					Meta: map[string]string{"internal_dedup": "true", "rewrite_skipped": "true", "path": path},
				}
			} else if call.Aborted {
				guards.recordArgAbort()
				// The model generated an unreasonably large/slow tool call (see
				// sonutruntime.CollectOptions.MaxToolArgBytes/MaxToolArgDuration).
				// Arguments are only a partial fragment — never dispatch it to the
				// tool. Feed the reason back so the model retries in smaller chunks.
				abortOutput := "Error: " + call.AbortReason
				fileChunkRecovered := false
				// Thrash cancel: never clobber a good/partial file with a rewrite.
				// If the aborted body has new lines beyond a saved partial prefix,
				// auto-append the delta so multi-minute generation is not discarded.
				if strings.Contains(call.AbortReason, "rewrite thrash") {
					path, content := parsePartialWriteArgs(call.Arguments)
					if path != "" && isAppendOnlyPath(appendOnlyAfterPartialWrite, path) &&
						strings.TrimSpace(content) != "" {
						existing := readWorkspaceFile(options.Cwd, path)
						delta := contentDeltaForAppend(existing, content)
						if strings.TrimSpace(delta) != "" {
							recoveredCall := call
							recoveredCall.Name = "append_file"
							recoveredCall.Aborted = false
							recoveredCall.AbortReason = ""
							recoveredArgs, _ := json.Marshal(map[string]any{"path": path, "content": delta})
							recoveredCall.Arguments = string(recoveredArgs)
							recovery, recoveryAbort := executeToolCall(ctx, registry, recoveredCall, permissionMode, options)
							if recoveryAbort != nil {
								abortErr = recoveryAbort
							}
							if recovery.Status != tools.StatusError {
								executedTool++
								fileChunkRecovered = true
								guards.recordArgDispatch()
								if recovery.Meta == nil {
									recovery.Meta = make(map[string]string)
								}
								recovery.Meta["auto_append_delta"] = "true"
								recovery.Meta["partial_recovery"] = "true"
								recovery.Meta["recovery_tool"] = "append_file"
								recovery.Output = "Rewrite thrash cancelled; saved NEW lines via append (nothing lost): " +
									recovery.Output + " Keep using append_file only. Do not rewrite."
								toolResult = recovery
							}
						}
					}
					if !fileChunkRecovered {
						toolResult = ToolResult{
							ToolCallID: call.ID,
							Name:       call.Name,
							Status:     tools.StatusOK,
							Output: "Skipped: " + path + " was already written successfully this run (rewrite thrash cancelled mid-stream). " +
								nextFileProgressNudge(guards.firstPendingPlanStep(), guards.firstPendingPlanFile(),
									guards.firstPendingPlanStep() == "" && guards.firstPendingPlanFile() == ""),
							Meta: map[string]string{"internal_dedup": "true", "rewrite_skipped": "true", "path": path},
						}
						fileChunkRecovered = true // prevent error overwrite below
					}
				} else if call.Name == "write_file" || call.Name == "append_file" || call.Name == "edit_file" {
					// A partial write_file is safe to preserve as a file prefix. A partial
					// append_file is safe to preserve as complete appended lines. A
					// partial edit_file is not safe to apply: its new_string is only a
					// fragment.
					var path, content string
					switch call.Name {
					case "write_file":
						path, content = parsePartialWriteArgs(call.Arguments)
					case "append_file":
						path, content = parsePartialAppendArgs(call.Arguments)
					default:
						path, content = parsePartialEditArgs(call.Arguments)
						if len(content) < minEditSalvageBytes {
							content = "" // tiny fragment — not a rewrite salvage
						}
					}
					// Also refuse salvage when this path already has a full success —
					// a late thrash abort must not overwrite the good file.
					if (call.Name == "write_file" || call.Name == "create_file" || call.Name == "overwrite_file") &&
						writePathAlreadySucceeded(successfulFullWrites, path) {
						toolResult = ToolResult{
							ToolCallID: call.ID,
							Name:       call.Name,
							Status:     tools.StatusOK,
							Output: "Skipped: " + path + " already exists from an earlier successful write. Partial rewrite discarded. " +
								nextFileProgressNudge(guards.firstPendingPlanStep(), guards.firstPendingPlanFile(),
									guards.firstPendingPlanStep() == "" && guards.firstPendingPlanFile() == ""),
							Meta: map[string]string{"internal_dedup": "true", "rewrite_skipped": "true", "path": path},
						}
						fileChunkRecovered = true
					} else if call.Name == "edit_file" {
						// An aborted edit contains only a prefix of new_string. It is
						// never a valid replacement for the original file. Do not put
						// that fragment back into model context: weaker executors treated
						// it as a complete file on the next turn and overwrote the good
						// target with it.
						if path != "" && content != "" {
							appendBlockedAfterEditAbort[recoveryPathKey(path)] = true
							abortOutput = "Error: oversized edit_file was not applied; the original file was preserved. " +
								"Discard the incomplete generated fragment. Re-plan from the current " + path + ". " +
								"Prefer a small 1-3 line edit_file/apply_patch change, or create a separate CSS/JS asset and link it. " +
								"Only use write_file when you have the complete final file."
						} else {
							abortOutput = "Error: oversized edit_file was not applied; the original file was preserved. " +
								"Re-plan from the current file; use a small 1-3 line edit_file/apply_patch change, " +
								"or create a separate asset and link it."
						}
					} else if (call.Name == "write_file" || call.Name == "append_file") && path != "" && content != "" {
						// Persist every complete generated line. If a prefix is already
						// on disk (append-only latch or prior partial), auto-append the
						// delta — never drop mid-stream content with a fail-closed error.
						//
						// Exception: tiny / wrong-type prefixes (bare JS into .html) are
						// NOT salvageable — latching them forces endless append thrash
						// (live: supermario.html with only `const TILE = 32`).
						if !tools.IsSalvageablePartialContent(path, content) {
							// Clear any append-only latch so a clean rewrite is allowed.
							unlatchAppendOnly(appendOnlyAfterPartialWrite, path)
							existing := readWorkspaceFile(options.Cwd, path)
							abortOutput = "Error: truncated " + call.Name + " for " + path +
								" was discarded — the partial body is not a usable start for this file type. "
							if existing != "" {
								abortOutput += "The existing file was preserved unchanged. "
							}
							abortOutput += "Do NOT append. Call write_file with a proper beginning: " +
								"for .html start with <!DOCTYPE html> containing <canvas> and <script>; " +
								"put game logic inside the script (or use a .js file). " +
								"First chunk ≤100 complete lines, then append_file."
						} else {
							recoveryName := call.Name
							recoveryContent := content
							existing := readWorkspaceFile(options.Cwd, path)
							// Never append onto a broken non-HTML .html stub.
							if tools.HTMLPathNeedsRewrite(path, existing) {
								unlatchAppendOnly(appendOnlyAfterPartialWrite, path)
								abortOutput = "Error: " + path + " on disk is not valid HTML (raw JS/CSS dump). " +
									"Discarded the truncated chunk. Call write_file with overwrite: true and a full HTML shell first."
							} else {
								useAppend := call.Name == "append_file" || isAppendOnlyPath(appendOnlyAfterPartialWrite, path) ||
									(call.Name == "write_file" && existing != "")
								if useAppend && call.Name == "write_file" {
									// Existing good file that is NOT our partial: refuse to
									// append a truncated rewrite (would corrupt HTML/JS).
									// Only auto-append when append-only is latched.
									if !isAppendOnlyPath(appendOnlyAfterPartialWrite, path) {
										useAppend = false
									}
								}
								if useAppend {
									recoveryName = "append_file"
									recoveryContent = contentDeltaForAppend(existing, content)
								}
								if useAppend && strings.TrimSpace(recoveryContent) == "" {
									executedTool++
									fileChunkRecovered = true
									guards.recordArgDispatch()
									toolResult = ToolResult{
										ToolCallID: call.ID,
										Name:       call.Name,
										Status:     tools.StatusOK,
										Output: "The oversized generation re-sent content already on disk for " + path +
											". Nothing new to append. Continue with append_file of ONLY the remaining unwritten source.",
										Meta: map[string]string{
											"partial_recovery": "true",
											"recovery_tool":    "append_file",
											"path":             path,
										},
									}
								} else if strings.TrimSpace(recoveryContent) != "" {
									recoveryArgs := map[string]any{"path": path, "content": recoveryContent}
									if recoveryName == "write_file" {
										recoveryArgs["overwrite"] = true
										recoveryArgs["partial_recovery"] = true
									}
									recoveredArgs, _ := json.Marshal(recoveryArgs)
									recoveredCall := call
									recoveredCall.Name = recoveryName
									recoveredCall.Arguments = string(recoveredArgs)
									recoveredCall.Aborted = false
									recoveredCall.AbortReason = ""
									recovery, recoveryAbort := executeToolCall(ctx, registry, recoveredCall, permissionMode, options)
									if recoveryAbort != nil {
										abortErr = recoveryAbort
									}
									if recovery.Status != tools.StatusError {
										executedTool++
										fileChunkRecovered = true
										// Productive save — reset runaway abort streak.
										guards.recordArgDispatch()
										if recovery.Meta == nil {
											recovery.Meta = make(map[string]string)
										}
										recovery.Meta["partial_recovery"] = "true"
										recovery.Meta["recovery_tool"] = recoveryName
										if recoveryName == "append_file" && call.Name == "write_file" {
											recovery.Meta["auto_append_delta"] = "true"
										}
										// Prefix/chunk is on disk — next chunks must append.
										latchAppendOnly(appendOnlyAfterPartialWrite, path)
										// Clear success marks so salvage is not treated as done.
										if key := recoveryPathKey(path); key != "" {
											delete(successfulFullWrites, key)
											if base := recoveryPathBase(key); base != "" {
												delete(successfulFullWrites, base)
											}
										}
										nextLine := ""
										if total := recovery.Meta["file.total_lines"]; total != "" {
											nextLine = " The file now ends at line " + total + "; continue from line " + total + "."
										} else {
											saved := readWorkspaceFile(options.Cwd, path)
											lines := strings.Count(saved, "\n")
											if saved != "" && !strings.HasSuffix(saved, "\n") {
												lines++
											}
											if lines > 0 {
												nextLine = fmt.Sprintf(" The saved content ends at line %d; continue from line %d.", lines, lines)
											}
										}
										recovery.Output = "Saved generated content (nothing lost): " + recovery.Output +
											nextLine + " Append ONLY the remaining content with append_file, in chunks of at most 100 lines. " +
											"Do NOT call write_file on this path again — that restarts generation and wastes minutes. " +
											"Do not re-read the full file. Do not move to the next plan step until this file is complete."
										toolResult = recovery
									} else if call.Name == "write_file" && !useAppend && !planSeeded &&
										strings.Contains(recovery.Output, "already exists") {
										if options.FileTracker != nil {
											_ = options.FileTracker.StashRejectedWrite("\x00partial-"+path, content)
										}
										abortOutput = "Error: oversized rewrite of " + path +
											" was not applied; the existing complete file was preserved. " +
											"Generated incomplete prefix was NOT written (would corrupt the file). " +
											"Use a small edit_file/apply_patch, or append_file only for true extensions. " +
											"Do not regenerate the whole file."
									} else if planSeeded && call.Name == "write_file" && !useAppend &&
										strings.Contains(recovery.Output, "already exists") {
										// Keep an existing target atomic, but do not discard the
										// generated prefix. Stage it beside the target and let the
										// executor continue with append_file chunks. This removes the
										// old "large write interrupted" dead end without risking a
										// partial replacement over a valid file.
										stage := partialRewriteStagePath(path)
										stageArgs, _ := json.Marshal(map[string]any{
											"path":             stage,
											"content":          content,
											"overwrite":        true,
											"partial_recovery": true,
										})
										stageCall := call
										stageCall.Name = "write_file"
										stageCall.Arguments = string(stageArgs)
										stageCall.Aborted = false
										stageCall.AbortReason = ""
										staged, stageAbort := executeToolCall(ctx, registry, stageCall, permissionMode, options)
										if stageAbort != nil {
											abortErr = stageAbort
										}
										if staged.Status != tools.StatusError {
											stagedRewritePaths[recoveryPathKey(path)] = stagedRewrite{target: path, stage: stage}
											executedTool++
											fileChunkRecovered = true
											guards.recordArgDispatch()
											if staged.Meta == nil {
												staged.Meta = make(map[string]string)
											}
											staged.Meta["partial_recovery"] = "true"
											staged.Meta["staged_rewrite"] = "true"
											staged.Meta["staged_path"] = stage
											staged.Output = "Large replacement was interrupted safely. The existing " + path +
												" was preserved; the generated prefix is staged at " + stage +
												". Continue with append_file chunks to that staging path. " +
												"When complete, promote it with exec_command: mv -- " + stage + " " + path + "."
											toolResult = staged
										} else {
											abortOutput = "Error: could not stage the interrupted replacement for " + path + ": " + staged.Output
										}
									} else {
										abortOutput = "Error: oversized " + call.Name + " recovery could not save the completed lines: " + recovery.Output
									}
								}
							} // end: not HTMLPathNeedsRewrite
						} // end: IsSalvageablePartialContent
					}
				}
				if !fileChunkRecovered {
					toolResult = ToolResult{
						ToolCallID: call.ID,
						Name:       call.Name,
						Status:     tools.StatusError,
						Output:     abortOutput,
						Meta:       map[string]string{"recovery": "true", "recovery_tool": call.Name},
					}
				}
			} else if repeat := func() repeatCallOutcome {
				// Same path already read this run → suppress. Different files are
				// always allowed (executors need multi-file understanding).
				// recordCallSignature still blocks byte-identical consecutive spam.
				return guards.recordReadFile(call.Name, call.Arguments)
			}(); repeat.Stop || repeat.Blocked {
				if repeat.Stop {
					messages = appendAbortedToolResults(messages, collected.ToolCalls[index:])
					result.FinalAnswer = repeat.StopNotice
					result.Messages = copyMessages(messages)
					return result, nil
				}
				// Cross-turn same-file de-duplication. Keep this result internal so
				// the user never sees a fake red tool failure. Notice always points
				// the model at OTHER paths — never bans read_file entirely.
				toolResult = ToolResult{
					ToolCallID: call.ID,
					Name:       call.Name,
					Status:     tools.StatusOK,
					Output:     repeat.BlockNotice,
					Meta:       map[string]string{"internal_dedup": "true"},
				}
				duplicateReadSuppressed++
			} else if repeat := func() repeatCallOutcome {
				guards.recordArgDispatch()
				return guards.recordCallSignatureInBatch(call.Name, call.Arguments, batchCallKeys)
			}(); repeat.Stop {
				// Consecutive-identical-call loop: the model re-issued the same
				// call past the hard cap despite block notices. Halt the run,
				// closing out every advertised tool_use so the recorded messages
				// stay valid for a strict provider replay.
				messages = appendAbortedToolResults(messages, collected.ToolCalls[index:])
				if repeat.StopNotice != "" {
					result.FinalAnswer = repeat.StopNotice
				} else {
					result.FinalAnswer = repeatCallStopAnswer(call.Name, guards.lastCallStreak)
				}
				result.Messages = copyMessages(messages)
				return result, nil
			} else if repeat.Blocked {
				// Third+ consecutive identical call: don't re-execute — feed the
				// block notice back so the model moves on instead of looping.
				toolResult = ToolResult{
					ToolCallID: call.ID,
					Name:       call.Name,
					// This is an internal de-duplication result, not a failed user
					// operation. Keep the corrective text in the model context, but
					// mark it so interactive surfaces can hide the plumbing row.
					Status: tools.StatusOK,
					Output: repeat.BlockNotice,
					Meta:   map[string]string{"internal_dedup": "true"},
				}
			} else if notice := emptyAppendFileNotice(call); notice != "" {
				emptyAppendStreak++
				pending := guards.firstPendingPlanStep()
				if emptyAppendStreak >= 2 {
					notice = "HARD STOP: empty append_file thrash (×" + strconv.Itoa(emptyAppendStreak) + "). " +
						"Do NOT call append_file again without a non-empty body. " +
						"Call edit_file or write_file with a COMPLETE feature block of real source code"
					if pending != "" {
						notice += " implementing: " + pending
					}
					notice += " (minimum ~30 lines of HTML/JS/CSS). Empty content is forbidden."
				} else if pending != "" {
					notice += " REQUIRED next: implement \"" + pending +
						"\" with real source code in content= (not a description). " +
						"Example shape: append_file path=... content=<actual JS/HTML for this step>."
				}
				toolResult = ToolResult{
					ToolCallID: call.ID,
					Name:       call.Name,
					Status:     tools.StatusOK,
					Output:     notice,
					Meta: map[string]string{
						"internal_dedup":      "true",
						"empty_append_guard":  "true",
						"empty_append_streak": strconv.Itoa(emptyAppendStreak),
					},
				}
			} else if notice := invalidFrontendCommand(call, prompt, options, seededPlan); notice != "" {
				toolResult = ToolResult{
					ToolCallID: call.ID,
					Name:       call.Name,
					Status:     tools.StatusOK,
					Output:     notice,
					Meta:       map[string]string{"internal_dedup": "true", "frontend_command_guard": "true"},
				}
			} else {
				executedTool++
				toolStart := time.Now()
				toolResult, abortErr = executeToolCall(ctx, registry, call, permissionMode, options)
				observability.PerfLog("tool",
					"name", call.Name,
					"ms", time.Since(toolStart).Milliseconds(),
					"status", string(toolResult.Status),
					"out_bytes", len(toolResult.Output),
					"err", abortErr != nil,
				)
				// Cache the output so repeated identical calls receive the cached
				// result without re-executing (dedup guard in recordCallSignature).
				guards.cacheToolCallResult(call.Name, call.Arguments, toolResult.Output)
				// A missing edit anchor is not recoverable by appending to the target,
				// and doing so used to corrupt otherwise valid HTML/CSS. Latch the
				// target immediately even when the edit was a complete (non-aborted)
				// call, so the next model action is forced to choose a fresh anchor or
				// a full write instead of blindly appending the rejected fragment.
				if call.Name == "edit_file" &&
					((toolResult.Status == tools.StatusError && strings.Contains(strings.ToLower(toolResult.Output), "old_string not found")) ||
						toolResult.Meta != nil && toolResult.Meta["edit_anchor_miss"] == "true") {
					path, _ := parsePartialEditArgs(call.Arguments)
					if key := recoveryPathKey(path); key != "" {
						appendBlockedAfterEditAbort[key] = true
					}
					if toolResult.Meta == nil {
						toolResult.Meta = make(map[string]string)
					}
					toolResult.Meta["edit_anchor_miss"] = "true"
				}
				// Hosted-only intent layer: first touch of a sub-tree may surface
				// that provider's optional local guideline file.
				if intent != nil && call.Arguments != "" {
					var callArgs map[string]any
					if decodeToolArguments(call.Arguments, &callArgs) == nil {
						if note := intent.observeToolCall(callArgs); note != "" {
							intentNotesThisBatch = append(intentNotesThisBatch, note)
						}
					}
				}
			}
			normalizeToolResultLifecycle(&toolResult)
			elapsed := time.Since(toolDispatchStart)
			emitToolLifecycleProgress(options, call, toolResult.Lifecycle, "complete", toolProgressMessage(toolResult), toolResult.Error, &elapsed)
			if options.OnToolResult != nil {
				options.OnToolResult(toolResult)
			}
			// Unified builder runs create their roadmap in the same continuous
			// conversation. Promote a successful model-authored update_plan into
			// the loop's execution state immediately: subsequent turns can switch
			// to act-fast mode, advance concrete steps from writes, and avoid
			// re-reading/re-planning after an interrupted prompt. The plan tool's
			// output is the authoritative persisted snapshot for this run.
			if options.UnifiedWorkflow && !planSeeded && call.Name == "update_plan" && toolResult.Status == tools.StatusOK {
				planSeeded = true
				seededPlan = strings.TrimSpace(toolResult.Output)
				guards.seedPlannerPlan(seededPlan)
				plannerHandoffReadyAt = time.Now()
			}
			guards.recordSandboxBlock(call.Name, toolResult.Output, toolResult.Status == tools.StatusError)
			// Union the deferred tools this result asked to load into the per-run
			// set BEFORE any abort/stop/guard branch, so a load that coincides with
			// a turn-ending result is still recorded for the next turn's partition.
			for _, name := range toolResult.LoadedTools {
				loaded[name] = true
			}
			if turnRequestedModel == "" && toolResult.RequestedModel != "" {
				turnRequestedModel = toolResult.RequestedModel
			}
			toolOut := toolResult.Output
			// Cap tool dumps in the live history for hybrid local: every follow-up
			// re-prefills these bodies from zero. Full 128 KiB/file reads were the
			// multi-minute post-Explore stall. Model can re-read a tighter range.
			// Head+tail keep (qwen-style) preserves structure and trailing errors.
			if isLocalHybridRun(options) && len(toolOut) > localToolResultMaxBytes {
				note := "\n\n[truncated for local prefill speed — re-read with a tighter start_line/end_line/max_lines if you need more]"
				capBody := localToolResultMaxBytes - len(note)
				if capBody < 64 {
					toolOut = truncateUTF8(toolOut, localToolResultMaxBytes)
				} else {
					toolOut = tools.FitTextHeadTail(toolOut, capBody, note)
				}
			}
			messages = append(messages, sonutruntime.Message{
				Role:       sonutruntime.MessageRoleTool,
				Content:    toolOut,
				ToolCallID: toolResult.ToolCallID,
			})
			// Any tool result arms concise executor mode for subsequent turns — even
			// when the planner never seeded a plan.
			toolsReturnedThisRun = true
			if toolResult.Status == tools.StatusError {
				// Soft recovery errors already tell the model exactly what to do
				// next (append remaining, skip rewrite). Re-deepening to high
				// effort after those burned multi-minute re-plans on local hybrid
				// without changing strategy. Only hard/unknown failures re-deepen.
				softRecovery := toolResult.Meta != nil &&
					(toolResult.Meta["append_only"] == "true" ||
						toolResult.Meta["recovery"] == "true" ||
						toolResult.Meta["rewrite_skipped"] == "true" ||
						toolResult.Meta["append_after_edit_abort"] == "true")
				if !softRecovery {
					// A failed step needs real thought — restore full effort for
					// the next turn so the model can diagnose instead of speed-running.
					redeepenNext = true
				}
			}

			// A tool may demand the run ABORT — a canceled/timed-out ask_user prompt
			// returns context.Canceled rather than fabricating a headless answer. Stop
			// promptly with that error (or run-context cancellation), keeping messages
			// valid by closing out the still-advertised calls first.
			if abortErr == nil && ctx.Err() != nil {
				abortErr = ctx.Err()
			}
			if abortErr != nil {
				messages = appendAbortedToolResults(messages, collected.ToolCalls[index+1:])
				result.Messages = copyMessages(messages)
				return result, abortErr
			}
			if stopReason := stopReasonFromToolResult(toolResult); stopReason != "" {
				messages = appendAbortedToolResults(messages, collected.ToolCalls[index+1:])
				result.FinalAnswer = nonEmptyFinalAnswer(toolResult.Output, guards, &result)
				result.StopReason = stopReason
				result.Messages = copyMessages(messages)
				return result, nil
			}

			// Repeated-failure guard: if a tool keeps failing the same way, hint
			// once (with its schema) then halt — so no model loops on a bad call.
			// Only RETRIABLE failures (bad arguments / execution errors) drive it:
			// policy refusals (disabled tool, permission denial, sandbox block)
			// aren't fixed by reformatting the call, so a "match this schema" hint
			// would misdirect the model toward JSON shape or blocked behavior.
			browserConsoleFailure := isBrowserVerifyToolName(call.Name) &&
				browserOutputHasConsoleErrors(toolResult.Output)
			retriableFailure := isRetriableToolError(toolResult) || browserConsoleFailure
			outcome := guards.observeToolResult(call.Name, retriableFailure, toolResult.Output)
			if browserConsoleFailure {
				// A final-answer nudge that was already consumed must be available
				// again after a reported runtime error; otherwise the next confident
				// text turn could slip through while verification is still pending.
				browserVerificationFinalNudgeSent = false
			}
			if outcome.Stop {
				// The assistant message advertised EVERY collected tool call, but
				// the guard halts mid-turn so the calls after this one never run.
				// Append an aborted placeholder result for each remaining call so
				// every tool_use has a matching tool_result and the recorded
				// messages stay valid for a strict provider replay (Anthropic
				// rejects a tool_use with no answering tool_result).
				messages = appendAbortedToolResults(messages, collected.ToolCalls[index+1:])
				result.FinalAnswer = toolFailureStopAnswer(call.Name, outcome.Count)
				result.Messages = copyMessages(messages)
				return result, nil
			}
			if outcome.InjectHint && failureHint == "" {
				failureHint = toolFailureHint(call.Name, toolSchemaJSON(registry, call.Name), toolResult.Output)
			}
			// Try-Best (MiMo): near-identical edit thrash / bash retry / action
			// streak → soft strategy-change nudge. Never hard-stops.
			guards.observeTryBest(call.Name, toolCallArgsMap(call.Arguments), toolResult)

			// After the first successful file mutation on a UI task, expand the
			// Playwright surface so verify can click/type without another
			// tool_search round-trip — but only THEN (not on the cold turn).
			if toolResult.Status == tools.StatusOK {
				switch strings.ToLower(strings.TrimSpace(call.Name)) {
				case "write_file", "append_file", "edit_file", "create_file",
					"overwrite_file", "apply_patch", "str_replace":
					maybeExpandBrowserToolsAfterWrite(loaded, registry, prompt+"\n"+options.PlannerPrompt, true)
				}
			}

			// Browser-specific loop budget: repeating the same action+target
			// (e.g. clicking the same covered ref) forces a snapshot + replan,
			// and too many consecutive browser failures (any action/target)
			// halts the run instead of burning the rest of the turn budget on
			// a blocked browser session.
			if isBrowserToolName(call.Name) {
				browserOutcome := guards.observeBrowserToolResult(browserActionKey(call), retriableFailure)
				if browserOutcome.HardStop {
					messages = appendAbortedToolResults(messages, collected.ToolCalls[index+1:])
					result.FinalAnswer = browserActionStopAnswer(browserOutcome.StreakCount)
					result.Messages = copyMessages(messages)
					return result, nil
				}
				if browserOutcome.ForceSnapshot {
					messages = append(messages, sonutruntime.Message{
						Role: sonutruntime.MessageRoleUser,
						Content: "The same browser action (" + browserActionKey(call) + ") has failed " +
							fmt.Sprintf("%d", browserActionForceSnapshotAt) + " times in a row. Do not retry it again. " +
							"Call the browser snapshot tool (mcp_playwright_browser_snapshot) now to see the page's actual current state, then replan your next action from that.",
					})
				}
			}

			// Post-edit self-correction: collect the files this successful mutating
			// tool changed; verification runs once over the union after the batch.
			// A read-only tool (no ChangedFiles) never contributes.
			partialRecovery := toolResult.Meta != nil && toolResult.Meta["partial_recovery"] == "true"
			autoAppendDone := toolResult.Meta != nil && toolResult.Meta["auto_append_delta"] == "true" && !partialRecovery
			recoveryTool := ""
			if toolResult.Meta != nil {
				recoveryTool = toolResult.Meta["recovery_tool"]
			}
			if toolResult.Meta != nil {
				if toolResult.Meta["rewrite_skipped"] == "true" {
					thrashSkipThisBatch = true
				}
				if toolResult.Meta["empty_append_guard"] == "true" {
					emptyMutationThisBatch = true
				}
			}
			isMutatingCall := call.Name == "write_file" || call.Name == "append_file" || call.Name == "edit_file" ||
				call.Name == "create_file" || call.Name == "overwrite_file" || call.Name == "apply_patch" ||
				recoveryTool == "append_file" || recoveryTool == "write_file"
			if toolResult.Status == tools.StatusOK && len(toolResult.ChangedFiles) > 0 {
				// Any real bytes on disk clear empty-append thrash (including partial saves).
				if isMutatingCall {
					emptyAppendStreak = 0
				}
				for _, f := range toolResult.ChangedFiles {
					guards.recordWrittenFile(f)
					key := recoveryPathKey(f)
					if call.Name != "append_file" && recoveryTool != "append_file" {
						delete(appendBlockedAfterEditAbort, key)
					}
					if partialRecovery {
						// Incomplete prefix/chunk — force append-only and do NOT
						// mark the path fully done or advance plan steps yet.
						latchAppendOnly(appendOnlyAfterPartialWrite, f)
						if key != "" {
							delete(successfulFullWrites, key)
							if base := recoveryPathBase(key); base != "" {
								delete(successfulFullWrites, base)
							}
						}
						continue
					}
					// A complete write/create lands the full file; drop append-only
					// latch and mark the path (+ basename) done so the executor
					// cannot thrash path twins (dummy/x.html vs x.html).
					if call.Name == "write_file" || call.Name == "create_file" || call.Name == "overwrite_file" || autoAppendDone {
						unlatchAppendOnly(appendOnlyAfterPartialWrite, f)
						markWriteSucceeded(successfulFullWrites, f)
					}
					// A successful non-partial append lands real progress. Mark the
					// path so write_file restarts thrash-cancel early — but keep
					// append_file available. Partial salvage already continued above.
					if call.Name == "append_file" || recoveryTool == "append_file" {
						markWriteSucceeded(successfulFullWrites, f)
					}
				}
				// Partial saves need an immediate continue-append nudge so the
				// model never regenerates the prefix or jumps to the next step.
				if partialRecovery {
					for _, f := range toolResult.ChangedFiles {
						saved := readWorkspaceFile(options.Cwd, f)
						if saved == "" {
							continue
						}
						pendingPartialContinuations = append(pendingPartialContinuations,
							partialFileContinuationNudge(f, saved))
					}
				}
				// A concrete write of the file explicitly named by a plan step is
				// stronger evidence than the executor's narration. Advance the
				// interactive roadmap immediately instead of leaving a completed
				// first step stuck pending until the model emits update_plan again.
				// Partial recoveries do not complete plan steps.
				if !partialRecovery {
					if items, changed := guards.completeWrittenFileSteps(); changed && options.OnPlanProgress != nil {
						options.OnPlanProgress(items)
					}
				}
				if options.SelfCorrect != nil && !partialRecovery {
					changedFilesThisBatch = append(changedFilesThisBatch, toolResult.ChangedFiles...)
				}
				// Real disk progress always counts — not only plan-seeded runs.
				// (Old gate required planSeeded and left non-plan multi-file work
				// looking like "no progress" after successful writes.)
				if !validationReworkPending && isMutatingCall {
					wroteFileThisBatch = true
				}
			} else if isMutatingCall && toolResult.Meta != nil && toolResult.Meta["rewrite_skipped"] == "true" {
				// already flagged thrashSkipThisBatch
			} else if isMutatingCall && toolResult.Meta != nil && toolResult.Meta["empty_append_guard"] == "true" {
				// already flagged emptyMutationThisBatch
			} else if isMutatingCall && (toolResult.Status == tools.StatusError || call.Aborted) {
				// Aborted/error mutation with no ChangedFiles = real failed attempt.
				// (Thrash skips set StatusOK and are excluded above.)
				if toolResult.Meta == nil || toolResult.Meta["rewrite_skipped"] != "true" {
					hardMutationFailThisBatch = true
				}
			}
		}
		toolCallsSinceValidation += executedTool
		// A successful promotion (normally the planner-directed mv command)
		// removes the staging sibling. Forget the latch immediately so completion
		// can proceed; if it still exists, the final-answer gate below keeps the
		// executor on the safe promotion path instead of accepting a half-write.
		for key, rewrite := range stagedRewritePaths {
			if !workspaceFileExists(options.Cwd, rewrite.stage) {
				delete(stagedRewritePaths, key)
			}
		}

		// No-progress circuit breaker (perf.log turns 12–20): consecutive model
		// turns that attempted file mutations but landed zero bytes (empty append,
		// aborted salvage fail) burned multi-minute re-prefills.
		// Rewrite thrash skips are NOT "no progress" — they are intentional
		// redirects to the next NEW path; counting them toward hard-stop killed
		// multi-file runs after the first deliverable landed.
		attemptedMutating := false
		for _, call := range collected.ToolCalls {
			switch call.Name {
			case "write_file", "append_file", "edit_file", "create_file", "overwrite_file", "apply_patch":
				attemptedMutating = true
			}
		}
		if wroteFileThisBatch || len(pendingPartialContinuations) > 0 {
			noProgressStreak = 0
			thrashSkipStreak = 0
		} else if thrashSkipThisBatch && !hardMutationFailThisBatch && !emptyMutationThisBatch {
			// Pure rewrite thrash: steer to the next NEW file; do not burn the
			// no-progress budget that ends the run.
			noProgressStreak = 0
			thrashSkipStreak++
			pendingStep := guards.firstPendingPlanStep()
			pendingFile := guards.firstPendingPlanFile()
			finished := pendingStep == "" && pendingFile == ""
			if thrashSkipStreak >= hardStopThrashSkipStreak && finished {
				result.Incomplete = false
				result.FinalAnswer = "Stopped rewrite thrash after " + strconv.Itoa(thrashSkipStreak) +
					" attempts to regenerate files already on disk. Existing partial/full writes were preserved."
				result.Messages = copyMessages(messages)
				return result, nil
			}
			if thrashSkipStreak >= hardStopThrashSkipStreak && !finished {
				result.Incomplete = true
				result.IncompleteReason = "rewrite thrash blocked multi-file progress"
				resume := pendingFile
				if resume == "" {
					resume = pendingStep
				}
				result.FinalAnswer = "Stopped after " + strconv.Itoa(thrashSkipStreak) +
					" rewrite attempts of already-written files while later plan work remained. " +
					"On-disk files were preserved. Resume by implementing the next path with write_file " +
					"(first ≤100 lines) then append_file — do not regenerate finished files. Next: " + resume
				result.Messages = copyMessages(messages)
				return result, nil
			}
			messages = append(messages, sonutruntime.Message{
				Role:    sonutruntime.MessageRoleUser,
				Content: nextFileProgressNudge(pendingStep, pendingFile, finished),
			})
		} else if attemptedMutating && (hardMutationFailThisBatch || emptyMutationThisBatch || thrashSkipThisBatch) {
			// Real failed mutations (and mixed thrash+fail turns) count toward stop.
			noProgressStreak++
			thrashSkipStreak = 0
			if noProgressStreak >= hardStopNoProgressStreak {
				result.Incomplete = true
				result.IncompleteReason = "no productive file progress after repeated write attempts"
				pending := guards.pendingPlanSummary(3)
				result.FinalAnswer = "Stopped after " + strconv.Itoa(noProgressStreak) +
					" consecutive turns with no file progress. Any partial content already on disk was preserved. " +
					"Resume by asking me to finish incomplete files with append_file (do not regenerate from scratch)."
				if pending != "" {
					result.FinalAnswer += " Remaining plan: " + pending
				}
				if next := guards.firstPendingPlanFile(); next != "" {
					result.FinalAnswer += " Next file: " + next
				}
				result.Messages = copyMessages(messages)
				return result, nil
			}
			if noProgressStreak >= maxNoProgressStreak {
				pendingStep := guards.firstPendingPlanStep()
				pendingFile := guards.firstPendingPlanFile()
				nudge := "HARD STOP: " + strconv.Itoa(noProgressStreak) +
					" mutating turns with no bytes landed. Do NOT re-think or re-read. " +
					nextFileProgressNudge(pendingStep, pendingFile, pendingStep == "" && pendingFile == "")
				messages = append(messages, sonutruntime.Message{
					Role:    sonutruntime.MessageRoleUser,
					Content: nudge,
				})
			}
		} else if attemptedMutating {
			// Mutation attempted but classified as neither thrash nor hard fail
			// (e.g. aborted before path known). Still count carefully.
			noProgressStreak++
			if noProgressStreak >= hardStopNoProgressStreak {
				result.Incomplete = true
				result.IncompleteReason = "no productive file progress after repeated write attempts"
				result.FinalAnswer = "Stopped after " + strconv.Itoa(noProgressStreak) +
					" consecutive turns with no file progress. Any partial content already on disk was preserved. " +
					"Resume by asking me to finish incomplete files with append_file (do not regenerate from scratch)."
				if next := guards.firstPendingPlanFile(); next != "" {
					result.FinalAnswer += " Next file: " + next
				}
				result.Messages = copyMessages(messages)
				return result, nil
			}
			if noProgressStreak >= maxNoProgressStreak {
				messages = append(messages, sonutruntime.Message{
					Role: sonutruntime.MessageRoleUser,
					Content: "HARD STOP: " + strconv.Itoa(noProgressStreak) +
						" mutating turns with no bytes landed. " +
						nextFileProgressNudge(guards.firstPendingPlanStep(), guards.firstPendingPlanFile(), false),
				})
			}
		} else {
			// Non-mutating turns (reads, plan updates) are not write thrash.
			// Keep thrashSkipStreak so consecutive rewrite-only turns still trip,
			// but clear no-progress (reads between fails should not free-ride).
			noProgressStreak = 0
		}

		// Newly discovered directory guidelines (intent layer), appended after
		// every tool_result so the assistant's tool_results stay contiguous.
		for _, note := range intentNotesThisBatch {
			messages = append(messages, sonutruntime.Message{
				Role:    sonutruntime.MessageRoleUser,
				Content: note,
			})
		}
		if wroteFileThisBatch {
			// Live mario session: successful write → re-read same file → full
			// rewrite for 8+ minutes. Ban rewrite thrash, but ALLOW write_file
			// for the next NEW path (game.js after index.html) — the old nudge
			// said "edit_file/append_file only" and multi-file runs never started
			// the second deliverable.
			pendingStep := guards.firstPendingPlanStep()
			pendingFile := guards.firstPendingPlanFile()
			messages = append(messages, sonutruntime.Message{
				Role: sonutruntime.MessageRoleUser,
				Content: "File is on disk. " + nextFileProgressNudge(pendingStep, pendingFile,
					pendingStep == "" && pendingFile == "") +
					" The planner already supplied repository context; do not read or re-plan—act on the next step.",
			})
		}
		if duplicateReadSuppressed > 0 && executedTool == 0 {
			messages = append(messages, sonutruntime.Message{
				Role: sonutruntime.MessageRoleUser,
				Content: "A re-read of the same unchanged path was suppressed. Reuse that file's contents. " +
					"You may still read_file DIFFERENT paths to understand the codebase, " +
					"then implement with write_file, append_file, or edit_file.",
			})
		}

		// Verbose pre-tool narration clamp: a long paragraph before a tool call
		// is pure latency on a local model. Appended after every tool_result so
		// the assistant's tool_results stay contiguous.
		if nudge := guards.checkVerboseNarration(collected.Text, len(collected.ToolCalls)); nudge != "" {
			messages = append(messages, sonutruntime.Message{
				Role:    sonutruntime.MessageRoleUser,
				Content: nudge,
			})
		}

		// Repeated runaway-argument aborts: each one costs minutes of dead
		// generation, so from the second consecutive abort force a strategy
		// change instead of letting the model re-issue the same giant call.
		// Third identical read-only call: the result is already in context, so
		// stop the model from re-fetching what it already has.
		if nudge := guards.takeRepeatNudge(); nudge != "" {
			messages = append(messages, sonutruntime.Message{
				Role:    sonutruntime.MessageRoleUser,
				Content: nudge,
			})
		}
		// Frontend-verify nudge: a frontend file was edited, but no successful
		// browser call followed — remind the model to open/reload and re-verify.
		if nudge := guards.checkBrowserVerifyNudge(); nudge != "" {
			messages = append(messages, sonutruntime.Message{
				Role:    sonutruntime.MessageRoleUser,
				Content: nudge,
			})
		}
		// Repeated sandbox/network denials: a policy block never yields to a
		// retried command variant, so redirect to web_fetch or the user.
		if nudge := guards.checkSandboxBlockStreak(); nudge != "" {
			messages = append(messages, sonutruntime.Message{
				Role:    sonutruntime.MessageRoleUser,
				Content: nudge,
			})
		}
		if nudge := guards.checkArgAbortStreak(); nudge != "" {
			messages = append(messages, sonutruntime.Message{
				Role:    sonutruntime.MessageRoleUser,
				Content: nudge,
			})
		}
		// Partial-write salvage: force append continuation with the file tail so
		// generated content is never regenerated and the next plan step waits.
		for _, nudge := range pendingPartialContinuations {
			if strings.TrimSpace(nudge) == "" {
				continue
			}
			messages = append(messages, sonutruntime.Message{
				Role:    sonutruntime.MessageRoleUser,
				Content: nudge,
			})
		}
		pendingPartialContinuations = pendingPartialContinuations[:0]

		// Run post-edit self-correction once over the union of files this turn
		// changed, then append any feedback after every tool_result is recorded so
		// the assistant's tool_results stay contiguous (a user message between
		// tool_results breaks strict provider replay). nil SelfCorrect is a no-op.
		if options.SelfCorrect != nil && len(changedFilesThisBatch) > 0 {
			if feedback, _ := options.SelfCorrect.AfterEdit(ctx, dedupeStrings(changedFilesThisBatch)); feedback != "" {
				messages = append(messages, sonutruntime.Message{
					Role:    sonutruntime.MessageRoleUser,
					Content: feedback,
				})
			}
		}

		// Mid-run model escalation: if a tool asked to switch models this turn and
		// a switcher is wired, rebuild the provider on the new model for the rest
		// of the run. At most one switch per turn (first request wins). A switcher
		// error is NON-FATAL — record a brief note and continue on the current
		// model. nil switcher ⇒ requests are ignored entirely (escalation off).
		if turnRequestedModel != "" && options.ModelSwitcher != nil {
			newProvider, switchErr := options.ModelSwitcher(ctx, turnRequestedModel)
			if switchErr != nil {
				messages = append(messages, sonutruntime.Message{
					Role:    sonutruntime.MessageRoleUser,
					Content: escalationFailedNoticePrefix + " (" + turnRequestedModel + "): " + switchErr.Error() + ". Continuing on " + options.Model + ".",
				})
			} else if newProvider != nil {
				// Reassign the local provider so the next turn's StreamCompletion and
				// compaction use it; update options.Model so subsequent RunOptions.Model,
				// context-window sizing, and usage attribution follow the new model.
				provider = newProvider
				options.Model = turnRequestedModel
				// KNOWN LIMITATION (deferred): the compactor's context-window budget
				// is fixed at run start from options.ContextWindow and is NOT updated
				// here, so a switch to a model with a different window keeps compacting
				// against the original budget. Fixing it needs a ModelSwitcher contract
				// change (return the new window) — out of scope for this change.
			}
		}

		// A turn can mix valid tool calls with a dropped (nameless) one. The valid
		// calls executed above; surface the dropped call too so it is never
		// silently ignored just because the turn also did real work. This is
		// independent of (and additive to) the failure-hint / plan-reminder nudges.
		if collected.DroppedToolCalls > 0 {
			messages = append(messages, sonutruntime.Message{
				Role:    sonutruntime.MessageRoleUser,
				Content: droppedToolCallNotice,
			})
		}

		// Exploration nudge (soft, one-shot): after many consecutive read-only
		// turns, suggest also landing an edit. Never bans further read_file —
		// understanding the codebase is allowed and expected.
		if consecutiveReadOnly >= explorationNudgeAt && !explorationNudgeSent {
			explorationNudgeSent = true
			exploreMsg := "You have explored for several turns without a file edit. " +
				"Keep reading any files you still need, and also start implementing soon " +
				"(write_file / edit_file / append_file), or tell the user what is blocking you."
			if planSeeded {
				pendingStep := guards.firstPendingPlanStep()
				pendingFile := guards.firstPendingPlanFile()
				exploreMsg = "Grounding is complete and plan is seeded. DO NOT continue calling read_file or inspection tools. " +
					nextFileProgressNudge(pendingStep, pendingFile, pendingStep == "" && pendingFile == "")
			}
			messages = append(messages, sonutruntime.Message{
				Role:    sonutruntime.MessageRoleUser,
				Content: exploreMsg,
			})
		}

		// A repeated-failure hint (schema + exact error) takes priority over the
		// planning reminders — fixing the failing call matters more than plan
		// hygiene. Both are light, one-shot, user-role nudges.
		if unverified := guards.pendingUnverifiedCompletion; unverified != "" {
			guards.pendingUnverifiedCompletion = ""
			messages = append(messages, sonutruntime.Message{
				Role:    sonutruntime.MessageRoleUser,
				Content: unverified,
			})
		} else if failureHint != "" {
			messages = append(messages, sonutruntime.Message{
				Role:    sonutruntime.MessageRoleUser,
				Content: failureHint,
			})
		} else if reminder := guards.progressReminder(); reminder != "" {
			messages = append(messages, sonutruntime.Message{
				Role:    sonutruntime.MessageRoleUser,
				Content: reminder,
			})
		} else if !planSeeded {
			if reminder := guards.planReminder(result.Turns); reminder != "" {
				messages = append(messages, sonutruntime.Message{
					Role:    sonutruntime.MessageRoleUser,
					Content: reminder,
				})
			}
		}
	}

	if ctx.Err() != nil {
		result.Messages = copyMessages(messages)
		return result, ctx.Err()
	}
	// Reaching here means the loop hit the maxTurns ceiling — the agent was cut off
	// mid-run, not a natural completion (a finished run returns via the no-tool-call
	// success path before this). Under the headless completion gate that is INCOMPLETE,
	// not success, so a run that loops to the turn limit isn't reported as done.
	if answer, finalMessages, finishReason := finalAnswerAfterMaxTurns(ctx, provider, messages, options); strings.TrimSpace(answer) != "" {
		result.FinalAnswer = answer
		result.FinishReason = finishReason
		result.Messages = copyMessages(finalMessages)
		if options.RequireCompletionSignal {
			result.Incomplete = true
			result.IncompleteReason = "reached the max-turns limit without completing"
		}
		return result, nil
	}

	result.FinalAnswer = maxTurnsAnswer
	result.Messages = copyMessages(messages)
	if options.RequireCompletionSignal {
		result.Incomplete = true
		result.IncompleteReason = "reached the max-turns limit without a final answer"
	}
	return result, nil
}

// playwrightVerificationToolNames is the full verification surface. These stay
// loadable via tool_search; only the cold-path minimal set is ever force-eager.
var playwrightVerificationToolNames = []string{
	"mcp_playwright_browser_navigate",
	"mcp_playwright_browser_console_messages",
	"mcp_playwright_browser_snapshot",
	"mcp_playwright_browser_click",
	"mcp_playwright_browser_type",
	"mcp_playwright_browser_press_key",
	"mcp_playwright_browser_evaluate",
}

// playwrightColdPathToolNames is the MINIMUM schema set needed to open a page
// and confirm it rendered. Measured 2026-07-30: preloading the full 7-tool
// Playwright set on every "build a game/page" turn pushed tools_exposed from
// ~12 → 20 and added ~1–2k tokens of schema tax to every hybrid re-prefill
// (≈30–40 s of "↑ 0 tok" per turn at ~60 tok/s). Interaction tools
// (click/type/press/evaluate/console) stay deferred until the model loads them
// via tool_search or until after the first successful file mutation.
var playwrightColdPathToolNames = []string{
	"mcp_playwright_browser_navigate",
	"mcp_playwright_browser_snapshot",
}

// preloadBrowserToolsForUITask exposes a MINIMAL browser surface for UI tasks.
// Full interaction tools remain deferred (tool_search) until the verify phase
// (see maybeExpandBrowserToolsAfterWrite). Non-UI tasks pay zero schema tax.
func preloadBrowserToolsForUITask(loaded map[string]bool, registry *tools.Registry, task string) {
	if loaded == nil || registry == nil || !looksLikeUIStep(task) {
		return
	}
	for _, name := range playwrightColdPathToolNames {
		if _, ok := registry.Get(name); ok {
			loaded[name] = true
		}
	}
}

// maybeExpandBrowserToolsAfterWrite adds the remaining Playwright interaction
// tools once a file mutation has landed. UI verify needs click/type, but
// building (write_file/edit_file) must not pay their schema cost every turn.
func maybeExpandBrowserToolsAfterWrite(loaded map[string]bool, registry *tools.Registry, task string, fileMutated bool) {
	if !fileMutated || loaded == nil || registry == nil || !looksLikeUIStep(task) {
		return
	}
	for _, name := range playwrightVerificationToolNames {
		if _, ok := registry.Get(name); ok {
			loaded[name] = true
		}
	}
}

func hasUsableBrowserVerificationTool(registry *tools.Registry, permissionMode PermissionMode, options Options) bool {
	if registry == nil {
		return false
	}
	for _, name := range playwrightVerificationToolNames {
		tool, ok := registry.Get(name)
		if ok && ToolVisible(tool, permissionMode, options.EnabledTools, options.DisabledTools) {
			return true
		}
	}
	return false
}

func withoutPlanTool(definitions []sonutruntime.ToolDefinition) []sonutruntime.ToolDefinition {
	filtered := make([]sonutruntime.ToolDefinition, 0, len(definitions))
	for _, definition := range definitions {
		if definition.Name != "update_plan" {
			filtered = append(filtered, definition)
		}
	}
	return filtered
}

func withoutTools(definitions []sonutruntime.ToolDefinition, names ...string) []sonutruntime.ToolDefinition {
	blocked := make(map[string]bool, len(names))
	for _, name := range names {
		blocked[name] = true
	}
	filtered := make([]sonutruntime.ToolDefinition, 0, len(definitions))
	for _, definition := range definitions {
		if !blocked[definition.Name] {
			filtered = append(filtered, definition)
		}
	}
	return filtered
}

func withoutDelegationTools(definitions []sonutruntime.ToolDefinition) []sonutruntime.ToolDefinition {
	filtered := definitions[:0]
	for _, definition := range definitions {
		name := strings.ToLower(strings.TrimSpace(definition.Name))
		if name == "task" || name == "specialist" || strings.HasPrefix(name, "swarm_") || strings.HasPrefix(name, "specialist_") {
			continue
		}
		filtered = append(filtered, definition)
	}
	return filtered
}

func hasToolDefinition(definitions []sonutruntime.ToolDefinition, name string) bool {
	for _, definition := range definitions {
		if definition.Name == name {
			return true
		}
	}
	return false
}

// prefersFullRewrite is deliberately conservative. It only changes the tool
// menu after a planner has produced a builder roadmap, and only when the task
// clearly describes replacing a whole frontend file. This prevents the model
// from spending minutes streaming a full replacement through edit_file.
func prefersFullRewrite(prompt, plannerPrompt, plan string) bool {
	text := strings.ToLower(prompt + "\n" + plannerPrompt + "\n" + plan)
	for _, marker := range []string{
		"full rewrite", "entire file", "whole file", "replace the entire", "rewrite the file",
		"complete html document", "full html document", "redesign", "recreate", "single html", "single-file", "single file",
	} {
		if strings.Contains(text, marker) {
			return true
		}
	}
	return strings.Contains(text, "make our code") && strings.Contains(text, "look like")
}

func invalidFrontendCommand(call ToolCall, prompt string, options Options, plan string) string {
	if call.Name != "exec_command" && call.Name != "bash" {
		return ""
	}
	text := strings.ToLower(prompt + "\n" + options.PlannerPrompt + "\n" + plan)
	var args map[string]any
	if decodeToolArguments(call.Arguments, &args) != nil {
		return ""
	}
	command := ""
	for _, key := range []string{"command", "cmd", "script"} {
		if value, ok := args[key].(string); ok {
			command = value
			break
		}
	}
	lower := strings.ToLower(strings.TrimSpace(command))
	// A local HTML game is a frontend task even when the user used shorthand
	// such as "fix the game" and never wrote "HTML" or "browser". Without
	// this target-aware check, `open game.html` reaches the shell/browser-open
	// interception and returns a misleading success, which commonly causes the
	// model to issue the exact same validation command again.
	htmlOpen := (strings.Contains(lower, ".html") || strings.Contains(lower, ".htm")) &&
		containsBrowserOpenCommand(lower)
	frontend := false
	for _, marker := range []string{"html", ".css", "frontend", "website", "web app", "canvas", "browser", "screenshot"} {
		if strings.Contains(text, marker) {
			frontend = true
			break
		}
	}
	if !frontend && strings.Contains(text, "game") {
		frontend = strings.Contains(text, "html") || strings.Contains(text, "canvas") || strings.Contains(text, "browser")
	}
	if !frontend && !htmlOpen {
		return ""
	}
	if strings.Contains(lower, "from game import") || strings.Contains(lower, "import game") ||
		(strings.Contains(lower, ".html") && (strings.Contains(lower, "python") || htmlOpen)) {
		return "Skipped an invalid frontend validation command. Use the headful browser/Playwright tools to open the HTML, inspect it, and interact with it; do not validate a browser game with a shell GUI opener or Python imports."
	}
	return ""
}

func containsBrowserOpenCommand(command string) bool {
	for _, opener := range []string{"open ", "xdg-open ", "sensible-browser ", "x-www-browser ", "gnome-open ", "start "} {
		if strings.HasPrefix(command, opener) || strings.Contains(command, "&& "+opener) ||
			strings.Contains(command, "; "+opener) || strings.Contains(command, "| "+opener) {
			return true
		}
	}
	return false
}

func emptyAppendFileNotice(call ToolCall) string {
	if call.Name != "append_file" {
		return ""
	}
	var args map[string]any
	if decodeToolArguments(call.Arguments, &args) != nil {
		return ""
	}
	content := ""
	for _, key := range []string{"content", "contents", "text", "body", "data", "file_content"} {
		if value, ok := args[key].(string); ok {
			content = value
			break
		}
	}
	if strings.TrimSpace(content) != "" {
		return ""
	}
	return "Skipped an empty append_file call; no file was changed. Continue with the next concrete action, or append only the remaining non-empty content."
}

func finalAnswerAfterMaxTurns(ctx context.Context, provider Provider, messages []sonutruntime.Message, options Options) (string, []sonutruntime.Message, string) {
	finalMessages := copyMessages(messages)
	finalMessages = append(finalMessages, sonutruntime.Message{
		Role:    sonutruntime.MessageRoleUser,
		Content: maxTurnsFinalAnswerPrompt,
	})
	// The max-turns final-answer call is a pre-content connect, often after a long
	// autonomous/cron run — route it through the reconnect helper so a single
	// transient hiccup doesn't drop the final summary (AUDIT-L1).
	stream, err := streamWithReconnect(ctx, provider, sonutruntime.CompletionRequest{
		Messages:        copyMessages(finalMessages),
		ReasoningEffort: options.ReasoningEffort,
	}, reconnectNoticeFor(options))
	if err != nil {
		return "", messages, ""
	}
	collected := sonutruntime.CollectStreamWithOptions(ctx, stream, sonutruntime.CollectOptions{
		OnText:          options.OnText,
		OnReasoning:     options.OnReasoning,
		OnUsage:         options.OnUsage,
		OnToolCallStart: options.OnToolCallStart,
		OnToolCallDelta: options.OnToolCallDelta,
	})
	if ctx.Err() != nil || collected.Error != "" || strings.TrimSpace(collected.Text) == "" {
		return "", messages, ""
	}
	finalMessages = append(finalMessages, sonutruntime.Message{
		Role:    sonutruntime.MessageRoleAssistant,
		Content: collected.Text,
	})
	return collected.Text, finalMessages, collected.FinishReason
}

func historySafeToolCalls(calls []ToolCall) []ToolCall {
	if len(calls) == 0 {
		return nil
	}
	safe := make([]ToolCall, len(calls))
	for i, call := range calls {
		safe[i] = call
		args := strings.TrimSpace(call.Arguments)
		switch {
		case args == "":
			safe[i].Arguments = "{}"
		case json.Valid([]byte(args)):
			// already a single valid JSON value — keep as-is
		default:
			// Recover the first object for the concatenated case so the replayed
			// transcript matches what executeToolCall actually ran; otherwise (real
			// corruption) fall back to an empty object.
			if first, ok := recoverableToolArguments(args); ok {
				safe[i].Arguments = first
			} else {
				safe[i].Arguments = "{}"
			}
		}
	}
	return safe
}

// toolSchemaJSON renders a tool's parameter schema as readable JSON for the
// repeated-failure corrective hint, so the model can see exactly what arguments
// the tool expects. Returns "{}" if the tool or schema is unavailable.
func toolSchemaJSON(registry *tools.Registry, name string) string {
	tool, ok := registry.Get(name)
	if !ok {
		return "{}"
	}
	data, err := json.MarshalIndent(schemaToRuntimeMap(tool.Parameters()), "", "  ")
	if err != nil {
		return "{}"
	}
	return string(data)
}

// executeToolCall runs one tool call and returns its result. The second return
// is a non-nil ABORT error only when the call demands the whole run stop (a
// canceled/timed-out ask_user prompt) rather than continuing — every ordinary
// success or tool error returns a nil abort error.
// recoverableToolArguments inspects a tool call's raw JSON arguments. It returns
// the raw text of the FIRST JSON value when the payload is one value optionally
// followed by additional WHOLE JSON values (the weak-model concatenated case, e.g.
// `{A}{B}`). ok is false when the payload is genuinely malformed: a bad/truncated
// first value, OR trailing NON-JSON garbage after a valid first value (e.g.
// `{"x":1}xyz`) — that case must still error, not be silently accepted. Sharing
// this between dispatch and replay-history keeps them consistent: only a cleanly
// recoverable first object is ever used.
func recoverableToolArguments(arguments string) (first string, ok bool) {
	dec := json.NewDecoder(strings.NewReader(arguments))
	var head json.RawMessage
	if err := dec.Decode(&head); err != nil {
		return "", false
	}
	// The remainder must be only whole JSON values (or nothing); any decode error
	// other than EOF means trailing garbage — reject so corruption still surfaces.
	for {
		var rest json.RawMessage
		if err := dec.Decode(&rest); err != nil {
			if errors.Is(err, io.EOF) {
				return strings.TrimSpace(string(head)), true
			}
			return "", false
		}
	}
}

// decodeToolArguments decodes a tool call's JSON arguments into v. It tolerates a
// weaker model that packs MULTIPLE concatenated top-level JSON objects into one
// arguments string (`{A}{B}`) — the cause of "invalid character '{' after top-level
// value" failures with small models like minimax-m3 — by decoding the FIRST object
// and ignoring the trailing WHOLE JSON values, so the primary intended call runs.
// A genuinely malformed payload (bad/truncated first object, or non-JSON trailing
// garbage) still returns the real parse error; a standard single-object payload
// decodes exactly as before.
func decodeToolArguments(arguments string, v any) error {
	if first, ok := recoverableToolArguments(arguments); ok {
		return json.Unmarshal([]byte(first), v)
	}
	// Not cleanly recoverable by splitting — try jsonrepair before failing.
	// Small local models emit near-JSON (unescaped newlines in strings, missing
	// closing brace after truncation, single quotes, trailing commas); repairing
	// and running the call beats bouncing a parse error back for another round.
	if err := json.Unmarshal([]byte(arguments), v); err != nil {
		repaired, rerr := jsonrepair.Repair(arguments)
		if rerr != nil {
			return err // repair failed; surface the original parse error
		}
		if uerr := json.Unmarshal([]byte(repaired), v); uerr != nil {
			return err
		}
	}
	return nil
}

func executeToolCall(ctx context.Context, registry *tools.Registry, call ToolCall, permissionMode PermissionMode, options Options) (ToolResult, error) {
	args := map[string]any{}
	if call.Arguments != "" {
		if err := decodeToolArguments(call.Arguments, &args); err != nil {
			return ToolResult{
				ToolCallID: call.ID,
				Name:       call.Name,
				Status:     tools.StatusError,
				Output:     "Error: Failed to parse arguments for " + call.Name + ": " + err.Error(),
			}, nil
		}
	}
	// tool_search is the gateway to the allowlisted deferred tools, so a non-empty
	// EnabledTools allowlist that omits it must NOT reject the call — otherwise the
	// discovery tool exposed to the model rejects at dispatch time (an inescapable
	// dead-end). The allowlist is exempted; an explicit DisabledTools entry for
	// tool_search is still honored (only the allowlist is exempted, not the denylist).
	toolSearchAllowed := call.Name == tools.ToolSearchToolName && !containsToolName(options.DisabledTools, tools.ToolSearchToolName)
	if !toolSearchAllowed && !ToolAllowedByFilters(call.Name, options.EnabledTools, options.DisabledTools) {
		return ToolResult{
			ToolCallID:   call.ID,
			Name:         call.Name,
			Status:       tools.StatusError,
			Output:       `Error: Tool "` + call.Name + `" is not enabled for this run.`,
			DenialReason: DenialFiltered,
		}, nil
	}
	tool, toolFound := registry.Get(call.Name)
	if options.NetworkDisabled && toolFound && tool.Safety().SideEffect == tools.SideEffectNetwork {
		return networkDisabledResult(call), nil
	}
	if permissionMode == PermissionModeSpecDraft && toolFound && !ToolAdvertised(tool, permissionMode) {
		return ToolResult{
			ToolCallID:   call.ID,
			Name:         call.Name,
			Status:       tools.StatusError,
			Output:       `Error: Tool "` + call.Name + `" is not available in spec-draft mode.`,
			DenialReason: DenialFiltered,
		}, nil
	}
	if toolFound {
		if rejecter, ok := tool.(tools.PrePermissionRejecter); ok {
			if result, rejected := rejecter.RejectBeforePermission(args); rejected {
				return toolResultFromPrePermissionReject(call, result), nil
			}
		}
	}

	// ask_user is intercepted in the loop (like permissions) so the question can
	// be routed to an interactive front-end instead of blocking inside the tool.
	// When no front-end is wired up it degrades to the tool's own graceful Run().
	if call.Name == "ask_user" {
		return executeAskUser(ctx, registry, call, args, permissionMode, options)
	}
	if call.Name == tools.RequestPermissionsToolName {
		return executeRequestPermissions(ctx, call, args, permissionMode, options)
	}

	permissionGranted := permissionMode == PermissionModeAuto ||
		permissionMode == PermissionModeUnsafe ||
		permissionMode == PermissionModeBypass
	if toolFound && effectivePermission(tool, args) == tools.PermissionAllow {
		permissionGranted = true
	}

	decisionReason := ""
	var decisionAction PermissionDecisionAction
	var decisionCommandPrefix []string
	var permissionCleanups []func()
	defer func() {
		for i := len(permissionCleanups) - 1; i >= 0; i-- {
			if permissionCleanups[i] != nil {
				permissionCleanups[i]()
			}
		}
	}()
	if toolFound && !permissionGranted {
		if grant, ok, session := matchCommandPrefix(call.Name, args, options); ok {
			permissionGranted = true
			if session {
				decisionAction = PermissionDecisionAllowPrefix
				decisionReason = "session command prefix approval matched"
			} else {
				decisionAction = PermissionDecisionAlwaysAllowPrefix
				decisionReason = "persistent command prefix approval matched"
			}
			decisionCommandPrefix = grant.Prefix
		}
	}

	var preflightDecision *sandbox.Decision
	if toolFound && options.Sandbox != nil {
		decision := options.Sandbox.Evaluate(ctx, sandboxRequest(call.Name, tool, args, permissionGranted, permissionMode, options))
		preflightDecision = &decision
		// /network off is an intentional offline mode, not a tool failure. Shell
		// commands that would reach the network are normalized to a quiet no-op
		// here, before permission prompts or red sandbox errors can reach the TUI.
		if options.NetworkDisabled && sandbox.HasRiskCategory(decision.Risk, "network") {
			return networkDisabledResult(call), nil
		}
	}

	if toolFound && options.OnPermissionRequest != nil && shouldRequestPermissionForMode(tool, args, permissionGranted, permissionMode, preflightDecision) {
		requestEvent, ok := buildPermissionEvent(call, tool, args, permissionGranted, permissionMode, options, preflightDecision)
		if !ok {
			requestEvent = fallbackPermissionEvent(call, tool, args, permissionMode, options)
		}
		request := permissionRequestFromEvent(requestEvent, args, options)
		decision, err := requestPermission(ctx, request, options)
		if err != nil {
			decision = PermissionDecision{Action: PermissionDecisionDeny, Reason: err.Error()}
		}
		decision.Action = normalizePermissionDecisionAction(decision.Action)
		decisionReason = strings.TrimSpace(decision.Reason)
		decisionAction = decision.Action
		switch decision.Action {
		case PermissionDecisionAllow, PermissionDecisionAllowStrict:
			permissionGranted = true
			requestEvent.DecisionAction = decision.Action
			cleanup, err := grantNetworkForSandboxPrompt(requestEvent, sandbox.PermissionGrantScopeTurn, options)
			if err != nil {
				reason := "failed to grant network permission: " + err.Error()
				emitDeniedPermission(options, call, requestEvent, reason)
				return deniedPermissionResult(call, reason, requestEvent), nil
			}
			permissionCleanups = append(permissionCleanups, cleanup)
			cleanup, err = grantInlineAdditionalPermissions(args, sandbox.PermissionGrantScopeTurn, options)
			if err != nil {
				reason := "failed to grant additional permissions: " + err.Error()
				emitDeniedPermission(options, call, requestEvent, reason)
				return deniedPermissionResult(call, reason, requestEvent), nil
			}
			permissionCleanups = append(permissionCleanups, cleanup)
			cleanup, err = grantFilesystemForSandboxPrompt(requestEvent, sandbox.PermissionGrantScopeTurn, options)
			if err != nil {
				reason := "failed to grant filesystem permission: " + err.Error()
				emitDeniedPermission(options, call, requestEvent, reason)
				return deniedPermissionResult(call, reason, requestEvent), nil
			}
			permissionCleanups = append(permissionCleanups, cleanup)
		case PermissionDecisionAllowForSession:
			permissionGranted = true
			requestEvent.DecisionAction = decision.Action
			if shellCommandAdditionalPermissionsRequested(args) {
				cleanup, err := grantInlineAdditionalPermissions(args, sandbox.PermissionGrantScopeSession, options)
				if err != nil {
					reason := "failed to grant session additional permissions: " + err.Error()
					emitDeniedPermission(options, call, requestEvent, reason)
					return deniedPermissionResult(call, reason, requestEvent), nil
				}
				permissionCleanups = append(permissionCleanups, cleanup)
			} else if networkSandboxPrompt(requestEvent) {
				cleanup, err := grantNetworkForSandboxPrompt(requestEvent, sandbox.PermissionGrantScopeSession, options)
				if err != nil {
					reason := "failed to grant session network permission: " + err.Error()
					emitDeniedPermission(options, call, requestEvent, reason)
					return deniedPermissionResult(call, reason, requestEvent), nil
				}
				permissionCleanups = append(permissionCleanups, cleanup)
			} else if filesystemSandboxPrompt(requestEvent) {
				cleanup, err := grantFilesystemForSandboxPrompt(requestEvent, sandbox.PermissionGrantScopeSession, options)
				if err != nil {
					reason := "failed to grant session filesystem permission: " + err.Error()
					emitDeniedPermission(options, call, requestEvent, reason)
					return deniedPermissionResult(call, reason, requestEvent), nil
				}
				permissionCleanups = append(permissionCleanups, cleanup)
			} else if options.Sandbox != nil {
				// The current call stays allowed if recording the session grant
				// fails; the user is simply prompted again for a later match.
				if grant, err := persistSessionPermissionGrant(call.Name, args, decisionReason, options); err == nil {
					requestEvent.GrantMatched = true
					requestEvent.Grant = &grant
				}
			}
		case PermissionDecisionAllowPrefix:
			if len(request.CommandPrefix) == 0 {
				emitDeniedPermission(options, call, requestEvent, decisionReason)
				return deniedPermissionResult(call, decisionReason, requestEvent), nil
			}
			permissionGranted = true
			requestEvent.DecisionAction = decision.Action
			decisionCommandPrefix = append([]string(nil), request.CommandPrefix...)
			if options.Sandbox != nil && len(decisionCommandPrefix) > 0 {
				options.Sandbox.GrantCommandPrefixForSession(call.Name, decisionCommandPrefix)
			}
			cleanup, err := grantNetworkForSandboxPrompt(requestEvent, sandbox.PermissionGrantScopeTurn, options)
			if err != nil {
				reason := "failed to grant network permission: " + err.Error()
				emitDeniedPermission(options, call, requestEvent, reason)
				return deniedPermissionResult(call, reason, requestEvent), nil
			}
			permissionCleanups = append(permissionCleanups, cleanup)
		case PermissionDecisionAlwaysAllowPrefix:
			if len(request.CommandPrefix) == 0 {
				emitDeniedPermission(options, call, requestEvent, decisionReason)
				return deniedPermissionResult(call, decisionReason, requestEvent), nil
			}
			permissionGranted = true
			requestEvent.DecisionAction = decision.Action
			decisionCommandPrefix = append([]string(nil), request.CommandPrefix...)
			if options.Sandbox != nil && len(decisionCommandPrefix) > 0 {
				if grant, err := persistCommandPrefixGrant(call.Name, decisionCommandPrefix, decisionReason, options); err == nil {
					decisionCommandPrefix = append([]string(nil), grant.Prefix...)
				}
			}
			cleanup, err := grantNetworkForSandboxPrompt(requestEvent, sandbox.PermissionGrantScopeTurn, options)
			if err != nil {
				reason := "failed to grant network permission: " + err.Error()
				emitDeniedPermission(options, call, requestEvent, reason)
				return deniedPermissionResult(call, reason, requestEvent), nil
			}
			permissionCleanups = append(permissionCleanups, cleanup)
		case PermissionDecisionAlwaysAllow:
			permissionGranted = true
			requestEvent.DecisionAction = decision.Action
			// "Always allow" also persists a grant so FUTURE calls skip the prompt.
			// With no sandbox engine there is nowhere to persist it, and persistence
			// can also fail — neither must deny what the user explicitly allowed, so
			// honor the allow for THIS call regardless and just skip remembering the
			// grant (the user is re-prompted next time rather than denied now). This
			// call's permission event is built from the sandbox decision below
			// (buildPermissionEvent reads decision.GrantMatched/Grant), so the
			// persisted grant does not need to be recorded on requestEvent here.
			if options.Sandbox != nil {
				if grant, err := persistPermissionGrant(call.Name, args, decisionReason, options); err == nil {
					requestEvent.GrantMatched = true
					requestEvent.Grant = &grant
				}
			}
		case PermissionDecisionCancel:
			emitCanceledPermission(options, call, requestEvent, decisionReason)
			return canceledPermissionResult(call, decisionReason, requestEvent), fmt.Errorf("%w for %s", errPermissionApprovalCanceled, call.Name)
		default:
			emitDeniedPermission(options, call, requestEvent, decisionReason)
			return deniedPermissionResult(call, decisionReason, requestEvent), nil
		}
	}

	// beforeTool hooks may veto the call before it runs (a non-zero exit blocks).
	if toolFound {
		if outcome, blocked := dispatchBeforeTool(ctx, options, call, args); blocked {
			return blockedByHookResult(call, outcome), nil
		}
	}
	args = shellExecutionArgsForApproval(call.Name, args, decisionAction, options)

	// Task tool: wire progress callback so the TUI sees live tool-call events
	// from the specialist child process.
	var progressCallback func(streamjson.Event)
	if call.Name == "Task" && options.OnToolProgress != nil {
		toolCallID := call.ID
		onProgress := options.OnToolProgress
		progressCallback = func(event streamjson.Event) {
			onProgress(toolCallID, event)
		}
	}

	var reportProgress func(tools.Progress)
	if options.OnToolProgress != nil {
		reporter := tools.NewProgressReporter(call.ID, func(progress tools.Progress) {
			current, total := progress.Current, progress.Total
			options.OnToolProgress(call.ID, streamjson.Event{
				Type: streamjson.EventToolProgress, ID: call.ID, Name: call.Name,
				Lifecycle: string(tools.LifecycleRunning), Phase: progress.Phase, Message: progress.Message,
				Sequence: progress.Sequence, Current: current, Total: total, Unit: progress.Unit,
				OutputDelta: progress.OutputDelta, ElapsedMillis: progress.Elapsed.Milliseconds(), RetryAttempt: progress.RetryAttempt,
			})
		})
		reportProgress = reporter.Report
	}
	var streamingCommit func() tools.Result
	if options.streamingCommits != nil {
		streamingCommit = options.streamingCommits[call.ID]
	}
	result := registry.RunWithOptions(ctx, call.Name, args, tools.RunOptions{
		PermissionGranted: permissionGranted,
		PermissionMode:    string(permissionMode),
		Autonomy:          options.Autonomy,
		Sandbox:           options.Sandbox,
		ToolCallID:        call.ID,
		SessionID:         options.SessionID,
		Model:             options.Model,
		ReasoningEffort:   options.ReasoningEffort,
		Depth:             options.Depth,
		Cwd:               options.Cwd,
		// Per-session file version tracker so write_file/edit_file refuse to clobber
		// a file that changed on disk outside @code since it was last read.
		FileTracker: options.FileTracker,
		// Interactive yes/no confirmations (e.g. write_file overwrite prompts).
		OnConfirmation: confirmationWithTimeout(options.OnConfirmation),
		// Forward the run's operator tool filters so a filter-aware tool
		// (tool_search) never discloses or loads an operator-hidden deferred tool.
		EnabledTools:      options.EnabledTools,
		DisabledTools:     options.DisabledTools,
		Progress:          progressCallback,
		ReportProgress:    reportProgress,
		PrecomputedCommit: streamingCommit,
		// The sandbox decision (if any) is returned synchronously on the Result and
		// used here for permission event building.
	})
	if streamingCommit != nil && options.streamingCommits != nil {
		delete(options.streamingCommits, call.ID)
	}
	if retryResult, directResult, retried, action, reason, prefix, abortErr := maybeRetryUnsandboxedAfterSandboxRestriction(ctx, registry, call, tool, args, result, permissionMode, options, progressCallback); retried || directResult != nil || abortErr != nil {
		if directResult != nil {
			return *directResult, abortErr
		}
		result = retryResult
		permissionGranted = true
		decisionAction = action
		decisionReason = reason
		if len(prefix) > 0 {
			decisionCommandPrefix = append([]string(nil), prefix...)
		}
	}
	sandboxDecision := result.SandboxDecision
	if toolFound && options.OnPermission != nil {
		if event, ok := buildPermissionEvent(call, tool, args, permissionGranted, permissionMode, options, sandboxDecision); ok {
			if permissionMode == PermissionModeAuto && decisionAction == "" &&
				sandboxDecision != nil && sandboxDecision.AutoAllowed {
				// The sandbox allowed this call by policy; it was not a
				// user-granted approval even though auto mode pre-authorizes the
				// dispatch path.
				event.PermissionGranted = false
			}
			event.DecisionReason = decisionReason
			if decisionAction != "" {
				event.DecisionAction = decisionAction
			}
			if len(decisionCommandPrefix) > 0 {
				event.CommandPrefix = append([]string(nil), decisionCommandPrefix...)
			}
			options.OnPermission(event)
		}
	}
	// afterTool hooks run once the tool has executed; their output (e.g. a
	// formatter or vet result) is surfaced back to the model on the result.
	if toolFound {
		if feedback := dispatchAfterTool(ctx, options, call, args, result); feedback != "" {
			var didRedact bool
			result.Output, didRedact = appendHookFeedback(result.Output, feedback)
			if didRedact {
				result.Redacted = true
			}
		}
	}
	// Secret scrubbing happens at the registry boundary (the single point both
	// the agent loop and the MCP server pass through), so result.Output is
	// already redacted here and result.Redacted reflects whether it changed.
	return ToolResult{
		ToolCallID:   call.ID,
		Name:         call.Name,
		Status:       result.Status,
		Lifecycle:    result.Lifecycle,
		Error:        result.Error,
		Output:       result.Output,
		Meta:         result.Meta,
		Redacted:     result.Redacted,
		ChangedFiles: result.ChangedFiles,
		Images:       sonutruntime.CloneImageBlocks(result.Images),
		Display:      result.Display,
		LoadedTools:  loadedToolsFromResult(result.Meta),
		// A tool may signal a mid-run model escalation by carrying the target id
		// in Meta["escalate_to_model"]. Lift it into the typed loop-level field;
		// the Run turn loop performs the actual provider switch. Empty for every
		// ordinary tool result.
		RequestedModel: result.Meta["escalate_to_model"],
	}, nil
}

// normalizeToolResultLifecycle covers synthetic loop results (argument
// failures, denials, guard stops) that intentionally bypass the tools.Registry.
// It keeps their model-facing text unchanged while giving every terminal result
// the same typed state as a normal tool execution.
func normalizeToolResultLifecycle(result *ToolResult) {
	if result == nil {
		return
	}
	normalized := tools.Result{
		Status:    result.Status,
		Lifecycle: result.Lifecycle,
		Error:     result.Error,
		Output:    result.Output,
	}
	tools.NormalizeResultLifecycle(&normalized, nil)
	result.Status = normalized.Status
	result.Lifecycle = normalized.Lifecycle
	result.Error = normalized.Error
}

func emitToolLifecycleProgress(options Options, call ToolCall, state tools.LifecycleStatus, phase string, message string, info *tools.StructuredError, elapsed *time.Duration) {
	if options.OnToolProgress == nil {
		return
	}
	event := streamjson.Event{
		Type:      streamjson.EventToolProgress,
		ID:        call.ID,
		Name:      call.Name,
		Lifecycle: string(state),
		Phase:     phase,
		Message:   message,
	}
	if info != nil {
		event.ErrorKind = string(info.Kind)
		event.Hint = info.Hint
		retryable := info.Retryable
		event.Retryable = &retryable
	}
	if elapsed != nil {
		event.ElapsedMillis = elapsed.Milliseconds()
	}
	options.OnToolProgress(call.ID, event)
}

func toolProgressMessage(result ToolResult) string {
	if result.Error != nil && result.Error.Message != "" {
		return result.Error.Message
	}
	switch result.Lifecycle {
	case tools.LifecycleSucceeded:
		return "completed " + result.Name
	case tools.LifecycleCancelled:
		return "cancelled " + result.Name
	case tools.LifecycleBlocked:
		return "blocked " + result.Name
	case tools.LifecycleTimedOut:
		return "timed out " + result.Name
	default:
		return "failed " + result.Name
	}
}

const sandboxNamespaceLimitedReason = "sandbox output is limited to the sandbox PID namespace; host/global state requires approval"

func maybeRetryUnsandboxedAfterSandboxRestriction(ctx context.Context, registry *tools.Registry, call ToolCall, tool tools.Tool, args map[string]any, result tools.Result, permissionMode PermissionMode, options Options, progressCallback func(streamjson.Event)) (tools.Result, *ToolResult, bool, PermissionDecisionAction, string, []string, error) {
	if retryResult, directResult, retried, action, reason, abortErr := maybeRetryWithNetworkAfterSandboxDenial(ctx, registry, call, tool, args, result, permissionMode, options, progressCallback); retried || directResult != nil || abortErr != nil {
		return retryResult, directResult, retried, action, reason, nil, abortErr
	}
	if !sandboxRestrictedShellRetryCandidate(call, args, result, options) {
		return result, nil, false, "", "", nil, nil
	}
	requestEvent := sandboxRestrictionRetryEvent(call, tool, args, permissionMode, options, result)
	request := permissionRequestFromEvent(requestEvent, args, options)
	if permissionMode == PermissionModeUnsafe || permissionMode == PermissionModeBypass {
		retryArgs := unsandboxedRetryArgs(args)
		retry := runToolForUnsandboxedRetry(ctx, registry, call.Name, call.ID, retryArgs, permissionMode, options, progressCallback)
		return retry, nil, true, PermissionDecisionAllow, "bypass/unsafe permission mode permits unsandboxed retry", nil, nil
	}
	decision, err := requestPermission(ctx, request, options)
	if err != nil {
		decision = PermissionDecision{Action: PermissionDecisionDeny, Reason: err.Error()}
	}
	decision.Action = normalizePermissionDecisionAction(decision.Action)
	reason := strings.TrimSpace(decision.Reason)
	if reason == "" {
		reason = request.Reason
	}
	switch decision.Action {
	case PermissionDecisionAllow, PermissionDecisionAllowStrict, PermissionDecisionAllowForSession, PermissionDecisionAllowPrefix, PermissionDecisionAlwaysAllowPrefix:
		prefix := []string(nil)
		if decision.Action == PermissionDecisionAllowPrefix || decision.Action == PermissionDecisionAlwaysAllowPrefix {
			if len(request.CommandPrefix) == 0 {
				emitDeniedPermission(options, call, requestEvent, reason)
				denied := deniedPermissionResult(call, reason, requestEvent)
				return result, &denied, true, decision.Action, reason, nil, nil
			}
			prefix = append([]string(nil), request.CommandPrefix...)
			if options.Sandbox != nil {
				if decision.Action == PermissionDecisionAlwaysAllowPrefix {
					if grant, err := persistCommandPrefixGrant(call.Name, prefix, reason, options); err == nil {
						prefix = append([]string(nil), grant.Prefix...)
					}
				} else {
					options.Sandbox.GrantCommandPrefixForSession(call.Name, prefix)
				}
			}
		}
		retryArgs := unsandboxedRetryArgs(args)
		retry := runToolForUnsandboxedRetry(ctx, registry, call.Name, call.ID, retryArgs, permissionMode, options, progressCallback)
		return retry, nil, true, decision.Action, reason, prefix, nil
	case PermissionDecisionCancel:
		emitCanceledPermission(options, call, requestEvent, reason)
		canceled := canceledPermissionResult(call, reason, requestEvent)
		return result, &canceled, true, decision.Action, reason, nil, fmt.Errorf("%w for %s", errPermissionApprovalCanceled, call.Name)
	default:
		emitDeniedPermission(options, call, requestEvent, reason)
		denied := deniedPermissionResult(call, reason, requestEvent)
		return result, &denied, true, decision.Action, reason, nil, nil
	}
}

func maybeRetryWithNetworkAfterSandboxDenial(ctx context.Context, registry *tools.Registry, call ToolCall, tool tools.Tool, args map[string]any, result tools.Result, permissionMode PermissionMode, options Options, progressCallback func(streamjson.Event)) (tools.Result, *ToolResult, bool, PermissionDecisionAction, string, error) {
	if !sandboxDeniedNetworkRetryCandidate(call, args, result, options) {
		return result, nil, false, "", "", nil
	}
	requestEvent := sandboxDeniedNetworkRetryEvent(call, tool, args, permissionMode, options, result)
	request := permissionRequestFromEvent(requestEvent, args, options)
	if permissionMode == PermissionModeUnsafe || permissionMode == PermissionModeBypass {
		retry := runToolForNetworkRetry(ctx, registry, call.Name, call.ID, args, permissionMode, options, progressCallback)
		return retry, nil, true, PermissionDecisionAllow, "bypass/unsafe permission mode permits sandbox network retry", nil
	}
	decision, err := requestPermission(ctx, request, options)
	if err != nil {
		decision = PermissionDecision{Action: PermissionDecisionDeny, Reason: err.Error()}
	}
	decision.Action = normalizePermissionDecisionAction(decision.Action)
	reason := strings.TrimSpace(decision.Reason)
	if reason == "" {
		reason = request.Reason
	}
	switch decision.Action {
	case PermissionDecisionAllow, PermissionDecisionAllowStrict, PermissionDecisionAllowForSession:
		scope := sandbox.PermissionGrantScopeTurn
		if decision.Action == PermissionDecisionAllowForSession {
			scope = sandbox.PermissionGrantScopeSession
		}
		cleanup, err := grantNetworkForSandboxPrompt(requestEvent, scope, options)
		if err != nil {
			reason := "failed to grant network permission: " + err.Error()
			emitDeniedPermission(options, call, requestEvent, reason)
			denied := deniedPermissionResult(call, reason, requestEvent)
			return result, &denied, true, decision.Action, reason, nil
		}
		if cleanup != nil {
			defer cleanup()
		}
		retry := runToolForNetworkRetry(ctx, registry, call.Name, call.ID, args, permissionMode, options, progressCallback)
		return retry, nil, true, decision.Action, reason, nil
	case PermissionDecisionCancel:
		emitCanceledPermission(options, call, requestEvent, reason)
		canceled := canceledPermissionResult(call, reason, requestEvent)
		return result, &canceled, true, decision.Action, reason, fmt.Errorf("%w for %s", errPermissionApprovalCanceled, call.Name)
	default:
		emitDeniedPermission(options, call, requestEvent, reason)
		denied := deniedPermissionResult(call, reason, requestEvent)
		return result, &denied, true, decision.Action, reason, nil
	}
}

func sandboxDeniedNetworkRetryCandidate(call ToolCall, args map[string]any, result tools.Result, options Options) bool {
	if options.Sandbox == nil || !isShellCommandTool(call.Name) {
		return false
	}
	if result.Meta[tools.SandboxLikelyDeniedMeta] != "true" || result.Meta[tools.SandboxDenialKindMeta] != tools.SandboxDenialKindNetwork {
		return false
	}
	if value, _ := args["sandbox_permissions"].(string); strings.TrimSpace(value) == string(tools.SandboxPermissionsRequireEscalated) {
		return false
	}
	return true
}

func sandboxDeniedNetworkRetryEvent(call ToolCall, tool tools.Tool, args map[string]any, permissionMode PermissionMode, options Options, result tools.Result) PermissionEvent {
	risk := sandbox.Classify(sandbox.Request{
		ToolName:          call.Name,
		SideEffect:        sandbox.SideEffect(tool.Safety().SideEffect),
		Permission:        sandbox.Permission(tool.Safety().Permission),
		PermissionGranted: false,
		PermissionMode:    sandbox.PermissionMode(permissionMode),
		Args:              args,
		Reason:            sandbox.ReasonNetworkBlocked,
	})
	risk = ensureRiskCategory(risk, "network", sandbox.RiskCritical)
	reason := sandbox.ReasonNetworkBlocked
	if keyword := strings.TrimSpace(result.Meta[tools.SandboxDenialKeywordMeta]); keyword != "" {
		reason += " (" + keyword + ")"
	}
	return PermissionEvent{
		ToolCallID:     call.ID,
		ToolName:       call.Name,
		Action:         PermissionActionPrompt,
		Permission:     string(tools.PermissionPrompt),
		PermissionMode: permissionMode,
		Autonomy:       options.Autonomy,
		SideEffect:     string(tools.SideEffectShell),
		Reason:         sandbox.ReasonNetworkBlocked,
		DecisionReason: reason,
		Scope:          permissionScope(call.Name, args),
		Risk:           risk,
		CommandPrefix:  proposedCommandPrefix(call.Name, args),
	}
}

func sandboxRestrictedShellRetryCandidate(call ToolCall, args map[string]any, result tools.Result, options Options) bool {
	if options.Sandbox == nil || !isShellCommandTool(call.Name) {
		return false
	}
	if !options.Sandbox.UnsandboxedExecutionAllowed() {
		return false
	}
	if value, _ := args["sandbox_permissions"].(string); strings.TrimSpace(value) == string(tools.SandboxPermissionsRequireEscalated) {
		return false
	}
	return sandboxDeniedShellResult(result) || sandboxNamespaceLimitedShellResult(result)
}

func sandboxDeniedShellResult(result tools.Result) bool {
	return result.Status == tools.StatusError && result.Meta[tools.SandboxLikelyDeniedMeta] == "true"
}

func sandboxNamespaceLimitedShellResult(result tools.Result) bool {
	if result.Status != tools.StatusOK {
		return false
	}
	if result.Meta["sandbox_wrapped"] != "true" {
		return false
	}
	if result.Meta["sandbox_backend"] != string(sandbox.BackendLinuxBwrap) && result.Meta["sandbox_target_backend"] != string(sandbox.BackendLinuxBwrap) {
		return false
	}
	output := strings.ToLower(result.Output)
	return strings.Contains(output, "bwrap ") &&
		strings.Contains(output, "--unshare-pid") &&
		strings.Contains(output, "-- /bin/sh -c")
}

func sandboxRestrictionRetryEvent(call ToolCall, tool tools.Tool, args map[string]any, permissionMode PermissionMode, options Options, result tools.Result) PermissionEvent {
	reason := sandboxRestrictionRetryReason(result)
	if keyword := strings.TrimSpace(result.Meta[tools.SandboxDenialKeywordMeta]); keyword != "" {
		reason += " (" + keyword + ")"
	}
	risk := sandbox.Classify(sandbox.Request{
		ToolName:          call.Name,
		SideEffect:        sandbox.SideEffect(tool.Safety().SideEffect),
		Permission:        sandbox.Permission(tool.Safety().Permission),
		PermissionGranted: false,
		PermissionMode:    sandbox.PermissionMode(permissionMode),
		Args:              args,
		Reason:            reason,
	})
	return PermissionEvent{
		ToolCallID:     call.ID,
		ToolName:       call.Name,
		Action:         PermissionActionPrompt,
		Permission:     string(tools.PermissionPrompt),
		PermissionMode: permissionMode,
		Autonomy:       options.Autonomy,
		SideEffect:     string(tools.SideEffectShell),
		Reason:         reason,
		Scope:          permissionScope(call.Name, args),
		Risk:           risk,
		CommandPrefix:  proposedCommandPrefix(call.Name, args),
	}
}

func sandboxRestrictionRetryReason(result tools.Result) string {
	if sandboxDeniedShellResult(result) {
		if reason := strings.TrimSpace(result.Meta[tools.SandboxDenialReasonMeta]); reason != "" {
			return reason
		}
		return "sandbox blocked command execution"
	}
	if sandboxNamespaceLimitedShellResult(result) {
		return sandboxNamespaceLimitedReason
	}
	return "sandbox blocked command execution"
}

func runToolForNetworkRetry(ctx context.Context, registry *tools.Registry, name string, toolCallID string, args map[string]any, permissionMode PermissionMode, options Options, progressCallback func(streamjson.Event)) tools.Result {
	return registry.RunWithOptions(ctx, name, cloneArgs(args), tools.RunOptions{
		PermissionGranted: true,
		PermissionMode:    string(permissionMode),
		Autonomy:          options.Autonomy,
		Sandbox:           options.Sandbox,
		ToolCallID:        toolCallID,
		SessionID:         options.SessionID,
		Model:             options.Model,
		ReasoningEffort:   options.ReasoningEffort,
		Depth:             options.Depth,
		Cwd:               options.Cwd,
		FileTracker:       options.FileTracker,
		OnConfirmation:    confirmationWithTimeout(options.OnConfirmation),
		EnabledTools:      options.EnabledTools,
		DisabledTools:     options.DisabledTools,
		Progress:          progressCallback,
	})
}

func unsandboxedRetryArgs(args map[string]any) map[string]any {
	retryArgs := cloneArgs(args)
	if retryArgs == nil {
		retryArgs = map[string]any{}
	}
	retryArgs["sandbox_permissions"] = string(tools.SandboxPermissionsRequireEscalated)
	return retryArgs
}

func runToolForUnsandboxedRetry(ctx context.Context, registry *tools.Registry, name string, toolCallID string, args map[string]any, permissionMode PermissionMode, options Options, progressCallback func(streamjson.Event)) tools.Result {
	return registry.RunWithOptions(ctx, name, args, tools.RunOptions{
		PermissionGranted: true,
		PermissionMode:    string(permissionMode),
		Autonomy:          options.Autonomy,
		Sandbox:           options.Sandbox,
		ToolCallID:        toolCallID,
		SessionID:         options.SessionID,
		Model:             options.Model,
		ReasoningEffort:   options.ReasoningEffort,
		Depth:             options.Depth,
		Cwd:               options.Cwd,
		FileTracker:       options.FileTracker,
		OnConfirmation:    confirmationWithTimeout(options.OnConfirmation),
		EnabledTools:      options.EnabledTools,
		DisabledTools:     options.DisabledTools,
		Progress:          progressCallback,
	})
}

func toolResultFromPrePermissionReject(call ToolCall, result tools.Result) ToolResult {
	output, outputRedacted := scrubInterceptedOutput(result.Output)
	display := result.Display
	summary, summaryRedacted := scrubInterceptedOutput(display.Summary)
	display.Summary = summary

	meta := result.Meta
	metaRedacted := false
	if len(result.Meta) > 0 {
		meta = make(map[string]string, len(result.Meta))
		for key, value := range result.Meta {
			scrubbed := redaction.RedactString(value, redaction.Options{})
			if scrubbed != value {
				metaRedacted = true
			}
			meta[key] = scrubbed
		}
	}

	return ToolResult{
		ToolCallID:     call.ID,
		Name:           call.Name,
		Status:         result.Status,
		Output:         output,
		Meta:           meta,
		Redacted:       result.Redacted || outputRedacted || summaryRedacted || metaRedacted,
		ChangedFiles:   result.ChangedFiles,
		Images:         sonutruntime.CloneImageBlocks(result.Images),
		Display:        display,
		LoadedTools:    loadedToolsFromResult(meta),
		RequestedModel: meta["escalate_to_model"],
	}
}

func networkDisabledResult(call ToolCall) ToolResult {
	return ToolResult{
		ToolCallID: call.ID,
		Name:       call.Name,
		Status:     tools.StatusOK,
		Output:     "Network is off; no request was made. Continue using local context or answer that current online information is unavailable.",
		Meta: map[string]string{
			"network_disabled": "true",
			"internal_dedup":   "true",
		},
	}
}

// dispatchBeforeTool runs configured beforeTool hooks for a tool call. A hook
// that exits non-zero vetoes the call: the returned bool is true and the tool
// must not run. A nil dispatcher (no hooks wired) is a no-op.
func dispatchBeforeTool(ctx context.Context, options Options, call ToolCall, args map[string]any) (hooks.DispatchOutcome, bool) {
	if options.Hooks == nil {
		return hooks.DispatchOutcome{}, false
	}
	outcome := options.Hooks.Dispatch(ctx, hooks.DispatchInput{
		Event:      hooks.EventBeforeTool,
		ToolName:   call.Name,
		ToolCallID: call.ID,
		Payload: map[string]any{
			"event":      string(hooks.EventBeforeTool),
			"tool":       call.Name,
			"toolCallId": call.ID,
			"sessionId":  options.SessionID,
			"cwd":        options.Cwd,
			"args":       args,
		},
	})
	return outcome, outcome.Blocked
}

// dispatchAfterTool runs configured afterTool hooks once a tool has executed and
// returns any advisory output (e.g. a formatter or vet result) to surface back
// to the model. afterTool hooks never block. A nil dispatcher is a no-op.
func dispatchAfterTool(ctx context.Context, options Options, call ToolCall, args map[string]any, result tools.Result) string {
	if options.Hooks == nil {
		return ""
	}
	outcome := options.Hooks.Dispatch(ctx, hooks.DispatchInput{
		Event:      hooks.EventAfterTool,
		ToolName:   call.Name,
		ToolCallID: call.ID,
		Payload: map[string]any{
			"event":        string(hooks.EventAfterTool),
			"tool":         call.Name,
			"toolCallId":   call.ID,
			"sessionId":    options.SessionID,
			"cwd":          options.Cwd,
			"status":       string(result.Status),
			"changedFiles": result.ChangedFiles,
		},
	})
	return strings.TrimSpace(strings.Join(outcome.Messages, "\n"))
}

// blockedByHookResult is the tool result for a call vetoed by a beforeTool hook.
func blockedByHookResult(call ToolCall, outcome hooks.DispatchOutcome) ToolResult {
	// The hook reason (its stdout/stderr) is model-visible here, an intercepted
	// path that bypasses the registry's output redaction boundary — so scrub it
	// like every other string that crosses into the transcript, and flag Redacted
	// when scrubbing changed it (matching the registry's contract).
	scrubbed := redaction.RedactString(outcome.Reason, redaction.Options{})
	redacted := scrubbed != outcome.Reason
	reason := strings.TrimSpace(scrubbed)
	if reason == "" {
		reason = "blocked by a beforeTool hook"
	}
	message := fmt.Sprintf("Error: %q was blocked by hook %q: %s", call.Name, outcome.BlockedBy, reason)
	return ToolResult{
		ToolCallID:   call.ID,
		Name:         call.Name,
		Status:       tools.StatusError,
		Output:       message,
		Redacted:     redacted,
		DenialReason: DenialHookBlocked,
	}
}

// appendHookFeedback appends afterTool hook output to a tool result's output,
// scrubbed for secrets like every other string crossing the tool boundary. The
// returned bool reports whether scrubbing changed the feedback, so the caller can
// set ToolResult.Redacted to match the registry's redaction contract.
func appendHookFeedback(output string, feedback string) (string, bool) {
	scrubbed := redaction.RedactString(feedback, redaction.Options{})
	redacted := scrubbed != feedback
	if strings.TrimSpace(scrubbed) == "" {
		return output, redacted
	}
	if strings.TrimSpace(output) == "" {
		return "Hook output:\n" + scrubbed, redacted
	}
	return output + "\n\nHook output:\n" + scrubbed, redacted
}

// isRetriableToolError reports whether a failed tool result is one the model can
// plausibly fix by changing its next call (argument or execution failure), as
// opposed to a policy refusal (disabled tool, permission denial, sandbox
// block) that no reformatting will satisfy. Only retriable failures should
// drive the repeated-failure schema hint / stop.
func isRetriableToolError(result ToolResult) bool {
	if result.Status != tools.StatusError {
		return false
	}
	// A categorized denial (filtered / permission / sandbox) is a policy decision,
	// not a transient failure — never retry it. This is robust to message wording
	// (the text checks below remain as a fallback for results lacking the field).
	if result.DenialReason != DenialNone {
		return false
	}
	if result.Meta["permission_action"] == string(PermissionActionDeny) {
		return false
	}
	switch {
	case strings.Contains(result.Output, "is not enabled for this run"),
		strings.Contains(result.Output, "Permission denied for "),
		strings.Contains(result.Output, "Permission required for "),
		strings.Contains(result.Output, "Sandbox block"),
		strings.Contains(result.Output, "Sandbox approval required for "):
		return false
	}
	return true
}

// scrubInterceptedOutput mirrors the registry's scrubResultSecrets boundary for
// the loop-intercepted paths (ask_user answers, task child final answers) that
// build a ToolResult.Output directly instead of going through
// registry.RunWithOptions. RedactString substitutes "[REDACTED]" inline; the
// returned bool reports whether anything was scrubbed so the caller can set
// ToolResult.Redacted, keeping these paths consistent with every other tool
// result the model and transcript see.
func scrubInterceptedOutput(output string) (string, bool) {
	scrubbed := redaction.RedactString(output, redaction.Options{})
	return scrubbed, scrubbed != output
}

// executeAskUser routes an ask_user call to the interactive front-end via
// options.OnAskUser, mirroring the async permission flow. If no handler is set,
// or the handler errors, it falls back to the tool's own graceful result so the
// loop never blocks forever waiting on a user who isn't there.
// executeAskUser returns (result, abortErr). abortErr is non-nil ONLY when the
// prompt was canceled/timed out and the run must stop with that error; every
// other path (success, bad args, headless degradation) returns a nil abortErr.
func executeAskUser(ctx context.Context, registry *tools.Registry, call ToolCall, args map[string]any, permissionMode PermissionMode, options Options) (ToolResult, error) {
	questions, err := tools.ParseAskUserQuestions(args)
	if err != nil {
		return ToolResult{
			ToolCallID: call.ID,
			Name:       call.Name,
			Status:     tools.StatusError,
			Output:     "Error: Invalid arguments for ask_user: " + err.Error(),
		}, nil
	}

	if options.OnAskUser == nil {
		return askUserFallbackResult(ctx, registry, call, args, permissionMode, options), nil
	}

	header, _ := args["header"].(string)
	request := AskUserRequest{
		ToolCallID: call.ID,
		Header:     strings.TrimSpace(header),
		Questions:  toAgentAskUserQuestions(questions),
	}
	promptCtx, promptCancel := context.WithTimeout(ctx, userInteractionTimeout)
	response, err := options.OnAskUser(promptCtx, request)
	promptCancel()
	if err != nil {
		// A deliberate cancellation stops the run. A 10-second no-response
		// timeout degrades to the same autonomous fallback as a headless surface,
		// so an unattended prompt cannot leave the agent idle forever.
		if errors.Is(err, context.Canceled) {
			return ToolResult{
				ToolCallID: call.ID,
				Name:       call.Name,
				Status:     tools.StatusError,
				Output:     "Error: ask_user canceled: " + err.Error(),
			}, err
		}
		if errors.Is(err, context.DeadlineExceeded) {
			return askUserFallbackResult(ctx, registry, call, args, permissionMode, options), nil
		}
		// Genuine handler unavailability (no interactive surface / non-cancel error):
		// degrade to the same headless path as a missing handler.
		return askUserFallbackResult(ctx, registry, call, args, permissionMode, options), nil
	}

	// Scrub the formatted answers through the same redaction boundary the
	// registry applies to tool output, so a secret in a user's answer never lands
	// in the transcript unredacted.
	output, redacted := scrubInterceptedOutput(tools.FormatAskUserAnswers(questions, response.Answers))
	return ToolResult{
		ToolCallID: call.ID,
		Name:       call.Name,
		Status:     tools.StatusOK,
		Output:     output,
		Redacted:   redacted,
	}, nil
}

// askUserFallbackResult runs the registered ask_user tool (or returns the shared
// graceful message) so the no-interactive-user path matches every OTHER tool: it
// goes through registry.RunWithOptions with the same run context (ToolCallID,
// session/model/depth/cwd, sandbox/permission) and copies the full result fields
// (Meta/ChangedFiles/Display), instead of the bare registry.Run that dropped them.
func askUserFallbackResult(ctx context.Context, registry *tools.Registry, call ToolCall, args map[string]any, permissionMode PermissionMode, options Options) ToolResult {
	if _, ok := registry.Get(call.Name); ok {
		result := registry.RunWithOptions(ctx, call.Name, args, tools.RunOptions{
			// ask_user is read-only (PermissionAllow); no prompt/grant is required.
			PermissionGranted: true,
			PermissionMode:    string(permissionMode),
			Autonomy:          options.Autonomy,
			Sandbox:           options.Sandbox,
			ToolCallID:        call.ID,
			SessionID:         options.SessionID,
			Model:             options.Model,
			ReasoningEffort:   options.ReasoningEffort,
			Depth:             options.Depth,
			Cwd:               options.Cwd,
		})
		return ToolResult{
			ToolCallID:   call.ID,
			Name:         call.Name,
			Status:       result.Status,
			Output:       result.Output,
			Meta:         result.Meta,
			Redacted:     result.Redacted,
			ChangedFiles: result.ChangedFiles,
			Images:       sonutruntime.CloneImageBlocks(result.Images),
			Display:      result.Display,
		}
	}
	return ToolResult{
		ToolCallID: call.ID,
		Name:       call.Name,
		Status:     tools.StatusOK,
		Output:     tools.AskUserNonInteractiveMessage(),
	}
}

func toAgentAskUserQuestions(questions []tools.AskUserQuestion) []AskUserQuestion {
	converted := make([]AskUserQuestion, len(questions))
	for index, question := range questions {
		converted[index] = AskUserQuestion{
			Question:           question.Question,
			Header:             question.Header,
			Options:            append([]string{}, question.Options...),
			OptionDescriptions: append([]string{}, question.OptionDescriptions...),
			Recommended:        question.Recommended,
			MultiSelect:        question.MultiSelect,
		}
	}
	return converted
}

func sandboxRequest(toolName string, tool tools.Tool, args map[string]any, permissionGranted bool, permissionMode PermissionMode, options Options) sandbox.Request {
	safety := tool.Safety()
	return sandbox.Request{
		WorkspaceRoot:     "",
		ToolName:          toolName,
		SideEffect:        sandbox.SideEffect(safety.SideEffect),
		Permission:        sandbox.Permission(safety.Permission),
		PermissionGranted: permissionGranted,
		PermissionMode:    sandbox.PermissionMode(permissionMode),
		Args:              args,
		Reason:            safety.Reason,
	}
}

// effectivePermission returns the permission for THIS specific call. A tool that
// implements tools.ArgsPermissioner may relax its static permission for arguments
// it can prove are safe (the Task tool auto-allows a read-only sub-agent); every
// other tool falls back to its static Safety().Permission unchanged.
func effectivePermission(tool tools.Tool, args map[string]any) tools.Permission {
	if p, ok := tool.(tools.ArgsPermissioner); ok {
		return p.PermissionForArgs(args)
	}
	return tool.Safety().Permission
}

func shouldRequestPermission(tool tools.Tool, args map[string]any, permissionGranted bool, decision *sandbox.Decision) bool {
	return shouldRequestPermissionForMode(tool, args, permissionGranted, PermissionModeAsk, decision)
}

// shouldRequestPermissionForMode is shouldRequestPermission with the active
// PermissionMode threaded through: the sandbox decision (e.g. network access)
// is a separate permission axis from the tool-call permission (e.g. a matched
// persistent command-prefix grant). An "always allow `curl`" grant authorizes
// running that command, but must not silently also authorize network access
// the sandbox flags as needing its own prompt — so a sandbox prompt verdict is
// checked before the permissionGranted bypass, UNLESS the run is in true
// bypass/unsafe mode, which never prompts regardless of sandbox verdict.
func shouldRequestPermissionForMode(tool tools.Tool, args map[string]any, permissionGranted bool, permissionMode PermissionMode, decision *sandbox.Decision) bool {
	trueBypassMode := permissionMode == PermissionModeUnsafe || permissionMode == PermissionModeBypass
	if !trueBypassMode && decision != nil && decision.Action == sandbox.ActionPrompt {
		return true
	}
	if permissionGranted {
		return false
	}
	if tool.Safety().Permission != tools.PermissionPrompt {
		return false
	}
	if !permissionGranted && shellCommandAdditionalPermissionsRequested(args) {
		return true
	}
	if decision != nil {
		if sandboxDecisionRequiresExplicitPermission(decision) {
			return true
		}
		if permissionGranted {
			return false
		}
		return decision.Action == sandbox.ActionPrompt
	}
	if permissionGranted {
		return false
	}
	return true
}

func sandboxDecisionRequiresExplicitPermission(decision *sandbox.Decision) bool {
	return decision != nil && decision.Action == sandbox.ActionPrompt && decision.Reason == sandbox.ReasonNetworkBlocked
}

func requestPermission(ctx context.Context, request PermissionRequest, options Options) (PermissionDecision, error) {
	if request.PermissionMode == PermissionModeAuto {
		return PermissionDecision{
			Action: PermissionDecisionAllow,
			Reason: "auto-approved by active permission mode",
		}, nil
	}
	if options.OnPermissionRequest == nil {
		return PermissionDecision{Action: PermissionDecisionDeny, Reason: request.Reason}, nil
	}
	promptCtx, cancel := context.WithTimeout(ctx, userInteractionTimeout)
	defer cancel()
	decision, err := options.OnPermissionRequest(promptCtx, request)
	if errors.Is(err, context.DeadlineExceeded) {
		return PermissionDecision{
			Action: PermissionDecisionDeny,
			Reason: "no user response after 10 seconds; continuing without the requested access",
		}, nil
	}
	return decision, err
}

const userInteractionTimeout = 10 * time.Second

func confirmationWithTimeout(callback func(context.Context, string) (bool, error)) func(context.Context, string) (bool, error) {
	if callback == nil {
		return nil
	}
	return func(ctx context.Context, question string) (bool, error) {
		promptCtx, cancel := context.WithTimeout(ctx, userInteractionTimeout)
		defer cancel()
		confirmed, err := callback(promptCtx, question)
		if errors.Is(err, context.DeadlineExceeded) {
			return false, nil
		}
		return confirmed, err
	}
}

func normalizePermissionDecisionAction(action PermissionDecisionAction) PermissionDecisionAction {
	switch action {
	case PermissionDecisionAllow, PermissionDecisionAllowStrict, PermissionDecisionAllowForSession, PermissionDecisionAllowPrefix, PermissionDecisionAlwaysAllowPrefix, PermissionDecisionAlwaysAllow, PermissionDecisionCancel:
		return action
	default:
		return PermissionDecisionDeny
	}
}

type requestPermissionsArgs struct {
	EnvironmentID string                           `json:"environment_id"`
	Reason        string                           `json:"reason"`
	Permissions   sandbox.RequestPermissionProfile `json:"permissions"`
}

func executeRequestPermissions(ctx context.Context, call ToolCall, args map[string]any, permissionMode PermissionMode, options Options) (ToolResult, error) {
	parsed, err := parseRequestPermissionsArgs(args)
	if err != nil {
		return ToolResult{
			ToolCallID: call.ID,
			Name:       call.Name,
			Status:     tools.StatusError,
			Output:     "Error: Invalid arguments for request_permissions: " + err.Error(),
		}, nil
	}
	normalized, err := sandbox.NormalizeRequestPermissionProfile(parsed.Permissions, options.Cwd)
	if err != nil {
		return ToolResult{
			ToolCallID: call.ID,
			Name:       call.Name,
			Status:     tools.StatusError,
			Output:     "Error: Invalid permissions for request_permissions: " + err.Error(),
		}, nil
	}
	if normalized.Empty() {
		return ToolResult{
			ToolCallID: call.ID,
			Name:       call.Name,
			Status:     tools.StatusError,
			Output:     "Error: request_permissions requires at least one permission.",
		}, nil
	}
	grantProfile, err := sandbox.RequestPermissionGrantProfile(normalized)
	if err != nil {
		return ToolResult{
			ToolCallID: call.ID,
			Name:       call.Name,
			Status:     tools.StatusError,
			Output:     "Error: Invalid permissions for request_permissions: " + err.Error(),
		}, nil
	}

	request := requestPermissionsPrompt(call, parsed, grantProfile, permissionMode, options)

	// Auto/bypass/unsafe modes are no-prompt modes. Auto still honors absolute
	// policy denials, but every normalized grantable request is approved for this
	// turn so the agent does not stop at recoverable filesystem/network access.
	if permissionMode == PermissionModeAuto ||
		permissionMode == PermissionModeBypass ||
		permissionMode == PermissionModeUnsafe {
		response := sandbox.RequestPermissionsResponse{
			Permissions: grantProfile,
			Scope:       sandbox.PermissionGrantScopeTurn,
		}
		if options.Sandbox != nil {
			cleanup, err := options.Sandbox.GrantRequestPermissions(response.Permissions, response.Scope)
			if err != nil {
				return ToolResult{
					ToolCallID: call.ID,
					Name:       call.Name,
					Status:     tools.StatusError,
					Output:     "Error: Failed to grant requested permissions: " + err.Error(),
				}, nil
			}
			if options.runPermissions != nil {
				options.runPermissions.add(cleanup)
			}
		}
		emitRequestPermissionsDecision(options, call, request, PermissionDecisionAllow, request.Reason, true)
		return requestPermissionsResult(call, response, false), nil
	}

	if options.OnPermissionRequest == nil {
		response := sandbox.RequestPermissionsResponse{
			Permissions: sandbox.RequestPermissionProfile{},
			Scope:       sandbox.PermissionGrantScopeTurn,
		}
		return requestPermissionsResult(call, response, false), nil
	}

	decision, err := requestPermission(ctx, request, options)
	if err != nil {
		decision = PermissionDecision{Action: PermissionDecisionDeny, Reason: err.Error()}
	}
	decision.Action = normalizePermissionDecisionAction(decision.Action)
	decisionReason := strings.TrimSpace(decision.Reason)
	if decisionReason == "" {
		decisionReason = request.Reason
	}

	response := sandbox.RequestPermissionsResponse{
		Permissions: sandbox.RequestPermissionProfile{},
		Scope:       sandbox.PermissionGrantScopeTurn,
	}
	permissionGranted := false
	switch decision.Action {
	case PermissionDecisionAllow:
		response.Permissions = grantProfile
		response.Scope = sandbox.PermissionGrantScopeTurn
		permissionGranted = true
	case PermissionDecisionAllowStrict:
		response.Permissions = grantProfile
		response.Scope = sandbox.PermissionGrantScopeTurn
		response.StrictAutoReview = true
		permissionGranted = true
	case PermissionDecisionAllowForSession:
		response.Permissions = grantProfile
		response.Scope = sandbox.PermissionGrantScopeSession
		permissionGranted = true
	case PermissionDecisionCancel:
		decision.Action = PermissionDecisionDeny
		decisionReason = firstNonEmptyString(decisionReason, "continued without granting permissions")
	default:
		decision.Action = PermissionDecisionDeny
		decisionReason = firstNonEmptyString(decisionReason, "continued without granting permissions")
	}

	if permissionGranted {
		if options.Sandbox == nil {
			return ToolResult{
				ToolCallID: call.ID,
				Name:       call.Name,
				Status:     tools.StatusError,
				Output:     "Error: request_permissions cannot grant permissions because the sandbox engine is not configured.",
			}, nil
		}
		cleanup, err := options.Sandbox.GrantRequestPermissions(response.Permissions, response.Scope)
		if err != nil {
			return ToolResult{
				ToolCallID: call.ID,
				Name:       call.Name,
				Status:     tools.StatusError,
				Output:     "Error: Failed to grant requested permissions: " + err.Error(),
			}, nil
		}
		if response.Scope == sandbox.PermissionGrantScopeTurn && options.runPermissions != nil {
			options.runPermissions.add(cleanup)
		}
	}

	emitRequestPermissionsDecision(options, call, request, decision.Action, decisionReason, permissionGranted)
	return requestPermissionsResult(call, response, false), nil
}

func parseRequestPermissionsArgs(args map[string]any) (requestPermissionsArgs, error) {
	data, err := json.Marshal(args)
	if err != nil {
		return requestPermissionsArgs{}, err
	}
	var parsed requestPermissionsArgs
	if err := json.Unmarshal(data, &parsed); err != nil {
		return requestPermissionsArgs{}, err
	}
	if parsed.EnvironmentID == "" {
		if raw, ok := args["environmentId"].(string); ok {
			parsed.EnvironmentID = raw
		}
	}
	return parsed, nil
}

func requestPermissionsPrompt(call ToolCall, parsed requestPermissionsArgs, profile sandbox.RequestPermissionProfile, permissionMode PermissionMode, options Options) PermissionRequest {
	autonomy := options.Autonomy
	reason := strings.TrimSpace(parsed.Reason)
	if reason == "" {
		reason = "The agent is requesting additional permissions."
	}
	return PermissionRequest{
		ToolCallID:     call.ID,
		ToolName:       tools.RequestPermissionsToolName,
		Action:         PermissionActionPrompt,
		Permission:     string(tools.PermissionPrompt),
		PermissionMode: permissionMode,
		Autonomy:       autonomy,
		SideEffect:     "permissions",
		Reason:         reason,
		Scope:          requestPermissionsScope(profile),
		Args: map[string]any{
			"environment_id": parsed.EnvironmentID,
			"reason":         parsed.Reason,
			"permissions":    profile,
		},
		AvailableDecisions: []PermissionDecisionAction{
			PermissionDecisionAllow,
			PermissionDecisionAllowStrict,
			PermissionDecisionAllowForSession,
			PermissionDecisionDeny,
		},
	}
}

func requestPermissionsScope(profile sandbox.RequestPermissionProfile) string {
	parts := []string{}
	if profile.Network != nil && profile.Network.Enabled != nil && *profile.Network.Enabled {
		parts = append(parts, "network all outbound access")
	}
	if profile.FileSystem != nil {
		for _, path := range profile.FileSystem.Read {
			parts = append(parts, "read "+path)
		}
		for _, path := range profile.FileSystem.Write {
			parts = append(parts, "write "+path)
		}
		for _, path := range profile.FileSystem.DenyRead {
			parts = append(parts, "deny read "+path)
		}
	}
	return strings.Join(parts, ", ")
}

func emitRequestPermissionsDecision(options Options, call ToolCall, request PermissionRequest, action PermissionDecisionAction, reason string, granted bool) {
	if options.OnPermission == nil {
		return
	}
	eventAction := PermissionActionDeny
	if granted {
		eventAction = PermissionActionAllow
	}
	options.OnPermission(PermissionEvent{
		ToolCallID:        call.ID,
		ToolName:          call.Name,
		Action:            eventAction,
		DecisionAction:    action,
		Permission:        request.Permission,
		PermissionGranted: granted,
		PermissionMode:    request.PermissionMode,
		Autonomy:          request.Autonomy,
		SideEffect:        request.SideEffect,
		Reason:            reason,
		Scope:             request.Scope,
		DecisionReason:    reason,
	})
}

func requestPermissionsResult(call ToolCall, response sandbox.RequestPermissionsResponse, redacted bool) ToolResult {
	data, err := json.Marshal(response)
	if err != nil {
		return ToolResult{
			ToolCallID: call.ID,
			Name:       call.Name,
			Status:     tools.StatusError,
			Output:     "Error: Failed to serialize request_permissions response: " + err.Error(),
		}
	}
	output, didRedact := scrubInterceptedOutput(string(data))
	return ToolResult{
		ToolCallID: call.ID,
		Name:       call.Name,
		Status:     tools.StatusOK,
		Output:     output,
		Redacted:   redacted || didRedact,
	}
}

func firstNonEmptyString(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

func persistPermissionGrant(toolName string, args map[string]any, reason string, options Options) (sandbox.Grant, error) {
	if options.Sandbox == nil {
		return sandbox.Grant{}, errors.New("sandbox engine is not configured")
	}
	// Scope the grant to exactly what the permission card showed (the file or
	// directory the call touches); engine.Grant anchors it to the workspace. A
	// call with no path-like argument yields an empty scope — a tool-wide grant.
	scope, kind := sandbox.DeriveScope(toolName, args)
	return options.Sandbox.Grant(sandbox.GrantInput{
		ToolName:  toolName,
		Decision:  sandbox.GrantAllow,
		Reason:    reason,
		Scope:     scope,
		ScopeKind: kind,
	})
}

func persistSessionPermissionGrant(toolName string, args map[string]any, reason string, options Options) (sandbox.Grant, error) {
	if options.Sandbox == nil {
		return sandbox.Grant{}, errors.New("sandbox engine is not configured")
	}
	scope, kind := sandbox.DeriveScope(toolName, args)
	return options.Sandbox.GrantForSession(sandbox.GrantInput{
		ToolName:  toolName,
		Decision:  sandbox.GrantAllow,
		Reason:    reason,
		Scope:     scope,
		ScopeKind: kind,
	})
}

func persistCommandPrefixGrant(toolName string, prefix []string, reason string, options Options) (sandbox.CommandPrefixGrant, error) {
	if options.Sandbox == nil {
		return sandbox.CommandPrefixGrant{}, errors.New("sandbox engine is not configured")
	}
	return options.Sandbox.GrantCommandPrefix(sandbox.CommandPrefixInput{
		ToolName: toolName,
		Prefix:   prefix,
		Reason:   reason,
	})
}

func emitDeniedPermission(options Options, call ToolCall, requestEvent PermissionEvent, reason string) {
	if options.OnPermission == nil {
		return
	}
	reason = strings.TrimSpace(reason)
	if reason == "" {
		reason = requestEvent.Reason
	}
	event := requestEvent
	event.ToolCallID = call.ID
	event.ToolName = call.Name
	event.Action = PermissionActionDeny
	event.DecisionAction = PermissionDecisionDeny
	event.PermissionGranted = false
	event.DecisionReason = reason
	options.OnPermission(event)
}

func emitCanceledPermission(options Options, call ToolCall, requestEvent PermissionEvent, reason string) {
	if options.OnPermission == nil {
		return
	}
	reason = strings.TrimSpace(reason)
	if reason == "" {
		reason = "cancelled in TUI"
	}
	event := requestEvent
	event.ToolCallID = call.ID
	event.ToolName = call.Name
	event.Action = PermissionActionCancel
	event.DecisionAction = PermissionDecisionCancel
	event.PermissionGranted = false
	event.DecisionReason = reason
	event.Reason = reason
	options.OnPermission(event)
}

func deniedPermissionResult(call ToolCall, reason string, requestEvent PermissionEvent) ToolResult {
	reason = strings.TrimSpace(reason)
	if reason == "" {
		reason = requestEvent.Reason
	}
	if reason == "" {
		reason = "tool requires approval before execution"
	}
	event := requestEvent
	event.Action = PermissionActionDeny
	event.DecisionAction = PermissionDecisionDeny
	event.PermissionGranted = false
	event.DecisionReason = reason
	if event.Risk.Level == "" {
		event.Risk = sandbox.Risk{Level: sandbox.RiskMedium, Reason: reason}
	}
	if requestEvent.ToolName == "" {
		event.ToolName = call.Name
	}
	// A denial driven by a sandbox block is categorized distinctly from a
	// plain approval-declined so a surface can tell policy from user choice.
	denial := DenialPermissionDenied
	if requestEvent.Block != nil {
		denial = DenialSandboxBlock
	}
	return ToolResult{
		ToolCallID:   call.ID,
		Name:         call.Name,
		Status:       tools.StatusError,
		Output:       "Error: Permission denied for " + call.Name + ": " + reason,
		DenialReason: denial,
		Meta: map[string]string{
			"permission_action": string(event.Action),
		},
	}
}

func canceledPermissionResult(call ToolCall, reason string, requestEvent PermissionEvent) ToolResult {
	reason = strings.TrimSpace(reason)
	if reason == "" {
		reason = "cancelled in TUI"
	}
	event := requestEvent
	event.Action = PermissionActionCancel
	event.DecisionAction = PermissionDecisionCancel
	event.PermissionGranted = false
	event.DecisionReason = reason
	event.Reason = reason
	if requestEvent.ToolName == "" {
		event.ToolName = call.Name
	}
	return ToolResult{
		ToolCallID:   call.ID,
		Name:         call.Name,
		Status:       tools.StatusError,
		Output:       "Error: Permission approval cancelled for " + call.Name + ": " + reason,
		DenialReason: DenialApprovalCanceled,
		Meta: map[string]string{
			"permission_action": string(event.Action),
		},
	}
}

// permissionScope returns a concise, human-readable description of what a tool
// call will actually touch -- a file path, directory, working dir, or network
// host lifted from its arguments -- so the permission card and persisted
// decision can show the user exactly what "allow" covers. It is empty when the
// tool exposes no scoped argument (the grant is then plainly tool-wide). The
// first matching key wins, ordered most-specific (a concrete file) first.
func permissionScope(toolName string, args map[string]any) string {
	// sandbox.DeriveScope is the single source of truth for which arguments carry
	// a scope, shared with grant persistence and matching so the card display can
	// never diverge from what an "always allow" actually covers.
	raw, _ := sandbox.DeriveScope(toolName, args)
	if raw == "" {
		return ""
	}
	return truncateScope(raw)
}

func truncateScope(value string) string {
	const maxScopeRunes = 80
	runes := []rune(value)
	if len(runes) <= maxScopeRunes {
		return value
	}
	return string(runes[:maxScopeRunes-1]) + "…"
}

func buildPermissionEvent(call ToolCall, tool tools.Tool, args map[string]any, permissionGranted bool, permissionMode PermissionMode, options Options, decision *sandbox.Decision) (PermissionEvent, bool) {
	safety := tool.Safety()
	var action PermissionAction
	reason := safety.Reason
	risk := sandbox.Classify(sandbox.Request{
		WorkspaceRoot:     "",
		ToolName:          call.Name,
		SideEffect:        sandbox.SideEffect(safety.SideEffect),
		Permission:        sandbox.Permission(safety.Permission),
		PermissionGranted: permissionGranted,
		PermissionMode:    sandbox.PermissionMode(permissionMode),
		Args:              args,
		Reason:            safety.Reason,
	})
	var block *sandbox.Block
	grantMatched := false
	var grant *sandbox.Grant

	if decision != nil {
		action = permissionActionFromSandbox(decision.Action)
		if decision.Reason != "" {
			reason = decision.Reason
		}
		risk = decision.Risk
		block = decision.Block
		grantMatched = decision.GrantMatched
		grant = decision.Grant
	} else {
		switch safety.Permission {
		case tools.PermissionDeny:
			action = PermissionActionDeny
		case tools.PermissionPrompt:
			if permissionGranted {
				action = PermissionActionAllow
			} else {
				action = PermissionActionPrompt
			}
		default:
			return PermissionEvent{}, false
		}
	}

	if safety.Permission == tools.PermissionAllow && action == PermissionActionAllow && !grantMatched && block == nil {
		return PermissionEvent{}, false
	}

	scopeText := permissionScope(call.Name, args)
	if profile, ok, err := inlineAdditionalPermissionsProfile(args, options.Cwd); ok && err == nil {
		scopeText = requestPermissionsScope(profile)
	}
	reason = userFacingPermissionReason(call.Name, args, reason)

	return PermissionEvent{
		ToolCallID:        call.ID,
		ToolName:          call.Name,
		Action:            action,
		DecisionAction:    grantDecisionAction(grant),
		Permission:        string(safety.Permission),
		PermissionGranted: permissionGranted,
		PermissionMode:    permissionMode,
		Autonomy:          options.Autonomy,
		SideEffect:        string(safety.SideEffect),
		Reason:            reason,
		Scope:             scopeText,
		Risk:              risk,
		Block:             block,
		GrantMatched:      grantMatched,
		Grant:             grant,
		CommandPrefix:     proposedCommandPrefix(call.Name, args),
	}, true
}

func userFacingPermissionReason(toolName string, args map[string]any, reason string) string {
	reason = strings.TrimSpace(reason)
	if !isShellCommandTool(toolName) || !shellCommandRequiresEscalated(args) {
		return reason
	}
	if justification, ok := firstStringArg(args, "justification"); ok {
		return justification
	}
	if reason == "" || reason == sandbox.ReasonEscalatedSandboxRequired {
		return "This command needs to run outside the sandbox."
	}
	return reason
}

func grantDecisionAction(grant *sandbox.Grant) PermissionDecisionAction {
	if grant == nil {
		return ""
	}
	if grant.Session {
		return PermissionDecisionAllowForSession
	}
	if grant.Decision == sandbox.GrantAllow {
		return PermissionDecisionAlwaysAllow
	}
	return ""
}

func fallbackPermissionEvent(call ToolCall, tool tools.Tool, args map[string]any, permissionMode PermissionMode, options Options) PermissionEvent {
	event, _ := buildPermissionEvent(call, tool, args, false, permissionMode, options, nil)
	return event
}

func permissionRequestFromEvent(event PermissionEvent, args map[string]any, options Options) PermissionRequest {
	return PermissionRequest{
		ToolCallID:         event.ToolCallID,
		ToolName:           event.ToolName,
		Action:             event.Action,
		Permission:         event.Permission,
		PermissionMode:     event.PermissionMode,
		Autonomy:           event.Autonomy,
		SideEffect:         event.SideEffect,
		Reason:             event.Reason,
		Scope:              event.Scope,
		Risk:               event.Risk,
		Args:               cloneArgs(args),
		Block:              event.Block,
		GrantMatched:       event.GrantMatched,
		Grant:              event.Grant,
		CommandPrefix:      append([]string(nil), event.CommandPrefix...),
		AvailableDecisions: availablePermissionDecisions(event, args, options),
	}
}

func availablePermissionDecisions(event PermissionEvent, args map[string]any, options Options) []PermissionDecisionAction {
	if event.Action != PermissionActionPrompt {
		return nil
	}
	decisions := []PermissionDecisionAction{PermissionDecisionAllow}
	inlineAdditionalPermissions := shellCommandAdditionalPermissionsRequested(args)
	if options.Sandbox != nil {
		decisions = append(decisions, PermissionDecisionAllowForSession)
		if isShellCommandTool(event.ToolName) && len(event.CommandPrefix) > 0 && !networkSandboxPrompt(event) && !inlineAdditionalPermissions {
			decisions = append(decisions, PermissionDecisionAllowPrefix)
			if options.Sandbox.CanPersistGrants() {
				decisions = append(decisions, PermissionDecisionAlwaysAllowPrefix)
			}
		}
		if options.Sandbox.CanPersistGrants() && permissionSupportsPersistentDecision(event.ToolName) && !filesystemSandboxPrompt(event) && !inlineAdditionalPermissions {
			decisions = append(decisions, PermissionDecisionAlwaysAllow)
		}
	}
	switch {
	case isShellCommandTool(event.ToolName):
		decisions = append(decisions, PermissionDecisionDeny, PermissionDecisionCancel)
	case event.ToolName == "apply_patch":
		decisions = append(decisions, PermissionDecisionCancel)
	default:
		decisions = append(decisions, PermissionDecisionDeny)
	}
	return decisions
}

func networkSandboxPrompt(event PermissionEvent) bool {
	return isShellCommandTool(event.ToolName) &&
		event.Reason == sandbox.ReasonNetworkBlocked &&
		sandbox.HasRiskCategory(event.Risk, "network")
}

func ensureRiskCategory(risk sandbox.Risk, category string, level sandbox.RiskLevel) sandbox.Risk {
	if !sandbox.HasRiskCategory(risk, category) {
		risk.Categories = append(risk.Categories, category)
		sort.Strings(risk.Categories)
	}
	risk.Level = level
	if len(risk.Categories) > 0 {
		risk.Reason = fmt.Sprintf("%s risk: %s", risk.Level, strings.Join(risk.Categories, ", "))
	}
	return risk
}

func shellCommandAdditionalPermissionsRequested(args map[string]any) bool {
	raw, ok := args["sandbox_permissions"]
	if !ok || raw == nil {
		return false
	}
	value, ok := raw.(string)
	if !ok {
		value = fmt.Sprint(raw)
	}
	return strings.TrimSpace(value) == string(tools.SandboxPermissionsWithAdditionalPermissions)
}

func inlineAdditionalPermissionsProfile(args map[string]any, basePath string) (sandbox.RequestPermissionProfile, bool, error) {
	if !shellCommandAdditionalPermissionsRequested(args) {
		if _, exists := args["additional_permissions"]; exists {
			return sandbox.RequestPermissionProfile{}, false, fmt.Errorf("additional_permissions requires sandbox_permissions set to %q", tools.SandboxPermissionsWithAdditionalPermissions)
		}
		return sandbox.RequestPermissionProfile{}, false, nil
	}
	raw, ok := args["additional_permissions"]
	if !ok || raw == nil {
		return sandbox.RequestPermissionProfile{}, true, fmt.Errorf("missing additional_permissions; provide at least one of network or file_system")
	}
	data, err := json.Marshal(raw)
	if err != nil {
		return sandbox.RequestPermissionProfile{}, true, err
	}
	var profile sandbox.RequestPermissionProfile
	if err := json.Unmarshal(data, &profile); err != nil {
		return sandbox.RequestPermissionProfile{}, true, err
	}
	normalized, err := sandbox.NormalizeRequestPermissionProfile(profile, basePath)
	if err != nil {
		return sandbox.RequestPermissionProfile{}, true, err
	}
	if normalized.Empty() {
		return sandbox.RequestPermissionProfile{}, true, fmt.Errorf("additional_permissions must include at least one requested permission in network or file_system")
	}
	grantProfile, err := sandbox.RequestPermissionGrantProfile(normalized)
	if err != nil {
		return sandbox.RequestPermissionProfile{}, true, err
	}
	return grantProfile, true, nil
}

func grantInlineAdditionalPermissions(args map[string]any, scope sandbox.PermissionGrantScope, options Options) (func(), error) {
	profile, ok, err := inlineAdditionalPermissionsProfile(args, options.Cwd)
	if err != nil || !ok {
		return nil, err
	}
	if options.Sandbox == nil {
		return nil, errors.New("sandbox engine is not configured")
	}
	return options.Sandbox.GrantRequestPermissions(profile, scope)
}

func grantNetworkForSandboxPrompt(event PermissionEvent, scope sandbox.PermissionGrantScope, options Options) (func(), error) {
	if !networkSandboxPrompt(event) || options.Sandbox == nil {
		return nil, nil
	}
	enabled := true
	return options.Sandbox.GrantRequestPermissions(sandbox.RequestPermissionProfile{
		Network: &sandbox.NetworkPermissions{Enabled: &enabled},
	}, scope)
}

func filesystemSandboxPrompt(event PermissionEvent) bool {
	return event.Block != nil &&
		event.Block.Code == sandbox.BlockOutsideWorkspace &&
		event.Block.Recoverable &&
		strings.TrimSpace(event.Block.Path) != ""
}

func grantFilesystemForSandboxPrompt(event PermissionEvent, scope sandbox.PermissionGrantScope, options Options) (func(), error) {
	if !filesystemSandboxPrompt(event) || options.Sandbox == nil {
		return nil, nil
	}
	path := event.Block.Path
	fs := sandbox.FileSystemPermissions{}
	switch sandbox.SideEffect(event.SideEffect) {
	case sandbox.SideEffectRead:
		fs.Read = []string{path}
	case sandbox.SideEffectWrite, sandbox.SideEffectOutOfWorkspace:
		fs.Write = []string{path}
	default:
		return nil, nil
	}
	return options.Sandbox.GrantRequestPermissions(sandbox.RequestPermissionProfile{
		FileSystem: &fs,
	}, scope)
}

func permissionSupportsPersistentDecision(toolName string) bool {
	switch toolName {
	case "bash", "exec_command", "write_stdin", "apply_patch":
		return false
	case "browser_install", "browser_launch", "browser_connect", "browser_open", "browser_snapshot", "browser_click", "browser_type", "browser_press", "browser_action", "desktop_windows", "desktop_snapshot", "desktop_action", "terminal_session", "capture_artifact":
		return false
	default:
		return true
	}
}

// isBrowserToolName reports whether name is one of the browser automation
// tools subject to the browser-specific consecutive-failure loop budget.
// Browser automation is served by the playwright MCP server, whose tools
// register with an "mcp_<server>_" prefix (e.g. mcp_playwright_browser_click),
// so match interaction tools by their browser_* suffix as well as the legacy
// bare names — otherwise the loop budget never applies to the tools actually
// in use.
func isBrowserToolName(name string) bool {
	switch name {
	case "browser_click", "browser_type", "browser_press", "browser_action", "web_browser":
		return true
	}
	// MCP-prefixed interaction tools (playwright: browser_click, browser_type,
	// browser_press_key, browser_drag, browser_select_option, browser_tabs, ...).
	// Only state-mutating interactions count toward the failure budget — snapshots
	// and navigation are how the model recovers, so they stay exempt.
	// browser_tabs is included: repeated tab-open failures (e.g. file:// block)
	// should trigger the budget and force a new approach, not loop indefinitely.
	for _, suffix := range []string{"_browser_click", "_browser_type", "_browser_press_key", "_browser_drag", "_browser_select_option", "_browser_fill_form", "_browser_tabs"} {
		if strings.HasPrefix(name, "mcp_") && strings.HasSuffix(name, suffix) {
			return true
		}
	}
	return false
}

// browserActionKey builds a composite "action+target" key from a tool call so
// the loop budget can tell identical retries (same click on the same ref)
// apart from genuinely different actions.
func browserActionKey(call ToolCall) string {
	var args map[string]any
	if call.Arguments != "" {
		_ = decodeToolArguments(call.Arguments, &args)
	}
	target := ""
	for _, key := range []string{"ref", "selector", "command", "key"} {
		if value, ok := args[key].(string); ok && value != "" {
			target = value
			break
		}
	}
	if target == "" {
		return call.Name
	}
	return call.Name + ":" + target
}

// nonEmptyFinalAnswer guarantees the run never surfaces a blank final answer.
// A tool-driven stop reason (e.g. a spec-review gate) can legitimately carry
// empty Output; rather than showing nothing after minutes of work, this
// synthesizes a short recap from what the guard state actually observed —
// files changed, tool calls made, and the last tool failure, if any — and
// marks the result Incomplete so the completion gate treats a synthesized
// answer as unfinished work, not a confirmed success.
func nonEmptyFinalAnswer(raw string, guards *guardState, result *Result) string {
	if strings.TrimSpace(raw) != "" {
		return raw
	}
	result.Incomplete = true
	if result.IncompleteReason == "" {
		result.IncompleteReason = "the model produced no final answer text"
	}
	var parts []string
	if guards != nil && len(guards.writtenFiles) > 0 {
		files := make([]string, 0, len(guards.writtenFiles))
		for f := range guards.writtenFiles {
			files = append(files, f)
		}
		sort.Strings(files)
		if len(files) > 6 {
			files = append(files[:6], fmt.Sprintf("+%d more", len(guards.writtenFiles)-6))
		}
		parts = append(parts, "Files changed: "+strings.Join(files, ", "))
	}
	if result.Turns > 0 {
		parts = append(parts, fmt.Sprintf("Ran %d turn(s) of tool calls.", result.Turns))
	}
	if guards != nil {
		for name, record := range guards.toolFailures {
			if record != nil && record.count > 0 {
				parts = append(parts, fmt.Sprintf("Last error from %s: %s", name, record.errSig))
				break
			}
		}
	}
	summary := "The assistant ended the turn without producing a final answer."
	if len(parts) > 0 {
		summary += " " + strings.Join(parts, " ")
	}
	return summary
}

func cloneArgs(args map[string]any) map[string]any {
	if len(args) == 0 {
		return nil
	}
	copied := make(map[string]any, len(args))
	for key, value := range args {
		copied[key] = value
	}
	return copied
}

func permissionActionFromSandbox(action sandbox.Action) PermissionAction {
	switch action {
	case sandbox.ActionAllow:
		return PermissionActionAllow
	case sandbox.ActionDeny:
		return PermissionActionDeny
	default:
		return PermissionActionPrompt
	}
}

// partitionTools builds the per-turn advertised tool list and optional
// tool_search discovery text. INACTIVE (DeferThreshold <= 0 or the eligible count is
// below it): every visible tool is exposed with its full schema EXCEPT tool_search
// (dropped so it is never advertised when it cannot help), and the discovery text is
// empty — byte-identical to the pre-deferral output. ACTIVE: a deferred-eligible
// tool is exposed only when loaded[name]; otherwise it is hidden and searchable
// through tool_search. Non-deferred tools (including tool_search) are always
// exposed. The exposed slice is alpha-sorted by name, matching the legacy order
// so the inactive path is stable.
func partitionTools(registry *tools.Registry, permissionMode PermissionMode, options Options, loaded map[string]bool) ([]sonutruntime.ToolDefinition, string) {
	// Shape-gated no-tool runs (greetings, pure knowledge Q) skip every schema
	// so hybrid local models don't re-prefill 1–2k tool tokens for a 1-line reply.
	if options.NoTools || registry == nil {
		return nil, ""
	}
	registeredTools := registry.All()

	visible := make([]tools.Tool, 0, len(registeredTools))
	eligible := 0
	for _, tool := range registeredTools {
		if options.QuickWebSearch && tool.Name() != "web_search" {
			continue
		}
		if options.QuickBrowser && !isQuickBrowserToolName(tool.Name()) {
			continue
		}
		if !ToolVisible(tool, permissionMode, options.EnabledTools, options.DisabledTools) {
			continue
		}
		visible = append(visible, tool)
		// Count by deferral-ELIGIBILITY, not current deferred state: a tool that
		// un-defers at runtime (e.g. swarm coordination tools once a swarm is
		// active) still counts, so it can't drop `eligible` below the threshold
		// and force-expose every other deferred tool. Whether a tool is actually
		// hidden is decided by IsDeferred in the exposure loop below.
		if tools.IsDeferralEligible(tool) {
			eligible++
		}
	}

	// Deferral may activate only when tool_search is actually runnable; otherwise
	// the loop would hide deferred tools behind a loader the dispatch gate rejects
	// — an inescapable dead-end. "Runnable" mirrors executeToolCall's gate:
	// registered, not in DisabledTools, and advertised in the current permission
	// mode (e.g. not spec-draft, where tool_search is not advertised). The
	// EnabledTools allowlist is intentionally NOT checked here — tool_search is
	// exempt from the allowlist at dispatch, so an allowlist that omits it must
	// not disable deferral.
	loader, loaderFound := registry.Get(tools.ToolSearchToolName)
	loaderUsable := loaderFound &&
		!containsToolName(options.DisabledTools, tools.ToolSearchToolName) &&
		ToolAdvertised(loader, permissionMode)

	active := options.DeferThreshold > 0 && eligible >= options.DeferThreshold && loaderUsable

	definitions := make([]sonutruntime.ToolDefinition, 0, len(visible))
	exposedNames := make(map[string]bool, len(visible))
	var hiddenTools []tools.Tool
	for _, tool := range visible {
		name := tool.Name()
		deferred := tools.IsDeferred(tool)

		if !active {
			// Inactive: byte-identical to legacy, but tool_search is never advertised.
			if name == tools.ToolSearchToolName {
				continue
			}
			definitions = append(definitions, sonutruntime.ToolDefinition{
				Name:        name,
				Description: tool.Description(),
				Parameters:  schemaToRuntimeMap(tool.Parameters()),
			})
			exposedNames[name] = true
			continue
		}

		// Active path.
		if deferred && !loaded[name] {
			hiddenTools = append(hiddenTools, tool)
			continue
		}
		definitions = append(definitions, sonutruntime.ToolDefinition{
			Name:        name,
			Description: tool.Description(),
			Parameters:  schemaToRuntimeMap(tool.Parameters()),
		})
		exposedNames[name] = true
	}

	// On the ACTIVE path tool_search is guaranteed runnable (active implies
	// loaderUsable), so it must ALWAYS be reachable — even when a non-empty
	// EnabledTools allowlist omits it (the operator allowlisted the deferred
	// tools, not the loader). Expose its full definition whenever it is not
	// already in the exposed set. This never runs on the inactive path, so the
	// byte-identical below-threshold output is preserved.
	discovery := ""
	if active && len(hiddenTools) > 0 {
		discovery = tools.BuildToolSearchDescription(hiddenTools)
	}
	if active && !exposedNames[tools.ToolSearchToolName] {
		description := loader.Description()
		if discovery != "" {
			description = discovery
		}
		definitions = append(definitions, sonutruntime.ToolDefinition{
			Name:        loader.Name(),
			Description: description,
			Parameters:  schemaToRuntimeMap(loader.Parameters()),
		})
	} else if active && discovery != "" {
		for i := range definitions {
			if definitions[i].Name == tools.ToolSearchToolName {
				definitions[i].Description = discovery
				break
			}
		}
	}

	sort.Slice(definitions, func(left int, right int) bool {
		return definitions[left].Name < definitions[right].Name
	})

	return definitions, discovery
}

// isQuickBrowserToolName keeps an explicit browser request lightweight. The
// local unified browser can open and snapshot in one tool, while Playwright
// MCP exposes the same two operations as separate prefixed tools.
func isQuickBrowserToolName(name string) bool {
	switch name {
	case "browser_open", "browser_snapshot", "web_browser":
		return true
	}
	return strings.HasPrefix(name, "mcp_") &&
		(strings.HasSuffix(name, "_browser_navigate") || strings.HasSuffix(name, "_browser_snapshot"))
}

func ToolVisible(tool tools.Tool, permissionMode PermissionMode, enabledTools []string, disabledTools []string) bool {
	return ToolAllowedByFilters(tool.Name(), enabledTools, disabledTools) && ToolAdvertised(tool, permissionMode)
}

func ToolAllowedByFilters(name string, enabledTools []string, disabledTools []string) bool {
	if len(enabledTools) > 0 {
		if !containsToolName(enabledTools, name) {
			return false
		}
	}
	if containsToolName(disabledTools, name) {
		return false
	}
	return true
}

func containsToolName(names []string, name string) bool {
	for _, candidate := range names {
		if candidate == name {
			return true
		}
	}
	return false
}

func schemaToRuntimeMap(schema tools.Schema) map[string]any {
	parameters := map[string]any{
		"type":                 schema.Type,
		"additionalProperties": schema.AdditionalProperties,
	}

	if len(schema.Required) > 0 {
		parameters["required"] = append([]string{}, schema.Required...)
	}

	if len(schema.Properties) > 0 {
		properties := make(map[string]any, len(schema.Properties))
		for name, property := range schema.Properties {
			properties[name] = propertyToRuntimeMap(property)
		}
		parameters["properties"] = properties
	}

	return parameters
}

func propertyToRuntimeMap(property tools.PropertySchema) map[string]any {
	schema := map[string]any{
		"type": property.Type,
	}
	if property.Description != "" {
		schema["description"] = property.Description
	}
	if len(property.Enum) > 0 {
		schema["enum"] = append([]string{}, property.Enum...)
	}
	if property.Default != nil {
		schema["default"] = property.Default
	}
	if property.Items != nil {
		schema["items"] = propertyToRuntimeMap(*property.Items)
	}
	if property.Minimum != nil {
		schema["minimum"] = *property.Minimum
	}
	if property.Maximum != nil {
		schema["maximum"] = *property.Maximum
	}
	if len(property.Properties) > 0 {
		properties := make(map[string]any, len(property.Properties))
		for name, nested := range property.Properties {
			properties[name] = propertyToRuntimeMap(nested)
		}
		schema["properties"] = properties
	}
	if len(property.Required) > 0 {
		schema["required"] = append([]string{}, property.Required...)
	}
	return schema
}

func ToolAdvertised(tool tools.Tool, permissionMode PermissionMode) bool {
	if tool.Safety().Permission == tools.PermissionDeny {
		return false
	}
	if permissionMode == PermissionModeSpecDraft {
		return toolAdvertisedInSpecDraft(tool)
	}
	if permissionMode == PermissionModeAuto {
		return tool.Safety().Permission != tools.PermissionDeny
	}
	if permissionMode == PermissionModeMemberAuto {
		// Like Auto, plus the in-workspace mutators a headless member needs to
		// build. The sandbox engine still decides at call time: in-workspace writes
		// and sandbox-backed shell auto-allow, while out-of-workspace writes,
		// network, and destructive commands prompt → denied headless. So this
		// advertises capability without widening sandbox authority.
		if tool.Safety().Permission == tools.PermissionAllow || tool.Safety().AdvertiseInAuto {
			return true
		}
		switch tool.Safety().SideEffect {
		case tools.SideEffectWrite, tools.SideEffectShell:
			return true
		}
		return false
	}
	return true
}

func toolAdvertisedInSpecDraft(tool tools.Tool) bool {
	switch tool.Name() {
	case "ask_user", "submit_spec":
		return true
	case "update_plan":
		return false
	}
	safety := tool.Safety()
	return safety.SideEffect == tools.SideEffectRead && safety.Permission == tools.PermissionAllow
}

func stopReasonFromToolResult(result ToolResult) StopReason {
	if result.Meta == nil {
		return ""
	}
	if result.Meta[toolResultMetaControl] == toolResultControlSpecReview {
		return StopReasonSpecReviewRequired
	}
	return ""
}

// loadedToolsFromResult extracts the deferred-tool names a tool (tool_search)
// asked the loop to expose next turn from Meta["load_tools"] (comma-separated).
// It trims each name and drops empties; returns nil when the key is absent or
// yields no names, so an ordinary result keeps a nil LoadedTools. Mirrors the
// Meta-driven control signal read by stopReasonFromToolResult.
func loadedToolsFromResult(meta map[string]string) []string {
	raw := meta["load_tools"]
	if strings.TrimSpace(raw) == "" {
		return nil
	}
	var names []string
	for _, part := range strings.Split(raw, ",") {
		if trimmed := strings.TrimSpace(part); trimmed != "" {
			names = append(names, trimmed)
		}
	}
	return names
}

// appendAbortedToolResults adds a placeholder tool-result message for each of
// the given (unexecuted) tool calls, so every advertised tool_use keeps a
// matching tool_result when the loop halts a turn before all calls have run.
func appendAbortedToolResults(messages []Message, remaining []ToolCall) []Message {
	for _, call := range remaining {
		messages = append(messages, sonutruntime.Message{
			Role:       sonutruntime.MessageRoleTool,
			Content:    abortedToolResultNotice,
			ToolCallID: call.ID,
		})
	}
	return messages
}

// dedupeStrings returns values with duplicates removed, preserving first-seen
// order so the deduped list (e.g. a turn's changed files) stays deterministic.
func dedupeStrings(values []string) []string {
	seen := make(map[string]bool, len(values))
	out := make([]string, 0, len(values))
	for _, value := range values {
		if seen[value] {
			continue
		}
		seen[value] = true
		out = append(out, value)
	}
	return out
}

// isFileWriteTool reports whether a tool name is a file-mutating tool.
func isFileWriteTool(name string) bool {
	switch name {
	case "write_file", "append_file", "edit_file", "apply_patch", "create_file", "overwrite_file":
		return true
	}
	return false
}

// claimsFileWrite reports whether the model's text claims to have edited or
// written a file — past-tense assertions of completed file work that would
// only be true if a file tool had actually been called.
func claimsFileWrite(text string) bool {
	text = strings.TrimSpace(text)
	if len(text) == 0 || len(text) > 1200 {
		return false
	}
	lower := strings.ToLower(text)
	// Past-tense or declarative claims of file mutation.
	claimPhrases := []string{
		"edited ", "updated ", "modified ", "rewrote ", "rewritten ",
		"wrote to ", "written to ", "saved to ", "created the file",
		"the file has been", "file is now ", "file now contains",
		"i have edited", "i have updated", "i have modified", "i have written",
		"i've edited", "i've updated", "i've written", "i've created",
		"successfully edited", "successfully updated", "successfully written",
		"successfully created", "successfully modified",
		"changes have been", "code has been updated", "html has been updated",
		"css has been updated", "js has been updated",
	}
	for _, phrase := range claimPhrases {
		if strings.Contains(lower, phrase) {
			return true
		}
	}
	return false
}

// claimsCompletion reports whether the model's final text asserts the work is
// finished — the trigger for the verify-before-done gate after unverified file
// writes. A false positive only costs one bounded verification nudge.
func claimsCompletion(text string) bool {
	if claimsFileWrite(text) {
		return true
	}
	lower := strings.ToLower(strings.TrimSpace(text))
	if lower == "" {
		return false
	}
	for _, phrase := range []string{
		"done", " fixed", "fixed ", "complete", "implemented", "finished",
		"all set", "should now", "is now", "now works", "ready to use",
	} {
		if strings.Contains(lower, phrase) {
			return true
		}
	}
	return false
}

// requestMessages returns the exact message list to send for this request: a
// copy of the persisted history plus a transient, authoritative plan-state
// block folded into the latest user message whenever a plan has been recorded.
// The block is rebuilt fresh from guards' own store on every call. Folding it
// into the existing user turn avoids an extra synthetic message and keeps the
// provider's prompt sequence compact while preserving plan ground truth.
func requestMessages(messages []Message, guards *guardState) []Message {
	copied := copyMessages(messages)
	if block := guards.planStateBlock(); block != "" {
		for i := len(copied) - 1; i >= 0; i-- {
			if copied[i].Role != sonutruntime.MessageRoleUser {
				continue
			}
			copied[i].Content += "\n\n" + block
			break
		}
	}
	return copied
}

// seedConversationMessages keeps persisted turns as real role-separated
// messages. The current user prompt is always the final message, so a short
// follow-up such as "which position" can refer to the preceding answer while
// still taking the flash path's small system prompt and tool set.
func seedConversationMessages(systemPrompt, userPrompt string, history []Message, images []sonutruntime.ImageBlock) []Message {
	messages := []Message{{Role: sonutruntime.MessageRoleSystem, Content: systemPrompt}}
	for _, message := range history {
		if message.Role != sonutruntime.MessageRoleUser && message.Role != sonutruntime.MessageRoleAssistant {
			continue
		}
		if strings.TrimSpace(message.Content) == "" && len(message.Images) == 0 && len(message.ToolCalls) == 0 {
			continue
		}
		messages = append(messages, copyMessages([]Message{message})[0])
	}
	messages = append(messages, Message{
		Role:    sonutruntime.MessageRoleUser,
		Content: userPrompt,
		Images:  sonutruntime.CloneImageBlocks(images),
	})
	return messages
}

func copyMessages(messages []Message) []Message {
	copied := make([]Message, len(messages))
	for index, message := range messages {
		copied[index] = message
		if message.ToolCalls != nil {
			copied[index].ToolCalls = append([]ToolCall{}, message.ToolCalls...)
		}
		if message.Reasoning != nil {
			copied[index].Reasoning = append([]sonutruntime.ReasoningBlock{}, message.Reasoning...)
		}
		// Deep-copy image attachments (slice AND each Data byte slice) so the
		// raw image bytes are never aliased across history/request/result copies.
		copied[index].Images = sonutruntime.CloneImageBlocks(message.Images)
	}
	return copied
}
