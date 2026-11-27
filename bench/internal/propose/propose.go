package propose

import (
	"fmt"
	"math"
	"regexp"
	"sort"
	"strings"

	"github.com/servtuner/servtuner/bench/internal/analyze"
	"github.com/servtuner/servtuner/bench/internal/analyze/compare"
	"github.com/servtuner/servtuner/bench/internal/spec"
)

// rpsRounding scales ladder rates to hundredths of a request per second, so
// the output has no float noise.
const rpsRounding = 100

func roundRPS(rps float64) float64 { return math.Round(rps*rpsRounding) / rpsRounding }

// Propose returns the next configurations to boot or run, or says the search
// is done. It is deterministic in its inputs.
func Propose(space SearchSpace, boots BootResults, evidence []Evidence) (Proposal, error) {
	s, err := newState(space, boots, evidence)
	if err != nil {
		return Proposal{}, err
	}
	p := Proposal{Objective: space.Objective, NoiseFloor: space.NoiseFloor, Tiers: []TierStatus{}, Proposals: []Proposed{}, Notes: []string{}}
	if s.leader != nil {
		p.Leader = &Leader{ConfigId: s.leader.id, Knobs: spec.TuningKnobSet(s.leader.knobs), SloMaxRps: roundRPS(s.leader.sloMaxRPS),
			ObjectiveValue: s.leader.score, RunIds: s.leader.runIDs()}
	}
	reasons := map[Tier]string{}
	p.Notes = sortedNotes(s.notes)

	probes, survivors := s.feasibilityStep()
	if len(s.feasibilityNames()) == 0 {
		reasons[TierFeasibility] = "no feasibility knobs in the search space"
	} else {
		if len(probes) > 0 {
			return s.propose(p, PhaseFeasibility, TierFeasibility, reasons, probes), nil
		}
		if len(survivors) == 0 {
			return s.finish(p, reasons, Done{Outcome: OutcomeNoFeasible,
				Reason: "no combination of the feasibility knobs boots and holds the preset's concurrency"}), nil
		}
		reasons[TierFeasibility] = fmt.Sprintf("%d of %d combinations boot and hold the preset's concurrency", len(survivors), len(s.feasibilityCombinations()))
	}

	if s.leader == nil {
		return s.proposeBaseline(p, reasons, survivors)
	}

	steps := []struct {
		tier Tier
		run  func() ([]Proposed, string, []string)
	}{{TierStructural, s.structuralStep}, {TierScheduler, s.schedulerStep}, {TierFineTuning, s.fineTuningStep}}
	var notes []string
	for _, step := range steps {
		proposals, converged, stepNotes := step.run()
		notes = append(notes, stepNotes...)
		if len(proposals) > 0 {
			ladder := s.ladder(LadderScreen)
			for i := range proposals {
				proposals[i].Ladder = &ladder
			}
			p.Notes = sortedNotes(append(notes, s.notes...))
			return s.propose(p, PhaseScreen, step.tier, reasons, proposals), nil
		}
		reasons[step.tier] = converged
	}
	p.Notes = sortedNotes(append(notes, s.notes...))
	return s.confirm(p, reasons)
}

// propose finishes a proposal that has configurations to boot or run.
func (s *state) propose(p Proposal, phase Phase, active Tier, reasons map[Tier]string, proposals []Proposed) Proposal {
	for i := range proposals {
		proposals[i].Labels = spec.ProposalProposedLabels{LabelConfig: proposals[i].ConfigId}
		if proposals[i].Ladder != nil {
			proposals[i].Labels[LabelLadder] = string(proposals[i].Ladder.Kind)
		}
	}
	p.Status, p.Phase, p.Proposals = StatusPropose, phase, proposals
	p.Tiers = tierStatuses(active, reasons)
	return p
}

func (s *state) finish(p Proposal, reasons map[Tier]string, done Done) Proposal {
	p.Status, p.Phase, p.Done = StatusDone, PhaseDone, &done
	p.Tiers = tierStatuses("", reasons)
	return p
}

