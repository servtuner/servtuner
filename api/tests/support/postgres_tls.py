"""A throwaway Postgres container that accepts only TLS with a client certificate.

Shared by the api's mTLS tests and the local end-to-end script (e2e/local.py).
The server refuses any connection that does not present a client certificate
whose CN is the database user, so a connection that works really did mTLS.
"""

import shutil
from pathlib import Path

from testcontainers.core.container import DockerContainer
from testcontainers.core.wait_strategies import ExecWaitStrategy

from tests.support.certs import CertificateAuthority

DATABASE_USER = "servtuner"
DATABASE_NAME = "servtuner"
POSTGRES_IMAGE = "postgres:17"
POSTGRES_PORT = 5432
# Postgres insists its key is owned by it and mode 0600, so copy it inside the container.
START_SCRIPT = """
set -e
install -d -o postgres -g postgres -m 700 /pgcerts
install -o postgres -g postgres -m 600 /certs-in/server.key /pgcerts/server.key
install -o postgres -g postgres -m 644 /certs-in/server.crt /pgcerts/server.crt
install -o postgres -g postgres -m 644 /certs-in/ca.crt /pgcerts/ca.crt
printf 'local all all trust\\nhostssl all all all cert\\n' > /pgcerts/pg_hba.conf
chown postgres:postgres /pgcerts/pg_hba.conf
exec docker-entrypoint.sh postgres -c ssl=on -c ssl_cert_file=/pgcerts/server.crt \\
  -c ssl_key_file=/pgcerts/server.key -c ssl_ca_file=/pgcerts/ca.crt \\
  -c hba_file=/pgcerts/pg_hba.conf
"""


def tls_postgres_container(authority: CertificateAuthority, mount: Path) -> DockerContainer:
    """Not started: use it as a context manager. ``mount`` is a scratch directory for the certs."""
    server = authority.issue("pg-server")
    shutil.copy(server.cert_file, mount / "server.crt")
    shutil.copy(server.key_file, mount / "server.key")
    shutil.copy(authority.cert_file, mount / "ca.crt")
    return (
        DockerContainer(POSTGRES_IMAGE)
        .with_env("POSTGRES_USER", DATABASE_USER)
        .with_env("POSTGRES_DB", DATABASE_NAME)
        .with_env("POSTGRES_PASSWORD", "unused-with-cert-auth")
        .with_exposed_ports(POSTGRES_PORT)
        .with_volume_mapping(str(mount), "/certs-in", "ro")
        .with_command(["bash", "-c", START_SCRIPT])
        # The official image first runs a throwaway init server that listens on the unix socket
        # only, stops it, then starts the real one with our TLS settings. pg_isready over TCP
        # succeeds only for the real one, and needs no login (a psql probe, like
        # PostgresContainer's, is refused by this server by design). LogMessageWaitStrategy's
        # ``times`` is ignored by testcontainers, so counting the "ready" log lines raced.
        .waiting_for(
            ExecWaitStrategy(["pg_isready", "--host", "127.0.0.1", "--port", str(POSTGRES_PORT)])
        )
    )
