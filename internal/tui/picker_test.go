package tui

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/aistatine/sonut/internal/config"
	"github.com/aistatine/sonut/internal/modelregistry"
	"github.com/aistatine/sonut/internal/providercatalog"
	"github.com/aistatine/sonut/internal/providermodeldiscovery"
	"github.com/aistatine/sonut/internal/sonutruntime"
)

func TestEffortCommandDoesNotOpenPicker(t *testing.T) {
	// Manual effort switching is removed: /effort renders the automatic-policy
	// card instead of opening a picker.
	m := newModel(context.Background(), Options{ModelName: "claude-sonnet-4.5"})
	m.input.SetValue("/effort")

	updated, _ := m.Update(testKey(tea.KeyEnter))
	m = updated.(model)
	if m.picker != nil {
		t.Fatalf("expected no effort picker, got %#v", m.picker)
	}
	if !transcriptContains(m.transcript, "managed automatically") {
		t.Fatalf("expected automatic-policy card, got %#v", m.transcript)
	}
}

func TestThemeCommandOpensPicker(t *testing.T) {
	// Bare /theme opens the theme popup (live preview on move, apply on Enter),
	// like /model and /effort. Full preview/commit/cancel behavior is covered in
	// theme_picker_test.go; here we just pin that the no-arg command opens it.
	defer applyTheme(themeDark, true)
	m := newModel(context.Background(), Options{})
	m.input.SetValue("/theme")
	updated, _ := m.Update(testKey(tea.KeyEnter))
	m = updated.(model)
	if m.picker == nil || m.picker.kind != pickerTheme {
		t.Fatalf("expected the theme picker to open, got %#v", m.picker)
	}
}

func TestEffortCommandWhilePendingDoesNotOpenPicker(t *testing.T) {
	// /effort no longer opens a picker at all (manual switching removed); even
	// mid-run it just renders the automatic-policy card without touching the
	// in-flight run.
	m := newModel(context.Background(), Options{ModelName: "claude-sonnet-4.5"})
	m.pending = true
	m.input.SetValue("/effort")

	updated, cmd := m.Update(testKey(tea.KeyEnter))
	next := updated.(model)
	if cmd != nil {
		t.Fatal("/effort while pending should not start a run")
	}
	if next.picker != nil {
		t.Fatalf("/effort should never open a picker, got %#v", next.picker)
	}
	if !next.pending {
		t.Fatal("/effort must not clear the in-flight run")
	}
}

func TestPickerRenders(t *testing.T) {
	m := newModel(context.Background(), Options{ModelName: "claude-sonnet-4.5"})
	m.width, m.height = 96, 30
	m.input.SetValue("/theme")
	updated, _ := m.Update(testKey(tea.KeyEnter))
	m = updated.(model)
	if !strings.Contains(viewString(m.View()), "theme") {
		t.Fatal("view should render the picker title")
	}
}

func TestPickerOverlayCapsVisibleRows(t *testing.T) {
	items := make([]pickerItem, 0, 20)
	for i := range 20 {
		items = append(items, pickerItem{Label: fmt.Sprintf("model-%02d", i), Value: fmt.Sprintf("model-%02d", i)})
	}
	m := newModel(context.Background(), Options{})
	m.picker = &commandPicker{
		kind:     pickerModel,
		title:    "Choose a model",
		items:    items,
		allItems: append([]pickerItem{}, items...),
		selected: 15,
	}

	got := plainRender(t, m.pickerOverlay(120))
	if !strings.Contains(got, "Choose a model") || !strings.Contains(got, "model-15") {
		t.Fatalf("picker overlay should render selected window, got %q", got)
	}
	if strings.Contains(got, "model-00") || strings.Contains(got, "model-09") {
		t.Fatalf("picker overlay should cap visible rows around selection, got %q", got)
	}
}

func TestModelContextWindowResolution(t *testing.T) {
	m := model{}
	// Registered model → the exact registry window (compare to the registry value so
	// this doesn't break on a benign catalog update).
	registry, err := modelregistry.DefaultRegistry()
	if err != nil {
		t.Fatalf("load default registry: %v", err)
	}
	entry, ok := registry.Resolve("gpt-4.1")
	if !ok || entry.ContextLimits.ContextWindow <= 0 {
		t.Fatalf("gpt-4.1 should resolve a positive window in the registry")
	}
	if got := m.modelContextWindow("gpt-4.1"); got != entry.ContextLimits.ContextWindow {
		t.Fatalf("registered model window = %d, want %d", got, entry.ContextLimits.ContextWindow)
	}
	// Unknown model → 0 (display shows no denominator); compaction applies its own
	// fallback via AgentContextWindow.
	if got := m.modelContextWindow("totally-unknown-proxy-model"); got != 0 {
		t.Fatalf("unknown model should resolve 0 for display, got %d", got)
	}
	if got := modelregistry.AgentContextWindow(m.modelContextWindow("totally-unknown-proxy-model")); got != modelregistry.FallbackContextWindow {
		t.Fatalf("compaction window for unknown model should be %d, got %d", modelregistry.FallbackContextWindow, got)
	}
	// Live-discovered window wins for an unregistered model.
	m.modelPickerLiveByProvider = map[string][]providermodeldiscovery.Model{
		"xai": {{ID: "grok-4", ContextWindow: 256000}},
	}
	if got := m.modelContextWindow("grok-4"); got != 256000 {
		t.Fatalf("discovered window should win for unregistered model, got %d", got)
	}
	// Empty name → 0 (no window).
	if got := m.modelContextWindow(""); got != 0 {
		t.Fatalf("empty model name should yield 0, got %d", got)
	}
	// A custom/local Ollama model tag has no curated-catalog entry and its
	// generic /v1/models listing carries no window either — the only source is
	// the Ollama-specific /api/show probe, landed via ollamaContextWindowByModel.
	m.ollamaContextWindowByModel = map[string]int{"kimi-k2.7-code:cloud": 131072}
	if got := m.modelContextWindow("kimi-k2.7-code:cloud"); got != 131072 {
		t.Fatalf("ollama-discovered window should be used for an unregistered model, got %d", got)
	}
}

