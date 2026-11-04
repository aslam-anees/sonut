package tui

import (
	"context"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
)

func pongKey(code rune) tea.KeyPressMsg {
	return tea.KeyPressMsg{Code: code}
}

func TestPongStartsOnSpaceWhilePending(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	m := newModel(context.Background(), Options{})
	m.pending = true
	handled, cmd := m.handlePongKey(pongKey(tea.KeySpace))
	if !handled || cmd == nil {
		t.Fatalf("space while pending: handled=%v cmd=%v", handled, cmd)
	}
	if !m.pong.active || m.pong.countdown != 3 {
		t.Fatalf("game state = %+v", m.pong)
	}
	if !strings.Contains(m.renderPongPanel(80), "PONG") {
		t.Fatal("board not rendered")
	}
}

func TestPongCommandKeepsEasterEggReachableWithVoice(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	m := newModel(context.Background(), Options{})
	m.pending = true
	updated, cmd := m.executeCommand(parseCommand("/pong"))
	next := updated.(model)
	if !next.pong.active || cmd == nil {
		t.Fatalf("/pong: active=%v cmd=%v", next.pong.active, cmd)
	}
}

func TestPongDoesNotHijackTyping(t *testing.T) {
	m := newModel(context.Background(), Options{})
	m.pending = true
	m.input.SetValue("hello") // composer not empty — space is typing
	if handled, _ := m.handlePongKey(pongKey(tea.KeySpace)); handled {
		t.Fatal("space during typing must not start the game")
	}
	// Idle with no prior game: space is just a key.
	m2 := newModel(context.Background(), Options{})
	if handled, _ := m2.handlePongKey(pongKey(tea.KeySpace)); handled {
		t.Fatal("space while idle with no game must fall through")
	}
}

func TestPongTypingQuitsGameAndFallsThrough(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	m := newModel(context.Background(), Options{})
	m.pending = true
	m.handlePongKey(pongKey(tea.KeySpace)) // start
	msg := tea.KeyPressMsg{Code: 'h', Text: "h"}
	handled, _ := m.handlePongKey(msg)
	if handled || m.pong.active {
		t.Fatalf("typing: handled=%v active=%v — must quit and fall through", handled, m.pong.active)
	}
}

func TestPongScoreAndLossPersistBest(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	g := pongGame{}
	g.start(80)
	g.countdown = 0
	// Aim the ball at the player paddle: it must bounce and score.
	g.ballX, g.ballY, g.velX, g.velY = 1, g.playerY+1, -1.2, 0
	if !g.step() || g.score != 1 {
		t.Fatalf("return: score=%d lost=%v", g.score, g.lost)
	}
	// Aim past the paddle: rally lost, best persisted.
	g.playerY = 0
	g.ballX, g.ballY, g.velX, g.velY = 1, float64(g.height-1), -1.2, 0
	if g.step() || !g.lost {
		t.Fatalf("miss: lost=%v", g.lost)
	}
	if loadPongBest() != 1 {
		t.Fatalf("best = %d, want 1", loadPongBest())
	}
	// Space after a loss starts a fresh game with countdown.
	g.active = true
	g.start(80)
	if g.score != 0 || g.lost || g.countdown != 3 {
		t.Fatalf("fresh game = %+v", g)
	}
}
