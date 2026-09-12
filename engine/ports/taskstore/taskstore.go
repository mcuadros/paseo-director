// SPDX-License-Identifier: Apache-2.0

// Package taskstore defines the typed persistence boundary owned by the
// Director Engine. The contract contains domain records only: callers cannot
// select a database, submit SQL, or obtain a raw backend connection.
package taskstore

import (
	"context"
	"errors"

	"github.com/mcuadros/director-engine/domain"
)

// SchemaVersion 2 adds immutable Candidate claim/manifest JSON. Existing
// version-one stores fail closed; M5 owns backup/restore migration tooling.
const SchemaVersion = 2

var (
	ErrNotFound             = errors.New("taskstore record not found")
	ErrAlreadyExists        = errors.New("taskstore identity already exists")
	ErrInvalidRecord        = errors.New("taskstore record is invalid")
	ErrReferentialIntegrity = errors.New("taskstore reference is invalid")
	ErrWorkspaceConflict    = errors.New("taskstore Workspace repository mapping conflicts")
	ErrIdempotencyConflict  = errors.New("IDEMPOTENCY_KEY_REUSED_WITH_DIFFERENT_PAYLOAD")
	ErrSchemaVersion        = errors.New("taskstore schema version mismatch")
	ErrUnhealthy            = errors.New("taskstore is unhealthy")
)

// HealthCode is a bounded, backend-independent diagnostic safe for durable
// health state and logs. It never contains raw driver or server output.
type HealthCode string

const (
	HealthControlConnectionUnavailable HealthCode = "CONTROL_CONNECTION_UNAVAILABLE"
	HealthWriterConnectionUnavailable  HealthCode = "WRITER_CONNECTION_UNAVAILABLE"
	HealthReadConnectionUnavailable    HealthCode = "READ_CONNECTION_UNAVAILABLE"
	HealthGlobalCommitModeSetFailed    HealthCode = "GLOBAL_COMMIT_MODE_SET_FAILED"
	HealthSessionCommitModeSetFailed   HealthCode = "SESSION_COMMIT_MODE_SET_FAILED"
	HealthCommitModeReadFailed         HealthCode = "COMMIT_MODE_READ_FAILED"
	HealthUnsafeSessionCommitMode      HealthCode = "UNSAFE_SESSION_COMMIT_MODE"
	HealthUnsafeGlobalCommitMode       HealthCode = "UNSAFE_GLOBAL_COMMIT_MODE"
	HealthDatabaseIdentityReadFailed   HealthCode = "DATABASE_IDENTITY_READ_FAILED"
	HealthDatabaseIdentityMismatch     HealthCode = "DATABASE_IDENTITY_MISMATCH"
	HealthBackendVersionMismatch       HealthCode = "BACKEND_VERSION_MISMATCH"
	HealthStoreIdentityReadFailed      HealthCode = "STORE_IDENTITY_READ_FAILED"
	HealthStoreIdentityMismatch        HealthCode = "STORE_IDENTITY_MISMATCH"
	HealthCommandReadFailed            HealthCode = "COMMAND_READ_FAILED"
	HealthQueryFailed                  HealthCode = "QUERY_FAILED"
	HealthScanFailed                   HealthCode = "SCAN_FAILED"
	HealthRowsFailed                   HealthCode = "ROWS_FAILED"
	HealthStoredRecordInvalid          HealthCode = "STORED_RECORD_INVALID"
	HealthTransactionBeginFailed       HealthCode = "TRANSACTION_BEGIN_FAILED"
	HealthWriteFailed                  HealthCode = "WRITE_FAILED"
	HealthRetryBudgetExhausted         HealthCode = "TRANSACTION_RETRY_BUDGET_EXHAUSTED"
)

// SchemaCode classifies a fail-closed schema problem without exposing SQL or
// server diagnostics.
type SchemaCode string

const (
	SchemaInspectionFailed      SchemaCode = "SCHEMA_INSPECTION_FAILED"
	SchemaTableSetMismatch      SchemaCode = "SCHEMA_TABLE_SET_MISMATCH"
	SchemaTriggerSetMismatch    SchemaCode = "SCHEMA_TRIGGER_SET_MISMATCH"
	SchemaGuardRowsMismatch     SchemaCode = "SCHEMA_GUARD_ROWS_MISMATCH"
	SchemaInstallFailed         SchemaCode = "SCHEMA_INSTALL_FAILED"
	SchemaIdentityInstallFailed SchemaCode = "STORE_IDENTITY_INSTALL_FAILED"
	SchemaVersionReadFailed     SchemaCode = "SCHEMA_VERSION_READ_FAILED"
	SchemaVersionMismatch       SchemaCode = "SCHEMA_VERSION_MISMATCH"
)

// ValidationCode classifies invalid caller input without retaining untrusted
// payload content in a port-facing error.
type ValidationCode string

const (
	ValidationJSONInvalid     ValidationCode = "JSON_INVALID"
	ValidationPayloadTooLarge ValidationCode = "PAYLOAD_TOO_LARGE"
)

// HealthError unwraps to ErrUnhealthy while exposing only a closed code.
type HealthError struct {
	Code HealthCode
}

