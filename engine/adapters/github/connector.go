// SPDX-License-Identifier: Apache-2.0

// Package github is the thin GitHub CLI connector for PR publication. It
// translates bounded public API responses into engine-owned port values and
// performs one requested operation without selecting workflow or fallback.
package github

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/mcuadros/director-engine/domain/correction"
	feedbackdomain "github.com/mcuadros/director-engine/domain/feedback"
	domainintegration "github.com/mcuadros/director-engine/domain/integration"
	publicationdomain "github.com/mcuadros/director-engine/domain/publication"
	domainvalidation "github.com/mcuadros/director-engine/domain/validation"
	githubport "github.com/mcuadros/director-engine/ports/github"
)

const (
	maximumResponseBytes = 4 * 1024 * 1024
	maximumErrorBytes    = 16 * 1024
	apiVersion           = "2022-11-28"
)

var httpStatusPattern = regexp.MustCompile(`(?i)(?:HTTP[^0-9]+|status(?: code)?[=: ]+)([1-5][0-9]{2})`)

type Result struct {
	Started    bool
	ExitCode   int
	Stdout     []byte
	Stderr     []byte
	HTTPStatus int
}

type Runner interface {
	Run(context.Context, []string, []byte) Result
}

type Connector struct{ runner Runner }

var _ githubport.Port = (*Connector)(nil)
var _ githubport.ChecksPort = (*Connector)(nil)
var _ githubport.FeedbackPort = (*Connector)(nil)
var _ githubport.FeedbackObservationPort = (*Connector)(nil)
var _ githubport.IntegrationPort = (*Connector)(nil)

func New() *Connector { return &Connector{runner: commandRunner{}} }

func NewWithRunner(runner Runner) *Connector { return &Connector{runner: runner} }

type feedbackUser struct {
	ID     int64  `json:"id"`
	NodeID string `json:"node_id"`
	Login  string `json:"login"`
	Type   string `json:"type"`
}

type feedbackApp struct {
	ID     int64  `json:"id"`
	NodeID string `json:"node_id"`
	Slug   string `json:"slug"`
}

type feedbackResponse struct {
	ID                    int64        `json:"id"`
	NodeID                string       `json:"node_id"`
	PullRequestReviewID   int64        `json:"pull_request_review_id"`
	InReplyToID           int64        `json:"in_reply_to_id"`
	Body                  string       `json:"body"`
	State                 string       `json:"state"`
	CommitID              string       `json:"commit_id"`
	OriginalCommitID      string       `json:"original_commit_id"`
	Path                  string       `json:"path"`
	DiffHunk              string       `json:"diff_hunk"`
	Line                  *int64       `json:"line"`
	OriginalLine          *int64       `json:"original_line"`
	Side                  string       `json:"side"`
	StartLine             *int64       `json:"start_line"`
	OriginalStartLine     *int64       `json:"original_start_line"`
	StartSide             string       `json:"start_side"`
	SubmittedAt           string       `json:"submitted_at"`
	CreatedAt             string       `json:"created_at"`
	UpdatedAt             string       `json:"updated_at"`
	User                  feedbackUser `json:"user"`
	PerformedViaGitHubApp *feedbackApp `json:"performed_via_github_app"`
}

func feedbackActor(value feedbackResponse) feedbackdomain.Actor {
	actor := feedbackdomain.Actor{ID: value.User.NodeID, Login: value.User.Login, Authenticated: true}
	if actor.ID == "" {
		actor.ID = "github-user-" + strconv.FormatInt(value.User.ID, 10)
	}
	if value.PerformedViaGitHubApp != nil {
		actor.Kind, actor.Attestation = feedbackdomain.ActorApp, feedbackdomain.AttestationGitHubApp
		if value.PerformedViaGitHubApp.NodeID != "" {
			actor.ID = value.PerformedViaGitHubApp.NodeID
		}
		if value.PerformedViaGitHubApp.Slug != "" {
			actor.Login = value.PerformedViaGitHubApp.Slug
		}
	} else if strings.EqualFold(value.User.Type, "User") {
		actor.Kind, actor.Attestation = feedbackdomain.ActorHuman, feedbackdomain.AttestationGitHubUser
	} else {
		actor.Kind, actor.Attestation = feedbackdomain.ActorBot, feedbackdomain.AttestationGitHubBot
	}
	return actor
}

func feedbackTime(value string) (int64, bool) {
	parsed, err := time.Parse(time.RFC3339Nano, value)
	return parsed.UnixMilli(), err == nil
}

func feedbackKind(source feedbackdomain.Source, state string) (feedbackdomain.Kind, bool, bool) {
	if source != feedbackdomain.SourceGitHubReview {
		return feedbackdomain.KindComment, true, true
	}
	switch strings.ToUpper(state) {
	case "CHANGES_REQUESTED":
		return feedbackdomain.KindChangesRequested, true, true
	case "COMMENTED", "PENDING":
		return feedbackdomain.KindComment, true, true
	case "APPROVED":
		return feedbackdomain.KindApproved, false, true
	case "DISMISSED":
		return feedbackdomain.KindDismissed, false, true
	default:
		return "", false, false
	}
}

func feedbackEndpoint(request githubport.FeedbackRequest) (string, bool) {
	base := "repos/" + url.PathEscape(request.Owner) + "/" + url.PathEscape(request.Name)
	identifier := strconv.FormatInt(request.PullRequestNumber, 10)
	switch request.Source {
	case feedbackdomain.SourceGitHubReview:
		return base + "/pulls/" + identifier + "/reviews", true
	case feedbackdomain.SourceGitHubReviewComment:
		return base + "/pulls/" + identifier + "/comments", true
	case feedbackdomain.SourceGitHubIssueComment:
		return base + "/issues/" + identifier + "/comments", true
	default:
		return "", false
	}
}

func hasNextPage(link string) bool {
	for _, part := range strings.Split(link, ",") {
		if strings.Contains(part, `rel="next"`) {
			return true
		}
	}
	return false
}

