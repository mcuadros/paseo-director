// SPDX-License-Identifier: Apache-2.0

package fake

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"sync"

	feedbackdomain "github.com/mcuadros/director-engine/domain/feedback"
	domainintegration "github.com/mcuadros/director-engine/domain/integration"
	publicationdomain "github.com/mcuadros/director-engine/domain/publication"
	domainvalidation "github.com/mcuadros/director-engine/domain/validation"
	githubport "github.com/mcuadros/director-engine/ports/github"
)

// GitHub is a deterministic in-memory implementation of the thin forge port.
// It intentionally performs no adoption or retry decision.
type GitHub struct {
	mu                 sync.Mutex
	RepositoryID       int64
	RepositoryNodeID   string
	Owner              string
	Name               string
	Viewer             string
	RepositoryCode     publicationdomain.ExternalCode
	ChecksCode         domainvalidation.Code
	Pulls              []publicationdomain.PullRequest
	WorkflowRuns       []domainvalidation.WorkflowRun
	CheckRuns          []domainvalidation.CheckRun
	CommitStatuses     []domainvalidation.CommitStatus
	CombinedStatus     string
	CreateDispatches   uint64
	UpdateDispatches   uint64
	DraftDispatches    uint64
	MergeDispatches    uint64
	IntegrationCode    domainintegration.Code
	Mergeable          bool
	MergeableState     string
	AtomicExpectedHead bool
	DefaultBranch      string
	NextMergeCommitSHA string
	MergedPulls        map[int64]domainintegration.ForgeObservation
	LoseMergeResponse  bool
	Feedback           map[feedbackdomain.Source][]feedbackdomain.Item
	FeedbackCode       string
	FeedbackReads      map[feedbackdomain.Source]uint64
	nextNumber         int64
}

var _ githubport.Port = (*GitHub)(nil)
var _ githubport.ChecksPort = (*GitHub)(nil)
var _ githubport.FeedbackPort = (*GitHub)(nil)
var _ githubport.FeedbackObservationPort = (*GitHub)(nil)
var _ githubport.IntegrationPort = (*GitHub)(nil)

func NewGitHub(repositoryID int64, nodeID, owner, name, viewer string) *GitHub {
	return &GitHub{RepositoryID: repositoryID, RepositoryNodeID: nodeID, Owner: owner, Name: name,
		Viewer: viewer, RepositoryCode: publicationdomain.CodeOK, ChecksCode: domainvalidation.CodeOK,
		CombinedStatus: "checks_only_no_statuses", Feedback: map[feedbackdomain.Source][]feedbackdomain.Item{},
		FeedbackCode: "ok", FeedbackReads: map[feedbackdomain.Source]uint64{}, nextNumber: 1,
		IntegrationCode: domainintegration.CodeOK, Mergeable: true, MergeableState: "clean", AtomicExpectedHead: true,
		DefaultBranch: "main", MergedPulls: map[int64]domainintegration.ForgeObservation{}}
}

func fakeIntegrationObservation(request githubport.IntegrationRequest, status domainintegration.ForgeStatus, code domainintegration.Code) domainintegration.ForgeObservation {
	binding := request.Binding
	return domainintegration.SealForgeObservation(domainintegration.ForgeObservation{ID: fmt.Sprintf("fake-integration-%d", request.TaskStoreNowMillis),
		Status: status, Code: code, RepositoryID: binding.GitHubRepositoryID, RepositoryNodeID: binding.GitHubRepositoryNodeID,
		Owner: binding.RepositoryOwner, Name: binding.RepositoryName, ViewerLogin: binding.ViewerLogin,
		APIVersion: "2022-11-28", PullRequestNumber: binding.PullRequestNumber, PullRequestNodeID: binding.PullRequestNodeID,
		HeadSHA: binding.CandidateSHA, HeadRef: binding.Branch, BaseRef: strings.TrimPrefix(binding.BaseRef, "refs/heads/"),
		HeadRepositoryID: binding.GitHubRepositoryID, BaseRepositoryID: binding.GitHubRepositoryID,
		HeadOwner: binding.ViewerLogin, AuthorLogin: binding.ViewerLogin, MarkerSHA256: binding.MarkerSHA256, MarkerCount: 1,
		ObservedAtMillis: request.TaskStoreNowMillis, MaximumAgeMillis: domainintegration.MaximumObservationAgeMS})
}

