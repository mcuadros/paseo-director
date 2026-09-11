// SPDX-License-Identifier: Apache-2.0

package projectionoracle

import (
	"encoding/json"
	"errors"
	"reflect"
	"slices"
	"testing"
)

func TestProjectCanonicalCorpus(t *testing.T) {
	tests := []struct {
		name        string
		facts       Facts
		state       State
		done        bool
		explanation ExplanationCode
		blockers    []BlockerCode
		attention   []AttentionCode
	}{
		{
			name: "dependency wait stays queued", facts: queuedFacts("dependency", EligibilityDependencyWait),
			state: StateQueued, explanation: ExplanationQueued,
			blockers: []BlockerCode{BlockerDependencyWait},
		},
		{
			name: "actionable escalation", facts: needsYouFacts("attention"),
			state: StateNeedsYou, explanation: ExplanationNeedsYou,
			attention: []AttentionCode{AttentionPermissionRequired},
		},
		{
			name: "active run", facts: buildingFacts("building"),
			state: StateBuilding, explanation: ExplanationBuilding,
		},
		{
			name: "validation pending after review", facts: validatingFacts("validating"),
			state: StateValidating, explanation: ExplanationValidating,
			blockers: []BlockerCode{BlockerValidationPending},
		},
		{
			name: "review pending after validation", facts: inReviewFacts("review"),
			state: StateInReview, explanation: ExplanationInReview,
			blockers: []BlockerCode{BlockerReviewPending},
		},
		{
			name: "ready for publication", facts: readyFacts("ready"),
			state: StateReady, explanation: ExplanationReady,
			blockers: []BlockerCode{BlockerDeliveryPending},
		},
		{
			name: "done filter membership", facts: doneFacts("done"),
			done: true, explanation: ExplanationDone,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got := Project(test.facts)
			if got.State != test.state || got.DoneMember != test.done || got.BoardMember == test.done ||
				got.Explanation != test.explanation || !slices.Equal(got.Blockers, test.blockers) ||
				!slices.Equal(got.Attention, test.attention) {
				t.Fatalf("Project() = %#v", got)
			}
			assertWellFormed(t, got)
		})
	}
}

func TestProjectAcceptsDurableTaskVersionZero(t *testing.T) {
	tests := []struct {
		name  string
		facts Facts
		state State
		done  bool
	}{
		{name: "queued", facts: queuedFacts("zero-queued", EligibilityEligible), state: StateQueued},
		{name: "building", facts: buildingFacts("zero-building"), state: StateBuilding},
		{name: "ready", facts: readyFacts("zero-ready"), state: StateReady},
		{name: "done", facts: doneFacts("zero-done"), done: true},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			facts := withTaskVersion(test.facts, 0)
			got := Project(facts)
			if got.State != test.state || got.DoneMember != test.done ||
				slices.Contains(got.Blockers, BlockerTaskFactContradictory) {
				t.Fatalf("version-zero Task projection = %#v", got)
			}
			assertWellFormed(t, got)
		})
	}
}

func TestProjectInvalidTaskIdentityCannotReachReadyOrDone(t *testing.T) {
	tests := []struct {
		name  string
		facts Facts
	}{
		{name: "ready", facts: readyFacts("invalid-ready")},
		{name: "done", facts: doneFacts("invalid-done")},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			facts := test.facts
			facts.TaskID = ""
			got := Project(facts)
			if got.State == StateReady || got.DoneMember ||
				!slices.Contains(got.Blockers, BlockerTaskFactContradictory) {
				t.Fatalf("invalid Task identity reached Ready/Done: %#v", got)
			}
		})
	}
}

func TestProjectNeedsYouRequiresAReconciledActionableFact(t *testing.T) {
	baseline := queuedFacts("task", EligibilityDependencyWait)
	cases := []struct {
		name   string
		change func(*Facts)
		block  BlockerCode
	}{
		{
			name: "eligibility escalation claim alone",
			change: func(facts *Facts) {
				facts.Eligibility.Decision = EligibilityEscalate
			},
			block: BlockerEscalationUnreconciled,
		},
		{
			name: "agent claim alone",
			change: func(facts *Facts) {
				facts.Claims = []ClaimFact{{
					Required: true, Status: FactCurrent, TaskVersion: facts.TaskVersion,
					Outcome: OutcomeNeedsHumanDecision,
				}}
			},
		},
		{
			name: "stale human fact",
			change: func(facts *Facts) {
				facts.HumanInput = actionableHumanFact(facts.TaskVersion)
				facts.HumanInput.Status = FactStale
			},
			block: BlockerHumanInputStale,
		},
		{
			name: "untyped human fact",
			change: func(facts *Facts) {
				facts.HumanInput = actionableHumanFact(facts.TaskVersion)
				facts.HumanInput.Code = "future_attention"
			},
			block: BlockerHumanInputContradictory,
		},
		{
			name: "missing wake condition",
			change: func(facts *Facts) {
				facts.HumanInput = actionableHumanFact(facts.TaskVersion)
				facts.HumanInput.WakeCondition = ""
			},
			block: BlockerHumanInputContradictory,
		},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			facts := cloneFacts(baseline)
			test.change(&facts)
			got := Project(facts)
			if got.State == StateNeedsYou || len(got.Attention) != 0 {
				t.Fatalf("unreconciled input projected Needs you: %#v", got)
			}
			if test.block != "" && !slices.Contains(got.Blockers, test.block) {
				t.Fatalf("blockers = %v, want %q", got.Blockers, test.block)
			}
		})
	}

	actionable := cloneFacts(baseline)
	actionable.HumanInput = actionableHumanFact(actionable.TaskVersion)
	got := Project(actionable)
	if got.State != StateNeedsYou || !slices.Equal(got.Attention, []AttentionCode{AttentionPermissionRequired}) {
		t.Fatalf("actionable human fact = %#v", got)
	}
}

