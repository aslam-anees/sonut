package tui

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"charm.land/bubbles/v2/spinner"
	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/aistatine/sonut/internal/agent"
	"github.com/aistatine/sonut/internal/config"
	"github.com/aistatine/sonut/internal/doctor"
	"github.com/aistatine/sonut/internal/evolution"
	userfeedback "github.com/aistatine/sonut/internal/feedback"
	"github.com/aistatine/sonut/internal/llama"
	"github.com/aistatine/sonut/internal/localcontrol"
	"github.com/aistatine/sonut/internal/lsp"
	internalmcp "github.com/aistatine/sonut/internal/mcp"
	"github.com/aistatine/sonut/internal/modelregistry"
	"github.com/aistatine/sonut/internal/notify"
	"github.com/aistatine/sonut/internal/providerhealth"
	"github.com/aistatine/sonut/internal/providermodeldiscovery"
	"github.com/aistatine/sonut/internal/providers/providerio"
	"github.com/aistatine/sonut/internal/sandbox"
	"github.com/aistatine/sonut/internal/sessions"
	"github.com/aistatine/sonut/internal/sonutgit"
	"github.com/aistatine/sonut/internal/sonutruntime"
	"github.com/aistatine/sonut/internal/sonuttrash"
	"github.com/aistatine/sonut/internal/streamjson"
	"github.com/aistatine/sonut/internal/tools"
	"github.com/aistatine/sonut/internal/usage"
	"github.com/aistatine/sonut/internal/usercommands"
	voicepkg "github.com/aistatine/sonut/internal/voice"
	"github.com/aistatine/sonut/internal/webmirror"
)

const tuiToolOutputLimit = 240
const defaultResponseStyle = "balanced"
const chatWheelScrollLines = 5
const ctrlCExitConfirmDuration = 3 * time.Second
const ctrlCExitConfirmText = "Press Ctrl+C again to exit"

// escCancelConfirmDuration/escCancelConfirmText guard Esc cancelling a running
// turn, mirroring ctrlCExitConfirmDuration/ctrlCExitConfirmText's pattern (same
// window, same amber footer treatment) so the two "stop it" keybinds feel
// consistent. Without this, a single stray Esc — e.g. meant to dismiss a
// suggestion overlay that had already closed — silently threw away an
// in-progress run with no chance to reconsider.
const escCancelConfirmDuration = 3 * time.Second
const escCancelConfirmText = "Press Esc again to cancel"

// dragEdgeScrollInterval/dragEdgeScrollStep drive the smooth-glide auto-scroll
// while a drag holds past the transcript edge (see edgeScrollDelta). A small step
// on a short, steady cadence reads as a smooth continuous scroll; the wheel-scroll
// tick size (chatWheelScrollLines) would jump too far per step for that.
const dragEdgeScrollInterval = 70 * time.Millisecond
const dragEdgeScrollStep = 1

type model struct {
	ctx                         context.Context
	cwd                         string
	userCommands                []usercommands.Command // file-sourced /commands (.atcode/commands)
	userConfigPath              string
	doctorUserConfigPath        string
	projectConfigPath           string
	gitBranch                   string
	providerName                string
	modelName                   string
	llamaPort                   int  // port for the local llama.cpp server (default 2748)
	llamaVision                 bool // true when llama-server has multimodal/mmproj loaded
	providerProfile             config.ProviderProfile
	savedProviders              []config.ProviderProfile
	provider                    sonutruntime.Provider
	newProvider                 func(config.ProviderProfile) (sonutruntime.Provider, error)
	probeProviderHealth         func(context.Context, providerhealth.Options) providerhealth.Result
	discoverProviderModels      func(context.Context, config.ProviderProfile) ([]providermodeldiscovery.Model, error)
	discoverOllamaContextWindow func(ctx context.Context, baseURL string, model string) (int, error)
	registry                    *tools.Registry
	sessionStore                *sessions.Store
	sandboxStore                *sandbox.GrantStore
	mcpConfig                   config.MCPConfig
	mcpPermissionStore          *internalmcp.PermissionStore
	mcpTokenStore               *internalmcp.TokenStore
	mcpCommand                  func(context.Context, []string) MCPCommandResult
	sandboxSetupCommand         func(context.Context) SandboxSetupCommandResult
	mcpViewStateCache           MCPViewState
	mcpViewStateReady           bool
	mcpCommandSeq               int
	mcpCommandCancel            context.CancelFunc
	sandboxSetupSeq             int
	sandboxSetupInFlight        bool
	doctorCommandSeq            int
	doctorInFlight              bool
	doctorFrame                 int
	activeSession               sessions.Metadata
	sessionEvents               []sessions.Event
	// titledSessions records session ids for which a model-generated title has
	// already been attempted this process, so a finished turn re-fires the title
	// generator at most once per session (even before its async result lands).
	// Lazily initialized.
	titledSessions map[string]bool
	// retitle* drive the sequential /retitle backfill: queued session ids still
	// awaiting a title, whether a backfill is running, and its progress counters.
	retitleQueue              []string
	retitleActive             bool
	retitleTotal              int
	retitleDone               int
	retitleOK                 int
	usageTracker              *usage.Tracker
	sessionCompactor          SessionCompactor
	prService                 *PrService
	submitFeedback            func(context.Context, string) error
	prState                   PrState
	prWatcherStop             func()
	runtimeMessageSink        func(tea.Msg)
	agentOptions              agent.Options
	notifier                  *notify.Notifier
	permissionMode            agent.PermissionMode
	browserHeadful            bool // browser automation window shown to the user (desktop default: headful)
	terminalBrowserActive     bool // the real terminal-browser split owns the visible browser surface
	secView                   secViewState
	voiceView                 voiceViewState
	knowledgeView             knowledgeViewState
	knowledgeSeq              int
	knowledgeGeneration       int
	secScanSeq                int  // monotonically identifies background /sec scans
	networkEnabled            bool // internet/tool egress is enabled by default; /network toggles it per session
	selfCorrectTests          bool
	reasoningEffort           modelregistry.ReasoningEffort
	responseStyle             string
	voice                     *voiceRuntime
	voiceRuns                 map[int]string
	voiceBuilderRuns          map[int]bool
	themeMode                 themeMode // palette preference: auto (default), dark, light
	hasDarkBg                 bool      // last terminal background-detection result (auto mode)
	userAgent                 string
	compactRequests           int
	compactInFlight           bool
	compactFrame              int
	lastCompactResult         *CompactResult
	lastCompactError          string
	compactCustomInstructions string
	// nonstop enables automatic run continuation when plan tasks remain open
	// after a turn ends. It is opt-in because the in-run planner/executor loop
	// already owns continuation; external runs are only a recovery escape hatch.
	nonstop           bool
	nonstopNudgeCount int
	// vimMode enables vim-style editing in the composer (normal/insert modes, hjkl motions).
	vimMode vimModeState
	// buildMode overlays the system prompt with the app-builder workflow
	// (ported from dyad) so plain requests become complete scaffolded web apps.
	buildMode bool
	// Diminishing-returns stop (mirrors sonut-code's tokenBudget.ts): track the
	// open-plan count across consecutive nudges; if several nudges in a row make
	// no plan progress, stop auto-continuing instead of burning the full budget.
	nonstopLastOpenCount int
	nonstopStaleNudges   int

	// Knowledge auto-capture: every successful run appends a prompt→outcome
	// entry to the session's note in ~/.atcode/knowledge, unless the user opted
	// this session out with /hide session.
	lastUserPrompt  string
	knowledgeHidden bool
	// initialResumeID, when non-empty, triggers an automatic /resume on startup.
	// Set from --resume CLI flag via Options.ResumeSessionID.
	initialResumeID       string
	unpricedRequests      int
	unpricedTokens        int
	lastUsage             usage.Normalized
	lastUsageSeen         bool
	transcript            []transcriptRow
	transcriptDetailed    bool
	helpOverlay           bool // the `?` keyboard-shortcut overlay is open
	transcriptBodyHeights *transcriptBodyHeightCache
	input                 textinput.Model
	composer              composerState
	composerActive        bool
	composerCursorVisible bool
	// ambientPhase is the low-frequency animation clock for decorative idle
	// surfaces such as the landing wordmark. It advances on the composer's
	// existing blink tick, so the empty screen gains a restrained shimmer
	// without introducing another timer or repaint loop.
	ambientPhase          int
	composerPastePreviews []composerPastePreview
	composerSelection     composerSelectionState
	// plan holds the sticky plan panel state (steps, expansion, timings)
	// synced from the update_plan tool. See plan_panel.go.
	plan            planPanelState
	specialists     specialistTracker
	stepWork        map[string][]planStepWork // file mutations + commands captured per in_progress plan step, for the clickable step detail
	stepNarration   map[string][]string       // the agent's own prose narration captured per in_progress plan step, for the step detail's explanation
	planDetailOpen  bool                      // a plan-step detail card is currently shown (click-to-toggle)
	planDetailStep  int                       // which step index the shown detail card is for
	planDetailGen   int                       // bumped each run; an in-flight explanation result from an older gen is dropped
	stepExplanation map[string]string         // model-written step write-ups, keyed by planStepExplanationKey, cached so re-clicking is instant
	subchat         subchatState
	altScreen       bool
	setup           setupState
	setupSave       func(SetupSelection) (SetupResult, error)
	// spinner animates the running-tool glyph in card heads. Its tick is started
	// with each run and stops itself once pending clears (the TickMsg is simply
	// not forwarded), so an idle UI schedules no timers.
	spinner spinner.Model
	// spinnerPhase advances once per spinner tick while a run is in flight and is
	// the shared animation clock for the cosine ripple on the working status line
	// (ripple.go). Reusing the spinner's existing tick keeps a single ~80ms timer
	// driving both the braille glyph and the colour wave — no second ticker.
	spinnerPhase int
	// spinnerTicking tracks whether the spinner's self-scheduling tick loop is
	// currently alive, so a kick (ensureSpinnerTick) never double-issues the tick
	// when the loop is already running. Set true whenever a Tick cmd is returned
	// from the TickMsg handler / beginRun, cleared when the handler stops the loop.
	spinnerTicking       bool
	pending              bool
	btwPending           bool // true while a /btw aside run is in flight
	btwDone              bool // /btw answer finished; the next empty-composer Enter acknowledges the return
	btwRunID             int  // run id of the in-flight /btw aside (0 = none); its events stay out of model context
	deferredEffortWarmup bool // warmup requested while run was active; fire after run completes
	// turnStartedAt is when the in-flight run began; the working status line
	// renders the live elapsed time from it so a long or stalled turn never looks
	// like a frozen terminal (for ANY provider, not just slow ones). @code = idle.
	turnStartedAt time.Time
	// prefillStatus holds the current prefill progress message (e.g. "prefilling context… 43%")
	// shown in the working status line. Empty when not prefilling.
	prefillStatus string
	// Keep numeric progress separately so the live counter does not look reset
	// while the model is processing input.
	prefillProcessed int
	prefillTotal     int
	prefillComplete  bool
	queuedMessage    string
	// steerInbox is the live-injection channel into the ACTIVE run: pressing
	// Enter on an already-queued prompt pushes it here, and the agent loop
	// drains it at the next turn boundary — the prompt joins the running
	// session instead of waiting for it to finish. Created per run in
	// beginRun; nil when idle.
	steerInbox *agent.SteerInbox
	exiting    bool
	// webMirror serves the live session to other devices (/share). Nil when off;
	// pointer state survives model copies so every Update sees the same server.
	webMirror *webmirror.Server

	// pong is the footer easter-egg game (see pong.go). Pure UI state — it
	// never touches the run pipeline.
	pong        pongGame
	runCancel   context.CancelFunc
	runID       int
	activeRunID int
	// flushRunIDs holds the ids of runs cancelled while still in flight, mapped
	// to the session they were recording into AT CANCEL TIME. Each cancelled
	// agent goroutine keeps running to completion and returns its accumulated
	// sessionEvents (including EventSessionCheckpoint payloads captured before
	// each mutating tool) in a final agentResponseMsg. activeRunID is already
	// zeroed by then, so without this the message would be dropped and the
	// checkpoint blobs already written to disk would be orphaned (breaking
	// /rewind). It is a MAP (not a single id) so a second cancel before the
	// first goroutine returns doesn't overwrite/lose the first run's pending
	// flush; the recorded session id keeps the late flush out of whatever
	// session is active by then (e.g. after /resume), which would otherwise
	// contaminate the new session's log with the old run's events. The
	// agentResponseMsg handler persists each such run's session events (only) so
	// the checkpoints stay referenced, then removes the id.
	flushRunIDs     map[int]string
	liveUsageCounts map[int]int
	// swarmSessionMap maps a swarm task id to its member's durable child session
	// id (carried up by swarm_collect's Meta), so the AGENTS sidebar rows can drill
	// into a member's conversation. Persists across turns; only completed members
	// have an entry.
	swarmSessionMap   map[string]string
	pendingPermission *pendingPermissionPrompt
	pendingAskUser    *pendingAskUserPrompt
	pendingSpecReview *pendingSpecReviewPrompt
	width             int
	height            int
	// hidePinnedPlan suppresses the pinned plan panel above the composer. Set on
	// the chat-column model copy in the two-column layout, where the plan is
	// surfaced in the context sidebar instead so it isn't shown twice.
	hidePinnedPlan bool
	// sidebarHidden is the user's Ctrl+S preference to collapse the right context
	// sidebar; when set, the chat reflows to full width. Distinct from the
	// availability conditions in sidebarAvailable (geometry / mode / overlays).
	sidebarHidden bool
	// sidebarOpen keeps an explicitly opened empty sidebar visible. Normally an
	// empty sidebar auto-hides, but Ctrl+S can show it before a plan, agent, file,
	// or browser event exists.
	sidebarOpen bool
	// selectedFile is the touched file selected by clicking its FILES sidebar
	// row: its edit cards tint in the chat (rowTouchesSelectedFile) and a second
	// click opens the drill-in file view. "" when nothing is selected; Esc clears.
	selectedFile string
	// fileView is the drill-in view for a touched file (file_view.go): while
	// active the chat column's body shows the file's diff/content instead of the
	// transcript, mirroring the subchat drill-in.
	fileView fileViewState
	// resetFiles contains files explicitly reset from the FILES surface. The
	// marker is persisted in the session so a resume does not resurrect a file
	// that has already been restored to its pre-session state.
	resetFiles map[string]bool
	// Git-sweep state (files_git_sweep.go): the startup snapshot of already-dirty
	// paths (nil until Init's sweep answers), the newly dirty files discovered by
	// live sweeps (bash/subagent mutations that carry no changedFiles), the
	// single-flight guard, and the "not a git repo / no git" latch.
	gitFileBaseline     map[string]bool
	gitTouched          []gitSweepFile
	gitSweepInFlight    bool
	gitSweepUnavailable bool
	// swarmDoneAt records when each swarm member was first seen finished (done/
	// failed) in a swarm_status report, so the sidebar can linger it briefly with a
	// fading ✓ before dropping it (a smooth exit, not an abrupt pop). Stamped in the
	// spinner tick; keyed by member id. Always non-nil (initialised in newModel).
	swarmDoneAt      map[string]time.Time
	now              func() time.Time
	chatScrollOffset int
	// browserState tracks what the agent-browser is doing for the browser preview panel.
	browserState browserState
	// safetyState logs permission approvals/denials for the sidebar SAFETY section.
	safetyState safetyState
	// chatBodyLines is the live body's line count at the last update; used to pin
	// the viewport (hold the read position) when content streams in while the user
	// has scrolled up. 0 means "at the bottom / not pinned".
	chatBodyLines int

	// Flush-frontier state (see flush.go). In inline mode, transcript[:flushed]
	// is already in native scrollback; in alt-screen mode this frontier stays
	// idle so history cannot reveal prior shell output.
	// flushedAny gates the first turn-separator blank line; flushQueue/
	// printInFlight serialize ordered scrollback prints; headerPrinted records
	// the one-time inline title-bar print at startup.
	flushed       int
	flushedAny    bool
	flushQueue    []string
	printInFlight bool
	headerPrinted bool

	// Composer input history (shell-style ↑/↓ recall of submitted inputs).
	// historyIdx == len(inputHistory) means "not navigating"; historyDraft
	// preserves whatever was typed before recall started.
	inputHistory []string
	historyIdx   int
	historyDraft string

	streamingText              string // live assistant text for the current segment
	streamingReasoning         string // live provider reasoning for the current segment
	streamingReasoningExpanded bool
	// turnStreamedRunes accumulates every reasoning+answer rune streamed in the
	// current turn so the working line can show a live, monotonic token estimate.
	// It is NOT reset at segment boundaries (where streamingText/Reasoning clear),
	// only at turn start (beginRun), so the count climbs across a multi-tool turn
	// instead of snapping back to zero after each tool call.
	turnStreamedRunes int
	// sessionStreamedRunes is the cumulative sum of turnStreamedRunes from all
	// completed turns this session. Used as a fallback eco-counter baseline when
	// the model backend does not emit usage events (e.g. llama-server strict-mode
	// drops stream_options/include_usage, so unpricedTokens stays 0).
	sessionStreamedRunes int
	// Streaming-text fade state. lineAges is keyed to LOGICAL lines of
	// streamingText (one entry per \n in the accumulated text), and
	// lastStreamActivity is the time of the most recent delta (used for
	// the in-progress last line — the one the model is currently typing
	// into). fadeActive is true from the first agentTextMsg of a run
	// until the matching agentResponseMsg, and gates both the per-line
	// fade application in interimBlock and the streamingFadeTick
	// re-render loop. The state is reset on stream end, on cancel, and
	// on terminal resize (where the visual line count may change and
	// per-line ages are no longer meaningful).
	lineAges           []time.Time
	lastStreamActivity time.Time
	fadeActive         bool
	fadeDisabled       bool // streaming fade off (SONUT_NO_FADE / SSH / tmux / low-color / reduced motion)
	reducedMotion      bool // SONUT_REDUCED_MOTION / no-TTY: static spinner glyph, no fade
	// In-progress tool call whose arguments are streaming (a file being written),
	// shown live by streamingToolCallView so a long write/edit isn't a frozen
	// spinner. Cleared when the call completes (next text/turn) — see updateModel.
	// streamCallDecoder decodes the streamed args incrementally (O(1) per delta).
	streamCallID      string
	streamCallName    string
	streamCallDecoder *streamingDecoder

	// Slash-command autocomplete (purely additive UI state). suggestions is the
	// live match list for the current "/token"; suggestionIdx is the highlighted
	// row. commandPaletteOpen keeps a zero-match command search active so invalid
	// query text stays in the palette instead of leaking into the composer.
	// filePaletteOpen does the same for a trailing "@token" file search.
	suggestions        []commandSuggestion
	suggestionIdx      int
	commandPaletteOpen bool
	filePaletteOpen    bool
	// suggestionsAreFiles is true when the overlay is showing "@file" matches
	// rather than "/command" matches, so completion inserts a path token instead
	// of replacing the whole input.
	suggestionsAreFiles bool
	// suggestionsAreSpecialists is true when the overlay is showing leading
	// "@specialist" matches; completion inserts "@name " and the submit path
	// expands the mention into a Task-delegation directive (launchPrompt).
	suggestionsAreSpecialists bool
	lastMouseSelection        mouseSelectionTarget
	mouseCapture              bool
	// mouseReleased, when true, forces terminal mouse capture OFF so the user can
	// drag-select and copy text natively (Ctrl+E toggles it). App mouse features
	// (clickable suggestions, right-click paste, transcript select) pause while on.
	mouseReleased       bool
	transcriptSelection transcriptSelectionState
	// hover identifies the single clickable row (if any) currently under the
	// mouse cursor with no button pressed, so it renders in a distinct style —
	// the visual cue that it's clickable. Requires AllMotion mouse reporting
	// (see wantsMouseCapture) since idle cursor movement carries no button.
	hover             hoverTarget
	copyStatus        string
	copyStatusSeq     int
	exitConfirmActive bool
	exitConfirmSeq    int
	// cancelConfirmActive/cancelConfirmSeq mirror exitConfirmActive/exitConfirmSeq
	// (same seq-gated tea.Tick pattern) but guard a DIFFERENT action: Esc
	// cancelling a running turn. The two are deliberately separate state (not a
	// shared flag) since they're different actions with different consequences
	// (quit the app vs. cancel the current run) that are armed by different
	// keys — Ctrl+C and Esc respectively.
	cancelConfirmActive bool
	cancelConfirmSeq    int
	// edgeScrollDelta drives the smooth-glide auto-scroll while a drag holds past
	// the transcript's top/bottom edge: 0 when idle, else the signed per-tick step
	// (matches transcriptEdgeScrollDelta's sign convention). A self-scheduling
	// tea.Tick chain (see dragEdgeScrollTickCmd) keeps stepping it at a fixed small
	// increment regardless of whether new raw mouse-motion events arrive — a
	// terminal only reports motion on actual cursor movement, so without a timer
	// the scroll would stop dead the instant the physical mouse holds still, even
	// while parked past the edge. edgeScrollSeq invalidates a stale in-flight tick
	// (mirroring exitConfirmSeq/copyStatusSeq) whenever the drag moves back into
	// the body, releases, or the chain is otherwise stopped.
	edgeScrollDelta int
	edgeScrollSeq   int
	// edgeScrollMouseX is the column the tick chain keeps extending the selection
	// at — captured from the triggering drag since a timer tick carries no mouse
	// position of its own.
	edgeScrollMouseX int

	// picker, when non-nil, is an open interactive selector overlay (/model,
	// /effort with no argument). It captures ↑/↓/Enter/Esc and applies
	// the chosen value through the existing command handlers.
	picker                       *commandPicker
	providerWizard               *providerWizardState
	mcpManager                   *mcpManagerState
	mcpAddWizard                 *mcpAddWizardState
	favoriteModels               map[string]bool
	recapsEnabled                bool         // post-turn "※ recap:" line (config: recaps on|off)
	recappedRuns                 map[int]bool // per-run guard so a recap fires at most once per turn
	modelPickerLoading           bool
	modelPickerLoadingProviderID string
	modelPickerLoadError         string
	// modelPickerLiveByProvider holds live-discovered models per provider (keyed by
	// catalog descriptor ID), so /model shows each provider's real current models —
	// the same list the provider-setup wizard discovers — not the static catalog.
	modelPickerLiveByProvider map[string][]providermodeldiscovery.Model
	// ollamaContextWindowByModel holds context-window sizes fetched from a local
	// Ollama daemon's native /api/show endpoint (keyed by model name), for
	// custom/local models that have no curated-catalog entry and whose
	// OpenAI-compatible /v1/models listing doesn't carry that metadata at all —
	// see modelContextWindow.
	ollamaContextWindowByModel map[string]int

	// pendingImages holds image attachments staged by /image for the next user
	// turn; pendingImageLabels are their display names (base(path)) for the chip
	// row. pendingImagePaths holds the absolute source path when known (empty
	// for clipboard). Cleared after submit or /image clear.
	pendingImages      []sonutruntime.ImageBlock
	pendingImageLabels []string
	pendingImagePaths  []string

	// pendingDocuments holds PDF text layers staged by /image for the next user
	// turn; the text is prepended to the prompt as a preamble at submit time and
	// the slice is cleared (or by /image clear). nil = no documents staged.
	pendingDocuments []pendingDocument

	// attachmentView is the Grok-style Format/Dimensions/Size/Path inspector
	// for staged attachments. Empty/inactive = normal chat.
	attachmentView attachmentViewState

	// captureRunImages, when set, is invoked with the images a run is launched
	// with. Nil in production; used by tests to assert image threading without a
	// real provider round-trip.
	captureRunImages func([]sonutruntime.ImageBlock)
}

type agentTextMsg struct {
	runID int
	delta string
}

type prefillProgressMsg struct {
	runID     int
	processed int
	total     int
}

type exitConfirmExpiredMsg struct {
	seq int
}

// cancelConfirmExpiredMsg mirrors exitConfirmExpiredMsg for the Esc
// cancel-a-run confirmation (see cancelConfirmActive).
type cancelConfirmExpiredMsg struct {
	seq int
}

// dragEdgeScrollTickMsg advances the smooth-glide auto-scroll one step (see
// edgeScrollDelta). seq must match m.edgeScrollSeq or the tick is stale (the
// drag moved back into the body, released, or was otherwise stopped since this
// tick was scheduled) and is silently dropped — the self-scheduling chain simply
// doesn't reschedule itself, so it terminates rather than ticking forever.
type dragEdgeScrollTickMsg struct {
	seq int
}

// toolCallStreamStartMsg / toolCallStreamDeltaMsg carry a tool call's live
// argument stream from the agent goroutine to the update loop, so a file being
// written renders as it streams (see streamingToolCallView).
type toolCallStreamStartMsg struct {
	runID int
	id    string
	name  string
}

type toolCallStreamDeltaMsg struct {
	runID    int
	id       string
	fragment string
}

type agentReasoningMsg struct {
	runID int
	delta string
}

type agentUsageMsg struct {
	runID   int
	modelID string
	usage   sonutruntime.Usage
}

type agentResponseMsg struct {
	runID         int
	rows          []transcriptRow
	usageEvents   []sonutruntime.Usage
	usageModelID  string
	sessionEvents []pendingSessionEvent
	specReview    *pendingSpecReviewPrompt
	err           error
	// Turn metadata for settled rows that do not otherwise carry it.
	turnTools   int
	turnElapsed time.Duration
}

type agentRowMsg struct {
	runID int
	row   transcriptRow
}

// planUpdateMsg carries a snapshot of plan items from the update_plan tool
// result callback to the live model. The callback runs on the agent goroutine
// and captures model by value, so it cannot mutate m.plan directly — it sends
// this message through the runtimeMessageSink instead.
type planUpdateMsg struct {
	runID         int
	items         []tools.PlanItem
	authoritative bool // planner output replaces the roadmap; tool updates may be partial
}

// planStepExplanationMsg carries the model's fresh, plain-English write-up of a
// clicked plan step back to the live model (the one-shot request runs on a
// goroutine via a tea.Cmd, so it can't mutate m directly). text is the written
// explanation; err is set when the request failed (the card then falls back to
// the local summary). key caches the result so re-clicking the step in the same
// state is instant; stepIndex re-renders the card in place when it's still open.
type planStepExplanationMsg struct {
	stepIndex int
	key       string
	gen       int // the planDetailGen when the request started; stale gens are ignored
	text      string
	err       error
}

// dataFlowMsg carries the external destinations a tool call sends data to
// (web hosts, MCP servers, search providers) from the OnToolCall callback to
// the live model, so the SAFETY sidebar can show WHERE session data flows —
// not just which tools were approved.
type dataFlowMsg struct {
	runID        int
	destinations []string
}

// specialistStartMsg carries specialist start info from the OnToolCall
// callback to the live model (same rationale as planUpdateMsg).
type specialistStartMsg struct {
	runID          int
	name           string
	description    string
	childSessionID string
}

// specialistCompleteMsg carries specialist completion info from the
// OnToolResult callback to the live model.
type specialistCompleteMsg struct {
	runID          int
	toolCallID     string
	childSessionID string
	status         specialistStatus
	errorMsg       string
}

// swarmSessionsMsg carries swarm task_id -> member session_id pairs (from
// swarm_collect's Meta) so the AGENTS sidebar rows can drill into a member's
// session like a specialist card.
type swarmSessionsMsg struct {
	runID    int
	sessions map[string]string
}

// specialistProgressMsg carries a live tool-call progress update from the
// specialist child process, sent via OnToolProgress → runtimeMessageSink.
type specialistProgressMsg struct {
	runID      int
	toolCallID string
	toolName   string
	detail     string
}

type mcpCommandOrigin int

const (
	mcpCommandOriginTranscript mcpCommandOrigin = iota
	mcpCommandOriginManager
	mcpCommandOriginWizard
)

type mcpCommandRequest struct {
	id              int
	origin          mcpCommandOrigin
	args            []string
	raw             string
	managerSelected int
	managerQuery    string
	wizardDisabled  bool
}

type mcpCommandResultMsg struct {
	request mcpCommandRequest
	result  MCPCommandResult
}

type doctorCommandResultMsg struct {
	id   int
	text string
}

type sandboxSetupCommandResultMsg struct {
	id     int
	result SandboxSetupCommandResult
}

type feedbackSubmittedMsg struct {
	err error
}

type prStateMsg struct {
	state PrState
}

type prWatcherStartedMsg struct {
	stop func()
}

type permissionDecision = agent.PermissionDecisionAction

const (
	permissionDecisionAllow             permissionDecision = agent.PermissionDecisionAllow
	permissionDecisionAllowStrict       permissionDecision = agent.PermissionDecisionAllowStrict
	permissionDecisionAllowForSession   permissionDecision = agent.PermissionDecisionAllowForSession
	permissionDecisionAllowPrefix       permissionDecision = agent.PermissionDecisionAllowPrefix
	permissionDecisionAlwaysAllowPrefix permissionDecision = agent.PermissionDecisionAlwaysAllowPrefix
	permissionDecisionDeny              permissionDecision = agent.PermissionDecisionDeny
	permissionDecisionAlwaysAllow       permissionDecision = agent.PermissionDecisionAlwaysAllow
	permissionDecisionCancel            permissionDecision = agent.PermissionDecisionCancel
)

type permissionRequestMsg struct {
	runID   int
	request agent.PermissionRequest
	decide  func(agent.PermissionDecision)
}

type pendingPermissionPrompt struct {
	request agent.PermissionRequest
	decide  func(agent.PermissionDecision)
	// cursor is the highlighted option index (into permissionOptions): 0 is the
	// resting approval choice. Moved by ↑/↓/Tab; confirmed by Enter or a click.
	// Hotkeys resolve the matching request-provided option directly.
	cursor int
}

// askUserRequestMsg is the TUI-loop equivalent of permissionRequestMsg: the
// agent goroutine sends it (via the runtime sink) and blocks until the model
// hands answers back through the answer callback.
type askUserRequestMsg struct {
	runID   int
	request agent.AskUserRequest
	answer  func([]string)
}

// interactionPromptTimeoutMsg closes a prompt after the agent's bounded
// 10-second wait. ToolCallID prevents a late timeout from dismissing a newer
// prompt in the same run.
type interactionPromptTimeoutMsg struct {
	runID      int
	toolCallID string
	kind       string
}

// pendingAskUserPrompt tracks an in-progress questionnaire rendered in the composer
// region as a row of tabs — one per question plus a trailing Confirm tab. Questions
// are answered in any order (Tab switches); the answer callback is invoked exactly
// once when the user submits on the Confirm tab or dismisses (Esc). active is the
// current tab (0..N-1 = questions, N = Confirm); states holds the per-question
// picker/free-text state and committed answer. See ask_user_prompt.go.
type pendingAskUserPrompt struct {
	request agent.AskUserRequest
	answer  func([]string)
	active  int
	states  []askUserAnswerState
}

type pendingSpecReviewPrompt struct {
	SpecID         string
	SpecTitle      string
	SpecFilePath   string
	RelativePath   string
	DraftSessionID string
}

type tuiAgentRunOptions struct {
	registry            *tools.Registry
	permissionMode      agent.PermissionMode
	systemPrompt        string
	specDraft           bool
	noTools             bool                          // disable all tools for a fast, direct response (used by /btw)
	reasoningEffort     modelregistry.ReasoningEffort // override reasoning effort; zero = use model default
	plannerPrompt       string                        // raw user request for the planner triviality gate (see agent.Options.PlannerPrompt)
	runMode             agent.RunMode                 // explicit quick-vs-builder workflow selected before inference
	executorOnly        bool                          // resume the existing planner roadmap without re-planning
	conversationHistory []sonutruntime.Message        // ordered prior session turns; current prompt is appended by agent.Run
	voiceOrigin         bool
	speakResponse       bool
	voiceUtteranceID    string
}

