package tui

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"sync"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/aistatine/sonut/internal/agent"
	"github.com/aistatine/sonut/internal/config"
	voicepkg "github.com/aistatine/sonut/internal/voice"
)

type voiceRuntime struct {
	manager *voicepkg.Manager
	config  config.VoiceConfig
	state   voicepkg.State
	ready   bool
	err     string
	devices []voicepkg.Device

	utteranceID string
	// responseLanguage is scoped to the active utterance. It starts with the
	// configured hint and is replaced by the ASR detector when available; it
	// must not be persisted as a personal setting.
	responseLanguage string
	transcript       *voicepkg.ReplaceableTranscript
	hold             *voicepkg.HoldMachine
	keyRelease       bool

	pending     *voiceBuildConfirmation
	chunker     *voicepkg.SentenceChunker
	setupStatus string
	setupCancel context.CancelFunc
	mu          sync.Mutex
}

type voiceBuildConfirmation struct {
	Prompt        string
	Language      string
	UtteranceID   string
	PlannerPrompt string
}

type voiceEventMsg struct{ event voicepkg.Event }
type voiceStartedMsg struct{ err error }
type voiceSetupProgressMsg struct {
	artifact string
	received int64
	total    int64
}
type voiceSetupDoneMsg struct{ err error }

func (v *voiceRuntime) currentUtteranceID() string {
	v.mu.Lock()
	defer v.mu.Unlock()
	return v.utteranceID
}

func (v *voiceRuntime) setUtteranceID(id string) {
	v.mu.Lock()
	v.utteranceID = id
	v.mu.Unlock()
}

func (v *voiceRuntime) setResponseLanguage(language string) {
	v.mu.Lock()
	v.responseLanguage = language
	v.mu.Unlock()
}

func (v *voiceRuntime) currentResponseLanguage() string {
	v.mu.Lock()
	defer v.mu.Unlock()
	return v.responseLanguage
}

func newVoiceRuntime(options Options, sink func(tea.Msg)) *voiceRuntime {
	cfg := options.Voice.Effective()
	parsed, err := voicepkg.ParseShortcut(cfg.Shortcut)
	if err != nil {
		parsed, _ = voicepkg.ParseShortcut("hold:space:2s")
	}
	runtime := &voiceRuntime{config: cfg, state: voicepkg.StateUninstalled, hold: voicepkg.NewHoldMachine(parsed), chunker: voicepkg.NewSentenceChunker()}
	runtime.manager = voicepkg.NewManager(voicepkg.ManagerOptions{
		Config: cfg,
		OnEvent: func(event voicepkg.Event) {
			if sink != nil {
				sink(voiceEventMsg{event: event})
			}
		},
		Stderr: os.Stderr,
	})
	return runtime
}

func (m model) voiceStartCmd() tea.Cmd {
	if m.voice == nil || m.voice.manager == nil {
		return nil
	}
	manager := m.voice.manager
	ctx := m.ctx
	return func() tea.Msg {
		err := manager.Start(ctx)
		return voiceStartedMsg{err: err}
	}
}

func (m model) voiceStatusLabel() string {
	if m.voice == nil {
		return "Voice unavailable"
	}
	v := m.voice
	if v.setupStatus != "" {
		return v.setupStatus
	}
	switch v.state {
	case voicepkg.StateDisabled:
		return "Voice off"
	case voicepkg.StateUninstalled:
		return "Voice not installed · /voice setup"
	case voicepkg.StateArming:
		if v.hold != nil && v.hold.Active() {
			return "Hold Space… 2s"
		}
		return "Voice arming"
	case voicepkg.StateListening:
		language := v.config.Language
		if language == "" {
			language = "auto"
		}
		return "Listening · " + language
	case voicepkg.StateFinalizing:
		return "Transcribing"
	case voicepkg.StateSubmitting:
		return "Submitting"
	case voicepkg.StateThinking:
		return "Thinking"
	case voicepkg.StateSpeaking:
		return fmt.Sprintf("Speaking · %s · %.2fx", v.config.ResponseVoice, v.config.Speed)
	case voicepkg.StateConfirmBuild:
		return "Build confirmation required"
	case voicepkg.StateBuilding:
		return "Building"
	case voicepkg.StateError:
		if v.err != "" {
			return "Voice error · " + v.err
		}
		return "Voice error · /voice doctor"
	default:
		return "Voice ready"
	}
}

