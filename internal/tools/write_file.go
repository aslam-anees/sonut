package tools

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/aistatine/sonut/internal/sonuttrash"
)

// pendingWriteStashKey stores content from a write_file call that failed
// argument validation before a target path was known (e.g. the model omitted
// path entirely). NUL prefix guarantees it can never collide with a real
// absolute path.
const pendingWriteStashKey = "\x00pending-write"

type writeFileTool struct {
	baseTool
	workspaceRoot string
	scope         PathScope
}

func NewWriteFileTool(workspaceRoot string) Tool {
	return NewScopedWriteFileTool(workspaceRoot, nil)
}

func NewScopedWriteFileTool(workspaceRoot string, scope PathScope) Tool {
	return writeFileTool{
		baseTool: baseTool{
			name:        "write_file",
			description: "Create a new file or perform an explicitly requested complete replacement; for an existing-file fix, use edit_file or append_file and preserve the surrounding structure. The content field must match the file type: .html must start with a real HTML document (<!DOCTYPE html> or <html>…), never bare JS constants. For a single-file HTML game, first chunk = HTML shell with <canvas> and <script>; put game logic inside the script via append_file (or use a separate .js). For files longer than ~150 lines, write_file the first ≤100 lines, then append_file chunks. Never a stub/placeholder as the final product.",
			parameters: Schema{
				Type: "object",
				Properties: map[string]PropertySchema{
					"path":          {Type: "string", Description: "Absolute or relative path of the file to write."},
					"content":       {Type: "string", Description: "Complete, final file contents. Must be the full file — not a partial, stub, or placeholder. Required unless reuse_content is true (then OMIT it entirely). If you cannot produce the full content, use exec_command to download or generate it via shell instead."},
					"overwrite":     {Type: "boolean", Description: "Whether to allow overwriting an existing file. Set true to replace an existing file. Files this session already created or read are replaced without this flag.", Default: false},
					"reuse_content": {Type: "boolean", Description: "After a write was refused because the file already exists: set true (with overwrite: true) and omit content to rewrite the exact content from that refused call without resending it.", Default: false},
				},
				// content is deliberately NOT in Required: a reuse_content retry must
				// omit it, and advertising it as required pushes schema-faithful models
				// to regenerate the whole body (minutes on local models) instead of
				// reusing the stash. Missing content without reuse_content still fails
				// with a clear error at run time (fileContentArg).
				Required:             []string{"path"},
				AdditionalProperties: false,
			},
			safety: promptSafety(SideEffectWrite, "Creates or overwrites files."),
		},
		workspaceRoot: normalizeWorkspaceRoot(workspaceRoot),
		scope:         scope,
	}
}

func (tool writeFileTool) Run(ctx context.Context, args map[string]any) Result {
	return tool.RunWithOptions(ctx, args, RunOptions{})
}

