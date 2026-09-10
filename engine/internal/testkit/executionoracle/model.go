// SPDX-License-Identifier: Apache-2.0

package executionoracle

import "slices"

// FactKind is the complete top-level vocabulary understood by the oracle.
type FactKind uint8

const (
	FactOrganizer FactKind = iota + 1
	FactWorker
	FactReviewer
	FactHelper
	FactRun
	FactCandidate
	FactWorkspace
	FactLease
	FactProvider
	FactScopedMCP
	FactUsage
	FactControl
	FactFailure
)

// FactKinds returns every fact category in canonical order.
func FactKinds() []FactKind {
	return []FactKind{
		FactOrganizer, FactWorker, FactReviewer, FactHelper, FactRun,
		FactCandidate, FactWorkspace, FactLease, FactProvider,
		FactScopedMCP, FactUsage, FactControl, FactFailure,
	}
}

func (kind FactKind) valid() bool { return kind >= FactOrganizer && kind <= FactFailure }

// Platform contains only the supported release platform. Other numeric values
// are unknown facts and fail closed.
type Platform uint8

const PlatformLinux Platform = 1

// Role is the complete scoped execution role vocabulary. Organizer is a
// repository/configuration authority surface, never an AgentFact.
type Role uint8

const (
	RoleOrganizer Role = iota + 1
	RoleWorker
	RoleReviewer
	RoleHelper
)

// Roles returns every scoped role in canonical order.
func Roles() []Role { return []Role{RoleOrganizer, RoleWorker, RoleReviewer, RoleHelper} }

func (role Role) valid() bool { return role >= RoleOrganizer && role <= RoleHelper }

// Scope is the immutable Project/Task/Run/Workspace binding for one execution.
type Scope struct {
	ProjectID   string
	TaskID      string
	RunID       string
	WorkspaceID string
}

func (scope Scope) valid() bool {
	return scope.ProjectID != "" && scope.TaskID != "" && scope.RunID != "" && scope.WorkspaceID != ""
}

// OrganizerState is the closed Organizer revision observation vocabulary.
type OrganizerState uint8

const (
	OrganizerApproved OrganizerState = iota + 1
	OrganizerUnavailable
	OrganizerAmbiguous
)

func (state OrganizerState) valid() bool {
	return state >= OrganizerApproved && state <= OrganizerAmbiguous
}

// OrganizerFact binds the Run to its approved Organizer revision.
type OrganizerFact struct {
	ProjectID string
	Revision  string
	State     OrganizerState
}

// RunState is the closed durable Run lifecycle vocabulary.
type RunState uint8

const (
	RunPrepared RunState = iota + 1
	RunActive
	RunPaused
	RunCancelled
	RunTerminal
	RunAmbiguous
)

func (state RunState) valid() bool { return state >= RunPrepared && state <= RunAmbiguous }

// RunFact contains only frozen execution identity and finite lifecycle counts.
type RunFact struct {
	Scope               Scope
	State               RunState
	OrganizerRevision   string
	ProfileRevision     string
	ReplacementCount    uint8
	MaximumReplacements uint8
	MaximumHelpers      uint8
	PreparationReady    bool
	RootWorkerVisible   bool
	RootReviewerVisible bool
}

// WorkspaceState is the closed fake-host workspace vocabulary.
type WorkspaceState uint8

const (
	WorkspaceAbsent WorkspaceState = iota + 1
	WorkspaceReady
	WorkspaceArchived
	WorkspaceAmbiguous
)

func (state WorkspaceState) valid() bool {
	return state >= WorkspaceAbsent && state <= WorkspaceAmbiguous
}

// WorkspaceFact carries Director ownership separately from host visibility.
type WorkspaceFact struct {
	ID      string
	RunID   string
	State   WorkspaceState
	Owned   bool
	Visible bool
}

// LeaseState is the complete execution-lease observation vocabulary.
type LeaseState uint8

const (
	LeaseCurrent LeaseState = iota + 1
	LeaseLost
	LeaseStale
	LeaseAmbiguous
)

