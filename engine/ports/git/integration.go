// SPDX-License-Identifier: Apache-2.0

package git

import (
	"context"

	"github.com/mcuadros/director-engine/domain/candidate"
	"github.com/mcuadros/director-engine/domain/execution"
	domainintegration "github.com/mcuadros/director-engine/domain/integration"
)

type IntegrationRequest struct {
	Binding                 domainintegration.Binding   `json:"binding"`
	CandidateClaim          candidate.Claim             `json:"candidateClaim"`
	CandidateManifest       candidate.Manifest          `json:"candidateManifest"`
	Repository              execution.RepositoryBinding `json:"repository"`
	RepositoryBindingSHA256 string                      `json:"repositoryBindingSha256"`
	MergeCommitSHA          string                      `json:"mergeCommitSha,omitempty"`
	TaskStoreNowMillis      int64                       `json:"taskStoreNowMillis"`
}

// IntegrationPort observes exact live Task/base/default refs and verifies the
// post-merge commit parents/tree. It has no merge, push, retry, or cleanup
// operation.
type IntegrationPort interface {
	ObserveIntegration(context.Context, IntegrationRequest) (domainintegration.GitObservation, error)
}