func normalizeFeedback(value feedbackResponse, request githubport.FeedbackRequest) (feedbackdomain.Item, bool) {
	if value.ID <= 0 || value.User.Login == "" {
		return feedbackdomain.Item{}, false
	}
	kind, actionable, ok := feedbackKind(request.Source, value.State)
	if !ok {
		return feedbackdomain.Item{}, false
	}
	createdText := value.CreatedAt
	if createdText == "" {
		createdText = value.SubmittedAt
	}
	updatedText := value.UpdatedAt
	if updatedText == "" {
		updatedText = value.SubmittedAt
	}
	created, createdOK := feedbackTime(createdText)
	updated, updatedOK := feedbackTime(updatedText)
	if !createdOK || !updatedOK {
		return feedbackdomain.Item{}, false
	}
	externalID := value.NodeID
	if externalID == "" {
		externalID = "github-feedback-" + strconv.FormatInt(value.ID, 10)
	}
	body := value.Body
	if strings.TrimSpace(body) == "" {
		switch kind {
		case feedbackdomain.KindApproved:
			body = "GitHub review approved"
		case feedbackdomain.KindDismissed:
			body = "GitHub review dismissed"
		default:
			body = "GitHub review submitted without a body"
		}
	}
	candidateSHA := value.CommitID
	if request.Source == feedbackdomain.SourceGitHubIssueComment {
		candidateSHA = ""
	}
	number := func(value *int64) string {
		if value == nil {
			return ""
		}
		return strconv.FormatInt(*value, 10)
	}
	contextSHA := feedbackdomain.DigestText(strings.Join([]string{value.CommitID, value.OriginalCommitID,
		strconv.FormatInt(value.PullRequestReviewID, 10), strconv.FormatInt(value.InReplyToID, 10),
		value.Path, value.DiffHunk, number(value.Line), number(value.OriginalLine), value.Side,
		number(value.StartLine), number(value.OriginalStartLine), value.StartSide, request.CandidateSHA, request.BaseSHA}, "\x1f"))
	revision := "github-revision-" + feedbackdomain.DigestText(strings.Join([]string{externalID, updatedText, value.State, body, candidateSHA, contextSHA}, "\x1f"))[:32]
	severity := correction.SeverityP3
	if kind == feedbackdomain.KindChangesRequested {
		severity = correction.SeverityP1
	}
	return feedbackdomain.Item{Source: request.Source, ExternalID: externalID, RevisionID: revision, Actor: feedbackActor(value), Kind: kind,
		CandidateSHA: candidateSHA, BaseSHA: request.BaseSHA, ContextSHA256: contextSHA, Body: body, Actionable: actionable,
		Severity: severity, CreatedAtMillis: created, UpdatedAtMillis: updated}, true
}

// ObserveFeedbackPage performs one bounded read. The GitHub port deliberately
// has no method for replying, resolving a thread, approving, or dismissing.
func (connector *Connector) ObserveFeedbackPage(ctx context.Context, request githubport.FeedbackRequest) (githubport.FeedbackPage, error) {
	page := githubport.FeedbackPage{ID: "github-feedback-page-" + feedbackdomain.DigestText(strings.Join([]string{
		request.Owner, request.Name, strconv.FormatInt(request.PullRequestNumber, 10), string(request.Source), strconv.FormatUint(uint64(request.Page), 10),
		request.CandidateSHA, request.BaseSHA, request.BindingSHA256, strconv.FormatInt(request.TaskStoreNowMillis, 10)}, "\x1f"))[:32],
		Code: string(publicationdomain.CodeUnavailable), Source: request.Source, RepositoryID: request.RepositoryID,
		RepositoryNodeID: request.RepositoryNodeID, PullRequestNumber: request.PullRequestNumber, CandidateSHA: request.CandidateSHA,
		BaseSHA: request.BaseSHA, BindingSHA256: request.BindingSHA256, Page: request.Page,
		ObservedAtMillis: request.TaskStoreNowMillis, MaximumAgeMillis: feedbackdomain.MaximumObservationAge}
	endpoint, ok := feedbackEndpoint(request)
	if !ok || request.Page == 0 || request.Page > feedbackdomain.MaximumPages || request.PageSize == 0 || request.PageSize > feedbackdomain.MaximumPageSize {
		page.Code = "invalid_request"
		return githubport.SealFeedbackPage(page), nil
	}
	query := url.Values{}
	query.Set("per_page", strconv.FormatUint(uint64(request.PageSize), 10))
	query.Set("page", strconv.FormatUint(uint64(request.Page), 10))
	result := connector.api(ctx, "GET", endpoint+"?"+query.Encode(), nil)
	if result.code != publicationdomain.CodeOK {
		page.Code = string(result.code)
		return githubport.SealFeedbackPage(page), nil
	}
	var response []feedbackResponse
	if json.Unmarshal(result.body, &response) != nil || len(response) > int(request.PageSize) {
		page.Code = string(publicationdomain.CodeResponseUnknown)
		return githubport.SealFeedbackPage(page), nil
	}
	page.Items = make([]feedbackdomain.Item, 0, len(response))
	for _, value := range response {
		item, valid := normalizeFeedback(value, request)
		if !valid {
			page.Code = string(publicationdomain.CodeResponseUnknown)
			page.Items = nil
			return githubport.SealFeedbackPage(page), nil
		}
		page.Items = append(page.Items, item)
	}
	page.Code = "ok"
	if hasNextPage(result.headers["link"]) {
		page.NextPage = request.Page + 1
	} else {
		page.Complete = true
	}
	return githubport.SealFeedbackPage(page), nil
}

type limitedBuffer struct {
	bytes.Buffer
	maximum  int
	overflow bool
}

func (buffer *limitedBuffer) Write(value []byte) (int, error) {
	remaining := buffer.maximum - buffer.Len()
	if remaining <= 0 {
		buffer.overflow = true
		return len(value), nil
	}
	if len(value) > remaining {
		_, _ = buffer.Buffer.Write(value[:remaining])
		buffer.overflow = true
		return len(value), nil
	}
	return buffer.Buffer.Write(value)
}

type commandRunner struct{}

func githubEnvironment() []string {
	environment := []string{"LC_ALL=C", "LANG=C", "GH_PROMPT_DISABLED=1", "PAGER=cat", "GH_PAGER=cat"}
	for _, name := range []string{"PATH", "HOME", "XDG_CONFIG_HOME"} {
		if value := os.Getenv(name); value != "" && !strings.ContainsRune(value, 0) {
			environment = append(environment, name+"="+value)
		}
	}
	return environment
}

func (commandRunner) Run(ctx context.Context, arguments []string, input []byte) Result {
	command := exec.CommandContext(ctx, "gh", arguments...)
	command.Env = githubEnvironment()
	command.Stdin = bytes.NewReader(input)
	stdout := &limitedBuffer{maximum: maximumResponseBytes}
	stderr := &limitedBuffer{maximum: maximumErrorBytes}
	command.Stdout, command.Stderr = stdout, stderr
	if err := command.Start(); err != nil {
		return Result{ExitCode: -1}
	}
	err := command.Wait()
	result := Result{Started: true, Stdout: stdout.Bytes(), Stderr: stderr.Bytes()}
	if stdout.overflow || stderr.overflow {
		result.ExitCode = -1
		return result
	}
	if err == nil {
		return result
	}
	var exit *exec.ExitError
	if errors.As(err, &exit) {
		result.ExitCode = exit.ExitCode()
	} else {
		result.ExitCode = -1
	}
	return result
}

