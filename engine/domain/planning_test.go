// SPDX-License-Identifier: Apache-2.0

package domain

import (
	"fmt"
	"testing"

	planningtestkit "github.com/mcuadros/director-engine/internal/planningtestkit"
	"github.com/mcuadros/director-engine/internal/testkit/secretfixture"
)

func planningRef(kind PlanningNodeKind, id string) PlanningNodeRef {
	return PlanningNodeRef{Kind: kind, ID: id}
}

func planningFixture() PlanningProject {
	blockingEpic := Epic{
		ID: "epic-blocker", ProjectID: "project-1", Key: "BLOCK", Title: "Blocking epic",
	}
	dependentEdge := PlanningDependency{
		From: planningRef(PlanningNodeEpic, "epic-dependent"),
		On:   planningRef(PlanningNodeEpic, "epic-blocker"),
	}
	dependentEpic := Epic{
		ID: "epic-dependent", ProjectID: "project-1", Key: "DEPEND", Title: "Dependent epic",
		Dependencies: []PlanningDependency{dependentEdge},
	}
	crossWorkspaceEdge := PlanningDependency{
		From: planningRef(PlanningNodeTask, "task-cross-workspace"),
		On:   planningRef(PlanningNodeTask, "task-blocker"),
	}
	return PlanningProject{
		ID:         "project-1",
		Workspaces: []string{"workspace-a", "workspace-b"},
		Epics:      []Epic{blockingEpic, dependentEpic},
		Tasks: []Task{
			{
				ID: "task-blocker", ProjectID: "project-1", Key: "T-1", Title: "Blocker",
				Objective: "Produce the prerequisite", AcceptanceCriteria: "Prerequisite is complete",
				WorkspaceIDs: []string{"workspace-a"}, Priority: PriorityNormal,
			},
			{
				ID: "task-epic-a", ProjectID: "project-1", Key: "T-2", Title: "Epic child A",
				Objective: "Wait for the blocking Epic", AcceptanceCriteria: "Epic dependency is complete",
				WorkspaceIDs: []string{"workspace-a"}, Parent: pointerRef(planningRef(PlanningNodeEpic, "epic-dependent")),
				Priority: PriorityHigh,
			},
			{
				ID: "task-epic-b", ProjectID: "project-1", Key: "T-3", Title: "Epic child B",
				Objective: "Also wait for the blocking Epic", AcceptanceCriteria: "Epic dependency is complete",
				WorkspaceIDs: []string{"workspace-b"}, Parent: pointerRef(planningRef(PlanningNodeEpic, "epic-dependent")),
				Priority: PriorityNormal,
			},
			{
				ID: "task-cross-workspace", ProjectID: "project-1", Key: "T-4", Title: "Cross repository",
				Objective: "Depend across repositories", AcceptanceCriteria: "Cross repository dependency is complete",
				WorkspaceIDs: []string{"workspace-b"}, Dependencies: []PlanningDependency{crossWorkspaceEdge},
				ExternalReferences: []ExternalReference{{Provider: "github", Key: "example/product#42"}},
			},
			{
				ID: "task-standalone", ProjectID: "project-1", Key: "T-5", Title: "Standalone",
				Objective: "Remain independent", AcceptanceCriteria: "No implicit Epic blocker",
				WorkspaceIDs: []string{"workspace-b"},
			},
		},
	}
}

func pointerRef(reference PlanningNodeRef) *PlanningNodeRef {
	copy := reference
	return &copy
}

func requireTaskBlocked(t *testing.T, report PlanningReport, taskID string, blocked bool) TaskDependencyResult {
	t.Helper()
	result, ok := report.Result(taskID)
	if !ok {
		t.Fatalf("missing Task result %q: %#v", taskID, report.Tasks)
	}
	if result.Blocked != blocked {
		t.Fatalf("Task %q blocked = %t, want %t: %#v", taskID, result.Blocked, blocked, result.Explanations)
	}
	return result
}

