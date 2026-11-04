package tools

import (
	"context"
	"fmt"
	"net/url"
	"regexp"
	"sort"
	"strings"

	"github.com/aistatine/sonut/internal/browser"
)

// Browser-open interception. GUI apps cannot launch inside the sandbox: `open
// <url>` / `xdg-open <url>` / osascript browser opens fail with opaque errors
// (NSOSStatusError on macOS) and the model wastes turns retrying. Intercept
// these commands in the registry BEFORE permission checks and execution, and
// steer the model to the playwright MCP browser tools instead (when they are
// registered).
//
// Matching is deliberately conservative: only commands whose primary purpose
// is opening a URL or a GUI browser are intercepted. `open somefile.txt`,
// `open .`, and every unrelated command run untouched and may fail naturally.

// playwrightNavigateToolName is the registered name of the playwright MCP
// navigation tool (mcp_<server>_<tool> per registryToolName).
const playwrightNavigateToolName = "mcp_playwright_browser_navigate"

// playwrightTabsToolName is the registered name of the playwright MCP
// tab-management tool. When the model calls this with a file:// URL to open
// a new tab pointing at a local file, we intercept and serve via HTTP.
const playwrightTabsToolName = "mcp_playwright_browser_tabs"

// BrowserOpenOptionHost / BrowserOpenOptionSandboxInstall are the two verbatim
// choices the model must present (via ask_user) when something the agent wants
// to open or use is not available inside the sandbox. Exported so prompt text
// and tests reference the exact same wording.
const (
	BrowserOpenOptionHost           = "1. Open/use it OUTSIDE the sandbox on my machine (host), with my approval"
	BrowserOpenOptionSandboxInstall = "2. Try downloading/installing it INSIDE the sandbox and use it there"
)

var (
	// httpURLPattern extracts the first http(s) URL token in a command.
	httpURLPattern = regexp.MustCompile(`https?://[^\s'"]+`)
	// fileURLPattern extracts file:// URLs from a command.
	fileURLPattern = regexp.MustCompile(`file://[^\s'"]+`)
	// knownBrowserAppPattern matches `-a <browser>` application names used with
	// macOS `open` (quoted or bare).
	knownBrowserAppPattern = regexp.MustCompile(`(?i)^["']?(safari|google\s*chrome|chrome|chromium|firefox|arc|brave(\s*browser)?|microsoft\s*edge|edge|opera)["']?$`)
	// bareHostPattern matches host-like tokens the user wrote without a scheme
	// (`x.com`, `www.example.org/path`). Used so `open x.com` is treated as a
	// browser open instead of falling through to the sandbox (which then
	// denies path `/` as outside_workspace and burns the turn).
	bareHostPattern = regexp.MustCompile(`(?i)^(?:www\.)?[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?(?:\.[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?)+(?:[/:?#].*)?$`)
)

