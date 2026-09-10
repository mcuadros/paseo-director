// SPDX-License-Identifier: Apache-2.0

package scheduleroracle

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"reflect"
	"slices"
	"testing"
)

func TestExhaustiveSmallPolicyDependencyAndActiveRunMatrix(t *testing.T) {
	policies := []LaunchPolicy{PolicyManual, PolicyAutomatic}
	overrides := []PolicyOverride{PolicyInherit, PolicyForceManual, PolicyForceAutomatic}
	launchRequests := []bool{false, true}
	dependencies := []DependencyState{DependenciesSatisfied, DependenciesWaiting, DependenciesAuditedOverride}
	activeRuns := []bool{false, true}

	caseCount := 0
	for _, policy := range policies {
		for _, override := range overrides {
			for _, launchNow := range launchRequests {
				for _, dependency := range dependencies {
					for _, activeRun := range activeRuns {
						caseCount++
						task := NewTask("task", "workspace", 0).
							WithPolicyOverride(override).
							WithLaunchNow(launchNow).
							WithDependency(dependency).
							WithActiveRun(activeRun)
						result := Evaluate(NewSnapshot(policy, DefaultLimits(), NewUsage(0, 0, 0, 0), task))

						want := CodeSelectedAutomatic
						if activeRun {
							want = CodeDuplicateActiveRun
						} else if dependency == DependenciesWaiting {
							want = CodeDependencyWait
						} else if !launchNow && effectivePolicy(policy, override) == PolicyManual {
							want = CodeManualPolicyWait
						} else if launchNow {
							want = CodeSelectedLaunchNow
						}
						if got := explanationFor(result, "task"); got != want {
							t.Fatalf("policy=%v override=%v launch=%v dependency=%v active=%v: got %q want %q", policy, override, launchNow, dependency, activeRun, got, want)
						}
					}
				}
			}
		}
	}
	if caseCount != 72 {
		t.Fatalf("exercised %d cases, want 72", caseCount)
	}

	for _, policy := range policies {
		for _, dependency := range dependencies {
			progress := NewTask("progress", "workspace", 0).
				WithDependency(dependency).
				AsActiveRunProgression(ProgressionDemand(0, 0))
			result := Evaluate(NewSnapshot(policy, DefaultLimits(), NewUsage(1, 0, 0, 0), progress))
			if got := explanationFor(result, "progress"); got != CodeSelectedActiveProgression {
				t.Fatalf("active progression policy=%v dependency=%v: %q", policy, dependency, got)
			}
		}
	}
}

