// SPDX-License-Identifier: Apache-2.0

package execution

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"slices"
)

func evidenceDigest(value any) string {
	encoded, err := json.Marshal(value)
	if err != nil {
		panic("marshal fixed execution evidence: " + err.Error())
	}
	digest := sha256.Sum256(encoded)
	return hex.EncodeToString(digest[:])
}

// EffectObservationHash returns the canonical identity of a normalized effect
// observation with its self-hash field cleared.
func EffectObservationHash(observation EffectObservation) string {
	observation.FactHash = ""
	return evidenceDigest(observation)
}

// ValidEffectObservation rejects an adapter-supplied hash which does not bind
// the complete normalized observation.
func ValidEffectObservation(observation EffectObservation) bool {
	if observation.FactHash == "" || observation.FactHash != EffectObservationHash(observation) {
		return false
	}
	if observation.NativeAgent != nil && !ValidNativeAgentRecoveryFact(*observation.NativeAgent) {
		return false
	}
	return observation.Inventory == nil || ValidPrimaryRecoveryInventory(*observation.Inventory)
}

// ValidNativeAgentRecoveryFact rejects raw or unbounded host material. The
// exact failure signal list is canonical so its hash is stable across hosts.
func ValidNativeAgentRecoveryFact(fact NativeAgentRecoveryFact) bool {
	if fact.AgentID == "" || fact.WorkspaceID == "" || fact.EffectID == "" ||
		!slices.Contains([]ControlledAgentRole{ControlledTaskAgent, ControlledReviewer, ControlledHelper}, fact.Role) ||
		!slices.Contains([]string{"idle", "running", "initializing", "closed", "error"}, fact.Status) ||
		len(fact.FailureSignals) == 0 || len(fact.FailureSignals) > 4 || !slices.IsSorted(fact.FailureSignals) {
		return false
	}
	seen := make(map[ProviderFailureSignal]struct{}, len(fact.FailureSignals))
	for _, signal := range fact.FailureSignals {
		if !validFailureSignal(signal) {
			return false
		}
		if _, duplicate := seen[signal]; duplicate {
			return false
		}
		seen[signal] = struct{}{}
	}
	return true
}

// ValidPrimaryRecoveryInventory proves the host supplied one bounded complete
// scan without duplicate native identities.
func ValidPrimaryRecoveryInventory(inventory PrimaryRecoveryInventory) bool {
	if !inventory.Complete || len(inventory.Workspaces) > 64 || len(inventory.Agents) > 128 {
		return false
	}
	workspaces := make(map[string]struct{}, len(inventory.Workspaces))
	for _, workspace := range inventory.Workspaces {
		if workspace.WorkspaceID == "" {
			return false
		}
		if _, duplicate := workspaces[workspace.WorkspaceID]; duplicate {
			return false
		}
		workspaces[workspace.WorkspaceID] = struct{}{}
	}
	agents := make(map[string]struct{}, len(inventory.Agents))
	for _, agent := range inventory.Agents {
		if !ValidNativeAgentRecoveryFact(agent) {
			return false
		}
		if _, duplicate := agents[agent.AgentID]; duplicate {
			return false
		}
		agents[agent.AgentID] = struct{}{}
	}
	return true
}

// CurrentEffectObservation applies the positive finite TaskStore-clock window.
func CurrentEffectObservation(observation EffectObservation, nowMillis int64) bool {
	return ValidEffectObservation(observation) && observation.MaximumAgeMillis > 0 &&
		observation.ObservedAtMillis >= 0 && observation.ObservedAtMillis <= nowMillis &&
		nowMillis-observation.ObservedAtMillis <= observation.MaximumAgeMillis
}

// CandidateObservationHash returns the canonical identity of exact Git facts.
func CandidateObservationHash(observation CandidateObservation) string {
	observation.FactHash = ""
	return evidenceDigest(observation)
}

// ValidCandidateObservation rejects a partial or self-inconsistent Git fact.
func ValidCandidateObservation(observation CandidateObservation) bool {
	return observation.FactHash != "" && observation.FactHash == CandidateObservationHash(observation)
}

// CurrentCandidateObservation applies the positive finite Git-fact window.
func CurrentCandidateObservation(observation CandidateObservation, nowMillis int64) bool {
	return ValidCandidateObservation(observation) && observation.MaximumAgeMillis > 0 &&
		observation.ObservedAtMillis >= 0 && observation.ObservedAtMillis <= nowMillis &&
		nowMillis-observation.ObservedAtMillis <= observation.MaximumAgeMillis
}

// StartupReconciliationHash binds one startup scan to its complete bounded
// durable/external fact summary while excluding the self-hash field.
func StartupReconciliationHash(observation StartupReconciliation) string {
	observation.FactHash = ""
	return evidenceDigest(observation)
}

// ValidStartupReconciliation rejects a partial or self-inconsistent startup
// record before it can be treated as recovered state.
func ValidStartupReconciliation(observation StartupReconciliation) bool {
	recoveryFactsValid := observation.PrimaryRecoveryPhase == "" && observation.ReplacementAuthorityID == "" &&
		observation.OriginalPrimaryAgentID == "" && observation.ReplacementPrimaryAgentID == "" ||
		observation.PrimaryRecoveryPhase != "" && validScopePart(observation.OriginalPrimaryAgentID) &&
			(observation.ReplacementAuthorityID == "" || validScopePart(observation.ReplacementAuthorityID)) &&
			(observation.ReplacementPrimaryAgentID == "" ||
				observation.ReplacementAuthorityID != "" && validScopePart(observation.ReplacementPrimaryAgentID))
	return observation.SchemaVersion == StartupReconciliationSchemaVersion &&
		observation.ID != "" && observation.ObservedAtMillis >= 0 &&
		observation.CommandCount > 0 && observation.CommandChainHash != "" &&
		observation.LastCommandID != "" && observation.OperationalObservationID != "" &&
		(observation.EffectObservationCount == 0) == (observation.EffectObservationChainHash == "") &&
		(observation.HelperCount == 0) == (observation.HelperChainHash == "") &&
		(observation.CandidateID == "") == (observation.CandidateSHA == "") &&
		(observation.FrontierObservationID == "") == (observation.FrontierObservationHash == "") &&
		(observation.CandidateObservationID == "") == (observation.CandidateObservationHash == "") &&
		recoveryFactsValid &&
		observation.FactHash != "" &&
		observation.FactHash == StartupReconciliationHash(observation)
}
