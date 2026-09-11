// SPDX-License-Identifier: Apache-2.0

package feedback

import (
	"slices"
	"strings"
	"testing"

	"github.com/mcuadros/director-engine/domain/correction"
)

func testBinding() Binding {
	return SealBinding(Binding{ProjectID: "project-1", WorkspaceID: "workspace-1", TaskID: "task-1", TaskVersion: 4,
		RunID: "run-1", CandidateID: "candidate-1", CandidateSHA: strings.Repeat("1", 40), BaseSHA: strings.Repeat("0", 40),
		CandidateGeneration: 1, ManifestSHA256: strings.Repeat("a", 64), RepositoryID: 123,
		RepositoryNodeID: "R_node", PullRequestNumber: 7})
}

func human(source Source) Actor {
	attestation := AttestationGitHubUser
	if source == SourcePaseoDirect {
		attestation = AttestationPaseoHuman
	}
	return Actor{Kind: ActorHuman, ID: "human-node-1", Login: "human", Authenticated: true, Attestation: attestation}
}

func item(source Source, external, revision string, updated int64) Item {
	return Item{Source: source, ExternalID: external, RevisionID: revision, Actor: human(source), Kind: KindComment,
		CandidateSHA: strings.Repeat("1", 40), BaseSHA: strings.Repeat("0", 40), ContextSHA256: strings.Repeat("b", 64),
		Body: "Please cover the restart case", Actionable: true, Severity: correction.SeverityP3,
		CreatedAtMillis: 900, UpdatedAtMillis: updated}
}

func snapshot(id string, source Source, complete bool, now int64, items ...Item) Snapshot {
	return SealSnapshot(Snapshot{ID: id, Source: source, BindingSHA256: testBinding().BindingSHA256,
		Complete: complete, PageCount: 1, ObservedAtMillis: now, MaximumAgeMillis: MaximumObservationAge, Items: items})
}

func TestReconcileRedactsAndRoutesOnlyExactAuthenticatedHumanFeedback(t *testing.T) {
	input := item(SourcePaseoDirect, "message-1", "revision-1", 1_000)
	input.Body = "Please inspect /tmp/private/evidence and token=FAKE_REDACTION_FIXTURE_12345 before retry"
	state, changed, ok := Reconcile(nil, testBinding(), []Snapshot{snapshot("snapshot-1", SourcePaseoDirect, false, 1_001, input)}, 1_001)
	if !ok || !changed || !ValidState(state) || state.Phase != PhaseCorrectionReady || len(CurrentActionable(state)) != 1 {
		t.Fatalf("state = %#v, changed=%v ok=%v", state, changed, ok)
	}
	record := CurrentActionable(state)[0]
	if strings.Contains(record.Summary, "/tmp/") || strings.Contains(record.Summary, "FAKE_REDACTION_FIXTURE_12345") ||
		!strings.Contains(record.Summary, "[REDACTED_PATH]") || !strings.Contains(record.Summary, "[REDACTED_SECRET]") {
		t.Fatalf("summary was not centrally redacted: %q", record.Summary)
	}
	humanSnapshot, findings, ok := CorrectionInputs(state)
	if !ok || humanSnapshot.Source != correction.SourceHuman || humanSnapshot.Count != 1 || len(findings) != 1 ||
		findings[0].CandidateSHA != testBinding().CandidateSHA || findings[0].Evidence[0].SHA256 != record.AuditSHA256 {
		t.Fatalf("correction input = %#v / %#v", humanSnapshot, findings)
	}
}

func TestCorrectionDispatchWindowClosesOnlyAfterExactBatchIsObserved(t *testing.T) {
	input := item(SourcePaseoDirect, "message-dispatch", "revision-1", 1_000)
	state, _, ok := Reconcile(nil, testBinding(), []Snapshot{snapshot("snapshot-dispatch", SourcePaseoDirect, false, 1_001, input)}, 1_001)
	if !ok || !DispatchInFlight(state) {
		t.Fatalf("correction-ready state did not expose dispatch window: %#v", state)
	}
	routed, ok := MarkCorrectionRouted(state, strings.Repeat("f", 64))
	if !ok || DispatchInFlight(routed) {
		t.Fatalf("observed correction dispatch remained in flight: %#v", routed)
	}
	invalid := Invalidate(state, "candidate_changed")
	if !ValidState(invalid) || DispatchInFlight(invalid) {
		t.Fatalf("invalidated history retained dispatch authority: %#v", invalid)
	}
}

