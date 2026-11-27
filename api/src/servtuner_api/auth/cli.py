"""Recovery from inside the api pod: ``servtuner-api admin ...`` through ``kubectl exec``.

Shell access to the pod is the proof; the setup token is never reused for recovery.
"""

import argparse
import sys
from collections.abc import Sequence
from typing import TextIO

from servtuner_api.auth.principal import AuthMethod, Role
from servtuner_api.auth.service import issue_temporary_password
from servtuner_api.auth.store import AuditEntry, AuthStore, LastAdminError
from servtuner_api.records.base import utc_now


def run_admin_command(argv: Sequence[str], store: AuthStore, out: TextIO = sys.stdout) -> int:
    parser = argparse.ArgumentParser(prog="servtuner-api admin")
    commands = parser.add_subparsers(dest="command", required=True)
    reset = commands.add_parser(
        "reset-password", help="give a user a temporary password they must change"
    )
    reset.add_argument("username")
    role = commands.add_parser("set-role", help="change a user's role")
    role.add_argument("username")
    role.add_argument("role", choices=[member.value for member in Role])
    enable = commands.add_parser("enable", help="enable a disabled user")
    enable.add_argument("username")
    arguments = parser.parse_args(argv)

    user = store.user_by_username(arguments.username)
    if user is None:
        print(f"no user {arguments.username!r}", file=out)
        return 1
    now = utc_now()
    if arguments.command == "reset-password":
        password = issue_temporary_password(store, user.id, now)
        detail: dict[str, str] = {}
        print(f"temporary password for {user.username}: {password}", file=out)
        print("they must change it at their next sign-in", file=out)
    elif arguments.command == "set-role":
        try:
            store.update_user_keeping_an_admin(user.id, role=arguments.role)
        except LastAdminError as error:
            print(str(error), file=out)
            return 1
        store.delete_user_sessions(user.id)
        detail = {"role": arguments.role}
        print(f"{user.username} is now {arguments.role}", file=out)
    else:
        store.update_user(user.id, disabled_at=None)
        detail = {}
        print(f"{user.username} is enabled", file=out)
    store.audit(
        AuditEntry(
            f"admin_cli_{arguments.command.replace('-', '_')}",
            "success",
            AuthMethod.NONE,
            target_kind="user",
            target_id=user.id,
            detail=detail,
        ),
        now,
    )
    return 0