func TestExhaustiveSmallCapacityAndReservationMatrices(t *testing.T) {
	const limit = uint64(3)
	for active := uint64(0); active <= limit+1; active++ {
		for reserved := uint64(0); reserved <= limit+1; reserved++ {
			name := fmt.Sprintf("project/%d/%d", active, reserved)
			t.Run(name, func(t *testing.T) {
				task := NewTask("task", "workspace", 0)
				result := Evaluate(NewSnapshot(PolicyAutomatic, NewLimits(limit, 9, 9, 9), NewUsage(active, reserved, 0, 0), task))
				want := CodeSelectedAutomatic
				if active+reserved+1 > limit {
					want = CodeProjectCapacity
				}
				if got := explanationFor(result, "task"); got != want {
					t.Fatalf("project active=%d reserved=%d: got %q want %q", active, reserved, got, want)
				}
			})

			t.Run(fmt.Sprintf("workspace/%d/%d", active, reserved), func(t *testing.T) {
				task := NewTask("task", "workspace", 0)
				snapshot := NewSnapshot(PolicyAutomatic, NewLimits(9, limit, 9, 9), NewUsage(0, 0, 0, 0), task).
					WithWorkspaceUsage(NewWorkspaceUsage("workspace", active, reserved))
				want := CodeSelectedAutomatic
				if active+reserved+1 > limit {
					want = CodeWorkspaceCapacity
				}
				if got := explanationFor(Evaluate(snapshot), "task"); got != want {
					t.Fatalf("workspace active=%d reserved=%d: got %q want %q", active, reserved, got, want)
				}
			})

			t.Run(fmt.Sprintf("agent/%d/%d", active, reserved), func(t *testing.T) {
				task := NewTask("task", "workspace", 0)
				snapshot := NewSnapshot(PolicyAutomatic, NewLimits(9, 9, limit, 9), NewUsage(0, 0, active, reserved), task)
				want := CodeSelectedAutomatic
				if active+reserved+1 > limit {
					want = CodeAgentCapacity
				}
				if got := explanationFor(Evaluate(snapshot), "task"); got != want {
					t.Fatalf("agents active=%d reserved=%d: got %q want %q", active, reserved, got, want)
				}
			})

			t.Run(fmt.Sprintf("helper/%d/%d", active, reserved), func(t *testing.T) {
				task := NewTask("task", "workspace", 0).
					AsActiveRunProgression(ProgressionDemand(1, 1)).
					WithHelperUsage(active, reserved)
				snapshot := NewSnapshot(PolicyAutomatic, NewLimits(9, 9, 9, limit), NewUsage(1, 0, 0, 0), task)
				want := CodeSelectedActiveProgression
				if active+reserved+1 > limit {
					want = CodeHelperCapacity
				}
				if got := explanationFor(Evaluate(snapshot), "task"); got != want {
					t.Fatalf("helpers active=%d reserved=%d: got %q want %q", active, reserved, got, want)
				}
			})
		}
	}
}

func TestEveryInputPermutationHasTheSameOutput(t *testing.T) {
	tasks := []TaskFacts{
		NewTask("normal", "workspace-a", 3),
		NewTask("urgent", "workspace-b", 2).WithPriority(PriorityUrgent),
		NewTask("launch", "workspace-c", 1).WithLaunchNow(true),
		NewTask("blocked", "workspace-d", 0).WithDependency(DependenciesWaiting),
		NewTask("progress", "workspace-e", 4).AsActiveRunProgression(ProgressionDemand(1, 0)),
	}
	want := Evaluate(NewSnapshot(PolicyAutomatic, roomyModelLimits(), NewUsage(1, 0, 1, 0), tasks...))
	permutationCount := 0
	forEachPermutation(tasks, func(permutation []TaskFacts) {
		permutationCount++
		got := Evaluate(NewSnapshot(PolicyAutomatic, roomyModelLimits(), NewUsage(1, 0, 1, 0), permutation...))
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("permutation %d changed output: got %#v want %#v", permutationCount, got, want)
		}
	})
	if permutationCount != 120 {
		t.Fatalf("permutation count = %d, want 120", permutationCount)
	}
}

