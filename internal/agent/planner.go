package agent

import (
	"context"
	"encoding/json"
	"errors"
	"log"
	"os"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/aistatine/sonut/internal/observability"
	"github.com/aistatine/sonut/internal/sonutruntime"
	"github.com/aistatine/sonut/internal/tools"
)

// Planner role (harness architecture: Planner → Executor → Planner validation).
// Before the executor starts working, a separate planner pass —
// fresh minimal context, its own strict system prompt, MAXIMUM thinking —
// turns the user's request into a short numbered plan. The plan is injected
// into the generator's conversation as a seed so the generator starts with a
// concrete roadmap instead of discovering structure turn by turn. Bounded to
// one pass per run; a planner failure is never fatal — the run simply
// continues plannerless.

// plannerInstructions is the planning-specific instruction appended to the user
// message. The planner never executes anything; it only decomposes the request
// into concrete, executor-ready steps. This instruction is folded into the last
// user message instead of being a separate system prompt, so the planner shares
// the main conversation's system prompt and can reuse its KV cache prefix.
//
// Quality bar (from live thrash analysis): shallow one-liners ("create html
// file", "add physics") force the executor to re-plan for minutes before the
// first tool call. Each step must carry enough HOW + DONE WHEN that a
// concise-thinking executor can emit a real tool call immediately.
// Stable marker "Before executing anything" is asserted by harness tests and
// keeps the instruction recognizable across prompt rewrites.
const plannerInstructions = `Before executing anything, privately decide the user's intent, deliverables, and verification. Do not print that analysis, a "Thinking Process", prompt review, or constraint checklist.

Then reply ONLY with a numbered plan (3–8 unique steps). Do NOT execute. Stop after the list — never restate steps or append a second copy.

Executor has zero thinking: every step must be a clear-cut task with enough HOW for one immediate writing/building tool call.

Use [grounding context] facts verbatim in Do lines (exact hex colors, existing function names, real file paths). Do NOT re-read any file already covered in [grounding context] — those facts are authoritative.
NEVER include read_file, list_directory, or inspection steps in the plan. All inspection is done during planner grounding. Plan steps MUST be write_file, edit_file, append_file, or exec_command actions ONLY.
If no grounding context is present: workspace snapshot paths, AGENTS.md, attached images (layout/colors/spacing/type/states), named files. Prefer existing files (game.js, index.html) over twins.

Existing file rule: reuse the file and make the smallest necessary change. Use edit_file/apply_patch with a unique 1-3 line anchor for surgical changes; use append_file for true additions. Use write_file only for a new file or an explicitly requested full rewrite — never copy an entire existing file into an edit step.

Rules: exact paths + tools (write_file/append_file/edit_file/exec_command/web_search); Do = HOW (fields, hex, layout, bindings); Done when = observable check; no source/fences/tool JSON; no duplicate titles; max 8; browser/verify only for UI/games.
Multi-file work: one numbered step per deliverable path (index.html, game.js, player.js, …) — never bury several writes in one step. Large files: Do must say write_file first ≤100 lines then append_file chunks.
Single-file HTML game/page: first write_file MUST be a real HTML shell (<!DOCTYPE html>, <canvas>, open <script>) — NEVER bare JS constants into a .html path. Then append_file game logic inside the script in ≤100-line chunks. Prefer compact procedural level data over giant zero-filled 2D arrays. Paths are relative to the Working directory — never prefix with parent folder names (cwd=.../sonut/test → path=supermario.html, NOT sonut/test/supermario.html). If a file is truncated mid-generation (ends mid-array/object or missing </html>), continue with append_file ONLY — never re-issue edit_file with identical old_string/new_string.

Shape:
1. <title with path>
   - Do: <tool + path + HOW>
   - Done when: <criterion>`

const plannerValidationInstructions = `You are the planning agent returning to validate an execution run. Act as a skeptical verifier, not as the executor. Review the original task, the acceptance conditions, concrete tool results, changed files, and the executor's final answer. Never approve based only on the executor's claim. You own the roadmap and may call update_plan when the roadmap must change. You may use the available browser tools to validate the actual running result. Never write source files and never act as the executor. Decide whether every important requirement is demonstrably complete from evidence.

If everything is complete, return VERDICT: PASS and reproduce the current roadmap with terminal statuses. If anything is missing or unverified, return VERDICT: REPLAN, list at most three concrete issues, then return a corrected numbered roadmap focused only on those gaps. For UI or game work, source edits alone are never full proof — open the app in the headful browser and gather enough evidence: open the page, then either (a) capture a screenshot or DOM snapshot, and (b) either interact with controls OR confirm the console is clean. Do not demand a perfect end-to-end test script. Prefer PASS with minor notes over endless REPLAN when the core request clearly works. Keep every roadmap item to one short line. Never repeat private reasoning or dump file contents.

Output exactly:
VERDICT: PASS or VERDICT: REPLAN
ISSUES: (only when replanning)
1. <issue>
PLAN:
1. [completed|in_progress|pending|failed] <roadmap item>
...`

// maxPlannerValidationRounds bounds planner ↔ executor rework. Two rounds are
// intentional: the first validation can reject, the executor gets one concrete
// repair opportunity, and the corrected result receives a final PASS/REPLAN
// decision. This keeps accuracy without reopening the historical unbounded
// validation loop.
const maxPlannerValidationRounds = 2

const (
	plannerEvidenceBudget = 8 * 1024
	plannerItemClamp      = 700
	// Legacy names kept for package-level regression tests and integrations.
	evaluatorItemClamp = plannerItemClamp
)

// plannerEvidence collects only recent, concrete execution evidence for the
// validation pass. Keeping it compact avoids another large context-prefill.
func evaluatorEvidence(messages []sonutruntime.Message) string {
	var items []string
	total := 0
	planSeen := false
	for i := len(messages) - 1; i >= 0 && total < plannerEvidenceBudget; i-- {
		msg := messages[i]
		switch msg.Role {
		case sonutruntime.MessageRoleTool:
			item := "tool_result: " + clampPlannerEvidence(msg.Content)
			items = append(items, item)
			total += len(item)
		case sonutruntime.MessageRoleAssistant:
			for _, call := range msg.ToolCalls {
				if !planSeen && call.Name == "update_plan" {
					item := "plan: " + clampPlannerEvidence(call.Arguments)
					items = append(items, item)
					total += len(item)
					planSeen = true
				}
			}
			if text := strings.TrimSpace(msg.Content); text != "" {
				item := "executor: " + clampPlannerEvidence(text)
				items = append(items, item)
				total += len(item)
			}
		}
	}
	for l, r := 0, len(items)-1; l < r; l, r = l+1, r-1 {
		items[l], items[r] = items[r], items[l]
	}
	return strings.Join(items, "\n")
}

func clampPlannerEvidence(s string) string {
	s = strings.TrimSpace(s)
	if len(s) <= plannerItemClamp {
		return s
	}
	cut := plannerItemClamp
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return s[:cut] + " …[truncated]"
}

func clampEvidence(s string) string { return clampPlannerEvidence(s) }

// parseEvaluatorVerdict remains as a tiny compatibility helper for callers
// that used the old parser directly. The active harness uses
// parsePlannerValidation and never starts an evaluator role.
func parseEvaluatorVerdict(text string) (passed bool, feedback string, err error) {
	trimmed := strings.TrimSpace(text)
	upper := strings.ToUpper(trimmed)
	failIdx := strings.Index(upper, "VERDICT: FAIL")
	passIdx := strings.Index(upper, "VERDICT: PASS")
	if failIdx >= 0 && (passIdx < 0 || failIdx < passIdx) {
		issues := strings.TrimSpace(trimmed[failIdx+len("VERDICT: FAIL"):])
		if issues == "" {
			return true, "", nil
		}
		return false, issues, nil
	}
	return true, "", nil
}

