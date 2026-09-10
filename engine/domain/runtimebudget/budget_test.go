// SPDX-License-Identifier: Apache-2.0

package runtimebudget

import (
	"math"
	"strings"
	"testing"

	domainconfig "github.com/mcuadros/director-engine/domain/configuration"
)

func testPolicy(cost uint64) Policy {
	return NewPolicy("revision-1", 10_000, 1_000, 32, cost, 4)
}

func testLedger(t *testing.T, cost uint64) Ledger {
	t.Helper()
	ledger, err := NewLedger(testPolicy(cost), 1_000)
	if err != nil {
		t.Fatal(err)
	}
	return ledger
}

func reserveModel(t *testing.T, ledger Ledger, id string, activity Activity, demand Demand, now int64) Ledger {
	t.Helper()
	next, decision, err := Reserve(ledger, ReserveRequest{
		ID: id, EffectID: "effect-" + id, Activity: activity, LeaseEpoch: 7,
		PolicyRevision: ledger.Policy.Revision, Demand: demand,
	}, now)
	if err != nil || decision.Disposition != DispositionAllow {
		t.Fatalf("reserve %s = %#v, %v", id, decision, err)
	}
	return next
}

func applyUsage(t *testing.T, ledger Ledger, id string, activity Activity, input, output, cost uint64, now int64) (Ledger, Decision) {
	t.Helper()
	observation := ProviderObservation{
		ID: "usage-" + id, EffectID: "effect-" + id, AgentID: "agent-1", Activity: activity,
		LeaseEpoch: 7, PolicyRevision: ledger.Policy.Revision, Sequence: uint64(len(ledger.ProviderSnapshots) + 1),
		ObservedAtMillis: now,
		ProviderUsage: ProviderUsage{
			State: UsageCurrent, SourceRevision: "source-" + id,
			InputTokensPresent: true, InputTokens: input,
			OutputTokensPresent: true, OutputTokens: output,
			CostMicrousdPresent: ledger.Policy.CostLimitMicrousd > 0, CostMicrousd: cost,
		},
	}
	observation.FactHash = ProviderObservationHash(observation)
	next, decision, err := ApplyProviderObservation(ledger, observation)
	if err != nil {
		t.Fatal(err)
	}
	return next, decision
}

func TestPolicyUsesJournalAndPlanThresholds(t *testing.T) {
	policy, err := PolicyFromRunBudget("configuration-sha", domainconfig.RunBudget{
		ElapsedSeconds: 7_200, Tokens: 200_000, Turns: 32, CICycles: 4,
	})
	if err != nil {
		t.Fatal(err)
	}
	if policy.WallTimeLimitMilliseconds != 7_200_000 || policy.TokenLimit != 200_000 ||
		policy.TurnLimit != 32 || policy.CICycleLimit != 4 || policy.CostLimitMicrousd != 0 ||
		policy.SoftThresholdBasisPoints != 8_500 || policy.CorrectionLimit != 3 ||
		policy.ReplacementLimit != 1 || policy.SetupAttemptLimit != 2 {
		t.Fatalf("frozen policy = %#v", policy)
	}
	withCost, err := PolicyFromRunBudget("configuration-sha", domainconfig.RunBudget{
		ElapsedSeconds: 1, Tokens: 1, Turns: 1, CICycles: 1, CostMicrousd: 50_000,
	})
	if err != nil || withCost.CostLimitMicrousd != 50_000 {
		t.Fatalf("optional cost policy = %#v, %v", withCost, err)
	}
}

