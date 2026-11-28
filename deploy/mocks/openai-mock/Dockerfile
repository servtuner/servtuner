FROM golang:1.27.3 AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY *.go ./
# The release version (docker-bake.hcl sets it from the tag): openai-mock --version.
ARG SERVTUNER_VERSION=0.0.0-dev
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w -X main.version=${SERVTUNER_VERSION}" -o /openai-mock .

FROM gcr.io/distroless/static:nonroot
COPY --from=build /openai-mock /openai-mock
# The same binary as `vllm serve ...`: it emulates vLLM when invoked under that name (vllm.go), so a managed
# target can run this image on kind.
COPY --from=build /openai-mock /usr/local/bin/vllm
# And as `python3 -m sglang.launch_server ...`: a managed SGLang (sglang_serve.go).
COPY --from=build /openai-mock /usr/local/bin/python3
USER nonroot:nonroot
EXPOSE 8000
ENTRYPOINT ["/openai-mock"]
