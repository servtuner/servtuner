# Releasing servtuner

A release is a tag on `main`. Pushing it runs `.github/workflows/release.yml`, which publishes everything; nothing
is built or uploaded by hand.

| Published | Where |
|---|---|
| `servtuner-api`, `servtuner-bench`, `servtuner-frontend`, `servtuner-mock` (linux/amd64, linux/arm64; provenance and SBOM attached; cosign-signed) | `ghcr.io/servtuner/<image>:<version>`, also `:<major>.<minor>` and, for the newest stable release, `:latest` |
| The Helm charts (cosign-signed): servtuner, and the mock target | `oci://ghcr.io/servtuner/charts/servtuner` and `charts/servtuner-mock`, version = the release |
| The `servtuner` CLI and the `servtuner-mock` target for linux and darwin, amd64 and arm64 | the GitHub Release |
| The air-gapped archive, the deploy bundle, `SHA256SUMS`, `THIRD_PARTY_NOTICES`, `INSTALL.md` | the GitHub Release |
| `checksums.txt` over every Release file, and its cosign bundle `checksums.txt.sigstore.json` | the GitHub Release |

Every binary and image reports the release version (`servtuner --version`, `servtuner-mock --version`, and
`bench_version` in every run bundle).

## Versions

- `vX.Y.Z` is a release. `vX.Y.Z-rc.N` (any `-` suffix) is a pre-release: its images and chart get exactly that
  version, the GitHub Release is marked pre-release, and `:latest` and `:X.Y` do not move.
- `:latest` moves only to the newest stable release. A patch to an older line (`v0.2.4` after `v0.3.0`) updates
  `:0.2` and leaves `:latest` on 0.3.
- The documentation site follows the same tags (servtuner/docs, `README.md`, "Versions and releases").

## Before the first release (once)

- **Tag protection:** add a repository ruleset for tags matching `v*` that allows only maintainers to create
  them and blocks updates and deletions. A pushed tag publishes.
- **Packages:** after the first release, an organization owner makes each package public (Organization →
  Packages → each `servtuner-*` image and chart → Package settings → Change visibility). Until then, only members
  can pull. A package published for the first time (such as `servtuner-mock` and `charts/servtuner-mock`) starts
  private too.

## Steps

1. **Try it.** Run the workflow by hand (Actions → release → Run workflow) on `main`. That dry run builds the
   images for both platforms, packages the chart and builds the CLI, and publishes nothing.
2. **Release candidate.** Tag and push:

   ```sh
   git switch main && git pull --ff-only
   git tag -a v0.1.0-rc.1 -m "servtuner 0.1.0-rc.1"
   git push origin v0.1.0-rc.1
   ```

   The workflow reruns every CI gate on the tagged commit, then publishes. Make the packages public (a package
   published for the first time starts private), then verify the candidate the way a public user installs it,
   from what was published only:

   ```sh
   just verify-release 0.1.0-rc.1 --cluster
   git worktree add ../servtuner-v0.1.0-rc.1 v0.1.0-rc.1      # the air-gapped run uses the tag's own e2e
   (cd ../servtuner-v0.1.0-rc.1 && just verify-release 0.1.0-rc.1 --airgap)
   ```

   Each step passes or says why it was skipped. A failure is fixed on `main`, in the app, the chart or the docs,
   and goes into the next candidate.
3. **Release.** When the candidate is good, tag the same commit `v0.1.0`, push it, and run
   `just verify-release 0.1.0 --cluster` before announcing it.
4. **Docs.** In servtuner/docs: `npm run cut-version -- <servtuner checkout> v0.1.0`, review, push the branch.

A tag that is not on `main` is refused. If a step fails, fix it on `main` and tag the next pre-release; never
move or reuse a pushed tag.

## Verify a release

`just verify-release <version>` (`e2e/verify_release.py`) runs these checks and more with an empty `HOME` and
no registry logins: the signed checksums and every download, the CLI's and the mock's versions, every image (both
platforms) and chart signed, and a quickstart run. `--cluster` adds a new kind cluster (`servtuner-verify`, its own
kubeconfig) installed from the deploy bundle, with the UI, the API, `GET /version` and a benchmark through the
Gateway; `--airgap` adds the air-gapped archive. By hand:

```sh
identity='^https://github\.com/servtuner/servtuner/\.github/workflows/release\.yml@refs/tags/v'
issuer=https://token.actions.githubusercontent.com

cosign verify ghcr.io/servtuner/servtuner-api:0.1.0 --certificate-identity-regexp "$identity" --certificate-oidc-issuer "$issuer"
cosign verify ghcr.io/servtuner/charts/servtuner:0.1.0 --certificate-identity-regexp "$identity" --certificate-oidc-issuer "$issuer"
cosign verify-blob checksums.txt --bundle checksums.txt.sigstore.json \
  --certificate-identity-regexp "$identity" --certificate-oidc-issuer "$issuer"
sha256sum --check --ignore-missing checksums.txt
docker buildx imagetools inspect ghcr.io/servtuner/servtuner-api:0.1.0 --format '{{json .Provenance}}'
```

The air-gapped archive carries the images' signatures and attestations, so a site can verify them after
`hauler store copy`, with no internet access.

## How it is built

- `docker-bake.hcl` defines the images, for `just images` and the release alike.
- `.goreleaser.yaml` builds the CLI and creates the Release.
- `deploy/release/build.sh --from-registry` builds the air-gapped archive from the images just pushed, verifying
  each signature as it is added.
- `.tool-versions` pins helm, helmfile, hauler, cosign and GoReleaser; `.github/scripts/install-release-tools.sh`
  installs the first three, checksum-verified.
