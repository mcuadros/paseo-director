// SPDX-License-Identifier: Apache-2.0

package taskstoremaintenance

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"

	domainmaintenance "github.com/mcuadros/director-engine/domain/taskstoremaintenance"
	maintenanceport "github.com/mcuadros/director-engine/ports/taskstoremaintenance"
)

func testDigest(character string) string { return strings.Repeat(character, 64) }

type memoryStateStore struct {
	mu    sync.Mutex
	state domainmaintenance.State
}

func newMemoryStateStore() *memoryStateStore {
	return &memoryStateStore{state: domainmaintenance.NewState("store-1", testDigest("d"))}
}

func (store *memoryStateStore) Load(context.Context) (domainmaintenance.State, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	copy := store.state
	copy.Backups = append([]domainmaintenance.Backup(nil), store.state.Backups...)
	if store.state.Migration != nil {
		migration := *store.state.Migration
		migration.Projects = append([]domainmaintenance.ProjectBinding(nil), migration.Projects...)
		copy.Migration = &migration
	}
	return copy, nil
}

func (store *memoryStateStore) CompareAndSwap(_ context.Context, expected uint64, next domainmaintenance.State) error {
	store.mu.Lock()
	defer store.mu.Unlock()
	if store.state.Revision != expected {
		return errors.New("private/cas/path must not escape")
	}
	store.state = next
	return nil
}

type fakeBackend struct {
	mu                                                                                       sync.Mutex
	store                                                                                    domainmaintenance.StoreObservation
	pausedFingerprint                                                                        string
	backups                                                                                  map[string]maintenanceport.BackupObservation
	projects                                                                                 []domainmaintenance.ProjectBinding
	paused, resumed                                                                          bool
	createCount, validateCount, applyCount, restoreCount, expireCount, logCount              int
	loseCreateResponse, failBackupObservationOnce, mismatchValidation, changeAfterValidation bool
	loseApplyResponse, partialApply, loseExpireResponse                                      bool
	disks                                                                                    []domainmaintenance.DiskObservation
}

func newFakeBackend() *fakeBackend {
	return &fakeBackend{
		store:             domainmaintenance.StoreObservation{SchemaVersion: 1, Fingerprint: testDigest("a"), Exact: true},
		pausedFingerprint: testDigest("b"), backups: map[string]maintenanceport.BackupObservation{},
		projects: []domainmaintenance.ProjectBinding{{ID: "project-1", OriginalState: "active", OriginalVersion: 2, PausedVersion: 3}},
	}
}

func (backend *fakeBackend) ObserveStore(context.Context) (domainmaintenance.StoreObservation, error) {
	backend.mu.Lock()
	defer backend.mu.Unlock()
	return backend.store, nil
}

func (backend *fakeBackend) ObserveBackup(_ context.Context, id string) (maintenanceport.BackupObservation, error) {
	backend.mu.Lock()
	defer backend.mu.Unlock()
	if backend.failBackupObservationOnce {
		backend.failBackupObservationOnce = false
		return maintenanceport.BackupObservation{}, errors.New("secret path observation")
	}
	if value, ok := backend.backups[id]; ok {
		return value, nil
	}
	return maintenanceport.BackupObservation{Exact: true, PriorDispatcherAbsent: true}, nil
}

func (backend *fakeBackend) CreateBackup(_ context.Context, backup domainmaintenance.Backup) (maintenanceport.BackupResult, error) {
	backend.mu.Lock()
	defer backend.mu.Unlock()
	backend.createCount++
	source := domainmaintenance.StoreObservation{SchemaVersion: backup.SourceSchemaVersion, Fingerprint: backup.SourceFingerprint, Exact: true}
	backend.backups[backup.ID] = maintenanceport.BackupObservation{Present: true, Exact: true, Source: source}
	if backend.loseCreateResponse {
		return maintenanceport.BackupResult{}, errors.New("credential in lost response")
	}
	return maintenanceport.BackupResult{Source: source}, nil
}

