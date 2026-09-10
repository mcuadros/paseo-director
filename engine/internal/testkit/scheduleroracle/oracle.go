// SPDX-License-Identifier: Apache-2.0

package scheduleroracle

import (
	"math/bits"
	"slices"
)

// Code is the closed scheduler explanation/refusal vocabulary.
type Code string

const (
	CodeSelectedActiveProgression Code = "selected_active_run_progression"
	CodeSelectedLaunchNow         Code = "selected_launch_now"
	CodeSelectedAutomatic         Code = "selected_automatic"
	CodeProjectInactive           Code = "project_inactive"
	CodeLeaseLost                 Code = "project_lease_lost"
	CodeFactsStale                Code = "required_facts_stale"
	CodeFactsAmbiguous            Code = "required_facts_ambiguous"
	CodeDiskUnavailable           Code = "disk_fact_unavailable"
	CodeDiskLimit                 Code = "disk_limit_exceeded"
	CodeProviderUnavailable       Code = "provider_unavailable"
	CodeProviderNotAdmitted       Code = "provider_not_admitted"
	CodeTaskIncomplete            Code = "task_incomplete"
	CodeOrganizerUnapproved       Code = "organizer_revision_unapproved"
	CodePreflightNotReady         Code = "preflight_not_ready"
	CodeManualPolicyWait          Code = "manual_policy_wait"
	CodeDependencyWait            Code = "dependency_wait"
	CodeDuplicateActiveRun        Code = "duplicate_active_run"
	CodeTimeBudgetUnavailable     Code = "time_budget_unavailable"
	CodeCostBudgetUnavailable     Code = "cost_budget_unavailable"
	CodeCIBudgetUnavailable       Code = "ci_budget_unavailable"
	CodeTimeSoftBudget            Code = "time_soft_budget_acknowledgement_required"
	CodeCostSoftBudget            Code = "cost_soft_budget_acknowledgement_required"
	CodeTimeHardBudget            Code = "time_hard_budget_exhausted"
	CodeCostHardBudget            Code = "cost_hard_budget_exhausted"
	CodeCIHardBudget              Code = "ci_hard_budget_exhausted"
	CodeProjectCapacity           Code = "project_task_capacity_exhausted"
	CodeWorkspaceCapacity         Code = "workspace_task_capacity_exhausted"
	CodeAgentCapacity             Code = "agent_capacity_exhausted"
	CodeHelperCapacity            Code = "helper_capacity_exhausted"
)

// Codes returns a new slice containing every possible result code.
func Codes() []Code {
	return []Code{
		CodeSelectedActiveProgression,
		CodeSelectedLaunchNow,
		CodeSelectedAutomatic,
		CodeProjectInactive,
		CodeLeaseLost,
		CodeFactsStale,
		CodeFactsAmbiguous,
		CodeDiskUnavailable,
		CodeDiskLimit,
		CodeProviderUnavailable,
		CodeProviderNotAdmitted,
		CodeTaskIncomplete,
		CodeOrganizerUnapproved,
		CodePreflightNotReady,
		CodeManualPolicyWait,
		CodeDependencyWait,
		CodeDuplicateActiveRun,
		CodeTimeBudgetUnavailable,
		CodeCostBudgetUnavailable,
		CodeCIBudgetUnavailable,
		CodeTimeSoftBudget,
		CodeCostSoftBudget,
		CodeTimeHardBudget,
		CodeCostHardBudget,
		CodeCIHardBudget,
		CodeProjectCapacity,
		CodeWorkspaceCapacity,
		CodeAgentCapacity,
		CodeHelperCapacity,
	}
}

// Explanation records exactly one closed result for a Task.
type Explanation struct {
	TaskID TaskID `json:"taskId"`
	Code   Code   `json:"code"`
}

// Result is the complete deterministic oracle output. OrderedTaskIDs is in
// dispatch-request order; Explanations is in stable Task ID order.
type Result struct {
	OrderedTaskIDs []TaskID      `json:"orderedTaskIds"`
	Explanations   []Explanation `json:"explanations"`
}

