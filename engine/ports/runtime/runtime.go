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
	Scope                     execution.Scope                `json:"scope"`
	Effect                    execution.Effect               `json:"effect"`
	LeaseBinding              execution.LeaseBinding         `json:"leaseBinding"`
	Repository                execution.RepositoryBinding    `json:"repository"`
	SourcePath                string                         `json:"sourcePath"`
	WorktreePath              string                         `json:"worktreePath"`
	Branch                    string                         `json:"branch"`
	BaseSHA                   string                         `json:"baseSha"`
	WorktreeID                string                         `json:"worktreeId,omitempty"`
	BindingHash               string                         `json:"bindingHash"`
	LifecycleSurfaces         execution.LifecycleSurfaces    `json:"lifecycleSurfaces"`
	LifecycleApproval         *execution.LifecycleApproval   `json:"lifecycleApproval,omitempty"`
	LifecycleDigest           string                         `json:"lifecycleDigest"`
	Isolation                 execution.IsolationObservation `json:"isolation"`
	IsolationDigest           string                         `json:"isolationDigest,omitempty"`
	RecoveryWorktreePaths     []string                       `json:"recoveryWorktreePaths,omitempty"`
	ControlRecoveryArtifactID string                         `json:"controlRecoveryArtifactId,omitempty"`
	ControlCleanupAuthorized  bool                           `json:"controlCleanupAuthorized"`
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

// PrimaryRecoveryRequest fixes the complete durable Run binding for one
// read-only recovery observation. CandidateSHA is empty before admission.
type PrimaryRecoveryRequest struct {
	Scope           execution.Scope             `json:"scope"`
	LeaseBinding    execution.LeaseBinding      `json:"leaseBinding"`
	Repository      execution.RepositoryBinding `json:"repository"`
	BindingHash     string                      `json:"bindingHash"`
	WorktreeID      string                      `json:"worktreeId"`
	CandidateID     string                      `json:"candidateId,omitempty"`
	CandidateSHA    string                      `json:"candidateSha,omitempty"`
	OriginalAgentID string                      `json:"originalAgentId"`
}

// PrimaryRecoveryPort observes repository, worktree, process, and Candidate
// facts. It performs no containment, replacement, or cleanup effect.
type PrimaryRecoveryPort interface {
	ObservePrimaryRecovery(context.Context, PrimaryRecoveryRequest) (execution.PrimaryRuntimeRecoveryObservation, error)
}

// HelperRequest binds a helper-only runtime operation to its immutable Run,
// parent, repository, checkout mode, and ADR-0014 facts. Implementations do
// not decide admission or retry.
type HelperRequest struct {
	Scope                     execution.Scope                `json:"scope"`
	Helper                    execution.Helper               `json:"helper"`
	Effect                    execution.Effect               `json:"effect"`
	LeaseBinding              execution.LeaseBinding         `json:"leaseBinding"`
	Repository                execution.RepositoryBinding    `json:"repository"`
	BindingHash               string                         `json:"bindingHash"`
	PrimaryWorktreePath       string                         `json:"primaryWorktreePath"`
	LifecycleSurfaces         execution.LifecycleSurfaces    `json:"lifecycleSurfaces"`
	LifecycleApproval         *execution.LifecycleApproval   `json:"lifecycleApproval,omitempty"`
	LifecycleDigest           string                         `json:"lifecycleDigest"`
	Isolation                 execution.IsolationObservation `json:"isolation"`
	OperationalPolicy         execution.OperationalPolicy    `json:"operationalPolicy"`
	ControlRecoveryArtifactID string                         `json:"controlRecoveryArtifactId,omitempty"`
	ControlCleanupAuthorized  bool                           `json:"controlCleanupAuthorized"`
}

// HelperPort is the runtime-only companion for controlled helpers. The
// parent Task Agent performs native helper creation; this port prepares and
// observes isolated checkouts/boundaries and imports a verified commit object.
type HelperPort interface {
	ObserveHelperCapacity(context.Context, execution.Scope, execution.HelperPolicy) (execution.HelperCapacityObservation, error)
	ReserveHelperCapacity(context.Context, HelperCapacityReservation) (HelperCapacityReservationResult, error)
	ReleaseHelperCapacity(context.Context, string, execution.Scope) error
	ObserveHelperBoundary(context.Context, HelperRequest) (execution.HelperBoundaryObservation, error)
	ObserveHelperEffect(context.Context, HelperRequest) (execution.EffectObservation, error)
	DispatchHelperEffect(context.Context, HelperRequest) error
	ObserveHelperContribution(context.Context, HelperRequest) (execution.HelperContributionObservation, error)
}

// HelperCapacityReservation is a store-only compare request. The engine has
// already decided availability; the adapter only atomically compares the
// exact observation and records the idempotent reservation.
type HelperCapacityReservation struct {
	ID          string                              `json:"id"`
	Scope       execution.Scope                     `json:"scope"`
	Policy      execution.HelperPolicy              `json:"policy"`
	Observation execution.HelperCapacityObservation `json:"observation"`
}

type HelperCapacityReservationResult struct {
	Applied bool `json:"applied"`
	Replay  bool `json:"replay"`
}

// Port contains only effect-specific observations and dispatches.
type Port interface {
	ObserveEffect(context.Context, Request) (execution.EffectObservation, error)
	DispatchEffect(context.Context, Request) error
	ObserveOperational(context.Context, execution.Scope, execution.OperationalPolicy) (execution.OperationalObservation, error)
	ObserveCandidate(context.Context, CandidateRequest) (execution.CandidateObservation, error)
}
