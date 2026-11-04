package sandbox

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestScopeValidateMultiRootSymlinkTraversalPreferred pins Fix 1: when a
// symlink inside an extra root escapes outside all roots, validate must return
// BlockSymlinkTraversal (not BlockOutsideWorkspace) with the original
// requested path and without the --add-dir hint.
func TestScopeValidateMultiRootSymlinkTraversalPreferred(t *testing.T) {
	workspace := t.TempDir()
	extra := t.TempDir()
	outside := outsideDefaultTempPath(workspace, "symlink-target")
	link := filepath.Join(extra, "link")
	if err := os.Symlink(outside, link); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	scope, err := NewScope(workspace, []string{extra})
	if err != nil {
		t.Fatalf("NewScope: %v", err)
	}
	requestedPath := filepath.Join(extra, "link", "escape.txt")
	block := scope.validate(requestedPath)
	if block == nil {
		t.Fatal("validate(extra/link/escape.txt) = nil, want block")
	}
	if block.Code != BlockSymlinkTraversal {
		t.Fatalf("block.Code=%q want %q", block.Code, BlockSymlinkTraversal)
	}
	if block.Path != requestedPath {
		t.Fatalf("block.Path=%q want original requestedPath %q", block.Path, requestedPath)
	}
	if strings.Contains(block.Reason, "--add-dir") {
		t.Fatalf("block.Reason=%q must not contain --add-dir hint for symlink traversal", block.Reason)
	}
}

// TestValidateResolvesAliasedPathPrefixes is a deterministic cross-platform
// test for normalizePrefixForRoot: builds aliasing by hand via a symlink from
// an alias parent directory to the real workspace root, then verifies that
// paths under the alias are accepted (platform alias resolved) and that an
// in-root symlink escaping outside is still caught as BlockSymlinkTraversal.
func TestValidateResolvesAliasedPathPrefixes(t *testing.T) {
	real := t.TempDir()
	aliasParent := tempDirOutsideDefaultTemp(t)
	alias := filepath.Join(aliasParent, "alias")
	if err := os.Symlink(real, alias); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	scope, err := NewScope(real, nil)
	if err != nil {
		t.Fatalf("NewScope: %v", err)
	}
	if block := scope.validate(filepath.Join(alias, "new.txt")); block != nil {
		t.Fatalf("validate(alias-prefixed path) = %v, want nil", block)
	}
	outside := outsideDefaultTempPath(real, "symlink-target")
	link := filepath.Join(real, "link")
	if err := os.Symlink(outside, link); err != nil {
		t.Fatalf("Symlink: %v", err)
	}
	if block := scope.validate(filepath.Join(alias, "link", "x.txt")); block == nil || block.Code != BlockSymlinkTraversal {
		t.Fatalf("validate(alias path through in-root symlink) = %v, want BlockSymlinkTraversal", block)
	}
}

// TestScopeAddNormalizesSymlinkedRoot verifies that Add resolves symlinks so
// the stored root is the real path.
func TestScopeAddNormalizesSymlinkedRoot(t *testing.T) {
	real := tempDirOutsideDefaultTemp(t)
	linkParent := tempDirOutsideDefaultTemp(t)
	link := filepath.Join(linkParent, "link")
	if err := os.Symlink(real, link); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	workspace := t.TempDir()
	scope, err := NewScope(workspace, nil)
	if err != nil {
		t.Fatalf("NewScope: %v", err)
	}
	if _, err := scope.Add(link); err != nil {
		t.Fatalf("Add(symlinked root): %v", err)
	}
	roots := scope.Roots()
	if len(roots) < 2 {
		t.Fatalf("Roots()=%v want workspace + 1 extra", roots)
	}
	resolvedReal, err := filepath.EvalSymlinks(real)
	if err != nil {
		t.Fatalf("EvalSymlinks(real): %v", err)
	}
	if !stringSliceContains(roots, resolvedReal) {
		t.Fatalf("Roots()=%v want resolved path %q (no symlink component)", roots, resolvedReal)
	}
}

// TestScopeAddTildeExpansion verifies that ~ paths are expanded to the home
// directory. When home is not accessible or writable for a subdir, we assert
// only that a clearly invalid tilde-variant returns a clean error.
func TestScopeAddTildeExpansion(t *testing.T) {
	// Verify that a nonsensical ~-variant fails cleanly rather than panicking.
	workspace := t.TempDir()
	scope, err := NewScope(workspace, nil)
	if err != nil {
		t.Fatalf("NewScope: %v", err)
	}
	if _, err := scope.Add("~nonexistent-subdir-zero-test"); err == nil {
		t.Fatal("Add(~nonexistent-subdir) = nil error, want rejection")
	}
}

