// SPDX-License-Identifier: Apache-2.0

package domain

import (
	"errors"
	"fmt"
	"sort"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/mcuadros/director-engine/domain/safedata"
)

const (
	MaximumPlanningTitleBytes       = 512
	MaximumPlanningDescriptionBytes = 16 * 1024
	MaximumPlanningLabelBytes       = 128
	MaximumExternalReferenceBytes   = 512
)

var ErrInvalidPlanning = errors.New("planning model is invalid")

// PlanningNodeKind is the closed dependency and hierarchy endpoint
// vocabulary. Project and Workspace kinds are retained only so untrusted
// planning input can be rejected with a precise code.
type PlanningNodeKind string

const (
	PlanningNodeProject   PlanningNodeKind = "project"
	PlanningNodeWorkspace PlanningNodeKind = "workspace"
	PlanningNodeEpic      PlanningNodeKind = "epic"
	PlanningNodeTask      PlanningNodeKind = "task"
)

// PlanningNodeRef identifies one canonical planning object. External
// reference provider keys use a different type and cannot become endpoints.
type PlanningNodeRef struct {
	Kind PlanningNodeKind `json:"kind"`
	ID   string           `json:"id"`
}

// Priority is the closed Task ordering vocabulary. The empty value is
// accepted at proposal boundaries and has the required normal default.
type Priority string

const (
	PriorityUrgent Priority = "urgent"
	PriorityHigh   Priority = "high"
	PriorityNormal Priority = "normal"
	PriorityLow    Priority = "low"
)

// EffectivePriority applies the PLAN default without mutating a proposal.
func EffectivePriority(priority Priority) Priority {
	if priority == "" {
		return PriorityNormal
	}
	return priority
}

// ExternalReference is contextual metadata owned by a Task. Provider and Key
// are deliberately not PlanningNodeRef fields.
type ExternalReference struct {
	Provider string `json:"provider"`
	Key      string `json:"key"`
}

// PlanningDependency states that From depends on On.
type PlanningDependency struct {
	From PlanningNodeRef `json:"from"`
	On   PlanningNodeRef `json:"on"`
}

// PlanningActorKind is closed because dependency release before completion is
// reserved for an authenticated human action.
type PlanningActorKind string

const (
	PlanningActorHuman     PlanningActorKind = "human"
	PlanningActorAgent     PlanningActorKind = "agent"
	PlanningActorConnector PlanningActorKind = "connector"
)

// DependencyOverride is an immutable audited decision bound to one Task
// version and one exact dependency edge. Invalid or stale historical facts are
// retained but never release the Task.
type DependencyOverride struct {
	ID              string             `json:"id"`
	TaskID          string             `json:"taskId"`
	TaskVersion     uint64             `json:"taskVersion"`
	Dependency      PlanningDependency `json:"dependency"`
	ActorKind       PlanningActorKind  `json:"actorKind"`
	ActorID         string             `json:"actorId"`
	AuditID         string             `json:"auditId"`
	Granted         bool               `json:"granted"`
	GrantedAtMillis int64              `json:"grantedAtMillis"`
}

// HumanDependencyOverrideGrant is the typed input to the engine-owned
// TaskStore operation. ActorKind, Granted, the Task version, and server time
// are assigned from current durable facts rather than accepted from a model or
// connector payload.
type HumanDependencyOverrideGrant struct {
	ID                  string             `json:"id"`
	TaskID              string             `json:"taskId"`
	ExpectedTaskVersion uint64             `json:"expectedTaskVersion"`
	Dependency          PlanningDependency `json:"dependency"`
	HumanActorID        string             `json:"humanActorId"`
	AuditID             string             `json:"auditId"`
}

// Epic is the only grouping aggregate. Parent exists at the validation
// boundary solely to make an attempted third planning level explicitly
// rejectable; every persisted Epic has Parent nil.
type Epic struct {
	ID           string
	ProjectID    string
	Key          string
	Title        string
	Description  string
	Parent       *PlanningNodeRef
	Complete     bool
	Priority     Priority
	Labels       []string
	Dependencies []PlanningDependency
	Version      uint64
}

// PlanningProject is the complete pure input for hierarchy, dependency, and
// override evaluation. Project and Workspace aggregates remain independently
// owned records; this value contains only their stable identities.
type PlanningProject struct {
	ID         string
	Workspaces []string
	Epics      []Epic
	Tasks      []Task
	Overrides  []DependencyOverride
}

// PlanningCode is the closed machine-readable validation and blocking
// vocabulary shared by engine callers. It intentionally contains no prose or
// external data.
type PlanningCode string