func (backend *fakeBackend) ValidateBackup(_ context.Context, backup domainmaintenance.Backup) (maintenanceport.ValidationResult, error) {
	backend.mu.Lock()
	defer backend.mu.Unlock()
	backend.validateCount++
	restored := backup.SourceFingerprint
	if backend.mismatchValidation {
		restored = testDigest("f")
	}
	result := maintenanceport.ValidationResult{Source: domainmaintenance.StoreObservation{SchemaVersion: backup.SourceSchemaVersion,
		Fingerprint: backup.SourceFingerprint, Exact: true}, RestoredFingerprint: restored}
	if backend.changeAfterValidation {
		backend.store.Fingerprint = testDigest("e")
	}
	return result, nil
}

func (backend *fakeBackend) PlanProjectPause(context.Context) ([]domainmaintenance.ProjectBinding, error) {
	backend.mu.Lock()
	defer backend.mu.Unlock()
	return append([]domainmaintenance.ProjectBinding(nil), backend.projects...), nil
}

func (backend *fakeBackend) PauseProjects(_ context.Context, migration domainmaintenance.Migration) ([]domainmaintenance.ProjectBinding, error) {
	backend.mu.Lock()
	defer backend.mu.Unlock()
	backend.paused = true
	backend.store.Fingerprint = backend.pausedFingerprint
	return append([]domainmaintenance.ProjectBinding(nil), migration.Projects...), nil
}

func (backend *fakeBackend) ObserveProjectsPaused(_ context.Context, migration domainmaintenance.Migration) ([]domainmaintenance.ProjectBinding, bool, error) {
	backend.mu.Lock()
	defer backend.mu.Unlock()
	return append([]domainmaintenance.ProjectBinding(nil), migration.Projects...), backend.paused, nil
}

func (backend *fakeBackend) ApplyMigration(_ context.Context, migration domainmaintenance.Migration) (maintenanceport.MigrationResult, error) {
	backend.mu.Lock()
	defer backend.mu.Unlock()
	backend.applyCount++
	if backend.partialApply {
		backend.store = domainmaintenance.StoreObservation{SchemaVersion: 1, Fingerprint: testDigest("c"), Exact: false}
		return maintenanceport.MigrationResult{}, errors.New("partial DDL")
	}
	backend.store = domainmaintenance.StoreObservation{SchemaVersion: migration.ToVersion, Fingerprint: testDigest("c"), Exact: true}
	if backend.loseApplyResponse {
		return maintenanceport.MigrationResult{}, errors.New("lost response")
	}
	return maintenanceport.MigrationResult{Store: backend.store}, nil
}

func (backend *fakeBackend) RestoreBackup(_ context.Context, migration domainmaintenance.Migration, _ domainmaintenance.Backup) (maintenanceport.RestoreResult, error) {
	backend.mu.Lock()
	defer backend.mu.Unlock()
	backend.restoreCount++
	return maintenanceport.RestoreResult{Fingerprint: migration.SourceFingerprint}, nil
}

func (backend *fakeBackend) ResumeProjects(context.Context, domainmaintenance.Migration) error {
	backend.mu.Lock()
	defer backend.mu.Unlock()
	backend.resumed = true
	return nil
}

func (backend *fakeBackend) ObserveProjectsResumed(context.Context, domainmaintenance.Migration) (bool, error) {
	backend.mu.Lock()
	defer backend.mu.Unlock()
	return backend.resumed, nil
}

func (backend *fakeBackend) ExpireBackup(_ context.Context, backup domainmaintenance.Backup) error {
	backend.mu.Lock()
	defer backend.mu.Unlock()
	backend.expireCount++
	delete(backend.backups, backup.ID)
	if backend.loseExpireResponse {
		return errors.New("lost response")
	}
	return nil
}

