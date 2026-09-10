// SPDX-License-Identifier: Apache-2.0

package fake

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"math"
	"slices"
	"sync"

	domainscheduling "github.com/mcuadros/director-engine/domain/scheduling"
	storeport "github.com/mcuadros/director-engine/ports/scheduling"
)

type storedReservationBatch struct {
	hash   string
	result storeport.ReserveResult
}

type storedPermit struct {
	hash   string
	permit storeport.Permit
}

type reservationWorld struct {
	mu                     sync.Mutex
	snapshot               domainscheduling.Snapshot
	reservations           map[string]domainscheduling.Reservation
	batches                map[string]storedReservationBatch
	permits                map[string]storedPermit
	authorizedReservations map[string]string
}

// ReservationStore is a deterministic restartable fake of the scheduler's
// compare-and-reserve store. All restarted handles share one durable world.
type ReservationStore struct {
	world *reservationWorld
}

func NewReservationStore(snapshot domainscheduling.Snapshot) *ReservationStore {
	return &ReservationStore{world: &reservationWorld{
		snapshot:     domainscheduling.CloneSnapshot(snapshot),
		reservations: make(map[string]domainscheduling.Reservation),
		batches:      make(map[string]storedReservationBatch), permits: make(map[string]storedPermit),
		authorizedReservations: make(map[string]string),
	}}
}

func (store *ReservationStore) Restart() *ReservationStore {
	if store == nil {
		return nil
	}
	return &ReservationStore{world: store.world}
}

func (store *ReservationStore) Snapshot(ctx context.Context, projectID string) (domainscheduling.Snapshot, error) {
	if err := ctx.Err(); err != nil {
		return domainscheduling.Snapshot{}, err
	}
	if store == nil || store.world == nil {
		return domainscheduling.Snapshot{}, storeport.ErrInvalidRequest
	}
	store.world.mu.Lock()
	defer store.world.mu.Unlock()
	if store.world.snapshot.ProjectID != projectID {
		return domainscheduling.Snapshot{}, storeport.ErrInvalidRequest
	}
	return domainscheduling.CloneSnapshot(store.world.snapshot), nil
}

func reservationPayloadHash(value any) string {
	encoded, err := json.Marshal(value)
	if err != nil {
		return ""
	}
	digest := sha256.Sum256(encoded)
	return hex.EncodeToString(digest[:])
}

func validReservationBatch(batch storeport.ReservationBatch) bool {
	if batch.ID == "" || batch.ProjectID == "" || batch.FactsHash == "" || batch.LeaseEpoch == 0 || len(batch.Reservations) == 0 {
		return false
	}
	seen := make(map[string]struct{}, len(batch.Reservations))
	for _, reservation := range batch.Reservations {
		if reservation.ID == "" || reservation.TaskID == "" || reservation.Workspace == "" || reservation.TaskVersion == 0 ||
			reservation.LeaseEpoch != batch.LeaseEpoch {
			return false
		}
		if _, duplicate := seen[reservation.ID]; duplicate {
			return false
		}
		seen[reservation.ID] = struct{}{}
	}
	return true
}

func sameReservationTask(task domainscheduling.TaskFacts, reservation domainscheduling.Reservation) bool {
	return task.ID == reservation.TaskID && task.Workspace == reservation.Workspace &&
		task.TaskVersion == reservation.TaskVersion && task.WorkClass == reservation.WorkClass &&
		task.Demand == reservation.Demand && task.Budgets.Time.Requested == reservation.TimeRequested &&
		task.Budgets.Tokens.Requested == reservation.TokenRequested && task.Budgets.Turns.Requested == reservation.TurnRequested &&
		task.Budgets.Cost.Requested == reservation.CostRequested && task.Budgets.CI.Requested == reservation.CIRequested
}

func addCapacity(left, right uint64) (uint64, bool) {
	if right > math.MaxUint64-left {
		return 0, false
	}
	return left + right, true
}

