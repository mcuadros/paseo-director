// SPDX-License-Identifier: Apache-2.0

package executionoracle

import (
	"reflect"
	"slices"
	"testing"
)

func TestClosedFactAndActionVocabularies(t *testing.T) {
	facts := FactKinds()
	if len(facts) != 13 {
		t.Fatalf("fact kinds = %d, want 13", len(facts))
	}
	for index, kind := range facts {
		if !kind.valid() || int(kind) != index+1 {
			t.Fatalf("fact kind %d = %d", index, kind)
		}
	}
	actions := ActionKinds()
	if len(actions) != 20 {
		t.Fatalf("action kinds = %d, want 20", len(actions))
	}
	reasons := Reasons()
	seenReasons := make(map[Reason]bool, len(reasons))
	for _, reason := range reasons {
		if !reason.valid() || seenReasons[reason] {
			t.Fatalf("invalid or duplicate reason %q", reason)
		}
		seenReasons[reason] = true
	}
	seen := make(map[ActionKind]bool, len(actions))
	for _, kind := range actions {
		if !kind.valid() || seen[kind] {
			t.Fatalf("invalid or duplicate action kind %d", kind)
		}
		seen[kind] = true
	}
	if len(Roles()) != 4 || AllowedTools(RoleOrganizer) == 0 || AllowedTools(RoleWorker) == 0 ||
		AllowedTools(RoleReviewer) == 0 || AllowedTools(RoleHelper) == 0 {
		t.Fatal("closed role/tool vocabulary is incomplete")
	}
}

func TestNormalBootstrapPromptAndReviewSequence(t *testing.T) {
	snapshot := Baseline()
	assertSingleAction(t, Evaluate(snapshot), ActionCreateWorkspace)

	snapshot.Workspace.State = WorkspaceReady
	snapshot.Workspace.Visible = true
	assertSingleAction(t, Evaluate(snapshot), ActionCreateWorkerBootstrap)

	worker := workerFact(snapshot, "worker-1")
	worker.BootstrapDone = false
	snapshot.Agents = []AgentFact{worker}
	assertSingleAction(t, Evaluate(snapshot), ActionObserveWorker)

	snapshot.Agents[0].BootstrapDone = true
	workerPrompt := Evaluate(snapshot)
	assertSingleAction(t, workerPrompt, ActionSendWorkerPrompt)
	if !workerPrompt.Actions[0].NotifyOnFinish {
		t.Fatal("real Worker prompt omitted terminal notification")
	}

	snapshot.Agents[0].State = AgentRunning
	snapshot.Agents[0].Prompt = PromptSent
	assertSingleAction(t, Evaluate(snapshot), ActionAwaitNotification)

	snapshot.Agents[0].State = AgentIdle
	snapshot.Agents[0].Prompt = PromptFinished
	snapshot.Candidate = CandidateFact{ID: "candidate", RunID: snapshot.Run.Scope.RunID, WorkerID: "worker-1", State: CandidateCurrent}
	assertSingleAction(t, Evaluate(snapshot), ActionCreateReviewerBootstrap)

	reviewer := reviewerFact(snapshot, "reviewer-1")
	reviewer.BootstrapDone = true
	snapshot.Agents = append(snapshot.Agents, reviewer)
	reviewerPrompt := Evaluate(snapshot)
	assertSingleAction(t, reviewerPrompt, ActionSendReviewerPrompt)
	if !reviewerPrompt.Actions[0].NotifyOnFinish {
		t.Fatal("real Reviewer prompt omitted terminal notification")
	}

	notPrepared := Baseline()
	notPrepared.Run.PreparationReady = false
	assertParkReason(t, Evaluate(notPrepared), ReasonPreparationRequired)
	notVisible := Baseline()
	notVisible.Workspace.State = WorkspaceReady
	notVisible.Run.RootWorkerVisible = false
	assertParkReason(t, Evaluate(notVisible), ReasonWorkerVisibility)
}

