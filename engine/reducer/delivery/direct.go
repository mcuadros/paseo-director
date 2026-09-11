// SPDX-License-Identifier: Apache-2.0

package delivery

import directdomain "github.com/mcuadros/director-engine/domain/directdelivery"

const DirectSchemaVersion = "director.reducer.direct-delivery/v1"

// DirectFacts is the closed current fact set for one direct-delivery step.
// PRPublicationStarted is a refusal fact only: no value can convert it into a
// direct intent.
type DirectFacts struct {
	SchemaVersion        string             `json:"schemaVersion"`
	State                directdomain.State `json:"state"`
	TaskBlocked          bool               `json:"taskBlocked"`
	ProjectActive        bool               `json:"projectActive"`
	LeaseCurrent         bool               `json:"leaseCurrent"`
	CandidateCurrent     bool               `json:"candidateCurrent"`
	ReviewApproved       bool               `json:"reviewApproved"`
	CIPassed             bool               `json:"ciPassed"`
	CorrectionSettled    bool               `json:"correctionSettled"`
	PRPublicationStarted bool               `json:"prPublicationStarted"`
	NowMillis            int64              `json:"nowMillis"`
}

type DirectDecisionKind string

const (
	DirectDecisionWaitHuman    DirectDecisionKind = "wait_human"
	DirectDecisionObserve      DirectDecisionKind = "observe_remote"
	DirectDecisionDispatch     DirectDecisionKind = "dispatch_exact_push"
	DirectDecisionComplete     DirectDecisionKind = "complete"
	DirectDecisionWaitExternal DirectDecisionKind = "wait_external"
	DirectDecisionEscalate     DirectDecisionKind = "escalate"
)

const (
	DirectCodeFactsVersionMismatch = "direct_facts_version_mismatch"
	DirectCodeStateInvalid         = "direct_state_invalid"
	DirectCodeTaskBlocked          = "direct_task_blocked"
	DirectCodeProjectInactive      = "direct_project_inactive"
	DirectCodeLeaseStale           = "direct_lease_stale"
	DirectCodeCandidateStale       = "direct_candidate_stale"
	DirectCodeReviewNotApproved    = "direct_review_not_approved"
	DirectCodeCINotPassed          = "direct_ci_not_passed"
	DirectCodeCorrectionPending    = "direct_correction_pending"
	DirectCodePRFallbackForbidden  = "direct_pr_fallback_forbidden"
	DirectCodeRemoteRefChanged     = "direct_remote_ref_changed"
	DirectCodeRemoteRefAbsent      = "direct_remote_ref_absent"
	DirectCodeRemoteAmbiguous      = "direct_remote_ambiguous"
	DirectCodeAttemptsExhausted    = "direct_attempts_exhausted"
)

// DirectDecision never grants PR publication, cleanup, or any mutation other
// than the one exact conditional push represented by Dispatch.
type DirectDecision struct {
	SchemaVersion     string             `json:"schemaVersion"`
	Kind              DirectDecisionKind `json:"kind"`
	Code              string             `json:"code,omitempty"`
	PushAuthorized    bool               `json:"pushAuthorized"`
	CleanupAuthorized bool               `json:"cleanupAuthorized"`
}

func directDecision(kind DirectDecisionKind, code string) DirectDecision {
	return DirectDecision{SchemaVersion: DirectSchemaVersion, Kind: kind, Code: code,
		PushAuthorized: kind == DirectDecisionDispatch}
}

// ReduceDirect applies all exact Candidate/quality/policy gates before it
// consumes a fresh remote observation. Missing or ambiguous facts never
// choose a different delivery mode.
func ReduceDirect(facts DirectFacts) DirectDecision {
	switch {
	case facts.SchemaVersion != DirectSchemaVersion:
		return directDecision(DirectDecisionEscalate, DirectCodeFactsVersionMismatch)
	case !directdomain.ValidState(facts.State) || facts.NowMillis < 0:
		return directDecision(DirectDecisionEscalate, DirectCodeStateInvalid)
	case facts.PRPublicationStarted:
		return directDecision(DirectDecisionEscalate, DirectCodePRFallbackForbidden)
	case facts.State.Phase == directdomain.PhaseComplete:
		return directDecision(DirectDecisionComplete, "")
	case facts.State.Phase == directdomain.PhaseNeedsYou:
		return directDecision(DirectDecisionEscalate, facts.State.NeedsYouCode)
	case facts.State.Phase == directdomain.PhaseInvalidated:
		return directDecision(DirectDecisionEscalate, facts.State.InvalidationCode)
	case facts.TaskBlocked:
		return directDecision(DirectDecisionEscalate, DirectCodeTaskBlocked)
	case !facts.ProjectActive:
		return directDecision(DirectDecisionWaitExternal, DirectCodeProjectInactive)
	case !facts.LeaseCurrent:
		return directDecision(DirectDecisionWaitExternal, DirectCodeLeaseStale)
	case !facts.CandidateCurrent:
		return directDecision(DirectDecisionEscalate, DirectCodeCandidateStale)
	case !facts.ReviewApproved:
		return directDecision(DirectDecisionEscalate, DirectCodeReviewNotApproved)
	case !facts.CIPassed:
		return directDecision(DirectDecisionEscalate, DirectCodeCINotPassed)
	case !facts.CorrectionSettled:
		return directDecision(DirectDecisionEscalate, DirectCodeCorrectionPending)
	}

	state := facts.State
	switch state.Phase {
	case directdomain.PhaseWaitingHuman:
		return directDecision(DirectDecisionWaitHuman, "")
	}
	if state.Integration == nil || state.Integration.Observation == nil {
		return directDecision(DirectDecisionObserve, "")
	}
	observation := *state.Integration.Observation
	if !directdomain.ValidObservation(observation, state.Binding, state.Integration.ID, facts.NowMillis) {
		return directDecision(DirectDecisionObserve, "")
	}
	switch observation.Status {
	case directdomain.ObservationDesired:
		return directDecision(DirectDecisionComplete, "")
	case directdomain.ObservationUnavailable:
		return directDecision(DirectDecisionWaitExternal, string(observation.Code))
	case directdomain.ObservationCurrentExpected:
		if state.Integration.Attempt >= state.Integration.AttemptLimit {
			return directDecision(DirectDecisionEscalate, DirectCodeAttemptsExhausted)
		}
		return directDecision(DirectDecisionDispatch, "")
	case directdomain.ObservationDifferent:
		return directDecision(DirectDecisionEscalate, DirectCodeRemoteRefChanged)
	case directdomain.ObservationAbsent:
		return directDecision(DirectDecisionEscalate, DirectCodeRemoteRefAbsent)
	case directdomain.ObservationAmbiguous:
		return directDecision(DirectDecisionEscalate, string(observation.Code))
	default:
		return directDecision(DirectDecisionEscalate, DirectCodeRemoteAmbiguous)
	}
}
