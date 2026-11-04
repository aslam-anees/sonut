package tools

import (
	"context"
	"fmt"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/aistatine/sonut/internal/localcontrol"
	"github.com/aistatine/sonut/internal/security"
	"github.com/aistatine/sonut/internal/sonutruntime"
)

const localBrowserOutputBudgetBytes = 128 * 1024

const (
	browserInstallTimeout     = 10 * time.Minute
	browserPickElementTimeout = 15 * time.Second
	browserNavigateTimeout    = 60 * time.Second
)

type localControlRunner interface {
	Run(ctx context.Context, args ...string) (localcontrol.CommandResult, error)
}

type timeoutLocalControlRunner interface {
	RunWithTimeout(ctx context.Context, timeout time.Duration, args ...string) (localcontrol.CommandResult, error)
}

type browserAppLauncher interface {
	LaunchBrowserApp(ctx context.Context, request localcontrol.BrowserAppLaunchRequest) (localcontrol.BrowserAppLaunchResult, error)
}

func NewLocalBrowserTools(options localcontrol.BrowserOptions) []Tool {
	return []Tool{
		newBrowserInstallTool(options),
		newBrowserLaunchTool(options),
		newBrowserConnectTool(options),
		newBrowserOpenTool(options),
		newBrowserSnapshotTool(options),
		newBrowserClickTool(options),
		newBrowserTypeTool(options),
		newBrowserPressTool(options),
		newBrowserActionTool(options),
		newWebBrowserTool(options),
	}
}

type browserInstallTool struct {
	baseTool
	browser localcontrol.Browser
}

func newBrowserInstallTool(options localcontrol.BrowserOptions) Tool {
	return browserInstallTool{
		baseTool: baseTool{
			name:        "browser_install",
			description: "Install the local browser runtime used by browser automation.",
			parameters: Schema{
				Type: "object",
				Properties: map[string]PropertySchema{
					"with_deps": {Type: "boolean", Description: "Also install Linux system dependencies when supported by the helper.", Default: false},
				},
				AdditionalProperties: false,
			},
			safety: localControlSafety(options.Enabled, SideEffectLocalBrowser, "Downloads browser runtime files for local browser automation."),
		},
		browser: localcontrol.NewBrowser(options),
	}
}

func (tool browserInstallTool) Run(ctx context.Context, args map[string]any) Result {
	withDeps, err := boolArg(args, "with_deps", false)
	if err != nil {
		return errorResult("Error: Invalid arguments for browser_install: " + err.Error())
	}
	commandArgs := []string{"install"}
	if withDeps {
		commandArgs = append(commandArgs, "--with-deps")
	}
	return browserCommandResultTimeout(ctx, tool.browser, "install", commandArgs, browserInstallTimeout)
}

type browserLaunchTool struct {
	baseTool
	browser  localcontrol.Browser
	launcher browserAppLauncher
}

func newBrowserLaunchTool(options localcontrol.BrowserOptions) Tool {
	return newBrowserLaunchToolWithLauncher(options, localcontrol.NewBrowserAppLauncher(localcontrol.BrowserAppLaunchOptions{}))
}

func newBrowserLaunchToolWithLauncher(options localcontrol.BrowserOptions, launcher browserAppLauncher) Tool {
	return browserLaunchTool{
		baseTool: baseTool{
			name:        "browser_launch",
			description: "Launch a supported local Chromium/Electron app with Chrome DevTools enabled and attach browser automation to it. Use this for installed Electron apps such as Discord instead of launching GUI apps through shell commands.",
			parameters: Schema{
				Type: "object",
				Properties: map[string]PropertySchema{
					"app":           {Type: "string", Description: "Supported local app to launch.", Enum: []string{"discord"}},
					"debug_port":    {Type: "integer", Description: "Local Chrome DevTools port to expose.", Default: localcontrol.DefaultDevToolsPort, Minimum: intPtr(1024), Maximum: intPtr(65535)},
					"stop_existing": {Type: "boolean", Description: "Stop the supported app before relaunching it with DevTools enabled.", Default: true},
					"wait":          {Type: "boolean", Description: "Wait until the DevTools endpoint responds before returning.", Default: true},
					"connect":       {Type: "boolean", Description: "Attach browser automation to the DevTools endpoint after launch.", Default: true},
				},
				Required:             []string{"app"},
				AdditionalProperties: false,
			},
			safety: localControlSafety(options.Enabled, SideEffectLocalBrowser, "Launches a supported local Chromium/Electron app with DevTools enabled and attaches local browser automation."),
		},
		browser:  localcontrol.NewBrowser(options),
		launcher: launcher,
	}
}

func (tool browserLaunchTool) RejectBeforePermission(args map[string]any) (Result, bool) {
	if _, err := browserLaunchRequestArg(args); err != nil {
		return errorResult("Error: Invalid arguments for browser_launch: " + err.Error()), true
	}
	return Result{}, false
}

func (tool browserLaunchTool) Run(ctx context.Context, args map[string]any) Result {
	request, err := browserLaunchRequestArg(args)
	if err != nil {
		return errorResult("Error: Invalid arguments for browser_launch: " + err.Error())
	}
	result, err := tool.launcher.LaunchBrowserApp(ctx, request)
	if err != nil {
		return errorResult("Error launching browser app: " + err.Error())
	}
	output := fmt.Sprintf("Launched %s with DevTools at %s (pid %d).", result.App, result.DevToolsURL, result.PID)
	connect, err := boolArg(args, "connect", true)
	if err != nil {
		return errorResult("Error: Invalid arguments for browser_launch: " + err.Error())
	}
	if !connect {
		return Result{
			Status: StatusOK,
			Output: output + "\n\nNext call browser_connect with target " + strconv.Itoa(result.DebugPort) + ".",
			Meta: map[string]string{
				"browser_command": "launch",
				"app":             result.App,
				"debug_port":      strconv.Itoa(result.DebugPort),
				"pid":             strconv.Itoa(result.PID),
			},
			Display: Display{Summary: "launch completed", Kind: "browser"},
		}
	}
	connectResult := browserCommandResult(ctx, tool.browser, "connect", []string{"connect", strconv.Itoa(result.DebugPort)})
	if connectResult.Status == StatusOK {
		connectResult.Output = output + "\n\nBrowser automation is already connected; use browser_snapshot next.\n\n" + connectResult.Output
	} else {
		connectResult.Output = output + "\n\n" + connectResult.Output
	}
	if connectResult.Meta == nil {
		connectResult.Meta = map[string]string{}
	}
	connectResult.Meta["app"] = result.App
	connectResult.Meta["debug_port"] = strconv.Itoa(result.DebugPort)
	connectResult.Meta["pid"] = strconv.Itoa(result.PID)
	return connectResult
}

type browserConnectTool struct {
	baseTool
	browser localcontrol.Browser
}

func newBrowserConnectTool(options localcontrol.BrowserOptions) Tool {
	return browserConnectTool{
		baseTool: baseTool{
			name:        "browser_connect",
			description: "Attach local browser automation to an existing Chrome/Electron DevTools endpoint. For Electron apps, first launch the app with --remote-debugging-port=<port>, then connect to that port instead of using desktop control.",
			parameters: Schema{
				Type: "object",
				Properties: map[string]PropertySchema{
					"target": {Type: "string", Description: "CDP target: a port such as 9222, host:port, http(s) URL, ws(s) URL, or DevTools websocket URL."},
				},
				Required:             []string{"target"},
				AdditionalProperties: false,
			},
			safety: localControlSafety(options.Enabled, SideEffectLocalBrowser, "Attaches to a local browser or Electron app exposed over the Chrome DevTools Protocol."),
		},
		browser: localcontrol.NewBrowser(options),
	}
}

