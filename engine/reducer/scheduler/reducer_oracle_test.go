// SPDX-License-Identifier: Apache-2.0

package scheduler

import (
	"fmt"
	"reflect"
	"slices"
	"testing"

	domainscheduling "github.com/mcuadros/director-engine/domain/scheduling"
	oracle "github.com/mcuadros/director-engine/internal/testkit/scheduleroracle"
)

type budgetSpec struct {
	state                 uint8
	limit, used, reserved uint64
	requested             uint64
	acknowledgement       uint8
}

type taskSpec struct {
	id, workspace                  string
	queuedAt                       int64
	priority, policy, workClass    uint8
	dependency                     uint8
	launchNow, activeRun           bool
	complete, organizer, preflight bool
	agents, helpers                uint64
	helpersActive, helpersReserved uint64
	time, cost, ci                 budgetSpec
}

type snapshotSpec struct {
	policy                             uint8
	projectActive                      bool
	lease, disk, provider              uint8
	maxTasks, maxWorkspace             uint64
	maxAgents, maxHelpers              uint64
	activeTasks, reservedTasks         uint64
	activeAgents, reservedAgents       uint64
	workspaceActive, workspaceReserved map[string]uint64
	tasks                              []taskSpec
}

func admittedBudgetSpec() budgetSpec {
	return budgetSpec{state: 1, limit: 100, requested: 1, acknowledgement: 1}
}

func admittedTaskSpec(id, workspace string, queuedAt int64) taskSpec {
	return taskSpec{
		id: id, workspace: workspace, queuedAt: queuedAt,
		priority: 3, policy: 1, workClass: 1, dependency: 1,
		complete: true, organizer: true, preflight: true,
		agents: 1, time: admittedBudgetSpec(), cost: admittedBudgetSpec(), ci: admittedBudgetSpec(),
	}
}

func admittedSnapshotSpec(tasks ...taskSpec) snapshotSpec {
	return snapshotSpec{
		policy: 2, projectActive: true, lease: 1, disk: 1, provider: 1,
		maxTasks: 4, maxWorkspace: 2, maxAgents: 8, maxHelpers: 3,
		workspaceActive: map[string]uint64{}, workspaceReserved: map[string]uint64{},
		tasks: tasks,
	}
}

func oracleBudget(spec budgetSpec) oracle.Budget {
	acknowledgements := []oracle.AcknowledgementState{
		0, oracle.AcknowledgementNone, oracle.AcknowledgementCurrent,
		oracle.AcknowledgementStale, oracle.AcknowledgementAmbiguous,
	}
	states := []oracle.BudgetState{0, oracle.BudgetReady, oracle.BudgetUnavailable, oracle.BudgetStale, oracle.BudgetAmbiguous}
	return oracle.NewBudget(spec.limit, spec.used, spec.reserved, spec.requested, acknowledgements[spec.acknowledgement]).WithState(states[spec.state])
}

func productionBudget(name string, spec budgetSpec, ci bool) domainscheduling.Budget {
	states := []domainscheduling.BudgetState{
		"", domainscheduling.BudgetReady, domainscheduling.BudgetUnavailable,
		domainscheduling.BudgetStale, domainscheduling.BudgetAmbiguous,
	}
	acks := []domainscheduling.AcknowledgementState{
		"", domainscheduling.AcknowledgementNone, domainscheduling.AcknowledgementCurrent,
		domainscheduling.AcknowledgementStale, domainscheduling.AcknowledgementAmbiguous,
	}
	ack := acks[spec.acknowledgement]
	ackRevision := ""
	if ack == domainscheduling.AcknowledgementCurrent {
		ackRevision = name + "-revision"
	} else if ack == domainscheduling.AcknowledgementStale {
		ackRevision = name + "-previous-revision"
	}
	if ci {
		ack = domainscheduling.AcknowledgementNone
		ackRevision = ""
	}
	return domainscheduling.Budget{
		State: states[spec.state], Revision: name + "-revision", Limit: spec.limit,
		Used: spec.used, Reserved: spec.reserved, Requested: spec.requested,
		Acknowledgement: ack, AcknowledgedRevision: ackRevision,
	}
}

