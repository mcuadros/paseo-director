// SPDX-License-Identifier: Apache-2.0

// Package cleanup defines thin exact-effect ports for Git/filesystem cleanup.
// The application service owns sequencing, authority, retry, and lifecycle
// state; an adapter observes or performs exactly one requested effect.
package cleanup

import (
	"context"
	"errors"

	"github.com/mcuadros/director-engine/domain/candidate"
	domaincleanup "github.com/mcuadros/director-engine/domain/cleanup"
	"github.com/mcuadros/director-engine/domain/execution"
)

type Target struct {
	Binding                 domaincleanup.Binding           `json:"binding"`
	Policy                  domaincleanup.Policy            `json:"policy"`
	Repository              execution.RepositoryBinding     `json:"repository"`
	RepositoryBindingSHA256 string                          `json:"repositoryBindingSha256"`
	CandidateClaim          candidate.Claim                 `json:"candidateClaim"`
	CandidateManifest       candidate.Manifest              `json:"candidateManifest"`
	EffectID                string                          `json:"effectId"`
	EffectKind              domaincleanup.ResourceKind      `json:"effectKind"`
	Attempt                 uint32                          `json:"attempt"`
	Snapshot                *domaincleanup.SnapshotEvidence `json:"snapshot,omitempty"`
	PrivateArtifactRoot     string                          `json:"-"`
	TaskStoreNowMillis      int64                           `json:"taskStoreNowMillis"`
}

type DispatchCommand struct {
	Target              Target                          `json:"target"`
	Attempt             uint32                          `json:"attempt"`
	ExpectedObservation domaincleanup.Observation       `json:"expectedObservation"`
	Snapshot            *domaincleanup.SnapshotEvidence `json:"snapshot,omitempty"`
}

type DispatchResult struct {
	Handoff bool               `json:"handoff"`
	Code    domaincleanup.Code `json:"code"`
}

type DispatchFailure string

const (
	FailureBeforeHandoff   DispatchFailure = "before_handoff"
	FailurePossibleHandoff DispatchFailure = "possible_handoff"
)

type DispatchError struct {
	Failure DispatchFailure
}

func (failure *DispatchError) Error() string { return "cleanup effect dispatch failed" }
func (failure *DispatchError) PossibleHandoff() bool {
	return failure.Failure == FailurePossibleHandoff
}

var ErrInvalidTarget = errors.New("cleanup target is invalid")

type GitPort interface {
	Observe(context.Context, Target) (domaincleanup.Observation, error)
	CreateSnapshot(context.Context, DispatchCommand) (DispatchResult, error)
	RemoveWorktree(context.Context, DispatchCommand) (DispatchResult, error)
	DeleteRemoteRef(context.Context, DispatchCommand) (DispatchResult, error)
	DeleteLocalRef(context.Context, DispatchCommand) (DispatchResult, error)
	ExpirePrivateArtifact(context.Context, DispatchCommand) (DispatchResult, error)
	ExpireRecoveryRefs(context.Context, DispatchCommand) (DispatchResult, error)
}
