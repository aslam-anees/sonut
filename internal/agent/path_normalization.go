package agent

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
)

// canonicalToolPathForCwd removes the common model mistake of including the
// current directory name in a path that is already relative to that directory
// (for example, cwd=/workspace/dummy and path=dummy/index.html). The native
// tools already tolerate this spelling, but the agent guards must canonicalize
// it too or they treat one file as two different files and re-read/re-generate
// unnecessarily.
//
// Absolute paths that are only the cwd basename ("/test" while already in
// .../sonut/test after /cd) are rewritten to "." so the sandbox and tools never
// treat the leaf directory name as a host-root path and burn a turn on
// outside_workspace.
//
// Multi-segment ancestry restatements are also stripped (cwd=.../sonut/test +
// path=sonut/test/supermario.html → supermario.html). Live thrash created
// test/sonut/test/supermario.html and then looped edit_file on the shadow.
//
// Only an unambiguous redundant prefix is removed for single-segment cases: the
// candidate must exist, or the doubled path must not exist. A real subdirectory
// named like the cwd is therefore never silently redirected.
func canonicalToolPathForCwd(cwd, raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" || cwd == "" {
		return raw
	}
	cleanCwd := filepath.Clean(cwd)
	base := filepath.Base(cleanCwd)
	if base == "" || base == "." || base == string(filepath.Separator) {
		return raw
	}

	// Absolute "/basename" or "/basename/rest" when basename is the current
	// working directory leaf (after /cd into .../test the model invents /test).
	if filepath.IsAbs(raw) {
		return canonicalAbsolutePathForCwd(cleanCwd, base, raw)
	}

	// Multi-segment: sonut/test/foo while already in .../sonut/test.
	if stripped := ancestryRelativePathForCwd(cleanCwd, raw); stripped != "" {
		return stripped
	}

	cleanRaw := filepath.Clean(raw)
	if cleanRaw != base && !strings.HasPrefix(cleanRaw, base+string(filepath.Separator)) {
		return raw
	}
	candidate := strings.TrimPrefix(cleanRaw, base+string(filepath.Separator))
	if candidate == "" {
		candidate = "."
	}
	// If the cwd genuinely contains a directory with the same name, preserve
	// the spelling: it is a real nested target, not a duplicated cwd prefix.
	if info, err := filepath.Abs(filepath.Join(cleanCwd, base)); err == nil {
		if stat, statErr := os.Stat(info); statErr == nil && stat.IsDir() {
			return raw
		}
	}
	candidateExists := workspaceFileExists(cleanCwd, candidate)
	doubledExists := workspaceFileExists(cleanCwd, cleanRaw)
	if candidateExists || !doubledExists {
		return filepath.ToSlash(candidate)
	}
	return raw
}

// ancestryRelativePathForCwd strips multi-segment path prefixes that restate
// trailing components of the absolute cwd. Mirrors tools.ancestryPathFallback
// so agent guards and native tools agree on one spelling.
func ancestryRelativePathForCwd(cleanCwd, raw string) string {
	pathSlash := strings.TrimPrefix(filepath.ToSlash(filepath.Clean(raw)), "./")
	if pathSlash == "" || pathSlash == "." {
		return ""
	}
	cwdParts := strings.Split(strings.Trim(filepath.ToSlash(cleanCwd), "/"), "/")
	pathParts := strings.Split(pathSlash, "/")
	if len(cwdParts) < 2 || len(pathParts) < 2 {
		return ""
	}
	max := len(cwdParts)
	if len(pathParts) < max {
		max = len(pathParts)
	}
	for n := max; n >= 2; n-- {
		suffix := strings.Join(cwdParts[len(cwdParts)-n:], "/")
		prefix := strings.Join(pathParts[:n], "/")
		if !strings.EqualFold(suffix, prefix) {
			continue
		}
		rest := strings.Join(pathParts[n:], "/")
		if rest == "" {
			return "."
		}
		return rest
	}
	return ""
}

