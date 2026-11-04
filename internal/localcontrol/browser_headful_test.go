package localcontrol

import "testing"

func TestHeadfulAvailability(t *testing.T) {
	fakeEnv := func(values map[string]string) func(string) string {
		return func(key string) string { return values[key] }
	}

	cases := []struct {
		name   string
		goos   string
		env    map[string]string
		wantOK bool
	}{
		{name: "macos always available", goos: "darwin", env: map[string]string{}, wantOK: true},
		{name: "windows always available", goos: "windows", env: map[string]string{}, wantOK: true},
		{name: "linux with DISPLAY available", goos: "linux", env: map[string]string{"DISPLAY": ":0"}, wantOK: true},
		{name: "linux with WAYLAND_DISPLAY available", goos: "linux", env: map[string]string{"WAYLAND_DISPLAY": "wayland-0"}, wantOK: true},
		{name: "linux without display unavailable", goos: "linux", env: map[string]string{}, wantOK: false},
		{name: "ssh forces headless even on macos", goos: "darwin", env: map[string]string{"SSH_TTY": "/dev/ttys0"}, wantOK: false},
		{name: "ssh connection forces headless on linux with display", goos: "linux", env: map[string]string{"DISPLAY": ":0", "SSH_CONNECTION": "1.2.3.4 22 5.6.7.8 22"}, wantOK: false},
		{name: "CI forces headless", goos: "darwin", env: map[string]string{"CI": "true"}, wantOK: false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ok, reason := headfulAvailability(tc.goos, fakeEnv(tc.env))
			if ok != tc.wantOK {
				t.Fatalf("headfulAvailability(%q, %v) = (%v, %q), want ok=%v", tc.goos, tc.env, ok, reason, tc.wantOK)
			}
			if !ok && reason == "" {
				t.Fatalf("expected a non-empty reason when unavailable")
			}
		})
	}
}

func TestBrowserHeadfulEnvReflectsMode(t *testing.T) {
	// Explicit toggles must win over the desktop default.
	defer SetBrowserHeadful(false)

	SetBrowserHeadful(false)
	if env := browserHeadfulEnv(); env != nil {
		t.Fatalf("expected no extra env in headless mode, got %v", env)
	}

	SetBrowserHeadful(true)
	if !BrowserHeadful() {
		t.Fatalf("expected BrowserHeadful() to report true after SetBrowserHeadful(true)")
	}
	env := browserHeadfulEnv()
	if len(env) != 1 || env[0] != "AGENT_BROWSER_HEADED=1" {
		t.Fatalf("expected headful env var, got %v", env)
	}
}

func TestBrowserHeadfulDefaultWhenDisplayAvailable(t *testing.T) {
	// On macOS/Windows CI without SSH_TTY this is headful; on Linux CI without
	// DISPLAY it is headless. Only assert the env helper follows BrowserHeadful().
	_ = BrowserHeadful()
	if BrowserHeadful() {
		env := browserHeadfulEnv()
		if len(env) != 1 || env[0] != "AGENT_BROWSER_HEADED=1" {
			t.Fatalf("headful mode should set AGENT_BROWSER_HEADED, got %v", env)
		}
	} else {
		if env := browserHeadfulEnv(); env != nil {
			t.Fatalf("headless mode should not set env, got %v", env)
		}
	}
}
