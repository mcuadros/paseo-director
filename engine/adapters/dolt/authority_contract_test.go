// SPDX-License-Identifier: Apache-2.0

package dolt_test

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/go-sql-driver/mysql"

	"github.com/mcuadros/director-engine/adapters/dolt"
	"github.com/mcuadros/director-engine/domain"
	domainmaintenance "github.com/mcuadros/director-engine/domain/taskstoremaintenance"
)

func testRuntimeIdentity(user, marker string) dolt.RuntimeIdentity {
	return dolt.RuntimeIdentity{User: user, Host: "%", Password: strings.Repeat(marker, 24)}
}

func provisionRuntimeAuthorities(t *testing.T, fixture *doltFixture, storeID string) dolt.Config {
	t.Helper()
	bootstrapStore, err := dolt.Open(fixtureConfig(fixture, storeID))
	if err != nil {
		t.Fatal(err)
	}
	if err := bootstrapStore.Bootstrap(context.Background()); err != nil {
		bootstrapStore.Close()
		t.Fatal(err)
	}
	if err := bootstrapStore.Close(); err != nil {
		t.Fatal(err)
	}
	controlIdentity, writerIdentity, maintenanceIdentity := testRuntimeIdentity("director_control", "c"),
		testRuntimeIdentity("director_writer", "w"), testRuntimeIdentity("director_maintenance", "m")
	config, status, err := dolt.ProvisionRuntimeAuthority(context.Background(), dolt.RuntimeAuthorityBootstrap{
		Owner: dolt.Endpoint{Address: fixture.address, Database: fixture.database, User: "root"}, StoreID: storeID,
		Control: controlIdentity, Writer: writerIdentity, Maintenance: maintenanceIdentity,
		PrivilegeFile: filepath.Join(fixture.root, ".doltcfg", "privileges.db"),
	})
	if err != nil || !status.Exact || status.Code != dolt.AuthorityCurrent || len(status.SHA256) != 64 {
		t.Fatalf("provision runtime authority status=%#v err=%v", status, err)
	}
	return config
}

func endpointDatabase(t *testing.T, endpoint dolt.Endpoint) *sql.DB {
	t.Helper()
	connector, err := mysql.NewConnector(&mysql.Config{User: endpoint.User, Passwd: endpoint.Password, Net: "tcp",
		Addr: endpoint.Address, DBName: endpoint.Database, AllowNativePasswords: true})
	if err != nil {
		t.Fatal(err)
	}
	return sql.OpenDB(connector)
}

