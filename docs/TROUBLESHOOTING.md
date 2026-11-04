# @code Troubleshooting Guide

Common issues and solutions.

## Startup Issues

### "Failed to detect hardware"

**Symptom**: @code exits with hardware detection error

**Causes**:
- Running in restricted environment (container, VM)
- Permissions issue on /proc (Linux)
- Broken system calls

**Solutions**:
```bash
# Check system info
at doctor

# Force context size
export ATCODE_CONTEXT_SIZE=32k
at

# Try with CPU only
export ATCODE_GPU_LAYERS=0
at
```

### "Port already in use"

**Symptom**: `Address already in use :2748` or `Address already in use :8000`

**Cause**: Another atcode-server or reverse proxy already running (internal port 42611, public proxy on 2748)

**Solutions**:
```bash
# Find and kill existing server
ps aux | grep atcode-server
kill <pid>

# Or kill by port (2748 is public, 8000 is internal)
lsof -ti :2748 | xargs kill -9
lsof -ti :8000 | xargs kill -9

# Or use different port (set ATCODE_LLAMA_PORT=<custom_port>)
export ATCODE_LLAMA_PORT=8001
at
```

### "Model initialization timeout"

**Symptom**: "atcode-server startup timeout" after 60 seconds

**Causes**:
- Slow disk (can take >60s on HDD)
- Model file corrupted
- System under heavy load

**Solutions**:
```bash
# Increase timeout (via code or wait longer)
# Re-download model
rm ~/.atcode/models/*.gguf
at setup

# Check disk health
diskutil verifyDisk /  # macOS
fsck /dev/sda1         # Linux

# Reduce load
killall other-apps
at
```

## Inference Issues

### "No tokens generated"

**Symptom**: Response is empty or just "[end of text]"

**Causes**:
- Model not warmed up
- Invalid prompt
- KV cache issue

**Solutions**:
```bash
# Check model status
at /model status

# Try simpler prompt
at exec "hello"

# Restart and force warmup
at setup
at
```

### "Tokens per second too slow"

**Symptom**: < 1 tok/s (very sluggish)

**Causes**:
1. Running on CPU when GPU available
2. Wrong quantization format
3. System thermal throttling
4. Disk bottleneck

**Solutions**:
```bash
# Check if GPU is used
at /model status
# Look for "GPU layers: X/40"

# If 0/40, GPU not detected
export ATCODE_GPU_LAYERS=40
at

# For M1/M2 (Metal):
# Should auto-work. If not:
at doctor | grep Metal

# Reduce context to speed up
/compact
# Or:
export ATCODE_CONTEXT_SIZE=32k
at

# Monitor system
top  # macOS/Linux
```

### "Context overflow (token limit exceeded)"

**Symptom**: "Context size exceeded, please run /compact"

**Causes**:
- Long conversation with lots of code
- Large files added to context
- User kept adding more turns

**Solutions**:
```bash
# Compress conversation
/compact

# If compact fails, fallback happens automatically:
# 256K → 128K → 32K

# Check context usage
/model  # Shows token count

# Clear old sessions if persistent
rm ~/.atcode/sessions/old-*.jsonl
```

## Tool Issues

### "File tool: permission denied"

**Symptom**: Can't read/write files outside workspace

**Causes**:
- Sandbox mode active (default)
- File permissions issue

**Solutions**:
```bash
# Check permission mode
/profile show  # Look for "Permission Mode"

# Switch to confirm mode (safer than unrestricted)
/profile edit permission_mode=confirm

# Or explicitly approve in prompt:
# "Read /etc/passwd" → @code asks, you confirm
```

### "Bash tool: command not found"

**Symptom**: "command: not found" or "permission denied"

**Causes**:
- Command not installed
- Not in PATH
- Sandbox blocked it

**Solutions**:
```bash
# Check if command exists
bash -c "which <command>"

# Try full path
# Instead of "ls", use "/bin/ls"

# Check sandbox list
/profile show  # custom_bash_allow_list

# Whitelist command
/profile edit custom_bash_allow_list="curl,wget,git"
```

### "Vision/screenshot not working"

**Symptom**: "browser screenshot requires browser tool integration"

**Causes**:
- Browser tool not connected
- Vision disabled in profile

**Solutions**:
```bash
# Check if browser available
at doctor | grep -i browser

# Enable vision in profile
/profile edit vision=true

# Restart
at
```

### "MCP server not responding"

**Symptom**: "MCP server timeout" during startup or tool discovery

**Causes**:
- MCP server crashed
- Network disconnected
- Server misconfigured

**Solutions**:
```bash
# List active MCP servers
at mcp list

# Verify the server and inspect its discovered tools
at mcp check <server-name>
at mcp tools list

# Mute problematic server
at mcp disable <server-name>
```

MCP servers are started on demand by the current command/session; there is no
separate MCP daemon or `at mcp restart` command. For a local stdio server,
`at mcp check` exercises a fresh process. The built-in Playwright server gets a
longer first-run bootstrap window because `npx` may need to download it.

## Authentication Issues

### "/auth login fails with 'network error'"

**Symptom**: "SignInWithMagicLink: network error"

**Causes**:
- Internet disconnected
- Supabase endpoint unreachable
- Firewall blocking HTTPS

**Solutions**:
```bash
# Check internet
ping supabase.co

# Check credentials
echo $SUPABASE_URL
# Should output: https://YOUR_PROJECT.supabase.co

# Try magic link instead of OAuth
/auth login magic your@email.com

# Verify Supabase is running
curl -I https://YOUR_PROJECT.supabase.co
```

### "Magic link not arriving in email"

**Symptom**: No email after 5+ minutes

**Causes**:
- Email in spam folder
- Typo in email address
- Supabase email service down

