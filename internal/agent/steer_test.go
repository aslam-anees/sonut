package agent

import (
	"context"
	"strings"
	"testing"

	"github.com/aistatine/sonut/internal/sonutruntime"
)

// steerOnFirstTurnProvider pushes a live user message into the steer inbox
// while the FIRST turn is streaming — the race the feature exists for: the
// user typed mid-run, and the run must not end on the answer in flight.
type steerOnFirstTurnProvider struct {
	mockProvider
	inbox *SteerInbox
}

func (provider *steerOnFirstTurnProvider) StreamCompletion(ctx context.Context, request sonutruntime.CompletionRequest) (<-chan sonutruntime.StreamEvent, error) {
	if len(provider.requests) == 0 {
		provider.inbox.Push("also rename the flag to --fast")
	}
	return provider.mockProvider.StreamCompletion(ctx, request)
}

func requestContains(request sonutruntime.CompletionRequest, needle string) bool {
	for _, message := range request.Messages {
		if message.Role == sonutruntime.MessageRoleUser && strings.Contains(message.Content, needle) {
			return true
		}
	}
	return false
}

// A message pushed before the run's first turn is injected into the very
// first request as a priority user turn.
func TestSteerMessageInjectedAtTurnBoundary(t *testing.T) {
	inbox := &SteerInbox{}
	inbox.Push("focus on the login bug first")
	provider := &mockProvider{turns: [][]sonutruntime.StreamEvent{{
		{Type: sonutruntime.StreamEventText, Content: "done"},
		{Type: sonutruntime.StreamEventDone},
	}}}

	result, err := Run(context.Background(), "fix the bug", provider, Options{Steer: inbox})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if result.FinalAnswer != "done" {
		t.Fatalf("final answer = %q, want done", result.FinalAnswer)
	}
	if len(provider.requests) != 1 {
		t.Fatalf("requests = %d, want 1", len(provider.requests))
	}
	if !requestContains(provider.requests[0], "focus on the login bug first") ||
		!requestContains(provider.requests[0], "[live user message") {
		t.Fatalf("first request is missing the injected steer turn: %+v", provider.requests[0].Messages)
	}
	if !inbox.Empty() {
		t.Fatal("inbox should be drained after injection")
	}
}

// A message that arrives WHILE the final answer streams keeps the run alive:
// the loop continues, injects it, and the model answers it in the same run.
func TestSteerMessageKeepsRunAliveAtFinalAnswer(t *testing.T) {
	inbox := &SteerInbox{}
	provider := &steerOnFirstTurnProvider{inbox: inbox}
	provider.turns = [][]sonutruntime.StreamEvent{
		{
			{Type: sonutruntime.StreamEventText, Content: "first answer"},
			{Type: sonutruntime.StreamEventDone},
		},
		{
			{Type: sonutruntime.StreamEventText, Content: "second answer"},
			{Type: sonutruntime.StreamEventDone},
		},
	}

	result, err := Run(context.Background(), "do the task", provider, Options{Steer: inbox})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(provider.requests) != 2 {
		t.Fatalf("requests = %d, want 2 (run must continue past the first final answer)", len(provider.requests))
	}
	if !requestContains(provider.requests[1], "also rename the flag to --fast") {
		t.Fatalf("second request is missing the steered message: %+v", provider.requests[1].Messages)
	}
	if result.FinalAnswer != "second answer" {
		t.Fatalf("final answer = %q, want the post-steer answer", result.FinalAnswer)
	}
	if result.Turns != 2 {
		t.Fatalf("turns = %d, want 2", result.Turns)
	}
}

// A nil inbox and an empty inbox are exact no-ops.
func TestSteerNilAndEmptyInboxAreNoOps(t *testing.T) {
	if got := drainSteerMessages(nil); got != nil {
		t.Fatalf("drainSteerMessages(nil) = %v, want nil", got)
	}
	var nilInbox *SteerInbox
	nilInbox.Push("ignored") // must not panic
	if !nilInbox.Empty() {
		t.Fatal("nil inbox must report empty")
	}
	inbox := &SteerInbox{}
	inbox.Push("   ")
	if !inbox.Empty() {
		t.Fatal("whitespace-only push must be ignored")
	}
}
