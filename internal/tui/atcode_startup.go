package tui

// This file owns the branded @code startup and engine warmup flow.
import (
	"context"
	"fmt"
	"math/rand"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"charm.land/bubbles/v2/spinner"
	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/aistatine/sonut/internal/agent"
	"github.com/aistatine/sonut/internal/auth"
	"github.com/aistatine/sonut/internal/background"
	"github.com/aistatine/sonut/internal/config"
	"github.com/aistatine/sonut/internal/llama"
	"github.com/aistatine/sonut/internal/selfupdate"
)

// startupPhase tracks the current phase of the @code startup flow.
type startupPhase int

const (
	phaseLogin         startupPhase = iota // show login options
	phaseModelCheck                        // checking if model files are present
	phaseUpdateCheck                       // checking for binary/model updates
	phaseUpdatePrompt                      // asking the user to apply available updates
	phaseBinaryUpdate                      // downloading + installing the new @ binary
	phaseModelDownload                     // downloading model from HF
	phaseHardwareSetup                     // detecting hardware capabilities
	phaseToolsSetup                        // installing browser runtime if needed
	phaseLlamaCheck                        // checking llama server
	phaseLlamaStart                        // starting llama server
	phaseDone                              // done — transition to main TUI
	phaseError                             // unrecoverable error
)

// loginMode tracks which login input is active.
type loginMode int

const (
	loginModeChoose  loginMode = iota // showing login options
	loginModeMagic                    // email input for magic link
	loginModeWaiting                  // waiting for browser click (magic link or Google)
)

// startupModel is the Bubble Tea model for the pre-TUI startup screen.
type startupModel struct {
	phase        startupPhase
	loginMode    loginMode
	emailInput   textinput.Model
	spinner      spinner.Model
	width        int
	height       int
	statusMsg    string
	errorMsg     string
	llamaPort    int
	agentOpts    agent.Options // forwarded to real KV warmup
	skip         bool          // user chose to skip login
	waitingLabel string        // shown while waiting for browser (magic link or Google)

	// update state
	binUpdate   *selfupdate.BinaryUpdate
	modelUpdate *selfupdate.ModelUpdate
	updateNote  string // one-line result of a binary update attempt

	// model download state
	downloadTotal   int64        // total bytes expected
	downloadDone    int64        // bytes received so far
	downloadPercent float64      // 0–100
	downloadFile    string       // current filename being downloaded
	downloadTip     string       // rotating tip shown below the bar
	tipIndex        int          // tip rotation counter
	downloadCh      chan tea.Msg // channel from the download goroutine

}

// startup messages
type (
	llamaReadyMsg     struct{}
	llamaStartedMsg   struct{}
	startupErrMsg     struct{ err error }
	skipLoginMsg      struct{}
	googleAuthDoneMsg struct{ session *auth.Session }
	otpVerifiedMsg    struct{ session *auth.Session }

	// update messages
	updateCheckMsg struct {
		bin   *selfupdate.BinaryUpdate
		model *selfupdate.ModelUpdate
	}
	binaryUpdateDoneMsg struct{ err error }

	// model-setup messages
	modelCheckMsg struct {
		mainPath string
		present  bool
	}
	downloadProgressMsg struct {
		done    int64
		total   int64
		percent float64
		file    string
	}
	downloadDoneMsg  struct{}
	hardwareSetupMsg struct{}
	toolsSetupMsg    struct {
		installed bool
		statusMsg string
	}
	tipRotateMsg struct{}

	// in-session llama restart (triggered by /memory command)
	llamaRestartDoneMsg struct {
		ctxSize int
		err     error
	}
)

// downloadTips cycle in the spinner card during model download.
var downloadTips = []string{
	"Use /history to browse all your past conversations",
	"Use /resume to continue any previous session",
	"Use /compact to free context when chats get long",
	"Use /effort super for deep, careful reasoning",
	"Use /btw to ask a quick question mid-task",
	"Use /search to find anything across all sessions",
	"Use /rewind to restore files to a checkpoint",
	"Use /image to share screenshots for visual analysis",
	"Use /spec to draft a plan before making changes",
	"Use /clear to start the transcript fresh",
	"Press ? anytime to see all keyboard shortcuts",
	"Use /tools to list every tool available to the agent",
	"@code runs entirely locally — your code stays private",
	"Use /style for concise, balanced, or explanatory replies",
	"Use /plan to see the current step-by-step task plan",
	"Use /export to save your conversation to a file",
	"Use /commit to create a git commit with AI assistance",
	"Run /voice setup for the optional local multilingual voice pack",
}

// checkExistingSession returns true if a valid saved session exists.
func checkExistingSession() bool {
	home, _ := os.UserHomeDir()
	cfg := config.Load()
	am := auth.NewAuthManager(cfg.SupabaseURL, cfg.SupabaseKey, cfg.GoogleClientID, cfg.GoogleClientSecret, filepath.Join(home, ".atcode"))
	sess, err := am.LoadStoredSession()
	if err != nil || sess == nil {
		return false
	}
	if !sess.IsExpired() {
		return true
	}
	// Startup must never wait on a network refresh. An expired session can be
	// refreshed later from the login flow; the local engine and TUI should become
	// visible immediately instead of spending up to ten seconds here.
	return false
}

func newStartupModel(llamaPort int, agentOpts agent.Options) startupModel {
	sp := spinner.New()
	sp.Spinner = spinner.Dot
	sp.Style = lipgloss.NewStyle().Foreground(lipgloss.Color("#4d9fff"))

	ei := textinput.New()
	ei.Placeholder = "you@example.com"
	ei.CharLimit = 200

	tip := downloadTips[rand.Intn(len(downloadTips))]

	return startupModel{
		phase:       phaseLogin,
		loginMode:   loginModeChoose,
		spinner:     sp,
		emailInput:  ei,
		llamaPort:   llamaPort,
		agentOpts:   agentOpts,
		downloadTip: tip,
	}
}

