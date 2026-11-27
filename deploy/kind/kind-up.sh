#!/usr/bin/env bash
# CI only (`just kind-up`): a fresh kind cluster with the prerequisites the e2e needs, at the versions
# servtuner is tested against. Refuses to run if the cluster already exists,
# so it can never touch a shared one. Needs kind, kubectl, helm and network access.
set -euo pipefail

CLUSTER_NAME="${SERVTUNER_KIND_CLUSTER:-kind}"
CERT_MANAGER_VERSION=v1.21.1
CNPG_VERSION=1.30.0
INGRESS_NGINX_VERSION=1.15.1
ENVOY_GATEWAY_VERSION=v1.10.0
READY_TIMEOUT=300s

here="$(cd "$(dirname "$0")" && pwd)"
context="kind-${CLUSTER_NAME}"

step() { echo "kind-up: $*"; }

if [ "${CI:-}" != "true" ] && [ "${SERVTUNER_KIND_UP_CONFIRM:-}" != "1" ]; then
  echo "kind-up: CI only. Set SERVTUNER_KIND_UP_CONFIRM=1 if you really want a new cluster here" >&2
  exit 1
fi

# Captured first: grep -q on a pipe can SIGPIPE `kind`, which pipefail would turn into a failure.
existing_clusters="$(kind get clusters 2>/dev/null || true)"
if grep -qx "$CLUSTER_NAME" <<<"$existing_clusters"; then
  echo "kind-up: a kind cluster named '$CLUSTER_NAME' already exists; refusing to touch it" >&2
  exit 1
fi
known_contexts="$(kubectl config get-contexts -o name 2>/dev/null || true)"
if grep -qx "$context" <<<"$known_contexts"; then
  echo "kind-up: the kube context '$context' already exists (a shared cluster?); refusing" >&2
  exit 1
fi

step "creating kind cluster '$CLUSTER_NAME'"
kind create cluster --name "$CLUSTER_NAME" --config "$here/kind-config.yaml" --wait "$READY_TIMEOUT"

# Every kubectl call names the context, so a changed current-context cannot redirect it.
kube() { kubectl --context "$context" "$@"; }

step "installing cert-manager $CERT_MANAGER_VERSION"
kube apply -f "https://github.com/cert-manager/cert-manager/releases/download/${CERT_MANAGER_VERSION}/cert-manager.yaml"
kube -n cert-manager rollout status deployment --timeout "$READY_TIMEOUT"

step "installing CloudNativePG $CNPG_VERSION"
kube apply --server-side -f "https://github.com/cloudnative-pg/cloudnative-pg/releases/download/v${CNPG_VERSION}/cnpg-${CNPG_VERSION}.yaml"
kube -n cnpg-system rollout status deployment cnpg-controller-manager --timeout "$READY_TIMEOUT"

step "installing ingress-nginx $INGRESS_NGINX_VERSION"
kube apply -f "https://raw.githubusercontent.com/kubernetes/ingress-nginx/controller-v${INGRESS_NGINX_VERSION}/deploy/static/provider/kind/deploy.yaml"
kube -n ingress-nginx rollout status deployment ingress-nginx-controller --timeout "$READY_TIMEOUT"

# Gateway API through Envoy Gateway (ingress-nginx is retired). The Backend API carries
# the client certificate of the gateway -> api hop with TLS.
step "installing Envoy Gateway $ENVOY_GATEWAY_VERSION"
helm --kube-context "$context" install eg oci://docker.io/envoyproxy/gateway-helm \
  --version "$ENVOY_GATEWAY_VERSION" -n envoy-gateway-system --create-namespace \
  --set config.envoyGateway.extensionApis.enableBackend=true
kube -n envoy-gateway-system wait deployment/envoy-gateway --for=condition=Available --timeout "$READY_TIMEOUT"
kube apply -f "$here/envoy-gateway-class.yaml"

step "applying the servtuner-ca ClusterIssuer"
# cert-manager's webhook can lag behind its Deployment; retry for a short while.
for attempt in 1 2 3 4 5 6; do
  kube apply -f "$here/../helmfile/manifests/issuer/cluster-issuer.yaml" && break
  [ "$attempt" -lt 6 ] || { echo "kind-up: the ClusterIssuer was not accepted" >&2; exit 1; }
  sleep 10
done
kube wait --for=condition=Ready clusterissuer/servtuner-ca --timeout "$READY_TIMEOUT"

step "done: context $context"