func TestMetamorphicProperties(t *testing.T) {
	baseTasks := []TaskFacts{
		NewTask("urgent", "one", 1).WithPriority(PriorityUrgent),
		NewTask("normal", "two", 2),
		NewTask("low", "three", 3).WithPriority(PriorityLow),
	}
	roomy := NewSnapshot(PolicyAutomatic, roomyModelLimits(), NewUsage(0, 0, 0, 0), baseTasks...)
	base := Evaluate(roomy)

	withBlocked := append(append([]TaskFacts(nil), baseTasks...), NewTask("blocked", "four", -100).
		WithPriority(PriorityUrgent).
		WithDependency(DependenciesWaiting))
	blockedResult := Evaluate(NewSnapshot(PolicyAutomatic, roomyModelLimits(), NewUsage(0, 0, 0, 0), withBlocked...))
	if !slices.Equal(blockedResult.OrderedTaskIDs, base.OrderedTaskIDs) {
		t.Fatalf("ineligible Task changed selected order: got %v want %v", blockedResult.OrderedTaskIDs, base.OrderedTaskIDs)
	}

	shifted := []TaskFacts{
		NewTask("urgent", "one", 101).WithPriority(PriorityUrgent),
		NewTask("normal", "two", 102),
		NewTask("low", "three", 103).WithPriority(PriorityLow),
	}
	shiftedResult := Evaluate(NewSnapshot(PolicyAutomatic, roomyModelLimits(), NewUsage(0, 0, 0, 0), shifted...))
	if !slices.Equal(shiftedResult.OrderedTaskIDs, base.OrderedTaskIDs) {
		t.Fatalf("uniform queuedAt shift changed order: got %v want %v", shiftedResult.OrderedTaskIDs, base.OrderedTaskIDs)
	}

	limited := Evaluate(NewSnapshot(PolicyAutomatic, NewLimits(2, 2, 9, 9), NewUsage(0, 0, 0, 0), baseTasks...))
	if !slices.Equal(limited.OrderedTaskIDs, base.OrderedTaskIDs[:2]) {
		t.Fatalf("capacity relaxation does not preserve selected prefix: limited=%v roomy=%v", limited.OrderedTaskIDs, base.OrderedTaskIDs)
	}

	consumed := Evaluate(NewSnapshot(PolicyAutomatic, NewLimits(4, 4, 8, 3), NewUsage(2, 0, 3, 0), NewTask("task", "one", 0)))
	reserved := Evaluate(NewSnapshot(PolicyAutomatic, NewLimits(4, 4, 8, 3), NewUsage(0, 2, 0, 3), NewTask("task", "one", 0)))
	if !reflect.DeepEqual(consumed, reserved) {
		t.Fatalf("capacity consumption/reservation split changed result: consumed=%#v reserved=%#v", consumed, reserved)
	}

	consumedBudget := NewTask("budget", "one", 0).WithBudgets(NewBudgets(
		NewBudget(100, 80, 0, 1, AcknowledgementNone),
		NewBudget(100, 0, 0, 1, AcknowledgementNone),
		NewBudget(10, 2, 0, 1, AcknowledgementNone),
	))
	reservedBudget := NewTask("budget", "one", 0).WithBudgets(NewBudgets(
		NewBudget(100, 0, 80, 1, AcknowledgementNone),
		NewBudget(100, 0, 0, 1, AcknowledgementNone),
		NewBudget(10, 0, 2, 1, AcknowledgementNone),
	))
	left := Evaluate(NewSnapshot(PolicyAutomatic, roomyModelLimits(), NewUsage(0, 0, 0, 0), consumedBudget))
	right := Evaluate(NewSnapshot(PolicyAutomatic, roomyModelLimits(), NewUsage(0, 0, 0, 0), reservedBudget))
	if !reflect.DeepEqual(left, right) {
		t.Fatalf("budget consumption/reservation split changed result: used=%#v reserved=%#v", left, right)
	}
}

func TestSeededModelVectorsAreRepeatableAndPinned(t *testing.T) {
	first := seededCorpus(0x5ced_0213, 256)
	second := seededCorpus(0x5ced_0213, 256)
	if !reflect.DeepEqual(first, second) {
		t.Fatal("same deterministic seed produced different vectors")
	}
	encoded, err := json.Marshal(first)
	if err != nil {
		t.Fatalf("marshal seeded results: %v", err)
	}
	digest := sha256.Sum256(encoded)
	const expectedDigest = "614d1f4f01c46ef9d22f213a1ad7b5d5eb3acb67fea2182505e15eec6ce1da42"
	if got := hex.EncodeToString(digest[:]); got != expectedDigest {
		t.Fatalf("seeded corpus digest = %s, want %s", got, expectedDigest)
	}
}

