// SPDX-License-Identifier: Apache-2.0

package closure

import (
	"strings"
	"testing"

	domaincleanup "github.com/mcuadros/director-engine/domain/cleanup"
	"github.com/mcuadros/director-engine/domain/execution"
)

func closureFacts() Facts {
	return Facts{
		SchemaVersion:             SchemaVersion,
		FakeTerminalRung:          true,
		CandidateCurrent:          true,
		CriteriaClaimsSatisfied:   true,
		ExactOwnership:            true,
		OperationalLimitsAdmitted: true,
		OperationalObservationID:  "periodic-1",
		TaskStoreNowMillis:        1_001,
		AgentArchive: execution.Effect{
			ID: "agent-archive", Kind: execution.EffectAgentArchive,
			Phase: execution.EffectIntentRecorded, AttemptLimit: 2,
		},
	}
}

func TestDeliveryClosureConsumesCurrentCleanupEvidenceWithoutPerformingCleanup(t *testing.T) {
	policy, _ := domaincleanup.NewPolicy(strings.Repeat("a", 64))
	policy.TerminateOnCompletion = false
	policy = domaincleanup.SealPolicy(policy)
	binding := domaincleanup.SealBinding(domaincleanup.Binding{ProjectID: "project-1", WorkspaceID: "workspace-1",
		TaskID: "dir-m4.10", RunID: "run-1", CandidateID: "candidate-1", CandidateSHA: strings.Repeat("b", 40),
		BaseSHA: strings.Repeat("c", 40), TreeSHA: strings.Repeat("d", 40), CandidateGeneration: 1, TaskVersion: 1,
		ConfigurationSHA256: policy.ConfigurationSHA256, RepositoryID: "github:123", RepositoryBindingSHA256: strings.Repeat("e", 64),
		SourceDevice: 1, SourceInode: 2, CommonDevice: 1, CommonInode: 3, WorktreeDevice: 1, WorktreeInode: 4,
		CanonicalRemoteSHA256: strings.Repeat("f", 64), SourcePathSHA256: strings.Repeat("1", 64), CommonDirectorySHA256: strings.Repeat("2", 64),
		WorktreePathSHA256: strings.Repeat("3", 64), Branch: "task/dir-m4.10-cleanup", BaseRef: "refs/heads/main", WorktreeID: "worktree-1",
		TaskAgentID: "agent-1", TaskWorkspaceID: "workspace-native-1", OwnershipSHA256: strings.Repeat("4", 64), CleanupAdmittedAtMillis: 1_000,
		IntegrationKind: "pull_request", IntegrationEvidenceID: "integration-1", IntegrationEvidenceSHA256: strings.Repeat("5", 64),
		MergeCommitSHA: strings.Repeat("6", 40), LeaseEpoch: 1, PolicySHA256: policy.SHA256})
	cleanup, ok := domaincleanup.NewState(binding, policy, domaincleanup.TriggerIntegrated, domaincleanup.LifecycleActive)
	if !ok || cleanup.Phase != domaincleanup.PhaseRetained {
		t.Fatal("retained cleanup state")
	}
	facts := closureFacts()
	facts.FakeTerminalRung = false
	facts.DeliveryTerminalRung = true
	facts.CleanupBindingCurrent = true
	facts.Cleanup = &cleanup
	if decision := Reduce(facts); decision.Kind != DecisionTerminal || decision.CleanupAuthorized {
		t.Fatalf("delivery closure = %#v", decision)
	}
	facts.CleanupBindingCurrent = false
	if decision := Reduce(facts); decision.Kind != DecisionEscalate || decision.CleanupAuthorized {
		t.Fatalf("stale cleanup = %#v", decision)
	}
}

func TestReduceOrdersTerminalAgentViewAndDirectorWorktreeCleanup(t *testing.T) {
	facts := closureFacts()
	if decision := Reduce(facts); decision.Kind != DecisionObserve || decision.EffectKind != execution.EffectAgentArchive {
		t.Fatalf("agent cleanup = %#v", decision)
	}
	facts.AgentArchive.Phase = execution.EffectComplete
	facts.AgentArchive.ExternalID = "agent-1"
	facts.HostViewArchive = execution.Effect{
		ID: "host-view-archive", Kind: execution.EffectHostViewArchive,
		Phase: execution.EffectIntentRecorded, AttemptLimit: 2,
	}
	if decision := Reduce(facts); decision.EffectKind != execution.EffectHostViewArchive {
		t.Fatalf("view cleanup = %#v", decision)
	}
	facts.HostViewArchive.Phase = execution.EffectComplete
	facts.HostViewArchive.ExternalID = "workspace-1"
	facts.WorktreeRemove = execution.Effect{
		ID: "worktree-remove", Kind: execution.EffectWorktreeRemove,
		Phase: execution.EffectIntentRecorded, AttemptLimit: 1,
	}
	if decision := Reduce(facts); decision.EffectKind != execution.EffectWorktreeRemove {
		t.Fatalf("worktree cleanup = %#v", decision)
	}
	facts.WorktreeRemove.Phase = execution.EffectComplete
	facts.WorktreeRemove.ExternalID = "worktree-1"
	if decision := Reduce(facts); decision.Kind != DecisionTerminal {
		t.Fatalf("terminal decision = %#v", decision)
	}
}

func TestReduceNeverTreatsNeedsYouAsCleanupAuthority(t *testing.T) {
	for name, mutate := range map[string]func(*Facts){
		"limits": func(facts *Facts) {
			facts.OperationalLimitsAdmitted = false
			facts.OperationalNeedCode = execution.NeedProcessLimit
		},
		"ownership": func(facts *Facts) { facts.ExactOwnership = false },
		"candidate": func(facts *Facts) { facts.CandidateCurrent = false },
	} {
		t.Run(name, func(t *testing.T) {
			facts := closureFacts()
			mutate(&facts)
			decision := Reduce(facts)
			if decision.Kind != DecisionEscalate || decision.CleanupAuthorized {
				t.Fatalf("cleanup refusal = %#v", decision)
			}
		})
	}
}
