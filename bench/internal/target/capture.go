package target

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"time"

	"github.com/servtuner/servtuner/bench/internal/spec"

	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
)

// NoClusterReason is recorded when no Kubernetes client is available, which is
// the normal case for a subprocess run (bench/CLAUDE.md, "Snapshot").
const NoClusterReason = "no cluster reachable: not running in a cluster and no kubeconfig was given"

// NewKubeClient returns the read-only client for the snapshot, or nil and the
// reason it was skipped. It uses the in-cluster config when bench runs in a
// pod, else the kubeconfig the caller names. It never reads KUBECONFIG or
// ~/.kube/config on its own: a user's kubeconfig points wherever that user
// last worked, and the snapshot must read only the cluster the caller chose.
// A kubeconfig that was named but cannot be used is an error, not a skip.
func NewKubeClient(kubeconfigPath string) (kubernetes.Interface, string, error) {
	config, err := rest.InClusterConfig()
	switch {
	case err == nil:
	case errors.Is(err, rest.ErrNotInCluster) && kubeconfigPath == "":
		return nil, NoClusterReason, nil
	case errors.Is(err, rest.ErrNotInCluster):
		config, err = clientcmd.BuildConfigFromFlags("", kubeconfigPath)
		if err != nil {
			return nil, "", fmt.Errorf("load kubeconfig %s: %w", kubeconfigPath, err)
		}
	default:
		return nil, "", fmt.Errorf("load in-cluster config: %w", err)
	}
	client, err := kubernetes.NewForConfig(config)
	if err != nil {
		return nil, "", fmt.Errorf("build kubernetes client: %w", err)
	}
	return client, "", nil
}

// TargetRef is one workload to capture: the RunSpec's target, or one of its
// upstreams.
type TargetRef struct {
	Adapter TargetAdapter
	// Selector names the Kubernetes workload; nil when the RunSpec gives none.
	Selector *spec.WorkloadSelector
	// Endpoint is where Info reaches the server; nil skips Info (for example an
	// upstream, which the router reaches and bench may not).
	Endpoint *Endpoint
}

// Request describes one snapshot.
type Request struct {
	// Phase is pre or post (target/snapshot-pre.json, snapshot-post.json).
	Phase spec.SnapshotPhase
	// Kube is the read-only client from NewKubeClient; nil skips the Kubernetes part.
	Kube      kubernetes.Interface
	Target    TargetRef
	Upstreams []TargetRef
	// Now is the clock; the caller passes the run's clock source.
	Now func() time.Time
}

// Result is everything one snapshot produced. The caller (the pipeline) writes
// the files; this package returns the documents.
type Result struct {
	// Snapshot is the document of target/snapshot-<phase>.json.
	Snapshot spec.TargetSnapshot
	// Knobs are the captured values of the RunSpec's knobs, by knob name. A knob
	// two workloads disagree on, or that could not be read, is absent.
	Knobs Knobs
	// ConfigDocuments are the redacted files for target/config/, keyed by file
	// name; upstream documents are prefixed "upstream-<index>-".
	ConfigDocuments map[string][]byte
	// KubeSkipReason is non-empty when the Kubernetes part was skipped.
	KubeSkipReason string
	// Problems lists what could not be read, one entry each. They never contain
	// secrets: errors name the path and status, not bodies or headers.
	Problems []string
}

// Capture takes one snapshot of the target and its upstreams. It does not fail
// on a partial read: a workload that cannot be read is recorded with what was
// read, and the reason goes to Result.Problems.
func Capture(ctx context.Context, request Request) Result {
	result := Result{
		Snapshot:        spec.TargetSnapshot{Phase: request.Phase},
		ConfigDocuments: map[string][]byte{},
	}
	if request.Now == nil {
		request.Now = time.Now
	}
	if request.Kube == nil {
		result.KubeSkipReason = NoClusterReason
	}
	var knobSets []Knobs

	targetWorkload, knobs := captureWorkload(ctx, request, request.Target, spec.WorkloadRoleTarget, nil, "", &result)
	result.Snapshot.Workloads = append(result.Snapshot.Workloads, targetWorkload)
	knobSets = append(knobSets, knobs)
	for index, upstream := range request.Upstreams {
		upstreamIndex := index
		workload, knobs := captureWorkload(ctx, request, upstream, spec.WorkloadRoleUpstream, &upstreamIndex, "upstream-"+strconv.Itoa(index)+"-", &result)
		result.Snapshot.Workloads = append(result.Snapshot.Workloads, workload)
		knobSets = append(knobSets, knobs)
	}

	result.Knobs = agreedKnobs(knobSets)
	result.Snapshot.CapturedAt = spec.UtcTimestamp(request.Now().UTC().Format(time.RFC3339Nano))
	return result
}

