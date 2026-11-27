"""All api configuration, read in one place from the chart's environment contract.

The names are those of ``deploy/helm/servtuner/README.md`` ("Environment
contract"). This is a small frozen dataclass rather than pydantic-settings:
that package is not among the pinned libraries, and the rules here (hops must
agree, certificate settings may not appear without an mtls hop) are
cross-field checks that a plain function expresses more clearly anyway.

Nothing is inferred. A missing hop mode falls back to the default
transport mode, and that to ``mtls``; a contradictory combination stops the
api at startup with every problem listed.

- ``models``: the settings, their enums and defaults.
- ``env``: the environment variable names, defined once.
- ``reader``: reading and cross-checking the environment."""
