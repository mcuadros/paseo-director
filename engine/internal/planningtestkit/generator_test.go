// SPDX-License-Identifier: Apache-2.0

package planningtestkit

import (
	"encoding/json"
	"reflect"
	"testing"
)

func TestSeededGeneratorIsBoundedRepeatableAndFeatureComplete(t *testing.T) {
	bounds := DefaultBounds()
	for seed := uint64(0); seed < 64; seed++ {
		first, err := Generate(seed, bounds)
		if err != nil {
			t.Fatalf("Generate(%d): %v", seed, err)
		}
		second, err := Generate(seed, bounds)
		if err != nil {
			t.Fatalf("repeat Generate(%d): %v", seed, err)
		}
		firstJSON, err := json.Marshal(first)
		if err != nil {
			t.Fatalf("marshal first seed %d: %v", seed, err)
		}
		secondJSON, err := json.Marshal(second)
		if err != nil {
			t.Fatalf("marshal second seed %d: %v", seed, err)
		}
		if string(firstJSON) != string(secondJSON) {
			t.Fatalf("seed %d was not byte-repeatable", seed)
		}
		if len(first.Workspaces) > bounds.MaxWorkspaces || len(first.Epics) > bounds.MaxEpics ||
			len(first.Tasks) > bounds.MaxTasks || len(first.Dependencies) > bounds.MaxDependencies ||
			len(first.Overrides) > bounds.MaxOverrides || lenExternalReferences(first) > bounds.MaxExternalReferences {
			t.Fatalf("seed %d exceeded bounds: %#v", seed, first)
		}
		if report := Evaluate(first); !report.Valid || len(report.Findings) != 0 {
			t.Fatalf("seed %d generated invalid facts: %#v", seed, report.Findings)
		}
		assertGeneratedFeatures(t, seed, first)
		if !reflect.DeepEqual(Evaluate(first), Evaluate(second)) {
			t.Fatalf("seed %d oracle output was not repeatable", seed)
		}
	}

	zero, _ := Generate(0, bounds)
	one, _ := Generate(1, bounds)
	if reflect.DeepEqual(zero, one) {
		t.Fatal("different seeds produced the same Project")
	}
}

func TestGeneratorRejectsUnboundedOrInsufficientInputs(t *testing.T) {
	invalid := []Bounds{
		{},
		{MaxWorkspaces: 1, MaxTasks: 1, MaxEpics: -1},
		{MaxWorkspaces: hardMaximumWorkspaces + 1, MaxTasks: 1},
		{MaxWorkspaces: 1, MaxTasks: hardMaximumTasks + 1},
	}
	for _, bounds := range invalid {
		if _, err := Generate(1, bounds); err == nil {
			t.Fatalf("Generate accepted invalid bounds %#v", bounds)
		}
	}
	if _, err := GenerateCase(1, Bounds{MaxWorkspaces: 1, MaxTasks: 1}, VariantDependencyCycle); err == nil {
		t.Fatal("GenerateCase accepted insufficient adversarial bounds")
	}
	if _, err := GenerateCase(1, DefaultBounds(), Variant("new-policy")); err == nil {
		t.Fatal("GenerateCase accepted an open-ended variant")
	}
}

func TestGeneratedCorpusCoversEveryClosedVariant(t *testing.T) {
	bounds := DefaultBounds()
	first, err := GenerateCorpus(991, bounds)
	if err != nil {
		t.Fatal(err)
	}
	second, err := GenerateCorpus(991, bounds)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(first, second) {
		t.Fatal("same corpus seed produced different Projects")
	}
	if len(first) != len(Variants()) {
		t.Fatalf("corpus size = %d, variants = %d", len(first), len(Variants()))
	}
	expected := map[Variant]Code{
		VariantDuplicateIdentity:   CodeTaskIDDuplicate,
		VariantMissingWorkspace:    CodeTaskWorkspaceCount,
		VariantMultipleWorkspaces:  CodeTaskWorkspaceCount,
		VariantThirdPlanningLevel:  CodeHierarchyThirdLevel,
		VariantDuplicateDependency: CodeDependencyDuplicate,
		VariantSelfDependency:      CodeDependencySelf,
		VariantDependencyCycle:     CodeDependencyCycle,
		VariantMissingOverride:     CodeOverrideMissing,
		VariantStaleOverride:       CodeOverrideStale,
		VariantAmbiguousOverride:   CodeOverrideAmbiguous,
		VariantUnauditedOverride:   CodeOverrideAuditMissing,
	}
	for index, variant := range Variants() {
		code, hasExpectation := expected[variant]
		if hasExpectation && !Evaluate(first[index]).HasCode(code) {
			t.Fatalf("variant %s did not produce %s", variant, code)
		}
	}
}

