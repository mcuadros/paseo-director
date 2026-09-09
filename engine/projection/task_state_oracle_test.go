// SPDX-License-Identifier: Apache-2.0

package projection

import (
	"reflect"
	"strconv"
	"testing"

	"github.com/mcuadros/director-engine/domain/agentoutcome"
	oracle "github.com/mcuadros/director-engine/internal/testkit/projectionoracle"
)

func productionFacts(facts oracle.Facts) TaskStateFacts {
	result := TaskStateFacts{
		TaskID: facts.TaskID, TaskVersion: facts.TaskVersion,
		Eligibility: EligibilityFact{
			Status: FactStatus(facts.Eligibility.Status), TaskVersion: facts.Eligibility.TaskVersion,
			Decision: EligibilityDecision(facts.Eligibility.Decision),
		},
		Run: RunFact{
			Status: FactStatus(facts.Run.Status), TaskVersion: facts.Run.TaskVersion,
			ID: facts.Run.ID, Active: facts.Run.Active,
		},
		Candidate: CandidateFact{
			Status: FactStatus(facts.Candidate.Status), TaskVersion: facts.Candidate.TaskVersion,
			ID: facts.Candidate.ID, RunID: facts.Candidate.RunID,
		},
		Validation: ValidationFact{
			Status: FactStatus(facts.Validation.Status), CandidateID: facts.Validation.CandidateID,
			Outcome: ValidationOutcome(facts.Validation.Outcome),
		},
		Review: ReviewFact{
			Status: FactStatus(facts.Review.Status), CandidateID: facts.Review.CandidateID,
			Outcome: ReviewOutcome(facts.Review.Outcome),
		},
		Feedback: FeedbackFact{
			Status: FactStatus(facts.Feedback.Status), CandidateID: facts.Feedback.CandidateID,
			State: FeedbackState(facts.Feedback.State),
		},
		Delivery: DeliveryFact{
			Status: FactStatus(facts.Delivery.Status), CandidateID: facts.Delivery.CandidateID,
			State: DeliveryState(facts.Delivery.State),
		},
		Cleanup: CleanupFact{
			Status: FactStatus(facts.Cleanup.Status), RunID: facts.Cleanup.RunID,
			State: CleanupState(facts.Cleanup.State),
		},
		HumanInput: HumanInputFact{
			Status: FactStatus(facts.HumanInput.Status), TaskVersion: facts.HumanInput.TaskVersion,
			State: HumanInputState(facts.HumanInput.State), Code: AttentionCode(facts.HumanInput.Code),
			WakeCondition: facts.HumanInput.WakeCondition,
		},
		Terminal: TerminalFact{
			Status: FactStatus(facts.Terminal.Status), TaskVersion: facts.Terminal.TaskVersion,
			State: TerminalState(facts.Terminal.State),
		},
	}
	for _, claim := range facts.Claims {
		result.Claims = append(result.Claims, ClaimFact{
			Required: claim.Required, Status: FactStatus(claim.Status), TaskVersion: claim.TaskVersion,
			RunID: claim.RunID, CandidateID: claim.CandidateID, Outcome: agentoutcome.Kind(claim.Outcome),
		})
	}
	return result
}

func productionExpected(expected oracle.Projection) TaskProjection {
	result := TaskProjection{
		State: BoardState(expected.State), BoardMember: expected.BoardMember, DoneMember: expected.DoneMember,
		Explanation: ExplanationCode(expected.Explanation),
	}
	for _, blocker := range expected.Blockers {
		result.Blockers = append(result.Blockers, BlockerCode(blocker))
	}
	for _, attention := range expected.Attention {
		result.Attention = append(result.Attention, AttentionCode(attention))
	}
	return result
}

