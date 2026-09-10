// SPDX-License-Identifier: Apache-2.0

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"testing"

	"github.com/mcuadros/director-engine/adapters/dolt"
	"github.com/mcuadros/director-engine/adapters/fake"
	executionapp "github.com/mcuadros/director-engine/application/execution"
	"github.com/mcuadros/director-engine/domain"
	"github.com/mcuadros/director-engine/domain/execution"
	"github.com/mcuadros/director-engine/domain/runtimebudget"
	executionoracle "github.com/mcuadros/director-engine/internal/testkit/executionoracle"
	"github.com/mcuadros/director-engine/ports/host"
	runtimeport "github.com/mcuadros/director-engine/ports/runtime"
	storeport "github.com/mcuadros/director-engine/ports/taskstore"
)

type missingHelperBoundary struct{ *fake.Environment }

func (adapter missingHelperBoundary) ObserveHelperBoundary(context.Context, runtimeport.HelperRequest) (execution.HelperBoundaryObservation, error) {
	return execution.HelperBoundaryObservation{}, nil
}

type missingHelperTelemetry struct{ *fake.Environment }

func (adapter missingHelperTelemetry) ObserveOperational(context.Context, execution.Scope, execution.OperationalPolicy) (execution.OperationalObservation, error) {
	return execution.OperationalObservation{}, nil
}

func activePrimaryRun(t *testing.T, loseResponses bool, globalActive uint32) (*verticalDolt, *fake.Environment, domain.Project, domain.Task, domain.Run) {
	return activePrimaryRunWithCommand(t, loseResponses, globalActive, nil)
}

func activePrimaryRunWithCommand(t *testing.T, loseResponses bool, globalActive uint32, mutate func(*executionapp.StartCommand)) (*verticalDolt, *fake.Environment, domain.Project, domain.Task, domain.Run) {
	t.Helper()
	fixture := startVerticalDolt(t)
	store := openVerticalStore(t, fixture)
	source, base := initializeRepository(t, "controlled-helper")
	project, task := createVerticalRecords(t, store, "controlled-helper", source)
	worktree := source + "-worktree"
	scope := execution.Scope{ProjectID: project.ID, WorkspaceID: task.WorkspaceIDs[0], TaskID: task.ID, RunID: "run-controlled-helper"}
	environment := fake.NewEnvironment(fake.Options{
		SourcePath: source, WorktreePath: worktree, Branch: "task/" + task.ID, BaseSHA: base,
		Operational: operationalObservation("controlled-helper"), LoseEveryMutationResponse: loseResponses,
		GlobalActiveAgents: globalActive,
	})
	t.Cleanup(environment.RemoveFixture)
	controller := executionapp.NewController(store, environment, environment, environment)
	command := startCommand(t, task, scope, source, worktree, base, eligibilityFacts(scope, execution.LifecycleSurfaces{}))
	if mutate != nil {
		mutate(&command)
	}
	if _, err := controller.Start(context.Background(), command); err != nil {
		t.Fatal(err)
	}
	run := runSteps(t, store, environment, scope.RunID, func(current domain.Run) bool {
		return current.Execution.AgentPrompt.Observation != nil && current.Execution.AgentPrompt.Observation.Status == execution.ObservationOwnedPresent
	})
	event, err := environment.TerminalEvent(execution.CompletionEventFinished, run.Execution.AgentPrompt.ID, testNowMillis())
	if err != nil {
		t.Fatal(err)
	}
	if err := controller.RecordCompletionEvent(context.Background(), run.ID, event, testNowMillis()); err != nil {
		t.Fatal(err)
	}
	run = runSteps(t, store, environment, scope.RunID, func(current domain.Run) bool { return current.Execution.AgentPrompt.Phase == execution.EffectComplete })
	return fixture, environment, project, task, run
}

func stepHelperUntil(t *testing.T, fixture *verticalDolt, environment *fake.Environment, runID, helperID string, stop func(execution.Helper) bool) domain.Run {
	t.Helper()
	store := openVerticalStore(t, fixture)
	for step := 0; step < 100; step++ {
		run, err := store.Run(context.Background(), runID)
		if err != nil {
			t.Fatal(err)
		}
		index := execution.HelperIndex(run.Execution.Helpers, helperID)
		if index < 0 {
			t.Fatal("durable helper disappeared")
		}
		if stop(run.Execution.Helpers[index]) {
			return run
		}
		controller := executionapp.NewController(store, environment.Restart(), environment.Restart(), environment.Restart())
		if _, err := controller.StepHelper(context.Background(), runID, helperID, testNowMillis()); err != nil {
			t.Fatal(err)
		}
	}
	current, _ := store.Run(context.Background(), runID)
	index := execution.HelperIndex(current.Execution.Helpers, helperID)
	if index >= 0 {
		helper := current.Execution.Helpers[index]
		t.Fatalf("helper transition did not converge: phase=%s need=%#v remove=%#v helper=%#v", helper.Phase, helper.NeedsYou, helper.CheckoutRemove.Observation, helper)
	}
	t.Fatal("helper transition did not converge")
	return domain.Run{}
}

func helperGit(t *testing.T, directory string, arguments ...string) string {
	t.Helper()
	command := exec.Command("git", arguments...)
	command.Dir = directory
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v: %s", arguments, err, output)
	}
	return string(bytes.TrimSpace(output))
}

