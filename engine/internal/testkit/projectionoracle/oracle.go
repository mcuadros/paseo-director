// SPDX-License-Identifier: Apache-2.0

package projectionoracle

// SchemaVersion identifies this test oracle's closed fact and result contract.
const SchemaVersion = 1

// FactStatus says whether a named durable/external fact is usable now.
type FactStatus string

const (
	FactMissing       FactStatus = "missing"
	FactCurrent       FactStatus = "current"
	FactStale         FactStatus = "stale"
	FactContradictory FactStatus = "contradictory"
)

// State is one of the six Board/List lanes. Completed Tasks have no lane and
// are represented by Projection.DoneMember instead.
type State string

const (
	StateNeedsYou   State = "needs_you"
	StateQueued     State = "queued"
	StateBuilding   State = "building"
	StateValidating State = "validating"
	StateInReview   State = "in_review"
	StateReady      State = "ready"
)

// ExplanationCode gives every projection a stable primary explanation.
type ExplanationCode string

const (
	ExplanationNeedsYou   ExplanationCode = "projected_needs_you"
	ExplanationQueued     ExplanationCode = "projected_queued"
	ExplanationBuilding   ExplanationCode = "projected_building"
	ExplanationValidating ExplanationCode = "projected_validating"
	ExplanationInReview   ExplanationCode = "projected_in_review"
	ExplanationReady      ExplanationCode = "projected_ready"
	ExplanationDone       ExplanationCode = "projected_done"
)

// BlockerCode explains a wait or fail-closed projection. Project always emits
// these codes in the order returned by BlockerCodeOrder.
type BlockerCode string

const (
	BlockerTaskFactContradictory      BlockerCode = "task_fact_contradictory"
	BlockerEligibilityMissing         BlockerCode = "eligibility_missing"
	BlockerEligibilityStale           BlockerCode = "eligibility_stale"
	BlockerEligibilityContradictory   BlockerCode = "eligibility_contradictory"
	BlockerDependencyWait             BlockerCode = "dependency_wait"
	BlockerPolicyWait                 BlockerCode = "launch_policy_wait"
	BlockerCapacityWait               BlockerCode = "capacity_wait"
	BlockerExternalWait               BlockerCode = "external_wait"
	BlockerEscalationUnreconciled     BlockerCode = "escalation_unreconciled"
	BlockerRunStale                   BlockerCode = "run_stale"
	BlockerRunContradictory           BlockerCode = "run_contradictory"
	BlockerCandidateStale             BlockerCode = "candidate_stale"
	BlockerCandidateContradictory     BlockerCode = "candidate_contradictory"
	BlockerClaimMissing               BlockerCode = "claim_missing"
	BlockerClaimStale                 BlockerCode = "claim_stale"
	BlockerClaimContradictory         BlockerCode = "claim_contradictory"
	BlockerValidationMissing          BlockerCode = "validation_missing"
	BlockerValidationStale            BlockerCode = "validation_stale"
	BlockerValidationContradictory    BlockerCode = "validation_contradictory"
	BlockerValidationPending          BlockerCode = "validation_pending"
	BlockerValidationFailed           BlockerCode = "validation_failed"
	BlockerBaseRevalidationRequired   BlockerCode = "base_revalidation_required"
	BlockerValidationExternalWait     BlockerCode = "validation_external_wait"
	BlockerValidationAmbiguous        BlockerCode = "validation_ambiguous"
	BlockerReviewMissing              BlockerCode = "review_missing"
	BlockerReviewStale                BlockerCode = "review_stale"
	BlockerReviewContradictory        BlockerCode = "review_contradictory"
	BlockerReviewPending              BlockerCode = "review_pending"
	BlockerReviewChangesRequested     BlockerCode = "review_changes_requested"
	BlockerReviewDecisionUnreconciled BlockerCode = "review_decision_unreconciled"
	BlockerFeedbackMissing            BlockerCode = "feedback_missing"
	BlockerFeedbackStale              BlockerCode = "feedback_stale"
	BlockerFeedbackContradictory      BlockerCode = "feedback_contradictory"
	BlockerFeedbackActionable         BlockerCode = "feedback_actionable"
	BlockerDeliveryMissing            BlockerCode = "delivery_missing"
	BlockerDeliveryStale              BlockerCode = "delivery_stale"
	BlockerDeliveryContradictory      BlockerCode = "delivery_contradictory"
	BlockerDeliveryPending            BlockerCode = "delivery_pending"
	BlockerIntegrationPending         BlockerCode = "integration_pending"
	BlockerCleanupMissing             BlockerCode = "cleanup_missing"
	BlockerCleanupStale               BlockerCode = "cleanup_stale"
	BlockerCleanupContradictory       BlockerCode = "cleanup_contradictory"
	BlockerCleanupPending             BlockerCode = "cleanup_pending"
	BlockerHumanInputMissing          BlockerCode = "human_input_missing"
	BlockerHumanInputStale            BlockerCode = "human_input_stale"
	BlockerHumanInputContradictory    BlockerCode = "human_input_contradictory"
	BlockerTerminalStale              BlockerCode = "terminal_stale"
	BlockerTerminalContradictory      BlockerCode = "terminal_contradictory"
	BlockerTerminalPending            BlockerCode = "terminal_pending"
	BlockerTerminalDoneUnproved       BlockerCode = "terminal_done_unproved"
)

