package propose

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/servtuner/servtuner/bench/internal/analyze"
	"github.com/servtuner/servtuner/bench/internal/analyze/compare"
	"github.com/servtuner/servtuner/bench/internal/bundle"
	"github.com/servtuner/servtuner/bench/internal/spec"
)

// Evidence is one analysed bundle as propose reads it: the comparison's view
// of the run, and the advisors' recommendations on it.
type Evidence struct {
	Run    compare.Run
	Advice []Advice
}

// Advice is one knob recommendation of recommendations.json: the rule that
// fired (V1 to V5), the knob, and where it points.
type Advice struct {
	Rule        string
	Knob        string
	Recommended *float64 // nil when the rule names a direction but no value
	Max         *float64
}

// LoadEvidence reads the analysed bundle directories. It reads
// manifest.json, spec.resolved.json, analysis.json, capacity_model.json and
// recommendations.json, and never the request records: latency objectives are
// not what the search ranks.
func LoadEvidence(ctx context.Context, bundleDirs []string) ([]Evidence, error) {
	evidence := make([]Evidence, 0, len(bundleDirs))
	for _, dir := range bundleDirs {
		run, err := analyze.LoadComparisonRun(ctx, dir)
		if err != nil {
			return nil, fmt.Errorf("bundle %s: %w", dir, err)
		}
		run.TTFT = nil // no request records: the TTFT objectives stay unavailable
		advice, err := readAdvice(dir)
		if err != nil {
			return nil, fmt.Errorf("bundle %s: %w", dir, err)
		}
		evidence = append(evidence, Evidence{Run: run, Advice: advice})
	}
	return evidence, nil
}

func readAdvice(bundleDir string) ([]Advice, error) {
	content, err := os.ReadFile(filepath.Join(bundleDir, bundle.RecommendationsJSON))
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil // a bundle with no advisor has no advice
	}
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", bundle.RecommendationsJSON, err)
	}
	file, err := spec.Parse[spec.Recommendations](spec.SchemaRecommendations, content)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", bundle.RecommendationsJSON, err)
	}
	var advice []Advice
	for _, recommendation := range file.Recommendations {
		if recommendation.Subject.Kind != spec.RecommendationSubjectKindKnob {
			continue
		}
		item := Advice{Rule: recommendation.Rule, Knob: recommendation.Subject.Name, Max: recommendation.Max}
		if value, ok := recommendation.Recommended.(float64); ok {
			item.Recommended = &value
		}
		advice = append(advice, item)
	}
	return advice, nil
}
