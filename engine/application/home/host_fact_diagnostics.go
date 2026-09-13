// SPDX-License-Identifier: Apache-2.0

package home

import (
	"errors"

	homeport "github.com/mcuadros/director-engine/ports/home"
)

// HostFactField, HostFactExpectation, and HostFactObserved are closed,
// value-free diagnostics. They identify a rejected predicate without carrying
// the host value, path, credential, identifier, or arbitrary adapter text.
type HostFactField string
type HostFactExpectation string
type HostFactObserved string

const (
	expectExactRequestedHost HostFactExpectation = "exact_requested_host"
	expectBoundedText        HostFactExpectation = "bounded_text"
	expectNonnegativeTime    HostFactExpectation = "nonnegative_time"
	expectNotFuture          HostFactExpectation = "not_future"
	expectPositiveBounded    HostFactExpectation = "positive_bounded"
	expectClosedState        HostFactExpectation = "closed_state"
	expectExactProjectSet    HostFactExpectation = "exact_project_set"
	expectZeroMaximum        HostFactExpectation = "zero_when_unobserved"
	expectPositiveMaximum    HostFactExpectation = "positive_when_observed"
	expectSHAOrEmpty         HostFactExpectation = "sha256_or_empty"
	expectNonemptySHA        HostFactExpectation = "nonempty_sha256"
	expectEqualSHA           HostFactExpectation = "equal_sha256"
	expectUnequalSHA         HostFactExpectation = "unequal_sha256"
	expectMatchingReason     HostFactExpectation = "state_matching_reason"
	expectUnique             HostFactExpectation = "unique"
)

const (
	observedEmpty       HostFactObserved = "empty"
	observedMismatch    HostFactObserved = "mismatch"
	observedInvalid     HostFactObserved = "invalid"
	observedNegative    HostFactObserved = "negative"
	observedFuture      HostFactObserved = "future"
	observedZero        HostFactObserved = "zero"
	observedAboveBound  HostFactObserved = "above_bound"
	observedMissing     HostFactObserved = "missing"
	observedCount       HostFactObserved = "count_mismatch"
	observedUnequal     HostFactObserved = "unequal"
	observedEqual       HostFactObserved = "equal"
	observedDuplicate   HostFactObserved = "duplicate"
	observedUnsupported HostFactObserved = "unsupported"
)

type HostFactRejection struct {
	Field    HostFactField       `json:"field"`
	Expected HostFactExpectation `json:"expected"`
	Observed HostFactObserved    `json:"observed"`
}

type HostFactRejectionError struct{ Rejection HostFactRejection }

func (value *HostFactRejectionError) Error() string {
	return "Home host fact rejected: " + string(value.Rejection.Field) + ":" + string(value.Rejection.Expected) + ":" + string(value.Rejection.Observed)
}

func (value *HostFactRejectionError) Unwrap() error { return ErrHostFacts }

func HostFactRejectionFrom(err error) (HostFactRejection, bool) {
	var rejection *HostFactRejectionError
	if !errors.As(err, &rejection) || rejection == nil {
		return HostFactRejection{}, false
	}
	if !validHostFactRejection(rejection.Rejection) {
		return HostFactRejection{}, false
	}
	return rejection.Rejection, true
}

