// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"fmt"
	"sync"
	"testing"

	"github.com/mcuadros/director-engine/adapters/fake"
	applicationscheduling "github.com/mcuadros/director-engine/application/scheduling"
	domainscheduling "github.com/mcuadros/director-engine/domain/scheduling"
	storeport "github.com/mcuadros/director-engine/ports/scheduling"
	"github.com/mcuadros/director-engine/reducer/scheduler"
)

func budget(name string, limit, used, reserved, requested uint64, acknowledged bool) domainscheduling.Budget {
	result := domainscheduling.Budget{
		State: domainscheduling.BudgetReady, Revision: name + "-revision", Limit: limit,
		Used: used, Reserved: reserved, Requested: requested,
		Acknowledgement: domainscheduling.AcknowledgementNone,
	}
	if acknowledged {
		result.Acknowledgement = domainscheduling.AcknowledgementCurrent
		result.AcknowledgedRevision = result.Revision
	}
	return result
}

func task(id, workspace string, queuedAt int64) domainscheduling.TaskFacts {
	return domainscheduling.TaskFacts{
		ID: domainscheduling.TaskID(id), Workspace: domainscheduling.WorkspaceID(workspace), TaskVersion: 1,
		QueuedAtUnixMillis: queuedAt, Priority: domainscheduling.PriorityNormal,
		PolicyOverride: domainscheduling.PolicyInherit, WorkClass: domainscheduling.WorkNewRun,
		Dependency: domainscheduling.DependenciesSatisfied, TaskComplete: true,
		OrganizerApproved: true, PreflightReady: true, Demand: domainscheduling.NewRunDemand(),
		Budgets: domainscheduling.Budgets{
			Time: budget("time", 100, 0, 0, 1, false), Tokens: budget("tokens", 100, 0, 0, 1, false),
			Turns: budget("turns", 100, 0, 0, 1, false), Cost: budget("cost", 100, 0, 0, 1, false),
			CI: budget("ci", 4, 0, 0, 1, false),
		},
	}
}

func snapshot(tasks ...domainscheduling.TaskFacts) domainscheduling.Snapshot {
	return domainscheduling.Snapshot{
		SchemaVersion: domainscheduling.SchemaVersion, ProjectID: "project-1", Version: 1,
		Policy: domainscheduling.PolicyAutomatic, ProjectActive: true,
		Lease: domainscheduling.LeaseCurrent, LeaseEpoch: 7,
		Disk: domainscheduling.DiskReady, Provider: domainscheduling.ProviderReady,
		Limits: domainscheduling.DefaultLimits(), Tasks: tasks,
	}
}

func TestConcurrentSchedulerClaimsNeverExceedProjectWorkspaceOrAgentCapacity(t *testing.T) {
	facts := snapshot(task("task-a", "workspace-1", 1), task("task-b", "workspace-1", 2))
	facts.Limits = domainscheduling.Limits{
		MaxActiveTasks: 1, MaxActiveTasksPerWorkspace: 1, MaxConcurrentAgents: 1, MaxHelpersPerTask: 1,
	}
	store := fake.NewReservationStore(facts)
	const contenders = 32
	results := make(chan applicationscheduling.Result, contenders)
	errors := make(chan error, contenders)
	var ready sync.WaitGroup
	var start sync.WaitGroup
	ready.Add(contenders)
	start.Add(1)
	for index := 0; index < contenders; index++ {
		go func(index int) {
			ready.Done()
			start.Wait()
			result, err := applicationscheduling.NewService(store.Restart()).Schedule(context.Background(), applicationscheduling.Command{
				RequestID: fmt.Sprintf("concurrent-request-%04d", index), ProjectID: "project-1",
			})
			results <- result
			errors <- err
		}(index)
	}
	ready.Wait()
	start.Done()
	var applied int
	for index := 0; index < contenders; index++ {
		if err := <-errors; err != nil {
			t.Fatalf("concurrent schedule: %v", err)
		}
		result := <-results
		if result.Reservation.Outcome == storeport.ReserveApplied {
			applied++
		}
	}
	if applied != 1 || len(store.Reservations()) != 1 {
		t.Fatalf("applied batches=%d reservations=%d", applied, len(store.Reservations()))
	}
	current, err := store.Snapshot(context.Background(), "project-1")
	if err != nil {
		t.Fatal(err)
	}
	if current.Usage.ActiveTasks+current.Usage.ReservedTasks > current.Limits.MaxActiveTasks ||
		current.Usage.ActiveAgents+current.Usage.ReservedAgents > current.Limits.MaxConcurrentAgents {
		t.Fatalf("capacity exceeded after concurrent claims: %#v", current)
	}
	if len(current.WorkspaceUsages) != 1 || current.WorkspaceUsages[0].ReservedTasks != 1 {
		t.Fatalf("Workspace reservation = %#v", current.WorkspaceUsages)
	}
}

