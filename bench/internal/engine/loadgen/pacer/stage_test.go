package pacer

import (
	"fmt"
	"math"
	"testing"
	"time"

	vegeta "github.com/tsenart/vegeta/v12/lib"

	"github.com/servtuner/servtuner/bench/internal/spec"
)

// countHits replays a stage on a simulated clock: the sender is never late, so
// each hit happens exactly when the pacer says it is due. No wall-clock time
// passes, which keeps the counts exact and the tests fast.
func countHits(t *testing.T, pacer vegeta.Pacer, stageDuration time.Duration) uint64 {
	t.Helper()
	var elapsed time.Duration
	var hits uint64
	for {
		wait, stop := pacer.Pace(elapsed, hits)
		if wait < 0 {
			t.Fatalf("negative wait %s at %s after %d hits", wait, elapsed, hits)
		}
		if stop {
			return hits
		}
		elapsed += wait
		if elapsed >= stageDuration {
			return hits
		}
		hits++
	}
}

// within asserts the hit count is the expected area under the rate curve, give
// or take one hit: a hit that lands exactly on a boundary is on either side.
// Constant and linear pacing are asserted exactly instead, where the pacer is
// deterministic enough to pin the count; sine and the ForLoad check use this.
func within(t *testing.T, name string, got uint64, want float64) {
	t.Helper()
	if math.Abs(float64(got)-want) > 1 {
		t.Errorf("%s: %d hits, want %.2f +/- 1", name, got, want)
	}
}

// TestConstantHitCount: a hit is due every 1/rate seconds, the first one a
// full interval after the stage starts. The hit due exactly at the end of the
// stage belongs to the next stage, so a stage of D seconds sends rate*D - 1.
func TestConstantHitCount(t *testing.T) {
	for _, tc := range []struct {
		rate     float64
		duration time.Duration
		want     uint64
	}{{10, 60 * time.Second, 599}, {250, 30 * time.Second, 7499}, {0.5, 120 * time.Second, 59}} {
		pacer, err := Constant(tc.rate)
		if err != nil {
			t.Fatal(err)
		}
		if got := countHits(t, pacer, tc.duration); got != tc.want {
			t.Errorf("rate %v for %s: %d hits, want %d", tc.rate, tc.duration, got, tc.want)
		}
	}
}

// TestLinearHitCount: the hits over a ramp are the area of the trapezoid under
// the rate line, (start+end)/2*duration, less the hit due on the end boundary.
// vegeta's LinearPacer also sends its first hit at once instead of one
// interval in, which adds one back when the ramp is steep enough to need it
// (the down ramp), so the counts are pinned per case.
func TestLinearHitCount(t *testing.T) {
	for _, tc := range []struct {
		name       string
		start, end float64
		duration   time.Duration
		want       uint64
	}{
		{"up", 10, 50, 60 * time.Second, 1799},   // area 1800
		{"down", 80, 20, 60 * time.Second, 3000}, // area 3000
		{"flat", 25, 25, 40 * time.Second, 999},  // area 1000
	} {
		pacer, err := Linear(tc.start, tc.end, tc.duration)
		if err != nil {
			t.Fatal(err)
		}
		if got := countHits(t, pacer, tc.duration); got != tc.want {
			t.Errorf("%s: %d hits, want %d", tc.name, got, tc.want)
		}
		if got := pacer.Rate(0); math.Abs(got-tc.start) > 1e-6 {
			t.Errorf("%s: Rate(0) = %v, want %v", tc.name, got, tc.start)
		}
		if got := pacer.Rate(tc.duration); math.Abs(got-tc.end) > 1e-6 {
			t.Errorf("%s: Rate(duration) = %v, want %v", tc.name, got, tc.end)
		}
	}
}

// TestSineHitCount: over whole periods the sine integrates to the mean rate,
// whatever the amplitude. The ratio of exactly 1 is the schema's upper limit.
func TestSineHitCount(t *testing.T) {
	for _, amplitudeRatio := range []float64{0.5, 1} {
		period := 20 * time.Second
		stageDuration := 3 * period
		pacer, err := Sine(40, amplitudeRatio, period)
		if err != nil {
			t.Fatal(err)
		}
		within(t, "sine", countHits(t, pacer, stageDuration), 40*stageDuration.Seconds())
		peak := pacer.Rate(period / 4)
		if want := 40 * (1 + amplitudeRatio); math.Abs(peak-want) > 1e-3 {
			t.Errorf("ratio %v: peak rate %v, want %v", amplitudeRatio, peak, want)
		}
	}
}

// TestPoissonHitCountIsWithinStatisticalBound: the count over a stage is
// Poisson with mean rate*duration, so its standard deviation is the square
// root of that. With a fixed seed the outcome is deterministic; the bound is
// four standard deviations, which a correct generator misses once in ~16000
// seeds, so a failure here means the generator changed, not bad luck.
func TestPoissonHitCountIsWithinStatisticalBound(t *testing.T) {
	const rate, seconds = 50.0, 120.0
	expected := rate * seconds
	bound := 4 * math.Sqrt(expected)
	for stageIndex := range 3 {
		pacer, err := Poisson(rate, 42, stageIndex)
		if err != nil {
			t.Fatal(err)
		}
		got := float64(countHits(t, pacer, seconds*time.Second))
		if math.Abs(got-expected) > bound {
			t.Errorf("stage %d: %v hits, want %v +/- %.0f", stageIndex, got, expected, bound)
		}
	}
}

