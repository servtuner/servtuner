"""Run records in Postgres: every query and state transition lives here.

Lifecycle (api/CLAUDE.md):
``pending -> launching -> running(phase) -> collecting -> succeeded | failed | cancelled``.
bench's events move a run between ``running`` and ``collecting``; only the
runner's final status ends it, so a pod killed by OOM is ``failed`` even if it
never reported. A final status is never revised.

- ``base``: statuses, filters and the ``Records`` base class.
- ``runs``, ``experiments``, ``targets``, ``tuning``, ``schedules``: each domain's queries.
- ``repository``: ``RunRepository``, all of them together."""
