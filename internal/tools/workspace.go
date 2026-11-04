package tools

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/aistatine/sonut/internal/sandbox"
	"github.com/aistatine/sonut/internal/workspaceindex"
)

var ignoredDirectories = map[string]bool{
	".git":         true,
	"node_modules": true,
	"dist":         true,
	"build":        true,
	".next":        true,
	".turbo":       true,
	"coverage":     true,
	".cache":       true,
	"tmp":          true,
	"temp":         true,
}

func normalizeWorkspaceRoot(workspaceRoot string) string {
	root, err := filepath.Abs(workspaceRoot)
	if err != nil {
		return workspaceRoot
	}
	return root
}

func resolveWorkspacePath(workspaceRoot string, requestedPath string) (string, string, error) {
	if requestedPath == "" {
		requestedPath = "."
	}

	root, err := filepath.Abs(workspaceRoot)
	if err != nil {
		return "", "", err
	}
	root, err = filepath.EvalSymlinks(root)
	if err != nil {
		return "", "", err
	}
	requestedPath = normalizeWorkspaceRequestedPath(root, requestedPath)
	if filepath.IsAbs(requestedPath) {
		requestedPath = sandbox.NormalizePrefixForRoot(requestedPath, root)
		if fallback := absoluteDuplicateRootFallback(root, requestedPath); fallback != "" {
			requestedPath = fallback
		}
	}

	target := requestedPath
	if !filepath.IsAbs(target) {
		target = filepath.Join(root, target)
	}

	target, err = filepath.Abs(target)
	if err != nil {
		return "", "", err
	}
	target, err = filepath.EvalSymlinks(target)
	if err != nil {
		return "", "", err
	}

	relative, err := filepath.Rel(root, target)
	if err != nil {
		return "", "", outsideWorkspaceError(requestedPath)
	}
	if relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) || filepath.IsAbs(relative) {
		return "", "", outsideWorkspaceError(requestedPath)
	}
	if relative == "." {
		return target, ".", nil
	}
	return target, filepath.ToSlash(relative), nil
}

func resolveWorkspaceTargetPath(workspaceRoot string, requestedPath string) (string, string, error) {
	if requestedPath == "" {
		requestedPath = "."
	}

	root, err := filepath.Abs(workspaceRoot)
	if err != nil {
		return "", "", err
	}
	root, err = filepath.EvalSymlinks(root)
	if err != nil {
		return "", "", err
	}
	requestedPath = normalizeWorkspaceRequestedPath(root, requestedPath)
	if filepath.IsAbs(requestedPath) {
		requestedPath = sandbox.NormalizePrefixForRoot(requestedPath, root)
		if fallback := absoluteDuplicateRootFallback(root, requestedPath); fallback != "" {
			requestedPath = fallback
		}
	}

	target := requestedPath
	if !filepath.IsAbs(target) {
		target = filepath.Join(root, target)
	}
	target, err = filepath.Abs(target)
	if err != nil {
		return "", "", err
	}
	if err := recheckWorkspaceWriteTarget(root, target); err != nil {
		return "", "", err
	}

	existing := target
	missingSegments := []string{}
	for {
		if _, err := os.Lstat(existing); err == nil {
			break
		} else if os.IsNotExist(err) || errors.Is(err, syscall.ENOTDIR) {
			parent := filepath.Dir(existing)
			if parent == existing {
				return "", "", err
			}
			missingSegments = append([]string{filepath.Base(existing)}, missingSegments...)
			existing = parent
			continue
		} else {
			return "", "", err
		}
	}

	resolved, err := filepath.EvalSymlinks(existing)
	if err != nil {
		return "", "", err
	}
	for _, segment := range missingSegments {
		resolved = filepath.Join(resolved, segment)
	}

	relative, err := filepath.Rel(root, resolved)
	if err != nil {
		return "", "", outsideWorkspaceError(requestedPath)
	}
	if relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) || filepath.IsAbs(relative) {
		return "", "", outsideWorkspaceError(requestedPath)
	}
	if relative == "." {
		return resolved, ".", nil
	}
	return resolved, filepath.ToSlash(relative), nil
}

