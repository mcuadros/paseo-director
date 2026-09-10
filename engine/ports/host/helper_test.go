// SPDX-License-Identifier: Apache-2.0

package host

import (
	"strings"
	"testing"

	"github.com/mcuadros/director-engine/domain/execution"
)

func helperRegistrationFixture() HelperRegistration {
	return HelperRegistration{
		RootWorkspaceID: "root-workspace", ExecutionWorkspaceID: "execution-workspace",
		Scope:         execution.Scope{ProjectID: "project", WorkspaceID: "repository", TaskID: "task", RunID: "run"},
		ParentAgentID: "primary-agent", BaseSHA: strings.Repeat("a", 40), EffectID: "helper-effect",
		ProfileSHA256: strings.Repeat("b", 64), SessionSHA256: strings.Repeat("c", 64),
		RegisteredAt: "2026-09-10T08:00:00Z", StartedAt: "2026-09-10T08:00:00Z",
	}
}

func helperCommandFixture(t *testing.T, kind execution.EffectKind, capability Capability, nativeID string) Command {
	t.Helper()
	registration := helperRegistrationFixture()
	labels, err := HelperLabels(registration)
	if err != nil {
		t.Fatal(err)
	}
	parent := registration.ParentAgentID
	return Command{
		RequestID: "request-helper", IdempotencyKey: "idempotency-helper", ExpectedVersion: 1,
		Capability: capability,
		Arguments: Arguments{
			Scope: registration.Scope, EffectKind: kind, EffectID: registration.EffectID,
			WorktreePath: "/srv/primary", WorkspaceID: registration.ExecutionWorkspaceID,
			AgentID: nativeID, Title: "Helper helper-1", ParentAgentID: &parent,
			Labels: labels, BindingHash: strings.Repeat("d", 64),
			InitialPrompt: execution.HelperBootstrapPrompt, ClientMessageID: "message-helper-bootstrap",
		},
	}
}

func TestHelperHostBoundaryAdmitsOnlyObservationAndExactArchive(t *testing.T) {
	registration := helperRegistrationFixture()
	observe := helperCommandFixture(t, execution.EffectHelperAgentObserve, CapabilityHelperAgentObserve, "")
	if err := AdmitHelperObserve(observe, registration, ""); err != nil {
		t.Fatal(err)
	}
	archive := helperCommandFixture(t, execution.EffectHelperAgentArchive, CapabilityAgentArchive, "native-helper")
	if err := AdmitHelperArchive(archive, registration, "native-helper"); err != nil {
		t.Fatal(err)
	}

	mutations := []func(*Command){
		func(value *Command) { other := "other-parent"; value.Arguments.ParentAgentID = &other },
		func(value *Command) { value.Arguments.Scope.RunID = "other-run" },
		func(value *Command) { value.Arguments.WorkspaceID = "other-workspace" },
		func(value *Command) { value.Arguments.Labels["director.task"] = "other-task" },
		func(value *Command) { value.Capability = CapabilityTaskAgentCreate },
	}
	for index, mutate := range mutations {
		changed := helperCommandFixture(t, execution.EffectHelperAgentObserve, CapabilityHelperAgentObserve, "")
		mutate(&changed)
		if err := AdmitHelperObserve(changed, registration, ""); err == nil {
			t.Fatalf("helper host mutation %d survived", index)
		}
	}
}
