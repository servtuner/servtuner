#!/usr/bin/env bash
# Offline render tests for the servtuner chart. Client-side only: no cluster is contacted
# (KUBECONFIG is forced empty; capabilities are simulated with --api-versions).
# Usage: deploy/helm/test.sh        Run from anywhere.
set -euo pipefail
export KUBECONFIG=/dev/null

here="$(cd "$(dirname "$0")" && pwd)"
chart="$here/servtuner"
namespace=servtuner
capabilities=(--api-versions postgresql.cnpg.io/v1 --api-versions cert-manager.io/v1)
failures=0

# NOTES.txt only prints on install; helm install has no --api-versions, so use external Postgres.
notes() { helm install servtuner "$chart" -n "$namespace" --dry-run=client --set postgres.mode=external --set postgres.external.host=postgres.example.internal "$@"; }

render() { helm template servtuner "$chart" -n "$namespace" "${capabilities[@]}" "$@"; }

check() { # check <description> <condition-exit-status>
  if [ "$2" -eq 0 ]; then echo "ok   $1"; else echo "FAIL $1"; failures=$((failures + 1)); fi
}

# Count lines matching a pattern in rendered output.
count() { grep -cE "$1" <<<"$rendered" || true; }

for values in mutual-tls-cert-manager mutual-tls-secret tls-off tls-server-only s3-separate-namespace; do
  file="$chart/ci/$values.yaml"
  rendered="$(render -f "$file")" || { echo "FAIL render $values"; failures=$((failures + 1)); continue; }
  echo "ok   render $values"
  lint_status=0
  helm lint "$chart" -f "$file" -f "$chart/ci/lint-overlay.yaml" >/dev/null || lint_status=$?
  check "lint $values" "$lint_status"
  check "$values: api and frontend drain before stopping (preStop sleep)" "$([ "$(count 'sleep: \{seconds: 5\}')" -eq 2 ] && echo 0 || echo 1)"

  case "$values" in
    mutual-tls-cert-manager)
      check "$values: Certificate resources rendered" "$([ "$(count '^kind: Certificate$')" -ge 5 ] && echo 0 || echo 1)"
      check "$values: CNPG Cluster rendered" "$([ "$(count '^kind: Cluster$')" -eq 1 ] && echo 0 || echo 1)"
      check "$values: SSL_CERT_FILE set" "$([ "$(count 'SSL_CERT_FILE')" -ge 1 ] && echo 0 || echo 1)"
      check "$values: one shared CA init container" "$([ "$(count 'name: build-ca-bundle')" -eq 1 ] && echo 0 || echo 1)"
      check "$values: pg_hba requires client certs" "$([ "$(count 'hostssl all all all cert')" -eq 1 ] && echo 0 || echo 1)"
      check "$values: no RWO warning" "$(notes -f "$file" | grep -q 'ReadWriteOnce' && echo 1 || echo 0)"
      check "$values: client CA file rendered, from the CA source Secret" "$([ "$(count 'SERVTUNER_TLS_CLIENT_CA_FILE: /etc/servtuner/tls/client-ca/ca.crt')" -eq 1 ] && echo 0 || echo 1)"
      check "$values: client CA mounted in the api" "$([ "$(count 'mountPath: /etc/servtuner/tls/client-ca')" -eq 1 ] && echo 0 || echo 1)"
      check "$values: exactly one PVC" "$([ "$(count '^kind: PersistentVolumeClaim$')" -eq 1 ] && echo 0 || echo 1)"
      check "$values: api Role plus bench read Roles in both target namespaces" "$([ "$(count '^kind: Role$')" -eq 3 ] && echo 0 || echo 1)"
      ;;
    mutual-tls-secret)
      check "$values: no Certificate resources" "$([ "$(count '^kind: Certificate$')" -eq 0 ] && echo 0 || echo 1)"
      check "$values: client CA file rendered (CA source is caSecretRef)" "$([ "$(count 'SERVTUNER_TLS_CLIENT_CA_FILE')" -eq 1 ] && echo 0 || echo 1)"
      check "$values: named Secret mounted" "$([ "$(count 'secretName: my-api-tls')" -ge 1 ] && echo 0 || echo 1)"
      check "$values: caSecretRef feeds the CA bundle" "$([ "$(count 'secretName: "my-private-ca"')" -ge 1 ] && echo 0 || echo 1)"
      check "$values: no PVC created for an existing claim" "$([ "$(count '^kind: PersistentVolumeClaim$')" -eq 0 ] && echo 0 || echo 1)"
      ;;
    s3-separate-namespace)
      check "$values: bench Certificate is in the runs namespace" "$(grep -A4 'name: servtuner-bench-tls' <<<"$rendered" | grep -q 'namespace: servtuner-runs' && echo 0 || echo 1)"
      check "$values: no PVC" "$([ "$(count '^kind: PersistentVolumeClaim$')" -eq 0 ] && echo 0 || echo 1)"
      check "$values: api Role is in the runs namespace" "$(grep -B1 -A2 'name: servtuner-api$' <<<"$rendered" | grep -q 'namespace: servtuner-runs' && echo 0 || echo 1)"
      ;;
    tls-off)
      check "$values: no Certificate resources" "$([ "$(count '^kind: Certificate$')" -eq 0 ] && echo 0 || echo 1)"
      check "$values: no SSL_CERT_FILE or CA init container" "$([ "$(count 'SSL_CERT_FILE|build-ca-bundle')" -eq 0 ] && echo 0 || echo 1)"
      check "$values: no client CA file" "$([ "$(count 'SERVTUNER_TLS_CLIENT_CA_FILE|client-ca')" -eq 0 ] && echo 0 || echo 1)"
      check "$values: database sslmode disable" "$([ "$(count 'SERVTUNER_DATABASE_SSLMODE: "disable"')" -eq 1 ] && echo 0 || echo 1)"
      check "$values: RWO warning in NOTES" "$(notes -f "$file" | grep -q 'WARNING: the data volume is ReadWriteOnce' && echo 0 || echo 1)"
      check "$values: node pinning passed to the runner" "$([ "$(count 'SERVTUNER_STORAGE_NODE_PINNED: "true"')" -eq 1 ] && echo 0 || echo 1)"
      ;;
    tls-server-only)
      check "$values: the api asks for no client certificate" "$([ "$(count 'SERVTUNER_TLS_CLIENT_AUTH: "none"')" -eq 1 ] && [ "$(count 'SERVTUNER_TLS_CLIENT_CA_FILE|client-ca')" -eq 0 ] && echo 0 || echo 1)"
      check "$values: no bench client certificate, but the CA that verifies the api" "$(grep -qF 'SERVTUNER_TLS_CA_SECRET: "servtuner-bench-tls"' <<<"$rendered" && grep -qF 'SERVTUNER_API_URL: "https://' <<<"$rendered" && echo 0 || echo 1)"
      check "$values: Postgres takes a password over TLS" "$([ "$(count 'hostssl all all all scram-sha-256')" -eq 1 ] && [ "$(count 'hostssl all all all cert')" -eq 0 ] && grep -qF 'SERVTUNER_TLS_HOP_POSTGRES_CLIENT_CERT: "false"' <<<"$rendered" && grep -qF 'SERVTUNER_DATABASE_SSLMODE: "verify-full"' <<<"$rendered" && [ "$(count 'name: SERVTUNER_DATABASE_PASSWORD')" -eq 1 ] && echo 0 || echo 1)"
      check "$values: the api's own hops are TLS" "$(grep -qF 'SERVTUNER_TLS_HOP_INGRESS_API_ENABLED: "true"' <<<"$rendered" && echo 0 || echo 1)"
      ;;
  esac
