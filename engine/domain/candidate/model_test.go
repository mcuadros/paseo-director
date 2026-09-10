// SPDX-License-Identifier: Apache-2.0

package candidate

import (
	"strings"
	"testing"
)

func testClaim() Claim {
	return Claim{
		SchemaVersion: ClaimSchemaVersion, ID: "claim-1",
		ProjectID: "project-1", WorkspaceID: "workspace-1", TaskID: "task-1", RunID: "run-1",
		ActorID: "paseo:agent-1", WorktreeID: "worktree-1", Branch: "task/task-1", BaseRef: "refs/heads/main",
		CandidateSHA: strings.Repeat("1", 40), BaseSHA: strings.Repeat("0", 40),
		LeaseEpoch: 2, ExpectedRunVersion: 7, TaskVersion: 3,
		AcceptanceSHA256: strings.Repeat("a", 64), ConfigurationSHA256: strings.Repeat("b", 64),
		ProfileSHA256: strings.Repeat("c", 64), ContextSHA256: strings.Repeat("d", 64),
		DecisionsSHA256: strings.Repeat("e", 64), FindingsSHA256: strings.Repeat("f", 64),
	}
}

func testObservation(claim Claim) Observation {
	return SealObservation(Observation{
		ClaimSHA256: ClaimSHA256(claim), RepositoryBindingSHA256: strings.Repeat("9", 64),
		ObservedAtMillis: 1_000, MaximumAgeMillis: MaximumObservationAgeMS,
		ObjectFormat: "sha1", CommitSHA: claim.CandidateSHA, BaseSHA: claim.BaseSHA,
		ParentSHA: claim.BaseSHA, TreeSHA: strings.Repeat("2", 40),
		BranchHeadSHA: claim.CandidateSHA, BaseRefHeadSHA: claim.BaseSHA,
		DiffSHA256: strings.Repeat("3", 64), ChangedPathsSHA256: strings.Repeat("4", 64),
		SourceDevice: 1, SourceInode: 2, CommonDevice: 1, CommonInode: 3, WorktreeDevice: 1, WorktreeInode: 4,
		RepositoryExact: true, RemoteExact: true, PathsCanonical: true, RegistrationExact: true,
		ObjectPresent: true, ObjectStoreOwned: true, BranchStable: true, BaseStable: true,
		DescendsFromBase: true, DirectParent: true, WorktreeClean: true, IndexClean: true,
		UntrackedAbsent: true, IgnoredAbsent: true, SubmodulesClean: true, ConflictFree: true,
		IntentToAddAbsent: true, SparseCheckoutAbsent: true, FilesystemExact: true,
		SnapshotSHA256: strings.Repeat("5", 64), Code: CodeOK,
	})
}

func TestFakeGitPropertyMatrixRequiresEveryExactPredicate(t *testing.T) {
	claim := testClaim()
	binding := strings.Repeat("9", 64)
	base := testObservation(claim)
	if decision := Evaluate(claim, base, binding, 1_001); decision.Kind != DecisionAdmit || decision.Manifest == nil || !ValidManifest(*decision.Manifest) {
		t.Fatalf("valid Candidate refused: %#v", decision)
	}
	mutations := []struct {
		name  string
		apply func(*Observation)
	}{
		{"repository", func(v *Observation) { v.RepositoryExact = false }},
		{"remote", func(v *Observation) { v.RemoteExact = false }},
		{"canonical paths", func(v *Observation) { v.PathsCanonical = false }},
		{"registration", func(v *Observation) { v.RegistrationExact = false }},
		{"object", func(v *Observation) { v.ObjectPresent = false }},
		{"object ownership", func(v *Observation) { v.ObjectStoreOwned = false }},
		{"branch", func(v *Observation) { v.BranchStable = false }},
		{"base", func(v *Observation) { v.BaseStable = false }},
		{"ancestry", func(v *Observation) { v.DescendsFromBase = false }},
		{"worktree", func(v *Observation) { v.WorktreeClean = false }},
		{"index", func(v *Observation) { v.IndexClean = false }},
		{"untracked", func(v *Observation) { v.UntrackedAbsent = false }},
		{"ignored", func(v *Observation) { v.IgnoredAbsent = false }},
		{"submodule", func(v *Observation) { v.SubmodulesClean = false }},
		{"conflict", func(v *Observation) { v.ConflictFree = false }},
		{"intent to add", func(v *Observation) { v.IntentToAddAbsent = false }},
		{"sparse", func(v *Observation) { v.SparseCheckoutAbsent = false }},
		{"filesystem", func(v *Observation) { v.FilesystemExact = false }},
	}
	for _, mutation := range mutations {
		t.Run(mutation.name, func(t *testing.T) {
			changed := base
			mutation.apply(&changed)
			changed = SealObservation(changed)
			if decision := Evaluate(claim, changed, binding, 1_001); decision.Kind != DecisionPark || decision.Manifest != nil {
				t.Fatalf("mutated fact admitted: %#v", decision)
			}
		})
	}
	if decision := Evaluate(claim, base, binding, 1_000+MaximumObservationAgeMS+1); decision.Code != CodeObservationStale {
		t.Fatalf("stale decision = %#v", decision)
	}
}

