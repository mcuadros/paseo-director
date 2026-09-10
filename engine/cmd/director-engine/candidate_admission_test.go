// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"testing"

	"github.com/mcuadros/director-engine/adapters/fake"
	executionapp "github.com/mcuadros/director-engine/application/execution"
	"github.com/mcuadros/director-engine/domain"
	candidatedomain "github.com/mcuadros/director-engine/domain/candidate"
	"github.com/mcuadros/director-engine/domain/execution"
	"github.com/mcuadros/director-engine/reducer/eligibility"
)

func candidateAdmissionGit(t *testing.T, directory string, arguments ...string) {
	t.Helper()
	command := exec.Command("git", arguments...)
	command.Dir = directory
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("Git %v: %v: %s", arguments, err, output)
	}
}

func TestExactCandidateAdmissionFences32ConcurrentCoordinatorsAndReplays(t *testing.T) {
	doltFixture := startVerticalDolt(t)
	store := openVerticalStore(t, doltFixture)
	source, base := initializeRepository(t, "candidate-concurrency")
	project, task := createVerticalRecords(t, store, "candidate-concurrency", source)
	worktree := filepath.Join(filepath.Dir(source), "candidate-concurrency-worktree")
	scope := execution.Scope{
		ProjectID: project.ID, WorkspaceID: task.WorkspaceIDs[0], TaskID: task.ID, RunID: "run-candidate-concurrency",
	}
	environment := fake.NewEnvironment(fake.Options{
		SourcePath: source, WorktreePath: worktree, Branch: "task/" + task.ID, BaseSHA: base,
		Operational: operationalObservation("candidate-concurrency"),
	})
	t.Cleanup(environment.RemoveFixture)
	controller := executionapp.NewController(store, environment, environment, environment)
	started, err := controller.Start(context.Background(), startCommand(
		t, task, scope, source, worktree, base, eligibilityFacts(scope, execution.LifecycleSurfaces{}),
	))
	if err != nil || started.Decision.Kind != eligibility.DecisionEligible {
		t.Fatalf("start = %#v, %v", started, err)
	}
	run := runSteps(t, store, environment, scope.RunID, func(current domain.Run) bool {
		return current.Execution.AgentPrompt.Observation != nil && current.Execution.AgentPrompt.Observation.Status == execution.ObservationOwnedPresent
	})
	claim, err := environment.ProduceCandidate(context.Background(), fake.CandidateRequest{
		ClaimID: "claim-candidate-concurrency", AgentID: run.Execution.Agent.ExternalID,
		BaseSHA: base, CriteriaResults: map[string]string{"criterion-1": "claimed_satisfied"},
	})
	if err != nil {
		t.Fatal(err)
	}
	nowMillis := testNowMillis()
	completion, err := environment.TerminalEvent(execution.CompletionEventFinished, run.Execution.AgentPrompt.ID, nowMillis)
	if err != nil {
		t.Fatal(err)
	}
	if err := controller.RecordCompletionEvent(context.Background(), run.ID, completion, nowMillis); err != nil {
		t.Fatal(err)
	}
	run = runSteps(t, store, environment, run.ID, func(current domain.Run) bool {
		return current.Execution.AgentPrompt.Phase == execution.EffectComplete
	})
	if err := controller.RecordCompletedClaim(context.Background(), run.ID, claim); err != nil {
		t.Fatal(err)
	}
	run = runSteps(t, store, environment, run.ID, func(current domain.Run) bool {
		return current.Execution.CandidateObservation != nil
	})
	decision := candidatedomain.Evaluate(
		*run.Execution.CandidateClaim, *run.Execution.CandidateObservation,
		run.Execution.RepositoryBindingHash, testNowMillis(),
	)
	if decision.Manifest == nil {
		t.Fatalf("Candidate observation was not admissible: %#v", decision)
	}
	candidate := domain.Candidate{
		SchemaVersion: domain.CandidateSchemaVersion,
		ID:            candidatedomain.RecordID(run.ID, 1, claim.CandidateSHA), RunID: run.ID, Sequence: 1,
		CommitSHA: claim.CandidateSHA, Claim: *run.Execution.CandidateClaim, Manifest: *decision.Manifest,
		AdmittedAtMillis: testNowMillis(),
	}
	type admissionResult struct {
		index  int
		result domain.CommandResult
		err    error
	}
	start := make(chan struct{})
	results := make(chan admissionResult, 32)
	var wait sync.WaitGroup
	for index := 0; index < 32; index++ {
		wait.Add(1)
		go func(index int) {
			defer wait.Done()
			<-start
			commandID := fmt.Sprintf("candidate-concurrent-%02d", index)
			payload, _ := json.Marshal(map[string]string{"candidateId": candidate.ID, "bindingSha256": candidate.Manifest.BindingSHA256})
			result, appendErr := store.AppendCandidate(context.Background(), domain.CommandRequest{
				IdempotencyKey: commandID, Type: "candidate.admit", AggregateID: run.ID,
				ExpectedVersion: run.Version, Payload: payload,
			}, candidate, domain.Event{
				ID: commandID + "-event", RunID: run.ID, Sequence: run.Version + 2,
				AggregateID: run.ID, AggregateVersion: run.Version + 1,
				Type: "candidate.admitted", Payload: payload,
			})
			results <- admissionResult{index: index, result: result, err: appendErr}
		}(index)
	}
	close(start)
	wait.Wait()
	close(results)
	applied, conflicted, winner := 0, 0, -1
	for value := range results {
		if value.err != nil {
			t.Fatalf("coordinator %d: %v", value.index, value.err)
		}
		switch value.result.Outcome {
		case domain.CommandApplied:
			applied++
			winner = value.index
		case domain.CommandRejectedVersionConflict:
			conflicted++
		default:
			t.Fatalf("coordinator %d outcome = %#v", value.index, value.result)
		}
	}
	if applied != 1 || conflicted != 31 {
		t.Fatalf("concurrent outcomes applied=%d conflicted=%d", applied, conflicted)
	}
	storedRun, err := store.Run(context.Background(), run.ID)
	if err != nil || storedRun.CurrentCandidateID != candidate.ID || storedRun.Execution.CandidateAuthority == nil ||
		!candidatedomain.ValidAuthority(*storedRun.Execution.CandidateAuthority) ||
		!candidatedomain.DownstreamEmpty(storedRun.Execution.CandidateAuthority.Downstream) {
		t.Fatalf("durable Candidate authority = %#v, %v", storedRun.Execution.CandidateAuthority, err)
	}
	storedCandidates, err := store.Candidates(context.Background(), run.ID)
	if err != nil || len(storedCandidates) != 1 || storedCandidates[0] != candidate {
		t.Fatalf("durable Candidate sequence = %#v, %v", storedCandidates, err)
	}
	winnerID := fmt.Sprintf("candidate-concurrent-%02d", winner)
	payload, _ := json.Marshal(map[string]string{"candidateId": candidate.ID, "bindingSha256": candidate.Manifest.BindingSHA256})
	replay, err := store.AppendCandidate(context.Background(), domain.CommandRequest{
		IdempotencyKey: winnerID, Type: "candidate.admit", AggregateID: run.ID,
		ExpectedVersion: run.Version, Payload: payload,
	}, candidate, domain.Event{
		ID: winnerID + "-event", RunID: run.ID, Sequence: run.Version + 2,
		AggregateID: run.ID, AggregateVersion: run.Version + 1, Type: "candidate.admitted", Payload: payload,
	})
	if err != nil || !replay.Replay || replay.Outcome != domain.CommandApplied {
		t.Fatalf("winning admission replay = %#v, %v", replay, err)
	}

	bound := func(id string) *candidatedomain.EvidenceBinding {
		return &candidatedomain.EvidenceBinding{ID: id, CandidateID: candidate.ID,
			CandidateSHA: candidate.CommitSHA, BaseSHA: candidate.Manifest.BaseSHA,
			Generation: storedRun.Execution.CandidateAuthority.Generation, BindingSHA256: candidate.Manifest.BindingSHA256}
	}
	withEvidence := storedRun
	withEvidence.Version++
	withEvidence.Execution.CandidateAuthority.Downstream = candidatedomain.Downstream{
		Validation: bound("validation-1"), Review: bound("review-1"), CI: bound("ci-1"),
		Publication: bound("publication-1"), Feedback: bound("feedback-1"), Ready: bound("ready-1"),
		Integration: bound("integration-1"),
	}
	result, err := store.UpdateRun(context.Background(), domain.CommandRequest{
		IdempotencyKey: "candidate-authority-evidence", Type: "candidate.authority.test_bind", AggregateID: run.ID,
		ExpectedVersion: storedRun.Version, Payload: json.RawMessage(`{}`),
	}, withEvidence, domain.Event{
		ID: "candidate-authority-evidence-event", RunID: run.ID, Sequence: storedRun.Version + 2,
		AggregateID: run.ID, AggregateVersion: withEvidence.Version, Type: "candidate.authority.test_bound", Payload: json.RawMessage(`{}`),
	})
	if err != nil || result.Outcome != domain.CommandApplied {
		t.Fatalf("seed downstream authority = %#v, %v", result, err)
	}
	if _, err := controller.ReconcileCandidateAuthority(context.Background(), executionapp.CandidateAuthorityCommand{
		SchemaVersion: executionapp.CandidateAuthorityCommandSchemaVersion, RequestID: "stale-lease-reconcile",
		RunID: run.ID, ExpectedRunVersion: withEvidence.Version, LeaseEpoch: run.Execution.LeaseBinding.Epoch + 1,
		NowMillis: testNowMillis(),
	}); !errors.Is(err, executionapp.ErrProjectLeaseUnavailable) {
		t.Fatalf("stale lease reconciliation error = %v", err)
	}
	if err := os.WriteFile(filepath.Join(source, "BASE-MOVED.md"), []byte("moved\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	candidateAdmissionGit(t, source, "add", "BASE-MOVED.md")
	candidateAdmissionGit(t, source, "commit", "-m", "fixture: move base after admission")
	invalidation, err := controller.ReconcileCandidateAuthority(context.Background(), executionapp.CandidateAuthorityCommand{
		SchemaVersion: executionapp.CandidateAuthorityCommandSchemaVersion, RequestID: "reconcile-base-movement",
		RunID: run.ID, ExpectedRunVersion: withEvidence.Version, LeaseEpoch: run.Execution.LeaseBinding.Epoch,
		NowMillis: testNowMillis(),
	})
	if err != nil || !invalidation.Invalidated || invalidation.Code != candidatedomain.CodeBaseMoved ||
		invalidation.Run.Execution.CandidateAuthority == nil || !invalidation.Run.Execution.CandidateAuthority.Invalidated ||
		!candidatedomain.DownstreamEmpty(invalidation.Run.Execution.CandidateAuthority.Downstream) {
		t.Fatalf("base-movement invalidation = %#v, %v", invalidation, err)
	}
}
