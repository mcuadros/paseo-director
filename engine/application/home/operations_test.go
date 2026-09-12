// SPDX-License-Identifier: Apache-2.0

package home

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strings"
	"sync"
	"testing"

	"github.com/mcuadros/director-engine/domain"
	"github.com/mcuadros/director-engine/internal/testkit/secretfixture"
	homeport "github.com/mcuadros/director-engine/ports/home"
	planningport "github.com/mcuadros/director-engine/ports/planning"
)

type memoryManualExecutor struct {
	mu       sync.Mutex
	requests map[string]homeport.ManualOperation
	results  map[string]homeport.ManualOperationEvidence
	apply    int
}

func (executor *memoryManualExecutor) ObserveManualOperation(_ context.Context, requestID string) (homeport.ManualOperation, homeport.ManualOperationEvidence, bool, error) {
	executor.mu.Lock()
	defer executor.mu.Unlock()
	result, ok := executor.results[requestID]
	return executor.requests[requestID], result, ok, nil
}

func (executor *memoryManualExecutor) ApplyManualOperation(_ context.Context, input homeport.ManualOperation) (homeport.ManualOperationEvidence, error) {
	executor.mu.Lock()
	defer executor.mu.Unlock()
	if result, ok := executor.results[input.RequestID]; ok {
		return result, nil
	}
	executor.apply++
	executor.requests[input.RequestID] = input
	result := homeport.ManualOperationEvidence{RequestID: input.RequestID, Kind: input.Kind, HostID: input.HostID, HostInstanceID: input.HostInstanceID,
		ProjectID: input.ProjectID, ProjectVersion: input.ProjectVersion, Cursor: input.Cursor + 1, ObservationID: input.ObservationID,
		GitState: homeport.SyncCurrent, TaskStoreState: homeport.SyncFailed, SuccessfulHalfRetried: false, CompletedAtMillis: 10_001}
	executor.results[input.RequestID] = result
	return result, nil
}

type memoryBundleWriter struct {
	mu       sync.Mutex
	writes   int
	entries  map[string][]byte
	evidence homeport.SupportBundleEvidence
}

func (writer *memoryBundleWriter) ObserveSupportBundle(_ context.Context, bundleID, fileName string) (homeport.SupportBundleEvidence, bool, error) {
	writer.mu.Lock()
	defer writer.mu.Unlock()
	return writer.evidence, writer.evidence.BundleID == bundleID && writer.evidence.FileName == fileName, nil
}

func (writer *memoryBundleWriter) WriteSupportBundle(_ context.Context, input homeport.SupportBundleWrite) (homeport.SupportBundleEvidence, error) {
	writer.mu.Lock()
	defer writer.mu.Unlock()
	if writer.evidence.BundleID == input.BundleID {
		return writer.evidence, nil
	}
	writer.writes++
	writer.entries = map[string][]byte{}
	hash := sha256.New()
	var size uint64
	for _, name := range []string{"audit.json", "health.json", "logs.json", "manifest.json"} {
		writer.entries[name] = append([]byte{}, input.Entries[name]...)
		hash.Write([]byte(name))
		hash.Write(input.Entries[name])
		size += uint64(len(name) + len(input.Entries[name]))
	}
	writer.evidence = homeport.SupportBundleEvidence{BundleID: input.BundleID, FileName: input.FileName, SHA256: hex.EncodeToString(hash.Sum(nil)),
		Bytes: size, Permission: "0600", GeneratedAtMillis: input.GeneratedAtMillis, UploadAttempted: false}
	return writer.evidence, nil
}

func detailedSync(state homeport.SyncStreamState, reason homeport.SyncReason, local, remote string, now int64, retryable bool) homeport.SyncStreamObservation {
	return homeport.SyncStreamObservation{State: state, Reason: reason, LocalRevisionFingerprint: local, RemoteRevisionFingerprint: remote,
		ObservedAtMillis: now, LastSuccessAtMillis: now - 100, Retryable: retryable}
}

