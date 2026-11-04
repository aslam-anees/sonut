package tools

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"unicode"
	"unicode/utf8"
)

type editFileTool struct {
	baseTool
	workspaceRoot string
	scope         PathScope
}

func NewEditFileTool(workspaceRoot string) Tool {
	return NewScopedEditFileTool(workspaceRoot, nil)
}

func NewScopedEditFileTool(workspaceRoot string, scope PathScope) Tool {
	return editFileTool{
		baseTool: baseTool{
			name: "edit_file",
			// FIRST sentence only survives local hybrid schema compaction — put
			// the reuse/minimal-diff rule there. Giant edit_file args burn minutes
			// and can turn one feature change into a whole-file rewrite.
			description: "Edit existing files surgically: reuse the current code and replace only a unique 1-3 line anchor; never regenerate the whole file in old_string/new_string. For a deliberate full rewrite, call write_file with overwrite:true.",
			parameters: Schema{
				Type: "object",
				Properties: map[string]PropertySchema{
					"path":       {Type: "string", Description: "Path of the file to edit."},
					"old_string": {Type: "string", Description: "Exact unique 1-3 line anchor from the current file (byte-for-byte); never send the whole file."},
					"new_string": {Type: "string", Description: "Small replacement for that anchor only. Reuse existing code; full documents go in write_file."},
					// Models trained on other file-edit harnesses commonly call this
					// replacement "content" or "new_content". Advertise the safe
					// aliases so the tolerant decoder below can recover the call instead
					// of surfacing a schema/argument error.
					"new_content":         {Type: "string", Description: "Alias for new_string."},
					"replacement_content": {Type: "string", Description: "Alias for new_string."},
					"content":             {Type: "string", Description: "Alias for new_string; use only for a small replacement."},
					"contents":            {Type: "string", Description: "Alias for new_string; use only for a small replacement."},
					"replace_all":         {Type: "boolean", Description: "Replace every occurrence instead of requiring uniqueness.", Default: false},
					"reuse_replacement":   {Type: "boolean", Description: "Reuse the exact replacement saved by a failed edit; omit new_string and provide a fresh old_string.", Default: false},
				},
				// new_string remains the canonical field, but aliases are accepted for
				// compatibility with equivalent edit tools. Run still refuses a call
				// that contains no replacement at all; it never guesses destructive data.
				Required:             []string{"path", "old_string"},
				AdditionalProperties: false,
			},
			safety: promptSafety(SideEffectWrite, "Edits files in place."),
		},
		workspaceRoot: normalizeWorkspaceRoot(workspaceRoot),
		scope:         scope,
	}
}

func (tool editFileTool) Run(ctx context.Context, args map[string]any) Result {
	return tool.RunWithOptions(ctx, args, RunOptions{})
}

