// SPDX-License-Identifier: Apache-2.0

package planningtestkit

import (
	"reflect"
	"slices"
	"testing"
)

func TestOracleRejectsIdentityWorkspaceHierarchyAndReferenceViolations(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*Project)
		code   Code
	}{
		{"missing project identity", func(project *Project) { project.ID = "" }, CodeProjectIDMissing},
		{"duplicate workspace identity", func(project *Project) { project.Workspaces = append(project.Workspaces, project.Workspaces[0]) }, CodeWorkspaceIDDuplicate},
		{"duplicate epic identity", func(project *Project) { project.Epics = append(project.Epics, project.Epics[0]) }, CodeEpicIDDuplicate},
		{"duplicate task identity", func(project *Project) { project.Tasks = append(project.Tasks, project.Tasks[0]) }, CodeTaskIDDuplicate},
		{"zero task workspaces", func(project *Project) { project.Tasks[0].WorkspaceIDs = nil }, CodeTaskWorkspaceCount},
		{"two task workspaces", func(project *Project) { project.Tasks[0].WorkspaceIDs = []ID{"workspace-a", "workspace-b"} }, CodeTaskWorkspaceCount},
		{"unknown task workspace", func(project *Project) { project.Tasks[0].WorkspaceIDs = []ID{"external-workspace"} }, CodeTaskWorkspaceUnknown},
		{"nested epic", func(project *Project) { parent := ref(NodeEpic, "epic-a"); project.Epics[1].Parent = &parent }, CodeHierarchyThirdLevel},
		{"task below task", func(project *Project) { parent := ref(NodeTask, "task-a"); project.Tasks[1].Parent = &parent }, CodeHierarchyThirdLevel},
		{"unknown epic", func(project *Project) { parent := ref(NodeEpic, "external-epic"); project.Tasks[1].Parent = &parent }, CodeTaskEpicUnknown},
		{"missing external provider", func(project *Project) { project.Tasks[0].ExternalReferences[0].Provider = "" }, CodeExternalReferenceProviderMissing},
		{"missing external key", func(project *Project) { project.Tasks[0].ExternalReferences[0].Key = "" }, CodeExternalReferenceKeyMissing},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			project := validProject()
			test.mutate(&project)
			report := Evaluate(project)
			if report.Valid || !report.HasCode(test.code) {
				t.Fatalf("report = %#v, want invalid with %s", report, test.code)
			}
			for _, result := range report.Tasks {
				if !result.Blocked || !hasExplanation(result.Explanations, CodePlanningInvalid) {
					t.Fatalf("invalid planning input did not fail Task closed: %#v", result)
				}
			}
		})
	}
}

func TestExternalReferencesNeverBecomeCanonicalDependencyIdentity(t *testing.T) {
	project := validProject()
	project.Dependencies = []Dependency{{
		From: ref(NodeTask, "task-b"),
		On:   ref(NodeTask, ID(project.Tasks[0].ExternalReferences[0].Key)),
	}}
	report := Evaluate(project)
	if report.Valid || !report.HasCode(CodeDependencyEndpointUnknown) {
		t.Fatalf("external reference resolved as a canonical Task: %#v", report)
	}
}