func (tool browserConnectTool) RejectBeforePermission(args map[string]any) (Result, bool) {
	if _, err := browserConnectTargetArg(args); err != nil {
		return errorResult("Error: Invalid arguments for browser_connect: " + err.Error()), true
	}
	return Result{}, false
}

func (tool browserConnectTool) Run(ctx context.Context, args map[string]any) Result {
	target, err := browserConnectTargetArg(args)
	if err != nil {
		return errorResult("Error: Invalid arguments for browser_connect: " + err.Error())
	}
	return browserCommandResult(ctx, tool.browser, "connect", []string{"connect", target})
}

type browserOpenTool struct {
	baseTool
	browser localcontrol.Browser
}

func newBrowserOpenTool(options localcontrol.BrowserOptions) Tool {
	return browserOpenTool{
		baseTool: baseTool{
			name:        "browser_open",
			description: "Open a URL in the local browser automation session.",
			parameters: Schema{
				Type: "object",
				Properties: map[string]PropertySchema{
					"url": {Type: "string", Description: "HTTP/HTTPS URL or local file path (e.g. './index.html', '/abs/path/to/file.html'). Bare hosts default to https."},
				},
				Required:             []string{"url"},
				AdditionalProperties: false,
			},
			safety: localControlSafety(options.Enabled, SideEffectNetwork, "Opens a URL through the local browser automation helper. For already-running Chrome/Electron apps, use browser_connect instead. If first use reports a missing browser runtime, run browser_install once."),
		},
		browser: localcontrol.NewBrowser(options),
	}
}

func (tool browserOpenTool) RejectBeforePermission(args map[string]any) (Result, bool) {
	if _, err := browserOpenURLArg(args); err != nil {
		// Relative HTML paths are resolved against RunOptions.Cwd at execution
		// time. Do not reject them early against sonut's process directory.
		if raw, ok := args["url"].(string); ok && !strings.Contains(raw, "://") && !filepath.IsAbs(strings.TrimSpace(raw)) && isHTMLFilePath(raw) {
			return Result{}, false
		}
		return errorResult("Error: Invalid arguments for browser_open: " + err.Error()), true
	}
	return Result{}, false
}

func (tool browserOpenTool) Run(ctx context.Context, args map[string]any) Result {
	return tool.runWithCwd(ctx, args, "")
}

func (tool browserOpenTool) RunWithOptions(ctx context.Context, args map[string]any, options RunOptions) Result {
	return tool.runWithCwd(ctx, args, options.Cwd)
}

func (tool browserOpenTool) runWithCwd(ctx context.Context, args map[string]any, cwd string) Result {
	rawURL, err := browserOpenURLArgWithCwd(args, cwd)
	if err != nil {
		return errorResult("Error: Invalid arguments for browser_open: " + err.Error())
	}
	return browserOpenWithPreview(ctx, tool.browser, rawURL)
}

// browserOpenWithPreview keeps browser_open useful to both the model and the
// in-app preview. The screenshot is optional: a browser that can navigate but
// cannot capture a visual still returns the successful open result unchanged.
func browserOpenWithPreview(ctx context.Context, browser localcontrol.Browser, rawURL string) Result {
	return browserCommandResultTimeout(ctx, browser, "open", []string{"open", rawURL}, browserNavigateTimeout)
}

type browserSnapshotTool struct {
	baseTool
	browser localcontrol.Browser
}

func newBrowserSnapshotTool(options localcontrol.BrowserOptions) Tool {
	return browserSnapshotTool{
		baseTool: baseTool{
			name:        "browser_snapshot",
			description: "Return an accessibility snapshot from the current local browser automation session.",
			parameters: Schema{
				Type: "object",
				Properties: map[string]PropertySchema{
					"interactive":                {Type: "boolean", Description: "Return interactive elements only.", Default: true},
					"include_cursor_interactive": {Type: "boolean", Description: "Include cursor-interactive elements such as onclick containers.", Default: false},
					"compact":                    {Type: "boolean", Description: "Use compact snapshot output.", Default: false},
					"depth":                      {Type: "integer", Description: "Optional tree depth limit.", Minimum: intPtr(0), Maximum: intPtr(20)},
					"selector":                   {Type: "string", Description: "Optional CSS selector to scope the snapshot."},
				},
				AdditionalProperties: false,
			},
			safety: localControlSafety(options.Enabled, SideEffectLocalBrowser, "Reads the current local browser automation session state."),
		},
		browser: localcontrol.NewBrowser(options),
	}
}

func (tool browserSnapshotTool) Run(ctx context.Context, args map[string]any) Result {
	commandArgs, err := browserSnapshotCommandArgs(args)
	if err != nil {
		return errorResult("Error: Invalid arguments for browser_snapshot: " + err.Error())
	}
	return browserCommandResult(ctx, tool.browser, "snapshot", commandArgs)
}

func (tool browserSnapshotTool) RejectBeforePermission(args map[string]any) (Result, bool) {
	if _, err := browserSnapshotCommandArgs(args); err != nil {
		return errorResult("Error: Invalid arguments for browser_snapshot: " + err.Error()), true
	}
	return Result{}, false
}

type browserClickTool struct {
	baseTool
	browser localcontrol.Browser
}

func newBrowserClickTool(options localcontrol.BrowserOptions) Tool {
	return browserClickTool{
		baseTool: baseTool{
			name:        "browser_click",
			description: "Click a ref from browser_snapshot in the current local browser automation session.",
			parameters: Schema{
				Type: "object",
				Properties: map[string]PropertySchema{
					"ref": {Type: "string", Description: "Element ref from browser_snapshot, for example e114."},
				},
				Required:             []string{"ref"},
				AdditionalProperties: false,
			},
			safety: localControlSafety(options.Enabled, SideEffectLocalBrowser, "Clicks an element in the local browser automation session."),
		},
		browser: localcontrol.NewBrowser(options),
	}
}

func (tool browserClickTool) RejectBeforePermission(args map[string]any) (Result, bool) {
	if _, err := browserRefArg(args); err != nil {
		return errorResult("Error: Invalid arguments for browser_click: " + err.Error()), true
	}
	return Result{}, false
}

func (tool browserClickTool) Run(ctx context.Context, args map[string]any) Result {
	ref, err := browserRefArg(args)
	if err != nil {
		return errorResult("Error: Invalid arguments for browser_click: " + err.Error())
	}
	result := browserCommandResult(ctx, tool.browser, "click", []string{"click", ref})
	if result.Status != StatusOK {
		return augmentBrowserFailure(ctx, tool.browser, result)
	}
	return result
}

// overlayCoveringSelectorPattern extracts the CSS selector of the element that
// intercepted a click from a Playwright-style "covered by <selector>" error, so
// the caller can look up dismiss controls inside it.
var overlayCoveringSelectorPattern = regexp.MustCompile(`(?i)(?:covered by|intercepts pointer events)[^<]*<([^\s>]+)`)

