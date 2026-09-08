// SPDX-License-Identifier: Apache-2.0

package execution

import (
	"strings"
	"testing"
)

func testScope() Scope {
	return Scope{
		ProjectID:   "project-1",
		WorkspaceID: "workspace-1",
		TaskID:      "task-1",
		RunID:       "run-1",
	}
}

func admittedIsolation() IsolationObservation {
	return IsolationObservation{
		Observed:                true,
		Runtime:                 "fixture-rootless-oci",
		Rootless:                true,
		ReadOnlyRootFilesystem:  true,
		CapabilitiesDropped:     true,
		NoNewPrivileges:         true,
		PrivateNetworkNamespace: true,
		RuntimeSocketsAbsent:    true,
		ControlToolsAbsent:      true,
		OwnedWorktreeOnly:       true,
		FixedStdioMCP:           true,
	}
}

func operationalPolicyFixture() OperationalPolicy {
	return OperationalPolicy{
		MinimumFreeDiskBasisPoints:  1_000,
		MaximumWorktreeBytes:        1 << 20,
		MaximumProcesses:            32,
		MaximumMemoryBytes:          256 << 20,
		MaximumElapsedMilliseconds:  60_000,
		MaximumOutputBytes:          4 << 20,
		MaximumTemporaryBytes:       16 << 20,
		MaximumObservationAgeMillis: 30_000,
	}
}

func atOperationalBoundary() OperationalObservation {
	return OperationalObservation{
		ID:                  "observation-1",
		ObservedAtMillis:    1_000,
		FreeDiskBasisPoints: measurement(1_000),
		WorktreeBytes:       measurement(uint64(1 << 20)),
		Processes:           measurement(uint64(32)),
		MemoryBytes:         measurement(uint64(256 << 20)),
		ElapsedMilliseconds: measurement(uint64(60_000)),
		OutputBytes:         measurement(uint64(4 << 20)),
		TemporaryBytes:      measurement(uint64(16 << 20)),
	}
}

func measurement(value uint64) Measurement {
	return Measurement{Present: true, Value: value}
}

func TestLifecycleAdmissionCoversEveryInstalledSurface(t *testing.T) {
	fixtures := map[string]LifecycleSurfaces{
		"setup":       {Setup: []string{" setup command "}},
		"teardown":    {Teardown: []string{"teardown command"}},
		"terminal":    {TerminalCommands: []string{"terminal command"}},
		"port-script": {ServicePortScript: []string{"port command"}},
	}
	for name, surfaces := range fixtures {
		t.Run(name, func(t *testing.T) {
			withoutApproval := AdmitLifecycle(testScope(), surfaces, nil)
			if withoutApproval.Kind != AdmissionPark || withoutApproval.Code != NeedLifecycleApproval {
				t.Fatalf("unapproved admission = %#v", withoutApproval)
			}
			if withoutApproval.CleanupAuthorized {
				t.Fatal("parking granted cleanup authority")
			}
			if strings.Contains(string(withoutApproval.Code), "command") {
				t.Fatalf("admission exposed command content: %#v", withoutApproval)
			}

			approval := &LifecycleApproval{
				ActorKind: "human",
				Source:    "authenticated_engine_command",
				ActorID:   "human-1",
				Scope:     testScope(),
				Digest:    LifecycleDigest(surfaces),
			}
			accepted := AdmitLifecycle(testScope(), surfaces, approval)
			if accepted.Kind != AdmissionAllow || accepted.Digest != approval.Digest {
				t.Fatalf("approved admission = %#v", accepted)
			}
		})
	}
}

func TestLifecycleAdmissionRejectsStaleNonHumanAndCrossScopeApproval(t *testing.T) {
	surfaces := LifecycleSurfaces{Setup: []string{"setup command"}}
	valid := LifecycleApproval{
		ActorKind: "human",
		Source:    "authenticated_engine_command",
		ActorID:   "human-1",
		Scope:     testScope(),
		Digest:    LifecycleDigest(surfaces),
	}
	invalid := []LifecycleApproval{
		{ActorKind: "agent", Source: valid.Source, ActorID: valid.ActorID, Scope: valid.Scope, Digest: valid.Digest},
		{ActorKind: valid.ActorKind, Source: "agent_claim", ActorID: valid.ActorID, Scope: valid.Scope, Digest: valid.Digest},
		{ActorKind: valid.ActorKind, Source: valid.Source, ActorID: valid.ActorID, Scope: Scope{ProjectID: "other", WorkspaceID: "workspace-1", TaskID: "task-1", RunID: "run-1"}, Digest: valid.Digest},
		{ActorKind: valid.ActorKind, Source: valid.Source, ActorID: valid.ActorID, Scope: valid.Scope, Digest: strings.Repeat("0", 64)},
	}
	for index := range invalid {
		result := AdmitLifecycle(testScope(), surfaces, &invalid[index])
		if result.Kind != AdmissionPark || result.Code != NeedLifecycleApprovalInvalid || result.CleanupAuthorized {
			t.Fatalf("invalid approval %d = %#v", index, result)
		}
	}
	changed := surfaces
	changed.Setup = append(changed.Setup, "changed command")
	result := AdmitLifecycle(testScope(), changed, &valid)
	if result.Kind != AdmissionPark || result.Code != NeedLifecycleApprovalInvalid {
		t.Fatalf("changed lifecycle reused approval: %#v", result)
	}
}

