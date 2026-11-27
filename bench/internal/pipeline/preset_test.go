package pipeline

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"

	"github.com/servtuner/servtuner/bench/internal/bundle"
	"github.com/servtuner/servtuner/bench/internal/spec"
	"github.com/servtuner/servtuner/bench/internal/validate"
)

func TestResolveExpandsWorkloadPreset(t *testing.T) {
	options, store, _ := newOptions(t, readExample(t, "valid", "workload-preset.json"))
	if result := Execute(context.Background(), options); result.Err != nil {
		t.Fatal(result.Err)
	}
	read := func(key string) string {
		reader, err := store.Get(context.Background(), key)
		if err != nil {
			t.Fatal(err)
		}
		defer reader.Close()
		content, _ := io.ReadAll(reader)
		return string(content)
	}
	resolved, err := spec.ParseRunSpec([]byte(read(bundle.ResolvedSpecJSON)))
	if err != nil {
		t.Fatal(err)
	}
	if resolved.Workload.Preset != nil || resolved.Workload.Dataset == nil || resolved.Workload.Dataset.InputTokens.Mean != 256 {
		t.Errorf("workload not expanded: %+v", resolved.Workload)
	}
	if resolved.WorkloadPreset == nil || resolved.WorkloadPreset.Name != "chat-short" || resolved.WorkloadPreset.Version != 1 {
		t.Errorf("workload_preset = %+v", resolved.WorkloadPreset)
	}
	if !strings.Contains(read(bundle.SpecYAML), `"preset": "chat-short@1"`) {
		t.Error("spec.yaml must keep the submitted preset reference")
	}
}

func TestResolveRejectsUnknownPreset(t *testing.T) {
	document := strings.Replace(string(readExample(t, "valid", "workload-preset.json")), "chat-short@1", "chat-short@42", 1)
	options, _, _ := newOptions(t, []byte(document))
	result := Execute(context.Background(), options)
	if !errors.Is(result.Err, validate.ErrInvalidSpec) || !errors.Is(result.Err, spec.ErrUnknownPreset) {
		t.Errorf("err = %v, want an invalid spec naming the unknown preset", result.Err)
	}
}
