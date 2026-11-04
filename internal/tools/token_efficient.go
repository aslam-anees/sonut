package tools

// Token-efficient post-cleanse for shell tool output (ported from MiMo-Code's
// bash_token_efficient pipeline). Cleans only the LLM-facing string: raw bytes
// are not archived here; TUI can still show the same Result.Output.
//
// Pipeline order (must not change):
//  1. progress — fold \r redraw frames, keep last frame per line
//  2. ansi     — strip CSI/OSC/DCS, backspace overstrike, control bytes
//  3. longline — elide lines over maxLineChars
//  4. heuristic shapes (go test, pytest, npm, make, stacktrace, gitdiff, …)
//
// Never-worse gate: if cleaned output is not strictly smaller, return original.
// Opt-out: command contains "# nofilter" / "# raw", or ATCODE_BASH_RAW=1.

import (
	"os"
	"regexp"
	"strings"
	"unicode/utf8"
)

const (
	defaultMaxLineChars = 500
	defaultLineHeadKeep = 160
	neverWorseMargin    = 0
	headTailFingerprint = 4096
)

var (
	ansiCSI   = regexp.MustCompile(`\x1b\[[0-?]*[ -/]*[@-~]`)
	ansiOSC   = regexp.MustCompile(`\x1b\][^\x07\x1b]*(?:\x07|\x1b\\)`)
	ansiDCS   = regexp.MustCompile(`\x1b[PX^_][\s\S]*?\x1b\\`)
	backspace = regexp.MustCompile(`[^\n]\x08`)
	ctrlBytes = regexp.MustCompile(`[\x00-\x08\x0B\x0C\x0E-\x1F\x7F]`)

	passthroughPatterns = []*regexp.Regexp{
		regexp.MustCompile(`(^|\s)--json(\s|=|$)`),
		regexp.MustCompile(`(^|\s)--format[= ]json(\s|$)`),
		regexp.MustCompile(`(^|\s)-o[= ]json(\s|$)`),
		regexp.MustCompile(`(^|\s)--no-color(\s|$)`),
		regexp.MustCompile(`\|\s*tee(\s|$)`),
		regexp.MustCompile(`\|\s*xxd(\s|$)`),
		regexp.MustCompile(`\|\s*hexdump(\s|$)`),
	}

	// Command-name channel. First hit wins.
	commandShapePatterns = []struct {
		re *regexp.Regexp
		id string
	}{
		{regexp.MustCompile(`(?i)^\s*pytest(\s|$)`), "pytest"},
		{regexp.MustCompile(`(?i)^\s*(?:npm|pnpm|yarn)\s+(?:install|i|add)(\s|$)`), "npm"},
		{regexp.MustCompile(`(?i)^\s*(?:make|cmake|automake)(\s|$)`), "make"},
		{regexp.MustCompile(`(?i)^\s*git\s+(?:diff|show)(\s|$)`), "gitdiff"},
		{regexp.MustCompile(`(?i)^\s*tsc(\s|$)`), "tsc"},
		{regexp.MustCompile(`(?i)^\s*go\s+test\b`), "gotest"},
		{regexp.MustCompile(`(?i)^\s*(?:cargo|rustc)\s+(?:test|check|clippy|build)\b`), "make"},
	}

	bodyShapeFingerprints = []struct {
		re *regexp.Regexp
		id string
	}{
		{regexp.MustCompile(`(?m)^={5,}\s+test session starts\s+={5,}`), "pytest"},
		{regexp.MustCompile(`(?m)^diff --git `), "gitdiff"},
		{regexp.MustCompile(`(?m)^Traceback \(most recent call last\)`), "stacktrace"},
		{regexp.MustCompile(`(?m)^\s*at .+:\d+:\d+`), "stacktrace"},
		{regexp.MustCompile(`(?m)^error\[E\d+\]:`), "stacktrace"},
		{regexp.MustCompile(`(?m)^--- (PASS|FAIL|SKIP): `), "gotest"},
		{regexp.MustCompile(`(?m)^FAIL\t\S+\s`), "gotest"},
	}

	gitdiffNoisePaths = regexp.MustCompile(`(?:^|/)(?:package-lock\.json|pnpm-lock\.yaml|yarn\.lock|composer\.lock|Cargo\.lock|Gemfile\.lock|poetry\.lock|uv\.lock|.*\.min\.js|.*\.min\.css|dist/.*|build/.*|node_modules/.*|.*\.generated\..*)$`)

	depFramePaths = regexp.MustCompile(`(?:site-packages|\.venv|node_modules|/usr/lib/go/|/usr/local/go/src/|/lib/python)`)
)

