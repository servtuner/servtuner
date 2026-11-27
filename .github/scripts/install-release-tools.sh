#!/usr/bin/env bash
# Installs helm, helmfile and hauler at the versions .tool-versions pins, each checked against its project's
# published SHA-256 checksums, into the directory given (added to GITHUB_PATH when run in Actions).
#   .github/scripts/install-release-tools.sh <bin dir>
set -euo pipefail

bin="${1:?usage: install-release-tools.sh <bin dir>}"
root="$(cd "$(dirname "$0")/../.." && pwd)"
pin() { awk -v tool="$1" '$1 == tool {print $2}' "$root/.tool-versions"; }
work="$(mktemp -d)"
trap 'rm -rf "$work"' EXIT
mkdir -p "$bin"

# fetch <archive URL> <checksums URL> <binary path inside the archive> <name>
fetch() {
  local archive="$work/$(basename "$1")"
  curl -fsSL --retry 3 -o "$archive" "$1"
  curl -fsSL --retry 3 -o "$work/sums" "$2"
  # A checksums file lists every asset ("<sum>  <file>"); a .sha256sum file holds one line for this archive.
  local want
  want="$(awk -v f="$(basename "$1")" 'NF == 1 || $2 == f || $2 == "*"f {print $1; exit}' "$work/sums")"
  [ -n "$want" ] || { echo "install: no checksum for $(basename "$1")" >&2; exit 1; }
  echo "$want  $archive" | sha256sum --check --quiet
  tar -xzf "$archive" -C "$work" "$3"
  install -m 0755 "$work/$3" "$bin/$4"
  echo "install: $4 $("$bin/$4" version 2>/dev/null | head -1 || true)"
}

v="$(pin helm)"
fetch "https://get.helm.sh/helm-v$v-linux-amd64.tar.gz" "https://get.helm.sh/helm-v$v-linux-amd64.tar.gz.sha256sum" linux-amd64/helm helm
v="$(pin helmfile)"
fetch "https://github.com/helmfile/helmfile/releases/download/v$v/helmfile_${v}_linux_amd64.tar.gz" \
  "https://github.com/helmfile/helmfile/releases/download/v$v/helmfile_${v}_checksums.txt" helmfile helmfile
v="$(pin hauler)"
fetch "https://github.com/hauler-dev/hauler/releases/download/v$v/hauler_${v}_linux_amd64.tar.gz" \
  "https://github.com/hauler-dev/hauler/releases/download/v$v/hauler_${v}_checksums.txt" hauler hauler

[ -z "${GITHUB_PATH:-}" ] || echo "$bin" >> "$GITHUB_PATH"
