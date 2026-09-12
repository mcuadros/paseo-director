// SPDX-License-Identifier: Apache-2.0

package planning

import (
	"errors"
	"strings"
	"unicode"
	"unicode/utf8"
)

const (
	QueryPath              = "/v1/planning/query"
	TaskDetailQueryPath    = "/v1/planning/task-detail"
	HomeQueryPath          = "/v1/planning/home"
	DoctorQueryPath        = "/v1/planning/doctor"
	OperationsQueryPath    = "/v1/planning/operations"
	OperationsMutationPath = "/v1/planning/operations-mutate"
	RepairMutationPath     = "/v1/planning/repair"
	OrganizerMutationPath  = "/v1/planning/organizer-bootstrap"
	MaximumRequestBytes    = 64 * 1024
	MaximumResponseBytes   = 4 * 1024 * 1024
	MaximumPageSize        = 100
	MaximumProjects        = 100
	MaximumWorkspaces      = 100
	MaximumEpics           = 500
	MaximumHomePageSize    = 50
	MaximumAuditEntries    = 64
	MaximumTechnicalLogs   = 128
)

var ErrQueryInvalid = errors.New("planning query is invalid")

type QueryInput struct {
	ProjectID    *string  `json:"projectId"`
	WorkspaceIDs []string `json:"workspaceIds"`
	EpicIDs      []string `json:"epicIds"`
	States       []string `json:"states"`
	Priorities   []string `json:"priorities"`
	Labels       []string `json:"labels"`
	Attention    []string `json:"attention"`
	Search       *string  `json:"search"`
	Sort         string   `json:"sort"`
	Cursor       *string  `json:"cursor"`
	PageSize     int      `json:"pageSize"`
}

func validOpaque(value string) bool {
	return value != "" && utf8.ValidString(value) && utf8.RuneCountInString(value) <= 128 &&
		value == strings.TrimSpace(value) && strings.IndexFunc(value, unicode.IsControl) < 0
}

func validUnique(values []string, maximum int, allowed map[string]struct{}) bool {
	if values == nil || len(values) > maximum {
		return false
	}
	seen := make(map[string]struct{}, len(values))
	for _, value := range values {
		if !validOpaque(value) {
			return false
		}
		if allowed != nil {
			if _, ok := allowed[value]; !ok {
				return false
			}
		}
		if _, duplicate := seen[value]; duplicate {
			return false
		}
		seen[value] = struct{}{}
	}
	return true
}

func allowed(values ...string) map[string]struct{} {
	result := make(map[string]struct{}, len(values))
	for _, value := range values {
		result[value] = struct{}{}
	}
	return result
}

var (
	queryStates     = allowed("needs_you", "queued", "building", "validating", "in_review", "ready", "done")
	queryPriorities = allowed("urgent", "high", "normal", "low")
	queryAttention  = allowed(
		"permission_required", "correction_budget_exhausted", "replacement_budget_exhausted",
		"git_state_irreconcilable", "configuration_required", "credential_required", "policy_override_required",
		"soft_budget_acknowledgment_required", "hard_budget_exhausted", "recovery_ambiguous",
		"review_decision_required", "feedback_decision_required",
	)
)

func validPageCursor(value string) bool {
	if len(value) == 0 || len(value) > 5500 {
		return false
	}
	return strings.IndexFunc(value, func(character rune) bool {
		return !((character >= 'A' && character <= 'Z') || (character >= 'a' && character <= 'z') ||
			(character >= '0' && character <= '9') || character == '_' || character == '-')
	}) < 0
}

// ValidateQuery applies the engine side of the generated Zod boundary before
// any TaskStore read. Projection performs the independent normalized filter
// and cursor validation after facts are loaded.
func ValidateQuery(input QueryInput) error {
	if input.PageSize < 1 || input.PageSize > MaximumPageSize ||
		(input.ProjectID != nil && !validOpaque(*input.ProjectID)) ||
		!validUnique(input.WorkspaceIDs, MaximumWorkspaces, nil) ||
		!validUnique(input.EpicIDs, MaximumEpics, nil) ||
		!validUnique(input.States, 7, queryStates) ||
		!validUnique(input.Priorities, 4, queryPriorities) ||
		!validUnique(input.Labels, 64, nil) ||
		!validUnique(input.Attention, 12, queryAttention) ||
		(input.Search != nil && (*input.Search == "" || !utf8.ValidString(*input.Search) ||
			utf8.RuneCountInString(*input.Search) > 256 || *input.Search != strings.TrimSpace(*input.Search) ||
			strings.IndexFunc(*input.Search, unicode.IsControl) >= 0)) ||
		(input.Sort != "scheduler_order" && input.Sort != "updated_desc" && input.Sort != "priority_fifo" && input.Sort != "key_asc") ||
		(input.Cursor != nil && !validPageCursor(*input.Cursor)) {
		return ErrQueryInvalid
	}
	return nil
}

type Explanation struct {
	Code                string  `json:"code"`
	Message             string  `json:"message"`
	WakeCondition       *string `json:"wakeCondition"`
	HumanActionRequired bool    `json:"humanActionRequired"`
}