type guard uint8

const (
	guardOrderProgression guard = iota
	guardOrderLaunchNow
	guardOrderPriority
	guardOrderFIFO
	guardOrderStableID
	guardProjectCapacity
	guardWorkspaceCapacity
	guardAgentCapacity
	guardHelperCapacity
	guardCapacityReservations
	guardBudgetReservations
	guardNoDuplicateActiveRun
	guardCount
)

type guardSet [guardCount]bool

func allGuards() guardSet {
	var guards guardSet
	for index := range guards {
		guards[index] = true
	}
	return guards
}

// Evaluate applies the complete independent reference model.
func Evaluate(snapshot Snapshot) Result {
	return evaluateWithGuards(snapshot, allGuards())
}

func evaluateWithGuards(snapshot Snapshot, guards guardSet) Result {
	tasks := append([]TaskFacts(nil), snapshot.tasks...)
	slices.SortStableFunc(tasks, func(left, right TaskFacts) int {
		return compareTasks(left, right, guards)
	})

	explanations := make([]Explanation, 0, len(tasks))
	ordered := make([]TaskID, 0, len(tasks))
	globalCode := validateGlobal(snapshot)
	workspaceUse, workspaceAmbiguous := workspaceUsage(snapshot.workspaceUsages, guards)
	if globalCode == "" && workspaceAmbiguous {
		globalCode = CodeFactsAmbiguous
	}
	if globalCode == "" && duplicateTaskID(tasks) {
		globalCode = CodeFactsAmbiguous
	}

	activeTasks, tasksOK := sum(snapshot.usage.activeTasks, reservation(snapshot.usage.reservedTasks, guards, guardCapacityReservations))
	activeAgents, agentsOK := sum(snapshot.usage.activeAgents, reservation(snapshot.usage.reservedAgents, guards, guardCapacityReservations))
	if globalCode == "" && (!tasksOK || !agentsOK) {
		globalCode = CodeFactsAmbiguous
	}

	for _, task := range tasks {
		code := globalCode
		if code == "" {
			code = validateTask(task, snapshot.policy, guards)
		}
		if code == "" {
			code = admitCapacity(task, snapshot.limits, activeTasks, activeAgents, workspaceUse, guards)
		}
		if code == "" {
			code = selectedCode(task)
			ordered = append(ordered, task.id)
			activeTasks, _ = sum(activeTasks, task.demand.projectTasks)
			activeAgents, _ = sum(activeAgents, task.demand.agents)
			workspaceUse[task.workspace], _ = sum(workspaceUse[task.workspace], task.demand.workspaceTasks)
		}
		explanations = append(explanations, Explanation{TaskID: task.id, Code: code})
	}

	slices.SortFunc(explanations, func(left, right Explanation) int {
		if left.TaskID < right.TaskID {
			return -1
		}
		if left.TaskID > right.TaskID {
			return 1
		}
		return 0
	})
	return Result{OrderedTaskIDs: ordered, Explanations: explanations}
}

func compareTasks(left, right TaskFacts, guards guardSet) int {
	if guards[guardOrderProgression] && left.workClass != right.workClass {
		if left.workClass == WorkActiveRunProgression {
			return -1
		}
		return 1
	}
	if guards[guardOrderLaunchNow] && left.launchNow != right.launchNow {
		if left.launchNow {
			return -1
		}
		return 1
	}
	if guards[guardOrderPriority] && left.priority != right.priority {
		if left.priority < right.priority {
			return -1
		}
		return 1
	}
	if guards[guardOrderFIFO] && left.queuedAt != right.queuedAt {
		if left.queuedAt < right.queuedAt {
			return -1
		}
		return 1
	}
	if guards[guardOrderStableID] {
		if left.id < right.id {
			return -1
		}
		if left.id > right.id {
			return 1
		}
	}
	return 0
}

