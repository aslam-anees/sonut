package tui

import (
	"encoding/json"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"strconv"
	"strings"

	"charm.land/lipgloss/v2"

	"github.com/aistatine/sonut/internal/agent"
)

const (
	suggestionPaletteMaxVisible = 7
	suggestionPaletteMaxWidth   = 76
	suggestionPaletteMinWidth   = 44
	pickerOverlayMaxVisible     = 10
	pickerOverlayMaxWidth       = 92
	pickerOverlayMinWidth       = 56
	modelPickerOverlayMaxWidth  = 76
	modelPickerOverlayMinWidth  = 58
)

// layoutTier buckets the terminal width into the spec's adaptive tiers. It
// is derived from the live width at every render, so a WindowSizeMsg
// re-evaluates it implicitly.
type layoutTier int

const (
	tierTiny   layoutTier = iota // < 58: single-segment header, rail-less cards
	tierNarrow                   // 58–79: no gutters, bare badge, lean status
	tierMedium                   // 80–99: no tool-arg column, no ctx
	tierFull                     // ≥ 100: everything
)

func widthTier(width int) layoutTier {
	switch {
	case width >= 100:
		return tierFull
	case width >= 80:
		return tierMedium
	case width >= minStartupWidth:
		return tierNarrow
	default:
		return tierTiny
	}
}

// titleBar renders the top zone of the chat surface: git branch and cwd on the
// left, provider/model and context window on the right, then a rule. Segments
// drop with the width tier (full → no ctx → no cwd → branch/path only), reusing
// the startupHeaderLine candidate fallback.
func (m model) titleBar(width int) string {
	tier := widthTier(width)

	workspace := m.titleWorkspaceSegment()
	workspaceShort := m.titleWorkspaceSegmentShort()
	branchOnly := m.titleBranchSegment()
	cwdOnly := sonutTheme.faint.Render(shortenPath(m.cwd))
	compactLeft := cwdOnly
	if branchOnly != "" {
		compactLeft = branchOnly
	}
	model := m.titleModelSegment()
	ctx := ""
	if window := m.modelContextWindow(m.modelName); window > 0 {
		ctx = sonutTheme.faint.Render(" · " + formatContextWindow(window))
	}

	var candidates []headerCandidate
	switch tier {
	case tierFull:
		candidates = []headerCandidate{
			{left: workspace, right: model + ctx},
			{left: workspace, right: model},
			{left: workspaceShort, right: model},
			{left: cwdOnly, right: model},
			{left: compactLeft, right: model},
		}
	case tierMedium:
		candidates = []headerCandidate{
			{left: workspace, right: model},
			{left: workspaceShort, right: model},
			{left: cwdOnly, right: model},
			{left: compactLeft, right: model},
		}
	case tierNarrow:
		candidates = []headerCandidate{
			{left: branchOnly, right: model},
			{left: "", right: model},
		}
	default:
		// Tiny: one segment, no right column.
		candidates = []headerCandidate{
			{left: compactLeft, right: ""},
			{left: cwdOnly, right: ""},
		}
	}

	line := titleHeaderLine(width, candidates)
	rule := sonutTheme.line.Render(strings.Repeat("─", width))
	return line + "\n" + rule
}

// titleHeaderLine anchors metadata at both edges and turns the otherwise empty
// gap into a quiet diagonal field. The field only uses the active theme's border
// token, so it stays subordinate to the workspace and model labels.
func titleHeaderLine(width int, candidates []headerCandidate) string {
	for _, candidate := range candidates {
		leftWidth := lipgloss.Width(candidate.left)
		rightWidth := lipgloss.Width(candidate.right)
		plain := joinHeaderLine(candidate.left, candidate.right, width)
		if lipgloss.Width(plain) > width {
			continue
		}
		gap := width - leftWidth - rightWidth
		if candidate.left == "" || candidate.right == "" || gap < 5 {
			return plain
		}
		field := sonutTheme.line.Render(strings.Repeat("╱", gap-2))
		return candidate.left + " " + field + " " + candidate.right
	}
	if len(candidates) == 0 {
		return ""
	}
	last := candidates[len(candidates)-1]
	return fitStyledLine(joinHeaderLine(last.left, last.right, width), width)
}

func (m model) titleWorkspaceSegment() string {
	cwd := sonutTheme.faint.Render(shortenPath(m.cwd))
	parts := []string{}
	if branch := m.titleBranchSegment(); branch != "" {
		parts = append(parts, branch)
	}
	if pr := m.titlePRSegment(); pr != "" {
		parts = append(parts, pr)
	}
	if len(parts) > 0 {
		return strings.Join(append(parts, cwd), "  ")
	}
	return cwd
}

func (m model) titleWorkspaceSegmentShort() string {
	cwd := sonutTheme.faint.Render(shortenPath(m.cwd))
	parts := []string{}
	branch := strings.TrimSpace(m.gitBranch)
	if branch != "" {
		icon := sonutTheme.muted.Render("")
		parts = append(parts, icon+" "+sonutTheme.muted.Render(middleTruncate(branch, 22)))
	}
	if pr := m.titlePRSegment(); pr != "" {
		parts = append(parts, pr)
	}
	if len(parts) > 0 {
		return strings.Join(append(parts, cwd), "  ")
	}
	return cwd
}

