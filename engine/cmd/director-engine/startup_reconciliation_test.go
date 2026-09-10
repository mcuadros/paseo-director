// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/mcuadros/director-engine/adapters/fake"
	executionapp "github.com/mcuadros/director-engine/application/execution"
	"github.com/mcuadros/director-engine/domain"
	"github.com/mcuadros/director-engine/domain/execution"
	"github.com/mcuadros/director-engine/ports/host"
	runtimeport "github.com/mcuadros/director-engine/ports/runtime"
	storeport "github.com/mcuadros/director-engine/ports/taskstore"
)

func dispatchingEffect(run domain.Run) execution.EffectKind {
	for _, effect := range []execution.Effect{
		run.Execution.Worktree, run.Execution.HostView, run.Execution.Boundary,
		run.Execution.Setup, run.Execution.Agent, run.Execution.AgentPrompt, run.Execution.AgentArchive,
		run.Execution.HostViewArchive, run.Execution.WorktreeRemove,
	} {
		if effect.Phase == execution.EffectDispatching {
			return effect.Kind
		}
	}
	return ""
}

func TestStartupReconciliationRestartsBeforeAndAfterEveryWalkingSkeletonEffect(t *testing.T) {
	fixture := startVerticalDolt(t)
	store := openVerticalStore(t, fixture)
	source, base := initializeRepository(t, "startup-boundaries")
	project, task := createVerticalRecords(t, store, "startup-boundaries", source)
	worktree := filepath.Join(filepath.Dir(source), "startup-boundaries-worktree")
	scope := execution.Scope{
		ProjectID: project.ID, WorkspaceID: task.WorkspaceIDs[0],
		TaskID: task.ID, RunID: "run-startup-boundaries",
	}
	surfaces := execution.LifecycleSurfaces{Setup: []string{"fixture setup"}}
	facts := eligibilityFacts(scope, surfaces)
	facts.LifecycleApproval = &execution.LifecycleApproval{
		ActorKind: "human", Source: "authenticated_engine_command", ActorID: "human-startup",
		Scope: scope, Digest: execution.LifecycleDigest(surfaces),
	}
	environment := fake.NewEnvironment(fake.Options{
		SourcePath: source, WorktreePath: worktree, Branch: "task/" + task.ID,
		BaseSHA: base, Operational: operationalObservation("startup-periodic"),
		LoseEveryMutationResponse: true,
	})
	t.Cleanup(environment.RemoveFixture)
	controller := executionapp.NewController(store, environment, environment, environment)
	if _, err := controller.Start(context.Background(), startCommand(t, task, scope, source, worktree, base, facts)); err != nil {
		t.Fatal(err)
	}

	unknownHandoffs := make(map[execution.EffectKind]bool)
	maximumCleanupIntents := 0
	candidateRecovered := false
	claimRecorded := false
	var pendingClaim *execution.CompletedClaim
	for restart := 1; restart <= 200; restart++ {
		run, err := store.Run(context.Background(), scope.RunID)
		if err != nil {
			t.Fatal(err)
		}
		if run.Execution.Terminal {
			break
		}
		if run.Execution.AgentPrompt.Phase == execution.EffectComplete && pendingClaim != nil && run.Execution.Claim == nil {
			restarted := environment.Restart()
			if err := executionapp.NewController(store, restarted, restarted, restarted).RecordCompletedClaim(context.Background(), run.ID, *pendingClaim); err != nil {
				t.Fatal(err)
			}
			claimRecorded = true
			continue
		}
		if run.Execution.AgentPrompt.Observation != nil &&
			run.Execution.AgentPrompt.Observation.Status == execution.ObservationOwnedPresent &&
			run.Execution.Claim == nil {
			claim, err := environment.ProduceCandidate(context.Background(), fake.CandidateRequest{
				ClaimID: "claim-startup-boundaries", AgentID: run.Execution.Agent.ExternalID,
				BaseSHA: base, CriteriaResults: map[string]string{"criterion-1": "claimed_satisfied"},
			})
			if err != nil {
				t.Fatal(err)
			}
			restarted := environment.Restart()
			restartedController := executionapp.NewController(store, restarted, restarted, restarted)
			nowMillis := testNowMillis()
			completion, err := environment.TerminalEvent(execution.CompletionEventFinished, run.Execution.AgentPrompt.ID, nowMillis)
			if err != nil {
				t.Fatal(err)
			}
			if err := restartedController.RecordCompletionEvent(context.Background(), run.ID, completion, nowMillis); err != nil {
				t.Fatal(err)
			}
			pending := claim
			pendingClaim = &pending
			continue
		}
		if run.Execution.Agent.Observation != nil &&
			run.Execution.Agent.Observation.Status == execution.ObservationOwnedPresent {
			nowMillis := testNowMillis()
			completion, err := environment.TerminalEvent(execution.CompletionEventFinished, run.Execution.Agent.ID, nowMillis)
			if err != nil {
				t.Fatal(err)
			}
			restarted := environment.Restart()
			if err := executionapp.NewController(store, restarted, restarted, restarted).RecordCompletionEvent(context.Background(), run.ID, completion, nowMillis); err != nil {
				t.Fatal(err)
			}
			continue
		}

		restarted := environment.Restart()
		result, err := executionapp.NewController(store, restarted, restarted, restarted).ReconcileStartup(
			context.Background(), executionapp.StartupCommand{
				SchemaVersion: executionapp.StartupCommandSchemaVersion,
				RequestID:     fmt.Sprintf("startup-boundary-%03d", restart), NowMillis: testNowMillis(),
			},
		)
		if err != nil {
			t.Fatalf("restart %d: %v", restart, err)
		}
		if result.Projects != 1 || result.Tasks != 1 || len(result.Runs) != 1 {
			t.Fatalf("restart inventory %d = %#v", restart, result)
		}
		entry := result.Runs[0]
		if entry.ReconciliationID == "" || entry.RecoveredCommands == 0 {
			t.Fatalf("restart fact recovery %d = %#v", restart, entry)
		}
		if entry.CleanupIntents > maximumCleanupIntents {
			maximumCleanupIntents = entry.CleanupIntents
		}
		candidateRecovered = candidateRecovered || (entry.CandidateID != "" && entry.CandidateSHA != "")
		if entry.HandoffUnknown {
			reloaded, err := store.Run(context.Background(), scope.RunID)
			if err != nil {
				t.Fatal(err)
			}
			kind := dispatchingEffect(reloaded)
			if kind == "" {
				t.Fatalf("restart %d reported an unknown handoff without durable dispatch", restart)
			}
			unknownHandoffs[kind] = true
		}
	}

	run, err := store.Run(context.Background(), scope.RunID)
	if err != nil {
		t.Fatal(err)
	}
	if !claimRecorded || !run.Execution.Terminal || run.Execution.NeedsYou != nil {
		t.Fatalf("startup-recovered Run = %#v", run)
	}
	if !candidateRecovered || maximumCleanupIntents != 3 {
		t.Fatalf("recovered Candidate=%v maximum cleanup intents=%d", candidateRecovered, maximumCleanupIntents)
	}
	for _, kind := range []execution.EffectKind{
		execution.EffectWorktreeCreate, execution.EffectHostViewCreate,
		execution.EffectBoundaryMaterialize, execution.EffectSetupRun,
		execution.EffectAgentCreate, execution.EffectAgentPrompt, execution.EffectAgentArchive,
		execution.EffectHostViewArchive, execution.EffectWorktreeRemove,
	} {
		if !unknownHandoffs[kind] {
			t.Errorf("%s did not exercise restart after possible handoff", kind)
		}
		if count := environment.MutationCount(kind); count != 1 {
			t.Errorf("%s mutation count = %d, want 1", kind, count)
		}
	}
	if environment.WorktreePresent() {
		t.Fatal("startup recovery left the Director-owned worktree present")
	}
	events, err := store.Events(context.Background(), domain.EventQuery{RunID: run.ID, Limit: 1000})
	if err != nil {
		t.Fatal(err)
	}
	startupEvents := 0
	var immutableOperational, immutableEffect, immutableCandidate bool
	for _, event := range events {
		if event.Type == "run.startup_reconciled" {
			startupEvents++
			payload := string(event.Payload)
			immutableOperational = immutableOperational || strings.Contains(payload, `"operationalObservation"`)
			immutableEffect = immutableEffect || strings.Contains(payload, `"effectObservations"`)
			immutableCandidate = immutableCandidate || strings.Contains(payload, `"candidateObservation"`)
		}
	}
	if startupEvents < len(unknownHandoffs)*2 {
		t.Fatalf("startup reconciliation events = %d, want at least %d", startupEvents, len(unknownHandoffs)*2)
	}
	if !immutableOperational || !immutableEffect || !immutableCandidate {
		t.Fatalf("startup event evidence missing: operational=%v effect=%v Candidate=%v", immutableOperational, immutableEffect, immutableCandidate)
	}
}