func TestEveryCapacityBudgetAndEligibilityRefusalStopsReservation(t *testing.T) {
	testCases := []struct {
		name   string
		change func(*domainscheduling.Snapshot)
		want   scheduler.Code
	}{
		{"project-capacity", func(value *domainscheduling.Snapshot) { value.Usage.ActiveTasks = value.Limits.MaxActiveTasks }, scheduler.CodeProjectCapacity},
		{"workspace-capacity", func(value *domainscheduling.Snapshot) {
			value.WorkspaceUsages = []domainscheduling.WorkspaceUsage{{Workspace: "workspace-1", ActiveTasks: value.Limits.MaxActiveTasksPerWorkspace}}
		}, scheduler.CodeWorkspaceCapacity},
		{"agent-capacity", func(value *domainscheduling.Snapshot) { value.Usage.ActiveAgents = value.Limits.MaxConcurrentAgents }, scheduler.CodeAgentCapacity},
		{"helper-capacity", func(value *domainscheduling.Snapshot) {
			value.Tasks[0].WorkClass = domainscheduling.WorkActiveRunProgression
			value.Tasks[0].HasActiveRun = true
			value.Tasks[0].Demand = domainscheduling.ProgressionDemand(1, 1)
			value.Tasks[0].HelpersActive = value.Limits.MaxHelpersPerTask
		}, scheduler.CodeHelperCapacity},
		{"time-soft", func(value *domainscheduling.Snapshot) { value.Tasks[0].Budgets.Time.Used = 84 }, scheduler.CodeTimeSoftBudget},
		{"time-hard", func(value *domainscheduling.Snapshot) { value.Tasks[0].Budgets.Time.Used = 99 }, scheduler.CodeTimeHardBudget},
		{"token-soft", func(value *domainscheduling.Snapshot) { value.Tasks[0].Budgets.Tokens.Used = 84 }, scheduler.CodeTokenSoftBudget},
		{"turn-hard", func(value *domainscheduling.Snapshot) { value.Tasks[0].Budgets.Turns.Used = 99 }, scheduler.CodeTurnHardBudget},
		{"cost-hard", func(value *domainscheduling.Snapshot) { value.Tasks[0].Budgets.Cost.Used = 99 }, scheduler.CodeCostHardBudget},
		{"ci-hard", func(value *domainscheduling.Snapshot) { value.Tasks[0].Budgets.CI.Used = 3 }, scheduler.CodeCIHardBudget},
		{"disk-hard", func(value *domainscheduling.Snapshot) { value.Disk = domainscheduling.DiskLimitExceeded }, scheduler.CodeDiskLimit},
		{"dependency", func(value *domainscheduling.Snapshot) {
			value.Tasks[0].Dependency = domainscheduling.DependenciesWaiting
			value.Tasks[0].LaunchNow = domainscheduling.LaunchNowRequest{
				Requested: true, ActorKind: "human", ActorID: "human-1", AuditID: "launch-audit-1", TaskVersion: 1,
			}
		}, scheduler.CodeDependencyWait},
		{"duplicate-active-run", func(value *domainscheduling.Snapshot) { value.Tasks[0].HasActiveRun = true }, scheduler.CodeDuplicateActiveRun},
	}
	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			facts := snapshot(task("task-a", "workspace-1", 1))
			testCase.change(&facts)
			store := fake.NewReservationStore(facts)
			result, err := applicationscheduling.NewService(store).Schedule(context.Background(), applicationscheduling.Command{
				RequestID: "refusal-request-" + testCase.name, ProjectID: "project-1",
			})
			if err != nil {
				t.Fatal(err)
			}
			if len(result.Decision.Explanations) != 1 || result.Decision.Explanations[0].Code != testCase.want {
				t.Fatalf("decision = %#v, want %q", result.Decision, testCase.want)
			}
			if len(store.Reservations()) != 0 || result.Reservation.Outcome != "" {
				t.Fatalf("refused work mutated reservations: result=%#v reservations=%#v", result, store.Reservations())
			}
		})
	}
}

