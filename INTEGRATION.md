# @code Integration Guide

This document explains how @code's core systems work together and how to integrate them into your application.

## System Overview

@code consists of several independent packages that can be initialized and used together:

```
┌─────────────────────────────────────────┐
│      @code Initialization (init.go)     │
│    Creates and wires all systems        │
└─────────────────────────────────────────┘
                    │
        ┌───────────┼───────────────────┐
        │           │                   │
    ┌───▼───┐   ┌──▼───┐   ┌─────────┐ │
    │Config │   │Auth  │   │atcode   │ │
    │       │   │      │   │         │ │
    │• Env  │   │• Supabase          │ │
    │• Creds│   │• OAuth  │ • Server │ │
    │• Cloud│   │• Tokens │ • Models │ │
    └───────┘   └────────┘ └────────┘ │
        │           │           │      │
        └───────────┼───────────┘      │
                    │                   │
        ┌───────────┼────────┬──────────┘
        │           │        │
    ┌──▼────┐  ┌───▼──┐ ┌──▼─────┐
    │Profile│  │ Tips │ │ Theme  │
    │       │  │      │ │        │
    │• Prefs│  │• DB  │ │• Colors│
    │• Stats│  │• Tips│ │• Styles│
    │• Tools│  │• Suggest       │
    └───────┘  └──────┘ └────────┘
```

## Core Packages

### `internal/config/` - Configuration Management

**Purpose**: Load and provide access to all configuration

**Key Files**:
- `config.go` - Main configuration struct with embedded secrets

**Usage**:
```go
cfg := config.Load()
fmt.Println(cfg.SupabaseURL)      // Embedded default
fmt.Println(cfg.GoogleClientID)   // Can override with env vars
```

**Environment Variables**:
```
SUPABASE_URL
SUPABASE_ANON_KEY
GOOGLE_CLIENT_ID
GOOGLE_CLIENT_SECRET
ATCODE_LLAMA_PORT
ATCODE_GPU_LAYERS
ATCODE_CONTEXT_SIZE
ATCODE_MODEL_DIR
```

### `internal/auth/` - Authentication

**Purpose**: Handle user authentication via Supabase and Google OAuth

**Key Files**:
- `supabase.go` - Supabase API client
- `oauth.go` - Google OAuth 2.0 PKCE flow
- `storage.go` - Token and profile persistence
- `commands.go` - CLI command handlers

**Usage**:
```go
authMgr := auth.NewAuthManager(
    "https://...", "eyJ...",  // Supabase
    "871...", "GOCSPX-...",   // Google OAuth
    "~/.atcode",               // Config dir
)

// Login with magic link
authMgr.SupabaseClient.SignInWithMagicLink(ctx, "user@example.com")

// Check if logged in
if authMgr.IsLoggedIn() {
    user := authMgr.GetCurrentUser()
    fmt.Println(user.Email)
}
```

**Features**:
- ✅ Magic link authentication
- ✅ Google OAuth 2.0 PKCE
- ✅ Token auto-refresh
- ✅ Secure token storage (mode 0600)
- ✅ Profile persistence

### `internal/llama/` - Local Inference

**Purpose**: Manage atcode-server for local model inference

**Key Files**:
- `server.go` - Server process management
- `config.go` - Hardware detection and config optimization
- `models.go` - Model download and management
- `warmup.go` - Model prefill and performance estimation

**Usage**:
```go
// Detect hardware
caps := llama.DetectCapabilities()
optimal := llama.RecommendConfig(caps)

// Configure server
serverConfig := &llama.ServerConfig{
    ModelPath:    "/path/to/model.gguf",
    ContextSize:  optimal.ContextTier.Value(),
    GPULayers:    optimal.GPULayers,
    Port:         2748,
}

// Start server
srv := llama.NewServer(serverConfig)
srv.Start()

// Use server
response, err := srv.Complete(ctx, "Your prompt here", 100)
embeddings, err := srv.Embeddings(ctx, "Text to embed")

// Stop server
srv.Stop()
```

**Features**:
- ✅ Auto-detect GPU/CPU
- ✅ Context tier selection (32K/128K/256K)
- ✅ Model download with verification
- ✅ Warmup for optimal performance
- ✅ Health checks and ready detection

### `internal/profile/` - User Profiles

**Purpose**: Store and manage user preferences and statistics

**Key Files**:
- `profile.go` - Profile CRUD and statistics

**Usage**:
```go
mgr := profile.NewManager("~/.atcode")

// Get default profile
profile := profile.DefaultProfile("user-123", "user@example.com")

// Load existing profile
profile, err := mgr.Load("user-123")

// Update preference
profile.Preferences.Theme = "dark"
profile.Preferences.PermissionMode = "confirm"

// Save profile
err := mgr.Save(profile)
```

**Profile Structure**:
```
Profile
├── Preferences (theme, permissions, tools)
├── Settings (model params, penalties, limits)
├── ModelDefaults (quantization, GPU layers)
├── Stats (usage analytics)
└── Timestamps (created/updated)
```

### `internal/tips/` - Contextual Tips

**Purpose**: Provide smart suggestions based on context

**Key Files**:
- `tips.go` - Tips database and matching

**Usage**:
```go
tipsDB := tips.NewTipsDatabase()

// Get random tip
tip := tipsDB.SelectRandomTip()
fmt.Println(tip.Title)
fmt.Println(tip.Description)

// Find tips by keywords
userInput := "how do I read a file"
relevantTips := tipsDB.FindRelevantTips(userInput)
```