func (m startupModel) Init() tea.Cmd {
	// Hosted authentication is optional. The source release ships no service
	// credentials, and local inference must not wait behind an unusable login.
	cfg := config.Load()
	if cfg.SupabaseURL == "" || cfg.SupabaseKey == "" || checkExistingSession() {
		return func() tea.Msg { return skipLoginMsg{} }
	}
	return m.spinner.Tick
}

func (m startupModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		return m, nil

	case spinner.TickMsg:
		var cmd tea.Cmd
		m.spinner, cmd = m.spinner.Update(msg)
		return m, cmd

	case tea.KeyMsg:
		return m.handleKey(msg)

	// --- Auth completion → model check ---
	case googleAuthDoneMsg:
		cfg := config.Load()
		am := auth.NewAuthManager(cfg.SupabaseURL, cfg.SupabaseKey, cfg.GoogleClientID, cfg.GoogleClientSecret, authConfigDir())
		if msg.session != nil {
			_ = am.SaveSession(msg.session)
		}
		m.phase = phaseModelCheck
		m.statusMsg = "Checking model files…"
		return m, tea.Batch(m.spinner.Tick, checkModelCmd())

	case otpVerifiedMsg:
		cfg := config.Load()
		am := auth.NewAuthManager(cfg.SupabaseURL, cfg.SupabaseKey, cfg.GoogleClientID, cfg.GoogleClientSecret, authConfigDir())
		if msg.session != nil {
			_ = am.SaveSession(msg.session)
		}
		m.phase = phaseModelCheck
		m.statusMsg = "Checking model files…"
		return m, tea.Batch(m.spinner.Tick, checkModelCmd())

	case skipLoginMsg:
		m.skip = true
		m.phase = phaseModelCheck
		m.statusMsg = "Checking model files…"
		return m, tea.Batch(m.spinner.Tick, checkModelCmd())

	// --- Model presence check result ---
	case modelCheckMsg:
		if msg.present {
			// A model is already present: proceed locally. Network update checks
			// are deliberately opt-in so a normal launch is not held hostage by a
			// slow or unavailable update endpoint.
			if atcodeEnv("STARTUP_UPDATE_CHECK") == "1" {
				m.phase = phaseUpdateCheck
				m.statusMsg = "Checking for updates…"
				return m, tea.Batch(m.spinner.Tick, updateCheckCmd())
			}
			return m.proceedToHardware()
		}
		return m.startModelDownload()

	// --- Update check result ---
	case updateCheckMsg:
		m.binUpdate = msg.bin
		m.modelUpdate = msg.model
		if m.binUpdate != nil || m.modelUpdate != nil {
			m.phase = phaseUpdatePrompt
			return m, m.spinner.Tick
		}
		return m.proceedToHardware()

	case binaryUpdateDoneMsg:
		if msg.err != nil {
			m.updateNote = "Binary update failed: " + msg.err.Error() + " — continuing with the current version."
		} else if m.binUpdate != nil {
			m.updateNote = "Updated to v" + m.binUpdate.Latest + " — the new version runs on the next launch."
		}
		m.binUpdate = nil
		return m.afterBinaryUpdate()

	// --- Streaming download progress ---
	case downloadProgressMsg:
		m.downloadDone = msg.done
		m.downloadTotal = msg.total
		m.downloadPercent = msg.percent
		m.downloadFile = msg.file
		// Schedule next read from the channel
		return m, readDownloadMsg(m.downloadCh)

	case downloadDoneMsg:
		m.downloadCh = nil
		m.phase = phaseHardwareSetup
		m.statusMsg = "Detecting hardware…"
		return m, tea.Batch(m.spinner.Tick, detectHardwareCmd())

	// --- Tip rotation during download ---
	case tipRotateMsg:
		if m.phase == phaseModelDownload {
			m.tipIndex++
			m.downloadTip = downloadTips[m.tipIndex%len(downloadTips)]
			return m, tipRotateCmd()
		}
		return m, nil

	// --- Hardware detection done → browser tools setup ---
	case hardwareSetupMsg:
		m.phase = phaseToolsSetup
		m.statusMsg = "Checking browser tools…"
		return m, tea.Batch(m.spinner.Tick, setupToolsCmd())

	// --- Browser tools done → llama check ---
	case toolsSetupMsg:
		if msg.statusMsg != "" {
			m.statusMsg = msg.statusMsg
		}
		m.phase = phaseLlamaCheck
		m.statusMsg = "Checking the atcode engine…"
		return m, tea.Batch(m.spinner.Tick, checkLlamaCmd(m.llamaPort))

	// --- Llama server flow ---
	case llamaReadyMsg:
		// The engine process is running. Hand the terminal to the real TUI without
		// issuing a background completion: llama-server has one inference slot, so
		// startup prefill can become stale work that blocks the first user turn.
		m.phase = phaseDone
		m.statusMsg = "Engine ready"
		return m, tea.Quit

	case llamaStartedMsg:
		m.phase = phaseLlamaStart
		m.statusMsg = "Starting the atcode engine…"
		return m, tea.Batch(m.spinner.Tick, startLlamaCmd(m.llamaPort))

	case startupErrMsg:
		m.phase = phaseError
		m.errorMsg = msg.err.Error()
		return m, nil
	}
	return m, nil
}