const (
	PlanningProjectIDMissing                 PlanningCode = "project_id_missing"
	PlanningWorkspaceRequired                PlanningCode = "workspace_required"
	PlanningWorkspaceIDMissing               PlanningCode = "workspace_id_missing"
	PlanningWorkspaceIDDuplicate             PlanningCode = "workspace_id_duplicate"
	PlanningEpicIDMissing                    PlanningCode = "epic_id_missing"
	PlanningEpicIDDuplicate                  PlanningCode = "epic_id_duplicate"
	PlanningTaskIDMissing                    PlanningCode = "task_id_missing"
	PlanningTaskIDDuplicate                  PlanningCode = "task_id_duplicate"
	PlanningTaskWorkspaceCount               PlanningCode = "task_workspace_count_invalid"
	PlanningTaskWorkspaceUnknown             PlanningCode = "task_workspace_unknown"
	PlanningHierarchyThirdLevel              PlanningCode = "hierarchy_third_level_forbidden"
	PlanningTaskEpicUnknown                  PlanningCode = "task_epic_unknown"
	PlanningExternalReferenceProviderMissing PlanningCode = "external_reference_provider_missing"
	PlanningExternalReferenceKeyMissing      PlanningCode = "external_reference_key_missing"
	PlanningDependencyKindInvalid            PlanningCode = "dependency_kind_invalid"
	PlanningDependencyEndpointMissing        PlanningCode = "dependency_endpoint_missing"
	PlanningDependencyEndpointUnknown        PlanningCode = "dependency_endpoint_unknown"
	PlanningDependencyDuplicate              PlanningCode = "dependency_duplicate"
	PlanningDependencySelf                   PlanningCode = "dependency_self"
	PlanningDependencyCycle                  PlanningCode = "dependency_cycle"
	PlanningOverrideIDMissing                PlanningCode = "dependency_override_id_missing"
	PlanningOverrideIDDuplicate              PlanningCode = "dependency_override_id_duplicate"
	PlanningOverrideBindingUnknown           PlanningCode = "dependency_override_binding_unknown"
	PlanningOverrideMissing                  PlanningCode = "dependency_override_missing"
	PlanningOverrideStale                    PlanningCode = "dependency_override_stale"
	PlanningOverrideAmbiguous                PlanningCode = "dependency_override_ambiguous"
	PlanningOverrideActorNotHuman            PlanningCode = "dependency_override_actor_not_human"
	PlanningOverrideActorMissing             PlanningCode = "dependency_override_actor_missing"
	PlanningOverrideAuditMissing             PlanningCode = "dependency_override_audit_missing"
	PlanningOverrideNotGranted               PlanningCode = "dependency_override_not_granted"
	PlanningInputInvalid                     PlanningCode = "planning_input_invalid"
	PlanningTaskDependencyBlocked            PlanningCode = "task_dependency_blocked"
	PlanningTaskDependencyBlockedTransitive  PlanningCode = "task_dependency_blocked_transitive"
	PlanningEpicDependencyBlocked            PlanningCode = "epic_dependency_blocked"
	PlanningEpicDependencyBlockedTransitive  PlanningCode = "epic_dependency_blocked_transitive"
)

// PlanningExplanation binds a closed code to exact canonical records.
type PlanningExplanation struct {
	Code       PlanningCode
	Subject    PlanningNodeRef
	Related    []PlanningNodeRef
	OverrideID string
}

// TaskDependencyResult is the deterministic dependency outcome for one Task.
// It is planning input for later eligibility work, not an M2.4 Board state.
type TaskDependencyResult struct {
	TaskID       string
	Blocked      bool
	Explanations []PlanningExplanation
}

// PlanningReport contains structural validity, rejected override facts, and
// per-Task dependency results.
type PlanningReport struct {
	Valid    bool
	Findings []PlanningExplanation
	Tasks    []TaskDependencyResult
}

// HasCode reports whether a code appears anywhere in the report.
func (report PlanningReport) HasCode(code PlanningCode) bool {
	for _, finding := range report.Findings {
		if finding.Code == code {
			return true
		}
	}
	for _, task := range report.Tasks {
		for _, explanation := range task.Explanations {
			if explanation.Code == code {
				return true
			}
		}
	}
	return false
}

// Result returns the dependency outcome for one canonical Task.
func (report PlanningReport) Result(taskID string) (TaskDependencyResult, bool) {
	index := sort.Search(len(report.Tasks), func(index int) bool { return report.Tasks[index].TaskID >= taskID })
	if index == len(report.Tasks) || report.Tasks[index].TaskID != taskID {
		return TaskDependencyResult{}, false
	}
	return report.Tasks[index], true
}

// PlanningValidationError exposes only deterministic bounded explanations.
type PlanningValidationError struct {
	Findings []PlanningExplanation
}

func (failure *PlanningValidationError) Error() string {
	if len(failure.Findings) == 0 {
		return ErrInvalidPlanning.Error()
	}
	return fmt.Sprintf("%s: %s", ErrInvalidPlanning, failure.Findings[0].Code)
}

func (failure *PlanningValidationError) Unwrap() error { return ErrInvalidPlanning }