type commandFaultStore struct {
	storeport.TaskStore
	key string
}

type staleCursorHost struct {
	host.Port
	cursor uint64
}

type mismatchedWorktreeRuntime struct {
	runtimeport.Port
}

func (adapter mismatchedWorktreeRuntime) ObserveEffect(ctx context.Context, request runtimeport.Request) (execution.EffectObservation, error) {
	observation, err := adapter.Port.ObserveEffect(ctx, request)
	if err == nil && request.Effect.Kind == execution.EffectWorktreeCreate &&
		request.Effect.Phase == execution.EffectComplete {
		observation.ExternalID = "worktree-foreign"
		observation.FactHash = execution.EffectObservationHash(observation)
	}
	return observation, err
}

func (adapter staleCursorHost) Invoke(ctx context.Context, command host.Command) (host.Observation, error) {
	observation, err := adapter.Port.Invoke(ctx, command)
	if err == nil && observation.Cursor > 0 {
		observation.Cursor = adapter.cursor
	}
	return observation, err
}

func (store commandFaultStore) Command(ctx context.Context, key string) (domain.Command, error) {
	command, err := store.TaskStore.Command(ctx, key)
	if err == nil && key == store.key {
		command.Type = "run.command.tampered"
	}
	return command, err
}

func TestStartupReconciliationRejectsCommandConflictBeforeExternalMutation(t *testing.T) {
	fixture := startVerticalDolt(t)
	store := openVerticalStore(t, fixture)
	source, base := initializeRepository(t, "startup-command-conflict")
	project, task := createVerticalRecords(t, store, "startup-command-conflict", source)
	worktree := filepath.Join(filepath.Dir(source), "startup-command-conflict-worktree")
	scope := execution.Scope{
		ProjectID: project.ID, WorkspaceID: task.WorkspaceIDs[0],
		TaskID: task.ID, RunID: "run-startup-command-conflict",
	}
	environment := fake.NewEnvironment(fake.Options{
		SourcePath: source, WorktreePath: worktree, Branch: "task/" + task.ID,
		BaseSHA: base, Operational: operationalObservation("startup-command-conflict"),
	})
	t.Cleanup(environment.RemoveFixture)
	controller := executionapp.NewController(store, environment, environment, environment)
	if _, err := controller.Start(context.Background(), startCommand(t, task, scope, source, worktree, base, eligibilityFacts(scope, execution.LifecycleSurfaces{}))); err != nil {
		t.Fatal(err)
	}
	runBefore, err := store.Run(context.Background(), scope.RunID)
	if err != nil {
		t.Fatal(err)
	}
	faulted := commandFaultStore{TaskStore: store, key: runBefore.Execution.StartCommandID}
	restarted := environment.Restart()
	_, err = executionapp.NewController(faulted, restarted, restarted, restarted).ReconcileStartup(
		context.Background(), executionapp.StartupCommand{SchemaVersion: executionapp.StartupCommandSchemaVersion, RequestID: "startup-command-conflict", NowMillis: testNowMillis()},
	)
	if err == nil || !strings.Contains(err.Error(), "command outcome") {
		t.Fatalf("command conflict = %v", err)
	}
	runAfter, err := store.Run(context.Background(), scope.RunID)
	if err != nil {
		t.Fatal(err)
	}
	if runAfter.Version != runBefore.Version || environment.TotalMutationCount() != 0 || environment.HostCallCount() != 0 {
		t.Fatalf("command conflict mutated state: before=%d after=%d mutations=%d host=%d", runBefore.Version, runAfter.Version, environment.TotalMutationCount(), environment.HostCallCount())
	}
}

