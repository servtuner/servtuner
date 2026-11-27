package pipeline

import (
	"context"
	"encoding/json"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/servtuner/servtuner/bench/internal/spec"
)

// fakeClock advances only when the run sleeps, so a wait times out at once.
type fakeClock struct{ now time.Time }

func (c *fakeClock) Now() time.Time { return c.now }

func (c *fakeClock) Sleep(_ context.Context, d time.Duration) error {
	c.now = c.now.Add(max(d, 0))
	return nil
}

func withFakeClock(options Options) Options {
	clock := &fakeClock{now: fixedTime}
	options.Now, options.Sleep = clock.Now, clock.Sleep
	return options
}

func TestALeaderWhoseFollowersNeverComeFailsTheLoadSaying(t *testing.T) {
	options, _, _ := newOptions(t, readExample(t, "valid", "sharded-loadgen.json"))
	result := Execute(context.Background(), withFakeClock(options))
	if result.Failure == nil || result.Failure.Step != spec.PipelineStepLoad {
		t.Fatalf("failure = %+v, want the load step", result.Failure)
	}
	if !strings.Contains(result.Failure.Message, "shard 1 of 4 never became ready") ||
		!strings.Contains(result.Failure.Message, "--shard-index 1") {
		t.Errorf("message = %q", result.Failure.Message)
	}
}

func TestAFollowerWithoutALeaderReportsDoneAsFailed(t *testing.T) {
	options, store, _ := newOptions(t, readExample(t, "valid", "sharded-loadgen.json"))
	options.ShardIndex = 2
	err := ExecuteShard(context.Background(), withFakeClock(options))
	if err == nil || !strings.Contains(err.Error(), "published no start time") {
		t.Fatalf("err = %v", err)
	}
	if _, err := store.Stat(context.Background(), shardKey(2, "ready.json")); err != nil {
		t.Errorf("ready.json: %v", err)
	}
	content, err := store.Get(context.Background(), shardKey(2, "done.json"))
	if err != nil {
		t.Fatal(err)
	}
	defer content.Close()
	raw, _ := io.ReadAll(content)
	var done shardDone
	if err := json.Unmarshal(raw, &done); err != nil || done.Status != spec.RunStatusFailed {
		t.Errorf("done.json = %s (%v)", raw, err)
	}
}

func TestAFollowerIndexOutsideTheShardsIsRefused(t *testing.T) {
	options, _, _ := newOptions(t, readExample(t, "valid", "sharded-loadgen.json"))
	options.ShardIndex = 4
	if err := ExecuteShard(context.Background(), withFakeClock(options)); err == nil || !strings.Contains(err.Error(), "1..3") {
		t.Fatalf("err = %v", err)
	}
}

func TestShardStartsInterleaveAcrossOneInterArrivalTime(t *testing.T) {
	load := spec.Load{Stages: []spec.Stage{{Rate: 100}}}
	if got := shardOffset(load, 0); got != 0 {
		t.Errorf("leader offset %s", got)
	}
	if got := shardOffset(load, 3); got != 30*time.Millisecond {
		t.Errorf("shard 3 offset %s, want 30ms (3 / 100 RPS)", got)
	}
	slow := spec.Load{Stages: []spec.Stage{{Rate: 0.5}}}
	if got := shardOffset(slow, 3); got != maxShardOffset {
		t.Errorf("slow offset %s, want the cap", got)
	}
	scaled := spec.ShardLoad(spec.Load{Stages: []spec.Stage{{Rate: 90}, {Rate: 30}}}, 3)
	if scaled.Stages[0].Rate != 30 || scaled.Stages[1].Rate != 10 {
		t.Errorf("ShardLoad = %+v", scaled.Stages)
	}
}
