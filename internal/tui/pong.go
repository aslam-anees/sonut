package tui

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
)

// Pong easter egg: while a run is grinding, the footer offers
// "@ press spacebar to ping pong". Spacebar opens a small pong board directly
// above the composer — the ball is "@", the player (left paddle, ↑/↓) plays
// the computer (right paddle), and every return bumps both score and ball
// speed. The best score persists across sessions.
//
// When the local voice pack is ready, /pong is the explicit entry point because
// Space is reserved for the voice hold gesture.
//
// The game must never disturb the core flow: it renders inside the footer
// (never over the transcript), pauses itself the moment a permission prompt /
// ask-user questionnaire needs the keyboard, resumes with a 3-2-1 countdown
// on spacebar, and any key it doesn't own falls through to normal handling.

const (
	pongTickInterval      = 55 * time.Millisecond
	pongCountdownInterval = 700 * time.Millisecond
	pongBoardHeight       = 10 // playfield rows (excluding border)
	pongMaxBoardWidth     = 48 // playfield columns (excluding border)
	pongPaddleHeight      = 3
)

type pongGame struct {
	active  bool // board is visible and (unless paused) ticking
	started bool // a game exists (allows resume-after-run via spacebar)
	paused  bool
	lost    bool
	// countdown > 0 means the 3-2-1 pre-play phase is running.
	countdown int
	// seq guards the tick chain: every (re)activation bumps it, and stale
	// pongTickMsg values are dropped, so pause/resume can never double-tick.
	seq int

	width, height int
	ballX, ballY  float64
	velX, velY    float64
	playerY, aiY  float64 // paddle top row
	score, best   int
	bestLoaded    bool
}

func pongBestPath() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".atcode", "pong_best")
}

func loadPongBest() int {
	path := pongBestPath()
	if path == "" {
		return 0
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return 0
	}
	best, _ := strconv.Atoi(strings.TrimSpace(string(data)))
	return best
}

func savePongBest(best int) {
	path := pongBestPath()
	if path == "" {
		return
	}
	_ = os.MkdirAll(filepath.Dir(path), 0o755)
	_ = os.WriteFile(path, []byte(strconv.Itoa(best)), 0o644)
}

// pongTickMsg advances the game one frame (or one countdown step).
type pongTickMsg struct{ seq int }

func (g *pongGame) tickCmd() tea.Cmd {
	interval := pongTickInterval
	if g.countdown > 0 {
		interval = pongCountdownInterval
	}
	seq := g.seq
	return tea.Tick(interval, func(time.Time) tea.Msg { return pongTickMsg{seq: seq} })
}

// reset lays out a fresh rally (fresh game when full is true).
func (g *pongGame) reset(width int, full bool) {
	g.width = min(pongMaxBoardWidth, max(24, width-4))
	g.height = pongBoardHeight
	g.ballX = float64(g.width) / 2
	g.ballY = float64(g.height) / 2
	g.velX = -0.9
	g.velY = 0.45
	g.playerY = float64(g.height-pongPaddleHeight) / 2
	g.aiY = g.playerY
	if full {
		g.score = 0
		g.lost = false
	}
}

// start begins or resumes the game with a countdown. A lost game restarts
// fresh; a merely paused one keeps its rally state.
func (g *pongGame) start(width int) {
	if !g.bestLoaded {
		g.best = loadPongBest()
		g.bestLoaded = true
	}
	if !g.started || g.lost {
		g.reset(width, true)
	}
	g.active = true
	g.started = true
	g.paused = false
	g.countdown = 3
	g.seq++
}

// pause freezes the game (input needed elsewhere / run finished / user left).
func (g *pongGame) pause() {
	if g.active {
		g.paused = true
		g.seq++ // orphan any in-flight tick
	}
}

// quit hides the board entirely; best score is already persisted on loss.
func (g *pongGame) quit() {
	g.active = false
	g.paused = false
	g.seq++
}

