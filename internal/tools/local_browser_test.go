package tools

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/aistatine/sonut/internal/localcontrol"
)

type fakeBrowserRunner struct {
	args     []string
	calls    [][]string
	stdout   string
	stderr   string
	exitCode int
	err      error
}

type fakeBrowserAppLauncher struct {
	request localcontrol.BrowserAppLaunchRequest
	result  localcontrol.BrowserAppLaunchResult
	err     error
}

func (runner *fakeBrowserRunner) Run(_ context.Context, path string, args []string, _ []string, _ time.Duration) (localcontrol.CommandResult, error) {
	runner.args = append([]string(nil), args...)
	runner.calls = append(runner.calls, append([]string(nil), args...))
	stdout := runner.stdout
	if stdout == "" && runner.stderr == "" && runner.err == nil {
		stdout = "browser ok\n"
	}
	if len(args) >= 2 && args[0] == "screenshot" {
		if err := os.WriteFile(args[len(args)-1], []byte("fake-png"), 0o600); err != nil {
			return localcontrol.CommandResult{Path: path, Args: append([]string(nil), args...)}, err
		}
	}
	return localcontrol.CommandResult{
		Path:     path,
		Args:     append([]string(nil), args...),
		Stdout:   stdout,
		Stderr:   runner.stderr,
		ExitCode: runner.exitCode,
	}, runner.err
}

func (launcher *fakeBrowserAppLauncher) LaunchBrowserApp(_ context.Context, request localcontrol.BrowserAppLaunchRequest) (localcontrol.BrowserAppLaunchResult, error) {
	launcher.request = request
	if launcher.err != nil {
		return localcontrol.BrowserAppLaunchResult{}, launcher.err
	}
	if launcher.result.App == "" {
		launcher.result = localcontrol.BrowserAppLaunchResult{
			App:         request.App,
			PID:         1234,
			DebugPort:   request.DebugPort,
			DevToolsURL: "http://127.0.0.1:9222",
		}
	}
	return launcher.result, nil
}

func localBrowserTestOptions(t *testing.T, runner localcontrol.CommandRunner) localcontrol.BrowserOptions {
	t.Helper()
	helper := filepath.Join(t.TempDir(), "agent-browser")
	if err := os.WriteFile(helper, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatalf("write helper: %v", err)
	}
	return localcontrol.BrowserOptions{
		Enabled:    true,
		HelperPath: helper,
		Runner:     runner,
	}
}

func TestBrowserLaunchRunsSupportedAppAndConnects(t *testing.T) {
	runner := &fakeBrowserRunner{}
	launcher := &fakeBrowserAppLauncher{}
	tool := newBrowserLaunchToolWithLauncher(localBrowserTestOptions(t, runner), launcher)

	result := tool.Run(context.Background(), map[string]any{"app": "discord"})
	if result.Status != StatusOK {
		t.Fatalf("status = %s output = %q", result.Status, result.Output)
	}
	wantRequest := localcontrol.BrowserAppLaunchRequest{
		App:          "discord",
		DebugPort:    localcontrol.DefaultDevToolsPort,
		StopExisting: true,
		Wait:         true,
	}
	if !reflect.DeepEqual(launcher.request, wantRequest) {
		t.Fatalf("launch request = %#v, want %#v", launcher.request, wantRequest)
	}
	if want := []string{"connect", "9222"}; !reflect.DeepEqual(runner.args, want) {
		t.Fatalf("browser args = %#v, want %#v", runner.args, want)
	}
	if !strings.Contains(result.Output, "Launched discord") {
		t.Fatalf("output = %q, want launch summary", result.Output)
	}
	if !strings.Contains(result.Output, "already connected") {
		t.Fatalf("output = %q, want connected handoff", result.Output)
	}
}

func TestBrowserLaunchCanSkipConnect(t *testing.T) {
	runner := &fakeBrowserRunner{}
	launcher := &fakeBrowserAppLauncher{}
	tool := newBrowserLaunchToolWithLauncher(localBrowserTestOptions(t, runner), launcher)

	result := tool.Run(context.Background(), map[string]any{
		"app":           "discord",
		"debug_port":    float64(9333),
		"stop_existing": false,
		"wait":          false,
		"connect":       false,
	})
	if result.Status != StatusOK {
		t.Fatalf("status = %s output = %q", result.Status, result.Output)
	}
	wantRequest := localcontrol.BrowserAppLaunchRequest{
		App:          "discord",
		DebugPort:    9333,
		StopExisting: false,
		Wait:         false,
	}
	if !reflect.DeepEqual(launcher.request, wantRequest) {
		t.Fatalf("launch request = %#v, want %#v", launcher.request, wantRequest)
	}
	if runner.args != nil {
		t.Fatalf("browser args = %#v, want no connect", runner.args)
	}
	if !strings.Contains(result.Output, "Next call browser_connect with target 9333") {
		t.Fatalf("output = %q, want connect hint", result.Output)
	}
}