func newModel(ctx context.Context, options Options) model {
	if ctx == nil {
		ctx = context.Background()
	}

	cwd := options.Cwd
	if cwd == "" {
		if current, err := os.Getwd(); err == nil {
			cwd = current
		}
	}

	userConfigDir, _ := os.UserConfigDir()
	loadedUserCommands := usercommands.Load(usercommands.DefaultPaths(cwd, userConfigDir))

	registry := options.Registry
	if registry == nil {
		registry = options.AgentOptions.Registry
	}
	if registry == nil {
		registry = tools.NewRegistry()
	}
	sessionStore := options.SessionStore
	if sessionStore == nil {
		sessionStore = sessions.NewStore(sessions.StoreOptions{})
	}
	sandboxStore := options.SandboxStore
	usageTracker := options.UsageTracker
	if usageTracker == nil {
		usageTracker = usage.NewTracker(usage.TrackerOptions{})
	}
	prService := options.PrService
	if prService == nil {
		prService = NewPrService(cwd)
	}
	submitFeedback := options.SubmitFeedback
	if submitFeedback == nil {
		submitFeedback = userfeedback.Submit
	}
	doctorUserConfigPath := options.DoctorUserConfigPath
	if doctorUserConfigPath == "" {
		doctorUserConfigPath = options.UserConfigPath
	}

	permissionMode := options.PermissionMode
	if permissionMode == "" {
		permissionMode = options.AgentOptions.PermissionMode
	}
	if permissionMode == "" {
		permissionMode = agent.PermissionModeUnsafe
	}

	input := textinput.New()
	input.Prompt = "❯ "
	input.Placeholder = composerPlaceholder
	// Bubble's Ctrl+V binding reads the clipboard itself. Keep it disabled so
	// terminal bracketed paste (Paste: true) is the single paste path.
	input.KeyMap.Paste.SetEnabled(false)
	input.Focus()

	runSpinner := spinner.New(spinner.WithSpinner(spinner.MiniDot))

	// Default to bell + OSC-9 desktop notifications when the user hasn't
	// configured notify: with the unfocused-only focus policy this only fires
	// while the terminal is in the background, so it can't nag an active user.
	notifyMode := notify.Mode(strings.TrimSpace(options.Notify.Mode))
	if notifyMode == "" {
		notifyMode = notify.ModeBoth
	}
	notifier := notify.New(notifyTerminal, notify.Config{
		Mode:      notifyMode,
		FocusMode: notify.FocusMode(strings.TrimSpace(options.Notify.FocusMode)),
	})
	// Opt-in webhook fan-out (SONUT_NOTIFY_WEBHOOK_URL). Delivery failures stay
	// silent here: the TUI owns the alt-screen, so writing to stderr would
	// corrupt the display.
	notify.MaybeAddWebhookSink(notifier, os.Getenv, nil)
	notifier.SetFocused(true)

	m := model{
		ctx:                         ctx,
		cwd:                         cwd,
		swarmDoneAt:                 map[string]time.Time{},
		resetFiles:                  map[string]bool{},
		userCommands:                loadedUserCommands,
		composerCursorVisible:       true,
		userConfigPath:              options.UserConfigPath,
		doctorUserConfigPath:        doctorUserConfigPath,
		projectConfigPath:           options.ProjectConfigPath,
		savedProviders:              options.SavedProviders,
		gitBranch:                   gitBranch(cwd),
		providerName:                options.ProviderName,
		modelName:                   options.ModelName,
		llamaPort:                   options.LlamaPort,
		llamaVision:                 options.LlamaVision,
		providerProfile:             options.ProviderProfile,
		favoriteModels:              favoriteModelSet(options.FavoriteModels),
		recapsEnabled:               options.RecapsEnabled,
		provider:                    options.Provider,
		newProvider:                 options.NewProvider,
		probeProviderHealth:         options.ProbeProviderHealth,
		discoverProviderModels:      options.DiscoverProviderModels,
		discoverOllamaContextWindow: options.DiscoverOllamaContextWindow,
		registry:                    registry,
		sessionStore:                sessionStore,
		sandboxStore:                sandboxStore,
		mcpConfig:                   options.MCPConfig,
		mcpPermissionStore:          options.MCPPermissionStore,
		mcpTokenStore:               options.MCPTokenStore,
		mcpCommand:                  options.MCPCommand,
		sandboxSetupCommand:         options.SandboxSetupCommand,
		agentOptions:                options.AgentOptions,
		sessionCompactor:            options.SessionCompactor,
		runtimeMessageSink:          options.RuntimeMessageSink,
		permissionMode:              permissionMode,
		// Desktop default: visible Playwright/automation browser so the user
		// can watch work and interact (login, captcha). SSH/CI stay headless.
		browserHeadful:        localcontrol.BrowserHeadful(),
		networkEnabled:        true,
		reasoningEffort:       defaultReasoningEffort(options.ReasoningEffort),
		responseStyle:         defaultedResponseStyle(options.ResponseStyle),
		initialResumeID:       strings.TrimSpace(options.ResumeSessionID),
		themeMode:             resolveThemeMode(options.Theme, atcodeEnv("THEME"), options.SavedTheme),
		hasDarkBg:             true,
		userAgent:             options.UserAgent,
		usageTracker:          usageTracker,
		transcript:            initialTranscript(),
		transcriptBodyHeights: newTranscriptBodyHeightCache(defaultTranscriptBodyHeightCacheMaxEntries),
		prService:             prService,
		submitFeedback:        submitFeedback,
		prState:               prService.GetState(),
		input:                 input,
		spinner:               runSpinner,
		now:                   time.Now,
		browserState:          newBrowserState(),
		notifier:              notifier,
		altScreen:             options.AltScreen,
		liveUsageCounts:       map[int]int{},
		swarmSessionMap:       map[string]string{},
		setup:                 newSetupState(options.Setup),
		setupSave:             options.Setup.Save,
		nonstop:               false, // opt-in: the in-run planner/executor loop owns continuation
	}
	m.voice = newVoiceRuntime(options, options.RuntimeMessageSink)
	m.voiceRuns = map[int]string{}
	m.voiceBuilderRuns = map[int]bool{}
	if !m.voice.config.Enabled {
		m.voice.state = voicepkg.StateDisabled
	}
	// Apply an explicit theme immediately; auto stays on the dark default until
	// Init's terminal background probe resolves it (see Init / BackgroundColorMsg).
	if m.themeMode != themeAuto {
		applyTheme(m.themeMode, true)
	}
	m.reducedMotion = defaultReducedMotion()
	// The streaming-text fade (a lime→ink glow on freshly streamed lines) is
	// disabled: it read as a distracting glow rather than a subtle liveness cue.
	// Streaming text always renders statically at base ink (the disabled path in
	// styleStreamingLine), so no accent glow and no per-line fade ticks.
	m.fadeDisabled = true
	m.refreshMCPViewState()
	return m
}

func (m model) doctorOptions(connectivity bool) doctor.Options {
	var local *doctor.LocalRuntime
	if m.llamaPort > 0 && !config.HasProviderProfile(m.providerProfile) {
		local = doctor.DetectLocalRuntime(connectivity)
	}
	var health *providerhealth.Result
	if connectivity && m.probeProviderHealth != nil && config.HasProviderProfile(m.providerProfile) {
		ctx := m.ctx
		if ctx == nil {
			ctx = context.Background()
		}
		result := m.probeProviderHealth(ctx, providerhealth.Options{
			Profile:      m.providerProfile,
			Connectivity: true,
			UserAgent:    m.userAgent,
		})
		health = &result
	}

	return doctor.Options{
		Now:            m.now,
		Runtime:        "go",
		UserConfig:     m.doctorUserConfigPath,
		ProjectConfig:  m.projectConfigPath,
		Provider:       m.providerProfile,
		WorkspaceRoot:  m.cwd,
		Connectivity:   connectivity,
		ProviderHealth: health,
		LocalRuntime:   local,
	}
}

const (
	composerPlaceholder     = "describe a task for @code…"
	composerMaxVisibleLines = 4
)

// composerCursorBlinkInterval is the on/off period of the composer text cursor.
const composerCursorBlinkInterval = 530 * time.Millisecond

// composerBlinkMsg toggles the composer cursor's visibility each tick. The custom
// composer render draws its own cursor (not textinput's), so it drives its own
// blink rather than relying on textinput.Blink.
type composerBlinkMsg struct{}

func composerBlinkCmd() tea.Cmd {
	return tea.Tick(composerCursorBlinkInterval, func(time.Time) tea.Msg {
		return composerBlinkMsg{}
	})
}

func (m model) Init() tea.Cmd {
	cmds := []tea.Cmd{textinput.Blink, composerBlinkCmd()}
	if m.voice != nil && m.voice.config.Enabled {
		cmds = append(cmds, m.voiceStartCmd())
	}
	// KV cache priming starts at the engine-ready startup transition and runs
	// behind the loading-screen/TUI handoff. Running kvWarmupCmd() here would
	// start a duplicate request and overwrite the slot's cached state.
	// Bubble Tea documents an initial WindowSizeMsg as delivered automatically
	// on program start, so m.height/m.width are normally set before the first
	// render. But that's the terminal proactively pushing a size — if it's
	// ever missed (a slow/unusual terminal, a multiplexer, a startup race),
	// nothing else ever asks again: m.height stays its zero value forever,
	// `if m.altScreen && m.height > 0` (transcriptView) falls back to the
	// unpadded, non-fullscreen render path for the rest of the session, and
	// the alt-screen viewport never gets filled below the actual content.
	// Explicitly requesting it here means @code doesn't depend solely on the
	// terminal's unprompted push — mirrors the RequestBackgroundColor request
	// below for the same reason.
	cmds = append(cmds, tea.RequestWindowSize)
	// The startup llamaVision flag is a pre-server snapshot (see options.go). Now
	// that the local server is up, re-probe its ACTUAL loaded modalities so a
	// projector that really loaded lets images through. Upgrade-only: skip when
	// vision is already granted, and the handler never revokes it.
	if !m.llamaVision && m.isLlamaProvider() && m.llamaPort > 0 {
		cmds = append(cmds, probeLlamaVisionCmd(m.llamaPort))
	}
	// Ensure the workspace is a git repo (auto-init if not), then run the
	// baseline git sweep. The init is fast and idempotent for existing repos.
	if strings.TrimSpace(m.cwd) != "" {
		cwd := m.cwd
		ctx := m.ctx
		cmds = append(cmds, func() tea.Msg {
			sonutgit.EnsureRepo(ctx, cwd) //nolint:errcheck — best effort
			// Sweep expired TTL trash entries on startup.
			sonuttrash.Sweep(cwd)
			return nil
		})
		cmds = append(cmds, gitSweepCmd(m.ctx, m.cwd, true))
	}
	// In auto mode, ask the terminal for its background color; the reply arrives
	// as tea.BackgroundColorMsg and selects light vs dark (see updateModel).
	if m.themeMode == themeAuto {
		cmds = append(cmds, tea.RequestBackgroundColor)
	}
	// Warm model discovery for the active provider in the background so the
	// context-usage gauge (used / total tokens + % fill) knows the active model's
	// window from launch — including proxy/custom models not in the curated
	// registry. Async: never blocks startup; if discovery is unavailable the gauge
	// just shows the used-token count until the window is otherwise learned.
	if descriptor, ok := m.activeProviderDescriptor(); ok {
		if cmd := m.modelPickerProviderDiscoveryCmd(descriptor, m.providerProfile); cmd != nil {
			cmds = append(cmds, cmd)
		}
		// The generic discovery above has no source for a local Ollama model's
		// context window (see ollamaContextWindowDiscoveryCmd); probe its
		// native /api/show separately so the gauge works for custom/local
		// Ollama models too, not just ones in the curated catalog.
		if cmd := m.ollamaContextWindowDiscoveryCmd(descriptor, m.providerProfile.BaseURL, m.modelName); cmd != nil {
			cmds = append(cmds, cmd)
		}
	}
	if m.prService != nil && m.runtimeMessageSink != nil {
		service := m.prService
		sink := m.runtimeMessageSink
		ctx := m.ctx
		if ctx == nil {
			ctx = context.Background()
		}
		cmds = append(cmds, func() tea.Msg {
			stop := WatchPRStateContext(ctx, service, func(state PrState) {
				sink(prStateMsg{state: state})
			})
			return prWatcherStartedMsg{stop: stop}
		})
	}
	// --resume flag: auto-trigger /resume <id> on startup so the user lands
	// directly in the named session instead of a blank new one.
	if m.initialResumeID != "" {
		id := m.initialResumeID
		cmds = append(cmds, func() tea.Msg {
			return parsedCommand{kind: commandResume, text: id}
		})
	}
	return tea.Batch(cmds...)
}

func (m *model) stopPRWatcher() {
	if m.prWatcherStop == nil {
		return
	}
	m.prWatcherStop()
	m.prWatcherStop = nil
}

// noBlockingModal reports that no modal surface (permission prompt, ask_user,
// spec review, provider/MCP wizard, MCP manager, or picker) is up, so a global
// shortcut may act instead of falling through to a modal's own handler. Shared
// by every shortcut that should defer to whichever modal is focused.
func (m model) noBlockingModal() bool {
	return m.pendingPermission == nil && m.pendingAskUser == nil && m.pendingSpecReview == nil &&
		m.providerWizard == nil && m.mcpAddWizard == nil && m.mcpManager == nil && m.picker == nil &&
		!m.secView.active && !m.voiceView.active && !m.knowledgeView.active
}

func (m model) quit() (tea.Model, tea.Cmd) {
	m.stopPRWatcher()
	if m.voice != nil && m.voice.manager != nil {
		m.voice.manager.Close()
	}
	m.stopAllBackgroundTerminalSessions()
	if m.webMirror != nil {
		m.webMirror.Close()
	}
	// Reap the local atcode engine we launched/adopted this session so closing
	// the TUI does not leave a multi-GB llama-server orphaned in the background.
	llama.StopOwnedServers()
	return m, tea.Quit
}

func (m model) handleCtrlC() (tea.Model, tea.Cmd) {
	if !m.pending && m.composerValue() != "" && m.noBlockingModal() && !m.transcriptDetailed && !m.subchat.active {
		m.clearComposer()
		m.clearSuggestions()
		m = m.disarmExitConfirmation()
		return m, nil
	}
	if m.exitConfirmActive {
		m = m.disarmExitConfirmation()
		m.cancelRun()
		m.exiting = true
		// A cancelled run may still need to flush checkpoint/session events; quit
		// only after agentResponseMsg drains flushRunIDs so /rewind stays valid.
		if len(m.flushRunIDs) > 0 {
			return m, nil
		}
		return m.quit()
	}
	m.cancelRun()
	m.exitConfirmActive = true
	m.exitConfirmSeq++
	seq := m.exitConfirmSeq
	return m, tea.Tick(ctrlCExitConfirmDuration, func(time.Time) tea.Msg {
		return exitConfirmExpiredMsg{seq: seq}
	})
}

func (m model) disarmExitConfirmation() model {
	if m.exitConfirmActive {
		m.exitConfirmActive = false
		m.exitConfirmSeq++
	}
	return m
}

func (m model) disarmCancelConfirmation() model {
	if m.cancelConfirmActive {
		m.cancelConfirmActive = false
		m.cancelConfirmSeq++
	}
	return m
}

// Update routes every message through updateModel, then advances the flush
// frontier for inline rendering. Alt-screen runs keep rows in the managed view
// instead of printing into terminal scrollback (see flush.go).
func (m model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	if _, ok := msg.(flushedMsg); ok {
		m.printInFlight = false
		return m.drainFlushQueue()
	}
	next, cmd := m.updateModel(msg)
	nm, ok := next.(model)
	if !ok {
		return next, cmd
	}
	nm = nm.syncChatScroll()
	nm, mouseCmd := nm.syncMouseCapture()
	nm, flushCmd := nm.settleTranscript()
	return nm, batchCommands(cmd, mouseCmd, flushCmd)
}

func batchCommands(cmds ...tea.Cmd) tea.Cmd {
	filtered := make([]tea.Cmd, 0, len(cmds))
	for _, cmd := range cmds {
		if cmd != nil {
			filtered = append(filtered, cmd)
		}
	}
	switch len(filtered) {
	case 0:
		return nil
	case 1:
		return filtered[0]
	default:
		return tea.Batch(filtered...)
	}
}

