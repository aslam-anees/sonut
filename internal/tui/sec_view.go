package tui

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/aistatine/sonut/internal/sandbox"
	"github.com/aistatine/sonut/internal/sec"
	runtimeSecurity "github.com/aistatine/sonut/internal/security"
	"github.com/aistatine/sonut/internal/security/sensor"
)

type secTab int

const (
	secTabOverview secTab = iota
	secTabFindings
	secTabPaths
	secTabAtlas
	secTabActivity
	secTabControls
	secTabSurfaces
	secTabSignals
	secTabAdvisor
	secTabRoutes
	secTabCount
)

var secTabNames = [...]string{"Overview", "Risks", "Paths", "Atlas", "Activity", "Controls", "Surfaces", "Signals", "Advisor", "Routes"}

type secAdvisorMessage struct {
	role string
	text string
}

// secViewState is the native, full-width /sec workspace. The report is an
// immutable scan snapshot; selection, tabs, and chat are process-local UI state.
type secViewState struct {
	active             bool
	transitioning      bool
	transitionSeq      int
	transitionFrame    int
	report             sec.Report
	reportPath         string
	tab                secTab
	selected           int
	expanded           bool
	atlasFrame         int
	atlasSelected      int
	atlasFilter        secAtlasFilter
	atlasPaused        bool
	atlasExpanded      bool
	atlasInterfaces    []runtimeSecurity.NetworkInterface
	parentScrollOffset int
	advisor            *sec.Advisor
	advisorMessages    []secAdvisorMessage
	advisorAsking      bool
	advisorGeneration  int
	notice             string
}

type secAdvisorResultMsg struct {
	generation int
	question   string
	answer     string
	err        error
}

type secTransitionTickMsg struct {
	seq int
}

const secTransitionTickInterval = 90 * time.Millisecond

func secTransitionTickCmd(seq int) tea.Cmd {
	return tea.Tick(secTransitionTickInterval, func(time.Time) tea.Msg {
		return secTransitionTickMsg{seq: seq}
	})
}

func (m model) openSecView(report sec.Report, reportPath string) model {
	parentOffset := m.chatScrollOffset
	if m.secView.active {
		parentOffset = m.secView.parentScrollOffset
	}
	view := secViewState{
		active:             true,
		report:             report,
		reportPath:         reportPath,
		atlasInterfaces:    runtimeSecurity.LocalNetworkInterfaces(),
		parentScrollOffset: parentOffset,
	}
	if m.provider != nil {
		view.advisor, _ = sec.NewAdvisor(report, m.provider)
	}
	m.secView = view
	m.chatScrollOffset = 0
	m.clearComposer()
	m.clearSuggestions()
	m = m.clearHover()
	return m
}

func (m model) closeSecView() model {
	if !m.secView.active {
		return m
	}
	m.chatScrollOffset = m.secView.parentScrollOffset
	m.secView = secViewState{}
	m.clearComposer()
	m.clearSuggestions()
	m = m.clearHover()
	return m
}

func (m model) secViewNavBar(width int) string {
	left := sonutTheme.accent.Render("◆ Security posture")
	available := maxInt(0, width-lipgloss.Width(left)-2)
	tabs := make([]string, 0, int(secTabCount))
	for index, name := range secTabNames {
		label := name
		if secTab(index) == m.secView.tab {
			label = sonutTheme.ink.Bold(true).Render("[" + name + "]")
		} else {
			label = sonutTheme.faint.Render(name)
		}
		tabs = append(tabs, label)
	}
	right := strings.Join(tabs, sonutTheme.line.Render("  "))
	if lipgloss.Width(right) > available {
		right = sonutTheme.faint.Render(fmt.Sprintf("%d/%d %s", int(m.secView.tab)+1, secTabCount, secTabNames[m.secView.tab]))
	}
	return fitStyledLine(joinHeaderLine(left, right, width), width)
}

// secTransitionView replaces the chat surface while the bounded scan runs.
// The scan used to happen inside the keypress update, which meant Bubble Tea
// could not paint anything until the filesystem work was complete. Keeping the
// transition full-screen makes that handoff feel intentional and gives the
// user a clear indication that /sec is still alive.
func (m model) secTransitionView(width int) string {
	if width <= 0 {
		width = defaultStartupWidth
	}
	height := m.height
	if height <= 0 {
		height = defaultStartupHeight
	}

	lines := make([]string, height)
	for row := range lines {
		lines[row] = secTransitionDotLine(width, row, height, m.secView.transitionFrame)
	}

	dotCount := 1
	if !m.reducedMotion {
		dotCount = (m.secView.transitionFrame/5)%4 + 1
	}
	loadingDots := strings.Repeat(".", dotCount)
	cardWidth := minInt(60, maxInt(16, width-4))
	cardInnerWidth := cardWidth - 4
	card := secTransitionCard(cardWidth, cardInnerWidth, loadingDots, m.secView.transitionFrame)
	start := maxInt(0, (height-len(card))/2)
	for index, line := range card {
		row := start + index
		if row >= len(lines) {
			break
		}
		lines[row] = centerLine(fitStyledLine(line, width), width)
	}
	return strings.Join(lines, "\n")
}

