// SPDX-License-Identifier: Apache-2.0

package git

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"

	candidatedomain "github.com/mcuadros/director-engine/domain/candidate"
	domaincleanup "github.com/mcuadros/director-engine/domain/cleanup"
	cleanuport "github.com/mcuadros/director-engine/ports/cleanup"
)

type cleanupFixture struct {
	repositoryFixture
	adapter      *CleanupAdapter
	policy       domaincleanup.Policy
	binding      domaincleanup.Binding
	manifest     candidatedomain.Manifest
	artifactRoot string
}

func newCleanupFixture(t *testing.T, objectFormat string, integrated bool) cleanupFixture {
	t.Helper()
	fixture := newRepositoryFixture(t, objectFormat)
	observation := observe(t, New(), fixture.request)
	decision := candidatedomain.Evaluate(fixture.request.Claim, observation, fixture.request.RepositoryBindingSHA256, fixture.request.TaskStoreNowMillis)
	if decision.Manifest == nil {
		t.Fatal("Candidate manifest")
	}
	policy, ok := domaincleanup.NewPolicy(fixture.request.Claim.ConfigurationSHA256)
	if !ok {
		t.Fatal("policy")
	}
	binding := domaincleanup.Binding{ProjectID: fixture.request.Claim.ProjectID, WorkspaceID: fixture.request.Claim.WorkspaceID,
		TaskID: fixture.request.Claim.TaskID, RunID: fixture.request.Claim.RunID, CandidateID: "candidate-1",
		CandidateSHA: fixture.candidate, BaseSHA: fixture.base, TreeSHA: decision.Manifest.TreeSHA, CandidateGeneration: 1,
		TaskVersion: fixture.request.Claim.TaskVersion, ConfigurationSHA256: fixture.request.Claim.ConfigurationSHA256,
		RepositoryID: fixture.request.Repository.RepositoryID, RepositoryBindingSHA256: fixture.request.RepositoryBindingSHA256,
		SourceDevice: observation.SourceDevice, SourceInode: observation.SourceInode, CommonDevice: observation.CommonDevice,
		CommonInode: observation.CommonInode, WorktreeDevice: observation.WorktreeDevice, WorktreeInode: observation.WorktreeInode,
		CanonicalRemoteSHA256: domaincleanup.DigestText(fixture.request.Repository.CanonicalRemote),
		SourcePathSHA256:      domaincleanup.DigestText(fixture.source), CommonDirectorySHA256: domaincleanup.DigestText(fixture.request.Repository.GitCommonDirectory),
		WorktreePathSHA256: domaincleanup.DigestText(fixture.worktree), Branch: fixture.request.Claim.Branch,
		BaseRef: fixture.request.Claim.BaseRef, WorktreeID: fixture.request.Claim.WorktreeID, TaskAgentID: "agent-1",
		TaskWorkspaceID: "workspace-native-1", OwnershipSHA256: strings.Repeat("8", 64),
		CleanupAdmittedAtMillis: fixture.request.TaskStoreNowMillis, LeaseEpoch: fixture.request.Claim.LeaseEpoch, PolicySHA256: policy.SHA256}
	if integrated {
		binding.IntegrationKind, binding.IntegrationEvidenceID = "pull_request", "integration-evidence-1"
		binding.IntegrationEvidenceSHA256, binding.MergeCommitSHA = strings.Repeat("9", 64), fixture.candidate
	}
	binding = domaincleanup.SealBinding(binding)
	if !domaincleanup.ValidBinding(binding) {
		t.Fatalf("cleanup binding = %#v", binding)
	}
	artifactRoot := filepath.Join(fixture.root, "private-recovery")
	if err := os.Mkdir(artifactRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	return cleanupFixture{repositoryFixture: fixture, adapter: NewCleanup(), policy: policy,
		binding: binding, manifest: *decision.Manifest, artifactRoot: artifactRoot}
}

func (fixture cleanupFixture) target(t *testing.T, kind domaincleanup.ResourceKind) cleanuport.Target {
	t.Helper()
	trigger := domaincleanup.TriggerCancelled
	if fixture.binding.IntegrationKind != "" {
		trigger = domaincleanup.TriggerIntegrated
	}
	state, ok := domaincleanup.NewState(fixture.binding, fixture.policy, trigger, domaincleanup.LifecycleActive)
	if !ok {
		t.Fatal("cleanup state")
	}
	index := domaincleanup.EffectIndex(state, kind)
	if index < 0 {
		t.Fatalf("effect %s absent", kind)
	}
	return cleanuport.Target{Binding: fixture.binding, Policy: fixture.policy, Repository: fixture.request.Repository,
		RepositoryBindingSHA256: fixture.request.RepositoryBindingSHA256, CandidateClaim: fixture.request.Claim,
		CandidateManifest: fixture.manifest, EffectID: state.Effects[index].ID, EffectKind: kind,
		PrivateArtifactRoot: fixture.artifactRoot, TaskStoreNowMillis: fixture.request.TaskStoreNowMillis}
}

func cleanupObserve(t *testing.T, fixture cleanupFixture, kind domaincleanup.ResourceKind) domaincleanup.Observation {
	t.Helper()
	target := fixture.target(t, kind)
	observation, err := fixture.adapter.Observe(context.Background(), target)
	if err != nil {
		t.Fatal(err)
	}
	if !domaincleanup.ValidObservation(observation, target.Binding, target.EffectID, kind, target.TaskStoreNowMillis) {
		t.Fatalf("invalid observation: %#v", observation)
	}
	return observation
}

func TestCleanupAdapterSnapshotsDirtyWorkAndRetainsSevenDayRefs(t *testing.T) {
	for _, objectFormat := range []string{"sha1", "sha256"} {
		t.Run(objectFormat, func(t *testing.T) {
			fixture := newCleanupFixture(t, objectFormat, false)
			if err := os.WriteFile(filepath.Join(fixture.worktree, "RESULT.md"), []byte("dirty tracked\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(fixture.worktree, "untracked.txt"), []byte("untracked\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			if err := os.Mkdir(filepath.Join(fixture.worktree, "empty-local"), 0o700); err != nil {
				t.Fatal(err)
			}
			target := fixture.target(t, domaincleanup.ResourceSnapshot)
			before, err := fixture.adapter.Observe(context.Background(), target)
			if err != nil || before.Status != domaincleanup.StatusDirty || !before.Dirty {
				t.Fatalf("before = %#v %v", before, err)
			}
			result, err := fixture.adapter.CreateSnapshot(context.Background(), cleanuport.DispatchCommand{Target: target, Attempt: 1, ExpectedObservation: before})
			if err != nil || !result.Handoff || result.Code != domaincleanup.CodeOK {
				t.Fatalf("snapshot = %#v %v", result, err)
			}
			target.Attempt = 1
			after, err := fixture.adapter.Observe(context.Background(), target)
			if err != nil || after.Status != domaincleanup.StatusVerified || after.Snapshot == nil ||
				!domaincleanup.ValidSnapshot(*after.Snapshot, fixture.binding, fixture.policy) {
				t.Fatalf("after = %#v %v", after, err)
			}
			if after.Snapshot.RetentionUntilMillis-after.Snapshot.CreatedAtMillis != 7*24*60*60*1_000 ||
				RetentionExpired(*after.Snapshot, after.Snapshot.RetentionUntilMillis-1) || !RetentionExpired(*after.Snapshot, after.Snapshot.RetentionUntilMillis) {
				t.Fatalf("retention = %#v", after.Snapshot)
			}
			for _, ref := range []string{after.Snapshot.WorktreeRef} {
				if oid := fixtureGit(t, fixture.source, "rev-parse", ref); oid == "" {
					t.Fatal("recovery ref absent")
				}
			}
			if _, err := os.Stat(filepath.Join(fixture.worktree, "RESULT.md")); err != nil {
				t.Fatal("snapshot removed source work")
			}
		})
	}
}

func TestCleanupAdapterPreservesBoundedIgnoredAndEmptyMaterialPrivately(t *testing.T) {
	fixture := newCleanupFixture(t, "sha1", true)
	ignored := filepath.Join(fixture.worktree, "node_modules", "package")
	if err := os.MkdirAll(ignored, 0o700); err != nil {
		t.Fatal(err)
	}
	secretName := "local-private-value.txt"
	if err := os.WriteFile(filepath.Join(ignored, secretName), []byte("private bytes\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(fixture.worktree, "empty-generated"), 0o700); err != nil {
		t.Fatal(err)
	}
	target := fixture.target(t, domaincleanup.ResourceSnapshot)
	before, _ := fixture.adapter.Observe(context.Background(), target)
	if before.Status != domaincleanup.StatusClean || before.Dirty || !before.Ignored || before.IgnoredEntries == 0 {
		t.Fatalf("ignored before = %#v", before)
	}
	if _, err := fixture.adapter.CreateSnapshot(context.Background(), cleanuport.DispatchCommand{Target: target, Attempt: 1, ExpectedObservation: before}); err != nil {
		t.Fatal(err)
	}
	target.Attempt = 1
	after, _ := fixture.adapter.Observe(context.Background(), target)
	if after.Status != domaincleanup.StatusVerified || after.Snapshot == nil || after.Snapshot.PrivateArtifactID == "" {
		t.Fatalf("ignored after = %#v", after)
	}
	encoded := string(mustJSON(t, after))
	if strings.Contains(encoded, secretName) || strings.Contains(encoded, fixture.worktree) || strings.Contains(encoded, "private bytes") {
		t.Fatalf("public observation leaked private material: %s", encoded)
	}
	if !verifyPrivateArtifact(target, *after.Snapshot) {
		t.Fatal("private artifact verification failed")
	}
}

func mustJSON(t *testing.T, value any) []byte {
	t.Helper()
	encoded, err := jsonMarshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return encoded
}

var jsonMarshal = func(value any) ([]byte, error) {
	return json.Marshal(value)
}

func TestCleanupAdapterRefusesDirtyIgnoredSymlinkNestedAndSpecialTrees(t *testing.T) {
	tests := map[string]func(*testing.T, cleanupFixture){
		"dirty plus ignored": func(t *testing.T, fixture cleanupFixture) {
			os.WriteFile(filepath.Join(fixture.worktree, "RESULT.md"), []byte("dirty\n"), 0o600)
			os.MkdirAll(filepath.Join(fixture.worktree, "node_modules"), 0o700)
			os.WriteFile(filepath.Join(fixture.worktree, "node_modules", "private"), []byte("x"), 0o600)
		},
		"untracked symlink": func(t *testing.T, fixture cleanupFixture) {
			os.Symlink("RESULT.md", filepath.Join(fixture.worktree, "alias"))
		},
		"nested repository": func(t *testing.T, fixture cleanupFixture) {
			nested := filepath.Join(fixture.worktree, "nested")
			os.Mkdir(nested, 0o700)
			fixtureGit(t, nested, "init")
		},
		"fifo": func(t *testing.T, fixture cleanupFixture) {
			if err := syscall.Mkfifo(filepath.Join(fixture.worktree, "pipe"), 0o600); err != nil {
				t.Fatal(err)
			}
		},
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			fixture := newCleanupFixture(t, "sha1", false)
			mutate(t, fixture)
			observation := cleanupObserve(t, fixture, domaincleanup.ResourceSnapshot)
			if observation.Status != domaincleanup.StatusAmbiguous || observation.Code != domaincleanup.CodeUnsupportedContent {
				t.Fatalf("observation = %#v", observation)
			}
			if _, err := os.Stat(fixture.worktree); err != nil {
				t.Fatal("unsafe tree was removed")
			}
			if head := fixtureGit(t, fixture.source, "rev-parse", "refs/heads/"+fixture.request.Repository.Branch); head != fixture.candidate {
				t.Fatal("Task ref changed")
			}
		})
	}
}

func TestCleanupAdapterRejectsPathDeviceRepositoryBranchAndConsumerAttacks(t *testing.T) {
	tests := map[string]func(*testing.T, *cleanupFixture){
		"source identity": func(_ *testing.T, fixture *cleanupFixture) {
			fixture.binding.SourceInode++
			fixture.binding = domaincleanup.SealBinding(fixture.binding)
		},
		"common identity": func(_ *testing.T, fixture *cleanupFixture) {
			fixture.binding.CommonDevice++
			fixture.binding = domaincleanup.SealBinding(fixture.binding)
		},
		"worktree identity": func(_ *testing.T, fixture *cleanupFixture) {
			fixture.binding.WorktreeInode++
			fixture.binding = domaincleanup.SealBinding(fixture.binding)
		},
		"repository": func(_ *testing.T, fixture *cleanupFixture) {
			fixture.binding.RepositoryID = "github:other"
			fixture.binding = domaincleanup.SealBinding(fixture.binding)
		},
		"branch": func(_ *testing.T, fixture *cleanupFixture) {
			fixture.binding.Branch = "task/other"
			fixture.binding = domaincleanup.SealBinding(fixture.binding)
		},
		"symlink replacement": func(t *testing.T, fixture *cleanupFixture) {
			replacement := filepath.Join(fixture.root, "replacement")
			if err := os.Rename(fixture.worktree, replacement); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(replacement, fixture.worktree); err != nil {
				t.Fatal(err)
			}
		},
		"foreign consumer": func(t *testing.T, fixture *cleanupFixture) {
			other := filepath.Join(fixture.root, "other")
			fixtureGit(t, fixture.source, "worktree", "add", "--detach", other, fixture.candidate)
			fixtureGit(t, other, "symbolic-ref", "HEAD", "refs/heads/"+fixture.request.Repository.Branch)
		},
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			fixture := newCleanupFixture(t, "sha1", true)
			mutate(t, &fixture)
			target := fixture.target(t, domaincleanup.ResourceWorktree)
			observation, _ := fixture.adapter.Observe(context.Background(), target)
			if observation.Status != domaincleanup.StatusAmbiguous && observation.Status != domaincleanup.StatusDifferent {
				t.Fatalf("attack admitted: %#v", observation)
			}
		})
	}
}

