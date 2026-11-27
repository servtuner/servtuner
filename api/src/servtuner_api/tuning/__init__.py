"""Tuning sessions.

A session searches the vLLM flags of one managed target for one workload preset and SLOs. Each
``tick`` makes at most one transition and stores it, so a restarted api resumes a session from
its row:

    propose -> [approval] -> deploy -> booting -> (boot result | running) -> propose ...

- propose: ``servtuner propose`` over the session's analysed runs and boot results. ``done`` ends
  the session; otherwise the first proposal is the next step.
- deploy / booting: the managed target is deployed with the proposal's knobs and polled. A boot
  probe ends with its boot result; a run step waits until ready (an infeasible boot becomes a
  boot result too).
- running: the proposal's repeats run one at a time, never concurrently, since they share the
  GPU they measure. A run that fails is retried once.

The lease is the database's: at most one active session.

- ``models``: the session request and resource, budgets, statuses and errors.
- ``proposer``: ``servtuner propose``.
- ``run_specs``: the RunSpec of a session's run.
- ``evidence``: reusing earlier sessions' evidence.
- ``outcome``: the recommendation and Pareto set a finished session carries.
- ``service``: the state machine (``TuningService``)."""