func validHostFactRejection(value HostFactRejection) bool {
	switch value.Field {
	case "host.id", "host.label", "host.instance_id", "host.observed_at", "host.maximum_age", "host.state", "host.projects",
		"project.presence", "project.git_sync.state", "project.taskstore_sync.state", "project.sync_observed_at", "project.sync_maximum_age",
		"project.git_sync_detail.state", "project.git_sync_detail.reason", "project.git_sync_detail.local_revision_fingerprint",
		"project.git_sync_detail.remote_revision_fingerprint", "project.git_sync_detail.observed_at", "project.git_sync_detail.last_success_at",
		"project.git_sync_detail.revision_relation", "project.taskstore_sync_detail.state", "project.taskstore_sync_detail.reason",
		"project.taskstore_sync_detail.local_revision_fingerprint", "project.taskstore_sync_detail.remote_revision_fingerprint",
		"project.taskstore_sync_detail.observed_at", "project.taskstore_sync_detail.last_success_at", "project.taskstore_sync_detail.revision_relation",
		"project.reconciliation.state", "project.reconciliation.reason", "project.reconciliation.observed_at", "project.reconciliation.last_completed_at",
		"project.technical_log.sequence", "project.technical_log.occurred_at", "project.technical_log.occurrences", "project.technical_log.level",
		"project.technical_log.component", "project.technical_log.code", "project.workspace.health", "project.preflight.capability", "project.preflight.state":
	default:
		return false
	}
	switch value.Expected {
	case expectExactRequestedHost, expectBoundedText, expectNonnegativeTime, expectNotFuture, expectPositiveBounded, expectClosedState,
		expectExactProjectSet, expectZeroMaximum, expectPositiveMaximum, expectSHAOrEmpty, expectNonemptySHA, expectEqualSHA,
		expectUnequalSHA, expectMatchingReason, expectUnique:
	default:
		return false
	}
	switch value.Observed {
	case observedEmpty, observedMismatch, observedInvalid, observedNegative, observedFuture, observedZero, observedAboveBound,
		observedMissing, observedCount, observedUnequal, observedEqual, observedDuplicate, observedUnsupported:
		return true
	default:
		return false
	}
}

func rejectFact(field HostFactField, expected HostFactExpectation, observed HostFactObserved) error {
	return &HostFactRejectionError{Rejection: HostFactRejection{Field: field, Expected: expected, Observed: observed}}
}

func invalidTextObservation(value string) HostFactObserved {
	if value == "" {
		return observedEmpty
	}
	return observedInvalid
}

func validateSyncDetail(prefix string, value homeport.SyncStreamObservation, now int64) error {
	field := func(name string) HostFactField { return HostFactField(prefix + "." + name) }
	if !validDetailedSyncState(value.State) {
		return rejectFact(field("state"), expectClosedState, observedUnsupported)
	}
	if !validSyncReason(value.Reason) {
		return rejectFact(field("reason"), expectClosedState, observedUnsupported)
	}
	if !validFingerprint(value.LocalRevisionFingerprint) {
		return rejectFact(field("local_revision_fingerprint"), expectSHAOrEmpty, observedInvalid)
	}
	if !validFingerprint(value.RemoteRevisionFingerprint) {
		return rejectFact(field("remote_revision_fingerprint"), expectSHAOrEmpty, observedInvalid)
	}
	if value.ObservedAtMillis < 0 {
		return rejectFact(field("observed_at"), expectNonnegativeTime, observedNegative)
	}
	if value.ObservedAtMillis > now {
		return rejectFact(field("observed_at"), expectNotFuture, observedFuture)
	}
	if value.LastSuccessAtMillis < 0 {
		return rejectFact(field("last_success_at"), expectNonnegativeTime, observedNegative)
	}
	if value.LastSuccessAtMillis > now {
		return rejectFact(field("last_success_at"), expectNotFuture, observedFuture)
	}
	if value.State == homeport.SyncCurrent {
		if value.Reason != homeport.SyncReasonAligned {
			return rejectFact(field("reason"), expectMatchingReason, observedMismatch)
		}
		if value.LocalRevisionFingerprint == "" {
			return rejectFact(field("local_revision_fingerprint"), expectNonemptySHA, observedEmpty)
		}
		if value.RemoteRevisionFingerprint == "" {
			return rejectFact(field("remote_revision_fingerprint"), expectNonemptySHA, observedEmpty)
		}
		if value.LocalRevisionFingerprint != value.RemoteRevisionFingerprint {
			return rejectFact(field("revision_relation"), expectEqualSHA, observedUnequal)
		}
		return nil
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
		return rejectFact(field("reason"), expectMatchingReason, observedMismatch)
	}
	if value.State == homeport.SyncLocalAhead || value.State == homeport.SyncRemoteAhead || value.State == homeport.SyncDiverged {
		if value.LocalRevisionFingerprint == "" {
			return rejectFact(field("local_revision_fingerprint"), expectNonemptySHA, observedEmpty)
		}
		if value.RemoteRevisionFingerprint == "" {
			return rejectFact(field("remote_revision_fingerprint"), expectNonemptySHA, observedEmpty)
		}
		if value.LocalRevisionFingerprint == value.RemoteRevisionFingerprint {
			return rejectFact(field("revision_relation"), expectUnequalSHA, observedEqual)
		}
	}
	return nil
}

