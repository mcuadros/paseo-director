// SPDX-License-Identifier: Apache-2.0

package host

import (
	"bytes"
	"strings"
	"testing"

	"github.com/mcuadros/director-engine/domain/execution"
	"github.com/mcuadros/director-engine/domain/runtimebudget"
)

func TestEmbeddedContractAndDescriptor(t *testing.T) {
	descriptor, err := ExpectedDescriptor()
	if err != nil {
		t.Fatalf("ExpectedDescriptor() error = %v", err)
	}
	if err := ValidateDescriptor(descriptor); err != nil {
		t.Fatalf("ValidateDescriptor(expected) error = %v", err)
	}
	if descriptor.ContractVersion != "director-host/v1" {
		t.Fatalf("contract version = %q", descriptor.ContractVersion)
	}
	if descriptor.CredentialScope != "full-daemon-operator" {
		t.Fatalf("credential scope = %q", descriptor.CredentialScope)
	}
	if len(descriptor.ContractHash) != 64 {
		t.Fatalf("contract hash length = %d", len(descriptor.ContractHash))
	}
	definition, err := EmbeddedDefinition()
	if err != nil {
		t.Fatal(err)
	}
	if definition.BoardQuery.Name != "board.snapshot" || definition.BoardQuery.Path != "/v1/board" || len(definition.BoardQuery.States) != 6 {
		t.Fatalf("Board query definition = %#v", definition.BoardQuery)
	}
}

func TestCanonicalHashIgnoresWhitespaceAndObjectKeyOrder(t *testing.T) {
	first := []byte(`{
		"schemaVersion": 1,
		"contractVersion": "v1",
		"credentialScope": "scope",
		"capabilities": ["one", "two"],
		"nested": {"limit": 10, "enabled": true}
	}`)
	second := []byte(`{"nested":{"enabled":true,"limit":10},"capabilities":["one","two"],"credentialScope":"scope","contractVersion":"v1","schemaVersion":1}`)
	firstCanonical, err := CanonicalJSON(first)
	if err != nil {
		t.Fatal(err)
	}
	secondCanonical, err := CanonicalJSON(second)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(firstCanonical, secondCanonical) {
		t.Fatalf("canonical documents differ:\n%s\n%s", firstCanonical, secondCanonical)
	}
	firstHash, err := CanonicalSHA256(first)
	if err != nil {
		t.Fatal(err)
	}
	secondHash, err := CanonicalSHA256(second)
	if err != nil {
		t.Fatal(err)
	}
	if firstHash != secondHash {
		t.Fatalf("canonical hashes differ: %s != %s", firstHash, secondHash)
	}
	changedHash, err := CanonicalSHA256(bytes.ReplaceAll(second, []byte(`"two"`), []byte(`"changed"`)))
	if err != nil {
		t.Fatal(err)
	}
	if changedHash == firstHash {
		t.Fatal("semantic contract drift did not change the hash")
	}
}

func TestCanonicalJSONRejectsDuplicateKeysAtEveryDepth(t *testing.T) {
	for name, schema := range map[string]string{
		"root":   `{"schemaVersion":1,"schemaVersion":1}`,
		"nested": `{"schemaVersion":1,"nested":{"same":1,"same":2}}`,
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := CanonicalJSON([]byte(schema)); err == nil {
				t.Fatal("CanonicalJSON() accepted duplicate object keys")
			}
		})
	}
}

func TestValidateDescriptorFailsClosed(t *testing.T) {
	expected, err := ExpectedDescriptor()
	if err != nil {
		t.Fatal(err)
	}
	tests := map[string]Descriptor{
		"scope": {
			CredentialScope: "scoped",
			ContractVersion: expected.ContractVersion,
			ContractHash:    expected.ContractHash,
			Capabilities:    expected.Capabilities,
		},
		"version": {
			CredentialScope: expected.CredentialScope,
			ContractVersion: "director-host/v2",
			ContractHash:    expected.ContractHash,
			Capabilities:    expected.Capabilities,
		},
		"hash": {
			CredentialScope: expected.CredentialScope,
			ContractVersion: expected.ContractVersion,
			ContractHash:    strings.Repeat("0", 64),
			Capabilities:    expected.Capabilities,
		},
		"capability": {
			CredentialScope: expected.CredentialScope,
			ContractVersion: expected.ContractVersion,
			ContractHash:    expected.ContractHash,
			Capabilities:    expected.Capabilities[:len(expected.Capabilities)-1],
		},
	}
	for name, descriptor := range tests {
		t.Run(name, func(t *testing.T) {
			if err := ValidateDescriptor(descriptor); err == nil {
				t.Fatal("ValidateDescriptor() accepted drift")
			}
		})
	}
}

func TestParseDefinitionRejectsDuplicateCapabilities(t *testing.T) {
	schema := []byte(`{"schemaVersion":1,"contractVersion":"v1","credentialScope":"scope","capabilities":["same","same"],"boardQuery":{"name":"board.snapshot","method":"GET","path":"/v1/board","schemaVersion":1,"maximumTasks":1000,"maximumBytes":2097152,"states":["needs_you","queued","building","validating","in_review","ready"]}}`)
	if _, err := ParseDefinition(schema); err == nil {
		t.Fatal("ParseDefinition() accepted duplicate capabilities")
	}
}