func TestStartupReconciliationScansEveryRunBeforeAnyEffect(t *testing.T) {
	fixture := startVerticalDolt(t)
	store := openVerticalStore(t, fixture)
	sourceOne, baseOne := initializeRepository(t, "global-first")
	projectOne, taskOne := createVerticalRecords(t, store, "global-first", sourceOne)
	worktreeOne := filepath.Join(filepath.Dir(sourceOne), "global-first-worktree")
	scopeOne := execution.Scope{
		ProjectID: projectOne.ID, WorkspaceID: taskOne.WorkspaceIDs[0],
		TaskID: taskOne.ID, RunID: "run-global-first",
	}
	environmentOne := fake.NewEnvironment(fake.Options{
		SourcePath: sourceOne, WorktreePath: worktreeOne, Branch: "task/" + taskOne.ID,
		BaseSHA: baseOne, Operational: operationalObservation("global-first"),
	})
	t.Cleanup(environmentOne.RemoveFixture)
	controllerOne := executionapp.NewController(store, environmentOne, environmentOne, environmentOne)
	if _, err := controllerOne.Start(context.Background(), startCommand(t, taskOne, scopeOne, sourceOne, worktreeOne, baseOne, eligibilityFacts(scopeOne, execution.LifecycleSurfaces{}))); err != nil {
		t.Fatal(err)
	}

	sourceTwo, baseTwo := initializeRepository(t, "global-second")
	projectTwo, taskTwo := createVerticalRecords(t, store, "global-second", sourceTwo)
	worktreeTwo := filepath.Join(filepath.Dir(sourceTwo), "global-second-worktree")
	scopeTwo := execution.Scope{
		ProjectID: projectTwo.ID, WorkspaceID: taskTwo.WorkspaceIDs[0],
		TaskID: taskTwo.ID, RunID: "run-global-second",
	}
	environmentTwo := fake.NewEnvironment(fake.Options{
		SourcePath: sourceTwo, WorktreePath: worktreeTwo, Branch: "task/" + taskTwo.ID,
		BaseSHA: baseTwo, Operational: operationalObservation("global-second"),
	})
	t.Cleanup(environmentTwo.RemoveFixture)
	controllerTwo := executionapp.NewController(store, environmentTwo, environmentTwo, environmentTwo)
	if _, err := controllerTwo.Start(context.Background(), startCommand(t, taskTwo, scopeTwo, sourceTwo, worktreeTwo, baseTwo, eligibilityFacts(scopeTwo, execution.LifecycleSurfaces{}))); err != nil {
		t.Fatal(err)
	}
	runOne, err := store.Run(context.Background(), scopeOne.RunID)
	if err != nil {
		t.Fatal(err)
	}
	runTwo, err := store.Run(context.Background(), scopeTwo.RunID)
	if err != nil {
		t.Fatal(err)
	}
	faulted := commandFaultStore{TaskStore: store, key: runTwo.Execution.StartCommandID}
	restarted := environmentOne.Restart()
	_, err = executionapp.NewController(faulted, restarted, restarted, restarted).ReconcileStartup(
		context.Background(), executionapp.StartupCommand{
			SchemaVersion: executionapp.StartupCommandSchemaVersion,
			RequestID:     "startup-global-scan", NowMillis: testNowMillis(),
		},
	)
	if err == nil || !strings.Contains(err.Error(), "command outcome") {
		t.Fatalf("global scan conflict = %v", err)
	}
	currentOne, err := store.Run(context.Background(), scopeOne.RunID)
	if err != nil {
		t.Fatal(err)
	}
	currentTwo, err := store.Run(context.Background(), scopeTwo.RunID)
	if err != nil {
		t.Fatal(err)
	}
	if currentOne.Version != runOne.Version || currentTwo.Version != runTwo.Version ||
		environmentOne.TotalMutationCount() != 0 || environmentOne.HostCallCount() != 0 ||
		environmentTwo.TotalMutationCount() != 0 || environmentTwo.HostCallCount() != 0 {
		t.Fatalf("global scan crossed the read-only barrier: versions=%d/%d mutations=%d/%d host=%d/%d",
			currentOne.Version, currentTwo.Version, environmentOne.TotalMutationCount(), environmentTwo.TotalMutationCount(),
			environmentOne.HostCallCount(), environmentTwo.HostCallCount())
	}
}

