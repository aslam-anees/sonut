package config

import (
	"os"
	"testing"
)

func TestLoadHasNoBundledHostedCredentials(t *testing.T) {
	for _, name := range []string{"SUPABASE_URL", "SUPABASE_ANON_KEY", "GOOGLE_CLIENT_ID", "GOOGLE_CLIENT_SECRET"} {
		t.Setenv(name, "")
		if err := os.Unsetenv(name); err != nil {
			t.Fatal(err)
		}
	}
	cfg := Load()
	if cfg.SupabaseURL != "" || cfg.SupabaseKey != "" || cfg.GoogleClientID != "" || cfg.GoogleClientSecret != "" {
		t.Fatal("local use must not select a bundled hosted project or credential")
	}
}

func TestLoadUsesExplicitHostedConfiguration(t *testing.T) {
	t.Setenv("SUPABASE_URL", "https://auth.example.com")
	t.Setenv("SUPABASE_ANON_KEY", "example-anon-value")
	t.Setenv("GOOGLE_CLIENT_ID", "example-client-id")
	t.Setenv("GOOGLE_CLIENT_SECRET", "example-client-value")
	cfg := Load()
	if cfg.SupabaseURL != "https://auth.example.com" || cfg.SupabaseKey != "example-anon-value" || cfg.GoogleClientID != "example-client-id" || cfg.GoogleClientSecret != "example-client-value" {
		t.Fatal("explicit hosted configuration must remain available")
	}
}
