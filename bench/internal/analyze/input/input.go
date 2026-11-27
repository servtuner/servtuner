// Package input defines the narrow interfaces through which the analyzer reads
// a run's raw data. The analyzer never opens files itself: the bundle loader in
// the parent package (and the tests) hand it these values, so the parquet
// reader, the Prometheus results and the snapshots can each be swapped without
// touching a check.
package input

import (
	"fmt"
	"iter"
	"sort"
	"time"

	"github.com/servtuner/servtuner/bench/internal/engine"
	"github.com/servtuner/servtuner/bench/internal/spec"
)

// RecordSource yields the request records of a run, one at a time, in any
// order. A read error ends the iteration and is yielded as the last pair. The
// parquet reader of P1-records implements it for requests.parquet; the tests
// use SliceRecords.
type RecordSource interface {
	Records() iter.Seq2[engine.RequestRow, error]
}

// SliceRecords is an in-memory RecordSource.
type SliceRecords []engine.RequestRow

// Records implements RecordSource.
func (s SliceRecords) Records() iter.Seq2[engine.RequestRow, error] {
	return func(yield func(engine.RequestRow, error) bool) {
		for _, row := range s {
			if !yield(row, nil) {
				return
			}
		}
	}
}

// Stage is the window of one load stage, from phases.json.
type Stage struct {
	Index      int
	OfferedRPS float64
	Start      time.Time
	End        time.Time
}

// Duration is the length of the stage window.
func (s Stage) Duration() time.Duration { return s.End.Sub(s.Start) }

// StagesFromPhases lists the stage phases of phases.json ordered by stage
// index. Warmup and cooldown are not stages and never enter the analysis.
func StagesFromPhases(phases spec.Phases) ([]Stage, error) {
	var stages []Stage
	for _, phase := range phases.Phases {
		if phase.Kind != spec.PhaseKindStage {
			continue
		}
		if phase.StageIndex == nil || phase.OfferedRps == nil {
			return nil, fmt.Errorf("phase %s: a stage needs stage_index and offered_rps", phase.Name)
		}
		start, err := phase.Start.Time()
		if err != nil {
			return nil, fmt.Errorf("phase %s start: %w", phase.Name, err)
		}
		end, err := phase.End.Time()
		if err != nil {
			return nil, fmt.Errorf("phase %s end: %w", phase.Name, err)
		}
		if !end.After(start) {
			return nil, fmt.Errorf("phase %s: end %s is not after start %s", phase.Name, phase.End, phase.Start)
		}
		stages = append(stages, Stage{Index: *phase.StageIndex, OfferedRPS: *phase.OfferedRps, Start: start, End: end})
	}
	sort.Slice(stages, func(i, j int) bool { return stages[i].Index < stages[j].Index })
	for i := 1; i < len(stages); i++ {
		if stages[i].Index == stages[i-1].Index {
			return nil, fmt.Errorf("phases.json lists stage %d twice", stages[i].Index)
		}
	}
	return stages, nil
}

// Sample is one point of a time series.
type Sample struct {
	At    time.Time
	Value float64
}

// SeriesRef names one collected catalog entry: the catalog and the entry name
// that prometheus/queries.json and prometheus/<catalog>/<name>.json use.
type SeriesRef struct {
	Catalog spec.CatalogName
	Name    string
}

// Series is one labelled series of a range-query result.
type Series struct {
	Labels  map[string]string
	Samples []Sample
}

// MatrixSource reads the raw result of a catalog entry: every series of the
// range query with its labels, samples sorted by time. found is false when the
// entry was not collected (an absent optional catalog, a query that returned
// nothing); a check that needs it then has no evidence and must say so, never
// assume zero. The bundle loader reads prometheus/<catalog>/<name>.json.
type MatrixSource interface {
	Matrix(ref SeriesRef) (series []Series, found bool, err error)
}

// MapMatrix is an in-memory MatrixSource.
type MapMatrix map[SeriesRef][]Series

// Matrix implements MatrixSource.
func (m MapMatrix) Matrix(ref SeriesRef) ([]Series, bool, error) {
	series, found := m[ref]
	return series, found && len(series) > 0, nil
}

// NoMatrix is a MatrixSource with nothing collected, for runs without Prometheus.
type NoMatrix struct{}

// Matrix implements MatrixSource.
func (NoMatrix) Matrix(SeriesRef) ([]Series, bool, error) { return nil, false, nil }

// Run is everything the analyzer reads about one run. Optional members are nil
// when the run did not produce them (a subprocess run has no snapshots and no
// Prometheus).
type Run struct {
	// ID seeds the bootstrap, so a recomputation gives the same intervals.
	ID      string
	Spec    spec.RunSpec
	Phases  spec.Phases
	Records RecordSource
	// Matrix is never nil; use NoMatrix.
	Matrix MatrixSource
	// BenchPod is the bench pod's name (environment.json "pod"), which tells its
	// series from the target's in the shared kubernetes catalog. Empty when the
	// run was not in a pod.
	BenchPod string
	// ShardPods are a sharded run's follower bench pods (environment.json
	// "shard_pods").
	ShardPods []string
	// EngineMaxInFlight is the engine's in-flight cap (environment.json
	// engine.max_in_flight); 0 when the engine has none or did not record it.
	EngineMaxInFlight int
	// Targets and Queries are prometheus/targets.json and prometheus/queries.json.
	Targets *spec.PrometheusTargets
	Queries *spec.PrometheusQueries
	// PreSnapshot and PostSnapshot are target/snapshot-pre.json and snapshot-post.json.
	PreSnapshot  *spec.TargetSnapshot
	PostSnapshot *spec.TargetSnapshot
	// CapturedKnobs maps a knob name such as "vllm.max_num_seqs" to the value
	// captured from the target, as target.Capture reports it. A knob that is
	// absent could not be captured.
	CapturedKnobs map[string]spec.KnobValue
	// ConfigDocuments are the target's redacted config documents captured
	// before the run (target/config/pre-*), by document name such as
	// "bifrost-config.json". Empty when the adapter has none.
	ConfigDocuments map[string][]byte
	// InfraPrices is pricing/infra.json, the prices the run resolved; nil
	// when the run has none.
	InfraPrices *spec.InfraPricing
	// Datasheet is pricing/datasheet.json as stored, nil when the run fetched
	// none (token_pricing source none, or the fetch failed).
	Datasheet []byte
	// TokenPricing says where Datasheet came from and when (pricing/token.json); nil without one.
	TokenPricing *spec.TokenPricing
}
