// SPDX-License-Identifier: Apache-2.0

package dolt_test

import (
	"strings"

	"github.com/mcuadros/director-engine/domain"
	candidatedomain "github.com/mcuadros/director-engine/domain/candidate"
)

func storedCandidate(id, runID string, sequence uint64, commitSHA string) domain.Candidate {
	base := strings.Repeat("0", len(commitSHA))
	if base == commitSHA {
		base = strings.Repeat("1", len(commitSHA))
	}
	claim := candidatedomain.Claim{
		SchemaVersion: candidatedomain.ClaimSchemaVersion, ID: "claim-" + id,
		ProjectID: "project-fixture", WorkspaceID: "workspace-fixture", TaskID: "task-fixture", RunID: runID,
		ActorID: "paseo:fixture", WorktreeID: "worktree-fixture", Branch: "task/fixture", BaseRef: "refs/heads/main",
		CandidateSHA: commitSHA, BaseSHA: base, LeaseEpoch: 1, ExpectedRunVersion: sequence, TaskVersion: 1,
		AcceptanceSHA256: strings.Repeat("a", 64), ConfigurationSHA256: strings.Repeat("b", 64),
		ProfileSHA256: strings.Repeat("c", 64), ContextSHA256: strings.Repeat("d", 64),
		DecisionsSHA256: strings.Repeat("e", 64), FindingsSHA256: strings.Repeat("f", 64),
	}
	observation := candidatedomain.SealObservation(candidatedomain.Observation{
		ClaimSHA256: candidatedomain.ClaimSHA256(claim), RepositoryBindingSHA256: strings.Repeat("9", 64),
		ObservedAtMillis: 1_000, MaximumAgeMillis: candidatedomain.MaximumObservationAgeMS,
		ObjectFormat: map[int]string{40: "sha1", 64: "sha256"}[len(commitSHA)],
		CommitSHA:    commitSHA, BaseSHA: base, ParentSHA: base, TreeSHA: strings.Repeat("2", len(commitSHA)),
		BranchHeadSHA: commitSHA, BaseRefHeadSHA: base,
		DiffSHA256: strings.Repeat("3", 64), ChangedPathsSHA256: strings.Repeat("4", 64),
		SourceDevice: 1, SourceInode: 2, CommonDevice: 1, CommonInode: 3, WorktreeDevice: 1, WorktreeInode: 4,
		RepositoryExact: true, RemoteExact: true, PathsCanonical: true, RegistrationExact: true,
		ObjectPresent: true, ObjectStoreOwned: true, BranchStable: true, BaseStable: true,
		DescendsFromBase: true, DirectParent: true, WorktreeClean: true, IndexClean: true,
		UntrackedAbsent: true, IgnoredAbsent: true, SubmodulesClean: true, ConflictFree: true,
		IntentToAddAbsent: true, SparseCheckoutAbsent: true, FilesystemExact: true,
		SnapshotSHA256: strings.Repeat("5", 64), Code: candidatedomain.CodeOK,
	})
	manifest := candidatedomain.Evaluate(claim, observation, strings.Repeat("9", 64), 1_000).Manifest
	return domain.Candidate{
		SchemaVersion: domain.CandidateSchemaVersion, ID: id, RunID: runID, Sequence: sequence,
		CommitSHA: commitSHA, Claim: claim, Manifest: *manifest, AdmittedAtMillis: 1_000,
	}
}