func (m model) voiceSetupCmd() tea.Cmd {
	if m.voice == nil {
		return nil
	}
	root := voicepkg.DefaultInstallRoot()
	if root == "" {
		return func() tea.Msg {
			return voiceSetupDoneMsg{err: fmt.Errorf("cannot determine the user voice install directory")}
		}
	}
	ctx := m.ctx
	if ctx == nil {
		ctx = context.Background()
	}
	ctx, cancel := context.WithCancel(ctx)
	m.voice.setupCancel = cancel
	sink := m.runtimeMessageSink
	return func() tea.Msg {
		err := voicepkg.Install(ctx, voicepkg.DefaultLightManifest(), voicepkg.InstallerOptions{
			Root:   root,
			Client: http.DefaultClient,
			Progress: func(progress voicepkg.DownloadProgress) {
				if sink != nil {
					sink(voiceSetupProgressMsg{artifact: progress.Artifact.Name, received: progress.Received, total: progress.Total})
				}
			},
		})
		cancel()
		return voiceSetupDoneMsg{err: err}
	}
}

func (m model) voiceStopSetup() model {
	if m.voice != nil && m.voice.setupCancel != nil {
		m.voice.setupCancel()
		m.voice.setupCancel = nil
		m.voice.setupStatus = ""
	}
	return m
}

func (m model) persistVoiceConfig() error {
	if m.voice == nil {
		return fmt.Errorf("voice is unavailable in this session")
	}
	m.voice.config = m.voice.config.Effective()
	if err := m.voice.config.Validate(); err != nil {
		return err
	}
	if _, err := config.SetVoiceConfig(m.userConfigPath, m.voice.config); err != nil {
		return err
	}
	if m.voice.manager != nil {
		m.voice.manager.SetConfig(m.voice.config)
	}
	return nil
}

func (m model) voiceEnabledAndFocused() bool {
	return m.voice != nil && m.voice.ready && m.voice.config.Enabled && (m.composerActive || m.input.Focused()) &&
		m.noBlockingModal() && !m.setup.visible && !m.transcriptDetailed && !m.subchat.active
}

func (m *model) voiceKeyboardEnhancements(msg tea.KeyboardEnhancementsMsg) {
	if m.voice != nil {
		m.voice.keyRelease = msg.SupportsEventTypes()
	}
}

func (m model) voiceManualPasteActive() bool {
	return m.voiceIsTemporaryActive() && m.voice.pending == nil
}

func (m model) voiceIsTemporaryActive() bool {
	if m.voice == nil {
		return false
	}
	switch m.voice.state {
	case voicepkg.StateArming, voicepkg.StateListening, voicepkg.StateFinalizing,
		voicepkg.StateSubmitting, voicepkg.StateThinking, voicepkg.StateSpeaking:
		return true
	default:
		return false
	}
}

func (m model) voiceNewUtteranceID() string {
	return fmt.Sprintf("u-%d-%d", m.runID+1, time.Now().UnixNano())
}

func (m model) voiceStartListening() model {
	if m.voice == nil || !m.voice.ready || !m.voice.config.Enabled {
		return m
	}
	currentID := m.voice.currentUtteranceID()
	if m.voice.state == voicepkg.StateSpeaking && m.voice.manager != nil {
		_ = m.voice.manager.CancelTTS(currentID)
	}
	if m.voice.transcript != nil {
		m = m.voiceCancel(false)
	}
	m.voice.setUtteranceID(m.voiceNewUtteranceID())
	utteranceID := m.voice.currentUtteranceID()
	m.voice.setResponseLanguage(m.voice.config.Language)
	m.voice.transcript = voicepkg.NewReplaceableTranscript(m.currentComposerSnapshot())
	m.voice.state = voicepkg.StateListening
	m.voice.err = ""
	if err := m.voice.manager.StartListening(utteranceID); err != nil {
		m.voice.state = voicepkg.StateError
		m.voice.err = err.Error()
		m.voice.transcript = nil
		return m
	}
	return m
}

