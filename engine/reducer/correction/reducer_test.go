// SPDX-License-Identifier: Apache-2.0

package correction

import (
	"slices"
	"strings"
	"testing"

	domaincorrection "github.com/mcuadros/director-engine/domain/correction"
	"github.com/mcuadros/director-engine/domain/runtimebudget"
)

func reducerState(t *testing.T) domaincorrection.State {
	t.Helper()
	candidateID, candidateSHA := "candidate-1", strings.Repeat("1", 40)
	inputs := []domaincorrection.FindingInput{
		{ID: "review-1", Source: domaincorrection.SourceReview, Class: "CORRECTNESS", Severity: domaincorrection.SeverityP1,
			CandidateID: candidateID, CandidateSHA: candidateSHA, Summary: "Blocking review finding",
			Evidence:           []domaincorrection.Evidence{{ID: "review-evidence", Kind: domaincorrection.EvidenceReviewFinding, SHA256: strings.Repeat("a", 64)}},
			AcceptanceCoverage: []string{"criterion-1"}, Blocking: true},
		{ID: "ci-1", Source: domaincorrection.SourceCI, Class: "LINUX_CI_FAILURE", Severity: domaincorrection.SeverityP1,
			CandidateID: candidateID, CandidateSHA: candidateSHA, Summary: "Linux check failed",
			Evidence:           []domaincorrection.Evidence{{ID: "ci-evidence", Kind: domaincorrection.EvidenceCIObservation, SHA256: strings.Repeat("b", 64)}},
			AcceptanceCoverage: []string{"criterion-2"}, Blocking: true},
	}
	snapshots := []domaincorrection.SourceSnapshot{
		{Source: domaincorrection.SourceReview, Revision: strings.Repeat("1", 64), Count: 1},
		{Source: domaincorrection.SourceValidation, Revision: strings.Repeat("2", 64), Count: 0},
		{Source: domaincorrection.SourceCI, Revision: strings.Repeat("3", 64), Count: 1},
		{Source: domaincorrection.SourceHuman, Revision: strings.Repeat("4", 64), Count: 0},
	}
	batch, ok := domaincorrection.Canonicalize(candidateID, candidateSHA, []string{"criterion-1", "criterion-2"}, snapshots, inputs)
	if !ok {
		t.Fatal("canonical batch rejected")
	}
	state, ok := domaincorrection.NewState("task-1", "run-1", candidateID, candidateSHA,
		"11111111-1111-4111-8111-111111111111", []string{"criterion-1", "criterion-2"},
		strings.Repeat("a", 64), strings.Repeat("b", 64), strings.Repeat("c", 64), strings.Repeat("d", 64),
		domaincorrection.Policy{AutoFixCIFailures: true, AutoFixReviewFeedback: true, AttemptLimit: 3}, batch)
	if !ok {
		t.Fatal("correction state rejected")
	}
	return state
}

func reducerFacts(t *testing.T) Facts {
	state := reducerState(t)
	return Facts{
		SchemaVersion: SchemaVersion, State: state, CurrentTaskAgentUUID: state.OriginalTaskAgentUUID,
		CurrentCandidateID: state.CurrentCandidateID, CurrentCandidateSHA: state.CurrentCandidateSHA,
		CurrentPlanDigest: state.FrozenPlanDigest, CurrentSkillSetDigest: state.FrozenSkillSetDigest,
		CurrentDecisionDigest: state.CurrentDecisionDigest, CurrentDiffDigest: state.CurrentDiffDigest,
		ProviderState: ProviderCurrent, BudgetDisposition: runtimebudget.DispositionAllow, CIBudgetAvailable: true,
		NewBlockingClasses: []string{},
	}
}

