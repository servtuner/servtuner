package loadgen

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"github.com/servtuner/servtuner/bench/internal/engine"
	"github.com/servtuner/servtuner/bench/internal/engine/loadgen/pacer"
	"github.com/servtuner/servtuner/bench/internal/spec"
)

// Stats are the counts the harness may want to report next to the records.
// Warmup requests are also counted here; their rows are written with phase
// warmup.
type Stats struct {
	// WarmupRequests and WarmupErrors count requests of the warmup phase.
	WarmupRequests, WarmupErrors int64
	// CapWaits counts requests that found the in-flight cap full and had to
	// wait for a slot. They were still sent, late; the lag is in their rows.
	CapWaits int64
	// Unsent counts due requests discarded because the run was cancelled or
	// failed before they could be sent.
	Unsent int64
	// Shed counts due requests not sent because MaxWaiting requests were
	// already waiting for a slot. Shedding means the client is the bottleneck
	// (doc.go): the stage will fail its achieved-versus-offered check.
	Shed int64
	// RowQueueBlocks counts requests that found the row queue full and waited
	// to queue their row, which means the sink is slower than the requests.
	RowQueueBlocks int64
	// Lost counts rows that finished but were never handed to the sink: the
	// sink did not drain in time, or failed.
	Lost int64
	// Abandoned counts requests cut off when the drain time ran out. They were
	// sent, so each has a row with error class "abandoned".
	Abandoned int64
}

type counters struct {
	warmupRequests, warmupErrors, capWaits, unsent, shed, rowQueueBlocks, lost, abandoned atomic.Int64
}

// Stats returns the counts so far.
func (e *Engine) Stats() Stats {
	return Stats{
		WarmupRequests: e.counters.warmupRequests.Load(),
		WarmupErrors:   e.counters.warmupErrors.Load(),
		CapWaits:       e.counters.capWaits.Load(),
		Unsent:         e.counters.unsent.Load(),
		Shed:           e.counters.shed.Load(),
		RowQueueBlocks: e.counters.rowQueueBlocks.Load(),
		Lost:           e.counters.lost.Load(),
		Abandoned:      e.counters.abandoned.Load(),
	}
}

// runState is everything that lives for one Run.
type runState struct {
	engine *Engine
	sink   engine.Sink

	// slots is the hard in-flight cap: a request holds one from just before it
	// is sent until its response is fully read.
	slots chan struct{}
	// waiting counts requests parked for a slot, bounded by limits.maxWaiting.
	waiting  atomic.Int64
	lags     *lagTracker
	inFlight atomic.Int64
	// currentStage is the stage index being paced, or noStage.
	currentStage atomic.Int64
	pending      sync.WaitGroup

	// pacingCtx ends when the run is cancelled or fails; no new request is
	// sent after it ends. abortCtx ends when the drain time runs out and cuts
	// off requests still in flight.
	pacingCtx  context.Context
	stopPacing context.CancelFunc
	abortCtx   context.Context
	abort      context.CancelFunc

	// rows carries finished request rows to the one goroutine that writes the
	// sink, so a slow sink never holds an in-flight slot.
	rows       chan engine.RequestRow
	writerDone chan struct{}

	failureMutex sync.Mutex
	failure      error
}

// rowQueueSize bounds the rows waiting for the sink. A full queue blocks the
// finishing request (not its slot) and is counted.
const rowQueueSize = 4096

// writerFlushGrace is how long the sink gets, after the requests have ended, to
// take the rows still queued.
const writerFlushGrace = 250 * time.Millisecond

// Run paces every phase in order and returns when the last request has
// finished, or after the drain time. Cancelling ctx stops pacing at once and
// waits the drain time for requests already sent; rows written stay written.
func (e *Engine) Run(ctx context.Context, phases []engine.Phase, sink engine.Sink) (engine.Result, error) {
	if e.transport == nil {
		return engine.Result{}, errors.New("run loadgen: Prepare was not called")
	}
	run, stop := newRunState(ctx, e, sink)
	defer stop()
	defer e.transport.CloseIdleConnections()

	go run.writeRows()
	monitorDone := run.startMonitor()
	result, pacingErr := run.runPhases(ctx, phases)
	deadline := run.drain()
	writerErr := run.finishWriter(deadline)
	monitorErr := monitorDone()

	return result, errors.Join(run.failureError(), pacingErr, writerErr, monitorErr)
}

