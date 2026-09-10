// SPDX-License-Identifier: Apache-2.0

package scheduleroracle_test

import (
	"math"
	"reflect"
	"slices"
	"testing"

	oracle "github.com/mcuadros/director-engine/internal/testkit/scheduleroracle"
)

func resultCode(t *testing.T, result oracle.Result, id oracle.TaskID) oracle.Code {
	t.Helper()
	for _, explanation := range result.Explanations {
		if explanation.TaskID == id {
			return explanation.Code
		}
	}
	t.Fatalf("result has no explanation for %q: %#v", id, result)
	return ""
}

func assertOrder(t *testing.T, result oracle.Result, want ...oracle.TaskID) {
	t.Helper()
	if !slices.Equal(result.OrderedTaskIDs, want) {
		t.Fatalf("ordered Task IDs = %v, want %v; explanations=%v", result.OrderedTaskIDs, want, result.Explanations)
	}
}

func roomyLimits() oracle.Limits {
	return oracle.NewLimits(32, 32, 32, 8)
}

func TestClosedVocabularyAndDefaultLimits(t *testing.T) {
	priorities := oracle.Priorities()
	wantPriorities := []oracle.Priority{
		oracle.PriorityUrgent,
		oracle.PriorityHigh,
		oracle.PriorityNormal,
		oracle.PriorityLow,
	}
	if !slices.Equal(priorities, wantPriorities) {
		t.Fatalf("priorities = %v, want %v", priorities, wantPriorities)
	}
	priorities[0] = oracle.PriorityLow
	if oracle.Priorities()[0] != oracle.PriorityUrgent {
		t.Fatal("Priorities exposed mutable package state")
	}

	codes := oracle.Codes()
	seen := make(map[oracle.Code]struct{}, len(codes))
	for _, code := range codes {
		if code == "" {
			t.Fatal("closed vocabulary contains an empty code")
		}
		if _, duplicate := seen[code]; duplicate {
			t.Fatalf("closed vocabulary repeats %q", code)
		}
		seen[code] = struct{}{}
	}
	codes[0] = "mutated"
	if oracle.Codes()[0] == "mutated" {
		t.Fatal("Codes exposed mutable package state")
	}

	tasks := []oracle.TaskFacts{
		oracle.NewTask("a", "one", 0),
		oracle.NewTask("b", "one", 1),
		oracle.NewTask("c", "two", 2),
		oracle.NewTask("d", "two", 3),
		oracle.NewTask("e", "three", 4),
	}
	result := oracle.Evaluate(oracle.NewSnapshot(oracle.PolicyAutomatic, oracle.DefaultLimits(), oracle.NewUsage(0, 0, 0, 0), tasks...))
	assertOrder(t, result, "a", "b", "c", "d")
	if code := resultCode(t, result, "e"); code != oracle.CodeProjectCapacity {
		t.Fatalf("fifth Task code = %q, want %q", code, oracle.CodeProjectCapacity)
	}
}

func TestManualAutomaticOverridesAndLaunchNow(t *testing.T) {
	for _, projectPolicy := range []oracle.LaunchPolicy{oracle.PolicyManual, oracle.PolicyAutomatic} {
		for _, override := range []oracle.PolicyOverride{
			oracle.PolicyInherit,
			oracle.PolicyForceManual,
			oracle.PolicyForceAutomatic,
		} {
			for _, launchNow := range []bool{false, true} {
				name := string(rune('0'+projectPolicy)) + "/" + string(rune('0'+override))
				t.Run(name, func(t *testing.T) {
					task := oracle.NewTask("task", "workspace", 1).
						WithPolicyOverride(override).
						WithLaunchNow(launchNow)
					result := oracle.Evaluate(oracle.NewSnapshot(projectPolicy, roomyLimits(), oracle.NewUsage(0, 0, 0, 0), task))
					effectiveAutomatic := projectPolicy == oracle.PolicyAutomatic
					if override == oracle.PolicyForceManual {
						effectiveAutomatic = false
					}
					if override == oracle.PolicyForceAutomatic {
						effectiveAutomatic = true
					}
					if launchNow || effectiveAutomatic {
						assertOrder(t, result, "task")
						wantCode := oracle.CodeSelectedAutomatic
						if launchNow {
							wantCode = oracle.CodeSelectedLaunchNow
						}
						if code := resultCode(t, result, "task"); code != wantCode {
							t.Fatalf("selected code = %q, want %q", code, wantCode)
						}
						return
					}
					assertOrder(t, result)
					if code := resultCode(t, result, "task"); code != oracle.CodeManualPolicyWait {
						t.Fatalf("manual policy code = %q", code)
					}
				})
			}
		}
	}
}