func (m model) updateModel(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case composerBlinkMsg:
		m.composerCursorVisible = !m.composerCursorVisible
		if !m.reducedMotion && m.transcriptEmpty() {
			m.ambientPhase++
		}
		if !m.reducedMotion && m.secView.active && m.secView.tab == secTabAtlas && !m.secView.atlasPaused {
			m.secView.atlasFrame++
		}
		m = m.applyKnowledgeFrame()
		return m, composerBlinkCmd()
	case terminalBrowserOpenedMsg:
		// terminal-browser is an optional visual surface. Keep the TUI browser
		// preview usable when the host terminal does not support splits or the
		// CLI is not on PATH; do not surface the helper's stderr in the TUI.
		if msg.err != nil {
			m.browserState.lastAction = "terminal-browser unavailable"
		} else if msg.opened {
			m.terminalBrowserActive = true
		}
		return m, nil
	case llamaVisionProbedMsg:
		// Upgrade-only: the live /props probe can only GRANT vision the startup
		// snapshot missed, never revoke a capability already in effect.
		if msg.vision {
			m.llamaVision = true
		}
		return m, nil
	case tea.BackgroundColorMsg:
		// Terminal background-color reply (from Init's RequestBackgroundColor). In
		// auto mode it selects light vs dark; applyTheme repaints (clears the render
		// cache). An explicit dark/light theme ignores it but still records the bg.
		m.hasDarkBg = msg.IsDark()
		if m.themeMode == themeAuto {
			applyTheme(themeAuto, m.hasDarkBg)
		}
		return m, nil
	case tea.MouseMsg:
		if m.setup.visible {
			return m.handleSetupMouse(msg)
		}
		if m.voiceView.active {
			if next, cmd, handled := m.handleVoiceViewMouse(msg); handled {
				return next, cmd
			}
		}
		return m.handleMouse(msg)
	case transcriptCopiedMsg:
		m.copyStatusSeq++
		if msg.err != nil {
			// Keep the selection so the user can retry; just surface the failure.
			m.copyStatus = "Copy failed"
		} else {
			m.transcriptSelection = transcriptSelectionState{}
			m.copyStatus = "Copied!"
		}
		seq := m.copyStatusSeq
		return m, tea.Tick(2*time.Second, func(time.Time) tea.Msg {
			return transcriptCopyStatusExpiredMsg{seq: seq}
		})
	case transcriptCopyStatusExpiredMsg:
		if msg.seq == m.copyStatusSeq {
			m.copyStatus = ""
		}
		return m, nil
	case exitConfirmExpiredMsg:
		if msg.seq == m.exitConfirmSeq {
			m.exitConfirmActive = false
		}
		return m, nil
	case cancelConfirmExpiredMsg:
		if msg.seq == m.cancelConfirmSeq {
			m.cancelConfirmActive = false
		}
		return m, nil
	case dragEdgeScrollTickMsg:
		if msg.seq != m.edgeScrollSeq || m.edgeScrollDelta == 0 || !m.transcriptSelection.active {
			return m, nil // stale, or the chain was stopped since this tick was scheduled
		}
		m = m.dragToEdgeScroll(m.edgeScrollDelta, m.edgeScrollMouseX)
		return m, dragEdgeScrollTickCmd(m.edgeScrollSeq)
	case providerWizardOAuthMsg:
		return m.applyProviderWizardOAuth(msg)
	case providerWizardDeviceCodeMsg:
		return m.applyProviderWizardDeviceCode(msg)
	case secTransitionTickMsg:
		if !m.secView.active || !m.secView.transitioning || msg.seq != m.secView.transitionSeq {
			return m, nil
		}
		if m.reducedMotion {
			return m, nil
		}
		m.secView.transitionFrame++
		return m, secTransitionTickCmd(msg.seq)
	case secScanResultMsg:
		return m.applySecScanResult(msg), nil
	case secProcessResultMsg:
		return m.applySecProcessResult(msg), nil
	case knowledgeLoadedMsg:
		return m.applyKnowledgeLoaded(msg), nil
	case knowledgeWebSearchMsg:
		return m.applyKnowledgeWebSearch(msg), nil
	case knowledgeWebFetchMsg:
		return m.applyKnowledgeWebFetch(msg), nil
	case knowledgeSaveMsg:
		return m.applyKnowledgeSave(msg)
	case knowledgeTrashMsg:
		return m.applyKnowledgeTrash(msg)
	case secAdvisorResultMsg:
		return m.applySecAdvisorResult(msg), nil
	case clipboardReadMsg:
		// Result of a right-click paste. Insert on success; surface a brief
		// status if the clipboard couldn't be read (e.g. no clipboard utility on
		// a remote session). An empty clipboard is a silent no-op.
		if msg.err != nil {
			m.copyStatusSeq++
			m.copyStatus = "Paste failed"
			seq := m.copyStatusSeq
			return m, tea.Tick(2*time.Second, func(time.Time) tea.Msg {
				return transcriptCopyStatusExpiredMsg{seq: seq}
			})
		}
		if msg.content == "" {
			// Empty text clipboard — may be a screenshot. Probe for image.
			return m, readClipboardImageCmd()
		}
		return m.routePaste(msg.content)
	case clipboardImageMsg:
		if msg.err != nil {
			return m.appendImageNotice("Clipboard image read failed: " + msg.err.Error()), nil
		}
		if msg.data == nil {
			return m, nil // no image — silent no-op
		}
		return m.attachClipboardImage(msg.data, msg.mediaType), nil
	case tea.KeyboardEnhancementsMsg:
		m.voiceKeyboardEnhancements(msg)
		return m, nil
	case voiceStartedMsg:
		if msg.err != nil {
			if m.voice != nil {
				m.voice.state = voicepkg.StateError
				m.voice.err = msg.err.Error()
				if errors.Is(msg.err, voicepkg.ErrVoiceDisabled) {
					m.voice.state = voicepkg.StateDisabled
				}
			}
			return m, nil
		}
		return m, nil
	case voiceSetupProgressMsg:
		if m.voice != nil {
			percent := 0
			if msg.total > 0 {
				percent = int(msg.received * 100 / msg.total)
			}
			m.voice.setupStatus = fmt.Sprintf("Installing voice · %s · %d%%", msg.artifact, percent)
			if m.voiceView.active {
				m.voiceView.tab = voiceViewSetup
				m.voiceView.notice = m.voice.setupStatus
			}
		}
		return m, nil
	case voiceSetupDoneMsg:
		if m.voice == nil {
			return m, nil
		}
		m.voice.setupCancel = nil
		m.voice.setupStatus = ""
		if msg.err != nil {
			if errors.Is(msg.err, context.Canceled) {
				m.voice.state = voicepkg.StateUninstalled
				if m.voiceView.active {
					m.voiceView.tab = voiceViewSetup
					m.voiceView.notice = "Voice setup cancelled. Partial downloads remain resumable."
				}
				return m, nil
			}
			m.voice.state = voicepkg.StateError
			m.voice.err = msg.err.Error()
			if m.voiceView.active {
				m.voiceView.tab = voiceViewSetup
				m.voiceView.notice = "Voice setup failed: " + msg.err.Error()
			}
			return m, nil
		}
		m.voice.err = ""
		m.voice.state = voicepkg.StateUninstalled
		if m.voiceView.active {
			m.voiceView.tab = voiceViewSetup
			m.voiceView.notice = "Voice models installed. The helper is restarting."
		}
		if m.voice.manager != nil && m.voice.manager.Started() {
			if err := m.voice.manager.Reload(); err != nil {
				m.voice.state = voicepkg.StateError
				m.voice.err = err.Error()
				if m.voiceView.active {
					m.voiceView.notice = "Models installed, but the helper could not reload: " + err.Error()
				}
			}
			return m, nil
		}
		return m, m.voiceStartCmd()
	case voiceEventMsg:
		return m.handleVoiceEvent(msg.event)
	case tea.PasteMsg:
		if m.voiceView.active {
			return m, nil
		}
		if m.voiceManualPasteActive() {
			m = m.voiceCancel(true)
		}
		return m.routePaste(msg.Content)
	case webPromptMsg:
		// A prompt typed on a remote device (/share phone mirror): dispatched
		// through the same guards as locally typed input, composer untouched.
		return m.handleWebPrompt(msg.text)
	case pongTickMsg:
		// Stale ticks (from before a pause/restart) are dropped by seq so the
		// chain can never double up.
		if msg.seq != m.pong.seq || !m.pong.active || m.pong.paused {
			return m, nil
		}
		if m.pong.step() {
			return m, m.pong.tickCmd()
		}
		return m, nil // rally lost — board stays up, ticking stops
	case webStopMsg:
		// Stop pressed on a remote device: cancel the in-flight run, same as
		// Esc in the terminal. No-op when idle.
		if m.pending {
			m.cancelRun()
		}
		return m, nil
	case tea.KeyPressMsg:
		if m.setup.visible {
			return m.handleSetupKey(msg)
		}
		m.transcriptSelection = transcriptSelectionState{}
		m.composerSelection = composerSelectionState{}
		m.clearMouseSelection()
		if !keyCtrl(msg, 'c') {
			m = m.disarmExitConfirmation()
		}
		// Mirrors the exit-confirmation reset above: any key that isn't itself
		// the confirming Esc means the user moved on to something else, so a
		// later, unrelated Esc must arm a fresh confirmation rather than
		// silently cancel off a stale press from seconds ago.
		if !keyIs(msg, tea.KeyEsc) {
			m = m.disarmCancelConfirmation()
		}
		// The `?` help overlay is modal: `?`, Esc, q, or Enter close it; every
		// other key is swallowed so nothing types into the hidden composer.
		if m.helpOverlay {
			if keyText(msg) == "?" || keyText(msg) == "q" || keyIs(msg, tea.KeyEsc) || keyIs(msg, tea.KeyEnter) || keyCtrl(msg, 'c') {
				m.helpOverlay = false
			}
			return m, nil
		}
		if m.secView.active {
			if next, cmd, handled := m.handleSecViewKey(msg); handled {
				return next, cmd
			}
		}
		if m.knowledgeView.active {
			if next, cmd, handled := m.handleKnowledgeViewKey(msg); handled {
				return next, cmd
			}
		}
		if m.voiceView.active {
			if next, cmd, handled := m.handleVoiceViewKey(msg); handled {
				return next, cmd
			} else {
				// A few Voice keys intentionally close the workspace and fall
				// through to the normal composer flow (notably `/`). Preserve
				// that returned model before continuing with global handling.
				m = next
				if cmd != nil {
					return m, cmd
				}
			}
		}
		if next, cmd, handled := m.handleVoiceKey(msg); handled {
			return next, cmd
		}
		if m.voiceManualTyping(msg) {
			m = m.voiceCancel(true)
		}
		// Pong easter egg (pong.go): intercepts only keys the game surface owns
		// (↑/↓/space/esc while playing, space-on-empty-composer to start);
		// everything else falls through to normal handling untouched.
		if handled, cmd := m.handlePongKey(msg); handled {
			return m, cmd
		}
		switch {
		case keyCtrl(msg, 'c'):
			return m.handleCtrlC()
		case keyCtrl(msg, 'o'):
			return m.toggleReasoningOrTranscript(), nil
		case m.fileView.active && m.noBlockingModal() && m.composerValue() == "" && (keyText(msg) == "r" || keyText(msg) == "R"):
			path := m.fileView.path
			next, text := m.resetTouchedFile(path)
			next.transcript = reduceTranscript(next.transcript, transcriptAction{kind: actionAppendSystem, text: text})
			return next, nil
		case m.fileView.active && m.noBlockingModal() && m.composerValue() == "" && (keyText(msg) == "d" || keyText(msg) == "f"):
			// Mode toggle for the file drill-in, only while the composer is empty
			// (so mid-sentence typing is never hijacked) and no modal is up (so a
			// permission prompt / ask-user / wizard keeps its own key handling).
			if keyText(msg) == "f" {
				return m.setFileViewMode(fileViewFull), nil
			}
			return m.setFileViewMode(fileViewDiff), nil
		case m.attachmentView.active && m.noBlockingModal() && m.composerValue() == "":
			// Attachment inspector keys (empty composer only).
			switch {
			case keyIs(msg, tea.KeyLeft):
				return m.cycleAttachmentView(-1), nil
			case keyIs(msg, tea.KeyRight):
				return m.cycleAttachmentView(1), nil
			case keyText(msg) == "x" || keyText(msg) == "X":
				return m.removeActiveAttachment(), nil
			case keyText(msg) == "o" || keyText(msg) == "O":
				return m.openActiveAttachmentInSystem(), nil
			}
		case keyCtrl(msg, 'e'):
			// Release/recapture the mouse so the user can drag-select and copy text
			// natively (mouse capture otherwise intercepts terminal selection).
			m.mouseReleased = !m.mouseReleased
			if m.mouseReleased {
				return m.appendSystemNotice("Mouse released — drag to select and copy text. Press Ctrl+E again to re-enable mouse interaction (clicks, right-click paste)."), nil
			}
			return m.appendSystemNotice("Mouse interaction re-enabled."), nil
		case keyIs(msg, tea.KeyEsc):
			// Esc is heavily overloaded below (subchat exit, MCP cancel, ask-user,
			// permission deny, wizard/picker/suggestions dismiss, ...) before ever
			// reaching the run-cancel fallback. Capture whether this press really
			// is the confirming second Esc BEFORE any of those branches can fire,
			// then disarm unconditionally: an Esc that gets consumed by one of
			// them wasn't a confirm, so it must not leave cancelConfirmActive
			// armed for some later, unrelated Esc to silently act on.
			wasConfirmingCancel := m.pending && m.cancelConfirmActive
			m = m.disarmCancelConfirmation()
			// Subchat view exits on Esc (returns to main chat).
			if m.subchat.active {
				m.chatScrollOffset = m.subchat.exit()
				m = m.clearHover() // bodyY numbering differs between subchat and the parent transcript
				return m, nil
			}
			// File drill-in exits on Esc (returns to the chat at its saved scroll
			// position); the file stays selected so a second Esc clears that. Only
			// with no blocking modal up: Esc on a permission prompt / ask-user /
			// wizard must reach THAT surface's deny/cancel handling below, not
			// silently close the drill-in behind it.
			if m.fileView.active && m.noBlockingModal() {
				return m.exitFileView(), nil
			}
			if m.attachmentView.active && m.noBlockingModal() {
				return m.exitAttachmentView(), nil
			}
			if m.mcpCommandCancel != nil {
				m.cancelMCPCommand()
				if m.mcpAddWizard != nil {
					m.mcpAddWizard.result = mcpAddWizardResult{Title: "MCP setup cancelled", State: "cancelled", Message: "MCP action was cancelled.", ActionHint: "Edit config"}
					m.mcpAddWizard.step = mcpAddWizardStepResult
					return m, nil
				}
				m.transcript = appendTranscriptRow(m.transcript, transcriptRow{kind: rowSystem, tool: "mcp", text: "MCP action cancelled"})
				return m, nil
			}
			if m.transcriptDetailed {
				m.transcriptDetailed = false
				return m, nil
			}
			// Esc on an ask-user prompt: from the "type my own" free-text it steps
			// back to the selector for that question; otherwise it cancels the
			// questionnaire (not the run), delivering whatever answers were collected
			// so the agent loop unblocks and degrades to its best-assumption path.
			if m.pendingAskUser != nil {
				return m.escapeAskUser()
			}
			if m.pendingSpecReview != nil {
				return m.cancelSpecReview()
			}
			if m.pendingPermission != nil && m.pendingPermission.request.ToolName == tools.RequestPermissionsToolName {
				return m.resolvePermission(permissionDecisionDeny)
			}
			if m.providerWizard != nil {
				m.providerWizard = nil
				return m, nil
			}
			if m.mcpAddWizard != nil {
				m.mcpAddWizard = nil
				return m, nil
			}
			if m.mcpManager != nil {
				m.mcpManager = nil
				return m, nil
			}
			// An open picker cancels first; then an active suggestion overlay is
			// dismissed. Neither cancels the run or clears the input.
			if m.picker != nil {
				if m.picker.kind == pickerModel {
					m.clearModelPickerLoadState()
				}
				if m.picker.kind == pickerTheme {
					// A live theme preview was applied while navigating; restore the
					// committed palette since Esc dismisses without choosing.
					m.restoreCommittedTheme()
				}
				m.picker = nil
				return m, nil
			}
			if m.suggestionsActive() {
				return m.dismissSuggestions(), nil
			}
			// A selected FILES row clears before anything run-related: the
			// selection is a passive highlight, so Esc dropping it is cheap and
			// expected (mirrors how editors clear selection on Esc).
			if m.selectedFile != "" {
				m.selectedFile = ""
				return m, nil
			}
			if m.hasQueuedMessage() {
				return m.clearQueuedMessage(), nil
			}
			// Vim mode: with every modal surface above already given its shot,
			// Esc in INSERT mode switches to NORMAL instead of falling through
			// to the run-cancel / clear-composer fallback. In NORMAL mode Esc
			// falls through so cancelling a run still works.
			if m.vimMode.enabled && m.noBlockingModal() && m.vimMode.mode == vimModeINSERT {
				m.vimMode.mode = vimModeNORMAL
				m.vimMode.pendingKey = 0
				return m, nil
			}
			m.clearSuggestions()
			if m.pending {
				if wasConfirmingCancel {
					m.clearComposer()
					m.cancelRun()
					return m, nil
				}
				// First Esc only arms the confirmation — preserve whatever
				// draft the user has typed, since nothing has actually been
				// cancelled yet and they may not press Esc again.
				m.cancelConfirmActive = true
				m.cancelConfirmSeq++
				seq := m.cancelConfirmSeq
				return m, tea.Tick(escCancelConfirmDuration, func(time.Time) tea.Msg {
					return cancelConfirmExpiredMsg{seq: seq}
				})
			}
			m.clearComposer()
			return m, nil
		case keyIs(msg, tea.KeyEnter):
			if m.transcriptDetailed {
				if command := parseCommand(m.input.Value()); command.kind == commandTranscript {
					m.input.SetValue("")
					return m.toggleDetailedTranscript(), nil
				}
				return m, nil
			}
			if m.pendingPermission != nil {
				// Enter confirms the highlighted option (default: allow once); the
				// a/y/d hotkeys and a click still resolve directly.
				return m.confirmPermissionCursor()
			}
			if m.pendingAskUser != nil {
				return m.confirmAskUser()
			}
			if m.pendingSpecReview != nil {
				return m, nil
			}
			if m.providerWizard != nil {
				return m.handleProviderWizardKey(msg)
			}
			if m.mcpAddWizard != nil {
				return m.handleMCPAddWizardKey(msg)
			}
			if m.mcpManager != nil {
				return m.handleMCPManagerKey(msg)
			}
			if m.picker != nil {
				return m.choosePicker()
			}
			if keyAlt(msg) {
				if next, ok := m.applyComposerKey(msg); ok {
					return next, nil
				}
			}
			// Enter on file suggestions inserts the @file token for continued
			// composing. Command suggestions execute only when the selected command
			// is self-contained; commands that require a value are inserted so the
			// user can finish the argument first.
			if m.suggestionsActive() {
				return m.chooseSuggestion()
			}
			// An empty-composer Enter after a /btw aside is the promised "return to
			// the main conversation" — acknowledge it visibly. A typed message
			// clears the flag in handleSubmit instead (submitting IS returning).
			if m.btwDone && strings.TrimSpace(m.composerValue()) == "" {
				m.btwDone = false
				m.transcript = reduceTranscript(m.transcript, transcriptAction{
					kind: actionAppendSystem,
					text: "── back to the main conversation ──",
				})
				return m, nil
			}
			return m.handleSubmit()
		case keyIs(msg, tea.KeyTab) && keyShift(msg):
			if m.transcriptDetailed {
				return m, nil
			}
			if m.pendingPermission != nil {
				return m.movePermissionCursor(-1), nil
			}
			if m.pendingAskUser != nil {
				return m.moveAskUserTab(-1), nil
			}
			// shift+tab toggles the permission mode between Auto and Ask (Unsafe
			// is intentionally not reachable by a casual keypress — see
			// nextPermissionMode), but only when nothing modal is up: a permission
			// prompt, ask_user questionnaire, or open picker all take precedence
			// and let the key fall through to their own handlers below.
			if m.noBlockingModal() {
				m.permissionMode = nextPermissionMode(m.permissionMode)
				return m, nil
			}
		case keyCtrl(msg, 't'):
			if m.transcriptDetailed {
				return m, nil
			}
			// Ctrl+T cycles reasoning effort (auto -> low ->
			// medium -> high -> auto), but only when nothing modal is up — the
			// same gate shift+tab uses above. Not gated on m.pending: cycling
			// mid-run is allowed and takes effect on the next turn, matching
			// /effort. cycleReasoningEffort is a silent no-op on models with no
			// effort controls.
			if m.noBlockingModal() {
				before := m.reasoningEffort
				next, cmd := m.cycleReasoningEffort()
				if next.reasoningEffort != before {
					if next.pending {
						next.deferredEffortWarmup = true
						return next, cmd
					}
					return next, tea.Batch(cmd, next.warmupEffortCmd())
				}
				return next, cmd
			}
		case keyCtrl(msg, 'p'):
			// Ctrl+P toggles the plan panel expansion (collapse/expand step list).
			if m.noBlockingModal() && !m.plan.isEmpty() {
				m.plan.expanded = !m.plan.expanded
				return m, nil
			}
		case keyCtrl(msg, 's'):
			// Ctrl+S collapses / restores the right context sidebar. Only acts when
			// nothing modal is active. It must be consumed even when the sidebar is
			// temporarily unavailable; otherwise it falls through to composer input.
			// It can explicitly open an otherwise empty sidebar. Hiding reflows the
			// chat to full width, so mirror the width-change bookkeeping (re-wrap the
			// streaming fade, resize the composer) the WindowSizeMsg path does.
			if m.noBlockingModal() {
				if !m.sidebarAvailable() {
					return m, nil
				}
				// Just show/hide — no transcript notice. The reflow IS the feedback,
				// and emitting a line every toggle piled up noise in the chat.
				if m.sidebarActive() {
					m.sidebarHidden = true
					m.sidebarOpen = false
				} else {
					m.sidebarHidden = false
					m.sidebarOpen = true
				}
				m.lineAges = nil
				m.input.SetWidth(maxInt(20, m.chatColumnWidth()-14))
				return m, nil
			}
		case keyCtrl(msg, 'f'):
			if m.picker != nil && m.picker.kind == pickerModel {
				if m.modelPickerIsLoading() {
					return m, nil
				}
				return m.toggleModelFavorite(), nil
			}
		case keyText(msg) == "d" && !keyAlt(msg) && !keyHasMod(msg, tea.ModCtrl):
			if m.picker != nil && m.picker.kind == pickerHistory {
				if item, ok := m.picker.current(); ok && item.Value != "" {
					if m.sessionStore != nil {
						_ = m.sessionStore.Delete(item.Value)
					}
					// Remove the deleted item from allItems and items, keep selection stable.
					removeFromPickerItems(m.picker, item.Value)
					if len(m.picker.items) == 0 {
						m.picker = nil
					}
				}
				return m, nil
			}
		case keyText(msg) == "?" && !keyAlt(msg) && !keyHasMod(msg, tea.ModCtrl):
			// `?` opens the keyboard-shortcut overlay, but ONLY on an empty
			// composer with nothing modal up — otherwise it must type a literal
			// "?" into the prompt. Falls through to the rune-insert path below
			// when the composer is non-empty or a popup is active.
			if m.composerValue() == "" && m.noBlockingModal() && !m.transcriptDetailed && !m.subchat.active && !m.suggestionsActive() {
				m.helpOverlay = true
				return m, nil
			}
		case keyBackspace(msg):
			if m.picker != nil {
				if m.modelPickerIsLoading() {
					return m, nil
				}
				m.picker.deleteQueryRune()
				// Editing the filter changes which row is highlighted; keep the
				// theme preview in sync with it (no-op for other pickers).
				m.previewSelectedTheme()
				return m, nil
			}
			// On an empty composer, Backspace removes the last attachment chip
			// ([Image #N] / [Doc #N]) so you can drop one you don't need without
			// clearing them all. With text present it deletes a character as usual.
			if m.composerValue() == "" {
				if next, removed := m.removeLastAttachment(); removed {
					return next, nil
				}
			}
		case keyIs(msg, tea.KeyTab):
			if m.transcriptDetailed {
				return m, nil
			}
			if m.pendingPermission != nil {
				return m.movePermissionCursor(1), nil
			}
			if m.pendingAskUser != nil {
				return m.moveAskUserTab(1), nil
			}
			if m.providerWizard != nil {
				return m.handleProviderWizardKey(msg)
			}
			if m.mcpAddWizard != nil {
				return m.handleMCPAddWizardKey(msg)
			}
			if m.mcpManager != nil {
				return m.handleMCPManagerKey(msg)
			}
			if m.picker == nil && m.suggestionsActive() {
				m.moveSuggestion(1)
				return m, nil
			}
		case keyIs(msg, tea.KeyPgUp):
			if m.transcriptDetailed {
				return m, nil
			}
			// A stationary mouse over a bodyY-keyed transcript hover target would
			// otherwise stay lit at the scrolled-away bodyY until the next real
			// motion event (see clearHover) — same reasoning as the wheel-scroll
			// cases in mouse.go.
			m = m.clearHover()
			return m.scrollChat(m.chatPageScrollLines()), nil
		case keyIs(msg, tea.KeyPgDown):
			if m.transcriptDetailed {
				return m, nil
			}
			m = m.clearHover()
			return m.scrollChat(-m.chatPageScrollLines()), nil
		case keyIs(msg, tea.KeyDown):
			if m.transcriptDetailed {
				return m, nil
			}
			if m.pendingPermission != nil {
				return m.movePermissionCursor(1), nil
			}
			if m.pendingAskUser != nil {
				return m.moveAskUserCursor(1), nil
			}
			if m.providerWizard != nil {
				return m.handleProviderWizardKey(msg)
			}
			if m.mcpAddWizard != nil {
				return m.handleMCPAddWizardKey(msg)
			}
			if m.mcpManager != nil {
				return m.handleMCPManagerKey(msg)
			}
			if m.picker != nil {
				if m.modelPickerIsLoading() {
					return m, nil
				}
				m.pickerMoved(1)
				return m, nil
			}
			if m.suggestionsActive() {
				m.moveSuggestion(1)
				return m, nil
			}
			if next, ok := m.moveComposerVisualCursor(1); ok {
				return next, nil
			}
			if m.historyRecallActive() {
				return m.recallHistory(1), nil
			}
		case keyIs(msg, tea.KeyUp):
			// ArrowUp exits subchat view (returns to main chat).
			if m.subchat.active {
				m.chatScrollOffset = m.subchat.exit()
				m = m.clearHover() // bodyY numbering differs between subchat and the parent transcript
				return m, nil
			}
			if m.transcriptDetailed {
				return m, nil
			}
			if m.pendingPermission != nil {
				return m.movePermissionCursor(-1), nil
			}
			if m.pendingAskUser != nil {
				return m.moveAskUserCursor(-1), nil
			}
			if m.providerWizard != nil {
				return m.handleProviderWizardKey(msg)
			}
			if m.mcpAddWizard != nil {
				return m.handleMCPAddWizardKey(msg)
			}
			if m.mcpManager != nil {
				return m.handleMCPManagerKey(msg)
			}
			if m.picker != nil {
				if m.modelPickerIsLoading() {
					return m, nil
				}
				m.pickerMoved(-1)
				return m, nil
			}
			if m.suggestionsActive() {
				m.moveSuggestion(-1)
				return m, nil
			}
			if next, ok := m.moveComposerVisualCursor(-1); ok {
				return next, nil
			}
			if m.historyRecallActive() {
				return m.recallHistory(-1), nil
			}
		}
		if m.transcriptDetailed {
			return m, nil
		}
		if m.pendingAskUser != nil {
			_, state, ok := m.pendingAskUser.activeQuestion()
			if !ok {
				return m, nil // Confirm tab: ignore stray keys
			}
			// In picker mode a printable keystroke means the user wants to type their
			// own answer, so flip into free-text first instead of letting the text
			// accumulate invisibly and then be discarded when Enter picks an option.
			if !state.typing && keyPrintable(msg) {
				state.typing = true
				m.input.SetValue("")
			}
			if state.typing {
				var cmd tea.Cmd
				m.input, cmd = m.input.Update(msg)
				return m, cmd
			}
			return m, nil // picker mode: non-navigation keys do nothing
		}
		if m.pendingSpecReview != nil {
			return m.handleSpecReviewKey(msg)
		}
		if m.pendingPermission != nil {
			return m.handlePermissionKey(msg)
		}
		if m.providerWizard != nil {
			return m.handleProviderWizardKey(msg)
		}
		if m.mcpAddWizard != nil {
			return m.handleMCPAddWizardKey(msg)
		}
		if m.mcpManager != nil {
			return m.handleMCPManagerKey(msg)
		}
		// An open picker is modal over the input: swallow remaining keys so they
		// don't type into the field. ↑/↓/Enter/Esc were already handled above.
		if m.picker != nil {
			if m.modelPickerIsLoading() {
				return m, nil
			}
			if keyPrintable(msg) {
				m.picker.appendQuery(keyRunes(msg))
				// Filtering changes the highlighted row; keep the theme preview in
				// sync with it (no-op for other pickers).
				m.previewSelectedTheme()
			}
			return m, nil
		}
		// Handle vim-mode keys if vim mode is enabled and the composer is active.
		if m.vimMode.enabled && m.composerActive {
			if m.applyVimKey(msg) {
				return m, nil
			}
		}
		if next, ok := m.applyComposerKey(msg); ok {
			return next, nil
		}
		if m.composerActive && strings.Contains(m.composer.text, "\n") {
			return m, nil
		}
		// The key fell through to the text input: let it update, then refresh the
		// autocomplete match list from the new value.
		var cmd tea.Cmd
		m.input, cmd = m.input.Update(msg)
		m.resetComposerFromInput()
		m.recomputeSuggestions()
		return m, cmd
	case tea.FocusMsg:
		if m.notifier != nil {
			m.notifier.SetFocused(true)
		}
		return m, nil
	case tea.BlurMsg:
		if m.notifier != nil {
			m.notifier.SetFocused(false)
		}
		return m, nil
	case toolCallStreamStartMsg:
		if msg.runID != m.activeRunID {
			return m, nil
		}
		// A new tool call opened — reset the live "writing" block to it.
		m.streamCallID = msg.id
		m.streamCallName = msg.name
		m.streamCallDecoder = newStreamingDecoder()
		return m, nil
	case toolCallStreamDeltaMsg:
		if msg.runID != m.activeRunID || msg.id != m.streamCallID || m.streamCallDecoder == nil {
			return m, nil
		}
		m.streamCallDecoder.feed(msg.fragment)
		// A streamed tool-call argument (e.g. a file's contents in write_file) is
		// real generated output: count it toward the live token estimate so the
		// "↑ N tok" pulse climbs during a long write, and bump lastStreamActivity so
		// the quiet-generation hint stays clear of an actively-streaming provider.
		m.turnStreamedRunes += utf8.RuneCountInString(msg.fragment)
		m.lastStreamActivity = m.now()
		return m, nil
	case agentTextMsg:
		if msg.runID != m.activeRunID {
			return m, nil
		}
		m.prefillStatus = "" // real output started; clear prefill indicator
		// Streaming text means any in-progress tool call has finished — clear the
		// live "writing" block so it doesn't linger over new prose.
		m.clearStreamingToolCall()
		m.streamingText += msg.delta
		m.turnStreamedRunes += utf8.RuneCountInString(msg.delta)
		// recordStreamingDelta appends a time.Time to lineAges for every
		// newline in the delta and bumps lastStreamActivity. It also
		// re-stamps the in-progress last entry so the line that's still
		// being filled stays visibly fresh.
		m.recordStreamingDelta(msg.delta)
		// The fade's tick is self-perpetuating (the streamingFadeTickMsg
		// case schedules the next one). Schedule the FIRST tick only on
		// the inactive→active transition; subsequent deltas just refresh
		// state and rely on the existing tick chain.
		// When the fade is disabled (SONUT_NO_FADE / SSH / tmux / low-color),
		// fadeActive stays false so styleStreamingLine renders streaming text
		// statically at base ink, and no self-perpetuating tick is scheduled.
		if !m.fadeDisabled {
			startTick := !m.fadeActive
			m.fadeActive = true
			if startTick {
				return m, streamingFadeTick()
			}
		}
		return m, nil
	case prefillProgressMsg:
		if msg.runID != m.activeRunID || !m.pending {
			return m, nil
		}
		// Prefill is real server work — refresh the activity clock so the
		// quiet-generation hint doesn't paint a healthy prefill as a hang.
		m.lastStreamActivity = m.now()
		m.prefillProcessed = msg.processed
		m.prefillTotal = msg.total
		if msg.total > 0 {
			if msg.processed >= msg.total {
				m.prefillComplete = true
				m.prefillStatus = ""
			} else {
				m.prefillStatus = fmt.Sprintf("prefilling context… %d%% (%d/%d tok)", msg.processed*100/msg.total, msg.processed, msg.total)
			}
		} else if msg.processed > 0 {
			m.prefillStatus = fmt.Sprintf("prefilling context… %d tok", msg.processed)
		} else {
			m.prefillStatus = "prefilling context…"
		}
		return m, nil
	case agentReasoningMsg:
		if msg.runID != m.activeRunID {
			return m, nil
		}
		m.prefillStatus = "" // real output started; clear prefill indicator
		m.streamingReasoning += msg.delta
		m.turnStreamedRunes += utf8.RuneCountInString(msg.delta)
		// Reasoning IS live provider output, so refresh the activity clock — else the
		// quiet-generation hint can wrongly read "still generating…" mid-think.
		if msg.delta != "" {
			m.lastStreamActivity = m.now()
		}
		return m, nil
	case spinner.TickMsg:
		// Record when swarm members first finish so the sidebar can linger them
		// with a fading ✓ before removal. Cheap (the tick only fires while a run is
		// in flight or the sidebar holds agents — exactly when this can change).
		m.stampSwarmDone()
		// Not forwarding the tick while idle stops the spinner's self-scheduling,
		// so no timer fires between runs. The one exception is an active sidebar
		// holding agents: their cool ripple animation needs the phase to keep
		// advancing even when no run is pending, so the tick loop stays alive while
		// sidebarHasAgents() holds (and stops the moment the agents/sidebar clear).
		if !m.pending && !m.compactInFlight && !m.doctorInFlight {
			if m.sidebarHasAgents() && !m.reducedMotion {
				m.spinner, _ = m.spinner.Update(msg)
				m.spinnerPhase++
				m.spinnerTicking = true
				return m, m.spinner.Tick
			}
			m.spinnerTicking = false
			return m, nil
		}
		m.spinnerTicking = true
		var cmd tea.Cmd
		m.spinner, cmd = m.spinner.Update(msg)
		// Advance the shared ripple phase in lock-step with the spinner glyph;
		// frozen under reduced motion so the colour wave stops with the glyph.
		if !m.reducedMotion {
			m.spinnerPhase++
		}
		if m.compactInFlight {
			if !m.reducedMotion {
				m.compactFrame++ // frozen frame under reduced motion -> static ring
			}
			m = m.setCompactStatusRow(m.compactText(true))
		}
		if m.doctorInFlight {
			if !m.reducedMotion {
				m.doctorFrame++
			}
			m = m.setDoctorStatusRow(m.doctorConnectivityRunningText())
		}
		return m, cmd
	case streamingFadeTickMsg:
		// The fade's own tick (separate from the spinner so a slower
		// cadence is enough). Short-circuits when fadeActive is false,
		// which is how the ticker stops cleanly at stream end: the
		// agentResponseMsg handler sets fadeActive = false, and the
		// next tick that lands after that point returns nil here.
		if !m.fadeActive {
			return m, nil
		}
		return m, streamingFadeTick()
	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		// A resize re-wraps content at a new width, shifting every row's bodyY;
		// a stale transcript-hover target could coincidentally land on an
		// unrelated clickable row (same reasoning as clearHover's other callers).
		m = m.clearHover()
		// Reset the streaming-text fade state. A width change can re-wrap
		// the in-progress text into a different number of visual lines,
		// which invalidates the per-line age mapping. The next delta
		// will reseed lineAges and restart the tick.
		m.lineAges = nil
		m.lastStreamActivity = m.now()
		// Size the composer so long input scrolls horizontally with the cursor
		// visible instead of being clipped invisibly past the right edge.
		m.input.SetWidth(maxInt(20, chatWidth(msg.Width)-14))
		// The title bar prints once into native scrollback when the inline
		// renderer is active. In alt-screen mode it stays pinned inside View.
		if !m.altScreen && !m.headerPrinted && msg.Width > 0 {
			m.headerPrinted = true
			m.flushQueue = append(m.flushQueue, m.titleBar(chatWidth(msg.Width)))
		}
		// A resumed/idle session may already hold sidebar agents now that geometry
		// (and thus sidebarActive) is known; kick the ripple tick loop if so. No-op
		// when the loop is already running or there is nothing to animate.
		return m, m.ensureSpinnerTick()
	case permissionRequestMsg:
		if msg.runID != m.activeRunID {
			return m, nil
		}
		promptRow := permissionTranscriptRow(permissionEventFromRequest(msg.request))
		promptRow.runID = msg.runID
		m.transcript = appendTranscriptRow(m.transcript, promptRow)
		if msg.request.Action == agent.PermissionActionPrompt {
			m.pendingPermission = &pendingPermissionPrompt{
				request: msg.request,
				decide:  msg.decide,
			}
			m.webNotice("input", "Permission requested: "+msg.request.ToolName)
			m.pong.pause() // the keyboard belongs to the prompt now
		}
		return m, nil
	case askUserRequestMsg:
		if msg.runID != m.activeRunID {
			return m, nil
		}
		// A request with no questions has nothing to answer — resolve it
		// immediately so the run isn't stalled waiting on manual input. Mirror the
		// normal flow: record the (empty) request in the transcript and answer with
		// an empty slice (not nil) so downstream sees the same Answers shape.
		if len(msg.request.Questions) == 0 {
			m.transcript = appendTranscriptRow(m.transcript, askUserTranscriptRow(msg.request))
			if msg.answer != nil {
				msg.answer([]string{})
			}
			return m, nil
		}
		m.transcript = appendTranscriptRow(m.transcript, askUserTranscriptRow(msg.request))
		m.pendingAskUser = &pendingAskUserPrompt{
			request: msg.request,
			answer:  msg.answer,
			states:  newAskUserStates(msg.request.Questions),
		}
		m.webNotice("input", "@code needs your input — answer in the terminal.")
		m.pong.pause() // the questionnaire owns the keyboard
		m.clearComposer()
		m.clearSuggestions()
		return m, nil
	case interactionPromptTimeoutMsg:
		if msg.runID != m.activeRunID {
			return m, nil
		}
		switch msg.kind {
		case "permission":
			if m.pendingPermission != nil && m.pendingPermission.request.ToolCallID == msg.toolCallID {
				m.safetyState.recordPermissionDecision(m.pendingPermission.request, permissionDecisionDeny)
				m.pendingPermission = nil
				m.webNotice("input", "Permission prompt timed out after 10 seconds; continuing safely.")
			}
		case "ask_user":
			if m.pendingAskUser != nil && m.pendingAskUser.request.ToolCallID == msg.toolCallID {
				m.pendingAskUser = nil
				m.clearComposer()
				m.clearSuggestions()
				m.webNotice("input", "Input prompt timed out after 10 seconds; continuing autonomously.")
			}
		}
		return m, nil
	case agentResponseMsg:
		if msg.runID != m.activeRunID {
			// A run cancelled while in flight still finishes in its goroutine and
			// returns its accumulated session events here. Persist ONLY those events
			// (notably the EventSessionCheckpoint payloads captured before each
			// mutating tool) so the checkpoint blobs stay referenced and /rewind
			// works; the cancel path already wrote the "Run cancelled." marker, so
			// skip transcript rows, the trailing cancellation error, and any pending
			// state changes.
			if flushSessionID, flushing := m.flushRunIDs[msg.runID]; flushing {
				delete(m.flushRunIDs, msg.runID)
				// The cancelled run still consumed tokens; record them so the usage
				// readout doesn't undercount interrupted turns.
				liveUsageCount := m.liveUsageCounts[msg.runID]
				for index, event := range msg.usageEvents {
					if index < liveUsageCount {
						continue
					}
					var usageRows []transcriptRow
					m, usageRows = m.recordUsageEvent(msg.usageModelID, event)
					for _, row := range usageRows {
						m.transcript = appendTranscriptRow(m.transcript, row)
					}
				}
				delete(m.liveUsageCounts, msg.runID)
				// Events are persisted into the session the run was recording into AT
				// CANCEL TIME — the active session may have changed since (/resume),
				// and writing there would contaminate its log with checkpoint payloads
				// whose blobs live under the original session. appendSessionEvents*
				// only returns rows for persist FAILURES; surface them so a failed
				// checkpoint/tool flush (which would silently degrade /rewind) is
				// visible rather than swallowed.
				var flushRows []transcriptRow
				events := flushableSessionEvents(msg.sessionEvents)
				if flushSessionID == m.activeSession.SessionID {
					m, flushRows = m.appendSessionEvents(events)
				} else {
					flushRows = m.appendSessionEventsTo(flushSessionID, events)
				}
				for _, row := range flushRows {
					m.transcript = appendTranscriptRow(m.transcript, row)
				}
				// A Ctrl+C during an in-flight run defers its quit until the run's
				// checkpoint session events have been flushed (above). Now that the
				// last pending flush is drained, fire the deferred quit.
				if m.exiting && len(m.flushRunIDs) == 0 {
					return m.quit()
				}
			}
			return m, nil
		}
		m.clearStreamingToolCall() // active run finished — drop any lingering "writing" block
		m.pending = false
		if utteranceID, ok := m.voiceRuns[msg.runID]; ok {
			delete(m.voiceRuns, msg.runID)
			builderRun := m.voiceBuilderRuns[msg.runID]
			delete(m.voiceBuilderRuns, msg.runID)
			if builderRun && m.voice != nil && m.voice.utteranceID == utteranceID {
				for index := len(msg.rows) - 1; index >= 0; index-- {
					if msg.rows[index].kind == rowAssistant && strings.TrimSpace(msg.rows[index].text) != "" {
						m.voiceSpeakDelta(utteranceID, m.voiceLanguage(), msg.rows[index].text)
						break
					}
				}
			}
			m.voiceFlushSpeech(utteranceID, m.voiceLanguage())
			if m.voice != nil && m.voice.utteranceID == utteranceID && m.voice.state != voicepkg.StateConfirmBuild {
				m.voice.state = voicepkg.StateIdle
			}
		}
		// A steered message pushed in the instant the run was already finishing
		// never reached the loop; fold it back into the queue so it launches as
		// the next turn instead of silently disappearing. (Its user row is
		// already in the transcript, so launchPrompt would duplicate it — but
		// this race window is a few milliseconds wide and losing input is worse.)
		if m.steerInbox != nil {
			if leftover := m.steerInbox.Drain(); len(leftover) > 0 {
				pendingQueue := strings.TrimSpace(strings.Join(append(leftover, m.queuedMessage), "\n"))
				m.queuedMessage = pendingQueue
			}
			m.steerInbox = nil
		}
		if m.btwPending {
			m.btwPending = false
			m.btwDone = true // armed: the next empty-composer Enter prints the return row
			m.transcript = reduceTranscript(m.transcript, transcriptAction{
				kind: actionAppendSystem,
				text: "── /btw done · press Enter to return to the main conversation ──",
			})
		}
		m.prefillStatus = "" // run finished; clear any lingering prefill indicator
		m.prefillProcessed = 0
		m.prefillTotal = 0
		m.prefillComplete = false
		m = m.disarmCancelConfirmation() // the run finished on its own — nothing left to confirm cancelling
		// Fully reset the fade state at stream end. The next render
		// emits the final row in solid ink (no settling animation), and
		// the pending streamingFadeTickMsg that lands after this point
		// short-circuits because fadeActive is false. Clearing lineAges
		// and lastStreamActivity here too prevents stale age data from
		// carrying over to the next turn (and stops lineAges from
		// growing indefinitely across many runs).
		m.resetStreamingFade()
		// The run is complete: release its context now instead of waiting for the
		// parent context — every prompt leaked a CancelFunc (and its timer
		// resources) until app exit otherwise.
		if m.runCancel != nil {
			m.runCancel()
		}
		m.runCancel = nil
		m.activeRunID = 0
		setUserRunActive(false)   // allow background KV warmups again
		m.plan.frozenAt = m.now() // freeze the plan clock while idle (no run in flight)
		// A fully successful turn means the task is done. Weaker models often
		// forget the final update_plan, leaving the panel stuck mid-progress;
		// reconcile it to complete here. Read pendingAskUser/pendingPermission
		// BEFORE the reset below clears them, and skip spec-draft reviews — those
		// are legitimate mid-plan err==nil yields where the plan is NOT done.
		if msg.err == nil && msg.specReview == nil &&
			m.pendingAskUser == nil && m.pendingPermission == nil {
			m.plan.completeRemaining(m.now())
		}
		m.pendingPermission = nil
		m.pendingAskUser = nil
		liveUsageCount := m.liveUsageCounts[msg.runID]
		for index, event := range msg.usageEvents {
			if index < liveUsageCount {
				continue
			}
			var usageRows []transcriptRow
			m, usageRows = m.recordUsageEvent(msg.usageModelID, event)
			for _, row := range usageRows {
				m.transcript = appendTranscriptRow(m.transcript, row)
			}
		}
		delete(m.liveUsageCounts, msg.runID)
		var sessionRows []transcriptRow
		if msg.runID == m.btwRunID && m.btwRunID != 0 {
			// A /btw aside is persisted for resume fidelity but kept OUT of the
			// model-facing context (m.sessionEvents) so it never steers later turns.
			m.btwRunID = 0
			m, sessionRows = m.appendSessionEventsEphemeral(msg.sessionEvents)
		} else {
			m, sessionRows = m.appendSessionEvents(msg.sessionEvents)
		}
		for _, row := range sessionRows {
			m.transcript = appendTranscriptRow(m.transcript, row)
		}
		// Reasoning is a live, ephemeral preview only. Never commit provider
		// scratch work to the transcript, even if a backend supplies it through a
		// legacy row callback.
		for _, row := range msg.rows {
			if row.kind == rowReasoning {
				continue
			}
			m = m.clearFileResetForRow(row)
			m.transcript = appendTranscriptRow(m.transcript, row)
		}
		if msg.err != nil {
			// A failed turn has no final answer row to supersede the streamed
			// text the user already watched — keep the partial answer instead of
			// letting it vanish from history.
			if text := strings.TrimRight(m.streamingText, "\n"); strings.TrimSpace(text) != "" {
				m.transcript = appendTranscriptRow(m.transcript, transcriptRow{kind: rowAssistant, text: text})
			}
			// The error row terminates the turn, so it carries the done-line
			// metadata a final assistant row would have carried.
			m.transcript = appendTranscriptRow(m.transcript, transcriptRow{
				kind:        rowError,
				text:        msg.err.Error(),
				final:       true,
				turnTools:   msg.turnTools,
				turnElapsed: msg.turnElapsed,
			})
		}
		m.streamingText = ""
		m.streamingReasoning = ""
		m.streamingReasoningExpanded = false
		if msg.specReview != nil {
			m = m.activateSpecReview(*msg.specReview)
		}
		if m.notifier != nil {
			m.notifier.Notify(notify.Completion, notify.DefaultMessage(notify.Completion))
		}
		m.webNotice("done", "Run finished.")
		// The answer needs the user's eyes; freeze the rally, spacebar resumes.
		m.pong.pause()
		// A successful turn gives the session real content; if it still carries its
		// default first-message title, generate a concise one in the background
		// (one-shot per session). A failed turn is skipped — there's nothing to name.
		var titleCmd, recapCmd, knowledgeCmd tea.Cmd
		if msg.err == nil {
			m, titleCmd = m.maybeAutoTitleActiveSession()
			// Post-turn recap (gated on the recaps preference): one short sentence
			// summarizing the turn's final answer, shown as a "※ recap:" footnote.
			var finalAnswer string
			for _, row := range msg.rows {
				if row.kind == rowAssistant && row.final {
					finalAnswer = row.text
				}
			}
			m, recapCmd = m.maybeRecapTurn(msg.runID, finalAnswer, msg.turnTools)
			knowledgeCmd = m.maybeCaptureKnowledge(finalAnswer)
		}
		// End-of-turn git sweep: catch file mutations the tool stream couldn't
		// report (bash scaffolding, subagent edits) so the FILES sidebar is
		// complete once the turn settles.
		var sweepCmd tea.Cmd
		m, sweepCmd = m.maybeGitSweep()

		// Nonstop / keepGoing: when enabled and the run completed successfully,
		// check whether plan items are still open. If so, auto-inject a
		// continuation nudge (bounded by maxNonstopNudges so it can't loop
		// forever). This mirrors sonut-code's keepGoing.ts feature.
		const maxNonstopNudges = 3
		if m.nonstop && msg.err == nil && msg.specReview == nil &&
			m.pendingAskUser == nil && m.pendingPermission == nil {
			openCount := m.plan.openCount()
			// Diminishing returns: a nudge that closed no plan item is stale.
			// Three stale nudges in a row means the model is stuck, not working —
			// stop force-marching it (mirrors tokenBudget.ts's stop decision).
			if openCount > 0 && m.nonstopLastOpenCount > 0 && openCount >= m.nonstopLastOpenCount {
				m.nonstopStaleNudges++
			} else {
				m.nonstopStaleNudges = 0
			}
			m.nonstopLastOpenCount = openCount
			if openCount > 0 && m.nonstopNudgeCount < maxNonstopNudges && m.nonstopStaleNudges < 3 {
				m.nonstopNudgeCount++
				noun := "tasks"
				if openCount == 1 {
					noun = "task"
				}
				nudge := fmt.Sprintf(
					"%d %s in your plan %s still open. Keep going — do the next open task now. "+
						"Do not stop to ask 'should I continue?' and do not hand back to the user "+
						"until every task is completed. When all tasks are done, give a short final summary.",
					openCount, noun, map[bool]string{true: "is", false: "are"}[openCount == 1],
				)
				// Ride the LIVE plan state on the nudge so the continued run acts on
				// current facts instead of a stale approach it remembers.
				if snapshot := m.plan.statusSnapshot(); snapshot != "" {
					nudge += "\n\nCurrent plan status:\n" + snapshot
				}
				// This is a continuation of the same planner-owned roadmap, not a
				// new user build request. Keep the executor fast and skip the
				// expensive grounding/planner pass for the nudge.
				next, continueCmd := m.launchPromptWithOptions(nudge, tuiAgentRunOptions{
					plannerPrompt: "continue the existing plan",
					runMode:       agent.RunModeBuilder,
					executorOnly:  true,
				})
				return next, tea.Batch(titleCmd, recapCmd, knowledgeCmd, sweepCmd, continueCmd)
			}
			if openCount == 0 {
				m.nonstopNudgeCount = 0
				m.nonstopLastOpenCount = 0
				m.nonstopStaleNudges = 0
			}
		}

		// Fire any deferred effort warmup now that llama.cpp is free.
		var warmupCmd tea.Cmd
		if m.deferredEffortWarmup {
			m.deferredEffortWarmup = false
			warmupCmd = m.warmupEffortCmd()
		}
		next, queuedCmd := m.launchQueuedMessageIfReady()
		return next, tea.Batch(titleCmd, recapCmd, knowledgeCmd, sweepCmd, queuedCmd, warmupCmd)
	case sessionTitleGeneratedMsg:
		return m.handleSessionTitleGenerated(msg)
	case recapGeneratedMsg:
		return m.handleRecapGenerated(msg)
	case compactResultMsg:
		if !m.compactInFlight {
			return m, nil
		}
		m.compactInFlight = false
		m.compactFrame = 0
		m.lastCompactResult = nil
		m.lastCompactError = ""
		if msg.err != nil {
			m.lastCompactError = msg.err.Error()
			m = m.setCompactStatusRow(m.compactText(true))
			return m, nil
		}
		if msg.hasSessionSnapshot {
			m.activeSession = msg.activeSession
			m.sessionEvents = append([]sessions.Event{}, msg.sessionEvents...)
			m.transcript = append([]transcriptRow{}, msg.transcript...)
			m.resetFlushFrontier("· compacted ·")
		}
		m.lastCompactResult = &msg.result
		m = m.setCompactStatusRow(m.compactText(true))
		return m, nil
	case planUpdateMsg:
		if msg.runID != m.activeRunID {
			return m, nil
		}
		items := msg.items
		if !msg.authoritative {
			items = m.plan.mergePartialItems(items)
		}
		m.plan.updateFromItems(items, m.now())
		return m, nil
	case planStepExplanationMsg:
		// Drop a result from a previous run: beginRun bumps planDetailGen and clears
		// stepExplanation, so a stale in-flight write-up must not repopulate the
		// cache or overwrite the new run's data.
		if msg.gen != m.planDetailGen {
			return m, nil
		}
		// Cache the write-up so re-clicking the step is instant; an empty result
		// (failed/blank) caches "" so the card shows the local fallback summary and
		// we don't retry the model on every re-click. Only re-render the card when
		// this step's detail is still the one open (the user may have closed it or
		// clicked another step while the request was in flight).
		if m.stepExplanation == nil {
			m.stepExplanation = map[string]string{}
		}
		text := strings.TrimSpace(msg.text)
		if msg.err != nil {
			text = ""
		}
		m.stepExplanation[msg.key] = text
		if m.planDetailOpen && m.planDetailStep == msg.stepIndex &&
			msg.stepIndex >= 0 && msg.stepIndex < len(m.plan.steps) &&
			planStepExplanationKey(m.plan.steps[msg.stepIndex]) == msg.key {
			m.transcript = dropTranscriptRowsByID(m.transcript, planStepDetailRowID)
			m.transcript = m.appendPlanStepCard(msg.stepIndex, text, false)
		}
		return m, nil
	case agentUsageMsg:
		if msg.runID != m.activeRunID {
			return m, nil
		}
		var usageRows []transcriptRow
		m, usageRows = m.recordUsageEvent(msg.modelID, msg.usage)
		if m.liveUsageCounts == nil {
			m.liveUsageCounts = map[int]int{}
		}
		m.liveUsageCounts[msg.runID]++
		for _, row := range usageRows {
			m.transcript = appendTranscriptRow(m.transcript, row)
		}
		return m, nil
	case specialistStartMsg:
		if msg.runID != m.activeRunID {
			return m, nil
		}
		m.specialists.start(msg.name, msg.description, msg.childSessionID, m.now())
		return m, nil
	case dataFlowMsg:
		// No runID gate: a cancelled run's calls still sent the data — the
		// exposure map must count them regardless.
		m.safetyState.addDestinations(msg.destinations)
		return m, nil
	case specialistCompleteMsg:
		if msg.runID != m.activeRunID {
			return m, nil
		}
		// The specialist was started with the tool call ID as a temporary key
		// (the real session ID isn't known until the child process creates it).
		// Reconcile: complete by the tool call ID, then rewrite the tracker
		// entry's childSessionID to the real session ID so subchat.enter can
		// find the child session's events in the store.
		m.specialists.complete(msg.toolCallID, msg.status, 0, msg.errorMsg, m.now())
		if msg.childSessionID != "" && msg.childSessionID != msg.toolCallID {
			m.specialists.reconcileSessionID(msg.toolCallID, msg.childSessionID)
		}
		if info, ok := m.specialists.getBySessionID(msg.childSessionID); ok {
			if info.childSessionID == "" {
				info.childSessionID = msg.toolCallID
			}
			cardRow := transcriptRow{
				kind:           rowSpecialist,
				runID:          msg.runID,
				specialistInfo: &info,
			}
			m.transcript = appendTranscriptRow(m.transcript, cardRow)
		}
		return m, nil
	case specialistProgressMsg:
		if msg.runID != m.activeRunID {
			return m, nil
		}
		// Each progress message is one specialist tool call (OnToolProgress fires only
		// for EventToolCall); bump the card's tool-call counter so it stops showing a
		// permanent "0 tool calls" (M18). The tracker is still keyed by the tool-call
		// id at this point (reconciled to the session id only on completion).
		m.specialists.incrementToolCount(msg.toolCallID)
		m.specialists.setCurrentTool(msg.toolCallID, msg.toolName, msg.detail)
		return m, nil
	case agentRowMsg:
		if msg.runID != m.activeRunID {
			return m, nil
		}
		m.prefillStatus = "" // real output started; clear prefill indicator
		if msg.row.kind == rowReasoning {
			m.streamingReasoning = ""
			m.streamingReasoningExpanded = false
		}
		// A tool call ends the current streamed text segment. The segment is the
		// assistant's working narration ("Let me check X…") — append it as a
		// non-final assistant row so it stays in history instead of silently
		// vanishing when the tool card replaces the interim block.
		if msg.row.kind == rowToolCall {
			// Reasoning is an ephemeral preview; the tool card supersedes it.
			m.streamingReasoning = ""
			m.streamingReasoningExpanded = false
			if text := strings.TrimRight(m.streamingText, "\n"); strings.TrimSpace(text) != "" {
				m.transcript = appendTranscriptRow(m.transcript, transcriptRow{kind: rowAssistant, text: text})
				// This interim narration is the agent explaining what it's about to
				// do — attribute it to the active plan step so the step-detail card
				// can replay the agent's own account of the work.
				m = m.captureStepNarration(text)
			}
			m.streamingText = ""
			// The tool call has finalized into its card — drop the live "writing"
			// preview so it doesn't linger or duplicate beneath the card.
			m.clearStreamingToolCall()
		}
		// Collapse a repeated swarm status/collect card so re-checks don't flood
		// the chat with identical blocks.
		m = m.clearFileResetForRow(msg.row)
		m.transcript = collapseRepeatedStatusCard(m.transcript, msg.row)
		m.transcript = appendTranscriptRow(m.transcript, msg.row)
		// Seed the plan from planner output if the plan is currently empty.
		// The planner emits "planner_<runID>" rows with text "Plan:\n1. ...\n2. ..."
		// Parse these to populate the plan panel before update_plan tool calls happen.
		// A finished plan from an earlier turn must not block the new turn's
		// roadmap, so a complete plan is treated as replaceable here.
		if msg.row.kind == rowSystem && (m.plan.isEmpty() || m.plan.isComplete()) {
			if strings.HasPrefix(msg.row.id, "planner_") && !strings.HasPrefix(msg.row.id, "planner_start_") {
				items := parsePlannerOutputText(msg.row.text, 12)
				if len(items) > 0 {
					m.plan.updateFromItems(items, m.now())
				}
			}
		}
		// Update browser preview panel state from tool calls/results. Navigate the
		// real terminal-browser pane for URL-changing calls as well, so the visual
		// pane follows the agent in both headful and headless execution modes.
		var terminalBrowserCmd tea.Cmd
		if isBrowserTool(msg.row.tool) {
			if msg.row.kind == rowToolCall {
				args := browserToolRowArguments(msg.row)
				m.browserState.parseBrowserToolCall(msg.row.tool, args)
				if target := browserToolCallURL(msg.row.tool, args); target != "" {
					if m.terminalBrowserActive {
						terminalBrowserCmd = mirrorTerminalBrowserNavigationCmd(m.ctx, target)
					} else {
						terminalBrowserCmd = openTerminalBrowserTargetCmd(m.ctx, target)
					}
				}
			} else if msg.row.kind == rowToolResult {
				m.browserState.parseBrowserToolResultWithImage(msg.row.tool, msg.row.detail, msg.row.status == tools.StatusError, msg.row.browserImage)
			}
		} else if msg.row.kind == rowToolResult && isLocalFileBrowserHandoff(msg.row.detail) {
			m.browserState.parseLocalFileBrowserHandoffWithImage(msg.row.detail, msg.row.status == tools.StatusError, msg.row.browserImage)
		}
		m = m.captureStepWork(msg.row)
		// A finished command tool may have mutated files git can see but no
		// changedFiles reports (npm create, heredoc writes, subagent edits) —
		// re-sweep so the FILES sidebar picks them up mid-turn.
		if msg.row.kind == rowToolResult && isPlanCommandTool(msg.row.tool) {
			var sweep tea.Cmd
			m, sweep = m.maybeGitSweep()
			return m, tea.Batch(sweep, terminalBrowserCmd)
		}
		return m, terminalBrowserCmd
	case swarmSessionsMsg:
		// Merge completed swarm members' session ids so their AGENTS sidebar rows
		// become drill-in clickable. Session ids are durable facts, so this is not
		// gated on the active run.
		if m.swarmSessionMap == nil {
			m.swarmSessionMap = map[string]string{}
		}
		for taskID, sessionID := range msg.sessions {
			if taskID != "" && sessionID != "" {
				m.swarmSessionMap[taskID] = sessionID
			}
		}
		return m, nil
	case doctorCommandResultMsg:
		if msg.id == 0 || msg.id == m.doctorCommandSeq {
			m.doctorInFlight = false
			m.doctorFrame = 0
			m = m.setDoctorStatusRow(msg.text)
		}
		return m, nil
	case sandboxSetupCommandResultMsg:
		if msg.id == 0 || msg.id == m.sandboxSetupSeq {
			m.sandboxSetupInFlight = false
			m = m.setSandboxSetupStatusRow(sandboxSetupResultText(msg.result))
		}
		return m, nil
	case feedbackSubmittedMsg:
		m.transcript = reduceTranscript(m.transcript, transcriptAction{
			kind: actionAppendSystem,
			text: feedbackResultText(msg.err),
		})
		return m, nil
	case prStateMsg:
		m.prState = msg.state
		return m, nil
	case gitSweepMsg:
		return m.handleGitSweepMsg(msg), nil
	case kvWarmupDoneMsg:
		return m, nil

	case llamaRestartDoneMsg:
		var text string
		if msg.err != nil {
			text = fmt.Sprintf("atcode engine restart failed: %v\nCheck ~/.atcode/atcode-server.log for details.", msg.err)
		} else {
			text = fmt.Sprintf("Model server restarted — context window is now %s.", memoryCtxLabel(msg.ctxSize))
		}
		m.transcript = reduceTranscript(m.transcript, transcriptAction{kind: actionAppendSystem, text: text})
		return m, nil

	case prWatcherStartedMsg:
		if msg.stop == nil {
			return m, nil
		}
		if m.prWatcherStop != nil {
			m.prWatcherStop()
		}
		m.prWatcherStop = msg.stop
		return m, nil
	case bashResultMsg:
		m.transcript = reduceTranscript(m.transcript, transcriptAction{kind: actionAppendSystem, text: msg.output})
		return m, nil
	case providerModelsDiscoveredMsg:
		return m.applyProviderModelsDiscovered(msg), nil
	case setupModelsDiscoveredMsg:
		return m.applySetupModelsDiscovered(msg), nil
	case setupOAuthMsg:
		return m.applySetupOAuth(msg)
	case setupOAuthDeviceMsg:
		return m.applySetupOAuthDeviceCode(msg)
	case modelPickerModelsDiscoveredMsg:
		return m.applyModelPickerModelsDiscovered(msg), nil
	case ollamaContextWindowDiscoveredMsg:
		if msg.err == nil && msg.contextWindow > 0 {
			if m.ollamaContextWindowByModel == nil {
				m.ollamaContextWindowByModel = map[string]int{}
			}
			m.ollamaContextWindowByModel[msg.modelName] = msg.contextWindow
		}
		return m, nil
	case mcpCommandResultMsg:
		return m.applyMCPCommandResultMessage(msg), nil
	case tea.KeyReleaseMsg:
		next, cmd, _ := m.handleVoiceRelease(msg)
		return next, cmd
	case parsedCommand:
		// Startup --resume (and any other path that injects a command as a tea.Msg)
		// must run through the same dispatcher as typed slash commands. Without this
		// case, Init's resume message was silently dropped and `at --resume` always
		// opened a blank session.
		return m.executeCommand(msg)
	}

	var cmd tea.Cmd
	m.input, cmd = m.input.Update(msg)
	return m, cmd
}