func oracleStage(stage string) oracle.Facts {
	facts := oracle.Facts{
		TaskID: "task-1", TaskVersion: 3,
		Eligibility: oracle.EligibilityFact{Status: oracle.FactCurrent, TaskVersion: 3, Decision: oracle.EligibilityEligible},
		Run:         oracle.RunFact{Status: oracle.FactMissing},
		Candidate:   oracle.CandidateFact{Status: oracle.FactMissing},
		Validation:  oracle.ValidationFact{Status: oracle.FactMissing},
		Review:      oracle.ReviewFact{Status: oracle.FactMissing},
		Feedback:    oracle.FeedbackFact{Status: oracle.FactMissing},
		Delivery:    oracle.DeliveryFact{Status: oracle.FactMissing},
		Cleanup:     oracle.CleanupFact{Status: oracle.FactMissing},
		HumanInput:  oracle.HumanInputFact{Status: oracle.FactCurrent, TaskVersion: 3, State: oracle.HumanInputNone},
		Terminal:    oracle.TerminalFact{Status: oracle.FactCurrent, TaskVersion: 3, State: oracle.TerminalOpen},
	}
	if stage == "queued" {
		facts.Eligibility.Decision = oracle.EligibilityDependencyWait
		return facts
	}
	facts.Run = oracle.RunFact{Status: oracle.FactCurrent, TaskVersion: 3, ID: "run-1", Active: true}
	if stage == "building" {
		return facts
	}
	facts.Candidate = oracle.CandidateFact{
		Status: oracle.FactCurrent, TaskVersion: 3, ID: "candidate-1", RunID: "run-1",
	}
	facts.Validation = oracle.ValidationFact{
		Status: oracle.FactCurrent, CandidateID: "candidate-1", Outcome: oracle.ValidationPending,
	}
	if stage == "validating" {
		return facts
	}
	facts.Validation.Outcome = oracle.ValidationPassed
	facts.Review = oracle.ReviewFact{
		Status: oracle.FactCurrent, CandidateID: "candidate-1", Outcome: oracle.ReviewPending,
	}
	if stage == "review" {
		return facts
	}
	facts.Review.Outcome = oracle.ReviewApproved
	facts.Feedback = oracle.FeedbackFact{
		Status: oracle.FactCurrent, CandidateID: "candidate-1", State: oracle.FeedbackNone,
	}
	facts.Delivery = oracle.DeliveryFact{
		Status: oracle.FactCurrent, CandidateID: "candidate-1", State: oracle.DeliveryPendingPublication,
	}
	if stage == "ready" {
		return facts
	}
	facts.Delivery.State = oracle.DeliveryIntegrated
	facts.Cleanup = oracle.CleanupFact{Status: oracle.FactCurrent, RunID: "run-1", State: oracle.CleanupComplete}
	facts.Run.Active = false
	facts.Terminal.State = oracle.TerminalDone
	return facts
}

func TestTaskProjectionMatchesIndependentOracleStages(t *testing.T) {
	for _, stage := range []string{"queued", "building", "validating", "review", "ready", "done"} {
		t.Run(stage, func(t *testing.T) {
			assertProjectionOracle(t, oracleStage(stage))
		})
	}
}

func TestTaskProjectionCodeOrdersMatchOraclePublicContract(t *testing.T) {
	wantBlockers := oracle.BlockerCodeOrder()
	gotBlockers := BlockerCodeOrder()
	if len(gotBlockers) != len(wantBlockers) {
		t.Fatalf("blocker code count = %d, want %d", len(gotBlockers), len(wantBlockers))
	}
	for index := range wantBlockers {
		if gotBlockers[index] != BlockerCode(wantBlockers[index]) {
			t.Fatalf("blocker code %d = %q, want %q", index, gotBlockers[index], wantBlockers[index])
		}
	}
	wantAttention := oracle.AttentionCodeOrder()
	gotAttention := AttentionCodeOrder()
	if len(gotAttention) != len(wantAttention) {
		t.Fatalf("attention code count = %d, want %d", len(gotAttention), len(wantAttention))
	}
	for index := range wantAttention {
		if gotAttention[index] != AttentionCode(wantAttention[index]) {
			t.Fatalf("attention code %d = %q, want %q", index, gotAttention[index], wantAttention[index])
		}
	}
	gotBlockers[0], gotAttention[0] = "mutated", "mutated"
	if BlockerCodeOrder()[0] == "mutated" || AttentionCodeOrder()[0] == "mutated" {
		t.Fatal("code order exposes shared mutable storage")
	}
}

