// SPDX-License-Identifier: Apache-2.0

package execution

import (
	"strings"
	"testing"

	candidatedomain "github.com/mcuadros/director-engine/domain/candidate"
)

func TestEffectAndCandidateEvidenceRequireCurrentSelfConsistentFacts(t *testing.T) {
	effect := EffectObservation{
		ID: "observation-1", EffectID: "effect-1", Status: ObservationAbsent,
		BindingHash: "binding-1", PriorDispatcherAbsent: true,
		ObservedAtMillis: 1_000, MaximumAgeMillis: 30_000,
	}
	effect.FactHash = EffectObservationHash(effect)
	if !CurrentEffectObservation(effect, 31_000) {
		t.Fatal("effect observation rejected its exact freshness boundary")
	}
	if CurrentEffectObservation(effect, 31_001) {
		t.Fatal("stale effect observation remained current")
	}
	tamperedEffect := effect
	tamperedEffect.BindingHash = "other"
	if CurrentEffectObservation(tamperedEffect, 1_001) {
		t.Fatal("tampered effect observation remained current")
	}

	candidate := CandidateObservation{
		ClaimSHA256: strings.Repeat("a", 64), RepositoryBindingSHA256: strings.Repeat("b", 64),
		ObservedAtMillis: 1_000, MaximumAgeMillis: candidatedomain.MaximumObservationAgeMS,
		ObjectFormat: "sha1", CommitSHA: strings.Repeat("1", 40), BaseSHA: strings.Repeat("0", 40),
		ParentSHA: strings.Repeat("0", 40), TreeSHA: strings.Repeat("2", 40),
		BranchHeadSHA: strings.Repeat("1", 40), BaseRefHeadSHA: strings.Repeat("0", 40),
		DiffSHA256: strings.Repeat("3", 64), ChangedPathsSHA256: strings.Repeat("4", 64),
		SourceDevice: 1, SourceInode: 2, CommonDevice: 1, CommonInode: 3, WorktreeDevice: 1, WorktreeInode: 4,
		RepositoryExact: true, RemoteExact: true, PathsCanonical: true, RegistrationExact: true,
		ObjectPresent: true, ObjectStoreOwned: true, BranchStable: true, BaseStable: true,
		DescendsFromBase: true, DirectParent: true, WorktreeClean: true, IndexClean: true,
		UntrackedAbsent: true, IgnoredAbsent: true, SubmodulesClean: true, ConflictFree: true,
		IntentToAddAbsent: true, SparseCheckoutAbsent: true, FilesystemExact: true,
		SnapshotSHA256: strings.Repeat("5", 64), Code: candidatedomain.CodeOK,
	}
	candidate = candidatedomain.SealObservation(candidate)
	if !CurrentCandidateObservation(candidate, 6_000) {
		t.Fatal("Candidate observation rejected its exact freshness boundary")
	}
	if CurrentCandidateObservation(candidate, 6_001) || CurrentCandidateObservation(candidate, 999) {
		t.Fatal("future Candidate observation remained current")
	}
}

func TestStartupReconciliationBindsCommandCandidateExecutionAndCleanupFacts(t *testing.T) {
	reconciliation := StartupReconciliation{
		SchemaVersion: StartupReconciliationSchemaVersion,
		ID:            "startup-1", ObservedRunVersion: 9, ObservedAtMillis: 1_000,
		CommandCount: 10, CommandChainHash: "command-chain", LastCommandID: "command-10",
		OperationalObservationID: "operational-1",
		EffectObservationCount:   1, EffectObservationChainHash: "effect-chain",
		WorktreeID: "worktree-1", WorkspaceID: "workspace-1", AgentID: "agent-1",
		CandidateID: "candidate-1", CandidateSHA: "1111111111111111111111111111111111111111",
		CleanupIntents: []CleanupIntentFact{{
			EffectID: "cleanup-1", Kind: EffectAgentArchive,
			Phase: EffectDispatching, Attempt: 1,
		}},
		FrontierEffectID: "cleanup-1", FrontierEffectKind: EffectAgentArchive,
		FrontierObservationID: "effect-observation-1", FrontierObservationHash: "effect-hash",
		CandidateObservationID: "candidate-observation-1", CandidateObservationHash: "candidate-hash",
		HostCursor: 17,
	}
	reconciliation.FactHash = StartupReconciliationHash(reconciliation)
	if !ValidStartupReconciliation(reconciliation) {
		t.Fatal("exact startup reconciliation was rejected")
	}
	tampered := reconciliation
	tampered.AgentID = "agent-2"
	if ValidStartupReconciliation(tampered) {
		t.Fatal("tampered startup execution identity remained valid")
	}
	tampered = reconciliation
	tampered.CleanupIntents[0].Attempt++
	if ValidStartupReconciliation(tampered) {
		t.Fatal("tampered startup cleanup intent remained valid")
	}
}