func TestEditsDuplicatesOutOfOrderAndConflictsPreserveImmutableRevisions(t *testing.T) {
	first := item(SourcePaseoDirect, "message-1", "revision-1", 1_000)
	state, _, ok := Reconcile(nil, testBinding(), []Snapshot{snapshot("snapshot-1", SourcePaseoDirect, false, 1_001, first)}, 1_001)
	if !ok {
		t.Fatal("initial reconcile failed")
	}
	state, changed, ok := Reconcile(&state, testBinding(), []Snapshot{snapshot("snapshot-1", SourcePaseoDirect, false, 1_001, first)}, 1_001)
	if !ok || changed || len(state.Records) != 1 || len(state.Snapshots) != 1 {
		t.Fatalf("exact replay = %#v changed=%v", state, changed)
	}
	routed, ok := MarkCorrectionRouted(state, strings.Repeat("f", 64))
	if !ok {
		t.Fatal("mark routed")
	}
	routed, changed, ok = Reconcile(&routed, testBinding(), []Snapshot{snapshot("snapshot-repeat", SourcePaseoDirect, false, 1_050, first)}, 1_050)
	if !ok || changed || routed.Phase != PhaseCorrectionRouted || routed.CorrectionBatchSHA != strings.Repeat("f", 64) || len(routed.Snapshots) != 1 {
		t.Fatalf("semantic replay changed routed state: %#v", routed)
	}
	state = routed
	edit := item(SourcePaseoDirect, "message-1", "revision-2", 1_100)
	edit.Body = "Edited bounded request"
	state, changed, ok = Reconcile(&state, testBinding(), []Snapshot{snapshot("snapshot-2", SourcePaseoDirect, false, 1_101, edit)}, 1_101)
	if !ok || !changed || len(state.Records) != 2 || state.Records[1].ReplacesRecordID != state.Records[0].ID ||
		CurrentActionable(state)[0].Summary != "Edited bounded request" {
		t.Fatalf("edit state = %#v", state)
	}
	late := item(SourcePaseoDirect, "message-1", "revision-0", 950)
	late.CreatedAtMillis = 900
	late.Body = "Late old revision"
	state, _, ok = Reconcile(&state, testBinding(), []Snapshot{snapshot("snapshot-3", SourcePaseoDirect, false, 1_102, late)}, 1_102)
	if !ok || len(state.Records) != 3 || CurrentActionable(state)[0].Summary != "Edited bounded request" {
		t.Fatalf("out-of-order revision became current: %#v", state.Current)
	}
	conflict := first
	conflict.Body = "Conflicting reuse"
	if _, _, valid := Reconcile(&state, testBinding(), []Snapshot{snapshot("snapshot-4", SourcePaseoDirect, false, 1_103, conflict)}, 1_103); valid {
		t.Fatal("conflicting source revision identity was accepted")
	}
}

func TestCompleteGitHubSnapshotCreatesDeletionWithoutErasingHistory(t *testing.T) {
	comment := item(SourceGitHubReviewComment, "comment-1", "revision-1", 1_000)
	state, _, ok := Reconcile(nil, testBinding(), []Snapshot{snapshot("github-snapshot-1", SourceGitHubReviewComment, true, 1_001, comment)}, 1_001)
	if !ok {
		t.Fatal("initial GitHub snapshot failed")
	}
	empty := snapshot("github-snapshot-2", SourceGitHubReviewComment, true, 1_100)
	state, changed, ok := Reconcile(&state, testBinding(), []Snapshot{empty}, 1_100)
	if !ok || !changed || len(state.Records) != 2 || !state.Records[1].Deleted || state.Records[1].ReplacesRecordID != state.Records[0].ID ||
		len(CurrentActionable(state)) != 0 || state.Phase != PhaseObserved {
		t.Fatalf("deletion state = %#v", state)
	}
}

func TestBotAppStaleAndUnboundCommentsNeverAcquireHumanAuthority(t *testing.T) {
	bot := item(SourceGitHubReviewComment, "bot-comment", "revision-1", 1_000)
	bot.Actor = Actor{Kind: ActorBot, ID: "bot-node", Login: "dependabot[bot]", Authenticated: true, Attestation: AttestationGitHubBot}
	app := item(SourceGitHubReviewComment, "app-comment", "revision-1", 1_000)
	app.Actor = Actor{Kind: ActorApp, ID: "app-node", Login: "review-app", Authenticated: true, Attestation: AttestationGitHubApp}
	stale := item(SourceGitHubReviewComment, "stale-comment", "revision-1", 1_000)
	stale.CandidateSHA = strings.Repeat("2", 40)
	unbound := item(SourceGitHubIssueComment, "issue-comment", "revision-1", 1_000)
	unbound.CandidateSHA = ""
	state, _, ok := Reconcile(nil, testBinding(), []Snapshot{
		snapshot("bot-snapshot", SourceGitHubReviewComment, true, 1_001, bot, app, stale),
		snapshot("issue-snapshot", SourceGitHubIssueComment, true, 1_001, unbound),
	}, 1_001)
	if !ok || len(state.Records) != 4 || len(CurrentActionable(state)) != 0 || state.Phase != PhaseNeedsYou {
		t.Fatalf("identity/context state = %#v", state)
	}
	dispositions := make([]Disposition, 0, len(state.Records))
	for _, record := range state.Records {
		dispositions = append(dispositions, record.Disposition)
	}
	if !slices.Contains(dispositions, DispositionIgnoredNonHuman) || !slices.Contains(dispositions, DispositionHistorical) ||
		!slices.Contains(dispositions, DispositionNeedsDecision) {
		t.Fatalf("dispositions = %v", dispositions)
	}
}

func TestP2RequiresSeparateAuthenticatedPaseoDecision(t *testing.T) {
	p2 := item(SourcePaseoDirect, "message-p2", "revision-1", 1_000)
	p2.Severity, p2.RequiresHumanDecision = correction.SeverityP2, true
	state, _, ok := Reconcile(nil, testBinding(), []Snapshot{snapshot("snapshot-p2", SourcePaseoDirect, false, 1_001, p2)}, 1_001)
	if !ok || state.Phase != PhaseNeedsYou {
		t.Fatalf("P2 state = %#v", state)
	}
	bot := Actor{Kind: ActorBot, ID: "bot-node", Login: "bot", Authenticated: true, Attestation: AttestationGitHubBot}
	if _, accepted := ApplyDecision(state, bot, "decision-1", true, 1_002); accepted {
		t.Fatal("bot decision was accepted")
	}
	decided, accepted := ApplyDecision(state, human(SourcePaseoDirect), "decision-1", true, 1_002)
	if !accepted || decided.Phase != PhaseCorrectionReady || decided.Decision == nil || !decided.Decision.AllowCorrection {
		t.Fatalf("human decision = %#v", decided)
	}
	_, findings, ok := CorrectionInputs(decided)
	if !ok || findings[0].RequiresHumanDecision {
		t.Fatalf("accepted P2 remained a model-inferred decision gate: %#v", findings)
	}
}