func (m model) titleBranchSegment() string {
	branch := strings.TrimSpace(m.gitBranch)
	if branch == "" {
		return ""
	}
	return sonutTheme.muted.Render("") + " " + sonutTheme.muted.Render(branch)
}

func (m model) titlePRSegment() string {
	return renderPRSegments(BuildPRSegments(m.prState, false))
}

func (m model) titleModelSegment() string {
	provider := strings.TrimSpace(m.providerDisplayName())
	model := strings.TrimSpace(m.modelName)
	switch {
	case provider == "" && model == "":
		return ""
	case model == "":
		return sonutTheme.ink.Render(provider)
	case provider == "":
		return sonutTheme.ink.Render(model)
	default:
		return sonutTheme.ink.Render(provider + "/" + model)
	}
}

func (m model) composerDividerLine(width int) string {
	// Show effort level (min·mid·max) in the divider so the user always knows
	// thinking depth at a glance. Falls back to model name if effort is not set.
	meta := m.effortDividerLabel()
	metaWidth := lipgloss.Width(meta)
	if width < 8 {
		return sonutTheme.lineStrong.Render(strings.Repeat("─", width))
	}
	if width < metaWidth+4 {
		return sonutTheme.lineStrong.Render("╰" + strings.Repeat("─", width-2) + "╯")
	}
	rule := strings.Repeat("─", width-metaWidth-4)
	return sonutTheme.lineStrong.Render("╰"+rule+" ") + meta + sonutTheme.lineStrong.Render(" ╯")
}

// statusLine renders the bottom one-liner. During a run it becomes the live
// working indicator: `✦ <verb> · <action> · <effort> · <elapsed> · ↑<tokens>`.
// At rest it shows the permission mode + effort + context gauge + usage.
// Both views share exactly ONE line so the footer height never shifts.
func (m model) statusLine(width int) string {
	tier := widthTier(width)
	separator := sonutTheme.line.Render(" · ")
	prefix := "  "

	// Confirm prompts override everything.
	if m.exitConfirmActive {
		return fitStyledLine(prefix+sonutTheme.amber.Render("●")+" "+sonutTheme.amber.Render(ctrlCExitConfirmText), width)
	}
	if m.cancelConfirmActive {
		return fitStyledLine(prefix+sonutTheme.amber.Render("●")+" "+sonutTheme.amber.Render(escCancelConfirmText), width)
	}

	// While a run is active: unified working line with spinner + action + effort + elapsed + tokens.
	if m.activeRunID != 0 && tier >= tierNarrow {
		label := workingLabel(m.spinnerPhase)
		verb := activityLabelView(label, m.spinnerPhase, m.reducedMotion)
		line := prefix + sonutTheme.accent.Render(m.spinnerGlyph()) + " " + verb
		// Keep the live network switch visible while a turn is running too. The
		// label says "web" because this is the session's web/network egress
		// switch controlled by /network.
		line += separator + m.webStatusSegment()

		// Current action: live editing path or active tool name, then effort.
		if action := m.statusLineAction(); action != "" {
			line += separator + sonutTheme.muted.Render(action)
		}
		if m.reasoningEffort != "" {
			line += separator + sonutTheme.accent.Render(effortDisplayName(string(m.reasoningEffort)))
		}
		if !m.turnStartedAt.IsZero() {
			line += separator + sonutTheme.faint.Render(formatWorkingElapsed(m.now().Sub(m.turnStartedAt)))
		}
		line += separator + sonutTheme.faint.Render("↑"+m.workingTokenIndicator())
		if hint := m.quietGenerationHint(); hint != "" && !m.sidebarActive() {
			line += separator + sonutTheme.amber.Render(hint)
		}
		runTokens := m.sessionEcoTokens()
		// Add current-turn streaming estimate (server won't report until turn ends).
		runTokens += int64(m.turnStreamedRunes / 4)
		line += separator + sonutTheme.faint.Render(envSavingsStatusSegment(runTokens))
		return fitStyledLine(line, width)
	}

	// Idle: mode chip + vim indicator + effort + context gauge + usage.
	modeText, modeStyle := m.modeLabel()
	left := prefix + sonutTheme.accent.Render("✦") + " " + modeStyle.Render(modeText)
	left += separator + m.webStatusSegment()
	if tier == tierTiny {
		return fitStyledLine(left, width)
	}

	// Vim mode indicator ("-- NORMAL --" / "-- INSERT --") next to the mode
	// chip so the composer's editing mode is always visible while vim is on.
	if vim := m.vimStatusIndicator(); vim != "" {
		left += separator + sonutTheme.amber.Render(vim)
	}

	if m.reasoningEffort != "" {
		left += separator + sonutTheme.accent.Render(effortDisplayName(string(m.reasoningEffort)))
	}
	if summary := m.backgroundTerminalSummary(); summary != "" {
		left += sonutTheme.line.Render(" │ ") + sonutTheme.muted.Render(summary)
	}

	rightGroups := []string{}
	gaugeShown := false
	if tier >= tierNarrow && !m.sidebarActive() {
		if gauge := m.contextWindowSegment(); gauge != "" {
			rightGroups = append(rightGroups, gauge)
			gaugeShown = true
		}
	}
	usage := m.usageStatusSegment()
	if m.sidebarActive() || gaugeShown {
		usage = m.usageCostSegment()
	}
	if usage != "" {
		rightGroups = append(rightGroups, sonutTheme.muted.Render(usage))
	}

	// Environmental counter — always visible, updates with every token change.
	{
		eco := envSavingsStatusSegment(m.sessionEcoTokens())
		rightGroups = append(rightGroups, sonutTheme.faint.Render(eco))
	}

	right := strings.Join(rightGroups, sonutTheme.line.Render(" │ "))

	line := fitStyledLine(joinHeaderLine(left, right, width), width)
	// Some terminals/lipgloss width profiles measure emoji differently when a
	// line is truncated than when it is measured as a whole. Refit once against
	// the actual rendered width so the footer never spills by one cell.
	if lipgloss.Width(line) > width {
		line = fitStyledLine(line, width-1)
	}
	return line
}