done

# frontend.basePath reaches the frontend container as SERVTUNER_BASE_PATH (default "/").
rendered="$(render -f "$chart/ci/tls-off.yaml")"
check "frontend: SERVTUNER_BASE_PATH defaults to /" "$(grep -A1 'name: SERVTUNER_BASE_PATH' <<<"$rendered" | grep -q 'value: "/"' && echo 0 || echo 1)"
rendered="$(render -f "$chart/ci/tls-off.yaml" --set frontend.basePath=/tools/servtuner/)"
check "frontend: basePath passed as SERVTUNER_BASE_PATH" "$(grep -A1 'name: SERVTUNER_BASE_PATH' <<<"$rendered" | grep -q 'value: "/tools/servtuner/"' && echo 0 || echo 1)"

# Optional Ingress and the chart-rendered config.json (both modes). Only the api Ingress differs.
ingress_args=(--set ingress.enabled=true --set ingress.host=servtuner.example.test --set ingress.className=nginx --set frontend.basePath=/tools/servtuner/)
for values in mutual-tls-cert-manager tls-off; do
  rendered="$(render -f "$chart/ci/$values.yaml" "${ingress_args[@]}")" || { echo "FAIL render ingress $values"; failures=$((failures + 1)); continue; }
  check "ingress $values: two Ingresses" "$([ "$(count '^kind: Ingress$')" -eq 2 ] && echo 0 || echo 1)"
  check "ingress $values: UI routed under frontend.basePath" "$([ "$(count 'path: "/tools/servtuner"$')" -eq 1 ] && echo 0 || echo 1)"
  check "ingress $values: api path prefix is stripped" "$([ "$(count 'rewrite-target: /\$2')" -eq 1 ] && echo 0 || echo 1)"
  check "ingress $values: no snippet annotations" "$([ "$(count 'snippet')" -eq 0 ] && echo 0 || echo 1)"