func TestDependencyRequiresSeparateAuditedOverride(t *testing.T) {
	waiting := oracle.NewTask("waiting", "workspace", 1).
		WithLaunchNow(true).
		WithDependency(oracle.DependenciesWaiting)
	overridden := oracle.NewTask("overridden", "workspace", 2).
		WithDependency(oracle.DependenciesAuditedOverride)
	result := oracle.Evaluate(oracle.NewSnapshot(oracle.PolicyAutomatic, roomyLimits(), oracle.NewUsage(0, 0, 0, 0), waiting, overridden))
	assertOrder(t, result, "overridden")
	if code := resultCode(t, result, "waiting"); code != oracle.CodeDependencyWait {
		t.Fatalf("Launch now bypassed dependency: %q", code)
	}
}

func TestOrderingProgressionLaunchNowPriorityFIFOAndStableID(t *testing.T) {
	tasks := []oracle.TaskFacts{
		oracle.NewTask("normal", "workspace", 5),
		oracle.NewTask("low-launch", "workspace", 10).WithPriority(oracle.PriorityLow).WithLaunchNow(true),
		oracle.NewTask("urgent-late", "workspace", 9).WithPriority(oracle.PriorityUrgent),
		oracle.NewTask("urgent-a", "workspace", 1).WithPriority(oracle.PriorityUrgent),
		oracle.NewTask("urgent-b", "workspace", 1).WithPriority(oracle.PriorityUrgent),
		oracle.NewTask("high", "workspace", 0).WithPriority(oracle.PriorityHigh),
		oracle.NewTask("low", "workspace", 0).WithPriority(oracle.PriorityLow),
		oracle.NewTask("progress", "workspace", 99).
			WithPriority(oracle.PriorityLow).
			AsActiveRunProgression(oracle.ProgressionDemand(1, 0)),
	}
	result := oracle.Evaluate(oracle.NewSnapshot(oracle.PolicyAutomatic, roomyLimits(), oracle.NewUsage(1, 0, 1, 0), tasks...))
	assertOrder(t, result,
		"progress",
		"low-launch",
		"urgent-a",
		"urgent-b",
		"urgent-late",
		"high",
		"normal",
		"low",
	)
	if code := resultCode(t, result, "progress"); code != oracle.CodeSelectedActiveProgression {
		t.Fatalf("progression code = %q", code)
	}
}

func TestNoPreemptionAndNoDuplicateActiveRun(t *testing.T) {
	launchNow := oracle.NewTask("launch", "workspace", 0).WithLaunchNow(true)
	full := oracle.NewSnapshot(
		oracle.PolicyAutomatic,
		oracle.NewLimits(1, 1, 1, 1),
		oracle.NewUsage(1, 0, 1, 0),
		launchNow,
	).WithWorkspaceUsage(oracle.NewWorkspaceUsage("workspace", 1, 0))
	result := oracle.Evaluate(full)
	assertOrder(t, result)
	if code := resultCode(t, result, "launch"); code != oracle.CodeProjectCapacity {
		t.Fatalf("Launch now preempted capacity: %q", code)
	}

	duplicate := oracle.NewTask("duplicate", "workspace", 0).WithActiveRun(true)
	result = oracle.Evaluate(oracle.NewSnapshot(oracle.PolicyAutomatic, roomyLimits(), oracle.NewUsage(0, 0, 0, 0), duplicate))
	if code := resultCode(t, result, "duplicate"); code != oracle.CodeDuplicateActiveRun {
		t.Fatalf("duplicate Run code = %q", code)
	}
	missing := oracle.NewTask("missing", "workspace", 0).
		AsActiveRunProgression(oracle.ProgressionDemand(0, 0)).
		WithActiveRun(false)
	result = oracle.Evaluate(oracle.NewSnapshot(oracle.PolicyAutomatic, roomyLimits(), oracle.NewUsage(0, 0, 0, 0), missing))
	if code := resultCode(t, result, "missing"); code != oracle.CodeFactsAmbiguous {
		t.Fatalf("progression without active Run = %q", code)
	}
}