func (tool editFileTool) RunWithOptions(ctx context.Context, args map[string]any, options RunOptions) Result {
	requestedPath, err := aliasedStringArg(args, []string{"path", "file", "file_path", "filename", "filepath", "target_file"}, "", false, true)
	if err != nil {
		if replacement, replacementErr := aliasedStringArg(args, []string{"new_string", "new_content", "replacement_content", "content", "contents", "new", "replace", "replacement", "new_str"}, "", true, true); replacementErr == nil {
			_, _ = saveGeneratedRecovery(tool.workspaceRoot, "edit_file", "", "", replacement)
		}
		return errorResult("Error: Invalid arguments for edit_file: " + err.Error())
	}
	oldString, err := aliasedStringArg(args, []string{"old_string", "old", "search", "find", "old_str"}, "", true, false)
	if err != nil {
		if replacement, replacementErr := aliasedStringArg(args, []string{"new_string", "new_content", "replacement_content", "content", "contents", "new", "replace", "replacement", "new_str"}, "", true, true); replacementErr == nil {
			_, _ = saveGeneratedRecovery(tool.workspaceRoot, "edit_file", requestedPath, "", replacement)
		}
		if strings.Contains(err.Error(), "is required") && options.ToolCallID != "" {
			// A live model can omit the anchor after reading a file. This is a
			// recoverable argument-shape mistake, not a filesystem failure: keep
			// the file untouched and return a successful tool result carrying the
			// exact retry contract. That prevents a red activity error and lets
			// the next turn reuse the already-read file instead of re-generating it.
			return Result{
				Status: StatusOK,
				Output: "No edit applied: old_string was missing. Retry edit_file with path, old_string, and new_string (or new_content/content). Use a unique 1-3 line anchor from the file you just read; for a complete document use write_file with overwrite:true. No file was changed.",
				Meta: map[string]string{
					"internal_dedup":      "true",
					"retryable_arguments": "true",
				},
			}
		}
		return errorResult("Error: Invalid arguments for edit_file: " + err.Error())
	}
	reuseReplacement, err := boolArg(args, "reuse_replacement", false)
	if err != nil {
		return errorResult("Error: Invalid arguments for edit_file: " + err.Error())
	}
	newString, err := aliasedStringArg(args, []string{"new_string", "new_content", "replacement_content", "content", "contents", "new", "replace", "replacement", "new_str"}, "", true, true)
	if err != nil {
		if reuseReplacement && strings.Contains(err.Error(), "is required") {
			newString = ""
			err = nil
		} else if strings.Contains(err.Error(), "is required") {
			// A missing replacement is recoverable model-shape noise, not a
			// filesystem failure. Keep the call side-effect free, return a quiet
			// correction, and let the agent issue the same edit with the required
			// replacement instead of showing a red tool error to the user.
			return Result{
				Status: StatusOK,
				Output: "No edit applied: the replacement text was missing. Retry edit_file with path, old_string, and new_string (or new_content/content). No file was changed.",
				Meta:   map[string]string{"internal_dedup": "true", "retryable_arguments": "true"},
			}
		}
		if err != nil {
			return errorResult("Error: Invalid arguments for edit_file: " + err.Error())
		}
	}
	replaceAll, err := boolArg(args, "replace_all", false)
	if err != nil {
		if newString != "" {
			_, _ = saveGeneratedRecovery(tool.workspaceRoot, "edit_file", requestedPath, "", newString)
		}
		return errorResult("Error: Invalid arguments for edit_file: " + err.Error())
	}
	preserveReplacement := func(path, target, body string) string {
		if strings.TrimSpace(body) == "" {
			return ""
		}
		artifactPath, saveErr := saveGeneratedRecovery(tool.workspaceRoot, "edit_file", path, target, body)
		if saveErr != nil {
			return ""
		}
		return artifactPath
	}

	// Path omitted: recover from session context instead of failing the turn.
	// Small models routinely drop path when only one file is in play. The
	// old_string match against the inferred file's real content is the safety
	// net — a wrong inference cannot silently corrupt anything, it just errors
	// "old_string not found" like any mistaken edit.
	if requestedPath == "" {
		inferred := inferEditTarget(options.FileTracker, oldString, tool.workspaceRoot)
		if inferred == "" {
			preserveReplacement("", "", newString)
			return errorResult("Error: Invalid arguments for edit_file: path is required — pass the file you want to edit (e.g. the file you last read with read_file)")
		}
		requestedPath = inferred
	}
	absolutePath, relativePath, err := resolveScopedPath(tool.workspaceRoot, tool.scope, requestedPath, options.Cwd)
	if err != nil {
		preserveReplacement(requestedPath, "", newString)
		return errorResult("Error reading " + requestedPath + ": " + err.Error())
	}
	recoveryArtifactPath := ""
	if reuseReplacement && newString == "" {
		artifact, artifactPath, ok := loadLatestGeneratedRecovery(tool.workspaceRoot, "edit_file", requestedPath, absolutePath)
		if !ok {
			return errorResult("Error: no saved edit replacement found for " + relativePath + "; resend new_string.")
		}
		newString = artifact.Content
		recoveryArtifactPath = artifactPath
	}
	contentBytes, err := os.ReadFile(absolutePath)
	if err != nil {
		preserveReplacement(requestedPath, absolutePath, newString)
		return errorResult("Error reading " + relativePath + ": " + err.Error())
	}
	content := string(contentBytes)
	occurrences := strings.Count(content, oldString)
	staleBaseline := options.FileTracker.CheckConflict(absolutePath, contentBytes) != nil
	if staleBaseline && occurrences == 0 && newString != "" && strings.Contains(content, newString) {
		// The same edit was already applied outside this invocation (commonly by a
		// formatter or a recovered/replayed call). Treat it as an idempotent
		// success and adopt the current file as the new session baseline.
		info, _ := os.Stat(absolutePath)
		options.FileTracker.Record(absolutePath, contentBytes, info)
		return okResult(fmt.Sprintf("No changes needed: %s already contains new_string — this edit was already applied. Do not retry it; move to the next step.", relativePath))
	}
	// A whole-file overwrite must fail on stale content, but a surgical edit can
	// safely rebase onto the latest file when its exact anchor is still unique.
	// This preserves unrelated formatter/user changes while avoiding a needless
	// read→retry round trip. Fuzzy matching remains disabled on a stale baseline.
	if staleBaseline && occurrences != 1 {
		preserveReplacement(requestedPath, absolutePath, newString)
		return errorResult(fileConflictMessage(relativePath))
	}
	// A complete document must never be inserted inside an existing file as a
	// surgical replacement. Route it through write_file so the already-generated
	// body is reused once, while write_file retains the normal overwrite/conflict
	// and trash protections. This also fixes the case where old_string happens to
	// match once: strings.Replace would otherwise nest an entire HTML document.
	if looksLikeCompleteFileReplacement(relativePath, newString) {
		result := tool.applyFullFileFallback(ctx, requestedPath, newString, options)
		if result.Status == StatusOK {
			removeGeneratedRecoveryArtifacts(tool.workspaceRoot, "edit_file", requestedPath, absolutePath)
		}
		return result
	}

	// Fuzzy fallback: if exact match fails, try normalised-whitespace matching.
	// This handles the common LLM failure of generating slightly different
	// indentation or line endings than the actual file content.
	if occurrences == 0 {
		fuzzyCount := fuzzyCount(content, oldString)
		if !replaceAll && fuzzyCount > 1 {
			preserveReplacement(requestedPath, absolutePath, newString)
			return errorResult(fmt.Sprintf("Error: old_string matches %d locations (fuzzy) in %s. Either make old_string more specific, or pass replace_all: true to replace every occurrence.", fuzzyCount, relativePath))
		}
		if updated, found := fuzzyReplace(content, oldString, newString, replaceAll); found {
			if updated == content {
				return identicalEditNoopResult(relativePath, content)
			}
			if err := recheckScopedWriteTargetAt(tool.workspaceRoot, tool.scope, requestedPath, options.Cwd); err != nil {
				preserveReplacement(requestedPath, absolutePath, newString)
				return errorResult("Error writing " + relativePath + ": " + err.Error())
			}
			if err := atomicWriteFile(absolutePath, []byte(updated), 0o644); err != nil {
				preserveReplacement(requestedPath, absolutePath, newString)
				return errorResult("Error writing " + relativePath + ": " + err.Error())
			}
			removeGeneratedRecovery(recoveryArtifactPath)
			removeGeneratedRecoveryArtifacts(tool.workspaceRoot, "edit_file", requestedPath, absolutePath)
			newInfo, _ := os.Stat(absolutePath)
			options.FileTracker.Record(absolutePath, []byte(updated), newInfo)
			summary := fmt.Sprintf("Successfully edited %s.", relativePath)
			result := okResult(summary)
			result.ChangedFiles = []string{relativePath}
			result.Display = Display{Summary: fmt.Sprintf("Edited %s", relativePath), Kind: "diff", Preview: boundedUnifiedDiff(relativePath, content, updated)}
			return result
		}

		// Already applied: the model often re-issues an edit that already
		// succeeded (old_string is gone because it was replaced). Detecting the
		// new_string on disk turns that retry into a no-op success instead of an
		// error that sends the model into a correction spiral.
		if newString != "" && strings.Contains(content, newString) {
			return okResult(fmt.Sprintf("No changes needed: %s already contains new_string — this edit was already applied. Do not retry it; move to the next step.", relativePath))
		}

		// Hard failure: show the first 300 chars for debugging.
		// If the completed replacement is clearly a whole local document, recover
		// it as an atomic write instead of asking the model to regenerate after a
		// stale edit anchor. This path still goes through write_file's workspace,
		// conflict, trash, and tracker checks; it is never used for a small or
		// ambiguous fragment.
		preview := content
		if len(preview) > 300 {
			preview = preview[:300] + "\n… (truncated)"
		}
		message := fmt.Sprintf(
			"Error: old_string not found in %s — it must match the file content byte-for-byte (including whitespace and newlines).\n"+
				"For a FULL FILE rewrite, use write_file instead of edit_file.\n"+
				"Do NOT retry this same old_string. Read the current file and choose a fresh 1-3 line anchor, or send the complete file to write_file with overwrite:true.\n"+
				"Current file content (first 300 chars):\n%s",
			relativePath, preview)
		recoveryPath := preserveReplacement(requestedPath, absolutePath, newString)
		if recoveryPath != "" {
			message += "\nThe exact generated replacement was preserved. Read the current file, choose a fresh unique anchor, then call edit_file with reuse_replacement:true and omit new_string; do not regenerate it."
		} else if newString != "" {
			message += "\nKeep and reuse the exact new_string from this call with a fresh anchor; do not regenerate it."
		}
		// A live agent call is recoverable model-shape noise. Returning a successful
		// internal result keeps the TUI/activity feed clean while still sending the
		// corrective text back to the model. Direct library callers retain the
		// historical error status so misuse remains observable in tests and APIs.
		if options.ToolCallID != "" {
			return Result{
				Status: StatusOK,
				Output: "No edit applied: " + strings.TrimPrefix(message, "Error: "),
				Meta: map[string]string{
					"internal_dedup":      "true",
					"edit_anchor_miss":    "true",
					"retryable_arguments": "true",
					"replacement_saved":   fmt.Sprintf("%t", recoveryPath != ""),
				},
			}
		}
		return errorResult(message)
	}
	if !replaceAll && occurrences > 1 {
		preserveReplacement(requestedPath, absolutePath, newString)
		return errorResult(fmt.Sprintf("Error: old_string matches %d locations in %s. Either make old_string more specific, or pass replace_all: true to replace every occurrence.", occurrences, relativePath))
	}

	updated := strings.Replace(content, oldString, newString, 1)
	replacedCount := 1
	if replaceAll {
		updated = strings.ReplaceAll(content, oldString, newString)
		replacedCount = occurrences
	}
	if updated == content {
		return identicalEditNoopResult(relativePath, content)
	}
	if err := recheckScopedWriteTargetAt(tool.workspaceRoot, tool.scope, requestedPath, options.Cwd); err != nil {
		preserveReplacement(requestedPath, absolutePath, newString)
		return errorResult("Error writing " + relativePath + ": " + err.Error())
	}
	if err := atomicWriteFile(absolutePath, []byte(updated), 0o644); err != nil {
		preserveReplacement(requestedPath, absolutePath, newString)
		return errorResult("Error writing " + relativePath + ": " + err.Error())
	}
	removeGeneratedRecovery(recoveryArtifactPath)
	removeGeneratedRecoveryArtifacts(tool.workspaceRoot, "edit_file", requestedPath, absolutePath)
	// Re-baseline to the content we just wrote so subsequent edits in this session
	// compare against the current on-disk state, not the pre-edit version.
	newInfo, _ := os.Stat(absolutePath)
	options.FileTracker.Record(absolutePath, []byte(updated), newInfo)

	suffix := ""
	if replacedCount != 1 {
		suffix = "s"
	}
	summary := fmt.Sprintf("Successfully edited %s (replaced %d occurrence%s).", relativePath, replacedCount, suffix)
	result := okResult(summary)
	result.ChangedFiles = []string{relativePath}
	// Card-only preview (Display.Preview): the model's Output stays the one-line
	// summary, so the red/green diff costs sonut model tokens.
	result.Display = Display{Summary: fmt.Sprintf("Edited %s", relativePath), Kind: "diff", Preview: boundedUnifiedDiff(relativePath, content, updated)}
	return result
}

