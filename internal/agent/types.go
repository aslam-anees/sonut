package agent

import (
	"context"
	"time"

	"github.com/aistatine/sonut/internal/hooks"
	"github.com/aistatine/sonut/internal/sandbox"
	"github.com/aistatine/sonut/internal/sonutruntime"
	"github.com/aistatine/sonut/internal/streamjson"
	"github.com/aistatine/sonut/internal/tools"
)

type Message = sonutruntime.Message
type Provider = sonutruntime.Provider
type ToolCall = sonutruntime.ToolCall
type Usage = sonutruntime.Usage

type PermissionMode string
type PermissionAction string
type PermissionDecisionAction string

// RunMode is the deliberate top-level workflow selected by the fast router.
// Quick answers skip planning and workspace tools; a fresh-fact quick answer
// may expose only web_search. Builder runs get one deep planning phase and
// then fast tool execution/validation. Empty means auto-detect from the ask.
type RunMode string

const (
	RunModeAuto    RunMode = ""
	RunModeQuick   RunMode = "quick"
	RunModeBuilder RunMode = "builder"
)

const (
	PermissionModeAuto      PermissionMode = "auto"
	PermissionModeAsk       PermissionMode = "ask"
	PermissionModeUnsafe    PermissionMode = "unsafe"
	PermissionModeBypass    PermissionMode = "bypass" // full privilege, no permission prompts
	PermissionModeSpecDraft PermissionMode = "spec-draft"
	// PermissionModeMemberAuto is a headless mode for swarm/specialist MEMBERS: it
	// advertises the in-workspace mutators a member needs to build (write/edit +
	// shell) on top of the Auto set, while the sandbox engine still gates them at
	// call time — in-workspace writes and sandbox-backed shell auto-allow, but
	// out-of-workspace writes, network, and destructive commands still prompt (and
	// a headless member has no approver, so they are denied). It normalizes to Auto
	// everywhere except ToolAdvertised, so authority is never widened beyond what an
	// interactive auto agent already has inside the sandbox.
	PermissionModeMemberAuto PermissionMode = "member-auto"
)

type StopReason string

const (
	StopReasonSpecReviewRequired StopReason = "spec_review_required"
)

const (
	PermissionActionAllow  PermissionAction = "allow"
	PermissionActionPrompt PermissionAction = "prompt"
	PermissionActionDeny   PermissionAction = "deny"
	PermissionActionCancel PermissionAction = "cancel"
)

const (
	PermissionDecisionAllow             PermissionDecisionAction = "allow"
	PermissionDecisionAllowStrict       PermissionDecisionAction = "allow_with_strict_auto_review"
	PermissionDecisionAllowForSession   PermissionDecisionAction = "allow_for_session"
	PermissionDecisionAllowPrefix       PermissionDecisionAction = "allow_prefix_for_session"
	PermissionDecisionAlwaysAllowPrefix PermissionDecisionAction = "always_allow_prefix"
	PermissionDecisionDeny              PermissionDecisionAction = "deny"
	PermissionDecisionAlwaysAllow       PermissionDecisionAction = "always_allow"
	PermissionDecisionCancel            PermissionDecisionAction = "cancel"
)

type ToolResult struct {
	ToolCallID string
	Name       string
	Status     tools.Status
	// Lifecycle/Error are structured lifecycle metadata for UI, sessions, and
	// stream-json consumers. Output remains the compact model-facing result.
	Lifecycle    tools.LifecycleStatus
	Error        *tools.StructuredError
	Output       string
	Meta         map[string]string
	Redacted     bool
	ChangedFiles []string
	Images       []sonutruntime.ImageBlock
	Display      tools.Display
	// DenialReason categorizes why a tool call was blocked (empty when it ran).
	// It lets a surface distinguish the cause precisely instead of parsing Output.
	DenialReason DenialCategory
	// LoadedTools carries the deferred-tool names a tool_search call asked the
	// loop to expose next turn (lifted from Meta["load_tools"]). nil for every
	// ordinary tool result; only tool_search populates it.
	LoadedTools []string
	// RequestedModel is the model id a tool asked the loop to switch to for the
	// rest of the run (lifted from the tool's Meta["escalate_to_model"]). Empty
	// for every normal tool result; the Run loop performs the switch when it is
	// set and Options.ModelSwitcher is wired.
	RequestedModel string
}

