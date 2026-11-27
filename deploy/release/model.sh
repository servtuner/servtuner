#!/usr/bin/env bash
# A model archive for a managed vLLM's model_source pvc (INSTALL.md, "Models"):
#
#   deploy/release/model.sh NAME CACHE_DIR LICENSE_FILE
#
# CACHE_DIR is a Hugging Face cache holding the model (HF_HOME/hub, or the folder `hf download
# --cache-dir` wrote); LICENSE_FILE is the model's licence text. Writes dist/servtuner-model-NAME.tar.zst,
# a Hauler archive with one file, servtuner-model-NAME.tar.gz: the cache and the licence.
#
# Only a model whose licence allows redistribution may be archived. The model card says:
# a gated model (extra_gated_*) is refused, and so is any licence outside the open list below; for
# anything else, read the licence yourself before shipping the archive.
set -euo pipefail

name="${1:?usage: model.sh NAME CACHE_DIR LICENSE_FILE}"
cache="${2:?usage: model.sh NAME CACHE_DIR LICENSE_FILE}"
licence="${3:?usage: model.sh NAME CACHE_DIR LICENSE_FILE}"
root="$(cd "$(dirname "$0")/../.." && pwd)"
# SPDX ids of licences that allow redistribution without conditions this script cannot check.
open_licences="apache-2.0 mit bsd-2-clause bsd-3-clause cc-by-4.0 cc0-1.0"

[[ "$name" =~ ^[a-z0-9][a-z0-9.-]*$ ]] || { echo "model.sh: name it in lower case, letters, digits, . and -" >&2; exit 2; }
[ -d "$cache" ] || { echo "model.sh: $cache is not a directory" >&2; exit 2; }
[ -s "$licence" ] || { echo "model.sh: $licence is missing or empty: the archive carries the model's licence" >&2; exit 2; }

card="$(find "$cache" -name README.md -path '*snapshots*' -print -quit)"
[ -n "$card" ] || { echo "model.sh: no model card (snapshots/*/README.md) in $cache to read the licence from" >&2; exit 2; }
if grep -q '^extra_gated' "$card"; then
  echo "model.sh: $card marks the model gated: its terms bind each user, so it is not redistributed" >&2
  exit 1
fi
declared="$(awk '/^---$/ {front++; next} front == 1 && $1 == "license:" {print tolower($2); exit}' "$card")"
if [ -z "$declared" ] || ! grep -qw -- "$declared" <<<"$open_licences"; then
  echo "model.sh: the model card's licence is '${declared:-none}', not one of: $open_licences" >&2
  exit 1
fi

work="$(mktemp -d)"
trap 'rm -rf "$work"' EXIT
mkdir -p "$work/model/hub" "$root/dist"
cp -r "$cache"/. "$work/model/hub/"
cp "$licence" "$work/model/LICENSE"
tar -czf "$work/servtuner-model-$name.tar.gz" -C "$work" model
hauler store -s "$work/store" add file "$work/servtuner-model-$name.tar.gz"
hauler store -s "$work/store" save -f "$root/dist/servtuner-model-$name.tar.zst"
echo "model.sh: dist/servtuner-model-$name.tar.zst ($declared)"
