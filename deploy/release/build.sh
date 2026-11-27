#!/usr/bin/env bash
# Build a servtuner release:
#
#   deploy/release/build.sh VERSION [--with-vllm] [--with-flashinfer [--sm SM]...]
#                               [--from-registry REGISTRY [--verify-identity REGEX]]
#
# By default servtuner's images are built here (just _image-api _image-frontend _image-mock). With --from-registry
# they are the published ones instead, REGISTRY/servtuner-{api,bench,frontend,mock}:VERSION (the release workflow passes
# ghcr.io/servtuner after pushing them), so the archive holds exactly what a connected install pulls, with its
# signatures and attestations. --verify-identity checks each image's cosign keyless signature as it is added: the
# signing workflow's certificate identity must match REGEX (GitHub Actions' OIDC issuer).
#
# writes dist/servtuner-VERSION/:
#   servtuner-VERSION.tar.zst          the core Hauler archive: servtuner's images, every image in
#                                   deploy/release/images.lock [core], the deploy bundle and bench
#   servtuner-vllm-VERSION.tar.zst     with --with-vllm: the managed vLLM images ([vllm])
#   servtuner-flashinfer-*.tar.zst     with --with-flashinfer: FlashInfer's GPU kernels as wheels, for a
#                                   host's vLLM or a custom image (flashinfer.sh; --sm narrows it)
#   servtuner-deploy-VERSION.tar.gz   the deploy bundle (also inside the core archive)
#   SHA256SUMS, THIRD_PARTY_NOTICES, INSTALL.md
#
# The deploy bundle (hauler file servtuner-deploy-VERSION.tar.gz) holds deploy/helmfile.yaml.gotmpl, its
# environments and values, the servtuner chart and every prerequisite chart file, so the airgap
# environment installs with no chart repository. Needs docker, helm, hauler and network access.
set -euo pipefail

usage="usage: build.sh VERSION [--with-vllm] [--with-flashinfer [--sm SM]...] [--from-registry REGISTRY [--verify-identity REGEX]]"
version="${1:?$usage}"
shift
with_vllm=""
with_flashinfer=""
architectures=()
from_registry=""
verify_identity=""
while [ $# -gt 0 ]; do
  case "$1" in
    --with-vllm) with_vllm="--with-vllm" ;;
    --with-flashinfer) with_flashinfer=1 ;;
    --sm) architectures+=("${2:?--sm needs an architecture, e.g. sm90a}"); shift ;;
    --from-registry) from_registry="${2:?--from-registry needs a registry, e.g. ghcr.io/servtuner}"; shift ;;
    --verify-identity) verify_identity="${2:?--verify-identity needs a regular expression}"; shift ;;
    *) echo "release: unknown option $1" >&2; exit 2 ;;
  esac
  shift
done
root="$(cd "$(dirname "$0")/../.." && pwd)"
deploy="$root/deploy"
out="$root/dist/servtuner-$version"
work="$(mktemp -d)"
trap 'rm -rf "$work"' EXIT

step() { echo "release: $*"; }
# A pinned chart version from the versions: block of the helmfile's common environment.
value() { awk -v key="$1" '/^versions:/ {inside = 1; next} /^[^ #]/ {inside = 0} inside && $1 == key":" {print $2}' "$deploy/helmfile/env/common.yaml"; }

[[ "$version" =~ ^[0-9]+\.[0-9]+\.[0-9]+([.-][0-9A-Za-z.-]+)?$ ]] || { echo "release: $version is not a version" >&2; exit 2; }
rm -rf "$out" && mkdir -p "$out"

step "checking the image lock against the charts"
python3 "$deploy/release/images.py" check

# The archive's platform is the image lock's (each [core] digest is that platform's manifest).
platform="$(awk '/^# Platform:/ {print $3; exit}' "$deploy/release/images.lock")"
[ -n "$platform" ] || { echo "release: images.lock names no platform" >&2; exit 2; }
[ -z "$verify_identity" ] || [ -n "$from_registry" ] || { echo "release: --verify-identity needs --from-registry" >&2; exit 2; }
if [ -n "$from_registry" ]; then
  step "using the published images $from_registry/servtuner-*:$version ($platform)"
  docker pull --quiet --platform "$platform" "$from_registry/servtuner-bench:$version" >/dev/null
  bench_image="$from_registry/servtuner-bench:$version"
else
  step "building servtuner's images as $version"
  (cd "$root" && SERVTUNER_IMAGE_TAG="$version" SERVTUNER_VERSION="$version" just _image-api _image-frontend _image-mock)
  bench_image="servtuner-bench:$version"
fi

