// SPDX-License-Identifier: Apache-2.0

// Package host owns the single versioned interface implemented by host connectors.
package host

import (
	"context"
	"crypto/sha256"
	_ "embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"slices"

	"github.com/mcuadros/director-engine/domain/execution"
	"github.com/mcuadros/director-engine/domain/jsondocument"
)

//go:embed host-interface.v1.json
var embeddedSchema []byte

// Definition is the generator-facing portion of the engine-owned host schema.
type Definition struct {
	SchemaVersion   int                  `json:"schemaVersion"`
	ContractVersion string               `json:"contractVersion"`
	CredentialScope string               `json:"credentialScope"`
	Capabilities    []string             `json:"capabilities"`
	BoardQuery      BoardQueryDefinition `json:"boardQuery"`
}

// BoardQueryDefinition is the transport metadata for the engine-computed
// walking-skeleton Board snapshot. It adds no Paseo or connector policy.
type BoardQueryDefinition struct {
	Name          string   `json:"name"`
	Method        string   `json:"method"`
	Path          string   `json:"path"`
	SchemaVersion int      `json:"schemaVersion"`
	MaximumTasks  int      `json:"maximumTasks"`
	MaximumBytes  int      `json:"maximumBytes"`
	States        []string `json:"states"`
}

// Descriptor is the complete information a connector may advertise at handshake.
type Descriptor struct {
	CredentialScope string   `json:"credentialScope"`
	ContractVersion string   `json:"contractVersion"`
	ContractHash    string   `json:"contractHash"`
	Capabilities    []string `json:"capabilities"`
}

// Capability is one exact operation in the engine-owned host contract.
type Capability string

const (
	CapabilityWorkspaceCreate     Capability = "executionWorkspace.createManaged"
	CapabilityWorkspaceObserve    Capability = "executionWorkspace.observe"
	CapabilityWorkspaceArchive    Capability = "executionWorkspace.archive"
	CapabilityTaskAgentCreate     Capability = "taskAgent.createWithInitialPrompt"
	CapabilityReviewerAgentCreate Capability = "reviewerAgent.createWithInitialPrompt"
	CapabilityHelperAgentObserve  Capability = "helperAgent.observe"
	CapabilityAgentObserve        Capability = "agent.observe"
	CapabilityAgentArchive        Capability = "agent.archive"
)

// Arguments is the typed M1 subset of the host command union. ParentAgentID is
// a pointer so a top-level Task Agent request proves the field was omitted.
type Arguments struct {
	Scope                  execution.Scope      `json:"scope"`
	EffectKind             execution.EffectKind `json:"effectKind"`
	EffectID               string               `json:"effectId"`
	WorktreeID             string               `json:"worktreeId,omitempty"`
	WorktreePath           string               `json:"worktreePath,omitempty"`
	WorkspaceID            string               `json:"workspaceId,omitempty"`
	AgentID                string               `json:"agentId,omitempty"`
	Title                  string               `json:"title,omitempty"`
	InitialPrompt          string               `json:"initialPrompt,omitempty"`
	ParentAgentID          *string              `json:"parentAgentId,omitempty"`
	LifecycleDigest        string               `json:"lifecycleDigest,omitempty"`
	IsolationDigest        string               `json:"isolationDigest,omitempty"`
	PreparationReady       bool                 `json:"preparationReady,omitempty"`
	PreparationBarrierHash string               `json:"preparationBarrierHash,omitempty"`
	BindingHash            string               `json:"bindingHash"`
}

// Command is the exact typed request accepted by a host connector.
type Command struct {
	RequestID       string     `json:"requestId"`
	IdempotencyKey  string     `json:"idempotencyKey"`
	ExpectedVersion uint64     `json:"expectedVersion"`
	Capability      Capability `json:"capability"`
	Arguments       Arguments  `json:"arguments"`
}

// ObservationResult is a normalized host fact. SDK receipts and model text
// are not represented as evidence.
type ObservationResult struct {
	EffectID              string                      `json:"effectId"`
	Status                execution.ObservationStatus `json:"status"`
	ExternalID            string                      `json:"externalId,omitempty"`
	BindingHash           string                      `json:"bindingHash"`
	PriorDispatcherAbsent bool                        `json:"priorDispatcherAbsent"`
	MaximumAgeMillis      int64                       `json:"maximumAgeMillis"`
	FactHash              string                      `json:"factHash"`
}

// Observation is the resumable typed transport envelope returned by the host.
type Observation struct {
	RequestID  string            `json:"requestId"`
	Cursor     uint64            `json:"cursor"`
	ObservedAt string            `json:"observedAt"`
	Result     ObservationResult `json:"result"`
}

// Port is the one engine-owned host interface. Director for Paseo implements
// it without owning launch, retry, routing, or cleanup policy.
type Port interface {
	Describe(context.Context) (Descriptor, error)
	Invoke(context.Context, Command) (Observation, error)
}

// ObservationResultHash binds every normalized result field while excluding
// the self-hash field.
func ObservationResultHash(result ObservationResult) string {
	result.FactHash = ""
	encoded, err := json.Marshal(result)
	if err != nil {
		panic("marshal fixed host observation result: " + err.Error())
	}
	digest := sha256.Sum256(encoded)
	return hex.EncodeToString(digest[:])
}

