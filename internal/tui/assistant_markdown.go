package tui

import (
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"unicode"
	"unicode/utf8"

	"charm.land/lipgloss/v2"
)

const (
	markdownTableColumnSeparator = " │ "
	markdownTableMinColumnWidth  = 4
	markdownTableHeaderMaxWidth  = 18
	// Separate body rows with horizontal rules when the table is taller than this.
	markdownTableRuleBodyRows = 4
	markdownBoldStart         = "\x1b[1m"
	markdownBoldEnd           = "\x1b[22m"
	markdownItalicStart       = "\x1b[3m"
	markdownItalicEnd         = "\x1b[23m"
)

type markdownDisplayStyle int

const (
	markdownDisplayNormal markdownDisplayStyle = iota
	markdownDisplayBold
	markdownDisplayItalic
	markdownDisplayBoldItalic
	markdownDisplayRule
)

type markdownTableAlignment int

const (
	markdownTableAlignLeft markdownTableAlignment = iota
	markdownTableAlignCenter
	markdownTableAlignRight
)

func renderAssistantMarkdownText(text string, proseMeasure int, tableMeasure int, allowHighlight bool) []string {
	if proseMeasure < 16 {
		proseMeasure = 16
	}
	if tableMeasure < proseMeasure {
		tableMeasure = proseMeasure
	}
	text = stripXMLControlBlocks(text)
	raw := strings.Split(strings.TrimRight(strings.ReplaceAll(strings.ReplaceAll(text, "\r\n", "\n"), "\r", "\n"), "\n"), "\n")
	if len(raw) == 0 {
		return []string{""}
	}
	if allowHighlight && looksLikeBareCodeBlock(raw) {
		if highlighted, ok := highlightCodeAuto(raw, "", tableMeasure); ok {
			return trimMarkdownDisplayBlankEdges(wrapCodeBlockWithHeader(highlighted, raw, "", tableMeasure))
		}
	}

	lines := []string{}
	// blankBefore inserts one separator blank line before a block (heading or
	// paragraph) for vertical breathing room, but never doubles an existing blank
	// and never leads with one (lines is empty at the top; trimMarkdownDisplay-
	// BlankEdges strips any leading blank anyway). Lists, code, and tables are left
	// tight — they don't call it.
	blankBefore := func() {
		if len(lines) > 0 && strings.TrimSpace(lines[len(lines)-1]) != "" {
			lines = append(lines, "")
		}
	}
	for index := 0; index < len(raw); {
		line := raw[index]
		trimmed := strings.TrimSpace(line)

		if trimmed == "" {
			lines = append(lines, "")
			index++
			continue
		}

		if fence, ok := markdownFenceMarker(trimmed); ok {
			info := strings.TrimSpace(strings.TrimPrefix(trimmed, fence))
			lang := ""
			if fields := strings.Fields(info); len(fields) > 0 {
				lang = fields[0] // first token only: "```go title=x" -> "go"
			}
			code := []string{}
			index++
			for index < len(raw) {
				if closing, ok := markdownFenceMarker(strings.TrimSpace(raw[index])); ok && closing == fence {
					index++
					break
				}
				code = append(code, strings.ReplaceAll(raw[index], "\t", "    "))
				index++
			}
			if allowHighlight {
				if highlighted, ok := highlightCodeAuto(code, lang, tableMeasure); ok {
					lines = append(lines, wrapCodeBlockWithHeader(highlighted, code, lang, tableMeasure)...)
					continue
				}
			}
			lines = append(lines, wrapCodeBlockWithHeader(renderMarkdownCodeBlock(code, tableMeasure), code, lang, tableMeasure)...)
			continue
		}

		if isMarkdownImageLine(trimmed) {
			lines = append(lines, renderMarkdownImageLines(trimmed, proseMeasure)...)
			index++
			continue
		}

		if index+1 < len(raw) && isMarkdownTableHeader(line, raw[index+1]) {
			tableRows := [][]string{parseMarkdownTableRow(line)}
			alignments := parseMarkdownTableAlignments(raw[index+1])
			index += 2
			for index < len(raw) && isMarkdownTableRow(raw[index]) {
				tableRows = append(tableRows, parseMarkdownTableRow(raw[index]))
				index++
			}
			lines = append(lines, renderMarkdownTable(tableRows, alignments, tableMeasure)...)
			continue
		}

		if isMarkdownHorizontalRule(trimmed) {
			blankBefore()
			lines = append(lines, renderMarkdownHorizontalRule(proseMeasure))
			index++
			continue
		}

		if heading := markdownHeadingText(trimmed); heading != "" {
			blankBefore()
			// Headings are distinguished by weight + underline, not a bright colour —
			// calm for dark-mode terminals (ink, the body colour, not the lime accent).
			headingStyle := sonutTheme.ink.Bold(true).Underline(true)
			plain := strings.ReplaceAll(strings.ReplaceAll(heading, "**", ""), "`", "")
			for _, hl := range wrapPlainText(plain, proseMeasure) {
				lines = append(lines, headingStyle.Render(hl))
			}
			index++
			continue
		}

		if isMarkdownListLine(trimmed) || strings.HasPrefix(trimmed, ">") {
			lines = append(lines, wrapMarkdownInline(renderMarkdownStandaloneLine(line), proseMeasure)...)
			index++
			continue
		}

		if allowHighlight && looksLikeBareCodeLine(line) {
			code := []string{}
			probe := index
			for probe < len(raw) {
				next := raw[probe]
				nextTrimmed := strings.TrimSpace(next)
				if nextTrimmed == "" || markdownStartsBlock(raw, probe) || !looksLikeBareCodeLine(next) {
					break
				}
				code = append(code, strings.ReplaceAll(next, "\t", "    "))
				probe++
			}
			if looksLikeBareCodeBlock(code) {
				if highlighted, ok := highlightCodeAuto(code, "", tableMeasure); ok {
					lines = append(lines, wrapCodeBlockWithHeader(highlighted, code, "", tableMeasure)...)
					index = probe
					continue
				}
			}
		}

		blankBefore()
		paragraph := []string{strings.TrimSpace(line)}
		index++
		for index < len(raw) {
			next := raw[index]
			nextTrimmed := strings.TrimSpace(next)
			if nextTrimmed == "" || markdownStartsBlock(raw, index) {
				break
			}
			paragraph = append(paragraph, nextTrimmed)
			index++
		}
		lines = append(lines, wrapMarkdownInline(strings.Join(paragraph, " "), proseMeasure)...)
	}

	return trimMarkdownDisplayBlankEdges(lines)
}

