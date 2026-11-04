package tui

import (
	"bytes"
	"encoding/base64"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"charm.land/lipgloss/v2"
)

const (
	// browserLandingURL is retained for recognizing the old placeholder URL in
	// browser results. It is never rendered as the in-app preview destination.
	browserLandingURL = "https://terminal-browser.com/"
	browserIdleTitle  = "atcode"
)

// browserState tracks the browser session the agent is controlling.
// Updated whenever a browser tool call or result passes through the transcript.
type browserState struct {
	url            string
	title          string
	status         string   // "idle", "navigating", "loading", "ready", "error"
	lastAction     string   // short human-readable description of what just happened
	actions        []string // rolling log of recent actions (newest last), capped at 20
	screenshot     string   // filesystem path of the last screenshot saved
	screenshotData []byte   // screenshot bytes attached directly by a browser tool
	lastSnapshot   string   // most recent browser snapshot result (accessibility tree)
	idlePage       bool     // true until the first browser action replaces the landing surface
	updatedAt      time.Time
}

func newBrowserState() browserState {
	return browserState{
		status:   "idle",
		idlePage: true,
	}
}

func (b *browserState) addAction(text string) {
	text = strings.TrimSpace(text)
	if text == "" {
		return
	}
	b.actions = append(b.actions, text)
	if len(b.actions) > 20 {
		b.actions = b.actions[len(b.actions)-20:]
	}
	b.lastAction = text
	b.updatedAt = time.Now()
}

// parseBrowserToolCall updates browser state from an agent tool call row.
// tool is the tool name; argsJSON is the raw JSON arguments string.
func (b *browserState) parseBrowserToolCall(tool, argsJSON string) {
	b.idlePage = false
	if b.title == browserIdleTitle {
		b.title = ""
	}

	// browser_action uses a "command" field to dispatch sub-actions.
	// Resolve the effective command first.
	effectiveTool := browserEffectiveTool(tool, argsJSON)
	if !isBrowserNavigate(effectiveTool) && b.url == browserLandingURL {
		b.url = ""
	}

	switch {
	case isBrowserNavigate(effectiveTool):
		u := browserToolArgument(argsJSON, "url", "uri", "target")
		if u == "" {
			u = inlineHTTPURL(argsJSON)
		}
		if u != "" {
			b.url = u
		} else if b.url == browserLandingURL {
			b.url = ""
		}
		b.status = "navigating"
		b.addAction("→ navigate " + truncateBrowserStr(u, 40))

	case isBrowserClick(effectiveTool):
		sel := extractJSONStringField(argsJSON, "selector")
		if sel == "" {
			sel = extractJSONStringField(argsJSON, "element")
		}
		if sel == "" {
			sel = extractJSONStringField(argsJSON, "coordinate")
		}
		b.status = "loading"
		b.addAction("⊙ click " + truncateBrowserStr(sel, 40))

	case isBrowserType(effectiveTool):
		sel := extractJSONStringField(argsJSON, "selector")
		val := extractJSONStringField(argsJSON, "text")
		if val == "" {
			val = extractJSONStringField(argsJSON, "value")
		}
		b.status = "loading"
		b.addAction("✎ type into " + truncateBrowserStr(sel, 20) + " → " + truncateBrowserStr(val, 20))

	case isBrowserScreenshot(effectiveTool):
		b.status = "loading"
		b.addAction("📸 screenshot")

	case isBrowserScroll(effectiveTool):
		dir := extractJSONStringField(argsJSON, "direction")
		if dir == "" {
			dir = "down"
		}
		b.addAction("↕ scroll " + dir)

	case isBrowserBack(effectiveTool):
		b.addAction("← back")

	case isBrowserWait(effectiveTool):
		b.addAction("⏳ wait")

	case isBrowserPress(effectiveTool):
		key := extractJSONStringField(argsJSON, "key")
		b.addAction("⌨ press " + truncateBrowserStr(key, 20))

	case isBrowserEval(effectiveTool) || isBrowserJS(effectiveTool):
		b.addAction("⚡ execute script")

	default:
		// Unknown browser sub-command — still record it so panel shows activity.
		if effectiveTool != tool && effectiveTool != "" {
			b.addAction("⊞ " + truncateBrowserStr(effectiveTool, 38))
		}
	}
}

