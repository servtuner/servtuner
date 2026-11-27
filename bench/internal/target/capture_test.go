package target

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/servtuner/servtuner/bench/internal/spec"

	"k8s.io/client-go/kubernetes/fake"
)

var fixedNow = func() time.Time { return time.Date(2026, 10, 29, 12, 0, 0, 123, time.UTC) }

func TestCaptureTargetAndUpstreamWithRoutersAndModelServer(t *testing.T) {
	cluster := append(vllmCluster(), bifrostPod())
	bifrost, _ := AdapterFor(spec.TargetTypeBifrost)
	vllm, _ := AdapterFor(spec.TargetTypeVllm)

	forEachTransportMode(t, serveFixtures(t, bifrostFixtures(), ""), func(t *testing.T, endpoint Endpoint) {
		result := Capture(context.Background(), Request{
			Phase: spec.SnapshotPhasePre,
			Kube:  fake.NewSimpleClientset(cluster...),
			Target: TargetRef{
				Adapter:  bifrost,
				Selector: &spec.WorkloadSelector{Namespace: testNamespace, Selector: spec.WorkloadSelectorSelector{"app": "bifrost"}},
				Endpoint: &endpoint,
			},
			Upstreams: []TargetRef{{Adapter: vllm, Selector: &vllmSelector}},
			Now:       fixedNow,
		})
		if len(result.Problems) != 0 {
			t.Fatalf("problems: %v", result.Problems)
		}
		workloads := result.Snapshot.Workloads
		if len(workloads) != 2 || workloads[0].Role != spec.WorkloadRoleTarget || workloads[0].UpstreamIndex != nil {
			t.Fatalf("workloads = %+v", workloads)
		}
		if workloads[1].Role != spec.WorkloadRoleUpstream || workloads[1].UpstreamIndex == nil || *workloads[1].UpstreamIndex != 0 || workloads[1].TargetType != spec.TargetTypeVllm {
			t.Errorf("upstream workload = role %s index %v type %s", workloads[1].Role, workloads[1].UpstreamIndex, workloads[1].TargetType)
		}
		if workloads[0].TargetInfo == nil || *workloads[0].TargetInfo.Version != "v2.3.2-test" {
			t.Errorf("target_info = %+v", workloads[0].TargetInfo)
		}
		// vLLM knobs come from the upstream's args, Bifrost's from its config API.
		if result.Knobs[KnobVLLMMaxNumSeqs] != 256.0 || result.Knobs[KnobBifrostConcurrency] != 1000.0 {
			t.Errorf("knobs = %v", result.Knobs)
		}
		if _, has := result.ConfigDocuments[BifrostConfigDocument]; !has {
			t.Errorf("config documents = %v", result.ConfigDocuments)
		}
		if string(result.Snapshot.CapturedAt) != "2026-10-29T12:00:00.000000123Z" {
			t.Errorf("captured_at = %s", result.Snapshot.CapturedAt)
		}

		document, err := json.Marshal(result.Snapshot)
		if err != nil {
			t.Fatal(err)
		}
		if err := spec.Validate(spec.SchemaTargetSnapshot, document); err != nil {
			t.Errorf("snapshot does not satisfy the schema: %v\n%s", err, document)
		}
		assertNoSecrets(t, string(document))
		for name, content := range result.ConfigDocuments {
			assertNoSecrets(t, name+string(content))
		}
	})
}

func TestCaptureSkipsKubernetesCleanlyWithNoCluster(t *testing.T) {
	adapter, _ := AdapterFor(spec.TargetTypeVllm)
	forEachTransportMode(t, serveFixtures(t, vllmFixtures(), ""), func(t *testing.T, endpoint Endpoint) {
		result := Capture(context.Background(), Request{
			Phase:  spec.SnapshotPhasePost,
			Target: TargetRef{Adapter: adapter, Selector: &vllmSelector, Endpoint: &endpoint},
			Now:    fixedNow,
		})
		if result.KubeSkipReason != NoClusterReason || len(result.Problems) != 0 {
			t.Errorf("skip reason = %q problems = %v", result.KubeSkipReason, result.Problems)
		}
		workload := result.Snapshot.Workloads[0]
		if len(workload.Pods) != 0 || workload.HardwareProfile != nil || workload.TargetInfo == nil {
			t.Errorf("workload = %+v", workload)
		}
		if result.Knobs[KnobVLLMMaxNumSeqs] != 128.0 {
			t.Errorf("target-reported knobs must still be captured: %v", result.Knobs)
		}
		document, _ := json.Marshal(result.Snapshot)
		if err := spec.Validate(spec.SchemaTargetSnapshot, document); err != nil {
			t.Errorf("skipped snapshot does not satisfy the schema: %v\n%s", err, document)
		}
	})
}

func TestCaptureRecordsProblemsWithoutFailing(t *testing.T) {
	adapter, _ := AdapterFor(spec.TargetTypeVllm)
	forEachTransportMode(t, serveFixtures(t, map[string]string{}, ""), func(t *testing.T, endpoint Endpoint) {
		result := Capture(context.Background(), Request{
			Phase:  spec.SnapshotPhasePre,
			Kube:   fake.NewSimpleClientset(),
			Target: TargetRef{Adapter: adapter, Endpoint: &endpoint},
			Now:    fixedNow,
		})
		joined := strings.Join(result.Problems, "\n")
		if !strings.Contains(joined, "names no workload selector") || !strings.Contains(joined, "/version") {
			t.Errorf("problems = %v", result.Problems)
		}
		if len(result.Snapshot.Workloads) != 1 {
			t.Errorf("a snapshot always has the target workload: %+v", result.Snapshot.Workloads)
		}
	})
}

