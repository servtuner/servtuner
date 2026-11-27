# servtuner's images, defined once for `just images` (local, one platform, loaded into docker) and for the release
# workflow (.github/workflows/release.yml: both platforms, pushed to ghcr.io, with provenance and an SBOM).
#
#   docker buildx bake --load                     servtuner-{bench,api,frontend,mock}:$TAG for this machine
#   VERSION=0.1.0 docker buildx bake release      the published set (the workflow adds tags, labels and --push)
#
# The meta-* targets hold each image's tags. They default to the local name; in the release workflow,
# docker/metadata-action writes files that redefine them with the ghcr.io tags and OCI labels.

variable "TAG" {
  default = "e2e" # the local tag (just images: SERVTUNER_IMAGE_TAG)
}

variable "VERSION" {
  default = "0.0.0-dev" # the release version, stamped into the binary, the api and the UI (the tag without its v)
}

variable "COMMIT" {
  default = "unknown" # the full commit, reported by the api's GET /version
}

group "default" {
  targets = ["bench", "api", "frontend", "mock"]
}

# What a release publishes. The mock is a test target and is never published.
group "release" {
  targets = ["bench", "api", "frontend"]
}

target "meta-bench" {
  tags = ["servtuner-bench:${TAG}"]
}

target "meta-api" {
  tags = ["servtuner-api:${TAG}"]
}

target "meta-frontend" {
  tags = ["servtuner-frontend:${TAG}"]
}

target "_version" {
  args = { SERVTUNER_VERSION = VERSION, SERVTUNER_COMMIT = COMMIT }
}

target "bench" {
  inherits = ["_version", "meta-bench"]
  context  = "bench"
}

target "api" {
  inherits = ["_version", "meta-api"]
  context  = "api"
  # The binary comes from the bench target, built for the same platform; the schemas from the repository.
  contexts = {
    bench-image = "target:bench"
    schema      = "schema/json"
  }
}

target "frontend" {
  inherits = ["_version", "meta-frontend"]
  context  = "frontend"
}

target "mock" {
  context = "deploy/mocks/openai-mock"
  tags    = ["servtuner-mock:${TAG}"]
}
