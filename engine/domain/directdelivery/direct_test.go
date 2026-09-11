// SPDX-License-Identifier: Apache-2.0

package directdelivery

import (
	"encoding/json"
	"strings"
	"testing"
)

func testPolicy(mode IntegrationMode) Policy {
	policy := Policy{DeliveryMode: "direct", IntegrationMode: mode, SelectionSource: SelectionFrozenRunConfiguration,
		ConfigurationSHA256: strings.Repeat("1", 64), AuthorizedTargetRefs: []string{"refs/heads/main"}, AttemptLimit: 2}
	if mode == IntegrationAutomatic {
		policy.AutomaticTargetRefs = []string{"refs/heads/main"}
	}
	return SealPolicy(policy)
}

func testBinding(policy Policy) Binding {
	return SealBinding(Binding{TaskID: "task-1", RunID: "run-1", CandidateID: "candidate-1",
		CandidateSHA: strings.Repeat("a", 40), BaseSHA: strings.Repeat("b", 40), TreeSHA: strings.Repeat("c", 40),
		ManifestSHA256: strings.Repeat("2", 64), CandidateGeneration: 1, TaskVersion: 3,
		ConfigurationSHA256: policy.ConfigurationSHA256, RepositoryID: "repository-1",
		RepositoryBindingSHA256: strings.Repeat("3", 64), TargetRef: "refs/heads/main", PolicySHA256: policy.SHA256,
		ReviewEvidenceID: "review-evidence-1", ReviewerUUID: "11111111-1111-4111-8111-111111111111",
		CIObservationID: "ci-observation-1", CIObservationSHA256: strings.Repeat("4", 64), LeaseEpoch: 7})
}

func testAuthorization(binding Binding) HumanAuthorization {
	return SealAuthorization(HumanAuthorization{ID: "authorization-1", ActorKind: "human", ActorID: "owner@example.invalid",
		DecisionID: "decision-1", Action: "integrate_direct", BindingSHA256: binding.SHA256,
		CandidateSHA: binding.CandidateSHA, BaseSHA: binding.BaseSHA, TargetRef: binding.TargetRef,
		PolicySHA256: binding.PolicySHA256, AuthorizedAtMillis: 1_000})
}

func testObservation(state State, status ObservationStatus, current string, observedAt int64) Observation {
	code := CodeOK
	if status == ObservationAbsent {
		code = CodeRemoteRefAbsent
	}
	if status == ObservationDifferent {
		code = CodeRemoteRefChanged
	}
	if status == ObservationUnavailable {
		code = CodeRemoteUnavailable
	}
	return SealObservation(Observation{EffectID: state.Integration.ID, BindingSHA256: state.Binding.SHA256,
		Attempt: state.Integration.Attempt, Status: status, Code: code, RepositoryID: state.Binding.RepositoryID,
		TargetRef: state.Binding.TargetRef, CurrentSHA: current, ObservedAtMillis: observedAt,
		MaximumAgeMillis: MaximumObservationAgeMS})
}

func TestPolicyHasNoFallbackSelectionAndRejectsSecretShapedTargets(t *testing.T) {
	valid := testPolicy(IntegrationAutomatic)
	if !ValidPolicy(valid) || !AutomaticTargetAuthorized(valid, "refs/heads/main") {
		t.Fatalf("valid policy = %#v", valid)
	}
	fallback := valid
	fallback.SelectionSource = SelectionSource("pull_request_failure")
	fallback = SealPolicy(fallback)
	if ValidPolicy(fallback) {
		t.Fatal("PR failure was accepted as a direct-delivery selection source")
	}
	secret := valid
	secret.AuthorizedTargetRefs = []string{"refs/heads/token=" + "gh" + "p_" + strings.Repeat("x", 24)}
	secret.AutomaticTargetRefs = secret.AuthorizedTargetRefs
	secret = SealPolicy(secret)
	if ValidPolicy(secret) || ValidTargetRef(secret.AuthorizedTargetRefs[0]) {
		t.Fatal("secret-shaped target ref was accepted")
	}
}

