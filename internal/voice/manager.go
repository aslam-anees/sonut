package voice

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/aistatine/sonut/internal/config"
)

var (
	ErrVoiceDisabled     = errors.New("voice is disabled")
	ErrHelperUnavailable = errors.New("atcode-voice helper is not installed")
)

type EventType string

const (
	EventReady       EventType = "ready"
	EventState       EventType = "state"
	EventDevices     EventType = "devices"
	EventSpeechStart EventType = "vad.speech_start"
	EventSpeechEnd   EventType = "vad.speech_end"
	EventASRPartial  EventType = "asr.partial"
	EventASRFinal    EventType = "asr.final"
	EventTTSStarted  EventType = "tts.started"
	EventTTSDone     EventType = "tts.done"
	EventMetric      EventType = "metric"
	EventError       EventType = "error"
	EventHelperExit  EventType = "helper.exit"
)

type Event struct {
	Type        EventType
	RequestID   string
	UtteranceID string
	Text        string
	Language    string
	State       string
	Devices     []Device
	Metrics     map[string]any
	Error       error
}

type ManagerOptions struct {
	Config     config.VoiceConfig
	HelperPath string
	OnEvent    func(Event)
	Stderr     io.Writer
}

// Manager owns only the helper process and protocol. Audio sessions and model
// lifetimes remain inside atcode-voice, so the Go process stays CGO-free.
type Manager struct {
	options ManagerOptions
	ctx     context.Context
	cancel  context.CancelFunc

	mu       sync.Mutex
	writeMu  sync.Mutex
	cmd      *exec.Cmd
	stdin    io.WriteCloser
	started  bool
	closing  bool
	restarts int
	request  atomic.Uint64
}

func NewManager(options ManagerOptions) *Manager {
	options.Config = options.Config.Effective()
	return &Manager{options: options}
}

func (m *Manager) Start(parent context.Context) error {
	if os.Getenv("ATCODE_VOICE_DISABLE") == "1" || !m.options.Config.Enabled {
		return ErrVoiceDisabled
	}
	if strings.TrimSpace(m.options.HelperPath) == "" {
		m.options.HelperPath = DiscoverHelperPath()
	}
	if m.options.HelperPath == "" {
		return ErrHelperUnavailable
	}
	if _, err := os.Stat(m.options.HelperPath); err != nil {
		if os.IsNotExist(err) {
			return ErrHelperUnavailable
		}
		return fmt.Errorf("voice helper: %w", err)
	}
	m.mu.Lock()
	if m.started {
		m.mu.Unlock()
		return nil
	}
	m.ctx, m.cancel = context.WithCancel(parent)
	m.started = true
	m.closing = false
	m.mu.Unlock()
	if err := m.startProcess(); err != nil {
		m.mu.Lock()
		m.started = false
		m.mu.Unlock()
		return err
	}
	// Complete the handshake here so a restarted helper receives the same
	// configuration as a fresh helper. The TUI only treats the resulting ready
	// event as usable; a failed write is surfaced by the next command/event.
	if err := m.Hello(); err != nil {
		return err
	}
	if err := m.Configure(); err != nil {
		return err
	}
	return nil
}

func DiscoverHelperPath() string {
	if explicit := strings.TrimSpace(os.Getenv("ATCODE_VOICE_HELPER")); explicit != "" {
		return explicit
	}
	base, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	name := "atcode-voice"
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	userPath := filepath.Join(base, ".atcode", "voice", "bin", name)
	if _, err := os.Stat(userPath); err == nil {
		return userPath
	}
	if executable, err := os.Executable(); err == nil {
		adjacent := filepath.Join(filepath.Dir(executable), name)
		if _, err := os.Stat(adjacent); err == nil {
			return adjacent
		}
	}
	return userPath
}

func (m *Manager) startProcess() error {
	m.mu.Lock()
	if m.closing || !m.started {
		m.mu.Unlock()
		return context.Canceled
	}
	if m.cmd != nil {
		m.mu.Unlock()
		return nil
	}
	ctx := m.ctx
	path := m.options.HelperPath
	m.mu.Unlock()

	cmd := exec.CommandContext(ctx, path)
	if os.Getenv("ATCODE_VOICE_BACKEND") == "" {
		cmd.Env = append(os.Environ(), "ATCODE_VOICE_BACKEND="+SelectedBackend())
	}
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		_ = stdin.Close()
		return err
	}
	if m.options.Stderr != nil {
		cmd.Stderr = m.options.Stderr
	}
	if err := cmd.Start(); err != nil {
		_ = stdin.Close()
		return fmt.Errorf("start voice helper: %w", err)
	}
	m.mu.Lock()
	if m.closing {
		m.mu.Unlock()
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		return context.Canceled
	}
	m.cmd = cmd
	m.stdin = stdin
	m.mu.Unlock()

	go m.readProcess(stdout)
	go m.waitProcess(cmd)
	return nil
}

func (m *Manager) readProcess(stdout io.ReadCloser) {
	scanner := bufio.NewScanner(stdout)
	scanner.Buffer(make([]byte, 4096), MaxProtocolLine)
	for scanner.Scan() {
		message, err := DecodeMessage(scanner.Bytes())
		if err != nil {
			m.emit(Event{Type: EventError, Error: fmt.Errorf("voice helper protocol: %w", err)})
			continue
		}
		m.handleMessage(message)
	}
	if err := scanner.Err(); err != nil && !errors.Is(err, os.ErrClosed) {
		m.emit(Event{Type: EventError, Error: fmt.Errorf("voice helper output: %w", err)})
	}
	_ = stdout.Close()
}