// browserToolCallURL returns the destination from a navigate call, including
// Playwright MCP's mcp_<server>_browser_navigate naming. It is used only to
// mirror navigation into the terminal-browser pane; the original tool call
// remains the source of truth for automation.
func browserToolCallURL(tool, argsJSON string) string {
	effectiveTool := browserEffectiveTool(tool, argsJSON)
	if !isBrowserNavigate(effectiveTool) {
		return ""
	}
	if value := browserToolArgument(argsJSON, "url", "uri", "target"); value != "" {
		return value
	}
	return inlineHTTPURL(argsJSON)
}

// parseBrowserToolResult updates browser state from a tool result row.
// isError reports whether the underlying tool call failed, so the panel's
// status badge can render red instead of always claiming "ready".
func (b *browserState) parseBrowserToolResult(tool, detail string, isError bool) {
	b.parseBrowserToolResultWithImage(tool, detail, isError, nil)
}

func (b *browserState) parseBrowserToolResultWithImage(tool, detail string, isError bool, imageData []byte) {
	b.idlePage = false
	if b.title == browserIdleTitle {
		b.title = ""
	}
	if b.url == browserLandingURL {
		b.url = ""
	}

	if isError {
		b.status = "error"
	} else {
		b.status = "ready"
	}
	if t := extractTitle(detail); t != "" {
		b.title = t
	}
	if path := extractScreenshotPath(detail); path != "" {
		b.screenshot = path
		b.lastAction = "📸 screenshot → " + truncateBrowserStr(path, 36)
	}
	if len(imageData) > 0 {
		b.screenshot = ""
		b.screenshotData = append(b.screenshotData[:0], imageData...)
		b.lastAction = "📸 screenshot"
	}
	// Extract URL from result if present (some tools echo it back).
	// The initial landing URL is a placeholder, not the browser's current URL.
	// Allow the first result to replace it with the URL reported by the real
	// browser session (the old empty-only check left the pane stuck on the
	// terminal-browser landing page forever).
	if u := extractResultURL(detail); u != "" && (b.url == "" || b.url == browserLandingURL) {
		b.url = u
	}
	// Capture snapshot results for the live viewport panel. The detail field
	// holds the raw tool output; for snapshot calls it is the accessibility tree.
	if !isError && isBrowserSnapshotTool(tool) && strings.TrimSpace(detail) != "" {
		b.lastSnapshot = detail
	}
}

// browserToolRowArguments prefers the raw arguments retained on live and
// rehydrated tool rows. Older rows only have compact display hints, so callers
// can still recover URLs from those as a fallback.
func browserToolRowArguments(row transcriptRow) string {
	if strings.TrimSpace(row.rawArgs) != "" {
		return row.rawArgs
	}
	return strings.TrimSpace(strings.Join([]string{row.detail, row.arg}, " "))
}

func browserToolArgument(args string, keys ...string) string {
	for _, key := range keys {
		if value := extractJSONStringField(args, key); value != "" {
			return value
		}
	}
	return ""
}

func browserEffectiveTool(tool, args string) string {
	effectiveTool := tool
	if !isBrowserActionDispatch(tool) {
		return effectiveTool
	}
	cmd := browserToolArgument(args, "command", "action")
	if cmd == "" {
		// Compact rows from older sessions may contain "open https://…" rather
		// than the original JSON object. Recognize the command prefix there too.
		lower := strings.ToLower(strings.TrimSpace(args))
		for _, candidate := range []string{"navigate", "open", "click", "type", "fill", "input", "screenshot", "scroll", "back", "press", "wait", "evaluate", "eval", "snapshot"} {
			if lower == candidate || strings.HasPrefix(lower, candidate+" ") {
				cmd = candidate
				break
			}
		}
	}
	if cmd != "" {
		effectiveTool = cmd
	}
	return effectiveTool
}

// parseLocalFileBrowserHandoff records a browser navigation performed inside
// the registry while handling an outer `open file://…` shell call. The agent's
// visible transcript retains the shell card, but the sidebar must still show
// that the real browser session navigated rather than misleadingly staying idle.
func (b *browserState) parseLocalFileBrowserHandoff(detail string, isError bool) {
	b.parseLocalFileBrowserHandoffWithImage(detail, isError, nil)
}