// validateWithPlanner gives the same planning role the final say after the
// executor works. The planner may use only browser tools and update_plan during
// this pass; source edits and shell work remain executor privileges. Keeping the
// browser loop here makes validation real instead of trusting a text-only claim.
func validateWithPlanner(ctx context.Context, provider Provider, systemPrompt, task string, messages []sonutruntime.Message, finalAnswer string, options Options) (passed bool, feedback, plan string, err error) {
	ctx, cancel := context.WithTimeout(ctx, 120*time.Second)
	defer cancel()
	validationStarted := time.Now()
	validationStatus := "error"
	observability.PerfLog("validation_start", "role", "planner", "task_chars", len(task))
	defer func() {
		if err == nil {
			if passed {
				validationStatus = "pass"
			} else {
				validationStatus = "replan"
			}
		}
		observability.PerfLog("validation_end", "role", "planner", "status", validationStatus, "ms", time.Since(validationStarted).Milliseconds(), "plan_bytes", len(plan), "err", err != nil)
	}()
	var prompt strings.Builder
	prompt.WriteString("ORIGINAL TASK:\n")
	prompt.WriteString(strings.TrimSpace(task))
	prompt.WriteString("\n\nEXECUTION EVIDENCE (chronological):\n")
	prompt.WriteString(evaluatorEvidence(messages))
	prompt.WriteString("\n\nEXECUTOR FINAL ANSWER:\n")
	prompt.WriteString(strings.TrimSpace(finalAnswer))
	prompt.WriteString("\n\n")
	definitions := plannerValidationToolDefinitions(options.Registry, options.PermissionMode, options.EnabledTools, options.DisabledTools)
	browserAvailable := plannerValidationHasBrowserTool(definitions)
	if plannerValidationNeedsBrowser(task, messages) {
		if !browserAvailable {
			prompt.WriteString("HEADFUL VALIDATION TOOL: unavailable in this session. Do not claim PASS; return REPLAN with a browser validation step.\n\n")
		} else {
			prompt.WriteString("HEADFUL VALIDATION TOOL: available and required before PASS. Use it now against the changed app.\n\n")
		}
	}
	prompt.WriteString(plannerValidationInstructions)

	validationMessages := []sonutruntime.Message{
		{Role: sonutruntime.MessageRoleSystem, Content: systemPrompt},
		{Role: sonutruntime.MessageRoleUser, Content: prompt.String()},
	}
	requiresBrowser := plannerValidationNeedsBrowser(task, messages)
	var browserEvidence plannerBrowserEvidence
	// Tighter browser validation loop: 4 turns max instead of 6.
	// open → inspect → done. Most validations should complete in 2-3 turns.
	for turn := 0; turn < 4; turn++ {
		stream, streamErr := provider.StreamCompletion(ctx, sonutruntime.CompletionRequest{
			Messages:        validationMessages,
			Tools:           definitions,
			ReasoningEffort: "high",
			// Validation is planner-owned: always think. Do not DisableThinking.
		})
		if streamErr != nil {
			return true, "", "", streamErr
		}
		collected := sonutruntime.CollectStreamWithOptions(ctx, stream, sonutruntime.CollectOptions{
			OnUsage:              options.OnUsage,
			MaxReasoningBytes:    12 * 1024,
			MaxReasoningDuration: 60 * time.Second,
			// Stream validation thinking only (not verdict text) so the UI stays
			// interactive without polluting the assistant final-answer buffer.
			OnReasoning: options.OnReasoning,
		})
		if collected.Error != "" {
			return true, "", "", errors.New(collected.Error)
		}
		validationMessages = append(validationMessages, sonutruntime.Message{
			Role:      sonutruntime.MessageRoleAssistant,
			Content:   collected.Text,
			ToolCalls: historySafeToolCalls(collected.ToolCalls),
			Reasoning: collected.ReasoningBlocks,
		})
		if len(collected.ToolCalls) == 0 {
			passed, feedback, plan, parseErr := parsePlannerValidation(collected.Text)
			if parseErr != nil {
				if !requiresBrowser {
					// Non-visual validation remains best-effort: providers and
					// lightweight test/local models that ignore the validator
					// format must not trap an otherwise completed CLI run.
					return true, "", plan, nil
				}
				return false, "Planner validation did not return the required PASS/REPLAN verdict; rerun validation with concrete evidence.", plan, nil
			}
			if !requiresBrowser || !passed || browserEvidence.complete() {
				return passed, feedback, plan, nil
			}
			feedback = browserEvidence.feedback(browserAvailable)
			return false, feedback, plan, nil
		}

		var screenshotImages []sonutruntime.ImageBlock
		for _, call := range collected.ToolCalls {
			if options.Registry != nil {
				call.Name = options.Registry.Resolve(call.Name)
			}
			if !plannerValidationToolAllowed(call.Name, definitions) {
				validationMessages = append(validationMessages, sonutruntime.Message{
					Role:       sonutruntime.MessageRoleTool,
					ToolCallID: call.ID,
					Content:    "Error: planner validation may call only browser tools or update_plan; executor tools are not available in this phase.",
				})
				continue
			}
			if options.OnToolCall != nil {
				options.OnToolCall(call)
			}
			args := decodePlannerToolArgs(call.Arguments)
			if call.Name == "ask_user" {
				result, askErr := executeAskUser(ctx, options.Registry, call, args, options.PermissionMode, options)
				if askErr != nil && !errors.Is(askErr, context.DeadlineExceeded) {
					return false, "Planner browser validation was canceled while waiting for user input.", "", askErr
				}
				if options.OnToolResult != nil {
					options.OnToolResult(result)
				}
				validationMessages = append(validationMessages, sonutruntime.Message{
					Role:       sonutruntime.MessageRoleTool,
					ToolCallID: call.ID,
					Content:    result.Output,
				})
				continue
			}
			toolCtx, toolCancel := context.WithTimeout(ctx, 45*time.Second)
			result := options.Registry.RunWithOptions(toolCtx, call.Name, args, tools.RunOptions{
				PermissionGranted: true,
				PermissionMode:    string(options.PermissionMode),
				Autonomy:          options.Autonomy,
				Sandbox:           options.Sandbox,
				ToolCallID:        call.ID,
				SessionID:         options.SessionID,
				Model:             options.Model,
				ReasoningEffort:   "high",
				Cwd:               options.Cwd,
				FileTracker:       options.FileTracker,
			})
			toolCancel()
			if isPlannerBrowserTool(call.Name) && result.Status == tools.StatusOK {
				browserEvidence.observe(call.Name, args, result)
			}
			toolResult := ToolResult{
				ToolCallID:   call.ID,
				Name:         call.Name,
				Status:       result.Status,
				Output:       result.Output,
				Meta:         result.Meta,
				ChangedFiles: result.ChangedFiles,
				Images:       sonutruntime.CloneImageBlocks(result.Images),
				Display:      result.Display,
			}
			if options.OnToolResult != nil {
				options.OnToolResult(toolResult)
			}
			validationMessages = append(validationMessages, sonutruntime.Message{
				Role:       sonutruntime.MessageRoleTool,
				ToolCallID: call.ID,
				Content:    result.Output,
			})
			if len(result.Images) > 0 {
				screenshotImages = append(screenshotImages, sonutruntime.CloneImageBlocks(result.Images)...)
			}
		}
		if len(screenshotImages) > 0 {
			validationMessages = append(validationMessages, sonutruntime.Message{
				Role:    sonutruntime.MessageRoleUser,
				Content: "HEADFUL SCREENSHOT EVIDENCE: inspect the attached screenshot of the currently loaded page and compare it with the task requirements before deciding PASS or REPLAN.",
				Images:  screenshotImages,
			})
		}
	}
	if requiresBrowser {
		return false, browserEvidence.feedback(browserAvailable) + " The bounded validation loop ended before sufficient proof was collected.", "", nil
	}
	return false, "Planner validation did not return a verdict before its bounded evidence loop ended.", "", nil
}

type plannerBrowserEvidence struct {
	opened               bool
	domInspected         bool
	screenshotInspected  bool
	consoleChecked       bool
	consoleClean         bool
	interacted           bool
	postInteractionCheck bool
}

func (e *plannerBrowserEvidence) observe(name string, args map[string]any, result tools.Result) {
	action := plannerBrowserAction(name, args)
	if plannerBrowserActionMatches(action, "open", "navigate", "goto", "launch", "connect") {
		e.opened = true
	}
	domInspection := plannerBrowserActionMatches(action, "snapshot", "accessibility", "get_text", "get_html", "eval")
	screenshot := plannerBrowserActionMatches(action, "screenshot") ||
		len(result.Images) > 0 || result.Meta["screenshot_attached"] == "true"
	console := plannerBrowserActionMatches(action, "console", "console_logs")
	interaction := plannerBrowserActionMatches(action,
		"click", "dblclick", "fill", "type", "press", "keyboard", "drag", "select", "check", "uncheck")

	if e.interacted && (domInspection || screenshot) {
		e.postInteractionCheck = true
	}
	if domInspection {
		e.domInspected = true
	}
	if screenshot {
		e.screenshotInspected = true
	}
	if console {
		e.consoleChecked = true
		e.consoleClean = plannerConsoleOutputClean(result.Output)
	}
	// Some browser adapters attach a console summary to navigate/snapshot
	// output instead of requiring a separate console call. Treat that summary
	// as a failed console check so interaction evidence cannot hide a runtime
	// error.
	if browserOutputHasConsoleErrors(result.Output) {
		e.consoleChecked = true
		e.consoleClean = false
	}
	if interaction {
		e.interacted = true
	}
}

func (e plannerBrowserEvidence) complete() bool {
	if !e.opened {
		return false
	}
	hasInspection := e.domInspected || e.screenshotInspected
	hasInteraction := e.interacted
	hasConsoleCheck := e.consoleChecked
	if e.consoleChecked && !e.consoleClean {
		return false
	}
	// Opening and inspecting proves only that something rendered. Require a
	// second independent signal: interaction, or a clean console for static pages.
	return hasInspection && (hasInteraction || (hasConsoleCheck && e.consoleClean))
}

