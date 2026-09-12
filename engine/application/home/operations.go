// SPDX-License-Identifier: Apache-2.0

package home

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/mcuadros/director-engine/domain"
	homeport "github.com/mcuadros/director-engine/ports/home"
	planningport "github.com/mcuadros/director-engine/ports/planning"
)

const (
	operationsObservationMaximumAgeMillis int64  = 30_000
	operationsAuditScanLimit                     = 1_000
	operationsLogRetentionMillis          int64  = 14 * 24 * 60 * 60 * 1_000
	operationsLogRetentionBytes           uint64 = 100 * 1024 * 1024
	operationsSyncDebounceMillis          uint64 = 60_000
	operationsTerminalTargetMillis        uint64 = 1_000
	operationsActiveIntervalMillis        uint64 = 30_000
	operationsIdleIntervalMillis          uint64 = 5 * 60_000
	operationsLostEventWatchdogMillis     uint64 = 5 * 60_000
)

var (
	ErrOperationsSnapshot = errors.New("operations facts changed during snapshot read")
	ErrOperationsFacts    = errors.New("operations facts are invalid")
)

type OperationsStore interface {
	Project(context.Context, string) (domain.Project, error)
	Workspaces(context.Context, string) ([]domain.Workspace, error)
	Tasks(context.Context, string) ([]domain.Task, error)
	Runs(context.Context, string) ([]domain.Run, error)
	Events(context.Context, domain.EventQuery) ([]domain.Event, error)
	LatestEventSequence(context.Context) (uint64, error)
}

type AuthenticatedOperationsActor struct {
	Kind          string
	ID            string
	SessionID     string
	Authenticated bool
}

type OperationsService struct {
	store    OperationsStore
	source   OperationalSource
	executor homeport.ManualOperationExecutor
	bundles  homeport.SupportBundleWriter
	now      func() int64
}

func NewOperationsService(store OperationsStore, source OperationalSource, executor homeport.ManualOperationExecutor, bundles homeport.SupportBundleWriter, now func() int64) *OperationsService {
	if now == nil {
		now = func() int64 { return time.Now().UnixMilli() }
	}
	return &OperationsService{store: store, source: source, executor: executor, bundles: bundles, now: now}
}

func operationsDigest(parts ...string) string {
	digest := sha256.Sum256([]byte(strings.Join(parts, "\x1f")))
	return hex.EncodeToString(digest[:])
}

func pointer(value string) *string { return &value }

func operationsTime(value int64) *string {
	if value <= 0 {
		return nil
	}
	text := time.UnixMilli(value).UTC().Format(time.RFC3339Nano)
	return &text
}

func validSyncReason(reason homeport.SyncReason) bool {
	switch reason {
	case homeport.SyncReasonAligned, homeport.SyncReasonLocalAhead, homeport.SyncReasonRemoteAhead,
		homeport.SyncReasonDiverged, homeport.SyncReasonOperationFailed, homeport.SyncReasonIdentityMismatch,
		homeport.SyncReasonAuthenticationUnavailable, homeport.SyncReasonRemoteUnavailable,
		homeport.SyncReasonObservationStale, homeport.SyncReasonNotConfigured:
		return true
	default:
		return false
	}
}

func validDetailedSyncState(state homeport.SyncStreamState) bool {
	switch state {
	case homeport.SyncCurrent, homeport.SyncLocalAhead, homeport.SyncRemoteAhead, homeport.SyncDiverged,
		homeport.SyncFailed, homeport.SyncIdentityMismatch, homeport.SyncNotConfigured,
		homeport.SyncUnavailable, homeport.SyncStale:
		return true
	default:
		return false
	}
}

func validFingerprint(value string) bool {
	if value == "" {
		return true
	}
	return validSHA(value, 64)
}

func validSyncDetail(value homeport.SyncStreamObservation, now int64) bool {
	if !validDetailedSyncState(value.State) || !validSyncReason(value.Reason) ||
		!validFingerprint(value.LocalRevisionFingerprint) || !validFingerprint(value.RemoteRevisionFingerprint) ||
		value.ObservedAtMillis < 0 || value.ObservedAtMillis > now || value.LastSuccessAtMillis < 0 || value.LastSuccessAtMillis > now {
		return false
	}
	if value.State == homeport.SyncCurrent {
		return value.Reason == homeport.SyncReasonAligned && value.LocalRevisionFingerprint != "" &&
			value.LocalRevisionFingerprint == value.RemoteRevisionFingerprint
	}
	exactReasons := map[homeport.SyncStreamState]homeport.SyncReason{
		homeport.SyncLocalAhead:       homeport.SyncReasonLocalAhead,
		homeport.SyncRemoteAhead:      homeport.SyncReasonRemoteAhead,
		homeport.SyncDiverged:         homeport.SyncReasonDiverged,
		homeport.SyncIdentityMismatch: homeport.SyncReasonIdentityMismatch,
		homeport.SyncNotConfigured:    homeport.SyncReasonNotConfigured,
		homeport.SyncStale:            homeport.SyncReasonObservationStale,
	}
	if reason, exact := exactReasons[value.State]; exact && value.Reason != reason {
		return false
	}
	if value.State == homeport.SyncLocalAhead || value.State == homeport.SyncRemoteAhead || value.State == homeport.SyncDiverged {
		return value.LocalRevisionFingerprint != "" && value.RemoteRevisionFingerprint != "" &&
			value.LocalRevisionFingerprint != value.RemoteRevisionFingerprint
	}
	return true
}

