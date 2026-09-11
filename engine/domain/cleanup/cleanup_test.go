// SPDX-License-Identifier: Apache-2.0

package cleanup

import (
	"encoding/json"
	"strings"
	"testing"
)

func testPolicy(t *testing.T) Policy {
	t.Helper()
	policy, ok := NewPolicy(strings.Repeat("a", 64))
	if !ok {
		t.Fatal("default cleanup policy invalid")
	}
	return policy
}

func testBinding(t *testing.T, policy Policy, integrated, reviewer bool) Binding {
	t.Helper()
	value := Binding{ProjectID: "project-1", WorkspaceID: "workspace-1", TaskID: "dir-m4.10", RunID: "run-1",
		CandidateID: "candidate-1", CandidateSHA: strings.Repeat("b", 40), BaseSHA: strings.Repeat("c", 40),
		TreeSHA: strings.Repeat("d", 40), CandidateGeneration: 1, TaskVersion: 3, ConfigurationSHA256: policy.ConfigurationSHA256,
		RepositoryID: "github:123", RepositoryBindingSHA256: strings.Repeat("e", 64), SourceDevice: 1, SourceInode: 2,
		CommonDevice: 1, CommonInode: 3, WorktreeDevice: 1, WorktreeInode: 4,
		CanonicalRemoteSHA256: strings.Repeat("f", 64), SourcePathSHA256: strings.Repeat("1", 64),
		CommonDirectorySHA256: strings.Repeat("2", 64), WorktreePathSHA256: strings.Repeat("3", 64),
		Branch: "task/dir-m4.10-cleanup", BaseRef: "refs/heads/main", WorktreeID: "worktree-1",
		TaskAgentID: "agent-1", TaskWorkspaceID: "paseo-workspace-1", OwnershipSHA256: strings.Repeat("4", 64),
		CleanupAdmittedAtMillis: 1_000, LeaseEpoch: 7, PolicySHA256: policy.SHA256}
	if reviewer {
		value.ReviewerAgentID, value.ReviewerWorkspaceID = "reviewer-1", "review-workspace-1"
		value.ReviewerCheckoutSHA256 = strings.Repeat("5", 64)
	}
	if integrated {
		value.IntegrationKind, value.IntegrationEvidenceID = "pull_request", "integration-evidence-1"
		value.IntegrationEvidenceSHA256, value.MergeCommitSHA = strings.Repeat("6", 64), strings.Repeat("7", 40)
	}
	value = SealBinding(value)
	if !ValidBinding(value) {
		t.Fatalf("binding invalid: %#v", value)
	}
	return value
}

func TestPolicyDefaultsAndEveryMeasuredCeiling(t *testing.T) {
	policy := testPolicy(t)
	if !policy.TerminateOnCompletion || policy.CancellationMode != CancellationSnapshotThenDelete ||
		!policy.DeleteRemoteTaskBranch || policy.RetentionMillis != MaximumRetentionMillis ||
		policy.RecoveryEntries != 10_000 || policy.InspectedEntries != 25_000 ||
		policy.AggregateBytes != 512*1024*1024 || policy.FileBytes != 256*1024*1024 ||
		policy.StreamBufferBytes != 64*1024 || policy.FreeSpaceFloorBasisPoint != 1_000 {
		t.Fatalf("defaults = %#v", policy)
	}
	mutations := map[string]func(*Policy){
		"retention":         func(value *Policy) { value.RetentionMillis++ },
		"recovery entries":  func(value *Policy) { value.RecoveryEntries++ },
		"inspected entries": func(value *Policy) { value.InspectedEntries++ },
		"aggregate":         func(value *Policy) { value.AggregateBytes++ },
		"file":              func(value *Policy) { value.FileBytes++ },
		"buffer":            func(value *Policy) { value.StreamBufferBytes++ },
		"phase":             func(value *Policy) { value.PhaseMillis++ },
		"lifecycle":         func(value *Policy) { value.LifecycleMillis++ },
		"worker":            func(value *Policy) { value.WorkerMillis++ },
		"rss":               func(value *Policy) { value.WorkerRSSBytes++ },
		"floor":             func(value *Policy) { value.FreeSpaceFloorBasisPoint-- },
		"attempts":          func(value *Policy) { value.AttemptLimit++ },
	}
	for name, mutate := range mutations {
		t.Run(name, func(t *testing.T) {
			changed := policy
			mutate(&changed)
			changed = SealPolicy(changed)
			if ValidPolicy(changed) {
				t.Fatalf("expanded policy admitted: %#v", changed)
			}
		})
	}
}

