// SPDX-License-Identifier: Apache-2.0

package planningtestkit

// ID is a canonical Director planning identity. External reference values are
// deliberately represented by a different type and never resolve an ID.
type ID string

// NodeKind is the closed planning-object vocabulary used by dependency and
// hierarchy references.
type NodeKind string

const (
	NodeProject   NodeKind = "project"
	NodeWorkspace NodeKind = "workspace"
	NodeEpic      NodeKind = "epic"
	NodeTask      NodeKind = "task"
)

// NodeRef identifies one planning object by its canonical kind and ID.
type NodeRef struct {
	Kind NodeKind `json:"kind"`
	ID   ID       `json:"id"`
}

// Project is the complete, deliberately small input to the planning oracle.
// It is not a production aggregate or persistence representation.
type Project struct {
	ID           ID             `json:"id"`
	Workspaces   []Workspace    `json:"workspaces"`
	Epics        []Epic         `json:"epics"`
	Tasks        []Task         `json:"tasks"`
	Dependencies []Dependency   `json:"dependencies"`
	Overrides    []OverrideFact `json:"overrides"`
}

// Workspace represents one canonical product repository.
type Workspace struct {
	ID ID `json:"id"`
}

// Epic is the sole permitted grouping level. Parent exists only so generated
// invalid input can prove that another planning level is rejected.
type Epic struct {
	ID       ID       `json:"id"`
	Parent   *NodeRef `json:"parent,omitempty"`
	Complete bool     `json:"complete"`
}

// Task represents either a standalone Task (Parent nil) or an Epic Task
// (Parent is an Epic). WorkspaceIDs is a slice so zero and multiple bindings
// can be represented and rejected by the oracle.
type Task struct {
	ID                 ID                  `json:"id"`
	Version            uint64              `json:"version"`
	WorkspaceIDs       []ID                `json:"workspaceIds"`
	Parent             *NodeRef            `json:"parent,omitempty"`
	Complete           bool                `json:"complete"`
	ExternalReferences []ExternalReference `json:"externalReferences,omitempty"`
}

// ExternalReference is contextual metadata. Provider and Key never replace a
// Task's canonical ID and are never dependency endpoints.
type ExternalReference struct {
	Provider string `json:"provider"`
	Key      string `json:"key"`
}

// Dependency states that From depends on On. Tasks may depend on Tasks or
// Epics; Epics may depend only on Epics.
type Dependency struct {
	From NodeRef `json:"from"`
	On   NodeRef `json:"on"`
}

// ActorKind is closed because only an authenticated human may grant a
// dependency override in this planning oracle.
type ActorKind string

const ActorHuman ActorKind = "human"

// OverrideFact is an immutable claimed human decision bound to one Task, its
// current version, and one exact dependency edge. The oracle applies it only
// when exactly one complete, current, granted human audit fact matches.
type OverrideFact struct {
	ID          ID         `json:"id"`
	TaskID      ID         `json:"taskId"`
	TaskVersion uint64     `json:"taskVersion"`
	Dependency  Dependency `json:"dependency"`
	ActorKind   ActorKind  `json:"actorKind"`
	ActorID     string     `json:"actorId"`
	AuditID     string     `json:"auditId"`
	Granted     bool       `json:"granted"`
}

// Code is the closed machine-readable explanation vocabulary.
type Code string