func (state LeaseState) valid() bool { return state >= LeaseCurrent && state <= LeaseAmbiguous }

// LeaseFact contains the durable epoch and the independently reconciled
// dispatch gate. Expiry alone can therefore never grant fake-host authority.
type LeaseFact struct {
	ProjectID       string
	Holder          string
	Epoch           uint64
	State           LeaseState
	DispatchAllowed bool
}

// ProviderKind is the exact admitted native-provider family vocabulary. The
// oracle intentionally has no generic provider family.
type ProviderKind uint8

const (
	ProviderCodex ProviderKind = iota + 1
	ProviderClaude
	ProviderOpenCode
)

func (kind ProviderKind) valid() bool { return kind >= ProviderCodex && kind <= ProviderOpenCode }

// ProviderState is the complete provider observation vocabulary.
type ProviderState uint8

const (
	ProviderReady ProviderState = iota + 1
	ProviderUnavailable
	ProviderNotAdmitted
	ProviderStale
	ProviderAmbiguous
)

func (state ProviderState) valid() bool { return state >= ProviderReady && state <= ProviderAmbiguous }

// ProviderFact binds capability observations to the frozen Run profile.
type ProviderFact struct {
	Kind            ProviderKind
	State           ProviderState
	ProfileRevision string
	SupportsMCP     bool
	ExactToolPolicy bool
}

// Tool is the complete mock Director MCP tool vocabulary.
type Tool uint16

const (
	ToolInspectProject Tool = 1 << iota
	ToolSubmitPlanningCommand
	ToolInspectRun
	ToolSubmitOutcome
	ToolRequestHelper
	ToolInspectCandidate
	ToolSubmitVerdict
	ToolSubmitContribution
)

// ToolSet is a bit set over the closed Tool vocabulary.
type ToolSet uint16

func tools(values ...Tool) ToolSet {
	var result ToolSet
	for _, value := range values {
		result |= ToolSet(value)
	}
	return result
}

func allTools() ToolSet {
	return tools(
		ToolInspectProject, ToolSubmitPlanningCommand, ToolInspectRun,
		ToolSubmitOutcome, ToolRequestHelper, ToolInspectCandidate,
		ToolSubmitVerdict, ToolSubmitContribution,
	)
}

// AllowedTools returns the exact tool set for one role. Unknown roles receive
// no tools and are rejected by fact validation.
func AllowedTools(role Role) ToolSet {
	switch role {
	case RoleOrganizer:
		return tools(ToolInspectProject, ToolSubmitPlanningCommand)
	case RoleWorker:
		return tools(ToolInspectRun, ToolSubmitOutcome, ToolRequestHelper)
	case RoleReviewer:
		return tools(ToolInspectCandidate, ToolSubmitVerdict)
	case RoleHelper:
		return tools(ToolInspectRun, ToolSubmitContribution)
	default:
		return 0
	}
}

// MCPBinding is one fixed role/session scope. Organizer is Project-scoped and
// therefore leaves Task, Run, and Workspace empty; all execution roles must
// match the complete Run scope.
type MCPBinding struct {
	Role        Role
	ProjectID   string
	TaskID      string
	RunID       string
	WorkspaceID string
	Tools       ToolSet
}

// MCPFact holds exactly one binding for every closed role.
type MCPFact struct {
	Bindings []MCPBinding
}

// UsageState is the freshness vocabulary for one consumptive ledger.
type UsageState uint8

const (
	UsageCurrent UsageState = iota + 1
	UsageUnavailable
	UsageStale
	UsageAmbiguous
)

func (state UsageState) valid() bool { return state >= UsageCurrent && state <= UsageAmbiguous }

// DimensionUsage models durable consumption plus outstanding and proposed
// reservations. AcknowledgedRevision never enlarges Limit.
type DimensionUsage struct {
	State                UsageState
	Limit                uint64
	Consumed             uint64
	Reserved             uint64
	NextReservation      uint64
	Revision             uint64
	AcknowledgedRevision uint64
}

