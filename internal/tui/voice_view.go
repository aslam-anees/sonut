package tui

import (
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strings"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	voicepkg "github.com/aistatine/sonut/internal/voice"
)

type voiceViewTab int

const (
	voiceViewSettings voiceViewTab = iota
	voiceViewSetup
	voiceViewDevices
	voiceViewDoctor
	voiceViewTabCount
)

var voiceViewTabNames = [...]string{"Settings", "Setup", "Devices", "Doctor"}

var voiceViewLanguages = []string{"auto", "en", "hi", "ar", "es", "fr", "de", "ja", "ko", "zh"}
var voiceViewVoices = []string{"F1", "F2", "F3", "F4", "F5"}
var voiceViewShortcuts = []string{"hold:space:2s", "ctrl+space", "alt+v", "f8", "off"}

const (
	voiceSettingEnabled = iota
	voiceSettingLanguage
	voiceSettingResponseVoice
	voiceSettingSpeed
	voiceSettingShortcut
	voiceSettingInput
	voiceSettingOutput
	voiceSettingCount
)

type voiceViewState struct {
	active             bool
	tab                voiceViewTab
	selected           int
	parentScrollOffset int
	notice             string
}

func (m model) openVoiceView(tab voiceViewTab) model {
	parent := m.chatScrollOffset
	if m.voiceView.active {
		parent = m.voiceView.parentScrollOffset
	}
	m.voiceView = voiceViewState{
		active:             true,
		tab:                clampVoiceViewTab(tab),
		parentScrollOffset: parent,
	}
	m.chatScrollOffset = 0
	m.clearComposer()
	m.clearSuggestions()
	m = m.clearHover()
	return m
}

func (m model) closeVoiceView() model {
	if !m.voiceView.active {
		return m
	}
	m.chatScrollOffset = m.voiceView.parentScrollOffset
	m.voiceView = voiceViewState{}
	m.clearComposer()
	m.clearSuggestions()
	m = m.clearHover()
	return m
}

func clampVoiceViewTab(tab voiceViewTab) voiceViewTab {
	if tab < 0 || tab >= voiceViewTabCount {
		return voiceViewSettings
	}
	return tab
}

func (m model) setVoiceViewTab(tab voiceViewTab) model {
	m.voiceView.tab = clampVoiceViewTab(tab)
	m.voiceView.selected = 0
	m.chatScrollOffset = 0
	return m
}

func (m model) voiceViewNavBar(width int) string {
	left := sonutTheme.accent.Render("◆ Voice") + sonutTheme.faint.Render("  · Esc/q main")
	available := maxInt(0, width-lipgloss.Width(left)-2)
	tabs := make([]string, 0, int(voiceViewTabCount))
	for index, name := range voiceViewTabNames {
		label := sonutTheme.faint.Render(name)
		if voiceViewTab(index) == m.voiceView.tab {
			label = sonutTheme.ink.Bold(true).Render("[" + name + "]")
		}
		tabs = append(tabs, label)
	}
	right := strings.Join(tabs, sonutTheme.line.Render("  "))
	if lipgloss.Width(right) > available {
		right = sonutTheme.faint.Render(fmt.Sprintf("%d/%d %s", int(m.voiceView.tab)+1, voiceViewTabCount, voiceViewTabNames[m.voiceView.tab]))
	}
	return fitStyledLine(joinHeaderLine(left, right, width), width)
}

func (m model) voiceViewBodyItems(width int) []transcriptBodyItem {
	return []transcriptBodyItem{transcriptBlockBodyItem(transcriptBodyItemRow, -1, m.renderVoiceView(width))}
}

func (m model) renderVoiceView(width int) string {
	inner := maxInt(20, width-2)
	var lines []string
	switch m.voiceView.tab {
	case voiceViewSetup:
		lines = m.renderVoiceSetup(inner)
	case voiceViewDevices:
		lines = m.renderVoiceDevices(inner)
	case voiceViewDoctor:
		lines = m.renderVoiceDoctor(inner)
	default:
		lines = m.renderVoiceSettings(inner)
	}
	for index := range lines {
		lines[index] = fitStyledLine(lines[index], inner)
	}
	return strings.Join(lines, "\n")
}

