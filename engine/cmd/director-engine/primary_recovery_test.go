// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/mcuadros/director-engine/adapters/dolt"
	"github.com/mcuadros/director-engine/adapters/fake"
	executionapp "github.com/mcuadros/director-engine/application/execution"
	"github.com/mcuadros/director-engine/domain"
	"github.com/mcuadros/director-engine/domain/execution"
	runtimeport "github.com/mcuadros/director-engine/ports/runtime"
	storeport "github.com/mcuadros/director-engine/ports/taskstore"
	"github.com/mcuadros/director-engine/reducer/eligibility"
)

type runtimeWithoutRecovery struct{ inner runtimeport.Port }

func (runtime runtimeWithoutRecovery) ObserveEffect(ctx context.Context, request runtimeport.Request) (execution.EffectObservation, error) {
	return runtime.inner.ObserveEffect(ctx, request)
}

func (runtime runtimeWithoutRecovery) DispatchEffect(ctx context.Context, request runtimeport.Request) error {
	return runtime.inner.DispatchEffect(ctx, request)
}

func (runtime runtimeWithoutRecovery) ObserveOperational(ctx context.Context, scope execution.Scope, policy execution.OperationalPolicy) (execution.OperationalObservation, error) {
	return runtime.inner.ObserveOperational(ctx, scope, policy)
}

func (runtime runtimeWithoutRecovery) ObserveCandidate(ctx context.Context, request runtimeport.CandidateRequest) (execution.CandidateObservation, error) {
	return runtime.inner.ObserveCandidate(ctx, request)
}

type primaryRecoveryFixture struct {
	dolt        *verticalDolt
	store       *dolt.DoltTaskStore
	environment *fake.Environment
	project     domain.Project
	task        domain.Task
	run         domain.Run
}

func startPrimaryRecoveryFixture(t *testing.T, suffix string, options fake.Options) primaryRecoveryFixture {
	t.Helper()
	doltFixture := startVerticalDolt(t)
	store := openVerticalStore(t, doltFixture)
	source, base := initializeRepository(t, "recovery-"+suffix)
	project, task := createVerticalRecords(t, store, "recovery-"+suffix, source)
	worktree := filepath.Join(filepath.Dir(source), "owned-recovery-worktree-"+suffix)
	scope := execution.Scope{
		ProjectID: project.ID, WorkspaceID: task.WorkspaceIDs[0], TaskID: task.ID,
		RunID: "run-recovery-" + suffix,
	}
	options.SourcePath = source
	options.WorktreePath = worktree
	options.Branch = "task/" + task.ID
	options.BaseSHA = base
	options.Operational = operationalObservation("recovery-operational-" + suffix)
	environment := fake.NewEnvironment(options)
	t.Cleanup(environment.RemoveFixture)
	controller := executionapp.NewController(store, environment, environment, environment)
	started, err := controller.Start(context.Background(), startCommand(
		t, task, scope, source, worktree, base, eligibilityFacts(scope, execution.LifecycleSurfaces{}),
	))
	if err != nil || started.Decision.Kind != eligibility.DecisionEligible {
		t.Fatalf("start recovery fixture = %#v, %v", started, err)
	}
	run := runSteps(t, store, environment, scope.RunID, func(current domain.Run) bool {
		return current.Execution.AgentPrompt.Observation != nil &&
			current.Execution.AgentPrompt.Observation.Status == execution.ObservationOwnedPresent
	})
	return primaryRecoveryFixture{dolt: doltFixture, store: store, environment: environment, project: project, task: task, run: run}
}

func requestFailedPrimaryRecovery(t *testing.T, fixture *primaryRecoveryFixture, failures uint32) {
	t.Helper()
	ctx := context.Background()
	now := testNowMillis()
	controller := executionapp.NewController(fixture.store, fixture.environment, fixture.environment, fixture.environment)
	event, err := fixture.environment.TerminalEvent(execution.CompletionEventError, fixture.run.Execution.AgentPrompt.ID, now)
	if err != nil {
		t.Fatal(err)
	}
	if err := controller.RecordCompletionEvent(ctx, fixture.run.ID, event, now); err != nil {
		t.Fatal(err)
	}
	run, err := fixture.store.Run(ctx, fixture.run.ID)
	if err != nil {
		t.Fatal(err)
	}
	command := executionapp.PrimaryRecoveryCommand{
		SchemaVersion: executionapp.PrimaryRecoveryCommandSchemaVersion,
		RequestID:     "request-primary-recovery-" + fixture.run.ID,
		RunID:         run.ID, ExpectedRunVersion: run.Version,
		Trigger: execution.RecoveryProviderFailure, RepeatedFailureCount: failures, NowMillis: now,
	}
	result, err := controller.RequestPrimaryRecovery(ctx, command)
	if err != nil || !result.Progressed {
		t.Fatalf("request recovery = %#v, %v", result, err)
	}
	replay, err := controller.RequestPrimaryRecovery(ctx, command)
	if err != nil || replay.Progressed || replay.Run.Version != result.Run.Version {
		t.Fatalf("request recovery replay = %#v, %v", replay, err)
	}
	fixture.run = result.Run
}