// webStatusSegment mirrors the /network session toggle in the footer. It uses
// "web" as the compact user-facing label while keeping the command's network
// meaning explicit in the status text.
func (m model) webStatusSegment() string {
	if m.networkEnabled {
		return sonutTheme.green.Render("web: on")
	}
	return sonutTheme.red.Render("web: off")
}

// statusLineAction returns the current action segment for the working status line:
// the live editing path (file being written/edited), the current streaming tool name,
// or empty when nothing specific is happening.
func (m model) statusLineAction() string {
	// Prefill and attachment preprocessing happen before a tool path exists. It
	// must win over the normal editing/tool labels or the footer reads as idle
	// during the most expensive part of a local-model turn.
	if m.prefillStatus != "" {
		return prefillActivityLabel(m.prefillStatus)
	}
	if live := m.liveEditingPath(); live != "" {
		return "editing " + live
	}
	if m.streamCallName != "" {
		return m.streamCallName
	}
	return m.workingActivity()
}

func (m model) providerDisplayName() string {
	provider := strings.TrimSpace(m.providerName)
	if provider == "" {
		provider = strings.TrimSpace(m.providerProfile.Name)
	}
	if !providerDisplayNameIsGenericCustom(provider) {
		return provider
	}
	baseURL := strings.TrimSpace(m.providerProfile.BaseURL)
	if baseURL == "" || strings.Contains(strings.ToLower(baseURL), "example.invalid") {
		return provider
	}
	derived := providerWizardNameFromBaseURL(baseURL)
	if derived == "" || derived == "custom" {
		return provider
	}
	return derived
}

func providerDisplayNameIsGenericCustom(name string) bool {
	switch strings.ToLower(strings.TrimSpace(name)) {
	case "custom-openai-compatible", "custom-anthropic-compatible":
		return true
	default:
		return false
	}
}

// nextPermissionMode cycles through three modes: Ask → Auto → Bypass → Ask.
// Bypass (full privilege, no prompts) is intentionally reachable via the cycle
// so power users can enable it without restarting.
func nextPermissionMode(mode agent.PermissionMode) agent.PermissionMode {
	switch mode {
	case agent.PermissionModeAsk:
		return agent.PermissionModeAuto
	case agent.PermissionModeAuto:
		return agent.PermissionModeBypass
	case agent.PermissionModeBypass:
		return agent.PermissionModeAsk
	default:
		// Unsafe or anything else folds to Ask for safety.
		return agent.PermissionModeAsk
	}
}

func (m model) modeLabel() (string, lipgloss.Style) {
	switch m.permissionMode {
	case agent.PermissionModeAuto:
		return "auto-approve", sonutTheme.modeAuto
	case agent.PermissionModeAsk:
		return "ask", sonutTheme.modeAsk
	case agent.PermissionModeUnsafe:
		return "auto-approve", sonutTheme.modeAuto
	case agent.PermissionModeBypass:
		return "bypass", sonutTheme.modeUnsafe
	default:
		mode := strings.TrimSpace(string(m.permissionMode))
		if mode == "" {
			return "auto-approve", sonutTheme.modeAuto
		}
		return mode, sonutTheme.muted
	}
}

// usageStatusSegment shows the latest provider step's token footprint, plus
// cumulative cost once anything is priced.
func (m model) usageStatusSegment() string {
	if m.usageTracker == nil {
		return ""
	}
	summary := m.usageTracker.Summary()
	tokens := m.latestUsageTokens(summary)
	if tokens <= 0 {
		return ""
	}
	if summary.RecordCount == 0 {
		return humanCount(tokens) + " tok"
	}
	return fmt.Sprintf("%s tok · %s",
		humanCount(tokens),
		summary.FormattedTotalCost,
	)
}

// usageCostSegment returns just the session cost, with the token figure dropped.
// Used in the status line when the sidebar is open and already showing tokens at
// its floor, so the cost survives without duplicating the token count. Empty
// until a priced usage record lands (no cost to show yet).
func (m model) usageCostSegment() string {
	if m.usageTracker == nil {
		return ""
	}
	summary := m.usageTracker.Summary()
	if summary.RecordCount == 0 {
		return ""
	}
	return strings.TrimSpace(summary.FormattedTotalCost)
}