**Features**:
- ✅ Keyword-based matching
- ✅ Priority weighting
- ✅ Recency bias (don't repeat)
- ✅ 15+ built-in tips
- ✅ Expandable database

### `internal/theme/` - Visual Identity

**Purpose**: Provide consistent dark blue theme

**Key Files**:
- `dark_blue.go` - Color palette and styling

**Usage**:
```go
theme := theme.NewDarkBlueTheme()

// Use colors
fmt.Print(theme.Style(text, theme.HeaderStyle()))

// Access colors directly
fmt.Printf("Primary: %s\n", theme.Primary)
fmt.Printf("Accent: %s\n", theme.Accent)
```

**Colors**:
- Primary: #0051A5 (Dark Blue)
- Accent: #00D4FF (Cyan)
- Success: #10B981 (Green)
- Error: #EF4444 (Red)
- Background: #0D0D17 (Black)
- Text: #E6EDF3 (Off-white)

### `internal/init/` - System Initialization

**Purpose**: Initialize and wire all systems together

**Key Files**:
- `init.go` - Central initialization coordinator

**Usage**:
```go
// Initialize all systems at once
state, err := init.Initialize()

// Access any system
state.Config          // Configuration
state.AuthMgr         // Authentication
state.LlamaSrv        // atcode-server
state.Profile         // User profiles
state.Tips            // Tips database
state.Theme           // Theme system

// Start services
state.Start()

// Get status
status := state.Status()

// Clean shutdown
state.Stop()
```

## Integration Workflow

### 1. Basic Initialization

```go
package main

import (
    "context"
    snutinit "github.com/aistatine/sonut/internal/init"
)

func main() {
    // Initialize (config is pre-populated with embedded secrets)
    state, err := snutinit.Initialize()
    if err != nil {
        panic(err)
    }
    
    // Start services
    state.Start()
    defer state.Stop()
    
    // Now use any system
    // ...
}
```

### 2. Authentication Flow

```go
// Check if logged in
if state.AuthMgr.IsLoggedIn() {
    user := state.AuthMgr.GetCurrentUser()
    // Use authenticated user
} else {
    // Prompt for login
    // /auth login magic <email>
    // or
    // /auth login google
}
```

### 3. Model Inference

```go
// Ensure server is running
if err := state.LlamaSrv.Start(); err != nil {
    panic(err)
}

// Generate completion
response, err := state.LlamaSrv.Complete(ctx, "Hello, world!", 100)

// Get embeddings
embeddings, err := state.LlamaSrv.Embeddings(ctx, "Some text")
```

### 4. User Preferences

```go
// Load user profile
profile, err := state.Profile.Load(userID)

// Access preferences
if profile.Preferences.PermissionMode == "confirm" {
    // Ask user before file operations
}

// Update preferences
profile.Preferences.Theme = "dark"
state.Profile.Save(profile)
```

## CLI Integration

@code includes CLI command handlers in `internal/cli/sonut_init.go`:

```go
// Handle slash commands
handled, output, err := cli.Handle@codeCommand(state, "/auth login magic user@example.com")

// Supported commands:
// /auth login [magic|google] [email]
// /auth status
// /auth logout
// /auth verify <email> <otp>
// /profile show
// /profile edit <key>=<value>
// /tips
// /model status
// /status
```

## Security Considerations

1. **Embedded Credentials**: Supabase and Google OAuth credentials are embedded in code (safe for this use case since they're public keys)

2. **Token Storage**: Access tokens stored in `~/.atcode/auth.json` with mode 0600 (owner read/write only)

3. **PKCE OAuth**: Google OAuth uses PKCE flow to secure code exchange

4. **Sandbox Mode**: All tool execution defaults to sandboxed mode with user confirmation

5. **No Telemetry**: Telemetry is disabled by default and must be explicitly opted-in

## Example: Complete Application

```go
package main

import (
    "context"
    "fmt"
    snutinit "github.com/aistatine/sonut/internal/init"
)

func main() {
    // Initialize all systems
    state, err := snutinit.Initialize()
    if err != nil {
        fmt.Println("Error:", err)
        return
    }
    
    // Start services
    state.Start()
    defer state.Stop()
    
    // Show status
    fmt.Println("🌙 @code Status:")
    status := state.Status()
    for k, v := range status {
        fmt.Printf("%s: %v\n", k, v)
    }
    
    // Generate a completion
    ctx := context.Background()
    response, err := state.LlamaSrv.Complete(ctx, "Hello", 20)
    if err != nil {
        fmt.Println("Error:", err)
        return
    }
    
    fmt.Println("Response:", response)
}
```

## Testing

See `cmd/atcode-test/main.go` for a complete example program.

```bash
go build -o sonut-test ./cmd/atcode-test
./at-test
```

## Environment Overrides

All configuration can be overridden via environment variables:

```bash
export ATCODE_CONTEXT_SIZE=256k
export ATCODE_GPU_LAYERS=40
export ATCODE_LLAMA_PORT=9000
export ATCODE_MODEL_DIR=/custom/model/dir
./at
```

## Next Steps

1. **Review Architecture**: See `docs/ARCHITECTURE.md` for detailed design
2. **Setup Guide**: See `docs/SETUP.md` for installation steps
3. **Quick Start**: See `QUICKSTART.md` for first-time users
4. **Troubleshooting**: See `docs/TROUBLESHOOTING.md` for common issues

---

**@code is fully self-contained and production-ready. All credentials are embedded for ease of use.**
