// SPDX-License-Identifier: Apache-2.0

package domain

import (
	"slices"
	"strings"
	"testing"

	domainscheduling "github.com/mcuadros/director-engine/domain/scheduling"
)

func schedulingLedgerFixture() SchedulingLedger {
	reservation := domainscheduling.Reservation{ID: "scheduler-reservation-1", TaskID: "task-1", Workspace: "workspace-1",
		TaskVersion: 3, WorkClass: domainscheduling.WorkNewRun, LeaseEpoch: 7, Demand: domainscheduling.NewRunDemand(),
		TimeRequested: 60_000, TokenRequested: 1_000, TurnRequested: 1, CIRequested: 1}
	permit := strings.Repeat("b", 64)
	return SchedulingLedger{
		Batches: []SchedulingBatchRecord{{ID: "scheduler-batch-1", PayloadSHA256: strings.Repeat("a", 64),
			Outcome: "applied", SnapshotVersion: 9, LeaseEpoch: 7, ReservationIDs: []string{reservation.ID}}},
		Reservations: []SchedulingReservationRecord{{Reservation: reservation, PermitID: permit}},
		Permits: []SchedulingPermitRecord{{RequestID: "scheduler-permit-1", PayloadSHA256: strings.Repeat("c", 64),
			PermitID: permit, ReservationID: reservation.ID, LeaseEpoch: 7, Allowed: true}},
	}
}

func TestSchedulingLedgerBindsBatchReservationPermitAndLeaseExactly(t *testing.T) {
	fixture := schedulingLedgerFixture()
	if !ValidSchedulingLedger(fixture) {
		t.Fatal("valid scheduling ledger was refused")
	}
	mutations := map[string]func(*SchedulingLedger){
		"batch hash":        func(value *SchedulingLedger) { value.Batches[0].PayloadSHA256 = strings.Repeat("d", 63) },
		"batch reservation": func(value *SchedulingLedger) { value.Batches[0].ReservationIDs[0] = "scheduler-reservation-other" },
		"reservation epoch": func(value *SchedulingLedger) { value.Reservations[0].Reservation.LeaseEpoch++ },
		"permit lease":      func(value *SchedulingLedger) { value.Permits[0].LeaseEpoch++ },
		"permit identity":   func(value *SchedulingLedger) { value.Permits[0].PermitID = strings.Repeat("d", 64) },
		"duplicate permit":  func(value *SchedulingLedger) { value.Permits = append(value.Permits, value.Permits[0]) },
	}
	for name, mutate := range mutations {
		t.Run(name, func(t *testing.T) {
			candidate := fixture
			candidate.Batches = slices.Clone(fixture.Batches)
			candidate.Batches[0].ReservationIDs = slices.Clone(fixture.Batches[0].ReservationIDs)
			candidate.Reservations = slices.Clone(fixture.Reservations)
			candidate.Permits = slices.Clone(fixture.Permits)
			mutate(&candidate)
			if ValidSchedulingLedger(candidate) {
				t.Fatal("mutated scheduling ledger was accepted")
			}
		})
	}
}