// contextFillPercent returns the latest request's context-window fill as a percent
// (0-100), the tokens used, the model's window, and a colour graded for the fill
// (green <75% → amber ≥75% → red ≥90%). ok is false until a usage event lands or
// when the model's window is unknown. Shared by the status-line gauge and the
// sidebar context chip so they grade identically. This is the "you're at X% of
// context" reading the compaction trigger already reasons about at ~80%.
func (m model) contextFillPercent() (pct, used, window int, style lipgloss.Style, ok bool) {
	if m.usageTracker == nil {
		return 0, 0, 0, lipgloss.Style{}, false
	}
	summary := m.usageTracker.Summary()
	used = m.latestUsageTokens(summary)
	window = m.modelContextWindow(m.modelName)
	if used <= 0 || window <= 0 {
		return 0, 0, 0, lipgloss.Style{}, false
	}
	ratio := float64(used) / float64(window)
	if ratio > 1 {
		ratio = 1
	}
	style = sonutTheme.green
	switch {
	case ratio >= 0.90:
		style = sonutTheme.red
	case ratio >= 0.70:
		style = sonutTheme.amber
	}
	return int(ratio*100 + 0.5), used, window, style, true
}

// contextWindowSegment renders the status-line context-fill gauge as
// "◔ used/window · NN%", graded by contextFillPercent.
func (m model) contextWindowSegment() string {
	pct, used, window, style, ok := m.contextFillPercent()
	if !ok {
		return ""
	}
	return style.Render(fmt.Sprintf("◔ %s/%s · %d%%", humanCount(used), humanCount(window), pct))
}

// humanCount renders a token count the way the status line wants it: 999,
// 12.4K, 200K.
func humanCount(n int) string {
	if n < 0 {
		n = 0
	}
	if n < 1000 {
		return strconv.Itoa(n)
	}
	value := float64(n) / 1000
	text := fmt.Sprintf("%.1fK", value)
	return strings.Replace(text, ".0K", "K", 1)
}

// formatContextWindow renders a model's context window for the title bar
// (200000 → 200K, 1048576 → 1M).
func formatContextWindow(window int) string {
	if window <= 0 {
		return ""
	}
	if window >= 1_000_000 && window%1_000_000 < 100_000 {
		return strconv.Itoa(window/1_000_000) + "M"
	}
	return strconv.Itoa(window/1000) + "K"
}

func shortenPath(path string) string {
	path = strings.TrimSpace(path)
	if path == "" {
		return "unknown"
	}
	if home, err := os.UserHomeDir(); err == nil && home != "" {
		// Match on a path boundary: a bare prefix check would mangle siblings
		// like /Users/alice2 when home is /Users/alice.
		if path == home {
			return "~"
		}
		if strings.HasPrefix(path, home+string(os.PathSeparator)) {
			return "~" + path[len(home):]
		}
	}
	return path
}

// gitBranch reads the current branch (or short SHA when detached) for cwd, handling
// both regular checkouts (.git dir) and worktrees (.git file). Returns "" on any
// problem — the header simply omits the segment.
func gitBranch(cwd string) string {
	if strings.TrimSpace(cwd) == "" {
		return ""
	}
	gitPath := filepath.Join(cwd, ".git")
	info, err := os.Stat(gitPath)
	if err != nil {
		return ""
	}

	headPath := filepath.Join(gitPath, "HEAD")
	if !info.IsDir() {
		data, err := os.ReadFile(gitPath)
		if err != nil {
			return ""
		}
		dir := strings.TrimPrefix(strings.TrimSpace(string(data)), "gitdir: ")
		if dir == "" {
			return ""
		}
		headPath = filepath.Join(dir, "HEAD")
	}

	data, err := os.ReadFile(headPath)
	if err != nil {
		return ""
	}
	ref := strings.TrimSpace(string(data))
	if strings.HasPrefix(ref, "ref: ") {
		ref = strings.TrimPrefix(ref, "ref: ")
		return strings.TrimPrefix(ref, "refs/heads/")
	}
	if len(ref) >= 7 {
		return ref[:7]
	}
	return ref
}

// suggestionOverlay renders the slash-command/file autocomplete palette below
// the composer: compact, bordered, and capped so short prefixes cannot flood
// the chat surface. Returns "" when no overlay should show.
func (m model) suggestionOverlay(width int) string {
	if !m.suggestionsActive() {
		return ""
	}
	title := "Commands"
	query := commandSuggestionQuery(m.input.Value())
	footer := "↑/↓ move   Enter run   Esc close"
	if m.selectedCommandSuggestionRequiresInput() {
		footer = "↑/↓ move   Enter insert   Esc close"
	}
	if m.suggestionsAreFiles {
		title = "Files"
		query = fileSuggestionQuery(m.input.Value())
		footer = "↑/↓ move   Enter insert   Esc close"
	}
	return centerRenderedBlock(renderSuggestionPalette(selectableItems(m.suggestions, m.suggestionsAreFiles), m.suggestionIdx, width, title, query, footer), width)
}