func (forge *GitHub) ObserveIntegration(_ context.Context, request githubport.IntegrationRequest) (domainintegration.ForgeObservation, error) {
	forge.mu.Lock()
	defer forge.mu.Unlock()
	code := forge.IntegrationCode
	if code == "" {
		code = domainintegration.CodeOK
	}
	if code != domainintegration.CodeOK {
		status := domainintegration.ForgeInvalid
		if domainintegration.WaitingCode(code) {
			status = domainintegration.ForgeUnavailable
		}
		return fakeIntegrationObservation(request, status, code), nil
	}
	if merged, ok := forge.MergedPulls[request.Binding.PullRequestNumber]; ok {
		merged.ObservedAtMillis = request.TaskStoreNowMillis
		merged.MaximumAgeMillis = domainintegration.MaximumObservationAgeMS
		return domainintegration.SealForgeObservation(merged), nil
	}
	for _, pull := range forge.Pulls {
		if pull.Number != request.Binding.PullRequestNumber {
			continue
		}
		value := fakeIntegrationObservation(request, domainintegration.ForgeReady, domainintegration.CodeOK)
		value.RepositoryID, value.RepositoryNodeID, value.Owner, value.Name, value.ViewerLogin = forge.RepositoryID, forge.RepositoryNodeID, forge.Owner, forge.Name, forge.Viewer
		value.Authenticated, value.CanMerge, value.TLSVerified, value.RateRemaining = true, true, true, 5_000
		value.AtomicExpectedHead, value.DefaultBranch = forge.AtomicExpectedHead, forge.DefaultBranch
		value.PullRequestNodeID, value.PullRequestState, value.Draft = pull.NodeID, pull.State, pull.Draft
		value.HeadSHA, value.HeadRef, value.BaseRef = pull.HeadSHA, pull.HeadRef, pull.BaseRef
		value.HeadRepositoryID, value.BaseRepositoryID, value.HeadOwner, value.AuthorLogin = pull.HeadRepositoryID, pull.BaseRepositoryID, pull.HeadOwner, pull.AuthorLogin
		value.MarkerSHA256, value.MarkerCount = pull.MarkerSHA256, pull.MarkerCount
		value.Mergeable, value.MergeableState = forge.Mergeable, forge.MergeableState
		sealed := domainintegration.SealForgeObservation(value)
		if !domainintegration.CurrentForgeObservation(sealed, request.Binding, request.TaskStoreNowMillis) {
			if !forge.AtomicExpectedHead {
				return fakeIntegrationObservation(request, domainintegration.ForgeInvalid, domainintegration.CodeAtomicHeadUnavailable), nil
			}
			return fakeIntegrationObservation(request, domainintegration.ForgeInvalid, domainintegration.CodePullRequestMismatch), nil
		}
		return sealed, nil
	}
	return fakeIntegrationObservation(request, domainintegration.ForgeInvalid, domainintegration.CodeNotFound), nil
}

