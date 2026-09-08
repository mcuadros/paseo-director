// SPDX-License-Identifier: Apache-2.0

package eligibility

import (
	"testing"

	"github.com/mcuadros/director-engine/domain/execution"
)

func trueFact() BooleanFact { return BooleanFact{Observed: true, Value: true} }

func eligibleFacts() Facts {
	observation := execution.OperationalObservation{
		ID:                  "launch-limits-1",
		ObservedAtMillis:    1_000,
		FreeDiskBasisPoints: execution.Measurement{Present: true, Value: 1_000},
		WorktreeBytes:       execution.Measurement{Present: true, Value: 0},
		Processes:           execution.Measurement{Present: true, Value: 1},
		MemoryBytes:         execution.Measurement{Present: true, Value: 1},
		ElapsedMilliseconds: execution.Measurement{Present: true, Value: 0},
		OutputBytes:         execution.Measurement{Present: true, Value: 0},
		TemporaryBytes:      execution.Measurement{Present: true, Value: 0},
	}
	return Facts{
		SchemaVersion:           SchemaVersion,
		Scope:                   execution.Scope{ProjectID: "project-1", WorkspaceID: "workspace-1", TaskID: "task-1", RunID: "run-1"},
		ProjectLeaseCurrent:     trueFact(),
		ProjectActive:           trueFact(),
		OrganizerRevisionActive: trueFact(),
		TaskComplete:            trueFact(),
		DependenciesSatisfied:   trueFact(),
		NoActiveRun:             trueFact(),
		LaunchPolicyAllows:      trueFact(),
		CapacityAvailable:       trueFact(),
		BudgetsAvailable:        trueFact(),
		ProviderAdmitted:        trueFact(),
		RepositoryIdentityExact: trueFact(),
		LifecycleSurfaces:       execution.LifecycleSurfaces{},
		Isolation: execution.IsolationObservation{
			Observed: true, Runtime: "fixture", Rootless: true,
			ReadOnlyRootFilesystem: true, CapabilitiesDropped: true,
			NoNewPrivileges: true, PrivateNetworkNamespace: true,
			RuntimeSocketsAbsent: true, ControlToolsAbsent: true,
			OwnedWorktreeOnly: true, FixedStdioMCP: true,
		},
		OperationalPolicy: execution.OperationalPolicy{
			MinimumFreeDiskBasisPoints: 1_000, MaximumWorktreeBytes: 1,
			MaximumProcesses: 1, MaximumMemoryBytes: 1,
			MaximumElapsedMilliseconds: 1, MaximumOutputBytes: 1,
			MaximumTemporaryBytes: 1, MaximumObservationAgeMillis: 30_000,
		},
		OperationalObservation: observation,
		TaskStoreNowMillis:     1_001,
	}
}

func TestReduceIsPureVersionedAndNamesItsAdmittedFacts(t *testing.T) {
	facts := eligibleFacts()
	first := Reduce(facts)
	second := Reduce(facts)
	if first != second {
		t.Fatalf("same facts produced different decisions: %#v %#v", first, second)
	}
	if first.Kind != DecisionEligible || first.SchemaVersion != SchemaVersion || first.DecisionID == "" || first.FactsHash == "" {
		t.Fatalf("eligible decision = %#v", first)
	}
	if first.LifecycleDigest != execution.LifecycleDigest(facts.LifecycleSurfaces) {
		t.Fatalf("lifecycle digest = %q", first.LifecycleDigest)
	}
}

func TestReduceDistinguishesDependencyWaitFromSafetyParking(t *testing.T) {
	facts := eligibleFacts()
	facts.DependenciesSatisfied.Value = false
	if decision := Reduce(facts); decision.Kind != DecisionWaitQueued || decision.Code != CodeDependenciesUnsatisfied {
		t.Fatalf("dependency decision = %#v", decision)
	}

	facts = eligibleFacts()
	facts.OperationalObservation.MemoryBytes.Present = false
	decision := Reduce(facts)
	if decision.Kind != DecisionEscalate || decision.Code != CodeOperationalFactMissing || decision.CleanupAuthorized {
		t.Fatalf("missing operational fact = %#v", decision)
	}
}

func TestReduceRequiresAnExactHumanLifecycleDigest(t *testing.T) {
	facts := eligibleFacts()
	facts.LifecycleSurfaces.Setup = []string{"fixture setup"}
	withoutApproval := Reduce(facts)
	if withoutApproval.Kind != DecisionEscalate || withoutApproval.Code != CodeLifecycleApprovalRequired {
		t.Fatalf("unapproved lifecycle = %#v", withoutApproval)
	}
	facts.LifecycleApproval = &execution.LifecycleApproval{
		ActorKind: "human", Source: "authenticated_engine_command", ActorID: "human-1",
		Scope: facts.Scope, Digest: execution.LifecycleDigest(facts.LifecycleSurfaces),
	}
	if decision := Reduce(facts); decision.Kind != DecisionEligible {
		t.Fatalf("approved lifecycle = %#v", decision)
	}
}