func TestDeterministicIntegratedAndCancellationEffects(t *testing.T) {
	policy := testPolicy(t)
	integrated, ok := NewState(testBinding(t, policy, true, true), policy, TriggerIntegrated, LifecycleActive)
	if !ok {
		t.Fatal("integrated state")
	}
	want := []ResourceKind{ResourceReviewerAgent, ResourceTaskAgent, ResourceSnapshot, ResourceReviewerWorkspace,
		ResourceTaskWorkspace, ResourceWorktree, ResourceRemoteRef, ResourceLocalRef}
	for index, kind := range want {
		if integrated.Effects[index].Kind != kind {
			t.Fatalf("effect %d = %s", index, integrated.Effects[index].Kind)
		}
	}
	for _, lifecycle := range []LifecycleState{LifecycleActive, LifecycleRestored, LifecycleReclaimed} {
		if _, ok := NewState(testBinding(t, policy, true, false), policy, TriggerIntegrated, lifecycle); !ok {
			t.Fatalf("lifecycle %s rejected", lifecycle)
		}
	}

	retaining := policy
	retaining.CancellationMode = CancellationRetain
	retaining = SealPolicy(retaining)
	cancelled, ok := NewState(testBinding(t, retaining, false, false), retaining, TriggerCancelled, LifecycleRestored)
	if !ok || len(cancelled.Effects) != 1 || cancelled.Effects[0].Kind != ResourceTaskAgent {
		t.Fatalf("retain cancellation = %#v", cancelled)
	}
	if cancelled.CleanupAuthorized {
		t.Fatal("retain cancellation authorized cleanup")
	}

	noTermination := policy
	noTermination.TerminateOnCompletion = false
	noTermination = SealPolicy(noTermination)
	retained, ok := NewState(testBinding(t, noTermination, true, false), noTermination, TriggerIntegrated, LifecycleActive)
	if !ok || retained.Phase != PhaseRetained || len(retained.Effects) != 0 || retained.CleanupAuthorized {
		t.Fatalf("terminateOnCompletion=false = %#v", retained)
	}
}

func TestSnapshotThenDeleteRequiresExactSevenDayEvidence(t *testing.T) {
	policy := testPolicy(t)
	binding := testBinding(t, policy, false, false)
	state, ok := NewState(binding, policy, TriggerCancelled, LifecycleActive)
	if !ok {
		t.Fatal("state")
	}
	// The agent must terminate first.
	agent := &state.Effects[0]
	agent.Observation = func() *Observation {
		value := SealObservation(Observation{EffectID: agent.ID, BindingSHA256: binding.SHA256,
			Kind: agent.Kind, Status: StatusTerminated, Code: CodeOK, Archived: true, ProcessAbsent: true,
			ObservedAtMillis: 2_000, MaximumAgeMillis: MaximumObservationAgeMillis})
		return &value
	}()
	state, ok = CompleteEffect(state, ResourceTaskAgent, nil)
	if !ok {
		t.Fatal("agent completion")
	}
	snapshotEffect := &state.Effects[EffectIndex(state, ResourceSnapshot)]
	snapshot := SealSnapshot(SnapshotEvidence{BindingSHA256: binding.SHA256,
		WorktreeRef:       "refs/director/recovery/dir-m4.10/run-1/0123456789abcdef/worktree",
		WorktreeCommitSHA: strings.Repeat("8", 40), WorktreeTreeSHA: strings.Repeat("9", 40),
		IndexTreeSHA: strings.Repeat("d", 40), CreatedAtMillis: binding.CleanupAdmittedAtMillis,
		RetentionUntilMillis: binding.CleanupAdmittedAtMillis + policy.RetentionMillis})
	if !ValidSnapshot(snapshot, binding, policy) {
		t.Fatalf("snapshot invalid: %#v", snapshot)
	}
	observation := SealObservation(Observation{EffectID: snapshotEffect.ID, BindingSHA256: binding.SHA256,
		Kind: ResourceSnapshot, Status: StatusVerified, Code: CodeOK, Snapshot: &snapshot,
		ObservedAtMillis: 2_000, MaximumAgeMillis: MaximumObservationAgeMillis})
	snapshotEffect.Observation = &observation
	state, ok = CompleteEffect(state, ResourceSnapshot, &snapshot)
	if !ok || !state.CleanupAuthorized || state.Snapshot == nil {
		t.Fatalf("snapshot completion = %#v", state)
	}

	encoded, _ := json.Marshal(state)
	for _, secret := range []string{"/srv/worktree", "private-file-name", "credential="} {
		if strings.Contains(string(encoded), secret) {
			t.Fatalf("public state leaked %q", secret)
		}
	}
	tampered := snapshot
	tampered.RetentionUntilMillis++
	tampered = SealSnapshot(tampered)
	if ValidSnapshot(tampered, binding, policy) {
		t.Fatal("retention beyond seven days admitted")
	}
}

func TestParkAlwaysRevokesCleanupAuthority(t *testing.T) {
	policy := testPolicy(t)
	state, _ := NewState(testBinding(t, policy, true, false), policy, TriggerIntegrated, LifecycleActive)
	state.CleanupAuthorized = true
	// Authorization without a completed snapshot is invalid and cannot be used
	// to manufacture a parked state. Start from the valid state instead.
	state, _ = NewState(testBinding(t, policy, true, false), policy, TriggerIntegrated, LifecycleActive)
	parked, ok := Park(state, CodeWorktreeChanged, "exact_owner_repair")
	if !ok || parked.Phase != PhaseNeedsYou || parked.CleanupAuthorized || !ValidState(parked) {
		t.Fatalf("parked state = %#v", parked)
	}
}