done
rendered="$(render -f "$chart/ci/mutual-tls-cert-manager.yaml" "${ingress_args[@]}")"
check "ingress mutual-tls: backend HTTPS with the ingress certificate" "$([ "$(count 'backend-protocol: HTTPS')" -eq 1 ] && [ "$(count 'proxy-ssl-secret: "servtuner/servtuner-ingress-tls"')" -eq 1 ] && echo 0 || echo 1)"
check "ingress mutual-tls: api certificate verified" "$([ "$(count 'proxy-ssl-verify: "on"')" -eq 1 ] && echo 0 || echo 1)"
check "ingress mutual-tls: proxy-ssl-name is an api certificate SAN" "$(grep -qF 'proxy-ssl-name: "servtuner-api.servtuner.svc"' <<<"$rendered" && grep -qF -- '- servtuner-api.servtuner.svc' <<<"$rendered" && echo 0 || echo 1)"
check "api certificate carries localhost for the SubprocessRunner" "$(grep -qF -- '- localhost' <<<"$rendered" && echo 0 || echo 1)"
rendered="$(render -f "$chart/ci/tls-off.yaml" "${ingress_args[@]}")"
check "ingress tls-off: plain HTTP to the api" "$([ "$(count 'backend-protocol|proxy-ssl')" -eq 0 ] && echo 0 || echo 1)"
# The api path for each base path; an explicit ingress.apiPath and frontend.apiBaseUrl write config.json.
for base in / /servtuner/; do
  rendered="$(render -f "$chart/ci/tls-off.yaml" --set ingress.enabled=true --set ingress.host=h.example.test --set frontend.basePath=$base)"
  expected="$(printf '%s' "${base%/}")/api"
  check "ingress basePath $base: api path $expected, no config.json" "$(grep -qF "path: \"$expected(/|\$)(.*)\"" <<<"$rendered" && [ "$(count 'config.json')" -eq 0 ] && echo 0 || echo 1)"
  rendered="$(render -f "$chart/ci/tls-off.yaml" --set ingress.enabled=true --set ingress.host=h.example.test --set frontend.basePath=$base --set ingress.apiPath=/servtuner-api)"
  check "ingress basePath $base: explicit apiPath routed and written to config.json" "$(grep -qF 'path: "/servtuner-api(/|$)(.*)"' <<<"$rendered" && grep -qF 'apiBaseUrl\":\"/servtuner-api' <<<"$rendered" && echo 0 || echo 1)"
