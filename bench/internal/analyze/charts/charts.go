// Package charts builds the UI's chart-ready files from a
// run's request records: charts/client_timeseries.json,
// charts/histograms.json and charts/inflight.json. The frontend never computes from raw data, so these
// are its only view of the records; the statistics with intervals stay in
// analysis.json.
//
// One pass over the records fills both. Warmup requests and requests of no
// stage are left out, as in the stage statistics.
package charts

import (
	"fmt"
	"math"
	"sort"
	"time"

	"github.com/servtuner/servtuner/bench/internal/analyze/aggregate"
	"github.com/servtuner/servtuner/bench/internal/analyze/input"
	"github.com/servtuner/servtuner/bench/internal/engine"
	"github.com/servtuner/servtuner/bench/internal/spec"
	"github.com/servtuner/servtuner/bench/internal/stats"
)

// Bucket sizing: 1 s buckets for short runs and 5 s otherwise,
// widened so no series has more than MaxBuckets points.
const (
	ShortRunSeconds    = 600
	ShortBucketSeconds = 1
	LongBucketSeconds  = 5
	MaxBuckets         = 2000
)

// Histogram edges: EdgesPerDecade logarithmic edges per decade from
// 10^MinEdgeExponent to 10^MaxEdgeExponent seconds (1 ms to 1000 s).
const (
	EdgesPerDecade  = 20
	MinEdgeExponent = -3
	MaxEdgeExponent = 3
)

// Edges returns the histogram bucket edges in seconds.
func Edges() []float64 {
	count := (MaxEdgeExponent-MinEdgeExponent)*EdgesPerDecade + 1
	edges := make([]float64, count)
	for i := range edges {
		edges[i] = math.Pow(10, float64(MinEdgeExponent)+float64(i)/EdgesPerDecade)
	}
	return edges
}

// Stage is a stage window with what its band shows.
type Stage struct {
	Window     input.Stage
	IsValid    bool
	IsExcluded bool
}

// BucketSeconds picks the bucket width for stages spanning duration.
func BucketSeconds(duration time.Duration) int {
	seconds := duration.Seconds()
	width := LongBucketSeconds
	if seconds <= ShortRunSeconds {
		width = ShortBucketSeconds
	}
	if seconds/float64(width) > MaxBuckets {
		width = int(math.Ceil(seconds / MaxBuckets))
	}
	return width
}

type bucket struct {
	requests, errors int
	outputTokens     int64
	hasOutputTokens  bool
	ttft, e2e, lag   []float64
}

type stageCounts struct {
	ttft, e2e, itl []int
	hasTTFT        bool
	hasITL         bool
}

// Documents are the chart files Build writes.
type Documents struct {
	Timeseries spec.ClientTimeseries
	Histograms spec.LatencyHistograms
	InFlight   spec.InFlightChart
}

// Build returns the chart documents. stages must be in index order and not
// empty.
func Build(stages []Stage, records input.RecordSource, analyzerVersion string) (Documents, error) {
	if len(stages) == 0 {
		return Documents{}, fmt.Errorf("no stages to chart")
	}
	start := stages[0].Window.Start
	duration := stages[len(stages)-1].Window.End.Sub(start)
	width := BucketSeconds(duration)
	count := max(int(math.Ceil(duration.Seconds()/float64(width))), 1)
	buckets := make([]bucket, count)
	edges := Edges()
	histograms := make([]stageCounts, len(stages))
	flight := newInFlight(stages, start, duration)
	position := map[int]int{}
	for i, stage := range stages {
		position[stage.Window.Index] = i
		histograms[i] = stageCounts{ttft: make([]int, len(edges)+1), e2e: make([]int, len(edges)+1), itl: make([]int, len(edges)+1)}
	}
	if records != nil {
		for row, err := range records.Records() {
			if err != nil {
				return Documents{}, fmt.Errorf("read request records: %w", err)
			}
			if row.StageIndex == nil || aggregate.IsWarmupRow(row) {
				continue
			}
			flight.add(row, position)
			if index := int(row.SentAt.Sub(start).Seconds()) / width; index >= 0 && index < count && !row.SentAt.Before(start) {
				buckets[index].add(row)
			}
			if i, ok := position[int(*row.StageIndex)]; ok {
				histograms[i].add(row, edges)
			}
		}
	}
	return Documents{
		Timeseries: timeseries(stages, buckets, start, width, analyzerVersion),
		Histograms: histogramsDocument(stages, histograms, edges, analyzerVersion),
		InFlight:   flight.document(analyzerVersion),
	}, nil
}