func apiArguments(method, endpoint string) []string {
	arguments := []string{"api", "--hostname", "github.com", "--include", "--header", "Accept: application/vnd.github+json",
		"--header", "X-GitHub-Api-Version: " + apiVersion}
	if method != "GET" {
		arguments = append(arguments, "--method", method, "--input", "-")
	}
	return append(arguments, endpoint)
}

type apiResult struct {
	started       bool
	status        int
	body          []byte
	headers       map[string]string
	rateRemaining int64
	rateResetMS   int64
	code          publicationdomain.ExternalCode
}

func responseCode(result Result) publicationdomain.ExternalCode {
	text := strings.ToLower(string(result.Stderr))
	if strings.Contains(text, "x509") || strings.Contains(text, "certificate") || strings.Contains(text, "tls") {
		return publicationdomain.CodeTLS
	}
	status := result.HTTPStatus
	if status == 0 {
		if match := httpStatusPattern.FindStringSubmatch(string(result.Stderr)); len(match) == 2 {
			status, _ = strconv.Atoi(match[1])
		}
	}
	switch status {
	case 401:
		return publicationdomain.CodeUnauthorized
	case 403:
		if strings.Contains(text, "rate limit") {
			return publicationdomain.CodeRateLimited
		}
		return publicationdomain.CodeForbidden
	case 404:
		return publicationdomain.CodeNotFound
	case 409:
		return publicationdomain.CodeConflict
	case 422:
		return publicationdomain.CodeUnprocessable
	default:
		if status >= 500 && status <= 599 {
			return publicationdomain.CodeServer
		}
	}
	return publicationdomain.CodeUnavailable
}

func parseIncluded(output []byte) (int, map[string]string, []byte, bool) {
	if !utf8.Valid(output) {
		return 0, nil, nil, false
	}
	normalized := bytes.ReplaceAll(output, []byte("\r\n"), []byte("\n"))
	separator := bytes.Index(normalized, []byte("\n\n"))
	if separator < 0 {
		return 0, nil, nil, false
	}
	lines := strings.Split(string(normalized[:separator]), "\n")
	fields := strings.Fields(lines[0])
	if len(fields) < 2 {
		return 0, nil, nil, false
	}
	status, err := strconv.Atoi(fields[1])
	if err != nil {
		return 0, nil, nil, false
	}
	headers := map[string]string{}
	for _, line := range lines[1:] {
		name, value, found := strings.Cut(line, ":")
		if found {
			headers[strings.ToLower(strings.TrimSpace(name))] = strings.TrimSpace(value)
		}
	}
	return status, headers, normalized[separator+2:], true
}

func (connector *Connector) api(ctx context.Context, method, endpoint string, input []byte) apiResult {
	result := connector.runner.Run(ctx, apiArguments(method, endpoint), input)
	if !result.Started {
		return apiResult{code: publicationdomain.CodeUnavailable}
	}
	if result.ExitCode != 0 {
		return apiResult{started: true, status: result.HTTPStatus, code: responseCode(result)}
	}
	status, headers, body, ok := parseIncluded(result.Stdout)
	if !ok || status < 200 || status > 299 {
		result.HTTPStatus = status
		return apiResult{started: true, status: status, code: responseCode(result)}
	}
	remaining, remainingErr := strconv.ParseInt(headers["x-ratelimit-remaining"], 10, 64)
	reset, resetErr := strconv.ParseInt(headers["x-ratelimit-reset"], 10, 64)
	if remainingErr != nil || resetErr != nil || remaining <= 0 {
		return apiResult{started: true, status: status, code: publicationdomain.CodeRateLimited}
	}
	return apiResult{started: true, status: status, body: body, headers: headers, rateRemaining: remaining, rateResetMS: reset * 1000, code: publicationdomain.CodeOK}
}

func observationID(prefix string, values ...string) string {
	return prefix + "-" + publicationdomain.DigestText(strings.Join(values, "\x1f"))
}

type repositoryResponse struct {
	ID            int64  `json:"id"`
	NodeID        string `json:"node_id"`
	Name          string `json:"name"`
	Archived      bool   `json:"archived"`
	Disabled      bool   `json:"disabled"`
	DefaultBranch string `json:"default_branch"`
	Owner         struct {
		Login string `json:"login"`
	} `json:"owner"`
	Permissions struct {
		Pull bool `json:"pull"`
		Push bool `json:"push"`
	} `json:"permissions"`
}

type userResponse struct {
	Login string `json:"login"`
}

func (connector *Connector) ObserveRepository(ctx context.Context, request githubport.RepositoryRequest) (publicationdomain.RepositoryObservation, error) {
	observation := publicationdomain.RepositoryObservation{
		ID:   observationID("github-repository", request.Owner, request.Name, strconv.FormatInt(request.TaskStoreNowMillis, 10)),
		Code: publicationdomain.CodeUnavailable, RepositoryID: request.RepositoryID, RepositoryNodeID: request.RepositoryNodeID,
		Owner: request.Owner, Name: request.Name, ViewerLogin: request.ExpectedViewer,
		APIVersion: apiVersion, ObservedAtMillis: request.TaskStoreNowMillis, MaximumAgeMillis: publicationdomain.MaximumObservationAgeMS,
	}
	user := connector.api(ctx, "GET", "user", nil)
	if user.code != publicationdomain.CodeOK {
		observation.Code = user.code
		return publicationdomain.SealRepositoryObservation(observation), nil
	}
	repository := connector.api(ctx, "GET", "repos/"+request.Owner+"/"+request.Name, nil)
	if repository.code != publicationdomain.CodeOK {
		observation.Code = repository.code
		return publicationdomain.SealRepositoryObservation(observation), nil
	}
	var viewer userResponse
	var value repositoryResponse
	if json.Unmarshal(user.body, &viewer) != nil || json.Unmarshal(repository.body, &value) != nil {
		observation.Code = publicationdomain.CodeResponseUnknown
		return publicationdomain.SealRepositoryObservation(observation), nil
	}
	observation.Code = publicationdomain.CodeOK
	observation.RepositoryID, observation.RepositoryNodeID = value.ID, value.NodeID
	observation.Owner, observation.Name, observation.ViewerLogin = value.Owner.Login, value.Name, viewer.Login
	observation.Authenticated, observation.CanPush = viewer.Login != "", value.Permissions.Push
	observation.CanPullRequests = value.Permissions.Pull && value.Permissions.Push
	observation.Archived, observation.Disabled, observation.TLSVerified = value.Archived, value.Disabled, true
	observation.RateRemaining = min(user.rateRemaining, repository.rateRemaining)
	observation.RateResetAtMillis = max(user.rateResetMS, repository.rateResetMS)
	return publicationdomain.SealRepositoryObservation(observation), nil
}