func TestActiveRunProgressionUsesRemainingAgentCapacityWithoutCreatingAnotherTask(t *testing.T) {
	progress := oracle.NewTask("progress", "workspace", 10).
		AsActiveRunProgression(oracle.ProgressionDemand(1, 0))
	launch := oracle.NewTask("launch", "other", 0).WithLaunchNow(true)
	snapshot := oracle.NewSnapshot(
		oracle.PolicyAutomatic,
		oracle.NewLimits(1, 1, 2, 2),
		oracle.NewUsage(1, 0, 1, 0),
		launch,
		progress,
	).WithWorkspaceUsage(oracle.NewWorkspaceUsage("workspace", 1, 0))
	result := oracle.Evaluate(snapshot)
	assertOrder(t, result, "progress")
	if code := resultCode(t, result, "launch"); code != oracle.CodeProjectCapacity {
		t.Fatalf("new Run was not kept behind the non-preempting capacity gate: %q", code)
	}
}

func TestCapacityAndReservations(t *testing.T) {
	newTask := oracle.NewTask("task", "workspace", 0)
	progressHelper := oracle.NewTask("helper", "workspace", 0).
		AsActiveRunProgression(oracle.ProgressionDemand(1, 1)).
		WithHelperUsage(2, 1)
	tests := []struct {
		name     string
		snapshot oracle.Snapshot
		taskID   oracle.TaskID
		wantCode oracle.Code
	}{
		{
			name:     "project reservations",
			snapshot: oracle.NewSnapshot(oracle.PolicyAutomatic, oracle.DefaultLimits(), oracle.NewUsage(3, 1, 0, 0), newTask),
			taskID:   "task", wantCode: oracle.CodeProjectCapacity,
		},
		{
			name: "workspace reservations",
			snapshot: oracle.NewSnapshot(oracle.PolicyAutomatic, oracle.DefaultLimits(), oracle.NewUsage(0, 0, 0, 0), newTask).
				WithWorkspaceUsage(oracle.NewWorkspaceUsage("workspace", 1, 1)),
			taskID: "task", wantCode: oracle.CodeWorkspaceCapacity,
		},
		{
			name:     "agent reservations",
			snapshot: oracle.NewSnapshot(oracle.PolicyAutomatic, oracle.DefaultLimits(), oracle.NewUsage(0, 0, 7, 1), newTask),
			taskID:   "task", wantCode: oracle.CodeAgentCapacity,
		},
		{
			name:     "helper reservations",
			snapshot: oracle.NewSnapshot(oracle.PolicyAutomatic, oracle.DefaultLimits(), oracle.NewUsage(1, 0, 1, 0), progressHelper),
			taskID:   "helper", wantCode: oracle.CodeHelperCapacity,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			result := oracle.Evaluate(test.snapshot)
			assertOrder(t, result)
			if code := resultCode(t, result, test.taskID); code != test.wantCode {
				t.Fatalf("capacity code = %q, want %q", code, test.wantCode)
			}
		})
	}

	first := oracle.NewTask("first", "workspace-a", 0)
	second := oracle.NewTask("second", "workspace-b", 1)
	result := oracle.Evaluate(oracle.NewSnapshot(oracle.PolicyAutomatic, oracle.NewLimits(1, 1, 2, 1), oracle.NewUsage(0, 0, 0, 0), second, first))
	assertOrder(t, result, "first")
	if code := resultCode(t, result, "second"); code != oracle.CodeProjectCapacity {
		t.Fatalf("later reservation code = %q", code)
	}
}

func TestSoftAcknowledgementAndHardBudgets(t *testing.T) {
	baseTime := oracle.NewBudget(100, 0, 0, 1, oracle.AcknowledgementNone)
	baseCost := oracle.NewBudget(100, 0, 0, 1, oracle.AcknowledgementNone)
	baseCI := oracle.NewBudget(10, 0, 0, 1, oracle.AcknowledgementNone)
	tests := []struct {
		name string
		time oracle.Budget
		cost oracle.Budget
		ci   oracle.Budget
		want oracle.Code
	}{
		{"time below soft", oracle.NewBudget(100, 83, 0, 1, oracle.AcknowledgementNone), baseCost, baseCI, oracle.CodeSelectedAutomatic},
		{"time reaches soft through reservation", oracle.NewBudget(100, 83, 1, 1, oracle.AcknowledgementNone), baseCost, baseCI, oracle.CodeTimeSoftBudget},
		{"time soft acknowledged", oracle.NewBudget(100, 84, 0, 1, oracle.AcknowledgementCurrent), baseCost, baseCI, oracle.CodeSelectedAutomatic},
		{"time acknowledgement stale", oracle.NewBudget(100, 84, 0, 1, oracle.AcknowledgementStale), baseCost, baseCI, oracle.CodeFactsStale},
		{"time hard", oracle.NewBudget(100, 98, 1, 1, oracle.AcknowledgementCurrent), baseCost, baseCI, oracle.CodeTimeHardBudget},
		{"cost soft", baseTime, oracle.NewBudget(100, 84, 0, 1, oracle.AcknowledgementNone), baseCI, oracle.CodeCostSoftBudget},
		{"cost hard", baseTime, oracle.NewBudget(100, 99, 0, 1, oracle.AcknowledgementCurrent), baseCI, oracle.CodeCostHardBudget},
		{"ci reservation hard", baseTime, baseCost, oracle.NewBudget(10, 8, 1, 1, oracle.AcknowledgementNone), oracle.CodeCIHardBudget},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			task := oracle.NewTask("task", "workspace", 0).
				WithBudgets(oracle.NewBudgets(test.time, test.cost, test.ci))
			result := oracle.Evaluate(oracle.NewSnapshot(oracle.PolicyAutomatic, roomyLimits(), oracle.NewUsage(0, 0, 0, 0), task))
			if code := resultCode(t, result, "task"); code != test.want {
				t.Fatalf("budget code = %q, want %q", code, test.want)
			}
		})
	}
}