func operationsFixture(now int64) (*homeStore, *staticObservationSource, *memoryManualExecutor, *memoryBundleWriter, *OperationsService) {
	store, observation := homeFixture(now, 1)
	project := observation.Projects["project-00"]
	project.GitSyncDetail = detailedSync(homeport.SyncCurrent, homeport.SyncReasonAligned, strings.Repeat("a", 64), strings.Repeat("a", 64), now, false)
	project.TaskStoreSyncDetail = detailedSync(homeport.SyncFailed, homeport.SyncReasonOperationFailed, strings.Repeat("b", 64), strings.Repeat("c", 64), now, true)
	project.AutomaticSyncEnabled = true
	project.Reconciliation = homeport.ReconciliationObservation{State: homeport.ReconciliationCurrent, Reason: homeport.ReconciliationReasonObserved,
		ObservedAtMillis: now, LastCompletedAtMillis: now - 10, PendingWakeups: 0}
	project.TechnicalLogs = []homeport.TechnicalLogObservation{
		{Sequence: 3, OccurredAtMillis: now - 10, Level: homeport.TechnicalLogError, Component: homeport.TechnicalLogDolt, Code: homeport.TechnicalLogDoltSyncFailed, Occurrences: 1},
		{Sequence: 2, OccurredAtMillis: now - 20, Level: homeport.TechnicalLogInfo, Component: homeport.TechnicalLogGit, Code: homeport.TechnicalLogGitSyncCurrent, Occurrences: 1},
	}
	project.TechnicalLogsAvailable = true
	project.Operations.Sync, project.Operations.Reconcile = true, true
	observation.Projects["project-00"] = project
	store.events = []domain.Event{
		{GlobalSequence: 40, ID: "event-private-name", AggregateID: "project-00", AggregateVersion: 1, Type: "project.pause", Payload: json.RawMessage(`{"path":"/home/alice/private","token":"github_pat_private"}`)},
		{GlobalSequence: 41, ID: "event-task-private", RunID: "run-00", AggregateID: "task-00-build", AggregateVersion: 1, Type: "candidate.admitted", Payload: json.RawMessage(`{"content":"do not expose"}`)},
	}
	source := &staticObservationSource{value: observation}
	executor := &memoryManualExecutor{requests: map[string]homeport.ManualOperation{}, results: map[string]homeport.ManualOperationEvidence{}}
	bundles := &memoryBundleWriter{}
	return store, source, executor, bundles, NewOperationsService(store, source, executor, bundles, func() int64 { return now })
}

func operationsMutation(kind, version string, preview *string, confirmed bool) planningport.OperationsMutationInput {
	hash, _ := planningport.SchemaSHA256()
	return planningport.OperationsMutationInput{SchemaVersion: 1, ContractVersion: "director-planning/v1", ContractHash: hash, HostID: "host-a",
		RequestID: "operations-request-0001", Kind: kind, ProjectID: "project-00", ExpectedProjectVersion: version, PreviewID: preview, Confirmed: confirmed}
}

var humanOperationsActor = AuthenticatedOperationsActor{Kind: "human", ID: "owner", SessionID: "session", Authenticated: true}

func TestOperationsProjectsAccuratePartialSyncHybridHealthAndSafeBoundedSinks(t *testing.T) {
	_, _, _, _, service := operationsFixture(10_000)
	report, err := service.Operations(context.Background(), planningport.OperationsQueryInput{HostID: "host-a", ProjectID: "project-00", ExpectedProjectVersion: "1"})
	if err != nil {
		t.Fatal(err)
	}
	if report.Status != "partial_sync" || report.Sync.State != "partial" || len(report.Sync.Streams) != 2 || report.Sync.Streams[0].State != "current" || report.Sync.Streams[1].State != "failed" {
		t.Fatalf("partial sync report = %#v", report)
	}
	if report.Hybrid.Mode != "hybrid" || report.Hybrid.TerminalEventDispatch != "synchronous_wake_only" || report.Hybrid.TerminalTargetMillis != "1000" ||
		report.Hybrid.ActiveIntervalMillis != "30000" || report.Hybrid.IdleIntervalMillis != "300000" || report.Hybrid.LostEventWatchdogMillis != "300000" {
		t.Fatalf("hybrid reconciliation = %#v", report.Hybrid)
	}
	if !report.Sync.AutomaticEnabled || report.Sync.DebounceMillis != "60000" || !report.Sync.FlushAtCriticalTransitions {
		t.Fatalf("automatic synchronization = %#v", report.Sync)
	}
	if len(report.Audit.Entries) != 2 || report.Audit.PayloadsIncluded || report.Audit.PathsIncluded || len(report.Logs.Entries) != 2 || report.Logs.RawOutputIncluded ||
		report.Logs.RetentionDays != "14" || report.Logs.RetentionBytes != "104857600" {
		t.Fatalf("bounded operational data = audit %#v logs %#v", report.Audit, report.Logs)
	}
	encoded, _ := json.Marshal(report)
	for _, forbidden := range []string{"/home/alice", "github_pat_private", "do not expose", "event-private-name", "event-task-private", "task-00-build", "run-00"} {
		if strings.Contains(string(encoded), forbidden) {
			t.Fatalf("Operations leaked %q: %s", forbidden, encoded)
		}
	}
}