func (m model) renderVoiceSettings(width int) []string {
	if m.voice == nil {
		return []string{"", sonutTheme.amber.Render("Voice is unavailable in this session.")}
	}
	v := m.voice
	enabled := "on"
	if !v.config.Enabled {
		enabled = "off"
	}
	lines := []string{
		"",
		sonutTheme.ink.Bold(true).Render("Local voice") + sonutTheme.faint.Render("  optional, local-first input and spoken responses"),
		voiceViewSection("Status", width, m.voiceStatusLabel()),
		sonutTheme.faint.Render("Voice mode is temporary. Hold Space for 2 seconds; 3 seconds of silence submits the utterance."),
		"",
		voiceViewSection("Settings", width),
		voiceViewSettingLineSelected("Enabled", enabled, "e toggle", m.voiceView.selected == voiceSettingEnabled),
		voiceViewSettingLineSelected("Language", v.config.Language, "l cycle", m.voiceView.selected == voiceSettingLanguage),
		voiceViewSettingLineSelected("Response voice", v.config.ResponseVoice, "v cycle F1–F5", m.voiceView.selected == voiceSettingResponseVoice),
		voiceViewSettingLineSelected("Speaking speed", fmt.Sprintf("%.2fx", v.config.Speed), "- / + adjust", m.voiceView.selected == voiceSettingSpeed),
		voiceViewSettingLineSelected("Shortcut", v.config.Shortcut, "k cycle", m.voiceView.selected == voiceSettingShortcut),
		voiceViewSettingLineSelected("Input", v.config.InputDevice, "d devices", m.voiceView.selected == voiceSettingInput),
		voiceViewSettingLineSelected("Output", v.config.OutputDevice, "d devices", m.voiceView.selected == voiceSettingOutput),
		voiceViewSettingLine("Model profile", v.config.ModelProfile, "light"),
		voiceViewSettingLine("Endpoint silence", fmt.Sprintf("%.1fs", float64(v.config.SilenceMs)/1000), "fixed"),
		"",
		sonutTheme.faint.Render("No microphone is opened until listening starts. Raw audio is not retained by default."),
	}
	if v.err != "" {
		lines = append(lines, "", sonutTheme.amber.Render("Action needed: "+v.err))
	}
	return appendVoiceViewNotice(lines, m.voiceView.notice, width)
}

func (m model) renderVoiceSetup(width int) []string {
	manifest := voicepkg.DefaultLightManifest()
	total := int64(0)
	for _, artifact := range manifest.Artifacts {
		total += artifact.Bytes
	}
	installed := "not installed"
	if root := voicepkg.DefaultInstallRoot(); root != "" {
		if _, err := os.Stat(filepath.Join(root, "manifest.json")); err == nil {
			installed = "installed"
		}
	}
	lines := []string{
		"",
		voiceViewSection("Voice pack", width, manifest.ProfileVersion),
		sonutTheme.faint.Render(fmt.Sprintf("Download %.2f GiB · disk-space preflight %.2f GiB · %s", float64(total)/(1<<30), float64(manifest.RequiredBytes)/(1<<30), installed)),
		"",
		sonutTheme.ink.Bold(true).Render("Artifacts"),
		voiceViewArtifactLine(manifest.Artifacts[0].Name, manifest.Artifacts[0].Bytes, manifest.Artifacts[0].License),
		voiceViewArtifactLine(manifest.Artifacts[1].Name, manifest.Artifacts[1].Bytes, manifest.Artifacts[1].License),
		voiceViewArtifactLine(manifest.Artifacts[2].Name, manifest.Artifacts[2].Bytes, manifest.Artifacts[2].License),
		"",
		sonutTheme.faint.Render("audio.cpp " + manifest.AudioCPPRevision),
		sonutTheme.faint.Render("HTTPS-only downloads · resumable .part files · SHA-256 verified · atomic install"),
	}
	if m.voice != nil && m.voice.setupCancel != nil {
		lines = append(lines, "", sonutTheme.accent.Render("◌ "+nonEmpty(m.voice.setupStatus, "Installing voice pack…")), sonutTheme.faint.Render("Press c to cancel. Partial downloads remain resumable."))
	} else {
		lines = append(lines, "", sonutTheme.faint.Render("Press s to install or retry · Esc closes this page"))
	}
	return appendVoiceViewNotice(lines, m.voiceView.notice, width)
}

