// SPDX-License-Identifier: Apache-2.0

package fake

import (
	"context"
	"strings"
	"testing"

	"github.com/mcuadros/director-engine/domain/correction"
	feedbackdomain "github.com/mcuadros/director-engine/domain/feedback"
	githubport "github.com/mcuadros/director-engine/ports/github"
)

func TestFakeGitHubFeedbackUsesTheSameBoundedPaginationContract(t *testing.T) {
	forge := NewGitHub(123, "R_node", "example", "product", "example")
	items := make([]feedbackdomain.Item, 0, 101)
	for index := range 101 {
		items = append(items, feedbackdomain.Item{Source: feedbackdomain.SourceGitHubReviewComment,
			ExternalID: "comment-" + string(rune('a'+index%26)) + "-" + string(rune('A'+index/26)), RevisionID: "revision-1",
			Actor: feedbackdomain.Actor{Kind: feedbackdomain.ActorHuman, ID: "U_human", Login: "owner", Authenticated: true,
				Attestation: feedbackdomain.AttestationGitHubUser}, Kind: feedbackdomain.KindComment,
			CandidateSHA: strings.Repeat("1", 40), BaseSHA: strings.Repeat("0", 40), ContextSHA256: strings.Repeat("a", 64),
			Body: "Bounded comment", Actionable: true, Severity: correction.SeverityP3, CreatedAtMillis: 900, UpdatedAtMillis: 950})
	}
	forge.Feedback[feedbackdomain.SourceGitHubReviewComment] = items
	request := githubport.FeedbackRequest{Owner: "example", Name: "product", RepositoryID: 123, RepositoryNodeID: "R_node",
		PullRequestNumber: 7, Source: feedbackdomain.SourceGitHubReviewComment, Page: 1, PageSize: 100,
		CandidateSHA: strings.Repeat("1", 40), BaseSHA: strings.Repeat("0", 40), BindingSHA256: strings.Repeat("b", 64), TaskStoreNowMillis: 1_000}
	first, err := forge.ObserveFeedbackPage(context.Background(), request)
	if err != nil || !githubport.CurrentFeedbackPage(first, request) || first.NextPage != 2 || len(first.Items) != 100 {
		t.Fatalf("first page = %#v, %v", first, err)
	}
	request.Page = 2
	second, err := forge.ObserveFeedbackPage(context.Background(), request)
	if err != nil || !githubport.CurrentFeedbackPage(second, request) || !second.Complete || len(second.Items) != 1 {
		t.Fatalf("second page = %#v, %v", second, err)
	}
}