func validateReconciliation(value homeport.ReconciliationObservation, now int64) error {
	switch value.State {
	case homeport.ReconciliationCurrent, homeport.ReconciliationRunning, homeport.ReconciliationWaiting,
		homeport.ReconciliationDegraded, homeport.ReconciliationUnavailable, homeport.ReconciliationStale:
	default:
		return rejectFact("project.reconciliation.state", expectClosedState, observedUnsupported)
	}
	switch value.Reason {
	case homeport.ReconciliationReasonObserved, homeport.ReconciliationReasonEventWake,
		homeport.ReconciliationReasonExternalWait, homeport.ReconciliationReasonFactMismatch,
		homeport.ReconciliationReasonSourceOffline, homeport.ReconciliationReasonObservationOld:
	default:
		return rejectFact("project.reconciliation.reason", expectClosedState, observedUnsupported)
	}
	if value.ObservedAtMillis < 0 {
		return rejectFact("project.reconciliation.observed_at", expectNonnegativeTime, observedNegative)
	}
	if value.ObservedAtMillis > now {
		return rejectFact("project.reconciliation.observed_at", expectNotFuture, observedFuture)
	}
	if value.LastCompletedAtMillis < 0 {
		return rejectFact("project.reconciliation.last_completed_at", expectNonnegativeTime, observedNegative)
	}
	if value.LastCompletedAtMillis > now {
		return rejectFact("project.reconciliation.last_completed_at", expectNotFuture, observedFuture)
	}
	return nil
}

func validateLog(value homeport.TechnicalLogObservation, now int64) error {
	if value.Sequence == 0 {
		return rejectFact("project.technical_log.sequence", expectPositiveBounded, observedZero)
	}
	if value.OccurredAtMillis <= 0 {
		return rejectFact("project.technical_log.occurred_at", expectPositiveBounded, observedZero)
	}
	if value.OccurredAtMillis > now {
		return rejectFact("project.technical_log.occurred_at", expectNotFuture, observedFuture)
	}
	if value.Occurrences == 0 {
		return rejectFact("project.technical_log.occurrences", expectPositiveBounded, observedZero)
	}
	switch value.Level {
	case homeport.TechnicalLogInfo, homeport.TechnicalLogWarning, homeport.TechnicalLogError:
	default:
		return rejectFact("project.technical_log.level", expectClosedState, observedUnsupported)
	}
	switch value.Component {
	case homeport.TechnicalLogEngine, homeport.TechnicalLogTaskStore, homeport.TechnicalLogGit, homeport.TechnicalLogDolt,
		homeport.TechnicalLogReconciliation, homeport.TechnicalLogSecurity, homeport.TechnicalLogSupport:
	default:
		return rejectFact("project.technical_log.component", expectClosedState, observedUnsupported)
	}
	switch value.Code {
	case homeport.TechnicalLogEngineStarted, homeport.TechnicalLogReconciliationCompleted, homeport.TechnicalLogReconciliationWaiting,
		homeport.TechnicalLogGitSyncCurrent, homeport.TechnicalLogGitSyncFailed, homeport.TechnicalLogDoltSyncCurrent,
		homeport.TechnicalLogDoltSyncFailed, homeport.TechnicalLogTaskStoreUnhealthy, homeport.TechnicalLogSupportBundleGenerated,
		homeport.TechnicalLogUnsafeOutputSuppressed:
		return nil
	default:
		return rejectFact("project.technical_log.code", expectClosedState, observedUnsupported)
	}
}