func newRunState(ctx context.Context, e *Engine, sink engine.Sink) (run *runState, stop func()) {
	run = &runState{
		engine: e, sink: sink,
		slots:      make(chan struct{}, e.limits.maxInFlight),
		lags:       newLagTracker(),
		rows:       make(chan engine.RequestRow, rowQueueSize),
		writerDone: make(chan struct{}),
	}
	run.currentStage.Store(noStage)
	run.pacingCtx, run.stopPacing = context.WithCancel(ctx)
	run.abortCtx, run.abort = context.WithCancel(context.Background())
	return run, func() {
		run.stopPacing()
		run.abort()
	}
}

func (r *runState) startMonitor() (stop func() error) {
	if r.engine.options.ClientSink == nil {
		return func() error { return nil }
	}
	interval := r.engine.options.SampleInterval
	if interval <= 0 {
		interval = DefaultSampleInterval
	}
	monitorCtx, stopMonitor := context.WithCancel(context.Background())
	finished := make(chan error, 1)
	sampler := monitor{
		sink:         r.engine.options.ClientSink,
		interval:     interval,
		now:          r.engine.now,
		lags:         r.lags,
		inFlight:     &r.inFlight,
		currentStage: &r.currentStage,
	}
	go func() { finished <- sampler.run(monitorCtx) }()
	return func() error {
		stopMonitor()
		return <-finished
	}
}

func (r *runState) runPhases(ctx context.Context, phases []engine.Phase) (engine.Result, error) {
	var result engine.Result
	start := r.engine.now()
	for _, phase := range phases {
		window, end, err := r.runPhase(phase, start)
		result.Windows = append(result.Windows, window)
		if err != nil {
			if ctx.Err() != nil {
				err = fmt.Errorf("run cancelled during %s: %w", phase.Name, ctx.Err())
			}
			return result, err
		}
		// The next phase starts exactly where this one ended, so the windows
		// tile the run and no scheduled time falls between them.
		start = end
	}
	return result, nil
}

// runPhase paces one phase from start. It returns the window for the record
// (UTC) and, separately, the end instant for scheduling the next phase: that
// one keeps its monotonic reading, which .UTC() would strip, so scheduling and
// send lag stay immune to clock steps in every phase (doc.go). On an early stop
// the window ends now, so a cut-short phase is still recorded.
func (r *runState) runPhase(phase engine.Phase, start time.Time) (window engine.Window, end time.Time, err error) {
	stagePacer, err := r.engine.pacerFor(phase)
	if err != nil {
		return engine.Window{Phase: phase, Start: start.UTC(), End: start.UTC()}, start, err
	}
	if phase.StageIndex != nil {
		r.currentStage.Store(int64(*phase.StageIndex))
	} else {
		r.currentStage.Store(noStage)
	}
	cutShort := func(cause error) (engine.Window, time.Time, error) {
		stoppedAt := r.engine.now()
		return engine.Window{Phase: phase, Start: start.UTC(), End: stoppedAt.UTC()}, stoppedAt, cause
	}
	for offset := range scheduleOffsets(stagePacer) {
		scheduledAt := start.Add(offset)
		if err := sleepUntil(r.pacingCtx, r.engine.now, scheduledAt); err != nil {
			return cutShort(err)
		}
		r.pending.Add(1)
		go r.execute(phase, scheduledAt)
	}
	end = start.Add(phase.Duration)
	if err := sleepUntil(r.pacingCtx, r.engine.now, end); err != nil {
		return cutShort(err)
	}
	return engine.Window{Phase: phase, Start: start.UTC(), End: end.UTC()}, end, nil
}

