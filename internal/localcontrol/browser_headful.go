package localcontrol

import (
	"os"
	"runtime"
	"sync"
	"sync/atomic"
)

var browserHeadfulEnabled atomic.Bool

// headfulDefaultOnce applies the desktop-first default once: visible browser
// when a display is available, headless only on SSH/CI/no-display. Users can
// still flip with /browser show|hide after launch.
var headfulDefaultOnce sync.Once

func ensureHeadfulDefault() {
	headfulDefaultOnce.Do(func() {
		if ok, _ := HeadfulAvailability(); ok {
			browserHeadfulEnabled.Store(true)
		}
	})
}

// SetBrowserHeadful toggles whether subsequent Browser.Run calls launch the
// agent-browser helper in headful (visible, user-interactive) mode instead of
// headless. Desktop sessions default to headful; /browser hide forces headless.
func SetBrowserHeadful(enabled bool) {
	// Mark default applied so a later ensureHeadfulDefault cannot override an
	// explicit user/test choice.
	headfulDefaultOnce.Do(func() {})
	browserHeadfulEnabled.Store(enabled)
}

// BrowserHeadful reports the current headful/headless mode.
// Desktop default is headful so the user can watch and interact.
func BrowserHeadful() bool {
	ensureHeadfulDefault()
	return browserHeadfulEnabled.Load()
}

// browserHeadfulEnv returns the extra environment variables to pass to the
// agent-browser helper for the current mode.
// agent-browser reads AGENT_BROWSER_HEADED (not SONUT_BROWSER_HEADFUL).
func browserHeadfulEnv() []string {
	if !BrowserHeadful() {
		return nil
	}
	return []string{"AGENT_BROWSER_HEADED=1"}
}

// HeadfulAvailability reports whether headful (GUI) browser mode can be used
// in the current environment, and if not, why. SSH sessions and CI runners
// force headless since there is no local display to show the user a window
// on. On Linux a display server is required (DISPLAY or WAYLAND_DISPLAY);
// macOS and Windows are assumed to always have a GUI available.
func HeadfulAvailability() (bool, string) {
	return headfulAvailability(runtime.GOOS, os.Getenv)
}

func headfulAvailability(goos string, getenv func(string) string) (bool, string) {
	if getenv("SSH_TTY") != "" || getenv("SSH_CONNECTION") != "" {
		return false, "headful browser mode is unavailable over SSH (no local display); staying headless"
	}
	if getenv("CI") != "" {
		return false, "headful browser mode is unavailable in CI; staying headless"
	}
	switch goos {
	case "linux":
		if getenv("DISPLAY") == "" && getenv("WAYLAND_DISPLAY") == "" {
			return false, "headful browser mode is unavailable (no display: set DISPLAY or WAYLAND_DISPLAY); staying headless"
		}
	}
	return true, ""
}