// TestPoissonGapsAreExponential checks shape, not just count: for exponential
// gaps the standard deviation equals the mean, unlike constant spacing.
func TestPoissonGapsAreExponential(t *testing.T) {
	pacer, err := Poisson(100, 7, 0)
	if err != nil {
		t.Fatal(err)
	}
	const samples = 20000
	gapsSeconds := make([]float64, 0, samples)
	var previousArrival time.Duration
	for hits := range uint64(samples) {
		wait, _ := pacer.Pace(0, hits)
		gapsSeconds = append(gapsSeconds, (wait - previousArrival).Seconds())
		previousArrival = wait
	}
	var sum, sumOfSquares float64
	for _, gap := range gapsSeconds {
		sum += gap
		sumOfSquares += gap * gap
	}
	mean := sum / samples
	standardDeviation := math.Sqrt(sumOfSquares/samples - mean*mean)
	if math.Abs(mean-0.01) > 0.0005 {
		t.Errorf("mean gap %v s, want 0.01 s", mean)
	}
	if math.Abs(standardDeviation/mean-1) > 0.05 {
		t.Errorf("gap standard deviation / mean = %v, want about 1", standardDeviation/mean)
	}
}

func TestPoissonIsSeededAndPerStage(t *testing.T) {
	firstArrivals := func(seed uint64, stageIndex int) [5]time.Duration {
		pacer, err := Poisson(10, seed, stageIndex)
		if err != nil {
			t.Fatal(err)
		}
		var arrivals [5]time.Duration
		for hits := range arrivals {
			arrivals[hits], _ = pacer.Pace(0, uint64(hits))
		}
		return arrivals
	}
	baseline := firstArrivals(1, 0)
	if repeated := firstArrivals(1, 0); repeated != baseline {
		t.Error("the same seed and stage gave different schedules")
	}
	if firstArrivals(2, 0) == baseline {
		t.Error("different seeds gave the same schedule")
	}
	if firstArrivals(1, 1) == baseline {
		t.Error("different stages share a schedule")
	}
}

// TestPoissonKeepsScheduleWhenSenderIsLate: arrival times do not depend on when
// Pace is called, and a late sender is told to send at once.
func TestPoissonKeepsScheduleWhenSenderIsLate(t *testing.T) {
	pacer, err := Poisson(10, 3, 0)
	if err != nil {
		t.Fatal(err)
	}
	onTime, _ := pacer.Pace(0, 4)
	late, _ := pacer.Pace(onTime-time.Millisecond, 4)
	if late != time.Millisecond {
		t.Errorf("wait %v, want 1ms: the arrival moved", late)
	}
	if wait, _ := pacer.Pace(onTime+time.Second, 4); wait != 0 {
		t.Errorf("late sender told to wait %v, want 0", wait)
	}
}

func TestBadParametersAreRejected(t *testing.T) {
	if _, err := Poisson(0, 1, 0); err == nil {
		t.Error("Poisson accepted a zero rate")
	}
	if _, err := Linear(0, 10, time.Second); err == nil {
		t.Error("Linear accepted a zero start rate")
	}
	if _, err := Linear(10, 10, 0); err == nil {
		t.Error("Linear accepted a zero duration")
	}
	if _, err := Sine(10, 0, time.Second); err == nil {
		t.Error("Sine accepted a zero amplitude")
	}
	if _, err := Sine(10, 1.5, time.Second); err == nil {
		t.Error("Sine accepted an amplitude ratio above 1")
	}
	if _, err := Sine(10, 0.5, 0); err == nil {
		t.Error("Sine accepted a zero period")
	}
}

func loadFrom(t *testing.T, pacing string) spec.Load {
	t.Helper()
	document := `{"pacing": "` + pacing + `", "sine": {"period": "10s", "amplitude_ratio": 0.5},
		"stages": [{"rate": 10, "duration": "20s"}, {"rate": 30, "duration": "20s"}]}`
	var load spec.Load
	if err := load.UnmarshalJSON([]byte(document)); err != nil {
		t.Fatal(err)
	}
	return load
}

// TestForLoadBuildsOnePacerPerStage covers each pacing, and the linear rule
// that stage 0 holds its rate while later stages ramp from the previous one.
func TestForLoadBuildsOnePacerPerStage(t *testing.T) {
	for _, pacing := range []string{"constant", "poisson", "linear", "sine"} {
		stagePacers, err := ForLoad(loadFrom(t, pacing), 99)
		if err != nil {
			t.Fatalf("%s: %v", pacing, err)
		}
		if len(stagePacers) != 2 || stagePacers[1].Duration != 20*time.Second || stagePacers[1].StageIndex != 1 {
			t.Fatalf("%s: unexpected stage pacers %+v", pacing, stagePacers)
		}
	}
	stagePacers, err := ForLoad(loadFrom(t, "linear"), 99)
	if err != nil {
		t.Fatal(err)
	}
	if got := stagePacers[0].Pacer.Rate(15 * time.Second); got != 10 {
		t.Errorf("stage 0 rate %v, want it held at 10", got)
	}
	if got := stagePacers[1].Pacer.Rate(0); math.Abs(got-10) > 1e-6 {
		t.Errorf("stage 1 starts at %v, want the previous stage's rate 10", got)
	}
	within(t, "linear stage 1", countHits(t, stagePacers[1].Pacer, 20*time.Second), 20*20)
}

