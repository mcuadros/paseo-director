// SPDX-License-Identifier: Apache-2.0

package cleanup

import (
	"strings"
	"sync"
	"testing"

	domaincleanup "github.com/mcuadros/director-engine/domain/cleanup"
)

func policy(t *testing.T) domaincleanup.Policy {
	t.Helper()
	value, ok := domaincleanup.NewPolicy(strings.Repeat("a", 64))
	if !ok {
		t.Fatal("policy")
	}
	return value
}

func binding(t *testing.T, policy domaincleanup.Policy, integrated bool) domaincleanup.Binding {
	t.Helper()
	value := domaincleanup.Binding{ProjectID: "project-1", WorkspaceID: "workspace-1", TaskID: "dir-m4.10", RunID: "run-1",
		CandidateID: "candidate-1", CandidateSHA: strings.Repeat("b", 40), BaseSHA: strings.Repeat("c", 40), TreeSHA: strings.Repeat("d", 40),
		CandidateGeneration: 1, TaskVersion: 1, ConfigurationSHA256: policy.ConfigurationSHA256,
		RepositoryID: "github:123", RepositoryBindingSHA256: strings.Repeat("e", 64), SourceDevice: 1, SourceInode: 2,
		CommonDevice: 1, CommonInode: 3, WorktreeDevice: 1, WorktreeInode: 4,
		CanonicalRemoteSHA256: strings.Repeat("f", 64), SourcePathSHA256: strings.Repeat("1", 64),
		CommonDirectorySHA256: strings.Repeat("2", 64), WorktreePathSHA256: strings.Repeat("3", 64), Branch: "task/dir-m4.10-cleanup",
		BaseRef: "refs/heads/main", WorktreeID: "worktree-1", TaskAgentID: "agent-1", TaskWorkspaceID: "workspace-native-1",
		OwnershipSHA256: strings.Repeat("4", 64), CleanupAdmittedAtMillis: 1_000, LeaseEpoch: 1, PolicySHA256: policy.SHA256}
	if integrated {
		value.IntegrationKind, value.IntegrationEvidenceID = "pull_request", "integration-evidence-1"
		value.IntegrationEvidenceSHA256, value.MergeCommitSHA = strings.Repeat("5", 64), strings.Repeat("6", 40)
	}
	return domaincleanup.SealBinding(value)
}

func facts(state domaincleanup.State) Facts {
	return Facts{SchemaVersion: SchemaVersion, State: state, ProjectActive: true, LeaseCurrent: true,
		CandidateCurrent: true, IntegrationVerified: state.Trigger != domaincleanup.TriggerIntegrated || state.Binding.IntegrationKind != "",
		NowMillis: 2_000}
}

func observed(state domaincleanup.State, kind domaincleanup.ResourceKind, status domaincleanup.Status) domaincleanup.State {
	index := domaincleanup.EffectIndex(state, kind)
	effect := state.Effects[index]
	observation := domaincleanup.SealObservation(domaincleanup.Observation{EffectID: effect.ID, BindingSHA256: state.Binding.SHA256,
		Kind: kind, Attempt: effect.Attempt, Status: status, Code: domaincleanup.CodeOK,
		Archived: status == domaincleanup.StatusTerminated, ProcessAbsent: status == domaincleanup.StatusTerminated,
		ObservedAtMillis: 2_000, MaximumAgeMillis: domaincleanup.MaximumObservationAgeMillis})
	next, ok := domaincleanup.RecordObservation(state, kind, observation, 2_000)
	if !ok {
		panic("invalid test observation")
	}
	return next
}

func completeEffect(state domaincleanup.State, kind domaincleanup.ResourceKind) domaincleanup.State {
	status := domaincleanup.StatusAbsent
	if kind == domaincleanup.ResourceTaskAgent || kind == domaincleanup.ResourceReviewerAgent {
		status = domaincleanup.StatusTerminated
	}
	state = observed(state, kind, status)
	next, ok := domaincleanup.CompleteEffect(state, kind, nil)
	if !ok {
		panic("complete test effect")
	}
	return next
}

func TestReducerRequiresDeterministicOrderAndExactIntegration(t *testing.T) {
	policy := policy(t)
	state, _ := domaincleanup.NewState(binding(t, policy, true), policy, domaincleanup.TriggerIntegrated, domaincleanup.LifecycleActive)
	decision := Reduce(facts(state))
	if decision.Kind != DecisionObserve || decision.EffectKind != domaincleanup.ResourceTaskAgent || decision.CleanupAuthorized {
		t.Fatalf("first decision = %#v", decision)
	}
	missing := facts(state)
	missing.IntegrationVerified = false
	if decision := Reduce(missing); decision.Kind != DecisionEscalate || decision.Code != domaincleanup.CodeIntegrationUnverified || decision.CleanupAuthorized {
		t.Fatalf("missing integration = %#v", decision)
	}
	stale := facts(state)
	stale.CandidateCurrent = false
	if decision := Reduce(stale); decision.Code != domaincleanup.CodeBindingChanged {
		t.Fatalf("stale Candidate = %#v", decision)
	}
}