func (m model) View() tea.View {
	var content string
	if m.setup.visible {
		content = m.setupView(chatWidth(m.width))
	} else if m.helpOverlay {
		content = m.renderKeybindingHelpOverlay(chatWidth(m.width), m.height)
	} else if m.transcriptDetailed {
		content = m.detailedTranscriptView()
	} else {
		content = m.transcriptView()
	}
	// Mirror the frame to any connected /share devices. View runs after every
	// Update, making it the single choke point where transcript changes from
	// ALL code paths are visible; the fingerprint check inside keeps the
	// no-change cost negligible.
	m.publishWebMirror()

	view := tea.NewView(content)
	view.AltScreen = m.altScreen
	// Paint the whole frame with the active theme's surface. @code never paints the
	// terminal's own canvas, so without this a theme's text falls on the terminal
	// background — fine when they share polarity, but a light theme's dark text on a
	// dark terminal (or vice versa) is invisible, and a color theme never shows its
	// real surface. Painting the panel makes every theme self-contained and legible
	// on any terminal, and fills the transparent popup interiors (e.g. the /theme
	// picker) too. Alt-screen only, so inline output never leaves a painted
	// background behind in the user's scrollback after exit.
	if m.altScreen {
		view.BackgroundColor = sonutTheme.bgPanel
	}
	view.ReportFocus = m.notifier != nil
	view.KeyboardEnhancements.ReportEventTypes = true
	if m.wantsMouseCapture() {
		// AllMotion (not CellMotion) is required for hover highlighting: it
		// reports cursor movement even with no button pressed. CellMotion only
		// reports motion while a button is held (drag) — see bubbletea's
		// MouseMode docs. AllMotion has marginally worse terminal compatibility
		// but is well supported by the terminals this app targets; the existing
		// 15ms mouse-event throttle (mouseEventThrottleInterval) already bounds
		// the redraw rate from the extra motion events.
		view.MouseMode = tea.MouseModeAllMotion
	}
	return view
}

// transcriptEmpty reports whether the chat surface has no real content yet
// (only the welcome row), which is when the empty state renders.
func (m model) transcriptEmpty() bool {
	for _, row := range m.transcript {
		if row.kind != rowWelcome {
			return false
		}
	}
	return true
}

// transcriptView renders the visible chat surface: in inline mode this is the
// live tail not yet settled into native scrollback; in alt-screen mode it is
// the managed conversation view. Streaming/modal blocks and composer chrome are
// always rendered here.
func (m model) transcriptView() string {
	// /voice is a native full-width workspace: settings, setup, device
	// selection, and diagnostics stay contained here rather than becoming chat
	// transcript announcements on the main page.
	if m.voiceView.active {
		width := chatWidth(m.width)
		header := m.voiceViewNavBar(width)
		bodyItems := m.voiceViewBodyItems(width)
		footer := m.voiceViewFooter(width)
		if m.altScreen && m.height > 0 {
			return m.scrollableTranscriptItemsView(header, bodyItems, footer, width, "")
		}
		body := layoutTranscriptBodyItems(bodyItems).String()
		return header + "\n\n" + body + "\n" + footer
	}
	// /sec is a native full-width workspace: it deliberately replaces the chat
	// body and browser/sidebar split so evidence, paths, and remediation remain
	// readable at terminal scale.
	if m.secView.active {
		width := chatWidth(m.width)
		if m.secView.transitioning {
			return m.secTransitionView(width)
		}
		header := m.secViewNavBar(width)
		bodyItems := m.secViewBodyItems(width)
		footer := m.secFooterView(width)
		if m.altScreen && m.height > 0 {
			return m.scrollableTranscriptItemsView(header, bodyItems, footer, width, "")
		}
		body := layoutTranscriptBodyItems(bodyItems).String()
		return header + "\n\n" + body + "\n" + footer
	}
	if m.knowledgeView.active {
		width := chatWidth(m.width)
		header, footer := m.knowledgeHeaderAndFooter(width)
		bodyItems := m.knowledgeViewBodyItems(width)
		if m.altScreen && m.height > 0 {
			return m.scrollableTranscriptItemsView(header, bodyItems, footer, width, "")
		}
		body := layoutTranscriptBodyItems(bodyItems).String()
		return header + "\n\n" + body + "\n" + footer
	}

	// Two-column layout: in alt-screen managed mode on a wide-enough terminal,
	// the chat renders into a left column and a context sidebar (FILES / PLAN /
	// tokens) into a right column. The chat is rendered by the existing scroll
	// engine at the reduced column width via a model copy, then joined with the
	// sidebar row-by-row. The subchat drill-in keeps its own single-column view.
	// sidebarActive includes the explicit Ctrl+S empty-sidebar override, so the
	// split also renders before the model has produced any plan or tool activity.
	if m.sidebarActive() {
		return m.twoColumnTranscriptView()
	}

	width := chatWidth(m.width)

	// Subchat drill-in: when active, show the child session's transcript with
	// a nav bar instead of the main chat.
	if m.subchat.active {
		navBar := renderSubchatNavBar(m.subchat.childSessionTitle, width)
		childBodyItems := m.transcriptBodyItemsFromRows(m.subchat.childRows, width)
		footer := m.footerView(width)
		if m.altScreen && m.height > 0 {
			return m.scrollableTranscriptItemsView(navBar, childBodyItems, footer, width, "")
		}
		bodyLayout := layoutTranscriptBodyItems(childBodyItems)
		body := navBar + "\n\n" + bodyLayout.String()
		return body + footer
	}

	suggestionOverlay := m.suggestionOverlay(width)
	providerOverlay := m.providerWizardOverlay(width)
	mcpAddOverlay := m.mcpAddWizardOverlay(width)
	mcpOverlay := m.mcpManagerOverlay(width)
	pickerOverlay := m.pickerOverlay(width)
	viewportOverlay := ""
	switch {
	case providerOverlay != "":
		viewportOverlay = providerOverlay
	case mcpAddOverlay != "":
		viewportOverlay = mcpAddOverlay
	case mcpOverlay != "":
		viewportOverlay = mcpOverlay
	case pickerOverlay != "":
		viewportOverlay = pickerOverlay
	case suggestionOverlay != "":
		viewportOverlay = suggestionOverlay
	}
	emptyOverlay := ""
	if m.transcriptEmpty() && !m.pending && viewportOverlay != "" {
		emptyOverlay = viewportOverlay
	}
	bodyItems := m.transcriptBodyItems(width, emptyOverlay)

	footer := m.footerView(width)

	overlayForViewport := viewportOverlay
	if m.transcriptEmpty() && !m.pending && viewportOverlay != "" {
		overlayForViewport = ""
	}

	// Plan panel renders inline in the transcript body (as a transcript row),
	// not pinned at the top. It appears above the specialist cards like a
	// chat message, the way todo/plan updates render inline.
	if m.altScreen && m.height > 0 {
		header := m.pinnedTitleBar(width)
		return m.scrollableTranscriptItemsView(header, bodyItems, footer, width, overlayForViewport)
	}

	bodyLayout := layoutTranscriptBodyItems(bodyItems)
	body := bodyLayout.String()
	if overlayForViewport != "" {
		body += "\n" + overlayForViewport + "\n"
	}
	return body + footer
}

// twoColumnTranscriptView renders the alt-screen chat into a left column and
// the context sidebar (FILES / PLAN / tokens) into a right column. The chat is
// produced by the existing scroll engine at the reduced chat-column width (via
// chatColumnWidth, which every frame/geometry caller already routes through),
// yielding exactly m.height lines at the column width; the sidebar block is
// built to the same height and joined row-by-row. Overlays/wizards never reach
// here — sidebarActive() returns false while any is up, falling back to the
// single-column path. Caller guarantees sidebarActive() && !subchat.active.
func (m model) twoColumnTranscriptView() string {
	chatW := m.chatColumnWidth()
	sidebarW := sidebarWidth(m.width)

	width := chatW

	suggestionOverlay := m.suggestionOverlay(width)
	bodyItems := m.transcriptBodyItems(width, "")
	footer := m.footerView(width)
	overlayForViewport := suggestionOverlay
	if m.transcriptEmpty() && !m.pending {
		overlayForViewport = ""
	}

	header := m.pinnedTitleBar(width)
	chatBlock := viewLines(m.scrollableTranscriptItemsView(header, bodyItems, footer, width, overlayForViewport))

	totalLines := len(chatBlock)

	// Right column: split vertically — top portion for agents/plan/files sidebar,
	// bottom portion for browser preview. Browser panel is shown when there has been
	// any browser activity; otherwise the full height goes to the sidebar.
	rightBlock := m.buildRightColumn(sidebarW, totalLines)
	rows := joinColumns(chatBlock, rightBlock, chatW, sidebarW)
	return strings.Join(rows, "\n")
}

// buildRightColumn assembles the right column (sidebar + optional browser panel)
// as exactly `height` lines at `width` cells each.
func (m model) buildRightColumn(width, height int) []string {
	// Always show the in-app split layout: sidebar on top, browser preview on
	// bottom. The optional terminal-browser process may mirror navigation, but
	// it never replaces this native preview surface.
	// Give the in-app browser enough vertical resolution to show a useful
	// fallback image. The browser is a first-class preview surface, so it gets
	// roughly half the right column rather than a thin status strip.
	browserH := height * 50 / 100
	if browserH < 10 {
		browserH = 10
	}
	if browserH > 24 {
		browserH = 24
	}
	if browserH >= height {
		browserH = height / 2
	}
	sidebarH := height - browserH - 1 // -1 for the divider line
	if sidebarH < 1 {
		sidebarH = 1
	}

	sidebarBlock := m.renderContextSidebar(width, sidebarH)
	divider := renderBrowserPanelDivider(width)
	browserBlock := m.renderBrowserPanel(width, browserH)

	out := make([]string, 0, height)
	out = append(out, sidebarBlock...)
	out = append(out, padStyledLine(divider, width))
	out = append(out, browserBlock...)

	// Ensure exactly height lines
	for len(out) < height {
		out = append(out, padStyledLine("", width))
	}
	return out[:height]
}

func (m model) titleBarInTranscriptBody() bool {
	return !m.altScreen && !m.headerPrinted
}

func (m model) pinnedTitleBar(width int) string {
	if !m.altScreen || m.height <= 0 {
		return ""
	}
	if m.secView.active {
		return m.secViewNavBar(width)
	}
	if m.voiceView.active {
		return m.voiceViewNavBar(width)
	}
	if m.knowledgeView.active {
		return m.knowledgeViewNavBar(width)
	}
	// The file drill-in replaces the title bar with its nav line (path + key
	// hints). Both are exactly one line, and every frame computation routes
	// through here, so the swap never desyncs the viewport geometry.
	if m.fileView.active {
		return m.fileViewNavBar(width)
	}
	if m.attachmentView.active {
		return m.attachmentViewNavBar(width)
	}
	return m.titleBar(width)
}

func (m model) footerView(width int) string {
	var footer strings.Builder
	if m.voiceView.active {
		return m.voiceViewFooter(width)
	}
	// While an ask-user questionnaire is active it REPLACES the composer box (the
	// text box becomes the questionnaire): render the tabbed prompt + status line and
	// skip the plan panel / idle hints / composer for a focused modal.
	if m.pendingAskUser != nil {
		footer.WriteString(renderAskUserQuestionnaire(*m.pendingAskUser, m.input.Value(), width))
		footer.WriteString("\n")
		footer.WriteString(m.statusLine(width))
		return footer.String()
	}
	// Pinned plan panel: sits directly above the composer so it stays visible
	// while the transcript scrolls underneath (a streaming turn no longer pushes
	// the plan off-screen). Budgeted to at most a third of the screen height; a
	// taller plan collapses to a one-line summary so the composer always stays
	// on screen. Skipped in the subchat drill-in: m.plan belongs to the PARENT
	// run, not the subagent/swarm child session being viewed there, so pinning it
	// above that composer would show unrelated state.
	if !m.subchat.active {
		if plan := m.renderPinnedPlanPanel(width, m.pinnedPlanMaxHeight()); plan != "" {
			footer.WriteString(plan)
			footer.WriteString("\n")
		}
		// Pong easter egg board (pong.go): sits above the composer like the
		// plan panel; empty string when the game is closed.
		if board := m.renderPongPanel(width); board != "" {
			footer.WriteString(board)
			footer.WriteString("\n")
		}
	}
	// The row above the composer: transient copy feedback takes priority; otherwise
	// a faint idle affordance — discoverable key hints on the left, a jump-to-bottom
	// cue on the right when scrolled up. Always one line (blank when nothing shows),
	// so the footer height is unchanged.
	if copyStatus := strings.TrimSpace(m.copyStatus); copyStatus != "" {
		footer.WriteString(rightAlignedLine(sonutTheme.ink.Render(copyStatus), width))
	} else if left, right := m.composerIdleHint(), m.jumpToBottomHint(); left != "" || right != "" {
		footer.WriteString(fitStyledLine(joinHeaderLine("  "+left, right, width), width))
	}
	footer.WriteString("\n")
	// A message typed while a run is active is queued for the next turn; show its
	// preview directly ABOVE the input box (not below), so it reads as "waiting to
	// send" sitting on top of what you're currently typing.
	if queued := renderQueuedMessagePreview(m.queuedMessage, width); queued != "" {
		footer.WriteString(queued)
		footer.WriteString("\n")
	}
	footer.WriteString(m.composerBox(width))
	if hint := m.composerDescriptionHint(width); hint != "" {
		footer.WriteString("\n")
		footer.WriteString(hint)
	}
	footer.WriteString("\n")
	footer.WriteString(m.statusLine(width))
	return footer.String()
}

