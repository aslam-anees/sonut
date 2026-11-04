package tools

import (
	"regexp"
	"strings"
)

type shellRuntime struct {
	GOOS       string
	Executable string
	Syntax     string
}

type shellIssue struct {
	Kind       string
	Message    string
	Suggestion string
}

var (
	windowsBashStyleCDPattern = regexp.MustCompile(`(?i)(^|[&|;]\s*)cd\s+/(?:[a-ce-z0-9_./~-]|d[a-z0-9_./~-])[a-z0-9_./~-]*`)
	windowsLSCommandPattern   = regexp.MustCompile(`(?i)(^|[&|;]\s*)ls\b(?:\s+|$)`)
)

func detectShellRuntime(goos string) shellRuntime {
	if goos == "windows" {
		return shellRuntime{GOOS: goos, Executable: "cmd.exe", Syntax: "Windows cmd.exe"}
	}
	return shellRuntime{GOOS: goos, Executable: "/bin/bash", Syntax: "/bin/bash"}
}

func shellGuidanceForGOOS(goos string) string {
	runtime := detectShellRuntime(goos)
	if goos == "windows" {
		return "Uses " + runtime.Syntax + " syntax on Windows; prefer cwd over cd when changing directories."
	}
	guidance := "Uses " + runtime.Syntax + " syntax (supports bash features like process substitution, arrays, and [[)."
	if goos == "darwin" {
		// `ps` is setuid root and cannot run under the macOS sandbox; `pgrep` needs a
		// blocked system service. Point the model at the tools that DO work so it
		// doesn't waste turns: lsof to find a process, kill to stop it.
		guidance += " To find or stop a process, use `lsof -i :PORT` (or `lsof -nP -iTCP -sTCP:LISTEN`) for the PID then `kill <pid>`; `ps` and `pgrep` do not work under the sandbox."
	}
	return guidance
}

func detectShellCommandIssue(command string, goos string) *shellIssue {
	if goos != "windows" {
		return nil
	}
	trimmed := strings.TrimSpace(command)
	if windowsBashStyleCDPattern.MatchString(trimmed) ||
		windowsLSCommandPattern.MatchString(trimmed) {
		return &shellIssue{
			Kind:       "windows_shell_syntax",
			Message:    "Command looks like POSIX/Bash syntax, but @code runs bash tool commands through Windows cmd.exe on this host.",
			Suggestion: "Use the cwd argument instead of cd, use Windows cmd.exe syntax, or use native tools such as list_directory, read_file, grep, and glob.",
		}
	}
	return nil
}

var backgroundSuffixPattern = regexp.MustCompile(`(?:^|[^&])&\s*$`)

// detectBashBackgroundIssue flags commands that try to background a process
// with a trailing `&` (or nohup/disown) through the one-shot bash tool. The
// bash tool kills its whole process group when the command returns (see
// hardenProcessLifetime), so the backgrounded child dies immediately and the
// model sees a confusing "no output" + exit -1. Redirect it to exec_command,
// whose sessions stay alive across turns and are tracked for cleanup.
func detectBashBackgroundIssue(command string) *shellIssue {
	trimmed := strings.TrimSpace(command)
	backgrounded := backgroundSuffixPattern.MatchString(trimmed) ||
		strings.HasPrefix(trimmed, "nohup ") ||
		strings.Contains(trimmed, " nohup ") ||
		strings.Contains(trimmed, "disown")
	if !backgrounded {
		return nil
	}
	return &shellIssue{
		Kind:       "background_process",
		Message:    "This command backgrounds a process (& / nohup), but the bash tool kills all child processes when the command returns — the backgrounded process would die immediately with no output.",
		Suggestion: "Use the exec_command tool instead: run the same command WITHOUT the trailing '&' (e.g. cmd: \"python3 -m http.server 8080\"). exec_command keeps the process running in a persistent session after returning its initial output, so servers and watchers stay alive across turns.",
	}
}

func detectShellOutputIssue(command string, output string, goos string) *shellIssue {
	if goos != "windows" {
		return nil
	}
	haystack := strings.ToLower(command + "\n" + output)
	if strings.Contains(haystack, "the syntax of the command is incorrect") ||
		strings.Contains(haystack, "is not recognized as an internal or external command") {
		return &shellIssue{
			Kind:       "windows_shell_syntax",
			Message:    "Windows cmd.exe rejected the command syntax.",
			Suggestion: "Translate the command to Windows cmd.exe syntax, set the bash tool cwd argument instead of running cd, or prefer native @code tools for file inspection.",
		}
	}
	return nil
}

func appendShellIssueHint(output string, issue shellIssue) string {
	output = strings.TrimRight(output, "\r\n")
	hint := "[@code] shell issue: " + issue.Message
	if strings.TrimSpace(issue.Suggestion) != "" {
		hint += "\nSuggestion: " + issue.Suggestion
	}
	if output == "" {
		return hint
	}
	return output + "\n" + hint
}
