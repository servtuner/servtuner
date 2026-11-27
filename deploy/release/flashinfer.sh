#!/usr/bin/env bash
# FlashInfer's precompiled GPU kernels for installs with no internet access (INSTALL.md,
# "GPU kernels"):
#
#   deploy/release/flashinfer.sh OUT_DIR [SM...]      e.g. sm80 sm90a (default: every architecture)
#
# FlashInfer downloads a kernel it does not find installed. These are the wheels its own
# `flashinfer download-kernels` installs: flashinfer-cubin (the cubins) and flashinfer-jit-cache-<sm>
# (prebuilt JIT modules, one wheel per GPU architecture), at the versions in
# deploy/helmfile/env/common.yaml. Writes OUT_DIR/servtuner-flashinfer-<version>-<cuda>.tar.zst, a
# Hauler archive with one file holding the wheels, their SHA256SUMS and how to install them.
# Needs uv and hauler, and network access to flashinfer.ai.
set -euo pipefail

out="${1:?usage: flashinfer.sh OUT_DIR [SM...]}"
shift
root="$(cd "$(dirname "$0")/../.." && pwd)"
value() { awk -v key="$1" '/^versions:/ {inside = 1; next} /^[^ #]/ {inside = 0} inside && $1 == key":" {print $2}' "$root/deploy/helmfile/env/common.yaml"; }
version="$(value flashinfer)"
cuda="$(value flashinferCuda)"
# The architectures flashinfer.ai publishes jit-cache wheels for (its /whl/<cuda>/ index).
architectures=("${@:-sm80 sm89 sm90a sm100a sm103a sm110a sm120f sm121a}")
read -r -a architectures <<<"${architectures[*]}"
index="https://flashinfer.ai/whl"
name="servtuner-flashinfer-$version-$cuda"
work="$(mktemp -d)"
trap 'rm -rf "$work"' EXIT
wheels="$work/flashinfer/wheels"
mkdir -p "$wheels" "$out"

download() { uv run --no-project --with pip python -m pip download --quiet --no-deps --only-binary=:all: -d "$wheels" --index-url "$@"; }
echo "flashinfer: flashinfer-cubin $version"
download "$index" "flashinfer-cubin==$version"
for sm in "${architectures[@]}"; do
  echo "flashinfer: flashinfer-jit-cache-$sm $version+$cuda"
  download "$index/$cuda" "flashinfer-jit-cache-$sm==$version+$cuda"
done
(cd "$wheels" && sha256sum -- *.whl > SHA256SUMS)
cat > "$work/flashinfer/README.txt" <<README
FlashInfer $version kernels for CUDA ${cuda#cu} (${architectures[*]}): install them where vLLM runs, so
FlashInfer never downloads one.

  sha256sum -c wheels/SHA256SUMS
  # into vLLM's Python environment (a host target's venv, or a custom image's Dockerfile):
  pip install --no-index --no-deps wheels/flashinfer_cubin-*.whl wheels/flashinfer_jit_cache_<sm>-*.whl

Pick the jit-cache wheel for your GPU: sm80 covers Ampere (A100, RTX 30xx), sm89 Ada (L4, L40S,
RTX 40xx), sm90a Hopper (H100, H200), sm100a/sm103a Blackwell (B200, B300), sm120f RTX 50xx and RTX
PRO Blackwell. The stock vllm-openai image already carries all of them.
README
tar -czf "$work/$name.tar.gz" -C "$work" flashinfer
hauler store -s "$work/store" add file "$work/$name.tar.gz"
hauler store -s "$work/store" save -f "$out/$name.tar.zst"
echo "flashinfer: $out/$name.tar.zst"