func (b *browserState) parseLocalFileBrowserHandoffWithImage(detail string, isError bool, imageData []byte) {
	b.idlePage = false

	if isError {
		b.status = "error"
		return
	}
	hasImage := len(imageData) > 0
	if hasImage {
		b.screenshot = ""
		b.screenshotData = append(b.screenshotData[:0], imageData...)
	}
	b.status = "ready"
	if u := extractResultURL(detail); u == "" {
		u = inlineHTTPURL(detail)
		if u != "" {
			b.url = u
			b.addAction("→ opened local page " + truncateBrowserStr(u, 40))
			if hasImage {
				b.lastAction = "📸 screenshot"
			}
			return
		}
	} else {
		b.url = u
		b.addAction("→ opened local page " + truncateBrowserStr(u, 40))
		if hasImage {
			b.lastAction = "📸 screenshot"
		}
		return
	}
	b.addAction("→ opened local page in browser")
	if hasImage {
		b.lastAction = "📸 screenshot"
	}
}

func isLocalFileBrowserHandoff(detail string) bool {
	return strings.Contains(detail, "Opened local page in the visible browser at http://localhost:")
}

// inlineHTTPURL extracts the bounded URL from the registry's human-readable
// handoff sentence. General browser outputs retain the stricter line-oriented
// extractResultURL behavior above; this only supports the known local handoff.
func inlineHTTPURL(text string) string {
	start := strings.Index(text, "http://")
	if start < 0 {
		start = strings.Index(text, "https://")
	}
	if start < 0 {
		return ""
	}
	rest := text[start:]
	if end := strings.IndexAny(rest, " \t\r\n"); end >= 0 {
		rest = rest[:end]
	}
	return strings.TrimRight(rest, ".,;:)\"'}]")
}

// isBrowserSnapshotTool reports whether the tool name is a browser snapshot
// (accessibility tree) call — including playwright MCP prefixed variants.
func isBrowserSnapshotTool(t string) bool {
	if hasAny(t, "browser_snapshot", "snapshot") {
		return true
	}
	// playwright MCP: mcp_playwright_browser_snapshot
	return strings.HasPrefix(t, "mcp_") && strings.HasSuffix(t, "_browser_snapshot")
}

