package voice

import (
	"fmt"
	"strings"
	"time"
)

type ShortcutKind string

const (
	ShortcutOff   ShortcutKind = "off"
	ShortcutHold  ShortcutKind = "hold"
	ShortcutChord ShortcutKind = "chord"
)

type Shortcut struct {
	Raw     string
	Kind    ShortcutKind
	Key     string
	HoldFor time.Duration
}

func ParseShortcut(raw string) (Shortcut, error) {
	raw = strings.ToLower(strings.TrimSpace(raw))
	if raw == "" {
		return Shortcut{}, fmt.Errorf("voice shortcut is empty; use hold:space:2s, ctrl+space, alt+v, f8, or off")
	}
	if raw == "off" {
		return Shortcut{Raw: raw, Kind: ShortcutOff}, nil
	}
	if strings.HasPrefix(raw, "hold:") {
		parts := strings.Split(raw, ":")
		if len(parts) != 3 || parts[1] != "space" || parts[2] != "2s" {
			return Shortcut{}, fmt.Errorf("unsupported hold shortcut %q; the supported hold shortcut is hold:space:2s", raw)
		}
		return Shortcut{Raw: raw, Kind: ShortcutHold, Key: "space", HoldFor: 2 * time.Second}, nil
	}
	switch raw {
	case "ctrl+space", "alt+v", "f8":
		return Shortcut{Raw: raw, Kind: ShortcutChord, Key: raw}, nil
	default:
		return Shortcut{}, fmt.Errorf("unsupported voice shortcut %q; use hold:space:2s, ctrl+space, alt+v, f8, or off", raw)
	}
}

type HoldEvent string

const (
	HoldNone     HoldEvent = ""
	HoldArmed    HoldEvent = "armed"
	HoldActivate HoldEvent = "activate"
	HoldInsert   HoldEvent = "insert"
	HoldCancel   HoldEvent = "cancel"
)

// HoldMachine recognizes an actual held/repeated key. A press/release pair
// becomes one ordinary space; activation requires a genuine repeat event (or
// a legacy terminal's repeated press), so pressing Space and waiting is safe.
type HoldMachine struct {
	shortcut      Shortcut
	pressed       bool
	startedAt     time.Time
	activated     bool
	genuineRepeat bool
}

func NewHoldMachine(shortcut Shortcut) *HoldMachine {
	return &HoldMachine{shortcut: shortcut}
}

func (m *HoldMachine) Press(now time.Time, genuineRepeat bool) HoldEvent {
	if m.shortcut.Kind != ShortcutHold {
		return HoldNone
	}
	if !m.pressed {
		m.pressed = true
		m.startedAt = now
		m.genuineRepeat = genuineRepeat
		return HoldArmed
	}
	if genuineRepeat {
		m.genuineRepeat = true
	}
	return m.maybeActivate(now)
}

func (m *HoldMachine) Tick(now time.Time) HoldEvent {
	return m.maybeActivate(now)
}

func (m *HoldMachine) maybeActivate(now time.Time) HoldEvent {
	if !m.pressed || m.activated || !m.genuineRepeat || now.Sub(m.startedAt) < m.shortcut.HoldFor {
		return HoldNone
	}
	m.activated = true
	return HoldActivate
}

func (m *HoldMachine) Release(time.Time) HoldEvent {
	if !m.pressed {
		return HoldNone
	}
	wasActivated := m.activated
	m.pressed = false
	m.startedAt = time.Time{}
	m.genuineRepeat = false
	m.activated = false
	if wasActivated {
		return HoldCancel
	}
	return HoldInsert
}

func (m *HoldMachine) Active() bool    { return m.pressed }
func (m *HoldMachine) Activated() bool { return m.activated }