// detectBrowserOpenCommand reports whether command's primary purpose is to
// open a URL in a GUI browser, returning the extracted URL when parseable.
// file:// URLs and absolute local .html paths are also returned so the caller
// can redirect them to a local HTTP file server.
func detectBrowserOpenCommand(command string) (url string, matched bool) {
	trimmed := strings.TrimSpace(command)
	if trimmed == "" {
		return "", false
	}
	// Only inspect the first pipeline/sequence segment: a compound command that
	// merely mentions a URL later (e.g. curl | grep) must not be intercepted.
	segment := trimmed
	for _, sep := range []string{"&&", "||", ";", "|"} {
		if idx := strings.Index(segment, sep); idx >= 0 {
			segment = segment[:idx]
		}
	}
	segment = strings.TrimSpace(segment)
	fields := strings.Fields(segment)
	if len(fields) == 0 {
		return "", false
	}
	url = httpURLPattern.FindString(segment)
	// Also capture file:// URLs — they need to be served over HTTP.
	if url == "" {
		if furl := fileURLPattern.FindString(segment); furl != "" {
			url = furl
		}
	}

	switch fields[0] {
	case "open": // macOS
		// `open -a <Browser> [url]` — a GUI browser launch even without a URL.
		for i := 1; i < len(fields)-1; i++ {
			if fields[i] == "-a" {
				app := fields[i+1]
				// Re-join a possibly quoted two-word app name ("Google Chrome").
				if i+2 < len(fields) && (strings.HasPrefix(app, `"`) || strings.HasPrefix(app, `'`)) && !strings.HasSuffix(app, `"`) && !strings.HasSuffix(app, `'`) {
					app = app + " " + fields[i+2]
				}
				if knownBrowserAppPattern.MatchString(app) {
					return url, true
				}
				return "", false // -a with a non-browser app: let it run
			}
		}
		// Bare `open <url>`: intercept for http(s), file://, and bare hosts
		// like `open x.com` (observed live: sandbox denied path `/` because
		// the intercept never matched and exec_command tried to scope the cwd).
		// `open file.txt` / `open .` / `open /path/to/file.txt` run untouched —
		// only .html/.htm local files are treated as browser targets.
		if url != "" {
			return url, true
		}
		// Absolute path to an .html file (e.g. `open /tmp/result.html`).
		if len(fields) == 2 {
			if _, isFile := parseFileURL(fields[1]); isFile {
				return fields[1], true
			}
			if hostURL := normalizeBareHostURL(fields[1]); hostURL != "" {
				return hostURL, true
			}
		}
		// `open -a Safari x.com` with bare host already handled above when -a
		// matched a known browser; if we only have a trailing bare host after
		// flags we don't understand, leave it alone.
		return "", false
	case "xdg-open", "sensible-browser", "x-www-browser", "gnome-open": // Linux
		if url != "" {
			return url, true
		}
		if len(fields) >= 2 {
			if hostURL := normalizeBareHostURL(fields[len(fields)-1]); hostURL != "" {
				return hostURL, true
			}
		}
		return "", false
	case "start": // Windows cmd
		if url != "" {
			return url, true
		}
		if len(fields) >= 2 {
			if hostURL := normalizeBareHostURL(fields[len(fields)-1]); hostURL != "" {
				return hostURL, true
			}
		}
		return "", false
	case "osascript": // AppleScript browser opens
		lower := strings.ToLower(segment)
		if strings.Contains(lower, "open location") ||
			((strings.Contains(lower, `tell application "safari"`) || strings.Contains(lower, `tell application "google chrome"`)) && url != "") {
			return url, true
		}
		return "", false
	}
	return "", false
}

// fileExtDeny is a cheap denylist so `open notes.txt` never becomes
// https://notes.txt via the bare-host heuristic.
var fileExtDeny = map[string]bool{
	"txt": true, "md": true, "go": true, "js": true, "ts": true, "tsx": true,
	"jsx": true, "py": true, "rb": true, "rs": true, "c": true, "h": true,
	"cpp": true, "hpp": true, "java": true, "kt": true, "swift": true,
	"json": true, "yaml": true, "yml": true, "toml": true, "xml": true,
	"csv": true, "log": true, "sh": true, "zsh": true, "bash": true,
	"png": true, "jpg": true, "jpeg": true, "gif": true, "webp": true,
	"svg": true, "pdf": true, "zip": true, "tar": true, "gz": true,
	"mp3": true, "mp4": true, "mov": true, "wav": true, "ico": true,
	"css": true, "scss": true, "less": true, "sql": true, "env": true,
	"lock": true, "sum": true, "mod": true, "conf": true, "cfg": true,
	"ini": true, "plist": true, "dylib": true, "so": true, "a": true,
	"o": true, "class": true, "wasm": true, "map": true, "min": true,
}

// siteAlias resolves common brand short-names the user types without a TLD
// (`open twitter`, `open youtube`). Without this, bare tokens miss the host
// intercept, fall into the sandbox, and fail with outside_workspace — while
// the same request with `x.com` works. Keep this map tiny; unknown names stay
// non-hosts.
var siteAlias = map[string]string{
	// Full brand words only — no 1–2 letter tokens (`x`, `yt`) that could
	// collide with local filenames (`open x`).
	"twitter":   "https://x.com",
	"youtube":   "https://www.youtube.com",
	"github":    "https://github.com",
	"google":    "https://www.google.com",
	"gmail":     "https://mail.google.com",
	"reddit":    "https://www.reddit.com",
	"facebook":  "https://www.facebook.com",
	"instagram": "https://www.instagram.com",
	"linkedin":  "https://www.linkedin.com",
	"wikipedia": "https://www.wikipedia.org",
}