// startModelDownload flips into the download screen and launches the
// download goroutine (used for both first-run install and model updates).
func (m startupModel) startModelDownload() (startupModel, tea.Cmd) {
	ch := make(chan tea.Msg, 64)
	m.downloadCh = ch
	m.phase = phaseModelDownload
	m.downloadPercent = 0
	m.downloadDone = 0
	m.downloadTotal = 0
	m.downloadFile = ""
	m.downloadTip = downloadTips[m.tipIndex%len(downloadTips)]
	go runModelDownload(ch)
	return m, tea.Batch(
		m.spinner.Tick,
		readDownloadMsg(ch),
		tipRotateCmd(),
	)
}

// proceedToHardware continues the normal startup chain after update handling.
func (m startupModel) proceedToHardware() (startupModel, tea.Cmd) {
	m.phase = phaseHardwareSetup
	m.statusMsg = "Detecting hardware…"
	return m, tea.Batch(m.spinner.Tick, detectHardwareCmd())
}

// afterBinaryUpdate routes to the model-update prompt if one is pending,
// otherwise continues to hardware detection.
func (m startupModel) afterBinaryUpdate() (startupModel, tea.Cmd) {
	if m.modelUpdate != nil {
		m.phase = phaseUpdatePrompt
		return m, m.spinner.Tick
	}
	return m.proceedToHardware()
}

// updateCheckCmd runs the binary and model update checks concurrently.
// Each check has its own short timeout, so this phase is bounded.
func updateCheckCmd() tea.Cmd {
	return func() tea.Msg {
		phaseStart := time.Now()
		var bin *selfupdate.BinaryUpdate
		var model *selfupdate.ModelUpdate
		done := make(chan struct{}, 2)
		go func() { bin = selfupdate.CheckBinary(); done <- struct{}{} }()
		go func() { model = selfupdate.CheckModels(); done <- struct{}{} }()
		<-done
		<-done
		tracePhase("update-check", time.Since(phaseStart))
		return updateCheckMsg{bin: bin, model: model}
	}
}

// applyBinaryUpdateCmd downloads and installs the new @ binary.
func applyBinaryUpdateCmd(u *selfupdate.BinaryUpdate) tea.Cmd {
	return func() tea.Msg {
		return binaryUpdateDoneMsg{err: selfupdate.ApplyBinary(u)}
	}
}

// readDownloadMsg returns a command that blocks until the next message arrives
// on the download goroutine's channel, then delivers it to Bubble Tea.
func readDownloadMsg(ch chan tea.Msg) tea.Cmd {
	return func() tea.Msg {
		return <-ch
	}
}

// runModelDownload downloads all GGUF files from the @code HF repository,
// streaming progress into ch. Must be called in a goroutine.
func runModelDownload(ch chan tea.Msg) {
	if err := llama.EnsureAtcodeDir(); err != nil {
		ch <- startupErrMsg{err: fmt.Errorf("could not prepare model directory: %w", err)}
		return
	}

	files, err := llama.ListHFModelFiles()
	if err != nil {
		ch <- startupErrMsg{err: fmt.Errorf("could not list model files: %w", err)}
		return
	}
	if len(files) == 0 {
		ch <- startupErrMsg{err: fmt.Errorf("no model files found in repository — check back soon")}
		return
	}
	// The repo must contain a MAIN model, not just support files (mmproj /
	// draft). Otherwise we'd burn a multi-hundred-MB download and still fail
	// later with "no model found" when the engine starts.
	hasMain := false
	for _, f := range files {
		lower := strings.ToLower(f.Filename)
		if !strings.Contains(lower, "mmproj") && !strings.Contains(lower, "dflash") && !strings.Contains(lower, "draft") {
			hasMain = true
			break
		}
	}
	if !hasMain {
		ch <- startupErrMsg{err: fmt.Errorf("model repository has no main model yet (only support files) — check back soon")}
		return
	}

	modelDir := llama.GetModelDir()

	// grandTotal is built lazily from real HTTP Content-Length, because the HF API
	// returns size=0 for LFS-stored GGUF files (pointer file size, not actual size).
	var grandTotal int64
	fileTotals := make([]int64, len(files))
	var grandDone int64

	for i, f := range files {
		localFilename := llama.LocalModelFilename(f.Filename)
		localPath := filepath.Join(modelDir, localFilename)
		// Already fully downloaded (e.g. a model update kept this file, or a
		// previous run finished it): skip instead of re-downloading gigabytes.
		if info, statErr := os.Stat(localPath); statErr == nil && info.Size() > 0 {
			continue
		}
		remoteFilename := f.Filename
		fileDoneAtStart := grandDone
		fileIdx := i

		err := llama.DownloadHFFile(remoteFilename, localPath, func(fileDone, fileSize int64) {
			// Update grand total when we learn the real size from the HTTP response
			if fileSize > 0 && fileTotals[fileIdx] != fileSize {
				grandTotal += fileSize - fileTotals[fileIdx]
				fileTotals[fileIdx] = fileSize
			}
			done := fileDoneAtStart + fileDone
			pct := 0.0
			if grandTotal > 0 {
				pct = float64(done) * 100.0 / float64(grandTotal)
			}
			select {
			case ch <- downloadProgressMsg{
				done:    done,
				total:   grandTotal,
				percent: pct,
				file:    localFilename,
			}:
			default:
			}
		})
		if err != nil {
			ch <- startupErrMsg{err: fmt.Errorf("download failed for %s: %w", localFilename, err)}
			return
		}
		// Advance grandDone by the actual downloaded size (from real Content-Length)
		if fileTotals[i] > 0 {
			grandDone = fileDoneAtStart + fileTotals[i]
		} else {
			grandDone += f.Size
		}
	}

	ch <- downloadDoneMsg{}
}

// tipRotateCmd fires a tip-rotation message after 5 seconds.
func tipRotateCmd() tea.Cmd {
	return tea.Tick(5*time.Second, func(_ time.Time) tea.Msg {
		return tipRotateMsg{}
	})
}