func integratedRemoteFixture(t *testing.T, objectFormat string) cleanupFixture {
	t.Helper()
	fixture := newCleanupFixture(t, objectFormat, true)
	remote := bareRemote(t, fixture.repositoryFixture, objectFormat)
	fixtureGit(t, fixture.worktree, "push", remote, fixture.candidate+":refs/heads/"+fixture.request.Repository.Branch)
	fixtureGit(t, fixture.source, "checkout", "-b", "merge-cleanup", fixture.base)
	fixtureGit(t, fixture.source, "merge", "--no-ff", "--no-edit", fixture.candidate)
	merge := fixtureGit(t, fixture.source, "rev-parse", "HEAD")
	fixtureGit(t, fixture.source, "push", "--force", remote, merge+":refs/heads/main")
	fixture.binding.MergeCommitSHA = merge
	fixture.binding = domaincleanup.SealBinding(fixture.binding)
	fixture.adapter.cleanupRemoteOverride = remote
	return fixture
}

func TestCleanupAdapterDeletesRemoteOnlyWithExactLeaseAndVerifiesAbsence(t *testing.T) {
	for _, objectFormat := range []string{"sha1", "sha256"} {
		t.Run(objectFormat, func(t *testing.T) {
			fixture := integratedRemoteFixture(t, objectFormat)
			target := fixture.target(t, domaincleanup.ResourceRemoteRef)
			before, err := fixture.adapter.Observe(context.Background(), target)
			if err != nil || before.Status != domaincleanup.StatusExactPresent || before.CurrentOID != fixture.candidate {
				t.Fatalf("before = %#v %v", before, err)
			}
			result, err := fixture.adapter.DeleteRemoteRef(context.Background(), cleanuport.DispatchCommand{Target: target, Attempt: 1, ExpectedObservation: before})
			if err != nil || !result.Handoff || result.Code != domaincleanup.CodeOK {
				t.Fatalf("delete = %#v %v", result, err)
			}
			target.Attempt = 1
			after, _ := fixture.adapter.Observe(context.Background(), target)
			if after.Status != domaincleanup.StatusAbsent {
				t.Fatalf("absence not verified: %#v", after)
			}
		})
	}
}