done
rendered="$(render -f "$chart/ci/tls-off.yaml" --set frontend.apiBaseUrl=https://api.example.test/v1)"
check "frontend.apiBaseUrl writes config.json" "$([ "$(count 'mountPath: /usr/share/nginx/html/config.json')" -eq 1 ] && echo 0 || echo 1)"
rendered="$(render -f "$chart/ci/mutual-tls-cert-manager.yaml" "${ingress_args[@]}" --set 'ingress.annotations.example\.io/extra=x')"
check "ingress: user annotations render before the chart's TLS ones on the api Ingress" "$([ "$(grep -n 'example.io/extra' <<<"$rendered" | tail -1 | cut -d: -f1)" -lt "$(grep -n 'proxy-ssl-verify' <<<"$rendered" | cut -d: -f1)" ] && echo 0 || echo 1)"
rendered="$(render -f "$chart/ci/mutual-tls-cert-manager.yaml")"
check "runner kubernetes with mutual TLS: bench verifies the api with the bench Secret's CA" "$(grep -qF 'SERVTUNER_TLS_CA_SECRET: "servtuner-bench-tls"' <<<"$rendered" && grep -qF 'SERVTUNER_API_URL: "https://servtuner-api.servtuner.svc:8080"' <<<"$rendered" && echo 0 || echo 1)"
check "every Secret volume with a private key is mode 0440 (libpq rejects group/world access)" "$([ "$(count '^ *- name: tls-')" -ge 1 ] && [ "$(count '^ *- name: tls-')" -eq "$(count 'defaultMode: 288$')" ] && echo 0 || echo 1)"
check "the setup token Secret is mounted 0440 and optional" "$(grep -qF 'secret: {secretName: "servtuner-setup-token", defaultMode: 288, optional: true}' <<<"$rendered" && echo 0 || echo 1)"
rendered="$(render -f "$chart/ci/tls-off.yaml")"
check "runner: kubernetes by default, and only bench's ServiceAccount reads targets" "$(grep -qF 'SERVTUNER_RUNNER: "kubernetes"' <<<"$rendered" && [ "$(count 'name: servtuner-api, namespace: servtuner\}')" -eq 1 ] && echo 0 || echo 1)"
check "runner kubernetes: Jobs reach the api through its Service" "$(grep -qF 'SERVTUNER_API_URL: "http://servtuner-api.servtuner.svc:8080"' <<<"$rendered" && grep -qF 'SERVTUNER_BENCH_TARGET_ANTI_AFFINITY: "required"' <<<"$rendered" && echo 0 || echo 1)"
check "api: pod name, namespace and node from the downward API" "$([ "$(count 'fieldPath: (metadata.name|metadata.namespace|spec.nodeName)')" -ge 3 ] && echo 0 || echo 1)"
rendered="$(render -f "$chart/ci/tls-off.yaml" --set runner=subprocess --set 'rbac.bench.targetNamespaces={targets}')"
check "runner subprocess: the api's ServiceAccount gets bench's read-only access (nodes and targets)" "$(grep -qF 'SERVTUNER_RUNNER: "subprocess"' <<<"$rendered" && [ "$(count 'name: servtuner-api, namespace: servtuner\}')" -eq 3 ] && echo 0 || echo 1)"
rendered="$(render -f "$chart/ci/s3-separate-namespace.yaml")"
check "s3 with a secret: Jobs get the Secret's name and keys, never the values" "$(grep -qF 'SERVTUNER_S3_CREDENTIALS_SECRET: "servtuner-s3"' <<<"$rendered" && grep -qF 'SERVTUNER_S3_SECRET_KEY_KEY: "secret-key"' <<<"$rendered" && echo 0 || echo 1)"
rendered="$(render -f "$chart/ci/tls-off.yaml" --set runner=subprocess --set 'rbac.bench.targetNamespaces={targets}')"
check "runner subprocess: no Job settings, and bench keeps the api's localhost URL" "$(grep -qE 'SERVTUNER_(API_URL|BENCH_CPU)' <<<"$rendered" && echo 1 || echo 0)"
rendered="$(render -f "$chart/ci/tls-off.yaml" --set runner=subprocess --set 'subprocess.secrets={vllm-key}')"
check "runner subprocess: a named run Secret is mounted read-only where bench reads it, 0440" "$(grep -qF 'mountPath: /etc/servtuner/secrets/vllm-key, readOnly: true' <<<"$rendered" && grep -qF 'secret: {secretName: "vllm-key", defaultMode: 288}' <<<"$rendered" && echo 0 || echo 1)"
rendered="$(render -f "$chart/ci/tls-off.yaml" --set runner=subprocess --set 'rbac.bench.targetNamespaces={targets}')"
check "the api may server-side apply Secrets (saved targets' keys)" "$(grep -qF 'verbs: [create, get, list, watch, update, patch, delete]' <<<"$rendered" && echo 0 || echo 1)"
check "target read role covers HPAs (snapshot)" "$(grep -qF 'horizontalpodautoscalers' <<<"$rendered" && echo 0 || echo 1)"
rendered="$(render -f "$chart/ci/tls-off.yaml" --set rbac.tuning=null --set tuning=null)"
check "values without rbac.tuning or tuning still render, granting nothing" "$(grep -qE 'servtuner-api-tuning|SERVTUNER_TUNING' <<<"$rendered" && echo 1 || echo 0)"
rendered="$(render -f "$chart/ci/tls-off.yaml")"
check "managed targets: no tuning Role and no tuning namespaces by default" "$(grep -qE 'servtuner-api-tuning|SERVTUNER_TUNING_NAMESPACES' <<<"$rendered" && echo 1 || echo 0)"
rendered="$(render -f "$chart/ci/tls-off.yaml" --set 'rbac.tuning.namespaces={servtuner-tune-a,servtuner-tune-b}' --set 'rbac.bench.targetNamespaces={servtuner-tune-a}')"
check "managed targets: the api gets a tuning Role in each listed namespace, and the api learns the list" "$([ "$(count 'name: servtuner-api-tuning$')" -eq 4 ] && grep -qF 'SERVTUNER_TUNING_NAMESPACES: "servtuner-tune-a,servtuner-tune-b"' <<<"$rendered" && echo 0 || echo 1)"
check "managed targets: the monitor mode reaches the api (podmonitor by default)" "$(grep -qF 'SERVTUNER_TUNING_MONITOR: "podmonitor"' <<<"$rendered" && echo 0 || echo 1)"
check "managed targets: Secrets are get-only in tuning namespaces (never listed or written)" "$(grep -A1 -F 'resources: [secrets]' <<<"$rendered" | grep -qF 'verbs: [get]' && echo 0 || echo 1)"
check "managed targets: bench reads each tuning namespace once, even when also a target namespace" "$([ "$(count 'name: servtuner-bench-read$')" -eq 4 ] && echo 0 || echo 1)"
rendered="$(render -f "$chart/ci/tls-off.yaml")"
check "no ingress, no config.json override by default" "$([ "$(count '^kind: Ingress$|config.json')" -eq 0 ] && echo 0 || echo 1)"
check "api data volume is group-writable for the non-root user" "$([ "$(count 'fsGroup: 10001')" -eq 1 ] && echo 0 || echo 1)"

