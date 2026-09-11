// SPDX-License-Identifier: Apache-2.0

// Package cleanup owns the pure decision which advances one exact cleanup
// effect. Adapters supply observations and perform one admitted operation; they
// never select cleanup policy, order, retry, or escalation.
package cleanup

import domaincleanup "github.com/mcuadros/director-engine/domain/cleanup"

const SchemaVersion = "director.reducer.cleanup/v1"

type DecisionKind string

const (
	DecisionObserve      DecisionKind = "observe"
	DecisionDispatch     DecisionKind = "dispatch"
	DecisionAdopt        DecisionKind = "adopt"
	DecisionComplete     DecisionKind = "complete"
	DecisionRetain       DecisionKind = "retain"
	DecisionWaitExternal DecisionKind = "wait_external"
	DecisionEscalate     DecisionKind = "escalate"
)

type Facts struct {
	SchemaVersion       string              `json:"schemaVersion"`
	State               domaincleanup.State `json:"state"`
	ProjectActive       bool                `json:"projectActive"`
	LeaseCurrent        bool                `json:"leaseCurrent"`
	CandidateCurrent    bool                `json:"candidateCurrent"`
	IntegrationVerified bool                `json:"integrationVerified"`
	DiskPressure        bool                `json:"diskPressure"`
	NowMillis           int64               `json:"nowMillis"`
}

type Decision struct {
	SchemaVersion     string                     `json:"schemaVersion"`
	Kind              DecisionKind               `json:"kind"`
	EffectKind        domaincleanup.ResourceKind `json:"effectKind,omitempty"`
	Code              domaincleanup.Code         `json:"code,omitempty"`
	CleanupAuthorized bool                       `json:"cleanupAuthorized"`
}

func decision(kind DecisionKind, effect domaincleanup.ResourceKind, code domaincleanup.Code, authorized bool) Decision {
	return Decision{SchemaVersion: SchemaVersion, Kind: kind, EffectKind: effect, Code: code, CleanupAuthorized: authorized}
}

func escalate(effect domaincleanup.ResourceKind, code domaincleanup.Code) Decision {
	return decision(DecisionEscalate, effect, code, false)
}

