// SPDX-License-Identifier: Apache-2.0

package planningtestkit

import (
	"reflect"
	"testing"
)

func TestShrinkIsDeterministicStrictAndDoesNotMutateInput(t *testing.T) {
	project, err := GenerateCase(77, DefaultBounds(), VariantDependencyCycle)
	if err != nil {
		t.Fatal(err)
	}
	original := Clone(project)
	first := Shrink(project)
	second := Shrink(project)
	if !reflect.DeepEqual(first, second) {
		t.Fatal("Shrink is not deterministic")
	}
	if !reflect.DeepEqual(project, original) {
		t.Fatal("Shrink mutated its input")
	}
	for index, candidate := range first {
		if Complexity(candidate) >= Complexity(project) {
			t.Fatalf("candidate %d did not shrink: %d >= %d", index, Complexity(candidate), Complexity(project))
		}
	}
}

func TestMinimizePreservesFailureAndSurvivingStableIdentities(t *testing.T) {
	project, err := GenerateCase(91, DefaultBounds(), VariantDependencyCycle)
	if err != nil {
		t.Fatal(err)
	}
	predicate := func(candidate Project) bool { return Evaluate(candidate).HasCode(CodeDependencyCycle) }
	first := Minimize(project, predicate)
	second := Minimize(project, predicate)
	if !reflect.DeepEqual(first, second) {
		t.Fatal("Minimize is not deterministic")
	}
	if Complexity(first) >= Complexity(project) {
		t.Fatalf("minimum did not shrink: %d >= %d", Complexity(first), Complexity(project))
	}
	if !predicate(first) {
		t.Fatal("minimum lost the dependency-cycle failure")
	}
	assertSurvivingIdentities(t, project, first)
}

func assertSurvivingIdentities(t *testing.T, original, minimized Project) {
	t.Helper()
	originalIDs := make(map[NodeRef]struct{})
	for _, workspace := range original.Workspaces {
		originalIDs[ref(NodeWorkspace, workspace.ID)] = struct{}{}
	}
	for _, epic := range original.Epics {
		originalIDs[ref(NodeEpic, epic.ID)] = struct{}{}
	}
	for _, task := range original.Tasks {
		originalIDs[ref(NodeTask, task.ID)] = struct{}{}
	}
	for _, workspace := range minimized.Workspaces {
		if _, exists := originalIDs[ref(NodeWorkspace, workspace.ID)]; !exists {
			t.Fatalf("shrinker invented Workspace identity %q", workspace.ID)
		}
	}
	for _, epic := range minimized.Epics {
		if _, exists := originalIDs[ref(NodeEpic, epic.ID)]; !exists {
			t.Fatalf("shrinker invented Epic identity %q", epic.ID)
		}
	}
	for _, task := range minimized.Tasks {
		if _, exists := originalIDs[ref(NodeTask, task.ID)]; !exists {
			t.Fatalf("shrinker invented Task identity %q", task.ID)
		}
	}
}
