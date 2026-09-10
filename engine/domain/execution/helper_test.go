// SPDX-License-Identifier: Apache-2.0

package execution

import (
	"strings"
	"testing"
)

func helperState() State {
	return State{
		SchemaVersion:           SchemaVersion,
		Scope:                   Scope{ProjectID: "project", WorkspaceID: "workspace", TaskID: "task", RunID: "run"},
		WorktreePath:            "/tmp/director-primary",
		HostView:                Effect{ExternalID: "workspace-native"},
		PrimarySession:          PrimarySession{NativeAgentID: "primary-agent", PermissionMode: "workspace-write"},
		EffectiveProfilesSHA256: strings.Repeat("a", 64),
		RepositoryBinding:       RepositoryBinding{BaseSHA: strings.Repeat("b", 40)},
		HelperPolicy:            HelperPolicy{MaximumPerTask: 3, MaximumConcurrentAgents: 8},
	}
}

func helperIsolation() IsolationObservation {
	return IsolationObservation{
		Observed: true, Runtime: "rootless-oci", Rootless: true, ReadOnlyRootFilesystem: true,
		CapabilitiesDropped: true, NoNewPrivileges: true, PrivateNetworkNamespace: true,
		RuntimeSocketsAbsent: true, ControlToolsAbsent: true, OwnedWorktreeOnly: true, FixedStdioMCP: true,
	}
}

func TestHelperRequestsDeriveWriterIsolationAndReadOnlySharing(t *testing.T) {
	for _, mode := range []HelperMode{HelperWriter, HelperReadOnly} {
		t.Run(string(mode), func(t *testing.T) {
			state := helperState()
			path := HelperWorktreePath(state.WorktreePath, "helper-1", mode)
			next, err := AppendHelperRequest(state, "helper-1", "request-1", "primary-agent", mode, "bounded contribution", path)
			if err != nil {
				t.Fatal(err)
			}
			if len(next.Helpers) != 1 || next.Helpers[0].Scope != state.Scope || next.Helpers[0].ParentAgentID != "primary-agent" {
				t.Fatalf("helper attribution = %#v", next.Helpers)
			}
			if (mode == HelperWriter && path == state.WorktreePath) || (mode == HelperReadOnly && path != state.WorktreePath) {
				t.Fatalf("mode %s path = %s", mode, path)
			}
			if !ValidHelpers(next) {
				t.Fatal("valid helper request failed TaskStore guard")
			}
		})
	}
}

func TestHelperTaskQuotaAndRequestReplayAreDeterministic(t *testing.T) {
	state := helperState()
	state.HelperPolicy.MaximumPerTask = 1
	path := HelperWorktreePath(state.WorktreePath, "helper-1", HelperWriter)
	first, err := AppendHelperRequest(state, "helper-1", "request-1", "primary-agent", HelperWriter, "write", path)
	if err != nil {
		t.Fatal(err)
	}
	replay, err := AppendHelperRequest(first, "helper-1", "request-1", "primary-agent", HelperWriter, "write", path)
	if err != nil || len(replay.Helpers) != 1 {
		t.Fatalf("replay = %#v, %v", replay.Helpers, err)
	}
	secondPath := HelperWorktreePath(state.WorktreePath, "helper-2", HelperWriter)
	if _, err := AppendHelperRequest(first, "helper-2", "request-2", "primary-agent", HelperWriter, "write", secondPath); err == nil || err.Error() != string(NeedHelperQuota) {
		t.Fatalf("quota error = %v", err)
	}
	if _, err := AppendHelperRequest(first, "helper-1", "request-1", "primary-agent", HelperReadOnly, "changed", state.WorktreePath); err == nil {
		t.Fatal("same request identity accepted changed payload")
	}
}

