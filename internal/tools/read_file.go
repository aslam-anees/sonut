package tools

import (
	"context"
	"fmt"
	"os"
	"strconv"
	"strings"

	"github.com/aistatine/sonut/internal/security"
)

type readFileTool struct {
	baseTool
	workspaceRoot string
	scope         PathScope
}

func NewReadFileTool(workspaceRoot string) Tool {
	return NewScopedReadFileTool(workspaceRoot, nil)
}

func NewScopedReadFileTool(workspaceRoot string, scope PathScope) Tool {
	return readFileTool{
		baseTool: baseTool{
			name:        "read_file",
			description: "Read a file with optional 1-based inclusive line range and max line cap.",
			parameters: Schema{
				Type: "object",
				Properties: map[string]PropertySchema{
					"path":       {Type: "string", Description: "Path of the file to read."},
					"start_line": {Type: "integer", Description: "1-based inclusive line number to start reading from.", Minimum: intPtr(1)},
					"end_line":   {Type: "integer", Description: "1-based inclusive line number to stop reading at.", Minimum: intPtr(1)},
					"max_lines":  {Type: "integer", Description: "Maximum number of lines to return.", Minimum: intPtr(1)},
				},
				Required:             []string{"path"},
				AdditionalProperties: false,
			},
			safety: readOnlySafety("Reads file contents without modifying files."),
		},
		workspaceRoot: normalizeWorkspaceRoot(workspaceRoot),
		scope:         scope,
	}
}

func (tool readFileTool) Run(ctx context.Context, args map[string]any) Result {
	return tool.RunWithOptions(ctx, args, RunOptions{})
}

