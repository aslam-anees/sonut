package tui

import (
	"errors"
	"strings"
	"testing"

	"github.com/aistatine/sonut/internal/providercatalog"
)

// wizardModelAt builds a model whose provider wizard is at step with providerID
// selected.
func wizardModelAt(t *testing.T, providerID string, step providerWizardStep) model {
	t.Helper()
	m := mouseTestModel()
	w := m.newProviderWizard()
	idx := -1
	for i, d := range w.providers {
		if d.ID == providerID {
			idx = i
			break
		}
	}
	if idx < 0 {
		t.Fatalf("provider %q not offered by the wizard", providerID)
	}
	w.selectedProvider = idx
	w.step = step
	m.providerWizard = w
	return m
}

func TestProviderWizardMethodChooserOAuthPath(t *testing.T) {
	m := mouseTestModel()
	m.providerWizard = m.newProviderWizard()
	if m.providerWizard.step != providerWizardStepMethod {
		t.Fatalf("wizard should open on the method chooser, got %v", m.providerWizard.step)
	}
	m.providerWizard.selectedMethod = 0 // "Sign in with OAuth" (default, first)
	next, _ := m.advanceProviderWizard()
	w := next.providerWizard
	if w.step != providerWizardStepProvider || !w.oauthMode {
		t.Fatalf("OAuth method should enter the provider step in oauthMode, got step=%v oauth=%v", w.step, w.oauthMode)
	}
	if len(w.providers) != len(providercatalog.OAuthProviders()) {
		t.Fatalf("OAuth path should list only OAuth providers, got %d", len(w.providers))
	}
	for _, d := range w.providers {
		if !d.OAuth {
			t.Fatalf("non-OAuth provider %q in the OAuth list", d.ID)
		}
	}
}

func TestProviderWizardMethodChooserBrowsePath(t *testing.T) {
	m := mouseTestModel()
	m.providerWizard = m.newProviderWizard()
	m.providerWizard.selectedMethod = len(providerWizardMethodOptions()) - 1 // "browse / API key"
	next, _ := m.advanceProviderWizard()
	w := next.providerWizard
	if w.step != providerWizardStepProvider || w.oauthMode {
		t.Fatalf("browse method should enter the provider step (not oauthMode), got step=%v oauth=%v", w.step, w.oauthMode)
	}
	if len(w.providers) <= len(providercatalog.OAuthProviders()) {
		t.Fatalf("browse path should list the full catalog, got %d", len(w.providers))
	}
}

func beginTestOAuthAttempt(wizard *providerWizardState, device bool) (string, int) {
	providerID := wizard.currentProvider().ID
	return providerID, wizard.beginOAuthAttempt(device)
}

func TestProviderWizardDeviceErrorSurfaced(t *testing.T) {
	m := mouseTestModel()
	m.providerWizard = m.newProviderWizard()
	providerID, attemptID := beginTestOAuthAttempt(m.providerWizard, true)

	out, cmd := m.applyProviderWizardDeviceCode(providerWizardDeviceCodeMsg{
		providerID: providerID, attemptID: attemptID, err: errors.New("device endpoint unreachable"),
	})
	if out.providerWizard.oauthPending || out.providerWizard.oauthDevice {
		t.Fatal("device error should clear pending/device state")
	}
	if out.providerWizard.oauthErr == "" {
		t.Fatal("device error should surface a message")
	}
	if cmd != nil {
		t.Fatal("device error should not start a poll")
	}
}

func TestProviderWizardOAuthSuccessClearsDeviceState(t *testing.T) {
	m := mouseTestModel()
	m.providerWizard = m.newProviderWizard()
	providerID, attemptID := beginTestOAuthAttempt(m.providerWizard, true)
	m.providerWizard.deviceUserCode = "X-1"
	m.providerWizard.deviceVerificationURI = "https://x.ai/device"

	out, _ := m.applyProviderWizardOAuth(providerWizardOAuthMsg{providerID: providerID, attemptID: attemptID, tokenLogin: true})
	if out.providerWizard.oauthDevice || out.providerWizard.deviceUserCode != "" || out.providerWizard.deviceVerificationURI != "" {
		t.Fatalf("success should clear device state: %+v", out.providerWizard)
	}
	if out.providerWizard.step != providerWizardStepModel {
		t.Fatalf("success should advance to the model step, got %v", out.providerWizard.step)
	}
}

func TestProviderWizardOAuthDispatchFromList(t *testing.T) {
	m := mouseTestModel()
	m.providerWizard = m.newProviderWizard()
	m.providerWizard.selectedMethod = 0
	next, _ := m.advanceProviderWizard() // → OAuth provider list
	// select openrouter
	found := false
	for i, d := range next.providerWizard.providers {
		if d.ID == "openrouter" {
			next.providerWizard.selectedProvider = i
			found = true
			break
		}
	}
	if !found {
		t.Fatal("openrouter not present in the OAuth provider list")
	}
	next, cmd := next.advanceProviderWizard()
	if !next.providerWizard.oauthPending {
		t.Fatal("advancing from the OAuth list should start the login (oauthPending)")
	}
	if next.providerWizard.oauthAttemptID == 0 {
		t.Fatal("advancing from the OAuth list should assign an OAuth attempt id")
	}
	if cmd == nil {
		t.Fatal("advancing from the OAuth list should return the OAuth command")
	}
}

