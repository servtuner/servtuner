"""The deployer of managed targets.

It renders a managed target's objects, applies them, and answers one question per poll: is
the vLLM ready for load, still starting, or did it fail to boot (and was that the
configuration's fault or a deploy defect)?

- ``boot``: reading a failed boot from vLLM's log (the configuration's fault, or a defect).
- ``manifests``: the Kubernetes objects a managed target is made of.
- ``base``: what both deployers share: the cluster and checker protocols, errors, readiness,
  logs and boot probes.
- ``kubernetes``: the Deployer on a cluster, and the lightkube client it uses.
- ``launcher``: hosts outside Kubernetes, through servtuner launcher.

Safety rules:
- servtuner only touches objects that carry both ownership labels; an object of the same name
  without them belongs to someone else and stops the deploy.
- The Secrets a deploy needs are checked before anything is applied, because vLLM reports a bad
  certificate path only after the model has loaded.
- vLLM's flags come from bench's knob table (``servtuner vllm-args``), the same table that captures
  them, so the captured args can be compared exactly."""
