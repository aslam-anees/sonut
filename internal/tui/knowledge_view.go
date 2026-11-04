package tui

import (
	"context"
	"fmt"
	"hash/fnv"
	"math"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"charm.land/bubbles/v2/textarea"
	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/colorprofile"

	"github.com/aistatine/sonut/internal/knowledge"
	"github.com/aistatine/sonut/internal/tools"
)

type knowledgeTab int

const (
	knowledgeTabAtlas knowledgeTab = iota
	knowledgeTabNotes
	knowledgeTabSearch
	knowledgeTabSources
	knowledgeTabTimeline
	knowledgeTabInsights
	knowledgeTabCount
)

var knowledgeTabNames = [...]string{"Atlas", "Notes", "Search", "Sources", "Timeline", "Insights"}

type knowledgeSearchLens int

const (
	knowledgeLensLocal knowledgeSearchLens = iota
	knowledgeLensConnected
	knowledgeLensOverview
)

func (l knowledgeSearchLens) String() string {
	switch l {
	case knowledgeLensConnected:
		return "connected"
	case knowledgeLensOverview:
		return "overview"
	default:
		return "local"
	}
}

func (l knowledgeSearchLens) domain() knowledge.SearchLens {
	switch l {
	case knowledgeLensConnected:
		return knowledge.SearchConnected
	case knowledgeLensOverview:
		return knowledge.SearchOverview
	default:
		return knowledge.SearchLocal
	}
}

type knowledgeViewState struct {
	active             bool
	loading            bool
	loadSeq            int
	generation         int
	parentScrollOffset int
	parentComposer     composerState
	parentComposerLive bool
	tab                knowledgeTab
	notes              []knowledge.Note
	graph              knowledge.Graph
	selected           int
	selectedID         string
	expanded           bool
	graphDepth         int
	grouping           int
	filter             int
	timelineGrouping   int
	paused             bool
	frame              int
	pinned             map[string]bool
	notice             string
	searchQuery        string
	searchLens         knowledgeSearchLens
	searchResults      []knowledge.SearchHit
	searchInput        textinput.Model
	searchFocused      bool
	webResults         []knowledgeWebResult
	webQuery           string
	webPreview         *knowledgeWebPreview
	webHistory         []knowledgeWebPreview
	webHistoryIndex    int
	help               bool
	confirmDelete      string
	editor             knowledgeEditorState
}

type knowledgeEditorField int

const (
	knowledgeEditorTitle knowledgeEditorField = iota
	knowledgeEditorTags
	knowledgeEditorBody
	knowledgeEditorSource
	knowledgeEditorFieldCount
)

type knowledgeEditorState struct {
	active         bool
	editing        bool
	field          knowledgeEditorField
	title          textinput.Model
	tags           textinput.Model
	source         textinput.Model
	body           textarea.Model
	existing       *knowledge.Note
	frontmatter    string
	savePending    bool
	webQuery       string
	webPreview     string
	createdFromWeb bool
}

type knowledgeLoadedMsg struct {
	seq    int
	query  string
	lens   knowledgeSearchLens
	notes  []knowledge.Note
	graph  knowledge.Graph
	search []knowledge.SearchHit
	err    error
}

type knowledgeWebSearchMsg struct {
	generation int
	query      string
	results    []knowledgeWebResult
	offline    bool
	err        error
}

type knowledgeWebFetchMsg struct {
	generation int
	result     knowledgeWebPreview
	err        error
}

type knowledgeSaveMsg struct {
	generation int
	path       string
	summary    string
	err        error
}

type knowledgeTrashMsg struct {
	generation int
	path       string
	err        error
}

type knowledgeWebResult struct {
	Title   string
	URL     string
	Domain  string
	Snippet string
	Score   string
}

type knowledgeWebPreview struct {
	Title string
	URL   string
	Body  string
	Query string
}

const (
	knowledgeMaxVisibleNodes = 140
	knowledgeDefaultDepth    = 1
	knowledgeGraphHeight     = 21
)

func (m model) startKnowledgeCommand(argText string) (model, tea.Cmd) {
	args := strings.TrimSpace(argText)
	lower := strings.ToLower(args)
	switch {
	case strings.HasPrefix(lower, "add") && (len(args) == 3 || strings.HasPrefix(lower, "add ")):
		rest := strings.TrimSpace(args[3:])
		if rest == "" {
			return m.appendKnowledgeNotice("Usage: /knowledge add <title> :: <content>  (content may contain [[wikilinks]] and #tags)"), nil
		}
		title, content := rest, ""
		if idx := strings.Index(rest, "::"); idx >= 0 {
			title = strings.TrimSpace(rest[:idx])
			content = strings.TrimSpace(rest[idx+2:])
		}
		return m, func() tea.Msg {
			path, err := knowledge.AddNote(title, content)
			return knowledgeSaveMsg{generation: m.knowledgeView.generation, path: path, summary: fmt.Sprintf("Saved note %q → %s", title, path), err: err}
		}
	case lower == "list":
		return m.openKnowledgeView(knowledgeTabNotes, "", knowledgeLensLocal)
	case lower == "stats":
		return m.openKnowledgeView(knowledgeTabInsights, "", knowledgeLensOverview)
	case strings.HasPrefix(lower, "search") && (len(args) == 6 || strings.HasPrefix(lower, "search ")):
		query := strings.TrimSpace(args[6:])
		if query == "" {
			return m.appendKnowledgeNotice("Usage: /knowledge search <query>  (press m in Search to switch local lenses)"), nil
		}
		return m.openKnowledgeView(knowledgeTabSearch, query, knowledgeLensLocal)
	case strings.HasPrefix(lower, "web") && (len(args) == 3 || strings.HasPrefix(lower, "web ")):
		query := strings.TrimSpace(args[3:])
		if query == "" {
			return m.appendKnowledgeNotice("Usage: /knowledge web <query>  (explicit network search; local search remains available)"), nil
		}
		return m.openKnowledgeWebSearch(query)
	case lower == "open browser":
		return m, func() tea.Msg {
			path, nodes, links, err := knowledge.ExportHTML()
			if err != nil {
				return knowledgeSaveMsg{generation: m.knowledgeView.generation, err: err}
			}
			if err := knowledge.OpenInBrowser(path); err != nil {
				return knowledgeSaveMsg{generation: m.knowledgeView.generation, path: fmt.Sprintf("EXPORT|%s|%d|%d|%v", path, nodes, links, err)}
			}
			return knowledgeSaveMsg{generation: m.knowledgeView.generation, path: fmt.Sprintf("EXPORT|%s|%d|%d", path, nodes, links)}
		}
	case lower == "export":
		return m, func() tea.Msg {
			path, nodes, links, err := knowledge.ExportHTML()
			if err != nil {
				return knowledgeSaveMsg{generation: m.knowledgeView.generation, err: err}
			}
			return knowledgeSaveMsg{generation: m.knowledgeView.generation, path: fmt.Sprintf("EXPORT|%s|%d|%d", path, nodes, links)}
		}
	case args == "" || lower == "view":
		return m.openKnowledgeView(knowledgeTabAtlas, "", knowledgeLensLocal)
	default:
		return m.appendKnowledgeNotice("Usage: /knowledge | /knowledge view | /knowledge add <title> :: <content> | /knowledge search <query> | /knowledge web <query> | /knowledge list | /knowledge stats | /knowledge export | /knowledge open browser"), nil
	}
}

// startResearchCommand is the concise product surface for Phase 3 native
// research. It intentionally reuses the Knowledge Atlas web pipeline so every
// result keeps its URL/domain, page preview, offline/error state, and citation
// navigation instead of creating a second browser/search implementation.
func (m model) startResearchCommand(query string) (model, tea.Cmd) {
	query = strings.TrimSpace(query)
	if query == "" {
		return m.appendKnowledgeNotice("Usage: /research <query> (network search; results retain source URLs and previews)"), nil
	}
	return m.openKnowledgeWebSearch(query)
}

func (m model) appendKnowledgeNotice(text string) model {
	m.transcript = reduceTranscript(m.transcript, transcriptAction{kind: actionAppendSystem, text: text})
	return m
}

func (m model) openKnowledgeView(tab knowledgeTab, query string, lens knowledgeSearchLens) (model, tea.Cmd) {
	m.knowledgeSeq++
	m.knowledgeGeneration++
	parent := m.chatScrollOffset
	parentComposer := m.currentComposerState()
	parentComposerLive := m.composerActive
	if m.knowledgeView.active {
		parent = m.knowledgeView.parentScrollOffset
		parentComposer = m.knowledgeView.parentComposer
		parentComposerLive = m.knowledgeView.parentComposerLive
	}
	m.knowledgeView = knowledgeViewState{
		active:             true,
		loading:            true,
		loadSeq:            m.knowledgeSeq,
		generation:         m.knowledgeGeneration,
		parentScrollOffset: parent,
		parentComposer:     parentComposer,
		parentComposerLive: parentComposerLive,
		tab:                tab,
		graphDepth:         knowledgeDefaultDepth,
		searchQuery:        query,
		searchLens:         lens,
		pinned:             map[string]bool{},
		searchInput:        knowledgeSearchInput(query),
	}
	seq := m.knowledgeView.loadSeq
	generation := m.knowledgeView.generation
	m.chatScrollOffset = 0
	m.clearComposer()
	m.clearSuggestions()
	m = m.clearHover()
	return m, knowledgeLoadCmd(seq, query, lens, generation)
}

func knowledgeSearchInput(value string) textinput.Model {
	in := textinput.New()
	in.Prompt = "  / "
	in.Placeholder = "Search local knowledge…"
	in.SetValue(value)
	in.SetWidth(52)
	return in
}

func knowledgeLoadCmd(seq int, query string, lens knowledgeSearchLens, generation int) tea.Cmd {
	return func() tea.Msg {
		notes, err := knowledge.LoadNotes()
		if err != nil {
			return knowledgeLoadedMsg{seq: seq, query: query, lens: lens, err: err}
		}
		graph := knowledge.BuildAtlasGraph(notes)
		var search []knowledge.SearchHit
		if strings.TrimSpace(query) != "" {
			backend, searchErr := knowledge.NewSearchBackend()
			if searchErr != nil {
				return knowledgeLoadedMsg{seq: seq, query: query, lens: lens, notes: notes, graph: graph, err: searchErr}
			}
			search, err = backend.SearchLens(query, graph, notes, lens.domain(), 24)
			if err != nil {
				return knowledgeLoadedMsg{seq: seq, query: query, lens: lens, notes: notes, graph: graph, err: err}
			}
		}
		_ = generation // generation travels on the view; seq is the load fence.
		return knowledgeLoadedMsg{seq: seq, query: query, lens: lens, notes: notes, graph: graph, search: search}
	}
}

func (m model) openKnowledgeWebSearch(query string) (model, tea.Cmd) {
	m, cmd := m.openKnowledgeView(knowledgeTabSearch, query, knowledgeLensLocal)
	m.knowledgeView.webQuery = query
	m.knowledgeView.notice = "NETWORK · searching explicitly…"
	generation := m.knowledgeView.generation
	return m, tea.Batch(cmd, m.knowledgeWebSearchCmd(query, generation))
}