func TestOracleRejectsMalformedDuplicateSelfAndCyclicEdges(t *testing.T) {
	tests := []struct {
		name         string
		dependencies []Dependency
		code         Code
	}{
		{
			"unknown endpoint",
			[]Dependency{{From: ref(NodeTask, "task-b"), On: ref(NodeTask, "external-task")}},
			CodeDependencyEndpointUnknown,
		},
		{
			"epic depending on task",
			[]Dependency{{From: ref(NodeEpic, "epic-a"), On: ref(NodeTask, "task-a")}},
			CodeDependencyKindInvalid,
		},
		{
			"duplicate edge",
			[]Dependency{
				{From: ref(NodeTask, "task-b"), On: ref(NodeTask, "task-a")},
				{From: ref(NodeTask, "task-b"), On: ref(NodeTask, "task-a")},
			},
			CodeDependencyDuplicate,
		},
		{
			"self edge",
			[]Dependency{{From: ref(NodeTask, "task-a"), On: ref(NodeTask, "task-a")}},
			CodeDependencySelf,
		},
		{
			"task cycle",
			[]Dependency{
				{From: ref(NodeTask, "task-b"), On: ref(NodeTask, "task-a")},
				{From: ref(NodeTask, "task-a"), On: ref(NodeTask, "task-b")},
			},
			CodeDependencyCycle,
		},
		{
			"epic cycle",
			[]Dependency{
				{From: ref(NodeEpic, "epic-b"), On: ref(NodeEpic, "epic-a")},
				{From: ref(NodeEpic, "epic-a"), On: ref(NodeEpic, "epic-b")},
			},
			CodeDependencyCycle,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			project := validProject()
			project.Dependencies = test.dependencies
			report := Evaluate(project)
			if report.Valid || !report.HasCode(test.code) {
				t.Fatalf("report = %#v, want invalid with %s", report, test.code)
			}
		})
	}
}

func TestTaskBlockersAreDirectTransitiveAndStopAtCompletedDependencies(t *testing.T) {
	project := validProject()
	project.Dependencies = []Dependency{
		{From: ref(NodeTask, "task-c"), On: ref(NodeTask, "task-b")},
		{From: ref(NodeTask, "task-b"), On: ref(NodeTask, "task-a")},
	}
	report := Evaluate(project)
	result, _ := report.Result("task-c")
	if !result.Blocked || !hasExplanation(result.Explanations, CodeTaskDependencyBlocked) ||
		!hasExplanation(result.Explanations, CodeTaskDependencyBlockedTransitive) {
		t.Fatalf("transitive Task blockers = %#v", result)
	}
	closure := DependencyClosure(project, ref(NodeTask, "task-c"))
	wantClosure := []NodeRef{ref(NodeTask, "task-a"), ref(NodeTask, "task-b")}
	if !reflect.DeepEqual(closure, wantClosure) {
		t.Fatalf("closure = %#v, want %#v", closure, wantClosure)
	}

	for index := range project.Tasks {
		if project.Tasks[index].ID == "task-b" {
			project.Tasks[index].Complete = true
		}
	}
	result, _ = Evaluate(project).Result("task-c")
	if result.Blocked {
		t.Fatalf("completed direct dependency did not stop traversal: %#v", result)
	}
}

func TestEpicBlockersPropagateTransitivelyAndOverrideOnlyOneTask(t *testing.T) {
	project := validProject()
	project.Dependencies = []Dependency{
		{From: ref(NodeEpic, "epic-b"), On: ref(NodeEpic, "epic-a")},
		{From: ref(NodeEpic, "epic-a"), On: ref(NodeEpic, "epic-root")},
	}
	report := Evaluate(project)
	for _, taskID := range []ID{"task-c", "task-d"} {
		result, _ := report.Result(taskID)
		if !result.Blocked || !hasExplanation(result.Explanations, CodeEpicDependencyBlocked) ||
			!hasExplanation(result.Explanations, CodeEpicDependencyBlockedTransitive) {
			t.Fatalf("Task %s Epic blockers = %#v", taskID, result)
		}
	}

	taskC := taskByID(project, "task-c")
	project.Overrides = []OverrideFact{validOverride(taskC, project.Dependencies[0], "override-task-c")}
	report = Evaluate(project)
	assertBlocked(t, report, "task-c", false)
	assertBlocked(t, report, "task-d", true)
}