// UsageFact is the closed mandatory time/token/turn and optional-cost ledger.
// Cost remains finite in this small-state oracle to keep every permutation
// bounded and comparable.
type UsageFact struct {
	Time   DimensionUsage
	Tokens DimensionUsage
	Turns  DimensionUsage
	Cost   DimensionUsage
}

// ControlKind is the complete operator-control vocabulary.
type ControlKind uint8

const (
	ControlNone ControlKind = iota + 1
	ControlPause
	ControlResume
	ControlCancel
	ControlEmergencyStop
)

func (kind ControlKind) valid() bool { return kind >= ControlNone && kind <= ControlEmergencyStop }

// ControlFact binds a control to the exact Run scope and records authenticated
// human confirmation where the operation requires it.
type ControlFact struct {
	Kind           ControlKind
	RunID          string
	HumanConfirmed bool
}

// FailureKind is the complete failure-observation vocabulary.
type FailureKind uint8

const (
	FailureNone FailureKind = iota + 1
	FailureRecoverableWorker
	FailureUnrecoverableWorker
	FailureExternalUnavailable
	FailureUnknown
	FailureContradictory
)

func (kind FailureKind) valid() bool { return kind >= FailureNone && kind <= FailureContradictory }

// FailureFact is bounded, redacted, and scope-fixed.
type FailureFact struct {
	Kind        FailureKind
	RunID       string
	Fingerprint string
}

// LivenessState separates wake signals and suspicion from authoritative
// termination. The compound state can only request a fresh observation.
type LivenessState uint8

const (
	LivenessHealthy LivenessState = iota + 1
	LivenessSuspected
	LivenessTerminalWake
	LivenessCompoundStalled
	LivenessAmbiguous
)

func (state LivenessState) valid() bool {
	return state >= LivenessHealthy && state <= LivenessAmbiguous
}

// LivenessFact contains deterministic counters rather than wall-clock reads.
type LivenessFact struct {
	State                        LivenessState
	QuietSeconds                 uint16
	LocalSamples                 uint8
	ExternalCycles               uint8
	IndependentSources           uint8
	ActiveExternalCadenceSeconds uint16
	IdleExternalCadenceSeconds   uint16
	TerminalLatencyMillis        uint16
	ActivePolling                bool
	KnownWait                    bool
	DeclaredLongStep             bool
	ExternalOutage               bool
}

// CandidateState is the complete Candidate observation vocabulary.
type CandidateState uint8

const (
	CandidateAbsent CandidateState = iota + 1
	CandidateCurrent
	CandidateStale
	CandidateAmbiguous
)

func (state CandidateState) valid() bool {
	return state >= CandidateAbsent && state <= CandidateAmbiguous
}

// CandidateFact binds an immutable Candidate to its producing Worker and Run.
type CandidateFact struct {
	ID       string
	RunID    string
	WorkerID string
	State    CandidateState
}

// AgentState is the closed host-observation vocabulary.
type AgentState uint8

const (
	AgentCreated AgentState = iota + 1
	AgentIdle
	AgentRunning
	AgentClosed
	AgentAmbiguous
)

func (state AgentState) valid() bool { return state >= AgentCreated && state <= AgentAmbiguous }

// PromptState keeps bootstrap and real-work boundaries distinct.
type PromptState uint8

const (
	PromptNone PromptState = iota + 1
	PromptSent
	PromptFinished
	PromptErrored
	PromptPermission
	PromptAmbiguous
)

func (state PromptState) valid() bool { return state >= PromptNone && state <= PromptAmbiguous }

// AgentFact models Worker, Reviewer, and helper observations. Worker and
// Reviewer parent IDs must be empty; every helper must name its Worker parent.
type AgentFact struct {
	ID              string
	Role            Role
	RunID           string
	WorkspaceID     string
	ParentID        string
	ProfileRevision string
	CandidateID     string
	State           AgentState
	BootstrapDone   bool
	Prompt          PromptState
	Archived        bool
	ProcessAbsent   bool
}