func oracleTask(spec taskSpec) oracle.TaskFacts {
	priorities := []oracle.Priority{0, oracle.PriorityUrgent, oracle.PriorityHigh, oracle.PriorityNormal, oracle.PriorityLow}
	policies := []oracle.PolicyOverride{0, oracle.PolicyInherit, oracle.PolicyForceManual, oracle.PolicyForceAutomatic}
	dependencies := []oracle.DependencyState{0, oracle.DependenciesSatisfied, oracle.DependenciesWaiting, oracle.DependenciesAuditedOverride}
	task := oracle.NewTask(oracle.TaskID(spec.id), oracle.WorkspaceID(spec.workspace), spec.queuedAt).
		WithPriority(priorities[spec.priority]).WithPolicyOverride(policies[spec.policy]).
		WithLaunchNow(spec.launchNow).WithDependency(dependencies[spec.dependency]).
		WithActiveRun(spec.activeRun).WithRequiredFacts(spec.complete, spec.organizer, spec.preflight).
		WithHelperUsage(spec.helpersActive, spec.helpersReserved).
		WithBudgets(oracle.NewBudgets(oracleBudget(spec.time), oracleBudget(spec.cost), oracleBudget(spec.ci)))
	if spec.workClass == 2 {
		task = task.AsActiveRunProgression(oracle.ProgressionDemand(spec.agents, spec.helpers)).WithActiveRun(spec.activeRun)
	}
	return task
}

func productionTask(spec taskSpec) domainscheduling.TaskFacts {
	priorities := []domainscheduling.Priority{"", domainscheduling.PriorityUrgent, domainscheduling.PriorityHigh, domainscheduling.PriorityNormal, domainscheduling.PriorityLow}
	policies := []domainscheduling.PolicyOverride{"", domainscheduling.PolicyInherit, domainscheduling.PolicyForceManual, domainscheduling.PolicyForceAutomatic}
	classes := []domainscheduling.WorkClass{"", domainscheduling.WorkNewRun, domainscheduling.WorkActiveRunProgression}
	dependencies := []domainscheduling.DependencyState{"", domainscheduling.DependenciesSatisfied, domainscheduling.DependenciesWaiting, domainscheduling.DependenciesAuditedOverride}
	task := domainscheduling.TaskFacts{
		ID: domainscheduling.TaskID(spec.id), Workspace: domainscheduling.WorkspaceID(spec.workspace),
		TaskVersion: 1, QueuedAtUnixMillis: spec.queuedAt, Priority: priorities[spec.priority],
		PolicyOverride: policies[spec.policy], WorkClass: classes[spec.workClass], Dependency: dependencies[spec.dependency],
		HasActiveRun: spec.activeRun, TaskComplete: spec.complete, OrganizerApproved: spec.organizer,
		PreflightReady: spec.preflight, HelpersActive: spec.helpersActive, HelpersReserved: spec.helpersReserved,
		Budgets: domainscheduling.Budgets{
			Time: productionBudget("time", spec.time, false), Cost: productionBudget("cost", spec.cost, false),
			CI: productionBudget("ci", spec.ci, true),
		},
	}
	if spec.workClass == 1 {
		task.Demand = domainscheduling.NewRunDemand()
	} else {
		task.Demand = domainscheduling.ProgressionDemand(spec.agents, spec.helpers)
	}
	if spec.launchNow {
		task.LaunchNow = domainscheduling.LaunchNowRequest{
			Requested: true, ActorKind: "human", ActorID: "human-1", AuditID: "launch-audit-1", TaskVersion: task.TaskVersion,
		}
	}
	if task.Dependency == domainscheduling.DependenciesAuditedOverride {
		task.DependencyOverrideAuditIDs = []string{"dependency-audit-1"}
		task.DependencyOverrideVersion = task.TaskVersion
	}
	return task
}

