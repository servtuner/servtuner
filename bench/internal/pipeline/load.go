package pipeline

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/servtuner/servtuner/bench/internal/bundle"
	"github.com/servtuner/servtuner/bench/internal/engine"
	"github.com/servtuner/servtuner/bench/internal/spec"
)

// postSnapshotTimeout bounds the snapshot taken right after the load.
const postSnapshotTimeout = 2 * time.Minute

// sinkStuckMessage is what the loadgen engine's error says when the request
// sink did not take its rows: the bundle could not be written, so it maps to
// the bundle-write exit code.
const sinkStuckMessage = "request sink stuck"

// load drives the engine through warmup and each stage, records the exact UTC
// window of each phase in phases.json (even when the engine fails part way),
// takes the post-load snapshot and stores the engine's native output.
func (r *run) load(ctx context.Context) (string, error) {
	phases, err := engine.PlanPhases(r.runSpec.Load)
	if err != nil {
		return "", err
	}
	sharded := r.shardCount() > 1
	if sharded {
		start, err := r.startShards(ctx)
		if err != nil {
			return "", err
		}
		if err := r.opts.Sleep(ctx, start.Sub(r.opts.Now())); err != nil {
			return "", err
		}
	}
	result, runErr := r.engine.Run(ctx, phases, r.requestSink())
	r.windows = result.Windows
	if sharded {
		// The followers' rows join the streams before normalize closes them.
		runErr = errors.Join(runErr, r.mergeShards(ctx))
	}
	if runErr != nil && strings.Contains(runErr.Error(), sinkStuckMessage) {
		runErr = fmt.Errorf("%w: %w", bundle.ErrWrite, runErr)
	}
	if reporter, ok := r.engine.(engine.StatsReporter); ok {
		r.logger.Info("engine counters", reporter.LogAttrs()...)
	}

	var writeErrs []error
	if len(r.windows) > 0 {
		if err := r.writer.WriteDocument(ctx, bundle.PhasesJSON, spec.SchemaPhases, r.phasesDocument()); err != nil {
			writeErrs = append(writeErrs, err)
		}
	}
	if ctx.Err() == nil {
		postCtx, cancel := context.WithTimeout(ctx, postSnapshotTimeout)
		captured, err := r.capture(postCtx, spec.SnapshotPhasePost)
		cancel()
		if err != nil {
			writeErrs = append(writeErrs, err)
		} else {
			r.postSnapshot = &captured.Snapshot
		}
	}
	if err := r.writeEnvironment(ctx); err != nil {
		writeErrs = append(writeErrs, err)
	}
	if result.NativeOutputDir != "" {
		if err := r.storeNativeOutput(ctx, result.NativeOutputDir); err != nil {
			writeErrs = append(writeErrs, err)
		}
	}
	if runErr != nil {
		runErr = fmt.Errorf("engine %s: %w", r.engine.Name(), runErr)
	}
	return "", errors.Join(append([]error{runErr}, writeErrs...)...)
}

// phasesDocument is phases.json: the measured windows, the offered rate of each
// stage from the engine's pacer, and the warnings found at resolve.
func (r *run) phasesDocument() spec.Phases {
	document := spec.Phases{
		Phases:         []spec.Phase{},
		Warnings:       r.report.StageWarnings,
		ScrapeWarnings: r.report.ScrapeWarnings,
	}
	var offeredHits []float64
	if reporter, ok := r.engine.(engine.OfferedReporter); ok {
		offeredHits = reporter.OfferedHits()
	}
	// A sharded run offered the sum of its shards' hits.
	for _, hits := range r.shardOfferedHits {
		for index := range min(len(hits), len(offeredHits)) {
			offeredHits[index] += hits[index]
		}
	}
	for _, window := range r.windows {
		phase := spec.Phase{
			Name:  window.Phase.Name,
			Kind:  window.Phase.Kind,
			Start: spec.NewUTCTimestamp(window.Start),
			End:   spec.NewUTCTimestamp(window.End),
		}
		if window.Phase.Kind == spec.PhaseKindStage {
			offered := window.Phase.OfferedRPS
			stageIndex := *window.Phase.StageIndex
			if stageIndex < len(offeredHits) && window.Phase.Duration > 0 {
				offered = offeredHits[stageIndex] / window.Phase.Duration.Seconds()
			}
			phase.StageIndex = window.Phase.StageIndex
			phase.OfferedRps = &offered
		}
		document.Phases = append(document.Phases, phase)
	}
	return document
}

// storeNativeOutput adds the engine's untouched output to the bundle's
// engine/ directory. Output the engine already wrote there is only recorded in
// the manifest; output elsewhere is copied in.
func (r *run) storeNativeOutput(ctx context.Context, dir string) error {
	bundleEngineDir := filepath.Join(r.opts.BundleDir, nativeOutputDirName)
	inPlace := filepath.Clean(dir) == filepath.Clean(bundleEngineDir)
	return filepath.WalkDir(dir, func(location string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() {
			return nil
		}
		relative, err := filepath.Rel(dir, location)
		if err != nil {
			return err
		}
		name := nativeOutputDirName + "/" + filepath.ToSlash(relative)
		if inPlace {
			return r.writer.RecordExistingFile(ctx, name)
		}
		content, err := os.ReadFile(location)
		if err != nil {
			return fmt.Errorf("read engine output %s: %w", location, err)
		}
		return r.writer.WriteFile(ctx, name, content)
	})
}