// DenialCategory classifies why a tool call was blocked before it executed.
type DenialCategory string

const (
	DenialNone             DenialCategory = ""
	DenialFiltered         DenialCategory = "filtered"          // tool not enabled for this run
	DenialPermissionDenied DenialCategory = "permission_denied" // approval declined
	DenialApprovalCanceled DenialCategory = "approval_canceled" // approval canceled and run aborted
	DenialSandboxBlock     DenialCategory = "sandbox_block"     // blocked by the sandbox
	DenialHookBlocked      DenialCategory = "hook_blocked"      // vetoed by a beforeTool hook
)

type PermissionRequest struct {
	ToolCallID         string                     `json:"toolCallId"`
	ToolName           string                     `json:"name"`
	Action             PermissionAction           `json:"action"`
	Permission         string                     `json:"permission"`
	PermissionMode     PermissionMode             `json:"permissionMode"`
	Autonomy           string                     `json:"autonomy,omitempty"`
	SideEffect         string                     `json:"sideEffect"`
	Reason             string                     `json:"reason,omitempty"`
	Scope              string                     `json:"scope,omitempty"`
	Risk               sandbox.Risk               `json:"risk"`
	Args               map[string]any             `json:"args,omitempty"`
	Block              *sandbox.Block             `json:"block,omitempty"`
	GrantMatched       bool                       `json:"grantMatched,omitempty"`
	Grant              *sandbox.Grant             `json:"grant,omitempty"`
	CommandPrefix      []string                   `json:"commandPrefix,omitempty"`
	AvailableDecisions []PermissionDecisionAction `json:"availableDecisions,omitempty"`
}

type PermissionDecision struct {
	Action PermissionDecisionAction `json:"action"`
	Reason string                   `json:"reason,omitempty"`
}

type PermissionEvent struct {
	ToolCallID        string                   `json:"toolCallId"`
	ToolName          string                   `json:"name"`
	Action            PermissionAction         `json:"action"`
	DecisionAction    PermissionDecisionAction `json:"decisionAction,omitempty"`
	Permission        string                   `json:"permission"`
	PermissionGranted bool                     `json:"permissionGranted,omitempty"`
	PermissionMode    PermissionMode           `json:"permissionMode"`
	Autonomy          string                   `json:"autonomy,omitempty"`
	SideEffect        string                   `json:"sideEffect"`
	Reason            string                   `json:"reason,omitempty"`
	Scope             string                   `json:"scope,omitempty"`
	DecisionReason    string                   `json:"decisionReason,omitempty"`
	Risk              sandbox.Risk             `json:"risk"`
	Block             *sandbox.Block           `json:"block,omitempty"`
	GrantMatched      bool                     `json:"grantMatched,omitempty"`
	Grant             *sandbox.Grant           `json:"grant,omitempty"`
	CommandPrefix     []string                 `json:"commandPrefix,omitempty"`
}

// AskUserQuestion is one clarifying question the agent wants answered. Options are
// optional suggested answers an interactive front-end can render as a picker;
// Recommended (when set) is the suggested default — it should match one of Options.
// Header is an optional short tab title for a multi-question prompt (falls back to
// the question text). OptionDescriptions, when present, holds a one-line description
// per option aligned by index to Options (empty string = no description).
type AskUserQuestion struct {
	Question           string   `json:"question"`
	Header             string   `json:"header,omitempty"`
	Options            []string `json:"options,omitempty"`
	OptionDescriptions []string `json:"optionDescriptions,omitempty"`
	Recommended        string   `json:"recommended,omitempty"`
	MultiSelect        bool     `json:"multiSelect,omitempty"`
}