func legacySyncDetail(state homeport.SyncStreamState, observedAt int64) homeport.SyncStreamObservation {
	reason := homeport.SyncReasonOperationFailed
	switch state {
	case homeport.SyncCurrent:
		reason = homeport.SyncReasonAligned
	case homeport.SyncNotConfigured:
		reason = homeport.SyncReasonNotConfigured
	case homeport.SyncUnavailable:
		reason = homeport.SyncReasonRemoteUnavailable
	case homeport.SyncStale:
		reason = homeport.SyncReasonObservationStale
	}
	return homeport.SyncStreamObservation{State: state, Reason: reason, ObservedAtMillis: observedAt, Retryable: state == homeport.SyncFailed || state == homeport.SyncUnavailable}
}

func streamDetail(kind string, detailed homeport.SyncStreamObservation, legacy homeport.SyncStreamState, observedAt, maximumAge, now int64) (planningport.SyncStreamDetail, error) {
	legacyOnly := detailed.State == ""
	if legacyOnly {
		detailed = legacySyncDetail(legacy, observedAt)
	} else if !validSyncDetail(detailed, now) {
		return planningport.SyncStreamDetail{}, ErrOperationsFacts
	}
	if observedAt > 0 && maximumAge > 0 && now-observedAt > maximumAge {
		detailed.State = homeport.SyncStale
		detailed.Reason = homeport.SyncReasonObservationStale
		detailed.Retryable = false
	}
	details := map[homeport.SyncReason]string{
		homeport.SyncReasonAligned:                   "Local and remote exact revision fingerprints are aligned.",
		homeport.SyncReasonLocalAhead:                "The local revision is ahead; the remote stream has not accepted this exact state.",
		homeport.SyncReasonRemoteAhead:               "The remote revision is ahead; reconciliation must preserve it before another update.",
		homeport.SyncReasonDiverged:                  "Local and remote revisions diverged; no force overwrite or automatic winner is allowed.",
		homeport.SyncReasonOperationFailed:           "The latest stream effect did not complete; its successful counterpart remains preserved.",
		homeport.SyncReasonIdentityMismatch:          "The observed destination identity does not match the configured stream.",
		homeport.SyncReasonAuthenticationUnavailable: "Authentication for this configured stream is unavailable.",
		homeport.SyncReasonRemoteUnavailable:         "The configured remote could not be observed; unavailability is not absence.",
		homeport.SyncReasonObservationStale:          "The stream observation exceeded its positive freshness window.",
		homeport.SyncReasonNotConfigured:             "This synchronization stream has no configured remote.",
	}
	result := planningport.SyncStreamDetail{
		Kind: kind, State: string(detailed.State), ReasonCode: string(detailed.Reason), Detail: details[detailed.Reason],
		ObservedAt: operationsTime(detailed.ObservedAtMillis), LastSuccessAt: operationsTime(detailed.LastSuccessAtMillis), Retryable: detailed.Retryable,
	}
	if !legacyOnly && detailed.LocalRevisionFingerprint != "" {
		result.LocalRevisionFingerprint = pointer(detailed.LocalRevisionFingerprint)
	}
	if !legacyOnly && detailed.RemoteRevisionFingerprint != "" {
		result.RemoteRevisionFingerprint = pointer(detailed.RemoteRevisionFingerprint)
	}
	return result, nil
}

func combinedSyncState(streams []planningport.SyncStreamDetail) string {
	left, right := streams[0].State, streams[1].State
	switch {
	case left == "current" && right == "current":
		return "current"
	case left == "not_configured" && right == "not_configured":
		return "not_configured"
	case left == "stale" || right == "stale":
		return "stale"
	case left == "current" || right == "current":
		return "partial"
	case left == "unavailable" || right == "unavailable":
		return "unavailable"
	default:
		return "failed"
	}
}

func validReconciliation(value homeport.ReconciliationObservation, now int64) bool {
	switch value.State {
	case homeport.ReconciliationCurrent, homeport.ReconciliationRunning, homeport.ReconciliationWaiting,
		homeport.ReconciliationDegraded, homeport.ReconciliationUnavailable, homeport.ReconciliationStale:
	default:
		return false
	}
	switch value.Reason {
	case homeport.ReconciliationReasonObserved, homeport.ReconciliationReasonEventWake,
		homeport.ReconciliationReasonExternalWait, homeport.ReconciliationReasonFactMismatch,
		homeport.ReconciliationReasonSourceOffline, homeport.ReconciliationReasonObservationOld:
	default:
		return false
	}
	return value.ObservedAtMillis >= 0 && value.ObservedAtMillis <= now && value.LastCompletedAtMillis >= 0 && value.LastCompletedAtMillis <= now
}

