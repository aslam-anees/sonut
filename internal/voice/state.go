package voice

import (
	"fmt"
	"time"
)

// State is the public lifecycle state shown by the TUI. The native helper has
// its own state, but it never mutates this value directly.
type State string

const (
	StateDisabled     State = "disabled"
	StateUninstalled  State = "uninstalled"
	StateIdle         State = "idle"
	StateArming       State = "arming"
	StateListening    State = "listening"
	StateFinalizing   State = "finalizing"
	StateSubmitting   State = "submitting"
	StateThinking     State = "thinking"
	StateSpeaking     State = "speaking"
	StateConfirmBuild State = "confirm_build"
	StateBuilding     State = "building"
	StateError        State = "error"
)

func (s State) String() string { return string(s) }

// EndpointConfig keeps endpointing values named and testable. SilenceMs is
// deliberately separate from the no-speech timeout: silence ends an active
// utterance, while no-speech timeout ends a listening session with no speech.
type EndpointConfig struct {
	PreRollMs       int
	MinSpeechMs     int
	SilenceMs       int
	NoSpeechTimeout time.Duration
	MaxUtterance    time.Duration
}

func DefaultEndpointConfig() EndpointConfig {
	return EndpointConfig{
		PreRollMs:       300,
		MinSpeechMs:     250,
		SilenceMs:       3000,
		NoSpeechTimeout: 10 * time.Second,
		MaxUtterance:    120 * time.Second,
	}
}

func (c EndpointConfig) Validate() error {
	if c.PreRollMs < 0 || c.PreRollMs > 2000 {
		return fmt.Errorf("voice pre-roll must be 0..2000 ms")
	}
	if c.MinSpeechMs < 50 || c.MinSpeechMs > 5000 {
		return fmt.Errorf("voice minimum speech must be 50..5000 ms")
	}
	if c.SilenceMs < 500 || c.SilenceMs > 30000 {
		return fmt.Errorf("voice silence must be 500..30000 ms")
	}
	if c.NoSpeechTimeout < time.Second || c.NoSpeechTimeout > 5*time.Minute {
		return fmt.Errorf("voice no-speech timeout must be 1s..5m")
	}
	if c.MaxUtterance < 5*time.Second || c.MaxUtterance > 10*time.Minute {
		return fmt.Errorf("voice maximum utterance must be 5s..10m")
	}
	return nil
}

type EndpointEvent string

const (
	EndpointNone          EndpointEvent = ""
	EndpointSpeechStarted EndpointEvent = "speech_started"
	EndpointSpeechEnded   EndpointEvent = "speech_ended"
	EndpointNoSpeech      EndpointEvent = "no_speech"
	EndpointMaximum       EndpointEvent = "maximum_duration"
)

// EndpointTracker is a small deterministic endpointing state machine. The
// native helper uses the same values while Go tests exercise its transitions.
type EndpointTracker struct {
	config       EndpointConfig
	startedAt    time.Time
	speechAt     time.Time
	lastSpeechAt time.Time
	inSpeech     bool
}

func NewEndpointTracker(config EndpointConfig) (*EndpointTracker, error) {
	if err := config.Validate(); err != nil {
		return nil, err
	}
	return &EndpointTracker{config: config}, nil
}

func (t *EndpointTracker) Start(now time.Time) {
	t.startedAt = now
	t.speechAt = time.Time{}
	t.lastSpeechAt = time.Time{}
	t.inSpeech = false
}

func (t *EndpointTracker) SpeechStart(now time.Time) EndpointEvent {
	if t.startedAt.IsZero() {
		t.Start(now)
	}
	if t.speechAt.IsZero() {
		t.speechAt = now
	}
	t.lastSpeechAt = now
	if !t.inSpeech {
		t.inSpeech = true
		return EndpointSpeechStarted
	}
	return EndpointNone
}

func (t *EndpointTracker) SpeechEnd(now time.Time) EndpointEvent {
	if !t.inSpeech {
		return EndpointNone
	}
	t.lastSpeechAt = now
	return EndpointNone
}

func (t *EndpointTracker) Tick(now time.Time) EndpointEvent {
	if t.startedAt.IsZero() {
		return EndpointNone
	}
	if now.Sub(t.startedAt) >= t.config.MaxUtterance {
		return EndpointMaximum
	}
	if !t.inSpeech {
		if now.Sub(t.startedAt) >= t.config.NoSpeechTimeout {
			return EndpointNoSpeech
		}
		return EndpointNone
	}
	if now.Sub(t.speechAt) < time.Duration(t.config.MinSpeechMs)*time.Millisecond {
		return EndpointNone
	}
	if !t.lastSpeechAt.IsZero() && now.Sub(t.lastSpeechAt) >= time.Duration(t.config.SilenceMs)*time.Millisecond {
		t.inSpeech = false
		return EndpointSpeechEnded
	}
	return EndpointNone
}