// CleanOptions configures shell output cleansing for the model view.
type CleanOptions struct {
	Command string
}

// CleanResult is the outcome of CleanForModel.
type CleanResult struct {
	Text     string
	BytesIn  int
	BytesOut int
	// Shape is the heuristic shape applied, if any (e.g. "gotest").
	Shape string
	// Degraded is true when the never-worse gate rolled back to the original.
	Degraded bool
}

// CleanForModel shrinks shell tool output for the LLM while preserving
// signal. Safe by construction: never returns a larger string than the input
// (byte length). Empty input and opt-outs are no-ops.
func CleanForModel(text string, opts CleanOptions) CleanResult {
	bytesIn := len(text)
	if text == "" || shouldSkipClean(opts.Command) {
		return CleanResult{Text: text, BytesIn: bytesIn, BytesOut: bytesIn}
	}

	out := cleanProgress(text)
	out = cleanANSI(out)
	out = cleanLongLines(out)

	shape := ""
	if !shouldPassthrough(opts.Command) {
		if id := detectShape(opts.Command, out); id != "" {
			shaped := applyShape(id, out)
			if len(shaped) < len(out) {
				out = shaped
				shape = id
			}
		}
	}

	bytesOut := len(out)
	if bytesOut+neverWorseMargin >= bytesIn {
		return CleanResult{Text: text, BytesIn: bytesIn, BytesOut: bytesIn, Degraded: true}
	}
	return CleanResult{Text: out, BytesIn: bytesIn, BytesOut: bytesOut, Shape: shape}
}

func shouldSkipClean(command string) bool {
	if os.Getenv("ATCODE_BASH_RAW") == "1" {
		return true
	}
	if command == "" {
		return false
	}
	return strings.Contains(command, "# nofilter") || strings.Contains(command, "# raw")
}

func shouldPassthrough(command string) bool {
	if command == "" {
		return false
	}
	for _, re := range passthroughPatterns {
		if re.MatchString(command) {
			return true
		}
	}
	return false
}

func cleanProgress(text string) string {
	if !strings.Contains(text, "\r") {
		return text
	}
	lines := strings.Split(text, "\n")
	for i, line := range lines {
		stripped := line
		if strings.HasSuffix(stripped, "\r") {
			stripped = stripped[:len(stripped)-1]
		}
		if idx := strings.LastIndex(stripped, "\r"); idx >= 0 {
			lines[i] = stripped[idx+1:]
		} else {
			lines[i] = stripped
		}
	}
	return strings.Join(lines, "\n")
}

func cleanANSI(text string) string {
	out := ansiCSI.ReplaceAllString(text, "")
	out = ansiOSC.ReplaceAllString(out, "")
	out = ansiDCS.ReplaceAllString(out, "")
	// Collapse backspace overstrike: "ab\b\bcd" → "cd".
	for backspace.MatchString(out) {
		out = backspace.ReplaceAllString(out, "")
	}
	return ctrlBytes.ReplaceAllString(out, "")
}

func cleanLongLines(text string) string {
	if len(text) <= defaultMaxLineChars {
		return text
	}
	lines := strings.Split(text, "\n")
	changed := false
	for i, line := range lines {
		if utf8.RuneCountInString(line) <= defaultMaxLineChars {
			continue
		}
		runes := []rune(line)
		keep := defaultLineHeadKeep
		if keep > len(runes) {
			keep = len(runes)
		}
		elided := len(runes) - keep
		lines[i] = string(runes[:keep]) + "…<elided " + itoa(elided) + " chars>"
		changed = true
	}
	if !changed {
		return text
	}
	return strings.Join(lines, "\n")
}