// AskUserRequest is handed to OnAskUser when the model invokes the ask_user tool.
type AskUserRequest struct {
	ToolCallID string            `json:"toolCallId"`
	Header     string            `json:"header,omitempty"`
	Questions  []AskUserQuestion `json:"questions"`
}

// AskUserResponse carries the user's answers back to the loop, one per question.
type AskUserResponse struct {
	Answers []string `json:"answers"`
}

// SpecialistInfo is a one-line summary of a delegatable sub-agent (its name and
// when-to-use description) surfaced to the orchestrator's system prompt so it can
// route work to the right specialist. It is plain data so the agent package needs
// no dependency on internal/specialist.
type SpecialistInfo struct {
	Name      string
	WhenToUse string
}

type Options struct {
	MaxTurns int
	// DeferThreshold activates deferred MCP-tool loading: when the number of
	// deferred-eligible visible tools is >= this value (and it is > 0), their
	// full schemas are withheld and advertised as compact lines via tool_search.
	// 0 (or below the eligible count) keeps every tool eager — byte-identical to
	// the pre-deferral behavior.
	DeferThreshold int
	// Specialists lists the sub-agents the orchestrator may delegate to via the
	// Task tool; when non-empty the system prompt gains a delegation section that
	// names them and nudges the model to offload read-heavy work (search,
	// exploration) so verbose tool output stays out of the main context. It is
	// populated only where the Task tool is actually registered, so an empty slice
	// (the default) reproduces the previous prompt byte-for-byte.
	Specialists []SpecialistInfo
	// Specialist/sub-agent metadata is carried through exec now and consumed by
	// the specialist runtime in later slices.
	SessionID        string
	CallingSessionID string
	CallingToolUseID string
	Tag              string
	Depth            int
	SessionTitle     string
	ProviderName     string
	Model            string
	ReasoningEffort  string
	Cwd              string
	SystemPrompt     string
	// PersistentContext is a bounded, durable memory/checkpoint fragment. It is
	// appended to coding system prompts and deliberately excluded from flash
	// Q&A prompts so long-lived sessions do not add latency to short answers.
	// When empty, Run loads the project-local evolution context automatically.
	PersistentContext string
	// ResponseStyle is the operator-selected reply style from the TUI /style
	// command (e.g. "concise", "explanatory", "review"). It is rendered into the
	// system prompt as a short directive. Empty or "balanced" adds nothing — the
	// prompt is then byte-identical to the pre-style behavior.
	ResponseStyle string
	// RunMode selects the two-phase surface workflow. Empty lets Run classify
	// the raw request without an extra model round trip.
	RunMode RunMode
	// VoiceOrigin marks a request submitted by the temporary voice surface. It
	// is metadata only: the caller still supplies an explicit RunMode.
	VoiceOrigin bool
	// SpeakResponse asks a voice-aware surface to consume streamed answer text
	// for speech. The agent remains responsible only for producing text.
	SpeakResponse bool
	// VoiceUtteranceID correlates streamed text and cancellation with the native
	// voice helper. It is never sent to the model.
	VoiceUtteranceID string
	// UnifiedWorkflow keeps builder work in one agent loop. The model privately
	// classifies, plans with update_plan, executes, and verifies; the harness does
	// not launch a separate planner or validation model request.
	UnifiedWorkflow bool
	// ExecutorOnly resumes an existing planner-owned roadmap. It skips the
	// initial planner pass and starts the run in the concise-thinking executor
	// phase. Interactive continuation nudges use this instead of looking like a
	// fresh builder request, which otherwise re-runs grounding and planning.
	ExecutorOnly bool
	// BuildMode is the TUI /build toggle: when true the system prompt gains an
	// app-builder overlay (workflow + tech stack, ported from dyad). False (the
	// default) leaves the prompt byte-identical to pre-build-mode behavior.
	BuildMode bool
	// BuildIntegrations lists integrations the user configured via /integrate
	// (names only, never secrets); build mode wires them into the prompt.
	BuildIntegrations []string
	// Images are optional image attachments to seed onto the initial user turn.
	// nil for text-only runs (the seeded message then carries no images, exactly
	// as before).
	Images []sonutruntime.ImageBlock
	// ConversationHistory contains prior user/assistant turns loaded by a
	// surface from its session store. The current prompt is appended after this
	// history. Keeping this separate from the prompt prevents fast Q&A routing
	// from accidentally dropping the context needed by short follow-ups.
	ConversationHistory []sonutruntime.Message
	// ContextWindow is the model's maximum input token budget. When > 0 the agent
	// loop compacts long conversations once the estimated size crosses a fraction
	// of this window. 0 DISABLES compaction entirely (every existing caller/test
	// behaves identically).
	ContextWindow int
	// CompactionPreserveLast is how many trailing messages compaction keeps
	// verbatim. <= 0 falls back to defaultCompactionPreserveLast.
	CompactionPreserveLast int
	Registry               *tools.Registry
	PermissionMode         PermissionMode
	Autonomy               string
	Sandbox                *sandbox.Engine
	// NetworkDisabled is the interactive /network off switch. It blocks both
	// in-process web tools and sandboxed shell egress without making offline
	// questions fail the run.
	NetworkDisabled bool
	// FileTracker records per-session file read/write versions so the write tools
	// can detect a file changed on disk outside @code since it was last read. nil
	// disables the check. Created once per session and threaded into every tool run.
	FileTracker *tools.FileTracker
	// Hooks, when set, runs configured beforeTool (blocking) and afterTool
	// (advisory) commands around each tool call. nil disables hooks entirely; a
	// dispatcher built from an empty config is also a safe no-op.
	Hooks         *hooks.Dispatcher
	EnabledTools  []string
	DisabledTools []string
	// NoTools forces an empty advertised tool list for this run. Used for
	// greetings and pure knowledge/identity questions so local hybrid models
	// (Bonsai/atcode) skip the tool-schema prefill tax on every cold turn.
	// Tools remain available for real tasks; this is a per-turn shape gate.
	NoTools bool
	// QuickWebSearch exposes only web_search on a quick factual-answer turn.
	// It avoids the full workspace/tool-schema prefill while still letting a
	// current answer use DuckDuckGo. It is set internally by Run.
	QuickWebSearch bool
	// QuickBrowser exposes only the small browser surface needed for an explicit
	// quick browser request (open/navigate plus snapshot). It keeps flash turns
	// fast without turning an instruction such as "open Airbnb" into a no-tool
	// answer.
	QuickBrowser bool
	// OnText streams visible assistant content (executor answers). During the
	// planner pass the model writes the plan into the collect buffer instead;
	// the final numbered plan is delivered via OnPlan.
	OnText func(string)
	// OnReasoning streams private reasoning. Fires for planner high-think and
	// for any executor redeepen thinks so the TUI can show process live.
	OnReasoning func(string)
	OnToolCall  func(ToolCall)
	// OnToolCallStart / OnToolCallDelta stream a tool call's arguments LIVE as the
	// model generates them — OnToolCallStart on open (id, tool name), then
	// OnToolCallDelta for each argument fragment. A surface can render the
	// in-progress call (e.g. a file being written) instead of waiting for
	// OnToolCall, which only fires once the whole call has accumulated. nil no-ops.
	OnToolCallStart func(id, name string)
	OnToolCallDelta func(id, fragment string)
	// streamingCommits is populated internally for the duration of a generation
	// turn. File tools stream into sibling staging files first; executeToolCall
	// passes the matching commit closure through the normal permission/hook path
	// once the provider has completed the JSON arguments.
	streamingCommits    map[string]func() tools.Result
	OnPermissionRequest func(context.Context, PermissionRequest) (PermissionDecision, error)
	OnPermission        func(PermissionEvent)
	OnAskUser           func(context.Context, AskUserRequest) (AskUserResponse, error)
	// OnConfirmation, when set, lets a tool ask the user a simple yes/no
	// question mid-run (e.g. write_file overwrite confirmation). Forwarded to
	// tools.RunOptions.OnConfirmation on every tool dispatch. nil means no
	// interactive surface: tools treat that as "not confirmed" fail-safe.
	OnConfirmation func(context.Context, string) (bool, error)
	OnToolResult   func(ToolResult)
	OnUsage        func(Usage)
	// OnToolProgress receives out-of-band lifecycle updates for every tool call.
	// Task also forwards its child stream-json events through this callback. The
	// toolCallID identifies the parent call; progress never enters model context.
	OnToolProgress func(toolCallID string, event streamjson.Event)
	// OnContext, when set, is called once per turn with the per-category context
	// budget of the request about to be sent, so a surface (TUI/CLI) can show
	// context utilization. Opt-in like the other callbacks; nil is a no-op.
	OnContext func(ContextBreakdown)
	// OnCompaction is called after a paid summary successfully replaces old
	// messages. Surfaces can persist the summary as a durable session event so a
	// later resume reconstructs the same compact context instead of replaying an
	// unbounded raw transcript. Cheap tool-output reclamation does not trigger it.
	OnCompaction func(CompactionResult)
	// ModelSwitcher, when set, lets a tool escalate the run to a stronger model
	// mid-run: the loop calls it with the requested model id and, on success,
	// swaps the active provider and updates Options.Model for the rest of the
	// run. nil DISABLES escalation entirely (the loop ignores any switch
	// request), so every existing caller is unaffected. A returned error is
	// non-fatal: the run continues on the current model.
	ModelSwitcher func(ctx context.Context, modelID string) (Provider, error)
	// SelfCorrect, when set, runs a post-edit verify-and-correct cycle after a
	// mutating tool call: it verifies the changed files (LSP diagnostics + project
	// tests) and feeds failures back to the model to fix, bounded by an attempt
	// ceiling and the autonomy gate. nil disables it entirely (the loop is
	// byte-identical to before). One instance per run — it holds attempt state.
	SelfCorrect *SelfCorrector

	// Steer, when set, lets the surface inject user messages typed DURING the
	// run: the loop drains the inbox at every turn boundary (and before
	// accepting a final answer) and appends each message as a priority user
	// turn, so the model reacts mid-run instead of after the run ends. nil
	// disables steering entirely (the loop is byte-identical to before).
	Steer *SteerInbox

	// RequireCompletionSignal gates run completion for HEADLESS exec. Without it,
	// any assistant turn that produces text but no tool call is accepted as the
	// final answer. With it, a no-tool-call turn is NOT treated as "done" while
	// work clearly remains — pending update_plan items, or a message that ends on a
	// continuation cue ("…Let me check the config:"). The loop then nudges the
	// model to continue instead, bounded by maxContinueNudges (and still by
	// MaxTurns and the run deadline); if the model keeps stalling, the run
	// finalizes as INCOMPLETE (Result.Incomplete) rather than success. Default
	// false leaves the loop byte-identical, so the interactive TUI is unaffected.
	RequireCompletionSignal bool

	// GenerationTimeout, when > 0, is a hard wall-clock cap on every model
	// generation. If a generation exceeds it the stream is cancelled and retried
	// ONCE with thinking forced off and a timeout nudge — backstops reasoning
	// runaways that per-turn byte caps miss. 0 disables the cap (default).
	// Recommended: 240s for local models.
	GenerationTimeout time.Duration

	// VerifyBeforeDone enables the interactive verify-before-done gate: when the
	// model concludes with a completion claim while its most recent tool activity
	// was an unverified file write, the loop injects one bounded nudge asking for
	// a proving step (build/test/run/read-back) before accepting the answer.
	// Default false keeps the loop byte-identical for existing callers and tests.
	VerifyBeforeDone bool

	// Evaluate enables the optional planner harness for legacy callers. TUI
	// builder runs use UnifiedWorkflow instead, keeping planning and execution in
	// one continuous model loop.
	Evaluate bool
	// PlannerPrompt, when non-empty, is the user's RAW request text used to
	// decide whether the planner pass should run (and as the planner's input).
	// Surfaces that wrap the prompt with session context (the TUI's resume
	// preamble) must set this, otherwise the triviality gate sees the wrapped
	// prompt and fires the planner on "do it"-sized asks. Empty falls back to
	// the run prompt.
	PlannerPrompt string
	// OnEvaluator is retained as a source-compatible legacy hook. When set, it
	// receives planner-validation results; interactive surfaces should prefer
	// OnPlan so validation updates the roadmap without adding noisy transcript
	// rows. nil is a no-op.
	OnEvaluator func(passed bool, feedback string)
	// OnPlan, when set, is called with the planner pass's numbered plan (if one
	// ran) so a surface can render it — the plan itself is folded into the
	// generator's user message, never shown as a fake assistant turn. nil is a
	// no-op.
	OnPlan func(plan string)
	// OnPlanProgress receives a full reconciled roadmap after a successful file
	// write clearly completes a file-named step. It keeps interactive plan panes
	// truthful even when a fast executor forgets a redundant update_plan call.
	OnPlanProgress func(items []tools.PlanItem)
	// OnPlanStart, when set, is called right before the planner pass begins so a
	// surface can show "planning…" instead of dead air — on a slow local model
	// the pass can take tens of seconds with zero visible output. nil is a no-op.
	OnPlanStart func()
	// OnValidationStart, when set, is called before the planner's post-execution
	// validation pass so a surface can show active verification between the
	// executor and final answer. nil is a no-op.
	OnValidationStart func()
	// OnPrefillProgress, when set, is called with prefill progress metrics
	// (processed/total tokens) on each prefill-progress heartbeat so a surface
	// can show live prefill percentage. nil is a no-op.
	OnPrefillProgress func(processed, total int)

	runPermissions *permissionRunState
	// resolvedFromAuto records that ReasoningEffort was resolved by the auto
	// classifier (not pinned explicitly by the user), which permits the
	// planner-turn upgrade to maximum thinking.
	resolvedFromAuto bool
}

