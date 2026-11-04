package config

import (
	"os"
	"strconv"
)

// Config holds all @code configuration
type Config struct {
	// Supabase Configuration
	SupabaseURL string
	SupabaseKey string

	// Google OAuth Configuration
	GoogleClientID     string
	GoogleClientSecret string
	GoogleRedirectURI  string

	// llama.cpp Configuration
	LlamaPort      int
	LlamaGPULayers int
	ContextSize    string
	ModelDir       string

	// Application Configuration
	AppVersion string
	AppName    string
	Verbose    bool
	Telemetry  bool
}

// Load loads configuration from environment or defaults
func Load() *Config {
	cfg := &Config{
		// Default values
		AppName:     "atcode",
		AppVersion:  "1.0.0",
		LlamaPort:   2748,
		ContextSize: "256k",
		Telemetry:   false,
	}

	// Optional Supabase services. No hosted project or credentials are bundled.
	cfg.SupabaseURL = getEnv("SUPABASE_URL", "")
	cfg.SupabaseKey = getEnv("SUPABASE_ANON_KEY", "")

	// Optional Google OAuth. Configure your own OAuth application.
	cfg.GoogleClientID = getEnv("GOOGLE_CLIENT_ID", "")
	cfg.GoogleClientSecret = getEnv("GOOGLE_CLIENT_SECRET", "")
	cfg.GoogleRedirectURI = getEnv("GOOGLE_REDIRECT_URI", "http://localhost:8080/callback")

	// llama.cpp Configuration
	if port := getProductEnv("LLAMA_PORT", ""); port != "" {
		// Parse port if provided
		if p, err := strconv.Atoi(port); err == nil {
			cfg.LlamaPort = p
		}
	}

	if layers := getProductEnv("GPU_LAYERS", ""); layers != "" {
		if l, err := strconv.Atoi(layers); err == nil {
			cfg.LlamaGPULayers = l
		}
	}

	cfg.ContextSize = getProductEnv("CONTEXT_SIZE", "256k")
	cfg.ModelDir = getProductEnv("MODEL_DIR", expandHome("~/.atcode/model"))

	// Application Configuration
	cfg.Verbose = getProductEnv("VERBOSE", "") == "1" || getProductEnv("VERBOSE", "") == "true"
	cfg.Telemetry = getProductEnv("TELEMETRY", "") == "1" || getProductEnv("TELEMETRY", "") == "true"

	return cfg
}

// getEnv gets an environment variable with a default value
func getEnv(key, defaultValue string) string {
	if value, exists := os.LookupEnv(key); exists {
		return value
	}
	return defaultValue
}

func getProductEnv(suffix, defaultValue string) string {
	if value, exists := os.LookupEnv("ATCODE_" + suffix); exists {
		return value
	}
	return getEnv("SONUT_"+suffix, defaultValue)
}

// expandHome expands ~ to home directory
func expandHome(path string) string {
	if len(path) > 0 && path[0] == '~' {
		home, err := os.UserHomeDir()
		if err != nil {
			return path
		}
		return home + path[1:]
	}
	return path
}