func TestCleanupAdapterRemoteRaceAndSameSHARecreationRemainRecoverable(t *testing.T) {
	fixture := integratedRemoteFixture(t, "sha1")
	target := fixture.target(t, domaincleanup.ResourceRemoteRef)
	before, _ := fixture.adapter.Observe(context.Background(), target)
	fixtureGit(t, fixture.source, "commit", "--allow-empty", "-m", "foreign remote branch")
	foreign := fixtureGit(t, fixture.source, "rev-parse", "HEAD")
	fixtureGit(t, fixture.source, "push", "--force", fixture.adapter.cleanupRemoteOverride, foreign+":refs/heads/"+fixture.binding.Branch)
	if _, err := fixture.adapter.DeleteRemoteRef(context.Background(), cleanuport.DispatchCommand{Target: target, Attempt: 1, ExpectedObservation: before}); err == nil {
		t.Fatal("moved remote ref was deleted")
	}
	if head := fixtureGit(t, fixture.root, "--git-dir", fixture.adapter.cleanupRemoteOverride, "rev-parse", "refs/heads/"+fixture.binding.Branch); head != foreign {
		t.Fatal("foreign ref changed")
	}

	fixture = integratedRemoteFixture(t, "sha1")
	target = fixture.target(t, domaincleanup.ResourceRemoteRef)
	before, _ = fixture.adapter.Observe(context.Background(), target)
	fixture.adapter.afterCleanupRemoteDelete = func() {
		fixtureGit(t, fixture.worktree, "push", fixture.adapter.cleanupRemoteOverride, fixture.candidate+":refs/heads/"+fixture.binding.Branch)
	}
	if _, err := fixture.adapter.DeleteRemoteRef(context.Background(), cleanuport.DispatchCommand{Target: target, Attempt: 1, ExpectedObservation: before}); err != nil {
		t.Fatal(err)
	}
	target.Attempt = 1
	after, _ := fixture.adapter.Observe(context.Background(), target)
	if after.Status != domaincleanup.StatusExactPresent || after.CurrentOID != fixture.candidate {
		t.Fatalf("same-SHA recreation was not preserved: %#v", after)
	}
}

