// SPDX-License-Identifier: Apache-2.0

package review

import (
	_ "embed"
	"encoding/json"
	"slices"
	"strings"
	"testing"
)

var (
	//go:embed testdata/m0.4-setup-thrash.json
	m04SetupThrash []byte
	//go:embed testdata/m0.6-setup-thrash.json
	m06SetupThrash []byte
)

const (
	taskAgentUUID   = "11111111-1111-4111-8111-111111111111"
	coordinatorUUID = "22222222-2222-4222-8222-222222222222"
	ownerUUID       = "33333333-3333-4333-8333-333333333333"
	reviewerUUID    = "abcdefab-cdef-4abc-8def-abcdefabcdef"
)

func repeated(character string, count int) string { return strings.Repeat(character, count) }

func bindingFixture() Binding {
	return SealBinding(Binding{
		TaskID: "dir-m4.3", RunID: "run-m4-3", CandidateID: "candidate-m4-3",
		CandidateSHA: repeated("a", 40), BaseSHA: repeated("b", 40), TreeSHA: repeated("c", 40),
		ManifestSHA256: repeated("1", 64), AcceptanceSHA256: repeated("2", 64),
		ConfigurationSHA256: repeated("3", 64), ProfileSHA256: repeated("4", 64),
		ContextSHA256: repeated("5", 64), DecisionsSHA256: repeated("6", 64), FindingsSHA256: repeated("7", 64),
		CandidateGeneration: 1, CISlotID: "ci-slot-1", TaskAgentUUID: taskAgentUUID,
		CoordinatorUUID: coordinatorUUID, ReviewOwnerUUID: ownerUUID,
	})
}

func ciFixture(binding Binding) CIObservation {
	return SealCIObservation(CIObservation{
		ID: "ci-observation-1", SlotID: binding.CISlotID, CandidateSHA: binding.CandidateSHA,
		BaseSHA: binding.BaseSHA, TreeSHA: binding.TreeSHA, ManifestSHA256: binding.ManifestSHA256,
		WorkflowRunID: "34500000123", RequiredChecks: []string{"maintained-linux-ci"}, Status: "passed",
		Authoritative: true, Complete: true, ObservedAtMillis: 1_000,
	})
}

func planFixture() []Probe {
	return []Probe{{ID: "git-identity", Kind: ProbeGitInspect, Argv: []string{"git", "status", "--short"}, TimeoutSeconds: 30}}
}

func stateFixture(t *testing.T) State {
	t.Helper()
	state, ok := NewState(bindingFixture(), []string{"criterion-b", "criterion-a"}, planFixture(), ProfileDecision{Admitted: true, Code: "reviewer_profile_admitted"})
	if !ok {
		t.Fatal("NewState rejected valid review")
	}
	return state
}

func claimFixture(t *testing.T, state State) Claim {
	t.Helper()
	matrix := make([]MatrixResult, len(state.Matrix))
	for index, row := range state.Matrix {
		matrix[index] = MatrixResult{ID: row.ID, Kind: row.Kind, Status: "covered"}
	}
	checkout := SealCheckoutEvidence(CheckoutEvidence{
		CheckoutID: state.ReviewKey, OwnerUUID: state.Binding.ReviewOwnerUUID, CandidateSHA: state.Binding.CandidateSHA,
		TreeSHA: state.Binding.TreeSHA, Detached: true, Clean: true, PrimaryDistinct: true, SourceUnchanged: true,
	})
	return Claim{
		SchemaVersion: ClaimSchemaVersion, ReviewKey: state.ReviewKey,
		AttemptKey: AttemptKey(state.Binding, AttemptInitial), AttemptReason: AttemptInitial,
		Binding: state.Binding, ReviewerUUID: reviewerUUID, HarnessReviewerUUID: reviewerUUID,
		Checkout: checkout, CIObservation: ciFixture(state.Binding),
		Verdict: VerdictApproveCandidate, Matrix: matrix, Findings: []Finding{}, ResidualRiskCodes: []string{},
		HarnessVersion: 2, Setup: SetupObservation{Mode: SetupRemoteOnly, GitAvailable: true, DependencyState: DependencyMissing},
		Probes: []ProbeResult{{ID: "git-identity", Status: "passed", ExitCode: 0, OutputSHA256: repeated("8", 64)}},
	}
}

