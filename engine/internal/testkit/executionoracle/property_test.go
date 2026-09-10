// SPDX-License-Identifier: Apache-2.0

package executionoracle

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"reflect"
	"slices"
	"testing"
)

func TestSeededPropertiesAreRepeatableScopeSafeAndPinned(t *testing.T) {
	first := seededDecisions(0x3_10_2026, 1024)
	second := seededDecisions(0x3_10_2026, 1024)
	if !reflect.DeepEqual(first, second) {
		t.Fatal("same seed produced different decisions")
	}
	encoded, err := json.Marshal(first)
	if err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(encoded)
	const expected = "d88f62a83fe75f0fad6fcf20a14d4c8451d42b9f3f972c0b62593d2dc8d11fa6"
	if got := hex.EncodeToString(digest[:]); got != expected {
		t.Fatalf("seeded decision digest = %s, want %s", got, expected)
	}
}

func seededDecisions(seed uint64, count int) []Decision {
	random := deterministicRandom{state: seed}
	results := make([]Decision, 0, count)
	for index := 0; index < count; index++ {
		snapshot := Baseline()
		snapshot.Run.Scope = Scope{
			ProjectID: fmt.Sprintf("project-%d", index%7), TaskID: fmt.Sprintf("task-%d", index),
			RunID: fmt.Sprintf("run-%d", index), WorkspaceID: fmt.Sprintf("workspace-%d", index%11),
		}
		rebind(&snapshot)
		snapshot.Workspace.State = WorkspaceState(1 + random.intn(4))
		snapshot.Lease.State = LeaseState(1 + random.intn(4))
		snapshot.Lease.DispatchAllowed = snapshot.Lease.State == LeaseCurrent
		snapshot.Provider.Kind = ProviderKind(1 + random.intn(3))
		snapshot.Provider.State = ProviderState(1 + random.intn(5))
		snapshot.Control.Kind = ControlKind(1 + random.intn(5))
		snapshot.Control.HumanConfirmed = snapshot.Control.Kind == ControlEmergencyStop && random.intn(2) == 0
		snapshot.Failure.Kind = FailureKind(1 + random.intn(6))
		snapshot.Liveness.State = LivenessState(1 + random.intn(5))
		snapshot.Liveness.QuietSeconds = uint16(random.intn(420))
		snapshot.Liveness.LocalSamples = uint8(random.intn(5))
		snapshot.Liveness.ExternalCycles = uint8(random.intn(4))
		snapshot.Liveness.IndependentSources = uint8(random.intn(4))
		snapshot.Liveness.TerminalLatencyMillis = uint16(random.intn(1002))
		snapshot.Liveness.ActivePolling = random.intn(20) == 0
		snapshot.Liveness.KnownWait = random.intn(5) == 0
		snapshot.Liveness.DeclaredLongStep = random.intn(7) == 0
		snapshot.Liveness.ExternalOutage = random.intn(6) == 0
		for _, dimension := range []*DimensionUsage{&snapshot.Usage.Time, &snapshot.Usage.Tokens, &snapshot.Usage.Turns, &snapshot.Usage.Cost} {
			dimension.Consumed = uint64(random.intn(101))
			dimension.Reserved = uint64(random.intn(4))
			dimension.NextReservation = uint64(random.intn(3))
			if random.intn(4) == 0 {
				dimension.AcknowledgedRevision = dimension.Revision
			}
		}
		workerCount := random.intn(3)
		for workerIndex := 0; workerIndex < workerCount; workerIndex++ {
			worker := workerFact(snapshot, fmt.Sprintf("worker-%d", workerIndex))
			worker.BootstrapDone = random.intn(2) == 0
			worker.State = AgentState(1 + random.intn(5))
			worker.Prompt = PromptState(1 + random.intn(6))
			if worker.State == AgentClosed && random.intn(2) == 0 {
				worker.Archived = true
				worker.ProcessAbsent = random.intn(2) == 0
			}
			snapshot.Agents = append(snapshot.Agents, worker)
		}
		if workerCount > 0 && random.intn(3) == 0 {
			helper := helperFact(snapshot, "helper", snapshot.Agents[0].ID)
			if random.intn(8) == 0 {
				helper.RunID = "cross-run"
			}
			snapshot.Agents = append(snapshot.Agents, helper)
		}
		if workerCount > 0 && random.intn(4) == 0 {
			snapshot.Candidate = CandidateFact{ID: "candidate", RunID: snapshot.Run.Scope.RunID, WorkerID: snapshot.Agents[0].ID, State: CandidateCurrent}
			if random.intn(6) == 0 {
				snapshot.Candidate.RunID = "cross-run"
			}
		}
		if random.intn(12) == 0 {
			snapshot.MCP.Bindings[1].WorkspaceID = "cross-workspace"
		}
		if random.intn(16) == 0 {
			snapshot.Provider.ProfileRevision = "drifted-profile"
		}

		decision := Evaluate(snapshot)
		permuted := snapshot.Clone()
		slices.Reverse(permuted.Agents)
		slices.Reverse(permuted.MCP.Bindings)
		if got := Evaluate(permuted); !reflect.DeepEqual(got, decision) {
			panic(fmt.Sprintf("input permutation changed decision at vector %d: %#v != %#v", index, got, decision))
		}
		for _, action := range decision.Actions {
			if action.Scope != snapshot.Run.Scope || !validAction(action) {
				panic(fmt.Sprintf("vector %d emitted cross-scope action %#v", index, action))
			}
			if action.Kind == ActionReplaceWorkerBootstrap && snapshot.Run.ReplacementCount >= snapshot.Run.MaximumReplacements {
				panic(fmt.Sprintf("vector %d exceeded replacement budget", index))
			}
		}
		results = append(results, decision)
	}
	return results
}

func rebind(snapshot *Snapshot) {
	scope := snapshot.Run.Scope
	snapshot.Organizer.ProjectID = scope.ProjectID
	snapshot.Workspace.ID = scope.WorkspaceID
	snapshot.Workspace.RunID = scope.RunID
	snapshot.Lease.ProjectID = scope.ProjectID
	snapshot.Control.RunID = scope.RunID
	snapshot.Failure.RunID = scope.RunID
	snapshot.HelperRequest.RunID = scope.RunID
	snapshot.HelperRequest.WorkspaceID = scope.WorkspaceID
	for index := range snapshot.MCP.Bindings {
		snapshot.MCP.Bindings[index].ProjectID = scope.ProjectID
		if snapshot.MCP.Bindings[index].Role != RoleOrganizer {
			snapshot.MCP.Bindings[index].TaskID = scope.TaskID
			snapshot.MCP.Bindings[index].RunID = scope.RunID
			snapshot.MCP.Bindings[index].WorkspaceID = scope.WorkspaceID
		}
	}
}

type deterministicRandom struct{ state uint64 }

func (random *deterministicRandom) intn(bound int) int {
	if bound <= 0 {
		panic("non-positive random bound")
	}
	random.state += 0x9e3779b97f4a7c15
	value := random.state
	value = (value ^ (value >> 30)) * 0xbf58476d1ce4e5b9
	value = (value ^ (value >> 27)) * 0x94d049bb133111eb
	value ^= value >> 31
	return int(value % uint64(bound))
}