func oracleSnapshot(spec snapshotSpec) oracle.Snapshot {
	policies := []oracle.LaunchPolicy{0, oracle.PolicyManual, oracle.PolicyAutomatic}
	leases := []oracle.LeaseState{0, oracle.LeaseCurrent, oracle.LeaseLost, oracle.LeaseStale, oracle.LeaseAmbiguous}
	disks := []oracle.DiskState{0, oracle.DiskReady, oracle.DiskUnavailable, oracle.DiskLimitExceeded, oracle.DiskStale, oracle.DiskAmbiguous}
	providers := []oracle.ProviderState{0, oracle.ProviderReady, oracle.ProviderUnavailable, oracle.ProviderNotAdmitted, oracle.ProviderStale, oracle.ProviderAmbiguous}
	tasks := make([]oracle.TaskFacts, 0, len(spec.tasks))
	for _, task := range spec.tasks {
		tasks = append(tasks, oracleTask(task))
	}
	snapshot := oracle.NewSnapshot(
		policies[spec.policy], oracle.NewLimits(spec.maxTasks, spec.maxWorkspace, spec.maxAgents, spec.maxHelpers),
		oracle.NewUsage(spec.activeTasks, spec.reservedTasks, spec.activeAgents, spec.reservedAgents), tasks...,
	).WithProjectActive(spec.projectActive).WithLease(leases[spec.lease]).WithDisk(disks[spec.disk]).WithProvider(providers[spec.provider])
	workspaceIDs := make([]string, 0, len(spec.workspaceActive))
	for workspace := range spec.workspaceActive {
		workspaceIDs = append(workspaceIDs, workspace)
	}
	slices.Sort(workspaceIDs)
	usages := make([]oracle.WorkspaceUsage, 0, len(workspaceIDs))
	for _, workspace := range workspaceIDs {
		usages = append(usages, oracle.NewWorkspaceUsage(oracle.WorkspaceID(workspace), spec.workspaceActive[workspace], spec.workspaceReserved[workspace]))
	}
	return snapshot.WithWorkspaceUsage(usages...)
}

func productionSnapshot(spec snapshotSpec) domainscheduling.Snapshot {
	policies := []domainscheduling.LaunchPolicy{"", domainscheduling.PolicyManual, domainscheduling.PolicyAutomatic}
	leases := []domainscheduling.LeaseState{"", domainscheduling.LeaseCurrent, domainscheduling.LeaseLost, domainscheduling.LeaseStale, domainscheduling.LeaseAmbiguous}
	disks := []domainscheduling.DiskState{"", domainscheduling.DiskReady, domainscheduling.DiskUnavailable, domainscheduling.DiskLimitExceeded, domainscheduling.DiskStale, domainscheduling.DiskAmbiguous}
	providers := []domainscheduling.ProviderState{"", domainscheduling.ProviderReady, domainscheduling.ProviderUnavailable, domainscheduling.ProviderNotAdmitted, domainscheduling.ProviderStale, domainscheduling.ProviderAmbiguous}
	snapshot := domainscheduling.Snapshot{
		SchemaVersion: domainscheduling.SchemaVersion, ProjectID: "project-1", Version: 1,
		Policy: policies[spec.policy], ProjectActive: spec.projectActive, Lease: leases[spec.lease], LeaseEpoch: 1,
		Disk: disks[spec.disk], Provider: providers[spec.provider],
		Limits: domainscheduling.Limits{
			MaxActiveTasks: spec.maxTasks, MaxActiveTasksPerWorkspace: spec.maxWorkspace,
			MaxConcurrentAgents: spec.maxAgents, MaxHelpersPerTask: spec.maxHelpers,
		},
		Usage: domainscheduling.Usage{
			ActiveTasks: spec.activeTasks, ReservedTasks: spec.reservedTasks,
			ActiveAgents: spec.activeAgents, ReservedAgents: spec.reservedAgents,
		},
	}
	for workspace, active := range spec.workspaceActive {
		snapshot.WorkspaceUsages = append(snapshot.WorkspaceUsages, domainscheduling.WorkspaceUsage{
			Workspace: domainscheduling.WorkspaceID(workspace), ActiveTasks: active,
			ReservedTasks: spec.workspaceReserved[workspace],
		})
	}
	slices.SortFunc(snapshot.WorkspaceUsages, func(left, right domainscheduling.WorkspaceUsage) int {
		return compareTasks(
			domainscheduling.TaskFacts{ID: domainscheduling.TaskID(left.Workspace)},
			domainscheduling.TaskFacts{ID: domainscheduling.TaskID(right.Workspace)}, allGuards(),
		)
	})
	for _, task := range spec.tasks {
		snapshot.Tasks = append(snapshot.Tasks, productionTask(task))
	}
	return snapshot
}

