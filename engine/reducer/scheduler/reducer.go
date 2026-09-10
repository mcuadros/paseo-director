// SPDX-License-Identifier: Apache-2.0

// Package scheduler is the exclusive home of deterministic M2 scheduling,
// capacity, lease, and launch-budget policy. It is a pure reducer: adapters
// may persist its reservations but cannot reproduce or modify its decisions.
package scheduler

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"math/bits"
	"slices"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/mcuadros/director-engine/domain/scheduling"
)

const SchemaVersion = "director.reducer.scheduler/v1"

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

func Codes() []Code {
	return []Code{
		CodeSelectedActiveProgression, CodeSelectedLaunchNow, CodeSelectedAutomatic,
		CodeProjectInactive, CodeLeaseLost, CodeFactsStale, CodeFactsAmbiguous,
		CodeDiskUnavailable, CodeDiskLimit, CodeProviderUnavailable, CodeProviderNotAdmitted,
		CodeTaskIncomplete, CodeOrganizerUnapproved, CodePreflightNotReady,
		CodeManualPolicyWait, CodeDependencyWait, CodeDuplicateActiveRun,
		CodeTimeBudgetUnavailable, CodeCostBudgetUnavailable, CodeCIBudgetUnavailable,
		CodeTimeSoftBudget, CodeCostSoftBudget, CodeTimeHardBudget, CodeCostHardBudget,
		CodeCIHardBudget, CodeProjectCapacity, CodeWorkspaceCapacity,
		CodeAgentCapacity, CodeHelperCapacity,
	}
}

type Explanation struct {
	TaskID scheduling.TaskID `json:"taskId"`
	Code   Code              `json:"code"`
}

