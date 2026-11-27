package engine

import "time"

// RequestRow is one row of requests.parquet. spec.RequestRecord is the JSON
// description of the same record; this is the typed row with real timestamps,
// and a test keeps the column names equal to that schema's properties. Optional
// columns are pointers (null in Parquet when not measured).
type RequestRow struct {
	// ScheduledAt and SendLagSeconds are nil (null in Parquet) for an engine
	// that records no schedule, such as inference-perf 0.7.0.
	ScheduledAt         *time.Time `parquet:"scheduled_at,optional,timestamp(nanosecond)"`
	SentAt              time.Time  `parquet:"sent_at,timestamp(nanosecond)"`
	SendLagSeconds      *float64   `parquet:"send_lag_seconds,optional"`
	TTFBSeconds         *float64   `parquet:"ttfb_seconds,optional"`
	TTFTSeconds         *float64   `parquet:"ttft_seconds,optional"`
	FirstContentSeconds *float64   `parquet:"first_content_seconds,optional"`
	ITLSeconds          []float64  `parquet:"itl_seconds,list"`
	E2ESeconds          float64    `parquet:"e2e_seconds"`
	InputTokens         *int64     `parquet:"input_tokens,optional"`
	OutputTokens        *int64     `parquet:"output_tokens,optional"`
	RequestBytes        *int64     `parquet:"request_bytes,optional"`
	Status              int64      `parquet:"status"`
	Error               *string    `parquet:"error,optional"`
	// Phase is RowPhaseWarmup or RowPhaseStage. Rows read from a bundle that
	// predates the column have "", which readers treat as a stage row.
	Phase string `parquet:"phase"`
	// StageIndex is required when Phase is stage and nil for warmup rows.
	StageIndex *int64 `parquet:"stage_index,optional"`
	Worker     *int64 `parquet:"worker,optional"`
}

// Values of RequestRow.Phase.
const (
	RowPhaseWarmup = "warmup"
	RowPhaseStage  = "stage"
)

// Sink receives request rows while a run is in progress. Engines write to it;
// the harness turns what it collected into requests.parquet. Write may be
// called from many goroutines. Implementations may queue rows: when the queue
// is full Write blocks (back-pressure on the engine) rather than dropping the
// row, and an implementation that fails to persist a row returns the error from
// Write or from its closing call, never silently.
type Sink interface {
	Write(row RequestRow) error
}

// ClientSampleRow is one row of client/samples.parquet; spec.ClientSample is the
// JSON description of the same record, and a test keeps the columns equal to
// that schema.
type ClientSampleRow struct {
	SampledAt         time.Time `parquet:"sampled_at,timestamp(nanosecond)"`
	Source            string    `parquet:"source"`
	StageIndex        *int64    `parquet:"stage_index,optional"`
	Goroutines        int64     `parquet:"goroutines"`
	InFlightRequests  int64     `parquet:"in_flight_requests"`
	SendLagMaxSeconds float64   `parquet:"send_lag_max_seconds"`
	SendLagP99Seconds float64   `parquet:"send_lag_p99_seconds"`
	Worker            *int64    `parquet:"worker,optional"`
}

// ClientSink receives the engine's once-a-second self-monitoring samples. Like
// Sink it may be called from many goroutines.
type ClientSink interface {
	WriteSample(sample ClientSampleRow) error
}
