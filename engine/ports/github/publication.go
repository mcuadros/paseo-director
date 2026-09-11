// SPDX-License-Identifier: Apache-2.0

// Package github defines the narrow forge publication port. The connector
// returns normalized facts or performs one exact engine-authorized operation;
// it owns no delivery, retry, adoption, readiness, or fallback decision.
package github

import (
	"context"

	publicationdomain "github.com/mcuadros/director-engine/domain/publication"
)

type RepositoryRequest struct {
	Owner              string `json:"owner"`
	Name               string `json:"name"`
	RepositoryID       int64  `json:"repositoryId"`
	RepositoryNodeID   string `json:"repositoryNodeId"`
	ExpectedViewer     string `json:"expectedViewer"`
	TaskStoreNowMillis int64  `json:"taskStoreNowMillis"`
}

type ListRequest struct {
	Owner              string `json:"owner"`
	Name               string `json:"name"`
	HeadOwner          string `json:"headOwner"`
	HeadRef            string `json:"headRef"`
	Page               uint32 `json:"page"`
	PageSize           uint32 `json:"pageSize"`
	TaskStoreNowMillis int64  `json:"taskStoreNowMillis"`
	Marker             string `json:"marker"`
}

type PullRequestRequest struct {
	Owner              string `json:"owner"`
	Name               string `json:"name"`
	Number             int64  `json:"number"`
	ExpectedHeadSHA    string `json:"expectedHeadSha"`
	ExpectedBaseRef    string `json:"expectedBaseRef"`
	ExpectedUpdatedAt  int64  `json:"expectedUpdatedAt"`
	Title              string `json:"title,omitempty"`
	Body               string `json:"body,omitempty"`
	Draft              bool   `json:"draft"`
	TaskStoreNowMillis int64  `json:"taskStoreNowMillis"`
}

type CreateRequest struct {
	Owner              string `json:"owner"`
	Name               string `json:"name"`
	HeadOwner          string `json:"headOwner"`
	HeadRef            string `json:"headRef"`
	BaseRef            string `json:"baseRef"`
	ExpectedHeadSHA    string `json:"expectedHeadSha"`
	Title              string `json:"title"`
	Body               string `json:"body"`
	Draft              bool   `json:"draft"`
	TaskStoreNowMillis int64  `json:"taskStoreNowMillis"`
}

// DispatchResult classifies only the transport handoff boundary. Success is
// never evidence; the engine must observe GitHub after every possible handoff.
type DispatchResult struct {
	Handoff bool                           `json:"handoff"`
	Code    publicationdomain.ExternalCode `json:"code"`
}

type Port interface {
	ObserveRepository(context.Context, RepositoryRequest) (publicationdomain.RepositoryObservation, error)
	ListPullRequests(context.Context, ListRequest) (publicationdomain.PullRequestPage, error)
	CreatePullRequest(context.Context, CreateRequest) (DispatchResult, error)
	UpdatePullRequest(context.Context, PullRequestRequest) (DispatchResult, error)
	SetPullRequestDraft(context.Context, PullRequestRequest) (DispatchResult, error)
}
