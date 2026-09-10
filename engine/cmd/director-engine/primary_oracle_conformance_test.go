// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"testing"
	"time"

	"github.com/mcuadros/director-engine/adapters/fake"
	executionapp "github.com/mcuadros/director-engine/application/execution"
	"github.com/mcuadros/director-engine/domain"
	domainconfig "github.com/mcuadros/director-engine/domain/configuration"
	"github.com/mcuadros/director-engine/domain/execution"
	executionoracle "github.com/mcuadros/director-engine/internal/testkit/executionoracle"
	"github.com/mcuadros/director-engine/ports/host"
	"github.com/mcuadros/director-engine/reducer/eligibility"
)

func productionOracleSnapshot(run domain.Run) executionoracle.Snapshot {
	scope := executionoracle.Scope{
		ProjectID: run.Execution.Scope.ProjectID, TaskID: run.Execution.Scope.TaskID,
		RunID: run.Execution.Scope.RunID, WorkspaceID: run.Execution.Scope.WorkspaceID,
	}
	snapshot := executionoracle.Baseline()
	snapshot.Run.Scope = scope
	snapshot.Run.OrganizerRevision = run.Execution.PrimarySession.OrganizerRevision
	snapshot.Run.ProfileRevision = run.Execution.EffectiveProfilesSHA256
	snapshot.Run.PreparationReady = true
	snapshot.Run.RootWorkerVisible = true
	snapshot.Organizer.ProjectID = scope.ProjectID
	snapshot.Organizer.Revision = snapshot.Run.OrganizerRevision
	snapshot.Workspace = executionoracle.WorkspaceFact{
		ID: scope.WorkspaceID, RunID: scope.RunID, State: executionoracle.WorkspaceAbsent, Owned: true,
	}
	snapshot.Lease = executionoracle.LeaseFact{
		ProjectID: scope.ProjectID, Holder: run.Execution.LeaseBinding.HolderInstance,
		Epoch: run.Execution.LeaseBinding.Epoch, State: executionoracle.LeaseCurrent, DispatchAllowed: true,
	}
	snapshot.Provider.ProfileRevision = snapshot.Run.ProfileRevision
	switch run.Execution.PrimarySession.Provider {
	case domainconfig.ProviderClaudeCode:
		snapshot.Provider.Kind = executionoracle.ProviderClaude
	case domainconfig.ProviderOpenCode:
		snapshot.Provider.Kind = executionoracle.ProviderOpenCode
	default:
		snapshot.Provider.Kind = executionoracle.ProviderCodex
	}
	snapshot.Control.RunID = scope.RunID
	snapshot.Failure.RunID = scope.RunID
	snapshot.HelperRequest.RunID = scope.RunID
	snapshot.HelperRequest.WorkspaceID = scope.WorkspaceID
	for index := range snapshot.MCP.Bindings {
		binding := &snapshot.MCP.Bindings[index]
		binding.ProjectID = scope.ProjectID
		if binding.Role != executionoracle.RoleOrganizer {
			binding.TaskID = scope.TaskID
			binding.RunID = scope.RunID
			binding.WorkspaceID = scope.WorkspaceID
		}
	}
	return snapshot
}

func requireOracleAction(t *testing.T, snapshot executionoracle.Snapshot, kind executionoracle.ActionKind) executionoracle.Action {
	t.Helper()
	decision := executionoracle.Evaluate(snapshot)
	if len(decision.Actions) != 1 || decision.Actions[0].Kind != kind || decision.Actions[0].Scope != snapshot.Run.Scope {
		t.Fatalf("oracle decision = %#v, want one bound action %d", decision, kind)
	}
	return decision.Actions[0]
}