type AllowedAction struct {
	Kind                    string  `json:"kind"`
	Label                   string  `json:"label"`
	TargetID                *string `json:"targetId"`
	RequestID               string  `json:"requestId"`
	IdempotencyKey          string  `json:"idempotencyKey"`
	ExpectedVersion         string  `json:"expectedVersion"`
	HumanApprovalRef        *string `json:"humanApprovalRef"`
	AcknowledgementRevision *string `json:"acknowledgementRevision"`
	Emphasis                string  `json:"emphasis"`
}

type TaskCounts struct {
	Open     string `json:"open"`
	Done     string `json:"done"`
	NeedsYou string `json:"needsYou"`
}

type ProjectSummary struct {
	ID             string          `json:"id"`
	Version        string          `json:"version"`
	Name           string          `json:"name"`
	State          string          `json:"state"`
	WorkspaceCount string          `json:"workspaceCount"`
	TaskCounts     TaskCounts      `json:"taskCounts"`
	Control        *Explanation    `json:"control"`
	AllowedActions []AllowedAction `json:"allowedActions"`
}

type WorkspaceSummary struct {
	ID                string          `json:"id"`
	ProjectID         string          `json:"projectId"`
	Version           string          `json:"version"`
	Name              string          `json:"name"`
	Health            string          `json:"health"`
	DefaultBaseBranch string          `json:"defaultBaseBranch"`
	TaskCounts        TaskCounts      `json:"taskCounts"`
	AllowedActions    []AllowedAction `json:"allowedActions"`
}

type EpicProgress struct {
	Completed string `json:"completed"`
	Total     string `json:"total"`
}

type EpicSummary struct {
	ID             string          `json:"id"`
	ProjectID      string          `json:"projectId"`
	Version        string          `json:"version"`
	Key            string          `json:"key"`
	Title          string          `json:"title"`
	Priority       *string         `json:"priority"`
	Labels         []string        `json:"labels"`
	Progress       EpicProgress    `json:"progress"`
	Blockers       []Explanation   `json:"blockers"`
	AllowedActions []AllowedAction `json:"allowedActions"`
}

type SchedulerFacts struct {
	LaunchMode        string        `json:"launchMode"`
	LaunchDisposition string        `json:"launchDisposition"`
	QueuePosition     *string       `json:"queuePosition"`
	FactsRevision     string        `json:"factsRevision"`
	Explanations      []Explanation `json:"explanations"`
}

type BudgetDimensionSummary struct {
	Dimension        string `json:"dimension"`
	Enabled          bool   `json:"enabled"`
	Consumed         string `json:"consumed"`
	Reserved         string `json:"reserved"`
	Limit            string `json:"limit"`
	RatioBasisPoints string `json:"ratioBasisPoints"`
}

type BudgetCountSummary struct {
	Dimension string `json:"dimension"`
	Consumed  string `json:"consumed"`
	Reserved  string `json:"reserved"`
	Limit     string `json:"limit"`
}

// RuntimeBudgetSummary is the bounded Organizer-facing projection. It exposes
// exact ledger amounts and churn counters, never raw provider payloads or a
// connector-computed lifecycle decision.
type RuntimeBudgetSummary struct {
	PolicyRevision           string                   `json:"policyRevision"`
	State                    string                   `json:"state"`
	SoftThresholdBasisPoints string                   `json:"softThresholdBasisPoints"`
	Dimensions               []BudgetDimensionSummary `json:"dimensions"`
	Counts                   []BudgetCountSummary     `json:"counts"`
	WorkerTurns              string                   `json:"workerTurns"`
	HelperTurns              string                   `json:"helperTurns"`
	ReviewerTurns            string                   `json:"reviewerTurns"`
	CorrectionTurns          string                   `json:"correctionTurns"`
	ReasonCode               *string                  `json:"reasonCode"`
}

// FeedbackSummary is the bounded Organizer/Board projection. It exposes only
// immutable audit identities and counts, never comment bodies or actor
// credentials, and cannot be used to reply to or resolve a GitHub thread.
type FeedbackSummary struct {
	Phase             string  `json:"phase"`
	CurrentActionable string  `json:"currentActionable"`
	AuditRecords      string  `json:"auditRecords"`
	CurrentRevision   string  `json:"currentRevision"`
	CorrectionBatch   *string `json:"correctionBatch"`
	ReasonCode        *string `json:"reasonCode"`
}

type TaskSummary struct {
	ID              string                `json:"id"`
	ProjectID       string                `json:"projectId"`
	WorkspaceID     string                `json:"workspaceId"`
	EpicID          *string               `json:"epicId"`
	Version         string                `json:"version"`
	Key             string                `json:"key"`
	Title           string                `json:"title"`
	DerivedState    string                `json:"derivedState"`
	Priority        string                `json:"priority"`
	Labels          []string              `json:"labels"`
	UpdatedAt       string                `json:"updatedAt"`
	Blockers        []Explanation         `json:"blockers"`
	NeedsYou        []Explanation         `json:"needsYou"`
	AllowedActions  []AllowedAction       `json:"allowedActions"`
	SchedulingFacts SchedulerFacts        `json:"schedulingFacts"`
	RuntimeBudget   *RuntimeBudgetSummary `json:"runtimeBudget"`
	Feedback        *FeedbackSummary      `json:"feedback"`
}

