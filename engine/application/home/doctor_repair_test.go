// SPDX-License-Identifier: Apache-2.0

package home

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"

	homeport "github.com/mcuadros/director-engine/ports/home"
	planningport "github.com/mcuadros/director-engine/ports/planning"
)

type mutatingRepairExecutor struct {
	store   *homeStore
	calls   []homeport.RepairExecution
	inputs  map[string]homeport.RepairExecution
	results map[string]homeport.RepairEvidence
	failure error
}

func (executor *mutatingRepairExecutor) ObserveRepair(_ context.Context, requestID string) (homeport.RepairExecution, homeport.RepairEvidence, bool, error) {
	if executor.failure != nil {
		return homeport.RepairExecution{}, homeport.RepairEvidence{}, false, executor.failure
	}
	result, ok := executor.results[requestID]
	return executor.inputs[requestID], result, ok, nil
}

func (executor *mutatingRepairExecutor) ApplyRepair(_ context.Context, input homeport.RepairExecution) (homeport.RepairEvidence, error) {
	if executor.failure != nil {
		return homeport.RepairEvidence{}, executor.failure
	}
	if result, ok := executor.results[input.RequestID]; ok {
		return result, nil
	}
	executor.calls = append(executor.calls, input)
	for index := range executor.store.projects {
		if executor.store.projects[index].ID == input.ProjectID {
			executor.store.projects[index].Version++
		}
	}
	executor.store.cursor++
	result := homeport.RepairEvidence{ProjectVersion: input.ProjectVersion + 1, Cursor: input.Cursor + 1, OperationIDs: append([]string{}, input.OperationIDs...)}
	executor.inputs[input.RequestID] = input
	executor.results[input.RequestID] = result
	return result, nil
}

func doctorRepairFixture(now int64) (*homeStore, *staticObservationSource, *mutatingRepairExecutor, *DoctorRepairService) {
	store, observation := homeFixture(now, 1)
	project := observation.Projects["project-00"]
	project.TaskStoreSync = SyncFailed
	project.Operations.Repair = true
	project.RepairCapabilities = homeport.RepairCapabilities{DynamicState: true, WorkspaceRecovery: true}
	project.Preflight = []homeport.PreflightObservation{
		{Capability: homeport.CapabilityPaseoRuntime, State: homeport.PreflightCurrent},
		{Capability: homeport.CapabilityConnectorContract, State: homeport.PreflightCurrent},
		{Capability: homeport.CapabilityProviderCodex, State: homeport.PreflightMissing},
		{Capability: homeport.CapabilityProviderAuth, State: homeport.PreflightUnavailable},
		{Capability: homeport.CapabilitySessionMCP, State: homeport.PreflightCurrent},
		{Capability: homeport.CapabilityExactMCPPolicy, State: homeport.PreflightCurrent},
		{Capability: homeport.CapabilityRootlessOCI, State: homeport.PreflightCurrent},
		{Capability: homeport.CapabilityRepositoryIdentity, State: homeport.PreflightCurrent},
		{Capability: homeport.CapabilityResourceLimits, State: homeport.PreflightCurrent},
	}
	observation.Projects["project-00"] = project
	source := &staticObservationSource{value: observation}
	executor := &mutatingRepairExecutor{store: store, inputs: map[string]homeport.RepairExecution{}, results: map[string]homeport.RepairEvidence{}}
	return store, source, executor, NewDoctorRepairService(store, source, executor, func() int64 { return now })
}

func repairInput(kind, version string, preview *string, confirmed bool) planningport.RepairInput {
	hash, _ := planningport.SchemaSHA256()
	return planningport.RepairInput{SchemaVersion: 1, ContractVersion: "director-planning/v1", ContractHash: hash,
		HostID: "host-a", RequestID: "repair-request-0001", Kind: kind, ProjectID: "project-00",
		ExpectedProjectVersion: version, PreviewID: preview, Confirmed: confirmed}
}