// HelperRequestState distinguishes engine admission from Task-Agent invocation
// and host observation. The oracle never emits an engine create-helper action.
type HelperRequestState uint8

const (
	HelperNotRequested HelperRequestState = iota + 1
	HelperRequested
	HelperAdmitted
	HelperObserved
	HelperUnknown
)

func (state HelperRequestState) valid() bool {
	return state >= HelperNotRequested && state <= HelperUnknown
}

// HelperRequestFact binds the one-use admission to its parent Worker.
type HelperRequestFact struct {
	State       HelperRequestState
	RunID       string
	WorkspaceID string
	ParentID    string
	IntentID    string
	HelperID    string
}

// Snapshot is the complete immutable input to Evaluate.
type Snapshot struct {
	Platform      Platform
	Organizer     OrganizerFact
	Run           RunFact
	Workspace     WorkspaceFact
	Lease         LeaseFact
	Provider      ProviderFact
	MCP           MCPFact
	Usage         UsageFact
	Control       ControlFact
	Failure       FailureFact
	Liveness      LivenessFact
	Candidate     CandidateFact
	Agents        []AgentFact
	HelperRequest HelperRequestFact
}

// Clone returns a deep copy suitable for independent mutation by a property
// test or a production test-source consumer.
func (snapshot Snapshot) Clone() Snapshot {
	snapshot.Agents = slices.Clone(snapshot.Agents)
	snapshot.MCP.Bindings = slices.Clone(snapshot.MCP.Bindings)
	return snapshot
}

// Baseline returns the smallest admitted pre-launch fact set.
func Baseline() Snapshot {
	scope := Scope{ProjectID: "project", TaskID: "task", RunID: "run", WorkspaceID: "workspace"}
	dimension := func() DimensionUsage {
		return DimensionUsage{State: UsageCurrent, Limit: 100, NextReservation: 1, Revision: 1}
	}
	bindings := make([]MCPBinding, 0, len(Roles()))
	for _, role := range Roles() {
		binding := MCPBinding{Role: role, ProjectID: scope.ProjectID, Tools: AllowedTools(role)}
		if role != RoleOrganizer {
			binding.TaskID = scope.TaskID
			binding.RunID = scope.RunID
			binding.WorkspaceID = scope.WorkspaceID
		}
		bindings = append(bindings, binding)
	}
	return Snapshot{
		Platform:  PlatformLinux,
		Organizer: OrganizerFact{ProjectID: scope.ProjectID, Revision: "organizer-r1", State: OrganizerApproved},
		Run: RunFact{
			Scope: scope, State: RunPrepared, OrganizerRevision: "organizer-r1",
			ProfileRevision: "profile-r1", MaximumReplacements: 1, MaximumHelpers: 3,
			PreparationReady: true, RootWorkerVisible: true, RootReviewerVisible: true,
		},
		Workspace: WorkspaceFact{ID: scope.WorkspaceID, RunID: scope.RunID, State: WorkspaceAbsent, Owned: true},
		Lease:     LeaseFact{ProjectID: scope.ProjectID, Holder: "engine", Epoch: 1, State: LeaseCurrent, DispatchAllowed: true},
		Provider: ProviderFact{
			Kind: ProviderCodex, State: ProviderReady, ProfileRevision: "profile-r1",
			SupportsMCP: true, ExactToolPolicy: true,
		},
		MCP:     MCPFact{Bindings: bindings},
		Usage:   UsageFact{Time: dimension(), Tokens: dimension(), Turns: dimension(), Cost: dimension()},
		Control: ControlFact{Kind: ControlNone, RunID: scope.RunID},
		Failure: FailureFact{Kind: FailureNone, RunID: scope.RunID},
		Liveness: LivenessFact{
			State: LivenessHealthy, ActiveExternalCadenceSeconds: 30, IdleExternalCadenceSeconds: 300,
		},
		Candidate:     CandidateFact{State: CandidateAbsent},
		HelperRequest: HelperRequestFact{State: HelperNotRequested, RunID: scope.RunID, WorkspaceID: scope.WorkspaceID},
	}
}
