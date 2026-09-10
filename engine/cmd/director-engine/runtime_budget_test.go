// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"strings"
	"sync"
	"testing"

	"github.com/mcuadros/director-engine/adapters/dolt"
	"github.com/mcuadros/director-engine/adapters/fake"
	executionapp "github.com/mcuadros/director-engine/application/execution"
	"github.com/mcuadros/director-engine/domain"
	"github.com/mcuadros/director-engine/domain/execution"
	"github.com/mcuadros/director-engine/domain/runtimebudget"
	"github.com/mcuadros/director-engine/reducer/eligibility"
)

func newBudgetFixture(t *testing.T, suffix string) (*verticalDolt, *dolt.DoltTaskStore, *fake.Environment, domain.Project, domain.Task, execution.Scope, string, string, string) {
	t.Helper()
	fixture := startVerticalDolt(t)
	store := openVerticalStore(t, fixture)
	sourcePath, base := initializeRepository(t, suffix)
	project, task := createVerticalRecords(t, store, suffix, sourcePath)
	worktree := sourcePath + "-worktree"
	scope := execution.Scope{
		ProjectID: project.ID, WorkspaceID: task.WorkspaceIDs[0], TaskID: task.ID, RunID: "run-" + suffix,
	}
	environment := fake.NewEnvironment(fake.Options{
		SourcePath: sourcePath, WorktreePath: worktree, Branch: "task/" + task.ID,
		BaseSHA: base, Operational: operationalObservation("budget-" + suffix),
	})
	t.Cleanup(environment.RemoveFixture)
	return fixture, store, environment, project, task, scope, sourcePath, worktree, base
}

func TestRuntimeBudgetSoftPauseAcknowledgementAndHardStopPreserveRun(t *testing.T) {
	_, store, environment, project, task, scope, sourcePath, worktree, base := newBudgetFixture(t, "soft-budget")
	facts := eligibilityFacts(scope, execution.LifecycleSurfaces{})
	command := startCommand(t, task, scope, sourcePath, worktree, base, facts)
	command.BudgetPolicy = runtimebudget.NewPolicy(command.EffectiveProfiles.ConfigurationSHA256(), 100_000, 100, 10, 0, 4)
	command.TurnBudgetDemand = runtimebudget.Demand{WallTimeMilliseconds: 100, Tokens: 85, Turns: 1}
	controller := executionapp.NewController(store, environment, environment, environment)
	started, err := controller.Start(context.Background(), command)
	if err != nil || started.Decision.Kind != eligibility.DecisionEligible {
		t.Fatalf("start = %#v, %v", started, err)
	}
	paused := runSteps(t, store, environment, scope.RunID, func(run domain.Run) bool {
		return run.Execution.NeedsYou != nil
	})
	if paused.Execution.NeedsYou.Code != execution.NeedCode(runtimebudget.ReasonTokensSoft) ||
		paused.Execution.NeedsYou.CleanupAuthorized || len(paused.Execution.Budget.Warnings) != 1 ||
		environment.MutationCount(execution.EffectAgentCreate) != 0 || !environment.WorktreePresent() {
		t.Fatalf("soft pause = %#v; agent mutations=%d worktree=%v", paused.Execution.NeedsYou, environment.MutationCount(execution.EffectAgentCreate), environment.WorktreePresent())
	}
	warning := paused.Execution.Budget.Warnings[0]
	acknowledged, err := controller.AcknowledgeBudgetWarning(context.Background(), executionapp.BudgetAcknowledgementCommand{
		RequestID: "ack-soft-budget", RunID: paused.ID, ExpectedRunVersion: paused.Version,
		LeaseEpoch: project.Lease.Epoch, WarningID: warning.ID, PolicyRevision: warning.PolicyRevision,
		ActorKind: "human", ActorID: "human-1", Source: "authenticated_engine_command", NowMillis: testNowMillis(),
	})
	if err != nil || acknowledged.Run.Execution.NeedsYou != nil {
		t.Fatalf("acknowledgement = %#v, %v", acknowledged, err)
	}
	continued := runSteps(t, store, environment, scope.RunID, func(run domain.Run) bool {
		return run.Execution.Agent.Phase == execution.EffectComplete
	})
	if continued.Execution.Budget.Consumption.Turns != 1 || continued.Execution.Budget.Consumption.Tokens != 12 ||
		environment.MutationCount(execution.EffectAgentCreate) != 1 {
		t.Fatalf("continued budget = %#v; agent mutations=%d", continued.Execution.Budget, environment.MutationCount(execution.EffectAgentCreate))
	}
	events, err := store.Events(context.Background(), domain.EventQuery{RunID: scope.RunID, Limit: 1_000})
	if err != nil {
		t.Fatal(err)
	}
	softEvents := 0
	for _, event := range events {
		if event.Type == "soft_budget_reached" {
			softEvents++
		}
	}
	if softEvents != 1 {
		t.Fatalf("soft_budget_reached events = %d", softEvents)
	}

	_, hardStore, hardEnvironment, _, hardTask, hardScope, hardSource, hardWorktree, hardBase := newBudgetFixture(t, "hard-budget")
	hardFacts := eligibilityFacts(hardScope, execution.LifecycleSurfaces{})
	hardCommand := startCommand(t, hardTask, hardScope, hardSource, hardWorktree, hardBase, hardFacts)
	hardCommand.BudgetPolicy = runtimebudget.NewPolicy(hardCommand.EffectiveProfiles.ConfigurationSHA256(), 100_000, 100, 10, 0, 4)
	hardCommand.TurnBudgetDemand = runtimebudget.Demand{WallTimeMilliseconds: 100, Tokens: 100, Turns: 1}
	if _, err := executionapp.NewController(hardStore, hardEnvironment, hardEnvironment, hardEnvironment).Start(context.Background(), hardCommand); err != nil {
		t.Fatal(err)
	}
	hard := runSteps(t, hardStore, hardEnvironment, hardScope.RunID, func(run domain.Run) bool {
		return run.Execution.NeedsYou != nil
	})
	if hard.Execution.NeedsYou.Code != execution.NeedCode(runtimebudget.ReasonTokensHard) ||
		hardEnvironment.MutationCount(execution.EffectAgentCreate) != 0 || !hardEnvironment.WorktreePresent() {
		t.Fatalf("hard stop = %#v; agent mutations=%d worktree=%v", hard.Execution.NeedsYou, hardEnvironment.MutationCount(execution.EffectAgentCreate), hardEnvironment.WorktreePresent())
	}
	hardEvents, err := hardStore.Events(context.Background(), domain.EventQuery{RunID: hardScope.RunID, Limit: 1_000})
	if err != nil {
		t.Fatal(err)
	}
	hardCount := 0
	for _, event := range hardEvents {
		if event.Type == "hard_budget_exhausted" {
			hardCount++
		}
	}
	if hardCount != 1 {
		t.Fatalf("hard_budget_exhausted events = %d", hardCount)
	}
}

