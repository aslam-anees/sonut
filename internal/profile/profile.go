package profile

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// Profile represents a user's @code configuration and preferences
type Profile struct {
	UserID        string        `json:"user_id"`
	Email         string        `json:"email"`
	DisplayName   string        `json:"display_name"`
	Preferences   Preferences   `json:"preferences"`
	Settings      Settings      `json:"settings"`
	ModelDefaults ModelDefaults `json:"model_defaults"`
	Stats         Stats         `json:"stats"`
	CreatedAt     time.Time     `json:"created_at"`
	UpdatedAt     time.Time     `json:"updated_at"`
}

// Preferences user preferences
type Preferences struct {
	Theme              string `json:"theme"`                // "dark", "light", "auto"
	DefaultContextTier string `json:"default_context_tier"` // "32k", "128k", "256k"
	AutoCompact        bool   `json:"auto_compact"`         // Auto-compress old turns
	CompactThreshold   int    `json:"compact_threshold"`    // Tokens before auto-compact
	ShowTips           bool   `json:"show_tips"`            // Show contextual tips
	TipFrequency       string `json:"tip_frequency"`        // "always", "often", "sometimes"
	PermissionMode     string `json:"permission_mode"`      // "sandbox", "confirm", "unrestricted"
	AutoSave           bool   `json:"auto_save"`            // Auto-save sessions
	SaveInterval       int    `json:"save_interval"`        // Seconds
	VerboseOutput      bool   `json:"verbose_output"`       // Show detailed output
	ColorOutput        bool   `json:"color_output"`         // Use colored text
	GPUAcceleration    bool   `json:"gpu_acceleration"`     // Use GPU if available
	TelemetryOptIn     bool   `json:"telemetry_opt_in"`     // Share anonymized usage
}

// Settings advanced settings
type Settings struct {
	MaxContextTokens     int             `json:"max_context_tokens"`
	MaxOutputTokens      int             `json:"max_output_tokens"`
	Temperature          float64         `json:"temperature"`
	TopP                 float64         `json:"top_p"`
	TopK                 int             `json:"top_k"`
	RepeatPenalty        float64         `json:"repeat_penalty"`
	PresencePenalty      float64         `json:"presence_penalty"`
	FrequencyPenalty     float64         `json:"frequency_penalty"`
	SystemPromptOverride string          `json:"system_prompt_override"`
	CustomBashAllowList  []string        `json:"custom_bash_allow_list"`
	CustomBashBlockList  []string        `json:"custom_bash_block_list"`
	MutedMCPServers      []string        `json:"muted_mcp_servers"`
	EnabledTools         map[string]bool `json:"enabled_tools"`
}

// ModelDefaults default model configuration
type ModelDefaults struct {
	Model              string `json:"model"`
	QuantizationFormat string `json:"quantization_format"` // "Q4_0", "Q5_0", "BF16"
	GPULayers          int    `json:"gpu_layers"`
	Threads            int    `json:"threads"`
	ThreadsBatch       int    `json:"threads_batch"`
	BatchSize          int    `json:"batch_size"`
}

// Stats user statistics
type Stats struct {
	TotalSessions      int       `json:"total_sessions"`
	TotalTurns         int       `json:"total_turns"`
	TotalTokensUsed    int64     `json:"total_tokens_used"`
	LastActiveAt       time.Time `json:"last_active_at"`
	FavoriteTools      []string  `json:"favorite_tools"`
	MostUsedCommands   []string  `json:"most_used_commands"`
	AverageTurnsPerSes int       `json:"average_turns_per_session"`
}

// Manager handles profile operations
type Manager struct {
	configDir string
	profile   *Profile
}

// NewManager creates a new profile manager
func NewManager(configDir string) *Manager {
	return &Manager{
		configDir: configDir,
	}
}

// DefaultProfile returns a profile with sensible defaults
func DefaultProfile(userID, email string) *Profile {
	return &Profile{
		UserID:    userID,
		Email:     email,
		CreatedAt: time.Now(),
		UpdatedAt: time.Now(),
		Preferences: Preferences{
			Theme:              "dark",
			DefaultContextTier: "256k",
			AutoCompact:        true,
			CompactThreshold:   24000,
			ShowTips:           true,
			TipFrequency:       "often",
			PermissionMode:     "confirm",
			AutoSave:           true,
			SaveInterval:       30,
			VerboseOutput:      false,
			ColorOutput:        true,
			GPUAcceleration:    true,
			TelemetryOptIn:     false,
		},
		Settings: Settings{
			MaxContextTokens: 131072, // 128K default
			MaxOutputTokens:  4096,
			Temperature:      0.7,
			TopP:             0.9,
			TopK:             40,
			RepeatPenalty:    1.1,
			PresencePenalty:  0.0,
			FrequencyPenalty: 0.0,
			EnabledTools: map[string]bool{
				"file":    true,
				"bash":    true,
				"browser": true,
				"code":    true,
				"vision":  true,
			},
		},
		ModelDefaults: ModelDefaults{
			Model:              "qwen2.5-coder-4b-q4_0",
			QuantizationFormat: "Q4_0",
			GPULayers:          999, // Auto-detect
			Threads:            0,   // Auto-detect
			ThreadsBatch:       1,
			BatchSize:          256,
		},
		Stats: Stats{
			TotalSessions: 0,
			TotalTurns:    0,
		},
	}
}

