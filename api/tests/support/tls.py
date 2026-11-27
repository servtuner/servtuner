"""TlsSettings for tests that need one without reading the environment."""

from servtuner_api.settings.models import ClientAuth, Connection, ConnectionTls, TlsSettings


def plain_tls() -> TlsSettings:
    """No TLS on any hop, and no certificate files."""
    return TlsSettings(
        connections={hop: ConnectionTls(enabled=False, client_cert=False) for hop in Connection},
        client_auth=ClientAuth.NONE,
        cert_file=None,
        key_file=None,
        ca_bundle_file=None,
        client_ca_file=None,
    )
