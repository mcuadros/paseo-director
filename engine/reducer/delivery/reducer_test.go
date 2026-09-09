// SPDX-License-Identifier: Apache-2.0

package delivery

import (
	"reflect"
	"strings"
	"testing"
)

func admittedFacts() Facts {
	return Facts{
		SchemaVersion: SchemaVersion, CandidateAdmitted: true,
		CandidateSHA: strings.Repeat("a", 40), BaseSHA: strings.Repeat("b", 40),
		Draft: Obligation{Phase: PhaseAbsent}, RemoteCI: Obligation{Phase: PhaseAbsent},
		Review: Obligation{Phase: PhaseAbsent},
	}
}

func bound(phase Phase, candidate string) Obligation {
	return Obligation{Phase: phase, CandidateSHA: candidate}
}

func TestReducePublishesDraftBeforeDispatchingCIAndReviewTogether(t *testing.T) {
	facts := admittedFacts()
	decision := Reduce(facts)
	if decision.Kind != DecisionPublishDraft ||
		!reflect.DeepEqual(decision.Effects, []EffectKind{EffectPublishDraft}) ||
		decision.MergeAuthorized || decision.CleanupAuthorized {
		t.Fatalf("initial decision = %#v", decision)
	}

	facts.Draft = bound(PhaseIntent, facts.CandidateSHA)
	if decision := Reduce(facts); decision.Kind != DecisionReconcileDraft || len(decision.Effects) != 0 {
		t.Fatalf("unproven draft decision = %#v", decision)
	}

	facts.Draft = bound(PhaseComplete, facts.CandidateSHA)
	decision = Reduce(facts)
	if decision.Kind != DecisionDispatchSibling ||
		!reflect.DeepEqual(decision.Effects, []EffectKind{EffectObserveRemoteCI, EffectIndependentReview}) ||
		decision.MergeAuthorized || decision.CleanupAuthorized {
		t.Fatalf("post-draft decision = %#v", decision)
	}
}

func TestReduceRecoveryNeverRepeatsARecordedSiblingIntent(t *testing.T) {
	facts := admittedFacts()
	facts.Draft = bound(PhaseComplete, facts.CandidateSHA)
	facts.RemoteCI = bound(PhaseIntent, facts.CandidateSHA)
	decision := Reduce(facts)
	if decision.Kind != DecisionDispatchSibling ||
		!reflect.DeepEqual(decision.Effects, []EffectKind{EffectIndependentReview}) {
		t.Fatalf("missing Review recovery = %#v", decision)
	}

	facts.Review = bound(PhaseDispatching, facts.CandidateSHA)
	if decision := Reduce(facts); decision.Kind != DecisionWaitSibling || len(decision.Effects) != 0 {
		t.Fatalf("recorded siblings = %#v", decision)
	}
	facts.RemoteCI = bound(PhaseComplete, facts.CandidateSHA)
	facts.Review = bound(PhaseComplete, facts.CandidateSHA)
	if decision := Reduce(facts); decision.Kind != DecisionEvaluateGates ||
		decision.MergeAuthorized || decision.CleanupAuthorized {
		t.Fatalf("completed siblings = %#v", decision)
	}
}

func TestReduceFailsClosedForBlockedTasksAndStaleCandidateBindings(t *testing.T) {
	facts := admittedFacts()
	facts.TaskBlocked = true
	if decision := Reduce(facts); decision.Kind != DecisionEscalate || decision.Code != CodeTaskBlocked {
		t.Fatalf("blocked Task decision = %#v", decision)
	}

	facts = admittedFacts()
	facts.Draft = bound(PhaseComplete, strings.Repeat("c", 40))
	if decision := Reduce(facts); decision.Kind != DecisionEscalate ||
		decision.Code != CodeBindingMismatch || decision.CleanupAuthorized {
		t.Fatalf("stale Candidate binding = %#v", decision)
	}

	facts = admittedFacts()
	facts.Draft.Phase = "unknown"
	if decision := Reduce(facts); decision.Kind != DecisionEscalate || decision.Code != CodePhaseInvalid {
		t.Fatalf("unknown phase = %#v", decision)
	}
}