func TestCleanupAdapterLocalRefRequiresAbsentWorktreeAndNoConsumer(t *testing.T) {
	fixture := newCleanupFixture(t, "sha1", true)
	target := fixture.target(t, domaincleanup.ResourceLocalRef)
	if observation, _ := fixture.adapter.Observe(context.Background(), target); observation.Code != domaincleanup.CodeConsumerPresent {
		t.Fatalf("live worktree did not block local deletion: %#v", observation)
	}
	worktreeTarget := fixture.target(t, domaincleanup.ResourceWorktree)
	worktreeObservation, _ := fixture.adapter.Observe(context.Background(), worktreeTarget)
	if _, err := fixture.adapter.RemoveWorktree(context.Background(), cleanuport.DispatchCommand{Target: worktreeTarget, Attempt: 1,
		ExpectedObservation: worktreeObservation}); err != nil {
		t.Fatal(err)
	}
	before, _ := fixture.adapter.Observe(context.Background(), target)
	if before.Status != domaincleanup.StatusExactPresent {
		t.Fatalf("local before = %#v", before)
	}
	if _, err := fixture.adapter.DeleteLocalRef(context.Background(), cleanuport.DispatchCommand{Target: target, Attempt: 1, ExpectedObservation: before}); err != nil {
		t.Fatal(err)
	}
	target.Attempt = 1
	after, _ := fixture.adapter.Observe(context.Background(), target)
	if after.Status != domaincleanup.StatusAbsent {
		t.Fatalf("local after = %#v", after)
	}
}

