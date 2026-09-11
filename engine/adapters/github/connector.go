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
	publicationdomain "github.com/mcuadros/director-engine/domain/publication"
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
var _ githubport.FeedbackPort = (*Connector)(nil)
var _ githubport.FeedbackObservationPort = (*Connector)(nil)

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
	ID       int64  `json:"id"`
	NodeID   string `json:"node_id"`
	Name     string `json:"name"`
	Archived bool   `json:"archived"`
	Disabled bool   `json:"disabled"`
	Owner    struct {
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
	Number    int64  `json:"number"`
	NodeID    string `json:"node_id"`
	State     string `json:"state"`
	Draft     bool   `json:"draft"`
	Title     string `json:"title"`
	Body      string `json:"body"`
	HTMLURL   string `json:"html_url"`
	UpdatedAt string `json:"updated_at"`
	User      struct {
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
