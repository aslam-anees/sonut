package cli

import (
	"encoding/json"
	"fmt"
	"io"
	"strings"

	"github.com/aistatine/sonut/internal/agent"
	"github.com/aistatine/sonut/internal/sessions"
	"github.com/aistatine/sonut/internal/tools"
)

type execSessionRecorder struct {
	prepared sessions.PreparedExec
	err      error
}

const execCompactionPreserveLast = 8

// recordCompaction persists an automatic agent-loop summary using the current
// durable event log as the source of truth. The in-memory agent has already
// compacted its prompt; this event makes the same boundary available to future
// --resume calls without replaying the discarded tool-heavy prefix.
func (recorder *execSessionRecorder) recordCompaction(summary string, preserveLast int) {
	if recorder == nil || recorder.err != nil || recorder.prepared.Store == nil || recorder.prepared.Session.SessionID == "" {
		return
	}
	plan, err := recorder.prepared.Store.PlanCompaction(recorder.prepared.Session.SessionID, sessions.CompactionOptions{
		PreserveLast: preserveLast,
	})
	if err != nil || plan.CompactableCount == 0 {
		return
	}
	_, err = recorder.prepared.Store.RecordCompaction(recorder.prepared.Session.SessionID, sessions.RecordCompactionInput{
		Plan:    plan,
		Summary: summary,
	})
	if err != nil {
		recorder.err = err
	}
}

// captureCheckpoint snapshots the before-state of files a tool call will mutate.
// Best-effort: failures never affect the run. Returns the checkpoint event and
// true when one was recorded.
func (recorder *execSessionRecorder) captureCheckpoint(workspaceRoot string, call agent.ToolCall) (sessions.Event, bool) {
	if recorder.prepared.Store == nil || recorder.prepared.Session.SessionID == "" {
		return sessions.Event{}, false
	}
	var args map[string]any
	if call.Arguments != "" {
		_ = json.Unmarshal([]byte(call.Arguments), &args)
	}
	targets := tools.MutationTargets(workspaceRoot, call.Name, args)
	if len(targets) == 0 {
		return sessions.Event{}, false
	}
	event, err := recorder.prepared.Store.CaptureToolCheckpoint(recorder.prepared.Session.SessionID, workspaceRoot, call.Name, targets)
	if err != nil || event.Type == "" {
		return sessions.Event{}, false
	}
	return event, true
}

func shouldUseExecSession(options execOptions) bool {
	return options.outputFormat == execOutputStreamJSON ||
		options.resume != "" ||
		options.resumeLatest ||
		options.fork != "" ||
		options.initSessionID != ""
}

func preflightExecSession(options execOptions) error {
	if options.resume == "" && !options.resumeLatest && options.fork == "" {
		return nil
	}
	if (options.resume != "" || options.resumeLatest) && options.fork != "" {
		return execUsageError{"Use either --resume or --fork, not both."}
	}

	store := sessions.NewStore(sessions.StoreOptions{})
	switch {
	case options.fork != "":
		session, err := store.Get(options.fork)
		if err != nil {
			return err
		}
		if session == nil {
			return execUsageError{"@code session not found: " + options.fork}
		}
	case options.resume != "":
		session, err := store.Get(options.resume)
		if err != nil {
			return err
		}
		if session == nil {
			return execUsageError{"@code session not found: " + options.resume}
		}
	case options.resumeLatest:
		latest, err := store.LatestResumable()
		if err != nil {
			return err
		}
		if latest == nil {
			return execUsageError{"No @code sessions available to resume."}
		}
	}
	return nil
}

func createSessionTitle(prompt string) string {
	title := strings.Join(strings.Fields(prompt), " ")
	if len(title) > 80 {
		// Cut on a rune boundary so a multi-byte rune at the limit can't
		// persist invalid UTF-8 into the session metadata.
		title = cutRuneBoundary(title, 80)
	}
	if title == "" {
		return "@code exec session"
	}
	return title
}

func execSessionTitle(options execOptions, prompt string) string {
	if title := strings.TrimSpace(options.sessionTitle); title != "" {
		return title
	}
	return createSessionTitle(prompt)
}

func specialistAgentName(sessionTitle string) string {
	title := strings.TrimSpace(sessionTitle)
	if title == "" {
		return ""
	}
	if name, _, ok := strings.Cut(title, ":"); ok {
		return strings.TrimSpace(name)
	}
	return title
}

func (recorder *execSessionRecorder) append(eventType sessions.EventType, payload any) {
	if recorder.err != nil || recorder.prepared.Store == nil || recorder.prepared.Session.SessionID == "" {
		return
	}
	_, recorder.err = recorder.prepared.Store.AppendEvent(recorder.prepared.Session.SessionID, sessions.AppendEventInput{
		Type:    eventType,
		Payload: payload,
	})
}

// warnIfRecordingFailed surfaces a latched session-append failure to stderr.
// Recording is best-effort (it never fails the run), but a silent drop would let
// a user believe a session was persisted when it was not — so the first failure
// is reported once at run end. No-op when recording succeeded.
func (recorder *execSessionRecorder) warnIfRecordingFailed(stderr io.Writer) {
	if recorder.err != nil {
		fmt.Fprintf(stderr, "[@code] WARNING: session not fully recorded: %v\n", recorder.err)
	}
}