func TestControlBudgetLeaseAndLivenessPrecedence(t *testing.T) {
	active := Baseline()
	active.Workspace.State = WorkspaceReady
	worker := workerFact(active, "worker")
	worker.BootstrapDone = true
	worker.State = AgentRunning
	worker.Prompt = PromptSent
	active.Agents = []AgentFact{worker}

	emergency := active.Clone()
	emergency.Control = ControlFact{Kind: ControlEmergencyStop, RunID: emergency.Run.Scope.RunID, HumanConfirmed: true}
	emergency.Usage.Time.Consumed = emergency.Usage.Time.Limit
	decision := Evaluate(emergency)
	if decision.Reason != ReasonEmergencyStop || !containsAction(decision, ActionArchiveWorker) || !containsAction(decision, ActionCancelRun) {
		t.Fatalf("emergency did not precede hard budget: %#v", decision)
	}
	emergency.MCP.Bindings[1].Tools = AllowedTools(RoleReviewer)
	emergency.Provider.ProfileRevision = "drifted"
	decision = Evaluate(emergency)
	if decision.Reason != ReasonEmergencyStop || !containsAction(decision, ActionArchiveWorker) {
		t.Fatalf("emergency was blocked by ordinary MCP/profile drift: %#v", decision)
	}

	lostLease := emergency.Clone()
	lostLease.Lease.State = LeaseLost
	lostLease.Lease.DispatchAllowed = false
	decision = Evaluate(lostLease)
	assertSingleAction(t, decision, ActionObserveAll)
	if decision.Reason != ReasonLeaseObserveOnly {
		t.Fatalf("lease loss reason = %q", decision.Reason)
	}

	cancelled := active.Clone()
	cancelled.Control = ControlFact{Kind: ControlCancel, RunID: cancelled.Run.Scope.RunID, HumanConfirmed: true}
	cancelled.Usage.Time.Consumed = cancelled.Usage.Time.Limit
	decision = Evaluate(cancelled)
	if decision.Reason != ReasonCancelled || !containsAction(decision, ActionCancelRun) {
		t.Fatalf("cancel did not precede hard budget: %#v", decision)
	}

	hard := active.Clone()
	hard.Usage.Time.Consumed = hard.Usage.Time.Limit
	decision = Evaluate(hard)
	assertSingleAction(t, decision, ActionParkNeedsYou)
	if decision.Reason != ReasonHardBudget {
		t.Fatalf("hard budget reason = %q", decision.Reason)
	}
	hardReservation := active.Clone()
	hardReservation.Usage.Time.Consumed = 99
	hardReservation.Usage.Time.NextReservation = 1
	assertParkReason(t, Evaluate(hardReservation), ReasonHardBudget)

	soft := active.Clone()
	soft.Usage.Time.Consumed = 84
	soft.Usage.Time.NextReservation = 1
	decision = Evaluate(soft)
	assertSingleAction(t, decision, ActionAwaitSafeBoundary)
	if decision.Reason != ReasonSoftBudget {
		t.Fatalf("soft budget reason = %q", decision.Reason)
	}

	soft.Usage.Time.AcknowledgedRevision = soft.Usage.Time.Revision
	assertSingleAction(t, Evaluate(soft), ActionAwaitNotification)
	resume := soft.Clone()
	resume.Usage.Time.AcknowledgedRevision = 0
	resume.Control.Kind = ControlResume
	assertSingleAction(t, Evaluate(resume), ActionResumeReconcile)
	pauseAtSoft := resume.Clone()
	pauseAtSoft.Control.Kind = ControlPause
	decision = Evaluate(pauseAtSoft)
	if decision.Reason != ReasonPaused {
		t.Fatalf("explicit pause did not precede soft budget: %#v", decision)
	}

	wake := active.Clone()
	wake.Liveness.State = LivenessTerminalWake
	wake.Liveness.TerminalLatencyMillis = 999
	decision = Evaluate(wake)
	assertSingleAction(t, decision, ActionObserveAll)
	if decision.Reason != ReasonObserveWake {
		t.Fatalf("terminal wake reason = %q", decision.Reason)
	}
	wake.Liveness.TerminalLatencyMillis = 1000
	assertParkReason(t, Evaluate(wake), ReasonLivenessAmbiguous)
	wake.Liveness.TerminalLatencyMillis = 999
	wake.Liveness.ActivePolling = true
	assertParkReason(t, Evaluate(wake), ReasonLivenessAmbiguous)

	stall := active.Clone()
	stall.Liveness = LivenessFact{
		State: LivenessCompoundStalled, QuietSeconds: 300, LocalSamples: 3, ExternalCycles: 2, IndependentSources: 2,
		ActiveExternalCadenceSeconds: 30, IdleExternalCadenceSeconds: 300,
	}
	decision = Evaluate(stall)
	assertSingleAction(t, decision, ActionObserveAll)
	if decision.Reason != ReasonObserveStall {
		t.Fatalf("compound stall reason = %q", decision.Reason)
	}
	stall.Liveness.KnownWait = true
	assertSingleAction(t, Evaluate(stall), ActionAwaitNotification)
}