// step advances one frame; returns false when the rally just ended (loss).
func (g *pongGame) step() bool {
	if g.countdown > 0 {
		g.countdown--
		return true
	}
	// Computer paddle: tracks the ball with a speed cap so long rallies stay
	// winnable but the computer stays honest.
	aiCenter := g.aiY + float64(pongPaddleHeight)/2
	aiSpeed := 0.55
	if g.ballY > aiCenter+0.3 {
		g.aiY = min(float64(g.height-pongPaddleHeight), g.aiY+aiSpeed)
	} else if g.ballY < aiCenter-0.3 {
		g.aiY = max(0, g.aiY-aiSpeed)
	}

	g.ballX += g.velX
	g.ballY += g.velY
	if g.ballY <= 0 {
		g.ballY, g.velY = -g.ballY, -g.velY
	}
	if g.ballY >= float64(g.height-1) {
		g.ballY, g.velY = 2*float64(g.height-1)-g.ballY, -g.velY
	}
	if g.ballX <= 0 { // player edge
		if g.ballY >= g.playerY-0.5 && g.ballY <= g.playerY+float64(pongPaddleHeight)-0.5 {
			g.ballX, g.velX = -g.ballX, -g.velX
			// Returns score and speed up the rally.
			g.score++
			g.velX *= 1.07
			g.velY *= 1.05
			if g.score > g.best {
				g.best = g.score
				savePongBest(g.best)
			}
		} else {
			g.lost = true
			g.paused = true
			g.seq++
			return false
		}
	}
	if g.ballX >= float64(g.width-1) { // computer edge
		if g.ballY >= g.aiY-0.5 && g.ballY <= g.aiY+float64(pongPaddleHeight)-0.5 {
			g.ballX, g.velX = 2*float64(g.width-1)-g.ballX, -g.velX
		} else {
			// Computer missed: celebrate with points and a fresh rally.
			g.score += 5
			if g.score > g.best {
				g.best = g.score
				savePongBest(g.best)
			}
			g.reset(g.width+4, false)
		}
	}
	return true
}

func (g *pongGame) movePlayer(delta float64) {
	g.playerY = max(0, min(float64(g.height-pongPaddleHeight), g.playerY+delta))
}

// handlePongKey routes one key press. handled=false means the key belongs to
// the normal input flow (and may also have quit the game on its way through).
func (m *model) handlePongKey(msg tea.KeyMsg) (bool, tea.Cmd) {
	spacePressed := keyIs(msg, tea.KeySpace) || keyText(msg) == " "
	if m.pong.active {
		switch {
		case keyIs(msg, tea.KeyUp):
			m.pong.movePlayer(-1.5)
			return true, nil
		case keyIs(msg, tea.KeyDown):
			m.pong.movePlayer(1.5)
			return true, nil
		case spacePressed:
			if m.pong.paused || m.pong.lost {
				m.pong.start(chatWidth(m.width))
				return true, m.pong.tickCmd()
			}
			m.pong.pause()
			return true, nil
		case keyIs(msg, tea.KeyEsc):
			m.pong.quit()
			return true, nil
		default:
			// Any other key: the user is back to work. Hide the board and let
			// the key do its normal job (type into the composer, etc.).
			m.pong.quit()
			return false, nil
		}
	}
	// Board hidden: spacebar on an empty composer starts (while a run is
	// grinding) or resumes (a previously started game, even after the run).
	if spacePressed && m.composerValue() == "" && m.noBlockingModal() &&
		!m.suggestionsActive() && !m.subchat.active && (m.pending || m.pong.started) {
		m.pong.start(chatWidth(m.width))
		return true, m.pong.tickCmd()
	}
	return false, nil
}

// renderPongPanel draws the board for the footer; empty when inactive.
func (m model) renderPongPanel(width int) string {
	g := m.pong
	if !g.active {
		return ""
	}
	grid := make([][]rune, g.height)
	for y := range grid {
		grid[y] = make([]rune, g.width)
		for x := range grid[y] {
			grid[y][x] = ' '
		}
	}
	put := func(x, y int, r rune) {
		if y >= 0 && y < g.height && x >= 0 && x < g.width {
			grid[y][x] = r
		}
	}
	for i := 0; i < pongPaddleHeight; i++ {
		put(0, int(g.playerY)+i, '█')
		put(g.width-1, int(g.aiY)+i, '█')
	}
	put(int(g.ballX+0.5), int(g.ballY+0.5), '@')

	status := fmt.Sprintf("PONG · score %d · best %d", g.score, g.best)
	switch {
	case g.lost:
		status += " · lost! space for a new game · esc to close"
	case g.countdown > 0:
		status += fmt.Sprintf(" · %d…", g.countdown)
	case g.paused:
		status += " · paused · space to resume (3-2-1)"
	default:
		status += " · ↑/↓ move · space pause · esc quit"
	}

	var b strings.Builder
	b.WriteString(sonutTheme.faint.Render(status))
	b.WriteString("\n")
	border := strings.Repeat("─", g.width)
	b.WriteString(sonutTheme.faint.Render("┌" + border + "┐"))
	b.WriteString("\n")
	for y := 0; y < g.height; y++ {
		b.WriteString(sonutTheme.faint.Render("│"))
		b.WriteString(string(grid[y]))
		b.WriteString(sonutTheme.faint.Render("│"))
		b.WriteString("\n")
	}
	b.WriteString(sonutTheme.faint.Render("└" + border + "┘"))
	return b.String()
}