func helperLabels(t *testing.T, environment *fake.Environment, run domain.Run, helper execution.Helper) map[string]string {
	t.Helper()
	registration, err := environment.HelperRegistration(run.Execution.Scope, helper, run.Execution.RootWorkspaceID, run.Execution.HostView.ExternalID, run.BaseSHA, run.Execution.EffectiveProfilesSHA256)
	if err != nil {
		t.Fatal(err)
	}
	labels, err := host.HelperLabels(registration)
	if err != nil {
		t.Fatal(err)
	}
	return labels
}

func admitAndSeedHelper(t *testing.T, fixture *verticalDolt, environment *fake.Environment, run domain.Run, requestID string, mode execution.HelperMode) (domain.Run, execution.Helper) {
	t.Helper()
	store := openVerticalStore(t, fixture)
	requested, err := executionapp.NewController(store, environment, environment, environment).RequestHelper(context.Background(), executionapp.RequestHelperCommand{
		RequestID: requestID, RunID: run.ID, ParentAgentID: run.Execution.Agent.ExternalID,
		Mode: mode, Purpose: "bounded budget interaction",
	}, testNowMillis())
	if err != nil {
		t.Fatal(err)
	}
	helperID := requested.Helper.ID
	run = stepHelperUntil(t, fixture, environment, run.ID, helperID, func(helper execution.Helper) bool {
		return helper.Phase == execution.HelperAdmissionReady || helper.Phase == execution.HelperParked
	})
	helper := run.Execution.Helpers[execution.HelperIndex(run.Execution.Helpers, helperID)]
	if helper.Phase == execution.HelperParked {
		return run, helper
	}
	consumed, err := executionapp.NewController(store, environment, environment, environment).ConsumeHelperAdmission(
		context.Background(), run.ID, helperID, "invoke-"+requestID, testNowMillis(),
	)
	if err != nil {
		t.Fatal(err)
	}
	if consumed.Helper.Phase != execution.HelperInvocationConsumed {
		return consumed.Run, consumed.Helper
	}
	labels := helperLabels(t, environment, consumed.Run, consumed.Helper)
	if _, err := environment.SeedHelperAgent(consumed.Helper, labels, consumed.Run.Execution.HostView.ExternalID); err != nil {
		t.Fatal(err)
	}
	run = stepHelperUntil(t, fixture, environment, run.ID, helperID, func(helper execution.Helper) bool { return helper.Phase == execution.HelperActive })
	return run, run.Execution.Helpers[execution.HelperIndex(run.Execution.Helpers, helperID)]
}

func recordHelperUsage(t *testing.T, store *dolt.DoltTaskStore, environment *fake.Environment, runID, helperID string, sequence, inputTokens, outputTokens, costMicrousd uint64, costPresent bool) domain.Run {
	t.Helper()
	run, err := store.Run(context.Background(), runID)
	if err != nil {
		t.Fatal(err)
	}
	helper := run.Execution.Helpers[execution.HelperIndex(run.Execution.Helpers, helperID)]
	observation := runtimebudget.ProviderObservation{
		ID: "helper-usage-" + helperID + "-" + string(rune('a'+sequence)), EffectID: helper.BudgetEffectID,
		AgentID: helper.NativeAgentID, Activity: runtimebudget.ActivityHelperTurn,
		LeaseEpoch: run.Execution.LeaseBinding.Epoch, PolicyRevision: run.Execution.Budget.Policy.Revision,
		Sequence: sequence, ObservedAtMillis: testNowMillis(), ProviderUsage: runtimebudget.ProviderUsage{
			State: runtimebudget.UsageCurrent, SourceRevision: "helper-source-" + helperID + "-" + string(rune('a'+sequence)),
			InputTokensPresent: true, InputTokens: inputTokens, OutputTokensPresent: true, OutputTokens: outputTokens,
			CostMicrousdPresent: costPresent, CostMicrousd: costMicrousd,
		},
	}
	observation.FactHash = runtimebudget.ProviderObservationHash(observation)
	result, err := executionapp.NewController(store, environment, environment, environment).RecordHelperProviderUsage(
		context.Background(), runID, helperID, run.Version, run.Execution.LeaseBinding.Epoch, observation,
	)
	if err != nil {
		t.Fatal(err)
	}
	return result.Run
}

