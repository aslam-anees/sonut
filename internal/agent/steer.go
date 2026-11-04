package agent

import (
	"strings"
	"sync"

	"github.com/aistatine/sonut/internal/sonutruntime"
)

// SteerInbox carries user messages typed WHILE a run is active into the live
// run. The surface pushes from its input loop; the agent loop drains at every
// turn boundary and appends each message as a fresh user turn, so the model
// reacts to mid-run input immediately instead of after the whole run ends.
// The loop also checks the inbox before accepting a final answer: a message
// that arrived while the answer streamed keeps the run alive for another turn
// rather than being parked until a brand-new run.
//
// One inbox per run. A nil inbox disables steering entirely (the loop is
// byte-identical to the pre-steering behavior).
type SteerInbox struct {
	mu      sync.Mutex
	pending []string
}

// Push queues a user message for injection into the running loop. Empty or
// whitespace-only text is ignored.
func (inbox *SteerInbox) Push(text string) {
	text = strings.TrimSpace(text)
	if inbox == nil || text == "" {
		return
	}
	inbox.mu.Lock()
	defer inbox.mu.Unlock()
	inbox.pending = append(inbox.pending, text)
}

// Drain returns and clears every pending message, oldest first.
func (inbox *SteerInbox) Drain() []string {
	if inbox == nil {
		return nil
	}
	inbox.mu.Lock()
	defer inbox.mu.Unlock()
	drained := inbox.pending
	inbox.pending = nil
	return drained
}

// Empty reports whether nothing is waiting.
func (inbox *SteerInbox) Empty() bool {
	if inbox == nil {
		return true
	}
	inbox.mu.Lock()
	defer inbox.mu.Unlock()
	return len(inbox.pending) == 0
}

// drainSteerMessages converts every pending steer message into user turns
// ready to append to the conversation. The wrapper text tells the model the
// message arrived live so it treats it as the newest instruction and folds it
// into the work in progress instead of finishing the old plan first.
func drainSteerMessages(inbox *SteerInbox) []sonutruntime.Message {
	drained := inbox.Drain()
	if len(drained) == 0 {
		return nil
	}
	messages := make([]sonutruntime.Message, 0, len(drained))
	for _, text := range drained {
		messages = append(messages, sonutruntime.Message{
			Role: sonutruntime.MessageRoleUser,
			Content: "[live user message — sent while you were working] " + text + "\n\n" +
				"This is the user's newest instruction and takes priority: adjust your current plan to honor it now, then continue.",
		})
	}
	return messages
}
