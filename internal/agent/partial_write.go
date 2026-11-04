package agent

import (
	"encoding/json"
	"os"
	"path"
	"path/filepath"
	"strconv"
	"strings"
	"unicode"
)

// parsePartialWriteArgs extracts the path and a best-effort content string from
// a write_file argument JSON fragment that was cut mid-stream when the byte cap
// or stall watchdog aborted the call. It falls back from json.Unmarshal (fast
// path for a call that actually completed its JSON before the cap) to a manual
// scanner that decodes as much of a truncated string value as is intact.
//
// content is trimmed to the last COMPLETE line so a half-written final line is
// never persisted; if not even one full line survived, content is "" and the
// caller skips recovery. The returned path/content are well-formed and safe to
// hand to the real write_file tool (which owns path resolution, scope
// confinement, and the hallucinated-path remap) — this function deliberately
// does NOT touch the filesystem or resolve paths itself.
func parsePartialWriteArgs(partial string) (path, content string) {
	return parsePartialFileContentArgs(partial,
		[]string{"path", "file", "file_path", "filename"},
		[]string{"content", "contents", "text", "body", "data", "file_content"},
	)
}

// parsePartialAppendArgs extracts the same path/content shape as write_file,
// but its caller appends the recovered complete lines instead of replacing the
// destination. Keeping a distinct entry point documents that an append recovery
// must never be routed through write_file and accidentally clobber prior chunks.
func parsePartialAppendArgs(partial string) (path, content string) {
	return parsePartialFileContentArgs(partial,
		[]string{"path", "file", "file_path", "filename"},
		[]string{"content", "contents", "text", "body", "data", "file_content"},
	)
}

// parsePartialEditArgs extracts path + new_string from an aborted edit_file
// call. The caller preserves this fragment in model context but never applies
// it as a replacement, because an aborted new_string is incomplete.
func parsePartialEditArgs(partial string) (path, content string) {
	return parsePartialFileContentArgs(partial,
		[]string{"path", "file", "file_path", "filename"},
		[]string{"new_string", "new_content", "replacement_content", "content", "contents", "new", "replace", "replacement", "new_str"},
	)
}

// repeatedFileContentPrefix identifies the small-model failure where a source
// token or source line starts repeating while the tool argument is still being
// generated. It returns the clean prefix ending before the repeated material.
// The detector is deliberately conservative: it needs a reasonably large body
// and either three identical long lines or four repeated identifier fragments,
// so ordinary loops and repeated HTML/CSS declarations are not interrupted.
func repeatedFileContentPrefix(content string) (string, bool) {
	if len(content) < 256 {
		return "", false
	}
	lines := strings.Split(content, "\n")
	for index := 0; index+2 < len(lines); index++ {
		line := strings.TrimSpace(lines[index])
		if len(line) < 48 || !looksLikeGeneratedSourceLine(line) {
			continue
		}
		repeats := 1
		for index+repeats < len(lines) && strings.TrimSpace(lines[index+repeats]) == line {
			repeats++
		}
		if repeats < 3 {
			continue
		}
		prefix := strings.Join(lines[:index], "\n")
		if index > 0 {
			prefix += "\n"
		}
		return prefix, true
	}

	// A common runaway shape grows one identifier forever:
	// particlesToSpawnIncDecDecDecDec... . Find a repeated 2–12 byte unit
	// inside an identifier and cut before its fourth repetition. This catches
	// the malformed token before it consumes the full file-argument budget.
	for tokenStart := 0; tokenStart < len(content); {
		for tokenStart < len(content) && !isIdentifierByte(content[tokenStart]) {
			tokenStart++
		}
		if tokenStart >= len(content) {
			break
		}
		tokenEnd := tokenStart
		for tokenEnd < len(content) && isIdentifierByte(content[tokenEnd]) {
			tokenEnd++
		}
		token := content[tokenStart:tokenEnd]
		if len(token) >= 32 {
			for unitLen := 2; unitLen <= 12; unitLen++ {
				for unitStart := 0; unitStart+unitLen*4 <= len(token); unitStart++ {
					unit := token[unitStart : unitStart+unitLen]
					if !hasDistinctIdentifierBytes(unit) {
						continue
					}
					repeats := 0
					for offset := unitStart; offset+unitLen <= len(token) &&
						strings.HasPrefix(token[offset:], unit); offset += unitLen {
						repeats++
					}
					if repeats >= 4 {
						// Drop the whole malformed identifier line rather than
						// persisting a half-token such as "...SpawnIn". The
						// preceding complete source is a safer append checkpoint.
						lineStart := strings.LastIndexByte(content[:tokenStart], '\n') + 1
						return content[:lineStart], true
					}
				}
			}
		}
		tokenStart = tokenEnd
	}
	return "", false
}

