// SPDX-License-Identifier: Apache-2.0

package delivery

import domainintegration "github.com/mcuadros/director-engine/domain/integration"

const IntegrationSchemaVersion = "director.reducer.integration/v1"

type IntegrationFacts struct {
	SchemaVersion        string                  `json:"schemaVersion"`
	State                domainintegration.State `json:"state"`
	TaskBlocked          bool                    `json:"taskBlocked"`
	ProjectActive        bool                    `json:"projectActive"`
	LeaseCurrent         bool                    `json:"leaseCurrent"`
	CandidateCurrent     bool                    `json:"candidateCurrent"`
	PublicationReady     bool                    `json:"publicationReady"`
	ValidationPassed     bool                    `json:"validationPassed"`
	ReviewApproved       bool                    `json:"reviewApproved"`
	FeedbackClear        bool                    `json:"feedbackClear"`
	CorrectionSettled    bool                    `json:"correctionSettled"`
	DirectDeliveryAbsent bool                    `json:"directDeliveryAbsent"`
	CIBudgetValid        bool                    `json:"ciBudgetValid"`
	NowMillis            int64                   `json:"nowMillis"`
}

type IntegrationDecisionKind string

const (
	IntegrationDecisionWaitHuman    IntegrationDecisionKind = "wait_human"
	IntegrationDecisionObserve      IntegrationDecisionKind = "observe"
	IntegrationDecisionDispatch     IntegrationDecisionKind = "dispatch_exact_merge"
	IntegrationDecisionComplete     IntegrationDecisionKind = "complete"
	IntegrationDecisionWaitExternal IntegrationDecisionKind = "wait_external"
	IntegrationDecisionEscalate     IntegrationDecisionKind = "escalate"
)

const (
	IntegrationCodeFactsVersionMismatch = "integration_facts_version_mismatch"
	IntegrationCodeStateInvalid         = "integration_state_invalid"
	IntegrationCodeTaskBlocked          = "integration_task_blocked"
	IntegrationCodeProjectInactive      = "integration_project_inactive"
	IntegrationCodeLeaseStale           = "integration_lease_stale"
	IntegrationCodeCandidateStale       = "integration_candidate_stale"
	IntegrationCodePublicationStale     = "integration_publication_stale"
	IntegrationCodeValidationStale      = "integration_validation_stale"
	IntegrationCodeReviewStale          = "integration_review_stale"
	IntegrationCodeFeedbackPending      = "integration_feedback_pending"
	IntegrationCodeCorrectionPending    = "integration_correction_pending"
	IntegrationCodeDirectConflict       = "integration_direct_delivery_conflict"
	IntegrationCodeCIBudgetInvalid      = "integration_ci_budget_invalid"
)

type IntegrationDecision struct {
	SchemaVersion     string                  `json:"schemaVersion"`
	Kind              IntegrationDecisionKind `json:"kind"`
	Code              string                  `json:"code,omitempty"`
	MergeAuthorized   bool                    `json:"mergeAuthorized"`
	CleanupAuthorized bool                    `json:"cleanupAuthorized"`
}

func integrationDecision(kind IntegrationDecisionKind, code string) IntegrationDecision {
	return IntegrationDecision{SchemaVersion: IntegrationSchemaVersion, Kind: kind, Code: code,
		MergeAuthorized: kind == IntegrationDecisionDispatch}
}

// ReduceIntegration requires all durable gates before considering one fresh
// external observation. It never grants cleanup or a direct-delivery fallback.
func ReduceIntegration(facts IntegrationFacts) IntegrationDecision {
	switch {
	case facts.SchemaVersion != IntegrationSchemaVersion:
		return integrationDecision(IntegrationDecisionEscalate, IntegrationCodeFactsVersionMismatch)
	case !domainintegration.ValidState(facts.State) || facts.NowMillis < 0:
		return integrationDecision(IntegrationDecisionEscalate, IntegrationCodeStateInvalid)
	case facts.State.Phase == domainintegration.PhaseComplete:
		return integrationDecision(IntegrationDecisionComplete, "")
	case facts.State.Phase == domainintegration.PhaseNeedsYou:
		return integrationDecision(IntegrationDecisionEscalate, facts.State.NeedsYouCode)
	case facts.State.Phase == domainintegration.PhaseInvalidated:
		return integrationDecision(IntegrationDecisionEscalate, facts.State.InvalidationCode)
	case facts.TaskBlocked:
		return integrationDecision(IntegrationDecisionEscalate, IntegrationCodeTaskBlocked)
	case !facts.ProjectActive:
		return integrationDecision(IntegrationDecisionWaitExternal, IntegrationCodeProjectInactive)
	case !facts.LeaseCurrent:
		return integrationDecision(IntegrationDecisionWaitExternal, IntegrationCodeLeaseStale)
	case !facts.CandidateCurrent:
		return integrationDecision(IntegrationDecisionEscalate, IntegrationCodeCandidateStale)
	case !facts.PublicationReady:
		return integrationDecision(IntegrationDecisionEscalate, IntegrationCodePublicationStale)
	case !facts.ValidationPassed:
		return integrationDecision(IntegrationDecisionEscalate, IntegrationCodeValidationStale)
	case !facts.ReviewApproved:
		return integrationDecision(IntegrationDecisionEscalate, IntegrationCodeReviewStale)
	case !facts.FeedbackClear:
		return integrationDecision(IntegrationDecisionEscalate, IntegrationCodeFeedbackPending)
	case !facts.CorrectionSettled:
		return integrationDecision(IntegrationDecisionEscalate, IntegrationCodeCorrectionPending)
	case !facts.DirectDeliveryAbsent:
		return integrationDecision(IntegrationDecisionEscalate, IntegrationCodeDirectConflict)
	case !facts.CIBudgetValid:
		return integrationDecision(IntegrationDecisionEscalate, IntegrationCodeCIBudgetInvalid)
	case facts.State.Phase == domainintegration.PhaseReady:
		return integrationDecision(IntegrationDecisionWaitHuman, "")
	}
	state := facts.State
	if state.Integration == nil || state.Integration.Observation == nil {
		return integrationDecision(IntegrationDecisionObserve, "")
	}
	observation := *state.Integration.Observation
	if !domainintegration.ValidObservation(observation, state.Binding, state.Integration.ID, facts.NowMillis) {
		return integrationDecision(IntegrationDecisionObserve, "")
	}
	switch observation.Status {
	case domainintegration.ObservationIntegrated:
		return integrationDecision(IntegrationDecisionComplete, "")
	case domainintegration.ObservationUnavailable:
		return integrationDecision(IntegrationDecisionWaitExternal, string(observation.Code))
	case domainintegration.ObservationInvalid:
		return integrationDecision(IntegrationDecisionEscalate, string(observation.Code))
	case domainintegration.ObservationReady:
		if state.Integration.Attempt >= state.Integration.AttemptLimit {
			return integrationDecision(IntegrationDecisionEscalate, string(domainintegration.CodeAttemptsExhausted))
		}
		return integrationDecision(IntegrationDecisionDispatch, "")
	default:
		return integrationDecision(IntegrationDecisionEscalate, string(domainintegration.CodeResponseUnknown))
	}
}
