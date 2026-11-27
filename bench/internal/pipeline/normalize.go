package pipeline

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/parquet-go/parquet-go"

	"github.com/servtuner/servtuner/bench/internal/analyze"
	"github.com/servtuner/servtuner/bench/internal/bundle"
	"github.com/servtuner/servtuner/bench/internal/engine"
	"github.com/servtuner/servtuner/bench/internal/spec"
	"github.com/servtuner/servtuner/bench/internal/storage"
	"github.com/servtuner/servtuner/bench/internal/version"
)

// normalize finishes requests.parquet and client/samples.parquet. loadgen
// streams the native record, and inference-perf's rows were converted by its own
// normalizer inside Run, so both arrive through the sink and this step only
// closes the streams and records the files.
func (r *run) normalize(ctx context.Context) (string, error) {
	return "", r.finishStreams(ctx)
}

// finishStreams closes the two streams once and adds their files to the
// manifest. A stream that failed to persist a row keeps the part files it did
// write; they are compacted with bundle.Recover so every flushed row survives,
// and the failure is returned.
func (r *run) finishStreams(ctx context.Context) error {
	if !r.streams || r.streamsDone {
		return nil
	}
	r.streamsDone = true
	var problems []error
	closeOne := func(name string, closeStream func() error, stats func() bundle.StreamStats, recoverParts func(string) error) {
		path := filepath.Join(r.opts.BundleDir, filepath.FromSlash(name))
		err := closeStream()
		counters := stats()
		r.logger.Info("stream closed", "file", name, "rows", counters.Accepted, "flushed", counters.Flushed, "parts", counters.Parts, "blocked_writes", counters.Blocked, "lost", counters.Lost)
		if err != nil {
			problems = append(problems, fmt.Errorf("%w: %s: %w (%d rows lost)", bundle.ErrWrite, name, err, counters.Lost))
			r.reportUnreadableLastPart(name, path)
			if recoverErr := recoverParts(path); recoverErr != nil {
				problems = append(problems, fmt.Errorf("%w: recover %s: %w", bundle.ErrWrite, name, recoverErr))
			}
		}
		if _, statErr := os.Stat(path); statErr == nil {
			if err := r.writer.RecordExistingFile(ctx, name); err != nil {
				problems = append(problems, err)
			}
		}
	}
	closeOne(bundle.RequestsParquet, r.requests.Close, r.requests.Stats, func(path string) error {
		return bundle.Recover[engine.RequestRow](path, 0)
	})
	closeOne(bundle.ClientSamplesParquet, r.samples.Close, r.samples.Stats, func(path string) error {
		return bundle.Recover[engine.ClientSampleRow](path, 0)
	})
	return errors.Join(problems...)
}

// recoverLeftover compacts a parts directory left by a run that died, so its
// flushed rows are kept as a finalized file.
func (r *run) recoverLeftover(name, path string) error {
	if _, err := os.Stat(path + bundle.PartsDirSuffix); err != nil {
		return nil
	}
	r.reportUnreadableLastPart(name, path)
	var err error
	if name == bundle.RequestsParquet {
		err = bundle.Recover[engine.RequestRow](path, 0)
	} else {
		err = bundle.Recover[engine.ClientSampleRow](path, 0)
	}
	if err != nil {
		return fmt.Errorf("%w: recover %s: %w", bundle.ErrWrite, name, err)
	}
	r.logger.Warn("recovered a parts directory left by an earlier run", "file", name)
	return nil
}

// reportUnreadableLastPart logs the last part file of a stream when it cannot
// be opened as Parquet. bundle.Recover and the readers skip such a part (it was
// being written when the run died), so this is where it is made visible.
func (r *run) reportUnreadableLastPart(name, path string) {
	entries, err := os.ReadDir(path + bundle.PartsDirSuffix)
	if err != nil {
		return
	}
	var last string
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), "part-") && strings.HasSuffix(entry.Name(), ".parquet") && (last == "" || len(entry.Name()) > len(last) || (len(entry.Name()) == len(last) && entry.Name() > last)) {
			last = entry.Name()
		}
	}
	if last == "" {
		return
	}
	file, err := os.Open(filepath.Join(path+bundle.PartsDirSuffix, last))
	if err == nil {
		defer file.Close()
		var info os.FileInfo
		if info, err = file.Stat(); err == nil {
			_, err = parquet.OpenFile(file, info.Size())
		}
	}
	if err != nil {
		r.logger.Warn("the last part of a stream is unreadable and its rows are skipped", "file", name, "part", last, "error", err)
	}
}