type pullResponse struct {
	Number         int64  `json:"number"`
	NodeID         string `json:"node_id"`
	State          string `json:"state"`
	Draft          bool   `json:"draft"`
	Title          string `json:"title"`
	Body           string `json:"body"`
	HTMLURL        string `json:"html_url"`
	UpdatedAt      string `json:"updated_at"`
	Merged         bool   `json:"merged"`
	MergedAt       string `json:"merged_at"`
	MergeCommitSHA string `json:"merge_commit_sha"`
	Mergeable      *bool  `json:"mergeable"`
	MergeableState string `json:"mergeable_state"`
	User           struct {
		Login string `json:"login"`
	} `json:"user"`
	Head struct {
		SHA  string `json:"sha"`
		Ref  string `json:"ref"`
		User struct {
			Login string `json:"login"`
		} `json:"user"`
		Repo struct {
			ID int64 `json:"id"`
		} `json:"repo"`
	} `json:"head"`
	Base struct {
		Ref  string `json:"ref"`
		Repo struct {
			ID int64 `json:"id"`
		} `json:"repo"`
	} `json:"base"`
}

func normalizedPull(value pullResponse, marker, viewer string) publicationdomain.PullRequest {
	updatedAt := int64(0)
	if parsed, err := timeParse(value.UpdatedAt); err == nil {
		updatedAt = parsed
	}
	count := uint32(strings.Count(value.Body, marker))
	markerHash := ""
	if count > 0 {
		markerHash = publicationdomain.DigestText(marker)
	}
	return publicationdomain.PullRequest{Number: value.Number, NodeID: value.NodeID, URL: value.HTMLURL, State: value.State, Draft: value.Draft,
		HeadSHA: value.Head.SHA, HeadRef: value.Head.Ref, BaseRef: value.Base.Ref, HeadOwner: value.Head.User.Login,
		HeadRepositoryID: value.Head.Repo.ID, BaseRepositoryID: value.Base.Repo.ID, AuthorLogin: value.User.Login,
		CreatedByViewer: strings.EqualFold(value.User.Login, viewer), MarkerSHA256: markerHash, MarkerCount: count,
		TitleSHA256: publicationdomain.DigestText(value.Title), BodySHA256: publicationdomain.DigestText(value.Body), UpdatedAtMillis: updatedAt}
}

func timeParse(value string) (int64, error) {
	parsed, err := time.Parse(time.RFC3339, value)
	if err != nil {
		return 0, err
	}
	return parsed.UnixMilli(), nil
}

func (connector *Connector) ListPullRequests(ctx context.Context, request githubport.ListRequest) (publicationdomain.PullRequestPage, error) {
	page := publicationdomain.PullRequestPage{ID: observationID("github-pulls", request.Owner, request.Name,
		strconv.FormatUint(uint64(request.Page), 10), strconv.FormatInt(request.TaskStoreNowMillis, 10)),
		Code: publicationdomain.CodeUnavailable, Page: request.Page, ObservedAtMillis: request.TaskStoreNowMillis,
		MaximumAgeMillis: publicationdomain.MaximumObservationAgeMS}
	endpoint := fmt.Sprintf("repos/%s/%s/pulls?state=all&head=%s&per_page=%d&page=%d",
		request.Owner, request.Name, url.QueryEscape(request.HeadOwner+":"+request.HeadRef), request.PageSize, request.Page)
	response := connector.api(ctx, "GET", endpoint, nil)
	if response.code != publicationdomain.CodeOK {
		page.Code = response.code
		return publicationdomain.SealPullRequestPage(page), nil
	}
	var values []pullResponse
	if json.Unmarshal(response.body, &values) != nil || len(values) > int(request.PageSize) {
		page.Code = publicationdomain.CodeResponseUnknown
		return publicationdomain.SealPullRequestPage(page), nil
	}
	page.Code = publicationdomain.CodeOK
	page.PullRequests = make([]publicationdomain.PullRequest, 0, len(values))
	for _, value := range values {
		page.PullRequests = append(page.PullRequests, normalizedPull(value, request.Marker, request.HeadOwner))
	}
	if len(values) == int(request.PageSize) {
		page.NextPage = request.Page + 1
	} else {
		page.Complete = true
	}
	return publicationdomain.SealPullRequestPage(page), nil
}

func mutationResult(response apiResult) githubport.DispatchResult {
	return githubport.DispatchResult{Handoff: response.started, Code: response.code}
}

func (connector *Connector) currentPullRequest(ctx context.Context, request githubport.PullRequestRequest) publicationdomain.ExternalCode {
	endpoint := fmt.Sprintf("repos/%s/%s/pulls/%d", request.Owner, request.Name, request.Number)
	response := connector.api(ctx, "GET", endpoint, nil)
	if response.code != publicationdomain.CodeOK {
		return response.code
	}
	var value pullResponse
	if json.Unmarshal(response.body, &value) != nil {
		return publicationdomain.CodeResponseUnknown
	}
	updatedAt, err := timeParse(value.UpdatedAt)
	if err != nil || value.Number != request.Number || value.State != "open" ||
		value.Head.SHA != request.ExpectedHeadSHA || value.Base.Ref != request.ExpectedBaseRef ||
		updatedAt != request.ExpectedUpdatedAt {
		return publicationdomain.CodeHeadChanged
	}
	return publicationdomain.CodeOK
}

func (connector *Connector) CreatePullRequest(ctx context.Context, request githubport.CreateRequest) (githubport.DispatchResult, error) {
	payload, err := json.Marshal(struct {
		Title string `json:"title"`
		Body  string `json:"body"`
		Head  string `json:"head"`
		Base  string `json:"base"`
		Draft bool   `json:"draft"`
	}{request.Title, request.Body, request.HeadOwner + ":" + request.HeadRef, request.BaseRef, request.Draft})
	if err != nil {
		return githubport.DispatchResult{Code: publicationdomain.CodeResponseUnknown}, nil
	}
	return mutationResult(connector.api(ctx, "POST", "repos/"+request.Owner+"/"+request.Name+"/pulls", payload)), nil
}

