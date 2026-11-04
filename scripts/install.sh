#!/usr/bin/env bash
set -euo pipefail

ATCODE_REPO="${ATCODE_REPO:-${SONUT_REPO:-aslam-anees/sonut}}"
ATCODE_VERSION="${ATCODE_VERSION:-${SONUT_VERSION:-latest}}"
ATCODE_INSTALL_DIR="${ATCODE_INSTALL_DIR:-${SONUT_INSTALL_DIR:-$HOME/.atcode/bin}}"
ATCODE_ENGINE_DIR="${ATCODE_ENGINE_DIR:-$HOME/.atcode/engine}"
ATCODE_GITHUB_API="${ATCODE_GITHUB_API:-${SONUT_GITHUB_API:-https://api.github.com}}"
ATCODE_GITHUB_BASE_URL="${ATCODE_GITHUB_BASE_URL:-${SONUT_GITHUB_BASE_URL:-https://github.com}}"
ATCODE_ENGINE_REPO="${ATCODE_ENGINE_REPO:-ggml-org/llama.cpp}"

usage() {
  cat <<'EOF'
Install @code from GitHub Releases.

Usage:
  scripts/install.sh [--version <version>] [--repo <owner/repo>] [--install-dir <path>]

Environment:
  ATCODE_VERSION          Release version or tag. Defaults to latest.
  ATCODE_REPO             GitHub repository. Defaults to aslam-anees/sonut.
  ATCODE_INSTALL_DIR      Directory for the at binary. Defaults to ~/.atcode/bin.
  ATCODE_ENGINE_DIR       Private engine directory. Defaults to ~/.atcode/engine.
  ATCODE_GITHUB_API       GitHub API base URL. Defaults to https://api.github.com.
  ATCODE_GITHUB_BASE_URL  GitHub web base URL. Defaults to https://github.com.
EOF
}

fail() {
  echo "@code install: $*" >&2
  exit 1
}

while [ "$#" -gt 0 ]; do
  case "$1" in
    --version)
      [ "$#" -ge 2 ] || fail "--version requires a value"
      ATCODE_VERSION="$2"
      shift 2
      ;;
    --repo)
      [ "$#" -ge 2 ] || fail "--repo requires a value"
      ATCODE_REPO="$2"
      shift 2
      ;;
    --install-dir)
      [ "$#" -ge 2 ] || fail "--install-dir requires a value"
      ATCODE_INSTALL_DIR="$2"
      shift 2
      ;;
    -h|--help)
      usage
      exit 0
      ;;
    *)
      fail "unknown argument: $1"
      ;;
  esac
done

need_command() {
  command -v "$1" >/dev/null 2>&1 || fail "$1 is required"
}

download() {
  local url="$1"
  local output="$2"

  if command -v curl >/dev/null 2>&1; then
    curl --fail --location --show-error --silent "$url" --output "$output"
  elif command -v wget >/dev/null 2>&1; then
    wget --quiet "$url" --output-document "$output"
  else
    fail "curl or wget is required"
  fi
}

download_json() {
  local url="$1"
  local output="$2"

  if command -v curl >/dev/null 2>&1; then
    curl --fail --location --show-error --silent --header 'Accept: application/vnd.github+json' "$url" --output "$output"
  elif command -v wget >/dev/null 2>&1; then
    wget --quiet --header='Accept: application/vnd.github+json' "$url" --output-document "$output"
  else
    fail "curl or wget is required"
  fi
}

detect_platform() {
  case "$(uname -s)" in
    Linux) echo "linux" ;;
    Darwin) echo "macos" ;;
    *) fail "unsupported platform: $(uname -s)" ;;
  esac
}

detect_arch() {
  case "$(uname -m)" in
    x86_64|amd64) echo "x64" ;;
    arm64|aarch64) echo "arm64" ;;
    *) fail "unsupported architecture: $(uname -m)" ;;
  esac
}