func (e plannerBrowserEvidence) feedback(available bool) string {
	if !available {
		return "Headful browser validation is unavailable; the UI/game result cannot be marked complete from source edits alone."
	}
	var missing []string
	if !e.opened {
		missing = append(missing, "open the changed app in the headful browser")
	}
	hasAnyEvidence := (e.domInspected || e.screenshotInspected) &&
		(e.interacted || (e.consoleChecked && e.consoleClean))

	if !hasAnyEvidence {
		missing = append(missing, "perform at least ONE verification: inspect DOM, capture screenshot, test interaction, or check console")
	}

	if len(missing) == 0 {
		return "Headful browser evidence was collected, but the planner did not produce a valid final verdict."
	}
	return "Headful validation is incomplete: " + strings.Join(missing, "; ") + "."
}

func plannerBrowserAction(name string, args map[string]any) string {
	parts := []string{strings.ToLower(strings.TrimSpace(name))}
	for _, key := range []string{"command", "action", "method"} {
		if value, ok := args[key].(string); ok {
			parts = append(parts, strings.ToLower(strings.TrimSpace(value)))
		}
	}
	return strings.Join(parts, " ")
}

func plannerBrowserActionMatches(action string, markers ...string) bool {
	for _, marker := range markers {
		if strings.Contains(action, marker) {
			return true
		}
	}
	return false
}

func plannerConsoleOutputClean(output string) bool {
	normalized := strings.ToLower(strings.TrimSpace(output))
	if normalized == "" || normalized == "[]" ||
		strings.Contains(normalized, "no console messages") ||
		strings.Contains(normalized, "no errors") ||
		strings.Contains(normalized, "0 errors") ||
		strings.Contains(normalized, `"errors":[]`) {
		return true
	}
	return !browserOutputHasConsoleErrors(normalized)
}

func decodePlannerToolArgs(raw string) map[string]any {
	var args map[string]any
	if strings.TrimSpace(raw) == "" {
		return map[string]any{}
	}
	if err := json.Unmarshal([]byte(raw), &args); err != nil || args == nil {
		return map[string]any{}
	}
	return args
}

// plannerValidationToolDefinitions deliberately exposes a tiny, explicit
// capability set. The planner can inspect and interact with the visible app,
// ask once for user interaction, and maintain the roadmap, but it cannot
// read/write source or run shell.
func plannerValidationToolDefinitions(registry *tools.Registry, permissionMode PermissionMode, enabledTools, disabledTools []string) []sonutruntime.ToolDefinition {
	if registry == nil {
		return nil
	}
	var definitions []sonutruntime.ToolDefinition
	for _, tool := range registry.All() {
		name := tool.Name()
		browserTool := isPlannerBrowserTool(name)
		if name != "update_plan" && name != "ask_user" && !browserTool {
			continue
		}
		// Playwright MCP tools are commonly deferred and permission-prompted in
		// normal executor turns. Validation is a planner-owned, explicitly
		// bounded phase and runs with PermissionGranted=true, so hiding them by
		// the normal advertisement rule would make required headful validation
		// impossible. Respect hard denials and operator filters, but otherwise
		// expose registered browser tools directly to this pass.
		if browserTool {
			if tool.Safety().Permission == tools.PermissionDeny ||
				!ToolAllowedByFilters(name, enabledTools, disabledTools) {
				continue
			}
		} else if !ToolAdvertised(tool, permissionMode) {
			continue
		}
		definitions = append(definitions, sonutruntime.ToolDefinition{
			Name:        name,
			Description: tool.Description(),
			Parameters:  schemaToRuntimeMap(tool.Parameters()),
		})
	}
	sort.Slice(definitions, func(left, right int) bool { return definitions[left].Name < definitions[right].Name })
	return definitions
}

func plannerValidationToolAllowed(name string, definitions []sonutruntime.ToolDefinition) bool {
	for _, definition := range definitions {
		if definition.Name == name {
			return true
		}
	}
	return false
}

func plannerValidationHasBrowserTool(definitions []sonutruntime.ToolDefinition) bool {
	for _, definition := range definitions {
		if isPlannerBrowserTool(definition.Name) {
			return true
		}
	}
	return false
}

func isPlannerBrowserTool(name string) bool {
	name = strings.ToLower(strings.TrimSpace(name))
	return name == "web_browser" ||
		strings.HasPrefix(name, "browser_") ||
		strings.HasPrefix(name, "desktop_") ||
		strings.HasPrefix(name, "mcp_playwright_") ||
		(strings.HasPrefix(name, "mcp_") && strings.Contains(name, "browser"))
}

func plannerValidationNeedsBrowser(task string, messages []sonutruntime.Message) bool {
	text := strings.ToLower(task)
	_ = messages // evidence is supplied separately; do not classify from AGENTS/session prose
	for _, marker := range []string{
		"html", "css", "frontend", "web app", "website", "landing page", "react", "vue", "svelte", "next.js",
		"tailwind", "component", "electron", "ui", "interface", "canvas", "game", "browser", "screenshot", "image",
	} {
		if strings.Contains(text, marker) {
			return true
		}
	}
	return false
}

func parsePlannerValidation(text string) (passed bool, feedback, plan string, err error) {
	trimmed := strings.TrimSpace(text)
	upper := strings.ToUpper(trimmed)
	pass := strings.Index(upper, "VERDICT: PASS")
	replan := strings.Index(upper, "VERDICT: REPLAN")
	if replan < 0 {
		// Accept FAIL from older/local prompt variants while the harness rolls
		// forward; it still routes through planner validation, never evaluator.
		replan = strings.Index(upper, "VERDICT: FAIL")
	}
	if pass < 0 && replan < 0 {
		return false, "", "", errors.New("planner validation did not return VERDICT: PASS or VERDICT: REPLAN")
	}
	if replan >= 0 && (pass < 0 || replan < pass) {
		start := replan
		marker := "VERDICT: REPLAN"
		if strings.HasPrefix(strings.TrimSpace(trimmed[start:]), "VERDICT: FAIL") {
			marker = "VERDICT: FAIL"
		}
		rest := strings.TrimSpace(trimmed[start+len(marker):])
		planIdx := strings.Index(strings.ToUpper(rest), "PLAN:")
		if planIdx >= 0 {
			feedback = strings.TrimSpace(rest[:planIdx])
			plan = resolvePlanText(strings.TrimSpace(rest[planIdx+len("PLAN:"):]), "")
		} else {
			feedback = rest
		}
		if strings.TrimSpace(feedback) == "" && strings.TrimSpace(plan) == "" {
			return true, "", "", nil
		}
		return false, feedback, plan, nil
	}
	planIdx := strings.Index(upper, "PLAN:")
	if planIdx >= 0 {
		plan = resolvePlanText(strings.TrimSpace(trimmed[planIdx+len("PLAN:"):]), "")
	}
	return true, "", plan, nil
}

// visualPlannerSystemPrompt keeps the one image-aware pass cheap. The visual
// intake model only needs to understand the reference and turn it into a
// concrete build plan; loading the full builder prompt and tool-oriented
// workspace guidance here needlessly enlarges the cold vision prefill.
const visualPlannerSystemPrompt = `You are the visual intake and planning stage of a coding agent.

Inspect the attached image together with the user's request, workspace snapshot,
and project guidelines. Decide whether the request needs a builder workflow or a
direct answer.

For a builder request, return ONLY a numbered implementation plan (3-8 steps)
with title + Do + Done when lines. The executor has zero thinking — every Do line
must be a clear-cut task with enough visual facts that it can call tools
immediately: layout regions, hierarchy, exact colors (hex when visible), spacing,
typography (size/weight/family if visible), control states, interactions, and
which existing files to edit. Prefer real workspace paths (game.js, index.html,
…) over inventing twins. Name tools in Do lines when helpful (write_file,
append_file, edit_file, exec_command). NEVER include read_file or inspection steps in the plan. Never dump source code, never claim the
image is missing, never restate the request as prose outside the numbered list.

For a direct-answer request, return only the answer. Never include private
reasoning in the response body (thinking is allowed internally; the user-visible
output is the plan or answer only).`

// plannerTrivialPattern matches greetings and other zero-step turns that gain
// nothing from a planning pass (mirrors autoEffortGreetingPattern's intent).
var plannerTrivialPattern = regexp.MustCompile(`(?i)^\s*(hi|hello|hey|thanks|thank you|ok|okay|cool|nice|great|sounds good|yes|no)\W*$`)