// pacerFor returns the pacer of a phase. A stage uses its own pacer. Warmup has
// no rate of its own in the plan (engine.PlanPhases), so it offers the first
// stage's rate at constant pacing: the target is warmed at the load the run
// opens with.
func (e *Engine) pacerFor(phase engine.Phase) (pacer.StagePacer, error) {
	if phase.StageIndex != nil {
		index := *phase.StageIndex
		if index < 0 || index >= len(e.stagePacers) {
			return pacer.StagePacer{}, fmt.Errorf("phase %s: stage index %d out of range (have %d stages)", phase.Name, index, len(e.stagePacers))
		}
		return e.stagePacers[index], nil
	}
	warmupLoad := spec.Load{
		Pacing: spec.PacingConstant,
		Stages: []spec.Stage{{Rate: e.runSpec.Load.Stages[0].Rate, Duration: spec.Duration(phase.Duration.String())}},
	}
	stagePacers, err := pacer.ForLoad(warmupLoad, e.runSpec.Workload.Seed)
	if err != nil {
		return pacer.StagePacer{}, fmt.Errorf("phase %s: %w", phase.Name, err)
	}
	return stagePacers[0], nil
}

// sleepUntil waits for the clock to reach target and reports why it stopped
// early. Already past target it returns at once, which is how a late sender
// catches up; it still notices cancellation.
func sleepUntil(ctx context.Context, now func() time.Time, target time.Time) error {
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		remaining := target.Sub(now())
		if remaining <= 0 {
			return nil
		}
		timer := time.NewTimer(remaining)
		select {
		case <-timer.C:
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		}
	}
}

// drain waits for requests in flight, and cuts them off when the drain time
// runs out. It returns the instant the drain time ends, which also bounds the
// sink (finishWriter).
func (r *runState) drain() time.Time {
	finished := make(chan struct{})
	go func() {
		r.pending.Wait()
		close(finished)
	}()
	deadline := r.engine.now().Add(r.engine.limits.drainTimeout)
	timer := time.NewTimer(r.engine.limits.drainTimeout)
	defer timer.Stop()
	select {
	case <-finished:
	case <-timer.C:
		r.abort()
		<-finished
	}
	return deadline
}

// writeRows is the one goroutine that writes the sink. After a failure it keeps
// taking rows off the queue, so senders never block on a dead sink, and counts
// them lost.
func (r *runState) writeRows() {
	defer close(r.writerDone)
	for row := range r.rows {
		if r.failureError() != nil {
			r.engine.counters.lost.Add(1)
			continue
		}
		if err := r.sink.Write(row); err != nil {
			r.engine.counters.lost.Add(1)
			r.fail(err)
		}
	}
}

// queueRow hands a finished row to the writer. When the queue is full the
// request waits here, after it released its slot, so a slow sink cannot hold
// back sending. Once the run is aborted the row is lost rather than waited on.
func (r *runState) queueRow(row engine.RequestRow) {
	select {
	case r.rows <- row:
		return
	default:
	}
	r.engine.counters.rowQueueBlocks.Add(1)
	select {
	case r.rows <- row:
	case <-r.abortCtx.Done():
		r.engine.counters.lost.Add(1)
	}
}

// finishWriter closes the queue and waits for the sink to take what is in it,
// until the drain deadline plus a short grace. A sink that is still stuck by
// then is reported, instead of hanging the run; the rows it never took are
// counted lost.
func (r *runState) finishWriter(drainDeadline time.Time) error {
	close(r.rows)
	wait := max(drainDeadline.Sub(r.engine.now()), 0) + writerFlushGrace
	timer := time.NewTimer(wait)
	defer timer.Stop()
	select {
	case <-r.writerDone:
		return nil
	case <-timer.C:
		unwritten := len(r.rows) + 1 // the queue, and the row the sink holds
		r.engine.counters.lost.Add(int64(unwritten))
		return fmt.Errorf("request sink stuck: it did not take its rows within %s of the end of the run; about %d rows lost", wait, unwritten)
	}
}

// fail records the first fatal error (a sink that cannot persist rows) and
// stops pacing.
func (r *runState) fail(err error) {
	r.failureMutex.Lock()
	first := r.failure == nil
	if first {
		r.failure = err
	}
	r.failureMutex.Unlock()
	if first {
		r.stopPacing()
	}
}

func (r *runState) failureError() error {
	r.failureMutex.Lock()
	defer r.failureMutex.Unlock()
	return r.failure
}
