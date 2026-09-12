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
	"strings"

	"github.com/mcuadros/director-engine/domain/execution"
	"github.com/mcuadros/director-engine/domain/jsondocument"
	"github.com/mcuadros/director-engine/domain/runtimebudget"
)

//go:embed host-interface.v1.json
var embeddedSchema []byte

// Definition is the generator-facing portion of the engine-owned host schema.
type Definition struct {
	SchemaVersion   int                      `json:"schemaVersion"`
	ContractVersion string                   `json:"contractVersion"`
	CredentialScope string                   `json:"credentialScope"`
	Capabilities    []string                 `json:"capabilities"`
	BoardQuery      BoardQueryDefinition     `json:"boardQuery"`
	WorkerRegistry  WorkerRegistryDefinition `json:"workerRegistry"`
	Command         CommandDefinition        `json:"command"`
	Observation     ObservationDefinition    `json:"observation"`
}

// CommandDefinition contains the schema-owned host effect vocabulary.
type CommandDefinition struct {
	Arguments CommandArgumentsDefinition `json:"arguments"`
}

// CommandArgumentsDefinition is the generated command-union metadata.
type CommandArgumentsDefinition struct {
	EffectKinds []string `json:"effectKinds"`
}

// ObservationDefinition contains the schema-owned normalized result metadata.
type ObservationDefinition struct {
	Result ObservationResultDefinition `json:"result"`
}

