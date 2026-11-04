package tui

import tea "charm.land/bubbletea/v2"

func keyCode(msg tea.KeyMsg) rune {
	return msg.Key().Code
}

func keyMatchesCode(msg tea.KeyMsg, code rune) bool {
	key := msg.Key()
	return key.Code == code || key.BaseCode == code
}

func keyMatchesControlByte(msg tea.KeyMsg, code rune) bool {
	if code >= 'A' && code <= 'Z' {
		code += 'a' - 'A'
	}
	if code < 'a' || code > 'z' {
		return false
	}
	controlByte := code - 'a' + 1
	key := msg.Key()
	return key.Code == controlByte || key.BaseCode == controlByte
}

func keyText(msg tea.KeyMsg) string {
	return msg.Key().Text
}

func keyRunes(msg tea.KeyMsg) []rune {
	return []rune(keyText(msg))
}

func keyHasMod(msg tea.KeyMsg, mod tea.KeyMod) bool {
	return msg.Key().Mod.Contains(mod)
}

func keyAlt(msg tea.KeyMsg) bool {
	return keyHasMod(msg, tea.ModAlt)
}

func keyShift(msg tea.KeyMsg) bool {
	return keyHasMod(msg, tea.ModShift)
}

func keyIs(msg tea.KeyMsg, code rune) bool {
	return keyCode(msg) == code
}

func keyCtrl(msg tea.KeyMsg, code rune) bool {
	// Bubble Tea normally preserves the Ctrl modifier. A few terminals and
	// multiplexers deliver the canonical keystroke string instead, so accept
	// both representations; otherwise a global shortcut can fall through to
	// composer editing and appear to do something unrelated.
	return (keyMatchesCode(msg, code) && keyHasMod(msg, tea.ModCtrl)) ||
		keyMatchesControlByte(msg, code) ||
		msg.String() == "ctrl+"+string(code)
}

func keyCtrlArrow(msg tea.KeyMsg, code rune) bool {
	return keyIs(msg, code) && keyHasMod(msg, tea.ModCtrl)
}

func keyPrintable(msg tea.KeyMsg) bool {
	return keyText(msg) != "" && !keyAlt(msg) && !keyHasMod(msg, tea.ModCtrl)
}

func keyBackspace(msg tea.KeyMsg) bool {
	return keyIs(msg, tea.KeyBackspace) || keyCtrl(msg, 'h')
}

func mouseEvent(msg tea.MouseMsg) tea.Mouse {
	return msg.Mouse()
}

func mouseX(msg tea.MouseMsg) int {
	return mouseEvent(msg).X
}

func mouseY(msg tea.MouseMsg) int {
	return mouseEvent(msg).Y
}

func mouseLeftPress(msg tea.MouseMsg) bool {
	event := mouseEvent(msg)
	return event.Button == tea.MouseLeft && isMouseClick(msg)
}

func mouseRightPress(msg tea.MouseMsg) bool {
	event := mouseEvent(msg)
	return event.Button == tea.MouseRight && isMouseClick(msg)
}

func mouseMotion(msg tea.MouseMsg) bool {
	_, ok := msg.(tea.MouseMotionMsg)
	return ok
}

func mouseRelease(msg tea.MouseMsg) bool {
	_, ok := msg.(tea.MouseReleaseMsg)
	return ok
}

func mouseWheelUp(msg tea.MouseMsg) bool {
	event := mouseEvent(msg)
	return event.Button == tea.MouseWheelUp
}

func mouseWheelDown(msg tea.MouseMsg) bool {
	event := mouseEvent(msg)
	return event.Button == tea.MouseWheelDown
}

func isMouseClick(msg tea.MouseMsg) bool {
	_, ok := msg.(tea.MouseClickMsg)
	return ok
}
