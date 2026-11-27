package pipeline

import (
	"context"
	"errors"

	"github.com/servtuner/servtuner/bench/internal/spec"
	"github.com/servtuner/servtuner/bench/internal/version"
)

// finalize writes manifest.json, always last, with the checksums of every
// other file and the final status. When an earlier step failed it first closes
// the streams, so the rows collected so far are in the bundle.
func (r *run) finalize(ctx context.Context) (string, error) {
	streamErr := r.finishStreams(ctx)
	finishedAt := spec.NewUTCTimestamp(r.opts.Now())
	manifest := spec.Manifest{
		RunId:        r.opts.RunID,
		BenchVersion: version.Version,
		Status:       spec.RunStatusSucceeded,
		StartedAt:    spec.NewUTCTimestamp(r.startedAt),
		FinishedAt:   &finishedAt,
	}
	if r.hasSpec {
		name := r.runSpec.Name
		manifest.Name = &name
	}
	if r.failure != nil {
		manifest.Status = spec.RunStatusFailed
		manifest.Error = r.failure
	}
	if streamErr != nil && r.failure == nil {
		manifest.Status = spec.RunStatusFailed
		manifest.Error = &spec.Failure{Step: spec.PipelineStepFinalize, Message: streamErr.Error()}
	}
	manifestErr := r.writer.WriteManifest(ctx, manifest)
	return "", errors.Join(streamErr, manifestErr)
}
