# servtuner root Justfile. Component recipes live in <component>/Justfile.
# Toolchain pins live in .tool-versions only.

components := "schema bench api frontend deploy"
jscpd_version := `awk '$1 == "jscpd" {print $2}' .tool-versions`
zizmor_version := `awk '$1 == "zizmor" {print $2}' .tool-versions`
# One tag for every image. Not "latest": the chart's IfNotPresent pull policy must find an image loaded into kind.
image_tag := env("SERVTUNER_IMAGE_TAG", "e2e")
# The version stamped into the images (bench_version in every bundle); a release sets it from its tag.
image_version := env("SERVTUNER_VERSION", "0.0.0-dev")

default:
    @just --list

# Install toolchains and dependencies for every present component.
setup: (_each "setup")

# Regenerate generated code in every present component.
gen: (_each "gen")

# Lint every present component, then check duplication across the repo and the workflows' security.
lint: (_each "lint") jscpd large-files lint-workflows

# Run the tests of every present component.
test: (_each "test")

# Regenerate, then fail if anything changed (including untracked files), and fail when an image the
# releases render is missing from deploy/release/images.lock (images-check: needs helm, helmfile and
# the chart repositories).
gen-check: gen images-check
    @test -z "$(git status --porcelain)" || { echo "gen-check: tree changed after 'just gen':"; git status --porcelain; exit 1; }

# No tracked file over 1 MiB: build output (a 14 MB mock binary once) does not belong in git.
large-files:
    @large="$(git ls-files -z | xargs -0 -r stat --format '%s %n' | awk '$1 > 1048576')"; if [ -n "$large" ]; then echo "tracked files over 1 MiB:"; echo "$large"; exit 1; fi

# Security lint of .github (config in .github/zizmor.yml): actions pinned to commit SHAs, no dangerous triggers
# such as pull_request_target, no credentials left in checkouts. Offline without GH_TOKEN; CI sets it, which adds
# the online audits (for example that each pinned SHA belongs to the action's own repository).
lint-workflows:
    uvx zizmor@{{zizmor_version}} {{ if env("GH_TOKEN", "") == "" { "--offline" } else { "" } }} --format plain .github

# Duplication check across languages; threshold lives in .jscpd.json.
jscpd:
    npx --yes jscpd@{{jscpd_version}} --config .jscpd.json .

# Regenerate bench's vLLM knob catalog from vLLM's own parser when the pinned vLLM
# changes. Dev-time only, never in CI: it needs a vLLM checkout at the tag (git clone --branch
# v0.34.0 https://github.com/vllm-project/vllm) and a virtualenv with CPU torch and the checkout's
# requirements/common.txt, about 2.4 GB. Review the diff.
vllm-catalog vllm_src venv version:
    VLLM_TARGET_DEVICE=cpu {{venv}}/bin/python -I tools/vllm-catalog/dump.py {{vllm_src}} {{version}} > bench/internal/target/vllm_catalog.json

# Regenerate bench's SGLang knob catalog from SGLang's own field declarations when the pinned SGLang
# changes. Dev-time only, never in CI: it needs an SGLang checkout at the tag (git clone --branch
# v0.5.23 https://github.com/sgl-project/sglang). It parses the source; it imports neither SGLang nor torch.
sglang-catalog sglang_src version:
    python3 -I tools/sglang-catalog/dump.py {{sglang_src}} {{version}} > bench/internal/target/sglang_catalog.json

# Local end to end, no cluster: Postgres container + api + the real bench binary (needs docker, go, openssl).
e2e-local-mutual-tls: (_e2e-local "mutual-tls")
e2e-local-tls-off: (_e2e-local "tls-off")

# The frontend's Playwright smoke test against the real api and bench (needs docker, go, node, Chrome).
e2e-smoke:
    PYTHONPATH=api uv run --project api python e2e/smoke.py

# Local end to end of the loadgen engine against the repository's mock, through `servtuner run` (needs go only).
e2e-local-loadgen:
    cd bench && go test -tags e2e -count=1 -v ./internal/e2e/...

# Build all four images (servtuner-api, servtuner-bench, servtuner-frontend, servtuner-mock), all tagged image_tag.
images: (_bake "bench" "api" "frontend" "mock")

# Smoke-test the built images offline: schemas load, bench runs in both images (needs `just images` first).
image-test:
    e2e/test-images.sh {{image_tag}} {{image_version}}

# Cluster end to end on the kube context SERVTUNER_KUBE_CONTEXT (default kind-kind); see e2e/cluster.py.
e2e-mutual-tls: (_e2e-cluster "mutual-tls")
e2e-tls-off: (_e2e-cluster "tls-off")

# Forward-header auth on kind: ingress-nginx auth-url with a stub auth service,
# setup through the proxy, a forged header, a linked viewer, a run, and a NetworkPolicy probe.
e2e-forward-header:
    PYTHONPATH=api uv run --project api python e2e/cluster.py tls-off --forward-header --image-tag {{image_tag}}