// Load loads a profile from disk
func (m *Manager) Load(userID string) (*Profile, error) {
	profileFile := filepath.Join(m.configDir, fmt.Sprintf("profile_%s.json", userID))

	data, err := os.ReadFile(profileFile)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("failed to read profile: %w", err)
	}

	var profile Profile
	if err := json.Unmarshal(data, &profile); err != nil {
		return nil, fmt.Errorf("failed to parse profile: %w", err)
	}

	m.profile = &profile
	return &profile, nil
}

// Save persists the profile to disk
func (m *Manager) Save(profile *Profile) error {
	if profile == nil {
		return fmt.Errorf("profile is nil")
	}

	profile.UpdatedAt = time.Now()

	profileFile := filepath.Join(m.configDir, fmt.Sprintf("profile_%s.json", profile.UserID))

	data, err := json.MarshalIndent(profile, "", "  ")
	if err != nil {
		return fmt.Errorf("failed to marshal profile: %w", err)
	}

	if err := os.WriteFile(profileFile, data, 0600); err != nil {
		return fmt.Errorf("failed to write profile: %w", err)
	}

	m.profile = profile
	return nil
}

// Get returns the current profile
func (m *Manager) Get() *Profile {
	return m.profile
}

// Update updates a profile field
func (m *Manager) Update(userID string, key string, value interface{}) error {
	if m.profile == nil {
		return fmt.Errorf("no profile loaded")
	}

	// Update nested fields
	switch key {
	case "display_name":
		if v, ok := value.(string); ok {
			m.profile.DisplayName = v
		}
	case "theme":
		if v, ok := value.(string); ok {
			m.profile.Preferences.Theme = v
		}
	case "permission_mode":
		if v, ok := value.(string); ok {
			m.profile.Preferences.PermissionMode = v
		}
	case "default_context_tier":
		if v, ok := value.(string); ok {
			m.profile.Preferences.DefaultContextTier = v
		}
	case "show_tips":
		if v, ok := value.(bool); ok {
			m.profile.Preferences.ShowTips = v
		}
	default:
		return fmt.Errorf("unknown profile field: %s", key)
	}

	return m.Save(m.profile)
}

// IncrementStats updates profile statistics
func (m *Manager) IncrementStats(sessionCompleted bool, turns int, tokensUsed int64) error {
	if m.profile == nil {
		return fmt.Errorf("no profile loaded")
	}

	m.profile.Stats.TotalTurns += turns
	m.profile.Stats.TotalTokensUsed += tokensUsed
	m.profile.Stats.LastActiveAt = time.Now()

	if sessionCompleted {
		m.profile.Stats.TotalSessions++
		if m.profile.Stats.TotalSessions > 0 {
			m.profile.Stats.AverageTurnsPerSes = m.profile.Stats.TotalTurns / m.profile.Stats.TotalSessions
		}
	}

	return m.Save(m.profile)
}

// RecordToolUsage records which tools are used
func (m *Manager) RecordToolUsage(toolName string) error {
	if m.profile == nil {
		return fmt.Errorf("no profile loaded")
	}

	// Simple frequency tracking
	for i, tool := range m.profile.Stats.FavoriteTools {
		if tool == toolName {
			// Move to end (most recent)
			m.profile.Stats.FavoriteTools = append(
				m.profile.Stats.FavoriteTools[:i],
				m.profile.Stats.FavoriteTools[i+1:]...,
			)
			m.profile.Stats.FavoriteTools = append(m.profile.Stats.FavoriteTools, toolName)
			return m.Save(m.profile)
		}
	}

	// New tool
	if len(m.profile.Stats.FavoriteTools) >= 10 {
		m.profile.Stats.FavoriteTools = m.profile.Stats.FavoriteTools[1:]
	}
	m.profile.Stats.FavoriteTools = append(m.profile.Stats.FavoriteTools, toolName)

	return m.Save(m.profile)
}

// ProfileCommand represents the /profile command
func ProfileCommand(action string, manager *Manager) error {
	profile := manager.Get()
	if profile == nil {
		return fmt.Errorf("no profile loaded")
	}

	switch action {
	case "show":
		fmt.Println("\n👤 Your @code Profile")
		fmt.Printf("Email: %s\n", profile.Email)
		if profile.DisplayName != "" {
			fmt.Printf("Name: %s\n", profile.DisplayName)
		}
		fmt.Printf("Theme: %s\n", profile.Preferences.Theme)
		fmt.Printf("Context Tier: %s\n", profile.Preferences.DefaultContextTier)
		fmt.Printf("Permission Mode: %s\n", profile.Preferences.PermissionMode)
		fmt.Printf("\nStats:\n")
		fmt.Printf("  Sessions: %d\n", profile.Stats.TotalSessions)
		fmt.Printf("  Total Turns: %d\n", profile.Stats.TotalTurns)
		fmt.Printf("  Tokens Used: %d\n", profile.Stats.TotalTokensUsed)
		if len(profile.Stats.FavoriteTools) > 0 {
			fmt.Printf("  Favorite Tools: %v\n", profile.Stats.FavoriteTools)
		}
		return nil

	case "reset":
		// Reset to defaults
		*profile = *DefaultProfile(profile.UserID, profile.Email)
		if err := manager.Save(profile); err != nil {
			return fmt.Errorf("failed to reset profile: %w", err)
		}
		fmt.Println("✅ Profile reset to defaults")
		return nil

	default:
		return fmt.Errorf("unknown profile action: %s", action)
	}
}