type CapacityFacts struct {
	ActiveTasks                string `json:"activeTasks"`
	MaxActiveTasks             string `json:"maxActiveTasks"`
	ActiveWorkspaceTasks       string `json:"activeWorkspaceTasks"`
	MaxActiveTasksPerWorkspace string `json:"maxActiveTasksPerWorkspace"`
	ActiveAgents               string `json:"activeAgents"`
	MaxConcurrentAgents        string `json:"maxConcurrentAgents"`
	ReservedAgents             string `json:"reservedAgents"`
}

type Page struct {
	SelectedProjectID    *string            `json:"selectedProjectId"`
	SelectedWorkspaceIDs []string           `json:"selectedWorkspaceIds"`
	SelectedEpicIDs      []string           `json:"selectedEpicIds"`
	AppliedQuery         QueryInput         `json:"appliedQuery"`
	AvailableSorts       []string           `json:"availableSorts"`
	AvailableLabels      []string           `json:"availableLabels"`
	Projects             []ProjectSummary   `json:"projects"`
	Workspaces           []WorkspaceSummary `json:"workspaces"`
	Epics                []EpicSummary      `json:"epics"`
	Tasks                []TaskSummary      `json:"tasks"`
	Capacity             CapacityFacts      `json:"capacity"`
	SurfaceActions       []AllowedAction    `json:"surfaceActions"`
	TotalTasks           string             `json:"totalTasks"`
	NextCursor           *string            `json:"nextCursor"`
}

type Snapshot struct {
	SchemaVersion   int    `json:"schemaVersion"`
	ContractVersion string `json:"contractVersion"`
	ContractHash    string `json:"contractHash"`
	Cursor          string `json:"cursor"`
	Page            Page   `json:"page"`
}

// TaskDetailQueryInput resolves either one explicit Board Task or the one Task
// whose current Run is exactly bound to the native Paseo agent/workspace pair.
// The two contexts are intentionally disjoint so a native ID can never be
// confused with a Director Workspace ID.
type TaskDetailQueryInput struct {
	HostID           string  `json:"hostId"`
	Context          string  `json:"context"`
	TaskID           *string `json:"taskId"`
	PaseoWorkspaceID *string `json:"paseoWorkspaceId"`
	PaseoAgentID     *string `json:"paseoAgentId"`
	AfterCursor      *string `json:"afterCursor"`
}

func validUint64Decimal(value string) bool {
	if value == "" || len(value) > 20 || (len(value) > 1 && value[0] == '0') {
		return false
	}
	for _, character := range value {
		if character < '0' || character > '9' {
			return false
		}
	}
	if len(value) == 20 && value > "18446744073709551615" {
		return false
	}
	return true
}

func ValidateTaskDetailQuery(input TaskDetailQueryInput) error {
	if !validOpaque(input.HostID) || (input.AfterCursor != nil && !validUint64Decimal(*input.AfterCursor)) {
		return ErrQueryInvalid
	}
	board := input.Context == "board" && input.TaskID != nil && validOpaque(*input.TaskID) &&
		input.PaseoWorkspaceID == nil && input.PaseoAgentID == nil
	agent := input.Context == "agent" && input.TaskID == nil && input.PaseoWorkspaceID != nil &&
		input.PaseoAgentID != nil && validOpaque(*input.PaseoWorkspaceID) && validOpaque(*input.PaseoAgentID)
	if !board && !agent {
		return ErrQueryInvalid
	}
	return nil
}

type TaskDetailBinding struct {
	HostID            string       `json:"hostId"`
	ProjectID         string       `json:"projectId"`
	WorkspaceID       string       `json:"workspaceId"`
	TaskID            string       `json:"taskId"`
	TaskVersion       string       `json:"taskVersion"`
	RunID             *string      `json:"runId"`
	RunNumber         *string      `json:"runNumber"`
	RunVersion        *string      `json:"runVersion"`
	CandidateID       *string      `json:"candidateId"`
	CandidateSHA      *string      `json:"candidateSha"`
	PaseoWorkspaceID  *string      `json:"paseoWorkspaceId"`
	PaseoAgentID      *string      `json:"paseoAgentId"`
	AgentNavigation   string       `json:"agentNavigation"`
	UnavailableReason *Explanation `json:"unavailableReason"`
}

type TaskAcceptanceCriterion struct {
	ID     string `json:"id"`
	Text   string `json:"text"`
	Status string `json:"status"`
}

type TaskDependencyDetail struct {
	ID             string      `json:"id"`
	Kind           string      `json:"kind"`
	Key            string      `json:"key"`
	Title          string      `json:"title"`
	Satisfied      bool        `json:"satisfied"`
	OverrideStatus string      `json:"overrideStatus"`
	Explanation    Explanation `json:"explanation"`
}

