// SPDX-License-Identifier: Apache-2.0

// Package closure is the exclusive home of the pure, versioned closure
// decision reducer.
package closure

import (
	domaincleanup "github.com/mcuadros/director-engine/domain/cleanup"
	"github.com/mcuadros/director-engine/domain/execution"
)

const SchemaVersion = "director.reducer.closure/v1"

// Facts is the closed terminal-cleanup input for the M1 fake execution rung.
// FakeTerminalRung explicitly prevents this skeleton from being mistaken for
// the full Validation/Review/delivery Task closure owned by M4.
type Facts struct {
	SchemaVersion             string               `json:"schemaVersion"`
	FakeTerminalRung          bool                 `json:"fakeTerminalRung"`
	DeliveryTerminalRung      bool                 `json:"deliveryTerminalRung"`
	CleanupBindingCurrent     bool                 `json:"cleanupBindingCurrent"`
	Cleanup                   *domaincleanup.State `json:"cleanup,omitempty"`
	CandidateCurrent          bool                 `json:"candidateCurrent"`
	CriteriaClaimsSatisfied   bool                 `json:"criteriaClaimsSatisfied"`
	ExactOwnership            bool                 `json:"exactOwnership"`
	OperationalLimitsAdmitted bool                 `json:"operationalLimitsAdmitted"`
	OperationalObservationID  string               `json:"operationalObservationId"`
	TaskStoreNowMillis        int64                `json:"taskStoreNowMillis"`
	OperationalNeedCode       execution.NeedCode   `json:"operationalNeedCode,omitempty"`
	AgentArchive              execution.Effect     `json:"agentArchive"`
	HostViewArchive           execution.Effect     `json:"hostViewArchive"`
	WorktreeRemove            execution.Effect     `json:"worktreeRemove"`
}

// DecisionKind is the closed cleanup action vocabulary.
type DecisionKind string

const (
	DecisionObserve                  DecisionKind = "observe"
	DecisionDispatch                 DecisionKind = "dispatch"
	DecisionAdopt                    DecisionKind = "adopt"
	DecisionRetry                    DecisionKind = "retry"
	DecisionCreateAgentIntent        DecisionKind = "create_agent_archive_intent"
	DecisionCreateHostViewIntent     DecisionKind = "create_host_view_archive_intent"
	DecisionCreateWorktreeIntent     DecisionKind = "create_worktree_remove_intent"
	DecisionTerminal                 DecisionKind = "terminal"
	DecisionObserveOperationalLimits DecisionKind = "observe_operational_limits"
	DecisionEscalate                 DecisionKind = "escalate"
)

// Decision authorizes at most one observation, effect, or durable transition.
type Decision struct {
	SchemaVersion     string               `json:"schemaVersion"`
	Kind              DecisionKind         `json:"kind"`
	EffectKind        execution.EffectKind `json:"effectKind,omitempty"`
	Code              string               `json:"code,omitempty"`
	CleanupAuthorized bool                 `json:"cleanupAuthorized"`
}

func needs(code string) Decision {
	return Decision{SchemaVersion: SchemaVersion, Kind: DecisionEscalate, Code: code}
}

func reduceEffect(effect execution.Effect, nowMillis int64) Decision {
	if effect.ID == "" || effect.Kind == "" || effect.AttemptLimit == 0 {
		return needs("cleanup_effect_intent_missing")
	}
	if effect.Phase == execution.EffectComplete {
		if effect.ExternalID == "" {
			return needs("cleanup_effect_identity_missing")
		}
		return Decision{}
	}
	if effect.Observation == nil {
		return Decision{SchemaVersion: SchemaVersion, Kind: DecisionObserve, EffectKind: effect.Kind}
	}
	if effect.Observation.ID == "" || effect.Observation.EffectID != effect.ID ||
		!execution.CurrentEffectObservation(*effect.Observation, nowMillis) {
		return needs("cleanup_observation_binding_invalid")
	}
	switch effect.Observation.Status {
	case execution.ObservationDesired:
		if effect.Observation.ExternalID == "" {
			return needs("cleanup_observation_identity_missing")
		}
		return Decision{SchemaVersion: SchemaVersion, Kind: DecisionAdopt, EffectKind: effect.Kind, CleanupAuthorized: true}
	case execution.ObservationOwnedPresent:
		if effect.Phase == execution.EffectDispatching {
			return Decision{SchemaVersion: SchemaVersion, Kind: DecisionRetry, EffectKind: effect.Kind}
		}
		if effect.Attempt >= effect.AttemptLimit {
			return needs("cleanup_effect_attempts_exhausted")
		}
		return Decision{SchemaVersion: SchemaVersion, Kind: DecisionDispatch, EffectKind: effect.Kind, CleanupAuthorized: true}
	default:
		return needs("cleanup_observation_unsafe")
	}
}

// Reduce permits cleanup only after Candidate and exact ownership facts pass.
func Reduce(facts Facts) Decision {
	if facts.SchemaVersion != SchemaVersion {
		return needs("closure_facts_version_mismatch")
	}
	if facts.OperationalObservationID == "" {
		return Decision{SchemaVersion: SchemaVersion, Kind: DecisionObserveOperationalLimits}
	}
	if !facts.OperationalLimitsAdmitted {
		code := string(facts.OperationalNeedCode)
		if code == "" {
			code = "operational_limit_fact_missing"
		}
		return needs(code)
	}
	if facts.DeliveryTerminalRung {
		if !facts.CandidateCurrent || !facts.CriteriaClaimsSatisfied || !facts.ExactOwnership || !facts.CleanupBindingCurrent ||
			facts.Cleanup == nil || !domaincleanup.ValidState(*facts.Cleanup) ||
			(facts.Cleanup.Phase != domaincleanup.PhaseComplete && facts.Cleanup.Phase != domaincleanup.PhaseRetained) {
			return needs("delivery_cleanup_facts_missing")
		}
		return Decision{SchemaVersion: SchemaVersion, Kind: DecisionTerminal}
	}
	if !facts.FakeTerminalRung || !facts.CandidateCurrent || !facts.CriteriaClaimsSatisfied || !facts.ExactOwnership {
		return needs("terminal_cleanup_facts_missing")
	}
	if facts.AgentArchive.ID == "" {
		return Decision{SchemaVersion: SchemaVersion, Kind: DecisionCreateAgentIntent, CleanupAuthorized: true}
	}
	if decision := reduceEffect(facts.AgentArchive, facts.TaskStoreNowMillis); decision.Kind != "" {
		return decision
	}
	if facts.HostViewArchive.ID == "" {
		return Decision{SchemaVersion: SchemaVersion, Kind: DecisionCreateHostViewIntent, CleanupAuthorized: true}
	}
	if decision := reduceEffect(facts.HostViewArchive, facts.TaskStoreNowMillis); decision.Kind != "" {
		return decision
	}
	if facts.WorktreeRemove.ID == "" {
		return Decision{SchemaVersion: SchemaVersion, Kind: DecisionCreateWorktreeIntent, CleanupAuthorized: true}
	}
	if decision := reduceEffect(facts.WorktreeRemove, facts.TaskStoreNowMillis); decision.Kind != "" {
		return decision
	}
	return Decision{SchemaVersion: SchemaVersion, Kind: DecisionTerminal}
}
