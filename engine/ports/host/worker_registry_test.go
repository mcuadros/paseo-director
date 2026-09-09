// SPDX-License-Identifier: Apache-2.0

package host

import (
	"maps"
	"strings"
	"testing"

	"github.com/mcuadros/director-engine/domain/execution"
)

func workerRegistration(role WorkerRole) WorkerRegistration {
	return WorkerRegistration{
		RootWorkspaceID: "wks_9f01d0202bcf05fa", ExecutionWorkspaceID: "wks_restored_checkout",
		Scope: execution.Scope{ProjectID: "project-1", WorkspaceID: "repository-1", TaskID: "dir-m2.17", RunID: "run-1"},
		Role:  role, Phase: WorkerPhaseBuilding, BaseSHA: strings.Repeat("1", 40),
		CandidateSHA: strings.Repeat("2", 40), RegisteredAt: "2026-09-09T16:00:00Z",
		StartedAt: "2026-09-09T16:00:01Z",
	}
}

func TestWorkerRegistryBindsRootAndRestoredExecutionWorkspace(t *testing.T) {
	registration := workerRegistration(WorkerRoleTaskAgent)
	labels, err := WorkerLabels(registration)
	if err != nil {
		t.Fatal(err)
	}
	if labels["director.root-workspace"] != "wks_9f01d0202bcf05fa" ||
		labels["director.execution-workspace"] != "wks_restored_checkout" ||
		labels["director.task"] != "dir-m2.17" || labels["director.role"] != "task-agent" ||
		labels["director.candidate"] != registration.CandidateSHA {
		t.Fatalf("worker labels = %#v", labels)
	}
	if err := ValidateWorkerLabels(labels, registration); err != nil {
		t.Fatalf("ValidateWorkerLabels() error = %v", err)
	}

	for name, mutate := range map[string]func(map[string]string){
		"missing root":    func(value map[string]string) { delete(value, "director.root-workspace") },
		"wrong workspace": func(value map[string]string) { value["director.execution-workspace"] = "wks_archived" },
		"parented":        func(value map[string]string) { value["paseo.parent-agent-id"] = "agent-parent" },
	} {
		t.Run(name, func(t *testing.T) {
			changed := maps.Clone(labels)
			mutate(changed)
			if err := ValidateWorkerLabels(changed, registration); err == nil {
				t.Fatal("ValidateWorkerLabels accepted changed visibility facts")
			}
		})
	}
}

func TestWorkerRegistryAdmitsBothParentlessDirectorRoles(t *testing.T) {
	for _, role := range []WorkerRole{WorkerRoleTaskAgent, WorkerRoleReviewer} {
		registration := workerRegistration(role)
		if role == WorkerRoleReviewer {
			registration.Phase = WorkerPhaseReviewing
		}
		labels, err := WorkerLabels(registration)
		if err != nil {
			t.Fatalf("WorkerLabels(%q) error = %v", role, err)
		}
		if err := ValidateWorkerLabels(labels, registration); err != nil {
			t.Fatalf("ValidateWorkerLabels(%q) error = %v", role, err)
		}
	}
}

func workerVisibility(t *testing.T, registration WorkerRegistration) execution.WorkerVisibility {
	t.Helper()
	digest, err := RegistrationDigest(registration)
	if err != nil {
		t.Fatal(err)
	}
	return execution.WorkerVisibility{
		RootWorkspaceID:      registration.RootWorkspaceID,
		ExecutionWorkspaceID: registration.ExecutionWorkspaceID,
		Role:                 string(registration.Role), Phase: registration.Phase,
		CandidateSHA: registration.CandidateSHA, BaseSHA: registration.BaseSHA,
		RegisteredAt: registration.RegisteredAt, StartedAt: registration.StartedAt,
		Digest: digest,
	}
}

func TestRegistrationDigestBindsTheExactPublishedLabelSet(t *testing.T) {
	registration := workerRegistration(WorkerRoleTaskAgent)
	digest, err := RegistrationDigest(registration)
	if err != nil {
		t.Fatal(err)
	}
	if len(digest) != 64 {
		t.Fatalf("registration digest = %q", digest)
	}

	// The digest must move with every visible fact, otherwise a frozen record
	// could be replayed for a worker registered against a different root
	// workspace, Task, phase, or Candidate.
	for name, mutate := range map[string]func(*WorkerRegistration){
		"root workspace":      func(value *WorkerRegistration) { value.RootWorkspaceID = "wks_other_root" },
		"execution workspace": func(value *WorkerRegistration) { value.ExecutionWorkspaceID = "wks_other" },
		"task":                func(value *WorkerRegistration) { value.Scope.TaskID = "dir-m2.18" },
		"role":                func(value *WorkerRegistration) { value.Role = WorkerRoleReviewer },
		"phase":               func(value *WorkerRegistration) { value.Phase = WorkerPhaseReviewing },
		"candidate":           func(value *WorkerRegistration) { value.CandidateSHA = strings.Repeat("3", 40) },
	} {
		t.Run(name, func(t *testing.T) {
			changed := registration
			mutate(&changed)
			other, err := RegistrationDigest(changed)
			if err != nil {
				t.Fatal(err)
			}
			if other == digest {
				t.Fatal("registration digest ignored a visible launch fact")
			}
		})
	}

	if _, err := RegistrationDigest(workerRegistration("helper")); err == nil {
		t.Fatal("RegistrationDigest accepted a role outside the published vocabulary")
	}
}