func TestSoftWarningReservationAcknowledgementAndHardExhaustion(t *testing.T) {
	policy := NewPolicy("revision-1", 100_000, 100, 10, 1_000, 4)
	ledger, _ := NewLedger(policy, 0)
	ledger = reserveModel(t, ledger, "first", ActivityWorkerTurn, Demand{
		WallTimeMilliseconds: 1, Tokens: 10, Turns: 1, CostMicrousd: 1,
	}, 0)
	ledger, decision := applyUsage(t, ledger, "first", ActivityWorkerTurn, 80, 4, 100, 1)
	if decision.Disposition != DispositionAllow || ledger.Consumption.Tokens != 84 {
		t.Fatalf("84%% decision = %#v; ledger=%#v", decision, ledger.Consumption)
	}
	paused, decision, err := Reserve(ledger, ReserveRequest{
		ID: "second", EffectID: "effect-second", Activity: ActivityWorkerTurn, LeaseEpoch: 7,
		PolicyRevision: policy.Revision,
		Demand:         Demand{WallTimeMilliseconds: 1, Tokens: 1, Turns: 1, CostMicrousd: 1},
	}, 2)
	if err != nil || decision.Disposition != DispositionSoftPause || decision.Reason != ReasonTokensSoft ||
		decision.RatioBasisPoints != 8_500 || len(paused.Warnings) != 1 || len(paused.Reservations) != 1 {
		t.Fatalf("85%% reservation = %#v; warnings=%#v reservations=%#v err=%v", decision, paused.Warnings, paused.Reservations, err)
	}
	acknowledged, err := AcknowledgeSoftWarning(paused, decision.WarningID, policy.Revision, "human-1", 2)
	if err != nil {
		t.Fatal(err)
	}
	reserved, decision, err := Reserve(acknowledged, ReserveRequest{
		ID: "second", EffectID: "effect-second", Activity: ActivityWorkerTurn, LeaseEpoch: 7,
		PolicyRevision: policy.Revision,
		Demand:         Demand{WallTimeMilliseconds: 1, Tokens: 1, Turns: 1, CostMicrousd: 1},
	}, 2)
	if err != nil || decision.Disposition != DispositionAllow || len(reserved.Reservations) != 2 {
		t.Fatalf("acknowledged reservation = %#v, %v", decision, err)
	}
	reserved, decision = applyUsage(t, reserved, "second", ActivityWorkerTurn, 14, 0, 1, 3)
	if decision.Disposition != DispositionAllow || reserved.Consumption.Tokens != 98 {
		t.Fatalf("post-ack usage = %#v; consumption=%#v", decision, reserved.Consumption)
	}
	_, decision, err = Reserve(reserved, ReserveRequest{
		ID: "third", EffectID: "effect-third", Activity: ActivityWorkerTurn, LeaseEpoch: 7,
		PolicyRevision: policy.Revision,
		Demand:         Demand{WallTimeMilliseconds: 1, Tokens: 2, Turns: 1, CostMicrousd: 1},
	}, 4)
	if err != nil || decision.Disposition != DispositionHardExhausted || decision.Reason != ReasonTokensHard {
		t.Fatalf("hard boundary = %#v, %v", decision, err)
	}
}

func TestProviderUnknownAmbiguousAndMonotonicUsageFailClosed(t *testing.T) {
	ledger := reserveModel(t, testLedger(t, 1_000), "one", ActivityReviewerTurn, Demand{
		WallTimeMilliseconds: 100, Tokens: 10, Turns: 1, CostMicrousd: 10,
	}, 1_000)
	missing := ProviderObservation{
		ID: "usage-one", EffectID: "effect-one", AgentID: "reviewer-1", Activity: ActivityReviewerTurn,
		LeaseEpoch: 7, PolicyRevision: ledger.Policy.Revision, Sequence: 1, ObservedAtMillis: 1_001,
		ProviderUsage: ProviderUsage{State: UsageCurrent, SourceRevision: "reviewer-source-pending-cost", InputTokensPresent: true, InputTokens: 1, OutputTokensPresent: true, OutputTokens: 1},
	}
	missing.FactHash = ProviderObservationHash(missing)
	unknown, decision, err := ApplyProviderObservation(ledger, missing)
	if err != nil || decision.Reason != ReasonProviderUsageUnavailable || unknown.TelemetryState != UsageUnavailable {
		t.Fatalf("missing cost = %#v state=%q err=%v", decision, unknown.TelemetryState, err)
	}
	if !ValidLedger(unknown) || unknown.Reservations[0].Released {
		t.Fatal("unknown usage lost its recoverable outstanding reservation")
	}
	recoveredUsage := missing
	recoveredUsage.ID = "usage-one-recovered"
	recoveredUsage.Sequence = 2
	recoveredUsage.ObservedAtMillis = 1_002
	recoveredUsage.ProviderUsage = ProviderUsage{
		State: UsageCurrent, SourceRevision: "reviewer-source-1",
		InputTokensPresent: true, InputTokens: 1, OutputTokensPresent: true, OutputTokens: 1,
		CostMicrousdPresent: true, CostMicrousd: 10,
	}
	recoveredUsage.FactHash = ProviderObservationHash(recoveredUsage)
	recovered, recoveredDecision, err := ApplyProviderObservation(unknown, recoveredUsage)
	if err != nil || recoveredDecision.Disposition != DispositionAllow || recovered.TelemetryState != UsageCurrent ||
		!recovered.Reservations[0].Released || len(recovered.ProviderSnapshots) != 2 || !ValidLedger(recovered) {
		t.Fatalf("recovered usage = %#v decision=%#v err=%v", recovered, recoveredDecision, err)
	}

	ledger = reserveModel(t, testLedger(t, 0), "two", ActivityWorkerTurn, Demand{WallTimeMilliseconds: 1, Tokens: 1, Turns: 1}, 1_000)
	ledger, decision = applyUsage(t, ledger, "two", ActivityWorkerTurn, 1, 1, 0, 1_001)
	if decision.Disposition != DispositionAllow {
		t.Fatal(decision)
	}
	replay, replayDecision, err := ApplyProviderObservation(ledger, ledger.ProviderSnapshots[0])
	if err != nil {
		t.Fatal(err)
	}
	if replayDecision.Disposition != DispositionAllow || !ValidLedger(replay) || len(replay.ProviderSnapshots) != 1 {
		t.Fatalf("exact replay = %#v ledger=%#v", replayDecision, replay)
	}
	conflict := ledger.ProviderSnapshots[0]
	conflict.ID = "usage-conflict"
	conflict.Sequence = 2
	conflict.OutputTokens++
	conflict.FactHash = ProviderObservationHash(conflict)
	ambiguous, decision, err := ApplyProviderObservation(ledger, conflict)
	if err != nil || decision.Reason != ReasonProviderUsageAmbiguous || ambiguous.TelemetryState != UsageAmbiguous {
		t.Fatalf("changed effect usage = %#v state=%q err=%v", decision, ambiguous.TelemetryState, err)
	}
	ledger = reserveModel(t, ledger, "three", ActivityWorkerTurn, Demand{WallTimeMilliseconds: 1, Tokens: 1, Turns: 1}, 1_002)
	repeatedSource := ProviderObservation{
		ID: "usage-three", EffectID: "effect-three", AgentID: "agent-1", Activity: ActivityWorkerTurn,
		LeaseEpoch: 7, PolicyRevision: ledger.Policy.Revision, Sequence: 2, ObservedAtMillis: 1_003,
		ProviderUsage: ProviderUsage{
			State: UsageCurrent, SourceRevision: "source-two", InputTokensPresent: true,
			InputTokens: 1, OutputTokensPresent: true, OutputTokens: 1,
		},
	}
	repeatedSource.FactHash = ProviderObservationHash(repeatedSource)
	ambiguous, decision, err = ApplyProviderObservation(ledger, repeatedSource)
	if err != nil || decision.Reason != ReasonProviderUsageAmbiguous || ambiguous.TelemetryState != UsageAmbiguous {
		t.Fatalf("repeated provider revision = %#v state=%q err=%v", decision, ambiguous.TelemetryState, err)
	}
}

