package sizing

import (
	"fmt"
	"math"
	"strings"

	"github.com/goccy/go-yaml"

	"github.com/servtuner/servtuner/bench/internal/analyze/derive"
	"github.com/servtuner/servtuner/bench/internal/spec"
)

// resourcesPatch is patches/resources.yaml: a
// resources block per container, sized per replica so it is valid at any
// traffic level, the Go runtime settings, and the HPA. Replicas and the HPA
// max appear only when target_rps is given.
type resourcesPatch struct {
	Workloads []patchWorkload `yaml:"workloads"`
}

type patchWorkload struct {
	Role          string           `yaml:"role"`
	UpstreamIndex *int             `yaml:"upstream_index,omitempty"`
	TargetType    string           `yaml:"target_type"`
	Namespace     string           `yaml:"namespace"`
	Replicas      *int             `yaml:"replicas,omitempty"`
	Containers    []patchContainer `yaml:"containers"`
	HPA           *patchHPA        `yaml:"hpa,omitempty"`
}

type patchContainer struct {
	Name      string         `yaml:"name"`
	Resources patchResources `yaml:"resources"`
	Env       []patchEnv     `yaml:"env,omitempty"`
}

type patchResources struct {
	Requests map[string]string `yaml:"requests,omitempty"`
	Limits   map[string]string `yaml:"limits,omitempty"`
}

type patchEnv struct {
	Name  string `yaml:"name"`
	Value string `yaml:"value"`
}

type patchHPA struct {
	MinReplicas int  `yaml:"minReplicas"`
	MaxReplicas *int `yaml:"maxReplicas,omitempty"`
	// TargetCPUUtilizationPercent is the autoscaling/v2 Resource metric target.
	TargetCPUUtilizationPercent *int `yaml:"targetCPUUtilizationPercent,omitempty"`
}

// renderResourcesPatch writes the recommendations as patches/resources.yaml.
func renderResourcesPatch(document spec.Recommendations, header string) ([]byte, error) {
	var patch resourcesPatch
	positionOf := map[string]int{}
	for _, recommendation := range document.Recommendations {
		key := workloadKey(recommendation.Target)
		position, ok := positionOf[key]
		if !ok {
			patch.Workloads = append(patch.Workloads, patchWorkload{
				Role: string(recommendation.Target.Role), UpstreamIndex: recommendation.Target.UpstreamIndex,
				TargetType: string(recommendation.Target.TargetType), Namespace: recommendation.Target.Namespace,
			})
			position = len(patch.Workloads) - 1
			positionOf[key] = position
		}
		if err := applyToWorkload(&patch.Workloads[position], recommendation); err != nil {
			return nil, err
		}
	}
	body, err := yaml.Marshal(patch)
	if err != nil {
		return nil, fmt.Errorf("render patches/resources.yaml: %w", err)
	}
	return append([]byte(header), body...), nil
}

func workloadKey(target spec.RecommendationTarget) string {
	if target.UpstreamIndex != nil {
		return fmt.Sprintf("%s%d", target.Role, *target.UpstreamIndex)
	}
	return string(target.Role)
}

func applyToWorkload(workload *patchWorkload, recommendation spec.Recommendation) error {
	value, ok := recommendation.Recommended.(float64)
	if !ok {
		return fmt.Errorf("%s: recommended %v is not a number", recommendation.Id, recommendation.Recommended)
	}
	switch recommendation.Subject.Kind {
	case spec.RecommendationSubjectKindReplicas:
		replicas := int(value)
		workload.Replicas = &replicas
	case spec.RecommendationSubjectKindHpa:
		if workload.HPA == nil {
			workload.HPA = &patchHPA{}
		}
		whole := int(value)
		switch recommendation.Subject.Name {
		case "min_replicas":
			workload.HPA.MinReplicas = whole
		case "max_replicas":
			workload.HPA.MaxReplicas = &whole
		case "cpu_utilization":
			workload.HPA.TargetCPUUtilizationPercent = &whole
		}
	case spec.RecommendationSubjectKindResource, spec.RecommendationSubjectKindEnv:
		container := containerOf(workload, *recommendation.Target.Container)
		applyToContainer(container, recommendation.Subject, value)
	}
	return nil
}

func containerOf(workload *patchWorkload, name string) *patchContainer {
	for i := range workload.Containers {
		if workload.Containers[i].Name == name {
			return &workload.Containers[i]
		}
	}
	workload.Containers = append(workload.Containers, patchContainer{Name: name})
	return &workload.Containers[len(workload.Containers)-1]
}

func applyToContainer(container *patchContainer, subject spec.RecommendationSubject, value float64) {
	if subject.Kind == spec.RecommendationSubjectKindEnv {
		text := derive.FormatNumber(value)
		if subject.Name == "GOMEMLIMIT" {
			text = goMemoryLimitText(value)
		}
		container.Env = append(container.Env, patchEnv{Name: subject.Name, Value: text})
		return
	}
	resource, bound, _ := strings.Cut(subject.Name, ".")
	quantity := CPUQuantity(value)
	if resource == "memory" {
		quantity = MemoryQuantity(value)
	}
	target := &container.Resources.Requests
	if bound == "limit" {
		target = &container.Resources.Limits
	}
	if *target == nil {
		*target = map[string]string{}
	}
	(*target)[resource] = quantity
}

// CPUQuantity writes cores as Kubernetes does: whole cores plainly, else millicores.
func CPUQuantity(cores float64) string {
	if cores == math.Trunc(cores) {
		return derive.FormatNumber(cores)
	}
	return fmt.Sprintf("%dm", int(math.Round(cores*1000)))
}

// MemoryQuantity writes bytes in the largest binary unit that divides them.
func MemoryQuantity(bytes float64) string {
	whole := int64(math.Ceil(bytes))
	for _, unit := range []struct {
		suffix string
		size   int64
	}{{"Gi", 1 << 30}, {"Mi", 1 << 20}, {"Ki", 1 << 10}} {
		if whole%unit.size == 0 {
			return fmt.Sprintf("%d%s", whole/unit.size, unit.suffix)
		}
	}
	return fmt.Sprintf("%d", whole)
}

// goMemoryLimitText writes GOMEMLIMIT in MiB, a suffix the Go runtime reads.
func goMemoryLimitText(bytes float64) string {
	return fmt.Sprintf("%dMiB", int64(bytes)/(1<<20))
}