func workspaceIndex(snapshot domainscheduling.Snapshot, id domainscheduling.WorkspaceID) int {
	return slices.IndexFunc(snapshot.WorkspaceUsages, func(usage domainscheduling.WorkspaceUsage) bool {
		return usage.Workspace == id
	})
}

func (store *ReservationStore) CompareAndReserve(ctx context.Context, batch storeport.ReservationBatch) (storeport.ReserveResult, error) {
	if err := ctx.Err(); err != nil {
		return storeport.ReserveResult{}, err
	}
	if store == nil || store.world == nil || !validReservationBatch(batch) {
		return storeport.ReserveResult{}, storeport.ErrInvalidRequest
	}
	hash := reservationPayloadHash(batch)
	if hash == "" {
		return storeport.ReserveResult{}, storeport.ErrInvalidRequest
	}
	store.world.mu.Lock()
	defer store.world.mu.Unlock()
	if previous, ok := store.world.batches[batch.ID]; ok {
		if previous.hash != hash {
			return storeport.ReserveResult{}, storeport.ErrIdempotencyConflict
		}
		result := previous.result
		result.Replay = true
		return result, nil
	}
	snapshot := &store.world.snapshot
	result := storeport.ReserveResult{SnapshotVersion: snapshot.Version}
	if batch.ProjectID != snapshot.ProjectID || batch.ExpectedSnapshotVersion != snapshot.Version {
		result.Outcome = storeport.ReserveVersionConflict
		store.world.batches[batch.ID] = storedReservationBatch{hash: hash, result: result}
		return result, nil
	}
	if snapshot.Lease != domainscheduling.LeaseCurrent || snapshot.LeaseEpoch != batch.LeaseEpoch {
		result.Outcome = storeport.ReserveLeaseLost
		store.world.batches[batch.ID] = storedReservationBatch{hash: hash, result: result}
		return result, nil
	}
	taskIndex := make(map[domainscheduling.TaskID]int, len(snapshot.Tasks))
	for index, task := range snapshot.Tasks {
		taskIndex[task.ID] = index
	}
	selected := make(map[domainscheduling.TaskID]struct{}, len(batch.Reservations))
	next := domainscheduling.CloneSnapshot(*snapshot)
	for _, reservation := range batch.Reservations {
		index, ok := taskIndex[reservation.TaskID]
		if !ok || !sameReservationTask(snapshot.Tasks[index], reservation) {
			return storeport.ReserveResult{}, storeport.ErrInvalidRequest
		}
		if _, duplicate := selected[reservation.TaskID]; duplicate {
			return storeport.ReserveResult{}, storeport.ErrInvalidRequest
		}
		selected[reservation.TaskID] = struct{}{}
		var okCapacity bool
		next.Usage.ReservedTasks, okCapacity = addCapacity(next.Usage.ReservedTasks, reservation.Demand.ProjectTasks)
		if !okCapacity {
			return storeport.ReserveResult{}, storeport.ErrInvalidRequest
		}
		next.Usage.ReservedAgents, okCapacity = addCapacity(next.Usage.ReservedAgents, reservation.Demand.Agents)
		if !okCapacity {
			return storeport.ReserveResult{}, storeport.ErrInvalidRequest
		}
		workspace := workspaceIndex(next, reservation.Workspace)
		if workspace < 0 {
			next.WorkspaceUsages = append(next.WorkspaceUsages, domainscheduling.WorkspaceUsage{Workspace: reservation.Workspace})
			workspace = len(next.WorkspaceUsages) - 1
		}
		next.WorkspaceUsages[workspace].ReservedTasks, okCapacity = addCapacity(
			next.WorkspaceUsages[workspace].ReservedTasks, reservation.Demand.WorkspaceTasks,
		)
		if !okCapacity {
			return storeport.ReserveResult{}, storeport.ErrInvalidRequest
		}
	}
	next.Tasks = slices.DeleteFunc(next.Tasks, func(task domainscheduling.TaskFacts) bool {
		_, reserved := selected[task.ID]
		return reserved
	})
	if next.Version == math.MaxUint64 {
		return storeport.ReserveResult{}, storeport.ErrInvalidRequest
	}
	next.Version++
	for _, reservation := range batch.Reservations {
		store.world.reservations[reservation.ID] = reservation
	}
	store.world.snapshot = next
	result.Outcome = storeport.ReserveApplied
	result.SnapshotVersion = next.Version
	store.world.batches[batch.ID] = storedReservationBatch{hash: hash, result: result}
	return result, nil
}