func acceptableRecoveryRaceError(err error) bool {
	if err == nil || errors.Is(err, fake.ErrResponseLost) {
		return true
	}
	return strings.Contains(err.Error(), "version conflict") || strings.Contains(err.Error(), "stale") ||
		errors.Is(err, storeport.ErrIdempotencyConflict)
}

func drivePrimaryRecovery(t *testing.T, fixture *primaryRecoveryFixture, competitors int) domain.Run {
	t.Helper()
	ctx := context.Background()
	for round := 0; round < 160; round++ {
		run, err := fixture.store.Run(ctx, fixture.run.ID)
		if err != nil {
			t.Fatal(err)
		}
		fixture.run = run
		if run.Execution.PrimaryRecovery.Phase == execution.PrimaryRecoveryComplete ||
			run.Execution.PrimaryRecovery.Phase == execution.PrimaryRecoveryNeedsYou {
			return run
		}
		replacement := run.Execution.PrimaryRecovery.ReplacementAgent
		if replacement.Observation != nil && replacement.Observation.Status == execution.ObservationOwnedPresent {
			now := testNowMillis()
			event, eventErr := fixture.environment.TerminalEvent(execution.CompletionEventFinished, replacement.ID, now)
			if eventErr != nil {
				t.Fatal(eventErr)
			}
			controller := executionapp.NewController(fixture.store, fixture.environment, fixture.environment, fixture.environment)
			if eventErr := controller.RecordCompletionEvent(ctx, run.ID, event, now); eventErr != nil {
				t.Fatal(eventErr)
			}
		}
		fixture.environment.RefreshObservationsAt(testNowMillis())
		errorsByWorker := make([]error, competitors)
		var wait sync.WaitGroup
		wait.Add(competitors)
		for worker := 0; worker < competitors; worker++ {
			go func(index int) {
				defer wait.Done()
				controller := executionapp.NewController(fixture.store, fixture.environment, fixture.environment, fixture.environment)
				_, errorsByWorker[index] = controller.ReconcilePrimaryRecovery(ctx, run.ID, testNowMillis())
			}(worker)
		}
		wait.Wait()
		var unexpected []string
		for _, workerErr := range errorsByWorker {
			if !acceptableRecoveryRaceError(workerErr) {
				unexpected = append(unexpected, workerErr.Error())
			}
		}
		if len(unexpected) > 0 {
			t.Fatalf("recovery round %d errors: %v", round, unexpected)
		}
	}
	t.Fatalf("primary recovery did not converge: %#v", fixture.run.Execution.PrimaryRecovery)
	return domain.Run{}
}