// ValidatePlanning rejects structural hierarchy and dependency errors. Stale,
// ambiguous, or unauthorized override facts remain auditable findings and are
// rejected by dependency evaluation rather than corrupting the graph.
func ValidatePlanning(project PlanningProject) error {
	report := EvaluatePlanning(project)
	if report.Valid {
		return nil
	}
	return &PlanningValidationError{Findings: append([]PlanningExplanation(nil), report.Findings...)}
}

type planningIndex struct {
	project      PlanningProject
	workspaces   map[string]struct{}
	epics        map[string]Epic
	tasks        map[string]Task
	dependencies []PlanningDependency
	adjacency    map[PlanningNodeRef][]PlanningDependency
	edges        map[string]PlanningDependency
}

func boundedPlanningText(value string, maximum int) bool {
	return len(value) > 0 && len(value) <= maximum && utf8.ValidString(value) &&
		value == strings.TrimSpace(value) && strings.IndexFunc(value, unicode.IsControl) < 0 &&
		safedata.ClassifyText(value, true) == safedata.Safe
}

func validPriority(priority Priority, optional bool) bool {
	if priority == "" {
		return optional || EffectivePriority(priority) == PriorityNormal
	}
	return priority == PriorityUrgent || priority == PriorityHigh || priority == PriorityNormal || priority == PriorityLow
}

func validLabels(labels []string) bool {
	seen := make(map[string]struct{}, len(labels))
	for _, label := range labels {
		if !boundedPlanningText(label, MaximumPlanningLabelBytes) {
			return false
		}
		if _, duplicate := seen[label]; duplicate {
			return false
		}
		seen[label] = struct{}{}
	}
	return true
}

// ValidateEpic checks intrinsic aggregate shape. Cross-record endpoints and
// cycles require ValidatePlanning over the complete Project graph.
func ValidateEpic(epic Epic) error {
	reference := PlanningNodeRef{Kind: PlanningNodeEpic, ID: epic.ID}
	if !stableIdentity(epic.ID, 128) || !stableIdentity(epic.ProjectID, 128) ||
		!boundedPlanningText(defaultPlanningKey(epic.Key, epic.ID), 128) ||
		!boundedPlanningText(epic.Title, MaximumPlanningTitleBytes) ||
		(epic.Description != "" && !boundedPlanningText(epic.Description, MaximumPlanningDescriptionBytes)) ||
		epic.Parent != nil || !validPriority(epic.Priority, true) || !validLabels(epic.Labels) {
		return ErrInvalidPlanning
	}
	seen := make(map[string]struct{}, len(epic.Dependencies))
	for _, dependency := range epic.Dependencies {
		if dependency.From != reference || dependency.On.Kind != PlanningNodeEpic ||
			!stableIdentity(dependency.On.ID, 128) || dependency.From == dependency.On {
			return ErrInvalidPlanning
		}
		key := dependencyKey(dependency)
		if _, duplicate := seen[key]; duplicate {
			return ErrInvalidPlanning
		}
		seen[key] = struct{}{}
	}
	return nil
}

// ValidateTask checks intrinsic Task shape, including exactly one Workspace
// binding. Cross-record hierarchy/dependency endpoints require
// ValidatePlanning over the complete Project graph.
func ValidateTask(task Task) error {
	reference := PlanningNodeRef{Kind: PlanningNodeTask, ID: task.ID}
	if !stableIdentity(task.ID, 128) || !stableIdentity(task.ProjectID, 128) ||
		!boundedPlanningText(defaultPlanningKey(task.Key, task.ID), 128) ||
		!boundedPlanningText(task.Title, MaximumPlanningTitleBytes) ||
		!boundedPlanningText(task.Objective, MaximumPlanningDescriptionBytes) ||
		!boundedPlanningText(task.AcceptanceCriteria, MaximumPlanningDescriptionBytes) ||
		len(task.WorkspaceIDs) != 1 || !stableIdentity(task.WorkspaceIDs[0], 128) ||
		task.QueuedAtUnixMillis < 0 ||
		!validPriority(task.Priority, false) || !validLabels(task.Labels) {
		return ErrInvalidPlanning
	}
	if task.Parent != nil && (task.Parent.Kind != PlanningNodeEpic || !stableIdentity(task.Parent.ID, 128)) {
		return ErrInvalidPlanning
	}
	seen := make(map[string]struct{}, len(task.Dependencies))
	for _, dependency := range task.Dependencies {
		if dependency.From != reference ||
			(dependency.On.Kind != PlanningNodeEpic && dependency.On.Kind != PlanningNodeTask) ||
			!stableIdentity(dependency.On.ID, 128) || dependency.From == dependency.On {
			return ErrInvalidPlanning
		}
		key := dependencyKey(dependency)
		if _, duplicate := seen[key]; duplicate {
			return ErrInvalidPlanning
		}
		seen[key] = struct{}{}
	}
	for _, external := range task.ExternalReferences {
		if !boundedPlanningText(external.Provider, MaximumPlanningLabelBytes) ||
			!boundedPlanningText(external.Key, MaximumExternalReferenceBytes) {
			return ErrInvalidPlanning
		}
	}
	return nil
}

