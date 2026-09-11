// SPDX-License-Identifier: Apache-2.0

package git

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"

	candidatedomain "github.com/mcuadros/director-engine/domain/candidate"
	"github.com/mcuadros/director-engine/domain/execution"
	repositorydomain "github.com/mcuadros/director-engine/domain/repository"
	gitport "github.com/mcuadros/director-engine/ports/git"
)

type repositoryFixture struct {
	root, source, worktree, base, candidate string
	request                                 gitport.CandidateRequest
}

func fixtureGit(t *testing.T, directory string, arguments ...string) string {
	t.Helper()
	command := exec.Command("git", arguments...)
	command.Dir = directory
	command.Env = append(os.Environ(), "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=/dev/null")
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("fixture Git %v failed: %v: %s", arguments, err, output)
	}
	return strings.TrimSpace(string(output))
}

func statIdentity(t *testing.T, path string) (string, uint64, uint64) {
	t.Helper()
	real, err := filepath.EvalSymlinks(path)
	if err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(real)
	if err != nil {
		t.Fatal(err)
	}
	identity := info.Sys().(*syscall.Stat_t)
	return real, uint64(identity.Dev), identity.Ino
}

func newRepositoryFixture(t *testing.T, objectFormat string) repositoryFixture {
	t.Helper()
	root := t.TempDir()
	source := filepath.Join(root, "source")
	worktree := filepath.Join(root, "worktree")
	if err := os.Mkdir(source, 0o700); err != nil {
		t.Fatal(err)
	}
	arguments := []string{"init", "--initial-branch=main"}
	if objectFormat == "sha256" {
		arguments = append(arguments, "--object-format=sha256")
	}
	fixtureGit(t, source, arguments...)
	fixtureGit(t, source, "config", "user.name", "Candidate fixture")
	fixtureGit(t, source, "config", "user.email", "candidate@example.invalid")
	remote, err := repositorydomain.CanonicalRemote("https://github.com/example/candidate-fixture.git")
	if err != nil {
		t.Fatal(err)
	}
	fixtureGit(t, source, "remote", "add", "origin", remote.Canonical)
	if err := os.WriteFile(filepath.Join(source, "README.md"), []byte("base\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(source, ".gitignore"), []byte("node_modules/\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	fixtureGit(t, source, "add", "README.md", ".gitignore")
	fixtureGit(t, source, "commit", "-m", "fixture: base")
	base := fixtureGit(t, source, "rev-parse", "HEAD")
	fixtureGit(t, source, "worktree", "add", "-b", "task/task-1", worktree, base)
	if err := os.WriteFile(filepath.Join(worktree, "RESULT.md"), []byte("candidate\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	fixtureGit(t, worktree, "add", "RESULT.md")
	fixtureGit(t, worktree, "commit", "-m", "fixture: candidate")
	candidate := fixtureGit(t, worktree, "rev-parse", "HEAD")
	source, sourceDevice, sourceInode := statIdentity(t, source)
	worktree, _, _ = statIdentity(t, worktree)
	common, commonDevice, commonInode := statIdentity(t, filepath.Join(source, ".git"))
	repository := execution.RepositoryBinding{
		RepositoryID: remote.ID, RepositoryKey: remote.Key, CanonicalRemote: remote.Canonical,
		SourcePath: source, SourceDevice: sourceDevice, SourceInode: sourceInode,
		GitCommonDirectory: common, GitCommonDevice: commonDevice, GitCommonInode: commonInode,
		WorktreePath: worktree, Branch: "task/task-1", BaseSHA: base,
	}
	claim := candidatedomain.Claim{
		SchemaVersion: candidatedomain.ClaimSchemaVersion, ID: "claim-1",
		ProjectID: "project-1", WorkspaceID: "workspace-1", TaskID: "task-1", RunID: "run-1",
		ActorID: "paseo:worker-1", WorktreeID: "worktree-1", Branch: repository.Branch, BaseRef: "refs/heads/main",
		CandidateSHA: candidate, BaseSHA: base, LeaseEpoch: 7, ExpectedRunVersion: 9, TaskVersion: 3,
		AcceptanceSHA256: strings.Repeat("a", 64), ConfigurationSHA256: strings.Repeat("b", 64),
		ProfileSHA256: strings.Repeat("c", 64), ContextSHA256: strings.Repeat("d", 64),
		DecisionsSHA256: strings.Repeat("e", 64), FindingsSHA256: strings.Repeat("f", 64),
	}
	request := gitport.CandidateRequest{
		Claim: claim, Repository: repository, RepositoryBindingSHA256: execution.RepositoryBindingSHA256(repository),
		TaskStoreNowMillis: 10_000,
	}
	return repositoryFixture{root: root, source: source, worktree: worktree, base: base, candidate: candidate, request: request}
}

func observe(t *testing.T, adapter *Adapter, request gitport.CandidateRequest) candidatedomain.Observation {
	t.Helper()
	observation, err := adapter.ObserveCandidate(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	if !candidatedomain.ValidObservation(observation) {
		t.Fatalf("invalid normalized observation: %#v", observation)
	}
	return observation
}

func TestAdapterAdmitsExactDirectParentFromRegisteredLinkedWorktree(t *testing.T) {
	for _, format := range []string{"sha1", "sha256"} {
		t.Run(format, func(t *testing.T) {
			fixture := newRepositoryFixture(t, format)
			observation := observe(t, New(), fixture.request)
			if observation.Code != candidatedomain.CodeOK || observation.CommitSHA != fixture.candidate ||
				observation.BaseRefHeadSHA != fixture.base || !observation.DirectParent || observation.TreeSHA == "" {
				t.Fatalf("exact observation = %#v", observation)
			}
			decision := candidatedomain.Evaluate(fixture.request.Claim, observation, fixture.request.RepositoryBindingSHA256, 10_000)
			if decision.Kind != candidatedomain.DecisionAdmit || decision.Manifest == nil || !candidatedomain.ValidManifest(*decision.Manifest) {
				t.Fatalf("admission = %#v", decision)
			}
		})
	}
}

func TestRelevantBaseChangeRejectsUnchangedCandidateAndAdmitsRebasedCommit(t *testing.T) {
	fixture := newRepositoryFixture(t, "sha1")
	if err := os.WriteFile(filepath.Join(fixture.source, "BASE.md"), []byte("advanced base\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	fixtureGit(t, fixture.source, "add", "BASE.md")
	fixtureGit(t, fixture.source, "commit", "-m", "fixture: advance base")
	newBase := fixtureGit(t, fixture.source, "rev-parse", "HEAD")
	stale := fixture.request
	stale.Claim.BaseSHA = newBase
	stale.Repository.BaseSHA = newBase
	stale.RepositoryBindingSHA256 = execution.RepositoryBindingSHA256(stale.Repository)
	observation := observe(t, New(), stale)
	decision := candidatedomain.Evaluate(stale.Claim, observation, stale.RepositoryBindingSHA256, 10_000)
	if decision.Kind != candidatedomain.DecisionPark || decision.Code != candidatedomain.CodeGraphRejected || observation.DescendsFromBase {
		t.Fatalf("unchanged Candidate on moved base = %#v %#v", observation, decision)
	}
	fixtureGit(t, fixture.worktree, "rebase", "main")
	rebasedSHA := fixtureGit(t, fixture.worktree, "rev-parse", "HEAD")
	if rebasedSHA == fixture.candidate {
		t.Fatal("rebase did not create a fresh Candidate")
	}
	rebased := stale
	rebased.Claim.CandidateSHA = rebasedSHA
	rebasedObservation := observe(t, New(), rebased)
	rebasedDecision := candidatedomain.Evaluate(rebased.Claim, rebasedObservation, rebased.RepositoryBindingSHA256, 10_000)
	if rebasedDecision.Kind != candidatedomain.DecisionAdmit || rebasedDecision.Manifest == nil ||
		!rebasedObservation.DescendsFromBase || !rebasedObservation.DirectParent || rebasedObservation.BaseRefHeadSHA != newBase {
		t.Fatalf("rebased Candidate = %#v %#v", rebasedObservation, rebasedDecision)
	}
}

func TestAdapterRejectsEveryDirtyOrAmbiguousWorktreeClass(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*testing.T, repositoryFixture)
		want   candidatedomain.Code
	}{
		{"tracked", func(t *testing.T, f repositoryFixture) {
			os.WriteFile(filepath.Join(f.worktree, "RESULT.md"), []byte("dirty\n"), 0o600)
		}, candidatedomain.CodeWorktreeDirty},
		{"untracked", func(t *testing.T, f repositoryFixture) {
			os.WriteFile(filepath.Join(f.worktree, "untracked.txt"), []byte("dirty\n"), 0o600)
		}, candidatedomain.CodeUntracked},
		{"ignored node_modules", func(t *testing.T, f repositoryFixture) {
			os.MkdirAll(filepath.Join(f.worktree, "node_modules", "package"), 0o700)
			os.WriteFile(filepath.Join(f.worktree, "node_modules", "package", "index.js"), []byte("generated\n"), 0o600)
		}, candidatedomain.CodeIgnored},
		{"intent to add", func(t *testing.T, f repositoryFixture) {
			os.WriteFile(filepath.Join(f.worktree, "intent.txt"), []byte("intent\n"), 0o600)
			fixtureGit(t, f.worktree, "add", "-N", "intent.txt")
		}, candidatedomain.CodeIntentToAdd},
		{"skip worktree", func(t *testing.T, f repositoryFixture) {
			fixtureGit(t, f.worktree, "update-index", "--skip-worktree", "README.md")
		}, candidatedomain.CodeSparseCheckout},
		{"conflict stages", func(t *testing.T, f repositoryFixture) {
			readme := fixtureGit(t, f.worktree, "rev-parse", "HEAD:README.md")
			result := fixtureGit(t, f.worktree, "rev-parse", "HEAD:RESULT.md")
			command := exec.Command("git", "update-index", "--index-info")
			command.Dir = f.worktree
			command.Stdin = strings.NewReader("0 0000000000000000000000000000000000000000\tRESULT.md\n100644 " + readme + " 1\tRESULT.md\n100644 " + result + " 2\tRESULT.md\n100644 " + readme + " 3\tRESULT.md\n")
			if output, err := command.CombinedOutput(); err != nil {
				t.Fatalf("create conflict stages: %v: %s", err, output)
			}
		}, candidatedomain.CodeConflict},
		{"empty directory", func(t *testing.T, f repositoryFixture) { os.Mkdir(filepath.Join(f.worktree, "empty-generated"), 0o700) }, candidatedomain.CodeUnsafePath},
		{"untracked symlink", func(t *testing.T, f repositoryFixture) { os.Symlink("RESULT.md", filepath.Join(f.worktree, "alias")) }, candidatedomain.CodeUntracked},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fixture := newRepositoryFixture(t, "sha1")
			test.mutate(t, fixture)
			observation := observe(t, New(), fixture.request)
			if observation.Code != test.want {
				t.Fatalf("diagnostic = %s, want %s", observation.Code, test.want)
			}
			if decision := candidatedomain.Evaluate(fixture.request.Claim, observation, fixture.request.RepositoryBindingSHA256, 10_000); decision.Kind != candidatedomain.DecisionPark {
				t.Fatalf("unsafe state admitted: %#v", decision)
			}
		})
	}
}

func TestAdapterRejectsDirtySubmoduleAndAcceptsOnlyExactCleanGitlink(t *testing.T) {
	fixture := newRepositoryFixture(t, "sha1")
	submodule := filepath.Join(fixture.root, "submodule-source")
	if err := os.Mkdir(submodule, 0o700); err != nil {
		t.Fatal(err)
	}
	fixtureGit(t, submodule, "init", "--initial-branch=main")
	fixtureGit(t, submodule, "config", "user.name", "Submodule fixture")
	fixtureGit(t, submodule, "config", "user.email", "submodule@example.invalid")
	if err := os.WriteFile(filepath.Join(submodule, "MODULE.md"), []byte("module\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	fixtureGit(t, submodule, "add", "MODULE.md")
	fixtureGit(t, submodule, "commit", "-m", "fixture: submodule")
	fixtureGit(t, fixture.worktree, "-c", "protocol.file.allow=always", "submodule", "add", submodule, "module")
	fixtureGit(t, fixture.worktree, "commit", "-am", "fixture: add submodule")
	fixture.request.Claim.CandidateSHA = fixtureGit(t, fixture.worktree, "rev-parse", "HEAD")
	fixture.request.Claim.GraphPolicy.AllowNonDirectParent = true
	if observation := observe(t, New(), fixture.request); observation.Code != candidatedomain.CodeOK || !observation.SubmodulesClean {
		t.Fatalf("clean exact submodule = %#v", observation)
	}
	if err := os.WriteFile(filepath.Join(fixture.worktree, "module", "MODULE.md"), []byte("dirty module\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	observation := observe(t, New(), fixture.request)
	if observation.Code != candidatedomain.CodeWorktreeDirty && observation.Code != candidatedomain.CodeSubmodule {
		t.Fatalf("dirty submodule diagnostic = %s", observation.Code)
	}
}

func TestAdapterRejectsPathAliasesAlternateObjectsAndMaliciousNamesWithoutLeaking(t *testing.T) {
	t.Run("worktree symlink alias", func(t *testing.T) {
		fixture := newRepositoryFixture(t, "sha1")
		alias := filepath.Join(fixture.root, "worktree-alias")
		if err := os.Symlink(fixture.worktree, alias); err != nil {
			t.Fatal(err)
		}
		fixture.request.Repository.WorktreePath = alias
		fixture.request.RepositoryBindingSHA256 = execution.RepositoryBindingSHA256(fixture.request.Repository)
		observation := observe(t, New(), fixture.request)
		if observation.Code != candidatedomain.CodePathAlias {
			t.Fatalf("alias diagnostic = %s", observation.Code)
		}
	})
	t.Run("alternate object store", func(t *testing.T) {
		fixture := newRepositoryFixture(t, "sha1")
		other := newRepositoryFixture(t, "sha1")
		alternates := filepath.Join(fixture.request.Repository.GitCommonDirectory, "objects", "info", "alternates")
		if err := os.WriteFile(alternates, []byte(filepath.Join(other.request.Repository.GitCommonDirectory, "objects")+"\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		observation := observe(t, New(), fixture.request)
		if observation.Code != candidatedomain.CodeObjectStoreAmbiguous {
			t.Fatalf("alternate diagnostic = %s", observation.Code)
		}
	})
	t.Run("malicious committed path", func(t *testing.T) {
		fixture := newRepositoryFixture(t, "sha1")
		name := "secret-token\nname"
		if err := os.WriteFile(filepath.Join(fixture.worktree, name), []byte("private-value\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		fixtureGit(t, fixture.worktree, "add", name)
		fixtureGit(t, fixture.worktree, "commit", "-m", "fixture: unsafe path")
		fixture.request.Claim.CandidateSHA = fixtureGit(t, fixture.worktree, "rev-parse", "HEAD")
		fixture.request.Claim.GraphPolicy.AllowNonDirectParent = true
		observation := observe(t, New(), fixture.request)
		if observation.Code != candidatedomain.CodeUnsafePath {
			t.Fatalf("malicious-path diagnostic = %s", observation.Code)
		}
		encoded := fmt.Sprintf("%#v", observation)
		if strings.Contains(encoded, name) || strings.Contains(encoded, "private-value") || strings.Contains(encoded, fixture.root) {
			t.Fatal("normalized observation leaked a path or content")
		}
	})
}

func TestAdapterRejectsWrongRepositoryRemoteCommonDirectoryAndObjectIdentity(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*testing.T, *repositoryFixture)
		want   candidatedomain.Code
	}{
		{"source filesystem identity", func(_ *testing.T, f *repositoryFixture) {
			f.request.Repository.SourceInode++
			f.request.RepositoryBindingSHA256 = execution.RepositoryBindingSHA256(f.request.Repository)
		}, candidatedomain.CodeRepositoryMismatch},
		{"common directory", func(t *testing.T, f *repositoryFixture) {
			other := newRepositoryFixture(t, "sha1")
			f.request.Repository.GitCommonDirectory = other.request.Repository.GitCommonDirectory
			f.request.Repository.GitCommonDevice = other.request.Repository.GitCommonDevice
			f.request.Repository.GitCommonInode = other.request.Repository.GitCommonInode
			f.request.RepositoryBindingSHA256 = execution.RepositoryBindingSHA256(f.request.Repository)
		}, candidatedomain.CodeRepositoryMismatch},
		{"remote", func(t *testing.T, f *repositoryFixture) {
			other, err := repositorydomain.CanonicalRemote("https://github.com/example/not-this-repository.git")
			if err != nil {
				t.Fatal(err)
			}
			f.request.Repository.RepositoryID, f.request.Repository.RepositoryKey, f.request.Repository.CanonicalRemote = other.ID, other.Key, other.Canonical
			f.request.RepositoryBindingSHA256 = execution.RepositoryBindingSHA256(f.request.Repository)
		}, candidatedomain.CodeRemoteMismatch},
		{"missing commit", func(_ *testing.T, f *repositoryFixture) {
			f.request.Claim.CandidateSHA = strings.Repeat("f", len(f.candidate))
		}, candidatedomain.CodeObjectMissing},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fixture := newRepositoryFixture(t, "sha1")
			test.mutate(t, &fixture)
			observation := observe(t, New(), fixture.request)
			if observation.Code != test.want {
				t.Fatalf("diagnostic = %s, want %s", observation.Code, test.want)
			}
		})
	}
}

func TestAdapterAcceptsCommittedTrackedSymlinkBytes(t *testing.T) {
	fixture := newRepositoryFixture(t, "sha1")
	if err := os.Symlink("RESULT.md", filepath.Join(fixture.worktree, "result-link")); err != nil {
		t.Fatal(err)
	}
	fixtureGit(t, fixture.worktree, "add", "result-link")
	fixtureGit(t, fixture.worktree, "commit", "-m", "fixture: tracked symlink")
	fixture.request.Claim.CandidateSHA = fixtureGit(t, fixture.worktree, "rev-parse", "HEAD")
	fixture.request.Claim.GraphPolicy.AllowNonDirectParent = true
	observation := observe(t, New(), fixture.request)
	if observation.Code != candidatedomain.CodeOK {
		t.Fatalf("tracked symlink observation = %#v", observation)
	}
}

func TestAdapterDetectsBaseAndBranchMovementBetweenSnapshots(t *testing.T) {
	for _, target := range []string{"base", "branch"} {
		t.Run(target, func(t *testing.T) {
			fixture := newRepositoryFixture(t, "sha1")
			adapter := New()
			adapter.afterFirstSnapshot = func() {
				if target == "base" {
					if err := os.WriteFile(filepath.Join(fixture.source, "BASE-MOVED.md"), []byte("moved\n"), 0o600); err != nil {
						t.Fatal(err)
					}
					fixtureGit(t, fixture.source, "add", "BASE-MOVED.md")
					fixtureGit(t, fixture.source, "commit", "-m", "fixture: move base")
					return
				}
				fixtureGit(t, fixture.source, "update-ref", "refs/heads/task/task-1", fixture.base, fixture.candidate)
			}
			observation := observe(t, adapter, fixture.request)
			if observation.Code != candidatedomain.CodeTOCTOU {
				t.Fatalf("movement diagnostic = %s", observation.Code)
			}
		})
	}
}

func TestAdapterRefusesAbbreviatedSHAEvenWithPrefixCollision(t *testing.T) {
	fixture := newRepositoryFixture(t, "sha1")
	seen := map[string]string{}
	prefix := ""
	for index := 0; index < 2_000 && prefix == ""; index++ {
		command := exec.Command("git", "hash-object", "-w", "--stdin")
		command.Dir = fixture.source
		command.Stdin = bytes.NewBufferString(fmt.Sprintf("collision-%d", index))
		output, err := command.Output()
		if err != nil {
			t.Fatal(err)
		}
		oid := strings.TrimSpace(string(output))
		key := oid[:3]
		if prior, exists := seen[key]; exists && prior != oid {
			prefix = key
			break
		}
		seen[key] = oid
	}
	if prefix == "" {
		t.Fatal("failed to create bounded SHA-prefix collision")
	}
	fixture.request.Claim.CandidateSHA = prefix
	observation := observe(t, New(), fixture.request)
	if observation.Code != candidatedomain.CodeClaimInvalid {
		t.Fatalf("abbreviated collision diagnostic = %s", observation.Code)
	}
}

func TestAdapterRejectsNonDirectParentUnlessExplicitGraphAllowanceExists(t *testing.T) {
	fixture := newRepositoryFixture(t, "sha1")
	if err := os.WriteFile(filepath.Join(fixture.worktree, "SECOND.md"), []byte("second\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	fixtureGit(t, fixture.worktree, "add", "SECOND.md")
	fixtureGit(t, fixture.worktree, "commit", "-m", "fixture: second candidate commit")
	fixture.request.Claim.CandidateSHA = fixtureGit(t, fixture.worktree, "rev-parse", "HEAD")
	observation := observe(t, New(), fixture.request)
	if observation.Code != candidatedomain.CodeOK || observation.DirectParent ||
		candidatedomain.Evaluate(fixture.request.Claim, observation, fixture.request.RepositoryBindingSHA256, 10_000).Kind != candidatedomain.DecisionPark {
		t.Fatalf("default graph facts/decision = %#v", observation)
	}
	fixture.request.Claim.GraphPolicy.AllowNonDirectParent = true
	observation = observe(t, New(), fixture.request)
	if observation.Code != candidatedomain.CodeOK || observation.DirectParent {
		t.Fatalf("explicit non-direct observation = %#v", observation)
	}
	if decision := candidatedomain.Evaluate(fixture.request.Claim, observation, fixture.request.RepositoryBindingSHA256, 10_000); decision.Kind != candidatedomain.DecisionAdmit {
		t.Fatalf("explicit non-direct allowance = %#v", decision)
	}
}