func detectShape(command, body string) string {
	for _, p := range commandShapePatterns {
		if p.re.MatchString(command) {
			return p.id
		}
	}
	head := body
	if len(head) > headTailFingerprint {
		head = head[:headTailFingerprint]
	}
	for _, p := range bodyShapeFingerprints {
		if p.re.MatchString(head) {
			return p.id
		}
	}
	return ""
}

func applyShape(id, body string) string {
	switch id {
	case "gotest":
		return shapeGoTest(body)
	case "pytest":
		return shapePytest(body)
	case "npm":
		return shapeNPM(body)
	case "make":
		return shapeMake(body)
	case "stacktrace":
		return shapeStacktrace(body)
	case "gitdiff":
		return shapeGitDiff(body)
	case "tsc":
		return shapeTSC(body)
	default:
		return body
	}
}

// shapeGoTest keeps FAIL/PASS package lines, failure details, and the final
// summary; drops long verbose PASS dumps that burn local-model context.
func shapeGoTest(body string) string {
	lines := strings.Split(body, "\n")
	var out []string
	inFailBlock := false
	keptPass := 0
	const maxPassLines = 8
	for _, line := range lines {
		switch {
		case strings.HasPrefix(line, "--- FAIL:"):
			inFailBlock = true
			out = append(out, line)
		case strings.HasPrefix(line, "--- PASS:"):
			inFailBlock = false
			if keptPass < maxPassLines {
				out = append(out, line)
				keptPass++
			}
		case strings.HasPrefix(line, "--- SKIP:"):
			inFailBlock = false
			// skip noise
		case strings.HasPrefix(line, "FAIL\t") || strings.HasPrefix(line, "ok  \t") || strings.HasPrefix(line, "ok\t"):
			inFailBlock = false
			out = append(out, line)
		case strings.HasPrefix(line, "PASS") || strings.HasPrefix(line, "FAIL") || strings.HasPrefix(line, "coverage:"):
			out = append(out, line)
		case strings.Contains(line, "Error Trace:") || strings.Contains(line, "Error:") || strings.Contains(line, "expected") || strings.Contains(line, "actual"):
			out = append(out, line)
		case inFailBlock:
			// Keep failure stack / assertion detail (bounded).
			if len(out) < 400 {
				out = append(out, line)
			}
		case strings.HasPrefix(line, "# ") || strings.Contains(line, ".go:"):
			// compiler / build errors
			out = append(out, line)
		}
	}
	if len(out) == 0 {
		return body
	}
	return strings.Join(out, "\n")
}

func shapePytest(body string) string {
	lines := strings.Split(body, "\n")
	phase := "header"
	var out []string
	for _, line := range lines {
		switch {
		case regexp.MustCompile(`^={5,}\s+test session starts\s+={5,}`).MatchString(line):
			phase = "header"
			out = append(out, line)
		case regexp.MustCompile(`^={3,}\s+FAILURES\s+={3,}`).MatchString(line):
			phase = "failures"
			out = append(out, line)
		case regexp.MustCompile(`^={3,}\s+short test summary info\s+={3,}`).MatchString(line):
			phase = "summary"
			out = append(out, line)
		case regexp.MustCompile(`^={3,}\s+.*(?:passed|failed|error).*={3,}$`).MatchString(line):
			out = append(out, line)
		case phase == "header":
			if strings.HasPrefix(line, "collected ") || strings.HasPrefix(line, "platform ") ||
				strings.HasPrefix(line, "rootdir:") || strings.HasPrefix(line, "plugins:") {
				out = append(out, line)
			}
			if strings.Contains(line, "[") && strings.HasSuffix(strings.TrimSpace(line), "%]") {
				phase = "progress"
			}
		case phase == "progress":
			if strings.HasPrefix(line, "FAILED ") {
				out = append(out, line)
			}
		case phase == "failures":
			if strings.HasPrefix(line, "E ") || strings.HasPrefix(line, "E\t") ||
				strings.HasPrefix(line, "FAILED ") || strings.Contains(line, ".py:") {
				out = append(out, line)
			}
		case phase == "summary":
			out = append(out, line)
		}
	}
	if len(out) == 0 {
		return body
	}
	return strings.Join(out, "\n")
}

