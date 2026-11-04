#!/usr/bin/env bash
# ALHAMDULILLAH — @code production release script.
#
# Builds the `at`, `@`, and `@code` binaries plus the platform sandbox helpers
# for every supported platform, then stages them into the at-code distribution
# repo. A separately verified native voice helper is included only when supplied
# through ATCODE_VOICE_ARTIFACT_ROOT. install/install.sh serves these artifacts
# directly from the `src` branch.
#
# Usage:
#   scripts/release-atcode.sh <version> [path-to-at-code-repo]
# Example:
#   scripts/release-atcode.sh 0.1.2 ../at-code
set -euo pipefail

# Native voice helpers are built separately with native/voice/CMakeLists.txt.
# Point this script at the reproducible artifact directory when publishing;
# leaving it unset keeps the existing CGO-free Go release path intact.
ATCODE_VOICE_ARTIFACT_ROOT="${ATCODE_VOICE_ARTIFACT_ROOT:-}"

VERSION="${1:?usage: release-atcode.sh <version> [at-code-repo]}"
ATCODE_REPO="${2:-$(dirname "$0")/../../at-code}"
ATCODE_SOURCE_ROOT="$(cd "$(dirname "$0")/.." && pwd)"
ATCODE_REPO="$(cd "$ATCODE_REPO" && pwd)"

[ -d "$ATCODE_REPO/bin" ] || { echo "error: $ATCODE_REPO/bin not found" >&2; exit 1; }

LDFLAGS="-s -w -X github.com/aistatine/sonut/internal/cli.version=$VERSION"

# os/arch pairs matching at-code/install/install.sh's expected asset names.
targets=(
  "darwin arm64"
  "darwin amd64"
  "linux amd64"
  "linux arm64"
  "windows amd64"
  "windows arm64"
)

echo "==> Building @code v$VERSION"
for t in "${targets[@]}"; do
  set -- $t
  GOOS_V="$1" GOARCH_V="$2"
  out="$ATCODE_REPO/bin/at-$GOOS_V-$GOARCH_V"
  alias_out="$ATCODE_REPO/bin/@-$GOOS_V-$GOARCH_V"
  if [ "$GOOS_V" = "windows" ]; then
    out="$out.exe"
    alias_out="$alias_out.exe"
  fi
  code_alias_out="$ATCODE_REPO/bin/@code-$GOOS_V-$GOARCH_V"
  if [ "$GOOS_V" = "windows" ]; then
    code_alias_out="$code_alias_out.exe"
  fi
  echo "    $GOOS_V/$GOARCH_V -> $out, $alias_out, $code_alias_out"
  (cd "$ATCODE_SOURCE_ROOT" && CGO_ENABLED=0 GOOS="$GOOS_V" GOARCH="$GOARCH_V" \
    go build -trimpath -ldflags "$LDFLAGS" -o "$out" ./cmd/atcode)
  cp "$out" "$alias_out"
  cp "$out" "$code_alias_out"

  case "$GOOS_V" in
    linux)
      for helper in atcode-linux-sandbox atcode-seccomp; do
        helper_out="$ATCODE_REPO/bin/${helper}-${GOOS_V}-${GOARCH_V}"
        echo "      helper -> $helper_out"
        (cd "$ATCODE_SOURCE_ROOT" && CGO_ENABLED=0 GOOS="$GOOS_V" GOARCH="$GOARCH_V" \
          go build -trimpath -ldflags "-s -w" -o "$helper_out" "./cmd/$helper")
      done
      ;;
    windows)
      for helper in atcode-windows-command-runner atcode-windows-sandbox-setup; do
        helper_out="$ATCODE_REPO/bin/${helper}-${GOOS_V}-${GOARCH_V}.exe"
        echo "      helper -> $helper_out"
        (cd "$ATCODE_SOURCE_ROOT" && CGO_ENABLED=0 GOOS="$GOOS_V" GOARCH="$GOARCH_V" \
          go build -trimpath -ldflags "-s -w" -o "$helper_out" "./cmd/$helper")
      done
      ;;
  esac

  if [ -n "$ATCODE_VOICE_ARTIFACT_ROOT" ]; then
    voice_name="atcode-voice-${GOOS_V}-${GOARCH_V}"
    if [ "$GOOS_V" = "windows" ]; then voice_name="$voice_name.exe"; fi
    voice_source="$ATCODE_VOICE_ARTIFACT_ROOT/$voice_name"
    if [ -f "$voice_source" ]; then
      voice_target="$ATCODE_REPO/bin/$voice_name"
      echo "      voice helper -> $voice_target"
      cp "$voice_source" "$voice_target"
    else
      echo "      voice helper missing (expected $voice_source); text-only artifact retained" >&2
      rm -f "$ATCODE_REPO/bin/$voice_name"
    fi
  else
    rm -f "$ATCODE_REPO/bin/atcode-voice-${GOOS_V}-${GOARCH_V}" \
      "$ATCODE_REPO/bin/atcode-voice-${GOOS_V}-${GOARCH_V}.exe"
  fi
done

(
  cd "$ATCODE_REPO/bin"
  if command -v sha256sum >/dev/null 2>&1; then
    find . -maxdepth 1 -type f ! -name SHA256SUMS -print |
      sed 's#^\./##' | LC_ALL=C sort | xargs sha256sum > SHA256SUMS
  elif command -v shasum >/dev/null 2>&1; then
    find . -maxdepth 1 -type f ! -name SHA256SUMS -print |
      sed 's#^\./##' | LC_ALL=C sort | xargs shasum -a 256 > SHA256SUMS
  else
    echo "error: sha256sum or shasum is required" >&2
    exit 1
  fi
)

printf '%s\n' "$VERSION" > "$ATCODE_REPO/install/VERSION"
echo "==> Staged binaries, helpers, checksums, and VERSION=$VERSION in $ATCODE_REPO"
echo "==> Next: cd $ATCODE_REPO && git add -A && git commit -m 'release: v$VERSION' && git push origin src"