func reconciliationDetail(value homeport.ReconciliationObservation, host homeport.HostObservation, now int64) (planningport.OperationsReconciliation, error) {
	if value.State == "" {
		value = homeport.ReconciliationObservation{State: homeport.ReconciliationUnavailable, Reason: homeport.ReconciliationReasonSourceOffline, ObservedAtMillis: host.ObservedAtMillis}
	}
	if !validReconciliation(value, now) {
		return planningport.OperationsReconciliation{}, ErrOperationsFacts
	}
	if now-host.ObservedAtMillis > host.MaximumAgeMillis {
		value.State, value.Reason = homeport.ReconciliationStale, homeport.ReconciliationReasonObservationOld
	}
	details := map[homeport.ReconciliationReason]string{
		homeport.ReconciliationReasonObserved:       "Event wakes and the periodic safety-net scan are reconciled from current authoritative facts.",
		homeport.ReconciliationReasonEventWake:      "A terminal event wake is queued; the event itself is not completion evidence.",
		homeport.ReconciliationReasonExternalWait:   "Reconciliation is waiting for an external fact and will not infer absence or retry a mutation.",
		homeport.ReconciliationReasonFactMismatch:   "Current durable and external facts disagree; lifecycle effects remain parked.",
		homeport.ReconciliationReasonSourceOffline:  "The exact reconciliation source is unavailable; no fallback host is selected.",
		homeport.ReconciliationReasonObservationOld: "The reconciliation observation is stale; controls remain unavailable until refreshed.",
	}
	return planningport.OperationsReconciliation{
		Mode: "hybrid", State: string(value.State), ReasonCode: string(value.Reason), Detail: details[value.Reason],
		TerminalEventDispatch: "synchronous_wake_only", TerminalTargetMillis: strconv.FormatUint(operationsTerminalTargetMillis, 10),
		ActiveIntervalMillis: strconv.FormatUint(operationsActiveIntervalMillis, 10), IdleIntervalMillis: strconv.FormatUint(operationsIdleIntervalMillis, 10),
		LostEventWatchdogMillis: strconv.FormatUint(operationsLostEventWatchdogMillis, 10), LastCompletedAt: operationsTime(value.LastCompletedAtMillis),
		PendingWakeups: strconv.FormatUint(value.PendingWakeups, 10),
	}, nil
}

func auditClassification(eventType string) (string, string, string, string) {
	category, action, actor := "system", "unclassified_event", "unavailable"
	switch {
	case strings.HasPrefix(eventType, "project.lease"):
		category, action = "project", "lease_changed"
	case strings.HasPrefix(eventType, "project.control") || eventType == "project.pause" || eventType == "project.resume":
		category, action = "control", "project_control_changed"
	case strings.HasPrefix(eventType, "project"):
		category, action = "project", "project_changed"
	case strings.HasPrefix(eventType, "organizer"):
		category, action = "organizer", "organizer_changed"
	case strings.HasPrefix(eventType, "workspace"):
		category, action = "workspace", "workspace_changed"
	case strings.HasPrefix(eventType, "task.dependency"):
		category, action = "task", "dependency_override_recorded"
	case strings.HasPrefix(eventType, "task.needs"):
		category, action = "task", "attention_requested"
	case strings.HasPrefix(eventType, "task"):
		category, action = "task", "task_changed"
	case strings.HasPrefix(eventType, "run"):
		category, action = "run", "run_changed"
	case strings.HasPrefix(eventType, "candidate.correction"):
		category, action = "candidate", "candidate_correction_recorded"
	case strings.HasPrefix(eventType, "candidate"):
		category, action = "candidate", "candidate_changed"
	case strings.HasPrefix(eventType, "validation"):
		category, action = "validation", "validation_recorded"
	case strings.HasPrefix(eventType, "review"):
		category, action = "review", "review_recorded"
	case strings.HasPrefix(eventType, "feedback"):
		category, action = "feedback", "feedback_recorded"
	case strings.HasPrefix(eventType, "publication"):
		category, action = "delivery", "publication_recorded"
	case strings.HasPrefix(eventType, "integration") || strings.HasPrefix(eventType, "direct"):
		category, action = "delivery", "integration_recorded"
	case strings.HasPrefix(eventType, "cleanup"):
		category, action = "cleanup", "cleanup_recorded"
	}
	outcome := "recorded"
	if strings.HasSuffix(eventType, ".created") || strings.HasSuffix(eventType, ".admitted") || strings.HasSuffix(eventType, ".completed") || strings.HasSuffix(eventType, ".applied") {
		outcome = "completed"
	} else if strings.Contains(eventType, "needs_you") || strings.Contains(eventType, "exhausted") {
		outcome = "attention"
	}
	return category, action, outcome, actor
}

func projectScope(ctx context.Context, store OperationsStore, project domain.Project) (map[string]struct{}, error) {
	identities := map[string]struct{}{project.ID: {}, project.Organizer.ID: {}}
	workspaces, err := store.Workspaces(ctx, project.ID)
	if err != nil {
		return nil, err
	}
	for _, workspace := range workspaces {
		if workspace.ProjectID != project.ID {
			return nil, ErrOperationsFacts
		}
		identities[workspace.ID] = struct{}{}
	}
	tasks, err := store.Tasks(ctx, project.ID)
	if err != nil {
		return nil, err
	}
	for _, task := range tasks {
		if task.ProjectID != project.ID {
			return nil, ErrOperationsFacts
		}
		identities[task.ID] = struct{}{}
		runs, err := store.Runs(ctx, task.ID)
		if err != nil {
			return nil, err
		}
		for _, run := range runs {
			if run.TaskID != task.ID {
				return nil, ErrOperationsFacts
			}
			identities[run.ID] = struct{}{}
			if run.CurrentCandidateID != "" {
				identities[run.CurrentCandidateID] = struct{}{}
			}
		}
	}
	return identities, nil
}

func auditEntries(events []domain.Event, scope map[string]struct{}) ([]planningport.OperationsAuditEntry, bool) {
	result := make([]planningport.OperationsAuditEntry, 0, planningport.MaximumAuditEntries)
	matched := 0
	for index := len(events) - 1; index >= 0; index-- {
		event := events[index]
		if _, ok := scope[event.AggregateID]; !ok {
			continue
		}
		matched++
		if len(result) == planningport.MaximumAuditEntries {
			continue
		}
		category, action, outcome, actor := auditClassification(event.Type)
		entry := planningport.OperationsAuditEntry{
			ID: operationsDigest("audit", event.ID, strconv.FormatUint(event.GlobalSequence, 10)), Sequence: strconv.FormatUint(event.GlobalSequence, 10),
			Category: category, Action: action, Outcome: outcome, ActorKind: actor, SubjectFingerprint: operationsDigest("subject", event.AggregateID), PayloadIncluded: false,
		}
		if event.RunID != "" {
			entry.RunFingerprint = pointer(operationsDigest("run", event.RunID))
		}
		result = append(result, entry)
	}
	return result, matched > len(result)
}

