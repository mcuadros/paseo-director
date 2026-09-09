// SPDX-License-Identifier: Apache-2.0

package projects

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"sort"
	"sync"
	"testing"

	"github.com/mcuadros/director-engine/domain"
	processport "github.com/mcuadros/director-engine/ports/process"
	storeport "github.com/mcuadros/director-engine/ports/taskstore"
)

type storedLeaseCommand struct {
	request domain.CommandRequest
	result  domain.CommandResult
}

type memoryLeaseStore struct {
	mu       sync.Mutex
	project  domain.Project
	now      int64
	commands map[string]storedLeaseCommand
}

func cloneLeaseProject(project domain.Project) domain.Project {
	if project.Organizer != nil {
		organizer := *project.Organizer
		organizer.PendingConfiguration = append(json.RawMessage(nil), organizer.PendingConfiguration...)
		project.Organizer = &organizer
	}
	if project.Lease != nil {
		lease := *project.Lease
		project.Lease = &lease
	}
	if project.LeaseObservation != nil {
		observation := *project.LeaseObservation
		project.LeaseObservation = &observation
	}
	return project
}

func newMemoryLeaseStore() *memoryLeaseStore {
	projectID := "project-1"
	return &memoryLeaseStore{
		project: domain.Project{
			ID: projectID, Name: "Project", State: "active",
			Organizer: &domain.Organizer{ID: domain.OrganizerID(projectID)},
		},
		now: 1_000, commands: make(map[string]storedLeaseCommand),
	}
}

func (store *memoryLeaseStore) Project(_ context.Context, id string) (domain.Project, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	if store.project.ID != id {
		return domain.Project{}, storeport.ErrNotFound
	}
	return cloneLeaseProject(store.project), nil
}

func (store *memoryLeaseStore) Command(_ context.Context, id string) (domain.Command, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	stored, ok := store.commands[id]
	if !ok {
		return domain.Command{}, storeport.ErrNotFound
	}
	return domain.Command{
		IdempotencyKey: stored.request.IdempotencyKey, Type: stored.request.Type,
		AggregateID: stored.request.AggregateID, ExpectedVersion: stored.request.ExpectedVersion,
		Payload: stored.request.Payload, Outcome: stored.result.Outcome,
		ObservedVersion: stored.result.ObservedVersion, EventID: stored.result.EventID,
	}, nil
}

func (store *memoryLeaseStore) apply(
	request domain.CommandRequest,
	transition func(domain.Project, int64) (domain.Project, error),
) (domain.CommandResult, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	if stored, ok := store.commands[request.IdempotencyKey]; ok {
		if !reflect.DeepEqual(stored.request, request) {
			return domain.CommandResult{}, storeport.ErrIdempotencyConflict
		}
		result := stored.result
		result.Replay = true
		return result, nil
	}
	if request.ExpectedVersion != store.project.Version {
		result := domain.CommandResult{Outcome: domain.CommandRejectedVersionConflict, ObservedVersion: store.project.Version}
		store.commands[request.IdempotencyKey] = storedLeaseCommand{request, result}
		return result, nil
	}
	next, err := transition(cloneLeaseProject(store.project), store.now)
	if err != nil {
		return domain.CommandResult{}, err
	}
	if next.Version != store.project.Version+1 {
		return domain.CommandResult{}, storeport.ErrInvalidRecord
	}
	store.project = cloneLeaseProject(next)
	result := domain.CommandResult{
		Outcome: domain.CommandApplied, ObservedVersion: next.Version,
		EventID: "event-" + request.IdempotencyKey,
	}
	store.commands[request.IdempotencyKey] = storedLeaseCommand{request, result}
	return result, nil
}

func (store *memoryLeaseStore) ApplyProjectLease(
	_ context.Context,
	request domain.CommandRequest,
	mutation domain.ProjectLeaseMutation,
) (domain.CommandResult, error) {
	return store.apply(request, func(current domain.Project, now int64) (domain.Project, error) {
		lease, lastLeaseEpoch, err := domain.ApplyProjectLeaseMutation(
			current.Lease, current.LastLeaseEpoch, mutation, now,
		)
		if err != nil {
			return domain.Project{}, err
		}
		current.Version++
		current.LastLeaseEpoch = lastLeaseEpoch
		current.Lease = lease
		if mutation.Kind != domain.ProjectLeaseRenew {
			current.LeaseObservation = nil
		}
		return current, nil
	})
}