func TestManualStateCannotRecordAnIntentBeforeExactHumanAction(t *testing.T) {
	policy := testPolicy(IntegrationManual)
	binding := testBinding(policy)
	state, ok := NewState(binding, policy)
	if !ok || state.Phase != PhaseWaitingHuman || state.Integration != nil {
		t.Fatalf("manual state = %#v, ok=%v", state, ok)
	}
	authorization := testAuthorization(binding)
	wrong := authorization
	wrong.CandidateSHA = strings.Repeat("d", 40)
	wrong = SealAuthorization(wrong)
	if _, ok := AuthorizeManual(state, wrong); ok {
		t.Fatal("wrong-Candidate human action was accepted")
	}
	state, ok = AuthorizeManual(state, authorization)
	if !ok || state.Phase != PhaseIntentRecorded || state.Integration == nil || state.Integration.Attempt != 0 {
		t.Fatalf("authorized state = %#v, ok=%v", state, ok)
	}
}

func TestAutomaticStatePersistsObservationDispatchAndCompletionFrontiers(t *testing.T) {
	policy := testPolicy(IntegrationAutomatic)
	state, ok := NewState(testBinding(policy), policy)
	if !ok {
		t.Fatal("automatic state rejected")
	}
	precondition := testObservation(state, ObservationCurrentExpected, state.Binding.BaseSHA, 1_000)
	state, ok = RecordObservation(state, precondition)
	if !ok || state.Integration.Observation == nil {
		t.Fatal("precondition observation rejected")
	}
	state, ok = BeginDispatch(state)
	if !ok || state.Phase != PhaseDispatching || state.Integration.Attempt != 1 ||
		state.Integration.ConsumedObservationID != precondition.ID || state.Integration.Observation != nil {
		t.Fatalf("dispatch state = %#v, ok=%v", state, ok)
	}
	if !DispatchInFlight(state) {
		t.Fatal("dispatching state did not block Candidate replacement")
	}
	state, ok = RequireObservation(state)
	if !ok || state.Phase != PhaseObservationRequired {
		t.Fatalf("observation-required state = %#v, ok=%v", state, ok)
	}
	if !DispatchInFlight(state) {
		t.Fatal("unreconciled handoff did not block Candidate replacement")
	}
	desired := testObservation(state, ObservationDesired, state.Binding.CandidateSHA, 1_001)
	state, ok = RecordObservation(state, desired)
	if !ok {
		t.Fatal("desired observation rejected")
	}
	state, ok = Complete(state, desired, 1_001)
	if !ok || !ValidState(state) || state.Evidence == nil || state.Phase != PhaseComplete {
		t.Fatalf("complete state = %#v, ok=%v", state, ok)
	}
	if _, ok := BeginDispatch(state); ok {
		t.Fatal("completed direct delivery regained dispatch authority")
	}
	if DispatchInFlight(state) {
		t.Fatal("completed direct delivery remained in flight")
	}
	encoded, err := json.Marshal(state)
	if err != nil || strings.Contains(string(encoded), "/tmp/") || strings.Contains(strings.ToLower(string(encoded)), "password=") {
		t.Fatalf("durable state is not bounded/redacted: %s, %v", encoded, err)
	}
}

func TestObservationFreshnessAndExactBindingAreMandatory(t *testing.T) {
	policy := testPolicy(IntegrationAutomatic)
	state, _ := NewState(testBinding(policy), policy)
	observation := testObservation(state, ObservationCurrentExpected, state.Binding.BaseSHA, 1_000)
	if !ValidObservation(observation, state.Binding, state.Integration.ID, 1_000+MaximumObservationAgeMS) ||
		ValidObservation(observation, state.Binding, state.Integration.ID, 1_001+MaximumObservationAgeMS) {
		t.Fatal("observation freshness boundary is incorrect")
	}
	changed := state.Binding
	changed.CandidateSHA = strings.Repeat("d", 40)
	changed = SealBinding(changed)
	if ValidObservation(observation, changed, state.Integration.ID, 1_000) {
		t.Fatal("observation was rebound to another Candidate")
	}
}
