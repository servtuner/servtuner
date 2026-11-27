package validate

import (
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/servtuner/servtuner/bench/internal/prometheus"
	"github.com/servtuner/servtuner/bench/internal/spec"
)

func TestMinimumStageDuration(t *testing.T) {
	cases := map[time.Duration]time.Duration{
		5 * time.Second:  60 * time.Second, // 10 x 5s = 50s, below the 60s floor
		10 * time.Second: 100 * time.Second,
		time.Minute:      10 * time.Minute, // an untuned cluster
	}
	for interval, want := range cases {
		if got := MinimumStageDuration(interval); got != want {
			t.Errorf("MinimumStageDuration(%s) = %s, want %s", interval, got, want)
		}
	}
}

func TestStageLengthBoundary(t *testing.T) {
	runSpec := runSpecWithStages("59s", "60s", "1m30s")
	warnings, errs := stageLengthProblems(runSpec, prometheus.AssumedInterval)
	if len(errs) != 0 || len(warnings) != 1 || !strings.Contains(warnings[0].Message, "load.stages[0]") {
		t.Errorf("warnings %v, errors %v", warnings, errs)
	}
}

func TestStageLengthMalformedDurationsAreErrors(t *testing.T) {
	_, errs := stageLengthProblems(runSpecWithStages("0s", "5"), prometheus.AssumedInterval)
	if len(errs) != 2 {
		t.Errorf("errors = %v", errs)
	}
}

func TestReportErrWrapsInvalidSpecAndListsEverything(t *testing.T) {
	report := Report{Errors: []string{"first", "second"}}
	err := report.Err()
	if !errors.Is(err, ErrInvalidSpec) || !strings.Contains(err.Error(), "first") || !strings.Contains(err.Error(), "second") {
		t.Errorf("err = %v", err)
	}
	if (Report{Warnings: []string{"only a warning"}}).Err() != nil {
		t.Error("warnings alone must not be an error")
	}
}

func TestTransportProblemsRejectsLossyTLS(t *testing.T) {
	lossy := map[string]any{"mode": "platform", "secretRef": map[string]any{"name": "s"}}
	if problems := transportProblems("target", lossy, "http://svc"); len(problems) != 1 {
		t.Errorf("problems = %v", problems)
	}
}

// runSpecWithStages is a spec with a default warmup and constant stages of 10
// requests per second and the given durations.
func runSpecWithStages(durations ...spec.Duration) spec.RunSpec {
	load := spec.Load{Warmup: "30s", Pacing: spec.PacingConstant}
	for _, duration := range durations {
		load.Stages = append(load.Stages, spec.Stage{Rate: 10, Duration: duration})
	}
	return spec.RunSpec{Load: load}
}

func resolutionOf(interval time.Duration, job string) prometheus.Resolution {
	return prometheus.Resolution{ScrapeInterval: prometheus.ScrapeInterval{Interval: interval, Source: spec.ScrapeIntervalSourceDiscovered, Job: job}}
}

func TestAssumedIntervalWarningKeepsItsMessageAndNamesNoJob(t *testing.T) {
	report := Check(runSpecWithStages("59s"))
	const want = "load.stages[0].duration 59s is shorter than the minimum 1m0s (max(10 x 5s scrape interval, 1m0s); the interval is assumed, not read from Prometheus)"
	if len(report.Warnings) != 1 || report.Warnings[0] != want {
		t.Fatalf("warnings = %q, want %q", report.Warnings, want)
	}
	warning := report.StageWarnings[0]
	if warning.Rule != spec.StageLengthRuleMinStageDuration || warning.ScrapeIntervalSource != spec.ScrapeIntervalSourceAssumed ||
		warning.Job != nil || warning.StageIndex == nil || *warning.StageIndex != 0 ||
		warning.RequiredDurationSeconds != 60 || warning.ActualDurationSeconds != 59 || warning.ScrapeIntervalSeconds != 5 {
		t.Errorf("warning = %+v", warning)
	}
}