func (m model) renderVoiceDevices(width int) []string {
	if m.voice == nil {
		return []string{"", sonutTheme.amber.Render("Voice is unavailable in this session.")}
	}
	lines := []string{
		"",
		voiceViewSection("Audio devices", width, "miniaudio"),
		voiceViewSettingLine("Input", m.voice.config.InputDevice, "i next"),
		voiceViewSettingLine("Output", m.voice.config.OutputDevice, "o next"),
		"",
	}
	if len(m.voice.devices) == 0 {
		lines = append(lines, sonutTheme.faint.Render("No device list loaded."), sonutTheme.faint.Render("Press r to refresh devices."))
	} else {
		for _, device := range m.voice.devices {
			marker := "  "
			if device.ID == m.voice.config.InputDevice || device.ID == m.voice.config.OutputDevice {
				marker = sonutTheme.accent.Render("› ")
			}
			lines = append(lines, marker+sonutTheme.ink.Render(device.ID)+"  "+sonutTheme.muted.Render(device.Name)+"  "+sonutTheme.faint.Render(device.Direction))
		}
	}
	lines = append(lines, "", sonutTheme.faint.Render("i cycles input · o cycles output · r refresh · Esc closes this page"))
	return appendVoiceViewNotice(lines, m.voiceView.notice, width)
}

func (m model) renderVoiceDoctor(width int) []string {
	lines := []string{
		"",
		voiceViewSection("Diagnostics", width, "local checks"),
	}
	if m.voice != nil {
		for _, line := range wrapPlainText(m.voiceDoctorText(), maxInt(20, width-2)) {
			lines = append(lines, sonutTheme.muted.Render(line))
		}
	}
	lines = append(lines,
		"",
		voiceViewSection("Privacy", width),
		sonutTheme.faint.Render("Microphone capture is opened only while listening or arming."),
		sonutTheme.faint.Render("Raw microphone audio, transcripts, and diagnostic logs are not retained by default."),
		"",
		voiceViewSection("Permissions", width),
		sonutTheme.faint.Render("macOS: System Settings → Privacy & Security → Microphone."),
		sonutTheme.faint.Render("Windows: Privacy & security → Microphone → desktop-app access."),
		sonutTheme.faint.Render("Linux: check PipeWire, PulseAudio, or ALSA source/sink permissions."),
		"",
		sonutTheme.faint.Render("r refresh helper/device diagnostics · Esc closes this page"),
	)
	return appendVoiceViewNotice(lines, m.voiceView.notice, width)
}

func voiceViewSection(title string, width int, info ...string) string {
	left := sonutTheme.ink.Bold(true).Render(title)
	right := ""
	if len(info) > 0 && strings.TrimSpace(info[0]) != "" {
		right = sonutTheme.faint.Render(info[0])
	}
	remaining := maxInt(1, width-lipgloss.Width(left)-lipgloss.Width(right)-2)
	line := left + " " + sonutTheme.line.Render(strings.Repeat("─", remaining))
	if right != "" {
		line += " " + right
	}
	return line
}

func voiceViewSettingLine(label, value, hint string) string {
	return voiceViewSettingLineSelected(label, value, hint, false)
}

func voiceViewSettingLineSelected(label, value, hint string, selected bool) string {
	surface := transparentSurface
	marker := "  "
	if selected {
		surface = sonutTheme.onSel
		marker = "› "
	}
	valueStyle := sonutTheme.ink
	if label == "Enabled" {
		if value == "on" {
			valueStyle = sonutTheme.green
		} else {
			valueStyle = sonutTheme.faint
		}
	}
	return surface(sonutTheme.ink).Render(marker) +
		surface(sonutTheme.muted).Render(fmt.Sprintf("%-18s", label)) +
		" " + surface(valueStyle).Render(value) +
		"  " + surface(sonutTheme.faint).Render("· "+hint)
}

func voiceViewArtifactLine(name string, bytes int64, license string) string {
	return "  " + sonutTheme.ink.Render(name) + sonutTheme.faint.Render(fmt.Sprintf("  %.2f GiB  ·  %s", float64(bytes)/(1<<30), license))
}

func appendVoiceViewNotice(lines []string, notice string, width int) []string {
	if strings.TrimSpace(notice) == "" {
		return lines
	}
	lines = append(lines, "", voiceViewSection("Notice", width))
	for _, line := range wrapPlainText(notice, maxInt(20, width-2)) {
		lines = append(lines, sonutTheme.amber.Render(line))
	}
	return lines
}