func (forge *GitHub) MergeExpectedHead(_ context.Context, request githubport.MergeRequest) (githubport.MergeResult, error) {
	forge.mu.Lock()
	defer forge.mu.Unlock()
	if !domainintegration.CurrentForgeObservation(request.ExpectedObservation, request.Binding, request.TaskStoreNowMillis) ||
		request.ExpectedObservation.Status != domainintegration.ForgeReady || !forge.AtomicExpectedHead {
		return githubport.MergeResult{Code: domainintegration.CodeConflict}, nil
	}
	if _, merged := forge.MergedPulls[request.Binding.PullRequestNumber]; merged {
		return githubport.MergeResult{Handoff: true, Code: domainintegration.CodeConflict}, nil
	}
	for index := range forge.Pulls {
		pull := &forge.Pulls[index]
		if pull.Number != request.Binding.PullRequestNumber {
			continue
		}
		if pull.State != "open" || pull.Draft || pull.HeadSHA != request.Binding.CandidateSHA ||
			pull.BaseRef != strings.TrimPrefix(request.Binding.BaseRef, "refs/heads/") {
			return githubport.MergeResult{Code: domainintegration.CodeConflict}, nil
		}
		forge.MergeDispatches++
		mergeSHA := forge.NextMergeCommitSHA
		if mergeSHA == "" {
			mergeSHA = strings.Repeat("d", len(request.Binding.CandidateSHA))
		}
		pull.State = "closed"
		value := fakeIntegrationObservation(githubport.IntegrationRequest{Binding: request.Binding, TaskStoreNowMillis: request.TaskStoreNowMillis}, domainintegration.ForgeIntegrated, domainintegration.CodeOK)
		value.RepositoryID, value.RepositoryNodeID, value.Owner, value.Name, value.ViewerLogin = forge.RepositoryID, forge.RepositoryNodeID, forge.Owner, forge.Name, forge.Viewer
		value.Authenticated, value.CanMerge, value.TLSVerified, value.RateRemaining = true, true, true, 5_000
		value.AtomicExpectedHead, value.DefaultBranch = true, forge.DefaultBranch
		value.PullRequestState, value.Draft, value.HeadSHA, value.HeadRef, value.BaseRef = "closed", false, pull.HeadSHA, pull.HeadRef, pull.BaseRef
		value.HeadRepositoryID, value.BaseRepositoryID, value.HeadOwner, value.AuthorLogin = pull.HeadRepositoryID, pull.BaseRepositoryID, pull.HeadOwner, pull.AuthorLogin
		value.MarkerSHA256, value.MarkerCount = pull.MarkerSHA256, pull.MarkerCount
		value.Merged, value.MergedAtMillis, value.MergeCommitSHA = true, request.TaskStoreNowMillis+1, mergeSHA
		forge.MergedPulls[pull.Number] = domainintegration.SealForgeObservation(value)
		if forge.LoseMergeResponse {
			forge.LoseMergeResponse = false
			return githubport.MergeResult{Handoff: true, Code: domainintegration.CodeResponseUnknown}, nil
		}
		return githubport.MergeResult{Handoff: true, Code: domainintegration.CodeOK}, nil
	}
	return githubport.MergeResult{Code: domainintegration.CodeNotFound}, nil
}

func (forge *GitHub) ObserveFeedbackPage(_ context.Context, request githubport.FeedbackRequest) (githubport.FeedbackPage, error) {
	forge.mu.Lock()
	defer forge.mu.Unlock()
	forge.FeedbackReads[request.Source]++
	page := githubport.FeedbackPage{ID: fmt.Sprintf("fake-feedback-%s-%d-%d", request.Source, request.Page, request.TaskStoreNowMillis),
		Code: forge.FeedbackCode, Source: request.Source, RepositoryID: forge.RepositoryID, RepositoryNodeID: forge.RepositoryNodeID,
		PullRequestNumber: request.PullRequestNumber, CandidateSHA: request.CandidateSHA, BaseSHA: request.BaseSHA,
		BindingSHA256: request.BindingSHA256, Page: request.Page, ObservedAtMillis: request.TaskStoreNowMillis,
		MaximumAgeMillis: feedbackdomain.MaximumObservationAge}
	if page.Code == "" {
		page.Code = "ok"
	}
	if page.Code != "ok" {
		return githubport.SealFeedbackPage(page), nil
	}
	values := forge.Feedback[request.Source]
	start := int((request.Page - 1) * request.PageSize)
	end := min(start+int(request.PageSize), len(values))
	if start < len(values) {
		page.Items = slices.Clone(values[start:end])
	}
	if end < len(values) {
		page.NextPage = request.Page + 1
	} else {
		page.Complete = true
	}
	return githubport.SealFeedbackPage(page), nil
}