func validLog(value homeport.TechnicalLogObservation, now int64) bool {
	if value.Sequence == 0 || value.OccurredAtMillis <= 0 || value.OccurredAtMillis > now || value.Occurrences == 0 {
		return false
	}
	switch value.Level {
	case homeport.TechnicalLogInfo, homeport.TechnicalLogWarning, homeport.TechnicalLogError:
	default:
		return false
	}
	switch value.Component {
	case homeport.TechnicalLogEngine, homeport.TechnicalLogTaskStore, homeport.TechnicalLogGit, homeport.TechnicalLogDolt,
		homeport.TechnicalLogReconciliation, homeport.TechnicalLogSecurity, homeport.TechnicalLogSupport:
	default:
		return false
	}
	switch value.Code {
	case homeport.TechnicalLogEngineStarted, homeport.TechnicalLogReconciliationCompleted, homeport.TechnicalLogReconciliationWaiting,
		homeport.TechnicalLogGitSyncCurrent, homeport.TechnicalLogGitSyncFailed, homeport.TechnicalLogDoltSyncCurrent,
		homeport.TechnicalLogDoltSyncFailed, homeport.TechnicalLogTaskStoreUnhealthy, homeport.TechnicalLogSupportBundleGenerated,
		homeport.TechnicalLogUnsafeOutputSuppressed:
		return true
	default:
		return false
	}
}

func technicalLogs(values []homeport.TechnicalLogObservation, available bool, now int64) (planningport.OperationsLogs, error) {
	messages := map[homeport.TechnicalLogCode]string{
		homeport.TechnicalLogEngineStarted:           "Director Engine started with its exact configured identity.",
		homeport.TechnicalLogReconciliationCompleted: "A full reconciliation pass completed from durable and external facts.",
		homeport.TechnicalLogReconciliationWaiting:   "Reconciliation is waiting for an external fact; no mutation was retried.",
		homeport.TechnicalLogGitSyncCurrent:          "Organizer Git synchronization is current.",
		homeport.TechnicalLogGitSyncFailed:           "Organizer Git synchronization did not complete; the Dolt outcome remains independent.",
		homeport.TechnicalLogDoltSyncCurrent:         "TaskStore/Dolt synchronization is current.",
		homeport.TechnicalLogDoltSyncFailed:          "TaskStore/Dolt synchronization did not complete; the Git outcome remains independent.",
		homeport.TechnicalLogTaskStoreUnhealthy:      "TaskStore health failed closed before another durable mutation.",
		homeport.TechnicalLogSupportBundleGenerated:  "A local redacted support bundle was generated after human confirmation.",
		homeport.TechnicalLogUnsafeOutputSuppressed:  "Untrusted output was suppressed before reaching a Director-owned sink.",
	}
	rows := slices.Clone(values)
	slices.SortFunc(rows, func(left, right homeport.TechnicalLogObservation) int {
		if left.Sequence > right.Sequence {
			return -1
		}
		if left.Sequence < right.Sequence {
			return 1
		}
		return 0
	})
	seen := map[uint64]struct{}{}
	result := planningport.OperationsLogs{EntryLimit: strconv.Itoa(planningport.MaximumTechnicalLogs), RetentionDays: "14",
		RetentionBytes: strconv.FormatUint(operationsLogRetentionBytes, 10), RawOutputIncluded: false, State: "current"}
	if !available {
		result.State = "unavailable"
		result.Reason = disabledReason("technical_logs_unavailable", "The exact local bounded log sink is unavailable; raw output is not substituted")
	}
	var bytes uint64
	for _, value := range rows {
		if !validLog(value, now) {
			return planningport.OperationsLogs{}, ErrOperationsFacts
		}
		if _, duplicate := seen[value.Sequence]; duplicate {
			return planningport.OperationsLogs{}, ErrOperationsFacts
		}
		seen[value.Sequence] = struct{}{}
		if now-value.OccurredAtMillis > operationsLogRetentionMillis || len(result.Entries) == planningport.MaximumTechnicalLogs {
			result.Truncated = true
			continue
		}
		entry := planningport.OperationsLogEntry{Sequence: strconv.FormatUint(value.Sequence, 10), OccurredAt: time.UnixMilli(value.OccurredAtMillis).UTC().Format(time.RFC3339Nano),
			Level: string(value.Level), Component: string(value.Component), Code: string(value.Code), Message: messages[value.Code], Occurrences: strconv.FormatUint(value.Occurrences, 10)}
		encoded, _ := json.Marshal(entry)
		bytes += uint64(len(encoded))
		result.Entries = append(result.Entries, entry)
	}
	result.ReturnedBytes = strconv.FormatUint(bytes, 10)
	return result, nil
}