func (m model) voiceViewFooter(width int) string {
	footer := []string{sonutTheme.line.Render(strings.Repeat("─", maxInt(1, width)))}
	navigation := "1 Settings · 2 Setup · 3 Devices · 4 Doctor · Tab next · Esc/q main"
	switch m.voiceView.tab {
	case voiceViewSetup:
		footer = append(footer, sonutTheme.faint.Render(navigation))
		footer = append(footer, sonutTheme.faint.Render("s/Enter install/retry · c cancel"))
	case voiceViewDevices:
		footer = append(footer, sonutTheme.faint.Render(navigation))
		footer = append(footer, sonutTheme.faint.Render("i input · o output · r refresh"))
	case voiceViewDoctor:
		footer = append(footer, sonutTheme.faint.Render(navigation))
		footer = append(footer, sonutTheme.faint.Render("r refresh diagnostics"))
	default:
		footer = append(footer, sonutTheme.faint.Render(navigation))
		footer = append(footer, sonutTheme.faint.Render("↑/↓ select · Enter change · e/l/v/k shortcuts · -/+ speed · d devices"))
	}
	for index := range footer {
		footer[index] = fitStyledLine(footer[index], width)
	}
	return strings.Join(footer, "\n")
}

func (m model) handleVoiceViewKey(msg tea.KeyPressMsg) (model, tea.Cmd, bool) {
	if !m.voiceView.active {
		return m, nil, false
	}
	if keyCtrl(msg, 'c') || keyIs(msg, tea.KeyEsc) || strings.EqualFold(keyText(msg), "q") {
		return m.closeVoiceView(), nil, true
	}
	// A slash is the escape hatch for the normal command flow. Close the page
	// and let the same key continue into the composer, so `/sec`, `/help`, and
	// other commands never require a separate exit keystroke.
	if keyText(msg) == "/" {
		return m.closeVoiceView(), nil, false
	}
	if keyIs(msg, tea.KeyPgUp) || keyIs(msg, tea.KeyPgDown) {
		return m, nil, false
	}
	if keyIs(msg, tea.KeyTab) {
		delta := 1
		if keyShift(msg) {
			delta = -1
		}
		return m.setVoiceViewTab(voiceViewTab((int(m.voiceView.tab) + delta + int(voiceViewTabCount)) % int(voiceViewTabCount))), nil, true
	}
	if m.voiceView.tab != voiceViewSettings && (keyIs(msg, tea.KeyLeft) || keyIs(msg, tea.KeyRight)) {
		delta := -1
		if keyIs(msg, tea.KeyRight) {
			delta = 1
		}
		return m.setVoiceViewTab(voiceViewTab((int(m.voiceView.tab) + delta + int(voiceViewTabCount)) % int(voiceViewTabCount))), nil, true
	}
	switch keyText(msg) {
	case "1":
		return m.setVoiceViewTab(voiceViewSettings), nil, true
	case "2":
		return m.setVoiceViewTab(voiceViewSetup), nil, true
	case "3":
		return m.setVoiceViewTab(voiceViewDevices), nil, true
	case "4":
		return m.setVoiceViewTab(voiceViewDoctor), nil, true
	}
	if m.voice == nil {
		return m, nil, true
	}
	switch m.voiceView.tab {
	case voiceViewSettings:
		switch {
		case keyIs(msg, tea.KeyUp):
			m.voiceView.selected = clampInt(m.voiceView.selected-1, 0, voiceSettingCount-1)
			return m, nil, true
		case keyIs(msg, tea.KeyDown):
			m.voiceView.selected = clampInt(m.voiceView.selected+1, 0, voiceSettingCount-1)
			return m, nil, true
		case keyIs(msg, tea.KeyEnter):
			return m.activateVoiceViewSetting()
		case keyIs(msg, tea.KeyLeft), keyIs(msg, tea.KeyRight):
			if m.voiceView.selected == voiceSettingSpeed {
				if keyIs(msg, tea.KeyLeft) {
					m.voice.config.Speed = math.Max(0.5, math.Round((m.voice.config.Speed-0.05)*100)/100)
				} else {
					m.voice.config.Speed = math.Min(2.0, math.Round((m.voice.config.Speed+0.05)*100)/100)
				}
				return m.saveVoiceViewConfig(fmt.Sprintf("Speaking speed set to %.2fx.", m.voice.config.Speed)), nil, true
			}
		}
		switch keyText(msg) {
		case "e":
			m.voice.config.Enabled = !m.voice.config.Enabled
			if !m.voice.config.Enabled {
				m = m.voiceCancel(false)
				m.voice.state = voicepkg.StateDisabled
			}
			if err := m.persistVoiceConfig(); err != nil {
				m.voiceView.notice = err.Error()
			} else {
				m.voiceView.notice = "Voice settings saved."
				if m.voice.config.Enabled && !m.voice.ready {
					return m, m.voiceStartCmd(), true
				}
			}
		case "l":
			m.voice.config.Language = cycleVoiceValue(m.voice.config.Language, voiceViewLanguages)
			m = m.saveVoiceViewConfig("Language set to " + m.voice.config.Language + ".")
		case "v":
			m.voice.config.ResponseVoice = cycleVoiceValue(m.voice.config.ResponseVoice, voiceViewVoices)
			m = m.saveVoiceViewConfig("Response voice set to " + m.voice.config.ResponseVoice + ".")
		case "-":
			m.voice.config.Speed = math.Max(0.5, math.Round((m.voice.config.Speed-0.05)*100)/100)
			m = m.saveVoiceViewConfig(fmt.Sprintf("Speaking speed set to %.2fx.", m.voice.config.Speed))
		case "+":
			m.voice.config.Speed = math.Min(2.0, math.Round((m.voice.config.Speed+0.05)*100)/100)
			m = m.saveVoiceViewConfig(fmt.Sprintf("Speaking speed set to %.2fx.", m.voice.config.Speed))
		case "k":
			m.voice.config.Shortcut = cycleVoiceValue(m.voice.config.Shortcut, voiceViewShortcuts)
			m.voice.hold = voicepkg.NewHoldMachine(mustVoiceShortcut(m.voice.config.Shortcut))
			m = m.saveVoiceViewConfig("Shortcut set to " + m.voice.config.Shortcut + ".")
		case "d":
			m = m.setVoiceViewTab(voiceViewDevices)
			if m.voice.manager != nil {
				if err := m.voice.manager.ListDevices(); err != nil {
					m.voiceView.notice = err.Error()
				} else {
					m.voiceView.notice = "Refreshing audio devices…"
				}
			}
		}
	case voiceViewSetup:
		if keyIs(msg, tea.KeyEnter) {
			return m.startVoiceViewSetup()
		}
		switch keyText(msg) {
		case "s", "r":
			return m.startVoiceViewSetup()
		case "c":
			m = m.voiceStopSetup()
			m.voiceView.notice = "Setup cancelled. Partial downloads remain resumable."
		}
	case voiceViewDevices:
		switch keyText(msg) {
		case "r":
			if m.voice.manager != nil {
				if err := m.voice.manager.ListDevices(); err != nil {
					m.voiceView.notice = err.Error()
				} else {
					m.voiceView.notice = "Refreshing audio devices…"
				}
			}
		case "i":
			m = m.cycleVoiceDevice(true)
		case "o":
			m = m.cycleVoiceDevice(false)
		}
	case voiceViewDoctor:
		if keyText(msg) == "r" && m.voice.manager != nil {
			if err := m.voice.manager.Ping(); err != nil {
				m.voiceView.notice = err.Error()
			} else {
				m.voiceView.notice = "Helper ping sent."
			}
		}
	}
	return m, nil, true
}

