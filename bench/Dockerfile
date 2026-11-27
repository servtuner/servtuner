# Build stage: the Go version matches .tool-versions (go 1.27.3). It runs on the build machine's own platform and
# cross-compiles for the target (TARGETOS/TARGETARCH, set by buildx), so a multi-platform build never emulates Go.
FROM --platform=$BUILDPLATFORM golang:1.27.3 AS build
ARG TARGETOS TARGETARCH
# The release version, recorded in every bundle as bench_version (docker-bake.hcl sets it from the tag).
ARG SERVTUNER_VERSION=0.0.0-dev
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH go build -trimpath \
    -ldflags="-s -w -X github.com/servtuner/servtuner/bench/internal/version.Version=${SERVTUNER_VERSION}" \
    -o /servtuner ./cmd/servtuner

# inference-perf venv: its own virtualenv, built by uv from the
# hash-locked requirements, on the same Python image the runtime stage uses so the venv's
# interpreter links resolve there. 0.7.0 needs Python >= 3.12 and pins transformers exactly.
FROM ghcr.io/astral-sh/uv:0.13.10 AS uv

FROM python:3.12.15-slim-trixie AS inference-perf
COPY --from=uv /uv /usr/local/bin/uv
ENV UV_LINK_MODE=copy UV_COMPILE_BYTECODE=1
COPY internal/engine/inferenceperf/requirements.txt /requirements.txt
RUN uv venv /opt/inference-perf && \
    uv pip install --python /opt/inference-perf/bin/python --require-hashes --no-deps -r /requirements.txt

# Runtime stage: the slim Python image has sh, cat and the system CAs (tls.caBundle's init
# container needs them) and the interpreter the venv links to. The two parts are
# separate layers; the venv comes first because it changes far less often than bench.
FROM python:3.12.15-slim-trixie
COPY --from=inference-perf /opt/inference-perf /opt/inference-perf
COPY --from=build /servtuner /usr/local/bin/servtuner
# Where a tokenizer download lands for the non-root user (the home directory is not writable).
ENV HF_HOME=/tmp/huggingface
USER 10001:10001
ENTRYPOINT ["/usr/local/bin/servtuner"]