type ConfigurationOverride struct {
	Key   string      `json:"key"`
	Mode  string      `json:"mode"`
	Value interface{} `json:"value,omitempty"`
}

type ConfigurationEntry struct {
	Key             string                `json:"key"`
	Configured      ConfigurationOverride `json:"configured"`
	EffectiveValue  interface{}           `json:"effectiveValue"`
	EffectiveSource string                `json:"effectiveSource"`
	AllowedValues   []interface{}         `json:"allowedValues"`
}

type ConfigurationTarget struct {
	Scope string `json:"scope"`
	ID    string `json:"id"`
}

type TaskActivity struct {
	ID         string  `json:"id"`
	Sequence   string  `json:"sequence"`
	OccurredAt *string `json:"occurredAt"`
	Kind       string  `json:"kind"`
	Code       string  `json:"code"`
	Message    string  `json:"message"`
}

// TaskDetail is a bounded semantic projection. Activity contains humanized
// event summaries only; raw event payloads, logs, paths, and credentials have
// no representation here.
type TaskDetail struct {
	Binding              TaskDetailBinding         `json:"binding"`
	Summary              TaskSummary               `json:"summary"`
	Objective            string                    `json:"objective"`
	AcceptanceCriteria   []TaskAcceptanceCriterion `json:"acceptanceCriteria"`
	Dependencies         []TaskDependencyDetail    `json:"dependencies"`
	ConfigurationTarget  ConfigurationTarget       `json:"configurationTarget"`
	Configuration        []ConfigurationEntry      `json:"configuration"`
	ConfigurationPreview interface{}               `json:"configurationPreview"`
	Activity             []TaskActivity            `json:"activity"`
}

type TaskDetailSnapshot struct {
	SchemaVersion     int                  `json:"schemaVersion"`
	ContractVersion   string               `json:"contractVersion"`
	ContractHash      string               `json:"contractHash"`
	HostID            string               `json:"hostId"`
	Cursor            string               `json:"cursor"`
	Query             TaskDetailQueryInput `json:"query"`
	Detail            *TaskDetail          `json:"detail"`
	UnavailableReason *Explanation         `json:"unavailableReason"`
}

type HomeQueryInput struct {
	HostID   string  `json:"hostId"`
	Cursor   *string `json:"cursor"`
	PageSize int     `json:"pageSize"`
}

func ValidateHomeQuery(input HomeQueryInput) error {
	if !validOpaque(input.HostID) || input.PageSize < 1 || input.PageSize > MaximumHomePageSize ||
		(input.Cursor != nil && !validPageCursor(*input.Cursor)) {
		return ErrQueryInvalid
	}
	return nil
}

type HomeHost struct {
	ID               string `json:"id"`
	Label            string `json:"label"`
	InstanceID       string `json:"instanceId"`
	State            string `json:"state"`
	ObservedAt       string `json:"observedAt"`
	MaximumAgeMillis string `json:"maximumAgeMillis"`
}

type HomeWorkspace struct {
	ID               string  `json:"id"`
	Key              string  `json:"key"`
	Name             string  `json:"name"`
	Health           string  `json:"health"`
	PaseoWorkspaceID *string `json:"paseoWorkspaceId"`
}

type HomeOrganizer struct {
	ID                  string  `json:"id"`
	Mode                string  `json:"mode"`
	Phase               string  `json:"phase"`
	ConfigurationState  string  `json:"configurationState"`
	OrganizerRevision   *string `json:"organizerRevision"`
	ConfigurationSHA256 *string `json:"configurationSha256"`
	PaseoWorkspaceID    *string `json:"paseoWorkspaceId"`
}

type HomeLease struct {
	State     string  `json:"state"`
	Epoch     string  `json:"epoch"`
	ExpiresAt *string `json:"expiresAt"`
}

type HomeSync struct {
	State        string  `json:"state"`
	Git          string  `json:"git"`
	DynamicState string  `json:"dynamicState"`
	ObservedAt   *string `json:"observedAt"`
}

type HomeActiveWork struct {
	Building   string `json:"building"`
	Validating string `json:"validating"`
	InReview   string `json:"inReview"`
	Ready      string `json:"ready"`
	Total      string `json:"total"`
}

type HomeAttentionReason struct {
	Code  string `json:"code"`
	Count string `json:"count"`
}

type HomeAction struct {
	Kind              string         `json:"kind"`
	Label             string         `json:"label"`
	HostID            string         `json:"hostId"`
	ProjectID         *string        `json:"projectId"`
	Enabled           bool           `json:"enabled"`
	UnavailableReason *Explanation   `json:"unavailableReason"`
	PaseoWorkspaceID  *string        `json:"paseoWorkspaceId"`
	Command           *AllowedAction `json:"command"`
	Emphasis          string         `json:"emphasis"`
}

