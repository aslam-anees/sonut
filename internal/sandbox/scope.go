package sandbox

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
)

// Scope is the shared set of directories the sandbox allows writes in: the
// workspace root plus zero or more user-granted extra roots. One instance is
// created per run and shared by the policy engine, the OS command runners, and
// the file tools, so a mid-session Add is immediately visible to every layer.
type Scope struct {
	mu            sync.RWMutex
	workspaceRoot string
	readRoots     []string
	extraRoots    []string
}

// NewScope builds a scope for workspaceRoot plus the given extra roots. The
// workspace root is normalized best-effort (it may not exist in tests); every
// extra root must normalize strictly via Add and an invalid one fails the
// whole construction so a bad --add-dir/config entry surfaces at startup.
func NewScope(workspaceRoot string, extras []string) (*Scope, error) {
	scope := &Scope{workspaceRoot: normalizeWorkspaceRootBestEffort(workspaceRoot)}
	for _, extra := range extras {
		if _, err := scope.Add(extra); err != nil {
			return nil, fmt.Errorf("write root %q: %w", extra, err)
		}
	}
	if scope.workspaceRoot != "" {
		for _, root := range defaultTempWriteRootCandidates() {
			_, _ = scope.Add(root)
		}
	}
	return scope, nil
}

// WorkspaceRoot returns the resolved workspace root. It is safe to call
// without acquiring the lock because workspaceRoot is immutable after
// construction.
func (s *Scope) WorkspaceRoot() string {
	return s.workspaceRoot
}

// Roots returns the workspace root first, then the extra roots, as a copy.
func (s *Scope) Roots() []string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	roots := make([]string, 0, 1+len(s.extraRoots))
	roots = append(roots, s.workspaceRoot)
	roots = append(roots, s.extraRoots...)
	return roots
}

// ReadRoots returns the workspace root, write roots, and read-only roots as a
// copy. Write roots are included because anything writable must also be readable
// by the tool layer and native sandbox profile.
func (s *Scope) ReadRoots() []string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	roots := make([]string, 0, 1+len(s.extraRoots)+len(s.readRoots))
	roots = append(roots, s.workspaceRoot)
	roots = append(roots, s.extraRoots...)
	roots = append(roots, s.readRoots...)
	return dedupeScopeRoots(roots)
}

