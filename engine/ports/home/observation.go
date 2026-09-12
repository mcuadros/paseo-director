// SPDX-License-Identifier: Apache-2.0

// Package home defines the policy-free operational facts consumed by the
// engine-owned Director Home projection.
package home

import "context"

type SyncStreamState string

const (
	SyncCurrent          SyncStreamState = "current"
	SyncLocalAhead       SyncStreamState = "local_ahead"
	SyncRemoteAhead      SyncStreamState = "remote_ahead"
	SyncDiverged         SyncStreamState = "diverged"
	SyncFailed           SyncStreamState = "failed"
	SyncIdentityMismatch SyncStreamState = "identity_mismatch"
	SyncNotConfigured    SyncStreamState = "not_configured"
	SyncUnavailable      SyncStreamState = "unavailable"
	SyncStale            SyncStreamState = "stale"
)

// SyncReason is a closed, path/content-free classification produced by an
// adapter. Human-facing wording and Project health remain engine-owned.
type SyncReason string

const (
	SyncReasonAligned                   SyncReason = "aligned"
	SyncReasonLocalAhead                SyncReason = "local_ahead"
	SyncReasonRemoteAhead               SyncReason = "remote_ahead"
	SyncReasonDiverged                  SyncReason = "diverged"
	SyncReasonOperationFailed           SyncReason = "operation_failed"
	SyncReasonIdentityMismatch          SyncReason = "identity_mismatch"
	SyncReasonAuthenticationUnavailable SyncReason = "authentication_unavailable"
	SyncReasonRemoteUnavailable         SyncReason = "remote_unavailable"
	SyncReasonObservationStale          SyncReason = "observation_stale"
	SyncReasonNotConfigured             SyncReason = "not_configured"
)

// SyncStreamObservation contains only normalized exact-head fingerprints and
// closed state. It cannot transport a ref, path, remote, command output, or
// credential. Fingerprints are lowercase SHA-256 values when present.
type SyncStreamObservation struct {
	State                     SyncStreamState
	Reason                    SyncReason
	LocalRevisionFingerprint  string
	RemoteRevisionFingerprint string
	ObservedAtMillis          int64
	LastSuccessAtMillis       int64
	Retryable                 bool
}

type ReconciliationState string

const (
	ReconciliationCurrent     ReconciliationState = "current"
	ReconciliationRunning     ReconciliationState = "running"
	ReconciliationWaiting     ReconciliationState = "waiting_external"
	ReconciliationDegraded    ReconciliationState = "degraded"
	ReconciliationUnavailable ReconciliationState = "unavailable"
	ReconciliationStale       ReconciliationState = "stale"
)

type ReconciliationReason string

const (
	ReconciliationReasonObserved       ReconciliationReason = "observed"
	ReconciliationReasonEventWake      ReconciliationReason = "event_wake_pending"
	ReconciliationReasonExternalWait   ReconciliationReason = "external_wait"
	ReconciliationReasonFactMismatch   ReconciliationReason = "fact_mismatch"
	ReconciliationReasonSourceOffline  ReconciliationReason = "source_offline"
	ReconciliationReasonObservationOld ReconciliationReason = "observation_stale"
)

// ReconciliationObservation reports hybrid event/periodic control state. The
// cadence itself is fixed by Director Engine and is never adapter policy.
type ReconciliationObservation struct {
	State                 ReconciliationState
	Reason                ReconciliationReason
	ObservedAtMillis      int64
	LastCompletedAtMillis int64
	PendingWakeups        uint64
}

type TechnicalLogLevel string

const (
	TechnicalLogInfo    TechnicalLogLevel = "info"
	TechnicalLogWarning TechnicalLogLevel = "warning"
	TechnicalLogError   TechnicalLogLevel = "error"
)

type TechnicalLogComponent string

const (
	TechnicalLogEngine         TechnicalLogComponent = "engine"
	TechnicalLogTaskStore      TechnicalLogComponent = "dynamic_state"
	TechnicalLogGit            TechnicalLogComponent = "git"
	TechnicalLogDolt           TechnicalLogComponent = "dolt"
	TechnicalLogReconciliation TechnicalLogComponent = "coordination"
	TechnicalLogSecurity       TechnicalLogComponent = "security"
	TechnicalLogSupport        TechnicalLogComponent = "support"
)