func (m model) voiceCancel(submitTyped bool) model {
	if m.voice == nil {
		return m
	}
	id := m.voice.currentUtteranceID()
	if m.voice.transcript != nil {
		m.setComposerSnapshot(m.voice.transcript.Cancel())
		m.voice.transcript = nil
	}
	if m.voice.manager != nil && id != "" {
		_ = m.voice.manager.CancelListening(id)
		_ = m.voice.manager.CancelTTS(id)
	}
	m.voice.setUtteranceID("")
	m.voice.setResponseLanguage("")
	m.voice.state = voicepkg.StateIdle
	if !submitTyped {
		m.voice.pending = nil
	}
	return m
}

func (m model) handleVoiceEvent(event voicepkg.Event) (model, tea.Cmd) {
	if m.voice == nil {
		return m, nil
	}
	v := m.voice
	switch event.Type {
	case voicepkg.EventReady:
		v.ready = true
		v.state = voicepkg.StateIdle
		v.err = ""
	case voicepkg.EventState:
		if event.UtteranceID != "" && event.UtteranceID != v.currentUtteranceID() {
			return m, nil
		}
		if event.State != "" {
			v.state = voicepkg.State(event.State)
		}
	case voicepkg.EventDevices:
		v.devices = append([]voicepkg.Device(nil), event.Devices...)
		if m.voiceView.active {
			m.voiceView.notice = fmt.Sprintf("Audio devices refreshed · %d device(s).", len(event.Devices))
		}
	case voicepkg.EventASRPartial:
		if event.UtteranceID != v.utteranceID || v.transcript == nil {
			return m, nil
		}
		if strings.TrimSpace(event.Language) != "" {
			v.setResponseLanguage(strings.ToLower(strings.TrimSpace(event.Language)))
		}
		m.setComposerSnapshot(v.transcript.Update(event.Text))
	case voicepkg.EventASRFinal:
		if v.pending != nil && v.transcript == nil && event.UtteranceID == v.utteranceID {
			answer := strings.ToLower(strings.TrimSpace(event.Text))
			switch answer {
			case "yes", "y", "да", "نعم", "sí", "si", "oui", "はい", "네":
				return m.confirmVoiceBuild(true)
			case "no", "n", "нет", "لا", "non", "いいえ", "아니요":
				return m.confirmVoiceBuild(false)
			default:
				m.voice.state = voicepkg.StateConfirmBuild
				m.voice.setUtteranceID("")
				m.transcript = reduceTranscript(m.transcript, transcriptAction{kind: actionAppendSystem, text: "Please answer yes or no. Build confirmation remains open."})
				return m, nil
			}
		}
		if event.UtteranceID != v.utteranceID || v.transcript == nil {
			return m, nil
		}
		v.state = voicepkg.StateFinalizing
		if strings.TrimSpace(event.Language) != "" {
			v.setResponseLanguage(strings.ToLower(strings.TrimSpace(event.Language)))
		}
		final := voicepkg.NormalizeTranscript(event.Text)
		m.setComposerSnapshot(v.transcript.Commit(final))
		v.transcript = nil
		return m.submitVoiceTranscript(final, event.Language)
	case voicepkg.EventTTSStarted:
		if event.UtteranceID == v.utteranceID {
			v.state = voicepkg.StateSpeaking
		}
	case voicepkg.EventTTSDone:
		if event.UtteranceID == v.utteranceID && !m.pending {
			v.state = voicepkg.StateIdle
		}
	case voicepkg.EventError, voicepkg.EventHelperExit:
		if event.UtteranceID != "" && event.UtteranceID != v.utteranceID {
			return m, nil
		}
		v.state = voicepkg.StateError
		if event.Error != nil {
			v.err = event.Error.Error()
			if strings.Contains(strings.ToLower(v.err), "tts not available") || strings.Contains(strings.ToLower(v.err), "tts language") {
				if m.voiceView.active {
					m.voiceView.notice = "TTS not available for this language; the response remains visible as text."
				}
			}
		}
	}
	return m, nil
}

