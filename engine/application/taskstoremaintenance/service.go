// SPDX-License-Identifier: Apache-2.0

// Package taskstoremaintenance coordinates durable TaskStore maintenance.
// Every external handoff is preceded by a sealed CAS transition and every
// uncertain response is reconciled before the bounded retry budget is used.
package taskstoremaintenance

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"strconv"
	"sync"

	domainmaintenance "github.com/mcuadros/director-engine/domain/taskstoremaintenance"
	maintenanceport "github.com/mcuadros/director-engine/ports/taskstoremaintenance"
)

var (
	ErrInvalidCommand       = errors.New("TaskStore maintenance command is invalid")
	ErrInvalidState         = errors.New("TaskStore maintenance state is invalid")
	ErrConcurrentTransition = errors.New("TaskStore maintenance transition lost a CAS race")
	ErrExternalUnavailable  = errors.New("TaskStore maintenance external observation is unavailable")
	ErrNeedsYou             = errors.New("TaskStore maintenance requires human attention")
)

type Command struct {
	ID        string
	NowMillis int64
}

type MigrationCommand struct {
	ID          string
	FromVersion int
	ToVersion   int
	NowMillis   int64
}

type Result struct {
	State      domainmaintenance.State
	Progressed bool
	Disk       domainmaintenance.DiskDecision
}

type Service struct {
	store   maintenanceport.StateStore
	backend maintenanceport.Backend
	gate    sync.Mutex
}

func NewService(store maintenanceport.StateStore, backend maintenanceport.Backend) (*Service, error) {
	if store == nil || backend == nil {
		return nil, ErrInvalidCommand
	}
	return &Service{store: store, backend: backend}, nil
}

func stableID(prefix string, values ...string) string {
	digest := sha256.New()
	for _, value := range values {
		_, _ = digest.Write([]byte{byte(len(value) >> 24), byte(len(value) >> 16), byte(len(value) >> 8), byte(len(value))})
		_, _ = digest.Write([]byte(value))
	}
	return prefix + "-" + hex.EncodeToString(digest.Sum(nil)[:16])
}

func (service *Service) load(ctx context.Context) (domainmaintenance.State, error) {
	state, err := service.store.Load(ctx)
	if err != nil {
		if ctx.Err() != nil {
			return domainmaintenance.State{}, ctx.Err()
		}
		return domainmaintenance.State{}, ErrInvalidState
	}
	if !domainmaintenance.ValidState(state) {
		return domainmaintenance.State{}, ErrInvalidState
	}
	return state, nil
}

func (service *Service) save(ctx context.Context, prior domainmaintenance.State, next domainmaintenance.State) (domainmaintenance.State, error) {
	next.Revision = prior.Revision + 1
	next = domainmaintenance.SealState(next)
	if !domainmaintenance.ValidState(next) {
		return domainmaintenance.State{}, ErrInvalidState
	}
	if err := service.store.CompareAndSwap(ctx, prior.Revision, next); err != nil {
		return domainmaintenance.State{}, ErrConcurrentTransition
	}
	return next, nil
}

func replaceBackup(state *domainmaintenance.State, backup domainmaintenance.Backup) bool {
	index := domainmaintenance.BackupIndex(*state, backup.ID)
	if index < 0 {
		if len(state.Backups) == domainmaintenance.MaximumRecords {
			remove := -1
			for candidate := range state.Backups {
				if state.Backups[candidate].Phase == domainmaintenance.BackupExpired &&
					(state.Migration == nil || state.Migration.BackupID != state.Backups[candidate].ID) {
					remove = candidate
					break
				}
			}
			if remove < 0 {
				return false
			}
			state.Backups = append(state.Backups[:remove], state.Backups[remove+1:]...)
		}
		state.Backups = append(state.Backups, backup)
		return true
	}
	state.Backups[index] = backup
	return true
}

func validCommand(command Command) bool {
	return command.ID != "" && command.NowMillis >= 0
}

func externalFailure(ctx context.Context) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}
	return ErrExternalUnavailable
}

func backupMatches(observation maintenanceport.BackupObservation, backup domainmaintenance.Backup) bool {
	return observation.Present && observation.Exact && domainmaintenance.ValidStoreObservation(observation.Source) &&
		observation.Source.SchemaVersion == backup.SourceSchemaVersion && observation.Source.Fingerprint == backup.SourceFingerprint
}