type HomeProject struct {
	ID              string                `json:"id"`
	Version         string                `json:"version"`
	Name            string                `json:"name"`
	State           string                `json:"state"`
	Health          string                `json:"health"`
	HealthReasons   []Explanation         `json:"healthReasons"`
	Workspaces      []HomeWorkspace       `json:"workspaces"`
	Organizer       HomeOrganizer         `json:"organizer"`
	Lease           HomeLease             `json:"lease"`
	Sync            HomeSync              `json:"sync"`
	Tasks           TaskCounts            `json:"tasks"`
	ActiveWork      HomeActiveWork        `json:"activeWork"`
	NeedsYouReasons []HomeAttentionReason `json:"needsYouReasons"`
	Actions         []HomeAction          `json:"actions"`
}

type HomeTotals struct {
	Projects   string `json:"projects"`
	Healthy    string `json:"healthy"`
	Degraded   string `json:"degraded"`
	Paused     string `json:"paused"`
	NeedsYou   string `json:"needsYou"`
	ActiveWork string `json:"activeWork"`
}

type HomePage struct {
	Host           HomeHost      `json:"host"`
	Projects       []HomeProject `json:"projects"`
	Totals         HomeTotals    `json:"totals"`
	SurfaceActions []HomeAction  `json:"surfaceActions"`
	TotalProjects  string        `json:"totalProjects"`
	NextCursor     *string       `json:"nextCursor"`
}

type HomeSnapshot struct {
	SchemaVersion   int      `json:"schemaVersion"`
	ContractVersion string   `json:"contractVersion"`
	ContractHash    string   `json:"contractHash"`
	Cursor          string   `json:"cursor"`
	Page            HomePage `json:"page"`
}

// OperationsQueryInput requests one exact Project's current operational
// projection. Audit and technical logs are always server-bounded; callers
// cannot request arbitrary history, paths, payloads, or raw output.
type OperationsQueryInput struct {
	HostID                 string `json:"hostId"`
	ProjectID              string `json:"projectId"`
	ExpectedProjectVersion string `json:"expectedProjectVersion"`
}

func ValidateOperationsQuery(input OperationsQueryInput) error {
	if !validOpaque(input.HostID) || !validOpaque(input.ProjectID) {
		return ErrQueryInvalid
	}
	if _, err := ParseExpectedVersion(input.ExpectedProjectVersion); err != nil {
		return ErrQueryInvalid
	}
	return nil
}

type SyncStreamDetail struct {
	Kind                      string  `json:"kind"`
	State                     string  `json:"state"`
	ReasonCode                string  `json:"reasonCode"`
	Detail                    string  `json:"detail"`
	LocalRevisionFingerprint  *string `json:"localRevisionFingerprint"`
	RemoteRevisionFingerprint *string `json:"remoteRevisionFingerprint"`
	ObservedAt                *string `json:"observedAt"`
	LastSuccessAt             *string `json:"lastSuccessAt"`
	Retryable                 bool    `json:"retryable"`
}

type OperationsSync struct {
	State                      string             `json:"state"`
	Streams                    []SyncStreamDetail `json:"streams"`
	AutomaticEnabled           bool               `json:"automaticEnabled"`
	DebounceMillis             string             `json:"debounceMillis"`
	FlushAtCriticalTransitions bool               `json:"flushAtCriticalTransitions"`
}

type OperationsReconciliation struct {
	Mode                    string  `json:"mode"`
	State                   string  `json:"state"`
	ReasonCode              string  `json:"reasonCode"`
	Detail                  string  `json:"detail"`
	TerminalEventDispatch   string  `json:"terminalEventDispatch"`
	TerminalTargetMillis    string  `json:"terminalTargetMillis"`
	ActiveIntervalMillis    string  `json:"activeIntervalMillis"`
	IdleIntervalMillis      string  `json:"idleIntervalMillis"`
	LostEventWatchdogMillis string  `json:"lostEventWatchdogMillis"`
	LastCompletedAt         *string `json:"lastCompletedAt"`
	PendingWakeups          string  `json:"pendingWakeups"`
}

// OperationsAuditEntry is derived only from immutable Event envelope fields.
// Raw payload, identifiers, paths, content, and actor claims are absent.
type OperationsAuditEntry struct {
	ID                 string  `json:"id"`
	Sequence           string  `json:"sequence"`
	Category           string  `json:"category"`
	Action             string  `json:"action"`
	Outcome            string  `json:"outcome"`
	ActorKind          string  `json:"actorKind"`
	ActorFingerprint   *string `json:"actorFingerprint"`
	SubjectFingerprint string  `json:"subjectFingerprint"`
	RunFingerprint     *string `json:"runFingerprint"`
	PayloadIncluded    bool    `json:"payloadIncluded"`
}

type OperationsAudit struct {
	Entries          []OperationsAuditEntry `json:"entries"`
	EntryLimit       string                 `json:"entryLimit"`
	Truncated        bool                   `json:"truncated"`
	PayloadsIncluded bool                   `json:"payloadsIncluded"`
	PathsIncluded    bool                   `json:"pathsIncluded"`
}

type OperationsLogEntry struct {
	Sequence    string `json:"sequence"`
	OccurredAt  string `json:"occurredAt"`
	Level       string `json:"level"`
	Component   string `json:"component"`
	Code        string `json:"code"`
	Message     string `json:"message"`
	Occurrences string `json:"occurrences"`
}

