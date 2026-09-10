// SPDX-License-Identifier: Apache-2.0

// Package scheduling defines the closed facts and durable reservation values
// shared by the scheduler reducer, application service, and store adapters.
// It contains no clock, I/O, connector, or lifecycle-effect authority.
package scheduling

// SchemaVersion identifies the complete scheduler fact contract.
const SchemaVersion = "director.scheduler.facts/v1"

type TaskID string
type WorkspaceID string

type Priority string

const (
	PriorityUrgent Priority = "urgent"
	PriorityHigh   Priority = "high"
	PriorityNormal Priority = "normal"
	PriorityLow    Priority = "low"
)

type LaunchPolicy string

const (
	PolicyManual    LaunchPolicy = "manual"
	PolicyAutomatic LaunchPolicy = "automatic"
)

type PolicyOverride string

const (
	PolicyInherit        PolicyOverride = "inherit"
	PolicyForceManual    PolicyOverride = "manual"
	PolicyForceAutomatic PolicyOverride = "automatic"
)

type WorkClass string

const (
	WorkNewRun               WorkClass = "new_run"
	WorkActiveRunProgression WorkClass = "active_run_progression"
)

// DependencyState makes an audited override distinct from both an ordinary
// satisfied graph and a blocked graph. There is no bare bypass boolean.
type DependencyState string

const (
	DependenciesSatisfied       DependencyState = "satisfied"
	DependenciesWaiting         DependencyState = "waiting"
	DependenciesAuditedOverride DependencyState = "audited_override"
)

type LeaseState string

const (
	LeaseCurrent   LeaseState = "current"
	LeaseLost      LeaseState = "lost"
	LeaseStale     LeaseState = "stale"
	LeaseAmbiguous LeaseState = "ambiguous"
)

type DiskState string

const (
	DiskReady         DiskState = "ready"
	DiskUnavailable   DiskState = "unavailable"
	DiskLimitExceeded DiskState = "limit_exceeded"
	DiskStale         DiskState = "stale"
	DiskAmbiguous     DiskState = "ambiguous"
)

type ProviderState string

const (
	ProviderReady       ProviderState = "ready"
	ProviderUnavailable ProviderState = "unavailable"
	ProviderNotAdmitted ProviderState = "not_admitted"
	ProviderStale       ProviderState = "stale"
	ProviderAmbiguous   ProviderState = "ambiguous"
)

type BudgetState string

const (
	BudgetReady       BudgetState = "ready"
	BudgetUnavailable BudgetState = "unavailable"
	BudgetStale       BudgetState = "stale"
	BudgetAmbiguous   BudgetState = "ambiguous"
)

// AcknowledgementState is explicit so missing, stale, and contradictory human
// decisions cannot collapse into the same boolean.
type AcknowledgementState string

const (
	AcknowledgementNone      AcknowledgementState = "none"
	AcknowledgementCurrent   AcknowledgementState = "current"
	AcknowledgementStale     AcknowledgementState = "stale"
	AcknowledgementAmbiguous AcknowledgementState = "ambiguous"
)

// Budget is a finite immutable ledger observation. Revision binds the soft
// acknowledgement to the exact unchanged limit. CI ignores acknowledgement
// because its integer cycle count has only a hard limit.
type Budget struct {
	State                BudgetState          `json:"state"`
	Revision             string               `json:"revision"`
	Limit                uint64               `json:"limit"`
	Used                 uint64               `json:"used"`
	Reserved             uint64               `json:"reserved"`
	Requested            uint64               `json:"requested"`
	Acknowledgement      AcknowledgementState `json:"acknowledgement"`
	AcknowledgedRevision string               `json:"acknowledgedRevision,omitempty"`
}

type Budgets struct {
	Time Budget `json:"time"`
	Cost Budget `json:"cost"`
	CI   Budget `json:"ci"`
}

type CapacityDemand struct {
	ProjectTasks   uint64 `json:"projectTasks"`
	WorkspaceTasks uint64 `json:"workspaceTasks"`
	Agents         uint64 `json:"agents"`
	Helpers        uint64 `json:"helpers"`
}

func NewRunDemand() CapacityDemand {
	return CapacityDemand{ProjectTasks: 1, WorkspaceTasks: 1, Agents: 1}
}

func ProgressionDemand(agents, helpers uint64) CapacityDemand {
	return CapacityDemand{Agents: agents, Helpers: helpers}
}

type Limits struct {
	MaxActiveTasks             uint64 `json:"maxActiveTasks"`
	MaxActiveTasksPerWorkspace uint64 `json:"maxActiveTasksPerWorkspace"`
	MaxConcurrentAgents        uint64 `json:"maxConcurrentAgents"`
	MaxHelpersPerTask          uint64 `json:"maxHelpersPerTask"`
}