func TestParseDefinitionRequiresTheExactWorkerRegistry(t *testing.T) {
	const prefix = `{"schemaVersion":1,"contractVersion":"v1","credentialScope":"scope","capabilities":["one"],"boardQuery":{"name":"board.snapshot","method":"GET","path":"/v1/board","schemaVersion":1,"maximumTasks":1000,"maximumBytes":2097152,"states":["needs_you","queued","building","validating","in_review","ready"]}`
	const labels = `"labels":{"project":"director.project","rootWorkspace":"director.root-workspace","workspace":"director.workspace","executionWorkspace":"director.execution-workspace","task":"director.task","run":"director.run","role":"director.role","phase":"director.phase","candidate":"director.candidate","base":"director.base","effect":"director.effect","profile":"director.profile","session":"director.session","registeredAt":"director.registered-at","startedAt":"director.started-at"}`

	for name, registry := range map[string]string{
		"absent":             "",
		"wrong version":      `,"workerRegistry":{"schemaVersion":2,"roles":["task-agent","reviewer"],` + labels + `}`,
		"extra role":         `,"workerRegistry":{"schemaVersion":1,"roles":["task-agent","reviewer","helper"],` + labels + `}`,
		"reordered roles":    `,"workerRegistry":{"schemaVersion":1,"roles":["reviewer","task-agent"],` + labels + `}`,
		"renamed root label": `,"workerRegistry":{"schemaVersion":1,"roles":["task-agent","reviewer"],"labels":{"project":"director.project","rootWorkspace":"director.root","workspace":"director.workspace","executionWorkspace":"director.execution-workspace","task":"director.task","run":"director.run","role":"director.role","phase":"director.phase","candidate":"director.candidate","base":"director.base","effect":"director.effect","profile":"director.profile","session":"director.session","registeredAt":"director.registered-at","startedAt":"director.started-at"}}`,
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := ParseDefinition([]byte(prefix + registry + "}")); err == nil {
				t.Fatal("ParseDefinition() accepted a worker registry outside the contract")
			}
		})
	}

	definition, err := ParseDefinition([]byte(prefix + `,"workerRegistry":{"schemaVersion":1,"roles":["task-agent","reviewer"],` + labels + `}}`))
	if err != nil {
		t.Fatalf("ParseDefinition() error = %v", err)
	}
	if definition.WorkerRegistry.Labels.RootWorkspace != "director.root-workspace" {
		t.Fatalf("worker registry labels = %#v", definition.WorkerRegistry.Labels)
	}
}

func TestValidateObservationBindsEnvelopeResultAndHash(t *testing.T) {
	command := Command{
		RequestID: "request-1", IdempotencyKey: "effect-1", ExpectedVersion: 1,
		AfterCursor: 0,
		Capability:  CapabilityAgentObserve,
		Arguments: Arguments{
			Scope:      execution.Scope{ProjectID: "project-1", WorkspaceID: "workspace-1", TaskID: "task-1", RunID: "run-1"},
			EffectKind: execution.EffectAgentCreate, EffectID: "effect-1",
			BindingHash: strings.Repeat("1", 64),
		},
	}
	result := ObservationResult{
		EffectID: command.Arguments.EffectID, Status: execution.ObservationAbsent,
		BindingHash: command.Arguments.BindingHash, PriorDispatcherAbsent: true,
		MaximumAgeMillis: 30_000,
	}
	result.FactHash = ObservationResultHash(result)
	observation := Observation{
		RequestID: command.RequestID, Cursor: 1, ObservedAt: "1970-01-01T00:00:01Z",
		Result: result,
	}
	if err := ValidateObservation(command, observation); err != nil {
		t.Fatalf("valid observation: %v", err)
	}
	for name, mutate := range map[string]func(*Observation){
		"request": func(value *Observation) { value.RequestID = "other" },
		"cursor":  func(value *Observation) { value.Cursor = 0 },
		"resume":  func(value *Observation) { value.Cursor = command.AfterCursor },
		"binding": func(value *Observation) { value.Result.BindingHash = "other" },
		"hash":    func(value *Observation) { value.Result.FactHash = strings.Repeat("0", 64) },
	} {
		t.Run(name, func(t *testing.T) {
			changed := observation
			mutate(&changed)
			if err := ValidateObservation(command, changed); err == nil {
				t.Fatal("ValidateObservation accepted drift")
			}
		})
	}
}

