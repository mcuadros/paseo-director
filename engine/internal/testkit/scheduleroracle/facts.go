// SPDX-License-Identifier: Apache-2.0

package scheduleroracle

// TaskID is an opaque stable Task identifier.
type TaskID string

// WorkspaceID is an opaque canonical Workspace identifier.
type WorkspaceID string

// Priority is the PLAN's closed scheduler priority vocabulary.
type Priority uint8

const (
	PriorityUrgent Priority = iota + 1
	PriorityHigh
	PriorityNormal
	PriorityLow
)

// Priorities returns the priority values in scheduler order.
func Priorities() []Priority {
	return []Priority{PriorityUrgent, PriorityHigh, PriorityNormal, PriorityLow}
}

func (priority Priority) valid() bool {
	return priority >= PriorityUrgent && priority <= PriorityLow
}

// LaunchPolicy is the Project's frozen launch policy.
type LaunchPolicy uint8

const (
	PolicyManual LaunchPolicy = iota + 1
	PolicyAutomatic
)

func (policy LaunchPolicy) valid() bool {
	return policy == PolicyManual || policy == PolicyAutomatic
}

// PolicyOverride is the Task's frozen launch-policy override.
type PolicyOverride uint8

const (
	PolicyInherit PolicyOverride = iota + 1
	PolicyForceManual
	PolicyForceAutomatic
)

func (override PolicyOverride) valid() bool {
	return override >= PolicyInherit && override <= PolicyForceAutomatic
}

// WorkClass distinguishes a new Run launch from progression of an existing
// active Run. The distinction prevents a new Run from duplicating an active
// one while preserving PLAN's progression preference.
type WorkClass uint8

const (
	WorkNewRun WorkClass = iota + 1
	WorkActiveRunProgression
)

func (class WorkClass) valid() bool {
	return class == WorkNewRun || class == WorkActiveRunProgression
}

// DependencyState is the complete dependency admission vocabulary. An
// override is represented only by the audited state; a bare bypass boolean is
// intentionally impossible.
type DependencyState uint8

const (
	DependenciesSatisfied DependencyState = iota + 1
	DependenciesWaiting
	DependenciesAuditedOverride
)

func (state DependencyState) valid() bool {
	return state >= DependenciesSatisfied && state <= DependenciesAuditedOverride
}

// LeaseState captures the closed Project execution-lease observations used by
// scheduler admission.
type LeaseState uint8

const (
	LeaseCurrent LeaseState = iota + 1
	LeaseLost
	LeaseStale
	LeaseAmbiguous
)

func (state LeaseState) valid() bool {
	return state >= LeaseCurrent && state <= LeaseAmbiguous
}

// DiskState captures the closed preflight disk observations.
type DiskState uint8

const (
	DiskReady DiskState = iota + 1
	DiskUnavailable
	DiskLimitExceeded
	DiskStale
	DiskAmbiguous
)

func (state DiskState) valid() bool {
	return state >= DiskReady && state <= DiskAmbiguous
}

// ProviderState captures the closed provider admission observations.
type ProviderState uint8

const (
	ProviderReady ProviderState = iota + 1
	ProviderUnavailable
	ProviderNotAdmitted
	ProviderStale
	ProviderAmbiguous
)

func (state ProviderState) valid() bool {
	return state >= ProviderReady && state <= ProviderAmbiguous
}

// AcknowledgementState binds the 85 percent soft-budget acknowledgement to
// the frozen budget revision represented by the fact set.
type AcknowledgementState uint8

const (
	AcknowledgementNone AcknowledgementState = iota + 1
	AcknowledgementCurrent
	AcknowledgementStale
	AcknowledgementAmbiguous
)

func (state AcknowledgementState) valid() bool {
	return state >= AcknowledgementNone && state <= AcknowledgementAmbiguous
}

// BudgetState captures whether the durable ledger/provider facts for one
// budget dimension are current enough to use.
type BudgetState uint8

const (
	BudgetReady BudgetState = iota + 1
	BudgetUnavailable
	BudgetStale
	BudgetAmbiguous
)

func (state BudgetState) valid() bool {
	return state >= BudgetReady && state <= BudgetAmbiguous
}

// Budget is an immutable finite budget observation. Used and Reserved are
// already durable; Requested is the reservation needed by this scheduler
// action. SoftAcknowledgement is used for consumptive time/cost budgets and is
// ignored for CI, whose configured count is a hard budget.
type Budget struct {
	state               BudgetState
	limit               uint64
	used                uint64
	reserved            uint64
	requested           uint64
	softAcknowledgement AcknowledgementState
}

