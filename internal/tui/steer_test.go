package tui

import (
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/aistatine/sonut/internal/agent"
)

// Pressing Enter on an EMPTY composer while a prompt is queued steers the
// queued prompt into the LIVE run: it lands in the steer inbox (for the agent
// loop's next turn boundary), shows up as a user turn immediately, and leaves
// the queue empty — the run keeps going, no second run starts.
func TestEnterAgainSteersQueuedPromptIntoLiveRun(t *testing.T) {
	m := newQueuedMessageTestModel(t)
	m.pending = true
	m.activeRunID = 1
	m.runID = 1
	m.steerInbox = &agent.SteerInbox{}

	// First Enter: queue the prompt (existing behavior).
	m.input.SetValue("also add dark mode")
	updated, _ := m.Update(testKey(tea.KeyEnter))
	next := updated.(model)
	if !next.hasQueuedMessage() {
		t.Fatal("first enter should queue the prompt")
	}

	// Second Enter on the empty composer: push it into the live run.
	updated, cmd := next.Update(testKey(tea.KeyEnter))
	next = updated.(model)
	if cmd != nil {
		t.Fatal("steering must not start a second run")
	}
	if next.hasQueuedMessage() {
		t.Fatal("steered prompt should leave the queue")
	}
	if next.steerInbox.Empty() {
		t.Fatal("steered prompt should be waiting in the run's steer inbox")
	}
	drained := next.steerInbox.Drain()
	if len(drained) != 1 || drained[0] != "also add dark mode" {
		t.Fatalf("steer inbox = %v, want the queued prompt", drained)
	}
	if !transcriptContains(next.transcript, "also add dark mode") {
		t.Fatal("steered prompt should appear as a user turn immediately")
	}
	if !next.pending {
		t.Fatal("the active run must stay pending")
	}
}

// An empty Enter with nothing queued stays a no-op, and a leftover steered
// message that the finishing run never consumed is folded back into the queue
// (so input is never lost to the shutdown race).
func TestSteerLeftoverFoldsBackIntoQueueOnRunEnd(t *testing.T) {
	m := newQueuedMessageTestModel(t)
	m.pending = true
	m.activeRunID = 1
	m.runID = 1
	m.steerInbox = &agent.SteerInbox{}

	// Empty Enter, nothing queued: no-op.
	updated, cmd := m.Update(testKey(tea.KeyEnter))
	next := updated.(model)
	if cmd != nil || next.hasQueuedMessage() || !next.steerInbox.Empty() {
		t.Fatal("empty enter with no queued prompt must be a no-op")
	}

	// Push arrives just as the run finishes: the response handler folds it
	// back into the queue, and the queued-message machinery relaunches it as
	// the next run — the input is never dropped.
	next.steerInbox.Push("missed by a hair")
	updated, cmd = next.Update(agentResponseMsg{runID: 1})
	next = updated.(model)
	if cmd == nil || !next.pending {
		t.Fatal("leftover steer message should relaunch as the next run")
	}
	if next.hasQueuedMessage() {
		t.Fatalf("relaunched message should leave the queue, got %q", next.queuedMessage)
	}
	if !transcriptContains(next.transcript, "missed by a hair") {
		t.Fatal("relaunched message should appear as a user turn")
	}
}