latest_tag() {
  local metadata_file="$1"
  local api_url="${ATCODE_GITHUB_API%/}/repos/${ATCODE_REPO}/releases/latest"
  local tag

  download_json "$api_url" "$metadata_file"
  tag="$(sed -n 's/.*"tag_name"[[:space:]]*:[[:space:]]*"\([^"]*\)".*/\1/p' "$metadata_file" | head -n 1)"
  [ -n "$tag" ] || fail "could not read tag_name from $api_url"
  echo "$tag"
}

verify_checksum() {
  local checksum_file="$1"

  if command -v shasum >/dev/null 2>&1; then
    shasum -a 256 -c "$checksum_file"
  elif command -v sha256sum >/dev/null 2>&1; then
    sha256sum -c "$checksum_file"
  else
    fail "shasum or sha256sum is required"
  fi
}

find_extracted_entry() {
  local root="$1"
  local name="$2"
  local kind="$3"
  local candidate

  if [ "$kind" = "dir" ] && [ -d "$root/$name" ]; then
    echo "$root/$name"
    return 0
  fi
  if [ "$kind" = "file" ] && [ -f "$root/$name" ]; then
    echo "$root/$name"
    return 0
  fi

  for candidate in "$root"/*/"$name"; do
    if [ "$kind" = "dir" ] && [ -d "$candidate" ]; then
      echo "$candidate"
      return 0
    fi
    if [ "$kind" = "file" ] && [ -f "$candidate" ]; then
      echo "$candidate"
      return 0
    fi
  done

  return 1
}

find_extracted_binary() {
  find_extracted_entry "$1" "at" "file" ||
    find_extracted_entry "$1" "sonut" "file"
}

latest_asset_url() {
  local repository="$1"
  local pattern="$2"
  local metadata_file="$3"

  download_json "${ATCODE_GITHUB_API%/}/repos/${repository}/releases/latest" "$metadata_file"
  sed -n 's/.*"browser_download_url"[[:space:]]*:[[:space:]]*"\([^"]*\)".*/\1/p' "$metadata_file" |
    grep -E -m1 "$pattern" || true
}

migrate_engine_names() {
  local legacy_root="$HOME/.atcode/prism-llama.cpp"
  local legacy_bin="$HOME/.atcode/bin/llama-server"

  if [ -d "$legacy_root" ]; then
    local migrated_root="$ATCODE_ENGINE_DIR"
    if [ -e "$migrated_root" ]; then
      migrated_root="$ATCODE_ENGINE_DIR/compat-runtime"
      local index=2
      while [ -e "$migrated_root" ]; do
        migrated_root="$ATCODE_ENGINE_DIR/compat-runtime-$index"
        index=$((index + 1))
      done
    fi
    mkdir -p "$(dirname "$migrated_root")"
    mv "$legacy_root" "$migrated_root"
  fi

  local old_paths=("$legacy_bin")
  while IFS= read -r old_path; do
    old_paths+=("$old_path")
  done < <(find "$ATCODE_ENGINE_DIR" -type f -name 'llama-server' 2>/dev/null || true)
  for old_path in "${old_paths[@]}"; do
    if [ -f "$old_path" ]; then
      new_path="$(dirname "$old_path")/atcode-server"
      local index=2
      while [ -e "$new_path" ]; do
        new_path="$(dirname "$old_path")/atcode-server-compat-$index"
        index=$((index + 1))
      done
      mv "$old_path" "$new_path"
    fi
  done
}

install_engine() {
  local canonical_server="$ATCODE_ENGINE_DIR/atcode-server"
  if [ -x "$canonical_server" ]; then
    echo "Verified $canonical_server"
    return
  fi

  local package_server
  if package_server="$(find_extracted_entry "$extract_dir" "atcode-server" "file")"; then
    mkdir -p "$ATCODE_ENGINE_DIR"
    cp "$package_server" "$canonical_server"
    chmod 755 "$canonical_server"
    echo "Installed $canonical_server"
    return
  fi

  local engine_pattern
  case "${platform}-${arch}" in
    macos-arm64) engine_pattern='bin-macos-arm64\.tar\.gz$' ;;
    macos-x64) engine_pattern='bin-macos-x64\.tar\.gz$' ;;
    linux-arm64) engine_pattern='bin-ubuntu-arm64\.tar\.gz$' ;;
    linux-x64) engine_pattern='bin-ubuntu-x64\.tar\.gz$' ;;
    *) fail "no @code engine build is available for ${platform}-${arch}" ;;
  esac

  local engine_url
  engine_url="$(latest_asset_url "$ATCODE_ENGINE_REPO" "$engine_pattern" "$tmp_dir/engine-release.json")"
  [ -n "$engine_url" ] || fail "could not resolve an @code engine build for ${platform}-${arch}"

  local engine_archive
  case "$engine_url" in
    *.tar.gz) engine_archive="$tmp_dir/atcode-engine.tar.gz" ;;
    *.zip) engine_archive="$tmp_dir/atcode-engine.zip" ;;
    *) fail "the @code engine archive format is unsupported" ;;
  esac
  local engine_extract="$tmp_dir/engine-extract"
  download "$engine_url" "$engine_archive"
  mkdir -p "$engine_extract"
  case "$engine_archive" in
    *.tar.gz)
      tar -xzf "$engine_archive" -C "$engine_extract"
      ;;
    *.zip)
      if command -v unzip >/dev/null 2>&1; then
        unzip -q "$engine_archive" -d "$engine_extract"
      else
        tar -xf "$engine_archive" -C "$engine_extract" ||
          fail "unzip or a zip-capable tar is required to install the @code engine"
      fi
      ;;
    *)
      fail "unzip or a zip-capable tar is required to install the @code engine"
      ;;
  esac

  local source_server
  source_server="$(find "$engine_extract" -type f -name 'llama-server' -print -quit)"
  [ -n "$source_server" ] || fail "the downloaded @code engine package was incomplete"
  mkdir -p "$ATCODE_ENGINE_DIR"
  cp "$source_server" "$canonical_server"
  chmod 755 "$canonical_server"

  # Keep only runtime libraries required beside the executable; do not expose
  # supplier CLI/tool executable names in ~/.atcode.
  find "$(dirname "$source_server")" -maxdepth 1 \( -type f -o -type l \) \
    \( -name '*.so' -o -name '*.so.*' -o -name '*.dylib' \) \
    -exec cp -P {} "$ATCODE_ENGINE_DIR/" \;
  echo "Installed $canonical_server"
}