func compareOracle(t *testing.T, spec snapshotSpec) {
	t.Helper()
	want := oracle.Evaluate(oracleSnapshot(spec))
	got := Reduce(productionSnapshot(spec))
	gotIDs := make([]oracle.TaskID, len(got.OrderedTaskIDs))
	for index, id := range got.OrderedTaskIDs {
		gotIDs[index] = oracle.TaskID(id)
	}
	gotExplanations := make([]oracle.Explanation, len(got.Explanations))
	for index, explanation := range got.Explanations {
		gotExplanations[index] = oracle.Explanation{TaskID: oracle.TaskID(explanation.TaskID), Code: oracle.Code(explanation.Code)}
	}
	if !reflect.DeepEqual(gotIDs, want.OrderedTaskIDs) || !reflect.DeepEqual(gotExplanations, want.Explanations) {
		t.Fatalf("scheduler disagrees with independent oracle\nspec=%#v\ngot=%#v\nwant=%#v", spec, got, want)
	}
}

type stream uint64

func (value *stream) next(bound uint64) uint64 {
	*value = *value*6364136223846793005 + 1442695040888963407
	if bound == 0 {
		return 0
	}
	return uint64(*value>>32) % bound
}

func generatedBudget(values *stream) budgetSpec {
	limit := values.next(100) + 1
	return budgetSpec{
		state: uint8(values.next(4) + 1), limit: limit,
		used: values.next(limit + 2), reserved: values.next(limit + 2), requested: values.next(4),
		acknowledgement: uint8(values.next(4) + 1),
	}
}

func generatedSpec(seed uint64) snapshotSpec {
	values := stream(seed + 1)
	maxTasks := values.next(5) + 1
	maxWorkspace := values.next(maxTasks) + 1
	maxAgents := maxTasks + values.next(5)
	spec := snapshotSpec{
		policy: uint8(values.next(2) + 1), projectActive: values.next(5) != 0,
		lease: uint8(values.next(4) + 1), disk: uint8(values.next(5) + 1), provider: uint8(values.next(5) + 1),
		maxTasks: maxTasks, maxWorkspace: maxWorkspace, maxAgents: maxAgents, maxHelpers: values.next(4) + 1,
		activeTasks: values.next(maxTasks + 2), reservedTasks: values.next(3),
		activeAgents: values.next(maxAgents + 2), reservedAgents: values.next(3),
		workspaceActive: map[string]uint64{}, workspaceReserved: map[string]uint64{},
	}
	for workspace := 0; workspace < 3; workspace++ {
		id := fmt.Sprintf("workspace-%d", workspace)
		spec.workspaceActive[id] = values.next(maxWorkspace + 2)
		spec.workspaceReserved[id] = values.next(3)
	}
	count := int(values.next(8) + 1)
	for index := 0; index < count; index++ {
		task := admittedTaskSpec(fmt.Sprintf("task-%03d", index), fmt.Sprintf("workspace-%d", values.next(3)), int64(values.next(20)))
		task.priority = uint8(values.next(4) + 1)
		task.policy = uint8(values.next(3) + 1)
		task.workClass = uint8(values.next(2) + 1)
		task.dependency = uint8(values.next(3) + 1)
		task.launchNow = values.next(4) == 0
		task.activeRun = task.workClass == 2
		if task.workClass == 1 && values.next(6) == 0 {
			task.activeRun = true
		}
		task.complete = values.next(6) != 0
		task.organizer = values.next(6) != 0
		task.preflight = values.next(6) != 0
		if task.workClass == 2 {
			task.agents = values.next(3)
			task.helpers = values.next(task.agents + 1)
		}
		task.helpersActive = values.next(spec.maxHelpers + 2)
		task.helpersReserved = values.next(3)
		task.time, task.cost, task.ci = generatedBudget(&values), generatedBudget(&values), generatedBudget(&values)
		spec.tasks = append(spec.tasks, task)
	}
	return spec
}