func recheckWorkspaceWriteTarget(workspaceRoot string, requestedPath string) error {
	if requestedPath == "" {
		requestedPath = "."
	}

	root, err := filepath.Abs(workspaceRoot)
	if err != nil {
		return err
	}
	root, err = filepath.EvalSymlinks(root)
	if err != nil {
		return err
	}
	requestedPath = normalizeWorkspaceRequestedPath(root, requestedPath)
	if filepath.IsAbs(requestedPath) {
		requestedPath = sandbox.NormalizePrefixForRoot(requestedPath, root)
		if fallback := absoluteDuplicateRootFallback(root, requestedPath); fallback != "" {
			requestedPath = fallback
		}
	}

	target := requestedPath
	if !filepath.IsAbs(target) {
		target = filepath.Join(root, target)
	}
	target, err = filepath.Abs(target)
	if err != nil {
		return err
	}

	relative, err := filepath.Rel(root, target)
	if err != nil {
		return outsideWorkspaceError(requestedPath)
	}
	if relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) || filepath.IsAbs(relative) {
		return outsideWorkspaceError(requestedPath)
	}
	if relative == "." {
		return nil
	}

	current := root
	for _, segment := range strings.Split(filepath.Clean(relative), string(filepath.Separator)) {
		if segment == "." || segment == "" {
			continue
		}

		current = filepath.Join(current, segment)
		info, err := os.Lstat(current)
		if err != nil {
			// ENOTDIR (an intermediate component is a file) means the path does
			// not exist — same boundary answer as ENOENT; the write itself will
			// surface the real filesystem error.
			if os.IsNotExist(err) || errors.Is(err, syscall.ENOTDIR) {
				return nil
			}
			return err
		}
		if info.Mode()&os.ModeSymlink != 0 {
			symlinkRelative, err := filepath.Rel(root, current)
			if err != nil {
				symlinkRelative = current
			}
			return fmt.Errorf("%s must not traverse symlink %s", requestedPath, filepath.ToSlash(symlinkRelative))
		}
	}

	return nil
}

func outsideWorkspaceError(requestedPath string) error {
	return fmt.Errorf("%s must stay inside the workspace", requestedPath)
}

// normalizeWorkspaceRequestedPath removes path-shape damage introduced before
// a native tool receives the model argument. In particular, filepath.Join in
// Go does not discard an earlier operand when a later operand is absolute, so
// an upstream caller can accidentally turn
//
//	/workspace/dummy + /workspace/dummy
//
// into a relative-looking value such as dummy//workspace/dummy. The resolver
// must recognize the embedded absolute path instead of creating a workspace
// subtree named "workspace" (or "Users" on macOS).
//
// It also accepts the common missing-leading-slash form of an absolute cwd.
// Both repairs are deliberately conservative: they only apply when the
// embedded/missing-slash path identifies this exact workspace root. A genuine
// nested directory keeps its original spelling and is still checked normally.
func normalizeWorkspaceRequestedPath(workspaceRoot, requestedPath string) string {
	requestedPath = strings.TrimSpace(requestedPath)
	if requestedPath == "" || filepath.IsAbs(requestedPath) {
		return requestedPath
	}

	// Recover an absolute operand that was concatenated with a separator. Keep
	// this before filepath.Clean, which would erase the double separator that
	// tells us an absolute path was embedded in the value.
	slashPath := filepath.ToSlash(requestedPath)
	for offset := strings.Index(slashPath, "//"); offset >= 0; {
		tail := slashPath[offset+1:]
		if strings.HasPrefix(tail, "/") {
			candidate := filepath.FromSlash(tail)
			if filepath.IsAbs(candidate) {
				return candidate
			}
		}
		next := strings.Index(slashPath[offset+2:], "//")
		if next < 0 {
			break
		}
		offset += next + 2
	}

	root, err := filepath.Abs(workspaceRoot)
	if err != nil {
		return requestedPath
	}
	root = filepath.Clean(root)
	if fallback := duplicateRootFallback(root, requestedPath); fallback != "" {
		requestedPath = fallback
	}

	// Models sometimes omit the leading slash when copying the cwd from tool
	// output. Map "Users/aslam/project/dummy/file.html" back to
	// "/Users/aslam/project/dummy/file.html" only when its prefix is exactly
	// this workspace root.
	rootSlash := filepath.ToSlash(root)
	rootRelative := strings.TrimPrefix(rootSlash, "/")
	candidateSlash := filepath.ToSlash(filepath.Clean(requestedPath))
	if rootRelative != "" && (candidateSlash == rootRelative || strings.HasPrefix(candidateSlash, rootRelative+"/")) {
		tail := strings.TrimPrefix(candidateSlash, rootRelative)
		return filepath.FromSlash(rootSlash + tail)
	}
	return requestedPath
}

