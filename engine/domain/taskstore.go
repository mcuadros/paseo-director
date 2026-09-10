// SPDX-License-Identifier: Apache-2.0

package domain

import (
	"encoding/json"

	candidatedomain "github.com/mcuadros/director-engine/domain/candidate"
	"github.com/mcuadros/director-engine/domain/execution"
)

// OrganizerMode records how a Project's Organizer repository entered Director.
type OrganizerMode string

const (
	OrganizerModeCreate OrganizerMode = "create"
	OrganizerModeAdopt  OrganizerMode = "adopt"
)

// OrganizerPhase is the durable create/adopt progress projected through the
// TaskStore. Only PhaseActive may be used as an approved Project Organizer.
type OrganizerPhase string

const (
	OrganizerPhaseIntentRecorded        OrganizerPhase = "intent_recorded"
	OrganizerPhaseRepositoryPrepared    OrganizerPhase = "repository_prepared"
	OrganizerPhaseConfigurationWritten  OrganizerPhase = "configuration_written"
	OrganizerPhaseReadmeWritten         OrganizerPhase = "readme_written"
	OrganizerPhaseReferencesWritten     OrganizerPhase = "references_written"
	OrganizerPhaseRepositoryInitialized OrganizerPhase = "repository_initialized"
	OrganizerPhaseRevisionCommitted     OrganizerPhase = "revision_committed"
	OrganizerPhaseActive                OrganizerPhase = "active"
)

// Organizer is the durable repository/configuration projection owned by one
// Project. PendingConfiguration exists only while an approved Create is being
// recovered; active configuration remains canonical in Organizer Git.
type Organizer struct {
	ID                   string
	Mode                 OrganizerMode
	Phase                OrganizerPhase
	RepositoryPath       string
	PreviewID            string
	OperationID          string
	HumanActorID         string
	ConfigurationSHA256  string
	OrganizerRevision    string
	PendingConfiguration json.RawMessage
}

// Project is the mutable M2 Project aggregate. It owns exactly one Organizer,
// one optional daemon execution lease, its independently durable monotonic
// fencing epoch, and one or more separately persisted Workspaces. Version
// advances only through an expected-version TaskStore write.
type Project struct {
	ID               string
	Name             string
	State            string
	Version          uint64
	Organizer        *Organizer
	LastLeaseEpoch   uint64
	Lease            *ProjectLease
	LeaseObservation *ProjectLeaseObservation
	Control          execution.ProjectControl
}

// Task is one mutable planning unit. It belongs to exactly one Project,
// targets exactly one Workspace through WorkspaceIDs, and may be standalone
// or have one Epic parent. Dependencies and external references never replace
// its canonical ID. ProjectID and Key are immutable after creation.
type Task struct {
	ID                 string
	ProjectID          string
	Key                string
	Title              string
	Objective          string
	AcceptanceCriteria string
	WorkspaceIDs       []string
	Parent             *PlanningNodeRef
	Complete           bool
	Priority           Priority
	Labels             []string
	Dependencies       []PlanningDependency
	ExternalReferences []ExternalReference
	QueuedAtUnixMillis int64
	Attention          *execution.NeedsYou
	Version            uint64
}

// Run is the minimal mutable execution aggregate for one Task. Candidate
// appends advance Version and replace CurrentCandidateID without changing old
// Candidate records.
type Run struct {
	ID                 string
	TaskID             string
	Number             uint64
	BaseSHA            string
	CurrentCandidateID string
	Execution          execution.State
	Version            uint64
}

const CandidateSchemaVersion = "director.candidate/v1"

// Candidate is an immutable exact Git commit and its complete admitted claim
// and manifest. Later evidence may refer only to Manifest.BindingSHA256; it
// cannot reinterpret this record after Candidate, base, or context changes.
type Candidate struct {
	SchemaVersion    string                   `json:"schemaVersion"`
	ID               string                   `json:"id"`
	RunID            string                   `json:"runId"`
	Sequence         uint64                   `json:"sequence"`
	CommitSHA        string                   `json:"commitSha"`
	Claim            candidatedomain.Claim    `json:"claim"`
	Manifest         candidatedomain.Manifest `json:"manifest"`
	AdmittedAtMillis int64                    `json:"admittedAtMillis"`
}

// Command is the immutable request and durable outcome stored for one
// idempotency key. PayloadHash covers every immutable request field, not only
// Payload.
type Command struct {
	IdempotencyKey  string
	Type            string
	AggregateID     string
	ExpectedVersion uint64
	Payload         json.RawMessage
	PayloadHash     string
	Outcome         CommandOutcome
	ObservedVersion uint64
	EventID         string
}

// CommandRequest is the immutable part of a Command supplied to TaskStore.
type CommandRequest struct {
	IdempotencyKey  string
	Type            string
	AggregateID     string
	ExpectedVersion uint64
	Payload         json.RawMessage
}

// CommandOutcome is a closed persistence-level result. Rejected version
// conflicts are durable and replayable just like applied commands.
type CommandOutcome string

const (
	CommandApplied                 CommandOutcome = "applied"
	CommandRejectedVersionConflict CommandOutcome = "rejected_version_conflict"
)

// CommandResult is returned for both first execution and idempotent replay.
type CommandResult struct {
	Outcome         CommandOutcome
	ObservedVersion uint64
	EventID         string
	Replay          bool
}

// Event is an immutable ordered domain fact. RunID may be empty for Project
// or Task bootstrap events which occur before a Run exists.
type Event struct {
	GlobalSequence   uint64
	ID               string
	RunID            string
	Sequence         uint64
	AggregateID      string
	AggregateVersion uint64
	Type             string
	Payload          json.RawMessage
}

// EventQuery is a bounded query over the append-only event stream. Empty IDs
// are wildcards and AfterGlobalSequence is exclusive. TaskStore assigns
// GlobalSequence under a transactional stream lock, so committed Events never
// appear below a cursor which has already been returned to a consumer.
type EventQuery struct {
	RunID               string
	AggregateID         string
	AfterGlobalSequence uint64
	Limit               uint32
}
