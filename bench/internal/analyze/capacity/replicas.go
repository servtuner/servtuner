package capacity

import (
	"fmt"
	"math"

	"github.com/servtuner/servtuner/bench/internal/analyze/input"
	"github.com/servtuner/servtuner/bench/internal/spec"
)

// perReplica normalises the SLO-max RPS by the ready replicas and by the GPUs.
// It is nil without an SLO-max or without a known,
// positive replica count.
func perReplica(slo *spec.SloMaxRps, readyReplicas, gpusPerReplica int) *spec.PerReplica {
	if slo == nil || readyReplicas < 1 {
		return nil
	}
	result := &spec.PerReplica{ReadyReplicas: readyReplicas, RpsPerReplica: slo.Rps / float64(readyReplicas)}
	if gpusPerReplica > 0 {
		totalGPUs := readyReplicas * gpusPerReplica
		perGPU := slo.Rps / float64(totalGPUs)
		result.TotalGpus, result.RpsPerGpu = &totalGPUs, &perGPU
	}
	return result
}

// ReadyReplicas is the minimum number of ready target replicas during the
// usable stages, the ones the SLO-max comes from: the minimum of the
// kube-state-metrics series over their windows when it was collected,
// otherwise the pre-run snapshot, or the post-run one without it. Excluded
// stages are left out because they may run on a different replica count (a
// scale-down is what excludes them), and dividing the capacity of two replicas
// by one would overstate the per-replica figure. It returns 0 when unknown.
func ReadyReplicas(series input.SignalSource, usable []input.Stage, pre, post *spec.TargetSnapshot) (int, error) {
	samples, found, err := series.Signal(input.ReadyPods)
	if err != nil {
		return 0, fmt.Errorf("read signal %s: %w", input.ReadyPods, err)
	}
	if found {
		lowest := math.Inf(1)
		for _, sample := range samples {
			for _, stage := range usable {
				if !sample.At.Before(stage.Start) && sample.At.Before(stage.End) {
					lowest = min(lowest, sample.Value)
				}
			}
		}
		if !math.IsInf(lowest, 1) {
			return int(math.Floor(lowest)), nil
		}
	}
	for _, snapshot := range []*spec.TargetSnapshot{pre, post} {
		if ready, ok := targetReady(snapshot); ok {
			return ready, nil
		}
	}
	return 0, nil
}

func targetReady(snapshot *spec.TargetSnapshot) (int, bool) {
	if snapshot == nil {
		return 0, false
	}
	for _, workload := range snapshot.Workloads {
		if workload.Role == spec.WorkloadRoleTarget && workload.Replicas != nil {
			return workload.Replicas.Ready, true
		}
	}
	return 0, false
}

// GPUsPerReplica is the GPU count of one target pod from the hardware profile
// of the snapshot, or 0 when it is unknown or the target has no accelerator.
func GPUsPerReplica(snapshot *spec.TargetSnapshot) int {
	if snapshot == nil {
		return 0
	}
	for _, workload := range snapshot.Workloads {
		if workload.Role == spec.WorkloadRoleTarget && workload.HardwareProfile != nil && workload.HardwareProfile.GpuCountPerPod != nil {
			return *workload.HardwareProfile.GpuCountPerPod
		}
	}
	return 0
}
