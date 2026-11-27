package pacer

import (
	"fmt"
	"math/rand/v2"
	"sync"
	"time"

	vegeta "github.com/tsenart/vegeta/v12/lib"
)

// poisson schedules arrivals with exponentially distributed gaps whose mean is
// 1/rate, which is a Poisson process. Arrival times are fixed in advance and
// do not depend on when Pace is called, so a sender that falls behind catches
// up on the same schedule, as the other pacers do (open loop).
type poisson struct {
	requestsPerSecond float64

	// mu guards the schedule: vegeta calls Pace from one goroutine, but a pacer
	// that corrupts its schedule when shared is a trap.
	mu          sync.Mutex
	random      *rand.Rand
	nextHitIdx  uint64
	nextHitTime time.Duration
}

var _ vegeta.Pacer = (*poisson)(nil)

// Poisson returns a pacer with exponential inter-arrival gaps at the given mean
// rate. The schedule is a pure function of seed and stageIndex, so a run can be
// repeated exactly and no two stages share a random stream.
func Poisson(requestsPerSecond float64, seed uint64, stageIndex int) (vegeta.Pacer, error) {
	if err := validateRate(requestsPerSecond); err != nil {
		return nil, fmt.Errorf("poisson pacer: %w", err)
	}
	p := &poisson{
		requestsPerSecond: requestsPerSecond,
		random:            rand.New(rand.NewPCG(seed, uint64(stageIndex))),
	}
	p.nextHitTime = p.drawGap()
	return p, nil
}

func (p *poisson) drawGap() time.Duration {
	return time.Duration(p.random.ExpFloat64() / p.requestsPerSecond * float64(time.Second))
}

// Pace returns the time left until arrival number hits (counting from zero).
func (p *poisson) Pace(elapsed time.Duration, hits uint64) (time.Duration, bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	for p.nextHitIdx < hits {
		p.nextHitTime += p.drawGap()
		p.nextHitIdx++
	}
	return max(p.nextHitTime-elapsed, 0), false
}

// Rate is the mean rate, which is all a Poisson process has.
func (p *poisson) Rate(time.Duration) float64 {
	return p.requestsPerSecond
}