// plannerQuestionPattern matches interrogative openers — pure questions get a
// direct answer, not a plan (a plan for "how many r in strawberry" is noise
// that leaks the answer into a fake preamble).
var plannerQuestionPattern = regexp.MustCompile(`(?i)^\s*(who|what|when|where|why|how|which|is|are|was|were|do|does|did|can|could|should|would|will|am)\b`)

var plannerShortBuildPattern = regexp.MustCompile(`(?i)\b(build|create|fix|edit|update|write|implement|generate|refactor|remove|delete|change|modify)\b|\.[a-z0-9]{1,8}\b`)

// isTrivialPlannerRequest reports whether the run's request is simple enough
// (a very short, single-step ask, or a plain question) that a planner pass
// would add nothing but latency and tokens.
func isTrivialPlannerRequest(prompt string) bool {
	trimmed := strings.TrimSpace(prompt)
	if trimmed == "" || plannerTrivialPattern.MatchString(trimmed) {
		return true
	}
	// Questions want answers, not plans.
	if strings.HasSuffix(trimmed, "?") || plannerQuestionPattern.MatchString(trimmed) {
		return true
	}
	// Continuation turns inherit the existing plan — a new planner pass would
	// just regenerate the same roadmap at full thinking cost.
	if classifyTurnShape(trimmed) == TurnShapeContinuation {
		return true
	}
	// Short build requests are still real work. File names and implementation verbs are
	// stronger signals than word count; treating "implement game.js" as a
	// trivial question skipped grounding and made the planner test—and small
	// real edits—fall into the executor with no repository facts.
	if words := len(strings.Fields(trimmed)); words <= 8 && plannerShortBuildPattern.MatchString(trimmed) {
		return false
	}
	// A short single-sentence ask (no "and"/multi-clause structure) rarely
	// benefits from decomposition; the generator can just do it.
	words := len(strings.Fields(trimmed))
	return words <= 8
}

// plannerMaxPlanBytes caps the plan's visible text. Plans must be detailed
// enough for a concise-thinking executor (title + Do + Done when) but must never
// become full source dumps (observed: HTML game bodies as "step 1"). Anything
// past this is treated as implementation leakage; the stream is cancelled and
// the text collected so far is sanitized. 3.5 KB fits ~8 deep steps without a
// second copy of the list (live thrash: 1–8 then 9–16 identical titles).
const plannerMaxPlanBytes = 3500

// plannerMaxSteps hard-caps unique plan steps after dedupe.
const plannerMaxSteps = 8

// plannerMaxDuration bounds planning so it improves the first action instead
// of becoming a second unattended coding session on slow local models.
const plannerMaxDuration = 75 * time.Second

// ── Planner grounding pass ────────────────────────────────────────────────────
//
// Before the text-only planning turn (runPlannerTurn), the planner runs a short
// read-only tool loop to gather concrete workspace facts: file contents, color
// values, function signatures, structure. The executor then receives plan steps
// with that context baked in and can immediately write code — no re-reading loop.

// plannerGroundingMaxTurns is the hard turn cap for maintenance/debug work.
// Greenfield builds use the compact workspace snapshot and skip this exchange.
const plannerGroundingMaxTurns = 3

// plannerGroundingContextBudget caps the grounding output appended to the
// planning prompt. 6 KB fits several file snippets without bloating the cold
// planning prefill beyond the planner's own budget.
const plannerGroundingContextBudget = 6 * 1024

// plannerGroundingMaxDuration bounds the whole grounding phase wall-clock.
const plannerGroundingMaxDuration = 30 * time.Second

// finishGroundingTool is the synthetic tool name the model calls to signal that
// it has collected enough context. It is never added to the global registry —
// it exists only inside the grounding loop's definition list.
const finishGroundingTool = "finish_grounding"

// plannerGroundingInstructions is the system-level directive for the grounding
// phase. It is appended to the planning prompt so the model knows its role.
const plannerGroundingInstructions = `You are the context-gathering phase of a coding agent's planning system.
Your ONLY job: inspect the files and directories most relevant to the task, then call finish_grounding with a compact summary of what you found.

Rules:
- Use read_file, list_directory, grep, and glob ONLY. Do NOT write, edit, append, or execute anything.
- Read each key file ONCE. Maximum 5 tool calls total, then you MUST call finish_grounding.
- Keep file excerpts short (≤ 30 lines each). Prefer grep over full reads when looking for specific values.
- finish_grounding context field must contain: exact hex colors, existing function/variable names, file line counts, API signatures, and relevant code fragments — the facts an executor needs to write without re-reading.
- Never say you cannot read files. Never ask the user for information. Never output prose; your only text output is the finish_grounding call.`

// plannerGroundingReadOnlyTools is the set of tool names allowed in the
// grounding phase. Anything not in this set is silently rejected.
var plannerGroundingReadOnlyTools = map[string]bool{
	"read_file":          true,
	"read_minified_file": true,
	"list_directory":     true,
	"grep":               true,
	"glob":               true,
	"web_search":         true,
}

// plannerGroundingToolDefinitions returns the read-only tool subset the planner
// may use during the grounding phase, plus the synthetic finish_grounding tool.
func plannerGroundingToolDefinitions(registry *tools.Registry, enabledTools, disabledTools []string) []sonutruntime.ToolDefinition {
	var defs []sonutruntime.ToolDefinition
	if registry != nil {
		for _, tool := range registry.All() {
			name := tool.Name()
			if !plannerGroundingReadOnlyTools[name] {
				continue
			}
			if tool.Safety().Permission == tools.PermissionDeny {
				continue
			}
			if !ToolAllowedByFilters(name, enabledTools, disabledTools) {
				continue
			}
			defs = append(defs, sonutruntime.ToolDefinition{
				Name:        name,
				Description: tool.Description(),
				Parameters:  schemaToRuntimeMap(tool.Parameters()),
			})
		}
	}
	// Always append the synthetic finish_grounding tool last.
	defs = append(defs, sonutruntime.ToolDefinition{
		Name:        finishGroundingTool,
		Description: "Signal that you have finished collecting context. Call this when you have read the key files, or after 5 tool calls at the latest.",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"context": map[string]any{
					"type":        "string",
					"description": "Compact summary of gathered facts: hex colors, function names, file paths, relevant code fragments (≤30 lines each), line counts.",
				},
			},
			"required": []string{"context"},
		},
	})
	sort.Slice(defs, func(i, j int) bool { return defs[i].Name < defs[j].Name })
	return defs
}