func renderStreamingAssistantMarkdownText(text string, proseMeasure int, tableMeasure int) []string {
	stablePrefix := streamingMarkdownStablePrefix(text)
	if defaultRenderCache == nil {
		return renderAssistantMarkdownText(stablePrefix, proseMeasure, tableMeasure, true)
	}
	key := streamingMarkdownRenderCacheKey(stablePrefix, proseMeasure, tableMeasure)
	rendered := defaultRenderCache.render(key, true, func() string {
		return strings.Join(renderAssistantMarkdownText(stablePrefix, proseMeasure, tableMeasure, true), "\n")
	})
	return viewLines(rendered)
}

func streamingMarkdownRenderCacheKey(text string, proseMeasure int, tableMeasure int) string {
	sum := sha256.Sum256([]byte(text))
	return fmt.Sprintf("stream-md-v1:%d:%d:%x", proseMeasure, tableMeasure, sum)
}

func streamingMarkdownStablePrefix(text string) string {
	text = strings.ReplaceAll(strings.ReplaceAll(text, "\r\n", "\n"), "\r", "\n")
	offset := 0
	openFenceStart := -1
	openFence := ""
	for offset <= len(text) {
		lineStart := offset
		next := strings.IndexByte(text[offset:], '\n')
		line := ""
		if next < 0 {
			line = text[offset:]
			offset = len(text) + 1
		} else {
			line = text[offset : offset+next]
			offset += next + 1
		}
		if fence, ok := markdownFenceMarker(strings.TrimSpace(line)); ok {
			if openFence == "" {
				openFence = fence
				openFenceStart = lineStart
			} else if fence == openFence {
				openFence = ""
				openFenceStart = -1
			}
		}
		if next < 0 {
			break
		}
	}
	if openFenceStart >= 0 {
		return strings.TrimRight(text[:openFenceStart], "\n")
	}
	return text
}

func renderAssistantMarkdownPlainText(text string, proseMeasure int, tableMeasure int) []string {
	lines := renderAssistantMarkdownText(text, proseMeasure, tableMeasure, false)
	for index := range lines {
		lines[index] = stripMarkdownRenderControls(lines[index])
	}
	return lines
}

func stripMarkdownRenderControls(text string) string {
	text = strings.ReplaceAll(text, markdownBoldStart, "")
	text = strings.ReplaceAll(text, markdownBoldEnd, "")
	text = strings.ReplaceAll(text, markdownItalicStart, "")
	return strings.ReplaceAll(text, markdownItalicEnd, "")
}

func styleAssistantMarkdownLine(line string, base lipgloss.Style) string {
	if hasExternalANSIStyle(line) {
		return line
	}

	var builder strings.Builder
	style := markdownDisplayNormal
	var run strings.Builder

	flush := func() {
		if run.Len() == 0 {
			return
		}
		text := run.String()
		switch style {
		case markdownDisplayBold:
			// Emphasis is weight-only (no colour) — dark-mode-friendly and calm.
			builder.WriteString(sonutTheme.ink.Bold(true).Render(text))
		case markdownDisplayItalic:
			builder.WriteString(sonutTheme.ink.Italic(true).Render(text))
		case markdownDisplayBoldItalic:
			builder.WriteString(sonutTheme.ink.Bold(true).Italic(true).Render(text))
		case markdownDisplayRule:
			builder.WriteString(sonutTheme.lineStrong.Render(text))
		default:
			builder.WriteString(base.Render(text))
		}
		run.Reset()
	}

	boldOn := false
	italicOn := false
	applyStyle := func() markdownDisplayStyle {
		switch {
		case boldOn && italicOn:
			return markdownDisplayBoldItalic
		case boldOn:
			return markdownDisplayBold
		case italicOn:
			return markdownDisplayItalic
		default:
			return markdownDisplayNormal
		}
	}

	for index := 0; index < len(line); {
		switch {
		case strings.HasPrefix(line[index:], markdownBoldStart):
			flush()
			boldOn = true
			style = applyStyle()
			index += len(markdownBoldStart)
			continue
		case strings.HasPrefix(line[index:], markdownBoldEnd):
			flush()
			boldOn = false
			style = applyStyle()
			index += len(markdownBoldEnd)
			continue
		case strings.HasPrefix(line[index:], markdownItalicStart):
			flush()
			italicOn = true
			style = applyStyle()
			index += len(markdownItalicStart)
			continue
		case strings.HasPrefix(line[index:], markdownItalicEnd):
			flush()
			italicOn = false
			style = applyStyle()
			index += len(markdownItalicEnd)
			continue
		case line[index] == '\x1b':
			// Already-styled input (highlighted code, headings, tables) carries real
			// ANSI. Emit each escape verbatim instead of treating its bytes as runes
			// and re-Render()ing them: re-wrapping doubles the SGR density and a
			// flush()/truncate can then slice mid-escape, leaking "[38;2;…" / "[1;4;…"
			// fragments into the visible text. Mirrors truncateStyledLine. The bold
			// markers above are matched first, so their semantic style switch is kept.
			if end := ansiSequenceEnd(line, index); end > index {
				flush()
				builder.WriteString(line[index:end])
				index = end
				continue
			}
		}

		r, size := utf8.DecodeRuneInString(line[index:])
		if r == utf8.RuneError && size == 0 {
			break
		}
		nextStyle := markdownDisplayStyleForRune(r, style)
		if nextStyle != style {
			flush()
			style = nextStyle
		}
		run.WriteRune(r)
		index += size
	}
	flush()
	return builder.String()
}