// renderBrowserPanel renders the browser preview panel as exactly `height` lines,
// each exactly `width` cells wide.
func (m model) renderBrowserPanel(width, height int) []string {
	if width <= 0 || height <= 0 {
		return nil
	}

	lines := make([]string, 0, height)
	add := func(s string) { lines = append(lines, s) }

	br := m.browserState

	// Header
	add(sidebarHeaderWithCount("BROWSER", browserStatusLabel(br.status), browserStatusStyle(br.status), width))

	if m.browserHeadful {
		add(" " + sonutTheme.accent.Render("◉") + " " + sonutTheme.faint.Render("headful · user-visible"))
	} else {
		add(" " + sonutTheme.faint.Render("○") + " " + sonutTheme.faint.Render("headless · agent-controlled"))
	}

	if br.url != "" {
		urlText := " " + sonutTheme.accent.Render("⊞") + " " + sonutTheme.faint.Render(truncateBrowserStr(br.url, width-4))
		add(urlText)
	}
	if br.title != "" {
		add(" " + sonutTheme.faint.Render(truncateBrowserStr(br.title, width-2)))
	}

	// Inline image preview (iTerm2 / Kitty protocol).
	// Reserve imgLines rows for the image; the escape sequence itself is one string
	// but the terminal renders it at that visual height.
	visualRendered := false
	if len(br.screenshotData) > 0 || br.screenshot != "" {
		imgLines := height - len(lines) - 3 // leave 3 rows for ACTIONS header + 2 action rows
		if imgLines < 4 {
			imgLines = 4
		}
		if imgLines > height-len(lines)-1 {
			imgLines = height - len(lines) - 1
		}
		if imgLines > 0 {
			var imageLines []string
			if len(br.screenshotData) > 0 {
				imageLines = renderBrowserImageData(br.screenshotData, width, imgLines)
			} else {
				imageLines = renderBrowserImagePath(br.screenshot, width, imgLines)
			}
			if len(imageLines) > 0 {
				add("") // spacer
				for _, line := range imageLines {
					add(line)
				}
				visualRendered = true
				// Pad the visual rows the image occupies so our line count stays accurate.
				for i := len(imageLines); i < imgLines; i++ {
					add("")
				}
			}
		}
	}

	// Live viewport: last browser snapshot (accessibility tree). Shows what the
	// agent currently "sees" in the browser — updates on every snapshot call.
	// Capped at 15 lines so the ACTIONS log still has room below it.
	if !visualRendered && br.lastSnapshot != "" {
		const maxSnapshotLines = 15
		add(sidebarHeader("VISION", width))
		snapshotLines := strings.SplitAfter(br.lastSnapshot, "\n")
		shown := snapshotLines
		if len(shown) > maxSnapshotLines {
			shown = shown[:maxSnapshotLines]
		}
		for _, sl := range shown {
			sl = strings.TrimRight(sl, "\n\r")
			if sl == "" {
				continue
			}
			add(" " + sonutTheme.faint.Render(truncateBrowserStr(sl, width-2)))
		}
		if len(snapshotLines) > maxSnapshotLines {
			add(" " + sonutTheme.faint.Render(fmt.Sprintf("… (%d more lines)", len(snapshotLines)-maxSnapshotLines)))
		}
		visualRendered = true
	}

	if !visualRendered {
		// A browser result may contain no usable screenshot or snapshot (for
		// example while a page is still loading). Keep the preview useful with
		// the real project logo instead of fabricated page text or an empty pane.
		if renderBrowserLogoFallback(&lines, width, height) {
			return normalizeBrowserPanelLines(lines, width, height)
		}
	}

	// Safety: permission mode + recent approval/denial decisions. Always
	// visible so the panel shows the sandbox posture even with no browser use.
	for _, line := range m.renderSafetyLines(width, 6) {
		add(line)
	}

	// Recent actions log
	add(sidebarHeader("ACTIONS", width))

	if len(br.actions) == 0 {
		add(sidebarPlaceholder("no browser activity", width))
	} else {
		available := height - len(lines)
		if available < 1 {
			available = 1
		}
		start := 0
		if len(br.actions) > available {
			start = len(br.actions) - available
		}
		for _, action := range br.actions[start:] {
			add(" " + sonutTheme.faint.Render(truncateBrowserStr(action, width-2)))
		}
	}

	return normalizeBrowserPanelLines(lines, width, height)
}

// renderBrowserLogoFallback draws the project's logo when the browser has not
// produced a visual preview yet. The asset is searched relative to the current
// workspace and executable so it works both from a checkout and from an
// install that keeps logo.png next to the binary.
func renderBrowserLogoFallback(lines *[]string, width, height int) bool {
	path := browserLogoAssetPath()
	if path == "" {
		return false
	}

	available := height - len(*lines)
	if available <= 0 {
		return false
	}
	spacer := 0
	if available > 1 {
		spacer = 1
	}
	imgLines := available - spacer
	imageLines := renderBrowserImagePath(path, width, imgLines)
	if len(imageLines) == 0 {
		return false
	}

	if spacer > 0 {
		*lines = append(*lines, "")
	}
	*lines = append(*lines, imageLines...)
	for i := len(imageLines); i < imgLines; i++ {
		*lines = append(*lines, "")
	}
	return true
}

func browserLogoAssetPath() string {
	var candidates []string
	if cwd, err := os.Getwd(); err == nil {
		candidates = append(candidates, browserLogoCandidates(cwd)...)
	}
	if executable, err := os.Executable(); err == nil {
		candidates = append(candidates, browserLogoCandidates(filepath.Dir(executable))...)
	}

	for _, candidate := range candidates {
		info, err := os.Stat(candidate)
		if err == nil && info.Mode().IsRegular() {
			return candidate
		}
	}
	return ""
}