func TestBrowserLaunchRejectsUnsupportedAppBeforePermission(t *testing.T) {
	tool := newBrowserLaunchToolWithLauncher(localcontrol.BrowserOptions{Enabled: true}, &fakeBrowserAppLauncher{})
	result, rejected := tool.(PrePermissionRejecter).RejectBeforePermission(map[string]any{"app": "telegram"})
	if !rejected || result.Status != StatusError || !strings.Contains(result.Output, "app must be one of") {
		t.Fatalf("reject = (%v, %#v), want app rejection", rejected, result)
	}
}

func TestBrowserOpenNormalizesBareHost(t *testing.T) {
	runner := &fakeBrowserRunner{}
	tool := newBrowserOpenTool(localBrowserTestOptions(t, runner))

	result := tool.Run(context.Background(), map[string]any{"url": "example.com"})
	if result.Status != StatusOK {
		t.Fatalf("status = %s output = %q", result.Status, result.Output)
	}
	if len(runner.calls) < 2 || !reflect.DeepEqual(runner.calls[0], []string{"open", "https://example.com"}) {
		t.Fatalf("calls = %#v, want open followed by optional preview capture", runner.calls)
	}
	if len(result.Images) != 1 || string(result.Images[0].Data) != "fake-png" {
		t.Fatalf("open preview images = %#v, want one attached image", result.Images)
	}
}

func TestBrowserOpenResolvesWorkspaceRelativeHTMLFromRunCwd(t *testing.T) {
	runner := &fakeBrowserRunner{}
	workspace := t.TempDir()
	path := filepath.Join(workspace, "mariogame.html")
	if err := os.WriteFile(path, []byte("<html></html>"), 0o644); err != nil {
		t.Fatalf("write html fixture: %v", err)
	}
	tool := newBrowserOpenTool(localBrowserTestOptions(t, runner))
	result := tool.(interface {
		RunWithOptions(context.Context, map[string]any, RunOptions) Result
	}).RunWithOptions(context.Background(), map[string]any{"url": "mariogame.html"}, RunOptions{Cwd: workspace})
	if result.Status != StatusOK {
		t.Fatalf("status = %s output = %q", result.Status, result.Output)
	}
	if len(runner.calls) < 2 || len(runner.calls[0]) != 2 || runner.calls[0][0] != "open" || !strings.HasPrefix(runner.calls[0][1], "file://") {
		t.Fatalf("calls = %#v, want open file:// workspace path followed by optional preview capture", runner.calls)
	}
	if !strings.Contains(runner.calls[0][1], "mariogame.html") {
		t.Fatalf("calls = %#v, missing workspace file", runner.calls)
	}
}

func TestWebBrowserScreenshotAttachesLoadedPageImage(t *testing.T) {
	runner := &fakeBrowserRunner{}
	tool := newWebBrowserTool(localBrowserTestOptions(t, runner))
	result := tool.Run(context.Background(), map[string]any{"action": "screenshot"})
	if result.Status != StatusOK {
		t.Fatalf("status = %s output = %q", result.Status, result.Output)
	}
	if len(result.Images) != 1 || string(result.Images[0].Data) != "fake-png" {
		t.Fatalf("screenshot images = %#v, want one attached image", result.Images)
	}
	if result.Meta["screenshot_attached"] != "true" {
		t.Fatalf("screenshot metadata = %#v, want screenshot_attached", result.Meta)
	}
}