// identicalEditNoopResult is the live supermario thrash: model re-issues
// edit_file with old_string == new_string (or a fuzzy no-op) three times until
// the consecutive-call guard hard-stops the run. Status stays OK so the TUI
// stays clean, but Meta + output force a strategy change: append continuation
// for truncated files, or write_file overwrite for a full rewrite.
func identicalEditNoopResult(relativePath, content string) Result {
	msg := "No changes: new_string is identical to old_string — this edit does nothing. " +
		"Do NOT re-issue the same edit_file call."
	if looksLikeTruncatedGeneratedFile(relativePath, content) {
		msg += " The file looks TRUNCATED mid-generation. Call append_file with the NEXT chunk of real code " +
			"(continue from where it ends — do not resend the existing prefix). " +
			"Or call write_file with overwrite:true and a COMPLETE file body."
	} else {
		msg += " To add more content use append_file. To rewrite the whole file use write_file with overwrite:true. " +
			"To change something, pick a different 1-3 line old_string that actually needs replacing."
	}
	return Result{
		Status: StatusOK,
		Output: msg,
		Meta: map[string]string{
			"internal_dedup":     "true",
			"edit_noop":          "true",
			"retryable_strategy": "append_or_write",
		},
	}
}

// looksLikeTruncatedGeneratedFile detects incomplete HTML/JS that the model
// tried to "edit" instead of continuing. Live: supermario.html ending with
// `const levelData = [` and no closing tags.
func looksLikeTruncatedGeneratedFile(relativePath, content string) bool {
	trimmed := strings.TrimSpace(content)
	if trimmed == "" {
		return false
	}
	ext := strings.ToLower(filepath.Ext(relativePath))
	lower := strings.ToLower(trimmed)
	// Ends mid-structure: open bracket/brace/paren/comma or assignment without closer.
	last := trimmed[len(trimmed)-1]
	openEnded := last == '[' || last == '{' || last == '(' || last == ',' || last == '=' || last == ':'
	switch ext {
	case ".html", ".htm":
		if strings.Contains(lower, "<html") && !strings.Contains(lower, "</html>") {
			return true
		}
		if strings.Contains(lower, "<script") && !strings.Contains(lower, "</script>") {
			return true
		}
		return openEnded && LooksLikeHTMLDocument(content)
	case ".js", ".ts", ".mjs", ".cjs":
		return openEnded || strings.Count(trimmed, "{") > strings.Count(trimmed, "}")
	default:
		return openEnded && len(trimmed) < 8*1024
	}
}

