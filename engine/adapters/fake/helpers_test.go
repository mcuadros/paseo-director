// SPDX-License-Identifier: Apache-2.0

package fake

import (
	"context"
	"sync"
	"testing"

	"github.com/mcuadros/director-engine/domain/execution"
	runtimeport "github.com/mcuadros/director-engine/ports/runtime"
)

func TestHelperCapacityCompareAndReserveHasOneGlobalWinner(t *testing.T) {
	environment := NewEnvironment(Options{
		GlobalActiveAgents: 7,
		Operational:        execution.OperationalObservation{ObservedAtMillis: 1_000},
	})
	scope := execution.Scope{ProjectID: "project", WorkspaceID: "workspace", TaskID: "task", RunID: "run"}
	policy := execution.HelperPolicy{MaximumPerTask: 3, MaximumConcurrentAgents: 8}
	observation, err := environment.ObserveHelperCapacity(context.Background(), scope, policy)
	if err != nil {
		t.Fatal(err)
	}
	results := make(chan runtimeport.HelperCapacityReservationResult, 16)
	errorsSeen := make(chan error, 16)
	var wait sync.WaitGroup
	for index := 0; index < 16; index++ {
		wait.Add(1)
		go func(index int) {
			defer wait.Done()
			result, reserveErr := environment.ReserveHelperCapacity(context.Background(), runtimeport.HelperCapacityReservation{
				ID: "reservation-" + string(rune('a'+index)), Scope: scope, Policy: policy, Observation: observation,
			})
			results <- result
			errorsSeen <- reserveErr
		}(index)
	}
	wait.Wait()
	close(results)
	close(errorsSeen)
	for reserveErr := range errorsSeen {
		if reserveErr != nil {
			t.Fatal(reserveErr)
		}
	}
	applied := 0
	for result := range results {
		if result.Applied {
			applied++
		}
	}
	if applied != 1 || environment.HelperReservationCount() != 1 {
		t.Fatalf("applied=%d reservations=%d", applied, environment.HelperReservationCount())
	}
}

func TestHelperCapacityReservationReplayAndScopedRelease(t *testing.T) {
	environment := NewEnvironment(Options{GlobalActiveAgents: 1, Operational: execution.OperationalObservation{ObservedAtMillis: 1_000}})
	scope := execution.Scope{ProjectID: "project", WorkspaceID: "workspace", TaskID: "task", RunID: "run"}
	policy := execution.HelperPolicy{MaximumPerTask: 3, MaximumConcurrentAgents: 8}
	observation, _ := environment.ObserveHelperCapacity(context.Background(), scope, policy)
	request := runtimeport.HelperCapacityReservation{ID: "reservation", Scope: scope, Policy: policy, Observation: observation}
	first, err := environment.ReserveHelperCapacity(context.Background(), request)
	if err != nil || !first.Applied || first.Replay {
		t.Fatalf("first = %#v, %v", first, err)
	}
	replay, err := environment.Restart().ReserveHelperCapacity(context.Background(), request)
	if err != nil || !replay.Applied || !replay.Replay {
		t.Fatalf("replay = %#v, %v", replay, err)
	}
	wrong := scope
	wrong.RunID = "other-run"
	if err := environment.ReleaseHelperCapacity(context.Background(), request.ID, wrong); err == nil {
		t.Fatal("wrong-scope release was accepted")
	}
	if err := environment.ReleaseHelperCapacity(context.Background(), request.ID, scope); err != nil {
		t.Fatal(err)
	}
	if err := environment.ReleaseHelperCapacity(context.Background(), request.ID, scope); err != nil {
		t.Fatal(err)
	}
	if environment.HelperReservationCount() != 0 {
		t.Fatal("idempotent release retained reservation")
	}
}
