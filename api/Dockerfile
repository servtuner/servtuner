# Built by docker-bake.hcl (just images, and the release). Two named contexts:
# - bench-image: the bench image, so the binary is built once (bench/Dockerfile);
# - schema: the repo's schema/json, so the image holds the schemas without a copy in the tree (the api
#   validates every RunSpec against them).
# The api launches bench with the SubprocessRunner (api/CLAUDE.md "Runners").
FROM ghcr.io/astral-sh/uv:0.13.6 AS uv

# Build stage: the Python version matches .tool-versions (python 3.14). The runtime stage uses
# the same base image, so the virtualenv's interpreter links resolve there.
FROM python:3.14.10-slim-trixie AS build
COPY --from=uv /uv /usr/local/bin/uv
WORKDIR /app
ENV UV_PROJECT_ENVIRONMENT=/opt/venv UV_COMPILE_BYTECODE=1 UV_LINK_MODE=copy
COPY pyproject.toml uv.lock ./
RUN uv sync --locked --no-dev --no-install-project
COPY src ./src
RUN uv sync --locked --no-dev --no-editable

# Runtime stage. The slim image has sh, cat and the system CAs, which tls.caBundle's init
# container (this same image) needs to build the combined bundle.
FROM python:3.14.10-slim-trixie
# The release version and commit (docker-bake.hcl sets them; GET /version reports them).
ARG SERVTUNER_VERSION=0.0.0-dev
ARG SERVTUNER_COMMIT=unknown
ENV SERVTUNER_VERSION=$SERVTUNER_VERSION SERVTUNER_COMMIT=$SERVTUNER_COMMIT
COPY --from=build /opt/venv /opt/venv
COPY --from=bench-image /usr/local/bin/servtuner /usr/local/bin/servtuner
COPY --from=schema . /usr/share/servtuner/schema
# SERVTUNER_BENCH_BINARY is how the SubprocessRunner finds bench (deploy/helm/servtuner/README.md).
ENV PATH=/opt/venv/bin:$PATH SERVTUNER_BENCH_BINARY=/usr/local/bin/servtuner \
    SERVTUNER_SCHEMA_DIR=/usr/share/servtuner/schema
USER 10001:10001
EXPOSE 8080
ENTRYPOINT ["servtuner-api"]