func operationsReasons(host homeport.HostObservation, project domain.Project, syncState string, streams []planningport.SyncStreamDetail, reconciliation planningport.OperationsReconciliation, logs planningport.OperationsLogs) (string, []planningport.Explanation) {
	if host.State == "disconnected" {
		return "offline", []planningport.Explanation{explanation("operations_host_offline", "The exact Director host is offline; cached operational facts cannot authorize a control", false)}
	}
	if host.State == "stale" || reconciliation.State == "stale" || syncState == "stale" {
		return "stale", []planningport.Explanation{explanation("operations_facts_stale", "One or more operational facts exceeded their positive freshness window", false)}
	}
	reasons := []planningport.Explanation{}
	for _, stream := range streams {
		if stream.State != "current" && stream.State != "not_configured" {
			reasons = append(reasons, explanation(stream.Kind+"_"+stream.ReasonCode, stream.Detail, stream.State == "identity_mismatch" || stream.State == "diverged"))
		}
	}
	if reconciliation.State != "current" && reconciliation.State != "running" {
		reasons = append(reasons, explanation("reconciliation_"+reconciliation.ReasonCode, reconciliation.Detail, reconciliation.State == "degraded"))
	}
	if logs.State != "current" && logs.Reason != nil {
		reasons = append(reasons, *logs.Reason)
	}
	if project.State == "paused" {
		reasons = append(reasons, explanation("project_paused", "Project execution is paused; observation and non-destructive diagnostics remain available", false))
	}
	for _, stream := range streams {
		if stream.State == "identity_mismatch" || stream.State == "diverged" {
			return "needs_you", reasons
		}
	}
	if syncState == "partial" {
		return "partial_sync", reasons
	}
	if len(reasons) > 0 || project.State == "degraded" || host.State == "degraded" {
		return "degraded", reasons
	}
	return "healthy", reasons
}

func operationsObservationID(host homeport.HostObservation, projectID string, projectVersion, cursor uint64, streams []planningport.SyncStreamDetail, reconciliation planningport.OperationsReconciliation) string {
	parts := []string{"operations", host.InstanceID, host.State, strconv.FormatInt(host.ObservedAtMillis, 10), projectID,
		strconv.FormatUint(projectVersion, 10), strconv.FormatUint(cursor, 10)}
	for _, stream := range streams {
		parts = append(parts, stream.Kind, stream.State, stream.ReasonCode)
		if stream.LocalRevisionFingerprint != nil {
			parts = append(parts, *stream.LocalRevisionFingerprint)
		}
		if stream.RemoteRevisionFingerprint != nil {
			parts = append(parts, *stream.RemoteRevisionFingerprint)
		}
	}
	parts = append(parts, reconciliation.State, reconciliation.ReasonCode, reconciliation.PendingWakeups)
	return operationsDigest(parts...)
}

func (service *OperationsService) report(ctx context.Context, input planningport.OperationsQueryInput) (planningport.OperationsReport, error) {
	if service.store == nil || service.source == nil || planningport.ValidateOperationsQuery(input) != nil {
		return planningport.OperationsReport{}, planningport.ErrQueryInvalid
	}
	expected, _ := planningport.ParseExpectedVersion(input.ExpectedProjectVersion)
	for range 3 {
		before, err := service.store.LatestEventSequence(ctx)
		if err != nil {
			return planningport.OperationsReport{}, fmt.Errorf("read Operations cursor: %w", err)
		}
		project, err := service.store.Project(ctx, input.ProjectID)
		if err != nil {
			return planningport.OperationsReport{}, err
		}
		if !validHomeProject(project) || project.Version != expected {
			return planningport.OperationsReport{}, ErrOperationsSnapshot
		}
		host, err := service.source.Observe(ctx, input.HostID, []string{project.ID})
		if err != nil {
			return planningport.OperationsReport{}, err
		}
		now := service.now()
		if !validObservation(host, input.HostID, []string{project.ID}, now) {
			return planningport.OperationsReport{}, ErrOperationsFacts
		}
		observation := host.Projects[project.ID]
		gitStream, err := streamDetail("organizer_git", observation.GitSyncDetail, observation.GitSync, observation.SyncObservedAtMillis, observation.SyncMaximumAgeMillis, now)
		if err != nil {
			return planningport.OperationsReport{}, err
		}
		doltStream, err := streamDetail("taskstore_dolt", observation.TaskStoreSyncDetail, observation.TaskStoreSync, observation.SyncObservedAtMillis, observation.SyncMaximumAgeMillis, now)
		if err != nil {
			return planningport.OperationsReport{}, err
		}
		streams := []planningport.SyncStreamDetail{gitStream, doltStream}
		reconciliation, err := reconciliationDetail(observation.Reconciliation, host, now)
		if err != nil {
			return planningport.OperationsReport{}, err
		}
		scope, err := projectScope(ctx, service.store, project)
		if err != nil {
			return planningport.OperationsReport{}, err
		}
		start := uint64(0)
		if before > operationsAuditScanLimit {
			start = before - operationsAuditScanLimit
		}
		events, err := service.store.Events(ctx, domain.EventQuery{AfterGlobalSequence: start, Limit: operationsAuditScanLimit})
		if err != nil {
			return planningport.OperationsReport{}, err
		}
		audit, auditTruncated := auditEntries(events, scope)
		logs, err := technicalLogs(observation.TechnicalLogs, observation.TechnicalLogsAvailable, now)
		if err != nil {
			return planningport.OperationsReport{}, err
		}
		after, err := service.store.LatestEventSequence(ctx)
		if err != nil {
			return planningport.OperationsReport{}, err
		}
		if before != after {
			continue
		}
		definition, err := planningport.EmbeddedDefinition()
		if err != nil {
			return planningport.OperationsReport{}, err
		}
		contractHash, err := planningport.SchemaSHA256()
		if err != nil {
			return planningport.OperationsReport{}, err
		}
		syncState := combinedSyncState(streams)
		status, reasons := operationsReasons(host, project, syncState, streams, reconciliation, logs)
		observationID := operationsObservationID(host, project.ID, project.Version, after, streams, reconciliation)
		support := planningport.SupportAvailability{PreviewAvailable: service.bundles != nil && status != "offline" && status != "stale", UploadPolicy: "never"}
		if !support.PreviewAvailable {
			support.Reason = disabledReason("support_bundle_unavailable", "A current exact-host projection and configured local mode-0600 bundle sink are required")
		}
		return planningport.OperationsReport{
			SchemaVersion: definition.SchemaVersion, ContractVersion: definition.ContractVersion, ContractHash: contractHash,
			Cursor: strconv.FormatUint(after, 10), HostID: host.HostID, HostInstanceID: host.InstanceID, ProjectID: project.ID, ProjectName: project.Name,
			ProjectVersion: strconv.FormatUint(project.Version, 10), ObservationID: observationID, ObservedAt: time.UnixMilli(host.ObservedAtMillis).UTC().Format(time.RFC3339Nano),
			MaximumAgeMillis: strconv.FormatInt(host.MaximumAgeMillis, 10), Status: status, Reasons: reasons, Hybrid: reconciliation,
			Sync: planningport.OperationsSync{State: syncState, Streams: streams, AutomaticEnabled: observation.AutomaticSyncEnabled,
				DebounceMillis: strconv.FormatUint(operationsSyncDebounceMillis, 10), FlushAtCriticalTransitions: true},
			Audit: planningport.OperationsAudit{Entries: audit, EntryLimit: strconv.Itoa(planningport.MaximumAuditEntries), Truncated: auditTruncated || start > 0,
				PayloadsIncluded: false, PathsIncluded: false}, Logs: logs,
			Controls: operationControls(service.executor != nil, observation.Operations, status), Support: support,
		}, nil
	}
	return planningport.OperationsReport{}, ErrOperationsSnapshot
}