func (service *Service) reconcileBackup(ctx context.Context, state domainmaintenance.State, backupID string, nowMillis int64) (domainmaintenance.State, error) {
	for steps := 0; steps < 12; steps++ {
		index := domainmaintenance.BackupIndex(state, backupID)
		if index < 0 {
			return state, ErrInvalidState
		}
		backup := state.Backups[index]
		switch backup.Phase {
		case domainmaintenance.BackupValidated:
			return state, nil
		case domainmaintenance.BackupNeedsYou:
			return state, ErrNeedsYou
		case domainmaintenance.BackupIntentRecorded:
			backup.Phase = domainmaintenance.BackupDispatching
			backup.Attempt++
			next := state
			replaceBackup(&next, backup)
			var err error
			state, err = service.save(ctx, state, next)
			if err != nil {
				return state, err
			}
			_, handoffErr := service.backend.CreateBackup(ctx, backup)
			observation, observationErr := service.backend.ObserveBackup(ctx, backup.ID)
			if observationErr != nil {
				_ = handoffErr
				return state, externalFailure(ctx)
			}
			if backupMatches(observation, backup) {
				backup.Phase = domainmaintenance.BackupValidationRequired
			} else if observation.PriorDispatcherAbsent && backup.Attempt < 2 {
				backup.Phase = domainmaintenance.BackupIntentRecorded
			} else {
				backup.Phase = domainmaintenance.BackupNeedsYou
				backup.NeedsYouCode = "backup_handoff_ambiguous"
			}
			next = state
			replaceBackup(&next, backup)
			state, err = service.save(ctx, state, next)
			if err != nil {
				return state, err
			}
		case domainmaintenance.BackupDispatching:
			observation, err := service.backend.ObserveBackup(ctx, backup.ID)
			if err != nil {
				return state, externalFailure(ctx)
			}
			if backupMatches(observation, backup) {
				backup.Phase = domainmaintenance.BackupValidationRequired
			} else if observation.PriorDispatcherAbsent && backup.Attempt < 2 {
				backup.Phase = domainmaintenance.BackupIntentRecorded
			} else {
				backup.Phase = domainmaintenance.BackupNeedsYou
				backup.NeedsYouCode = "backup_handoff_ambiguous"
			}
			next := state
			replaceBackup(&next, backup)
			state, err = service.save(ctx, state, next)
			if err != nil {
				return state, err
			}
		case domainmaintenance.BackupValidationRequired:
			validated, err := service.backend.ValidateBackup(ctx, backup)
			if err != nil || !domainmaintenance.ValidStoreObservation(validated.Source) ||
				validated.Source.SchemaVersion != backup.SourceSchemaVersion || validated.Source.Fingerprint != backup.SourceFingerprint ||
				validated.RestoredFingerprint != backup.SourceFingerprint {
				backup.Phase = domainmaintenance.BackupNeedsYou
				backup.NeedsYouCode = "backup_restore_validation_failed"
			} else {
				backup.Phase = domainmaintenance.BackupValidated
				backup.ValidatedAtMillis = nowMillis
				backup.RestoreFingerprint = validated.RestoredFingerprint
			}
			next := state
			replaceBackup(&next, backup)
			state, err = service.save(ctx, state, next)
			if err != nil {
				return state, err
			}
		default:
			return state, ErrInvalidState
		}
	}
	return state, ErrInvalidState
}

func (service *Service) beginBackup(ctx context.Context, state domainmaintenance.State, command Command, purpose domainmaintenance.BackupPurpose, migrationID string, source domainmaintenance.StoreObservation) (domainmaintenance.State, string, error) {
	if !domainmaintenance.ValidStoreObservation(source) {
		return state, "", ErrInvalidState
	}
	id := stableID("backup", state.StoreID, string(purpose), migrationID,
		strconv.FormatInt(command.NowMillis/domainmaintenance.DailyIntervalMillis, 10), source.Fingerprint)
	if index := domainmaintenance.BackupIndex(state, id); index >= 0 {
		return state, id, nil
	}
	backup := domainmaintenance.Backup{
		ID: id, Purpose: purpose, MigrationID: migrationID, SourceSchemaVersion: source.SchemaVersion,
		SourceFingerprint: source.Fingerprint, Phase: domainmaintenance.BackupIntentRecorded,
		CreatedAtMillis: command.NowMillis, RetentionUntil: command.NowMillis + domainmaintenance.RetentionMillis,
	}
	next := state
	if !replaceBackup(&next, backup) {
		return state, "", ErrInvalidState
	}
	updated, err := service.save(ctx, state, next)
	return updated, id, err
}

