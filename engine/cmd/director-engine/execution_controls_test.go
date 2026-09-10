// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/mcuadros/director-engine/adapters/dolt"
	"github.com/mcuadros/director-engine/adapters/fake"
	executionapp "github.com/mcuadros/director-engine/application/execution"
	"github.com/mcuadros/director-engine/domain"
	"github.com/mcuadros/director-engine/domain/execution"
	"github.com/mcuadros/director-engine/reducer/eligibility"
)

type executionControlFixture struct {
	store       *dolt.DoltTaskStore
	environment *fake.Environment
	controller  *executionapp.Controller
	project     domain.Project
	task        domain.Task
	run         domain.Run
	source      string
	worktree    string
	base        string
}

func controlActor(session string) executionapp.AuthenticatedControlActor {
	return executionapp.AuthenticatedControlActor{
		Kind: execution.ControlActorHuman, ID: "human-owner", SessionID: session,
		Source: "server", Authenticated: true,
	}
}

func leaseBinding(project domain.Project) execution.LeaseBinding {
	return execution.LeaseBinding{
		HolderInstance: project.Lease.HolderInstance, HolderProcessIdentity: project.Lease.HolderProcessIdentity,
		Epoch: project.Lease.Epoch,
	}
}

func startExecutionControlFixture(t *testing.T, suffix string, loseResponses bool) executionControlFixture {
	return startExecutionControlFixtureWithPolicy(t, suffix, loseResponses, execution.ControlPolicy{})
}

func startExecutionControlFixtureWithPolicy(
	t *testing.T, suffix string, loseResponses bool, policy execution.ControlPolicy,
) executionControlFixture {
	t.Helper()
	fixture := startVerticalDolt(t)
	store := openVerticalStore(t, fixture)
	source, base := initializeRepository(t, suffix)
	project, task := createVerticalRecords(t, store, suffix, source)
	worktree := filepath.Join(filepath.Dir(source), "control-worktree-"+suffix)
	scope := execution.Scope{
		ProjectID: project.ID, WorkspaceID: task.WorkspaceIDs[0], TaskID: task.ID, RunID: "run-control-" + suffix,
	}
	environment := fake.NewEnvironment(fake.Options{
		SourcePath: source, WorktreePath: worktree, Branch: "task/" + task.ID,
		BaseSHA: base, Operational: operationalObservation("control-" + suffix),
		LoseEveryMutationResponse: loseResponses,
	})
	t.Cleanup(environment.RemoveFixture)
	controller := executionapp.NewController(store, environment, environment, environment)
	start := startCommand(t, task, scope, source, worktree, base, eligibilityFacts(scope, execution.LifecycleSurfaces{}))
	start.ControlPolicy = policy
	started, err := controller.Start(context.Background(), start)
	if err != nil || started.Decision.Kind != eligibility.DecisionEligible {
		t.Fatalf("start control fixture = %#v, %v", started, err)
	}
	run := runSteps(t, store, environment, scope.RunID, func(current domain.Run) bool {
		return current.Execution.AgentPrompt.Observation != nil &&
			current.Execution.AgentPrompt.Observation.Status == execution.ObservationOwnedPresent &&
			len(current.Execution.ControlledAgents) == 1
	})
	project, err = store.Project(context.Background(), project.ID)
	if err != nil {
		t.Fatal(err)
	}
	return executionControlFixture{store, environment, controller, project, task, run, source, worktree, base}
}

func TestCancelRetainPolicyTerminatesAgentsWithoutDeletingRecoverableWork(t *testing.T) {
	policy := execution.DefaultControlPolicy()
	policy.CancelRecovery = execution.RecoveryRetain
	fixture := startExecutionControlFixtureWithPolicy(t, "cancel-retain", false, policy)
	ctx := context.Background()
	if _, err := fixture.controller.CancelTask(ctx, executionapp.ControlCommand{
		RequestID: "cancel-retain-request", ProjectID: fixture.project.ID,
		TaskID: fixture.task.ID, RunID: fixture.run.ID,
		ExpectedProjectVersion: fixture.project.Version, ExpectedTaskVersion: fixture.task.Version,
		ExpectedRunVersion: fixture.run.Version, Lease: leaseBinding(fixture.project),
		Actor: controlActor("session-retain"), NowMillis: testNowMillis(),
	}); err != nil {
		t.Fatal(err)
	}
	for attempt := 0; attempt < 80; attempt++ {
		run, err := fixture.store.Run(ctx, fixture.run.ID)
		if err != nil {
			t.Fatal(err)
		}
		if run.Execution.Terminal {
			fixture.run = run
			break
		}
		if _, err := fixture.controller.Step(ctx, run.ID, testNowMillis()); err != nil {
			t.Fatal(err)
		}
	}
	if !fixture.run.Execution.Terminal || !fixture.run.Execution.Control.Recovery.Preserved ||
		fixture.run.Execution.Control.Recovery.CleanupAuthorized || !fixture.environment.WorktreePresent() ||
		fixture.environment.MutationCount(execution.EffectRecoverySnapshot) != 0 ||
		fixture.environment.MutationCount(execution.EffectHostViewArchive) != 0 ||
		fixture.environment.MutationCount(execution.EffectWorktreeRemove) != 0 {
		t.Fatalf("retain recovery state = %#v order=%v", fixture.run.Execution.Control.Recovery, fixture.environment.MutationOrder())
	}
}

