// SPDX-License-Identifier: Apache-2.0

package executionoracle

import (
	"errors"
	"fmt"
	"sync"
	"testing"
)

func TestFakeRuntimeCrashesAndReplaysAtEveryBoundary(t *testing.T) {
	for _, crash := range CrashPoints() {
		for _, fixture := range fakeActionFixtures() {
			t.Run(fmt.Sprintf("%d/%s", crash, fixture.name), func(t *testing.T) {
				runtime := NewFakeRuntime(fixture.action.Scope)
				fixture.seed(t, runtime.Host)
				err := runtime.Execute("effect", fixture.action, crash)
				if !errors.Is(err, ErrInjectedCrash) {
					t.Fatalf("crash result = %v, want injected crash", err)
				}
				if err := runtime.Execute("effect", fixture.action, CrashNone); err != nil {
					t.Fatalf("replay: %v", err)
				}
				record, ok := runtime.Store.Record("effect")
				if !ok || record.Phase != EffectObserved || record.Action != fixture.action {
					t.Fatalf("record after replay = %#v, %v", record, ok)
				}
				if got := runtime.Host.MutationCount(fixture.action.TargetID); got != fixture.mutations {
					t.Fatalf("physical mutations = %d, want %d", got, fixture.mutations)
				}
				if err := runtime.Execute("effect", fixture.action, CrashNone); err != nil {
					t.Fatalf("completed replay: %v", err)
				}
				if got := runtime.Host.MutationCount(fixture.action.TargetID); got != fixture.mutations {
					t.Fatalf("completed replay mutated host: %d", got)
				}
			})
		}
	}
}

func TestFakeRuntimeConcurrentReplayCreatesAndPromptsOnce(t *testing.T) {
	scope := Baseline().Run.Scope
	runtime := NewFakeRuntime(scope)
	workspace := Action{Kind: ActionCreateWorkspace, Scope: scope, TargetID: scope.WorkspaceID, Reason: ReasonWorkspaceRequired}
	runConcurrent(t, 64, func(index int) error {
		return runtime.Execute(fmt.Sprintf("workspace-%d", index%4), workspace, CrashNone)
	})
	if got := runtime.Host.MutationCount(scope.WorkspaceID); got != 1 {
		t.Fatalf("workspace physical mutations = %d, want 1", got)
	}
	if len(runtime.Host.Resources()) != 1 {
		t.Fatalf("workspace resources = %#v", runtime.Host.Resources())
	}

	workerID := "worker"
	create := Action{Kind: ActionCreateWorkerBootstrap, Scope: scope, Role: RoleWorker, TargetID: workerID, Reason: ReasonWorkerRequired}
	runConcurrent(t, 64, func(index int) error {
		return runtime.Execute(fmt.Sprintf("worker-%d", index%4), create, CrashNone)
	})
	if got := runtime.Host.MutationCount(workerID); got != 1 {
		t.Fatalf("worker physical mutations = %d, want 1", got)
	}

	prompt := Action{Kind: ActionSendWorkerPrompt, Scope: scope, Role: RoleWorker, TargetID: workerID, Reason: ReasonWorkerPromptRequired, NotifyOnFinish: true}
	runConcurrent(t, 64, func(index int) error {
		return runtime.Execute(fmt.Sprintf("prompt-%d", index%4), prompt, CrashNone)
	})
	if got := runtime.Host.MutationCount(workerID); got != 2 {
		t.Fatalf("worker create+prompt mutations = %d, want 2", got)
	}
	resource, ok := runtime.Host.Resource(workerID)
	if !ok || !resource.Prompted || resource.Archived {
		t.Fatalf("worker resource = %#v, %v", resource, ok)
	}
	if len(runtime.Host.Resources()) != 2 {
		t.Fatalf("duplicate or orphan resource set = %#v", runtime.Host.Resources())
	}
}

