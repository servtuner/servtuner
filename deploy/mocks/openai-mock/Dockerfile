FROM golang:1.27.3 AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY *.go ./
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /openai-mock .

FROM gcr.io/distroless/static:nonroot
COPY --from=build /openai-mock /openai-mock
# The same binary as `vllm serve ...`: it emulates vLLM when invoked under that name (vllm.go), so a managed
# target can run this image on kind.
COPY --from=build /openai-mock /usr/local/bin/vllm
USER nonroot:nonroot
EXPOSE 8000
ENTRYPOINT ["/openai-mock"]