var blockerCodeOrder = [...]BlockerCode{
	BlockerTaskFactContradictory,
	BlockerEligibilityMissing,
	BlockerEligibilityStale,
	BlockerEligibilityContradictory,
	BlockerDependencyWait,
	BlockerPolicyWait,
	BlockerCapacityWait,
	BlockerExternalWait,
	BlockerEscalationUnreconciled,
	BlockerRunStale,
	BlockerRunContradictory,
	BlockerCandidateStale,
	BlockerCandidateContradictory,
	BlockerClaimMissing,
	BlockerClaimStale,
	BlockerClaimContradictory,
	BlockerValidationMissing,
	BlockerValidationStale,
	BlockerValidationContradictory,
	BlockerValidationPending,
	BlockerValidationFailed,
	BlockerBaseRevalidationRequired,
	BlockerValidationExternalWait,
	BlockerValidationAmbiguous,
	BlockerReviewMissing,
	BlockerReviewStale,
	BlockerReviewContradictory,
	BlockerReviewPending,
	BlockerReviewChangesRequested,
	BlockerReviewDecisionUnreconciled,
	BlockerFeedbackMissing,
	BlockerFeedbackStale,
	BlockerFeedbackContradictory,
	BlockerFeedbackActionable,
	BlockerDeliveryMissing,
	BlockerDeliveryStale,
	BlockerDeliveryContradictory,
	BlockerDeliveryPending,
	BlockerIntegrationPending,
	BlockerCleanupMissing,
	BlockerCleanupStale,
	BlockerCleanupContradictory,
	BlockerCleanupPending,
	BlockerHumanInputMissing,
	BlockerHumanInputStale,
	BlockerHumanInputContradictory,
	BlockerTerminalStale,
	BlockerTerminalContradictory,
	BlockerTerminalPending,
	BlockerTerminalDoneUnproved,
}

// BlockerCodeOrder returns the immutable canonical blocker order.
func BlockerCodeOrder() []BlockerCode {
	return append([]BlockerCode(nil), blockerCodeOrder[:]...)
}

// AttentionCode is one closed actionable human-intervention category.
type AttentionCode string

const (
	AttentionPermissionRequired         AttentionCode = "permission_required"
	AttentionCorrectionBudgetExhausted  AttentionCode = "correction_budget_exhausted"
	AttentionReplacementBudgetExhausted AttentionCode = "replacement_budget_exhausted"
	AttentionGitStateIrreconcilable     AttentionCode = "git_state_irreconcilable"
	AttentionConfigurationRequired      AttentionCode = "configuration_required"
	AttentionCredentialRequired         AttentionCode = "credential_required"
	AttentionPolicyOverrideRequired     AttentionCode = "policy_override_required"
	AttentionSoftBudgetAcknowledgment   AttentionCode = "soft_budget_acknowledgment_required"
	AttentionHardBudgetExhausted        AttentionCode = "hard_budget_exhausted"
	AttentionRecoveryAmbiguous          AttentionCode = "recovery_ambiguous"
	AttentionReviewDecisionRequired     AttentionCode = "review_decision_required"
	AttentionFeedbackDecisionRequired   AttentionCode = "feedback_decision_required"
)

