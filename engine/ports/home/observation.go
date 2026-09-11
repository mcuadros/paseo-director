// SPDX-License-Identifier: Apache-2.0

// Package home defines the policy-free operational facts consumed by the
// engine-owned Director Home projection.
package home

import "context"

type SyncStreamState string

const (
	SyncCurrent       SyncStreamState = "current"
	SyncFailed        SyncStreamState = "failed"
	SyncNotConfigured SyncStreamState = "not_configured"
	SyncUnavailable   SyncStreamState = "unavailable"
	SyncStale         SyncStreamState = "stale"
)

type WorkspaceObservation struct {
	Health           string
	PaseoWorkspaceID string
}

type OperationAvailability struct {
	Sync      bool
	Reconcile bool
	Doctor    bool
	Repair    bool
	Control   bool
}

type ProjectObservation struct {
	GitSync              SyncStreamState
	TaskStoreSync        SyncStreamState
	SyncObservedAtMillis int64
	SyncMaximumAgeMillis int64
	OrganizerWorkspaceID string
	BoardWorkspaceID     string
	Workspaces           map[string]WorkspaceObservation
	Operations           OperationAvailability
}

type HostObservation struct {
	HostID           string
	Label            string
	InstanceID       string
	State            string
	ObservedAtMillis int64
	MaximumAgeMillis int64
	Projects         map[string]ProjectObservation
}

// OperationalSource returns one observation for the requested exact host. It
// must never reinterpret a Project ID as a cross-host identity.
type OperationalSource interface {
	Observe(context.Context, string, []string) (HostObservation, error)
}
