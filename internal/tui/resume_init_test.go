package tui

import (
	"context"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/aistatine/sonut/internal/sessions"
)

// TestInitResumeFlagHydratesSession is the CLI `at --resume <id>` path:
// Options.ResumeSessionID → Init emits commandResume → Update must hydrate.
// Regression: without a `case parsedCommand` in updateModel, the Init message
// was dropped and --resume always opened a blank session.
func TestInitResumeFlagHydratesSession(t *testing.T) {
	store := sessions.NewStore(sessions.StoreOptions{RootDir: t.TempDir()})
	session, err := store.Create(sessions.CreateInput{
		Title:    "resume-init-test",
		Cwd:      t.TempDir(),
		ModelID:  "test-model",
		Provider: "llama",
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if _, err := store.AppendEvent(session.SessionID, sessions.AppendEventInput{
		Type:    sessions.EventMessage,
		Payload: map[string]any{"role": "user", "content": "hello from prior turn"},
	}); err != nil {
		t.Fatalf("AppendEvent user: %v", err)
	}
	if _, err := store.AppendEvent(session.SessionID, sessions.AppendEventInput{
		Type:    sessions.EventMessage,
		Payload: map[string]any{"role": "assistant", "content": "prior answer"},
	}); err != nil {
		t.Fatalf("AppendEvent assistant: %v", err)
	}

	m := newModel(context.Background(), Options{
		SessionStore:    store,
		ResumeSessionID: session.SessionID,
	})
	if m.initialResumeID != session.SessionID {
		t.Fatalf("initialResumeID = %q, want %q", m.initialResumeID, session.SessionID)
	}

	foundResume := false
	for _, msg := range expandTeaCmds(m.Init()) {
		if pc, ok := msg.(parsedCommand); ok && pc.kind == commandResume {
			foundResume = true
			if pc.text != session.SessionID {
				t.Fatalf("resume command text = %q, want %q", pc.text, session.SessionID)
			}
		}
		next, _ := m.Update(msg)
		var ok bool
		m, ok = next.(model)
		if !ok {
			t.Fatalf("Update returned %T, want model", next)
		}
	}
	if !foundResume {
		t.Fatal("Init batch did not include commandResume")
	}
	if m.activeSession.SessionID != session.SessionID {
		t.Fatalf("after Init resume: activeSession = %q, want %q", m.activeSession.SessionID, session.SessionID)
	}
	if !transcriptContains(m.transcript, "Resumed @code session") {
		t.Fatalf("expected resume summary in transcript, got %#v", m.transcript)
	}
	if !transcriptContains(m.transcript, "hello from prior turn") || !transcriptContains(m.transcript, "prior answer") {
		t.Fatalf("expected prior turns in resumed transcript, got %#v", m.transcript)
	}
	if history := sessionConversationHistory(m.sessionEvents); len(history) < 2 {
		t.Fatalf("conversation history too short for follow-up turns: %#v", history)
	}
}

func TestInitResumeLatestHydratesNewest(t *testing.T) {
	// Force distinct UpdatedAt stamps so the test asserts ordering, not the
	// same-second SessionID tiebreaker (covered in sessions store tests).
	now := time.Date(2026, 7, 30, 12, 0, 0, 0, time.UTC)
	store := sessions.NewStore(sessions.StoreOptions{
		RootDir: t.TempDir(),
		Now: func() time.Time {
			cur := now
			now = now.Add(2 * time.Second)
			return cur
		},
	})
	older, err := store.Create(sessions.CreateInput{Title: "older", Cwd: "/", ModelID: "m", Provider: "p"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.AppendEvent(older.SessionID, sessions.AppendEventInput{
		Type: sessions.EventMessage, Payload: map[string]any{"role": "user", "content": "old prompt"},
	}); err != nil {
		t.Fatal(err)
	}
	newer, err := store.Create(sessions.CreateInput{Title: "newer", Cwd: "/", ModelID: "m", Provider: "p"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.AppendEvent(newer.SessionID, sessions.AppendEventInput{
		Type: sessions.EventMessage, Payload: map[string]any{"role": "user", "content": "new prompt"},
	}); err != nil {
		t.Fatal(err)
	}

	m := newModel(context.Background(), Options{
		SessionStore:    store,
		ResumeSessionID: "latest",
	})
	for _, msg := range expandTeaCmds(m.Init()) {
		next, _ := m.Update(msg)
		m = next.(model)
	}
	if m.activeSession.SessionID != newer.SessionID {
		t.Fatalf("latest resume = %q, want %q", m.activeSession.SessionID, newer.SessionID)
	}
	if !transcriptContains(m.transcript, "new prompt") {
		t.Fatalf("expected newer session content, got %#v", m.transcript)
	}
	if transcriptContains(m.transcript, "old prompt") {
		t.Fatalf("should not include older session, got %#v", m.transcript)
	}
}

func TestInitResumeByTitleHydratesSession(t *testing.T) {
	store := sessions.NewStore(sessions.StoreOptions{RootDir: t.TempDir()})
	session, err := store.Create(sessions.CreateInput{
		Title: "ALHAMDULILLAH", Cwd: "/", ModelID: "m", Provider: "p",
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.AppendEvent(session.SessionID, sessions.AppendEventInput{
		Type: sessions.EventMessage, Payload: map[string]any{"role": "user", "content": "greeting turn"},
	}); err != nil {
		t.Fatal(err)
	}

	// Title is a ValidSessionID shape ("ALHAMDULILLAH") but is not a session id —
	// resolveResumeSession must fall through to title search.
	m := newModel(context.Background(), Options{
		SessionStore:    store,
		ResumeSessionID: "ALHAMDULILLAH",
	})
	for _, msg := range expandTeaCmds(m.Init()) {
		next, _ := m.Update(msg)
		m = next.(model)
	}
	if m.activeSession.SessionID != session.SessionID {
		t.Fatalf("title resume = %q, want %q (title ALHAMDULILLAH)", m.activeSession.SessionID, session.SessionID)
	}
	if !transcriptContains(m.transcript, "greeting turn") {
		t.Fatalf("expected title-resumed transcript, got %#v", m.transcript)
	}
}

func TestResolveResumeByDuplicateTitlePrefersNewest(t *testing.T) {
	now := time.Date(2026, 7, 30, 12, 0, 0, 0, time.UTC)
	store := sessions.NewStore(sessions.StoreOptions{
		RootDir: t.TempDir(),
		Now: func() time.Time {
			current := now
			now = now.Add(2 * time.Second)
			return current
		},
	})
	create := func(prompt string) sessions.Metadata {
		t.Helper()
		session, err := store.Create(sessions.CreateInput{
			Title: "Build the landing page", Cwd: "/", ModelID: "m", Provider: "p",
		})
		if err != nil {
			t.Fatalf("create session: %v", err)
		}
		if _, err := store.AppendEvent(session.SessionID, sessions.AppendEventInput{
			Type:    sessions.EventMessage,
			Payload: map[string]any{"role": "user", "content": prompt},
		}); err != nil {
			t.Fatalf("append prompt: %v", err)
		}
		return session
	}
	older := create("older landing page prompt")
	newer := create("newer landing page prompt")

	m := newModel(context.Background(), Options{SessionStore: store})
	got, err := m.resolveResumeSession("Build the landing page")
	if err != nil {
		t.Fatalf("resolve duplicate title: %v", err)
	}
	if got == nil || got.SessionID != newer.SessionID {
		t.Fatalf("duplicate title resolved to %#v, want newest session %q (older was %q)", got, newer.SessionID, older.SessionID)
	}
}

func TestInitResumeBySessionIDPrefix(t *testing.T) {
	store := sessions.NewStore(sessions.StoreOptions{RootDir: t.TempDir()})
	session, err := store.Create(sessions.CreateInput{
		Title: "prefix-test", Cwd: "/", ModelID: "m", Provider: "p",
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.AppendEvent(session.SessionID, sessions.AppendEventInput{
		Type: sessions.EventMessage, Payload: map[string]any{"role": "user", "content": "prefixed"},
	}); err != nil {
		t.Fatal(err)
	}
	// Use a long-enough unique prefix of the generated id.
	prefix := session.SessionID
	if len(prefix) > 24 {
		prefix = prefix[:24]
	}

	m := newModel(context.Background(), Options{
		SessionStore:    store,
		ResumeSessionID: prefix,
	})
	for _, msg := range expandTeaCmds(m.Init()) {
		next, _ := m.Update(msg)
		m = next.(model)
	}
	if m.activeSession.SessionID != session.SessionID {
		t.Fatalf("prefix resume = %q, want %q (prefix %q)", m.activeSession.SessionID, session.SessionID, prefix)
	}
}

// expandTeaCmds runs a tea.Cmd tree (including Batch) and returns every
// produced message. Closures that would hit the network are still invoked —
// keep tests on models without live discovery providers.
func expandTeaCmds(cmd tea.Cmd) []tea.Msg {
	if cmd == nil {
		return nil
	}
	msg := cmd()
	if msg == nil {
		return nil
	}
	batch, ok := msg.(tea.BatchMsg)
	if !ok {
		return []tea.Msg{msg}
	}
	var out []tea.Msg
	for _, sub := range batch {
		out = append(out, expandTeaCmds(sub)...)
	}
	return out
}