func TestOperationsSupportPreviewRefusalGenerationAndReplayAreManualAndLocalOnly(t *testing.T) {
	_, _, _, writer, service := operationsFixture(10_000)
	unauthenticated, err := service.Mutate(context.Background(), operationsMutation("support.preview", "1", nil, false), AuthenticatedOperationsActor{})
	if err != nil || unauthenticated.Status != "refused" || unauthenticated.RefusalCode == nil || writer.writes != 0 {
		t.Fatalf("unauthenticated Preview = %#v writes=%d err=%v", unauthenticated, writer.writes, err)
	}
	previewResult, err := service.Mutate(context.Background(), operationsMutation("support.preview", "1", nil, false), humanOperationsActor)
	if err != nil || previewResult.Status != "preview" || previewResult.SupportPreview == nil || !previewResult.SupportPreview.Valid {
		t.Fatalf("support Preview = %#v err=%v", previewResult, err)
	}
	preview := previewResult.SupportPreview
	if !preview.LocalOnly || preview.UploadPolicy != "never" || preview.ScanStatus != "allowlist_safe" || len(preview.Items) != 4 || len(preview.Excluded) != 8 || writer.writes != 0 {
		t.Fatalf("support Preview policy = %#v writes=%d", preview, writer.writes)
	}
	mismatch := strings.Repeat("f", 64)
	refused, err := service.Mutate(context.Background(), operationsMutation("support.generate", "1", &mismatch, true), humanOperationsActor)
	if err != nil || refused.Status != "refused" || refused.RefusalCode == nil || *refused.RefusalCode != "support_preview_mismatch" || writer.writes != 0 {
		t.Fatalf("mismatched generation = %#v writes=%d err=%v", refused, writer.writes, err)
	}
	generated, err := service.Mutate(context.Background(), operationsMutation("support.generate", "1", &preview.ID, true), humanOperationsActor)
	if err != nil || generated.Status != "applied" || generated.Bundle == nil || generated.Bundle.Permission != "0600" || generated.Bundle.UploadAttempted || writer.writes != 1 {
		t.Fatalf("generated bundle = %#v writes=%d err=%v", generated, writer.writes, err)
	}
	for name, content := range writer.entries {
		if !diagnosticBytesSafe(content) || strings.Contains(string(content), "Rendered Project") || strings.Contains(string(content), "project-00") {
			t.Fatalf("unsafe bundle entry %s: %s", name, content)
		}
	}
	replayed, err := service.Mutate(context.Background(), operationsMutation("support.generate", "1", &preview.ID, true), humanOperationsActor)
	if err != nil || replayed.Status != "applied" || replayed.Bundle == nil || replayed.Bundle.SHA256 != generated.Bundle.SHA256 || writer.writes != 1 {
		t.Fatalf("bundle replay = %#v writes=%d err=%v", replayed, writer.writes, err)
	}
}

func TestManualSyncIsIdempotentAcrossConcurrentResponseLossAndNeverRetriesSuccessfulHalf(t *testing.T) {
	_, _, executor, _, service := operationsFixture(10_000)
	input := operationsMutation("sync.now", "1", nil, true)
	results := make(chan planningport.OperationsMutationResult, 2)
	errors := make(chan error, 2)
	var wait sync.WaitGroup
	for range 2 {
		wait.Add(1)
		go func() {
			defer wait.Done()
			result, err := service.Mutate(context.Background(), input, humanOperationsActor)
			results <- result
			errors <- err
		}()
	}
	wait.Wait()
	close(results)
	close(errors)
	for err := range errors {
		if err != nil {
			t.Fatal(err)
		}
	}
	for result := range results {
		if result.Status != "applied" || result.Effect == nil || result.Effect.GitState != "current" || result.Effect.DynamicState != "failed" || result.Effect.SuccessfulHalfRetried {
			t.Fatalf("manual Sync result = %#v", result)
		}
	}
	if executor.apply != 1 {
		t.Fatalf("manual Sync external applies = %d, want 1", executor.apply)
	}
}

func TestDiagnosticSafetyScannerRejectsEveryForbiddenClass(t *testing.T) {
	for _, unsafe := range []string{"/home/user/repo", "/tmp/private", `C:\\Users\\Alice\\secret`, "file:///srv/repo", "Authorization: bearer abc", "Bearer abc", "Basic abc", "token=value", "password=value", "secret=value", "api_key=value", "apikey=value", "github_pat_private", "ghp_private", "gho_private", "sk-private", secretfixture.PrivateKeyHeader(""), "https://user:pass@example.test"} {
		value, _ := json.Marshal(map[string]string{"value": unsafe})
		if diagnosticBytesSafe(value) {
			t.Fatalf("scanner accepted unsafe class %q", unsafe)
		}
	}
	if !diagnosticBytesSafe([]byte(`{"code":"current","message":"bounded structured state"}`)) {
		t.Fatal("scanner rejected safe allowlist output")
	}
}

func TestCombinedSyncProjectionExhaustivelyKeepsPartialSuccessVisible(t *testing.T) {
	states := []string{"current", "local_ahead", "remote_ahead", "diverged", "failed", "identity_mismatch", "not_configured", "unavailable", "stale"}
	for _, gitState := range states {
		for _, doltState := range states {
			got := combinedSyncState([]planningport.SyncStreamDetail{{State: gitState}, {State: doltState}})
			want := "failed"
			switch {
			case gitState == "current" && doltState == "current":
				want = "current"
			case gitState == "not_configured" && doltState == "not_configured":
				want = "not_configured"
			case gitState == "stale" || doltState == "stale":
				want = "stale"
			case gitState == "current" || doltState == "current":
				want = "partial"
			case gitState == "unavailable" || doltState == "unavailable":
				want = "unavailable"
			}
			if got != want {
				t.Fatalf("combinedSyncState(%s,%s)=%s want %s", gitState, doltState, got, want)
			}
		}
	}
}