func TestLifecycleAdmissionRejectsMalformedCommandFactsWithoutHashingThemAsApproval(t *testing.T) {
	for name, command := range map[string]string{
		"oversize": strings.Repeat("x", 4_097),
		"control":  "setup\x00command",
		"unicode":  string([]byte{0xff}),
	} {
		t.Run(name, func(t *testing.T) {
			result := AdmitLifecycle(testScope(), LifecycleSurfaces{Setup: []string{command}}, nil)
			if result.Kind != AdmissionPark || result.Code != NeedLifecycleConfigurationInvalid || result.Digest != "" || result.CleanupAuthorized {
				t.Fatalf("malformed lifecycle admission = %#v", result)
			}
		})
	}
}

func TestOperationalLimitsAcceptExactBoundaryAndParkEveryMissingOrExceededFact(t *testing.T) {
	policy := operationalPolicyFixture()
	boundary := atOperationalBoundary()
	if result := EvaluateOperationalLimits(policy, boundary, 31_000); result.Kind != AdmissionAllow {
		t.Fatalf("exact boundary = %#v", result)
	}

	missing := []func(*OperationalObservation){
		func(value *OperationalObservation) { value.FreeDiskBasisPoints.Present = false },
		func(value *OperationalObservation) { value.WorktreeBytes.Present = false },
		func(value *OperationalObservation) { value.Processes.Present = false },
		func(value *OperationalObservation) { value.MemoryBytes.Present = false },
		func(value *OperationalObservation) { value.ElapsedMilliseconds.Present = false },
		func(value *OperationalObservation) { value.OutputBytes.Present = false },
		func(value *OperationalObservation) { value.TemporaryBytes.Present = false },
	}
	for index, mutate := range missing {
		observation := boundary
		mutate(&observation)
		result := EvaluateOperationalLimits(policy, observation, 1_001)
		if result.Kind != AdmissionPark || result.Code != NeedOperationalFactMissing || result.CleanupAuthorized {
			t.Fatalf("missing fact %d = %#v", index, result)
		}
	}

	exceeded := []struct {
		code   NeedCode
		mutate func(*OperationalObservation)
	}{
		{NeedFreeDiskFloor, func(value *OperationalObservation) { value.FreeDiskBasisPoints.Value-- }},
		{NeedWorktreeBytes, func(value *OperationalObservation) { value.WorktreeBytes.Value++ }},
		{NeedProcessLimit, func(value *OperationalObservation) { value.Processes.Value++ }},
		{NeedMemoryLimit, func(value *OperationalObservation) { value.MemoryBytes.Value++ }},
		{NeedElapsedLimit, func(value *OperationalObservation) { value.ElapsedMilliseconds.Value++ }},
		{NeedOutputLimit, func(value *OperationalObservation) { value.OutputBytes.Value++ }},
		{NeedTemporaryLimit, func(value *OperationalObservation) { value.TemporaryBytes.Value++ }},
	}
	for _, fixture := range exceeded {
		observation := boundary
		fixture.mutate(&observation)
		result := EvaluateOperationalLimits(policy, observation, 1_001)
		if result.Kind != AdmissionPark || result.Code != fixture.code || result.CleanupAuthorized {
			t.Fatalf("exceeded %s = %#v", fixture.code, result)
		}
	}

	for _, fixture := range []struct {
		name string
		now  int64
		obs  OperationalObservation
	}{
		{"missing-id", 1_001, func() OperationalObservation { value := boundary; value.ID = ""; return value }()},
		{"future", 999, boundary},
		{"stale", 31_001, boundary},
	} {
		t.Run(fixture.name, func(t *testing.T) {
			result := EvaluateOperationalLimits(policy, fixture.obs, fixture.now)
			if result.Kind != AdmissionPark || result.Code != NeedOperationalFactMissing || result.CleanupAuthorized {
				t.Fatalf("freshness admission = %#v", result)
			}
		})
	}
}

func TestRootlessOCIAdmissionRequiresEveryBoundaryFact(t *testing.T) {
	accepted := admittedIsolation()
	if result := AdmitIsolation(accepted); result.Kind != AdmissionAllow {
		t.Fatalf("complete rootless OCI observation = %#v", result)
	}
	missing := accepted
	missing.Observed = false
	if result := AdmitIsolation(missing); result.Code != NeedIsolationFactMissing || result.CleanupAuthorized {
		t.Fatalf("missing isolation = %#v", result)
	}
	mutations := []func(*IsolationObservation){
		func(value *IsolationObservation) { value.Rootless = false },
		func(value *IsolationObservation) { value.ReadOnlyRootFilesystem = false },
		func(value *IsolationObservation) { value.CapabilitiesDropped = false },
		func(value *IsolationObservation) { value.NoNewPrivileges = false },
		func(value *IsolationObservation) { value.PrivateNetworkNamespace = false },
		func(value *IsolationObservation) { value.RuntimeSocketsAbsent = false },
		func(value *IsolationObservation) { value.ControlToolsAbsent = false },
		func(value *IsolationObservation) { value.OwnedWorktreeOnly = false },
		func(value *IsolationObservation) { value.FixedStdioMCP = false },
	}
	for index, mutate := range mutations {
		observation := accepted
		mutate(&observation)
		result := AdmitIsolation(observation)
		if result.Kind != AdmissionPark || result.Code != NeedRootlessOCI || result.CleanupAuthorized {
			t.Fatalf("isolation mutation %d = %#v", index, result)
		}
	}
}
