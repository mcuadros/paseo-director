// SPDX-License-Identifier: Apache-2.0

// Package taskstoremaintenance owns the pure TaskStore backup, migration,
// retention, and disk-pressure contract. It performs no SQL, filesystem,
// process, clock, or host operation.
package taskstoremaintenance

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"math/bits"
	"regexp"
	"slices"
)

const (
	StateSchemaVersion           = "director.taskstore-maintenance/v1"
	DailyIntervalMillis    int64 = 24 * 60 * 60 * 1_000
	RetentionMillis        int64 = 7 * DailyIntervalMillis
	MinimumFreeBasisPoints       = uint64(1_000)
	MaximumRecords               = 32
	MaximumProjects              = 10_000
)

var (
	identifierPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.:@/-]{0,127}$`)
	digestPattern     = regexp.MustCompile(`^[0-9a-f]{64}$`)
)

type BackupPurpose string

const (
	BackupDaily     BackupPurpose = "daily"
	BackupMigration BackupPurpose = "migration"
)

type BackupPhase string

const (
	BackupIntentRecorded     BackupPhase = "intent_recorded"
	BackupDispatching        BackupPhase = "dispatching"
	BackupValidationRequired BackupPhase = "validation_required"
	BackupValidated          BackupPhase = "validated"
	BackupExpiryDispatching  BackupPhase = "expiry_dispatching"
	BackupExpired            BackupPhase = "expired"
	BackupNeedsYou           BackupPhase = "needs_you"
)

type Backup struct {
	ID                  string        `json:"id"`
	Purpose             BackupPurpose `json:"purpose"`
	MigrationID         string        `json:"migrationId,omitempty"`
	SourceSchemaVersion int           `json:"sourceSchemaVersion"`
	SourceFingerprint   string        `json:"sourceFingerprint"`
	Phase               BackupPhase   `json:"phase"`
	Attempt             uint32        `json:"attempt"`
	CreatedAtMillis     int64         `json:"createdAtMillis"`
	RetentionUntil      int64         `json:"retentionUntilMillis"`
	ValidatedAtMillis   int64         `json:"validatedAtMillis,omitempty"`
	RestoreFingerprint  string        `json:"restoreFingerprint,omitempty"`
	NeedsYouCode        string        `json:"needsYouCode,omitempty"`
}

type ProjectBinding struct {
	ID              string `json:"id"`
	OriginalState   string `json:"originalState"`
	OriginalVersion uint64 `json:"originalVersion"`
	PausedVersion   uint64 `json:"pausedVersion"`
}

type MigrationPhase string

const (
	MigrationIntentRecorded  MigrationPhase = "intent_recorded"
	MigrationPauseRequired   MigrationPhase = "pause_required"
	MigrationBackupRequired  MigrationPhase = "backup_required"
	MigrationReady           MigrationPhase = "ready"
	MigrationDispatching     MigrationPhase = "dispatching"
	MigrationVerifyRequired  MigrationPhase = "verify_required"
	MigrationRestoreRequired MigrationPhase = "restore_required"
	MigrationResumeRequired  MigrationPhase = "resume_required"
	MigrationComplete        MigrationPhase = "complete"
	MigrationNeedsYou        MigrationPhase = "needs_you"
)

type Migration struct {
	ID                  string           `json:"id"`
	FromVersion         int              `json:"fromVersion"`
	ToVersion           int              `json:"toVersion"`
	Phase               MigrationPhase   `json:"phase"`
	Attempt             uint32           `json:"attempt"`
	BackupID            string           `json:"backupId,omitempty"`
	SourceFingerprint   string           `json:"sourceFingerprint,omitempty"`
	MigratedFingerprint string           `json:"migratedFingerprint,omitempty"`
	RestoreFingerprint  string           `json:"restoreFingerprint,omitempty"`
	Projects            []ProjectBinding `json:"projects,omitempty"`
	StartedAtMillis     int64            `json:"startedAtMillis"`
	CompletedAtMillis   int64            `json:"completedAtMillis,omitempty"`
	NeedsYouCode        string           `json:"needsYouCode,omitempty"`
}

type State struct {
	SchemaVersion      string     `json:"schemaVersion"`
	StoreID            string     `json:"storeId"`
	StoreBindingSHA256 string     `json:"storeBindingSha256"`
	Revision           uint64     `json:"revision"`
	Backups            []Backup   `json:"backups,omitempty"`
	Migration          *Migration `json:"migration,omitempty"`
	SHA256             string     `json:"sha256"`
}

func stateValue(value State) State { value.SHA256 = ""; return value }

func StateSHA256(value State) string {
	encoded, err := json.Marshal(stateValue(value))
	if err != nil {
		panic("marshal fixed TaskStore maintenance state: " + err.Error())
	}
	digest := sha256.Sum256(encoded)
	return hex.EncodeToString(digest[:])
}

func SealState(value State) State {
	value.SchemaVersion = StateSchemaVersion
	value.SHA256 = StateSHA256(value)
	return value
}

func NewState(storeID, storeBindingSHA256 string) State {
	return SealState(State{StoreID: storeID, StoreBindingSHA256: storeBindingSHA256, Revision: 1})
}

func validProjectState(value string) bool {
	return value == "active" || value == "paused" || value == "degraded" || value == "archived"
}

func validBackup(value Backup) bool {
	if !identifierPattern.MatchString(value.ID) || (value.Purpose != BackupDaily && value.Purpose != BackupMigration) ||
		value.SourceSchemaVersion <= 0 || !digestPattern.MatchString(value.SourceFingerprint) || value.Attempt > 2 ||
		value.CreatedAtMillis < 0 || value.RetentionUntil-value.CreatedAtMillis != RetentionMillis {
		return false
	}
	if value.Purpose == BackupMigration {
		if !identifierPattern.MatchString(value.MigrationID) {
			return false
		}
	} else if value.MigrationID != "" {
		return false
	}
	switch value.Phase {
	case BackupIntentRecorded, BackupDispatching, BackupValidationRequired:
		return value.ValidatedAtMillis == 0 && value.RestoreFingerprint == "" && value.NeedsYouCode == ""
	case BackupValidated, BackupExpiryDispatching:
		return value.ValidatedAtMillis >= value.CreatedAtMillis && value.ValidatedAtMillis <= value.RetentionUntil &&
			value.RestoreFingerprint == value.SourceFingerprint && value.NeedsYouCode == ""
	case BackupExpired:
		return value.ValidatedAtMillis >= value.CreatedAtMillis && value.RestoreFingerprint == value.SourceFingerprint && value.NeedsYouCode == ""
	case BackupNeedsYou:
		return identifierPattern.MatchString(value.NeedsYouCode)
	default:
		return false
	}
}

func validMigration(value Migration) bool {
	if !identifierPattern.MatchString(value.ID) || value.FromVersion <= 0 || value.ToVersion != value.FromVersion+1 ||
		value.Attempt > 2 || value.StartedAtMillis < 0 || value.CompletedAtMillis < 0 || len(value.Projects) > MaximumProjects {
		return false
	}
	seen := map[string]struct{}{}
	for _, project := range value.Projects {
		if !identifierPattern.MatchString(project.ID) || !validProjectState(project.OriginalState) || project.PausedVersion < project.OriginalVersion {
			return false
		}
		if _, duplicate := seen[project.ID]; duplicate {
			return false
		}
		seen[project.ID] = struct{}{}
	}
	if value.BackupID != "" && !identifierPattern.MatchString(value.BackupID) ||
		value.SourceFingerprint != "" && !digestPattern.MatchString(value.SourceFingerprint) ||
		value.MigratedFingerprint != "" && !digestPattern.MatchString(value.MigratedFingerprint) ||
		value.RestoreFingerprint != "" && !digestPattern.MatchString(value.RestoreFingerprint) {
		return false
	}
	switch value.Phase {
	case MigrationIntentRecorded, MigrationPauseRequired, MigrationBackupRequired:
		return value.CompletedAtMillis == 0 && value.NeedsYouCode == ""
	case MigrationReady, MigrationDispatching:
		return value.BackupID != "" && digestPattern.MatchString(value.SourceFingerprint) && value.CompletedAtMillis == 0 && value.NeedsYouCode == ""
	case MigrationVerifyRequired, MigrationResumeRequired:
		return value.BackupID != "" && digestPattern.MatchString(value.SourceFingerprint) && value.CompletedAtMillis == 0 && value.NeedsYouCode == ""
	case MigrationRestoreRequired:
		return value.BackupID != "" && digestPattern.MatchString(value.SourceFingerprint) && value.CompletedAtMillis == 0 && value.NeedsYouCode == ""
	case MigrationComplete:
		return value.BackupID != "" && digestPattern.MatchString(value.MigratedFingerprint) &&
			value.CompletedAtMillis >= value.StartedAtMillis && value.NeedsYouCode == ""
	case MigrationNeedsYou:
		return identifierPattern.MatchString(value.NeedsYouCode) && value.CompletedAtMillis == 0
	default:
		return false
	}
}

func ValidState(value State) bool {
	if value.SchemaVersion != StateSchemaVersion || !identifierPattern.MatchString(value.StoreID) ||
		!digestPattern.MatchString(value.StoreBindingSHA256) || value.Revision == 0 ||
		len(value.Backups) > MaximumRecords || !digestPattern.MatchString(value.SHA256) || value.SHA256 != StateSHA256(value) {
		return false
	}
	seen := map[string]struct{}{}
	for _, backup := range value.Backups {
		if !validBackup(backup) {
			return false
		}
		if _, duplicate := seen[backup.ID]; duplicate {
			return false
		}
		seen[backup.ID] = struct{}{}
	}
	return value.Migration == nil || validMigration(*value.Migration)
}

func BackupIndex(value State, id string) int {
	return slices.IndexFunc(value.Backups, func(backup Backup) bool { return backup.ID == id })
}

// LatestValidatedBackup returns only non-expired proof. A validation for a
// different schema or source fingerprint is never interchangeable.
func LatestValidatedBackup(value State, nowMillis int64) (Backup, bool) {
	var latest Backup
	for _, backup := range value.Backups {
		if backup.Purpose != BackupDaily || backup.Phase != BackupValidated || nowMillis >= backup.RetentionUntil {
			continue
		}
		if latest.ID == "" || backup.ValidatedAtMillis > latest.ValidatedAtMillis {
			latest = backup
		}
	}
	return latest, latest.ID != ""
}

func DailyBackupDue(value State, nowMillis int64) bool {
	latest, ok := LatestValidatedBackup(value, nowMillis)
	return !ok || nowMillis-latest.ValidatedAtMillis >= DailyIntervalMillis
}

type StoreObservation struct {
	SchemaVersion int    `json:"schemaVersion"`
	Fingerprint   string `json:"fingerprint"`
	Exact         bool   `json:"exact"`
}

func ValidStoreObservation(value StoreObservation) bool {
	return value.SchemaVersion > 0 && value.Exact && digestPattern.MatchString(value.Fingerprint)
}

type MigrationGateCode string

const (
	MigrationGateReady            MigrationGateCode = "MIGRATION_READY"
	MigrationGateStateInvalid     MigrationGateCode = "MIGRATION_STATE_INVALID"
	MigrationGateProjectNotPaused MigrationGateCode = "MIGRATION_PROJECT_NOT_PAUSED"
	MigrationGateBackupMissing    MigrationGateCode = "MIGRATION_VALIDATED_BACKUP_REQUIRED"
	MigrationGateBackupMismatch   MigrationGateCode = "MIGRATION_BACKUP_BINDING_MISMATCH"
	MigrationGateStoreMismatch    MigrationGateCode = "MIGRATION_STORE_BINDING_MISMATCH"
)

// GateMigration is the sole authority for schema mutation. In particular, an
// existing backup path or a successful backup call is insufficient: the exact
// fresh-restore fingerprint must equal the paused live-store fingerprint.
func GateMigration(state State, migration Migration, observation StoreObservation, nowMillis int64) MigrationGateCode {
	if !ValidState(state) || !validMigration(migration) || migration.Phase != MigrationReady || !ValidStoreObservation(observation) {
		return MigrationGateStateInvalid
	}
	for _, project := range migration.Projects {
		if (project.OriginalState == "active" || project.OriginalState == "degraded") && project.PausedVersion <= project.OriginalVersion {
			return MigrationGateProjectNotPaused
		}
	}
	index := BackupIndex(state, migration.BackupID)
	if index < 0 {
		return MigrationGateBackupMissing
	}
	backup := state.Backups[index]
	if backup.Phase != BackupValidated || nowMillis >= backup.RetentionUntil || backup.Purpose != BackupMigration ||
		backup.MigrationID != migration.ID || backup.SourceSchemaVersion != migration.FromVersion ||
		backup.RestoreFingerprint != backup.SourceFingerprint || backup.SourceFingerprint != migration.SourceFingerprint {
		return MigrationGateBackupMismatch
	}
	if observation.SchemaVersion != migration.FromVersion || observation.Fingerprint != migration.SourceFingerprint {
		return MigrationGateStoreMismatch
	}
	return MigrationGateReady
}

type DiskPhase string

const (
	DiskReady           DiskPhase = "ready"
	DiskCleanupRequired DiskPhase = "cleanup_required"
	DiskDegraded        DiskPhase = "degraded_needs_you"
	DiskUnavailable     DiskPhase = "unavailable"
)

type DiskObservation struct {
	ObservedAtMillis int64  `json:"observedAtMillis"`
	MaximumAgeMillis int64  `json:"maximumAgeMillis"`
	TotalBytes       uint64 `json:"totalBytes"`
	FreeBytes        uint64 `json:"freeBytes"`
}

type DiskDecision struct {
	Phase              DiskPhase `json:"phase"`
	FreeBasisPoints    uint64    `json:"freeBasisPoints"`
	LaunchAllowed      bool      `json:"launchAllowed"`
	CleanupExpiredOnly bool      `json:"cleanupExpiredOnly"`
	ConsumeMoreDisk    bool      `json:"consumeMoreDisk"`
}

// ReduceDisk applies the non-reducible 10-percent floor. CleanupRequested
// means bounded expiry/compaction was already attempted; it never means
// unintegrated recovery material may be deleted.
func ReduceDisk(observation DiskObservation, nowMillis int64, cleanupRequested bool) DiskDecision {
	if observation.ObservedAtMillis < 0 || observation.ObservedAtMillis > nowMillis || observation.MaximumAgeMillis <= 0 ||
		nowMillis-observation.ObservedAtMillis > observation.MaximumAgeMillis || observation.TotalBytes == 0 ||
		observation.FreeBytes > observation.TotalBytes {
		return DiskDecision{Phase: DiskUnavailable}
	}
	high, low := bits.Mul64(observation.FreeBytes, 10_000)
	basisPoints, _ := bits.Div64(high, low, observation.TotalBytes)
	if basisPoints >= MinimumFreeBasisPoints {
		return DiskDecision{Phase: DiskReady, FreeBasisPoints: basisPoints, LaunchAllowed: true, ConsumeMoreDisk: true}
	}
	if !cleanupRequested {
		return DiskDecision{Phase: DiskCleanupRequired, FreeBasisPoints: basisPoints, CleanupExpiredOnly: true}
	}
	return DiskDecision{Phase: DiskDegraded, FreeBasisPoints: basisPoints}
}