func TestRegistrationFromVisibilityRefusesAnIncompleteOrForgedRecord(t *testing.T) {
	registration := workerRegistration(WorkerRoleTaskAgent)
	visibility := workerVisibility(t, registration)
	rebuilt, err := RegistrationFromVisibility(registration.Scope, visibility)
	if err != nil {
		t.Fatal(err)
	}
	if rebuilt != registration {
		t.Fatalf("rebuilt registration = %#v", rebuilt)
	}

	for name, mutate := range map[string]func(*execution.WorkerVisibility){
		"no root workspace": func(value *execution.WorkerVisibility) { value.RootWorkspaceID = "" },
		"no digest":         func(value *execution.WorkerVisibility) { value.Digest = "" },
		"no base":           func(value *execution.WorkerVisibility) { value.BaseSHA = "" },
		"helper role":       func(value *execution.WorkerVisibility) { value.Role = "helper" },
	} {
		t.Run(name, func(t *testing.T) {
			changed := visibility
			mutate(&changed)
			if _, err := RegistrationFromVisibility(registration.Scope, changed); err == nil {
				t.Fatal("RegistrationFromVisibility accepted an unusable record")
			}
		})
	}
}

func TestAdmitAgentCreateIsTheLastGateBeforeAnInvisibleWorker(t *testing.T) {
	registration := workerRegistration(WorkerRoleTaskAgent)
	labels, err := WorkerLabels(registration)
	if err != nil {
		t.Fatal(err)
	}
	command := Command{
		RequestID: "request-1", IdempotencyKey: "key-1", Capability: CapabilityTaskAgentCreate,
		Arguments: Arguments{
			Scope: registration.Scope, EffectKind: execution.EffectAgentCreate,
			EffectID: "effect-1", Labels: labels, BindingHash: "binding-1",
			InitialPrompt: ZeroWorkBootstrapPrompt,
		},
	}
	if err := AdmitAgentCreate(command, registration); err != nil {
		t.Fatalf("AdmitAgentCreate() error = %v", err)
	}

	parent := "agent-parent"
	for name, mutate := range map[string]func(*Command){
		"unregistered":        func(value *Command) { value.Arguments.Labels = nil },
		"parented":            func(value *Command) { value.Arguments.ParentAgentID = &parent },
		"other run":           func(value *Command) { value.Arguments.Scope.RunID = "run-2" },
		"other effect":        func(value *Command) { value.Arguments.EffectKind = execution.EffectHostViewCreate },
		"real work":           func(value *Command) { value.Arguments.InitialPrompt = "do the task" },
		"notify create":       func(value *Command) { value.Arguments.NotifyOnFinish = true },
		"reviewer capability": func(value *Command) { value.Capability = CapabilityReviewerAgentCreate },
	} {
		t.Run(name, func(t *testing.T) {
			changed := command
			mutate(&changed)
			if err := AdmitAgentCreate(changed, registration); err == nil {
				t.Fatal("AdmitAgentCreate authorized an unusable agent creation")
			}
		})
	}
}

func TestAdmitAgentPromptRequiresPersistedIdentityAndNotification(t *testing.T) {
	registration := workerRegistration(WorkerRoleTaskAgent)
	command := Command{Capability: CapabilityAgentPrompt, Arguments: Arguments{
		Scope: registration.Scope, EffectKind: execution.EffectAgentPrompt,
		EffectID: "prompt-1", WorkspaceID: registration.ExecutionWorkspaceID,
		AgentID: "agent-1", InitialPrompt: "perform the exact Task", NotifyOnFinish: true,
	}}
	if err := AdmitAgentPrompt(command, registration, "agent-1"); err != nil {
		t.Fatal(err)
	}
	for name, mutate := range map[string]func(*Command){
		"wrong agent": func(value *Command) { value.Arguments.AgentID = "other" },
		"no notify":   func(value *Command) { value.Arguments.NotifyOnFinish = false },
		"bootstrap":   func(value *Command) { value.Arguments.InitialPrompt = ZeroWorkBootstrapPrompt },
		"labels":      func(value *Command) { value.Arguments.Labels = map[string]string{"unexpected": "label"} },
	} {
		t.Run(name, func(t *testing.T) {
			changed := command
			mutate(&changed)
			if err := AdmitAgentPrompt(changed, registration, "agent-1"); err == nil {
				t.Fatal("invalid real prompt admitted")
			}
		})
	}
}

func TestReviewerUsesTheSameBootstrapThenPromptContract(t *testing.T) {
	registration := workerRegistration(WorkerRoleReviewer)
	labels, err := WorkerLabels(registration)
	if err != nil {
		t.Fatal(err)
	}
	create := Command{Capability: CapabilityReviewerAgentCreate, Arguments: Arguments{
		Scope: registration.Scope, EffectKind: execution.EffectAgentCreate,
		EffectID: "reviewer-create", InitialPrompt: ZeroWorkBootstrapPrompt, Labels: labels,
	}}
	if err := AdmitAgentCreate(create, registration); err != nil {
		t.Fatal(err)
	}
	prompt := Command{Capability: CapabilityAgentPrompt, Arguments: Arguments{
		Scope: registration.Scope, EffectKind: execution.EffectAgentPrompt,
		EffectID: "reviewer-prompt", WorkspaceID: registration.ExecutionWorkspaceID,
		AgentID: "reviewer-1", InitialPrompt: "review the exact Candidate", NotifyOnFinish: true,
	}}
	if err := AdmitAgentPrompt(prompt, registration, "reviewer-1"); err != nil {
		t.Fatal(err)
	}
}