// findDismissCandidatesScript looks inside a covering element for the buttons
// that most likely dismiss it (close/×/dismiss/cookie-accept controls) and
// returns a small JSON array of selectors, capped and best-effort.
const findDismissCandidatesScript = `(() => {
  const root = document.querySelector(%s);
  if (!root) return JSON.stringify([]);
  const candidates = root.querySelectorAll(
    'button, [role="button"], a, [aria-label], [class*="close" i], [class*="dismiss" i]'
  );
  const results = [];
  for (const el of candidates) {
    const label = (el.getAttribute('aria-label') || el.textContent || '').trim();
    const looksLikeClose = /close|dismiss|✕|×|got it|accept|ok/i.test(label) ||
      /close|dismiss/i.test(el.className || '');
    if (!looksLikeClose) continue;
    let selector = el.tagName.toLowerCase();
    if (el.id) selector += '#' + el.id;
    else if (el.getAttribute('aria-label')) selector += '[aria-label="' + el.getAttribute('aria-label') + '"]';
    results.push(selector + (label ? (' ("' + label.slice(0, 40) + '")') : ''));
    if (results.length >= 8) break;
  }
  return JSON.stringify(results);
})()`

// augmentBrowserFailure enriches a failed click/action result: when the error
// indicates the target was covered by an overlay, it looks inside the covering
// element for likely dismiss controls and appends them, plus a hint to call
// dismiss_overlays. This replaces blind click retries with a grounded next
// step (see the "click on element 'e69'" overlay-mask failure loop).
func augmentBrowserFailure(ctx context.Context, browser localcontrol.Browser, result Result) Result {
	if match := overlayCoveringSelectorPattern.FindStringSubmatch(result.Output); match != nil {
		coveringSelector := match[1]
		evalResult := browserCommandResult(ctx, browser, "eval", []string{"eval", fmt.Sprintf(findDismissCandidatesScript, strconv.Quote(coveringSelector))})
		hint := "\n\nThe click was blocked by an overlay (" + coveringSelector + "). Call the dismiss_overlays action, or click one of its close controls first"
		if evalResult.Status == StatusOK && strings.TrimSpace(evalResult.Output) != "" && evalResult.Output != "[]" {
			hint += ": " + evalResult.Output
		}
		hint += "."
		result.Output += hint
		return result
	}
	if locateFailurePattern.MatchString(result.Output) {
		snapshotResult := browserCommandResult(ctx, browser, "snapshot", []string{"snapshot", "-i"})
		if strings.TrimSpace(snapshotResult.Output) != "" {
			result.Output += "\n\nThe element/role/name guessed above was not found. Do not guess again — these are the actual interactive elements on the page right now (role, name, ref):\n" + snapshotResult.Output
		}
		if localcontrol.BrowserHeadful() {
			result.Output += "\n\nIf this happens again, call browser_action with command \"pick_element\" and ask the user to click the element instead of guessing a third time."
		}
		return result
	}
	return result
}

// locateFailurePattern matches "could not locate/find element ..." style
// errors so a not-found response is never returned bare: the caller gets a
// fresh grounded snapshot instead of guessing a role/name again.
var locateFailurePattern = regexp.MustCompile(`(?i)could not (?:locate|find) element|no element (?:matching|found)|element not found`)

type browserTypeTool struct {
	baseTool
	browser localcontrol.Browser
}

func newBrowserTypeTool(options localcontrol.BrowserOptions) Tool {
	return browserTypeTool{
		baseTool: baseTool{
			name:        "browser_type",
			description: "Type text into a ref from browser_snapshot in the current local browser automation session.",
			parameters: Schema{
				Type: "object",
				Properties: map[string]PropertySchema{
					"ref":  {Type: "string", Description: "Element ref from browser_snapshot, for example e114."},
					"text": {Type: "string", Description: "Text to type into the target element."},
				},
				Required:             []string{"ref", "text"},
				AdditionalProperties: false,
			},
			safety: localControlSafety(options.Enabled, SideEffectLocalBrowser, "Types text into an element in the local browser automation session."),
		},
		browser: localcontrol.NewBrowser(options),
	}
}

func (tool browserTypeTool) RejectBeforePermission(args map[string]any) (Result, bool) {
	if _, _, err := browserTypeArgs(args); err != nil {
		return errorResult("Error: Invalid arguments for browser_type: " + err.Error()), true
	}
	return Result{}, false
}

func (tool browserTypeTool) Run(ctx context.Context, args map[string]any) Result {
	ref, text, err := browserTypeArgs(args)
	if err != nil {
		return errorResult("Error: Invalid arguments for browser_type: " + err.Error())
	}
	return browserCommandResult(ctx, tool.browser, "type", []string{"type", ref, text})
}

type browserPressTool struct {
	baseTool
	browser localcontrol.Browser
}

func newBrowserPressTool(options localcontrol.BrowserOptions) Tool {
	return browserPressTool{
		baseTool: baseTool{
			name:        "browser_press",
			description: "Press a keyboard key in the current local browser automation session.",
			parameters: Schema{
				Type: "object",
				Properties: map[string]PropertySchema{
					"key": {Type: "string", Description: "Key to press, for example Enter, Escape, or Control+L."},
				},
				Required:             []string{"key"},
				AdditionalProperties: false,
			},
			safety: localControlSafety(options.Enabled, SideEffectLocalBrowser, "Presses a key in the local browser automation session."),
		},
		browser: localcontrol.NewBrowser(options),
	}
}

func (tool browserPressTool) RejectBeforePermission(args map[string]any) (Result, bool) {
	if _, err := browserKeyArg(args); err != nil {
		return errorResult("Error: Invalid arguments for browser_press: " + err.Error()), true
	}
	return Result{}, false
}

func (tool browserPressTool) Run(ctx context.Context, args map[string]any) Result {
	key, err := browserKeyArg(args)
	if err != nil {
		return errorResult("Error: Invalid arguments for browser_press: " + err.Error())
	}
	return browserCommandResult(ctx, tool.browser, "press", []string{"press", key})
}

type browserActionTool struct {
	baseTool
	browser localcontrol.Browser
}

func browserSnapshotCommandArgs(args map[string]any) ([]string, error) {
	interactive, err := boolArg(args, "interactive", true)
	if err != nil {
		return nil, err
	}
	includeCursorInteractive, err := boolArg(args, "include_cursor_interactive", false)
	if err != nil {
		return nil, err
	}
	compact, err := boolArg(args, "compact", false)
	if err != nil {
		return nil, err
	}
	depth, err := intArg(args, "depth", 0, 0, 20)
	if err != nil {
		return nil, err
	}
	selector, err := stringArgWithEmpty(args, "selector", "", false, false)
	if err != nil {
		return nil, err
	}
	selector = strings.TrimSpace(selector)
	if err := validateBrowserSnapshotSelector(selector); err != nil {
		return nil, err
	}

	commandArgs := []string{"snapshot"}
	if interactive {
		commandArgs = append(commandArgs, "-i")
	}
	if includeCursorInteractive {
		commandArgs = append(commandArgs, "-C")
	}
	if compact {
		commandArgs = append(commandArgs, "-c")
	}
	if depth > 0 {
		commandArgs = append(commandArgs, "-d", strconv.Itoa(depth))
	}
	if selector != "" {
		commandArgs = append(commandArgs, "-s", selector)
	}
	return commandArgs, nil
}

