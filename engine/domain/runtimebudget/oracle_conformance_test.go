// SPDX-License-Identifier: Apache-2.0

package runtimebudget_test

import (
	"testing"

	budget "github.com/mcuadros/director-engine/domain/runtimebudget"
	oracle "github.com/mcuadros/director-engine/internal/testkit/executionoracle"
)

func productionLedger(t *testing.T) budget.Ledger {
	t.Helper()
	ledger, err := budget.NewLedger(budget.NewPolicy("revision-1", 100, 100, 100, 100, 4), 0)
	if err != nil {
		t.Fatal(err)
	}
	return ledger
}

func TestRuntimeBudgetBlackBoxConformsToIntegratedExecutionOracle(t *testing.T) {
	softLedger := productionLedger(t)
	_, soft, err := budget.Reserve(softLedger, budget.ReserveRequest{
		ID: "reservation-soft", EffectID: "effect-soft", Activity: budget.ActivityHelperTurn,
		LeaseEpoch: 1, PolicyRevision: softLedger.Policy.Revision,
		Demand: budget.Demand{WallTimeMilliseconds: 1, Tokens: 85, Turns: 1, CostMicrousd: 1},
	}, 0)
	if err != nil || soft.Disposition != budget.DispositionSoftPause || soft.Reason != budget.ReasonTokensSoft {
		t.Fatalf("production soft decision = %#v, %v", soft, err)
	}
	softOracle := oracle.Baseline()
	softOracle.Usage.Tokens.Consumed = 84
	softOracle.Usage.Tokens.NextReservation = 1
	oracleSoft := oracle.Evaluate(softOracle)
	if oracleSoft.Reason != oracle.ReasonSoftBudget || len(oracleSoft.Actions) != 1 ||
		oracleSoft.Actions[0].Kind != oracle.ActionPauseRun {
		t.Fatalf("oracle soft decision = %#v", oracleSoft)
	}

	hardLedger := productionLedger(t)
	_, hard, err := budget.Reserve(hardLedger, budget.ReserveRequest{
		ID: "reservation-hard", EffectID: "effect-hard", Activity: budget.ActivityHelperTurn,
		LeaseEpoch: 1, PolicyRevision: hardLedger.Policy.Revision,
		Demand: budget.Demand{WallTimeMilliseconds: 1, Tokens: 100, Turns: 1, CostMicrousd: 1},
	}, 0)
	if err != nil || hard.Disposition != budget.DispositionHardExhausted || hard.Reason != budget.ReasonTokensHard {
		t.Fatalf("production hard decision = %#v, %v", hard, err)
	}
	hardOracle := oracle.Baseline()
	hardOracle.Usage.Tokens.Consumed = 99
	hardOracle.Usage.Tokens.NextReservation = 1
	oracleHard := oracle.Evaluate(hardOracle)
	if oracleHard.Reason != oracle.ReasonHardBudget || len(oracleHard.Actions) != 1 ||
		oracleHard.Actions[0].Kind != oracle.ActionParkNeedsYou {
		t.Fatalf("oracle hard decision = %#v", oracleHard)
	}

	unknownLedger := productionLedger(t)
	unknownLedger, decision, err := budget.Reserve(unknownLedger, budget.ReserveRequest{
		ID: "reservation-unknown", EffectID: "effect-unknown", Activity: budget.ActivityReviewerTurn,
		LeaseEpoch: 1, PolicyRevision: unknownLedger.Policy.Revision,
		Demand: budget.Demand{WallTimeMilliseconds: 1, Tokens: 1, Turns: 1, CostMicrousd: 1},
	}, 0)
	if err != nil || decision.Disposition != budget.DispositionAllow {
		t.Fatal(decision, err)
	}
	usage := budget.ProviderObservation{
		ID: "usage-unknown", EffectID: "effect-unknown", AgentID: "reviewer-1",
		Activity: budget.ActivityReviewerTurn, LeaseEpoch: 1, PolicyRevision: unknownLedger.Policy.Revision,
		Sequence: 1, ObservedAtMillis: 1,
		ProviderUsage: budget.ProviderUsage{State: budget.UsageUnavailable},
	}
	usage.FactHash = budget.ProviderObservationHash(usage)
	_, unknown, err := budget.ApplyProviderObservation(unknownLedger, usage)
	if err != nil || unknown.Disposition != budget.DispositionFailClosed || unknown.Reason != budget.ReasonProviderUsageUnavailable {
		t.Fatalf("production unknown decision = %#v, %v", unknown, err)
	}
	unknownOracle := oracle.Baseline()
	unknownOracle.Usage.Tokens.State = oracle.UsageUnavailable
	oracleUnknown := oracle.Evaluate(unknownOracle)
	if oracleUnknown.Reason != oracle.ReasonContradictoryFact || len(oracleUnknown.Actions) != 1 ||
		oracleUnknown.Actions[0].Kind != oracle.ActionParkNeedsYou {
		t.Fatalf("oracle unknown decision = %#v", oracleUnknown)
	}
}
