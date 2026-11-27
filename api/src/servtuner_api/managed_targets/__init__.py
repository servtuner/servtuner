"""Managed targets.

A managed target is a vLLM servtuner deploys itself: a Deployment in a namespace set aside for it,
or a host through servtuner launcher, from the template stored here. The deployer creates it.

- ``spec``: the stored template and its checks.
- ``views``: the request and response models of the routes.
- ``runs``: what its runs and sessions derive from it: the RunSpec target block, labels,
  credentials and the objectives they can rank by.
- ``service``: storing, deploying and tearing down (``ManagedTargetService``).

Transport: plain HTTP by default; server-authenticated ``tls`` is the opt-in, with the
target's own certificate. ``mtls`` is not offered."""