func (service *OperationsService) Operations(ctx context.Context, input planningport.OperationsQueryInput) (planningport.OperationsReport, error) {
	return service.report(ctx, input)
}

func diagnosticBytesSafe(value []byte) bool {
	if len(value) == 0 || len(value) > planningport.MaximumResponseBytes {
		return false
	}
	lower := strings.ToLower(string(value))
	for _, forbidden := range []string{
		"/home/", "/tmp/", "\\users\\", "file://", "authorization:", "bearer ", "basic ", "token=", "password=", "secret=",
		"api_key=", "apikey=", "github_pat_", "ghp_", "gho_", "sk-", "-----begin private key-----", "https://user:",
	} {
		if strings.Contains(lower, forbidden) {
			return false
		}
	}
	return true
}

func supportItems() []planningport.SupportBundleItem {
	return []planningport.SupportBundleItem{
		{Name: "manifest.json", Description: "Bundle schema, exact contract identity, local-only policy, and pseudonymous Project binding."},
		{Name: "health.json", Description: "Closed health, reconciliation, and independent Git/Dolt stream states and reason codes."},
		{Name: "audit.json", Description: "Bounded structured event envelopes with hashed subjects and no payloads."},
		{Name: "logs.json", Description: "Bounded engine-authored technical codes with no raw process or provider output."},
	}
}

func supportExcluded() []string {
	return []string{"source and diffs", "prompts and conversations", "environment", "credentials and authentication configuration", "raw logs and command output", "TaskStore rows and event payloads", "Organizer contents", "full paths and remote URLs"}
}

func supportPreview(report planningport.OperationsReport, requestID string) planningport.SupportBundlePreview {
	projectFingerprint := operationsDigest("support-project", report.ProjectID)
	id := operationsDigest("support-preview", requestID, report.ContractHash, report.HostInstanceID, report.ProjectID, report.ProjectVersion, report.Cursor, report.ObservationID)
	fileName := "director-support-" + projectFingerprint[:12] + "-" + id[:12] + ".zip"
	preview := planningport.SupportBundlePreview{ID: id, RequestID: requestID, ProjectFingerprint: projectFingerprint, Cursor: report.Cursor,
		ProjectVersion: report.ProjectVersion, Items: supportItems(), Excluded: supportExcluded(), ScanStatus: "allowlist_safe", LocalOnly: true,
		UploadPolicy: "never", OutputFileName: fileName, Valid: report.Support.PreviewAvailable,
		Confirmation: "Generate this redacted bundle locally with mode 0600. Director will never upload it automatically."}
	if !preview.Valid {
		preview.ScanStatus = "refused"
		preview.Issues = []planningport.Explanation{*report.Support.Reason}
	}
	entries, ok := buildSupportEntries(report, preview, 0)
	if !ok {
		preview.Valid = false
		preview.ScanStatus = "refused"
		preview.Issues = []planningport.Explanation{explanation("support_bundle_redaction_failed", "The allowlist output failed the post-redaction safety scan", true)}
	}
	var bytes uint64
	for name, content := range entries {
		bytes += uint64(len(name) + len(content))
	}
	preview.EstimatedBytes = strconv.FormatUint(bytes, 10)
	return preview
}