func commandSuggestionQuery(value string) string {
	trimmed := strings.TrimLeft(value, " ")
	if trimmed == "" {
		return ""
	}
	if fields := strings.Fields(trimmed); len(fields) > 0 {
		return strings.TrimPrefix(fields[0], "/")
	}
	return strings.TrimPrefix(strings.TrimSpace(trimmed), "/")
}

func fileSuggestionQuery(value string) string {
	if token, ok := trailingAtToken(value); ok {
		return token
	}
	return ""
}

func renderSuggestionPalette(items []selectableListItem, selected, width int, title, query, footer string) string {
	if width <= 0 {
		width = defaultStartupWidth
	}
	paletteWidth := minInt(width, suggestionPaletteMaxWidth)
	if paletteWidth < suggestionPaletteMinWidth {
		paletteWidth = width
	}
	innerWidth := maxInt(1, paletteWidth-4)
	maxVisible := minInt(suggestionPaletteMaxVisible, len(items))
	visible := []selectableListItem{}
	start := 0
	if len(items) > 0 {
		selected = clampInt(selected, 0, len(items)-1)
		start = selectableListStart(len(items), maxVisible, selected)
		visible = items[start : start+maxVisible]
	}

	labelWidth := 0
	for _, item := range visible {
		if w := lipgloss.Width(item.Label); w > labelWidth {
			labelWidth = w
		}
	}
	labelWidth = minInt(labelWidth, maxInt(8, innerWidth/2))

	lines := make([]string, 0, len(visible)+5)
	searchInset := lipgloss.Width("❯ ")
	searchPrefix := transparentSurface(sonutTheme.ink).Render(strings.Repeat(" ", searchInset))
	lines = append(lines, fillPaletteLine(searchPrefix+renderSuggestionSearchLine(query, maxInt(1, innerWidth-searchInset)), innerWidth, transparentSurface))
	lines = append(lines, sonutTheme.line.Render(strings.Repeat("─", innerWidth)))

	for index, item := range visible {
		absoluteIndex := start + index
		surface := transparentSurface
		marker := surface(sonutTheme.faintest).Render("  ")
		if absoluteIndex == selected {
			surface = sonutTheme.onSel
			marker = surface(sonutTheme.accent).Render("❯ ")
		}

		labelText := truncateRunes(item.Label, labelWidth)
		label := surface(sonutTheme.ink).Render(labelText)
		pad := surface(sonutTheme.ink).Render(strings.Repeat(" ", maxInt(0, labelWidth-lipgloss.Width(labelText))))
		line := marker + label + pad
		if desc := strings.TrimSpace(item.Description); desc != "" {
			descWidth := innerWidth - lipgloss.Width(marker) - labelWidth - 2
			if truncated := truncateRunes(desc, maxInt(0, descWidth)); truncated != "" {
				line += surface(sonutTheme.faint).Render("  " + truncated)
			}
		}
		lines = append(lines, fillPaletteLine(line, innerWidth, surface))
	}
	if len(visible) == 0 {
		message := "no matching commands"
		if strings.EqualFold(strings.TrimSpace(title), "Files") {
			message = "no matching files"
		}
		lines = append(lines, fillPaletteLine(searchPrefix+sonutTheme.faint.Render(message), innerWidth, transparentSurface))
	}

	if footer = strings.TrimSpace(footer); footer != "" {
		lines = append(lines, sonutTheme.line.Render(strings.Repeat("─", innerWidth)))
		line := sonutTheme.faint.Render(footer)
		lines = append(lines, fillPaletteLine(line, innerWidth, transparentSurface))
	}
	return styledBlockFillTitle(paletteWidth, strings.TrimSpace(title), lines, sonutTheme.lineStrong, lipgloss.NewStyle())
}

func styledBlockFillTitle(width int, title string, lines []string, borderStyle lipgloss.Style, fill lipgloss.Style) string {
	return styledBlockFillTitleStyled(width, title, lines, borderStyle, fill, sonutTheme.ink.Bold(true))
}

// styledBlockFillTitleStyled is styledBlockFillTitle with a caller-supplied style
// for the inset title text, so a card can pick a calmer/status-tinted title
// without changing the default bright-bold heading every other card uses.
func styledBlockFillTitleStyled(width int, title string, lines []string, borderStyle lipgloss.Style, fill lipgloss.Style, titleStyle lipgloss.Style) string {
	if width < 4 {
		width = 4
	}
	if title = strings.TrimSpace(title); title == "" || widthTier(width) == tierTiny {
		return styledBlockFill(width, lines, borderStyle, fill)
	}
	ruleWidth := width - 2
	titleText := " " + title + " "
	titleWidth := lipgloss.Width(titleText)
	if titleWidth >= ruleWidth {
		return styledBlockFill(width, lines, borderStyle, fill)
	}

	leftRule := "──"
	rightRule := strings.Repeat("─", maxInt(0, ruleWidth-lipgloss.Width(leftRule)-titleWidth))
	top := borderStyle.Render("╭"+leftRule) + titleStyle.Render(titleText) + borderStyle.Render(rightRule+"╮")
	bottom := borderStyle.Render("╰" + strings.Repeat("─", width-2) + "╯")

	body := make([]string, 0, len(lines)+2)
	body = append(body, top)
	for _, line := range lines {
		available := width - 4
		fitted := fitStyledLine(line, available)
		pad := fill.Render(strings.Repeat(" ", maxInt(0, available-lipgloss.Width(fitted))))
		body = append(body, borderStyle.Render("│ ")+fitted+pad+borderStyle.Render(" │"))
	}
	body = append(body, bottom)
	return strings.Join(body, "\n")
}