func TestPlanningPropagatesEpicAndCrossWorkspaceBlocking(t *testing.T) {
	project := planningFixture()
	report := EvaluatePlanning(project)
	if !report.Valid {
		t.Fatalf("valid planning fixture rejected: %#v", report.Findings)
	}
	requireTaskBlocked(t, report, "task-epic-a", true)
	requireTaskBlocked(t, report, "task-epic-b", true)
	requireTaskBlocked(t, report, "task-cross-workspace", true)
	requireTaskBlocked(t, report, "task-standalone", false)
	if !report.HasCode(PlanningEpicDependencyBlocked) || !report.HasCode(PlanningTaskDependencyBlocked) {
		t.Fatalf("missing deterministic blocker explanations: %#v", report)
	}
}

func TestPlanningRejectsSecretAndPrivatePathBeforeDurableAdmission(t *testing.T) {
	for _, unsafe := range []string{"token=" + secretfixture.GitHubFineGrained(), "/home/owner/private-evidence"} {
		project := planningFixture()
		project.Tasks[0].Objective = unsafe
		if ValidateTask(project.Tasks[0]) == nil || EvaluatePlanning(project).Valid {
			t.Fatalf("unsafe planning text was admitted: %q", unsafe)
		}
	}
}

func TestPlanningRequiresOneCurrentUnambiguousHumanOverride(t *testing.T) {
	project := planningFixture()
	edge := project.Epics[1].Dependencies[0]
	grant := HumanDependencyOverrideGrant{
		ID: "override-1", TaskID: "task-epic-a", Dependency: edge,
		HumanActorID: "human-owner", AuditID: "audit-1",
	}
	override, err := NewDependencyOverride(project, grant, 1_000)
	if err != nil {
		t.Fatalf("grant override: %v", err)
	}
	project.Overrides = append(project.Overrides, override)
	report := EvaluatePlanning(project)
	requireTaskBlocked(t, report, "task-epic-a", false)
	requireTaskBlocked(t, report, "task-epic-b", true)

	for name, testCase := range map[string]struct {
		mutate func(*PlanningProject)
		code   PlanningCode
	}{
		"stale": {
			mutate: func(value *PlanningProject) {
				for index := range value.Tasks {
					if value.Tasks[index].ID == "task-epic-a" {
						value.Tasks[index].Version++
					}
				}
			},
			code: PlanningOverrideStale,
		},
		"ambiguous": {
			mutate: func(value *PlanningProject) {
				duplicate := value.Overrides[0]
				duplicate.ID = "override-2"
				duplicate.AuditID = "audit-2"
				value.Overrides = append(value.Overrides, duplicate)
			},
			code: PlanningOverrideAmbiguous,
		},
		"agent actor": {
			mutate: func(value *PlanningProject) { value.Overrides[0].ActorKind = PlanningActorAgent },
			code:   PlanningOverrideActorNotHuman,
		},
		"missing audit": {
			mutate: func(value *PlanningProject) { value.Overrides[0].AuditID = "" },
			code:   PlanningOverrideAuditMissing,
		},
		"missing actor": {
			mutate: func(value *PlanningProject) { value.Overrides[0].ActorID = "" },
			code:   PlanningOverrideActorMissing,
		},
		"missing identity": {
			mutate: func(value *PlanningProject) { value.Overrides[0].ID = "" },
			code:   PlanningOverrideIDMissing,
		},
		"not granted": {
			mutate: func(value *PlanningProject) { value.Overrides[0].Granted = false },
			code:   PlanningOverrideNotGranted,
		},
	} {
		t.Run(name, func(t *testing.T) {
			candidate := clonePlanningProject(project)
			testCase.mutate(&candidate)
			report := EvaluatePlanning(candidate)
			requireTaskBlocked(t, report, "task-epic-a", true)
			if !report.HasCode(testCase.code) {
				t.Fatalf("missing override refusal %q: %#v", testCase.code, report.Findings)
			}
		})
	}
}