# Through Envoy Gateway instead of ingress-nginx: HTTPRoutes, Envoy's external auth for
# forward-header auth, and with mutual TLS the gateway's client certificate and a BackendTLSPolicy name check.
# An SGLang target on kind: the mock as SGLang and one null-engine run read by bench's adapter (no load).
e2e-sglang:
    PYTHONPATH=api uv run --project api python e2e/cluster.py tls-off --sglang --image-tag {{image_tag}}

e2e-gateway-forward-header:
    PYTHONPATH=api uv run --project api python e2e/cluster.py tls-off --gateway --forward-header --image-tag {{image_tag}}

e2e-gateway-mutual-tls:
    PYTHONPATH=api uv run --project api python e2e/cluster.py mutual-tls --gateway --image-tag {{image_tag}}

# servtuner with no internet access: its own kind cluster (servtuner-airgap) and registry, egress
# blocked and proven, `helmfile -e airgap sync` from the release, priced runs, a compare and a plan.
# Needs `just release <version>` first; deletes what it made unless --keep. About 15 minutes.
e2e-airgap version *flags:
    PYTHONPATH=api uv run --project api python e2e/airgap.py {{version}} {{flags}}

# Verify a published release the way a public user installs it (e2e/verify_release.py): downloads, signatures,
# the quickstart; --cluster adds a kind trial from the deploy bundle, --airgap the archive (from a checkout at the tag).
verify-release version *flags:
    PYTHONPATH=api uv run --project api python e2e/verify_release.py {{version}} {{flags}}

# The managed vLLM from the UI on kind (Playwright, needs node and Chrome): add the mock as a
# managed vLLM, deploy, read its log, redeploy, benchmark, tune and delete it. About 30 minutes.
e2e-managed-ui:
    PYTHONPATH=api uv run --project api python e2e/cluster.py tls-off --runner kubernetes --tuning --ui --image-tag {{image_tag}}

# Install servtuner on the kind cluster for manual testing and leave it running (without TLS, real load
# engine, the mock and servtuner's Bifrost as targets). Reachable from the LAN at http://<host>/servtuner/ through
# kind's host port 80: on the other machine, map <host> to this machine's IP in /etc/hosts. Prints the
# cleanup command at the end.
deploy-local host="servtuner.localtest.me":
    SERVTUNER_E2E_HOST={{host}} PYTHONPATH=api uv run --project api python e2e/cluster.py tls-off --image-tag {{image_tag}} --load --keep
    @echo "servtuner: http://{{host}}/servtuner/"

# The image lock: render every release in deploy/helmfile.yaml.gotmpl, pin each image by digest
# (linux/amd64 unless --platform), and write deploy/hauler/. Run after changing a chart version or an
# image value. Needs helm, helmfile, docker buildx and network access.
images-lock *flags:
    python3 deploy/release/images.py lock {{flags}}

# Fail when a rendered image is missing from deploy/release/images.lock (or the lock lists one no release renders).
images-check:
    python3 deploy/release/images.py check

# A release for installs with or without internet access: dist/servtuner-<version>/ with the
# core Hauler archive, SHA256SUMS, THIRD_PARTY_NOTICES and INSTALL.md; --with-vllm adds the vLLM archive, --with-sglang the SGLang one,
# --with-flashinfer [--sm sm90a ...] FlashInfer's GPU kernels for a host's vLLM or a custom image.
release version *flags:
    deploy/release/build.sh {{version}} {{flags}}

# A model archive for managed vLLM's model_source pvc: a Hugging Face cache directory and the
# model's licence, refused unless the licence allows redistribution (INSTALL.md, "Models").
model-archive name cache_dir license:
    deploy/release/model.sh {{name}} {{cache_dir}} {{license}}

# CI only: a fresh kind cluster with cert-manager, CloudNativePG, ingress-nginx and the ClusterIssuer.
kind-up:
    deploy/kind/kind-up.sh

# The images are defined once, in docker-bake.hcl; these build them for this machine and load them into docker.
[private]
_bake +targets:
    TAG={{image_tag}} VERSION={{image_version}} COMMIT="$(git rev-parse HEAD)" docker buildx bake --load {{targets}}

[private]
_image-bench: (_bake "bench")

# The api image takes the bench binary from the bench target; bench is loaded too, for bench Jobs.
[private]
_image-api: (_bake "bench" "api")

[private]
_image-frontend: (_bake "frontend")

[private]
_image-mock: (_bake "mock")

[private]
_e2e-cluster mode:
    PYTHONPATH=api uv run --project api python e2e/cluster.py {{mode}} --image-tag {{image_tag}}

[private]
_e2e-local mode:
    PYTHONPATH=api uv run --project api python e2e/local.py {{mode}}

# Run <recipe> in each component that has a Justfile, in order.
[private]
_each recipe:
    #!/usr/bin/env bash
    set -euo pipefail
    for c in {{components}}; do
        [ -f "$c/Justfile" ] || continue
        just --justfile "$c/Justfile" --working-directory "$c" {{recipe}}
    done