func TestDoctorIsReadOnlyAndEveryFailureNamesConcreteGuidance(t *testing.T) {
	store, _, executor, service := doctorRepairFixture(10_000)
	version, cursor := store.projects[0].Version, store.cursor
	report, err := service.Doctor(context.Background(), planningport.DoctorQueryInput{HostID: "host-a", ProjectID: "project-00", ExpectedProjectVersion: "1"})
	if err != nil {
		t.Fatal(err)
	}
	if !report.ReadOnly || report.Status != "blocking" || report.BlockingCount == "0" || !report.Repair.Available || len(report.Checks) < 10 {
		t.Fatalf("Doctor report = %#v", report)
	}
	for _, check := range report.Checks {
		if check.Status == "passed" {
			if check.MissingCapability != nil || len(check.InstallationGuidance) != 0 || check.Blocking {
				t.Fatalf("passing check contains failure policy: %#v", check)
			}
			continue
		}
		if check.MissingCapability == nil || *check.MissingCapability == "" || len(check.InstallationGuidance) == 0 {
			t.Fatalf("failure lacks concrete capability guidance: %#v", check)
		}
	}
	if store.projects[0].Version != version || store.cursor != cursor || len(executor.calls) != 0 {
		t.Fatalf("Doctor mutated state: version=%d cursor=%d effects=%d", store.projects[0].Version, store.cursor, len(executor.calls))
	}
	encoded := report.Assurance
	for _, check := range report.Checks {
		encoded += check.Detail + strings.Join(check.InstallationGuidance, " ")
	}
	for _, forbidden := range []string{"/home/", "/tmp/", "credential-file", "password", "token="} {
		if strings.Contains(strings.ToLower(encoded), strings.ToLower(forbidden)) {
			t.Fatalf("Doctor leaked private or credential material %q", forbidden)
		}
	}
}

func TestDoctorKeepsHealthyDegradedBlockingOfflineAndStaleStatesDistinct(t *testing.T) {
	for name, configure := range map[string]struct {
		want  string
		apply func(*homeStore, *HostObservation)
	}{
		"healthy": {"healthy", func(_ *homeStore, observation *HostObservation) {
			project := observation.Projects["project-00"]
			project.TaskStoreSync = SyncCurrent
			for index := range project.Preflight {
				project.Preflight[index].State = homeport.PreflightCurrent
			}
			observation.Projects["project-00"] = project
		}},
		"degraded": {"degraded", func(_ *homeStore, observation *HostObservation) {
			project := observation.Projects["project-00"]
			project.TaskStoreSync = SyncCurrent
			for index := range project.Preflight {
				project.Preflight[index].State = homeport.PreflightCurrent
			}
			observation.Projects["project-00"] = project
			observation.State = "degraded"
		}},
		"blocking": {"blocking", func(_ *homeStore, _ *HostObservation) {}},
		"offline":  {"offline", func(_ *homeStore, observation *HostObservation) { observation.State = "disconnected" }},
		"stale":    {"stale", func(_ *homeStore, observation *HostObservation) { observation.ObservedAtMillis = 1 }},
	} {
		t.Run(name, func(t *testing.T) {
			store, source, executor, _ := doctorRepairFixture(10_000)
			configure.apply(store, &source.value)
			service := NewDoctorRepairService(store, source, executor, func() int64 { return 40_002 })
			if name != "stale" {
				service.now = func() int64 { return 10_000 }
			}
			report, err := service.Doctor(context.Background(), planningport.DoctorQueryInput{HostID: "host-a", ProjectID: "project-00", ExpectedProjectVersion: "1"})
			if err != nil || report.Status != configure.want {
				t.Fatalf("Doctor status = %q, want %q, err=%v", report.Status, configure.want, err)
			}
		})
	}
}

func TestDoctorAndRepairUseDetailedGitDoltStateInsteadOfCoarseFallback(t *testing.T) {
	store, source, _, service := doctorRepairFixture(10_000)
	project := source.value.Projects["project-00"]
	project.GitSync = SyncCurrent
	project.GitSyncDetail = detailedSync(SyncCurrent, homeport.SyncReasonAligned, strings.Repeat("a", 64), strings.Repeat("a", 64), 10_000, false)
	project.TaskStoreSync = SyncCurrent
	project.TaskStoreSyncDetail = detailedSync(SyncDiverged, homeport.SyncReasonDiverged, strings.Repeat("b", 64), strings.Repeat("c", 64), 10_000, false)
	project.RepairCapabilities.DynamicState = true
	source.value.Projects["project-00"] = project
	report, err := service.Doctor(context.Background(), planningport.DoctorQueryInput{HostID: "host-a", ProjectID: "project-00", ExpectedProjectVersion: "1"})
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, check := range report.Checks {
		if check.ID == "dynamic-state-sync" {
			found = check.Status == "blocking" && check.Code == "dynamic-state-sync_diverged"
		}
	}
	if !found || !repairTargetAvailable(doctorFacts{project: store.projects[0], operational: project, now: 10_000}) {
		t.Fatalf("Doctor did not use detailed Dolt state: %#v", report.Checks)
	}
}