func TestPlanningRejectsCyclesDuplicateAndThirdLevelHierarchy(t *testing.T) {
	for name, testCase := range map[string]struct {
		mutate func(*PlanningProject)
		code   PlanningCode
	}{
		"cycle": {
			mutate: func(value *PlanningProject) {
				value.Epics[0].Dependencies = []PlanningDependency{{
					From: planningRef(PlanningNodeEpic, "epic-blocker"),
					On:   planningRef(PlanningNodeEpic, "epic-dependent"),
				}}
			},
			code: PlanningDependencyCycle,
		},
		"duplicate": {
			mutate: func(value *PlanningProject) {
				value.Epics[1].Dependencies = append(value.Epics[1].Dependencies, value.Epics[1].Dependencies[0])
			},
			code: PlanningDependencyDuplicate,
		},
		"self": {
			mutate: func(value *PlanningProject) {
				value.Epics[0].Dependencies = []PlanningDependency{{
					From: planningRef(PlanningNodeEpic, "epic-blocker"),
					On:   planningRef(PlanningNodeEpic, "epic-blocker"),
				}}
			},
			code: PlanningDependencySelf,
		},
		"third level": {
			mutate: func(value *PlanningProject) {
				value.Epics[0].Parent = pointerRef(planningRef(PlanningNodeEpic, "epic-dependent"))
			},
			code: PlanningHierarchyThirdLevel,
		},
		"Task parent": {
			mutate: func(value *PlanningProject) {
				value.Tasks[0].Parent = pointerRef(planningRef(PlanningNodeTask, "task-standalone"))
			},
			code: PlanningHierarchyThirdLevel,
		},
		"missing Workspace": {
			mutate: func(value *PlanningProject) { value.Tasks[0].WorkspaceIDs = nil },
			code:   PlanningTaskWorkspaceCount,
		},
		"multiple Workspaces": {
			mutate: func(value *PlanningProject) {
				value.Tasks[0].WorkspaceIDs = []string{"workspace-a", "workspace-b"}
			},
			code: PlanningTaskWorkspaceCount,
		},
		"external provider": {
			mutate: func(value *PlanningProject) {
				value.Tasks[0].ExternalReferences = []ExternalReference{{Key: "example/repository#1"}}
			},
			code: PlanningExternalReferenceProviderMissing,
		},
		"external key": {
			mutate: func(value *PlanningProject) {
				value.Tasks[0].ExternalReferences = []ExternalReference{{Provider: "github"}}
			},
			code: PlanningExternalReferenceKeyMissing,
		},
	} {
		t.Run(name, func(t *testing.T) {
			candidate := planningFixture()
			testCase.mutate(&candidate)
			report := EvaluatePlanning(candidate)
			if report.Valid || !report.HasCode(testCase.code) {
				t.Fatalf("invalid planning result = %#v, want code %q", report, testCase.code)
			}
			if err := ValidatePlanning(candidate); err == nil {
				t.Fatal("ValidatePlanning accepted invalid input")
			}
		})
	}
}

func TestPlanningAllowsCurrentOverrideAfterImmutableHistoryBecomesStale(t *testing.T) {
	project := planningFixture()
	edge := project.Epics[1].Dependencies[0]
	first, err := NewDependencyOverride(project, HumanDependencyOverrideGrant{
		ID: "override-v0", TaskID: "task-epic-a", ExpectedTaskVersion: 0, Dependency: edge,
		HumanActorID: "human-owner", AuditID: "audit-v0",
	}, 1_000)
	if err != nil {
		t.Fatalf("grant initial override: %v", err)
	}
	project.Overrides = append(project.Overrides, first)
	for index := range project.Tasks {
		if project.Tasks[index].ID == "task-epic-a" {
			project.Tasks[index].Version = 1
		}
	}
	second, err := NewDependencyOverride(project, HumanDependencyOverrideGrant{
		ID: "override-v1", TaskID: "task-epic-a", ExpectedTaskVersion: 1, Dependency: edge,
		HumanActorID: "human-owner", AuditID: "audit-v1",
	}, 2_000)
	if err != nil {
		t.Fatalf("grant current override alongside immutable stale history: %v", err)
	}
	project.Overrides = append(project.Overrides, second)
	report := EvaluatePlanning(project)
	requireTaskBlocked(t, report, "task-epic-a", false)
	if !report.HasCode(PlanningOverrideStale) || report.HasCode(PlanningOverrideAmbiguous) {
		t.Fatalf("override history findings = %#v", report.Findings)
	}
}

