package tui

import (
	"fmt"
	"strings"

	tea "charm.land/bubbletea/v2"
	"github.com/aistatine/sonut/internal/tools"
	"github.com/aistatine/sonut/internal/webmirror"
	"github.com/mdp/qrterminal/v3"
)

// webPromptMsg carries a prompt typed on a remote device (phone browser) into
// the Bubble Tea loop, delivered via the runtime message sink so it lands on
// the Update goroutine like any other message.
type webPromptMsg struct{ text string }

// webStopMsg is a remote device pressing Stop: cancel the in-flight run, the
// same effect as Esc in the terminal.
type webStopMsg struct{}

// mirrorDetailCap bounds how much raw tool output each snapshot ships per
// row; phones don't need megabyte diffs and full snapshots are re-sent on
// every change.
const mirrorDetailCap = 4000

// handleWebCommand implements /share [on|off]: it starts or stops the LAN
// mirror server and drops a status card with the URL to open on the phone.
func (m model) handleWebCommand(arg string) model {
	want := m.webMirror == nil // bare /share toggles
	switch strings.ToLower(strings.TrimSpace(arg)) {
	case "on", "1", "true":
		want = true
	case "off", "0", "false":
		want = false
	case "":
	default:
		m.transcript = reduceTranscript(m.transcript, transcriptAction{
			kind: actionAppendSystem,
			text: "Web mirror\nstatus: warning\nUsage: /share [on|off]",
		})
		return m
	}
	switch {
	case want && m.webMirror != nil:
		m.transcript = reduceTranscript(m.transcript, transcriptAction{
			kind: actionAppendSystem,
			text: "Web mirror\nweb: on\nAlready serving — scan to join:\n\n" + qrCodeLines(m.webMirror.URL()) +
				"\nor open " + m.webMirror.URL(),
		})
	case want:
		sink := m.runtimeMessageSink
		if sink == nil {
			m.transcript = reduceTranscript(m.transcript, transcriptAction{
				kind: actionAppendSystem,
				text: "Web mirror\nstatus: error\nThis session cannot receive remote input (no runtime message sink).",
			})
			return m
		}
		commands := make([]webmirror.Command, 0, len(commandDefinitions))
		for _, def := range commandDefinitions {
			commands = append(commands, webmirror.Command{
				Name: def.name, Usage: def.usage, Description: def.description,
			})
		}
		server, err := webmirror.Start(webmirror.Options{
			OnPrompt: func(text string) { sink(webPromptMsg{text: text}) },
			OnStop:   func() { sink(webStopMsg{}) },
			Commands: commands,
		})
		if err != nil {
			m.transcript = reduceTranscript(m.transcript, transcriptAction{
				kind: actionAppendError,
				text: "web mirror: " + err.Error(),
			})
			return m
		}
		m.webMirror = server
		m.transcript = reduceTranscript(m.transcript, transcriptAction{
			kind: actionAppendSystem,
			text: "Web mirror\nweb: on\nScan on your phone (same Wi-Fi):\n\n" + qrCodeLines(server.URL()) +
				"\nor open " + server.URL() +
				"\nThe link includes a private token — anyone with it can read this session and send prompts. /share off to stop.",
		})
	case m.webMirror != nil:
		m.webMirror.Close()
		m.webMirror = nil
		m.transcript = reduceTranscript(m.transcript, transcriptAction{
			kind: actionAppendSystem,
			text: "Web mirror\nweb: off\nRemote devices have been disconnected.",
		})
	default:
		m.transcript = reduceTranscript(m.transcript, transcriptAction{
			kind: actionAppendSystem,
			text: "Web mirror\nweb: off",
		})
	}
	return m
}

// handleWebPrompt routes a remote prompt through the SAME guards and dispatch
// as locally typed input (see handleSubmit): mid-run prompts queue, and slash
// commands work from the phone too. The composer is deliberately untouched —
// the laptop user may be mid-keystroke.
func (m model) handleWebPrompt(text string) (model, tea.Cmd) {
	command := parseCommand(text)
	if command.kind == commandEmpty {
		return m, nil
	}
	if command.kind == commandPrompt {
		if m.exiting {
			return m, nil
		}
		if m.pending {
			return m.queueMessage(command.text), nil
		}
		if m.compactInFlight {
			m.transcript = reduceTranscript(m.transcript, transcriptAction{
				kind: actionAppendSystem,
				text: "Compact\nstatus: warning\nCompaction is running. Your next prompt will use the compacted context when this finishes.",
			})
			return m, nil
		}
	}
	m.chatScrollOffset = 0
	next, cmd := m.executeCommand(command)
	return next.(model), cmd
}