// ValidateDependencyOverride checks one immutable stored override fact. It
// does not decide freshness or whether the edge still affects the Task.
func ValidateDependencyOverride(override DependencyOverride) error {
	if !stableIdentity(override.ID, 128) || !stableIdentity(override.TaskID, 128) ||
		!validNodeKind(override.Dependency.From.Kind) ||
		!validNodeKind(override.Dependency.On.Kind) || !stableIdentity(override.Dependency.From.ID, 128) ||
		!stableIdentity(override.Dependency.On.ID, 128) || override.Dependency.From == override.Dependency.On ||
		override.ActorKind != PlanningActorHuman || !boundedPlanningText(override.ActorID, 256) ||
		!stableIdentity(override.AuditID, 128) || !override.Granted || override.GrantedAtMillis <= 0 {
		return ErrInvalidPlanning
	}
	return nil
}

func refKey(reference PlanningNodeRef) string {
	return string(reference.Kind) + "\x1f" + reference.ID
}

func dependencyKey(dependency PlanningDependency) string {
	return refKey(dependency.From) + "\x1e" + refKey(dependency.On)
}

func overrideBindingKey(taskID string, taskVersion uint64, dependency PlanningDependency) string {
	return fmt.Sprintf("%s\x1d%d\x1d%s", taskID, taskVersion, dependencyKey(dependency))
}

func overrideTaskEdgeKey(taskID string, dependency PlanningDependency) string {
	return taskID + "\x1d" + dependencyKey(dependency)
}

func refLess(left, right PlanningNodeRef) bool {
	if left.Kind != right.Kind {
		return left.Kind < right.Kind
	}
	return left.ID < right.ID
}

func dependencyLess(left, right PlanningDependency) bool {
	if left.From != right.From {
		return refLess(left.From, right.From)
	}
	return refLess(left.On, right.On)
}

func explanationLess(left, right PlanningExplanation) bool {
	if left.Code != right.Code {
		return left.Code < right.Code
	}
	if left.Subject != right.Subject {
		return refLess(left.Subject, right.Subject)
	}
	leftRelated, rightRelated := "", ""
	if len(left.Related) > 0 {
		leftRelated = refKey(left.Related[0])
	}
	if len(right.Related) > 0 {
		rightRelated = refKey(right.Related[0])
	}
	if leftRelated != rightRelated {
		return leftRelated < rightRelated
	}
	return left.OverrideID < right.OverrideID
}

func structuralFinding(code PlanningCode, subject PlanningNodeRef, related ...PlanningNodeRef) PlanningExplanation {
	return PlanningExplanation{Code: code, Subject: subject, Related: append([]PlanningNodeRef(nil), related...)}
}

func allDependencies(epics []Epic, tasks []Task) []PlanningDependency {
	dependencies := make([]PlanningDependency, 0)
	for _, epic := range epics {
		dependencies = append(dependencies, epic.Dependencies...)
	}
	for _, task := range tasks {
		dependencies = append(dependencies, task.Dependencies...)
	}
	sort.Slice(dependencies, func(left, right int) bool { return dependencyLess(dependencies[left], dependencies[right]) })
	return dependencies
}

func validNodeKind(kind PlanningNodeKind) bool {
	return kind == PlanningNodeEpic || kind == PlanningNodeTask
}

func (index planningIndex) nodeExists(reference PlanningNodeRef) bool {
	switch reference.Kind {
	case PlanningNodeEpic:
		_, ok := index.epics[reference.ID]
		return ok
	case PlanningNodeTask:
		_, ok := index.tasks[reference.ID]
		return ok
	default:
		return false
	}
}

func (index planningIndex) nodeComplete(reference PlanningNodeRef) bool {
	switch reference.Kind {
	case PlanningNodeEpic:
		return index.epics[reference.ID].Complete
	case PlanningNodeTask:
		return index.tasks[reference.ID].Complete
	default:
		return false
	}
}

