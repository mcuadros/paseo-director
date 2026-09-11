// SPDX-License-Identifier: Apache-2.0

package integration

import (
	"strings"
	"testing"
)

func testPolicy(t *testing.T, mode Mode) Policy {
	t.Helper()
	value, ok := NewPolicy(mode, strings.Repeat("1", 64))
	if !ok {
		t.Fatal("policy rejected")
	}
	return value
}

func testBinding(t *testing.T, policy Policy) Binding {
	t.Helper()
	value := SealBinding(Binding{TaskID: "dir-m4.9", RunID: "run-1", CandidateID: "candidate-1",
		CandidateSHA: strings.Repeat("b", 40), BaseSHA: strings.Repeat("a", 40), TreeSHA: strings.Repeat("c", 40),
		ManifestSHA256: strings.Repeat("2", 64), CandidateGeneration: 2, TaskVersion: 3,
		ConfigurationSHA256: policy.ConfigurationSHA256, RepositoryID: "repository-1",
		RepositoryBindingSHA256: strings.Repeat("3", 64), CanonicalRemote: "https://github.com/example/product",
		GitHubRepositoryID: 123, GitHubRepositoryNodeID: "R_node", RepositoryOwner: "example", RepositoryName: "product",
		ViewerLogin: "example", Branch: "task/dir-m4.9-final-integration", BaseRef: "refs/heads/main",
		PullRequestNumber: 7, PullRequestNodeID: "PR_node_7", PublicationEvidenceID: "publication-evidence-1",
		OwnershipSHA256: strings.Repeat("a", 64), MarkerSHA256: strings.Repeat("b", 64),
		ValidationPolicySHA256: strings.Repeat("4", 64), ValidationEvidenceID: "validation-evidence-1",
		ValidationEvidenceSHA256: strings.Repeat("5", 64), ReviewEvidenceID: "review-evidence-1",
		ReviewerUUID: "11111111-1111-4111-8111-111111111111", CIObservationID: "ci-observation-1",
		CIObservationSHA256: strings.Repeat("6", 64), FeedbackStateSHA256: strings.Repeat("7", 64),
		ReadyEvidenceID: "integration-ready-1", PolicySHA256: policy.SHA256, LeaseEpoch: 9})
	if !ValidBinding(value) {
		t.Fatalf("binding rejected: %#v", value)
	}
	return value
}

func testForge(binding Binding, status ForgeStatus, merge string, now int64) ForgeObservation {
	value := ForgeObservation{ID: "forge-observation-1", Status: status, Code: CodeOK,
		RepositoryID: binding.GitHubRepositoryID, RepositoryNodeID: binding.GitHubRepositoryNodeID,
		Owner: binding.RepositoryOwner, Name: binding.RepositoryName, ViewerLogin: binding.ViewerLogin,
		Authenticated: true, CanMerge: true, TLSVerified: true, RateRemaining: 100, APIVersion: "2022-11-28",
		AtomicExpectedHead: true, DefaultBranch: "main", PullRequestNumber: binding.PullRequestNumber,
		PullRequestNodeID: binding.PullRequestNodeID, Draft: false, HeadSHA: binding.CandidateSHA, HeadRef: binding.Branch,
		BaseRef: "main", HeadRepositoryID: binding.GitHubRepositoryID, BaseRepositoryID: binding.GitHubRepositoryID,
		HeadOwner: binding.ViewerLogin, AuthorLogin: binding.ViewerLogin, ObservedAtMillis: now, MaximumAgeMillis: MaximumObservationAgeMS}
	value.MarkerSHA256, value.MarkerCount = binding.MarkerSHA256, 1
	if status == ForgeReady {
		value.PullRequestState, value.Mergeable, value.MergeableState = "open", true, "clean"
	} else {
		value.PullRequestState, value.Merged, value.MergedAtMillis, value.MergeCommitSHA = "closed", true, now, merge
	}
	return SealForgeObservation(value)
}

func testGit(binding Binding, status GitStatus, merge string, now int64) GitObservation {
	base := binding.BaseSHA
	value := GitObservation{ID: "git-observation-1", Status: status, Code: CodeOK, RepositoryID: binding.RepositoryID,
		CanonicalRemote: binding.CanonicalRemote, HeadRef: "refs/heads/" + binding.Branch, HeadSHA: binding.CandidateSHA,
		BaseRef: binding.BaseRef, BaseSHA: base, DefaultRef: binding.BaseRef, DefaultSHA: base,
		ObservedAtMillis: now, MaximumAgeMillis: MaximumObservationAgeMS}
	if status == GitIntegrated {
		value.BaseSHA, value.DefaultSHA, value.MergeCommitSHA = merge, merge, merge
		value.ParentSHAs, value.TreeSHA = []string{binding.BaseSHA, binding.CandidateSHA}, binding.TreeSHA
	}
	return SealGitObservation(value)
}