func TestBindingAndReviewKeyInvalidateEveryExactAuthorityInput(t *testing.T) {
	original := bindingFixture()
	if !ValidBinding(original) {
		t.Fatal("valid binding rejected")
	}
	mutations := []func(*Binding){
		func(value *Binding) { value.TaskID = "dir-m4.4" }, func(value *Binding) { value.RunID = "run-other" },
		func(value *Binding) { value.CandidateID = "candidate-other" }, func(value *Binding) { value.CandidateSHA = repeated("d", 40) },
		func(value *Binding) { value.BaseSHA = repeated("d", 40) }, func(value *Binding) { value.TreeSHA = repeated("d", 40) },
		func(value *Binding) { value.ManifestSHA256 = repeated("8", 64) }, func(value *Binding) { value.AcceptanceSHA256 = repeated("8", 64) },
		func(value *Binding) { value.ConfigurationSHA256 = repeated("8", 64) }, func(value *Binding) { value.ProfileSHA256 = repeated("8", 64) },
		func(value *Binding) { value.ContextSHA256 = repeated("8", 64) }, func(value *Binding) { value.DecisionsSHA256 = repeated("8", 64) },
		func(value *Binding) { value.FindingsSHA256 = repeated("8", 64) }, func(value *Binding) { value.CandidateGeneration++ },
		func(value *Binding) { value.CISlotID = "ci-slot-2" },
	}
	for index, mutate := range mutations {
		changed := original
		mutate(&changed)
		changed = SealBinding(changed)
		if !ValidBinding(changed) || ReviewKey(changed) == ReviewKey(original) {
			t.Fatalf("mutation %d did not create a new exact review key", index)
		}
		state := stateFixture(t)
		if Current(state, changed) {
			t.Fatalf("mutation %d replayed old review authority", index)
		}
	}
	staleHash := original
	staleHash.TreeSHA = repeated("d", 40)
	if ValidBinding(staleHash) {
		t.Fatal("changed binding retained its old digest")
	}
}

func TestMatrixRequiresEveryCriterionAndEightDimensions(t *testing.T) {
	matrix, ok := FreezeMatrix([]string{"z", "a"})
	if !ok || len(matrix) != 10 || matrix[0].ID != "a" || matrix[1].ID != "z" {
		t.Fatalf("matrix = %#v", matrix)
	}
	got := make([]Dimension, 0, 8)
	for _, row := range matrix {
		if row.Kind == MatrixDimension {
			got = append(got, Dimension(row.ID))
		}
	}
	if !slices.Equal(got, Dimensions()) {
		t.Fatalf("dimensions = %#v", got)
	}
	if _, ok := FreezeMatrix([]string{"duplicate", "duplicate"}); ok {
		t.Fatal("duplicate criterion admitted")
	}
}

func TestProfilePolicyWarnsOrFailsWithoutFallback(t *testing.T) {
	worker := Profile{Provider: "codex", Model: "gpt-5", SHA256: repeated("a", 64), Supported: true}
	reviewer := worker
	decision := EvaluateProfile(ProfilePolicy{}, worker, reviewer)
	if !decision.Admitted || decision.Warning != "same_reviewer_model" {
		t.Fatalf("same-model decision = %#v", decision)
	}
	decision = EvaluateProfile(ProfilePolicy{RequireDifferentReviewerModel: true}, worker, reviewer)
	if decision.Admitted || decision.Code != "different_reviewer_model_required" {
		t.Fatalf("different-model policy = %#v", decision)
	}
	reviewer.Provider = "claude-code"
	reviewer.Model = "sonnet"
	if decision = EvaluateProfile(ProfilePolicy{RequireDifferentReviewerModel: true}, worker, reviewer); !decision.Admitted || decision.Warning != "" {
		t.Fatalf("different profile = %#v", decision)
	}
	reviewer.Supported = false
	if decision = EvaluateProfile(ProfilePolicy{}, worker, reviewer); decision.Admitted || decision.Code != "reviewer_profile_unsupported" {
		t.Fatalf("unsupported profile = %#v", decision)
	}
}

