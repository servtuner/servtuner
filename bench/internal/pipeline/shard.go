package pipeline

// Sharded load generation (engine.shards). Shard 0 leads: it runs
// the whole pipeline. Shards 1..N-1 follow: each builds the same engine for
// its share of the load (rate / N, every N-th request) and sends nothing else.
// The shards meet only through the bundle store, so no new network hop exists
// and no transport setting has to be added:
//
//	shards/<i>/ready.json      a follower's engine is prepared
//	shards/plan.json           the leader's common start time, once every follower is ready
//	shards/<i>/requests.parquet, shards/<i>/client/samples.parquet
//	shards/<i>/done.json       a follower finished (its status, error and offered hits)
//
// The leader merges each follower's rows into requests.parquet and
// client/samples.parquet (the worker column says which shard sent a row), so
// analysis reads one record of the whole run. The shards' clocks are the
// nodes' UTC clocks: their skew adds to the stage-window error.

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"time"

	"github.com/servtuner/servtuner/bench/internal/bundle"
	"github.com/servtuner/servtuner/bench/internal/engine"
	"github.com/servtuner/servtuner/bench/internal/spec"
	"github.com/servtuner/servtuner/bench/internal/storage"
	"github.com/servtuner/servtuner/bench/internal/validate"
)

const (
	shardPlanKey = "shards/plan.json"
	// DefaultShardReadyTimeout bounds the leader's wait for every follower to
	// be ready, and a follower's wait for the plan: long enough for a pod's
	// image pull and scheduling.
	DefaultShardReadyTimeout = 10 * time.Minute
	// DefaultShardDoneTimeout bounds the leader's wait, after its own load,
	// for every follower to report done: a follower drains its in-flight
	// requests (loadgen's drain timeout) and uploads its rows.
	DefaultShardDoneTimeout = 5 * time.Minute
	// shardStartLead is how far ahead of now the leader sets the start, so
	// every follower sees the plan before it is due.
	shardStartLead    = 5 * time.Second
	shardPollInterval = time.Second
	// maxShardOffset caps a shard's start offset (shardOffset).
	maxShardOffset = time.Second
)

func shardKey(index int, name string) string {
	return "shards/" + strconv.Itoa(index) + "/" + name
}

type shardPlan struct {
	StartAt time.Time `json:"start_at"`
	Shards  int       `json:"shards"`
}

// shardReady is a follower's ready.json: its pod, so the leader collects its
// CPU series too (client_bound covers every shard).
type shardReady struct {
	Shard int    `json:"shard"`
	Pod   string `json:"pod,omitempty"`
}

type shardDone struct {
	Status      spec.RunStatus `json:"status"`
	Error       string         `json:"error,omitempty"`
	OfferedHits []float64      `json:"offered_hits,omitempty"`
}

// shardOffset staggers the shards' starts by one inter-arrival time of the
// whole first stage, so N constant pacers interleave instead of firing N
// requests at once. Poisson shards draw independent arrivals anyway.
func shardOffset(load spec.Load, index int) time.Duration {
	if index == 0 || len(load.Stages) == 0 || load.Stages[0].Rate <= 0 {
		return 0
	}
	offset := time.Duration(float64(index) / load.Stages[0].Rate * float64(time.Second))
	return min(offset, maxShardOffset)
}

// workerSink tags every row with the shard that sent it.
type workerSink struct {
	worker   int64
	requests engine.Sink
	samples  engine.ClientSink
}

func (s workerSink) Write(row engine.RequestRow) error {
	row.Worker = &s.worker
	return s.requests.Write(row)
}

func (s workerSink) WriteSample(sample engine.ClientSampleRow) error {
	sample.Worker = &s.worker
	return s.samples.WriteSample(sample)
}

// shardStore is where the shards meet: the final store (the S3 bucket for an
// s3:// --out), not the local working copy.
func (r *run) shardStore() storage.BundleStore {
	if r.opts.ShardStore != nil {
		return r.opts.ShardStore
	}
	return r.opts.Store
}

func (r *run) shardCount() int {
	if !r.hasSpec {
		return 1
	}
	return r.runSpec.Engine.ShardCount()
}

