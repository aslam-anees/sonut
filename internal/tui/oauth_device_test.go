package tui

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/aistatine/sonut/internal/oauth"
)

func seedOAuthToken(t *testing.T, providerID, access string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "tok.json")
	t.Setenv("SONUT_OAUTH_TOKENS_PATH", path)
	store, err := oauth.NewStore(oauth.StoreOptions{FilePath: path})
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	if err := store.Save(oauth.ProviderKey(providerID), oauth.Token{AccessToken: access, ExpiresAt: time.Now().Add(time.Hour)}); err != nil {
		t.Fatalf("seed token: %v", err)
	}
}

func TestOAuthStoredTokenReadsStoredLogin(t *testing.T) {
	seedOAuthToken(t, "xai", "live-grok-token")

	if got := oauthStoredToken(context.Background(), "xai"); got != "live-grok-token" {
		t.Fatalf("oauthStoredToken(xai) = %q, want live-grok-token", got)
	}
	// No login for another provider → empty (no error, no panic).
	if got := oauthStoredToken(context.Background(), "openrouter"); got != "" {
		t.Fatalf("oauthStoredToken(openrouter) = %q, want empty", got)
	}
}