// ReconcileDaily creates at most one exact daily backup for the current
// 24-hour bucket and proves it by restoring into a fresh database.
func (service *Service) ReconcileDaily(ctx context.Context, command Command) (Result, error) {
	if service == nil || !validCommand(command) {
		return Result{}, ErrInvalidCommand
	}
	service.gate.Lock()
	defer service.gate.Unlock()
	state, err := service.load(ctx)
	if err != nil {
		return Result{}, err
	}
	if !domainmaintenance.DailyBackupDue(state, command.NowMillis) {
		return Result{State: state}, nil
	}
	source, err := service.backend.ObserveStore(ctx)
	if err != nil || !domainmaintenance.ValidStoreObservation(source) {
		return Result{State: state}, ErrExternalUnavailable
	}
	state, id, err := service.beginBackup(ctx, state, command, domainmaintenance.BackupDaily, "", source)
	if err != nil {
		return Result{State: state}, err
	}
	state, err = service.reconcileBackup(ctx, state, id, command.NowMillis)
	return Result{State: state, Progressed: true}, err
}

func migrationBackup(state domainmaintenance.State, migration domainmaintenance.Migration) (domainmaintenance.Backup, bool) {
	index := domainmaintenance.BackupIndex(state, migration.BackupID)
	return func() (domainmaintenance.Backup, bool) {
		if index < 0 {
			return domainmaintenance.Backup{}, false
		}
		return state.Backups[index], true
	}()
}