func TestFakeGitMutationMatrixBindsEveryImmutableManifestInput(t *testing.T) {
	baseClaim := testClaim()
	binding := strings.Repeat("9", 64)
	manifest := Evaluate(baseClaim, testObservation(baseClaim), binding, 1_001).Manifest
	if manifest == nil {
		t.Fatal("fixture manifest missing")
	}
	mutations := []struct {
		name  string
		apply func(*Claim, *Observation)
	}{
		{"tree", func(_ *Claim, v *Observation) { v.TreeSHA = strings.Repeat("6", 40) }},
		{"diff", func(_ *Claim, v *Observation) { v.DiffSHA256 = strings.Repeat("6", 64) }},
		{"changed paths", func(_ *Claim, v *Observation) { v.ChangedPathsSHA256 = strings.Repeat("7", 64) }},
		{"acceptance", func(c *Claim, _ *Observation) { c.AcceptanceSHA256 = strings.Repeat("6", 64) }},
		{"configuration", func(c *Claim, _ *Observation) { c.ConfigurationSHA256 = strings.Repeat("6", 64) }},
		{"profile", func(c *Claim, _ *Observation) { c.ProfileSHA256 = strings.Repeat("6", 64) }},
		{"context", func(c *Claim, _ *Observation) { c.ContextSHA256 = strings.Repeat("6", 64) }},
		{"decisions", func(c *Claim, _ *Observation) { c.DecisionsSHA256 = strings.Repeat("6", 64) }},
		{"findings", func(c *Claim, _ *Observation) { c.FindingsSHA256 = strings.Repeat("6", 64) }},
	}
	for _, mutation := range mutations {
		t.Run(mutation.name, func(t *testing.T) {
			claim := baseClaim
			observation := testObservation(claim)
			mutation.apply(&claim, &observation)
			observation.ClaimSHA256 = ClaimSHA256(claim)
			observation = SealObservation(observation)
			decision := Evaluate(claim, observation, binding, 1_001)
			if decision.Manifest == nil || decision.Manifest.BindingSHA256 == manifest.BindingSHA256 {
				t.Fatalf("%s did not change immutable binding", mutation.name)
			}
		})
	}
}