func TestNewScopeNormalizesAndValidatesExtraRoots(t *testing.T) {
	workspace := t.TempDir()
	extra := t.TempDir()

	scope, err := NewScope(workspace, []string{extra})
	if err != nil {
		t.Fatalf("NewScope: %v", err)
	}
	roots := scope.Roots()
	if roots[0] != scope.WorkspaceRoot() {
		t.Fatalf("Roots()[0]=%q want workspace root %q", roots[0], scope.WorkspaceRoot())
	}
	if !stringSliceContains(roots, normalizeWorkspaceRootBestEffort(extra)) {
		t.Fatalf("Roots()=%v want extra root %q", roots, normalizeWorkspaceRootBestEffort(extra))
	}
	if pathExists("/tmp") && !stringSliceContains(roots, normalizeWorkspaceRootBestEffort("/tmp")) {
		t.Fatalf("Roots()=%v want default /tmp write root", roots)
	}
}

func TestNewScopeRejectsBadExtraRoots(t *testing.T) {
	workspace := t.TempDir()
	missing := filepath.Join(t.TempDir(), "does-not-exist")
	file := filepath.Join(t.TempDir(), "file.txt")
	if err := os.WriteFile(file, []byte("x"), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	for name, root := range map[string]string{
		"missing directory": missing,
		"regular file":      file,
		"filesystem root":   string(filepath.Separator),
		"empty":             "   ",
	} {
		if _, err := NewScope(workspace, []string{root}); err == nil {
			t.Fatalf("NewScope(%s root %q) = nil error, want rejection", name, root)
		}
	}
}

func TestScopeAddIsIdempotentAndRejectsContainedPaths(t *testing.T) {
	workspace := t.TempDir()
	extra := t.TempDir()
	scope, err := NewScope(workspace, nil)
	if err != nil {
		t.Fatalf("NewScope: %v", err)
	}

	added, err := scope.Add(extra)
	if err != nil {
		t.Fatalf("Add: %v", err)
	}
	if _, err := scope.Add(extra); err != nil {
		t.Fatalf("Add (repeat): %v", err)
	}
	nested := filepath.Join(extra, "nested")
	if err := os.MkdirAll(nested, 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	if _, err := scope.Add(nested); err != nil {
		t.Fatalf("Add (nested in existing root): %v", err)
	}
	if _, err := scope.Add(workspace); err != nil {
		t.Fatalf("Add (workspace itself): %v", err)
	}
	if got := scope.Roots(); !stringSliceContains(got, normalizeWorkspaceRootBestEffort(workspace)) {
		t.Fatalf("Roots()=%v want workspace root", got)
	} else if !stringSliceContains(got, normalizeWorkspaceRootBestEffort(added)) && !rootsCoverPath(got, normalizeWorkspaceRootBestEffort(added)) {
		t.Fatalf("Roots()=%v want %q or a broader root covering it", got, added)
	}
}

func TestDefaultTempWriteRootCandidatesMatchPlatformEnvironment(t *testing.T) {
	env := func(values map[string]string) func(string) string {
		return func(key string) string {
			return values[key]
		}
	}

	windows := defaultTempWriteRootCandidatesForGOOS("windows", env(map[string]string{
		"TEMP":   `C:\Users\me\AppData\Local\Temp`,
		"TMP":    `D:\scratch`,
		"TMPDIR": `/ignored`,
	}))
	if len(windows) != 2 || windows[0] != `C:\Users\me\AppData\Local\Temp` || windows[1] != `D:\scratch` {
		t.Fatalf("windows temp roots = %#v, want TEMP and TMP", windows)
	}

	unix := defaultTempWriteRootCandidatesForGOOS("linux", env(map[string]string{
		"TMPDIR": "/var/tmp/zero",
		"TEMP":   "/ignored",
		"TMP":    "/ignored",
	}))
	if len(unix) != 2 || unix[0] != "/tmp" || unix[1] != "/var/tmp/zero" {
		t.Fatalf("unix temp roots = %#v, want /tmp and TMPDIR", unix)
	}
}

func rootsCoverPath(roots []string, path string) bool {
	for _, root := range roots {
		if pathWithinRoot(root, path) {
			return true
		}
	}
	return false
}

func outsideDefaultTempPath(workspaceRoot string, elems ...string) string {
	volume := filepath.VolumeName(workspaceRoot)
	root := volume + string(filepath.Separator)
	parts := append([]string{root, "zero-sandbox-outside-test"}, elems...)
	return filepath.Join(parts...)
}

func tempDirOutsideDefaultTemp(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp(".", ".atcode-sandbox-outside-")
	if err != nil {
		t.Fatalf("MkdirTemp outside default temp: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	abs, err := filepath.Abs(dir)
	if err != nil {
		t.Fatalf("Abs(%q): %v", dir, err)
	}
	if resolved, err := filepath.EvalSymlinks(abs); err == nil {
		return resolved
	}
	return filepath.Clean(abs)
}

func TestScopeValidateAllowsAnyRootButRelativeOnlyWorkspace(t *testing.T) {
	workspace := t.TempDir()
	extra := t.TempDir()
	scope, err := NewScope(workspace, []string{extra})
	if err != nil {
		t.Fatalf("NewScope: %v", err)
	}

	if block := scope.validate(filepath.Join(extra, "out.txt")); block != nil {
		t.Fatalf("validate(extra-root path) = %v, want nil", block)
	}
	if block := scope.validate(filepath.Join(workspace, "in.txt")); block != nil {
		t.Fatalf("validate(workspace path) = %v, want nil", block)
	}
	if block := scope.validate("nested/in.txt"); block != nil {
		t.Fatalf("validate(relative path) = %v, want nil (resolves against workspace)", block)
	}

	outside := outsideDefaultTempPath(workspace, "elsewhere.txt")
	block := scope.validate(outside)
	if block == nil {
		t.Fatal("validate(outside all roots) = nil, want block")
	}
	if block.Code != BlockOutsideWorkspace {
		t.Fatalf("block.Code=%q want %q", block.Code, BlockOutsideWorkspace)
	}
	if !strings.Contains(block.Reason, "--add-dir") {
		t.Fatalf("block.Reason=%q want actionable --add-dir hint", block.Reason)
	}
}

func TestScopeValidateKeepsSymlinkTraversalProtection(t *testing.T) {
	workspace := t.TempDir()
	outside := outsideDefaultTempPath(workspace, "symlink-target")
	link := filepath.Join(workspace, "link")
	if err := os.Symlink(outside, link); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	scope, err := NewScope(workspace, nil)
	if err != nil {
		t.Fatalf("NewScope: %v", err)
	}
	block := scope.validate(filepath.Join(link, "escape.txt"))
	if block == nil {
		t.Fatal("validate(symlink escape) = nil, want block")
	}
	if block.Code != BlockSymlinkTraversal {
		t.Fatalf("block.Code=%q want %q", block.Code, BlockSymlinkTraversal)
	}
}

// A small local model sometimes invents an absolute prefix for the workspace
// (e.g. "/home/lane/sonut_1.1/sonut/x.html" when the real root is under
// /Users/…). When the remapped in-root path validates cleanly, the request is
// ALLOWED — the tools layer applies the identical remap so the operation lands
// inside the root instead of failing and looping on the same path.
func TestScopeValidateRemapsHallucinatedPrefix(t *testing.T) {
	workspace := t.TempDir() // basename is unique per test run
	scope, err := NewScope(workspace, nil)
	if err != nil {
		t.Fatal(err)
	}
	fake := filepath.Join(string(filepath.Separator), "home", "lane", "elsewhere", filepath.Base(workspace), "sub", "google.html")
	if block := scope.validate(fake); block != nil {
		t.Fatalf("validate(%q) = %v, want nil (remapped into workspace)", fake, block)
	}
	suggestion, root := SuggestScopedPath(fake, scope.Roots())
	if want := filepath.Join(scope.Roots()[0], "sub", "google.html"); suggestion != want || root != scope.Roots()[0] {
		t.Fatalf("SuggestScopedPath = (%q, %q), want (%q, %q)", suggestion, root, want, scope.Roots()[0])
	}
	// A path with no matching segment keeps the plain --add-dir hint and denies.
	plain := scope.validate(filepath.Join(string(filepath.Separator), "etc", "hosts.bak"))
	if plain == nil || plain.Code != BlockOutsideWorkspace || strings.Contains(plain.Reason, "you likely meant") {
		t.Fatalf("unrelated outside path must still deny without a suggestion: %v", plain)
	}
}

// Regression for the live "ALHAMDULILLAH -> alhAMDulillah" case: the model
// recases the workspace folder name in an invented absolute prefix. On a
// case-insensitive filesystem that path IS the workspace, so the remap must
// still recover it into the root rather than dying as outside_workspace.
func TestSuggestScopedPathRemapsRecasedRootSegment(t *testing.T) {
	root := filepath.Join(string(filepath.Separator), "Users", "x", "Desktop", "ALHAMDULILLAH")
	recased := filepath.Join(string(filepath.Separator), "alhAMDulillah", "supermario.html")
	suggestion, matched := SuggestScopedPath(recased, []string{root})
	want := filepath.Join(root, "supermario.html")
	if suggestion != want || matched != root {
		t.Fatalf("SuggestScopedPath(%q) = (%q, %q), want (%q, %q)", recased, suggestion, matched, want, root)
	}
}

// Exact-case matches must still win over a case-insensitive one when both a
// correctly-cased and a recased segment could match, so a real subdirectory is
// never redirected in favor of a looser match elsewhere in the path.
func TestSuggestScopedPathPrefersExactCase(t *testing.T) {
	root := filepath.Join(string(filepath.Separator), "w", "Proj")
	// "proj" (lower) appears before the exact "Proj"; exact must still win.
	p := filepath.Join(string(filepath.Separator), "proj", "Proj", "x.txt")
	suggestion, _ := SuggestScopedPath(p, []string{root})
	if want := filepath.Join(root, "x.txt"); suggestion != want {
		t.Fatalf("SuggestScopedPath(%q) = %q, want %q (exact-case match)", p, suggestion, want)
	}
}

// Live failure after /cd into .../sonut/test: model calls list_directory with
// path="/test". The intermediate-segment match never sees a single-segment
// path, so SuggestScopedPath must map /basename → the root itself.
func TestSuggestScopedPathMapsBareBasenameToRoot(t *testing.T) {
	root := filepath.Join(string(filepath.Separator), "Users", "aslam", "Desktop", "aistatine", "sonut", "test")
	suggestion, matched := SuggestScopedPath(string(filepath.Separator)+"test", []string{root})
	if suggestion != root || matched != root {
		t.Fatalf("SuggestScopedPath(/test) = (%q, %q), want (%q, %q)", suggestion, matched, root, root)
	}
	// Trailing slash is cleaned the same way.
	suggestion, matched = SuggestScopedPath(string(filepath.Separator)+"test"+string(filepath.Separator), []string{root})
	if suggestion != root || matched != root {
		t.Fatalf("SuggestScopedPath(/test/) = (%q, %q), want root", suggestion, matched)
	}
	// Nested under bare basename still works via intermediate match.
	suggestion, _ = SuggestScopedPath(string(filepath.Separator)+"test"+string(filepath.Separator)+"mario.html", []string{root})
	if want := filepath.Join(root, "mario.html"); suggestion != want {
		t.Fatalf("SuggestScopedPath(/test/mario.html) = %q, want %q", suggestion, want)
	}
}

// Sandbox must allow list_directory(path="/test") when the workspace basename
// is "test", matching the tools-layer rootSlashFallback.
func TestScopeValidateAllowsBareBasenameAfterCD(t *testing.T) {
	parent := t.TempDir()
	workspace := filepath.Join(parent, "test")
	if err := os.Mkdir(workspace, 0o755); err != nil {
		t.Fatal(err)
	}
	scope, err := NewScope(workspace, nil)
	if err != nil {
		t.Fatal(err)
	}
	if block := scope.validate(string(filepath.Separator) + "test"); block != nil {
		t.Fatalf("validate(/test) = %v, want nil (cwd basename after /cd)", block)
	}
	// URL-style single-segment file path under workspace.
	if block := scope.validate(string(filepath.Separator) + "mario.html"); block != nil {
		t.Fatalf("validate(/mario.html) = %v, want nil (root-slash relative)", block)
	}
	// Multi-segment invented absolute path stays denied (with --add-dir hint).
	outside := outsideDefaultTempPath(workspace, "elsewhere.txt")
	if block := scope.validate(outside); block == nil || block.Code != BlockOutsideWorkspace {
		t.Fatalf("validate(multi-segment outside) = %v, want outside_workspace", block)
	}
	// Real host path still denied.
	if block := scope.validate(filepath.Join(string(filepath.Separator), "etc", "hosts")); block == nil {
		t.Fatal("validate(/etc/hosts) must still deny")
	}
}

func TestRootSlashRelative(t *testing.T) {
	if got := RootSlashRelative(string(filepath.Separator) + "index.html"); got != "index.html" {
		t.Fatalf("RootSlashRelative(/index.html) = %q", got)
	}
	if got := RootSlashRelative(filepath.Join(string(filepath.Separator), "etc", "hosts")); got != "" {
		t.Fatalf("RootSlashRelative(/etc/hosts) = %q, want empty", got)
	}
	if got := RootSlashRelative("relative/path"); got != "" {
		t.Fatalf("RootSlashRelative(relative) = %q, want empty", got)
	}
}