func validateObservation(value HostObservation, requested string, projectIDs []string, now int64) error {
	if value.HostID != requested {
		return rejectFact("host.id", expectExactRequestedHost, observedMismatch)
	}
	if !boundedHomeText(value.Label, 512) {
		return rejectFact("host.label", expectBoundedText, invalidTextObservation(value.Label))
	}
	if !boundedHomeText(value.InstanceID, 128) {
		return rejectFact("host.instance_id", expectBoundedText, invalidTextObservation(value.InstanceID))
	}
	if value.ObservedAtMillis < 0 {
		return rejectFact("host.observed_at", expectNonnegativeTime, observedNegative)
	}
	if value.ObservedAtMillis > now {
		return rejectFact("host.observed_at", expectNotFuture, observedFuture)
	}
	if value.MaximumAgeMillis <= 0 {
		return rejectFact("host.maximum_age", expectPositiveBounded, observedZero)
	}
	if value.MaximumAgeMillis > HostObservationMaximumAgeMillis {
		return rejectFact("host.maximum_age", expectPositiveBounded, observedAboveBound)
	}
	if value.State != "current" && value.State != "degraded" && value.State != "disconnected" && value.State != "stale" {
		return rejectFact("host.state", expectClosedState, observedUnsupported)
	}
	if len(value.Projects) != len(projectIDs) {
		return rejectFact("host.projects", expectExactProjectSet, observedCount)
	}
	for _, projectID := range projectIDs {
		project, ok := value.Projects[projectID]
		if !ok {
			return rejectFact("project.presence", expectExactProjectSet, observedMissing)
		}
		if !validSyncStream(project.GitSync) {
			return rejectFact("project.git_sync.state", expectClosedState, observedUnsupported)
		}
		if !validSyncStream(project.TaskStoreSync) {
			return rejectFact("project.taskstore_sync.state", expectClosedState, observedUnsupported)
		}
		if project.SyncObservedAtMillis < 0 {
			return rejectFact("project.sync_observed_at", expectNonnegativeTime, observedNegative)
		}
		if project.SyncObservedAtMillis > now {
			return rejectFact("project.sync_observed_at", expectNotFuture, observedFuture)
		}
		if project.SyncObservedAtMillis == 0 && project.SyncMaximumAgeMillis != 0 {
			return rejectFact("project.sync_maximum_age", expectZeroMaximum, observedMismatch)
		}
		if project.SyncObservedAtMillis > 0 && project.SyncMaximumAgeMillis <= 0 {
			return rejectFact("project.sync_maximum_age", expectPositiveMaximum, observedZero)
		}
		if project.SyncObservedAtMillis > 0 && project.SyncMaximumAgeMillis > 5*60*1000 {
			return rejectFact("project.sync_maximum_age", expectPositiveBounded, observedAboveBound)
		}
		if project.GitSyncDetail.State != "" {
			if err := validateSyncDetail("project.git_sync_detail", project.GitSyncDetail, now); err != nil {
				return err
			}
		}
		if project.TaskStoreSyncDetail.State != "" {
			if err := validateSyncDetail("project.taskstore_sync_detail", project.TaskStoreSyncDetail, now); err != nil {
				return err
			}
		}
		if project.Reconciliation.State != "" {
			if err := validateReconciliation(project.Reconciliation, now); err != nil {
				return err
			}
		}
		for _, entry := range project.TechnicalLogs {
			if err := validateLog(entry, now); err != nil {
				return err
			}
		}
		for _, workspace := range project.Workspaces {
			if workspace.Health != "healthy" && workspace.Health != "degraded" && workspace.Health != "disconnected" && workspace.Health != "stale" && workspace.Health != "unknown" {
				return rejectFact("project.workspace.health", expectClosedState, observedUnsupported)
			}
		}
		seenCapabilities := make(map[homeport.PreflightCapability]struct{}, len(project.Preflight))
		for _, preflight := range project.Preflight {
			if !validPreflightCapability(preflight.Capability) {
				return rejectFact("project.preflight.capability", expectClosedState, observedUnsupported)
			}
			if !validPreflightState(preflight.State) {
				return rejectFact("project.preflight.state", expectClosedState, observedUnsupported)
			}
			if _, duplicate := seenCapabilities[preflight.Capability]; duplicate {
				return rejectFact("project.preflight.capability", expectUnique, observedDuplicate)
			}
			seenCapabilities[preflight.Capability] = struct{}{}
		}
	}
	return nil
}
