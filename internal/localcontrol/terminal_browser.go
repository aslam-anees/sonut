package localcontrol

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
)

const DefaultTerminalBrowserDriver = "terminal-browser"

// TerminalBrowserSupported reports whether the official terminal-browser
// distribution can run on this host. Unsupported hosts keep using @code's
// existing browser tools; they should not attempt to launch the optional
// terminal split.
func TerminalBrowserSupported() bool {
	return runtime.GOOS == "darwin" && runtime.GOARCH == "arm64"
}

// ResolveTerminalBrowser finds the terminal-browser CLI. The installer places
// it in ~/.local/bin, which is not always present in the PATH inherited by a
// long-running terminal app, so include that canonical install location.
func ResolveTerminalBrowser() (string, error) {
	if path, err := exec.LookPath(DefaultTerminalBrowserDriver); err == nil {
		return path, nil
	}
	if home, err := os.UserHomeDir(); err == nil && home != "" {
		path := filepath.Join(home, ".local", "bin", DefaultTerminalBrowserDriver)
		if info, statErr := os.Stat(path); statErr == nil && !info.IsDir() {
			return path, nil
		}
	}
	return "", fmt.Errorf("%s was not found; install it from https://terminal-browser.com/", DefaultTerminalBrowserDriver)
}

// OpenTerminalBrowser opens url in a real terminal-browser pane. It inherits
// stdin so terminal-browser can identify the host terminal, while discarding
// command output so a short-lived helper cannot corrupt the TUI's alt screen.
func OpenTerminalBrowser(ctx context.Context, url, split string, size float64) error {
	path, err := ResolveTerminalBrowser()
	if err != nil {
		return err
	}
	if strings.TrimSpace(split) == "" {
		split = "right"
	}
	if size <= 0 {
		size = 0.35
	}
	cmd := exec.CommandContext(ctx, path, "open", url, "--split", split, "--size", strconv.FormatFloat(size, 'f', 2, 64))
	cmd.Stdin = os.Stdin
	cmd.Stdout = io.Discard
	cmd.Stderr = io.Discard
	return cmd.Run()
}

// RunTerminalBrowserAction drives the browser already opened by
// OpenTerminalBrowser. The command and args use the agent-browser-compatible
// syntax exposed by terminal-browser's action subcommand.
func RunTerminalBrowserAction(ctx context.Context, command string, args ...string) error {
	path, err := ResolveTerminalBrowser()
	if err != nil {
		return err
	}
	command = strings.TrimSpace(command)
	if command == "" {
		return fmt.Errorf("terminal-browser action command is required")
	}
	argv := []string{"action", "--", command}
	argv = append(argv, args...)
	cmd := exec.CommandContext(ctx, path, argv...)
	cmd.Stdin = os.Stdin
	cmd.Stdout = io.Discard
	cmd.Stderr = io.Discard
	return cmd.Run()
}