func TestOwnershipHelperProfileAndReplacementRules(t *testing.T) {
	base := activeWorkerSnapshot()
	helperRequest := base.Clone()
	helperRequest.HelperRequest = HelperRequestFact{
		State: HelperRequested, RunID: helperRequest.Run.Scope.RunID,
		WorkspaceID: helperRequest.Run.Scope.WorkspaceID, ParentID: "worker", IntentID: "helper-intent",
	}
	assertSingleAction(t, Evaluate(helperRequest), ActionIssueHelperAdmission)
	helperRequest.HelperRequest.State = HelperAdmitted
	assertSingleAction(t, Evaluate(helperRequest), ActionObserveHelper)
	helperRequest.HelperRequest.State = HelperObserved
	helperRequest.HelperRequest.HelperID = "helper"
	helperRequest.Agents = append(helperRequest.Agents, helperFact(helperRequest, "helper", "worker"))
	assertSingleAction(t, Evaluate(helperRequest), ActionAwaitNotification)

	duplicate := base.Clone()
	second := workerFact(duplicate, "worker-2")
	second.BootstrapDone = true
	second.Prompt = PromptSent
	second.State = AgentRunning
	duplicate.Agents = append(duplicate.Agents, second)
	assertParkReason(t, Evaluate(duplicate), ReasonOwnershipViolation)

	helper := helperFact(base, "helper", "worker")
	helper.WorkspaceID = "other-workspace"
	wrongScope := base.Clone()
	wrongScope.Agents = append(wrongScope.Agents, helper)
	assertParkReason(t, Evaluate(wrongScope), ReasonScopeViolation)

	drift := base.Clone()
	drift.Provider.ProfileRevision = "profile-r2"
	assertParkReason(t, Evaluate(drift), ReasonProfileDrift)

	terminated := Baseline()
	terminated.Workspace.State = WorkspaceReady
	old := workerFact(terminated, "old-worker")
	old.State = AgentClosed
	old.BootstrapDone = true
	old.Prompt = PromptErrored
	old.Archived = true
	old.ProcessAbsent = true
	terminated.Agents = []AgentFact{old}
	terminated.Failure.Kind = FailureRecoverableWorker
	decision := Evaluate(terminated)
	assertSingleAction(t, decision, ActionReplaceWorkerBootstrap)
	if decision.Actions[0].TargetID != "worker:run:1" {
		t.Fatalf("replacement target = %q", decision.Actions[0].TargetID)
	}

	terminated.Run.ReplacementCount = 1
	assertParkReason(t, Evaluate(terminated), ReasonReplacementExhausted)

	notTerminated := terminated.Clone()
	notTerminated.Run.ReplacementCount = 0
	notTerminated.Agents[0].ProcessAbsent = false
	assertSingleAction(t, Evaluate(notTerminated), ActionObserveWorker)
}

