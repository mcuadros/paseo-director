// SPDX-License-Identifier: Apache-2.0

// Package scheduling defines the persistence boundary for atomic scheduler
// reservations and one-use launch authorization. Stores compare immutable
// facts; they do not order Tasks or choose policy.
package scheduling

import (
	"context"
	"errors"

	domainscheduling "github.com/mcuadros/director-engine/domain/scheduling"
)

var (
	ErrInvalidRequest      = errors.New("scheduler store request is invalid")
	ErrIdempotencyConflict = errors.New("scheduler idempotency key was reused with different payload")
	ErrReservationNotFound = errors.New("scheduler reservation was not found")
)

type ReserveOutcome string

const (
	ReserveApplied         ReserveOutcome = "applied"
	ReserveVersionConflict ReserveOutcome = "version_conflict"
	ReserveLeaseLost       ReserveOutcome = "lease_lost"
)

type ReservationBatch struct {
	ID                      string                         `json:"id"`
	ProjectID               string                         `json:"projectId"`
	ExpectedSnapshotVersion uint64                         `json:"expectedSnapshotVersion"`
	FactsHash               string                         `json:"factsHash"`
	LeaseEpoch              uint64                         `json:"leaseEpoch"`
	Reservations            []domainscheduling.Reservation `json:"reservations"`
}

type ReserveResult struct {
	Outcome         ReserveOutcome `json:"outcome"`
	SnapshotVersion uint64         `json:"snapshotVersion"`
	Replay          bool           `json:"replay"`
}

type AuthorizeRequest struct {
	RequestID     string `json:"requestId"`
	ProjectID     string `json:"projectId"`
	ReservationID string `json:"reservationId"`
	LeaseEpoch    uint64 `json:"leaseEpoch"`
}

type Permit struct {
	ID            string `json:"id,omitempty"`
	ReservationID string `json:"reservationId"`
	LeaseEpoch    uint64 `json:"leaseEpoch"`
	Allowed       bool   `json:"allowed"`
	Replay        bool   `json:"replay"`
}

type Store interface {
	Snapshot(context.Context, string) (domainscheduling.Snapshot, error)
	CompareAndReserve(context.Context, ReservationBatch) (ReserveResult, error)
	AuthorizeLaunch(context.Context, AuthorizeRequest) (Permit, error)
}