// ResolveWorkspaceCwd returns one canonical lexical on-disk working directory
// for a caller-provided cwd. It is shared by CLI/TUI session boundaries and
// native file tools so a path is never resolved once for the UI and a different
// way for the agent. The spelling is kept stable across macOS aliases such as
// /var and /private/var; a not-yet-existing path is still returned as an
// absolute, cleaned path for the caller to validate.
func ResolveWorkspaceCwd(basePath, requestedPath string) string {
	base := strings.TrimSpace(basePath)
	if base == "" {
		if current, err := os.Getwd(); err == nil {
			base = current
		}
	}
	if absolute, err := filepath.Abs(base); err == nil {
		base = filepath.Clean(absolute)
	}
	if requestedPath == "" {
		requestedPath = base
	}
	requestedPath = normalizeWorkspaceRequestedPath(base, requestedPath)
	if filepath.IsAbs(requestedPath) {
		if fallback := absoluteDuplicateRootFallback(base, requestedPath); fallback != "" {
			requestedPath = fallback
		}
	}
	if !filepath.IsAbs(requestedPath) {
		requestedPath = filepath.Join(base, requestedPath)
	}
	if absolute, err := filepath.Abs(requestedPath); err == nil {
		requestedPath = filepath.Clean(absolute)
	}
	return filepath.Clean(requestedPath)
}

// workspacePathHint returns the safest workspace-relative spelling for a path
// whose prefix the model invented. It is used in write errors so a failed
// multi-minute generation can be retried with reuse_content instead of making
// the model guess the correction and regenerate the body.
func workspacePathHint(workspaceRoot string, scope PathScope, requestedPath string) string {
	if requestedPath == "" || !filepath.IsAbs(requestedPath) {
		return ""
	}
	roots, err := scopedRoots(workspaceRoot, scope)
	if err != nil || len(roots) == 0 {
		return ""
	}
	suggestion, matchedRoot := sandbox.SuggestScopedPath(requestedPath, roots)
	if suggestion == "" {
		return ""
	}
	if matchedRoot == roots[0] {
		root, rootErr := filepath.EvalSymlinks(roots[0])
		if rootErr != nil {
			root = normalizeWorkspaceRoot(roots[0])
		}
		if relative, relErr := filepath.Rel(root, suggestion); relErr == nil &&
			relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator)) &&
			!filepath.IsAbs(relative) {
			if relative == "." {
				return "."
			}
			return filepath.ToSlash(relative)
		}
	}
	return filepath.ToSlash(suggestion)
}

// rootSlashFallback reinterprets a failed absolute path as workspace-relative.
// Models routinely write "/index.html" (URL habit) meaning the file at the
// workspace root; without this the tool reports "nothing there" for a file
// that exists. Returns the trimmed relative path, or "" when not applicable.
//
// Delegates to sandbox.RootSlashRelative so the engine gate and tools layer
// recover the same spellings (including "/test" when the workspace is named
// test after /cd).
func rootSlashFallback(requestedPath string) string {
	return sandbox.RootSlashRelative(requestedPath)
}