func TestBrowserClickTypeAndPressRunStructuredCommands(t *testing.T) {
	for _, tc := range []struct {
		name string
		args map[string]any
		want []string
	}{
		{
			name: "click",
			args: map[string]any{"ref": "e114"},
			want: []string{"click", "e114"},
		},
		{
			name: "type",
			args: map[string]any{"ref": "e114", "text": "hey from zero"},
			want: []string{"type", "e114", "hey from zero"},
		},
		{
			name: "press",
			args: map[string]any{"key": "Enter"},
			want: []string{"press", "Enter"},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			runner := &fakeBrowserRunner{}
			var tool Tool
			switch tc.name {
			case "click":
				tool = newBrowserClickTool(localBrowserTestOptions(t, runner))
			case "type":
				tool = newBrowserTypeTool(localBrowserTestOptions(t, runner))
			case "press":
				tool = newBrowserPressTool(localBrowserTestOptions(t, runner))
			}
			result := tool.Run(context.Background(), tc.args)
			if result.Status != StatusOK {
				t.Fatalf("status = %s output = %q", result.Status, result.Output)
			}
			if len(runner.calls) < 2 || !reflect.DeepEqual(runner.calls[0], tc.want) || runner.calls[len(runner.calls)-1][0] != "screenshot" {
				t.Fatalf("calls = %#v, want action %#v followed by screenshot", runner.calls, tc.want)
			}
		})
	}
}

func TestBrowserTypeRejectsMissingRefBeforePermission(t *testing.T) {
	tool := newBrowserTypeTool(localcontrol.BrowserOptions{Enabled: true})
	result, rejected := tool.(PrePermissionRejecter).RejectBeforePermission(map[string]any{"text": "hey from zero"})
	if !rejected || result.Status != StatusError || !strings.Contains(result.Output, "ref is required") {
		t.Fatalf("reject = (%v, %#v), want ref rejection", rejected, result)
	}
}

func TestBrowserInstallRunsHelperInstall(t *testing.T) {
	runner := &fakeBrowserRunner{}
	tool := newBrowserInstallTool(localBrowserTestOptions(t, runner))

	result := tool.Run(context.Background(), map[string]any{"with_deps": true})
	if result.Status != StatusOK {
		t.Fatalf("status = %s output = %q", result.Status, result.Output)
	}
	if want := []string{"install", "--with-deps"}; !reflect.DeepEqual(runner.args, want) {
		t.Fatalf("args = %#v, want %#v", runner.args, want)
	}
}

func TestBrowserInstallUsesLocalBrowserSafety(t *testing.T) {
	tool := newBrowserInstallTool(localcontrol.BrowserOptions{Enabled: true})
	if tool.Safety().SideEffect != SideEffectLocalBrowser {
		t.Fatalf("browser_install side effect = %s, want %s", tool.Safety().SideEffect, SideEffectLocalBrowser)
	}
}

func TestBrowserConnectRunsHelperConnect(t *testing.T) {
	runner := &fakeBrowserRunner{}
	tool := newBrowserConnectTool(localBrowserTestOptions(t, runner))

	result := tool.Run(context.Background(), map[string]any{"target": "9222"})
	if result.Status != StatusOK {
		t.Fatalf("status = %s output = %q", result.Status, result.Output)
	}
	if want := []string{"connect", "9222"}; !reflect.DeepEqual(runner.args, want) {
		t.Fatalf("args = %#v, want %#v", runner.args, want)
	}
}

func TestBrowserConnectAcceptsLoopbackTargetsBeforePermission(t *testing.T) {
	tool := newBrowserConnectTool(localcontrol.BrowserOptions{Enabled: true})
	for _, target := range []string{
		"9222",
		"localhost:9222",
		"127.0.0.1:9222",
		"http://127.0.0.1:9222/json/version",
		"ws://[::1]:9222/devtools/browser/abc",
	} {
		if result, rejected := tool.(PrePermissionRejecter).RejectBeforePermission(map[string]any{"target": target}); rejected {
			t.Fatalf("target %q rejected before permission: %#v", target, result)
		}
	}
}

func TestBrowserConnectRejectsShellLikeTargetsBeforePermission(t *testing.T) {
	tool := newBrowserConnectTool(localcontrol.BrowserOptions{Enabled: true})
	for _, target := range []string{"--version", "9222;rm", "http://127.0.0.1:9222/json list"} {
		result, rejected := tool.(PrePermissionRejecter).RejectBeforePermission(map[string]any{"target": target})
		if !rejected || result.Status != StatusError || !strings.Contains(result.Output, "browser_connect") {
			t.Fatalf("target %q reject = (%v, %#v), want target rejection", target, rejected, result)
		}
	}
}

