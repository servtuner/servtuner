package prometheus

import (
	"os"
	"regexp"
	"strings"
	"testing"

	"github.com/servtuner/servtuner/bench/internal/spec"
)

// sglangLiveMetrics is SGLang v0.5.23's /metrics after a short benchmark,
// recorded on an RTX 3070 (#33).
const sglangLiveMetrics = "../target/testdata/sglang/metrics-after-load.txt"

var sglangMetricName = regexp.MustCompile(`sglang:[a-z_]+`)

// Every series the SGLang catalog queries exists on a live server, except the
// ones it marks may_be_empty (counters SGLang creates at their first event).
func TestSGLangCatalogQueriesNameLiveMetrics(t *testing.T) {
	recorded, err := os.ReadFile(sglangLiveMetrics)
	if err != nil {
		t.Fatal(err)
	}
	live := map[string]bool{}
	for _, line := range strings.Split(string(recorded), "\n") {
		if name := sglangMetricName.FindString(line); name != "" && !strings.HasPrefix(line, "#") {
			live[name] = true
		}
	}
	catalog, err := CatalogFor(spec.CatalogNameSglang)
	if err != nil {
		t.Fatal(err)
	}
	queries := append([]Entry{}, catalog.Queries...)
	queries = append(queries, Entry{Name: "marker", Template: catalog.Marker})
	for _, entry := range queries {
		if entry.MayBeEmpty {
			continue
		}
		for _, query := range append([]string{entry.Query}, entry.Alternates...) {
			for _, name := range sglangMetricName.FindAllString(query, -1) {
				// A histogram matched by regex, name_(bucket|sum|count), leaves name_.
				name = strings.TrimSuffix(name, "_")
				if !live[name] && !live[name+"_bucket"] && !live[name+"_total"] {
					t.Errorf("%s queries %s, which SGLang 0.5.23 does not export", entry.Name, name)
				}
			}
		}
	}
}