func buildPlanningIndex(project PlanningProject) (planningIndex, []PlanningExplanation) {
	index := planningIndex{
		project: project, workspaces: make(map[string]struct{}), epics: make(map[string]Epic),
		tasks: make(map[string]Task), adjacency: make(map[PlanningNodeRef][]PlanningDependency),
		edges: make(map[string]PlanningDependency),
	}
	findings := make([]PlanningExplanation, 0)
	projectRef := PlanningNodeRef{Kind: PlanningNodeProject, ID: project.ID}
	if !stableIdentity(project.ID, 128) {
		findings = append(findings, structuralFinding(PlanningProjectIDMissing, projectRef))
	}
	if len(project.Workspaces) == 0 {
		findings = append(findings, structuralFinding(PlanningWorkspaceRequired, projectRef))
	}
	workspaces := append([]string(nil), project.Workspaces...)
	sort.Strings(workspaces)
	for _, workspaceID := range workspaces {
		reference := PlanningNodeRef{Kind: PlanningNodeWorkspace, ID: workspaceID}
		if !stableIdentity(workspaceID, 128) {
			findings = append(findings, structuralFinding(PlanningWorkspaceIDMissing, reference))
			continue
		}
		if _, duplicate := index.workspaces[workspaceID]; duplicate {
			findings = append(findings, structuralFinding(PlanningWorkspaceIDDuplicate, reference))
			continue
		}
		index.workspaces[workspaceID] = struct{}{}
	}

	epics := append([]Epic(nil), project.Epics...)
	sort.Slice(epics, func(left, right int) bool { return epics[left].ID < epics[right].ID })
	for _, epic := range epics {
		reference := PlanningNodeRef{Kind: PlanningNodeEpic, ID: epic.ID}
		if !stableIdentity(epic.ID, 128) {
			findings = append(findings, structuralFinding(PlanningEpicIDMissing, reference))
			continue
		}
		if _, duplicate := index.epics[epic.ID]; duplicate {
			findings = append(findings, structuralFinding(PlanningEpicIDDuplicate, reference))
			continue
		}
		index.epics[epic.ID] = epic
		if epic.ProjectID != project.ID || !boundedPlanningText(defaultPlanningKey(epic.Key, epic.ID), 128) ||
			!boundedPlanningText(epic.Title, MaximumPlanningTitleBytes) ||
			(epic.Description != "" && !boundedPlanningText(epic.Description, MaximumPlanningDescriptionBytes)) ||
			!validPriority(epic.Priority, true) || !validLabels(epic.Labels) {
			findings = append(findings, structuralFinding(PlanningInputInvalid, reference))
		}
		if epic.Parent != nil {
			findings = append(findings, structuralFinding(PlanningHierarchyThirdLevel, reference, *epic.Parent))
		}
	}

	tasks := append([]Task(nil), project.Tasks...)
	sort.Slice(tasks, func(left, right int) bool { return tasks[left].ID < tasks[right].ID })
	for _, task := range tasks {
		reference := PlanningNodeRef{Kind: PlanningNodeTask, ID: task.ID}
		if !stableIdentity(task.ID, 128) {
			findings = append(findings, structuralFinding(PlanningTaskIDMissing, reference))
			continue
		}
		if _, duplicate := index.tasks[task.ID]; duplicate {
			findings = append(findings, structuralFinding(PlanningTaskIDDuplicate, reference))
			continue
		}
		index.tasks[task.ID] = task
		if task.ProjectID != project.ID || !boundedPlanningText(defaultPlanningKey(task.Key, task.ID), 128) ||
			!boundedPlanningText(task.Title, MaximumPlanningTitleBytes) ||
			!boundedPlanningText(task.Objective, MaximumPlanningDescriptionBytes) ||
			!boundedPlanningText(task.AcceptanceCriteria, MaximumPlanningDescriptionBytes) ||
			task.QueuedAtUnixMillis < 0 ||
			!validPriority(task.Priority, false) || !validLabels(task.Labels) {
			findings = append(findings, structuralFinding(PlanningInputInvalid, reference))
		}
		if len(task.WorkspaceIDs) != 1 {
			findings = append(findings, structuralFinding(PlanningTaskWorkspaceCount, reference))
		} else if _, known := index.workspaces[task.WorkspaceIDs[0]]; !known {
			findings = append(findings, structuralFinding(PlanningTaskWorkspaceUnknown, reference,
				PlanningNodeRef{Kind: PlanningNodeWorkspace, ID: task.WorkspaceIDs[0]}))
		}
		if task.Parent != nil {
			switch {
			case task.Parent.Kind != PlanningNodeEpic:
				findings = append(findings, structuralFinding(PlanningHierarchyThirdLevel, reference, *task.Parent))
			case index.epics[task.Parent.ID].ID == "":
				findings = append(findings, structuralFinding(PlanningTaskEpicUnknown, reference, *task.Parent))
			}
		}
		for _, external := range task.ExternalReferences {
			if !boundedPlanningText(external.Provider, MaximumPlanningLabelBytes) {
				findings = append(findings, structuralFinding(PlanningExternalReferenceProviderMissing, reference))
			}
			if !boundedPlanningText(external.Key, MaximumExternalReferenceBytes) {
				findings = append(findings, structuralFinding(PlanningExternalReferenceKeyMissing, reference))
			}
		}
	}

	index.dependencies = allDependencies(epics, tasks)
	for _, dependency := range index.dependencies {
		if !validNodeKind(dependency.From.Kind) || !validNodeKind(dependency.On.Kind) {
			findings = append(findings, structuralFinding(PlanningDependencyKindInvalid, dependency.From, dependency.On))
			continue
		}
		if !stableIdentity(dependency.From.ID, 128) || !stableIdentity(dependency.On.ID, 128) {
			findings = append(findings, structuralFinding(PlanningDependencyEndpointMissing, dependency.From, dependency.On))
			continue
		}
		if !index.nodeExists(dependency.From) || !index.nodeExists(dependency.On) {
			findings = append(findings, structuralFinding(PlanningDependencyEndpointUnknown, dependency.From, dependency.On))
			continue
		}
		ownerValid := false
		if dependency.From.Kind == PlanningNodeEpic && dependency.On.Kind == PlanningNodeEpic {
			owner := index.epics[dependency.From.ID]
			ownerValid = dependencyOwnedBy(dependency, owner.Dependencies)
		}
		if dependency.From.Kind == PlanningNodeTask &&
			(dependency.On.Kind == PlanningNodeTask || dependency.On.Kind == PlanningNodeEpic) {
			owner := index.tasks[dependency.From.ID]
			ownerValid = dependencyOwnedBy(dependency, owner.Dependencies)
		}
		if !ownerValid {
			findings = append(findings, structuralFinding(PlanningDependencyKindInvalid, dependency.From, dependency.On))
			continue
		}
		key := dependencyKey(dependency)
		if _, duplicate := index.edges[key]; duplicate {
			findings = append(findings, structuralFinding(PlanningDependencyDuplicate, dependency.From, dependency.On))
			continue
		}
		index.edges[key] = dependency
		index.adjacency[dependency.From] = append(index.adjacency[dependency.From], dependency)
		if dependency.From == dependency.On {
			findings = append(findings, structuralFinding(PlanningDependencySelf, dependency.From, dependency.On))
			continue
		}
	}
	for from := range index.adjacency {
		sort.Slice(index.adjacency[from], func(left, right int) bool {
			return dependencyLess(index.adjacency[from][left], index.adjacency[from][right])
		})
	}
	findings = append(findings, dependencyCycleFindings(index)...)
	sort.Slice(findings, func(left, right int) bool { return explanationLess(findings[left], findings[right]) })
	return index, findings
}