# User auth and NetworkPolicies.
rendered="$(render -f "$chart/ci/tls-off.yaml")"
check "auth: local by default, with the setup token file" "$(grep -qF 'SERVTUNER_AUTH_MODE: "local"' <<<"$rendered" && grep -qF 'SERVTUNER_AUTH_SETUP_TOKEN_FILE: "/etc/servtuner/auth/setup-token/token"' <<<"$rendered" && echo 0 || echo 1)"
check "auth: the chart creates the setup token Secret, kept on uninstall" "$(grep -A12 'name: servtuner-setup-token$' <<<"$rendered" | grep -qF 'helm.sh/resource-policy: keep' && echo 0 || echo 1)"
check "networkPolicy: api and bench policies by default" "$([ "$(count '^kind: NetworkPolicy$')" -eq 2 ] && echo 0 || echo 1)"
check "networkPolicy: bench pods admit nothing" "$(grep -A12 'name: servtuner-bench$' <<<"$rendered" | grep -qF 'ingress: []' && echo 0 || echo 1)"
check "networkPolicy: the api admits bench pods from the runs namespace" "$(grep -qF 'matchLabels: {app.kubernetes.io/name: servtuner, app.kubernetes.io/component: bench}' <<<"$rendered" && echo 0 || echo 1)"
check "NOTES: how to read the setup token" "$(notes -f "$chart/ci/tls-off.yaml" | grep -qF 'get secret servtuner-setup-token' && echo 0 || echo 1)"
rendered="$(render -f "$chart/ci/tls-off.yaml" --set auth.setupToken.existingSecret=my-token --set auth.setupToken.key=value)"
check "auth: a named setup token Secret is mounted and not created" "$(grep -qF 'secretName: "my-token"' <<<"$rendered" && grep -qF 'setup-token/value' <<<"$rendered" && [ "$(count 'name: servtuner-setup-token$')" -eq 0 ] && echo 0 || echo 1)"
rendered="$(render -f "$chart/ci/tls-off.yaml" --set auth.mode=none)"
check "auth none: no setup token, and NOTES warns" "$([ "$(count 'setup-token')" -eq 0 ] && notes -f "$chart/ci/tls-off.yaml" --set auth.mode=none | grep -qF 'WARNING: no route checks' && echo 0 || echo 1)"
rendered="$(render -f "$chart/ci/tls-off.yaml" --set 'auth.methods={forward_header,local}' --set auth.forwardHeader.provisioning=viewer)"
check "auth forward_header: methods in order, and NOTES states the trust" "$(grep -qF 'SERVTUNER_AUTH_METHODS: "forward_header,local"' <<<"$rendered" && notes -f "$chart/ci/tls-off.yaml" --set 'auth.methods={forward_header}' | grep -qF 'must overwrite any copy' && echo 0 || echo 1)"
rendered="$(render -f "$chart/ci/tls-off.yaml" --set networkPolicy.enabled=false)"
check "networkPolicy disabled: none rendered" "$([ "$(count '^kind: NetworkPolicy$')" -eq 0 ] && echo 0 || echo 1)"
rendered="$(render -f "$chart/ci/tls-off.yaml" --set runner=subprocess)"
check "networkPolicy subprocess: only the api policy, admitting the ingress controller" "$([ "$(count '^kind: NetworkPolicy$')" -eq 1 ] && grep -qF 'kubernetes.io/metadata.name: ingress-nginx' <<<"$rendered" && echo 0 || echo 1)"
rendered="$(render -f "$chart/ci/s3-separate-namespace.yaml")"
check "networkPolicy: the bench policy is in the runs namespace" "$(grep -A4 'name: servtuner-bench$' <<<"$rendered" | grep -qF 'namespace: servtuner-runs' && echo 0 || echo 1)"