// applyRootSlashFallback maps a denied absolute path into a workspace-relative
// spelling. When the relative form is just the workspace (or /cd) basename
// ("/test" while already in .../test), strip it to "." so we list/write the
// root instead of inventing a nested test/test shadow directory.
func applyRootSlashFallback(relativeRoot, requestedPath string) string {
	fallback := rootSlashFallback(requestedPath)
	if fallback == "" {
		return ""
	}
	if dup := duplicateRootFallback(relativeRoot, fallback); dup != "" {
		return dup
	}
	return fallback
}

// duplicateRootFallback strips a leading segment that duplicates the
// workspace root's basename — models routinely write "sonut/mario.html" in a
// workspace already named ".../sonut", meaning the file at the root. Applies
// only when no real subdirectory with that name exists, so a genuine
// root-named subdir is always respected. Returns "" when not applicable.
//
// It also strips multi-segment ancestry restatements of the absolute cwd/root
// (live supermario thrash: cwd=.../sonut/test + path=sonut/test/supermario.html
// must land at supermario.html, not test/sonut/test/supermario.html).
func duplicateRootFallback(workspaceRoot string, requestedPath string) string {
	root := normalizeWorkspaceRoot(workspaceRoot)
	if stripped := ancestryPathFallback(root, requestedPath); stripped != "" {
		return stripped
	}
	base := filepath.Base(root)
	if base == "" || base == "." || base == string(filepath.Separator) {
		return ""
	}
	trimmed := strings.TrimPrefix(filepath.ToSlash(filepath.Clean(requestedPath)), "./")
	prefix := base + "/"
	if trimmed != base && !strings.HasPrefix(trimmed, prefix) {
		return ""
	}
	if info, err := os.Stat(filepath.Join(root, base)); err == nil && info.IsDir() {
		return ""
	}
	if trimmed == base {
		return "."
	}
	return strings.TrimPrefix(trimmed, prefix)
}