// NewBudget freezes one finite budget observation.
func NewBudget(limit, used, reserved, requested uint64, acknowledgement AcknowledgementState) Budget {
	return Budget{
		state:               BudgetReady,
		limit:               limit,
		used:                used,
		reserved:            reserved,
		requested:           requested,
		softAcknowledgement: acknowledgement,
	}
}

// WithState returns a copy with the ledger/provider observation state for this
// budget dimension.
func (budget Budget) WithState(state BudgetState) Budget {
	budget.state = state
	return budget
}

// Budgets is the closed scheduler budget set.
type Budgets struct {
	time Budget
	cost Budget
	ci   Budget
}

// NewBudgets freezes time, cost, and CI budget facts.
func NewBudgets(time, cost, ci Budget) Budgets {
	return Budgets{time: time, cost: cost, ci: ci}
}

// AdmittedBudgets returns a small deterministic fact fixture below every soft
// and hard threshold.
func AdmittedBudgets() Budgets {
	return NewBudgets(
		NewBudget(100, 0, 0, 1, AcknowledgementNone),
		NewBudget(100, 0, 0, 1, AcknowledgementNone),
		NewBudget(100, 0, 0, 1, AcknowledgementNone),
	)
}

// CapacityDemand is the reservation needed by one scheduler action. New Runs
// consume one Project and Workspace Task slot. Progression may consume agent
// or helper capacity without creating another active Task.
type CapacityDemand struct {
	projectTasks   uint64
	workspaceTasks uint64
	agents         uint64
	helpers        uint64
}

// NewRunDemand is the fixed top-level Task Agent demand for a new Run.
func NewRunDemand() CapacityDemand {
	return CapacityDemand{projectTasks: 1, workspaceTasks: 1, agents: 1}
}

// ProgressionDemand freezes the agent and helper demand for an active Run.
func ProgressionDemand(agents, helpers uint64) CapacityDemand {
	return CapacityDemand{agents: agents, helpers: helpers}
}

// Limits is the Project's frozen scheduler capacity policy.
type Limits struct {
	maxActiveTasks             uint64
	maxActiveTasksPerWorkspace uint64
	maxConcurrentAgents        uint64
	maxHelpersPerTask          uint64
}

// NewLimits freezes explicit positive capacity limits.
func NewLimits(maxActiveTasks, maxActiveTasksPerWorkspace, maxConcurrentAgents, maxHelpersPerTask uint64) Limits {
	return Limits{
		maxActiveTasks:             maxActiveTasks,
		maxActiveTasksPerWorkspace: maxActiveTasksPerWorkspace,
		maxConcurrentAgents:        maxConcurrentAgents,
		maxHelpersPerTask:          maxHelpersPerTask,
	}
}

// DefaultLimits returns PLAN section 10.3's defaults.
func DefaultLimits() Limits {
	return NewLimits(4, 2, 8, 3)
}

func (limits Limits) valid() bool {
	return limits.maxActiveTasks > 0 &&
		limits.maxActiveTasksPerWorkspace > 0 &&
		limits.maxConcurrentAgents > 0 &&
		limits.maxHelpersPerTask > 0
}

// Usage separates durable consumption from outstanding reservations.
type Usage struct {
	activeTasks    uint64
	reservedTasks  uint64
	activeAgents   uint64
	reservedAgents uint64
}

// NewUsage freezes Project-wide active and reserved capacity.
func NewUsage(activeTasks, reservedTasks, activeAgents, reservedAgents uint64) Usage {
	return Usage{
		activeTasks:    activeTasks,
		reservedTasks:  reservedTasks,
		activeAgents:   activeAgents,
		reservedAgents: reservedAgents,
	}
}

// WorkspaceUsage separates active and reserved Task slots for one Workspace.
type WorkspaceUsage struct {
	workspace     WorkspaceID
	activeTasks   uint64
	reservedTasks uint64
}

// NewWorkspaceUsage freezes one Workspace capacity observation.
func NewWorkspaceUsage(workspace WorkspaceID, activeTasks, reservedTasks uint64) WorkspaceUsage {
	return WorkspaceUsage{workspace: workspace, activeTasks: activeTasks, reservedTasks: reservedTasks}
}

// TaskFacts is one immutable scheduler work item. Its fields are private and
// every With method returns a changed copy, so a constructed Snapshot cannot
// be changed through aliases.
type TaskFacts struct {
	id                TaskID
	workspace         WorkspaceID
	queuedAt          int64
	priority          Priority
	policyOverride    PolicyOverride
	workClass         WorkClass
	dependency        DependencyState
	launchNow         bool
	hasActiveRun      bool
	taskComplete      bool
	organizerApproved bool
	preflightReady    bool
	demand            CapacityDemand
	helpersActive     uint64
	helpersReserved   uint64
	budgets           Budgets
}

