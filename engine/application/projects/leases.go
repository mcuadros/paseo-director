// SPDX-License-Identifier: Apache-2.0

// Package projects owns Project and Workspace application transitions. Host
// connectors transport commands and render projections; they do not evaluate
// lease or repository policy.
package projects

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"

	"github.com/mcuadros/director-engine/domain"
	processport "github.com/mcuadros/director-engine/ports/process"
	storeport "github.com/mcuadros/director-engine/ports/taskstore"
)

type leaseStore interface {
	Project(context.Context, string) (domain.Project, error)
	Command(context.Context, string) (domain.Command, error)
	ApplyProjectLease(context.Context, domain.CommandRequest, domain.ProjectLeaseMutation) (domain.CommandResult, error)
	RecordProjectLeaseObservation(context.Context, domain.CommandRequest, string, domain.ProjectLeaseObservationInput) (domain.CommandResult, error)
	EnableProjectLeaseDispatch(context.Context, domain.CommandRequest, string, string) (domain.CommandResult, error)
}

type LeaseCommandKind = domain.ProjectLeaseMutationKind

const (
	LeaseAcquire  = domain.ProjectLeaseAcquire
	LeaseRenew    = domain.ProjectLeaseRenew
	LeaseTakeover = domain.ProjectLeaseTakeover
)

// LeaseCommand contains no clock or process-authority assertion. Duration is
// a bounded policy input; the TaskStore transaction supplies current time.
type LeaseCommand struct {
	Kind                   LeaseCommandKind
	RequestID              string
	ProjectID              string
	ExpectedProjectVersion uint64
	ExpectedLeaseEpoch     uint64
	HolderInstance         string
	HolderProcessIdentity  string
	DurationMillis         int64
}

// ObserveTakeoverCommand requests one exact authorized process observation.
type ObserveTakeoverCommand struct {
	RequestID              string
	ProjectID              string
	ExpectedProjectVersion uint64
}

// EnableDispatchCommand names only a previously persisted observation.
type EnableDispatchCommand struct {
	RequestID              string
	ProjectID              string
	ExpectedProjectVersion uint64
	ObservationID          string
}

// ReleaseLeaseCommand clears only the exact current holder and epoch.
type ReleaseLeaseCommand struct {
	RequestID              string
	ProjectID              string
	ExpectedProjectVersion uint64
	ExpectedLeaseEpoch     uint64
	HolderInstance         string
	HolderProcessIdentity  string
}

// LeaseResult reports only the durable command result and current Project
// projection. It is not external-effect evidence.
type LeaseResult struct {
	Command domain.CommandResult
	Project domain.Project
}

// LeaseService applies typed commands through the TaskStore and obtains
// takeover facts only from its authorized process adapter.
type LeaseService struct {
	store    leaseStore
	observer processport.ProjectLeaseTakeoverObserver
}

func NewLeaseService(store leaseStore, observer processport.ProjectLeaseTakeoverObserver) *LeaseService {
	return &LeaseService{store: store, observer: observer}
}

var (
	requestPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.:@/-]{15,95}$`)
	projectPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.:@/-]{0,127}$`)
)

func commandPayload(value any) json.RawMessage {
	encoded, _ := json.Marshal(value)
	return encoded
}

func (service *LeaseService) result(ctx context.Context, projectID string, command domain.CommandResult, err error) (LeaseResult, error) {
	if err != nil {
		return LeaseResult{}, err
	}
	if command.Outcome != domain.CommandApplied && command.Outcome != domain.CommandRejectedVersionConflict {
		return LeaseResult{}, fmt.Errorf("persist Project lease: unknown command outcome")
	}
	project, err := service.store.Project(ctx, projectID)
	if err != nil {
		return LeaseResult{}, err
	}
	if project.Version < command.ObservedVersion {
		return LeaseResult{}, fmt.Errorf("persist Project lease: stale readback")
	}
	return LeaseResult{Command: command, Project: project}, nil
}

func validCommandIdentity(requestID, projectID string) bool {
	return requestPattern.MatchString(requestID) && projectPattern.MatchString(projectID)
}

func leaseRequest(command LeaseCommand, mutation domain.ProjectLeaseMutation) domain.CommandRequest {
	return domain.CommandRequest{
		IdempotencyKey: "lease-" + command.RequestID,
		Type:           "project.lease." + string(mutation.Kind), AggregateID: command.ProjectID,
		ExpectedVersion: command.ExpectedProjectVersion, Payload: commandPayload(mutation),
	}
}

// ApplyLease requests one explicit acquire, renew, or expired takeover. The
// store decides using time read inside the versioned transaction.
func (service *LeaseService) ApplyLease(ctx context.Context, command LeaseCommand) (LeaseResult, error) {
	if !validCommandIdentity(command.RequestID, command.ProjectID) ||
		(command.Kind != LeaseAcquire && command.Kind != LeaseRenew && command.Kind != LeaseTakeover) {
		return LeaseResult{}, domain.ErrLeaseTransitionInvalid
	}
	mutation := domain.ProjectLeaseMutation{
		Kind: command.Kind, ProjectID: command.ProjectID, ExpectedLeaseEpoch: command.ExpectedLeaseEpoch,
		HolderInstance: command.HolderInstance, HolderProcessIdentity: command.HolderProcessIdentity,
		DurationMillis: command.DurationMillis,
	}
	request := leaseRequest(command, mutation)
	result, err := service.store.ApplyProjectLease(ctx, request, mutation)
	return service.result(ctx, command.ProjectID, result, err)
}