type Result struct {
	SchemaVersion  string              `json:"schemaVersion"`
	FactsHash      string              `json:"factsHash"`
	OrderedTaskIDs []scheduling.TaskID `json:"orderedTaskIds"`
	Explanations   []Explanation       `json:"explanations"`
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

func digest(value any) string {
	encoded, err := json.Marshal(value)
	if err != nil {
		panic("marshal closed scheduler value: " + err.Error())
	}
	sum := sha256.Sum256(encoded)
	return hex.EncodeToString(sum[:])
}

// Reduce orders every work item once, applies each refusal in a fixed order,
// and accounts for earlier selections before considering later Tasks.
func Reduce(snapshot scheduling.Snapshot) Result {
	return reduceWithGuards(snapshot, allGuards())
}

func reduceWithGuards(snapshot scheduling.Snapshot, guards guardSet) Result {
	factsHash := digest(canonicalSnapshot(snapshot))
	tasks := append([]scheduling.TaskFacts(nil), snapshot.Tasks...)
	slices.SortStableFunc(tasks, func(left, right scheduling.TaskFacts) int {
		return compareTasks(left, right, guards)
	})

	explanations := make([]Explanation, 0, len(tasks))
	ordered := make([]scheduling.TaskID, 0, len(tasks))
	globalCode := validateGlobal(snapshot)
	workspaceUse, workspaceAmbiguous := workspaceUsage(snapshot.WorkspaceUsages, guards)
	if globalCode == "" && workspaceAmbiguous {
		globalCode = CodeFactsAmbiguous
	}
	if globalCode == "" && duplicateTaskID(tasks) {
		globalCode = CodeFactsAmbiguous
	}
	activeTasks, tasksOK := sum(snapshot.Usage.ActiveTasks, reservation(snapshot.Usage.ReservedTasks, guards, guardCapacityReservations))
	activeAgents, agentsOK := sum(snapshot.Usage.ActiveAgents, reservation(snapshot.Usage.ReservedAgents, guards, guardCapacityReservations))
	if globalCode == "" && (!tasksOK || !agentsOK) {
		globalCode = CodeFactsAmbiguous
	}

	for _, task := range tasks {
		code := globalCode
		if code == "" {
			code = validateTask(task, snapshot.Policy, guards)
		}
		if code == "" {
			code = admitCapacity(task, snapshot.Limits, activeTasks, activeAgents, workspaceUse, guards)
		}
		if code == "" {
			code = selectedCode(task)
			ordered = append(ordered, task.ID)
			activeTasks, _ = sum(activeTasks, task.Demand.ProjectTasks)
			activeAgents, _ = sum(activeAgents, task.Demand.Agents)
			workspaceUse[task.Workspace], _ = sum(workspaceUse[task.Workspace], task.Demand.WorkspaceTasks)
		}
		explanations = append(explanations, Explanation{TaskID: task.ID, Code: code})
	}
	slices.SortFunc(explanations, func(left, right Explanation) int {
		return strings.Compare(string(left.TaskID), string(right.TaskID))
	})
	return Result{
		SchemaVersion: SchemaVersion, FactsHash: factsHash,
		OrderedTaskIDs: ordered, Explanations: explanations,
	}
}

func canonicalSnapshot(snapshot scheduling.Snapshot) scheduling.Snapshot {
	result := scheduling.CloneSnapshot(snapshot)
	slices.SortFunc(result.WorkspaceUsages, func(left, right scheduling.WorkspaceUsage) int {
		if comparison := strings.Compare(string(left.Workspace), string(right.Workspace)); comparison != 0 {
			return comparison
		}
		return strings.Compare(digest(left), digest(right))
	})
	slices.SortFunc(result.Tasks, func(left, right scheduling.TaskFacts) int {
		if comparison := strings.Compare(string(left.ID), string(right.ID)); comparison != 0 {
			return comparison
		}
		return strings.Compare(digest(left), digest(right))
	})
	return result
}

func compareTasks(left, right scheduling.TaskFacts, guards guardSet) int {
	if guards[guardOrderProgression] && left.WorkClass != right.WorkClass {
		if left.WorkClass == scheduling.WorkActiveRunProgression {
			return -1
		}
		return 1
	}
	if guards[guardOrderLaunchNow] && left.LaunchNow.Requested != right.LaunchNow.Requested {
		if left.LaunchNow.Requested {
			return -1
		}
		return 1
	}
	if guards[guardOrderPriority] && left.Priority != right.Priority {
		if priorityRank(left.Priority) < priorityRank(right.Priority) {
			return -1
		}
		return 1
	}
	if guards[guardOrderFIFO] && left.QueuedAtUnixMillis != right.QueuedAtUnixMillis {
		if left.QueuedAtUnixMillis < right.QueuedAtUnixMillis {
			return -1
		}
		return 1
	}
	if guards[guardOrderStableID] {
		return strings.Compare(string(left.ID), string(right.ID))
	}
	return 0
}

func priorityRank(priority scheduling.Priority) uint8 {
	switch priority {
	case scheduling.PriorityUrgent:
		return 1
	case scheduling.PriorityHigh:
		return 2
	case scheduling.PriorityNormal:
		return 3
	case scheduling.PriorityLow:
		return 4
	default:
		return 255
	}
}

func validateGlobal(snapshot scheduling.Snapshot) Code {
	if snapshot.SchemaVersion != scheduling.SchemaVersion || !boundedIdentity(snapshot.ProjectID, 128) ||
		!validPolicy(snapshot.Policy) || !validLimits(snapshot.Limits) || !validLease(snapshot.Lease) ||
		!validDisk(snapshot.Disk) || !validProvider(snapshot.Provider) || snapshot.LeaseEpoch == 0 {
		return CodeFactsAmbiguous
	}
	if !snapshot.ProjectActive {
		return CodeProjectInactive
	}
	switch snapshot.Lease {
	case scheduling.LeaseLost:
		return CodeLeaseLost
	case scheduling.LeaseStale:
		return CodeFactsStale
	case scheduling.LeaseAmbiguous:
		return CodeFactsAmbiguous
	}
	switch snapshot.Disk {
	case scheduling.DiskUnavailable:
		return CodeDiskUnavailable
	case scheduling.DiskLimitExceeded:
		return CodeDiskLimit
	case scheduling.DiskStale:
		return CodeFactsStale
	case scheduling.DiskAmbiguous:
		return CodeFactsAmbiguous
	}
	switch snapshot.Provider {
	case scheduling.ProviderUnavailable:
		return CodeProviderUnavailable
	case scheduling.ProviderNotAdmitted:
		return CodeProviderNotAdmitted
	case scheduling.ProviderStale:
		return CodeFactsStale
	case scheduling.ProviderAmbiguous:
		return CodeFactsAmbiguous
	}
	return ""
}

func validateTask(task scheduling.TaskFacts, projectPolicy scheduling.LaunchPolicy, guards guardSet) Code {
	if !boundedIdentity(string(task.ID), 128) || !boundedIdentity(string(task.Workspace), 128) ||
		task.TaskVersion == 0 || priorityRank(task.Priority) == 255 || !validOverride(task.PolicyOverride) ||
		!validWorkClass(task.WorkClass) || !validDependency(task.Dependency) || !validDemand(task) ||
		!validLaunchNow(task) || !validDependencyAudit(task) {
		return CodeFactsAmbiguous
	}
	if task.WorkClass == scheduling.WorkActiveRunProgression {
		if !task.HasActiveRun {
			return CodeFactsAmbiguous
		}
	} else {
		if guards[guardNoDuplicateActiveRun] && task.HasActiveRun {
			return CodeDuplicateActiveRun
		}
		if !task.TaskComplete {
			return CodeTaskIncomplete
		}
		if !task.OrganizerApproved {
			return CodeOrganizerUnapproved
		}
		if !task.PreflightReady {
			return CodePreflightNotReady
		}
		if task.Dependency == scheduling.DependenciesWaiting {
			return CodeDependencyWait
		}
		if !task.LaunchNow.Requested && effectivePolicy(projectPolicy, task.PolicyOverride) == scheduling.PolicyManual {
			return CodeManualPolicyWait
		}
	}
	if code := evaluateConsumptiveBudget(task.Budgets.Time, CodeTimeBudgetUnavailable, CodeTimeSoftBudget, CodeTimeHardBudget, guards); code != "" {
		return code
	}
	if code := evaluateConsumptiveBudget(task.Budgets.Cost, CodeCostBudgetUnavailable, CodeCostSoftBudget, CodeCostHardBudget, guards); code != "" {
		return code
	}
	return evaluateHardBudget(task.Budgets.CI, CodeCIBudgetUnavailable, CodeCIHardBudget, guards)
}

func boundedIdentity(value string, maximum int) bool {
	return len(value) > 0 && len(value) <= maximum && utf8.ValidString(value) &&
		value == strings.TrimSpace(value) && strings.IndexFunc(value, unicode.IsControl) < 0
}

func validPolicy(value scheduling.LaunchPolicy) bool {
	return value == scheduling.PolicyManual || value == scheduling.PolicyAutomatic
}

func validOverride(value scheduling.PolicyOverride) bool {
	return value == scheduling.PolicyInherit || value == scheduling.PolicyForceManual || value == scheduling.PolicyForceAutomatic
}

func validWorkClass(value scheduling.WorkClass) bool {
	return value == scheduling.WorkNewRun || value == scheduling.WorkActiveRunProgression
}

func validDependency(value scheduling.DependencyState) bool {
	return value == scheduling.DependenciesSatisfied || value == scheduling.DependenciesWaiting || value == scheduling.DependenciesAuditedOverride
}

func validLease(value scheduling.LeaseState) bool {
	return value == scheduling.LeaseCurrent || value == scheduling.LeaseLost || value == scheduling.LeaseStale || value == scheduling.LeaseAmbiguous
}

func validDisk(value scheduling.DiskState) bool {
	return value == scheduling.DiskReady || value == scheduling.DiskUnavailable || value == scheduling.DiskLimitExceeded ||
		value == scheduling.DiskStale || value == scheduling.DiskAmbiguous
}

func validProvider(value scheduling.ProviderState) bool {
	return value == scheduling.ProviderReady || value == scheduling.ProviderUnavailable || value == scheduling.ProviderNotAdmitted ||
		value == scheduling.ProviderStale || value == scheduling.ProviderAmbiguous
}

func validLimits(limits scheduling.Limits) bool {
	return limits.MaxActiveTasks > 0 && limits.MaxActiveTasksPerWorkspace > 0 && limits.MaxConcurrentAgents > 0 &&
		limits.MaxActiveTasksPerWorkspace <= limits.MaxActiveTasks && limits.MaxActiveTasks <= limits.MaxConcurrentAgents
}

func validDemand(task scheduling.TaskFacts) bool {
	if task.WorkClass == scheduling.WorkNewRun {
		return task.Demand.ProjectTasks == 1 && task.Demand.WorkspaceTasks == 1 && task.Demand.Agents == 1 && task.Demand.Helpers == 0
	}
	return task.Demand.ProjectTasks == 0 && task.Demand.WorkspaceTasks == 0 && task.Demand.Helpers <= task.Demand.Agents
}

func validLaunchNow(task scheduling.TaskFacts) bool {
	request := task.LaunchNow
	if !request.Requested {
		return request.ActorKind == "" && request.ActorID == "" && request.AuditID == "" && request.TaskVersion == 0
	}
	return request.ActorKind == "human" && boundedIdentity(request.ActorID, 256) && boundedIdentity(request.AuditID, 128) &&
		request.TaskVersion == task.TaskVersion
}

func validDependencyAudit(task scheduling.TaskFacts) bool {
	audits := task.DependencyOverrideAuditIDs
	if task.Dependency != scheduling.DependenciesAuditedOverride {
		return len(audits) == 0 && task.DependencyOverrideVersion == 0
	}
	if task.DependencyOverrideVersion != task.TaskVersion || len(audits) == 0 {
		return false
	}
	for index, auditID := range audits {
		if !boundedIdentity(auditID, 128) || (index > 0 && audits[index-1] >= auditID) {
			return false
		}
	}
	return true
}

func effectivePolicy(project scheduling.LaunchPolicy, override scheduling.PolicyOverride) scheduling.LaunchPolicy {
	switch override {
	case scheduling.PolicyForceManual:
		return scheduling.PolicyManual
	case scheduling.PolicyForceAutomatic:
		return scheduling.PolicyAutomatic
	default:
		return project
	}
}

func selectedCode(task scheduling.TaskFacts) Code {
	if task.WorkClass == scheduling.WorkActiveRunProgression {
		return CodeSelectedActiveProgression
	}
	if task.LaunchNow.Requested {
		return CodeSelectedLaunchNow
	}
	return CodeSelectedAutomatic
}

func budgetObservationCode(budget scheduling.Budget, unavailable Code) Code {
	switch budget.State {
	case scheduling.BudgetReady:
		return ""
	case scheduling.BudgetUnavailable:
		return unavailable
	case scheduling.BudgetStale:
		return CodeFactsStale
	case scheduling.BudgetAmbiguous:
		return CodeFactsAmbiguous
	default:
		return CodeFactsAmbiguous
	}
}

func validAcknowledgement(budget scheduling.Budget) bool {
	if !boundedIdentity(budget.Revision, 128) {
		return false
	}
	switch budget.Acknowledgement {
	case scheduling.AcknowledgementNone:
		return budget.AcknowledgedRevision == ""
	case scheduling.AcknowledgementCurrent:
		return budget.AcknowledgedRevision == budget.Revision
	case scheduling.AcknowledgementStale:
		return boundedIdentity(budget.AcknowledgedRevision, 128) && budget.AcknowledgedRevision != budget.Revision
	case scheduling.AcknowledgementAmbiguous:
		return true
	default:
		return false
	}
}

func evaluateConsumptiveBudget(budget scheduling.Budget, unavailable, soft, hard Code, guards guardSet) Code {
	if code := budgetObservationCode(budget, unavailable); code != "" {
		return code
	}
	projected, valid := projectedBudget(budget, guards)
	if !valid || !validAcknowledgement(budget) {
		return CodeFactsAmbiguous
	}
	if projected >= budget.Limit {
		return hard
	}
	if ratioAtLeast(projected, budget.Limit, 85, 100) {
		switch budget.Acknowledgement {
		case scheduling.AcknowledgementCurrent:
			return ""
		case scheduling.AcknowledgementStale:
			return CodeFactsStale
		case scheduling.AcknowledgementAmbiguous:
			return CodeFactsAmbiguous
		default:
			return soft
		}
	}
	return ""
}

func evaluateHardBudget(budget scheduling.Budget, unavailable, hard Code, guards guardSet) Code {
	if code := budgetObservationCode(budget, unavailable); code != "" {
		return code
	}
	projected, valid := projectedBudget(budget, guards)
	if !valid || !boundedIdentity(budget.Revision, 128) {
		return CodeFactsAmbiguous
	}
	if projected >= budget.Limit {
		return hard
	}
	return ""
}

func projectedBudget(budget scheduling.Budget, guards guardSet) (uint64, bool) {
	if budget.Limit == 0 {
		return 0, false
	}
	current, ok := sum(budget.Used, reservation(budget.Reserved, guards, guardBudgetReservations))
	if !ok {
		return 0, false
	}
	return sum(current, budget.Requested)
}

func ratioAtLeast(value, limit, numerator, denominator uint64) bool {
	valueHigh, valueLow := bits.Mul64(value, denominator)
	limitHigh, limitLow := bits.Mul64(limit, numerator)
	if valueHigh != limitHigh {
		return valueHigh > limitHigh
	}
	return valueLow >= limitLow
}

func admitCapacity(task scheduling.TaskFacts, limits scheduling.Limits, activeTasks, activeAgents uint64, workspaceUse map[scheduling.WorkspaceID]uint64, guards guardSet) Code {
	if guards[guardProjectCapacity] && exceeds(activeTasks, task.Demand.ProjectTasks, limits.MaxActiveTasks) {
		return CodeProjectCapacity
	}
	if guards[guardWorkspaceCapacity] && exceeds(workspaceUse[task.Workspace], task.Demand.WorkspaceTasks, limits.MaxActiveTasksPerWorkspace) {
		return CodeWorkspaceCapacity
	}
	if guards[guardAgentCapacity] && exceeds(activeAgents, task.Demand.Agents, limits.MaxConcurrentAgents) {
		return CodeAgentCapacity
	}
	helperUse, ok := sum(task.HelpersActive, reservation(task.HelpersReserved, guards, guardCapacityReservations))
	if !ok {
		return CodeFactsAmbiguous
	}
	if guards[guardHelperCapacity] && exceeds(helperUse, task.Demand.Helpers, limits.MaxHelpersPerTask) {
		return CodeHelperCapacity
	}
	return ""
}

func workspaceUsage(usages []scheduling.WorkspaceUsage, guards guardSet) (map[scheduling.WorkspaceID]uint64, bool) {
	result := make(map[scheduling.WorkspaceID]uint64, len(usages))
	for _, usage := range usages {
		if !boundedIdentity(string(usage.Workspace), 128) {
			return nil, true
		}
		if _, duplicate := result[usage.Workspace]; duplicate {
			return nil, true
		}
		value, ok := sum(usage.ActiveTasks, reservation(usage.ReservedTasks, guards, guardCapacityReservations))
		if !ok {
			return nil, true
		}
		result[usage.Workspace] = value
	}
	return result, false
}

func duplicateTaskID(tasks []scheduling.TaskFacts) bool {
	seen := make(map[scheduling.TaskID]struct{}, len(tasks))
	for _, task := range tasks {
		if _, duplicate := seen[task.ID]; duplicate {
			return true
		}
		seen[task.ID] = struct{}{}
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