func TestKnobsOfWorkloadsThatDisagreeAreAbsent(t *testing.T) {
	adapter, _ := AdapterFor(spec.TargetTypeVllm)
	cluster := vllmCluster()
	other := vllmPod("vllm-other", "node-1", []string{"--max-num-seqs", "64"})
	other.Namespace = "elsewhere"
	result := Capture(context.Background(), Request{
		Phase:     spec.SnapshotPhasePre,
		Kube:      fake.NewSimpleClientset(append(cluster, other)...),
		Target:    TargetRef{Adapter: adapter, Selector: &vllmSelector},
		Upstreams: []TargetRef{{Adapter: adapter, Selector: &spec.WorkloadSelector{Namespace: "elsewhere", Selector: spec.WorkloadSelectorSelector{"app": "vllm"}}}},
		Now:       fixedNow,
	})
	if _, has := result.Knobs[KnobVLLMMaxNumSeqs]; has {
		t.Errorf("conflicting workloads must leave the knob absent: %v", result.Knobs)
	}
}

func TestRefsFromRunSpec(t *testing.T) {
	selector := &spec.WorkloadSelector{Namespace: "ns", Selector: spec.WorkloadSelectorSelector{"app": "x"}}
	platform := spec.Tls(spec.TlsPlatform{Mode: "platform"})
	mtls := spec.Tls(spec.TlsMtls{Mode: "mtls", SecretRef: spec.SecretRef{Name: "servtuner-client"}})
	vllm := spec.TargetTypeVllm
	endpoint := "https://vllm.invalid:8000/v1"
	runTarget := spec.Target{
		Type:     spec.TargetTypeBifrost,
		Endpoint: "http://bifrost.invalid:8080/v1",
		Workload: selector,
		Upstreams: []spec.Upstream{
			{Name: "mock", Workload: selector, Tls: platform},
			{Name: "model", Type: &vllm, Endpoint: &endpoint, Tls: mtls},
		},
	}

	targetRef, upstreams, err := RefsFromRunSpec(runTarget, nil, "Bearer x", nil)
	if err != nil {
		t.Fatal(err)
	}
	if targetRef.Adapter.Type() != spec.TargetTypeBifrost || targetRef.Endpoint.Authorization != "Bearer x" || targetRef.Selector != selector {
		t.Errorf("target ref = %+v", targetRef)
	}
	if len(upstreams) != 2 || upstreams[0].Adapter.Type() != spec.TargetTypeOpenaiCompatible || upstreams[1].Adapter.Type() != spec.TargetTypeVllm {
		t.Errorf("an upstream uses the adapter of its type, openai-compatible when it has none: %+v", upstreams)
	}
	if upstreams[0].Endpoint != nil || upstreams[1].Endpoint != nil {
		t.Errorf("without an upstream client no upstream gets an Info call: %+v", upstreams)
	}

	var asked []spec.Upstream
	client := &http.Client{}
	_, upstreams, err = RefsFromRunSpec(runTarget, nil, "", func(upstream spec.Upstream) (*http.Client, error) {
		asked = append(asked, upstream)
		return client, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(asked) != 1 || asked[0].Name != "model" {
		t.Fatalf("the client is built only for the upstream with an endpoint: %+v", asked)
	}
	if mode, err := spec.ModeOf(asked[0].Tls); err != nil || mode != spec.TransportModeMtls {
		t.Errorf("the client must be built from the upstream's own tls, got mode %v, %v", mode, err)
	}
	if upstreams[0].Endpoint != nil || upstreams[1].Endpoint == nil || upstreams[1].Endpoint.Client != client ||
		upstreams[1].Endpoint.BaseURL != endpoint || upstreams[1].Endpoint.Authorization != "" {
		t.Errorf("upstream refs = %+v", upstreams)
	}

	failing := func(spec.Upstream) (*http.Client, error) { return nil, errors.New("no certificate") }
	if _, _, err := RefsFromRunSpec(runTarget, nil, "", failing); err == nil {
		t.Error("an upstream whose client cannot be built must be an error, never a fallback to another mode")
	}
	if _, _, err := RefsFromRunSpec(spec.Target{Type: "nonsense"}, nil, "", nil); err == nil {
		t.Error("an unknown target type must be an error")
	}
}

func TestNewKubeClientSkipsWithoutAClusterOrKubeconfig(t *testing.T) {
	t.Setenv("KUBERNETES_SERVICE_HOST", "")
	t.Setenv("KUBERNETES_SERVICE_PORT", "")
	// A user kubeconfig in the environment must be ignored.
	t.Setenv("KUBECONFIG", t.TempDir()+"/must-not-be-read")
	client, reason, err := NewKubeClient("")
	if err != nil || client != nil || reason != NoClusterReason {
		t.Errorf("client = %v reason = %q err = %v", client, reason, err)
	}
}

func TestNewKubeClientErrorsOnANamedKubeconfigThatCannotBeLoaded(t *testing.T) {
	t.Setenv("KUBERNETES_SERVICE_HOST", "")
	t.Setenv("KUBERNETES_SERVICE_PORT", "")
	client, _, err := NewKubeClient(t.TempDir() + "/missing")
	if err == nil || client != nil {
		t.Errorf("want an error, got client %v err %v", client, err)
	}
}

func assertNoSecrets(t *testing.T, text string) {
	t.Helper()
	for _, secret := range sentinelSecrets {
		if strings.Contains(text, secret) {
			t.Errorf("output contains %q", secret)
		}
	}
}
