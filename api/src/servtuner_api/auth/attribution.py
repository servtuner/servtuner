"""Who created a row: the acting user of the request, stamped on insert.

The guard sets ``acting_user_id`` for each request; ``database.py`` stamps it into
``created_by`` of any row inserted while it is set. Background work (the scheduler, tuning
sessions, run watchers) runs in threads that start with no user, so its rows have none.
"""

from contextvars import ContextVar

acting_user_id: ContextVar[str | None] = ContextVar("acting_user_id", default=None)
