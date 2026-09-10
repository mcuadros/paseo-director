// SPDX-License-Identifier: Apache-2.0

package routing

import (
	"strings"
	"testing"

	candidatedomain "github.com/mcuadros/director-engine/domain/candidate"
	"github.com/mcuadros/director-engine/domain/execution"
)

func routingFacts() Facts {
	facts := Facts{
		SchemaVersion:             SchemaVersion,
		AgentID:                   "agent-1",
		AgentTurnEnded:            true,
		RepositoryBindingHash:     strings.Repeat("9", 64),
		OperationalLimitsAdmitted: true,
		OperationalObservationID:  "periodic-1",
		TaskStoreNowMillis:        1_001,
		Claim: &execution.CompletedClaim{
			ID: "claim-1", SchemaVersion: "director.agent-outcome.completed/v1",
			Outcome: "completed", AgentID: "agent-1",
			CandidateSHA:    "1111111111111111111111111111111111111111",
			BaseSHA:         "0000000000000000000000000000000000000000",
			CriteriaResults: map[string]string{"criterion-1": "claimed_satisfied"},
		},
	}
	facts.CandidateClaim = &candidatedomain.Claim{
		SchemaVersion: candidatedomain.ClaimSchemaVersion, ID: facts.Claim.ID,
		ProjectID: "project-1", WorkspaceID: "workspace-1", TaskID: "task-1", RunID: "run-1",
		ActorID: facts.AgentID, WorktreeID: "worktree-1", Branch: "task/task-1", BaseRef: "refs/heads/main",
		CandidateSHA: facts.Claim.CandidateSHA, BaseSHA: facts.Claim.BaseSHA,
		LeaseEpoch: 1, ExpectedRunVersion: 7, TaskVersion: 2,
		AcceptanceSHA256: strings.Repeat("a", 64), ConfigurationSHA256: strings.Repeat("b", 64),
		ProfileSHA256: strings.Repeat("c", 64), ContextSHA256: strings.Repeat("d", 64),
		DecisionsSHA256: strings.Repeat("e", 64), FindingsSHA256: strings.Repeat("f", 64),
	}
	return facts
}

func exactObservation(facts Facts) execution.CandidateObservation {
	observation := execution.CandidateObservation{
		ClaimSHA256: candidatedomain.ClaimSHA256(*facts.CandidateClaim), RepositoryBindingSHA256: facts.RepositoryBindingHash,
		ObservedAtMillis: 1_000, MaximumAgeMillis: candidatedomain.MaximumObservationAgeMS,
		ObjectFormat: "sha1", CommitSHA: facts.Claim.CandidateSHA, BaseSHA: facts.Claim.BaseSHA,
		ParentSHA: facts.Claim.BaseSHA, TreeSHA: "2222222222222222222222222222222222222222",
		BranchHeadSHA: facts.Claim.CandidateSHA, BaseRefHeadSHA: facts.Claim.BaseSHA,
		DiffSHA256: strings.Repeat("1", 64), ChangedPathsSHA256: strings.Repeat("2", 64),
		SourceDevice: 1, SourceInode: 2, CommonDevice: 1, CommonInode: 3, WorktreeDevice: 1, WorktreeInode: 4,
		RepositoryExact: true, RemoteExact: true, PathsCanonical: true, RegistrationExact: true,
		ObjectPresent: true, ObjectStoreOwned: true, BranchStable: true, BaseStable: true,
		DescendsFromBase: true, DirectParent: true, WorktreeClean: true, IndexClean: true,
		UntrackedAbsent: true, IgnoredAbsent: true, SubmodulesClean: true, ConflictFree: true,
		IntentToAddAbsent: true, SparseCheckoutAbsent: true, FilesystemExact: true,
		SnapshotSHA256: strings.Repeat("3", 64), Code: candidatedomain.CodeOK,
	}
	return candidatedomain.SealObservation(observation)
}

func TestReduceTreatsCompletedClaimAsInputNotCandidateEvidence(t *testing.T) {
	facts := routingFacts()
	decision := Reduce(facts)
	if decision.Kind != DecisionObserveCandidate {
		t.Fatalf("claim without Git observation = %#v", decision)
	}

	observation := exactObservation(facts)
	facts.CandidateObservation = &observation
	decision = Reduce(facts)
	if decision.Kind != DecisionAdmitCandidate || decision.CandidateSHA != facts.Claim.CandidateSHA {
		t.Fatalf("reconciled Candidate = %#v", decision)
	}

	facts.CandidateObservation.WorktreeClean = false
	updated := candidatedomain.SealObservation(*facts.CandidateObservation)
	facts.CandidateObservation = &updated
	decision = Reduce(facts)
	if decision.Kind != DecisionEscalate || decision.CleanupAuthorized {
		t.Fatalf("dirty claimed Candidate = %#v", decision)
	}
}

func TestReduceParksOnPeriodicOperationalFailureWithoutCleanupAuthority(t *testing.T) {
	facts := routingFacts()
	facts.OperationalLimitsAdmitted = false
	facts.OperationalNeedCode = execution.NeedMemoryLimit
	decision := Reduce(facts)
	if decision.Kind != DecisionEscalate || decision.Code != string(execution.NeedMemoryLimit) || decision.CleanupAuthorized {
		t.Fatalf("periodic resource decision = %#v", decision)
	}

	facts = routingFacts()
	facts.OperationalObservationID = ""
	if decision := Reduce(facts); decision.Kind != DecisionObserveOperationalLimits {
		t.Fatalf("missing periodic observation = %#v", decision)
	}
}