func replayed(command domain.Command) domain.CommandResult {
	return domain.CommandResult{
		Outcome: command.Outcome, ObservedVersion: command.ObservedVersion,
		EventID: command.EventID, Replay: true,
	}
}

// ObserveTakeover obtains the absence/reconciliation fact from the authorized
// adapter and asks TaskStore to timestamp and persist it transactionally.
func (service *LeaseService) ObserveTakeover(ctx context.Context, command ObserveTakeoverCommand) (LeaseResult, error) {
	if !validCommandIdentity(command.RequestID, command.ProjectID) || service.observer == nil {
		return LeaseResult{}, domain.ErrLeaseProofInvalid
	}
	key := "lease-" + command.RequestID
	if stored, err := service.store.Command(ctx, key); err == nil {
		if stored.Type != "project.lease.observe_takeover" || stored.AggregateID != command.ProjectID {
			return LeaseResult{}, storeport.ErrIdempotencyConflict
		}
		if stored.ExpectedVersion != command.ExpectedProjectVersion {
			return LeaseResult{}, storeport.ErrIdempotencyConflict
		}
		return service.result(ctx, command.ProjectID, replayed(stored), nil)
	} else if err != nil && !errors.Is(err, storeport.ErrNotFound) {
		return LeaseResult{}, err
	}
	project, err := service.store.Project(ctx, command.ProjectID)
	if err != nil {
		return LeaseResult{}, err
	}
	if project.Version != command.ExpectedProjectVersion || project.Lease == nil || project.Lease.DispatchAllowed {
		return LeaseResult{}, domain.ErrLeaseProofInvalid
	}
	input, err := service.observer.ObserveProjectLeaseTakeover(ctx, processport.ProjectLeaseTakeoverTarget{
		ProjectID: project.ID, HolderInstance: project.Lease.HolderInstance,
		HolderProcessIdentity: project.Lease.HolderProcessIdentity, LeaseEpoch: project.Lease.Epoch,
		PriorHolderInstance:  project.Lease.PriorHolderInstance,
		PriorProcessIdentity: project.Lease.PriorProcessIdentity,
	})
	if err != nil {
		return LeaseResult{}, err
	}
	payload := struct {
		ProjectID string                              `json:"projectId"`
		Input     domain.ProjectLeaseObservationInput `json:"input"`
	}{command.ProjectID, input}
	request := domain.CommandRequest{
		IdempotencyKey: key, Type: "project.lease.observe_takeover", AggregateID: command.ProjectID,
		ExpectedVersion: command.ExpectedProjectVersion, Payload: commandPayload(payload),
	}
	result, err := service.store.RecordProjectLeaseObservation(ctx, request, command.ProjectID, input)
	return service.result(ctx, command.ProjectID, result, err)
}

// EnableDispatch atomically consumes one exact persisted fresh observation.
func (service *LeaseService) EnableDispatch(ctx context.Context, command EnableDispatchCommand) (LeaseResult, error) {
	if !validCommandIdentity(command.RequestID, command.ProjectID) || command.ObservationID == "" {
		return LeaseResult{}, domain.ErrLeaseProofInvalid
	}
	payload := struct {
		ProjectID     string `json:"projectId"`
		ObservationID string `json:"observationId"`
	}{command.ProjectID, command.ObservationID}
	request := domain.CommandRequest{
		IdempotencyKey: "lease-" + command.RequestID, Type: "project.lease.enable_dispatch",
		AggregateID: command.ProjectID, ExpectedVersion: command.ExpectedProjectVersion,
		Payload: commandPayload(payload),
	}
	result, err := service.store.EnableProjectLeaseDispatch(ctx, request, command.ProjectID, command.ObservationID)
	return service.result(ctx, command.ProjectID, result, err)
}

// Release clears one exact unexpired lease using TaskStore time.
func (service *LeaseService) Release(ctx context.Context, command ReleaseLeaseCommand) (LeaseResult, error) {
	if !validCommandIdentity(command.RequestID, command.ProjectID) {
		return LeaseResult{}, domain.ErrLeaseTransitionInvalid
	}
	leaseCommand := LeaseCommand{
		Kind: LeaseCommandKind(domain.ProjectLeaseRelease), RequestID: command.RequestID,
		ProjectID: command.ProjectID, ExpectedProjectVersion: command.ExpectedProjectVersion,
		ExpectedLeaseEpoch: command.ExpectedLeaseEpoch, HolderInstance: command.HolderInstance,
		HolderProcessIdentity: command.HolderProcessIdentity,
	}
	mutation := domain.ProjectLeaseMutation{
		Kind: domain.ProjectLeaseRelease, ProjectID: command.ProjectID,
		ExpectedLeaseEpoch: command.ExpectedLeaseEpoch, HolderInstance: command.HolderInstance,
		HolderProcessIdentity: command.HolderProcessIdentity,
	}
	request := leaseRequest(leaseCommand, mutation)
	result, err := service.store.ApplyProjectLease(ctx, request, mutation)
	return service.result(ctx, command.ProjectID, result, err)
}
