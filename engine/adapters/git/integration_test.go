// SPDX-License-Identifier: Apache-2.0

package git

import (
	"context"
	"strings"
	"testing"

	candidatedomain "github.com/mcuadros/director-engine/domain/candidate"
	domainintegration "github.com/mcuadros/director-engine/domain/integration"
	gitport "github.com/mcuadros/director-engine/ports/git"
)

func integrationRequest(t *testing.T, fixture repositoryFixture, remote string) (*Adapter, gitport.IntegrationRequest) {
	t.Helper()
	adapter := New()
	adapter.deliveryRemoteOverride = remote
	observed := observe(t, adapter, fixture.request)
	manifest := candidatedomain.Evaluate(fixture.request.Claim, observed, fixture.request.RepositoryBindingSHA256, fixture.request.TaskStoreNowMillis).Manifest
	if manifest == nil {
		t.Fatal("Candidate manifest")
	}
	policy, _ := domainintegration.NewPolicy(domainintegration.ModeAutomatic, fixture.request.Claim.ConfigurationSHA256)
	binding := domainintegration.SealBinding(domainintegration.Binding{TaskID: fixture.request.Claim.TaskID, RunID: fixture.request.Claim.RunID,
		CandidateID: "candidate-1", CandidateSHA: fixture.candidate, BaseSHA: fixture.base, TreeSHA: manifest.TreeSHA,
		ManifestSHA256: manifest.BindingSHA256, CandidateGeneration: 1, TaskVersion: fixture.request.Claim.TaskVersion,
		ConfigurationSHA256: fixture.request.Claim.ConfigurationSHA256, RepositoryID: fixture.request.Repository.RepositoryID,
		RepositoryBindingSHA256: fixture.request.RepositoryBindingSHA256, CanonicalRemote: fixture.request.Repository.CanonicalRemote,
		GitHubRepositoryID: 123, GitHubRepositoryNodeID: "R_node", RepositoryOwner: "example", RepositoryName: "candidate-fixture",
		ViewerLogin: "example", Branch: fixture.request.Claim.Branch, BaseRef: fixture.request.Claim.BaseRef, PullRequestNumber: 7,
		PullRequestNodeID: "PR_node_7", PublicationEvidenceID: "publication-1", ValidationPolicySHA256: strings.Repeat("1", 64),
		OwnershipSHA256: strings.Repeat("a", 64), MarkerSHA256: strings.Repeat("b", 64),
		ValidationEvidenceID: "validation-1", ValidationEvidenceSHA256: strings.Repeat("2", 64), ReviewEvidenceID: "review-1",
		ReviewerUUID: "11111111-1111-4111-8111-111111111111", CIObservationID: "ci-1", CIObservationSHA256: strings.Repeat("3", 64),
		FeedbackStateSHA256: strings.Repeat("4", 64), ReadyEvidenceID: "ready-1", PolicySHA256: policy.SHA256, LeaseEpoch: 7})
	if !domainintegration.ValidBinding(binding) {
		t.Fatal("integration binding")
	}
	return adapter, gitport.IntegrationRequest{Binding: binding, CandidateClaim: fixture.request.Claim, CandidateManifest: *manifest,
		Repository: fixture.request.Repository, RepositoryBindingSHA256: fixture.request.RepositoryBindingSHA256,
		TaskStoreNowMillis: fixture.request.TaskStoreNowMillis}
}

func TestIntegrationAdapterVerifiesLiveRefsDefaultRefAndExactMergeGraph(t *testing.T) {
	for _, format := range []string{"sha1", "sha256"} {
		t.Run(format, func(t *testing.T) {
			fixture := newRepositoryFixture(t, format)
			remote := bareRemote(t, fixture, format)
			fixtureGit(t, fixture.worktree, "push", remote, fixture.candidate+":refs/heads/task/task-1")
			adapter, request := integrationRequest(t, fixture, remote)
			before, err := adapter.ObserveIntegration(context.Background(), request)
			if err != nil || before.Status != domainintegration.GitReady || !domainintegration.CurrentGitObservation(before, request.Binding, "", request.TaskStoreNowMillis) {
				t.Fatalf("before = %#v %v", before, err)
			}
			fixtureGit(t, fixture.source, "checkout", "-b", "integration-fixture", fixture.base)
			fixtureGit(t, fixture.source, "merge", "--no-ff", "--no-edit", fixture.candidate)
			merge := fixtureGit(t, fixture.source, "rev-parse", "HEAD")
			fixtureGit(t, fixture.source, "push", "--force", remote, merge+":refs/heads/main")
			request.MergeCommitSHA = merge
			after, err := adapter.ObserveIntegration(context.Background(), request)
			if err != nil || after.Status != domainintegration.GitIntegrated || !domainintegration.CurrentGitObservation(after, request.Binding, merge, request.TaskStoreNowMillis) ||
				len(after.ParentSHAs) != 2 || after.ParentSHAs[0] != fixture.base || after.ParentSHAs[1] != fixture.candidate || after.TreeSHA != request.Binding.TreeSHA {
				t.Fatalf("after = %#v %v", after, err)
			}
		})
	}
}

func TestIntegrationAdapterRejectsMovedBaseAndWrongMergeParents(t *testing.T) {
	fixture := newRepositoryFixture(t, "sha1")
	remote := bareRemote(t, fixture, "sha1")
	fixtureGit(t, fixture.worktree, "push", remote, fixture.candidate+":refs/heads/task/task-1")
	adapter, request := integrationRequest(t, fixture, remote)
	fixtureGit(t, fixture.source, "checkout", "-b", "foreign", fixture.base)
	fixtureGit(t, fixture.source, "commit", "--allow-empty", "-m", "foreign")
	foreign := fixtureGit(t, fixture.source, "rev-parse", "HEAD")
	fixtureGit(t, fixture.source, "push", "--force", remote, foreign+":refs/heads/main")
	moved, _ := adapter.ObserveIntegration(context.Background(), request)
	if moved.Status != domainintegration.GitInvalid || moved.Code != domainintegration.CodeBaseChanged {
		t.Fatalf("moved = %#v", moved)
	}
	request.MergeCommitSHA = foreign
	wrong, _ := adapter.ObserveIntegration(context.Background(), request)
	if wrong.Status != domainintegration.GitInvalid || wrong.Code != domainintegration.CodeGraphMismatch {
		t.Fatalf("wrong graph = %#v", wrong)
	}
}