func TestProjectValidationAndReviewAreSiblingObligations(t *testing.T) {
	failedWhileReviewPending := candidateFacts("review-after-failure")
	failedWhileReviewPending.Validation.Outcome = ValidationFailed
	failedWhileReviewPending.Review.Outcome = ReviewPending
	got := Project(failedWhileReviewPending)
	if got.State != StateInReview || !slices.Contains(got.Blockers, BlockerValidationFailed) ||
		!slices.Contains(got.Blockers, BlockerReviewPending) {
		t.Fatalf("failed Validation suppressed Review: %#v", got)
	}

	reviewedWhileValidationPending := candidateFacts("validation-after-review")
	reviewedWhileValidationPending.Validation.Outcome = ValidationPending
	reviewedWhileValidationPending.Review.Outcome = ReviewChangesRequested
	got = Project(reviewedWhileValidationPending)
	if got.State != StateValidating || !slices.Contains(got.Blockers, BlockerValidationPending) ||
		!slices.Contains(got.Blockers, BlockerReviewChangesRequested) {
		t.Fatalf("Review result suppressed Validation: %#v", got)
	}

	bothCompleteWithFindings := candidateFacts("correction")
	bothCompleteWithFindings.Validation.Outcome = ValidationFailed
	bothCompleteWithFindings.Review.Outcome = ReviewChangesRequested
	got = Project(bothCompleteWithFindings)
	if got.State != StateBuilding {
		t.Fatalf("batched correction projection = %#v", got)
	}
}

func TestProjectClaimsNeverCreateStateEvidence(t *testing.T) {
	tests := []struct {
		name   string
		status FactStatus
		block  BlockerCode
	}{
		{name: "missing", status: FactMissing, block: BlockerClaimMissing},
		{name: "stale", status: FactStale, block: BlockerClaimStale},
		{name: "contradictory", status: FactContradictory, block: BlockerClaimContradictory},
		{name: "unknown status", status: "future", block: BlockerClaimContradictory},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			facts := buildingFacts("claim-" + test.name)
			facts.Claims = []ClaimFact{{
				Required: true, Status: test.status, TaskVersion: facts.TaskVersion,
				RunID: facts.Run.ID, Outcome: OutcomeCompleted,
			}}
			got := Project(facts)
			if got.State != StateBuilding || got.DoneMember || got.State == StateNeedsYou ||
				!slices.Contains(got.Blockers, test.block) {
				t.Fatalf("claim changed evidence-derived state: %#v", got)
			}
		})
	}

	current := buildingFacts("current-claim")
	current.Claims = []ClaimFact{{
		Required: true, Status: FactCurrent, TaskVersion: current.TaskVersion,
		RunID: current.Run.ID, Outcome: OutcomeCompleted,
	}}
	if got := Project(current); got.State != StateBuilding || got.DoneMember {
		t.Fatalf("current completed claim fabricated Candidate/Done: %#v", got)
	}

	wrongBinding := readyFacts("wrong-binding")
	wrongBinding.Claims = []ClaimFact{{
		Required: true, Status: FactCurrent, TaskVersion: wrongBinding.TaskVersion,
		RunID: wrongBinding.Run.ID, CandidateID: "older-candidate", Outcome: OutcomeCompleted,
	}}
	got := Project(wrongBinding)
	if got.State == StateReady || got.DoneMember || !slices.Contains(got.Blockers, BlockerClaimContradictory) {
		t.Fatalf("wrong-bound claim did not fail closed: %#v", got)
	}
}

func TestProjectCandidateBearingClaimsRequireExactBindings(t *testing.T) {
	outcomes := []AgentOutcome{OutcomeCompleted, OutcomeNeedsValidation, OutcomeNeedsReview}
	stages := []struct {
		name  string
		facts func(string) Facts
	}{
		{name: "ready", facts: readyFacts},
		{name: "done", facts: doneFacts},
	}
	bindings := []struct {
		name   string
		mutate func(*ClaimFact)
	}{
		{name: "run omitted", mutate: func(claim *ClaimFact) { claim.RunID = "" }},
		{name: "run wrong", mutate: func(claim *ClaimFact) { claim.RunID = "another-run" }},
		{name: "candidate omitted", mutate: func(claim *ClaimFact) { claim.CandidateID = "" }},
		{name: "candidate wrong", mutate: func(claim *ClaimFact) { claim.CandidateID = "another-candidate" }},
	}

	for _, stage := range stages {
		for _, outcome := range outcomes {
			name := stage.name + "/" + string(outcome)
			t.Run(name+"/valid", func(t *testing.T) {
				facts := stage.facts("valid-bindings")
				facts.Claims = []ClaimFact{candidateClaim(facts, outcome)}
				got := Project(facts)
				if (stage.name == "ready" && got.State != StateReady) ||
					(stage.name == "done" && !got.DoneMember) {
					t.Fatalf("valid candidate-bearing claim failed: %#v", got)
				}
			})
			for _, binding := range bindings {
				t.Run(name+"/"+binding.name, func(t *testing.T) {
					facts := stage.facts("invalid-bindings")
					claim := candidateClaim(facts, outcome)
					binding.mutate(&claim)
					facts.Claims = []ClaimFact{claim}
					got := Project(facts)
					if got.State == StateReady || got.DoneMember ||
						!slices.Contains(got.Blockers, BlockerClaimContradictory) {
						t.Fatalf("invalid candidate-bearing claim reached Ready/Done: %#v", got)
					}
				})
			}
		}
	}
}