// startShards waits until every follower is ready, publishes the plan and
// returns the common start time.
func (r *run) startShards(ctx context.Context) (time.Time, error) {
	shards := r.shardCount()
	store := r.shardStore()
	deadline := r.opts.Now().Add(r.opts.ShardReadyTimeout)
	for index := 1; index < shards; index++ {
		if err := waitForKey(ctx, store, shardKey(index, "ready.json"), deadline, r.opts.Now, r.opts.Sleep); err != nil {
			return time.Time{}, fmt.Errorf("shard %d of %d never became ready (start it with servtuner run --shard-index %d): %w", index, shards, index, err)
		}
		var ready shardReady
		if err := getJSON(ctx, store, shardKey(index, "ready.json"), &ready); err != nil {
			return time.Time{}, err
		}
		if ready.Pod != "" {
			r.shardPods = append(r.shardPods, ready.Pod)
		}
	}
	plan := shardPlan{StartAt: r.opts.Now().Add(shardStartLead).UTC(), Shards: shards}
	if err := putJSON(ctx, store, shardPlanKey, plan); err != nil {
		return time.Time{}, err
	}
	r.logger.Info("shards ready", "shards", shards, "start_at", plan.StartAt)
	return plan.StartAt, nil
}

// mergeShards waits for every follower to finish and appends its rows to the
// run's streams. A follower that failed, or never finished, fails the run:
// its share of the offered load is missing.
func (r *run) mergeShards(ctx context.Context) error {
	shards := r.shardCount()
	store := r.shardStore()
	deadline := r.opts.Now().Add(r.opts.ShardDoneTimeout)
	r.shardOfferedHits = nil
	var problems []error
	for index := 1; index < shards; index++ {
		if err := waitForKey(ctx, store, shardKey(index, "done.json"), deadline, r.opts.Now, r.opts.Sleep); err != nil {
			problems = append(problems, fmt.Errorf("shard %d never finished: %w", index, err))
			continue
		}
		var done shardDone
		if err := getJSON(ctx, store, shardKey(index, "done.json"), &done); err != nil {
			problems = append(problems, err)
			continue
		}
		// Rows a failed shard did send are still merged (raw first).
		if err := r.mergeShardFile(ctx, index, bundle.RequestsParquet, func(path string) error {
			return copyRows(path, r.requests.Write)
		}); err != nil {
			problems = append(problems, err)
		}
		if err := r.mergeShardFile(ctx, index, bundle.ClientSamplesParquet, func(path string) error {
			return copyRows(path, r.samples.WriteSample)
		}); err != nil {
			problems = append(problems, err)
		}
		if done.Status != spec.RunStatusSucceeded {
			problems = append(problems, fmt.Errorf("shard %d failed: %s", index, done.Error))
			continue
		}
		r.shardOfferedHits = append(r.shardOfferedHits, done.OfferedHits)
	}
	return errors.Join(append(problems, r.recordShardFiles(ctx))...)
}

// recordShardFiles keeps the shards' files in the bundle and its manifest:
// raw first, so each shard's own rows and its coordination record stay
// beside the merged requests.parquet. With an s3:// --out they are copied
// into the local working copy, which is uploaded at the end.
func (r *run) recordShardFiles(ctx context.Context) error {
	keys, err := r.shardStore().List(ctx, "shards/")
	if err != nil {
		return fmt.Errorf("list the shards' files: %w", err)
	}
	var problems []error
	for _, key := range keys {
		if _, err := r.opts.Store.Stat(ctx, key); errors.Is(err, storage.ErrNotFound) {
			if err := copyKey(ctx, r.shardStore(), r.opts.Store, key); err != nil {
				problems = append(problems, err)
				continue
			}
		}
		if err := r.writer.RecordExistingFile(ctx, key); err != nil {
			problems = append(problems, err)
		}
	}
	return errors.Join(problems...)
}

func copyKey(ctx context.Context, from, to storage.BundleStore, key string) error {
	content, err := from.Get(ctx, key)
	if err != nil {
		return fmt.Errorf("read %s: %w", key, err)
	}
	defer content.Close()
	if err := to.Put(ctx, key, content); err != nil {
		return fmt.Errorf("%w: copy %s: %w", bundle.ErrWrite, key, err)
	}
	return nil
}