func TestCancelContainsM35HelperPreservesCheckoutAndReleasesReservations(t *testing.T) {
	fixture, environment, project, task, run := activePrimaryRun(t, false, 1)
	run, helper := admitAndSeedHelper(t, fixture, environment, run, "control-writer", execution.HelperWriter)
	if helper.Phase != execution.HelperActive {
		t.Fatalf("helper was not active: %#v", helper)
	}
	recoverable := []byte("unintegrated helper work\n")
	if err := os.WriteFile(filepath.Join(helper.WorktreePath, "HELPER-RECOVERABLE.txt"), recoverable, 0o600); err != nil {
		t.Fatal(err)
	}
	store := openVerticalStore(t, fixture)
	controller := executionapp.NewController(store, environment, environment, environment)
	if _, err := controller.CancelTask(context.Background(), executionapp.ControlCommand{
		RequestID: "cancel-m35-helper", ProjectID: project.ID, TaskID: task.ID, RunID: run.ID,
		ExpectedProjectVersion: project.Version, ExpectedTaskVersion: task.Version, ExpectedRunVersion: run.Version,
		Lease: leaseBinding(project), Actor: controlActor("session-m35-helper"), NowMillis: testNowMillis(),
	}); err != nil {
		t.Fatal(err)
	}
	for step := 0; step < 160; step++ {
		current, err := store.Run(context.Background(), run.ID)
		if err != nil {
			t.Fatal(err)
		}
		if current.Execution.Terminal {
			run = current
			break
		}
		if _, err := executionapp.NewController(store, environment.Restart(), environment.Restart(), environment.Restart()).Step(
			context.Background(), run.ID, testNowMillis(),
		); err != nil {
			var handoff *executionapp.EffectHandoffError
			if !errors.As(err, &handoff) {
				t.Fatalf("control helper step %d: %v", step, err)
			}
		}
	}
	run, _ = store.Run(context.Background(), run.ID)
	if !run.Execution.Terminal {
		t.Fatalf("helper-aware cancellation did not converge: control=%#v helpers=%#v needs=%#v order=%v", run.Execution.Control, run.Execution.Helpers, run.Execution.NeedsYou, environment.MutationOrder())
	}
	helper = run.Execution.Helpers[execution.HelperIndex(run.Execution.Helpers, helper.ID)]
	if helper.Phase != execution.HelperTerminal || helper.CheckoutRemove.Phase != execution.EffectComplete ||
		helper.BudgetEvidenceID == "" {
		t.Fatalf("helper cleanup/accounting = %#v", helper)
	}
	for _, reservation := range run.Execution.Budget.Reservations {
		if reservation.ID == helper.BudgetReservationID && (!reservation.Released || reservation.EvidenceID != helper.BudgetEvidenceID) {
			t.Fatalf("helper budget reservation remained live: %#v", reservation)
		}
	}
	relative := filepath.Join("helpers", filepath.Base(helper.WorktreePath), "HELPER-RECOVERABLE.txt")
	recovered, err := environment.RecoveryFile(relative)
	if err != nil || string(recovered) != string(recoverable) {
		t.Fatalf("helper recovery = %q, %v", recovered, err)
	}
	if _, err := os.Stat(helper.WorktreePath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("owned helper checkout remained after verified preservation: %v", err)
	}
}

func TestPauseBlocksNewM35HelperAdmissionBeforeRunMaterialization(t *testing.T) {
	fixture, environment, project, _, run := activePrimaryRun(t, false, 1)
	store := openVerticalStore(t, fixture)
	controller := executionapp.NewController(store, environment, environment, environment)
	if _, err := controller.RequestProjectControl(context.Background(), executionapp.ControlCommand{
		Kind: execution.ControlPauseProject, RequestID: "pause-before-helper-request",
		ProjectID: project.ID, ExpectedProjectVersion: project.Version, Lease: leaseBinding(project),
		Actor: controlActor("session-pause-helper"), NowMillis: testNowMillis(),
	}); err != nil {
		t.Fatal(err)
	}
	before := len(run.Execution.Helpers)
	if _, err := controller.RequestHelper(context.Background(), executionapp.RequestHelperCommand{
		RequestID: "helper-after-project-pause", RunID: run.ID,
		ParentAgentID: run.Execution.Agent.ExternalID, Mode: execution.HelperReadOnly, Purpose: "must not start",
	}, testNowMillis()); err == nil {
		t.Fatal("paused Project admitted a new helper")
	}
	after, err := store.Run(context.Background(), run.ID)
	if err != nil || len(after.Execution.Helpers) != before || environment.HelperReservationCount() != 0 {
		t.Fatalf("paused helper request changed state: helpers=%d reservations=%d err=%v", len(after.Execution.Helpers), environment.HelperReservationCount(), err)
	}
}