func assertProjectionOracle(t *testing.T, facts oracle.Facts) {
	t.Helper()
	got := DeriveTaskProjection(productionFacts(facts))
	want := productionExpected(oracle.Project(facts))
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("projection differs from black-box oracle\nfacts: %#v\ngot:   %#v\nwant:  %#v", facts, got, want)
	}
}

func TestTaskProjectionMatchesIndependentOracleFactClasses(t *testing.T) {
	statuses := []oracle.FactStatus{
		oracle.FactMissing, oracle.FactCurrent, oracle.FactStale, oracle.FactContradictory, "future",
	}
	for _, status := range statuses {
		t.Run("eligibility/"+string(status), func(t *testing.T) {
			facts := oracleStage("queued")
			facts.Eligibility.Status = status
			assertProjectionOracle(t, facts)
		})
		t.Run("run/"+string(status), func(t *testing.T) {
			facts := oracleStage("building")
			facts.Run.Status = status
			assertProjectionOracle(t, facts)
		})
		t.Run("candidate/"+string(status), func(t *testing.T) {
			facts := oracleStage("validating")
			facts.Candidate.Status = status
			assertProjectionOracle(t, facts)
		})
		t.Run("validation/"+string(status), func(t *testing.T) {
			facts := oracleStage("ready")
			facts.Validation.Status = status
			assertProjectionOracle(t, facts)
		})
		t.Run("review/"+string(status), func(t *testing.T) {
			facts := oracleStage("ready")
			facts.Review.Status = status
			assertProjectionOracle(t, facts)
		})
		t.Run("feedback/"+string(status), func(t *testing.T) {
			facts := oracleStage("ready")
			facts.Feedback.Status = status
			assertProjectionOracle(t, facts)
		})
		t.Run("delivery/"+string(status), func(t *testing.T) {
			facts := oracleStage("ready")
			facts.Delivery.Status = status
			assertProjectionOracle(t, facts)
		})
		t.Run("cleanup/"+string(status), func(t *testing.T) {
			facts := oracleStage("done")
			facts.Cleanup.Status = status
			assertProjectionOracle(t, facts)
		})
		t.Run("human/"+string(status), func(t *testing.T) {
			facts := oracleStage("ready")
			facts.HumanInput.Status = status
			assertProjectionOracle(t, facts)
		})
		t.Run("terminal/"+string(status), func(t *testing.T) {
			facts := oracleStage("done")
			facts.Terminal.Status = status
			assertProjectionOracle(t, facts)
		})
	}
}