func TestExhaustiveSmallStateMatrixIsDeterministicAndFailClosed(t *testing.T) {
	controls := []ControlKind{ControlNone, ControlPause, ControlResume, ControlCancel, ControlEmergencyStop}
	leases := []LeaseState{LeaseCurrent, LeaseLost, LeaseStale, LeaseAmbiguous}
	budgetUsed := []uint64{0, 84, 85, 100}
	workerCounts := []int{0, 1, 2}
	failures := []FailureKind{FailureNone, FailureRecoverableWorker, FailureUnknown}
	liveness := []LivenessState{LivenessHealthy, LivenessTerminalWake, LivenessCompoundStalled}

	cases := 0
	for _, control := range controls {
		for _, lease := range leases {
			for _, used := range budgetUsed {
				for _, workers := range workerCounts {
					for _, failure := range failures {
						for _, live := range liveness {
							cases++
							snapshot := Baseline()
							snapshot.Workspace.State = WorkspaceReady
							snapshot.Control = ControlFact{Kind: control, RunID: snapshot.Run.Scope.RunID, HumanConfirmed: control == ControlEmergencyStop}
							snapshot.Lease.State = lease
							snapshot.Lease.DispatchAllowed = lease == LeaseCurrent
							snapshot.Usage.Time.Consumed = used
							snapshot.Failure.Kind = failure
							snapshot.Liveness = LivenessFact{
								State: live, QuietSeconds: 300, LocalSamples: 3, ExternalCycles: 2, IndependentSources: 2,
								ActiveExternalCadenceSeconds: 30, IdleExternalCadenceSeconds: 300, TerminalLatencyMillis: 999,
							}
							for index := 0; index < workers; index++ {
								worker := workerFact(snapshot, string(rune('a'+index)))
								worker.BootstrapDone = true
								worker.State = AgentRunning
								worker.Prompt = PromptSent
								snapshot.Agents = append(snapshot.Agents, worker)
							}
							first := Evaluate(snapshot)
							second := Evaluate(snapshot)
							if !reflect.DeepEqual(first, second) {
								t.Fatalf("nondeterministic case %d: %#v != %#v", cases, first, second)
							}
							for _, action := range first.Actions {
								if action.Scope != snapshot.Run.Scope || !validAction(action) {
									t.Fatalf("case %d emitted invalid or cross-scope action %#v", cases, action)
								}
							}
							if lease != LeaseCurrent && containsHostMutation(first) {
								t.Fatalf("case %d dispatched with lease %d: %#v", cases, lease, first)
							}
							if workers == 2 && first.Reason != ReasonOwnershipViolation {
								t.Fatalf("case %d duplicated primary was not refused: %#v", cases, first)
							}
						}
					}
				}
			}
		}
	}
	if cases != 2160 {
		t.Fatalf("exhaustive cases = %d, want 2160", cases)
	}
}

func TestEveryOracleGuardHasAKillingWitness(t *testing.T) {
	witnesses := []struct {
		guard    guard
		snapshot Snapshot
	}{
		{guardClosedVocabulary, mutate(Baseline(), func(snapshot *Snapshot) { snapshot.Platform = 2 })},
		{guardFixedScope, mutate(activeWorkerSnapshot(), func(snapshot *Snapshot) { snapshot.Agents[0].RunID = "other" })},
		{guardWorkspaceOwnership, mutate(Baseline(), func(snapshot *Snapshot) { snapshot.Workspace.Owned = false })},
		{guardOnePrimary, duplicateWorkerSnapshot()},
		{guardHelperIsolation, invalidHelperSnapshot()},
		{guardCandidateOwnership, invalidCandidateSnapshot()},
		{guardFrozenProfile, mutate(Baseline(), func(snapshot *Snapshot) { snapshot.Provider.ProfileRevision = "other" })},
		{guardLease, mutate(Baseline(), func(snapshot *Snapshot) { snapshot.Lease.State = LeaseLost; snapshot.Lease.DispatchAllowed = false })},
		{guardProvider, mutate(Baseline(), func(snapshot *Snapshot) { snapshot.Provider.State = ProviderNotAdmitted })},
		{guardScopedMCP, mutate(Baseline(), func(snapshot *Snapshot) { snapshot.MCP.Bindings[1].Tools = AllowedTools(RoleReviewer) })},
		{guardHardBudget, mutate(Baseline(), func(snapshot *Snapshot) { snapshot.Usage.Time.Consumed = 100 })},
		{guardControl, mutate(Baseline(), func(snapshot *Snapshot) {
			snapshot.Control.Kind = ControlEmergencyStop
			snapshot.Control.HumanConfirmed = true
		})},
		{guardSoftBudget, mutate(Baseline(), func(snapshot *Snapshot) { snapshot.Usage.Time.Consumed = 84 })},
		{guardLiveness, mutate(Baseline(), func(snapshot *Snapshot) { snapshot.Liveness.State = LivenessTerminalWake })},
		{guardReplacement, replacementWitness()},
	}
	if len(witnesses) != int(guardCount) {
		t.Fatalf("mutation witnesses = %d, want %d", len(witnesses), guardCount)
	}
	seen := make(map[guard]bool, guardCount)
	for _, witness := range witnesses {
		if seen[witness.guard] {
			t.Fatalf("duplicate witness for guard %d", witness.guard)
		}
		seen[witness.guard] = true
		full := allGuards()
		want := evaluateWithGuards(witness.snapshot.Clone(), full)
		full[witness.guard] = false
		mutant := evaluateWithGuards(witness.snapshot.Clone(), full)
		if reflect.DeepEqual(want, mutant) {
			t.Fatalf("guard %d deletion survived witness: %#v", witness.guard, mutant)
		}
	}
}

