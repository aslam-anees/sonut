# Installing @code

@code is distributed as:

- an npm package, `@aistatine/atcode`
- release archives on GitHub Releases
- source builds with Go 1.26.4+

The npm package and install scripts download a platform-specific release archive.
They require a published GitHub Release for the requested version.

## npm

```bash
npm install -g @aistatine/atcode
at
```

The package supports Linux, macOS, and Windows on x64 and arm64. It installs the
`at` command and downloads the matching release binary during `postinstall`.

Requirements:

- Node.js 18+
- network access to npm and GitHub Releases

## Linux And macOS Script

Install the latest release:

```bash
curl -fsSL https://raw.githubusercontent.com/aslam-anees/sonut/main/scripts/install.sh | bash
```

From a checkout:

```bash
scripts/install.sh
```

Install a specific version:

```bash
ATCODE_VERSION=0.1.0 scripts/install.sh
scripts/install.sh --version 0.1.0
```

Install somewhere else:

```bash
ATCODE_INSTALL_DIR="$HOME/bin" scripts/install.sh
scripts/install.sh --install-dir "$HOME/bin"
```

Defaults:

- Repository: `aistatine/sonut`
- Version: latest GitHub release
- Install path: `~/.atcode/bin/at` (with `atcode` and legacy `sonut` aliases)
- Engine path: `~/.atcode/engine/atcode-server`

Requirements: Bash, `curl` or `wget`, `tar`, and `shasum` or `sha256sum`.

## Windows PowerShell Script

Install the latest release:

```powershell
irm https://raw.githubusercontent.com/aslam-anees/sonut/main/scripts/install.ps1 | iex
```

From a checkout:

```powershell
powershell -ExecutionPolicy Bypass -File scripts/install.ps1
```

Install a specific version:

```powershell
powershell -ExecutionPolicy Bypass -File scripts/install.ps1 -Version 0.1.0
```

Install somewhere else:

```powershell
powershell -ExecutionPolicy Bypass -File scripts/install.ps1 -InstallDir "$env:USERPROFILE\bin"
```

Defaults:

- Repository: `aistatine/sonut`
- Version: latest GitHub release
- Install path: `%USERPROFILE%\.atcode\bin\at.exe` (with `atcode.exe` and a legacy `sonut.exe` alias)
- Engine path: `%USERPROFILE%\.atcode\engine\atcode-server.exe`

## From Source

```bash
git clone https://github.com/aslam-anees/sonut.git
cd sonut
go run ./cmd/atcode
```

Build a local binary:

```bash
go build -o at ./cmd/atcode
```

Source builds require Go 1.26.4+.

### Sandbox Helpers For Source Builds

Release archives include the platform sandbox helpers. If you build directly
from source, build the helpers you need:

Linux:

```bash
go build -o at ./cmd/atcode
go build -o atcode-linux-sandbox ./cmd/atcode-linux-sandbox
go build -o atcode-seccomp ./cmd/atcode-seccomp
```

Put `at` and `atcode-linux-sandbox` in the same directory on `PATH`, for example
`~/.atcode/bin`. `atcode-seccomp` is kept as a compatibility wrapper; the sandbox
helper applies the Unix-socket filter itself when that sandbox option is enabled.
Linux native sandboxing also requires Bubblewrap to be installed.

### Optional Linux eBPF sensor

The runtime sensor is separate from the agent and is off by default. Build the
minimal helper alongside `at`:

```bash
go build -o atcode-sensor ./cmd/atcode-sensor
```

Place `atcode-sensor` on `PATH` (or set `ATCODE_SENSOR_HELPER` to an absolute
path). From a human-controlled @code session, use `/sec sensor status` and
`/sec sensor start`. The helper uses the platform privilege flow, scopes its
cgroup connect hook to the @code process tree when cgroup v2 is available, and
falls back to `detect-only` or `unavailable` when kernel capabilities are
missing. Stop it with `/sec sensor stop`; closing the helper detaches its BPF
links. It captures sanitized destination metadata only—never packet payloads
or decrypted HTTPS content. The agent cannot start, stop, or reconfigure it
through `exec_command`.

macOS uses the system sandbox and does not need an extra helper binary.

Windows source builds can use the main `at.exe` as the command runner and setup
helper through @code's built-in self-dispatch path. If you want a release-style
layout anyway, build the standalone helper executables next to `at.exe`:

```powershell
go build -o at.exe ./cmd/atcode
go build -o atcode-windows-command-runner.exe ./cmd/atcode-windows-command-runner
go build -o atcode-windows-sandbox-setup.exe ./cmd/atcode-windows-sandbox-setup
```

## Release Archive Format

Release archives are named:

- `atcode-v<version>-linux-<arch>.tar.gz`
- `atcode-v<version>-macos-<arch>.tar.gz`
- `atcode-v<version>-windows-<arch>.zip`

Supported targets:

- `linux-x64`
- `linux-arm64`
- `macos-x64`
- `macos-arm64`
- `windows-x64`
- `windows-arm64`

Each archive must have a matching `.sha256` file. The install scripts download
both files, verify the checksum, and then copy the binary into the install
directory.

## Updating

Check for a newer release:

```bash
at update --check
```

Then reinstall with npm or rerun the install script for the version you want.