func TestDeletingEveryOrderingAndCapacityGuardChangesItsWitness(t *testing.T) {
	orderingLimits := roomyModelLimits()
	witnesses := []struct {
		guard    guard
		snapshot Snapshot
	}{
		{
			guardOrderProgression,
			NewSnapshot(PolicyAutomatic, orderingLimits, NewUsage(1, 0, 0, 0),
				NewTask("new", "one", 0),
				NewTask("progress", "two", 0).AsActiveRunProgression(ProgressionDemand(0, 0))),
		},
		{
			guardOrderLaunchNow,
			NewSnapshot(PolicyAutomatic, orderingLimits, NewUsage(0, 0, 0, 0),
				NewTask("a-regular", "one", 0),
				NewTask("z-launch", "two", 0).WithLaunchNow(true)),
		},
		{
			guardOrderPriority,
			NewSnapshot(PolicyAutomatic, orderingLimits, NewUsage(0, 0, 0, 0),
				NewTask("low", "one", 0).WithPriority(PriorityLow),
				NewTask("urgent", "two", 0).WithPriority(PriorityUrgent)),
		},
		{
			guardOrderFIFO,
			NewSnapshot(PolicyAutomatic, orderingLimits, NewUsage(0, 0, 0, 0),
				NewTask("a-late", "one", 2),
				NewTask("z-early", "two", 1)),
		},
		{
			guardOrderStableID,
			NewSnapshot(PolicyAutomatic, orderingLimits, NewUsage(0, 0, 0, 0),
				NewTask("b", "one", 0),
				NewTask("a", "two", 0)),
		},
		{
			guardProjectCapacity,
			NewSnapshot(PolicyAutomatic, NewLimits(1, 9, 9, 9), NewUsage(1, 0, 0, 0), NewTask("task", "one", 0)),
		},
		{
			guardWorkspaceCapacity,
			NewSnapshot(PolicyAutomatic, NewLimits(9, 1, 9, 9), NewUsage(0, 0, 0, 0), NewTask("task", "one", 0)).
				WithWorkspaceUsage(NewWorkspaceUsage("one", 1, 0)),
		},
		{
			guardAgentCapacity,
			NewSnapshot(PolicyAutomatic, NewLimits(9, 9, 1, 9), NewUsage(0, 0, 1, 0), NewTask("task", "one", 0)),
		},
		{
			guardHelperCapacity,
			NewSnapshot(PolicyAutomatic, NewLimits(9, 9, 9, 1), NewUsage(1, 0, 0, 0),
				NewTask("task", "one", 0).AsActiveRunProgression(ProgressionDemand(1, 1)).WithHelperUsage(1, 0)),
		},
		{
			guardCapacityReservations,
			NewSnapshot(PolicyAutomatic, NewLimits(4, 9, 9, 9), NewUsage(3, 1, 0, 0), NewTask("task", "one", 0)),
		},
		{
			guardBudgetReservations,
			NewSnapshot(PolicyAutomatic, orderingLimits, NewUsage(0, 0, 0, 0),
				NewTask("task", "one", 0).WithBudgets(NewBudgets(
					NewBudget(100, 84, 1, 0, AcknowledgementNone),
					NewBudget(100, 0, 0, 1, AcknowledgementNone),
					NewBudget(10, 0, 0, 1, AcknowledgementNone),
				))),
		},
		{
			guardNoDuplicateActiveRun,
			NewSnapshot(PolicyAutomatic, orderingLimits, NewUsage(0, 0, 0, 0), NewTask("task", "one", 0).WithActiveRun(true)),
		},
	}

	if len(witnesses) != int(guardCount) {
		t.Fatalf("mutation witnesses = %d, want one for each of %d guards", len(witnesses), guardCount)
	}
	seen := make(map[guard]bool, guardCount)
	for _, witness := range witnesses {
		if seen[witness.guard] {
			t.Fatalf("duplicate mutation witness for guard %d", witness.guard)
		}
		seen[witness.guard] = true
		full := allGuards()
		want := evaluateWithGuards(witness.snapshot, full)
		full[witness.guard] = false
		mutant := evaluateWithGuards(witness.snapshot, full)
		if reflect.DeepEqual(mutant, want) {
			t.Fatalf("guard %d deletion survived: %#v", witness.guard, mutant)
		}
	}
}

func explanationFor(result Result, id TaskID) Code {
	for _, explanation := range result.Explanations {
		if explanation.TaskID == id {
			return explanation.Code
		}
	}
	return ""
}

