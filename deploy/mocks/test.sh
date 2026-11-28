#!/usr/bin/env bash
# Offline render tests for the mock chart. Client-side only: no cluster is contacted.
# Usage: deploy/mocks/test.sh        Run from anywhere.
set -euo pipefail
export KUBECONFIG=/dev/null

here="$(cd "$(dirname "$0")" && pwd)"
chart="$here/chart"
failures=0

render() { helm template openai-mock "$chart" -n servtuner-mock "$@"; }
check() { if [ "$2" -eq 0 ]; then echo "ok   $1"; else echo "FAIL $1"; failures=$((failures + 1)); fi; }
count() { grep -cE "$1" <<<"$rendered" || true; }
is() { [ "$1" -eq "$2" ] && echo 0 || echo 1; }

for values in client-cert client-cert-optional server-tls tls-off; do
  rendered="$(render -f "$chart/ci/$values.yaml")" || { echo "FAIL render $values"; failures=$((failures + 1)); continue; }
  echo "ok   render $values"
  lint_status=0; helm lint "$chart" -f "$chart/ci/$values.yaml" >/dev/null || lint_status=$?
  check "lint $values" "$lint_status"
  check "$values: pod labelled servtuner.dev/scrape=true" "$(is "$(count 'servtuner.dev/scrape: "true"')" 1)"
  check "$values: namespace is servtuner-mock" "$([ "$(count 'namespace: servtuner-mock')" -ge 2 ] && echo 0 || echo 1)"
  check "$values: numeric non-root user and fsGroup" "$([ "$(count 'runAsUser: 65532')" -eq 1 ] && [ "$(count 'fsGroup: 65532')" -eq 1 ] && echo 0 || echo 1)"
  check "$values: plain metrics listener, scrape annotation and Service port agree on 9100" "$([ "$(count 'OPENAI_MOCK_METRICS_LISTEN, value: ":9100"')" -eq 1 ] && [ "$(count 'servtuner.dev/metrics-port: "9100"')" -eq 1 ] && [ "$(count 'name: metrics, port: 9100')" -eq 1 ] && echo 0 || echo 1)"
  check "$values: scheme http, no sidecar or proxy ConfigMap" "$([ "$(count 'servtuner.dev/metrics-scheme: http')" -eq 1 ] && [ "$(count 'metrics-proxy|nginx')" -eq 0 ] && echo 0 || echo 1)"
  case "$values" in
    tls-off)
      check "tls-off: OPENAI_MOCK_TLS false, no Certificate, no client auth, no cert files" "$([ "$(count 'OPENAI_MOCK_TLS, value: "false"')" -eq 1 ] && [ "$(count '^kind: Certificate$|_FILE|mock-tls|CLIENT_AUTH')" -eq 0 ] && echo 0 || echo 1)"
      ;;
    *)
      policy="$(grep -oE 'clientAuth: [a-z]+' "$chart/ci/$values.yaml" | cut -d' ' -f2)"
      check "$values: OPENAI_MOCK_TLS true with client auth $policy" "$([ "$(count 'OPENAI_MOCK_TLS, value: "true"')" -eq 1 ] && [ "$(count "OPENAI_MOCK_CLIENT_AUTH, value: \"$policy\"")" -eq 1 ] && echo 0 || echo 1)"
      check "$values: one Certificate from the servtuner-ca ClusterIssuer, server and client auth" "$([ "$(count '^kind: Certificate$')" -eq 1 ] && [ "$(count 'name: servtuner-ca$')" -eq 1 ] && [ "$(count 'server auth')" -eq 1 ] && echo 0 || echo 1)"
      check "$values: cert and key files passed" "$(is "$(count 'OPENAI_MOCK_TLS_(CERT|KEY)_FILE')" 2)"
      want_ca=1; [ "$policy" = none ] && want_ca=0
      check "$values: client CA file passed only when client certificates are checked" "$(is "$(count 'OPENAI_MOCK_TLS_CLIENT_CA_FILE')" "$want_ca")"
      ;;
  esac
done