func TestClaimRejectsParentageSelfReviewChangedCIAndIncompleteCoverage(t *testing.T) {
	state := stateFixture(t)
	claim := claimFixture(t, state)
	if !ValidateClaim(claim, state.Binding, reviewerUUID, claim.CIObservation, state.Matrix, state.ProbePlan) {
		t.Fatal("valid claim rejected")
	}
	tests := []func(*Claim){
		func(value *Claim) { parent := taskAgentUUID; value.ParentAgentID = &parent },
		func(value *Claim) { value.ReviewerUUID = taskAgentUUID },
		func(value *Claim) { value.HarnessReviewerUUID = ownerUUID },
		func(value *Claim) { value.ReviewerUUID = strings.ToUpper(reviewerUUID) },
		func(value *Claim) {
			value.CIObservation.ID = "ci-other"
			value.CIObservation = SealCIObservation(value.CIObservation)
		},
		func(value *Claim) {
			value.CIObservation.CandidateSHA = repeated("d", 40)
			value.CIObservation = SealCIObservation(value.CIObservation)
		},
		func(value *Claim) { value.Matrix = value.Matrix[:len(value.Matrix)-1] },
		func(value *Claim) { value.HarnessVersion = 1 },
		func(value *Claim) { value.Setup.PreparationAttempts = 1 },
	}
	for index, mutate := range tests {
		changed := claim
		changed.Matrix = slices.Clone(claim.Matrix)
		changed.Probes = slices.Clone(claim.Probes)
		mutate(&changed)
		if ValidateClaim(changed, state.Binding, changed.ReviewerUUID, claim.CIObservation, state.Matrix, state.ProbePlan) {
			t.Fatalf("invalid claim %d admitted", index)
		}
	}
}

func TestProbePlanIsBoundedReadOnlyAndNeverCompleteCI(t *testing.T) {
	valid := []Probe{
		{ID: "git", Kind: ProbeGitInspect, Argv: []string{"git", "diff", "HEAD^", "HEAD"}, TimeoutSeconds: 30},
		{ID: "source", Kind: ProbeSourceInspect, Argv: []string{"rg", "TODO", "."}, TimeoutSeconds: 30},
		{ID: "focused", Kind: ProbeFocusedTest, Argv: []string{"go", "test", "./domain/review", "-run", "TestClaim"}, TimeoutSeconds: 120},
	}
	if !ValidProbePlan(valid) {
		t.Fatal("bounded probe plan rejected")
	}
	for _, invalid := range [][]Probe{
		{{ID: "push", Kind: ProbeGitInspect, Argv: []string{"git", "push", "origin", "main"}, TimeoutSeconds: 30}},
		{{ID: "write", Kind: ProbeSourceInspect, Argv: []string{"sed", "-i", "s/a/b/", "source.go"}, TimeoutSeconds: 30}},
		{{ID: "complete", Kind: ProbeFocusedTest, Argv: []string{"go", "test", "./..."}, TimeoutSeconds: 300}},
		{{ID: "effect", Kind: ProbeFocusedTest, Argv: []string{"gh", "pr", "create"}, TimeoutSeconds: 30}},
	} {
		if ValidProbePlan(invalid) {
			t.Fatalf("effectful probe admitted: %#v", invalid)
		}
	}
}

func TestValidStateRejectsPartialOrReorderedDurableReview(t *testing.T) {
	state := stateFixture(t)
	state.SourcePath = "/srv/source"
	state.PrimaryPath = "/srv/task"
	state.ReviewerRoot = "/srv/reviewers"
	state.CheckoutPath = "/srv/reviewers/candidate"
	state.PrimaryHeadSHA = state.Binding.CandidateSHA
	if !ValidState(state) {
		t.Fatal("valid intent state rejected")
	}
	changed := state
	changed.Workspace.Phase = EffectDispatching
	if ValidState(changed) {
		t.Fatal("workspace dispatch before checkout admitted")
	}
	changed = state
	changed.Attempts[0].Reason = AttemptHarnessPreStart
	if ValidState(changed) {
		t.Fatal("changed attempt identity admitted")
	}
}

