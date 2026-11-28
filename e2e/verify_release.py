"""Verify a published release the way a public user installs it: the Release and the registry only.

    PYTHONPATH=api uv run --project api python e2e/verify_release.py VERSION [flags]
    just verify-release VERSION [--cluster] [--airgap] [--keep]

Every step runs with an empty HOME and no registry logins, from what the GitHub Release and
ghcr.io/servtuner publish, never from this checkout's builds:

1. downloads: checksums.txt and its cosign signature, then the CLI (this machine's OS and
   architecture), the mock and the deploy bundle against it; each binary reports VERSION;
2. the registry, anonymously: every image (both platforms) and chart is signed by the release
   workflow, and each chart's version is VERSION;
3. the quickstart: the downloaded mock and CLI run a benchmark, stamped bench_version VERSION;
4. --cluster: a new kind cluster (servtuner-verify, its own kubeconfig; never another cluster), the
   deploy bundle's `helmfile -e kind sync`, the UI and the api through the Gateway, the first admin,
   GET /version, the published mock chart, a benchmark as a bench Job, then the chart from the
   registry in place of the bundle's;
5. --airgap: the air-gapped archive through `just e2e-airgap VERSION` (from a checkout at the tag).

Steps a release predates (no mock before 0.2, no kind trial in 0.1's bundle) are reported as
skipped, never as passed. Needs cosign, docker (buildx) and helm; --cluster adds kind, kubectl and
helmfile; --airgap adds what `just e2e-airgap` needs.

To test this script before a release exists, --deploy-dir and --images-tag stand in a checkout's
deploy/ and images built with `just images` for the Release's: that tests the check, not a release.
"""

import argparse
import base64
import hashlib
import json
import os
import platform
import shutil
import socket
import subprocess
import sys
import tarfile
import tempfile
import urllib.request
from contextlib import ExitStack
from dataclasses import dataclass
from pathlib import Path
from typing import Any

import httpx2
from common import REPOSITORY, TERMINAL_STATES, check, stop_process, wait_for

RELEASES = "https://github.com/servtuner/servtuner/releases/download"
REGISTRY = "ghcr.io/servtuner"
# The release workflow's keyless signing identity (RELEASING.md).
SIGNER = r"^https://github.com/servtuner/servtuner/\.github/workflows/release\.yml@refs/tags/v"
OIDC_ISSUER = "https://token.actions.githubusercontent.com"
IMAGES = ("servtuner-api", "servtuner-bench", "servtuner-frontend")
MOCK_IMAGE = "servtuner-mock"
PLATFORMS = {"linux/amd64", "linux/arm64"}
CLUSTER = "servtuner-verify"
NAMESPACE = "servtuner"
MOCK_NAMESPACE = "servtuner-mock"
GATEWAY_NAMESPACE = "envoy-gateway-system"
GATEWAY_LABEL = "gateway.envoyproxy.io/owning-gateway-name=servtuner"
# The kind environment's Gateway answers for this host (deploy/helmfile/kind-values.yaml).
GATEWAY_HOST = "localhost"
DEV_VERSION = "0.0.0-dev"
READ_BLOCK_BYTES = 1048576  # 1 MiB per read while hashing a download
ADMIN = {"username": "verify-admin", "password": "verify-release-password-0"}
INSTALL_TIMEOUT = "15m"
# After the api is Ready the Gateway takes a few seconds to route to it (503 meanwhile).
ROUTE_TIMEOUT_SECONDS = 180.0
RUN_TIMEOUT_SECONDS = 600.0
# A short benchmark: the point is that it runs and is stamped, not its numbers.
RUNSPEC: dict[str, Any] = {
    "apiVersion": "servtuner.dev/v2",
    "kind": "RunSpec",
    "name": "verify-release",
    "target": {
        "type": "openai-compatible",
        "endpoint": "",
        "model": "mock-model",
        "tls": {"enabled": False},
    },
    "engine": {"type": "loadgen"},
    "workload": {
        "api": "chat",
        "streaming": True,
        "dataset": {
            "type": "synthetic",
            "input_tokens": {"mean": 64},
            "output_tokens": {"mean": 16},
        },
    },
    "load": {"warmup": "5s", "stages": [{"rate": 5, "duration": "20s"}]},
}


