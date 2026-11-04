package tui

import (
	"context"

	tea "charm.land/bubbletea/v2"

	"github.com/aistatine/sonut/internal/agent"
	"github.com/aistatine/sonut/internal/config"
	"github.com/aistatine/sonut/internal/mcp"
	"github.com/aistatine/sonut/internal/modelregistry"
	"github.com/aistatine/sonut/internal/providerhealth"
	"github.com/aistatine/sonut/internal/providermodeldiscovery"
	"github.com/aistatine/sonut/internal/sandbox"
	"github.com/aistatine/sonut/internal/sessions"
	"github.com/aistatine/sonut/internal/sonutruntime"
	"github.com/aistatine/sonut/internal/tools"
	"github.com/aistatine/sonut/internal/usage"
)

// Options configures the reusable @code terminal UI shell.
type Options struct {
	LlamaPort                   int  // port for the llama.cpp server (default 2748)
	LlamaVision                 bool // true when llama-server reports multimodal capability
	Cwd                         string
	UserConfigPath              string
	DoctorUserConfigPath        string
	ProjectConfigPath           string
	ProviderName                string
	ModelName                   string
	ProviderProfile             config.ProviderProfile
	SavedProviders              []config.ProviderProfile // all configured providers, for the /model multi-provider list
	FavoriteModels              []string
	RecapsEnabled               bool
	Provider                    sonutruntime.Provider
	NewProvider                 func(config.ProviderProfile) (sonutruntime.Provider, error)
	ProbeProviderHealth         func(context.Context, providerhealth.Options) providerhealth.Result
	DiscoverProviderModels      func(context.Context, config.ProviderProfile) ([]providermodeldiscovery.Model, error)
	DiscoverOllamaContextWindow func(ctx context.Context, baseURL string, model string) (int, error)
	RuntimeMessageSink          func(tea.Msg)
	Registry                    *tools.Registry
	SessionStore                *sessions.Store
	SandboxStore                *sandbox.GrantStore
	MCPConfig                   config.MCPConfig
	MCPPermissionStore          *mcp.PermissionStore
	MCPTokenStore               *mcp.TokenStore
	MCPCommand                  func(context.Context, []string) MCPCommandResult
	SandboxSetupCommand         func(context.Context) SandboxSetupCommandResult
	UsageTracker                *usage.Tracker
	SessionCompactor            SessionCompactor
	PrService                   *PrService
	SubmitFeedback              func(context.Context, string) error

	// ResumeSessionID, when non-empty, causes the TUI to resume the named session
	// instead of starting a fresh one. Set by the --resume CLI flag.
	ResumeSessionID string

	AgentOptions    agent.Options
	PermissionMode  agent.PermissionMode
	ReasoningEffort modelregistry.ReasoningEffort
	ResponseStyle   string
	// Theme is the operator's palette preference: "auto" (default), a built-in
	// ("dark"/"light"), or a registered color theme. Set from the --theme flag;
	// falls back to SONUT_THEME, then the persisted SavedTheme, then auto.
	Theme string
	// SavedTheme is the theme persisted in user config (Preferences.Theme). Applied
	// at startup below --theme and SONUT_THEME, so a /theme choice survives restart.
	SavedTheme string
	UserAgent  string

	// Notify configures completion / awaiting-input notifications.
	Notify config.NotifyConfig

	// Voice contains personal voice settings. The CLI resolves this from user
	// config; project config is deliberately not passed as a voice override.
	Voice config.VoiceConfig

	// AltScreen tells the model it is running inside Bubble Tea's alternate
	// screen. Run sets this for the interactive app; tests can leave it false
	// to exercise the native scrollback renderer.
	AltScreen bool

	// Setup configures the first-run/setup takeover. It is shown before the
	// normal chat surface when Visible is true.
	Setup SetupOptions
}

type MCPCommandResult struct {
	Config   config.MCPConfig
	Output   string
	Error    string
	ExitCode int
}

type SandboxSetupCommandResult struct {
	Output   string
	Error    string
	ExitCode int
}

// SetupOptions configures the guided first-run provider setup takeover.
type SetupOptions struct {
	Visible    bool
	Required   bool
	ConfigPath string
	Providers  []SetupProviderOption
	Save       func(SetupSelection) (SetupResult, error)
}

// SetupProviderOption is one provider choice offered by the setup takeover.
type SetupProviderOption struct {
	ID           string
	Name         string
	DefaultModel string
	EnvVar       string
	RequiresAuth bool
	Local        bool
	Recommended  bool
}

// SetupSelection is the user's setup choice.
type SetupSelection struct {
	CatalogID string
	Name      string
	BaseURL   string
	Model     string
	APIKey    string
}

// SetupResult describes a completed setup write.
type SetupResult struct {
	ConfigPath string
	Provider   config.ProviderProfile
}
