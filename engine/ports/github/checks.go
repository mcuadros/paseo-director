// SPDX-License-Identifier: Apache-2.0

package github

import (
	"context"

	domainvalidation "github.com/mcuadros/director-engine/domain/validation"
)

type ChecksRepositoryRequest struct {
	Owner              string `json:"owner"`
	Name               string `json:"name"`
	RepositoryID       int64  `json:"repositoryId"`
	RepositoryNodeID   string `json:"repositoryNodeId"`
	ExpectedViewer     string `json:"expectedViewer"`
	TaskStoreNowMillis int64  `json:"taskStoreNowMillis"`
}

type CandidatePageRequest struct {
	Owner              string `json:"owner"`
	Name               string `json:"name"`
	RepositoryID       int64  `json:"repositoryId"`
	CandidateSHA       string `json:"candidateSha"`
	Page               uint32 `json:"page"`
	PageSize           uint32 `json:"pageSize"`
	TaskStoreNowMillis int64  `json:"taskStoreNowMillis"`
}

// ChecksPort is read-only. GitHub Actions starts from the already-authorized
// exact branch push; observing it cannot trigger, retry, cancel, or waive CI.
type ChecksPort interface {
	ObserveChecksRepository(context.Context, ChecksRepositoryRequest) (domainvalidation.RepositoryObservation, error)
	ListWorkflowRuns(context.Context, CandidatePageRequest) (domainvalidation.WorkflowPage, error)
	ListCheckRuns(context.Context, CandidatePageRequest) (domainvalidation.CheckPage, error)
	ListCommitStatuses(context.Context, CandidatePageRequest) (domainvalidation.StatusPage, error)
}
