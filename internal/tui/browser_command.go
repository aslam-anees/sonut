package tui

import (
	"context"

	"github.com/aistatine/sonut/internal/localcontrol"
)

// restartBrowserSession is stubbed in tests to avoid exec'ing agent-browser.
var restartBrowserSession = localcontrol.RestartBrowserSession

// handleBrowserCommand implements /browser show|hide, toggling whether the
// automation browser launches with a visible, user-interactive window
// (headful) or headless. Desktop default is headful. It also logs the
// transition into the BROWSER panel's ACTIONS log.
//
// Note: Playwright MCP is started with headful args at process launch
// (see config.PlaywrightMCPArgs). This toggle controls the agent-browser
// helper path; restart @code after changing MCP headful env if you need
// Playwright itself to re-spawn with different flags.
func (m *model) handleBrowserCommand(arg string) string {
	switch arg {
	case "show":
		ok, reason := localcontrol.HeadfulAvailability()
		if !ok {
			m.browserState.addAction("headful request denied: " + reason)
			return "Browser\n" + reason
		}
		localcontrol.SetBrowserHeadful(true)
		m.browserHeadful = true
		if err := restartBrowserSession(context.Background()); err != nil {
			m.browserState.addAction("window shown (headful mode enabled; no running session to restart)")
		} else {
			m.browserState.addAction("window shown (headful mode enabled; browser session restarted)")
		}
		return "Browser\nHeadful mode enabled — Playwright/automation opens a visible window you can watch and interact with (login, captcha, clicks)."
	case "hide":
		localcontrol.SetBrowserHeadful(false)
		m.browserHeadful = false
		if err := restartBrowserSession(context.Background()); err != nil {
			m.browserState.addAction("window hidden (headless mode restored)")
		} else {
			m.browserState.addAction("window hidden (headless mode restored; browser session restarted)")
		}
		return "Browser\nHeadless mode restored for the agent-browser helper — future sessions run without a visible window.\n" +
			"Note: Playwright MCP always runs headful (visible browser) regardless of this setting — that cannot be changed."
	case "":
		state := "headless"
		if m.browserHeadful {
			state = "headful (user-visible · default on desktop)"
		}
		return "Browser\nCurrent mode: " + state + "\nUse /browser show or /browser hide to switch.\n" +
			"Headful = you can see and interact with the automation browser while the agent works."
	default:
		return "Browser\nUsage: /browser show|hide"
	}
}
