#!/usr/bin/env python3
"""The image lock: every image servtuner's releases render, pinned by digest.

    deploy/release/images.py lock     render every release, resolve each image's digest, write images.lock
    deploy/release/images.py check    render again; fail when an image is missing from the lock or stale

The releases are deploy/helmfile.yaml.gotmpl's, rendered in the kind environment (public registries,
every prerequisite on). Images reach the lock three ways: an `image:` or `imageName:` field (including
the ones inside an operator's config and CNPG's Cluster), a flag that names one (prometheus-operator's config reloader, cert-manager's
ACME solver), or EXTRA below (images a controller creates that no chart renders). servtuner's own images
are built by the release, so the lock names them without a digest. The vLLM images are the optional
vLLM archive's, never the core one's.

Needs helm, helmfile and network access to the chart repositories; `lock` also needs docker buildx to
read digests. Standard library only.
"""

import argparse
import json
import re
import subprocess
import sys
from pathlib import Path

DEPLOY = Path(__file__).resolve().parents[1]
LOCK = DEPLOY / "release" / "images.lock"
HAULER = DEPLOY / "hauler"
CHART = DEPLOY / "helm" / "servtuner" / "Chart.yaml"

# Capabilities the servtuner chart checks for (CNPG, cert-manager, Gateway API, Envoy Gateway); helm
# template has no cluster to ask.
API_VERSIONS = (
    "postgresql.cnpg.io/v1",
    "cert-manager.io/v1",
    "gateway.networking.k8s.io/v1",
    "gateway.envoyproxy.io/v1alpha1",
    "monitoring.coreos.com/v1",
)
IMAGE_FIELD = re.compile(r"""^\s*(?:-\s+)?(?:image|imageName):\s*["']?([^"'\s]+)["']?\s*$""", re.MULTILINE)
IMAGE_FLAGS = re.compile(
    r"""--(?:prometheus-config-reloader|acme-http01-solver-image)=["']?([^"'\s,]+)"""
)
# servtuner's own images: published as ghcr.io/servtuner/servtuner-*, under servtuner/servtuner-* in an archive.
SERVTUNER_OWN = re.compile(r"^(?:(?:ghcr\.io/)?servtuner/)?servtuner-(api|bench|frontend):")
# The optional vLLM archive: the managed vLLM images the UI offers (frontend managed-targets/form.ts).
VLLM = ("docker.io/vllm/vllm-openai:v0.34.0", "docker.io/vllm/vllm-openai-cpu:v0.34.0-x86_64")
# The optional SGLang archive: the managed SGLang image (its slimmer runtime variant, for serving).
SGLANG = ("docker.io/lmsysorg/sglang:v0.5.23-runtime",)
EXTRA: tuple[str, ...] = ()


def render() -> str:
    args = " ".join(f"--api-versions {version}" for version in API_VERSIONS)
    result = subprocess.run(
        ["helmfile", "-f", str(DEPLOY / "helmfile.yaml.gotmpl"), "-e", "kind", "template", "--args", args],
        check=True,
        capture_output=True,
        text=True,
    )
    return result.stdout


def normalized(reference: str) -> str:
    """The reference with its registry spelled out, the way the lock and Hauler name it."""
    first = reference.split("/", 1)[0]
    if "/" not in reference or not ("." in first or ":" in first or first == "localhost"):
        reference = "docker.io/" + (reference if "/" in reference else "library/" + reference)
    return reference


def rendered_images(manifests: str) -> tuple[set[str], set[str]]:
    """(third-party images, servtuner's own) in the rendered manifests."""
    found = set(IMAGE_FIELD.findall(manifests)) | set(IMAGE_FLAGS.findall(manifests)) | set(EXTRA)
    own = {image for image in found if SERVTUNER_OWN.match(image)}
    return {normalized(image) for image in found - own}, own


def without_digest(reference: str) -> str:
    return reference.split("@", 1)[0]


def digest_of(reference: str, platform: str) -> str:
    """The digest of the image's manifest for this platform: one platform's layers, not every
    platform's (a multi-platform index made the archive about five times larger). A reference that
    already names a digest is resolved through it."""
    result = subprocess.run(
        ["docker", "buildx", "imagetools", "inspect", reference, "--format", "{{json .Manifest}}"],
        check=True,
        capture_output=True,
        text=True,
    )
    manifest = json.loads(result.stdout)
    if "manifests" not in manifest:
        return str(manifest["digest"])
    os_name, architecture = platform.split("/", 1)
    for child in manifest["manifests"]:
        found = child.get("platform", {})
        if (found.get("os"), found.get("architecture")) == (os_name, architecture):
            return str(child["digest"])
    raise SystemExit(f"images.py: {reference} has no {platform} image")