func TestIntegratedCleanWorkAuthorizesRemovalOnlyAfterSnapshotGate(t *testing.T) {
	policy := policy(t)
	state, _ := domaincleanup.NewState(binding(t, policy, true), policy, domaincleanup.TriggerIntegrated, domaincleanup.LifecycleActive)
	state = completeEffect(state, domaincleanup.ResourceTaskAgent)
	state = observed(state, domaincleanup.ResourceSnapshot, domaincleanup.StatusClean)
	decision := Reduce(facts(state))
	if decision.Kind != DecisionAdopt || decision.EffectKind != domaincleanup.ResourceSnapshot || !decision.CleanupAuthorized {
		t.Fatalf("clean snapshot decision = %#v", decision)
	}
	next, ok := domaincleanup.CompleteEffect(state, domaincleanup.ResourceSnapshot, nil)
	if !ok || !next.CleanupAuthorized {
		t.Fatalf("snapshot gate = %#v", next)
	}
	if decision := Reduce(facts(next)); decision.Kind != DecisionObserve || decision.EffectKind != domaincleanup.ResourceTaskWorkspace || !decision.CleanupAuthorized {
		t.Fatalf("post-snapshot decision = %#v", decision)
	}
}

func TestCancellationAndDiskPressureNeverDeleteUnintegratedWork(t *testing.T) {
	policy := policy(t)
	state, _ := domaincleanup.NewState(binding(t, policy, false), policy, domaincleanup.TriggerCancelled, domaincleanup.LifecycleActive)
	pressured := facts(state)
	pressured.DiskPressure = true
	if decision := Reduce(pressured); decision.Kind != DecisionEscalate || decision.Code != domaincleanup.CodeDiskPressure || decision.CleanupAuthorized {
		t.Fatalf("disk pressure = %#v", decision)
	}
	state = completeEffect(state, domaincleanup.ResourceTaskAgent)
	state = observed(state, domaincleanup.ResourceSnapshot, domaincleanup.StatusDirty)
	if decision := Reduce(facts(state)); decision.Kind != DecisionDispatch || decision.EffectKind != domaincleanup.ResourceSnapshot || decision.CleanupAuthorized {
		t.Fatalf("dirty cancellation = %#v", decision)
	}

	retaining := policy
	retaining.CancellationMode = domaincleanup.CancellationRetain
	retaining = domaincleanup.SealPolicy(retaining)
	retained, _ := domaincleanup.NewState(binding(t, retaining, false), retaining, domaincleanup.TriggerFailed, domaincleanup.LifecycleRestored)
	retained = completeEffect(retained, domaincleanup.ResourceTaskAgent)
	if decision := Reduce(facts(retained)); decision.Kind != DecisionRetain || decision.CleanupAuthorized {
		t.Fatalf("retain decision = %#v", decision)
	}
}

func TestDestructiveResponseLossPreservesSameSHATarget(t *testing.T) {
	policy := policy(t)
	state, _ := domaincleanup.NewState(binding(t, policy, true), policy, domaincleanup.TriggerIntegrated, domaincleanup.LifecycleActive)
	// Advance prior effects with exact absence/clean observations.
	state = completeEffect(state, domaincleanup.ResourceTaskAgent)
	state = observed(state, domaincleanup.ResourceSnapshot, domaincleanup.StatusClean)
	state, _ = domaincleanup.CompleteEffect(state, domaincleanup.ResourceSnapshot, nil)
	state = observed(state, domaincleanup.ResourceTaskWorkspace, domaincleanup.StatusAbsent)
	state, _ = domaincleanup.CompleteEffect(state, domaincleanup.ResourceTaskWorkspace, nil)
	state = observed(state, domaincleanup.ResourceWorktree, domaincleanup.StatusAbsent)
	state, _ = domaincleanup.CompleteEffect(state, domaincleanup.ResourceWorktree, nil)
	state = observed(state, domaincleanup.ResourceRemoteRef, domaincleanup.StatusExactPresent)
	dispatching, ok := domaincleanup.BeginDispatch(state, domaincleanup.ResourceRemoteRef)
	if !ok {
		t.Fatal("dispatch")
	}
	dispatching, ok = domaincleanup.RequireObservation(dispatching, domaincleanup.ResourceRemoteRef)
	if !ok {
		t.Fatal("observation required")
	}
	dispatching = observed(dispatching, domaincleanup.ResourceRemoteRef, domaincleanup.StatusExactPresent)
	decision := Reduce(facts(dispatching))
	if decision.Kind != DecisionEscalate || decision.Code != domaincleanup.CodeResponseUnknown || decision.CleanupAuthorized {
		t.Fatalf("same-SHA recreation = %#v", decision)
	}
}

func TestThirtyTwoReducersHaveOneIdenticalDecision(t *testing.T) {
	policy := policy(t)
	state, _ := domaincleanup.NewState(binding(t, policy, true), policy, domaincleanup.TriggerIntegrated, domaincleanup.LifecycleReclaimed)
	const coordinators = 32
	results := make(chan Decision, coordinators)
	var wait sync.WaitGroup
	for range coordinators {
		wait.Add(1)
		go func() { defer wait.Done(); results <- Reduce(facts(state)) }()
	}
	wait.Wait()
	close(results)
	for decision := range results {
		if decision.Kind != DecisionObserve || decision.EffectKind != domaincleanup.ResourceTaskAgent || decision.CleanupAuthorized {
			t.Fatalf("decision = %#v", decision)
		}
	}
}
