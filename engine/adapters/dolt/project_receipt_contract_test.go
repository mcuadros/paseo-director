// SPDX-License-Identifier: Apache-2.0

package dolt_test

import (
	"context"
	"reflect"
	"strings"
	"testing"

	"github.com/mcuadros/director-engine/adapters/dolt"
	"github.com/mcuadros/director-engine/domain"
	domainscheduling "github.com/mcuadros/director-engine/domain/scheduling"
)

func TestDoltProjectCASReceiptSurvivesRestartAndLeaseMutation(t *testing.T) {
	fixture := startDoltFixture(t)
	const storeID = "scheduler-ledger-store"
	store := openContractStore(t, fixture, storeID, true)
	project, workspaces := createPlanningProject(t, store)
	nowMillis := int64(1_000)
	dolt.UseTransactionTimestampForTest(store, &nowMillis)
	lease := domain.ProjectLeaseMutation{Kind: domain.ProjectLeaseAcquire, ProjectID: project.ID,
		HolderInstance: "scheduler-engine", HolderProcessIdentity: "pid-1:start-1", DurationMillis: 60_000}
	result, err := store.ApplyProjectLease(context.Background(),
		typedCommand(t, "scheduler-lease-acquire", "project.lease.acquire", project.ID, 0, lease), lease)
	requireApplied(t, result, err)
	project, err = store.Project(context.Background(), project.ID)
	if err != nil || project.Version != 1 || project.Lease == nil {
		t.Fatalf("acquired Project lease = %#v, %v", project, err)
	}

	reservation := domainscheduling.Reservation{ID: "scheduler-reservation-contract", TaskID: "scheduler-task",
		Workspace: domainscheduling.WorkspaceID(workspaces[0].ID), TaskVersion: 1,
		WorkClass: domainscheduling.WorkNewRun, LeaseEpoch: project.Lease.Epoch,
		Demand: domainscheduling.NewRunDemand(), TimeRequested: 60_000, TokenRequested: 1_000,
		TurnRequested: 1, CIRequested: 1}
	project.Scheduling = domain.SchedulingLedger{
		Batches: []domain.SchedulingBatchRecord{{ID: "scheduler-batch-contract", PayloadSHA256: strings.Repeat("a", 64),
			Outcome: "applied", SnapshotVersion: 2, LeaseEpoch: project.Lease.Epoch,
			ReservationIDs: []string{reservation.ID}}},
		Reservations: []domain.SchedulingReservationRecord{{Reservation: reservation}},
	}
	project.Version = 2
	result, err = store.UpdateProject(context.Background(),
		command("scheduler-batch-contract", "scheduler.reserve", project.ID, 1, `{}`), project,
		event("scheduler-batch-contract-event", "", 3, project.ID, 2, "scheduler.reserved"))
	requireApplied(t, result, err)
	want := project.Scheduling
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}

	restarted := openContractStore(t, fixture, storeID, false)
	t.Cleanup(func() { _ = restarted.Close() })
	project, err = restarted.Project(context.Background(), project.ID)
	if err != nil || !reflect.DeepEqual(project.Scheduling, want) {
		t.Fatalf("restarted scheduling ledger = %#v, %v", project.Scheduling, err)
	}
	nowMillis = 2_000
	dolt.UseTransactionTimestampForTest(restarted, &nowMillis)
	renew := domain.ProjectLeaseMutation{Kind: domain.ProjectLeaseRenew, ProjectID: project.ID,
		ExpectedLeaseEpoch: project.Lease.Epoch, HolderInstance: project.Lease.HolderInstance,
		HolderProcessIdentity: project.Lease.HolderProcessIdentity, DurationMillis: 60_000}
	result, err = restarted.ApplyProjectLease(context.Background(),
		typedCommand(t, "scheduler-lease-renew", "project.lease.renew", project.ID, project.Version, renew), renew)
	requireApplied(t, result, err)
	project, err = restarted.Project(context.Background(), project.ID)
	if err != nil || !reflect.DeepEqual(project.Scheduling, want) {
		t.Fatalf("lease mutation lost scheduling ledger = %#v, %v", project.Scheduling, err)
	}
}