// runPlannerGroundingTurn performs the bounded read-only context-gathering phase.
// It gives the planner a short tool loop (≤ plannerGroundingMaxTurns) to read
// key workspace files and collect concrete facts into a grounding context string.
// The string is returned to maybeRunPlanner which embeds it in the text-only
// planning prompt. A nil registry, an empty tool list, or any error returns "".
func runPlannerGroundingTurn(
	ctx context.Context,
	provider Provider,
	systemPrompt string,
	plannerRequest string,
	registry *tools.Registry,
	options Options,
) string {
	if registry == nil {
		return ""
	}
	defs := plannerGroundingToolDefinitions(registry, options.EnabledTools, options.DisabledTools)
	// If only finish_grounding survived (no real read tools available), skip.
	if len(defs) <= 1 {
		return ""
	}

	ctx, cancel := context.WithTimeout(ctx, plannerGroundingMaxDuration)
	defer cancel()

	observability.PerfLog("grounding_start", "phase", "planner_grounding", "task_chars", len(plannerRequest))
	groundingStarted := time.Now()

	userContent := strings.TrimSpace(plannerRequest) + "\n\n" + plannerGroundingInstructions
	groundingMessages := []sonutruntime.Message{
		{Role: sonutruntime.MessageRoleSystem, Content: systemPrompt},
		{Role: sonutruntime.MessageRoleUser, Content: userContent},
	}

	var collectedContext strings.Builder

	for turn := 0; turn < plannerGroundingMaxTurns; turn++ {
		stream, streamErr := provider.StreamCompletion(ctx, sonutruntime.CompletionRequest{
			Messages:        groundingMessages,
			Tools:           defs,
			ReasoningEffort: "none", // grounding is mechanical; use only concise private thinking
		})
		if streamErr != nil {
			log.Printf("agent: planner grounding turn %d stream error: %v", turn, streamErr)
			break
		}
		collected := sonutruntime.CollectStreamWithOptions(ctx, stream, sonutruntime.CollectOptions{
			OnUsage: options.OnUsage,
		})
		if collected.Error != "" {
			log.Printf("agent: planner grounding turn %d collect error: %s", turn, collected.Error)
			break
		}
		groundingMessages = append(groundingMessages, sonutruntime.Message{
			Role:      sonutruntime.MessageRoleAssistant,
			Content:   collected.Text,
			ToolCalls: historySafeToolCalls(collected.ToolCalls),
		})
		if len(collected.ToolCalls) == 0 {
			// No tool call — model gave up or produced text. Use whatever text as context.
			if text := strings.TrimSpace(collected.Text); text != "" {
				collectedContext.WriteString(text)
			}
			break
		}
		// Execute each tool call; finish_grounding exits the loop.
		finished := false
		for _, call := range collected.ToolCalls {
			if registry != nil {
				call.Name = registry.Resolve(call.Name)
			}
			// Handle the synthetic finish_grounding tool.
			if call.Name == finishGroundingTool {
				args := decodePlannerToolArgs(call.Arguments)
				if ctx, ok := args["context"].(string); ok {
					collectedContext.WriteString(ctx)
				}
				groundingMessages = append(groundingMessages, sonutruntime.Message{
					Role:       sonutruntime.MessageRoleTool,
					ToolCallID: call.ID,
					Content:    "ok",
				})
				finished = true
				continue
			}
			// Reject any non-read-only tool silently so the model knows it failed.
			if !plannerGroundingReadOnlyTools[call.Name] {
				groundingMessages = append(groundingMessages, sonutruntime.Message{
					Role:       sonutruntime.MessageRoleTool,
					ToolCallID: call.ID,
					Content:    "Error: only read_file, list_directory, grep, glob, and web_search are allowed in the grounding phase.",
				})
				continue
			}
			// Run the read-only tool.
			if options.OnToolCall != nil {
				options.OnToolCall(call)
			}
			toolArgs := decodePlannerToolArgs(call.Arguments)
			toolCtx, toolCancel := context.WithTimeout(ctx, 20*time.Second)
			result := registry.RunWithOptions(toolCtx, call.Name, toolArgs, tools.RunOptions{
				PermissionGranted: true,
				PermissionMode:    string(options.PermissionMode),
				Autonomy:          options.Autonomy,
				Sandbox:           options.Sandbox,
				ToolCallID:        call.ID,
				SessionID:         options.SessionID,
				Model:             options.Model,
				Cwd:               options.Cwd,
				FileTracker:       options.FileTracker,
			})
			toolCancel()
			if options.OnToolResult != nil {
				options.OnToolResult(ToolResult{
					ToolCallID: call.ID,
					Name:       call.Name,
					Status:     result.Status,
					Output:     result.Output,
					Meta:       result.Meta,
					Display:    result.Display,
				})
			}
			output := result.Output
			// Cap individual tool output so one large file can't blow the budget.
			const perToolCap = 2 * 1024
			if len(output) > perToolCap {
				output = output[:perToolCap] + "\n…[output truncated by grounding phase]"
			}
			groundingMessages = append(groundingMessages, sonutruntime.Message{
				Role:       sonutruntime.MessageRoleTool,
				ToolCallID: call.ID,
				Content:    output,
			})
		}
		if finished {
			break
		}
	}

	ctxStr := strings.TrimSpace(collectedContext.String())
	// Cap total grounding output.
	if len(ctxStr) > plannerGroundingContextBudget {
		cut := plannerGroundingContextBudget
		for cut > 0 && !utf8.RuneStart(ctxStr[cut]) {
			cut--
		}
		ctxStr = ctxStr[:cut] + "\n…[grounding context truncated]"
	}
	observability.PerfLog("grounding_end", "phase", "planner_grounding",
		"ms", time.Since(groundingStarted).Milliseconds(),
		"context_bytes", len(ctxStr))
	return ctxStr
}

// codeLeakPattern detects file contents leaking into a plan step: markup
// preambles, fenced code blocks, or inline tool payloads.
var codeLeakPattern = regexp.MustCompile("<!DOCTYPE|<html|```|\\\\n|write_file\\(\"")

// sanitizePlan strips implementation leakage, drops duplicate step titles
// (renumbered), and caps size. Returns "" if nothing plan-like survives.
func sanitizePlan(plan string) string {
	if len(plan) > plannerMaxPlanBytes {
		plan = plan[:plannerMaxPlanBytes]
	}
	lines := strings.Split(plan, "\n")
	var kept []string
	for _, line := range lines {
		if loc := codeLeakPattern.FindStringIndex(line); loc != nil {
			line = strings.TrimSpace(line[:loc[0]])
			if line != "" {
				kept = append(kept, line)
			}
			// Everything after the first leak is implementation, not plan.
			break
		}
		kept = append(kept, line)
	}
	return dedupePlanSteps(strings.TrimSpace(strings.Join(kept, "\n")))
}

// dedupePlanSteps collapses repeated step titles (common local-model failure:
// emit steps 1–N then emit the same titles again as N+1…). Keeps first
// occurrence's Do/Done body and any [status] annotation, renumbers 1..N, caps
// at plannerMaxSteps. Statuses must survive — validation PASS roadmaps use
// "[completed]" and stripping them made every first step re-open as in_progress.
func dedupePlanSteps(plan string) string {
	if strings.TrimSpace(plan) == "" {
		return plan
	}
	type step struct {
		status string // optional: completed|pending|in_progress|failed
		title  string
		body   []string
	}
	var steps []step
	seen := map[string]struct{}{}
	var cur *step
	flush := func() {
		if cur == nil {
			return
		}
		key := normalizePlanTitle(cur.title)
		if key == "" {
			cur = nil
			return
		}
		if _, dup := seen[key]; dup {
			cur = nil
			return
		}
		seen[key] = struct{}{}
		steps = append(steps, *cur)
		cur = nil
	}
	for _, line := range strings.Split(plan, "\n") {
		trim := strings.TrimSpace(line)
		if isNumberedPlanTitle(trim) {
			flush()
			if len(steps) >= plannerMaxSteps {
				break
			}
			status, title := splitPlanTitleStatus(stripPlanNumberOnly(trim))
			cur = &step{status: status, title: title}
			continue
		}
		if cur == nil {
			continue
		}
		if trim == "" {
			continue
		}
		if isPlanContinuationLine(line, trim) {
			cur.body = append(cur.body, strings.TrimRight(line, " \t"))
		}
	}
	flush()
	if len(steps) == 0 {
		return plan
	}
	var b strings.Builder
	for i, s := range steps {
		if i > 0 {
			b.WriteByte('\n')
		}
		b.WriteString(strconv.Itoa(i + 1))
		b.WriteString(". ")
		if s.status != "" {
			b.WriteByte('[')
			b.WriteString(s.status)
			b.WriteString("] ")
		}
		b.WriteString(s.title)
		for _, body := range s.body {
			b.WriteByte('\n')
			b.WriteString(body)
		}
	}
	return b.String()
}

// stripPlanNumberOnly removes leading "1. " / "1) " but keeps any [status] tag.
func stripPlanNumberOnly(trim string) string {
	i := 0
	for i < len(trim) && trim[i] >= '0' && trim[i] <= '9' {
		i++
	}
	for i < len(trim) && (trim[i] == '.' || trim[i] == ')' || trim[i] == ':' || trim[i] == ' ') {
		i++
	}
	return strings.TrimSpace(trim[i:])
}

// splitPlanTitleStatus peels an optional leading "[status]" from a plan title.
func splitPlanTitleStatus(rest string) (status, title string) {
	rest = strings.TrimSpace(rest)
	if !strings.HasPrefix(rest, "[") {
		return "", rest
	}
	end := strings.Index(rest, "]")
	if end < 0 || end+1 > len(rest) {
		return "", rest
	}
	raw := strings.ToLower(strings.TrimSpace(rest[1:end]))
	title = strings.TrimSpace(rest[end+1:])
	switch raw {
	case "pending", "in_progress", "completed", "failed", "done", "complete", "in progress":
		if raw == "in progress" {
			raw = "in_progress"
		}
		if raw == "done" || raw == "complete" {
			raw = "completed"
		}
		return raw, title
	default:
		// Unknown bracket is part of the title (e.g. "[RFC] Ship API").
		return "", rest
	}
}

// normalizePlanTitle keys steps for dedupe: lowercased, strip status tags and
// trivial punctuation so "Implement Title Screen" == "implement title screen".
func normalizePlanTitle(title string) string {
	title = strings.TrimSpace(title)
	if strings.HasPrefix(title, "[") {
		if end := strings.Index(title, "]"); end >= 0 && end+1 < len(title) {
			title = strings.TrimSpace(title[end+1:])
		}
	}
	title = strings.ToLower(title)
	title = strings.Map(func(r rune) rune {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9', r == ' ', r == '/', r == '.', r == '-', r == '_':
			return r
		default:
			return -1
		}
	}, title)
	return strings.Join(strings.Fields(title), " ")
}

// stripPlanTitleMarker removes leading "1. " / "1) " / optional [status].
func stripPlanTitleMarker(trim string) string {
	i := 0
	for i < len(trim) && trim[i] >= '0' && trim[i] <= '9' {
		i++
	}
	for i < len(trim) && (trim[i] == '.' || trim[i] == ')' || trim[i] == ':' || trim[i] == ' ') {
		i++
	}
	rest := strings.TrimSpace(trim[i:])
	if strings.HasPrefix(rest, "[") {
		if end := strings.Index(rest, "]"); end >= 0 && end+1 < len(rest) {
			rest = strings.TrimSpace(rest[end+1:])
		}
	}
	return rest
}

