package tui

import (
	"github.com/aistatine/sonut/internal/localcontrol"
)

type setupToolCheck struct {
	name   string
	ok     bool
	detail string
}

// checkSetupTools is stubbed in tests; the real check probes the filesystem
// and PATH for local-control helper binaries.
var checkSetupTools = setupToolChecks

var setupToolCheckCache []setupToolCheck

// cachedSetupToolChecks probes once per process; the Ready screen re-renders
// on every keypress and the probe hits the filesystem and PATH.
func cachedSetupToolChecks() []setupToolCheck {
	if setupToolCheckCache == nil {
		setupToolCheckCache = checkSetupTools()
	}
	return setupToolCheckCache
}

// setupToolChecks probes the local-control tool stack so the setup Ready
// screen can show what is working and what still needs installing, instead of
// the user discovering missing helpers mid-task.
func setupToolChecks() []setupToolCheck {
	checks := make([]setupToolCheck, 0, 4)

	if path, err := localcontrol.NewBrowser(localcontrol.BrowserOptions{Enabled: true}).Resolve(); err != nil {
		checks = append(checks, setupToolCheck{name: "browser automation", detail: "missing — npm i -g agent-browser"})
	} else {
		checks = append(checks, setupToolCheck{name: "browser automation", ok: true, detail: shortenPath(path)})
	}

	if ok, _ := localcontrol.HeadfulAvailability(); ok {
		checks = append(checks, setupToolCheck{name: "visible browser window", ok: true, detail: "available — /browser show"})
	} else {
		checks = append(checks, setupToolCheck{name: "visible browser window", detail: "no local display; headless only"})
	}

	if path, err := localcontrol.NewDesktop(localcontrol.DesktopOptions{Enabled: true}).Resolve(); err != nil {
		checks = append(checks, setupToolCheck{name: "desktop control", detail: "missing — install cua-driver (optional)"})
	} else {
		checks = append(checks, setupToolCheck{name: "desktop control", ok: true, detail: shortenPath(path)})
	}

	if path, err := localcontrol.NewTerminal(localcontrol.TerminalOptions{Enabled: true}).Resolve(); err != nil {
		checks = append(checks, setupToolCheck{name: "terminal control", detail: "missing — install tuistory (optional)"})
	} else {
		checks = append(checks, setupToolCheck{name: "terminal control", ok: true, detail: shortenPath(path)})
	}

	return checks
}

func setupToolCheckLines(checks []setupToolCheck) []string {
	if len(checks) == 0 {
		return nil
	}
	lines := []string{"", sonutTheme.ink.Bold(true).Render("Tools"), ""}
	for _, check := range checks {
		marker := sonutTheme.faint.Render("✗")
		if check.ok {
			marker = sonutTheme.accent.Render("✓")
		}
		lines = append(lines, "  "+marker+" "+sonutTheme.ink.Render(check.name)+"  "+sonutTheme.faint.Render(check.detail))
	}
	return lines
}