func clonePlanningProject(project PlanningProject) PlanningProject {
	copy := project
	copy.Workspaces = append([]string(nil), project.Workspaces...)
	copy.Epics = append([]Epic(nil), project.Epics...)
	for index := range copy.Epics {
		copy.Epics[index].Dependencies = append([]PlanningDependency(nil), project.Epics[index].Dependencies...)
		copy.Epics[index].Labels = append([]string(nil), project.Epics[index].Labels...)
		if project.Epics[index].Parent != nil {
			copy.Epics[index].Parent = pointerRef(*project.Epics[index].Parent)
		}
	}
	copy.Tasks = append([]Task(nil), project.Tasks...)
	for index := range copy.Tasks {
		copy.Tasks[index].WorkspaceIDs = append([]string(nil), project.Tasks[index].WorkspaceIDs...)
		copy.Tasks[index].Dependencies = append([]PlanningDependency(nil), project.Tasks[index].Dependencies...)
		copy.Tasks[index].Labels = append([]string(nil), project.Tasks[index].Labels...)
		copy.Tasks[index].ExternalReferences = append([]ExternalReference(nil), project.Tasks[index].ExternalReferences...)
		if project.Tasks[index].Parent != nil {
			copy.Tasks[index].Parent = pointerRef(*project.Tasks[index].Parent)
		}
	}
	copy.Overrides = append([]DependencyOverride(nil), project.Overrides...)
	return copy
}

func productionRef(reference planningtestkit.NodeRef) PlanningNodeRef {
	return PlanningNodeRef{Kind: PlanningNodeKind(reference.Kind), ID: string(reference.ID)}
}

func productionPlanning(project planningtestkit.Project) PlanningProject {
	result := PlanningProject{ID: string(project.ID)}
	for _, workspace := range project.Workspaces {
		result.Workspaces = append(result.Workspaces, string(workspace.ID))
	}
	for _, epic := range project.Epics {
		converted := Epic{
			ID: string(epic.ID), ProjectID: result.ID, Key: string(epic.ID),
			Title: "Epic " + string(epic.ID), Complete: epic.Complete,
		}
		if epic.Parent != nil {
			converted.Parent = pointerRef(productionRef(*epic.Parent))
		}
		result.Epics = append(result.Epics, converted)
	}
	for _, task := range project.Tasks {
		converted := Task{
			ID: string(task.ID), ProjectID: result.ID, Key: string(task.ID), Title: "Task " + string(task.ID),
			Objective: "Execute " + string(task.ID), AcceptanceCriteria: "Complete " + string(task.ID),
			Version: task.Version, Complete: task.Complete,
		}
		for _, workspaceID := range task.WorkspaceIDs {
			converted.WorkspaceIDs = append(converted.WorkspaceIDs, string(workspaceID))
		}
		if task.Parent != nil {
			converted.Parent = pointerRef(productionRef(*task.Parent))
		}
		for _, external := range task.ExternalReferences {
			converted.ExternalReferences = append(converted.ExternalReferences, ExternalReference{
				Provider: external.Provider, Key: external.Key,
			})
		}
		result.Tasks = append(result.Tasks, converted)
	}
	for _, dependency := range project.Dependencies {
		converted := PlanningDependency{From: productionRef(dependency.From), On: productionRef(dependency.On)}
		for index := range result.Epics {
			if converted.From == planningRef(PlanningNodeEpic, result.Epics[index].ID) {
				result.Epics[index].Dependencies = append(result.Epics[index].Dependencies, converted)
			}
		}
		for index := range result.Tasks {
			if converted.From == planningRef(PlanningNodeTask, result.Tasks[index].ID) {
				result.Tasks[index].Dependencies = append(result.Tasks[index].Dependencies, converted)
			}
		}
	}
	for _, override := range project.Overrides {
		result.Overrides = append(result.Overrides, DependencyOverride{
			ID: string(override.ID), TaskID: string(override.TaskID), TaskVersion: override.TaskVersion,
			Dependency: PlanningDependency{From: productionRef(override.Dependency.From), On: productionRef(override.Dependency.On)},
			ActorKind:  PlanningActorKind(override.ActorKind), ActorID: override.ActorID, AuditID: override.AuditID,
			Granted: override.Granted, GrantedAtMillis: 1_000,
		})
	}
	return result
}

