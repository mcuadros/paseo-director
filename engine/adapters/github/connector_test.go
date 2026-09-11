// SPDX-License-Identifier: Apache-2.0

package github

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	feedbackdomain "github.com/mcuadros/director-engine/domain/feedback"
	publicationdomain "github.com/mcuadros/director-engine/domain/publication"
	githubport "github.com/mcuadros/director-engine/ports/github"
)

type queuedRunner struct {
	results   []Result
	arguments [][]string
	inputs    [][]byte
}

func TestFeedbackConnectorPaginatesAndAttestsHumanBotAndAppWithoutMutation(t *testing.T) {
	now := time.Date(2026, 9, 11, 1, 0, 0, 0, time.UTC).UnixMilli()
	head, base := strings.Repeat("b", 40), strings.Repeat("a", 40)
	body := `[{"id":1,"node_id":"PRR_human","body":"Please fix this","state":"CHANGES_REQUESTED","commit_id":"` + head + `","submitted_at":"2026-09-11T00:59:00Z","user":{"id":11,"node_id":"U_human","login":"owner","type":"User"}},{"id":2,"node_id":"PRR_bot","body":"Automated note","state":"COMMENTED","commit_id":"` + head + `","submitted_at":"2026-09-11T00:59:01Z","user":{"id":12,"node_id":"U_bot","login":"dependabot[bot]","type":"Bot"}}]`
	output := []byte("HTTP/2.0 200 OK\r\nx-ratelimit-remaining: 4999\r\nx-ratelimit-reset: 2000000000\r\nlink: <https://api.github.com/example?page=2>; rel=\"next\"\r\n\r\n" + body)
	runner := &queuedRunner{results: []Result{{Started: true, Stdout: output}}}
	request := githubport.FeedbackRequest{Owner: "example", Name: "product", RepositoryID: 123, RepositoryNodeID: "R_node",
		PullRequestNumber: 7, Source: feedbackdomain.SourceGitHubReview, Page: 1, PageSize: 100,
		CandidateSHA: head, BaseSHA: base, BindingSHA256: strings.Repeat("1", 64), TaskStoreNowMillis: now}
	page, err := NewWithRunner(runner).ObserveFeedbackPage(context.Background(), request)
	if err != nil || !githubport.CurrentFeedbackPage(page, request) || page.NextPage != 2 || page.Complete || len(page.Items) != 2 {
		t.Fatalf("page = %#v, %v", page, err)
	}
	if page.Items[0].Actor.Kind != feedbackdomain.ActorHuman || page.Items[0].Actor.Attestation != feedbackdomain.AttestationGitHubUser ||
		page.Items[0].Kind != feedbackdomain.KindChangesRequested || page.Items[0].Severity != "P1" ||
		page.Items[1].Actor.Kind != feedbackdomain.ActorBot || page.Items[1].Actor.Attestation != feedbackdomain.AttestationGitHubBot {
		t.Fatalf("items = %#v", page.Items)
	}
	arguments := strings.Join(runner.arguments[0], " ")
	if !strings.Contains(arguments, "repos/example/product/pulls/7/reviews?page=1&per_page=100") || strings.Contains(arguments, "--method") {
		t.Fatalf("feedback read arguments = %s", arguments)
	}
}

