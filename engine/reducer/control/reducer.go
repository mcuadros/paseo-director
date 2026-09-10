// SPDX-License-Identifier: Apache-2.0

// Package control contains the pure execution-control reducer. It chooses one
// durable transition, observation, or authorized effect; connectors only
// translate the resulting host command.
package control

import (
	"slices"
	"strings"

	"github.com/mcuadros/director-engine/domain/execution"
)

const SchemaVersion = "director.reducer.control/v1"

type DecisionKind string

const (
	DecisionWaitSafeBoundary     DecisionKind = "wait_safe_boundary"
	DecisionCreateBoundaryIntent DecisionKind = "create_boundary_intent"
	DecisionObserveBoundary      DecisionKind = "observe_boundary"
	DecisionAdoptBoundary        DecisionKind = "adopt_boundary"
	DecisionMarkPaused           DecisionKind = "mark_paused"
	DecisionReconcileResume      DecisionKind = "reconcile_resume"
	DecisionCompleteResume       DecisionKind = "complete_resume"
	DecisionCreateArchiveIntent  DecisionKind = "create_archive_intent"
	DecisionObserveArchive       DecisionKind = "observe_archive"
	DecisionDispatchArchive      DecisionKind = "dispatch_archive"
	DecisionAdoptArchive         DecisionKind = "adopt_archive"
	DecisionCreateRecoveryIntent DecisionKind = "create_recovery_intent"
	DecisionObserveRecovery      DecisionKind = "observe_recovery"
	DecisionDispatchRecovery     DecisionKind = "dispatch_recovery"
	DecisionAdoptRecovery        DecisionKind = "adopt_recovery"
	DecisionPreserveRetained     DecisionKind = "preserve_retained"
	DecisionCreateHostArchive    DecisionKind = "create_host_archive"
	DecisionDriveHostArchive     DecisionKind = "drive_host_archive"
	DecisionCreateWorktreeRemove DecisionKind = "create_worktree_remove"
	DecisionDriveWorktreeRemove  DecisionKind = "drive_worktree_remove"
	DecisionMarkCancelled        DecisionKind = "mark_cancelled"
	DecisionComplete             DecisionKind = "complete"
	DecisionWaitExternal         DecisionKind = "wait_external"
	DecisionEscalate             DecisionKind = "escalate"
)

type Facts struct {
	SchemaVersion       string
	ProjectState        string
	ProjectGeneration   uint64
	Control             execution.RunControl
	TrackedAgentsSHA256 string
	ResumeReconciled    bool
	HostViewArchive     execution.Effect
	WorktreeRemove      execution.Effect
}

type Decision struct {
	Kind        DecisionKind
	TargetIndex int
	EffectKind  execution.EffectKind
	ReasonCode  string
}

const (
	ReasonPausedSafeBoundary       = "project_paused_at_safe_boundary"
	ReasonResumeReconciliation     = "project_resume_reconciled"
	ReasonTaskCancelled            = "task_cancelled_recovery_preserved"
	ReasonEmergencyStopped         = "emergency_stop_recovery_preserved"
	ReasonControlRecoveryAmbiguous = "control_recovery_ambiguous"
	ReasonControlExternalWait      = "control_external_observation_unavailable"
)

func roleOrder(role execution.ControlledAgentRole) int {
	switch role {
	case execution.ControlledHelper:
		return 0
	case execution.ControlledReviewer:
		return 1
	case execution.ControlledTaskAgent:
		return 2
	default:
		return 3
	}
}

func orderedTargets(values []execution.ControlledAgent) []int {
	indices := make([]int, len(values))
	for index := range values {
		indices[index] = index
	}
	slices.SortFunc(indices, func(left, right int) int {
		leftRole, rightRole := roleOrder(values[left].Identity.Role), roleOrder(values[right].Identity.Role)
		if leftRole != rightRole {
			return leftRole - rightRole
		}
		return strings.Compare(values[left].Identity.ID, values[right].Identity.ID)
	})
	return indices
}