func TestReducerMatchesIndependentSchedulerOracleSeededCorpus(t *testing.T) {
	for seed := uint64(0); seed < 512; seed++ {
		t.Run(fmt.Sprintf("seed-%03d", seed), func(t *testing.T) {
			compareOracle(t, generatedSpec(seed))
		})
	}
}

func TestReducerOrderingAndNoPreemptionMatchOracle(t *testing.T) {
	progression := admittedTaskSpec("task-progression", "workspace-1", 9)
	progression.workClass, progression.activeRun, progression.agents = 2, true, 0
	launchNow := admittedTaskSpec("task-launch", "workspace-1", 8)
	launchNow.launchNow, launchNow.priority = true, 4
	urgentLater := admittedTaskSpec("task-urgent", "workspace-2", 7)
	urgentLater.priority = 1
	highFIFO := admittedTaskSpec("task-high-a", "workspace-2", 2)
	highFIFO.priority = 2
	highStable := admittedTaskSpec("task-high-b", "workspace-3", 2)
	highStable.priority = 2
	spec := admittedSnapshotSpec(highStable, urgentLater, progression, launchNow, highFIFO)
	compareOracle(t, spec)
	got := Reduce(productionSnapshot(spec))
	want := []domainscheduling.TaskID{"task-progression", "task-launch", "task-urgent", "task-high-a", "task-high-b"}
	if !slices.Equal(got.OrderedTaskIDs, want) {
		t.Fatalf("ordered Tasks = %v, want %v", got.OrderedTaskIDs, want)
	}
	// Progression preference changes ordering only; no Task is interrupted or
	// removed from the immutable input.
	if len(spec.tasks) != 5 || !spec.tasks[2].activeRun {
		t.Fatal("scheduler preempted or mutated an active Run")
	}
	reversed := productionSnapshot(spec)
	slices.Reverse(reversed.Tasks)
	slices.Reverse(reversed.WorkspaceUsages)
	if other := Reduce(reversed); !reflect.DeepEqual(got, other) {
		t.Fatalf("logical fact reordering changed result\nfirst=%#v\nsecond=%#v", got, other)
	}
}

func TestReducerRequiresSeparateCurrentHumanAudits(t *testing.T) {
	task := admittedTaskSpec("task-1", "workspace-1", 1)
	task.dependency, task.launchNow = 3, true
	snapshot := productionSnapshot(admittedSnapshotSpec(task))
	if decision := Reduce(snapshot); len(decision.OrderedTaskIDs) != 1 || decision.Explanations[0].Code != CodeSelectedLaunchNow {
		t.Fatalf("valid audited launch = %#v", decision)
	}
	snapshot.Tasks[0].DependencyOverrideAuditIDs = nil
	if decision := Reduce(snapshot); decision.Explanations[0].Code != CodeFactsAmbiguous {
		t.Fatalf("missing dependency audit = %#v", decision)
	}
	snapshot = productionSnapshot(admittedSnapshotSpec(task))
	snapshot.Tasks[0].LaunchNow = domainscheduling.LaunchNowRequest{Requested: true, ActorKind: "agent", AuditID: "launch-audit-1", TaskVersion: 1}
	if decision := Reduce(snapshot); decision.Explanations[0].Code != CodeFactsAmbiguous {
		t.Fatalf("agent launch-now request = %#v", decision)
	}
	task.dependency = 2
	compareOracle(t, admittedSnapshotSpec(task))
	if decision := Reduce(productionSnapshot(admittedSnapshotSpec(task))); decision.Explanations[0].Code != CodeDependencyWait {
		t.Fatalf("Launch now bypassed dependency = %#v", decision)
	}
}