func appendAttempt(state *domaincorrection.State, reason domaincorrection.AttemptReason) domaincorrection.Attempt {
	batch, _ := domaincorrection.CurrentBatch(*state)
	number := uint32(len(state.Attempts) + 1)
	key := domaincorrection.AttemptKey(state.LineageKey, batch.SHA256, number, reason)
	attempt := domaincorrection.Attempt{
		Number: number, Key: key, Reason: reason, BatchSHA256: batch.SHA256, ClassFingerprint: batch.ClassFingerprint,
		CandidateBeforeID: batch.CandidateID, CandidateBeforeSHA: batch.CandidateSHA,
		Prompt: domaincorrection.Effect{ID: domaincorrection.PromptEffectID(key), Phase: domaincorrection.EffectComplete,
			ExternalID: state.OriginalTaskAgentUUID, FactSHA256: strings.Repeat("e", 64), Cursor: uint64(number)},
		PromptSHA256: strings.Repeat("f", 64), BudgetReservationID: "reservation-" + key,
		BudgetEvidenceID: "evidence-" + key, CoverageDelta: domaincorrection.CoverageDelta{Added: []string{}, Removed: []string{}},
	}
	state.Attempts = append(state.Attempts, attempt)
	state.Phase = domaincorrection.PhaseAwaitingOutput
	return attempt
}

func TestReduceDispatchesOneCompleteFingerprintBoundBatch(t *testing.T) {
	facts := reducerFacts(t)
	decision := Reduce(facts)
	batch, _ := domaincorrection.CurrentBatch(facts.State)
	if decision.Kind != DecisionDispatchCorrection || decision.AttemptReason != domaincorrection.ReasonInitial || decision.CleanupAuthorized ||
		decision.BatchSHA256 != batch.SHA256 || decision.FindingFingerprint != batch.FindingFingerprint ||
		decision.ClassFingerprint != batch.ClassFingerprint || decision.CandidateFingerprint != batch.CandidateFingerprint ||
		decision.CoverageFingerprint != batch.CoverageFingerprint || !slices.Equal(decision.BatchedFindingIDs, domaincorrection.FindingIDs(batch)) {
		t.Fatalf("decision = %#v", decision)
	}
}

func TestReduceStopsForDriftIdentityProviderPolicyAndP2(t *testing.T) {
	for name, expectation := range map[string]struct {
		mutate func(*Facts)
		code   string
	}{
		"plan drift":           {func(f *Facts) { f.CurrentPlanDigest = strings.Repeat("9", 64) }, CodeFrozenContextChanged},
		"agent changed":        {func(f *Facts) { f.CurrentTaskAgentUUID = "22222222-2222-4222-8222-222222222222" }, CodeOriginalAgentChanged},
		"provider unavailable": {func(f *Facts) { f.ProviderState = ProviderUnavailable }, CodeProviderUnavailable},
		"provider ambiguous":   {func(f *Facts) { f.ProviderState = ProviderAmbiguous }, CodeProviderAmbiguous},
		"CI budget":            {func(f *Facts) { f.CIBudgetAvailable = false }, string(runtimebudget.ReasonCIHard)},
		"root repeated":        {func(f *Facts) { f.RepeatingRootCause = true }, CodeRootCauseRepeated},
		"auto fix disabled":    {func(f *Facts) { f.State.Policy.AutoFixReviewFeedback = false }, CodeAutomaticFixDisabled},
	} {
		t.Run(name, func(t *testing.T) {
			facts := reducerFacts(t)
			expectation.mutate(&facts)
			decision := Reduce(facts)
			if decision.Kind != DecisionEscalate || decision.Code != expectation.code || decision.CleanupAuthorized {
				t.Fatalf("decision = %#v", decision)
			}
		})
	}
	facts := reducerFacts(t)
	batch := facts.State.Batches[0]
	input := batch.Findings[0].FindingInput
	input.Severity, input.Blocking, input.RequiresHumanDecision = domaincorrection.SeverityP2, false, true
	input.Evidence[0].SHA256 = strings.Repeat("8", 64)
	inputs := []domaincorrection.FindingInput{input, batch.Findings[1].FindingInput}
	changed, ok := domaincorrection.Canonicalize(batch.CandidateID, batch.CandidateSHA, facts.State.CriterionIDs, batch.Sources, inputs)
	if !ok {
		t.Fatal("P2 batch rejected")
	}
	facts.State.Batches[0] = changed
	decision := Reduce(facts)
	if decision.Kind != DecisionEscalate || decision.Code != CodeP2DecisionRequired {
		t.Fatalf("P2 decision = %#v", decision)
	}
}