// ancestryPathFallback strips a multi-segment relative path prefix that restates
// trailing components of the absolute cwd/root. Example:
//
//	cwd  = /Users/a/Desktop/aistatine/sonut/test
//	path = sonut/test/supermario.html
//	→ supermario.html
//
// Single-segment cases stay with the basename guard in duplicateRootFallback so
// a real nested directory named like the cwd leaf is preserved. Multi-segment
// matches (≥2 path components) that restate cwd ancestry are almost always
// model path invention after /cd — intentional nested trees with the exact same
// multi-segment name as the cwd trailing path are vanishingly rare.
func ancestryPathFallback(cwd, requestedPath string) string {
	requestedPath = strings.TrimSpace(requestedPath)
	if requestedPath == "" || filepath.IsAbs(requestedPath) {
		return ""
	}
	cwd = filepath.Clean(cwd)
	if cwd == "" || cwd == "." || cwd == string(filepath.Separator) {
		return ""
	}
	pathSlash := strings.TrimPrefix(filepath.ToSlash(filepath.Clean(requestedPath)), "./")
	if pathSlash == "" || pathSlash == "." {
		return ""
	}
	cwdSlash := filepath.ToSlash(cwd)
	cwdParts := strings.Split(strings.Trim(cwdSlash, "/"), "/")
	pathParts := strings.Split(pathSlash, "/")
	if len(cwdParts) < 2 || len(pathParts) < 2 {
		return ""
	}
	max := len(cwdParts)
	if len(pathParts) < max {
		max = len(pathParts)
	}
	// Longest match first; require ≥2 segments so single-leaf cases keep the
	// real-nested-dir guard in the single-segment path below.
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

// absoluteDuplicateRootFallback handles the same model artifact when the
// model expands a workspace-relative path into an absolute path, for example
// /workspace/sonut/sonut/supermario.html while the workspace is already
// /workspace/sonut. It only strips the duplicate root segment when no genuine
// root-named subdirectory exists, preserving real nested projects.
func absoluteDuplicateRootFallback(workspaceRoot string, requestedPath string) string {
	root, err := filepath.Abs(workspaceRoot)
	if err != nil || !filepath.IsAbs(requestedPath) {
		return ""
	}
	base := filepath.Base(filepath.Clean(root))
	if base == "" || base == "." || base == string(filepath.Separator) {
		return ""
	}
	if info, statErr := os.Stat(filepath.Join(root, base)); statErr == nil && info.IsDir() {
		return ""
	}
	target, err := filepath.Abs(requestedPath)
	if err != nil {
		return ""
	}
	prefix := filepath.Clean(root) + string(filepath.Separator) + base + string(filepath.Separator)
	if !strings.HasPrefix(target, prefix) {
		return ""
	}
	return filepath.ToSlash(strings.TrimPrefix(target, prefix))
}

// resolveInventedFallback retries an absolute path whose leading directories
// look hallucinated by remapping it into a granted root — the exact remap the
// sandbox engine allows (sandbox.SuggestScopedPath) — and resolving the
// remapped path with the caller's resolver. roots[0] must be the workspace
// root; matches in extra roots report the absolute path, mirroring the
// scoped resolvers' convention.
func resolveInventedFallback(requestedPath string, roots []string, resolve func(root, path string) (string, string, error)) (string, string, bool) {
	suggestion, root := sandbox.SuggestScopedPath(requestedPath, roots)
	if suggestion == "" || suggestion == filepath.Clean(requestedPath) {
		return "", "", false
	}
	absolute, relative, err := resolve(root, suggestion)
	if err != nil {
		return "", "", false
	}
	if len(roots) > 0 && root != roots[0] {
		return absolute, absolute, true
	}
	return absolute, relative, true
}

func shouldSkipDirectory(name string) bool {
	return ignoredDirectories[name] || workspaceindex.ShouldSkipDir(name)
}

func shouldSkipWorkspaceFile(path string) bool {
	return workspaceindex.ShouldSkipFile(path)
}

// PathScope is the multi-root write scope shared with the sandbox engine.
// *sandbox.Scope satisfies it; nil means workspace-only (today's behavior).
// Roots()[0] must be the workspace root (sandbox.Scope guarantees this
// ordering); relative paths and error messages key off it.
type PathScope interface {
	Roots() []string
}

type readPathScope interface {
	ReadRoots() []string
}

// scopedRoots returns the ordered roots to try for an absolute path: the
// scope's roots when present, else just the workspace root. A non-nil scope
// must expose at least one root (the workspace root first, per PathScope); an
// empty Roots() is a contract block that fails closed with an error so the
// scoped helpers never silently accept a path with no root to validate against.
// Returning success there would resolve to an empty path and, e.g., run bash
// with Cmd.Dir == "" (the process cwd) instead of an allowed root.
func scopedRoots(workspaceRoot string, scope PathScope) ([]string, error) {
	if scope == nil {
		return []string{workspaceRoot}, nil
	}
	roots := scope.Roots()
	if len(roots) == 0 {
		return nil, fmt.Errorf("invalid path scope: no write roots configured")
	}
	return roots, nil
}

func scopedReadRoots(workspaceRoot string, scope PathScope) ([]string, error) {
	if scope == nil {
		return []string{workspaceRoot}, nil
	}
	if reader, ok := scope.(readPathScope); ok {
		roots := reader.ReadRoots()
		if len(roots) == 0 {
			return nil, fmt.Errorf("invalid path scope: no read roots configured")
		}
		return roots, nil
	}
	return scopedRoots(workspaceRoot, scope)
}

func resolveScopedReadPath(workspaceRoot string, scope PathScope, requestedPath string, runCwd ...string) (string, string, error) {
	if requestedPath == "" || !filepath.IsAbs(requestedPath) {
		root := effectiveRelativeRoot(workspaceRoot, runCwd...)
		requestedPath = normalizeWorkspaceRequestedPath(root, requestedPath)
		absolute, relative, err := resolveWorkspacePath(root, requestedPath)
		if err != nil {
			if fallback := duplicateRootFallback(root, requestedPath); fallback != "" {
				if a, r, e := resolveWorkspacePath(root, fallback); e == nil {
					return a, r, nil
				}
			}
		}
		return absolute, relative, err
	}
	relRoot := effectiveRelativeRoot(workspaceRoot, runCwd...)
	if scope == nil {
		absolute, relative, err := resolveWorkspacePath(workspaceRoot, requestedPath)
		if err != nil {
			if fallback := applyRootSlashFallback(relRoot, requestedPath); fallback != "" {
				if a, r, e := resolveWorkspacePath(relRoot, fallback); e == nil {
					return a, r, nil
				}
			}
			if a, r, ok := resolveInventedFallback(requestedPath, []string{workspaceRoot}, resolveWorkspacePath); ok {
				return a, r, nil
			}
		}
		return absolute, relative, err
	}
	roots, err := scopedReadRoots(workspaceRoot, scope)
	if err != nil {
		return "", "", err
	}
	var firstErr error
	for index, root := range roots {
		candidate := sandbox.NormalizePrefixForRoot(requestedPath, root)
		absolute, relative, err := resolveWorkspacePath(root, candidate)
		if err == nil {
			if index > 0 {
				return absolute, absolute, nil
			}
			return absolute, relative, nil
		}
		if firstErr == nil {
			firstErr = err
		}
	}
	// The absolute path matched no root; retry it as workspace-relative
	// ("/index.html" → "index.html", "/test" → "." when cwd basename is test).
	// Reads only succeed when the file/dir exists.
	if fallback := applyRootSlashFallback(relRoot, requestedPath); fallback != "" {
		if absolute, relative, err := resolveWorkspacePath(relRoot, fallback); err == nil {
			return absolute, relative, nil
		}
	}
	if absolute, relative, ok := resolveInventedFallback(requestedPath, roots, resolveWorkspacePath); ok {
		return absolute, relative, nil
	}
	return "", "", firstErr
}

// resolveScopedPath is resolveWorkspacePath generalized to a scope: relative
// paths resolve against the workspace root only; an absolute path resolves
// against the first root that contains it. The workspace root's error is
// returned when no root matches so messages stay stable. Prefix symlinks
// outside a root are resolved per root (macOS /var aliasing); symlinks INSIDE
// a root stay visible to the single-root checks, so a write target may resolve
// through a symlink only when its final location lies inside a DIFFERENT
// granted root — mirroring sandbox.Scope.validate's documented widening.
// When the matched root is an extra (non-workspace) root the second return
// value is the absolute path rather than a per-root-relative path; "relative
// to which root" is ambiguous for downstream consumers (ChangedFiles, cwd
// meta, display summaries) that document workspace-relative paths.
// When all roots deny, the workspace root's error is returned; unlike
// sandbox.Scope.validate this does not prefer traversal blocks — the
// engine layer reports those with full fidelity before tools run.
func resolveScopedPath(workspaceRoot string, scope PathScope, requestedPath string, runCwd ...string) (string, string, error) {
	if requestedPath == "" || !filepath.IsAbs(requestedPath) {
		root := effectiveRelativeRoot(workspaceRoot, runCwd...)
		requestedPath = normalizeWorkspaceRequestedPath(root, requestedPath)
		absolute, relative, err := resolveWorkspacePath(root, requestedPath)
		if err != nil {
			if fallback := duplicateRootFallback(root, requestedPath); fallback != "" {
				if a, r, e := resolveWorkspacePath(root, fallback); e == nil {
					return a, r, nil
				}
			}
		}
		return absolute, relative, err
	}
	relRoot := effectiveRelativeRoot(workspaceRoot, runCwd...)
	if scope == nil {
		absolute, relative, err := resolveWorkspacePath(workspaceRoot, requestedPath)
		if err != nil {
			if fallback := applyRootSlashFallback(relRoot, requestedPath); fallback != "" {
				if a, r, e := resolveWorkspacePath(relRoot, fallback); e == nil {
					return a, r, nil
				}
			}
			if a, r, ok := resolveInventedFallback(requestedPath, []string{workspaceRoot}, resolveWorkspacePath); ok {
				return a, r, nil
			}
		}
		return absolute, relative, err
	}
	roots, err := scopedRoots(workspaceRoot, scope)
	if err != nil {
		return "", "", err
	}
	var firstErr error
	for index, root := range roots {
		// Normalize platform-level symlinks (e.g. macOS /var -> /private/var)
		// in the prefix outside this root only, leaving in-root components
		// verbatim for the single-root symlink checks.
		candidate := sandbox.NormalizePrefixForRoot(requestedPath, root)
		absolute, relative, err := resolveWorkspacePath(root, candidate)
		if err == nil {
			if index > 0 {
				// Extra-root matches report the absolute path: "relative to
				// which root" is ambiguous downstream (ChangedFiles, cwd
				// meta, display), and consumers document workspace-relative.
				return absolute, absolute, nil
			}
			return absolute, relative, nil
		}
		if firstErr == nil {
			firstErr = err
		}
	}
	if fallback := applyRootSlashFallback(relRoot, requestedPath); fallback != "" {
		if absolute, relative, err := resolveWorkspacePath(relRoot, fallback); err == nil {
			return absolute, relative, nil
		}
	}
	if absolute, relative, ok := resolveInventedFallback(requestedPath, roots, resolveWorkspacePath); ok {
		return absolute, relative, nil
	}
	return "", "", firstErr
}

// resolveScopedTargetPath mirrors resolveWorkspaceTargetPath for write targets
// (the target may not exist yet) across all scope roots. When the matched root
// is an extra (non-workspace) root the second return value is the absolute
// path rather than a per-root-relative path; "relative to which root" is
// ambiguous for downstream consumers (ChangedFiles, cwd meta, display
// summaries) that document workspace-relative paths.
// When all roots deny, the workspace root's error is returned; unlike
// sandbox.Scope.validate this does not prefer traversal blocks — the
// engine layer reports those with full fidelity before tools run.
func resolveScopedTargetPath(workspaceRoot string, scope PathScope, requestedPath string, runCwd ...string) (string, string, error) {
	if requestedPath == "" || !filepath.IsAbs(requestedPath) {
		root := effectiveRelativeRoot(workspaceRoot, runCwd...)
		requestedPath = normalizeWorkspaceRequestedPath(root, requestedPath)
		// Pre-emptive for writes: a leading segment duplicating the workspace
		// folder name ("sonut/mario.html" inside .../sonut) is a model artifact —
		// land the file at the root instead of creating a shadow subtree. A real
		// root-named subdirectory disables the strip.
		if fallback := duplicateRootFallback(root, requestedPath); fallback != "" {
			requestedPath = fallback
		}
		return resolveWorkspaceTargetPath(root, requestedPath)
	}
	relRoot := effectiveRelativeRoot(workspaceRoot, runCwd...)
	if scope == nil {
		absolute, relative, err := resolveWorkspaceTargetPath(workspaceRoot, requestedPath)
		if err != nil {
			if fallback := applyRootSlashFallback(relRoot, requestedPath); fallback != "" {
				if a, r, e := resolveWorkspaceTargetPath(relRoot, fallback); e == nil {
					return a, r, nil
				}
			}
			if a, r, ok := resolveInventedFallback(requestedPath, []string{workspaceRoot}, resolveWorkspaceTargetPath); ok {
				return a, r, nil
			}
		}
		return absolute, relative, err
	}
	roots, err := scopedRoots(workspaceRoot, scope)
	if err != nil {
		return "", "", err
	}
	var firstErr error
	for index, root := range roots {
		// Normalize platform-level symlinks (e.g. macOS /var -> /private/var)
		// in the prefix outside this root only; in-root components stay
		// verbatim so the per-segment write-target symlink checks apply.
		candidate := sandbox.NormalizePrefixForRoot(requestedPath, root)
		absolute, relative, err := resolveWorkspaceTargetPath(root, candidate)
		if err == nil {
			if index > 0 {
				// Extra-root matches report the absolute path: "relative to
				// which root" is ambiguous downstream (ChangedFiles, cwd
				// meta, display), and consumers document workspace-relative.
				return absolute, absolute, nil
			}
			return absolute, relative, nil
		}
		if firstErr == nil {
			firstErr = err
		}
	}
	// Write fallback: "/style.css" that matches no root becomes a workspace-
	// root-relative target — strictly more useful than the guaranteed error,
	// and still confined to the workspace.
	if fallback := applyRootSlashFallback(relRoot, requestedPath); fallback != "" {
		if absolute, relative, err := resolveWorkspaceTargetPath(relRoot, fallback); err == nil {
			return absolute, relative, nil
		}
	}
	if absolute, relative, ok := resolveInventedFallback(requestedPath, roots, resolveWorkspaceTargetPath); ok {
		return absolute, relative, nil
	}
	return "", "", firstErr
}

// recheckScopedWriteTarget mirrors recheckWorkspaceWriteTarget across roots.
// Prefix symlinks outside a root are resolved per root (macOS /var aliasing);
// symlinks INSIDE a root stay visible to the single-root checks, so a write
// target may resolve through a symlink only when its final location lies
// inside a DIFFERENT granted root — mirroring sandbox.Scope.validate's
// documented widening.
func recheckScopedWriteTarget(workspaceRoot string, scope PathScope, requestedPath string) error {
	if requestedPath == "" || !filepath.IsAbs(requestedPath) {
		return recheckWorkspaceWriteTarget(workspaceRoot, requestedPath)
	}
	roots, err := scopedRoots(workspaceRoot, scope)
	if err != nil {
		return err
	}
	var firstErr error
	for _, root := range roots {
		candidate := sandbox.NormalizePrefixForRoot(requestedPath, root)
		err := recheckWorkspaceWriteTarget(root, candidate)
		if err == nil {
			return nil
		}
		if firstErr == nil {
			firstErr = err
		}
	}
	// resolveScopedTargetPath may safely recover an absolute path whose leading
	// prefix was invented by the model (for example
	// /System/Volumes/Data/home/.../sonut/dummy/file.html). The final TOCTOU
	// check must validate the SAME canonical target. Rechecking the raw spelling
	// here would undo the successful resolution and turn a one-call write into a
	// saved-content/regenerate loop. SuggestScopedPath only maps back into an
	// already-granted root; recheckWorkspaceWriteTarget still performs the full
	// per-segment symlink check on that remapped target.
	if suggestion, matchedRoot := sandbox.SuggestScopedPath(requestedPath, roots); suggestion != "" &&
		suggestion != filepath.Clean(requestedPath) {
		if err := recheckWorkspaceWriteTarget(matchedRoot, suggestion); err == nil {
			return nil
		}
	}
	return firstErr
}

// recheckScopedWriteTargetAt revalidates the model's original spelling using
// the same per-run cwd and duplicate-root normalization as the initial
// resolution. Keeping the original absolute spelling is security-critical:
// invented-path recovery may suggest an in-workspace target, but an actually
// outside or symlink-traversing write must still fail closed.
func recheckScopedWriteTargetAt(workspaceRoot string, scope PathScope, requestedPath string, runCwd string) error {
	if requestedPath == "" || !filepath.IsAbs(requestedPath) {
		root := effectiveRelativeRoot(workspaceRoot, runCwd)
		if fallback := duplicateRootFallback(root, requestedPath); fallback != "" {
			requestedPath = fallback
		}
		return recheckWorkspaceWriteTarget(root, requestedPath)
	}
	// Keep the final check aligned with resolveScopedTargetPath's safe
	// workspace-root fallback for URL-style absolute paths such as /index.html.
	// Otherwise the initial resolver succeeds at the workspace target and this
	// second check rejects the original spelling after the file body was already
	// generated.
	relRoot := effectiveRelativeRoot(workspaceRoot, runCwd)
	if fallback := applyRootSlashFallback(relRoot, requestedPath); fallback != "" {
		if err := recheckWorkspaceWriteTarget(relRoot, fallback); err == nil {
			return nil
		}
	}
	return recheckScopedWriteTarget(workspaceRoot, scope, requestedPath)
}