func TestTaskProjectionMatchesIndependentOracleDecisionMatrices(t *testing.T) {
	eligibility := []oracle.EligibilityDecision{
		oracle.EligibilityEligible, oracle.EligibilityDependencyWait, oracle.EligibilityPolicyWait,
		oracle.EligibilityCapacityWait, oracle.EligibilityExternalWait, oracle.EligibilityEscalate, "future",
	}
	for _, decision := range eligibility {
		t.Run("eligibility/"+string(decision), func(t *testing.T) {
			facts := oracleStage("queued")
			facts.Eligibility.Decision = decision
			assertProjectionOracle(t, facts)
		})
	}
	validations := []oracle.ValidationOutcome{
		oracle.ValidationPending, oracle.ValidationRunning, oracle.ValidationPassed, oracle.ValidationFailed, "future",
	}
	reviews := []oracle.ReviewOutcome{
		oracle.ReviewPending, oracle.ReviewRunning, oracle.ReviewApproved,
		oracle.ReviewChangesRequested, oracle.ReviewNeedsHuman, "future",
	}
	for _, validation := range validations {
		for _, review := range reviews {
			t.Run("quality/"+string(validation)+"/"+string(review), func(t *testing.T) {
				facts := oracleStage("ready")
				facts.Validation.Outcome = validation
				facts.Review.Outcome = review
				assertProjectionOracle(t, facts)
			})
		}
	}
	for _, feedback := range []oracle.FeedbackState{
		oracle.FeedbackNone, oracle.FeedbackActionable, oracle.FeedbackResolved, "future",
	} {
		t.Run("feedback/"+string(feedback), func(t *testing.T) {
			facts := oracleStage("ready")
			facts.Feedback.State = feedback
			assertProjectionOracle(t, facts)
		})
	}
	for _, delivery := range []oracle.DeliveryState{
		oracle.DeliveryPendingPublication, oracle.DeliveryPublished, oracle.DeliveryIntegrated, "future",
	} {
		t.Run("delivery/"+string(delivery), func(t *testing.T) {
			facts := oracleStage("done")
			facts.Delivery.State = delivery
			assertProjectionOracle(t, facts)
		})
	}
	for _, cleanup := range []oracle.CleanupState{oracle.CleanupPending, oracle.CleanupComplete, "future"} {
		t.Run("cleanup/"+string(cleanup), func(t *testing.T) {
			facts := oracleStage("done")
			facts.Cleanup.State = cleanup
			assertProjectionOracle(t, facts)
		})
	}
}

func TestTaskProjectionMatchesIndependentOracleClaimsAndHumanInput(t *testing.T) {
	for _, status := range []oracle.FactStatus{
		oracle.FactMissing, oracle.FactCurrent, oracle.FactStale, oracle.FactContradictory, "future",
	} {
		t.Run("claim/"+string(status), func(t *testing.T) {
			facts := oracleStage("ready")
			facts.Claims = []oracle.ClaimFact{{
				Required: true, Status: status, TaskVersion: facts.TaskVersion,
				RunID: facts.Run.ID, CandidateID: facts.Candidate.ID, Outcome: oracle.OutcomeNeedsValidation,
			}}
			assertProjectionOracle(t, facts)
		})
	}
	for _, outcome := range []oracle.AgentOutcome{
		oracle.OutcomeCompleted, oracle.OutcomeNeedsValidation, oracle.OutcomeNeedsReview,
		oracle.OutcomeNeedsHumanDecision, oracle.OutcomeBlockedByDependency,
		oracle.OutcomeBlockedByAccess, oracle.OutcomeBudgetExhausted, "future",
	} {
		t.Run("claim-outcome/"+string(outcome), func(t *testing.T) {
			facts := oracleStage("ready")
			candidateID := facts.Candidate.ID
			if outcome == oracle.OutcomeNeedsHumanDecision || outcome == oracle.OutcomeBlockedByDependency ||
				outcome == oracle.OutcomeBlockedByAccess || outcome == oracle.OutcomeBudgetExhausted {
				candidateID = ""
			}
			facts.Claims = []oracle.ClaimFact{{
				Required: true, Status: oracle.FactCurrent, TaskVersion: facts.TaskVersion,
				RunID: facts.Run.ID, CandidateID: candidateID, Outcome: outcome,
			}}
			assertProjectionOracle(t, facts)
		})
	}
	for _, code := range oracle.AttentionCodeOrder() {
		t.Run("attention/"+string(code), func(t *testing.T) {
			facts := oracleStage("ready")
			facts.HumanInput = oracle.HumanInputFact{
				Status: oracle.FactCurrent, TaskVersion: facts.TaskVersion, State: oracle.HumanInputPending,
				Code: code, WakeCondition: "human action is recorded",
			}
			assertProjectionOracle(t, facts)
		})
	}
}

