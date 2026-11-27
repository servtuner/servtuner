// Package propose chooses the next configuration of a tuning session
// ("Choosing the next configuration"). It is a pure
// function of three inputs: the search space, the boot-probe results, and the
// analysed bundles of the session so far. The same inputs give the same
// proposal, byte for byte: nothing here reads a clock, the network or the
// cluster, and no map is iterated in an order that reaches the output.
//
// The search goes tier by tier (feasibility, structural, scheduler,
// fine_tuning), then confirms the best few configurations with the full
// ladder and repeats, so that the comparison can call
// a winner significant. The session's budget is the caller's, not ours.
//
// The search space and the proposal are plain structs with snake_case JSON
// tags, shaped to move into JSON Schema unchanged.
package propose
