// SPDX-License-Identifier: Apache-2.0

// Package correction is the exclusive home of the pure, versioned correction
// routing reducer.
package correction

import (
	"slices"

	domaincorrection "github.com/mcuadros/director-engine/domain/correction"
	"github.com/mcuadros/director-engine/domain/runtimebudget"
)

const SchemaVersion = "director.reducer.correction/v2"

const (
	CodeFactsInvalid             = "correction_facts_invalid"
	CodeFrozenContextChanged     = "correction_frozen_context_changed"
	CodeOriginalAgentChanged     = "correction_original_agent_changed"
	CodeProviderUnavailable      = "correction_provider_unavailable"
	CodeProviderAmbiguous        = "correction_provider_ambiguous"
	CodeHumanDecisionRequired    = "correction_human_decision_required"
	CodeP2DecisionRequired       = "correction_p2_decision_required"
	CodeAutomaticFixDisabled     = "correction_automatic_fix_disabled"
	CodeAttemptLimitExhausted    = "correction_attempt_limit_exhausted"
	CodeNewBlockingClassAfterCap = "correction_new_blocking_class_after_cap"
	CodeRootCauseRepeated        = "correction_root_cause_repeated"
	CodeOutputBindingInvalid     = "correction_output_binding_invalid"
	CodeAcknowledgementRepeated  = "correction_acknowledgement_repeated"
	CodeUnchangedCandidate       = "correction_unchanged_candidate"
	CodePromptResultAmbiguous    = "correction_prompt_result_ambiguous"
)

type ProviderState string

const (
	ProviderCurrent     ProviderState = "current"
	ProviderUnavailable ProviderState = "unavailable"
	ProviderAmbiguous   ProviderState = "ambiguous"
)

// Facts is the complete closed correction decision input. Current digests and
// identities are reread for every decision; the model cannot choose them.
type Facts struct {
	SchemaVersion         string                    `json:"schemaVersion"`
	State                 domaincorrection.State    `json:"state"`
	CurrentTaskAgentUUID  string                    `json:"currentTaskAgentUuid"`
	CurrentCandidateID    string                    `json:"currentCandidateId"`
	CurrentCandidateSHA   string                    `json:"currentCandidateSha"`
	CurrentPlanDigest     string                    `json:"currentPlanDigest"`
	CurrentSkillSetDigest string                    `json:"currentSkillSetDigest"`
	CurrentDecisionDigest string                    `json:"currentDecisionDigest"`
	CurrentDiffDigest     string                    `json:"currentDiffDigest"`
	ProviderState         ProviderState             `json:"providerState"`
	BudgetDisposition     runtimebudget.Disposition `json:"budgetDisposition"`
	BudgetReason          runtimebudget.ReasonCode  `json:"budgetReason,omitempty"`
	CIBudgetAvailable     bool                      `json:"ciBudgetAvailable"`
	RepeatingRootCause    bool                      `json:"repeatingRootCause"`
	NewBlockingClasses    []string                  `json:"newBlockingClasses"`
	Output                *domaincorrection.Output  `json:"output,omitempty"`
}

type DecisionKind string

const (
	DecisionDispatchCorrection        DecisionKind = "dispatch_correction"
	DecisionRejectAcknowledgementOnce DecisionKind = "reject_acknowledgement_once"
	DecisionObserveCorrectedCandidate DecisionKind = "observe_corrected_candidate"
	DecisionEscalate                  DecisionKind = "escalate"
)

type Decision struct {
	SchemaVersion             string                         `json:"schemaVersion"`
	Kind                      DecisionKind                   `json:"kind"`
	Code                      string                         `json:"code,omitempty"`
	AttemptReason             domaincorrection.AttemptReason `json:"attemptReason,omitempty"`
	CandidateSHA              string                         `json:"candidateSha,omitempty"`
	BatchSHA256               string                         `json:"batchSha256,omitempty"`
	BatchedFindingIDs         []string                       `json:"batchedFindingIds,omitempty"`
	FindingFingerprint        string                         `json:"findingFingerprint,omitempty"`
	ClassFingerprint          string                         `json:"classFingerprint,omitempty"`
	CandidateFingerprint      string                         `json:"candidateFingerprint,omitempty"`
	CoverageFingerprint       string                         `json:"coverageFingerprint,omitempty"`
	ReusePlanDigest           string                         `json:"reusePlanDigest,omitempty"`
	ReuseSkillSetDigest       string                         `json:"reuseSkillSetDigest,omitempty"`
	RefreshDecisionDigest     string                         `json:"refreshDecisionDigest,omitempty"`
	RefreshDiffDigest         string                         `json:"refreshDiffDigest,omitempty"`
	AcknowledgementRejections uint32                         `json:"acknowledgementRejections,omitempty"`
	CleanupAuthorized         bool                           `json:"cleanupAuthorized"`
}

func escalate(code string) Decision {
	return Decision{SchemaVersion: SchemaVersion, Kind: DecisionEscalate, Code: code}
}

func hasSource(batch domaincorrection.Batch, source domaincorrection.Source) bool {
	return slices.ContainsFunc(batch.Findings, func(finding domaincorrection.Finding) bool { return finding.Source == source })
}