func secTransitionCard(width, innerWidth int, loadingDots string, frame int) []string {
	progressWidth := minInt(30, maxInt(8, innerWidth))
	return []string{
		sonutTheme.lineStrong.Render("╭" + strings.Repeat("─", width-2) + "╮"),
		secTransitionCardRow(sonutTheme.accent.Render("◆  SECURITY POSTURE"), innerWidth),
		secTransitionCardRow("", innerWidth),
		secTransitionCardRow(sonutTheme.ink.Bold(true).Render("Scanning workspace"+loadingDots), innerWidth),
		secTransitionCardRow(secTransitionProgress(progressWidth, frame), innerWidth),
		secTransitionCardRow(sonutTheme.faint.Render("read-only  ·  local evidence  ·  no code execution"), innerWidth),
		secTransitionCardRow(sonutTheme.faint.Render("Press Esc to cancel"), innerWidth),
		sonutTheme.lineStrong.Render("╰" + strings.Repeat("─", width-2) + "╯"),
	}
}

func secTransitionCardRow(content string, innerWidth int) string {
	content = fitStyledLine(content, innerWidth)
	padding := maxInt(0, innerWidth-lipgloss.Width(content))
	return sonutTheme.line.Render("│ ") + content + strings.Repeat(" ", padding) + sonutTheme.line.Render(" │")
}

func secTransitionProgress(width, frame int) string {
	var progress strings.Builder
	cursor := frame % maxInt(1, width)
	for index := 0; index < width; index++ {
		if index == cursor {
			progress.WriteString(sonutTheme.accent.Render("●"))
			continue
		}
		progress.WriteString(sonutTheme.faintest.Render("·"))
	}
	return progress.String()
}

func secTransitionDotLine(width, row, height, frame int) string {
	var line strings.Builder
	for column := 0; column < width; column += 4 {
		cell := column / 4
		nearCenter := secAbsInt(column-width/2) < width/3 && secAbsInt(row-height/2) < height/3
		phase := (cell*3 + row*5 + frame) % 19
		glyph := " "
		style := sonutTheme.faintest
		if !nearCenter {
			switch phase {
			case 0:
				glyph = "•"
				style = sonutTheme.accent
			case 1:
				glyph = "·"
				style = sonutTheme.faint
			}
		}
		line.WriteString(style.Render(glyph))
		if column+3 < width {
			line.WriteString("   ")
		}
	}
	return fitStyledLine(line.String(), width)
}

func secAbsInt(value int) int {
	if value < 0 {
		return -value
	}
	return value
}

func (m model) secViewBodyItems(width int) []transcriptBodyItem {
	block := m.renderSecView(width)
	return []transcriptBodyItem{transcriptBlockBodyItem(transcriptBodyItemRow, -1, block)}
}

func (m model) renderSecView(width int) string {
	inner := maxInt(20, width-2)
	var lines []string
	switch m.secView.tab {
	case secTabFindings:
		lines = m.renderSecFindings(inner)
	case secTabPaths:
		lines = m.renderSecPaths(inner)
	case secTabAtlas:
		lines = m.renderSecAtlas(inner)
	case secTabActivity:
		lines = m.renderSecActivity(inner)
	case secTabControls:
		lines = m.renderSecControls(inner)
	case secTabSurfaces:
		lines = m.renderSecSurfaces(inner)
	case secTabSignals:
		lines = m.renderSecSignals(inner)
	case secTabAdvisor:
		lines = m.renderSecAdvisor(inner)
	case secTabRoutes:
		lines = m.renderSecRoutes(inner)
	default:
		lines = m.renderSecOverview(inner)
	}
	for index := range lines {
		lines[index] = fitStyledLine(lines[index], inner)
	}
	return strings.Join(lines, "\n")
}

