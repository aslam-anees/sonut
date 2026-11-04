package tools

// read_many_files batches several workspace file reads into one tool result
// (qwen-code readManyFiles port). One round-trip instead of N×read_file cuts
// local hybrid prefill cost while fair per-file budgets keep the body lean.

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/aistatine/sonut/internal/security"
)

const (
	readManyMaxPaths      = 12
	readManyDefaultLines  = 80
	readManyTotalBudget   = 16 * 1024
	readManyPerFileBudget = 4 * 1024
	readManyMaxFileBytes  = 512 * 1024
)

type readManyFilesTool struct {
	baseTool
	workspaceRoot string
	scope         PathScope
}

func NewReadManyFilesTool(workspaceRoot string) Tool {
	return NewScopedReadManyFilesTool(workspaceRoot, nil)
}

func NewScopedReadManyFilesTool(workspaceRoot string, scope PathScope) Tool {
	return readManyFilesTool{
		baseTool: baseTool{
			name: "read_many_files",
			description: "Read multiple workspace files in one call. " +
				"Prefer this over sequential read_file when you need several files. " +
				"Each file is line-capped and the batch shares a byte budget.",
			parameters: Schema{
				Type: "object",
				Properties: map[string]PropertySchema{
					"paths": {
						Type:        "array",
						Description: "Workspace-relative or absolute paths (max 12).",
						Items:       &PropertySchema{Type: "string"},
					},
					"max_lines": {
						Type:        "integer",
						Description: "Max lines per file (default 80).",
						Minimum:     intPtr(1),
					},
				},
				Required:             []string{"paths"},
				AdditionalProperties: false,
			},
			safety: readOnlySafety("Reads multiple files without modifying them."),
		},
		workspaceRoot: normalizeWorkspaceRoot(workspaceRoot),
		scope:         scope,
	}
}

func (tool readManyFilesTool) Run(ctx context.Context, args map[string]any) Result {
	return tool.RunWithOptions(ctx, args, RunOptions{})
}