// resolveSiteAlias maps a brand short-name (optionally with a path) to a full
// https URL. "twitter/home" → "https://x.com/home". Empty when unknown.
func resolveSiteAlias(raw string) string {
	s := strings.TrimSpace(raw)
	s = strings.Trim(s, `"'`)
	if s == "" || strings.Contains(s, "://") || strings.ContainsAny(s, ` /$~`) {
		return ""
	}
	name := s
	path := ""
	if i := strings.IndexAny(s, "/?#"); i >= 0 {
		name = s[:i]
		path = s[i:]
	}
	// Single-label only (no dots) — dotted hosts go through normalizeBareHostURL.
	if strings.Contains(name, ".") {
		return ""
	}
	base, ok := siteAlias[strings.ToLower(name)]
	if !ok {
		return ""
	}
	return base + path
}

// normalizeBareHostURL turns a bare host token (`x.com`, `www.x.com/path`) or
// a known brand short-name (`twitter`) into an https URL, or returns "" when
// the token is not host-like (so we never intercept `open notes.txt` or `open .`).
func normalizeBareHostURL(raw string) string {
	s := strings.TrimSpace(raw)
	s = strings.Trim(s, `"'`)
	if s == "" || strings.Contains(s, "://") || strings.HasPrefix(s, "/") || strings.HasPrefix(s, ".") {
		return ""
	}
	// Local paths and shell expansions are never hosts.
	if strings.ContainsAny(s, ` ~$`) {
		return ""
	}
	// Brand short-name before the host heuristic so `open twitter` never falls
	// through to the sandbox (live failure: denied for twitter, worked for x.com).
	if alias := resolveSiteAlias(s); alias != "" {
		return alias
	}
	// Drop query/path for extension check on the last path segment.
	hostPart := s
	if i := strings.IndexAny(hostPart, "/?#"); i >= 0 {
		hostPart = hostPart[:i]
	}
	if !strings.Contains(hostPart, ".") {
		return ""
	}
	// Last label is the TLD — reject if it looks like a source/file extension.
	if i := strings.LastIndex(hostPart, "."); i >= 0 {
		tld := strings.ToLower(hostPart[i+1:])
		if fileExtDeny[tld] || len(tld) > 24 {
			return ""
		}
	}
	if !bareHostPattern.MatchString(s) {
		return ""
	}
	// twitter.com still redirects; open x.com so both spellings land the same.
	if strings.EqualFold(hostPart, "twitter.com") || strings.EqualFold(hostPart, "www.twitter.com") {
		rest := ""
		if len(s) > len(hostPart) {
			rest = s[len(hostPart):]
		}
		return "https://x.com" + rest
	}
	return "https://" + s
}

// interceptBrowserOpen returns a synthetic result for a tool call that tries
// to open a GUI browser or navigate to a file:// URL. ok is false when the
// tool should run normally.
//
// Paths:
//  1. bash / exec_command: detect GUI-browser open commands. For http(s) URLs
//     open the user's REAL browser on the host (after a one-line yes when
//     needed) — this is what "open x.com in browser" means. Sandbox redirect
//     loops wasted minutes of thinking without ever launching a window.
//  2. mcp_playwright_browser_navigate / tabs with file:// → local HTTP server.
func (registry *Registry) interceptBrowserOpen(ctx context.Context, toolName string, args map[string]any, options RunOptions) (Result, bool) {
	// Path 2a: playwright navigate called with a file:// URL.
	if toolName == playwrightNavigateToolName {
		rawURL, _ := args["url"].(string)
		rawURL = strings.TrimSpace(rawURL)
		if filePath, ok := parseFileURL(rawURL); ok {
			return registry.openLocalFileInBrowser(ctx, filePath, rawURL, options), true
		}
		return Result{}, false
	}

	// Path 2b: playwright browser_tabs called with a file:// URL.
	if toolName == playwrightTabsToolName {
		rawURL, _ := args["url"].(string)
		rawURL = strings.TrimSpace(rawURL)
		if rawURL == "" {
			return Result{}, false
		}
		if filePath, ok := parseFileURL(rawURL); ok {
			return registry.openLocalFileInBrowser(ctx, filePath, rawURL, options), true
		}
		return Result{}, false
	}

	// Path 1: bash / exec_command GUI-browser open.
	// (browser_open stays the automation helper — do not steal it for host
	// launch or snapshot/click sessions break.)
	if toolName != "bash" && toolName != "exec_command" {
		return Result{}, false
	}
	command, err := aliasedStringArg(args, []string{"command", "cmd", "script", "shell"}, "", true, false)
	if err != nil || strings.TrimSpace(command) == "" {
		return Result{}, false
	}
	rawURL, matched := detectBrowserOpenCommand(command)
	if !matched {
		return Result{}, false
	}

	// If the URL is a file:// or a local html path, serve it over HTTP.
	if filePath, ok := parseFileURL(rawURL); ok {
		return registry.openLocalFileInBrowser(ctx, filePath, rawURL, options), true
	}

	// Host browser path: user asked to open a page on their machine.
	target := resolveOpenTarget(rawURL)
	if target == "" {
		// GUI browser launch without a URL (e.g. `open -a Safari`) — still
		// needs a concrete URL so we open the right tab.
		return hostBrowserNeedsURLResult(), true
	}
	if u, err := url.Parse(target); err != nil || u.Scheme == "" || u.Host == "" {
		return Result{
			Status: StatusError,
			Output: "Error: could not parse browser URL from command. Use a full URL like https://example.com.",
			Meta:   map[string]string{"blocked": "gui_browser_open", "url": rawURL},
		}, true
	}

	return openHostBrowser(ctx, target, options), true
}