func (m model) renderSecOverview(width int) []string {
	report := m.secView.report
	findings := report.Findings
	pathCount := countAttackPaths(report)
	controlsPresent := 0
	for _, control := range report.Controls {
		if control.Status == "present" {
			controlsPresent++
		}
	}

	scoreStyle := sonutTheme.green.Bold(true)
	if report.Score < 80 {
		scoreStyle = sonutTheme.amber.Bold(true)
	}
	if report.Score < 55 {
		scoreStyle = sonutTheme.red.Bold(true)
	}
	score := scoreStyle.Render(fmt.Sprintf("%d", report.Score))
	grade := scoreStyle.Render("Grade " + report.Grade)
	gauge := secScoreGauge(report.Score, minInt(30, maxInt(12, width/3)))
	lines := []string{
		"",
		score + sonutTheme.faint.Render("/100  ") + grade + "  " + gauge,
		sonutTheme.faint.Render("Bounded workspace posture · source and configuration evidence only"),
		"",
		secSection("At a glance", width, fmt.Sprintf("%d files", report.Coverage.FilesScanned)),
		secMetricLine(report.Summary.Critical, "critical", sonutTheme.red) + "   " +
			secMetricLine(report.Summary.High, "high", sonutTheme.amber) + "   " +
			secMetricLine(report.Summary.Medium, "medium", sonutTheme.blue),
		fmt.Sprintf("%s %d verified path(s)   %s %d/%d controls present",
			sonutTheme.ink.Bold(true).Render("Paths"), pathCount,
			sonutTheme.ink.Bold(true).Render("Controls"), controlsPresent, len(report.Controls)),
		secRuntimeSummary(runtimeSecurity.Default().Summary()),
		secSensorSummary(m.cwd),
		secAgentOverviewSummary(m),
		"",
		secSection("Attack surface", width, "typed assets + exposure evidence"),
		secOverviewAttackSurface(report),
		secSection("Deep paths", width, "evidence-backed"),
		secOverviewPathSummary(report),
		secSection("Fix", width, "routing"),
		secOverviewFixSummary(report),
		secSection("Defense", width, "runtime layers"),
		secOverviewDefenseSummary(m.cwd),
		"",
		secSection("Fix first", width, "highest risk first"),
	}
	if len(findings) == 0 {
		lines = append(lines,
			sonutTheme.green.Render("✓ No findings in the scanned workspace scope."),
			sonutTheme.faint.Render("This is not a live-cloud or runtime assurance claim."),
		)
	} else {
		for index, finding := range findings {
			if index == 3 {
				break
			}
			lines = append(lines, secFindingSummary(finding, width, false))
		}
		lines = append(lines, sonutTheme.faint.Render("Press 2 for the complete prioritized list and evidence."))
	}
	lines = append(lines,
		"",
		secSection("Coverage", width, fmt.Sprintf("%dms", report.Coverage.DurationMillis)),
		fmt.Sprintf("%d seen · %d scanned · %d skipped · %s inspected",
			report.Coverage.FilesSeen, report.Coverage.FilesScanned,
			report.Coverage.FilesSkipped, formatSecBytes(report.Coverage.BytesScanned)),
		sonutTheme.faint.Render("Agentless · read-only · project code was not executed · ref/ and dependency trees skipped"),
		sonutTheme.faint.Render("Report · "+m.secView.reportPath),
		"",
		secSection("Trust boundary", width),
		sonutTheme.faint.Render("Secret values are redacted before persistence. /sec does not query cloud accounts, registries, or prove reachability."),
	)
	return lines
}

func (m model) renderSecFindings(width int) []string {
	findings := m.secView.report.Findings
	lines := []string{"", secSection("Prioritized risks", width, pluralCount(len(findings), "finding"))}
	if len(findings) == 0 {
		return append(lines, sonutTheme.green.Render("✓ No findings in the scanned workspace scope."))
	}
	selected := clampInt(m.secView.selected, 0, len(findings)-1)
	for index, finding := range findings {
		isSelected := index == selected
		lines = append(lines, secFindingSummary(finding, width, isSelected))
		if isSelected && m.secView.expanded {
			lines = append(lines, renderSecFindingDetail(finding, width)...)
		}
	}
	lines = append(lines, "", sonutTheme.faint.Render("↑/↓ or j/k select · Enter details · r rescan"))
	return lines
}

func secFindingSummary(finding sec.Finding, width int, selected bool) string {
	marker := "  "
	surface := transparentSurface
	if selected {
		marker = "› "
		surface = func(style lipgloss.Style) lipgloss.Style { return sonutTheme.onSel(style) }
	}
	severity := strings.ToUpper(string(finding.Severity))
	label := surface(secSeverityStyle(finding.Severity).Bold(true)).Render(fmt.Sprintf("%-8s", severity))
	score := surface(sonutTheme.faint).Render(fmt.Sprintf("%3d", finding.RiskScore))
	titleWidth := maxInt(8, width-lipgloss.Width(marker)-lipgloss.Width(label)-lipgloss.Width(score)-4)
	title := surface(sonutTheme.ink).Render(ansi.Truncate(finding.Title, titleWidth, "…"))
	line := surface(sonutTheme.ink).Render(marker) + label + " " + score + "  " + title
	location := finding.Path
	if finding.Line > 0 {
		location += fmt.Sprintf(":%d", finding.Line)
	}
	if location != "" && lipgloss.Width(line)+lipgloss.Width(location)+3 <= width {
		line += surface(sonutTheme.faint).Render("  " + location)
	}
	return fillPaletteLine(line, width, surface)
}

func renderSecFindingDetail(finding sec.Finding, width int) []string {
	indent := "    "
	measure := maxInt(20, width-lipgloss.Width(indent))
	lines := []string{}
	addWrapped := func(label, value string, style lipgloss.Style) {
		value = strings.TrimSpace(value)
		if value == "" {
			return
		}
		prefix := indent + sonutTheme.faint.Render(label+"  ")
		wrapped := wrapPlainText(value, maxInt(12, measure-len(label)-2))
		for index, line := range wrapped {
			if index == 0 {
				lines = append(lines, prefix+style.Render(line))
			} else {
				lines = append(lines, indent+strings.Repeat(" ", len(label)+2)+style.Render(line))
			}
		}
	}
	addWrapped("Observed", finding.Description, sonutTheme.muted)
	addWrapped("Evidence", finding.Evidence, sonutTheme.blue)
	if len(finding.Factors) > 0 {
		addWrapped("Why now", strings.Join(finding.Factors, " · "), sonutTheme.muted)
	}
	addWrapped("Fix", finding.Remediation, sonutTheme.green)
	addWrapped("Verify", finding.Verification, sonutTheme.blue)
	confidence := strings.TrimSpace(finding.Confidence)
	if confidence == "" {
		confidence = "unknown"
	}
	evidenceKind := strings.TrimSpace(finding.EvidenceKind)
	if evidenceKind == "" {
		evidenceKind = "local observation"
	}
	meta := "ID " + finding.ID + " · " + confidence + " confidence · " + evidenceKind
	if len(finding.AttackPath) > 0 {
		meta += " · correlated path"
	}
	lines = append(lines, indent+sonutTheme.faint.Render(meta))
	return lines
}

