// SPDX-License-Identifier: Apache-2.0

package taskstoremaintenance

import (
	"strings"
	"testing"
)

func digest(character string) string { return strings.Repeat(character, 64) }

func validatedMigrationState(t *testing.T) (State, Migration, StoreObservation) {
	t.Helper()
	state := NewState("store-1", digest("d"))
	migration := Migration{ID: "migration-1", FromVersion: 1, ToVersion: 2, Phase: MigrationReady,
		BackupID: "backup-1", SourceFingerprint: digest("a"), StartedAtMillis: 1_000,
		Projects: []ProjectBinding{{ID: "project-1", OriginalState: "active", OriginalVersion: 3, PausedVersion: 4}}}
	state.Backups = []Backup{{ID: "backup-1", Purpose: BackupMigration, MigrationID: migration.ID,
		SourceSchemaVersion: 1, SourceFingerprint: migration.SourceFingerprint, Phase: BackupValidated,
		CreatedAtMillis: 1_000, RetentionUntil: 1_000 + RetentionMillis, ValidatedAtMillis: 2_000,
		RestoreFingerprint: migration.SourceFingerprint, Attempt: 1}}
	state.Migration = &migration
	state = SealState(state)
	observation := StoreObservation{SchemaVersion: 1, Fingerprint: migration.SourceFingerprint, Exact: true}
	if !ValidState(state) {
		t.Fatal("fixture state is invalid")
	}
	return state, migration, observation
}

func TestMigrationGateRequiresExactFreshRestoreBinding(t *testing.T) {
	state, migration, observation := validatedMigrationState(t)
	if got := GateMigration(state, migration, observation, 3_000); got != MigrationGateReady {
		t.Fatalf("valid gate = %s", got)
	}
	cases := map[string]func(*State, *Migration, *StoreObservation, *int64){
		"unpaused project": func(_ *State, migration *Migration, _ *StoreObservation, _ *int64) {
			migration.Projects[0].PausedVersion = 3
		},
		"missing backup": func(state *State, _ *Migration, _ *StoreObservation, _ *int64) { state.Backups = nil },
		"unvalidated backup": func(state *State, _ *Migration, _ *StoreObservation, _ *int64) {
			state.Backups[0].Phase = BackupValidationRequired
			state.Backups[0].ValidatedAtMillis = 0
			state.Backups[0].RestoreFingerprint = ""
		},
		"wrong migration": func(state *State, _ *Migration, _ *StoreObservation, _ *int64) {
			state.Backups[0].MigrationID = "migration-other"
		},
		"wrong restore": func(state *State, _ *Migration, _ *StoreObservation, _ *int64) {
			state.Backups[0].RestoreFingerprint = digest("b")
		},
		"expired": func(_ *State, _ *Migration, _ *StoreObservation, now *int64) { *now = 1_000 + RetentionMillis },
		"store changed": func(_ *State, _ *Migration, observation *StoreObservation, _ *int64) {
			observation.Fingerprint = digest("b")
		},
		"schema changed":  func(_ *State, _ *Migration, observation *StoreObservation, _ *int64) { observation.SchemaVersion = 2 },
		"ambiguous store": func(_ *State, _ *Migration, observation *StoreObservation, _ *int64) { observation.Exact = false },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			candidateState, candidateMigration, candidateObservation := state, migration, observation
			candidateState.Backups = append([]Backup(nil), state.Backups...)
			candidateMigration.Projects = append([]ProjectBinding(nil), migration.Projects...)
			now := int64(3_000)
			mutate(&candidateState, &candidateMigration, &candidateObservation, &now)
			candidateState.Migration = &candidateMigration
			candidateState = SealState(candidateState)
			if got := GateMigration(candidateState, candidateMigration, candidateObservation, now); got == MigrationGateReady {
				t.Fatalf("%s passed exact backup gate", name)
			}
		})
	}
}

func TestDailyBackupAndSevenDayRetentionAreExact(t *testing.T) {
	state := NewState("store-1", digest("d"))
	if !DailyBackupDue(state, 1_000) {
		t.Fatal("empty store did not require daily backup")
	}
	state.Backups = []Backup{{ID: "migration-backup", Purpose: BackupMigration, MigrationID: "migration-1", SourceSchemaVersion: 1,
		SourceFingerprint: digest("a"), Phase: BackupValidated, Attempt: 1, CreatedAtMillis: 1_000,
		RetentionUntil: 1_000 + RetentionMillis, ValidatedAtMillis: 2_000, RestoreFingerprint: digest("a")}}
	state = SealState(state)
	if !DailyBackupDue(state, 2_001) {
		t.Fatal("migration backup incorrectly satisfied daily schedule")
	}
	state.Backups[0].Purpose, state.Backups[0].MigrationID = BackupDaily, ""
	state = SealState(state)
	if DailyBackupDue(state, 2_000+DailyIntervalMillis-1) {
		t.Fatal("daily backup became due early")
	}
	if !DailyBackupDue(state, 2_000+DailyIntervalMillis) {
		t.Fatal("daily backup not due at exact interval")
	}
	if _, ok := LatestValidatedBackup(state, 1_000+RetentionMillis-1); !ok {
		t.Fatal("backup expired early")
	}
	if _, ok := LatestValidatedBackup(state, 1_000+RetentionMillis); ok {
		t.Fatal("backup survived exact retention deadline")
	}
}

func TestDiskReducerCleansBeforeBlockingAndNeverConsumesBelowFloor(t *testing.T) {
	observation := DiskObservation{ObservedAtMillis: 1_000, MaximumAgeMillis: 5_000, TotalBytes: 100_000}
	for _, test := range []struct {
		name                     string
		free                     uint64
		cleaned                  bool
		phase                    DiskPhase
		launch, cleanup, consume bool
	}{
		{"exact floor", 10_000, false, DiskReady, true, false, true},
		{"below floor cleanup", 9_999, false, DiskCleanupRequired, false, true, false},
		{"below floor degraded", 9_999, true, DiskDegraded, false, false, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			observation.FreeBytes = test.free
			got := ReduceDisk(observation, 1_000, test.cleaned)
			if got.Phase != test.phase || got.LaunchAllowed != test.launch || got.CleanupExpiredOnly != test.cleanup || got.ConsumeMoreDisk != test.consume {
				t.Fatalf("decision = %#v", got)
			}
		})
	}
	stale := observation
	stale.ObservedAtMillis = 0
	if got := ReduceDisk(stale, 5_001, false); got.Phase != DiskUnavailable || got.LaunchAllowed || got.ConsumeMoreDisk {
		t.Fatalf("stale disk fact = %#v", got)
	}
	maximum := ^uint64(0)
	if got := ReduceDisk(DiskObservation{ObservedAtMillis: 1, MaximumAgeMillis: 1, TotalBytes: maximum, FreeBytes: maximum}, 1, false); got.Phase != DiskReady || got.FreeBasisPoints != 10_000 {
		t.Fatalf("maximum-sized disk fact overflowed = %#v", got)
	}
}