func (m model) knowledgeWebSearchCmd(query string, generation int) tea.Cmd {
	return func() tea.Msg {
		if !m.networkEnabled {
			return knowledgeWebSearchMsg{generation: generation, query: query, offline: true}
		}
		var result tools.Result
		if m.registry != nil {
			result = m.registry.RunWithOptions(context.Background(), "web_search", map[string]any{"query": query, "limit": 8}, tools.RunOptions{PermissionGranted: true, PermissionMode: string(m.permissionMode)})
		} else {
			result = tools.NewWebSearchTool().Run(context.Background(), map[string]any{"query": query, "limit": 8})
		}
		if result.Status != tools.StatusOK {
			return knowledgeWebSearchMsg{generation: generation, query: query, err: fmt.Errorf("%s", result.Output)}
		}
		if result.Meta["network_unavailable"] == "true" || strings.TrimSpace(result.Output) == "" {
			return knowledgeWebSearchMsg{generation: generation, query: query, offline: true}
		}
		return knowledgeWebSearchMsg{generation: generation, query: query, results: parseKnowledgeWebResults(result.Output)}
	}
}

func parseKnowledgeWebResults(output string) []knowledgeWebResult {
	var results []knowledgeWebResult
	lines := strings.Split(output, "\n")
	for i := 0; i < len(lines); i++ {
		line := strings.TrimSpace(lines[i])
		if len(line) < 4 || line[0] < '1' || line[0] > '9' {
			continue
		}
		dot := strings.Index(line, ". ")
		if dot < 0 {
			continue
		}
		rest := strings.TrimSpace(line[dot+2:])
		urlIndex := strings.Index(rest, " — http")
		if urlIndex < 0 {
			continue
		}
		title := strings.TrimSpace(rest[:urlIndex])
		urlAndScore := strings.TrimSpace(rest[urlIndex+3:])
		pageURL := urlAndScore
		score := ""
		if scoreIndex := strings.Index(urlAndScore, " — score "); scoreIndex >= 0 {
			pageURL = strings.TrimSpace(urlAndScore[:scoreIndex])
			score = strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(urlAndScore[scoreIndex:]), "— score "))
		}
		snippet := ""
		if i+1 < len(lines) && strings.HasPrefix(lines[i+1], "   ") {
			snippet = strings.TrimSpace(lines[i+1])
			i++
		}
		domain := pageURL
		if parsed, err := url.Parse(pageURL); err == nil {
			domain = parsed.Hostname()
		}
		results = append(results, knowledgeWebResult{Title: title, URL: pageURL, Domain: domain, Snippet: snippet, Score: score})
	}
	return results
}

func (m model) closeKnowledgeView() model {
	if !m.knowledgeView.active {
		return m
	}
	parentScrollOffset := m.knowledgeView.parentScrollOffset
	parentComposer := m.knowledgeView.parentComposer
	parentComposerLive := m.knowledgeView.parentComposerLive
	m.chatScrollOffset = parentScrollOffset
	m.knowledgeView = knowledgeViewState{}
	m.clearComposer()
	if parentComposerLive {
		m.setComposerState(parentComposer)
	} else {
		m.input.SetValue(parentComposer.text)
		m.input.SetCursor(parentComposer.cursor)
	}
	m.clearSuggestions()
	m = m.clearHover()
	return m
}

func (m model) applyKnowledgeLoaded(msg knowledgeLoadedMsg) model {
	if !m.knowledgeView.active || msg.seq != m.knowledgeView.loadSeq {
		return m
	}
	m.knowledgeView.loading = false
	if msg.err != nil {
		m.knowledgeView.notice = "Local index unavailable: " + msg.err.Error()
		return m
	}
	m.knowledgeView.notes = msg.notes
	m.knowledgeView.graph = msg.graph
	m.knowledgeView.searchResults = msg.search
	m.knowledgeView.searchQuery = msg.query
	m.knowledgeView.searchLens = msg.lens
	if msg.query != "" && len(msg.search) > 0 {
		m.knowledgeView.selectedID = msg.search[0].NoteName
	} else if m.knowledgeView.selectedID == "" && len(msg.graph.Nodes) > 0 {
		m.knowledgeView.selectedID = msg.graph.Nodes[0].ID
	}
	if m.knowledgeView.selectedID != "" && m.knowledgeNode(m.knowledgeView.selectedID) == nil && len(msg.graph.Nodes) > 0 {
		m.knowledgeView.selectedID = msg.graph.Nodes[0].ID
	}
	m.knowledgeView.selected = m.knowledgeSelectedIndex()
	if msg.query != "" {
		m.knowledgeView.notice = fmt.Sprintf("LOCAL · %d result(s) · BM25 + deterministic graph signals", len(msg.search))
	}
	return m
}

func (m model) applyKnowledgeWebSearch(msg knowledgeWebSearchMsg) model {
	if !m.knowledgeView.active || msg.generation != m.knowledgeView.generation || msg.query != m.knowledgeView.webQuery {
		return m
	}
	if msg.err != nil {
		m.knowledgeView.webResults = nil
		m.knowledgeView.notice = "NETWORK blocked or unavailable · Local results only"
		return m
	}
	m.knowledgeView.webResults = msg.results
	if msg.offline {
		m.knowledgeView.notice = "NETWORK unavailable · Local results only"
	} else {
		m.knowledgeView.notice = fmt.Sprintf("NETWORK · %d ranked result(s) · explicit request", len(msg.results))
	}
	m.knowledgeView.selected = 0
	if len(msg.results) > 0 {
		m.knowledgeView.selectedID = "web:0"
	} else {
		m.knowledgeView.selectedID = ""
	}
	return m
}

func (m model) knowledgeWebFetchCmd(result knowledgeWebResult, generation int, query string) tea.Cmd {
	return func() tea.Msg {
		if !m.networkEnabled {
			return knowledgeWebFetchMsg{generation: generation, err: fmt.Errorf("network is disabled; local results only")}
		}
		var fetched tools.Result
		args := map[string]any{"url": result.URL, "max_bytes": 65536}
		if m.registry != nil {
			fetched = m.registry.RunWithOptions(context.Background(), "web_fetch", args, tools.RunOptions{PermissionGranted: true, PermissionMode: string(m.permissionMode)})
		} else {
			fetched = tools.NewWebFetchTool().Run(context.Background(), args)
		}
		if fetched.Status != tools.StatusOK {
			return knowledgeWebFetchMsg{generation: generation, err: fmt.Errorf("%s", fetched.Output)}
		}
		body := fetched.Output
		if blank := strings.Index(body, "\n\n"); blank >= 0 {
			body = body[blank+2:]
		}
		return knowledgeWebFetchMsg{generation: generation, result: knowledgeWebPreview{Title: result.Title, URL: result.URL, Body: strings.TrimSpace(body), Query: query}}
	}
}

func (m model) applyKnowledgeWebFetch(msg knowledgeWebFetchMsg) model {
	if !m.knowledgeView.active || msg.generation != m.knowledgeView.generation {
		return m
	}
	if msg.err != nil {
		m.knowledgeView.notice = "Could not preview source · Local results only"
		return m
	}
	preview := msg.result
	if len(preview.Body) > 24000 {
		preview.Body = preview.Body[:24000] + "\n… page preview bounded at 24KB"
	}
	m.knowledgeView.webPreview = &preview
	m.knowledgeView.webHistory = append(m.knowledgeView.webHistory, preview)
	m.knowledgeView.webHistoryIndex = len(m.knowledgeView.webHistory) - 1
	m.knowledgeView.notice = "NETWORK · untrusted page text · s saves an exact note preview"
	return m
}

func (m model) knowledgeSelectedNode() *knowledge.GraphNode {
	id := m.knowledgeView.selectedID
	for i := range m.knowledgeView.graph.Nodes {
		if m.knowledgeView.graph.Nodes[i].ID == id {
			return &m.knowledgeView.graph.Nodes[i]
		}
	}
	return nil
}

func (m model) knowledgeSelectedIndex() int {
	items := m.knowledgeSelectionIDs()
	if len(items) == 0 {
		return 0
	}
	for i, id := range items {
		if id == m.knowledgeView.selectedID {
			return i
		}
	}
	return clampInt(m.knowledgeView.selected, 0, len(items)-1)
}

func (m model) knowledgeSelectionIDs() []string {
	switch m.knowledgeView.tab {
	case knowledgeTabSearch:
		if len(m.knowledgeView.webResults) > 0 {
			ids := make([]string, len(m.knowledgeView.webResults))
			for i := range ids {
				ids[i] = fmt.Sprintf("web:%d", i)
			}
			return ids
		}
		ids := make([]string, len(m.knowledgeView.searchResults))
		for i, hit := range m.knowledgeView.searchResults {
			ids[i] = hit.NoteName
		}
		return ids
	case knowledgeTabAtlas, knowledgeTabNotes, knowledgeTabSources, knowledgeTabTimeline, knowledgeTabInsights:
		ids := make([]string, 0, len(m.knowledgeView.graph.Nodes))
		for _, node := range m.knowledgeView.graph.Nodes {
			if node.Kind == knowledge.NodeTag && m.knowledgeView.tab != knowledgeTabAtlas {
				continue
			}
			ids = append(ids, node.ID)
		}
		sort.Strings(ids)
		return ids
	default:
		return nil
	}
}

func (m model) selectKnowledgeDelta(delta int) model {
	ids := m.knowledgeSelectionIDs()
	if len(ids) == 0 {
		return m
	}
	index := m.knowledgeSelectedIndex()
	index = (index + delta + len(ids)) % len(ids)
	m.knowledgeView.selected = index
	m.knowledgeView.selectedID = ids[index]
	m.knowledgeView.expanded = false
	return m
}

func (m model) setKnowledgeTab(tab knowledgeTab) model {
	if tab < 0 || tab >= knowledgeTabCount {
		return m
	}
	m.knowledgeView.tab = tab
	m.knowledgeView.selected = 0
	m.knowledgeView.selectedID = ""
	m.knowledgeView.expanded = false
	if ids := m.knowledgeSelectionIDs(); len(ids) > 0 {
		m.knowledgeView.selectedID = ids[0]
	}
	return m
}