func TestTaskProjectionMatchesIndependentOracleIdentityBindings(t *testing.T) {
	for name, mutate := range map[string]func(*oracle.Facts){
		"Task identity":       func(facts *oracle.Facts) { facts.TaskID = "" },
		"eligibility version": func(facts *oracle.Facts) { facts.Eligibility.TaskVersion++ },
		"Run version":         func(facts *oracle.Facts) { facts.Run.TaskVersion++ },
		"Candidate version":   func(facts *oracle.Facts) { facts.Candidate.TaskVersion++ },
		"Candidate Run":       func(facts *oracle.Facts) { facts.Candidate.RunID = "another-run" },
		"Validation Candidate": func(facts *oracle.Facts) {
			facts.Validation.CandidateID = "another-candidate"
		},
		"Review Candidate": func(facts *oracle.Facts) { facts.Review.CandidateID = "another-candidate" },
		"feedback Candidate": func(facts *oracle.Facts) {
			facts.Feedback.CandidateID = "another-candidate"
		},
		"delivery Candidate": func(facts *oracle.Facts) {
			facts.Delivery.CandidateID = "another-candidate"
		},
		"cleanup Run":   func(facts *oracle.Facts) { facts.Cleanup.RunID = "another-run" },
		"human version": func(facts *oracle.Facts) { facts.HumanInput.TaskVersion++ },
		"terminal version": func(facts *oracle.Facts) {
			facts.Terminal.TaskVersion++
		},
	} {
		t.Run(name, func(t *testing.T) {
			facts := oracleStage("done")
			mutate(&facts)
			assertProjectionOracle(t, facts)
		})
	}
}

type projectionTestStream uint64

func (stream *projectionTestStream) next() uint64 {
	*stream += 0x9e3779b97f4a7c15
	value := uint64(*stream)
	value = (value ^ (value >> 30)) * 0xbf58476d1ce4e5b9
	value = (value ^ (value >> 27)) * 0x94d049bb133111eb
	return value ^ (value >> 31)
}

func chooseProjectionTestValue[T any](stream *projectionTestStream, values []T) T {
	return values[stream.next()%uint64(len(values))]
}

