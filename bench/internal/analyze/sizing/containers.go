package sizing

import (
	"fmt"
	"regexp"

	"github.com/servtuner/servtuner/bench/internal/spec"
)

// containerRef is one container of a workload under test, as the pre-run
// snapshot captured it. The bench pod is never one: it is not in the snapshot.
type containerRef struct {
	// key prefixes the container's derivation node ids, such as
	// "target.bifrost" or "upstream0.openai_mock".
	key       string
	target    spec.RecommendationTarget
	namespace string
	name      string
	pods      map[string]bool
	// requests and limits are the captured spec, as Kubernetes quantities.
	requests, limits spec.ResourceQuantities
	// wasOOMKilled or restarted during the run (from the post-run snapshot).
	wasOOMKilled, restarted bool
}

// oomKilledReason is the kubelet's termination reason for a memory-limit kill.
const oomKilledReason = "OOMKilled"

// nodeIDUnsafe matches what a derivation node id cannot hold.
var nodeIDUnsafe = regexp.MustCompile(`[^A-Za-z0-9_]`)

// containersOf lists the containers to size, in snapshot order. The
// namespace comes from the RunSpec, which names each workload's namespace.
func containersOf(runSpec spec.RunSpec, pre, post *spec.TargetSnapshot) []containerRef {
	if pre == nil {
		return nil
	}
	var refs []containerRef
	for _, workload := range pre.Workloads {
		if len(workload.Pods) == 0 {
			continue
		}
		namespace, ok := namespaceOf(runSpec, workload)
		if !ok {
			continue // a workload the RunSpec does not name cannot be selected in Prometheus
		}
		pods := map[string]bool{}
		for _, pod := range workload.Pods {
			pods[pod.Name] = true
		}
		role := string(workload.Role)
		if workload.UpstreamIndex != nil {
			role = fmt.Sprintf("%s%d", role, *workload.UpstreamIndex)
		}
		for _, container := range workload.Pods[0].Containers {
			name := container.Name
			ref := containerRef{
				key:       role + "." + nodeIDUnsafe.ReplaceAllString(name, "_"),
				namespace: namespace,
				name:      name,
				pods:      pods,
				requests:  container.Requests,
				limits:    container.Limits,
				target: spec.RecommendationTarget{
					Role: workload.Role, UpstreamIndex: workload.UpstreamIndex, TargetType: workload.TargetType,
					Namespace: namespace, Container: &name,
				},
			}
			ref.wasOOMKilled, ref.restarted = restartsOf(container, post, workload.Role, workload.UpstreamIndex)
			refs = append(refs, ref)
		}
	}
	return refs
}

// namespaceOf is the namespace the RunSpec gives the workload: target.workload
// for the target, target.upstreams[i].workload for upstream i.
func namespaceOf(runSpec spec.RunSpec, workload spec.Workload_2) (string, bool) {
	if workload.UpstreamIndex == nil {
		if runSpec.Target.Workload == nil {
			return "", false
		}
		return runSpec.Target.Workload.Namespace, true
	}
	index := *workload.UpstreamIndex
	if index >= len(runSpec.Target.Upstreams) || runSpec.Target.Upstreams[index].Workload == nil {
		return "", false
	}
	return runSpec.Target.Upstreams[index].Workload.Namespace, true
}

// restartsOf compares a container's restart count before and after the run,
// and looks for an OOM kill in its last termination.
func restartsOf(before spec.Container, post *spec.TargetSnapshot, role spec.WorkloadRole, upstream *int) (oomKilled, restarted bool) {
	if post == nil {
		return false, false
	}
	beforeRestarts := 0
	if before.RestartCount != nil {
		beforeRestarts = *before.RestartCount
	}
	for _, workload := range post.Workloads {
		if workload.Role != role || !sameIndex(workload.UpstreamIndex, upstream) {
			continue
		}
		for _, pod := range workload.Pods {
			for _, container := range pod.Containers {
				if container.Name != before.Name {
					continue
				}
				if container.RestartCount == nil || *container.RestartCount <= beforeRestarts {
					continue // a termination reason from before the run is not this run's
				}
				restarted = true
				if container.LastTerminationReason != nil && *container.LastTerminationReason == oomKilledReason {
					oomKilled = true
				}
			}
		}
	}
	return oomKilled, restarted
}

func sameIndex(a, b *int) bool {
	return (a == nil && b == nil) || (a != nil && b != nil && *a == *b)
}