func TestForLoadRejectsSineWithoutParameters(t *testing.T) {
	load := loadFrom(t, "sine")
	load.Sine = nil
	if _, err := ForLoad(load, 1); err == nil {
		t.Error("ForLoad accepted sine pacing without load.sine")
	}
}

// sineIntegral is the hits a sine stage offers, from the closed form of the
// rate's integral, written out here independently of the pacer.
func sineIntegral(mean, ratio float64, period, stage time.Duration) float64 {
	omega := 2 * math.Pi / period.Seconds()
	return mean*stage.Seconds() + ratio*mean/omega*(1-math.Cos(omega*stage.Seconds()))
}

// TestSineHitCountAtHighAmplitude: vegeta's SinePacer ran away at these
// settings (ratio near 1 drives the rate to about zero at the trough).
func TestSineHitCountAtHighAmplitude(t *testing.T) {
	period := 2 * time.Second
	for _, mean := range []float64{20, 100, 1000} {
		for _, ratio := range []float64{0.97, 0.99, 1} {
			for _, stageDuration := range []time.Duration{20 * time.Second, 7300 * time.Millisecond} {
				pacer, err := Sine(mean, ratio, period)
				if err != nil {
					t.Fatal(err)
				}
				got := countHits(t, pacer, stageDuration)
				within(t, fmt.Sprintf("mean %v ratio %v stage %s", mean, ratio, stageDuration), got, sineIntegral(mean, ratio, period, stageDuration))
			}
		}
	}
}

func TestTinyRatesAreRejectedByEveryPacer(t *testing.T) {
	const tiny = 1e-12
	constructors := map[string]func() error{
		"constant":   func() error { _, err := Constant(tiny); return err },
		"poisson":    func() error { _, err := Poisson(tiny, 1, 0); return err },
		"linear up":  func() error { _, err := Linear(tiny, 10, time.Second); return err },
		"linear end": func() error { _, err := Linear(10, tiny, time.Second); return err },
		"sine":       func() error { _, err := Sine(tiny, 0.5, time.Second); return err },
	}
	for name, construct := range constructors {
		if construct() == nil {
			t.Errorf("%s accepted a rate of %v", name, tiny)
		}
	}
}

// TestStagePacerStopsAtDuration: every pacing ends at the stage end, including
// linear, whose own Pace returns nonsense past the ramp.
func TestStagePacerStopsAtDuration(t *testing.T) {
	for _, pacing := range []string{"constant", "poisson", "linear", "sine"} {
		stagePacers, err := ForLoad(loadFrom(t, pacing), 5)
		if err != nil {
			t.Fatal(err)
		}
		stage := stagePacers[1]
		if hits := countHits(t, stage.Pacer, stage.Duration); hits == 0 {
			t.Errorf("%s: no hits in the stage", pacing)
		}
		if _, stop := stage.Pacer.Pace(stage.Duration, 1000); !stop {
			t.Errorf("%s: no stop at the stage end", pacing)
		}
		if _, stop := stage.Pacer.Pace(stage.Duration+time.Hour, 1000); !stop {
			t.Errorf("%s: no stop long after the stage end", pacing)
		}
		if _, stop := stage.Pacer.Pace(stage.Duration-time.Nanosecond, 1_000_000); !stop {
			t.Errorf("%s: a hit due after the stage end was not stopped", pacing)
		}
	}
}

func TestOfferedHits(t *testing.T) {
	// Stage 1 of loadFrom: rate 30, 20s; previous stage rate 10; sine period
	// 10s and ratio 0.5. 20s is two whole periods, so test a partial one too.
	for pacing, want := range map[string]float64{"constant": 600, "poisson": 600, "linear": 400, "sine": 600} {
		stagePacers, err := ForLoad(loadFrom(t, pacing), 1)
		if err != nil {
			t.Fatal(err)
		}
		if got := OfferedHits(stagePacers[1]); math.Abs(got-want) > 1e-6 {
			t.Errorf("%s: OfferedHits = %v, want %v", pacing, got, want)
		}
	}
	pacer, err := Sine(30, 0.5, 10*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	partial := StagePacer{Duration: 13 * time.Second, Pacer: pacer}
	if got, want := OfferedHits(partial), sineIntegral(30, 0.5, 10*time.Second, 13*time.Second); math.Abs(got-want) > 1e-6 {
		t.Errorf("sine over 13s: OfferedHits = %v, want %v (not mean*duration %v)", got, want, 30*13.0)
	}
}