var attentionCodeOrder = [...]AttentionCode{
	AttentionPermissionRequired,
	AttentionCorrectionBudgetExhausted,
	AttentionReplacementBudgetExhausted,
	AttentionGitStateIrreconcilable,
	AttentionConfigurationRequired,
	AttentionCredentialRequired,
	AttentionPolicyOverrideRequired,
	AttentionSoftBudgetAcknowledgment,
	AttentionHardBudgetExhausted,
	AttentionRecoveryAmbiguous,
	AttentionReviewDecisionRequired,
	AttentionFeedbackDecisionRequired,
}

// AttentionCodeOrder returns the immutable canonical attention order.
func AttentionCodeOrder() []AttentionCode {
	return append([]AttentionCode(nil), attentionCodeOrder[:]...)
}

// EligibilityDecision is the closed result of versioned eligibility facts.
type EligibilityDecision string

const (
	EligibilityEligible       EligibilityDecision = "eligible"
	EligibilityDependencyWait EligibilityDecision = "dependency_wait"
	EligibilityPolicyWait     EligibilityDecision = "policy_wait"
	EligibilityCapacityWait   EligibilityDecision = "capacity_wait"
	EligibilityExternalWait   EligibilityDecision = "external_wait"
	EligibilityEscalate       EligibilityDecision = "escalate"
)

type EligibilityFact struct {
	Status      FactStatus
	TaskVersion uint64
	Decision    EligibilityDecision
}

type RunFact struct {
	Status      FactStatus
	TaskVersion uint64
	ID          string
	Active      bool
}

type CandidateFact struct {
	Status      FactStatus
	TaskVersion uint64
	ID          string
	RunID       string
}

type ValidationOutcome string

const (
	ValidationPending ValidationOutcome = "pending"
	ValidationRunning ValidationOutcome = "running"
	ValidationPassed  ValidationOutcome = "passed"
	ValidationFailed  ValidationOutcome = "failed"
)

type ValidationFact struct {
	Status      FactStatus
	CandidateID string
	Outcome     ValidationOutcome
	Reason      string
}

type ReviewOutcome string

const (
	ReviewPending          ReviewOutcome = "pending"
	ReviewRunning          ReviewOutcome = "running"
	ReviewApproved         ReviewOutcome = "approved"
	ReviewChangesRequested ReviewOutcome = "changes_requested"
	ReviewNeedsHuman       ReviewOutcome = "needs_human_decision"
)

type ReviewFact struct {
	Status      FactStatus
	CandidateID string
	Outcome     ReviewOutcome
}

type FeedbackState string

const (
	FeedbackNone       FeedbackState = "none"
	FeedbackActionable FeedbackState = "actionable"
	FeedbackResolved   FeedbackState = "resolved"
)

type FeedbackFact struct {
	Status      FactStatus
	CandidateID string
	State       FeedbackState
}

type DeliveryState string

const (
	DeliveryPendingPublication DeliveryState = "pending_publication"
	DeliveryPublished          DeliveryState = "published"
	DeliveryIntegrated         DeliveryState = "integrated"
)

type DeliveryFact struct {
	Status      FactStatus
	CandidateID string
	State       DeliveryState
}

type CleanupState string

const (
	CleanupPending  CleanupState = "pending"
	CleanupComplete CleanupState = "complete"
)

type CleanupFact struct {
	Status FactStatus
	RunID  string
	State  CleanupState
}

type HumanInputState string

const (
	HumanInputNone     HumanInputState = "none"
	HumanInputPending  HumanInputState = "pending"
	HumanInputResolved HumanInputState = "resolved"
)

type HumanInputFact struct {
	Status        FactStatus
	TaskVersion   uint64
	State         HumanInputState
	Code          AttentionCode
	WakeCondition string
}

type TerminalState string

const (
	TerminalOpen TerminalState = "open"
	TerminalDone TerminalState = "done"
)

type TerminalFact struct {
	Status      FactStatus
	TaskVersion uint64
	State       TerminalState
}