func TestDiscoveredIntervalDrivesTheRulesAndNamesTheJob(t *testing.T) {
	runSpec := runSpecWithStages("5m", "10m")
	runSpec.Load.Warmup = "2m"
	report := CheckWithResolution(runSpec, resolutionOf(time.Minute, "kubelet-cadvisor"))
	if len(report.StageWarnings) != 1 {
		t.Fatalf("warnings = %+v", report.StageWarnings)
	}
	warning := report.StageWarnings[0]
	if warning.Rule != spec.StageLengthRuleMinStageDuration || warning.RequiredDurationSeconds != 600 ||
		warning.ScrapeIntervalSource != spec.ScrapeIntervalSourceDiscovered || warning.Job == nil || *warning.Job != "kubelet-cadvisor" {
		t.Errorf("warning = %+v", warning)
	}
	if !strings.Contains(warning.Message, "read from Prometheus job kubelet-cadvisor") || strings.Contains(warning.Message, "assumed") {
		t.Errorf("message = %q", warning.Message)
	}
}

func TestWarmupRule(t *testing.T) {
	runSpec := runSpecWithStages("30m")
	if got := Check(runSpec).StageWarnings; len(got) != 0 {
		t.Errorf("30s warmup at 5s: warnings = %+v", got)
	}
	warnings := CheckWithResolution(runSpec, resolutionOf(30*time.Second, "dcgm")).StageWarnings
	if len(warnings) != 1 {
		t.Fatalf("30s warmup at 30s: warnings = %+v", warnings)
	}
	warning := warnings[0]
	if warning.Rule != spec.StageLengthRuleWarmupDuration || warning.StageIndex != nil ||
		warning.RequiredDurationSeconds != 60 || warning.ActualDurationSeconds != 30 {
		t.Errorf("warning = %+v", warning)
	}
}

func TestPercentileSampleCountRuleA(t *testing.T) {
	cases := map[spec.PercentileName]int{
		spec.PercentileNameP50: 100,
		spec.PercentileNameP90: 100,
		spec.PercentileNameP95: 200,
		spec.PercentileNameP99: 1000,
	}
	for percentile, want := range cases {
		if got := RequestsForPercentile(percentile); got != want {
			t.Errorf("RequestsForPercentile(%s) = %d, want %d", percentile, got, want)
		}
	}
	p95 := spec.Duration("1s")
	runSpec := spec.RunSpec{
		Slos: &spec.Slos{TtftP95: &p95},
		Load: spec.Load{Warmup: "30s", Pacing: spec.PacingConstant, Stages: []spec.Stage{
			{Rate: 2, Duration: "90s"},  // 180 requests, fewer than 200
			{Rate: 10, Duration: "90s"}, // 900 requests
		}},
	}
	warnings := Check(runSpec).StageWarnings
	if len(warnings) != 1 {
		t.Fatalf("warnings = %+v", warnings)
	}
	warning := warnings[0]
	if warning.Rule != spec.StageLengthRulePercentileSampleCount || *warning.StageIndex != 0 ||
		*warning.Percentile != spec.PercentileNameP95 || *warning.RequiredRequests != 200 || warning.RequiredDurationSeconds != 100 {
		t.Errorf("warning = %+v", warning)
	}
	runSpec.Slos = nil
	if got := Check(runSpec).StageWarnings; len(got) != 0 {
		t.Errorf("without SLOs Rule A has no percentile to check: %+v", got)
	}
}

func TestLinearPacingAveragesThePreviousRate(t *testing.T) {
	load := spec.Load{Pacing: spec.PacingLinear, Stages: []spec.Stage{{Rate: 10}, {Rate: 30}}}
	if got := meanOfferedRate(load, 0); got != 10 {
		t.Errorf("stage 0 holds its rate: %v", got)
	}
	if got := meanOfferedRate(load, 1); got != 20 {
		t.Errorf("stage 1 ramps from 10 to 30: %v", got)
	}
}

func TestStageWarningsMatchTheTextWarnings(t *testing.T) {
	report := CheckWithResolution(runSpecWithStages("10s", "20s"), resolutionOf(10*time.Second, "servtuner-targets"))
	if len(report.Warnings) != len(report.StageWarnings) || len(report.Warnings) == 0 {
		t.Fatalf("warnings %q, structured %+v", report.Warnings, report.StageWarnings)
	}
	for i, warning := range report.StageWarnings {
		if report.Warnings[i] != warning.Message {
			t.Errorf("warning %d: %q != %q", i, report.Warnings[i], warning.Message)
		}
	}
}