// canonicalAbsolutePathForCwd rewrites host-root lookalikes that are really the
// session cwd. "/test" → "." and "/test/mario.html" → "mario.html" when cwd's
// basename is "test". Real machine paths (/Users/..., /var/..., /etc/...) and
// absolute paths that already equal the cwd are left alone (or shortened to "."
// when they are exactly the cwd).
func canonicalAbsolutePathForCwd(cleanCwd, base, raw string) string {
	cleanRaw := filepath.Clean(raw)
	// Exact match of the full cwd: model echoed Working directory.
	if cleanRaw == cleanCwd {
		return "."
	}
	// Path under the real cwd: make it relative so tools and sandbox agree.
	if rel, err := filepath.Rel(cleanCwd, cleanRaw); err == nil &&
		rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) && !filepath.IsAbs(rel) {
		if rel == "." {
			return "."
		}
		return filepath.ToSlash(rel)
	}

	slash := filepath.ToSlash(cleanRaw)
	segments := strings.Split(strings.Trim(slash, "/"), "/")
	if len(segments) == 0 || segments[0] == "" {
		return raw
	}
	// Only rewrite when the first (and only host-root-looking) segment is the
	// cwd basename. Longer invented prefixes keep the sandbox SuggestScopedPath
	// path; we only fix the short "/test" artifact here.
	if !strings.EqualFold(segments[0], base) {
		return raw
	}
	// Reject real host roots even if basename collides (e.g. a folder named
	// "Users" must not rewrite /Users/...).
	switch strings.ToLower(segments[0]) {
	case "applications", "bin", "dev", "etc", "home", "library", "opt", "private", "sbin", "system", "tmp", "usr", "users", "var", "volumes":
		return raw
	}
	// Nested real subdir named like the cwd: keep absolute spelling so we do
	// not collapse .../test/test into the parent.
	if len(segments) > 1 {
		if info, err := os.Stat(filepath.Join(cleanCwd, base)); err == nil && info.IsDir() {
			// Prefer relative under cwd when the rest is not the doubled base.
			rest := strings.Join(segments[1:], "/")
			return filepath.ToSlash(rest)
		}
		return filepath.ToSlash(strings.Join(segments[1:], "/"))
	}
	// Exactly "/basename" → current directory.
	if info, err := os.Stat(filepath.Join(cleanCwd, base)); err == nil && info.IsDir() {
		// Real nested dir exists; listing /test might mean that nested folder.
		// Prefer the nested path only when the model used a relative "test"
		// spelling; absolute "/test" after /cd almost always means the cwd.
		// Keep absolute→"." so we do not thrash on outside_workspace.
		return "."
	}
	return "."
}

// canonicalizeToolCallPathForCwd rewrites only the path-bearing argument used
// by native file/directory tools. Keeping this at the loop boundary means all
// downstream logic (read de-duplication, partial-write recovery, plan progress
// and permission scopes) observes one stable spelling.
func canonicalizeToolCallPathForCwd(call ToolCall, cwd string) ToolCall {
	switch call.Name {
	case "read_file", "read_minified_file", "write_file", "create_file", "overwrite_file", "append_file", "edit_file", "list_directory":
	default:
		return call
	}
	if strings.TrimSpace(call.Arguments) == "" {
		return call
	}
	var args map[string]any
	if err := json.Unmarshal([]byte(call.Arguments), &args); err != nil {
		return call
	}
	for _, key := range []string{"path", "file", "file_path", "filename"} {
		value, ok := args[key].(string)
		if !ok || strings.TrimSpace(value) == "" {
			continue
		}
		canonical := canonicalToolPathForCwd(cwd, value)
		if canonical == value {
			return call
		}
		args[key] = canonical
		encoded, err := json.Marshal(args)
		if err != nil {
			return call
		}
		call.Arguments = string(encoded)
		return call
	}
	return call
}