func TestPrimaryLifecycleConformsToExecutionOracleBootstrapAndPromptSequence(t *testing.T) {
	fixture := startVerticalDolt(t)
	store := openVerticalStore(t, fixture)
	source, base := initializeRepository(t, "primary-oracle")
	project, task := createVerticalRecords(t, store, "primary-oracle", source)
	worktree := source + "-worktree"
	scope := execution.Scope{
		ProjectID: project.ID, WorkspaceID: task.WorkspaceIDs[0],
		TaskID: task.ID, RunID: "run-primary-oracle",
	}
	environment := fake.NewEnvironment(fake.Options{
		SourcePath: source, WorktreePath: worktree, Branch: "task/" + task.ID,
		BaseSHA: base, Operational: operationalObservation("primary-oracle"),
		LoseEveryMutationResponse: true,
	})
	t.Cleanup(environment.RemoveFixture)
	controller := executionapp.NewController(store, environment, environment, environment)
	started, err := controller.Start(
		context.Background(),
		startCommand(t, task, scope, source, worktree, base, eligibilityFacts(scope, execution.LifecycleSurfaces{})),
	)
	if err != nil || started.Decision.Kind != eligibility.DecisionEligible {
		t.Fatalf("start = %#v, %v", started, err)
	}
	run, err := store.Run(context.Background(), scope.RunID)
	if err != nil {
		t.Fatal(err)
	}
	oracle := productionOracleSnapshot(run)
	requireOracleAction(t, oracle, executionoracle.ActionCreateWorkspace)

	run = runSteps(t, store, environment, scope.RunID, func(current domain.Run) bool {
		return current.Execution.Worktree.Phase == execution.EffectComplete &&
			current.Execution.HostView.Phase == execution.EffectComplete
	})
	if environment.MutationCount(execution.EffectWorktreeCreate) != 1 ||
		environment.MutationCount(execution.EffectHostViewCreate) != 1 ||
		environment.MutationCount(execution.EffectAgentCreate) != 0 {
		t.Fatalf("production workspace milestone mutations = %v", environment.MutationOrder())
	}
	oracle = productionOracleSnapshot(run)
	oracle.Workspace.State = executionoracle.WorkspaceReady
	oracle.Workspace.Visible = true
	requireOracleAction(t, oracle, executionoracle.ActionCreateWorkerBootstrap)

	run = runSteps(t, store, environment, scope.RunID, func(current domain.Run) bool {
		return current.Execution.Agent.Observation != nil &&
			current.Execution.Agent.Observation.Status == execution.ObservationOwnedPresent
	})
	if environment.MutationCount(execution.EffectAgentCreate) != 1 {
		t.Fatalf("primary bootstrap mutations = %d", environment.MutationCount(execution.EffectAgentCreate))
	}
	oracleAgent := executionoracle.AgentFact{
		ID: run.Execution.Agent.Observation.ExternalID, Role: executionoracle.RoleWorker,
		RunID: oracle.Run.Scope.RunID, WorkspaceID: oracle.Run.Scope.WorkspaceID,
		ProfileRevision: oracle.Run.ProfileRevision, State: executionoracle.AgentRunning,
		Prompt: executionoracle.PromptNone,
	}
	oracle.Agents = []executionoracle.AgentFact{oracleAgent}
	requireOracleAction(t, oracle, executionoracle.ActionObserveWorker)

	nowMillis := testNowMillis()
	completion, err := environment.TerminalEvent(execution.CompletionEventFinished, run.Execution.Agent.ID, nowMillis)
	if err != nil {
		t.Fatal(err)
	}
	if err := controller.RecordCompletionEvent(context.Background(), run.ID, completion, nowMillis); err != nil {
		t.Fatal(err)
	}
	run = runSteps(t, store, environment, scope.RunID, func(current domain.Run) bool {
		return current.Execution.Agent.Phase == execution.EffectComplete &&
			current.Execution.WorkerVisibility != nil &&
			current.Execution.WorkerVisibility.AgentID == current.Execution.Agent.ExternalID &&
			current.Execution.AgentPrompt.ID == ""
	})
	oracleAgent.ID = run.Execution.Agent.ExternalID
	oracleAgent.State = executionoracle.AgentIdle
	oracleAgent.BootstrapDone = true
	oracle.Agents = []executionoracle.AgentFact{oracleAgent}
	promptAction := requireOracleAction(t, oracle, executionoracle.ActionSendWorkerPrompt)
	if !promptAction.NotifyOnFinish {
		t.Fatal("oracle real Worker prompt omitted notification")
	}

	run = runSteps(t, store, environment, scope.RunID, func(current domain.Run) bool {
		return current.Execution.AgentPrompt.Observation != nil &&
			current.Execution.AgentPrompt.Observation.Status == execution.ObservationOwnedPresent
	})
	prompt := environment.PromptRequest()
	if environment.MutationCount(execution.EffectAgentPrompt) != 1 || !prompt.NotifyOnFinish ||
		prompt.AgentID != run.Execution.Agent.ExternalID || prompt.InitialPrompt == host.ZeroWorkBootstrapPrompt {
		t.Fatalf("production notified prompt = %#v; mutations=%d", prompt, environment.MutationCount(execution.EffectAgentPrompt))
	}
	oracleAgent.State = executionoracle.AgentRunning
	oracleAgent.Prompt = executionoracle.PromptSent
	oracle.Agents = []executionoracle.AgentFact{oracleAgent}
	requireOracleAction(t, oracle, executionoracle.ActionAwaitNotification)
	for index := 0; index < 8; index++ {
		if _, err := executionapp.NewController(store, environment, environment, environment).Step(
			context.Background(), run.ID, testNowMillis(),
		); err != nil {
			t.Fatal(err)
		}
	}
	if environment.MutationCount(execution.EffectAgentPrompt) != 1 {
		t.Fatal("notification wait repeated nonrepeatable Worker prompt")
	}
}

