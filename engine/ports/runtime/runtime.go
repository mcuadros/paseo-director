// SPDX-License-Identifier: Apache-2.0

// Package runtime defines typed engine ports for the owned Git worktree,
// rootless-OCI fixture, operational observations, and exact Candidate facts.
// Implementations perform or observe one requested operation and own no policy.
package runtime

import (
	"context"

	"github.com/mcuadros/director-engine/domain/execution"
)

// Request binds one effect to its exact source, worktree, branch, and Run.
type Request struct {
	Scope             execution.Scope                `json:"scope"`
	Effect            execution.Effect               `json:"effect"`
	LeaseBinding      execution.LeaseBinding         `json:"leaseBinding"`
	Repository        execution.RepositoryBinding    `json:"repository"`
	SourcePath        string                         `json:"sourcePath"`
	WorktreePath      string                         `json:"worktreePath"`
	Branch            string                         `json:"branch"`
	BaseSHA           string                         `json:"baseSha"`
	WorktreeID        string                         `json:"worktreeId,omitempty"`
	BindingHash       string                         `json:"bindingHash"`
	LifecycleSurfaces execution.LifecycleSurfaces    `json:"lifecycleSurfaces"`
	LifecycleApproval *execution.LifecycleApproval   `json:"lifecycleApproval,omitempty"`
	LifecycleDigest   string                         `json:"lifecycleDigest"`
	Isolation         execution.IsolationObservation `json:"isolation"`
	IsolationDigest   string                         `json:"isolationDigest,omitempty"`
}

// CandidateRequest asks for external Git facts bound to one immutable claim.
type CandidateRequest struct {
	Scope        execution.Scope          `json:"scope"`
	SourcePath   string                   `json:"sourcePath"`
	WorktreePath string                   `json:"worktreePath"`
	Branch       string                   `json:"branch"`
	WorktreeID   string                   `json:"worktreeId"`
	BindingHash  string                   `json:"bindingHash"`
	Claim        execution.CompletedClaim `json:"claim"`
}

// Port contains only effect-specific observations and dispatches.
type Port interface {
	ObserveEffect(context.Context, Request) (execution.EffectObservation, error)
	DispatchEffect(context.Context, Request) error
	ObserveOperational(context.Context, execution.Scope, execution.OperationalPolicy) (execution.OperationalObservation, error)
	ObserveCandidate(context.Context, CandidateRequest) (execution.CandidateObservation, error)
}
