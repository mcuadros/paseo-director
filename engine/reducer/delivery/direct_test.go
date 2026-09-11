// SPDX-License-Identifier: Apache-2.0

package delivery

import (
	"strings"
	"testing"

	directdomain "github.com/mcuadros/director-engine/domain/directdelivery"
)

func directReducerState(t *testing.T, mode directdomain.IntegrationMode) directdomain.State {
	t.Helper()
	policy := directdomain.SealPolicy(directdomain.Policy{DeliveryMode: "direct", IntegrationMode: mode,
		SelectionSource: directdomain.SelectionFrozenRunConfiguration, ConfigurationSHA256: strings.Repeat("1", 64),
		AuthorizedTargetRefs: []string{"refs/heads/main"}, AttemptLimit: 2})
	if mode == directdomain.IntegrationAutomatic {
		policy.AutomaticTargetRefs = []string{"refs/heads/main"}
		policy = directdomain.SealPolicy(policy)
	}
	binding := directdomain.SealBinding(directdomain.Binding{TaskID: "task-1", RunID: "run-1", CandidateID: "candidate-1",
		CandidateSHA: strings.Repeat("a", 40), BaseSHA: strings.Repeat("b", 40), TreeSHA: strings.Repeat("c", 40),
		ManifestSHA256: strings.Repeat("2", 64), CandidateGeneration: 1, TaskVersion: 1,
		ConfigurationSHA256: policy.ConfigurationSHA256, RepositoryID: "repository-1",
		RepositoryBindingSHA256: strings.Repeat("3", 64), TargetRef: "refs/heads/main", PolicySHA256: policy.SHA256,
		ReviewEvidenceID: "review-evidence-1", ReviewerUUID: "11111111-1111-4111-8111-111111111111",
		CIObservationID: "ci-observation-1", CIObservationSHA256: strings.Repeat("4", 64), LeaseEpoch: 1})
	state, ok := directdomain.NewState(binding, policy)
	if !ok {
		t.Fatal("direct state rejected")
	}
	return state
}

func directReducerFacts(state directdomain.State) DirectFacts {
	return DirectFacts{SchemaVersion: DirectSchemaVersion, State: state, ProjectActive: true, LeaseCurrent: true,
		CandidateCurrent: true, ReviewApproved: true, CIPassed: true, CorrectionSettled: true, NowMillis: 1_000}
}

func TestReduceDirectManualWaitsForExplicitHumanAuthorization(t *testing.T) {
	state := directReducerState(t, directdomain.IntegrationManual)
	decision := ReduceDirect(directReducerFacts(state))
	if decision.Kind != DirectDecisionWaitHuman || decision.PushAuthorized || decision.CleanupAuthorized {
		t.Fatalf("manual decision = %#v", decision)
	}
	authorization := directdomain.SealAuthorization(directdomain.HumanAuthorization{ID: "authorization-1", ActorKind: "human",
		ActorSource: directdomain.AuthorizationActorSource, Authenticated: true,
		ActorID: "owner@example.invalid", DecisionID: "decision-1", Action: "integrate_direct",
		BindingSHA256: state.Binding.SHA256, CandidateSHA: state.Binding.CandidateSHA, BaseSHA: state.Binding.BaseSHA,
		TargetRef: state.Binding.TargetRef, PolicySHA256: state.Binding.PolicySHA256, AuthorizedAtMillis: 999})
	state, ok := directdomain.AuthorizeManual(state, authorization)
	if !ok || ReduceDirect(directReducerFacts(state)).Kind != DirectDecisionObserve {
		t.Fatalf("authorized manual state = %#v, ok=%v", state, ok)
	}
}

func TestReduceDirectAutomaticRequiresFreshExactRemoteFact(t *testing.T) {
	state := directReducerState(t, directdomain.IntegrationAutomatic)
	if decision := ReduceDirect(directReducerFacts(state)); decision.Kind != DirectDecisionObserve {
		t.Fatalf("initial automatic decision = %#v", decision)
	}
	observation := directdomain.SealObservation(directdomain.Observation{EffectID: state.Integration.ID,
		BindingSHA256: state.Binding.SHA256, Attempt: 0, Status: directdomain.ObservationCurrentExpected,
		Code: directdomain.CodeOK, RepositoryID: state.Binding.RepositoryID, TargetRef: state.Binding.TargetRef,
		CurrentSHA: state.Binding.BaseSHA, ObservedAtMillis: 990, MaximumAgeMillis: directdomain.MaximumObservationAgeMS})
	state, ok := directdomain.RecordObservation(state, observation)
	if !ok {
		t.Fatal("record exact remote observation")
	}
	decision := ReduceDirect(directReducerFacts(state))
	if decision.Kind != DirectDecisionDispatch || !decision.PushAuthorized || decision.CleanupAuthorized {
		t.Fatalf("exact remote decision = %#v", decision)
	}
	facts := directReducerFacts(state)
	facts.NowMillis += directdomain.MaximumObservationAgeMS + 1
	if decision = ReduceDirect(facts); decision.Kind != DirectDecisionObserve || decision.PushAuthorized {
		t.Fatalf("stale remote decision = %#v", decision)
	}
}

func TestReduceDirectNeverFallsBackFromPRAndFailsClosedOnDrift(t *testing.T) {
	state := directReducerState(t, directdomain.IntegrationAutomatic)
	facts := directReducerFacts(state)
	facts.PRPublicationStarted = true
	if decision := ReduceDirect(facts); decision.Kind != DirectDecisionEscalate || decision.Code != DirectCodePRFallbackForbidden || decision.PushAuthorized {
		t.Fatalf("PR fallback decision = %#v", decision)
	}
	facts = directReducerFacts(state)
	facts.CandidateCurrent = false
	if decision := ReduceDirect(facts); decision.Kind != DirectDecisionEscalate || decision.Code != DirectCodeCandidateStale {
		t.Fatalf("Candidate drift decision = %#v", decision)
	}
}