func TestFeedbackConnectorKeepsIssueCommentContextAmbiguousAndAppNonHuman(t *testing.T) {
	now := time.Date(2026, 9, 11, 1, 0, 0, 0, time.UTC).UnixMilli()
	head, base := strings.Repeat("b", 40), strings.Repeat("a", 40)
	body := `[{"id":3,"node_id":"IC_app","body":"App generated comment","created_at":"2026-09-11T00:59:00Z","updated_at":"2026-09-11T00:59:00Z","user":{"id":13,"node_id":"U_app_bot","login":"review-app[bot]","type":"Bot"},"performed_via_github_app":{"id":9,"node_id":"A_app","slug":"review-app"}},{"id":4,"node_id":"IC_human","body":"Please follow up","created_at":"2026-09-11T00:59:01Z","updated_at":"2026-09-11T00:59:01Z","user":{"id":14,"node_id":"U_human","login":"owner","type":"User"}}]`
	runner := &queuedRunner{results: []Result{{Started: true, Stdout: includedJSON(body, 5_000)}}}
	request := githubport.FeedbackRequest{Owner: "example", Name: "product", RepositoryID: 123, RepositoryNodeID: "R_node",
		PullRequestNumber: 7, Source: feedbackdomain.SourceGitHubIssueComment, Page: 1, PageSize: 100,
		CandidateSHA: head, BaseSHA: base, BindingSHA256: strings.Repeat("1", 64), TaskStoreNowMillis: now}
	page, err := NewWithRunner(runner).ObserveFeedbackPage(context.Background(), request)
	if err != nil || !githubport.CurrentFeedbackPage(page, request) || !page.Complete || len(page.Items) != 2 {
		t.Fatalf("page = %#v, %v", page, err)
	}
	if page.Items[0].Actor.Kind != feedbackdomain.ActorApp || page.Items[0].CandidateSHA != "" ||
		page.Items[1].Actor.Kind != feedbackdomain.ActorHuman || page.Items[1].CandidateSHA != "" {
		t.Fatalf("issue comments = %#v", page.Items)
	}
}

func TestFeedbackConnectorBindsReviewThreadAndDiffContextByDigest(t *testing.T) {
	now := time.Date(2026, 9, 11, 1, 0, 0, 0, time.UTC).UnixMilli()
	head, base := strings.Repeat("b", 40), strings.Repeat("a", 40)
	body := `[{"id":5,"node_id":"RC_parent","pull_request_review_id":90,"body":"Parent","commit_id":"` + head + `","original_commit_id":"` + head + `","path":"internal/private.go","diff_hunk":"@@ -1 +1 @@","line":3,"side":"RIGHT","created_at":"2026-09-11T00:59:00Z","updated_at":"2026-09-11T00:59:00Z","user":{"id":14,"node_id":"U_human","login":"owner","type":"User"}},{"id":6,"node_id":"RC_reply","pull_request_review_id":90,"in_reply_to_id":5,"body":"Reply","commit_id":"` + head + `","original_commit_id":"` + head + `","path":"internal/private.go","diff_hunk":"@@ -1 +1 @@","line":3,"side":"RIGHT","created_at":"2026-09-11T00:59:01Z","updated_at":"2026-09-11T00:59:01Z","user":{"id":14,"node_id":"U_human","login":"owner","type":"User"}}]`
	runner := &queuedRunner{results: []Result{{Started: true, Stdout: includedJSON(body, 5_000)}}}
	request := githubport.FeedbackRequest{Owner: "example", Name: "product", RepositoryID: 123, RepositoryNodeID: "R_node",
		PullRequestNumber: 7, Source: feedbackdomain.SourceGitHubReviewComment, Page: 1, PageSize: 100,
		CandidateSHA: head, BaseSHA: base, BindingSHA256: strings.Repeat("1", 64), TaskStoreNowMillis: now}
	page, err := NewWithRunner(runner).ObserveFeedbackPage(context.Background(), request)
	if err != nil || !githubport.CurrentFeedbackPage(page, request) || len(page.Items) != 2 ||
		page.Items[0].ContextSHA256 == page.Items[1].ContextSHA256 || strings.Contains(page.Items[0].ContextSHA256, "private.go") {
		t.Fatalf("thread context = %#v, %v", page.Items, err)
	}
}

func (runner *queuedRunner) Run(_ context.Context, arguments []string, input []byte) Result {
	runner.arguments = append(runner.arguments, append([]string(nil), arguments...))
	runner.inputs = append(runner.inputs, append([]byte(nil), input...))
	if len(runner.results) == 0 {
		return Result{}
	}
	result := runner.results[0]
	runner.results = runner.results[1:]
	return result
}

func includedJSON(body string, remaining int) []byte {
	return []byte(fmt.Sprintf("HTTP/2.0 200 OK\r\nx-ratelimit-remaining: %d\r\nx-ratelimit-reset: 2000000000\r\n\r\n%s", remaining, body))
}

