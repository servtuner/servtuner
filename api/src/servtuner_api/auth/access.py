"""What every route needs: the route-to-action table.

Every route of the app is listed here by method and path template. A route that
is missing is refused at runtime, and ``tests/test_auth_routes.py`` fails the
build, so a new route cannot be left unprotected by mistake.
"""

from enum import StrEnum

from servtuner_api.auth.principal import Action


class Open(StrEnum):
    """Access that is not one action."""

    PUBLIC = "public"  # no credential at all
    AUTHENTICATED = "authenticated"  # any signed-in user: their own profile and sessions


type RouteAccess = Action | Open

READ, RUNS_WRITE, TUNING_WRITE = Action.READ, Action.RUNS_WRITE, Action.TUNING_WRITE
TARGETS_CHECK, TARGETS_WRITE = Action.TARGETS_CHECK, Action.TARGETS_WRITE
SECRETS_WRITE, RUNS_DELETE = Action.SECRETS_WRITE, Action.RUNS_DELETE
LOGS_READ, USERS_ADMIN, EVENTS_WRITE = Action.LOGS_READ, Action.USERS_ADMIN, Action.EVENTS_WRITE

ROUTE_ACCESS: dict[tuple[str, str], RouteAccess] = {
    ("GET", "/healthz"): Open.PUBLIC,
    ("GET", "/version"): READ,
    # A user's own account
    ("GET", "/auth/status"): Open.PUBLIC,
    ("POST", "/auth/login"): Open.PUBLIC,
    ("POST", "/auth/logout"): Open.PUBLIC,
    ("POST", "/auth/setup"): Open.PUBLIC,
    ("GET", "/auth/me"): Open.AUTHENTICATED,
    ("POST", "/auth/password"): Open.AUTHENTICATED,
    ("GET", "/auth/sessions"): Open.AUTHENTICATED,
    ("DELETE", "/auth/sessions/{session_id}"): Open.AUTHENTICATED,
    ("POST", "/auth/sessions/revoke-others"): Open.AUTHENTICATED,
    # Users and audit (the admin's users page)
    ("GET", "/users"): USERS_ADMIN,
    ("POST", "/users"): USERS_ADMIN,
    ("GET", "/users/{user_id}"): USERS_ADMIN,
    ("PATCH", "/users/{user_id}"): USERS_ADMIN,
    ("POST", "/users/{user_id}/password"): USERS_ADMIN,
    ("POST", "/users/{user_id}/identities"): USERS_ADMIN,
    ("DELETE", "/users/{user_id}/identities"): USERS_ADMIN,
    ("DELETE", "/users/{user_id}/sessions"): USERS_ADMIN,
    ("GET", "/audit"): USERS_ADMIN,
    # Runs
    ("POST", "/runs"): RUNS_WRITE,
    ("GET", "/runs"): READ,
    ("GET", "/runs/{run_id}"): READ,
    ("POST", "/runs/{run_id}/cancel"): RUNS_WRITE,
    ("GET", "/runs/{run_id}/delete-impact"): READ,
    ("DELETE", "/runs/{run_id}"): RUNS_DELETE,
    ("GET", "/runs/{run_id}/files"): READ,
    ("GET", "/runs/{run_id}/files/{path:path}"): READ,
    ("GET", "/runs/{run_id}/events"): READ,
    ("POST", "/runs/{run_id}/events"): EVENTS_WRITE,
    ("GET", "/runs/{run_id}/analysis"): READ,
    ("GET", "/runs/{run_id}/recommendations"): READ,
    ("GET", "/runs/{run_id}/capacity"): READ,
    ("GET", "/runs/{run_id}/charts/client_timeseries"): READ,
    ("GET", "/runs/{run_id}/environment"): READ,
    ("GET", "/runs/{run_id}/snapshot"): READ,
    ("GET", "/runs/{run_id}/charts/inflight"): READ,
    ("GET", "/runs/{run_id}/charts/server"): READ,
    ("GET", "/runs/{run_id}/charts/histograms"): READ,
    ("POST", "/runs/{run_id}/followup"): RUNS_WRITE,
    ("POST", "/runs/{run_id}/confirm-applied"): RUNS_WRITE,
    # Comparing and experiments
    ("GET", "/compare"): READ,
    ("GET", "/experiments"): READ,
    ("GET", "/experiments/{name}"): READ,
    ("GET", "/experiments/{name}/delete-impact"): READ,
    ("DELETE", "/experiments/{name}"): RUNS_DELETE,
    ("POST", "/experiments/{name}/analyze"): RUNS_WRITE,
    # Computations that store nothing
    ("POST", "/plans"): READ,
    ("POST", "/scenarios/evaluate"): READ,
    ("POST", "/specs/validate"): READ,
    ("GET", "/knobs"): READ,
    ("GET", "/run-defaults"): READ,
    ("GET", "/workload-presets"): READ,
    ("GET", "/search-spaces"): READ,
    # Scenarios and schedules
    ("POST", "/scenarios"): RUNS_WRITE,
    ("GET", "/scenarios"): READ,
    ("GET", "/scenarios/{scenario_id}"): READ,
    ("DELETE", "/scenarios/{scenario_id}"): RUNS_WRITE,
    ("POST", "/schedules"): RUNS_WRITE,
    ("GET", "/schedules"): READ,
    ("GET", "/schedules/{schedule_id}"): READ,
    ("PATCH", "/schedules/{schedule_id}"): RUNS_WRITE,
    ("POST", "/schedules/{schedule_id}/trigger"): RUNS_WRITE,
    ("DELETE", "/schedules/{schedule_id}"): RUNS_WRITE,
    # Saved targets
    ("POST", "/targets/check"): TARGETS_CHECK,
    ("GET", "/targets"): READ,
    ("POST", "/targets"): TARGETS_WRITE,
    ("GET", "/targets/{target_id}"): READ,
    ("PUT", "/targets/{target_id}"): TARGETS_WRITE,
    ("DELETE", "/targets/{target_id}"): TARGETS_WRITE,
    ("PUT", "/targets/{target_id}/credentials/{slot}"): SECRETS_WRITE,
    ("DELETE", "/targets/{target_id}/credentials/{slot}"): SECRETS_WRITE,
    ("POST", "/targets/{target_id}/check"): TARGETS_CHECK,
    # Managed targets: deploying into a namespace is admin; a session's deploys are not
    ("GET", "/managed-targets"): READ,
    ("POST", "/managed-targets"): TARGETS_WRITE,
    ("GET", "/managed-targets/{target_id}"): READ,
    ("PUT", "/managed-targets/{target_id}"): TARGETS_WRITE,
    ("DELETE", "/managed-targets/{target_id}"): TARGETS_WRITE,
    ("PUT", "/managed-targets/{target_id}/credentials/{slot}"): SECRETS_WRITE,
    ("DELETE", "/managed-targets/{target_id}/credentials/{slot}"): SECRETS_WRITE,
    ("POST", "/managed-targets/{target_id}/deploy"): TARGETS_WRITE,
    ("GET", "/managed-targets/{target_id}/readiness"): READ,
    ("POST", "/managed-targets/{target_id}/check-launcher"): TARGETS_CHECK,
    ("GET", "/managed-targets/{target_id}/logs"): LOGS_READ,
    ("POST", "/managed-targets/{target_id}/boot-probe"): TARGETS_CHECK,
    ("POST", "/managed-targets/{target_id}/scale-down"): TARGETS_WRITE,
    # Hosts
    ("GET", "/hosts"): READ,
    ("POST", "/hosts"): TARGETS_WRITE,
    ("GET", "/hosts/{host_id}"): READ,
    ("PUT", "/hosts/{host_id}"): TARGETS_WRITE,
    ("DELETE", "/hosts/{host_id}"): TARGETS_WRITE,
    ("POST", "/hosts/{host_id}/read-machine"): TARGETS_WRITE,
    ("GET", "/price-book"): READ,
    ("PUT", "/price-book"): TARGETS_WRITE,
    ("GET", "/price-book/datasheet"): READ,
    ("GET", "/runs/{run_id}/datasheet"): Action.DATASHEET_READ,
    ("PUT", "/price-book/datasheet"): TARGETS_WRITE,
    ("DELETE", "/price-book/datasheet"): TARGETS_WRITE,
    # Tuning sessions
    ("POST", "/tuning-sessions"): TUNING_WRITE,
    ("GET", "/tuning-sessions"): READ,
    ("GET", "/tuning-sessions/{session_id}"): READ,
    ("POST", "/tuning-sessions/{session_id}/approve"): TUNING_WRITE,
    ("POST", "/tuning-sessions/{session_id}/cancel"): TUNING_WRITE,
}


def access_of(method: str, path: str) -> RouteAccess | None:
    """The access a route needs, or None for a route nobody listed (refused)."""
    return ROUTE_ACCESS.get((method, path))