func TestStartupReconciliationRequestReplayIsIdempotent(t *testing.T) {
	fixture := startVerticalDolt(t)
	store := openVerticalStore(t, fixture)
	source, base := initializeRepository(t, "startup-replay")
	project, task := createVerticalRecords(t, store, "startup-replay", source)
	worktree := filepath.Join(filepath.Dir(source), "startup-replay-worktree")
	scope := execution.Scope{
		ProjectID: project.ID, WorkspaceID: task.WorkspaceIDs[0],
		TaskID: task.ID, RunID: "run-startup-replay",
	}
	environment := fake.NewEnvironment(fake.Options{
		SourcePath: source, WorktreePath: worktree, Branch: "task/" + task.ID,
		BaseSHA: base, Operational: operationalObservation("startup-replay"),
		LoseEveryMutationResponse: true,
	})
	t.Cleanup(environment.RemoveFixture)
	controller := executionapp.NewController(store, environment, environment, environment)
	if _, err := controller.Start(context.Background(), startCommand(t, task, scope, source, worktree, base, eligibilityFacts(scope, execution.LifecycleSurfaces{}))); err != nil {
		t.Fatal(err)
	}
	command := executionapp.StartupCommand{SchemaVersion: executionapp.StartupCommandSchemaVersion, RequestID: "startup-replay-same", NowMillis: testNowMillis()}
	for attempt := 0; attempt < 2; attempt++ {
		restarted := environment.Restart()
		if _, err := executionapp.NewController(store, restarted, restarted, restarted).ReconcileStartup(context.Background(), command); err != nil {
			t.Fatal(err)
		}
	}
	if count := environment.MutationCount(execution.EffectWorktreeCreate); count != 1 {
		t.Fatalf("startup request replay created %d worktrees", count)
	}
	events, err := store.Events(context.Background(), domain.EventQuery{RunID: scope.RunID, Limit: 1000})
	if err != nil {
		t.Fatal(err)
	}
	startupEvents := 0
	for _, event := range events {
		if event.Type == "run.startup_reconciled" {
			startupEvents++
		}
	}
	if startupEvents != 1 {
		t.Fatalf("same startup request persisted %d reconciliation events", startupEvents)
	}
}

