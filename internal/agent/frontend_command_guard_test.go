package agent

import (
	"context"
	"strings"
	"testing"

	"github.com/aistatine/sonut/internal/sonutruntime"
	"github.com/aistatine/sonut/internal/tools"
)

func TestInvalidFrontendCommandIsRedirectedToBrowser(t *testing.T) {
	call := ToolCall{Name: "exec_command", Arguments: `{"command":"python3 -c \"from game import run\""}`}
	if got := invalidFrontendCommand(call, "make the HTML game work", Options{}, ""); got == "" {
		t.Fatal("expected Python import of an HTML game to be redirected")
	}
}

func TestInvalidFrontendOpenCommandIsRedirectedToBrowser(t *testing.T) {
	call := ToolCall{Name: "exec_command", Arguments: `{"command":"cd /workspace && open game.html"}`}
	if got := invalidFrontendCommand(call, "verify the browser game", Options{}, ""); got == "" {
		t.Fatal("expected file open command to be redirected to headful browser")
	}
}

func TestInvalidFrontendBareOpenHTMLIsRedirectedToBrowser(t *testing.T) {
	call := ToolCall{Name: "exec_command", Arguments: `{"command":"open mariogame.html"}`}
	if got := invalidFrontendCommand(call, "validate the HTML game", Options{}, ""); got == "" {
		t.Fatal("expected bare open of an HTML file to be redirected to headful browser")
	}
}

func TestGamePromptRedirectsHTMLOpenWithoutFrontendKeyword(t *testing.T) {
	call := ToolCall{Name: "exec_command", Arguments: `{"command":"open /workspace/supermario.html"}`}
	if got := invalidFrontendCommand(call, "fix the code in supermario game", Options{}, ""); got == "" {
		t.Fatal("expected an HTML open to be redirected for the shorthand game prompt")
	}
}

func TestHTMLOpenDetectionHandlesCompoundShellCommands(t *testing.T) {
	for _, command := range []string{
		`cd /workspace && open game.html`,
		`serve game.htm; xdg-open game.htm`,
		`echo ready | open game.html`,
	} {
		if !containsBrowserOpenCommand(command) {
			t.Errorf("containsBrowserOpenCommand(%q) = false, want true", command)
		}
	}
}

func TestRunBlocksShorthandGameHTMLOpenBeforeExecution(t *testing.T) {
	tool := &countingExecCommandTool{}
	registry := tools.NewRegistry()
	registry.Register(tool)
	provider := &mockProvider{turns: [][]sonutruntime.StreamEvent{
		toolTurn("open-1", "exec_command", `{"command":"open /workspace/supermario.html"}`),
		textTurn("I will validate the game through browser tooling."),
	}}

	result, err := Run(context.Background(), "fix the code in supermario game", provider, Options{
		Registry:     registry,
		Cwd:          "/workspace",
		RunMode:      RunModeBuilder,
		MaxTurns:     2,
		ProviderName: "openai",
	})
	if err != nil {
		t.Fatal(err)
	}
	if tool.calls != 0 {
		t.Fatalf("HTML GUI opener executed %d times, want zero", tool.calls)
	}
	if len(provider.requests) < 2 {
		t.Fatalf("provider requests = %d, want a recovery turn", len(provider.requests))
	}
	var sawBrowserRedirect bool
	for _, message := range provider.requests[1].Messages {
		if message.Role == sonutruntime.MessageRoleTool && strings.Contains(message.Content, "headful browser/Playwright") {
			sawBrowserRedirect = true
		}
	}
	if !sawBrowserRedirect {
		t.Fatal("recovery turn did not receive the browser-validation redirect")
	}
	if result.FinalAnswer == "" {
		t.Fatal("expected a final recovery answer")
	}
}

func TestFrontendGuardDoesNotBlockPythonTasks(t *testing.T) {
	call := ToolCall{Name: "exec_command", Arguments: `{"command":"python3 -c \"from game import run\""}`}
	if got := invalidFrontendCommand(call, "fix the Python game engine", Options{}, ""); got != "" {
		t.Fatalf("Python task was incorrectly redirected: %q", got)
	}
}

func TestFullFrontendRewritesPreferWriteFile(t *testing.T) {
	if !prefersFullRewrite("make our code look like the attached image", "", "Create index.html and styles.css") {
		t.Fatal("expected visual frontend rebuild to prefer write_file")
	}
	if prefersFullRewrite("change one selector", "", "") {
		t.Fatal("small surgical change should retain edit_file")
	}
}

func TestEmptyAppendFileIsHandledInternally(t *testing.T) {
	call := ToolCall{Name: "append_file", Arguments: `{"path":"mariogame.html","content":""}`}
	if got := emptyAppendFileNotice(call); got == "" {
		t.Fatal("expected empty append_file to be suppressed")
	}
	call.Arguments = `{"path":"mariogame.html","content":"  "}`
	if got := emptyAppendFileNotice(call); got == "" {
		t.Fatal("expected whitespace-only append_file to be suppressed")
	}
	call.Arguments = `{"path":"mariogame.html","content":"next line\n"}`
	if got := emptyAppendFileNotice(call); got != "" {
		t.Fatalf("non-empty append_file was suppressed: %q", got)
	}
}
