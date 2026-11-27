package prometheus

import (
	"os"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/goccy/go-yaml"

	"github.com/servtuner/servtuner/bench/internal/spec"
)

// The dedicated cAdvisor job keeps only listed metrics, so a catalog query on a
// metric missing from the list would find no data on that job.
func TestServtunerCadvisorJobKeepsEveryCadvisorMetricTheCatalogReads(t *testing.T) {
	keep := cadvisorKeepRegex(t, "../../../deploy/prometheus/servtuner-cadvisor.values.yaml")
	catalog, err := CatalogFor(spec.CatalogNameKubernetes)
	if err != nil {
		t.Fatal(err)
	}
	rendered, err := catalog.Render(renderInputFor(catalog, 15*time.Second))
	if err != nil {
		t.Fatal(err)
	}
	for _, query := range rendered {
		for _, block := range selectorBlocks(query.Query) {
			if strings.Contains(block[1], cadvisorMetricsPath) && !keep.MatchString(block[0]) {
				t.Errorf("%s reads %s, which servtuner-cadvisor drops", query.Entry.Name, block[0])
			}
		}
	}
}

func cadvisorKeepRegex(t *testing.T, path string) *regexp.Regexp {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var values struct {
		ExtraScrapeConfigs string `yaml:"extraScrapeConfigs"`
	}
	if err := yaml.Unmarshal(raw, &values); err != nil {
		t.Fatal(err)
	}
	var jobs []struct {
		JobName        string `yaml:"job_name"`
		MetricRelabels []struct {
			SourceLabels []string `yaml:"source_labels"`
			Regex        string   `yaml:"regex"`
			Action       string   `yaml:"action"`
		} `yaml:"metric_relabel_configs"`
	}
	if err := yaml.Unmarshal([]byte(values.ExtraScrapeConfigs), &jobs); err != nil {
		t.Fatal(err)
	}
	for _, job := range jobs {
		if job.JobName != "servtuner-cadvisor" {
			continue
		}
		for _, rule := range job.MetricRelabels {
			if rule.Action == "keep" && len(rule.SourceLabels) == 1 && rule.SourceLabels[0] == "__name__" {
				// Prometheus anchors relabel regexes.
				return regexp.MustCompile("^(?:" + rule.Regex + ")$")
			}
		}
	}
	t.Fatal("servtuner-cadvisor has no __name__ keep rule")
	return nil
}