type absentPromptHost struct{ host.Port }

func (adapter absentPromptHost) Invoke(ctx context.Context, command host.Command) (host.Observation, error) {
	if command.Capability != host.CapabilityAgentObserve || command.Arguments.EffectKind != execution.EffectAgentPrompt {
		return adapter.Port.Invoke(ctx, command)
	}
	result := host.ObservationResult{
		EffectID: command.Arguments.EffectID, Status: execution.ObservationAbsent,
		BindingHash: command.Arguments.BindingHash, PriorDispatcherAbsent: true,
		MaximumAgeMillis: 30_000,
	}
	result.FactHash = host.ObservationResultHash(result)
	return host.Observation{
		RequestID: command.RequestID, Cursor: command.AfterCursor + 1,
		ObservedAt: time.Now().UTC().Format(time.RFC3339Nano), Result: result,
	}, nil
}

func TestPrimaryLifecycleConformsToOracleFailClosedPromptAmbiguity(t *testing.T) {
	fixture := startVerticalDolt(t)
	store := openVerticalStore(t, fixture)
	source, base := initializeRepository(t, "primary-oracle-ambiguous")
	project, task := createVerticalRecords(t, store, "primary-oracle-ambiguous", source)
	worktree := source + "-worktree"
	scope := execution.Scope{
		ProjectID: project.ID, WorkspaceID: task.WorkspaceIDs[0],
		TaskID: task.ID, RunID: "run-primary-oracle-ambiguous",
	}
	environment := fake.NewEnvironment(fake.Options{
		SourcePath: source, WorktreePath: worktree, Branch: "task/" + task.ID,
		BaseSHA: base, Operational: operationalObservation("primary-oracle-ambiguous"),
	})
	t.Cleanup(environment.RemoveFixture)
	controller := executionapp.NewController(store, environment, environment, environment)
	if _, err := controller.Start(
		context.Background(),
		startCommand(t, task, scope, source, worktree, base, eligibilityFacts(scope, execution.LifecycleSurfaces{})),
	); err != nil {
		t.Fatal(err)
	}
	run := runSteps(t, store, environment, scope.RunID, func(current domain.Run) bool {
		return current.Execution.AgentPrompt.Observation != nil &&
			current.Execution.AgentPrompt.Observation.Status == execution.ObservationOwnedPresent
	})
	oracle := productionOracleSnapshot(run)
	oracle.Workspace.State = executionoracle.WorkspaceReady
	oracle.Workspace.Visible = true
	oracle.Agents = []executionoracle.AgentFact{{
		ID: run.Execution.Agent.ExternalID, Role: executionoracle.RoleWorker,
		RunID: oracle.Run.Scope.RunID, WorkspaceID: oracle.Run.Scope.WorkspaceID,
		ProfileRevision: oracle.Run.ProfileRevision, State: executionoracle.AgentAmbiguous,
		BootstrapDone: true, Prompt: executionoracle.PromptAmbiguous,
	}}
	decision := executionoracle.Evaluate(oracle)
	if len(decision.Actions) != 1 || decision.Actions[0].Kind != executionoracle.ActionParkNeedsYou ||
		decision.Reason != executionoracle.ReasonContradictoryFact {
		t.Fatalf("oracle ambiguity decision = %#v", decision)
	}

	nowMillis := testNowMillis()
	if err := controller.RecoverLostCompletionEvent(context.Background(), run.ID, execution.StallRecoveryFacts{
		ObservedAtMillis:     nowMillis,
		LastProgressAtMillis: nowMillis - execution.LostCompletionEventRecoveryMillis,
		AgentStateUnchanged:  true, NoToolActivity: true, NoWorktreeChange: true, NoUsageMovement: true,
		PendingTerminalEffectID: run.Execution.AgentPrompt.ID,
	}); err != nil {
		t.Fatal(err)
	}
	adapter := absentPromptHost{Port: environment}
	for step := 0; step < 20; step++ {
		current, err := store.Run(context.Background(), run.ID)
		if err != nil {
			t.Fatal(err)
		}
		if current.Execution.NeedsYou != nil {
			if current.Execution.NeedsYou.CleanupAuthorized {
				t.Fatal("ambiguous prompt park granted cleanup authority")
			}
			if environment.MutationCount(execution.EffectAgentPrompt) != 1 {
				t.Fatal("ambiguous prompt was repeated")
			}
			return
		}
		if _, err := executionapp.NewController(store, environment, adapter, environment).Step(
			context.Background(), run.ID, testNowMillis(),
		); err != nil {
			t.Fatal(err)
		}
	}
	t.Fatal("production prompt ambiguity did not converge to fail-closed parking")
}
