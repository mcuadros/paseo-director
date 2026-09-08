// SPDX-License-Identifier: Apache-2.0

package execution

import "testing"

func TestEffectAndCandidateEvidenceRequireCurrentSelfConsistentFacts(t *testing.T) {
	effect := EffectObservation{
		ID: "observation-1", EffectID: "effect-1", Status: ObservationAbsent,
		BindingHash: "binding-1", PriorDispatcherAbsent: true,
		ObservedAtMillis: 1_000, MaximumAgeMillis: 30_000,
	}
	effect.FactHash = EffectObservationHash(effect)
	if !CurrentEffectObservation(effect, 31_000) {
		t.Fatal("effect observation rejected its exact freshness boundary")
	}
	if CurrentEffectObservation(effect, 31_001) {
		t.Fatal("stale effect observation remained current")
	}
	tamperedEffect := effect
	tamperedEffect.BindingHash = "other"
	if CurrentEffectObservation(tamperedEffect, 1_001) {
		t.Fatal("tampered effect observation remained current")
	}

	candidate := CandidateObservation{
		ID: "candidate-observation-1", ClaimID: "claim-1",
		WorktreeID: "worktree-1", BindingHash: "binding-1",
		CommitSHA: "1111111111111111111111111111111111111111",
		BaseSHA:   "0000000000000000000000000000000000000000",
		Clean:     true, Reachable: true, Owned: true, DescendsFromBase: true, NoConflict: true,
		ObservedAtMillis: 1_000, MaximumAgeMillis: 30_000,
	}
	candidate.FactHash = CandidateObservationHash(candidate)
	if !CurrentCandidateObservation(candidate, 31_000) {
		t.Fatal("Candidate observation rejected its exact freshness boundary")
	}
	if CurrentCandidateObservation(candidate, 999) {
		t.Fatal("future Candidate observation remained current")
	}
}
