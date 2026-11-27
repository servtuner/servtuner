#!/usr/bin/env bash
# Builds the image and checks that one build serves under any context path.
# Not part of `just test`, because it needs docker. Run: just test-container
set -euo pipefail
cd "$(dirname "$0")"

image="servtuner-frontend:test-container"
hostile_prefix='"/><script>'
failures=0
# shellcheck source=../e2e/labeled-containers.sh
source ../e2e/labeled-containers.sh test-container

# Also removes the test image tag (the layer cache stays, so the next build is fast).
cleanup() {
  remove_run_containers
  docker rmi "$image" >/dev/null 2>&1 || true
}
trap cleanup EXIT

check() { # check <description> <status>
  if [ "$2" -eq 0 ]; then echo "ok   $1"; else echo "FAIL $1"; failures=$((failures + 1)); fi
}

docker build -q -t "$image" . >/dev/null

# start_container <env-var-args...>: prints the base URL of a container on a random port.
start_container() {
  local id port
  id="$(docker run -d --label "$run_label" -p 127.0.0.1::8080 "$@" "$image")"
  port="$(docker port "$id" 8080/tcp | head -1 | sed 's/.*://')"
  for _ in $(seq 1 50); do
    curl -fs -o /dev/null "http://127.0.0.1:$port/config.json" && { echo "http://127.0.0.1:$port"; return; }
    sleep 0.2
  done
  echo "container did not become ready" >&2; docker logs "$id" >&2; exit 1
}

# run_scenario <name> <expected-base> <mount-path> <prefix-header|-> <env-var-args...>
# mount-path is where the proxy mounts the app on the way in: "" when it strips the prefix.
run_scenario() {
  local name="$1" expected_base="$2" mount="$3" header="$4"; shift 4
  local origin headers=() body asset_path
  origin="$(start_container "$@")"
  [ "$header" = "-" ] || headers=(-H "X-Forwarded-Prefix: $header")

  body="$(curl -s "${headers[@]}" -D /tmp/servtuner-headers.$$ "$origin$mount/runs/abc")"
  check "$name: deep link returns index.html with <base href=\"$expected_base\">" \
    "$(grep -qF "<base href=\"$expected_base\" />" <<<"$body" && echo 0 || echo 1)"
  check "$name: index.html is no-cache and varies on X-Forwarded-Prefix" \
    "$(grep -qi '^cache-control: no-cache' /tmp/servtuner-headers.$$ && grep -qi '^vary: .*X-Forwarded-Prefix.*Accept-Encoding' /tmp/servtuner-headers.$$ && echo 0 || echo 1)"

  # The browser resolves each ./assets/... reference against <base href>.
  local kind pattern expected_type
  for kind in js css; do
    case "$kind" in
      js) pattern='src="\./assets/[^"]*\.js"'; expected_type='javascript' ;;
      css) pattern='href="\./assets/[^"]*\.css"'; expected_type='text/css' ;;
    esac
    asset_path="$(grep -oE "$pattern" <<<"$body" | head -1 | sed -E 's/^[a-z]+="\.\/(.*)"$/\1/')"
    check "$name: index.html references a $kind asset" "$([ -n "$asset_path" ] && echo 0 || echo 1)"
    check "$name: $kind asset returns 200 with $expected_type" \
      "$(curl -s "${headers[@]}" -o /dev/null -D - "$origin$mount/$asset_path" | tr -d '\r' | awk -v t="$expected_type" 'NR==1 && $2!="200" {bad=1} tolower($0) ~ "^content-type:.*" t {ok=1} END {exit !(ok && !bad)}' && echo 0 || echo 1)"
  done

  check "$name: config.json resolves, uncached" \
    "$(curl -s "${headers[@]}" -o /dev/null -D - "$origin$mount/config.json" | tr -d '\r' | awk 'NR==1 && $2!="200" {bad=1} tolower($0) ~ /^cache-control: no-store/ {ok=1} END {exit !(ok && !bad)}' && echo 0 || echo 1)"

  check "$name: config.json missing at depth is 404, not index.html" \
    "$([ "$(curl -s -o /dev/null -w '%{http_code}' "$origin$mount/runs/x/config.json")" = 404 ] && echo 0 || echo 1)"
  check "$name: an api path that reaches the UI is a JSON 404, not index.html" \
    "$(curl -s -o /dev/null -D - "${headers[@]}" "$origin$mount/api/auth/status" | tr -d '\r' | awk 'NR==1 && $2!="404" {bad=1} tolower($0) ~ /^content-type: application\/json/ {ok=1} END {exit !(ok && !bad)}' && echo 0 || echo 1)"
  check "$name: forged If-None-Match and If-Modified-Since do not 304 index.html" \
    "$([ "$(curl -s -o /dev/null -w '%{http_code}' "${headers[@]}" -H 'If-None-Match: *' -H 'If-Modified-Since: Fri, 01 Jan 2100 00:00:00 GMT' "$origin$mount/runs/abc")" = 200 ] && echo 0 || echo 1)"
  check "$name: duplicate X-Forwarded-Prefix falls back to the env path" \
    "$(curl -s -H 'X-Forwarded-Prefix: /p/q' -H 'X-Forwarded-Prefix: /r/s' "$origin$mount/runs/abc" | grep -qF "<base href=\"${SERVTUNER_EXPECTED_ENV_BASE:-/}\" />" && echo 0 || echo 1)"

  # A hostile header must give the env fallback and never appear in the body.
  local fallback_base="${SERVTUNER_EXPECTED_ENV_BASE:-/}"
  body="$(curl -s -H "X-Forwarded-Prefix: $hostile_prefix" "$origin$mount/runs/abc")"
  check "$name: hostile X-Forwarded-Prefix falls back to <base href=\"$fallback_base\">" \
    "$(grep -qF "<base href=\"$fallback_base\" />" <<<"$body" && echo 0 || echo 1)"
  check "$name: hostile payload is not reflected" "$(grep -qF "$hostile_prefix" <<<"$body" && echo 1 || echo 0)"
  rm -f /tmp/servtuner-headers.$$
}

SERVTUNER_EXPECTED_ENV_BASE=/ run_scenario default / "" -
SERVTUNER_EXPECTED_ENV_BASE=/tools/servtuner/ run_scenario "env path, prefix passed through" /tools/servtuner/ /tools/servtuner - -e SERVTUNER_BASE_PATH=/tools/servtuner/
SERVTUNER_EXPECTED_ENV_BASE=/ run_scenario "X-Forwarded-Prefix header" /x/y/ "" /x/y

# The same image also serves at "/" when the env path is set and the proxy strips it.
origin="$(start_container -e SERVTUNER_BASE_PATH=/tools/servtuner/)"
check "env path set, request at /: still served" \
  "$([ "$(curl -s -o /dev/null -w '%{http_code}' "$origin/runs/abc")" = 200 ] && echo 0 || echo 1)"

# An invalid SERVTUNER_BASE_PATH stops the container at start.
for invalid in '/a b' '/../x' '/a/<b>' $'/a\nb' $'/a\n}\nserver {' '/a;b' "/$(printf 'x%.0s' $(seq 1 65))"; do
  status=0
  docker run --rm --label "$run_label" -e "SERVTUNER_BASE_PATH=$invalid" "$image" >/dev/null 2>&1 || status=$?
  check "invalid SERVTUNER_BASE_PATH $(printf '%q' "${invalid:0:20}") (${#invalid} chars) fails fast" "$([ "$status" -ne 0 ] && echo 0 || echo 1)"
done

[ "$failures" -eq 0 ] && echo "all container tests passed" || { echo "$failures container test(s) failed"; exit 1; }