func newBrowserActionTool(options localcontrol.BrowserOptions) Tool {
	return browserActionTool{
		baseTool: baseTool{
			name:        "browser_action",
			description: "Run an allowed action against the current local browser automation session. Includes dismiss_overlays (close cookie banners/modals blocking a click) and pick_element (headful mode only: ask the user to click the element you need instead of guessing).",
			parameters: Schema{
				Type: "object",
				Properties: map[string]PropertySchema{
					"command": {Type: "string", Description: "Allowed browser command to run.", Enum: browserActionCommandNames()},
					"args":    {Type: "array", Items: &PropertySchema{Type: "string"}, Description: "Command arguments. Use refs from browser_snapshot where required."},
				},
				Required:             []string{"command"},
				AdditionalProperties: false,
			},
			safety: localControlSafety(options.Enabled, SideEffectLocalBrowser, "Interacts with the local browser automation session."),
		},
		browser: localcontrol.NewBrowser(options),
	}
}

func (tool browserActionTool) RejectBeforePermission(args map[string]any) (Result, bool) {
	if _, _, err := browserActionArgs(args); err != nil {
		return errorResult("Error: Invalid arguments for browser_action: " + err.Error()), true
	}
	return Result{}, false
}

func (tool browserActionTool) Run(ctx context.Context, args map[string]any) Result {
	command, commandArgs, err := browserActionArgs(args)
	if err != nil {
		return errorResult("Error: Invalid arguments for browser_action: " + err.Error())
	}
	if command == "dismiss_overlays" {
		return dismissOverlays(ctx, tool.browser)
	}
	if command == "pick_element" {
		return pickElement(ctx, tool.browser)
	}
	result := browserCommandResult(ctx, tool.browser, command, commandArgs)
	// Connecting through browser_action is itself a verification boundary: the
	// next useful model action is grounded by seeing the attached page. Keep the
	// standalone browser_connect/browser_launch paths lightweight, but capture a
	// preview for this explicit interactive action.
	if command == "connect" && result.Status == StatusOK {
		result = attachBrowserPreviewForced(ctx, tool.browser, result)
	}
	if result.Status != StatusOK {
		return augmentBrowserFailure(ctx, tool.browser, result)
	}
	return result
}

// dismissOverlaysScript best-effort closes common cookie banners/modals by
// clicking anything that looks like a close/accept control, then returns what
// it clicked so the model can confirm progress instead of retrying blindly.
const dismissOverlaysScript = `(() => {
  const selectors = [
    '[aria-label="close" i]', '[aria-label="dismiss" i]', '[class*="close" i]',
    'button[class*="dismiss" i]', '.cookie-banner button', '#onetrust-accept-btn-handler',
    '[data-testid*="close" i]', '[class*="modal"] button', '[class*="overlay"] button',
  ];
  const clicked = [];
  for (const sel of selectors) {
    document.querySelectorAll(sel).forEach((el) => {
      if (clicked.length >= 5) return;
      if (el.closest('form') || el.type === 'submit') return;
      const style = getComputedStyle(el);
      if (style.display === 'none' || style.visibility === 'hidden') return;
      el.click();
      clicked.push(sel);
    });
  }
  return JSON.stringify(clicked);
})()`

func dismissOverlays(ctx context.Context, browser localcontrol.Browser) Result {
	evalResult := browserCommandResult(ctx, browser, "eval", []string{"eval", dismissOverlaysScript})
	escResult := browserCommandResult(ctx, browser, "press", []string{"press", "Escape"})
	summary := "dismiss_overlays: clicked candidates=" + strings.TrimSpace(evalResult.Output) + "; pressed Escape"
	if escResult.Status != StatusOK {
		summary += " (Escape failed: " + escResult.Output + ")"
	}
	return Result{
		Status:  StatusOK,
		Output:  summary + "\n\nCall browser_snapshot to confirm the overlay is gone before retrying your original action.",
		Display: Display{Summary: "dismiss_overlays completed", Kind: "browser"},
	}
}

// pickElementScript highlights whatever the user hovers and, on their next
// click, reports a CSS selector for that element instead of performing the
// click. It times out after 10s so a user who never clicks doesn't hang the
// turn forever.
const pickElementScript = `(() => {
  return new Promise((resolve) => {
    const outline = '2px solid #ff3366';
    let hovered = null;
    const cssSelector = (el) => {
      if (el.id) return '#' + el.id;
      const parts = [];
      let node = el;
      while (node && node.nodeType === 1 && parts.length < 5) {
        let part = node.tagName.toLowerCase();
        if (node.classList.length) part += '.' + Array.from(node.classList).slice(0, 2).join('.');
        parts.unshift(part);
        node = node.parentElement;
      }
      return parts.join(' > ');
    };
    const cleanup = () => {
      document.removeEventListener('mouseover', onOver, true);
      document.removeEventListener('click', onClick, true);
      if (hovered) hovered.style.outline = hovered.__sonutPrevOutline || '';
      clearTimeout(timer);
    };
    const onOver = (e) => {
      if (hovered) hovered.style.outline = hovered.__sonutPrevOutline || '';
      hovered = e.target;
      hovered.__sonutPrevOutline = hovered.style.outline;
      hovered.style.outline = outline;
    };
    const onClick = (e) => {
      e.preventDefault();
      e.stopPropagation();
      const el = e.target;
      const label = (el.getAttribute('aria-label') || el.textContent || '').trim().slice(0, 60);
      const selector = cssSelector(el);
      cleanup();
      resolve(JSON.stringify({ selector, label, tag: el.tagName.toLowerCase() }));
    };
    document.addEventListener('mouseover', onOver, true);
    document.addEventListener('click', onClick, true);
    const timer = setTimeout(() => {
      cleanup();
      resolve(JSON.stringify({ timeout: true }));
    }, 10000);
  });
})()`

// pickElement hands control to the user in headful mode: it highlights
// whatever they hover and reports a selector for whatever they click, so the
// agent gets a grounded target instead of guessing a third time after two
// locate failures.
func pickElement(ctx context.Context, browser localcontrol.Browser) Result {
	if !localcontrol.BrowserHeadful() {
		return errorResult("Error: pick_element requires headful mode. Run /browser show first so the user can see and click the page, then retry.")
	}
	evalResult := browserCommandResultTimeout(ctx, browser, "eval", []string{"eval", pickElementScript}, browserPickElementTimeout)
	if evalResult.Status != StatusOK {
		return evalResult
	}
	output := strings.TrimSpace(evalResult.Output)
	if strings.Contains(output, `"timeout":true`) {
		return Result{
			Status:  StatusOK,
			Output:  "pick_element timed out after 10s waiting for the user to click an element. Continuing autonomously; use browser_snapshot to choose a grounded target.",
			Display: Display{Summary: "pick_element timed out", Kind: "browser"},
		}
	}
	return Result{
		Status:  StatusOK,
		Output:  "user selected: " + output,
		Display: Display{Summary: "pick_element completed", Kind: "browser"},
	}
}

type browserActionSpec struct {
	argv []string
	min  int
	max  int
}