func testObservation(binding Binding, attempt uint32, integrated bool, now int64) Observation {
	merge, status, forgeStatus, gitStatus := "", ObservationReady, ForgeReady, GitReady
	if integrated {
		merge, status, forgeStatus, gitStatus = strings.Repeat("d", 40), ObservationIntegrated, ForgeIntegrated, GitIntegrated
	}
	forge, git := testForge(binding, forgeStatus, merge, now), testGit(binding, gitStatus, merge, now)
	return SealObservation(Observation{BindingSHA256: binding.SHA256, Attempt: attempt, Status: status, Code: CodeOK,
		ForgeBefore: forge, ForgeAfter: forge, GitBefore: git, GitAfter: git, LiveValidationID: "validation-evidence-live",
		LiveValidationSHA256: strings.Repeat("8", 64), ConfiguredCheckCount: 2, ObservedCheckCount: 2,
		FeedbackSnapshotSHA256: strings.Repeat("9", 64), FeedbackClear: true, MergeCommitSHA: merge,
		ObservedAtMillis: now, MaximumAgeMillis: MaximumObservationAgeMS})
}

func TestManualIntegrationRequiresExactAuthenticatedAction(t *testing.T) {
	policy := testPolicy(t, ModeManual)
	binding := testBinding(t, policy)
	state, ok := NewState(binding, policy)
	if !ok || state.Phase != PhaseReady || state.Integration != nil {
		t.Fatalf("manual state = %#v", state)
	}
	authorization := SealAuthorization(HumanAuthorization{ID: "human-action-1", ActorKind: "human", ActorSource: AuthorizationActorSource,
		Authenticated: true, ActorID: "owner@example.invalid", SessionID: "session-1", DecisionID: "decision-1",
		Action: "integrate_pull_request", BindingSHA256: binding.SHA256, CandidateSHA: binding.CandidateSHA,
		BaseSHA: binding.BaseSHA, PullRequestNumber: binding.PullRequestNumber, PolicySHA256: policy.SHA256, AuthorizedAtMillis: 1_000})
	wrong := authorization
	wrong.CandidateSHA = strings.Repeat("e", 40)
	wrong = SealAuthorization(wrong)
	if _, accepted := AuthorizeManual(state, wrong); accepted {
		t.Fatal("stale human action was accepted")
	}
	next, accepted := AuthorizeManual(state, authorization)
	if !accepted || next.Phase != PhaseIntentRecorded || next.Integration == nil {
		t.Fatalf("authorized state = %#v", next)
	}
}

func TestAutomaticIntegrationUsesOneUseObservationAndExactMergeGraph(t *testing.T) {
	policy := testPolicy(t, ModeAutomatic)
	binding := testBinding(t, policy)
	state, ok := NewState(binding, policy)
	if !ok {
		t.Fatal("automatic state rejected")
	}
	ready := testObservation(binding, 0, false, 1_000)
	state, ok = RecordObservation(state, ready, 1_000)
	if !ok {
		t.Fatal("ready observation rejected")
	}
	state, ok = BeginDispatch(state)
	if !ok || state.Integration.Attempt != 1 || state.Integration.ConsumedObservationID != ready.ID {
		t.Fatalf("dispatch = %#v", state)
	}
	if _, duplicate := BeginDispatch(state); duplicate {
		t.Fatal("one-use observation dispatched twice")
	}
	state, ok = RequireObservation(state)
	if !ok {
		t.Fatal("observation frontier rejected")
	}
	integrated := testObservation(binding, 1, true, 1_001)
	state, ok = RecordObservation(state, integrated, 1_001)
	if !ok {
		t.Fatal("integrated graph rejected")
	}
	state, ok = Complete(state, integrated, 1_001)
	if !ok || state.Phase != PhaseComplete || state.Evidence.MergeCommitSHA != strings.Repeat("d", 40) {
		t.Fatalf("complete = %#v", state)
	}
	bad := integrated
	bad.GitAfter.TreeSHA = strings.Repeat("f", 40)
	bad.GitAfter = SealGitObservation(bad.GitAfter)
	bad = SealObservation(bad)
	if ValidObservation(bad, binding, EffectID(binding), 1_001) {
		t.Fatal("wrong merge tree became integration evidence")
	}
}

func TestIntegrationObservationsFailClosedAndRedactCodes(t *testing.T) {
	policy := testPolicy(t, ModeAutomatic)
	binding := testBinding(t, policy)
	for _, code := range []Code{CodeTLS, CodeRateLimited, CodeUnauthorized, CodeAtomicHeadUnavailable, CodeFeedbackPresent, CodeChecksChanged} {
		observation := SealObservation(Observation{BindingSHA256: binding.SHA256, Status: func() ObservationStatus {
			if WaitingCode(code) {
				return ObservationUnavailable
			}
			return ObservationInvalid
		}(),
			Code: code, ObservedAtMillis: 1_000, MaximumAgeMillis: MaximumObservationAgeMS})
		if !ValidObservation(observation, binding, EffectID(binding), 1_000) {
			t.Fatalf("code %s rejected", code)
		}
		if strings.Contains(strings.ToLower(string(code)), "token=") {
			t.Fatal("code exposed secret-shaped data")
		}
	}
}
