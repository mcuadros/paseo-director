// SPDX-License-Identifier: Apache-2.0

package dolt_test

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/go-sql-driver/mysql"

	"github.com/mcuadros/director-engine/adapters/diagnostics"
	"github.com/mcuadros/director-engine/adapters/dolt"
	"github.com/mcuadros/director-engine/domain"
	domainmaintenance "github.com/mcuadros/director-engine/domain/taskstoremaintenance"
	storeport "github.com/mcuadros/director-engine/ports/taskstore"
)

func maintenanceForFixture(t *testing.T, store *dolt.DoltTaskStore, root string) *dolt.Maintenance {
	t.Helper()
	logs, err := diagnostics.NewLogStore(filepath.Join(root, "logs"))
	if err != nil {
		t.Fatal(err)
	}
	maintenance, err := dolt.NewMaintenance(store, filepath.Join(root, "maintenance"), logs)
	if err != nil {
		t.Fatal(err)
	}
	return maintenance
}

func rootInspection(t *testing.T, fixture *doltFixture) *sql.DB {
	t.Helper()
	connector, err := mysql.NewConnector(&mysql.Config{User: "root", Net: "tcp", Addr: fixture.address,
		DBName: fixture.database, AllowNativePasswords: true})
	if err != nil {
		t.Fatal(err)
	}
	return sql.OpenDB(connector)
}

func TestDoltMaintenanceEffectsEndToEnd(t *testing.T) {
	fixture := startDoltFixture(t)
	store := openContractStore(t, fixture, "maintenance-store", true)
	t.Cleanup(func() { _ = store.Close() })
	maintenance := maintenanceForFixture(t, store, fixture.root)
	const now = int64(1_757_640_000_000)
	source, err := maintenance.ObserveStore(context.Background())
	if err != nil || !source.Exact || source.SchemaVersion != 2 {
		t.Fatalf("source observation=%#v err=%v", source, err)
	}
	backup := domainmaintenance.Backup{ID: "backup-contract", Purpose: domainmaintenance.BackupDaily,
		SourceSchemaVersion: 2, SourceFingerprint: source.Fingerprint, Phase: domainmaintenance.BackupDispatching,
		Attempt: 1, CreatedAtMillis: now, RetentionUntil: now + domainmaintenance.RetentionMillis}
	created, err := maintenance.CreateBackup(context.Background(), backup)
	if err != nil || created.Source != source {
		t.Fatalf("backup create=%#v err=%v", created, err)
	}
	validated, err := maintenance.ValidateBackup(context.Background(), backup)
	if err != nil || validated.Source != source || validated.RestoredFingerprint != source.Fingerprint {
		t.Fatalf("backup validation=%#v err=%v", validated, err)
	}
	if err := maintenance.ExpireBackup(context.Background(), backup); err != nil {
		t.Fatal(err)
	}
	observation, err := maintenance.ObserveBackup(context.Background(), backup.ID)
	if err != nil || observation.Present || !observation.Exact {
		t.Fatalf("expired backup observation=%#v err=%v", observation, err)
	}
}

