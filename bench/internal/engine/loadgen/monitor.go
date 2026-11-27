package loadgen

import (
	"context"
	"runtime"
	"sort"
	"sync"
	"sync/atomic"
	"time"

	"github.com/servtuner/servtuner/bench/internal/engine"
	"github.com/servtuner/servtuner/bench/internal/stats"
)

// SampleSource is recorded in every client sample this engine writes.
const SampleSource = "loadgen"

// DefaultSampleInterval is how often the engine samples itself.
const DefaultSampleInterval = time.Second

// noStage is the current stage while no stage is running (warmup).
const noStage = -1

// lagTracker collects the send lag of every request that was sent in the
// current sampling interval, and knows which requests are still waiting for a
// slot. Without the waiting ones, a client stuck at its in-flight cap would send
// nothing, record no lag, and look healthy.
type lagTracker struct {
	mutex        sync.Mutex
	sentLags     []float64
	nextWaiterID uint64
	waitingSince map[uint64]time.Time
}

func newLagTracker() *lagTracker {
	return &lagTracker{waitingSince: map[uint64]time.Time{}}
}

func (t *lagTracker) recordSent(lag time.Duration) {
	t.mutex.Lock()
	t.sentLags = append(t.sentLags, lag.Seconds())
	t.mutex.Unlock()
}

// startWaiting registers a request that is due but cannot be sent yet, and
// returns the function that unregisters it.
func (t *lagTracker) startWaiting(scheduledAt time.Time) (stopWaiting func()) {
	t.mutex.Lock()
	id := t.nextWaiterID
	t.nextWaiterID++
	t.waitingSince[id] = scheduledAt
	t.mutex.Unlock()
	return func() {
		t.mutex.Lock()
		delete(t.waitingSince, id)
		t.mutex.Unlock()
	}
}

// takeInterval returns the p99 and the maximum send lag over the interval that
// ends now and starts a new one. Requests still waiting count with the lag they
// have accumulated so far.
func (t *lagTracker) takeInterval(now time.Time) (p99Seconds, maxSeconds float64) {
	t.mutex.Lock()
	lags := t.sentLags
	t.sentLags = nil
	for _, scheduledAt := range t.waitingSince {
		lags = append(lags, now.Sub(scheduledAt).Seconds())
	}
	t.mutex.Unlock()
	if len(lags) == 0 {
		return 0, 0
	}
	sort.Float64s(lags)
	return stats.NearestRank(lags, 0.99), lags[len(lags)-1]
}

// monitor writes one engine.ClientSampleRow per interval.
type monitor struct {
	sink         engine.ClientSink
	interval     time.Duration
	now          func() time.Time
	lags         *lagTracker
	inFlight     *atomic.Int64
	currentStage *atomic.Int64
}

// run samples until ctx ends, then writes one last sample for the final partial
// interval. It returns the first write error, if any.
func (m monitor) run(ctx context.Context) error {
	ticker := time.NewTicker(m.interval)
	defer ticker.Stop()
	var firstErr error
	write := func() {
		if err := m.sink.WriteSample(m.sample()); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	for {
		select {
		case <-ticker.C:
			write()
		case <-ctx.Done():
			write()
			return firstErr
		}
	}
}

func (m monitor) sample() engine.ClientSampleRow {
	now := m.now()
	p99, maximum := m.lags.takeInterval(now)
	sample := engine.ClientSampleRow{
		SampledAt:         now.UTC(),
		Source:            SampleSource,
		Goroutines:        int64(runtime.NumGoroutine()),
		InFlightRequests:  m.inFlight.Load(),
		SendLagP99Seconds: p99,
		SendLagMaxSeconds: maximum,
	}
	if stage := m.currentStage.Load(); stage != noStage {
		sample.StageIndex = &stage
	}
	return sample
}