func roomyModelLimits() Limits {
	return NewLimits(64, 64, 64, 16)
}

func forEachPermutation(values []TaskFacts, visit func([]TaskFacts)) {
	permutation := append([]TaskFacts(nil), values...)
	var generate func(int)
	generate = func(index int) {
		if index == len(permutation) {
			visit(append([]TaskFacts(nil), permutation...))
			return
		}
		for next := index; next < len(permutation); next++ {
			permutation[index], permutation[next] = permutation[next], permutation[index]
			generate(index + 1)
			permutation[index], permutation[next] = permutation[next], permutation[index]
		}
	}
	generate(0)
}

func seededCorpus(seed int64, vectorCount int) []Result {
	random := seededRandom{state: uint64(seed)}
	results := make([]Result, 0, vectorCount)
	for vector := 0; vector < vectorCount; vector++ {
		taskCount := 1 + random.intn(8)
		tasks := make([]TaskFacts, 0, taskCount)
		for index := 0; index < taskCount; index++ {
			task := NewTask(
				TaskID(fmt.Sprintf("v%03d-t%02d", vector, index)),
				WorkspaceID(fmt.Sprintf("workspace-%d", random.intn(3))),
				int64(random.intn(7)),
			).
				WithPriority(Priorities()[random.intn(len(Priorities()))]).
				WithPolicyOverride(PolicyOverride(1 + random.intn(3))).
				WithLaunchNow(random.intn(4) == 0).
				WithDependency(DependencyState(1 + random.intn(3)))
			if random.intn(4) == 0 {
				agents := uint64(random.intn(3))
				helpers := uint64(0)
				if agents > 0 {
					helpers = uint64(random.intn(int(agents + 1)))
				}
				task = task.AsActiveRunProgression(ProgressionDemand(agents, helpers)).
					WithHelperUsage(uint64(random.intn(3)), uint64(random.intn(2)))
			} else if random.intn(8) == 0 {
				task = task.WithActiveRun(true)
			}
			task = task.WithBudgets(NewBudgets(
				NewBudget(100, uint64(random.intn(100)), uint64(random.intn(4)), uint64(random.intn(3)), AcknowledgementState(1+random.intn(4))),
				NewBudget(100, uint64(random.intn(100)), uint64(random.intn(4)), uint64(random.intn(3)), AcknowledgementState(1+random.intn(4))),
				NewBudget(10, uint64(random.intn(10)), uint64(random.intn(3)), uint64(random.intn(2)), AcknowledgementNone),
			))
			tasks = append(tasks, task)
		}
		policy := LaunchPolicy(1 + random.intn(2))
		limits := NewLimits(uint64(1+random.intn(5)), uint64(1+random.intn(3)), uint64(1+random.intn(9)), uint64(1+random.intn(4)))
		usage := NewUsage(uint64(random.intn(4)), uint64(random.intn(2)), uint64(random.intn(7)), uint64(random.intn(3)))
		snapshot := NewSnapshot(policy, limits, usage, tasks...).WithWorkspaceUsage(
			NewWorkspaceUsage("workspace-0", uint64(random.intn(3)), uint64(random.intn(2))),
			NewWorkspaceUsage("workspace-1", uint64(random.intn(3)), uint64(random.intn(2))),
			NewWorkspaceUsage("workspace-2", uint64(random.intn(3)), uint64(random.intn(2))),
		)
		results = append(results, Evaluate(snapshot))
	}
	return results
}

type seededRandom struct {
	state uint64
}

func (random *seededRandom) intn(bound int) int {
	if bound <= 0 {
		panic("seeded random bound must be positive")
	}
	random.state += 0x9e3779b97f4a7c15
	value := random.state
	value = (value ^ (value >> 30)) * 0xbf58476d1ce4e5b9
	value = (value ^ (value >> 27)) * 0x94d049bb133111eb
	value ^= value >> 31
	return int(value % uint64(bound))
}