// AgentOutcome is the closed model-claim vocabulary. Required claims are
// checked for current bindings, but no claim is projection evidence.
type AgentOutcome string

const (
	OutcomeCompleted           AgentOutcome = "completed"
	OutcomeNeedsValidation     AgentOutcome = "needs_validation"
	OutcomeNeedsReview         AgentOutcome = "needs_review"
	OutcomeNeedsHumanDecision  AgentOutcome = "needs_human_decision"
	OutcomeBlockedByDependency AgentOutcome = "blocked_by_dependency"
	OutcomeBlockedByAccess     AgentOutcome = "blocked_by_access"
	OutcomeBudgetExhausted     AgentOutcome = "budget_exhausted"
)

type ClaimFact struct {
	Required    bool
	Status      FactStatus
	TaskVersion uint64
	RunID       string
	CandidateID string
	Outcome     AgentOutcome
}

// Facts is the oracle's complete independent input. It intentionally contains
// no editable Board/List state or card position.
type Facts struct {
	TaskID      string
	TaskVersion uint64
	Eligibility EligibilityFact
	Run         RunFact
	Candidate   CandidateFact
	Validation  ValidationFact
	Review      ReviewFact
	Feedback    FeedbackFact
	Delivery    DeliveryFact
	Cleanup     CleanupFact
	HumanInput  HumanInputFact
	Terminal    TerminalFact
	Claims      []ClaimFact
}

// Projection is the expected black-box result for one Task. BoardMember and
// DoneMember are mutually exclusive.
type Projection struct {
	State       State
	BoardMember bool
	DoneMember  bool
	Explanation ExplanationCode
	Blockers    []BlockerCode
	Attention   []AttentionCode
}

type assessment struct {
	blockers  map[BlockerCode]bool
	attention map[AttentionCode]bool
}

func newAssessment() *assessment {
	return &assessment{
		blockers:  make(map[BlockerCode]bool),
		attention: make(map[AttentionCode]bool),
	}
}

func (a *assessment) block(code BlockerCode) {
	a.blockers[code] = true
}

func (a *assessment) attend(code AttentionCode) {
	a.attention[code] = true
}

func (a *assessment) projection(state State, done bool) Projection {
	result := Projection{
		State:       state,
		BoardMember: !done,
		DoneMember:  done,
		Explanation: explanationFor(state, done),
	}
	for _, code := range blockerCodeOrder {
		if a.blockers[code] {
			result.Blockers = append(result.Blockers, code)
		}
	}
	for _, code := range attentionCodeOrder {
		if a.attention[code] {
			result.Attention = append(result.Attention, code)
		}
	}
	return result
}

