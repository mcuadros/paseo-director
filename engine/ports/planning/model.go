// SPDX-License-Identifier: Apache-2.0

package planning

import (
	"errors"
	"strings"
	"unicode"
	"unicode/utf8"
)

const (
	QueryPath            = "/v1/planning/query"
	MaximumRequestBytes  = 64 * 1024
	MaximumResponseBytes = 4 * 1024 * 1024
	MaximumPageSize      = 100
	MaximumProjects      = 100
	MaximumWorkspaces    = 100
	MaximumEpics         = 500
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
