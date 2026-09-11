// SPDX-License-Identifier: Apache-2.0

package projection

import (
	"strings"

	"github.com/mcuadros/director-engine/domain/agentoutcome"
)

// FactStatus classifies whether one normalized durable or external fact is
// usable for the current Task projection. The empty and unknown values fail
// closed as contradictory.
type FactStatus string

const (
	FactMissing       FactStatus = "missing"
	FactCurrent       FactStatus = "current"
	FactStale         FactStatus = "stale"
	FactContradictory FactStatus = "contradictory"
)

// EligibilityDecision is the closed, already-reduced launch eligibility
// result consumed by projection. Projection never recomputes scheduling.
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

type ClaimFact struct {
	Required    bool
	Status      FactStatus
	TaskVersion uint64
	RunID       string
	CandidateID string
	Outcome     agentoutcome.Kind
}

const MaximumProjectionClaims = 7

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

// AttentionCode is the closed actionable human-intervention vocabulary.
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

// AttentionCodeOrder returns a defensive copy of the stable display order.
func AttentionCodeOrder() []AttentionCode {
	return append([]AttentionCode(nil), attentionCodeOrder[:]...)
}

type HumanInputFact struct {
	Status        FactStatus
	TaskVersion   uint64
	State         HumanInputState
	Code          AttentionCode
	ReasonCode    string
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

// TaskStateFacts is the complete normalized input for derived Board/List
// state. It intentionally has no state, lane, card position, or mutation
// field: only the engine reducer can derive those values.
type TaskStateFacts struct {
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

// ExplanationCode gives every derived state one stable primary explanation.
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

// BlockerCode is the closed fail-closed reason vocabulary. Values are emitted
// only in BlockerCodeOrder regardless of input ordering.
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

// BlockerCodeOrder returns a defensive copy of the stable blocker order.
func BlockerCodeOrder() []BlockerCode {
	return append([]BlockerCode(nil), blockerCodeOrder[:]...)
}

// TaskProjection is the conservative engine-owned result for one Task.
// BoardMember and DoneMember are mutually exclusive; Done has no editable
// lane state.
type TaskProjection struct {
	State       BoardState      `json:"state,omitempty"`
	BoardMember bool            `json:"boardMember"`
	DoneMember  bool            `json:"doneMember"`
	Explanation ExplanationCode `json:"explanation"`
	Blockers    []BlockerCode   `json:"blockers,omitempty"`
	Attention   []AttentionCode `json:"attention,omitempty"`
}

type projectionAssessment struct {
	blockers      map[BlockerCode]bool
	attention     map[AttentionCode]bool
	needsYou      bool
	terminalDone  bool
	terminalValid bool
}

func newProjectionAssessment() *projectionAssessment {
	return &projectionAssessment{
		blockers: make(map[BlockerCode]bool), attention: make(map[AttentionCode]bool),
	}
}

func (assessment *projectionAssessment) block(code BlockerCode) {
	assessment.blockers[code] = true
}

func (assessment *projectionAssessment) result(state BoardState, doneProof bool) TaskProjection {
	done := doneProof && assessment.terminalValid && assessment.terminalDone
	if assessment.terminalDone && !done && !assessment.needsYou {
		assessment.block(BlockerTerminalDoneUnproved)
	}
	if assessment.needsYou {
		state = StateNeedsYou
		done = false
	}
	result := TaskProjection{State: state, BoardMember: !done, DoneMember: done}
	if done {
		result.State = ""
		result.Explanation = ExplanationDone
	} else {
		switch state {
		case StateNeedsYou:
			result.Explanation = ExplanationNeedsYou
		case StateQueued:
			result.Explanation = ExplanationQueued
		case StateBuilding:
			result.Explanation = ExplanationBuilding
		case StateValidating:
			result.Explanation = ExplanationValidating
		case StateInReview:
			result.Explanation = ExplanationInReview
		case StateReady:
			result.Explanation = ExplanationReady
		default:
			result.State = StateQueued
			result.Explanation = ExplanationQueued
			assessment.block(BlockerTaskFactContradictory)
		}
	}
	for _, code := range blockerCodeOrder {
		if assessment.blockers[code] {
			result.Blockers = append(result.Blockers, code)
		}
	}
	for _, code := range attentionCodeOrder {
		if assessment.attention[code] {
			result.Attention = append(result.Attention, code)
		}
	}
	return result
}

func validAttention(code AttentionCode) bool {
	for _, candidate := range attentionCodeOrder {
		if candidate == code {
			return true
		}
	}
	return false
}

func assessHumanInput(facts TaskStateFacts, assessment *projectionAssessment) bool {
	human := facts.HumanInput
	switch human.Status {
	case FactMissing:
		assessment.block(BlockerHumanInputMissing)
		return false
	case FactStale:
		assessment.block(BlockerHumanInputStale)
		return false
	case FactContradictory:
		assessment.block(BlockerHumanInputContradictory)
		return false
	case FactCurrent:
	default:
		assessment.block(BlockerHumanInputContradictory)
		return false
	}
	if human.TaskVersion != facts.TaskVersion {
		assessment.block(BlockerHumanInputContradictory)
		return false
	}
	switch human.State {
	case HumanInputNone, HumanInputResolved:
		if human.Code != "" || human.ReasonCode != "" || human.WakeCondition != "" {
			assessment.block(BlockerHumanInputContradictory)
			return false
		}
		return true
	case HumanInputPending:
		if !validAttention(human.Code) || strings.TrimSpace(human.WakeCondition) == "" ||
			len(human.WakeCondition) > 256 || human.ReasonCode != "" &&
			(strings.TrimSpace(human.ReasonCode) == "" || len(human.ReasonCode) > 128) {
			assessment.block(BlockerHumanInputContradictory)
			return false
		}
		assessment.attention[human.Code] = true
		assessment.needsYou = true
		return true
	default:
		assessment.block(BlockerHumanInputContradictory)
		return false
	}
}

func assessTerminal(facts TaskStateFacts, assessment *projectionAssessment) {
	terminal := facts.Terminal
	switch terminal.Status {
	case FactMissing:
		return
	case FactStale:
		assessment.block(BlockerTerminalStale)
		return
	case FactContradictory:
		assessment.block(BlockerTerminalContradictory)
		return
	case FactCurrent:
	default:
		assessment.block(BlockerTerminalContradictory)
		return
	}
	if terminal.TaskVersion != facts.TaskVersion ||
		(terminal.State != TerminalOpen && terminal.State != TerminalDone) {
		assessment.block(BlockerTerminalContradictory)
		return
	}
	assessment.terminalValid = true
	assessment.terminalDone = terminal.State == TerminalDone
}

func assessClaims(
	facts TaskStateFacts,
	assessment *projectionAssessment,
	runValid bool,
	runPresent bool,
	candidateCurrent bool,
) bool {
	valid := true
	if len(facts.Claims) > MaximumProjectionClaims {
		assessment.block(BlockerClaimContradictory)
		return false
	}
	seen := make(map[agentoutcome.Kind]struct{})
	for _, claim := range facts.Claims {
		if !claim.Required {
			continue
		}
		switch claim.Status {
		case FactMissing:
			assessment.block(BlockerClaimMissing)
			valid = false
			continue
		case FactStale:
			assessment.block(BlockerClaimStale)
			valid = false
			continue
		case FactContradictory:
			assessment.block(BlockerClaimContradictory)
			valid = false
			continue
		case FactCurrent:
		default:
			assessment.block(BlockerClaimContradictory)
			valid = false
			continue
		}
		if claim.TaskVersion != facts.TaskVersion {
			assessment.block(BlockerClaimContradictory)
			valid = false
			continue
		}
		if _, duplicate := seen[claim.Outcome]; duplicate {
			assessment.block(BlockerClaimContradictory)
			valid = false
			continue
		}
		seen[claim.Outcome] = struct{}{}
		switch claim.Outcome {
		case agentoutcome.Completed, agentoutcome.NeedsValidation, agentoutcome.NeedsReview:
			if !runValid || !runPresent || claim.RunID != facts.Run.ID || !candidateCurrent ||
				claim.CandidateID == "" || claim.CandidateID != facts.Candidate.ID {
				assessment.block(BlockerClaimContradictory)
				valid = false
			}
		case agentoutcome.NeedsHumanDecision, agentoutcome.BlockedByDependency,
			agentoutcome.BlockedByAccess, agentoutcome.BudgetExhausted:
			if claim.CandidateID != "" ||
				(claim.RunID != "" && (!runValid || !runPresent || claim.RunID != facts.Run.ID)) {
				assessment.block(BlockerClaimContradictory)
				valid = false
			}
		default:
			assessment.block(BlockerClaimContradictory)
			valid = false
		}
	}
	return valid
}

func assessValidation(facts TaskStateFacts, assessment *projectionAssessment) bool {
	validation := facts.Validation
	switch validation.Status {
	case FactMissing:
		assessment.block(BlockerValidationMissing)
		return false
	case FactStale:
		assessment.block(BlockerValidationStale)
		return false
	case FactContradictory:
		assessment.block(BlockerValidationContradictory)
		return false
	case FactCurrent:
	default:
		assessment.block(BlockerValidationContradictory)
		return false
	}
	if validation.CandidateID != facts.Candidate.ID || validation.CandidateID == "" {
		assessment.block(BlockerValidationContradictory)
		return false
	}
	switch validation.Outcome {
	case ValidationPending, ValidationRunning:
		assessment.block(BlockerValidationPending)
		return false
	case ValidationFailed:
		assessment.block(BlockerValidationFailed)
		return false
	case ValidationPassed:
		return true
	default:
		assessment.block(BlockerValidationContradictory)
		return false
	}
}

func assessReview(facts TaskStateFacts, assessment *projectionAssessment) bool {
	review := facts.Review
	switch review.Status {
	case FactMissing:
		assessment.block(BlockerReviewMissing)
		return false
	case FactStale:
		assessment.block(BlockerReviewStale)
		return false
	case FactContradictory:
		assessment.block(BlockerReviewContradictory)
		return false
	case FactCurrent:
	default:
		assessment.block(BlockerReviewContradictory)
		return false
	}
	if review.CandidateID != facts.Candidate.ID || review.CandidateID == "" {
		assessment.block(BlockerReviewContradictory)
		return false
	}
	switch review.Outcome {
	case ReviewPending, ReviewRunning:
		assessment.block(BlockerReviewPending)
		return false
	case ReviewChangesRequested:
		assessment.block(BlockerReviewChangesRequested)
		return false
	case ReviewNeedsHuman:
		assessment.block(BlockerReviewDecisionUnreconciled)
		return false
	case ReviewApproved:
		return true
	default:
		assessment.block(BlockerReviewContradictory)
		return false
	}
}

func assessFeedback(facts TaskStateFacts, assessment *projectionAssessment) bool {
	feedback := facts.Feedback
	switch feedback.Status {
	case FactMissing:
		assessment.block(BlockerFeedbackMissing)
		return false
	case FactStale:
		assessment.block(BlockerFeedbackStale)
		return false
	case FactContradictory:
		assessment.block(BlockerFeedbackContradictory)
		return false
	case FactCurrent:
	default:
		assessment.block(BlockerFeedbackContradictory)
		return false
	}
	if feedback.CandidateID != facts.Candidate.ID || feedback.CandidateID == "" {
		assessment.block(BlockerFeedbackContradictory)
		return false
	}
	switch feedback.State {
	case FeedbackNone, FeedbackResolved:
		return true
	case FeedbackActionable:
		assessment.block(BlockerFeedbackActionable)
		return false
	default:
		assessment.block(BlockerFeedbackContradictory)
		return false
	}
}

func assessDelivery(facts TaskStateFacts, assessment *projectionAssessment) (DeliveryState, bool) {
	delivery := facts.Delivery
	switch delivery.Status {
	case FactMissing:
		assessment.block(BlockerDeliveryMissing)
		return "", false
	case FactStale:
		assessment.block(BlockerDeliveryStale)
		return "", false
	case FactContradictory:
		assessment.block(BlockerDeliveryContradictory)
		return "", false
	case FactCurrent:
	default:
		assessment.block(BlockerDeliveryContradictory)
		return "", false
	}
	if delivery.CandidateID != facts.Candidate.ID || delivery.CandidateID == "" {
		assessment.block(BlockerDeliveryContradictory)
		return "", false
	}
	switch delivery.State {
	case DeliveryPendingPublication, DeliveryPublished, DeliveryIntegrated:
		return delivery.State, true
	default:
		assessment.block(BlockerDeliveryContradictory)
		return "", false
	}
}

func assessRunFact(facts TaskStateFacts, assessment *projectionAssessment) (RunFact, bool, bool) {
	run := facts.Run
	switch run.Status {
	case FactMissing:
		return run, true, false
	case FactStale:
		assessment.block(BlockerRunStale)
		return run, false, false
	case FactContradictory:
		assessment.block(BlockerRunContradictory)
		return run, false, false
	case FactCurrent:
	default:
		assessment.block(BlockerRunContradictory)
		return run, false, false
	}
	if run.TaskVersion != facts.TaskVersion || (run.Active && run.ID == "") {
		assessment.block(BlockerRunContradictory)
		return run, false, false
	}
	return run, true, run.ID != ""
}

func assessCandidateFact(
	facts TaskStateFacts,
	assessment *projectionAssessment,
	runValid bool,
	runPresent bool,
) bool {
	candidate := facts.Candidate
	switch candidate.Status {
	case FactMissing:
		return false
	case FactStale:
		assessment.block(BlockerCandidateStale)
		return false
	case FactContradictory:
		assessment.block(BlockerCandidateContradictory)
		return false
	case FactCurrent:
	default:
		assessment.block(BlockerCandidateContradictory)
		return false
	}
	if !runValid || !runPresent || candidate.TaskVersion != facts.TaskVersion ||
		candidate.ID == "" || candidate.RunID != facts.Run.ID {
		assessment.block(BlockerCandidateContradictory)
		return false
	}
	return true
}

func assessUnavailableCandidateFacts(facts TaskStateFacts, assessment *projectionAssessment) {
	mark := func(status FactStatus, contradictory BlockerCode) {
		switch status {
		case FactMissing:
		default:
			assessment.block(contradictory)
		}
	}
	mark(facts.Validation.Status, BlockerValidationContradictory)
	mark(facts.Review.Status, BlockerReviewContradictory)
	mark(facts.Feedback.Status, BlockerFeedbackContradictory)
	mark(facts.Delivery.Status, BlockerDeliveryContradictory)
	for _, claim := range facts.Claims {
		if claim.Required && claim.Status == FactCurrent && claim.CandidateID != "" {
			assessment.block(BlockerClaimContradictory)
		}
	}
}

func assessCleanupFact(
	facts TaskStateFacts,
	assessment *projectionAssessment,
	runValid bool,
	runPresent bool,
	deliveryIntegrated bool,
) bool {
	cleanup := facts.Cleanup
	missingProof := func(code BlockerCode) {
		if deliveryIntegrated {
			assessment.block(BlockerCleanupMissing)
		}
		assessment.block(code)
	}
	switch cleanup.Status {
	case FactMissing:
		if deliveryIntegrated {
			assessment.block(BlockerCleanupMissing)
		}
		return false
	case FactStale:
		missingProof(BlockerCleanupStale)
		return false
	case FactContradictory:
		missingProof(BlockerCleanupContradictory)
		return false
	case FactCurrent:
	default:
		missingProof(BlockerCleanupContradictory)
		return false
	}
	if !runValid || !runPresent || cleanup.RunID == "" || cleanup.RunID != facts.Run.ID {
		missingProof(BlockerCleanupContradictory)
		return false
	}
	switch cleanup.State {
	case CleanupPending:
		if deliveryIntegrated {
			assessment.block(BlockerCleanupPending)
		}
		return false
	case CleanupComplete:
		return deliveryIntegrated
	default:
		missingProof(BlockerCleanupContradictory)
		return false
	}
}

// DeriveTaskProjection deterministically reduces normalized facts to one
// conservative Board/List state. Claims can block advancement but never prove
// state, and only a current valid pending HumanInput fact yields Needs you.
func DeriveTaskProjection(facts TaskStateFacts) TaskProjection {
	assessment := newProjectionAssessment()
	taskValid := strings.TrimSpace(facts.TaskID) != "" && len(facts.TaskID) <= 128
	if !taskValid {
		assessment.block(BlockerTaskFactContradictory)
	}
	humanCurrent := assessHumanInput(facts, assessment)
	assessTerminal(facts, assessment)
	run, runValid, runPresent := assessRunFact(facts, assessment)
	candidateCurrent := assessCandidateFact(facts, assessment, runValid, runPresent)
	claimsValid := assessClaims(facts, assessment, runValid, runPresent, candidateCurrent)
	validationPassed, reviewPassed, feedbackReady := false, false, false
	validationRunning, validationPending, validationFailed := false, false, false
	reviewRunning, reviewPending, reviewNeedsHuman, reviewChanges := false, false, false, false
	deliveryState, deliveryCurrent := DeliveryState(""), false
	if candidateCurrent {
		validationPassed = assessValidation(facts, assessment)
		reviewPassed = assessReview(facts, assessment)
		feedbackReady = assessFeedback(facts, assessment)
		deliveryState, deliveryCurrent = assessDelivery(facts, assessment)
		validationRunning = facts.Validation.Status == FactCurrent &&
			facts.Validation.CandidateID == facts.Candidate.ID && facts.Validation.Outcome == ValidationRunning
		validationPending = facts.Validation.Status == FactCurrent &&
			facts.Validation.CandidateID == facts.Candidate.ID && facts.Validation.Outcome == ValidationPending
		validationFailed = facts.Validation.Status == FactCurrent &&
			facts.Validation.CandidateID == facts.Candidate.ID && facts.Validation.Outcome == ValidationFailed
		reviewRunning = facts.Review.Status == FactCurrent &&
			facts.Review.CandidateID == facts.Candidate.ID && facts.Review.Outcome == ReviewRunning
		reviewPending = facts.Review.Status == FactCurrent &&
			facts.Review.CandidateID == facts.Candidate.ID && facts.Review.Outcome == ReviewPending
		reviewNeedsHuman = facts.Review.Status == FactCurrent &&
			facts.Review.CandidateID == facts.Candidate.ID && facts.Review.Outcome == ReviewNeedsHuman
		reviewChanges = facts.Review.Status == FactCurrent &&
			facts.Review.CandidateID == facts.Candidate.ID && facts.Review.Outcome == ReviewChangesRequested
	} else {
		assessUnavailableCandidateFacts(facts, assessment)
	}
	deliveryIntegrated := candidateCurrent && deliveryCurrent && deliveryState == DeliveryIntegrated
	cleanupComplete := assessCleanupFact(facts, assessment, runValid, runPresent, deliveryIntegrated)
	eligibility := facts.Eligibility
	eligibilityCurrent := false
	switch eligibility.Status {
	case FactMissing:
		assessment.block(BlockerEligibilityMissing)
	case FactStale:
		assessment.block(BlockerEligibilityStale)
	case FactContradictory:
		assessment.block(BlockerEligibilityContradictory)
	case FactCurrent:
		if eligibility.TaskVersion != facts.TaskVersion {
			assessment.block(BlockerEligibilityContradictory)
		} else {
			eligibilityCurrent = true
		}
	default:
		assessment.block(BlockerEligibilityContradictory)
	}
	if eligibilityCurrent {
		switch eligibility.Decision {
		case EligibilityDependencyWait:
			assessment.block(BlockerDependencyWait)
		case EligibilityPolicyWait:
			assessment.block(BlockerPolicyWait)
		case EligibilityCapacityWait:
			assessment.block(BlockerCapacityWait)
		case EligibilityExternalWait:
			assessment.block(BlockerExternalWait)
		case EligibilityEscalate:
			assessment.block(BlockerEscalationUnreconciled)
		case EligibilityEligible:
		default:
			assessment.block(BlockerEligibilityContradictory)
		}
	}
	if assessment.needsYou {
		return assessment.result(StateNeedsYou, false)
	}

	if !runValid || !runPresent {
		return assessment.result(StateQueued, false)
	}
	if !candidateCurrent {
		if !run.Active {
			return assessment.result(StateQueued, false)
		}
		return assessment.result(StateBuilding, false)
	}
	switch {
	case validationRunning:
		return assessment.result(StateValidating, false)
	case reviewRunning:
		return assessment.result(StateInReview, false)
	case validationPending:
		return assessment.result(StateValidating, false)
	case !validationPassed && !validationFailed:
		return assessment.result(StateValidating, false)
	case reviewPending || reviewNeedsHuman:
		return assessment.result(StateInReview, false)
	case !reviewPassed && !reviewChanges:
		return assessment.result(StateInReview, false)
	case validationFailed || reviewChanges:
		return assessment.result(StateBuilding, false)
	}
	if !feedbackReady {
		if facts.Feedback.Status == FactCurrent && facts.Feedback.CandidateID == facts.Candidate.ID &&
			facts.Feedback.State == FeedbackActionable {
			return assessment.result(StateBuilding, false)
		}
		return assessment.result(StateInReview, false)
	}
	if !taskValid || !claimsValid || !humanCurrent || !deliveryCurrent {
		return assessment.result(StateInReview, false)
	}
	switch deliveryState {
	case DeliveryPendingPublication:
		assessment.block(BlockerDeliveryPending)
		return assessment.result(StateReady, false)
	case DeliveryPublished:
		assessment.block(BlockerIntegrationPending)
		return assessment.result(StateReady, false)
	case DeliveryIntegrated:
	}
	if !cleanupComplete {
		return assessment.result(StateReady, false)
	}
	if !assessment.terminalValid || !assessment.terminalDone {
		assessment.block(BlockerTerminalPending)
		return assessment.result(StateReady, false)
	}
	return assessment.result(StateReady, !run.Active)
}