// NewTask returns the smallest admitted new-Run fixture. Policy is inherited,
// priority is normal, dependencies are satisfied, and all required frozen
// eligibility observations are present.
func NewTask(id TaskID, workspace WorkspaceID, queuedAt int64) TaskFacts {
	return TaskFacts{
		id:                id,
		workspace:         workspace,
		queuedAt:          queuedAt,
		priority:          PriorityNormal,
		policyOverride:    PolicyInherit,
		workClass:         WorkNewRun,
		dependency:        DependenciesSatisfied,
		taskComplete:      true,
		organizerApproved: true,
		preflightReady:    true,
		demand:            NewRunDemand(),
		budgets:           AdmittedBudgets(),
	}
}

// WithPriority returns a copy with the frozen Task priority.
func (task TaskFacts) WithPriority(priority Priority) TaskFacts {
	task.priority = priority
	return task
}

// WithPolicyOverride returns a copy with the permitted Task launch override.
func (task TaskFacts) WithPolicyOverride(override PolicyOverride) TaskFacts {
	task.policyOverride = override
	return task
}

// WithLaunchNow returns a copy with or without an explicit human Launch now
// request. It does not imply a dependency override.
func (task TaskFacts) WithLaunchNow(requested bool) TaskFacts {
	task.launchNow = requested
	return task
}

// WithDependency returns a copy with the frozen dependency state.
func (task TaskFacts) WithDependency(state DependencyState) TaskFacts {
	task.dependency = state
	return task
}

// AsActiveRunProgression returns a copy representing work for the already
// active Run and freezes its additional agent/helper demand.
func (task TaskFacts) AsActiveRunProgression(demand CapacityDemand) TaskFacts {
	task.workClass = WorkActiveRunProgression
	task.hasActiveRun = true
	task.demand = demand
	return task
}

// WithActiveRun returns a copy with the reconciled active-Run fact. It exists
// so tests can express stale/contradictory combinations explicitly.
func (task TaskFacts) WithActiveRun(active bool) TaskFacts {
	task.hasActiveRun = active
	return task
}

// WithRequiredFacts returns a copy with the three new-Run eligibility facts.
func (task TaskFacts) WithRequiredFacts(complete, organizerApproved, preflightReady bool) TaskFacts {
	task.taskComplete = complete
	task.organizerApproved = organizerApproved
	task.preflightReady = preflightReady
	return task
}

// WithHelperUsage returns a copy with active and already-reserved helpers for
// this Run.
func (task TaskFacts) WithHelperUsage(active, reserved uint64) TaskFacts {
	task.helpersActive = active
	task.helpersReserved = reserved
	return task
}

// WithBudgets returns a copy with frozen Task/Run budget facts.
func (task TaskFacts) WithBudgets(budgets Budgets) TaskFacts {
	task.budgets = budgets
	return task
}

// Snapshot is the immutable closed input to Evaluate.
type Snapshot struct {
	policy          LaunchPolicy
	projectActive   bool
	lease           LeaseState
	disk            DiskState
	provider        ProviderState
	limits          Limits
	usage           Usage
	workspaceUsages []WorkspaceUsage
	tasks           []TaskFacts
}

// NewSnapshot copies every supplied fact into an admitted global fixture.
func NewSnapshot(policy LaunchPolicy, limits Limits, usage Usage, tasks ...TaskFacts) Snapshot {
	return Snapshot{
		policy:        policy,
		projectActive: true,
		lease:         LeaseCurrent,
		disk:          DiskReady,
		provider:      ProviderReady,
		limits:        limits,
		usage:         usage,
		tasks:         append([]TaskFacts(nil), tasks...),
	}
}

// WithProjectActive returns a copy with the Project state observation.
func (snapshot Snapshot) WithProjectActive(active bool) Snapshot {
	snapshot.projectActive = active
	return snapshot
}

// WithLease returns a copy with the Project execution-lease observation.
func (snapshot Snapshot) WithLease(lease LeaseState) Snapshot {
	snapshot.lease = lease
	return snapshot
}

// WithDisk returns a copy with the preflight disk observation.
func (snapshot Snapshot) WithDisk(disk DiskState) Snapshot {
	snapshot.disk = disk
	return snapshot
}

// WithProvider returns a copy with the provider observation.
func (snapshot Snapshot) WithProvider(provider ProviderState) Snapshot {
	snapshot.provider = provider
	return snapshot
}

// WithWorkspaceUsage returns a copy containing copied Workspace capacity
// observations. Duplicate Workspace IDs are ambiguous and fail closed.
func (snapshot Snapshot) WithWorkspaceUsage(usages ...WorkspaceUsage) Snapshot {
	snapshot.workspaceUsages = append([]WorkspaceUsage(nil), usages...)
	return snapshot
}