func validateGlobal(snapshot Snapshot) Code {
	if !snapshot.policy.valid() || !snapshot.limits.valid() || !snapshot.lease.valid() ||
		!snapshot.disk.valid() || !snapshot.provider.valid() {
		return CodeFactsAmbiguous
	}
	if !snapshot.projectActive {
		return CodeProjectInactive
	}
	switch snapshot.lease {
	case LeaseLost:
		return CodeLeaseLost
	case LeaseStale:
		return CodeFactsStale
	case LeaseAmbiguous:
		return CodeFactsAmbiguous
	}
	switch snapshot.disk {
	case DiskUnavailable:
		return CodeDiskUnavailable
	case DiskLimitExceeded:
		return CodeDiskLimit
	case DiskStale:
		return CodeFactsStale
	case DiskAmbiguous:
		return CodeFactsAmbiguous
	}
	switch snapshot.provider {
	case ProviderUnavailable:
		return CodeProviderUnavailable
	case ProviderNotAdmitted:
		return CodeProviderNotAdmitted
	case ProviderStale:
		return CodeFactsStale
	case ProviderAmbiguous:
		return CodeFactsAmbiguous
	}
	return ""
}

func validateTask(task TaskFacts, projectPolicy LaunchPolicy, guards guardSet) Code {
	if task.id == "" || task.workspace == "" || !task.priority.valid() ||
		!task.policyOverride.valid() || !task.workClass.valid() || !task.dependency.valid() ||
		!validDemand(task) {
		return CodeFactsAmbiguous
	}
	if task.workClass == WorkActiveRunProgression {
		if !task.hasActiveRun {
			return CodeFactsAmbiguous
		}
	} else {
		if guards[guardNoDuplicateActiveRun] && task.hasActiveRun {
			return CodeDuplicateActiveRun
		}
		if !task.taskComplete {
			return CodeTaskIncomplete
		}
		if !task.organizerApproved {
			return CodeOrganizerUnapproved
		}
		if !task.preflightReady {
			return CodePreflightNotReady
		}
		if task.dependency == DependenciesWaiting {
			return CodeDependencyWait
		}
		if !task.launchNow && effectivePolicy(projectPolicy, task.policyOverride) == PolicyManual {
			return CodeManualPolicyWait
		}
	}
	if code := evaluateConsumptiveBudget(task.budgets.time, CodeTimeBudgetUnavailable, CodeTimeSoftBudget, CodeTimeHardBudget, guards); code != "" {
		return code
	}
	if code := evaluateConsumptiveBudget(task.budgets.cost, CodeCostBudgetUnavailable, CodeCostSoftBudget, CodeCostHardBudget, guards); code != "" {
		return code
	}
	return evaluateHardBudget(task.budgets.ci, CodeCIBudgetUnavailable, CodeCIHardBudget, guards)
}

func validDemand(task TaskFacts) bool {
	if task.workClass == WorkNewRun {
		return task.demand.projectTasks == 1 && task.demand.workspaceTasks == 1 &&
			task.demand.agents == 1 && task.demand.helpers == 0
	}
	return task.demand.projectTasks == 0 && task.demand.workspaceTasks == 0 &&
		task.demand.helpers <= task.demand.agents
}

func effectivePolicy(project LaunchPolicy, override PolicyOverride) LaunchPolicy {
	switch override {
	case PolicyForceManual:
		return PolicyManual
	case PolicyForceAutomatic:
		return PolicyAutomatic
	default:
		return project
	}
}

func selectedCode(task TaskFacts) Code {
	if task.workClass == WorkActiveRunProgression {
		return CodeSelectedActiveProgression
	}
	if task.launchNow {
		return CodeSelectedLaunchNow
	}
	return CodeSelectedAutomatic
}

func evaluateConsumptiveBudget(budget Budget, unavailableCode, softCode, hardCode Code, guards guardSet) Code {
	if code := budgetObservationCode(budget, unavailableCode); code != "" {
		return code
	}
	projected, valid := projectedBudget(budget, guards)
	if !valid || !budget.softAcknowledgement.valid() {
		return CodeFactsAmbiguous
	}
	if projected >= budget.limit {
		return hardCode
	}
	if ratioAtLeast(projected, budget.limit, 85, 100) {
		switch budget.softAcknowledgement {
		case AcknowledgementCurrent:
			return ""
		case AcknowledgementStale:
			return CodeFactsStale
		case AcknowledgementAmbiguous:
			return CodeFactsAmbiguous
		default:
			return softCode
		}
	}
	return ""
}

