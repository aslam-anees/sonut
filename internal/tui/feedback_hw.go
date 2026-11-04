package tui

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"unicode/utf8"

	tea "charm.land/bubbletea/v2"
	userfeedback "github.com/aistatine/sonut/internal/feedback"
	"github.com/aistatine/sonut/internal/llama"
)

// feedbackText renders the /feedback card.
func feedbackText() string {
	return strings.Join([]string{
		"Feedback",
		"Type your feedback after the command and press Enter:",
		"",
		"  /feedback tell us what would make @code better",
		"",
		"Your message is saved securely to your signed-in Supabase account.",
	}, "\n")
}

func (m model) startFeedbackCommand(message string) (model, tea.Cmd) {
	message = strings.TrimSpace(message)
	if message == "" {
		m.transcript = reduceTranscript(m.transcript, transcriptAction{kind: actionAppendSystem, text: feedbackText()})
		return m, nil
	}
	if utf8.RuneCountInString(message) > userfeedback.MaxMessageLength {
		m.transcript = reduceTranscript(m.transcript, transcriptAction{
			kind: actionAppendError,
			text: fmt.Sprintf("Feedback is limited to %d characters.", userfeedback.MaxMessageLength),
		})
		return m, nil
	}

	m.transcript = reduceTranscript(m.transcript, transcriptAction{
		kind: actionAppendSystem,
		text: "Feedback\nstatus: sending…",
	})
	submit := m.submitFeedback
	ctx := m.ctx
	return m, func() tea.Msg {
		if submit == nil {
			return feedbackSubmittedMsg{err: errors.New("feedback service is unavailable")}
		}
		return feedbackSubmittedMsg{err: submit(nonNilFeedbackContext(ctx), message)}
	}
}

func feedbackResultText(err error) string {
	switch {
	case err == nil:
		return "Feedback\nstatus: received\nJazakAllahu khairan — your feedback has been saved."
	case errors.Is(err, userfeedback.ErrNotSignedIn), errors.Is(err, userfeedback.ErrSessionExpired):
		return "Feedback\nstatus: not sent\nPlease sign in again, then retry /feedback."
	default:
		return "Feedback\nstatus: not sent\nCould not reach the feedback service. Please try again."
	}
}

func nonNilFeedbackContext(ctx context.Context) context.Context {
	if ctx == nil {
		return context.Background()
	}
	return ctx
}

// hardwareRefreshText re-runs hardware detection (CUDA / Apple Silicon / CPU),
// refreshes the cached tuning in ~/.atcode/hardware.json, and reports it.
// Run /hw after changing hardware (new GPU, new machine) so llama-server
// launches with the best flags next start.
func hardwareRefreshText() string {
	m, err := llama.DetectAndSaveHardwareMetrics()
	if err != nil {
		return "Hardware\nstatus: error\nCould not refresh hardware profile: " + err.Error()
	}
	gpu := "none (CPU inference)"
	if m.HasGPU {
		gpu = m.GPUType
		if m.GPUMemoryGiB != "" {
			gpu += " (" + m.GPUMemoryGiB + " GiB)"
		}
	}
	if m.IsAppleSilicon {
		gpu += " · Apple Silicon (Metal)"
	}
	lines := []string{
		"Hardware profile refreshed",
		fmt.Sprintf("OS/Arch: %s/%s", m.OS, m.Arch),
		fmt.Sprintf("CPU cores: %d · RAM: %s GiB", m.CPUCores, m.RAMGiB),
		"GPU: " + gpu,
		fmt.Sprintf("Tuning: ngl=%d · context=%d · batch=%d · threads=%d",
			m.RecommendedNGL, m.RecommendedContextSize, m.RecommendedBatchSize, m.RecommendedThreads),
		"",
		"Saved to ~/.atcode/hardware.json — applied on the next model-server start (or /memory to hot-restart).",
	}
	return strings.Join(lines, "\n")
}
