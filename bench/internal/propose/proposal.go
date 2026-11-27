package propose

import "github.com/servtuner/servtuner/bench/internal/spec"

// The proposal is the schema's (proposal.schema.json): these are the generated
// types and values under the names the search uses.
type (
	Proposal   = spec.Proposal
	Proposed   = spec.ProposalProposed
	Ladder     = spec.ProposalLadder
	Rationale  = spec.ProposalRationale
	GridCell   = spec.ProposalGridCell
	Leader     = spec.ProposalLeader
	Done       = spec.ProposalDone
	Winner     = spec.ProposalWinner
	TierStatus = spec.ProposalTierStatus

	// Tier is a stage of the search, run in tiersInOrder.
	Tier          = spec.TuningTier
	LadderKind    = spec.ProposalLadderKind
	Position      = spec.ProposalPosition
	Phase         = spec.ProposalPhase
	RationaleKind = spec.ProposalRationaleKind
	Outcome       = spec.ProposalDoneOutcome
	TierState     = spec.ProposalTierStatusState
)

const (
	TierFeasibility = spec.TuningTierFeasibility
	TierStructural  = spec.TuningTierStructural
	TierScheduler   = spec.TuningTierScheduler
	TierFineTuning  = spec.TuningTierFineTuning

	StatusPropose = spec.ProposalStatusPropose
	StatusDone    = spec.ProposalStatusDone

	PhaseFeasibility = spec.ProposalPhaseFeasibility
	PhaseBaseline    = spec.ProposalPhaseBaseline
	PhaseScreen      = spec.ProposalPhaseScreen
	PhaseConfirm     = spec.ProposalPhaseConfirm
	PhaseDone        = spec.ProposalPhaseDone

	// ActionBootProbe deploys the configuration with no load and reports its
	// boot result (a BootResult), including c_kv when it boots. ActionRun
	// benchmarks it with the proposal's ladder.
	ActionBootProbe = spec.ProposalProposedActionBootProbe
	ActionRun       = spec.ProposalProposedActionRun

	KindBootProbe    = spec.ProposalRationaleKindBootProbe
	KindBaseline     = spec.ProposalRationaleKindBaseline
	KindStructural   = spec.ProposalRationaleKindStructuralAb
	KindAdvisor      = spec.ProposalRationaleKindAdvisor
	KindGrid         = spec.ProposalRationaleKindGrid
	KindSchedulerAB  = spec.ProposalRationaleKindSchedulerAb
	KindFineTuningAB = spec.ProposalRationaleKindFineTuningAb
	KindConfirm      = spec.ProposalRationaleKindConfirm

	LadderProbe  = spec.ProposalLadderKindProbe
	LadderScreen = spec.ProposalLadderKindScreen
	LadderFull   = spec.ProposalLadderKindFull

	PositionDown   = spec.ProposalPositionDown
	PositionCentre = spec.ProposalPositionCentre
	PositionUp     = spec.ProposalPositionUp

	TierPending   = spec.ProposalTierStatusStatePending
	TierActive    = spec.ProposalTierStatusStateActive
	TierConverged = spec.ProposalTierStatusStateConverged

	OutcomeWinner              = spec.ProposalDoneOutcomeWinner
	OutcomeNoSignificantWinner = spec.ProposalDoneOutcomeNoSignificantWinner
	OutcomeNoFeasible          = spec.ProposalDoneOutcomeNoFeasibleConfiguration
	OutcomeNoSLOMet            = spec.ProposalDoneOutcomeNoSloMet
	OutcomeNoUsableRun         = spec.ProposalDoneOutcomeNoUsableRun
)

// tiersInOrder is the order the search visits the tiers.
var tiersInOrder = []Tier{TierFeasibility, TierStructural, TierScheduler, TierFineTuning}

func ptr[T any](value T) *T { return &value }