func copyRows[T any](path string, write func(T) error) error {
	for row, err := range bundle.ReadRows[T](path) {
		if err != nil {
			return fmt.Errorf("read %s: %w", path, err)
		}
		if err := write(row); err != nil {
			return fmt.Errorf("%w: %w", bundle.ErrWrite, err)
		}
	}
	return nil
}

// mergeShardFile downloads one follower file to a temp file and hands its path
// to merge. A missing file means the follower wrote no rows of that kind.
func (r *run) mergeShardFile(ctx context.Context, index int, name string, merge func(path string) error) error {
	content, err := r.shardStore().Get(ctx, shardKey(index, name))
	if errors.Is(err, storage.ErrNotFound) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("read shard %d %s: %w", index, name, err)
	}
	defer content.Close()
	temp, err := os.CreateTemp("", "bench-shard-*.parquet")
	if err != nil {
		return err
	}
	defer os.Remove(temp.Name())
	if _, err := io.Copy(temp, content); err != nil {
		temp.Close()
		return fmt.Errorf("download shard %d %s: %w", index, name, err)
	}
	if err := temp.Close(); err != nil {
		return err
	}
	return merge(temp.Name())
}

// ExecuteShard runs follower shard opts.ShardIndex (1..N-1) of a sharded run:
// it prepares the engine for its share of the load, reports ready, waits for
// the leader's start time, offers its load and uploads its rows. It writes no
// bundle files of its own and sends no status events: the leader's run is the
// run, and its exit status is authoritative.
func ExecuteShard(ctx context.Context, opts Options) error {
	applyDefaults(&opts)
	parsed, _, err := spec.LoadRunSpec(opts.SpecDocument)
	if err != nil {
		return validate.WrapParseError(err)
	}
	if err := validate.Check(parsed).Err(); err != nil {
		return err
	}
	shards := parsed.Engine.ShardCount()
	if opts.ShardIndex < 1 || opts.ShardIndex >= shards {
		return fmt.Errorf("--shard-index %d: a follower shard is 1..%d for engine.shards %d", opts.ShardIndex, shards-1, shards)
	}
	if opts.ShardStore == nil {
		opts.ShardStore = opts.Store
	}
	workDir, err := os.MkdirTemp("", "bench-shard-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(workDir)
	r := &run{opts: opts, logger: opts.Logger, secrets: secrets{dir: opts.SecretsDir}, runSpec: parsed, hasSpec: true}
	defer r.cleanup()
	done := shardDone{Status: spec.RunStatusSucceeded}
	runErr := r.followShard(ctx, workDir, &done)
	if runErr != nil {
		done.Status, done.Error = spec.RunStatusFailed, runErr.Error()
	}
	// The leader waits for done.json whatever happened, so it is always written.
	putErr := putJSON(context.WithoutCancel(ctx), opts.ShardStore, shardKey(opts.ShardIndex, "done.json"), done)
	return errors.Join(runErr, putErr)
}

func (r *run) followShard(ctx context.Context, workDir string, done *shardDone) error {
	index, store := r.opts.ShardIndex, r.opts.ShardStore
	requests, err := bundle.NewRequestStream(workDir, r.opts.StreamOptions)
	if err != nil {
		return fmt.Errorf("%w: %w", bundle.ErrWrite, err)
	}
	samples, err := bundle.NewClientStream(workDir, r.opts.StreamOptions)
	if err != nil {
		_ = requests.Close()
		return fmt.Errorf("%w: %w", bundle.ErrWrite, err)
	}
	sink := workerSink{worker: int64(index), requests: requests, samples: samples}
	var closeOnce sync.Once
	var closeErr error
	closeStreams := func() error {
		closeOnce.Do(func() { closeErr = errors.Join(requests.Close(), samples.Close()) })
		return closeErr
	}
	defer closeStreams()

	inputs := engine.Inputs{
		RunID: r.opts.RunID, OutputDir: filepath.Join(workDir, nativeOutputDirName), NumWorkers: r.opts.NumWorkers,
		ClientSink: sink, Now: r.opts.Now, Credentials: r.credentials,
		ShardIndex: index, ShardCount: r.runSpec.Engine.ShardCount(),
	}
	selected, err := r.opts.NewEngine(r.runSpec, inputs)
	if err != nil {
		return fmt.Errorf("select engine: %w", err)
	}
	if err := selected.Prepare(ctx, r.runSpec); err != nil {
		return fmt.Errorf("prepare engine %s: %w", selected.Name(), err)
	}
	if err := putJSON(ctx, store, shardKey(index, "ready.json"), shardReady{Shard: index, Pod: r.opts.Getenv("POD_NAME")}); err != nil {
		return err
	}
	deadline := r.opts.Now().Add(r.opts.ShardReadyTimeout)
	if err := waitForKey(ctx, store, shardPlanKey, deadline, r.opts.Now, r.opts.Sleep); err != nil {
		return fmt.Errorf("the leader (shard 0) published no start time: %w", err)
	}
	var plan shardPlan
	if err := getJSON(ctx, store, shardPlanKey, &plan); err != nil {
		return err
	}
	start := plan.StartAt.Add(shardOffset(r.runSpec.Load, index))
	r.logger.Info("shard waiting for the start", "shard", index, "start_at", start)
	if err := r.opts.Sleep(ctx, start.Sub(r.opts.Now())); err != nil {
		return err
	}
	phases, err := engine.PlanPhases(r.runSpec.Load)
	if err != nil {
		return err
	}
	_, runErr := selected.Run(ctx, phases, sink)
	if reporter, ok := selected.(engine.OfferedReporter); ok {
		done.OfferedHits = reporter.OfferedHits()
	}
	if err := closeStreams(); err != nil {
		runErr = errors.Join(runErr, fmt.Errorf("%w: %w", bundle.ErrWrite, err))
	}
	// Whatever rows were written are uploaded, even after an engine error.
	for _, name := range []string{bundle.RequestsParquet, bundle.ClientSamplesParquet} {
		if err := putFile(context.WithoutCancel(ctx), store, shardKey(index, name), filepath.Join(workDir, filepath.FromSlash(name))); err != nil {
			runErr = errors.Join(runErr, err)
		}
	}
	if runErr != nil {
		return fmt.Errorf("engine %s: %w", selected.Name(), runErr)
	}
	return nil
}

func putFile(ctx context.Context, store storage.BundleStore, key, path string) error {
	file, err := os.Open(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	defer file.Close()
	if err := store.Put(ctx, key, file); err != nil {
		return fmt.Errorf("%w: upload %s: %w", bundle.ErrWrite, key, err)
	}
	return nil
}

func putJSON(ctx context.Context, store storage.BundleStore, key string, document any) error {
	encoded, err := json.Marshal(document)
	if err != nil {
		return err
	}
	if err := store.Put(ctx, key, bytes.NewReader(encoded)); err != nil {
		return fmt.Errorf("%w: write %s: %w", bundle.ErrWrite, key, err)
	}
	return nil
}

func getJSON(ctx context.Context, store storage.BundleStore, key string, into any) error {
	content, err := store.Get(ctx, key)
	if err != nil {
		return fmt.Errorf("read %s: %w", key, err)
	}
	defer content.Close()
	if err := json.NewDecoder(content).Decode(into); err != nil {
		return fmt.Errorf("parse %s: %w", key, err)
	}
	return nil
}

// waitForKey polls until key exists or the deadline passes.
func waitForKey(ctx context.Context, store storage.BundleStore, key string, deadline time.Time, now func() time.Time, sleep func(context.Context, time.Duration) error) error {
	for {
		_, err := store.Stat(ctx, key)
		if err == nil {
			return nil
		}
		if !errors.Is(err, storage.ErrNotFound) {
			return fmt.Errorf("look for %s: %w", key, err)
		}
		if !now().Before(deadline) {
			return fmt.Errorf("no %s by %s", key, deadline.UTC().Format(time.RFC3339))
		}
		if err := sleep(ctx, shardPollInterval); err != nil {
			return err
		}
	}
}