func shapeNPM(body string) string {
	lines := strings.Split(body, "\n")
	var out []string
	deprecations := 0
	var topDeps []string
	for _, line := range lines {
		trim := strings.TrimSpace(line)
		low := strings.ToLower(trim)
		switch {
		case strings.Contains(low, "npm warn deprecated") || strings.Contains(low, "warn deprecated"):
			deprecations++
			if len(topDeps) < 3 {
				topDeps = append(topDeps, trim)
			}
		case strings.Contains(low, "added ") || strings.Contains(low, "removed ") ||
			strings.Contains(low, "changed ") || strings.Contains(low, "audited ") ||
			strings.Contains(low, "vulnerabilit") || strings.Contains(low, "funding") ||
			strings.Contains(low, "packages in") || strings.HasPrefix(low, "npm err") ||
			strings.HasPrefix(low, "error") || strings.Contains(low, "err!"):
			out = append(out, line)
		case strings.Contains(low, "npm notice") || strings.Contains(low, "npm warn"):
			// drop routine notices
		default:
			// Keep short non-noise lines (install progress summaries).
			if len(trim) > 0 && len(trim) < 120 && !strings.HasPrefix(trim, "http ") {
				if strings.Contains(low, "install") || strings.Contains(low, "done") ||
					strings.Contains(low, "success") {
					out = append(out, line)
				}
			}
		}
	}
	if deprecations > 0 {
		msg := "<×" + itoa(deprecations) + " deprecation warnings"
		if len(topDeps) > 0 {
			msg += ": top: " + strings.Join(topDeps, " | ")
		}
		msg += ">"
		out = append([]string{msg}, out...)
	}
	if len(out) == 0 {
		return body
	}
	return strings.Join(out, "\n")
}

func shapeMake(body string) string {
	lines := strings.Split(body, "\n")
	var out []string
	for _, line := range lines {
		trim := strings.TrimSpace(line)
		if strings.Contains(line, "Entering directory") || strings.Contains(line, "Leaving directory") {
			continue
		}
		// Keep errors, notes, warnings with file:line:col.
		if strings.Contains(line, "error:") || strings.Contains(line, "warning:") ||
			strings.Contains(line, "note:") || strings.Contains(line, "Error ") ||
			strings.Contains(line, "undefined") || strings.Contains(line, "fatal:") ||
			regexp.MustCompile(`\S+:\d+:\d+:`).MatchString(line) {
			out = append(out, line)
			continue
		}
		// Drop bare compile command lines (cc …, gcc …, go build … noise).
		if strings.HasPrefix(trim, "cc ") || strings.HasPrefix(trim, "gcc ") ||
			strings.HasPrefix(trim, "clang ") || strings.HasPrefix(trim, "c++ ") ||
			strings.HasPrefix(trim, "/usr/") {
			continue
		}
		if strings.HasPrefix(trim, "make:") || strings.HasPrefix(trim, "make[") {
			out = append(out, line)
		}
	}
	if len(out) == 0 {
		return body
	}
	return strings.Join(out, "\n")
}

func shapeStacktrace(body string) string {
	lines := strings.Split(body, "\n")
	var out []string
	var pending []string
	flushDeps := func() {
		if len(pending) >= 2 {
			out = append(out, "<"+itoa(len(pending))+" dependency frame(s) suppressed>")
		} else {
			out = append(out, pending...)
		}
		pending = pending[:0]
	}
	for _, line := range lines {
		if depFramePaths.MatchString(line) {
			pending = append(pending, line)
			continue
		}
		flushDeps()
		out = append(out, line)
	}
	flushDeps()
	if len(out) == 0 {
		return body
	}
	return strings.Join(out, "\n")
}