// checkModelCmd checks whether the @code model files exist in ~/.atcode/models/.
func checkModelCmd() tea.Cmd {
	return func() tea.Msg {
		phaseStart := time.Now()
		_ = llama.EnsureAtcodeDir()
		mainPath, _, present := llama.CheckModelsPresent()
		tracePhase("model-check", time.Since(phaseStart))
		return modelCheckMsg{mainPath: mainPath, present: present}
	}
}

// detectHardwareCmd ensures ~/.atcode/hardware.json exists. Detection (CUDA /
// Metal / ROCm probing, sysctl calls) runs only on first setup; every later
// boot reuses the cached profile so startup stays fast and tuning stays
// stable. /hw is the explicit way to re-detect after a hardware change.
func detectHardwareCmd() tea.Cmd {
	return func() tea.Msg {
		phaseStart := time.Now()
		if llama.LoadHardwareMetrics() == nil {
			_, _ = llama.DetectAndSaveHardwareMetrics()
		}
		tracePhase("hardware-detect", time.Since(phaseStart))
		return hardwareSetupMsg{}
	}
}

// setupToolsCmd ensures the agent-browser runtime (Chromium) is available.
// The install/update work runs entirely in a background goroutine — a Chrome
// version bump used to re-download 170+ MB in the foreground and hold the
// whole boot hostage for minutes. Browser tools are ready by the time the
// agent first needs them; if not, the tool surfaces its own error.
func setupToolsCmd() tea.Cmd {
	return func() tea.Msg {
		go installBrowserTools()
		return toolsSetupMsg{installed: true, statusMsg: ""}
	}
}

// installBrowserTools installs the agent-browser binary (via npm) and its
// Chromium runtime if missing. Runs detached from the startup flow; output
// goes to ~/.atcode/browser-install.log.
func installBrowserTools() {
	home, _ := os.UserHomeDir()
	logPath := filepath.Join(home, ".atcode", "browser-install.log")
	openLog := func() *os.File {
		f, _ := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
		return f
	}

	// 1. Check if the agent-browser binary is on PATH.
	bin, err := exec.LookPath("agent-browser")
	if err != nil {
		// Binary not found — try to install via npm.
		npm, npmErr := exec.LookPath("npm")
		if npmErr != nil {
			return // npm not available; agent will surface errors later
		}
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
		defer cancel()
		installCmd := exec.CommandContext(ctx, npm, "install", "-g", "agent-browser")
		if lf := openLog(); lf != nil {
			installCmd.Stdout = lf
			installCmd.Stderr = lf
			defer lf.Close()
		}
		if installErr := installCmd.Run(); installErr != nil {
			return
		}
		bin, err = exec.LookPath("agent-browser")
		if err != nil {
			return
		}
	}

	// 2. Run "agent-browser install" to download Chromium if not cached.
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	runtimeCmd := exec.CommandContext(ctx, bin, "install")
	if lf := openLog(); lf != nil {
		runtimeCmd.Stdout = lf
		runtimeCmd.Stderr = lf
		defer lf.Close()
	}
	_ = runtimeCmd.Run() // ignore error — old/cached install returns non-zero too
}

func (m startupModel) handleKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch m.phase {
	case phaseLogin:
		if m.loginMode == loginModeChoose {
			switch msg.String() {
			case "l", "L":
				return m.Update(skipLoginMsg{})
			case "m", "M":
				m.loginMode = loginModeMagic
				m.emailInput.Focus()
				return m, textinput.Blink
			case "g", "G":
				m.loginMode = loginModeWaiting
				m.waitingLabel = "Google Sign-In"
				m.statusMsg = "Browser opened — complete sign-in there. (Esc to cancel)"
				return m, tea.Batch(m.spinner.Tick, googleLoginCmd())
			case "q", "ctrl+c":
				return m, tea.Quit
			}
		} else if m.loginMode == loginModeMagic {
			switch msg.String() {
			case "enter":
				email := strings.TrimSpace(m.emailInput.Value())
				if email == "" {
					return m, nil
				}
				m.loginMode = loginModeWaiting
				m.waitingLabel = "Magic Link"
				m.statusMsg = fmt.Sprintf("Sending link to %s… check your email and click it.", email)
				m.emailInput.Blur()
				return m, tea.Batch(m.spinner.Tick, sendMagicLinkCmd(email))
			case "esc":
				m.loginMode = loginModeChoose
				m.emailInput.Blur()
				return m, nil
			default:
				var cmd tea.Cmd
				m.emailInput, cmd = m.emailInput.Update(msg)
				return m, cmd
			}
		} else if m.loginMode == loginModeWaiting {
			if msg.String() == "esc" {
				m.loginMode = loginModeChoose
				m.waitingLabel = ""
				m.statusMsg = ""
				return m, nil
			}
		}

	case phaseUpdatePrompt:
		switch msg.String() {
		case "y", "Y", "enter":
			if m.binUpdate != nil {
				m.phase = phaseBinaryUpdate
				m.statusMsg = fmt.Sprintf("Updating @code to v%s…", m.binUpdate.Latest)
				return m, tea.Batch(m.spinner.Tick, applyBinaryUpdateCmd(m.binUpdate))
			}
			if m.modelUpdate != nil {
				// Old models are removed first to free disk, then the new
				// ones stream in through the regular download screen.
				m.modelUpdate.RemoveStale()
				m.modelUpdate = nil
				return m.startModelDownload()
			}
			return m.proceedToHardware()
		case "n", "N", "esc":
			if m.binUpdate != nil {
				m.binUpdate = nil
				return m.afterBinaryUpdate()
			}
			m.modelUpdate = nil
			return m.proceedToHardware()
		case "ctrl+c":
			return m, tea.Quit
		}
		return m, nil

	case phaseError:
		switch msg.String() {
		case "q", "ctrl+c", "enter":
			return m, tea.Quit
		case "r", "R":
			m.phase = phaseLlamaCheck
			m.statusMsg = "Retrying…"
			m.errorMsg = ""
			return m, tea.Batch(m.spinner.Tick, checkLlamaCmd(m.llamaPort))
		}
	}
	if msg.String() == "ctrl+c" {
		return m, tea.Quit
	}
	return m, nil
}