type OperationsLogs struct {
	State             string               `json:"state"`
	Reason            *Explanation         `json:"reason"`
	Entries           []OperationsLogEntry `json:"entries"`
	EntryLimit        string               `json:"entryLimit"`
	ReturnedBytes     string               `json:"returnedBytes"`
	RetentionDays     string               `json:"retentionDays"`
	RetentionBytes    string               `json:"retentionBytes"`
	Truncated         bool                 `json:"truncated"`
	RawOutputIncluded bool                 `json:"rawOutputIncluded"`
}

type SupportAvailability struct {
	PreviewAvailable bool         `json:"previewAvailable"`
	Reason           *Explanation `json:"reason"`
	UploadPolicy     string       `json:"uploadPolicy"`
}

type OperationsControlAvailability struct {
	SyncAvailable      bool         `json:"syncAvailable"`
	ReconcileAvailable bool         `json:"reconcileAvailable"`
	Reason             *Explanation `json:"reason"`
}

type OperationsReport struct {
	SchemaVersion    int                           `json:"schemaVersion"`
	ContractVersion  string                        `json:"contractVersion"`
	ContractHash     string                        `json:"contractHash"`
	Cursor           string                        `json:"cursor"`
	HostID           string                        `json:"hostId"`
	HostInstanceID   string                        `json:"hostInstanceId"`
	ProjectID        string                        `json:"projectId"`
	ProjectName      string                        `json:"projectName"`
	ProjectVersion   string                        `json:"projectVersion"`
	ObservationID    string                        `json:"observationId"`
	ObservedAt       string                        `json:"observedAt"`
	MaximumAgeMillis string                        `json:"maximumAgeMillis"`
	Status           string                        `json:"status"`
	Reasons          []Explanation                 `json:"reasons"`
	Hybrid           OperationsReconciliation      `json:"hybrid"`
	Sync             OperationsSync                `json:"sync"`
	Audit            OperationsAudit               `json:"audit"`
	Logs             OperationsLogs                `json:"logs"`
	Controls         OperationsControlAvailability `json:"controls"`
	Support          SupportAvailability           `json:"support"`
}

type OperationsMutationInput struct {
	SchemaVersion          int     `json:"schemaVersion"`
	ContractVersion        string  `json:"contractVersion"`
	ContractHash           string  `json:"contractHash"`
	HostID                 string  `json:"hostId"`
	RequestID              string  `json:"requestId"`
	Kind                   string  `json:"kind"`
	ProjectID              string  `json:"projectId"`
	ExpectedProjectVersion string  `json:"expectedProjectVersion"`
	PreviewID              *string `json:"previewId"`
	Confirmed              bool    `json:"confirmed"`
}

func ValidateOperationsMutation(input OperationsMutationInput) error {
	hash, err := SchemaSHA256()
	if err != nil || input.SchemaVersion != 1 || input.ContractVersion != "director-planning/v1" || input.ContractHash != hash ||
		!validOpaque(input.HostID) || !validOpaque(input.ProjectID) || len(input.RequestID) < 16 || !validOpaque(input.RequestID) {
		return ErrQueryInvalid
	}
	if _, err := ParseExpectedVersion(input.ExpectedProjectVersion); err != nil {
		return ErrQueryInvalid
	}
	switch input.Kind {
	case "support.preview":
		if input.PreviewID != nil || input.Confirmed {
			return ErrQueryInvalid
		}
	case "support.generate":
		if input.PreviewID == nil || !validSHA256(*input.PreviewID) || !input.Confirmed {
			return ErrQueryInvalid
		}
	case "sync.now", "reconcile.now":
		if input.PreviewID != nil || !input.Confirmed {
			return ErrQueryInvalid
		}
	default:
		return ErrQueryInvalid
	}
	return nil
}

type SupportBundleItem struct {
	Name        string `json:"name"`
	Description string `json:"description"`
}

type SupportBundlePreview struct {
	ID                 string              `json:"id"`
	RequestID          string              `json:"requestId"`
	ProjectFingerprint string              `json:"projectFingerprint"`
	Cursor             string              `json:"cursor"`
	ProjectVersion     string              `json:"projectVersion"`
	Items              []SupportBundleItem `json:"items"`
	Excluded           []string            `json:"excluded"`
	EstimatedBytes     string              `json:"estimatedBytes"`
	ScanStatus         string              `json:"scanStatus"`
	LocalOnly          bool                `json:"localOnly"`
	UploadPolicy       string              `json:"uploadPolicy"`
	OutputFileName     string              `json:"outputFileName"`
	Valid              bool                `json:"valid"`
	Issues             []Explanation       `json:"issues"`
	Confirmation       string              `json:"confirmation"`
}

type SupportBundleResult struct {
	BundleID        string `json:"bundleId"`
	FileName        string `json:"fileName"`
	SHA256          string `json:"sha256"`
	Bytes           string `json:"bytes"`
	Permission      string `json:"permission"`
	GeneratedAt     string `json:"generatedAt"`
	UploadAttempted bool   `json:"uploadAttempted"`
}