func TestProjectMissingHumanInputHasCompleteOrderedExplanation(t *testing.T) {
	facts := readyFacts("human-input-missing")
	facts.HumanInput = HumanInputFact{Status: FactMissing}
	want := Projection{
		State:       StateInReview,
		BoardMember: true,
		Explanation: ExplanationInReview,
		Blockers:    []BlockerCode{BlockerHumanInputMissing},
	}
	if got := Project(facts); !reflect.DeepEqual(got, want) {
		t.Fatalf("missing human-input projection = %#v, want %#v", got, want)
	}
}

func TestProjectCardMovementMetamorphicAndInputImmutability(t *testing.T) {
	if _, ok := reflect.TypeFor[Facts]().FieldByName("State"); ok {
		t.Fatal("Facts exposes an editable State field")
	}
	if _, ok := reflect.TypeFor[Facts]().FieldByName("CardState"); ok {
		t.Fatal("Facts exposes an editable CardState field")
	}

	facts := readyFacts("immutable")
	facts.Claims = []ClaimFact{{
		Required: true, Status: FactCurrent, TaskVersion: facts.TaskVersion,
		RunID: facts.Run.ID, CandidateID: facts.Candidate.ID, Outcome: OutcomeCompleted,
	}}
	beforeFacts := cloneFacts(facts)
	beforeProjection := Project(facts)
	for _, requestedCardState := range []State{
		StateNeedsYou, StateQueued, StateBuilding, StateValidating, StateInReview, StateReady,
	} {
		_ = requestedCardState // Presentation input is deliberately outside Facts.
		if got := Project(facts); !reflect.DeepEqual(got, beforeProjection) {
			t.Fatalf("presentation request changed projection: %#v", got)
		}
	}
	if !reflect.DeepEqual(facts, beforeFacts) {
		t.Fatalf("Project mutated facts:\n got  %#v\n want %#v", facts, beforeFacts)
	}
}

func TestProjectMutationGatesReadyAndDone(t *testing.T) {
	readyMutations := []struct {
		name   string
		mutate func(*Facts)
	}{
		{name: "candidate stale", mutate: func(f *Facts) { f.Candidate.Status = FactStale }},
		{name: "validation stale", mutate: func(f *Facts) { f.Validation.Status = FactStale }},
		{name: "review missing", mutate: func(f *Facts) { f.Review.Status = FactMissing }},
		{name: "feedback missing", mutate: func(f *Facts) { f.Feedback.Status = FactMissing }},
		{name: "delivery stale", mutate: func(f *Facts) { f.Delivery.Status = FactStale }},
		{name: "human state missing", mutate: func(f *Facts) { f.HumanInput.Status = FactMissing }},
		{name: "Task identity missing", mutate: func(f *Facts) { f.TaskID = "" }},
		{name: "candidate claim Run binding missing", mutate: func(f *Facts) {
			claim := candidateClaim(*f, OutcomeCompleted)
			claim.RunID = ""
			f.Claims = []ClaimFact{claim}
		}},
		{name: "candidate claim Candidate binding missing", mutate: func(f *Facts) {
			claim := candidateClaim(*f, OutcomeCompleted)
			claim.CandidateID = ""
			f.Claims = []ClaimFact{claim}
		}},
		{name: "required claim missing", mutate: func(f *Facts) {
			f.Claims = []ClaimFact{{Required: true, Status: FactMissing}}
		}},
	}
	for _, mutation := range readyMutations {
		t.Run("ready/"+mutation.name, func(t *testing.T) {
			facts := readyFacts("ready-mutation")
			mutation.mutate(&facts)
			got := Project(facts)
			if got.State == StateReady || got.DoneMember {
				t.Fatalf("mutation retained Ready/Done: %#v", got)
			}
		})
	}

	doneMutations := []struct {
		name   string
		mutate func(*Facts)
	}{
		{name: "task version", mutate: func(f *Facts) { f.TaskVersion++ }},
		{name: "active run", mutate: func(f *Facts) { f.Run.Active = true }},
		{name: "candidate binding", mutate: func(f *Facts) { f.Candidate.RunID = "another-run" }},
		{name: "validation failure", mutate: func(f *Facts) { f.Validation.Outcome = ValidationFailed }},
		{name: "review findings", mutate: func(f *Facts) { f.Review.Outcome = ReviewChangesRequested }},
		{name: "feedback", mutate: func(f *Facts) { f.Feedback.State = FeedbackActionable }},
		{name: "not integrated", mutate: func(f *Facts) { f.Delivery.State = DeliveryPublished }},
		{name: "cleanup pending", mutate: func(f *Facts) { f.Cleanup.State = CleanupPending }},
		{name: "terminal open", mutate: func(f *Facts) { f.Terminal.State = TerminalOpen }},
		{name: "stale terminal", mutate: func(f *Facts) { f.Terminal.Status = FactStale }},
		{name: "Task identity missing", mutate: func(f *Facts) { f.TaskID = "" }},
		{name: "candidate claim Run binding missing", mutate: func(f *Facts) {
			claim := candidateClaim(*f, OutcomeCompleted)
			claim.RunID = ""
			f.Claims = []ClaimFact{claim}
		}},
		{name: "candidate claim Candidate binding missing", mutate: func(f *Facts) {
			claim := candidateClaim(*f, OutcomeCompleted)
			claim.CandidateID = ""
			f.Claims = []ClaimFact{claim}
		}},
		{name: "required claim missing", mutate: func(f *Facts) {
			f.Claims = []ClaimFact{{Required: true, Status: FactMissing}}
		}},
	}
	for _, mutation := range doneMutations {
		t.Run("done/"+mutation.name, func(t *testing.T) {
			facts := doneFacts("done-mutation")
			mutation.mutate(&facts)
			got := Project(facts)
			if got.DoneMember || got.Explanation == ExplanationDone {
				t.Fatalf("mutation retained Done: %#v", got)
			}
		})
	}
}