func TestPrimaryRecoveryReplacesPoisonedSessionOnceAcrossThirtyTwoCoordinators(t *testing.T) {
	const reviewerID = "reviewer-preserved"
	fixture := startPrimaryRecoveryFixture(t, "replace", fake.Options{
		LoseEveryMutationResponse: true,
		RecoveryFailureSignals:    []execution.ProviderFailureSignal{execution.ProviderFailurePolicy},
		RecoveryReviewerID:        reviewerID,
	})
	controller := executionapp.NewController(fixture.store, fixture.environment, fixture.environment, fixture.environment)
	reviewer := execution.ControlledAgentIdentity{
		ID: reviewerID, Role: execution.ControlledReviewer, WorkspaceID: "review-workspace",
		Title: "Independent review", WorktreePath: filepath.Join(filepath.Dir(fixture.run.Execution.WorktreePath), "review-checkout"),
		Labels: map[string]string{"director.role": "reviewer"}, ProfileSHA256: fixture.run.Execution.EffectiveProfilesSHA256,
	}
	registered, err := controller.RegisterControlledAgent(context.Background(), executionapp.AgentRegistrationCommand{
		RequestID: "register-reviewer-recovery", RunID: fixture.run.ID,
		ExpectedRunVersion: fixture.run.Version, LeaseEpoch: fixture.run.Execution.LeaseBinding.Epoch,
		Identity: reviewer,
	})
	if err != nil {
		t.Fatal(err)
	}
	fixture.run = registered
	requestFailedPrimaryRecovery(t, &fixture, 2)
	result := drivePrimaryRecovery(t, &fixture, 32)
	recovery := result.Execution.PrimaryRecovery
	if recovery.Phase != execution.PrimaryRecoveryComplete || !recovery.PoisonedSession ||
		recovery.Authority == nil || !execution.ValidReplacementAuthority(*recovery.Authority) {
		t.Fatalf("recovery result = %#v", recovery)
	}
	if result.Execution.Agent.ExternalID == recovery.OriginalAgent.ExternalID ||
		result.Execution.HostView.ExternalID != recovery.Authority.OldWorkspaceID ||
		result.Execution.WorkerVisibility == nil || result.Execution.WorkerVisibility.Phase != "recovering" ||
		result.Execution.WorkerVisibility.AgentID != result.Execution.Agent.ExternalID ||
		result.Execution.PrimarySession.NativeAgentID != result.Execution.Agent.ExternalID {
		t.Fatalf("replacement identity/visibility = %#v", result.Execution)
	}
	if fixture.environment.MutationCount(execution.EffectAgentCreate) != 2 ||
		fixture.environment.MutationCount(execution.EffectAgentPrompt) != 2 ||
		fixture.environment.MutationCount(execution.EffectAgentArchive) != 1 {
		t.Fatalf("mutation counts create=%d prompt=%d archive=%d order=%v",
			fixture.environment.MutationCount(execution.EffectAgentCreate),
			fixture.environment.MutationCount(execution.EffectAgentPrompt),
			fixture.environment.MutationCount(execution.EffectAgentArchive), fixture.environment.MutationOrder())
	}
	if fixture.environment.MutationCount(execution.EffectHostViewCreate) != 1 || !fixture.environment.WorktreePresent() {
		t.Fatal("replacement duplicated or removed the existing workspace/worktree")
	}
	foundOriginal, foundReplacement, foundReviewer := false, false, false
	for _, identity := range result.Execution.ControlledAgents {
		switch identity.ID {
		case recovery.OriginalAgent.ExternalID:
			foundOriginal = true
		case result.Execution.Agent.ExternalID:
			foundReplacement = true
		case reviewerID:
			foundReviewer = true
		}
	}
	if !foundOriginal || !foundReplacement || !foundReviewer {
		t.Fatalf("controlled resources were not preserved: %#v", result.Execution.ControlledAgents)
	}
	request := fixture.environment.PromptRequest()
	if request.AgentID != result.Execution.Agent.ExternalID || !request.NotifyOnFinish || request.ParentAgentID != nil ||
		!strings.Contains(request.InitialPrompt, "Use durable Task and Run facts only") {
		t.Fatalf("replacement prompt = %#v", request)
	}
	fixture.environment.RefreshObservationsAt(testNowMillis())
	startupController := executionapp.NewController(
		fixture.store, fixture.environment.Restart(), fixture.environment.Restart(), fixture.environment.Restart(),
	)
	startup, err := startupController.ReconcileStartup(context.Background(), executionapp.StartupCommand{
		SchemaVersion: executionapp.StartupCommandSchemaVersion,
		RequestID:     "startup-after-primary-replacement", NowMillis: testNowMillis(),
	})
	if err != nil || len(startup.Runs) != 1 || startup.Runs[0].AgentID != result.Execution.Agent.ExternalID {
		t.Fatalf("startup after replacement = %#v, %v", startup, err)
	}
}

