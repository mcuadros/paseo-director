// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/binary"
	"encoding/json"
	"errors"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/mcuadros/director-engine/adapters/diagnostics"
	"github.com/mcuadros/director-engine/adapters/dolt"
	maintenanceapp "github.com/mcuadros/director-engine/application/taskstoremaintenance"
	"github.com/mcuadros/director-engine/domain"
	repositorydomain "github.com/mcuadros/director-engine/domain/repository"
	domainmaintenance "github.com/mcuadros/director-engine/domain/taskstoremaintenance"
	maintenanceport "github.com/mcuadros/director-engine/ports/taskstoremaintenance"
)

const maintenanceSecurityStoreID = "organizer-flow-store"

func securityIdentity(user, marker string) dolt.RuntimeIdentity {
	return dolt.RuntimeIdentity{User: user, Host: "%", Password: strings.Repeat(marker, 24)}
}

func provisionSecurityAuthorities(t *testing.T, fixture *organizerDoltFixture) dolt.Config {
	t.Helper()
	runtimeRoot := filepath.Join(fixture.root, "security-runtime")
	if err := os.MkdirAll(runtimeRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	passwords := map[string]string{
		filepath.Join(runtimeRoot, "control.password"):     securityIdentity("director_control", "c").Password,
		filepath.Join(runtimeRoot, "writer.password"):      securityIdentity("director_writer", "w").Password,
		filepath.Join(runtimeRoot, "maintenance.password"): securityIdentity("director_maintenance", "m").Password,
	}
	for path, password := range passwords {
		if _, err := os.Lstat(path); errors.Is(err, os.ErrNotExist) {
			if err := os.WriteFile(path, []byte(password), 0o600); err != nil {
				t.Fatal(err)
			}
		} else if err != nil {
			t.Fatal(err)
		}
	}
	configPath := filepath.Join(runtimeRoot, "taskstore.json")
	result, err := provisionTaskStore(context.Background(), taskStoreProvisionInput{
		ConfigOutput: configPath, Address: fixture.address, Database: fixture.database, StoreID: maintenanceSecurityStoreID,
		OwnerUser: "root", ControlUser: "director_control", WriterUser: "director_writer", MaintenanceUser: "director_maintenance",
		ControlPasswordFile: filepath.Join(runtimeRoot, "control.password"), WriterPasswordFile: filepath.Join(runtimeRoot, "writer.password"),
		MaintenancePasswordFile: filepath.Join(runtimeRoot, "maintenance.password"),
		PrivilegeFile:           filepath.Join(fixture.root, ".doltcfg", "privileges.db"),
		MaintenanceRoot:         filepath.Join(runtimeRoot, "bootstrap-maintenance"), NowMillis: 10_000,
	})
	if err != nil || result.Authority != string(dolt.AuthorityCurrent) {
		t.Fatalf("provision exact TaskStore authorities result=%#v err=%v", result, err)
	}
	config, err := readBoardServerConfig(configPath)
	if err != nil {
		t.Fatal(err)
	}
	return config
}

func securityRootDatabase(t *testing.T, fixture *organizerDoltFixture) *sql.DB {
	t.Helper()
	database, err := sql.Open("mysql", "root@tcp("+fixture.address+")/"+fixture.database)
	if err != nil {
		t.Fatal(err)
	}
	return database
}

func setExactBackupGrant(t *testing.T, fixture *organizerDoltFixture, grant bool) {
	t.Helper()
	root := securityRootDatabase(t, fixture)
	defer root.Close()
	verb, suffix := "GRANT", " TO `director_maintenance`@`%`"
	if !grant {
		verb, suffix = "REVOKE", " FROM `director_maintenance`@`%`"
	}
	statement := verb + " EXECUTE ON PROCEDURE `" + fixture.database + "`.`dolt_backup`" + suffix
	if _, err := root.ExecContext(context.Background(), statement); err != nil {
		t.Fatalf("change exact backup routine grant: %v", err)
	}
}

func maintenanceBackend(t *testing.T, store *dolt.DoltTaskStore, root string) *dolt.Maintenance {
	t.Helper()
	logs, err := diagnostics.NewLogStore(filepath.Join(root, "logs"))
	if err != nil {
		t.Fatal(err)
	}
	backend, err := dolt.NewMaintenance(store, filepath.Join(root, "maintenance"), logs)
	if err != nil {
		t.Fatal(err)
	}
	return backend
}

func restartSecurityDolt(t *testing.T, fixture *organizerDoltFixture) {
	t.Helper()
	if fixture.command.Process == nil || fixture.command.Process.Signal(syscall.SIGKILL) != nil {
		t.Fatal("kill disposable Dolt server")
	}
	select {
	case <-fixture.waited:
		fixture.stopped = true
	case <-time.After(10 * time.Second):
		t.Fatal("disposable Dolt server did not stop")
	}
	command := exec.Command("dolt", "sql-server", "--config="+filepath.Join(fixture.root, "server.yaml"))
	command.Dir = fixture.root
	command.Env = organizerDoltEnvironment(filepath.Join(fixture.root, "client"))
	var output strings.Builder
	command.Stdout, command.Stderr = &output, &output
	if err := command.Start(); err != nil {
		t.Fatalf("restart disposable Dolt server: %v", err)
	}
	fixture.command, fixture.waited, fixture.stopped = command, make(chan error, 1), false
	go func() { fixture.waited <- command.Wait() }()
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		connection, err := net.DialTimeout("tcp", fixture.address, 100*time.Millisecond)
		if err == nil {
			connection.Close()
			return
		}
		select {
		case err := <-fixture.waited:
			fixture.stopped = true
			t.Fatalf("disposable Dolt restart exited: %v: %s", err, output.String())
		default:
		}
		time.Sleep(25 * time.Millisecond)
	}
	t.Fatalf("disposable Dolt restart did not listen: %s", output.String())
}

