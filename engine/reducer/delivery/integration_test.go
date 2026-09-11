// SPDX-License-Identifier: Apache-2.0

package delivery

import (
	"strings"
	"testing"

	domainintegration "github.com/mcuadros/director-engine/domain/integration"
)

func reducerState(t *testing.T, mode domainintegration.Mode) domainintegration.State {
	t.Helper()
	policy, _ := domainintegration.NewPolicy(mode, strings.Repeat("1", 64))
	binding := domainintegration.SealBinding(domainintegration.Binding{TaskID: "task-1", RunID: "run-1", CandidateID: "candidate-1",
		CandidateSHA: strings.Repeat("b", 40), BaseSHA: strings.Repeat("a", 40), TreeSHA: strings.Repeat("c", 40),
		ManifestSHA256: strings.Repeat("2", 64), CandidateGeneration: 1, TaskVersion: 1, ConfigurationSHA256: policy.ConfigurationSHA256,
		RepositoryID: "repository-1", RepositoryBindingSHA256: strings.Repeat("3", 64), CanonicalRemote: "https://github.com/example/product",
		GitHubRepositoryID: 1, GitHubRepositoryNodeID: "R_node", RepositoryOwner: "example", RepositoryName: "product", ViewerLogin: "example",
		Branch: "task/task-1", BaseRef: "refs/heads/main", PullRequestNumber: 1, PullRequestNodeID: "PR_node",
		OwnershipSHA256: strings.Repeat("8", 64), MarkerSHA256: strings.Repeat("9", 64),
		PublicationEvidenceID: "publication-1", ValidationPolicySHA256: strings.Repeat("4", 64), ValidationEvidenceID: "validation-1",
		ValidationEvidenceSHA256: strings.Repeat("5", 64), ReviewEvidenceID: "review-1", ReviewerUUID: "11111111-1111-4111-8111-111111111111",
		CIObservationID: "ci-1", CIObservationSHA256: strings.Repeat("6", 64), FeedbackStateSHA256: strings.Repeat("7", 64),
		ReadyEvidenceID: "ready-1", PolicySHA256: policy.SHA256, LeaseEpoch: 1})
	state, ok := domainintegration.NewState(binding, policy)
	if !ok {
		t.Fatal("state rejected")
	}
	return state
}

func reducerFacts(state domainintegration.State) IntegrationFacts {
	return IntegrationFacts{SchemaVersion: IntegrationSchemaVersion, State: state, ProjectActive: true, LeaseCurrent: true,
		CandidateCurrent: true, PublicationReady: true, ValidationPassed: true, ReviewApproved: true, FeedbackClear: true,
		CorrectionSettled: true, DirectDeliveryAbsent: true, CIBudgetValid: true, NowMillis: 1_000}
}

func TestIntegrationReducerManualWaitAndAutomaticObservation(t *testing.T) {
	manual := ReduceIntegration(reducerFacts(reducerState(t, domainintegration.ModeManual)))
	if manual.Kind != IntegrationDecisionWaitHuman || manual.MergeAuthorized {
		t.Fatalf("manual = %#v", manual)
	}
	automatic := ReduceIntegration(reducerFacts(reducerState(t, domainintegration.ModeAutomatic)))
	if automatic.Kind != IntegrationDecisionObserve || automatic.MergeAuthorized {
		t.Fatalf("automatic = %#v", automatic)
	}
}

func TestIntegrationReducerEveryDurableGateFailsClosed(t *testing.T) {
	tests := []struct {
		name, code string
		mutate     func(*IntegrationFacts)
	}{
		{"candidate", IntegrationCodeCandidateStale, func(f *IntegrationFacts) { f.CandidateCurrent = false }},
		{"publication", IntegrationCodePublicationStale, func(f *IntegrationFacts) { f.PublicationReady = false }},
		{"validation", IntegrationCodeValidationStale, func(f *IntegrationFacts) { f.ValidationPassed = false }},
		{"review", IntegrationCodeReviewStale, func(f *IntegrationFacts) { f.ReviewApproved = false }},
		{"feedback", IntegrationCodeFeedbackPending, func(f *IntegrationFacts) { f.FeedbackClear = false }},
		{"direct", IntegrationCodeDirectConflict, func(f *IntegrationFacts) { f.DirectDeliveryAbsent = false }},
		{"ci budget", IntegrationCodeCIBudgetInvalid, func(f *IntegrationFacts) { f.CIBudgetValid = false }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			facts := reducerFacts(reducerState(t, domainintegration.ModeAutomatic))
			test.mutate(&facts)
			decision := ReduceIntegration(facts)
			if decision.Kind != IntegrationDecisionEscalate || decision.Code != test.code || decision.MergeAuthorized || decision.CleanupAuthorized {
				t.Fatalf("decision = %#v", decision)
			}
		})
	}
}