func (forge *GitHub) ObserveRepository(_ context.Context, request githubport.RepositoryRequest) (publicationdomain.RepositoryObservation, error) {
	forge.mu.Lock()
	defer forge.mu.Unlock()
	code := forge.RepositoryCode
	if code == "" {
		code = publicationdomain.CodeOK
	}
	value := publicationdomain.RepositoryObservation{ID: fmt.Sprintf("fake-repository-%d", request.TaskStoreNowMillis), Code: code,
		RepositoryID: forge.RepositoryID, RepositoryNodeID: forge.RepositoryNodeID, Owner: forge.Owner, Name: forge.Name,
		ViewerLogin: forge.Viewer, Authenticated: code == publicationdomain.CodeOK, CanPush: code == publicationdomain.CodeOK,
		CanPullRequests: code == publicationdomain.CodeOK, TLSVerified: code != publicationdomain.CodeTLS,
		APIVersion: "2022-11-28", RateRemaining: 5_000, RateResetAtMillis: request.TaskStoreNowMillis + 60_000,
		ObservedAtMillis: request.TaskStoreNowMillis, MaximumAgeMillis: publicationdomain.MaximumObservationAgeMS}
	if code == publicationdomain.CodeRateLimited {
		value.RateRemaining = 0
	}
	return publicationdomain.SealRepositoryObservation(value), nil
}

func (forge *GitHub) ListPullRequests(_ context.Context, request githubport.ListRequest) (publicationdomain.PullRequestPage, error) {
	forge.mu.Lock()
	defer forge.mu.Unlock()
	matching := make([]publicationdomain.PullRequest, 0)
	for _, pull := range forge.Pulls {
		if pull.HeadRef == request.HeadRef && strings.EqualFold(pull.HeadOwner, request.HeadOwner) {
			matching = append(matching, pull)
		}
	}
	start := int((request.Page - 1) * request.PageSize)
	end := min(start+int(request.PageSize), len(matching))
	page := publicationdomain.PullRequestPage{ID: fmt.Sprintf("fake-pulls-%d-%d", request.Page, request.TaskStoreNowMillis),
		Code: publicationdomain.CodeOK, Page: request.Page, ObservedAtMillis: request.TaskStoreNowMillis,
		MaximumAgeMillis: publicationdomain.MaximumObservationAgeMS}
	if start < len(matching) {
		page.PullRequests = slices.Clone(matching[start:end])
	}
	if end < len(matching) {
		page.NextPage = request.Page + 1
	} else {
		page.Complete = true
	}
	return publicationdomain.SealPullRequestPage(page), nil
}

func (forge *GitHub) CreatePullRequest(_ context.Context, request githubport.CreateRequest) (githubport.DispatchResult, error) {
	forge.mu.Lock()
	defer forge.mu.Unlock()
	forge.CreateDispatches++
	number := forge.nextNumber
	forge.nextNumber++
	marker := markerFromBody(request.Body)
	forge.Pulls = append(forge.Pulls, publicationdomain.PullRequest{Number: number, NodeID: fmt.Sprintf("PR_node_%d", number),
		URL: fmt.Sprintf("https://github.com/%s/%s/pull/%d", request.Owner, request.Name, number), State: "open", Draft: request.Draft,
		HeadSHA: request.ExpectedHeadSHA, HeadRef: request.HeadRef, BaseRef: request.BaseRef, HeadOwner: request.HeadOwner,
		HeadRepositoryID: forge.RepositoryID, BaseRepositoryID: forge.RepositoryID, AuthorLogin: forge.Viewer,
		CreatedByViewer: true, MarkerSHA256: publicationdomain.DigestText(marker), MarkerCount: uint32(strings.Count(request.Body, marker)),
		TitleSHA256: publicationdomain.DigestText(request.Title), BodySHA256: publicationdomain.DigestText(request.Body),
		UpdatedAtMillis: request.TaskStoreNowMillis})
	return githubport.DispatchResult{Handoff: true, Code: publicationdomain.CodeOK}, nil
}

