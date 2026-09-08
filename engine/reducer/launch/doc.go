// SPDX-License-Identifier: Apache-2.0

// Package launch is the exclusive home of the pure, versioned launch decision
// reducer.
package launch

import "github.com/mcuadros/director-engine/domain/execution"

const SchemaVersion = "director.reducer.launch/v1"

// Facts is the closed input for one M1 launch reduction.
type Facts struct {
	SchemaVersion             string           `json:"schemaVersion"`
	EligibilityDecisionID     string           `json:"eligibilityDecisionId"`
	EligibilityCurrent        bool             `json:"eligibilityCurrent"`
	CapacityReserved          bool             `json:"capacityReserved"`
	BudgetReserved            bool             `json:"budgetReserved"`
	ImmutableRunReady         bool             `json:"immutableRunReady"`
	ExactRepositoryBinding    bool             `json:"exactRepositoryBinding"`
	LifecycleAdmissionDigest  string           `json:"lifecycleAdmissionDigest"`
	IsolationAdmissionDigest  string           `json:"isolationAdmissionDigest"`
	OperationalLimitsAdmitted bool             `json:"operationalLimitsAdmitted"`
	OperationalObservationID  string           `json:"operationalObservationId"`
	TaskStoreNowMillis        int64            `json:"taskStoreNowMillis"`
	SetupRequired             bool             `json:"setupRequired"`
	PreparationPlanID         string           `json:"preparationPlanId"`
	PreparationPlanValid      bool             `json:"preparationPlanValid"`
	PreparationBarrierHash    string           `json:"preparationBarrierHash"`
	PreparationReady          bool             `json:"preparationReady"`
	Worktree                  execution.Effect `json:"worktree"`
	HostView                  execution.Effect `json:"hostView"`
	Boundary                  execution.Effect `json:"boundary"`
	Setup                     execution.Effect `json:"setup"`
	Agent                     execution.Effect `json:"agent"`
}

// DecisionKind is the finite launch action vocabulary.
type DecisionKind string

const (
	DecisionObserve                  DecisionKind = "observe"
	DecisionDispatch                 DecisionKind = "dispatch"
	DecisionAdopt                    DecisionKind = "adopt"
	DecisionCommitPreparationReady   DecisionKind = "commit_preparation_ready"
	DecisionCreateAgentIntent        DecisionKind = "create_agent_intent"
	DecisionLaunched                 DecisionKind = "launched"
	DecisionObserveOperationalLimits DecisionKind = "observe_operational_limits"
	DecisionRetry                    DecisionKind = "retry"
	DecisionEscalate                 DecisionKind = "escalate"
)

// Decision contains one adapter command, durable transition, or refusal.
type Decision struct {
	SchemaVersion          string               `json:"schemaVersion"`
	Kind                   DecisionKind         `json:"kind"`
	EffectKind             execution.EffectKind `json:"effectKind,omitempty"`
	Code                   string               `json:"code,omitempty"`
	PreparationBarrierHash string               `json:"preparationBarrierHash,omitempty"`
	CleanupAuthorized      bool                 `json:"cleanupAuthorized"`
}

func needs(code string) Decision {
	return Decision{SchemaVersion: SchemaVersion, Kind: DecisionEscalate, Code: code}
}

func reduceEffect(effect execution.Effect, nowMillis int64) Decision {
	if effect.ID == "" || effect.AttemptLimit == 0 || effect.Kind == "" {
		return needs("launch_effect_intent_missing")
	}
	if effect.Phase == execution.EffectComplete {
		if effect.ExternalID == "" {
			return needs("launch_effect_identity_missing")
		}
		return Decision{}
	}
	if effect.Observation == nil {
		return Decision{SchemaVersion: SchemaVersion, Kind: DecisionObserve, EffectKind: effect.Kind}
	}
	if effect.Observation.EffectID != effect.ID || effect.Observation.ID == "" ||
		!execution.CurrentEffectObservation(*effect.Observation, nowMillis) {
		return needs("launch_observation_binding_invalid")
	}
	switch effect.Observation.Status {
	case execution.ObservationDesired:
		if effect.Observation.ExternalID == "" {
			return needs("launch_observation_identity_missing")
		}
		return Decision{SchemaVersion: SchemaVersion, Kind: DecisionAdopt, EffectKind: effect.Kind}
	case execution.ObservationAbsent:
		if effect.Phase == execution.EffectDispatching {
			return Decision{SchemaVersion: SchemaVersion, Kind: DecisionRetry, EffectKind: effect.Kind}
		}
		if effect.Attempt >= effect.AttemptLimit {
			return needs("launch_effect_attempts_exhausted")
		}
		return Decision{SchemaVersion: SchemaVersion, Kind: DecisionDispatch, EffectKind: effect.Kind}
	case execution.ObservationDifferent, execution.ObservationAmbiguous, execution.ObservationUnavailable:
		return needs("launch_effect_observation_unsafe")
	default:
		return needs("launch_effect_observation_invalid")
	}
}

// Reduce orders admitted launch effects without performing I/O.
func Reduce(facts Facts) Decision {
	if facts.SchemaVersion != SchemaVersion {
		return needs("launch_facts_version_mismatch")
	}
	if facts.EligibilityDecisionID == "" || !facts.EligibilityCurrent ||
		!facts.CapacityReserved || !facts.BudgetReserved || !facts.ImmutableRunReady ||
		!facts.ExactRepositoryBinding || facts.LifecycleAdmissionDigest == "" ||
		facts.IsolationAdmissionDigest == "" {
		return needs("launch_admission_fact_missing")
	}
	if facts.OperationalObservationID == "" {
		return Decision{SchemaVersion: SchemaVersion, Kind: DecisionObserveOperationalLimits}
	}
	if !facts.OperationalLimitsAdmitted {
		return needs("operational_limit_not_admitted")
	}
	if facts.PreparationPlanID == "" || !facts.PreparationPlanValid {
		return needs("preparation_plan_invalid")
	}
	for _, effect := range []execution.Effect{facts.Worktree, facts.HostView, facts.Boundary} {
		decision := reduceEffect(effect, facts.TaskStoreNowMillis)
		if decision.Kind != "" {
			return decision
		}
	}
	if facts.SetupRequired {
		decision := reduceEffect(facts.Setup, facts.TaskStoreNowMillis)
		if decision.Kind != "" {
			return decision
		}
	}
	if !facts.PreparationReady {
		if facts.PreparationBarrierHash == "" {
			return needs("preparation_outputs_incomplete")
		}
		return Decision{
			SchemaVersion: SchemaVersion, Kind: DecisionCommitPreparationReady,
			PreparationBarrierHash: facts.PreparationBarrierHash,
		}
	}
	if facts.PreparationBarrierHash == "" {
		return needs("preparation_barrier_missing")
	}
	if facts.Agent.ID == "" {
		return Decision{SchemaVersion: SchemaVersion, Kind: DecisionCreateAgentIntent}
	}
	if decision := reduceEffect(facts.Agent, facts.TaskStoreNowMillis); decision.Kind != "" {
		return decision
	}
	return Decision{SchemaVersion: SchemaVersion, Kind: DecisionLaunched}
}