@dataclass(frozen=True)
class Release:
    version: str
    work: Path  # scratch: downloads, the empty HOME, the kubeconfig
    files: frozenset[str]  # every file checksums.txt lists

    @property
    def home(self) -> Path:
        return self.work / "home"

    @property
    def ships_mock(self) -> bool:
        return any(name.startswith(f"{MOCK_IMAGE}_") for name in self.files)

    def env(self, **extra: str) -> dict[str, str]:
        """A public user's environment: an empty HOME and Docker config, no registry logins."""
        return {
            "HOME": str(self.home),
            "DOCKER_CONFIG": str(self.home / ".docker"),
            "PATH": os.environ["PATH"],
            **extra,
        }


def step(number: int, title: str) -> None:
    print(f"\n== {number}. {title}", flush=True)


def skip(reason: str) -> None:
    print(f"skip {reason}", flush=True)


def run(release: Release, *command: str, **extra: str) -> str:
    print(f"$ {' '.join(command)}", flush=True)
    result = subprocess.run(  # noqa: S603 - fixed argument lists, never a shell
        command, env=release.env(**extra), check=False, text=True, capture_output=True
    )
    if result.returncode != 0:
        raise AssertionError(
            f"{command[0]} failed ({result.returncode}):\n{result.stdout}{result.stderr}"
        )
    return result.stdout


def download(release: Release, name: str) -> Path:
    target = release.work / name
    with urllib.request.urlopen(f"{RELEASES}/v{release.version}/{name}") as response:  # noqa: S310
        target.write_bytes(response.read())
    return target


def sha256(path: Path) -> str:
    digest = hashlib.sha256()
    with path.open("rb") as stream:
        for block in iter(lambda: stream.read(READ_BLOCK_BYTES), b""):
            digest.update(block)
    return digest.hexdigest()


def free_port() -> int:
    with socket.socket() as probe:
        probe.bind(("127.0.0.1", 0))
        return int(probe.getsockname()[1])


# --- 1. downloads ---------------------------------------------------------------------------------


def verify_downloads(version: str, work: Path) -> tuple[Release, dict[str, Path]]:
    """checksums.txt signed by the release workflow, then each download against it."""
    step(1, "downloads: signed checksums, the CLI, the mock and the deploy bundle")
    probe = Release(version, work, frozenset())
    probe.home.mkdir(parents=True)
    checksums = download(probe, "checksums.txt")
    download(probe, "checksums.txt.sigstore.json")
    run(
        probe,
        "cosign",
        "verify-blob",
        str(checksums),
        "--bundle",
        str(work / "checksums.txt.sigstore.json"),
        "--certificate-identity-regexp",
        SIGNER,
        "--certificate-oidc-issuer",
        OIDC_ISSUER,
    )
    check(True, "checksums.txt is signed by the release workflow")
    expected: dict[str, str] = {}
    for line in checksums.read_text().splitlines():
        digest, name = line.split()
        expected[name] = digest
    release = Release(version, work, frozenset(expected))
    system = platform.system().lower()
    machine = {"x86_64": "amd64", "aarch64": "arm64", "arm64": "arm64"}[platform.machine().lower()]
    wanted = {
        "cli": f"servtuner_{version}_{system}_{machine}.tar.gz",
        "mock": f"{MOCK_IMAGE}_{version}_{system}_{machine}.tar.gz",
        "deploy": f"servtuner-deploy-{version}.tar.gz",
    }
    paths: dict[str, Path] = {}
    for role, name in wanted.items():
        if name not in expected and role == "mock":
            skip(f"{name}: this release predates the mock binary")
            continue
        check(name in expected, f"the release carries {name}")
        path = download(release, name)
        check(sha256(path) == expected[name], f"{name} matches checksums.txt")
        unpacked = work / role
        with tarfile.open(path) as archive:
            archive.extractall(unpacked, filter="data")
        paths[role] = unpacked
    cli = run(release, str(paths["cli"] / "servtuner"), "--version").strip()
    check(cli == f"servtuner version {version}", f"servtuner --version says {version} ({cli})")
    if "mock" in paths:
        mock = run(release, str(paths["mock"] / MOCK_IMAGE), "--version").strip()
        check(mock == version, f"servtuner-mock --version says {version} ({mock})")
    return release, paths


