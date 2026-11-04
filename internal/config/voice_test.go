package config

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestVoiceConfigDefaultsAndValidation(t *testing.T) {
	cfg := DefaultVoiceConfig()
	if cfg.Shortcut != "hold:space:2s" || cfg.SilenceMs != 3000 || cfg.ResponseVoice != "F1" || cfg.Speed != 1.05 {
		t.Fatalf("defaults = %+v", cfg)
	}
	if err := cfg.Validate(); err != nil {
		t.Fatal(err)
	}
	for _, bad := range []VoiceConfig{
		func() VoiceConfig { x := cfg; x.Speed = 0.49; return x }(),
		func() VoiceConfig { x := cfg; x.Speed = 2.01; return x }(),
		func() VoiceConfig { x := cfg; x.ResponseVoice = "M1"; return x }(),
		func() VoiceConfig { x := cfg; x.SilenceMs = 499; return x }(),
	} {
		if err := bad.Validate(); err == nil {
			t.Fatalf("invalid voice config accepted: %+v", bad)
		}
	}
}

func TestVoiceConfigExplicitFalseSurvivesMigrationDecode(t *testing.T) {
	var cfg FileConfig
	if err := json.Unmarshal([]byte(`{"voice":{"enabled":false}}`), &cfg); err != nil {
		t.Fatal(err)
	}
	resolved, err := Resolve(ResolveOptions{Env: map[string]string{}})
	if err != nil {
		t.Fatal(err)
	}
	if !resolved.Voice.Enabled {
		// An unrelated resolve has the normal default; this branch makes the
		// assertion below explicit rather than relying on zero-value semantics.
		t.Fatal("default voice unexpectedly disabled")
	}
	path := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(path, []byte(`{"voice":{"enabled":false}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	resolved, err = Resolve(ResolveOptions{UserConfigPath: path, Env: map[string]string{}})
	if err != nil {
		t.Fatal(err)
	}
	if resolved.Voice.Enabled {
		t.Fatal("explicit voice.enabled=false was replaced by defaults")
	}
}

func TestProjectVoiceConfigCannotOverrideUserVoiceSettings(t *testing.T) {
	dir := t.TempDir()
	userPath := filepath.Join(dir, "user.json")
	projectPath := filepath.Join(dir, "project.json")
	if err := os.WriteFile(userPath, []byte(`{"voice":{"language":"hi","shortcut":"f8","responseVoice":"F3"}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(projectPath, []byte(`{"voice":{"language":"ar","shortcut":"off","responseVoice":"F5"}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	resolved, err := Resolve(ResolveOptions{UserConfigPath: userPath, ProjectConfigPath: projectPath, Env: map[string]string{}})
	if err != nil {
		t.Fatal(err)
	}
	if resolved.Voice.Language != "hi" || resolved.Voice.Shortcut != "f8" || resolved.Voice.ResponseVoice != "F3" {
		t.Fatalf("project config changed personal voice settings: %+v", resolved.Voice)
	}
}
