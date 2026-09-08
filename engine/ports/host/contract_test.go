// SPDX-License-Identifier: Apache-2.0

package host

import (
	"bytes"
	"strings"
	"testing"

	"github.com/mcuadros/director-engine/domain/execution"
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