func TestOverrideMustBeExactCurrentUnambiguousAuditedAndHuman(t *testing.T) {
	project := validProject()
	edge := Dependency{From: ref(NodeTask, "task-b"), On: ref(NodeTask, "task-a")}
	project.Dependencies = []Dependency{edge}
	task := taskByID(project, "task-b")

	tests := []struct {
		name      string
		overrides []OverrideFact
		code      Code
		blocked   bool
	}{
		{"missing", nil, CodeOverrideMissing, true},
		{"valid", []OverrideFact{validOverride(task, edge, "override-valid")}, "", false},
		{"stale", mutateOverride(validOverride(task, edge, "override-stale"), func(value *OverrideFact) { value.TaskVersion++ }), CodeOverrideStale, true},
		{"ambiguous", []OverrideFact{validOverride(task, edge, "override-a"), validOverride(task, edge, "override-b")}, CodeOverrideAmbiguous, true},
		{"missing identity", mutateOverride(validOverride(task, edge, ""), func(*OverrideFact) {}), CodeOverrideIDMissing, true},
		{"non-human", mutateOverride(validOverride(task, edge, "override-agent"), func(value *OverrideFact) { value.ActorKind = "agent" }), CodeOverrideActorNotHuman, true},
		{"missing actor", mutateOverride(validOverride(task, edge, "override-actor"), func(value *OverrideFact) { value.ActorID = "" }), CodeOverrideActorMissing, true},
		{"missing audit", mutateOverride(validOverride(task, edge, "override-audit"), func(value *OverrideFact) { value.AuditID = "" }), CodeOverrideAuditMissing, true},
		{"not granted", mutateOverride(validOverride(task, edge, "override-denied"), func(value *OverrideFact) { value.Granted = false }), CodeOverrideNotGranted, true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			candidate := Clone(project)
			candidate.Overrides = test.overrides
			report := Evaluate(candidate)
			if !report.Valid {
				t.Fatalf("override rejection changed graph validity: %#v", report.Findings)
			}
			assertBlocked(t, report, task.ID, test.blocked)
			if test.code != "" && !report.HasCode(test.code) {
				t.Fatalf("override report lacks %s: %#v", test.code, report)
			}
		})
	}

	unbound := validOverride(task, Dependency{From: edge.From, On: ref(NodeTask, "external-task")}, "override-unbound")
	project.Overrides = []OverrideFact{unbound}
	if report := Evaluate(project); !report.HasCode(CodeOverrideBindingUnknown) {
		t.Fatalf("unbound override was not rejected: %#v", report)
	}
}

func TestDuplicateOverrideIDInvalidatesEveryDistinctTaskBinding(t *testing.T) {
	project := validProject()
	edges := []Dependency{
		{From: ref(NodeTask, "task-b"), On: ref(NodeTask, "task-a")},
		{From: ref(NodeTask, "task-c"), On: ref(NodeTask, "task-a")},
	}
	project.Dependencies = edges
	project.Overrides = []OverrideFact{
		validOverride(taskByID(project, "task-b"), edges[0], "override-duplicate"),
		validOverride(taskByID(project, "task-c"), edges[1], "override-duplicate"),
	}

	report := Evaluate(project)
	if !report.Valid || !report.HasCode(CodeOverrideIDDuplicate) {
		t.Fatalf("duplicate identity report = %#v", report)
	}
	for _, taskID := range []ID{"task-b", "task-c"} {
		result, _ := report.Result(taskID)
		if !result.Blocked || !hasExplanation(result.Explanations, CodeOverrideIDDuplicate) {
			t.Fatalf("Task %s used a globally duplicated override: %#v", taskID, result)
		}
	}
}