func attemptReason(state domaincorrection.State) domaincorrection.AttemptReason {
	if len(state.Attempts) == 0 {
		return domaincorrection.ReasonInitial
	}
	if state.AcknowledgementRejections > 0 && state.Attempts[len(state.Attempts)-1].Classification == domaincorrection.ClassificationChurn {
		return domaincorrection.ReasonAcknowledgementRejected
	}
	return domaincorrection.ReasonFindingsChanged
}

func contextDecision(state domaincorrection.State, batch domaincorrection.Batch) Decision {
	return Decision{
		SchemaVersion: SchemaVersion, BatchSHA256: batch.SHA256,
		BatchedFindingIDs: domaincorrection.FindingIDs(batch), FindingFingerprint: batch.FindingFingerprint,
		ClassFingerprint: batch.ClassFingerprint, CandidateFingerprint: batch.CandidateFingerprint,
		CoverageFingerprint: batch.CoverageFingerprint, ReusePlanDigest: state.FrozenPlanDigest,
		ReuseSkillSetDigest: state.FrozenSkillSetDigest, RefreshDecisionDigest: state.CurrentDecisionDigest,
		RefreshDiffDigest: state.CurrentDiffDigest,
	}
}

// Reduce chooses only correction dispatch, one acknowledgement rejection,
// exact-Candidate observation, or a typed fail-closed escalation.
func Reduce(facts Facts) Decision {
	state := facts.State
	batch, batchOK := domaincorrection.CurrentBatch(state)
	if facts.SchemaVersion != SchemaVersion || !domaincorrection.ValidState(state) || !batchOK ||
		facts.CurrentCandidateID != state.CurrentCandidateID || facts.CurrentCandidateSHA != state.CurrentCandidateSHA ||
		facts.CurrentDecisionDigest != state.CurrentDecisionDigest || facts.CurrentDiffDigest != state.CurrentDiffDigest {
		return escalate(CodeFactsInvalid)
	}
	if facts.CurrentPlanDigest != state.FrozenPlanDigest || facts.CurrentSkillSetDigest != state.FrozenSkillSetDigest {
		return escalate(CodeFrozenContextChanged)
	}
	if facts.CurrentTaskAgentUUID != state.OriginalTaskAgentUUID {
		return escalate(CodeOriginalAgentChanged)
	}
	switch facts.ProviderState {
	case ProviderUnavailable:
		return escalate(CodeProviderUnavailable)
	case ProviderAmbiguous:
		return escalate(CodeProviderAmbiguous)
	case ProviderCurrent:
	default:
		return escalate(CodeFactsInvalid)
	}
	if batch.RequiresHumanDecision {
		for _, finding := range batch.Findings {
			if finding.RequiresHumanDecision && finding.Severity == domaincorrection.SeverityP2 {
				return escalate(CodeP2DecisionRequired)
			}
		}
		return escalate(CodeHumanDecisionRequired)
	}
	if hasSource(batch, domaincorrection.SourceReview) || hasSource(batch, domaincorrection.SourceHuman) {
		if !state.Policy.AutoFixReviewFeedback {
			return escalate(CodeAutomaticFixDisabled)
		}
	}
	if hasSource(batch, domaincorrection.SourceValidation) || hasSource(batch, domaincorrection.SourceCI) {
		if !state.Policy.AutoFixCIFailures {
			return escalate(CodeAutomaticFixDisabled)
		}
	}
	if facts.BudgetDisposition != runtimebudget.DispositionAllow {
		if facts.BudgetReason == "" {
			return escalate(CodeFactsInvalid)
		}
		return escalate(string(facts.BudgetReason))
	}
	if !facts.CIBudgetAvailable {
		return escalate(string(runtimebudget.ReasonCIHard))
	}
	decision := contextDecision(state, batch)
	if facts.Output == nil {
		if len(state.Attempts) >= int(domaincorrection.AttemptLimit) {
			if len(facts.NewBlockingClasses) > 0 {
				return escalate(CodeNewBlockingClassAfterCap)
			}
			return escalate(CodeAttemptLimitExhausted)
		}
		if facts.RepeatingRootCause {
			return escalate(CodeRootCauseRepeated)
		}
		decision.Kind = DecisionDispatchCorrection
		decision.AttemptReason = attemptReason(state)
		return decision
	}
	attempt := state.Attempts[len(state.Attempts)-1]
	if !domaincorrection.ValidOutput(*facts.Output, state, attempt) || attempt.Output != nil {
		return escalate(CodeOutputBindingInvalid)
	}
	output := facts.Output
	unchanged := output.CandidateSHA == state.CurrentCandidateSHA
	if output.AcknowledgementOnly || unchanged {
		if state.AcknowledgementRejections > 0 {
			return escalate(CodeAcknowledgementRepeated)
		}
		decision.Kind = DecisionRejectAcknowledgementOnce
		decision.Code = CodeUnchangedCandidate
		decision.AcknowledgementRejections = 1
		return decision
	}
	decision.Kind = DecisionObserveCorrectedCandidate
	decision.CandidateSHA = output.CandidateSHA
	return decision
}