func TestPrimaryRecoveryFailureAndOrphanMatrixParksWithoutMutation(t *testing.T) {
	cases := []struct {
		name    string
		options fake.Options
		code    execution.NeedCode
	}{
		{name: "authentication", options: fake.Options{RecoveryFailureSignals: []execution.ProviderFailureSignal{execution.ProviderFailureAuthentication}}, code: execution.NeedRecoveryFailureRequiresHuman},
		{name: "configuration", options: fake.Options{RecoveryFailureSignals: []execution.ProviderFailureSignal{execution.ProviderFailureConfiguration}}, code: execution.NeedRecoveryFailureRequiresHuman},
		{name: "unclassified", options: fake.Options{RecoveryFailureSignals: []execution.ProviderFailureSignal{execution.ProviderFailureUnknown}}, code: execution.NeedRecoveryFailureRequiresHuman},
		{name: "contradictory", options: fake.Options{RecoveryFailureSignals: []execution.ProviderFailureSignal{execution.ProviderFailurePolicy, execution.ProviderFailureAuthentication}}, code: execution.NeedRecoveryFailureRequiresHuman},
		{name: "duplicate agent", options: fake.Options{RecoveryFailureSignals: []execution.ProviderFailureSignal{execution.ProviderFailurePolicy}, RecoveryDuplicateAgents: 1}, code: execution.NeedRecoveryOrphanResource},
		{name: "duplicate workspace", options: fake.Options{RecoveryFailureSignals: []execution.ProviderFailureSignal{execution.ProviderFailurePolicy}, RecoveryDuplicateWorkspaces: 1}, code: execution.NeedRecoveryWorkspaceInvalid},
		{name: "orphan worktree", options: fake.Options{RecoveryFailureSignals: []execution.ProviderFailureSignal{execution.ProviderFailurePolicy}, RecoveryOrphanWorktree: true}, code: execution.NeedRecoveryOrphanResource},
	}
	for index, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			fixture := startPrimaryRecoveryFixture(t, fmt.Sprintf("matrix-%d", index), test.options)
			requestFailedPrimaryRecovery(t, &fixture, 2)
			result := drivePrimaryRecovery(t, &fixture, 1)
			if result.Execution.PrimaryRecovery.Phase != execution.PrimaryRecoveryNeedsYou ||
				result.Execution.NeedsYou == nil || result.Execution.NeedsYou.Code != test.code ||
				result.Execution.NeedsYou.CleanupAuthorized {
				t.Fatalf("park = %#v", result.Execution.PrimaryRecovery)
			}
			if fixture.environment.MutationCount(execution.EffectAgentCreate) != 1 ||
				fixture.environment.MutationCount(execution.EffectAgentArchive) != 0 || !fixture.environment.WorktreePresent() {
				t.Fatal("unsafe recovery mutated or removed a resource")
			}
		})
	}
}

func TestPrimaryRecoveryAdoptsExactSessionAcrossInterruptionTriggers(t *testing.T) {
	triggers := []execution.RecoveryTrigger{
		execution.RecoveryPluginReload, execution.RecoveryDaemonInterruption,
		execution.RecoveryTerminalCallbackLost, execution.RecoveryCoordinatorRestart,
	}
	for index, trigger := range triggers {
		t.Run(string(trigger), func(t *testing.T) {
			fixture := startPrimaryRecoveryFixture(t, fmt.Sprintf("adopt-%d", index), fake.Options{})
			run, err := fixture.store.Run(context.Background(), fixture.run.ID)
			if err != nil {
				t.Fatal(err)
			}
			controller := executionapp.NewController(fixture.store, fixture.environment.Restart(), fixture.environment.Restart(), fixture.environment.Restart())
			requested, err := controller.RequestPrimaryRecovery(context.Background(), executionapp.PrimaryRecoveryCommand{
				SchemaVersion: executionapp.PrimaryRecoveryCommandSchemaVersion,
				RequestID:     fmt.Sprintf("request-adopt-%d", index), RunID: run.ID, ExpectedRunVersion: run.Version,
				Trigger: trigger, RepeatedFailureCount: 1, NowMillis: testNowMillis(),
			})
			if err != nil {
				t.Fatal(err)
			}
			fixture.run = requested.Run
			result := drivePrimaryRecovery(t, &fixture, 1)
			if result.Execution.PrimaryRecovery.Phase != execution.PrimaryRecoveryComplete ||
				result.Execution.PrimaryRecovery.Authority != nil ||
				fixture.environment.MutationCount(execution.EffectAgentCreate) != 1 ||
				fixture.environment.MutationCount(execution.EffectAgentPrompt) != 1 {
				t.Fatalf("adoption result = %#v", result.Execution.PrimaryRecovery)
			}
		})
	}
}