func TestDuplicateOverrideIDInvalidatesEveryDistinctEpicBinding(t *testing.T) {
	project := validProject()
	epicA := ref(NodeEpic, "epic-a")
	project.Tasks = append(project.Tasks, Task{
		ID: "task-e", Version: 13, WorkspaceIDs: []ID{"workspace-a"}, Parent: &epicA,
	})
	edges := []Dependency{
		{From: ref(NodeEpic, "epic-b"), On: ref(NodeEpic, "epic-a")},
		{From: ref(NodeEpic, "epic-a"), On: ref(NodeEpic, "epic-root")},
	}
	project.Dependencies = edges
	project.Overrides = []OverrideFact{
		validOverride(taskByID(project, "task-c"), edges[0], "override-duplicate"),
		validOverride(taskByID(project, "task-e"), edges[1], "override-duplicate"),
	}

	report := Evaluate(project)
	if !report.Valid || !report.HasCode(CodeOverrideIDDuplicate) {
		t.Fatalf("duplicate identity report = %#v", report)
	}
	for _, taskID := range []ID{"task-c", "task-e"} {
		result, _ := report.Result(taskID)
		if !result.Blocked || !hasExplanation(result.Explanations, CodeOverrideIDDuplicate) {
			t.Fatalf("Task %s used a globally duplicated Epic override: %#v", taskID, result)
		}
	}
}

func TestExplanationVocabularyIsClosedUniqueAndMachineReadable(t *testing.T) {
	codes := Codes()
	if len(codes) == 0 {
		t.Fatal("empty explanation vocabulary")
	}
	seen := make(map[Code]struct{}, len(codes))
	for _, code := range codes {
		if code == "" {
			t.Fatal("empty explanation code")
		}
		if _, exists := seen[code]; exists {
			t.Fatalf("duplicate explanation code %q", code)
		}
		seen[code] = struct{}{}
		for _, character := range code {
			if character != '_' && (character < 'a' || character > 'z') {
				t.Fatalf("code %q is not machine-readable snake_case", code)
			}
		}
	}
}

func TestEveryPlanningGuardChangesTheOracleOutcome(t *testing.T) {
	direct := validProject()
	direct.Dependencies = []Dependency{{From: ref(NodeTask, "task-b"), On: ref(NodeTask, "task-a")}}
	transitive := validProject()
	transitive.Dependencies = []Dependency{
		{From: ref(NodeTask, "task-c"), On: ref(NodeTask, "task-b")},
		{From: ref(NodeTask, "task-b"), On: ref(NodeTask, "task-a")},
	}
	epic := validProject()
	epic.Dependencies = []Dependency{{From: ref(NodeEpic, "epic-b"), On: ref(NodeEpic, "epic-a")}}
	stale := Clone(direct)
	task := taskByID(stale, "task-b")
	override := validOverride(task, stale.Dependencies[0], "override-stale")
	override.TaskVersion++
	stale.Overrides = []OverrideFact{override}
	external := validProject()
	external.Tasks[0].ExternalReferences[0].Provider = ""
	endpoint := validProject()
	endpoint.Dependencies = []Dependency{{From: ref(NodeTask, "task-b"), On: ref(NodeTask, "external-task")}}
	duplicateOverride := distinctTaskDuplicateOverrideProject()

	tests := []struct {
		name    string
		guard   guardSet
		project Project
		code    Code
	}{
		{"stable identity", guardStableIdentity, mustGeneratedCase(t, VariantDuplicateIdentity), CodeTaskIDDuplicate},
		{"Workspace binding", guardWorkspaceBinding, mustGeneratedCase(t, VariantMissingWorkspace), CodeTaskWorkspaceCount},
		{"two-level hierarchy", guardHierarchy, mustGeneratedCase(t, VariantThirdPlanningLevel), CodeHierarchyThirdLevel},
		{"external reference shape", guardExternalReferences, external, CodeExternalReferenceProviderMissing},
		{"canonical endpoints", guardDependencyEndpoints, endpoint, CodeDependencyEndpointUnknown},
		{"duplicate edges", guardDuplicateDependencies, mustGeneratedCase(t, VariantDuplicateDependency), CodeDependencyDuplicate},
		{"self edges", guardSelfDependencies, mustGeneratedCase(t, VariantSelfDependency), CodeDependencySelf},
		{"cycles", guardDependencyCycles, mustGeneratedCase(t, VariantDependencyCycle), CodeDependencyCycle},
		{"direct blockers", guardDirectBlockers, direct, CodeTaskDependencyBlocked},
		{"transitive blockers", guardTransitiveBlockers, transitive, CodeTaskDependencyBlockedTransitive},
		{"Epic propagation", guardEpicPropagation, epic, CodeEpicDependencyBlocked},
		{"override authorization", guardOverrideAuthorization, stale, CodeOverrideStale},
		{"global override identity", guardOverrideIdentityUniqueness, duplicateOverride, CodeOverrideIDDuplicate},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			baseline := evaluateWithGuards(test.project, allGuards)
			mutant := evaluateWithGuards(test.project, allGuards&^test.guard)
			if !baseline.HasCode(test.code) {
				t.Fatalf("baseline lacks %s: %#v", test.code, baseline)
			}
			if mutant.HasCode(test.code) {
				t.Fatalf("deleted guard still emitted %s: %#v", test.code, mutant)
			}
			if reflect.DeepEqual(baseline, mutant) {
				t.Fatal("guard deletion did not change the oracle outcome")
			}
		})
	}

	duplicateBaseline := evaluateWithGuards(duplicateOverride, allGuards)
	duplicateMutant := evaluateWithGuards(duplicateOverride, allGuards&^guardOverrideIdentityUniqueness)
	for _, taskID := range []ID{"task-b", "task-c"} {
		assertBlocked(t, duplicateBaseline, taskID, true)
		assertBlocked(t, duplicateMutant, taskID, false)
	}
}