func TestProjectExhaustiveBoundedQualityMatrix(t *testing.T) {
	statuses := []FactStatus{FactMissing, FactCurrent, FactStale, FactContradictory}
	eligibility := []EligibilityDecision{
		EligibilityEligible, EligibilityDependencyWait, EligibilityPolicyWait,
		EligibilityCapacityWait, EligibilityExternalWait, EligibilityEscalate,
	}
	validations := []ValidationOutcome{ValidationPending, ValidationRunning, ValidationPassed, ValidationFailed}
	reviews := []ReviewOutcome{ReviewPending, ReviewRunning, ReviewApproved, ReviewChangesRequested, ReviewNeedsHuman}
	humans := []HumanInputState{HumanInputNone, HumanInputPending, HumanInputResolved}

	cases := 0
	for _, eligibilityStatus := range statuses {
		for _, decision := range eligibility {
			for _, validation := range validations {
				for _, review := range reviews {
					for _, human := range humans {
						facts := candidateFacts("matrix")
						facts.Eligibility.Status = eligibilityStatus
						facts.Eligibility.Decision = decision
						facts.Validation.Outcome = validation
						facts.Review.Outcome = review
						facts.HumanInput.State = human
						if human == HumanInputPending {
							facts.HumanInput.Code = AttentionCredentialRequired
							facts.HumanInput.WakeCondition = "credential observation is current"
						}
						got := Project(facts)
						assertWellFormed(t, got)
						if (got.State == StateNeedsYou) != (human == HumanInputPending) {
							t.Fatalf("Needs you mismatch for %#v: %#v", facts, got)
						}
						if got.DoneMember {
							t.Fatalf("open matrix case reached Done: %#v", got)
						}
						cases++
					}
				}
			}
		}
	}
	if cases != 1440 {
		t.Fatalf("matrix cases = %d", cases)
	}
}

func TestProjectSeededPropertyAndRepeatability(t *testing.T) {
	random := newDeterministicRand(0xD1EC70)
	statuses := []FactStatus{FactMissing, FactCurrent, FactStale, FactContradictory, "future"}
	validations := []ValidationOutcome{"future", ValidationPending, ValidationRunning, ValidationPassed, ValidationFailed}
	reviews := []ReviewOutcome{"future", ReviewPending, ReviewRunning, ReviewApproved, ReviewChangesRequested, ReviewNeedsHuman}
	feedbacks := []FeedbackState{"future", FeedbackNone, FeedbackActionable, FeedbackResolved}
	deliveries := []DeliveryState{"future", DeliveryPendingPublication, DeliveryPublished, DeliveryIntegrated}
	cleanups := []CleanupState{"future", CleanupPending, CleanupComplete}
	terminals := []TerminalState{"future", TerminalOpen, TerminalDone}
	eligibilities := []EligibilityDecision{"future", EligibilityEligible, EligibilityDependencyWait, EligibilityPolicyWait, EligibilityCapacityWait, EligibilityExternalWait, EligibilityEscalate}
	outcomes := []AgentOutcome{"future", OutcomeCompleted, OutcomeNeedsValidation, OutcomeNeedsReview, OutcomeNeedsHumanDecision, OutcomeBlockedByDependency, OutcomeBlockedByAccess, OutcomeBudgetExhausted}

	for index := range 10000 {
		facts := candidateFacts("property")
		if random.intn(8) == 0 {
			facts = withTaskVersion(facts, 0)
		}
		if random.intn(16) == 0 {
			facts.TaskID = ""
		}
		facts.Eligibility.Status = statuses[random.intn(len(statuses))]
		facts.Eligibility.Decision = eligibilities[random.intn(len(eligibilities))]
		facts.Run.Status = statuses[random.intn(len(statuses))]
		facts.Run.Active = random.intn(2) == 0
		facts.Candidate.Status = statuses[random.intn(len(statuses))]
		facts.Validation.Status = statuses[random.intn(len(statuses))]
		facts.Validation.Outcome = validations[random.intn(len(validations))]
		facts.Review.Status = statuses[random.intn(len(statuses))]
		facts.Review.Outcome = reviews[random.intn(len(reviews))]
		facts.Feedback.Status = statuses[random.intn(len(statuses))]
		facts.Feedback.State = feedbacks[random.intn(len(feedbacks))]
		facts.Delivery.Status = statuses[random.intn(len(statuses))]
		facts.Delivery.State = deliveries[random.intn(len(deliveries))]
		facts.Cleanup.Status = statuses[random.intn(len(statuses))]
		facts.Cleanup.State = cleanups[random.intn(len(cleanups))]
		facts.Terminal.Status = statuses[random.intn(len(statuses))]
		facts.Terminal.State = terminals[random.intn(len(terminals))]
		facts.HumanInput.Status = statuses[random.intn(len(statuses))]
		facts.HumanInput.State = HumanInputState([]string{"future", string(HumanInputNone), string(HumanInputPending), string(HumanInputResolved)}[random.intn(4)])
		if random.intn(2) == 0 {
			facts.HumanInput.Code = AttentionCode([]string{"future", string(AttentionPermissionRequired), string(AttentionHardBudgetExhausted)}[random.intn(3)])
			facts.HumanInput.WakeCondition = []string{"", "fresh authorized command"}[random.intn(2)]
		}
		if random.intn(2) == 0 {
			facts.Claims = []ClaimFact{{
				Required: random.intn(2) == 0, Status: statuses[random.intn(len(statuses))],
				TaskVersion: facts.TaskVersion,
				RunID:       []string{"", facts.Run.ID, "another-run"}[random.intn(3)],
				CandidateID: []string{"", facts.Candidate.ID, "another-candidate"}[random.intn(3)],
				Outcome:     outcomes[random.intn(len(outcomes))],
			}}
		}

		got := Project(facts)
		assertWellFormed(t, got)
		if again := Project(facts); !reflect.DeepEqual(got, again) {
			t.Fatalf("case %d is not repeatable:\nfirst  %#v\nsecond %#v", index, got, again)
		}
		if got.State == StateNeedsYou && !isActionableHuman(facts) {
			t.Fatalf("case %d reached Needs you without actionable input: %#v", index, facts)
		}
		if got.DoneMember && !doneInputsProve(facts) {
			t.Fatalf("case %d reached Done without complete proof: %#v", index, facts)
		}
		if (got.State == StateReady || got.DoneMember) && facts.TaskID == "" {
			t.Fatalf("case %d reached Ready/Done without Task identity: %#v", index, facts)
		}
		if (got.State == StateReady || got.DoneMember) && !candidateBearingClaimsBound(facts) {
			t.Fatalf("case %d reached Ready/Done with an invalid candidate-bearing claim: %#v", index, facts)
		}
	}
}

