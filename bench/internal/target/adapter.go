// Package target captures what a benchmark target looks like before and after
// the load: its Kubernetes workload (read-only), what it reports about itself,
// and the hardware it ran on.
//
// One TargetAdapter exists per target type. The Kubernetes part is the same
// for every type and lives in kubeReader; an adapter adds only the endpoints
// its server exposes.
package target

import (
	"context"
	"fmt"
	"net/http"

	"github.com/servtuner/servtuner/bench/internal/spec"

	"k8s.io/client-go/kubernetes"
)

// Prometheus catalog names. Defined once; the catalog files are named
// after them.
const (
	CatalogKubernetes = "kubernetes"
	CatalogVLLM       = "vllm"
	CatalogLiteLLM    = "litellm"
	CatalogBifrost    = "bifrost"
	CatalogSGLang     = "sglang"
)

// Knobs maps a RunSpec knob name (for example "vllm.max_num_seqs") to the value
// captured from the target. A knob that could not be captured is absent, and
// analysis marks it unverified. Numbers are float64, as they
// are after decoding a RunSpec's JSON; booleans and strings are kept as they are.
type Knobs map[string]spec.KnobValue

// Endpoint is how Info reaches a target's HTTP interface.
type Endpoint struct {
	// Client comes from tlsconfig.NewHTTPClient for the target's hop, so the TLS
	// mode is the one the RunSpec declared and is never inferred here.
	Client *http.Client
	// BaseURL is the RunSpec's target.endpoint, with or without a /v1 suffix.
	BaseURL string
	// Authorization is the complete Authorization header value, or empty. The
	// caller resolves it from the Secret named by target.auth; it is never
	// logged or stored.
	Authorization string
}

// Info is what a target reports about itself.
type Info struct {
	// TargetInfo is the schema's target_info of the workload.
	TargetInfo spec.TargetInfo
	// Knobs are the effective values the endpoints reveal; they take precedence
	// over the values read from container args.
	Knobs Knobs
	// ConfigDocuments are redacted target configuration documents for
	// target/config/, keyed by file name (for example "bifrost-config.json").
	// The caller adds the phase to the name, since Info runs before and after
	// the load.
	ConfigDocuments map[string][]byte
}

// TargetAdapter is the per-target-type part of the snapshot.
type TargetAdapter interface {
	// Type is the RunSpec target type this adapter handles.
	Type() spec.TargetType

	// Snapshot reads the workload selected by selector, read-only. The returned
	// workload always has target_type set and is usable even when the error is
	// non-nil: the error then lists what could not be read (for example an
	// HPA the service account may not list).
	Snapshot(ctx context.Context, kube kubernetes.Interface, selector spec.WorkloadSelector) (spec.Workload_2, error)

	// Info queries the target's own endpoints. The returned Info holds what was
	// read even when the error is non-nil.
	Info(ctx context.Context, endpoint Endpoint) (Info, error)

	// KnobsFromWorkload extracts knobs from the captured container args.
	KnobsFromWorkload(workload spec.Workload_2) Knobs

	// DefaultCatalogs lists the Prometheus catalogs added by default for this
	// target type (prometheus.catalogs).
	DefaultCatalogs() []string
}

// AdapterFor returns the adapter of a RunSpec target type.
func AdapterFor(targetType spec.TargetType) (TargetAdapter, error) {
	switch targetType {
	case spec.TargetTypeVllm:
		return newVLLMAdapter(), nil
	case spec.TargetTypeBifrost:
		return newBifrostAdapter(), nil
	case spec.TargetTypeLitellm:
		return newLiteLLMAdapter(), nil
	case spec.TargetTypeOpenaiCompatible:
		return newOpenAICompatibleAdapter(), nil
	case spec.TargetTypeSglang:
		return newSGLangAdapter(), nil
	default:
		return nil, fmt.Errorf("no target adapter for type %q", targetType)
	}
}