func (m model) handleKnowledgeViewKey(msg tea.KeyPressMsg) (model, tea.Cmd, bool) {
	if !m.knowledgeView.active {
		return m, nil, false
	}
	if m.knowledgeView.help {
		if keyText(msg) == "?" || keyText(msg) == "q" || keyIs(msg, tea.KeyEsc) || keyIs(msg, tea.KeyEnter) {
			m.knowledgeView.help = false
		}
		return m, nil, true
	}
	if m.knowledgeView.editor.active {
		return m.handleKnowledgeEditorKey(msg)
	}
	if m.knowledgeView.confirmDelete != "" {
		switch strings.ToLower(keyText(msg)) {
		case "y":
			path := m.knowledgeView.confirmDelete
			m.knowledgeView.confirmDelete = ""
			generation := m.knowledgeView.generation
			return m, func() tea.Msg {
				trashed, err := knowledge.MoveToTrash(path)
				return knowledgeTrashMsg{generation: generation, path: trashed, err: err}
			}, true
		case "n":
			m.knowledgeView.confirmDelete = ""
			return m, nil, true
		case "esc":
			m.knowledgeView.confirmDelete = ""
			return m, nil, true
		}
		return m, nil, true
	}
	if m.knowledgeView.searchFocused {
		if keyIs(msg, tea.KeyEsc) {
			m.knowledgeView.searchFocused = false
			m.knowledgeView.searchInput.Blur()
			return m, nil, true
		}
		if keyIs(msg, tea.KeyEnter) {
			m.knowledgeView.searchFocused = false
			m.knowledgeView.searchInput.Blur()
			m.knowledgeView.searchQuery = strings.TrimSpace(m.knowledgeView.searchInput.Value())
			m.knowledgeGeneration++
			m.knowledgeSeq++
			m.knowledgeView.generation = m.knowledgeGeneration
			m.knowledgeView.loadSeq = m.knowledgeSeq
			seq, generation := m.knowledgeSeq, m.knowledgeGeneration
			return m, knowledgeLoadCmd(seq, m.knowledgeView.searchQuery, m.knowledgeView.searchLens, generation), true
		}
		var cmd tea.Cmd
		m.knowledgeView.searchInput, cmd = m.knowledgeView.searchInput.Update(msg)
		return m, cmd, true
	}

	if keyCtrl(msg, 's') {
		return m, nil, true
	}
	if keyIs(msg, tea.KeyEsc) && m.knowledgeView.webPreview != nil {
		m.knowledgeView.webPreview = nil
		m.knowledgeView.notice = "Returned to ranked web results"
		return m, nil, true
	}
	if keyIs(msg, tea.KeyEsc) || (keyText(msg) == "q" && m.composerValue() == "") {
		return m.closeKnowledgeView(), nil, true
	}
	if keyText(msg) == "?" {
		m.knowledgeView.help = true
		return m, nil, true
	}
	if m.composerValue() == "" {
		switch keyText(msg) {
		case "1", "2", "3", "4", "5", "6":
			index := int(keyText(msg)[0] - '1')
			return m.setKnowledgeTab(knowledgeTab(index)), nil, true
		}
	}
	if keyText(msg) == "/" {
		m = m.setKnowledgeTab(knowledgeTabSearch)
		m.knowledgeView.searchFocused = true
		m.knowledgeView.searchInput.Focus()
		return m, textinput.Blink, true
	}
	if keyText(msg) == "n" {
		return m.openKnowledgeEditor(nil), nil, true
	}
	if keyText(msg) == "e" {
		if node := m.knowledgeSelectedNode(); node != nil {
			for i := range m.knowledgeView.notes {
				if m.knowledgeView.notes[i].Name == node.ID {
					return m.openKnowledgeEditor(&m.knowledgeView.notes[i]), nil, true
				}
			}
		}
		return m, nil, true
	}
	if keyText(msg) == "x" || keyText(msg) == "X" {
		if node := m.knowledgeSelectedNode(); node != nil && node.Source != "" && node.Kind != knowledge.NodeGhost && strings.HasSuffix(node.Source, ".md") {
			m.knowledgeView.confirmDelete = node.Source
		}
		return m, nil, true
	}
	if keyText(msg) == "p" {
		if m.knowledgeView.pinned == nil {
			m.knowledgeView.pinned = map[string]bool{}
		}
		m.knowledgeView.pinned[m.knowledgeView.selectedID] = !m.knowledgeView.pinned[m.knowledgeView.selectedID]
		return m, nil, true
	}
	if keyText(msg) == "f" {
		m.knowledgeView.filter = (m.knowledgeView.filter + 1) % 3
		m.knowledgeView.notice = []string{"FILTER · all", "FILTER · unresolved / stale", "FILTER · pinned"}[m.knowledgeView.filter]
		return m, nil, true
	}
	if keyText(msg) == "r" && m.knowledgeView.tab == knowledgeTabSources {
		m.knowledgeGeneration++
		m.knowledgeSeq++
		m.knowledgeView.generation = m.knowledgeGeneration
		m.knowledgeView.loadSeq = m.knowledgeSeq
		m.knowledgeView.loading = true
		m.knowledgeView.notice = "Refreshing local provenance records…"
		return m, knowledgeLoadCmd(m.knowledgeSeq, m.knowledgeView.searchQuery, m.knowledgeView.searchLens, m.knowledgeGeneration), true
	}
	if keyText(msg) == "g" {
		if m.knowledgeView.tab == knowledgeTabTimeline {
			m.knowledgeView.timelineGrouping = (m.knowledgeView.timelineGrouping + 1) % 3
			m.knowledgeView.notice = "TIMELINE GROUP · " + []string{"day", "week", "month"}[m.knowledgeView.timelineGrouping]
			return m, nil, true
		}
		m.knowledgeView.grouping = (m.knowledgeView.grouping + 1) % 4
		m.knowledgeView.notice = "GROUP · " + []string{"community", "tag", "type", "source"}[m.knowledgeView.grouping]
		return m, nil, true
	}
	if keyText(msg) == "[" {
		m.knowledgeView.graphDepth = maxInt(0, m.knowledgeView.graphDepth-1)
		return m, nil, true
	}
	if keyText(msg) == "]" {
		m.knowledgeView.graphDepth = minInt(3, m.knowledgeView.graphDepth+1)
		return m, nil, true
	}
	if m.knowledgeView.tab == knowledgeTabSearch && (keyText(msg) == "," || keyText(msg) == ".") && len(m.knowledgeView.webHistory) > 0 {
		delta := -1
		if keyText(msg) == "." {
			delta = 1
		}
		index := clampInt(m.knowledgeView.webHistoryIndex+delta, 0, len(m.knowledgeView.webHistory)-1)
		m.knowledgeView.webHistoryIndex = index
		preview := m.knowledgeView.webHistory[index]
		m.knowledgeView.webPreview = &preview
		return m, nil, true
	}
	if keyText(msg) == " " {
		m.knowledgeView.paused = !m.knowledgeView.paused
		return m, nil, true
	}
	if keyText(msg) == "m" && m.knowledgeView.tab == knowledgeTabSearch {
		m.knowledgeView.searchLens = (m.knowledgeView.searchLens + 1) % 3
		m.knowledgeGeneration++
		m.knowledgeSeq++
		m.knowledgeView.generation = m.knowledgeGeneration
		m.knowledgeView.loadSeq = m.knowledgeSeq
		seq, generation := m.knowledgeView.loadSeq, m.knowledgeView.generation
		return m, knowledgeLoadCmd(seq, m.knowledgeView.searchQuery, m.knowledgeView.searchLens, generation), true
	}
	if keyText(msg) == "s" && m.knowledgeView.tab == knowledgeTabSearch && m.knowledgeView.webPreview != nil {
		preview := m.knowledgeView.webPreview
		return m, func() tea.Msg {
			return knowledgeSaveMsg{generation: m.knowledgeView.generation, path: "WEB_PREVIEW|" + preview.Title + "|" + preview.URL}
		}, true
	}
	if keyText(msg) == "o" {
		if node := m.knowledgeSelectedNode(); node != nil && strings.TrimSpace(node.Source) != "" {
			return m, openKnowledgeExternalCmd(node.Source), true
		}
		if strings.HasPrefix(m.knowledgeView.selectedID, "web:") {
			index := m.knowledgeView.selected
			if index >= 0 && index < len(m.knowledgeView.webResults) {
				return m, openKnowledgeExternalCmd(m.knowledgeView.webResults[index].URL), true
			}
		}
		return m, nil, true
	}

	if keyIs(msg, tea.KeyTab) {
		delta := 1
		if keyShift(msg) {
			delta = -1
		}
		return m.setKnowledgeTab(knowledgeTab((int(m.knowledgeView.tab) + delta + int(knowledgeTabCount)) % int(knowledgeTabCount))), nil, true
	}
	if keyIs(msg, tea.KeyLeft) {
		return m.setKnowledgeTab(knowledgeTab((int(m.knowledgeView.tab) - 1 + int(knowledgeTabCount)) % int(knowledgeTabCount))), nil, true
	}
	if keyIs(msg, tea.KeyRight) {
		return m.setKnowledgeTab(knowledgeTab((int(m.knowledgeView.tab) + 1) % int(knowledgeTabCount))), nil, true
	}
	if keyText(msg) == "h" || keyText(msg) == "l" {
		return m.traverseKnowledgeLink(keyText(msg) == "l"), nil, true
	}
	if keyIs(msg, tea.KeyDown) || keyText(msg) == "j" {
		return m.selectKnowledgeDelta(1), nil, true
	}
	if keyIs(msg, tea.KeyUp) || keyText(msg) == "k" {
		return m.selectKnowledgeDelta(-1), nil, true
	}
	if keyIs(msg, tea.KeyEnter) {
		if m.knowledgeView.tab == knowledgeTabSearch && len(m.knowledgeView.webResults) > 0 {
			index := clampInt(m.knowledgeView.selected, 0, len(m.knowledgeView.webResults)-1)
			return m, m.knowledgeWebFetchCmd(m.knowledgeView.webResults[index], m.knowledgeView.generation, m.knowledgeView.webQuery), true
		}
		m.knowledgeView.expanded = !m.knowledgeView.expanded
		return m, nil, true
	}
	if keyIs(msg, tea.KeyPgUp) {
		m.chatScrollOffset += maxInt(1, m.height/2)
		return m, nil, true
	}
	if keyIs(msg, tea.KeyPgDown) {
		m.chatScrollOffset = maxInt(0, m.chatScrollOffset-maxInt(1, m.height/2))
		return m, nil, true
	}
	return m, nil, true
}

func openKnowledgeExternalCmd(target string) tea.Cmd {
	target = strings.TrimSpace(target)
	if target == "" {
		return nil
	}
	if terminalBrowserSupported() {
		return openTerminalBrowserTargetCmd(context.Background(), target)
	}
	return func() tea.Msg {
		return terminalBrowserOpenedMsg{err: knowledge.OpenInBrowser(target)}
	}
}

func (m model) traverseKnowledgeLink(forward bool) model {
	node := m.knowledgeSelectedNode()
	if node == nil {
		return m
	}
	var links []knowledge.GraphLink
	if forward {
		links = m.knowledgeView.graph.ForwardLinks(node.ID)
	} else {
		links = m.knowledgeView.graph.Backlinks(node.ID)
	}
	if len(links) == 0 {
		return m
	}
	id := links[0].Target
	if !forward {
		id = links[0].Source
	}
	m.knowledgeView.selectedID = id
	m.knowledgeView.selected = m.knowledgeSelectedIndex()
	return m
}

func (m model) openKnowledgeEditor(note *knowledge.Note) model {
	inTitle := textinput.New()
	inTags := textinput.New()
	inSource := textinput.New()
	body := textarea.New()
	body.Prompt = "  │ "
	body.Placeholder = "Markdown body · [[wikilinks]] and #tags supported"
	body.SetHeight(9)
	state := knowledgeEditorState{active: true, field: knowledgeEditorTitle, title: inTitle, tags: inTags, source: inSource, body: body}
	if note == nil {
		state.title.Focus()
	} else {
		copy := *note
		state.editing = true
		state.existing = &copy
		state.frontmatter = note.FrontmatterRaw
		state.title.SetValue(note.Title)
		state.tags.SetValue(strings.Join(note.Tags, ", "))
		state.source.SetValue(note.SourceURL)
		state.body.SetValue(editorBody(note.Content))
		state.title.Focus()
	}
	m.knowledgeView.editor = state
	return m
}

func editorBody(content string) string {
	if strings.HasPrefix(content, "---") {
		if end := strings.Index(content[3:], "\n---"); end >= 0 {
			content = content[end+7:]
		}
	}
	lines := strings.SplitN(content, "\n", 2)
	if len(lines) == 2 && strings.HasPrefix(strings.TrimSpace(lines[0]), "#") {
		return strings.TrimSpace(lines[1])
	}
	return strings.TrimSpace(content)
}

func (m model) handleKnowledgeEditorKey(msg tea.KeyPressMsg) (model, tea.Cmd, bool) {
	ed := &m.knowledgeView.editor
	if keyIs(msg, tea.KeyEsc) {
		ed.active = false
		return m, nil, true
	}
	if keyCtrl(msg, 's') {
		return m.saveKnowledgeEditor()
	}
	if keyIs(msg, tea.KeyTab) {
		ed.field = (ed.field + 1) % knowledgeEditorFieldCount
		m.focusKnowledgeEditorField()
		return m, nil, true
	}
	if keyIs(msg, tea.KeyEnter) && ed.field != knowledgeEditorBody {
		ed.field = (ed.field + 1) % knowledgeEditorFieldCount
		m.focusKnowledgeEditorField()
		return m, nil, true
	}
	var cmd tea.Cmd
	switch ed.field {
	case knowledgeEditorTitle:
		ed.title, cmd = ed.title.Update(msg)
	case knowledgeEditorTags:
		ed.tags, cmd = ed.tags.Update(msg)
	case knowledgeEditorBody:
		ed.body, cmd = ed.body.Update(msg)
	case knowledgeEditorSource:
		ed.source, cmd = ed.source.Update(msg)
	}
	return m, cmd, true
}