func TestSchedulerAllowsOnlyAnExactlyDisabledOptionalCostBudget(t *testing.T) {
	facts := snapshot(task("task-a", "workspace-1", 1))
	facts.Tasks[0].Budgets.Cost = domainscheduling.Budget{
		State: domainscheduling.BudgetDisabled, Acknowledgement: domainscheduling.AcknowledgementNone,
	}
	decision := scheduler.Reduce(facts)
	if len(decision.OrderedTaskIDs) != 1 || decision.Explanations[0].Code != scheduler.CodeSelectedAutomatic {
		t.Fatalf("disabled optional cost decision = %#v", decision)
	}
	facts.Tasks[0].Budgets.Cost.Limit = 1
	decision = scheduler.Reduce(facts)
	if len(decision.OrderedTaskIDs) != 0 || decision.Explanations[0].Code != scheduler.CodeFactsAmbiguous {
		t.Fatalf("malformed disabled cost decision = %#v", decision)
	}
}

func TestLeaseLossBlocksEffectAndReconciliationRestoresOnlyExactEpoch(t *testing.T) {
	store := fake.NewReservationStore(snapshot(task("task-a", "workspace-1", 1)))
	service := applicationscheduling.NewService(store)
	result, err := service.Schedule(context.Background(), applicationscheduling.Command{
		RequestID: "schedule-request-0001", ProjectID: "project-1",
	})
	if err != nil || result.Reservation.Outcome != storeport.ReserveApplied || len(store.Reservations()) != 1 {
		t.Fatalf("reserve = %#v, %v", result, err)
	}
	reservation := store.Reservations()[0]
	if err := store.SetLease(domainscheduling.LeaseLost, 7); err != nil {
		t.Fatal(err)
	}
	denied, err := service.AuthorizeLaunch(context.Background(), applicationscheduling.Command{
		RequestID: "authorize-request-0001", ProjectID: "project-1",
	}, reservation.ID, 7)
	if err != nil || denied.Allowed {
		t.Fatalf("permit after lease loss = %#v, %v", denied, err)
	}
	if err := store.SetLease(domainscheduling.LeaseCurrent, 7); err != nil {
		t.Fatal(err)
	}
	allowed, err := service.AuthorizeLaunch(context.Background(), applicationscheduling.Command{
		RequestID: "authorize-request-0002", ProjectID: "project-1",
	}, reservation.ID, 7)
	if err != nil || !allowed.Allowed || allowed.ID == "" {
		t.Fatalf("permit after exact reconciliation = %#v, %v", allowed, err)
	}
	replay, err := service.AuthorizeLaunch(context.Background(), applicationscheduling.Command{
		RequestID: "authorize-request-0002", ProjectID: "project-1",
	}, reservation.ID, 7)
	if err != nil || !replay.Allowed || !replay.Replay || replay.ID != allowed.ID {
		t.Fatalf("permit replay = %#v, %v", replay, err)
	}
	second, err := service.AuthorizeLaunch(context.Background(), applicationscheduling.Command{
		RequestID: "authorize-request-0003", ProjectID: "project-1",
	}, reservation.ID, 7)
	if err != nil || second.Allowed {
		t.Fatalf("second one-use permit = %#v, %v", second, err)
	}
}

