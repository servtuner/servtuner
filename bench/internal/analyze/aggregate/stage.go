package aggregate

import (
	"fmt"
	"math"
	"sort"
	"time"

	"github.com/servtuner/servtuner/bench/internal/analyze/input"
	"github.com/servtuner/servtuner/bench/internal/engine"
	"github.com/servtuner/servtuner/bench/internal/spec"
	"github.com/servtuner/servtuner/bench/internal/stats"
)

// SLOThresholds are the RunSpec's SLOs in seconds (and a ratio for the error
// rate). A nil member is not configured.
type SLOThresholds struct {
	TTFTP95Seconds *float64
	E2EP95Seconds  *float64
	ITLP95Seconds  *float64
	MaxErrorRate   *float64
}

// SLOThresholdsFromSpec converts the RunSpec's slos block.
func SLOThresholdsFromSpec(slos *spec.Slos) (SLOThresholds, error) {
	var thresholds SLOThresholds
	if slos == nil {
		return thresholds, nil
	}
	for _, entry := range []struct {
		name   string
		source *spec.Duration
		target **float64
	}{
		{"ttft_p95", slos.TtftP95, &thresholds.TTFTP95Seconds},
		{"e2e_p95", slos.E2EP95, &thresholds.E2EP95Seconds},
		{"itl_p95", slos.ItlP95, &thresholds.ITLP95Seconds},
	} {
		if entry.source == nil {
			continue
		}
		parsed, err := entry.source.Std()
		if err != nil {
			return SLOThresholds{}, fmt.Errorf("slos.%s: %w", entry.name, err)
		}
		seconds := parsed.Seconds()
		*entry.target = &seconds
	}
	thresholds.MaxErrorRate = slos.MaxErrorRate
	return thresholds, nil
}

// StageInput is one stage's window and the records scheduled in it.
type StageInput struct {
	RunID string
	Stage input.Stage
	Rows  []engine.RequestRow
	SLOs  SLOThresholds
}

// StageResult is the aggregate and the figures the quality gate and the
// capacity model reuse.
type StageResult struct {
	Aggregate spec.StageAggregate
	// MeanE2ESeconds is the mean E2E of the successful requests; it sets the
	// Rule B trim and the 10 x E2E term of the resolution rule.
	MeanE2ESeconds float64
}

// unixEpoch bounds real schedule times from below. A zero time.Time written to
// Parquet as nanoseconds since 1970 overflows int64 and reads back as
// 1754-08-30, so IsZero() misses it after a round trip; any schedule before the
// epoch is the old "unknown" marker, not a measurement.
var unixEpoch = time.Unix(0, 0)

// SendLag returns a record's send lag and whether it means something. Some
// engines (inference-perf) report no schedule: scheduled_at and the lag are
// null (nil here). Bundles written before the columns were nullable carry the
// zero time and a NaN lag instead, which is also unknown. Such a row is left
// out of every send lag figure, so a stage whose engine reports none has no
// send lag statistics, not "NaN" ones.
func SendLag(row engine.RequestRow) (float64, bool) {
	if row.ScheduledAt == nil || row.ScheduledAt.Before(unixEpoch) || row.SendLagSeconds == nil {
		return 0, false
	}
	lag := *row.SendLagSeconds
	return lag, !math.IsNaN(lag) && !math.IsInf(lag, 0)
}

// IsSendLagKnown says whether a record's send lag means something (see SendLag).
func IsSendLagKnown(row engine.RequestRow) bool {
	_, known := SendLag(row)
	return known
}

// IsWarmupRow says whether a record was sent during warmup. Warmup rows are
// kept in the raw data but belong to no stage.
func IsWarmupRow(row engine.RequestRow) bool {
	return row.Phase == engine.RowPhaseWarmup
}

// TTFT is the row's time to first token as the stage statistics count it:
// present, and finite along with the row's end-to-end latency.
func TTFT(row engine.RequestRow) (float64, bool) {
	if row.TTFTSeconds == nil || !finite(*row.TTFTSeconds, row.E2ESeconds) {
		return 0, false
	}
	return *row.TTFTSeconds, true
}

