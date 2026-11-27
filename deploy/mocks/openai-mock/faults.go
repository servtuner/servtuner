package main

import (
	"context"
	"log"
	"net/http"
	"runtime"
	"sync"
	"sync/atomic"
	"time"
)

// Faults admits each inference request before it is served.
// handled is true when Admit has already answered the request (an
// injected error, a 429, or a client that gave up while waiting); otherwise
// the caller serves it and calls release when done.
type Faults interface {
	Admit(w http.ResponseWriter, r *http.Request) (release func(), handled bool)
}

// faults implements the knobs of FaultConfig. The zero FaultConfig admits
// every request at once and changes nothing.
type faults struct {
	cfg     FaultConfig
	metrics *metrics
	// slots holds one token per request being served; nil without a cap.
	slots    chan struct{}
	inFlight atomic.Int64
	// arrivals counts requests for the evenly spread error injection.
	arrivals atomic.Uint64
}

func newFaults(cfg FaultConfig, m *metrics) *faults {
	f := &faults{cfg: cfg, metrics: m}
	if cfg.MaxConcurrency > 0 {
		f.slots = make(chan struct{}, cfg.MaxConcurrency)
	}
	return f
}

func (f *faults) Admit(w http.ResponseWriter, r *http.Request) (func(), bool) {
	if f.injectError() {
		f.metrics.faults.WithLabelValues("error").Inc()
		writeError(w, f.cfg.ErrorStatus, "injected error (--error-rate)")
		return nil, true
	}
	if !f.takeSlot(w, r.Context()) {
		return nil, true
	}
	others := f.inFlight.Add(1) - 1
	held := touched(f.cfg.MemoryPerRequestBytes)
	release := func() {
		runtime.KeepAlive(held) // resident for as long as the request is served
		f.inFlight.Add(-1)
		if f.slots != nil {
			<-f.slots
		}
	}
	delay := f.cfg.ExtraLatency + time.Duration(others)*f.cfg.LatencyPerInFlight
	if err := sleep(r.Context(), delay); err != nil {
		release()
		return nil, true // the client gave up; nobody is left to answer
	}
	return release, false
}

// injectError says whether this request is one of the ErrorRate share. Request
// n fails when the running total n x rate crosses an integer, so failures are
// spread evenly and a run is reproducible.
func (f *faults) injectError() bool {
	if f.cfg.ErrorRate <= 0 {
		return false
	}
	n := float64(f.arrivals.Add(1))
	return int(n*f.cfg.ErrorRate) > int((n-1)*f.cfg.ErrorRate)
}

// takeSlot waits for (queue) or refuses (reject) a request beyond the cap. It
// returns false when the request has been answered or abandoned.
func (f *faults) takeSlot(w http.ResponseWriter, ctx context.Context) bool {
	if f.slots == nil {
		return true
	}
	select {
	case f.slots <- struct{}{}:
		return true
	default:
	}
	if f.cfg.OverCapacity == overCapacityReject {
		f.metrics.faults.WithLabelValues("rejected").Inc()
		writeError(w, http.StatusTooManyRequests, "over capacity (--max-concurrency)")
		return false
	}
	f.metrics.queued.Inc()
	defer f.metrics.queued.Dec()
	select {
	case f.slots <- struct{}{}:
		return true
	case <-ctx.Done():
		return false
	}
}

// pageSize is the stride touched writes at, so every page becomes resident.
const pageSize = 4096

// touched allocates size bytes and writes one byte per page: untouched pages
// would not count towards the container's working set.
func touched(size int) []byte {
	if size <= 0 {
		return nil
	}
	buffer := make([]byte, size)
	for i := 0; i < size; i += pageSize {
		buffer[i] = 1
	}
	return buffer
}

// memoryRamp retains MemoryRampBytesPerSecond more on every tick until
// MemoryRampMaxBytes, then holds it: a leak with a known slope.
type memoryRamp struct {
	mu       sync.Mutex
	retained [][]byte
	total    int
}

// startMemoryRamp grows the ramp on each tick until it is full or ctx ends.
func startMemoryRamp(ctx context.Context, cfg FaultConfig, m *metrics, tick <-chan time.Time) *memoryRamp {
	ramp := &memoryRamp{}
	if cfg.MemoryRampBytesPerSecond <= 0 {
		return ramp
	}
	log.Printf("openai-mock memory ramp: %d bytes/s up to %d bytes", cfg.MemoryRampBytesPerSecond, cfg.MemoryRampMaxBytes)
	go func() {
		for {
			select {
			case <-ctx.Done():
				return
			case <-tick:
				growing := ramp.grow(cfg)
				m.rampBytes.Set(float64(ramp.size()))
				if !growing {
					return
				}
			}
		}
	}()
	return ramp
}

// grow adds one tick's worth; false once the ramp has reached its maximum.
func (r *memoryRamp) grow(cfg FaultConfig) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	step := min(cfg.MemoryRampBytesPerSecond, cfg.MemoryRampMaxBytes-r.total)
	if step > 0 {
		r.retained = append(r.retained, touched(step))
		r.total += step
	}
	return r.total < cfg.MemoryRampMaxBytes
}

func (r *memoryRamp) size() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.total
}