func TestInvalidationRetainsHistoricalVerdictEvidenceWithoutAuthority(t *testing.T) {
	state := stateFixture(t)
	state.Evidence = &Evidence{ID: "historical-evidence"}
	state.VerdictDurablyObserved = true
	invalidated := Invalidate(state, "candidate_changed")
	if !invalidated.Invalidated || invalidated.InvalidationCode != "candidate_changed" ||
		invalidated.Evidence != state.Evidence || !invalidated.VerdictDurablyObserved || Current(invalidated, invalidated.Binding) {
		t.Fatalf("historical invalidation = %#v", invalidated)
	}
}

func TestClosedVerdictSeverityAndP2Rules(t *testing.T) {
	state := stateFixture(t)
	for _, severity := range []Severity{SeverityP0, SeverityP1, SeverityP2, SeverityP3} {
		claim := claimFixture(t, state)
		claim.Findings = []Finding{{Code: "FINDING", Severity: severity, Dimension: DimensionCorrectness, Summary: "bounded", References: []string{"source.go:1"}}}
		if severity == SeverityP0 || severity == SeverityP1 {
			claim.Verdict = VerdictChangesRequested
		}
		if severity == SeverityP2 {
			claim.HumanP2AcceptanceID = "human-decision-1"
		}
		if !ValidateClaim(claim, state.Binding, reviewerUUID, claim.CIObservation, state.Matrix, state.ProbePlan) {
			t.Fatalf("severity %s claim rejected", severity)
		}
	}
	claim := claimFixture(t, state)
	claim.Findings = []Finding{{Code: "P2_RISK", Severity: SeverityP2, Dimension: DimensionSecurity, Summary: "risk", References: []string{"source.go:1"}}}
	if ValidateClaim(claim, state.Binding, reviewerUUID, claim.CIObservation, state.Matrix, state.ProbePlan) {
		t.Fatal("P2 approval admitted without human acceptance")
	}
	claim = claimFixture(t, state)
	claim.Verdict = VerdictChangesRequested
	if ValidateClaim(claim, state.Binding, reviewerUUID, claim.CIObservation, state.Matrix, state.ProbePlan) {
		t.Fatal("empty changes_requested admitted")
	}
	claim = claimFixture(t, state)
	claim.Verdict = VerdictNeedsHumanDecision
	if ValidateClaim(claim, state.Binding, reviewerUUID, claim.CIObservation, state.Matrix, state.ProbePlan) {
		t.Fatal("human-decision verdict without a P2 finding admitted")
	}
	claim.Findings = []Finding{{Code: "P2_RISK", Severity: SeverityP2, Dimension: DimensionSecurity, Summary: "risk", References: []string{"source.go:1"}}}
	if !ValidateClaim(claim, state.Binding, reviewerUUID, claim.CIObservation, state.Matrix, state.ProbePlan) {
		t.Fatal("bounded P2 human-decision verdict rejected")
	}
}

func TestAttemptReasonsAreSingleUsePerCandidate(t *testing.T) {
	state := stateFixture(t)
	if _, ok := AdmitAttempt(state.Attempts, state.Binding, AttemptInitial); ok {
		t.Fatal("duplicate initial attempt admitted")
	}
	attempts, ok := AdmitAttempt(state.Attempts, state.Binding, AttemptProviderPreDispatch)
	if !ok || len(attempts) != 2 {
		t.Fatalf("deterministic retry = %#v, %t", attempts, ok)
	}
	if _, ok := AdmitAttempt(attempts, state.Binding, AttemptProviderPreDispatch); ok {
		t.Fatal("duplicate reason admitted")
	}
	attempts, ok = AdmitAttempt(attempts, state.Binding, AttemptHarnessPreStart)
	if !ok || len(attempts) != 3 || !AttemptAdmitted(attempts, state.Binding, AttemptHarnessPreStart, AttemptKey(state.Binding, AttemptHarnessPreStart)) {
		t.Fatalf("bounded harness attempt = %#v, %t", attempts, ok)
	}
	if _, ok := AdmitAttempt(attempts, state.Binding, AttemptHarnessPreStart); ok {
		t.Fatal("attempt budget exceeded")
	}
	changed := state.Binding
	changed.CandidateSHA = repeated("d", 40)
	changed = SealBinding(changed)
	if _, ok := AdmitAttempt(nil, changed, AttemptInitial); !ok {
		t.Fatal("new Candidate did not receive a new attempt namespace")
	}
}