func (m *model) focusKnowledgeEditorField() {
	ed := &m.knowledgeView.editor
	ed.title.Blur()
	ed.tags.Blur()
	ed.body.Blur()
	ed.source.Blur()
	switch ed.field {
	case knowledgeEditorTitle:
		ed.title.Focus()
	case knowledgeEditorTags:
		ed.tags.Focus()
	case knowledgeEditorBody:
		ed.body.Focus()
	case knowledgeEditorSource:
		ed.source.Focus()
	}
}

func (m model) saveKnowledgeEditor() (model, tea.Cmd, bool) {
	ed := m.knowledgeView.editor
	title := strings.TrimSpace(ed.title.Value())
	if title == "" {
		m.knowledgeView.notice = "A title is required"
		return m, nil, true
	}
	tags := splitKnowledgeTags(ed.tags.Value())
	draft := knowledge.NoteDraft{Title: title, Tags: tags, Body: ed.body.Value(), SourceURL: strings.TrimSpace(ed.source.Value()), SearchQuery: ed.webQuery, Frontmatter: ed.frontmatter}
	if ed.createdFromWeb {
		draft.Kind = "web"
		draft.CapturedAt = time.Now().UTC()
		if parsed, err := url.Parse(draft.SourceURL); err == nil {
			draft.SourceDomain = parsed.Hostname()
		}
	}
	if ed.existing != nil {
		draft.Path = ed.existing.Path
		draft.Name = ed.existing.Name
	}
	ed.savePending = true
	m.knowledgeView.editor = ed
	generation := m.knowledgeView.generation
	return m, func() tea.Msg {
		path, err := knowledge.SaveNote(draft)
		return knowledgeSaveMsg{generation: generation, path: path, err: err}
	}, true
}

func splitKnowledgeTags(value string) []string {
	value = strings.ReplaceAll(value, "#", "")
	parts := strings.FieldsFunc(value, func(r rune) bool { return r == ',' || r == ' ' || r == '\t' })
	seen := map[string]bool{}
	var out []string
	for _, part := range parts {
		part = strings.ToLower(strings.TrimSpace(part))
		if part != "" && !seen[part] {
			seen[part] = true
			out = append(out, part)
		}
	}
	return out
}

func (m model) applyKnowledgeSave(msg knowledgeSaveMsg) (model, tea.Cmd) {
	if msg.generation != m.knowledgeView.generation && m.knowledgeView.active {
		return m, nil
	}
	if msg.err != nil {
		if m.knowledgeView.active {
			m.knowledgeView.notice = "Save failed: " + msg.err.Error()
			m.knowledgeView.editor.savePending = false
			return m, nil
		}
		return m.appendKnowledgeNotice("Knowledge action failed: " + msg.err.Error()), nil
	}
	if !m.knowledgeView.active {
		if strings.HasPrefix(msg.path, "EXPORT|") {
			parts := strings.SplitN(msg.path, "|", 5)
			if len(parts) >= 4 {
				text := fmt.Sprintf("Knowledge graph exported — %s (%s nodes, %s links)", parts[1], parts[2], parts[3])
				if len(parts) == 5 {
					text += "\nBrowser fallback could not open it: " + parts[4]
				}
				return m.appendKnowledgeNotice(text), nil
			}
		}
		if msg.summary != "" {
			return m.appendKnowledgeNotice(msg.summary), nil
		}
		return m.appendKnowledgeNotice(fmt.Sprintf("Saved note → %s", msg.path)), nil
	}
	if strings.HasPrefix(msg.path, "WEB_PREVIEW|") {
		parts := strings.SplitN(msg.path, "|", 3)
		if len(parts) == 3 && m.knowledgeView.webPreview != nil {
			preview := m.knowledgeView.webPreview
			m.knowledgeView.editor = knowledgeEditorState{active: true, field: knowledgeEditorTitle, title: knowledgeSearchInput(parts[1]), source: knowledgeSearchInput(parts[2]), body: textarea.New(), frontmatter: "", createdFromWeb: true, webQuery: preview.Query}
			m.knowledgeView.editor.body.Prompt = "  │ "
			m.knowledgeView.editor.body.SetHeight(9)
			m.knowledgeView.editor.body.SetValue(preview.Body)
			m.knowledgeView.editor.tags = textinput.New()
			m.knowledgeView.editor.title.Focus()
			m.knowledgeView.notice = "Review exact page text, then Ctrl+S to save"
		}
		return m, nil
	}
	if m.knowledgeView.editor.active {
		m.knowledgeView.editor.active = false
	}
	m.knowledgeGeneration++
	m.knowledgeSeq++
	m.knowledgeView.generation = m.knowledgeGeneration
	m.knowledgeView.loadSeq = m.knowledgeSeq
	m.knowledgeView.loading = true
	m.knowledgeView.notice = "Saved locally · rebuilding deterministic index"
	seq, generation := m.knowledgeView.loadSeq, m.knowledgeView.generation
	return m, knowledgeLoadCmd(seq, m.knowledgeView.searchQuery, m.knowledgeView.searchLens, generation)
}

func (m model) applyKnowledgeTrash(msg knowledgeTrashMsg) (model, tea.Cmd) {
	if msg.generation != m.knowledgeView.generation {
		return m, nil
	}
	if msg.err != nil {
		m.knowledgeView.notice = "Could not move note to recoverable trash: " + msg.err.Error()
		return m, nil
	}
	m.knowledgeGeneration++
	m.knowledgeSeq++
	m.knowledgeView.generation = m.knowledgeGeneration
	m.knowledgeView.loadSeq = m.knowledgeSeq
	m.knowledgeView.loading = true
	m.knowledgeView.notice = "Moved to recoverable knowledge trash"
	seq, generation := m.knowledgeView.loadSeq, m.knowledgeView.generation
	return m, knowledgeLoadCmd(seq, m.knowledgeView.searchQuery, m.knowledgeView.searchLens, generation)
}

func (m model) knowledgeViewNavBar(width int) string {
	left := sonutTheme.sidebarHeader.Render("◆ Knowledge Atlas")
	available := maxInt(0, width-lipgloss.Width(left)-2)
	tabs := make([]string, 0, knowledgeTabCount)
	for i, name := range knowledgeTabNames {
		if knowledgeTab(i) == m.knowledgeView.tab {
			tabs = append(tabs, sonutTheme.badge.Render(" "+name+" "))
		} else {
			tabs = append(tabs, sonutTheme.muted.Render(name))
		}
	}
	right := strings.Join(tabs, sonutTheme.faintest.Render("  "))
	if lipgloss.Width(right) > available {
		right = sonutTheme.muted.Render(fmt.Sprintf("%d/6  %s", int(m.knowledgeView.tab)+1, knowledgeTabNames[m.knowledgeView.tab]))
	}
	return fitStyledLine(joinHeaderLine(left, right, width), width)
}

func (m model) knowledgeStatusLine(width int) string {
	notes := len(m.knowledgeView.notes)
	links := 0
	for _, edge := range m.knowledgeView.graph.Links {
		if edge.Type == "links_to" || edge.Type == "" {
			links++
		}
	}
	status := "LOCAL"
	if m.knowledgeView.tab == knowledgeTabSearch && (m.knowledgeView.webQuery != "" || len(m.knowledgeView.webResults) > 0) {
		status = "NETWORK"
	}
	statusStyle := sonutTheme.green
	if status == "NETWORK" {
		statusStyle = sonutTheme.amber
	}
	indexed := "now"
	if m.knowledgeView.loading {
		indexed = "…"
	}
	left := statusStyle.Bold(true).Render(" "+status+" ") + "  " + sonutTheme.ink.Render(fmt.Sprintf("%d notes", notes)) + sonutTheme.faintest.Render("  ·  ") + sonutTheme.ink.Render(fmt.Sprintf("%d links", links)) + sonutTheme.faintest.Render("  ·  ") + sonutTheme.faint.Render("indexed "+indexed)
	return fitStyledLine(left, width)
}

func (m model) knowledgeViewBodyItems(width int) []transcriptBodyItem {
	return []transcriptBodyItem{transcriptBlockBodyItem(transcriptBodyItemRow, -1, m.renderKnowledgeView(width))}
}

func (m model) renderKnowledgeView(width int) string {
	inner := maxInt(20, width-2)
	var body string
	if m.knowledgeView.editor.active {
		body = m.renderKnowledgeEditor(inner)
	} else if m.knowledgeView.help {
		body = m.renderKnowledgeHelp(inner)
	} else if m.knowledgeView.loading && len(m.knowledgeView.graph.Nodes) == 0 {
		body = strings.Join([]string{"", sonutTheme.accent.Render("Indexing local Markdown…"), sonutTheme.faint.Render("No network request is made by opening Knowledge Atlas."), "", m.knowledgeStatusLine(inner)}, "\n")
	} else {
		switch m.knowledgeView.tab {
		case knowledgeTabNotes:
			body = m.renderKnowledgeNotes(inner)
		case knowledgeTabSearch:
			body = m.renderKnowledgeSearch(inner)
		case knowledgeTabSources:
			body = m.renderKnowledgeSources(inner)
		case knowledgeTabTimeline:
			body = m.renderKnowledgeTimeline(inner)
		case knowledgeTabInsights:
			body = m.renderKnowledgeInsights(inner)
		default:
			body = m.renderKnowledgeAtlas(inner)
		}
		body = m.knowledgeStatusLine(inner) + "\n\n" + body
	}
	if m.knowledgeView.notice != "" {
		body += "\n\n" + sonutTheme.amber.Render(m.knowledgeView.notice)
	}
	lines := strings.Split(body, "\n")
	for i := range lines {
		lines[i] = fitStyledLine(lines[i], inner)
	}
	return strings.Join(lines, "\n")
}

func (m model) renderKnowledgeAtlas(width int) string {
	graph := m.knowledgeView.graph
	selected := m.knowledgeSelectedNode()
	if selected == nil && len(graph.Nodes) == 0 {
		return strings.Join([]string{"", sonutTheme.faint.Render("Your atlas is empty."), sonutTheme.muted.Render("Press n to write the first local Markdown note."), "", sonutTheme.faint.Render("Local Markdown knowledge • network only when requested • inferred links are labeled")}, "\n")
	}
	if selected == nil && len(graph.Nodes) > 0 {
		selected = &graph.Nodes[0]
	}
	if width >= 92 {
		leftWidth := maxInt(42, width*63/100)
		rightWidth := maxInt(28, width-leftWidth-3)
		left := strings.Join([]string{m.renderKnowledgeGraph(leftWidth), "", m.renderKnowledgeFocusRail(leftWidth, selected), "", m.renderKnowledgeReadout(leftWidth, selected)}, "\n")
		right := m.renderKnowledgeInspector(rightWidth, selected)
		return lipgloss.JoinHorizontal(lipgloss.Top, left, sonutTheme.line.Render(" │ "), right)
	}
	if width >= 62 {
		return m.renderKnowledgeGraph(width) + "\n\n" + m.renderKnowledgeInspector(width, selected)
	}
	return m.renderKnowledgeTree(width, selected)
}