func TestManualAutomaticAndTaskOverridesMatchOracle(t *testing.T) {
	testCases := []struct {
		name   string
		policy uint8
		change func(*taskSpec)
		want   Code
	}{
		{"manual-waits", 1, func(*taskSpec) {}, CodeManualPolicyWait},
		{"manual-launch-now", 1, func(task *taskSpec) { task.launchNow = true }, CodeSelectedLaunchNow},
		{"manual-force-automatic", 1, func(task *taskSpec) { task.policy = 3 }, CodeSelectedAutomatic},
		{"automatic-force-manual", 2, func(task *taskSpec) { task.policy = 2 }, CodeManualPolicyWait},
		{"active-progression-ignores-new-run-policy", 1, func(task *taskSpec) {
			task.workClass, task.activeRun, task.agents = 2, true, 0
		}, CodeSelectedActiveProgression},
	}
	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			task := admittedTaskSpec("task-1", "workspace-1", 1)
			testCase.change(&task)
			spec := admittedSnapshotSpec(task)
			spec.policy = testCase.policy
			compareOracle(t, spec)
			if got := Reduce(productionSnapshot(spec)).Explanations[0].Code; got != testCase.want {
				t.Fatalf("policy code = %q, want %q", got, testCase.want)
			}
		})
	}
}

func TestReducerBudgetBoundariesAndAcknowledgementRevision(t *testing.T) {
	for _, testCase := range []struct {
		name string
		used uint64
		ack  uint8
		want Code
	}{
		{"below-soft", 83, 1, CodeSelectedAutomatic},
		{"exact-soft", 84, 1, CodeTimeSoftBudget},
		{"acknowledged-soft", 84, 2, CodeSelectedAutomatic},
		{"below-hard", 98, 2, CodeSelectedAutomatic},
		{"exact-hard", 99, 2, CodeTimeHardBudget},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			task := admittedTaskSpec("task-1", "workspace-1", 1)
			task.time.used, task.time.acknowledgement = testCase.used, testCase.ack
			spec := admittedSnapshotSpec(task)
			compareOracle(t, spec)
			if got := Reduce(productionSnapshot(spec)).Explanations[0].Code; got != testCase.want {
				t.Fatalf("budget code = %q, want %q", got, testCase.want)
			}
		})
	}
	task := admittedTaskSpec("task-1", "workspace-1", 1)
	task.time.used, task.time.acknowledgement = 84, 3
	decision := Reduce(productionSnapshot(admittedSnapshotSpec(task)))
	if decision.Explanations[0].Code != CodeFactsStale {
		t.Fatalf("stale acknowledgement revision = %#v", decision)
	}
}

