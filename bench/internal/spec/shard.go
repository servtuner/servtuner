package spec

// ShardCount is engine.shards, 1 when absent.
func (e Engine) ShardCount() int {
	if e.Shards == nil || *e.Shards < 1 {
		return 1
	}
	return *e.Shards
}

// ShardLoad is one shard's part of the load: every stage's rate divided by
// the shard count, so the shards together offer the RunSpec's rates. The
// durations, pacing and warmup are unchanged.
func ShardLoad(load Load, shards int) Load {
	if shards <= 1 {
		return load
	}
	scaled := load
	scaled.Stages = make([]Stage, len(load.Stages))
	for index, stage := range load.Stages {
		stage.Rate /= float64(shards)
		scaled.Stages[index] = stage
	}
	return scaled
}