// Migrate advances exactly one schema version. A migration effect is
// structurally unreachable until Projects are paused and the exact paused
// source has passed a fresh-restore backup validation.
func (service *Service) Migrate(ctx context.Context, command MigrationCommand) (Result, error) {
	if service == nil || command.ID == "" || command.FromVersion <= 0 || command.ToVersion != command.FromVersion+1 || command.NowMillis < 0 {
		return Result{}, ErrInvalidCommand
	}
	service.gate.Lock()
	defer service.gate.Unlock()
	state, err := service.load(ctx)
	if err != nil {
		return Result{}, err
	}
	if state.Migration == nil {
		next := state
		next.Migration = &domainmaintenance.Migration{ID: command.ID, FromVersion: command.FromVersion, ToVersion: command.ToVersion,
			Phase: domainmaintenance.MigrationIntentRecorded, StartedAtMillis: command.NowMillis}
		state, err = service.save(ctx, state, next)
		if err != nil {
			return Result{State: state}, err
		}
	} else if state.Migration.ID != command.ID || state.Migration.FromVersion != command.FromVersion || state.Migration.ToVersion != command.ToVersion {
		return Result{State: state}, ErrInvalidCommand
	}
	for steps := 0; steps < 24; steps++ {
		migration := *state.Migration
		switch migration.Phase {
		case domainmaintenance.MigrationComplete:
			return Result{State: state, Progressed: true}, nil
		case domainmaintenance.MigrationNeedsYou:
			return Result{State: state, Progressed: true}, ErrNeedsYou
		case domainmaintenance.MigrationIntentRecorded:
			projects, planErr := service.backend.PlanProjectPause(ctx)
			if planErr != nil || len(projects) > domainmaintenance.MaximumProjects {
				return Result{State: state}, ErrExternalUnavailable
			}
			migration.Projects, migration.Phase = projects, domainmaintenance.MigrationPauseRequired
			next := state
			next.Migration = &migration
			state, err = service.save(ctx, state, next)
			if err != nil {
				return Result{State: state}, err
			}
		case domainmaintenance.MigrationPauseRequired:
			projects, handoffErr := service.backend.PauseProjects(ctx, migration)
			_ = handoffErr
			observed, paused, observationErr := service.backend.ObserveProjectsPaused(ctx, migration)
			if observationErr != nil {
				return Result{State: state}, ErrExternalUnavailable
			}
			if !paused || len(observed) != len(migration.Projects) {
				migration.Phase, migration.NeedsYouCode = domainmaintenance.MigrationNeedsYou, "migration_pause_unproven"
			} else {
				if len(projects) != 0 && len(projects) != len(migration.Projects) {
					return Result{State: state}, ErrInvalidState
				}
				migration.Phase = domainmaintenance.MigrationBackupRequired
			}
			next := state
			next.Migration = &migration
			state, err = service.save(ctx, state, next)
			if err != nil {
				return Result{State: state}, err
			}
		case domainmaintenance.MigrationBackupRequired:
			source, observeErr := service.backend.ObserveStore(ctx)
			if observeErr != nil || !domainmaintenance.ValidStoreObservation(source) || source.SchemaVersion != migration.FromVersion {
				return Result{State: state}, ErrExternalUnavailable
			}
			backupCommand := Command{ID: command.ID, NowMillis: command.NowMillis}
			state, migration.BackupID, err = service.beginBackup(ctx, state, backupCommand, domainmaintenance.BackupMigration, migration.ID, source)
			if err != nil {
				return Result{State: state}, err
			}
			migration.SourceFingerprint = source.Fingerprint
			next := state
			next.Migration = &migration
			state, err = service.save(ctx, state, next)
			if err != nil {
				return Result{State: state}, err
			}
			state, err = service.reconcileBackup(ctx, state, migration.BackupID, command.NowMillis)
			if err != nil {
				migration = *state.Migration
				migration.Phase, migration.NeedsYouCode = domainmaintenance.MigrationNeedsYou, "migration_backup_validation_failed"
				next = state
				next.Migration = &migration
				state, saveErr := service.save(ctx, state, next)
				if saveErr != nil {
					return Result{State: state}, saveErr
				}
				return Result{State: state, Progressed: true}, ErrNeedsYou
			}
			migration = *state.Migration
			migration.Phase = domainmaintenance.MigrationReady
			next = state
			next.Migration = &migration
			state, err = service.save(ctx, state, next)
			if err != nil {
				return Result{State: state}, err
			}
		case domainmaintenance.MigrationReady:
			observation, observeErr := service.backend.ObserveStore(ctx)
			if observeErr != nil {
				return Result{State: state}, ErrExternalUnavailable
			}
			if domainmaintenance.GateMigration(state, migration, observation, command.NowMillis) != domainmaintenance.MigrationGateReady {
				migration.Phase, migration.NeedsYouCode = domainmaintenance.MigrationNeedsYou, "migration_exact_backup_gate_refused"
				next := state
				next.Migration = &migration
				state, err = service.save(ctx, state, next)
				if err != nil {
					return Result{State: state}, err
				}
				return Result{State: state, Progressed: true}, ErrNeedsYou
			}
			migration.Phase, migration.Attempt = domainmaintenance.MigrationDispatching, migration.Attempt+1
			next := state
			next.Migration = &migration
			state, err = service.save(ctx, state, next)
			if err != nil {
				return Result{State: state}, err
			}
			_, handoffErr := service.backend.ApplyMigration(ctx, migration)
			_ = handoffErr
			observed, observationErr := service.backend.ObserveStore(ctx)
			if observationErr != nil {
				return Result{State: state}, ErrExternalUnavailable
			}
			migration = *state.Migration
			switch {
			case domainmaintenance.ValidStoreObservation(observed) && observed.SchemaVersion == migration.ToVersion:
				migration.Phase, migration.MigratedFingerprint = domainmaintenance.MigrationResumeRequired, observed.Fingerprint
			case domainmaintenance.ValidStoreObservation(observed) && observed.SchemaVersion == migration.FromVersion && observed.Fingerprint == migration.SourceFingerprint && migration.Attempt < 2:
				migration.Phase = domainmaintenance.MigrationReady
			default:
				migration.Phase = domainmaintenance.MigrationRestoreRequired
			}
			next = state
			next.Migration = &migration
			state, err = service.save(ctx, state, next)
			if err != nil {
				return Result{State: state}, err
			}
		case domainmaintenance.MigrationDispatching:
			observed, observeErr := service.backend.ObserveStore(ctx)
			if observeErr != nil {
				return Result{State: state}, ErrExternalUnavailable
			}
			if domainmaintenance.ValidStoreObservation(observed) && observed.SchemaVersion == migration.ToVersion {
				migration.Phase, migration.MigratedFingerprint = domainmaintenance.MigrationResumeRequired, observed.Fingerprint
			} else if domainmaintenance.ValidStoreObservation(observed) && observed.SchemaVersion == migration.FromVersion && observed.Fingerprint == migration.SourceFingerprint && migration.Attempt < 2 {
				migration.Phase = domainmaintenance.MigrationReady
			} else {
				migration.Phase = domainmaintenance.MigrationRestoreRequired
			}
			next := state
			next.Migration = &migration
			state, err = service.save(ctx, state, next)
			if err != nil {
				return Result{State: state}, err
			}
		case domainmaintenance.MigrationRestoreRequired:
			backup, ok := migrationBackup(state, migration)
			if !ok {
				return Result{State: state}, ErrInvalidState
			}
			restored, restoreErr := service.backend.RestoreBackup(ctx, migration, backup)
			migration.Phase, migration.NeedsYouCode = domainmaintenance.MigrationNeedsYou, "migration_failed_projects_remain_paused"
			if restoreErr == nil && restored.Fingerprint == migration.SourceFingerprint {
				migration.RestoreFingerprint = restored.Fingerprint
			}
			next := state
			next.Migration = &migration
			state, err = service.save(ctx, state, next)
			if err != nil {
				return Result{State: state}, err
			}
			return Result{State: state, Progressed: true}, ErrNeedsYou
		case domainmaintenance.MigrationResumeRequired:
			handoffErr := service.backend.ResumeProjects(ctx, migration)
			_ = handoffErr
			resumed, observationErr := service.backend.ObserveProjectsResumed(ctx, migration)
			if observationErr != nil {
				return Result{State: state}, ErrExternalUnavailable
			}
			if !resumed {
				migration.Phase, migration.NeedsYouCode = domainmaintenance.MigrationNeedsYou, "migration_resume_unproven"
			} else {
				migration.Phase, migration.CompletedAtMillis = domainmaintenance.MigrationComplete, command.NowMillis
			}
			next := state
			next.Migration = &migration
			state, err = service.save(ctx, state, next)
			if err != nil {
				return Result{State: state}, err
			}
		default:
			return Result{State: state}, ErrInvalidState
		}
	}
	return Result{State: state}, ErrInvalidState
}