func TestDolt232ExactBackupExecutionGrant(t *testing.T) {
	fixture := startDoltFixture(t)
	config := provisionRuntimeAuthorities(t, fixture, "maintenance-authority-store")
	store, err := dolt.Open(config)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if status, err := store.VerifyRuntimeAuthority(context.Background()); err != nil || !status.Exact || status.Code != dolt.AuthorityCurrent {
		t.Fatalf("runtime authority status=%#v err=%v", status, err)
	}

	for name, endpoint := range map[string]dolt.Endpoint{"control": config.Control, "writer": config.Writer} {
		t.Run(name+" cannot back up", func(t *testing.T) {
			database := endpointDatabase(t, endpoint)
			defer database.Close()
			target := filepath.Join(t.TempDir(), "denied")
			if _, err := database.ExecContext(context.Background(), `CALL DOLT_BACKUP('sync-url', ?)`, "file://"+filepath.ToSlash(target)); err == nil {
				t.Fatal("normal Engine identity executed DOLT_BACKUP")
			}
			if _, err := os.Lstat(target); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("denied routine created a target: %v", err)
			}
		})
	}

	maintenanceDatabase := endpointDatabase(t, config.Maintenance)
	defer maintenanceDatabase.Close()
	for name, statement := range map[string]string{
		"read application data": `SELECT * FROM aggregates`,
		"create table":          `CREATE TABLE authority_escape (id INT PRIMARY KEY)`,
		"grant privilege":       `GRANT SELECT ON *.* TO director_maintenance@'%'`,
		"other admin procedure": `CALL DOLT_GC()`,
	} {
		t.Run("maintenance cannot "+name, func(t *testing.T) {
			if _, err := maintenanceDatabase.ExecContext(context.Background(), statement); err == nil {
				t.Fatalf("maintenance identity unexpectedly executed %s", name)
			}
		})
	}
	project := domain.Project{ID: "authority-project", Name: "Authority project", State: "active", Organizer: testOrganizer("authority-project")}
	workspace := testWorkspace(t, project.ID, "authority-workspace", "/srv/authority", "https://github.com/example/authority.git")
	created, createErr := store.CreateProject(context.Background(),
		command("authority-project-create", "project.create", project.ID, 0, `{}`), project, []domain.Workspace{workspace},
		event("authority-project-event", "", 1, project.ID, 0, "project.created"))
	requireApplied(t, created, createErr)

	maintenance := maintenanceForFixture(t, store, filepath.Join(fixture.root, "authority-contract"))
	source, err := maintenance.ObserveStore(context.Background())
	if err != nil || !source.Exact {
		t.Fatalf("source=%#v err=%v", source, err)
	}
	backup := domainmaintenance.Backup{ID: "least-privilege-backup", Purpose: domainmaintenance.BackupDaily,
		SourceSchemaVersion: source.SchemaVersion, SourceFingerprint: source.Fingerprint,
		Phase: domainmaintenance.BackupDispatching, Attempt: 1, CreatedAtMillis: 10_000,
		RetentionUntil: 10_000 + domainmaintenance.RetentionMillis}
	if _, err := maintenance.CreateBackup(context.Background(), backup); err != nil {
		t.Fatalf("exact maintenance routine grant did not create backup: %v", err)
	}
	if validation, err := maintenance.ValidateBackup(context.Background(), backup); err != nil || validation.RestoredFingerprint != source.Fingerprint {
		t.Fatalf("least-privilege restore validation=%#v err=%v", validation, err)
	}
	root := rootInspection(t, fixture)
	if _, err := root.ExecContext(context.Background(), "GRANT ALL ON `"+fixture.database+"`.* TO director_control@'%'"); err != nil {
		root.Close()
		t.Fatal(err)
	}
	databaseScopedControl := endpointDatabase(t, config.Control)
	databaseScopedTarget := filepath.Join(t.TempDir(), "database-scoped-denied")
	if _, err := databaseScopedControl.ExecContext(context.Background(), `CALL DOLT_BACKUP('sync-url', ?)`, "file://"+filepath.ToSlash(databaseScopedTarget)); err == nil {
		databaseScopedControl.Close()
		root.Close()
		t.Fatal("database-scoped ALL unexpectedly authorized DOLT_BACKUP")
	}
	databaseScopedControl.Close()
	if _, err := root.ExecContext(context.Background(), `GRANT ALL ON *.* TO director_control@'%' WITH GRANT OPTION`); err != nil {
		root.Close()
		t.Fatal(err)
	}
	root.Close()
	if status, err := store.VerifyRuntimeAuthority(context.Background()); !errors.Is(err, dolt.ErrRuntimeAuthority) ||
		status.Code != dolt.AuthorityAttestationMismatch || status.Exact {
		t.Fatalf("broad authority drift status=%#v err=%v", status, err)
	}
	if _, err := maintenance.ObserveStore(context.Background()); !errors.Is(err, dolt.ErrMaintenanceUnsafe) {
		t.Fatalf("maintenance continued after privilege drift: %v", err)
	}
	repairedConfig := provisionRuntimeAuthorities(t, fixture, "maintenance-authority-store")
	repaired, err := dolt.Open(repairedConfig)
	if err != nil {
		t.Fatal(err)
	}
	defer repaired.Close()
	if status, err := repaired.VerifyRuntimeAuthority(context.Background()); err != nil || !status.Exact || status.Code != dolt.AuthorityCurrent {
		t.Fatalf("idempotent authority repair status=%#v err=%v", status, err)
	}
	if err := os.Chmod(repairedConfig.PrivilegeFile, 0o644); err != nil {
		t.Fatal(err)
	}
	if status, err := repaired.VerifyRuntimeAuthority(context.Background()); !errors.Is(err, dolt.ErrRuntimeAuthority) ||
		status.Code != dolt.AuthorityAttestationMismatch {
		t.Fatalf("broad privilege-file mode status=%#v err=%v", status, err)
	}
	if err := os.Chmod(repairedConfig.PrivilegeFile, 0o600); err != nil {
		t.Fatal(err)
	}
	if status, err := repaired.VerifyRuntimeAuthority(context.Background()); err != nil || !status.Exact {
		t.Fatalf("restored privilege-file mode status=%#v err=%v", status, err)
	}
}