func (m model) renderSecPaths(width int) []string {
	paths := make([]sec.Finding, 0)
	for _, finding := range m.secView.report.Findings {
		if len(finding.AttackPath) > 0 {
			paths = append(paths, finding)
		}
	}
	lines := []string{"", secSection("Verified risk paths", width, pluralCount(len(paths), "path"))}
	if len(paths) == 0 {
		lines = append(lines,
			sonutTheme.green.Render("✓ No toxic combination was proven."),
			sonutTheme.faint.Render("Isolated signals remain in Risks; /sec will not invent an edge without supporting evidence."),
		)
		return lines
	}
	for index, finding := range paths {
		lines = append(lines, sonutTheme.faint.Render(fmt.Sprintf("%02d", index+1))+"  "+secSeverityStyle(finding.Severity).Bold(true).Render(strings.ToUpper(string(finding.Severity)))+"  "+sonutTheme.ink.Bold(true).Render(finding.Title))
		nodes := make([]string, 0, len(finding.AttackPath))
		for _, node := range finding.AttackPath {
			nodes = append(nodes, secPathLabel(node))
		}
		lines = append(lines, "    "+strings.Join(nodes, sonutTheme.accent.Render("  →  ")))
		lines = append(lines, "    "+sonutTheme.faint.Render("Evidence: "+finding.Path+" · "+finding.ID))
		if index < len(paths)-1 {
			lines = append(lines, "")
		}
	}
	lines = append(lines, "", sonutTheme.faint.Render("A path means multiple observed signals connect in one resource; it does not prove exploitation."))
	return lines
}

func (m model) renderSecControls(width int) []string {
	controls := m.secView.report.Controls
	lines := []string{"", secSection("Foundational controls", width, pluralCount(len(controls), "control"))}
	for _, control := range controls {
		icon := sonutTheme.green.Render("✓")
		status := sonutTheme.green.Render("present")
		if control.Status != "present" {
			icon = sonutTheme.amber.Render("○")
			status = sonutTheme.amber.Render("missing")
		}
		nameWidth := maxInt(10, width-18)
		lines = append(lines, icon+"  "+sonutTheme.ink.Render(ansi.Truncate(control.Name, nameWidth, "…"))+"  "+status)
		description := control.Description
		if control.Evidence != "" {
			description += " · " + control.Evidence
		}
		for _, line := range wrapPlainText(description, maxInt(16, width-4)) {
			lines = append(lines, "   "+sonutTheme.faint.Render(line))
		}
	}
	lines = append(lines, "", sonutTheme.faint.Render("Missing means not observed in this workspace snapshot; it is not a compliance verdict."))
	return lines
}

func (m model) renderSecAdvisor(width int) []string {
	lines := []string{"", secSection("AI security advisor", width, "redacted report only")}
	if m.secView.advisor == nil {
		return append(lines,
			sonutTheme.amber.Render("Connect a model to use grounded security chat."),
			sonutTheme.faint.Render("The scan, evidence, paths, and remediation guidance remain fully available without AI."),
			sonutTheme.faint.Render("Run /provider after closing /sec, then reopen this report."),
		)
	}
	if len(m.secView.advisorMessages) == 0 {
		lines = append(lines,
			sonutTheme.ink.Bold(true).Render("Ask about this scan"),
			sonutTheme.faint.Render("Enter on an empty prompt asks the recommended first question."),
			"",
			sonutTheme.blue.Render("• What should I fix first, and why?"),
			sonutTheme.blue.Render("• Explain the highest-risk path in beginner-friendly terms."),
			sonutTheme.blue.Render("• Give me a verification checklist after remediation."),
		)
	}
	for _, message := range m.secView.advisorMessages {
		label := sonutTheme.accent.Render("YOU")
		bodyStyle := sonutTheme.ink
		if message.role == "advisor" {
			label = sonutTheme.blue.Render("SEC")
			bodyStyle = sonutTheme.muted
		}
		lines = append(lines, "", label)
		if message.role == "advisor" {
			for _, line := range renderAssistantMarkdownText(message.text, assistantMeasure(width-2), maxInt(20, width-2), true) {
				lines = append(lines, "  "+line)
			}
		} else {
			for _, line := range wrapPlainText(message.text, maxInt(20, width-2)) {
				lines = append(lines, "  "+bodyStyle.Render(line))
			}
		}
	}
	if m.secView.advisorAsking {
		lines = append(lines, "", sonutTheme.accent.Render("◌")+" "+sonutTheme.faint.Render("Reviewing the redacted report…"))
	}
	if m.secView.notice != "" {
		lines = append(lines, "", sonutTheme.amber.Render(m.secView.notice))
	}
	lines = append(lines, "", sonutTheme.faint.Render("No tools · no workspace writes · answers must cite finding IDs"))
	return lines
}