func renderSuggestionSearchLine(query string, width int) string {
	query = strings.TrimSpace(query)
	label := sonutTheme.userPrompt.Render("search > ")
	valueWidth := maxInt(1, width-lipgloss.Width(label))
	value := sonutTheme.ink.Render(truncateRunes(query, valueWidth))
	return fitStyledLine(label+value, width)
}

func transparentSurface(style lipgloss.Style) lipgloss.Style {
	return style
}

func fillPaletteLine(line string, width int, surface func(lipgloss.Style) lipgloss.Style) string {
	line = fitStyledLine(line, width)
	pad := maxInt(0, width-lipgloss.Width(line))
	if pad > 0 {
		line += surface(sonutTheme.ink).Render(strings.Repeat(" ", pad))
	}
	return line
}

func centerRenderedBlock(block string, width int) string {
	if block == "" || width <= 0 {
		return block
	}
	lines := strings.Split(block, "\n")
	blockWidth := 0
	for _, line := range lines {
		if w := lipgloss.Width(line); w > blockWidth {
			blockWidth = w
		}
	}
	pad := maxInt(0, (width-blockWidth)/2)
	if pad == 0 {
		return block
	}
	return indentBlock(block, pad)
}

func selectableItems(suggestions []commandSuggestion, files bool) []selectableListItem {
	items := make([]selectableListItem, 0, len(suggestions))
	for _, suggestion := range suggestions {
		item := selectableListItem{Label: suggestion.Name, Description: suggestion.Desc}
		if files {
			item = fileSelectableItem(suggestion.Name)
		} else {
			item.Label = strings.TrimPrefix(item.Label, "/")
		}
		items = append(items, item)
	}
	return items
}

func fileSelectableItem(token string) selectableListItem {
	rel := strings.TrimPrefix(token, "@")
	rel = filepath.ToSlash(rel)
	isDir := strings.HasSuffix(rel, "/")
	cleanRel := strings.TrimSuffix(rel, "/")
	base := path.Base(cleanRel)
	if base == "." || base == "/" || base == "" {
		return selectableListItem{Label: strings.TrimPrefix(token, "@"), Description: "file"}
	}
	if isDir {
		base += "/"
	}
	dir := path.Dir(cleanRel)
	if dir == "." || dir == "" {
		return selectableListItem{Label: base}
	}
	return selectableListItem{Label: base, Description: dir}
}

// pickerOverlay renders an open interactive selector as a centered modal: a
// bordered panel with a title-and-hints row, rows carrying a provider dot and
// right metadata when the catalog exposes them, and the selected row on the
// selection tint.
func (m model) pickerOverlay(width int) string {
	if m.picker == nil {
		return ""
	}
	if m.picker.kind == pickerModel {
		return m.modelPickerOverlay(width)
	}
	overlayWidth := minInt(width, pickerOverlayMaxWidth)
	if overlayWidth < pickerOverlayMinWidth {
		overlayWidth = width
	}
	innerWidth := maxInt(1, overlayWidth-4)
	maxVisible := minInt(pickerOverlayMaxVisible, len(m.picker.items))
	start := 0
	visible := []pickerItem{}
	if len(m.picker.items) > 0 {
		m.picker.selected = clampInt(m.picker.selected, 0, len(m.picker.items)-1)
		start = selectableListStart(len(m.picker.items), maxVisible, m.picker.selected)
		visible = m.picker.items[start : start+maxVisible]
	}

	lines := make([]string, 0, len(visible)+5)
	title := strings.TrimSpace(m.picker.title)
	hint := "↑/↓ · ⏎ · esc"
	lines = append(lines, sonutTheme.faint.Render(hint))
	lastGroup := ""
	for index, item := range visible {
		absoluteIndex := start + index
		if item.Group != "" && item.Group != lastGroup {
			lines = append(lines, sonutTheme.accent.Render(item.Group))
			lastGroup = item.Group
		}
		surface := transparentSurface
		marker := surface(sonutTheme.faintest).Render("  ")
		if absoluteIndex == m.picker.selected {
			surface = sonutTheme.onSel
			marker = surface(sonutTheme.accent).Render("❯ ")
		}
		left := marker
		switch {
		case item.Local:
			left += surface(sonutTheme.blue).Render("● ")
		case item.Remote:
			left += surface(sonutTheme.accent).Render("● ")
		}
		if item.Favorite {
			left += surface(sonutTheme.accent).Render("* ")
		}
		left += surface(sonutTheme.ink).Render(item.Label)
		right := ""
		if item.Meta != "" {
			right = surface(sonutTheme.faintest).Render(item.Meta)
		}
		// Paint the gap on the row surface so selected rows read as one solid
		// band; joinHeaderLine would pad with bare (untinted) spaces.
		gap := innerWidth - lipgloss.Width(left) - lipgloss.Width(right)
		line := left + surface(sonutTheme.ink).Render(strings.Repeat(" ", maxInt(1, gap))) + right
		lines = append(lines, fitStyledLine(line, innerWidth))
	}
	if len(visible) == 0 {
		lines = append(lines, sonutTheme.faint.Render("  no matching items"))
	}
	return centerRenderedBlock(styledBlockFillTitle(overlayWidth, title, lines, sonutTheme.lineStrong, lipgloss.NewStyle()), width)
}