func (m model) renderKnowledgeGraph(width int) string {
	graph := m.knowledgeView.graph
	ids := m.visibleKnowledgeNodeIDs()
	height := knowledgeGraphHeight
	if width >= 92 && m.height >= 40 {
		height = minInt(32, maxInt(knowledgeGraphHeight, m.height-16))
	}
	if width < 70 {
		height = 10
	}
	canvasWidth := maxInt(12, width-2)
	spin := 0.0
	if !m.knowledgeView.paused && !m.reducedMotion {
		spin = float64(m.knowledgeView.frame%720) * (2 * math.Pi / 720)
	}
	points := knowledgeTorusNodes(graph, ids, canvasWidth, height, spin)
	surface := make([][]rune, height)
	surfaceDepth := make([][]float64, height)
	surfaceTone := make([][]int, height)
	edges := make([][]bool, height)
	focusEdges := make([][]bool, height)
	for y := range surface {
		surface[y] = []rune(strings.Repeat(" ", canvasWidth))
		surfaceDepth[y] = make([]float64, canvasWidth)
		surfaceTone[y] = make([]int, canvasWidth)
		edges[y] = make([]bool, canvasWidth)
		focusEdges[y] = make([]bool, canvasWidth)
		for x := range surfaceDepth[y] {
			surfaceDepth[y][x] = -math.MaxFloat64
		}
	}

	// Paint a bounded sampled torus surface first. Depth buffering makes the
	// front tube replace the back tube at the same terminal cell, which gives
	// the ring a calm 3D silhouette without an unbounded simulation.
	for uIndex := 0; uIndex < 96; uIndex++ {
		for vIndex := 0; vIndex < 24; vIndex++ {
			u := 2*math.Pi*float64(uIndex)/96 + spin
			v := 2 * math.Pi * float64(vIndex) / 24
			point := knowledgeProjectTorus(u, v, canvasWidth, height)
			if point.Y < 0 || point.Y >= height || point.X < 0 || point.X >= canvasWidth {
				continue
			}
			if point.Depth < surfaceDepth[point.Y][point.X] {
				continue
			}
			tone := 0
			if point.Depth > 0.16 {
				tone = 2
			} else if point.Depth > -0.08 {
				tone = 1
			}
			surfaceDepth[point.Y][point.X] = point.Depth
			surfaceTone[point.Y][point.X] = tone
			if knowledgeASCII() {
				surface[point.Y][point.X] = '.'
			} else if tone == 2 {
				surface[point.Y][point.X] = '•'
			} else {
				surface[point.Y][point.X] = '·'
			}
		}
	}

	for _, edge := range graph.Links {
		if !ids[edge.Source] || !ids[edge.Target] {
			continue
		}
		from, fok := points[edge.Source]
		to, tok := points[edge.Target]
		if !fok || !tok {
			continue
		}
		focused := edge.Source == m.knowledgeView.selectedID || edge.Target == m.knowledgeView.selectedID
		for _, cell := range knowledgeLine(from.X, from.Y, to.X, to.Y) {
			if cell.y >= 0 && cell.y < height && cell.x >= 0 && cell.x < canvasWidth {
				edges[cell.y][cell.x] = true
				if focused {
					focusEdges[cell.y][cell.x] = true
				}
			}
		}
	}

	nodeAt := map[[2]int]knowledge.GraphNode{}
	for _, node := range graph.Nodes {
		if !ids[node.ID] {
			continue
		}
		point := points[node.ID]
		if point.Y < 0 || point.Y >= height || point.X < 0 || point.X >= canvasWidth {
			continue
		}
		nodeAt[[2]int{point.X, point.Y}] = node
	}
	center := m.knowledgeSelectedNode()
	if center == nil && len(graph.Nodes) > 0 {
		center = &graph.Nodes[0]
	}
	centerLabels := knowledgeTorusCenterCard(center, graph, canvasWidth, height, nodeAt, nil)
	labels := knowledgeTorusLabels(graph, ids, points, nodeAt, centerLabels, m.knowledgeView.selectedID, canvasWidth, height)
	for cell, label := range centerLabels {
		labels[cell] = label
	}

	lines := make([]string, 0, height+4)
	lines = append(lines, knowledgeSection("Atlas", width, fmt.Sprintf("depth %d · group %s · %d/%d visible", m.knowledgeView.graphDepth, []string{"community", "tag", "type", "source"}[m.knowledgeView.grouping], len(ids), len(graph.Nodes))))
	for y := range surface {
		var styled strings.Builder
		for x, char := range surface[y] {
			style := knowledgeTorusStyle(surfaceTone[y][x])
			if edges[y][x] && char == ' ' {
				if knowledgeASCII() {
					char = '.'
				} else {
					char = '·'
				}
				style = sonutTheme.faint
			}
			if focusEdges[y][x] {
				if knowledgeASCII() {
					char = '+'
				} else {
					char = '•'
				}
				style = sonutTheme.accent
			}
			if node, ok := nodeAt[[2]int{x, y}]; ok {
				char = []rune(knowledgeNodeGlyph(node))[0]
				if node.ID == m.knowledgeView.selectedID {
					char = []rune(knowledgeFocusGlyph(m.knowledgeView.frame, m.knowledgeView.paused || m.reducedMotion))[0]
					style = sonutTheme.accent.Bold(true)
				} else {
					style = knowledgeCommunityStyle(node)
				}
			}
			if label, ok := labels[[2]int{x, y}]; ok {
				char = label.Char
				style = label.Style
			}
			styled.WriteString(style.Render(string(char)))
		}
		lines = append(lines, styled.String())
	}
	legend := "● note  ◇ session  ◆ web  □ concept  # tag  ○ unresolved"
	if knowledgeASCII() {
		legend = "o note  s session  w web  c concept  # tag  ? unresolved"
	}
	lines = append(lines, sonutTheme.faint.Render(legend))
	return strings.Join(lines, "\n")
}

type knowledgeTorusPoint struct {
	X, Y  int
	Depth float64
}

type knowledgeTorusLabelCell struct {
	Char  rune
	Style lipgloss.Style
}

func knowledgeTorusLabels(graph knowledge.Graph, ids map[string]bool, points map[string]knowledgeTorusPoint, nodeAt map[[2]int]knowledge.GraphNode, reserved map[[2]int]knowledgeTorusLabelCell, selectedID string, width, height int) map[[2]int]knowledgeTorusLabelCell {
	nodes := make([]knowledge.GraphNode, 0, len(ids))
	for _, node := range graph.Nodes {
		if !ids[node.ID] {
			continue
		}
		if len(ids) > 18 && node.ID != selectedID && !knowledgeNodeTouchesSelection(graph, node.ID, selectedID) {
			continue
		}
		nodes = append(nodes, node)
	}
	sort.SliceStable(nodes, func(i, j int) bool {
		if nodes[i].ID == selectedID {
			return true
		}
		if nodes[j].ID == selectedID {
			return false
		}
		return nodes[i].ID < nodes[j].ID
	})

	occupied := make(map[[2]int]bool, len(nodeAt))
	for cell := range nodeAt {
		occupied[cell] = true
	}
	for cell := range reserved {
		occupied[cell] = true
	}
	labels := map[[2]int]knowledgeTorusLabelCell{}
	for _, node := range nodes {
		point, ok := points[node.ID]
		if !ok {
			continue
		}
		text := knowledgeTorusLabelText(node)
		maxWidth := 20
		style := knowledgeCommunityStyle(node)
		if node.ID == selectedID {
			maxWidth = 28
			style = sonutTheme.accent.Bold(true)
		}
		text = " " + knowledgeTorusTrimLabel(text, maxWidth) + " "
		style = sonutTheme.onPanel(style)
		labelX, labelY, placed := knowledgeTorusLabelPosition(point, text, occupied, width, height)
		if !placed {
			continue
		}
		for index, char := range []rune(text) {
			cell := [2]int{labelX + index, labelY}
			labels[cell] = knowledgeTorusLabelCell{Char: char, Style: style}
			occupied[cell] = true
		}
	}
	return labels
}

func knowledgeTorusLabelText(node knowledge.GraphNode) string {
	text := strings.Join(strings.Fields(node.Title), " ")
	if text == "" {
		text = node.ID
	}
	if node.Kind == knowledge.NodeTag {
		text = strings.TrimPrefix(text, "#")
	}
	return text
}

func knowledgeTorusCenterCard(selected *knowledge.GraphNode, graph knowledge.Graph, width, height int, nodeAt map[[2]int]knowledge.GraphNode, labels map[[2]int]knowledgeTorusLabelCell) map[[2]int]knowledgeTorusLabelCell {
	if selected == nil || height < 8 || width < 24 {
		return nil
	}
	boxWidth := minInt(24, maxInt(18, width/3))
	innerWidth := boxWidth - 4
	coreTitle := knowledgeTorusTrimLabel(knowledgeTorusLabelText(*selected), innerWidth)
	meta := fmt.Sprintf("%d links · %d nodes", len(graph.ForwardLinks(selected.ID))+len(graph.Backlinks(selected.ID)), len(graph.Nodes))
	meta = knowledgeTorusTrimLabel(meta, innerWidth)
	border := "─"
	left, right := "╭", "╮"
	bottomLeft, bottomRight := "╰", "╯"
	vertical := "│"
	if knowledgeASCII() {
		border = "-"
		left, right = "+", "+"
		bottomLeft, bottomRight = "+", "+"
		vertical = "|"
	}
	centerLine := func(text string) string {
		text = knowledgeTorusTrimLabel(text, innerWidth)
		padding := maxInt(0, innerWidth-lipgloss.Width(text))
		leftPad := padding / 2
		return vertical + " " + strings.Repeat(" ", leftPad) + text + strings.Repeat(" ", padding-leftPad) + " " + vertical
	}
	rows := []string{
		left + strings.Repeat(border, boxWidth-2) + right,
		centerLine("KNOWLEDGE CORE"),
		centerLine(coreTitle),
		centerLine(meta),
		bottomLeft + strings.Repeat(border, boxWidth-2) + bottomRight,
	}
	startX := (width - boxWidth) / 2
	startY := (height - len(rows)) / 2
	occupied := map[[2]int]bool{}
	for cell := range nodeAt {
		occupied[cell] = true
	}
	for cell := range labels {
		occupied[cell] = true
	}
	for rowIndex, row := range rows {
		for column := range []rune(row) {
			cell := [2]int{startX + column, startY + rowIndex}
			if cell[0] < 0 || cell[0] >= width || cell[1] < 0 || cell[1] >= height || occupied[cell] {
				return nil
			}
		}
	}
	card := map[[2]int]knowledgeTorusLabelCell{}
	for rowIndex, row := range rows {
		style := sonutTheme.onPanel(sonutTheme.lineStrong)
		if rowIndex == 1 {
			style = sonutTheme.onPanel(sonutTheme.accent.Bold(true))
		} else if rowIndex == 2 {
			style = sonutTheme.onPanel(sonutTheme.ink.Bold(true))
		} else if rowIndex == 3 {
			style = sonutTheme.onPanel(sonutTheme.faint)
		}
		for column, char := range []rune(row) {
			card[[2]int{startX + column, startY + rowIndex}] = knowledgeTorusLabelCell{Char: char, Style: style}
		}
	}
	return card
}

func knowledgeNodeTouchesSelection(graph knowledge.Graph, nodeID, selectedID string) bool {
	if selectedID == "" || nodeID == selectedID {
		return false
	}
	for _, edge := range graph.Links {
		if (edge.Source == nodeID && edge.Target == selectedID) || (edge.Target == nodeID && edge.Source == selectedID) {
			return true
		}
	}
	return false
}