func (m model) renderSecRoutes(width int) []string {
	routes := m.secView.report.Routes
	lines := []string{"", secSection("Configured routes", width, pluralCount(len(routes), "route")), sonutTheme.faint.Render("Proxy and ingress mappings observed in local configuration."), sonutTheme.faint.Render("Configured-public is configuration evidence, not proof of internet reachability."), ""}
	if len(routes) == 0 {
		return append(lines,
			sonutTheme.green.Render("✓ No proxy or ingress route was recognized."),
			sonutTheme.faint.Render("Compose ports, Kubernetes ingress, and route files remain covered by the typed attack-surface scan."),
		)
	}
	selected := clampInt(m.secView.selected, 0, len(routes)-1)
	for index, route := range routes {
		selectedStyle := transparentSurface
		marker := "  "
		if index == selected {
			selectedStyle = func(style lipgloss.Style) lipgloss.Style { return sonutTheme.onSel(style) }
			marker = "› "
		}
		state := routeExposureStyle(route.ExposureState).Bold(true).Render(strings.ToUpper(string(route.ExposureState)))
		title := firstSecRouteValue(route.Name, route.Host, "unnamed route")
		left := selectedStyle(sonutTheme.ink.Bold(true)).Render(marker + ansi.Truncate(title, maxInt(10, width-35), "…"))
		right := state
		if route.Protocol != "" {
			right += " " + sonutTheme.faint.Render(strings.ToUpper(route.Protocol))
		}
		lines = append(lines, fillPaletteLine(joinHeaderLine(left, right, width), width, selectedStyle))
		location := route.Host
		if route.Path != "" {
			location += route.Path
		}
		if location == "" {
			location = "local route"
		}
		meta := location + " → " + firstSecRouteValue(route.Target, "target unknown")
		if len(route.Ports) > 0 {
			meta += " · ports " + joinSecRoutePorts(route.Ports)
		}
		lines = append(lines, "  "+selectedStyle(sonutTheme.blue).Render(ansi.Truncate(meta, maxInt(12, width-2), "…")))
		if index == selected && m.secView.expanded {
			lines = append(lines, renderSecRouteDetail(route, width)...)
		}
	}
	lines = append(lines, "", sonutTheme.faint.Render("↑/↓ or j/k select · Enter details · r rescan · Tab switch"))
	return lines
}

func renderSecRouteDetail(route sec.Route, width int) []string {
	indent := "    "
	lines := []string{indent + sonutTheme.ink.Bold(true).Render("Policy")}
	lines = append(lines, indent+sonutTheme.faint.Render("authentication  "+firstSecRouteValue(route.Authentication, "unknown")))
	lines = append(lines, indent+sonutTheme.faint.Render("TLS             "+firstSecRouteValue(route.TLS, "unknown")))
	if len(route.Middlewares) > 0 {
		lines = append(lines, indent+sonutTheme.faint.Render("middlewares     "+strings.Join(route.Middlewares, ", ")))
	}
	lines = append(lines, indent+sonutTheme.faint.Render("source          "+firstSecRouteValue(route.SourcePath, "not recorded")))
	if route.SourceLine > 0 {
		lines[len(lines)-1] += fmt.Sprintf(":%d", route.SourceLine)
	}
	if len(route.EvidenceIDs) > 0 {
		lines = append(lines, indent+sonutTheme.faint.Render("evidence        "+strings.Join(route.EvidenceIDs, ", ")))
	}
	lines = append(lines, indent+sonutTheme.amber.Render("Reachability is not established by static route configuration."))
	for index := range lines {
		lines[index] = fitStyledLine(lines[index], width)
	}
	return lines
}

func routeExposureStyle(state sec.ExposureState) lipgloss.Style {
	switch state {
	case sec.ExposureRuntimeObserved:
		return sonutTheme.blue
	case sec.ExposureConfiguredPublic:
		return sonutTheme.amber
	case sec.ExposurePotential:
		return sonutTheme.faint
	default:
		return sonutTheme.muted
	}
}