var browserActionSpecs = map[string]browserActionSpec{
	"back":                 {argv: []string{"back"}, min: 0, max: 0},
	"forward":              {argv: []string{"forward"}, min: 0, max: 0},
	"reload":               {argv: []string{"reload"}, min: 0, max: 0},
	"close":                {argv: []string{"close"}, min: 0, max: 0},
	"connect":              {argv: []string{"connect"}, min: 1, max: 1},
	"click":                {argv: []string{"click"}, min: 1, max: 1},
	"dblclick":             {argv: []string{"dblclick"}, min: 1, max: 1},
	"fill":                 {argv: []string{"fill"}, min: 2, max: 2},
	"type":                 {argv: []string{"type"}, min: 2, max: 2},
	"press":                {argv: []string{"press"}, min: 1, max: 1},
	"keyboard_type":        {argv: []string{"keyboard", "type"}, min: 1, max: 1},
	"keyboard_insert_text": {argv: []string{"keyboard", "inserttext"}, min: 1, max: 1},
	"hover":                {argv: []string{"hover"}, min: 1, max: 1},
	"check":                {argv: []string{"check"}, min: 1, max: 1},
	"uncheck":              {argv: []string{"uncheck"}, min: 1, max: 1},
	"select":               {argv: []string{"select"}, min: 2, max: 2},
	"screenshot":           {argv: []string{"screenshot"}, min: 0, max: 0},
	"scroll":               {argv: []string{"scroll"}, min: 2, max: 2},
	"scroll_into_view":     {argv: []string{"scrollintoview"}, min: 1, max: 1},
	"drag":                 {argv: []string{"drag"}, min: 2, max: 2},
	"wait":                 {argv: []string{"wait"}, min: 1, max: 3},
	"get_text":             {argv: []string{"get", "text"}, min: 1, max: 1},
	"get_html":             {argv: []string{"get", "html"}, min: 1, max: 1},
	"get_value":            {argv: []string{"get", "value"}, min: 1, max: 1},
	"get_attr":             {argv: []string{"get", "attr"}, min: 2, max: 2},
	"get_title":            {argv: []string{"get", "title"}, min: 0, max: 0},
	"get_url":              {argv: []string{"get", "url"}, min: 0, max: 0},
	"tab":                  {argv: []string{"tab"}, min: 0, max: 3},
	"eval":                 {argv: []string{"eval"}, min: 1, max: 1},
	"dialog_accept":        {argv: []string{"dialog", "accept"}, min: 0, max: 1},
	"dialog_dismiss":       {argv: []string{"dialog", "dismiss"}, min: 0, max: 0},
	"dismiss_overlays":     {argv: nil, min: 0, max: 0},
	"pick_element":         {argv: nil, min: 0, max: 0},
}