func assertSingleAction(t *testing.T, decision Decision, kind ActionKind) {
	t.Helper()
	if len(decision.Actions) != 1 || decision.Actions[0].Kind != kind {
		t.Fatalf("decision = %#v, want one action %d", decision, kind)
	}
}

func assertParkReason(t *testing.T, decision Decision, reason Reason) {
	t.Helper()
	assertSingleAction(t, decision, ActionParkNeedsYou)
	if decision.Reason != reason {
		t.Fatalf("park reason = %q, want %q", decision.Reason, reason)
	}
}

func containsAction(decision Decision, kind ActionKind) bool {
	return slices.ContainsFunc(decision.Actions, func(action Action) bool { return action.Kind == kind })
}

func containsHostMutation(decision Decision) bool {
	return slices.ContainsFunc(decision.Actions, func(action Action) bool {
		switch action.Kind {
		case ActionCreateWorkspace, ActionCreateWorkerBootstrap, ActionCreateReviewerBootstrap,
			ActionSendWorkerPrompt, ActionSendReviewerPrompt, ActionArchiveWorker,
			ActionArchiveReviewer, ActionArchiveHelper, ActionReplaceWorkerBootstrap:
			return true
		default:
			return false
		}
	})
}

func workerFact(snapshot Snapshot, id string) AgentFact {
	return AgentFact{
		ID: id, Role: RoleWorker, RunID: snapshot.Run.Scope.RunID, WorkspaceID: snapshot.Run.Scope.WorkspaceID,
		ProfileRevision: snapshot.Run.ProfileRevision, State: AgentCreated, Prompt: PromptNone,
	}
}

func reviewerFact(snapshot Snapshot, id string) AgentFact {
	return AgentFact{
		ID: id, Role: RoleReviewer, RunID: snapshot.Run.Scope.RunID, WorkspaceID: snapshot.Run.Scope.WorkspaceID,
		ProfileRevision: snapshot.Run.ProfileRevision, CandidateID: snapshot.Candidate.ID, State: AgentCreated, Prompt: PromptNone,
	}
}

func helperFact(snapshot Snapshot, id, parent string) AgentFact {
	return AgentFact{
		ID: id, Role: RoleHelper, RunID: snapshot.Run.Scope.RunID, WorkspaceID: snapshot.Run.Scope.WorkspaceID,
		ParentID: parent, ProfileRevision: snapshot.Run.ProfileRevision, State: AgentRunning, BootstrapDone: true, Prompt: PromptSent,
	}
}

func activeWorkerSnapshot() Snapshot {
	snapshot := Baseline()
	snapshot.Workspace.State = WorkspaceReady
	worker := workerFact(snapshot, "worker")
	worker.BootstrapDone = true
	worker.State = AgentRunning
	worker.Prompt = PromptSent
	snapshot.Agents = []AgentFact{worker}
	return snapshot
}

func duplicateWorkerSnapshot() Snapshot {
	snapshot := activeWorkerSnapshot()
	second := snapshot.Agents[0]
	second.ID = "worker-2"
	snapshot.Agents = append(snapshot.Agents, second)
	return snapshot
}

func invalidHelperSnapshot() Snapshot {
	snapshot := activeWorkerSnapshot()
	helper := helperFact(snapshot, "helper", "missing-worker")
	snapshot.Agents = append(snapshot.Agents, helper)
	return snapshot
}

func invalidCandidateSnapshot() Snapshot {
	snapshot := activeWorkerSnapshot()
	snapshot.Agents[0].State = AgentIdle
	snapshot.Agents[0].Prompt = PromptFinished
	snapshot.Candidate = CandidateFact{ID: "candidate", RunID: snapshot.Run.Scope.RunID, WorkerID: "missing-worker", State: CandidateCurrent}
	return snapshot
}

func replacementWitness() Snapshot {
	snapshot := Baseline()
	snapshot.Workspace.State = WorkspaceReady
	worker := workerFact(snapshot, "old-worker")
	worker.BootstrapDone = true
	worker.Prompt = PromptErrored
	worker.State = AgentClosed
	worker.Archived = true
	worker.ProcessAbsent = true
	snapshot.Agents = []AgentFact{worker}
	snapshot.Failure.Kind = FailureRecoverableWorker
	return snapshot
}

func mutate(snapshot Snapshot, change func(*Snapshot)) Snapshot {
	result := snapshot.Clone()
	change(&result)
	return result
}