type OperationsEffectResult struct {
	Kind                  string `json:"kind"`
	EffectClass           string `json:"effectClass"`
	Outcome               string `json:"outcome"`
	GitState              string `json:"gitState"`
	DynamicState          string `json:"dynamicState"`
	SuccessfulHalfRetried bool   `json:"successfulHalfRetried"`
}

type OperationsMutationResult struct {
	SchemaVersion   int                     `json:"schemaVersion"`
	ContractVersion string                  `json:"contractVersion"`
	ContractHash    string                  `json:"contractHash"`
	HostID          string                  `json:"hostId"`
	ProjectID       string                  `json:"projectId"`
	Cursor          string                  `json:"cursor"`
	RequestID       string                  `json:"requestId"`
	Status          string                  `json:"status"`
	Message         string                  `json:"message"`
	SupportPreview  *SupportBundlePreview   `json:"supportPreview"`
	Bundle          *SupportBundleResult    `json:"bundle"`
	Effect          *OperationsEffectResult `json:"effect"`
	RefusalCode     *string                 `json:"refusalCode"`
}

type DoctorQueryInput struct {
	HostID                 string `json:"hostId"`
	ProjectID              string `json:"projectId"`
	ExpectedProjectVersion string `json:"expectedProjectVersion"`
}

func ValidateDoctorQuery(input DoctorQueryInput) error {
	if !validOpaque(input.HostID) || !validOpaque(input.ProjectID) {
		return ErrQueryInvalid
	}
	if _, err := ParseExpectedVersion(input.ExpectedProjectVersion); err != nil {
		return ErrQueryInvalid
	}
	return nil
}

type DoctorCheck struct {
	ID                   string   `json:"id"`
	Category             string   `json:"category"`
	Status               string   `json:"status"`
	Code                 string   `json:"code"`
	Title                string   `json:"title"`
	Detail               string   `json:"detail"`
	Blocking             bool     `json:"blocking"`
	MissingCapability    *string  `json:"missingCapability"`
	InstallationGuidance []string `json:"installationGuidance"`
}

type DoctorRepairAvailability struct {
	Available bool         `json:"available"`
	Reason    *Explanation `json:"reason"`
}

type DoctorReport struct {
	SchemaVersion    int                      `json:"schemaVersion"`
	ContractVersion  string                   `json:"contractVersion"`
	ContractHash     string                   `json:"contractHash"`
	Cursor           string                   `json:"cursor"`
	HostID           string                   `json:"hostId"`
	HostInstanceID   string                   `json:"hostInstanceId"`
	ProjectID        string                   `json:"projectId"`
	ProjectName      string                   `json:"projectName"`
	ProjectVersion   string                   `json:"projectVersion"`
	ObservationID    string                   `json:"observationId"`
	ObservedAt       string                   `json:"observedAt"`
	MaximumAgeMillis string                   `json:"maximumAgeMillis"`
	Status           string                   `json:"status"`
	ReadOnly         bool                     `json:"readOnly"`
	Assurance        string                   `json:"assurance"`
	Checks           []DoctorCheck            `json:"checks"`
	BlockingCount    string                   `json:"blockingCount"`
	Repair           DoctorRepairAvailability `json:"repair"`
}

type RepairInput struct {
	SchemaVersion          int     `json:"schemaVersion"`
	ContractVersion        string  `json:"contractVersion"`
	ContractHash           string  `json:"contractHash"`
	HostID                 string  `json:"hostId"`
	RequestID              string  `json:"requestId"`
	Kind                   string  `json:"kind"`
	ProjectID              string  `json:"projectId"`
	ExpectedProjectVersion string  `json:"expectedProjectVersion"`
	PreviewID              *string `json:"previewId"`
	Confirmed              bool    `json:"confirmed"`
}

func ValidateRepairInput(input RepairInput) error {
	hash, err := SchemaSHA256()
	if err != nil || input.SchemaVersion != 1 || input.ContractVersion != "director-planning/v1" || input.ContractHash != hash ||
		!validOpaque(input.HostID) || len(input.RequestID) < 16 || !validOpaque(input.RequestID) || !validOpaque(input.ProjectID) {
		return ErrQueryInvalid
	}
	if _, err := ParseExpectedVersion(input.ExpectedProjectVersion); err != nil {
		return ErrQueryInvalid
	}
	preview := input.Kind == "repair.preview"
	apply := input.Kind == "repair.apply"
	if (!preview && !apply) || preview == (input.PreviewID != nil) || preview == input.Confirmed ||
		(apply && (input.PreviewID == nil || !validSHA256(*input.PreviewID) || !input.Confirmed)) {
		return ErrQueryInvalid
	}
	return nil
}

type RepairOperation struct {
	ID               string `json:"id"`
	Kind             string `json:"kind"`
	Description      string `json:"description"`
	AffectedResource string `json:"affectedResource"`
	EffectClass      string `json:"effectClass"`
	Destructive      bool   `json:"destructive"`
	AutomaticInstall bool   `json:"automaticInstall"`
}

