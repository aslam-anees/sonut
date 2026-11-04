package tools

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

type appendFileTool struct {
	baseTool
	workspaceRoot string
	scope         PathScope
}

func NewAppendFileTool(workspaceRoot string) Tool {
	return NewScopedAppendFileTool(workspaceRoot, nil)
}

func NewScopedAppendFileTool(workspaceRoot string, scope PathScope) Tool {
	return appendFileTool{
		baseTool: baseTool{
			name: "append_file",
			description: "Append content to an existing file, or create it if it does not exist. " +
				"Use this to write large files in chunks: call write_file for the first chunk, " +
				"then append_file for each subsequent chunk. After an oversized write_file, append only when " +
				"the recovery result explicitly says a NEW file prefix was saved. Existing files are preserved " +
				"on an aborted rewrite and must never receive the discarded partial fragment.",
			parameters: Schema{
				Type: "object",
				Properties: map[string]PropertySchema{
					"path":          {Type: "string", Description: "Absolute or relative path of the file to append to."},
					"content":       {Type: "string", Description: "Content to append. Must not be empty."},
					"reuse_content": {Type: "boolean", Description: "Reuse the exact append body saved by a failed call; omit content.", Default: false},
				},
				Required:             []string{"path"},
				AdditionalProperties: false,
			},
			safety: promptSafety(SideEffectWrite, "Appends content to files."),
		},
		workspaceRoot: normalizeWorkspaceRoot(workspaceRoot),
		scope:         scope,
	}
}

func (tool appendFileTool) Run(ctx context.Context, args map[string]any) Result {
	return tool.RunWithOptions(ctx, args, RunOptions{})
}