func (m model) submitVoiceTranscript(transcript, language string) (model, tea.Cmd) {
	if transcript == "" {
		m.voice.state = voicepkg.StateIdle
		m.clearComposer()
		return m, nil
	}
	mode := agent.RouteRunMode(transcript)
	utteranceID := m.voice.currentUtteranceID()
	if language == "" {
		language = m.voiceLanguage()
	}
	if mode == agent.RunModeBuilder {
		m.voice.pending = &voiceBuildConfirmation{Prompt: transcript, Language: language, UtteranceID: utteranceID, PlannerPrompt: transcript}
		m.voice.state = voicepkg.StateConfirmBuild
		m.transcript = reduceTranscript(m.transcript, transcriptAction{kind: actionAppendSystem, text: "This request needs build mode. Should I start building? Press Y/Enter to confirm or N/Esc to cancel."})
		m.clearComposer()
		if m.voice.manager != nil {
			_ = m.voice.manager.EnqueueTTS(utteranceID, "This request needs build mode. Should I start building?", language)
		}
		return m, nil
	}
	m.voice.state = voicepkg.StateSubmitting
	m.clearComposer()
	next, cmd := m.launchPromptWithOptions(transcript, tuiAgentRunOptions{
		runMode:     agent.RunModeQuick,
		voiceOrigin: true, speakResponse: true, voiceUtteranceID: utteranceID,
		plannerPrompt: transcript,
	})
	return next, cmd
}

func (m model) confirmVoiceBuild(approved bool) (model, tea.Cmd) {
	if m.voice == nil || m.voice.pending == nil {
		return m, nil
	}
	pending := *m.voice.pending
	m.voice.pending = nil
	if !approved {
		m.voice.state = voicepkg.StateIdle
		m.voice.setUtteranceID("")
		m.voice.setResponseLanguage("")
		m.transcript = reduceTranscript(m.transcript, transcriptAction{kind: actionAppendSystem, text: "Build cancelled."})
		if m.voice.manager != nil {
			_ = m.voice.manager.CancelTTS(pending.UtteranceID)
		}
		return m, nil
	}
	// The original request ID owns the builder run and its acknowledgement;
	// the separate yes/no utterance must not become the run's speech identity.
	m.voice.setUtteranceID(pending.UtteranceID)
	m.voice.setResponseLanguage(pending.Language)
	m.voice.state = voicepkg.StateBuilding
	if m.voice.manager != nil {
		_ = m.voice.manager.CancelTTS(pending.UtteranceID)
		_ = m.voice.manager.EnqueueTTS(pending.UtteranceID, "Starting build mode.", pending.Language)
	}
	m.clearComposer()
	return m.launchPromptWithOptions(pending.Prompt, tuiAgentRunOptions{
		runMode: agent.RunModeBuilder, voiceOrigin: true, speakResponse: true, voiceUtteranceID: pending.UtteranceID,
		plannerPrompt: pending.PlannerPrompt,
	})
}

func (m model) voiceManualTyping(msg tea.KeyMsg) bool {
	if !m.voiceIsTemporaryActive() {
		return false
	}
	if keyIs(msg, tea.KeySpace) || keyText(msg) == " " {
		return false
	}
	return keyPrintable(msg) || keyBackspace(msg) || keyIs(msg, tea.KeyDelete) || keyIs(msg, tea.KeyEnter) || keyIs(msg, tea.KeyTab)
}