func (tool editFileTool) applyFullFileFallback(ctx context.Context, requestedPath, replacement string, options RunOptions) Result {
	writeTool := NewScopedWriteFileTool(tool.workspaceRoot, tool.scope).(optionsAwareTool)
	result := writeTool.RunWithOptions(ctx, map[string]any{
		"path":      requestedPath,
		"content":   replacement,
		"overwrite": true,
	}, options)
	if result.Status == StatusOK {
		if result.Meta == nil {
			result.Meta = make(map[string]string)
		}
		result.Meta["edit_full_file_fallback"] = "true"
		result.Output = "Applied the complete replacement as a full-file write; the generated body was reused once. " + result.Output
	}
	return result
}

// looksLikeCompleteFileReplacement recognizes a complete local document so the
// edit tool can redirect it to the full-file writer. Full rewrites remain
// allowed; ordinary feature blocks still use the surgical edit path.
func looksLikeCompleteFileReplacement(relativePath, content string) bool {
	lower := strings.ToLower(content)
	switch strings.ToLower(filepath.Ext(relativePath)) {
	case ".html", ".htm":
		return (strings.Contains(lower, "<!doctype html>") || strings.Contains(lower, "<html")) &&
			strings.Contains(lower, "</html>")
	case ".css":
		if len(content) < 4*1024 || strings.Count(content, "\n") < 20 {
			return false
		}
		return strings.Contains(content, "{") && strings.Contains(content, "}")
	case ".js", ".jsx", ".ts", ".tsx":
		if len(content) < 4*1024 || strings.Count(content, "\n") < 20 {
			return false
		}
		return strings.Contains(content, "{") && strings.Contains(content, "}")
	default:
		return false
	}
}