func TestAuthorityInvalidatesEveryDownstreamGateOnEveryRelevantChange(t *testing.T) {
	claim := testClaim()
	manifest := *Evaluate(claim, testObservation(claim), strings.Repeat("9", 64), 1_001).Manifest
	authority := NewAuthority(3, RecordID(claim.RunID, 1, claim.CandidateSHA), claim.Branch, claim.TaskVersion, manifest)
	evidence := func(id string) *EvidenceBinding {
		return &EvidenceBinding{ID: id, CandidateID: authority.CandidateID, CandidateSHA: authority.CandidateSHA,
			BaseSHA: authority.BaseSHA, Generation: authority.Generation, BindingSHA256: authority.BindingSHA256}
	}
	authority.Downstream = Downstream{
		Validation: evidence("validation-1"), Review: evidence("review-1"), CI: evidence("ci-1"),
		Publication: evidence("publication-1"), Feedback: evidence("feedback-1"), Ready: evidence("ready-1"),
		Integration: evidence("integration-1"),
	}
	base := AuthorityContext{
		CandidateID: authority.CandidateID, BindingSHA256: authority.BindingSHA256, CandidateSHA: authority.CandidateSHA,
		BaseSHA: authority.BaseSHA, Branch: authority.Branch, TaskVersion: authority.TaskVersion,
		ConfigurationSHA256: authority.ConfigurationSHA256, DecisionsSHA256: authority.DecisionsSHA256,
		FindingsSHA256: authority.FindingsSHA256,
	}
	mutations := []struct {
		name  string
		apply func(*AuthorityContext)
	}{
		{"Candidate", func(v *AuthorityContext) { v.CandidateSHA = strings.Repeat("8", 40) }},
		{"base", func(v *AuthorityContext) { v.BaseSHA = strings.Repeat("8", 40) }},
		{"branch", func(v *AuthorityContext) { v.Branch = "task/changed" }},
		{"Task version", func(v *AuthorityContext) { v.TaskVersion++ }},
		{"configuration", func(v *AuthorityContext) { v.ConfigurationSHA256 = strings.Repeat("8", 64) }},
		{"decisions", func(v *AuthorityContext) { v.DecisionsSHA256 = strings.Repeat("8", 64) }},
		{"findings", func(v *AuthorityContext) { v.FindingsSHA256 = strings.Repeat("8", 64) }},
		{"tree/diff/path binding", func(v *AuthorityContext) { v.BindingSHA256 = strings.Repeat("8", 64) }},
	}
	for _, mutation := range mutations {
		t.Run(mutation.name, func(t *testing.T) {
			current := base
			mutation.apply(&current)
			invalidated, changed := ReconcileAuthority(authority, current)
			if !changed || !invalidated.Invalidated || !DownstreamEmpty(invalidated.Downstream) || invalidated.Generation != authority.Generation+1 {
				t.Fatalf("authority not fully invalidated: %#v", invalidated)
			}
		})
	}
	if unchanged, changed := ReconcileAuthority(authority, base); changed || unchanged != authority {
		t.Fatal("unchanged exact authority was invalidated")
	}
}

func TestFullObjectIDsRejectPrefixesAndMixedFormats(t *testing.T) {
	claim := testClaim()
	for _, candidate := range []string{claim.CandidateSHA[:39], strings.Repeat("A", 40), strings.Repeat("1", 64)} {
		changed := claim
		changed.CandidateSHA = candidate
		if ValidClaim(changed) {
			t.Fatalf("non-exact or mixed Candidate accepted: %q", candidate)
		}
	}
	claim.CandidateSHA = strings.Repeat("1", 64)
	claim.BaseSHA = strings.Repeat("0", 64)
	if !ValidClaim(claim) {
		t.Fatal("exact SHA-256 claim was rejected")
	}
}

func TestMergeAndNonDirectCandidatesRequireDistinctExplicitAllowances(t *testing.T) {
	claim := testClaim()
	binding := strings.Repeat("9", 64)
	merge := testObservation(claim)
	merge.MergeCommit = true
	merge.DirectParent = false
	merge = SealObservation(merge)
	if decision := Evaluate(claim, merge, binding, 1_001); decision.Code != CodeGraphRejected {
		t.Fatalf("default merge decision = %#v", decision)
	}
	claim.GraphPolicy.AllowMergeCommit = true
	merge.ClaimSHA256 = ClaimSHA256(claim)
	merge = SealObservation(merge)
	if decision := Evaluate(claim, merge, binding, 1_001); decision.Kind != DecisionAdmit {
		t.Fatalf("explicit merge decision = %#v", decision)
	}

	nonDirectClaim := testClaim()
	nonDirect := testObservation(nonDirectClaim)
	nonDirect.DirectParent = false
	nonDirect.ParentSHA = strings.Repeat("7", 40)
	nonDirect = SealObservation(nonDirect)
	if decision := Evaluate(nonDirectClaim, nonDirect, binding, 1_001); decision.Code != CodeGraphRejected {
		t.Fatalf("default non-direct decision = %#v", decision)
	}
	nonDirectClaim.GraphPolicy.AllowNonDirectParent = true
	nonDirect.ClaimSHA256 = ClaimSHA256(nonDirectClaim)
	nonDirect = SealObservation(nonDirect)
	if decision := Evaluate(nonDirectClaim, nonDirect, binding, 1_001); decision.Kind != DecisionAdmit {
		t.Fatalf("explicit non-direct decision = %#v", decision)
	}
}
