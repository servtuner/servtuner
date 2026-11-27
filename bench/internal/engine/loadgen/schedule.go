package loadgen

import (
	"iter"
	"time"

	"github.com/servtuner/servtuner/bench/internal/engine/loadgen/pacer"
)

// scheduleOffsets yields the due time of every hit of a stage, as an offset
// from the stage start, in order, until the pacer stops.
//
// The pacers answer "how long until the next hit" for a given elapsed time and
// hit count. Asked with the real elapsed time, a pacer that is behind schedule
// answers zero, and the lateness would vanish from scheduled_at. So the pacer
// is asked in virtual time: elapsed is the previous hit's own due time, which
// makes the schedule a pure function of the pacer that does not depend on how
// fast this process runs. The sender then sleeps until each due time, or, if it
// is already past, sends at once and the delay shows as send lag.
func scheduleOffsets(stagePacer pacer.StagePacer) iter.Seq[time.Duration] {
	return func(yield func(time.Duration) bool) {
		var offset time.Duration
		for hits := uint64(0); ; hits++ {
			wait, stop := stagePacer.Pacer.Pace(offset, hits)
			if stop {
				return
			}
			offset += wait
			if !yield(offset) {
				return
			}
		}
	}
}