func (store *ReservationStore) AuthorizeLaunch(ctx context.Context, request storeport.AuthorizeRequest) (storeport.Permit, error) {
	if err := ctx.Err(); err != nil {
		return storeport.Permit{}, err
	}
	if store == nil || store.world == nil || request.RequestID == "" || request.ProjectID == "" ||
		request.ReservationID == "" || request.LeaseEpoch == 0 {
		return storeport.Permit{}, storeport.ErrInvalidRequest
	}
	hash := reservationPayloadHash(request)
	store.world.mu.Lock()
	defer store.world.mu.Unlock()
	if previous, ok := store.world.permits[request.RequestID]; ok {
		if previous.hash != hash {
			return storeport.Permit{}, storeport.ErrIdempotencyConflict
		}
		permit := previous.permit
		permit.Replay = true
		return permit, nil
	}
	reservation, ok := store.world.reservations[request.ReservationID]
	if !ok || request.ProjectID != store.world.snapshot.ProjectID {
		return storeport.Permit{}, storeport.ErrReservationNotFound
	}
	permit := storeport.Permit{ReservationID: request.ReservationID, LeaseEpoch: request.LeaseEpoch}
	_, alreadyAuthorized := store.world.authorizedReservations[request.ReservationID]
	if !alreadyAuthorized && store.world.snapshot.Lease == domainscheduling.LeaseCurrent &&
		store.world.snapshot.LeaseEpoch == request.LeaseEpoch && reservation.LeaseEpoch == request.LeaseEpoch {
		permit.Allowed = true
		permit.ID = reservationPayloadHash(struct {
			Request storeport.AuthorizeRequest   `json:"request"`
			Target  domainscheduling.Reservation `json:"target"`
		}{request, reservation})
		store.world.authorizedReservations[request.ReservationID] = permit.ID
	}
	store.world.permits[request.RequestID] = storedPermit{hash: hash, permit: permit}
	return permit, nil
}

// SetLease is a fake external fact transition. It mutates no policy and bumps
// the compare-and-swap version exactly as a durable lease observation would.
func (store *ReservationStore) SetLease(state domainscheduling.LeaseState, epoch uint64) error {
	if store == nil || store.world == nil || epoch == 0 {
		return storeport.ErrInvalidRequest
	}
	store.world.mu.Lock()
	defer store.world.mu.Unlock()
	if store.world.snapshot.Version == math.MaxUint64 {
		return storeport.ErrInvalidRequest
	}
	store.world.snapshot.Lease = state
	store.world.snapshot.LeaseEpoch = epoch
	store.world.snapshot.Version++
	return nil
}

func (store *ReservationStore) Reservations() []domainscheduling.Reservation {
	if store == nil || store.world == nil {
		return nil
	}
	store.world.mu.Lock()
	defer store.world.mu.Unlock()
	result := make([]domainscheduling.Reservation, 0, len(store.world.reservations))
	for _, reservation := range store.world.reservations {
		result = append(result, reservation)
	}
	slices.SortFunc(result, func(left, right domainscheduling.Reservation) int {
		if left.ID < right.ID {
			return -1
		}
		if left.ID > right.ID {
			return 1
		}
		return 0
	})
	return result
}