func (m model) handleVoiceViewMouse(msg tea.MouseMsg) (model, tea.Cmd, bool) {
	if !m.voiceView.active || !mouseLeftPress(msg) || mouseY(msg) != 0 {
		return m, nil, false
	}
	width := chatWidth(m.width)
	left := "◆ Voice  · Esc/q main"
	if mouseX(msg) < lipgloss.Width(left)+2 {
		return m.closeVoiceView(), nil, true
	}

	// The tabs are right-aligned by voiceViewNavBar. Recreate their compact
	// hitboxes from the unstyled labels so a click on any visible tab changes
	// page without relying on keyboard knowledge.
	rightWidth := 0
	for index, name := range voiceViewTabNames {
		label := name
		if voiceViewTab(index) == m.voiceView.tab {
			label = "[" + name + "]"
		}
		rightWidth += lipgloss.Width(label)
		if index+1 < len(voiceViewTabNames) {
			rightWidth += 2
		}
	}
	start := maxInt(0, width-rightWidth)
	position := start
	for index, name := range voiceViewTabNames {
		label := name
		if voiceViewTab(index) == m.voiceView.tab {
			label = "[" + name + "]"
		}
		end := position + lipgloss.Width(label)
		if mouseX(msg) >= position && mouseX(msg) < end {
			return m.setVoiceViewTab(voiceViewTab(index)), nil, true
		}
		position = end + 2
	}
	return m, nil, true
}