# Gateway API: HTTPRoutes instead of Ingresses; with TLS, Envoy's Backend and a BackendTLSPolicy.
gateway_caps=(--api-versions gateway.networking.k8s.io/v1 --api-versions gateway.envoyproxy.io/v1alpha1)
gateway_args=(--set gateway.enabled=true --set gateway.hostname=servtuner.example.test --set 'gateway.parentRefs[0].name=eg' --set frontend.basePath=/tools/servtuner/)
rendered="$(render -f "$chart/ci/tls-off.yaml" "${gateway_caps[@]}" "${gateway_args[@]}")"
check "gateway tls-off: two HTTPRoutes, no Ingress, no Backend" "$([ "$(count '^kind: HTTPRoute$')" -eq 2 ] && [ "$(count '^kind: (Ingress|Backend|BackendTLSPolicy)$')" -eq 0 ] && echo 0 || echo 1)"
check "gateway tls-off: the api route strips its prefix to the plain Service" "$(grep -qF 'value: "/tools/servtuner/api"' <<<"$rendered" && grep -qF 'replacePrefixMatch: /' <<<"$rendered" && grep -qF -- '- {name: servtuner-api, port: 8080}' <<<"$rendered" && echo 0 || echo 1)"
check "gateway: the NetworkPolicy admits the gateway's proxy namespace" "$(grep -qF 'kubernetes.io/metadata.name: envoy-gateway-system' <<<"$rendered" && echo 0 || echo 1)"
rendered="$(render -f "$chart/ci/mutual-tls-cert-manager.yaml" "${gateway_caps[@]}" "${gateway_args[@]}")"
check "gateway mutual-tls: the api route goes to an Envoy Backend with the ingress client certificate" "$(grep -qF -- '- {group: gateway.envoyproxy.io, kind: Backend, name: servtuner-api}' <<<"$rendered" && grep -qF 'clientCertificateRef: {kind: Secret, name: "servtuner-ingress-tls"}' <<<"$rendered" && echo 0 || echo 1)"
check "gateway mutual-tls: a BackendTLSPolicy checks the api's Service name" "$([ "$(count '^kind: BackendTLSPolicy$')" -eq 1 ] && grep -qF 'hostname: "servtuner-api.servtuner.svc"' <<<"$rendered" && echo 0 || echo 1)"
rendered="$(render -f "$chart/ci/tls-off.yaml" "${gateway_caps[@]}" "${gateway_args[@]}" --set gateway.envoy.extAuth.enabled=true --set gateway.envoy.extAuth.service.name=auth --set gateway.envoy.extAuth.service.port=8080)"
check "gateway extAuth: a SecurityPolicy on both routes forwards the user header" "$([ "$(count '^kind: SecurityPolicy$')" -eq 1 ] && grep -A1 'headersToBackend:' <<<"$rendered" | grep -qF -- '- X-Forwarded-User' && [ "$(count 'headersToExtAuth')" -eq 0 ] && echo 0 || echo 1)"
rendered="$(render -f "$chart/ci/tls-off.yaml" "${gateway_caps[@]}" "${gateway_args[@]}" --set gateway.envoy.extAuth.enabled=true --set gateway.envoy.extAuth.service.name=auth --set gateway.envoy.extAuth.service.port=8080 --set 'gateway.envoy.extAuth.headersToExtAuth={cookie}')"
check "gateway extAuth: headersToExtAuth passes chosen client headers to the auth service" "$(grep -A1 'headersToExtAuth:' <<<"$rendered" | grep -qF -- '- cookie' && echo 0 || echo 1)"