func browserLogoCandidates(start string) []string {
	start, err := filepath.Abs(start)
	if err != nil {
		return nil
	}

	candidates := make([]string, 0, 16)
	dir := filepath.Clean(start)
	for {
		candidates = append(candidates,
			filepath.Join(dir, "cmd", "logo.png"),
			filepath.Join(dir, "logo.png"),
		)
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
		dir = parent
	}
	return candidates
}

func normalizeBrowserPanelLines(lines []string, width, height int) []string {
	for len(lines) < height {
		lines = append(lines, "")
	}
	if len(lines) > height {
		lines = lines[:height]
	}

	// Normalize every row to exactly width cells. Skip normalization for the
	// image line (it contains raw escape sequences).
	for i := range lines {
		if !strings.Contains(lines[i], "\x1b]1337;") && !strings.Contains(lines[i], "\x1b_Ga=") {
			lines[i] = padStyledLine(lines[i], width)
		}
	}
	return lines
}

// renderInlineImage renders an image file as an iTerm2 inline image escape sequence.
// widthCells is the terminal column width; heightRows is the number of terminal rows to use.
// Returns "" if the file can't be read or the terminal doesn't support it.
func renderInlineImage(path string, widthCells, heightRows int) string {
	data, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	return renderInlineImageData(data, widthCells, heightRows)
}

func renderInlineImageData(data []byte, widthCells, heightRows int) string {
	if len(data) == 0 {
		return ""
	}
	encoded := base64.StdEncoding.EncodeToString(data)
	// iTerm2 inline image protocol:
	// ESC ] 1337 ; File=inline=1;width=<w>px;height=<h>;<params>:<base64> BEL
	// We specify width in cells and height in rows so the image fits the panel.
	return fmt.Sprintf("\x1b]1337;File=inline=1;width=%d;height=%d;preserveAspectRatio=1:%s\x07",
		widthCells, heightRows, encoded)
}

// renderBrowserImageData renders browser images as truecolor terminal cells.
// This is deliberately the primary path for the in-app pane: OSC inline-image
// sequences are ignored by some terminal widgets, which otherwise leaves the
// browser panel looking empty. The OSC form remains a compatibility fallback
// for malformed or unsupported image data.
func renderBrowserImageData(data []byte, widthCells, heightRows int) []string {
	if lines := renderTerminalImageData(data, widthCells, heightRows); len(lines) > 0 {
		return lines
	}
	if inline := renderInlineImageData(data, widthCells, heightRows); inline != "" {
		return []string{inline}
	}
	return nil
}

func renderBrowserImagePath(path string, widthCells, heightRows int) []string {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	return renderBrowserImageData(data, widthCells, heightRows)
}

// renderTerminalImageData converts a PNG into smoothed half-block truecolor
// rows. A terminal cell is roughly twice as tall as it is wide, so the source
// is mapped to a 2:1 cell grid. Each half averages its source area to avoid the
// checkerboard artifacts caused by nearest-neighbour sampling, while remaining
// independent of Kitty/iTerm2 support.
func renderTerminalImageData(data []byte, widthCells, heightRows int) []string {
	if len(data) == 0 || widthCells <= 0 || heightRows <= 0 {
		return nil
	}

	img, err := png.Decode(bytes.NewReader(data))
	if err != nil {
		return nil
	}
	bounds := img.Bounds()
	srcWidth, srcHeight := bounds.Dx(), bounds.Dy()
	if srcWidth <= 0 || srcHeight <= 0 {
		return nil
	}

	artWidth := minInt(widthCells, maxInt(1, heightRows*2))
	artHeight := heightRows * 2
	left := (widthCells - artWidth) / 2
	background := terminalImageColor(sonutTheme.bgPanel)
	lines := make([]string, 0, heightRows)

	for row := 0; row < heightRows; row++ {
		var line strings.Builder
		line.Grow(widthCells * 20)
		line.WriteString(strings.Repeat(" ", left))
		for col := 0; col < artWidth; col++ {
			x0 := bounds.Min.X + col*srcWidth/artWidth
			x1 := bounds.Min.X + (col+1)*srcWidth/artWidth
			topY0 := bounds.Min.Y + (row*2)*srcHeight/artHeight
			midY := bounds.Min.Y + (row*2+1)*srcHeight/artHeight
			bottomY := bounds.Min.Y + (row*2+2)*srcHeight/artHeight
			top := averageTerminalImagePixel(img, x0, x1, topY0, midY, background)
			bottom := averageTerminalImagePixel(img, x0, x1, midY, bottomY, background)

			line.WriteString("\x1b[38;2;")
			line.WriteString(strconv.Itoa(int(top.R)))
			line.WriteByte(';')
			line.WriteString(strconv.Itoa(int(top.G)))
			line.WriteByte(';')
			line.WriteString(strconv.Itoa(int(top.B)))
			line.WriteString("m\x1b[48;2;")
			line.WriteString(strconv.Itoa(int(bottom.R)))
			line.WriteByte(';')
			line.WriteString(strconv.Itoa(int(bottom.G)))
			line.WriteByte(';')
			line.WriteString(strconv.Itoa(int(bottom.B)))
			line.WriteString("m▀")
		}
		line.WriteString("\x1b[0m")
		line.WriteString(strings.Repeat(" ", widthCells-left-artWidth))
		lines = append(lines, line.String())
	}
	return lines
}