func (backend *fakeBackend) CompactLogs(context.Context, int64) error {
	backend.mu.Lock()
	defer backend.mu.Unlock()
	backend.logCount++
	return nil
}

func (backend *fakeBackend) ObserveDisk(context.Context) (domainmaintenance.DiskObservation, error) {
	backend.mu.Lock()
	defer backend.mu.Unlock()
	if len(backend.disks) == 0 {
		return domainmaintenance.DiskObservation{}, errors.New("secret/private/disk path is unavailable")
	}
	value := backend.disks[0]
	if len(backend.disks) > 1 {
		backend.disks = backend.disks[1:]
	}
	return value, nil
}

func newTestService(t *testing.T, store *memoryStateStore, backend *fakeBackend) *Service {
	t.Helper()
	service, err := NewService(store, backend)
	if err != nil {
		t.Fatal(err)
	}
	return service
}

func TestDailyBackupRecoversLostResponseAcrossServiceReopen(t *testing.T) {
	store, backend := newMemoryStateStore(), newFakeBackend()
	backend.loseCreateResponse, backend.failBackupObservationOnce = true, true
	first := newTestService(t, store, backend)
	_, err := first.ReconcileDaily(context.Background(), Command{ID: "daily-1", NowMillis: 10_000})
	if !errors.Is(err, ErrExternalUnavailable) || strings.Contains(err.Error(), "secret") || strings.Contains(err.Error(), "path") {
		t.Fatalf("first response = %v", err)
	}
	state, _ := store.Load(context.Background())
	if len(state.Backups) != 1 || state.Backups[0].Phase != domainmaintenance.BackupDispatching {
		t.Fatalf("interrupted state = %#v", state)
	}
	second := newTestService(t, store, backend)
	result, err := second.ReconcileDaily(context.Background(), Command{ID: "daily-1", NowMillis: 10_000})
	if err != nil || result.State.Backups[0].Phase != domainmaintenance.BackupValidated || backend.createCount != 1 || backend.validateCount != 1 {
		t.Fatalf("reconciled result=%#v err=%v creates=%d validates=%d", result, err, backend.createCount, backend.validateCount)
	}
	if duplicate, err := second.ReconcileDaily(context.Background(), Command{ID: "daily-duplicate", NowMillis: 10_001}); err != nil || duplicate.Progressed {
		t.Fatalf("duplicate daily backup = %#v, %v", duplicate, err)
	}
}

func TestMigrationRequiresExactValidatedBackupAndLeavesProjectsPausedOnRefusal(t *testing.T) {
	store, backend := newMemoryStateStore(), newFakeBackend()
	backend.changeAfterValidation = true
	service := newTestService(t, store, backend)
	result, err := service.Migrate(context.Background(), MigrationCommand{ID: "migration-1", FromVersion: 1, ToVersion: 2, NowMillis: 10_000})
	if !errors.Is(err, ErrNeedsYou) || result.State.Migration.Phase != domainmaintenance.MigrationNeedsYou ||
		backend.applyCount != 0 || !backend.paused || backend.resumed {
		t.Fatalf("gate refusal result=%#v err=%v backend=%#v", result, err, backend)
	}
}

func TestMigrationAdoptsAppliedDDLAfterResponseLossAndResumes(t *testing.T) {
	store, backend := newMemoryStateStore(), newFakeBackend()
	backend.loseCreateResponse, backend.loseApplyResponse = true, true
	service := newTestService(t, store, backend)
	result, err := service.Migrate(context.Background(), MigrationCommand{ID: "migration-1", FromVersion: 1, ToVersion: 2, NowMillis: 10_000})
	if err != nil || result.State.Migration.Phase != domainmaintenance.MigrationComplete || backend.applyCount != 1 ||
		backend.restoreCount != 0 || !backend.resumed {
		t.Fatalf("migration result=%#v err=%v backend=%#v", result, err, backend)
	}
	if reopened, err := newTestService(t, store, backend).Migrate(context.Background(), MigrationCommand{ID: "migration-1", FromVersion: 1, ToVersion: 2, NowMillis: 10_001}); err != nil || reopened.State.Migration.Phase != domainmaintenance.MigrationComplete || backend.applyCount != 1 {
		t.Fatalf("reopened migration=%#v err=%v applies=%d", reopened, err, backend.applyCount)
	}
}