func hasExternalANSIStyle(line string) bool {
	for index := 0; index < len(line); {
		if line[index] != '\x1b' {
			index++
			continue
		}
		end := ansiSequenceEnd(line, index)
		if end <= index {
			index++
			continue
		}
		seq := line[index:end]
		if seq != markdownBoldStart && seq != markdownBoldEnd &&
			seq != markdownItalicStart && seq != markdownItalicEnd {
			return true
		}
		index = end
	}
	return false
}

func markdownDisplayStyleForRune(r rune, current markdownDisplayStyle) markdownDisplayStyle {
	switch r {
	case '│', '─', '┼', '╭', '╮', '╰', '╯', '├', '┤', '┬', '┴':
		return markdownDisplayRule
	default:
		if current == markdownDisplayRule {
			return markdownDisplayNormal
		}
		return current
	}
}

func markdownStartsBlock(lines []string, index int) bool {
	if index >= len(lines) {
		return false
	}
	trimmed := strings.TrimSpace(lines[index])
	if trimmed == "" {
		return true
	}
	if _, ok := markdownFenceMarker(trimmed); ok {
		return true
	}
	if markdownHeadingText(trimmed) != "" || isMarkdownHorizontalRule(trimmed) || isMarkdownListLine(trimmed) || strings.HasPrefix(trimmed, ">") {
		return true
	}
	return index+1 < len(lines) && isMarkdownTableHeader(lines[index], lines[index+1])
}

func isMarkdownHorizontalRule(trimmed string) bool {
	if trimmed == "" {
		return false
	}
	marker := rune(0)
	count := 0
	for _, r := range trimmed {
		if r == ' ' || r == '\t' {
			continue
		}
		if r != '-' && r != '_' && r != '*' {
			return false
		}
		if marker == 0 {
			marker = r
		} else if marker != r {
			return false
		}
		count++
	}
	return count >= 3
}

func renderMarkdownHorizontalRule(measure int) string {
	width := clampInt(measure, 16, 96)
	return strings.Repeat("─", width)
}

func looksLikeBareCodeLine(line string) bool {
	trimmed := strings.TrimSpace(line)
	if trimmed == "" {
		return false
	}
	switch {
	case strings.HasPrefix(trimmed, "from ") && strings.Contains(trimmed, " import "):
		return true
	case strings.HasPrefix(trimmed, "import ") && !strings.Contains(trimmed, " from "):
		return true
	case strings.HasPrefix(trimmed, "def ") && strings.HasSuffix(trimmed, ":"):
		return true
	case strings.HasPrefix(trimmed, "class ") && strings.HasSuffix(trimmed, ":"):
		return true
	case strings.HasPrefix(trimmed, "if ") && strings.HasSuffix(trimmed, ":"):
		return true
	case strings.HasPrefix(trimmed, "elif ") && strings.HasSuffix(trimmed, ":"):
		return true
	case trimmed == "else:" || trimmed == "try:" || trimmed == "finally:":
		return true
	case strings.HasPrefix(trimmed, "for ") && strings.HasSuffix(trimmed, ":"):
		return true
	case strings.HasPrefix(trimmed, "while ") && strings.HasSuffix(trimmed, ":"):
		return true
	case strings.HasPrefix(trimmed, "with ") && strings.HasSuffix(trimmed, ":"):
		return true
	case strings.HasPrefix(trimmed, "except") && strings.HasSuffix(trimmed, ":"):
		return true
	case strings.HasPrefix(trimmed, "return "):
		return true
	case strings.HasPrefix(trimmed, "print("):
		return true
	case isSimpleFunctionCall(trimmed):
		return true
	case strings.HasPrefix(trimmed, "package "):
		return true
	case strings.HasPrefix(trimmed, "func ") && strings.Contains(trimmed, "{"):
		return true
	case strings.HasPrefix(trimmed, "const "), strings.HasPrefix(trimmed, "let "), strings.HasPrefix(trimmed, "var "):
		return true
	case strings.HasPrefix(trimmed, "function ") && strings.Contains(trimmed, "{"):
		return true
	case strings.HasPrefix(trimmed, "<!DOCTYPE "), strings.HasPrefix(trimmed, "<html"), strings.HasPrefix(trimmed, "<div"), strings.HasPrefix(trimmed, "<span"):
		return true
	}
	return false
}

func looksLikeBareCodeBlock(lines []string) bool {
	nonBlank := 0
	codeLike := 0
	strongSignals := 0
	for _, line := range lines {
		if strings.TrimSpace(line) == "" {
			continue
		}
		nonBlank++
		if isIndentedCodeContinuation(line) {
			codeLike++
			strongSignals++
			continue
		}
		if looksLikeBareCodeLine(line) {
			codeLike++
			if looksLikeStrongBareCodeLine(line) {
				strongSignals++
			}
		}
	}
	return nonBlank >= 2 && codeLike == nonBlank && strongSignals > 0
}

func looksLikeStrongBareCodeLine(line string) bool {
	trimmed := strings.TrimSpace(line)
	switch {
	case strings.HasPrefix(trimmed, "from ") && strings.Contains(trimmed, " import "):
		return true
	case strings.HasPrefix(trimmed, "import ") && !strings.Contains(trimmed, " from "):
		return true
	case strings.HasPrefix(trimmed, "def ") && strings.HasSuffix(trimmed, ":"):
		return true
	case strings.HasPrefix(trimmed, "class ") && strings.HasSuffix(trimmed, ":"):
		return true
	case strings.HasPrefix(trimmed, "print("):
		return true
	case isSimpleFunctionCall(trimmed):
		return true
	case strings.HasPrefix(trimmed, "package "):
		return true
	case strings.HasPrefix(trimmed, "func ") && strings.Contains(trimmed, "{"):
		return true
	case strings.HasPrefix(trimmed, "const "), strings.HasPrefix(trimmed, "let "), strings.HasPrefix(trimmed, "var "):
		return true
	case strings.HasPrefix(trimmed, "function ") && strings.Contains(trimmed, "{"):
		return true
	case strings.HasPrefix(trimmed, "<!DOCTYPE "), strings.HasPrefix(trimmed, "<html"), strings.HasPrefix(trimmed, "<div"), strings.HasPrefix(trimmed, "<span"):
		return true
	}
	return false
}

