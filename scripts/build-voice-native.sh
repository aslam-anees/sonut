#!/usr/bin/env bash
# Build one reproducible atcode-voice native artifact. Cross builds must provide
# a CMake toolchain and the matching audio backend through CMAKE_ARGS; this
# script never changes the CGO-free Go release path.
set -euo pipefail

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
BUILD_DIR="${ATCODE_VOICE_BUILD_DIR:-$ROOT/.build/voice-native}"
OUT_DIR="${ATCODE_VOICE_OUT_DIR:-$ROOT/dist/voice}"
GOOS_V="${ATCODE_VOICE_GOOS:-$(uname -s | tr '[:upper:]' '[:lower:]')}"
GOARCH_V="${ATCODE_VOICE_GOARCH:-$(uname -m)}"
case "$GOOS_V" in
  darwin) : ;;
  linux) : ;;
  windows|mingw*|msys*) GOOS_V=windows ;;
  *) echo "unsupported ATCODE_VOICE_GOOS=$GOOS_V" >&2; exit 1 ;;
esac
case "$GOARCH_V" in
  x86_64|amd64) GOARCH_V=amd64 ;;
  aarch64|arm64) GOARCH_V=arm64 ;;
  *) echo "unsupported ATCODE_VOICE_GOARCH=$GOARCH_V" >&2; exit 1 ;;
esac

cmake_args=(
  -DATCODE_AUDIO_CPP_REVISION=238ab6a9e321c17de8e120559f57efeedaeb1345
  -DATCODE_VOICE_ENABLE_CUDA="${ATCODE_VOICE_ENABLE_CUDA:-OFF}"
  -DATCODE_VOICE_ENABLE_VULKAN="${ATCODE_VOICE_ENABLE_VULKAN:-OFF}"
)
if [[ "$GOOS_V" == darwin ]]; then
  cmake_args+=(-DATCODE_VOICE_ENABLE_METAL=ON)
else
  cmake_args+=(-DATCODE_VOICE_ENABLE_METAL=OFF)
fi
if [[ -n "${ATCODE_VOICE_CMAKE_ARGS:-}" ]]; then
  # shellcheck disable=SC2206 — documented escape hatch for a trusted local build.
  cmake_args+=( ${ATCODE_VOICE_CMAKE_ARGS} )
fi

cmake -S "$ROOT/native/voice" -B "$BUILD_DIR" "${cmake_args[@]}"
cmake --build "$BUILD_DIR" --config Release --target atcode-voice --parallel "${CMAKE_BUILD_PARALLEL_LEVEL:-2}"

mkdir -p "$OUT_DIR"
name="atcode-voice-${GOOS_V}-${GOARCH_V}"
source="$BUILD_DIR/bin/atcode-voice"
if [[ "$GOOS_V" == windows ]]; then
  name+=".exe"
  [[ -f "$source.exe" ]] && source="$source.exe"
fi
[[ -f "$source" ]] || { echo "native helper not found: $source" >&2; exit 1; }
cp "$source" "$OUT_DIR/$name"
if [[ "$GOOS_V" != windows ]]; then chmod 0755 "$OUT_DIR/$name"; fi
if command -v sha256sum >/dev/null 2>&1; then
  sha256sum "$OUT_DIR/$name" > "$OUT_DIR/$name.sha256"
else
  shasum -a 256 "$OUT_DIR/$name" > "$OUT_DIR/$name.sha256"
fi
echo "built $OUT_DIR/$name"