func effectDecision(effect execution.Effect, observe, dispatch, adopt DecisionKind, index int) Decision {
	if effect.Observation == nil {
		return Decision{Kind: observe, TargetIndex: index, EffectKind: effect.Kind}
	}
	switch effect.Observation.Status {
	case execution.ObservationDesired:
		return Decision{Kind: adopt, TargetIndex: index, EffectKind: effect.Kind}
	case execution.ObservationAbsent, execution.ObservationOwnedPresent:
		if effect.Phase == execution.EffectIntentRecorded ||
			effect.Phase == execution.EffectDispatching && effect.Observation.PriorDispatcherAbsent && effect.Attempt < effect.AttemptLimit {
			return Decision{Kind: dispatch, TargetIndex: index, EffectKind: effect.Kind}
		}
		return Decision{Kind: DecisionEscalate, TargetIndex: index, EffectKind: effect.Kind, ReasonCode: ReasonControlRecoveryAmbiguous}
	case execution.ObservationUnavailable:
		return Decision{Kind: DecisionWaitExternal, TargetIndex: index, EffectKind: effect.Kind, ReasonCode: ReasonControlExternalWait}
	default:
		return Decision{Kind: DecisionEscalate, TargetIndex: index, EffectKind: effect.Kind, ReasonCode: ReasonControlRecoveryAmbiguous}
	}
}

func pauseDecision(control execution.RunControl) Decision {
	for _, index := range orderedTargets(control.Targets) {
		target := control.Targets[index]
		if target.Archived && target.ProcessAbsent {
			continue
		}
		if target.Boundary.ID == "" {
			return Decision{Kind: DecisionCreateBoundaryIntent, TargetIndex: index, EffectKind: execution.EffectControlAgentBoundary}
		}
		if target.Boundary.Phase == execution.EffectComplete {
			continue
		}
		if target.Boundary.Observation == nil {
			return Decision{Kind: DecisionObserveBoundary, TargetIndex: index, EffectKind: execution.EffectControlAgentBoundary}
		}
		switch target.Boundary.Observation.Status {
		case execution.ObservationDesired, execution.ObservationErrored, execution.ObservationPermission:
			return Decision{Kind: DecisionAdoptBoundary, TargetIndex: index, EffectKind: execution.EffectControlAgentBoundary}
		case execution.ObservationOwnedPresent:
			return Decision{Kind: DecisionWaitSafeBoundary, TargetIndex: index, EffectKind: execution.EffectControlAgentBoundary, ReasonCode: ReasonPausedSafeBoundary}
		case execution.ObservationUnavailable:
			return Decision{Kind: DecisionWaitExternal, TargetIndex: index, EffectKind: execution.EffectControlAgentBoundary, ReasonCode: ReasonControlExternalWait}
		default:
			return Decision{Kind: DecisionEscalate, TargetIndex: index, EffectKind: execution.EffectControlAgentBoundary, ReasonCode: ReasonControlRecoveryAmbiguous}
		}
	}
	return Decision{Kind: DecisionMarkPaused, ReasonCode: ReasonPausedSafeBoundary}
}