type Result struct {
	FinalAnswer string
	Turns       int
	Messages    []Message
	StopReason  StopReason
	// FinishReason is the provider's normalized terminal stop reason for the turn
	// that produced FinalAnswer: sonutruntime.FinishReasonLength when the output
	// hit the token cap, FinishReasonContentFilter when it was filtered. Empty for
	// a normal completion.
	FinishReason string
	// Incomplete reports that a headless run (RequireCompletionSignal) stopped with
	// work clearly unfinished: the model ended a turn with no tool call while plan
	// items were pending or the message ended mid-step, the model admitted it
	// guessed / could not meet the objective, and/or it failed a task-grounded
	// acceptance check. Callers map it to a non-success terminal status / exit
	// code. False for every normal completion.
	Incomplete bool
	// IncompleteReason is a short, model-derived explanation of why the run was
	// marked Incomplete (e.g. "pending plan items remain"). Empty when Incomplete
	// is false. Surfaced in logs / run_end so an abandoned run is debuggable.
	IncompleteReason string
}

// Truncated reports whether the final response ended abnormally (cut off at the
// output token cap or withheld by a content filter) rather than completing
// naturally. Callers can use it to warn the user that FinalAnswer is incomplete.
func (result Result) Truncated() bool {
	return result.FinishReason != ""
}

// TruncationNotice returns a user-facing warning when the final response was
// truncated, or "" for a normal completion. Shared by the CLI and TUI so the
// wording stays consistent.
func (result Result) TruncationNotice() string {
	switch result.FinishReason {
	case sonutruntime.FinishReasonLength:
		return "Response was cut off at the output token limit and may be incomplete. " +
			"Raise the model's max output tokens or ask @code to continue."
	case sonutruntime.FinishReasonContentFilter:
		return "Response was withheld or cut off by the provider's content filter and may be incomplete."
	case "":
		return ""
	default:
		return "Response ended early (" + result.FinishReason + ") and may be incomplete."
	}
}