// resolveOpenTarget normalizes a raw open target (URL, bare host, or brand
// short-name) into a full http(s)/file URL the host launcher can use.
func resolveOpenTarget(raw string) string {
	target := strings.TrimSpace(raw)
	target = strings.Trim(target, `"'`)
	if target == "" {
		return ""
	}
	if strings.HasPrefix(target, "file://") || strings.HasPrefix(target, "/") {
		return target
	}
	if strings.Contains(target, "://") {
		return target
	}
	if hostURL := normalizeBareHostURL(target); hostURL != "" {
		return hostURL
	}
	// Last resort: if it looks host-like after a force https, keep it.
	if strings.Contains(target, ".") && !strings.ContainsAny(target, ` ~$`) {
		return "https://" + target
	}
	return ""
}

// openHostBrowser launches the system default browser for target.
//
// Policy: NEVER refuse a user-requested page open. The model only reaches this
// path after the user asked to open a site (or the agent is fulfilling that
// ask). An extra yes/no in "ask" mode caused live failures — twitter denied,
// x.com worked — when confirmation was missing, timed out, or the model took
// a sandboxed route instead. Opening a URL in the user's browser is reversible
// (they close the tab); it is not a destructive host action.
//
// Never steers into a playwright/sandbox-retry loop.
func openHostBrowser(ctx context.Context, target string, options RunOptions) Result {
	_ = ctx
	_ = options
	if err := openURLForHost(target); err != nil {
		return Result{
			Status: StatusError,
			Output: "Error: failed to open browser for " + target + ": " + err.Error() +
				". Retry once with exec_command `open " + target + "` (macOS) or `xdg-open " + target + "` (Linux).",
			Meta: map[string]string{"blocked": "gui_browser_open", "url": target},
		}
	}
	return Result{
		Status: StatusOK,
		Output: "Opened " + target + " in your default browser.",
		Meta:   map[string]string{"opened": "host_browser", "url": target},
	}
}

// openURLForHost is the production hook for launching the system browser.
// Tests replace it so CI never spawns a real browser window.
var openURLForHost = browser.OpenURL

func hostBrowserNeedsURLResult() Result {
	return Result{
		Status: StatusError,
		Output: "Error: browser launch needs a URL. Call exec_command with `open https://example.com` " +
			"(or use ask_user to confirm the site first).",
		Meta: map[string]string{"blocked": "gui_browser_open"},
	}
}

