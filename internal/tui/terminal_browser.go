package tui

import (
	"context"
	_ "embed"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/aistatine/sonut/internal/localcontrol"
)

//go:embed browser_idle.html
var terminalBrowserIdleHTML []byte

const terminalBrowserIdleFile = "browser-idle.html"

// These seams keep TUI tests from spawning a terminal split while allowing the
// real app to use the installed terminal-browser CLI.
var openTerminalBrowser = localcontrol.OpenTerminalBrowser
var runTerminalBrowserAction = localcontrol.RunTerminalBrowserAction
var terminalBrowserSupported = localcontrol.TerminalBrowserSupported

type terminalBrowserOpenedMsg struct {
	err    error
	opened bool
}

func terminalBrowserIdlePath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("resolve home directory: %w", err)
	}
	dir := filepath.Join(home, ".atcode")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", fmt.Errorf("create atcode browser directory: %w", err)
	}
	path := filepath.Join(dir, terminalBrowserIdleFile)
	if err := os.WriteFile(path, terminalBrowserIdleHTML, 0o600); err != nil {
		return "", fmt.Errorf("write atcode browser page: %w", err)
	}
	return path, nil
}

func openTerminalBrowserCmd(ctx context.Context) tea.Cmd {
	return func() tea.Msg {
		if !terminalBrowserSupported() {
			return terminalBrowserOpenedMsg{}
		}
		if ctx == nil {
			ctx = context.Background()
		}
		page, err := terminalBrowserIdlePath()
		if err != nil {
			return terminalBrowserOpenedMsg{err: err}
		}
		callCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
		defer cancel()
		err = openTerminalBrowser(callCtx, page, "right", 0.35)
		return terminalBrowserOpenedMsg{err: err, opened: err == nil}
	}
}

func openTerminalBrowserTargetCmd(ctx context.Context, target string) tea.Cmd {
	return func() tea.Msg {
		if !terminalBrowserSupported() {
			return terminalBrowserOpenedMsg{}
		}
		if ctx == nil {
			ctx = context.Background()
		}
		target = strings.TrimSpace(target)
		if target == "" {
			return terminalBrowserOpenedMsg{}
		}
		callCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
		defer cancel()
		err := openTerminalBrowser(callCtx, target, "right", 0.35)
		return terminalBrowserOpenedMsg{err: err, opened: err == nil}
	}
}

func mirrorTerminalBrowserNavigationCmd(ctx context.Context, target string) tea.Cmd {
	target = strings.TrimSpace(target)
	if target == "" {
		return nil
	}
	return func() tea.Msg {
		if !terminalBrowserSupported() {
			return terminalBrowserOpenedMsg{}
		}
		if ctx == nil {
			ctx = context.Background()
		}
		callCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
		defer cancel()
		err := runTerminalBrowserAction(callCtx, "open", target)
		return terminalBrowserOpenedMsg{err: err, opened: err == nil}
	}
}