func (connector *Connector) UpdatePullRequest(ctx context.Context, request githubport.PullRequestRequest) (githubport.DispatchResult, error) {
	if code := connector.currentPullRequest(ctx, request); code != publicationdomain.CodeOK {
		return githubport.DispatchResult{Handoff: false, Code: code}, nil
	}
	payload, err := json.Marshal(struct {
		Title string `json:"title"`
		Body  string `json:"body"`
	}{request.Title, request.Body})
	if err != nil {
		return githubport.DispatchResult{Code: publicationdomain.CodeResponseUnknown}, nil
	}
	endpoint := fmt.Sprintf("repos/%s/%s/pulls/%d", request.Owner, request.Name, request.Number)
	return mutationResult(connector.api(ctx, "PATCH", endpoint, payload)), nil
}

func (connector *Connector) SetPullRequestDraft(ctx context.Context, request githubport.PullRequestRequest) (githubport.DispatchResult, error) {
	if code := connector.currentPullRequest(ctx, request); code != publicationdomain.CodeOK {
		return githubport.DispatchResult{Handoff: false, Code: code}, nil
	}
	arguments := []string{"pr", "ready", strconv.FormatInt(request.Number, 10), "--repo", "github.com/" + request.Owner + "/" + request.Name}
	if request.Draft {
		arguments = append(arguments, "--undo")
	}
	result := connector.runner.Run(ctx, arguments, nil)
	if !result.Started {
		return githubport.DispatchResult{Code: publicationdomain.CodeUnavailable}, nil
	}
	if result.ExitCode != 0 {
		return githubport.DispatchResult{Handoff: true, Code: responseCode(result)}, nil
	}
	return githubport.DispatchResult{Handoff: true, Code: publicationdomain.CodeOK}, nil
}

func integrationCode(code publicationdomain.ExternalCode) domainintegration.Code {
	switch code {
	case publicationdomain.CodeOK:
		return domainintegration.CodeOK
	case publicationdomain.CodeTLS:
		return domainintegration.CodeTLS
	case publicationdomain.CodeRateLimited:
		return domainintegration.CodeRateLimited
	case publicationdomain.CodeUnauthorized:
		return domainintegration.CodeUnauthorized
	case publicationdomain.CodeForbidden:
		return domainintegration.CodeForbidden
	case publicationdomain.CodeNotFound:
		return domainintegration.CodeNotFound
	case publicationdomain.CodeConflict:
		return domainintegration.CodeConflict
	case publicationdomain.CodeUnprocessable:
		return domainintegration.CodeUnprocessable
	case publicationdomain.CodeServer:
		return domainintegration.CodeServer
	default:
		return domainintegration.CodeUnavailable
	}
}

func integrationObservation(request githubport.IntegrationRequest, status domainintegration.ForgeStatus, code domainintegration.Code) domainintegration.ForgeObservation {
	binding := request.Binding
	return domainintegration.SealForgeObservation(domainintegration.ForgeObservation{
		ID: "github-integration-" + domainintegration.DigestText(strings.Join([]string{binding.SHA256,
			strconv.FormatInt(request.TaskStoreNowMillis, 10), string(status), string(code)}, "\x1f"))[:32],
		Status: status, Code: code, RepositoryID: binding.GitHubRepositoryID,
		RepositoryNodeID: binding.GitHubRepositoryNodeID, Owner: binding.RepositoryOwner, Name: binding.RepositoryName,
		ViewerLogin: binding.ViewerLogin, APIVersion: apiVersion, PullRequestNumber: binding.PullRequestNumber,
		PullRequestNodeID: binding.PullRequestNodeID, HeadSHA: binding.CandidateSHA, HeadRef: binding.Branch,
		BaseRef: strings.TrimPrefix(binding.BaseRef, "refs/heads/"), HeadRepositoryID: binding.GitHubRepositoryID,
		BaseRepositoryID: binding.GitHubRepositoryID, HeadOwner: binding.ViewerLogin, AuthorLogin: binding.ViewerLogin,
		MarkerSHA256: binding.MarkerSHA256, MarkerCount: 1,
		ObservedAtMillis: request.TaskStoreNowMillis, MaximumAgeMillis: domainintegration.MaximumObservationAgeMS,
	})
}

func mergeCapability(result Result) (bool, domainintegration.Code) {
	if !result.Started {
		return false, domainintegration.CodeUnavailable
	}
	if result.ExitCode != 0 || !utf8.Valid(result.Stdout) {
		return false, integrationCode(responseCode(result))
	}
	if !strings.Contains(string(result.Stdout), "--match-head-commit") {
		return false, domainintegration.CodeAtomicHeadUnavailable
	}
	return true, domainintegration.CodeOK
}

