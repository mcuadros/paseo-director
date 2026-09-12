// SPDX-License-Identifier: Apache-2.0

package publication

import (
	"strings"
	"testing"

	"github.com/mcuadros/director-engine/internal/testkit/secretfixture"
)

func testBinding(candidate string) Binding {
	return SealBinding(Binding{TaskID: "dir-m4.5", RunID: "run-1", CandidateID: "candidate-1",
		CandidateSHA: candidate, BaseSHA: strings.Repeat("a", 40), TreeSHA: strings.Repeat("c", 40),
		ManifestSHA256: strings.Repeat("1", 64), CandidateGeneration: 1, TaskVersion: 3,
		Branch: "task/dir-m4.5-publication", BaseRef: "refs/heads/main",
		RepositoryBindingSHA256: strings.Repeat("2", 64), CanonicalRemote: "https://github.com/example/product",
		GitHubRepositoryID: 123, GitHubRepositoryNodeID: "R_kgDOExample", RepositoryOwner: "example",
		RepositoryName: "product", HeadOwner: "example", OwnershipSHA256: strings.Repeat("3", 64)})
}

func TestPolicyDefaultsToReviewBeforePRAndRequiresExplicitDraftMode(t *testing.T) {
	reviewFirst := NewPolicy("pull_request", false, []string{"release", "release"})
	if !ValidPolicy(reviewFirst) || reviewFirst.Timing != TimingReviewBeforePR {
		t.Fatal("review-before-PR was not the valid default")
	}
	draftFirst := NewPolicy("pull_request", true, nil)
	if !ValidPolicy(draftFirst) || draftFirst.Timing != TimingPublishBeforePR {
		t.Fatal("explicit draft mode was not retained")
	}
	if ValidPolicy(NewPolicy("direct", false, nil)) {
		t.Fatal("direct delivery entered the PR publication contract")
	}
}

func TestMarkerIsStableAcrossCandidateCorrectionButBindingIsNot(t *testing.T) {
	policy := NewPolicy("pull_request", true, nil)
	first := testBinding(strings.Repeat("b", 40))
	first.PolicySHA256 = policy.SHA256
	first = SealBinding(first)
	second := first
	second.CandidateID = "candidate-2"
	second.CandidateSHA = strings.Repeat("d", 40)
	second.CandidateGeneration = 2
	second = SealBinding(second)
	if Marker(first) != Marker(second) {
		t.Fatal("correction changed the Run-owned public marker")
	}
	if first.BindingSHA256 == second.BindingSHA256 {
		t.Fatal("correction retained Candidate-bound authority")
	}
}

func TestTemplateIsDeterministicBoundedAndRejectsSecretsOrPrivatePaths(t *testing.T) {
	policy := NewPolicy("pull_request", false, nil)
	binding := testBinding(strings.Repeat("b", 40))
	binding.PolicySHA256 = policy.SHA256
	binding = SealBinding(binding)
	first, ok := RenderTemplate(binding, policy, "Implement publication", "approve_candidate", "review-1", "passed", "ci-1", []string{"P2_BOUND", "P2_BOUND"})
	second, again := RenderTemplate(binding, policy, "Implement publication", "approve_candidate", "review-1", "passed", "ci-1", []string{"P2_BOUND"})
	if !ok || !again || !ValidTemplate(first, binding, policy) || first.SHA256 != second.SHA256 || first.Body != second.Body {
		t.Fatal("template was not deterministic")
	}
	for _, forbidden := range []string{secretfixture.GitHubFineGrainedLetters(), "/tmp/private-evidence", "/home/operator/control.json"} {
		if _, accepted := RenderTemplate(binding, policy, "publish "+forbidden, "pending", "", "pending", "", nil); accepted {
			t.Fatalf("unsafe public text %q was accepted", forbidden)
		}
	}
	if strings.Contains(first.Body, "/tmp/") || strings.Contains(first.Body, "/home/") || !strings.Contains(first.Body, Marker(binding)) {
		t.Fatal("template leaked a path or omitted ownership")
	}
}