func TestPauseReconcilesInFlightExternalEffectWithoutNewDispatch(t *testing.T) {
	fixture := startVerticalDolt(t)
	store := openVerticalStore(t, fixture)
	source, base := initializeRepository(t, "pause-inflight")
	project, task := createVerticalRecords(t, store, "pause-inflight", source)
	worktree := source + "-worktree"
	scope := execution.Scope{ProjectID: project.ID, WorkspaceID: task.WorkspaceIDs[0], TaskID: task.ID, RunID: "run-pause-inflight"}
	environment := fake.NewEnvironment(fake.Options{
		SourcePath: source, WorktreePath: worktree, Branch: "task/" + task.ID, BaseSHA: base,
		Operational: operationalObservation("pause-inflight"), LoseEveryMutationResponse: true,
	})
	t.Cleanup(environment.RemoveFixture)
	controller := executionapp.NewController(store, environment, environment, environment)
	if _, err := controller.Start(context.Background(), startCommand(
		t, task, scope, source, worktree, base, eligibilityFacts(scope, execution.LifecycleSurfaces{}),
	)); err != nil {
		t.Fatal(err)
	}
	for step := 0; step < 8; step++ {
		current, _ := store.Run(context.Background(), scope.RunID)
		if current.Execution.Worktree.Phase == execution.EffectDispatching {
			break
		}
		_, err := controller.Step(context.Background(), scope.RunID, testNowMillis())
		var handoff *executionapp.EffectHandoffError
		if err != nil && !errors.As(err, &handoff) {
			t.Fatal(err)
		}
	}
	run, _ := store.Run(context.Background(), scope.RunID)
	if run.Execution.Worktree.Phase != execution.EffectDispatching || environment.MutationCount(execution.EffectWorktreeCreate) != 1 {
		t.Fatalf("in-flight setup = %#v order=%v", run.Execution.Worktree, environment.MutationOrder())
	}
	project, _ = store.Project(context.Background(), project.ID)
	if _, err := controller.RequestProjectControl(context.Background(), executionapp.ControlCommand{
		Kind: execution.ControlPauseProject, RequestID: "pause-inflight-request", ProjectID: project.ID,
		ExpectedProjectVersion: project.Version, Lease: leaseBinding(project),
		Actor: controlActor("session-inflight"), NowMillis: testNowMillis(),
	}); err != nil {
		t.Fatal(err)
	}
	for step := 0; step < 20; step++ {
		currentProject, _ := store.Project(context.Background(), project.ID)
		currentRun, _ := store.Run(context.Background(), run.ID)
		if currentProject.Control.Phase != execution.ControlPaused {
			_, _ = controller.ReconcileProjectControl(context.Background(), executionapp.ReconcileControlCommand{
				RequestID: "pause-inflight-reconcile", ProjectID: project.ID,
				Lease: leaseBinding(currentProject), NowMillis: testNowMillis(),
			})
			continue
		}
		if currentRun.Execution.Worktree.Phase == execution.EffectComplete {
			run = currentRun
			break
		}
		if _, err := controller.Step(context.Background(), run.ID, testNowMillis()); err != nil {
			t.Fatal(err)
		}
	}
	if run.Execution.Worktree.Phase != execution.EffectComplete || environment.MutationCount(execution.EffectWorktreeCreate) != 1 ||
		environment.MutationCount(execution.EffectAgentCreate) != 0 || environment.MutationCount(execution.EffectAgentPrompt) != 0 ||
		environment.HelperReservationCount() != 0 {
		t.Fatalf("paused evidence reconciliation dispatched work: run=%#v order=%v", run.Execution.Control, environment.MutationOrder())
	}
}

func reconcileControlUntil(t *testing.T, fixture *executionControlFixture, stop func(domain.Project, domain.Run) bool) (domain.Project, domain.Run) {
	t.Helper()
	ctx := context.Background()
	for attempt := 0; attempt < 160; attempt++ {
		project, err := fixture.store.Project(ctx, fixture.project.ID)
		if err != nil {
			t.Fatal(err)
		}
		run, err := fixture.store.Run(ctx, fixture.run.ID)
		if err != nil {
			t.Fatal(err)
		}
		if stop(project, run) {
			return project, run
		}
		_, err = executionapp.NewController(fixture.store, fixture.environment, fixture.environment, fixture.environment).ReconcileProjectControl(
			ctx, executionapp.ReconcileControlCommand{
				RequestID: "reconcile-" + fixture.run.ID + "-stable", ProjectID: project.ID,
				Lease: leaseBinding(project), NowMillis: testNowMillis(),
			},
		)
		var handoff *executionapp.EffectHandoffError
		if err != nil && !errors.As(err, &handoff) && !errors.Is(err, fake.ErrResponseLost) {
			t.Fatalf("reconcile control %d: %v", attempt, err)
		}
	}
	t.Fatal("control did not converge")
	return domain.Project{}, domain.Run{}
}

