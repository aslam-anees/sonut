package tools

import (
	"fmt"
	"strconv"
)

const (
	// readOutputBudgetBytes caps read_file / read_minified output. Local hybrid
	// models re-prefill every tool body; 24 KiB×4 still caused 2+ min prefills.
	// 12 KiB hard ceiling; default max_lines=120 is the primary throttle.
	readOutputBudgetBytes   = 12 * 1024
	searchOutputBudgetBytes = 12 * 1024
)

type outputBudgetResult struct {
	Output       string
	Truncated    bool
	RawBytes     int
	EmittedBytes int
}

func applyOutputBudget(output string, maxBytes int, hint string) outputBudgetResult {
	result := outputBudgetResult{
		Output:       output,
		RawBytes:     len(output),
		EmittedBytes: len(output),
	}
	if maxBytes <= 0 || len(output) <= maxBytes {
		return result
	}

	// Head+tail keep (qwen/exec style): start for structure, end for errors.
	marker := fmt.Sprintf("\n\n[truncated: output exceeded %d bytes; %s]\n", maxBytes, hint)
	budget := maxBytes - len(marker)
	if budget < 64 {
		result.Output = utf8Prefix(output, maxBytes)
		result.Truncated = true
		result.EmittedBytes = len(result.Output)
		return result
	}
	result.Output = FitTextHeadTail(output, budget, marker)
	result.Truncated = true
	result.EmittedBytes = len(result.Output)
	return result
}

func outputBudgetMeta(result outputBudgetResult) map[string]string {
	return map[string]string{
		"raw_bytes":        strconv.Itoa(result.RawBytes),
		"emitted_bytes":    strconv.Itoa(result.EmittedBytes),
		"estimated_tokens": strconv.Itoa(estimatedTokensFromBytes(result.EmittedBytes)),
	}
}

func estimatedTokensFromBytes(bytes int) int {
	if bytes <= 0 {
		return 0
	}
	return (bytes + 3) / 4
}
