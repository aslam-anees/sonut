package sessions

import (
	"encoding/json"
	"fmt"
	"log"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

type ExecMode string

const (
	ModeNew    ExecMode = "new"
	ModeResume ExecMode = "resume"
	ModeFork   ExecMode = "fork"
)

type ExecError struct {
	message string
}

func (err ExecError) Error() string {
	return err.message
}

type PrepareExecOptions struct {
	Store            *Store
	SessionID        string
	Title            string
	Cwd              string
	ModelID          string
	Provider         string
	Tag              string
	Depth            int
	CallingSessionID string
	CallingToolUseID string
	AgentName        string
	TaskID           string
	Resume           string
	ResumeLatest     bool
	Fork             string
}

type PreparedExec struct {
	Mode          ExecMode
	Session       Metadata
	ContextEvents []Event
	Store         *Store
}

// ConversationMessage is the model-facing portion of a persisted session.
// Keeping role and content separate lets interactive surfaces send history as
// real conversation turns instead of flattening it into the latest user
// prompt. Tool traffic is intentionally excluded here; it is audit data, not
// a safe substitute for a provider's structured tool-call protocol.
type ConversationMessage struct {
	Role    string
	Content string
}

const (
	maxConversationHistoryMessages = 24
	maxConversationHistoryChars    = 24000
)

// ConversationHistory extracts an ordered, bounded conversation from session
// events. Compaction summaries remain as assistant-side memory so /resume does
// not lose the decisions that were compressed out of the event log.
func ConversationHistory(events []Event) []ConversationMessage {
	history := make([]ConversationMessage, 0, len(events))
	for _, event := range events {
		switch event.Type {
		case EventMessage:
			var payload struct {
				Role    string `json:"role"`
				Content string `json:"content"`
			}
			if err := json.Unmarshal(event.Payload, &payload); err != nil {
				continue
			}
			role := strings.ToLower(strings.TrimSpace(payload.Role))
			// Older session logs sometimes recorded message content without a
			// role. Recover the alternating chat shape instead of silently
			// discarding that context on /resume.
			if role == "" {
				role = inferredConversationRole(history)
			}
			if role != "user" && role != "assistant" {
				continue
			}
			content := strings.TrimSpace(payload.Content)
			if content == "" {
				continue
			}
			history = append(history, ConversationMessage{Role: role, Content: content})
		case EventCompaction:
			var payload CompactionPayload
			if err := json.Unmarshal(event.Payload, &payload); err != nil {
				continue
			}
			if summary := strings.TrimSpace(payload.Summary); summary != "" {
				history = append(history, ConversationMessage{
					Role:    "assistant",
					Content: "[Earlier conversation summary]\n" + summary,
				})
			}
		}
	}

	// Preserve the latest turns first. This is a character guard as well as a
	// message guard: local hybrid models re-prefill this prefix on each turn,
	// so unbounded session history would make short follow-ups slow again.
	if len(history) > maxConversationHistoryMessages {
		history = history[len(history)-maxConversationHistoryMessages:]
	}
	chars := 0
	start := len(history)
	for i := len(history) - 1; i >= 0; i-- {
		chars += len(history[i].Content)
		if chars > maxConversationHistoryChars {
			break
		}
		start = i
	}
	if start > 0 {
		if start == len(history) {
			history = history[len(history)-1:]
			history[0].Content = truncateConversationContent(history[0].Content, maxConversationHistoryChars)
		} else {
			history = history[start:]
		}
	}
	return history
}

func inferredConversationRole(history []ConversationMessage) string {
	if len(history) == 0 || history[len(history)-1].Role == "assistant" {
		return "user"
	}
	return "assistant"
}

func truncateConversationContent(content string, maxChars int) string {
	if maxChars <= 0 || len(content) <= maxChars {
		return content
	}
	runes := []rune(content)
	if len(runes) <= maxChars {
		return content
	}
	front := maxChars / 2
	back := maxChars - front
	return string(runes[:front]) + "\n…[conversation turn truncated]…\n" + string(runes[len(runes)-back:])
}

var sessionPathTokenPattern = regexp.MustCompile(`(?:^|[\s"'()])((?:\./|\.\./|/)?[A-Za-z0-9_.-]+(?:/[A-Za-z0-9_.-]+)+|(?:\./|\.\./)?[A-Za-z0-9_.-]+\.(?:go|js|ts|tsx|jsx|py|rs|md|json|yaml|yml|toml|html|css|sql|sh|txt|csv))`)

var sessionPathKeys = map[string]bool{
	"path": true, "file": true, "file_path": true, "filepath": true,
	"filename": true, "directory": true, "dir": true, "workdir": true,
	"cwd": true, "target_file": true, "changedfiles": true,
}

func PrepareExec(options PrepareExecOptions) (PreparedExec, error) {
	resumeID := strings.TrimSpace(options.Resume)
	forkID := strings.TrimSpace(options.Fork)
	if (resumeID != "" || options.ResumeLatest) && forkID != "" {
		return PreparedExec{}, ExecError{"Use either --resume or --fork, not both."}
	}

	store := options.Store
	if store == nil {
		store = NewStore(StoreOptions{})
	}

	if forkID != "" {
		parent, err := store.Get(forkID)
		if err != nil {
			return PreparedExec{}, err
		}
		if parent == nil {
			return PreparedExec{}, ExecError{"@code session not found: " + forkID}
		}
		contextEvents, err := readExecContextEvents(store, parent.SessionID)
		if err != nil {
			return PreparedExec{}, err
		}
		session, err := store.Fork(parent.SessionID, ForkInput{
			SessionID: options.SessionID,
			Title:     firstNonEmpty(options.Title, forkTitle(parent.Title)),
			Cwd:       options.Cwd,
			ModelID:   options.ModelID,
			Provider:  options.Provider,
		})
		if err != nil {
			return PreparedExec{}, err
		}
		return PreparedExec{Mode: ModeFork, Session: session, ContextEvents: contextEvents, Store: store}, nil
	}

	if resumeID != "" || options.ResumeLatest {
		sessionID := resumeID
		if sessionID == "" && options.ResumeLatest {
			// Prefer a standalone conversation over a child/spec sub-run — same
			// rule as TUI `/resume latest` and `at --resume`.
			latest, err := store.LatestResumable()
			if err != nil {
				return PreparedExec{}, err
			}
			if latest == nil {
				return PreparedExec{}, ExecError{"No @code sessions available to resume."}
			}
			sessionID = latest.SessionID
		}
		session, err := store.Get(sessionID)
		if err != nil {
			return PreparedExec{}, err
		}
		if session == nil {
			return PreparedExec{}, ExecError{"@code session not found: " + sessionID}
		}
		contextEvents, err := readExecContextEvents(store, session.SessionID)
		if err != nil {
			return PreparedExec{}, err
		}
		return PreparedExec{Mode: ModeResume, Session: *session, ContextEvents: contextEvents, Store: store}, nil
	}

	createInput := CreateInput{
		SessionID: options.SessionID,
		Title:     options.Title,
		Cwd:       options.Cwd,
		ModelID:   options.ModelID,
		Provider:  options.Provider,
		Tag:       options.Tag,
		Depth:     options.Depth,
	}
	if strings.TrimSpace(options.CallingSessionID) != "" {
		parentSessionID := strings.TrimSpace(options.CallingSessionID)
		parent, err := store.Get(parentSessionID)
		if err != nil {
			return PreparedExec{}, err
		}
		if parent == nil {
			return PreparedExec{}, ExecError{"@code parent session not found: " + parentSessionID}
		}
		createInput.SessionKind = SessionKindChild
		createInput.ParentSessionID = parent.SessionID
		createInput.RootSessionID = firstNonEmpty(parent.RootSessionID, parent.SessionID)
		createInput.AgentName = strings.TrimSpace(options.AgentName)
		createInput.TaskID = strings.TrimSpace(firstNonEmpty(options.TaskID, options.SessionID))
		createInput.SpawnedFromEventID = strings.TrimSpace(options.CallingToolUseID)
	}
	session, err := store.Create(createInput)
	if err != nil {
		return PreparedExec{}, err
	}
	return PreparedExec{Mode: ModeNew, Session: session, ContextEvents: []Event{}, Store: store}, nil
}

func readExecContextEvents(store *Store, sessionID string) ([]Event, error) {
	contextEvents, err := store.ReadRehydratedEvents(sessionID)
	if err == nil {
		return contextEvents, nil
	}
	rawEvents, rawErr := store.ReadEvents(sessionID)
	if rawErr != nil {
		return nil, err
	}
	log.Printf("at sessions: failed to rehydrate compaction events for %s; falling back to raw events: %v", sessionID, err)
	return rawEvents, nil
}

func FormatExecPrompt(prompt string, prepared PreparedExec) string {
	if prepared.Mode == ModeNew || len(prepared.ContextEvents) == 0 {
		return prompt
	}
	// The continuation context must carry the CONVERSATION — user asks,
	// assistant conclusions, plan updates, compaction summaries, errors — not
	// raw tool traffic. A single tool-heavy turn emits 20+ tool_call/tool_result
	// events; with a flat last-N window those pushed out the assistant's most
	// recent decisions, so a continued run reasoned from a stale approach
	// (observed: kept "fixing" an API the previous turn had already replaced).
	narrative := make([]Event, 0, len(prepared.ContextEvents))
	var toolTail []Event
	var lastPlan *Event
	for i, event := range prepared.ContextEvents {
		switch event.Type {
		case EventMessage, EventCompaction, EventError:
			narrative = append(narrative, event)
		case EventToolCall, EventToolResult:
			// The latest update_plan call IS narrative — it carries the current
			// plan state the continued run must work from.
			if event.Type == EventToolCall && strings.Contains(string(event.Payload), `"update_plan"`) {
				lastPlan = &prepared.ContextEvents[i]
				continue
			}
			toolTail = append(toolTail, event)
		}
	}
	if lastPlan != nil {
		narrative = append(narrative, *lastPlan)
	}
	// Keep the resume preamble tight: local hybrid models re-prefill this
	// blob on every turn (no KV prefix reuse), so a long history alone can
	// cost tens of seconds before thinking starts. Cap balances speed with
	// enough recent turns that /compact summaries still fit.
	// Always keep EventCompaction summaries even when trimming the tail —
	// they are the only record of dropped history.
	if len(narrative) > 10 {
		var keep []Event
		for _, event := range narrative {
			if event.Type == EventCompaction {
				keep = append(keep, event)
			}
		}
		// Tail of the non-compaction narrative (last 10 - N compaction events).
		tailBudget := 10 - len(keep)
		if tailBudget < 4 {
			tailBudget = 4 // never starve the live window entirely
		}
		var rest []Event
		for _, event := range narrative {
			if event.Type != EventCompaction {
				rest = append(rest, event)
			}
		}
		if len(rest) > tailBudget {
			rest = rest[len(rest)-tailBudget:]
		}
		narrative = append(keep, rest...)
	}
	// A few of the most recent tool events still help ("what just ran"), merged
	// back in sequence order after the narrative slice is chosen.
	if len(toolTail) > 3 {
		toolTail = toolTail[len(toolTail)-3:]
	}
	events := append(narrative, toolTail...)
	sort.SliceStable(events, func(i, j int) bool { return events[i].Sequence < events[j].Sequence })

	lines := []string{}
	for _, event := range events {
		lines = append(lines, fmt.Sprintf("- #%d %s: %s", event.Sequence, event.Type, summarizePayload(event.Payload)))
	}
	label := "Continuing"
	sessionID := prepared.Session.SessionID
	if prepared.Mode == ModeFork {
		label = "Forked from"
		if prepared.Session.ParentSessionID != "" {
			sessionID = prepared.Session.ParentSessionID
		}
	}
	contextLines := []string{
		fmt.Sprintf("%s @code session %s.", label, sessionID),
		"Previous session context:",
		strings.Join(lines, "\n"),
	}
	if pathHints := collectSessionPathHints(prepared.ContextEvents, prepared.Session.Cwd); len(pathHints) > 0 {
		contextLines = append(contextLines, "", "Known workspace paths from earlier turns (use these to resolve references such as ‘that file’ or ‘that path’): "+strings.Join(pathHints, ", "))
	}
	contextLines = append(contextLines, "", "Current user request:", prompt)
	return strings.Join(contextLines, "\n")
}

func collectSessionPathHints(events []Event, cwd string) []string {
	seen := make(map[string]bool)
	var hints []string
	add := func(raw string) {
		raw = strings.TrimSpace(strings.Trim(raw, "\"'`"))
		raw = strings.TrimRight(raw, ".,;:)]}")
		if raw == "" || raw == "." {
			return
		}
		if filepath.IsAbs(raw) {
			if cwd == "" {
				return
			}
			abs, err := filepath.Abs(cwd)
			if err != nil {
				return
			}
			rel, err := filepath.Rel(abs, filepath.Clean(raw))
			if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) || filepath.IsAbs(rel) {
				return
			}
			raw = rel
		}
		raw = filepath.ToSlash(filepath.Clean(strings.TrimPrefix(raw, "./")))
		if raw == "" || raw == "." || raw == ".." || strings.HasPrefix(raw, "../") || seen[raw] {
			return
		}
		seen[raw] = true
		hints = append(hints, raw)
	}
	var visit func(any)
	visit = func(value any) {
		switch typed := value.(type) {
		case map[string]any:
			for childKey, child := range typed {
				lowerKey := strings.ToLower(childKey)
				switch {
				case lowerKey == "arguments":
					if raw, ok := child.(string); ok {
						var nested any
						if json.Unmarshal([]byte(raw), &nested) == nil {
							visit(nested)
						}
					}
				case sessionPathKeys[lowerKey]:
					switch pathValue := child.(type) {
					case string:
						if lowerKey == "changedfiles" {
							for _, token := range sessionPathTokenPattern.FindAllStringSubmatch(pathValue, -1) {
								if len(token) > 1 {
									add(token[1])
								}
							}
						} else {
							add(pathValue)
						}
					case []any:
						for _, item := range pathValue {
							if text, ok := item.(string); ok {
								add(text)
							}
						}
					}
				}
				if lowerKey != "arguments" {
					visit(child)
				}
			}
		case []any:
			for _, item := range typed {
				visit(item)
			}
		}
	}
	for _, event := range events {
		var decoded any
		if json.Unmarshal(event.Payload, &decoded) != nil {
			continue
		}
		visit(decoded)
		if event.Type == EventMessage {
			text := extractText(decoded)
			for _, token := range sessionPathTokenPattern.FindAllStringSubmatch(text, -1) {
				if len(token) > 1 {
					add(token[1])
				}
			}
		}
	}
	if len(hints) > 12 {
		hints = hints[len(hints)-12:]
	}
	return hints
}

