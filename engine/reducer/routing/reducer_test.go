// SPDX-License-Identifier: Apache-2.0

package routing

import (
	"testing"

	"github.com/mcuadros/director-engine/domain/execution"
)

func routingFacts() Facts {
	return Facts{
		SchemaVersion:             SchemaVersion,
		AgentID:                   "agent-1",
		AgentTurnEnded:            true,
		RepositoryBindingHash:     "binding-1",
		OperationalLimitsAdmitted: true,
		OperationalObservationID:  "periodic-1",
		TaskStoreNowMillis:        1_001,
		Claim: &execution.CompletedClaim{
			ID: "claim-1", SchemaVersion: "director.agent-outcome.completed/v1",
			Outcome: "completed", AgentID: "agent-1",
			CandidateSHA:    "1111111111111111111111111111111111111111",
			BaseSHA:         "0000000000000000000000000000000000000000",
			CriteriaResults: map[string]string{"criterion-1": "claimed_satisfied"},
		},
	}
}

func TestReduceTreatsCompletedClaimAsInputNotCandidateEvidence(t *testing.T) {
	facts := routingFacts()
	decision := Reduce(facts)
	if decision.Kind != DecisionObserveCandidate {
		t.Fatalf("claim without Git observation = %#v", decision)
	}

	facts.CandidateObservation = &execution.CandidateObservation{
		ID: "candidate-observation-1", ClaimID: facts.Claim.ID,
		WorktreeID: "worktree-1", BindingHash: "binding-1", FactHash: "facts-1",
		ObservedAtMillis: 1_000, MaximumAgeMillis: 30_000,
		CommitSHA: facts.Claim.CandidateSHA, BaseSHA: facts.Claim.BaseSHA,
		Clean: true, Reachable: true, Owned: true, DescendsFromBase: true, NoConflict: true,
	}
	facts.CandidateObservation.FactHash = execution.CandidateObservationHash(*facts.CandidateObservation)
	decision = Reduce(facts)
	if decision.Kind != DecisionAdmitCandidate || decision.CandidateSHA != facts.Claim.CandidateSHA {
		t.Fatalf("reconciled Candidate = %#v", decision)
	}

	facts.CandidateObservation.Clean = false
	facts.CandidateObservation.FactHash = execution.CandidateObservationHash(*facts.CandidateObservation)
	decision = Reduce(facts)
	if decision.Kind != DecisionEscalate || decision.CleanupAuthorized {
		t.Fatalf("dirty claimed Candidate = %#v", decision)
	}
}

func TestReduceParksOnPeriodicOperationalFailureWithoutCleanupAuthority(t *testing.T) {
	facts := routingFacts()
	facts.OperationalLimitsAdmitted = false
	facts.OperationalNeedCode = execution.NeedMemoryLimit
	decision := Reduce(facts)
	if decision.Kind != DecisionEscalate || decision.Code != string(execution.NeedMemoryLimit) || decision.CleanupAuthorized {
		t.Fatalf("periodic resource decision = %#v", decision)
	}

	facts = routingFacts()
	facts.OperationalObservationID = ""
	if decision := Reduce(facts); decision.Kind != DecisionObserveOperationalLimits {
		t.Fatalf("missing periodic observation = %#v", decision)
	}
}