func TestFakeRuntimeRejectsCrossRunAndConflictingReplayBeforeHostMutation(t *testing.T) {
	scope := Baseline().Run.Scope
	runtime := NewFakeRuntime(scope)
	action := Action{Kind: ActionCreateWorkspace, Scope: scope, TargetID: scope.WorkspaceID, Reason: ReasonWorkspaceRequired}
	crossRun := action
	crossRun.Scope.RunID = "other-run"
	if err := runtime.Execute("cross", crossRun, CrashNone); !errors.Is(err, ErrScopeViolation) {
		t.Fatalf("cross-Run result = %v", err)
	}
	if _, ok := runtime.Store.Record("cross"); ok {
		t.Fatal("cross-Run action reached durable intent")
	}
	if len(runtime.Host.Resources()) != 0 {
		t.Fatalf("cross-Run action mutated host: %#v", runtime.Host.Resources())
	}

	if err := runtime.Execute("same-key", action, CrashNone); err != nil {
		t.Fatal(err)
	}
	conflict := action
	conflict.Kind = ActionObserveAll
	conflict.TargetID = "all"
	conflict.Reason = ReasonObserveWake
	if err := runtime.Execute("same-key", conflict, CrashNone); !errors.Is(err, ErrEffectConflict) {
		t.Fatalf("conflicting replay result = %v", err)
	}
	if got := runtime.Host.MutationCount(scope.WorkspaceID); got != 1 {
		t.Fatalf("conflicting replay changed host: %d", got)
	}
}

func TestFakeHostRejectsOrphansDuplicatePrimariesAndSecondReplacement(t *testing.T) {
	scope := Baseline().Run.Scope
	host := NewFakeHost()
	if err := host.Seed(FakeResource{ID: "orphan", Scope: scope, Role: RoleWorker}); !errors.Is(err, ErrScopeViolation) {
		t.Fatalf("orphan Worker seed = %v", err)
	}
	if err := host.Seed(FakeResource{ID: scope.WorkspaceID, Scope: scope}); err != nil {
		t.Fatal(err)
	}
	runtime := &FakeRuntime{Store: NewFakeStore(), Host: host, Scope: scope}
	first := Action{Kind: ActionCreateWorkerBootstrap, Scope: scope, Role: RoleWorker, TargetID: "worker-0", Reason: ReasonWorkerRequired}
	if err := runtime.Execute("worker-0", first, CrashNone); err != nil {
		t.Fatal(err)
	}
	duplicate := first
	duplicate.TargetID = "worker-duplicate"
	if err := runtime.Execute("worker-duplicate", duplicate, CrashNone); !errors.Is(err, ErrEffectConflict) {
		t.Fatalf("duplicate primary = %v", err)
	}
	archive := Action{Kind: ActionArchiveWorker, Scope: scope, Role: RoleWorker, TargetID: "worker-0", Reason: ReasonReplacement}
	if err := runtime.Execute("archive-0", archive, CrashNone); err != nil {
		t.Fatal(err)
	}
	replace := Action{Kind: ActionReplaceWorkerBootstrap, Scope: scope, Role: RoleWorker, TargetID: "worker-1", Reason: ReasonReplacement}
	if err := runtime.Execute("replace-1", replace, CrashNone); err != nil {
		t.Fatal(err)
	}
	if err := runtime.Execute("archive-1", Action{Kind: ActionArchiveWorker, Scope: scope, Role: RoleWorker, TargetID: "worker-1", Reason: ReasonReplacement}, CrashNone); err != nil {
		t.Fatal(err)
	}
	second := replace
	second.TargetID = "worker-2"
	if err := runtime.Execute("replace-2", second, CrashNone); !errors.Is(err, ErrEffectConflict) {
		t.Fatalf("second replacement = %v", err)
	}
}