func TestEveryDeclaredBlockerCodeIsReachable(t *testing.T) {
	seen := make(map[BlockerCode]bool, len(blockerCodeOrder))
	observe := func(facts Facts) {
		for _, blocker := range Project(facts).Blockers {
			seen[blocker] = true
		}
	}

	invalidTask := readyFacts("invalid-task")
	invalidTask.TaskID = ""
	observe(invalidTask)
	for _, status := range []FactStatus{FactMissing, FactStale, FactContradictory} {
		facts := queuedFacts("eligibility-status", EligibilityEligible)
		facts.Eligibility.Status = status
		observe(facts)
	}
	for _, decision := range []EligibilityDecision{
		EligibilityDependencyWait, EligibilityPolicyWait, EligibilityCapacityWait,
		EligibilityExternalWait, EligibilityEscalate,
	} {
		observe(queuedFacts("eligibility-decision", decision))
	}
	for _, status := range []FactStatus{FactStale, FactContradictory} {
		facts := buildingFacts("run-status")
		facts.Run.Status = status
		observe(facts)
		facts = candidateFacts("candidate-status")
		facts.Candidate.Status = status
		observe(facts)
	}
	for _, status := range []FactStatus{FactMissing, FactStale, FactContradictory} {
		facts := readyFacts("claim-status")
		facts.Claims = []ClaimFact{{Required: true, Status: status}}
		observe(facts)

		facts = candidateFacts("validation-status")
		facts.Validation.Status = status
		observe(facts)
		facts = candidateFacts("review-status")
		facts.Review.Status = status
		observe(facts)
		facts = candidateFacts("feedback-status")
		facts.Feedback.Status = status
		observe(facts)
		facts = candidateFacts("delivery-status")
		facts.Delivery.Status = status
		observe(facts)
	}
	invalidClaim := readyFacts("claim-binding")
	invalidClaim.Claims = []ClaimFact{candidateClaim(invalidClaim, OutcomeCompleted)}
	invalidClaim.Claims[0].CandidateID = "wrong-candidate"
	observe(invalidClaim)

	for _, outcome := range []ValidationOutcome{ValidationPending, ValidationFailed} {
		facts := candidateFacts("validation-outcome")
		facts.Validation.Outcome = outcome
		observe(facts)
	}
	for _, reason := range []string{"VALIDATION_BASE_CHANGED", "VALIDATION_UNAVAILABLE", "VALIDATION_WORKFLOW_AMBIGUOUS"} {
		facts := candidateFacts("validation-reason")
		facts.Validation.Reason = reason
		observe(facts)
	}
	for _, outcome := range []ReviewOutcome{ReviewPending, ReviewChangesRequested, ReviewNeedsHuman} {
		facts := candidateFacts("review-outcome")
		facts.Review.Outcome = outcome
		observe(facts)
	}
	actionableFeedback := candidateFacts("feedback-actionable")
	actionableFeedback.Feedback.State = FeedbackActionable
	observe(actionableFeedback)

	observe(readyFacts("delivery-pending"))
	published := readyFacts("integration-pending")
	published.Delivery.State = DeliveryPublished
	observe(published)
	for _, status := range []FactStatus{FactMissing, FactStale, FactContradictory} {
		facts := readyFacts("cleanup-status")
		facts.Delivery.State = DeliveryIntegrated
		facts.Cleanup.Status = status
		observe(facts)
	}
	cleanupPending := readyFacts("cleanup-pending")
	cleanupPending.Delivery.State = DeliveryIntegrated
	cleanupPending.Cleanup = CleanupFact{Status: FactCurrent, RunID: cleanupPending.Run.ID, State: CleanupPending}
	observe(cleanupPending)

	for _, status := range []FactStatus{FactMissing, FactStale, FactContradictory} {
		facts := readyFacts("human-status")
		facts.HumanInput.Status = status
		observe(facts)
	}
	for _, status := range []FactStatus{FactStale, FactContradictory} {
		facts := doneFacts("terminal-status")
		facts.Terminal.Status = status
		observe(facts)
	}
	terminalPending := doneFacts("terminal-pending")
	terminalPending.Terminal.State = TerminalOpen
	observe(terminalPending)
	terminalDoneUnproved := readyFacts("terminal-done-unproved")
	terminalDoneUnproved.Terminal.State = TerminalDone
	observe(terminalDoneUnproved)

	var missing []BlockerCode
	for _, blocker := range blockerCodeOrder {
		if !seen[blocker] {
			missing = append(missing, blocker)
		}
	}
	if len(missing) != 0 {
		t.Fatalf("declared blocker codes are unreachable: %v", missing)
	}
}