func TestReplacementSecondFailureRoutesNeedsYouWithoutAnotherAuthority(t *testing.T) {
	fixture := startPrimaryRecoveryFixture(t, "second-failure", fake.Options{
		RecoveryFailureSignals: []execution.ProviderFailureSignal{execution.ProviderFailureTerminal},
	})
	requestFailedPrimaryRecovery(t, &fixture, 2)
	run := drivePrimaryRecovery(t, &fixture, 1)
	if run.Execution.PrimaryRecovery.Authority == nil {
		t.Fatal("first replacement authority is absent")
	}
	now := testNowMillis()
	event, err := fixture.environment.TerminalEvent(execution.CompletionEventError, run.Execution.AgentPrompt.ID, now)
	if err != nil {
		t.Fatal(err)
	}
	controller := executionapp.NewController(fixture.store, fixture.environment, fixture.environment, fixture.environment)
	if err := controller.RecordCompletionEvent(context.Background(), run.ID, event, now); err != nil {
		t.Fatal(err)
	}
	for step := 0; step < 8; step++ {
		fixture.environment.RefreshObservationsAt(testNowMillis())
		_, stepErr := controller.Step(context.Background(), run.ID, testNowMillis())
		if stepErr != nil && !acceptableRecoveryRaceError(stepErr) {
			t.Fatal(stepErr)
		}
		run, err = fixture.store.Run(context.Background(), run.ID)
		if err != nil {
			t.Fatal(err)
		}
		if run.Execution.NeedsYou != nil {
			break
		}
	}
	if run.Execution.NeedsYou == nil || run.Execution.NeedsYou.Code != execution.NeedRecoverySecondFailure ||
		fixture.environment.MutationCount(execution.EffectAgentCreate) != 2 {
		t.Fatalf("second failure = %#v", run.Execution)
	}
}

func TestReplacementPromptPossibleHandoffIsNeverRepeated(t *testing.T) {
	fixture := startPrimaryRecoveryFixture(t, "prompt-loss", fake.Options{
		RecoveryFailureSignals: []execution.ProviderFailureSignal{execution.ProviderFailurePolicy},
	})
	requestFailedPrimaryRecovery(t, &fixture, 2)
	ctx := context.Background()
	for step := 0; step < 100; step++ {
		run, err := fixture.store.Run(ctx, fixture.run.ID)
		if err != nil {
			t.Fatal(err)
		}
		fixture.run = run
		replacement := run.Execution.PrimaryRecovery.ReplacementAgent
		if replacement.Observation != nil && replacement.Observation.Status == execution.ObservationOwnedPresent {
			now := testNowMillis()
			event, eventErr := fixture.environment.TerminalEvent(execution.CompletionEventFinished, replacement.ID, now)
			if eventErr != nil {
				t.Fatal(eventErr)
			}
			controller := executionapp.NewController(fixture.store, fixture.environment, fixture.environment, fixture.environment)
			if eventErr := controller.RecordCompletionEvent(ctx, run.ID, event, now); eventErr != nil {
				t.Fatal(eventErr)
			}
		}
		if run.Execution.PrimaryRecovery.ReplacementPrompt.Attempt == 1 {
			break
		}
		fixture.environment.RefreshObservationsAt(testNowMillis())
		controller := executionapp.NewController(fixture.store, fixture.environment, fixture.environment, fixture.environment)
		_, stepErr := controller.ReconcilePrimaryRecovery(ctx, run.ID, testNowMillis())
		if stepErr != nil && !acceptableRecoveryRaceError(stepErr) {
			t.Fatal(stepErr)
		}
	}
	fixture.environment.DropCurrentPromptObservationForTest()
	for step := 0; step < 8; step++ {
		fixture.environment.RefreshObservationsAt(testNowMillis())
		controller := executionapp.NewController(fixture.store, fixture.environment, fixture.environment, fixture.environment)
		_, stepErr := controller.ReconcilePrimaryRecovery(ctx, fixture.run.ID, testNowMillis())
		if stepErr != nil && !acceptableRecoveryRaceError(stepErr) {
			t.Fatal(stepErr)
		}
		fixture.run, _ = fixture.store.Run(ctx, fixture.run.ID)
		if fixture.run.Execution.PrimaryRecovery.Phase == execution.PrimaryRecoveryNeedsYou {
			break
		}
	}
	if fixture.run.Execution.NeedsYou == nil || fixture.run.Execution.NeedsYou.Code != execution.NeedRecoveryPromptAmbiguous ||
		fixture.environment.MutationCount(execution.EffectAgentPrompt) != 2 {
		t.Fatalf("lost prompt recovery = %#v mutations=%d", fixture.run.Execution.PrimaryRecovery, fixture.environment.MutationCount(execution.EffectAgentPrompt))
	}
}

