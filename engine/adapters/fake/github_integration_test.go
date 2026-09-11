// SPDX-License-Identifier: Apache-2.0

package fake

import (
	"context"
	"strings"
	"testing"

	domainintegration "github.com/mcuadros/director-engine/domain/integration"
	publicationdomain "github.com/mcuadros/director-engine/domain/publication"
	githubport "github.com/mcuadros/director-engine/ports/github"
)

func fakeIntegrationBinding(t *testing.T) domainintegration.Binding {
	t.Helper()
	policy, _ := domainintegration.NewPolicy(domainintegration.ModeAutomatic, strings.Repeat("1", 64))
	binding := domainintegration.SealBinding(domainintegration.Binding{TaskID: "task-1", RunID: "run-1", CandidateID: "candidate-1", CandidateSHA: strings.Repeat("b", 40),
		BaseSHA: strings.Repeat("a", 40), TreeSHA: strings.Repeat("c", 40), ManifestSHA256: strings.Repeat("2", 64), CandidateGeneration: 1, TaskVersion: 1,
		ConfigurationSHA256: policy.ConfigurationSHA256, RepositoryID: "repository-1", RepositoryBindingSHA256: strings.Repeat("3", 64), CanonicalRemote: "https://github.com/example/product",
		GitHubRepositoryID: 123, GitHubRepositoryNodeID: "R_node", RepositoryOwner: "example", RepositoryName: "product", ViewerLogin: "example", Branch: "task/task-1", BaseRef: "refs/heads/main",
		PullRequestNumber: 7, PullRequestNodeID: "PR_node_7", PublicationEvidenceID: "publication-1", ValidationPolicySHA256: strings.Repeat("4", 64), ValidationEvidenceID: "validation-1",
		OwnershipSHA256: strings.Repeat("8", 64), MarkerSHA256: strings.Repeat("9", 64),
		ValidationEvidenceSHA256: strings.Repeat("5", 64), ReviewEvidenceID: "review-1", ReviewerUUID: "11111111-1111-4111-8111-111111111111", CIObservationID: "ci-1",
		CIObservationSHA256: strings.Repeat("6", 64), FeedbackStateSHA256: strings.Repeat("7", 64), ReadyEvidenceID: "ready-1", PolicySHA256: policy.SHA256, LeaseEpoch: 1})
	if !domainintegration.ValidBinding(binding) {
		t.Fatal("binding")
	}
	return binding
}

func TestFakeGitHubExpectedHeadMergeIsAtomicAndIdempotentlyObservable(t *testing.T) {
	binding := fakeIntegrationBinding(t)
	forge := NewGitHub(123, "R_node", "example", "product", "example")
	forge.NextMergeCommitSHA = strings.Repeat("d", 40)
	forge.Pulls = []publicationdomain.PullRequest{{Number: 7, NodeID: "PR_node_7", URL: "https://github.com/example/product/pull/7", State: "open", Draft: false, HeadSHA: binding.CandidateSHA,
		HeadRef: binding.Branch, BaseRef: "main", HeadOwner: "example", HeadRepositoryID: 123, BaseRepositoryID: 123, AuthorLogin: "example", CreatedByViewer: true, MarkerCount: 1,
		MarkerSHA256: binding.MarkerSHA256, TitleSHA256: strings.Repeat("9", 64), BodySHA256: strings.Repeat("a", 64), UpdatedAtMillis: 1_000}}
	request := githubport.IntegrationRequest{Binding: binding, TaskStoreNowMillis: 2_000}
	ready, err := forge.ObserveIntegration(context.Background(), request)
	if err != nil || ready.Status != domainintegration.ForgeReady {
		t.Fatalf("ready = %#v %v", ready, err)
	}
	result, err := forge.MergeExpectedHead(context.Background(), githubport.MergeRequest{Binding: binding, Attempt: 1, ExpectedObservation: ready, TaskStoreNowMillis: 2_000})
	if err != nil || !result.Handoff || forge.MergeDispatches != 1 {
		t.Fatalf("merge = %#v %v", result, err)
	}
	merged, _ := forge.ObserveIntegration(context.Background(), request)
	if merged.Status != domainintegration.ForgeIntegrated || merged.MergeCommitSHA != strings.Repeat("d", 40) {
		t.Fatalf("merged = %#v", merged)
	}
	second, _ := forge.MergeExpectedHead(context.Background(), githubport.MergeRequest{Binding: binding, Attempt: 2, ExpectedObservation: ready, TaskStoreNowMillis: 2_000})
	if second.Code != domainintegration.CodeConflict || forge.MergeDispatches != 1 {
		t.Fatalf("duplicate = %#v dispatches=%d", second, forge.MergeDispatches)
	}
}

func TestFakeGitHubRejectsMovedHeadAndUnavailableAtomicPrimitive(t *testing.T) {
	binding := fakeIntegrationBinding(t)
	forge := NewGitHub(123, "R_node", "example", "product", "example")
	forge.Pulls = []publicationdomain.PullRequest{{Number: 7, NodeID: "PR_node_7", URL: "https://github.com/example/product/pull/7", State: "open", HeadSHA: strings.Repeat("e", 40), HeadRef: binding.Branch, BaseRef: "main", HeadOwner: "example", HeadRepositoryID: 123, BaseRepositoryID: 123, AuthorLogin: "example", CreatedByViewer: true, TitleSHA256: strings.Repeat("9", 64), BodySHA256: strings.Repeat("a", 64), UpdatedAtMillis: 1_000}}
	observation, _ := forge.ObserveIntegration(context.Background(), githubport.IntegrationRequest{Binding: binding, TaskStoreNowMillis: 2_000})
	if observation.Status != domainintegration.ForgeInvalid {
		t.Fatalf("moved head = %#v", observation)
	}
	forge.Pulls[0].HeadSHA = binding.CandidateSHA
	forge.AtomicExpectedHead = false
	observation, _ = forge.ObserveIntegration(context.Background(), githubport.IntegrationRequest{Binding: binding, TaskStoreNowMillis: 2_000})
	if observation.Code != domainintegration.CodeAtomicHeadUnavailable {
		t.Fatalf("atomic = %#v", observation)
	}
}