func markerFromBody(body string) string {
	start := strings.LastIndex(body, "<!-- director-publication/v1 ")
	if start < 0 {
		return "missing-marker"
	}
	end := strings.Index(body[start:], "-->")
	if end < 0 {
		return "missing-marker"
	}
	return body[start : start+end+3]
}

func (forge *GitHub) UpdatePullRequest(_ context.Context, request githubport.PullRequestRequest) (githubport.DispatchResult, error) {
	forge.mu.Lock()
	defer forge.mu.Unlock()
	forge.UpdateDispatches++
	for index := range forge.Pulls {
		pull := &forge.Pulls[index]
		if pull.Number != request.Number {
			continue
		}
		if pull.State != "open" || pull.HeadSHA != request.ExpectedHeadSHA || pull.BaseRef != request.ExpectedBaseRef ||
			pull.UpdatedAtMillis != request.ExpectedUpdatedAt {
			return githubport.DispatchResult{Handoff: true, Code: publicationdomain.CodeConflict}, nil
		}
		marker := markerFromBody(request.Body)
		pull.TitleSHA256, pull.BodySHA256 = publicationdomain.DigestText(request.Title), publicationdomain.DigestText(request.Body)
		pull.MarkerSHA256, pull.MarkerCount = publicationdomain.DigestText(marker), uint32(strings.Count(request.Body, marker))
		pull.UpdatedAtMillis = request.TaskStoreNowMillis
		return githubport.DispatchResult{Handoff: true, Code: publicationdomain.CodeOK}, nil
	}
	return githubport.DispatchResult{Handoff: true, Code: publicationdomain.CodeNotFound}, nil
}

func (forge *GitHub) SetPullRequestDraft(_ context.Context, request githubport.PullRequestRequest) (githubport.DispatchResult, error) {
	forge.mu.Lock()
	defer forge.mu.Unlock()
	forge.DraftDispatches++
	for index := range forge.Pulls {
		pull := &forge.Pulls[index]
		if pull.Number != request.Number {
			continue
		}
		if pull.State != "open" || pull.HeadSHA != request.ExpectedHeadSHA || pull.BaseRef != request.ExpectedBaseRef ||
			pull.UpdatedAtMillis != request.ExpectedUpdatedAt {
			return githubport.DispatchResult{Handoff: true, Code: publicationdomain.CodeConflict}, nil
		}
		pull.Draft, pull.UpdatedAtMillis = request.Draft, request.TaskStoreNowMillis
		return githubport.DispatchResult{Handoff: true, Code: publicationdomain.CodeOK}, nil
	}
	return githubport.DispatchResult{Handoff: true, Code: publicationdomain.CodeNotFound}, nil
}

func (forge *GitHub) Snapshot() ([]publicationdomain.PullRequest, uint64, uint64, uint64) {
	forge.mu.Lock()
	defer forge.mu.Unlock()
	return slices.Clone(forge.Pulls), forge.CreateDispatches, forge.UpdateDispatches, forge.DraftDispatches
}

func (forge *GitHub) ReplacePulls(pulls []publicationdomain.PullRequest) {
	forge.mu.Lock()
	defer forge.mu.Unlock()
	forge.Pulls = slices.Clone(pulls)
	for _, pull := range pulls {
		forge.nextNumber = max(forge.nextNumber, pull.Number+1)
	}
}

func (forge *GitHub) ObserveChecksRepository(_ context.Context, request githubport.ChecksRepositoryRequest) (domainvalidation.RepositoryObservation, error) {
	forge.mu.Lock()
	defer forge.mu.Unlock()
	code := forge.ChecksCode
	if code == "" {
		code = domainvalidation.CodeOK
	}
	return domainvalidation.SealRepositoryObservation(domainvalidation.RepositoryObservation{
		ID: fmt.Sprintf("fake-checks-repository-%d", request.TaskStoreNowMillis), Code: code,
		RepositoryID: forge.RepositoryID, RepositoryNodeID: forge.RepositoryNodeID, Owner: forge.Owner, Name: forge.Name,
		ViewerLogin: forge.Viewer, Authenticated: code == domainvalidation.CodeOK, CanReadChecks: code == domainvalidation.CodeOK,
		TLSVerified: code != domainvalidation.CodeTLS, APIVersion: "2022-11-28", RateRemaining: 5_000,
		ObservedAtMillis: request.TaskStoreNowMillis, MaximumAgeMillis: domainvalidation.MaximumObservationAgeMS,
	}), nil
}