func containmentDecision(facts Facts) Decision {
	for _, index := range orderedTargets(facts.Control.Targets) {
		target := facts.Control.Targets[index]
		if target.Archived && target.ProcessAbsent {
			continue
		}
		if target.Archive.ID == "" {
			return Decision{Kind: DecisionCreateArchiveIntent, TargetIndex: index, EffectKind: execution.EffectControlAgentArchive}
		}
		if target.Archive.Phase == execution.EffectComplete {
			if !target.Archived || !target.ProcessAbsent {
				return Decision{Kind: DecisionEscalate, TargetIndex: index, ReasonCode: ReasonControlRecoveryAmbiguous}
			}
			continue
		}
		return effectDecision(target.Archive, DecisionObserveArchive, DecisionDispatchArchive, DecisionAdoptArchive, index)
	}
	recovery := facts.Control.Recovery
	if !recovery.Preserved {
		if recovery.Mode == execution.RecoveryRetain {
			return Decision{Kind: DecisionPreserveRetained}
		}
		if recovery.Mode != execution.RecoverySnapshotThenDelete {
			return Decision{Kind: DecisionEscalate, ReasonCode: ReasonControlRecoveryAmbiguous}
		}
		if recovery.Snapshot.ID == "" {
			return Decision{Kind: DecisionCreateRecoveryIntent, EffectKind: execution.EffectRecoverySnapshot}
		}
		if recovery.Snapshot.Phase != execution.EffectComplete {
			return effectDecision(recovery.Snapshot, DecisionObserveRecovery, DecisionDispatchRecovery, DecisionAdoptRecovery, -1)
		}
		return Decision{Kind: DecisionAdoptRecovery, EffectKind: execution.EffectRecoverySnapshot, TargetIndex: -1}
	}
	if facts.Control.PostPreservationNeedCode != "" {
		return Decision{Kind: DecisionEscalate, ReasonCode: string(facts.Control.PostPreservationNeedCode)}
	}
	if recovery.Mode == execution.RecoverySnapshotThenDelete {
		if facts.HostViewArchive.ID == "" {
			return Decision{Kind: DecisionCreateHostArchive, EffectKind: execution.EffectHostViewArchive}
		}
		if facts.HostViewArchive.Phase != execution.EffectComplete {
			return Decision{Kind: DecisionDriveHostArchive, EffectKind: execution.EffectHostViewArchive}
		}
		if facts.WorktreeRemove.ID == "" {
			return Decision{Kind: DecisionCreateWorktreeRemove, EffectKind: execution.EffectWorktreeRemove}
		}
		if facts.WorktreeRemove.Phase != execution.EffectComplete {
			return Decision{Kind: DecisionDriveWorktreeRemove, EffectKind: execution.EffectWorktreeRemove}
		}
	}
	return Decision{Kind: DecisionMarkCancelled}
}

// Reduce applies the control before ordinary budgets/profile/provider gates.
// Lease/current-scope fencing is supplied by the application layer before the
// reducer is called, matching the execution oracle's observe-only precedence.
func Reduce(facts Facts) Decision {
	if facts.SchemaVersion != SchemaVersion || facts.ProjectGeneration == 0 ||
		facts.Control.SchemaVersion != execution.RunControlSchemaVersion || !execution.ValidControlIntent(facts.Control.Intent) ||
		facts.Control.ProjectGeneration != facts.ProjectGeneration ||
		(len(facts.Control.Targets) > 0 && facts.TrackedAgentsSHA256 != facts.Control.TargetSetSHA256) {
		return Decision{Kind: DecisionEscalate, ReasonCode: ReasonControlRecoveryAmbiguous}
	}
	if facts.Control.UnresolvedAgents {
		return Decision{Kind: DecisionEscalate, ReasonCode: ReasonControlRecoveryAmbiguous}
	}
	switch facts.Control.Intent.Kind {
	case execution.ControlPauseProject:
		if facts.Control.Phase == execution.ControlPaused || facts.Control.Phase == execution.ControlComplete {
			return Decision{Kind: DecisionComplete, ReasonCode: ReasonPausedSafeBoundary}
		}
		return pauseDecision(facts.Control)
	case execution.ControlResumeProject:
		if facts.Control.Phase == execution.ControlComplete {
			return Decision{Kind: DecisionComplete, ReasonCode: ReasonResumeReconciliation}
		}
		if !facts.ResumeReconciled {
			return Decision{Kind: DecisionReconcileResume, ReasonCode: ReasonResumeReconciliation}
		}
		return Decision{Kind: DecisionCompleteResume, ReasonCode: ReasonResumeReconciliation}
	case execution.ControlCancelTask, execution.ControlEmergencyStop:
		if facts.Control.Phase == execution.ControlCancelled || facts.Control.Phase == execution.ControlComplete {
			return Decision{Kind: DecisionComplete}
		}
		return containmentDecision(facts)
	default:
		return Decision{Kind: DecisionEscalate, ReasonCode: ReasonControlRecoveryAmbiguous}
	}
}
