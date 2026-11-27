#!/usr/bin/env bash
# Offline smoke test of the built api and bench images (no cluster, no network): what a cluster
# run would otherwise be the first to find. Run after `just images`: just image-test
# Usage: e2e/test-images.sh <image tag> [<version the images were built with>]
set -euo pipefail
cd "$(dirname "$0")/.."

tag="${1:?usage: test-images.sh <image tag> [<version>]}"
version="${2:-0.0.0-dev}"
api_image="servtuner-api:$tag"
bench_image="servtuner-bench:$tag"
examples="$PWD/schema/examples/runspec/valid"
spec=/specs/minimal-vllm-mtls.json
failures=0
# shellcheck source=labeled-containers.sh
source e2e/labeled-containers.sh test-images
trap remove_run_containers EXIT

check() { # check <description> <status>
  if [ "$2" -eq 0 ]; then echo "ok   $1"; else echo "FAIL $1"; failures=$((failures + 1)); fi
}

# in_image <image> <entrypoint> <args...>: the specs are mounted read-only; output is kept for FAIL reports.
in_image() {
  local image="$1" entrypoint="$2"; shift 2
  docker run --rm --label "$run_label" -v "$examples:/specs:ro" --entrypoint "$entrypoint" "$image" "$@" 2>&1
}

output="$(in_image "$api_image" python -c '
import json
from servtuner_api.models.validation import parse_run_spec, schema_names
print(schema_names())
parse_run_spec(json.load(open("/specs/minimal-vllm-mtls.json")))
print("parsed")' || true)"
check "api image: schemas load through the api's own code path and a RunSpec parses" "$(grep -q '^parsed$' <<<"$output" && echo 0 || { echo "$output" >&2; echo 1; })"

output="$(in_image "$api_image" servtuner --help || true)"
check "api image: servtuner runs (the SubprocessRunner's binary)" "$(grep -q 'Usage:' <<<"$output" && echo 0 || { echo "$output" >&2; echo 1; })"

output="$(in_image "$api_image" sh -c 'exec "$SERVTUNER_BENCH_BINARY" validate --spec '"$spec" || true)"
check "api image: SERVTUNER_BENCH_BINARY validates an example RunSpec" "$(grep -q '^ok:' <<<"$output" && echo 0 || { echo "$output" >&2; echo 1; })"

output="$(in_image "$bench_image" /usr/local/bin/servtuner validate --spec "$spec" || true)"
check "bench image: servtuner validate accepts an example RunSpec" "$(grep -q '^ok:' <<<"$output" && echo 0 || { echo "$output" >&2; echo 1; })"

# The version is stamped at build time and recorded in every bundle as bench_version.
output="$(in_image "$bench_image" /usr/local/bin/servtuner --version || true)"
check "bench image: servtuner reports version $version" "$(grep -qx "servtuner version $version" <<<"$output" && echo 0 || { echo "$output" >&2; echo 1; })"
output="$(in_image "$api_image" sh -c '"$SERVTUNER_BENCH_BINARY" --version && echo "api $SERVTUNER_VERSION"' || true)"
check "api image: its servtuner and the api report version $version" "$(grep -qx "servtuner version $version" <<<"$output" && grep -qx "api $version" <<<"$output" && echo 0 || { echo "$output" >&2; echo 1; })"

[ "$failures" -eq 0 ] && echo "all image tests passed" || { echo "$failures image test(s) failed"; exit 1; }