func TestUnavailableStaleAndAmbiguousBudgetFactsFailClosed(t *testing.T) {
	ready := oracle.NewBudget(100, 0, 0, 1, oracle.AcknowledgementNone)
	tests := []struct {
		name string
		time oracle.Budget
		cost oracle.Budget
		ci   oracle.Budget
		want oracle.Code
	}{
		{"time unavailable", ready.WithState(oracle.BudgetUnavailable), ready, ready, oracle.CodeTimeBudgetUnavailable},
		{"cost unavailable", ready, ready.WithState(oracle.BudgetUnavailable), ready, oracle.CodeCostBudgetUnavailable},
		{"ci unavailable", ready, ready, ready.WithState(oracle.BudgetUnavailable), oracle.CodeCIBudgetUnavailable},
		{"time stale", ready.WithState(oracle.BudgetStale), ready, ready, oracle.CodeFactsStale},
		{"cost ambiguous", ready, ready.WithState(oracle.BudgetAmbiguous), ready, oracle.CodeFactsAmbiguous},
		{"ci stale", ready, ready, ready.WithState(oracle.BudgetStale), oracle.CodeFactsStale},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			task := oracle.NewTask("task", "workspace", 0).
				WithBudgets(oracle.NewBudgets(test.time, test.cost, test.ci))
			result := oracle.Evaluate(oracle.NewSnapshot(oracle.PolicyAutomatic, roomyLimits(), oracle.NewUsage(0, 0, 0, 0), task))
			assertOrder(t, result)
			if code := resultCode(t, result, "task"); code != test.want {
				t.Fatalf("budget fact code = %q, want %q", code, test.want)
			}
		})
	}
}

func TestUnavailableLostStaleAndAmbiguousFactsFailClosed(t *testing.T) {
	task := oracle.NewTask("task", "workspace", 0)
	base := oracle.NewSnapshot(oracle.PolicyAutomatic, roomyLimits(), oracle.NewUsage(0, 0, 0, 0), task)
	tests := []struct {
		name   string
		facts  oracle.Snapshot
		wanted oracle.Code
	}{
		{"inactive", base.WithProjectActive(false), oracle.CodeProjectInactive},
		{"lease lost", base.WithLease(oracle.LeaseLost), oracle.CodeLeaseLost},
		{"lease stale", base.WithLease(oracle.LeaseStale), oracle.CodeFactsStale},
		{"lease ambiguous", base.WithLease(oracle.LeaseAmbiguous), oracle.CodeFactsAmbiguous},
		{"disk unavailable", base.WithDisk(oracle.DiskUnavailable), oracle.CodeDiskUnavailable},
		{"disk exceeded", base.WithDisk(oracle.DiskLimitExceeded), oracle.CodeDiskLimit},
		{"disk stale", base.WithDisk(oracle.DiskStale), oracle.CodeFactsStale},
		{"disk ambiguous", base.WithDisk(oracle.DiskAmbiguous), oracle.CodeFactsAmbiguous},
		{"provider unavailable", base.WithProvider(oracle.ProviderUnavailable), oracle.CodeProviderUnavailable},
		{"provider denied", base.WithProvider(oracle.ProviderNotAdmitted), oracle.CodeProviderNotAdmitted},
		{"provider stale", base.WithProvider(oracle.ProviderStale), oracle.CodeFactsStale},
		{"provider ambiguous", base.WithProvider(oracle.ProviderAmbiguous), oracle.CodeFactsAmbiguous},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			result := oracle.Evaluate(test.facts)
			assertOrder(t, result)
			if code := resultCode(t, result, "task"); code != test.wanted {
				t.Fatalf("code = %q, want %q", code, test.wanted)
			}
		})
	}
}