func buildSupportEntries(report planningport.OperationsReport, preview planningport.SupportBundlePreview, generatedAt int64) (map[string][]byte, bool) {
	generated := "pending-human-confirmation"
	if generatedAt > 0 {
		generated = time.UnixMilli(generatedAt).UTC().Format(time.RFC3339Nano)
	}
	manifest := struct {
		SchemaVersion      int      `json:"schemaVersion"`
		BundleID           string   `json:"bundleId"`
		GeneratedAt        string   `json:"generatedAt"`
		ProjectFingerprint string   `json:"projectFingerprint"`
		ContractVersion    string   `json:"contractVersion"`
		ContractHash       string   `json:"contractHash"`
		LocalOnly          bool     `json:"localOnly"`
		UploadPolicy       string   `json:"uploadPolicy"`
		Entries            []string `json:"entries"`
	}{1, preview.ID, generated, preview.ProjectFingerprint, report.ContractVersion, report.ContractHash, true, "never", []string{"health.json", "audit.json", "logs.json"}}
	health := struct {
		Status      string                                `json:"status"`
		ReasonCodes []string                              `json:"reasonCodes"`
		Sync        planningport.OperationsSync           `json:"sync"`
		Hybrid      planningport.OperationsReconciliation `json:"hybrid"`
	}{Status: report.Status, Sync: report.Sync, Hybrid: report.Hybrid}
	for _, reason := range report.Reasons {
		health.ReasonCodes = append(health.ReasonCodes, reason.Code)
	}
	values := map[string]any{
		"manifest.json": manifest,
		"health.json":   health,
		"audit.json":    report.Audit,
		"logs.json":     report.Logs,
	}
	entries := make(map[string][]byte, len(values))
	for _, name := range []string{"manifest.json", "health.json", "audit.json", "logs.json"} {
		encoded, err := json.Marshal(values[name])
		if err != nil || !diagnosticBytesSafe(encoded) {
			return nil, false
		}
		entries[name] = encoded
	}
	return entries, true
}

func operationsResult(input planningport.OperationsMutationInput, status, message string, preview *planningport.SupportBundlePreview, bundle *planningport.SupportBundleResult, effect *planningport.OperationsEffectResult, refusal *string, cursor string) planningport.OperationsMutationResult {
	definition, _ := planningport.EmbeddedDefinition()
	hash, _ := planningport.SchemaSHA256()
	return planningport.OperationsMutationResult{SchemaVersion: definition.SchemaVersion, ContractVersion: definition.ContractVersion, ContractHash: hash,
		HostID: input.HostID, ProjectID: input.ProjectID, Cursor: cursor, RequestID: input.RequestID, Status: status, Message: message,
		SupportPreview: preview, Bundle: bundle, Effect: effect, RefusalCode: refusal}
}

func validOperationsActor(actor AuthenticatedOperationsActor) bool {
	return actor.Authenticated && actor.Kind == "human" && boundedHomeText(actor.ID, 128) && boundedHomeText(actor.SessionID, 128)
}

func manualEvidenceMatches(input homeport.ManualOperation, evidence homeport.ManualOperationEvidence) bool {
	return evidence.RequestID == input.RequestID && evidence.Kind == input.Kind && evidence.HostID == input.HostID &&
		evidence.HostInstanceID == input.HostInstanceID && evidence.ProjectID == input.ProjectID && evidence.ProjectVersion >= input.ProjectVersion &&
		evidence.Cursor >= input.Cursor && evidence.ObservationID == input.ObservationID && validDetailedSyncState(evidence.GitState) &&
		validDetailedSyncState(evidence.TaskStoreState) && evidence.CompletedAtMillis > 0
}

func operationControls(executor bool, available homeport.OperationAvailability, status string) planningport.OperationsControlAvailability {
	current := status != "offline" && status != "stale"
	result := planningport.OperationsControlAvailability{SyncAvailable: executor && available.Sync && current, ReconcileAvailable: executor && available.Reconcile && current}
	if !result.SyncAvailable || !result.ReconcileAvailable {
		result.Reason = disabledReason("operations_effect_unavailable", "Manual controls require fresh exact-host facts and the matching engine-owned executor capability")
	}
	return result
}

func (service *OperationsService) mutateManual(ctx context.Context, input planningport.OperationsMutationInput, report planningport.OperationsReport) (planningport.OperationsMutationResult, error) {
	if service.executor == nil || (input.Kind == "sync.now" && !report.Controls.SyncAvailable) || (input.Kind == "reconcile.now" && !report.Controls.ReconcileAvailable) {
		code := "operations_effect_unavailable"
		return operationsResult(input, "refused", "The exact engine operation is unavailable; no fallback, retry, or other-host effect was attempted.", nil, nil, nil, &code, report.Cursor), nil
	}
	version, _ := strconv.ParseUint(report.ProjectVersion, 10, 64)
	cursor, _ := strconv.ParseUint(report.Cursor, 10, 64)
	request := homeport.ManualOperation{RequestID: input.RequestID, Kind: input.Kind, HostID: report.HostID, HostInstanceID: report.HostInstanceID,
		ProjectID: report.ProjectID, ProjectVersion: version, Cursor: cursor, ObservationID: report.ObservationID}
	prior, evidence, exists, err := service.executor.ObserveManualOperation(ctx, input.RequestID)
	if err != nil {
		code := "operations_outcome_unavailable"
		return operationsResult(input, "refused", "The previous operation outcome cannot be proved; Director did not dispatch another effect.", nil, nil, nil, &code, report.Cursor), nil
	}
	if exists {
		if prior != request || !manualEvidenceMatches(prior, evidence) {
			code := "operations_replay_conflict"
			return operationsResult(input, "refused", "The request identity is already bound to different or invalid evidence.", nil, nil, nil, &code, report.Cursor), nil
		}
	} else {
		evidence, err = service.executor.ApplyManualOperation(ctx, request)
		if err != nil || !manualEvidenceMatches(request, evidence) {
			code := "operations_effect_refused"
			return operationsResult(input, "refused", "The exact operation did not return authoritative evidence; no automatic retry was attempted.", nil, nil, nil, &code, report.Cursor), nil
		}
	}
	effectClass := "conditional_update"
	if input.Kind == "reconcile.now" {
		effectClass = "store_only"
	}
	effect := &planningport.OperationsEffectResult{Kind: input.Kind, EffectClass: effectClass, Outcome: "observed",
		GitState: string(evidence.GitState), DynamicState: string(evidence.TaskStoreState), SuccessfulHalfRetried: evidence.SuccessfulHalfRetried}
	message := "Reconciliation completed from current durable and external facts."
	if input.Kind == "sync.now" {
		message = "Git and TaskStore/Dolt synchronization completed as two independently observed effects."
	}
	return operationsResult(input, "applied", message, nil, nil, effect, nil, strconv.FormatUint(evidence.Cursor, 10)), nil
}

