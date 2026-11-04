package tui

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/aistatine/sonut/internal/config"
	"github.com/aistatine/sonut/internal/providercatalog"
	"github.com/aistatine/sonut/internal/sonutruntime"
)

func TestProviderWizardUsesRuntimeProviderCatalog(t *testing.T) {
	wizard := newModel(context.Background(), Options{}).newProviderWizard()
	got := map[string]bool{}
	for _, provider := range wizard.providers {
		got[provider.ID] = true
		if !providercatalog.RuntimeSupported(provider) {
			t.Fatalf("wizard included unsupported provider %q", provider.ID)
		}
	}

	for _, provider := range providercatalog.All() {
		if !providercatalog.RuntimeSupported(provider) {
			continue
		}
		if !got[provider.ID] {
			t.Fatalf("wizard omitted runtime catalog provider %q", provider.ID)
		}
	}
	for _, unsupported := range []string{"bedrock", "vertex"} {
		if got[unsupported] {
			t.Fatalf("wizard should not include unsupported provider %q", unsupported)
		}
	}
}

func TestProviderWizardModelStepUsesFriendlyNamesAndStaysCompact(t *testing.T) {
	wizard := &providerWizardState{
		step:        providerWizardStepModel,
		modelSource: "models.dev",
		models:      providerWizardManyModelsForTest(18),
	}
	wizard.models[0] = providerWizardModel{
		ID:          "x-ai/grok-4.3",
		Description: "Grok 4.3",
		Meta:        "1M ctx · tools · reasoning",
	}

	view := plainRender(t, strings.Join(wizard.renderModelStep(84), "\n"))
	assertContains(t, view, "search > \u258cmodel name...")
	assertContains(t, view, "\u258c")
	assertNotContains(t, view, "SearchÃ")
	assertNotContains(t, view, "Searchâ")
	assertContains(t, view, "Grok 4.3")
	assertContains(t, view, "x-ai/grok-4.3 · 1M ctx · tools · reasoning")
	assertNotContains(t, view, "❯ x-ai/grok-4.3")
	assertNotContains(t, view, "more models")
	assertNotContains(t, view, "Model 17")
}

func TestProviderWizardModelSearchFiltersAndAppliesRawModelID(t *testing.T) {
	m := newModel(context.Background(), Options{})
	m.providerWizard = &providerWizardState{
		step:        providerWizardStepModel,
		modelSource: "models.dev",
		providers: []providercatalog.Descriptor{{
			ID:                  "openrouter",
			Name:                "OpenRouter",
			Transport:           providercatalog.TransportOpenAICompatible,
			DefaultBaseURL:      "https://openrouter.ai/api/v1",
			DefaultModel:        "openai/gpt-4.1",
			AuthEnvVars:         []string{"OPENROUTER_API_KEY"},
			RequiresAuth:        true,
			SupportedAPIFormats: []providercatalog.APIFormat{providercatalog.APIFormatOpenAIChatCompletions},
		}},
		models: []providerWizardModel{
			{ID: "openai/gpt-4.1", Description: "GPT-4.1", Meta: "1M ctx · tools"},
			{ID: "deepseek/deepseek-chat", Description: "DeepSeek Chat", Meta: "64K ctx · tools"},
			{ID: "deepseek/deepseek-v3.2", Description: "DeepSeek V3.2", Meta: "128K ctx · tools"},
		},
	}

	updated, _ := m.Update(testKeyText("deep"))
	next := updated.(model)

	if next.providerWizard.modelSearch != "deep" {
		t.Fatalf("model search = %q, want deep", next.providerWizard.modelSearch)
	}
	if got := next.providerWizard.currentModel().ID; got != "deepseek/deepseek-chat" {
		t.Fatalf("current model ID = %q, want raw OpenRouter ID", got)
	}
	view := plainRender(t, next.View())
	assertContains(t, view, "DeepSeek Chat")
	assertContains(t, view, "DeepSeek V3.2")
	assertNotContains(t, view, "GPT-4.1")
}

