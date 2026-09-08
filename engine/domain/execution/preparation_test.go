// SPDX-License-Identifier: Apache-2.0

package execution

import "testing"

func TestM1PreparationPlanIsVersionedFiniteAndDeterministic(t *testing.T) {
	scope := testScope()
	first := NewM1PreparationPlan(scope, "inputs-hash")
	second := NewM1PreparationPlan(scope, "inputs-hash")
	if first.ID == "" || first != second || first.SchemaVersion != PreparationPlanSchemaVersion {
		t.Fatalf("preparation plan identity = %#v %#v", first, second)
	}
	if first.WholePlanDeadlineSeconds != 1_200 || first.DependencyAggregateSeconds != 900 || len(first.Steps) != 9 {
		t.Fatalf("preparation plan ceilings = %#v", first)
	}
	for index, step := range first.Steps {
		if step.ID == "" || step.Ordinal != index+1 || step.TimeoutSeconds <= 0 || step.FailureCode == "" {
			t.Fatalf("step %d is not finite and closed: %#v", index+1, step)
		}
	}
	if first.Steps[6].PerCommandTimeoutSeconds != 300 || first.Steps[6].AggregateTimeoutSeconds != 900 {
		t.Fatalf("dependency ceilings = %#v", first.Steps[6])
	}
}

func TestPreparationBarrierBindsEveryFakePathOutput(t *testing.T) {
	plan := NewM1PreparationPlan(testScope(), "inputs-hash")
	outputs := PreparationOutputs{
		FrozenInputsHash: "frozen", EligibilityHash: "eligibility",
		SecurityAdmissionHash: "security", WorktreeHash: "worktree",
		HostViewHash: "host-view", IsolationHash: "isolation",
		ToolingHash: "tooling", DependenciesHash: "dependencies",
		ContextHash: "context",
	}
	first, ok := PreparationBarrier(plan, outputs)
	second, secondOK := PreparationBarrier(plan, outputs)
	if !ok || !secondOK || first == "" || first != second {
		t.Fatalf("barrier = %q/%v %q/%v", first, ok, second, secondOK)
	}
	outputs.ContextHash = ""
	if barrier, ok := PreparationBarrier(plan, outputs); ok || barrier != "" {
		t.Fatalf("incomplete outputs produced barrier %q", barrier)
	}
}
