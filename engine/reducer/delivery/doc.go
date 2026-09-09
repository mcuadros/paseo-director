// SPDX-License-Identifier: Apache-2.0

// Package delivery owns the pure Candidate fast-path ordering decision. It
// never publishes, launches, reviews, merges, or cleans by itself.
package delivery

import "regexp"

const SchemaVersion = "director.reducer.delivery/v1"

var shaPattern = regexp.MustCompile(`^[0-9a-f]{40}$`)

// Phase is the closed durable state of one delivery obligation.
type Phase string

const (
	PhaseAbsent      Phase = "absent"
	PhaseIntent      Phase = "intent_recorded"
	PhaseDispatching Phase = "dispatching"
	PhaseComplete    Phase = "complete"
)

// Obligation binds a durable effect phase to the exact Candidate it serves.
type Obligation struct {
	Phase        Phase  `json:"phase"`
	CandidateSHA string `json:"candidateSha,omitempty"`
}

// Facts contains only durable admission and effect facts. Validation and
// Reviewer verdicts are deliberately outside this ordering reducer.
type Facts struct {
	SchemaVersion     string     `json:"schemaVersion"`
	TaskBlocked       bool       `json:"taskBlocked"`
	CandidateAdmitted bool       `json:"candidateAdmitted"`
	CandidateSHA      string     `json:"candidateSha,omitempty"`
	BaseSHA           string     `json:"baseSha,omitempty"`
	Draft             Obligation `json:"draft"`
	RemoteCI          Obligation `json:"remoteCi"`
	Review            Obligation `json:"review"`
}

// EffectKind is the closed set of effects that can leave this reducer.
type EffectKind string

const (
	EffectPublishDraft      EffectKind = "publish_draft"
	EffectObserveRemoteCI   EffectKind = "observe_remote_ci"
	EffectIndependentReview EffectKind = "independent_review"
)

// DecisionKind is the closed fast-path decision vocabulary.
type DecisionKind string

const (
	DecisionWaitCandidate   DecisionKind = "wait_candidate"
	DecisionPublishDraft    DecisionKind = "publish_draft"
	DecisionReconcileDraft  DecisionKind = "reconcile_draft"
	DecisionDispatchSibling DecisionKind = "dispatch_sibling_obligations"
	DecisionWaitSibling     DecisionKind = "wait_sibling_obligations"
	DecisionEvaluateGates   DecisionKind = "evaluate_gates"
	DecisionEscalate        DecisionKind = "escalate"
)

const (
	CodeFactsVersionMismatch = "delivery_facts_version_mismatch"
	CodeTaskBlocked          = "delivery_task_blocked"
	CodeCandidateInvalid     = "delivery_candidate_invalid"
	CodePhaseInvalid         = "delivery_phase_invalid"
	CodeBindingMismatch      = "delivery_candidate_binding_mismatch"
)

// Decision may dispatch sibling obligations together, but grants no merge or
// cleanup authority. Effects have deterministic order for stable persistence.
type Decision struct {
	SchemaVersion     string       `json:"schemaVersion"`
	Kind              DecisionKind `json:"kind"`
	Code              string       `json:"code,omitempty"`
	Effects           []EffectKind `json:"effects,omitempty"`
	MergeAuthorized   bool         `json:"mergeAuthorized"`
	CleanupAuthorized bool         `json:"cleanupAuthorized"`
}

func escalate(code string) Decision {
	return Decision{SchemaVersion: SchemaVersion, Kind: DecisionEscalate, Code: code}
}

func validPhase(phase Phase) bool {
	switch phase {
	case PhaseAbsent, PhaseIntent, PhaseDispatching, PhaseComplete:
		return true
	default:
		return false
	}
}

func validBinding(obligation Obligation, candidate string) bool {
	if obligation.Phase == PhaseAbsent {
		return obligation.CandidateSHA == ""
	}
	return obligation.CandidateSHA == candidate
}

// Reduce enforces the owner-approved sequence: admit Candidate, publish one
// draft, then persist the remote-CI and independent-Review sibling intents in
// one decision. Recovery emits only a missing sibling and never repeats an
// already recorded CI or Review intent.
func Reduce(facts Facts) Decision {
	if facts.SchemaVersion != SchemaVersion {
		return escalate(CodeFactsVersionMismatch)
	}
	if facts.TaskBlocked {
		return escalate(CodeTaskBlocked)
	}
	if !facts.CandidateAdmitted {
		return Decision{SchemaVersion: SchemaVersion, Kind: DecisionWaitCandidate}
	}
	if !shaPattern.MatchString(facts.CandidateSHA) || !shaPattern.MatchString(facts.BaseSHA) {
		return escalate(CodeCandidateInvalid)
	}
	for _, obligation := range []Obligation{facts.Draft, facts.RemoteCI, facts.Review} {
		if !validPhase(obligation.Phase) {
			return escalate(CodePhaseInvalid)
		}
		if !validBinding(obligation, facts.CandidateSHA) {
			return escalate(CodeBindingMismatch)
		}
	}
	if facts.Draft.Phase == PhaseAbsent {
		return Decision{
			SchemaVersion: SchemaVersion, Kind: DecisionPublishDraft,
			Effects: []EffectKind{EffectPublishDraft},
		}
	}
	if facts.Draft.Phase != PhaseComplete {
		return Decision{SchemaVersion: SchemaVersion, Kind: DecisionReconcileDraft}
	}

	effects := make([]EffectKind, 0, 2)
	if facts.RemoteCI.Phase == PhaseAbsent {
		effects = append(effects, EffectObserveRemoteCI)
	}
	if facts.Review.Phase == PhaseAbsent {
		effects = append(effects, EffectIndependentReview)
	}
	if len(effects) > 0 {
		return Decision{
			SchemaVersion: SchemaVersion, Kind: DecisionDispatchSibling,
			Effects: effects,
		}
	}
	if facts.RemoteCI.Phase != PhaseComplete || facts.Review.Phase != PhaseComplete {
		return Decision{SchemaVersion: SchemaVersion, Kind: DecisionWaitSibling}
	}
	return Decision{SchemaVersion: SchemaVersion, Kind: DecisionEvaluateGates}
}
