package tui

import (
	"fmt"
	"strings"
)

// streamingTailLines is how many trailing lines of a file's in-progress content
// the live "writing" block shows — enough to watch the code flow without taking
// over the screen.
const streamingTailLines = 6

// isFileWritingTool reports whether a tool's streamed arguments are worth showing
// as live code (a file being written or edited).
func isFileWritingTool(name string) bool {
	switch name {
	case "write_file", "edit_file", "apply_patch":
		return true
	}
	return false
}

// decodeStreamingJSONString extracts the (possibly unterminated) value of a
// top-level string field from a streaming JSON args buffer — used to pull the
// path and the file content out of a tool call as its arguments arrive. Best
// effort: it unescapes \n \t \" \\ \/ for a readable preview, skips \uXXXX, and
// stops at the closing quote or the stream edge (a dangling backslash is dropped).
func decodeStreamingJSONString(args, key string) (string, bool) {
	// Find `"key"`, then tolerate optional whitespace around the colon and before
	// the opening quote, so both `"key":"v"` and `"key": "v"` (model JSON formatting
	// varies — kimi spaces after the colon) parse.
	keyMarker := `"` + key + `"`
	idx := strings.Index(args, keyMarker)
	if idx < 0 {
		return "", false
	}
	i := skipJSONSpace(args, idx+len(keyMarker))
	if i >= len(args) || args[i] != ':' {
		return "", false
	}
	i = skipJSONSpace(args, i+1)
	if i >= len(args) || args[i] != '"' {
		return "", false // value isn't a string yet (or hasn't streamed in)
	}
	rest := args[i+1:]
	var b strings.Builder
	for i := 0; i < len(rest); i++ {
		c := rest[i]
		if c == '\\' {
			if i+1 >= len(rest) {
				break // incomplete escape at the stream edge
			}
			i++
			switch rest[i] {
			case 'n':
				b.WriteByte('\n')
			case 't':
				b.WriteByte('\t')
			case 'r':
				// drop carriage returns from the preview
			case '"':
				b.WriteByte('"')
			case '\\':
				b.WriteByte('\\')
			case '/':
				b.WriteByte('/')
			case 'u':
				if i+4 < len(rest) {
					i += 4 // skip the 4 hex digits; the exact rune doesn't matter for a preview
				} else {
					i = len(rest)
				}
			default:
				b.WriteByte(rest[i])
			}
			continue
		}
		if c == '"' {
			break // closing quote
		}
		b.WriteByte(c)
	}
	return b.String(), true
}

// skipJSONSpace advances past JSON insignificant whitespace.
func skipJSONSpace(s string, i int) int {
	for i < len(s) && (s[i] == ' ' || s[i] == '\t' || s[i] == '\n' || s[i] == '\r') {
		i++
	}
	return i
}

// streamingFilePath pulls the file path out of a streaming tool-call args buffer,
// trying the argument names the file tools use. Used to seed the decoder's path.
func streamingFilePath(args string) string {
	for _, key := range []string{"path", "file", "file_path", "filepath", "filename"} {
		if v, ok := decodeStreamingJSONString(args, key); ok && v != "" {
			return v
		}
	}
	return ""
}

// streamingToolCallView renders the in-progress file-writing tool call: its path,
// live line count, elapsed time, and a bounded tail of the generated body. The
// body is a preview of the sibling runtime stage; the target file is promoted
// atomically only after the completed call passes validation.
func (m model) streamingToolCallView(width int) string {
	if m.streamCallID == "" || !isFileWritingTool(m.streamCallName) || m.streamCallDecoder == nil {
		return ""
	}
	d := m.streamCallDecoder

	headTag := ""
	switch {
	case d.hasContent():
		headTag = fmt.Sprintf("generated %d lines · %.1f KB…", d.lineTotal(), float64(d.rawLen)/1024)
	case d.rawLen > 0:
		headTag = fmt.Sprintf("receiving %.1f KB", float64(d.rawLen)/1024)
	}
	// Append elapsed time so a long write reads as actively progressing.
	if !m.turnStartedAt.IsZero() {
		elapsed := formatWorkingElapsed(m.now().Sub(m.turnStartedAt))
		if headTag != "" {
			headTag += "  " + elapsed
		} else {
			headTag = elapsed
		}
	}
	head := toolCardHead(m.streamCallName, displayStreamingPath(m.cwd, d.path), "", headTag, "", "", true, sonutTheme.ink, false, width, cardRenderOptions{cwd: m.cwd})
	lines := []string{sonutTheme.accent.Render(m.spinnerGlyph()) + " " + head}
	for _, line := range d.tailLines() {
		// Keep the live preview compact and avoid letting a long source line
		// expand the card past the terminal width.
		preview := truncateRunes(strings.ReplaceAll(line, "\t", "  "), maxInt(12, width-8))
		lines = append(lines, sonutTheme.faint.Render("  │ "+preview))
	}
	return strings.Join(lines, "\n")
}