// inferEditTarget picks the file an omitted-path edit_file call meant. The file
// whose current content contains old_string (exactly or fuzzily) wins when that
// match is unique across the session's tracked files; otherwise the most
// recently read/written file is the model's working file. Returns "" when the
// session has no usable candidate.
func inferEditTarget(tracker *FileTracker, oldString string, workspaceRoot string) string {
	var matches []string
	for _, path := range tracker.TrackedPaths() {
		content, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		text := string(content)
		if strings.Contains(text, oldString) || fuzzyCount(text, oldString) > 0 {
			matches = append(matches, path)
		}
	}
	target := tracker.LastRecordedPath()
	if len(matches) == 1 {
		target = matches[0]
	}
	if target == "" {
		return ""
	}
	// Tracked paths are absolute; re-express inside the workspace so the scope
	// checks see the same form a model-supplied relative path would produce
	// (absolute forms can differ through symlinks, e.g. /var vs /private/var).
	resolved := target
	if r, err := filepath.EvalSymlinks(target); err == nil {
		resolved = r
	}
	root := workspaceRoot
	if r, err := filepath.EvalSymlinks(workspaceRoot); err == nil {
		root = r
	}
	if rel, err := filepath.Rel(root, resolved); err == nil && !strings.HasPrefix(rel, "..") {
		return rel
	}
	return target
}