func (m model) modelPickerOverlay(width int) string {
	if m.picker == nil {
		return ""
	}
	if m.modelPickerLoading {
		return m.modelPickerLoadingOverlay(width)
	}
	overlayWidth := modelPickerOverlayWidth(width, m.picker)
	innerWidth := maxInt(1, overlayWidth-4)
	maxVisible := minInt(pickerOverlayMaxVisible, len(m.picker.items))
	start := 0
	visible := []pickerItem{}
	if len(m.picker.items) > 0 {
		m.picker.selected = clampInt(m.picker.selected, 0, len(m.picker.items)-1)
		start = selectableListStart(len(m.picker.items), maxVisible, m.picker.selected)
		visible = m.picker.items[start : start+maxVisible]
	}

	lines := make([]string, 0, len(visible)+6)
	searchInset := lipgloss.Width("❯ ")
	searchPrefix := transparentSurface(sonutTheme.ink).Render(strings.Repeat(" ", searchInset))
	lines = append(lines, fillPaletteLine(searchPrefix+renderModelPickerSearchLine(m.picker.query, maxInt(1, innerWidth-searchInset)), innerWidth, transparentSurface))
	if status := strings.TrimSpace(m.modelPickerLoadError); status != "" {
		lines = append(lines, fillPaletteLine(searchPrefix+sonutTheme.faint.Render(status), innerWidth, transparentSurface))
	}
	lines = append(lines, sonutTheme.line.Render(strings.Repeat("─", innerWidth)))
	lastGroup := ""
	for index, item := range visible {
		if item.Group != "" && item.Group != lastGroup {
			lines = append(lines, fillPaletteLine(sonutTheme.accent.Bold(true).Render(item.Group), innerWidth, transparentSurface))
			lastGroup = item.Group
		}
		lines = append(lines, renderModelPickerRow(innerWidth, start+index == m.picker.selected, item))
	}
	if len(visible) == 0 {
		lines = append(lines, fillPaletteLine(searchPrefix+sonutTheme.faint.Render("no matching models"), innerWidth, transparentSurface))
	}
	if item, ok := m.picker.current(); ok {
		if detail := modelPickerItemDetail(item); detail != "" {
			lines = append(lines, sonutTheme.line.Render(strings.Repeat("─", innerWidth)))
			lines = append(lines, fillPaletteLine(searchPrefix+sonutTheme.faint.Render(detail), innerWidth, transparentSurface))
		}
	}
	lines = append(lines, sonutTheme.line.Render(strings.Repeat("─", innerWidth)))
	footer := "↑/↓ move   Enter select   Ctrl+F favorite   Esc close"
	lines = append(lines, fillPaletteLine(sonutTheme.faint.Render(footer), innerWidth, transparentSurface))
	title := strings.TrimSpace(m.picker.title)
	if title == "" {
		title = "Choose a model"
	}
	return centerRenderedBlock(styledBlockFillTitle(overlayWidth, title, lines, sonutTheme.lineStrong, lipgloss.NewStyle()), width)
}

func (m model) modelPickerLoadingOverlay(width int) string {
	overlayWidth := modelPickerLoadingOverlayWidth(width)
	innerWidth := maxInt(1, overlayWidth-4)
	lines := []string{
		fillPaletteLine(sonutTheme.faint.Render("Checking available models..."), innerWidth, transparentSurface),
		fillPaletteLine(sonutTheme.faint.Render("Built-in models will be used if discovery fails."), innerWidth, transparentSurface),
		sonutTheme.line.Render(strings.Repeat("─", innerWidth)),
		fillPaletteLine(sonutTheme.faint.Render("Esc close"), innerWidth, transparentSurface),
	}
	title := strings.TrimSpace(m.picker.title)
	if title == "" {
		title = "Choose a model"
	}
	return centerRenderedBlock(styledBlockFillTitle(overlayWidth, title, lines, sonutTheme.lineStrong, lipgloss.NewStyle()), width)
}

func modelPickerLoadingOverlayWidth(terminalWidth int) int {
	if terminalWidth <= 0 {
		terminalWidth = defaultStartupWidth
	}
	available := minInt(terminalWidth, modelPickerOverlayMaxWidth)
	if terminalWidth < modelPickerOverlayMinWidth {
		available = terminalWidth
	}
	target := lipgloss.Width("Built-in models will be used if discovery fails.")
	target = maxInt(target, lipgloss.Width("Choose a model"))
	target = maxInt(target, lipgloss.Width("Esc close"))
	overlayWidth := maxInt(modelPickerOverlayMinWidth, target+4)
	return minInt(overlayWidth, maxInt(4, available))
}

