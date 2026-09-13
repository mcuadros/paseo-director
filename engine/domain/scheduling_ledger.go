// SPDX-License-Identifier: Apache-2.0

package domain

import (
	"slices"

	domainscheduling "github.com/mcuadros/director-engine/domain/scheduling"
)

const MaximumSchedulingLedgerRecords = 512

// SchedulingBatchRecord is the durable Project-CAS receipt for one scheduler
// reduction. Facts and Reservation IDs are bounded; no connector output or
// machine path enters the aggregate.
type SchedulingBatchRecord struct {
	ID              string   `json:"id"`
	PayloadSHA256   string   `json:"payloadSha256"`
	Outcome         string   `json:"outcome"`
	SnapshotVersion uint64   `json:"snapshotVersion"`
	LeaseEpoch      uint64   `json:"leaseEpoch"`
	ReservationIDs  []string `json:"reservationIds"`
}

type SchedulingReservationRecord struct {
	Reservation domainscheduling.Reservation `json:"reservation"`
	PermitID    string                       `json:"permitId,omitempty"`
}

type SchedulingPermitRecord struct {
	RequestID     string `json:"requestId"`
	PayloadSHA256 string `json:"payloadSha256"`
	PermitID      string `json:"permitId,omitempty"`
	ReservationID string `json:"reservationId"`
	LeaseEpoch    uint64 `json:"leaseEpoch"`
	Allowed       bool   `json:"allowed"`
}

type SchedulingLedger struct {
	Batches      []SchedulingBatchRecord       `json:"batches,omitempty"`
	Reservations []SchedulingReservationRecord `json:"reservations,omitempty"`
	Permits      []SchedulingPermitRecord      `json:"permits,omitempty"`
}

func validSchedulingReservation(value domainscheduling.Reservation) bool {
	return stableIdentity(value.ID, 128) && stableIdentity(string(value.TaskID), 128) &&
		stableIdentity(string(value.Workspace), 128) && value.TaskVersion > 0 && value.LeaseEpoch > 0 &&
		slices.Contains([]domainscheduling.WorkClass{domainscheduling.WorkNewRun, domainscheduling.WorkActiveRunProgression}, value.WorkClass)
}

func ValidSchedulingLedger(value SchedulingLedger) bool {
	if len(value.Batches) > MaximumSchedulingLedgerRecords || len(value.Reservations) > MaximumSchedulingLedgerRecords ||
		len(value.Permits) > MaximumSchedulingLedgerRecords {
		return false
	}
	reservations := make(map[string]SchedulingReservationRecord, len(value.Reservations))
	for _, record := range value.Reservations {
		if !validSchedulingReservation(record.Reservation) || record.PermitID != "" && !validHash(record.PermitID) {
			return false
		}
		if _, duplicate := reservations[record.Reservation.ID]; duplicate {
			return false
		}
		reservations[record.Reservation.ID] = record
	}
	batches := map[string]struct{}{}
	for _, batch := range value.Batches {
		if !stableIdentity(batch.ID, 128) || !validHash(batch.PayloadSHA256) || batch.Outcome != "applied" ||
			batch.SnapshotVersion == 0 || batch.LeaseEpoch == 0 || len(batch.ReservationIDs) == 0 ||
			len(batch.ReservationIDs) > MaximumSchedulingLedgerRecords {
			return false
		}
		if _, duplicate := batches[batch.ID]; duplicate {
			return false
		}
		batches[batch.ID] = struct{}{}
		seen := map[string]struct{}{}
		for _, id := range batch.ReservationIDs {
			reservation, present := reservations[id]
			if !present || reservation.Reservation.LeaseEpoch != batch.LeaseEpoch {
				return false
			}
			if _, duplicate := seen[id]; duplicate {
				return false
			}
			seen[id] = struct{}{}
		}
	}
	permits := map[string]struct{}{}
	for _, permit := range value.Permits {
		reservation, present := reservations[permit.ReservationID]
		if !stableIdentity(permit.RequestID, 128) || !validHash(permit.PayloadSHA256) || !present || permit.LeaseEpoch == 0 ||
			permit.LeaseEpoch != reservation.Reservation.LeaseEpoch || permit.Allowed != (permit.PermitID != "") ||
			permit.PermitID != "" && !validHash(permit.PermitID) {
			return false
		}
		if _, duplicate := permits[permit.RequestID]; duplicate {
			return false
		}
		permits[permit.RequestID] = struct{}{}
		if permit.Allowed && reservation.PermitID != permit.PermitID {
			return false
		}
	}
	return true
}