// ObserveIntegration reads the authenticated repository and exact pull
// request and independently verifies that the installed forge primitive can
// atomically reject a changed head.
func (connector *Connector) ObserveIntegration(ctx context.Context, request githubport.IntegrationRequest) (domainintegration.ForgeObservation, error) {
	if !domainintegration.ValidBinding(request.Binding) || request.TaskStoreNowMillis < 0 {
		return integrationObservation(request, domainintegration.ForgeInvalid, domainintegration.CodePullRequestMismatch), nil
	}
	binding := request.Binding
	user := connector.api(ctx, "GET", "user", nil)
	if user.code != publicationdomain.CodeOK {
		return integrationObservation(request, domainintegration.ForgeUnavailable, integrationCode(user.code)), nil
	}
	repository := connector.api(ctx, "GET", "repos/"+binding.RepositoryOwner+"/"+binding.RepositoryName, nil)
	if repository.code != publicationdomain.CodeOK {
		return integrationObservation(request, domainintegration.ForgeUnavailable, integrationCode(repository.code)), nil
	}
	pull := connector.api(ctx, "GET", fmt.Sprintf("repos/%s/%s/pulls/%d", binding.RepositoryOwner, binding.RepositoryName, binding.PullRequestNumber), nil)
	if pull.code != publicationdomain.CodeOK {
		return integrationObservation(request, domainintegration.ForgeUnavailable, integrationCode(pull.code)), nil
	}
	var viewer userResponse
	var repositoryValue repositoryResponse
	var pullValue pullResponse
	if json.Unmarshal(user.body, &viewer) != nil || json.Unmarshal(repository.body, &repositoryValue) != nil || json.Unmarshal(pull.body, &pullValue) != nil {
		return integrationObservation(request, domainintegration.ForgeInvalid, domainintegration.CodeResponseUnknown), nil
	}
	capable, capabilityCode := mergeCapability(connector.runner.Run(ctx, []string{"pr", "merge", "--help"}, nil))
	if !capable {
		status := domainintegration.ForgeInvalid
		if domainintegration.WaitingCode(capabilityCode) {
			status = domainintegration.ForgeUnavailable
		}
		return integrationObservation(request, status, capabilityCode), nil
	}
	value := integrationObservation(request, domainintegration.ForgeReady, domainintegration.CodeOK)
	value.RepositoryID, value.RepositoryNodeID = repositoryValue.ID, repositoryValue.NodeID
	value.Owner, value.Name, value.ViewerLogin = repositoryValue.Owner.Login, repositoryValue.Name, viewer.Login
	value.Authenticated, value.CanMerge, value.TLSVerified = viewer.Login != "", repositoryValue.Permissions.Pull && repositoryValue.Permissions.Push, true
	value.RateRemaining = min(user.rateRemaining, repository.rateRemaining, pull.rateRemaining)
	value.AtomicExpectedHead, value.DefaultBranch = true, repositoryValue.DefaultBranch
	value.PullRequestNumber, value.PullRequestNodeID, value.PullRequestState = pullValue.Number, pullValue.NodeID, pullValue.State
	value.Draft, value.HeadSHA, value.HeadRef, value.BaseRef = pullValue.Draft, pullValue.Head.SHA, pullValue.Head.Ref, pullValue.Base.Ref
	value.HeadRepositoryID, value.BaseRepositoryID = pullValue.Head.Repo.ID, pullValue.Base.Repo.ID
	value.HeadOwner, value.AuthorLogin = pullValue.Head.User.Login, pullValue.User.Login
	marker := fmt.Sprintf("<!-- director-publication/v1 task=%s run=%s branch=%s owner-sha256=%s -->", binding.TaskID, binding.RunID, binding.Branch, binding.OwnershipSHA256)
	value.MarkerCount = uint32(strings.Count(pullValue.Body, marker))
	if value.MarkerCount > 0 {
		value.MarkerSHA256 = publicationdomain.DigestText(marker)
	} else {
		value.MarkerSHA256 = ""
	}
	value.Mergeable = pullValue.Mergeable != nil && *pullValue.Mergeable
	value.MergeableState, value.Merged, value.MergeCommitSHA = pullValue.MergeableState, pullValue.Merged, pullValue.MergeCommitSHA
	if pullValue.Merged {
		mergedAt, err := timeParse(pullValue.MergedAt)
		if err != nil {
			return integrationObservation(request, domainintegration.ForgeInvalid, domainintegration.CodeResponseUnknown), nil
		}
		value.Status, value.PullRequestState, value.MergedAtMillis = domainintegration.ForgeIntegrated, "closed", mergedAt
	}
	sealed := domainintegration.SealForgeObservation(value)
	if !domainintegration.CurrentForgeObservation(sealed, binding, request.TaskStoreNowMillis) {
		code := domainintegration.CodePullRequestMismatch
		switch {
		case value.RepositoryID != binding.GitHubRepositoryID || value.RepositoryNodeID != binding.GitHubRepositoryNodeID:
			code = domainintegration.CodeRepositoryMismatch
		case value.HeadSHA != binding.CandidateSHA:
			code = domainintegration.CodeHeadChanged
		case value.BaseRef != strings.TrimPrefix(binding.BaseRef, "refs/heads/") || value.DefaultBranch != strings.TrimPrefix(binding.BaseRef, "refs/heads/"):
			code = domainintegration.CodeBaseChanged
		case !value.Mergeable || value.MergeableState != "clean":
			code = domainintegration.CodeMergeability
		}
		return integrationObservation(request, domainintegration.ForgeInvalid, code), nil
	}
	return sealed, nil
}

// MergeExpectedHead uses the exact GitHub CLI expected-head primitive. The
// adapter repeats the complete forge observation immediately before handoff.
func (connector *Connector) MergeExpectedHead(ctx context.Context, request githubport.MergeRequest) (githubport.MergeResult, error) {
	if request.Attempt == 0 || request.Attempt > domainintegration.MaximumAttempts ||
		!domainintegration.ValidBinding(request.Binding) || request.TaskStoreNowMillis < 0 ||
		!domainintegration.CurrentForgeObservation(request.ExpectedObservation, request.Binding, request.TaskStoreNowMillis) ||
		request.ExpectedObservation.Status != domainintegration.ForgeReady {
		return githubport.MergeResult{Code: domainintegration.CodeConflict}, nil
	}
	current, err := connector.ObserveIntegration(ctx, githubport.IntegrationRequest{Binding: request.Binding, TaskStoreNowMillis: request.TaskStoreNowMillis})
	if err != nil || current.FactSHA256 != request.ExpectedObservation.FactSHA256 {
		return githubport.MergeResult{Code: domainintegration.CodeConflict}, nil
	}
	arguments := []string{"pr", "merge", strconv.FormatInt(request.Binding.PullRequestNumber, 10), "--repo",
		"github.com/" + request.Binding.RepositoryOwner + "/" + request.Binding.RepositoryName, "--merge",
		"--match-head-commit", request.Binding.CandidateSHA}
	result := connector.runner.Run(ctx, arguments, nil)
	if !result.Started {
		return githubport.MergeResult{Code: domainintegration.CodeUnavailable}, nil
	}
	if result.ExitCode != 0 {
		return githubport.MergeResult{Handoff: true, Code: integrationCode(responseCode(result))}, nil
	}
	return githubport.MergeResult{Handoff: true, Code: domainintegration.CodeOK}, nil
}

func validationCode(code publicationdomain.ExternalCode) domainvalidation.Code {
	switch code {
	case publicationdomain.CodeOK:
		return domainvalidation.CodeOK
	case publicationdomain.CodeTLS:
		return domainvalidation.CodeTLS
	case publicationdomain.CodeRateLimited:
		return domainvalidation.CodeRateLimited
	case publicationdomain.CodeUnauthorized:
		return domainvalidation.CodeUnauthorized
	case publicationdomain.CodeForbidden:
		return domainvalidation.CodeForbidden
	case publicationdomain.CodeNotFound:
		return domainvalidation.CodeNotFound
	case publicationdomain.CodeServer:
		return domainvalidation.CodeServer
	case publicationdomain.CodeRepositoryMismatch:
		return domainvalidation.CodeRepositoryMismatch
	default:
		return domainvalidation.CodeUnavailable
	}
}