# --- 2. the registry ------------------------------------------------------------------------------


def cosign_verify(release: Release, reference: str) -> None:
    run(
        release,
        "cosign",
        "verify",
        reference,
        "--certificate-identity-regexp",
        SIGNER,
        "--certificate-oidc-issuer",
        OIDC_ISSUER,
    )


def verify_registry(release: Release) -> None:
    step(2, "the registry, anonymously: signed images on both platforms, signed charts")
    images = (*IMAGES, *((MOCK_IMAGE,) if release.ships_mock else ()))
    for image in images:
        reference = f"{REGISTRY}/{image}:{release.version}"
        manifest = json.loads(
            run(
                release,
                "docker",
                "buildx",
                "imagetools",
                "inspect",
                reference,
                "--format",
                "{{json .Manifest}}",
            )
        )
        platforms = {
            f"{entry['platform']['os']}/{entry['platform']['architecture']}"
            for entry in manifest.get("manifests", [])
            if entry.get("platform", {}).get("os") != "unknown"
        }
        check(
            PLATFORMS <= platforms,
            f"{image}:{release.version} pulls anonymously for {sorted(platforms)}",
        )
        cosign_verify(release, reference)
        check(True, f"{image}:{release.version} is signed by the release workflow")
    if not release.ships_mock:
        skip(f"{MOCK_IMAGE}: this release predates the published mock")
    charts = ("servtuner", *((MOCK_IMAGE,) if release.ships_mock else ()))
    for chart in charts:
        reference = f"oci://{REGISTRY}/charts/{chart}"
        shown = run(release, "helm", "show", "chart", reference, "--version", release.version)
        check(
            f"version: {release.version}" in shown.splitlines(),
            f"chart {chart} {release.version} pulls",
        )
        cosign_verify(release, f"{REGISTRY}/charts/{chart}:{release.version}")
        check(True, f"chart {chart}:{release.version} is signed by the release workflow")


# --- 3. the quickstart ----------------------------------------------------------------------------


def verify_quickstart(release: Release, paths: dict[str, Path]) -> None:
    step(3, "the quickstart: the downloaded mock and CLI run a benchmark")
    if "mock" not in paths:
        skip("the quickstart: this release predates the mock binary (docs run it with go run)")
        return
    port = free_port()
    with ExitStack() as stack:
        log = stack.enter_context((release.work / "mock.log").open("wb"))
        mock = subprocess.Popen(  # noqa: S603 - the binary just verified
            [str(paths["mock"] / MOCK_IMAGE), "--tls", "false", "--listen", f"127.0.0.1:{port}"],
            env=release.env(),
            stdout=log,
            stderr=subprocess.STDOUT,
        )
        stack.callback(stop_process, mock)
        wait_for("the mock", lambda: httpx2.get(f"http://127.0.0.1:{port}/healthz").is_success)
        spec = json.loads(json.dumps(RUNSPEC))
        spec["target"]["endpoint"] = f"http://127.0.0.1:{port}/v1"
        spec_file = release.work / "quickstart.json"
        spec_file.write_text(json.dumps(spec))
        out = release.work / "quickstart-run"
        run(
            release,
            str(paths["cli"] / "servtuner"),
            "run",
            "--spec",
            str(spec_file),
            "--out",
            str(out),
        )
    manifest = json.loads((out / "manifest.json").read_text())
    check(manifest["status"] == "succeeded", "the quickstart benchmark succeeded")
    check(
        manifest["bench_version"] == release.version,
        f"its bundle says bench_version {release.version}",
    )


