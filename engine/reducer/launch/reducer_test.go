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
	if decision := Reduce(facts); decision.Kind != DecisionCommitWorkerVisibility {
		t.Fatalf("worker-visibility decision = %#v", decision)
	}

	facts.WorkerVisibilityDigest = "worker-visibility-1"
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

// A worker the owner cannot find from its root workspace must never start.
// Registration is therefore ordered before the agent-creation intent, and a
// registration lost after that intent exists parks instead of launching.
func TestReduceRefusesAgentCreationWithoutFrozenWorkerVisibility(t *testing.T) {
	facts := launchFacts()
	facts.Worktree = execution.Effect{ID: "worktree-intent", Kind: execution.EffectWorktreeCreate, Phase: execution.EffectComplete, AttemptLimit: 2, ExternalID: "worktree-1"}
	facts.HostView = execution.Effect{ID: "host-view-intent", Kind: execution.EffectHostViewCreate, Phase: execution.EffectComplete, AttemptLimit: 2, ExternalID: "paseo-workspace-1"}
	facts.Boundary = execution.Effect{ID: "boundary-intent", Kind: execution.EffectBoundaryMaterialize, Phase: execution.EffectComplete, AttemptLimit: 2, ExternalID: "boundary-1"}
	facts.PreparationReady = true

	if decision := Reduce(facts); decision.Kind != DecisionCommitWorkerVisibility {
		t.Fatalf("unregistered launch decision = %#v", decision)
	}

	facts.Agent = execution.Effect{ID: "agent-intent", Kind: execution.EffectAgentCreate, Phase: execution.EffectIntentRecorded, AttemptLimit: 2}
	decision := Reduce(facts)
	if decision.Kind != DecisionEscalate ||
		decision.Code != "worker_visibility_registration_missing" || decision.CleanupAuthorized {
		t.Fatalf("lost registration decision = %#v", decision)
	}
}

func TestReduceWaitsForTerminalEventsAndOrdersIdentityBeforeRealPrompt(t *testing.T) {
	facts := launchFacts()
	facts.Worktree = execution.Effect{ID: "worktree", Kind: execution.EffectWorktreeCreate, Phase: execution.EffectComplete, AttemptLimit: 2, ExternalID: "worktree-1"}
	facts.HostView = execution.Effect{ID: "workspace", Kind: execution.EffectHostViewCreate, Phase: execution.EffectComplete, AttemptLimit: 2, ExternalID: "workspace-1"}
	facts.Boundary = execution.Effect{ID: "boundary", Kind: execution.EffectBoundaryMaterialize, Phase: execution.EffectComplete, AttemptLimit: 2, ExternalID: "boundary-1"}
	facts.PreparationReady = true
	facts.WorkerVisibilityDigest = "visibility-1"
	facts.Agent = execution.Effect{
		ID: "bootstrap", Kind: execution.EffectAgentCreate, Phase: execution.EffectDispatching,
		Attempt: 1, AttemptLimit: 2, Observation: &execution.EffectObservation{
			ID: "bootstrap-running", EffectID: "bootstrap", Status: execution.ObservationOwnedPresent,
			ObservedAtMillis: 1_000, MaximumAgeMillis: 30_000,
		},
	}
	facts.Agent.Observation.FactHash = execution.EffectObservationHash(*facts.Agent.Observation)
	if decision := Reduce(facts); decision.Kind != DecisionWaitTerminalEvent || decision.EffectKind != execution.EffectAgentCreate {
		t.Fatalf("active bootstrap decision = %#v", decision)
	}
	facts.Agent = execution.Effect{ID: "bootstrap", Kind: execution.EffectAgentCreate, Phase: execution.EffectComplete, Attempt: 1, AttemptLimit: 2, ExternalID: "agent-1"}
	if decision := Reduce(facts); decision.Kind != DecisionCommitWorkerIdentity {
		t.Fatalf("post-bootstrap decision = %#v", decision)
	}
	facts.WorkerIdentityPersisted = true
	if decision := Reduce(facts); decision.Kind != DecisionCreateAgentPromptIntent {
		t.Fatalf("persisted-identity decision = %#v", decision)
	}
	facts.AgentPrompt = execution.Effect{
		ID: "real-prompt", Kind: execution.EffectAgentPrompt, Phase: execution.EffectDispatching,
		Attempt: 1, AttemptLimit: 2, Observation: &execution.EffectObservation{
			ID: "prompt-running", EffectID: "real-prompt", Status: execution.ObservationOwnedPresent,
			ObservedAtMillis: 1_000, MaximumAgeMillis: 30_000,
		},
	}
	facts.AgentPrompt.Observation.FactHash = execution.EffectObservationHash(*facts.AgentPrompt.Observation)
	if decision := Reduce(facts); decision.Kind != DecisionWaitTerminalEvent || decision.EffectKind != execution.EffectAgentPrompt {
		t.Fatalf("active real prompt decision = %#v", decision)
	}
	for status, code := range map[execution.ObservationStatus]string{
		execution.ObservationErrored:    "agent_terminal_error",
		execution.ObservationPermission: "agent_terminal_permission",
	} {
		changed := facts
		observation := *facts.AgentPrompt.Observation
		observation.Status = status
		observation.FactHash = execution.EffectObservationHash(observation)
		changed.AgentPrompt.Observation = &observation
		if decision := Reduce(changed); decision.Kind != DecisionEscalate || decision.Code != code {
			t.Fatalf("terminal %s decision = %#v", status, decision)
		}
	}
}