func browserActionCommandNames() []string {
	names := make([]string, 0, len(browserActionSpecs))
	for name := range browserActionSpecs {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

func browserActionArgs(args map[string]any) (string, []string, error) {
	command, err := stringArg(args, "command", "", true)
	if err != nil {
		return "", nil, err
	}
	command = strings.ToLower(strings.TrimSpace(command))
	spec, ok := browserActionSpecs[command]
	if !ok {
		return "", nil, fmt.Errorf("command must be one of: %s", strings.Join(browserActionCommandNames(), ", "))
	}
	values, err := stringArrayArg(args, "args")
	if err != nil {
		return "", nil, err
	}
	if len(values) < spec.min || len(values) > spec.max {
		if spec.min == spec.max {
			return "", nil, fmt.Errorf("%s requires %d args", command, spec.min)
		}
		return "", nil, fmt.Errorf("%s requires between %d and %d args", command, spec.min, spec.max)
	}
	commandArgs, err := browserActionCommandArgs(command, spec, values)
	if err != nil {
		return "", nil, err
	}
	return command, commandArgs, nil
}

func browserActionCommandArgs(command string, spec browserActionSpec, values []string) ([]string, error) {
	switch command {
	case "connect":
		target, err := browserConnectTargetFromString(values[0])
		if err != nil {
			return nil, err
		}
		commandArgs := append([]string{}, spec.argv...)
		return append(commandArgs, target), nil
	case "click", "dblclick", "hover", "check", "uncheck", "scroll_into_view", "get_text", "get_html", "get_value":
		ref, err := browserRefFromString(values[0])
		if err != nil {
			return nil, err
		}
		commandArgs := append([]string{}, spec.argv...)
		return append(commandArgs, ref), nil
	case "fill", "type", "select":
		ref, err := browserRefFromString(values[0])
		if err != nil {
			return nil, err
		}
		commandArgs := append([]string{}, spec.argv...)
		commandArgs = append(commandArgs, ref)
		commandArgs = append(commandArgs, values[1:]...)
		return commandArgs, nil
	case "drag":
		fromRef, err := browserRefFromString(values[0])
		if err != nil {
			return nil, err
		}
		toRef, err := browserRefFromString(values[1])
		if err != nil {
			return nil, err
		}
		commandArgs := append([]string{}, spec.argv...)
		return append(commandArgs, fromRef, toRef), nil
	case "press":
		key, err := browserKeyFromString(values[0])
		if err != nil {
			return nil, err
		}
		commandArgs := append([]string{}, spec.argv...)
		return append(commandArgs, key), nil
	case "get_attr":
		ref, err := browserRefFromString(values[0])
		if err != nil {
			return nil, err
		}
		attr, err := browserSingleToken("attr", values[1])
		if err != nil {
			return nil, err
		}
		commandArgs := append([]string{}, spec.argv...)
		return append(commandArgs, ref, attr), nil
	case "scroll":
		x, err := browserIntegerToken("x", values[0])
		if err != nil {
			return nil, err
		}
		y, err := browserIntegerToken("y", values[1])
		if err != nil {
			return nil, err
		}
		commandArgs := append([]string{}, spec.argv...)
		return append(commandArgs, x, y), nil
	case "tab":
		commandArgs := append([]string{}, spec.argv...)
		for _, value := range values {
			arg, err := browserSingleToken("tab argument", value)
			if err != nil {
				return nil, err
			}
			commandArgs = append(commandArgs, arg)
		}
		return commandArgs, nil
	default:
		// eval scripts legitimately begin with any character (e.g. "-1" or a
		// leading unary minus); every other command's args must not look like
		// helper CLI flags, the same guard browserRefFromString applies to refs.
		if command != "eval" {
			for _, value := range values {
				if strings.HasPrefix(strings.TrimSpace(value), "-") {
					return nil, fmt.Errorf("%s arguments must not be an option", command)
				}
			}
		}
		commandArgs := append([]string{}, spec.argv...)
		commandArgs = append(commandArgs, values...)
		return commandArgs, nil
	}
}

func browserOpenURLArg(args map[string]any) (string, error) {
	return browserOpenURLArgWithCwd(args, "")
}

func browserOpenURLArgWithCwd(args map[string]any, cwd string) (string, error) {
	rawURL, err := stringArg(args, "url", "", true)
	if err != nil {
		return "", err
	}
	normalized := strings.TrimSpace(rawURL)

	// Treat bare file paths that point to HTML files as file:// URLs so the model
	// can open locally generated HTML files directly (e.g. "./index.html").
	// Absolute file:// URLs are only allowed when the path ends in .html/.htm to
	// prevent reading arbitrary system files via the browser.
	if !strings.Contains(normalized, "://") && isHTMLFilePath(normalized) {
		abs := normalized
		if !filepath.IsAbs(abs) {
			base := strings.TrimSpace(cwd)
			if base == "" {
				base, _ = os.Getwd()
			}
			if base != "" {
				abs = filepath.Join(base, abs)
			}
		}
		if _, statErr := os.Stat(abs); statErr != nil {
			return "", fmt.Errorf("file not found: %s", abs)
		}
		return (&url.URL{Scheme: "file", Path: abs}).String(), nil
	}

	if !strings.Contains(normalized, "://") {
		normalized = "https://" + normalized
	}
	parsed, err := url.Parse(normalized)
	if err != nil {
		return "", err
	}
	if parsed.Scheme == "file" {
		if !isHTMLFilePath(parsed.Path) {
			return "", fmt.Errorf("file:// URLs must point to an .html or .htm file")
		}
		return parsed.String(), nil
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return "", fmt.Errorf("url must use http or https")
	}
	if strings.TrimSpace(parsed.Host) == "" {
		return "", fmt.Errorf("url must include a host")
	}
	return parsed.String(), nil
}

func isHTMLFilePath(path string) bool {
	low := strings.ToLower(strings.TrimSpace(path))
	return strings.HasSuffix(low, ".html") || strings.HasSuffix(low, ".htm")
}

func browserScreenshotResult(ctx context.Context, browser localcontrol.Browser) Result {
	temporary, err := os.CreateTemp("", "sonut-browser-screenshot-*.png")
	if err != nil {
		return errorResult("Error: could not prepare browser screenshot: " + err.Error())
	}
	path := temporary.Name()
	if closeErr := temporary.Close(); closeErr != nil {
		_ = os.Remove(path)
		return errorResult("Error: could not prepare browser screenshot: " + closeErr.Error())
	}
	defer os.Remove(path)
	result := browserCommandResult(ctx, browser, "screenshot", []string{"screenshot", path})
	if result.Status != StatusOK {
		return result
	}
	data, readErr := os.ReadFile(path)
	if readErr != nil {
		result.Status = StatusError
		result.Output += "\nError: browser screenshot was not readable: " + readErr.Error()
		return result
	}
	result.Images = []sonutruntime.ImageBlock{{MediaType: "image/png", Data: data}}
	if result.Meta == nil {
		result.Meta = map[string]string{}
	}
	result.Meta["screenshot_attached"] = "true"
	return result
}

func browserConnectTargetArg(args map[string]any) (string, error) {
	target, err := stringArg(args, "target", "", true)
	if err != nil {
		return "", err
	}
	return browserConnectTargetFromString(target)
}

func browserConnectTargetFromString(target string) (string, error) {
	target = strings.TrimSpace(target)
	if target == "" {
		return "", fmt.Errorf("target is required")
	}
	if strings.ContainsAny(target, " \t\r\n;") {
		return "", fmt.Errorf("target must not contain whitespace or semicolons")
	}
	if strings.HasPrefix(target, "-") {
		return "", fmt.Errorf("target must be a CDP port or URL, not an option")
	}
	if port, err := strconv.Atoi(target); err == nil && port >= 1 && port <= 65535 {
		return target, nil
	}
	host, err := browserConnectTargetHost(target)
	if err != nil {
		return "", err
	}
	if !isLoopbackHost(host) {
		return "", fmt.Errorf("target must be a bare port or loopback host")
	}
	return target, nil
}

func browserConnectTargetHost(target string) (string, error) {
	parsed, err := url.Parse(target)
	if err == nil && parsed.Host != "" {
		if !validBrowserPort(parsed.Port()) {
			return "", fmt.Errorf("target URL must include a valid port")
		}
		return parsed.Hostname(), nil
	}
	if !strings.Contains(target, "://") {
		parsed, err = url.Parse("http://" + target)
		if err == nil && parsed.Host != "" {
			if !validBrowserPort(parsed.Port()) {
				return "", fmt.Errorf("target must include a valid port")
			}
			return parsed.Hostname(), nil
		}
	}
	return "", fmt.Errorf("target must be a bare port or loopback host:port/URL")
}

func validBrowserPort(raw string) bool {
	if raw == "" {
		return false
	}
	port, err := strconv.Atoi(raw)
	return err == nil && port >= 1 && port <= 65535
}

func isLoopbackHost(host string) bool {
	host = strings.Trim(strings.ToLower(strings.TrimSpace(host)), "[]")
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

func browserRefArg(args map[string]any) (string, error) {
	ref, err := stringArg(args, "ref", "", true)
	if err != nil {
		return "", err
	}
	return browserRefFromString(ref)
}

func browserRefFromString(ref string) (string, error) {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return "", fmt.Errorf("ref is required")
	}
	if strings.ContainsAny(ref, " \t\r\n;") {
		return "", fmt.Errorf("ref must be a single browser_snapshot ref")
	}
	if strings.HasPrefix(ref, "-") {
		return "", fmt.Errorf("ref must be a browser_snapshot ref, not an option")
	}
	return ref, nil
}

func browserTypeArgs(args map[string]any) (string, string, error) {
	ref, err := browserRefArg(args)
	if err != nil {
		return "", "", err
	}
	text, err := stringArg(args, "text", "", true)
	if err != nil {
		return "", "", err
	}
	if text == "" {
		return "", "", fmt.Errorf("text must be a non-empty string")
	}
	return ref, text, nil
}

func browserKeyArg(args map[string]any) (string, error) {
	key, err := stringArg(args, "key", "", true)
	if err != nil {
		return "", err
	}
	return browserKeyFromString(key)
}

func browserKeyFromString(key string) (string, error) {
	key = strings.TrimSpace(key)
	if key == "" {
		return "", fmt.Errorf("key is required")
	}
	if strings.ContainsAny(key, " \t\r\n;") {
		return "", fmt.Errorf("key must be a single key name")
	}
	if strings.HasPrefix(key, "-") {
		return "", fmt.Errorf("key must be a key name, not an option")
	}
	return key, nil
}

func browserSingleToken(name string, value string) (string, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return "", fmt.Errorf("%s is required", name)
	}
	if strings.ContainsAny(value, " \t\r\n;") {
		return "", fmt.Errorf("%s must be a single token", name)
	}
	if strings.HasPrefix(value, "-") {
		return "", fmt.Errorf("%s must not be an option", name)
	}
	return value, nil
}

func browserIntegerToken(name string, value string) (string, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return "", fmt.Errorf("%s is required", name)
	}
	if _, err := strconv.Atoi(value); err != nil {
		return "", fmt.Errorf("%s must be an integer", name)
	}
	return value, nil
}

func validateBrowserSnapshotSelector(selector string) error {
	if selector == "" {
		return nil
	}
	if strings.Contains(selector, "]*") {
		return fmt.Errorf("selector appears invalid: remove the trailing wildcard after the attribute selector")
	}
	if strings.ContainsAny(selector, "\r\n") {
		return fmt.Errorf("selector must be a single line")
	}
	return nil
}

func browserLaunchRequestArg(args map[string]any) (localcontrol.BrowserAppLaunchRequest, error) {
	app, err := stringArg(args, "app", "", true)
	if err != nil {
		return localcontrol.BrowserAppLaunchRequest{}, err
	}
	app = strings.ToLower(strings.TrimSpace(app))
	switch app {
	case "discord":
	default:
		return localcontrol.BrowserAppLaunchRequest{}, fmt.Errorf("app must be one of: discord")
	}
	port, err := intArg(args, "debug_port", localcontrol.DefaultDevToolsPort, 1024, 65535)
	if err != nil {
		return localcontrol.BrowserAppLaunchRequest{}, err
	}
	stopExisting, err := boolArg(args, "stop_existing", true)
	if err != nil {
		return localcontrol.BrowserAppLaunchRequest{}, err
	}
	wait, err := boolArg(args, "wait", true)
	if err != nil {
		return localcontrol.BrowserAppLaunchRequest{}, err
	}
	if _, err := boolArg(args, "connect", true); err != nil {
		return localcontrol.BrowserAppLaunchRequest{}, err
	}
	return localcontrol.BrowserAppLaunchRequest{
		App:          app,
		DebugPort:    port,
		StopExisting: stopExisting,
		Wait:         wait,
	}, nil
}