func defaultPlanningKey(key, id string) string {
	if key == "" {
		return id
	}
	return key
}

func dependencyOwnedBy(wanted PlanningDependency, dependencies []PlanningDependency) bool {
	for _, dependency := range dependencies {
		if dependency == wanted {
			return true
		}
	}
	return false
}

func dependencyCycleFindings(index planningIndex) []PlanningExplanation {
	colors := make(map[PlanningNodeRef]uint8, len(index.epics)+len(index.tasks))
	nodes := make([]PlanningNodeRef, 0, len(index.epics)+len(index.tasks))
	for id := range index.epics {
		nodes = append(nodes, PlanningNodeRef{Kind: PlanningNodeEpic, ID: id})
	}
	for id := range index.tasks {
		nodes = append(nodes, PlanningNodeRef{Kind: PlanningNodeTask, ID: id})
	}
	sort.Slice(nodes, func(left, right int) bool { return refLess(nodes[left], nodes[right]) })
	findings := make([]PlanningExplanation, 0)
	var visit func(PlanningNodeRef)
	visit = func(node PlanningNodeRef) {
		colors[node] = 1
		for _, dependency := range index.adjacency[node] {
			switch colors[dependency.On] {
			case 0:
				visit(dependency.On)
			case 1:
				findings = append(findings, structuralFinding(PlanningDependencyCycle, dependency.From, dependency.On))
			}
		}
		colors[node] = 2
	}
	for _, node := range nodes {
		if colors[node] == 0 {
			visit(node)
		}
	}
	return findings
}

type overrideState struct {
	active   map[string]DependencyOverride
	rejected map[string][]PlanningExplanation
}