func TestTaskProjectionMatchesIndependentOracleSeededProperties(t *testing.T) {
	statuses := []oracle.FactStatus{
		oracle.FactMissing, oracle.FactCurrent, oracle.FactStale, oracle.FactContradictory, "future",
	}
	stages := []string{"queued", "building", "validating", "review", "ready", "done"}
	for seed := uint64(0); seed < 512; seed++ {
		stream := projectionTestStream(seed)
		facts := oracleStage(chooseProjectionTestValue(&stream, stages))
		facts.Eligibility.Status = chooseProjectionTestValue(&stream, statuses)
		facts.Eligibility.Decision = chooseProjectionTestValue(&stream, []oracle.EligibilityDecision{
			oracle.EligibilityEligible, oracle.EligibilityDependencyWait, oracle.EligibilityPolicyWait,
			oracle.EligibilityCapacityWait, oracle.EligibilityExternalWait, oracle.EligibilityEscalate, "future",
		})
		facts.Run.Status = chooseProjectionTestValue(&stream, statuses)
		facts.Candidate.Status = chooseProjectionTestValue(&stream, statuses)
		facts.Validation.Status = chooseProjectionTestValue(&stream, statuses)
		facts.Validation.Outcome = chooseProjectionTestValue(&stream, []oracle.ValidationOutcome{
			oracle.ValidationPending, oracle.ValidationRunning, oracle.ValidationPassed, oracle.ValidationFailed, "future",
		})
		facts.Review.Status = chooseProjectionTestValue(&stream, statuses)
		facts.Review.Outcome = chooseProjectionTestValue(&stream, []oracle.ReviewOutcome{
			oracle.ReviewPending, oracle.ReviewRunning, oracle.ReviewApproved,
			oracle.ReviewChangesRequested, oracle.ReviewNeedsHuman, "future",
		})
		facts.Feedback.Status = chooseProjectionTestValue(&stream, statuses)
		facts.Feedback.State = chooseProjectionTestValue(&stream, []oracle.FeedbackState{
			oracle.FeedbackNone, oracle.FeedbackActionable, oracle.FeedbackResolved, "future",
		})
		facts.Delivery.Status = chooseProjectionTestValue(&stream, statuses)
		facts.Delivery.State = chooseProjectionTestValue(&stream, []oracle.DeliveryState{
			oracle.DeliveryPendingPublication, oracle.DeliveryPublished, oracle.DeliveryIntegrated, "future",
		})
		facts.Cleanup.Status = chooseProjectionTestValue(&stream, statuses)
		facts.Cleanup.State = chooseProjectionTestValue(&stream, []oracle.CleanupState{
			oracle.CleanupPending, oracle.CleanupComplete, "future",
		})
		facts.HumanInput.Status = chooseProjectionTestValue(&stream, statuses)
		facts.HumanInput.State = chooseProjectionTestValue(&stream, []oracle.HumanInputState{
			oracle.HumanInputNone, oracle.HumanInputPending, oracle.HumanInputResolved, "future",
		})
		if facts.HumanInput.State == oracle.HumanInputPending {
			facts.HumanInput.Code = chooseProjectionTestValue(&stream, oracle.AttentionCodeOrder())
			facts.HumanInput.WakeCondition = "record one bounded human decision"
		} else {
			facts.HumanInput.Code = ""
			facts.HumanInput.WakeCondition = ""
		}
		facts.Terminal.Status = chooseProjectionTestValue(&stream, statuses)
		facts.Terminal.State = chooseProjectionTestValue(&stream, []oracle.TerminalState{
			oracle.TerminalOpen, oracle.TerminalDone, "future",
		})
		if stream.next()%3 == 0 {
			outcome := chooseProjectionTestValue(&stream, []oracle.AgentOutcome{
				oracle.OutcomeCompleted, oracle.OutcomeNeedsValidation, oracle.OutcomeNeedsReview,
				oracle.OutcomeNeedsHumanDecision, oracle.OutcomeBlockedByDependency,
				oracle.OutcomeBlockedByAccess, oracle.OutcomeBudgetExhausted, "future",
			})
			candidateID := ""
			if outcome == oracle.OutcomeCompleted || outcome == oracle.OutcomeNeedsValidation || outcome == oracle.OutcomeNeedsReview {
				candidateID = facts.Candidate.ID
			}
			facts.Claims = []oracle.ClaimFact{{
				Required: true, Status: chooseProjectionTestValue(&stream, statuses),
				TaskVersion: facts.TaskVersion, RunID: facts.Run.ID, CandidateID: candidateID, Outcome: outcome,
			}}
		}
		if stream.next()%7 == 0 {
			facts.Candidate.RunID = "another-run"
		}
		if stream.next()%11 == 0 {
			facts.Validation.CandidateID = "another-candidate"
		}
		if stream.next()%13 == 0 {
			facts.TaskID = ""
		}
		if stream.next()%17 == 0 {
			facts.Eligibility.TaskVersion++
		}
		if stream.next()%19 == 0 {
			facts.Run.TaskVersion++
		}
		if stream.next()%23 == 0 {
			facts.Candidate.TaskVersion++
		}
		if stream.next()%29 == 0 {
			facts.Review.CandidateID = "another-candidate"
		}
		if stream.next()%31 == 0 {
			facts.Feedback.CandidateID = "another-candidate"
		}
		if stream.next()%37 == 0 {
			facts.Delivery.CandidateID = "another-candidate"
		}
		if stream.next()%41 == 0 {
			facts.Cleanup.RunID = "another-run"
		}
		if stream.next()%43 == 0 {
			facts.HumanInput.TaskVersion++
		}
		if stream.next()%47 == 0 {
			facts.Terminal.TaskVersion++
		}
		t.Run("seed-"+strconv.FormatUint(seed, 10), func(t *testing.T) {
			assertProjectionOracle(t, facts)
		})
	}
}