func TestProviderWizardRetreatFromProviderToMethod(t *testing.T) {
	m := mouseTestModel()
	m.providerWizard = m.newProviderWizard()
	m.providerWizard.selectedMethod = 0
	next, _ := m.advanceProviderWizard() // → OAuth provider list (oauthMode)
	next.providerWizard.retreat()
	if next.providerWizard.step != providerWizardStepMethod {
		t.Fatalf("retreat from provider should return to method, got %v", next.providerWizard.step)
	}
	if next.providerWizard.oauthMode {
		t.Fatal("retreat to method should clear oauthMode")
	}
}

func TestProviderWizardSupportsOAuth(t *testing.T) {
	or, _ := providercatalog.Get("openrouter")
	if !providerWizardSupportsOAuth(or) {
		t.Fatal("openrouter should offer in-wizard OAuth")
	}
	oa, _ := providercatalog.Get("openai")
	if providerWizardSupportsOAuth(oa) {
		t.Fatal("openai should NOT offer in-wizard OAuth (no usable direct OAuth)")
	}
}

func TestProviderWizardCtrlOStartsOAuthForOpenRouter(t *testing.T) {
	m := wizardModelAt(t, "openrouter", providerWizardStepCredential)
	next, cmd := m.handleProviderWizardKey(testKeyCtrl('o'))
	if next.providerWizard == nil || !next.providerWizard.oauthPending {
		t.Fatal("ctrl+o should mark the wizard oauthPending")
	}
	if next.providerWizard.oauthAttemptID == 0 {
		t.Fatal("ctrl+o should assign an OAuth attempt id")
	}
	if cmd == nil {
		t.Fatal("ctrl+o should return a command to run the OAuth flow")
	}
}

func TestProviderWizardCtrlONoopForNonOAuthProvider(t *testing.T) {
	m := wizardModelAt(t, "openai", providerWizardStepCredential)
	next, _ := m.handleProviderWizardKey(testKeyCtrl('o'))
	if next.providerWizard != nil && next.providerWizard.oauthPending {
		t.Fatal("ctrl+o must not start OAuth for a provider that doesn't support it")
	}
}

func TestApplyProviderWizardOAuthSuccessAdvances(t *testing.T) {
	m := wizardModelAt(t, "openrouter", providerWizardStepCredential)
	providerID, attemptID := beginTestOAuthAttempt(m.providerWizard, false)
	next, _ := m.applyProviderWizardOAuth(providerWizardOAuthMsg{providerID: providerID, attemptID: attemptID, apiKey: "sk-or-minted"})
	if next.providerWizard == nil {
		t.Fatal("wizard should remain open")
	}
	if next.providerWizard.oauthPending {
		t.Fatal("pending should clear on success")
	}
	if next.providerWizard.apiKey != "sk-or-minted" {
		t.Fatalf("minted key not applied: %q", next.providerWizard.apiKey)
	}
	if next.providerWizard.step != providerWizardStepModel {
		t.Fatalf("should advance to the model step, got %v", next.providerWizard.step)
	}
}

func TestApplyProviderWizardOAuthErrorStays(t *testing.T) {
	m := wizardModelAt(t, "openrouter", providerWizardStepCredential)
	providerID, attemptID := beginTestOAuthAttempt(m.providerWizard, false)
	next, _ := m.applyProviderWizardOAuth(providerWizardOAuthMsg{providerID: providerID, attemptID: attemptID, err: errors.New("nope")})
	if next.providerWizard == nil {
		t.Fatal("wizard should remain open on error")
	}
	if next.providerWizard.oauthPending {
		t.Fatal("pending should clear on error")
	}
	if next.providerWizard.step != providerWizardStepCredential {
		t.Fatalf("should stay at credential step, got %v", next.providerWizard.step)
	}
	if next.providerWizard.oauthErr == "" {
		t.Fatal("oauthErr should be set")
	}
}

func TestRenderCredentialStepShowsOAuthHintAndPending(t *testing.T) {
	m := wizardModelAt(t, "openrouter", providerWizardStepCredential)
	w := m.providerWizard
	if !strings.Contains(strings.Join(w.renderCredentialStep(80), "\n"), "ctrl+o") {
		t.Fatal("credential step should show the ctrl+o OAuth hint for openrouter")
	}
	w.oauthPending = true
	if !strings.Contains(strings.Join(w.renderOAuthWaiting(80), "\n"), "Waiting for authorization") {
		t.Fatal("pending state should show the browser-waiting message")
	}
}

func TestApplyProviderWizardOAuthIgnoresStaleAttempt(t *testing.T) {
	m := wizardModelAt(t, "openrouter", providerWizardStepCredential)
	providerID, attemptID := beginTestOAuthAttempt(m.providerWizard, false)

	next, cmd := m.applyProviderWizardOAuth(providerWizardOAuthMsg{
		providerID: providerID,
		attemptID:  attemptID - 1,
		apiKey:     "sk-or-stale",
	})
	if cmd != nil {
		t.Fatal("stale OAuth result should not start a command")
	}
	if !next.providerWizard.oauthPending {
		t.Fatal("stale OAuth result should leave the active attempt pending")
	}
	if next.providerWizard.apiKey != "" {
		t.Fatalf("stale OAuth result applied api key %q", next.providerWizard.apiKey)
	}
	if next.providerWizard.step != providerWizardStepCredential {
		t.Fatalf("stale OAuth result moved step to %v", next.providerWizard.step)
	}
}