func (m startupModel) View() tea.View {
	w := m.width
	if w < 40 {
		w = 80
	}

	var b strings.Builder

	// "@ CODE" wordmark header: blue "@" mark, white "CODE"
	b.WriteString("\n")
	wordmarkStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("#e8eaf4"))
	accentStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("#4d9fff")).Bold(true)
	for i, line := range sonutWordmarkMainLines {
		accentLine := ""
		if i < len(sonutWordmarkAccentLines) {
			accentLine = wordmarkStyle.Render(sonutWordmarkAccentLines[i])
		}
		b.WriteString(centerLine(accentStyle.Render(line)+accentLine, w))
		b.WriteString("\n")
	}
	b.WriteString("\n")
	b.WriteString(centerLine(lipgloss.NewStyle().Foreground(lipgloss.Color("#7a83a8")).Render("Local AI. No limits."), w))
	b.WriteString("\n\n")

	switch m.phase {
	case phaseLogin:
		b.WriteString(m.viewLogin(w))
	case phaseModelCheck, phaseUpdateCheck, phaseBinaryUpdate, phaseHardwareSetup, phaseToolsSetup, phaseLlamaCheck, phaseLlamaStart:
		b.WriteString(m.viewProgress(w))
	case phaseUpdatePrompt:
		b.WriteString(m.viewUpdatePrompt(w))
	case phaseModelDownload:
		b.WriteString(m.viewDownload(w))
	case phaseError:
		b.WriteString(m.viewError(w))
	case phaseDone:
		b.WriteString(centerLine(accentStyle.Render("✓ Ready!"), w))
		b.WriteString("\n")
	}

	v := tea.NewView(b.String())
	v.AltScreen = true
	return v
}

// viewUpdatePrompt renders the one-key update approval card.
func (m startupModel) viewUpdatePrompt(w int) string {
	accent := lipgloss.NewStyle().Foreground(lipgloss.Color("#4d9fff")).Bold(true)
	faint := lipgloss.NewStyle().Foreground(lipgloss.Color("#7a83a8"))
	ink := lipgloss.NewStyle().Foreground(lipgloss.Color("#e8eaf4"))
	border := lipgloss.NewStyle().Foreground(lipgloss.Color("#1c2236"))

	title := "Update available"
	var detail []string
	if m.binUpdate != nil {
		detail = append(detail,
			ink.Render("  @code v"+m.binUpdate.Current+" → v"+m.binUpdate.Latest),
			faint.Render("  Installs in place; the new version runs on the next launch."))
	} else if m.modelUpdate != nil {
		title = "New model available"
		for _, f := range m.modelUpdate.Missing {
			detail = append(detail, ink.Render("  "+llama.LocalModelFilename(f.Filename)))
		}
		detail = append(detail, faint.Render("  Old model files are removed first, then the new ones download."))
	}

	var b strings.Builder
	b.WriteString(centerLine(accent.Render(title), w))
	b.WriteString("\n\n")
	lines := append([]string{""}, detail...)
	lines = append(lines, "", ink.Render("  [Y] Update now    [N] Later"), "")
	boxW := 56
	if boxW > w-6 {
		boxW = w - 6
	}
	box := styledBlock(boxW, lines, border)
	for _, line := range strings.Split(box, "\n") {
		b.WriteString(centerLine(line, w))
		b.WriteString("\n")
	}
	if m.updateNote != "" {
		b.WriteString("\n")
		b.WriteString(centerLine(faint.Render(m.updateNote), w))
	}
	return b.String()
}

// viewDownload renders the beautiful download screen with progress bar and rotating tips.
func (m startupModel) viewDownload(w int) string {
	accent := lipgloss.NewStyle().Foreground(lipgloss.Color("#4d9fff")).Bold(true)
	gold := lipgloss.NewStyle().Foreground(lipgloss.Color("#f0c040")).Bold(true)
	faint := lipgloss.NewStyle().Foreground(lipgloss.Color("#7a83a8"))
	ink := lipgloss.NewStyle().Foreground(lipgloss.Color("#e8eaf4"))

	var b strings.Builder

	// Headline
	b.WriteString(centerLine(gold.Render("✦  Something great is coming up  ✦"), w))
	b.WriteString("\n\n")

	// Download label — show a clean model name (strip .gguf and quantization suffix like _4b, _Q4_K_M, etc.)
	label := "Downloading your model…"
	if m.downloadFile != "" {
		name := strings.TrimSuffix(m.downloadFile, filepath.Ext(m.downloadFile))
		// Strip trailing quantization tokens: _4b, _Q4_K_M, _8b, etc.
		for _, sep := range []string{"_Q", "_q", "_4b", "_8b", "_14b", "_32b", "_70b", "_3b", "_7b", "_2b", "_1b"} {
			if idx := strings.LastIndex(name, sep); idx > 0 {
				name = name[:idx]
				break
			}
		}
		label = "Downloading " + name + "…"
	}
	b.WriteString(centerLine(ink.Render(label), w))
	b.WriteString("\n\n")

	// Progress bar
	barWidth := 50
	if w < barWidth+18 {
		barWidth = w - 18
	}
	if barWidth < 10 {
		barWidth = 10
	}

	pct := m.downloadPercent
	if pct < 0 {
		pct = 0
	}
	if pct > 100 {
		pct = 100
	}

	filled := int(float64(barWidth) * pct / 100)
	bar := strings.Repeat("█", filled) + strings.Repeat("░", barWidth-filled)
	pctLabel := fmt.Sprintf("%3.0f%%", pct)
	barLine := accent.Render(bar) + "  " + accent.Render(pctLabel)
	b.WriteString(centerLine(barLine, w))
	b.WriteString("\n")

	// Keep setup focused: the bar and percentage convey progress without exposing
	// model-file sizes or download byte counts.
	b.WriteString(centerLine(faint.Render("Preparing your local AI…"), w))
	b.WriteString("\n\n")

	// Tip card with spinner
	tip := m.downloadTip
	if tip == "" {
		tip = downloadTips[0]
	}
	tipLines := []string{
		"",
		"  " + m.spinner.View() + "  " + faint.Render(tip),
		"",
	}
	cardW := 56
	if cardW > w-6 {
		cardW = w - 6
	}
	if cardW < 28 {
		cardW = 28
	}
	borderStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("#1c2236"))
	card := styledBlock(cardW, tipLines, borderStyle)
	for _, line := range strings.Split(card, "\n") {
		b.WriteString(centerLine(line, w))
		b.WriteString("\n")
	}

	b.WriteString("\n")
	b.WriteString(centerLine(faint.Render("Press Ctrl+C to quit"), w))
	return b.String()
}

