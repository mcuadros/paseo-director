// SPDX-License-Identifier: Apache-2.0

package home

import (
	"context"
	"errors"
	"strings"
	"testing"

	homeport "github.com/mcuadros/director-engine/ports/home"
	planningport "github.com/mcuadros/director-engine/ports/planning"
)

func validDiagnosticObservation(now int64) HostObservation {
	fingerprint := strings.Repeat("a", 64)
	return HostObservation{
		HostID: "host-a", Label: "Primary host", InstanceID: "engine-a", State: "current",
		ObservedAtMillis: now, MaximumAgeMillis: HostObservationMaximumAgeMillis,
		Projects: map[string]ProjectObservation{"project-a": {
			GitSync: SyncCurrent, TaskStoreSync: SyncCurrent,
			GitSyncDetail: homeport.SyncStreamObservation{State: homeport.SyncCurrent, Reason: homeport.SyncReasonAligned,
				LocalRevisionFingerprint: fingerprint, RemoteRevisionFingerprint: fingerprint, ObservedAtMillis: now, LastSuccessAtMillis: now},
			TaskStoreSyncDetail: homeport.SyncStreamObservation{State: homeport.SyncCurrent, Reason: homeport.SyncReasonAligned,
				LocalRevisionFingerprint: fingerprint, RemoteRevisionFingerprint: fingerprint, ObservedAtMillis: now, LastSuccessAtMillis: now},
			SyncObservedAtMillis: now, SyncMaximumAgeMillis: 30_000,
			Reconciliation: homeport.ReconciliationObservation{State: homeport.ReconciliationCurrent,
				Reason: homeport.ReconciliationReasonObserved, ObservedAtMillis: now, LastCompletedAtMillis: now},
			TechnicalLogs: []homeport.TechnicalLogObservation{{Sequence: 1, OccurredAtMillis: now, Occurrences: 1,
				Level: homeport.TechnicalLogInfo, Component: homeport.TechnicalLogEngine, Code: homeport.TechnicalLogEngineStarted}},
			Workspaces: map[string]WorkspaceObservation{"workspace-a": {Health: "healthy"}},
			Preflight:  []homeport.PreflightObservation{{Capability: homeport.CapabilityPaseoRuntime, State: homeport.PreflightCurrent}},
		}},
	}
}

func TestDoctorAndOperationsPreserveTheSameTypedHostFactRejection(t *testing.T) {
	now := int64(10_000)
	_, doctorSource, _, doctor := doctorRepairFixture(now)
	doctorProject := doctorSource.value.Projects["project-00"]
	doctorProject.TaskStoreSyncDetail = homeport.SyncStreamObservation{
		State: homeport.SyncCurrent, Reason: homeport.SyncReasonAligned, ObservedAtMillis: now,
	}
	doctorSource.value.Projects["project-00"] = doctorProject
	_, err := doctor.Doctor(context.Background(), planningport.DoctorQueryInput{HostID: "host-a", ProjectID: "project-00", ExpectedProjectVersion: "1"})
	doctorRejection, ok := HostFactRejectionFrom(err)
	if !ok || doctorRejection.Field != "project.taskstore_sync_detail.local_revision_fingerprint" {
		t.Fatalf("Doctor rejection = %#v, %v", doctorRejection, err)
	}

	_, operationsSource, _, _, operations := operationsFixture(now)
	operationsProject := operationsSource.value.Projects["project-00"]
	operationsProject.TaskStoreSyncDetail = homeport.SyncStreamObservation{
		State: homeport.SyncCurrent, Reason: homeport.SyncReasonAligned, ObservedAtMillis: now,
	}
	operationsSource.value.Projects["project-00"] = operationsProject
	_, err = operations.Operations(context.Background(), planningport.OperationsQueryInput{HostID: "host-a", ProjectID: "project-00", ExpectedProjectVersion: "1"})
	operationsRejection, ok := HostFactRejectionFrom(err)
	if !ok || operationsRejection != doctorRejection {
		t.Fatalf("Operations rejection = %#v, %v; Doctor = %#v", operationsRejection, err, doctorRejection)
	}
}

func mutateDiagnosticProject(value *HostObservation, mutate func(*ProjectObservation)) {
	project := value.Projects["project-a"]
	mutate(&project)
	value.Projects["project-a"] = project
}