func (tool readFileTool) RunWithOptions(_ context.Context, args map[string]any, options RunOptions) Result {
	requestedPath, err := aliasedStringArg(args, []string{"path", "file", "file_path", "filepath", "filename"}, "", true, false)
	if err != nil {
		return errorResult("Error: Invalid arguments for read_file: " + err.Error())
	}
	startLine, err := intArg(args, "start_line", 1, 1, 0)
	if err != nil {
		return errorResult("Error: Invalid arguments for read_file: " + err.Error())
	}
	endLine, err := intArg(args, "end_line", 0, 1, 0)
	if err != nil {
		return errorResult("Error: Invalid arguments for read_file: " + err.Error())
	}
	maxLines, err := intArg(args, "max_lines", 0, 1, 0)
	if err != nil {
		return errorResult("Error: Invalid arguments for read_file: " + err.Error())
	}
	// Default page size when the model omits max_lines / end_line: full-file
	// reads of 20–40 KB scripts were the dominant inter-turn prefill cost on
	// local hybrid (measured: 4 files → 8k tokens → 2+ min idle). 120 lines
	// is enough structure for coding; model can page with start_line.
	const defaultReadMaxLines = 120
	usedDefaultMaxLines := false
	if maxLines == 0 && endLine == 0 {
		maxLines = defaultReadMaxLines
		usedDefaultMaxLines = true
	}

	absolutePath, relativePath, err := resolveScopedReadPath(tool.workspaceRoot, tool.scope, requestedPath, options.Cwd)
	if err != nil {
		message := "Error reading file " + requestedPath + ": " + err.Error()
		if suggestion := pathNotFoundSuggestion(tool.workspaceRoot, requestedPath); suggestion != "" {
			message += ". Did you mean `" + suggestion + "`? Use that exact path; do not retry the missing path."
		}
		return errorResult(message)
	}

	// Stat first for the qwen-style unchanged short-circuit: when size/mtime and
	// the requested range match the prior successful read, skip os.ReadFile and
	// the multi-KB dump that would re-prefill on local hybrid models.
	info, statErr := os.Stat(absolutePath)
	if statErr == nil && options.FileTracker.UnchangedSinceRead(absolutePath, info, startLine, endLine, maxLines) {
		meta := map[string]string{
			"file_unchanged": "true",
			"path":           relativePath,
		}
		return Result{
			Status: StatusOK,
			Output: fmt.Sprintf(
				"[file unchanged: %s — same content and range as previous read; reuse that result. Do not re-read unless you need a different start_line/end_line/max_lines.]",
				relativePath,
			),
			Meta: meta,
		}
	}

	content, err := os.ReadFile(absolutePath)
	if err != nil {
		message := "Error reading file " + relativePath + ": " + err.Error()
		if os.IsNotExist(err) {
			if suggestion := pathNotFoundSuggestion(tool.workspaceRoot, requestedPath); suggestion != "" {
				message += ". Did you mean `" + suggestion + "`? Use that exact path; do not retry the missing path."
			}
		}
		return errorResult(message)
	}
	// Record the whole-file baseline (the raw bytes, matching what edit_file and
	// write_file read) so a later write can detect an out-of-@code modification.
	// Stat is best-effort: a missing FileInfo only drops the diagnostic size/mtime,
	// not the authoritative content hash. Prefer the pre-read Stat when available.
	if info == nil {
		info, _ = os.Stat(absolutePath)
	}

	normalizedContent := strings.ReplaceAll(string(content), "\r\n", "\n")
	normalizedContent = strings.TrimSuffix(normalizedContent, "\n")
	lines := strings.Split(normalizedContent, "\n")
	total := len(lines)
	if startLine > total {
		// Still establish a baseline so later edits detect drift, but range is
		// not a cacheable success for the requested window.
		options.FileTracker.Record(absolutePath, content, info)
		return okResult(fmt.Sprintf("File: %s\n(start_line %d is past the end of the file, which has %d lines)", relativePath, startLine, total))
	}

	resolvedEnd := endLine
	if resolvedEnd == 0 || resolvedEnd > total {
		resolvedEnd = total
	}
	if resolvedEnd < startLine {
		return errorResult("Error: Invalid arguments for read_file: end_line must be greater than or equal to start_line")
	}

	selected := lines[startLine-1 : resolvedEnd]
	truncated := false
	if maxLines > 0 && len(selected) > maxLines {
		selected = selected[:maxLines]
		truncated = true
	}

	lastLine := startLine + len(selected) - 1
	width := len(strconv.Itoa(lastLine))
	numbered := make([]string, 0, len(selected))
	for index, line := range selected {
		lineNumber := strconv.Itoa(startLine + index)
		numbered = append(numbered, strings.Repeat(" ", width-len(lineNumber))+lineNumber+" | "+line)
	}

	header := fmt.Sprintf("File: %s (%d lines)", relativePath, total)
	if startLine != 1 || resolvedEnd != total || maxLines > 0 {
		header = fmt.Sprintf("File: %s (lines %d-%d of %d)", relativePath, startLine, lastLine, total)
	}

	body := strings.Join(numbered, "\n")
	if truncated {
		// The Truncated flag alone is invisible to the model in the rendered
		// output, so it cannot tell a max_lines cut from a complete read. Make the
		// cut explicit and tell it how to continue.
		more := resolvedEnd - lastLine
		if usedDefaultMaxLines {
			body += fmt.Sprintf("\n\n[truncated: showing first %d lines of %d; set start_line=%d or max_lines to continue — do NOT re-read the whole file]", defaultReadMaxLines, total, lastLine+1)
		} else {
			body += fmt.Sprintf("\n\n[truncated: %d more line(s) in the requested range not shown; set start_line=%d to continue]", more, lastLine+1)
		}
	}

	output := header + "\n\n" + body
	budgeted := applyOutputBudget(output, readOutputBudgetBytes, "use start_line/end_line or max_lines to continue with a smaller range")
	meta := outputBudgetMeta(budgeted)
	if budgeted.Truncated {
		meta["truncated"] = "true"
		meta["truncation_reason"] = "byte_budget"
	}

	// Fingerprint with the *requested* range args (not resolved end) so a
	// follow-up with the same args can short-circuit.
	options.FileTracker.RecordRead(absolutePath, content, info, startLine, endLine, maxLines)
	security.Default().Graph().RecordFileReadWithMetadata(relativePath, budgeted.Output, options.SessionID, options.ToolCallID)

	return Result{
		Status:    StatusOK,
		Output:    budgeted.Output,
		Truncated: truncated || budgeted.Truncated,
		Meta:      meta,
	}
}