func TestRuntimeBudgetDirectDoltReplayConcurrentWriterFenceAndUnknownUsage(t *testing.T) {
	fixture, store, environment, project, task, scope, sourcePath, worktree, base := newBudgetFixture(t, "budget-ledger")
	command := startCommand(t, task, scope, sourcePath, worktree, base, eligibilityFacts(scope, execution.LifecycleSurfaces{}))
	if _, err := executionapp.NewController(store, environment, environment, environment).Start(context.Background(), command); err != nil {
		t.Fatal(err)
	}
	run, err := store.Run(context.Background(), scope.RunID)
	if err != nil {
		t.Fatal(err)
	}
	reserve := executionapp.BudgetWorkCommand{
		RequestID: "review-budget-request", RunID: run.ID, ExpectedRunVersion: run.Version,
		LeaseEpoch: project.Lease.Epoch, EffectID: "review-effect", Activity: runtimebudget.ActivityReviewerTurn,
		Demand: runtimebudget.Demand{WallTimeMilliseconds: 100, Tokens: 100, Turns: 1}, NowMillis: testNowMillis(),
	}
	controller := executionapp.NewController(store, environment, environment, environment)
	reserved, err := controller.ReserveAutomatedWork(context.Background(), reserve)
	if err != nil || reserved.Decision.Disposition != runtimebudget.DispositionAllow {
		t.Fatalf("reserve = %#v, %v", reserved, err)
	}
	replayCommand := reserve
	replayCommand.ExpectedRunVersion = reserved.Run.Version
	replayed, err := executionapp.NewController(openVerticalStore(t, fixture), environment.Restart(), environment.Restart(), environment.Restart()).ReserveAutomatedWork(context.Background(), replayCommand)
	if err != nil || !replayed.Replay || replayed.Run.Version != reserved.Run.Version {
		t.Fatalf("restart replay = %#v, %v", replayed, err)
	}

	current := reserved.Run
	commands := []executionapp.BudgetWorkCommand{
		{
			RequestID: "validation-budget-a", RunID: current.ID, ExpectedRunVersion: current.Version,
			LeaseEpoch: project.Lease.Epoch, EffectID: "validation-effect-a", Activity: runtimebudget.ActivityValidationCycle,
			Demand: runtimebudget.Demand{WallTimeMilliseconds: 100}, CandidateSHA: strings.Repeat("a", 40), NowMillis: testNowMillis(),
		},
		{
			RequestID: "validation-budget-b", RunID: current.ID, ExpectedRunVersion: current.Version,
			LeaseEpoch: project.Lease.Epoch, EffectID: "validation-effect-b", Activity: runtimebudget.ActivityValidationCycle,
			Demand: runtimebudget.Demand{WallTimeMilliseconds: 100}, CandidateSHA: strings.Repeat("b", 40), NowMillis: testNowMillis(),
		},
	}
	var wait sync.WaitGroup
	start := make(chan struct{})
	errorsFound := make(chan error, 2)
	for _, contender := range commands {
		contender := contender
		wait.Add(1)
		go func() {
			defer wait.Done()
			<-start
			_, contenderErr := controller.ReserveAutomatedWork(context.Background(), contender)
			errorsFound <- contenderErr
		}()
	}
	close(start)
	wait.Wait()
	close(errorsFound)
	var successes int
	for contenderErr := range errorsFound {
		if contenderErr == nil {
			successes++
		}
	}
	if successes != 1 {
		t.Fatalf("concurrent budget CAS successes = %d", successes)
	}
	afterCAS, err := store.Run(context.Background(), run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(afterCAS.Execution.Budget.Reservations) != 2 {
		t.Fatalf("concurrent writers persisted %d reservations", len(afterCAS.Execution.Budget.Reservations))
	}
	stale := commands[0]
	stale.ExpectedRunVersion = afterCAS.Version
	stale.LeaseEpoch++
	if _, err := controller.ReserveAutomatedWork(context.Background(), stale); err == nil {
		t.Fatal("stale lease reserved automated work")
	}

	usage := runtimebudget.ProviderObservation{
		ID: "provider-usage-unavailable", EffectID: reserve.EffectID, AgentID: "reviewer-1",
		Activity: reserve.Activity, LeaseEpoch: project.Lease.Epoch,
		PolicyRevision: afterCAS.Execution.Budget.Policy.Revision, Sequence: 1,
		ObservedAtMillis: testNowMillis(), ProviderUsage: runtimebudget.ProviderUsage{State: runtimebudget.UsageUnavailable},
	}
	usage.FactHash = runtimebudget.ProviderObservationHash(usage)
	unknown, err := controller.RecordProviderUsage(context.Background(), run.ID, afterCAS.Version, project.Lease.Epoch, usage)
	if err != nil || unknown.Decision.Reason != runtimebudget.ReasonProviderUsageUnavailable ||
		unknown.Run.Execution.NeedsYou == nil || unknown.Run.Execution.NeedsYou.CleanupAuthorized {
		t.Fatalf("unknown usage = %#v, %v", unknown, err)
	}
	blockedWork := commands[0]
	blockedWork.RequestID = "blocked-after-unknown"
	blockedWork.EffectID = "blocked-effect-after-unknown"
	blockedWork.ExpectedRunVersion = unknown.Run.Version
	if _, err := controller.ReserveAutomatedWork(context.Background(), blockedWork); err == nil {
		t.Fatal("provider-unknown pause admitted new automated work")
	}
	reopened := openVerticalStore(t, fixture)
	durable, err := reopened.Run(context.Background(), run.ID)
	if err != nil || durable.Execution.NeedsYou == nil ||
		durable.Execution.NeedsYou.Code != execution.NeedCode(runtimebudget.ReasonProviderUsageUnavailable) ||
		!runtimebudget.ValidLedger(durable.Execution.Budget) || durable.Execution.Budget.Reservations[0].Released {
		t.Fatalf("durable fail-closed ledger = %#v, %v", durable.Execution, err)
	}
}
