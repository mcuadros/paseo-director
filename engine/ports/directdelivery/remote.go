// SPDX-License-Identifier: Apache-2.0

// Package directdelivery defines the exact Git remote-ref observation and
// conditional-push boundary. The port exposes no delivery-mode, fallback,
// retry, integration, or cleanup decision.
package directdelivery

import (
	"context"
	"errors"

	"github.com/mcuadros/director-engine/domain/candidate"
	directdomain "github.com/mcuadros/director-engine/domain/directdelivery"
	"github.com/mcuadros/director-engine/domain/execution"
)

type Target struct {
	Binding                 directdomain.Binding        `json:"binding"`
	CandidateClaim          candidate.Claim             `json:"candidateClaim"`
	CandidateManifest       candidate.Manifest          `json:"candidateManifest"`
	Repository              execution.RepositoryBinding `json:"repository"`
	RepositoryBindingSHA256 string                      `json:"repositoryBindingSha256"`
	EffectID                string                      `json:"effectId"`
	Attempt                 uint32                      `json:"attempt"`
	TaskStoreNowMillis      int64                       `json:"taskStoreNowMillis"`
}

type PushCommand struct {
	Target              Target                   `json:"target"`
	Attempt             uint32                   `json:"attempt"`
	ExpectedObservation directdomain.Observation `json:"expectedObservation"`
}

type FailureCode string

const (
	FailurePreconditionChanged FailureCode = "DIRECT_PRECONDITION_CHANGED"
	FailureProcessUnavailable  FailureCode = "DIRECT_PROCESS_UNAVAILABLE"
	FailureResultUnknown       FailureCode = "DIRECT_RESULT_UNKNOWN"
)

// DispatchError contains only a closed code and the handoff classification.
// Raw Git output, paths, remotes, refs, and credentials cannot enter it.
type DispatchError struct {
	Code            FailureCode
	PossibleHandoff bool
}

func (failure *DispatchError) Error() string { return string(failure.Code) }

func HandoffPossible(err error) bool {
	var failure *DispatchError
	return errors.As(err, &failure) && failure.PossibleHandoff
}

type Port interface {
	Observe(context.Context, Target) (directdomain.Observation, error)
	Push(context.Context, PushCommand) error
}