func TestClaimOutputBoundsAndSanitizationFailClosed(t *testing.T) {
	state := stateFixture(t)
	base := claimFixture(t, state)
	for name, mutate := range map[string]func(*Claim){
		"findings": func(value *Claim) {
			value.Findings = make([]Finding, MaximumFindings+1)
		},
		"summary": func(value *Claim) {
			value.Findings = []Finding{{Code: "TOO_LONG", Severity: SeverityP1, Dimension: DimensionCorrectness, Summary: repeated("x", 1025), References: []string{"source.go:1"}}}
			value.Verdict = VerdictChangesRequested
		},
		"private reference": func(value *Claim) {
			value.Findings = []Finding{{Code: "PRIVATE_PATH", Severity: SeverityP1, Dimension: DimensionSecurity, Summary: "bounded", References: []string{"/tmp/private-history"}}}
			value.Verdict = VerdictChangesRequested
		},
		"secret summary": func(value *Claim) {
			value.Findings = []Finding{{Code: "SECRET", Severity: SeverityP1, Dimension: DimensionSecurity, Summary: "token=not-allowed-here", References: []string{"source.go:1"}}}
			value.Verdict = VerdictChangesRequested
		},
		"residual count": func(value *Claim) {
			value.ResidualRiskCodes = make([]string, 33)
			for index := range value.ResidualRiskCodes {
				value.ResidualRiskCodes[index] = "RISK_" + string(rune('A'+index))
			}
		},
	} {
		t.Run(name, func(t *testing.T) {
			claim := base
			mutate(&claim)
			if ValidateClaim(claim, state.Binding, reviewerUUID, claim.CIObservation, state.Matrix, state.ProbePlan) {
				t.Fatal("oversize or sensitive Review output admitted")
			}
		})
	}
}

func TestRemoteOnlyHarnessRejectsFocusedExecutionWithoutDependencies(t *testing.T) {
	state, ok := NewState(bindingFixture(), []string{"criterion"}, []Probe{{
		ID: "focused", Kind: ProbeFocusedTest, Argv: []string{"go", "test", "./domain/review", "-run", "TestClaim"}, TimeoutSeconds: 60,
	}}, ProfileDecision{Admitted: true, Code: "reviewer_profile_admitted"})
	if !ok {
		t.Fatal("focused state rejected")
	}
	claim := claimFixture(t, state)
	claim.Probes = []ProbeResult{{ID: "focused", Status: "passed", ExitCode: 0, OutputSHA256: repeated("8", 64)}}
	if ValidateClaim(claim, state.Binding, reviewerUUID, claim.CIObservation, state.Matrix, state.ProbePlan) {
		t.Fatal("remote-only harness executed a focused dependency probe")
	}
}

func TestSanitizedJournalSetupFixturesNeverBecomeCandidateFailure(t *testing.T) {
	for name, content := range map[string][]byte{"m0.4": m04SetupThrash, "m0.6": m06SetupThrash} {
		var observation SetupObservation
		if err := json.Unmarshal(content, &observation); err != nil {
			t.Fatal(err)
		}
		decision := EvaluateSetup(observation)
		if decision.CandidateFailure {
			t.Fatalf("%s became Candidate failure: %#v", name, decision)
		}
		if decision.Code != SetupThrash && decision.Code != SetupEnvironmentUnavailable {
			t.Fatalf("%s = %#v", name, decision)
		}
		if strings.Contains(strings.ToLower(string(content)), "password") || strings.Contains(strings.ToLower(string(content)), "token") || strings.Contains(string(content), "/home/") {
			t.Fatalf("%s contains raw secret/history-shaped data", name)
		}
	}
}