func (connector *Connector) ObserveChecksRepository(ctx context.Context, request githubport.ChecksRepositoryRequest) (domainvalidation.RepositoryObservation, error) {
	observation := domainvalidation.RepositoryObservation{ID: observationID("github-checks-repository", request.Owner, request.Name,
		strconv.FormatInt(request.TaskStoreNowMillis, 10)), Code: domainvalidation.CodeUnavailable, RepositoryID: request.RepositoryID,
		RepositoryNodeID: request.RepositoryNodeID, Owner: request.Owner, Name: request.Name, ViewerLogin: request.ExpectedViewer,
		APIVersion: apiVersion, ObservedAtMillis: request.TaskStoreNowMillis, MaximumAgeMillis: domainvalidation.MaximumObservationAgeMS}
	user := connector.api(ctx, "GET", "user", nil)
	if user.code != publicationdomain.CodeOK {
		observation.Code = validationCode(user.code)
		return domainvalidation.SealRepositoryObservation(observation), nil
	}
	repository := connector.api(ctx, "GET", "repos/"+request.Owner+"/"+request.Name, nil)
	if repository.code != publicationdomain.CodeOK {
		observation.Code = validationCode(repository.code)
		return domainvalidation.SealRepositoryObservation(observation), nil
	}
	var viewer userResponse
	var value repositoryResponse
	if json.Unmarshal(user.body, &viewer) != nil || json.Unmarshal(repository.body, &value) != nil {
		observation.Code = domainvalidation.CodeResponseUnknown
		return domainvalidation.SealRepositoryObservation(observation), nil
	}
	observation.Code, observation.RepositoryID, observation.RepositoryNodeID = domainvalidation.CodeOK, value.ID, value.NodeID
	observation.Owner, observation.Name, observation.ViewerLogin = value.Owner.Login, value.Name, viewer.Login
	observation.Authenticated, observation.CanReadChecks, observation.TLSVerified = viewer.Login != "", value.Permissions.Pull, true
	observation.Archived, observation.Disabled = value.Archived, value.Disabled
	observation.RateRemaining = min(user.rateRemaining, repository.rateRemaining)
	return domainvalidation.SealRepositoryObservation(observation), nil
}

type workflowRunsResponse struct {
	TotalCount   uint32 `json:"total_count"`
	WorkflowRuns []struct {
		ID             int64  `json:"id"`
		WorkflowID     int64  `json:"workflow_id"`
		Name           string `json:"name"`
		HeadSHA        string `json:"head_sha"`
		CheckSuiteID   int64  `json:"check_suite_id"`
		Status         string `json:"status"`
		Conclusion     string `json:"conclusion"`
		RunAttempt     uint32 `json:"run_attempt"`
		RunStartedAt   string `json:"run_started_at"`
		UpdatedAt      string `json:"updated_at"`
		HeadRepository struct {
			ID int64 `json:"id"`
		} `json:"head_repository"`
	} `json:"workflow_runs"`
}

func pageBoundary(total, count, page, size uint32) (uint32, bool, bool) {
	if page == 0 || size == 0 || count > size {
		return 0, false, false
	}
	consumed := (page-1)*size + count
	if consumed >= total {
		return 0, true, consumed == total
	}
	return page + 1, false, count == size
}

func parsedTime(value string) int64 {
	if value == "" {
		return 0
	}
	parsed, err := time.Parse(time.RFC3339, value)
	if err != nil {
		return -1
	}
	return parsed.UnixMilli()
}

func (connector *Connector) ListWorkflowRuns(ctx context.Context, request githubport.CandidatePageRequest) (domainvalidation.WorkflowPage, error) {
	page := domainvalidation.WorkflowPage{ID: observationID("github-workflow-runs", request.Owner, request.Name, request.CandidateSHA,
		strconv.FormatUint(uint64(request.Page), 10), strconv.FormatInt(request.TaskStoreNowMillis, 10)), Code: domainvalidation.CodeUnavailable,
		CandidateSHA: request.CandidateSHA, Page: request.Page, ObservedAtMillis: request.TaskStoreNowMillis,
		MaximumAgeMillis: domainvalidation.MaximumObservationAgeMS}
	endpoint := fmt.Sprintf("repos/%s/%s/actions/runs?head_sha=%s&per_page=%d&page=%d", request.Owner, request.Name,
		url.QueryEscape(request.CandidateSHA), request.PageSize, request.Page)
	response := connector.api(ctx, "GET", endpoint, nil)
	if response.code != publicationdomain.CodeOK {
		page.Code = validationCode(response.code)
		return domainvalidation.SealWorkflowPage(page), nil
	}
	var value workflowRunsResponse
	if json.Unmarshal(response.body, &value) != nil || len(value.WorkflowRuns) > int(request.PageSize) {
		page.Code = domainvalidation.CodeResponseUnknown
		return domainvalidation.SealWorkflowPage(page), nil
	}
	page.Code, page.TotalCount = domainvalidation.CodeOK, value.TotalCount
	for _, run := range value.WorkflowRuns {
		observed := domainvalidation.WorkflowRun{ID: run.ID, WorkflowID: run.WorkflowID, Name: run.Name,
			HeadSHA: run.HeadSHA, HeadRepositoryID: run.HeadRepository.ID, CheckSuiteID: run.CheckSuiteID, Status: run.Status,
			Conclusion: run.Conclusion, Attempt: run.RunAttempt, StartedAtMillis: parsedTime(run.RunStartedAt), UpdatedAtMillis: parsedTime(run.UpdatedAt)}
		if !domainvalidation.ValidWorkflowRun(observed) {
			page.Code, page.Runs = domainvalidation.CodeRedactionFailure, nil
			return domainvalidation.SealWorkflowPage(page), nil
		}
		page.Runs = append(page.Runs, observed)
	}
	next, complete, exact := pageBoundary(value.TotalCount, uint32(len(page.Runs)), request.Page, request.PageSize)
	if !exact {
		page.Code, page.Runs = domainvalidation.CodePaginationIncomplete, nil
		return domainvalidation.SealWorkflowPage(page), nil
	}
	page.NextPage, page.Complete = next, complete
	return domainvalidation.SealWorkflowPage(page), nil
}