func TestDoltV1ToV2EffectEndToEnd(t *testing.T) {
	fixture := startDoltFixture(t)
	store := openContractStore(t, fixture, "migration-store", true)
	t.Cleanup(func() { _ = store.Close() })
	project := domain.Project{ID: "migration-project", Name: "Migration project", State: "active", Organizer: testOrganizer("migration-project")}
	workspace := testWorkspace(t, project.ID, "migration-workspace", "/srv/migration-project", "https://github.com/example/migration-project.git")
	created, createErr := store.CreateProject(context.Background(),
		command("command-migration-project", "project.create", project.ID, 0, `{"name":"Migration project"}`),
		project, []domain.Workspace{workspace}, event("event-migration-project", "", 1, project.ID, 0, "project.created"))
	requireApplied(t, created, createErr)
	inspection := rootInspection(t, fixture)
	defer inspection.Close()
	ctx := context.Background()
	statements := []string{
		`DROP TRIGGER candidates_reject_update`,
		`ALTER TABLE candidates DROP COLUMN data`,
		`ALTER TABLE candidates MODIFY commit_sha CHAR(40) CHARACTER SET ascii COLLATE ascii_bin NOT NULL`,
		`CREATE TRIGGER candidates_reject_update BEFORE UPDATE ON candidates FOR EACH ROW INSERT INTO immutable_write_guard (singleton) VALUES (1)`,
		`UPDATE schema_metadata SET schema_version=1 WHERE singleton=1`,
		`INSERT INTO aggregates (id,kind,parent_id,version,data) VALUES ('legacy-run','run',NULL,1,JSON_OBJECT('number',1,'baseSha',?, 'currentCandidateId','legacy-candidate'))`,
		`INSERT INTO candidates (id,run_id,sequence,commit_sha) VALUES ('legacy-candidate','legacy-run',1,?)`,
	}
	for index, statement := range statements {
		var err error
		if index == 5 {
			_, err = inspection.ExecContext(ctx, statement, baseSHA)
		} else if index == 6 {
			_, err = inspection.ExecContext(ctx, statement, baseSHA)
		} else {
			_, err = inspection.ExecContext(ctx, statement)
		}
		if err != nil {
			t.Fatalf("prepare schema v1 statement %d: %v", index, err)
		}
	}
	maintenance := maintenanceForFixture(t, store, filepath.Join(fixture.root, "migration-fixture"))
	projects, err := maintenance.PlanProjectPause(ctx)
	if err != nil || len(projects) != 1 || projects[0].ID != project.ID || projects[0].OriginalState != "active" ||
		projects[0].OriginalVersion != 0 || projects[0].PausedVersion != 1 {
		t.Fatalf("pause plan=%#v err=%v", projects, err)
	}
	migration := domainmaintenance.Migration{ID: "migration-1-to-2", FromVersion: 1, ToVersion: 2,
		Phase: domainmaintenance.MigrationReady, Projects: projects, StartedAtMillis: time.Now().UnixMilli()}
	if _, err := maintenance.PauseProjects(ctx, migration); err != nil {
		t.Fatal(err)
	}
	source, err := maintenance.ObserveStore(ctx)
	if err != nil || !source.Exact || source.SchemaVersion != 1 {
		t.Fatalf("schema-v1 observation=%#v err=%v", source, err)
	}
	migration.SourceFingerprint = source.Fingerprint
	backup := domainmaintenance.Backup{ID: "migration-backup", Purpose: domainmaintenance.BackupMigration,
		MigrationID: migration.ID, SourceSchemaVersion: 1, SourceFingerprint: source.Fingerprint,
		Phase: domainmaintenance.BackupDispatching, Attempt: 1, CreatedAtMillis: migration.StartedAtMillis,
		RetentionUntil: migration.StartedAtMillis + domainmaintenance.RetentionMillis}
	if _, err := maintenance.CreateBackup(ctx, backup); err != nil {
		t.Fatal(err)
	}
	validation, err := maintenance.ValidateBackup(ctx, backup)
	if err != nil || validation.RestoredFingerprint != source.Fingerprint {
		t.Fatalf("migration backup validation=%#v err=%v", validation, err)
	}
	backup.Phase, backup.ValidatedAtMillis, backup.RestoreFingerprint = domainmaintenance.BackupValidated, migration.StartedAtMillis+1, validation.RestoredFingerprint
	migration.BackupID = backup.ID
	state, err := maintenance.Load(ctx)
	if err != nil {
		t.Fatal(err)
	}
	state.Backups, state.Migration = []domainmaintenance.Backup{backup}, &migration
	state = domainmaintenance.SealState(state)
	if gate := domainmaintenance.GateMigration(state, migration, source, migration.StartedAtMillis+2); gate != domainmaintenance.MigrationGateReady {
		t.Fatalf("migration gate=%s", gate)
	}
	migration.Phase = domainmaintenance.MigrationDispatching
	if _, err := maintenance.ApplyMigration(ctx, migration); !errors.Is(err, dolt.ErrMaintenanceUnsafe) {
		t.Fatalf("migration without durable validated-backup dispatch error=%v", err)
	}
	if observed, err := maintenance.ObserveStore(ctx); err != nil || observed.SchemaVersion != 1 || !observed.Exact {
		t.Fatalf("refused migration changed store=%#v err=%v", observed, err)
	}
	state.Migration = &migration
	state.Revision++
	state = domainmaintenance.SealState(state)
	if err := maintenance.CompareAndSwap(ctx, state.Revision-1, state); err != nil {
		t.Fatal(err)
	}
	result, err := maintenance.ApplyMigration(ctx, migration)
	if err != nil || result.Store.SchemaVersion != 2 || !result.Store.Exact {
		t.Fatalf("migration effect=%#v err=%v", result, err)
	}
	if err := maintenance.ResumeProjects(ctx, migration); err != nil {
		t.Fatal(err)
	}
	resumed, err := store.Project(ctx, project.ID)
	if err != nil || resumed.State != "active" || resumed.Version != 2 {
		t.Fatalf("resumed Project=%#v err=%v", resumed, err)
	}
	var maintenanceCommands int
	if err := inspection.QueryRowContext(ctx, `SELECT COUNT(*) FROM command_requests WHERE command_type LIKE 'project.control.maintenance_%'`).Scan(&maintenanceCommands); err != nil || maintenanceCommands != 2 {
		t.Fatalf("maintenance command count=%d err=%v", maintenanceCommands, err)
	}
	if version, err := store.SchemaVersion(ctx); err != nil || version != 2 {
		t.Fatalf("migrated schema version=%d err=%v", version, err)
	}
	candidate, err := store.Candidate(ctx, "legacy-candidate")
	if err != nil || candidate.SchemaVersion != domain.LegacyCandidateSchemaVersion || candidate.LegacyMigration == nil ||
		candidate.LegacyMigration.Authoritative || candidate.CommitSHA != baseSHA {
		t.Fatalf("legacy candidate=%#v err=%v", candidate, err)
	}
	if _, err := store.AppendCandidate(ctx, domain.CommandRequest{}, candidate, domain.Event{}); !errors.Is(err, storeport.ErrInvalidRecord) {
		t.Fatalf("legacy Candidate append error=%v", err)
	}
	var runDocument []byte
	if err := inspection.QueryRowContext(ctx, `SELECT data FROM aggregates WHERE id='legacy-run'`).Scan(&runDocument); err != nil {
		t.Fatal(err)
	}
	var run map[string]json.RawMessage
	if json.Unmarshal(runDocument, &run) != nil {
		t.Fatal("migrated Run JSON is invalid")
	}
	if _, present := run["currentCandidateId"]; present || strings.Contains(string(runDocument), "legacy-candidate") {
		t.Fatalf("legacy Run retained authority: %s", runDocument)
	}
	backups, err := os.ReadDir(filepath.Join(fixture.root, "migration-fixture", "maintenance", "backups"))
	if err != nil || len(backups) != 1 {
		t.Fatalf("validated migration backup count=%d err=%v", len(backups), err)
	}
}