func TestHelperGlobalCapacityUsesFreshFiniteObservation(t *testing.T) {
	policy := HelperPolicy{MaximumPerTask: 3, MaximumConcurrentAgents: 8}
	observation := HelperCapacityObservation{ID: "capacity-1", ObservedAtMillis: 1_000, MaximumAgeMillis: 30_000, ActiveAgents: 7}
	observation.FactHash = HelperCapacityObservationHash(observation)
	if !HelperCapacityAvailable(observation, policy, 1_001) {
		t.Fatal("exact final capacity slot was refused")
	}
	observation.ActiveAgents = 8
	observation.FactHash = HelperCapacityObservationHash(observation)
	if HelperCapacityAvailable(observation, policy, 1_001) {
		t.Fatal("global agent capacity was exceeded")
	}
	observation.ActiveAgents = 7
	observation.FactHash = HelperCapacityObservationHash(observation)
	if CurrentHelperCapacityObservation(observation, policy, 31_001) {
		t.Fatal("stale capacity observation was accepted")
	}
	observation.FactHash = ""
	if CurrentHelperCapacityObservation(observation, policy, 1_001) {
		t.Fatal("missing capacity telemetry was accepted")
	}
}

func TestHelperBoundaryRequiresEveryADR0014FactAndExactSharingMode(t *testing.T) {
	state := helperState()
	state.LifecycleDigest = strings.Repeat("c", 64)
	writerPath := HelperWorktreePath(state.WorktreePath, "helper-1", HelperWriter)
	state, _ = AppendHelperRequest(state, "helper-1", "request-1", "primary-agent", HelperWriter, "write", writerPath)
	helper := state.Helpers[0]
	baseline := HelperBoundaryObservation{
		ID: "boundary-1", HelperID: helper.ID, Mode: HelperWriter, ObservedAtMillis: 1_000, MaximumAgeMillis: 30_000,
		Isolation: helperIsolation(), LifecycleDigest: state.LifecycleDigest, TrustedRuntime: true, SeparateCheckout: true,
		PrimaryCheckoutUnavailable: true, EngineStateAbsent: true, TaskStoreCredentialAbsent: true,
		DeliveryCredentialAbsent: true, RawControlAbsent: true, ProviderCredentialOnly: true, OperationalTelemetryReady: true,
		ProviderPolicyApplied: true,
	}
	baseline.FactHash = HelperBoundaryObservationHash(baseline)
	if !CurrentHelperBoundary(baseline, helper, state, 1_001) {
		t.Fatal("complete writer boundary was refused")
	}
	mutations := []func(*HelperBoundaryObservation){
		func(value *HelperBoundaryObservation) { value.Isolation.Rootless = false },
		func(value *HelperBoundaryObservation) { value.TrustedRuntime = false },
		func(value *HelperBoundaryObservation) { value.SeparateCheckout = false },
		func(value *HelperBoundaryObservation) { value.PrimaryCheckoutUnavailable = false },
		func(value *HelperBoundaryObservation) { value.EngineStateAbsent = false },
		func(value *HelperBoundaryObservation) { value.TaskStoreCredentialAbsent = false },
		func(value *HelperBoundaryObservation) { value.DeliveryCredentialAbsent = false },
		func(value *HelperBoundaryObservation) { value.RawControlAbsent = false },
		func(value *HelperBoundaryObservation) { value.ProviderCredentialOnly = false },
		func(value *HelperBoundaryObservation) { value.ProviderPolicyApplied = false },
		func(value *HelperBoundaryObservation) { value.ProviderPolicyReadOnly = true },
		func(value *HelperBoundaryObservation) { value.OperationalTelemetryReady = false },
		func(value *HelperBoundaryObservation) { value.LifecycleDigest = strings.Repeat("d", 64) },
	}
	for index, mutate := range mutations {
		current := baseline
		mutate(&current)
		current.FactHash = HelperBoundaryObservationHash(current)
		if CurrentHelperBoundary(current, helper, state, 1_001) {
			t.Fatalf("boundary mutation %d survived", index)
		}
	}

	readerState := helperState()
	readerState.LifecycleDigest = state.LifecycleDigest
	readerState, _ = AppendHelperRequest(readerState, "helper-r", "request-r", "primary-agent", HelperReadOnly, "inspect", readerState.WorktreePath)
	reader := readerState.Helpers[0]
	readBoundary := baseline
	readBoundary.HelperID = reader.ID
	readBoundary.Mode = HelperReadOnly
	readBoundary.SeparateCheckout = false
	readBoundary.PrimaryCheckoutUnavailable = false
	readBoundary.PrimaryCheckoutReadOnly = true
	readBoundary.ProviderPolicyReadOnly = true
	readBoundary.FactHash = HelperBoundaryObservationHash(readBoundary)
	if !CurrentHelperBoundary(readBoundary, reader, readerState, 1_001) {
		t.Fatal("proved read-only sharing was refused")
	}
	readBoundary.PrimaryCheckoutReadOnly = false
	readBoundary.FactHash = HelperBoundaryObservationHash(readBoundary)
	if CurrentHelperBoundary(readBoundary, reader, readerState, 1_001) {
		t.Fatal("writable shared primary checkout was admitted")
	}
}