func firstSecRouteValue(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

func joinSecRoutePorts(ports []int) string {
	parts := make([]string, len(ports))
	for index, port := range ports {
		parts[index] = fmt.Sprintf("%d", port)
	}
	return strings.Join(parts, ",")
}

func (m model) secFooterView(width int) string {
	var footer strings.Builder
	footer.WriteString(sonutTheme.line.Render(strings.Repeat("─", maxInt(1, width))))
	footer.WriteString("\n")
	if m.secView.tab == secTabAdvisor && m.secView.advisor != nil {
		footer.WriteString(m.composerBox(width))
		footer.WriteString("\n")
		if m.secView.advisorAsking {
			footer.WriteString(fitStyledLine(sonutTheme.faint.Render("Advisor is answering · Esc close when finished"), width))
		} else {
			footer.WriteString(fitStyledLine(sonutTheme.faint.Render("Type a question · Enter ask · Tab switch · Esc close"), width))
		}
		return footer.String()
	}
	hint := "1-9 views · 0 routes · Tab switch · r rescan · a advisor · Esc close"
	if m.secView.tab == secTabFindings {
		hint = "↑/↓ select · Enter details · x fix with agent · Tab switch · r rescan · Esc close"
	}
	if m.secView.tab == secTabRoutes {
		hint = "↑/↓ select · Enter details · Tab switch · r rescan · Esc close"
	}
	if m.secView.tab == secTabAtlas {
		hint = "↑/↓ inspect · f filter · Space pause · Tab switch · Esc close"
	}
	footer.WriteString(fitStyledLine(sonutTheme.faint.Render(hint), width))
	return footer.String()
}

func (m model) handleSecViewKey(msg tea.KeyPressMsg) (model, tea.Cmd, bool) {
	if !m.secView.active {
		return m, nil, false
	}
	if m.secView.transitioning {
		if keyCtrl(msg, 'c') || keyIs(msg, tea.KeyEsc) || (keyText(msg) == "q" && m.composerValue() == "") {
			return m.closeSecView(), nil, true
		}
		// Keep the transition focused while the scan is in flight. In particular,
		// don't let a key intended for the loading surface enter the composer.
		return m, nil, true
	}
	if keyCtrl(msg, 'c') {
		return m.closeSecView(), nil, true
	}
	if keyIs(msg, tea.KeyEsc) || (keyText(msg) == "q" && m.composerValue() == "") {
		if m.secView.tab == secTabAdvisor && m.composerValue() != "" {
			m.clearComposer()
			return m, nil, true
		}
		return m.closeSecView(), nil, true
	}
	if keyIs(msg, tea.KeyPgUp) || keyIs(msg, tea.KeyPgDown) {
		return m, nil, false
	}
	if keyText(msg) == "r" && m.composerValue() == "" && m.secView.tab != secTabAdvisor && !m.secView.advisorAsking {
		next, cmd := m.startSecCommand("rescan")
		return next, cmd, true
	}
	if keyText(msg) == "a" && m.composerValue() == "" && m.secView.tab != secTabAdvisor {
		m = m.setSecTab(secTabAdvisor)
		return m, nil, true
	}
	if (keyText(msg) == "x" || keyText(msg) == "X") && m.composerValue() == "" && m.secView.tab == secTabFindings && len(m.secView.report.Findings) > 0 {
		finding := m.secView.report.Findings[clampInt(m.secView.selected, 0, len(m.secView.report.Findings)-1)]
		prompt := secFixPrompt(finding, m.secView.report)
		m = m.closeSecView()
		next, cmd := m.launchPrompt(prompt)
		return next, cmd, true
	}
	if m.composerValue() == "" && m.secView.tab != secTabAdvisor {
		switch keyText(msg) {
		case "1":
			return m.setSecTab(secTabOverview), nil, true
		case "2":
			return m.setSecTab(secTabFindings), nil, true
		case "3":
			return m.setSecTab(secTabPaths), nil, true
		case "4":
			return m.setSecTab(secTabAtlas), nil, true
		case "5":
			return m.setSecTab(secTabActivity), nil, true
		case "6":
			return m.setSecTab(secTabControls), nil, true
		case "7":
			return m.setSecTab(secTabSurfaces), nil, true
		case "8":
			return m.setSecTab(secTabSignals), nil, true
		case "9":
			return m.setSecTab(secTabAdvisor), nil, true
		case "0":
			return m.setSecTab(secTabRoutes), nil, true
		}
	}
	if m.secView.tab == secTabAtlas && m.composerValue() == "" {
		switch keyText(msg) {
		case "f":
			m.secView.atlasFilter = (m.secView.atlasFilter + 1) % secAtlasFilterCount
			m.secView.atlasSelected = 0
			m.secView.atlasExpanded = false
			m.chatScrollOffset = 0
			return m, nil, true
		case " ":
			m.secView.atlasPaused = !m.secView.atlasPaused
			return m, nil, true
		}
	}
	switch {
	case keyIs(msg, tea.KeyTab):
		delta := 1
		if keyShift(msg) {
			delta = -1
		}
		tab := (int(m.secView.tab) + delta + int(secTabCount)) % int(secTabCount)
		return m.setSecTab(secTab(tab)), nil, true
	case keyIs(msg, tea.KeyLeft) && m.composerValue() == "":
		tab := (int(m.secView.tab) - 1 + int(secTabCount)) % int(secTabCount)
		return m.setSecTab(secTab(tab)), nil, true
	case keyIs(msg, tea.KeyRight) && m.composerValue() == "":
		tab := (int(m.secView.tab) + 1) % int(secTabCount)
		return m.setSecTab(secTab(tab)), nil, true
	case (keyIs(msg, tea.KeyDown) || keyText(msg) == "j") && m.composerValue() == "":
		if m.secView.tab == secTabFindings && len(m.secView.report.Findings) > 0 {
			m.secView.selected = (m.secView.selected + 1) % len(m.secView.report.Findings)
			m.secView.expanded = false
			m.chatScrollOffset = 0
		}
		if m.secView.tab == secTabAtlas {
			events := secAtlasFilteredEvents(runtimeSecurity.Default().Events(), m.secView.atlasFilter)
			if len(events) > 0 {
				m.secView.atlasSelected = (m.secView.atlasSelected + 1) % len(events)
				m.secView.atlasExpanded = false
			}
		}
		if m.secView.tab == secTabRoutes && len(m.secView.report.Routes) > 0 {
			m.secView.selected = (m.secView.selected + 1) % len(m.secView.report.Routes)
			m.secView.expanded = false
			m.chatScrollOffset = 0
		}
		return m, nil, true
	case (keyIs(msg, tea.KeyUp) || keyText(msg) == "k") && m.composerValue() == "":
		if m.secView.tab == secTabFindings && len(m.secView.report.Findings) > 0 {
			m.secView.selected = (m.secView.selected - 1 + len(m.secView.report.Findings)) % len(m.secView.report.Findings)
			m.secView.expanded = false
			m.chatScrollOffset = 0
		}
		if m.secView.tab == secTabAtlas {
			events := secAtlasFilteredEvents(runtimeSecurity.Default().Events(), m.secView.atlasFilter)
			if len(events) > 0 {
				m.secView.atlasSelected = (m.secView.atlasSelected - 1 + len(events)) % len(events)
				m.secView.atlasExpanded = false
			}
		}
		if m.secView.tab == secTabRoutes && len(m.secView.report.Routes) > 0 {
			m.secView.selected = (m.secView.selected - 1 + len(m.secView.report.Routes)) % len(m.secView.report.Routes)
			m.secView.expanded = false
			m.chatScrollOffset = 0
		}
		return m, nil, true
	case keyIs(msg, tea.KeyEnter):
		if m.secView.tab == secTabFindings && len(m.secView.report.Findings) > 0 {
			m.secView.expanded = !m.secView.expanded
			return m, nil, true
		}
		if m.secView.tab == secTabAtlas && len(secAtlasFilteredEvents(runtimeSecurity.Default().Events(), m.secView.atlasFilter)) > 0 {
			m.secView.atlasExpanded = !m.secView.atlasExpanded
			return m, nil, true
		}
		if m.secView.tab == secTabRoutes && len(m.secView.report.Routes) > 0 {
			m.secView.expanded = !m.secView.expanded
			return m, nil, true
		}
		if m.secView.tab == secTabAdvisor {
			return m.submitSecAdvisorQuestion()
		}
		return m, nil, true
	}
	if m.secView.tab != secTabAdvisor || m.secView.advisor == nil || m.secView.advisorAsking {
		return m, nil, true
	}
	if next, ok := m.applyComposerKey(msg); ok {
		return next, nil, true
	}
	return m, nil, false
}

func secFixPrompt(finding sec.Finding, report sec.Report) string {
	location := finding.Path
	if finding.Line > 0 {
		location += fmt.Sprintf(":%d", finding.Line)
	}
	pathText := "none recorded"
	if len(finding.AttackPath) > 0 {
		pathText = strings.Join(finding.AttackPath, " -> ")
	}
	return fmt.Sprintf(`Fix security finding %s in the normal agent workflow.

This is a bounded, redacted handoff from /sec. Do not assume the finding is correct: inspect the real files and validate it first.

Finding ID: %s
Rule: %s
Severity: %s
Location: %s
Evidence description: %s
Risk path: %s
Service: %s
Owner: %s
Fix target: %s
Remediation: %s
Verification: %s

Inspect the real files, implement the smallest safe code or IaC fix, preserve unrelated changes, run relevant tests, and report exactly what changed. Do not commit, push, open a PR, or contact an owner automatically. Follow the normal sandbox and permission rules.`,
		finding.ID, finding.ID, finding.RuleID, finding.Severity, nonEmpty(location, "not recorded"),
		nonEmpty(finding.Evidence, finding.Description), nonEmpty(pathText, "none recorded"),
		nonEmpty(finding.Service, "unassigned"), nonEmpty(finding.Owner, "unowned"),
		nonEmpty(finding.FixTarget, location), finding.Remediation, finding.Verification)
}

func secOverviewAttackSurface(report sec.Report) string {
	configured, observed, potential := 0, 0, 0
	for _, exposure := range report.Exposures {
		switch exposure.State {
		case sec.ExposureConfiguredPublic:
			configured++
		case sec.ExposureRuntimeObserved:
			observed++
		case sec.ExposurePotential:
			potential++
		}
	}
	return fmt.Sprintf("%d route(s) · %d configured-public · %d runtime-observed · %d potential · %d threat-intel match(es) · %d structured node(s)", len(report.Routes), configured, observed, potential, report.ThreatIntel.Matches, report.Coverage.StructuredNodes)
}

func secOverviewPathSummary(report sec.Report) string {
	counts := map[sec.RiskPathClass]int{}
	for _, path := range report.RiskPaths {
		counts[path.Class]++
	}
	return fmt.Sprintf("%d total · initial %d · lateral %d · privilege %d · data %d · supply-chain %d", len(report.RiskPaths), counts[sec.RiskPathInitialAccess], counts[sec.RiskPathLateralMovement], counts[sec.RiskPathPrivilegeEscalation], counts[sec.RiskPathDataAccess], counts[sec.RiskPathSupplyChain])
}

func secOverviewFixSummary(report sec.Report) string {
	owned, ready := 0, 0
	for _, finding := range report.Findings {
		if finding.Owner != "" && finding.Owner != "unowned" {
			owned++
		}
		if finding.Path != "" && finding.Remediation != "" {
			ready++
		}
	}
	return fmt.Sprintf("%d owned · %d unowned · %d fix-ready · Press 2, select a risk, then x to fix with agent", owned, len(report.Findings)-owned, ready)
}

func secOverviewDefenseSummary(root string) string {
	guard := runtimeSecurity.Default().Summary()
	status := sensor.NewManager(root).Status()
	sandboxState := "policy-managed"
	if sandbox.IsAlreadySandboxed() {
		sandboxState = "active"
	}
	return fmt.Sprintf("Tool Guard enforced · Sandbox %s · eBPF %s · %d decisions · %d blocked · %d suspicious signal(s)", sandboxState, status.Mode, guard.RequestsOut, guard.Blocked, 0)
}

func (m model) setSecTab(tab secTab) model {
	m.secView.tab = tab
	m.secView.selected = 0
	m.secView.expanded = false
	m.secView.notice = ""
	m.chatScrollOffset = 0
	m.clearSuggestions()
	return m
}

func (m model) submitSecAdvisorQuestion() (model, tea.Cmd, bool) {
	if m.secView.advisor == nil || m.secView.advisorAsking {
		return m, nil, true
	}
	question := strings.TrimSpace(m.composerValue())
	if question == "" {
		question = "What should I fix first, why is it the priority, and how do I verify the fix?"
	}
	m.clearComposer()
	m.secView.notice = ""
	m.secView.advisorMessages = append(m.secView.advisorMessages, secAdvisorMessage{role: "user", text: question})
	m.secView.advisorAsking = true
	m.secView.advisorGeneration++
	generation := m.secView.advisorGeneration
	advisor := m.secView.advisor
	m.chatScrollOffset = 0
	return m, func() tea.Msg {
		answer, err := advisor.AskWithContext(context.Background(), question, m.secAdvisorSessionContext())
		return secAdvisorResultMsg{generation: generation, question: question, answer: answer, err: err}
	}, true
}

func (m model) applySecAdvisorResult(msg secAdvisorResultMsg) model {
	if !m.secView.active || msg.generation != m.secView.advisorGeneration {
		return m
	}
	m.secView.advisorAsking = false
	if msg.err != nil {
		m.secView.notice = "The advisor could not answer right now. Check the configured model and try again."
		return m
	}
	m.secView.advisorMessages = append(m.secView.advisorMessages, secAdvisorMessage{role: "advisor", text: msg.answer})
	m.chatScrollOffset = 0
	return m
}

func secScoreGauge(score, width int) string {
	width = maxInt(4, width)
	filled := clampInt(score*width/100, 0, width)
	return sonutTheme.accent.Render(strings.Repeat("━", filled)) +
		sonutTheme.line.Render(strings.Repeat("━", width-filled))
}

func secMetricLine(value int, label string, style lipgloss.Style) string {
	return style.Bold(true).Render(fmt.Sprintf("%d", value)) + sonutTheme.faint.Render(" "+label)
}

func secSection(title string, width int, info ...string) string {
	left := sonutTheme.ink.Bold(true).Render(title)
	right := ""
	if len(info) > 0 && strings.TrimSpace(info[0]) != "" {
		right = sonutTheme.faint.Render(info[0])
	}
	remaining := maxInt(1, width-lipgloss.Width(left)-lipgloss.Width(right)-2)
	return left + " " + sonutTheme.line.Render(strings.Repeat("─", remaining)) + func() string {
		if right == "" {
			return ""
		}
		return " " + right
	}()
}

func secSeverityStyle(severity sec.Severity) lipgloss.Style {
	switch severity {
	case sec.SeverityCritical:
		return sonutTheme.red
	case sec.SeverityHigh:
		return sonutTheme.amber
	case sec.SeverityMedium:
		return sonutTheme.blue
	case sec.SeverityLow:
		return sonutTheme.green
	default:
		return sonutTheme.faint
	}
}

func secPathLabel(id string) string {
	switch id {
	case "internet":
		return "Internet"
	case "supply-chain":
		return "Supply chain"
	case "sensitive-data":
		return "Sensitive data"
	case "runtime-identity":
		return "Runtime identity"
	case "ci-identity":
		return "CI write identity"
	default:
		return strings.TrimPrefix(id, "file:")
	}
}

func secReportPath(root string) string {
	return filepath.Join(root, filepath.FromSlash(sec.OutputDir), "report.json")
}