func TestProjectStableOrderedCodes(t *testing.T) {
	facts := candidateFacts("codes")
	facts.TaskVersion++
	facts.Eligibility.Status = FactContradictory
	facts.Run.Status = FactStale
	facts.Candidate.Status = FactStale
	facts.Validation.Status = FactContradictory
	facts.Review.Status = FactStale
	facts.Feedback.Status = FactContradictory
	facts.Delivery.Status = FactStale
	facts.Cleanup.Status = FactContradictory
	facts.HumanInput.Status = FactStale
	facts.Terminal.Status = FactContradictory
	facts.Claims = []ClaimFact{
		{Required: true, Status: FactMissing},
		{Required: true, Status: FactStale},
	}

	first := Project(facts)
	assertWellFormed(t, first)
	encoded, err := json.Marshal(first)
	if err != nil {
		t.Fatal(err)
	}
	for range 1000 {
		got, err := json.Marshal(Project(facts))
		if err != nil || !slices.Equal(got, encoded) {
			t.Fatalf("projection bytes changed: %q, %v", got, err)
		}
	}

	blockers := BlockerCodeOrder()
	blockers[0] = "mutated"
	if BlockerCodeOrder()[0] == "mutated" {
		t.Fatal("BlockerCodeOrder returned mutable shared storage")
	}
	attention := AttentionCodeOrder()
	attention[0] = "mutated"
	if AttentionCodeOrder()[0] == "mutated" {
		t.Fatal("AttentionCodeOrder returned mutable shared storage")
	}
}

func TestOrderedAndCursorFixturesAreDeterministic(t *testing.T) {
	fixtures := []Fixture{
		fixture(needsYouFacts("needs"), PriorityLow, 70),
		fixture(queuedFacts("queued-low", EligibilityDependencyWait), PriorityLow, 10),
		fixture(queuedFacts("queued-urgent-b", EligibilityDependencyWait), PriorityUrgent, 20),
		fixture(queuedFacts("queued-urgent-a", EligibilityDependencyWait), PriorityUrgent, 20),
		fixture(buildingFacts("building"), PriorityNormal, 40),
		fixture(validatingFacts("validating"), PriorityNormal, 30),
		fixture(inReviewFacts("review"), PriorityHigh, 20),
		fixture(readyFacts("ready"), PriorityUrgent, 10),
		fixture(doneFacts("done"), PriorityUrgent, 1),
	}
	want := []string{
		"needs", "queued-urgent-a", "queued-urgent-b", "queued-low", "building",
		"validating", "review", "ready", "done",
	}

	ordered, err := Ordered(fixtures)
	if err != nil {
		t.Fatal(err)
	}
	if got := taskIDs(ordered); !slices.Equal(got, want) {
		t.Fatalf("order = %v, want %v", got, want)
	}

	wantPages, wantCursors, err := collectPages(fixtures, 9007199254740993, 3)
	if err != nil || !slices.Equal(wantPages, want) || len(wantCursors) != 2 {
		t.Fatalf("pages = %v, cursors = %v, error = %v", wantPages, wantCursors, err)
	}

	random := newDeterministicRand(1616)
	for range 250 {
		permuted := append([]Fixture(nil), fixtures...)
		random.shuffle(len(permuted), func(left, right int) {
			permuted[left], permuted[right] = permuted[right], permuted[left]
		})
		gotPages, gotCursors, err := collectPages(permuted, 9007199254740993, 3)
		if err != nil || !slices.Equal(gotPages, wantPages) || !slices.Equal(gotCursors, wantCursors) {
			t.Fatalf("permutation changed pages/cursors: %v %v %v", gotPages, gotCursors, err)
		}
	}

	first, err := Paginate(fixtures, 44, 3, "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Paginate(fixtures, 45, 3, first.NextCursor); !errors.Is(err, ErrCursorSnapshot) {
		t.Fatalf("snapshot mismatch error = %v", err)
	}
	if _, err := Paginate(fixtures, 44, 3, "not-a-cursor"); !errors.Is(err, ErrInvalidCursor) {
		t.Fatalf("invalid cursor error = %v", err)
	}
}