func (b *bucket) add(row engine.RequestRow) {
	b.requests++
	if aggregate.IsFailedRequest(row) {
		b.errors++
	}
	if ttft, ok := aggregate.TTFT(row); ok {
		b.ttft = append(b.ttft, ttft)
	}
	if isFinite(row.E2ESeconds) {
		b.e2e = append(b.e2e, row.E2ESeconds)
	}
	if lag, ok := aggregate.SendLag(row); ok && isFinite(lag) {
		b.lag = append(b.lag, lag)
	}
	if row.OutputTokens != nil {
		b.outputTokens += *row.OutputTokens
		b.hasOutputTokens = true
	}
}

func (c *stageCounts) add(row engine.RequestRow, edges []float64) {
	if ttft, ok := aggregate.TTFT(row); ok {
		c.ttft[bin(edges, ttft)]++
		c.hasTTFT = true
	}
	if isFinite(row.E2ESeconds) {
		c.e2e[bin(edges, row.E2ESeconds)]++
	}
	for _, gap := range row.ITLSeconds {
		if isFinite(gap) {
			c.itl[bin(edges, gap)]++
			c.hasITL = true
		}
	}
}

// bin is the counts index of value: the number of edges at or below it.
func bin(edges []float64, value float64) int {
	return sort.Search(len(edges), func(i int) bool { return edges[i] > value })
}

func isFinite(value float64) bool { return !math.IsNaN(value) && !math.IsInf(value, 0) }

func timeseries(stages []Stage, buckets []bucket, start time.Time, width int, analyzerVersion string) spec.ClientTimeseries {
	document := spec.ClientTimeseries{
		AnalyzerVersion: analyzerVersion, Start: spec.NewUTCTimestamp(start), BucketSeconds: width,
		Stages: make([]spec.StageBand, len(stages)), Buckets: make([]spec.TimeBucket, len(buckets)),
	}
	for i, stage := range stages {
		document.Stages[i] = spec.StageBand{
			StageIndex: stage.Window.Index, OfferedRps: stage.Window.OfferedRPS, IsValid: stage.IsValid, IsExcluded: stage.IsExcluded,
			StartOffsetSeconds: stage.Window.Start.Sub(start).Seconds(), EndOffsetSeconds: stage.Window.End.Sub(start).Seconds(),
		}
	}
	for i, b := range buckets {
		offset := float64(i * width)
		point := spec.TimeBucket{OffsetSeconds: offset, Requests: b.requests, Errors: b.errors, AchievedRps: float64(b.requests) / float64(width)}
		for _, stage := range document.Stages {
			if offset >= stage.StartOffsetSeconds && offset < stage.EndOffsetSeconds {
				index := stage.StageIndex
				point.StageIndex = &index
				break
			}
		}
		point.TtftSeconds = percentiles(b.ttft)
		point.E2ESeconds = percentiles(b.e2e)
		if len(b.lag) > 0 {
			p99 := stats.NearestRank(stats.SortedCopy(b.lag), 0.99)
			point.SendLagP99Seconds = &p99
		}
		if b.hasOutputTokens {
			rate := float64(b.outputTokens) / float64(width)
			point.OutputTokensPerSecond = &rate
		}
		document.Buckets[i] = point
	}
	return document
}

func percentiles(values []float64) *spec.BucketPercentiles {
	if len(values) == 0 {
		return nil
	}
	sorted := stats.SortedCopy(values)
	return &spec.BucketPercentiles{
		Samples: len(sorted), P50: stats.NearestRank(sorted, 0.50), P95: stats.NearestRank(sorted, 0.95), P99: stats.NearestRank(sorted, 0.99),
	}
}

func histogramsDocument(stages []Stage, counts []stageCounts, edges []float64, analyzerVersion string) spec.LatencyHistograms {
	document := spec.LatencyHistograms{AnalyzerVersion: analyzerVersion, EdgesSeconds: edges, Stages: make([]spec.StageHistograms, len(stages))}
	for i, stage := range stages {
		histogram := spec.StageHistograms{StageIndex: stage.Window.Index, E2ECounts: counts[i].e2e}
		if counts[i].hasTTFT {
			histogram.TtftCounts = counts[i].ttft
		}
		if counts[i].hasITL {
			histogram.ItlCounts = counts[i].itl
		}
		document.Stages[i] = histogram
	}
	return document
}