func isIndentedCodeContinuation(line string) bool {
	trimmed := strings.TrimSpace(line)
	return trimmed != "" && (strings.HasPrefix(line, " ") || strings.HasPrefix(line, "\t"))
}

func isSimpleFunctionCall(trimmed string) bool {
	open := strings.Index(trimmed, "(")
	return open > 0 &&
		strings.HasSuffix(trimmed, ")") &&
		isIdentifierText(trimmed[:open])
}

func isIdentifierText(text string) bool {
	if text == "" {
		return false
	}
	for index, r := range text {
		if r == '_' || unicode.IsLetter(r) || (index > 0 && unicode.IsDigit(r)) {
			continue
		}
		return false
	}
	return true
}

func markdownFenceMarker(trimmed string) (string, bool) {
	for _, marker := range []string{"```", "~~~"} {
		if strings.HasPrefix(trimmed, marker) {
			return marker, true
		}
	}
	return "", false
}

func renderMarkdownCodeBlock(code []string, measure int) []string {
	if len(code) == 0 {
		return []string{""}
	}
	lines := []string{}
	for _, line := range code {
		if line == "" {
			lines = append(lines, "")
			continue
		}
		for lipgloss.Width(line) > measure {
			head, tail := splitAtWidth(line, measure)
			lines = append(lines, head)
			line = tail
		}
		lines = append(lines, line)
	}
	return lines
}

func isMarkdownTableHeader(header string, separator string) bool {
	headerCells := parseMarkdownTableRow(header)
	separatorCells := parseMarkdownTableRow(separator)
	if len(headerCells) < 2 || len(headerCells) != len(separatorCells) {
		return false
	}
	for _, cell := range separatorCells {
		if !isMarkdownTableSeparatorCell(cell) {
			return false
		}
	}
	return true
}

func isMarkdownTableRow(line string) bool {
	cells := parseMarkdownTableRow(line)
	return len(cells) >= 2
}

func parseMarkdownTableRow(line string) []string {
	trimmed := strings.TrimSpace(line)
	if trimmed == "" || !strings.Contains(trimmed, "|") {
		return nil
	}
	trimmed = strings.TrimPrefix(trimmed, "|")
	trimmed = strings.TrimSuffix(trimmed, "|")
	parts := strings.Split(trimmed, "|")
	cells := make([]string, 0, len(parts))
	for _, part := range parts {
		cells = append(cells, normalizeMarkdownTableCell(part))
	}
	return cells
}

func normalizeMarkdownTableCell(cell string) string {
	replacer := strings.NewReplacer(
		"<br />", "\n",
		"<br/>", "\n",
		"<br>", "\n",
		"<BR />", "\n",
		"<BR/>", "\n",
		"<BR>", "\n",
	)
	return strings.TrimSpace(replacer.Replace(cell))
}

func isMarkdownTableSeparatorCell(cell string) bool {
	cell = strings.TrimSpace(cell)
	cell = strings.Trim(cell, ":")
	if len(cell) < 3 {
		return false
	}
	for _, r := range cell {
		if r != '-' {
			return false
		}
	}
	return true
}

func parseMarkdownTableAlignments(separator string) []markdownTableAlignment {
	cells := parseMarkdownTableRow(separator)
	alignments := make([]markdownTableAlignment, len(cells))
	for index, cell := range cells {
		cell = strings.TrimSpace(cell)
		left := strings.HasPrefix(cell, ":")
		right := strings.HasSuffix(cell, ":")
		switch {
		case left && right:
			alignments[index] = markdownTableAlignCenter
		case right:
			alignments[index] = markdownTableAlignRight
		default:
			alignments[index] = markdownTableAlignLeft
		}
	}
	return alignments
}

func renderMarkdownTable(rows [][]string, alignments []markdownTableAlignment, measure int) []string {
	rows, columns := normalizeMarkdownTableRows(rows)
	if columns == 0 {
		return nil
	}
	alignments = normalizeMarkdownTableAlignments(alignments, columns)
	widths := markdownTableWidths(rows, columns, measure)
	renderedRows := make([][]string, len(rows))
	for rowIndex, row := range rows {
		renderedRows[rowIndex] = renderMarkdownTableRow(row, widths, alignments, rowIndex == 0)
	}
	separateBodyRows := markdownTableNeedsBodyRules(renderedRows)
	out := []string{markdownTableTopRule(widths)}
	for rowIndex, rowLines := range renderedRows {
		if separateBodyRows && rowIndex > 1 {
			out = append(out, markdownTableRule(widths))
		}
		out = append(out, rowLines...)
		if rowIndex == 0 && len(rows) > 1 {
			out = append(out, markdownTableRule(widths))
		}
	}
	out = append(out, markdownTableBottomRule(widths))
	return out
}

func markdownTableNeedsBodyRules(renderedRows [][]string) bool {
	if len(renderedRows) > markdownTableRuleBodyRows {
		return true
	}
	for _, row := range renderedRows[1:] {
		if len(row) > 1 {
			return true
		}
	}
	return false
}

func wrapMarkdownTableCellPart(prefix string, text string, measure int) []string {
	continuationPrefix := strings.Repeat(" ", lipgloss.Width(prefix))
	lines := []string{}
	for index, segment := range strings.Split(text, "\n") {
		firstPrefix := prefix
		if index > 0 {
			firstPrefix = continuationPrefix
		}
		lines = append(lines, wrapMarkdownInlineWithPrefixes(firstPrefix, continuationPrefix, segment, measure)...)
	}
	if len(lines) == 0 {
		return []string{prefix}
	}
	return lines
}

type markdownTableCellPart struct {
	text   string
	bullet bool
}