func TestProjectPauseWaitsForBoundaryAndResumeReconcilesBeforeDispatch(t *testing.T) {
	fixture := startExecutionControlFixture(t, "pause-resume", false)
	ctx := context.Background()
	result, err := fixture.controller.RequestProjectControl(ctx, executionapp.ControlCommand{
		Kind: execution.ControlPauseProject, RequestID: "pause-project-request-0001",
		ProjectID: fixture.project.ID, ExpectedProjectVersion: fixture.project.Version,
		Lease: leaseBinding(fixture.project), Actor: controlActor("session-pause"), NowMillis: testNowMillis(),
	})
	if err != nil || result.Project.State != "paused" || !result.Project.Control.ResumeRequired {
		t.Fatalf("pause = %#v, %v", result, err)
	}
	for attempt := 0; attempt < 4; attempt++ {
		_, _ = fixture.controller.ReconcileProjectControl(ctx, executionapp.ReconcileControlCommand{
			RequestID: "pause-boundary-reconcile", ProjectID: fixture.project.ID,
			Lease: leaseBinding(result.Project), NowMillis: testNowMillis(),
		})
	}
	pausedRun, err := fixture.store.Run(ctx, fixture.run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if pausedRun.Execution.Control.Phase != execution.ControlAwaitingSafeBoundary ||
		environmentMutationCount(fixture.environment, execution.EffectControlAgentArchive) != 0 {
		t.Fatalf("pause crossed active boundary: %#v", pausedRun.Execution.Control)
	}

	now := testNowMillis()
	event, err := fixture.environment.TerminalEvent(execution.CompletionEventFinished, pausedRun.Execution.AgentPrompt.ID, now)
	if err != nil {
		t.Fatal(err)
	}
	if err := fixture.controller.RecordCompletionEvent(ctx, pausedRun.ID, event, now); err != nil {
		t.Fatal(err)
	}
	project, run := reconcileControlUntil(t, &fixture, func(project domain.Project, run domain.Run) bool {
		return project.Control.Phase == execution.ControlPaused && run.Execution.Control.Phase == execution.ControlPaused
	})
	beforePromptMutations := fixture.environment.MutationCount(execution.EffectAgentPrompt)
	resume, err := fixture.controller.RequestProjectControl(ctx, executionapp.ControlCommand{
		Kind: execution.ControlResumeProject, RequestID: "resume-project-request-0001",
		ProjectID: project.ID, ExpectedProjectVersion: project.Version,
		Lease: leaseBinding(project), Actor: controlActor("session-resume"), NowMillis: testNowMillis(),
	})
	if err != nil || resume.Project.State != "paused" || resume.Project.Control.Phase != execution.ControlReconciling {
		t.Fatalf("resume intent = %#v, %v", resume, err)
	}
	project, run = reconcileControlUntil(t, &fixture, func(project domain.Project, run domain.Run) bool {
		return project.State == "active" && project.Control.Phase == execution.ControlComplete &&
			run.Execution.Control.Phase == execution.ControlComplete
	})
	if run.Execution.Control.ResumeReconciliationID == "" || run.Execution.LastStartupReconciliation == nil ||
		run.Execution.Control.ResumeReconciliationID != run.Execution.LastStartupReconciliation.ID ||
		fixture.environment.MutationCount(execution.EffectAgentPrompt) != beforePromptMutations {
		t.Fatalf("resume did not reconcile exactly once: project=%#v run=%#v", project.Control, run.Execution.Control)
	}
}

func environmentMutationCount(environment *fake.Environment, kind execution.EffectKind) int {
	return environment.MutationCount(kind)
}

func TestCancelTaskIsIsolatedAndPreservesRecoveryBeforeCleanup(t *testing.T) {
	fixture := startExecutionControlFixture(t, "cancel-isolation", true)
	ctx := context.Background()
	siblingTask := fixture.task
	siblingTask.ID = fixture.task.ID + "-sibling"
	siblingTask.Title = "Unaffected sibling Task"
	if result, err := fixture.store.CreateTask(ctx, domain.CommandRequest{
		IdempotencyKey: "create-sibling-task", Type: "task.create", AggregateID: siblingTask.ID,
		Payload: eventPayloadForTest(struct{}{}),
	}, siblingTask, domain.Event{
		ID: "event-create-sibling-task", Sequence: 1, AggregateID: siblingTask.ID,
		Type: "task.created", Payload: eventPayloadForTest(struct{}{}),
	}); err != nil || result.Outcome != domain.CommandApplied {
		t.Fatalf("create sibling Task = %#v, %v", result, err)
	}
	siblingWorktree := fixture.worktree + "-sibling"
	siblingScope := execution.Scope{
		ProjectID: fixture.project.ID, WorkspaceID: siblingTask.WorkspaceIDs[0],
		TaskID: siblingTask.ID, RunID: fixture.run.ID + "-sibling",
	}
	siblingEnvironment := fake.NewEnvironment(fake.Options{
		SourcePath: fixture.source, WorktreePath: siblingWorktree, Branch: "task/" + siblingTask.ID,
		BaseSHA: fixture.base, Operational: operationalObservation("cancel-sibling"),
	})
	t.Cleanup(siblingEnvironment.RemoveFixture)
	siblingController := executionapp.NewController(fixture.store, siblingEnvironment, siblingEnvironment, siblingEnvironment)
	if _, err := siblingController.Start(ctx, startCommand(
		t, siblingTask, siblingScope, fixture.source, siblingWorktree, fixture.base,
		eligibilityFacts(siblingScope, execution.LifecycleSurfaces{}),
	)); err != nil {
		t.Fatal(err)
	}
	siblingRun := runSteps(t, fixture.store, siblingEnvironment, siblingScope.RunID, func(current domain.Run) bool {
		return current.Execution.AgentPrompt.Observation != nil &&
			current.Execution.AgentPrompt.Observation.Status == execution.ObservationOwnedPresent
	})
	siblingVersion, siblingMutations := siblingRun.Version, siblingEnvironment.TotalMutationCount()
	recoverable := []byte("uncommitted recoverable work\n")
	if err := os.WriteFile(filepath.Join(fixture.worktree, "RECOVERABLE.txt"), recoverable, 0o600); err != nil {
		t.Fatal(err)
	}
	before := fixture.environment.MutationOrder()
	result, err := fixture.controller.CancelTask(ctx, executionapp.ControlCommand{
		RequestID: "cancel-task-request-0001", ProjectID: fixture.project.ID,
		TaskID: fixture.task.ID, RunID: fixture.run.ID,
		ExpectedProjectVersion: fixture.project.Version, ExpectedTaskVersion: fixture.task.Version,
		ExpectedRunVersion: fixture.run.Version, Lease: leaseBinding(fixture.project),
		Actor: controlActor("session-cancel"), NowMillis: testNowMillis(),
	})
	if err != nil || result.Run == nil || result.Project.State != "active" {
		t.Fatalf("cancel = %#v, %v", result, err)
	}
	for attempt := 0; attempt < 160; attempt++ {
		run, readErr := fixture.store.Run(ctx, fixture.run.ID)
		if readErr != nil {
			t.Fatal(readErr)
		}
		if run.Execution.Terminal {
			fixture.run = run
			break
		}
		_, stepErr := executionapp.NewController(fixture.store, fixture.environment, fixture.environment, fixture.environment).Step(ctx, run.ID, testNowMillis())
		var handoff *executionapp.EffectHandoffError
		if stepErr != nil && !errors.As(stepErr, &handoff) && !errors.Is(stepErr, fake.ErrResponseLost) {
			t.Fatalf("cancel step %d: %v", attempt, stepErr)
		}
	}
	if !fixture.run.Execution.Terminal || fixture.run.Execution.Control.Phase != execution.ControlCancelled ||
		!fixture.run.Execution.Control.Recovery.Preserved || fixture.run.Execution.Control.Recovery.ArtifactID == "" ||
		fixture.environment.WorktreePresent() {
		t.Fatalf("cancel terminal state = %#v", fixture.run.Execution.Control)
	}
	recovered, err := fixture.environment.RecoveryFile("RECOVERABLE.txt")
	if err != nil || string(recovered) != string(recoverable) {
		t.Fatalf("recoverable dirty work was not preserved: %q, %v", recovered, err)
	}
	order := fixture.environment.MutationOrder()[len(before):]
	positions := map[execution.EffectKind]int{}
	for index, value := range order {
		positions[execution.EffectKind(value)] = index
	}
	if !(positions[execution.EffectControlAgentArchive] < positions[execution.EffectRecoverySnapshot] &&
		positions[execution.EffectRecoverySnapshot] < positions[execution.EffectHostViewArchive] &&
		positions[execution.EffectHostViewArchive] < positions[execution.EffectWorktreeRemove]) {
		t.Fatalf("cancel effect order = %v", order)
	}
	project, err := fixture.store.Project(ctx, fixture.project.ID)
	if err != nil || project.State != "active" || project.Control.SchemaVersion != "" {
		t.Fatalf("Task cancel changed Project control: %#v, %v", project, err)
	}
	relaunchScope := execution.Scope{
		ProjectID: project.ID, WorkspaceID: fixture.task.WorkspaceIDs[0],
		TaskID: fixture.task.ID, RunID: fixture.run.ID + "-unexpected-relaunch",
	}
	if _, err := fixture.controller.Start(ctx, startCommand(
		t, fixture.task, relaunchScope, fixture.source, fixture.worktree+"-unexpected",
		fixture.base, eligibilityFacts(relaunchScope, execution.LifecycleSurfaces{}),
	)); err == nil {
		t.Fatal("cancelled Task admitted an unexpected automatic relaunch")
	}
	siblingAfter, err := fixture.store.Run(ctx, siblingRun.ID)
	if err != nil || siblingAfter.Version != siblingVersion || siblingAfter.Execution.Control.SchemaVersion != "" ||
		siblingEnvironment.TotalMutationCount() != siblingMutations || !siblingEnvironment.WorktreePresent() {
		t.Fatalf("Task cancel crossed into sibling Run: before=%d after=%#v err=%v", siblingVersion, siblingAfter, err)
	}
}

func TestEmergencyStopRequiresFreshSameSessionConfirmationAndBeatsHardBudget(t *testing.T) {
	fixture := startExecutionControlFixture(t, "emergency", false)
	ctx := context.Background()
	hardBudgetRun := fixture.run
	hardBudgetRun.Version++
	hardBudgetRun.Execution.NeedsYou = &execution.NeedsYou{
		Code: "budget_tokens_exhausted", WakeCondition: "explicit_human_budget_revision",
		CleanupAuthorized: false,
	}
	if result, err := fixture.store.UpdateRun(ctx, domain.CommandRequest{
		IdempotencyKey: "seed-hard-budget-emergency", Type: "run.budget_hard_exhausted",
		AggregateID: hardBudgetRun.ID, ExpectedVersion: fixture.run.Version,
		Payload: eventPayloadForTest(struct{}{}),
	}, hardBudgetRun, domain.Event{
		ID: "event-seed-hard-budget-emergency", RunID: hardBudgetRun.ID,
		Sequence: hardBudgetRun.Version + 1, AggregateID: hardBudgetRun.ID,
		AggregateVersion: hardBudgetRun.Version, Type: "run.budget_hard_exhausted",
		Payload: eventPayloadForTest(struct{}{}),
	}); err != nil || result.Outcome != domain.CommandApplied {
		t.Fatalf("seed hard budget = %#v, %v", result, err)
	}
	fixture.run = hardBudgetRun
	prepared, err := fixture.controller.PrepareEmergencyStop(ctx, executionapp.ControlCommand{
		RequestID: "emergency-prepare-request", ProjectID: fixture.project.ID,
		ExpectedProjectVersion: fixture.project.Version, Lease: leaseBinding(fixture.project),
		Actor: controlActor("session-emergency"), NowMillis: testNowMillis(),
	})
	if err != nil || prepared.ConfirmationID == "" || prepared.Project.State != "active" {
		t.Fatalf("prepare emergency = %#v, %v", prepared, err)
	}
	expired := executionapp.ControlCommand{
		RequestID: "emergency-confirm-expired", ProjectID: fixture.project.ID,
		ExpectedProjectVersion: prepared.Project.Version, Lease: leaseBinding(prepared.Project),
		Actor:          controlActor("session-emergency"),
		NowMillis:      prepared.Project.Control.Confirmation.ExpiresAtMillis,
		ConfirmationID: prepared.ConfirmationID,
	}
	if _, err := fixture.controller.ConfirmEmergencyStop(ctx, expired); !errors.Is(err, executionapp.ErrEmergencyConfirmationStale) {
		t.Fatalf("expired confirmation error = %v", err)
	}
	stale := executionapp.ControlCommand{
		RequestID: "emergency-confirm-stale", ProjectID: fixture.project.ID,
		ExpectedProjectVersion: prepared.Project.Version, Lease: leaseBinding(prepared.Project),
		Actor: controlActor("another-session"), NowMillis: testNowMillis(), ConfirmationID: prepared.ConfirmationID,
	}
	if _, err := fixture.controller.ConfirmEmergencyStop(ctx, stale); !errors.Is(err, executionapp.ErrEmergencyConfirmationStale) {
		t.Fatalf("cross-session confirmation error = %v", err)
	}
	confirmed, err := fixture.controller.ConfirmEmergencyStop(ctx, executionapp.ControlCommand{
		RequestID: "emergency-confirm-current", ProjectID: fixture.project.ID,
		ExpectedProjectVersion: prepared.Project.Version, Lease: leaseBinding(prepared.Project),
		Actor: controlActor("session-emergency"), NowMillis: testNowMillis(), ConfirmationID: prepared.ConfirmationID,
	})
	if err != nil || confirmed.Project.State != "paused" || !confirmed.Project.Control.EmergencyLatched {
		t.Fatalf("confirm emergency = %#v, %v", confirmed, err)
	}
	project, run := reconcileControlUntil(t, &fixture, func(project domain.Project, run domain.Run) bool {
		return project.Control.Phase == execution.ControlComplete && run.Execution.Terminal
	})
	if project.State != "paused" || !project.Control.ResumeRequired || run.Execution.Control.Phase != execution.ControlCancelled ||
		!run.Execution.Control.Recovery.Preserved {
		t.Fatalf("emergency terminal = project %#v run %#v", project.Control, run.Execution.Control)
	}
	if _, err := fixture.controller.Start(ctx, startCommand(
		t, fixture.task, execution.Scope{
			ProjectID: project.ID, WorkspaceID: fixture.task.WorkspaceIDs[0], TaskID: fixture.task.ID, RunID: "unexpected-relaunch",
		}, fixture.source, fixture.worktree+"-new", fixture.base,
		eligibilityFacts(execution.Scope{
			ProjectID: project.ID, WorkspaceID: fixture.task.WorkspaceIDs[0], TaskID: fixture.task.ID, RunID: "unexpected-relaunch",
		}, execution.LifecycleSurfaces{}),
	)); err == nil {
		t.Fatal("emergency-stopped Project admitted an unexpected relaunch")
	}
	resume, err := fixture.controller.RequestProjectControl(ctx, executionapp.ControlCommand{
		Kind: execution.ControlResumeProject, RequestID: "emergency-resume-request",
		ProjectID: project.ID, ExpectedProjectVersion: project.Version, Lease: leaseBinding(project),
		Actor: controlActor("session-emergency-resume"), NowMillis: testNowMillis(),
	})
	if err != nil {
		t.Fatal(err)
	}
	resumed, err := fixture.controller.ReconcileProjectControl(ctx, executionapp.ReconcileControlCommand{
		RequestID: "emergency-resume-reconcile", ProjectID: project.ID,
		Lease: leaseBinding(resume.Project), NowMillis: testNowMillis(),
	})
	if err != nil || resumed.Project.State != "active" || resumed.Project.Control.ResumeRequired {
		t.Fatalf("explicit emergency resume = %#v, %v", resumed.Project.Control, err)
	}
	relaunchScope := execution.Scope{
		ProjectID: project.ID, WorkspaceID: fixture.task.WorkspaceIDs[0],
		TaskID: fixture.task.ID, RunID: "post-emergency-explicit-relaunch",
	}
	if _, err := fixture.controller.Start(ctx, startCommand(
		t, fixture.task, relaunchScope, fixture.source, fixture.worktree+"-resumed", fixture.base,
		eligibilityFacts(relaunchScope, execution.LifecycleSurfaces{}),
	)); err != nil {
		t.Fatalf("explicit Resume did not release emergency relaunch latch: %v", err)
	}
}

func TestConcurrentPauseCASAndLeaseLossHaveNoDuplicateEffects(t *testing.T) {
	fixture := startExecutionControlFixture(t, "pause-cas", false)
	ctx := context.Background()
	start := make(chan struct{})
	results := make(chan executionapp.ControlResult, 2)
	errorsChannel := make(chan error, 2)
	var wait sync.WaitGroup
	for _, requestID := range []string{"pause-concurrent-a", "pause-concurrent-b"} {
		requestID := requestID
		wait.Add(1)
		go func() {
			defer wait.Done()
			<-start
			result, err := executionapp.NewController(fixture.store, fixture.environment, fixture.environment, fixture.environment).RequestProjectControl(ctx, executionapp.ControlCommand{
				Kind: execution.ControlPauseProject, RequestID: requestID, ProjectID: fixture.project.ID,
				ExpectedProjectVersion: fixture.project.Version, Lease: leaseBinding(fixture.project),
				Actor: controlActor("session-cas"), NowMillis: testNowMillis(),
			})
			results <- result
			errorsChannel <- err
		}()
	}
	close(start)
	wait.Wait()
	close(results)
	close(errorsChannel)
	for err := range errorsChannel {
		if err != nil {
			t.Fatal(err)
		}
	}
	applied, rejected := 0, 0
	for result := range results {
		switch result.Command.Outcome {
		case domain.CommandApplied:
			applied++
		case domain.CommandRejectedVersionConflict:
			rejected++
		}
	}
	if applied != 1 || rejected != 1 {
		t.Fatalf("pause CAS outcomes applied=%d rejected=%d", applied, rejected)
	}
	project, err := fixture.store.Project(ctx, fixture.project.ID)
	if err != nil {
		t.Fatal(err)
	}
	wrongLease := leaseBinding(project)
	wrongLease.Epoch++
	before := fixture.environment.TotalMutationCount()
	_, err = fixture.controller.ReconcileProjectControl(ctx, executionapp.ReconcileControlCommand{
		RequestID: "lost-lease-reconcile", ProjectID: project.ID, Lease: wrongLease, NowMillis: testNowMillis(),
	})
	if !errors.Is(err, executionapp.ErrProjectLeaseUnavailable) || fixture.environment.TotalMutationCount() != before {
		t.Fatalf("lost lease = %v, mutations %d -> %d", err, before, fixture.environment.TotalMutationCount())
	}
}

func TestCancelRejectsCrossProjectScopeBeforeRunOrHostMutation(t *testing.T) {
	fixture := startExecutionControlFixture(t, "cross-project", false)
	otherSource, _ := initializeRepository(t, "cross-project-other")
	otherProject, _ := createVerticalRecords(t, fixture.store, "cross-project-other", otherSource)
	beforeRun := fixture.run
	beforeMutations := fixture.environment.TotalMutationCount()
	_, err := fixture.controller.CancelTask(context.Background(), executionapp.ControlCommand{
		RequestID: "cancel-cross-project", ProjectID: otherProject.ID,
		TaskID: fixture.task.ID, RunID: fixture.run.ID,
		ExpectedProjectVersion: otherProject.Version, ExpectedTaskVersion: fixture.task.Version,
		ExpectedRunVersion: fixture.run.Version, Lease: leaseBinding(otherProject),
		Actor: controlActor("session-cross-project"), NowMillis: testNowMillis(),
	})
	if !errors.Is(err, executionapp.ErrControlInvalid) {
		t.Fatalf("cross-Project cancellation error = %v", err)
	}
	afterRun, readErr := fixture.store.Run(context.Background(), fixture.run.ID)
	if readErr != nil || afterRun.Version != beforeRun.Version || afterRun.Execution.Control.SchemaVersion != "" ||
		fixture.environment.TotalMutationCount() != beforeMutations {
		t.Fatalf("cross-Project cancellation mutated Run/host: before=%d after=%#v err=%v", beforeRun.Version, afterRun, readErr)
	}
}

func TestTaskCancelStartupRecoveryCrossesEveryCrashFrontierWithoutReplay(t *testing.T) {
	fixture := startExecutionControlFixture(t, "cancel-startup", true)
	ctx := context.Background()
	if _, err := fixture.controller.CancelTask(ctx, executionapp.ControlCommand{
		RequestID: "cancel-startup-request", ProjectID: fixture.project.ID,
		TaskID: fixture.task.ID, RunID: fixture.run.ID,
		ExpectedProjectVersion: fixture.project.Version, ExpectedTaskVersion: fixture.task.Version,
		ExpectedRunVersion: fixture.run.Version, Lease: leaseBinding(fixture.project),
		Actor: controlActor("session-startup"), NowMillis: testNowMillis(),
	}); err != nil {
		t.Fatal(err)
	}
	for frontier := 0; frontier < 120; frontier++ {
		restarted := fixture.environment.Restart()
		_, err := executionapp.NewController(fixture.store, restarted, restarted, restarted).ReconcileStartup(
			ctx, executionapp.StartupCommand{
				SchemaVersion: executionapp.StartupCommandSchemaVersion,
				RequestID:     fmt.Sprintf("control-startup-%03d", frontier), NowMillis: testNowMillis(),
			},
		)
		var handoff *executionapp.EffectHandoffError
		if err != nil && !errors.As(err, &handoff) && !errors.Is(err, fake.ErrResponseLost) {
			t.Fatalf("startup frontier %d: %v", frontier, err)
		}
		run, readErr := fixture.store.Run(ctx, fixture.run.ID)
		if readErr != nil {
			t.Fatal(readErr)
		}
		if run.Execution.Terminal {
			fixture.run = run
			break
		}
	}
	if !fixture.run.Execution.Terminal {
		t.Fatal("startup recovery did not finish Task cancellation")
	}
	for _, kind := range []execution.EffectKind{
		execution.EffectControlAgentArchive, execution.EffectRecoverySnapshot,
		execution.EffectHostViewArchive, execution.EffectWorktreeRemove,
	} {
		if mutations := fixture.environment.MutationCount(kind); mutations != 1 {
			t.Fatalf("startup replayed %s %d times", kind, mutations)
		}
	}
}

func TestProjectPauseResumeStartupRecoveryNeverKillsOrRepeatsPrompt(t *testing.T) {
	fixture := startExecutionControlFixture(t, "pause-resume-startup", false)
	ctx := context.Background()
	paused, err := fixture.controller.RequestProjectControl(ctx, executionapp.ControlCommand{
		Kind: execution.ControlPauseProject, RequestID: "pause-startup-request",
		ProjectID: fixture.project.ID, ExpectedProjectVersion: fixture.project.Version,
		Lease: leaseBinding(fixture.project), Actor: controlActor("session-pause-startup"), NowMillis: testNowMillis(),
	})
	if err != nil {
		t.Fatal(err)
	}
	for frontier := 0; frontier < 4; frontier++ {
		restarted := fixture.environment.Restart()
		if _, err := executionapp.NewController(fixture.store, restarted, restarted, restarted).ReconcileStartup(ctx, executionapp.StartupCommand{
			SchemaVersion: executionapp.StartupCommandSchemaVersion,
			RequestID:     fmt.Sprintf("pause-startup-frontier-%02d", frontier), NowMillis: testNowMillis(),
		}); err != nil {
			t.Fatal(err)
		}
	}
	run, err := fixture.store.Run(ctx, fixture.run.ID)
	if err != nil || run.Execution.Control.Phase != execution.ControlAwaitingSafeBoundary ||
		fixture.environment.MutationCount(execution.EffectControlAgentArchive) != 0 {
		t.Fatalf("startup pause crossed safe boundary: %#v, %v", run.Execution.Control, err)
	}
	now := testNowMillis()
	event, err := fixture.environment.TerminalEvent(execution.CompletionEventFinished, run.Execution.AgentPrompt.ID, now)
	if err != nil {
		t.Fatal(err)
	}
	if err := fixture.controller.RecordCompletionEvent(ctx, run.ID, event, now); err != nil {
		t.Fatal(err)
	}
	project, run := reconcileControlUntil(t, &fixture, func(project domain.Project, run domain.Run) bool {
		return project.Control.Phase == execution.ControlPaused && run.Execution.Control.Phase == execution.ControlPaused
	})
	promptMutations := fixture.environment.MutationCount(execution.EffectAgentPrompt)
	if _, err := fixture.controller.RequestProjectControl(ctx, executionapp.ControlCommand{
		Kind: execution.ControlResumeProject, RequestID: "resume-startup-request",
		ProjectID: project.ID, ExpectedProjectVersion: project.Version, Lease: leaseBinding(paused.Project),
		Actor: controlActor("session-resume-startup"), NowMillis: testNowMillis(),
	}); err != nil {
		t.Fatal(err)
	}
	for frontier := 0; frontier < 20; frontier++ {
		current, readErr := fixture.store.Project(ctx, project.ID)
		if readErr != nil {
			t.Fatal(readErr)
		}
		if current.State == "active" {
			project = current
			break
		}
		restarted := fixture.environment.Restart()
		if _, err := executionapp.NewController(fixture.store, restarted, restarted, restarted).ReconcileStartup(ctx, executionapp.StartupCommand{
			SchemaVersion: executionapp.StartupCommandSchemaVersion,
			RequestID:     fmt.Sprintf("resume-startup-frontier-%02d", frontier), NowMillis: testNowMillis(),
		}); err != nil {
			t.Fatal(err)
		}
	}
	if project.State != "active" || fixture.environment.MutationCount(execution.EffectAgentPrompt) != promptMutations {
		t.Fatalf("startup resume state=%s prompt mutations=%d->%d", project.State, promptMutations, fixture.environment.MutationCount(execution.EffectAgentPrompt))
	}
}
