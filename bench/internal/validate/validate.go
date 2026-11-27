// Package validate holds the RunSpec rules that the JSON Schema cannot express
// (cross-field rules), and the checks that need a sense of time. Schema
// validity itself is spec.ParseRunSpec's job.
package validate

import (
	"errors"
	"fmt"
	"strings"

	"github.com/servtuner/servtuner/bench/internal/prometheus"
	"github.com/servtuner/servtuner/bench/internal/spec"
	"github.com/servtuner/servtuner/bench/internal/tlsconfig"
)

// ErrInvalidSpec marks every error caused by a bad RunSpec, whether from the
// schema, the transport rules or the file itself. The CLI maps it to exit
// code 2.
var ErrInvalidSpec = errors.New("invalid spec")

// Report is the outcome of checking one parsed RunSpec. Errors make the spec
// invalid; warnings do not (servtuner validate warns).
type Report struct {
	Errors   []string
	Warnings []string
	// StageWarnings is Warnings in structured form, in the same order and with
	// the same Message, ready for phases.json (the
	// bundle records the warning). The pipeline writes them; this package only
	// returns them.
	StageWarnings []spec.StageLengthWarning
	// ScrapeWarnings is the missing-scrape-role entries of Warnings in
	// structured form, in the same order and with the same Message, ready for
	// phases.json's scrape_warnings. Warnings lists stage-length warnings
	// first, then these.
	ScrapeWarnings []spec.ScrapeWarning
}

// Err returns nil when there are no errors, otherwise one error that lists all
// of them and wraps ErrInvalidSpec.
func (r Report) Err() error {
	return errorFromProblems(r.Errors)
}

func errorFromProblems(problems []string) error {
	if len(problems) == 0 {
		return nil
	}
	return fmt.Errorf("%w: %d problem(s):\n  - %s", ErrInvalidSpec, len(problems), strings.Join(problems, "\n  - "))
}

// WrapParseError marks a schema or syntax error from loading a spec as an
// invalid-spec error.
func WrapParseError(err error) error {
	return fmt.Errorf("%w: %w", ErrInvalidSpec, err)
}

// Check applies the cross-field rules to a spec that already passed the schema,
// with the scrape interval assumed because no Prometheus was asked.
func Check(runSpec spec.RunSpec) Report {
	return CheckWithResolution(runSpec, prometheus.AssumedResolution())
}

// CheckWithResolution is Check with the effective scrape interval read from
// Prometheus by prometheus.ResolveScrapeInterval. It also warns when a relevant
// job is missing from Prometheus: the data of that role may be scraped more
// slowly than reported, or not at all. Each is also returned structured in
// Report.ScrapeWarnings, for phases.json.
func CheckWithResolution(runSpec spec.RunSpec, resolution prometheus.Resolution) Report {
	scrapeInterval := resolution.ScrapeInterval
	var report Report
	if runSpec.Workload.Preset != nil {
		report.Errors = append(report.Errors, "workload.preset "+string(*runSpec.Workload.Preset)+" is not expanded: load the RunSpec with spec.LoadRunSpec")
	}
	report.Errors = append(report.Errors, transportProblems("target", runSpec.Target.Tls, runSpec.Target.Endpoint)...)
	for _, upstream := range runSpec.Target.Upstreams {
		if upstream.Endpoint != nil {
			report.Errors = append(report.Errors, transportProblems("upstream "+upstream.Name, upstream.Tls, *upstream.Endpoint)...)
		}
	}
	if runSpec.Prometheus != nil {
		report.Errors = append(report.Errors, transportProblems("prometheus", runSpec.Prometheus.Tls, runSpec.Prometheus.Url)...)
	}
	if runSpec.Target.Workload != nil && runSpec.Target.Metrics != nil {
		report.Errors = append(report.Errors, "target.metrics is for targets outside Kubernetes and cannot be combined with target.workload: "+
			"a Kubernetes target's series are found through its snapshot")
	}
	stageWarnings, stageErrors := stageLengthProblems(runSpec, scrapeInterval)
	for _, warning := range stageWarnings {
		report.Warnings = append(report.Warnings, warning.Message)
	}
	report.StageWarnings = stageWarnings
	report.ScrapeWarnings = missingRoleWarnings(runSpec, resolution.MissingRoles)
	for _, warning := range report.ScrapeWarnings {
		report.Warnings = append(report.Warnings, warning.Message)
	}
	report.Errors = append(report.Errors, stageErrors...)
	report.Errors = append(report.Errors, conversationProblems(runSpec)...)
	report.Errors = append(report.Errors, sharedPrefixProblems(runSpec)...)
	report.Errors = append(report.Errors, shardProblems(runSpec)...)
	return report
}