func TestCorrectionCIReplacementSetupAndChurnLimits(t *testing.T) {
	ledger := testLedger(t, 0)
	for index := 0; index < 3; index++ {
		id := string(rune('a' + index))
		ledger = reserveModel(t, ledger, id, ActivityCorrectionTurn, Demand{WallTimeMilliseconds: 1, Tokens: 1, Turns: 1}, 1_000+int64(index))
		var decision Decision
		ledger, decision = applyUsage(t, ledger, id, ActivityCorrectionTurn, 0, 0, 0, 1_000+int64(index))
		if decision.Disposition != DispositionAllow {
			t.Fatal(decision)
		}
	}
	_, decision, err := Reserve(ledger, ReserveRequest{
		ID: "fourth", EffectID: "effect-fourth", Activity: ActivityCorrectionTurn, LeaseEpoch: 7,
		PolicyRevision: ledger.Policy.Revision, Demand: Demand{WallTimeMilliseconds: 1, Tokens: 1, Turns: 1},
	}, 1_004)
	if err != nil || decision.Reason != ReasonCorrectionHard {
		t.Fatalf("fourth correction = %#v, %v", decision, err)
	}

	validation, _ := NewLedger(testPolicy(0), 1_000)
	request := ReserveRequest{
		ID: "validation-1", EffectID: "validation-effect-1", Activity: ActivityValidationCycle,
		LeaseEpoch: 7, PolicyRevision: validation.Policy.Revision,
		Demand: Demand{WallTimeMilliseconds: 100}, CandidateSHA: strings.Repeat("a", 40),
	}
	validation, decision, err = Reserve(validation, request, 1_000)
	if err != nil || decision.Disposition != DispositionAllow {
		t.Fatal(decision, err)
	}
	activity := ActivityObservation{
		ID: "activity-validation-1", ReservationID: request.ID, EffectID: request.EffectID,
		Activity: request.Activity, LeaseEpoch: 7, PolicyRevision: validation.Policy.Revision,
		ObservedAtMillis: 1_100, CandidateSHA: request.CandidateSHA,
	}
	activity.FactHash = ActivityObservationHash(activity)
	validation, decision, err = ApplyActivityObservation(validation, activity)
	if err != nil || decision.Disposition != DispositionAllow {
		t.Fatal(decision, err)
	}
	request.ID, request.EffectID = "validation-2", "validation-effect-2"
	_, decision, err = Reserve(validation, request, 1_101)
	if err != nil || decision.Reason != ReasonRepeatedValidation {
		t.Fatalf("same-Candidate validation = %#v, %v", decision, err)
	}

	setup, _ := NewLedger(testPolicy(0), 1_000)
	setupRequest := ReserveRequest{
		ID: "setup-1", EffectID: "setup-effect-1", Activity: ActivitySetupAttempt,
		LeaseEpoch: 7, PolicyRevision: setup.Policy.Revision,
		Demand: Demand{WallTimeMilliseconds: 100}, Fingerprint: strings.Repeat("f", 64),
	}
	setup, _, _ = Reserve(setup, setupRequest, 1_000)
	setupActivity := ActivityObservation{
		ID: "activity-setup-1", ReservationID: setupRequest.ID, EffectID: setupRequest.EffectID,
		Activity: setupRequest.Activity, LeaseEpoch: 7, PolicyRevision: setup.Policy.Revision,
		ObservedAtMillis: 1_100, Fingerprint: setupRequest.Fingerprint, Failed: true,
	}
	setupActivity.FactHash = ActivityObservationHash(setupActivity)
	setup, _, _ = ApplyActivityObservation(setup, setupActivity)
	setupRequest.ID, setupRequest.EffectID = "setup-2", "setup-effect-2"
	_, decision, err = Reserve(setup, setupRequest, 1_101)
	if err != nil || decision.Reason != ReasonSetupChurn {
		t.Fatalf("unchanged setup churn = %#v, %v", decision, err)
	}
}