func evaluateHardBudget(budget Budget, unavailableCode, hardCode Code, guards guardSet) Code {
	if code := budgetObservationCode(budget, unavailableCode); code != "" {
		return code
	}
	projected, valid := projectedBudget(budget, guards)
	if !valid {
		return CodeFactsAmbiguous
	}
	if projected >= budget.limit {
		return hardCode
	}
	return ""
}

func budgetObservationCode(budget Budget, unavailableCode Code) Code {
	if !budget.state.valid() {
		return CodeFactsAmbiguous
	}
	switch budget.state {
	case BudgetUnavailable:
		return unavailableCode
	case BudgetStale:
		return CodeFactsStale
	case BudgetAmbiguous:
		return CodeFactsAmbiguous
	default:
		return ""
	}
}

func projectedBudget(budget Budget, guards guardSet) (uint64, bool) {
	if budget.limit == 0 {
		return 0, false
	}
	reserved := reservation(budget.reserved, guards, guardBudgetReservations)
	current, ok := sum(budget.used, reserved)
	if !ok {
		return 0, false
	}
	return sum(current, budget.requested)
}

func ratioAtLeast(value, limit, numerator, denominator uint64) bool {
	valueHigh, valueLow := bits.Mul64(value, denominator)
	limitHigh, limitLow := bits.Mul64(limit, numerator)
	if valueHigh != limitHigh {
		return valueHigh > limitHigh
	}
	return valueLow >= limitLow
}

func admitCapacity(task TaskFacts, limits Limits, activeTasks, activeAgents uint64, workspaceUse map[WorkspaceID]uint64, guards guardSet) Code {
	if guards[guardProjectCapacity] && exceeds(activeTasks, task.demand.projectTasks, limits.maxActiveTasks) {
		return CodeProjectCapacity
	}
	if guards[guardWorkspaceCapacity] && exceeds(workspaceUse[task.workspace], task.demand.workspaceTasks, limits.maxActiveTasksPerWorkspace) {
		return CodeWorkspaceCapacity
	}
	if guards[guardAgentCapacity] && exceeds(activeAgents, task.demand.agents, limits.maxConcurrentAgents) {
		return CodeAgentCapacity
	}
	helperUse, ok := sum(task.helpersActive, reservation(task.helpersReserved, guards, guardCapacityReservations))
	if !ok {
		return CodeFactsAmbiguous
	}
	if guards[guardHelperCapacity] && exceeds(helperUse, task.demand.helpers, limits.maxHelpersPerTask) {
		return CodeHelperCapacity
	}
	return ""
}

func workspaceUsage(usages []WorkspaceUsage, guards guardSet) (map[WorkspaceID]uint64, bool) {
	result := make(map[WorkspaceID]uint64, len(usages))
	for _, usage := range usages {
		if usage.workspace == "" {
			return nil, true
		}
		if _, exists := result[usage.workspace]; exists {
			return nil, true
		}
		value, ok := sum(usage.activeTasks, reservation(usage.reservedTasks, guards, guardCapacityReservations))
		if !ok {
			return nil, true
		}
		result[usage.workspace] = value
	}
	return result, false
}

func duplicateTaskID(tasks []TaskFacts) bool {
	seen := make(map[TaskID]struct{}, len(tasks))
	for _, task := range tasks {
		if _, exists := seen[task.id]; exists {
			return true
		}
		seen[task.id] = struct{}{}
	}
	return false
}

func reservation(value uint64, guards guardSet, reservationGuard guard) uint64 {
	if guards[reservationGuard] {
		return value
	}
	return 0
}

func exceeds(used, demand, limit uint64) bool {
	total, ok := sum(used, demand)
	return !ok || total > limit
}

func sum(left, right uint64) (uint64, bool) {
	value := left + right
	return value, value >= left
}
