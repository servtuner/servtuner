"""KubernetesJobRunner: one ``batch/v1`` Job per run (api/CLAUDE.md "Runners").

For each run the runner creates, in the runs namespace:

- a Job running the same ``servtuner run`` command line as the subprocess runner;
- a ConfigMap holding the RunSpec exactly as submitted;
- a Secret holding the run token.

The ConfigMap and the Secret are owned by the Job, so ``ttlSecondsAfterFinished``
removes all three. The Job is never retried (``backoffLimit: 0``): a failed
benchmark is a result, and a second attempt would write into the same bundle.

The bench pod gets Guaranteed QoS (requests equal limits), stays off the nodes
of the workloads under test (``target.workload`` and every upstream), and
shares the api's node when the data volume is ReadWriteOnce.

The runner's view is authoritative: the Job's conditions decide
``succeeded`` or ``failed``, and a pod that never starts within the start
timeout fails the run with the scheduler's or the kubelet's reason.

- ``config``: names, labels, mount paths, the runner's config and the workloads to avoid.
- ``jobs``: the Job, ConfigMap and Secret of one run (``JobBuilder``).
- ``cluster``: what the runner needs of Kubernetes, and its lightkube client.
- ``runner``: launching, watching and cancelling (``KubernetesJobRunner``)."""
