#!/usr/bin/env bash
# Copy the kind kubelet's serving CA into the ConfigMap the servtuner-cadvisor scrape
# job verifies against (deploy/prometheus/servtuner-cadvisor.values.yaml). The kubelet
# writes its certificate and then its CA to kubelet.crt; the CA is public.
set -euo pipefail
node="${1:-kind-control-plane}"
namespace="${2:-monitoring}"
context="${KUBE_CONTEXT:-kind-kind}"
workdir="$(mktemp -d)"
trap 'rm -rf "$workdir"' EXIT
docker exec "$node" cat /var/lib/kubelet/pki/kubelet.crt \
  | awk '/BEGIN CERTIFICATE/{n++} n==2' >"$workdir/ca.crt"
openssl x509 -in "$workdir/ca.crt" -noout -subject -enddate
kubectl --context "$context" -n "$namespace" create configmap servtuner-kubelet-ca \
  --from-file=ca.crt="$workdir/ca.crt" --dry-run=client -o yaml \
  | kubectl --context "$context" apply -f -