func TestStageWarningsAreValidPhasesWarnings(t *testing.T) {
	p95 := spec.Duration("1s")
	runSpec := runSpecWithStages("10s")
	runSpec.Slos = &spec.Slos{TtftP95: &p95}
	runSpec.Load.Warmup = "10s"
	warnings := CheckWithResolution(runSpec, resolutionOf(30*time.Second, "dcgm")).StageWarnings
	if len(warnings) != 3 {
		t.Fatalf("want min stage, warmup and Rule A warnings, got %+v", warnings)
	}
	document, err := json.Marshal(spec.Phases{Phases: []spec.Phase{}, Warnings: warnings})
	if err != nil {
		t.Fatal(err)
	}
	if err := spec.Validate(spec.SchemaPhases, document); err != nil {
		t.Errorf("phases.json with these warnings is invalid: %v\n%s", err, document)
	}
}

func TestMissingRolesWarn(t *testing.T) {
	resolution := resolutionOf(5*time.Second, "servtuner-cadvisor")
	resolution.MissingRoles = []spec.JobRole{spec.JobRoleTarget, spec.JobRoleCadvisor}
	runSpec := runSpecWithStages("5m")

	report := CheckWithResolution(runSpec, resolution)
	if len(report.Warnings) != 1 || !strings.Contains(report.Warnings[0], "servtuner-targets job") {
		t.Errorf("without a workload only the target role warns: %q", report.Warnings)
	}
	if len(report.StageWarnings) != 0 {
		t.Errorf("a missing role is not a stage-length warning: %+v", report.StageWarnings)
	}
	if len(report.ScrapeWarnings) != 1 || report.ScrapeWarnings[0].Role != spec.JobRoleTarget ||
		report.ScrapeWarnings[0].Rule != spec.ScrapeWarningRuleMissingScrapeRole || report.ScrapeWarnings[0].Message != report.Warnings[0] {
		t.Errorf("the structured record must mirror the text warning: %+v", report.ScrapeWarnings)
	}

	runSpec.Target.Workload = &spec.WorkloadSelector{}
	report = CheckWithResolution(runSpec, resolution)
	if len(report.Warnings) != 2 || !strings.Contains(report.Warnings[1], "cAdvisor") {
		t.Errorf("a Kubernetes target also warns about cAdvisor: %q", report.Warnings)
	}
	if len(report.ScrapeWarnings) != 2 || report.ScrapeWarnings[1].Role != spec.JobRoleCadvisor {
		t.Errorf("scrape warnings = %+v", report.ScrapeWarnings)
	}
	document, err := json.Marshal(spec.Phases{Phases: []spec.Phase{}, ScrapeWarnings: report.ScrapeWarnings})
	if err != nil {
		t.Fatal(err)
	}
	if err := spec.Validate(spec.SchemaPhases, document); err != nil {
		t.Errorf("phases.json with these scrape warnings is invalid: %v\n%s", err, document)
	}

	resolution.MissingRoles = []spec.JobRole{spec.JobRoleDcgm}
	if got := CheckWithResolution(runSpec, resolution).Warnings; len(got) != 0 {
		t.Errorf("an unused dcgm job is not worth a warning: %q", got)
	}
	if got := Check(runSpec).Warnings; len(got) != 0 {
		t.Errorf("an assumed interval has nothing to be missing: %q", got)
	}
}

func TestUpstreamModeIsCheckedAgainstItsOwnURL(t *testing.T) {
	endpoint := "http://mock.invalid:8000/v1"
	runSpec := runSpecWithStages("5m")
	runSpec.Target = spec.Target{
		Endpoint: "https://router.invalid/v1",
		Tls:      spec.TlsEnabled{Enabled: true, ClientCert: &spec.TlsClientCert{SecretRef: spec.SecretRef{Name: "servtuner-client"}}},
		Upstreams: []spec.Upstream{
			{Name: "mock", Endpoint: &endpoint, Tls: spec.TlsEnabled{Enabled: true, ClientCert: &spec.TlsClientCert{SecretRef: spec.SecretRef{Name: "servtuner-client"}}}},
			{Name: "ok", Endpoint: &endpoint, Tls: spec.TlsDisabled{}},
		},
	}
	errs := Check(runSpec).Errors
	if len(errs) != 1 || !strings.Contains(errs[0], "upstream mock") {
		t.Errorf("only the TLS upstream with an http:// url is an error, and the target's tls must not rescue it: %q", errs)
	}
}

