// Package null is an engine that offers no load. It runs every phase in zero
// time and writes no request rows, so the whole pipeline and bundle can be
// exercised with no target. It is selected with the hidden
// `servtuner run --engine-override null` flag because the RunSpec schema has no
// null engine type.
package null

import (
	"context"
	"time"

	"github.com/servtuner/servtuner/bench/internal/engine"
	"github.com/servtuner/servtuner/bench/internal/spec"
)

// Name is what environment.json records as the engine.
const Name = "null"

// Engine implements engine.Engine.
type Engine struct {
	now func() time.Time
}

var _ engine.Engine = (*Engine)(nil)

// New returns a null engine that stamps phase windows with now.
func New(now func() time.Time) *Engine {
	return &Engine{now: now}
}

func (*Engine) Name() string    { return Name }
func (*Engine) Version() string { return "0" }

func (*Engine) Prepare(context.Context, spec.RunSpec) error { return nil }

// Run reports each phase with its planned duration, one after the other from
// now, without waiting: the engine sends nothing, so there is nothing to wait
// for, but a window of zero length would be rejected by the analysis.
func (e *Engine) Run(ctx context.Context, phases []engine.Phase, _ engine.Sink) (engine.Result, error) {
	var result engine.Result
	cursor := e.now()
	for _, phase := range phases {
		if err := ctx.Err(); err != nil {
			return result, err
		}
		end := cursor.Add(phase.Duration)
		result.Windows = append(result.Windows, engine.Window{Phase: phase, Start: cursor, End: end})
		cursor = end
	}
	return result, nil
}