// TestOllamaContextWindowDiscoveryCmdScopedToLocalOllama: the /api/show probe
// only makes sense against a local Ollama daemon (catalog ID "ollama") — a
// different provider like "ollama-cloud" is a distinct hosted service this
// endpoint isn't assumed to exist on, and every other provider already has
// its context window covered by the curated catalog or /v1/models discovery.
func TestOllamaContextWindowDiscoveryCmdScopedToLocalOllama(t *testing.T) {
	m := model{ctx: context.Background()}

	ollama, ok := providercatalog.Get("ollama")
	if !ok {
		t.Fatal("expected \"ollama\" to be a registered catalog descriptor")
	}
	if cmd := m.ollamaContextWindowDiscoveryCmd(ollama, "http://localhost:11434/v1", "kimi-k2.7-code:cloud"); cmd == nil {
		t.Fatal("expected a discovery command for the local ollama provider")
	}

	// ollama-cloud was removed from the catalog; a non-ollama descriptor (openai)
	// must not be probed with /api/show either.
	openai, ok := providercatalog.Get("openai")
	if !ok {
		t.Fatal("expected \"openai\" to be a registered catalog descriptor")
	}
	if cmd := m.ollamaContextWindowDiscoveryCmd(openai, "https://api.openai.com/v1", "gpt-4.1"); cmd != nil {
		t.Fatal("non-ollama providers must not be probed with /api/show")
	}

	if cmd := m.ollamaContextWindowDiscoveryCmd(ollama, "", "kimi-k2.7-code:cloud"); cmd != nil {
		t.Fatal("an empty base URL should yield no command")
	}
	if cmd := m.ollamaContextWindowDiscoveryCmd(ollama, "http://localhost:11434/v1", ""); cmd != nil {
		t.Fatal("an empty model name should yield no command")
	}
}

// TestOllamaContextWindowDiscoveredMsgPopulatesMap verifies the Update()
// handler for the async probe's result actually reaches modelContextWindow's
// fallback source, and that a failed/zero probe leaves the map untouched
// rather than caching a bogus zero.
func TestOllamaContextWindowDiscoveredMsgPopulatesMap(t *testing.T) {
	m := newModel(context.Background(), Options{})

	updated, cmd := m.Update(ollamaContextWindowDiscoveredMsg{modelName: "kimi-k2.7-code:cloud", contextWindow: 131072})
	next := updated.(model)
	if cmd != nil {
		t.Fatal("applying a discovered window should not schedule further work")
	}
	if got := next.modelContextWindow("kimi-k2.7-code:cloud"); got != 131072 {
		t.Fatalf("modelContextWindow after discovery = %d, want 131072", got)
	}

	updated, _ = next.Update(ollamaContextWindowDiscoveredMsg{modelName: "other-model", contextWindow: 0, err: errors.New("boom")})
	next = updated.(model)
	if got := next.modelContextWindow("other-model"); got != 0 {
		t.Fatalf("a failed probe must not populate a window, got %d", got)
	}
}

// TestSwitchProviderModelWarmsDiscoveryForTheNewProvider: switching providers
// mid-session (e.g. via the /model picker) previously fired no discovery at
// all for the newly active provider — Init() only warms the provider active
// at launch — so the context gauge stayed blank for the rest of the session
// unless /model happened to be reopened separately. Switching to Ollama
// specifically must also warm the /api/show probe, the only source of a
// context window for custom/local Ollama models.
func TestSwitchProviderModelWarmsDiscoveryForTheNewProvider(t *testing.T) {
	m := newModel(context.Background(), Options{
		ProviderName:    "openai",
		ModelName:       "gpt-5.1",
		Provider:        &fakeProvider{},
		ProviderProfile: config.ProviderProfile{Name: "openai", CatalogID: "openai", Model: "gpt-5.1"},
		SavedProviders: []config.ProviderProfile{
			{Name: "openai", CatalogID: "openai", Model: "gpt-5.1"},
			{Name: "ollama", CatalogID: "ollama", ProviderKind: config.ProviderKindOpenAICompatible, BaseURL: "http://localhost:11434/v1", Model: "kimi-k2.7-code:cloud"},
		},
		NewProvider: func(config.ProviderProfile) (sonutruntime.Provider, error) {
			return &fakeProvider{}, nil
		},
	})

	next, text, cmd := m.switchProviderModel("ollama", "kimi-k2.7-code:cloud")
	if !strings.Contains(text, "Switched to ollama") {
		t.Fatalf("switch notice = %q, want it to confirm the switch", text)
	}
	if next.modelName != "kimi-k2.7-code:cloud" || next.providerName != "ollama" {
		t.Fatalf("model/provider not switched: modelName=%q providerName=%q", next.modelName, next.providerName)
	}
	if cmd == nil {
		t.Fatal("switching to ollama should warm both the generic and ollama-specific discovery commands")
	}
}