func TestStartupReconciliationParksAdmittedCandidateDriftWithoutCleanup(t *testing.T) {
	fixture := startVerticalDolt(t)
	store := openVerticalStore(t, fixture)
	source, base := initializeRepository(t, "startup-candidate-drift")
	project, task := createVerticalRecords(t, store, "startup-candidate-drift", source)
	worktree := filepath.Join(filepath.Dir(source), "startup-candidate-drift-worktree")
	scope := execution.Scope{
		ProjectID: project.ID, WorkspaceID: task.WorkspaceIDs[0],
		TaskID: task.ID, RunID: "run-startup-candidate-drift",
	}
	environment := fake.NewEnvironment(fake.Options{
		SourcePath: source, WorktreePath: worktree, Branch: "task/" + task.ID,
		BaseSHA: base, Operational: operationalObservation("startup-candidate-drift"),
	})
	t.Cleanup(environment.RemoveFixture)
	controller := executionapp.NewController(store, environment, environment, environment)
	if _, err := controller.Start(context.Background(), startCommand(t, task, scope, source, worktree, base, eligibilityFacts(scope, execution.LifecycleSurfaces{}))); err != nil {
		t.Fatal(err)
	}
	run := runSteps(t, store, environment, scope.RunID, func(run domain.Run) bool {
		return run.Execution.AgentPrompt.Observation != nil &&
			run.Execution.AgentPrompt.Observation.Status == execution.ObservationOwnedPresent
	})
	claim, err := environment.ProduceCandidate(context.Background(), fake.CandidateRequest{
		ClaimID: "claim-startup-candidate-drift", AgentID: run.Execution.Agent.ExternalID,
		BaseSHA: base, CriteriaResults: map[string]string{"criterion-1": "claimed_satisfied"},
	})
	if err != nil {
		t.Fatal(err)
	}
	nowMillis := testNowMillis()
	completion, err := environment.TerminalEvent(execution.CompletionEventFinished, run.Execution.AgentPrompt.ID, nowMillis)
	if err != nil {
		t.Fatal(err)
	}
	if err := controller.RecordCompletionEvent(context.Background(), run.ID, completion, nowMillis); err != nil {
		t.Fatal(err)
	}
	run = runSteps(t, store, environment, scope.RunID, func(run domain.Run) bool {
		return run.Execution.AgentPrompt.Phase == execution.EffectComplete
	})
	if err := controller.RecordCompletedClaim(context.Background(), run.ID, claim); err != nil {
		t.Fatal(err)
	}
	run = runSteps(t, store, environment, scope.RunID, func(run domain.Run) bool {
		return run.CurrentCandidateID != ""
	})
	if err := os.WriteFile(filepath.Join(worktree, "UNCOMMITTED"), []byte("drift\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	restarted := environment.Restart()
	result, err := executionapp.NewController(store, restarted, restarted, restarted).ReconcileStartup(
		context.Background(), executionapp.StartupCommand{SchemaVersion: executionapp.StartupCommandSchemaVersion, RequestID: "startup-candidate-drift", NowMillis: testNowMillis()},
	)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Runs) != 1 || !result.Runs[0].NeedsYou || result.Runs[0].CandidateID != run.CurrentCandidateID {
		t.Fatalf("candidate drift startup result = %#v", result)
	}
	parked, err := store.Run(context.Background(), run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if parked.Execution.NeedsYou == nil || parked.Execution.NeedsYou.Code != execution.NeedCode("startup_candidate_facts_not_admitted") || parked.Execution.NeedsYou.CleanupAuthorized {
		t.Fatalf("candidate drift park = %#v", parked.Execution.NeedsYou)
	}
	if environment.MutationCount(execution.EffectAgentArchive) != 0 ||
		environment.MutationCount(execution.EffectHostViewArchive) != 0 ||
		environment.MutationCount(execution.EffectWorktreeRemove) != 0 || !environment.WorktreePresent() {
		t.Fatal("Candidate drift obtained cleanup authority")
	}
}

func TestStartupReconciliationRejectsAReplacementConnectorStaleCursor(t *testing.T) {
	fixture := startVerticalDolt(t)
	store := openVerticalStore(t, fixture)
	source, base := initializeRepository(t, "startup-stale-cursor")
	project, task := createVerticalRecords(t, store, "startup-stale-cursor", source)
	worktree := filepath.Join(filepath.Dir(source), "startup-stale-cursor-worktree")
	scope := execution.Scope{
		ProjectID: project.ID, WorkspaceID: task.WorkspaceIDs[0],
		TaskID: task.ID, RunID: "run-startup-stale-cursor",
	}
	environment := fake.NewEnvironment(fake.Options{
		SourcePath: source, WorktreePath: worktree, Branch: "task/" + task.ID,
		BaseSHA: base, Operational: operationalObservation("startup-stale-cursor"),
		LoseEveryMutationResponse: true,
	})
	t.Cleanup(environment.RemoveFixture)
	controller := executionapp.NewController(store, environment, environment, environment)
	if _, err := controller.Start(context.Background(), startCommand(t, task, scope, source, worktree, base, eligibilityFacts(scope, execution.LifecycleSurfaces{}))); err != nil {
		t.Fatal(err)
	}
	runSteps(t, store, environment, scope.RunID, func(run domain.Run) bool {
		return run.Execution.HostView.Phase == execution.EffectDispatching
	})
	restarted := environment.Restart()
	if _, err := executionapp.NewController(store, restarted, restarted, restarted).ReconcileStartup(
		context.Background(), executionapp.StartupCommand{SchemaVersion: executionapp.StartupCommandSchemaVersion, RequestID: "startup-cursor-baseline", NowMillis: testNowMillis()},
	); err != nil {
		t.Fatal(err)
	}
	run := runSteps(t, store, environment, scope.RunID, func(run domain.Run) bool {
		return run.Execution.Agent.Phase == execution.EffectDispatching
	})
	if run.Execution.LastStartupReconciliation == nil || run.Execution.LastStartupReconciliation.HostCursor == 0 {
		t.Fatalf("baseline startup cursor = %#v", run.Execution.LastStartupReconciliation)
	}
	priorCursor := run.Execution.LastStartupReconciliation.HostCursor
	restarted = environment.Restart()
	result, err := executionapp.NewController(
		store, restarted, staleCursorHost{Port: restarted, cursor: priorCursor}, restarted,
	).ReconcileStartup(
		context.Background(), executionapp.StartupCommand{SchemaVersion: executionapp.StartupCommandSchemaVersion, RequestID: "startup-cursor-stale", NowMillis: testNowMillis()},
	)
	if err == nil || !strings.Contains(err.Error(), "observation envelope is invalid") {
		t.Fatalf("stale cursor startup result = %#v, %v", result, err)
	}
	parked, err := store.Run(context.Background(), run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if parked.Version != run.Version || parked.Execution.NeedsYou != nil {
		t.Fatalf("stale cursor changed durable Run = %#v", parked)
	}
	if environment.MutationCount(execution.EffectAgentCreate) != 1 ||
		environment.MutationCount(execution.EffectAgentArchive) != 0 || !environment.WorktreePresent() {
		t.Fatal("stale connector cursor duplicated execution or authorized cleanup")
	}
}

func TestStartupReconciliationRejectsPriorLiveResourceIdentityDrift(t *testing.T) {
	fixture := startVerticalDolt(t)
	store := openVerticalStore(t, fixture)
	source, base := initializeRepository(t, "startup-identity-drift")
	project, task := createVerticalRecords(t, store, "startup-identity-drift", source)
	worktree := filepath.Join(filepath.Dir(source), "startup-identity-drift-worktree")
	scope := execution.Scope{
		ProjectID: project.ID, WorkspaceID: task.WorkspaceIDs[0],
		TaskID: task.ID, RunID: "run-startup-identity-drift",
	}
	environment := fake.NewEnvironment(fake.Options{
		SourcePath: source, WorktreePath: worktree, Branch: "task/" + task.ID,
		BaseSHA: base, Operational: operationalObservation("startup-identity-drift"),
	})
	t.Cleanup(environment.RemoveFixture)
	controller := executionapp.NewController(store, environment, environment, environment)
	if _, err := controller.Start(context.Background(), startCommand(t, task, scope, source, worktree, base, eligibilityFacts(scope, execution.LifecycleSurfaces{}))); err != nil {
		t.Fatal(err)
	}
	runSteps(t, store, environment, scope.RunID, func(run domain.Run) bool {
		return run.Execution.Worktree.Phase == execution.EffectComplete
	})
	restarted := environment.Restart()
	result, err := executionapp.NewController(
		store, mismatchedWorktreeRuntime{Port: restarted}, restarted, restarted,
	).ReconcileStartup(
		context.Background(), executionapp.StartupCommand{
			SchemaVersion: executionapp.StartupCommandSchemaVersion,
			RequestID:     "startup-identity-drift", NowMillis: testNowMillis(),
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Runs) != 1 || !result.Runs[0].NeedsYou {
		t.Fatalf("identity drift startup result = %#v", result)
	}
	parked, err := store.Run(context.Background(), scope.RunID)
	if err != nil {
		t.Fatal(err)
	}
	if parked.Execution.NeedsYou == nil ||
		parked.Execution.NeedsYou.Code != execution.NeedCode("startup_execution_identity_mismatch") ||
		parked.Execution.NeedsYou.CleanupAuthorized {
		t.Fatalf("identity drift park = %#v", parked.Execution.NeedsYou)
	}
	if environment.MutationCount(execution.EffectWorktreeCreate) != 1 ||
		environment.MutationCount(execution.EffectHostViewCreate) != 0 ||
		!environment.WorktreePresent() {
		t.Fatal("prior live-resource drift duplicated or cleaned an effect")
	}
}