// tierStatuses lists the tiers in order: those before the active one are
// converged, the active one is active, the rest pending. With no active tier,
// the tiers with a reason are converged and the rest pending.
func tierStatuses(active Tier, reasons map[Tier]string) []TierStatus {
	statuses := make([]TierStatus, 0, len(tiersInOrder))
	for _, tier := range tiersInOrder {
		status := TierStatus{Tier: tier, State: TierPending}
		switch reason, ended := reasons[tier]; {
		case tier == active:
			status.State = TierActive
		case ended:
			status.State, status.Reason = TierConverged, ptr(reason)
		}
		statuses = append(statuses, status)
	}
	return statuses
}

func (s *state) proposeBaseline(p Proposal, reasons map[Tier]string, survivors []Knobs) (Proposal, error) {
	knobs := s.baseline(survivors)
	if s.tried(knobs) {
		return s.finish(p, reasons, Done{Outcome: OutcomeNoUsableRun,
			Reason: fmt.Sprintf("every run of the baseline %s was excluded, by the quality gate or because a knob was contradicted (see notes): fix the target or the knob and start again", knobs.id())}), nil
	}
	if !s.viable(knobs) {
		return s.finish(p, reasons, Done{Outcome: OutcomeNoFeasible, Reason: "the baseline configuration cannot boot or hold the preset's concurrency"}), nil
	}
	ladder := s.ladder(LadderProbe)
	baseline := Proposed{ConfigId: knobs.id(), Action: ActionRun, Knobs: spec.TuningKnobSet(knobs), Ladder: &ladder, Repeats: ptr(1), Rationale: Rationale{
		Kind: KindBaseline, BasedOnRuns: []string{},
		Text: "the first configuration: the base knobs with each tuned knob at its default; a probe ladder finds where it saturates",
	}}
	return s.propose(p, PhaseBaseline, "", reasons, []Proposed{baseline}), nil
}

// ladder places the next run's ladder. Without a leader that met its SLOs
// there is nothing to centre on, so the probe ladder runs; later ladders are
// the leader's SLO-max times the ladder's factors, so the stages straddle the
// knee. A leader that never failed within its ladder has its SLO-max only as a
// lower bound, so the centre moves up by the top factor.
func (s *state) ladder(kind LadderKind) Ladder {
	if s.leader == nil || !s.leader.hasSLOMax {
		return Ladder{Kind: LadderProbe, Rps: s.space.Ladder.ProbeRPS}
	}
	factors := s.space.Ladder.ScreenFactors
	if kind == LadderFull {
		factors = s.space.Ladder.FullFactors
	}
	centre := s.leader.sloMaxRPS
	if s.leader.lowerBound {
		centre *= factors[len(factors)-1]
	}
	centre = roundRPS(centre)
	rps := make([]float64, len(factors))
	for i, factor := range factors {
		rps[i] = roundRPS(centre * factor)
	}
	sort.Float64s(rps)
	return Ladder{Kind: kind, Rps: rps, CentreRps: &centre}
}

// confirm asks for the full ladder with repeats on the best few
// configurations, then judges them with the comparison. The best few are
// ranked on the screening runs alone, so the choice does not move as the
// confirmation runs arrive.
func (s *state) confirm(p Proposal, reasons map[Tier]string) (Proposal, error) {
	var contenders []*configuration
	for _, config := range s.screened {
		if config.hasSLOMax && !config.excluded {
			contenders = append(contenders, config)
		}
	}
	var notes []string
	confirmable := contenders[:0:0]
	for _, config := range contenders {
		if len(config.contradictedFull) > 0 {
			notes = append(notes, fmt.Sprintf("configuration %s is dropped from confirmation: its flags did not apply as declared (run %s)", config.id, config.contradictedFull[0]))
			continue
		}
		confirmable = append(confirmable, config)
	}
	p.Notes = sortedNotes(append(p.Notes, notes...))
	if len(contenders) > 0 && len(confirmable) == 0 {
		return s.finish(p, reasons, Done{Outcome: OutcomeNoUsableRun,
			Reason: "no configuration could be confirmed: the target contradicted the declared knobs of every confirmation run (see notes)"}), nil
	}
	contenders = confirmable
	if len(contenders) == 0 {
		reason := "no configuration met the SLOs at any stage of its ladder: relax the SLOs or check the target"
		if s.unjudged {
			reason = "no SLO could be judged: no stage had the requests its percentile needs " +
				"(e.g. 200 for a p95): raise the ladder's rates or lengthen the stages"
		}
		return s.finish(p, reasons, Done{Outcome: OutcomeNoSLOMet, Reason: reason}), nil
	}
	sort.SliceStable(contenders, func(i, j int) bool { return contenders[i].score > contenders[j].score })
	contenders = contenders[:min(s.space.Confirm.Top, len(contenders))]

	ladder := s.ladder(LadderFull)
	var proposals []Proposed
	for rank, config := range contenders {
		missing := s.space.Confirm.Repeats - len(config.full)
		if missing <= 0 {
			continue
		}
		proposals = append(proposals, Proposed{ConfigId: config.id, Action: ActionRun, Knobs: spec.TuningKnobSet(config.knobs), Ladder: &ladder, Repeats: ptr(missing),
			Rationale: Rationale{Kind: KindConfirm, BasedOnRuns: screeningIDs(config),
				Text: fmt.Sprintf("confirm: screening rank %d of %d; %d more full-ladder runs give the comparison %d repeats", rank+1, len(contenders), missing, s.space.Confirm.Repeats)}})
	}
	if len(proposals) > 0 {
		return s.propose(p, PhaseConfirm, "", reasons, proposals), nil
	}
	return s.judge(p, reasons, contenders)
}