func stringArrayArg(args map[string]any, key string) ([]string, error) {
	value, ok := args[key]
	if !ok || value == nil {
		return nil, nil
	}
	raw, ok := value.([]any)
	if !ok {
		return nil, fmt.Errorf("%s must be an array of strings", key)
	}
	values := make([]string, 0, len(raw))
	for index, item := range raw {
		text, ok := item.(string)
		if !ok {
			return nil, fmt.Errorf("%s[%d] must be a string", key, index)
		}
		values = append(values, text)
	}
	return values, nil
}

func localControlSafety(enabled bool, sideEffect SideEffect, reason string) Safety {
	if !enabled {
		return Safety{
			SideEffect: sideEffect,
			Permission: PermissionDeny,
			Reason:     "Local control is disabled.",
		}
	}
	return promptSafety(sideEffect, reason)
}

func browserCommandResult(ctx context.Context, browser localcontrol.Browser, command string, args []string) Result {
	result := localControlCommandResult(ctx, browser, "browser", command, args)
	return attachBrowserPreview(ctx, browser, command, result)
}

// browserCommandResultTimeout is like browserCommandResult but overrides the
// helper's default timeout for calls that are known to run longer or must
// finish faster than the default (e.g. browser_install, pick_element's eval).
func browserCommandResultTimeout(ctx context.Context, browser localcontrol.Browser, command string, args []string, timeout time.Duration) Result {
	result := localControlCommandResultTimeout(ctx, browser, "browser", command, args, timeout)
	return attachBrowserPreview(ctx, browser, command, result)
}

// attachBrowserPreview keeps the in-app pane synchronized after every local
// browser operation, not only navigation. Screenshot, install, and connect
// already have their own lifecycle and must not trigger recursive captures.
func attachBrowserPreview(ctx context.Context, browser localcontrol.Browser, command string, result Result) Result {
	if result.Status != StatusOK || !browserPreviewAfterCommand(command) {
		return result
	}
	return attachBrowserPreviewForced(ctx, browser, result)
}

func attachBrowserPreviewForced(ctx context.Context, browser localcontrol.Browser, result Result) Result {
	preview := browserScreenshotResult(ctx, browser)
	if preview.Status != StatusOK || len(preview.Images) == 0 {
		return result
	}
	result.Images = preview.Images
	if result.Meta == nil {
		result.Meta = map[string]string{}
	}
	result.Meta["browser_preview_screenshot"] = "true"
	result.Meta["screenshot_attached"] = "true"
	return result
}

func browserPreviewAfterCommand(command string) bool {
	switch strings.ToLower(strings.TrimSpace(command)) {
	case "", "screenshot", "install", "connect", "close", "scroll":
		return false
	default:
		return true
	}
}

func localControlCommandResult(ctx context.Context, runner localControlRunner, kind string, command string, args []string) Result {
	result, err := runner.Run(ctx, args...)
	return buildLocalControlResult(kind, command, args, result, err)
}

func localControlCommandResultTimeout(ctx context.Context, runner timeoutLocalControlRunner, kind string, command string, args []string, timeout time.Duration) Result {
	result, err := runner.RunWithTimeout(ctx, timeout, args...)
	return buildLocalControlResult(kind, command, args, result, err)
}

func buildLocalControlResult(kind string, command string, args []string, result localcontrol.CommandResult, err error) Result {
	output := result.Output()
	budget := applyOutputBudget(output, localBrowserOutputBudgetBytes, "narrow the local-control query or request a scoped snapshot")
	meta := outputBudgetMeta(budget)
	meta[kind+"_command"] = command
	if result.ExitCode != 0 {
		meta["exit_code"] = strconv.Itoa(result.ExitCode)
	}
	if err != nil {
		// When the helper ran but exited non-zero, its stdout/stderr IS the
		// real error (e.g. "✗ Navigation failed: net::ERR_CONNECTION_REFUSED").
		// Show only that output so users see the meaningful error rather than
		// the Go exec wrapper "exit status 1". Fall back to the wrapper message
		// when the process never started (output is empty).
		helperRanButFailed := result.ExitCode > 0 && strings.TrimSpace(budget.Output) != ""
		var message string
		if helperRanButFailed {
			message = budget.Output
		} else {
			message = "Error running " + kind + " helper: " + err.Error()
			if strings.TrimSpace(budget.Output) != "" {
				message += "\n\n" + budget.Output
			}
		}
		if kind == "browser" && command != "install" && browserInstallHintApplies(err.Error()+"\n"+budget.Output) {
			message += "\n\nIf this is a first-use browser runtime error, call browser_install once, then retry the browser command."
		}
		return Result{
			Status:    StatusError,
			Output:    message,
			Truncated: budget.Truncated,
			Meta:      meta,
			Display: Display{
				Summary: command + " failed",
				Kind:    kind,
			},
		}
	}
	if strings.TrimSpace(budget.Output) == "" {
		budget.Output = kind + " command completed."
	}
	return Result{
		Status:    StatusOK,
		Output:    budget.Output,
		Truncated: budget.Truncated,
		Meta:      meta,
		Display: Display{
			Summary: command + " completed",
			Kind:    kind,
		},
	}
}

// webBrowserTool is a unified browser automation tool matching sonut-code's
// WebBrowserTool interface: one tool with an "action" field covering navigate,
// screenshot, get_text, eval, click, fill, console_logs, resize, and close.
type webBrowserTool struct {
	baseTool
	browser localcontrol.Browser
}

func newWebBrowserTool(options localcontrol.BrowserOptions) Tool {
	return webBrowserTool{
		baseTool: baseTool{
			name: "web_browser",
			description: "Launch and control a real browser for web automation.\n\n" +
				"WORKFLOW (always follow this order):\n" +
				"1. navigate — open a URL\n" +
				"2. snapshot — get the accessibility tree with element refs (e.g. e114); this is your DOM map\n" +
				"3. click/fill — act on elements using the ref from snapshot (NOT a CSS selector)\n" +
				"4. snapshot again — confirm the action had the expected effect\n" +
				"5. get_text — read visible text content\n\n" +
				"For verification: use get_text or snapshot — they return text a text-only model can read.\n" +
				"screenshot is attached to the planner as visual evidence; always pair with snapshot/get_text and interaction.\n\n" +
				"Actions: navigate, snapshot, screenshot, get_text, eval, click, fill, press, console_logs, resize, close.",
			parameters: Schema{
				Type: "object",
				Properties: map[string]PropertySchema{
					"action": {
						Type:        "string",
						Enum:        []string{"navigate", "snapshot", "screenshot", "get_text", "eval", "click", "fill", "press", "console_logs", "resize", "close"},
						Description: "Browser action to perform. Use snapshot before click/fill to get element refs.",
					},
					"url":      {Type: "string", Description: "URL to open (action: navigate)."},
					"selector": {Type: "string", Description: "Element ref from snapshot, e.g. e114 or @e114 (action: click, fill). Always call snapshot first to obtain a valid ref — do NOT invent CSS selectors."},
					"text":     {Type: "string", Description: "Text to type into element (action: fill)."},
					"key":      {Type: "string", Description: "Keyboard key to press on the focused page, e.g. ArrowLeft, ArrowRight, Space, Enter, w (action: press). Use for games and keyboard-driven UIs."},
					"script":   {Type: "string", Description: "JavaScript to evaluate in the page (action: eval)."},
					"width":    {Type: "integer", Description: "Viewport width in px (action: resize)."},
					"height":   {Type: "integer", Description: "Viewport height in px (action: resize)."},
				},
				Required:             []string{"action"},
				AdditionalProperties: false,
			},
			safety: localControlSafety(options.Enabled, SideEffectLocalBrowser, "Controls a local browser session."),
		},
		browser: localcontrol.NewBrowser(options),
	}
}

