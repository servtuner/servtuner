package pacer

import (
	"math"
	"testing"
	"time"
)

func TestConstantRate(t *testing.T) {
	for _, rate := range []float64{1, 10, 500.5, 0.25} {
		pacer, err := Constant(rate)
		if err != nil {
			t.Fatalf("Constant(%v): %v", rate, err)
		}
		if got := pacer.Rate(0); math.Abs(got-rate) > 1e-6 {
			t.Errorf("Rate = %v, want %v", got, rate)
		}
	}
}

// TestConstantSchedulesOpenLoop: with hits on schedule the next one is due one
// interval after the previous; when the sender falls behind the pacer says to
// send at once instead of waiting.
func TestConstantSchedulesOpenLoop(t *testing.T) {
	pacer, err := Constant(10)
	if err != nil {
		t.Fatal(err)
	}
	wait, stop := pacer.Pace(0, 0)
	if stop || wait != 100*time.Millisecond {
		t.Errorf("first hit: wait=%v stop=%v, want 100ms", wait, stop)
	}
	wait, stop = pacer.Pace(2*time.Second, 5)
	if stop || wait != 0 {
		t.Errorf("behind schedule: wait=%v stop=%v, want 0", wait, stop)
	}
}

func TestConstantRejectsBadRates(t *testing.T) {
	for _, rate := range []float64{0, -1, math.NaN(), math.Inf(1)} {
		if _, err := Constant(rate); err == nil {
			t.Errorf("Constant(%v) accepted", rate)
		}
	}
}