func TestBrowserConnectRejectsRemoteTargetsBeforePermission(t *testing.T) {
	tool := newBrowserConnectTool(localcontrol.BrowserOptions{Enabled: true})
	for _, target := range []string{
		"example.com:9222",
		"192.168.1.20:9222",
		"https://example.com:9222/json/version",
		"ws://example.com:9222/devtools/browser/abc",
		"localhost:notaport",
	} {
		result, rejected := tool.(PrePermissionRejecter).RejectBeforePermission(map[string]any{"target": target})
		if !rejected || result.Status != StatusError || !strings.Contains(result.Output, "browser_connect") {
			t.Fatalf("target %q reject = (%v, %#v), want target rejection", target, rejected, result)
		}
	}
}

func TestBrowserOpenRejectsNonHTMLFileURL(t *testing.T) {
	tool := newBrowserOpenTool(localcontrol.BrowserOptions{Enabled: true})
	// file:// URLs to non-HTML files (e.g. /etc/passwd) must be rejected.
	result, rejected := tool.(PrePermissionRejecter).RejectBeforePermission(map[string]any{"url": "file:///etc/passwd"})
	if !rejected || result.Status != StatusError || !strings.Contains(result.Output, ".html") {
		t.Fatalf("reject = (%v, %#v), want rejection for non-html file:// URL", rejected, result)
	}
}

func TestBrowserOpenAllowsHTMLFileURL(t *testing.T) {
	tool := newBrowserOpenTool(localcontrol.BrowserOptions{Enabled: true})
	result, rejected := tool.(PrePermissionRejecter).RejectBeforePermission(map[string]any{"url": "file:///tmp/index.html"})
	if rejected {
		t.Fatalf("expected file:///tmp/index.html to be allowed, got rejection: %v", result)
	}
}

func TestBrowserOpenURLArgEscapesSpacesInFilePath(t *testing.T) {
	dir := t.TempDir()
	path := dir + "/my file.html"
	if err := os.WriteFile(path, []byte("<html></html>"), 0o644); err != nil {
		t.Fatalf("write test file: %v", err)
	}
	got, err := browserOpenURLArg(map[string]any{"url": path})
	if err != nil {
		t.Fatalf("browserOpenURLArg: %v", err)
	}
	if strings.Contains(got, " ") {
		t.Fatalf("expected spaces to be escaped, got %q", got)
	}
	if !strings.Contains(got, "%20") {
		t.Fatalf("expected escaped space (%%20) in file URL, got %q", got)
	}
}

func TestBrowserOpenURLArgRejectsNonexistentFile(t *testing.T) {
	_, err := browserOpenURLArg(map[string]any{"url": "/nonexistent/path/that/does/not/exist.html"})
	if err == nil {
		t.Fatalf("expected error for nonexistent file, got nil")
	}
	if !strings.Contains(err.Error(), "not found") {
		t.Fatalf("expected a clear not-found error, got %q", err.Error())
	}
}

func TestBrowserSnapshotBuildsStructuredArgs(t *testing.T) {
	runner := &fakeBrowserRunner{}
	tool := newBrowserSnapshotTool(localBrowserTestOptions(t, runner))

	result := tool.Run(context.Background(), map[string]any{
		"compact":  true,
		"depth":    float64(3),
		"selector": "#app",
	})
	if result.Status != StatusOK {
		t.Fatalf("status = %s output = %q", result.Status, result.Output)
	}
	want := []string{"snapshot", "-i", "-c", "-d", "3", "-s", "#app"}
	if len(runner.calls) < 2 || !reflect.DeepEqual(runner.calls[0], want) || runner.calls[len(runner.calls)-1][0] != "screenshot" {
		t.Fatalf("calls = %#v, want snapshot %#v followed by screenshot", runner.calls, want)
	}
}

func TestBrowserSnapshotRejectsObviousInvalidSelectorBeforePermission(t *testing.T) {
	tool := newBrowserSnapshotTool(localcontrol.BrowserOptions{Enabled: true})
	result, rejected := tool.(PrePermissionRejecter).RejectBeforePermission(map[string]any{
		"selector": `main[aria-label*="channel"] [role="textbox"]*`,
	})
	if !rejected || result.Status != StatusError || !strings.Contains(result.Output, "selector appears invalid") {
		t.Fatalf("reject = (%v, %#v), want selector rejection", rejected, result)
	}
}

