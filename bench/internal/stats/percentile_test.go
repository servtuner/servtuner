package stats

import "testing"

func sequence(count int) []float64 {
	values := make([]float64, count)
	for i := range values {
		values[i] = float64(i + 1)
	}
	return values
}

func TestNearestRankIsAnObservedValue(t *testing.T) {
	values := sequence(100)
	for _, test := range []struct {
		fraction float64
		want     float64
	}{{0.50, 50}, {0.90, 90}, {0.95, 95}, {0.99, 99}, {1, 100}, {0, 1}} {
		if got := NearestRank(values, test.fraction); got != test.want {
			t.Errorf("NearestRank(1..100, %v) = %v, want %v", test.fraction, got, test.want)
		}
	}
	if got := NearestRank(nil, 0.5); got != 0 {
		t.Errorf("NearestRank(nil) = %v, want 0", got)
	}
}

func TestPercentileDoesNotReorderItsInput(t *testing.T) {
	values := []float64{3, 1, 2}
	if got := Percentile(values, 0.5); got != 2 {
		t.Errorf("median = %v, want 2", got)
	}
	if values[0] != 3 || values[1] != 1 || values[2] != 2 {
		t.Errorf("input was reordered: %v", values)
	}
}