func TestMigrationReopenAdoptsPersistedDispatchWithoutReapplying(t *testing.T) {
	store, backend := newMemoryStateStore(), newFakeBackend()
	migration := domainmaintenance.Migration{ID: "migration-reopen", FromVersion: 1, ToVersion: 2,
		Phase: domainmaintenance.MigrationDispatching, Attempt: 1, BackupID: "backup-reopen",
		SourceFingerprint: backend.pausedFingerprint, Projects: backend.projects, StartedAtMillis: 10_000}
	backup := domainmaintenance.Backup{ID: migration.BackupID, Purpose: domainmaintenance.BackupMigration,
		MigrationID: migration.ID, SourceSchemaVersion: 1, SourceFingerprint: migration.SourceFingerprint,
		Phase: domainmaintenance.BackupValidated, Attempt: 1, CreatedAtMillis: 10_000,
		RetentionUntil: 10_000 + domainmaintenance.RetentionMillis, ValidatedAtMillis: 10_001,
		RestoreFingerprint: migration.SourceFingerprint}
	store.state.Backups, store.state.Migration = []domainmaintenance.Backup{backup}, &migration
	store.state = domainmaintenance.SealState(store.state)
	backend.paused = true
	backend.store = domainmaintenance.StoreObservation{SchemaVersion: 2, Fingerprint: testDigest("c"), Exact: true}
	result, err := newTestService(t, store, backend).Migrate(context.Background(), MigrationCommand{
		ID: migration.ID, FromVersion: 1, ToVersion: 2, NowMillis: 10_002,
	})
	if err != nil || result.State.Migration.Phase != domainmaintenance.MigrationComplete || backend.applyCount != 0 || !backend.resumed {
		t.Fatalf("reopened dispatch result=%#v err=%v backend=%#v", result, err, backend)
	}
}

func TestPartialMigrationProvesRestoreAndNeverResumes(t *testing.T) {
	store, backend := newMemoryStateStore(), newFakeBackend()
	backend.partialApply = true
	result, err := newTestService(t, store, backend).Migrate(context.Background(), MigrationCommand{ID: "migration-1", FromVersion: 1, ToVersion: 2, NowMillis: 10_000})
	if !errors.Is(err, ErrNeedsYou) || result.State.Migration.Phase != domainmaintenance.MigrationNeedsYou ||
		result.State.Migration.RestoreFingerprint != result.State.Migration.SourceFingerprint || backend.restoreCount != 1 || backend.resumed {
		t.Fatalf("partial migration result=%#v err=%v backend=%#v", result, err, backend)
	}
}

func TestSevenDayExpiryAdoptsLostDeleteResponse(t *testing.T) {
	store, backend := newMemoryStateStore(), newFakeBackend()
	service := newTestService(t, store, backend)
	daily, err := service.ReconcileDaily(context.Background(), Command{ID: "daily-1", NowMillis: 10_000})
	if err != nil {
		t.Fatal(err)
	}
	backend.loseExpireResponse = true
	expired, err := service.ExpireBackups(context.Background(), Command{ID: "expiry-1", NowMillis: 10_000 + domainmaintenance.RetentionMillis})
	if err != nil || !expired.Progressed || expired.State.Backups[0].Phase != domainmaintenance.BackupExpired || backend.expireCount != 1 {
		t.Fatalf("expiry=%#v err=%v daily=%#v", expired, err, daily)
	}
}

