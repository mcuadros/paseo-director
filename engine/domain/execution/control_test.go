// SPDX-License-Identifier: Apache-2.0

package execution

import "testing"

func testControlIntent(kind ControlKind) ControlIntent {
	value := ControlIntent{
		RequestID: "request-control-domain", Kind: kind, ProjectID: "project",
		ActorKind: ControlActorHuman, ActorID: "owner", ActorSessionID: "session",
		Source: "server", Authenticated: true, RequestedAtMillis: 1_000,
	}
	if kind == ControlCancelTask {
		value.TaskID, value.RunID = "task", "run"
	}
	if kind == ControlEmergencyStop {
		value.ConfirmationID = "emergency-confirmation-current"
	}
	value.ID = ControlIntentID(value)
	return value
}

func TestEmergencyConfirmationIdentityIsOneUseAndConsumptionStable(t *testing.T) {
	value := EmergencyConfirmation{
		ProjectID: "project", ExpectedProjectVersion: 4, ActorID: "owner", ActorSessionID: "session",
		IssuedAtMillis: 1_000, ExpiresAtMillis: 1_000 + EmergencyConfirmationTTL,
		ChallengeSHA256: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
	}
	value.ID = EmergencyConfirmationID(value)
	if !ValidEmergencyConfirmation(value) {
		t.Fatal("fresh confirmation was invalid")
	}
	value.ConsumedAtMillis = 2_000
	if !ValidEmergencyConfirmation(value) || value.ID != EmergencyConfirmationID(value) {
		t.Fatal("one-use consumption changed the immutable confirmation identity")
	}
	for _, mutate := range []func(*EmergencyConfirmation){
		func(item *EmergencyConfirmation) { item.ProjectID = "other" },
		func(item *EmergencyConfirmation) { item.ExpectedProjectVersion++ },
		func(item *EmergencyConfirmation) { item.ActorSessionID = "other" },
		func(item *EmergencyConfirmation) { item.ExpiresAtMillis++ },
		func(item *EmergencyConfirmation) {
			item.ChallengeSHA256 = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
		},
	} {
		mutant := value
		mutate(&mutant)
		if ValidEmergencyConfirmation(mutant) {
			t.Fatalf("changed confirmation remained valid: %#v", mutant)
		}
	}
}

func TestControlIntentCannotInferEmergencyFromAgentOrConfirmationBoolean(t *testing.T) {
	value := testControlIntent(ControlEmergencyStop)
	if !ValidControlIntent(value) {
		t.Fatal("server-authenticated emergency intent was invalid")
	}
	value.Source = "agent"
	value.ID = ControlIntentID(value)
	if ValidControlIntent(value) {
		t.Fatal("agent-authored emergency intent was admitted")
	}
	value = testControlIntent(ControlEmergencyStop)
	value.ActorKind = ControlActorCoordinator
	value.ID = ControlIntentID(value)
	if ValidControlIntent(value) {
		t.Fatal("non-human emergency intent was admitted")
	}
}

func TestControlledAgentSetBindsIdentityNotMutableProgress(t *testing.T) {
	targets := []ControlledAgent{{Identity: ControlledAgentIdentity{
		ID: "worker", Role: ControlledTaskAgent, WorkspaceID: "workspace",
		Title: "Task", WorktreePath: "/tmp/worktree",
	}}}
	digest := ControlledAgentSetSHA256(targets)
	targets[0].Archive = Effect{ID: "archive", Kind: EffectControlAgentArchive, Phase: EffectComplete, AttemptLimit: 2}
	targets[0].Archived, targets[0].ProcessAbsent = true, true
	targets[0].TerminalEventID = "event"
	targets[0].TerminalEventHash = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	targets[0].TerminalCursor = 1
	if got := ControlledAgentSetSHA256(targets); got != digest {
		t.Fatalf("progress changed target identity digest: %s != %s", got, digest)
	}
	targets[0].Identity.WorkspaceID = "other"
	if got := ControlledAgentSetSHA256(targets); got == digest {
		t.Fatal("scope change did not change target identity digest")
	}
}