func TestProviderWizardBlocksAdvanceWhenModelSearchHasNoMatches(t *testing.T) {
	m := newModel(context.Background(), Options{})
	m.providerWizard = &providerWizardState{
		step: providerWizardStepModel,
		providers: []providercatalog.Descriptor{{
			ID:                  "openrouter",
			Name:                "OpenRouter",
			Transport:           providercatalog.TransportOpenAICompatible,
			DefaultBaseURL:      "https://openrouter.ai/api/v1",
			DefaultModel:        "openai/gpt-4.1",
			AuthEnvVars:         []string{"OPENROUTER_API_KEY"},
			RequiresAuth:        true,
			SupportedAPIFormats: []providercatalog.APIFormat{providercatalog.APIFormatOpenAIChatCompletions},
		}},
		models: []providerWizardModel{
			{ID: "openai/gpt-4.1", Description: "GPT-4.1"},
			{ID: "deepseek/deepseek-chat", Description: "DeepSeek Chat"},
		},
	}

	updated, _ := m.Update(testKeyText("nomatch"))
	next := updated.(model)
	updated, _ = next.Update(testKey(tea.KeyEnter))
	next = updated.(model)

	if next.providerWizard.step != providerWizardStepModel {
		t.Fatalf("wizard advanced to %v, want model step", next.providerWizard.step)
	}
	if next.providerWizard.err == "" {
		t.Fatal("expected model search error")
	}
	if got := next.providerWizard.currentModel().ID; got != "" {
		t.Fatalf("current model ID = %q, want empty when search has no matches", got)
	}
	view := plainRender(t, next.View())
	assertContains(t, view, "no matching models")
	assertContains(t, view, "choose a matching model")
}

func providerWizardManyModelsForTest(count int) []providerWizardModel {
	models := make([]providerWizardModel, 0, count)
	for index := 0; index < count; index++ {
		models = append(models, providerWizardModel{
			ID:          fmt.Sprintf("provider/model-%02d", index),
			Description: fmt.Sprintf("Model %02d", index),
			Meta:        "tools",
		})
	}
	return models
}

func TestWizardProviderStoredKey(t *testing.T) {
	m := model{savedProviders: []config.ProviderProfile{
		{Name: "acme", CatalogID: "acme-cloud", APIKeyStored: true},
		{Name: "nokey"},
	}}
	if name, ok := m.wizardProviderStoredKey(providercatalog.Descriptor{Name: "acme"}); !ok || name != "acme" {
		t.Fatalf("match by name = %q,%v", name, ok)
	}
	if name, ok := m.wizardProviderStoredKey(providercatalog.Descriptor{ID: "acme-cloud"}); !ok || name != "acme" {
		t.Fatalf("match by catalog id = %q,%v", name, ok)
	}
	if _, ok := m.wizardProviderStoredKey(providercatalog.Descriptor{Name: "nokey"}); ok {
		t.Fatal("provider without a stored key must not match")
	}
}

