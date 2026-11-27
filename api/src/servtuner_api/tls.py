"""Serving transport: TLS with a client-certificate policy (required, optional or none), or
plain HTTP.

Certificates come from mounted ``kubernetes.io/tls`` Secret files. cert-manager
renews them in place, so ``CertificateReloader`` reloads the listener's SSL
context when the files change; new handshakes then use the new certificate.
"""

import logging
import ssl
import threading
from dataclasses import dataclass
from pathlib import Path
from typing import Any

from servtuner_api.settings.models import ClientAuth, Hop, Settings, TlsSettings

logger = logging.getLogger(__name__)


# What ssl checks of a client, by the listener's policy. optional verifies a certificate that is
# sent, so a bad one is still refused.
CERT_REQS = {
    ClientAuth.REQUIRED: ssl.CERT_REQUIRED,
    ClientAuth.OPTIONAL: ssl.CERT_OPTIONAL,
    ClientAuth.NONE: ssl.CERT_NONE,
}


def uvicorn_ssl_options(tls: TlsSettings) -> dict[str, Any]:
    """Options for ``uvicorn.Config``; empty without TLS (plain HTTP)."""
    if not tls.uses_tls(Hop.INGRESS_API):
        return {}
    options: dict[str, Any] = {
        "ssl_certfile": str(tls.cert_file),
        "ssl_keyfile": str(tls.key_file),
        "ssl_cert_reqs": CERT_REQS[tls.client_auth],
    }
    if tls.client_auth is not ClientAuth.NONE:
        options["ssl_ca_certs"] = str(tls.client_ca_file)
    return options


@dataclass(frozen=True)
class _FileVersion:
    modified_ns: int
    size_bytes: int


def _version_of(path: Path) -> _FileVersion:
    # Kubernetes updates mounted Secrets by swapping a symlink, so follow it.
    stat = path.stat()
    return _FileVersion(stat.st_mtime_ns, stat.st_size)


class CertificateReloader:
    """Polls the certificate files and reloads them into a live SSL context."""

    def __init__(self, context: ssl.SSLContext, tls: TlsSettings, interval_seconds: float) -> None:
        assert tls.cert_file and tls.key_file
        self._context = context
        self._cert_file = tls.cert_file
        self._key_file = tls.key_file
        # The client CA is watched only when the listener checks client certificates.
        self._client_ca_file = (
            tls.client_ca_file if tls.client_auth is not ClientAuth.NONE else None
        )
        self._interval_seconds = interval_seconds
        self._seen = self._current_versions()
        self._stopping = threading.Event()
        self._thread = threading.Thread(target=self._run, daemon=True)

    def start(self) -> None:
        self._thread.start()

    def stop(self) -> None:
        self._stopping.set()
        self._thread.join()

    def reload_if_changed(self) -> bool:
        versions = self._current_versions()
        if versions == self._seen:
            return False
        self._context.load_cert_chain(str(self._cert_file), str(self._key_file))
        if self._client_ca_file is not None:
            # load_verify_locations only adds CAs: a CA removed from the file needs a restart.
            self._context.load_verify_locations(str(self._client_ca_file))
        self._seen = versions
        logger.info("reloaded api certificate from %s", self._cert_file)
        return True

    def _current_versions(self) -> list[_FileVersion]:
        paths = [self._cert_file, self._key_file]
        if self._client_ca_file is not None:
            paths.append(self._client_ca_file)
        return [_version_of(path) for path in paths]

    def _run(self) -> None:
        while not self._stopping.wait(self._interval_seconds):
            try:
                self.reload_if_changed()
            except OSError:  # ssl.SSLError is an OSError
                # A rotation can be seen half-done (new cert, old key); retry next tick.
                logger.warning("certificate reload failed; keeping the previous one", exc_info=True)


def reloader_for(
    config_ssl: ssl.SSLContext | None, settings: Settings
) -> CertificateReloader | None:
    if config_ssl is None:
        return None
    return CertificateReloader(config_ssl, settings.tls, settings.cert_reload_seconds)