func knowledgeTorusTrimLabel(text string, width int) string {
	if width <= 1 || lipgloss.Width(text) <= width {
		return text
	}
	head, _ := splitAtWidth(text, width-1)
	return head + "…"
}

func knowledgeTorusLabelPosition(point knowledgeTorusPoint, text string, occupied map[[2]int]bool, width, height int) (int, int, bool) {
	labelWidth := lipgloss.Width(text)
	if labelWidth == 0 || labelWidth >= width {
		return 0, 0, false
	}
	direction := 1
	if point.X >= width/2 {
		direction = -1
	}
	starts := []int{point.X + direction*2, point.X - direction*2 - labelWidth}
	for offset := 0; offset <= 4; offset++ {
		rowOffsets := []int{0}
		if offset > 0 {
			rowOffsets = []int{-offset, offset}
		}
		for _, rowOffset := range rowOffsets {
			y := point.Y + rowOffset
			if y < 0 || y >= height {
				continue
			}
			for _, start := range starts {
				if start < 0 || start+labelWidth > width {
					continue
				}
				clear := true
				for x := start - 1; x <= start+labelWidth; x++ {
					if x >= 0 && x < width && occupied[[2]int{x, y}] {
						clear = false
						break
					}
				}
				if clear {
					return start, y, true
				}
			}
		}
	}
	return 0, 0, false
}

func knowledgeTorusNodes(graph knowledge.Graph, ids map[string]bool, width, height int, spin float64) map[string]knowledgeTorusPoint {
	nodes := make([]knowledge.GraphNode, 0, len(ids))
	for _, node := range graph.Nodes {
		if ids[node.ID] {
			nodes = append(nodes, node)
		}
	}
	sort.SliceStable(nodes, func(i, j int) bool {
		if nodes[i].Community != nodes[j].Community {
			return nodes[i].Community < nodes[j].Community
		}
		if nodes[i].Degree != nodes[j].Degree {
			return nodes[i].Degree > nodes[j].Degree
		}
		return nodes[i].ID < nodes[j].ID
	})
	points := make(map[string]knowledgeTorusPoint, len(nodes))
	for index, node := range nodes {
		fraction := float64(index) / float64(maxInt(1, len(nodes)))
		u := 2*math.Pi*fraction + (knowledgeTorusStableFloat(node.ID)-0.5)*0.04
		v := 2 * math.Pi * knowledgeTorusStableFloat(node.ID+"/tube")
		points[node.ID] = knowledgeProjectTorus(u+spin, v, width, height)
	}
	return points
}

func knowledgeProjectTorus(u, v float64, width, height int) knowledgeTorusPoint {
	const (
		majorRadius = 1.0
		minorRadius = 0.38
		tilt        = 0.24
	)
	cosV, sinV := math.Cos(v), math.Sin(v)
	ringRadius := majorRadius + minorRadius*cosV
	x := ringRadius * math.Cos(u)
	y := ringRadius * math.Sin(u)
	z := minorRadius * sinV
	cosTilt, sinTilt := math.Cos(tilt), math.Sin(tilt)
	projectedY := y*cosTilt - z*sinTilt
	depth := y*sinTilt + z*cosTilt
	perspective := 1 / (1 + depth*0.12)
	screenX := int(math.Round(float64(width-1)/2 + x*float64(width)*0.31*perspective))
	screenY := int(math.Round(float64(height-1)/2 + projectedY*float64(height)*0.37*perspective))
	return knowledgeTorusPoint{X: screenX, Y: screenY, Depth: depth}
}

func knowledgeTorusStableFloat(value string) float64 {
	hasher := fnv.New32a()
	_, _ = hasher.Write([]byte(value))
	return float64(hasher.Sum32()) / float64(math.MaxUint32)
}

func knowledgeTorusStyle(tone int) lipgloss.Style {
	switch tone {
	case 2:
		return sonutTheme.lineStrong
	case 1:
		return sonutTheme.line
	default:
		return sonutTheme.faintest
	}
}

func knowledgeCommunityStyle(node knowledge.GraphNode) lipgloss.Style {
	switch node.Community % 4 {
	case 1:
		return sonutTheme.blue
	case 2:
		return sonutTheme.green
	case 3:
		return sonutTheme.amber
	default:
		return sonutTheme.accent
	}
}

func (m model) renderKnowledgeFocusRail(width int, selected *knowledge.GraphNode) string {
	if selected == nil {
		return knowledgeSection("Focus route", width, "no selection") + "\n" + sonutTheme.faint.Render("Select a node to reveal its explicit evidence trail.")
	}
	forward := m.knowledgeView.graph.ForwardLinks(selected.ID)
	back := m.knowledgeView.graph.Backlinks(selected.ID)
	lines := []string{knowledgeSection("Focus route", width, fmt.Sprintf("%d out · %d in", len(forward), len(back)))}
	lines = append(lines, sonutTheme.badge.Render(" FOCUS ")+" "+sonutTheme.ink.Bold(true).Render(selected.Title)+"  "+sonutTheme.faint.Render(string(selected.Kind)))
	shown := 0
	for _, edge := range forward {
		node := m.knowledgeNode(edge.Target)
		if node == nil {
			continue
		}
		lines = append(lines, "  "+sonutTheme.blue.Bold(true).Render("→")+" "+sonutTheme.ink.Render(node.Title)+"  "+sonutTheme.faint.Render(edge.Type))
		shown++
		if shown >= 2 {
			break
		}
	}
	for _, edge := range back {
		node := m.knowledgeNode(edge.Source)
		if node == nil {
			continue
		}
		lines = append(lines, "  "+sonutTheme.muted.Render("←")+" "+sonutTheme.ink.Render(node.Title)+"  "+sonutTheme.faint.Render("backlink"))
		shown++
		if shown >= 3 {
			break
		}
	}
	if shown == 0 {
		lines = append(lines, sonutTheme.faint.Render("  No explicit links in this neighborhood yet."))
	}
	return strings.Join(lines, "\n")
}

func (m model) renderKnowledgeReadout(width int, selected *knowledge.GraphNode) string {
	graph := m.knowledgeView.graph
	communities := graph.CommunityDetection()
	communityCount := map[int]bool{}
	for _, community := range communities {
		communityCount[community] = true
	}
	ghosts := len(graph.Ghosts())
	orphans := len(graph.Orphans())
	lines := []string{knowledgeSection("Atlas readout", width, "local evidence")}
	lines = append(lines, sonutTheme.ink.Render(fmt.Sprintf("%d nodes", len(graph.Nodes))+"  "+sonutTheme.faint.Render(fmt.Sprintf("%d explicit links  ·  %d communities", len(graph.Links), len(communityCount)))))
	if selected != nil {
		links := len(graph.ForwardLinks(selected.ID)) + len(graph.Backlinks(selected.ID))
		lines = append(lines, sonutTheme.faint.Render(fmt.Sprintf("Focus has %d connected route(s)  ·  %d unresolved ghost(s)  ·  %d orphan(s)", links, ghosts, orphans)))
	}
	suggestion := "Traverse the selected route with h/l, then press Enter to inspect evidence."
	if ghosts > 0 {
		suggestion = fmt.Sprintf("Next useful move  ·  resolve %d ghost link(s) by creating the missing note(s).", ghosts)
	} else if orphans > 0 {
		suggestion = fmt.Sprintf("Next useful move  ·  connect %d orphan note(s) with an explicit [[wikilink]].", orphans)
	}
	lines = append(lines, sonutTheme.muted.Render(suggestion))
	return strings.Join(lines, "\n")
}

type knowledgeCell struct{ x, y int }

func knowledgeLine(x0, y0, x1, y1 int) []knowledgeCell {
	steps := maxInt(knowledgeAbsInt(x1-x0), knowledgeAbsInt(y1-y0))
	if steps == 0 {
		return nil
	}
	line := make([]knowledgeCell, 0, steps)
	for i := 1; i < steps; i++ {
		t := float64(i) / float64(steps)
		line = append(line, knowledgeCell{x: int(float64(x0) + float64(x1-x0)*t), y: int(float64(y0) + float64(y1-y0)*t)})
	}
	return line
}

func knowledgeAbsInt(value int) int {
	if value < 0 {
		return -value
	}
	return value
}

func knowledgeNodeGlyph(node knowledge.GraphNode) string {
	if knowledgeASCII() {
		if node.Ghost || node.Kind == knowledge.NodeGhost {
			return "?"
		}
		switch node.Kind {
		case knowledge.NodeSession:
			return "s"
		case knowledge.NodeWebSource:
			return "w"
		case knowledge.NodeProjectConcept:
			return "c"
		case knowledge.NodeTag:
			return "#"
		default:
			return "o"
		}
	}
	if node.Ghost || node.Kind == knowledge.NodeGhost {
		return "○"
	}
	switch node.Kind {
	case knowledge.NodeSession:
		return "◇"
	case knowledge.NodeWebSource:
		return "◆"
	case knowledge.NodeProjectConcept:
		return "□"
	case knowledge.NodeTag:
		return "#"
	default:
		return "●"
	}
}

func isKnowledgeNodeGlyph(char rune) bool {
	return strings.ContainsRune("●◇◆□#○oswc?", char)
}

func knowledgeASCII() bool {
	return lipgloss.Writer.Profile == colorprofile.Ascii || lipgloss.Writer.Profile == colorprofile.NoTTY || noColorRequested(os.Getenv)
}

func knowledgeFocusGlyph(frame int, steady bool) string {
	if knowledgeASCII() {
		return "@"
	}
	if steady || frame%6 < 2 {
		return "◉"
	}
	return "◎"
}

func (m model) visibleKnowledgeNodeIDs() map[string]bool {
	graph := m.knowledgeView.graph
	ids := map[string]bool{}
	include := func(node knowledge.GraphNode) bool {
		switch m.knowledgeView.filter {
		case 1:
			return node.Ghost || node.Kind == knowledge.NodeGhost || node.Stale || node.Contested
		case 2:
			return m.knowledgeView.pinned[node.ID]
		default:
			return true
		}
	}
	selected := m.knowledgeView.selectedID
	if selected != "" && m.knowledgeView.graphDepth >= 0 {
		for _, node := range graph.Neighbors(selected, m.knowledgeView.graphDepth) {
			if include(node) || node.ID == selected {
				ids[node.ID] = true
			}
		}
	}
	// A low-degree tag or ghost should still open into an understandable atlas;
	// add deterministic hub context when the local neighborhood is too small.
	if len(ids) < 4 || m.knowledgeView.graphDepth >= 2 {
		for _, ranked := range graph.Hubs(knowledgeMaxVisibleNodes) {
			if len(ids) >= knowledgeMaxVisibleNodes {
				break
			}
			if include(ranked.Node) {
				ids[ranked.Node.ID] = true
			}
		}
	}
	return ids
}

func (m model) renderKnowledgeTree(width int, selected *knowledge.GraphNode) string {
	lines := []string{knowledgeSection("Local neighborhood", width, "accessible tree · depth "+fmt.Sprint(m.knowledgeView.graphDepth))}
	if selected == nil {
		return strings.Join(append(lines, sonutTheme.faint.Render("No node selected.")), "\n")
	}
	lines = append(lines, sonutTheme.accent.Bold(true).Render("● "+selected.Title))
	for _, edge := range m.knowledgeView.graph.ForwardLinks(selected.ID) {
		if node := m.knowledgeNode(edge.Target); node != nil {
			lines = append(lines, sonutTheme.muted.Render("  ├─ → "+knowledgeNodeGlyph(*node)+" "+node.Title+"  "+knowledgeEdgeMarker(edge)))
		}
	}
	for _, edge := range m.knowledgeView.graph.Backlinks(selected.ID) {
		if node := m.knowledgeNode(edge.Source); node != nil {
			lines = append(lines, sonutTheme.faint.Render("  └─ ← "+knowledgeNodeGlyph(*node)+" "+node.Title+"  "+knowledgeEdgeMarker(edge)))
		}
	}
	lines = append(lines, "", sonutTheme.faint.Render("j/k select · h/l backlinks/forward · Enter inspect · ? help"))
	return strings.Join(lines, "\n")
}