func TestHelperAdmissionIsOneUse(t *testing.T) {
	helper := Helper{Phase: HelperAdmissionReady, Admission: &HelperAdmission{IssuedAtMillis: 1_000}}
	consumed, err := ConsumeHelperAdmission(helper, "invoke-1", 1_001)
	if err != nil || consumed.Phase != HelperInvocationConsumed || consumed.Admission.ConsumedAtMillis != 1_001 {
		t.Fatalf("consume = %#v, %v", consumed, err)
	}
	if _, err := ConsumeHelperAdmission(consumed, "invoke-2", 1_002); err == nil {
		t.Fatal("one-use admission was consumed twice")
	}
}

func TestHelperOwnershipMutationsFailClosed(t *testing.T) {
	state := helperState()
	path := HelperWorktreePath(state.WorktreePath, "helper-1", HelperWriter)
	state, _ = AppendHelperRequest(state, "helper-1", "request-1", "primary-agent", HelperWriter, "write", path)
	mutations := []func(*State){
		func(value *State) { value.Helpers[0].Scope.WorkspaceID = "workspace-other" },
		func(value *State) { value.Helpers[0].Scope.RunID = "run-other" },
		func(value *State) { value.Helpers[0].ParentAgentID = "agent-other" },
		func(value *State) { value.Helpers[0].WorktreePath = value.WorktreePath },
		func(value *State) { value.Helpers[0].Mode = HelperReadOnly },
		func(value *State) { value.Helpers[0].RequestID = "" },
		func(value *State) { value.Helpers[0].CapacityReservationID = "capacity-other" },
	}
	for index, mutate := range mutations {
		changed := state
		changed.Helpers = CloneHelpers(state.Helpers)
		mutate(&changed)
		if ValidHelpers(changed) {
			t.Fatalf("ownership mutation %d survived", index)
		}
	}
}

func TestHelperPathAndQuotaPropertyMatrix(t *testing.T) {
	for maximum := uint32(1); maximum <= 8; maximum++ {
		state := helperState()
		state.HelperPolicy.MaximumPerTask = maximum
		state.HelperPolicy.MaximumConcurrentAgents = maximum + 4
		for index := uint32(0); index < maximum; index++ {
			id := "helper-" + string(rune('a'+index))
			mode := HelperWriter
			if index%2 == 1 {
				mode = HelperReadOnly
			}
			path := HelperWorktreePath(state.WorktreePath, id, mode)
			var err error
			state, err = AppendHelperRequest(state, id, "request-"+id, "primary-agent", mode, "bounded", path)
			if err != nil || !ValidHelpers(state) {
				t.Fatalf("maximum=%d index=%d: %v", maximum, index, err)
			}
		}
		overflowID := "helper-overflow"
		if _, err := AppendHelperRequest(state, overflowID, "request-overflow", "primary-agent", HelperWriter, "bounded", HelperWorktreePath(state.WorktreePath, overflowID, HelperWriter)); err == nil {
			t.Fatalf("maximum=%d admitted helper beyond quota", maximum)
		}
	}
}