func (m *Manager) waitProcess(cmd *exec.Cmd) {
	err := cmd.Wait()
	m.mu.Lock()
	if m.cmd != cmd {
		m.mu.Unlock()
		return
	}
	m.cmd = nil
	if m.stdin != nil {
		_ = m.stdin.Close()
	}
	m.stdin = nil
	closing := m.closing
	shouldRestart := !closing && m.restarts == 0
	if shouldRestart {
		m.restarts++
	}
	m.mu.Unlock()
	if closing {
		return
	}
	m.emit(Event{Type: EventHelperExit, Error: fmt.Errorf("voice helper exited: %w", err)})
	if shouldRestart {
		select {
		case <-time.After(250 * time.Millisecond):
		case <-m.ctx.Done():
			return
		}
		if err := m.startProcess(); err != nil {
			m.emit(Event{Type: EventError, Error: fmt.Errorf("voice helper restart failed: %w", err)})
		} else {
			if err := m.Hello(); err != nil {
				m.emit(Event{Type: EventError, Error: fmt.Errorf("voice helper restart handshake failed: %w", err)})
			}
			if err := m.Configure(); err != nil {
				m.emit(Event{Type: EventError, Error: fmt.Errorf("voice helper restart configuration failed: %w", err)})
			}
		}
	} else {
		m.emit(Event{Type: EventError, Error: errors.New("voice helper stopped after one restart; run /voice doctor")})
	}
}

func (m *Manager) handleMessage(message Message) {
	event := Event{
		Type: EventType(message.Type), RequestID: message.RequestID, UtteranceID: message.UtteranceID,
		Text: message.Text, Language: message.Language, State: message.State,
		Devices: message.Devices, Metrics: message.Metrics,
	}
	if message.Error != "" {
		event.Error = errors.New(message.Error)
	}
	if event.Type == EventError && event.Error == nil {
		event.Error = errors.New("voice helper error")
	}
	m.emit(event)
}

func (m *Manager) emit(event Event) {
	if m.options.OnEvent != nil {
		m.options.OnEvent(event)
	}
}

func (m *Manager) nextRequestID() string {
	return "r-" + strconv.FormatUint(m.request.Add(1), 10)
}

func (m *Manager) send(message Message) error {
	m.writeMu.Lock()
	defer m.writeMu.Unlock()
	m.mu.Lock()
	stdin := m.stdin
	closing := m.closing
	m.mu.Unlock()
	if closing || stdin == nil {
		return ErrHelperUnavailable
	}
	return EncodeMessage(stdin, message)
}

func (m *Manager) Configure() error {
	cfg := m.CurrentConfig()
	return m.send(Message{Type: "configure", RequestID: m.nextRequestID(), Profile: cfg.ModelProfile,
		Language: cfg.Language, Voice: cfg.ResponseVoice, Speed: cfg.Speed,
		InputDevice: cfg.InputDevice, OutputDevice: cfg.OutputDevice, SilenceMs: cfg.SilenceMs})
}

// Reload asks a warm helper to load newly installed models without creating a
// second process. It is used when /voice setup completes while @code is already
// running; the helper remains the single owner of native sessions.
func (m *Manager) Reload() error {
	return m.send(Message{Type: "models.reload", RequestID: m.nextRequestID()})
}

func (m *Manager) Hello() error {
	return m.send(Message{Type: "hello", RequestID: m.nextRequestID()})
}

func (m *Manager) StartListening(utteranceID string) error {
	return m.send(Message{Type: "listen.start", RequestID: m.nextRequestID(), UtteranceID: utteranceID})
}

func (m *Manager) CancelListening(utteranceID string) error {
	return m.send(Message{Type: "listen.cancel", RequestID: m.nextRequestID(), UtteranceID: utteranceID})
}

func (m *Manager) EnqueueTTS(utteranceID, text, language string) error {
	cfg := m.CurrentConfig()
	return m.send(Message{Type: "tts.enqueue", RequestID: m.nextRequestID(), UtteranceID: utteranceID,
		Text: text, Language: language, Voice: cfg.ResponseVoice, Speed: cfg.Speed})
}

func (m *Manager) CancelTTS(utteranceID string) error {
	return m.send(Message{Type: "tts.cancel", RequestID: m.nextRequestID(), UtteranceID: utteranceID})
}

func (m *Manager) ListDevices() error {
	return m.send(Message{Type: "devices.list", RequestID: m.nextRequestID()})
}

func (m *Manager) Ping() error { return m.send(Message{Type: "ping", RequestID: m.nextRequestID()}) }

func (m *Manager) Close() {
	m.mu.Lock()
	if !m.started || m.closing {
		m.mu.Unlock()
		return
	}
	m.closing = true
	cancel := m.cancel
	stdin := m.stdin
	cmd := m.cmd
	m.mu.Unlock()
	if stdin != nil {
		m.writeMu.Lock()
		_ = EncodeMessage(stdin, Message{Type: "shutdown", RequestID: m.nextRequestID()})
		m.writeMu.Unlock()
	}
	if cancel != nil {
		cancel()
	}
	if cmd != nil && cmd.Process != nil {
		_ = cmd.Process.Kill()
	}
	m.mu.Lock()
	m.started = false
	m.mu.Unlock()
}

func (m *Manager) Started() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.started && !m.closing
}

func (m *Manager) SetConfig(cfg config.VoiceConfig) {
	m.mu.Lock()
	m.options.Config = cfg.Effective()
	started := m.started && !m.closing
	m.mu.Unlock()
	if started {
		_ = m.Configure()
	}
}

func (m *Manager) CurrentConfig() config.VoiceConfig {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.options.Config.Effective()
}