func (m model) handleVoiceKey(msg tea.KeyMsg) (model, tea.Cmd, bool) {
	if m.voice == nil {
		return m, nil, false
	}
	if m.voice.pending != nil {
		switch {
		case keyIs(msg, tea.KeyEnter) || strings.EqualFold(keyText(msg), "y"):
			next, cmd := m.confirmVoiceBuild(true)
			return next, cmd, true
		case keyIs(msg, tea.KeyEsc) || strings.EqualFold(keyText(msg), "n"):
			next, cmd := m.confirmVoiceBuild(false)
			return next, cmd, true
		}
		// The confirmation is also voice-addressable. Keep the pending request
		// intact while a fresh utterance is recorded for a yes/no answer.
		if next, cmd, handled := m.handleVoiceActivationKey(msg); handled {
			return next, cmd, true
		}
		return m, nil, true
	}
	if keyIs(msg, tea.KeyEsc) && m.voiceIsTemporaryActive() {
		return m.voiceCancel(false), nil, true
	}
	if !m.voiceEnabledAndFocused() {
		return m, nil, false
	}
	shortcut, err := voicepkg.ParseShortcut(m.voice.config.Shortcut)
	if err != nil || shortcut.Kind == voicepkg.ShortcutOff {
		return m, nil, false
	}
	if shortcut.Kind == voicepkg.ShortcutChord && strings.EqualFold(msg.Key().Keystroke(), shortcut.Key) {
		return m.voiceStartListening(), nil, true
	}
	if shortcut.Kind != voicepkg.ShortcutHold || !keyIs(msg, tea.KeySpace) && keyText(msg) != " " {
		if m.voiceManualTyping(msg) {
			// The outer KeyPress handler owns the cancellation so it can preserve
			// and process this same keystroke. Returning a mutated model here with
			// handled=false would discard that mutation in the caller.
			return m, nil, false
		}
		return m, nil, false
	}
	if m.voice.keyRelease {
		event := m.voice.hold.Press(m.now(), msg.Key().IsRepeat)
		if event == voicepkg.HoldActivate {
			return m.voiceStartListening(), nil, true
		}
		m.voice.state = voicepkg.StateArming
		return m, nil, true
	}
	// Legacy terminals cannot tell a key release from a press. Preserve the
	// first Space as normal text and use a genuine repeated Space as the hold
	// signal; repeated spaces are swallowed once the fallback is armed.
	if !msg.Key().IsRepeat {
		m.voice.hold.Press(m.now(), false)
		m.voice.state = voicepkg.StateArming
		state := m.currentComposerState()
		m.setComposerState(insertComposerText(state, " "))
		return m, nil, true
	}
	event := m.voice.hold.Press(m.now(), true)
	if event == voicepkg.HoldActivate {
		return m.voiceStartListening(), nil, true
	}
	m.voice.state = voicepkg.StateArming
	return m, nil, true
}

func (m model) handleVoiceActivationKey(msg tea.KeyMsg) (model, tea.Cmd, bool) {
	if !m.voiceEnabledAndFocused() {
		return m, nil, false
	}
	shortcut, err := voicepkg.ParseShortcut(m.voice.config.Shortcut)
	if err != nil || shortcut.Kind == voicepkg.ShortcutOff {
		return m, nil, false
	}
	if shortcut.Kind == voicepkg.ShortcutChord && strings.EqualFold(msg.Key().Keystroke(), shortcut.Key) {
		return m.voiceStartListening(), nil, true
	}
	if shortcut.Kind != voicepkg.ShortcutHold || (!keyIs(msg, tea.KeySpace) && keyText(msg) != " ") {
		return m, nil, false
	}
	if m.voice.keyRelease {
		if m.voice.hold.Press(m.now(), msg.Key().IsRepeat) == voicepkg.HoldActivate {
			return m.voiceStartListening(), nil, true
		}
		return m, nil, true
	}
	if msg.Key().IsRepeat && m.voice.hold.Press(m.now(), true) == voicepkg.HoldActivate {
		return m.voiceStartListening(), nil, true
	}
	return m, nil, true
}

func (m model) handleVoiceRelease(msg tea.KeyReleaseMsg) (model, tea.Cmd, bool) {
	if m.voiceView.active {
		return m, nil, true
	}
	if m.voice == nil || !m.voice.keyRelease || !keyReleaseIsSpace(msg) {
		return m, nil, false
	}
	if m.voice.pending != nil {
		m.voice.hold.Release(m.now())
		return m, nil, true
	}
	event := m.voice.hold.Release(m.now())
	if event == voicepkg.HoldInsert {
		state := m.currentComposerState()
		m.setComposerState(insertComposerText(state, " "))
	}
	if event == voicepkg.HoldInsert || event == voicepkg.HoldCancel {
		m.voice.state = voicepkg.StateIdle
	}
	return m, nil, true
}