func TestEveryOrderingCapacityAndReservationGuardHasOracleWitness(t *testing.T) {
	witnesses := map[guard]snapshotSpec{}
	progression := admittedTaskSpec("task-z", "workspace-1", 2)
	progression.workClass, progression.activeRun, progression.agents = 2, true, 0
	urgent := admittedTaskSpec("task-a", "workspace-2", 1)
	urgent.priority = 1
	witnesses[guardOrderProgression] = admittedSnapshotSpec(urgent, progression)
	launch := admittedTaskSpec("task-z", "workspace-1", 2)
	launch.launchNow, launch.priority = true, 4
	witnesses[guardOrderLaunchNow] = admittedSnapshotSpec(urgent, launch)
	lowEarly := admittedTaskSpec("task-z", "workspace-1", 1)
	lowEarly.priority = 4
	priorityUrgent := admittedTaskSpec("task-a", "workspace-2", 2)
	priorityUrgent.priority = 1
	witnesses[guardOrderPriority] = admittedSnapshotSpec(lowEarly, priorityUrgent)
	later := admittedTaskSpec("task-a", "workspace-1", 2)
	earlier := admittedTaskSpec("task-z", "workspace-2", 1)
	witnesses[guardOrderFIFO] = admittedSnapshotSpec(later, earlier)
	witnesses[guardOrderStableID] = admittedSnapshotSpec(admittedTaskSpec("task-z", "workspace-1", 1), admittedTaskSpec("task-a", "workspace-2", 1))
	capacityTask := admittedTaskSpec("task-1", "workspace-1", 1)
	projectCapacity := admittedSnapshotSpec(capacityTask)
	projectCapacity.activeTasks = projectCapacity.maxTasks
	witnesses[guardProjectCapacity] = projectCapacity
	workspaceCapacity := admittedSnapshotSpec(capacityTask)
	workspaceCapacity.workspaceActive["workspace-1"] = workspaceCapacity.maxWorkspace
	witnesses[guardWorkspaceCapacity] = workspaceCapacity
	agentCapacity := admittedSnapshotSpec(capacityTask)
	agentCapacity.activeAgents = agentCapacity.maxAgents
	witnesses[guardAgentCapacity] = agentCapacity
	helperTask := admittedTaskSpec("task-1", "workspace-1", 1)
	helperTask.workClass, helperTask.activeRun, helperTask.agents, helperTask.helpers = 2, true, 1, 1
	helperTask.helpersActive = 3
	witnesses[guardHelperCapacity] = admittedSnapshotSpec(helperTask)
	reservedCapacity := admittedSnapshotSpec(capacityTask)
	reservedCapacity.activeTasks, reservedCapacity.reservedTasks = 3, 1
	witnesses[guardCapacityReservations] = reservedCapacity
	budgetReservation := admittedTaskSpec("task-1", "workspace-1", 1)
	budgetReservation.time.used, budgetReservation.time.reserved, budgetReservation.time.requested = 80, 19, 1
	budgetReservation.time.acknowledgement = 2
	witnesses[guardBudgetReservations] = admittedSnapshotSpec(budgetReservation)
	duplicate := admittedTaskSpec("task-1", "workspace-1", 1)
	duplicate.activeRun = true
	witnesses[guardNoDuplicateActiveRun] = admittedSnapshotSpec(duplicate)

	for current := guard(0); current < guardCount; current++ {
		t.Run(fmt.Sprintf("guard-%d", current), func(t *testing.T) {
			spec, ok := witnesses[current]
			if !ok {
				t.Fatalf("missing mutation witness for guard %d", current)
			}
			compareOracle(t, spec)
			baseline := Reduce(productionSnapshot(spec))
			guards := allGuards()
			guards[current] = false
			mutated := reduceWithGuards(productionSnapshot(spec), guards)
			if reflect.DeepEqual(baseline.OrderedTaskIDs, mutated.OrderedTaskIDs) && reflect.DeepEqual(baseline.Explanations, mutated.Explanations) {
				t.Fatalf("guard %d mutation survived oracle witness: %#v", current, baseline)
			}
		})
	}
}

func TestReducerCodeVocabularyMatchesPreservedOracle(t *testing.T) {
	want := oracle.Codes()
	got := Codes()
	if len(got) != len(want) {
		t.Fatalf("code count = %d, want %d", len(got), len(want))
	}
	for index := range got {
		if string(got[index]) != string(want[index]) {
			t.Fatalf("code[%d] = %q, want %q", index, got[index], want[index])
		}
	}
}