func (store *memoryLeaseStore) RecordProjectLeaseObservation(
	_ context.Context,
	request domain.CommandRequest,
	projectID string,
	input domain.ProjectLeaseObservationInput,
) (domain.CommandResult, error) {
	return store.apply(request, func(current domain.Project, now int64) (domain.Project, error) {
		observation, err := domain.NewProjectLeaseObservation(projectID, current.Lease, input, now)
		if err != nil {
			return domain.Project{}, err
		}
		current.Version++
		current.LeaseObservation = observation
		return current, nil
	})
}

func (store *memoryLeaseStore) EnableProjectLeaseDispatch(
	_ context.Context,
	request domain.CommandRequest,
	projectID string,
	observationID string,
) (domain.CommandResult, error) {
	return store.apply(request, func(current domain.Project, now int64) (domain.Project, error) {
		if current.LeaseObservation == nil || current.LeaseObservation.ID != observationID {
			return domain.Project{}, domain.ErrLeaseProofInvalid
		}
		lease, err := domain.EnableProjectLeaseDispatch(projectID, current.Lease, current.LeaseObservation, now)
		if err != nil {
			return domain.Project{}, err
		}
		current.Version++
		current.Lease = lease
		return current, nil
	})
}

type fakeTakeoverObserver struct {
	mu     sync.Mutex
	calls  int
	target processport.ProjectLeaseTakeoverTarget
	result domain.ProjectLeaseObservationInput
	err    error
}

func (observer *fakeTakeoverObserver) ObserveProjectLeaseTakeover(
	_ context.Context,
	target processport.ProjectLeaseTakeoverTarget,
) (domain.ProjectLeaseObservationInput, error) {
	observer.mu.Lock()
	defer observer.mu.Unlock()
	observer.calls++
	observer.target = target
	return observer.result, observer.err
}

func validObservationInput() domain.ProjectLeaseObservationInput {
	return domain.ProjectLeaseObservationInput{
		AdapterKind: "linux-process-supervisor", AdapterVersion: "v1",
		FactHash: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
	}
}

func TestLeaseCommandsExposeNoCallerClockOrAuthorityBooleans(t *testing.T) {
	for _, value := range []any{LeaseCommand{}, ObserveTakeoverCommand{}, EnableDispatchCommand{}, ReleaseLeaseCommand{}} {
		typeOf := reflect.TypeOf(value)
		for _, forbidden := range []string{
			"TaskStoreNowMillis", "ExpiresAtMillis", "Proof", "PriorProcessAbsent",
			"DispatchChildrenAbsent", "FullReconciliation", "OneDaemonIdentity",
		} {
			if _, present := typeOf.FieldByName(forbidden); present {
				t.Fatalf("%s exposes forbidden caller field %s", typeOf.Name(), forbidden)
			}
		}
	}
}

func TestLeaseServiceConcurrentClaimsHaveOneWinner(t *testing.T) {
	store := newMemoryLeaseStore()
	service := NewLeaseService(store, nil)
	commands := []LeaseCommand{
		{Kind: LeaseAcquire, RequestID: "request-engine-a-0001", ProjectID: "project-1", HolderInstance: "engine-a", HolderProcessIdentity: "pid-100:start-1", DurationMillis: 1_000},
		{Kind: LeaseAcquire, RequestID: "request-engine-b-0001", ProjectID: "project-1", HolderInstance: "engine-b", HolderProcessIdentity: "pid-200:start-1", DurationMillis: 1_000},
	}
	start := make(chan struct{})
	results := make(chan LeaseResult, 2)
	errorsChannel := make(chan error, 2)
	var wait sync.WaitGroup
	for _, command := range commands {
		command := command
		wait.Add(1)
		go func() {
			defer wait.Done()
			<-start
			result, err := service.ApplyLease(context.Background(), command)
			results <- result
			errorsChannel <- err
		}()
	}
	close(start)
	wait.Wait()
	close(results)
	close(errorsChannel)
	for err := range errorsChannel {
		if err != nil {
			t.Fatalf("concurrent claim: %v", err)
		}
	}
	var outcomes []domain.CommandOutcome
	for result := range results {
		outcomes = append(outcomes, result.Command.Outcome)
	}
	sort.Slice(outcomes, func(left, right int) bool { return outcomes[left] < outcomes[right] })
	if !reflect.DeepEqual(outcomes, []domain.CommandOutcome{domain.CommandApplied, domain.CommandRejectedVersionConflict}) {
		t.Fatalf("outcomes = %v", outcomes)
	}
}