const (
	CodeProjectIDMissing                 Code = "project_id_missing"
	CodeWorkspaceRequired                Code = "workspace_required"
	CodeWorkspaceIDMissing               Code = "workspace_id_missing"
	CodeWorkspaceIDDuplicate             Code = "workspace_id_duplicate"
	CodeEpicIDMissing                    Code = "epic_id_missing"
	CodeEpicIDDuplicate                  Code = "epic_id_duplicate"
	CodeTaskIDMissing                    Code = "task_id_missing"
	CodeTaskIDDuplicate                  Code = "task_id_duplicate"
	CodeTaskWorkspaceCount               Code = "task_workspace_count_invalid"
	CodeTaskWorkspaceUnknown             Code = "task_workspace_unknown"
	CodeHierarchyThirdLevel              Code = "hierarchy_third_level_forbidden"
	CodeTaskEpicUnknown                  Code = "task_epic_unknown"
	CodeExternalReferenceProviderMissing Code = "external_reference_provider_missing"
	CodeExternalReferenceKeyMissing      Code = "external_reference_key_missing"
	CodeDependencyKindInvalid            Code = "dependency_kind_invalid"
	CodeDependencyEndpointMissing        Code = "dependency_endpoint_missing"
	CodeDependencyEndpointUnknown        Code = "dependency_endpoint_unknown"
	CodeDependencyDuplicate              Code = "dependency_duplicate"
	CodeDependencySelf                   Code = "dependency_self"
	CodeDependencyCycle                  Code = "dependency_cycle"
	CodeOverrideIDMissing                Code = "dependency_override_id_missing"
	CodeOverrideIDDuplicate              Code = "dependency_override_id_duplicate"
	CodeOverrideBindingUnknown           Code = "dependency_override_binding_unknown"
	CodeOverrideMissing                  Code = "dependency_override_missing"
	CodeOverrideStale                    Code = "dependency_override_stale"
	CodeOverrideAmbiguous                Code = "dependency_override_ambiguous"
	CodeOverrideActorNotHuman            Code = "dependency_override_actor_not_human"
	CodeOverrideActorMissing             Code = "dependency_override_actor_missing"
	CodeOverrideAuditMissing             Code = "dependency_override_audit_missing"
	CodeOverrideNotGranted               Code = "dependency_override_not_granted"
	CodePlanningInvalid                  Code = "planning_input_invalid"
	CodeTaskDependencyBlocked            Code = "task_dependency_blocked"
	CodeTaskDependencyBlockedTransitive  Code = "task_dependency_blocked_transitive"
	CodeEpicDependencyBlocked            Code = "epic_dependency_blocked"
	CodeEpicDependencyBlockedTransitive  Code = "epic_dependency_blocked_transitive"
)

// Codes returns the complete explanation vocabulary in stable order.
func Codes() []Code {
	return []Code{
		CodeProjectIDMissing,
		CodeWorkspaceRequired,
		CodeWorkspaceIDMissing,
		CodeWorkspaceIDDuplicate,
		CodeEpicIDMissing,
		CodeEpicIDDuplicate,
		CodeTaskIDMissing,
		CodeTaskIDDuplicate,
		CodeTaskWorkspaceCount,
		CodeTaskWorkspaceUnknown,
		CodeHierarchyThirdLevel,
		CodeTaskEpicUnknown,
		CodeExternalReferenceProviderMissing,
		CodeExternalReferenceKeyMissing,
		CodeDependencyKindInvalid,
		CodeDependencyEndpointMissing,
		CodeDependencyEndpointUnknown,
		CodeDependencyDuplicate,
		CodeDependencySelf,
		CodeDependencyCycle,
		CodeOverrideIDMissing,
		CodeOverrideIDDuplicate,
		CodeOverrideBindingUnknown,
		CodeOverrideMissing,
		CodeOverrideStale,
		CodeOverrideAmbiguous,
		CodeOverrideActorNotHuman,
		CodeOverrideActorMissing,
		CodeOverrideAuditMissing,
		CodeOverrideNotGranted,
		CodePlanningInvalid,
		CodeTaskDependencyBlocked,
		CodeTaskDependencyBlockedTransitive,
		CodeEpicDependencyBlocked,
		CodeEpicDependencyBlockedTransitive,
	}
}

// Explanation is entirely machine-readable: Code identifies the rule,
// Subject identifies the affected object, Related binds exact other objects,
// and OverrideID binds an override fact when present.
type Explanation struct {
	Code       Code      `json:"code"`
	Subject    NodeRef   `json:"subject"`
	Related    []NodeRef `json:"related,omitempty"`
	OverrideID ID        `json:"overrideId,omitempty"`
}

// TaskResult is the oracle's dependency result for one canonical Task.
type TaskResult struct {
	TaskID       ID            `json:"taskId"`
	Blocked      bool          `json:"blocked"`
	Explanations []Explanation `json:"explanations,omitempty"`
}

// Report is deterministic for identical input. Valid describes structural
// planning validity. Override facts may be rejected in Findings while the
// underlying graph remains structurally valid.
type Report struct {
	Valid    bool          `json:"valid"`
	Findings []Explanation `json:"findings,omitempty"`
	Tasks    []TaskResult  `json:"tasks"`
}

// HasCode reports whether a code occurs in project findings or a Task result.
func (report Report) HasCode(code Code) bool {
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

// Result returns the result for id, if the report contains it.
func (report Report) Result(id ID) (TaskResult, bool) {
	for _, result := range report.Tasks {
		if result.TaskID == id {
			return result, true
		}
	}
	return TaskResult{}, false
}

func ref(kind NodeKind, id ID) NodeRef { return NodeRef{Kind: kind, ID: id} }

func refKey(value NodeRef) string { return string(value.Kind) + "\x00" + string(value.ID) }

func sameRef(left, right NodeRef) bool { return left == right }

func dependencyKey(value Dependency) string {
	return refKey(value.From) + "\x01" + refKey(value.On)
}