func (m startupModel) viewLogin(w int) string {
	accent := lipgloss.NewStyle().Foreground(lipgloss.Color("#4d9fff")).Bold(true)
	faint := lipgloss.NewStyle().Foreground(lipgloss.Color("#7a83a8"))
	ink := lipgloss.NewStyle().Foreground(lipgloss.Color("#e8eaf4"))
	border := lipgloss.NewStyle().Foreground(lipgloss.Color("#1c2236"))

	var b strings.Builder

	if m.loginMode == loginModeChoose {
		b.WriteString(centerLine(accent.Render("Start @code"), w))
		b.WriteString("\n\n")

		gBlue := lipgloss.NewStyle().Foreground(lipgloss.Color("#4285F4")).Bold(true)
		gRed := lipgloss.NewStyle().Foreground(lipgloss.Color("#EA4335")).Bold(true)
		gYellow := lipgloss.NewStyle().Foreground(lipgloss.Color("#FBBC05")).Bold(true)
		gGreen := lipgloss.NewStyle().Foreground(lipgloss.Color("#34A853")).Bold(true)
		ssoGrey := lipgloss.NewStyle().Foreground(lipgloss.Color("#9aa0b8"))
		googleLabel := gBlue.Render("G") + gRed.Render("o") + gYellow.Render("o") + gBlue.Render("g") + gGreen.Render("l") + gRed.Render("e") + " " + ssoGrey.Render("SSO")

		vStart := lipgloss.NewStyle().Foreground(lipgloss.Color("#9B59B6")).Bold(true)
		vMid := lipgloss.NewStyle().Foreground(lipgloss.Color("#8E44AD")).Bold(true)
		vEnd := lipgloss.NewStyle().Foreground(lipgloss.Color("#6C3483")).Bold(true)
		magicLabel := vStart.Render("Ma") + vMid.Render("gic") + vEnd.Render(" Link")

		boxW := 44
		lines := []string{
			"",
			ink.Render("  [L]  Continue locally") + faint.Render("  (no account)"),
			"",
			ink.Render("  [M]  ") + magicLabel + faint.Render("  (email + one-click)"),
			"",
			ink.Render("  [G]  ") + googleLabel + faint.Render("  (browser flow)"),
			"",
		}
		box := styledBlock(boxW, lines, border)
		for _, line := range strings.Split(box, "\n") {
			b.WriteString(centerLine(line, w))
			b.WriteString("\n")
		}
		b.WriteString("\n")
		b.WriteString(centerLine(faint.Render("Press Q to quit"), w))
		if m.statusMsg != "" {
			b.WriteString("\n\n")
			b.WriteString(centerLine(faint.Render(m.statusMsg), w))
		}

	} else if m.loginMode == loginModeMagic {
		b.WriteString(centerLine(accent.Render("Enter your email"), w))
		b.WriteString("\n\n")
		b.WriteString(centerLine("  "+m.emailInput.View()+"  ", w))
		b.WriteString("\n\n")
		b.WriteString(centerLine(faint.Render("Press Enter to send link · Esc to go back"), w))

	} else if m.loginMode == loginModeWaiting {
		b.WriteString(centerLine(accent.Render(m.waitingLabel), w))
		b.WriteString("\n\n")
		b.WriteString(centerLine(faint.Render(m.statusMsg), w))
		b.WriteString("\n")
		b.WriteString(centerLine(m.spinner.View()+" Waiting for browser authentication… (Esc to cancel)", w))
	}

	return b.String()
}

func (m startupModel) viewProgress(w int) string {
	accent := lipgloss.NewStyle().Foreground(lipgloss.Color("#4d9fff"))
	faint := lipgloss.NewStyle().Foreground(lipgloss.Color("#7a83a8"))

	msg := m.statusMsg
	if msg == "" {
		msg = "Starting…"
	}
	line := m.spinner.View() + "  " + accent.Render(msg)
	var b strings.Builder
	b.WriteString(centerLine(line, w))
	b.WriteString("\n\n")
	b.WriteString(centerLine(faint.Render("Press Ctrl+C to quit"), w))
	return b.String()
}

