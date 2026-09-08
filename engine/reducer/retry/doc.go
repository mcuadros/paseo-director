// SPDX-License-Identifier: Apache-2.0

// Package retry is the exclusive home of the pure, versioned retry decision
// reducer.
package retry

import "github.com/mcuadros/director-engine/domain/execution"

const SchemaVersion = "director.reducer.retry/v1"

// Facts is the closed retry input for one previously dispatched M1 effect.
type Facts struct {
	SchemaVersion         string                      `json:"schemaVersion"`
	Effect                execution.Effect            `json:"effect"`
	Observation           execution.EffectObservation `json:"observation"`
	BindingUnchanged      bool                        `json:"bindingUnchanged"`
	PriorDispatcherAbsent bool                        `json:"priorDispatcherAbsent"`
	BudgetAvailable       bool                        `json:"budgetAvailable"`
	TaskStoreNowMillis    int64                       `json:"taskStoreNowMillis"`
}

// DecisionKind is the complete M1 retry result vocabulary.
type DecisionKind string

const (
	DecisionDispatch DecisionKind = "dispatch"
	DecisionAdopt    DecisionKind = "adopt"
	DecisionEscalate DecisionKind = "escalate"
)

// Decision authorizes one new attempt, adopts observed completion, or requests
// typed escalation. It never changes provider, scope, or binding.
type Decision struct {
	SchemaVersion     string       `json:"schemaVersion"`
	Kind              DecisionKind `json:"kind"`
	Code              string       `json:"code,omitempty"`
	CleanupAuthorized bool         `json:"cleanupAuthorized"`
}

func escalation(code string) Decision {
	return Decision{SchemaVersion: SchemaVersion, Kind: DecisionEscalate, Code: code}
}

func uniqueCreate(kind execution.EffectKind) bool {
	switch kind {
	case execution.EffectWorktreeCreate, execution.EffectHostViewCreate,
		execution.EffectBoundaryMaterialize, execution.EffectSetupRun,
		execution.EffectAgentCreate:
		return true
	default:
		return false
	}
}

// Reduce applies the ADR-0015 class-specific possible-handoff rules.
func Reduce(facts Facts) Decision {
	if facts.SchemaVersion != SchemaVersion || facts.Effect.ID == "" ||
		facts.Effect.Phase != execution.EffectDispatching || facts.Observation.ID == "" ||
		facts.Observation.EffectID != facts.Effect.ID ||
		!execution.CurrentEffectObservation(facts.Observation, facts.TaskStoreNowMillis) {
		return escalation("retry_facts_invalid")
	}
	if !facts.BindingUnchanged || !facts.BudgetAvailable {
		return escalation("retry_binding_or_budget_unavailable")
	}
	if facts.Observation.Status == execution.ObservationDesired {
		return Decision{SchemaVersion: SchemaVersion, Kind: DecisionAdopt}
	}
	if facts.Effect.Attempt >= facts.Effect.AttemptLimit {
		return escalation("effect_attempts_exhausted")
	}
	if facts.Effect.Kind == execution.EffectWorktreeRemove {
		return escalation("destructive_result_ambiguous")
	}
	if !facts.PriorDispatcherAbsent {
		return escalation("prior_dispatcher_not_absent")
	}
	if uniqueCreate(facts.Effect.Kind) && facts.Observation.Status == execution.ObservationAbsent {
		return Decision{SchemaVersion: SchemaVersion, Kind: DecisionDispatch}
	}
	if (facts.Effect.Kind == execution.EffectAgentArchive || facts.Effect.Kind == execution.EffectHostViewArchive) &&
		facts.Observation.Status == execution.ObservationOwnedPresent {
		return Decision{SchemaVersion: SchemaVersion, Kind: DecisionDispatch}
	}
	return escalation("effect_result_not_retryable")
}