func (m model) knowledgeNode(id string) *knowledge.GraphNode {
	for i := range m.knowledgeView.graph.Nodes {
		if m.knowledgeView.graph.Nodes[i].ID == id {
			return &m.knowledgeView.graph.Nodes[i]
		}
	}
	return nil
}

func knowledgeSection(title string, width int, right string) string {
	left := sonutTheme.ink.Bold(true).Render(title)
	r := sonutTheme.muted.Render(right)
	return left + " " + sonutTheme.lineStrong.Render(strings.Repeat("─", maxInt(1, width-lipgloss.Width(left)-lipgloss.Width(r)-2))) + " " + r
}

func knowledgeEdgeMarker(edge knowledge.GraphLink) string {
	origin := string(edge.Origin)
	if origin == "" {
		origin = "EXTRACTED"
	}
	typeName := edge.Type
	if typeName == "" {
		typeName = "links_to"
	}
	marker := typeName + " · " + origin
	if edge.Confidence > 0 {
		marker += fmt.Sprintf(" · %.2f confidence", edge.Confidence)
	}
	return marker
}

func knowledgeCompactSource(source string) string {
	source = strings.TrimSpace(source)
	if source == "" {
		return ""
	}
	if parsed, err := url.Parse(source); err == nil && parsed.Host != "" {
		path := parsed.Host + parsed.Path
		if path != "" {
			return path
		}
		return parsed.Host
	}
	clean := filepath.Clean(source)
	if root := filepath.Clean(knowledge.Dir()); root != "." {
		if relative, err := filepath.Rel(root, clean); err == nil && relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
			return "knowledge/" + filepath.ToSlash(relative)
		}
	}
	return filepath.Base(clean)
}

func knowledgeWrappedStyled(label, value string, width int, labelStyle, valueStyle lipgloss.Style) []string {
	labelWidth := lipgloss.Width(label)
	wrapped := wrapPlainText(strings.TrimSpace(value), maxInt(8, width-labelWidth))
	if len(wrapped) == 0 {
		wrapped = []string{""}
	}
	lines := make([]string, 0, len(wrapped))
	indent := strings.Repeat(" ", labelWidth)
	for index, part := range wrapped {
		if index == 0 {
			lines = append(lines, labelStyle.Render(label)+valueStyle.Render(part))
			continue
		}
		lines = append(lines, valueStyle.Render(indent+part))
	}
	return lines
}

func knowledgeInspectorLinkLines(direction, title string, edge knowledge.GraphLink, width int, style lipgloss.Style) []string {
	detail := knowledgeEdgeMarker(edge)
	text := title + " · " + detail
	wrapped := wrapPlainText(text, maxInt(8, width-3))
	if len(wrapped) == 0 {
		wrapped = []string{text}
	}
	lines := make([]string, 0, len(wrapped))
	for index, part := range wrapped {
		prefix := "   "
		if index == 0 {
			prefix = direction + "  "
		}
		lines = append(lines, style.Render(prefix+part))
	}
	return lines
}

func (m model) renderKnowledgeInspector(width int, selected *knowledge.GraphNode) string {
	lines := []string{knowledgeSection("Inspector", width, "selected")}
	if selected == nil {
		return strings.Join(append(lines, sonutTheme.faint.Render("Select a node to inspect.")), "\n")
	}
	title := selected.Title
	if strings.TrimSpace(title) == "" {
		title = selected.ID
	}
	lines = append(lines, sonutTheme.badge.Render(" "+strings.ToUpper(string(selected.Kind))+" ")+" "+sonutTheme.ink.Bold(true).Render(knowledgeNodeGlyph(*selected)+" "+title))
	lines = append(lines, sonutTheme.muted.Render(fmt.Sprintf("degree %d  ·  community %d", maxInt(selected.Degree, selected.Size), selected.Community+1)))
	if selected.Ghost {
		lines = append(lines, sonutTheme.amber.Render("UNRESOLVED · no Markdown source yet"))
	}
	if selected.Contested {
		lines = append(lines, sonutTheme.red.Render("CONTESTED · local metadata disagrees"))
	}
	if selected.Stale {
		lines = append(lines, sonutTheme.amber.Render("STALE · source should be refreshed explicitly"))
	}
	if selected.Source != "" {
		lines = append(lines, knowledgeWrappedStyled("source: ", knowledgeCompactSource(selected.Source), width, sonutTheme.faint, sonutTheme.muted)...)
	}
	if len(selected.Tags) > 0 {
		lines = append(lines, knowledgeWrappedStyled("tags: ", "#"+strings.Join(selected.Tags, " #"), width, sonutTheme.faint, sonutTheme.blue)...)
	}
	if selected.Body != "" {
		preview := strings.Join(strings.Fields(selected.Body), " ")
		bodyLines := knowledgeWrappedStyled("", preview, width, sonutTheme.muted, sonutTheme.muted)
		if len(bodyLines) > 4 {
			bodyLines = bodyLines[:4]
			bodyLines[len(bodyLines)-1] += sonutTheme.faint.Render(" …")
		}
		lines = append(lines, "")
		lines = append(lines, bodyLines...)
	}
	forward := m.knowledgeView.graph.ForwardLinks(selected.ID)
	back := m.knowledgeView.graph.Backlinks(selected.ID)
	if !selected.Created.IsZero() || !selected.Modified.IsZero() {
		created := "unknown"
		modified := "unknown"
		if !selected.Created.IsZero() {
			created = selected.Created.Local().Format("2006-01-02 15:04")
		}
		if !selected.Modified.IsZero() {
			modified = selected.Modified.Local().Format("2006-01-02 15:04")
		}
		lines = append(lines, sonutTheme.faint.Render("created "+created+"  ·  modified "+modified))
	}
	if selected.Provenance != "" {
		lines = append(lines, knowledgeWrappedStyled("provenance: ", selected.Provenance, width, sonutTheme.faint, sonutTheme.muted)...)
	}
	lines = append(lines, "", sonutTheme.ink.Bold(true).Render(fmt.Sprintf("Links  %d forward  ·  %d backlinks", len(forward), len(back))))
	shown := 0
	for _, edge := range forward {
		node := m.knowledgeNode(edge.Target)
		if node == nil {
			continue
		}
		lines = append(lines, knowledgeInspectorLinkLines("→", node.Title, edge, width, sonutTheme.blue)...)
		shown++
		if shown >= 2 {
			break
		}
	}
	for _, edge := range back {
		node := m.knowledgeNode(edge.Source)
		if node == nil {
			continue
		}
		lines = append(lines, knowledgeInspectorLinkLines("←", node.Title, edge, width, sonutTheme.muted)...)
		shown++
		if shown >= 4 {
			break
		}
	}
	if shown == 0 {
		lines = append(lines, sonutTheme.faint.Render("No explicit links yet."))
	}
	return strings.Join(lines, "\n")
}

func (m model) renderKnowledgeNotes(width int) string {
	lines := []string{knowledgeSection("Notes", width, fmt.Sprintf("%d Markdown files", len(m.knowledgeView.notes)))}
	notes := append([]knowledge.Note(nil), m.knowledgeView.notes...)
	sort.SliceStable(notes, func(i, j int) bool { return notes[i].Updated.After(notes[j].Updated) })
	if len(notes) == 0 {
		return strings.Join(append(lines, sonutTheme.faint.Render("No notes yet · press n to create one.")), "\n")
	}
	for _, note := range notes {
		marker := "  "
		if note.Name == m.knowledgeView.selectedID {
			marker = "▶ "
		}
		meta := fmt.Sprintf("%d links", len(note.Links))
		if len(note.Tags) > 0 {
			meta += " · #" + strings.Join(note.Tags, " #")
		}
		lines = append(lines, sonutTheme.ink.Render(marker+note.Title)+sonutTheme.faint.Render("  "+meta))
		if len(lines) >= maxInt(7, m.height-8) {
			break
		}
	}
	lines = append(lines, "", sonutTheme.faint.Render("j/k select · Enter inspect · n new · e edit · x trash after confirmation"))
	return strings.Join(lines, "\n")
}

func (m model) renderKnowledgeSearch(width int) string {
	lines := []string{knowledgeSection("Search", width, m.knowledgeView.searchLens.String())}
	query := m.knowledgeView.searchQuery
	if m.knowledgeView.searchFocused {
		lines = append(lines, m.knowledgeView.searchInput.View())
	} else {
		lines = append(lines, sonutTheme.ink.Render("/ "+query), sonutTheme.faint.Render("m lens · "+m.knowledgeView.searchLens.String()+" · / edit query"))
	}
	if len(m.knowledgeView.webResults) > 0 || m.knowledgeView.webQuery != "" {
		lines = append(lines, "", sonutTheme.amber.Bold(true).Render("NETWORK"), sonutTheme.faint.Render("Explicit web mode · page text is untrusted data; it cannot invoke tools or create links."))
		for i, result := range m.knowledgeView.webResults {
			marker := "  "
			if i == m.knowledgeView.selected {
				marker = "▶ "
			}
			lines = append(lines, sonutTheme.ink.Render(marker+result.Title)+sonutTheme.faint.Render("  "+result.Domain+"  "+result.Score))
			lines = append(lines, sonutTheme.muted.Render("    "+result.URL), sonutTheme.faint.Render("    "+result.Snippet))
		}
		if m.knowledgeView.webPreview != nil {
			lines = append(lines, "", knowledgeSection("Terminal preview", width, "Enter fetch · s save exact text"), sonutTheme.ink.Render(m.knowledgeView.webPreview.Title), sonutTheme.faint.Render(m.knowledgeView.webPreview.URL))
			preview := strings.Split(m.knowledgeView.webPreview.Body, "\n")
			for i := 0; i < len(preview) && i < 14; i++ {
				lines = append(lines, sonutTheme.muted.Render(preview[i]))
			}
		}
		return strings.Join(lines, "\n")
	}
	if len(m.knowledgeView.searchResults) == 0 {
		return strings.Join(append(lines, "", sonutTheme.faint.Render("No local matches. Try a broader query or m for Connected / Overview.")), "\n")
	}
	for i, hit := range m.knowledgeView.searchResults {
		marker := "  "
		if i == m.knowledgeView.selected {
			marker = "▶ "
		}
		connection := hit.RelevantLink
		if connection == "" {
			connection = hit.Provenance
		}
		confidence := ""
		if hit.Confidence > 0 {
			confidence = fmt.Sprintf(" · %.2f confidence", hit.Confidence)
		}
		lines = append(lines, sonutTheme.ink.Render(marker+hit.Title)+sonutTheme.faint.Render(fmt.Sprintf("  %.2f · %s · %s%s", hit.Score, hit.SourceType, connection, confidence)))
		lines = append(lines, "    "+knowledgeHighlightedSnippet(query, hit.Snippet))
	}
	return strings.Join(lines, "\n")
}