// summarize computes summary.json from the stored raw files, never from
// in-memory state, so that `servtuner analyze` gives the same answer. It then writes
// analysis.json.
func (r *run) summarize(ctx context.Context) (string, error) {
	thresholds, err := r.stageValidity()
	if err != nil {
		return "", err
	}
	summary, err := analyze.SummarizeBundle(ctx, r.opts.Store, analyze.ParquetRecords(r.opts.BundleDir), thresholds)
	if err != nil {
		return "", err
	}
	if err := r.writer.WriteDocument(ctx, bundle.SummaryJSON, spec.SchemaSummary, summary); err != nil {
		return "", err
	}
	if err := r.analyze(ctx); err != nil {
		return "", fmt.Errorf("analyze: %w", err)
	}
	return "", nil
}

func (r *run) stageValidity() (spec.StageValidity, error) {
	if r.runSpec.Analysis != nil && r.runSpec.Analysis.StageValidity != nil {
		return *r.runSpec.Analysis.StageValidity, nil
	}
	return spec.DefaultStageValidity()
}

// analyze runs analyze.AnalyzeBundle on the raw files and adds analysis.json to
// the manifest. AnalyzeBundle reads manifest.json for the run id, but the real
// manifest is written last (the api never indexes a half-written
// bundle). So the analysis runs on a staging directory that links to the
// bundle's files and holds a provisional manifest; only analysis.json is copied
// back.
func (r *run) analyze(ctx context.Context) error {
	staging, err := os.MkdirTemp("", "bench-analyze-")
	if err != nil {
		return fmt.Errorf("create staging directory: %w", err)
	}
	defer os.RemoveAll(staging)

	if err := mirrorForAnalysis(r.opts.BundleDir, staging); err != nil {
		return err
	}
	provisional := spec.Manifest{
		RunId: r.opts.RunID, BenchVersion: version.Version, Status: spec.RunStatusRunning,
		StartedAt: spec.NewUTCTimestamp(r.startedAt),
	}
	if err := bundle.NewWriter(storage.NewFileStore(staging)).WriteManifest(ctx, provisional); err != nil {
		return err
	}
	if _, err := analyze.AnalyzeBundle(ctx, staging); err != nil {
		return err
	}
	for _, name := range analysisOutputs {
		content, err := os.ReadFile(filepath.Join(staging, name))
		if errors.Is(err, fs.ErrNotExist) && slices.Contains(optionalAnalysisOutputs, name) {
			continue
		}
		if err != nil {
			return fmt.Errorf("read %s: %w", name, err)
		}
		// AnalyzeBundle validated it against its schema before writing it.
		if err := r.writer.WriteFile(ctx, name, content); err != nil {
			return err
		}
	}
	return nil
}

// mirrorForAnalysis recreates the bundle's directories in staging and links
// every file, except the manifest and the analysis outputs. Files are linked
// one by one, never directories: listing does not descend into a linked
// directory, so target/config/ would be invisible to the analysis, and a
// linked patches/ would let the analysis write through it into the bundle.
func mirrorForAnalysis(bundleDir, staging string) error {
	// A relative link target resolves from the link's own directory, so the
	// links must name the bundle by its absolute path (--out ./bundle).
	bundleDir, err := filepath.Abs(bundleDir)
	if err != nil {
		return fmt.Errorf("resolve the bundle directory: %w", err)
	}
	return filepath.WalkDir(bundleDir, func(location string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		relative, err := filepath.Rel(bundleDir, location)
		if err != nil {
			return err
		}
		key := filepath.ToSlash(relative)
		if entry.IsDir() {
			return os.MkdirAll(filepath.Join(staging, relative), 0o755)
		}
		if key == bundle.ManifestJSON || slices.Contains(analysisOutputs, key) {
			return nil
		}
		if err := os.Symlink(location, filepath.Join(staging, relative)); err != nil {
			return fmt.Errorf("link %s into staging: %w", key, err)
		}
		return nil
	})
}

// analysisOutputs are the files AnalyzeBundle writes and the pipeline copies
// back from staging.
var analysisOutputs = []string{
	bundle.AnalysisJSON, bundle.RecommendationsJSON, bundle.CapacityJSON, bundle.CapacityCSV, bundle.CapacityModelJSON, bundle.ResourcesPatch,
	bundle.BifrostPatch, bundle.ChartClientTimeseriesJSON, bundle.ChartHistogramsJSON, bundle.ChartInFlightJSON, bundle.ChartServerJSON,
}

// optionalAnalysisOutputs are written only for some targets.
var optionalAnalysisOutputs = []string{bundle.BifrostPatch}
