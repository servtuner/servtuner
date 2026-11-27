#!/usr/bin/env bash
# Offline render test for deploy/kind/servtuner-bifrost.values.yaml. Client-side only: no cluster is contacted.
# Usage: deploy/kind/test-servtuner-bifrost.sh        Run from anywhere.
# BIFROST_CHART overrides the chart path (default: the local nuclio checkout, which is not part of this repo).
set -euo pipefail
export KUBECONFIG=/dev/null

here="$(cd "$(dirname "$0")" && pwd)"
values="$here/servtuner-bifrost.values.yaml"
chart="${BIFROST_CHART:-$HOME/nuclio/bifrost/helm-charts/bifrost}"
failures=0

check() { if [ "$2" -eq 0 ]; then echo "ok   $1"; else echo "FAIL $1"; failures=$((failures + 1)); fi; }
count() { grep -cE "$1" <<<"$rendered" || true; }

[ -d "$chart" ] || { echo "FAIL bifrost chart not found at $chart (set BIFROST_CHART)"; exit 1; }
rendered="$(helm template servtuner-bifrost "$chart" -n servtuner-bifrost -f "$values")" || { echo "FAIL render"; exit 1; }
echo "ok   render"
lint_status=0; helm lint "$chart" -f "$values" >/dev/null || lint_status=$?
check "lint" "$lint_status"

# The provider config is rendered into config.json inside a ConfigMap. Extract it as JSON.
configJson="$(sed -n '/^  config.json: |$/{n;s/^    //;p;}' <<<"$rendered")"
# configValue '<python expression over c>' prints the expression's value; c is the parsed config.json.
configValue() { python3 -c 'import json, sys; c = json.loads(sys.argv[1]); print(eval(sys.argv[2]))' "$configJson" "$1"; }
mock='c["providers"]["servtuner-mock"]'
is_value() { [ "$(configValue "$1")" = "$2" ] && echo 0 || echo 1; }

check "pod labelled servtuner.dev/scrape=true" "$([ "$(count '^        servtuner.dev/scrape: "true"$')" -eq 1 ] && echo 0 || echo 1)"
check "scrape annotations: port 8080, path /metrics, scheme http" "$([ "$(count 'servtuner.dev/metrics-port: "8080"')" -eq 1 ] && [ "$(count 'servtuner.dev/metrics-path: /metrics$')" -eq 1 ] && [ "$(count 'servtuner.dev/metrics-scheme: http$')" -eq 1 ] && echo 0 || echo 1)"
check "containerPort 8080 matches the metrics-port annotation" "$([ "$(count 'containerPort: 8080')" -eq 1 ] && echo 0 || echo 1)"
check "image is pinned (no latest, no empty tag)" "$([ "$(count 'image: "docker.io/maximhq/bifrost:v[0-9]')" -eq 1 ] && echo 0 || echo 1)"

check "provider servtuner-mock is declared" "$(is_value '"servtuner-mock" in c["providers"]' True)"
check "provider base type is openai" "$(is_value "$mock['custom_provider_config']['base_provider_type']" openai)"
check "provider base_url is the platform-mode mock, without /v1" "$(is_value "$mock['network_config']['base_url']" http://openai-mock.servtuner-mock.svc:8000)"
check "provider may dial the mock's private Service IP" "$(is_value "$mock['network_config']['allow_private_network']" True)"
check "provider serves mock-model" "$(is_value "$mock['keys'][0]['models']" "['mock-model']")"
check "provider allows chat, completions and their streams" "$(is_value "all($mock['custom_provider_config']['allowed_requests'].get(r) for r in ('chat_completion', 'chat_completion_stream', 'text_completion', 'text_completion_stream'))" True)"
check "inference does not require a virtual key" "$(is_value "c['client']['enforce_auth_on_inference']" False)"
check "admin auth is off (dev only), so no auth_config is rendered" "$(is_value '"auth_config" in c' False)"

# No secrets: the chart renders no Secret object, the only key value is the literal placeholder, and
# no env var comes from a secretKeyRef or carries a credential-looking value.
check "no Secret object rendered" "$([ "$(count '^kind: Secret$')" -eq 0 ] && echo 0 || echo 1)"
check "provider key value is the literal placeholder" "$(is_value "$mock['keys'][0]['value']" unused-placeholder)"
check "no secretKeyRef or credential-looking value" "$([ "$(count 'secretKeyRef|sk-[A-Za-z0-9]{8}|password|api[_-]?key: ')" -eq 0 ] && echo 0 || echo 1)"
check "values file has no IP address or home path" "$([ "$(grep -cE '([0-9]{1,3}\.){3}[0-9]{1,3}|/home/' "$values" || true)" -eq 0 ] && echo 0 || echo 1)"

# Namespace: every namespaced object is in servtuner-bifrost, and nothing names another namespace.
check "every namespaced object is in servtuner-bifrost" "$([ "$(count '^  namespace: servtuner-bifrost$')" -eq "$(count '^kind: (ServiceAccount|ConfigMap|Service|Deployment|StatefulSet)$')" ] && [ "$(count '^  namespace: ' )" -eq "$(count '^  namespace: servtuner-bifrost$')" ] && echo 0 || echo 1)"
check "release name gives Service servtuner-bifrost" "$([ "$(count '^  name: servtuner-bifrost$')" -ge 3 ] && echo 0 || echo 1)"
check "pod selector includes the instance label" "$([ "$(count 'app.kubernetes.io/instance: servtuner-bifrost')" -ge 4 ] && echo 0 || echo 1)"

[ "$failures" -eq 0 ] && echo "all servtuner-bifrost tests passed" || { echo "$failures servtuner-bifrost test(s) failed"; exit 1; }
