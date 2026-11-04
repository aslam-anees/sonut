package tui

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/aistatine/sonut/internal/knowledge"
)

func TestKnowledgeCommandOpensNativeAtlasWithoutBrowser(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("SONUT_KNOWLEDGE_DIR", dir)
	if err := os.WriteFile(filepath.Join(dir, "alpha.md"), []byte("# Alpha\n\nLinks to [[beta]] #core\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	m, cmd := (model{cwd: t.TempDir(), width: 120, height: 40, altScreen: true}).startKnowledgeCommand("")
	if !m.knowledgeView.active || cmd == nil {
		t.Fatalf("/knowledge active=%v cmd=%v", m.knowledgeView.active, cmd != nil)
	}
	if m.browserHeadful || m.terminalBrowserActive {
		t.Fatal("opening /knowledge must not open a browser")
	}
	msg := cmd()
	loaded, ok := msg.(knowledgeLoadedMsg)
	if !ok {
		t.Fatalf("load command returned %T", msg)
	}
	next, _ := m.Update(loaded)
	m = next.(model)
	if len(m.knowledgeView.graph.Nodes) < 2 || !strings.Contains(ansi.Strip(m.transcriptView()), "Knowledge Atlas") {
		t.Fatalf("native atlas did not render graph: nodes=%d\n%s", len(m.knowledgeView.graph.Nodes), ansi.Strip(m.transcriptView()))
	}
	for _, width := range []int{80, 120, 180} {
		view := ansi.Strip(m.renderKnowledgeView(width - 2))
		for _, line := range strings.Split(view, "\n") {
			if ansi.StringWidth(line) > width-4 {
				t.Fatalf("knowledge render line width=%d > %d at width %d: %q", ansi.StringWidth(line), width-4, width, line)
			}
		}
	}
}

func TestKnowledgeEmptyDirectorySeedsWelcomeGraph(t *testing.T) {
	t.Setenv("SONUT_KNOWLEDGE_DIR", t.TempDir())
	m, cmd := (model{cwd: t.TempDir(), width: 80, height: 24, altScreen: true}).startKnowledgeCommand("")
	msg := cmd()
	loaded, ok := msg.(knowledgeLoadedMsg)
	if !ok {
		t.Fatalf("load command returned %T", msg)
	}
	m = m.applyKnowledgeLoaded(loaded)
	if len(m.knowledgeView.notes) != 2 || len(m.knowledgeView.graph.Nodes) < 2 {
		t.Fatalf("empty knowledge base did not seed welcome graph: notes=%d nodes=%d", len(m.knowledgeView.notes), len(m.knowledgeView.graph.Nodes))
	}
}

func TestKnowledgeNavigationAndCloseRestoreParentScroll(t *testing.T) {
	m := model{cwd: t.TempDir(), width: 100, height: 30, chatScrollOffset: 17}
	m.setComposerState(composerState{text: "draft prompt", cursor: 5})
	m, _ = m.openKnowledgeView(knowledgeTabAtlas, "", knowledgeLensLocal)
	m.knowledgeView.loading = false
	m.knowledgeView.graph = knowledgeTestGraph()
	m.knowledgeView.selectedID = "a"
	m.knowledgeView.selected = 0
	for _, key := range []tea.KeyPressMsg{testKey(tea.KeyTab), testKey(tea.KeyRight), testKeyText("3")} {
		m, _, _ = m.handleKnowledgeViewKey(key)
	}
	if m.knowledgeView.tab != knowledgeTabSearch {
		t.Fatalf("tab navigation ended on %d, want Search", m.knowledgeView.tab)
	}
	m, _, _ = m.handleKnowledgeViewKey(testKey(tea.KeyEsc))
	if m.knowledgeView.active || m.chatScrollOffset != 17 || m.composerValue() != "draft prompt" || !m.composerActive || m.composer.cursor != 5 {
		t.Fatalf("close did not restore parent state: active=%v offset=%d composer=%q cursor=%d", m.knowledgeView.active, m.chatScrollOffset, m.composerValue(), m.composer.cursor)
	}
}

func TestKnowledgeNumberShortcutsSelectTabs(t *testing.T) {
	m := model{cwd: t.TempDir(), width: 100, height: 30}
	m, _ = m.openKnowledgeView(knowledgeTabAtlas, "", knowledgeLensLocal)
	for index, want := range []knowledgeTab{knowledgeTabAtlas, knowledgeTabNotes, knowledgeTabSearch, knowledgeTabSources, knowledgeTabTimeline, knowledgeTabInsights} {
		m, _, handled := m.handleKnowledgeViewKey(testKeyText(string(rune('1' + index))))
		if !handled || m.knowledgeView.tab != want {
			t.Fatalf("shortcut %d selected %d, want %d", index+1, m.knowledgeView.tab, want)
		}
	}
}

func TestKnowledgeEditorSaveAndCancelAreNative(t *testing.T) {
	m := model{cwd: t.TempDir(), width: 100, height: 30}
	m, _ = m.openKnowledgeView(knowledgeTabAtlas, "", knowledgeLensLocal)
	m.knowledgeView.loading = false
	m = m.openKnowledgeEditor(nil)
	if !m.knowledgeView.editor.active || !m.knowledgeView.editor.title.Focused() {
		t.Fatal("n should focus the native title editor")
	}
	m.knowledgeView.editor.title.SetValue("Safe note")
	m.knowledgeView.editor.body.SetValue("body #tag")
	next, cmd, handled := m.handleKnowledgeViewKey(testKeyCtrl('s'))
	if !handled || cmd == nil || !next.knowledgeView.editor.savePending {
		t.Fatalf("Ctrl+S did not start native save: handled=%v cmd=%v", handled, cmd != nil)
	}
	m, _, _ = m.handleKnowledgeViewKey(testKey(tea.KeyEsc))
	if m.knowledgeView.editor.active {
		t.Fatal("Esc should cancel the editor")
	}
}

func TestKnowledgeResponsiveLargeGraphAndReducedMotion(t *testing.T) {
	nodes := make([]knowledge.GraphNode, 220)
	links := make([]knowledge.GraphLink, 0, 220)
	for i := range nodes {
		id := fmt.Sprintf("note-%03d", i)
		nodes[i] = knowledge.GraphNode{ID: id, Title: id, Kind: knowledge.NodeNote, Degree: 2}
		if i > 0 {
			links = append(links, knowledge.GraphLink{Source: id, Target: fmt.Sprintf("note-%03d", i-1), Type: "links_to", Origin: knowledge.OriginExtracted, Confidence: 1})
		}
	}
	m := model{width: 180, height: 40, reducedMotion: true}
	m.knowledgeView = knowledgeViewState{active: true, graph: knowledge.Graph{Nodes: nodes, Links: links}, selectedID: "note-000", graphDepth: 1, pinned: map[string]bool{}}
	if m.applyKnowledgeFrame().knowledgeView.frame != 0 {
		t.Fatal("reduced motion advanced the atlas frame")
	}
	for _, width := range []int{40, 80, 180} {
		for _, line := range strings.Split(ansi.Strip(m.renderKnowledgeView(width)), "\n") {
			if ansi.StringWidth(line) > width-2 {
				t.Fatalf("large graph line exceeds width %d: %q", width, line)
			}
		}
	}
}

func TestKnowledgeAtlasRendersLabelsNearVisibleNodes(t *testing.T) {
	m := model{width: 100, height: 30, reducedMotion: true}
	m.knowledgeView = knowledgeViewState{active: true, graphDepth: 1, selectedID: "a", graph: knowledgeTestGraph()}
	view := ansi.Strip(m.renderKnowledgeGraph(80))
	if !strings.Contains(view, "Alpha") || !strings.Contains(view, "Beta") {
		t.Fatalf("visible node labels missing from atlas:\n%s", view)
	}
	if !strings.Contains(view, "KNOWLEDGE CORE") {
		t.Fatalf("atlas center card missing:\n%s", view)
	}
}

func knowledgeTestGraph() knowledge.Graph {
	return knowledge.Graph{Nodes: []knowledge.GraphNode{{ID: "a", Title: "Alpha", Kind: knowledge.NodeNote}, {ID: "b", Title: "Beta", Kind: knowledge.NodeNote}}, Links: []knowledge.GraphLink{{Source: "a", Target: "b", Type: "links_to", Origin: knowledge.OriginExtracted}}}
}