func TestControlledWriterHelperIsolatedCommitHandoffReplayAndCleanup(t *testing.T) {
	fixture, environment, _, _, run := activePrimaryRun(t, true, 1)
	store := openVerticalStore(t, fixture)
	primaryHead := helperGit(t, run.Execution.WorktreePath, "rev-parse", "HEAD")
	controller := executionapp.NewController(store, environment, environment, environment)
	requested, err := controller.RequestHelper(context.Background(), executionapp.RequestHelperCommand{
		RequestID: "request-writer-1", RunID: run.ID, ParentAgentID: run.Execution.Agent.ExternalID,
		Mode: execution.HelperWriter, Purpose: "implement one isolated contribution",
	}, testNowMillis())
	if err != nil {
		t.Fatal(err)
	}
	helperID := requested.Helper.ID

	oracleSnapshot := productionOracleSnapshot(run)
	oracleSnapshot.Workspace.State = executionoracle.WorkspaceReady
	oracleSnapshot.Agents = []executionoracle.AgentFact{{
		ID: run.Execution.Agent.ExternalID, Role: executionoracle.RoleWorker, RunID: run.ID,
		WorkspaceID: run.Execution.Scope.WorkspaceID, ProfileRevision: run.Execution.EffectiveProfilesSHA256,
		State: executionoracle.AgentRunning, BootstrapDone: true, Prompt: executionoracle.PromptFinished,
	}}
	oracleSnapshot.HelperRequest = executionoracle.HelperRequestFact{
		State: executionoracle.HelperRequested, RunID: run.ID, WorkspaceID: run.Execution.Scope.WorkspaceID,
		ParentID: run.Execution.Agent.ExternalID, IntentID: helperID,
	}
	if action := requireOracleAction(t, oracleSnapshot, executionoracle.ActionIssueHelperAdmission); action.ParentID != run.Execution.Agent.ExternalID {
		t.Fatalf("oracle parent = %q", action.ParentID)
	}

	run = stepHelperUntil(t, fixture, environment, run.ID, helperID, func(helper execution.Helper) bool { return helper.Phase == execution.HelperAdmissionReady })
	helper := run.Execution.Helpers[execution.HelperIndex(run.Execution.Helpers, helperID)]
	if helper.WorktreePath == run.Execution.WorktreePath || helper.Checkout.ExternalID == "" || helper.Boundary.ExternalID == "" ||
		helper.BoundaryObservation == nil || !helper.BoundaryObservation.PrimaryCheckoutUnavailable || !helper.BoundaryObservation.OperationalTelemetryReady {
		t.Fatalf("writer helper boundary = %#v", helper)
	}
	if head := helperGit(t, run.Execution.WorktreePath, "rev-parse", "HEAD"); head != primaryHead {
		t.Fatalf("primary HEAD changed during helper preparation: %s", head)
	}
	if status := helperGit(t, run.Execution.WorktreePath, "status", "--porcelain=v1"); status != "" {
		t.Fatalf("primary checkout mutated: %q", status)
	}
	if _, err := os.Stat(filepath.Join(helper.WorktreePath, ".git", "objects", "info", "alternates")); !os.IsNotExist(err) {
		t.Fatal("writer helper uses shared writable object storage")
	}

	consumed, err := executionapp.NewController(store, environment, environment, environment).ConsumeHelperAdmission(context.Background(), run.ID, helperID, "invoke-writer-1", testNowMillis())
	if err != nil {
		t.Fatal(err)
	}
	helper = consumed.Helper
	oracleSnapshot.HelperRequest.State = executionoracle.HelperAdmitted
	if action := requireOracleAction(t, oracleSnapshot, executionoracle.ActionObserveHelper); action.ParentID != run.Execution.Agent.ExternalID {
		t.Fatalf("oracle helper observation parent = %q", action.ParentID)
	}
	labels := helperLabels(t, environment, consumed.Run, helper)
	nativeID, err := environment.SeedHelperAgent(helper, labels, consumed.Run.Execution.HostView.ExternalID)
	if err != nil {
		t.Fatal(err)
	}
	run = stepHelperUntil(t, fixture, environment, run.ID, helperID, func(helper execution.Helper) bool { return helper.Phase == execution.HelperActive })
	helper = run.Execution.Helpers[execution.HelperIndex(run.Execution.Helpers, helperID)]
	if helper.NativeAgentID != nativeID || helper.ParentAgentID != run.Execution.Agent.ExternalID || helper.Scope != run.Execution.Scope {
		t.Fatalf("helper identity = %#v", helper)
	}

	commitSHA, err := environment.ProduceHelperCommit(helper, "HELPER.md")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := executionapp.NewController(store, environment, environment, environment).RecordHelperContribution(context.Background(), run.ID, helperID, nativeID, commitSHA, run.BaseSHA, testNowMillis()); err != nil {
		t.Fatal(err)
	}
	run = stepHelperUntil(t, fixture, environment, run.ID, helperID, func(helper execution.Helper) bool { return helper.Phase == execution.HelperHandoffReady })
	helper = run.Execution.Helpers[execution.HelperIndex(run.Execution.Helpers, helperID)]
	if helper.Handoff == nil || helper.Handoff.CommitSHA != commitSHA {
		t.Fatalf("handoff = %#v", helper.Handoff)
	}
	run = recordHelperUsage(t, store, environment, run.ID, helperID, 1, 5, 7, 0, false)
	helper = run.Execution.Helpers[execution.HelperIndex(run.Execution.Helpers, helperID)]
	if helper.BudgetEvidenceID == "" {
		t.Fatal("writer helper usage was not recorded")
	}
	if head := helperGit(t, run.Execution.WorktreePath, "rev-parse", "HEAD"); head != primaryHead {
		t.Fatal("commit handoff moved primary HEAD")
	}
	if status := helperGit(t, run.Execution.WorktreePath, "status", "--porcelain=v1"); status != "" {
		t.Fatalf("commit handoff mutated primary checkout: %q", status)
	}
	if got := helperGit(t, run.Execution.SourcePath, "cat-file", "-t", commitSHA); got != "commit" {
		t.Fatalf("imported object type = %q", got)
	}
	startup, err := executionapp.NewController(store, environment.Restart(), environment.Restart(), environment.Restart()).ReconcileStartup(context.Background(), executionapp.StartupCommand{
		SchemaVersion: executionapp.StartupCommandSchemaVersion, RequestID: "startup-with-helper-handoff", NowMillis: testNowMillis(),
	})
	if err != nil || len(startup.Runs) != 1 {
		t.Fatalf("helper startup reconciliation = %#v, %v", startup, err)
	}
	run, err = store.Run(context.Background(), run.ID)
	if err != nil {
		t.Fatal(err)
	}
	helper = run.Execution.Helpers[execution.HelperIndex(run.Execution.Helpers, helperID)]
	if helper.Handoff == nil || helper.Handoff.CommitSHA != commitSHA || environment.MutationCount(execution.EffectHelperCommitHandoff) != 1 ||
		run.Execution.LastStartupReconciliation == nil || run.Execution.LastStartupReconciliation.HelperCount != 1 ||
		run.Execution.LastStartupReconciliation.HelperChainHash != execution.HelperGraphHash(run.Execution.Helpers) {
		t.Fatalf("startup changed helper handoff: %#v", helper.Handoff)
	}

	if _, err := executionapp.NewController(store, environment, environment, environment).RequestHelperCleanup(context.Background(), run.ID, helperID, testNowMillis()); err != nil {
		t.Fatal(err)
	}
	run = stepHelperUntil(t, fixture, environment, run.ID, helperID, func(helper execution.Helper) bool { return helper.Phase == execution.HelperTerminal })
	if _, err := os.Stat(helper.WorktreePath); !os.IsNotExist(err) {
		t.Fatal("writer helper checkout remains after verified cleanup")
	}
	if environment.HelperReservationCount() != 0 {
		t.Fatal("helper capacity reservation remains after cleanup")
	}
	for _, kind := range []execution.EffectKind{execution.EffectHelperCheckoutCreate, execution.EffectHelperBoundary, execution.EffectHelperCommitHandoff, execution.EffectHelperAgentArchive, execution.EffectHelperCheckoutRemove} {
		if environment.MutationCount(kind) != 1 {
			t.Fatalf("%s mutation count = %d", kind, environment.MutationCount(kind))
		}
	}
}

