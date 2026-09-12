// SPDX-License-Identifier: Apache-2.0

// Package home defines the policy-free operational facts consumed by the
// engine-owned Director Home projection.
package home

import "context"

type SyncStreamState string

const (
	SyncCurrent       SyncStreamState = "current"
	SyncFailed        SyncStreamState = "failed"
	SyncNotConfigured SyncStreamState = "not_configured"
	SyncUnavailable   SyncStreamState = "unavailable"
	SyncStale         SyncStreamState = "stale"
)

type WorkspaceObservation struct {
	Health           string
	PaseoWorkspaceID string
}

type PreflightCapability string

const (
	CapabilityPaseoRuntime       PreflightCapability = "paseo_runtime"
	CapabilityConnectorContract  PreflightCapability = "connector_contract"
	CapabilityProviderCodex      PreflightCapability = "provider_codex"
	CapabilityProviderClaude     PreflightCapability = "provider_claude_code"
	CapabilityProviderOpenCode   PreflightCapability = "provider_opencode"
	CapabilityProviderAuth       PreflightCapability = "provider_authentication"
	CapabilitySessionMCP         PreflightCapability = "session_stdio_mcp"
	CapabilityExactMCPPolicy     PreflightCapability = "exact_mcp_tool_policy"
	CapabilityRootlessOCI        PreflightCapability = "rootless_oci"
	CapabilityRepositoryIdentity PreflightCapability = "repository_identity"
	CapabilityResourceLimits     PreflightCapability = "finite_resource_observations"
)

type PreflightState string

const (
	PreflightCurrent     PreflightState = "current"
	PreflightMissing     PreflightState = "missing"
	PreflightMismatch    PreflightState = "mismatch"
	PreflightUnavailable PreflightState = "unavailable"
	PreflightStale       PreflightState = "stale"
)

// PreflightObservation carries only closed capability/state facts. Human
// guidance and blocking semantics are engine-owned and cannot be supplied by
// a host adapter.
type PreflightObservation struct {
	Capability PreflightCapability
	State      PreflightState
}

type RepairCapabilities struct {
	GitSync           bool
	DynamicState      bool
	Lease             bool
	WorkspaceRecovery bool
}

type OperationAvailability struct {
	Sync      bool
	Reconcile bool
	Doctor    bool
	Repair    bool
	Control   bool
}

type ProjectObservation struct {
	GitSync              SyncStreamState
	TaskStoreSync        SyncStreamState
	SyncObservedAtMillis int64
	SyncMaximumAgeMillis int64
	OrganizerWorkspaceID string
	BoardWorkspaceID     string
	Workspaces           map[string]WorkspaceObservation
	Operations           OperationAvailability
	Preflight            []PreflightObservation
	RepairCapabilities   RepairCapabilities
}

type RepairExecution struct {
	RequestID      string
	PreviewID      string
	HostID         string
	HostInstanceID string
	ProjectID      string
	ProjectVersion uint64
	Cursor         uint64
	ObservationID  string
	OperationIDs   []string
}

type RepairEvidence struct {
	ProjectVersion uint64
	Cursor         uint64
	OperationIDs   []string
}

// RepairExecutor performs one exact engine-authorized plan. Implementations
// must use RequestID as the idempotency identity and return the first durable
// outcome for an exact replay.
type RepairExecutor interface {
	ObserveRepair(context.Context, string) (RepairExecution, RepairEvidence, bool, error)
	ApplyRepair(context.Context, RepairExecution) (RepairEvidence, error)
}

type HostObservation struct {
	HostID           string
	Label            string
	InstanceID       string
	State            string
	ObservedAtMillis int64
	MaximumAgeMillis int64
	Projects         map[string]ProjectObservation
}

// OperationalSource returns one observation for the requested exact host. It
// must never reinterpret a Project ID as a cross-host identity.
type OperationalSource interface {
	Observe(context.Context, string, []string) (HostObservation, error)
}