// ValidateObservation rejects a misbound or self-inconsistent host envelope.
func ValidateObservation(command Command, observation Observation) error {
	if observation.RequestID != command.RequestID || observation.Cursor == 0 ||
		observation.ObservedAt == "" || observation.Result.EffectID != command.Arguments.EffectID ||
		observation.Result.BindingHash != command.Arguments.BindingHash ||
		observation.Result.MaximumAgeMillis <= 0 ||
		observation.Result.FactHash == "" ||
		observation.Result.FactHash != ObservationResultHash(observation.Result) {
		return errors.New("host observation envelope is invalid")
	}
	switch observation.Result.Status {
	case execution.ObservationDesired, execution.ObservationAbsent,
		execution.ObservationOwnedPresent, execution.ObservationDifferent,
		execution.ObservationAmbiguous, execution.ObservationUnavailable:
		return nil
	default:
		return errors.New("host observation status is invalid")
	}
}

// CanonicalJSON parses a contract document, rejects duplicate keys, and emits
// a whitespace- and object-key-order-independent representation.
func CanonicalJSON(schema []byte) ([]byte, error) {
	canonical, err := jsondocument.Canonical(schema)
	if err != nil {
		return nil, fmt.Errorf("canonicalize host contract: %w", err)
	}
	return canonical, nil
}

// CanonicalSHA256 hashes the parsed, duplicate-safe canonical contract.
func CanonicalSHA256(schema []byte) (string, error) {
	canonical, err := CanonicalJSON(schema)
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256(canonical)
	return hex.EncodeToString(digest[:]), nil
}

// ParseDefinition validates the closed metadata needed by the engine and generators.
func ParseDefinition(schema []byte) (Definition, error) {
	canonical, err := CanonicalJSON(schema)
	if err != nil {
		return Definition{}, err
	}
	var definition Definition
	if err := json.Unmarshal(canonical, &definition); err != nil {
		return Definition{}, fmt.Errorf("decode host contract: %w", err)
	}
	if definition.SchemaVersion != 1 {
		return Definition{}, fmt.Errorf("unsupported host schema version %d", definition.SchemaVersion)
	}
	if definition.ContractVersion == "" || definition.CredentialScope == "" {
		return Definition{}, errors.New("host contract version and credential scope are required")
	}
	if len(definition.Capabilities) == 0 {
		return Definition{}, errors.New("host contract must declare capabilities")
	}
	seen := make(map[string]struct{}, len(definition.Capabilities))
	for _, capability := range definition.Capabilities {
		if capability == "" {
			return Definition{}, errors.New("host capability cannot be empty")
		}
		if _, exists := seen[capability]; exists {
			return Definition{}, fmt.Errorf("duplicate host capability %q", capability)
		}
		seen[capability] = struct{}{}
	}
	if definition.BoardQuery.Name != "board.snapshot" ||
		definition.BoardQuery.Method != "GET" ||
		definition.BoardQuery.Path != "/v1/board" ||
		definition.BoardQuery.SchemaVersion != 1 ||
		definition.BoardQuery.MaximumTasks != 1000 ||
		definition.BoardQuery.MaximumBytes != 2*1024*1024 {
		return Definition{}, errors.New("host Board query contract does not match")
	}
	if len(definition.BoardQuery.States) != 6 {
		return Definition{}, errors.New("host Board query states do not match")
	}
	seenStates := make(map[string]struct{}, len(definition.BoardQuery.States))
	for _, state := range definition.BoardQuery.States {
		if state == "" {
			return Definition{}, errors.New("host Board query state cannot be empty")
		}
		if _, exists := seenStates[state]; exists {
			return Definition{}, fmt.Errorf("duplicate host Board query state %q", state)
		}
		seenStates[state] = struct{}{}
	}
	return definition, nil
}

// EmbeddedDefinition returns a defensive copy of the embedded contract metadata.
func EmbeddedDefinition() (Definition, error) {
	definition, err := ParseDefinition(embeddedSchema)
	if err != nil {
		return Definition{}, err
	}
	definition.Capabilities = slices.Clone(definition.Capabilities)
	definition.BoardQuery.States = slices.Clone(definition.BoardQuery.States)
	return definition, nil
}

// SchemaSHA256 returns the canonical lowercase SHA-256 of the embedded schema.
func SchemaSHA256() (string, error) {
	return CanonicalSHA256(embeddedSchema)
}

// ExpectedDescriptor builds the only descriptor accepted by this contract version.
func ExpectedDescriptor() (Descriptor, error) {
	definition, err := EmbeddedDefinition()
	if err != nil {
		return Descriptor{}, err
	}
	contractHash, err := SchemaSHA256()
	if err != nil {
		return Descriptor{}, err
	}
	return Descriptor{
		CredentialScope: definition.CredentialScope,
		ContractVersion: definition.ContractVersion,
		ContractHash:    contractHash,
		Capabilities:    slices.Clone(definition.Capabilities),
	}, nil
}

// ValidateDescriptor fails closed on a missing, stale, extra, or reordered field.
func ValidateDescriptor(actual Descriptor) error {
	expected, err := ExpectedDescriptor()
	if err != nil {
		return err
	}
	if actual.CredentialScope != expected.CredentialScope {
		return errors.New("host credential scope mismatch")
	}
	if actual.ContractVersion != expected.ContractVersion {
		return errors.New("host contract version mismatch")
	}
	if actual.ContractHash != expected.ContractHash {
		return errors.New("host contract hash mismatch")
	}
	if !slices.Equal(actual.Capabilities, expected.Capabilities) {
		return errors.New("host capability set mismatch")
	}
	return nil
}