func (tool writeFileTool) RunWithOptions(ctx context.Context, args map[string]any, options RunOptions) Result {
	requestedPath, err := aliasedStringArg(args, []string{"path", "file", "file_path", "filename"}, "", true, false)
	preserveGenerated := func(path, target, body string) bool {
		if body == "" {
			return false
		}
		stashKey := target
		if stashKey == "" {
			stashKey = pendingWriteStashKey
		}
		memorySaved := options.FileTracker.StashRejectedWrite(stashKey, body)
		_, diskErr := saveGeneratedRecovery(tool.workspaceRoot, "write_file", path, target, body)
		return memorySaved || diskErr == nil
	}
	if err != nil {
		// A missing/invalid path after the model already generated the body is the
		// expensive failure mode on local models (minutes of generation gone).
		// Stash the body under the pending key so the retry can send just the path
		// with reuse_content: true instead of regenerating the whole file.
		if body, cerr := fileContentArg(args); cerr == nil && body != "" &&
			preserveGenerated("", "", body) {
			return errorResult("Error: Invalid arguments for write_file: " + err.Error() +
				". Your content was SAVED — call write_file again with only the path, overwrite: true and reuse_content: true (omit content). Do NOT regenerate the file.")
		}
		return errorResult("Error: Invalid arguments for write_file: " + err.Error())
	}
	overwrite, err := boolArg(args, "overwrite", false)
	if err != nil {
		if body, bodyErr := fileContentArg(args); bodyErr == nil {
			preserveGenerated(requestedPath, "", body)
		}
		return errorResult("Error: Invalid arguments for write_file: " + err.Error())
	}
	reuseContent, err := boolArg(args, "reuse_content", false)
	if err != nil {
		if body, bodyErr := fileContentArg(args); bodyErr == nil {
			preserveGenerated(requestedPath, "", body)
		}
		return errorResult("Error: Invalid arguments for write_file: " + err.Error())
	}
	// Internal-only: a truncated streamed write contains just a prefix of the
	// intended file. It is useful for a new target that can be continued with
	// append_file, but it must never replace an existing valid file.
	partialRecovery, err := boolArg(args, "partial_recovery", false)
	if err != nil {
		if body, bodyErr := fileContentArg(args); bodyErr == nil {
			preserveGenerated(requestedPath, "", body)
		}
		return errorResult("Error: Invalid arguments for write_file recovery: " + err.Error())
	}
	// Read the body before resolving the path. Path mistakes are common in local
	// runs (for example, a model emits the full host path with a duplicated
	// workspace prefix), but the body may already have taken minutes to generate.
	// Preserve it before returning the scope error so the next call can use
	// reuse_content and send only the corrected path.
	content, contentErr := fileContentArg(args)
	if reuseContent && contentErr != nil && strings.Contains(contentErr.Error(), "content is required") {
		contentErr = nil
	}
	if contentErr != nil {
		return errorResult("Error: Invalid arguments for write_file: " + contentErr.Error())
	}
	recoveryArtifactPath := ""

	absolutePath, relativePath, err := resolveScopedTargetPath(tool.workspaceRoot, tool.scope, requestedPath, options.Cwd)
	if err != nil {
		message := "Error writing file " + requestedPath + ": " + err.Error()
		if preserveGenerated(requestedPath, "", content) {
			hint := workspacePathHint(tool.workspaceRoot, tool.scope, requestedPath)
			if hint == "" {
				hint = "a path relative to the workspace root"
			}
			message += ". Your generated content was SAVED. Retry with path " + hint +
				", overwrite: true, reuse_content: true, and omit content. Do NOT regenerate the file."
		}
		return errorResult(message)
	}

	if reuseContent && content == "" {
		// A retry after an already-exists refusal may omit content (or send it
		// empty) and reuse the stashed body from the refused call, so a large file
		// is generated and transmitted only once.
		if stashed, ok := options.FileTracker.TakeRejectedWrite(absolutePath); ok {
			content, contentErr = stashed, nil
		} else if stashed, ok := options.FileTracker.TakeRejectedWrite(pendingWriteStashKey); ok {
			// Retry after an invalid-arguments refusal (e.g. missing path): the body
			// was stashed before any path was known, so it lives under the pending key.
			content, contentErr = stashed, nil
		} else {
			if artifact, artifactPath, ok := loadLatestGeneratedRecovery(tool.workspaceRoot, "write_file", requestedPath, absolutePath); ok {
				content, contentErr = artifact.Content, nil
				recoveryArtifactPath = artifactPath
			} else {
				return errorResult("Error: no stashed content to reuse for " + relativePath + " — resend the full content in the content field.")
			}
		}
	}
	if contentErr != nil {
		return errorResult("Error: Invalid arguments for write_file: " + contentErr.Error())
	}
	content, strippedProtocol := stripToolProtocolResidue(content)
	if strippedProtocol && content == "\n" {
		return errorResult("Error: write_file content contained only tool protocol markup; resend the actual file body.")
	}
	if err := validateGeneratedContentPath(requestedPath, content); err != nil {
		if preserveGenerated(requestedPath, absolutePath, content) {
			next := " The generated body is preserved. Do not read or regenerate it. Call write_file with only the corrected path, reuse_content: true, and overwrite: true; omit content. If this is an existing HTML file and the body is a CSS/JS feature block, use edit_file to place it in the existing document instead of retrying write_file."
			if hint := generatedContentPathHint(requestedPath, content); hint != "" {
				next += " " + hint
			}
			return errorResult("Error: write_file content/path mismatch for " + requestedPath + ": " + err.Error() + "." + next)
		}
		return errorResult("Error: write_file content/path mismatch for " + requestedPath + ": " + err.Error() + ". Send content that matches the file extension.")
	}
	if partialRecovery {
		if _, statErr := os.Stat(absolutePath); statErr == nil {
			preserveGenerated(requestedPath, absolutePath, content)
			return errorResult("Error: truncated write recovery was not applied because " + relativePath +
				" already exists; the existing file was preserved unchanged. Re-plan from the current file. " +
				"Use a small edit_file/apply_patch change, or create a separate asset and link it. " +
				"Do not overwrite this file with the incomplete generated prefix.")
		} else if !os.IsNotExist(statErr) {
			preserveGenerated(requestedPath, absolutePath, content)
			return errorResult("Error checking " + relativePath + " before truncated write recovery: " + statErr.Error())
		}
	}

	// Capture the prior content (before we replace it) so an overwrite can show a
	// real diff; a fresh create stays "" and previews as all-additions.
	priorContent := ""
	existed := false
	if _, err := os.Stat(absolutePath); err == nil {
		existed = true
		current, rerr := os.ReadFile(absolutePath)
		if _, tracked := options.FileTracker.Version(absolutePath); tracked {
			if rerr == nil && string(current) == content {
				newInfo, _ := os.Stat(absolutePath)
				options.FileTracker.Record(absolutePath, current, newInfo)
				return okResult("No changes needed: " + relativePath + " already contains the requested content.")
			}
			// Refuse to clobber a tracked file that changed on disk outside @code
			// since it was last read — the new content was likely composed against
			// a stale view. Fail CLOSED: if the tracked file can't be re-read to
			// verify it, refuse rather than clobber a file whose state is unknown.
			if rerr != nil {
				preserveGenerated(requestedPath, absolutePath, content)
				return errorResult(fileConflictMessage(relativePath))
			}
			if cerr := options.FileTracker.CheckConflict(absolutePath, current); cerr != nil {
				preserveGenerated(requestedPath, absolutePath, content)
				return errorResult(fileConflictMessage(relativePath))
			}
			// Tracked and unchanged: this session owns the current content, so
			// replacing it does not need the overwrite flag — the baseline check
			// above already provides the protection the flag exists for.
		} else if !overwrite {
			// User confirmation flow: if we have an interactive user, ask them
			// whether to overwrite instead of relying on the model to comply with
			// the error message (which small models often ignore).
			if options.OnConfirmation != nil {
				// Build a confirmation question asking the user about overwriting
				question := relativePath + " already exists. Overwrite with the newly generated content?"
				confirmed, confErr := options.OnConfirmation(ctx, question)
				if confErr != nil {
					// If confirmation fails, fall back to error message
					message := "Error: " + relativePath + " already exists. Call write_file again with overwrite: true to replace it"
					if preserveGenerated(requestedPath, absolutePath, content) {
						message += "; add reuse_content: true and omit content to reuse the exact content from this call without resending it"
					}
					return errorResult(message + ".")
				}
				if !confirmed {
					// User declined — return a short refusal and optionally stash content
					message := "User declined overwriting " + relativePath + ". Ask the user which file to write instead."
					// Stash the content for potential reuse if user retries with a different path
					preserveGenerated(requestedPath, absolutePath, content)
					return errorResult(message)
				}
				// User confirmed — continue with the overwrite below
			} else {
				// No interactive user available — fall back to error message
				message := "Error: " + relativePath + " already exists. Call write_file again with overwrite: true to replace it"
				if preserveGenerated(requestedPath, absolutePath, content) {
					message += "; add reuse_content: true and omit content to reuse the exact content from this call without resending it"
				}
				return errorResult(message + ".")
			}
		}
		if rerr == nil {
			priorContent = string(current)
		}
		// Back up the original into the TTL bin so overwrites are recoverable for 24h.
		_ = sonuttrash.Put(tool.workspaceRoot, absolutePath)
		// Sweep expired entries in the background (best-effort).
		go sonuttrash.Sweep(tool.workspaceRoot)
	} else if !os.IsNotExist(err) {
		preserveGenerated(requestedPath, absolutePath, content)
		return errorResult("Error writing file " + relativePath + ": " + err.Error())
	}

	if err := os.MkdirAll(filepath.Dir(absolutePath), 0o755); err != nil {
		preserveGenerated(requestedPath, absolutePath, content)
		return errorResult("Error writing file " + relativePath + ": " + err.Error())
	}
	if err := recheckScopedWriteTargetAt(tool.workspaceRoot, tool.scope, requestedPath, options.Cwd); err != nil {
		message := "Error writing file " + relativePath + ": " + err.Error()
		if preserveGenerated(requestedPath, absolutePath, content) {
			hint := workspacePathHint(tool.workspaceRoot, tool.scope, requestedPath)
			if hint == "" {
				hint = "a path relative to the workspace root"
			}
			message += ". Your generated content was SAVED. Retry with path " + hint +
				", overwrite: true, reuse_content: true, and omit content. Do NOT regenerate the file."
		}
		return errorResult(message)
	}
	// Recheck immediately before the atomic mutation as a final invariant. The
	// streamed and ordinary dispatch paths must never publish an HTML body at an
	// extensionless target, even if an earlier validation stage was bypassed.
	if err := validateGeneratedContentPath(requestedPath, content); err != nil {
		preserveGenerated(requestedPath, "", content)
		return errorResult("Error: write_file content/path mismatch for " + requestedPath + ": " + err.Error() + ". Send content that matches the file extension.")
	}
	if err := atomicWriteFile(absolutePath, []byte(content), 0o644); err != nil {
		preserveGenerated(requestedPath, absolutePath, content)
		return errorResult("Error writing file " + relativePath + ": " + err.Error())
	}
	removeGeneratedRecovery(recoveryArtifactPath)
	removeGeneratedRecoveryArtifacts(tool.workspaceRoot, "write_file", requestedPath, absolutePath)
	// Baseline the freshly written content so a later edit/overwrite in this
	// session compares against what is now on disk.
	newInfo, _ := os.Stat(absolutePath)
	options.FileTracker.Record(absolutePath, []byte(content), newInfo)

	verb := "Created"
	if existed {
		verb = "Overwrote"
	}
	// Report line count (not bytes): "Wrote 282 lines" reads as real work at a
	// glance, where a byte total is opaque noise.
	lines := strings.Count(content, "\n")
	if content != "" && !strings.HasSuffix(content, "\n") {
		lines++
	}
	summary := fmt.Sprintf("%s %s (%d lines).", verb, relativePath, lines)
	result := okResult(summary)
	result.ChangedFiles = []string{relativePath}
	// Card-only preview: a real unified diff (all-green for a create, red/green for
	// an overwrite) on Display.Preview. Output stays the summary, so the model never
	// re-reads the file — the rich preview costs sonut model tokens.
	result.Display = Display{Summary: summary, Kind: "file", Preview: boundedUnifiedDiff(relativePath, priorContent, content)}
	return result
}

// fileContentArg reads the file body from "content" or a common alias that weaker
// models sometimes use instead (contents/text/body/data/file_content). It
// delegates to the shared aliasedStringArg so the present-but-non-string type
// error ("content must be a string") and the required-but-missing error
// ("content is required") stay consistent with every other tool. An empty string
// is allowed (writing an empty file), so allowEmpty is true.
func fileContentArg(args map[string]any) (string, error) {
	return aliasedStringArg(args, []string{"content", "contents", "text", "body", "data", "file_content"}, "", true, true)
}