func pageRange(length int, request githubport.CandidatePageRequest) (int, int) {
	start := int((request.Page - 1) * request.PageSize)
	if start > length {
		start = length
	}
	return start, min(start+int(request.PageSize), length)
}

func pageEnd(length, end int, page uint32) (uint32, bool) {
	if end < length {
		return page + 1, false
	}
	return 0, true
}

func (forge *GitHub) ListWorkflowRuns(_ context.Context, request githubport.CandidatePageRequest) (domainvalidation.WorkflowPage, error) {
	forge.mu.Lock()
	defer forge.mu.Unlock()
	start, end := pageRange(len(forge.WorkflowRuns), request)
	next, complete := pageEnd(len(forge.WorkflowRuns), end, request.Page)
	return domainvalidation.SealWorkflowPage(domainvalidation.WorkflowPage{ID: fmt.Sprintf("fake-workflows-%d-%d", request.Page, request.TaskStoreNowMillis),
		Code: domainvalidation.CodeOK, CandidateSHA: request.CandidateSHA, Page: request.Page, TotalCount: uint32(len(forge.WorkflowRuns)),
		NextPage: next, Complete: complete, Runs: slices.Clone(forge.WorkflowRuns[start:end]), ObservedAtMillis: request.TaskStoreNowMillis,
		MaximumAgeMillis: domainvalidation.MaximumObservationAgeMS}), nil
}

func (forge *GitHub) ListCheckRuns(_ context.Context, request githubport.CandidatePageRequest) (domainvalidation.CheckPage, error) {
	forge.mu.Lock()
	defer forge.mu.Unlock()
	start, end := pageRange(len(forge.CheckRuns), request)
	next, complete := pageEnd(len(forge.CheckRuns), end, request.Page)
	return domainvalidation.SealCheckPage(domainvalidation.CheckPage{ID: fmt.Sprintf("fake-checks-%d-%d", request.Page, request.TaskStoreNowMillis),
		Code: domainvalidation.CodeOK, CandidateSHA: request.CandidateSHA, Page: request.Page, TotalCount: uint32(len(forge.CheckRuns)),
		NextPage: next, Complete: complete, Checks: slices.Clone(forge.CheckRuns[start:end]), ObservedAtMillis: request.TaskStoreNowMillis,
		MaximumAgeMillis: domainvalidation.MaximumObservationAgeMS}), nil
}

func (forge *GitHub) ListCommitStatuses(_ context.Context, request githubport.CandidatePageRequest) (domainvalidation.StatusPage, error) {
	forge.mu.Lock()
	defer forge.mu.Unlock()
	start, end := pageRange(len(forge.CommitStatuses), request)
	next, complete := pageEnd(len(forge.CommitStatuses), end, request.Page)
	rollup := forge.CombinedStatus
	if len(forge.CommitStatuses) == 0 {
		rollup = "checks_only_no_statuses"
	}
	return domainvalidation.SealStatusPage(domainvalidation.StatusPage{ID: fmt.Sprintf("fake-statuses-%d-%d", request.Page, request.TaskStoreNowMillis),
		Code: domainvalidation.CodeOK, CandidateSHA: request.CandidateSHA, CombinedState: rollup, Page: request.Page,
		TotalCount: uint32(len(forge.CommitStatuses)), NextPage: next, Complete: complete,
		Statuses: slices.Clone(forge.CommitStatuses[start:end]), ObservedAtMillis: request.TaskStoreNowMillis,
		MaximumAgeMillis: domainvalidation.MaximumObservationAgeMS}), nil
}