func captureWorkload(ctx context.Context, request Request, ref TargetRef, role spec.WorkloadRole, upstreamIndex *int, documentPrefix string, result *Result) (spec.Workload_2, Knobs) {
	workload := spec.Workload_2{
		Role:          role,
		UpstreamIndex: upstreamIndex,
		TargetType:    ref.Adapter.Type(),
		Pods:          []spec.Pod{},
	}
	label := string(role)
	if upstreamIndex != nil {
		label += " " + strconv.Itoa(*upstreamIndex)
	}

	if request.Kube != nil && ref.Selector != nil {
		read, err := ref.Adapter.Snapshot(ctx, request.Kube, *ref.Selector)
		read.Role, read.UpstreamIndex = role, upstreamIndex
		workload = read
		if err != nil {
			result.Problems = append(result.Problems, fmt.Sprintf("%s workload: %v", label, err))
		}
	} else if request.Kube != nil {
		result.Problems = append(result.Problems, label+" workload: the RunSpec names no workload selector, so Kubernetes facts were not captured")
	}

	knobs := ref.Adapter.KnobsFromWorkload(workload)
	if ref.Endpoint != nil {
		info, err := ref.Adapter.Info(ctx, *ref.Endpoint)
		if err != nil {
			result.Problems = append(result.Problems, fmt.Sprintf("%s info: %v", label, err))
		}
		if info.TargetInfo.Version != nil || info.TargetInfo.Models != nil || info.TargetInfo.CacheConfig != nil {
			targetInfo := info.TargetInfo
			workload.TargetInfo = &targetInfo
		}
		knobs = overlayKnobs(knobs, info.Knobs)
		for name, document := range info.ConfigDocuments {
			result.ConfigDocuments[documentPrefix+name] = document
		}
	}
	return workload, knobs
}

// UpstreamClient returns the HTTP client for one upstream's hop, built from the
// upstream's own tls (tlsconfig.NewHTTPClient over spec.DecodeTLS of
// upstream.Tls). The mode is the upstream's declaration, never the target's and
// never inferred.
type UpstreamClient func(upstream spec.Upstream) (*http.Client, error)

// RefsFromRunSpec builds the TargetRefs of a RunSpec. client is the HTTP client
// of the target hop (from tlsconfig.NewHTTPClient) and authorization the
// resolved Authorization header value, or empty.
//
// An upstream is captured with the adapter of its type, or openai-compatible
// when it states none. It gets an Info call only when it has an endpoint and
// upstreamClient is non-nil: the call goes through upstreamClient(upstream),
// i.e. through that upstream's own tls. With a nil upstreamClient only the
// Kubernetes facts are captured.
func RefsFromRunSpec(runTarget spec.Target, client *http.Client, authorization string, upstreamClient UpstreamClient) (TargetRef, []TargetRef, error) {
	adapter, err := AdapterFor(runTarget.Type)
	if err != nil {
		return TargetRef{}, nil, err
	}
	targetRef := TargetRef{
		Adapter:  adapter,
		Selector: runTarget.Workload,
		Endpoint: &Endpoint{Client: client, BaseURL: runTarget.Endpoint, Authorization: authorization},
	}
	var upstreams []TargetRef
	for _, upstream := range runTarget.Upstreams {
		upstreamType := spec.TargetTypeOpenaiCompatible
		if upstream.Type != nil {
			upstreamType = *upstream.Type
		}
		upstreamAdapter, err := AdapterFor(upstreamType)
		if err != nil {
			return TargetRef{}, nil, fmt.Errorf("upstream %q: %w", upstream.Name, err)
		}
		ref := TargetRef{Adapter: upstreamAdapter, Selector: upstream.Workload}
		if upstream.Endpoint != nil && upstreamClient != nil {
			upstreamHTTP, err := upstreamClient(upstream)
			if err != nil {
				return TargetRef{}, nil, fmt.Errorf("upstream %q: %w", upstream.Name, err)
			}
			ref.Endpoint = &Endpoint{Client: upstreamHTTP, BaseURL: *upstream.Endpoint}
		}
		upstreams = append(upstreams, ref)
	}
	return targetRef, upstreams, nil
}