// Project derives one conservative expected state from closed facts. Missing,
// stale, unknown, or contradictory facts may add blockers but never fabricate
// evidence. Only a current, fully typed pending HumanInputFact yields NeedsYou.
func Project(facts Facts) Projection {
	a := newAssessment()
	// Version zero is the durable creation version. Identity, rather than the
	// numeric version, distinguishes a present Task from malformed input.
	taskValid := facts.TaskID != ""
	if !taskValid {
		a.block(BlockerTaskFactContradictory)
	}

	eligibilityCurrent, eligibility := assessEligibility(a, facts)
	runCurrent := assessRun(a, facts)
	candidateCurrent := assessCandidate(a, facts, runCurrent)
	claimFactsCurrent := assessClaims(a, facts, runCurrent, candidateCurrent)

	validationCurrent, validation := assessValidation(a, facts, candidateCurrent)
	reviewCurrent, review := assessReview(a, facts, candidateCurrent)
	feedbackCurrent, feedback := assessFeedback(a, facts, candidateCurrent)
	deliveryCurrent, delivery := assessDelivery(a, facts, candidateCurrent)
	cleanupCurrent, cleanup := assessCleanup(a, facts, runCurrent)
	humanCurrent, human, actionableHuman := assessHumanInput(a, facts)
	terminalCurrent, terminal := assessTerminal(a, facts)

	if actionableHuman {
		return a.projection(StateNeedsYou, false)
	}
	if eligibilityCurrent && eligibility == EligibilityEscalate {
		a.block(BlockerEscalationUnreconciled)
	}

	validationPassed := validationCurrent && validation == ValidationPassed
	reviewApproved := reviewCurrent && review == ReviewApproved
	feedbackClear := feedbackCurrent && (feedback == FeedbackNone || feedback == FeedbackResolved)
	humanClear := humanCurrent && (human == HumanInputNone || human == HumanInputResolved)
	deliveryIntegrated := deliveryCurrent && delivery == DeliveryIntegrated
	cleanupComplete := cleanupCurrent && cleanup == CleanupComplete
	doneProved := taskValid && runCurrent && !facts.Run.Active && candidateCurrent &&
		validationPassed && reviewApproved && feedbackClear && deliveryIntegrated &&
		cleanupComplete && humanClear && claimFactsCurrent && terminalCurrent && terminal == TerminalDone
	if doneProved {
		return a.projection("", true)
	}
	if terminalCurrent && terminal == TerminalDone {
		a.block(BlockerTerminalDoneUnproved)
	}

	if !runCurrent {
		return a.projection(StateQueued, false)
	}
	if !candidateCurrent {
		if facts.Run.Active {
			return a.projection(StateBuilding, false)
		}
		return a.projection(StateQueued, false)
	}

	validationPending := !validationCurrent || validation == ValidationPending || validation == ValidationRunning
	reviewPending := !reviewCurrent || review == ReviewPending || review == ReviewRunning || review == ReviewNeedsHuman
	if validationPending && reviewPending {
		if reviewCurrent && review == ReviewRunning && (!validationCurrent || validation != ValidationRunning) {
			return a.projection(StateInReview, false)
		}
		return a.projection(StateValidating, false)
	}
	if validationPending {
		return a.projection(StateValidating, false)
	}
	if reviewPending {
		return a.projection(StateInReview, false)
	}

	needsCorrection := validation == ValidationFailed || review == ReviewChangesRequested ||
		(feedbackCurrent && feedback == FeedbackActionable)
	if needsCorrection {
		return a.projection(StateBuilding, false)
	}
	if !taskValid || !feedbackClear || !deliveryCurrent || !humanClear || !claimFactsCurrent {
		return a.projection(StateInReview, false)
	}

	switch delivery {
	case DeliveryPendingPublication:
		a.block(BlockerDeliveryPending)
	case DeliveryPublished:
		a.block(BlockerIntegrationPending)
	case DeliveryIntegrated:
		switch {
		case !cleanupCurrent:
			a.block(BlockerCleanupMissing)
		case cleanup == CleanupPending:
			a.block(BlockerCleanupPending)
		case !terminalCurrent || terminal == TerminalOpen:
			a.block(BlockerTerminalPending)
		}
	}
	return a.projection(StateReady, false)
}

func assessEligibility(a *assessment, facts Facts) (bool, EligibilityDecision) {
	fact := facts.Eligibility
	switch fact.Status {
	case FactMissing:
		a.block(BlockerEligibilityMissing)
		return false, ""
	case FactStale:
		a.block(BlockerEligibilityStale)
		return false, ""
	case FactContradictory:
		a.block(BlockerEligibilityContradictory)
		return false, ""
	case FactCurrent:
		if fact.TaskVersion != facts.TaskVersion || !knownEligibility(fact.Decision) {
			a.block(BlockerEligibilityContradictory)
			return false, ""
		}
	default:
		a.block(BlockerEligibilityContradictory)
		return false, ""
	}
	switch fact.Decision {
	case EligibilityDependencyWait:
		a.block(BlockerDependencyWait)
	case EligibilityPolicyWait:
		a.block(BlockerPolicyWait)
	case EligibilityCapacityWait:
		a.block(BlockerCapacityWait)
	case EligibilityExternalWait:
		a.block(BlockerExternalWait)
	}
	return true, fact.Decision
}

func assessRun(a *assessment, facts Facts) bool {
	fact := facts.Run
	switch fact.Status {
	case FactMissing:
		return false
	case FactStale:
		a.block(BlockerRunStale)
		return false
	case FactContradictory:
		a.block(BlockerRunContradictory)
		return false
	case FactCurrent:
		if fact.TaskVersion != facts.TaskVersion || fact.ID == "" {
			a.block(BlockerRunContradictory)
			return false
		}
		return true
	default:
		a.block(BlockerRunContradictory)
		return false
	}
}