func TestHostFactRejectionsIdentifyEveryBoundedPredicateWithoutRawValues(t *testing.T) {
	now := int64(10_000)
	tests := []struct {
		name     string
		field    HostFactField
		expected HostFactExpectation
		observed HostFactObserved
		mutate   func(*HostObservation)
	}{
		{"host identity", "host.id", expectExactRequestedHost, observedMismatch, func(value *HostObservation) { value.HostID = "private-host-value" }},
		{"host label", "host.label", expectBoundedText, observedEmpty, func(value *HostObservation) { value.Label = "" }},
		{"host instance", "host.instance_id", expectBoundedText, observedInvalid, func(value *HostObservation) { value.InstanceID = " invalid " }},
		{"host negative time", "host.observed_at", expectNonnegativeTime, observedNegative, func(value *HostObservation) { value.ObservedAtMillis = -1 }},
		{"host future time", "host.observed_at", expectNotFuture, observedFuture, func(value *HostObservation) { value.ObservedAtMillis = now + 1 }},
		{"host zero maximum", "host.maximum_age", expectPositiveBounded, observedZero, func(value *HostObservation) { value.MaximumAgeMillis = 0 }},
		{"host excessive maximum", "host.maximum_age", expectPositiveBounded, observedAboveBound, func(value *HostObservation) { value.MaximumAgeMillis++ }},
		{"host state", "host.state", expectClosedState, observedUnsupported, func(value *HostObservation) { value.State = "private-state" }},
		{"project count", "host.projects", expectExactProjectSet, observedCount, func(value *HostObservation) { value.Projects = map[string]ProjectObservation{} }},
		{"project presence", "project.presence", expectExactProjectSet, observedMissing, func(value *HostObservation) {
			value.Projects["foreign"] = value.Projects["project-a"]
			delete(value.Projects, "project-a")
		}},
		{"git legacy state", "project.git_sync.state", expectClosedState, observedUnsupported, func(value *HostObservation) {
			mutateDiagnosticProject(value, func(project *ProjectObservation) { project.GitSync = "private" })
		}},
		{"taskstore legacy state", "project.taskstore_sync.state", expectClosedState, observedUnsupported, func(value *HostObservation) {
			mutateDiagnosticProject(value, func(project *ProjectObservation) { project.TaskStoreSync = "private" })
		}},
		{"sync negative time", "project.sync_observed_at", expectNonnegativeTime, observedNegative, func(value *HostObservation) {
			mutateDiagnosticProject(value, func(project *ProjectObservation) { project.SyncObservedAtMillis = -1 })
		}},
		{"sync future time", "project.sync_observed_at", expectNotFuture, observedFuture, func(value *HostObservation) {
			mutateDiagnosticProject(value, func(project *ProjectObservation) { project.SyncObservedAtMillis = now + 1 })
		}},
		{"unobserved sync maximum", "project.sync_maximum_age", expectZeroMaximum, observedMismatch, func(value *HostObservation) {
			mutateDiagnosticProject(value, func(project *ProjectObservation) { project.SyncObservedAtMillis = 0 })
		}},
		{"observed sync zero maximum", "project.sync_maximum_age", expectPositiveMaximum, observedZero, func(value *HostObservation) {
			mutateDiagnosticProject(value, func(project *ProjectObservation) { project.SyncMaximumAgeMillis = 0 })
		}},
		{"observed sync excessive maximum", "project.sync_maximum_age", expectPositiveBounded, observedAboveBound, func(value *HostObservation) {
			mutateDiagnosticProject(value, func(project *ProjectObservation) { project.SyncMaximumAgeMillis = 5*60*1000 + 1 })
		}},
		{"detail state", "project.git_sync_detail.state", expectClosedState, observedUnsupported, func(value *HostObservation) {
			mutateDiagnosticProject(value, func(project *ProjectObservation) { project.GitSyncDetail.State = "private" })
		}},
		{"detail reason", "project.git_sync_detail.reason", expectClosedState, observedUnsupported, func(value *HostObservation) {
			mutateDiagnosticProject(value, func(project *ProjectObservation) { project.GitSyncDetail.Reason = "private" })
		}},
		{"local fingerprint format", "project.git_sync_detail.local_revision_fingerprint", expectSHAOrEmpty, observedInvalid, func(value *HostObservation) {
			mutateDiagnosticProject(value, func(project *ProjectObservation) { project.GitSyncDetail.LocalRevisionFingerprint = "private-value" })
		}},
		{"remote fingerprint format", "project.git_sync_detail.remote_revision_fingerprint", expectSHAOrEmpty, observedInvalid, func(value *HostObservation) {
			mutateDiagnosticProject(value, func(project *ProjectObservation) { project.GitSyncDetail.RemoteRevisionFingerprint = "private-value" })
		}},
		{"detail negative observed", "project.git_sync_detail.observed_at", expectNonnegativeTime, observedNegative, func(value *HostObservation) {
			mutateDiagnosticProject(value, func(project *ProjectObservation) { project.GitSyncDetail.ObservedAtMillis = -1 })
		}},
		{"detail future observed", "project.git_sync_detail.observed_at", expectNotFuture, observedFuture, func(value *HostObservation) {
			mutateDiagnosticProject(value, func(project *ProjectObservation) { project.GitSyncDetail.ObservedAtMillis = now + 1 })
		}},
		{"detail negative success", "project.git_sync_detail.last_success_at", expectNonnegativeTime, observedNegative, func(value *HostObservation) {
			mutateDiagnosticProject(value, func(project *ProjectObservation) { project.GitSyncDetail.LastSuccessAtMillis = -1 })
		}},
		{"detail future success", "project.git_sync_detail.last_success_at", expectNotFuture, observedFuture, func(value *HostObservation) {
			mutateDiagnosticProject(value, func(project *ProjectObservation) { project.GitSyncDetail.LastSuccessAtMillis = now + 1 })
		}},
		{"current reason", "project.git_sync_detail.reason", expectMatchingReason, observedMismatch, func(value *HostObservation) {
			mutateDiagnosticProject(value, func(project *ProjectObservation) { project.GitSyncDetail.Reason = homeport.SyncReasonOperationFailed })
		}},
		{"reported production missing local fingerprint", "project.taskstore_sync_detail.local_revision_fingerprint", expectNonemptySHA, observedEmpty, func(value *HostObservation) {
			mutateDiagnosticProject(value, func(project *ProjectObservation) { project.TaskStoreSyncDetail.LocalRevisionFingerprint = "" })
		}},
		{"current missing remote fingerprint", "project.git_sync_detail.remote_revision_fingerprint", expectNonemptySHA, observedEmpty, func(value *HostObservation) {
			mutateDiagnosticProject(value, func(project *ProjectObservation) { project.GitSyncDetail.RemoteRevisionFingerprint = "" })
		}},
		{"current unequal fingerprints", "project.git_sync_detail.revision_relation", expectEqualSHA, observedUnequal, func(value *HostObservation) {
			mutateDiagnosticProject(value, func(project *ProjectObservation) {
				project.GitSyncDetail.RemoteRevisionFingerprint = strings.Repeat("b", 64)
			})
		}},
		{"directional reason", "project.git_sync_detail.reason", expectMatchingReason, observedMismatch, func(value *HostObservation) {
			mutateDiagnosticProject(value, func(project *ProjectObservation) { project.GitSyncDetail.State = homeport.SyncDiverged })
		}},
		{"directional missing local", "project.git_sync_detail.local_revision_fingerprint", expectNonemptySHA, observedEmpty, func(value *HostObservation) {
			mutateDiagnosticProject(value, func(project *ProjectObservation) {
				project.GitSyncDetail.State = homeport.SyncDiverged
				project.GitSyncDetail.Reason = homeport.SyncReasonDiverged
				project.GitSyncDetail.LocalRevisionFingerprint = ""
			})
		}},
		{"directional missing remote", "project.git_sync_detail.remote_revision_fingerprint", expectNonemptySHA, observedEmpty, func(value *HostObservation) {
			mutateDiagnosticProject(value, func(project *ProjectObservation) {
				project.GitSyncDetail.State = homeport.SyncDiverged
				project.GitSyncDetail.Reason = homeport.SyncReasonDiverged
				project.GitSyncDetail.RemoteRevisionFingerprint = ""
			})
		}},
		{"directional equal fingerprints", "project.git_sync_detail.revision_relation", expectUnequalSHA, observedEqual, func(value *HostObservation) {
			mutateDiagnosticProject(value, func(project *ProjectObservation) {
				project.GitSyncDetail.State = homeport.SyncDiverged
				project.GitSyncDetail.Reason = homeport.SyncReasonDiverged
			})
		}},
		{"reconciliation state", "project.reconciliation.state", expectClosedState, observedUnsupported, func(value *HostObservation) {
			mutateDiagnosticProject(value, func(project *ProjectObservation) { project.Reconciliation.State = "private" })
		}},
		{"reconciliation reason", "project.reconciliation.reason", expectClosedState, observedUnsupported, func(value *HostObservation) {
			mutateDiagnosticProject(value, func(project *ProjectObservation) { project.Reconciliation.Reason = "private" })
		}},
		{"reconciliation observed", "project.reconciliation.observed_at", expectNotFuture, observedFuture, func(value *HostObservation) {
			mutateDiagnosticProject(value, func(project *ProjectObservation) { project.Reconciliation.ObservedAtMillis = now + 1 })
		}},
		{"reconciliation negative observed", "project.reconciliation.observed_at", expectNonnegativeTime, observedNegative, func(value *HostObservation) {
			mutateDiagnosticProject(value, func(project *ProjectObservation) { project.Reconciliation.ObservedAtMillis = -1 })
		}},
		{"reconciliation completed", "project.reconciliation.last_completed_at", expectNonnegativeTime, observedNegative, func(value *HostObservation) {
			mutateDiagnosticProject(value, func(project *ProjectObservation) { project.Reconciliation.LastCompletedAtMillis = -1 })
		}},
		{"reconciliation future completed", "project.reconciliation.last_completed_at", expectNotFuture, observedFuture, func(value *HostObservation) {
			mutateDiagnosticProject(value, func(project *ProjectObservation) { project.Reconciliation.LastCompletedAtMillis = now + 1 })
		}},
		{"log sequence", "project.technical_log.sequence", expectPositiveBounded, observedZero, func(value *HostObservation) {
			mutateDiagnosticProject(value, func(project *ProjectObservation) { project.TechnicalLogs[0].Sequence = 0 })
		}},
		{"log occurred", "project.technical_log.occurred_at", expectNotFuture, observedFuture, func(value *HostObservation) {
			mutateDiagnosticProject(value, func(project *ProjectObservation) { project.TechnicalLogs[0].OccurredAtMillis = now + 1 })
		}},
		{"log unobserved", "project.technical_log.occurred_at", expectPositiveBounded, observedZero, func(value *HostObservation) {
			mutateDiagnosticProject(value, func(project *ProjectObservation) { project.TechnicalLogs[0].OccurredAtMillis = 0 })
		}},
		{"log occurrences", "project.technical_log.occurrences", expectPositiveBounded, observedZero, func(value *HostObservation) {
			mutateDiagnosticProject(value, func(project *ProjectObservation) { project.TechnicalLogs[0].Occurrences = 0 })
		}},
		{"log level", "project.technical_log.level", expectClosedState, observedUnsupported, func(value *HostObservation) {
			mutateDiagnosticProject(value, func(project *ProjectObservation) { project.TechnicalLogs[0].Level = "private" })
		}},
		{"log component", "project.technical_log.component", expectClosedState, observedUnsupported, func(value *HostObservation) {
			mutateDiagnosticProject(value, func(project *ProjectObservation) { project.TechnicalLogs[0].Component = "private" })
		}},
		{"log code", "project.technical_log.code", expectClosedState, observedUnsupported, func(value *HostObservation) {
			mutateDiagnosticProject(value, func(project *ProjectObservation) { project.TechnicalLogs[0].Code = "private" })
		}},
		{"workspace health", "project.workspace.health", expectClosedState, observedUnsupported, func(value *HostObservation) {
			mutateDiagnosticProject(value, func(project *ProjectObservation) {
				project.Workspaces["workspace-a"] = WorkspaceObservation{Health: "private"}
			})
		}},
		{"preflight capability", "project.preflight.capability", expectClosedState, observedUnsupported, func(value *HostObservation) {
			mutateDiagnosticProject(value, func(project *ProjectObservation) { project.Preflight[0].Capability = "private" })
		}},
		{"preflight state", "project.preflight.state", expectClosedState, observedUnsupported, func(value *HostObservation) {
			mutateDiagnosticProject(value, func(project *ProjectObservation) { project.Preflight[0].State = "private" })
		}},
		{"preflight duplicate", "project.preflight.capability", expectUnique, observedDuplicate, func(value *HostObservation) {
			mutateDiagnosticProject(value, func(project *ProjectObservation) { project.Preflight = append(project.Preflight, project.Preflight[0]) })
		}},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			value := validDiagnosticObservation(now)
			test.mutate(&value)
			err := validateObservation(value, "host-a", []string{"project-a"}, now)
			rejection, ok := HostFactRejectionFrom(err)
			if !ok || !errors.Is(err, ErrHostFacts) || rejection.Field != test.field || rejection.Expected != test.expected || rejection.Observed != test.observed {
				t.Fatalf("rejection = %#v, %v", rejection, err)
			}
			if strings.Contains(err.Error(), "private") {
				t.Fatalf("diagnostic exposed rejected value: %v", err)
			}
		})
	}
}

func TestHostFactRejectionFromRefusesOpenDiagnosticValues(t *testing.T) {
	for _, rejection := range []HostFactRejection{
		{Field: "private.path", Expected: expectBoundedText, Observed: observedInvalid},
		{Field: "host.label", Expected: "private-expectation", Observed: observedInvalid},
		{Field: "host.label", Expected: expectBoundedText, Observed: "private-observation"},
	} {
		if value, ok := HostFactRejectionFrom(&HostFactRejectionError{Rejection: rejection}); ok {
			t.Fatalf("open diagnostic accepted: %#v", value)
		}
	}
}
