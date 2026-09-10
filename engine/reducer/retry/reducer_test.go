// SPDX-License-Identifier: Apache-2.0

package retry

import (
	"testing"

	"github.com/mcuadros/director-engine/domain/execution"
)

func TestReduceAppliesClassSpecificUnknownResultRules(t *testing.T) {
	base := Facts{
		SchemaVersion: SchemaVersion, BindingUnchanged: true,
		PriorDispatcherAbsent: true, BudgetAvailable: true,
		TaskStoreNowMillis: 1_001,
		Effect: execution.Effect{
			ID: "effect-1", Phase: execution.EffectDispatching,
			Attempt: 1, AttemptLimit: 2,
		},
		Observation: execution.EffectObservation{
			ID: "observation-1", EffectID: "effect-1",
			Status:           execution.ObservationAbsent,
			ObservedAtMillis: 1_000, MaximumAgeMillis: 30_000,
		},
	}
	base.Observation.FactHash = execution.EffectObservationHash(base.Observation)

	unique := base
	unique.Effect.Kind = execution.EffectWorktreeCreate
	if decision := Reduce(unique); decision.Kind != DecisionDispatch {
		t.Fatalf("unique-create absence = %#v", decision)
	}

	closeEffect := base
	closeEffect.Effect.Kind = execution.EffectAgentArchive
	closeEffect.Observation.Status = execution.ObservationOwnedPresent
	closeEffect.Observation.FactHash = execution.EffectObservationHash(closeEffect.Observation)
	if decision := Reduce(closeEffect); decision.Kind != DecisionDispatch {
		t.Fatalf("idempotent-close owned target = %#v", decision)
	}

	destructive := base
	destructive.Effect.Kind = execution.EffectWorktreeRemove
	destructive.Observation.Status = execution.ObservationOwnedPresent
	destructive.Observation.FactHash = execution.EffectObservationHash(destructive.Observation)
	if decision := Reduce(destructive); decision.Kind != DecisionEscalate {
		t.Fatalf("destructive present target = %#v", decision)
	}
	destructive.Observation.Status = execution.ObservationDesired
	destructive.Observation.FactHash = execution.EffectObservationHash(destructive.Observation)
	if decision := Reduce(destructive); decision.Kind != DecisionAdopt {
		t.Fatalf("destructive absence = %#v", decision)
	}
}

func TestReduceRequiresFreshBoundFactsAndFiniteAttempts(t *testing.T) {
	facts := Facts{
		SchemaVersion: SchemaVersion, BindingUnchanged: true,
		PriorDispatcherAbsent: true, BudgetAvailable: true,
		TaskStoreNowMillis: 1_001,
		Effect: execution.Effect{
			ID: "effect-1", Kind: execution.EffectAgentCreate,
			Phase: execution.EffectDispatching, Attempt: 2, AttemptLimit: 2,
		},
		Observation: execution.EffectObservation{
			ID: "observation-1", EffectID: "effect-1",
			Status:           execution.ObservationAbsent,
			ObservedAtMillis: 1_000, MaximumAgeMillis: 30_000,
		},
	}
	facts.Observation.FactHash = execution.EffectObservationHash(facts.Observation)
	if decision := Reduce(facts); decision.Kind != DecisionEscalate || decision.Code != "effect_attempts_exhausted" {
		t.Fatalf("exhausted retry = %#v", decision)
	}
	facts.Effect.Attempt = 1
	facts.Observation.EffectID = "other"
	facts.Observation.FactHash = execution.EffectObservationHash(facts.Observation)
	if decision := Reduce(facts); decision.Kind != DecisionEscalate || decision.CleanupAuthorized {
		t.Fatalf("misbound retry observation = %#v", decision)
	}
}

func TestReduceNeverRepeatsARealPromptAfterPossibleHandoff(t *testing.T) {
	facts := Facts{
		SchemaVersion: SchemaVersion, BindingUnchanged: true,
		PriorDispatcherAbsent: true, BudgetAvailable: true, TaskStoreNowMillis: 1_001,
		Effect: execution.Effect{
			ID: "prompt-1", Kind: execution.EffectAgentPrompt,
			Phase: execution.EffectDispatching, Attempt: 1, AttemptLimit: 2,
		},
		Observation: execution.EffectObservation{
			ID: "prompt-observation", EffectID: "prompt-1", Status: execution.ObservationAbsent,
			ObservedAtMillis: 1_000, MaximumAgeMillis: 30_000,
		},
	}
	facts.Observation.FactHash = execution.EffectObservationHash(facts.Observation)
	decision := Reduce(facts)
	if decision.Kind != DecisionEscalate || decision.Code != "nonrepeatable_prompt_result_ambiguous" {
		t.Fatalf("lost real prompt = %#v", decision)
	}
}