func TestTerminalCallbackAutomaticallyStartsProviderRecovery(t *testing.T) {
	fixture := startPrimaryRecoveryFixture(t, "terminal-callback", fake.Options{
		RecoveryFailureSignals: []execution.ProviderFailureSignal{execution.ProviderFailurePolicy},
	})
	ctx := context.Background()
	now := testNowMillis()
	event, err := fixture.environment.TerminalEvent(execution.CompletionEventError, fixture.run.Execution.AgentPrompt.ID, now)
	if err != nil {
		t.Fatal(err)
	}
	controller := executionapp.NewController(fixture.store, fixture.environment, fixture.environment, fixture.environment)
	if err := controller.RecordCompletionEvent(ctx, fixture.run.ID, event, now); err != nil {
		t.Fatal(err)
	}
	for step := 0; step < 12; step++ {
		fixture.environment.RefreshObservationsAt(testNowMillis())
		_, stepErr := controller.Step(ctx, fixture.run.ID, testNowMillis())
		if stepErr != nil && !acceptableRecoveryRaceError(stepErr) {
			t.Fatal(stepErr)
		}
		fixture.run, _ = fixture.store.Run(ctx, fixture.run.ID)
		if fixture.run.Execution.PrimaryRecovery.SchemaVersion != "" {
			break
		}
	}
	if fixture.run.Execution.PrimaryRecovery.Trigger != execution.RecoveryProviderFailure {
		t.Fatalf("automatic recovery intent = %#v", fixture.run.Execution.PrimaryRecovery)
	}
	result := drivePrimaryRecovery(t, &fixture, 1)
	if result.Execution.PrimaryRecovery.Phase != execution.PrimaryRecoveryComplete ||
		fixture.environment.MutationCount(execution.EffectAgentCreate) != 2 {
		t.Fatalf("automatic callback recovery = %#v", result.Execution.PrimaryRecovery)
	}
}