step "the deploy bundle"
bundle="$work/deploy"
mkdir -p "$bundle/deploy/charts" "$bundle/deploy/helm"
cp "$deploy/helmfile.yaml.gotmpl" "$bundle/deploy/"
cp -r "$deploy/helmfile" "$bundle/deploy/"
cp -r "$deploy/helm/servtuner" "$bundle/deploy/helm/"
# The mock target's chart, so an air-gapped site can try servtuner without GPUs (its image is in the core archive).
mkdir -p "$bundle/deploy/mocks"
cp -r "$deploy/mocks/chart" "$bundle/deploy/mocks/"
sed -i "s/^version: .*/version: $version/; s/^appVersion: .*/appVersion: \"$version\"/" "$bundle/deploy/mocks/chart/Chart.yaml"
sed -i "s/^servtunerVersion: .*/servtunerVersion: $version/" "$bundle/deploy/helmfile/env/common.yaml"
# The chart's version is the release's too, so helm list shows servtuner-<version>.
sed -i "s/^version: .*/version: $version/; s/^appVersion: .*/appVersion: \"$version\"/" "$bundle/deploy/helm/servtuner/Chart.yaml"
helm pull cert-manager --version "$(value certManager)" -d "$bundle/deploy/charts" --repo https://charts.jetstack.io
helm pull cloudnative-pg --version "$(value cnpg)" -d "$bundle/deploy/charts" --repo https://cloudnative-pg.github.io/charts
helm pull kube-prometheus-stack --version "$(value prometheus)" -d "$bundle/deploy/charts" --repo https://prometheus-community.github.io/helm-charts
helm pull oci://docker.io/envoyproxy/gateway-helm --version "$(value envoyGateway)" -d "$bundle/deploy/charts"
tar -czf "$work/servtuner-deploy-$version.tar.gz" -C "$bundle" deploy

step "bench, the static binary (launcher hosts, the subprocess runner)"
container="$(docker create --platform "$platform" "$bench_image")"
docker cp "$container:/usr/local/bin/servtuner" "$work/servtuner-linux-amd64"
docker rm "$container" >/dev/null

images() { awk -v section="[$1]" '/^\[/ {current = $0; next} current == section && NF && !/^#/' "$deploy/release/images.lock"; }

step "the core archive"
store="$work/core-store"
for name in api bench frontend mock; do
  if [ -n "$from_registry" ]; then
    verify=()
    [ -z "$verify_identity" ] || verify=(--certificate-identity-regexp "$verify_identity"
      --certificate-oidc-issuer https://token.actions.githubusercontent.com)
    hauler store -s "$store" add image "$from_registry/servtuner-$name:$version" --platform "$platform" \
      "${verify[@]}" --rewrite "servtuner/servtuner-$name:$version"
  else
    hauler store -s "$store" add image "servtuner-$name:$version" --local --rewrite "servtuner/servtuner-$name:$version"
  fi
done
hauler store -s "$store" sync -f "$deploy/hauler/core.yaml"
hauler store -s "$store" add file "$work/servtuner-deploy-$version.tar.gz"
hauler store -s "$store" add file "$work/servtuner-linux-amd64"
hauler store -s "$store" save -f "$out/servtuner-$version.tar.zst"

if [ "$with_vllm" = "--with-vllm" ]; then
  step "the vLLM archive"
  hauler store -s "$work/vllm-store" sync -f "$deploy/hauler/vllm.yaml"
  hauler store -s "$work/vllm-store" save -f "$out/servtuner-vllm-$version.tar.zst"
fi

if [ -n "$with_flashinfer" ]; then
  step "the FlashInfer kernel archive"
  "$deploy/release/flashinfer.sh" "$out" "${architectures[@]}"
fi

step "notices, install guide and checksums"
{
  cat "$deploy/release/THIRD_PARTY_NOTICES.head"
  echo "Exact images"
  images core | sed 's/^/  /'
  [ "$with_vllm" = "--with-vllm" ] && images vllm | sed 's/^/  /'
  if [ -n "$with_flashinfer" ]; then
    echo
    echo "FlashInfer kernel wheels (the optional kernel archive)"
    echo "  flashinfer-cubin, flashinfer-jit-cache-<sm>   Apache-2.0   https://github.com/flashinfer-ai/flashinfer"
  fi
  true
} > "$out/THIRD_PARTY_NOTICES"
cp "$root/INSTALL.md" "$out/"
# The deploy bundle also on its own: an install with internet access needs only it and the published images.
cp "$work/servtuner-deploy-$version.tar.gz" "$out/"
(cd "$out" && sha256sum -- *.tar.zst *.tar.gz THIRD_PARTY_NOTICES INSTALL.md > SHA256SUMS)
step "done: $out"
ls -la "$out"