func assessCandidate(a *assessment, facts Facts, runCurrent bool) bool {
	fact := facts.Candidate
	switch fact.Status {
	case FactMissing:
		return false
	case FactStale:
		a.block(BlockerCandidateStale)
		return false
	case FactContradictory:
		a.block(BlockerCandidateContradictory)
		return false
	case FactCurrent:
		if !runCurrent || fact.TaskVersion != facts.TaskVersion || fact.ID == "" || fact.RunID != facts.Run.ID {
			a.block(BlockerCandidateContradictory)
			return false
		}
		return true
	default:
		a.block(BlockerCandidateContradictory)
		return false
	}
}

func assessClaims(a *assessment, facts Facts, runCurrent, candidateCurrent bool) bool {
	current := true
	required := 0
	for _, fact := range facts.Claims {
		if !fact.Required {
			continue
		}
		required++
		switch fact.Status {
		case FactMissing:
			a.block(BlockerClaimMissing)
			current = false
		case FactStale:
			a.block(BlockerClaimStale)
			current = false
		case FactContradictory:
			a.block(BlockerClaimContradictory)
			current = false
		case FactCurrent:
			candidateBearing := isCandidateBearingOutcome(fact.Outcome)
			if fact.TaskVersion != facts.TaskVersion || !knownOutcome(fact.Outcome) ||
				(candidateBearing && (!runCurrent || fact.RunID == "" || fact.RunID != facts.Run.ID ||
					!candidateCurrent || fact.CandidateID == "" || fact.CandidateID != facts.Candidate.ID)) ||
				(!candidateBearing && fact.RunID != "" && (!runCurrent || fact.RunID != facts.Run.ID)) ||
				(!candidateBearing && fact.CandidateID != "" && (!candidateCurrent || fact.CandidateID != facts.Candidate.ID)) {
				a.block(BlockerClaimContradictory)
				current = false
			}
		default:
			a.block(BlockerClaimContradictory)
			current = false
		}
	}
	if required > 1 {
		a.block(BlockerClaimContradictory)
		return false
	}
	return current
}

func assessValidation(a *assessment, facts Facts, candidateCurrent bool) (bool, ValidationOutcome) {
	fact := facts.Validation
	switch fact.Reason {
	case "", "VALIDATION_OK", "VALIDATION_PENDING", "VALIDATION_FAILED", "VALIDATION_TIMED_OUT":
	case "VALIDATION_BASE_CHANGED", "VALIDATION_BASE_RACE":
		a.block(BlockerBaseRevalidationRequired)
	case "VALIDATION_UNAVAILABLE", "VALIDATION_RATE_LIMITED", "VALIDATION_SERVER_ERROR":
		a.block(BlockerValidationExternalWait)
	case "VALIDATION_WORKFLOW_AMBIGUOUS", "VALIDATION_REQUIRED_CHECK_AMBIGUOUS", "VALIDATION_CHECK_SUITE_AMBIGUOUS",
		"VALIDATION_STATUS_AMBIGUOUS", "VALIDATION_PAGINATION_INCOMPLETE", "VALIDATION_RESPONSE_UNKNOWN",
		"VALIDATION_REPOSITORY_MISMATCH", "VALIDATION_WORKFLOW_SHA_MISMATCH", "VALIDATION_CHECK_SHA_MISMATCH",
		"VALIDATION_STATUS_SHA_MISMATCH", "VALIDATION_REDACTION_FAILURE":
		a.block(BlockerValidationAmbiguous)
	default:
		a.block(BlockerValidationContradictory)
	}
	if !candidateCurrent {
		if fact.Status != FactMissing {
			a.block(BlockerValidationContradictory)
		}
		return false, ""
	}
	switch fact.Status {
	case FactMissing:
		a.block(BlockerValidationMissing)
		return false, ""
	case FactStale:
		a.block(BlockerValidationStale)
		return false, ""
	case FactContradictory:
		a.block(BlockerValidationContradictory)
		return false, ""
	case FactCurrent:
		if fact.CandidateID != facts.Candidate.ID || !knownValidation(fact.Outcome) {
			a.block(BlockerValidationContradictory)
			return false, ""
		}
	default:
		a.block(BlockerValidationContradictory)
		return false, ""
	}
	if fact.Outcome == ValidationPending || fact.Outcome == ValidationRunning {
		a.block(BlockerValidationPending)
	}
	if fact.Outcome == ValidationFailed {
		a.block(BlockerValidationFailed)
	}
	return true, fact.Outcome
}