type responseLossBackend struct {
	maintenanceport.Backend
	mu                        sync.Mutex
	loseRearm, loseValidation bool
}

func (backend *responseLossBackend) RearmBackup(ctx context.Context, backup domainmaintenance.Backup, evidence string) error {
	err := backend.Backend.RearmBackup(ctx, backup, evidence)
	backend.mu.Lock()
	defer backend.mu.Unlock()
	if err == nil && backend.loseRearm {
		backend.loseRearm = false
		return errors.New("bounded simulated response loss")
	}
	return err
}

func (backend *responseLossBackend) ValidateBackup(ctx context.Context, backup domainmaintenance.Backup) (maintenanceport.ValidationResult, error) {
	result, err := backend.Backend.ValidateBackup(ctx, backup)
	backend.mu.Lock()
	defer backend.mu.Unlock()
	if err == nil && backend.loseValidation {
		backend.loseValidation = false
		return maintenanceport.ValidationResult{}, errors.New("bounded simulated response loss")
	}
	return result, err
}

func TestTaskStoreDeniedFirstBackupRecoversAfterGrantRestartAndContention(t *testing.T) {
	fixture := startOrganizerDoltFixture(t)
	config := provisionSecurityAuthorities(t, fixture)
	setExactBackupGrant(t, fixture, false)
	legacyEndpoint := dolt.Endpoint{Address: fixture.address, Database: fixture.database, User: "root"}
	legacyStore, err := dolt.Open(dolt.Config{Control: legacyEndpoint, Writer: legacyEndpoint, Maintenance: config.Maintenance, StoreID: maintenanceSecurityStoreID})
	if err != nil {
		t.Fatal(err)
	}
	maintenanceRoot := filepath.Join(fixture.root, "denied-first-maintenance")
	backend := maintenanceBackend(t, legacyStore, maintenanceRoot)
	service, err := maintenanceapp.NewService(backend, backend)
	if err != nil {
		t.Fatal(err)
	}
	command := maintenanceapp.Command{ID: "denied-first-daily", NowMillis: 10_000}
	first, err := service.ReconcileDaily(context.Background(), command)
	if !errors.Is(err, maintenanceapp.ErrNeedsYou) || len(first.State.Backups) != 1 ||
		first.State.Backups[0].Phase != domainmaintenance.BackupNeedsYou || first.State.Backups[0].NeedsYouCode != "backup_handoff_ambiguous" {
		t.Fatalf("denied first backup=%#v err=%v", first, err)
	}
	backup := first.State.Backups[0]
	backupDirectory := filepath.Join(maintenanceRoot, "maintenance", "backups", backup.ID)
	if entries, readErr := os.ReadDir(backupDirectory); readErr != nil || len(entries) != 0 {
		t.Fatalf("denied backup artifact is not exact empty: entries=%d err=%v", len(entries), readErr)
	}
	if err := legacyStore.Close(); err != nil {
		t.Fatal(err)
	}
	config = provisionSecurityAuthorities(t, fixture)
	store, err := dolt.Open(config)
	if err != nil {
		t.Fatal(err)
	}
	backend = maintenanceBackend(t, store, maintenanceRoot)
	lost := &responseLossBackend{Backend: backend, loseRearm: true, loseValidation: true}
	lossService, err := maintenanceapp.NewService(backend, lost)
	if err != nil {
		t.Fatal(err)
	}
	interrupted, err := lossService.ReconcileDaily(context.Background(), command)
	if !errors.Is(err, maintenanceapp.ErrExternalUnavailable) || interrupted.State.Backups[0].Phase != domainmaintenance.BackupValidationRequired ||
		interrupted.State.Backups[0].ValidationAttempt != 1 {
		t.Fatalf("response-loss frontier=%#v err=%v", interrupted, err)
	}
	receiptPath := filepath.Join(maintenanceRoot, "maintenance", "manifests", backup.ID+".validated.json")
	if info, statErr := os.Lstat(receiptPath); statErr != nil || !info.Mode().IsRegular() || info.Mode().Perm() != 0o600 {
		t.Fatalf("validation response-loss receipt is unsafe: info=%v err=%v", info, statErr)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	restartSecurityDolt(t, fixture)
	reopened, err := dolt.Open(config)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	backend = maintenanceBackend(t, reopened, maintenanceRoot)
	if receipt, receiptErr := backend.ValidateBackup(context.Background(), interrupted.State.Backups[0]); receiptErr != nil ||
		receipt.RestoredFingerprint != backup.SourceFingerprint {
		t.Fatalf("restart did not adopt validation receipt=%#v err=%v", receipt, receiptErr)
	}
	start := make(chan struct{})
	results := make(chan error, 32)
	for range 32 {
		coordinator, serviceErr := maintenanceapp.NewService(backend, backend)
		if serviceErr != nil {
			t.Fatal(serviceErr)
		}
		go func() {
			<-start
			_, reconcileErr := coordinator.ReconcileDaily(context.Background(), command)
			results <- reconcileErr
		}()
	}
	close(start)
	for range 32 {
		<-results
	}
	finalService, _ := maintenanceapp.NewService(backend, backend)
	final, err := finalService.ReconcileDaily(context.Background(), command)
	if err != nil || len(final.State.Backups) != 1 || final.State.Backups[0].Phase != domainmaintenance.BackupValidated ||
		final.State.Backups[0].Attempt != 2 || final.State.Backups[0].ValidationAttempt != 2 {
		t.Fatalf("restarted contended recovery=%#v err=%v", final, err)
	}
	codes := make([]domainmaintenance.RecoveryAuditCode, 0, len(final.State.RecoveryAudit))
	for _, entry := range final.State.RecoveryAudit {
		codes = append(codes, entry.Code)
	}
	if wanted := []domainmaintenance.RecoveryAuditCode{domainmaintenance.RecoveryRearmAuthorized, domainmaintenance.RecoveryRearmCompleted, domainmaintenance.RecoveryBackupValidated}; !slices.Equal(codes, wanted) {
		t.Fatalf("immutable recovery audit codes=%v", codes)
	}
	if entries, readErr := os.ReadDir(backupDirectory); readErr != nil || len(entries) == 0 {
		t.Fatalf("recovered backup artifact is not populated: entries=%d err=%v", len(entries), readErr)
	}
	for _, forbidden := range []string{backupDirectory + ".rearm", backupDirectory + ".expired"} {
		if _, statErr := os.Lstat(forbidden); !errors.Is(statErr, os.ErrNotExist) {
			t.Fatalf("unexpected cleanup artifact: %v", statErr)
		}
	}
	validationDatabase, _, _ := dolt.MaintenanceDatabaseNames(config.StoreID)
	inspection := securityRootDatabase(t, fixture)
	defer inspection.Close()
	var validationDatabases int
	if err := inspection.QueryRowContext(context.Background(), `SELECT COUNT(*) FROM information_schema.schemata WHERE schema_name=?`, validationDatabase).Scan(&validationDatabases); err != nil || validationDatabases != 0 {
		t.Fatalf("validation database cleanup count=%d err=%v", validationDatabases, err)
	}
}

func securityOrganizer(projectID string) *domain.Organizer {
	return &domain.Organizer{ID: domain.OrganizerID(projectID), Mode: domain.OrganizerModeAdopt, Phase: domain.OrganizerPhaseActive,
		RepositoryPath: "/srv/organizers/" + projectID, PreviewID: "preview-security", OperationID: "operation-security",
		HumanActorID: "human:security", ConfigurationSHA256: strings.Repeat("a", 64), OrganizerRevision: strings.Repeat("b", 40)}
}

func securityWorkspace(t *testing.T, projectID string) domain.Workspace {
	t.Helper()
	remote, err := repositorydomain.CanonicalRemote("https://github.com/example/security.git")
	if err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256([]byte(projectID))
	return domain.Workspace{ID: domain.WorkspaceID(projectID, "security-workspace"), ProjectID: projectID,
		Key: "security-workspace", Name: "Security workspace", Repository: domain.RepositoryIdentity{
			ID: remote.ID, Key: remote.Key, CanonicalRemote: remote.Canonical, SourcePath: "/srv/security",
			SourceDevice: 1, SourceInode: binary.BigEndian.Uint64(digest[:8]) | 1,
			GitCommonDirectory: "/srv/security/.git", GitCommonDevice: 1,
			GitCommonInode: binary.BigEndian.Uint64(digest[8:16]) | 1,
		}, DefaultBaseBranch: "main", Policy: domain.WorkspacePolicy{LaunchPolicy: "inherit", DeliveryMode: "inherit"}}
}

func makeStoreNonEmpty(t *testing.T, store *dolt.DoltTaskStore) {
	t.Helper()
	project := domain.Project{ID: "nonempty-project", Name: "Nonempty project", State: "active", Organizer: securityOrganizer("nonempty-project")}
	workspace := securityWorkspace(t, project.ID)
	result, err := store.CreateProject(context.Background(), domain.CommandRequest{IdempotencyKey: "nonempty-create", Type: "project.create",
		AggregateID: project.ID, Payload: json.RawMessage(`{}`)}, project, []domain.Workspace{workspace}, domain.Event{ID: "nonempty-event",
		Sequence: 1, AggregateID: project.ID, Type: "project.created", Payload: json.RawMessage(`{"source":"security"}`)})
	if err != nil || result.Outcome != domain.CommandApplied {
		t.Fatalf("make store non-empty result=%#v err=%v", result, err)
	}
}

func TestTaskStoreDeniedFirstBackupRefusesChangedStoreAndFilesystemAttacks(t *testing.T) {
	for name, mutate := range map[string]func(*testing.T, *dolt.DoltTaskStore, string){
		"non-empty store": func(t *testing.T, store *dolt.DoltTaskStore, _ string) { makeStoreNonEmpty(t, store) },
		"mode drift": func(t *testing.T, _ *dolt.DoltTaskStore, directory string) {
			if err := os.Chmod(directory, 0o755); err != nil {
				t.Fatal(err)
			}
		},
		"symlink replacement": func(t *testing.T, _ *dolt.DoltTaskStore, directory string) {
			foreign := filepath.Join(filepath.Dir(filepath.Dir(directory)), "foreign")
			if err := os.Mkdir(foreign, 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(foreign, "owned-by-someone-else"), []byte("preserve"), 0o600); err != nil {
				t.Fatal(err)
			}
			if err := os.Remove(directory); err != nil || os.Symlink(foreign, directory) != nil {
				t.Fatal("replace empty artifact with symlink")
			}
		},
	} {
		t.Run(name, func(t *testing.T) {
			fixture := startOrganizerDoltFixture(t)
			config := provisionSecurityAuthorities(t, fixture)
			setExactBackupGrant(t, fixture, false)
			legacyEndpoint := dolt.Endpoint{Address: fixture.address, Database: fixture.database, User: "root"}
			legacyStore, err := dolt.Open(dolt.Config{Control: legacyEndpoint, Writer: legacyEndpoint, Maintenance: config.Maintenance, StoreID: maintenanceSecurityStoreID})
			if err != nil {
				t.Fatal(err)
			}
			maintenanceRoot := filepath.Join(fixture.root, "refusal-maintenance")
			backend := maintenanceBackend(t, legacyStore, maintenanceRoot)
			service, _ := maintenanceapp.NewService(backend, backend)
			command := maintenanceapp.Command{ID: "refusal-daily", NowMillis: 10_000}
			first, err := service.ReconcileDaily(context.Background(), command)
			if !errors.Is(err, maintenanceapp.ErrNeedsYou) {
				t.Fatalf("initial denial=%v", err)
			}
			directory := filepath.Join(maintenanceRoot, "maintenance", "backups", first.State.Backups[0].ID)
			mutate(t, legacyStore, directory)
			if err := legacyStore.Close(); err != nil {
				t.Fatal(err)
			}
			config = provisionSecurityAuthorities(t, fixture)
			store, err := dolt.Open(config)
			if err != nil {
				t.Fatal(err)
			}
			defer store.Close()
			backend = maintenanceBackend(t, store, maintenanceRoot)
			service, _ = maintenanceapp.NewService(backend, backend)
			refused, err := service.ReconcileDaily(context.Background(), command)
			if !errors.Is(err, maintenanceapp.ErrNeedsYou) || refused.State.Backups[0].Phase != domainmaintenance.BackupNeedsYou ||
				len(refused.State.RecoveryAudit) != 0 {
				t.Fatalf("unsafe recovery=%#v err=%v", refused, err)
			}
			if name == "symlink replacement" {
				content, readErr := os.ReadFile(filepath.Join(filepath.Dir(filepath.Dir(directory)), "foreign", "owned-by-someone-else"))
				if readErr != nil || string(content) != "preserve" {
					t.Fatalf("foreign content changed: %q err=%v", content, readErr)
				}
			}
		})
	}
}