// transportProblems checks one hop's tls block against its URL scheme. TLS
// comes from DecodeTLS and is never inferred from the URL.
func transportProblems(hop string, tls spec.Tls, endpoint string) []string {
	decoded, err := spec.DecodeTLS(tls)
	if err != nil {
		return []string{fmt.Sprintf("%s.tls: %v", hop, err)}
	}
	if err := tlsconfig.CheckScheme(decoded.Enabled, endpoint); err != nil {
		return []string{fmt.Sprintf("%s: %v", hop, err)}
	}
	return nil
}

// shardProblems: sharding splits loadgen's pacer and request sequence, so it
// needs loadgen; a conversation must stay on one shard, which needs
// conversation.concurrent to be a multiple of the shard count.
func shardProblems(runSpec spec.RunSpec) []string {
	shards := runSpec.Engine.ShardCount()
	if shards == 1 {
		return nil
	}
	var problems []string
	if runSpec.Engine.Type != spec.EngineTypeLoadgen {
		problems = append(problems, fmt.Sprintf("engine.shards %d: only the loadgen engine can be sharded, not %s", shards, runSpec.Engine.Type))
	}
	if conversation := runSpec.Workload.Conversation; conversation != nil {
		if concurrent := conversation.Concurrent; concurrent%shards != 0 {
			problems = append(problems, fmt.Sprintf("engine.shards %d: workload.conversation.concurrent %d must be a multiple of it, so each conversation stays on one shard", shards, conversation.Concurrent))
		}
	}
	return problems
}

// sharedPrefixProblems: input_tokens is the whole prompt, so the shared prefix
// must leave room for a per-request part; a conversation already repeats its
// own history, and embeddings have no prefix cache to exercise.
func sharedPrefixProblems(runSpec spec.RunSpec) []string {
	dataset := runSpec.Workload.Dataset
	if dataset == nil || dataset.SharedPrefix == nil {
		return nil
	}
	var problems []string
	if tokens := dataset.SharedPrefix.Tokens; dataset.InputTokens.Mean <= float64(tokens) {
		problems = append(problems, fmt.Sprintf("workload.dataset.shared_prefix.tokens %d must be below input_tokens.mean %v: input_tokens is the whole prompt, prefix included", tokens, dataset.InputTokens.Mean))
	}
	if runSpec.Workload.Conversation != nil {
		problems = append(problems, "workload.dataset.shared_prefix cannot be combined with workload.conversation")
	}
	if runSpec.Workload.Api == spec.WorkloadApiEmbeddings {
		problems = append(problems, "workload.dataset.shared_prefix needs a generating api (chat or completion), not embeddings")
	}
	return problems
}

// conversationProblems: multi-turn workloads are built by bench's own
// generator for the chat API, so they need loadgen and chat.
func conversationProblems(runSpec spec.RunSpec) []string {
	if runSpec.Workload.Conversation == nil {
		return nil
	}
	var problems []string
	if api := runSpec.Workload.Api; api != "" && api != spec.WorkloadApiChat {
		problems = append(problems, fmt.Sprintf("workload.conversation needs workload.api chat, not %s", api))
	}
	if runSpec.Engine.Type != spec.EngineTypeLoadgen {
		problems = append(problems, fmt.Sprintf("workload.conversation is built by the loadgen engine; engine %s builds its own requests", runSpec.Engine.Type))
	}
	return problems
}
