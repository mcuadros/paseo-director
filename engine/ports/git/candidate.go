// SPDX-License-Identifier: Apache-2.0

// Package git defines the read-only, effect-specific Git Candidate port. The
// adapter observes exact facts; it cannot decide Candidate admission.
package git

import (
	"context"

	candidatedomain "github.com/mcuadros/director-engine/domain/candidate"
	"github.com/mcuadros/director-engine/domain/execution"
)

// CandidateRequest fixes every durable identity before an adapter reads Git.
// It contains no credential and gives the adapter no delivery mutation.
type CandidateRequest struct {
	Claim                   candidatedomain.Claim       `json:"claim"`
	Repository              execution.RepositoryBinding `json:"repository"`
	RepositoryBindingSHA256 string                      `json:"repositoryBindingSha256"`
	TaskStoreNowMillis      int64                       `json:"taskStoreNowMillis"`
}

// CandidateObserver performs only a bounded read of the fixed repository and
// returns a path/content-free normalized observation.
type CandidateObserver interface {
	ObserveCandidate(context.Context, CandidateRequest) (candidatedomain.Observation, error)
}