func modelPickerOverlayWidth(terminalWidth int, picker *commandPicker) int {
	if terminalWidth <= 0 {
		terminalWidth = defaultStartupWidth
	}
	available := minInt(terminalWidth, modelPickerOverlayMaxWidth)
	if terminalWidth < modelPickerOverlayMinWidth {
		available = terminalWidth
	}
	target := lipgloss.Width("Choose a model")
	target = maxInt(target, lipgloss.Width("  search > model name..."))
	target = maxInt(target, lipgloss.Width("↑/↓ move   Enter select   Ctrl+F favorite   Esc close"))
	target = maxInt(target, lipgloss.Width("  Using built-in model list"))
	if picker != nil {
		for _, item := range picker.items {
			labelWidth := lipgloss.Width(item.Label)
			if item.Favorite {
				labelWidth += lipgloss.Width("* ")
			}
			target = maxInt(target, lipgloss.Width("❯ ")+labelWidth)
			if detail := modelPickerItemDetail(item); detail != "" {
				target = maxInt(target, lipgloss.Width("  "+detail))
			}
		}
	}
	overlayWidth := maxInt(modelPickerOverlayMinWidth, target+4)
	return minInt(overlayWidth, maxInt(4, available))
}

func renderModelPickerSearchLine(query string, width int) string {
	query = strings.TrimSpace(query)
	prompt := sonutTheme.userPrompt.Render("search > ")
	cursor := sonutTheme.accent.Render("▌")
	if query == "" {
		return fitStyledLine(prompt+cursor+sonutTheme.faint.Render("model name..."), width)
	}
	return fitStyledLine(prompt+sonutTheme.ink.Render(query)+cursor, width)
}

func renderModelPickerRow(width int, selected bool, item pickerItem) string {
	surface := transparentSurface
	marker := surface(sonutTheme.faintest).Render("  ")
	if selected {
		surface = sonutTheme.onSel
		marker = surface(sonutTheme.accent).Render("❯ ")
	}
	label := strings.TrimSpace(item.Label)
	if label == "" {
		label = strings.TrimSpace(item.Value)
	}
	prefix := ""
	if item.Favorite {
		prefix = "* "
	}
	left := marker + surface(sonutTheme.ink).Render(prefix+label)
	// The provider is shown as a section header above each group, so rows no longer
	// repeat it as a right-aligned tag (matches a grouped provider+model list).
	return fillPaletteLine(left, width, surface)
}

func modelPickerItemDetail(item pickerItem) string {
	parts := []string{}
	value := strings.TrimSpace(item.Value)
	label := strings.TrimSpace(item.Label)
	if value != "" && value != label {
		parts = append(parts, value)
	}
	if meta := strings.TrimSpace(item.Meta); meta != "" {
		parts = append(parts, meta)
	}
	return strings.Join(parts, " · ")
}

// argHint extracts the most representative argument from a tool call's raw JSON
// arguments for the single-line tool row (the path, pattern, or command acted on).
func argHint(raw string) string {
	return firstArgValue(raw, []string{"path", "file", "file_path", "filepath", "pattern", "query", "command", "cmd", "url", "task"})
}

// argHintSecondary extracts the card head's faintest arg column: the
// non-target argument (pattern/query/command) when argHint already resolved to
// a path. With no path argument the value is argHint itself, so it stays in
// the target slot and this returns "".
func argHintSecondary(raw string) string {
	secondary := firstArgValue(raw, []string{"pattern", "query", "command", "cmd", "url"})
	if secondary == "" || secondary == argHint(raw) {
		return ""
	}
	return secondary
}

func firstArgValue(raw string, keys []string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" || raw == "{}" {
		return ""
	}
	var args map[string]any
	if err := json.Unmarshal([]byte(raw), &args); err != nil {
		return ""
	}
	for _, key := range keys {
		if value, ok := args[key]; ok {
			if text, ok := value.(string); ok && strings.TrimSpace(text) != "" {
				return singleLineToolHeadText(text)
			}
		}
	}
	return ""
}

// looksLikeDiff reports whether output should be rendered as a diff card: a
// real hunk header, or both old/new file headers. A single line starting with
// "---" (a Markdown rule, YAML document marker, log separator…) must NOT
// hijack ordinary bash/tool output into the diff renderer.
func looksLikeDiff(text string) bool {
	if !strings.Contains(text, "\n") {
		return false
	}
	hasOld, hasNew := false, false
	for _, line := range strings.Split(text, "\n") {
		switch {
		case hunkHeaderPattern.MatchString(line):
			return true
		case strings.HasPrefix(line, "+++ "):
			hasNew = true
		case strings.HasPrefix(line, "--- "):
			hasOld = true
		}
		if hasOld && hasNew {
			return true
		}
	}
	return false
}

func truncateRunes(text string, limit int) string {
	if limit <= 0 {
		return ""
	}
	runes := []rune(text)
	if len(runes) <= limit {
		return text
	}
	if limit == 1 {
		return "…"
	}
	return string(runes[:limit-1]) + "…"
}