func TestConnectorPreflightBindsViewerCapabilitiesIdentityAndRate(t *testing.T) {
	runner := &queuedRunner{results: []Result{
		{Started: true, Stdout: includedJSON(`{"login":"example"}`, 5_000)},
		{Started: true, Stdout: includedJSON(`{"id":123,"node_id":"R_node","name":"product","owner":{"login":"example"},"archived":false,"disabled":false,"permissions":{"pull":true,"push":true}}`, 4_999)},
	}}
	connector := NewWithRunner(runner)
	observation, err := connector.ObserveRepository(context.Background(), githubport.RepositoryRequest{Owner: "example", Name: "product",
		RepositoryID: 123, RepositoryNodeID: "R_node", ExpectedViewer: "example", TaskStoreNowMillis: 1_000})
	binding := publicationdomain.SealBinding(publicationdomain.Binding{TaskID: "dir-m4.5", RunID: "run-1", CandidateID: "candidate-1",
		CandidateSHA: strings.Repeat("b", 40), BaseSHA: strings.Repeat("a", 40), TreeSHA: strings.Repeat("c", 40),
		ManifestSHA256: strings.Repeat("1", 64), CandidateGeneration: 1, TaskVersion: 1, Branch: "task/dir-m4.5",
		BaseRef: "refs/heads/main", RepositoryBindingSHA256: strings.Repeat("2", 64), CanonicalRemote: "https://github.com/example/product",
		GitHubRepositoryID: 123, GitHubRepositoryNodeID: "R_node", RepositoryOwner: "example", RepositoryName: "product",
		HeadOwner: "example", OwnershipSHA256: strings.Repeat("3", 64), PolicySHA256: strings.Repeat("4", 64)})
	if err != nil || observation.Code != publicationdomain.CodeOK || !publicationdomain.CurrentRepositoryObservation(observation, binding, 1_000) {
		t.Fatalf("observation = %#v, %v", observation, err)
	}
	for _, arguments := range runner.arguments {
		joined := strings.Join(arguments, " ")
		if !strings.Contains(joined, "X-GitHub-Api-Version: 2022-11-28") {
			t.Fatalf("API version absent from %v", arguments)
		}
		if strings.Contains(strings.ToLower(joined), "token=") || strings.Contains(joined, "github_pat_") {
			t.Fatal("credential entered argv")
		}
	}
}

func TestConnectorClassifiesTLSRateAndHTTPFailuresWithoutInferringAbsence(t *testing.T) {
	tests := []struct {
		name   string
		result Result
		want   publicationdomain.ExternalCode
	}{
		{"tls", Result{Started: true, ExitCode: 1, Stderr: []byte("x509: certificate signed by unknown authority")}, publicationdomain.CodeTLS},
		{"401", Result{Started: true, ExitCode: 1, HTTPStatus: 401}, publicationdomain.CodeUnauthorized},
		{"403", Result{Started: true, ExitCode: 1, HTTPStatus: 403}, publicationdomain.CodeForbidden},
		{"rate", Result{Started: true, ExitCode: 1, HTTPStatus: 403, Stderr: []byte("API rate limit exceeded")}, publicationdomain.CodeRateLimited},
		{"404", Result{Started: true, ExitCode: 1, HTTPStatus: 404}, publicationdomain.CodeNotFound},
		{"409", Result{Started: true, ExitCode: 1, HTTPStatus: 409}, publicationdomain.CodeConflict},
		{"422", Result{Started: true, ExitCode: 1, HTTPStatus: 422}, publicationdomain.CodeUnprocessable},
		{"500", Result{Started: true, ExitCode: 1, HTTPStatus: 500}, publicationdomain.CodeServer},
		{"unavailable", Result{}, publicationdomain.CodeUnavailable},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			runner := &queuedRunner{results: []Result{test.result}}
			observation, _ := NewWithRunner(runner).ObserveRepository(context.Background(), githubport.RepositoryRequest{Owner: "example", Name: "product", RepositoryID: 123, RepositoryNodeID: "R_node", ExpectedViewer: "example", TaskStoreNowMillis: 1_000})
			if observation.Code != test.want {
				t.Fatalf("code = %s, want %s", observation.Code, test.want)
			}
			if observation.Code == publicationdomain.CodeOK {
				t.Fatal("failed request became desired state")
			}
		})
	}
}