func TestLeaseServiceTakeoverUsesAuthorizedPersistedObservation(t *testing.T) {
	store := newMemoryLeaseStore()
	observer := &fakeTakeoverObserver{result: validObservationInput()}
	service := NewLeaseService(store, observer)
	ctx := context.Background()
	acquired, err := service.ApplyLease(ctx, LeaseCommand{
		Kind: LeaseAcquire, RequestID: "request-acquire-0001", ProjectID: "project-1",
		HolderInstance: "engine-a", HolderProcessIdentity: "pid-100:start-1", DurationMillis: 1_000,
	})
	if err != nil || acquired.Command.Outcome != domain.CommandApplied {
		t.Fatalf("acquire = %#v, %v", acquired, err)
	}
	store.now = 2_000
	taken, err := service.ApplyLease(ctx, LeaseCommand{
		Kind: LeaseTakeover, RequestID: "request-takeover-0001", ProjectID: "project-1",
		ExpectedProjectVersion: 1, ExpectedLeaseEpoch: 1,
		HolderInstance: "engine-b", HolderProcessIdentity: "pid-200:start-2", DurationMillis: 2_000,
	})
	if err != nil || taken.Project.Lease == nil || taken.Project.Lease.DispatchAllowed || taken.Project.Lease.Epoch != 2 {
		t.Fatalf("takeover = %#v, %v", taken, err)
	}
	observed, err := service.ObserveTakeover(ctx, ObserveTakeoverCommand{
		RequestID: "request-observe-0001", ProjectID: "project-1", ExpectedProjectVersion: 2,
	})
	if err != nil || observed.Project.LeaseObservation == nil || observer.calls != 1 {
		t.Fatalf("observe = %#v calls=%d error=%v", observed, observer.calls, err)
	}
	if observer.target.ProjectID != "project-1" || observer.target.LeaseEpoch != 2 ||
		observer.target.PriorProcessIdentity != "pid-100:start-1" {
		t.Fatalf("observer target = %#v", observer.target)
	}
	if _, err := service.EnableDispatch(ctx, EnableDispatchCommand{
		RequestID: "request-enable-fake-01", ProjectID: "project-1", ExpectedProjectVersion: 3,
		ObservationID: "fabricated-observation",
	}); !errors.Is(err, domain.ErrLeaseProofInvalid) {
		t.Fatalf("fabricated observation error = %v", err)
	}
	enabled, err := service.EnableDispatch(ctx, EnableDispatchCommand{
		RequestID: "request-enable-000001", ProjectID: "project-1", ExpectedProjectVersion: 3,
		ObservationID: observed.Project.LeaseObservation.ID,
	})
	if err != nil || enabled.Project.Lease == nil || !enabled.Project.Lease.DispatchAllowed {
		t.Fatalf("enable = %#v, %v", enabled, err)
	}
	replay, err := NewLeaseService(store, observer).ObserveTakeover(ctx, ObserveTakeoverCommand{
		RequestID: "request-observe-0001", ProjectID: "project-1", ExpectedProjectVersion: 2,
	})
	if err != nil || !replay.Command.Replay || observer.calls != 1 {
		t.Fatalf("observation replay = %#v calls=%d error=%v", replay, observer.calls, err)
	}
}
