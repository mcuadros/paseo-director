// SPDX-License-Identifier: Apache-2.0

package launch

import (
	"testing"

	"github.com/mcuadros/director-engine/domain/execution"
)

func launchFacts() Facts {
	return Facts{
		SchemaVersion:             SchemaVersion,
		EligibilityDecisionID:     "eligibility-1",
		EligibilityCurrent:        true,
		CapacityReserved:          true,
		BudgetReserved:            true,
		ImmutableRunReady:         true,
		ExactRepositoryBinding:    true,
		LifecycleAdmissionDigest:  "lifecycle-1",
		IsolationAdmissionDigest:  "isolation-1",
		OperationalLimitsAdmitted: true,
		OperationalObservationID:  "limits-1",
		TaskStoreNowMillis:        1_001,
		PreparationPlanID:         "plan-1",
		PreparationPlanValid:      true,
		PreparationBarrierHash:    "barrier-1",
		Worktree:                  execution.Effect{ID: "worktree-intent", Kind: execution.EffectWorktreeCreate, Phase: execution.EffectIntentRecorded, AttemptLimit: 2},
	}
}

func TestReduceOrdersWorktreeHostViewBoundaryPreparationAndTopLevelAgent(t *testing.T) {
	facts := launchFacts()
	if decision := Reduce(facts); decision.Kind != DecisionObserve || decision.EffectKind != execution.EffectWorktreeCreate {
		t.Fatalf("initial decision = %#v", decision)
	}

	facts.Worktree.Phase = execution.EffectComplete
	facts.Worktree.ExternalID = "worktree-1"
	facts.HostView = execution.Effect{ID: "host-view-intent", Kind: execution.EffectHostViewCreate, Phase: execution.EffectIntentRecorded, AttemptLimit: 2}
	if decision := Reduce(facts); decision.EffectKind != execution.EffectHostViewCreate {
		t.Fatalf("host view decision = %#v", decision)
	}

	facts.HostView.Phase = execution.EffectComplete
	facts.HostView.ExternalID = "paseo-workspace-1"
	facts.Boundary = execution.Effect{ID: "boundary-intent", Kind: execution.EffectBoundaryMaterialize, Phase: execution.EffectIntentRecorded, AttemptLimit: 2}
	if decision := Reduce(facts); decision.EffectKind != execution.EffectBoundaryMaterialize {
		t.Fatalf("boundary decision = %#v", decision)
	}

	facts.Boundary.Phase = execution.EffectComplete
	facts.Boundary.ExternalID = "boundary-1"
	if decision := Reduce(facts); decision.Kind != DecisionCommitPreparationReady {
		t.Fatalf("preparation decision = %#v", decision)
	}

	facts.PreparationReady = true
	facts.PreparationBarrierHash = "barrier-1"
	if decision := Reduce(facts); decision.Kind != DecisionCreateAgentIntent {
		t.Fatalf("agent-intent decision = %#v", decision)
	}

	facts.Agent = execution.Effect{ID: "agent-intent", Kind: execution.EffectAgentCreate, Phase: execution.EffectIntentRecorded, AttemptLimit: 2}
	if decision := Reduce(facts); decision.Kind != DecisionObserve || decision.EffectKind != execution.EffectAgentCreate {
		t.Fatalf("agent decision = %#v", decision)
	}
}

func TestReduceParksBeforeWorkspaceWhenAdmissionFactsAreMissing(t *testing.T) {
	for name, mutate := range map[string]func(*Facts){
		"lifecycle":  func(facts *Facts) { facts.LifecycleAdmissionDigest = "" },
		"isolation":  func(facts *Facts) { facts.IsolationAdmissionDigest = "" },
		"limits":     func(facts *Facts) { facts.OperationalLimitsAdmitted = false },
		"repository": func(facts *Facts) { facts.ExactRepositoryBinding = false },
	} {
		t.Run(name, func(t *testing.T) {
			facts := launchFacts()
			mutate(&facts)
			decision := Reduce(facts)
			if decision.Kind != DecisionEscalate || decision.CleanupAuthorized {
				t.Fatalf("launch admission = %#v", decision)
			}
		})
	}
	facts := launchFacts()
	facts.OperationalObservationID = ""
	if decision := Reduce(facts); decision.Kind != DecisionObserveOperationalLimits {
		t.Fatalf("missing observation did not request a fresh read: %#v", decision)
	}
}