func TestClockOverflowAndMutationBoundaries(t *testing.T) {
	ledger := testLedger(t, 0)
	if _, decision := Evaluate(ledger, 999); decision.Reason != ReasonClockRegressed {
		t.Fatalf("regressed clock = %#v", decision)
	}
	policy := NewPolicy("revision-1", math.MaxUint64, math.MaxUint64, 100, 0, 4)
	ledger, _ = NewLedger(policy, 0)
	ledger.Consumption.Tokens = math.MaxUint64
	if _, decision := decideDimensions(ledger, Demand{Tokens: 1}, 0); decision.Reason != ReasonUsageOverflow {
		t.Fatalf("overflow = %#v", decision)
	}

	valid := testLedger(t, 0)
	mutations := map[string]func(*Ledger){
		"schema":        func(value *Ledger) { value.SchemaVersion = "future" },
		"wall clock":    func(value *Ledger) { value.Consumption.WallTimeMilliseconds++ },
		"policy":        func(value *Ledger) { value.Policy.CorrectionLimit++ },
		"telemetry":     func(value *Ledger) { value.TelemetryState = "future" },
		"warning bound": func(value *Ledger) { value.Warnings = make([]Warning, MaximumWarnings+1) },
	}
	for name, mutate := range mutations {
		t.Run(name, func(t *testing.T) {
			changed := clone(valid)
			mutate(&changed)
			if ValidLedger(changed) {
				t.Fatalf("mutation %q survived", name)
			}
		})
	}
}

func TestAuthoritativePreHandoffAbsenceReleasesWithoutConsumption(t *testing.T) {
	ledger := reserveModel(t, testLedger(t, 0), "absent", ActivitySetupAttempt, Demand{WallTimeMilliseconds: 100}, 1_000)
	released, err := ReleaseAbsentReservation(ledger, "effect-absent", 7, "budget-absence-observation")
	if err != nil || !released.Reservations[0].Released || released.Consumption.SetupAttempts != 0 ||
		released.Reservations[0].EvidenceID != "budget-absence-observation" || !ValidLedger(released) {
		t.Fatalf("released reservation = %#v, %v", released, err)
	}
	if _, err := ReleaseAbsentReservation(released, "effect-absent", 7, "budget-absence-second"); err == nil {
		t.Fatal("released reservation was consumed twice")
	}
}

func TestSeededBudgetPropertiesAreDeterministicAndMonotonic(t *testing.T) {
	state := uint64(36)
	next := func(bound uint64) uint64 {
		state = state*6_364_136_223_846_793_005 + 1_442_695_040_888_963_407
		return state % bound
	}
	for iteration := 0; iteration < 2_000; iteration++ {
		limit := next(10_000) + 1
		used := next(limit)
		threshold := softThreshold(limit)
		if threshold == 0 || threshold > limit {
			t.Fatalf("threshold(%d)=%d", limit, threshold)
		}
		if used >= threshold && ratioBasisPoints(used, limit) < SoftThresholdBasisPoints && limit >= 20 {
			t.Fatalf("ratio regressed: used=%d limit=%d threshold=%d ratio=%d", used, limit, threshold, ratioBasisPoints(used, limit))
		}
		if ratioBasisPoints(used, limit) > 10_000 {
			t.Fatalf("ratio overflow: used=%d limit=%d", used, limit)
		}
	}
}