func shapeGitDiff(body string) string {
	parts := splitGitDiffFiles(body)
	var out []string
	for _, file := range parts {
		if !strings.HasPrefix(file, "diff --git") {
			out = append(out, file)
			continue
		}
		pathMatch := regexp.MustCompile(`(?m)^diff --git a/(\S+) b/(\S+)`).FindStringSubmatch(file)
		target := ""
		if len(pathMatch) >= 3 {
			target = pathMatch[2]
		}
		header, hunks := splitDiffHeaderHunks(file)
		if target != "" && gitdiffNoisePaths.MatchString(target) {
			added, removed := countDiffLines(hunks)
			out = append(out, strings.Join(header, "\n")+"\n<hunks suppressed: generated/lockfile — +"+itoa(added)+" -"+itoa(removed)+">")
			continue
		}
		capped := capHunkLines(hunks, 100)
		added, removed := countDiffLines(capped)
		block := append(append([]string{}, header...), capped...)
		block = append(block, "<file summary: +"+itoa(added)+" -"+itoa(removed)+">")
		out = append(out, strings.Join(block, "\n"))
	}
	return strings.Join(out, "")
}

func shapeTSC(body string) string {
	// Group by error code; keep one sample per code and a count.
	type group struct {
		sample string
		count  int
	}
	byCode := map[string]*group{}
	var order []string
	lines := strings.Split(body, "\n")
	codeRE := regexp.MustCompile(`error\s+(TS\d+)`)
	for _, line := range lines {
		m := codeRE.FindStringSubmatch(line)
		if m == nil {
			continue
		}
		code := m[1]
		if g, ok := byCode[code]; ok {
			g.count++
			continue
		}
		byCode[code] = &group{sample: line, count: 1}
		order = append(order, code)
	}
	if len(order) == 0 {
		return body
	}
	var out []string
	limit := 8
	if len(order) < limit {
		limit = len(order)
	}
	for _, code := range order[:limit] {
		g := byCode[code]
		if g.count > 1 {
			out = append(out, g.sample+"  <×"+itoa(g.count)+" "+code+">")
		} else {
			out = append(out, g.sample)
		}
	}
	if len(order) > limit {
		out = append(out, "<… "+itoa(len(order)-limit)+" more error codes>")
	}
	return strings.Join(out, "\n")
}

func splitGitDiffFiles(body string) []string {
	if !strings.Contains(body, "diff --git ") {
		return []string{body}
	}
	// Split keeping the delimiter.
	re := regexp.MustCompile(`(?m)(?=^diff --git )`)
	parts := re.Split(body, -1)
	return parts
}

func splitDiffHeaderHunks(file string) (header, hunks []string) {
	inHunk := false
	for _, line := range strings.Split(file, "\n") {
		if strings.HasPrefix(line, "@@") {
			inHunk = true
			hunks = append(hunks, line)
			continue
		}
		if !inHunk {
			header = append(header, line)
		} else {
			hunks = append(hunks, line)
		}
	}
	return header, hunks
}

func capHunkLines(hunks []string, maxPerHunk int) []string {
	var out []string
	currentLen := 0
	elided := false
	for _, line := range hunks {
		if strings.HasPrefix(line, "@@") {
			currentLen = 0
			elided = false
			out = append(out, line)
			continue
		}
		currentLen++
		if currentLen <= maxPerHunk {
			out = append(out, line)
			continue
		}
		if !elided {
			out = append(out, "<hunk elided: more lines>")
			elided = true
		}
	}
	return out
}

func countDiffLines(lines []string) (added, removed int) {
	for _, line := range lines {
		if strings.HasPrefix(line, "+") && !strings.HasPrefix(line, "+++") {
			added++
		} else if strings.HasPrefix(line, "-") && !strings.HasPrefix(line, "---") {
			removed++
		}
	}
	return added, removed
}

// itoa avoids strconv import cycles in this hot path for tiny integers.
func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		buf[i] = '-'
	}
	return string(buf[i:])
}