func TestValidateObservationRequiresExactBoundedProviderUsage(t *testing.T) {
	command := Command{
		RequestID: "request-usage", IdempotencyKey: "effect-usage", ExpectedVersion: 1,
		Capability: CapabilityAgentObserve,
		Arguments: Arguments{
			Scope:      execution.Scope{ProjectID: "project-1", WorkspaceID: "workspace-1", TaskID: "task-1", RunID: "run-1"},
			EffectKind: execution.EffectAgentPrompt, EffectID: "effect-usage", BindingHash: strings.Repeat("1", 64),
		},
	}
	valid := Observation{
		RequestID: command.RequestID, Cursor: 1, ObservedAt: "1970-01-01T00:00:01Z",
		Result: ObservationResult{
			EffectID: command.Arguments.EffectID, Status: execution.ObservationDesired,
			BindingHash: command.Arguments.BindingHash, PriorDispatcherAbsent: true, MaximumAgeMillis: 30_000,
			Usage: &runtimebudget.ProviderUsage{
				State: runtimebudget.UsageCurrent, SourceRevision: "agent-updated-at-1",
				InputTokensPresent: true, InputTokens: 10, CachedInputTokens: 4,
				OutputTokensPresent: true, OutputTokens: 2, CostMicrousdPresent: true, CostMicrousd: 9,
			},
		},
	}
	valid.Result.FactHash = ObservationResultHash(valid.Result)
	if err := ValidateObservation(command, valid); err != nil {
		t.Fatal(err)
	}
	for name, mutate := range map[string]func(*runtimebudget.ProviderUsage){
		"missing source revision": func(value *runtimebudget.ProviderUsage) { value.SourceRevision = "" },
		"missing tokens":          func(value *runtimebudget.ProviderUsage) { value.InputTokensPresent = false },
		"transport overflow":      func(value *runtimebudget.ProviderUsage) { value.OutputTokens = 9_007_199_254_740_992 },
		"unknown state":           func(value *runtimebudget.ProviderUsage) { value.State = "future" },
	} {
		t.Run(name, func(t *testing.T) {
			changed := valid
			usage := *valid.Result.Usage
			mutate(&usage)
			changed.Result.Usage = &usage
			changed.Result.FactHash = ObservationResultHash(changed.Result)
			if err := ValidateObservation(command, changed); err == nil {
				t.Fatal("invalid provider usage was accepted")
			}
		})
	}
}

func TestValidateObservationRequiresClosedRecoveryInventory(t *testing.T) {
	command := Command{
		RequestID: "request-recovery", IdempotencyKey: "effect-recovery", ExpectedVersion: 1,
		Capability: CapabilityAgentObserve,
		Arguments: Arguments{
			Scope:      execution.Scope{ProjectID: "project-1", WorkspaceID: "workspace-1", TaskID: "task-1", RunID: "run-1"},
			EffectKind: execution.EffectPrimaryRecoveryObserve, EffectID: "effect-recovery",
			BindingHash: strings.Repeat("1", 64),
		},
	}
	inventory := execution.PrimaryRecoveryInventory{
		Complete: true,
		Workspaces: []execution.NativeWorkspaceRecoveryFact{{
			WorkspaceID: "workspace-native", Active: true, WorktreeExact: true, TitleExact: true, KindExact: true,
		}},
		Agents: []execution.NativeAgentRecoveryFact{{
			AgentID: "agent-1", WorkspaceID: "workspace-native", Role: execution.ControlledTaskAgent,
			EffectID: "agent-effect", Status: "error", TitleExact: true, WorktreeExact: true,
			LabelsRunExact: true, ProfileExact: true, SessionExact: true, BootstrapPresent: true,
			PromptPresent: true, PersistenceReferencePresent: true,
			FailureSignals: []execution.ProviderFailureSignal{execution.ProviderFailurePolicy},
		}},
	}
	valid := Observation{
		RequestID: command.RequestID, Cursor: 1, ObservedAt: "1970-01-01T00:00:01Z",
		Result: ObservationResult{
			EffectID: command.Arguments.EffectID, Status: execution.ObservationDesired,
			BindingHash: command.Arguments.BindingHash, PriorDispatcherAbsent: true,
			MaximumAgeMillis: 30_000, Inventory: &inventory,
		},
	}
	valid.Result.FactHash = ObservationResultHash(valid.Result)
	if err := ValidateObservation(command, valid); err != nil {
		t.Fatal(err)
	}
	for name, mutate := range map[string]func(*execution.PrimaryRecoveryInventory){
		"incomplete": func(value *execution.PrimaryRecoveryInventory) { value.Complete = false },
		"unknown failure": func(value *execution.PrimaryRecoveryInventory) {
			value.Agents[0].FailureSignals[0] = "raw-provider-output"
		},
		"duplicate agent": func(value *execution.PrimaryRecoveryInventory) {
			value.Agents = append(value.Agents, value.Agents[0])
		},
	} {
		t.Run(name, func(t *testing.T) {
			changed := valid
			cloned := inventory
			cloned.Workspaces = append([]execution.NativeWorkspaceRecoveryFact(nil), inventory.Workspaces...)
			cloned.Agents = append([]execution.NativeAgentRecoveryFact(nil), inventory.Agents...)
			cloned.Agents[0].FailureSignals = append([]execution.ProviderFailureSignal(nil), inventory.Agents[0].FailureSignals...)
			mutate(&cloned)
			changed.Result.Inventory = &cloned
			changed.Result.FactHash = ObservationResultHash(changed.Result)
			if err := ValidateObservation(command, changed); err == nil {
				t.Fatal("invalid recovery inventory was accepted")
			}
		})
	}
}