func TestExhaustiveSmallStatesCoverEveryTwoNodeGraphAndOverrideClass(t *testing.T) {
	first := ExhaustiveSmallStates()
	second := ExhaustiveSmallStates()
	if !reflect.DeepEqual(first, second) {
		t.Fatal("exhaustive states are not repeatable")
	}
	const graphCases = 4 * (1 << 4)
	if len(first) != graphCases*2+5 {
		t.Fatalf("small-state count = %d", len(first))
	}
	for index, testCase := range first[:graphCases] {
		completionMask := index / 16
		edgeMask := index % 16
		report := Evaluate(testCase.Project)
		self := edgeMask&1 != 0 || edgeMask&8 != 0
		twoCycle := edgeMask&2 != 0 && edgeMask&4 != 0
		if report.Valid == (self || twoCycle) {
			t.Fatalf("%s validity = %v", testCase.Name, report.Valid)
		}
		if self || twoCycle {
			continue
		}
		assertBlocked(t, report, "task-0", edgeMask&2 != 0 && completionMask&2 == 0)
		assertBlocked(t, report, "task-1", edgeMask&4 != 0 && completionMask&1 == 0)
	}
	for offset, testCase := range first[graphCases : graphCases*2] {
		completionMask := offset / 16
		edgeMask := offset % 16
		report := Evaluate(testCase.Project)
		self := edgeMask&1 != 0 || edgeMask&8 != 0
		twoCycle := edgeMask&2 != 0 && edgeMask&4 != 0
		if report.Valid == (self || twoCycle) {
			t.Fatalf("%s validity = %v", testCase.Name, report.Valid)
		}
		if self || twoCycle {
			continue
		}
		assertBlocked(t, report, "task-0", edgeMask&2 != 0 && completionMask&2 == 0)
		assertBlocked(t, report, "task-1", edgeMask&4 != 0 && completionMask&1 == 0)
	}
	overrideCases := first[graphCases*2:]
	assertBlocked(t, Evaluate(overrideCases[0].Project), "task-1", true)
	assertBlocked(t, Evaluate(overrideCases[1].Project), "task-1", false)
	for _, testCase := range overrideCases[2:] {
		assertBlocked(t, Evaluate(testCase.Project), "task-1", true)
	}
}

func assertGeneratedFeatures(t *testing.T, seed uint64, project Project) {
	t.Helper()
	standalone := false
	epicTask := false
	references := false
	for _, task := range project.Tasks {
		standalone = standalone || task.Parent == nil
		epicTask = epicTask || task.Parent != nil
		references = references || len(task.ExternalReferences) > 0
	}
	if !standalone || !epicTask || !references || len(project.Epics) < 2 || len(project.Overrides) == 0 {
		t.Fatalf("seed %d omitted required generated features", seed)
	}
	workspaceByTask := make(map[ID]ID, len(project.Tasks))
	for _, task := range project.Tasks {
		workspaceByTask[task.ID] = task.WorkspaceIDs[0]
	}
	crossWorkspace := false
	for _, dependency := range project.Dependencies {
		if dependency.From.Kind == NodeTask && dependency.On.Kind == NodeTask &&
			workspaceByTask[dependency.From.ID] != workspaceByTask[dependency.On.ID] {
			crossWorkspace = true
		}
	}
	if !crossWorkspace {
		t.Fatalf("seed %d omitted a cross-Workspace dependency", seed)
	}
	for _, override := range project.Overrides {
		if override.ActorKind != ActorHuman || override.ActorID == "" || override.AuditID == "" {
			t.Fatalf("seed %d generated an unaudited override: %#v", seed, override)
		}
	}
}

func assertBlocked(t *testing.T, report Report, taskID ID, expected bool) {
	t.Helper()
	result, exists := report.Result(taskID)
	if !exists {
		t.Fatalf("missing Task result %q", taskID)
	}
	if result.Blocked != expected {
		t.Fatalf("Task %q blocked = %v, want %v; explanations %#v", taskID, result.Blocked, expected, result.Explanations)
	}
}
