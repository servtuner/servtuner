// Package prometheus is servtuner's Prometheus side: a client for
// range queries and target discovery, the effective scrape interval, the
// versioned PromQL catalogs, optional-catalog probing and the collection of a
// run's server-side series. It writes nothing; the pipeline stores what it
// returns.
//
// Catalog entries ending in _max, _p95, _min or _avg are rolling windows of the
// shortest stage, evaluated at every step across the run; analysis reads each
// stage's value at the step nearest that stage's end.
package prometheus

import (
	"context"
	"fmt"
	"net/http"
	"time"

	"github.com/prometheus/client_golang/api"
	v1 "github.com/prometheus/client_golang/api/prometheus/v1"
	"github.com/prometheus/common/model"

	"github.com/servtuner/servtuner/bench/internal/spec"
	"github.com/servtuner/servtuner/bench/internal/tlsconfig"
)

// Defaults for Options. Four attempts with pauses of 0.5s, 1s and 2s keep a
// busy Prometheus from failing a run without delaying a dead one for long.
const (
	defaultRequestTimeout = 30 * time.Second
	defaultMaxAttempts    = 4
	defaultInitialBackoff = 500 * time.Millisecond
	defaultMaxBackoff     = 4 * time.Second
)

// Options tunes the client. Zero values select the defaults above.
type Options struct {
	// RequestTimeout bounds each attempt, not the whole retried request.
	RequestTimeout time.Duration
	// MaxAttempts is the total number of tries, so 1 means no retry.
	MaxAttempts    int
	InitialBackoff time.Duration
	MaxBackoff     time.Duration
	// Now is the clock for fetched_at; it defaults to time.Now.
	Now func() time.Time
}

func (o Options) withDefaults() Options {
	if o.RequestTimeout <= 0 {
		o.RequestTimeout = defaultRequestTimeout
	}
	if o.MaxAttempts <= 0 {
		o.MaxAttempts = defaultMaxAttempts
	}
	if o.InitialBackoff <= 0 {
		o.InitialBackoff = defaultInitialBackoff
	}
	if o.MaxBackoff <= 0 {
		o.MaxBackoff = defaultMaxBackoff
	}
	if o.Now == nil {
		o.Now = time.Now
	}
	return o
}

// Window is a UTC time range of a run or a phase.
type Window struct {
	Start time.Time
	End   time.Time
}

// Spec converts the window to its bundle representation.
func (w Window) Spec() spec.TimeWindow {
	return spec.TimeWindow{Start: spec.NewUTCTimestamp(w.Start), End: spec.NewUTCTimestamp(w.End)}
}

// Client talks to one Prometheus over one hop. It is safe for
// concurrent use.
type Client struct {
	api     v1.API
	raw     api.Client
	options Options
}

// NewClient builds the client from the hop: with TLS it verifies the server and
// presents the client certificate when the hop has one; without TLS it is
// plain http://. The HTTP client comes from
// tlsconfig, so the trust pool is the system pool plus the private CA, there is
// no way to skip verification, and redirects are never followed.
func NewClient(hop tlsconfig.Hop, options Options) (*Client, error) {
	options = options.withDefaults()
	httpClient, err := tlsconfig.NewHTTPClient(hop, options.RequestTimeout)
	if err != nil {
		return nil, err
	}
	base, err := api.NewClient(api.Config{Address: hop.URL, Client: httpClient})
	if err != nil {
		return nil, fmt.Errorf("%s hop: create prometheus client for %s: %w", hop.Name, hop.URL, err)
	}
	retrying := retryingClient{
		Client:         base,
		maxAttempts:    options.MaxAttempts,
		initialBackoff: options.InitialBackoff,
		maxBackoff:     options.MaxBackoff,
	}
	return &Client{api: v1.NewAPI(retrying), raw: retrying, options: options}, nil
}

// maxPointsPerChunk keeps each request under Prometheus's limit of 11,000
// points per series, with room to spare.
const maxPointsPerChunk = 10000