func TestPlanningMatchesIndependentOracleProperties(t *testing.T) {
	bounds := planningtestkit.DefaultBounds()
	for seed := uint64(0); seed < 128; seed++ {
		corpus, err := planningtestkit.GenerateCorpus(seed, bounds)
		if err != nil {
			t.Fatalf("seed %d: generate corpus: %v", seed, err)
		}
		for variantIndex, generated := range corpus {
			oracle := planningtestkit.Evaluate(generated)
			production := EvaluatePlanning(productionPlanning(generated))
			if production.Valid != oracle.Valid {
				t.Fatalf("seed %d variant %s: validity = %t, oracle %t; production=%#v oracle=%#v",
					seed, planningtestkit.Variants()[variantIndex], production.Valid, oracle.Valid,
					production.Findings, oracle.Findings)
			}
			for _, expected := range oracle.Tasks {
				actual, ok := production.Result(string(expected.TaskID))
				if !ok || actual.Blocked != expected.Blocked {
					t.Fatalf("seed %d variant %s Task %s: blocked = %t/%t, present=%t; production=%#v oracle=%#v",
						seed, planningtestkit.Variants()[variantIndex], expected.TaskID,
						actual.Blocked, expected.Blocked, ok, actual.Explanations, expected.Explanations)
				}
			}
			for _, code := range planningtestkit.Codes() {
				if production.HasCode(PlanningCode(code)) != oracle.HasCode(code) {
					t.Fatalf("seed %d variant %s: code %s presence differs; production=%#v oracle=%#v",
						seed, planningtestkit.Variants()[variantIndex], code, production, oracle)
				}
			}
		}
	}
}

func TestPlanningMatchesIndependentOracleExhaustiveSmallStates(t *testing.T) {
	for _, testCase := range planningtestkit.ExhaustiveSmallStates() {
		oracle := planningtestkit.Evaluate(testCase.Project)
		production := EvaluatePlanning(productionPlanning(testCase.Project))
		if production.Valid != oracle.Valid {
			t.Fatalf("%s: validity = %t, oracle %t; production=%#v oracle=%#v",
				testCase.Name, production.Valid, oracle.Valid, production.Findings, oracle.Findings)
		}
		for _, expected := range oracle.Tasks {
			actual, ok := production.Result(string(expected.TaskID))
			if !ok || actual.Blocked != expected.Blocked {
				t.Fatalf("%s Task %s: blocked = %t/%t, present=%t; production=%#v oracle=%#v",
					testCase.Name, expected.TaskID, actual.Blocked, expected.Blocked, ok,
					actual.Explanations, expected.Explanations)
			}
		}
		for _, code := range planningtestkit.Codes() {
			if production.HasCode(PlanningCode(code)) != oracle.HasCode(code) {
				t.Fatalf("%s: code %s presence differs; production=%#v oracle=%#v",
					testCase.Name, code, production, oracle)
			}
		}
	}
}

func TestPlanningEvaluationIsDeterministicUnderConcurrentReads(t *testing.T) {
	project := planningFixture()
	want := fmt.Sprintf("%#v", EvaluatePlanning(project))
	errors := make(chan string, 32)
	for worker := 0; worker < cap(errors); worker++ {
		go func() {
			for iteration := 0; iteration < 64; iteration++ {
				if got := fmt.Sprintf("%#v", EvaluatePlanning(project)); got != want {
					errors <- got
					return
				}
			}
			errors <- ""
		}()
	}
	for worker := 0; worker < cap(errors); worker++ {
		if got := <-errors; got != "" {
			t.Fatalf("concurrent evaluation changed output: %s", got)
		}
	}
}
