# @code Setup Guide

Get @code running on your machine in 5 minutes.

## Prerequisites

- **Go 1.26.4+** (for building from source)
- **atcode engine** (installed automatically)
- **RAM**: 8GB minimum (16GB recommended)
- **Disk**: 5GB for model files

## Installation

### Option 1: npm (Recommended)

```bash
npm install -g @aistatine/atcode
at
```

This downloads the @code binary for your platform from GitHub Releases.

### Option 2: Homebrew (macOS/Linux)

```bash
brew install aistatine/sonut/sonut
at
```

### Option 3: Linux/macOS Script

```bash
curl -fsSL https://raw.githubusercontent.com/aslam-anees/sonut/main/scripts/install.sh | bash
at
```

### Option 4: From Source

```bash
git clone https://github.com/aslam-anees/sonut.git
cd sonut
go build -o at ./cmd/atcode
./at
```

## First Run Setup

When you run `at` for the first time:

1. **Environment Check**
   - Detects your hardware (RAM, CPU, GPU)
   - Checks the atcode-server installation
   - Creates `~/.atcode/` directory

2. **Model Download** (if needed)
   - Downloads Qwen 3.5-4B model (~4.1GB)
   - Verifies SHA256 checksum
   - Saves to `~/.atcode/models/`

3. **Server Startup**
   - Launches atcode-server
   - Runs warmup (text + vision prefill)
   - Ready for your first prompt

4. **Authentication** (optional)
   - Run `/auth login magic <email>` to set up profiles
   - Or skip to use @code locally without account

## Configuration Files

After first run, you'll have:

```
~/.atcode/
├── config.json              # Device config (auto-detected)
├── profile_<user-id>.json   # Your preferences
├── auth.json                # Auth token (if logged in)
├── models/
│   ├── qwen2.5-coder-4b-q4_0.gguf    # Main model (4.1GB)
│   └── qwen2.5-coder-4b-mmproj.gguf  # Vision module (700MB)
└── sessions/
    ├── <session-id>.jsonl   # Conversation history
    └── ...
```

## Environment Variables

Optional configuration via environment variables:

```bash
# Context size (32k, 128k, 256k)
export ATCODE_CONTEXT_SIZE=256k

# GPU layers (0 = CPU only, 999 = max offload)
export ATCODE_GPU_LAYERS=40

# Model directory
export ATCODE_MODEL_DIR=~/.atcode/models

# Supabase (for profiles & tips)
export SUPABASE_URL=https://YOUR_PROJECT.supabase.co
export SUPABASE_ANON_KEY=eyJ...

# Google OAuth (for sign-in)
export GOOGLE_CLIENT_ID=...
export GOOGLE_CLIENT_SECRET=...

# Enable telemetry (disabled by default)
export ATCODE_TELEMETRY=1
```

## Quick Start Commands

### Interactive Mode
```bash
at
```
Launches the dark blue TUI. Try these first:

- Type your question and press Enter
- Type `/` for slash commands
- `/resume` — Pick up previous session
- `/tips` — See a contextual tip
- `/clear` — Start fresh
- `Ctrl+C` — Exit

### Batch Mode
```bash
at exec "list all .rs files in this repo"
at exec < turns.jsonl  # Process JSON turns
```

### Setup & Configuration
```bash
at setup              # Run setup wizard
at status            # Check model and server status
at doctor            # Diagnostics
at /profile show     # View your preferences
```

### Authentication
```bash
at /auth login magic your@email.com
at /auth status
at /auth logout
```

## Hardware-Specific Setup

### macOS (M1/M2/M3)

✅ Everything works out of the box.

- Metal GPU acceleration: automatic
- Startup time: 30-45 seconds
- Tokens/sec: 4-6 (M1 8GB), 8-12 (M2/M3 16GB+)

```bash
at  # Just run it
```

### Linux (NVIDIA GPU)

Requires CUDA toolkit. If you have NVIDIA GPU:

```bash
# Install CUDA toolkit first
curl https://developer.download.nvidia.com/compute/cuda/repos/.../cuda.repo
apt install cuda

# @code will auto-detect
at
```

If no CUDA, it falls back to CPU automatically.

### Linux (AMD GPU)

Requires ROCm. If you have AMD GPU:

```bash
# Install ROCm first
wget -q -O - https://repo.radeon.com/rocm/rocm.gpg.key | sudo apt-key add -

# @code will auto-detect
at
```

### Windows

Native support for:
- ✅ NVIDIA CUDA GPUs
- ✅ CPU (all versions)
- ⚠️ AMD GPUs (experimental)

```bash
# Download from GitHub Releases or npm
npm install -g @aistatine/atcode
at
```

## Troubleshooting

### "atcode-server not found"

**Problem**: Can't launch inference server

**Solution**: re-run the checksum-verifying @code installer:
```bash
curl -fsSL https://raw.githubusercontent.com/aslam-anees/sonut/src/scripts/install.sh | bash
```

### "Model file not found"

**Problem**: Qwen model didn't download

**Solution**:
```bash
# Re-run setup
at setup

# Or manually download
curl -L https://huggingface.co/Qwen/Qwen2.5-Coder-4B-Instruct-GGUF/resolve/main/qwen2.5-coder-4b-instruct-q4_0.gguf \
  -o ~/.atcode/models/qwen2.5-coder-4b-q4_0.gguf
```

### "OOM: Out of memory"

**Problem**: Model doesn't fit in RAM

**Solution**:
```bash
# @code auto-fallbacks to smaller context:
# 256K → 128K → 32K

# Or manually limit:
export ATCODE_CONTEXT_SIZE=32k
at
```

### "Slow inference" (< 1 tok/s)

**Problem**: Token generation is sluggish

**Causes & Solutions**:

1. **CPU-only fallback** (no GPU detected)
   - Check `at status` for GPU info
   - Install CUDA/ROCm if available

2. **CPU thermal throttling**
   - Give system a break between sessions
   - Try `/compact` to reduce context

3. **Disk is full**
   - Check disk space
   - Clear old sessions: `rm ~/.atcode/sessions/old-*.jsonl`

### "Auth not working"

**Problem**: `/auth login` fails

**Solution**:
```bash
# Check credentials
echo $SUPABASE_URL
test -n "$SUPABASE_ANON_KEY" && echo "Supabase key is configured"

# If not set, configure:
export SUPABASE_URL=https://YOUR_PROJECT.supabase.co
export SUPABASE_ANON_KEY=eyJ...

at /auth init
```

### "Can't connect to browser"

**Problem**: Vision tools fail (screenshots, page analysis)

**Solution**:
```bash
# Ensure a browser is running
# Chrome/Chromium: usually auto-detected
# Firefox: needs additional setup

# Or disable vision temporarily
at /profile edit vision=false
```

## Next Steps

1. **Read the guide**: `at /help`
2. **Explore tools**: Try `/file read`, `/bash ls -la`, `/browser screenshot`
3. **Review tips**: Run `/tips` to learn tricks
4. **Set up auth**: Optional but recommended for profiles
5. **Optimize**: Run `/model` to see performance stats

## Getting Help

- **In-chat help**: Type `/help` in @code TUI
- **Diagnostics**: Run `at doctor` for detailed system info
- **Issues**: GitHub: https://github.com/aslam-anees/sonut/issues
- **FAQ**: See docs/FAQ.md

## Uninstallation

```bash
# Via npm
npm uninstall -g @aistatine/atcode

# Via brew
brew uninstall sonut

# Manual cleanup
rm -rf ~/.atcode  # Remove config and models
```

---

**Enjoy using @code! 🌙**