func TestSevenDayExpiryReopenAdoptsPersistedDispatch(t *testing.T) {
	store, backend := newMemoryStateStore(), newFakeBackend()
	backup := domainmaintenance.Backup{ID: "backup-expiry-reopen", Purpose: domainmaintenance.BackupDaily,
		SourceSchemaVersion: 1, SourceFingerprint: testDigest("a"), Phase: domainmaintenance.BackupExpiryDispatching,
		Attempt: 1, CreatedAtMillis: 10_000, RetentionUntil: 10_000 + domainmaintenance.RetentionMillis,
		ValidatedAtMillis: 10_001, RestoreFingerprint: testDigest("a")}
	store.state.Backups = []domainmaintenance.Backup{backup}
	store.state = domainmaintenance.SealState(store.state)
	result, err := newTestService(t, store, backend).ExpireBackups(context.Background(), Command{
		ID: "expiry-reopen", NowMillis: backup.RetentionUntil,
	})
	if err != nil || !result.Progressed || result.State.Backups[0].Phase != domainmaintenance.BackupExpired || backend.expireCount != 1 {
		t.Fatalf("reopened expiry=%#v err=%v expires=%d", result, err, backend.expireCount)
	}
}

func TestDiskPressureAttemptsOnlyBoundedRetentionThenDegrades(t *testing.T) {
	store, backend := newMemoryStateStore(), newFakeBackend()
	service := newTestService(t, store, backend)
	if _, err := service.ReconcileDaily(context.Background(), Command{ID: "daily-1", NowMillis: 10_000}); err != nil {
		t.Fatal(err)
	}
	now := int64(10_000 + domainmaintenance.RetentionMillis)
	low := domainmaintenance.DiskObservation{ObservedAtMillis: now, MaximumAgeMillis: 5_000, TotalBytes: 100_000, FreeBytes: 9_000}
	backend.disks = []domainmaintenance.DiskObservation{low, low}
	result, err := service.ReconcileDisk(context.Background(), Command{ID: "disk-1", NowMillis: now})
	if err != nil || result.Disk.Phase != domainmaintenance.DiskDegraded || result.Disk.LaunchAllowed || result.Disk.ConsumeMoreDisk ||
		backend.expireCount != 1 || backend.logCount != 1 || backend.applyCount != 0 || backend.restoreCount != 0 {
		t.Fatalf("disk result=%#v err=%v backend=%#v", result, err, backend)
	}
}

func TestDiskObservationErrorsAreBoundedAndRedacted(t *testing.T) {
	service := newTestService(t, newMemoryStateStore(), newFakeBackend())
	_, err := service.ReconcileDisk(context.Background(), Command{ID: "disk-unavailable", NowMillis: 10_000})
	if !errors.Is(err, ErrExternalUnavailable) || strings.Contains(err.Error(), "secret") || strings.Contains(err.Error(), "path") {
		t.Fatalf("disk response = %v", err)
	}
}

func TestConcurrentDailyReconcilersUseStateCAS(t *testing.T) {
	store, backend := newMemoryStateStore(), newFakeBackend()
	services := []*Service{newTestService(t, store, backend), newTestService(t, store, backend)}
	start := make(chan struct{})
	results := make(chan error, 2)
	for _, service := range services {
		go func(service *Service) {
			<-start
			commandID := "daily-race-a"
			if service == services[1] {
				commandID = "daily-race-b"
			}
			_, err := service.ReconcileDaily(context.Background(), Command{ID: commandID, NowMillis: 10_000})
			results <- err
		}(service)
	}
	close(start)
	first, second := <-results, <-results
	if first != nil && second != nil {
		t.Fatalf("both reconcilers failed: %v, %v", first, second)
	}
	state, _ := store.Load(context.Background())
	if len(state.Backups) != 1 || backend.createCount != 1 {
		t.Fatalf("race created %d records and %d effects", len(state.Backups), backend.createCount)
	}
}