func TestRepairPreviewHasExactNonInstallingEffectsAndApplyIsHumanServerEnforced(t *testing.T) {
	store, _, executor, service := doctorRepairFixture(10_000)
	previewResult, err := service.Repair(context.Background(), repairInput("repair.preview", "1", nil, false), AuthenticatedRepairActor{})
	if err != nil || previewResult.Status != "preview" || previewResult.Preview == nil || !previewResult.Preview.Valid {
		t.Fatalf("Repair Preview = %#v, %v", previewResult, err)
	}
	preview := previewResult.Preview
	if len(preview.Operations) != 1 || preview.Operations[0].Kind != "reconcile_dynamic_state" {
		t.Fatalf("exact Repair operations = %#v", preview.Operations)
	}
	for _, operation := range preview.Operations {
		if operation.Destructive || operation.AutomaticInstall || strings.Contains(strings.ToLower(operation.Description), "install") {
			t.Fatalf("Repair Preview contains prohibited effect: %#v", operation)
		}
	}

	unauthenticated, err := service.Repair(context.Background(), repairInput("repair.apply", "1", &preview.ID, true), AuthenticatedRepairActor{})
	if err != nil || unauthenticated.Status != "refused" || unauthenticated.RefusalCode == nil || *unauthenticated.RefusalCode != "repair_human_auth_required" || len(executor.calls) != 0 {
		t.Fatalf("unauthenticated Apply = %#v, effects=%d, err=%v", unauthenticated, len(executor.calls), err)
	}

	mismatch := strings.Repeat("f", 64)
	refused, err := service.Repair(context.Background(), repairInput("repair.apply", "1", &mismatch, true), AuthenticatedRepairActor{Kind: "human", ID: "owner", SessionID: "session", Authenticated: true})
	if err != nil || refused.Status != "refused" || refused.RefusalCode == nil || *refused.RefusalCode != "repair_preview_mismatch" || len(executor.calls) != 0 {
		t.Fatalf("mismatched Apply = %#v, effects=%d, err=%v", refused, len(executor.calls), err)
	}

	applyInput := repairInput("repair.apply", "1", &preview.ID, true)
	applied, err := service.Repair(context.Background(), applyInput, AuthenticatedRepairActor{Kind: "human", ID: "owner", SessionID: "session", Authenticated: true})
	if err != nil || applied.Status != "applied" || applied.ProjectVersion == nil || *applied.ProjectVersion != "2" || len(executor.calls) != 1 || store.projects[0].Version != 2 || store.cursor != 42 {
		t.Fatalf("applied Repair = %#v, effects=%d, project=%d cursor=%d, err=%v", applied, len(executor.calls), store.projects[0].Version, store.cursor, err)
	}
	replayed, err := service.Repair(context.Background(), applyInput, AuthenticatedRepairActor{Kind: "human", ID: "owner", SessionID: "session", Authenticated: true})
	if err != nil || replayed.Status != "applied" || len(executor.calls) != 1 {
		t.Fatalf("Repair replay = %#v, effects=%d, err=%v", replayed, len(executor.calls), err)
	}
}

func TestRepairFailsClosedOnHostVersionObservationAndEvidenceDrift(t *testing.T) {
	_, source, executor, service := doctorRepairFixture(10_000)
	if _, err := service.Doctor(context.Background(), planningport.DoctorQueryInput{HostID: "host-b", ProjectID: "project-00", ExpectedProjectVersion: "1"}); !errors.Is(err, ErrHostMismatch) {
		t.Fatalf("cross-host Doctor error = %v", err)
	}
	previewResult, err := service.Repair(context.Background(), repairInput("repair.preview", "1", nil, false), AuthenticatedRepairActor{})
	if err != nil || previewResult.Preview == nil {
		t.Fatal(previewResult, err)
	}
	preview := previewResult.Preview
	changed := source.value.Projects["project-00"]
	changed.TaskStoreSync = SyncUnavailable
	source.value.Projects["project-00"] = changed
	refused, err := service.Repair(context.Background(), repairInput("repair.apply", "1", &preview.ID, true), AuthenticatedRepairActor{Kind: "human", ID: "owner", SessionID: "session", Authenticated: true})
	if err != nil || refused.Status != "refused" || refused.RefusalCode == nil || *refused.RefusalCode != "repair_preview_mismatch" || len(executor.calls) != 0 {
		t.Fatalf("observation-drift Apply = %#v, effects=%d, err=%v", refused, len(executor.calls), err)
	}

	_, _, executor, service = doctorRepairFixture(10_000)
	executor.failure = errors.New("effect unavailable")
	previewResult, err = service.Repair(context.Background(), repairInput("repair.preview", "1", nil, false), AuthenticatedRepairActor{})
	if err != nil || previewResult.Preview == nil {
		t.Fatal(previewResult, err)
	}
	preview = previewResult.Preview
	refused, err = service.Repair(context.Background(), repairInput("repair.apply", "1", &preview.ID, true), AuthenticatedRepairActor{Kind: "human", ID: "owner", SessionID: "session", Authenticated: true})
	if err != nil || refused.Status != "refused" || refused.RefusalCode == nil || !slices.Contains([]string{"repair_outcome_unavailable", "repair_effect_refused"}, *refused.RefusalCode) {
		t.Fatalf("effect refusal = %#v, %v", refused, err)
	}
}
