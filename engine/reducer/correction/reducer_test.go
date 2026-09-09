// SPDX-License-Identifier: Apache-2.0

package correction

import (
	"slices"
	"strings"
	"testing"
)

func correctionFacts() Facts {
	return Facts{
		SchemaVersion:         SchemaVersion,
		TaskAgentID:           "agent-task-1",
		BaseSHA:               strings.Repeat("0", 40),
		PreviousCandidateSHA:  strings.Repeat("1", 40),
		FrozenPlanDigest:      strings.Repeat("a", 64),
		CurrentPlanDigest:     strings.Repeat("a", 64),
		FrozenSkillSetDigest:  strings.Repeat("b", 64),
		CurrentSkillSetDigest: strings.Repeat("b", 64),
		CurrentDecisionDigest: strings.Repeat("c", 64),
		CurrentDiffDigest:     strings.Repeat("d", 64),
		Findings: FindingSet{
			Review:        []string{"review-2", "review-1"},
			Validation:    []string{"ci-1"},
			HumanFeedback: []string{"human-1"},
		},
		CorrectionAttempts:     1,
		CorrectionAttemptLimit: 3,
	}
}

func TestReduceDispatchesOneBatchWithReusedAndRefreshedContext(t *testing.T) {
	facts := correctionFacts()
	decision := Reduce(facts)
	if decision.Kind != DecisionDispatchCorrection || decision.CleanupAuthorized {
		t.Fatalf("decision = %#v", decision)
	}
	expected := []string{"ci-1", "human-1", "review-1", "review-2"}
	if !slices.Equal(decision.BatchedFindingIDs, expected) {
		t.Fatalf("finding batch = %#v", decision.BatchedFindingIDs)
	}
	if decision.ReusePlanDigest != facts.FrozenPlanDigest ||
		decision.ReuseSkillSetDigest != facts.FrozenSkillSetDigest ||
		decision.RefreshDecisionDigest != facts.CurrentDecisionDigest ||
		decision.RefreshDiffDigest != facts.CurrentDiffDigest {
		t.Fatalf("context binding = %#v", decision)
	}
}

func TestReduceRefusesFrozenContextDriftAndIncompleteFindingBatches(t *testing.T) {
	for name, expectation := range map[string]struct {
		mutate func(*Facts)
		code   string
	}{
		"plan moved":        {func(value *Facts) { value.CurrentPlanDigest = strings.Repeat("e", 64) }, "correction_frozen_context_changed"},
		"skills moved":      {func(value *Facts) { value.CurrentSkillSetDigest = strings.Repeat("e", 64) }, "correction_frozen_context_changed"},
		"duplicate finding": {func(value *Facts) { value.Findings.Validation = []string{"review-1"} }, "correction_finding_batch_invalid"},
		"empty findings":    {func(value *Facts) { value.Findings = FindingSet{} }, "correction_finding_batch_invalid"},
	} {
		t.Run(name, func(t *testing.T) {
			facts := correctionFacts()
			expectation.mutate(&facts)
			decision := Reduce(facts)
			if decision.Kind != DecisionEscalate || decision.Code != expectation.code || decision.CleanupAuthorized {
				t.Fatalf("decision = %#v", decision)
			}
		})
	}
}

func TestReduceRejectsAcknowledgementOnlyExactlyOnce(t *testing.T) {
	facts := correctionFacts()
	batch, _ := findingBatch(facts.Findings)
	facts.Output = &TurnOutput{
		AgentID:             facts.TaskAgentID,
		AcknowledgementOnly: true,
		BatchedFindingIDs:   batch,
	}
	decision := Reduce(facts)
	if decision.Kind != DecisionRejectAcknowledgementOnce ||
		decision.AcknowledgementRejections != 1 || decision.CleanupAuthorized {
		t.Fatalf("first acknowledgement = %#v", decision)
	}

	facts.AcknowledgementRejections = 1
	decision = Reduce(facts)
	if decision.Kind != DecisionEscalate ||
		decision.Code != "correction_acknowledgement_repeated" || decision.CleanupAuthorized {
		t.Fatalf("repeated acknowledgement = %#v", decision)
	}
}

func TestReduceAdmitsOnlyAChangedCandidateFromTheSameAgentAndWholeBatch(t *testing.T) {
	facts := correctionFacts()
	batch, _ := findingBatch(facts.Findings)
	facts.Output = &TurnOutput{
		AgentID:           facts.TaskAgentID,
		CandidateSHA:      strings.Repeat("2", 40),
		BatchedFindingIDs: batch,
	}
	decision := Reduce(facts)
	if decision.Kind != DecisionAdmitCorrectedCandidate ||
		decision.CandidateSHA != facts.Output.CandidateSHA || decision.CleanupAuthorized {
		t.Fatalf("corrected Candidate = %#v", decision)
	}

	for name, expectation := range map[string]struct {
		mutate func(*Facts)
		code   string
	}{
		"same Candidate":     {func(value *Facts) { value.Output.CandidateSHA = value.PreviousCandidateSHA }, "corrected_candidate_invalid"},
		"other agent":        {func(value *Facts) { value.Output.AgentID = "agent-other" }, "correction_output_binding_invalid"},
		"partial batch":      {func(value *Facts) { value.Output.BatchedFindingIDs = value.Output.BatchedFindingIDs[:2] }, "correction_output_binding_invalid"},
		"attempts exhausted": {func(value *Facts) { value.CorrectionAttempts = value.CorrectionAttemptLimit }, "corrected_candidate_invalid"},
	} {
		t.Run(name, func(t *testing.T) {
			changed := facts
			output := *facts.Output
			output.BatchedFindingIDs = append([]string(nil), facts.Output.BatchedFindingIDs...)
			changed.Output = &output
			expectation.mutate(&changed)
			decision := Reduce(changed)
			if decision.Kind != DecisionEscalate || decision.Code != expectation.code || decision.CleanupAuthorized {
				t.Fatalf("decision = %#v", decision)
			}
		})
	}
}