func markdownTableCellParts(value string) []markdownTableCellPart {
	value = strings.TrimSpace(value)
	starts := markdownInlineBulletStarts(value)
	if len(starts) == 0 {
		return []markdownTableCellPart{{text: value}}
	}

	parts := []markdownTableCellPart{}
	if starts[0] > 0 {
		if text := strings.TrimSpace(value[:starts[0]]); text != "" {
			parts = append(parts, markdownTableCellPart{text: text})
		}
	}
	for index, start := range starts {
		end := len(value)
		if index+1 < len(starts) {
			end = starts[index+1]
		}
		text := strings.TrimSpace(value[start:end])
		text = strings.TrimSpace(strings.TrimPrefix(text, "-"))
		if text != "" {
			parts = append(parts, markdownTableCellPart{text: text, bullet: true})
		}
	}
	if len(parts) == 0 {
		return []markdownTableCellPart{{text: value}}
	}
	return parts
}

func markdownInlineBulletStarts(value string) []int {
	starts := []int{}
	if strings.HasPrefix(value, "- ") {
		starts = append(starts, 0)
	}
	offset := 0
	for {
		index := strings.Index(value[offset:], " - ")
		if index < 0 {
			break
		}
		hyphen := offset + index + 1
		candidate := hyphen + 2
		if isProbableInlineBulletLabel(value[candidate:]) {
			starts = append(starts, hyphen)
		}
		offset = candidate
	}
	return starts
}

func isProbableInlineBulletLabel(text string) bool {
	colon := strings.IndexByte(text, ':')
	if colon <= 0 || colon > 48 {
		return false
	}
	label := text[:colon]
	return !strings.ContainsAny(label, ".!?;")
}

func normalizeMarkdownTableRows(rows [][]string) ([][]string, int) {
	columns := 0
	for _, row := range rows {
		if len(row) > columns {
			columns = len(row)
		}
	}
	if columns == 0 {
		return nil, 0
	}
	normalized := make([][]string, 0, len(rows))
	for _, row := range rows {
		next := make([]string, columns)
		copy(next, row)
		normalized = append(normalized, next)
	}
	return normalized, columns
}

func normalizeMarkdownTableAlignments(alignments []markdownTableAlignment, columns int) []markdownTableAlignment {
	normalized := make([]markdownTableAlignment, columns)
	copy(normalized, alignments)
	return normalized
}

func markdownTableWidths(rows [][]string, columns int, measure int) []int {
	separatorWidth := lipgloss.Width(markdownTableColumnSeparator)*maxInt(0, columns-1) + 4
	contentWidth := maxInt(columns*markdownTableMinColumnWidth, measure-separatorWidth)
	widths := make([]int, columns)
	minWidths := make([]int, columns)
	for column := range widths {
		headerWidth := 0
		if len(rows) > 0 && column < len(rows[0]) {
			headerWidth = lipgloss.Width(markdownInlinePlain(rows[0][column]))
		}
		minWidths[column] = clampInt(headerWidth, markdownTableMinColumnWidth, markdownTableHeaderMaxWidth)
		widths[column] = minWidths[column]
	}
	for _, row := range rows {
		for column, cell := range row {
			if column >= len(widths) {
				continue
			}
			for _, line := range markdownTableCellWidthParts(cell) {
				plainLine := markdownInlinePlain(line)
				lineWidth := lipgloss.Width(plainLine)
				if wordWidth := longestMarkdownWordWidth(plainLine); wordWidth > lineWidth {
					lineWidth = wordWidth
				}
				if lineWidth > widths[column] {
					widths[column] = minInt(lineWidth, contentWidth)
				}
			}
		}
	}
	for sumInts(widths) > contentWidth && shrinkMarkdownTableWidth(widths, minWidths) {
	}
	for sumInts(widths) > contentWidth {
		largest := 0
		for index, width := range widths {
			if width > widths[largest] {
				largest = index
			}
		}
		if widths[largest] <= markdownTableMinColumnWidth {
			break
		}
		widths[largest]--
	}
	return widths
}

func shrinkMarkdownTableWidth(widths []int, minWidths []int) bool {
	largest := -1
	for index, width := range widths {
		if width <= minWidths[index] {
			continue
		}
		if largest < 0 || width > widths[largest] {
			largest = index
		}
	}
	if largest < 0 {
		return false
	}
	widths[largest]--
	return true
}

func renderMarkdownTableRow(row []string, widths []int, alignments []markdownTableAlignment, header bool) []string {
	wrapped := make([][]string, len(widths))
	height := 1
	for column, width := range widths {
		cell := ""
		if column < len(row) {
			cell = row[column]
		}
		wrapped[column] = markdownTableCellLines(cell, width)
		if len(wrapped[column]) == 0 {
			wrapped[column] = []string{""}
		}
		if len(wrapped[column]) > height {
			height = len(wrapped[column])
		}
	}

	lines := make([]string, 0, height)
	for lineIndex := 0; lineIndex < height; lineIndex++ {
		parts := make([]string, len(widths))
		for column, width := range widths {
			cell := ""
			if lineIndex < len(wrapped[column]) {
				cell = wrapped[column][lineIndex]
			}
			if header && cell != "" {
				cell = renderMarkdownBoldText(cell)
			}
			parts[column] = alignMarkdownTableCell(cell, width, alignments[column])
		}
		lines = append(lines, "│ "+strings.Join(parts, markdownTableColumnSeparator)+" │")
	}
	return lines
}

func markdownTableCellWidthParts(cell string) []string {
	parts := markdownTableCellParts(cell)
	lines := []string{}
	for _, part := range parts {
		line := part.text
		if part.bullet {
			line = "- " + line
		}
		for _, segment := range strings.Split(line, "\n") {
			if segment != "" {
				lines = append(lines, segment)
			}
		}
	}
	if len(lines) == 0 {
		return []string{""}
	}
	return lines
}

func markdownTableCellLines(cell string, width int) []string {
	parts := markdownTableCellParts(cell)
	if len(parts) == 1 && !parts[0].bullet {
		return wrapMarkdownInline(cell, width)
	}
	lines := []string{}
	for _, part := range parts {
		prefix := ""
		if part.bullet {
			prefix = "- "
		}
		lines = append(lines, wrapMarkdownTableCellPart(prefix, part.text, width)...)
	}
	return lines
}