func (m startupModel) viewError(w int) string {
	red := lipgloss.NewStyle().Foreground(lipgloss.Color("#ff7a7a"))
	faint := lipgloss.NewStyle().Foreground(lipgloss.Color("#7a83a8"))
	var b strings.Builder
	b.WriteString(centerLine(red.Render("✗ Error"), w))
	b.WriteString("\n\n")

	// Wrap the error message for narrow terminals
	errLines := strings.Split(m.errorMsg, "\n")
	for _, el := range errLines {
		b.WriteString(centerLine(red.Render(el), w))
		b.WriteString("\n")
	}
	b.WriteString("\n")
	b.WriteString(centerLine(faint.Render("[R] Retry  [Q] Quit"), w))
	return b.String()
}

// authConfigDir returns the directory for auth token storage.
func authConfigDir() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".atcode")
}

// googleLoginCmd starts the loopback server, opens the browser, and waits for the token.
func googleLoginCmd() tea.Cmd {
	return func() tea.Msg {
		cfg := config.Load()

		srv, err := auth.NewLoopbackServer()
		if err != nil {
			return startupErrMsg{err: fmt.Errorf("could not start auth server: %w", err)}
		}

		redirectURL := srv.CallbackURL()
		authURL := auth.BuildSupabaseGoogleURL(cfg.SupabaseURL, redirectURL)
		if err := auth.OpenBrowser(authURL); err != nil {
			_ = err
		}

		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
		defer cancel()

		sess, err := srv.Wait(ctx)
		if err != nil {
			return startupErrMsg{err: fmt.Errorf("google sign-in failed: %w", err)}
		}
		return googleAuthDoneMsg{session: sess}
	}
}

// sendMagicLinkCmd sends a magic link and waits for the user to click it.
func sendMagicLinkCmd(email string) tea.Cmd {
	return func() tea.Msg {
		cfg := config.Load()

		srv, err := auth.NewLoopbackServer()
		if err != nil {
			return startupErrMsg{err: fmt.Errorf("could not start auth server: %w", err)}
		}

		client := auth.NewSupabaseClientWithRedirect(cfg.SupabaseURL, cfg.SupabaseKey, srv.CallbackURL())
		sendCtx, sendCancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer sendCancel()
		if err := client.SignInWithMagicLink(sendCtx, email); err != nil {
			return startupErrMsg{err: fmt.Errorf("could not send magic link: %w", err)}
		}

		waitCtx, waitCancel := context.WithTimeout(context.Background(), 10*time.Minute)
		defer waitCancel()
		sess, err := srv.Wait(waitCtx)
		if err != nil {
			return startupErrMsg{err: fmt.Errorf("magic link sign-in failed: %w", err)}
		}
		return otpVerifiedMsg{session: sess}
	}
}