func terminalImageColor(c color.Color) color.RGBA {
	if c == nil {
		return color.RGBA{R: 10, G: 12, B: 18, A: 255}
	}
	r, g, b, _ := c.RGBA()
	return color.RGBA{R: uint8(r >> 8), G: uint8(g >> 8), B: uint8(b >> 8), A: 255}
}

func averageTerminalImagePixel(img image.Image, x0, x1, y0, y1 int, background color.RGBA) color.RGBA {
	if x1 <= x0 {
		x1 = x0 + 1
	}
	if y1 <= y0 {
		y1 = y0 + 1
	}

	var red, green, blue float64
	count := 0.0
	for y := y0; y < y1; y++ {
		for x := x0; x < x1; x++ {
			r, g, b, a := img.At(x, y).RGBA()
			alpha := float64(a) / 65535
			red += (float64(r)/257)*alpha + float64(background.R)*(1-alpha)
			green += (float64(g)/257)*alpha + float64(background.G)*(1-alpha)
			blue += (float64(b)/257)*alpha + float64(background.B)*(1-alpha)
			count++
		}
	}
	if count == 0 {
		return background
	}
	return color.RGBA{
		R: uint8(red / count),
		G: uint8(green / count),
		B: uint8(blue / count),
		A: 255,
	}
}

// renderBrowserPanelDivider returns a single divider line for the browser panel.
func renderBrowserPanelDivider(width int) string {
	return sonutTheme.line.Render(strings.Repeat("─", width))
}

func browserStatusLabel(status string) string {
	switch status {
	case "navigating", "loading":
		return "loading"
	case "ready":
		return "ready"
	case "error":
		return "error"
	default:
		return "idle"
	}
}

func browserStatusStyle(status string) lipgloss.Style {
	switch status {
	case "navigating", "loading":
		return sonutTheme.amber
	case "ready":
		return sonutTheme.green
	case "error":
		return sonutTheme.red
	default:
		return sonutTheme.faint
	}
}

// --- tool name classifiers ---

// isBrowserActionDispatch returns true for tools that use a "command" sub-field
// to dispatch different browser actions (e.g. browser_action, web_browser).
func isBrowserActionDispatch(t string) bool {
	return hasAny(t, "browser_action", "web_browser", "browser", "computer_browser", "action")
}

func isBrowserNavigate(t string) bool {
	return hasAny(t, "navigate", "browser_navigate", "goto", "open_url", "open", "open_browser", "browser_open", "launch") ||
		strings.HasSuffix(t, "_browser_navigate")
}

func isBrowserClick(t string) bool {
	return hasAny(t, "click", "browser_click", "tap", "left_click")
}

func isBrowserType(t string) bool {
	return hasAny(t, "type", "browser_type", "fill", "input_text", "keyboard_type", "key", "input")
}

func isBrowserScreenshot(t string) bool {
	return hasAny(t, "screenshot", "browser_screenshot", "take_screenshot", "capture")
}

func isBrowserScroll(t string) bool {
	return hasAny(t, "scroll", "browser_scroll", "scroll_down", "scroll_up")
}

func isBrowserBack(t string) bool {
	return hasAny(t, "back", "browser_back", "go_back", "history_back")
}