func TestOrderedAndPaginateRejectInvalidInputsWithoutMutation(t *testing.T) {
	fixtures := []Fixture{
		fixture(readyFacts("b"), PriorityHigh, 2),
		fixture(readyFacts("a"), PriorityHigh, 1),
	}
	before := cloneFixtures(fixtures)
	if _, err := Paginate(fixtures, 1, 0, ""); !errors.Is(err, ErrInvalidPageSize) {
		t.Fatalf("zero page error = %v", err)
	}
	if _, err := Paginate(fixtures, 1, MaximumPageSize+1, ""); !errors.Is(err, ErrInvalidPageSize) {
		t.Fatalf("large page error = %v", err)
	}
	duplicate := append(cloneFixtures(fixtures), fixtures[0])
	if _, err := Ordered(duplicate); !errors.Is(err, ErrInvalidFixture) {
		t.Fatalf("duplicate error = %v", err)
	}
	invalidPriority := cloneFixtures(fixtures)
	invalidPriority[0].Priority = "critical"
	if _, err := Ordered(invalidPriority); !errors.Is(err, ErrInvalidFixture) {
		t.Fatalf("priority error = %v", err)
	}
	if !reflect.DeepEqual(fixtures, before) {
		t.Fatalf("ordering mutated fixtures:\n got  %#v\n want %#v", fixtures, before)
	}

	page, err := Paginate(fixtures, 1, 1, "")
	if err != nil {
		t.Fatal(err)
	}
	page.Tasks[0].Projection.Blockers[0] = "mutated"
	again, err := Paginate(fixtures, 1, 1, "")
	if err != nil || slices.Contains(again.Tasks[0].Projection.Blockers, BlockerCode("mutated")) {
		t.Fatalf("page leaked mutable storage: %#v, %v", again, err)
	}
}

func assertWellFormed(t *testing.T, projection Projection) {
	t.Helper()
	if projection.BoardMember == projection.DoneMember {
		t.Fatalf("membership is not exclusive: %#v", projection)
	}
	if projection.DoneMember {
		if projection.State != "" || projection.Explanation != ExplanationDone {
			t.Fatalf("Done projection is malformed: %#v", projection)
		}
	} else if !knownState(projection.State) || projection.Explanation != explanationFor(projection.State, false) {
		t.Fatalf("Board projection is malformed: %#v", projection)
	}
	if (projection.State == StateNeedsYou) != (len(projection.Attention) > 0) {
		t.Fatalf("attention is not exclusive to Needs you: %#v", projection)
	}
	assertOrderedUnique(t, projection.Blockers, blockerCodeOrder[:])
	assertOrderedUnique(t, projection.Attention, attentionCodeOrder[:])
}

func assertOrderedUnique[T comparable](t *testing.T, actual []T, canonical []T) {
	t.Helper()
	prior := -1
	seen := make(map[T]bool, len(actual))
	for _, value := range actual {
		index := slices.Index(canonical, value)
		if index < 0 || index <= prior || seen[value] {
			t.Fatalf("codes are not canonical and unique: %v", actual)
		}
		seen[value] = true
		prior = index
	}
}

func queuedFacts(taskID string, decision EligibilityDecision) Facts {
	const version = 7
	return Facts{
		TaskID: taskID, TaskVersion: version,
		Eligibility: EligibilityFact{Status: FactCurrent, TaskVersion: version, Decision: decision},
		Run:         RunFact{Status: FactMissing},
		Candidate:   CandidateFact{Status: FactMissing},
		Validation:  ValidationFact{Status: FactMissing},
		Review:      ReviewFact{Status: FactMissing},
		Feedback:    FeedbackFact{Status: FactMissing},
		Delivery:    DeliveryFact{Status: FactMissing},
		Cleanup:     CleanupFact{Status: FactMissing},
		HumanInput:  HumanInputFact{Status: FactCurrent, TaskVersion: version, State: HumanInputNone},
		Terminal:    TerminalFact{Status: FactCurrent, TaskVersion: version, State: TerminalOpen},
	}
}

func needsYouFacts(taskID string) Facts {
	facts := queuedFacts(taskID, EligibilityEscalate)
	facts.HumanInput = actionableHumanFact(facts.TaskVersion)
	return facts
}

func actionableHumanFact(version uint64) HumanInputFact {
	return HumanInputFact{
		Status: FactCurrent, TaskVersion: version, State: HumanInputPending,
		Code: AttentionPermissionRequired, WakeCondition: "authorized permission observation is current",
	}
}

func buildingFacts(taskID string) Facts {
	facts := queuedFacts(taskID, EligibilityEligible)
	facts.Run = RunFact{Status: FactCurrent, TaskVersion: facts.TaskVersion, ID: "run-" + taskID, Active: true}
	return facts
}

func candidateFacts(taskID string) Facts {
	facts := buildingFacts(taskID)
	facts.Candidate = CandidateFact{
		Status: FactCurrent, TaskVersion: facts.TaskVersion,
		ID: "candidate-" + taskID, RunID: facts.Run.ID,
	}
	facts.Validation = ValidationFact{Status: FactCurrent, CandidateID: facts.Candidate.ID, Outcome: ValidationPending}
	facts.Review = ReviewFact{Status: FactCurrent, CandidateID: facts.Candidate.ID, Outcome: ReviewPending}
	facts.Feedback = FeedbackFact{Status: FactCurrent, CandidateID: facts.Candidate.ID, State: FeedbackNone}
	facts.Delivery = DeliveryFact{Status: FactCurrent, CandidateID: facts.Candidate.ID, State: DeliveryPendingPublication}
	return facts
}

func validatingFacts(taskID string) Facts {
	facts := candidateFacts(taskID)
	facts.Validation.Outcome = ValidationPending
	facts.Review.Outcome = ReviewApproved
	return facts
}

func inReviewFacts(taskID string) Facts {
	facts := candidateFacts(taskID)
	facts.Validation.Outcome = ValidationPassed
	facts.Review.Outcome = ReviewPending
	return facts
}

func readyFacts(taskID string) Facts {
	facts := candidateFacts(taskID)
	facts.Validation.Outcome = ValidationPassed
	facts.Review.Outcome = ReviewApproved
	return facts
}