func (tool readManyFilesTool) RunWithOptions(_ context.Context, args map[string]any, options RunOptions) Result {
	paths, err := stringListArg(args, "paths", true)
	if err != nil {
		return errorResult("Error: Invalid arguments for read_many_files: " + err.Error())
	}
	if len(paths) == 0 {
		return errorResult("Error: paths must contain at least one path")
	}
	if len(paths) > readManyMaxPaths {
		return errorResult(fmt.Sprintf("Error: paths is limited to %d entries (got %d)", readManyMaxPaths, len(paths)))
	}
	maxLines, err := intArg(args, "max_lines", readManyDefaultLines, 1, 0)
	if err != nil {
		return errorResult("Error: Invalid arguments for read_many_files: " + err.Error())
	}

	seen := map[string]bool{}
	sections := make([]string, 0, len(paths))
	okCount := 0
	errCount := 0
	for _, requested := range paths {
		requested = strings.TrimSpace(requested)
		if requested == "" {
			errCount++
			sections = append(sections, "### <empty path>\nError: empty path")
			continue
		}
		absolutePath, relativePath, resolveErr := resolveScopedReadPath(tool.workspaceRoot, tool.scope, requested, options.Cwd)
		if resolveErr != nil {
			errCount++
			message := "Error reading " + requested + ": " + resolveErr.Error()
			if suggestion := pathNotFoundSuggestion(tool.workspaceRoot, requested); suggestion != "" {
				message += ". Did you mean `" + suggestion + "`?"
			}
			sections = append(sections, "### "+requested+"\n"+message)
			continue
		}
		key := filepath.Clean(absolutePath)
		if seen[key] {
			continue
		}
		seen[key] = true

		info, statErr := os.Stat(absolutePath)
		if statErr != nil {
			errCount++
			sections = append(sections, "### "+relativePath+"\nError: "+statErr.Error())
			continue
		}
		if info.IsDir() {
			errCount++
			sections = append(sections, "### "+relativePath+"\nError: path is a directory; use list_directory")
			continue
		}
		if info.Size() > readManyMaxFileBytes {
			errCount++
			sections = append(sections, fmt.Sprintf("### %s\nError: file exceeds %d bytes; use read_file with a line range", relativePath, readManyMaxFileBytes))
			continue
		}
		content, readErr := os.ReadFile(absolutePath)
		if readErr != nil {
			errCount++
			sections = append(sections, "### "+relativePath+"\nError: "+readErr.Error())
			continue
		}
		options.FileTracker.Record(absolutePath, content, info)

		normalized := strings.ReplaceAll(string(content), "\r\n", "\n")
		normalized = strings.TrimSuffix(normalized, "\n")
		lines := strings.Split(normalized, "\n")
		total := len(lines)
		selected := lines
		truncated := false
		if maxLines > 0 && len(selected) > maxLines {
			selected = selected[:maxLines]
			truncated = true
		}
		width := len(strconv.Itoa(len(selected)))
		if width < 1 {
			width = 1
		}
		numbered := make([]string, 0, len(selected))
		for index, line := range selected {
			lineNumber := strconv.Itoa(index + 1)
			numbered = append(numbered, strings.Repeat(" ", width-len(lineNumber))+lineNumber+" | "+line)
		}
		header := fmt.Sprintf("### %s (%d lines)", relativePath, total)
		body := strings.Join(numbered, "\n")
		if truncated {
			body += fmt.Sprintf("\n\n[truncated: first %d of %d lines; use read_file start_line=%d for more]", maxLines, total, maxLines+1)
		}
		section := header + "\n" + body
		// Soft per-file cap before the shared batch budget.
		if len(section) > readManyPerFileBudget {
			section = FitTextHeadTail(section, readManyPerFileBudget, "\n…[per-file budget]…\n")
		}
		security.Default().Graph().RecordFileReadWithMetadata(relativePath, section, options.SessionID, options.ToolCallID)
		sections = append(sections, section)
		okCount++
	}

	output := JoinBudgetedFiles(sections, readManyTotalBudget)
	if output == "" {
		return errorResult("Error: no files could be read")
	}
	header := fmt.Sprintf("read_many_files: %d ok, %d error(s), %d path(s)\n\n", okCount, errCount, len(paths))
	budgeted := applyOutputBudget(header+output, readManyTotalBudget+256, "use read_file on individual paths for full content")
	meta := outputBudgetMeta(budgeted)
	meta["files_ok"] = strconv.Itoa(okCount)
	meta["files_error"] = strconv.Itoa(errCount)
	if budgeted.Truncated {
		meta["truncated"] = "true"
	}
	status := StatusOK
	if okCount == 0 {
		status = StatusError
	}
	return Result{
		Status:    status,
		Output:    budgeted.Output,
		Truncated: budgeted.Truncated,
		Meta:      meta,
	}
}

// stringListArg accepts JSON arrays of strings or a single string.
func stringListArg(args map[string]any, key string, required bool) ([]string, error) {
	value, ok := args[key]
	if !ok || value == nil {
		if required {
			return nil, fmt.Errorf("%s is required", key)
		}
		return nil, nil
	}
	switch typed := value.(type) {
	case []string:
		out := make([]string, 0, len(typed))
		for _, item := range typed {
			item = strings.TrimSpace(item)
			if item != "" {
				out = append(out, item)
			}
		}
		return out, nil
	case []any:
		out := make([]string, 0, len(typed))
		for i, item := range typed {
			text, ok := item.(string)
			if !ok {
				return nil, fmt.Errorf("%s[%d] must be a string", key, i)
			}
			text = strings.TrimSpace(text)
			if text != "" {
				out = append(out, text)
			}
		}
		return out, nil
	case string:
		text := strings.TrimSpace(typed)
		if text == "" {
			if required {
				return nil, fmt.Errorf("%s must be a non-empty list", key)
			}
			return nil, nil
		}
		// Tolerate a single path or comma/newline-separated list from weak models.
		parts := strings.FieldsFunc(text, func(r rune) bool {
			return r == ',' || r == '\n' || r == ';'
		})
		out := make([]string, 0, len(parts))
		for _, part := range parts {
			part = strings.TrimSpace(part)
			if part != "" {
				out = append(out, part)
			}
		}
		return out, nil
	default:
		return nil, fmt.Errorf("%s must be an array of strings", key)
	}
}