// ExpireBackups removes only already-validated backup artifacts after the
// fixed seven-day deadline. Unknown or ambiguous artifacts fail closed.
func (service *Service) ExpireBackups(ctx context.Context, command Command) (Result, error) {
	if service == nil || !validCommand(command) {
		return Result{}, ErrInvalidCommand
	}
	service.gate.Lock()
	defer service.gate.Unlock()
	state, err := service.load(ctx)
	if err != nil {
		return Result{}, err
	}
	progressed := false
	for index := range state.Backups {
		backup := state.Backups[index]
		if (backup.Phase != domainmaintenance.BackupValidated && backup.Phase != domainmaintenance.BackupExpiryDispatching) ||
			command.NowMillis < backup.RetentionUntil {
			continue
		}
		if backup.Phase == domainmaintenance.BackupValidated {
			backup.Phase = domainmaintenance.BackupExpiryDispatching
			next := state
			replaceBackup(&next, backup)
			state, err = service.save(ctx, state, next)
			if err != nil {
				return Result{State: state}, err
			}
		}
		handoffErr := service.backend.ExpireBackup(ctx, backup)
		_ = handoffErr
		observed, observationErr := service.backend.ObserveBackup(ctx, backup.ID)
		if observationErr != nil {
			return Result{State: state}, ErrExternalUnavailable
		}
		if observed.Present || !observed.Exact {
			return Result{State: state}, ErrNeedsYou
		}
		backup.Phase = domainmaintenance.BackupExpired
		next := state
		replaceBackup(&next, backup)
		state, err = service.save(ctx, state, next)
		if err != nil {
			return Result{State: state}, err
		}
		progressed = true
	}
	return Result{State: state, Progressed: progressed}, nil
}

// ReconcileDisk applies the cleanup-first 10-percent launch gate. The only
// cleanup effects in this service are expired-backup deletion and bounded log
// compaction; it cannot address a worktree, recovery artifact, or Git ref.
func (service *Service) ReconcileDisk(ctx context.Context, command Command) (Result, error) {
	if service == nil || !validCommand(command) {
		return Result{}, ErrInvalidCommand
	}
	first, err := service.backend.ObserveDisk(ctx)
	if err != nil {
		return Result{}, externalFailure(ctx)
	}
	decision := domainmaintenance.ReduceDisk(first, command.NowMillis, false)
	if decision.Phase != domainmaintenance.DiskCleanupRequired {
		state, loadErr := service.load(ctx)
		return Result{State: state, Disk: decision}, loadErr
	}
	if _, err := service.ExpireBackups(ctx, command); err != nil {
		return Result{Disk: decision}, err
	}
	if err := service.backend.CompactLogs(ctx, command.NowMillis); err != nil {
		return Result{Disk: decision}, externalFailure(ctx)
	}
	second, err := service.backend.ObserveDisk(ctx)
	if err != nil {
		return Result{Disk: decision}, externalFailure(ctx)
	}
	state, loadErr := service.load(ctx)
	return Result{State: state, Progressed: true, Disk: domainmaintenance.ReduceDisk(second, command.NowMillis, true)}, loadErr
}