# --- 4. a kind cluster ----------------------------------------------------------------------------


def deploy_dir(release: Release, paths: dict[str, Path], override: Path | None) -> Path | None:
    if override is not None:
        copy = release.work / "deploy-override" / "deploy"
        shutil.copytree(override, copy)
        return copy
    deploy = paths["deploy"] / "deploy"
    if not (deploy / "helmfile" / "manifests" / "gateway").is_dir():
        return None
    return deploy


def verify_cluster(
    release: Release,
    paths: dict[str, Path],
    override: Path | None,
    images_tag: str | None,
    keep: bool,
) -> None:
    step(4, f"a new kind cluster ({CLUSTER}): helmfile -e kind, the Gateway, a run")
    deploy = deploy_dir(release, paths, override)
    if deploy is None:
        skip(
            "the kind trial: this release's deploy bundle predates it "
            "(its docs add the issuer and Gateway by hand)"
        )
        return
    if (
        CLUSTER
        in subprocess.run(
            ["kind", "get", "clusters"], capture_output=True, text=True
        ).stdout.split()
    ):
        raise SystemExit(
            f"a kind cluster named {CLUSTER} exists; delete it first (it is never reused)"
        )
    kubeconfig = release.work / "kubeconfig"
    kube = {"KUBECONFIG": str(kubeconfig)}
    expected_version = DEV_VERSION if images_tag else release.version
    with ExitStack() as stack:
        run(
            release, "kind", "create", "cluster", "--name", CLUSTER, "--kubeconfig", str(kubeconfig)
        )
        if not keep:
            stack.callback(
                subprocess.run,
                ["kind", "delete", "cluster", "--name", CLUSTER, "--kubeconfig", str(kubeconfig)],
                check=False,
            )
        if images_tag:
            local = [
                f"servtuner-{name}:{images_tag}" for name in ("api", "bench", "frontend", "mock")
            ]
            run(release, "kind", "load", "docker-image", "--name", CLUSTER, *local)
            (deploy / "site-values.yaml").write_text(
                json.dumps(
                    {
                        name: {"image": {"repository": f"servtuner-{name}", "tag": images_tag}}
                        for name in ("api", "bench", "frontend")
                    }
                )
            )
        run(
            release,
            "helmfile",
            "-f",
            str(deploy / "helmfile.yaml.gotmpl"),
            "-e",
            "kind",
            "sync",
            **kube,
        )
        check(True, "helmfile -e kind sync installed every release")
        kubectl = ("kubectl", "--kubeconfig", str(kubeconfig))
        run(
            release,
            *kubectl,
            "wait",
            "--for=condition=Ready",
            "clusterissuer/servtuner-ca",
            "--timeout=5m",
        )
        run(
            release,
            *kubectl,
            "-n",
            NAMESPACE,
            "wait",
            "--for=condition=Programmed",
            "gateway/servtuner",
            f"--timeout={INSTALL_TIMEOUT}",
        )
        run(
            release,
            *kubectl,
            "-n",
            NAMESPACE,
            "wait",
            "--for=condition=Ready",
            "pod",
            "-l",
            "app.kubernetes.io/component=api",
            f"--timeout={INSTALL_TIMEOUT}",
        )
        check(True, "the issuer is Ready, the Gateway Programmed, the api Ready")
        service = run(
            release,
            *kubectl,
            "-n",
            GATEWAY_NAMESPACE,
            "get",
            "svc",
            "-l",
            GATEWAY_LABEL,
            "-o",
            "name",
        )
        port = free_port()
        forward = subprocess.Popen(  # noqa: S603 - fixed argument list
            [*kubectl, "-n", GATEWAY_NAMESPACE, "port-forward", service.strip(), f"{port}:80"],
            env=release.env(),
            stdout=subprocess.DEVNULL,
            stderr=subprocess.DEVNULL,
        )
        stack.callback(stop_process, forward)
        origin = f"http://{GATEWAY_HOST}:{port}"
        client = stack.enter_context(httpx2.Client(base_url=origin, headers={"Origin": origin}))
        wait_for(
            "the UI through the Gateway", lambda: client.get("/").is_success, ROUTE_TIMEOUT_SECONDS
        )
        wait_for(
            "the api through the Gateway",
            lambda: client.get("/api/healthz").is_success,
            ROUTE_TIMEOUT_SECONDS,
        )
        check(True, f"the UI and the api answer through the Gateway at {origin}")
        token = run(
            release,
            *kubectl,
            "-n",
            NAMESPACE,
            "get",
            "secret",
            "servtuner-setup-token",
            "-o",
            "jsonpath={.data.token}",
        )
        created = client.post(
            "/api/auth/setup",
            json=ADMIN,
            headers={"X-Servtuner-Setup-Token": base64.b64decode(token).decode()},
        )
        check(
            created.status_code == 201,
            f"the first admin, with the setup token ({created.status_code})",
        )
        # The session cookie is Secure: a browser sends it to http://localhost, httpx only over
        # https, so send it as the browser would.
        client.headers["Cookie"] = "; ".join(
            f"{name}={value}" for name, value in created.cookies.items()
        )
        version = client.get("/api/version").json()["version"]
        check(version == expected_version, f"GET /version says {expected_version} ({version})")
        install_mock(release, kubectl, kube, images_tag)
        spec = json.loads(json.dumps(RUNSPEC))
        spec["target"]["endpoint"] = f"http://openai-mock.{MOCK_NAMESPACE}.svc:8000/v1"
        created = client.post("/api/runs", json=spec)
        check(created.status_code == 201, f"POST /runs accepted ({created.status_code})")
        run_id = created.json()["id"]

        def finished() -> dict[str, Any] | None:
            current = client.get(f"/api/runs/{run_id}")
            body: dict[str, Any] = current.json() if current.is_success else {}
            return body if body.get("status") in TERMINAL_STATES else None

        final = wait_for(f"run {run_id}", finished, RUN_TIMEOUT_SECONDS)
        check(
            final["status"] == "succeeded", f"the benchmark ran as a bench Job ({final['status']})"
        )
        manifest = client.get(f"/api/runs/{run_id}/files/manifest.json").json()
        check(
            manifest["bench_version"] == expected_version,
            f"its bundle says bench_version {expected_version}",
        )
        if images_tag:
            skip("the chart from the registry: --images-tag has no published chart to test")
        else:
            # A cluster that runs every prerequisite installs the chart alone from the registry
            # (docs: Kubernetes): here the published chart takes over the release, same values.
            run(
                release,
                "helm",
                "--kubeconfig",
                str(kubeconfig),
                "upgrade",
                "servtuner",
                f"oci://{REGISTRY}/charts/servtuner",
                "--version",
                release.version,
                "--namespace",
                NAMESPACE,
                "--reuse-values",
                "--wait",
                f"--timeout={INSTALL_TIMEOUT}",
                **kube,
            )
            wait_for(
                "the api after the upgrade",
                lambda: client.get("/api/healthz").is_success,
                ROUTE_TIMEOUT_SECONDS,
            )
            version = client.get("/api/version").json()["version"]
            check(
                version == release.version,
                f"the registry's chart servtuner {release.version} serves {version}",
            )
        if keep:
            print(f"kept: kind cluster {CLUSTER}, kubeconfig {kubeconfig}", flush=True)


