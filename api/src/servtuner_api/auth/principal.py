"""The caller of a request, its role and the actions roles grant.

Routes check actions, never roles, so custom roles later change data, not routes.
"""

from dataclasses import dataclass
from enum import StrEnum


class Role(StrEnum):
    VIEWER = "viewer"
    OPERATOR = "operator"
    ADMIN = "admin"


class AuthMethod(StrEnum):
    """How a request proved who it is; recorded on sessions, audit rows and created rows."""

    NONE = "none"  # auth.mode none: nothing was checked
    SESSION = "session"  # a local account's session cookie
    FORWARD_HEADER = "forward_header"  # a header set by the trusted proxy
    RUN_TOKEN = "run_token"  # a bench job's per-run token


class Action(StrEnum):
    READ = "read"
    RUNS_WRITE = "runs.write"
    TUNING_WRITE = "tuning.write"
    TARGETS_CHECK = "targets.check"
    TARGETS_WRITE = "targets.write"
    SECRETS_WRITE = "secrets.write"
    RUNS_DELETE = "runs.delete"
    LOGS_READ = "logs.read"
    USERS_ADMIN = "users.admin"
    # Only a run token carries it, and only for its own run's events.
    EVENTS_WRITE = "events.write"
    # Only a run token carries it: bench fetches the price book's datasheet for its own run
    # (token_pricing.from_price_book). Users read it through GET /price-book/datasheet.
    DATASHEET_READ = "datasheet.read"


VIEWER_ACTIONS = frozenset({Action.READ})
OPERATOR_ACTIONS = VIEWER_ACTIONS | {
    Action.RUNS_WRITE,
    Action.TUNING_WRITE,
    Action.TARGETS_CHECK,
    Action.LOGS_READ,
}
ADMIN_ACTIONS = OPERATOR_ACTIONS | {
    Action.TARGETS_WRITE,
    Action.SECRETS_WRITE,
    Action.RUNS_DELETE,
    Action.USERS_ADMIN,
}
ROLE_ACTIONS: dict[Role, frozenset[Action]] = {
    Role.VIEWER: VIEWER_ACTIONS,
    Role.OPERATOR: OPERATOR_ACTIONS,
    Role.ADMIN: ADMIN_ACTIONS,
}
RUN_TOKEN_ACTIONS = frozenset({Action.EVENTS_WRITE, Action.DATASHEET_READ})


@dataclass(frozen=True)
class Principal:
    """Who a request is. Handlers see only this, never the credential that produced it."""

    auth_method: AuthMethod
    actions: frozenset[Action]
    user_id: str | None = None  # a users row; None for auth.mode none and run tokens
    username: str | None = None
    role: Role | None = None  # None for run tokens
    session_id: str | None = None  # the session's token hash, for session requests only
    run_id: str | None = None  # the run a run token belongs to
    is_bypassed: bool = False  # True when no credential was checked (auth.mode none)
    must_change_password: bool = False  # an admin reset it: nothing else until it is changed

    def can(self, action: Action) -> bool:
        return action in self.actions


def user_principal(
    user_id: str,
    username: str,
    role: Role,
    auth_method: AuthMethod,
    session_id: str | None = None,
    must_change_password: bool = False,
) -> Principal:
    return Principal(
        auth_method=auth_method,
        actions=ROLE_ACTIONS[role],
        user_id=user_id,
        username=username,
        role=role,
        session_id=session_id,
        must_change_password=must_change_password,
    )


# auth.mode none keeps the behaviour from before user auth: everything is allowed, and the
# request is marked as unchecked so nothing mistakes it for an authenticated admin.
UNCHECKED_PRINCIPAL = Principal(
    auth_method=AuthMethod.NONE,
    actions=ADMIN_ACTIONS,
    role=Role.ADMIN,
    is_bypassed=True,
)


def run_token_principal(run_id: str) -> Principal:
    return Principal(auth_method=AuthMethod.RUN_TOKEN, actions=RUN_TOKEN_ACTIONS, run_id=run_id)