func (tool webBrowserTool) Run(ctx context.Context, args map[string]any) Result {
	return tool.runWithCwd(ctx, args, "", security.OutboundMetadata{})
}

func (tool webBrowserTool) RunWithOptions(ctx context.Context, args map[string]any, options RunOptions) Result {
	return tool.runWithCwd(ctx, args, options.Cwd, security.OutboundMetadata{SessionID: options.SessionID, ToolCallID: options.ToolCallID})
}

func (tool webBrowserTool) runWithCwd(ctx context.Context, args map[string]any, cwd string, metadata security.OutboundMetadata) Result {
	action, _ := args["action"].(string)
	switch action {
	case "navigate":
		rawURL, _ := args["url"].(string)
		if rawURL == "" {
			return errorResult("Error: web_browser navigate requires a url")
		}
		normalizedURL, urlErr := browserOpenURLArgWithCwd(map[string]any{"url": rawURL}, cwd)
		if urlErr != nil {
			return errorResult("Error: web_browser navigate: " + urlErr.Error())
		}
		if parsed, err := url.Parse(normalizedURL); err == nil && (strings.EqualFold(parsed.Scheme, "http") || strings.EqualFold(parsed.Scheme, "https")) {
			port := 80
			if strings.EqualFold(parsed.Scheme, "https") {
				port = 443
			}
			if parsed.Port() != "" {
				if parsedPort, parseErr := strconv.Atoi(parsed.Port()); parseErr == nil {
					port = parsedPort
				}
			}
			if _, err := security.Default().CheckOutboundWithMetadata("web_browser", parsed.Hostname(), port, strings.ToLower(parsed.Scheme), normalizedURL, false, metadata); err != nil {
				return errorResult("Error: web_browser navigation blocked by security policy: " + err.Error())
			}
		}
		return browserOpenWithPreview(ctx, tool.browser, normalizedURL)
	case "snapshot":
		return browserCommandResult(ctx, tool.browser, "snapshot", []string{"snapshot", "-i"})
	case "screenshot":
		// Take the screenshot then append a DOM snapshot so a text-only model has
		// something actionable — the image file path alone is a dead end.
		screenshotResult := browserScreenshotResult(ctx, tool.browser)
		snapshotResult := browserCommandResult(ctx, tool.browser, "snapshot", []string{"snapshot", "-i"})
		combined := screenshotResult.Output
		if strings.TrimSpace(snapshotResult.Output) != "" {
			combined += "\n\n--- DOM SNAPSHOT (use refs for click/fill) ---\n" + snapshotResult.Output
		}
		screenshotResult.Output = combined
		if snapshotResult.Status == StatusOK {
			screenshotResult.Truncated = screenshotResult.Truncated || snapshotResult.Truncated
		}
		return screenshotResult
	case "get_text":
		return browserCommandResult(ctx, tool.browser, "read", []string{"read"})
	case "eval":
		script, _ := args["script"].(string)
		if script == "" {
			return errorResult("Error: web_browser eval requires a script")
		}
		if _, err := security.Default().CheckOutboundWithMetadata("web_browser", "browser-page", 443, "https", script, false, metadata); err != nil {
			return errorResult("Error: web_browser input blocked by security policy: " + err.Error())
		}
		return browserCommandResult(ctx, tool.browser, "eval", []string{"eval", script})
	case "click":
		sel, _ := args["selector"].(string)
		if sel == "" {
			return errorResult("Error: web_browser click requires a selector (ref from snapshot, e.g. e114). Call snapshot first.")
		}
		// Strip optional @ prefix so both "e114" and "@e114" work.
		ref := strings.TrimPrefix(sel, "@")
		clickResult := browserCommandResult(ctx, tool.browser, "click", []string{"click", ref})
		if clickResult.Status != StatusOK {
			// Append snapshot to help the model find the right element.
			snapshotResult := browserCommandResult(ctx, tool.browser, "snapshot", []string{"snapshot", "-i"})
			if strings.TrimSpace(snapshotResult.Output) != "" {
				clickResult.Output += "\n\nCurrent interactive elements (call snapshot to refresh):\n" + snapshotResult.Output
			}
		}
		return clickResult
	case "fill":
		sel, _ := args["selector"].(string)
		text, _ := args["text"].(string)
		if sel == "" {
			return errorResult("Error: web_browser fill requires a selector (ref from snapshot, e.g. e114). Call snapshot first.")
		}
		if _, err := security.Default().CheckOutboundWithMetadata("web_browser", "browser-page", 443, "https", text, false, metadata); err != nil {
			return errorResult("Error: web_browser form input blocked by security policy: " + err.Error())
		}
		ref := strings.TrimPrefix(sel, "@")
		fillResult := browserCommandResult(ctx, tool.browser, "fill", []string{"fill", ref, text})
		if fillResult.Status != StatusOK {
			snapshotResult := browserCommandResult(ctx, tool.browser, "snapshot", []string{"snapshot", "-i"})
			if strings.TrimSpace(snapshotResult.Output) != "" {
				fillResult.Output += "\n\nCurrent interactive elements (call snapshot to refresh):\n" + snapshotResult.Output
			}
		}
		return fillResult
	case "press":
		rawKey, _ := args["key"].(string)
		if rawKey == "" {
			return errorResult("Error: web_browser press requires a key (e.g. ArrowLeft, Space, Enter).")
		}
		key, keyErr := browserKeyFromString(rawKey)
		if keyErr != nil {
			return errorResult("Error: web_browser press: " + keyErr.Error())
		}
		return browserCommandResult(ctx, tool.browser, "press", []string{"press", key})
	case "console_logs":
		return browserCommandResult(ctx, tool.browser, "console", []string{"console"})
	case "resize":
		w, err := intArg(args, "width", 0, 100, 10000)
		if err != nil {
			return errorResult("Error: web_browser resize: " + err.Error())
		}
		h, err := intArg(args, "height", 0, 100, 10000)
		if err != nil {
			return errorResult("Error: web_browser resize: " + err.Error())
		}
		if w == 0 || h == 0 {
			return errorResult("Error: web_browser resize requires width and height")
		}
		return browserCommandResult(ctx, tool.browser, "set", []string{"set", "viewport", strconv.Itoa(w), strconv.Itoa(h)})
	case "close":
		return browserCommandResult(ctx, tool.browser, "close", []string{"close"})
	default:
		return errorResult("Error: unknown web_browser action: " + action)
	}
}

func browserInstallHintApplies(message string) bool {
	lower := strings.ToLower(message)
	if strings.Contains(lower, "cdp error") {
		return false
	}
	if strings.Contains(lower, "was not found on path") {
		return true
	}
	if strings.Contains(lower, "executable doesn't exist") {
		return true
	}
	if strings.Contains(lower, "browser runtime") {
		return true
	}
	if strings.Contains(lower, "please run") && strings.Contains(lower, "install") && strings.Contains(lower, "browser") {
		return true
	}
	if strings.Contains(lower, "could not find") && (strings.Contains(lower, "chromium") || strings.Contains(lower, "chrome") || strings.Contains(lower, "browser")) {
		return true
	}
	return false
}