func alignMarkdownTableCell(text string, width int, alignment markdownTableAlignment) string {
	padding := width - lipgloss.Width(text)
	if padding <= 0 {
		return text
	}
	switch alignment {
	case markdownTableAlignRight:
		return strings.Repeat(" ", padding) + text
	case markdownTableAlignCenter:
		left := padding / 2
		right := padding - left
		return strings.Repeat(" ", left) + text + strings.Repeat(" ", right)
	default:
		return text + strings.Repeat(" ", padding)
	}
}

func markdownTableRule(widths []int) string {
	return markdownTableBorderRule(widths, "├", "┼", "┤")
}

func markdownTableTopRule(widths []int) string {
	return markdownTableBorderRule(widths, "╭", "┬", "╮")
}

func markdownTableBottomRule(widths []int) string {
	return markdownTableBorderRule(widths, "╰", "┴", "╯")
}

func markdownTableBorderRule(widths []int, left string, separator string, right string) string {
	parts := make([]string, len(widths))
	for index, width := range widths {
		parts[index] = strings.Repeat("─", maxInt(1, width+2))
	}
	return left + strings.Join(parts, separator) + right
}

func markdownHeadingText(trimmed string) string {
	level := 0
	for level < len(trimmed) && trimmed[level] == '#' {
		level++
	}
	if level == 0 || level > 6 || level >= len(trimmed) || trimmed[level] != ' ' {
		return ""
	}
	return strings.TrimSpace(trimmed[level:])
}

