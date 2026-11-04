# @code Quick Start Guide

Welcome to @code! This guide will get you up and running in minutes.

## Prerequisites

- **Go 1.26.4+** (to build from source)
- **atcode engine** (installed automatically)
- **8GB+ RAM** (16GB recommended)
- **5GB disk space** for models

## Step 1: Clone and Build

```bash
git clone https://github.com/aslam-anees/sonut.git
cd sonut
go build -o at ./cmd/atcode
```

## Step 2: Environment Setup

Copy the example environment file:

```bash
cp .env.example .env
```

**Authentication is optional and requires your own configuration.** No credentials are embedded. Export values from your own project if you enable hosted authentication:

```bash
export SUPABASE_URL=https://YOUR_PROJECT.supabase.co
export SUPABASE_ANON_KEY=eyJhbGciOi...
export GOOGLE_CLIENT_ID=871183585190-...
export GOOGLE_CLIENT_SECRET=GOCSPX-...
```

## Step 3: First Run

```bash
./at
```

On first run:
1. System detects hardware capabilities
2. Downloads the atcode model pack (~4.1GB) to `~/.atcode/models/`
3. Starts the atcode engine on port 2748
4. Launches interactive TUI

## Step 4: Sign In (Optional)

In the TUI, run:

```
/auth login magic your@email.com
```

Then click the link in your email, or use Google OAuth:

```
/auth login google
```

## Available Commands

- `/auth login` - Sign in with email or Google
- `/auth status` - Show login status
- `/auth logout` - Sign out
- `/profile show` - View preferences
- `/tips` - Show a contextual tip
- `/model status` - Model and server info
- `/status` - Overall system status
- `/help` - Show all commands

## Troubleshooting

### "atcode-server not found"

```bash
# Re-run the platform installer; it installs and verifies atcode-server:
curl -fsSL https://raw.githubusercontent.com/aslam-anees/sonut/src/scripts/install.sh | bash
```

### "Out of Memory"

@code auto-fallbacks to smaller context:
- 256K → 128K → 32K

Or manually:
```bash
export ATCODE_CONTEXT_SIZE=32k
./at
```

### "Slow inference"

Check GPU is being used:
```bash
./at
/model status
# Look for "GPU layers: X/40"
```

If showing 0/40, GPU not detected. Install:
- **macOS**: Update to Big Sur+ (Metal auto-works)
- **Linux/NVIDIA**: Install CUDA toolkit
- **Linux/AMD**: Install ROCm

## Next Steps

1. **Review the architecture**: See `docs/ARCHITECTURE.md`
2. **Configure preferences**: `/profile edit theme=dark`
3. **Enable tools**: Tools like `/file`, `/bash`, `/browser` are built-in
4. **Explore**: Ask @code questions about your code!

## Documentation

- `docs/ARCHITECTURE.md` - System design
- `docs/SETUP.md` - Detailed installation guide
- `docs/TROUBLESHOOTING.md` - Common issues and solutions
- `.env.example` - All environment variables

## Key Features

✨ **Local-first**: 100% runs locally, no cloud required  
🔐 **Configurable authentication**: Bring your own credentials; legacy Supabase sessions are stored locally with owner-only file permissions
🌙 **Dark Blue Theme**: Beautiful terminal UI  
🧠 **Smart Tips**: Context-aware suggestions  
⚡ **Fast**: ~5-12 tok/s depending on hardware  
🛠️ **Full Tooling**: File, bash, browser, vision tools  

---

**Enjoy using @code! Questions? Check GitHub Issues.**