// looksLikeGeneratedSourceLine avoids treating a legitimate data-heavy file
// (for example a generated lookup table or a test fixture containing a long
// run of one character) as model repetition. Repeated executable lines have
// structural punctuation; the detector's purpose is to catch malformed source,
// not uniform payload data.
func looksLikeGeneratedSourceLine(line string) bool {
	return strings.ContainsAny(line, "{}()[];=,:.'\"`#")
}

func hasDistinctIdentifierBytes(value string) bool {
	if len(value) < 2 {
		return false
	}
	first := value[0]
	for index := 1; index < len(value); index++ {
		if value[index] != first {
			return true
		}
	}
	return false
}

func isIdentifierByte(value byte) bool {
	return value >= 'a' && value <= 'z' || value >= 'A' && value <= 'Z' ||
		value >= '0' && value <= '9' || value == '_' || value == '$'
}

// recoveryPathKey gives streamed partial-call paths a stable comparison form.
// It is deliberately lexical: actual filesystem resolution still belongs to
// the tools and their sandbox/scope checks. Lowercased + Clean so that
// "MarioGame.html", "./mariogame.html", and "mariogame.html" share one key.
func recoveryPathKey(value string) string {
	value = strings.TrimSpace(strings.ReplaceAll(value, `\`, "/"))
	value = strings.TrimPrefix(value, "./")
	if value == "" {
		return ""
	}
	return strings.ToLower(path.Clean(value))
}

// recoveryPathBase returns the basename of a recoveryPathKey result.
func recoveryPathBase(key string) string {
	if key == "" || key == "." {
		return ""
	}
	base := path.Base(key)
	if base == "." || base == "/" {
		return ""
	}
	return base
}

// partialRewriteStagePath returns a sibling used while replacing an existing
// file. Keeping the interrupted body out of the real target makes recovery
// resumable without ever appending a replacement fragment to the old file.
func partialRewriteStagePath(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return ""
	}
	if filepath.IsAbs(value) {
		return value + ".sonut.partial"
	}
	return value + ".sonut.partial"
}

func workspaceFileExists(cwd, value string) bool {
	value = strings.TrimSpace(value)
	if value == "" {
		return false
	}
	if !filepath.IsAbs(value) && cwd != "" {
		value = filepath.Join(cwd, value)
	}
	_, err := os.Stat(value)
	return err == nil
}

// markWriteSucceeded records a path (and its basename) so the plan-seeded
// rewrite thrash guard can block both "mariogame.html" and "dummy/mariogame.html"
// after either lands successfully. Live failure: model wrote dummy/X then spent
// ~8 minutes rewriting bare X — same file, different path string.
func markWriteSucceeded(m map[string]bool, filePath string) {
	if m == nil {
		return
	}
	key := recoveryPathKey(filePath)
	if key == "" || key == "." {
		return
	}
	m[key] = true
	if base := recoveryPathBase(key); base != "" && base != key {
		m[base] = true
	}
}

// writePathAlreadySucceeded reports whether a write/create target was already
// completed this run. Matches full keys and bare basenames so alternate path
// spellings of the same deliverable cannot thrash.
func writePathAlreadySucceeded(m map[string]bool, filePath string) bool {
	if len(m) == 0 {
		return false
	}
	key := recoveryPathKey(filePath)
	if key == "" || key == "." {
		return false
	}
	if m[key] {
		return true
	}
	// Do not skip a legitimate second file such as public/index.html merely
	// because src/index.html was written earlier. Basename recovery remains
	// useful for a bare model path (index.html) after a qualified path landed.
	if strings.Contains(key, "/") {
		return false
	}
	base := recoveryPathBase(key)
	if base == "" {
		return false
	}
	if m[base] {
		return true
	}
	for k := range m {
		if recoveryPathBase(k) == base {
			return true
		}
	}
	return false
}

// minEditSalvageBytes: ignore tiny new_string fragments (not worth a rewrite).
const minEditSalvageBytes = 120

// isAppendOnlyPath reports whether a path (or its basename) is latched as
// "prefix already on disk — only append_file may extend it".
func isAppendOnlyPath(m map[string]bool, filePath string) bool {
	if len(m) == 0 {
		return false
	}
	key := recoveryPathKey(filePath)
	if key == "" || key == "." {
		return false
	}
	if m[key] {
		return true
	}
	// A path-qualified request must never inherit append-only state from an
	// unrelated directory that merely has the same basename. Bare paths are
	// still allowed to resolve to the one active basename for the current cwd.
	if strings.Contains(key, "/") {
		return false
	}
	return m[recoveryPathBase(key)]
}

// latchAppendOnly marks path + basename so rewrite restarts cannot clobber a
// saved partial prefix.
func latchAppendOnly(m map[string]bool, filePath string) {
	if m == nil {
		return
	}
	key := recoveryPathKey(filePath)
	if key == "" || key == "." {
		return
	}
	m[key] = true
	if base := recoveryPathBase(key); base != "" && base != key {
		m[base] = true
	}
}

// unlatchAppendOnly clears the append-only latch once a file is complete.
func unlatchAppendOnly(m map[string]bool, filePath string) {
	if m == nil {
		return
	}
	key := recoveryPathKey(filePath)
	if key == "" {
		return
	}
	delete(m, key)
	if base := recoveryPathBase(key); base != "" {
		delete(m, base)
	}
}

// contentDeltaForAppend returns the portion of generated that should be
// appended after existing. This is the core "never lose generated content /
// never regenerate the prefix" rule:
//
//   - empty generated → nothing to save
//   - empty existing → write everything (caller may still route via write_file)
//   - generated starts with existing → keep only the new suffix (model re-sent
//     the saved prefix under write_file)
//   - existing already ends with generated → idempotent replay, nothing new
//   - otherwise → treat generated as the next chunk (model called write_file
//     when it should have called append_file)
func contentDeltaForAppend(existing, generated string) string {
	if generated == "" {
		return ""
	}
	if existing == "" {
		return generated
	}
	if generated == existing {
		return ""
	}
	if strings.HasPrefix(generated, existing) {
		return generated[len(existing):]
	}
	// Saved prefix sometimes lost a final newline the model re-added.
	if !strings.HasSuffix(existing, "\n") {
		if withNL := existing + "\n"; strings.HasPrefix(generated, withNL) {
			return generated[len(withNL):]
		}
	}
	if strings.HasSuffix(existing, generated) {
		return ""
	}
	// Do not suppress an interior match. Repeated source blocks are legitimate
	// (HTML cards, CSS rules, fixtures); only prefix/suffix evidence proves that
	// this is an idempotent replay. Dropping an interior match loses generated
	// code and is worse than a possible duplicate that verification can catch.
	return generated
}

// readWorkspaceFile reads path relative to cwd (or absolute). Empty on miss.
// Used only for delta-append salvage so we never re-dispatch a rewrite that
// would clobber a saved prefix.
func readWorkspaceFile(cwd, rel string) string {
	rel = strings.TrimSpace(rel)
	if rel == "" {
		return ""
	}
	candidates := []string{rel}
	if cwd != "" && !filepath.IsAbs(rel) {
		candidates = append(candidates, filepath.Join(cwd, rel))
	}
	// Also try cleaned relative form.
	clean := path.Clean(strings.ReplaceAll(rel, `\`, "/"))
	if clean != rel && clean != "." {
		candidates = append(candidates, clean)
		if cwd != "" && !filepath.IsAbs(clean) {
			candidates = append(candidates, filepath.Join(cwd, clean))
		}
	}
	for _, p := range candidates {
		b, err := os.ReadFile(p)
		if err == nil {
			return string(b)
		}
	}
	return ""
}

// lastNLines returns the last n logical lines of s (preserving a trailing
// newline when the original had one on the final selected line).
func lastNLines(s string, n int) string {
	if n <= 0 || s == "" {
		return s
	}
	// Split keeps a trailing empty element when s ends with \n.
	parts := strings.Split(s, "\n")
	trailingNL := strings.HasSuffix(s, "\n")
	if trailingNL && len(parts) > 0 {
		parts = parts[:len(parts)-1]
	}
	if len(parts) <= n {
		return s
	}
	out := strings.Join(parts[len(parts)-n:], "\n")
	if trailingNL {
		out += "\n"
	}
	return out
}

// partialFileContinuationNudge forces the executor to finish an incomplete
// saved file with append_file instead of regenerating from scratch or jumping
// to the next plan step.
func partialFileContinuationNudge(filePath, savedContent string) string {
	lines := strings.Count(savedContent, "\n")
	if savedContent != "" && !strings.HasSuffix(savedContent, "\n") {
		lines++
	}
	tail := lastNLines(savedContent, 12)
	if len(tail) > 900 {
		// Keep the most recent end of the file for the model to continue from.
		tail = tail[len(tail)-900:]
		if i := strings.Index(tail, "\n"); i >= 0 {
			tail = tail[i+1:]
		}
	}
	return "CONTINUE THIS FILE NOW before any other plan step: " + filePath +
		" has an INCOMPLETE saved prefix (" + strconv.Itoa(lines) + " lines on disk). " +
		"Your next tool call MUST be append_file on this exact path with the NEXT real source code only " +
		"(≤100 lines, non-empty body). " +
		"FORBIDDEN: write_file on this path, empty append_file, re-generating the saved prefix, " +
		"final answers, or moving to the next plan step until this file is finished.\n" +
		"File currently ends with:\n```\n" + tail + "```\n" +
		"Append ONLY what comes after that tail. Content already on disk must not be regenerated."
}

func parsePartialFileContentArgs(partial string, pathKeys, contentKeys []string) (path, content string) {
	var args map[string]any
	if json.Unmarshal([]byte(partial), &args) == nil {
		// Complete JSON: keep content as-is (including single-line / no trailing \n).
		path = stringFromKeys(args, pathKeys...)
		content = stringFromKeys(args, contentKeys...)
		return path, content
	}

	// Incomplete JSON: scan + drop half-written final line.
	path = extractJSONStringFieldAny(partial, pathKeys...)
	content = extractJSONStringFieldAny(partial, contentKeys...)
	if i := strings.LastIndex(content, "\n"); i >= 0 {
		content = content[:i+1]
	} else {
		content = ""
	}
	return path, content
}

func stringFromKeys(m map[string]any, keys ...string) string {
	for _, k := range keys {
		if v, ok := m[k].(string); ok && v != "" {
			return v
		}
	}
	return ""
}

func extractJSONStringFieldAny(partial string, keys ...string) string {
	for _, k := range keys {
		if v := extractJSONStringField(partial, k); v != "" {
			return v
		}
	}
	return ""
}

// extractJSONStringField locates `"key":` in partial and decodes the following
// JSON string value, which may be truncated mid-stream.
func extractJSONStringField(partial, key string) string {
	needle := `"` + key + `"`
	idx := strings.Index(partial, needle)
	if idx < 0 {
		return ""
	}
	rest := partial[idx+len(needle):]
	rest = strings.TrimLeftFunc(rest, unicode.IsSpace)
	if !strings.HasPrefix(rest, ":") {
		return ""
	}
	rest = strings.TrimLeftFunc(rest[1:], unicode.IsSpace)
	if !strings.HasPrefix(rest, `"`) {
		return ""
	}
	return decodePartialJSONString(rest[1:])
}

// decodePartialJSONString decodes a JSON string body (everything after the
// opening quote) that may be cut mid-stream. It handles standard escape
// sequences and stops cleanly at an incomplete escape at the end of input.
func decodePartialJSONString(body string) string {
	var b strings.Builder
	b.Grow(len(body))
	for i := 0; i < len(body); {
		ch := body[i]
		if ch == '"' {
			break
		}
		if ch != '\\' {
			b.WriteByte(ch)
			i++
			continue
		}
		if i+1 >= len(body) {
			break // dangling backslash at truncation — stop cleanly
		}
		i++
		switch esc := body[i]; esc {
		case '"':
			b.WriteByte('"')
		case '\\':
			b.WriteByte('\\')
		case '/':
			b.WriteByte('/')
		case 'n':
			b.WriteByte('\n')
		case 'r':
			b.WriteByte('\r')
		case 't':
			b.WriteByte('\t')
		case 'b':
			b.WriteByte('\b')
		case 'f':
			b.WriteByte('\f')
		case 'u':
			if i+4 >= len(body) {
				return b.String() // incomplete \uXXXX at truncation
			}
			if r := hexToRune(body[i+1 : i+5]); r >= 0 {
				b.WriteRune(r)
				i += 4
			}
		default:
			b.WriteByte(esc)
		}
		i++
	}
	return b.String()
}

func hexToRune(s string) rune {
	var r rune
	for _, c := range []byte(s) {
		r <<= 4
		switch {
		case c >= '0' && c <= '9':
			r |= rune(c - '0')
		case c >= 'a' && c <= 'f':
			r |= rune(c-'a') + 10
		case c >= 'A' && c <= 'F':
			r |= rune(c-'A') + 10
		default:
			return -1
		}
	}
	return r
}
