"""Alembic environment: runs on the connection ``upgrade_database`` hands in."""

from alembic import context

from servtuner_api.database import Base

connection = context.config.attributes["connection"]
context.configure(connection=connection, target_metadata=Base.metadata)
with context.begin_transaction():
    context.run_migrations()