func isBrowserPress(t string) bool {
	return hasAny(t, "press", "browser_press", "key_press")
}

func isBrowserWait(t string) bool {
	return hasAny(t, "wait", "browser_wait", "wait_for", "sleep", "wait_for_selector")
}

func isBrowserEval(t string) bool {
	return hasAny(t, "evaluate", "browser_evaluate", "eval")
}

func isBrowserJS(t string) bool {
	return hasAny(t, "execute_javascript", "browser_js", "run_javascript", "execute_script")
}

func isBrowserTool(t string) bool {
	return isBrowserActionDispatch(t) ||
		isBrowserNavigate(t) || isBrowserClick(t) || isBrowserType(t) ||
		isBrowserScreenshot(t) || isBrowserScroll(t) || isBrowserBack(t) ||
		isBrowserWait(t) || isBrowserPress(t) || isBrowserEval(t) || isBrowserJS(t) ||
		isBrowserSnapshotTool(t) ||
		strings.HasPrefix(t, "browser_") || strings.HasPrefix(t, "web_browser") ||
		// playwright MCP tools register as mcp_playwright_browser_<action>
		(strings.HasPrefix(t, "mcp_") && strings.Contains(t, "_browser_"))
}

func hasAny(s string, candidates ...string) bool {
	for _, c := range candidates {
		if s == c {
			return true
		}
	}
	return false
}

// --- text helpers ---

func truncateBrowserStr(s string, limit int) string {
	if limit <= 0 {
		return ""
	}
	s = strings.TrimSpace(s)
	runes := []rune(s)
	if len(runes) <= limit {
		return s
	}
	if limit <= 1 {
		return "…"
	}
	return string(runes[:limit-1]) + "…"
}

func extractJSONStringField(json, key string) string {
	// Minimal extraction: find `"key":"value"` or `"key": "value"` patterns
	needle := fmt.Sprintf(`"%s"`, key)
	idx := strings.Index(json, needle)
	if idx < 0 {
		return ""
	}
	rest := json[idx+len(needle):]
	// Skip whitespace and colon
	rest = strings.TrimLeft(rest, " \t\r\n:")
	if len(rest) == 0 || rest[0] != '"' {
		return ""
	}
	rest = rest[1:]
	// Read until closing unescaped quote
	var sb strings.Builder
	for i := 0; i < len(rest); {
		r, size := utf8.DecodeRuneInString(rest[i:])
		if r == '\\' && i+size < len(rest) {
			next, ns := utf8.DecodeRuneInString(rest[i+size:])
			sb.WriteRune(next)
			i += size + ns
			continue
		}
		if r == '"' {
			break
		}
		sb.WriteRune(r)
		i += size
	}
	return sb.String()
}

func extractTitle(text string) string {
	for _, line := range strings.Split(text, "\n") {
		if strings.HasPrefix(line, "Title:") || strings.HasPrefix(line, "title:") {
			return strings.TrimSpace(line[6:])
		}
		if strings.Contains(line, "<title>") {
			start := strings.Index(line, "<title>") + 7
			end := strings.Index(line, "</title>")
			if end > start {
				return strings.TrimSpace(line[start:end])
			}
		}
	}
	return ""
}

func extractResultURL(text string) string {
	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "URL:") || strings.HasPrefix(line, "url:") {
			u := strings.TrimSpace(line[4:])
			if strings.HasPrefix(u, "http") {
				return u
			}
		}
		if strings.HasPrefix(line, "http://") || strings.HasPrefix(line, "https://") {
			// bare URL line in result
			if !strings.Contains(line, " ") {
				return line
			}
		}
	}
	return ""
}

func extractScreenshotPath(text string) string {
	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimSpace(line)
		if strings.HasSuffix(line, ".png") || strings.HasSuffix(line, ".jpg") || strings.HasSuffix(line, ".jpeg") {
			return line
		}
		if strings.HasPrefix(line, "Saved:") || strings.HasPrefix(line, "saved:") {
			return strings.TrimSpace(line[6:])
		}
		if strings.HasPrefix(line, "Screenshot:") {
			return strings.TrimSpace(line[11:])
		}
	}
	return ""
}