// judge compares the confirmed configurations on their full-ladder runs.
func (s *state) judge(p Proposal, reasons map[Tier]string, contenders []*configuration) (Proposal, error) {
	var runs []compare.Run
	configOf := map[string]*configuration{}
	for _, config := range contenders {
		runs = append(runs, config.full...)
		for _, run := range config.full {
			configOf[run.ID] = config
		}
	}
	result, err := compare.Compare(runs, s.space.Objective, analyze.AnalyzerVersion)
	if err != nil {
		return Proposal{}, err
	}
	byCompareID := map[string]*configuration{}
	for _, entry := range result.Configurations {
		byCompareID[entry.Id] = configOf[entry.RunIds[0]]
	}
	if len(result.Rankings) == 0 || result.Rankings[0].Objective != s.space.Objective {
		return s.finish(p, reasons, Done{Outcome: OutcomeNoSignificantWinner,
			Reason: "the confirmation runs gave no ranking on " + string(s.space.Objective) + ": " + unrankedReason(result.Comparability.Issues, result.ExcludedRuns)}), nil
	}
	ranking := result.Rankings[0]
	best := byCompareID[ranking.Order[0].ConfigurationId]
	winner := &Winner{ConfigId: best.id, Knobs: spec.TuningKnobSet(best.knobs), ObjectiveValue: ranking.Order[0].Value,
		IsSignificant: ranking.IsSignificant, Confidence: spec.ProposalWinnerConfidence(ranking.Confidence), RunIds: runIDsOf(best.full)}
	outcome := OutcomeNoSignificantWinner
	if ranking.IsSignificant {
		outcome = OutcomeWinner
	}
	return s.finish(p, reasons, Done{Outcome: outcome, Reason: inConfigIDs(ranking.Reason, byCompareID), Winner: winner}), nil
}

// unrankedReason says why the comparison named no ranking: the comparability
// issues it found, or the runs it excluded, or else that an objective was
// unavailable for some configuration.
func unrankedReason(issues []spec.ComparisonIssue, excluded []spec.ExcludedRun) string {
	var reasons []string
	for _, issue := range issues {
		reasons = append(reasons, issue.Message)
	}
	for _, run := range excluded {
		reasons = append(reasons, fmt.Sprintf("run %s: %s", run.RunId, run.Reason))
	}
	if len(reasons) == 0 {
		return "the objective is unavailable for some configuration"
	}
	return strings.Join(reasons, "; ")
}

var compareID = regexp.MustCompile(`\bc\d+\b`)

// inConfigIDs rewrites the comparison's configuration ids (c1, c2) in a
// sentence as the config_ids the proposal uses.
func inConfigIDs(text string, byCompareID map[string]*configuration) string {
	return compareID.ReplaceAllStringFunc(text, func(id string) string {
		if config, ok := byCompareID[id]; ok {
			return config.id
		}
		return id
	})
}

func runIDsOf(runs []compare.Run) []string {
	ids := make([]string, len(runs))
	for i, run := range runs {
		ids[i] = run.ID
	}
	return ids
}