func (failure *HealthError) Error() string {
	return "taskstore unhealthy: " + string(failure.Code)
}

func (failure *HealthError) Unwrap() error {
	return ErrUnhealthy
}

// SchemaError unwraps to ErrSchemaVersion while exposing only a closed code.
type SchemaError struct {
	Code SchemaCode
}

// ValidationError unwraps to ErrInvalidRecord and exposes only a closed code.
type ValidationError struct {
	Code ValidationCode
}

func (failure *ValidationError) Error() string {
	return "taskstore invalid record: " + string(failure.Code)
}

func (failure *ValidationError) Unwrap() error {
	return ErrInvalidRecord
}

func (failure *SchemaError) Error() string {
	return "taskstore schema failure: " + string(failure.Code)
}

func (failure *SchemaError) Unwrap() error {
	return ErrSchemaVersion
}

// Unhealthy constructs a redacted typed health failure.
func Unhealthy(code HealthCode) error {
	return &HealthError{Code: code}
}

// InvalidSchema constructs a redacted typed schema failure.
func InvalidSchema(code SchemaCode) error {
	return &SchemaError{Code: code}
}

// Invalid constructs a bounded redacted caller-validation failure.
func Invalid(code ValidationCode) error {
	return &ValidationError{Code: code}
}

// TaskStore persists the Director aggregate records. Every mutation carries
// an immutable Command request and Event; implementations atomically persist
// the aggregate change, command outcome, and event. Project, Workspace, Epic,
// Task, and Run records use optimistic versions. DependencyOverride,
// Candidate, Command, and Event records are append-only. Applied Event
// sequence allocation is serialized through commit, making strict
// AfterGlobalSequence resume safe across concurrent writers. The adapter
// structurally canonicalizes and bounds JSON, but this generic port cannot
// infer a payload's schema version or secret semantics. Owning typed ingresses
// must reject unsupported versions and secret-bearing values first; dir-m5.9
// owns complete cross-ingress secret-safety hardening before release.
type TaskStore interface {
	SchemaVersion(context.Context) (int, error)

	CreateProject(context.Context, domain.CommandRequest, domain.Project, []domain.Workspace, domain.Event) (domain.CommandResult, error)
	Project(context.Context, string) (domain.Project, error)
	Projects(context.Context) ([]domain.Project, error)
	UpdateProject(context.Context, domain.CommandRequest, domain.Project, domain.Event) (domain.CommandResult, error)
	ApplyProjectLease(context.Context, domain.CommandRequest, domain.ProjectLeaseMutation) (domain.CommandResult, error)
	RecordProjectLeaseObservation(context.Context, domain.CommandRequest, string, domain.ProjectLeaseObservationInput) (domain.CommandResult, error)
	EnableProjectLeaseDispatch(context.Context, domain.CommandRequest, string, string) (domain.CommandResult, error)

	CreateWorkspace(context.Context, domain.CommandRequest, domain.Workspace, domain.Event) (domain.CommandResult, error)
	Workspace(context.Context, string) (domain.Workspace, error)
	Workspaces(context.Context, string) ([]domain.Workspace, error)
	UpdateWorkspace(context.Context, domain.CommandRequest, domain.Workspace, domain.Event) (domain.CommandResult, error)

	CreateEpic(context.Context, domain.CommandRequest, domain.Epic, domain.Event) (domain.CommandResult, error)
	Epic(context.Context, string) (domain.Epic, error)
	Epics(context.Context, string) ([]domain.Epic, error)
	UpdateEpic(context.Context, domain.CommandRequest, domain.Epic, domain.Event) (domain.CommandResult, error)

	CreateTask(context.Context, domain.CommandRequest, domain.Task, domain.Event) (domain.CommandResult, error)
	Task(context.Context, string) (domain.Task, error)
	Tasks(context.Context, string) ([]domain.Task, error)
	UpdateTask(context.Context, domain.CommandRequest, domain.Task, domain.Event) (domain.CommandResult, error)
	GrantDependencyOverride(context.Context, domain.CommandRequest, domain.HumanDependencyOverrideGrant) (domain.CommandResult, error)
	DependencyOverride(context.Context, string) (domain.DependencyOverride, error)
	DependencyOverrides(context.Context, string) ([]domain.DependencyOverride, error)

	CreateRun(context.Context, domain.CommandRequest, domain.Run, domain.Event) (domain.CommandResult, error)
	Run(context.Context, string) (domain.Run, error)
	Runs(context.Context, string) ([]domain.Run, error)
	UpdateRun(context.Context, domain.CommandRequest, domain.Run, domain.Event) (domain.CommandResult, error)

	AppendCandidate(context.Context, domain.CommandRequest, domain.Candidate, domain.Event) (domain.CommandResult, error)
	Candidate(context.Context, string) (domain.Candidate, error)
	Candidates(context.Context, string) ([]domain.Candidate, error)

	Command(context.Context, string) (domain.Command, error)
	Events(context.Context, domain.EventQuery) ([]domain.Event, error)
	LatestEventSequence(context.Context) (uint64, error)
}