func TestLeaseTakeoverCannotUsePriorEpochReservation(t *testing.T) {
	store := fake.NewReservationStore(snapshot(task("task-a", "workspace-1", 1)))
	service := applicationscheduling.NewService(store)
	_, err := service.Schedule(context.Background(), applicationscheduling.Command{
		RequestID: "schedule-request-0002", ProjectID: "project-1",
	})
	if err != nil {
		t.Fatal(err)
	}
	reservation := store.Reservations()[0]
	if err := store.SetLease(domainscheduling.LeaseCurrent, 8); err != nil {
		t.Fatal(err)
	}
	permit, err := service.AuthorizeLaunch(context.Background(), applicationscheduling.Command{
		RequestID: "authorize-request-0004", ProjectID: "project-1",
	}, reservation.ID, 7)
	if err != nil || permit.Allowed {
		t.Fatalf("prior epoch permit = %#v, %v", permit, err)
	}
}

func TestFakeReservationStateSurvivesRestartAndBatchReplayIsIdempotent(t *testing.T) {
	facts := snapshot(task("task-a", "workspace-1", 1))
	store := fake.NewReservationStore(facts)
	service := applicationscheduling.NewService(store)
	result, err := service.Schedule(context.Background(), applicationscheduling.Command{
		RequestID: "schedule-request-0003", ProjectID: "project-1",
	})
	if err != nil || result.Reservation.Outcome != storeport.ReserveApplied {
		t.Fatalf("schedule = %#v, %v", result, err)
	}
	reservation := store.Reservations()[0]
	batch := storeport.ReservationBatch{
		ID: result.BatchID, ProjectID: "project-1", ExpectedSnapshotVersion: facts.Version,
		FactsHash: result.Decision.FactsHash, LeaseEpoch: facts.LeaseEpoch,
		Reservations: []domainscheduling.Reservation{reservation},
	}
	replayed, err := store.Restart().CompareAndReserve(context.Background(), batch)
	if err != nil || !replayed.Replay || replayed.Outcome != storeport.ReserveApplied {
		t.Fatalf("batch replay = %#v, %v", replayed, err)
	}
	if len(store.Restart().Reservations()) != 1 {
		t.Fatalf("restart duplicated reservations: %#v", store.Reservations())
	}
	batch.Reservations[0].TimeRequested++
	if _, err := store.CompareAndReserve(context.Background(), batch); err != storeport.ErrIdempotencyConflict {
		t.Fatalf("different-payload replay error = %v", err)
	}
}

func TestOrganizerConsumesNoAgentSlotAndHardLimitsStillWin(t *testing.T) {
	facts := snapshot(task("task-a", "workspace-1", 1))
	if limits := domainscheduling.DefaultLimits(); limits.MaxActiveTasks != 6 || limits.MaxActiveTasksPerWorkspace != 2 ||
		limits.MaxConcurrentAgents != 8 || limits.MaxHelpersPerTask != 3 {
		t.Fatalf("PLAN defaults = %#v", limits)
	}
	facts.Usage.ActiveAgents = 7
	decision := scheduler.Reduce(facts)
	if len(decision.OrderedTaskIDs) != 1 {
		t.Fatalf("Organizer incorrectly consumed an agent slot: %#v", decision)
	}
	facts.Usage.ActiveAgents = 8
	decision = scheduler.Reduce(facts)
	if decision.Explanations[0].Code != scheduler.CodeAgentCapacity {
		t.Fatalf("hard agent capacity = %#v", decision)
	}
}