func validBundleEvidence(preview planningport.SupportBundlePreview, evidence homeport.SupportBundleEvidence) bool {
	return evidence.BundleID == preview.ID && evidence.FileName == preview.OutputFileName && validSHA(evidence.SHA256, 64) &&
		evidence.Bytes > 0 && evidence.Permission == "0600" && evidence.GeneratedAtMillis > 0 && !evidence.UploadAttempted
}

func (service *OperationsService) Mutate(ctx context.Context, input planningport.OperationsMutationInput, actor AuthenticatedOperationsActor) (planningport.OperationsMutationResult, error) {
	if planningport.ValidateOperationsMutation(input) != nil {
		return planningport.OperationsMutationResult{}, planningport.ErrQueryInvalid
	}
	if !validOperationsActor(actor) {
		code := "operations_human_auth_required"
		return operationsResult(input, "refused", "This manual operation requires a server-authenticated human session.", nil, nil, nil, &code, "0"), nil
	}
	report, err := service.report(ctx, planningport.OperationsQueryInput{HostID: input.HostID, ProjectID: input.ProjectID, ExpectedProjectVersion: input.ExpectedProjectVersion})
	if err != nil {
		return planningport.OperationsMutationResult{}, err
	}
	if input.Kind == "sync.now" || input.Kind == "reconcile.now" {
		if report.Status == "offline" || report.Status == "stale" {
			code := "operations_facts_not_current"
			return operationsResult(input, "refused", "Manual controls require fresh exact-host facts; no effect was dispatched.", nil, nil, nil, &code, report.Cursor), nil
		}
		return service.mutateManual(ctx, input, report)
	}
	preview := supportPreview(report, input.RequestID)
	if input.Kind == "support.preview" {
		return operationsResult(input, "preview", "Review the exact allowlist and exclusions before generating a local bundle.", &preview, nil, nil, nil, report.Cursor), nil
	}
	if input.PreviewID == nil || *input.PreviewID != preview.ID {
		code := "support_preview_mismatch"
		return operationsResult(input, "refused", "Operational facts changed or the Preview does not match; generate a fresh Preview.", nil, nil, nil, &code, report.Cursor), nil
	}
	if !preview.Valid || service.bundles == nil {
		code := "support_bundle_unavailable"
		return operationsResult(input, "refused", "The local support-bundle sink or safety scan is unavailable.", nil, nil, nil, &code, report.Cursor), nil
	}
	if prior, exists, observeErr := service.bundles.ObserveSupportBundle(ctx, preview.ID, preview.OutputFileName); observeErr != nil {
		code := "support_bundle_outcome_unavailable"
		return operationsResult(input, "refused", "A previous bundle outcome is ambiguous; Director did not write another bundle.", nil, nil, nil, &code, report.Cursor), nil
	} else if exists {
		if !validBundleEvidence(preview, prior) {
			code := "support_bundle_identity_conflict"
			return operationsResult(input, "refused", "An existing local bundle does not match the exact Preview identity.", nil, nil, nil, &code, report.Cursor), nil
		}
		bundle := planningport.SupportBundleResult{BundleID: prior.BundleID, FileName: prior.FileName, SHA256: prior.SHA256, Bytes: strconv.FormatUint(prior.Bytes, 10),
			Permission: prior.Permission, GeneratedAt: time.UnixMilli(prior.GeneratedAtMillis).UTC().Format(time.RFC3339Nano), UploadAttempted: false}
		return operationsResult(input, "applied", "The existing exact local support bundle was verified and adopted without another write.", nil, &bundle, nil, nil, report.Cursor), nil
	}
	generatedAt := service.now()
	entries, safe := buildSupportEntries(report, preview, generatedAt)
	if !safe {
		code := "support_bundle_redaction_failed"
		return operationsResult(input, "refused", "The post-redaction safety scan refused bundle generation.", nil, nil, nil, &code, report.Cursor), nil
	}
	evidence, err := service.bundles.WriteSupportBundle(ctx, homeport.SupportBundleWrite{BundleID: preview.ID, FileName: preview.OutputFileName, GeneratedAtMillis: generatedAt, Entries: entries})
	if err != nil || !validBundleEvidence(preview, evidence) {
		code := "support_bundle_write_unverified"
		return operationsResult(input, "refused", "Local bundle generation did not return verifiable mode-0600 evidence.", nil, nil, nil, &code, report.Cursor), nil
	}
	bundle := planningport.SupportBundleResult{BundleID: evidence.BundleID, FileName: evidence.FileName, SHA256: evidence.SHA256, Bytes: strconv.FormatUint(evidence.Bytes, 10),
		Permission: evidence.Permission, GeneratedAt: time.UnixMilli(evidence.GeneratedAtMillis).UTC().Format(time.RFC3339Nano), UploadAttempted: false}
	return operationsResult(input, "applied", "The redacted support bundle was generated locally. Director did not upload it.", nil, &bundle, nil, nil, report.Cursor), nil
}