// startLlamaCmd auto-starts llama-server with hardware-optimized settings.
// The server runs internally on llama.InternalLlamaPort (non-obvious, loopback
// only, --no-webui), and a reverse proxy on the public port (2748 by default)
// masks the web UI and proxies API requests.
func startLlamaCmd(port int) tea.Cmd {
	return func() tea.Msg {
		phaseStart := time.Now()
		// Internal port for the actual llama server (not exposed directly).
		// Deliberately NOT 8000: an old llama-server left over from a previous
		// version (started without --no-webui) may still be listening there,
		// and the adopt-if-healthy fast path below must never pick it up.
		internalPort := llama.InternalLlamaPort
		if err := llama.PrepareEngineAuth(); err != nil {
			return startupErrMsg{err: fmt.Errorf("prepare local engine authentication: %w", err)}
		}

		// Evict any legacy raw llama-server still bound to the public port so
		// the proxy bind below cannot fail with "address already in use".
		llama.ReclaimPublicPort(port)

		// Start the reverse proxy on the public port before starting the server.
		// This ensures the public port is ready when the server starts.
		if err := llama.StartWebUIProxy(port, internalPort); err != nil {
			return startupErrMsg{err: fmt.Errorf("failed to start web UI proxy: %w", err)}
		}

		// If it came up already, skip (fast check: 500ms timeout)
		// Check the internal port where the actual llama server runs
		healthURL := fmt.Sprintf("http://127.0.0.1:%d/health", internalPort)
		{
			ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
			req, _ := http.NewRequestWithContext(ctx, "GET", healthURL, nil)
			if resp, err := http.DefaultClient.Do(req); err == nil && resp.StatusCode == http.StatusOK {
				resp.Body.Close()
				cancel()
				// Adopt only when the process matches the current Bonsai/Prism
				// profile (-b 1024, single slot, …). A leftover server started
				// with -b 512 (or multi-slot defaults) starves every follow-up
				// prefill — observed live as "first message fast, rest slow".
				if llama.ServerProfileIsCurrent(internalPort) {
					// Adopted an already-running engine: still ours for this
					// session so quit reaps it (no orphan multi-GB process).
					llama.MarkOwnedPort(internalPort)
					return llamaReadyMsg{}
				}
				llama.KillLlamaOnPort(internalPort)
				// Fall through and relaunch with OptimalLlamaArgs.
			} else {
				cancel()
			}
		}

		// Resolve only the stable @code engine name inside ~/.atcode. External
		// package-manager names remain a compatibility fallback.
		serverBin, found := llama.FindAtcodeServer()
		if !found {
			return startupErrMsg{err: fmt.Errorf(
				"the atcode engine is not installed.\n" +
					"Re-run the @code installer to set it up for this platform.")}
		}

		// Find model path
		modelPath := os.Getenv("ATCODE_MODEL_PATH")
		if modelPath == "" {
			modelPath = atcodeEnv("MODEL_PATH")
		}
		var mmProjPath string
		if modelPath == "" {
			mainPath, mmPath, present := llama.CheckModelsPresent()
			if present {
				modelPath = mainPath
				mmProjPath = mmPath
			}
		}
		if modelPath == "" {
			return startupErrMsg{err: fmt.Errorf("no model found — set ATCODE_MODEL_PATH or restart @code to download")}
		}

		// Build hardware-optimized command args (with vision model if present)
		// Use the internal port for the actual llama server (the public port gets
		// a reverse proxy that masks the web UI)
		args := llama.OptimalLlamaArgs(modelPath, mmProjPath, internalPort)

		// Capture stderr to a log file so startup errors are visible
		logFile, _ := os.OpenFile(llama.AtcodeServerLogPath(), os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)

		cmd := exec.Command(serverBin, args...)
		cmd.Stdout = logFile
		cmd.Stderr = logFile
		// Own process group so StopOwnedServers can reap the whole tree and a
		// parent SIGINT does not race with our deliberate kill-on-exit path.
		background.ConfigureChildProcessGroup(cmd)
		if err := cmd.Start(); err != nil {
			if logFile != nil {
				logFile.Close()
			}
			return startupErrMsg{err: fmt.Errorf("failed to start the atcode engine: %w", err)}
		}
		// Mark ownership immediately after Start so a quit mid-warmup still reaps.
		llama.MarkOwnedPort(internalPort)

		// Do not hold the TUI hostage while the 262k source model maps and
		// allocates its KV cache. The local provider and background warmup both
		// wait for /health, so opening the interface here is safe even on a cold
		// model load.
		if logFile != nil {
			_ = logFile.Close()
		}
		tracePhase("engine-spawn", time.Since(phaseStart))
		go func() {
			if err := cmd.Wait(); err != nil {
				tracePhase("engine-exit", 0)
			}
			llama.ClearOwnedPort(internalPort)
		}()
		return llamaReadyMsg{}

		/*
			// Synchronous readiness loop retained here as documentation for the
			// health-gated provider path. The process is intentionally not waited on
			// by startup anymore.
			done := make(chan error, 1)
			go func() { done <- cmd.Wait() }()

			deadline := time.Now().Add(180 * time.Second)
			pollInterval := 50 * time.Millisecond
			const maxPollInterval = 500 * time.Millisecond
			phaseStart := time.Now()
			for time.Now().Before(deadline) {
				// Check if process exited (crashed)
				select {
				case waitErr := <-done:
					if logFile != nil {
						logFile.Close()
					}
					llama.ClearOwnedPort(internalPort)
					msg := "the atcode engine exited unexpectedly"
					if waitErr != nil {
						msg = fmt.Sprintf("the atcode engine failed: %v (see ~/.atcode/atcode-server.log)", waitErr)
					}
					tracePhase("engine-start-crash", time.Since(phaseStart))
					return startupErrMsg{err: fmt.Errorf("%s", msg)}
				default:
				}

				ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
				req, _ := http.NewRequestWithContext(ctx, "GET", healthURL, nil)
				resp, err := http.DefaultClient.Do(req)
				cancel()
				if err == nil && resp.StatusCode == http.StatusOK {
					resp.Body.Close()
					if logFile != nil {
						logFile.Close()
					}
					tracePhase("engine-start", time.Since(phaseStart))
					return llamaReadyMsg{}
				}
				if resp != nil {
					resp.Body.Close()
				}
				time.Sleep(pollInterval)
				// Exponential backoff: 50ms → 100ms → 200ms → 500ms (capped)
				pollInterval = time.Duration(float64(pollInterval) * 1.5)
				if pollInterval > maxPollInterval {
					pollInterval = maxPollInterval
				}
			}
			if logFile != nil {
				logFile.Close()
			}
			return startupErrMsg{err: fmt.Errorf("the atcode engine did not start within 180s (see ~/.atcode/atcode-server.log)")}
		*/
	}
}

// checkLlamaCmd checks if the llama server is already running.
func checkLlamaCmd(port int) tea.Cmd {
	return func() tea.Msg {
		phaseStart := time.Now()
		// A raw llama-server from an older version may still own the public
		// port (web UI exposed, no proxy). It answers /health, so without this
		// it would be adopted and the UI would stay reachable all session.
		llama.ReclaimPublicPort(port)
		url := fmt.Sprintf("http://127.0.0.1:%d/health", port)
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
		if err != nil {
			tracePhase("engine-check", time.Since(phaseStart))
			return llamaStartedMsg{}
		}
		resp, err := http.DefaultClient.Do(req)
		tracePhase("engine-check", time.Since(phaseStart))
		if err == nil && resp.StatusCode == http.StatusOK {
			resp.Body.Close()
			// The public proxy can remain healthy after the engine behind it was
			// launched with an old context, batch size, or slot profile. Validate
			// the actual local engine before adopting it; otherwise the first user
			// request inherits the stale server and can sit at 0 tokens for minutes.
			if !llama.ServerProfileIsCurrent(llama.InternalLlamaPort) {
				tracePhase("engine-profile-stale", time.Since(phaseStart))
				return llamaStartedMsg{}
			}
			// Public proxy/health is up — claim the internal engine for this
			// session so quit still reaps whatever is behind the proxy.
			llama.MarkOwnedPort(llama.InternalLlamaPort)
			return llamaReadyMsg{}
		}
		if resp != nil {
			resp.Body.Close()
		}
		return llamaStartedMsg{}
	}
}

// jsonString encodes s as a JSON string literal (with surrounding quotes).
func jsonString(s string) string {
	return fmt.Sprintf("%q", s)
}

// RunStartup runs the startup screen: login → model check/download → hardware
// detection → llama startup.
// Returns true if the main TUI should proceed, false if the user quit.
func RunStartup(ctx context.Context, llamaPort int, agentOpts agent.Options) bool {
	m := newStartupModel(llamaPort, agentOpts)
	p := tea.NewProgram(m, tea.WithContext(ctx))
	result, err := p.Run()
	if err != nil {
		return false
	}
	sm, ok := result.(startupModel)
	if !ok {
		return false
	}
	return sm.phase == phaseDone
}