func keyReleaseIsSpace(msg tea.KeyReleaseMsg) bool {
	return msg.Key().Code == tea.KeySpace || msg.Key().Text == " "
}

func (m model) voiceHandleCommand(text string) (model, string, tea.Cmd) {
	args := strings.Fields(text)
	if m.voice == nil {
		return m, "", nil
	}
	if len(args) == 0 {
		return m.openVoiceView(voiceViewSettings), "", nil
	}
	arg := strings.ToLower(args[0])
	switch arg {
	case "on", "off":
		m = m.openVoiceView(voiceViewSettings)
		m.voice.config.Enabled = arg == "on"
		if err := m.persistVoiceConfig(); err != nil {
			m.voiceView.notice = err.Error()
			return m, "", nil
		}
		if !m.voice.config.Enabled {
			m = m.voiceCancel(false)
			m.voice.state = voicepkg.StateDisabled
		} else if !m.voice.ready {
			m.voiceView.notice = "Voice enabled. The helper is not ready; install the helper or run setup."
			return m, "", m.voiceStartCmd()
		}
		if m.voice.config.Enabled {
			m.voice.state = voicepkg.StateIdle
		}
		m.voiceView.notice = "Voice " + arg + "."
		return m, "", nil
	case "status":
		return m.openVoiceView(voiceViewSettings), "", nil
	case "listen":
		m = m.openVoiceView(voiceViewSettings)
		if !m.voice.ready {
			m.voiceView.notice = "Voice helper is not ready; install the helper or run setup."
			return m, "", nil
		}
		m = m.voiceStartListening()
		m.voiceView.notice = "Listening."
		return m, "", nil
	case "stop":
		m = m.openVoiceView(voiceViewSettings)
		m = m.voiceStopSetup()
		m = m.voiceCancel(false)
		m.voiceView.notice = "Voice stopped."
		return m, "", nil
	case "language":
		m = m.openVoiceView(voiceViewSettings)
		if len(args) != 2 {
			m.voiceView.notice = "Usage: /voice language auto|<BCP-47 code>"
			return m, "", nil
		}
		language := strings.ToLower(strings.TrimSpace(args[1]))
		if language != "auto" && !regexp.MustCompile(`^[a-z]{2,3}(?:-[a-z0-9]{2,8})?$`).MatchString(language) {
			m.voiceView.notice = "Language must be auto or a supported BCP-47-style code such as en, hi, ar, or zh."
			return m, "", nil
		}
		m.voice.config.Language = language
	case "voice":
		m = m.openVoiceView(voiceViewSettings)
		if len(args) != 2 || !regexp.MustCompile(`^[fF][1-5]$`).MatchString(args[1]) {
			m.voiceView.notice = "Usage: /voice voice F1|F2|F3|F4|F5"
			return m, "", nil
		}
		m.voice.config.ResponseVoice = strings.ToUpper(args[1])
	case "speed":
		m = m.openVoiceView(voiceViewSettings)
		if len(args) != 2 {
			m.voiceView.notice = "Usage: /voice speed 0.5..2.0"
			return m, "", nil
		}
		var speed float64
		if _, err := fmt.Sscan(args[1], &speed); err != nil {
			m.voiceView.notice = "Speed must be a number from 0.5 to 2.0."
			return m, "", nil
		}
		m.voice.config.Speed = speed
	case "shortcut":
		m = m.openVoiceView(voiceViewSettings)
		if len(args) != 2 {
			m.voiceView.notice = "Usage: /voice shortcut hold:space:2s|ctrl+space|alt+v|f8|off"
			return m, "", nil
		}
		parsed, err := voicepkg.ParseShortcut(args[1])
		if err != nil {
			m.voiceView.notice = err.Error()
			return m, "", nil
		}
		m.voice.config.Shortcut = parsed.Raw
		m.voice.hold = voicepkg.NewHoldMachine(parsed)
	case "input", "output":
		m = m.openVoiceView(voiceViewDevices)
		if len(args) > 2 {
			m.voiceView.notice = "Usage: /voice input [device-id] or /voice output [device-id]"
			return m, "", nil
		}
		if len(args) == 1 {
			if m.voice.manager != nil {
				if err := m.voice.manager.ListDevices(); err != nil {
					m.voiceView.notice = err.Error()
				} else {
					m.voiceView.notice = "Refreshing audio devices…"
				}
			}
			return m, "", nil
		}
		deviceID := strings.TrimSpace(args[1])
		if deviceID == "" {
			m.voiceView.notice = "Audio device id cannot be empty; use /voice input or /voice output to list devices."
			return m, "", nil
		}
		if len(m.voice.devices) > 0 && !voiceDeviceKnown(m.voice.devices, deviceID) {
			m.voiceView.notice = fmt.Sprintf("Unknown audio device %q; refresh the Devices tab first.", deviceID)
			return m, "", nil
		}
		if arg == "input" {
			m.voice.config.InputDevice = deviceID
		} else {
			m.voice.config.OutputDevice = deviceID
		}
	case "models":
		m = m.openVoiceView(voiceViewSetup)
		m.voiceView.notice = "Pinned model details are shown on the Setup tab."
		return m, "", nil
	case "doctor":
		return m.openVoiceView(voiceViewDoctor), "", nil
	case "setup":
		m = m.openVoiceView(voiceViewSetup)
		if m.voice.setupCancel != nil {
			m.voiceView.notice = "Voice setup is already running."
			return m, "", nil
		}
		m.voice.setupStatus = "Preparing voice pack…"
		m.voiceView.notice = "Setup started."
		return m, "", m.voiceSetupCmd()
	default:
		m = m.openVoiceView(voiceViewSettings)
		m.voiceView.notice = "Unknown /voice command. Use the Settings, Setup, Devices, and Doctor tabs."
		return m, "", nil
	}
	m = m.openVoiceView(voiceViewSettings)
	if err := m.persistVoiceConfig(); err != nil {
		m.voiceView.notice = err.Error()
	} else {
		m.voiceView.notice = "Voice settings saved."
	}
	return m, "", nil
}

