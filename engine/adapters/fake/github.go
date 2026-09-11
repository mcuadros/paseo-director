// SPDX-License-Identifier: Apache-2.0

package fake

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"sync"

	publicationdomain "github.com/mcuadros/director-engine/domain/publication"
	githubport "github.com/mcuadros/director-engine/ports/github"
)

// GitHub is a deterministic in-memory implementation of the thin forge port.
// It intentionally performs no adoption or retry decision.
type GitHub struct {
	mu               sync.Mutex
	RepositoryID     int64
	RepositoryNodeID string
	Owner            string
	Name             string
	Viewer           string
	RepositoryCode   publicationdomain.ExternalCode
	Pulls            []publicationdomain.PullRequest
	CreateDispatches uint64
	UpdateDispatches uint64
	DraftDispatches  uint64
	nextNumber       int64
}

var _ githubport.Port = (*GitHub)(nil)

func NewGitHub(repositoryID int64, nodeID, owner, name, viewer string) *GitHub {
	return &GitHub{RepositoryID: repositoryID, RepositoryNodeID: nodeID, Owner: owner, Name: name,
		Viewer: viewer, RepositoryCode: publicationdomain.CodeOK, nextNumber: 1}
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