func TestBrowserActionMapsAllowedCommand(t *testing.T) {
	runner := &fakeBrowserRunner{}
	tool := newBrowserActionTool(localBrowserTestOptions(t, runner))

	result := tool.Run(context.Background(), map[string]any{
		"command": "keyboard_insert_text",
		"args":    []any{"hello"},
	})
	if result.Status != StatusOK {
		t.Fatalf("status = %s output = %q", result.Status, result.Output)
	}
	want := []string{"keyboard", "inserttext", "hello"}
	if len(runner.calls) < 2 || !reflect.DeepEqual(runner.calls[0], want) || runner.calls[len(runner.calls)-1][0] != "screenshot" {
		t.Fatalf("calls = %#v, want action %#v followed by screenshot", runner.calls, want)
	}
}

func TestBrowserActionConnectAcceptsLoopbackTarget(t *testing.T) {
	runner := &fakeBrowserRunner{}
	tool := newBrowserActionTool(localBrowserTestOptions(t, runner))

	result := tool.Run(context.Background(), map[string]any{
		"command": "connect",
		"args":    []any{"127.0.0.1:9222"},
	})
	if result.Status != StatusOK {
		t.Fatalf("status = %s output = %q", result.Status, result.Output)
	}
	want := []string{"connect", "127.0.0.1:9222"}
	if len(runner.calls) < 2 || !reflect.DeepEqual(runner.calls[0], want) || runner.calls[len(runner.calls)-1][0] != "screenshot" {
		t.Fatalf("calls = %#v, want scroll %#v followed by screenshot", runner.calls, want)
	}
}

func TestBrowserActionScrollAcceptsSignedIntegerDeltas(t *testing.T) {
	runner := &fakeBrowserRunner{}
	tool := newBrowserActionTool(localBrowserTestOptions(t, runner))

	result := tool.Run(context.Background(), map[string]any{
		"command": "scroll",
		"args":    []any{"0", "-500"},
	})
	if result.Status != StatusOK {
		t.Fatalf("status = %s output = %q", result.Status, result.Output)
	}
	want := []string{"scroll", "0", "-500"}
	if !reflect.DeepEqual(runner.args, want) {
		t.Fatalf("args = %#v, want %#v", runner.args, want)
	}
}

func TestBrowserActionRejectsUnknownCommandBeforePermission(t *testing.T) {
	tool := newBrowserActionTool(localcontrol.BrowserOptions{Enabled: true})
	result, rejected := tool.(PrePermissionRejecter).RejectBeforePermission(map[string]any{
		"command": "shell",
	})
	if !rejected || result.Status != StatusError || !strings.Contains(result.Output, "command must be one of") {
		t.Fatalf("reject = (%v, %#v), want command rejection", rejected, result)
	}
}

func TestBrowserActionRejectsInvalidStructuredArgsBeforePermission(t *testing.T) {
	tool := newBrowserActionTool(localcontrol.BrowserOptions{Enabled: true})
	for _, tc := range []struct {
		name string
		args map[string]any
		want string
	}{
		{
			name: "remote connect",
			args: map[string]any{"command": "connect", "args": []any{"example.com:9222"}},
			want: "loopback",
		},
		{
			name: "click ref with space",
			args: map[string]any{"command": "click", "args": []any{"e 1"}},
			want: "ref",
		},
		{
			name: "type ref option",
			args: map[string]any{"command": "type", "args": []any{"--ref", "hello"}},
			want: "ref",
		},
		{
			name: "press key with space",
			args: map[string]any{"command": "press", "args": []any{"Control L"}},
			want: "key",
		},
		{
			name: "drag target ref with space",
			args: map[string]any{"command": "drag", "args": []any{"e1", "e 2"}},
			want: "ref",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			result, rejected := tool.(PrePermissionRejecter).RejectBeforePermission(tc.args)
			if !rejected || result.Status != StatusError || !strings.Contains(result.Output, tc.want) {
				t.Fatalf("reject = (%v, %#v), want %q", rejected, result, tc.want)
			}
		})
	}
}

func TestBrowserCDPErrorsDoNotSuggestBrowserInstall(t *testing.T) {
	runner := &fakeBrowserRunner{
		stdout:   "",
		stderr:   "✗ CDP error (DOM.describeNode): Object id doesn't reference a Node\n",
		exitCode: 1,
		err:      errors.New("exit status 1"),
	}
	tool := newBrowserSnapshotTool(localBrowserTestOptions(t, runner))

	result := tool.Run(context.Background(), map[string]any{})
	if result.Status != StatusError {
		t.Fatalf("status = %s output = %q", result.Status, result.Output)
	}
	if strings.Contains(result.Output, "browser_install") {
		t.Fatalf("output = %q, should not suggest browser_install for CDP errors", result.Output)
	}
}