**Solutions**:
```bash
# Check spam folder
# Check for typos
/auth login magic correct@email.com

# Verify email address format
# Re-try (rate limited to 1/minute)

# Check Supabase status
curl https://supabase.co/status
```

### "OTP verification fails"

**Symptom**: "OTP verification failed" after clicking email link

**Causes**:
- OTP expired (valid for 10 minutes)
- Wrong OTP entered
- Account doesn't exist

**Solutions**:
```bash
# Request new magic link (old one expired)
/auth logout
/auth login magic your@email.com

# Check OTP from email carefully
# Enter exactly: /auth verify your@email.com <otp>

# If still fails, check Supabase logs
# On supabase.co → Auth tab
```

### "Token refresh fails"

**Symptom**: Session expires, refresh fails

**Causes**:
- Refresh token expired (30-day expiry)
- Supabase service down
- Local token corrupted

**Solutions**:
```bash
# Re-login
/auth logout
/auth login magic your@email.com

# Check token file
cat ~/.atcode/auth.json  # Should be valid JSON

# Restore from backup (if exists)
cp ~/.atcode/auth.json.bak ~/.atcode/auth.json
```

## Performance Issues

### "TUI is sluggish / input lag"

**Symptom**: Keyboard input delayed, rendering slow

**Causes**:
- Terminal emulator inefficient
- @code rendering too much output
- System low on resources

**Solutions**:
```bash
# Use better terminal
# macOS: iTerm2 or kitty
# Linux: alacritty or kitty
# Windows: Windows Terminal or cmder

# Reduce output verbosity
/profile edit verbose_output=false

# Clear large scrollback
at /clear

# Monitor system
top  # Check CPU/memory
```

### "Model warmup takes too long"

**Symptom**: "Model startup" takes > 60 seconds

**Causes**:
- Slow disk (HDD)
- First-time model load (unfamiliar code paths)
- System load high

**Solutions**:
```bash
# SSD recommended but not required
# HDD slowdown is normal, wait it out

# Upgrade to SSD if persistent bottleneck

# Pre-warm model (one-time)
# Just run @code and let it sit 60-90 seconds
# Subsequent runs cache optimizations

# Reduce load
killall other-apps
at
```

## Storage Issues

### "Out of disk space"

**Symptom**: "No space left on device"

**Causes**:
- Model files very large (~5GB total)
- Session history accumulating
- System disk full

**Solutions**:
```bash
# Check disk usage
df -h  # macOS/Linux
# Windows: Disk Management

# Remove old sessions
ls -la ~/.atcode/sessions/
rm ~/.atcode/sessions/old-*.jsonl  # Keep recent

# Compress sessions
gzip ~/.atcode/sessions/*.jsonl  # Optional

# Move to external drive
mkdir /Volumes/External/.atcode
mv ~/.atcode /Volumes/External/
ln -s /Volumes/External/.atcode ~/.atcode

# Clear caches
rm ~/.atcode/atcode_cache_*
```

### "Model file corrupted"

**Symptom**: "Failed to load model" or weird inference output

**Causes**:
- Download interrupted
- SHA256 verification failed
- Disk error

**Solutions**:
```bash
# Delete and re-download
rm ~/.atcode/models/*.gguf
at setup

# Verify integrity
sha256sum ~/.atcode/models/*.gguf
# Compare with known hashes

# Check disk health
diskutil verifyDisk /  # macOS
```

## Cross-Platform Issues

### Windows: "Command not found"

**Symptom**: Bash tool fails to find Windows commands

**Causes**:
- PowerShell vs cmd.exe differences
- Path separators (\\ vs /)
- Environment variables not set

**Solutions**:
```bash
# Use full paths
# Instead of "python", use "C:\Python39\python.exe"

# Use bash-compatible tools
# Install Git Bash or WSL

# For WSL:
# Everything should work as on Linux
```

### Linux: "GPU not detected"

**Symptom**: Always shows "GPU layers: 0/40"

**Causes**:
- CUDA/ROCm not installed
- NVIDIA drivers missing
- GPU disabled in BIOS

**Solutions**:
```bash
# Check GPU visibility
nvidia-smi  # NVIDIA
rocm-smi    # AMD

# Install drivers
# NVIDIA: https://developer.nvidia.com/cuda-toolkit
# AMD: https://rocmdocs.amd.com/

# Check BIOS
# Restart, enter BIOS, enable GPU/Integrated Graphics
```

### macOS: "Metal not working"

**Symptom**: M1/M2 Mac doesn't use Metal acceleration

**Causes**:
- Older macOS version (need 11.x+)
- Rosetta 2 translation layer issue

**Solutions**:
```bash
# Update macOS
# Apple menu → System Preferences → Software Update

# Re-install @code (native arm64 binary)
# Don't run under Rosetta 2
```

## Getting Help

If these solutions don't work:

1. **Run diagnostics**
   ```bash
   at doctor > diagnostics.txt
   ```

2. **Share with support**
   - Include diagnostics.txt
   - Describe what you did
   - Include error messages

3. **Check logs**
   ```bash
   tail -f ~/.atcode/*.log
   ```

4. **Report issue**
   - GitHub: https://github.com/aslam-anees/sonut/issues
   - Include: OS, Go version, hardware, error message

## Known Limitations

| Issue | When | Fix |
|-------|------|-----|
| Context tier fallback slow | On OOM | Auto-retries in 30s |
| Browser screenshots fail | First time | Browser startup takes 5-10s |
| Magic links slow (30s+) | Network lag | Rate-limited by Supabase |
| GPU memory leak | Long sessions | Restart periodically |
| Model warmup cached poorly | After update | Clear ~/.atcode/atcode_cache_* |

---

**Still stuck? Ask in GitHub Discussions or open an issue!**