func voiceDeviceKnown(devices []voicepkg.Device, id string) bool {
	for _, device := range devices {
		if device.ID == id {
			return true
		}
	}
	return false
}

func (m model) voiceDoctorText() string {
	path := voicepkg.DiscoverHelperPath()
	status := "missing"
	if _, err := os.Stat(path); err == nil {
		status = "installed"
	}
	root, _ := os.UserHomeDir()
	manifest := filepath.Join(root, ".atcode", "voice", "manifest.json")
	manifestStatus := "missing"
	if _, err := os.Stat(manifest); err == nil {
		manifestStatus = "present"
	}
	return fmt.Sprintf("Voice doctor\nhelper: %s (%s)\nmanifest: %s (%s)\nplatform: %s/%s\nATCODE_VOICE_DISABLE: %s\nIf microphone access fails, grant @code permission in the OS privacy settings and retry /voice input or /voice output.", path, status, manifest, manifestStatus, runtimeGOOS(), runtimeGOARCH(), os.Getenv("ATCODE_VOICE_DISABLE"))
}

func (m model) voiceSpeakDelta(utteranceID, language, delta string) {
	if m.voice == nil || m.voice.manager == nil || delta == "" {
		return
	}
	v := m.voice
	v.mu.Lock()
	defer v.mu.Unlock()
	if v.utteranceID != utteranceID {
		return
	}
	for _, chunk := range v.chunker.Push(delta) {
		_ = v.manager.EnqueueTTS(utteranceID, chunk, language)
	}
}

func (m model) voiceLanguage() string {
	if m.voice == nil {
		return "auto"
	}
	if language := strings.TrimSpace(m.voice.currentResponseLanguage()); language != "" {
		return language
	}
	return "auto"
}

func (m model) voiceFlushSpeech(utteranceID, language string) {
	if m.voice == nil || m.voice.manager == nil {
		return
	}
	v := m.voice
	v.mu.Lock()
	defer v.mu.Unlock()
	if v.utteranceID != utteranceID {
		return
	}
	for _, chunk := range v.chunker.Flush() {
		_ = v.manager.EnqueueTTS(utteranceID, chunk, language)
	}
}

func runtimeGOOS() string   { return runtime.GOOS }
func runtimeGOARCH() string { return runtime.GOARCH }
