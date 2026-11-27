package charts

import (
	"math"
	"time"

	"github.com/servtuner/servtuner/bench/internal/engine"
	"github.com/servtuner/servtuner/bench/internal/spec"
)

// inFlight counts the client's requests in flight at each whole second from
// the first stage's start, with a difference array (one pass, O(requests +
// seconds)), and sums each stage's arrivals and E2E for Little's law.
type inFlight struct {
	stages     []Stage
	start      time.Time
	difference []int
	arrivals   []int
	e2eSeconds []float64
}

func newInFlight(stages []Stage, start time.Time, duration time.Duration) *inFlight {
	seconds := int(math.Ceil(duration.Seconds())) + 1
	return &inFlight{
		stages: stages, start: start, difference: make([]int, seconds+1),
		arrivals: make([]int, len(stages)), e2eSeconds: make([]float64, len(stages)),
	}
}

// add counts one request: in flight at every whole second in [sent, sent+E2E).
func (f *inFlight) add(row engine.RequestRow, position map[int]int) {
	if !isFinite(row.E2ESeconds) || row.E2ESeconds <= 0 || row.SentAt.Before(f.start) {
		return
	}
	if i, ok := position[int(*row.StageIndex)]; ok {
		f.arrivals[i]++
		f.e2eSeconds[i] += row.E2ESeconds
	}
	sent := row.SentAt.Sub(f.start).Seconds()
	last := len(f.difference) - 1
	from := min(int(math.Ceil(sent)), last)
	to := min(int(math.Ceil(sent+row.E2ESeconds)), last)
	if from < to {
		f.difference[from]++
		f.difference[to]--
	}
}

func (f *inFlight) samples() []float64 {
	counts := make([]float64, len(f.difference)-1)
	running := 0
	for i := range counts {
		running += f.difference[i]
		counts[i] = float64(running)
	}
	return counts
}

func (f *inFlight) document(analyzerVersion string) spec.InFlightChart {
	counts := f.samples()
	width := max(1, int(math.Ceil(float64(len(counts))/MaxBuckets)))
	document := spec.InFlightChart{
		AnalyzerVersion: analyzerVersion, Start: spec.NewUTCTimestamp(f.start), BucketSeconds: width,
		Stages: make([]spec.LittleCheck, len(f.stages)), Points: []spec.InFlightPoint{},
	}
	for from := 0; from < len(counts); from += width {
		bucket := counts[from:min(from+width, len(counts))]
		low, high, total := math.Inf(1), math.Inf(-1), 0.0
		for _, count := range bucket {
			low, high, total = math.Min(low, count), math.Max(high, count), total+count
		}
		document.Points = append(document.Points, spec.InFlightPoint{OffsetSeconds: float64(from), Mean: total / float64(len(bucket)), Min: low, Max: high})
	}
	for i, stage := range f.stages {
		document.Stages[i] = f.little(i, stage, counts)
	}
	return document
}

// little is the stage's Little's-law check. Its mean in flight skips the
// stage's first mean-E2E seconds, while requests of the stage before drain.
func (f *inFlight) little(i int, stage Stage, counts []float64) spec.LittleCheck {
	startOffset, endOffset := stage.Window.Start.Sub(f.start).Seconds(), stage.Window.End.Sub(f.start).Seconds()
	check := spec.LittleCheck{StageIndex: stage.Window.Index, StartOffsetSeconds: startOffset, EndOffsetSeconds: endOffset}
	seconds := stage.Window.Duration().Seconds()
	if seconds <= 0 {
		return check
	}
	check.ArrivalRps = float64(f.arrivals[i]) / seconds
	if f.arrivals[i] == 0 {
		return check
	}
	meanE2E := f.e2eSeconds[i] / float64(f.arrivals[i])
	check.MeanE2ESeconds = &meanE2E
	from, to := int(math.Ceil(startOffset+meanE2E)), min(int(math.Floor(endOffset)), len(counts))
	if from >= to {
		return check
	}
	total := 0.0
	for _, count := range counts[from:to] {
		total += count
	}
	meanInFlight := total / float64(to-from)
	check.MeanInFlight = &meanInFlight
	if expected := check.ArrivalRps * meanE2E; expected > 0 {
		relative := math.Abs(meanInFlight-expected) / expected
		check.LittleError = &relative
	}
	return check
}
