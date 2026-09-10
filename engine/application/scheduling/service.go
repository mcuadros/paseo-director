// SPDX-License-Identifier: Apache-2.0

// Package scheduling applies the pure scheduler reducer and asks one store to
// reserve its complete selected batch atomically. It dispatches no external
// effect; callers must obtain a one-use lease-bound permit immediately before
// launch.
package scheduling

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"regexp"
	"strings"

	domainscheduling "github.com/mcuadros/director-engine/domain/scheduling"
	storeport "github.com/mcuadros/director-engine/ports/scheduling"
	"github.com/mcuadros/director-engine/reducer/scheduler"
)

var (
	ErrInvalidCommand     = errors.New("scheduler command is invalid")
	ErrInvalidStoreResult = errors.New("scheduler store returned an invalid result")
	requestPattern        = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.:@/-]{15,95}$`)
	projectPattern        = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.:@/-]{0,127}$`)
)

type Command struct {
	RequestID string
	ProjectID string
}

type Result struct {
	Decision    scheduler.Result
	Reservation storeport.ReserveResult
	BatchID     string
}

type Service struct {
	store storeport.Store
}

func NewService(store storeport.Store) *Service { return &Service{store: store} }

func stableID(prefix string, parts ...string) string {
	digest := sha256.Sum256([]byte(strings.Join(parts, "\x1f")))
	return prefix + "-" + hex.EncodeToString(digest[:16])
}

func validCommand(command Command) bool {
	return requestPattern.MatchString(command.RequestID) && projectPattern.MatchString(command.ProjectID)
}

// Schedule reduces one immutable snapshot and atomically reserves every
// selected item. A version conflict is a closed result: a later scheduler
// cycle must use a new durable command identity and freshly observed facts.
func (service *Service) Schedule(ctx context.Context, command Command) (Result, error) {
	if service == nil || service.store == nil || !validCommand(command) {
		return Result{}, ErrInvalidCommand
	}
	snapshot, err := service.store.Snapshot(ctx, command.ProjectID)
	if err != nil {
		return Result{}, err
	}
	if snapshot.ProjectID != command.ProjectID {
		return Result{}, ErrInvalidCommand
	}
	decision := scheduler.Reduce(snapshot)
	result := Result{Decision: decision}
	if len(decision.OrderedTaskIDs) == 0 {
		return result, nil
	}
	tasks := make(map[domainscheduling.TaskID]domainscheduling.TaskFacts, len(snapshot.Tasks))
	for _, task := range snapshot.Tasks {
		tasks[task.ID] = task
	}
	batch := storeport.ReservationBatch{
		ID:        stableID("scheduler-batch", command.ProjectID, command.RequestID),
		ProjectID: command.ProjectID, ExpectedSnapshotVersion: snapshot.Version,
		FactsHash: decision.FactsHash, LeaseEpoch: snapshot.LeaseEpoch,
		Reservations: make([]domainscheduling.Reservation, 0, len(decision.OrderedTaskIDs)),
	}
	for _, taskID := range decision.OrderedTaskIDs {
		task, ok := tasks[taskID]
		if !ok {
			return Result{}, ErrInvalidCommand
		}
		batch.Reservations = append(batch.Reservations, domainscheduling.Reservation{
			ID:     stableID("scheduler-reservation", command.ProjectID, command.RequestID, string(task.ID)),
			TaskID: task.ID, Workspace: task.Workspace, TaskVersion: task.TaskVersion,
			WorkClass: task.WorkClass, LeaseEpoch: snapshot.LeaseEpoch, Demand: task.Demand,
			TimeRequested:  task.Budgets.Time.Requested,
			TokenRequested: task.Budgets.Tokens.Requested,
			TurnRequested:  task.Budgets.Turns.Requested,
			CostRequested:  task.Budgets.Cost.Requested,
			CIRequested:    task.Budgets.CI.Requested,
		})
	}
	reserved, err := service.store.CompareAndReserve(ctx, batch)
	if err != nil {
		return Result{}, err
	}
	if reserved.Outcome != storeport.ReserveApplied && reserved.Outcome != storeport.ReserveVersionConflict &&
		reserved.Outcome != storeport.ReserveLeaseLost {
		return Result{}, ErrInvalidStoreResult
	}
	result.Reservation = reserved
	result.BatchID = batch.ID
	return result, nil
}

// AuthorizeLaunch asks the store to consume one exact permit under the current
// lease. Lease loss, takeover, or observe-only reconciliation state therefore
// blocks the external effect even after an earlier reservation succeeded.
func (service *Service) AuthorizeLaunch(ctx context.Context, command Command, reservationID string, leaseEpoch uint64) (storeport.Permit, error) {
	if service == nil || service.store == nil || !validCommand(command) || reservationID == "" || leaseEpoch == 0 {
		return storeport.Permit{}, ErrInvalidCommand
	}
	permit, err := service.store.AuthorizeLaunch(ctx, storeport.AuthorizeRequest{
		RequestID: command.RequestID, ProjectID: command.ProjectID,
		ReservationID: reservationID, LeaseEpoch: leaseEpoch,
	})
	if err != nil {
		return storeport.Permit{}, err
	}
	if permit.ReservationID != reservationID || permit.LeaseEpoch != leaseEpoch ||
		(permit.Allowed && permit.ID == "") || (!permit.Allowed && permit.ID != "") {
		return storeport.Permit{}, ErrInvalidStoreResult
	}
	return permit, nil
}