func forkTitle(title string) string {
	if title == "" {
		return ""
	}
	return title + " (fork)"
}

func summarizePayload(payload any) string {
	text := extractText(payload)
	text = strings.Join(strings.Fields(text), " ")
	if text == "" {
		data, err := json.Marshal(payload)
		if err != nil {
			return "{}"
		}
		text = string(data)
	}
	if len(text) > 240 {
		return text[:240]
	}
	return text
}

func extractText(value any) string {
	switch typed := value.(type) {
	case string:
		return typed
	case json.RawMessage:
		var decoded any
		if err := json.Unmarshal(typed, &decoded); err == nil {
			return extractText(decoded)
		}
		return string(typed)
	case float64, bool, int:
		return fmt.Sprint(typed)
	case []any:
		parts := []string{}
		for _, item := range typed {
			if text := extractText(item); text != "" {
				parts = append(parts, text)
			}
		}
		return strings.Join(parts, " ")
	case map[string]any:
		if summary, ok := typed["summary"].(string); ok && strings.TrimSpace(summary) != "" {
			return summary
		}
		parts := []string{}
		for _, item := range typed {
			if text := extractText(item); text != "" {
				parts = append(parts, text)
			}
		}
		return strings.Join(parts, " ")
	default:
		return ""
	}
}