type checkRunsResponse struct {
	TotalCount uint32 `json:"total_count"`
	CheckRuns  []struct {
		ID          int64  `json:"id"`
		Name        string `json:"name"`
		HeadSHA     string `json:"head_sha"`
		Status      string `json:"status"`
		Conclusion  string `json:"conclusion"`
		DetailsURL  string `json:"details_url"`
		StartedAt   string `json:"started_at"`
		CompletedAt string `json:"completed_at"`
		CheckSuite  struct {
			ID      int64  `json:"id"`
			HeadSHA string `json:"head_sha"`
		} `json:"check_suite"`
		App struct {
			ID   int64  `json:"id"`
			Slug string `json:"slug"`
		} `json:"app"`
	} `json:"check_runs"`
}

func (connector *Connector) ListCheckRuns(ctx context.Context, request githubport.CandidatePageRequest) (domainvalidation.CheckPage, error) {
	page := domainvalidation.CheckPage{ID: observationID("github-check-runs", request.Owner, request.Name, request.CandidateSHA,
		strconv.FormatUint(uint64(request.Page), 10), strconv.FormatInt(request.TaskStoreNowMillis, 10)), Code: domainvalidation.CodeUnavailable,
		CandidateSHA: request.CandidateSHA, Page: request.Page, ObservedAtMillis: request.TaskStoreNowMillis,
		MaximumAgeMillis: domainvalidation.MaximumObservationAgeMS}
	endpoint := fmt.Sprintf("repos/%s/%s/commits/%s/check-runs?filter=latest&per_page=%d&page=%d", request.Owner, request.Name,
		url.PathEscape(request.CandidateSHA), request.PageSize, request.Page)
	response := connector.api(ctx, "GET", endpoint, nil)
	if response.code != publicationdomain.CodeOK {
		page.Code = validationCode(response.code)
		return domainvalidation.SealCheckPage(page), nil
	}
	var value checkRunsResponse
	if json.Unmarshal(response.body, &value) != nil || len(value.CheckRuns) > int(request.PageSize) {
		page.Code = domainvalidation.CodeResponseUnknown
		return domainvalidation.SealCheckPage(page), nil
	}
	page.Code, page.TotalCount = domainvalidation.CodeOK, value.TotalCount
	for _, check := range value.CheckRuns {
		observed := domainvalidation.CheckRun{ID: check.ID, Name: check.Name, HeadSHA: check.HeadSHA,
			SuiteID: check.CheckSuite.ID, SuiteHeadSHA: check.CheckSuite.HeadSHA, AppID: check.App.ID, AppSlug: check.App.Slug,
			Status: check.Status, Conclusion: check.Conclusion, DetailsURLSHA256: publicationdomain.DigestText(check.DetailsURL),
			StartedAtMillis: parsedTime(check.StartedAt), CompletedAtMillis: parsedTime(check.CompletedAt)}
		if !domainvalidation.ValidCheckRun(observed) {
			page.Code, page.Checks = domainvalidation.CodeRedactionFailure, nil
			return domainvalidation.SealCheckPage(page), nil
		}
		page.Checks = append(page.Checks, observed)
	}
	next, complete, exact := pageBoundary(value.TotalCount, uint32(len(page.Checks)), request.Page, request.PageSize)
	if !exact {
		page.Code, page.Checks = domainvalidation.CodePaginationIncomplete, nil
		return domainvalidation.SealCheckPage(page), nil
	}
	page.NextPage, page.Complete = next, complete
	return domainvalidation.SealCheckPage(page), nil
}

type statusesResponse struct {
	State      string `json:"state"`
	TotalCount uint32 `json:"total_count"`
	Statuses   []struct {
		ID        int64  `json:"id"`
		Context   string `json:"context"`
		SHA       string `json:"sha"`
		State     string `json:"state"`
		TargetURL string `json:"target_url"`
		UpdatedAt string `json:"updated_at"`
		Creator   struct {
			ID    int64  `json:"id"`
			Login string `json:"login"`
		} `json:"creator"`
	} `json:"statuses"`
}

func (connector *Connector) ListCommitStatuses(ctx context.Context, request githubport.CandidatePageRequest) (domainvalidation.StatusPage, error) {
	page := domainvalidation.StatusPage{ID: observationID("github-commit-statuses", request.Owner, request.Name, request.CandidateSHA,
		strconv.FormatUint(uint64(request.Page), 10), strconv.FormatInt(request.TaskStoreNowMillis, 10)), Code: domainvalidation.CodeUnavailable,
		CandidateSHA: request.CandidateSHA, Page: request.Page, ObservedAtMillis: request.TaskStoreNowMillis,
		MaximumAgeMillis: domainvalidation.MaximumObservationAgeMS}
	endpoint := fmt.Sprintf("repos/%s/%s/commits/%s/status?per_page=%d&page=%d", request.Owner, request.Name,
		url.PathEscape(request.CandidateSHA), request.PageSize, request.Page)
	response := connector.api(ctx, "GET", endpoint, nil)
	if response.code != publicationdomain.CodeOK {
		page.Code = validationCode(response.code)
		return domainvalidation.SealStatusPage(page), nil
	}
	var value statusesResponse
	if json.Unmarshal(response.body, &value) != nil || len(value.Statuses) > int(request.PageSize) {
		page.Code = domainvalidation.CodeResponseUnknown
		return domainvalidation.SealStatusPage(page), nil
	}
	page.Code, page.TotalCount, page.CombinedState = domainvalidation.CodeOK, value.TotalCount, value.State
	if value.TotalCount == 0 {
		page.CombinedState = "checks_only_no_statuses"
	}
	for _, status := range value.Statuses {
		observed := domainvalidation.CommitStatus{ID: status.ID, Context: status.Context, SHA: status.SHA,
			State: status.State, CreatorID: status.Creator.ID, CreatorLogin: status.Creator.Login,
			TargetURLSHA256: publicationdomain.DigestText(status.TargetURL), UpdatedAtMillis: parsedTime(status.UpdatedAt)}
		if !domainvalidation.ValidCommitStatus(observed) {
			page.Code, page.Statuses, page.CombinedState, page.TotalCount = domainvalidation.CodeRedactionFailure, nil, "", 0
			return domainvalidation.SealStatusPage(page), nil
		}
		page.Statuses = append(page.Statuses, observed)
	}
	next, complete, exact := pageBoundary(value.TotalCount, uint32(len(page.Statuses)), request.Page, request.PageSize)
	if !exact {
		page.Code, page.Statuses = domainvalidation.CodePaginationIncomplete, nil
		return domainvalidation.SealStatusPage(page), nil
	}
	page.NextPage, page.Complete = next, complete
	return domainvalidation.SealStatusPage(page), nil
}