func TestControlledHelperUnknownCreationParksAndLaterContainsObservedOrphan(t *testing.T) {
	fixture, environment, _, _, run := activePrimaryRun(t, false, 1)
	store := openVerticalStore(t, fixture)
	requested, err := executionapp.NewController(store, environment, environment, environment).RequestHelper(context.Background(), executionapp.RequestHelperCommand{
		RequestID: "request-orphan", RunID: run.ID, ParentAgentID: run.Execution.Agent.ExternalID,
		Mode: execution.HelperReadOnly, Purpose: "inspect only",
	}, testNowMillis())
	if err != nil {
		t.Fatal(err)
	}
	helperID := requested.Helper.ID
	run = stepHelperUntil(t, fixture, environment, run.ID, helperID, func(helper execution.Helper) bool { return helper.Phase == execution.HelperAdmissionReady })
	prepared := run.Execution.Helpers[execution.HelperIndex(run.Execution.Helpers, helperID)]
	if prepared.WorktreePath != run.Execution.WorktreePath || prepared.BoundaryObservation == nil ||
		!prepared.BoundaryObservation.PrimaryCheckoutReadOnly || prepared.BoundaryObservation.SeparateCheckout {
		t.Fatalf("read-only sharing boundary = %#v", prepared.BoundaryObservation)
	}
	consumed, err := executionapp.NewController(store, environment, environment, environment).ConsumeHelperAdmission(context.Background(), run.ID, helperID, "invoke-orphan", testNowMillis())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := executionapp.NewController(store, environment, environment, environment).StepHelper(context.Background(), run.ID, helperID, testNowMillis()); err != nil {
		t.Fatal(err)
	}
	parked, err := store.Run(context.Background(), run.ID)
	if err != nil {
		t.Fatal(err)
	}
	helper := parked.Execution.Helpers[execution.HelperIndex(parked.Execution.Helpers, helperID)]
	if helper.Phase != execution.HelperParked || helper.NeedsYou == nil || helper.NeedsYou.Code != execution.NeedHelperCreationUnknown || helper.NeedsYou.CleanupAuthorized {
		t.Fatalf("unknown helper = %#v", helper)
	}

	labels := helperLabels(t, environment, consumed.Run, consumed.Helper)
	if _, err := environment.SeedHelperAgent(consumed.Helper, labels, consumed.Run.Execution.HostView.ExternalID); err != nil {
		t.Fatal(err)
	}
	if _, err := executionapp.NewController(store, environment.Restart(), environment.Restart(), environment.Restart()).StepHelper(context.Background(), run.ID, helperID, testNowMillis()); err != nil {
		t.Fatal(err)
	}
	recovered, err := store.Run(context.Background(), run.ID)
	if err != nil {
		t.Fatal(err)
	}
	helper = recovered.Execution.Helpers[execution.HelperIndex(recovered.Execution.Helpers, helperID)]
	if helper.Phase != execution.HelperActive || recovered.Execution.NeedsYou != nil {
		t.Fatalf("observed orphan was not reconciled: %#v", helper)
	}
	recovered = recordHelperUsage(t, store, environment, run.ID, helperID, 1, 2, 1, 0, false)
	if _, err := executionapp.NewController(store, environment, environment, environment).RequestHelperCleanup(context.Background(), run.ID, helperID, testNowMillis()); err != nil {
		t.Fatal(err)
	}
	stepHelperUntil(t, fixture, environment, run.ID, helperID, func(helper execution.Helper) bool { return helper.Phase == execution.HelperTerminal })
}

