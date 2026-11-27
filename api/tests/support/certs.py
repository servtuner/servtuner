"""Throwaway certificates for TLS tests, issued with the ``openssl`` CLI.

Shared by every test that needs a CA, a server certificate or a client
certificate (one test-support module per component).
"""

import subprocess
from dataclasses import dataclass
from pathlib import Path

LOOPBACK = "127.0.0.1"
EXTENSIONS = f"subjectAltName=DNS:localhost,IP:{LOOPBACK}\nextendedKeyUsage=serverAuth,clientAuth\n"


@dataclass(frozen=True)
class IssuedCertificate:
    cert_file: Path
    key_file: Path


@dataclass(frozen=True)
class CertificateAuthority:
    cert_file: Path
    key_file: Path
    directory: Path

    def issue(self, common_name: str) -> IssuedCertificate:
        """A leaf valid as both a server and a client certificate for localhost."""
        key_file = self.directory / f"{common_name}.key"
        request_file = self.directory / f"{common_name}.csr"
        cert_file = self.directory / f"{common_name}.crt"
        extensions_file = self.directory / "extensions.cnf"
        extensions_file.write_text(EXTENSIONS)
        _openssl(
            "req",
            "-new",
            *_new_key(key_file),
            "-out",
            str(request_file),
            "-subj",
            f"/CN={common_name}",
        )
        _openssl(
            "x509", "-req", "-in", str(request_file), "-CA", str(self.cert_file),
            "-CAkey", str(self.key_file), "-CAcreateserial", "-out", str(cert_file),
            "-days", "2", "-extfile", str(extensions_file),
        )  # fmt: skip
        return IssuedCertificate(cert_file, key_file)


def create_authority(
    directory: Path, common_name: str = "servtuner-test-ca"
) -> CertificateAuthority:
    directory.mkdir(parents=True, exist_ok=True)
    cert_file = directory / f"{common_name}.crt"
    key_file = directory / f"{common_name}.key"
    _openssl(
        "req", "-x509", *_new_key(key_file), "-out", str(cert_file), "-days", "2",
        "-subj", f"/CN={common_name}", "-addext", "basicConstraints=critical,CA:TRUE",
        "-addext", "keyUsage=critical,keyCertSign",
    )  # fmt: skip
    return CertificateAuthority(cert_file, key_file, directory)


def _new_key(key_file: Path) -> list[str]:
    return [
        "-newkey",
        "ec",
        "-pkeyopt",
        "ec_paramgen_curve:prime256v1",
        "-nodes",
        "-keyout",
        str(key_file),
    ]


def _openssl(*arguments: str) -> None:
    subprocess.run(["openssl", *arguments], check=True, capture_output=True)  # noqa: S603, S607
