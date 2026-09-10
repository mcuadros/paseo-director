// SPDX-License-Identifier: Apache-2.0

package control

import (
	"testing"

	"github.com/mcuadros/director-engine/domain/execution"
	oracle "github.com/mcuadros/director-engine/internal/testkit/executionoracle"
)

func intent(kind execution.ControlKind) execution.ControlIntent {
	value := execution.ControlIntent{
		RequestID: "request-control-0001", Kind: kind, ProjectID: "project",
		ActorKind: execution.ControlActorHuman, ActorID: "owner", ActorSessionID: "session",
		Source: "server", Authenticated: true, RequestedAtMillis: 1_000,
	}
	if kind == execution.ControlCancelTask {
		value.TaskID, value.RunID = "task", "run"
	}
	if kind == execution.ControlEmergencyStop {
		value.ConfirmationID = "emergency-confirmation-current"
	}
	value.ID = execution.ControlIntentID(value)
	return value
}

func agent(role execution.ControlledAgentRole, id string) execution.ControlledAgent {
	parent := ""
	if role == execution.ControlledHelper {
		parent = "worker"
	}
	return execution.ControlledAgent{Identity: execution.ControlledAgentIdentity{
		ID: id, Role: role, ParentID: parent, WorkspaceID: "host-workspace",
		Title: id, WorktreePath: "/tmp/control-worktree",
	}}
}

func facts(kind execution.ControlKind) Facts {
	value := execution.RunControl{
		SchemaVersion: execution.RunControlSchemaVersion, Intent: intent(kind), ProjectGeneration: 1,
		Phase: execution.ControlIntentRecorded, RelaunchBlocked: true, ExplanationCode: "control",
	}
	if kind == execution.ControlCancelTask || kind == execution.ControlEmergencyStop {
		value.Recovery = execution.RecoveryIntent{Mode: execution.RecoveryRetain}
	}
	return Facts{
		SchemaVersion: SchemaVersion, ProjectState: "paused", ProjectGeneration: 1,
		Control: value,
	}
}

func TestPauseWaitsForSafeBoundaryAndConformsToExecutionOracle(t *testing.T) {
	input := facts(execution.ControlPauseProject)
	input.Control.Targets = []execution.ControlledAgent{agent(execution.ControlledTaskAgent, "worker")}
	input.Control.TargetSetSHA256 = execution.ControlledAgentSetSHA256(input.Control.Targets)
	input.TrackedAgentsSHA256 = input.Control.TargetSetSHA256
	decision := Reduce(input)
	if decision.Kind != DecisionCreateBoundaryIntent {
		t.Fatalf("create boundary = %#v", decision)
	}
	input.Control.Targets[0].Boundary = execution.Effect{
		ID: "effect-boundary", Kind: execution.EffectControlAgentBoundary,
		Phase: execution.EffectIntentRecorded, AttemptLimit: 1,
		Observation: &execution.EffectObservation{Status: execution.ObservationOwnedPresent},
	}
	decision = Reduce(input)
	if decision.Kind != DecisionWaitSafeBoundary || decision.ReasonCode != ReasonPausedSafeBoundary {
		t.Fatalf("active pause = %#v", decision)
	}

	oracleInput := oracle.Baseline()
	oracleInput.Workspace.State = oracle.WorkspaceReady
	oracleInput.Control.Kind = oracle.ControlPause
	oracleInput.Agents = []oracle.AgentFact{{
		ID: "worker", Role: oracle.RoleWorker, RunID: oracleInput.Run.Scope.RunID,
		WorkspaceID: oracleInput.Run.Scope.WorkspaceID, ProfileRevision: oracleInput.Run.ProfileRevision,
		State: oracle.AgentRunning, BootstrapDone: true, Prompt: oracle.PromptSent,
	}}
	oracleDecision := oracle.Evaluate(oracleInput)
	if oracleDecision.Reason != oracle.ReasonPaused || len(oracleDecision.Actions) != 1 ||
		oracleDecision.Actions[0].Kind != oracle.ActionAwaitSafeBoundary {
		t.Fatalf("oracle pause = %#v", oracleDecision)
	}
}