func TestDoltInterruptedSchemaEffectProducesExactRecovery(t *testing.T) {
	fixture := startDoltFixture(t)
	store := openContractStore(t, fixture, "interruption-store", true)
	t.Cleanup(func() { _ = store.Close() })
	project := domain.Project{ID: "interruption-project", Name: "Interruption project", State: "active", Organizer: testOrganizer("interruption-project")}
	workspace := testWorkspace(t, project.ID, "interruption-workspace", "/srv/interruption-project", "https://github.com/example/interruption-project.git")
	created, createErr := store.CreateProject(context.Background(),
		command("command-interruption-project", "project.create", project.ID, 0, `{"name":"Interruption project"}`),
		project, []domain.Workspace{workspace}, event("event-interruption-project", "", 1, project.ID, 0, "project.created"))
	requireApplied(t, created, createErr)
	maintenance := maintenanceForFixture(t, store, filepath.Join(fixture.root, "interruption-fixture"))
	ctx := context.Background()
	projects, err := maintenance.PlanProjectPause(ctx)
	if err != nil || len(projects) != 1 {
		t.Fatalf("pause plan=%#v err=%v", projects, err)
	}
	migration := domainmaintenance.Migration{ID: "interrupted-schema-effect", FromVersion: 2, ToVersion: 3,
		Phase: domainmaintenance.MigrationReady, Projects: projects, StartedAtMillis: 1_757_640_000_000}
	if _, err := maintenance.PauseProjects(ctx, migration); err != nil {
		t.Fatal(err)
	}
	source, err := maintenance.ObserveStore(ctx)
	if err != nil || !source.Exact {
		t.Fatalf("paused source=%#v err=%v", source, err)
	}
	migration.SourceFingerprint = source.Fingerprint
	backup := domainmaintenance.Backup{ID: "interruption-backup", Purpose: domainmaintenance.BackupMigration,
		MigrationID: migration.ID, SourceSchemaVersion: 2, SourceFingerprint: source.Fingerprint,
		Phase: domainmaintenance.BackupDispatching, Attempt: 1, CreatedAtMillis: migration.StartedAtMillis,
		RetentionUntil: migration.StartedAtMillis + domainmaintenance.RetentionMillis}
	if _, err := maintenance.CreateBackup(ctx, backup); err != nil {
		t.Fatal(err)
	}
	validated, err := maintenance.ValidateBackup(ctx, backup)
	if err != nil || validated.RestoredFingerprint != source.Fingerprint {
		t.Fatalf("validated backup=%#v err=%v", validated, err)
	}
	inspection := rootInspection(t, fixture)
	defer inspection.Close()
	if _, err := inspection.ExecContext(ctx, `ALTER TABLE candidates ADD COLUMN interrupted_schema_effect VARCHAR(16) NULL`); err != nil {
		t.Fatal(err)
	}
	if partial, err := maintenance.ObserveStore(ctx); err != nil || partial.Exact || partial.Fingerprint == source.Fingerprint {
		t.Fatalf("partial schema observation=%#v err=%v", partial, err)
	}
	restored, err := maintenance.RestoreBackup(ctx, migration, backup)
	if err != nil || restored.Fingerprint != source.Fingerprint {
		t.Fatalf("fresh recovery=%#v err=%v", restored, err)
	}
	if _, paused, err := maintenance.ObserveProjectsPaused(ctx, migration); err != nil || !paused {
		t.Fatalf("Projects did not remain paused: paused=%t err=%v", paused, err)
	}
	var recoveryDatabases int
	if err := inspection.QueryRowContext(ctx, `SELECT COUNT(*) FROM information_schema.schemata WHERE schema_name LIKE 'director_recovery_%'`).Scan(&recoveryDatabases); err != nil || recoveryDatabases != 1 {
		t.Fatalf("fresh recovery database count=%d err=%v", recoveryDatabases, err)
	}
}