func knowledgeHighlightedSnippet(query, snippet string) string {
	snippet = strings.Join(strings.Fields(snippet), " ")
	if snippet == "" {
		return sonutTheme.muted.Render("(no snippet)")
	}
	terms := strings.Fields(strings.ToLower(query))
	sort.SliceStable(terms, func(i, j int) bool { return len(terms[i]) > len(terms[j]) })
	if len(terms) == 0 {
		return sonutTheme.muted.Render(snippet)
	}
	var out strings.Builder
	for offset := 0; offset < len(snippet); {
		lower := strings.ToLower(snippet[offset:])
		bestStart, bestEnd := -1, -1
		for _, term := range terms {
			if term == "" {
				continue
			}
			start := strings.Index(lower, term)
			if start < 0 || (bestStart >= 0 && start >= bestStart) {
				continue
			}
			bestStart, bestEnd = start, start+len(term)
		}
		if bestStart < 0 {
			out.WriteString(sonutTheme.muted.Render(snippet[offset:]))
			break
		}
		if bestStart > 0 {
			out.WriteString(sonutTheme.muted.Render(snippet[offset : offset+bestStart]))
		}
		out.WriteString(sonutTheme.accent.Bold(true).Render(snippet[offset+bestStart : offset+bestEnd]))
		offset += bestEnd
	}
	return out.String()
}

func (m model) renderKnowledgeSources(width int) string {
	lines := []string{knowledgeSection("Sources", width, "provenance first")}
	groups := map[string][]knowledge.Note{}
	for _, note := range m.knowledgeView.notes {
		kind := "manual"
		if note.Kind == "session" || strings.HasPrefix(note.Name, "session-") {
			kind = "session"
		} else if note.SourceURL != "" {
			kind = "web"
		} else if strings.EqualFold(note.Kind, "project") || strings.EqualFold(note.Kind, string(knowledge.NodeProjectConcept)) {
			kind = "project / local"
		}
		groups[kind] = append(groups[kind], note)
	}
	keys := make([]string, 0, len(groups))
	for key := range groups {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		lines = append(lines, "", sonutTheme.accent.Bold(true).Render(strings.ToUpper(key))+sonutTheme.faint.Render(fmt.Sprintf("  %d", len(groups[key]))))
		for _, note := range groups[key] {
			marker := "  "
			if note.Name == m.knowledgeView.selectedID {
				marker = "▶ "
			}
			source := note.SourceURL
			if source == "" {
				source = note.Path
			}
			lines = append(lines, sonutTheme.ink.Render(marker+note.Title)+sonutTheme.faint.Render("  "+source))
		}
	}
	if len(lines) == 1 {
		lines = append(lines, sonutTheme.faint.Render("No provenance records yet."))
	}
	return strings.Join(lines, "\n")
}

func (m model) renderKnowledgeTimeline(width int) string {
	groupName := []string{"day", "week", "month"}[m.knowledgeView.timelineGrouping]
	lines := []string{knowledgeSection("Timeline", width, "grouped by "+groupName)}
	notes := append([]knowledge.Note(nil), m.knowledgeView.notes...)
	sort.SliceStable(notes, func(i, j int) bool { return notes[i].Updated.After(notes[j].Updated) })
	lastGroup := ""
	for _, note := range notes {
		stamp := note.Updated
		if stamp.IsZero() {
			stamp = note.Modified
		}
		when := "unknown time"
		if !stamp.IsZero() {
			when = stamp.Local().Format("2006-01-02 15:04")
		}
		group := knowledgeTimelineGroup(stamp, m.knowledgeView.timelineGrouping)
		if group != lastGroup {
			lines = append(lines, "", sonutTheme.accent.Bold(true).Render(group))
			lastGroup = group
		}
		marker := "  "
		if note.Name == m.knowledgeView.selectedID {
			marker = "▶ "
		}
		lines = append(lines, sonutTheme.faint.Render(when)+"  "+sonutTheme.ink.Render(marker+note.Title)+sonutTheme.faint.Render("  "+firstNonEmptyKnowledge(note.Kind, "note")))
		if len(lines) > maxInt(8, m.height-7) {
			break
		}
	}
	lines = append(lines, "", sonutTheme.faint.Render("Evidence is limited to timestamps recorded by local Markdown files; this is not unsupported time travel."))
	return strings.Join(lines, "\n")
}

func knowledgeTimelineGroup(stamp time.Time, grouping int) string {
	if stamp.IsZero() {
		return "Unknown date"
	}
	stamp = stamp.Local()
	switch grouping {
	case 1:
		year, week := stamp.ISOWeek()
		return fmt.Sprintf("%04d-W%02d", year, week)
	case 2:
		return stamp.Format("2006-01")
	default:
		return stamp.Format("2006-01-02")
	}
}

func (m model) renderKnowledgeInsights(width int) string {
	g := m.knowledgeView.graph
	lines := []string{knowledgeSection("Insights", width, "deterministic local evidence")}
	hubs := g.Hubs(5)
	if len(hubs) > 0 {
		lines = append(lines, "", sonutTheme.accent.Bold(true).Render("Hubs"))
		for _, hub := range hubs {
			lines = append(lines, fmt.Sprintf("  %s  %s  degree %d", knowledgeNodeGlyph(hub.Node), hub.Node.Title, int(hub.Score)))
		}
	}
	ghosts := g.Ghosts()
	orphans := g.Orphans()
	lines = append(lines, "", sonutTheme.amber.Bold(true).Render("Unresolved / isolated"), fmt.Sprintf("  %d ghost node(s) · %d orphan node(s)", len(ghosts), len(orphans)))
	if len(ghosts) > 0 {
		lines = append(lines, sonutTheme.faint.Render("  "+ghosts[0].Title+" is linked but has no source note"))
	}
	surprising := g.SurprisingConnections(4)
	lines = append(lines, "", sonutTheme.blue.Bold(true).Render("Cross-community routes"))
	if len(surprising) == 0 {
		lines = append(lines, sonutTheme.faint.Render("  None detected from explicit links."))
	} else {
		for _, edge := range surprising {
			lines = append(lines, sonutTheme.faint.Render("  "+edge.Source+" → "+edge.Target+" · "+knowledgeEdgeMarker(edge)))
		}
	}
	aliases := g.DuplicateAliasCandidates()
	lines = append(lines, "", sonutTheme.ink.Bold(true).Render("Metadata quality"))
	lines = append(lines, sonutTheme.faint.Render(fmt.Sprintf("  %d stale source(s) · %d duplicate/alias candidate(s)", len(g.StaleSources()), len(aliases))))
	contested := 0
	for _, node := range g.Nodes {
		if node.Contested {
			contested++
		}
	}
	lines = append(lines, sonutTheme.faint.Render(fmt.Sprintf("  %d contested node(s) · evidence remains local and inspectable", contested)))
	lines = append(lines, "", sonutTheme.faint.Render("Suggested next step: press n to capture the missing idea, or select a ghost and press Enter to inspect its evidence boundary."))
	return strings.Join(lines, "\n")
}

func firstNonEmptyKnowledge(value, fallback string) string {
	if strings.TrimSpace(value) != "" {
		return value
	}
	return fallback
}

func (m model) renderKnowledgeEditor(width int) string {
	ed := m.knowledgeView.editor
	title := ed.title.View()
	tags := ed.tags.View()
	source := ed.source.View()
	lines := []string{knowledgeSection("Write note", width, "Ctrl+S save · Esc cancel"), "", sonutTheme.faint.Render("Title"), title, "", sonutTheme.faint.Render("Tags  comma or space separated"), tags, "", sonutTheme.faint.Render("Markdown body"), ed.body.View()}
	lines = append(lines, "", sonutTheme.faint.Render("Source URL (optional; preserved as provenance)"), source)
	if ed.field == knowledgeEditorBody {
		lines = append(lines, "", sonutTheme.accent.Render("Suggested links: "+m.knowledgeEditorSuggestions(ed.body.Value())))
	}
	lines = append(lines, "", sonutTheme.faint.Render("Tab next field · Enter next field · [[wikilinks]] are extracted, never inferred"))
	return strings.Join(lines, "\n")
}

func (m model) knowledgeEditorSuggestions(body string) string {
	if !strings.Contains(body, "[[") {
		return "type [[ to discover notes"
	}
	last := strings.LastIndex(body, "[[")
	fragment := body[last+2:]
	if strings.Contains(fragment, "]]") {
		return "saved as an explicit link"
	}
	fragment = strings.ToLower(strings.TrimSpace(fragment))
	var matches []string
	for _, note := range m.knowledgeView.notes {
		if fragment == "" || strings.Contains(strings.ToLower(note.Title), fragment) || strings.Contains(note.Name, fragment) {
			matches = append(matches, note.Title)
		}
	}
	if len(matches) == 0 {
		return "no existing note · ghost nodes remain visible"
	}
	if len(matches) > 4 {
		matches = matches[:4]
	}
	return strings.Join(matches, " · ")
}

func (m model) renderKnowledgeHelp(width int) string {
	lines := []string{knowledgeSection("Knowledge Atlas help", width, "keyboard authoritative"), "", sonutTheme.ink.Bold(true).Render("Navigate"), "  ↑/↓ or j/k  select", "  h/l          backlinks / forward links", "  Tab, ←/→, 1–6 tabs", "  Enter        inspect / expand", "", sonutTheme.ink.Bold(true).Render("Create and trust"), "  n new note · e edit · Ctrl+S save · Esc cancel/back", "  o explicitly open source · s save reviewed web preview", "  x recoverable trash after y confirmation", "  / search · m cycle Local / Connected / Overview", "  [ / ] depth · g grouping · f filters · p pin", "  Space pause settle pulse · ? close help", "", sonutTheme.faint.Render("Local Markdown knowledge • network only when requested • inferred links are labeled")}
	return strings.Join(lines, "\n")
}

func (m model) knowledgeFooterView(width int) string {
	if m.knowledgeView.editor.active {
		return fitStyledLine(sonutTheme.faint.Render("Tab next field  ·  Ctrl+S save  ·  Esc cancel"), width)
	}
	hints := "j/k move · h/l links · Enter inspect · n new · e edit · / search · [ ] depth · Space pause · ? help · Esc close"
	if m.knowledgeView.tab == knowledgeTabSources {
		hints = "j/k move · Enter inspect · r refresh · o open source · n new · e edit · ? help · Esc close"
	}
	if m.knowledgeView.tab == knowledgeTabSearch {
		hints = "m lens · / query · Enter preview · ,/. history · s save source · o external · j/k move · Esc close"
	}
	return fitStyledLine(sonutTheme.faint.Render(hints), width)
}

func (m model) knowledgeHeaderAndFooter(width int) (string, string) {
	header := m.knowledgeViewNavBar(width)
	return header, m.knowledgeFooterView(width)
}

func (m model) handleKnowledgeMouse(msg tea.MouseMsg) (model, tea.Cmd, bool) {
	if !m.knowledgeView.active {
		return m, nil, false
	}
	if mouseWheelUp(msg) {
		m.chatScrollOffset += chatWheelScrollLines
		return m, nil, true
	}
	if mouseWheelDown(msg) {
		m.chatScrollOffset = maxInt(0, m.chatScrollOffset-chatWheelScrollLines)
		return m, nil, true
	}
	if mouseLeftPress(msg) {
		// The graph remains keyboard-authoritative. A click on a visible node is
		// intentionally conservative: use the stable list index instead of
		// guessing from styled cell widths, which keeps SSH/no-color behavior exact.
		if m.knowledgeView.tab == knowledgeTabNotes || m.knowledgeView.tab == knowledgeTabSearch {
			m = m.selectKnowledgeDelta(1)
			return m, nil, true
		}
	}
	return m, nil, true
}

func (m model) applyKnowledgeFrame() model {
	if m.knowledgeView.active && !m.knowledgeView.paused && !m.reducedMotion {
		m.knowledgeView.frame++
	}
	return m
}