func TestFakeHostPreservesHelperParentAndScopeDuringContainment(t *testing.T) {
	snapshot := activeWorkerSnapshot()
	helper := helperFact(snapshot, "helper", "worker")
	snapshot.Agents = append(snapshot.Agents, helper)
	snapshot.Control = ControlFact{Kind: ControlEmergencyStop, RunID: snapshot.Run.Scope.RunID, HumanConfirmed: true}
	decision := Evaluate(snapshot)
	runtime := NewFakeRuntime(snapshot.Run.Scope)
	if err := runtime.Host.Seed(FakeResource{ID: snapshot.Run.Scope.WorkspaceID, Scope: snapshot.Run.Scope}); err != nil {
		t.Fatal(err)
	}
	if err := runtime.Host.Seed(FakeResource{ID: "worker", Scope: snapshot.Run.Scope, Role: RoleWorker, Prompted: true}); err != nil {
		t.Fatal(err)
	}
	if err := runtime.Host.Seed(FakeResource{ID: "helper", Scope: snapshot.Run.Scope, Role: RoleHelper, ParentID: "worker", Prompted: true}); err != nil {
		t.Fatal(err)
	}
	for index, action := range decision.Actions {
		if err := runtime.Execute(fmt.Sprintf("stop-%d", index), action, CrashNone); err != nil {
			t.Fatalf("execute %#v: %v", action, err)
		}
	}
	helperResource, _ := runtime.Host.Resource("helper")
	workerResource, _ := runtime.Host.Resource("worker")
	if !helperResource.Archived || helperResource.ParentID != "worker" || helperResource.Scope != snapshot.Run.Scope {
		t.Fatalf("helper containment lost parent/scope: %#v", helperResource)
	}
	if !workerResource.Archived {
		t.Fatalf("worker was not contained: %#v", workerResource)
	}
}

type fakeFixture struct {
	name      string
	action    Action
	seed      func(*testing.T, *FakeHost)
	mutations uint64
}

func fakeActionFixtures() []fakeFixture {
	scope := Baseline().Run.Scope
	seedNothing := func(*testing.T, *FakeHost) {}
	seedWorkspace := func(t *testing.T, host *FakeHost) {
		t.Helper()
		if err := host.Seed(FakeResource{ID: scope.WorkspaceID, Scope: scope}); err != nil {
			t.Fatal(err)
		}
	}
	seedWorker := func(t *testing.T, host *FakeHost) {
		t.Helper()
		seedWorkspace(t, host)
		if err := host.Seed(FakeResource{ID: "worker", Scope: scope, Role: RoleWorker}); err != nil {
			t.Fatal(err)
		}
	}
	return []fakeFixture{
		{name: "workspace", action: Action{Kind: ActionCreateWorkspace, Scope: scope, TargetID: scope.WorkspaceID, Reason: ReasonWorkspaceRequired}, seed: seedNothing, mutations: 1},
		{name: "worker", action: Action{Kind: ActionCreateWorkerBootstrap, Scope: scope, Role: RoleWorker, TargetID: "worker", Reason: ReasonWorkerRequired}, seed: seedWorkspace, mutations: 1},
		{name: "prompt", action: Action{Kind: ActionSendWorkerPrompt, Scope: scope, Role: RoleWorker, TargetID: "worker", Reason: ReasonWorkerPromptRequired, NotifyOnFinish: true}, seed: seedWorker, mutations: 1},
		{name: "observe", action: Action{Kind: ActionObserveAll, Scope: scope, TargetID: "all", Reason: ReasonObserveWake}, seed: seedNothing, mutations: 0},
		{name: "helper-admission", action: Action{Kind: ActionIssueHelperAdmission, Scope: scope, Role: RoleHelper, TargetID: "helper-intent", ParentID: "worker", Reason: ReasonHelperAdmission}, seed: seedNothing, mutations: 0},
	}
}

func runConcurrent(t *testing.T, count int, run func(int) error) {
	t.Helper()
	var group sync.WaitGroup
	errorsFound := make(chan error, count)
	for index := 0; index < count; index++ {
		group.Add(1)
		go func(current int) {
			defer group.Done()
			if err := run(current); err != nil {
				errorsFound <- err
			}
		}(index)
	}
	group.Wait()
	close(errorsFound)
	for err := range errorsFound {
		t.Errorf("concurrent operation: %v", err)
	}
}