// composerIdleHint returns a faint one-line key-shortcut hint shown above the
// composer on an empty, idle prompt, so the chord bindings are discoverable
// without opening the ? overlay. Empty while typing, during a run, in the
// full-screen transcript, or under any modal/overlay so it never competes for
// attention. Width-tiered so a narrow terminal only shows the essential pointer.
func (m model) composerIdleHint() string {
	// Managed (alt-screen) mode only: inline mode prints to native scrollback where
	// this footer row isn't a stable surface. Hidden while typing, during a run, in
	// the full-screen transcript, or under any modal/overlay.
	if m.pending {
		// While a run grinds, the idle row advertises the pong easter egg
		// (hidden once the board is up — the board has its own status line).
		if !m.altScreen || m.pong.active || m.composerValue() != "" ||
			!m.noBlockingModal() || m.subchat.active || m.suggestionsActive() ||
			m.transcriptDetailed || widthTier(m.width) == tierTiny {
			return ""
		}
		return sonutTheme.faint.Render("@ press spacebar to ping pong")
	}
	if !m.altScreen || m.composerValue() != "" || !m.noBlockingModal() ||
		m.subchat.active || m.suggestionsActive() || m.transcriptDetailed {
		return ""
	}
	var hint string
	switch widthTier(m.width) {
	case tierTiny:
		return "" // too cramped for a hint
	case tierNarrow:
		hint = "? shortcuts"
	case tierMedium:
		hint = "? shortcuts · Ctrl+S sidebar · Ctrl+E copy"
	default:
		hint = "? shortcuts · Ctrl+S sidebar · Ctrl+O detail · Ctrl+E copy · Shift+Tab mode"
	}
	return sonutTheme.faint.Render(hint)
}

// jumpToBottomHint returns a faint "↓ N more · PgDn" cue when the transcript is
// scrolled up (chatScrollOffset counts lines below the fold), so it's clear new
// output may be below and how to catch up. Empty at the bottom.
func (m model) jumpToBottomHint() string {
	if m.chatScrollOffset <= 0 {
		return ""
	}
	return sonutTheme.faint.Render(fmt.Sprintf("↓ %d more · PgDn", m.chatScrollOffset))
}

// pinnedPlanMaxHeight is the line budget for the pinned plan panel: at most a
// third of the screen, so even a long plan can't crowd out the transcript or
// the composer. Beyond this the panel collapses to its one-line summary. Falls
// back to a generous cap when the height isn't known yet (unmeasured/headless).
func (m model) pinnedPlanMaxHeight() int {
	if m.height <= 0 {
		return 12
	}
	budget := m.height / 3
	if budget < 3 {
		budget = 3
	}
	return budget
}

type tuiRect struct {
	x      int
	y      int
	width  int
	height int
}

func (r tuiRect) contains(x int, y int) bool {
	return x >= r.x && y >= r.y && x < r.x+r.width && y < r.y+r.height
}

func (r tuiRect) local(x int, y int) (int, int, bool) {
	if !r.contains(x, y) {
		return 0, 0, false
	}
	return x - r.x, y - r.y, true
}

type transcriptFrameLayout struct {
	width           int
	height          int
	headerRect      tuiRect
	bodyRect        tuiRect
	footerRect      tuiRect
	composerRect    tuiRect
	statusRect      tuiRect
	headerLines     []string
	bodyHeight      int
	footerLines     []string
	fullFooterLines []string
	footerClip      int
}

func (m model) scrollableTranscriptFrame(header string, footer string) transcriptFrameLayout {
	headerLines := viewLines(header)
	fullFooterLines := viewLines(footer)
	footerLines := append([]string(nil), fullFooterLines...)

	maxFooterLines := maxInt(0, m.height-1)
	if len(footerLines) > maxFooterLines {
		footerLines = footerLines[len(footerLines)-maxFooterLines:]
	}
	if len(headerLines)+len(footerLines) >= m.height {
		maxHeaderLines := maxInt(0, m.height-len(footerLines)-1)
		if len(headerLines) > maxHeaderLines {
			headerLines = headerLines[:maxHeaderLines]
		}
	}
	if len(headerLines)+len(footerLines) >= m.height {
		maxFooterLines = maxInt(0, m.height-len(headerLines)-1)
		if len(footerLines) > maxFooterLines {
			footerLines = footerLines[len(footerLines)-maxFooterLines:]
		}
	}

	bodyHeight := m.height - len(headerLines) - len(footerLines)
	if bodyHeight < 1 {
		bodyHeight = 1
	}
	width := m.chatColumnWidth()
	footerTop := len(headerLines) + bodyHeight
	frame := transcriptFrameLayout{
		width:           width,
		height:          m.height,
		headerRect:      tuiRect{width: width, height: len(headerLines)},
		bodyRect:        tuiRect{y: len(headerLines), width: width, height: bodyHeight},
		footerRect:      tuiRect{y: footerTop, width: width, height: len(footerLines)},
		headerLines:     headerLines,
		bodyHeight:      bodyHeight,
		footerLines:     footerLines,
		fullFooterLines: fullFooterLines,
		footerClip:      maxInt(0, len(fullFooterLines)-len(footerLines)),
	}
	frame.composerRect = frame.footerSubrect(viewLines(m.composerBox(width)))
	if len(fullFooterLines) > 0 {
		frame.statusRect = frame.footerLineRect(len(fullFooterLines) - 1)
	}
	return frame
}

func (f transcriptFrameLayout) footerSubrect(sequence []string) tuiRect {
	if len(sequence) == 0 || len(f.footerLines) == 0 {
		return tuiRect{}
	}
	top := lineSequenceIndex(f.fullFooterLines, sequence)
	if top < 0 {
		return tuiRect{}
	}
	visibleTop := maxInt(top, f.footerClip)
	visibleBottom := minInt(top+len(sequence), f.footerClip+len(f.footerLines))
	if visibleTop >= visibleBottom {
		return tuiRect{}
	}
	return tuiRect{
		y:      f.footerRect.y + visibleTop - f.footerClip,
		width:  f.width,
		height: visibleBottom - visibleTop,
	}
}

func (f transcriptFrameLayout) footerLineRect(line int) tuiRect {
	if line < f.footerClip || line >= f.footerClip+len(f.footerLines) {
		return tuiRect{}
	}
	return tuiRect{
		y:      f.footerRect.y + line - f.footerClip,
		width:  f.width,
		height: 1,
	}
}

func (m model) scrollableTranscriptLayoutView(header string, body transcriptBodyLayout, footer string, width int, overlay string) string {
	frame := m.scrollableTranscriptFrame(header, footer)
	window := transcriptViewportForLayout(body, frame, m.chatScrollOffset).window()

	bodyWindow := body.visibleLines(window)
	return m.renderScrollableTranscriptWindow(frame, bodyWindow, window, width, overlay)
}

func (m model) scrollableTranscriptItemsView(header string, items []transcriptBodyItem, footer string, width int, overlay string) string {
	frame := m.scrollableTranscriptFrame(header, footer)
	metrics := measureTranscriptBodyItems(items, m.transcriptBodyHeights)
	window := transcriptViewportForLayout(metrics, frame, m.chatScrollOffset).window()
	body := layoutVisibleTranscriptBodyItems(items, metrics, window)

	return m.renderScrollableTranscriptWindow(frame, body.lines, window, width, overlay)
}

func (m model) renderScrollableTranscriptWindow(frame transcriptFrameLayout, bodyWindow []string, window transcriptViewportWindow, width int, overlay string) string {
	for len(bodyWindow) < window.height {
		bodyWindow = append(bodyWindow, "")
	}
	bodyWindow = overlayViewportLines(bodyWindow, overlay, width)

	lines := make([]string, 0, len(frame.headerLines)+len(bodyWindow)+len(frame.footerLines))
	lines = append(lines, frame.headerLines...)
	lines = append(lines, bodyWindow...)
	lines = append(lines, frame.footerLines...)
	for index, line := range lines {
		lines[index] = fitStyledLine(line, width)
	}
	return strings.Join(lines, "\n")
}

func overlayViewportLines(lines []string, overlay string, width int) []string {
	if strings.TrimSpace(overlay) == "" || len(lines) == 0 {
		return lines
	}
	overlayLines := viewLines(overlay)
	if len(overlayLines) == 0 {
		return lines
	}
	left, overlayLines, overlayWidth := normalizeOverlayBlock(overlayLines, width)
	if overlayWidth <= 0 {
		return lines
	}
	// Scrim: dim the whole transcript backdrop so a floating overlay (slash-command
	// palette, picker, wizard) clearly stands out instead of blending into the live
	// chat behind it. Header and composer are rendered separately and stay bright.
	for index := range lines {
		lines[index] = scrimViewportLine(lines[index], width)
	}
	start := maxInt(0, (len(lines)-len(overlayLines))/2)
	for offset, line := range overlayLines {
		target := start + offset
		if target >= len(lines) {
			break
		}
		lines[target] = overlayViewportLine(lines[target], line, left, overlayWidth, width)
	}
	return lines
}

// scrimViewportLine dims one backdrop line: it strips the line's own colors and
// re-renders the text faint, so the dimmed transcript recedes behind the overlay.
// Blank lines are left untouched.
func scrimViewportLine(line string, width int) string {
	plain := ansi.Strip(line)
	if strings.TrimSpace(plain) == "" {
		return line
	}
	return sonutTheme.faint.Render(plain)
}

func normalizeOverlayBlock(lines []string, width int) (int, []string, int) {
	left := -1
	for _, line := range lines {
		if strings.TrimSpace(ansi.Strip(line)) == "" {
			continue
		}
		spaces := leadingPlainSpaces(line)
		if left < 0 || spaces < left {
			left = spaces
		}
	}
	if left < 0 {
		left = 0
	}
	left = minInt(left, maxInt(0, width-1))

	trimmed := make([]string, 0, len(lines))
	blockWidth := 0
	for _, line := range lines {
		if left > 0 && len(line) >= left {
			line = line[left:]
		}
		trimmed = append(trimmed, line)
		blockWidth = maxInt(blockWidth, lipgloss.Width(line))
	}
	blockWidth = minInt(blockWidth, maxInt(0, width-left))
	return left, trimmed, blockWidth
}

func leadingPlainSpaces(line string) int {
	spaces := 0
	for spaces < len(line) && line[spaces] == ' ' {
		spaces++
	}
	return spaces
}

func overlayViewportLine(base string, overlay string, left int, overlayWidth int, width int) string {
	if width <= 0 {
		return ""
	}
	left = clampInt(left, 0, width)
	overlayWidth = minInt(overlayWidth, width-left)
	rightStart := minInt(width, left+overlayWidth)

	base = fitStyledLine(base, width)
	prefix := padStyledLine(ansi.Cut(base, 0, left), left)
	panel := padStyledLine(overlay, overlayWidth)
	suffix := padStyledLine(ansi.Cut(base, rightStart, width), width-rightStart)
	return prefix + panel + suffix
}

func padStyledLine(line string, width int) string {
	if width <= 0 {
		return ""
	}
	line = fitStyledLine(line, width)
	if pad := width - lipgloss.Width(line); pad > 0 {
		line += strings.Repeat(" ", pad)
	}
	return line
}

func viewLines(value string) []string {
	if value == "" {
		return nil
	}
	return strings.Split(strings.TrimSuffix(value, "\n"), "\n")
}

func (m model) scrollChat(delta int) model {
	if !m.altScreen || delta == 0 {
		return m
	}
	viewport, ok := m.chatTranscriptViewport()
	if !ok {
		return m
	}
	m.chatScrollOffset = viewport.scroll(delta).offset
	if m.chatScrollOffset == 0 {
		m.chatBodyLines = 0
	}
	return m
}

func (m model) chatMaxScrollOffset() int {
	_, maxOffset := m.chatScrollMetrics()
	return maxOffset
}

func (m model) chatScrollMetrics() (int, int) {
	viewport, ok := m.chatTranscriptViewport()
	if !ok {
		return 0, 0
	}
	return viewport.totalLines, viewport.maxOffset()
}

func (m model) chatTranscriptViewport() (transcriptViewport, bool) {
	if !m.altScreen || m.height <= 0 {
		return transcriptViewport{}, false
	}
	width := m.chatColumnWidth()
	items := m.transcriptBodyItems(width, "")
	body := measureTranscriptBodyItems(items, m.transcriptBodyHeights)
	frame := m.scrollableTranscriptFrame(m.pinnedTitleBar(width), m.footerView(width))
	return transcriptViewportForLayout(body, frame, m.chatScrollOffset), true
}

// syncChatScroll pins the viewport to what the user is reading. The scroll offset
// is measured from the bottom, so when the transcript grows (streaming) the window
// would otherwise follow the new bottom and drag the user off their spot. While
// the user has scrolled up, shift the offset by however many lines the body changed
// so the absolute view holds; at the bottom (offset 0) it follows normally. Only the
// scrolled-up path renders the body, so the common case stays cheap.
func (m model) syncChatScroll() model {
	if !m.altScreen || m.chatScrollOffset <= 0 {
		// At the bottom (or inline mode): follow the tail; reset the pin baseline.
		m.chatBodyLines = 0
		return m
	}
	current, maxOffset := m.chatScrollMetrics()
	m.chatScrollOffset = clampInt(m.chatScrollOffset, 0, maxOffset)
	if m.chatScrollOffset <= 0 {
		m.chatBodyLines = 0
		return m
	}
	if m.chatBodyLines == 0 {
		// Just scrolled up: establish the baseline, no adjustment this frame.
		m.chatBodyLines = current
		return m
	}
	// Shift by the signed delta so the absolute view holds whether the body grew
	// (streaming appended lines) or shrank (a tool card collapsed, transcript
	// cleared). Clamp at zero so a large shrink lands the user back at the tail
	// rather than underflowing past it.
	m.chatScrollOffset = clampInt(m.chatScrollOffset+current-m.chatBodyLines, 0, maxOffset)
	m.chatBodyLines = current
	return m
}

func (m model) chatPageScrollLines() int {
	if m.height <= 0 {
		return 10
	}
	return maxInt(3, m.height-8)
}

// interimBlock renders the live assistant text while a turn streams. It uses
// the same lightweight markdown renderer as completed assistant rows, so
// tables and simple formatting stabilize as soon as enough tokens arrive.
// Before the first delta arrives it falls back to the spinner so the surface
// still shows liveness. The cursor needs no ticker — it appears exactly while
// pending.
func (m model) interimBlock(width int) string {
	text := strings.TrimRight(m.streamingText, "\n")
	blocks := []string{}
	hasText := strings.TrimSpace(text) != ""

	if !hasText {
		// Pure thinking phase: show a compact natural tail when it is safe to do
		// so. The full provider reasoning stream remains private and is never
		// rendered as a transcript.
		if preview := reasoningPreviewLines(m.streamingReasoning, width); len(preview) > 0 {
			blocks = append(blocks, sonutTheme.faint.Render("> thinking .."))
			blocks = append(blocks, strings.Join(preview, "\n"))
		}
		if writing := m.streamingToolCallView(width); writing != "" {
			blocks = append(blocks, writing)
		}
		if len(blocks) == 0 {
			// Prefill can take longer than the first answer delta. Show a natural
			// phase label when the provider has not emitted reasoning yet; this is
			// UI progress, not a claim about the model's private thoughts.
			blocks = append(blocks, sonutTheme.faint.Render("> thinking .."))
			if phase := m.silentThinkingPhase(); phase != "" {
				blocks = append(blocks, "  "+sonutTheme.faint.Render(phase))
			}
		}
		return strings.Join(blocks, "\n")
	}

	// Text is streaming: show text and the optional tool-call card. Raw private
	// reasoning is deliberately omitted once answer text is visible.
	// The working indicator lives in the footer status line.
	lines := renderStreamingAssistantMarkdownText(text, assistantMeasure(width), width)
	for index, line := range lines {
		lines[index] = m.styleStreamingLine(line, index, len(lines))
	}
	lines = m.appendStreamingCursor(lines, width)
	blocks = append(blocks, strings.Join(lines, "\n"))
	if writing := m.streamingToolCallView(width); writing != "" {
		blocks = append(blocks, writing)
	}
	return strings.Join(blocks, "\n")
}

// silentThinkingPhase gives the user a truthful, natural description when a
// provider is busy preparing or waiting for its first output token and has not
// emitted a reasoning delta. It deliberately describes observable progress,
// never pretending to be a verbatim model thought.
func (m model) silentThinkingPhase() string {
	if !m.pending {
		return ""
	}
	if m.prefillStatus != "" {
		switch prefillActivityLabel(m.prefillStatus) {
		case "analyzing input":
			return "preparing the request context…"
		default:
			return strings.TrimSuffix(prefillActivityLabel(m.prefillStatus), "…") + "…"
		}
	}
	if hint := m.quietGenerationHint(); strings.Contains(hint, "first token") {
		return "waiting for the first response token…"
	}
	return "preparing a response…"
}

// workingStatusLine renders the live "working" indicator shown on every pending
// render: an animated spinner, the rotating working verb, and the elapsed time.
// It is shown even once partial text has streamed so an upstream stall never
// looks like a frozen terminal — the spinner tick (~80ms, time-based) drives the
// re-render, so the elapsed clock keeps advancing for ANY provider/model even
// when no stream data arrives.
// spinnerGlyph is the liveness glyph every renderer should use instead of
// m.spinner.View() directly: the animated frame normally, or a steady dot under
// reduced motion. The caller applies its own color; liveness is preserved by the
// advancing elapsed timer, so the static glyph never reads as frozen.
func (m model) spinnerGlyph() string {
	if m.reducedMotion {
		return "•"
	}
	return m.spinner.View()
}

// workingActivity labels what the agent is doing right now for the working
// status line. The phase is deliberately user-facing: a provider prefill or
// planner pass is real work even though no answer tokens are visible yet.
func (m model) workingActivity() string {
	if m.prefillStatus != "" {
		return prefillActivityLabel(m.prefillStatus)
	}
	if m.streamCallName != "" {
		return "generating " + strings.ToLower(toolDisplayName(m.streamCallName))
	}
	if strings.TrimSpace(m.streamingReasoning) != "" {
		return "thinking through response"
	}
	if strings.TrimSpace(m.streamingText) != "" {
		return "writing"
	}
	if m.pending {
		if m.prefillComplete {
			return "thinking through response"
		}
		return "analyzing input"
	}
	return effortDisplayName(string(m.reasoningEffort))
}

// prefillActivityLabel translates internal provider/preprocessing messages into
// concise labels suitable for the one-line status bar. Keep progress details so
// users can tell that the input is actively moving through context preparation.
func prefillActivityLabel(status string) string {
	status = strings.TrimSpace(status)
	if status == "" {
		return "analyzing input"
	}

	const progressMarker = "…"
	if marker := strings.Index(status, progressMarker); marker >= 0 {
		progress := strings.TrimSpace(status[marker+len(progressMarker):])
		if progress != "" {
			return "analyzing input… " + progress
		}
	}
	return "analyzing input"
}

// toolCardSuppressedInTranscript reports tools whose transcript card is redundant
// because a dedicated UI surface already shows their state: Task (its specialist
// card) and update_plan (the pinned plan panel + PLAN sidebar). Their session
// events are still recorded; only the visible card is skipped.
func toolCardSuppressedInTranscript(name string) bool {
	return name == "Task" || name == "update_plan"
}

// workingLabels rotates through nature/fun-inspired phrases instead of a
// static "Working". Each phrase shows for ~3 s (40 ticks × ~80 ms each).
var workingLabels = []string{
	"Working",
	"Thinking",
	"Planting seeds",
	"Brewing ideas",
	"Chasing fireflies",
	"Reading the wind",
	"Following rivers",
	"Gathering light",
	"Turning leaves",
	"Riding currents",
	"Counting stars",
	"Watching clouds",
	"Growing roots",
	"Catching sparks",
	"Weaving threads",
	"Climbing slowly",
	"Mapping the terrain",
	"Listening closely",
	"Finding the path",
	"Building bridges",
}

func workingLabel(phase int) string {
	idx := (phase / activityLabelLifetime) % len(workingLabels)
	return workingLabels[idx]
}

func (m model) workingStatusLine() string {
	// Cosine ripple FX: the working label breathes through a cold-to-warm theme
	// ramp, rotating to a new nature/fun phrase every ~3 s. Wavelength tracks the
	// label length so the gradient always spans the full word.
	label := workingLabel(m.spinnerPhase)
	working := activityLabelView(label, m.spinnerPhase, m.reducedMotion)
	line := sonutTheme.accent.Render(m.spinnerGlyph()) + " " + working
	// Prefill progress: while the model is processing the prompt, show the percentage
	// instead of the normal working activity verb.
	if m.prefillStatus != "" {
		line += sonutTheme.faint.Render("  ·  " + prefillActivityLabel(m.prefillStatus))
	} else {
		// Phase label so a long, output-less step reads as live progress rather than a
		// frozen screen: "writing" while the answer streams, "thinking" otherwise
		// (reasoning, waiting on the model, or running a tool).
		line += sonutTheme.faint.Render("  ·  " + m.workingActivity())
	}
	if !m.turnStartedAt.IsZero() {
		line += sonutTheme.faint.Render("  ·  " + formatWorkingElapsed(m.now().Sub(m.turnStartedAt)))
	}
	// Live token estimate so the working line visibly climbs as the model reasons
	// and writes, instead of a static figure. Shown from the start of the turn (at
	// 0) so the counter is never missing — the authoritative totals stay in the
	// status line and sidebar; this is the at-a-glance "it's generating" pulse.
	line += sonutTheme.faint.Render("  ·  " + m.workingTokenIndicator())
	// If the model has gone quiet (no streamed text, reasoning, OR tool-call output
	// for a while — common when a provider buffers a large tool call instead of
	// streaming it), say so plainly with an advancing timer, so a long silent
	// generation never reads as a frozen screen. Only on the working line when the
	// context sidebar isn't showing it — the sidebar's ACTIVITY pulse carries it
	// whenever the sidebar is up, so it never appears in both places at once.
	if !m.sidebarActive() {
		if hint := m.quietGenerationHint(); hint != "" {
			line += sonutTheme.amber.Render("  ·  " + hint)
		}
	}
	// A second line carries live plan progress (how far along + the current step)
	// so a long working stretch shows the task advancing without consulting the
	// sidebar. Replaces the old per-call update_plan transcript cards. Empty when
	// there is no active plan.
	if planLine := m.workingPlanLine(); planLine != "" {
		line += "\n" + planLine
	}
	return line
}

// workingPlanLine is the optional second line under the working indicator: the
// plan's done/total and the step currently in progress. Empty when there is no
// plan or the plan is already complete.
func (m model) workingPlanLine() string {
	if m.plan.isEmpty() || m.plan.isComplete() {
		return ""
	}
	total := len(m.plan.steps)
	done := 0
	for _, step := range m.plan.steps {
		if step.status == "completed" || step.status == "failed" {
			done++
		}
	}
	text := fmt.Sprintf("· plan %d/%d", done, total)
	if current := truncateStep(currentStepContent(m.plan.steps), 48); current != "" {
		text += " · " + current
	}
	return "  " + sonutTheme.faint.Render(text)
}

// workingTokenIndicator renders a live "↑ <n> tok" estimate of the tokens
// generated so far in the current turn, so the working line keeps moving while
// the model reasons and writes. It is shown for the whole turn — starting at
// "↑ 0 tok" before the first delta and climbing — so the counter never blinks
// out. Providers only report exact usage when a step finishes, so this estimates
// from the streamed reasoning+answer length at the usual ~4 characters per
// token; turnStreamedRunes accumulates across the whole turn (it survives the
// per-segment buffer clears), giving a monotonic climb that resets on the next
// turn.
func (m model) workingTokenIndicator() string {
	if m.turnStreamedRunes == 0 && m.prefillTotal > 0 {
		return "↑ " + humanCount(m.prefillProcessed) + "/" + humanCount(m.prefillTotal) + " in"
	}
	tokens := m.turnStreamedRunes / 4
	if m.turnStreamedRunes > 0 && tokens < 1 {
		tokens = 1
	}
	return "↑ " + humanCount(tokens) + " tok"
}

// quietWorkingHint is how long the stream must be silent (no streamed text,
// reasoning, or tool-call output) during an active turn before the working line
// calls out that it's still generating — so a provider that buffers a big tool
// call (instead of streaming the file as it's written) doesn't read as stuck.
// quietWorkingHint is how long a pending turn may sit with no streamed tokens
// before the footer names the wait. Lowered from 8s → 2s so hybrid local
// prefill (often 30–90s before first token) is labeled immediately instead of
// looking like a frozen UI.
const quietWorkingHint = 2 * time.Second

// quietGenerationHint returns a "still generating…" cue with an advancing
// quiet-timer when the active turn has produced no streamed output for a while,
// else "". The advancing number is itself the liveness signal.
//
// Past half the provider's idle timeout, the cue escalates to name what's
// actually happening and when @code will act on its own: a heartbeating-but-
// silent stream (observed on chatgpt/gpt-5.x and ollama reasoning models,
// see providerio.ErrStreamStalled) is bounded by the content-stall watchdog at
// providerio.ContentStallTimeout(idle), but until it fires this exact same
// plain "still generating… Xs" text is indistinguishable from a genuine hang —
// the ticking number was the only signal, and it looks identical whether real
// (if slow) content is coming or nothing ever will.
func (m model) quietGenerationHint() string {
	if m.activeRunID == 0 {
		return ""
	}
	// Prefill progress already explains the wait — don't also yell "still
	// generating" (that was the irritating "100s idle" read during healthy
	// prompt processing on local hybrid models).
	if m.prefillStatus != "" {
		return ""
	}
	last := m.lastStreamActivity
	if last.IsZero() {
		last = m.turnStartedAt
	}
	if last.IsZero() {
		return ""
	}
	quiet := m.now().Sub(last)
	if quiet < quietWorkingHint {
		return ""
	}
	// Zero generated tokens after the quiet threshold on a local model almost always
	// means cold prefill before the first SSE byte — name it instead of
	// "still generating" so the wait isn't mistaken for a hang.
	if m.turnStreamedRunes == 0 && quiet >= quietWorkingHint {
		return "waiting for first token (prompt prefill)… " + formatWorkingElapsed(quiet)
	}
	if idleTimeout := providerio.ResolveStreamIdleTimeout(0); idleTimeout > 0 && quiet >= idleTimeout/2 {
		ceiling := providerio.ContentStallTimeout(idleTimeout)
		return fmt.Sprintf("still generating… %s — unusually quiet, @code will auto-recover by ~%s if it doesn't resume", formatWorkingElapsed(quiet), formatWorkingElapsed(ceiling))
	}
	return "still generating… " + formatWorkingElapsed(quiet)
}

// formatWorkingElapsed renders a turn's running time compactly: "8s", "1m04s".
func formatWorkingElapsed(d time.Duration) string {
	if d < 0 {
		d = 0
	}
	if d < time.Minute {
		return fmt.Sprintf("%ds", int(d.Seconds()))
	}
	return fmt.Sprintf("%dm%02ds", int(d/time.Minute), int(d.Seconds())%60)
}

// reasoningPreviewLines renders one compact, natural tail of the in-flight
// reasoning stream. The model keeps thinking enabled, but the UI should not
// turn private scratch work into a second transcript or a prompt checklist.
// The live expanded state intentionally keeps using this compact preview.
func reasoningPreviewLines(reasoning string, width int) []string {
	line := compactReasoningTail(reasoning)
	if line == "" {
		return nil
	}
	avail := width - 4
	if avail < 8 {
		avail = 8
	}
	line = truncateRunes(line, avail*2)
	if len([]rune(line)) <= avail {
		return []string{"  " + sonutTheme.faint.Render(line)}
	}
	runes := []rune(line)
	return []string{
		"  " + sonutTheme.faint.Render(string(runes[:avail])),
		"  " + sonutTheme.faint.Render(string(runes[avail:])),
	}
}

func compactReasoningTail(reasoning string) string {
	// Redact prompt echoes before selecting the tail. A model can emit several
	// ordinary-looking lines around one quoted instruction, so filtering only
	// the final line would still make the preview vulnerable to prompt leakage.
	reasoning = redactSystemPromptEcho(reasoning)
	lines := strings.Split(strings.TrimSpace(reasoning), "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		line := cleanReasoningPreviewLine(lines[i])
		if line == "" || reasoningLooksLikePromptOrChecklist(line) {
			continue
		}
		return line
	}
	return ""
}

func cleanReasoningPreviewLine(line string) string {
	line = strings.Join(strings.Fields(line), " ")
	line = strings.TrimSpace(strings.Trim(line, "`"))
	line = strings.ReplaceAll(line, "**", "")
	line = strings.ReplaceAll(line, "__", "")
	return strings.TrimSpace(line)
}

func reasoningLooksLikePromptOrChecklist(line string) bool {
	lower := strings.ToLower(strings.TrimSpace(line))
	for _, marker := range []string{
		"system prompt", "you are @code", "you are atcode", "check constraints",
		"final output generation", "analyze user input", "analyze system prompt",
		"formulate response", "output matches response", "response matches constraints",
		"thinking process", "chain of thought", "internal reasoning", "scratchpad",
		"assistant analysis", "tool call", "tool_calls", "json schema",
		"<analysis", "</analysis", "<system", "</system", "<tool", "</tool",
		"plan:", "steps:",
	} {
		if strings.Contains(lower, marker) {
			return true
		}
	}
	if strings.HasPrefix(lower, "-") || strings.HasPrefix(lower, "*") {
		return true
	}
	if strings.HasPrefix(lower, "#") || strings.HasPrefix(lower, "{") || strings.HasPrefix(lower, "[") {
		return true
	}
	for i := 0; i < len(lower); i++ {
		if lower[i] < '0' || lower[i] > '9' {
			break
		}
		if i+1 < len(lower) && lower[i+1] == '.' {
			return true
		}
	}
	return false
}

// previewTail returns the last `width` runes of s, prefixed with "…" when text
// was dropped, so a streaming preview shows the newest content. s is plain text
// (reasoning deltas carry no ANSI), so rune counting is a safe width proxy.
func previewTail(s string, width int) string {
	runes := []rune(s)
	if width <= 0 || len(runes) <= width {
		return s
	}
	if width == 1 {
		return string(runes[len(runes)-1:])
	}
	return "…" + string(runes[len(runes)-(width-1):])
}

func (m model) appendStreamingCursor(lines []string, width int) []string {
	// Pulse the caret on the shared spinner clock so the typing edge reads as alive
	// even during fade-tick gaps or upstream stalls. Width-stable (bright ↔ dim,
	// never on/off, so the line never jitters). Steady bright under reduced motion.
	cursor := sonutTheme.accent.Render("▌")
	if !m.reducedMotion && (m.spinnerPhase/6)%2 == 1 {
		cursor = sonutTheme.faint.Render("▌")
	}
	if len(lines) == 0 {
		return []string{cursor}
	}
	last := len(lines) - 1
	if width > 0 && lipgloss.Width(lines[last])+1 > width {
		return append(lines, cursor)
	}
	lines[last] += cursor
	return lines
}

// composerLine renders the borderless composer.
func (m model) composerLine(width int) string {
	input := m.input
	hideInputForSuggestions := m.suggestionsActive() && (!m.suggestionsAreFiles || fileSuggestionOnlyInput(m.input.Value()))
	if hideInputForSuggestions {
		input.SetValue("")
		input.Placeholder = ""
		input.CursorEnd()
	}
	state := composerState{text: input.Value(), cursor: input.Position()}
	if m.composerActive {
		state = m.composer
	}
	if hideInputForSuggestions {
		state = composerState{}
	}
	argumentHint := commandArgumentHintForInput(input.Value())
	if argumentHint != "" && input.Position() != len([]rune(input.Value())) {
		argumentHint = ""
	}
	if argumentHint != "" {
		input.SetWidth(0)
		return fitStyledLine(commandArgumentHintComposerLine(input, argumentHint), width)
	}
	previews := validComposerPastePreviews(state, m.composerPastePreviews)
	displayState := composerDisplayStateForPastePreviews(state, previews)
	displaySelection := composerSelectionState{}
	if start, end, ok := m.composerSelection.rangeFor(state); ok {
		displaySelection = composerSelectionState{
			active: true,
			anchor: composerDisplayCursorForPastePreviews(start, previews),
			cursor: composerDisplayCursorForPastePreviews(end, previews),
		}
	}
	return renderComposerInput(input, displayState, width, m.composerCursorVisible, displaySelection)
}

