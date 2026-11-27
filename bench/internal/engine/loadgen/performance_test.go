package loadgen

import (
	"io"
	"net/http"
	"sort"
	"testing"
	"time"

	"github.com/servtuner/servtuner/bench/internal/stats"
)

// maxP99SendLag is the stage-validity bound on send lag.
const maxP99SendLag = 50 * time.Millisecond

// TestSendLagAtTwoThousandRPS drives an in-process, non-streaming mock with a
// tiny body at 2000 requests per second and checks that the sender keeps its
// schedule: p99 send lag under 50ms, and the stage's hits all sent. Under
// -short it runs one second instead of five. The client and the mock share this
// process and its CPUs, so the result is a floor for the ceiling on a loaded
// CI machine, not a measurement of the engine alone.
func TestSendLagAtTwoThousandRPS(t *testing.T) {
	const rate = 2000
	duration := 5 * time.Second
	if testing.Short() {
		duration = time.Second
	}
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		_, _ = io.WriteString(w, `{"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}}`)
	})
	runSpec := specFor(platformServer(t, handler), plainTLS, false, rate, duration)
	e := prepare(t, Options{}, runSpec)
	sink := &collectingSink{}
	if _, err := runAll(t, e, runSpec, sink); err != nil {
		t.Fatalf("Run: %v", err)
	}
	rows := sink.snapshot()

	offered := e.OfferedHits()[0]
	if achieved := float64(len(rows)); achieved < 0.97*offered {
		t.Errorf("sent %d of %.0f offered hits (%.1f%%)", len(rows), offered, 100*achieved/offered)
	}
	lags := make([]float64, 0, len(rows))
	for _, row := range rows {
		if row.Error != nil {
			t.Fatalf("request failed: %s", errorOf(row))
		}
		lags = append(lags, *row.SendLagSeconds)
	}
	sort.Float64s(lags)
	p99 := time.Duration(stats.NearestRank(lags, 0.99) * float64(time.Second))
	maximum := time.Duration(lags[len(lags)-1] * float64(time.Second))
	t.Logf("%d rps for %s: %d requests, send lag p99 %v max %v, cap waits %d", rate, duration, len(rows), p99, maximum, e.Stats().CapWaits)
	if p99 > maxP99SendLag {
		t.Errorf("p99 send lag %v exceeds %v", p99, maxP99SendLag)
	}
}