def app_version() -> str:
    match = re.search(r'^appVersion:\s*"?([^"\s]+)"?', CHART.read_text(), re.MULTILINE)
    assert match, f"{CHART} has no appVersion"
    return match.group(1)


def read_lock() -> dict[str, list[str]]:
    sections: dict[str, list[str]] = {}
    current = None
    for line in LOCK.read_text().splitlines():
        if line.startswith("[") and line.endswith("]"):
            current = sections.setdefault(line[1:-1], [])
        elif line.strip() and not line.startswith("#"):
            assert current is not None, f"{LOCK}: an image before any [section]"
            current.append(line.strip())
    return sections


def hauler_manifest(name: str, description: str, images: list[str]) -> str:
    """A Hauler Images manifest (content.hauler.cattle.io/v1)."""
    lines = [
        f"# {description}",
        "# Generated from deploy/release/images.lock by `just images-lock`; never edit by hand.",
        "apiVersion: content.hauler.cattle.io/v1",
        "kind: Images",
        "metadata:",
        f"  name: {name}",
        "spec:",
        "  images:",
    ]
    lines += [f"    - name: {image}" for image in images]
    return "\n".join(lines) + "\n"


def manifests(sections: dict[str, list[str]]) -> dict[Path, str]:
    return {
        HAULER / "core.yaml": hauler_manifest(
            "servtuner-core",
            "Every third-party image of the core archive. servtuner's own images are added from the build"
            " (just release).",
            sections["core"],
        ),
        HAULER / "vllm.yaml": hauler_manifest(
            "servtuner-vllm", "The optional vLLM archive: the managed vLLM images.", sections["vllm"]
        ),
        HAULER / "sglang.yaml": hauler_manifest(
            "servtuner-sglang", "The optional SGLang archive: the managed SGLang image.", sections["sglang"]
        ),
    }


def lock(platform: str) -> None:
    third_party, _ = rendered_images(render())
    pinned = sorted(f"{without_digest(image)}@{digest_of(image, platform)}" for image in third_party)
    vllm = sorted(f"{image}@{digest_of(image, platform)}" for image in VLLM)
    sglang = sorted(f"{image}@{digest_of(image, platform)}" for image in SGLANG)
    own = [f"servtuner/servtuner-{name}:{app_version()}" for name in ("api", "bench", "frontend")]
    LOCK.write_text(
        "# Every image servtuner's releases render, pinned by digest (deploy/release/images.py).\n"
        "# Generated: run `just images-lock` after changing a chart version or a value that names an image.\n"
        "# [core] goes in the core archive, [servtuner] is built by `just release`, [vllm] and [sglang] are\n"
        "# the optional archives.\n"
        f"# Platform: {platform} (each digest is that platform's manifest).\n"
        "[core]\n" + "\n".join(pinned) + "\n\n"
        "[servtuner]\n" + "\n".join(own) + "\n\n"
        "[vllm]\n" + "\n".join(vllm) + "\n\n"
        "[sglang]\n" + "\n".join(sglang) + "\n"
    )
    HAULER.mkdir(exist_ok=True)
    for path, text in manifests(read_lock()).items():
        path.write_text(text)
    print(
        f"wrote {LOCK}: {len(pinned)} images, {len(own)} of servtuner's, {len(vllm)} vLLM,"
        f" {len(sglang)} SGLang; and {HAULER}/"
    )


def check() -> int:
    third_party, _ = rendered_images(render())
    locked = {without_digest(image): image for image in read_lock().get("core", [])}
    missing = sorted(image for image in third_party if without_digest(image) not in locked)
    stale = sorted(set(locked) - {without_digest(image) for image in third_party})
    for image in missing:
        print(f"images-check: rendered but not in {LOCK.name}: {image}")
    for image in stale:
        print(f"images-check: in {LOCK.name} but no release renders it: {image}")
    sections = read_lock()
    for path, text in manifests(sections).items():
        if not path.exists() or path.read_text() != text:
            print(f"images-check: {path.relative_to(DEPLOY)} does not match {LOCK.name}")
            missing.append(str(path))
    if missing or stale:
        print("images-check: run `just images-lock`")
        return 1
    print(f"images-check: {len(locked)} images, all locked")
    return 0


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    parser.add_argument("command", choices=["lock", "check"])
    parser.add_argument("--platform", default="linux/amd64", help="the nodes' platform (lock only)")
    arguments = parser.parse_args()
    if arguments.command == "lock":
        lock(arguments.platform)
        return 0
    return check()


if __name__ == "__main__":
    sys.exit(main())