# Air-gapped installs: global.imageRegistry swaps the registry host of every image and keeps the path.
registry_args=(--set global.imageRegistry=registry.example.internal:5000/ --set postgres.cnpg.imageName=ghcr.io/cloudnative-pg/postgresql:17.6 --set 'global.imagePullSecrets[0].name=regcred' --set 'imagePullSecrets[0].name=regcred' --set 'imagePullSecrets[1].name=other')
rendered="$(render -f "$chart/ci/mutual-tls-cert-manager.yaml" "${registry_args[@]}" --set api.image.repository=servtuner/servtuner-api --set frontend.image.repository=servtuner/servtuner-frontend --set bench.image.repository=servtuner/servtuner-bench --set offline=true)"
check "registry: every rendered image is under global.imageRegistry" "$([ "$(count '^ *image: ')" -ge 3 ] && [ "$(grep -E '^ *image: ' <<<"$rendered" | grep -vc 'registry.example.internal:5000/servtuner/')" -eq 0 ] && echo 0 || echo 1)"
check "registry: bench Jobs get the rewritten bench image" "$(grep -qF 'SERVTUNER_BENCH_IMAGE: "registry.example.internal:5000/servtuner/servtuner-bench:' <<<"$rendered" && echo 0 || echo 1)"
check "registry: the Postgres image keeps its path under the registry" "$(grep -qF 'imageName: "registry.example.internal:5000/cloudnative-pg/postgresql:17.6"' <<<"$rendered" && echo 0 || echo 1)"
check "registry: the api learns the registry and offline" "$(grep -qF 'SERVTUNER_IMAGE_REGISTRY: "registry.example.internal:5000"' <<<"$rendered" && grep -qF 'SERVTUNER_OFFLINE: "true"' <<<"$rendered" && echo 0 || echo 1)"
check "registry: pull secrets merged once, for pods and bench Jobs" "$(grep -qF 'SERVTUNER_IMAGE_PULL_SECRETS: "regcred,other"' <<<"$rendered" && [ "$(count '^ *- name: regcred$')" -eq 2 ] && echo 0 || echo 1)"
rendered="$(render -f "$chart/ci/mutual-tls-cert-manager.yaml" --set api.image.digest=sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef)"
check "image digest wins over the tag" "$(grep -qF 'image: "ghcr.io/servtuner/servtuner-api@sha256:0123456789abcdef' <<<"$rendered" && echo 0 || echo 1)"
rendered="$(render -f "$chart/ci/tls-off.yaml" "${registry_args[@]}")"
check "registry: the published default keeps its path under the site registry" "$(grep -qF 'image: "registry.example.internal:5000/servtuner/servtuner-api:' <<<"$rendered" && echo 0 || echo 1)"
rendered="$(render -f "$chart/ci/tls-off.yaml")"
check "no registry: the published images, offline false, no pull secrets" "$(grep -qE 'image: \"ghcr.io/servtuner/servtuner-api:' <<<"$rendered" && grep -qF 'SERVTUNER_OFFLINE: "false"' <<<"$rendered" && [ "$(count 'imagePullSecrets:')" -eq 0 ] && echo 0 || echo 1)"
# The release's image lock covers the images the helmfile names for servtuner; the full check
# renders every chart (just images-check, needs network).
postgres_image="$(sed -n 's/^ *imageName: //p' "$here/../helmfile/values/servtuner.yaml.gotmpl")"
check "the helmfile's Postgres image is in deploy/release/images.lock" "$(grep -q "^${postgres_image}@sha256:" "$here/../release/images.lock" && echo 0 || echo 1)"
check "the servtuner chart carries no subcharts" "$(grep -q '^dependencies:' "$chart/Chart.yaml" && echo 1 || echo 0)"

# Each failure case must fail, and with its message. Header lines: "# expect: <text>" and
# optionally "# api-versions: <comma list>" to replace the default simulated capabilities.
for file in "$here"/tests/fail/*.yaml; do
  name="$(basename "$file" .yaml)"
  expected="$(sed -n 's/^# expect: //p' "$file")"
  versions=(--api-versions postgresql.cnpg.io/v1 --api-versions cert-manager.io/v1)
  override="$(sed -n 's/^# api-versions: //p' "$file")"
  [ -n "$override" ] && versions=(--api-versions "$override")
  if output="$(helm template servtuner "$chart" -n "$namespace" "${versions[@]}" -f "$file" 2>&1)"; then
    echo "FAIL fail/$name: rendered but should have failed"; failures=$((failures + 1))
  elif grep -qF -- "$expected" <<<"$output"; then
    echo "ok   fail/$name"
  else
    echo "FAIL fail/$name: expected message \"$expected\""; echo "$output" | head -5; failures=$((failures + 1))
  fi
done

[ "$failures" -eq 0 ] && echo "all chart tests passed" || { echo "$failures chart test(s) failed"; exit 1; }