func TestEveryDestructiveGateReverifiesStoredSnapshot(t *testing.T) {
	fixture := newCleanupFixture(t, "sha1", false)
	if err := os.WriteFile(filepath.Join(fixture.worktree, "RESULT.md"), []byte("dirty\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	snapshotTarget := fixture.target(t, domaincleanup.ResourceSnapshot)
	before, _ := fixture.adapter.Observe(context.Background(), snapshotTarget)
	if _, err := fixture.adapter.CreateSnapshot(context.Background(), cleanuport.DispatchCommand{Target: snapshotTarget,
		Attempt: 1, ExpectedObservation: before}); err != nil {
		t.Fatal(err)
	}
	snapshotTarget.Attempt = 1
	verified, _ := fixture.adapter.Observe(context.Background(), snapshotTarget)
	if verified.Snapshot == nil {
		t.Fatal("snapshot missing")
	}
	fixtureGit(t, fixture.source, "update-ref", "-d", verified.Snapshot.WorktreeRef, verified.Snapshot.WorktreeCommitSHA)
	worktreeTarget := fixture.target(t, domaincleanup.ResourceWorktree)
	worktreeBefore, _ := fixture.adapter.Observe(context.Background(), worktreeTarget)
	if _, err := fixture.adapter.RemoveWorktree(context.Background(), cleanuport.DispatchCommand{Target: worktreeTarget,
		Attempt: 1, ExpectedObservation: worktreeBefore, Snapshot: verified.Snapshot}); err == nil {
		t.Fatal("missing recovery ref authorized worktree removal")
	}
	if _, err := os.Stat(fixture.worktree); err != nil {
		t.Fatal("worktree was not preserved")
	}
	if head := fixtureGit(t, fixture.source, "rev-parse", "refs/heads/"+fixture.binding.Branch); head != fixture.candidate {
		t.Fatal("Task ref changed")
	}
}

func TestCleanupAdapterErrorsContainNoPathsOrPrivateMaterial(t *testing.T) {
	fixture := newCleanupFixture(t, "sha1", false)
	target := fixture.target(t, domaincleanup.ResourceSnapshot)
	target.Repository.WorktreePath = filepath.Join(fixture.root, "missing-private-path")
	observation, err := fixture.adapter.Observe(context.Background(), target)
	if err != nil {
		t.Fatal(err)
	}
	encoded := string(mustJSON(t, observation))
	if strings.Contains(encoded, fixture.root) || strings.Contains(encoded, "missing-private-path") {
		t.Fatalf("path leaked: %s", encoded)
	}
	var dispatch *cleanuport.DispatchError
	_, err = fixture.adapter.CreateSnapshot(context.Background(), cleanuport.DispatchCommand{Target: target, Attempt: 1, ExpectedObservation: observation})
	if err == nil || !errors.As(err, &dispatch) || strings.Contains(err.Error(), fixture.root) {
		t.Fatalf("unbounded error = %v", err)
	}
}

func TestCleanupAdapterRefusesReplacedCommonDirectoryAfterWorktreeRemoval(t *testing.T) {
	fixture := newCleanupFixture(t, "sha1", true)
	worktreeTarget := fixture.target(t, domaincleanup.ResourceWorktree)
	worktreeBefore, _ := fixture.adapter.Observe(context.Background(), worktreeTarget)
	if _, err := fixture.adapter.RemoveWorktree(context.Background(), cleanuport.DispatchCommand{Target: worktreeTarget,
		Attempt: 1, ExpectedObservation: worktreeBefore}); err != nil {
		t.Fatal(err)
	}
	originalCommon := fixture.request.Repository.GitCommonDirectory + ".owned-original"
	if err := os.Rename(fixture.request.Repository.GitCommonDirectory, originalCommon); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(fixture.request.Repository.GitCommonDirectory, 0o700); err != nil {
		t.Fatal(err)
	}
	target := fixture.target(t, domaincleanup.ResourceLocalRef)
	observation, _ := fixture.adapter.Observe(context.Background(), target)
	if observation.Status != domaincleanup.StatusAmbiguous && observation.Status != domaincleanup.StatusDifferent {
		t.Fatalf("replacement common directory admitted: %#v", observation)
	}
	if head := fixtureGit(t, fixture.root, "--git-dir", originalCommon, "rev-parse", "refs/heads/"+fixture.binding.Branch); head != fixture.candidate {
		t.Fatal("owned ref in original common directory changed")
	}
}

func TestCleanupAdapterExpiresPrivateArtifactThenRecoveryRefsAtExactBoundary(t *testing.T) {
	fixture := newCleanupFixture(t, "sha1", true)
	ignored := filepath.Join(fixture.worktree, "node_modules")
	if err := os.MkdirAll(ignored, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(ignored, "cache.bin"), []byte("cache"), 0o600); err != nil {
		t.Fatal(err)
	}
	target := fixture.target(t, domaincleanup.ResourceSnapshot)
	before, _ := fixture.adapter.Observe(context.Background(), target)
	if _, err := fixture.adapter.CreateSnapshot(context.Background(), cleanuport.DispatchCommand{Target: target, Attempt: 1,
		ExpectedObservation: before}); err != nil {
		t.Fatal(err)
	}
	target.Attempt = 1
	verified, _ := fixture.adapter.Observe(context.Background(), target)
	if verified.Status != domaincleanup.StatusVerified || verified.Snapshot == nil || verified.Snapshot.PrivateArtifactID == "" {
		t.Fatalf("snapshot = %#v", verified)
	}

	state, _ := domaincleanup.NewState(fixture.binding, fixture.policy, domaincleanup.TriggerIntegrated, domaincleanup.LifecycleActive)
	agentIndex := domaincleanup.EffectIndex(state, domaincleanup.ResourceTaskAgent)
	agent := state.Effects[agentIndex]
	agentObservation := domaincleanup.SealObservation(domaincleanup.Observation{EffectID: agent.ID, BindingSHA256: state.Binding.SHA256,
		Kind: agent.Kind, Status: domaincleanup.StatusTerminated, Code: domaincleanup.CodeOK, Archived: true, ProcessAbsent: true,
		ObservedAtMillis: target.TaskStoreNowMillis, MaximumAgeMillis: domaincleanup.MaximumObservationAgeMillis})
	state, _ = domaincleanup.RecordObservation(state, agent.Kind, agentObservation, target.TaskStoreNowMillis)
	state, _ = domaincleanup.CompleteEffect(state, agent.Kind, nil)
	snapshotIndex := domaincleanup.EffectIndex(state, domaincleanup.ResourceSnapshot)
	snapshotObservation := verified
	snapshotObservation.EffectID = state.Effects[snapshotIndex].ID
	snapshotObservation.Attempt = 0
	snapshotObservation = domaincleanup.SealObservation(snapshotObservation)
	state, _ = domaincleanup.RecordObservation(state, domaincleanup.ResourceSnapshot, snapshotObservation, target.TaskStoreNowMillis)
	state, _ = domaincleanup.CompleteEffect(state, domaincleanup.ResourceSnapshot, verified.Snapshot)

	expiryTarget := func(kind domaincleanup.ResourceKind) cleanuport.Target {
		effect := state.RetentionEffects[domaincleanup.RetentionEffectIndex(state, kind)]
		value := target
		value.EffectID, value.EffectKind, value.Attempt, value.Snapshot = effect.ID, kind, 0, verified.Snapshot
		value.TaskStoreNowMillis = verified.Snapshot.RetentionUntilMillis
		return value
	}
	privateTarget := expiryTarget(domaincleanup.ResourcePrivateArtifact)
	privateBefore, _ := fixture.adapter.Observe(context.Background(), privateTarget)
	if privateBefore.Status != domaincleanup.StatusExactPresent {
		t.Fatalf("private before = %#v", privateBefore)
	}
	if _, err := fixture.adapter.ExpirePrivateArtifact(context.Background(), cleanuport.DispatchCommand{Target: privateTarget,
		Attempt: 1, ExpectedObservation: privateBefore, Snapshot: verified.Snapshot}); err != nil {
		t.Fatal(err)
	}
	privateTarget.Attempt = 1
	privateAfter, _ := fixture.adapter.Observe(context.Background(), privateTarget)
	if privateAfter.Status != domaincleanup.StatusAbsent {
		t.Fatalf("private after = %#v", privateAfter)
	}

	refsTarget := expiryTarget(domaincleanup.ResourceRecoveryRef)
	refsBefore, _ := fixture.adapter.Observe(context.Background(), refsTarget)
	if refsBefore.Status != domaincleanup.StatusExactPresent {
		t.Fatalf("refs before = %#v", refsBefore)
	}
	if _, err := fixture.adapter.ExpireRecoveryRefs(context.Background(), cleanuport.DispatchCommand{Target: refsTarget,
		Attempt: 1, ExpectedObservation: refsBefore, Snapshot: verified.Snapshot}); err != nil {
		t.Fatal(err)
	}
	refsTarget.Attempt = 1
	refsAfter, _ := fixture.adapter.Observe(context.Background(), refsTarget)
	if refsAfter.Status != domaincleanup.StatusAbsent {
		t.Fatalf("refs after = %#v", refsAfter)
	}
}