func (tool appendFileTool) RunWithOptions(_ context.Context, args map[string]any, options RunOptions) Result {
	requestedPath, err := aliasedStringArg(args, []string{"path", "file", "file_path", "filename"}, "", true, false)
	if err != nil {
		if body, bodyErr := fileContentArg(args); bodyErr == nil {
			_, _ = saveGeneratedRecovery(tool.workspaceRoot, "append_file", "", "", body)
		}
		return errorResult("Error: Invalid arguments for append_file: " + err.Error())
	}
	reuseContent, err := boolArg(args, "reuse_content", false)
	if err != nil {
		if body, bodyErr := fileContentArg(args); bodyErr == nil {
			_, _ = saveGeneratedRecovery(tool.workspaceRoot, "append_file", requestedPath, "", body)
		}
		return errorResult("Error: Invalid arguments for append_file: " + err.Error())
	}
	content, contentErr := fileContentArg(args)
	if reuseContent && contentErr != nil && strings.Contains(contentErr.Error(), "content is required") {
		contentErr = nil
	}
	if contentErr != nil {
		return errorResult("Error: Invalid arguments for append_file: " + contentErr.Error())
	}
	content, _ = stripToolProtocolResidue(content)
	if content == "" {
		if !reuseContent {
			return errorResult("Error: append_file content must not be empty.")
		}
	}
	preserveAppend := func(path, target, body string) bool {
		if body == "" {
			return false
		}
		_, saveErr := saveGeneratedRecovery(tool.workspaceRoot, "append_file", path, target, body)
		return saveErr == nil
	}
	recoveryArtifactPath := ""

	absolutePath, relativePath, err := resolveScopedTargetPath(tool.workspaceRoot, tool.scope, requestedPath, options.Cwd)
	if err != nil {
		message := "Error appending to file " + requestedPath + ": " + err.Error()
		if preserveAppend(requestedPath, "", content) {
			message += ". Your generated append content was SAVED. Retry with path " + requestedPath + ", reuse_content: true, and omit content. Do NOT regenerate it."
		}
		return errorResult(message)
	}
	if reuseContent && content == "" {
		if artifact, artifactPath, ok := loadLatestGeneratedRecovery(tool.workspaceRoot, "append_file", requestedPath, absolutePath); ok {
			content = artifact.Content
			recoveryArtifactPath = artifactPath
		} else {
			return errorResult("Error: no stashed append content to reuse for " + relativePath + " — resend the content field.")
		}
	}
	if content == "" {
		return errorResult("Error: append_file content must not be empty.")
	}
	if err := recheckScopedWriteTargetAt(tool.workspaceRoot, tool.scope, requestedPath, options.Cwd); err != nil {
		preserveAppend(requestedPath, absolutePath, content)
		return errorResult("Error appending to file " + relativePath + ": " + err.Error())
	}
	if err := os.MkdirAll(filepath.Dir(absolutePath), 0o755); err != nil {
		preserveAppend(requestedPath, absolutePath, content)
		return errorResult("Error appending to file " + relativePath + ": " + err.Error())
	}

	current, readErr := os.ReadFile(absolutePath)
	if readErr != nil {
		if os.IsNotExist(readErr) {
			current = nil
		} else {
			preserveAppend(requestedPath, absolutePath, content)
			return errorResult("Error reading file before append " + relativePath + ": " + readErr.Error())
		}
	}
	// Creating a new .html via append_file still requires a real HTML start.
	// Appending bare JS constants into a missing/broken .html is how supermario
	// thrash left a non-playable stub and then looped append forever.
	if HTMLPathNeedsRewrite(requestedPath, string(current)) {
		preserveAppend(requestedPath, absolutePath, content)
		return errorResult("Error: " + relativePath + " is not a valid HTML document (looks like raw JS/CSS). " +
			"Do NOT append more. Call write_file with overwrite: true and a complete HTML shell " +
			"(<!DOCTYPE html>…<canvas>…<script>…), then append_file only for logic inside the script. " +
			"Or write a .js file and link it with <script src>.")
	}
	if len(current) == 0 {
		if err := validateGeneratedContentPath(requestedPath, content); err != nil {
			preserveAppend(requestedPath, absolutePath, content)
			hint := generatedContentPathHint(requestedPath, content)
			msg := "Error: append_file cannot create " + relativePath + ": " + err.Error() + "."
			if hint != "" {
				msg += " " + hint
			}
			return errorResult(msg)
		}
	}
	if conflictErr := options.FileTracker.CheckConflict(absolutePath, current); conflictErr != nil {
		preserveAppend(requestedPath, absolutePath, content)
		return errorResult(fileConflictMessage(relativePath))
	}
	// Make chunk continuation idempotent across retries and resumed runs. The
	// in-run duplicate-call guard catches identical calls in one process, but a
	// reconnect/resume can replay the last successful append. Appending it again
	// silently duplicates CSS/JS/HTML and corrupts the output.
	if len(current) >= len(content) && bytes.HasSuffix(current, []byte(content)) {
		newInfo, _ := os.Stat(absolutePath)
		options.FileTracker.Record(absolutePath, current, newInfo)
		removeGeneratedRecovery(recoveryArtifactPath)
		removeGeneratedRecoveryArtifacts(tool.workspaceRoot, "append_file", requestedPath, absolutePath)
		return okResult("No changes needed: " + relativePath + " already ends with this append chunk.")
	}
	updated := append(append([]byte(nil), current...), []byte(content)...)
	if err := atomicWriteFile(absolutePath, updated, 0o644); err != nil {
		preserveAppend(requestedPath, absolutePath, content)
		return errorResult("Error appending to file " + relativePath + ": " + err.Error())
	}
	removeGeneratedRecovery(recoveryArtifactPath)
	removeGeneratedRecoveryArtifacts(tool.workspaceRoot, "append_file", requestedPath, absolutePath)

	totalLines := 0
	// Update the tracker so a later write/edit sees the current content. The same
	// read gives partial-stream recovery an exact continuation line without an
	// extra full-file read or another exploratory model turn.
	if options.FileTracker != nil {
		if newContent, rerr := os.ReadFile(absolutePath); rerr == nil {
			newInfo, _ := os.Stat(absolutePath)
			options.FileTracker.Record(absolutePath, newContent, newInfo)
			totalLines = bytes.Count(newContent, []byte{'\n'})
			if len(newContent) > 0 && newContent[len(newContent)-1] != '\n' {
				totalLines++
			}
		}
	}

	lines := strings.Count(content, "\n")
	if content != "" && !strings.HasSuffix(content, "\n") {
		lines++
	}
	summary := fmt.Sprintf("Appended to %s (%d lines).", relativePath, lines)
	result := okResult(summary)
	result.ChangedFiles = []string{relativePath}
	result.Display = Display{Summary: summary, Kind: "file"}
	if totalLines > 0 {
		result.Meta = map[string]string{"file.total_lines": fmt.Sprintf("%d", totalLines)}
	}
	return result
}