type TechnicalLogCode string

const (
	TechnicalLogEngineStarted           TechnicalLogCode = "engine_started"
	TechnicalLogReconciliationCompleted TechnicalLogCode = "fact_scan_completed"
	TechnicalLogReconciliationWaiting   TechnicalLogCode = "fact_scan_waiting_external"
	TechnicalLogGitSyncCurrent          TechnicalLogCode = "git_sync_current"
	TechnicalLogGitSyncFailed           TechnicalLogCode = "git_sync_failed"
	TechnicalLogDoltSyncCurrent         TechnicalLogCode = "dolt_sync_current"
	TechnicalLogDoltSyncFailed          TechnicalLogCode = "dolt_sync_failed"
	TechnicalLogTaskStoreUnhealthy      TechnicalLogCode = "taskstore_unhealthy"
	TechnicalLogSupportBundleGenerated  TechnicalLogCode = "support_bundle_generated"
	TechnicalLogUnsafeOutputSuppressed  TechnicalLogCode = "unsafe_output_suppressed"
)

// TechnicalLogObservation deliberately has no arbitrary message or fields.
// Adapters can emit only a closed code and bounded occurrence count.
type TechnicalLogObservation struct {
	Sequence         uint64
	OccurredAtMillis int64
	Level            TechnicalLogLevel
	Component        TechnicalLogComponent
	Code             TechnicalLogCode
	Occurrences      uint64
}

type TechnicalLogSource interface {
	ReadTechnicalLogs(context.Context, string, int64) ([]TechnicalLogObservation, error)
}

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
	GitSync                SyncStreamState
	TaskStoreSync          SyncStreamState
	GitSyncDetail          SyncStreamObservation
	TaskStoreSyncDetail    SyncStreamObservation
	SyncObservedAtMillis   int64
	SyncMaximumAgeMillis   int64
	AutomaticSyncEnabled   bool
	Reconciliation         ReconciliationObservation
	TechnicalLogs          []TechnicalLogObservation
	TechnicalLogsAvailable bool
	OrganizerWorkspaceID   string
	BoardWorkspaceID       string
	Workspaces             map[string]WorkspaceObservation
	Operations             OperationAvailability
	Preflight              []PreflightObservation
	RepairCapabilities     RepairCapabilities
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

type ManualOperation struct {
	RequestID      string
	Kind           string
	HostID         string
	HostInstanceID string
	ProjectID      string
	ProjectVersion uint64
	Cursor         uint64
	ObservationID  string
}

type ManualOperationEvidence struct {
	RequestID             string
	Kind                  string
	HostID                string
	HostInstanceID        string
	ProjectID             string
	ProjectVersion        uint64
	Cursor                uint64
	ObservationID         string
	GitState              SyncStreamState
	TaskStoreState        SyncStreamState
	SuccessfulHalfRetried bool
	CompletedAtMillis     int64
}

// ManualOperationExecutor applies only an already admitted exact Sync-now or
// Reconcile-now effect. It owns no retry, routing, or health policy.
type ManualOperationExecutor interface {
	ObserveManualOperation(context.Context, string) (ManualOperation, ManualOperationEvidence, bool, error)
	ApplyManualOperation(context.Context, ManualOperation) (ManualOperationEvidence, error)
}

type SupportBundleWrite struct {
	BundleID          string
	FileName          string
	GeneratedAtMillis int64
	Entries           map[string][]byte
}

type SupportBundleEvidence struct {
	BundleID          string
	FileName          string
	SHA256            string
	Bytes             uint64
	Permission        string
	GeneratedAtMillis int64
	UploadAttempted   bool
}

// SupportBundleWriter is a local filesystem sink. It has no upload method and
// must adopt an exact previously written bundle after response loss.
type SupportBundleWriter interface {
	ObserveSupportBundle(context.Context, string, string) (SupportBundleEvidence, bool, error)
	WriteSupportBundle(context.Context, SupportBundleWrite) (SupportBundleEvidence, error)
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