// DefaultLimits returns PLAN section 10.3's current Project defaults. The
// Organizer is configuration/state, not an agent, and consumes no slot.
func DefaultLimits() Limits {
	return Limits{
		MaxActiveTasks: 6, MaxActiveTasksPerWorkspace: 2,
		MaxConcurrentAgents: 8, MaxHelpersPerTask: 3,
	}
}

type Usage struct {
	ActiveTasks    uint64 `json:"activeTasks"`
	ReservedTasks  uint64 `json:"reservedTasks"`
	ActiveAgents   uint64 `json:"activeAgents"`
	ReservedAgents uint64 `json:"reservedAgents"`
}

type WorkspaceUsage struct {
	Workspace     WorkspaceID `json:"workspace"`
	ActiveTasks   uint64      `json:"activeTasks"`
	ReservedTasks uint64      `json:"reservedTasks"`
}

// LaunchNowRequest is a current server-authenticated human command. It never
// implies a dependency override, and version drift invalidates it.
type LaunchNowRequest struct {
	Requested   bool   `json:"requested"`
	ActorKind   string `json:"actorKind,omitempty"`
	ActorID     string `json:"actorId,omitempty"`
	AuditID     string `json:"auditId,omitempty"`
	TaskVersion uint64 `json:"taskVersion,omitempty"`
}

type TaskFacts struct {
	ID                         TaskID           `json:"id"`
	Workspace                  WorkspaceID      `json:"workspace"`
	TaskVersion                uint64           `json:"taskVersion"`
	QueuedAtUnixMillis         int64            `json:"queuedAtUnixMillis"`
	Priority                   Priority         `json:"priority"`
	PolicyOverride             PolicyOverride   `json:"policyOverride"`
	WorkClass                  WorkClass        `json:"workClass"`
	Dependency                 DependencyState  `json:"dependency"`
	DependencyOverrideAuditIDs []string         `json:"dependencyOverrideAuditIds,omitempty"`
	DependencyOverrideVersion  uint64           `json:"dependencyOverrideVersion,omitempty"`
	LaunchNow                  LaunchNowRequest `json:"launchNow"`
	HasActiveRun               bool             `json:"hasActiveRun"`
	TaskComplete               bool             `json:"taskComplete"`
	OrganizerApproved          bool             `json:"organizerApproved"`
	PreflightReady             bool             `json:"preflightReady"`
	Demand                     CapacityDemand   `json:"demand"`
	HelpersActive              uint64           `json:"helpersActive"`
	HelpersReserved            uint64           `json:"helpersReserved"`
	Budgets                    Budgets          `json:"budgets"`
}

// Snapshot is copied from durable and authoritative observations before one
// pure reduction. Version is the compare-and-swap version consumed by the
// reservation store; LeaseEpoch binds all resulting launch permits.
type Snapshot struct {
	SchemaVersion   string           `json:"schemaVersion"`
	ProjectID       string           `json:"projectId"`
	Version         uint64           `json:"version"`
	Policy          LaunchPolicy     `json:"policy"`
	ProjectActive   bool             `json:"projectActive"`
	Lease           LeaseState       `json:"lease"`
	LeaseEpoch      uint64           `json:"leaseEpoch"`
	Disk            DiskState        `json:"disk"`
	Provider        ProviderState    `json:"provider"`
	Limits          Limits           `json:"limits"`
	Usage           Usage            `json:"usage"`
	WorkspaceUsages []WorkspaceUsage `json:"workspaceUsages,omitempty"`
	Tasks           []TaskFacts      `json:"tasks,omitempty"`
}

// CloneSnapshot prevents a store or caller from mutating reducer facts through
// shared slice aliases.
func CloneSnapshot(snapshot Snapshot) Snapshot {
	snapshot.WorkspaceUsages = append([]WorkspaceUsage(nil), snapshot.WorkspaceUsages...)
	snapshot.Tasks = append([]TaskFacts(nil), snapshot.Tasks...)
	for index := range snapshot.Tasks {
		snapshot.Tasks[index].DependencyOverrideAuditIDs = append(
			[]string(nil), snapshot.Tasks[index].DependencyOverrideAuditIDs...,
		)
	}
	return snapshot
}

type Reservation struct {
	ID            string         `json:"id"`
	TaskID        TaskID         `json:"taskId"`
	Workspace     WorkspaceID    `json:"workspace"`
	TaskVersion   uint64         `json:"taskVersion"`
	WorkClass     WorkClass      `json:"workClass"`
	LeaseEpoch    uint64         `json:"leaseEpoch"`
	Demand        CapacityDemand `json:"demand"`
	TimeRequested uint64         `json:"timeRequested"`
	CostRequested uint64         `json:"costRequested"`
	CIRequested   uint64         `json:"ciRequested"`
}