type composerVisualLine struct {
	first bool
	start int
	end   int
}

func renderComposerInput(input textinput.Model, state composerState, width int, cursorVisible bool, selection composerSelectionState) string {
	state = normalizeComposerState(state)
	if width <= 0 {
		return ""
	}
	if state.text == "" {
		// Empty box: show a (blinking) cursor before the placeholder so the focused
		// input always has a visible caret. A plain space when blinked off keeps the
		// placeholder column stable.
		cursor := " "
		if cursorVisible {
			cursor = composerCursor(" ")
		}
		return fitStyledLine(composerVisualLinePrefix(input, true)+cursor+sonutTheme.faint.Render(input.Placeholder), width)
	}

	segments, cursorLine := composerVisibleVisualLines(input, state, width)
	lines := make([]string, 0, len(segments))
	for index, segment := range segments {
		lines = append(lines, fitStyledLine(renderComposerVisualLine(input, state, segment, index == cursorLine, cursorVisible, selection), width))
	}
	return strings.Join(lines, "\n")
}

func composerVisibleVisualLines(input textinput.Model, state composerState, width int) ([]composerVisualLine, int) {
	segments := composerWrappedVisualLines(input, state, width)
	cursorLine := composerCursorVisualLine(segments, state.cursor)
	if len(segments) <= composerMaxVisibleLines {
		return segments, cursorLine
	}
	start := clamp(cursorLine-composerMaxVisibleLines+1, 0, len(segments)-composerMaxVisibleLines)
	end := start + composerMaxVisibleLines
	cursorLine -= start
	segments = segments[start:end]
	if len(segments) > 0 {
		segments[0].first = true
	}
	return segments, cursorLine
}

func composerWrappedVisualLines(input textinput.Model, state composerState, width int) []composerVisualLine {
	runes := []rune(state.text)
	segments := []composerVisualLine{}
	first := true
	start := 0
	for index, r := range runes {
		if r != '\n' {
			continue
		}
		segments = appendComposerWrappedVisualLines(segments, input, runes, start, index, width, &first)
		start = index + 1
	}
	segments = appendComposerWrappedVisualLines(segments, input, runes, start, len(runes), width, &first)
	return segments
}

func appendComposerWrappedVisualLines(segments []composerVisualLine, input textinput.Model, runes []rune, start int, end int, width int, first *bool) []composerVisualLine {
	if start >= end {
		segments = append(segments, composerVisualLine{first: *first, start: start, end: end})
		*first = false
		return segments
	}
	for start < end {
		lineFirst := *first
		measure := maxInt(1, width-lipgloss.Width(composerVisualLinePrefix(input, lineFirst)))
		split := start
		used := 0
		for split < end {
			nextWidth := lipgloss.Width(string(runes[split]))
			if used+nextWidth > measure {
				break
			}
			used += nextWidth
			split++
		}
		if split == start {
			split++
		}
		segments = append(segments, composerVisualLine{first: lineFirst, start: start, end: split})
		*first = false
		start = split
	}
	return segments
}

func composerCursorVisualLine(segments []composerVisualLine, cursor int) int {
	if len(segments) == 0 {
		return 0
	}
	for index, segment := range segments {
		if cursor < segment.start || cursor > segment.end {
			continue
		}
		if cursor == segment.end && index+1 < len(segments) && segments[index+1].start == cursor {
			continue
		}
		return index
	}
	return len(segments) - 1
}

func renderComposerVisualLine(input textinput.Model, state composerState, segment composerVisualLine, hasCursor bool, cursorVisible bool, selection composerSelectionState) string {
	runes := []rune(state.text)
	prefix := composerVisualLinePrefix(input, segment.first)
	textStyle := sonutTheme.ink.Inline(true)
	selectionStart, selectionEnd, hasSelection := selection.rangeFor(state)
	// Render ordinary composer text through the same compact Markdown path as
	// plan rows. Selection mode remains character-by-character so selection
	// highlighting and cursor geometry stay exact while editing.
	if !hasSelection {
		text := string(runes[segment.start:segment.end])
		cursorOffset := -1
		if hasCursor {
			cursorOffset = clamp(state.cursor-segment.start, 0, len([]rune(text)))
			textRunes := []rune(text)
			textRunes = append(textRunes, 0)
			copy(textRunes[cursorOffset+1:], textRunes[cursorOffset:])
			textRunes[cursorOffset] = 0
			text = string(textRunes)
		}
		rendered := styleAssistantMarkdownLine(renderMarkdownInline(text), textStyle)
		if cursorOffset >= 0 {
			cursor := ""
			if cursorVisible {
				cursor = composerCursor(" ")
			}
			rendered = strings.Replace(rendered, "\x00", cursor, 1)
		}
		return prefix + rendered
	}
	cursorIndex := -1
	if hasCursor {
		cursorIndex = clamp(state.cursor, segment.start, segment.end)
	}

	var line strings.Builder
	line.WriteString(prefix)
	for index := segment.start; index < segment.end; index++ {
		cell := string(runes[index])
		switch {
		case index == cursorIndex && cursorVisible:
			line.WriteString(composerCursor(cell))
		case hasSelection && index >= selectionStart && index < selectionEnd:
			line.WriteString(sonutTheme.selection.Render(cell))
		default:
			line.WriteString(textStyle.Render(cell))
		}
	}
	if cursorIndex == segment.end && cursorVisible {
		line.WriteString(composerCursor(" "))
	}
	return line.String()
}

func composerVisualLinePrefix(input textinput.Model, first bool) string {
	if first {
		return sonutTheme.userPrompt.Render(input.Prompt)
	}
	return "  "
}

func composerDisplayStateForPastePreviews(state composerState, previews []composerPastePreview) composerState {
	state = normalizeComposerState(state)
	valid := validComposerPastePreviews(state, previews)
	if len(valid) == 0 {
		return state
	}
	runes := []rune(state.text)
	display := make([]rune, 0, len(runes))
	last := 0
	for _, preview := range valid {
		display = append(display, runes[last:preview.start]...)
		display = append(display, []rune(preview.label)...)
		last = preview.end
	}
	display = append(display, runes[last:]...)
	return composerState{
		text:   string(display),
		cursor: composerDisplayCursorForPastePreviews(state.cursor, valid),
	}
}

func composerDisplayCursorForPastePreviews(cursor int, previews []composerPastePreview) int {
	delta := 0
	for _, preview := range previews {
		labelLen := len([]rune(preview.label))
		hiddenLen := preview.end - preview.start
		displayStart := preview.start + delta
		switch {
		case cursor <= preview.start:
			return cursor + delta
		case cursor <= preview.end:
			return displayStart + labelLen
		default:
			delta += labelLen - hiddenLen
		}
	}
	return cursor + delta
}

func (m model) moveComposerVisualCursor(direction int) (model, bool) {
	if direction == 0 {
		return m, false
	}
	width := chatWidth(m.width)
	if width < 8 {
		return m, false
	}
	input := m.input
	state := m.currentComposerState()
	state = normalizeComposerState(state)
	if state.text == "" {
		return m, false
	}
	previews := validComposerPastePreviews(state, m.composerPastePreviews)
	displayState := composerDisplayStateForPastePreviews(state, previews)
	segments := composerWrappedVisualLines(input, displayState, maxInt(1, width-4))
	if len(segments) <= 1 {
		return m, false
	}
	cursorLine := composerCursorVisualLine(segments, displayState.cursor)
	targetLine := clamp(cursorLine+direction, 0, len(segments)-1)
	if targetLine == cursorLine {
		return m, true
	}
	column := composerVisualCursorColumn(displayState, segments[cursorLine])
	displayState.cursor = composerCursorForVisualColumn(displayState, segments[targetLine], column)
	state.cursor = composerOriginalCursorForPastePreviews(displayState.cursor, previews)
	m.setComposerState(state)
	return m, true
}

func composerOriginalCursorForPastePreviews(displayCursor int, previews []composerPastePreview) int {
	if len(previews) == 0 {
		return displayCursor
	}
	delta := 0
	for _, preview := range previews {
		labelLen := len([]rune(preview.label))
		hiddenLen := preview.end - preview.start
		displayStart := preview.start + delta
		displayEnd := displayStart + labelLen
		switch {
		case displayCursor <= displayStart:
			return displayCursor - delta
		case displayCursor <= displayEnd:
			return preview.end
		default:
			delta += labelLen - hiddenLen
		}
	}
	return displayCursor - delta
}

func composerVisualCursorColumn(state composerState, segment composerVisualLine) int {
	state = normalizeComposerState(state)
	runes := []rune(state.text)
	cursor := clamp(state.cursor, segment.start, segment.end)
	column := 0
	for index := segment.start; index < cursor && index < len(runes); index++ {
		column += lipgloss.Width(string(runes[index]))
	}
	return column
}

func composerCursorForVisualColumn(state composerState, segment composerVisualLine, column int) int {
	state = normalizeComposerState(state)
	runes := []rune(state.text)
	used := 0
	for index := segment.start; index < segment.end && index < len(runes); index++ {
		width := lipgloss.Width(string(runes[index]))
		if used+width > column {
			return index
		}
		used += width
	}
	return segment.end
}

func commandArgumentHintComposerLine(input textinput.Model, argumentHint string) string {
	hintRunes := []rune(argumentHint)
	if len(hintRunes) == 0 {
		return input.View()
	}
	displayValue := strings.TrimRightFunc(input.Value(), unicode.IsSpace)
	return sonutTheme.userPrompt.Render(input.Prompt) +
		sonutTheme.ink.Inline(true).Render(displayValue) +
		sonutTheme.faint.Render(" ") +
		composerCursor(sonutTheme.faint.Render(string(hintRunes[0]))) +
		sonutTheme.faint.Render(string(hintRunes[1:]))
}

func composerCursor(char string) string {
	return sonutTheme.selection.Render(char)
}

func commandArgumentHintForInput(value string) string {
	command := parseCommand(value)
	if command.name == "" || strings.TrimSpace(command.text) != "" {
		return ""
	}
	return commandRequiredInputHint(command.name)
}

func (m model) composerBox(width int) string {
	if width < 8 {
		return fitStyledLine(m.composerLine(width), width)
	}
	innerWidth := maxInt(1, width-4)
	content := m.composerLine(innerWidth)
	lines := strings.Split(content, "\n")

	rendered := make([]string, 0, len(lines)+3)
	rendered = append(rendered, sonutTheme.lineStrong.Render("╭"+strings.Repeat("─", width-2)+"╮"))
	// Attachment chips ([Image #1] …) render INSIDE the box as a Grok-style bar
	// above the input line — path-free, accent-tinted, clickable to inspect.
	if chips := renderAttachmentChips(m.pendingImageLabels, m.pendingDocuments); chips != "" {
		// Accent on the bar so staged attachments read as a distinct strip
		// (grok-build prompt bar), not plain muted body text.
		styled := sonutTheme.accent.Render(chips)
		fitted := fitStyledLine(styled, innerWidth)
		pad := strings.Repeat(" ", maxInt(0, innerWidth-lipgloss.Width(fitted)))
		rendered = append(rendered, sonutTheme.lineStrong.Render("│ ")+fitted+pad+sonutTheme.lineStrong.Render(" │"))
	}
	for _, line := range lines {
		fitted := fitStyledLine(line, innerWidth)
		pad := strings.Repeat(" ", maxInt(0, innerWidth-lipgloss.Width(fitted)))
		rendered = append(rendered, sonutTheme.lineStrong.Render("│ ")+fitted+pad+sonutTheme.lineStrong.Render(" │"))
	}
	rendered = append(rendered, m.composerDividerLine(width))
	return strings.Join(rendered, "\n")
}

// composerDescriptionHint returns the description line that sits below the
// composer box, claude-code style, when the input is a single unambiguous
// slash command. Returns "" when the user is mid-prompt, the palette is closed,
// or more than one command matches. Slash commands only; the @file palette
// already shows its rows. The inline argument hint ([low|medium|...]) is
// unchanged and continues to render inside the composer box.
func (m model) composerDescriptionHint(width int) string {
	if width < 8 {
		return ""
	}
	if m.suggestionsAreFiles {
		return ""
	}
	if !m.commandPaletteOpen || len(m.suggestions) != 1 {
		return ""
	}
	if m.suggestionIdx != 0 {
		return ""
	}
	value := strings.TrimSpace(m.input.Value())
	if !strings.HasPrefix(value, "/") || strings.ContainsAny(value, " \t\n") {
		return ""
	}
	suggestion := m.suggestions[0]
	desc := strings.TrimSpace(suggestion.Desc)
	if desc == "" {
		return ""
	}
	return fitStyledLine(sonutTheme.muted.Render(desc), width)
}

// startsTurn reports whether a row begins a new conversational turn and therefore
// gets a blank line of separation above it (tool rows stay grouped together).
func startsTurn(kind rowKind) bool {
	switch kind {
	case rowUser, rowAssistant, rowSystem, rowError:
		return true
	default:
		return false
	}
}

// isToolCardKind reports whether a row renders as a tool card (a running call or
// its collapsed result). Used to add one blank line between consecutive tool
// cards in a turn. Specialist cards are excluded — they own their own grouping
// (summary line + injected spacing) and must not be double-spaced.
func isToolCardKind(kind rowKind) bool {
	return kind == rowToolCall || kind == rowToolResult
}

func needsSeparatorBeforeToolCard(previous rowKind, current rowKind) bool {
	if !isToolCardKind(current) {
		return false
	}
	return isToolCardKind(previous) || previous == rowAssistant || previous == rowUser
}

func shouldRuleBeforeTurn(previous rowKind, current rowKind) bool {
	return current == rowAssistant && isToolCardKind(previous)
}

func (m model) handlePermissionKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	if m.pendingPermission == nil {
		return m, nil
	}
	key := strings.ToLower(msg.String())
	for _, option := range permissionOptions(m.pendingPermission.request) {
		if option.hotkey == key {
			return m.resolvePermission(option.choice)
		}
	}
	return m, nil
}

func (m model) resolvePermission(decision permissionDecision) (tea.Model, tea.Cmd) {
	pending := m.pendingPermission
	if pending == nil {
		return m, nil
	}

	if pending.decide != nil {
		pending.decide(agent.PermissionDecision{
			Action: decision,
			Reason: permissionDecisionReason(decision),
		})
	}
	m.safetyState.recordPermissionDecision(pending.request, decision)
	m.pendingPermission = nil
	return m, nil
}

func permissionDecisionReason(decision permissionDecision) string {
	switch decision {
	case permissionDecisionAllow:
		return "approved in TUI"
	case permissionDecisionAllowStrict:
		return "approved with model review request in TUI"
	case permissionDecisionAllowForSession:
		return "approved for this session in TUI"
	case permissionDecisionAllowPrefix:
		return "approved command prefix for this session in TUI"
	case permissionDecisionAlwaysAllowPrefix:
		return "persistently approved command prefix in TUI"
	case permissionDecisionAlwaysAllow:
		return "persistently approved in TUI"
	case permissionDecisionCancel:
		return "cancelled in TUI"
	case permissionDecisionDeny:
		return "denied in TUI"
	default:
		return "denied in TUI"
	}
}

// choosePicker applies the highlighted picker item through the same handler the
// typed command would have used, appends the resulting status text, and closes
// the picker. Behavior is identical to running "/model <id>" or "/effort <v>".
func (m model) choosePicker() (tea.Model, tea.Cmd) {
	if m.modelPickerIsLoading() {
		return m, nil
	}
	picker := m.picker
	if picker != nil && picker.kind == pickerModel {
		m.clearModelPickerLoadState()
	}
	m.picker = nil
	if picker == nil {
		return m, nil
	}
	item, ok := picker.current()
	if !ok {
		if picker.kind == pickerTheme {
			// No selectable row (e.g. the filter matched nothing): undo any live
			// preview so the palette matches the committed m.themeMode.
			m.restoreCommittedTheme()
		}
		return m, nil
	}
	var cmd tea.Cmd
	switch picker.kind {
	case pickerModel:
		text := ""
		if owner := strings.TrimSpace(item.OwnerProvider); owner != "" && !strings.EqualFold(owner, strings.TrimSpace(m.providerName)) {
			// A model from another saved provider: switch provider + model together.
			m, text, cmd = m.switchProviderModel(owner, item.Value)
		} else {
			m, text = m.handleModelCommand(item.Value)
		}
		m.transcript = reduceTranscript(m.transcript, transcriptAction{kind: actionAppendSystem, text: text})
	case pickerEffort:
		text := ""
		before := m.reasoningEffort
		m, text = m.handleEffortCommand(item.Value)
		m.transcript = reduceTranscript(m.transcript, transcriptAction{kind: actionAppendSystem, text: text})
		if m.reasoningEffort != before {
			if m.pending {
				m.deferredEffortWarmup = true
			} else {
				cmd = m.warmupEffortCmd()
			}
		}
	case pickerSession, pickerHistory:
		// item.Value is the chosen session id; handleResumeCommand hydrates it and
		// rebuilds the transcript (returning "" on success, an error note on failure).
		text := ""
		m, text = m.handleResumeCommand(item.Value)
		if text != "" {
			m.transcript = reduceTranscript(m.transcript, transcriptAction{kind: actionAppendSystem, text: text})
		}
	case pickerTheme:
		// The hovered palette is already live from the preview; handleThemeCommand
		// records the choice (m.themeMode) and re-applies it, and reports the switch.
		text := ""
		m, text = m.handleThemeCommand(item.Value)
		m.transcript = reduceTranscript(m.transcript, transcriptAction{kind: actionAppendSystem, text: text})
		if m.themeMode == themeAuto {
			// Re-probe the terminal background so a committed `auto` re-detects
			// light/dark instead of reusing the preview's reading — mirrors the
			// text /theme dispatch (M17).
			return m, tea.RequestBackgroundColor
		}
	}
	return m, cmd
}

func (m model) chooseSuggestion() (tea.Model, tea.Cmd) {
	if !m.suggestionsActive() || len(m.suggestions) == 0 {
		return m, nil
	}
	wasFiles := m.suggestionsAreFiles
	wasDirectory := m.selectedSuggestionIsDirectory()
	requiresInput := m.selectedCommandSuggestionRequiresInput()
	next := m.completeSuggestion()
	if !wasFiles {
		next.resetComposerFromInput()
	}
	if wasFiles && wasDirectory {
		next.recomputeSuggestions()
		return next, nil
	}
	if !wasFiles {
		if requiresInput {
			return next, nil
		}
		return next.handleSubmit()
	}
	return next, nil
}

func (m model) handleSubmit() (tea.Model, tea.Cmd) {
	input := m.composerValue()
	m.btwDone = false // submitting anything ends the /btw aside implicitly
	// A drag-dropped image/PDF path that reached the composer (e.g. inserted as
	// text) attaches instead of being parsed as an unknown "/…" command.
	if path, ok := droppedAttachmentPath(input, m.cwd); ok {
		m = m.handleImageCommand(path)
		m.clearComposer()
		m.clearSuggestions()
		return m, nil
	}
	command := parseCommand(input)
	// While exiting (Ctrl+C waiting on the cancelled run's checkpoint flush) a
	// new run must not start: the deferred tea.Quit would abort it mid-flight
	// and orphan its checkpoint blobs — the exact loss flushRunIDs prevents.
	if command.kind == commandPrompt && m.exiting {
		return m, nil
	}
	// Enter on an EMPTY composer while a prompt is queued = "send it now": the
	// queued prompt is steered into the live run at the next turn boundary
	// instead of waiting for the run to finish.
	if command.kind == commandEmpty && m.pending && m.hasQueuedMessage() {
		return m.steerQueuedMessage()
	}
	if command.kind == commandPrompt && m.pending {
		return m.queueMessage(command.text), nil
	}
	if command.kind == commandPrompt && m.compactInFlight {
		m.transcript = reduceTranscript(m.transcript, transcriptAction{
			kind: actionAppendSystem,
			text: "Compact\nstatus: warning\nCompaction is running. Your next prompt will use the compacted context when this finishes.",
		})
		return m, nil
	}
	m.rememberInput(input)
	m.clearComposer()
	m.clearSuggestions()
	// Snap the viewport back to the bottom for a real submission, but not for an
	// empty Enter (a no-op) — that would yank the user away from wherever they
	// had scrolled without anything actually being submitted.
	if command.kind != commandEmpty {
		m.chatScrollOffset = 0
	}

	return m.executeCommand(command)
}