def install_mock(
    release: Release, kubectl: tuple[str, ...], kube: dict[str, str], images_tag: str | None
) -> None:
    """The published mock chart; with --images-tag, the checkout's chart and the local image."""
    if not release.ships_mock and not images_tag:
        raise AssertionError("the kind trial needs the published mock chart")
    common = [
        "-n",
        MOCK_NAMESPACE,
        "--create-namespace",
        "--set",
        "tls.enabled=false",
        "--wait",
        "--timeout",
        "5m",
    ]
    if images_tag:
        chart = [
            str(REPOSITORY / "deploy" / "mocks" / "chart"),
            "--set",
            "image.repository=servtuner-mock",
            "--set",
            f"image.tag={images_tag}",
        ]
    else:
        chart = [f"oci://{REGISTRY}/charts/{MOCK_IMAGE}", "--version", release.version]
    run(
        release,
        "helm",
        "--kubeconfig",
        kubectl[2],
        "install",
        "openai-mock",
        *chart,
        *common,
        **kube,
    )
    check(True, "the mock chart is installed")


# --- 5. air-gapped --------------------------------------------------------------------------------


def verify_airgap(release: Release) -> None:
    step(5, "air-gapped: the release's archive through just e2e-airgap")
    head = subprocess.run(
        ["git", "-C", str(REPOSITORY), "describe", "--exact-match", "--tags", "HEAD"],
        capture_output=True,
        text=True,
        check=False,
    ).stdout.strip()
    if head != f"v{release.version}":
        raise SystemExit(
            f"--airgap runs the release's own e2e: run it from a checkout at v{release.version} "
            f"(git worktree add ../servtuner-v{release.version} v{release.version}); "
            f"this one is at {head or 'no tag'}"
        )
    dist = REPOSITORY / "dist" / f"servtuner-{release.version}"
    dist.mkdir(parents=True, exist_ok=True)
    # The air-gapped set is what the release's SHA256SUMS lists (deploy/release/build.sh).
    sums = download(release, "SHA256SUMS")
    shutil.copy(sums, dist / "SHA256SUMS")
    for line in sums.read_text().splitlines():
        digest, name = line.split()
        target = dist / name
        if not target.is_file() or sha256(target) != digest:
            shutil.copy(download(release, name), target)
        check(sha256(target) == digest, f"{name} matches SHA256SUMS")
    for command in (["just", "_image-mock"], ["just", "e2e-airgap", release.version]):
        subprocess.run(
            command, cwd=REPOSITORY, check=True, env={**os.environ, "SERVTUNER_IMAGE_TAG": "e2e"}
        )