// finite keeps NaN and infinite values out of a statistic.
func finite(values ...float64) bool {
	for _, value := range values {
		if math.IsNaN(value) || math.IsInf(value, 0) {
			return false
		}
	}
	return true
}

// IsFailedRequest says whether a record counts as an error: no response, an
// HTTP error status, or an error class from the engine.
func IsFailedRequest(row engine.RequestRow) bool {
	return row.Error != nil || row.Status == 0 || row.Status >= 400
}

// ErrorClass names the class of a failed request: the engine's error class
// when it has one, otherwise the HTTP status.
func ErrorClass(row engine.RequestRow) string {
	switch {
	case row.Error != nil:
		return *row.Error
	case row.Status == 0:
		return "no_response"
	default:
		return HTTPErrorClass(int(row.Status))
	}
}

// HTTPErrorClass is the error class of a response with an HTTP error status.
func HTTPErrorClass(status int) string {
	return fmt.Sprintf("http_%d", status)
}

// AggregateStage computes the client statistics of one stage over every
// request scheduled in it. Requests are attributed to
// the stage that scheduled them, so the client statistics need no edge
// trimming; trimming applies to the server-side series (TrimWindow).
func AggregateStage(in StageInput) StageResult {
	seconds := in.Stage.Duration().Seconds()
	aggregate := spec.StageAggregate{
		StageIndex: in.Stage.Index,
		OfferedRps: in.Stage.OfferedRPS,
		Requests:   len(in.Rows),
	}
	if seconds > 0 {
		aggregate.AchievedRps = float64(len(in.Rows)) / seconds
	}

	var series stageSeries
	classes := map[string]int{}
	var good int
	for _, row := range in.Rows {
		if lag, known := SendLag(row); known {
			series.sendLag = append(series.sendLag, lag)
		}
		series.addTokens(row)
		if IsFailedRequest(row) {
			aggregate.Errors++
			classes[ErrorClass(row)]++
			continue
		}
		series.addLatencies(row)
		if in.SLOs.meetsLatency(row) {
			good++
		}
	}
	for _, class := range sortedKeys(classes) {
		aggregate.ErrorsByClass = append(aggregate.ErrorsByClass, spec.ErrorClassCount{ErrorClass: class, Count: classes[class]})
	}
	if seconds > 0 && len(in.Rows) > 0 {
		goodput := float64(good) / seconds
		aggregate.GoodputRps = &goodput
		series.setTokensPerSecond(&aggregate, seconds)
	}

	label := func(metric string) string { return fmt.Sprintf("stage-%d/%s", in.Stage.Index, metric) }
	aggregate.Ttft = Summarize(in.RunID, label("ttft"), series.ttft)
	aggregate.Itl = Summarize(in.RunID, label("itl"), series.itl)
	aggregate.Tpot = Summarize(in.RunID, label("tpot"), series.tpot)
	aggregate.E2E = Summarize(in.RunID, label("e2e"), series.e2e)
	setSendLag(&aggregate, series.sendLag)
	aggregate.SloResults = in.SLOs.evaluate(aggregate)
	return StageResult{Aggregate: aggregate, MeanE2ESeconds: Mean(series.e2e)}
}

// stageSeries collects the per-request values of one stage.
type stageSeries struct {
	ttft, itl, tpot, e2e, sendLag []float64
	inputTokens, outputTokens     int64
	hasInputTokens                bool
	hasOutputTokens               bool
}

func (s *stageSeries) addTokens(row engine.RequestRow) {
	if row.InputTokens != nil {
		s.inputTokens += *row.InputTokens
		s.hasInputTokens = true
	}
	if row.OutputTokens != nil {
		s.outputTokens += *row.OutputTokens
		s.hasOutputTokens = true
	}
}