// executeCommand dispatches a parsed command. Split from handleSubmit so
// remote surfaces (the /share phone mirror) run through the exact same command
// path as locally typed input.
func (m model) executeCommand(command parsedCommand) (tea.Model, tea.Cmd) {
	switch command.kind {
	case commandEmpty:
		return m, nil
	case commandHelp:
		m.transcript = reduceTranscript(m.transcript, transcriptAction{kind: actionAppendSystem, text: helpText()})
		return m, nil
	case commandClear:
		m.transcript = reduceTranscript(m.transcript, transcriptAction{kind: actionClear})
		// Scrollback above can't be un-printed; a faint divider marks where the
		// cleared surface ended and the frontier restarts for the fresh transcript.
		m.resetFlushFrontier("· cleared ·")
		// /clear is the explicit signal to discard the session plan.
		m.plan.clear()
		// /clear must also drop the MODEL's context, not just the visible
		// transcript — otherwise the next prompt still carries the whole prior
		// conversation via sessionPrompt's resume preamble and the model keeps
		// replying to cleared turns. The next prompt starts a fresh session.
		m.activeSession = sessions.Metadata{}
		m.sessionEvents = nil
		m.lastUserPrompt = ""
		return m, nil
	case commandExit:
		// /exit gets the same protection as Ctrl+C: cancel any in-flight run and
		// defer the quit until its checkpoint session events flush — quitting
		// immediately would orphan the blobs and break /rewind.
		m.cancelRun()
		m.exiting = true
		if len(m.flushRunIDs) > 0 {
			return m, nil
		}
		return m.quit()
	case commandTools:
		m.transcript = reduceTranscript(m.transcript, transcriptAction{kind: actionAppendSystem, text: m.toolsText()})
		return m, nil
	case commandSkills:
		m.transcript = reduceTranscript(m.transcript, transcriptAction{kind: actionAppendSystem, text: m.skillsText()})
		return m, nil
	case commandMCP:
		if strings.TrimSpace(command.text) == "" {
			return m.openMCPManager(), nil
		}
		return m.startMCPTranscriptCommand(command.text)
	case commandPermissions:
		m.transcript = reduceTranscript(m.transcript, transcriptAction{kind: actionAppendSystem, text: m.permissionsText()})
		return m, nil
	case commandMode:
		arg := strings.ToLower(strings.TrimSpace(command.text))
		switch arg {
		case "ask":
			m.permissionMode = agent.PermissionModeAsk
		case "auto":
			m.permissionMode = agent.PermissionModeAuto
		case "bypass":
			m.permissionMode = agent.PermissionModeBypass
		case "":
			m.permissionMode = nextPermissionMode(m.permissionMode)
		}
		label, _ := m.modeLabel()
		text := "Mode\nPermission mode: " + label + "\nTools will be " + func() string {
			if m.permissionMode == agent.PermissionModeAuto {
				return "auto-approved (no prompts)."
			}
			if m.permissionMode == agent.PermissionModeBypass {
				return "auto-approved with full privileges — no permission prompts."
			}
			return "prompted before running."
		}()
		m.transcript = reduceTranscript(m.transcript, transcriptAction{kind: actionAppendSystem, text: text})
		return m, nil
	case commandNetwork:
		arg := strings.ToLower(strings.TrimSpace(command.text))
		switch arg {
		case "on":
			m.networkEnabled = true
		case "off":
			m.networkEnabled = false
		case "":
			m.networkEnabled = !m.networkEnabled
		default:
			m.transcript = reduceTranscript(m.transcript, transcriptAction{kind: actionAppendSystem, text: "Usage: /network [on|off]"})
			return m, nil
		}
		state := "off"
		if m.networkEnabled {
			state = "on"
		}
		m.transcript = reduceTranscript(m.transcript, transcriptAction{kind: actionAppendSystem, text: "Network: " + state + " (DuckDuckGo web search " + state + ")"})
		return m, nil
	case commandBrowser:
		arg := strings.ToLower(strings.TrimSpace(command.text))
		text := m.handleBrowserCommand(arg)
		m.transcript = reduceTranscript(m.transcript, transcriptAction{kind: actionAppendSystem, text: text})
		return m, nil
	case commandPS:
		m.transcript = reduceTranscript(m.transcript, transcriptAction{kind: actionAppendSystem, text: m.backgroundTerminalsText()})
		return m, nil
	case commandStop:
		m.transcript = reduceTranscript(m.transcript, transcriptAction{kind: actionAppendSystem, text: m.stopBackgroundTerminalsText(command.text)})
		return m, nil
	case commandSandboxSetup:
		return m.startSandboxSetupCommand(command.text)
	case commandFeedback:
		return m.startFeedbackCommand(command.text)
	case commandHW:
		m.transcript = reduceTranscript(m.transcript, transcriptAction{kind: actionAppendSystem, text: hardwareRefreshText()})
		return m, nil
	case commandContext:
		m.transcript = reduceTranscript(m.transcript, transcriptAction{kind: actionAppendSystem, text: m.contextText()})
		return m, nil
	case commandSec:
		return m.startSecCommand(command.text)
	case commandKnowledge:
		return m.startKnowledgeCommand(command.text)
	case commandResearch:
		return m.startResearchCommand(command.text)
	case commandConfig:
		if arg := strings.ToLower(strings.TrimSpace(command.text)); arg != "" {
			var text string
			m, text = m.handleConfigCommand(arg)
			m.transcript = reduceTranscript(m.transcript, transcriptAction{kind: actionAppendSystem, text: text})
			return m, nil
		}
		m.transcript = reduceTranscript(m.transcript, transcriptAction{kind: actionAppendSystem, text: m.configText()})
		return m, nil
	case commandDebug:
		m.transcript = reduceTranscript(m.transcript, transcriptAction{kind: actionAppendSystem, text: m.debugText()})
		return m, nil
	case commandPlan:
		m.transcript = reduceTranscript(m.transcript, transcriptAction{kind: actionAppendSystem, text: m.planText()})
		return m, nil
	case commandDoctor:
		return m.startDoctorCommand(command.text)
	case commandSearch:
		m.transcript = reduceTranscript(m.transcript, transcriptAction{kind: actionAppendSystem, text: m.searchText(command.text)})
		return m, nil
	case commandResume:
		if m.pending {
			m.transcript = reduceTranscript(m.transcript, transcriptAction{
				kind: actionAppendError,
				text: "Cannot resume sessions while a run is active.",
			})
			return m, nil
		}
		// Bare `/resume` opens an interactive session picker (like /model & /provider);
		// `/resume <id>` and `/resume latest` still resolve directly. The picker falls
		// back to the text path when there is nothing to resume.
		if strings.TrimSpace(command.text) == "" {
			if next, ok := m.openSessionPicker(); ok {
				return next, nil
			}
		}
		text := ""
		m, text = m.handleResumeCommand(command.text)
		if strings.HasPrefix(text, sessionsCardsPrefix) {
			// The list payload renders as stacked session cards, not a note.
			m.transcript = appendTranscriptRow(m.transcript, transcriptRow{
				kind: rowSystem,
				tool: "sessions",
				text: strings.TrimPrefix(text, sessionsCardsPrefix),
			})
		} else if text != "" {
			m.transcript = reduceTranscript(m.transcript, transcriptAction{kind: actionAppendSystem, text: text})
		}
		return m, nil
	case commandHistory:
		if m.pending {
			m.transcript = reduceTranscript(m.transcript, transcriptAction{
				kind: actionAppendError,
				text: "Cannot browse history while a run is active.",
			})
			return m, nil
		}
		if next, ok := m.openHistoryPicker(); ok {
			return next, nil
		}
		m.transcript = reduceTranscript(m.transcript, transcriptAction{kind: actionAppendSystem, text: "No session history found."})
		return m, nil
	case commandRetitle:
		if m.pending {
			m.transcript = reduceTranscript(m.transcript, transcriptAction{
				kind: actionAppendError,
				text: "Cannot retitle sessions while a run is active.",
			})
			return m, nil
		}
		text := ""
		var retitleCmd tea.Cmd
		m, retitleCmd, text = m.startSessionRetitle()
		if text != "" {
			m.transcript = reduceTranscript(m.transcript, transcriptAction{kind: actionAppendSystem, text: text})
		}
		return m, retitleCmd
	case commandSpec:
		return m.handleSpecCommand(command.text)
	case commandInit:
		return m.handleInitCommand()
	case commandCompact:
		text := ""
		var compactCmd tea.Cmd
		m, text, compactCmd = m.handleCompactCommand(command.text)
		m = m.setCompactStatusRow(text)
		return m, compactCmd
	case commandTranscript:
		return m.toggleDetailedTranscript(), nil
	case commandRewind:
		text := ""
		m, text = m.handleRewindCommand(command.text)
		m.transcript = reduceTranscript(m.transcript, transcriptAction{kind: actionAppendSystem, text: text})
		return m, nil
	case commandEffort:
		// Manual effort switching is removed — no picker; handleEffortCommand
		// re-arms auto and explains the automatic policy.
		text := ""
		before := m.reasoningEffort
		m, text = m.handleEffortCommand(command.text)
		m.transcript = reduceTranscript(m.transcript, transcriptAction{kind: actionAppendSystem, text: text})
		// Re-prime the KV cache for the new effort in the background so the next
		// message doesn't stall on a thinking-mode re-prefill.
		// If a run is active, defer warmup until it completes to avoid
		// competing with the in-flight llama.cpp request (single-threaded).
		if m.reasoningEffort != before {
			if m.pending {
				m.deferredEffortWarmup = true
				return m, nil
			}
			return m, m.warmupEffortCmd()
		}
		return m, nil
	case commandHandoff:
		text := ""
		m, text = m.handleHandoffCommand(command.text)
		m.transcript = reduceTranscript(m.transcript, transcriptAction{kind: actionAppendSystem, text: text})
		return m, nil
	case commandStyle:
		text := ""
		m, text = m.handleStyleCommand(command.text)
		m.transcript = reduceTranscript(m.transcript, transcriptAction{kind: actionAppendSystem, text: text})
		return m, nil
	case commandSelfCorrect:
		text := ""
		m, text = m.handleSelfCorrectCommand(command.text)
		m.transcript = reduceTranscript(m.transcript, transcriptAction{kind: actionAppendSystem, text: text})
		return m, nil
	case commandTurns:
		// Changing the budget mid-run would mutate the inherited SONUT_MAX_TURNS env
		// that sub-agents spawned later in THIS run read, making the run's budget
		// inconsistent. Require an idle session (the new budget applies next run).
		if m.pending && strings.TrimSpace(command.text) != "" {
			m.transcript = reduceTranscript(m.transcript, transcriptAction{kind: actionAppendSystem, text: "Turns\nFinish or stop the current run before changing the tool-turn budget."})
			return m, nil
		}
		text := ""
		m, text = m.handleTurnsCommand(command.text)
		m.transcript = reduceTranscript(m.transcript, transcriptAction{kind: actionAppendSystem, text: text})
		return m, nil
	case commandTheme:
		// Bare `/theme` opens the popup picker (live preview on move, apply on
		// Enter), matching /model and /effort. An explicit `/theme auto|dark|light`
		// (or `/theme list`) still runs the text handler directly.
		if strings.TrimSpace(command.text) == "" {
			m.picker = m.newThemePicker()
			return m, nil
		}
		text := ""
		m, text = m.handleThemeCommand(command.text)
		m.transcript = reduceTranscript(m.transcript, transcriptAction{kind: actionAppendSystem, text: text})
		if m.themeMode == themeAuto {
			// Re-probe the terminal background so /theme auto re-detects light/dark
			// instead of reusing a stale reading from startup; the BackgroundColorMsg
			// handler re-applies the auto palette with the fresh result (M17).
			return m, tea.RequestBackgroundColor
		}
		return m, nil
	case commandImage:
		m = m.handleImageCommand(command.text)
		return m, nil
	case commandAddDir:
		m = m.handleAddDirCommand(command.text)
		return m, nil
	case commandCD:
		return m.handleCDCommand(command.text)
	case commandShare:
		return m.handleWebCommand(command.text), nil
	case commandBuild:
		arg := strings.TrimSpace(strings.ToLower(command.text))
		switch arg {
		case "on", "1", "true":
			m.buildMode = true
		case "off", "0", "false":
			m.buildMode = false
		default:
			// bare /build toggles
			m.buildMode = !m.buildMode
		}
		status := "off"
		detail := "@code is back to normal coding-agent behavior."
		if m.buildMode {
			status = "on"
			detail = "Describe the app you want — @code will scaffold, implement, and start a live preview (dev server)."
		}
		text := "Build mode\nbuild mode: " + status + "\n" + detail
		if m.buildMode {
			text += "\n\n" + integrationsSection(m.cwd)
		}
		m.transcript = reduceTranscript(m.transcript, transcriptAction{
			kind: actionAppendSystem,
			text: text,
		})
		return m, nil
	case commandVoice:
		var text string
		m, text, cmd := m.voiceHandleCommand(command.text)
		if text != "" {
			m.transcript = reduceTranscript(m.transcript, transcriptAction{kind: actionAppendSystem, text: text})
		}
		return m, cmd
	case commandPong:
		if m.noBlockingModal() && !m.suggestionsActive() && !m.subchat.active && (m.pending || m.pong.started) {
			m.pong.start(chatWidth(m.width))
			return m, m.pong.tickCmd()
		}
		m.transcript = reduceTranscript(m.transcript, transcriptAction{kind: actionAppendSystem, text: "Pong is available while a run is active or after it has been started."})
		return m, nil
	case commandIntegrate:
		return m.handleIntegrateCommand(command.text), nil
	case commandNonstop:
		arg := strings.TrimSpace(strings.ToLower(command.text))
		switch arg {
		case "on", "1", "true":
			m.nonstop = true
		case "off", "0", "false":
			m.nonstop = false
			m.nonstopNudgeCount = 0
		default:
			// bare /nonstop toggles
			m.nonstop = !m.nonstop
			m.nonstopNudgeCount = 0
		}
		status := "off"
		if m.nonstop {
			status = "on"
		}
		m.transcript = reduceTranscript(m.transcript, transcriptAction{
			kind: actionAppendSystem,
			text: "Nonstop\nnonstop mode: " + status + "\nWhen on, @code auto-continues after each run while plan tasks remain open.",
		})
		return m, nil
	case commandUnknown:
		// A "/name" not in the builtin registry may be a user-defined command
		// from .atcode/commands/<name>.md — expand its template and run it as a
		// normal prompt before reporting "unknown".
		if next, cmd, handled := m.handleUserCommand(command.text); handled {
			return next, cmd
		}
		m.transcript = reduceTranscript(m.transcript, transcriptAction{
			kind: actionAppendError,
			text: "unknown command: " + command.text,
		})
		return m, nil
	case commandCommit:
		msg := strings.TrimSpace(command.text)
		if msg == "" {
			return m.launchPrompt("Run `git diff --cached` and `git status`, then write a concise conventional commit message and run `git commit -m \"<message>\"`.")
		}
		return m.launchPrompt(fmt.Sprintf("Run: git add -A && git commit -m %q", msg))
	case commandReview:
		target := strings.TrimSpace(command.text)
		if target == "" {
			target = "the current git diff (HEAD)"
		}
		return m.launchPrompt(fmt.Sprintf("Review %s for bugs, security issues, and code quality. Show a concise list of findings grouped by severity.", target))
	case commandMemory:
		args := strings.Fields(command.text)
		// Context-size subcommands: /memory 32k | 64k | 128k | 256k
		if len(args) > 0 {
			newCtx := parseMemoryContextSize(args[0])
			if newCtx > 0 {
				if err := llama.SaveContextOverride(newCtx); err != nil {
					m.transcript = reduceTranscript(m.transcript, transcriptAction{
						kind: actionAppendSystem,
						text: fmt.Sprintf("Failed to save context setting: %v", err),
					})
					return m, nil
				}
				label := memoryCtxLabel(newCtx)
				port := m.llamaPort
				m.transcript = reduceTranscript(m.transcript, transcriptAction{
					kind: actionAppendSystem,
					text: fmt.Sprintf("Restarting the atcode engine with %s context window…", label),
				})
				return m, func() tea.Msg {
					// Kill the actual llama-server on its internal port — NOT
					// the public port, which is the in-process reverse proxy
					// (killing that would signal sonut itself). startLlamaCmd
					// re-launches the server; its proxy start is idempotent.
					llama.KillLlamaOnPort(llama.InternalLlamaPort)
					// Reuse startup logic — reads new context from the saved override.
					inner := startLlamaCmd(port)
					result := inner()
					switch r := result.(type) {
					case llamaReadyMsg:
						return llamaRestartDoneMsg{ctxSize: newCtx}
					case startupErrMsg:
						return llamaRestartDoneMsg{ctxSize: newCtx, err: r.err}
					default:
						return llamaRestartDoneMsg{ctxSize: newCtx}
					}
				}
			}
		}
		// Memory-file subcommands (original behavior)
		if len(args) == 0 || args[0] == "list" {
			return m.launchPrompt("List all SONUT.md, CLAUDE.md, and .atcode/memory/*.md files in the project and summarize what each one contains.")
		}
		if len(args) >= 2 && args[0] == "show" {
			return m.launchPrompt(fmt.Sprintf("Show the full contents of %s", args[1]))
		}
		// Bare /memory with no valid subcommand → show context + memory files info
		curCtx := llama.LoadContextOverride()
		if curCtx == 0 {
			if hw := llama.LoadHardwareMetrics(); hw != nil && hw.RecommendedContextSize > 0 {
				curCtx = hw.RecommendedContextSize
			} else {
				curCtx = 262144 // default 256k
			}
		}
		m.transcript = reduceTranscript(m.transcript, transcriptAction{
			kind: actionAppendSystem,
			text: fmt.Sprintf("Current context window: %s  •  /memory 32k | 64k | 128k | 256k to change and hot-restart.\nUse /memory list to view project memory files.", memoryCtxLabel(curCtx)),
		})
		return m, nil
	case commandExport:
		path := strings.TrimSpace(command.text)
		if path == "" {
			path = "atcode-session.md"
		}
		return m.launchPrompt(fmt.Sprintf("Write the full conversation so far as Markdown to the file %q.", path))
	case commandVim:
		m = m.handleVimCommand(command.text)
		return m, nil
	case commandTasks:
		return m.launchPrompt("List all current tasks and their status from the session context.")
	case commandHide:
		var text string
		m, text = m.handleHideCommand(command.text)
		m.transcript = reduceTranscript(m.transcript, transcriptAction{kind: actionAppendSystem, text: text})
		return m, nil
	case commandEnterprise:
		m.transcript = reduceTranscript(m.transcript, transcriptAction{kind: actionAppendSystem, text: m.enterpriseText()})
		return m, nil
	case commandDream:
		focus := strings.TrimSpace(command.text)
		prompt := "Run one focused dream pass for this project. Read .atcode/memory/MEMORY.md, the current session checkpoint and notes, and recent session history. Consolidate only explicit, verified rules, architecture decisions, and durable facts into .atcode/memory/projects/" + evolution.ProjectID(m.cwd) + "/MEMORY.md. Remove stale or contradicted entries, keep the file compact, and do not invent facts."
		if focus != "" {
			prompt += "\nUser focus: " + focus
		}
		return m.launchPrompt(prompt)
	case commandDistill:
		focus := strings.TrimSpace(command.text)
		prompt := "Run one bounded distill pass for this project. Inspect recent session notes/checkpoints and existing skills/commands. Identify repeated workflows with evidence from at least two runs, then create or extend only the smallest high-confidence reusable asset. Do not create speculative automation; verify any new asset with focused tests or a dry run."
		if focus != "" {
			prompt += "\nUser focus: " + focus
		}
		return m.launchPrompt(prompt)
	case commandEvolve:
		focus := strings.TrimSpace(command.text)
		prompt := "Run a safe evolution pass for @code. Review .atcode/memory and recent session outcomes, find one concrete recurring weakness, and improve the smallest relevant project-local behavior, skill, command, hook, or test. Preserve existing behavior, avoid broad rewrites and secrets, and run focused verification before declaring success."
		if focus != "" {
			prompt += "\nUser focus: " + focus
		}
		return m.launchPrompt(prompt)
	case commandGraph:
		text := m.handleGraphCommand(command.text)
		m.transcript = reduceTranscript(m.transcript, transcriptAction{kind: actionAppendSystem, text: text})
		return m, nil
	case commandTrash:
		text := m.trashListText()
		m.transcript = reduceTranscript(m.transcript, transcriptAction{kind: actionAppendSystem, text: text})
		return m, nil
	case commandGit:
		sub := strings.TrimSpace(command.text)
		switch sub {
		case "log":
			return m.launchPrompt("Run `git log --oneline -20` and show the result.")
		case "diff":
			return m.launchPrompt("Run `git diff HEAD` and show the result.")
		default:
			return m.launchPrompt("Run `git status` and show the result.")
		}
	case commandBtw:
		q := strings.TrimSpace(command.text)
		if q == "" {
			m.transcript = reduceTranscript(m.transcript, transcriptAction{
				kind: actionAppendSystem,
				text: "Usage: /btw <question> — ask a quick side question.",
			})
			return m, nil
		}
		m.btwPending = true
		return m.launchBtw(q)
	case commandSignOut:
		home, _ := os.UserHomeDir()
		authFile := filepath.Join(home, ".atcode", "auth.json")
		if err := os.Remove(authFile); err != nil && !os.IsNotExist(err) {
			m.transcript = reduceTranscript(m.transcript, transcriptAction{
				kind: actionAppendError,
				text: "sign-out failed: " + err.Error(),
			})
			return m, nil
		}
		return m, tea.Quit
	case commandBash:
		cmdText := strings.TrimSpace(command.text)
		if cmdText == "" {
			m.transcript = reduceTranscript(m.transcript, transcriptAction{kind: actionAppendSystem, text: "Usage: !<shell command>"})
			return m, nil
		}
		// A "!cmd" shell escape runs OUTSIDE the agent sandbox, so gate it behind
		// the explicit unsafe permission mode. In auto/ask mode it is not executed;
		// the user is told how to enable it. This keeps a sandbox-bypassing exec
		// from running without a deliberate safety posture.
		if m.permissionMode != agent.PermissionModeUnsafe {
			m.transcript = reduceTranscript(m.transcript, transcriptAction{
				kind: actionAppendSystem,
				text: "Shell escape (!) is disabled in " + string(m.permissionMode) + " mode — it bypasses the sandbox. Relaunch with --skip-permissions-unsafe to run shell commands directly.",
			})
			return m, nil
		}
		m.transcript = reduceTranscript(m.transcript, transcriptAction{kind: actionAppendSystem, text: "$ " + cmdText})
		return m, runBashEscape(m.cwd, cmdText)
	case commandPrompt:
		if intent, ok := detectMCPSetupIntent(command.text); ok {
			return m.openMCPAddWizardFromIntent(intent), nil
		}
		return m.launchPrompt(command.text)
	default:
		return m, nil
	}
}

// launchPrompt starts a normal agent turn from text already accepted by the
// composer. Queued prompts use this path too, so session and image behavior
// stays identical to immediate submissions.
func (m model) launchPrompt(prompt string) (model, tea.Cmd) {
	return m.launchPromptWithOptions(prompt, tuiAgentRunOptions{})
}

// launchPromptWithOptions starts a normal or internal continuation turn. An
// internal continuation keeps the visible nudge in the transcript, but marks
// it as executor-only so the long nudge cannot be misclassified as a fresh
// builder request and trigger another grounding + planner pass.
func (m model) launchPromptWithOptions(prompt string, runOptions tuiAgentRunOptions) (model, tea.Cmd) {
	m.lastUserPrompt = prompt
	attachmentRefs := m.attachmentReferenceText()
	visiblePrompt := prompt
	if attachmentRefs != "" {
		visiblePrompt += "  " + attachmentRefs
	}
	m.transcript = reduceTranscript(m.transcript, transcriptAction{kind: actionAppendUser, text: visiblePrompt})
	if m.provider == nil {
		m.transcript = reduceTranscript(m.transcript, transcriptAction{
			kind: actionAppendAssistant,
			text: "The atcode engine is not connected. Restart @code to relaunch it (or set ATCODE_LLAMA_PORT if you run the engine on a custom port), then relaunch.",
		})
		if m.voice != nil && runOptions.voiceOrigin {
			m.voice.state = voicepkg.StateError
			m.voice.err = "atcode engine is not connected"
		}
		return m, nil
	}
	// A leading "@specialist <task>" is expanded into an explicit Task-delegation
	// directive for the agent only; the transcript above keeps the user's verbatim
	// "@mention". Non-mentions and mid-message "@file" references are unchanged.
	if expanded, ok := expandSpecialistMention(prompt, m.agentOptions.Specialists); ok {
		prompt = expanded
	} else if resolved, ok := resolveFileMentions(prompt, m.cwd); ok {
		// The transcript above already recorded the user's verbatim "@mention";
		// only the agent-facing copy drops the "@" so tools never see it as part
		// of a path (it was being passed through literally and failing lstat).
		prompt = resolved
	}
	// Prepend any staged PDF document text as a model-facing preamble. The
	// visible transcript above keeps the user's clean prompt; the agent (and the
	// recorded session, for resume fidelity) sees the document text first.
	if preamble := m.consumePendingDocuments(); preamble != "" {
		prompt = preamble + prompt
	}
	if attachmentRefs != "" {
		prompt += "\n\n[attachment_manifest]\n" + attachmentRefs + "\nImages and documents are ordered exactly as listed. Answer references to [Image #N] or [Document #N] using that attachment.\n[/attachment_manifest]"
	}
	var err error
	m, err = m.ensureActiveSession(prompt)
	if err != nil {
		m.transcript = reduceTranscript(m.transcript, transcriptAction{
			kind: actionAppendError,
			text: "session create error: " + err.Error(),
		})
	} else {
		// Capture the context before recording this turn. agent.Run appends the
		// current prompt after these prior turns, mirroring OpenCode's ordered
		// message history and preventing the current question from being mistaken
		// for the whole conversation on fast Q&A turns.
		runOptions.conversationHistory = sessionConversationHistory(m.sessionEvents)
		m, err = m.appendSessionEvent(sessions.EventMessage, map[string]any{
			"role":    "user",
			"content": prompt,
		})
		if err != nil {
			m.transcript = reduceTranscript(m.transcript, transcriptAction{
				kind: actionAppendError,
				text: "session record error: " + err.Error(),
			})
		}
	}
	// Re-check vision support against the CURRENT effective model at submit
	// time, not just at /image attach time: the user may have attached on a
	// vision model and then /model-switched to a non-vision one. If the active
	// model can't accept images, drop them (with an inline notice mirroring
	// exec's drop+warn wording) rather than sending them to a model that
	// rejects them. Pending state is cleared either way below.
	turnImages := m.pendingImages
	if len(turnImages) > 0 && !m.modelSupportsVisionTUI() {
		name := m.modelName
		if name == "" {
			name = "the active model"
		}
		m.transcript = reduceTranscript(m.transcript, transcriptAction{
			kind: actionAppendSystem,
			text: fmt.Sprintf("Model %s does not support image input; ignoring %d image(s).", name, len(turnImages)),
		})
		turnImages = nil
	}
	m.pendingImages = nil
	m.pendingImageLabels = nil
	m.pendingImagePaths = nil
	m.attachmentView = attachmentViewState{}
	runCtx, cancel := context.WithCancel(m.ctx)
	m = m.beginRun(cancel)
	if runOptions.voiceOrigin && strings.TrimSpace(runOptions.voiceUtteranceID) != "" {
		if m.voiceRuns == nil {
			m.voiceRuns = map[int]string{}
		}
		if m.voiceBuilderRuns == nil {
			m.voiceBuilderRuns = map[int]bool{}
		}
		m.voiceRuns[m.activeRunID] = runOptions.voiceUtteranceID
		if runOptions.runMode == agent.RunModeBuilder {
			m.voiceBuilderRuns[m.activeRunID] = true
		}
		if m.voice != nil && runOptions.runMode != agent.RunModeBuilder {
			m.voice.state = voicepkg.StateThinking
		}
	}
	if len(turnImages) > 0 {
		// Image encoding/vision preprocessing happens before llama-server emits
		// prompt-progress events. Name that work immediately so an attached
		// screenshot never presents as a blank 0-token wait.
		m.prefillStatus = "analyzing attached image…"
	}
	// The planner gate must see the user's RAW ask, not the session-context-
	// wrapped prompt, or "do it"-sized follow-ups trigger a planning pass.
	if strings.TrimSpace(runOptions.plannerPrompt) == "" {
		runOptions.plannerPrompt = strings.TrimSpace(m.lastUserPrompt + "\n" + attachmentRefs)
	}
	if runOptions.runMode == agent.RunModeAuto {
		runOptions.runMode = agent.RouteRunMode(m.lastUserPrompt)
	}
	return m, tea.Batch(m.runAgentWithOptions(m.activeRunID, runCtx, prompt, turnImages, runOptions), m.spinner.Tick)
}

// launchBtw runs a quick /btw side-question: only the user's question is shown
// in the transcript (no hidden prefix noise), tools are disabled for speed, and
// a concise-answer instruction is prepended invisibly to the model prompt only.
func (m model) launchBtw(question string) (model, tea.Cmd) {
	// Show only the clean question in the transcript — no "(quick aside…)" prefix.
	m.transcript = reduceTranscript(m.transcript, transcriptAction{kind: actionAppendUser, text: "/btw: " + question})
	if m.provider == nil {
		m.transcript = reduceTranscript(m.transcript, transcriptAction{
			kind: actionAppendAssistant,
			text: "No model server connected.",
		})
		return m, nil
	}
	// The model-facing prompt carries the aside instruction; the UI never sees it.
	// Reasoning is disabled ("none") so a trivial question doesn't burn 60s thinking.
	modelPrompt := "Answer this question concisely in 1-3 sentences, then stop. Question: " + question
	runCtx, cancel := context.WithCancel(m.ctx)
	m = m.beginRun(cancel)
	m.btwRunID = m.activeRunID // aside run: its events are persisted but kept out of model context
	// Record the question ephemerally too, so a /resume replays the full aside pair.
	m, _ = m.appendSessionEventsEphemeral([]pendingSessionEvent{{
		Type:    sessions.EventMessage,
		Payload: map[string]any{"role": "user", "content": "/btw: " + question},
	}})
	return m, tea.Batch(
		m.runAgentWithOptions(m.activeRunID, runCtx, modelPrompt, nil, tuiAgentRunOptions{
			noTools:         true,
			runMode:         agent.RunModeQuick,
			reasoningEffort: modelregistry.ReasoningEffortNone,
		}),
		m.spinner.Tick,
	)
}

// beginRun stamps the shared run-start state for a new agent turn: a fresh run
// ID, the cancel func, pending = true, the turn-start timestamp (the source for
// the working status line's live elapsed clock), and a reset working-verb
// rotation so the brand word shows first. Centralized so every launch path
// (normal prompt + spec draft/impl) keeps these in sync — a missing
// turnStartedAt previously dropped the elapsed timer on spec-mode runs.
func (m model) beginRun(cancel context.CancelFunc) model {
	// Free the single llama slot: any in-flight KV warmup would otherwise queue
	// this turn behind a 1-2 minute prefill (seen live: 95s first greeting with
	// a "connection lost — reconnecting" row). The partial prefix stays cached.
	// setUserRunActive also blocks NEW warmups from starting until the run ends.
	setUserRunActive(true)
	m.runID++
	m.activeRunID = m.runID
	m.runCancel = cancel
	m.pending = true
	// Fresh live-steering inbox for this run; the run's agent.Options captures
	// the pointer, so later model copies push into the same inbox.
	m.steerInbox = &agent.SteerInbox{}
	// Clear per-run tracking state so stale specialists from the previous turn
	// don't bleed into the new one. Plans persist intentionally across turns —
	// the model continues working a multi-step plan between user messages.
	// Only the freeze timer is reset so the plan clock resumes.
	m.specialists.clear()
	m.plan.frozenAt = time.Time{}
	m.stepWork = nil
	m.stepNarration = nil
	m.stepExplanation = nil
	m.planDetailOpen = false
	m.planDetailGen++ // invalidate any in-flight step-explanation from the prior run
	// A new run clears the sidebar's content (plan/agents), so the user's Ctrl+S
	// hide was for the OLD context — reset it so the new run's sidebar isn't
	// suppressed by a stale preference.
	m.sidebarHidden = false
	m.sidebarOpen = false
	m.turnStartedAt = m.now()
	m.sessionStreamedRunes += m.turnStreamedRunes // persist completed-turn estimate before reset
	m.turnStreamedRunes = 0
	// Show an explicit phase immediately. llama-server may not emit its first
	// prompt-progress heartbeat for several seconds, so an empty footer made a
	// healthy prefill look like a hung agent between actions.
	m.prefillStatus = "preparing context…"
	m.prefillProcessed = 0
	m.prefillTotal = 0
	m.prefillComplete = false
	m.spinnerTicking = true
	return m
}

// ensureSpinnerTick returns the spinner.Tick cmd to (re)start the self-scheduling
// tick loop when an active sidebar holds agents to animate but the loop is not
// already running (e.g. a resumed session whose swarm members exist before any
// run started this process). It returns nil — issuing no second timer — when the
// loop is already alive, when reduced motion is set, or when there is nothing to
// animate, so an idle plain session schedules no timer.
func (m *model) ensureSpinnerTick() tea.Cmd {
	if m.spinnerTicking || m.reducedMotion || !m.sidebarHasAgents() {
		return nil
	}
	m.spinnerTicking = true
	return m.spinner.Tick
}

func (m model) launchQueuedMessageIfReady() (model, tea.Cmd) {
	if !m.hasQueuedMessage() || m.pending || m.exiting || m.pendingPermission != nil || m.pendingAskUser != nil || m.pendingSpecReview != nil {
		return m, nil
	}
	prompt := m.queuedMessage
	m.queuedMessage = ""
	// A queued message is a continuation of the active conversation. Re-running
	// the planner here duplicates the expensive planning request and can make a
	// follow-up appear stuck before it answers. The existing roadmap/context is
	// already in the session, so continue directly with the executor.
	return m.launchPromptWithOptions(prompt, tuiAgentRunOptions{
		plannerPrompt: "continue the existing plan",
		runMode:       agent.RunModeBuilder,
		executorOnly:  true,
	})
}

// historyRecallActive reports whether ↑/↓ should navigate previously submitted
// inputs: history exists and no modal surface owns the arrow keys.
func (m model) historyRecallActive() bool {
	return len(m.inputHistory) > 0 &&
		m.pendingAskUser == nil && m.pendingPermission == nil && m.pendingSpecReview == nil
}

// recallHistory steps through submitted inputs (-1 = older, +1 = newer),
// stashing the in-progress draft so stepping back past the newest recalled
// entry restores whatever was being typed.
func (m model) recallHistory(direction int) model {
	if m.historyIdx == len(m.inputHistory) {
		if direction > 0 {
			return m
		}
		m.historyDraft = m.composerValue()
	}
	next := clamp(m.historyIdx+direction, 0, len(m.inputHistory))
	if next == m.historyIdx {
		return m
	}
	m.historyIdx = next
	if next == len(m.inputHistory) {
		m.input.SetValue(m.historyDraft)
	} else {
		m.input.SetValue(m.inputHistory[next])
	}
	m.input.CursorEnd()
	m.resetComposerFromInput()
	m.recomputeSuggestions()
	return m
}

// rememberInput records a submitted composer value for ↑ recall and resets the
// navigation cursor past the newest entry.
func (m *model) rememberInput(value string) {
	trimmed := strings.TrimSpace(value)
	if trimmed != "" && (len(m.inputHistory) == 0 || m.inputHistory[len(m.inputHistory)-1] != trimmed) {
		m.inputHistory = append(m.inputHistory, trimmed)
	}
	m.historyIdx = len(m.inputHistory)
	m.historyDraft = ""
}

func (m *model) cancelRun() {
	if m.voice != nil && m.voice.utteranceID != "" {
		_ = m.voice.manager.CancelTTS(m.voice.utteranceID)
		if m.voiceIsTemporaryActive() || m.voice.state == voicepkg.StateBuilding {
			*m = m.voiceCancel(false)
		}
	}
	if m.runCancel != nil {
		m.runCancel()
	}
	m.clearStreamingToolCall() // a cancelled file-write must not linger into the next run
	// Remember the in-flight run — and the session it was recording into — so
	// its final agentResponseMsg is still drained for session-event persistence
	// after activeRunID is cleared. Otherwise the checkpoint blobs it captured
	// before each mutating tool are orphaned on disk and /rewind can't reference
	// them; without the session id, a /resume before the flush lands would
	// append the old run's events into the newly active session.
	if m.pending && m.activeRunID != 0 {
		if m.flushRunIDs == nil {
			m.flushRunIDs = make(map[int]string)
		}
		m.flushRunIDs[m.activeRunID] = m.activeSession.SessionID
	}
	if m.pending {
		// A cancelled run must terminate visibly in the transcript: first the
		// partial streamed answer (if any), then the cancellation marker — the
		// session log gets the same marker below.
		if text := strings.TrimRight(m.streamingText, "\n"); strings.TrimSpace(text) != "" {
			m.transcript = appendTranscriptRow(m.transcript, transcriptRow{kind: rowAssistant, text: text})
		}
		m.transcript = appendTranscriptRow(m.transcript, transcriptRow{kind: rowSystem, text: "Run cancelled."})
	}
	if m.pending && m.activeSession.SessionID != "" {
		if next, err := (*m).appendSessionEvent(sessions.EventError, map[string]any{
			"message": "Run cancelled.",
		}); err == nil {
			*m = next
		}
	}
	m.pending = false
	m.runCancel = nil
	m.activeRunID = 0
	setUserRunActive(false)       // allow background KV warmups again
	m.steerInbox = nil            // a cancelled run can't consume steered input anymore
	m.cancelConfirmActive = false // whatever path got here, there's nothing left to confirm cancelling
	m.plan.frozenAt = m.now()     // freeze the plan clock while idle (no run in flight)
	m.pendingPermission = nil
	m.pendingAskUser = nil
	// The interim block renders streamingText live; a cancelled run's partial
	// answer must not leak into (and concatenate with) the next turn's stream.
	m.streamingText = ""
	m.streamingReasoning = ""
	m.streamingReasoningExpanded = false
	// Hard-stop the fade and drop the per-line age map. The next turn's
	// first agentTextMsg will seed a fresh lineAges slice and restart
	// the tick.
	m.resetStreamingFade()
}

// selfCorrectAutonomyForMode maps the active permission mode to the self-correct
// autonomy gate: more autonomous modes auto-fix after a failed verification,
// while restrictive modes only surface the failure. Mirrors exec's --auto levels.
func selfCorrectAutonomyForMode(mode agent.PermissionMode) string {
	switch mode {
	case agent.PermissionModeUnsafe:
		return "high"
	case agent.PermissionModeAuto:
		return "medium"
	default: // ask, etc. — report the failure without starting an auto-fix round
		return "low"
	}
}