// openLocalFileInBrowser makes local HTML validation a single operation. The
// previous handoff only told the model about a localhost URL; it did not open
// that URL, which left the visible browser idle and encouraged it to fall back
// to `open file://...` or web search. Prefer a registered Playwright navigation
// tool, then the built-in web_browser helper, and retain an explicit handoff
// only when no automation surface is available.
func (registry *Registry) openLocalFileInBrowser(ctx context.Context, filePath, originalURL string, options RunOptions) Result {
	localURL, err := serveLocalFile(filePath)
	if err != nil {
		return Result{
			Status: StatusError,
			Output: fmt.Sprintf("Error: cannot serve local file — %s. Check that the file exists and the path is correct.", err),
			Meta:   map[string]string{"blocked": "file_url", "path": filePath},
		}
	}
	var lastFailure *Result
	for _, toolName := range registry.browserNavigateToolNames() {
		result := registry.RunWithOptions(ctx, toolName, map[string]any{"url": localURL}, options)
		if result.Meta == nil {
			result.Meta = map[string]string{}
		}
		result.Meta["served_file"] = filePath
		result.Meta["blocked"] = "file_url"
		result.Meta["local_url"] = localURL
		result.Meta["original_url"] = originalURL
		result.Meta["browser_navigation"] = toolName
		if result.Status == StatusOK {
			// MCP navigation results do not always include a screenshot, unlike
			// the built-in web_browser helper. Capture one through the same
			// browser session so local `open page.html` gets a real in-app
			// preview instead of falling back to the project logo.
			if preview := registry.captureBrowserPreview(ctx, toolName, options); len(preview.Images) > 0 {
				result.Images = preview.Images
				result.Meta["browser_preview_screenshot"] = "true"
				result.Meta["screenshot_attached"] = "true"
			}
			result.Output = "Opened local page in the visible browser at " + localURL + " via " + toolName + ".\n\n" + result.Output
			return result
		}
		lastFailure = &result
	}
	if lastFailure != nil {
		return *lastFailure
	}
	message := fmt.Sprintf(
		"file:// URLs are blocked by the browser sandbox. The file has been served locally at %s — "+
			"use %s with {\"url\": \"%s\"} to open it. "+
			"(Original URL was: %s)",
		localURL, playwrightNavigateToolName, localURL, originalURL,
	)
	return Result{
		// This is a transparent compatibility handoff, not a failed operation.
		// Returning StatusError made the model display a red error and often retry
		// the same blocked file:// URL. The local server is already running, so
		// report success and give the next browser action explicitly.
		Status: StatusOK,
		Output: message,
		Meta: map[string]string{
			"blocked":   "file_url",
			"local_url": localURL,
			"path":      filePath,
		},
	}
}

// captureBrowserPreview asks the same registered browser backend that handled
// navigation for a screenshot. It is intentionally best-effort: the page
// opening result remains successful when a backend has no screenshot surface.
func (registry *Registry) captureBrowserPreview(ctx context.Context, navigationTool string, options RunOptions) Result {
	for _, toolName := range registry.browserScreenshotToolNames(navigationTool) {
		args := map[string]any{}
		if toolName == "web_browser" {
			args["action"] = "screenshot"
		}
		preview := registry.RunWithOptions(ctx, toolName, args, options)
		if preview.Status == StatusOK && len(preview.Images) > 0 {
			return preview
		}
	}
	return Result{}
}

func (registry *Registry) browserScreenshotToolNames(navigationTool string) []string {
	seen := map[string]bool{}
	names := make([]string, 0, 4)
	add := func(name string) {
		if name == "" || seen[name] {
			return
		}
		if _, ok := registry.Get(name); !ok {
			return
		}
		seen[name] = true
		names = append(names, name)
	}

	if navigationTool == "web_browser" {
		add("web_browser")
		return names
	}
	if strings.HasPrefix(navigationTool, "mcp_") && strings.HasSuffix(navigationTool, "_browser_navigate") {
		serverPrefix := strings.TrimSuffix(navigationTool, "_browser_navigate")
		add(serverPrefix + "_browser_take_screenshot")
		add(serverPrefix + "_browser_screenshot")
		for _, tool := range registry.All() {
			name := tool.Name()
			if strings.HasPrefix(name, serverPrefix+"_browser_") && strings.Contains(name, "screenshot") {
				add(name)
			}
		}
	}
	return names
}

// browserNavigateToolNames returns available navigation tools in preference
// order. The default Playwright MCP name is first; custom MCP server names are
// supported by the stable `_browser_navigate` suffix; web_browser is the local
// helper fallback. Sorting makes custom-server selection deterministic.
func (registry *Registry) browserNavigateToolNames() []string {
	candidates := make([]string, 0, 3)
	if _, ok := registry.Get(playwrightNavigateToolName); ok {
		candidates = append(candidates, playwrightNavigateToolName)
	}
	var custom []string
	for _, tool := range registry.All() {
		name := tool.Name()
		if name != playwrightNavigateToolName && strings.HasPrefix(name, "mcp_") && strings.HasSuffix(name, "_browser_navigate") {
			custom = append(custom, name)
		}
	}
	sort.Strings(custom)
	candidates = append(candidates, custom...)
	if _, ok := registry.Get("web_browser"); ok {
		candidates = append(candidates, "web_browser")
	}
	return candidates
}