def main() -> None:
    parser = argparse.ArgumentParser(description=__doc__.split("\n\n")[0])
    parser.add_argument("version", help="the release, without its v (0.2.0)")
    parser.add_argument("--cluster", action="store_true", help="also the kind trial (step 4)")
    parser.add_argument(
        "--airgap", action="store_true", help="also the air-gapped archive (step 5)"
    )
    parser.add_argument("--keep", action="store_true", help="keep the kind cluster")
    parser.add_argument(
        "--deploy-dir", type=Path, help="testing this script: a checkout's deploy/ for the bundle's"
    )
    parser.add_argument(
        "--images-tag", help="testing this script: images from `just images` under this tag"
    )
    arguments = parser.parse_args()
    with tempfile.TemporaryDirectory(prefix="servtuner-verify-") as scratch:
        release, paths = verify_downloads(arguments.version, Path(scratch))
        verify_registry(release)
        verify_quickstart(release, paths)
        if arguments.cluster:
            verify_cluster(
                release, paths, arguments.deploy_dir, arguments.images_tag, arguments.keep
            )
        if arguments.airgap:
            verify_airgap(release)
    print(
        f"\nverify-release {arguments.version}: every step above passed or says why it was skipped",
        flush=True,
    )


if __name__ == "__main__":
    try:
        main()
    except AssertionError as failure:
        print(f"FAIL {failure}", file=sys.stderr, flush=True)
        sys.exit(1)