func (m model) runAgentWithOptions(runID int, runCtx context.Context, prompt string, images []sonutruntime.ImageBlock, runOptions tuiAgentRunOptions) tea.Cmd {
	return func() tea.Msg {
		// Preempt any leftover KV warmup before we touch the single llama slot.
		// beginRun already canceled; this catches a warmup that raced in and
		// waits briefly so the server has time to free the slot (otherwise the
		// real turn queues silently behind a 60–95 s prefill — 0 tok for minutes).
		cancelActiveWarmups()
		// The warmup client closes its connection on cancel. A short grace period
		// is enough for llama.cpp to release the single slot; waiting 1.5s made
		// every user turn visibly linger in "analyzing input" when a background
		// effort warmup raced with submit.
		waitForWarmupsToDrain(500 * time.Millisecond)

		started := m.now()
		toolCalls := 0
		rows := []transcriptRow{}
		usageEvents := []sonutruntime.Usage{}
		sessionEvents := []pendingSessionEvent{}
		usageModelID := m.modelName
		var specReview *pendingSpecReviewPrompt
		options := m.agentOptions
		options.Registry = m.registry
		if runOptions.registry != nil {
			options.Registry = runOptions.registry
		}
		if runOptions.noTools {
			options.Registry = tools.NewRegistry() // empty — no tools offered to model
		}
		options.NetworkDisabled = !m.networkEnabled
		options.PermissionMode = m.permissionMode
		if runOptions.permissionMode != "" {
			options.PermissionMode = runOptions.permissionMode
		}
		if runOptions.systemPrompt != "" {
			options.SystemPrompt = runOptions.systemPrompt
		}
		if runOptions.voiceOrigin && runOptions.speakResponse {
			language := m.voiceLanguage()
			options.SystemPrompt += fmt.Sprintf("\n\nVoice response directive: answer in the user's detected or configured language (%s). Keep the spoken answer concise, natural, and self-contained. Avoid markdown-heavy formatting. Do not read code, tables, raw URLs, hashes, or long paths aloud.", language)
		}
		options.SessionID = m.activeSession.SessionID
		// Inference is ALWAYS local llama.cpp for @code. Preferences may still
		// show a cloud provider name in the UI, but overwriting ProviderName with
		// that name disabled isLocalHybridRun — images were re-encoded every turn
		// (58KB → multi-minute prefills) and tool dumps never clamped (40KB reads
		// → 18k-token re-prefills). Keep the agent path on "llama" whenever the
		// local engine port is live.
		if m.llamaPort > 0 {
			options.ProviderName = "llama"
			// Force a hybrid-identifying model id so isLocalHybridRun stays true
			// even if Preferences still list a cloud model name.
			options.Model = "atcode-v1"
		} else {
			options.ProviderName = m.providerName
			options.Model = m.modelName
		}
		options.ReasoningEffort = string(m.reasoningEffort)
		if runOptions.reasoningEffort != "" {
			options.ReasoningEffort = string(runOptions.reasoningEffort)
		}
		options.Steer = m.steerInbox
		options.ResponseStyle = m.responseStyle
		options.RunMode = runOptions.runMode
		options.VoiceOrigin = runOptions.voiceOrigin
		options.SpeakResponse = runOptions.speakResponse
		options.VoiceUtteranceID = runOptions.voiceUtteranceID
		options.ExecutorOnly = runOptions.executorOnly
		options.BuildMode = m.buildMode
		if m.buildMode {
			options.BuildIntegrations = configuredIntegrationNames(m.cwd)
		}
		options.Cwd = m.cwd
		options.Images = images
		options.ConversationHistory = append([]sonutruntime.Message{}, runOptions.conversationHistory...)
		if m.captureRunImages != nil {
			m.captureRunImages(images)
		}
		// Enable agent-loop compaction sized to the active model's context window.
		// AgentContextWindow applies a positive fallback for unknown/custom models so
		// compaction (proactive + reactive) is enabled for every model, not just
		// catalogued ones.
		options.ContextWindow = modelregistry.AgentContextWindow(m.modelContextWindow(m.modelName))
		// Automatic compaction happens inside the agent goroutine. Record its
		// summary alongside the run events so the next prompt/resume can rebuild
		// from a durable boundary instead of replaying the discarded prefix.
		compactionRecorded := false
		options.OnCompaction = func(compaction agent.CompactionResult) {
			if compactionRecorded || m.sessionStore == nil || strings.TrimSpace(m.activeSession.SessionID) == "" {
				return
			}
			plan, err := m.sessionStore.PlanCompaction(m.activeSession.SessionID, sessions.CompactionOptions{PreserveLast: tuiCompactionPreserveLast})
			if err != nil || plan.CompactableCount == 0 {
				return
			}
			payload, err := sessions.CompactionPayloadFromPlan(compaction.SummaryText, plan)
			if err != nil {
				return
			}
			compactionRecorded = true
			sessionEvents = append(sessionEvents, pendingSessionEvent{Type: sessions.EventCompaction, Payload: payload})
		}

		// Post-edit self-correction is on by default in the TUI but kept FAST: it
		// runs LSP diagnostics over the changed files only — cheap, change-scoped,
		// and a no-op when no language server is installed. The project test plan
		// (`go test ./...`, whole-repo) is NOT run per edit by default — that would
		// add the full suite's latency to every turn and let a pre-existing failure
		// hijack the agent — so the test half is opt-in via `/selfcorrect on`
		// (m.selfCorrectTests). The spec-draft (planning) path never wires it,
		// matching exec; the per-turn lsp.Manager is torn down when this run
		// returns; auto-fix vs report-only follows the active permission mode.
		if !runOptions.specDraft && !runOptions.noTools && options.Cwd != "" {
			lspManager := lsp.NewManager(options.Cwd)
			defer func() {
				shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
				defer cancel()
				_ = lspManager.Shutdown(shutdownCtx)
			}()
			options.SelfCorrect = agent.NewSelfCorrector(options.Cwd, agent.NewLSPDiagnosticsChecker(lspManager), agent.NewProjectVerifier(options.Cwd), agent.SelfCorrectConfig{
				Enabled:      true,
				IncludeTests: m.selfCorrectTests,
				IncludeLSP:   true,
				Autonomy:     selfCorrectAutonomyForMode(options.PermissionMode),
			})
		}

		// Builder harness: Planner → Executor → Planner validation. The planner
		// validates after execution and updates the same sticky roadmap; no
		// separate evaluator status is rendered.
		// noTools runs (/btw asides) skip the whole Planner→Executor harness: a
		// quick side question must not pay a planning pass ("Planning the
		// approach…" on a letter-count question) or a validation round.
		// Some providers synthesize tool-call ids that repeat within a run (e.g.
		// Gemini restarts its gemini_tool_N numbering on every provider turn).
		// Transcript rows need distinct ids for dedup and call→result collapse,
		// so repeats get an ordinal suffix; session payloads keep the provider's
		// original ids.
		callSeq := map[string]int{}
		// Provider reasoning is useful for accuracy but is private output. Keep
		// it inside the agent/provider state; do not capture or forward it to the
		// transcript, remote mirror, or any other user-facing surface.
		flushReasoning := func(time.Time) {}

		// Builder harness only — pure Q&A (RunModeQuick) must never pay a
		// planner pass ("Planning the approach…") or validation round. Live:
		// multi-sentence questions with sentence periods mis-routed to builder
		// and sat idle ~1 min before streaming an Answer while the spinner kept
		// spinning through leftover executor/validation turns.
		if !runOptions.specDraft && !runOptions.noTools && runOptions.runMode != agent.RunModeQuick {
			options.Evaluate = true
			options.UnifiedWorkflow = true
			options.VerifyBeforeDone = true
			options.PlannerPrompt = runOptions.plannerPrompt
			// The unified workflow has no separate planner identity. This label
			// makes the first private analysis visible as a concise phase without
			// dumping model reasoning into the transcript.
			options.OnPlanStart = func() {
				status := "Analyzing request…"
				if len(images) > 0 {
					status = "Analyzing request and reference image…"
				}
				row := transcriptRow{
					kind:  rowSystem,
					id:    fmt.Sprintf("planner_start_%d", runID),
					text:  status,
					runID: runID,
				}
				rows = append(rows, row)
				m.sendAgentRow(runID, row)
			}
			options.OnPlan = func(plan string) {
				row := transcriptRow{
					kind:  rowSystem,
					id:    fmt.Sprintf("planner_%d", runID),
					text:  "Plan:\n" + plan,
					runID: runID,
				}
				rows = append(rows, row)
				m.sendAgentRow(runID, row)
				// Seed the sticky plan immediately from the planner callback. The
				// generator may not call update_plan until after its first tool turn;
				// waiting for that tool made the sidebar appear stale or empty even
				// though the harness had already produced a roadmap.
				if items := parsePlannerOutputText(row.text, 8); len(items) > 0 && m.runtimeMessageSink != nil {
					m.runtimeMessageSink(planUpdateMsg{runID: runID, items: items, authoritative: true})
				}
			}
			options.OnPlanProgress = func(items []tools.PlanItem) {
				if m.runtimeMessageSink != nil {
					m.runtimeMessageSink(planUpdateMsg{runID: runID, items: items, authoritative: true})
				}
			}
			options.OnValidationStart = func() {
				// Flush planner-side reasoning before validation so the
				// transcript order stays process → plan → tools → validation.
				flushReasoning(m.now())
				row := transcriptRow{
					kind:  rowSystem,
					id:    fmt.Sprintf("planner_validation_start_%d_%d", runID, time.Now().UnixNano()),
					text:  "Checking the result…",
					runID: runID,
				}
				rows = append(rows, row)
				m.sendAgentRow(runID, row)
			}
		}

		onText := options.OnText
		options.OnText = func(delta string) {
			m.sendAgentText(runID, delta)
			if runOptions.voiceOrigin && runOptions.speakResponse && runOptions.runMode != agent.RunModeBuilder {
				m.voiceSpeakDelta(runOptions.voiceUtteranceID, m.voiceLanguage(), delta)
			}
			if onText != nil {
				onText(delta)
			}
		}
		// Stream a tool call's arguments live so a long write_file/edit shows the
		// code being written instead of a frozen spinner (see streamingToolCallView).
		options.OnToolCallStart = func(id, name string) {
			m.sendToolCallStreamStart(runID, id, name)
		}
		options.OnToolCallDelta = func(id, fragment string) {
			m.sendToolCallStreamDelta(runID, id, fragment)
		}
		// Prefill progress: update the status line to show "prefilling context… X%".
		// This is called on each prefill-progress heartbeat before any output streams.
		options.OnPrefillProgress = func(processed, total int) {
			if m.runtimeMessageSink != nil {
				m.runtimeMessageSink(prefillProgressMsg{runID: runID, processed: processed, total: total})
			}
		}
		onPermissionRequest := options.OnPermissionRequest
		options.OnPermissionRequest = func(ctx context.Context, request agent.PermissionRequest) (agent.PermissionDecision, error) {
			if onPermissionRequest != nil {
				return onPermissionRequest(ctx, request)
			}
			if m.runtimeMessageSink == nil {
				return agent.PermissionDecision{Action: agent.PermissionDecisionDeny, Reason: "permission prompt unavailable"}, nil
			}
			if m.notifier != nil {
				m.notifier.Notify(notify.AwaitingInput, notify.DefaultMessage(notify.AwaitingInput))
			}
			decisionCh := make(chan agent.PermissionDecision, 1)
			m.sendPermissionRequest(runID, request, func(decision agent.PermissionDecision) {
				select {
				case decisionCh <- decision:
				default:
				}
			})
			sessionEvents = append(sessionEvents, pendingSessionEvent{
				Type:    sessions.EventPermissionRequest,
				Payload: request,
			})
			select {
			case decision := <-decisionCh:
				if strings.TrimSpace(decision.Reason) == "" {
					decision.Reason = permissionDecisionReason(permissionDecision(decision.Action))
				}
				return decision, nil
			case <-ctx.Done():
				m.sendInteractionPromptTimeout(runID, request.ToolCallID, "permission")
				return agent.PermissionDecision{Action: agent.PermissionDecisionDeny, Reason: ctx.Err().Error()}, ctx.Err()
			}
		}

		onAskUser := options.OnAskUser
		options.OnAskUser = func(ctx context.Context, request agent.AskUserRequest) (agent.AskUserResponse, error) {
			if onAskUser != nil {
				return onAskUser(ctx, request)
			}
			if m.runtimeMessageSink == nil {
				// No interactive surface: let the loop degrade gracefully.
				return agent.AskUserResponse{}, fmt.Errorf("ask_user prompt unavailable")
			}
			// Only notify when there is actually something to answer — a request
			// with no questions auto-resolves without ever prompting the user.
			if m.notifier != nil && len(request.Questions) > 0 {
				m.notifier.Notify(notify.AwaitingInput, notify.DefaultMessage(notify.AwaitingInput))
			}
			answerCh := make(chan []string, 1)
			m.sendAskUserRequest(runID, request, func(answers []string) {
				select {
				case answerCh <- answers:
				default:
				}
			})
			sessionEvents = append(sessionEvents, pendingSessionEvent{
				Type:    sessions.EventMessage,
				Payload: askUserSessionPayload(request),
			})
			select {
			case answers := <-answerCh:
				// Persist the answers next to the question event so the exchange
				// is complete on /resume; rehydration renders them as a system note.
				sessionEvents = append(sessionEvents, pendingSessionEvent{
					Type: sessions.EventMessage,
					Payload: map[string]any{
						"role":       "ask_user_answers",
						"toolCallId": request.ToolCallID,
						"answers":    answers,
					},
				})
				return agent.AskUserResponse{Answers: answers}, nil
			case <-ctx.Done():
				m.sendInteractionPromptTimeout(runID, request.ToolCallID, "ask_user")
				return agent.AskUserResponse{}, ctx.Err()
			}
		}

		options.OnConfirmation = func(ctx context.Context, question string) (bool, error) {
			if m.runtimeMessageSink == nil {
				// No interactive surface: cannot prompt the user.
				return false, fmt.Errorf("confirmation prompt unavailable")
			}
			// Reuse the ask_user mechanism for simple yes/no confirmations.
			// Route the confirmation as an ask_user question with yes/no options.
			request := agent.AskUserRequest{
				ToolCallID: "confirmation",
				Questions: []agent.AskUserQuestion{
					{
						Question:    question,
						Options:     []string{"Yes", "No"},
						Recommended: "Yes",
					},
				},
			}
			if m.notifier != nil {
				m.notifier.Notify(notify.AwaitingInput, notify.DefaultMessage(notify.AwaitingInput))
			}
			answerCh := make(chan []string, 1)
			m.sendAskUserRequest(runID, request, func(answers []string) {
				select {
				case answerCh <- answers:
				default:
				}
			})
			select {
			case answers := <-answerCh:
				// Treat "Yes" as confirmed, anything else as declined
				if len(answers) > 0 && answers[0] == "Yes" {
					return true, nil
				}
				return false, nil
			case <-ctx.Done():
				m.sendInteractionPromptTimeout(runID, request.ToolCallID, "ask_user")
				if errors.Is(ctx.Err(), context.DeadlineExceeded) {
					return false, nil
				}
				return false, ctx.Err()
			case <-runCtx.Done():
				return false, runCtx.Err()
			}
		}

		onReasoning := options.OnReasoning
		options.OnReasoning = func(delta string) {
			// Feed the live state so the TUI can show its compact, display-time
			// sanitized tail. It is never committed to the transcript or mirror.
			if delta != "" {
				m.sendAgentReasoning(runID, delta)
			}
			if onReasoning != nil {
				onReasoning(delta)
			}
		}

		onToolCall := options.OnToolCall
		options.OnToolCall = func(call agent.ToolCall) {
			flushReasoning(m.now())
			toolCalls++
			callSeq[call.ID]++
			row := transcriptRow{
				kind:    rowToolCall,
				id:      effectiveToolRowID(call.ID, callSeq[call.ID]),
				text:    "tool call: " + call.Name,
				tool:    call.Name,
				detail:  argHint(call.Arguments),
				arg:     argHintSecondary(call.Arguments),
				rawArgs: call.Arguments,
				runID:   runID,
			}
			// A Task delegation is shown by the specialist card below, and update_plan
			// is shown by the pinned plan panel + PLAN sidebar, so skip both redundant
			// transcript cards — the dedicated UI supersedes them.
			if !toolCardSuppressedInTranscript(call.Name) {
				rows = append(rows, row)
				m.sendAgentRow(runID, row)
			}
			// Data-flow ledger: report the external destinations this call sends
			// data to (web hosts, MCP servers, search) so the SAFETY sidebar can
			// show where session data flows.
			if dests := dataDestinations(call.Name, call.Arguments); len(dests) > 0 && m.runtimeMessageSink != nil {
				m.runtimeMessageSink(dataFlowMsg{runID: runID, destinations: dests})
			}
			// Track specialist delegation: when the Task tool is called, register
			// the specialist start so the specialist card + task table can show
			// live status. The child session ID is not known yet (it's created
			// inside the executor), so we use the tool call ID as a temporary
			// key and reconcile on the result.
			if call.Name == "Task" {
				name, desc := parseTaskCallArgs(call.Arguments)
				if m.runtimeMessageSink != nil {
					m.runtimeMessageSink(specialistStartMsg{
						runID:          runID,
						name:           name,
						description:    desc,
						childSessionID: call.ID,
					})
				}
			}
			sessionEvents = append(sessionEvents, pendingSessionEvent{
				Type: sessions.EventToolCall,
				Payload: map[string]any{
					"id":        call.ID,
					"name":      call.Name,
					"arguments": call.Arguments,
				},
			})
			// Snapshot before-state of files this call will mutate, NOW (before the
			// mutation runs), then batch the checkpoint event IN ORDER with the other
			// session events so the recorded sequence matches execution (recording it
			// out-of-band would reorder it ahead of the batched tool_call/result).
			// SnapshotForCheckpoint writes the blobs; the batched event referencing
			// them is flushed at end-of-run AND on cancel (flushRunIDs), so the blobs
			// never stay orphaned — see its contract in internal/sessions.
			if m.sessionStore != nil && m.activeSession.SessionID != "" {
				var args map[string]any
				if call.Arguments != "" {
					_ = json.Unmarshal([]byte(call.Arguments), &args)
				}
				if targets := tools.MutationTargets(m.cwd, call.Name, args); len(targets) > 0 {
					if payload, ok := m.sessionStore.SnapshotForCheckpoint(m.activeSession.SessionID, m.cwd, call.Name, targets); ok {
						sessionEvents = append(sessionEvents, pendingSessionEvent{
							Type:    sessions.EventSessionCheckpoint,
							Payload: payload,
						})
					}
				}
			}
			if onToolCall != nil {
				onToolCall(call)
			}
		}

		options.OnToolProgress = func(toolCallID string, event streamjson.Event) {
			if event.Type == streamjson.EventToolCall && m.runtimeMessageSink != nil {
				m.runtimeMessageSink(specialistProgressMsg{
					runID:      runID,
					toolCallID: toolCallID,
					toolName:   event.Name,
					detail:     toolCallSummary(event),
				})
			}
		}

		onToolResult := options.OnToolResult
		options.OnToolResult = func(result agent.ToolResult) {
			if runOptions.specDraft {
				if info, ok := tuiSpecReviewFromToolResult(result, m.activeSession.SessionID); ok {
					specReview = &info
				}
			}
			row := transcriptRow{
				kind:         rowToolResult,
				id:           effectiveToolRowID(result.ToolCallID, callSeq[result.ToolCallID]),
				text:         toolResultRowText(result),
				tool:         result.Name,
				status:       result.Status,
				detail:       toolResultDetail(result),
				runID:        runID,
				changedFiles: result.ChangedFiles,
			}
			if (isBrowserTool(result.Name) || result.Meta["browser_preview_screenshot"] == "true" || result.Meta["blocked"] == "file_url") && len(result.Images) > 0 {
				row.browserImage = append([]byte(nil), result.Images[0].Data...)
			}
			// A Task result is shown by the specialist card, and update_plan by the
			// plan panel/sidebar, so skip both redundant transcript rows. Internal
			// duplicate-call results are also hidden: the model still receives the
			// corrective output, but the user should never see a fake red command
			// error for a call that was deliberately not re-executed.
			internalDedup := result.Meta != nil && result.Meta["internal_dedup"] == "true"
			// An oversized streamed file call is a recoverable harness condition,
			// not a user-facing failure. The model receives the real corrective
			// result; the TUI shows only a short status — and must not claim
			// "preserved" when content was actually saved via partial recovery.
			recovery := result.Meta != nil && result.Meta["recovery"] == "true"
			partialSaved := result.Meta != nil && result.Meta["partial_recovery"] == "true"
			autoAppend := result.Meta != nil && result.Meta["auto_append_delta"] == "true"
			overflowRecovery := recovery || partialSaved || autoAppend ||
				(strings.Contains(result.Output, "tool call arguments exceeded") &&
					strings.Contains(result.Output, "DO NOT retry edit_file"))
			if overflowRecovery {
				status := "File operation recovered safely"
				if partialSaved {
					status = "Saved partial file content; continuing with append (nothing lost)"
				} else if autoAppend {
					status = "Continued file from saved prefix (no rewrite)"
				} else if result.Meta != nil && result.Meta["recovery_tool"] == "edit_file" {
					status = "Edit kept safe; use a fresh anchor or full-file write"
				} else if recovery {
					status = "File kept safe; retrying with a smaller complete operation"
				}
				if result.Meta != nil && result.Meta["append_after_edit_abort"] == "true" {
					status = "Append blocked; original file preserved safely"
				}
				statusRow := transcriptRow{kind: rowSystem, id: "safe_write_" + result.ToolCallID, text: status, runID: runID}
				rows = append(rows, statusRow)
				m.sendAgentRow(runID, statusRow)
			}
			if !toolCardSuppressedInTranscript(result.Name) && !internalDedup && !overflowRecovery {
				rows = append(rows, row)
				m.sendAgentRow(runID, row)
			}
			// Sync the sticky plan panel when update_plan runs.
			if result.Name == "update_plan" && m.registry != nil {
				if planTool, ok := m.registry.Get("update_plan"); ok {
					if reader, ok := planTool.(interface{ CurrentPlan() []tools.PlanItem }); ok {
						if m.runtimeMessageSink != nil {
							m.runtimeMessageSink(planUpdateMsg{runID: runID, items: reader.CurrentPlan()})
						}
					}
				}
			}
			toolPayload := map[string]any{
				"toolCallId": result.ToolCallID,
				"name":       result.Name,
				"status":     string(result.Status),
				"output":     result.Output,
			}
			if result.Lifecycle != "" {
				toolPayload["lifecycle"] = string(result.Lifecycle)
			}
			if result.Error != nil {
				toolPayload["error"] = result.Error
			}
			if result.Redacted {
				toolPayload["redacted"] = true
			}
			if len(result.Meta) > 0 {
				toolPayload["meta"] = result.Meta
			}
			if len(result.ChangedFiles) > 0 {
				toolPayload["changedFiles"] = result.ChangedFiles
			}
			sessionEvents = append(sessionEvents, pendingSessionEvent{
				Type:    sessions.EventToolResult,
				Payload: toolPayload,
			})
			// Complete specialist tracking when the Task tool returns.
			if result.Name == "Task" {
				status := specialistCompleted
				if result.Status == tools.StatusError {
					status = specialistError
				}
				childSessionID := result.ToolCallID
				if sid, ok := result.Meta["session_id"]; ok && sid != "" {
					childSessionID = sid
				}
				if m.runtimeMessageSink != nil {
					m.runtimeMessageSink(specialistCompleteMsg{
						runID:          runID,
						toolCallID:     result.ToolCallID,
						childSessionID: childSessionID,
						status:         status,
						errorMsg:       result.Output,
					})
				}
			}
			// swarm_collect carries task_id -> session_id for completed members, so
			// the AGENTS sidebar rows can drill into a member's session like a
			// specialist card.
			if result.Name == "swarm_collect" && len(result.Meta) > 0 && m.runtimeMessageSink != nil {
				m.runtimeMessageSink(swarmSessionsMsg{runID: runID, sessions: result.Meta})
			}
			if onToolResult != nil {
				onToolResult(result)
			}
		}

		onPermission := options.OnPermission
		options.OnPermission = func(event agent.PermissionEvent) {
			// The audit event is recorded for every call so the session log stays
			// complete; the visible row is only emitted when the event carries
			// user-facing information (a real prompt, a denial, an explicit durable
			// grant), not for silent auto-approvals.
			if permissionEventIsNoteworthy(event) {
				row := permissionTranscriptRow(event)
				row.runID = runID
				rows = append(rows, row)
				m.sendAgentRow(runID, row)
			}
			sessionEvents = append(sessionEvents, pendingSessionEvent{
				Type:    tuiPermissionEventType(event),
				Payload: event,
			})
			if onPermission != nil {
				onPermission(event)
			}
		}

		onUsage := options.OnUsage
		options.OnUsage = func(event sonutruntime.Usage) {
			usageEvents = append(usageEvents, event)
			sessionEvents = append(sessionEvents, pendingSessionEvent{
				Type:    sessions.EventUsage,
				Payload: usage.EventUsagePayload(event),
			})
			m.sendAgentUsage(runID, usageModelID, event)
			if onUsage != nil {
				onUsage(event)
			}
		}

		result, err := agent.Run(runCtx, prompt, m.provider, options)
		if err != nil {
			flushReasoning(m.now())
			sessionEvents = append(sessionEvents, pendingSessionEvent{
				Type:    sessions.EventError,
				Payload: map[string]any{"message": err.Error()},
			})
			return agentResponseMsg{runID: runID, rows: rows, usageEvents: usageEvents, usageModelID: usageModelID, sessionEvents: sessionEvents, err: err, turnTools: toolCalls, turnElapsed: m.now().Sub(started)}
		}
		if runOptions.specDraft {
			if result.StopReason != agent.StopReasonSpecReviewRequired || specReview == nil || specReview.SpecID == "" || specReview.SpecFilePath == "" {
				err := fmt.Errorf("spec draft ended without submit_spec")
				flushReasoning(m.now())
				sessionEvents = append(sessionEvents, pendingSessionEvent{
					Type:    sessions.EventError,
					Payload: map[string]any{"message": err.Error()},
				})
				return agentResponseMsg{runID: runID, rows: rows, usageEvents: usageEvents, usageModelID: usageModelID, sessionEvents: sessionEvents, err: err, turnTools: toolCalls, turnElapsed: m.now().Sub(started)}
			}
			flushReasoning(m.now())
			return agentResponseMsg{runID: runID, rows: rows, usageEvents: usageEvents, usageModelID: usageModelID, sessionEvents: sessionEvents, specReview: specReview, turnTools: toolCalls, turnElapsed: m.now().Sub(started)}
		}
		flushReasoning(m.now())
		elapsed := m.now().Sub(started)
		rows = append(rows, transcriptRow{
			kind:        rowAssistant,
			text:        result.FinalAnswer,
			final:       true,
			turnTools:   toolCalls,
			turnElapsed: elapsed,
		})
		if notice := result.TruncationNotice(); notice != "" {
			rows = append(rows, transcriptRow{kind: rowSystem, text: notice})
		}
		sessionEvents = append(sessionEvents, pendingSessionEvent{
			Type: sessions.EventMessage,
			Payload: map[string]any{
				"role":    "assistant",
				"content": result.FinalAnswer,
			},
		})
		return agentResponseMsg{runID: runID, rows: rows, usageEvents: usageEvents, usageModelID: usageModelID, sessionEvents: sessionEvents, turnTools: toolCalls, turnElapsed: elapsed}
	}
}

func (m model) sendPermissionRequest(runID int, request agent.PermissionRequest, decide func(agent.PermissionDecision)) {
	if m.runtimeMessageSink == nil {
		return
	}
	m.runtimeMessageSink(permissionRequestMsg{runID: runID, request: request, decide: decide})
}

func (m model) sendAskUserRequest(runID int, request agent.AskUserRequest, answer func([]string)) {
	if m.runtimeMessageSink == nil {
		return
	}
	m.runtimeMessageSink(askUserRequestMsg{runID: runID, request: request, answer: answer})
}

func (m model) sendInteractionPromptTimeout(runID int, toolCallID, kind string) {
	if m.runtimeMessageSink == nil {
		return
	}
	m.runtimeMessageSink(interactionPromptTimeoutMsg{runID: runID, toolCallID: toolCallID, kind: kind})
}

func tuiPermissionEventType(event agent.PermissionEvent) sessions.EventType {
	if event.Action == agent.PermissionActionPrompt {
		return sessions.EventPermissionRequest
	}
	if event.Action == agent.PermissionActionAllow || event.Action == agent.PermissionActionDeny || event.Action == agent.PermissionActionCancel {
		return sessions.EventPermissionDecision
	}
	return sessions.EventPermission
}

func (m model) sendAgentRow(runID int, row transcriptRow) {
	if m.runtimeMessageSink == nil {
		return
	}
	m.runtimeMessageSink(agentRowMsg{runID: runID, row: row})
}

func (m model) sendAgentText(runID int, delta string) {
	if m.runtimeMessageSink == nil {
		return
	}
	m.runtimeMessageSink(agentTextMsg{runID: runID, delta: delta})
}

func (m model) sendToolCallStreamStart(runID int, id, name string) {
	if m.runtimeMessageSink == nil {
		return
	}
	m.runtimeMessageSink(toolCallStreamStartMsg{runID: runID, id: id, name: name})
}

func (m model) sendToolCallStreamDelta(runID int, id, fragment string) {
	if m.runtimeMessageSink == nil {
		return
	}
	m.runtimeMessageSink(toolCallStreamDeltaMsg{runID: runID, id: id, fragment: fragment})
}

// clearStreamingToolCall drops the in-progress live "writing" block (id + name +
// accumulated args). Called whenever the streamed tool call is no longer the
// active live preview: it finalizes into a card, text resumes, the run ends, or
// the run is cancelled. Releasing the args buffer also caps memory after a write.
func (m *model) clearStreamingToolCall() {
	m.streamCallID = ""
	m.streamCallName = ""
	m.streamCallDecoder = nil
}

func (m model) sendAgentReasoning(runID int, delta string) {
	if m.runtimeMessageSink == nil {
		return
	}
	m.runtimeMessageSink(agentReasoningMsg{runID: runID, delta: delta})
}

func (m model) sendAgentUsage(runID int, modelID string, event sonutruntime.Usage) {
	if m.runtimeMessageSink == nil {
		return
	}
	m.runtimeMessageSink(agentUsageMsg{runID: runID, modelID: modelID, usage: event})
}

// toolResultDetail is the card body source: the rich card-only Display.Preview
// (a code/diff preview) when present on a successful result, else the Output that
// the model also saw. Error results keep their Output so the failure shows.
func toolResultDetail(result agent.ToolResult) string {
	if result.Status != tools.StatusError && strings.TrimSpace(result.Display.Preview) != "" {
		return result.Display.Preview
	}
	return result.Output
}

func toolResultRowText(result agent.ToolResult) string {
	status := result.Status
	if status == "" {
		status = tools.StatusOK
	}
	return fmt.Sprintf("tool result: %s %s %s", result.Name, status, truncateTUIOutput(result.Output, tuiToolOutputLimit))
}

// parseMemoryContextSize parses context-size arguments for the /memory command.
// Accepts: 32k, 64k, 128k, 256k (case-insensitive). Returns 0 if not a size arg.
func parseMemoryContextSize(arg string) int {
	switch strings.ToLower(strings.TrimSpace(arg)) {
	case "32k", "32768":
		return 32768
	case "64k", "65536":
		return 65536
	case "128k", "131072":
		return 131072
	case "256k", "262144":
		return 262144
	}
	return 0
}

// memoryCtxLabel returns a human-readable label for a context size in tokens.
func memoryCtxLabel(tokens int) string {
	switch tokens {
	case 32768:
		return "32k"
	case 65536:
		return "64k"
	case 131072:
		return "128k"
	case 262144:
		return "256k"
	default:
		return fmt.Sprintf("%dk", tokens/1024)
	}
}

// parsePlannerOutputText extracts plan items from a planner row's text.
// The text has format "Plan:\n1. ...\n2. ...\n" or "Plan:\n- ...\n- ..." etc.
// Extracts numbered (1. 2) 3)) or bulleted (- * •) lines as plan steps.
// Returns up to maxSteps items; skips empty lines and markers. First step is marked in_progress.
func parsePlannerOutputText(text string, maxSteps int) []tools.PlanItem {
	const prefix = "Plan:\n"
	if !strings.HasPrefix(text, prefix) {
		return nil
	}

	// Strip the "Plan:\n" prefix
	body := text[len(prefix):]
	lines := strings.Split(body, "\n")
	hasNumbered := false
	for _, line := range lines {
		if isNumberedPlanLine(strings.TrimSpace(line)) {
			hasNumbered = true
			break
		}
	}

	var items []tools.PlanItem
	for _, line := range lines {
		if len(items) >= maxSteps {
			break
		}
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		// When numbered titles are present, detail lines belong to the title;
		// they are not separate roadmap items. This prevents "Do: write_file…"
		// from becoming a second fake step in the sidebar.
		if hasNumbered && !isNumberedPlanLine(line) {
			continue
		}

		// Extract content by stripping numbered or bulleted markers
		content := extractPlanStepContent(line)
		if content != "" {
			status := "pending"
			if len(items) == 0 {
				status = "in_progress" // First step is in_progress
			}
			items = append(items, tools.PlanItem{
				ID:      fmt.Sprintf("%d", len(items)+1),
				Content: content,
				Status:  status,
			})
		}
	}

	return items
}

// extractPlanStepContent strips numbered and bulleted markers from a plan line.
// Handles: "1. text", "1) text", "- text", "* text", "• text"
func extractPlanStepContent(line string) string {
	line = strings.TrimSpace(line)
	// Models occasionally terminate a markdown list with a bare marker. It is
	// not a step and must not become an empty row in the PLAN sidebar.
	if line == "" || line == "-" || line == "*" || line == "•" {
		return ""
	}

	// Convert to runes for cleaner multi-byte character handling
	runes := []rune(line)
	if len(runes) < 2 {
		return line
	}

	// Try numbered formats: "N." or "N)"
	if runes[0] >= '0' && runes[0] <= '9' {
		// Single digit number
		if len(runes) >= 3 {
			if runes[1] == '.' && runes[2] == ' ' {
				return strings.TrimSpace(string(runes[3:]))
			}
			if runes[1] == ')' && runes[2] == ' ' {
				return strings.TrimSpace(string(runes[3:]))
			}
		}
		// Multi-digit number: find the first non-digit
		for i := 1; i < len(runes); i++ {
			if runes[i] < '0' || runes[i] > '9' {
				if i+1 < len(runes) && (runes[i] == '.' || runes[i] == ')') && runes[i+1] == ' ' {
					return strings.TrimSpace(string(runes[i+2:]))
				}
				break
			}
		}
	}

	// Try bullet formats: "-", "*", "•"
	if runes[0] == '-' && len(runes) > 1 && runes[1] == ' ' {
		return strings.TrimSpace(string(runes[2:]))
	}
	if runes[0] == '*' && len(runes) > 1 && runes[1] == ' ' {
		return strings.TrimSpace(string(runes[2:]))
	}
	if runes[0] == '•' && len(runes) > 1 && runes[1] == ' ' {
		return strings.TrimSpace(string(runes[2:]))
	}

	// If no markers found, return the original trimmed line
	return line
}
