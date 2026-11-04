#!/usr/bin/env bash
# Compatibility entrypoint for the canonical checksum-verifying @code installer.
set -euo pipefail

source_repo="${ATCODE_INSTALL_SOURCE_REPO:-aslam-anees/sonut}"
source_ref="${ATCODE_INSTALL_SOURCE_REF:-src}"
installer_url="https://raw.githubusercontent.com/${source_repo}/${source_ref}/scripts/install.sh"
temp_dir="$(mktemp -d "${TMPDIR:-/tmp}/atcode-bootstrap.XXXXXX")"
installer_path="$temp_dir/install.sh"
trap 'rm -rf "$temp_dir"' EXIT

if command -v curl >/dev/null 2>&1; then
  curl --fail --location --show-error --silent "$installer_url" --output "$installer_path"
elif command -v wget >/dev/null 2>&1; then
  wget --quiet "$installer_url" --output-document "$installer_path"
else
  echo "@code install: curl or wget is required" >&2
  exit 1
fi

chmod 755 "$installer_path"
"$installer_path" "$@"
