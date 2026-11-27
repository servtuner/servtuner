package analyze

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"sort"
	"strconv"
	"time"

	"github.com/servtuner/servtuner/bench/internal/analyze/input"
	"github.com/servtuner/servtuner/bench/internal/bundle"
	"github.com/servtuner/servtuner/bench/internal/storage"
)

// bundleMatrix reads prometheus/<catalog>/<name>.json: the `data` object of a
// range-query response, {"resultType":"matrix","result":[{"metric":{...},
// "values":[[ts,"v"],...]}]}, one file per catalog entry.
type bundleMatrix struct {
	ctx   context.Context
	store storage.BundleStore
}

// NewBundleMatrix returns the MatrixSource of a bundle.
func NewBundleMatrix(ctx context.Context, store storage.BundleStore) input.MatrixSource {
	return bundleMatrix{ctx: ctx, store: store}
}

type rangeData struct {
	ResultType string `json:"resultType"`
	Result     []struct {
		Metric map[string]string    `json:"metric"`
		Values [][2]json.RawMessage `json:"values"`
	} `json:"result"`
}

// Matrix implements input.MatrixSource. A file that is not there is not
// collected; NaN and infinite samples are dropped, as Prometheus reports gaps
// and stale values that way.
func (b bundleMatrix) Matrix(ref input.SeriesRef) ([]input.Series, bool, error) {
	name := bundle.PrometheusSeriesFile(string(ref.Catalog), ref.Name)
	content, err := readAll(b.ctx, b.store, name)
	if errors.Is(err, storage.ErrNotFound) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	var data rangeData
	if err := json.Unmarshal(content, &data); err != nil {
		return nil, false, fmt.Errorf("decode %s: %w", name, err)
	}
	if data.ResultType != "matrix" {
		return nil, false, fmt.Errorf("decode %s: want a matrix result, got %q", name, data.ResultType)
	}
	series := make([]input.Series, 0, len(data.Result))
	for _, result := range data.Result {
		one := input.Series{Labels: result.Metric}
		for _, pair := range result.Values {
			var seconds float64
			var text string
			if err := json.Unmarshal(pair[0], &seconds); err != nil {
				return nil, false, fmt.Errorf("decode %s: timestamp %s: %w", name, pair[0], err)
			}
			if err := json.Unmarshal(pair[1], &text); err != nil {
				return nil, false, fmt.Errorf("decode %s: value %s: %w", name, pair[1], err)
			}
			value, err := strconv.ParseFloat(text, 64)
			if err != nil {
				return nil, false, fmt.Errorf("decode %s: value %q: %w", name, text, err)
			}
			if math.IsNaN(value) || math.IsInf(value, 0) {
				continue
			}
			one.Samples = append(one.Samples, input.Sample{At: time.UnixMilli(int64(math.Round(seconds * 1000))).UTC(), Value: value})
		}
		sort.Slice(one.Samples, func(i, j int) bool { return one.Samples[i].At.Before(one.Samples[j].At) })
		series = append(series, one)
	}
	return series, len(series) > 0, nil
}