// fuzzyReplace attempts a whitespace-normalised search-and-replace when exact
// matching fails. It walks the original content and oldString simultaneously,
// collapsing whitespace runs, so byte offsets are always derived directly from
// the original rather than via a fragile index map.
func fuzzyReplace(content, oldString, newString string, replaceAll bool) (string, bool) {
	spans := fuzzySpans(content, oldString, replaceAll)
	if len(spans) == 0 {
		return "", false
	}
	result := content
	for i := len(spans) - 1; i >= 0; i-- {
		s := spans[i]
		result = result[:s.start] + newString + result[s.end:]
	}
	return result, true
}

// fuzzyCount returns how many whitespace-normalised occurrences of oldString
// exist in content.
func fuzzyCount(content, oldString string) int {
	return len(fuzzySpans(content, oldString, true))
}

type fuzzySpan struct{ start, end int }

// fuzzySpans finds byte spans in content that match oldString under whitespace
// normalisation. It advances through content rune-by-rune, collapsing whitespace
// runs to single spaces, and matches them against the likewise-normalised old string.
func fuzzySpans(content, oldString string, all bool) []fuzzySpan {
	normOld := normalizeWS(oldString)
	if normOld == "" {
		return nil
	}
	normOldRunes := []rune(normOld)

	// Build a parallel slice of (rune, byteOffset) for the original content so
	// we can translate rune positions back to byte offsets cheaply.
	type runePos struct {
		r   rune
		off int // byte offset in content
	}
	crunes := make([]runePos, 0, len(content))
	for off, r := range content {
		crunes = append(crunes, runePos{r, off})
	}

	var spans []fuzzySpan

	ci := 0
	for ci < len(crunes) {
		// normOld is trimmed, so it never starts with a space. Skip leading
		// whitespace in content so we can anchor the search at non-space chars.
		if unicode.IsSpace(crunes[ci].r) {
			ci++
			continue
		}

		// Try to match normOldRunes starting at crunes[ci].
		ni := 0
		cj := ci
		for ni < len(normOldRunes) && cj < len(crunes) {
			nr := normOldRunes[ni]
			if nr == ' ' {
				// Pattern expects a whitespace run — content must have at least one.
				if !unicode.IsSpace(crunes[cj].r) {
					break
				}
				for cj < len(crunes) && unicode.IsSpace(crunes[cj].r) {
					cj++
				}
				ni++
			} else {
				if crunes[cj].r != nr {
					break
				}
				cj++
				ni++
			}
		}

		if ni == len(normOldRunes) {
			startByte := crunes[ci].off
			// End byte is the byte after the last consumed rune.
			endByte := crunes[cj-1].off + utf8.RuneLen(crunes[cj-1].r)
			spans = append(spans, fuzzySpan{startByte, endByte})
			if !all {
				break
			}
			ci = cj
		} else {
			ci++
		}
	}
	return spans
}

// normalizeWS collapses every run of whitespace to a single space and trims.
func normalizeWS(s string) string {
	var b strings.Builder
	inSpace := false
	for _, r := range s {
		if unicode.IsSpace(r) {
			if !inSpace {
				b.WriteRune(' ')
				inSpace = true
			}
		} else {
			b.WriteRune(r)
			inSpace = false
		}
	}
	return strings.TrimSpace(b.String())
}