// QueryRange runs a range query and returns its series. A query that matches
// nothing returns an empty matrix, not an error. Warnings are Prometheus's own
// notes about the query (for example a deprecated function). A window with more
// than maxPointsPerChunk steps is split into consecutive chunks on step
// boundaries, and the series are joined, so a long run is never refused for its
// length.
func (c *Client) QueryRange(ctx context.Context, query string, window Window, step time.Duration) (model.Matrix, v1.Warnings, error) {
	if step <= 0 {
		return nil, nil, fmt.Errorf("query range %q: step must be positive, got %s", query, step)
	}
	if !window.End.After(window.Start) {
		return nil, nil, fmt.Errorf("query range %q: window end %s is not after start %s", query, window.End, window.Start)
	}
	var merged model.Matrix
	var warnings v1.Warnings
	chunkSpan := maxPointsPerChunk * step
	for start := window.Start; !start.After(window.End); start = start.Add(chunkSpan + step) {
		end := start.Add(chunkSpan)
		if end.After(window.End) {
			end = window.End
		}
		matrix, chunkWarnings, err := c.queryRangeOnce(ctx, query, Window{Start: start, End: end}, step)
		warnings = append(warnings, chunkWarnings...)
		if err != nil {
			return nil, warnings, err
		}
		merged = appendSeries(merged, matrix)
		if end.Equal(window.End) {
			break
		}
	}
	return merged, warnings, nil
}

// appendSeries joins the series of a later chunk onto those already collected,
// matching them by label set. The next chunk starts one step after the previous
// ends, so no sample appears twice.
func appendSeries(collected, next model.Matrix) model.Matrix {
	positions := make(map[model.Fingerprint]int, len(collected))
	for i, stream := range collected {
		positions[stream.Metric.Fingerprint()] = i
	}
	for _, stream := range next {
		position, seen := positions[stream.Metric.Fingerprint()]
		if !seen {
			positions[stream.Metric.Fingerprint()] = len(collected)
			collected = append(collected, stream)
			continue
		}
		collected[position].Values = append(collected[position].Values, stream.Values...)
		collected[position].Histograms = append(collected[position].Histograms, stream.Histograms...)
	}
	return collected
}

func (c *Client) queryRangeOnce(ctx context.Context, query string, window Window, step time.Duration) (model.Matrix, v1.Warnings, error) {
	value, warnings, _, err := c.api.QueryRange(ctx, query, v1.Range{Start: window.Start, End: window.End, Step: step})
	if err != nil {
		return nil, warnings, fmt.Errorf("query range %q: %w", query, err)
	}
	matrix, isMatrix := value.(model.Matrix)
	if !isMatrix {
		return nil, warnings, fmt.Errorf("query range %q: expected a matrix, got %s", query, value.Type())
	}
	return matrix, warnings, nil
}

// QueryInstant evaluates a query at one instant, for probing.
func (c *Client) QueryInstant(ctx context.Context, query string, at time.Time) (model.Vector, error) {
	value, _, _, err := c.api.Query(ctx, query, at)
	if err != nil {
		return nil, fmt.Errorf("query %q: %w", query, err)
	}
	vector, isVector := value.(model.Vector)
	if !isVector {
		return nil, fmt.Errorf("query %q: expected a vector, got %s", query, value.Type())
	}
	return vector, nil
}

// doGet sends a GET through the retrying client and returns the body of a 2xx
// answer.
func (c *Client) doGet(ctx context.Context, endpoint string, args map[string]string) ([]byte, error) {
	address := c.raw.URL(endpoint, nil)
	query := address.Query()
	for name, value := range args {
		query.Set(name, value)
	}
	address.RawQuery = query.Encode()
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, address.String(), nil)
	if err != nil {
		return nil, fmt.Errorf("build request for %s: %w", address, err)
	}
	response, body, err := c.raw.Do(ctx, request)
	if err != nil {
		return nil, fmt.Errorf("get %s: %w", address, err)
	}
	if response.StatusCode/100 != 2 {
		return nil, fmt.Errorf("get %s: status %d: %s", address, response.StatusCode, truncate(string(body), 200))
	}
	return body, nil
}

func truncate(text string, limit int) string {
	if len(text) <= limit {
		return text
	}
	return text[:limit] + "..."
}