func assessReview(a *assessment, facts Facts, candidateCurrent bool) (bool, ReviewOutcome) {
	fact := facts.Review
	if !candidateCurrent {
		if fact.Status != FactMissing {
			a.block(BlockerReviewContradictory)
		}
		return false, ""
	}
	switch fact.Status {
	case FactMissing:
		a.block(BlockerReviewMissing)
		return false, ""
	case FactStale:
		a.block(BlockerReviewStale)
		return false, ""
	case FactContradictory:
		a.block(BlockerReviewContradictory)
		return false, ""
	case FactCurrent:
		if fact.CandidateID != facts.Candidate.ID || !knownReview(fact.Outcome) {
			a.block(BlockerReviewContradictory)
			return false, ""
		}
	default:
		a.block(BlockerReviewContradictory)
		return false, ""
	}
	if fact.Outcome == ReviewPending || fact.Outcome == ReviewRunning {
		a.block(BlockerReviewPending)
	}
	if fact.Outcome == ReviewChangesRequested {
		a.block(BlockerReviewChangesRequested)
	}
	if fact.Outcome == ReviewNeedsHuman {
		a.block(BlockerReviewDecisionUnreconciled)
	}
	return true, fact.Outcome
}

func assessFeedback(a *assessment, facts Facts, candidateCurrent bool) (bool, FeedbackState) {
	fact := facts.Feedback
	if !candidateCurrent {
		if fact.Status != FactMissing {
			a.block(BlockerFeedbackContradictory)
		}
		return false, ""
	}
	switch fact.Status {
	case FactMissing:
		a.block(BlockerFeedbackMissing)
		return false, ""
	case FactStale:
		a.block(BlockerFeedbackStale)
		return false, ""
	case FactContradictory:
		a.block(BlockerFeedbackContradictory)
		return false, ""
	case FactCurrent:
		if fact.CandidateID != facts.Candidate.ID || !knownFeedback(fact.State) {
			a.block(BlockerFeedbackContradictory)
			return false, ""
		}
	default:
		a.block(BlockerFeedbackContradictory)
		return false, ""
	}
	if fact.State == FeedbackActionable {
		a.block(BlockerFeedbackActionable)
	}
	return true, fact.State
}

func assessDelivery(a *assessment, facts Facts, candidateCurrent bool) (bool, DeliveryState) {
	fact := facts.Delivery
	if !candidateCurrent {
		if fact.Status != FactMissing {
			a.block(BlockerDeliveryContradictory)
		}
		return false, ""
	}
	switch fact.Status {
	case FactMissing:
		a.block(BlockerDeliveryMissing)
		return false, ""
	case FactStale:
		a.block(BlockerDeliveryStale)
		return false, ""
	case FactContradictory:
		a.block(BlockerDeliveryContradictory)
		return false, ""
	case FactCurrent:
		if fact.CandidateID != facts.Candidate.ID || !knownDelivery(fact.State) {
			a.block(BlockerDeliveryContradictory)
			return false, ""
		}
	default:
		a.block(BlockerDeliveryContradictory)
		return false, ""
	}
	return true, fact.State
}

func assessCleanup(a *assessment, facts Facts, runCurrent bool) (bool, CleanupState) {
	fact := facts.Cleanup
	switch fact.Status {
	case FactMissing:
		return false, ""
	case FactStale:
		a.block(BlockerCleanupStale)
		return false, ""
	case FactContradictory:
		a.block(BlockerCleanupContradictory)
		return false, ""
	case FactCurrent:
		if !runCurrent || fact.RunID != facts.Run.ID || !knownCleanup(fact.State) {
			a.block(BlockerCleanupContradictory)
			return false, ""
		}
		return true, fact.State
	default:
		a.block(BlockerCleanupContradictory)
		return false, ""
	}
}