func TestRequiredNewRunFacts(t *testing.T) {
	tests := []struct {
		name string
		task oracle.TaskFacts
		want oracle.Code
	}{
		{"complete", oracle.NewTask("task", "workspace", 0).WithRequiredFacts(false, true, true), oracle.CodeTaskIncomplete},
		{"organizer", oracle.NewTask("task", "workspace", 0).WithRequiredFacts(true, false, true), oracle.CodeOrganizerUnapproved},
		{"preflight", oracle.NewTask("task", "workspace", 0).WithRequiredFacts(true, true, false), oracle.CodePreflightNotReady},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			result := oracle.Evaluate(oracle.NewSnapshot(oracle.PolicyAutomatic, roomyLimits(), oracle.NewUsage(0, 0, 0, 0), test.task))
			if code := resultCode(t, result, "task"); code != test.want {
				t.Fatalf("code = %q, want %q", code, test.want)
			}
		})
	}
}

func TestContradictoryInvalidAndOverflowingFactsAreAmbiguous(t *testing.T) {
	tests := []struct {
		name     string
		snapshot oracle.Snapshot
	}{
		{
			"duplicate Task identity",
			oracle.NewSnapshot(oracle.PolicyAutomatic, roomyLimits(), oracle.NewUsage(0, 0, 0, 0),
				oracle.NewTask("same", "one", 0),
				oracle.NewTask("same", "two", 1)),
		},
		{
			"duplicate Workspace observation",
			oracle.NewSnapshot(oracle.PolicyAutomatic, roomyLimits(), oracle.NewUsage(0, 0, 0, 0), oracle.NewTask("task", "one", 0)).
				WithWorkspaceUsage(
					oracle.NewWorkspaceUsage("one", 0, 0),
					oracle.NewWorkspaceUsage("one", 0, 0),
				),
		},
		{
			"invalid priority",
			oracle.NewSnapshot(oracle.PolicyAutomatic, roomyLimits(), oracle.NewUsage(0, 0, 0, 0),
				oracle.NewTask("task", "one", 0).WithPriority(oracle.Priority(0))),
		},
		{
			"capacity overflow",
			oracle.NewSnapshot(oracle.PolicyAutomatic, roomyLimits(), oracle.NewUsage(math.MaxUint64, 1, 0, 0),
				oracle.NewTask("task", "one", 0)),
		},
		{
			"budget overflow",
			oracle.NewSnapshot(oracle.PolicyAutomatic, roomyLimits(), oracle.NewUsage(0, 0, 0, 0),
				oracle.NewTask("task", "one", 0).WithBudgets(oracle.NewBudgets(
					oracle.NewBudget(math.MaxUint64, math.MaxUint64, 1, 0, oracle.AcknowledgementCurrent),
					oracle.NewBudget(100, 0, 0, 1, oracle.AcknowledgementNone),
					oracle.NewBudget(10, 0, 0, 1, oracle.AcknowledgementNone),
				))),
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			result := oracle.Evaluate(test.snapshot)
			assertOrder(t, result)
			for _, explanation := range result.Explanations {
				if explanation.Code != oracle.CodeFactsAmbiguous {
					t.Fatalf("explanation = %#v, want ambiguous", explanation)
				}
			}
		})
	}
}

func TestSnapshotCopiesInputAndEvaluationIsRepeatable(t *testing.T) {
	tasks := []oracle.TaskFacts{oracle.NewTask("original", "workspace", 0)}
	usages := []oracle.WorkspaceUsage{oracle.NewWorkspaceUsage("workspace", 0, 0)}
	snapshot := oracle.NewSnapshot(oracle.PolicyAutomatic, roomyLimits(), oracle.NewUsage(0, 0, 0, 0), tasks...).WithWorkspaceUsage(usages...)
	want := oracle.Evaluate(snapshot)
	tasks[0] = oracle.NewTask("replacement", "workspace", 0)
	usages[0] = oracle.NewWorkspaceUsage("workspace", 100, 100)
	for iteration := 0; iteration < 100; iteration++ {
		if got := oracle.Evaluate(snapshot); !reflect.DeepEqual(got, want) {
			t.Fatalf("iteration %d changed immutable result: got %#v want %#v", iteration, got, want)
		}
	}
}