# Every variable the chart sets is one the mock reads (config.go), so a misspelt knob cannot be ignored.
rendered="$(render -f "$chart/ci/tls-off.yaml")"
read_by_mock="$(grep -oE 'env\.[A-Za-z]+\("[A-Z_]+"' "$here/openai-mock/config.go" | sed -E 's/.*\("([A-Z_]+)"/OPENAI_MOCK_\1/' | sort -u)"
unread="$(grep -oE 'OPENAI_MOCK_[A-Z_]+' <<<"$rendered" | sort -u | comm -23 - <(echo "$read_by_mock"))"
check "every OPENAI_MOCK_* variable the chart sets is read by the mock ${unread:+(unread: $unread)}" "$([ -z "$unread" ] && echo 0 || echo 1)"
check "fault knobs are passed, off by default" "$([ "$(count 'OPENAI_MOCK_(EXTRA_LATENCY|LATENCY_PER_IN_FLIGHT|MAX_CONCURRENCY|OVER_CAPACITY|ERROR_RATE|ERROR_STATUS|MEMORY_[A-Z_]+), value')" -eq 9 ] && [ "$(count 'OPENAI_MOCK_ERROR_RATE, value: "0"')" -eq 1 ] && echo 0 || echo 1)"
check "no emulation by default" "$([ "$(count 'OPENAI_MOCK_EMULATE')" -eq 0 ] && echo 0 || echo 1)"
rendered="$(render -f "$chart/ci/tls-off.yaml" --set mock.emulate=sglang)"
check "emulate sglang is passed" "$([ "$(count 'OPENAI_MOCK_EMULATE, value: "sglang"')" -eq 1 ] && echo 0 || echo 1)"
rendered="$(render -f "$chart/ci/tls-off.yaml")"

# helm upgrade --reuse-values keeps a release's old values and skips new chart defaults: a release
# that predates the fault knobs, given one of them, must still render every knob as a real value.
partial='{"mock": {"faults": {"extraLatency": null, "maxConcurrency": null, "overCapacity": null, "errorRate": null, "errorStatus": null, "memoryPerRequestBytes": null, "memoryRampBytesPerSecond": null, "memoryRampMaxBytes": null, "latencyPerInFlight": "1ms"}}}'
rendered="$(render -f "$chart/ci/tls-off.yaml" -f <(echo "$partial"))"
check "fault knobs missing from the values fall back to their defaults (--reuse-values)" "$([ "$(count '<nil>')" -eq 0 ] && [ "$(count 'OPENAI_MOCK_ERROR_RATE, value: "0"')" -eq 1 ] && [ "$(count 'OPENAI_MOCK_LATENCY_PER_IN_FLIGHT, value: "1ms"')" -eq 1 ] && echo 0 || echo 1)"

# Each failure case must fail with its message.
expect_failure() { # <message> <helm args...>
  local message="$1"; shift
  if output="$(render "$@" 2>&1)"; then echo "FAIL $message: rendered but should have failed"; failures=$((failures + 1))
  elif grep -qF -- "$message" <<<"$output"; then echo "ok   fail: $message"
  else echo "FAIL expected message \"$message\""; echo "$output" | head -5; failures=$((failures + 1)); fi
}
expect_failure 'tls.clientAuth "sometimes" is not one of' --set tls.clientAuth=sometimes
expect_failure 'tls.enabled must be true or false' --set-string tls.enabled=yes
expect_failure 'tls.enabled needs tls.certManager.issuerRef.name' --set tls.certManager.issuerRef.name=
expect_failure 'was replaced in servtuner 0.2' --set tls.mode=mtls

# The published image by default, at the chart's appVersion (a release packages the chart with its version).
app_version="$(sed -n 's/^appVersion: "\(.*\)"$/\1/p' "$chart/Chart.yaml")"
rendered="$(render -f "$chart/ci/tls-off.yaml")"
check "the published image at the chart's appVersion by default" "$(grep -qF "image: \"ghcr.io/servtuner/servtuner-mock:$app_version\"" <<<"$rendered" && echo 0 || echo 1)"
rendered="$(render -f "$chart/ci/tls-off.yaml" --set image.repository=servtuner-mock --set image.tag=e2e)"
check "a local build: repository and tag as given" "$(grep -qF 'image: "servtuner-mock:e2e"' <<<"$rendered" && echo 0 || echo 1)"

[ "$failures" -eq 0 ] && echo "all mock chart tests passed" || { echo "$failures mock chart test(s) failed"; exit 1; }