func TestPrimaryRecoveryDirectDoltReopenAtEveryFrontier(t *testing.T) {
	fixture := startPrimaryRecoveryFixture(t, "reopen", fake.Options{
		LoseEveryMutationResponse: true,
		RecoveryFailureSignals:    []execution.ProviderFailureSignal{execution.ProviderFailurePolicy},
	})
	requestFailedPrimaryRecovery(t, &fixture, 2)
	ctx := context.Background()
	for frontier := 0; frontier < 120; frontier++ {
		run, err := fixture.store.Run(ctx, fixture.run.ID)
		if err != nil {
			t.Fatal(err)
		}
		fixture.run = run
		if run.Execution.PrimaryRecovery.Phase == execution.PrimaryRecoveryComplete {
			break
		}
		if run.Execution.PrimaryRecovery.Phase == execution.PrimaryRecoveryNeedsYou {
			t.Fatalf("reopen recovery parked: %#v", run.Execution.PrimaryRecovery)
		}
		replacement := run.Execution.PrimaryRecovery.ReplacementAgent
		if replacement.Observation != nil && replacement.Observation.Status == execution.ObservationOwnedPresent {
			now := testNowMillis()
			event, eventErr := fixture.environment.TerminalEvent(execution.CompletionEventFinished, replacement.ID, now)
			if eventErr != nil {
				t.Fatal(eventErr)
			}
			controller := executionapp.NewController(fixture.store, fixture.environment, fixture.environment, fixture.environment)
			if eventErr := controller.RecordCompletionEvent(ctx, run.ID, event, now); eventErr != nil {
				t.Fatal(eventErr)
			}
		}
		fixture.environment.RefreshObservationsAt(testNowMillis())
		controller := executionapp.NewController(fixture.store, fixture.environment.Restart(), fixture.environment.Restart(), fixture.environment.Restart())
		_, stepErr := controller.ReconcilePrimaryRecovery(ctx, run.ID, testNowMillis())
		if !acceptableRecoveryRaceError(stepErr) {
			t.Fatalf("frontier %d: %v", frontier, stepErr)
		}
		if err := fixture.store.Close(); err != nil {
			t.Fatal(err)
		}
		fixture.store = openVerticalStore(t, fixture.dolt)
	}
	result, err := fixture.store.Run(ctx, fixture.run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if result.Execution.PrimaryRecovery.Phase != execution.PrimaryRecoveryComplete ||
		fixture.environment.MutationCount(execution.EffectAgentCreate) != 2 ||
		fixture.environment.MutationCount(execution.EffectAgentArchive) != 1 {
		t.Fatalf("reopened recovery = %#v", result.Execution.PrimaryRecovery)
	}
}

func TestPrimaryRecoveryRebindsOnlyAfterObservedStaleLeaseTakeover(t *testing.T) {
	fixture := startPrimaryRecoveryFixture(t, "takeover", fake.Options{})
	ctx := context.Background()
	project, err := fixture.store.Project(ctx, fixture.project.ID)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UnixMilli()
	renew := domain.ProjectLeaseMutation{
		Kind: domain.ProjectLeaseRenew, ProjectID: project.ID, ExpectedLeaseEpoch: project.Lease.Epoch,
		HolderInstance: project.Lease.HolderInstance, HolderProcessIdentity: project.Lease.HolderProcessIdentity,
		DurationMillis: 1,
	}
	result, err := fixture.store.ApplyProjectLease(ctx, domain.CommandRequest{
		IdempotencyKey: "recovery-lease-renew", Type: "project.lease.renew", AggregateID: project.ID,
		ExpectedVersion: project.Version, Payload: eventPayloadForTest(renew),
	}, renew)
	if err != nil || result.Outcome != domain.CommandApplied {
		t.Fatalf("renew lease = %#v, %v", result, err)
	}
	project, err = fixture.store.Project(ctx, project.ID)
	if err != nil {
		t.Fatal(err)
	}
	for time.Now().UnixMilli() < project.Lease.ExpiresAtMillis {
		time.Sleep(time.Millisecond)
	}
	now = time.Now().UnixMilli()
	takeover := domain.ProjectLeaseMutation{
		Kind: domain.ProjectLeaseTakeover, ProjectID: project.ID, ExpectedLeaseEpoch: project.Lease.Epoch,
		HolderInstance: "engine-recovery", HolderProcessIdentity: "pid-200:recovery",
		DurationMillis: domain.MaximumProjectLeaseDurationMillis,
	}
	result, err = fixture.store.ApplyProjectLease(ctx, domain.CommandRequest{
		IdempotencyKey: "recovery-lease-takeover", Type: "project.lease.takeover", AggregateID: project.ID,
		ExpectedVersion: project.Version, Payload: eventPayloadForTest(takeover),
	}, takeover)
	if err != nil || result.Outcome != domain.CommandApplied {
		t.Fatalf("takeover lease = %#v, %v", result, err)
	}
	project, err = fixture.store.Project(ctx, project.ID)
	if err != nil {
		t.Fatal(err)
	}
	input := domain.ProjectLeaseObservationInput{
		AdapterKind: "linux-process-supervisor", AdapterVersion: "v1",
		FactHash: strings.Repeat("e", 64),
	}
	payload := struct {
		ProjectID string                              `json:"projectId"`
		Input     domain.ProjectLeaseObservationInput `json:"input"`
	}{project.ID, input}
	result, err = fixture.store.RecordProjectLeaseObservation(ctx, domain.CommandRequest{
		IdempotencyKey: "recovery-lease-observe", Type: "project.lease.observe_takeover",
		AggregateID: project.ID, ExpectedVersion: project.Version, Payload: eventPayloadForTest(payload),
	}, project.ID, input)
	if err != nil || result.Outcome != domain.CommandApplied {
		t.Fatalf("observe takeover = %#v, %v", result, err)
	}
	project, err = fixture.store.Project(ctx, project.ID)
	if err != nil {
		t.Fatal(err)
	}
	enablePayload := struct {
		ProjectID     string `json:"projectId"`
		ObservationID string `json:"observationId"`
	}{project.ID, project.LeaseObservation.ID}
	result, err = fixture.store.EnableProjectLeaseDispatch(ctx, domain.CommandRequest{
		IdempotencyKey: "recovery-lease-enable", Type: "project.lease.enable_dispatch",
		AggregateID: project.ID, ExpectedVersion: project.Version, Payload: eventPayloadForTest(enablePayload),
	}, project.ID, enablePayload.ObservationID)
	if err != nil || result.Outcome != domain.CommandApplied {
		t.Fatalf("enable takeover = %#v, %v", result, err)
	}
	project, err = fixture.store.Project(ctx, project.ID)
	if err != nil {
		t.Fatal(err)
	}
	now = project.Lease.AcquiredAtMillis + 1
	startupController := executionapp.NewController(
		fixture.store, fixture.environment.Restart(), fixture.environment.Restart(), fixture.environment.Restart(),
	)
	startup, err := startupController.ReconcileStartup(ctx, executionapp.StartupCommand{
		SchemaVersion: executionapp.StartupCommandSchemaVersion,
		RequestID:     "startup-primary-recovery-takeover", NowMillis: now,
	})
	if err != nil || len(startup.Runs) != 1 {
		t.Fatalf("startup takeover recovery = %#v, %v", startup, err)
	}
	run, err := fixture.store.Run(ctx, fixture.run.ID)
	if err != nil || run.Execution.PrimaryRecovery.Trigger != execution.RecoveryLeaseTakeover {
		t.Fatalf("startup recovery intent = %#v, %v", run.Execution.PrimaryRecovery, err)
	}
	fixture.run = run
	for step := 0; step < 30; step++ {
		fixture.environment.RefreshObservationsAt(now + int64(step) + 1)
		controller := executionapp.NewController(fixture.store, fixture.environment.Restart(), fixture.environment.Restart(), fixture.environment.Restart())
		_, stepErr := controller.ReconcilePrimaryRecovery(ctx, run.ID, now+int64(step)+1)
		if stepErr != nil && !acceptableRecoveryRaceError(stepErr) {
			t.Fatal(stepErr)
		}
		fixture.run, err = fixture.store.Run(ctx, run.ID)
		if err != nil {
			t.Fatal(err)
		}
		if fixture.run.Execution.PrimaryRecovery.Phase == execution.PrimaryRecoveryComplete {
			break
		}
	}
	project, err = fixture.store.Project(ctx, project.ID)
	if err != nil {
		t.Fatal(err)
	}
	if fixture.run.Execution.PrimaryRecovery.Phase != execution.PrimaryRecoveryComplete ||
		fixture.run.Execution.LeaseBinding.Epoch != project.Lease.Epoch ||
		!fixture.run.Execution.PrimaryRecovery.PoisonedSession && fixture.run.Execution.PrimaryRecovery.Authority != nil {
		t.Fatalf("takeover recovery = %#v lease=%#v", fixture.run.Execution.PrimaryRecovery, fixture.run.Execution.LeaseBinding)
	}
	for _, reservation := range fixture.run.Execution.Budget.Reservations {
		if !reservation.Released && reservation.LeaseEpoch != project.Lease.Epoch {
			t.Fatalf("outstanding reservation retained stale epoch: %#v", reservation)
		}
	}
}

func TestPrimaryRecoveryMissingEnforcementTelemetryRoutesNeedsYou(t *testing.T) {
	fixture := startPrimaryRecoveryFixture(t, "missing-enforcement", fake.Options{
		RecoveryFailureSignals: []execution.ProviderFailureSignal{execution.ProviderFailurePolicy},
	})
	requestFailedPrimaryRecovery(t, &fixture, 2)
	runtime := runtimeWithoutRecovery{inner: fixture.environment}
	controller := executionapp.NewController(fixture.store, runtime, fixture.environment, fixture.environment)
	for step := 0; step < 6; step++ {
		fixture.environment.RefreshObservationsAt(testNowMillis())
		_, err := controller.ReconcilePrimaryRecovery(context.Background(), fixture.run.ID, testNowMillis())
		if err != nil && !acceptableRecoveryRaceError(err) {
			t.Fatal(err)
		}
		fixture.run, _ = fixture.store.Run(context.Background(), fixture.run.ID)
		if fixture.run.Execution.PrimaryRecovery.Phase == execution.PrimaryRecoveryNeedsYou {
			break
		}
	}
	if fixture.run.Execution.NeedsYou == nil || fixture.run.Execution.NeedsYou.Code != execution.NeedRecoveryEnforcementUnavailable ||
		fixture.environment.MutationCount(execution.EffectAgentArchive) != 0 {
		t.Fatalf("missing enforcement result = %#v", fixture.run.Execution.PrimaryRecovery)
	}
}