func TestReduceRejectsUnchangedOutputOnceAndRepeatedAcknowledgementEscalates(t *testing.T) {
	facts := reducerFacts(t)
	attempt := appendAttempt(&facts.State, domaincorrection.ReasonInitial)
	batch, _ := domaincorrection.CurrentBatch(facts.State)
	output := domaincorrection.Output{
		SchemaVersion: domaincorrection.OutputSchemaVersion, AttemptKey: attempt.Key, AgentUUID: facts.State.OriginalTaskAgentUUID,
		BatchSHA256: batch.SHA256, BatchedFindingIDs: domaincorrection.FindingIDs(batch), CandidateSHA: batch.CandidateSHA,
		ResolvedFindingIDs: []string{}, AcceptanceCoverage: slices.Clone(batch.AcceptanceCoverage),
	}
	facts.Output = &output
	decision := Reduce(facts)
	if decision.Kind != DecisionRejectAcknowledgementOnce || decision.Code != CodeUnchangedCandidate || decision.AcknowledgementRejections != 1 {
		t.Fatalf("unchanged = %#v", decision)
	}
	facts.State.AcknowledgementRejections = 1
	decision = Reduce(facts)
	if decision.Kind != DecisionEscalate || decision.Code != CodeAcknowledgementRepeated {
		t.Fatalf("repeat = %#v", decision)
	}
}

func TestReduceAdmitsChangedOutputOnThirdAttemptAndStopsFourth(t *testing.T) {
	facts := reducerFacts(t)
	for index := 0; index < 3; index++ {
		reason := domaincorrection.ReasonFindingsChanged
		if index == 0 {
			reason = domaincorrection.ReasonInitial
		}
		attempt := appendAttempt(&facts.State, reason)
		if index < 2 {
			batch, _ := domaincorrection.CurrentBatch(facts.State)
			attempt.Output = &domaincorrection.Output{
				SchemaVersion: domaincorrection.OutputSchemaVersion, AttemptKey: attempt.Key,
				AgentUUID: facts.State.OriginalTaskAgentUUID, BatchSHA256: batch.SHA256,
				BatchedFindingIDs: domaincorrection.FindingIDs(batch), CandidateSHA: strings.Repeat(string(rune('2'+index)), 40),
				ResolvedFindingIDs: []string{domaincorrection.FindingIDs(batch)[0]}, AcceptanceCoverage: slices.Clone(batch.AcceptanceCoverage),
			}
			attempt.Classification = domaincorrection.ClassificationProductive
			facts.State.Attempts[len(facts.State.Attempts)-1] = attempt
			facts.State.Phase = domaincorrection.PhaseReady
		}
	}
	batch, _ := domaincorrection.CurrentBatch(facts.State)
	last := facts.State.Attempts[2]
	output := domaincorrection.Output{
		SchemaVersion: domaincorrection.OutputSchemaVersion, AttemptKey: last.Key, AgentUUID: facts.State.OriginalTaskAgentUUID,
		BatchSHA256: batch.SHA256, BatchedFindingIDs: domaincorrection.FindingIDs(batch), CandidateSHA: strings.Repeat("2", 40),
		ResolvedFindingIDs: []string{domaincorrection.FindingIDs(batch)[0]}, AcceptanceCoverage: slices.Clone(batch.AcceptanceCoverage),
	}
	facts.Output = &output
	decision := Reduce(facts)
	if decision.Kind != DecisionObserveCorrectedCandidate || decision.CandidateSHA != output.CandidateSHA {
		t.Fatalf("third output = %#v", decision)
	}
	facts.Output = nil
	facts.State.Phase = domaincorrection.PhaseReady
	decision = Reduce(facts)
	if decision.Kind != DecisionEscalate || decision.Code != CodeAttemptLimitExhausted {
		t.Fatalf("fourth = %#v", decision)
	}
	facts.NewBlockingClasses = []string{"review:NEW_ROOT"}
	decision = Reduce(facts)
	if decision.Code != CodeNewBlockingClassAfterCap {
		t.Fatalf("new class after cap = %#v", decision)
	}
}