func isMarkdownListLine(trimmed string) bool {
	if len(trimmed) >= 2 {
		if strings.Contains("-*+", string(trimmed[0])) && trimmed[1] == ' ' {
			return true
		}
	}
	dot := strings.IndexByte(trimmed, '.')
	if dot <= 0 || dot+1 >= len(trimmed) || trimmed[dot+1] != ' ' {
		return false
	}
	for _, r := range trimmed[:dot] {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

func renderMarkdownStandaloneLine(line string) string {
	trimmed := strings.TrimSpace(line)
	if strings.HasPrefix(trimmed, ">") {
		bar := sonutTheme.accent.Render("▎")
		content := strings.TrimSpace(strings.TrimPrefix(trimmed, ">"))
		return bar + " " + content
	}
	return strings.TrimRight(line, " ")
}

type markdownInlineSegment struct {
	text   string
	bold   bool
	italic bool
	code   bool
}

func wrapMarkdownInline(text string, measure int) []string {
	if measure < 1 {
		measure = 1
	}
	out := []string{}
	for _, paragraph := range strings.Split(strings.ReplaceAll(text, "\r\n", "\n"), "\n") {
		if strings.TrimSpace(paragraph) == "" {
			out = append(out, "")
			continue
		}
		body := strings.TrimLeft(paragraph, " \t")
		indent := strings.ReplaceAll(paragraph[:len(paragraph)-len(body)], "\t", "    ")
		if len(indent) >= measure {
			indent = strings.Repeat(" ", measure/2)
		}
		out = append(out, wrapMarkdownInlineWithPrefixes(indent, indent, body, measure)...)
	}
	return out
}

func wrapMarkdownInlineWithPrefixes(firstPrefix string, continuationPrefix string, text string, measure int) []string {
	words := markdownInlineWords(parseMarkdownInline(text))
	if len(words) == 0 {
		return []string{firstPrefix}
	}

	lines := []string{}
	line := ""
	lineWidth := 0
	prefix := firstPrefix
	available := maxInt(1, measure-lipgloss.Width(prefix))

	flush := func() {
		lines = append(lines, prefix+line)
		line = ""
		lineWidth = 0
		prefix = continuationPrefix
		available = maxInt(1, measure-lipgloss.Width(prefix))
	}

	for _, word := range words {
		for lipgloss.Width(word.text) > available {
			if line != "" {
				flush()
			}
			head, tail := splitAtWidth(word.text, available)
			lines = append(lines, prefix+renderMarkdownInlineSegment(markdownInlineSegment{text: head, bold: word.bold, italic: word.italic}))
			word.text = tail
			prefix = continuationPrefix
			available = maxInt(1, measure-lipgloss.Width(prefix))
		}
		wordWidth := lipgloss.Width(word.text)
		rendered := renderMarkdownInlineSegment(word)
		separator := " "
		separatorWidth := 1
		if joinsPreviousMarkdownWord(word.text) {
			separator = ""
			separatorWidth = 0
		}
		switch {
		case line == "":
			line = rendered
			lineWidth = wordWidth
		case lineWidth+separatorWidth+wordWidth <= available:
			line += separator + rendered
			lineWidth += separatorWidth + wordWidth
		default:
			flush()
			line = rendered
			lineWidth = wordWidth
		}
	}
	if line != "" {
		lines = append(lines, prefix+line)
	}
	return lines
}

func markdownInlineWords(segments []markdownInlineSegment) []markdownInlineSegment {
	words := []markdownInlineSegment{}
	for _, segment := range segments {
		for _, word := range strings.Fields(segment.text) {
			words = append(words, markdownInlineSegment{text: word, bold: segment.bold, italic: segment.italic})
		}
	}
	return words
}

func joinsPreviousMarkdownWord(text string) bool {
	return text != "" && strings.Trim(text, ".,!?;:%)]}") == ""
}

func renderMarkdownInline(text string) string {
	segments := parseMarkdownInline(text)
	var builder strings.Builder
	for _, segment := range segments {
		builder.WriteString(renderMarkdownInlineSegment(segment))
	}
	return builder.String()
}

// renderCompactMarkdownLine renders Markdown in fixed-height UI rows such as
// plan steps and sidebar entries. Full assistant Markdown can expand into
// multiple lines; these rows cannot because their line positions are used for
// keyboard and mouse hit-testing. Preserve inline formatting and headings,
// then clip the visible text to the row budget.
func renderCompactMarkdownLine(text string, base lipgloss.Style, maxWidth int) string {
	text = strings.TrimSpace(text)
	if text == "" {
		return ""
	}
	if heading := markdownHeadingText(text); heading != "" {
		text = heading
		base = base.Bold(true).Underline(true)
	}
	plain := markdownInlinePlain(text)
	if maxWidth > 0 && lipgloss.Width(plain) > maxWidth {
		return base.Render(truncateStep(plain, maxWidth))
	}
	// Do not wrap an already ANSI-styled inline fragment in base.Render:
	// lipgloss may re-style the escape bytes themselves. The shared Markdown
	// styler preserves code colours while applying base to normal/bold text.
	return styleAssistantMarkdownLine(renderMarkdownInline(text), base)
}

func renderMarkdownInlineSegment(segment markdownInlineSegment) string {
	if segment.text == "" {
		return ""
	}
	if segment.code {
		return sonutTheme.faint.Render("`") + sonutTheme.amber.Render(segment.text) + sonutTheme.faint.Render("`")
	}
	out := segment.text
	if segment.bold {
		out = renderMarkdownBoldText(out)
	}
	if segment.italic {
		out = renderMarkdownItalicText(out)
	}
	return out
}

func renderMarkdownBoldText(text string) string {
	return markdownBoldStart + text + markdownBoldEnd
}

func renderMarkdownItalicText(text string) string {
	return markdownItalicStart + text + markdownItalicEnd
}

func markdownInlinePlain(text string) string {
	segments := parseMarkdownInline(text)
	var builder strings.Builder
	for _, segment := range segments {
		builder.WriteString(segment.text)
	}
	return strings.TrimSpace(builder.String())
}

func parseMarkdownInline(text string) []markdownInlineSegment {
	segments := []markdownInlineSegment{}
	var builder strings.Builder
	bold := false
	emphasis := false
	code := false
	flush := func() {
		if builder.Len() == 0 {
			return
		}
		segments = append(segments, markdownInlineSegment{
			text:   builder.String(),
			bold:   bold && !code,
			italic: emphasis && !code,
			code:   code,
		})
		builder.Reset()
	}

	for index := 0; index < len(text); {
		switch {
		case strings.HasPrefix(text[index:], "**"):
			if !code && ((bold && canCloseMarkdownDelimiter(text, index, "**")) || (!bold && canOpenMarkdownDelimiter(text, index, "**") && hasClosingMarkdownDelimiter(text, index+2, "**"))) {
				flush()
				bold = !bold
				index += 2
				continue
			}
		case strings.HasPrefix(text[index:], "__"):
			if !code && ((bold && canCloseMarkdownDelimiter(text, index, "__")) || (!bold && canOpenMarkdownDelimiter(text, index, "__") && hasClosingMarkdownDelimiter(text, index+2, "__"))) {
				flush()
				bold = !bold
				index += 2
				continue
			}
		case text[index] == '`':
			if code || strings.Contains(text[index+1:], "`") {
				flush()
				code = !code
				index++
				continue
			}
		case text[index] == '*':
			if !code && !strings.HasPrefix(text[index:], "**") && ((emphasis && canCloseMarkdownDelimiter(text, index, "*")) || (!emphasis && canOpenMarkdownDelimiter(text, index, "*") && hasClosingMarkdownDelimiter(text, index+1, "*"))) {
				flush()
				emphasis = !emphasis
				index++
				continue
			}
		case strings.HasPrefix(text[index:], "_"):
			// Single underscore italics — only when not part of identifiers like __init__.
			if !code && !strings.HasPrefix(text[index:], "__") && ((emphasis && canCloseMarkdownDelimiter(text, index, "_")) || (!emphasis && canOpenMarkdownDelimiter(text, index, "_") && hasClosingMarkdownDelimiter(text, index+1, "_"))) {
				flush()
				emphasis = !emphasis
				index++
				continue
			}
		}

		r, size := utf8.DecodeRuneInString(text[index:])
		if r == utf8.RuneError && size == 0 {
			break
		}
		builder.WriteRune(r)
		index += size
	}
	flush()
	return segments
}

func hasClosingMarkdownDelimiter(text string, start int, marker string) bool {
	for index := start; index < len(text); index++ {
		if !strings.HasPrefix(text[index:], marker) {
			continue
		}
		if marker == "*" && index+1 < len(text) && text[index+1] == '*' {
			index++
			continue
		}
		if canCloseMarkdownDelimiter(text, index, marker) {
			return true
		}
		index += len(marker) - 1
	}
	return false
}

func canOpenMarkdownDelimiter(text string, index int, marker string) bool {
	before, hasBefore := markdownRuneBefore(text, index)
	after, hasAfter := markdownRuneAfter(text, index+len(marker))
	if !hasAfter || unicode.IsSpace(after) {
		return false
	}
	if hasBefore && markdownIsWordRune(before) {
		return false
	}
	if marker == "__" && isMarkdownDunderIdentifier(text, index) {
		return false
	}
	return true
}

func canCloseMarkdownDelimiter(text string, index int, marker string) bool {
	before, hasBefore := markdownRuneBefore(text, index)
	after, hasAfter := markdownRuneAfter(text, index+len(marker))
	if !hasBefore || unicode.IsSpace(before) {
		return false
	}
	if hasAfter && markdownIsWordRune(after) {
		return false
	}
	return true
}

func isMarkdownDunderIdentifier(text string, index int) bool {
	end := strings.Index(text[index+2:], "__")
	if end < 0 {
		return false
	}
	body := text[index+2 : index+2+end]
	if body == "" {
		return false
	}
	for _, r := range body {
		if !markdownIsWordRune(r) {
			return false
		}
	}
	return true
}

func markdownRuneBefore(text string, index int) (rune, bool) {
	if index <= 0 {
		return 0, false
	}
	r, _ := utf8.DecodeLastRuneInString(text[:index])
	return r, r != utf8.RuneError
}

func markdownRuneAfter(text string, index int) (rune, bool) {
	if index >= len(text) {
		return 0, false
	}
	r, _ := utf8.DecodeRuneInString(text[index:])
	return r, r != utf8.RuneError
}

func markdownIsWordRune(r rune) bool {
	return r == '_' || unicode.IsLetter(r) || unicode.IsDigit(r)
}

func longestMarkdownWordWidth(text string) int {
	longest := 0
	for _, word := range strings.Fields(text) {
		if width := lipgloss.Width(word); width > longest {
			longest = width
		}
	}
	return longest
}

func sumInts(values []int) int {
	sum := 0
	for _, value := range values {
		sum += value
	}
	return sum
}

// isMarkdownImageLine reports whether trimmed is a standalone markdown image: ![alt](url)
func isMarkdownImageLine(trimmed string) bool {
	if !strings.HasPrefix(trimmed, "![") {
		return false
	}
	bracketClose := strings.Index(trimmed, "](")
	if bracketClose < 0 {
		return false
	}
	return strings.HasSuffix(trimmed, ")")
}

// renderMarkdownImageLines renders a markdown image. On supporting terminals
// (iTerm2, WezTerm, Ghostty) it emits an iTerm2 inline-image sequence followed
// by blank reservation lines so BubbleTea's layout accounting stays correct.
// Falls back to a styled hyperlinked label on unsupported terminals.
func renderMarkdownImageLines(trimmed string, measure int) []string {
	bracketClose := strings.Index(trimmed, "](")
	alt := trimmed[2:bracketClose]
	urlPart := trimmed[bracketClose+2 : len(trimmed)-1]
	if alt == "" {
		alt = urlPart
	}

	// Resolve the image path: accept absolute paths and workspace-relative ones.
	imgPath := strings.TrimSpace(urlPart)
	if imgPath != "" && !filepath.IsAbs(imgPath) {
		if abs, err := filepath.Abs(imgPath); err == nil {
			imgPath = abs
		}
	}

	const imgRows = 12 // terminal rows to reserve for the inline image

	if imgPath != "" {
		if _, err := os.Stat(imgPath); err == nil {
			if seq := renderInlineImage(imgPath, measure, imgRows); seq != "" {
				out := make([]string, imgRows+1)
				out[0] = seq
				// Blank lines reserve the visual rows; they are left empty so the
				// terminal-rendered image pixels are not overwritten by spaces.
				for i := 1; i <= imgRows; i++ {
					out[i] = ""
				}
				return out
			}
		}
	}

	// Fallback: styled label with file:// hyperlink so click-to-open works in
	// terminals that support OSC 8 (iTerm2, WezTerm, Ghostty, kitty, etc.).
	name := urlPart
	if idx := strings.LastIndexAny(urlPart, "/\\"); idx >= 0 {
		name = urlPart[idx+1:]
	}
	label := "📸  " + alt
	if name != "" && name != alt {
		label += "  (" + name + ")"
	}
	styled := sonutTheme.faint.Render(label)
	if imgPath != "" {
		fileURL := "file://" + imgPath
		styled = hyperlink(fileURL, styled)
	}
	return []string{styled}
}

// wrapCodeBlockWithHeader adds a language+copy header and footer border around
// already-rendered code block lines and registers the raw content for copy.
func wrapCodeBlockWithHeader(rendered []string, rawCode []string, lang string, measure int) []string {
	id := registerCodeBlock(rawCode)
	tag := codeBlockIDTag(id)

	// Build header: ╭─ lang ─────────────────── [⎘] ─╮
	langLabel := strings.ToLower(strings.TrimSpace(lang))
	copyBtn := " [⎘] "
	borderStyle := sonutTheme.lineStrong
	btnStyle := sonutTheme.faint

	const leftPad = 2  // "╭─"
	const rightPad = 2 // "─╮"
	copyW := lipgloss.Width(copyBtn)
	langW := 0
	if langLabel != "" {
		langW = lipgloss.Width(langLabel) + 1 // space after lang
	}
	fillW := measure - leftPad - langW - copyW - rightPad
	if fillW < 0 {
		fillW = 0
	}
	fill := strings.Repeat("─", fillW)

	var hdr strings.Builder
	hdr.WriteString(tag) // hidden zero-width copy ID
	hdr.WriteString(borderStyle.Render("╭─"))
	if langLabel != "" {
		hdr.WriteString(" " + sonutTheme.accent.Render(langLabel))
	}
	hdr.WriteString(borderStyle.Render(fill))
	hdr.WriteString(btnStyle.Render(copyBtn))
	hdr.WriteString(borderStyle.Render("─╮"))

	footer := borderStyle.Render("╰" + strings.Repeat("─", measure-2) + "╯")

	out := make([]string, 0, len(rendered)+2)
	out = append(out, hdr.String())
	out = append(out, rendered...)
	out = append(out, footer)
	return out
}

// stripXMLControlBlocks removes XML-style control tags from text that the model
// emits as tool invocations (e.g. <capture_artifact …>…</capture_artifact>).
// Only full open+close tag pairs and self-closing tags are stripped; partial
// matches and plain HTML entities are left alone.
func stripXMLControlBlocks(text string) string {
	// Known control tag names to strip from display.
	// plan / builder_workflow are executor-seed blocks that weak models echo
	// back as content; never show them in the transcript.
	controlTags := []string{
		"capture_artifact",
		"plan",
		"builder_workflow",
	}
	for _, tag := range controlTags {
		// Strip paired <tag ...>...</tag> blocks.
		for {
			open := "<" + tag
			start := strings.Index(text, open)
			if start < 0 {
				break
			}
			// Find the end of the opening tag.
			end := strings.Index(text[start:], ">")
			if end < 0 {
				break
			}
			end += start + 1
			// Self-closing <tag />?
			if strings.HasSuffix(strings.TrimSpace(text[start:end]), "/>") {
				text = text[:start] + text[end:]
				continue
			}
			// Find closing </tag>.
			closeTag := "</" + tag + ">"
			closeStart := strings.Index(text[end:], closeTag)
			if closeStart < 0 {
				// No closing tag — strip just the open tag line.
				text = text[:start] + text[end:]
				continue
			}
			closeStart += end
			text = text[:start] + text[closeStart+len(closeTag):]
		}
	}
	return text
}

func trimMarkdownDisplayBlankEdges(lines []string) []string {
	for len(lines) > 0 && strings.TrimSpace(lines[0]) == "" {
		lines = lines[1:]
	}
	for len(lines) > 0 && strings.TrimSpace(lines[len(lines)-1]) == "" {
		lines = lines[:len(lines)-1]
	}
	if len(lines) == 0 {
		return []string{""}
	}
	return lines
}
