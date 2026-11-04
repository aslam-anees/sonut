package tools

import (
	"path/filepath"
	"strings"
	"sync/atomic"
)

// sessionWorkingDir is the session-level working directory set by /cd. When
// non-empty, RELATIVE tool paths (write_file, read_file, exec cwd default, …)
// resolve against it instead of the workspace root the tools were constructed
// with, so /cd actually moves where new files land and where commands run.
// Absolute paths keep validating against the full scope root set unchanged.
// The caller (/cd) is responsible for granting scope access to the directory
// before setting it.
var sessionWorkingDir atomic.Value

// SetSessionWorkingDir updates the working directory used to resolve relative
// tool paths. Pass "" to restore the constructed workspace root.
func SetSessionWorkingDir(path string) { sessionWorkingDir.Store(path) }

// SessionWorkingDir returns the current /cd override, or "" if unset.
func SessionWorkingDir() string {
	value, _ := sessionWorkingDir.Load().(string)
	return value
}

// effectiveRelativeRoot picks the root relative paths resolve against.
//
// A RunOptions.Cwd supplied by the caller is authoritative. The process-global
// /cd value is retained only as a compatibility fallback for direct Tool.Run
// callers that cannot provide RunOptions. Depending on the global value for an
// agent run is incorrect: resumed sessions and delegated runs do not execute
// handleCDCommand, and concurrent sessions can overwrite each other's value.
func effectiveRelativeRoot(workspaceRoot string, runCwd ...string) string {
	for _, dir := range runCwd {
		if trimmed := strings.TrimSpace(dir); trimmed != "" {
			if !filepath.IsAbs(trimmed) {
				trimmed = filepath.Join(workspaceRoot, trimmed)
			}
			return filepath.Clean(trimmed)
		}
	}
	if dir := SessionWorkingDir(); dir != "" {
		return dir
	}
	return workspaceRoot
}