func doneFacts(taskID string) Facts {
	facts := readyFacts(taskID)
	facts.Run.Active = false
	facts.Delivery.State = DeliveryIntegrated
	facts.Cleanup = CleanupFact{Status: FactCurrent, RunID: facts.Run.ID, State: CleanupComplete}
	facts.Terminal.State = TerminalDone
	return facts
}

func cloneFacts(facts Facts) Facts {
	result := facts
	result.Claims = append([]ClaimFact(nil), facts.Claims...)
	return result
}

func cloneFixtures(fixtures []Fixture) []Fixture {
	result := append([]Fixture(nil), fixtures...)
	for index := range result {
		result[index].Facts = cloneFacts(result[index].Facts)
	}
	return result
}

func fixture(facts Facts, priority Priority, queuedAt int64) Fixture {
	return Fixture{Facts: facts, Priority: priority, QueuedAtUnixMillis: queuedAt}
}

func taskIDs(tasks []ExpectedTask) []string {
	result := make([]string, len(tasks))
	for index, task := range tasks {
		result[index] = task.TaskID
	}
	return result
}

func collectPages(fixtures []Fixture, snapshot uint64, limit int) ([]string, []string, error) {
	var ids []string
	var cursors []string
	after := ""
	for {
		page, err := Paginate(fixtures, snapshot, limit, after)
		if err != nil {
			return nil, nil, err
		}
		ids = append(ids, taskIDs(page.Tasks)...)
		if page.NextCursor == "" {
			return ids, cursors, nil
		}
		cursors = append(cursors, page.NextCursor)
		after = page.NextCursor
	}
}

func isActionableHuman(facts Facts) bool {
	fact := facts.HumanInput
	return fact.Status == FactCurrent && fact.TaskVersion == facts.TaskVersion &&
		fact.State == HumanInputPending && knownAttention(fact.Code) && fact.WakeCondition != ""
}

func doneInputsProve(facts Facts) bool {
	return facts.TaskID != "" &&
		facts.Run.Status == FactCurrent && facts.Run.TaskVersion == facts.TaskVersion && facts.Run.ID != "" && !facts.Run.Active &&
		facts.Candidate.Status == FactCurrent && facts.Candidate.TaskVersion == facts.TaskVersion && facts.Candidate.ID != "" && facts.Candidate.RunID == facts.Run.ID &&
		facts.Validation.Status == FactCurrent && facts.Validation.CandidateID == facts.Candidate.ID && facts.Validation.Outcome == ValidationPassed &&
		facts.Review.Status == FactCurrent && facts.Review.CandidateID == facts.Candidate.ID && facts.Review.Outcome == ReviewApproved &&
		facts.Feedback.Status == FactCurrent && facts.Feedback.CandidateID == facts.Candidate.ID && (facts.Feedback.State == FeedbackNone || facts.Feedback.State == FeedbackResolved) &&
		facts.Delivery.Status == FactCurrent && facts.Delivery.CandidateID == facts.Candidate.ID && facts.Delivery.State == DeliveryIntegrated &&
		facts.Cleanup.Status == FactCurrent && facts.Cleanup.RunID == facts.Run.ID && facts.Cleanup.State == CleanupComplete &&
		facts.HumanInput.Status == FactCurrent && facts.HumanInput.TaskVersion == facts.TaskVersion && (facts.HumanInput.State == HumanInputNone || facts.HumanInput.State == HumanInputResolved) &&
		facts.Terminal.Status == FactCurrent && facts.Terminal.TaskVersion == facts.TaskVersion && facts.Terminal.State == TerminalDone &&
		candidateBearingClaimsBound(facts)
}

func withTaskVersion(facts Facts, version uint64) Facts {
	facts = cloneFacts(facts)
	facts.TaskVersion = version
	facts.Eligibility.TaskVersion = version
	facts.Run.TaskVersion = version
	facts.Candidate.TaskVersion = version
	facts.HumanInput.TaskVersion = version
	facts.Terminal.TaskVersion = version
	for index := range facts.Claims {
		facts.Claims[index].TaskVersion = version
	}
	return facts
}

func candidateClaim(facts Facts, outcome AgentOutcome) ClaimFact {
	return ClaimFact{
		Required: true, Status: FactCurrent, TaskVersion: facts.TaskVersion,
		RunID: facts.Run.ID, CandidateID: facts.Candidate.ID, Outcome: outcome,
	}
}

func candidateBearingClaimsBound(facts Facts) bool {
	for _, claim := range facts.Claims {
		if !claim.Required || !isCandidateBearingOutcome(claim.Outcome) {
			continue
		}
		if claim.Status != FactCurrent || claim.TaskVersion != facts.TaskVersion ||
			facts.Run.Status != FactCurrent || claim.RunID == "" || claim.RunID != facts.Run.ID ||
			facts.Candidate.Status != FactCurrent || claim.CandidateID == "" || claim.CandidateID != facts.Candidate.ID {
			return false
		}
	}
	return true
}

type deterministicRand struct {
	state uint64
}

func newDeterministicRand(seed uint64) *deterministicRand {
	return &deterministicRand{state: seed}
}

func (random *deterministicRand) intn(limit int) int {
	if limit <= 0 {
		panic("deterministic random limit must be positive")
	}
	random.state += 0x9e3779b97f4a7c15
	value := random.state
	value = (value ^ (value >> 30)) * 0xbf58476d1ce4e5b9
	value = (value ^ (value >> 27)) * 0x94d049bb133111eb
	value ^= value >> 31
	return int(value % uint64(limit))
}

func (random *deterministicRand) shuffle(length int, swap func(int, int)) {
	for left := length - 1; left > 0; left-- {
		swap(left, random.intn(left+1))
	}
}