// isNumberedPlanTitle reports whether a trimmed line looks like "1. …" / "1) …".
func isNumberedPlanTitle(trim string) bool {
	if len(trim) < 2 {
		return false
	}
	if trim[0] < '1' || trim[0] > '9' {
		return false
	}
	// Support multi-digit step numbers ("10. …").
	i := 1
	for i < len(trim) && trim[i] >= '0' && trim[i] <= '9' {
		i++
	}
	if i >= len(trim) {
		return false
	}
	rest := trim[i:]
	return strings.HasPrefix(rest, ". ") || strings.HasPrefix(rest, ") ") ||
		strings.HasPrefix(rest, ": ") || strings.HasPrefix(rest, ".") ||
		strings.HasPrefix(rest, ")")
}

// isPlanContinuationLine keeps indented / bullet Do-Done detail under a title.
// These lines are part of the plan contract for the executor and must not be
// dropped by extractNumberedPlan.
func isPlanContinuationLine(raw, trim string) bool {
	if trim == "" {
		return false
	}
	if len(raw) > 0 && (raw[0] == ' ' || raw[0] == '\t') {
		return true
	}
	lower := strings.ToLower(trim)
	return strings.HasPrefix(trim, "-") || strings.HasPrefix(trim, "*") ||
		strings.HasPrefix(lower, "do:") || strings.HasPrefix(lower, "do ") ||
		strings.HasPrefix(lower, "done") || strings.HasPrefix(lower, "how:") ||
		strings.HasPrefix(lower, "path:") || strings.HasPrefix(lower, "accept")
}

// extractNumberedPlan pulls a "1. …\n2. …" block out of free-form text
// (including reasoning dumps), preserving indented Do/Done detail lines.
// Returns "" when fewer than two numbered titles appear — a single "1." line
// is usually a false positive mid-prose.
func extractNumberedPlan(text string) string {
	lines := strings.Split(text, "\n")
	var numbered []string
	for _, line := range lines {
		trim := strings.TrimSpace(line)
		if isNumberedPlanTitle(trim) {
			numbered = append(numbered, trim)
			continue
		}
		if len(numbered) == 0 {
			continue
		}
		if trim == "" {
			// Blank lines inside a multi-line plan are noise; keep collecting.
			continue
		}
		if isPlanContinuationLine(line, trim) {
			numbered = append(numbered, strings.TrimRight(line, " \t"))
			continue
		}
		// First non-plan prose after titles ends the block.
		break
	}
	titles := 0
	for _, line := range numbered {
		if isNumberedPlanTitle(strings.TrimSpace(line)) {
			titles++
		}
	}
	if titles < 2 {
		return ""
	}
	return strings.Join(numbered, "\n")
}

// resolvePlanText prefers visible content, then falls back to a numbered list
// found inside reasoning (Bonsai often plans inside reasoning_content and
// leaves content empty — without this the plan UI stays blank).
func resolvePlanText(content, reasoning string) string {
	if plan := sanitizePlan(strings.TrimSpace(content)); plan != "" {
		// Prefer content when it already looks like a plan.
		if extractNumberedPlan(plan) != "" || strings.Contains(plan, "1.") {
			return plan
		}
		// Content that is not plan-shaped still wins if reasoning is empty.
		if strings.TrimSpace(reasoning) == "" {
			return plan
		}
	}
	if plan := sanitizePlan(extractNumberedPlan(reasoning)); plan != "" {
		return plan
	}
	if plan := sanitizePlan(extractNumberedPlan(content)); plan != "" {
		return plan
	}
	return sanitizePlan(strings.TrimSpace(content))
}

// planLooksExecutorReady reports whether visible plan text already has enough
// clear-cut steps (titles + Do/Done) that further generation is pure latency.
// Used to cut the planner stream early so planner→executor handoff is fast.
// Also true when the model starts a second copy of the list (step number resets
// or title repeats) — more tokens only bloat the seed.
func planLooksExecutorReady(text string) bool {
	plan := extractNumberedPlan(text)
	if plan == "" {
		return false
	}
	titles, withDo, withDone := 0, 0, 0
	seenTitles := map[string]struct{}{}
	dupTitle := false
	prevNum := 0
	numReset := false
	for _, line := range strings.Split(plan, "\n") {
		trim := strings.TrimSpace(line)
		if isNumberedPlanTitle(trim) {
			titles++
			title := normalizePlanTitle(stripPlanTitleMarker(trim))
			if title != "" {
				if _, ok := seenTitles[title]; ok {
					dupTitle = true
				}
				seenTitles[title] = struct{}{}
			}
			n := planStepNumber(trim)
			if n > 0 && prevNum > 0 && n <= prevNum {
				numReset = true
			}
			if n > 0 {
				prevNum = n
			}
			continue
		}
		lower := strings.ToLower(trim)
		if strings.Contains(lower, "do:") || strings.HasPrefix(lower, "- do ") {
			withDo++
		}
		if strings.Contains(lower, "done when") || strings.HasPrefix(lower, "- done") {
			withDone++
		}
	}
	if dupTitle || numReset {
		return titles >= 2
	}
	if titles >= plannerMaxSteps {
		return true
	}
	// At least 3 clear-cut steps with Do/Done on most of them.
	return titles >= 3 && withDo >= 2 && withDone >= 2
}

// planStepNumber returns the leading integer of a numbered title, or 0.
func planStepNumber(trim string) int {
	i := 0
	for i < len(trim) && trim[i] >= '0' && trim[i] <= '9' {
		i++
	}
	if i == 0 {
		return 0
	}
	n, err := strconv.Atoi(trim[:i])
	if err != nil {
		return 0
	}
	return n
}

// runPlannerTurn performs one tool-less planner pass at high thinking effort
// and returns the plan text (numbered list) verbatim, or "" if the model
// produced nothing usable. It reuses the main conversation's system prompt so
// the planner can leverage the KV cache prefix.
// images, when non-empty, ride on the user turn so the plan can reference
// the screenshot (never claim "no image was attached").
// onReasoning / onPrefillProgress stream the process live so the UI is not
// silent until the final plan lands.
func runPlannerTurn(ctx context.Context, provider Provider, systemPrompt string, prompt string, images []sonutruntime.ImageBlock, onUsage func(Usage), onPrefillProgress func(int, int), onReasoning func(string)) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, plannerMaxDuration)
	defer cancel()
	userMessage := strings.TrimSpace(prompt) + "\n\n" + plannerInstructions
	if len(images) > 0 {
		userMessage += "\n\n[system] IMAGE(S) ARE ATTACHED to this message. First understand the visual reference, then route the request normally. Include the concrete visual facts the builder must preserve (layout, hierarchy, colors, spacing, typography, and interactions) in concise plan steps. Never say no image was attached."
	}
	userMsg := sonutruntime.Message{Role: sonutruntime.MessageRoleUser, Content: userMessage}
	if len(images) > 0 {
		userMsg.Images = sonutruntime.CloneImageBlocks(images)
	}
	request := sonutruntime.CompletionRequest{
		Messages: []sonutruntime.Message{
			{Role: sonutruntime.MessageRoleSystem, Content: systemPrompt},
			userMsg,
		},
		// No tools; medium thinking is enough to decompose a task while
		// avoiding the high-effort planner stalls seen on local models.
		ReasoningEffort: "medium",
		// Planner always thinks. Never set DisableThinking here.
	}
	stream, err := provider.StreamCompletion(ctx, request)
	if err != nil {
		return "", err
	}
	// Cut generation as soon as the plan exceeds the size cap or already looks
	// executor-ready — further tokens only delay the handoff.
	textBytes := 0
	capped := false
	var reasoningBuf strings.Builder
	var textBuf strings.Builder
	collected := sonutruntime.CollectStreamWithOptions(ctx, stream, sonutruntime.CollectOptions{
		OnUsage:              onUsage,
		MaxReasoningBytes:    8 * 1024,
		MaxReasoningDuration: 35 * time.Second,
		OnPing: func(processed, total int) {
			if onPrefillProgress != nil {
				onPrefillProgress(processed, total)
			}
		},
		OnText: func(fragment string) {
			textBuf.WriteString(fragment)
			textBytes += len(fragment)
			if !capped && (textBytes > plannerMaxPlanBytes || planLooksExecutorReady(textBuf.String())) {
				capped = true
				cancel()
			}
		},
		OnReasoning: func(fragment string) {
			reasoningBuf.WriteString(fragment)
			// Do not stream planner reasoning directly into the TUI. Reasoning
			// models often put the numbered roadmap in reasoning_content rather
			// than the visible content field; forwarding it immediately makes the
			// plan look like a Thought block and prevents the surface from creating
			// its real PLAN state. The planner status row already communicates that
			// work is active. If the model produced no usable plan, flush the
			// buffered reasoning below as a diagnostic instead of losing it.
		},
	})
	// Prefer collected.Text (full stream) but fall back to early-cancel buffer.
	visible := collected.Text
	if strings.TrimSpace(visible) == "" {
		visible = textBuf.String()
	}
	plan := resolvePlanText(visible, reasoningBuf.String())
	if plan != "" {
		return plan, nil
	}
	if onReasoning != nil && reasoningBuf.Len() > 0 {
		onReasoning(reasoningBuf.String())
	}
	if collected.Error != "" && !capped {
		return "", errors.New(collected.Error)
	}
	return "", nil
}