func (m model) startVoiceViewSetup() (model, tea.Cmd, bool) {
	if m.voice.setupCancel == nil {
		m.voice.setupStatus = "Preparing voice pack…"
		m.voiceView.notice = "Setup started."
		return m, m.voiceSetupCmd(), true
	}
	m.voiceView.notice = "Voice setup is already running."
	return m, nil, true
}

func (m model) activateVoiceViewSetting() (model, tea.Cmd, bool) {
	switch m.voiceView.selected {
	case voiceSettingEnabled:
		m.voice.config.Enabled = !m.voice.config.Enabled
		if !m.voice.config.Enabled {
			m = m.voiceCancel(false)
			m.voice.state = voicepkg.StateDisabled
		}
		if err := m.persistVoiceConfig(); err != nil {
			m.voiceView.notice = err.Error()
		} else {
			m.voiceView.notice = "Voice settings saved."
			if m.voice.config.Enabled && !m.voice.ready {
				return m, m.voiceStartCmd(), true
			}
		}
	case voiceSettingLanguage:
		m.voice.config.Language = cycleVoiceValue(m.voice.config.Language, voiceViewLanguages)
		m = m.saveVoiceViewConfig("Language set to " + m.voice.config.Language + ".")
	case voiceSettingResponseVoice:
		m.voice.config.ResponseVoice = cycleVoiceValue(m.voice.config.ResponseVoice, voiceViewVoices)
		m = m.saveVoiceViewConfig("Response voice set to " + m.voice.config.ResponseVoice + ".")
	case voiceSettingSpeed:
		m.voice.config.Speed = math.Min(2.0, math.Round((m.voice.config.Speed+0.05)*100)/100)
		m = m.saveVoiceViewConfig(fmt.Sprintf("Speaking speed set to %.2fx.", m.voice.config.Speed))
	case voiceSettingShortcut:
		m.voice.config.Shortcut = cycleVoiceValue(m.voice.config.Shortcut, voiceViewShortcuts)
		m.voice.hold = voicepkg.NewHoldMachine(mustVoiceShortcut(m.voice.config.Shortcut))
		m = m.saveVoiceViewConfig("Shortcut set to " + m.voice.config.Shortcut + ".")
	case voiceSettingInput:
		m = m.setVoiceViewTab(voiceViewDevices)
		m.voiceView.notice = "Select an input device with i."
	case voiceSettingOutput:
		m = m.setVoiceViewTab(voiceViewDevices)
		m.voiceView.notice = "Select an output device with o."
	}
	return m, nil, true
}

func cycleVoiceValue(current string, values []string) string {
	for index, value := range values {
		if strings.EqualFold(current, value) {
			return values[(index+1)%len(values)]
		}
	}
	return values[0]
}

func mustVoiceShortcut(raw string) voicepkg.Shortcut {
	parsed, err := voicepkg.ParseShortcut(raw)
	if err != nil {
		parsed, _ = voicepkg.ParseShortcut("hold:space:2s")
	}
	return parsed
}

func (m model) saveVoiceViewConfig(notice string) model {
	if err := m.persistVoiceConfig(); err != nil {
		m.voiceView.notice = err.Error()
	} else {
		m.voiceView.notice = notice
	}
	return m
}

func (m model) cycleVoiceDevice(input bool) model {
	if len(m.voice.devices) == 0 {
		m.voiceView.notice = "No audio devices loaded. Press r to refresh."
		return m
	}
	current := m.voice.config.OutputDevice
	direction := "output"
	if input {
		current = m.voice.config.InputDevice
		direction = "input"
	}
	candidates := make([]voicepkg.Device, 0, len(m.voice.devices))
	for _, device := range m.voice.devices {
		if strings.Contains(strings.ToLower(device.Direction), direction) {
			candidates = append(candidates, device)
		}
	}
	if len(candidates) == 0 {
		m.voiceView.notice = "No " + direction + " devices reported."
		return m
	}
	index := 0
	for candidateIndex, device := range candidates {
		if device.ID == current {
			index = (candidateIndex + 1) % len(candidates)
			break
		}
	}
	if input {
		m.voice.config.InputDevice = candidates[index].ID
	} else {
		m.voice.config.OutputDevice = candidates[index].ID
	}
	label := strings.ToUpper(direction[:1]) + direction[1:]
	return m.saveVoiceViewConfig(label + " device set to " + candidates[index].ID + ".")
}
