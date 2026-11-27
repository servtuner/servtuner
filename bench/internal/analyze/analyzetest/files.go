package analyzetest

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"

	"github.com/servtuner/servtuner/bench/internal/analyze/input"
	"github.com/servtuner/servtuner/bench/internal/bundle"
	"github.com/servtuner/servtuner/bench/internal/spec"
)

// WriteBundle writes the scenario as a bundle directory, generated from the
// scenario and nothing else. Every document is checked against its schema
// first, so a broken scenario fails here and not in the analyzer. The request
// records go through the production writer.
func (s Scenario) WriteBundle(dir string) error {
	files := map[string][]byte{}
	put := func(name, schemaName string, document any) error {
		encoded, err := json.Marshal(document)
		if err != nil {
			return fmt.Errorf("encode %s: %w", name, err)
		}
		if err := spec.Validate(schemaName, encoded); err != nil {
			return fmt.Errorf("%s: %w", name, err)
		}
		files[name] = encoded
		return nil
	}
	finish := spec.NewUTCTimestamp(s.stageWindow(len(s.Stages) - 1).End)
	manifest := spec.Manifest{
		BundleVersion: bundle.BundleVersion, RunId: s.RunID(), Name: &s.Name, BenchVersion: "0.0.0-synthetic",
		Status: spec.RunStatusSucceeded, StartedAt: spec.NewUTCTimestamp(StartTime), FinishedAt: &finish, Files: []spec.ManifestFile{},
	}
	pod := BenchPod
	environment := spec.Environment{
		BenchVersion: "0.0.0-synthetic", GoVersion: "go0", Pod: &pod, ClockSource: "synthetic",
		Engine: spec.EngineInfo{Name: "loadgen", Version: "0"}, Transport: spec.HopTransports{Target: spec.HopTransport{}},
	}
	if s.Bifrost != nil {
		environment.Knobs = s.Bifrost.knobs()
		configFiles, err := s.bifrostFiles()
		if err != nil {
			return err
		}
		for name, content := range configFiles {
			files[name] = content
		}
	}
	for _, step := range []struct {
		name, schema string
		document     any
	}{
		{bundle.ManifestJSON, spec.SchemaManifest, manifest},
		{bundle.ResolvedSpecJSON, spec.SchemaRunSpec, s.RunSpecDocument()},
		{bundle.PhasesJSON, spec.SchemaPhases, s.Phases()},
		{bundle.EnvironmentJSON, spec.SchemaEnvironment, environment},
	} {
		if err := put(step.name, step.schema, step.document); err != nil {
			return err
		}
	}
	if s.Targets != nil {
		if err := put(bundle.PrometheusTargetsJSON, spec.SchemaPrometheusTargets, spec.PrometheusTargets{FetchedAt: spec.NewUTCTimestamp(StartTime), Targets: s.Targets}); err != nil {
			return err
		}
	}
	if s.Queries != nil {
		if err := put(bundle.PrometheusQueriesJSON, spec.SchemaPrometheusQueries, s.Queries); err != nil {
			return err
		}
	}
	for name, snapshot := range map[string]*spec.TargetSnapshot{bundle.SnapshotPreJSON: s.Pre, bundle.SnapshotPostJSON: s.Post} {
		if snapshot != nil {
			if err := put(name, spec.SchemaTargetSnapshot, snapshot); err != nil {
				return err
			}
		}
	}
	matrix := s.Matrix()
	for _, entry := range s.EntryRefs() {
		series, _, _ := matrix.Matrix(entry)
		encoded, err := matrixData(series)
		if err != nil {
			return err
		}
		files[bundle.PrometheusSeriesFile(string(entry.Catalog), entry.Name)] = encoded
	}
	for name, content := range files {
		path := filepath.Join(dir, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			return err
		}
		if err := os.WriteFile(path, append(content, '\n'), 0o644); err != nil {
			return err
		}
	}
	return s.writeRequests(dir)
}

func (s Scenario) writeRequests(dir string) error {
	stream, err := bundle.NewRequestStream(dir, bundle.StreamOptions{})
	if err != nil {
		return err
	}
	for _, row := range s.Records() {
		if err := stream.Write(row); err != nil {
			_ = stream.Close()
			return err
		}
	}
	return stream.Close()
}

// matrixData renders series as the `data` object of a range-query response.
func matrixData(series []input.Series) ([]byte, error) {
	type result struct {
		Metric map[string]string `json:"metric"`
		Values [][2]any          `json:"values"`
	}
	results := make([]result, len(series))
	for i, one := range series {
		results[i] = result{Metric: one.Labels, Values: make([][2]any, len(one.Samples))}
		for j, sample := range one.Samples {
			results[i].Values[j] = [2]any{sample.At.Unix(), strconv.FormatFloat(sample.Value, 'g', -1, 64)}
		}
	}
	return json.Marshal(map[string]any{"resultType": "matrix", "result": results})
}