// firstPlanStepTitle returns the first numbered step's title line for an
// execute-now directive. Multi-line Do/Done bodies are intentionally omitted
// from this short cue (they remain in the full <plan> block).
func firstPlanStepTitle(plan string) string {
	for _, line := range strings.Split(plan, "\n") {
		trim := strings.TrimSpace(line)
		if !isNumberedPlanTitle(trim) {
			continue
		}
		// Strip leading "1. " / "1) " markers for a cleaner directive.
		i := 0
		for i < len(trim) && trim[i] >= '0' && trim[i] <= '9' {
			i++
		}
		for i < len(trim) && (trim[i] == '.' || trim[i] == ')' || trim[i] == ':' || trim[i] == ' ') {
			i++
		}
		// Optional status annotation: "[in_progress] title"
		rest := strings.TrimSpace(trim[i:])
		if strings.HasPrefix(rest, "[") {
			if end := strings.Index(rest, "]"); end >= 0 && end+1 < len(rest) {
				rest = strings.TrimSpace(rest[end+1:])
			}
		}
		if rest != "" {
			return rest
		}
	}
	return ""
}

// injectPlanSeed folds the plan into the LAST USER message rather than
// appending a synthetic assistant message. A fake assistant turn is hostile to
// small local models — they echo "Before starting, I planned this out..." back
// verbatim as their answer (looping the seed into the transcript) — and some
// providers reject assistant-final message lists. Embedding it in the user turn
// keeps the roadmap visible without impersonating the generator.
//
// Keep the seed SHORT. Long builder_workflow prose is often echoed back as
// content (live thrash: full <plan> + rules dumped into the transcript).
func injectPlanSeed(messages []sonutruntime.Message, plan string) []sonutruntime.Message {
	firstStep := firstPlanStepTitle(plan)
	executeNow := "Start step 1 NOW with a tool call that includes real source code."
	if firstStep != "" {
		executeNow = "EXECUTE NOW: " + firstStep + " — one tool call with real source in arguments."
	}
	for i := len(messages) - 1; i >= 0; i-- {
		if messages[i].Role == sonutruntime.MessageRoleUser {
			// Ultra-short seed (OpenCode-style: act, no preamble). Long seeds are
			// echoed as content by weak local models (live thrash: full plan dump).
			messages[i].Content += "\n\n<plan>\n" + plan + "\n</plan>"
			messages[i].Content += "\n\n<builder_workflow>\n" +
				"Keep private reasoning private. Start with the first concrete tool call using real arguments; do not restate this plan or these rules. Never update_plan. Read different files when needed, but never re-read the same path without a concrete reason. If a path is unclear, inspect with list_directory/glob/grep/read_file before editing. No web tools; the planner already supplied repository context. No identical re-call. Paths relative to Working directory only (never sonut/test/file when already in that folder). Reuse existing files and make the smallest working diff: edit_file/apply_patch for a unique 1-3 line anchor, append_file for true additions, write_file only for new files or an explicit full rewrite. Never copy a whole existing file into edit_file. Never edit_file with identical old_string and new_string. Truncated files: append_file the next chunk only. Large new files: write ~80–120 lines then append ≤100. One path; after write use edit/append only. Keep going until every plan step is done.\n" +
				executeNow + "\n" +
				"</builder_workflow>"
			break
		}
	}
	return messages
}

// injectUnifiedWorkflow gives the model one compact contract for the complete
// task lifecycle. Unlike injectPlanSeed, it does not manufacture a planner
// turn or an executor phase; update_plan and the implementation tools remain
// available in the same conversation.
func injectUnifiedWorkflow(messages []sonutruntime.Message, hasImages bool) []sonutruntime.Message {
	workflow := `<atcode_workflow>
You are one continuous coding agent. Work through the task yourself; never describe or expose internal routing.

For a real task:
1. Privately classify the request, identify the deliverables, define observable done conditions, and gather only the evidence needed from the workspace or attachment.
2. If the work has multiple meaningful steps, call update_plan once with 3–8 concrete steps. Each step must name the real path/tool and its verification. Do not print a plan as prose and do not call update_plan repeatedly for the same roadmap.
3. Immediately execute the first pending step with the narrowest useful tool. Do not output source code instead of writing it.
4. Continue through every pending step. After edits, verify by running the focused check, opening the UI, or reading back the result. Fix concrete failures yourself.
5. Only then give a short final answer stating what changed and what verification passed.

For an empty workspace and a request for a single-file HTML game or other self-contained web page, do not spend the first action listing the empty directory or narrating. Write the first complete HTML shell to the requested path with <!DOCTYPE html>, a canvas or visible root, and an inline script; continue the logic with append_file in bounded chunks. Then navigate to the local file with the browser tools using the exact absolute path reported by the write result (never invent a /home/user path or strip the leading slash), call the browser console-messages/errors tool and inspect its actual entries (not only the error count), take a snapshot or screenshot, press ArrowRight and Space (or the page's documented equivalents), and evaluate a visible or game-state value to confirm the controls changed it. If local navigation reports file-not-found, retry once with the exact workspace path joined to the written relative path before diagnosing the page. A write-only turn is not completion: fix every console/runtime issue, reload, and repeat the console plus control check before marking the work done.

Keep analysis private. Never output “Thinking Process”, prompt analysis, constraints, drafts, self-critique, or a progress essay. Status is conveyed by tool calls and the plan tool. Do not delegate to specialists or spawn sub-agents for ordinary work; use one coherent context.
</atcode_workflow>`
	if hasImages {
		workflow += `
[attachment workflow] Treat the image as visual ground truth. Analyze layout, hierarchy, colors, spacing, typography, and states privately, then implement and verify the result. Do not claim the image is missing.`
	}
	for i := range messages {
		if messages[i].Role == sonutruntime.MessageRoleSystem {
			messages[i].Content += "\n\n" + workflow
			break
		}
	}
	return messages
}

// extractSeededPlan recovers the exact planner roadmap after injectPlanSeed so
// the executor-side progress guard can reconcile concrete writes with the plan
// even before the model sends its first update_plan tool call.
func extractSeededPlan(messages []sonutruntime.Message) string {
	for i := len(messages) - 1; i >= 0; i-- {
		if messages[i].Role != sonutruntime.MessageRoleUser {
			continue
		}
		content := messages[i].Content
		start := strings.LastIndex(content, "<plan>\n")
		if start < 0 {
			continue
		}
		start += len("<plan>\n")
		// The fixed prose heading is not part of the numbered roadmap.
		if lineEnd := strings.IndexByte(content[start:], '\n'); lineEnd >= 0 {
			start += lineEnd + 1
		}
		end := strings.Index(content[start:], "\n</plan>")
		if end < 0 {
			return ""
		}
		return strings.TrimSpace(content[start : start+end])
	}
	return ""
}

// fallbackPlan keeps the builder contract deterministic when the separate
// planner stream is unavailable, malformed, or canceled. The UI still shows a
// real plan and the generator still receives an actionable roadmap; no second
// planning retry is allowed to introduce an idle loop.
func fallbackPlan(prompt string, hasImages bool) string {
	_ = prompt // Keep the fallback stable so it is cache-friendly and concise.
	base := "1. Implement the primary deliverable with write_file or edit_file\n" +
		"   - Do: write_file/edit_file/append_file with real source code; reuse existing files; no empty bodies\n" +
		"   - Done when: the primary deliverable file is written to disk\n" +
		"2. Complete logic, styling, and supporting features\n" +
		"   - Do: append_file/edit_file with real implementation code to fulfill all remaining requirements\n" +
		"   - Done when: the primary deliverable and logic match the request\n" +
		"3. Verify the result with a focused command or browser check\n" +
		"   - Do: run a focused check or open the UI in the headful browser when applicable\n" +
		"   - Done when: acceptance criteria for the request are met"
	if hasImages {
		base = "1. Capture visual requirements from the analyzed image into concrete layout/structure notes\n" +
			"   - Do: preserve hierarchy, colors, spacing, typography, and interactive states from the image plan\n" +
			"   - Done when: the first implementation step has explicit visual targets\n" +
			base
	}
	return base
}