func effectDecision(state domaincleanup.State, effect domaincleanup.Effect) Decision {
	observation := effect.Observation
	if observation == nil {
		return decision(DecisionObserve, effect.Kind, "", state.CleanupAuthorized)
	}
	if observation.Status == domaincleanup.StatusUnavailable {
		// A later reconciler refreshes an unavailable observation. The caller
		// owns backoff; retaining this fact forever would strand the effect.
		return decision(DecisionObserve, effect.Kind, domaincleanup.CodeExternalUnavailable, false)
	}
	if observation.Status == domaincleanup.StatusDifferent || observation.Status == domaincleanup.StatusAmbiguous {
		return escalate(effect.Kind, observation.Code)
	}

	switch effect.Kind {
	case domaincleanup.ResourceReviewerAgent, domaincleanup.ResourceTaskAgent,
		domaincleanup.ResourceReviewerWorkspace, domaincleanup.ResourceTaskWorkspace:
		switch observation.Status {
		case domaincleanup.StatusTerminated, domaincleanup.StatusAbsent:
			return decision(DecisionAdopt, effect.Kind, "", state.CleanupAuthorized)
		case domaincleanup.StatusExactPresent:
			if effect.Phase == domaincleanup.EffectIntent ||
				effect.Phase == domaincleanup.EffectObservationRequired && observation.PriorDispatcherAbsent && effect.Attempt < effect.AttemptLimit {
				return decision(DecisionDispatch, effect.Kind, "", state.CleanupAuthorized)
			}
			return escalate(effect.Kind, domaincleanup.CodeResponseUnknown)
		default:
			return escalate(effect.Kind, domaincleanup.CodeLifecycleMismatch)
		}

	case domaincleanup.ResourceSnapshot:
		if observation.Status == domaincleanup.StatusVerified && observation.Snapshot != nil {
			return decision(DecisionAdopt, effect.Kind, "", true)
		}
		if state.Trigger == domaincleanup.TriggerIntegrated && state.LifecycleState == domaincleanup.LifecycleReclaimed &&
			state.Binding.ReclaimedEvidenceSHA256 != "" && observation.Status == domaincleanup.StatusAbsent {
			return decision(DecisionAdopt, effect.Kind, "", true)
		}
		if state.Trigger == domaincleanup.TriggerIntegrated && observation.Status == domaincleanup.StatusClean && !observation.Ignored {
			return decision(DecisionAdopt, effect.Kind, "", true)
		}
		if observation.Status != domaincleanup.StatusClean && observation.Status != domaincleanup.StatusDirty &&
			observation.Status != domaincleanup.StatusExactPresent {
			return escalate(effect.Kind, domaincleanup.CodeSnapshotUnverified)
		}
		if observation.Code != domaincleanup.CodeOK {
			return escalate(effect.Kind, observation.Code)
		}
		if effect.Phase == domaincleanup.EffectIntent ||
			effect.Phase == domaincleanup.EffectObservationRequired && observation.PriorDispatcherAbsent && effect.Attempt < effect.AttemptLimit {
			return decision(DecisionDispatch, effect.Kind, "", false)
		}
		return escalate(effect.Kind, domaincleanup.CodeResponseUnknown)

	case domaincleanup.ResourceRemoteRef, domaincleanup.ResourceLocalRef:
		if observation.Status == domaincleanup.StatusAbsent {
			return decision(DecisionAdopt, effect.Kind, "", state.CleanupAuthorized)
		}
		if observation.Status != domaincleanup.StatusExactPresent {
			return escalate(effect.Kind, observation.Code)
		}
		if observation.CurrentOID != state.Binding.CandidateSHA {
			return escalate(effect.Kind, domaincleanup.CodeRefChanged)
		}
		if !state.CleanupAuthorized {
			return escalate(effect.Kind, domaincleanup.CodeSnapshotUnverified)
		}
		if effect.Phase == domaincleanup.EffectIntent {
			return decision(DecisionDispatch, effect.Kind, "", true)
		}
		if (effect.Phase == domaincleanup.EffectDispatching || effect.Phase == domaincleanup.EffectObservationRequired) &&
			effect.Attempt < effect.AttemptLimit {
			// Exact Task refs are the narrow destructive recovery case with an
			// atomic expected-OID delete. The adapter re-observes the same fact
			// immediately before applying that unchanged compare guard.
			return decision(DecisionDispatch, effect.Kind, "", true)
		}
		if effect.Attempt >= effect.AttemptLimit {
			return escalate(effect.Kind, domaincleanup.CodeAttemptsExhausted)
		}
		return escalate(effect.Kind, domaincleanup.CodeResponseUnknown)

	case domaincleanup.ResourceWorktree, domaincleanup.ResourcePrivateArtifact, domaincleanup.ResourceRecoveryRef:
		if observation.Status == domaincleanup.StatusAbsent {
			return decision(DecisionAdopt, effect.Kind, "", state.CleanupAuthorized)
		}
		if observation.Status != domaincleanup.StatusExactPresent {
			return escalate(effect.Kind, observation.Code)
		}
		if !state.CleanupAuthorized {
			return escalate(effect.Kind, domaincleanup.CodeSnapshotUnverified)
		}
		// Targets without an atomic exact-value delete remain ambiguous after
		// any possible handoff, even when their identity appears unchanged.
		if effect.Phase != domaincleanup.EffectIntent {
			return escalate(effect.Kind, domaincleanup.CodeResponseUnknown)
		}
		return decision(DecisionDispatch, effect.Kind, "", true)
	default:
		return escalate(effect.Kind, domaincleanup.CodeBindingChanged)
	}
}

// Reduce applies immutable authority before any observation. Disk pressure
// may stop new launches elsewhere, but it never manufactures permission to
// remove unintegrated work.
func Reduce(facts Facts) Decision {
	if facts.SchemaVersion != SchemaVersion || !domaincleanup.ValidState(facts.State) || facts.NowMillis < 0 {
		return escalate("", domaincleanup.CodeBindingChanged)
	}
	state := facts.State
	if state.Phase == domaincleanup.PhaseNeedsYou {
		return escalate("", domaincleanup.Code(state.NeedsYouCode))
	}
	if state.Phase == domaincleanup.PhaseComplete {
		return decision(DecisionComplete, "", "", true)
	}
	if state.Phase == domaincleanup.PhaseRetained {
		return decision(DecisionRetain, "", "", false)
	}
	if !facts.ProjectActive || !facts.LeaseCurrent {
		return decision(DecisionWaitExternal, "", domaincleanup.CodeExternalUnavailable, false)
	}
	if !facts.CandidateCurrent {
		return escalate("", domaincleanup.CodeBindingChanged)
	}
	if state.Trigger == domaincleanup.TriggerIntegrated && !facts.IntegrationVerified {
		return escalate("", domaincleanup.CodeIntegrationUnverified)
	}
	if facts.DiskPressure && state.Trigger != domaincleanup.TriggerIntegrated {
		return escalate("", domaincleanup.CodeDiskPressure)
	}
	for _, effect := range state.Effects {
		if effect.Phase != domaincleanup.EffectComplete {
			return effectDecision(state, effect)
		}
	}
	if state.Trigger != domaincleanup.TriggerIntegrated && state.Policy.CancellationMode == domaincleanup.CancellationRetain {
		return decision(DecisionRetain, "", "", false)
	}
	if !state.CleanupAuthorized {
		return escalate("", domaincleanup.CodeSnapshotUnverified)
	}
	return decision(DecisionComplete, "", "", true)
}