// publishWebMirror pushes the current transcript to connected devices when it
// changed. Called from View (which runs after every Update), so the mirror
// tracks the terminal frame-for-frame; the fingerprint check keeps the
// per-frame cost to a few string ops when nothing moved.
func (m model) publishWebMirror() {
	if m.webMirror == nil {
		return
	}
	last := ""
	if n := len(m.transcript); n > 0 {
		row := m.transcript[n-1]
		last = fmt.Sprintf("%d:%d:%d:%s", row.kind, len(row.text), len(row.detail), row.status)
	}
	fingerprint := fmt.Sprintf("%d|%s|%d|%d|%d|%t",
		len(m.transcript), last, len(m.streamingText), len(m.streamingReasoning), m.activeRunID, m.pending)
	m.webMirror.MaybePublish(fingerprint, m.pending, func() []webmirror.Event { return m.mirrorEvents() })
}

// mirrorEvents converts the transcript (plus any live streaming segment) into
// the device-facing snapshot.
func (m model) mirrorEvents() []webmirror.Event {
	events := make([]webmirror.Event, 0, len(m.transcript)+2)
	for _, row := range m.transcript {
		switch row.kind {
		case rowUser:
			events = append(events, webmirror.Event{Kind: "user", Text: row.text})
		case rowAssistant:
			events = append(events, webmirror.Event{Kind: "assistant", Text: row.text})
		case rowReasoning:
			// Private reasoning never crosses the mirror boundary. The local TUI
			// uses a neutral activity status while the model reasons internally.
			continue
		case rowToolCall:
			events = append(events, webmirror.Event{Kind: "tool", Tool: row.tool, Text: row.arg, Status: "running"})
		case rowToolResult:
			status := "ok"
			if row.status == tools.StatusError {
				status = "error"
			}
			text := row.text
			if text == "" {
				text = truncateMirrorDetail(row.detail)
			}
			events = append(events, webmirror.Event{Kind: "tool", Tool: row.tool, Text: text, Status: status})
		case rowSystem:
			events = append(events, webmirror.Event{Kind: "system", Text: row.text})
		case rowError:
			events = append(events, webmirror.Event{Kind: "error", Text: row.text})
		case rowAskUser:
			events = append(events, webmirror.Event{Kind: "ask", Text: row.text})
		case rowPermission:
			if row.permission != nil {
				events = append(events, webmirror.Event{
					Kind: "system",
					Text: "Permission requested (" + row.permission.ToolName + ") — respond in the terminal.",
				})
			}
		}
	}
	if m.streamingText != "" {
		events = append(events, webmirror.Event{Kind: "assistant", Text: m.streamingText, Live: true})
	}
	return events
}

// webNotice pushes an attention signal (run done / input needed) to connected
// devices; no-op when the mirror is off.
func (m model) webNotice(kind, text string) {
	if m.webMirror != nil {
		m.webMirror.Notify(kind, text)
	}
}

// qrCodeLines renders the mirror URL as a scannable half-block QR code for
// the status card, so joining from a phone is one camera scan instead of
// typing a tokenized URL.
func qrCodeLines(url string) string {
	var b strings.Builder
	qrterminal.GenerateWithConfig(url, qrterminal.Config{
		Level:          qrterminal.L, // lowest density — biggest modules, easiest scan from a terminal
		Writer:         &b,
		HalfBlocks:     true,
		BlackChar:      qrterminal.BLACK_BLACK,
		WhiteChar:      qrterminal.WHITE_WHITE,
		BlackWhiteChar: qrterminal.BLACK_WHITE,
		WhiteBlackChar: qrterminal.WHITE_BLACK,
		QuietZone:      1,
	})
	return b.String()
}

func truncateMirrorDetail(detail string) string {
	if len(detail) <= mirrorDetailCap {
		return detail
	}
	return detail[:mirrorDetailCap] + "\n… (truncated)"
}