func assessHumanInput(a *assessment, facts Facts) (bool, HumanInputState, bool) {
	fact := facts.HumanInput
	switch fact.Status {
	case FactMissing:
		a.block(BlockerHumanInputMissing)
		return false, "", false
	case FactStale:
		a.block(BlockerHumanInputStale)
		return false, "", false
	case FactContradictory:
		a.block(BlockerHumanInputContradictory)
		return false, "", false
	case FactCurrent:
		if fact.TaskVersion != facts.TaskVersion || !knownHumanInput(fact.State) {
			a.block(BlockerHumanInputContradictory)
			return false, "", false
		}
	default:
		a.block(BlockerHumanInputContradictory)
		return false, "", false
	}
	if fact.State != HumanInputPending {
		return true, fact.State, false
	}
	if !knownAttention(fact.Code) || fact.WakeCondition == "" {
		a.block(BlockerHumanInputContradictory)
		return false, "", false
	}
	a.attend(fact.Code)
	return true, fact.State, true
}

func assessTerminal(a *assessment, facts Facts) (bool, TerminalState) {
	fact := facts.Terminal
	switch fact.Status {
	case FactMissing:
		return false, ""
	case FactStale:
		a.block(BlockerTerminalStale)
		return false, ""
	case FactContradictory:
		a.block(BlockerTerminalContradictory)
		return false, ""
	case FactCurrent:
		if fact.TaskVersion != facts.TaskVersion || !knownTerminal(fact.State) {
			a.block(BlockerTerminalContradictory)
			return false, ""
		}
		return true, fact.State
	default:
		a.block(BlockerTerminalContradictory)
		return false, ""
	}
}

func explanationFor(state State, done bool) ExplanationCode {
	if done {
		return ExplanationDone
	}
	switch state {
	case StateNeedsYou:
		return ExplanationNeedsYou
	case StateQueued:
		return ExplanationQueued
	case StateBuilding:
		return ExplanationBuilding
	case StateValidating:
		return ExplanationValidating
	case StateInReview:
		return ExplanationInReview
	case StateReady:
		return ExplanationReady
	default:
		return ""
	}
}

func knownEligibility(value EligibilityDecision) bool {
	switch value {
	case EligibilityEligible, EligibilityDependencyWait, EligibilityPolicyWait,
		EligibilityCapacityWait, EligibilityExternalWait, EligibilityEscalate:
		return true
	default:
		return false
	}
}

func knownValidation(value ValidationOutcome) bool {
	switch value {
	case ValidationPending, ValidationRunning, ValidationPassed, ValidationFailed:
		return true
	default:
		return false
	}
}

func knownReview(value ReviewOutcome) bool {
	switch value {
	case ReviewPending, ReviewRunning, ReviewApproved, ReviewChangesRequested, ReviewNeedsHuman:
		return true
	default:
		return false
	}
}

func knownFeedback(value FeedbackState) bool {
	return value == FeedbackNone || value == FeedbackActionable || value == FeedbackResolved
}

func knownDelivery(value DeliveryState) bool {
	return value == DeliveryPendingPublication || value == DeliveryPublished || value == DeliveryIntegrated
}

func knownCleanup(value CleanupState) bool {
	return value == CleanupPending || value == CleanupComplete
}

func knownHumanInput(value HumanInputState) bool {
	return value == HumanInputNone || value == HumanInputPending || value == HumanInputResolved
}

func knownTerminal(value TerminalState) bool {
	return value == TerminalOpen || value == TerminalDone
}

func knownOutcome(value AgentOutcome) bool {
	switch value {
	case OutcomeCompleted, OutcomeNeedsValidation, OutcomeNeedsReview, OutcomeNeedsHumanDecision,
		OutcomeBlockedByDependency, OutcomeBlockedByAccess, OutcomeBudgetExhausted:
		return true
	default:
		return false
	}
}

func isCandidateBearingOutcome(value AgentOutcome) bool {
	switch value {
	case OutcomeCompleted, OutcomeNeedsValidation, OutcomeNeedsReview:
		return true
	default:
		return false
	}
}

func knownAttention(value AttentionCode) bool {
	for _, known := range attentionCodeOrder {
		if value == known {
			return true
		}
	}
	return false
}