// Add grants write access under path. The path must be an existing directory;
// it is home-expanded, made absolute, and symlink-resolved before being
// trusted, and the filesystem root is rejected outright. Adding a path already
// covered by an existing root is an idempotent success.
func (s *Scope) Add(path string) (string, error) {
	root, err := normalizeScopeRoot(path)
	if err != nil {
		return "", err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, existing := range append([]string{s.workspaceRoot}, s.extraRoots...) {
		if pathWithinRoot(existing, root) {
			return root, nil
		}
	}
	s.extraRoots = append(s.extraRoots, root)
	return root, nil
}

// AddRead grants read-only access under path. If the path is already covered by
// a writable root, no separate read root is stored.
func (s *Scope) AddRead(path string) (string, error) {
	root, err := normalizeScopeRoot(path)
	if err != nil {
		return "", err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.writeRootCoversLocked(root) {
		return root, nil
	}
	for _, existing := range s.readRoots {
		if pathWithinRoot(existing, root) {
			return root, nil
		}
	}
	s.readRoots = append(s.readRoots, root)
	return root, nil
}

func (s *Scope) AddTemporaryRead(path string) (string, func(), error) {
	root, err := normalizeScopeRoot(path)
	if err != nil {
		return "", nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.writeRootCoversLocked(root) {
		return root, func() {}, nil
	}
	for _, existing := range s.readRoots {
		if pathWithinRoot(existing, root) {
			return root, func() {}, nil
		}
	}
	s.readRoots = append(s.readRoots, root)
	return root, func() { s.removeReadRoot(root) }, nil
}

func (s *Scope) AddTemporaryWrite(path string) (string, func(), error) {
	root, err := normalizeScopeRoot(path)
	if err != nil {
		return "", nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.writeRootCoversLocked(root) {
		return root, func() {}, nil
	}
	s.extraRoots = append(s.extraRoots, root)
	return root, func() { s.removeWriteRoot(root) }, nil
}

func (s *Scope) writeRootCoversLocked(root string) bool {
	for _, existing := range append([]string{s.workspaceRoot}, s.extraRoots...) {
		if pathWithinRoot(existing, root) {
			return true
		}
	}
	return false
}

func (s *Scope) removeReadRoot(root string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.readRoots = removeScopeRoot(s.readRoots, root)
}

func (s *Scope) removeWriteRoot(root string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.extraRoots = removeScopeRoot(s.extraRoots, root)
}

func removeScopeRoot(roots []string, root string) []string {
	next := roots[:0]
	for _, existing := range roots {
		if existing != root {
			next = append(next, existing)
		}
	}
	return next
}

func dedupeScopeRoots(roots []string) []string {
	seen := map[string]struct{}{}
	out := roots[:0]
	for _, root := range roots {
		if root == "" {
			continue
		}
		if _, ok := seen[root]; ok {
			continue
		}
		seen[root] = struct{}{}
		out = append(out, root)
	}
	return out
}

// validate reports whether requestedPath is allowed by any scope root.
// Relative paths resolve against the workspace root only; absolute paths are
// accepted if they validate (including per-segment symlink checks) under ANY
// root. A symlink whose final target lies inside any granted root is allowed —
// this is a deliberate semantic widening compared with single-root validation,
// because the true write target is inside an allowed root.
//
// When all roots deny, a BlockSymlinkTraversal result from any root is
// preferred over BlockOutsideWorkspace; the --add-dir hint is appended
// only on outside_workspace results. The returned block always carries
// the caller's original requestedPath.
func (s *Scope) validate(requestedPath string) *pathBlock {
	return s.validateAgainstRoots(requestedPath, s.Roots())
}

func (s *Scope) validateRead(requestedPath string) *pathBlock {
	return s.validateAgainstRoots(requestedPath, s.ReadRoots())
}

func (s *Scope) validateAgainstRoots(requestedPath string, roots []string) *pathBlock {
	if len(roots) == 0 {
		return &pathBlock{
			Code:   BlockOutsideWorkspace,
			Path:   requestedPath,
			Reason: fmt.Sprintf("%s is outside the workspace", requestedPath),
		}
	}
	if !filepath.IsAbs(requestedPath) {
		return validateWorkspacePath(roots[0], requestedPath)
	}
	// For each root, normalize the leading path prefix so that platform-level
	// symlinks (e.g. macOS /var -> /private/var) are resolved before comparing
	// against the symlink-resolved scope roots, while leaving workspace-internal
	// symlinks intact so validateWorkspacePath can detect traversal blocks.
	var outsideBlock *pathBlock
	var traversalBlock *pathBlock
	for _, root := range roots {
		normalized := NormalizePrefixForRoot(requestedPath, root)
		block := validateWorkspacePath(root, normalized)
		if block == nil {
			return nil
		}
		switch block.Code {
		case BlockSymlinkTraversal:
			if traversalBlock == nil {
				traversalBlock = block
			}
		default:
			if outsideBlock == nil {
				outsideBlock = block
			}
		}
	}
	// Prefer symlink-traversal: the path was lexically inside a granted root
	// but crossed an in-root symlink — the --add-dir hint would be misleading.
	if traversalBlock != nil {
		return &pathBlock{
			Code:   BlockSymlinkTraversal,
			Path:   requestedPath,
			Reason: traversalBlock.Reason,
		}
	}
	// Plain outside-workspace denial. Before denying, try the invented-prefix
	// remap: models routinely hallucinate the leading directories of an
	// otherwise-correct in-workspace path. When the remapped path validates
	// cleanly inside a granted root, ALLOW the request — the tools layer applies
	// the identical remap (SuggestScopedPath), so the operation lands on the
	// remapped path, never the requested one.
	reason := fmt.Sprintf("%s is outside the workspace (use /add-dir or --add-dir to allow writes there)", requestedPath)
	if suggestion, root := SuggestScopedPath(requestedPath, roots); suggestion != "" {
		if validateWorkspacePath(root, suggestion) == nil {
			return nil
		}
		reason = fmt.Sprintf("%s is outside the workspace — the path prefix looks invented; the real workspace root is %s, so you likely meant %s. Retry with that exact path (or a path relative to the workspace root). Do NOT retry the same path.",
			requestedPath, roots[0], suggestion)
	}
	// URL-style single-segment paths ("/index.html", "/mario.html") never match
	// a host root and never hit SuggestScopedPath. Treat ONLY those as
	// workspace-relative so the sandbox agrees with the tools rootSlashFallback
	// for the common case without remapping multi-segment host paths like
	// /zero-sandbox-outside-test/elsewhere.txt into the workspace.
	if rel := RootSlashRelative(requestedPath); rel != "" && !strings.Contains(rel, "/") {
		for _, root := range roots {
			if validateWorkspacePath(root, rel) == nil {
				return nil
			}
		}
		reason = fmt.Sprintf("%s looks like a workspace-relative path with a leading slash; use %q (relative to %s) instead of an absolute host path. Do NOT retry %s.",
			requestedPath, rel, roots[0], requestedPath)
	}
	return &pathBlock{
		Code:   BlockOutsideWorkspace,
		Path:   requestedPath,
		Reason: reason,
	}
}

// SuggestScopedPath maps an absolute path with a hallucinated prefix (e.g.
// "/home/<user>/…/sonut/google.html" when the real root is
// "/Users/…/sonut") back into a granted root by matching the root's basename
// among the requested path's segments and reattaching the trailing segments.
// Returns the remapped path and the root it belongs to, or "" when no root's
// basename appears in the path. Exported so the tools layer applies the exact
// same remap the engine allowed, keeping the two layers byte-consistent.
//
// Single-segment paths like "/test" (when a granted root's basename is "test")
// are also recovered: models treat the cwd leaf as a host-root path after /cd
// into .../test, and the intermediate-segment match below never sees them.
func SuggestScopedPath(requestedPath string, roots []string) (string, string) {
	if !filepath.IsAbs(requestedPath) {
		return "", ""
	}
	segments := strings.Split(filepath.ToSlash(filepath.Clean(requestedPath)), "/")
	// Exact-case match first so a genuine subdirectory that happens to differ
	// only in case from a root basename is never silently redirected.
	for _, root := range roots {
		base := filepath.Base(root)
		if base == "" || base == "." || base == string(filepath.Separator) {
			continue
		}
		for i := len(segments) - 2; i >= 0; i-- {
			if segments[i] == base {
				return filepath.Join(append([]string{root}, segments[i+1:]...)...), root
			}
		}
		// Path is exactly "/basename" (or "/basename/") for this root.
		if meaningfulSegmentCount(segments) == 1 && segments[len(segments)-1] == base {
			return root, root
		}
	}
	// Case-insensitive fallback: the model routinely recases the workspace
	// folder ("ALHAMDULILLAH" -> "alhAMDulillah"), which the exact match above
	// misses and the write then dies as outside_workspace. This runs only after
	// the strict resolution already failed, and still remaps strictly INTO a
	// granted root, so it cannot widen the sandbox — it just recovers a path the
	// case-insensitive filesystem would have accepted anyway.
	for _, root := range roots {
		base := filepath.Base(root)
		if base == "" || base == "." || base == string(filepath.Separator) {
			continue
		}
		for i := len(segments) - 2; i >= 0; i-- {
			if strings.EqualFold(segments[i], base) {
				return filepath.Join(append([]string{root}, segments[i+1:]...)...), root
			}
		}
		if meaningfulSegmentCount(segments) == 1 && strings.EqualFold(segments[len(segments)-1], base) {
			return root, root
		}
	}
	return "", ""
}

func meaningfulSegmentCount(segments []string) int {
	n := 0
	for _, s := range segments {
		if s != "" {
			n++
		}
	}
	return n
}

// RootSlashRelative reinterprets a failed absolute path as workspace-relative.
// Models routinely write "/index.html" or "/src/app.js" (URL habit) meaning the
// file under the workspace root. Host-root prefixes (/Users, /var, /etc, …)
// are rejected so real machine paths stay outside-workspace errors.
// Returns the relative form, or "" when not applicable.
func RootSlashRelative(requestedPath string) string {
	if !filepath.IsAbs(requestedPath) {
		return ""
	}
	trimmed := strings.TrimLeft(filepath.ToSlash(filepath.Clean(requestedPath)), "/")
	if trimmed == "" {
		return ""
	}
	first := trimmed
	if slash := strings.IndexByte(first, '/'); slash >= 0 {
		first = first[:slash]
	}
	switch strings.ToLower(first) {
	case "applications", "bin", "dev", "etc", "home", "library", "opt", "private", "sbin", "system", "tmp", "usr", "users", "var", "volumes":
		return ""
	}
	return trimmed
}

// NormalizePrefixForRoot resolves platform-level symlinks (e.g. macOS
// /var -> /private/var) in the portion of absPath that lies outside
// resolvedRoot, while leaving workspace-internal path components intact so
// that validateWorkspacePath can detect symlink traversal blocks there.
// It is exported because the tools layer shares it to normalize absolute
// paths per scope root before running its own single-root checks.
//
// Algorithm: walk absPath component-by-component, resolving each via
// EvalSymlinks. Once the running resolved prefix equals resolvedRoot we are
// inside the root — stop resolving and append the remaining components
// verbatim. If a component inside the root is a symlink, leave it for
// validateWorkspacePath to handle. Non-existent tail components are always
// appended verbatim.
//
// The walk is volume-aware so it works on Windows as well as POSIX. On
// Windows the same alias problem appears in a different guise — a workspace
// created under an 8.3 short path (C:\Users\RUNNER~1\...) is resolved by
// EvalSymlinks to its long form (C:\Users\runneradmin\...), so a raw
// short-form request would escape the long-form root unless its prefix is
// resolved here first. The component walk must therefore start from the
// volume root (C:\ or \\host\share\), not "/", or it would mangle a drive
// path into a drive-relative form (C:\Users -> C:Users) that the single-root
// checks treat as RELATIVE — failing the policy gate OPEN. On POSIX
// VolumeName is empty and the volume root reduces to "/", so behavior there
// is byte-identical to a plain "/"-rooted walk.
func NormalizePrefixForRoot(absPath, resolvedRoot string) string {
	volume := filepath.VolumeName(absPath)
	volumeRoot := volume + string(filepath.Separator)
	tail := strings.TrimPrefix(filepath.Clean(absPath), volume)
	parts := strings.Split(strings.TrimPrefix(tail, string(filepath.Separator)), string(filepath.Separator))
	current := volumeRoot
	for i, part := range parts {
		if part == "" {
			continue
		}
		// If we've reached the resolved root boundary, stop resolving and
		// append the remaining tail verbatim so validateWorkspacePath sees the
		// original symlink names.
		if current == resolvedRoot {
			return filepath.Join(append([]string{current}, parts[i:]...)...)
		}
		next := filepath.Join(current, part)
		info, lerr := os.Lstat(next)
		if lerr != nil {
			// Non-existent component — append rest verbatim.
			return filepath.Join(append([]string{current}, parts[i:]...)...)
		}
		if info.Mode()&os.ModeSymlink != 0 {
			// Symlink. Only resolve it if we're still outside the root.
			if pathWithinRoot(resolvedRoot, current) {
				// Inside root — leave this symlink for validateWorkspacePath.
				return filepath.Join(append([]string{current}, parts[i:]...)...)
			}
			// Outside root (or a jump into the root) — resolve this platform-level symlink.
			resolved, err := filepath.EvalSymlinks(next)
			if err != nil {
				return filepath.Join(append([]string{current}, parts[i:]...)...)
			}
			current = resolved
			continue
		}
		// Regular component outside root — resolve it.
		resolved, err := filepath.EvalSymlinks(next)
		if err != nil {
			current = next
		} else {
			current = resolved
		}
	}
	return current
}

func normalizeWorkspaceRootBestEffort(workspaceRoot string) string {
	trimmed := strings.TrimSpace(workspaceRoot)
	if trimmed == "" {
		return ""
	}
	absolute, err := filepath.Abs(trimmed)
	if err != nil {
		return filepath.Clean(trimmed)
	}
	if resolved, err := filepath.EvalSymlinks(absolute); err == nil {
		return resolved
	}
	return filepath.Clean(absolute)
}

func normalizeScopeRoot(path string) (string, error) {
	trimmed := strings.TrimSpace(path)
	if trimmed == "" {
		return "", errors.New("write root path is empty")
	}
	if trimmed == "~" || strings.HasPrefix(trimmed, "~/") || strings.HasPrefix(trimmed, "~"+string(filepath.Separator)) {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", fmt.Errorf("expand ~: %w", err)
		}
		trimmed = filepath.Join(home, strings.TrimPrefix(strings.TrimPrefix(trimmed[1:], "/"), string(filepath.Separator)))
	}
	absolute, err := filepath.Abs(trimmed)
	if err != nil {
		return "", err
	}
	resolved, err := filepath.EvalSymlinks(absolute)
	if err != nil {
		return "", fmt.Errorf("write root must exist: %w", err)
	}
	info, err := os.Stat(resolved)
	if err != nil {
		return "", err
	}
	if !info.IsDir() {
		return "", fmt.Errorf("write root %s is not a directory", resolved)
	}
	if filepath.Dir(resolved) == resolved {
		return "", fmt.Errorf("refusing filesystem root %s as a write root", resolved)
	}
	return resolved, nil
}

func defaultTempWriteRootCandidates() []string {
	return defaultTempWriteRootCandidatesForGOOS(runtime.GOOS, os.Getenv)
}

func defaultTempWriteRootCandidatesForGOOS(goos string, getenv func(string) string) []string {
	var roots []string
	if goos == "windows" {
		for _, key := range []string{"TEMP", "TMP"} {
			if root := strings.TrimSpace(getenv(key)); root != "" {
				roots = append(roots, root)
			}
		}
		return roots
	}
	if goos != "windows" {
		roots = append(roots, "/tmp")
	}
	if tmpdir := strings.TrimSpace(getenv("TMPDIR")); tmpdir != "" {
		roots = append(roots, tmpdir)
	}
	return roots
}

func defaultTempWriteRoots() []string {
	return normalizeProfileDirs(defaultTempWriteRootCandidates())
}

func pathWithinRoot(root string, candidate string) bool {
	if root == "" {
		return false
	}
	relative, err := filepath.Rel(root, candidate)
	if err != nil {
		return false
	}
	return relative == "." || (relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator)) && !filepath.IsAbs(relative))
}
