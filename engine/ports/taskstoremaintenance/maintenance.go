// SPDX-License-Identifier: Apache-2.0

// Package taskstoremaintenance defines effect-only persistence, Dolt, log,
// and disk boundaries for engine-owned maintenance orchestration.
package taskstoremaintenance

import (
	"context"

	"github.com/mcuadros/director-engine/domain/taskstoremaintenance"
)

type BackupObservation struct {
	Present               bool
	Exact                 bool
	PriorDispatcherAbsent bool
	Source                taskstoremaintenance.StoreObservation
}

type BackupResult struct {
	Source taskstoremaintenance.StoreObservation
}

type ValidationResult struct {
	Source              taskstoremaintenance.StoreObservation
	RestoredFingerprint string
}

type MigrationResult struct {
	Store taskstoremaintenance.StoreObservation
}

type RestoreResult struct {
	Fingerprint string
}

type StateStore interface {
	Load(context.Context) (taskstoremaintenance.State, error)
	CompareAndSwap(context.Context, uint64, taskstoremaintenance.State) error
}

// Backend performs one already-authorized exact effect and returns bounded
// observations. It owns no due-time, retry, migration-gate, resume, retention,
// disk-pressure, or lifecycle decision.
type Backend interface {
	ObserveStore(context.Context) (taskstoremaintenance.StoreObservation, error)
	ObserveBackup(context.Context, string) (BackupObservation, error)
	CreateBackup(context.Context, taskstoremaintenance.Backup) (BackupResult, error)
	ValidateBackup(context.Context, taskstoremaintenance.Backup) (ValidationResult, error)
	PlanProjectPause(context.Context) ([]taskstoremaintenance.ProjectBinding, error)
	PauseProjects(context.Context, taskstoremaintenance.Migration) ([]taskstoremaintenance.ProjectBinding, error)
	ObserveProjectsPaused(context.Context, taskstoremaintenance.Migration) ([]taskstoremaintenance.ProjectBinding, bool, error)
	ApplyMigration(context.Context, taskstoremaintenance.Migration) (MigrationResult, error)
	RestoreBackup(context.Context, taskstoremaintenance.Migration, taskstoremaintenance.Backup) (RestoreResult, error)
	ResumeProjects(context.Context, taskstoremaintenance.Migration) error
	ObserveProjectsResumed(context.Context, taskstoremaintenance.Migration) (bool, error)
	ExpireBackup(context.Context, taskstoremaintenance.Backup) error
	CompactLogs(context.Context, int64) error
	ObserveDisk(context.Context) (taskstoremaintenance.DiskObservation, error)
}