func evaluateOverrides(index planningIndex, overrides []DependencyOverride) (overrideState, []PlanningExplanation) {
	ordered := append([]DependencyOverride(nil), overrides...)
	sort.Slice(ordered, func(left, right int) bool {
		if ordered[left].ID != ordered[right].ID {
			return ordered[left].ID < ordered[right].ID
		}
		return overrideBindingKey(ordered[left].TaskID, ordered[left].TaskVersion, ordered[left].Dependency) <
			overrideBindingKey(ordered[right].TaskID, ordered[right].TaskVersion, ordered[right].Dependency)
	})
	state := overrideState{
		active: make(map[string]DependencyOverride), rejected: make(map[string][]PlanningExplanation),
	}
	findings := make([]PlanningExplanation, 0)
	reject := func(override DependencyOverride, finding PlanningExplanation, bindingKnown bool) {
		findings = append(findings, finding)
		if bindingKnown {
			key := overrideTaskEdgeKey(override.TaskID, override.Dependency)
			state.rejected[key] = append(state.rejected[key], finding)
		}
	}
	idCounts := make(map[string]int, len(ordered))
	for _, override := range ordered {
		idCounts[override.ID]++
	}
	eligible := make(map[string][]DependencyOverride)
	for _, override := range ordered {
		taskRef := PlanningNodeRef{Kind: PlanningNodeTask, ID: override.TaskID}
		task, taskKnown := index.tasks[override.TaskID]
		_, edgeKnown := index.edges[dependencyKey(override.Dependency)]
		bindingKnown := taskKnown && edgeKnown && dependencyAffectsTask(index, override.TaskID, override.Dependency)
		switch {
		case !stableIdentity(override.ID, 128):
			reject(override, PlanningExplanation{Code: PlanningOverrideIDMissing, Subject: taskRef}, bindingKnown)
			continue
		case idCounts[override.ID] != 1:
			reject(override, PlanningExplanation{
				Code: PlanningOverrideIDDuplicate, Subject: taskRef, OverrideID: override.ID,
			}, bindingKnown)
			continue
		}
		if !bindingKnown {
			reject(override, overrideFinding(PlanningOverrideBindingUnknown, override), false)
			continue
		}
		switch {
		case override.TaskVersion != task.Version:
			reject(override, overrideFinding(PlanningOverrideStale, override), true)
		case override.ActorKind != PlanningActorHuman:
			reject(override, overrideFinding(PlanningOverrideActorNotHuman, override), true)
		case !boundedPlanningText(override.ActorID, 256):
			reject(override, overrideFinding(PlanningOverrideActorMissing, override), true)
		case !stableIdentity(override.AuditID, 128) || override.GrantedAtMillis <= 0:
			reject(override, overrideFinding(PlanningOverrideAuditMissing, override), true)
		case !override.Granted:
			reject(override, overrideFinding(PlanningOverrideNotGranted, override), true)
		default:
			binding := overrideBindingKey(override.TaskID, override.TaskVersion, override.Dependency)
			eligible[binding] = append(eligible[binding], override)
		}
	}
	for binding, candidates := range eligible {
		sort.Slice(candidates, func(left, right int) bool { return candidates[left].ID < candidates[right].ID })
		if len(candidates) == 1 {
			state.active[binding] = candidates[0]
			continue
		}
		for _, override := range candidates {
			reject(override, overrideFinding(PlanningOverrideAmbiguous, override), true)
		}
	}
	sort.Slice(findings, func(left, right int) bool { return explanationLess(findings[left], findings[right]) })
	return state, findings
}

func overrideFinding(code PlanningCode, override DependencyOverride) PlanningExplanation {
	return PlanningExplanation{
		Code: code, Subject: PlanningNodeRef{Kind: PlanningNodeTask, ID: override.TaskID},
		Related: []PlanningNodeRef{override.Dependency.From, override.Dependency.On}, OverrideID: override.ID,
	}
}

func dependencyAffectsTask(index planningIndex, taskID string, wanted PlanningDependency) bool {
	_, ok := index.tasks[taskID]
	if !ok {
		return false
	}
	starts := []PlanningNodeRef{{Kind: PlanningNodeTask, ID: taskID}}
	seen := make(map[PlanningNodeRef]struct{})
	var visit func(PlanningNodeRef) bool
	visit = func(node PlanningNodeRef) bool {
		if _, duplicate := seen[node]; duplicate {
			return false
		}
		seen[node] = struct{}{}
		if node.Kind == PlanningNodeTask {
			current := index.tasks[node.ID]
			if current.Parent != nil && current.Parent.Kind == PlanningNodeEpic && visit(*current.Parent) {
				return true
			}
		}
		for _, dependency := range index.adjacency[node] {
			if dependency == wanted || visit(dependency.On) {
				return true
			}
		}
		return false
	}
	for _, start := range starts {
		if visit(start) {
			return true
		}
	}
	return false
}

