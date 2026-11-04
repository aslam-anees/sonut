package voice

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/aistatine/sonut/internal/config"
)

func writeFakeVoiceHelper(t *testing.T, crashOnPing bool) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "atcode-voice")
	crash := ""
	if crashOnPing {
		crash = `if echo "$line" | grep -q '"type":"ping"'; then exit 7; fi`
	}
	script := `#!/bin/sh
while IFS= read -r line; do
  ` + crash + `
  if echo "$line" | grep -q '"type":"hello"'; then
    echo '{"version":1,"type":"ready","requestId":"hello"}'
  elif echo "$line" | grep -q '"type":"configure"'; then
    echo '{"version":1,"type":"state","state":"idle"}'
  elif echo "$line" | grep -q '"type":"listen.start"'; then
    echo '{"version":1,"type":"asr.partial","utteranceId":"u1","text":"hello"}'
    echo '{"version":1,"type":"asr.final","utteranceId":"u1","text":"hello world","language":"en"}'
  elif echo "$line" | grep -q '"type":"tts.enqueue"'; then
    echo '{"version":1,"type":"tts.started","utteranceId":"u1"}'
    echo '{"version":1,"type":"tts.done","utteranceId":"u1"}'
  elif echo "$line" | grep -q '"type":"devices.list"'; then
    echo '{"version":1,"type":"devices","devices":[{"id":"default","name":"Default","direction":"input,output"}]}'
  elif echo "$line" | grep -q '"type":"shutdown"'; then
    exit 0
  fi
done
`
	if err := os.WriteFile(path, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	return path
}

func waitVoiceEvent(t *testing.T, events <-chan Event, want EventType, timeout time.Duration) Event {
	t.Helper()
	deadline := time.NewTimer(timeout)
	defer deadline.Stop()
	for {
		select {
		case event := <-events:
			if event.Type == want {
				return event
			}
		case <-deadline.C:
			t.Fatalf("timed out waiting for %s", want)
			return Event{}
		}
	}
}

func TestManagerFakeHelperHandshakeStreamingAndTTS(t *testing.T) {
	events := make(chan Event, 32)
	m := NewManager(ManagerOptions{
		Config:     config.DefaultVoiceConfig(),
		HelperPath: writeFakeVoiceHelper(t, false),
		OnEvent:    func(event Event) { events <- event },
	})
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := m.Start(ctx); err != nil {
		t.Fatal(err)
	}
	defer m.Close()
	waitVoiceEvent(t, events, EventReady, time.Second)
	if err := m.StartListening("u1"); err != nil {
		t.Fatal(err)
	}
	partial := waitVoiceEvent(t, events, EventASRPartial, time.Second)
	if partial.UtteranceID != "u1" || partial.Text != "hello" {
		t.Fatalf("partial = %+v", partial)
	}
	final := waitVoiceEvent(t, events, EventASRFinal, time.Second)
	if final.UtteranceID != "u1" || final.Language != "en" || final.Text != "hello world" {
		t.Fatalf("final = %+v", final)
	}
	if err := m.EnqueueTTS("u1", "Hello.", "en"); err != nil {
		t.Fatal(err)
	}
	waitVoiceEvent(t, events, EventTTSStarted, time.Second)
	waitVoiceEvent(t, events, EventTTSDone, time.Second)
	if err := m.ListDevices(); err != nil {
		t.Fatal(err)
	}
	devices := waitVoiceEvent(t, events, EventDevices, time.Second)
	if len(devices.Devices) != 1 || devices.Devices[0].ID != "default" {
		t.Fatalf("devices = %+v", devices.Devices)
	}
}

func TestManagerRestartsHelperOnceAndRehandshakes(t *testing.T) {
	events := make(chan Event, 32)
	m := NewManager(ManagerOptions{
		Config:     config.DefaultVoiceConfig(),
		HelperPath: writeFakeVoiceHelper(t, true),
		OnEvent:    func(event Event) { events <- event },
	})
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := m.Start(ctx); err != nil {
		t.Fatal(err)
	}
	defer m.Close()
	waitVoiceEvent(t, events, EventReady, time.Second)
	if err := m.Ping(); err != nil {
		t.Fatal(err)
	}
	waitVoiceEvent(t, events, EventHelperExit, time.Second)
	waitVoiceEvent(t, events, EventReady, 2*time.Second)
}

func TestManagerRejectsMalformedHelperOutput(t *testing.T) {
	path := filepath.Join(t.TempDir(), "atcode-voice")
	contents := `#!/bin/sh
echo not-json
sleep 1
`
	if err := os.WriteFile(path, []byte(contents), 0o700); err != nil {
		t.Fatal(err)
	}
	events := make(chan Event, 8)
	m := NewManager(ManagerOptions{Config: config.DefaultVoiceConfig(), HelperPath: path, OnEvent: func(event Event) { events <- event }})
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := m.Start(ctx); err != nil {
		t.Fatal(err)
	}
	defer m.Close()
	deadline := time.After(time.Second)
	for {
		select {
		case event := <-events:
			if event.Type == EventError && strings.Contains(event.Error.Error(), "protocol") {
				return
			}
		case <-deadline:
			t.Fatal("malformed helper output was not reported")
		}
	}
}