func TestControlledHelperQuotaAndGlobalCapacitySerialize(t *testing.T) {
	fixture, environment, _, _, run := activePrimaryRun(t, false, 7)
	store := openVerticalStore(t, fixture)
	// One CAS winner per request identity proves a concurrent replay cannot
	// reserve more than one helper slot.
	var wait sync.WaitGroup
	errorsSeen := make(chan error, 16)
	for index := 0; index < 16; index++ {
		wait.Add(1)
		go func() {
			defer wait.Done()
			_, err := executionapp.NewController(store, environment, environment, environment).RequestHelper(context.Background(), executionapp.RequestHelperCommand{
				RequestID: "same-concurrent-helper", RunID: run.ID, ParentAgentID: run.Execution.Agent.ExternalID,
				Mode: execution.HelperReadOnly, Purpose: "same bounded request",
			}, testNowMillis())
			errorsSeen <- err
		}()
	}
	wait.Wait()
	close(errorsSeen)
	for err := range errorsSeen {
		if err != nil && err.Error() != "persist execution transition: version conflict" {
			t.Fatalf("concurrent request error = %v", err)
		}
	}
	current, err := store.Run(context.Background(), run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(current.Execution.Helpers) != 1 {
		t.Fatalf("concurrent helper count = %d", len(current.Execution.Helpers))
	}
	firstHelper := current.Execution.Helpers[0]
	current = stepHelperUntil(t, fixture, environment, run.ID, firstHelper.ID, func(helper execution.Helper) bool { return helper.Phase == execution.HelperAdmissionReady })
	consumeErrors := make(chan error, 16)
	wait = sync.WaitGroup{}
	for index := 0; index < 16; index++ {
		wait.Add(1)
		go func() {
			defer wait.Done()
			_, consumeErr := executionapp.NewController(store, environment, environment, environment).ConsumeHelperAdmission(
				context.Background(), run.ID, firstHelper.ID, "same-helper-invocation", testNowMillis(),
			)
			consumeErrors <- consumeErr
		}()
	}
	wait.Wait()
	close(consumeErrors)
	for consumeErr := range consumeErrors {
		if consumeErr != nil && consumeErr.Error() != "persist execution transition: version conflict" &&
			!errors.Is(consumeErr, storeport.ErrIdempotencyConflict) {
			t.Fatalf("concurrent consume error = %v", consumeErr)
		}
	}
	current, err = store.Run(context.Background(), run.ID)
	if err != nil {
		t.Fatal(err)
	}
	firstHelper = current.Execution.Helpers[execution.HelperIndex(current.Execution.Helpers, firstHelper.ID)]
	if firstHelper.Phase != execution.HelperInvocationConsumed || firstHelper.BudgetReservationID == "" {
		t.Fatalf("concurrent helper admission = %#v", firstHelper)
	}
	helpersReserved := 0
	for _, reservation := range current.Execution.Budget.Reservations {
		if reservation.Activity == runtimebudget.ActivityHelperTurn && !reservation.Released {
			helpersReserved++
		}
	}
	if helpersReserved != 1 {
		t.Fatalf("helper budget reservations = %d", helpersReserved)
	}
	second, err := executionapp.NewController(store, environment, environment, environment).RequestHelper(context.Background(), executionapp.RequestHelperCommand{
		RequestID: "second-capacity-helper", RunID: run.ID, ParentAgentID: run.Execution.Agent.ExternalID,
		Mode: execution.HelperReadOnly, Purpose: "must wait for global slot",
	}, testNowMillis())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := executionapp.NewController(store, environment, environment, environment).StepHelper(context.Background(), run.ID, second.Helper.ID, testNowMillis()); err != nil {
		t.Fatal(err)
	}
	current, err = store.Run(context.Background(), run.ID)
	if err != nil {
		t.Fatal(err)
	}
	secondHelper := current.Execution.Helpers[execution.HelperIndex(current.Execution.Helpers, second.Helper.ID)]
	if secondHelper.NeedsYou == nil || secondHelper.NeedsYou.Code != execution.NeedHelperCapacity {
		t.Fatalf("concurrent global capacity = %#v", secondHelper)
	}

	// A separate Run at the exact Project ceiling parks before checkout,
	// boundary, or native-helper work.
	capacityFixture, capacityEnvironment, _, _, capacityRun := activePrimaryRun(t, false, 8)
	capacityStore := openVerticalStore(t, capacityFixture)
	requested, err := executionapp.NewController(capacityStore, capacityEnvironment, capacityEnvironment, capacityEnvironment).RequestHelper(context.Background(), executionapp.RequestHelperCommand{
		RequestID: "capacity-helper", RunID: capacityRun.ID, ParentAgentID: capacityRun.Execution.Agent.ExternalID,
		Mode: execution.HelperWriter, Purpose: "must not dispatch",
	}, testNowMillis())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := executionapp.NewController(capacityStore, capacityEnvironment, capacityEnvironment, capacityEnvironment).StepHelper(context.Background(), capacityRun.ID, requested.Helper.ID, testNowMillis()); err != nil {
		t.Fatal(err)
	}
	capacityRun, err = capacityStore.Run(context.Background(), capacityRun.ID)
	if err != nil {
		t.Fatal(err)
	}
	capacityHelper := capacityRun.Execution.Helpers[execution.HelperIndex(capacityRun.Execution.Helpers, requested.Helper.ID)]
	if capacityHelper.NeedsYou == nil || capacityHelper.NeedsYou.Code != execution.NeedHelperCapacity || capacityEnvironment.MutationCount(execution.EffectHelperCheckoutCreate) != 0 {
		t.Fatalf("capacity refusal = %#v", capacityHelper)
	}
}

func TestControlledHelperRuntimeBudgetCountsTurnTokensCostAndWallTimeExactly(t *testing.T) {
	fixture, environment, _, _, run := activePrimaryRunWithCommand(t, false, 1, func(command *executionapp.StartCommand) {
		command.BudgetPolicy = runtimebudget.NewPolicy(command.EffectiveProfiles.ConfigurationSHA256(), 100_000, 10_000, 20, 10_000, 4)
		command.TurnBudgetDemand = runtimebudget.Demand{WallTimeMilliseconds: 1_000, Tokens: 100, Turns: 1, CostMicrousd: 500}
	})
	store := openVerticalStore(t, fixture)
	before := run.Execution.Budget.Consumption
	run, helper := admitAndSeedHelper(t, fixture, environment, run, "budget-counting", execution.HelperReadOnly)
	if helper.BudgetReservationID == "" || helper.BudgetEvidenceID != "" {
		t.Fatalf("helper reservation = %#v", helper)
	}
	run = recordHelperUsage(t, store, environment, run.ID, helper.ID, 1, 9, 3, 400, true)
	after := run.Execution.Budget.Consumption
	if after.Turns != before.Turns+1 || after.Tokens != before.Tokens+12 || after.CostMicrousd != before.CostMicrousd+400 ||
		after.WallTimeMilliseconds != uint64(run.Execution.Budget.LastObservedAtMillis-run.Execution.Budget.StartedAtMillis) {
		t.Fatalf("helper budget delta before=%#v after=%#v", before, after)
	}
	helper = run.Execution.Helpers[execution.HelperIndex(run.Execution.Helpers, helper.ID)]
	if !helperBudgetReleasedForTest(run, helper) {
		t.Fatalf("helper budget evidence = %#v", helper)
	}
}

func helperBudgetReleasedForTest(run domain.Run, helper execution.Helper) bool {
	for _, reservation := range run.Execution.Budget.Reservations {
		if reservation.ID == helper.BudgetReservationID && reservation.Activity == runtimebudget.ActivityHelperTurn {
			return reservation.Released && reservation.EvidenceID == helper.BudgetEvidenceID
		}
	}
	return false
}

func TestControlledHelperSoftAndHardBudgetPreventNativeDispatch(t *testing.T) {
	tests := []struct {
		name               string
		tokenLimit, demand uint64
		want               runtimebudget.ReasonCode
	}{
		{name: "soft", tokenLimit: 100, demand: 61, want: runtimebudget.ReasonTokensSoft},
		{name: "hard", tokenLimit: 60, demand: 36, want: runtimebudget.ReasonTokensHard},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fixture, environment, _, _, run := activePrimaryRunWithCommand(t, false, 1, func(command *executionapp.StartCommand) {
				command.BudgetPolicy = runtimebudget.NewPolicy(command.EffectiveProfiles.ConfigurationSHA256(), 100_000, test.tokenLimit, 20, 0, 4)
				command.TurnBudgetDemand = runtimebudget.Demand{WallTimeMilliseconds: 100, Tokens: test.demand, Turns: 1}
			})
			run, helper := admitAndSeedHelper(t, fixture, environment, run, "budget-"+test.name, execution.HelperReadOnly)
			if run.Execution.NeedsYou == nil || run.Execution.NeedsYou.Code != execution.NeedCode(test.want) || run.Execution.NeedsYou.CleanupAuthorized ||
				helper.Phase != execution.HelperAdmissionReady || helper.Admission == nil || helper.Admission.ConsumedAtMillis != 0 ||
				helper.BudgetReservationID != "" || helper.NativeAgentID != "" {
				t.Fatalf("%s helper budget refusal: run=%#v helper=%#v", test.name, run.Execution.NeedsYou, helper)
			}
			if environment.HelperReservationCount() != 1 {
				t.Fatal("budget refusal orphaned the tracked global-capacity reservation")
			}
		})
	}
}