func evaluateTaskDependencies(index planningIndex, overrides overrideState, task Task) TaskDependencyResult {
	result := TaskDependencyResult{TaskID: task.ID}
	seenExplanations := make(map[string]struct{})
	appendExplanation := func(explanation PlanningExplanation) {
		key := string(explanation.Code) + "\x1f" + refKey(explanation.Subject) + "\x1f" + explanation.OverrideID
		for _, related := range explanation.Related {
			key += "\x1f" + refKey(related)
		}
		if _, duplicate := seenExplanations[key]; duplicate {
			return
		}
		seenExplanations[key] = struct{}{}
		result.Explanations = append(result.Explanations, explanation)
	}
	var visit func(PlanningNodeRef, int, bool, map[PlanningNodeRef]struct{})
	visit = func(node PlanningNodeRef, depth int, epicOrigin bool, path map[PlanningNodeRef]struct{}) {
		if _, loop := path[node]; loop {
			return
		}
		nextPath := make(map[PlanningNodeRef]struct{}, len(path)+1)
		for current := range path {
			nextPath[current] = struct{}{}
		}
		nextPath[node] = struct{}{}
		if node.Kind == PlanningNodeTask {
			current := index.tasks[node.ID]
			if current.Parent != nil && current.Parent.Kind == PlanningNodeEpic {
				visit(*current.Parent, depth, true, nextPath)
			}
		}
		for _, dependency := range index.adjacency[node] {
			binding := overrideBindingKey(task.ID, task.Version, dependency)
			if _, released := overrides.active[binding]; released {
				continue
			}
			if !index.nodeComplete(dependency.On) {
				rejected := overrides.rejected[overrideTaskEdgeKey(task.ID, dependency)]
				if len(rejected) == 0 {
					appendExplanation(PlanningExplanation{
						Code: PlanningOverrideMissing, Subject: PlanningNodeRef{Kind: PlanningNodeTask, ID: task.ID},
						Related: []PlanningNodeRef{dependency.From, dependency.On},
					})
				} else {
					for _, explanation := range rejected {
						appendExplanation(explanation)
					}
				}
				code := PlanningTaskDependencyBlocked
				if epicOrigin {
					code = PlanningEpicDependencyBlocked
				}
				if depth > 0 {
					if epicOrigin {
						code = PlanningEpicDependencyBlockedTransitive
					} else {
						code = PlanningTaskDependencyBlockedTransitive
					}
				}
				appendExplanation(PlanningExplanation{
					Code: code, Subject: PlanningNodeRef{Kind: PlanningNodeTask, ID: task.ID},
					Related: []PlanningNodeRef{dependency.From, dependency.On},
				})
				visit(dependency.On, depth+1, epicOrigin, nextPath)
			}
		}
	}
	visit(PlanningNodeRef{Kind: PlanningNodeTask, ID: task.ID}, 0, false, nil)
	sort.Slice(result.Explanations, func(left, right int) bool {
		return explanationLess(result.Explanations[left], result.Explanations[right])
	})
	result.Blocked = len(result.Explanations) > 0
	return result
}

// EvaluatePlanning validates the complete planning graph, rejects every
// non-current or non-human override, and deterministically propagates direct,
// transitive, and Epic blockers to Tasks. It performs no I/O and derives no
// Board/List projection.
func EvaluatePlanning(project PlanningProject) PlanningReport {
	index, structural := buildPlanningIndex(project)
	overrides, overrideFindings := evaluateOverrides(index, project.Overrides)
	report := PlanningReport{Valid: len(structural) == 0}
	report.Findings = append(report.Findings, structural...)
	report.Findings = append(report.Findings, overrideFindings...)
	sort.Slice(report.Findings, func(left, right int) bool {
		return explanationLess(report.Findings[left], report.Findings[right])
	})
	tasks := make([]Task, 0, len(index.tasks))
	for _, task := range index.tasks {
		tasks = append(tasks, task)
	}
	sort.Slice(tasks, func(left, right int) bool { return tasks[left].ID < tasks[right].ID })
	for _, task := range tasks {
		if !report.Valid {
			report.Tasks = append(report.Tasks, TaskDependencyResult{
				TaskID: task.ID, Blocked: true,
				Explanations: []PlanningExplanation{{
					Code: PlanningInputInvalid, Subject: PlanningNodeRef{Kind: PlanningNodeTask, ID: task.ID},
				}},
			})
			continue
		}
		report.Tasks = append(report.Tasks, evaluateTaskDependencies(index, overrides, task))
	}
	return report
}

// NewDependencyOverride builds the only qualifying override fact from a typed
// authenticated-human grant and current TaskStore facts.
func NewDependencyOverride(
	project PlanningProject,
	grant HumanDependencyOverrideGrant,
	grantedAtMillis int64,
) (DependencyOverride, error) {
	index, structural := buildPlanningIndex(project)
	if len(structural) != 0 || !stableIdentity(grant.ID, 128) ||
		!boundedPlanningText(grant.HumanActorID, 256) || !stableIdentity(grant.AuditID, 128) ||
		grantedAtMillis <= 0 {
		return DependencyOverride{}, ErrInvalidPlanning
	}
	task, known := index.tasks[grant.TaskID]
	if !known || grant.ExpectedTaskVersion != task.Version ||
		!dependencyAffectsTask(index, grant.TaskID, grant.Dependency) {
		return DependencyOverride{}, ErrInvalidPlanning
	}
	for _, current := range project.Overrides {
		if current.ID == grant.ID ||
			(current.TaskID == grant.TaskID && current.TaskVersion == task.Version && current.Dependency == grant.Dependency) {
			return DependencyOverride{}, ErrInvalidPlanning
		}
	}
	override := DependencyOverride{
		ID: grant.ID, TaskID: grant.TaskID, TaskVersion: task.Version, Dependency: grant.Dependency,
		ActorKind: PlanningActorHuman, ActorID: grant.HumanActorID, AuditID: grant.AuditID,
		Granted: true, GrantedAtMillis: grantedAtMillis,
	}
	next := project
	next.Overrides = append(append([]DependencyOverride(nil), project.Overrides...), override)
	report := EvaluatePlanning(next)
	for _, finding := range report.Findings {
		if finding.OverrideID == override.ID {
			return DependencyOverride{}, ErrInvalidPlanning
		}
	}
	return override, nil
}