func TestTLSNeedsAnHTTPSURL(t *testing.T) {
	runSpec := runSpecWithStages("5m")
	runSpec.Target = spec.Target{Endpoint: "http://vllm.invalid:8000/v1", Tls: spec.TlsEnabled{Enabled: true}}
	errs := Check(runSpec).Errors
	if len(errs) != 1 || !strings.Contains(errs[0], "tls enabled needs an https:// url") {
		t.Errorf("tls with an http:// url is an error: %q", errs)
	}
	runSpec.Target.Endpoint = "https://vllm.invalid:8000/v1"
	if errs := Check(runSpec).Errors; len(errs) != 0 {
		t.Errorf("tls with an https:// url is valid: %q", errs)
	}
}

func TestSharedPrefixProblems(t *testing.T) {
	withPrefix := func(tokens int, change func(*spec.RunSpec)) spec.RunSpec {
		runSpec := runSpecWithStages("5m")
		runSpec.Target = spec.Target{Endpoint: "http://vllm.invalid:8000/v1", Tls: spec.TlsDisabled{}}
		runSpec.Engine.Type = spec.EngineTypeLoadgen
		runSpec.Workload = spec.Workload{Api: spec.WorkloadApiChat, Dataset: &spec.Dataset{
			Type: spec.DatasetTypeSynthetic, InputTokens: spec.TokenDistribution{Mean: 2048}, OutputTokens: spec.TokenDistribution{Mean: 128},
			SharedPrefix: &spec.SharedPrefix{Tokens: tokens, Groups: 8},
		}}
		if change != nil {
			change(&runSpec)
		}
		return runSpec
	}
	if errs := Check(withPrefix(1536, nil)).Errors; len(errs) != 0 {
		t.Fatalf("a prefix inside the prompt is valid: %q", errs)
	}
	cases := map[string]struct {
		runSpec spec.RunSpec
		want    string
	}{
		"prefix as long as the prompt": {withPrefix(2048, nil), "must be below input_tokens.mean"},
		"with a conversation":          {withPrefix(512, func(s *spec.RunSpec) { s.Workload.Conversation = &spec.Conversation{Turns: 2} }), "cannot be combined"},
		"embeddings":                   {withPrefix(512, func(s *spec.RunSpec) { s.Workload.Api = spec.WorkloadApiEmbeddings }), "embeddings"},
	}
	for name, test := range cases {
		t.Run(name, func(t *testing.T) {
			errs := Check(test.runSpec).Errors
			if !slices.ContainsFunc(errs, func(e string) bool { return strings.Contains(e, test.want) }) {
				t.Errorf("errors %q, want one mentioning %q", errs, test.want)
			}
		})
	}
}

func TestMetricsAndWorkloadAreExclusive(t *testing.T) {
	runSpec := runSpecWithStages("5m")
	runSpec.Target = spec.Target{Endpoint: "http://vllm-host.invalid:8080/v1", Tls: spec.TlsDisabled{}}
	runSpec.Target.Metrics = &spec.TargetMetrics{Job: prometheus.ExternalJob, Namespace: "servtuner-external", Pod: "vllm-lan"}
	if errs := Check(runSpec).Errors; len(errs) != 0 {
		t.Fatalf("metrics alone is valid: %q", errs)
	}
	runSpec.Target.Workload = &spec.WorkloadSelector{Namespace: "llm", Selector: spec.WorkloadSelectorSelector{"app": "vllm"}}
	errs := Check(runSpec).Errors
	if len(errs) != 1 || !strings.Contains(errs[0], "target.metrics") {
		t.Errorf("metrics with a workload is one error naming target.metrics: %q", errs)
	}
}