func TestControlledHelperUnavailableAndAmbiguousUsageReconcileEvidenceOnlyWithoutOrphan(t *testing.T) {
	for _, state := range []runtimebudget.UsageState{runtimebudget.UsageUnavailable, runtimebudget.UsageAmbiguous} {
		t.Run(string(state), func(t *testing.T) {
			fixture, environment, _, _, run := activePrimaryRunWithCommand(t, false, 1, func(command *executionapp.StartCommand) {
				command.BudgetPolicy = runtimebudget.NewPolicy(command.EffectiveProfiles.ConfigurationSHA256(), 100_000, 10_000, 20, 10_000, 4)
				command.TurnBudgetDemand = runtimebudget.Demand{WallTimeMilliseconds: 1_000, Tokens: 100, Turns: 1, CostMicrousd: 500}
			})
			store := openVerticalStore(t, fixture)
			run, helper := admitAndSeedHelper(t, fixture, environment, run, "telemetry-"+string(state), execution.HelperReadOnly)
			observation := runtimebudget.ProviderObservation{
				ID: "helper-usage-" + string(state), EffectID: helper.BudgetEffectID, AgentID: helper.NativeAgentID,
				Activity: runtimebudget.ActivityHelperTurn, LeaseEpoch: run.Execution.LeaseBinding.Epoch,
				PolicyRevision: run.Execution.Budget.Policy.Revision, Sequence: 1, ObservedAtMillis: testNowMillis(),
				ProviderUsage: runtimebudget.ProviderUsage{State: state},
			}
			observation.FactHash = runtimebudget.ProviderObservationHash(observation)
			result, err := executionapp.NewController(store, environment, environment, environment).RecordHelperProviderUsage(
				context.Background(), run.ID, helper.ID, run.Version, run.Execution.LeaseBinding.Epoch, observation,
			)
			if err != nil {
				t.Fatal(err)
			}
			want := runtimebudget.ReasonProviderUsageUnavailable
			if state == runtimebudget.UsageAmbiguous {
				want = runtimebudget.ReasonProviderUsageAmbiguous
			}
			parked := result.Run
			parkedHelper := parked.Execution.Helpers[execution.HelperIndex(parked.Execution.Helpers, helper.ID)]
			if parked.Execution.NeedsYou == nil || parked.Execution.NeedsYou.Code != execution.NeedCode(want) ||
				parked.Execution.NeedsYou.CleanupAuthorized || parkedHelper.NativeAgentID != helper.NativeAgentID ||
				parkedHelper.BudgetEvidenceID != "" || environment.HelperReservationCount() != 1 {
				t.Fatalf("%s usage park = %#v helper=%#v", state, parked.Execution.NeedsYou, parkedHelper)
			}
			if _, err := executionapp.NewController(store, environment, environment, environment).RequestHelper(context.Background(), executionapp.RequestHelperCommand{
				RequestID: "blocked-after-" + string(state), RunID: run.ID, ParentAgentID: run.Execution.Agent.ExternalID,
				Mode: execution.HelperReadOnly, Purpose: "must remain blocked",
			}, testNowMillis()); err == nil {
				t.Fatal("budget-paused Run admitted a new helper")
			}

			complete := runtimebudget.ProviderObservation{
				ID: "helper-usage-recovered-" + string(state), EffectID: helper.BudgetEffectID, AgentID: helper.NativeAgentID,
				Activity: runtimebudget.ActivityHelperTurn, LeaseEpoch: run.Execution.LeaseBinding.Epoch,
				PolicyRevision: run.Execution.Budget.Policy.Revision, Sequence: 2, ObservedAtMillis: testNowMillis(),
				ProviderUsage: runtimebudget.ProviderUsage{
					State: runtimebudget.UsageCurrent, SourceRevision: "helper-recovered-" + string(state),
					InputTokensPresent: true, InputTokens: 4, OutputTokensPresent: true, OutputTokens: 2,
					CostMicrousdPresent: true, CostMicrousd: 100,
				},
			}
			complete.FactHash = runtimebudget.ProviderObservationHash(complete)
			recovered, err := executionapp.NewController(store, environment.Restart(), environment.Restart(), environment.Restart()).RecordHelperProviderUsage(
				context.Background(), run.ID, helper.ID, parked.Version, run.Execution.LeaseBinding.Epoch, complete,
			)
			if err != nil || recovered.Run.Execution.NeedsYou != nil {
				t.Fatalf("evidence-only recovery = %#v, %v", recovered, err)
			}
			recoveredHelper := recovered.Run.Execution.Helpers[execution.HelperIndex(recovered.Run.Execution.Helpers, helper.ID)]
			if !helperBudgetReleasedForTest(recovered.Run, recoveredHelper) || recovered.Run.Execution.Budget.Consumption.Turns == run.Execution.Budget.Consumption.Turns {
				t.Fatalf("recovered helper budget = %#v", recoveredHelper)
			}
			if _, err := executionapp.NewController(store, environment, environment, environment).RequestHelperCleanup(context.Background(), run.ID, helper.ID, testNowMillis()); err != nil {
				t.Fatal(err)
			}
			stepHelperUntil(t, fixture, environment, run.ID, helper.ID, func(value execution.Helper) bool { return value.Phase == execution.HelperTerminal })
			if environment.HelperReservationCount() != 0 {
				t.Fatal("recovered helper capacity reservation was not released")
			}
		})
	}
}

