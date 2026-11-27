// Package analyze turns the raw files of a run bundle into analysis.json:
// the data quality gate, the per-stage client
// aggregates with bootstrap confidence intervals, and the capacity model. It
// also holds the quick per-stage summary (SummarizeBundle).
//
// The orchestrator in this package reads a bundle (LoadRun), runs the three
// stages in subpackages (aggregate, quality, capacity) over narrow inputs
// (input, with signals deriving server-side quantities from the Prometheus
// catalog entries), checks its own result (SelfCheck) and writes it validated
// against its schema (AnalyzeBundle). The same raw files always give the same
// bytes: the bootstrap is seeded from the run id.
//
// Behaviours worth knowing:
//
//   - Offered load is the pacer's own integral. The offered request count of a
//     stage is pacer.OfferedHits of the stage pacer built from the RunSpec, so
//     linear ramps and sine stages that are not whole periods are judged
//     against what they really offer; offered_rps is that count divided by the
//     stage length. A poisson stage passes at min(97% of E, E - 3 sqrt(E)) of
//     its expected count E, because its count is random.
//   - Send lag may be unknown. A record with a NaN send lag or a zero
//     scheduled_at (inference-perf) is left out of every send lag figure. A
//     stage with none has no send lag statistics, skips the send lag ceiling and
//     the growth check, and carries an insufficient_samples note saying so.
//   - SLO-max is conservative about missing evidence. A latency SLO whose p95
//     has too few samples (Rule A) has no observed value and counts as not met,
//     so a stage that cannot show an SLO holds is never the SLO-max. The
//     ladder is read upward and stops at the first failing usable stage.
//   - Without a snapshot, every pod that is not the bench pod counts as a
//     target pod when separating the shared kubernetes catalog's series. With
//     one, the target pods are those of the snapshot's workloads.
//   - DCGM series are attributed by exported_pod when it is set (the stock
//     exporter's pod label is its own pod), else by pod; a series with neither
//     is taken as node-level.
//   - Series with no collected entry are "no evidence", never zero: a check
//     that needs one is skipped and the run notes it.
package analyze