setup_path() {
  case ":$PATH:" in
    *":$ATCODE_INSTALL_DIR:"*) return ;;
  esac
  if [ "${ATCODE_NO_PATH_UPDATE:-0}" = "1" ]; then
    echo "Add $ATCODE_INSTALL_DIR to PATH to run at from any directory."
    return
  fi

  local shell_rc
  case "$(basename "${SHELL:-}")" in
    zsh) shell_rc="$HOME/.zshrc" ;;
    bash) shell_rc="$HOME/.bashrc" ;;
    *) shell_rc="$HOME/.profile" ;;
  esac
  local path_line="export PATH=\"$ATCODE_INSTALL_DIR:\$PATH\""
  if [ ! -f "$shell_rc" ] || ! grep -Fqx "$path_line" "$shell_rc"; then
    {
      echo
      echo "# Added by @code installer"
      echo "$path_line"
    } >>"$shell_rc"
  fi
  export PATH="$ATCODE_INSTALL_DIR:$PATH"
  echo "Added $ATCODE_INSTALL_DIR to PATH in $shell_rc"
}

copy_optional_file() {
  local name="$1"
  local source_path

  if source_path="$(find_extracted_entry "$extract_dir" "$name" "file")"; then
    cp "$source_path" "$ATCODE_INSTALL_DIR/$name"
    chmod 755 "$ATCODE_INSTALL_DIR/$name"
  fi
}

copy_optional_dir() {
  local name="$1"
  local source_path

  if source_path="$(find_extracted_entry "$extract_dir" "$name" "dir")"; then
    rm -rf "$ATCODE_INSTALL_DIR/$name"
    cp -R "$source_path" "$ATCODE_INSTALL_DIR/$name"
  fi
}

need_command uname
need_command sed
need_command tar
need_command mktemp

tmp_dir="$(mktemp -d "${TMPDIR:-/tmp}/atcode-install.XXXXXX")"
cleanup() {
  rm -rf "$tmp_dir"
}
trap cleanup EXIT

if [ "$ATCODE_VERSION" = "latest" ]; then
  tag="$(latest_tag "$tmp_dir/latest.json")"
else
  case "$ATCODE_VERSION" in
    v*) tag="$ATCODE_VERSION" ;;
    *) tag="v$ATCODE_VERSION" ;;
  esac
fi

version="${tag#v}"
platform="$(detect_platform)"
arch="$(detect_arch)"
archive_name="atcode-v${version}-${platform}-${arch}.tar.gz"
checksum_name="${archive_name}.sha256"
release_url="${ATCODE_GITHUB_BASE_URL%/}/${ATCODE_REPO}/releases/download/${tag}"
archive_path="$tmp_dir/$archive_name"
checksum_path="$tmp_dir/$checksum_name"
extract_dir="$tmp_dir/extract"

echo "Installing @code ${tag} for ${platform}-${arch}"
download "${release_url}/${archive_name}" "$archive_path"
download "${release_url}/${checksum_name}" "$checksum_path"

(
  cd "$tmp_dir"
  verify_checksum "$checksum_name"
)

mkdir -p "$extract_dir"
tar -xzf "$archive_path" -C "$extract_dir"

binary_path="$(find_extracted_binary "$extract_dir")" || fail "release archive did not contain the @code binary"

mkdir -p "$ATCODE_INSTALL_DIR"
cp "$binary_path" "$ATCODE_INSTALL_DIR/at"
chmod 755 "$ATCODE_INSTALL_DIR/at"
ln -sf at "$ATCODE_INSTALL_DIR/atcode"
ln -sf at "$ATCODE_INSTALL_DIR/sonut"
copy_optional_file "atcode-linux-sandbox"
copy_optional_file "atcode-seccomp"
copy_optional_dir "helpers"
if [ "${ATCODE_SKIP_ENGINE:-0}" != "1" ]; then
  migrate_engine_names
  install_engine
fi

echo "Installed $ATCODE_INSTALL_DIR/at"
setup_path