func TestMissingBrowserHelperSuggestsBrowserInstall(t *testing.T) {
	browser := localcontrol.NewBrowser(localcontrol.BrowserOptions{
		Enabled: true,
		Driver:  "definitely-missing-agent-browser",
	})

	result := browserCommandResult(context.Background(), browser, "snapshot", []string{"snapshot"})
	if result.Status != StatusError {
		t.Fatalf("status = %s output = %q", result.Status, result.Output)
	}
	if !strings.Contains(result.Output, "browser_install") {
		t.Fatalf("output = %q, want browser_install hint", result.Output)
	}
}

func TestDisabledBrowserToolsAreDenied(t *testing.T) {
	var toolset []Tool
	toolset = append(toolset, NewLocalBrowserTools(localcontrol.BrowserOptions{})...)
	toolset = append(toolset, NewLocalDesktopTools(localcontrol.DesktopOptions{})...)
	toolset = append(toolset, NewLocalTerminalTools(localcontrol.TerminalOptions{})...)
	for _, tool := range toolset {
		if tool.Safety().Permission != PermissionDeny {
			t.Fatalf("%s permission = %s, want deny", tool.Name(), tool.Safety().Permission)
		}
	}
}

func TestPickElementRequiresHeadfulMode(t *testing.T) {
	localcontrol.SetBrowserHeadful(false)
	runner := &fakeBrowserRunner{}
	tool := newBrowserActionTool(localBrowserTestOptions(t, runner))

	result := tool.Run(context.Background(), map[string]any{"command": "pick_element"})
	if result.Status != StatusError {
		t.Fatalf("status = %s, want error when not headful", result.Status)
	}
	if !strings.Contains(result.Output, "/browser show") {
		t.Fatalf("output = %q, want a hint to run /browser show", result.Output)
	}
	if len(runner.calls) != 0 {
		t.Fatalf("expected no browser helper calls, got %v", runner.calls)
	}
}

func TestPickElementReturnsUserSelection(t *testing.T) {
	localcontrol.SetBrowserHeadful(true)
	defer localcontrol.SetBrowserHeadful(false)
	runner := &fakeBrowserRunner{stdout: `{"selector":"button#submit","label":"Submit","tag":"button"}`}
	tool := newBrowserActionTool(localBrowserTestOptions(t, runner))

	result := tool.Run(context.Background(), map[string]any{"command": "pick_element"})
	if result.Status != StatusOK {
		t.Fatalf("status = %s output = %q", result.Status, result.Output)
	}
	if !strings.Contains(result.Output, "user selected:") || !strings.Contains(result.Output, "button#submit") {
		t.Fatalf("output = %q, want it to report the user's selection", result.Output)
	}
}

func TestPickElementTimeoutContinuesAutonomouslyAfterTenSeconds(t *testing.T) {
	localcontrol.SetBrowserHeadful(true)
	defer localcontrol.SetBrowserHeadful(false)
	runner := &fakeBrowserRunner{stdout: `{"timeout":true}`}
	tool := newBrowserActionTool(localBrowserTestOptions(t, runner))

	result := tool.Run(context.Background(), map[string]any{"command": "pick_element"})
	if result.Status != StatusOK {
		t.Fatalf("status = %s output = %q", result.Status, result.Output)
	}
	if !strings.Contains(result.Output, "timed out after 10s") ||
		!strings.Contains(result.Output, "Continuing autonomously") {
		t.Fatalf("output = %q, want bounded autonomous fallback", result.Output)
	}
}

func TestLocateFailureHintMentionsPickElementWhenHeadful(t *testing.T) {
	localcontrol.SetBrowserHeadful(true)
	defer localcontrol.SetBrowserHeadful(false)
	runner := &fakeBrowserRunner{stdout: "could not locate element role=button name=Submit\n", exitCode: 1, err: errors.New("exit status 1")}
	tool := newBrowserClickTool(localBrowserTestOptions(t, runner))

	result := tool.Run(context.Background(), map[string]any{"ref": "e69"})
	if !strings.Contains(result.Output, "pick_element") {
		t.Fatalf("output = %q, want a pick_element hint in headful mode", result.Output)
	}
}