func (s *stageSeries) addLatencies(row engine.RequestRow) {
	if finite(row.E2ESeconds) {
		s.e2e = append(s.e2e, row.E2ESeconds)
	}
	for _, gap := range row.ITLSeconds {
		if finite(gap) {
			s.itl = append(s.itl, gap)
		}
	}
	ttft, ok := TTFT(row)
	if !ok {
		return
	}
	s.ttft = append(s.ttft, ttft)
	// TPOT: the time per output token after the first one, so it
	// needs the first-token time and at least two output tokens.
	if row.OutputTokens != nil && *row.OutputTokens > 1 {
		s.tpot = append(s.tpot, max(row.E2ESeconds-*row.TTFTSeconds, 0)/float64(*row.OutputTokens-1))
	}
}

func (s *stageSeries) setTokensPerSecond(aggregate *spec.StageAggregate, seconds float64) {
	if s.hasInputTokens {
		perSecond := float64(s.inputTokens) / seconds
		aggregate.InputTokensPerSecond = &perSecond
	}
	if s.hasOutputTokens {
		perSecond := float64(s.outputTokens) / seconds
		aggregate.OutputTokensPerSecond = &perSecond
	}
	if s.hasInputTokens || s.hasOutputTokens {
		perSecond := float64(s.inputTokens+s.outputTokens) / seconds
		aggregate.TotalTokensPerSecond = &perSecond
	}
}

func setSendLag(aggregate *spec.StageAggregate, sendLag []float64) {
	if len(sendLag) == 0 {
		return
	}
	sorted := stats.SortedCopy(sendLag)
	p50, p99, maximum := stats.NearestRank(sorted, 0.50), stats.NearestRank(sorted, 0.99), sorted[len(sorted)-1]
	aggregate.SendLagP50Seconds, aggregate.SendLagP99Seconds, aggregate.SendLagMaxSeconds = &p50, &p99, &maximum
}

// meetsLatency says whether a successful request is within every configured
// latency SLO. The SLOs are p95 targets, so a request is judged against the
// same thresholds: its TTFT, its E2E, and its own p95 inter-token latency.
// Without a latency SLO every successful request counts as good.
func (t SLOThresholds) meetsLatency(row engine.RequestRow) bool {
	if t.TTFTP95Seconds != nil && (row.TTFTSeconds == nil || *row.TTFTSeconds > *t.TTFTP95Seconds) {
		return false
	}
	if t.E2EP95Seconds != nil && row.E2ESeconds > *t.E2EP95Seconds {
		return false
	}
	if t.ITLP95Seconds != nil && len(row.ITLSeconds) > 0 && stats.Percentile(row.ITLSeconds, 0.95) > *t.ITLP95Seconds {
		return false
	}
	return true
}

// evaluate judges the stage against each configured SLO. A latency SLO with
// too few samples for its percentile has no observed value and is not met,
// because it cannot be shown to hold.
func (t SLOThresholds) evaluate(aggregate spec.StageAggregate) []spec.SloResult {
	var results []spec.SloResult
	latency := func(name spec.SloName, threshold *float64, summary *spec.LatencySummary) {
		if threshold == nil {
			return
		}
		result := spec.SloResult{Slo: name, Threshold: *threshold}
		if summary != nil && summary.P95Seconds.Value != nil {
			result.Observed = summary.P95Seconds.Value
			result.IsMet = *summary.P95Seconds.Value <= *threshold
		}
		results = append(results, result)
	}
	latency(spec.SloNameTtftP95, t.TTFTP95Seconds, aggregate.Ttft)
	latency(spec.SloNameE2EP95, t.E2EP95Seconds, aggregate.E2E)
	latency(spec.SloNameItlP95, t.ITLP95Seconds, aggregate.Itl)
	if t.MaxErrorRate != nil {
		result := spec.SloResult{Slo: spec.SloNameMaxErrorRate, Threshold: *t.MaxErrorRate}
		if aggregate.Requests > 0 {
			rate := float64(aggregate.Errors) / float64(aggregate.Requests)
			result.Observed = &rate
			result.IsMet = rate <= *t.MaxErrorRate
		}
		results = append(results, result)
	}
	return results
}

func sortedKeys(counts map[string]int) []string {
	keys := make([]string, 0, len(counts))
	for key := range counts {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}