func TestControlledHelperRejectsWrongWorkspaceRepositoryRunParentAndUnavailableBoundary(t *testing.T) {
	fixture, environment, _, _, run := activePrimaryRun(t, false, 1)
	store := openVerticalStore(t, fixture)
	controller := executionapp.NewController(store, environment, environment, environment)
	if _, err := controller.RequestHelper(context.Background(), executionapp.RequestHelperCommand{
		RequestID: "wrong-run", RunID: "run-other", ParentAgentID: run.Execution.Agent.ExternalID,
		Mode: execution.HelperWriter, Purpose: "wrong run",
	}, testNowMillis()); err == nil {
		t.Fatal("wrong Run helper request was accepted")
	}
	if _, err := controller.RequestHelper(context.Background(), executionapp.RequestHelperCommand{
		RequestID: "wrong-parent", RunID: run.ID, ParentAgentID: "agent-other",
		Mode: execution.HelperWriter, Purpose: "wrong parent",
	}, testNowMillis()); err == nil {
		t.Fatal("wrong parent helper request was accepted")
	}
	current, err := store.Run(context.Background(), run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(current.Execution.Helpers) != 0 {
		t.Fatal("rejected helper request reserved state")
	}

	persistTamper := func(current domain.Run, next domain.Run, suffix string) domain.Run {
		next.Version = current.Version + 1
		commandID := "helper-tamper-" + suffix
		result, updateErr := store.UpdateRun(context.Background(), domain.CommandRequest{
			IdempotencyKey: commandID, Type: "helper.test_tamper", AggregateID: current.ID,
			ExpectedVersion: current.Version, Payload: json.RawMessage(`{"test":"bounded"}`),
		}, next, domain.Event{
			ID: "event-" + commandID, RunID: current.ID, Sequence: current.Version + 2,
			AggregateID: current.ID, AggregateVersion: next.Version, Type: "helper.test_tamper",
			Payload: json.RawMessage(`{"test":"bounded"}`),
		})
		if updateErr != nil || result.Outcome != domain.CommandApplied {
			t.Fatalf("persist tamper: %#v %v", result, updateErr)
		}
		return next
	}
	original := current
	wrongWorkspace := current
	wrongWorkspace.Execution.Scope.WorkspaceID = "workspace-other"
	current = persistTamper(current, wrongWorkspace, "workspace")
	if _, err := executionapp.NewController(store, environment, environment, environment).RequestHelper(context.Background(), executionapp.RequestHelperCommand{
		RequestID: "wrong-workspace", RunID: run.ID, ParentAgentID: run.Execution.Agent.ExternalID,
		Mode: execution.HelperWriter, Purpose: "wrong workspace",
	}, testNowMillis()); err == nil {
		t.Fatal("wrong Workspace helper request was accepted")
	}
	restored := original
	restored.Version = current.Version + 1
	current = persistTamper(current, restored, "restore")
	wrongRepository := current
	wrongRepository.Execution.RepositoryBinding.RepositoryID = "repository-other"
	wrongRepository.Execution.RepositoryBindingHash = execution.RepositoryBindingSHA256(wrongRepository.Execution.RepositoryBinding)
	current = persistTamper(current, wrongRepository, "repository")
	if _, err := executionapp.NewController(store, environment, environment, environment).RequestHelper(context.Background(), executionapp.RequestHelperCommand{
		RequestID: "wrong-repository", RunID: run.ID, ParentAgentID: run.Execution.Agent.ExternalID,
		Mode: execution.HelperWriter, Purpose: "wrong repository",
	}, testNowMillis()); !errors.Is(err, executionapp.ErrRepositoryBindingChanged) {
		t.Fatalf("wrong repository error = %v", err)
	}
	restored = original
	restored.Version = current.Version + 1
	current = persistTamper(current, restored, "restore-repository")

	requested, err := executionapp.NewController(store, environment, environment, environment).RequestHelper(context.Background(), executionapp.RequestHelperCommand{
		RequestID: "missing-boundary", RunID: run.ID, ParentAgentID: run.Execution.Agent.ExternalID,
		Mode: execution.HelperReadOnly, Purpose: "must fail closed",
	}, testNowMillis())
	if err != nil {
		t.Fatal(err)
	}
	helperID := requested.Helper.ID
	// The first step records capacity and operational facts. Later preparation
	// may materialize its non-mutating fake boundary effect, but exact boundary
	// enforcement remains unavailable and therefore no admission is issued.
	for step := 0; step < 20; step++ {
		run, err = store.Run(context.Background(), run.ID)
		if err != nil {
			t.Fatal(err)
		}
		helper := run.Execution.Helpers[execution.HelperIndex(run.Execution.Helpers, helperID)]
		if helper.Phase == execution.HelperParked {
			break
		}
		adapter := missingHelperBoundary{Environment: environment}
		if _, err := executionapp.NewController(store, adapter, adapter, environment).StepHelper(context.Background(), run.ID, helperID, testNowMillis()); err != nil {
			t.Fatal(err)
		}
	}
	run, err = store.Run(context.Background(), run.ID)
	if err != nil {
		t.Fatal(err)
	}
	helper := run.Execution.Helpers[execution.HelperIndex(run.Execution.Helpers, helperID)]
	if helper.NeedsYou == nil || helper.NeedsYou.Code != execution.NeedHelperBoundaryInvalid || helper.Admission != nil || helper.NativeAgentID != "" || helper.NeedsYou.CleanupAuthorized {
		t.Fatalf("missing helper boundary = %#v", helper)
	}
}

func TestControlledHelperMissingOperationalTelemetryFailsClosed(t *testing.T) {
	fixture, environment, _, _, run := activePrimaryRun(t, false, 1)
	store := openVerticalStore(t, fixture)
	requested, err := executionapp.NewController(store, environment, environment, environment).RequestHelper(context.Background(), executionapp.RequestHelperCommand{
		RequestID: "missing-telemetry", RunID: run.ID, ParentAgentID: run.Execution.Agent.ExternalID,
		Mode: execution.HelperWriter, Purpose: "must fail without telemetry",
	}, testNowMillis())
	if err != nil {
		t.Fatal(err)
	}
	adapter := missingHelperTelemetry{Environment: environment}
	if _, err := executionapp.NewController(store, adapter, adapter, environment).StepHelper(context.Background(), run.ID, requested.Helper.ID, testNowMillis()); err != nil {
		t.Fatal(err)
	}
	current, err := store.Run(context.Background(), run.ID)
	if err != nil {
		t.Fatal(err)
	}
	helper := current.Execution.Helpers[execution.HelperIndex(current.Execution.Helpers, requested.Helper.ID)]
	if helper.NeedsYou == nil || helper.NeedsYou.Code != execution.NeedOperationalFactMissing || helper.Admission != nil ||
		environment.MutationCount(execution.EffectHelperCheckoutCreate) != 0 || helper.NeedsYou.CleanupAuthorized {
		t.Fatalf("missing telemetry helper = %#v", helper)
	}
}