func TestConnectorReturnsOneBoundedPullRequestPageAndPublicMarkerHash(t *testing.T) {
	marker := "<!-- director-publication/v1 task=dir-m4.5 run=run-1 branch=task/dir-m4.5 owner-sha256=" + strings.Repeat("a", 64) + " -->"
	body := `[{"number":7,"node_id":"PR_node_7","state":"open","draft":true,"title":"title","body":"body\n\n` + marker + `\n","html_url":"https://github.com/example/product/pull/7","updated_at":"2026-09-11T00:00:00Z","user":{"login":"example"},"head":{"sha":"` + strings.Repeat("b", 40) + `","ref":"task/dir-m4.5","user":{"login":"example"},"repo":{"id":123}},"base":{"ref":"main","repo":{"id":123}}}]`
	runner := &queuedRunner{results: []Result{{Started: true, Stdout: includedJSON(body, 5_000)}}}
	page, err := NewWithRunner(runner).ListPullRequests(context.Background(), githubport.ListRequest{Owner: "example", Name: "product",
		HeadOwner: "example", HeadRef: "task/dir-m4.5", Page: 1, PageSize: 100, TaskStoreNowMillis: 1_000, Marker: marker})
	if err != nil || !publicationdomain.CurrentPullRequestPage(page, 1, 1_000) || len(page.PullRequests) != 1 {
		t.Fatalf("page = %#v, %v", page, err)
	}
	pull := page.PullRequests[0]
	if pull.MarkerCount != 1 || pull.MarkerSHA256 != publicationdomain.DigestText(marker) || !pull.CreatedByViewer || !pull.Draft {
		t.Fatalf("pull = %#v", pull)
	}
}

func TestConnectorMutationsUseBodyStdinAndExactRepositoryArguments(t *testing.T) {
	pull := `{"number":7,"state":"open","updated_at":"2026-09-11T00:00:00Z","head":{"sha":"` + strings.Repeat("b", 40) + `"},"base":{"ref":"main"}}`
	runner := &queuedRunner{results: []Result{{Started: true, Stdout: includedJSON(`{}`, 5_000)},
		{Started: true, Stdout: includedJSON(pull, 5_000)}, {Started: true}}}
	connector := NewWithRunner(runner)
	created, _ := connector.CreatePullRequest(context.Background(), githubport.CreateRequest{Owner: "example", Name: "product",
		HeadOwner: "example", HeadRef: "task/dir-m4.5", BaseRef: "main", ExpectedHeadSHA: strings.Repeat("b", 40),
		Title: "title", Body: "public body", Draft: true, TaskStoreNowMillis: 1_000})
	if !created.Handoff || created.Code != publicationdomain.CodeOK {
		t.Fatalf("create = %#v", created)
	}
	if strings.Contains(strings.Join(runner.arguments[0], " "), "public body") || !strings.Contains(string(runner.inputs[0]), "public body") {
		t.Fatal("PR body was not isolated to stdin")
	}
	ready, _ := connector.SetPullRequestDraft(context.Background(), githubport.PullRequestRequest{Owner: "example", Name: "product", Number: 7,
		ExpectedHeadSHA: strings.Repeat("b", 40), ExpectedBaseRef: "main", ExpectedUpdatedAt: time.Date(2026, 9, 11, 0, 0, 0, 0, time.UTC).UnixMilli(), Draft: true})
	if !ready.Handoff || !strings.Contains(strings.Join(runner.arguments[2], " "), "--undo") || !strings.Contains(strings.Join(runner.arguments[2], " "), "example/product") {
		t.Fatalf("draft = %#v args=%v", ready, runner.arguments[2])
	}
}