func distinctTaskDuplicateOverrideProject() Project {
	project := validProject()
	edges := []Dependency{
		{From: ref(NodeTask, "task-b"), On: ref(NodeTask, "task-a")},
		{From: ref(NodeTask, "task-c"), On: ref(NodeTask, "task-a")},
	}
	project.Dependencies = edges
	project.Overrides = []OverrideFact{
		validOverride(taskByID(project, "task-b"), edges[0], "override-duplicate"),
		validOverride(taskByID(project, "task-c"), edges[1], "override-duplicate"),
	}
	return project
}

func validProject() Project {
	epicA := ref(NodeEpic, "epic-a")
	epicB := ref(NodeEpic, "epic-b")
	return Project{
		ID:         "project-a",
		Workspaces: []Workspace{{ID: "workspace-a"}, {ID: "workspace-b"}},
		Epics: []Epic{
			{ID: "epic-root", Complete: false},
			{ID: epicA.ID, Complete: false},
			{ID: epicB.ID, Complete: false},
		},
		Tasks: []Task{
			{ID: "task-a", Version: 3, WorkspaceIDs: []ID{"workspace-a"}, ExternalReferences: []ExternalReference{{Provider: "github", Key: "GH-42"}}},
			{ID: "task-b", Version: 5, WorkspaceIDs: []ID{"workspace-b"}},
			{ID: "task-c", Version: 7, WorkspaceIDs: []ID{"workspace-a"}, Parent: &epicB},
			{ID: "task-d", Version: 11, WorkspaceIDs: []ID{"workspace-b"}, Parent: &epicB},
		},
	}
}

func taskByID(project Project, id ID) Task {
	for _, task := range project.Tasks {
		if task.ID == id {
			return task
		}
	}
	panic("missing test Task " + string(id))
}

func mutateOverride(value OverrideFact, mutate func(*OverrideFact)) []OverrideFact {
	mutate(&value)
	return []OverrideFact{value}
}

func hasExplanation(explanations []Explanation, code Code) bool {
	return slices.ContainsFunc(explanations, func(explanation Explanation) bool { return explanation.Code == code })
}

func mustGeneratedCase(t *testing.T, variant Variant) Project {
	t.Helper()
	project, err := GenerateCase(404, DefaultBounds(), variant)
	if err != nil {
		t.Fatal(err)
	}
	return project
}