// ObservationResultDefinition is the generated observation-union metadata.
type ObservationResultDefinition struct {
	Statuses []string `json:"statuses"`
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

// WorkerRegistryDefinition is the engine-owned label vocabulary which makes
// parentless workers observable from their root workspace without joining
// through an execution-workspace card.
type WorkerRegistryDefinition struct {
	SchemaVersion int                  `json:"schemaVersion"`
	Roles         []string             `json:"roles"`
	Labels        WorkerRegistryLabels `json:"labels"`
}

// WorkerRegistryLabels names every fixed public Paseo label in the launch
// registry. The native agent ID and current workspace ID remain public agent
// facts and are not duplicated into caller-controlled labels.
type WorkerRegistryLabels struct {
	Project            string `json:"project"`
	RootWorkspace      string `json:"rootWorkspace"`
	Workspace          string `json:"workspace"`
	ExecutionWorkspace string `json:"executionWorkspace"`
	Task               string `json:"task"`
	Run                string `json:"run"`
	Role               string `json:"role"`
	Phase              string `json:"phase"`
	Candidate          string `json:"candidate"`
	Base               string `json:"base"`
	Effect             string `json:"effect"`
	Profile            string `json:"profile"`
	Session            string `json:"session"`
	RegisteredAt       string `json:"registeredAt"`
	StartedAt          string `json:"startedAt"`
}

func engineHostEffectKinds() []string {
	return []string{
		string(execution.EffectHostViewCreate),
		string(execution.EffectAgentCreate),
		string(execution.EffectReviewerAgentCreate),
		string(execution.EffectAgentPrompt),
		string(execution.EffectPrimaryRecoveryObserve),
		string(execution.EffectHelperAgentObserve),
		string(execution.EffectHelperAgentArchive),
		string(execution.EffectControlAgentBoundary),
		string(execution.EffectControlAgentArchive),
		string(execution.EffectAgentArchive),
		string(execution.EffectReviewerAgentArchive),
		string(execution.EffectHostViewArchive),
	}
}

func engineHostObservationStatuses() []string {
	return []string{
		string(execution.ObservationDesired),
		string(execution.ObservationAbsent),
		string(execution.ObservationOwnedPresent),
		string(execution.ObservationErrored),
		string(execution.ObservationPermission),
		string(execution.ObservationDifferent),
		string(execution.ObservationAmbiguous),
		string(execution.ObservationUnavailable),
	}
}

// ProviderOption is one already-authorized non-secret provider-native value.
type ProviderOption struct {
	Name  string `json:"name"`
	Value string `json:"value"`
}

// AgentProfile is the exact immutable m3.2 Worker selection transported to a
// host. A connector translates these fields to the public Paseo v0.7 request;
// it never chooses a replacement or broadens them.
type AgentProfile struct {
	Provider        string           `json:"provider"`
	Model           string           `json:"model"`
	Effort          string           `json:"effort"`
	Mode            string           `json:"mode"`
	PermissionMode  string           `json:"permissionMode"`
	ProviderOptions []ProviderOption `json:"providerOptions"`
	SHA256          string           `json:"sha256"`
}

// MCPServer is the credential-free stdio process descriptor authorized by
// the engine for this exact Run.
type MCPServer struct {
	Name    string            `json:"name"`
	Command string            `json:"command"`
	Args    []string          `json:"args"`
	Env     map[string]string `json:"env"`
}

// MCPSession is the m3.3 transport descriptor fixed to the primary session
// reservation. Scope selectors remain absent from model-facing tool inputs.
type MCPSession struct {
	ContractVersion string    `json:"contractVersion"`
	ContractHash    string    `json:"contractHash"`
	SessionSHA256   string    `json:"sessionSha256"`
	Role            string    `json:"role"`
	Provider        string    `json:"provider"`
	Model           string    `json:"model"`
	Tools           []string  `json:"tools"`
	Server          MCPServer `json:"server"`
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
	CapabilityTaskAgentCreate     Capability = "taskAgent.createWithBootstrap"
	CapabilityReviewerAgentCreate Capability = "reviewerAgent.createWithBootstrap"
	CapabilityAgentPrompt         Capability = "send_agent_prompt"
	CapabilityHelperAgentObserve  Capability = "helperAgent.observe"
	CapabilityAgentObserve        Capability = "agent.observe"
	CapabilityAgentArchive        Capability = "agent.archive"
)

// ZeroWorkBootstrapPrompt is the complete first turn for every Director-created
// parentless worker. It deliberately authorizes no Task or Review activity.
const ZeroWorkBootstrapPrompt = "Director bootstrap only. Do not inspect files, call tools, or perform Task or Review work. Finish this turn immediately."

// Arguments is the typed host command union. ParentAgentID is a pointer so a
// Director-launched Task Agent or Reviewer proves the field was omitted.
type Arguments struct {
	Scope                    execution.Scope      `json:"scope"`
	EffectKind               execution.EffectKind `json:"effectKind"`
	EffectID                 string               `json:"effectId"`
	WorktreeID               string               `json:"worktreeId,omitempty"`
	WorktreePath             string               `json:"worktreePath,omitempty"`
	WorkspaceID              string               `json:"workspaceId,omitempty"`
	AgentID                  string               `json:"agentId,omitempty"`
	Title                    string               `json:"title,omitempty"`
	InitialPrompt            string               `json:"initialPrompt,omitempty"`
	ParentAgentID            *string              `json:"parentAgentId,omitempty"`
	LifecycleDigest          string               `json:"lifecycleDigest,omitempty"`
	IsolationDigest          string               `json:"isolationDigest,omitempty"`
	PreparationReady         bool                 `json:"preparationReady,omitempty"`
	PreparationBarrierHash   string               `json:"preparationBarrierHash,omitempty"`
	NotifyOnFinish           bool                 `json:"notifyOnFinish,omitempty"`
	ClientMessageID          string               `json:"clientMessageId,omitempty"`
	BoundaryID               string               `json:"boundaryId,omitempty"`
	OperationalObservationID string               `json:"operationalObservationId,omitempty"`
	Profile                  *AgentProfile        `json:"profile,omitempty"`
	Session                  *MCPSession          `json:"session,omitempty"`
	SessionBindingSHA256     string               `json:"sessionBindingSha256,omitempty"`
	Labels                   map[string]string    `json:"labels,omitempty"`
	BindingHash              string               `json:"bindingHash"`
}

// Command is the exact typed request accepted by a host connector.
type Command struct {
	RequestID       string     `json:"requestId"`
	IdempotencyKey  string     `json:"idempotencyKey"`
	ExpectedVersion uint64     `json:"expectedVersion"`
	AfterCursor     uint64     `json:"afterCursor,omitempty"`
	Capability      Capability `json:"capability"`
	Arguments       Arguments  `json:"arguments"`
}

// ObservationResult is a normalized host fact. SDK receipts and model text
// are not represented as evidence.
type ObservationResult struct {
	EffectID              string                              `json:"effectId"`
	Status                execution.ObservationStatus         `json:"status"`
	ExternalID            string                              `json:"externalId,omitempty"`
	BindingHash           string                              `json:"bindingHash"`
	CorrelationHash       string                              `json:"correlationHash,omitempty"`
	PriorDispatcherAbsent bool                                `json:"priorDispatcherAbsent"`
	MaximumAgeMillis      int64                               `json:"maximumAgeMillis"`
	Usage                 *runtimebudget.ProviderUsage        `json:"usage,omitempty"`
	NativeAgent           *execution.NativeAgentRecoveryFact  `json:"nativeAgent,omitempty"`
	Inventory             *execution.PrimaryRecoveryInventory `json:"inventory,omitempty"`
	FactHash              string                              `json:"factHash"`
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
		observation.Cursor <= command.AfterCursor ||
		observation.ObservedAt == "" || observation.Result.EffectID != command.Arguments.EffectID ||
		observation.Result.BindingHash != command.Arguments.BindingHash ||
		observation.Result.MaximumAgeMillis <= 0 ||
		observation.Result.FactHash == "" ||
		observation.Result.FactHash != ObservationResultHash(observation.Result) {
		return errors.New("host observation envelope is invalid")
	}
	if observation.Result.CorrelationHash != "" &&
		(len(observation.Result.CorrelationHash) != 64 || strings.IndexFunc(observation.Result.CorrelationHash, func(character rune) bool {
			return !strings.ContainsRune("0123456789abcdef", character)
		}) >= 0) {
		return errors.New("host observation correlation is invalid")
	}
	if usage := observation.Result.Usage; usage != nil {
		const maximumJavaScriptSafeInteger = uint64(9_007_199_254_740_991)
		if usage.InputTokens > maximumJavaScriptSafeInteger || usage.CachedInputTokens > maximumJavaScriptSafeInteger ||
			usage.OutputTokens > maximumJavaScriptSafeInteger || usage.CostMicrousd > maximumJavaScriptSafeInteger {
			return errors.New("host usage observation exceeds the exact transport range")
		}
		switch usage.State {
		case runtimebudget.UsageCurrent:
			if !usage.InputTokensPresent || !usage.OutputTokensPresent || usage.SourceRevision == "" || len(usage.SourceRevision) > 128 {
				return errors.New("host usage observation is incomplete")
			}
		case runtimebudget.UsageUnavailable, runtimebudget.UsageAmbiguous:
			if usage.SourceRevision != "" || usage.InputTokensPresent || usage.InputTokens != 0 || usage.CachedInputTokens != 0 ||
				usage.OutputTokensPresent || usage.OutputTokens != 0 || usage.CostMicrousdPresent || usage.CostMicrousd != 0 {
				return errors.New("host unavailable usage observation carries values")
			}
		default:
			return errors.New("host usage observation state is invalid")
		}
	}
	if observation.Result.NativeAgent != nil && !execution.ValidNativeAgentRecoveryFact(*observation.Result.NativeAgent) {
		return errors.New("host native agent recovery fact is invalid")
	}
	if observation.Result.Inventory != nil && !execution.ValidPrimaryRecoveryInventory(*observation.Result.Inventory) {
		return errors.New("host primary recovery inventory is invalid")
	}
	switch observation.Result.Status {
	case execution.ObservationDesired, execution.ObservationAbsent,
		execution.ObservationOwnedPresent, execution.ObservationDifferent,
		execution.ObservationAmbiguous, execution.ObservationUnavailable,
		execution.ObservationErrored, execution.ObservationPermission:
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
	if definition.WorkerRegistry.SchemaVersion != 1 ||
		!slices.Equal(definition.WorkerRegistry.Roles, []string{"task-agent", "reviewer"}) {
		return Definition{}, errors.New("host worker registry identity does not match")
	}
	expectedLabels := WorkerRegistryLabels{
		Project: "director.project", RootWorkspace: "director.root-workspace",
		Workspace: "director.workspace", ExecutionWorkspace: "director.execution-workspace",
		Task: "director.task", Run: "director.run", Role: "director.role",
		Phase: "director.phase", Candidate: "director.candidate", Base: "director.base",
		Effect: "director.effect", Profile: "director.profile", Session: "director.session",
		RegisteredAt: "director.registered-at", StartedAt: "director.started-at",
	}
	if definition.WorkerRegistry.Labels != expectedLabels {
		return Definition{}, errors.New("host worker registry labels do not match")
	}
	if !slices.Equal(definition.Command.Arguments.EffectKinds, engineHostEffectKinds()) {
		return Definition{}, errors.New("host effect kinds do not match the Go execution vocabulary")
	}
	if !slices.Equal(definition.Observation.Result.Statuses, engineHostObservationStatuses()) {
		return Definition{}, errors.New("host observation statuses do not match the Go execution vocabulary")
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
	definition.WorkerRegistry.Roles = slices.Clone(definition.WorkerRegistry.Roles)
	definition.Command.Arguments.EffectKinds = slices.Clone(definition.Command.Arguments.EffectKinds)
	definition.Observation.Result.Statuses = slices.Clone(definition.Observation.Result.Statuses)
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
