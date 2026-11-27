package quality

import (
	"fmt"
	"maps"
	"slices"
	"sort"
	"time"

	"github.com/servtuner/servtuner/bench/internal/analyze/input"
	"github.com/servtuner/servtuner/bench/internal/spec"
)

// oomKilledReason is the Kubernetes termination reason of a container killed
// for exceeding its memory limit.
const oomKilledReason = "OOMKilled"

// Drift is what differs between the pre- and post-run snapshots
// (config drift).
type Drift struct {
	// Changes describes each difference, in a fixed order.
	Changes []string
	// OOMKills names containers that restarted after an OOMKilled termination.
	OOMKills []string
}

// DetectDrift compares the snapshots workload by workload: pods that appeared
// or vanished, container images, digests, args, env, resources and restart
// counts, and the replica counts. It returns no drift when either snapshot is
// missing, because a run without a cluster has nothing to compare.
func DetectDrift(pre, post *spec.TargetSnapshot) Drift {
	var drift Drift
	if pre == nil || post == nil {
		return drift
	}
	postByKey := map[string]spec.Workload_2{}
	for _, workload := range post.Workloads {
		postByKey[workloadKey(workload)] = workload
	}
	for _, before := range pre.Workloads {
		key := workloadKey(before)
		after, found := postByKey[key]
		if !found {
			drift.Changes = append(drift.Changes, key+": workload missing after the run")
			continue
		}
		delete(postByKey, key)
		compareWorkload(&drift, key, before, after)
	}
	for _, key := range slices.Sorted(maps.Keys(postByKey)) {
		drift.Changes = append(drift.Changes, key+": workload appeared during the run")
	}
	return drift
}

func workloadKey(workload spec.Workload_2) string {
	if workload.UpstreamIndex != nil {
		return fmt.Sprintf("%s %d", workload.Role, *workload.UpstreamIndex)
	}
	return string(workload.Role)
}

func compareWorkload(drift *Drift, key string, before, after spec.Workload_2) {
	if before.Replicas != nil && after.Replicas != nil && *before.Replicas != *after.Replicas {
		drift.Changes = append(drift.Changes, fmt.Sprintf("%s: replicas changed from %d desired, %d ready to %d desired, %d ready",
			key, before.Replicas.Desired, before.Replicas.Ready, after.Replicas.Desired, after.Replicas.Ready))
	}
	beforePods, afterPods := podsByName(before), podsByName(after)
	for _, name := range slices.Sorted(maps.Keys(beforePods)) {
		afterPod, found := afterPods[name]
		if !found {
			drift.Changes = append(drift.Changes, fmt.Sprintf("%s: pod %s was replaced", key, name))
			continue
		}
		compareContainers(drift, key+" pod "+name, beforePods[name], afterPod)
	}
	for _, name := range slices.Sorted(maps.Keys(afterPods)) {
		if _, found := beforePods[name]; !found {
			drift.Changes = append(drift.Changes, fmt.Sprintf("%s: pod %s appeared during the run", key, name))
		}
	}
}

func podsByName(workload spec.Workload_2) map[string]spec.Pod {
	pods := map[string]spec.Pod{}
	for _, pod := range workload.Pods {
		pods[pod.Name] = pod
	}
	return pods
}

func compareContainers(drift *Drift, where string, before, after spec.Pod) {
	afterByName := map[string]spec.Container{}
	for _, container := range after.Containers {
		afterByName[container.Name] = container
	}
	names := make([]string, 0, len(before.Containers))
	beforeByName := map[string]spec.Container{}
	for _, container := range before.Containers {
		names = append(names, container.Name)
		beforeByName[container.Name] = container
	}
	sort.Strings(names)
	for _, name := range names {
		was, now := beforeByName[name], afterByName[name]
		label := where + " container " + name
		if _, found := afterByName[name]; !found {
			drift.Changes = append(drift.Changes, label+": missing after the run")
			continue
		}
		if was.Image != now.Image || !equalOptional(was.ImageDigest, now.ImageDigest) {
			drift.Changes = append(drift.Changes, label+": image changed")
		}
		if !slices.Equal(was.Args, now.Args) {
			drift.Changes = append(drift.Changes, label+": args changed")
		}
		if !maps.Equal(renderEnv(was.Env), renderEnv(now.Env)) {
			drift.Changes = append(drift.Changes, label+": env changed")
		}
		if !maps.Equal(was.Requests, now.Requests) || !maps.Equal(was.Limits, now.Limits) {
			drift.Changes = append(drift.Changes, label+": resources changed")
		}
		if restarts(now) > restarts(was) {
			drift.Changes = append(drift.Changes, fmt.Sprintf("%s: restarted %d time(s) during the run", label, restarts(now)-restarts(was)))
			if now.LastTerminationReason != nil && *now.LastTerminationReason == oomKilledReason {
				drift.OOMKills = append(drift.OOMKills, label)
			}
		}
	}
}

func restarts(container spec.Container) int {
	if container.RestartCount == nil {
		return 0
	}
	return *container.RestartCount
}

func equalOptional(a, b *string) bool {
	if a == nil || b == nil {
		return a == b
	}
	return *a == *b
}

// renderEnv reduces env vars to name and value, with redacted ones carrying no
// value, so that two snapshots compare without ever seeing a secret.
func renderEnv(env []spec.EnvVar) map[string]string {
	rendered := map[string]string{}
	for _, variable := range env {
		fields, ok := variable.(map[string]any)
		if !ok {
			continue
		}
		name, _ := fields["name"].(string)
		value, hasValue := fields["value"].(string)
		if !hasValue {
			value = "<redacted>"
		}
		rendered[name] = value
	}
	return rendered
}

// ChangeTime estimates when the target changed from the signals kube-state-
// metrics gives: the first time the ready pod count differs from its first
// value (it is a trailing minimum, so a dip shows at once), or restarts rise
// above zero. ok is false without such a signal or without a change in it.
func ChangeTime(series input.SignalSource) (time.Time, bool, error) {
	var earliest time.Time
	var found bool
	consider := func(at time.Time) {
		if !found || at.Before(earliest) {
			earliest, found = at, true
		}
	}
	ready, ok, err := series.Signal(input.ReadyPods)
	if err != nil {
		return time.Time{}, false, fmt.Errorf("read signal %s: %w", input.ReadyPods, err)
	}
	if ok {
		for _, sample := range ready[1:] {
			if sample.Value != ready[0].Value {
				consider(sample.At)
				break
			}
		}
	}
	restarts, ok, err := series.Signal(input.Restarts)
	if err != nil {
		return time.Time{}, false, fmt.Errorf("read signal %s: %w", input.Restarts, err)
	}
	if ok {
		for _, sample := range restarts {
			if sample.Value > 0 {
				consider(sample.At)
				break
			}
		}
	}
	return earliest, found, nil
}
