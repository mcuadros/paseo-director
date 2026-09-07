// SPDX-License-Identifier: Apache-2.0

package domain

import "encoding/json"

// Project is the minimal mutable Project aggregate persisted by the walking
// skeleton. Version is advanced only by an expected-version TaskStore write.
type Project struct {
	ID      string
	Name    string
	State   string
	Version uint64
}

// Task is the minimal mutable Task aggregate persisted by the walking
// skeleton. ProjectID is immutable after creation.
type Task struct {
	ID                 string
	ProjectID          string
	Title              string
	Objective          string
	AcceptanceCriteria string
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
	Version            uint64
}

// Candidate is an immutable exact Git commit produced by a Run.
type Candidate struct {
	ID        string
	RunID     string
	Sequence  uint64
	CommitSHA string
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