func TestProviderWizardManageKeyRemove(t *testing.T) {
	t.Setenv("SONUT_CRED_STORAGE", "encrypted-file")
	configPath := filepath.Join(t.TempDir(), "atcode", "config.json")
	if err := os.MkdirAll(filepath.Dir(configPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(configPath, []byte(`{"providers":[{"name":"acme","apiKeyStored":true}]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	store, err := config.ProviderKeyStoreAt(filepath.Dir(configPath))
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Set("acme", "sk-secret"); err != nil {
		t.Fatal(err)
	}

	m := newModel(context.Background(), Options{UserConfigPath: configPath})
	m.providerWizard = &providerWizardState{step: providerWizardStepManageKey, manageProviderName: "acme", manageKeyCursor: 2}
	next, _ := m.applyManageKeyChoice()
	if next.providerWizard != nil {
		t.Fatal("remove should close the wizard")
	}
	if _, ok, _ := store.Get("acme"); ok {
		t.Fatal("remove should delete the key from the credential store")
	}
}

func TestProviderWizardManageKeyReplaceAndKeep(t *testing.T) {
	m := newModel(context.Background(), Options{UserConfigPath: filepath.Join(t.TempDir(), "config.json")})

	m.providerWizard = &providerWizardState{step: providerWizardStepManageKey, manageProviderName: "acme", manageKeyCursor: 1}
	next, _ := m.applyManageKeyChoice()
	if next.providerWizard == nil || next.providerWizard.step != providerWizardStepCredential {
		t.Fatal("replace should route to the credential step")
	}

	m.providerWizard = &providerWizardState{step: providerWizardStepManageKey, manageProviderName: "acme", manageKeyCursor: 0}
	next, _ = m.applyManageKeyChoice()
	if next.providerWizard != nil {
		t.Fatal("keep should close the wizard without changes")
	}
}

// Applying the wizard switches the live provider, so it must export
// SONUT_PROVIDER exactly like the /model and /provider switch paths — a stale
// value from an earlier switch would otherwise win over config in every
// spawned child (applyEnv) and pin specialists/swarm members to the OLD
// provider's credentials.
func TestApplyProviderWizardExportsActiveProviderEnv(t *testing.T) {
	t.Setenv(config.ActiveProviderEnv, "stale-previous-provider")
	m := newModel(context.Background(), Options{})
	// Isolate the SUCCESS path's persist to a temp config (the default empty
	// path would skip persist; a future default must never reach the real user
	// config), and stub the build so the full commit sequence runs.
	m.userConfigPath = filepath.Join(t.TempDir(), "config.json")
	m.newProvider = func(config.ProviderProfile) (sonutruntime.Provider, error) {
		return &fakeProvider{}, nil
	}
	m.providerWizard = &providerWizardState{
		step:        providerWizardStepModel,
		profileName: "acme-wizard",
		providers: []providercatalog.Descriptor{{
			ID:                  "openrouter",
			Name:                "OpenRouter",
			Transport:           providercatalog.TransportOpenAICompatible,
			DefaultBaseURL:      "https://openrouter.ai/api/v1",
			DefaultModel:        "openai/gpt-4.1",
			AuthEnvVars:         []string{"OPENROUTER_API_KEY"},
			RequiresAuth:        true,
			SupportedAPIFormats: []providercatalog.APIFormat{providercatalog.APIFormatOpenAIChatCompletions},
		}},
		models: []providerWizardModel{{ID: "openai/gpt-4.1", Description: "GPT-4.1"}},
	}

	updated, _ := m.applyProviderWizard()
	next := updated

	if next.providerName == "" {
		t.Fatal("wizard apply should have set a provider name")
	}
	if got := os.Getenv(config.ActiveProviderEnv); got != next.providerName {
		t.Fatalf("%s = %q after wizard apply, want %q (children would spawn on the stale provider)", config.ActiveProviderEnv, got, next.providerName)
	}
}

// On a config PERSIST failure, applyProviderWizard must leave live state fully
// unchanged — the chat must NOT already be running on the new provider while the
// status line and the SONUT_PROVIDER export (which pins spawned children) still
// point at the old one. Build and persist are staged into locals; nothing is
// committed unless both succeed.
func TestApplyProviderWizardPersistFailureLeavesLiveStateUnchanged(t *testing.T) {
	t.Setenv("SONUT_CRED_STORAGE", "encrypted-file") // never touch the real OS keychain: apiKey is secured before the persist fails
	t.Setenv(config.ActiveProviderEnv, "old-provider")

	// A config path whose parent is a regular FILE, so writeConfigFile's MkdirAll
	// fails and UpsertProvider returns an error.
	blocker := filepath.Join(t.TempDir(), "not-a-dir")
	if err := os.WriteFile(blocker, []byte("x"), 0o600); err != nil {
		t.Fatalf("seed blocker file: %v", err)
	}
	brokenConfigPath := filepath.Join(blocker, "config.json")

	oldProvider := &fakeProvider{}
	newProvider := &fakeProvider{}
	m := newModel(context.Background(), Options{})
	m.provider = oldProvider
	m.providerProfile = config.ProviderProfile{Name: "old-provider"}
	m.providerName = "old-provider"
	m.userConfigPath = brokenConfigPath
	m.newProvider = func(config.ProviderProfile) (sonutruntime.Provider, error) { return newProvider, nil }
	m.providerWizard = &providerWizardState{
		step:        providerWizardStepModel,
		profileName: "acme-new",
		providers: []providercatalog.Descriptor{{
			ID:                  "openrouter",
			Name:                "OpenRouter",
			Transport:           providercatalog.TransportOpenAICompatible,
			DefaultBaseURL:      "https://openrouter.ai/api/v1",
			DefaultModel:        "openai/gpt-4.1",
			AuthEnvVars:         []string{"OPENROUTER_API_KEY"},
			RequiresAuth:        true,
			SupportedAPIFormats: []providercatalog.APIFormat{providercatalog.APIFormatOpenAIChatCompletions},
		}},
		models: []providerWizardModel{{ID: "openai/gpt-4.1", Description: "GPT-4.1"}},
		apiKey: "sk-new",
	}

	updated, _ := m.applyProviderWizard()
	next := updated

	if next.providerWizard == nil || next.providerWizard.err == "" {
		t.Fatal("a persist failure must surface a wizard error and keep the wizard open")
	}
	if next.provider != oldProvider {
		t.Fatal("live provider must NOT be swapped when the persist fails")
	}
	if next.providerName != "old-provider" {
		t.Fatalf("providerName = %q, want it unchanged on persist failure", next.providerName)
	}
	if got := os.Getenv(config.ActiveProviderEnv); got != "old-provider" {
		t.Fatalf("%s = %q, want it unchanged on persist failure (children would diverge from parent)", config.ActiveProviderEnv, got)
	}
}