func TestContainmentIsHelperReviewerWorkerThenRecovery(t *testing.T) {
	input := facts(execution.ControlEmergencyStop)
	input.Control.Targets = []execution.ControlledAgent{
		agent(execution.ControlledTaskAgent, "worker"),
		agent(execution.ControlledReviewer, "reviewer"),
		agent(execution.ControlledHelper, "helper-b"),
		agent(execution.ControlledHelper, "helper-a"),
	}
	input.Control.TargetSetSHA256 = execution.ControlledAgentSetSHA256(input.Control.Targets)
	input.TrackedAgentsSHA256 = input.Control.TargetSetSHA256
	want := []string{"helper-a", "helper-b", "reviewer", "worker"}
	for _, id := range want {
		decision := Reduce(input)
		if decision.Kind != DecisionCreateArchiveIntent ||
			input.Control.Targets[decision.TargetIndex].Identity.ID != id {
			t.Fatalf("next containment target = %#v", decision)
		}
		target := &input.Control.Targets[decision.TargetIndex]
		target.Archive = execution.Effect{
			ID: "archive-" + id, Kind: execution.EffectControlAgentArchive,
			Phase: execution.EffectComplete, AttemptLimit: 2,
		}
		target.Archived, target.ProcessAbsent = true, true
	}
	if decision := Reduce(input); decision.Kind != DecisionPreserveRetained {
		t.Fatalf("recovery follows containment = %#v", decision)
	}
}

func TestCancelAndEmergencyPrecedeBudgetInIntegratedOracle(t *testing.T) {
	for _, test := range []struct {
		kind   oracle.ControlKind
		reason oracle.Reason
	}{
		{oracle.ControlCancel, oracle.ReasonCancelled},
		{oracle.ControlEmergencyStop, oracle.ReasonEmergencyStop},
	} {
		snapshot := oracle.Baseline()
		snapshot.Workspace.State = oracle.WorkspaceReady
		snapshot.Control.Kind = test.kind
		snapshot.Control.HumanConfirmed = test.kind == oracle.ControlEmergencyStop
		snapshot.Usage.Time.Consumed = snapshot.Usage.Time.Limit
		decision := oracle.Evaluate(snapshot)
		if decision.Reason != test.reason {
			t.Fatalf("control %d lost precedence: %#v", test.kind, decision)
		}
	}
}

func TestResumeRequiresReconciliationAndMutationGuardsFailClosed(t *testing.T) {
	input := facts(execution.ControlResumeProject)
	decision := Reduce(input)
	if decision.Kind != DecisionReconcileResume {
		t.Fatalf("resume = %#v", decision)
	}
	input.ResumeReconciled = true
	if decision = Reduce(input); decision.Kind != DecisionCompleteResume {
		t.Fatalf("reconciled resume = %#v", decision)
	}
	mutations := []func(*Facts){
		func(value *Facts) { value.SchemaVersion = "changed" },
		func(value *Facts) { value.ProjectGeneration++ },
		func(value *Facts) { value.Control.Intent.ProjectID = "other" },
		func(value *Facts) {
			value.Control.Targets = []execution.ControlledAgent{agent(execution.ControlledTaskAgent, "worker")}
			value.Control.TargetSetSHA256 = execution.ControlledAgentSetSHA256(value.Control.Targets)
			value.TrackedAgentsSHA256 = "changed"
		},
	}
	for index, mutate := range mutations {
		mutant := input
		mutant.Control = input.Control
		mutate(&mutant)
		if got := Reduce(mutant); got.Kind != DecisionEscalate {
			t.Fatalf("mutant %d = %#v", index, got)
		}
	}
}
