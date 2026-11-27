// Package aggregate computes the per-stage client statistics from the request records, and the windowed statistics
// of server-side series that the quality gate and the capacity model read.
package aggregate

// Mean is the arithmetic mean, or 0 for no values.
func Mean(values []float64) float64 {
	if len(values) == 0 {
		return 0
	}
	var sum float64
	for _, value := range values {
		sum += value
	}
	return sum / float64(len(values))
}