type RepairPreview struct {
	ID             string            `json:"id"`
	RequestID      string            `json:"requestId"`
	HostID         string            `json:"hostId"`
	HostInstanceID string            `json:"hostInstanceId"`
	ProjectID      string            `json:"projectId"`
	ProjectName    string            `json:"projectName"`
	ProjectVersion string            `json:"projectVersion"`
	Cursor         string            `json:"cursor"`
	ObservationID  string            `json:"observationId"`
	Operations     []RepairOperation `json:"operations"`
	Valid          bool              `json:"valid"`
	Issues         []Explanation     `json:"issues"`
	Confirmation   string            `json:"confirmation"`
}

type RepairResult struct {
	SchemaVersion   int            `json:"schemaVersion"`
	ContractVersion string         `json:"contractVersion"`
	ContractHash    string         `json:"contractHash"`
	HostID          string         `json:"hostId"`
	ProjectID       string         `json:"projectId"`
	Cursor          string         `json:"cursor"`
	RequestID       string         `json:"requestId"`
	Status          string         `json:"status"`
	Message         string         `json:"message"`
	Preview         *RepairPreview `json:"preview"`
	ProjectVersion  *string        `json:"projectVersion"`
	RefusalCode     *string        `json:"refusalCode"`
}

type OrganizerBootstrapInput struct {
	SchemaVersion     int     `json:"schemaVersion"`
	ContractVersion   string  `json:"contractVersion"`
	ContractHash      string  `json:"contractHash"`
	HostID            string  `json:"hostId"`
	RequestID         string  `json:"requestId"`
	Kind              string  `json:"kind"`
	ProjectID         string  `json:"projectId"`
	ProjectName       string  `json:"projectName"`
	RepositoryPath    string  `json:"repositoryPath"`
	ConfigurationJSON *string `json:"configurationJson"`
	PreviewID         *string `json:"previewId"`
}

func ValidateOrganizerBootstrap(input OrganizerBootstrapInput) error {
	hash, err := SchemaSHA256()
	if err != nil || input.SchemaVersion != 1 || input.ContractVersion != "director-planning/v1" || input.ContractHash != hash ||
		!validOpaque(input.HostID) || len(input.RequestID) < 16 || !validOpaque(input.RequestID) || !validOpaque(input.ProjectID) ||
		input.ProjectName == "" || len(input.ProjectName) > 512 || input.ProjectName != strings.TrimSpace(input.ProjectName) ||
		len(input.RepositoryPath) < 2 || len(input.RepositoryPath) > 4096 || input.RepositoryPath != strings.TrimSpace(input.RepositoryPath) {
		return ErrQueryInvalid
	}
	isCreate := input.Kind == "create.preview" || input.Kind == "create.apply"
	isApply := input.Kind == "create.apply" || input.Kind == "adopt.apply"
	if (!isCreate && input.Kind != "adopt.preview" && input.Kind != "adopt.apply") ||
		isCreate != (input.ConfigurationJSON != nil) || (input.ConfigurationJSON != nil && (len(*input.ConfigurationJSON) < 2 || len(*input.ConfigurationJSON) > 60000)) ||
		isApply != (input.PreviewID != nil) || (input.PreviewID != nil && !validSHA256(*input.PreviewID)) {
		return ErrQueryInvalid
	}
	return nil
}

func validSHA256(value string) bool {
	if len(value) != 64 {
		return false
	}
	return strings.IndexFunc(value, func(character rune) bool {
		return !((character >= '0' && character <= '9') || (character >= 'a' && character <= 'f'))
	}) < 0
}

type OrganizerPreviewFile struct {
	Path   string `json:"path"`
	SHA256 string `json:"sha256"`
}

type OrganizerPreviewIssue struct {
	Code    string `json:"code"`
	Field   string `json:"field"`
	Message string `json:"message"`
}

type OrganizerBootstrapPreview struct {
	ID                  string                  `json:"id"`
	Kind                string                  `json:"kind"`
	RequestID           string                  `json:"requestId"`
	ProjectID           string                  `json:"projectId"`
	ProjectName         string                  `json:"projectName"`
	RepositoryPath      string                  `json:"repositoryPath"`
	OrganizerRevision   *string                 `json:"organizerRevision"`
	ConfigurationSHA256 *string                 `json:"configurationSha256"`
	Files               []OrganizerPreviewFile  `json:"files"`
	Operations          []string                `json:"operations"`
	Valid               bool                    `json:"valid"`
	Issues              []OrganizerPreviewIssue `json:"issues"`
}

type OrganizerBootstrapResult struct {
	SchemaVersion   int                        `json:"schemaVersion"`
	ContractVersion string                     `json:"contractVersion"`
	ContractHash    string                     `json:"contractHash"`
	HostID          string                     `json:"hostId"`
	Cursor          string                     `json:"cursor"`
	RequestID       string                     `json:"requestId"`
	Status          string                     `json:"status"`
	Message         string                     `json:"message"`
	Preview         *OrganizerBootstrapPreview `json:"preview"`
	ProjectVersion  *string                    `json:"projectVersion"`
}