// detachAnalyzedImages transfers visual understanding from the one image-aware
// planner pass into the text plan. Keeping the raw image on the builder's first
// tool request makes llama.cpp re-encode the projector and causes the 60–90s
// follow-up stalls seen in perf.log. The planner has already consumed it, so
// every later role gets the compact plan instead of paying that cost again.
func detachAnalyzedImages(messages []sonutruntime.Message) []sonutruntime.Message {
	for i := range messages {
		if len(messages[i].Images) == 0 {
			continue
		}
		messages[i].Images = nil
		if messages[i].Role == sonutruntime.MessageRoleUser &&
			!strings.Contains(messages[i].Content, "image was analyzed in the planning phase") {
			messages[i].Content += "\n\n[system] The attached image was analyzed in the planning phase. Use the concrete visual facts in <plan> as the visual source of truth; do not claim the image was missing and do not request it again."
		}
	}
	return messages
}

// maybeRunPlanner runs the bounded planner pass described above. It is a
// no-op when evaluation is off, quick mode is selected, or a legacy lower-level
// caller submits a trivial continuation/question. Explicit builder mode never
// skips planning; a planner failure falls back to a deterministic roadmap so
// the run remains visible and actionable without retrying the planner.
// The bool return reports whether a plan was actually produced and seeded.
// Execution turns switch to concise private thinking (fast execution) when
// this is true: once the planner has assigned concrete steps, the executor and
// evaluator do not need to re-deliberate deeply. When no plan was seeded
// (trivial requests, plain questions, planner disabled or failed), the run
// keeps its resolved thinking effort so answers and open-ended work still get
// real reasoning.
func maybeRunPlanner(ctx context.Context, provider Provider, prompt string, messages []sonutruntime.Message, options Options) ([]sonutruntime.Message, bool) {
	// Gate (and plan) on the raw user request when the surface provided it —
	// the run prompt may be wrapped in session context that defeats the
	// triviality check.
	plannerInput := strings.TrimSpace(options.PlannerPrompt)
	if plannerInput == "" {
		plannerInput = prompt
	}
	if !options.Evaluate || options.ExecutorOnly || options.RunMode == RunModeQuick {
		return messages, false
	}
	// A continuation is an existing builder run being resumed, not a new build
	// request. Keep its current plan/context and go straight back to execution.
	if classifyTurnShape(plannerInput) == TurnShapeContinuation {
		return messages, false
	}
	// Empty mode retains the lower-level API's cheap continuation/question
	// planner gate. Explicit builder mode is the interactive contract: every
	// actual build gets exactly one planning phase, including short asks.
	if options.RunMode != RunModeBuilder && (isTrivialPlannerRequest(plannerInput) || len(strings.Fields(plannerInput)) <= 4) {
		return messages, false
	}
	if options.OnPlanStart != nil {
		options.OnPlanStart()
	}
	plannerStarted := time.Now()
	plannerStatus := "ok"
	observability.PerfLog("planner_start", "phase", "initial", "has_images", len(options.Images) > 0, "task_chars", len(plannerInput))
	plannerRequest := plannerInput
	// Hosted planners receive the compact workspace snapshot and guideline
	// excerpt. Local hybrid planners ground existing work through the read-only
	// pass below; repeating the same files here would undo the micro-prompt win.
	if !isLocalLlamaProviderName(options.ProviderName) {
		if seed := workspaceSeedContextPinned(options.Cwd); seed != "" {
			plannerRequest += "\n\n[workspace snapshot — existing files are authoritative]\n" + seed
		}
		if guide := projectGuidelinesSnippet(options.Cwd); guide != "" {
			plannerRequest += "\n\n[project guidelines]\n" + guide
		}
	}
	if len(options.Images) > 0 {
		plannerRequest += "\n\n[attachments] " + strconv.Itoa(len(options.Images)) +
			" image(s) attached to this planning turn — treat them as ground truth for visual requirements."
	}
	// Use a compact visual-intake prompt for image requests. The builder still
	// receives the full system prompt after this pass, while the vision projector
	// avoids paying for unrelated tool/workspace instructions up front.
	mainSystemPrompt := buildSystemPrompt(options)
	if len(options.Images) > 0 {
		mainSystemPrompt = visualPlannerSystemPrompt
	}
	// Stream planner reasoning live (OnReasoning) so the TUI shows process, not
	// only the final plan. Text is collected inside runPlannerTurn; the final
	// numbered plan is delivered via OnPlan below.
	// Grounding pass: read key files before the text-only planning turn so the
	// plan steps carry exact values (hex colors, function names, file structure)
	// instead of shallow directives that force the executor to re-read everything.
	// A genuinely empty workspace has no repository facts to ground. Running the
	// two-turn grounding exchange there only burns a full local prefill before
	// the planner can start; keep grounding for existing projects and visual
	// requests where the workspace is the source of truth. An image by itself
	// is not a reason to ground: on a new/empty generation directory there are
	// no files to inspect, and the old condition still paid for a full bounded
	// grounding exchange before the visual planner. That was the measured
	// planner→executor idle gap on local Bonsai runs (60s timeout + a wasted
	// no-output retry). The image itself is supplied to the planner below.
	hasWorkspaceFacts := workspaceHasVisibleEntries(options.Cwd)
	// Evidence is valuable for maintenance, debugging, and named existing
	// files. New builds already have the compact workspace map and should reach
	// the planner in one model request, especially when an image is attached.
	shouldGround := hasWorkspaceFacts && plannerNeedsGrounding(plannerInput)
	if shouldGround {
		if groundingCtx := runPlannerGroundingTurn(ctx, provider, mainSystemPrompt, plannerRequest, options.Registry, options); groundingCtx != "" {
			observability.PerfLog("grounding_applied", "context_bytes", len(groundingCtx))
			plannerRequest += "\n\n[grounding context — facts gathered from workspace before planning]\n" + groundingCtx
		}
	} else if len(options.Images) > 0 && !hasWorkspaceFacts {
		observability.PerfLog("grounding_skipped", "reason", "empty_workspace_visual_intake")
	}
	plan, err := runPlannerTurn(ctx, provider, mainSystemPrompt, plannerRequest, options.Images, options.OnUsage, options.OnPrefillProgress, options.OnReasoning)
	if err != nil {
		plannerStatus = "fallback"
		log.Printf("agent: planner turn failed, using deterministic builder fallback: %v", err)
		plan = fallbackPlan(plannerInput, len(options.Images) > 0)
	}
	if plan == "" {
		plan = fallbackPlan(plannerInput, len(options.Images) > 0)
	} else if !planLooksExecutorReady(plan) {
		// A local model can stop after a title or half of a Do line. Never seed
		// the executor with that partial roadmap: it produces bad sidebar state
		// and forces the executor to rediscover the missing work. Keep this a
		// single-pass handoff; replace malformed output locally instead of paying
		// for a second planner generation.
		log.Printf("agent: planner returned an incomplete roadmap; using fallback")
		plan = fallbackPlan(plannerInput, len(options.Images) > 0)
	}
	if options.OnPlan != nil {
		options.OnPlan(plan)
	}
	observability.PerfLog("planner_end", "phase", "initial", "status", plannerStatus, "ms", time.Since(plannerStarted).Milliseconds(), "plan_bytes", len(plan), "err", err != nil)
	messages = injectPlanSeed(messages, plan)
	if len(options.Images) > 0 {
		messages = detachAnalyzedImages(messages)
	}
	return messages, true
}

// plannerNeedsGrounding keeps the routing decision deterministic. It avoids a
// model round-trip just to decide whether another model round-trip is needed.
func plannerNeedsGrounding(prompt string) bool {
	text := strings.ToLower(strings.TrimSpace(prompt))
	if text == "" {
		return false
	}
	for _, marker := range []string{
		"existing", "current code", "current implementation", "debug", "bug", "failing", "regression",
		"refactor", "preserve", "call site", "tests", "test failure", "stack trace", "log",
	} {
		if strings.Contains(text, marker) {
			return true
		}
	}
	for _, field := range strings.Fields(text) {
		field = strings.Trim(field, "`'\"(),:;[]")
		if strings.ContainsAny(field, "/\\") || strings.Contains(field, ".go") ||
			strings.Contains(field, ".ts") || strings.Contains(field, ".tsx") ||
			strings.Contains(field, ".js") || strings.Contains(field, ".jsx") ||
			strings.Contains(field, ".html") || strings.Contains(field, ".css") ||
			strings.Contains(field, ".json") || strings.Contains(field, ".yaml") ||
			strings.Contains(field, ".yml") {
			return true
		}
	}
	return false
}

func workspaceHasVisibleEntries(cwd string) bool {
	entries, err := os.ReadDir(strings.TrimSpace(cwd))
	if err != nil {
		return false
	}
	for _, entry := range entries {
		if !strings.HasPrefix(entry.Name(), ".") {
			return true
		}
	}
	return false
}
