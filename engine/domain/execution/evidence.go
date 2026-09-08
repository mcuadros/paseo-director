// SPDX-License-Identifier: Apache-2.0

package execution

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
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
	return observation.FactHash != "" && observation.FactHash == EffectObservationHash(observation)
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
