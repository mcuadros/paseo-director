// SPDX-License-Identifier: Apache-2.0

package dolt

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"sort"
	"strings"
	"syscall"
	"time"

	"github.com/mcuadros/director-engine/domain"
	"github.com/mcuadros/director-engine/domain/execution"
	domainmaintenance "github.com/mcuadros/director-engine/domain/taskstoremaintenance"
	maintenanceport "github.com/mcuadros/director-engine/ports/taskstoremaintenance"
)

const maximumBackupManifestBytes = 16 * 1024

type LogCompactor interface {
	CompactExpired(context.Context, int64) error
}

// Maintenance owns the private durable maintenance ledger and performs only
// exact effects authorized by the application service. Paths, credentials,
// SQL diagnostics, and filesystem identities never cross this adapter.
type Maintenance struct {
	store    *DoltTaskStore
	root     string
	identity maintenanceRootIdentity
	binding  string
	logs     LogCompactor
	now      func() time.Time
	statfs   func(string, *syscall.Statfs_t) error
}

func maintenanceStoreBinding(store *DoltTaskStore) string {
	digest := sha256.Sum256([]byte(strings.Join([]string{store.storeID, store.address, store.database}, "\x1f")))
	return hex.EncodeToString(digest[:])
}

var (
	_ maintenanceport.StateStore = (*Maintenance)(nil)
	_ maintenanceport.Backend    = (*Maintenance)(nil)
)

func DefaultMaintenanceRoot(storeID string) (string, error) {
	base, err := os.UserCacheDir()
	if err != nil || !filepath.IsAbs(base) || !safeIdentifier(storeID, 128) {
		return "", ErrMaintenanceUnsafe
	}
	digest := sha256.Sum256([]byte("director-taskstore-maintenance\x1f" + storeID))
	return filepath.Join(base, "director", "taskstore", hex.EncodeToString(digest[:16])), nil
}

func ensurePrivateDirectory(path string) (maintenanceRootIdentity, error) {
	if !filepath.IsAbs(path) || filepath.Clean(path) != path {
		return maintenanceRootIdentity{}, ErrMaintenanceUnsafe
	}
	if err := os.MkdirAll(path, 0o700); err != nil {
		return maintenanceRootIdentity{}, ErrMaintenanceUnsafe
	}
	resolved, err := filepath.EvalSymlinks(path)
	if err != nil || resolved != path {
		return maintenanceRootIdentity{}, ErrMaintenanceUnsafe
	}
	return exactOwnedDirectory(path)
}

func NewMaintenance(store *DoltTaskStore, root string, logs LogCompactor) (*Maintenance, error) {
	if store == nil || logs == nil {
		return nil, ErrMaintenanceUnsafe
	}
	identity, err := ensurePrivateDirectory(root)
	if err != nil {
		return nil, err
	}
	for _, child := range []string{"backups", "manifests"} {
		if _, err := ensurePrivateDirectory(filepath.Join(root, child)); err != nil {
			return nil, err
		}
	}
	maintenance := &Maintenance{store: store, root: root, identity: identity, binding: maintenanceStoreBinding(store), logs: logs, now: time.Now, statfs: syscall.Statfs}
	if err := maintenance.initializeState(context.Background()); err != nil {
		return nil, err
	}
	return maintenance, nil
}

type backupManifest struct {
	SchemaVersion     string `json:"schemaVersion"`
	ID                string `json:"id"`
	SourceVersion     int    `json:"sourceSchemaVersion"`
	SourceFingerprint string `json:"sourceFingerprint"`
	Device            uint64 `json:"device"`
	Inode             uint64 `json:"inode"`
	SHA256            string `json:"sha256"`
}

func manifestValue(value backupManifest) backupManifest { value.SHA256 = ""; return value }

func manifestSHA(value backupManifest) string {
	document, _ := json.Marshal(manifestValue(value))
	digest := sha256.Sum256(document)
	return hex.EncodeToString(digest[:])
}

func validManifest(value backupManifest) bool {
	return value.SchemaVersion == "director.taskstore-backup-manifest/v1" && safeIdentifier(value.ID, 128) &&
		value.SourceVersion > 0 && len(value.SourceFingerprint) == 64 && value.Device != 0 && value.Inode != 0 &&
		value.SHA256 == manifestSHA(value)
}

func backupPaths(maintenance *Maintenance, id string) (string, string, bool) {
	if !safeIdentifier(id, 128) || strings.Contains(id, "/") {
		return "", "", false
	}
	return filepath.Join(maintenance.root, "backups", id), filepath.Join(maintenance.root, "manifests", id+".json"), true
}

func readManifest(path string) (backupManifest, error) {
	before, err := exactOwnedFile(path, maximumBackupManifestBytes)
	if err != nil {
		return backupManifest{}, err
	}
	document, err := os.ReadFile(path)
	if err != nil {
		return backupManifest{}, ErrMaintenanceUnsafe
	}
	after, err := exactOwnedFile(path, maximumBackupManifestBytes)
	if err != nil || !os.SameFile(before, after) || before.Size() != after.Size() || before.ModTime() != after.ModTime() || !json.Valid(document) {
		return backupManifest{}, ErrMaintenanceUnsafe
	}
	decoder := json.NewDecoder(bytes.NewReader(document))
	decoder.DisallowUnknownFields()
	var value backupManifest
	if decoder.Decode(&value) != nil {
		return backupManifest{}, ErrMaintenanceUnsafe
	}
	if _, err := decoder.Token(); !errors.Is(err, io.EOF) || !validManifest(value) {
		return backupManifest{}, ErrMaintenanceUnsafe
	}
	return value, nil
}

func writeExclusivePrivate(path string, value any) error {
	document, err := json.Marshal(value)
	if err != nil || len(document) == 0 || len(document) > maximumBackupManifestBytes {
		return ErrMaintenanceUnsafe
	}
	file, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return ErrMaintenanceUnsafe
	}
	if _, err := file.Write(document); err != nil || file.Sync() != nil || file.Close() != nil {
		return ErrMaintenanceUnsafe
	}
	return syncDirectory(filepath.Dir(path))
}

func directoryDeviceInode(path string) (uint64, uint64, error) {
	identity, err := exactOwnedDirectory(path)
	if err != nil {
		return 0, 0, err
	}
	return identity.device, identity.inode, nil
}

func hashQuery(ctx context.Context, digest interface{ Write([]byte) (int, error) }, connection *sql.Conn, label, query string, arguments ...any) error {
	rows, err := connection.QueryContext(ctx, query, arguments...)
	if err != nil {
		return ErrMaintenanceUnsafe
	}
	defer rows.Close()
	columns, err := rows.Columns()
	if err != nil {
		return ErrMaintenanceUnsafe
	}
	writeFrame := func(value []byte) {
		var size [8]byte
		binary.BigEndian.PutUint64(size[:], uint64(len(value)))
		_, _ = digest.Write(size[:])
		_, _ = digest.Write(value)
	}
	writeFrame([]byte(label))
	for _, column := range columns {
		writeFrame([]byte(column))
	}
	for rows.Next() {
		values := make([]sql.RawBytes, len(columns))
		destinations := make([]any, len(values))
		for index := range values {
			destinations[index] = &values[index]
		}
		if rows.Scan(destinations...) != nil {
			return ErrMaintenanceUnsafe
		}
		for _, value := range values {
			if value == nil {
				writeFrame([]byte{0})
			} else {
				writeFrame(append([]byte{1}, value...))
			}
		}
	}
	if rows.Err() != nil || rows.Close() != nil {
		return ErrMaintenanceUnsafe
	}
	return nil
}

func primaryKeyColumns(ctx context.Context, connection *sql.Conn, table string) ([]string, error) {
	rows, err := connection.QueryContext(ctx, `SELECT column_name FROM information_schema.statistics
		WHERE table_schema=DATABASE() AND table_name=? AND index_name='PRIMARY' ORDER BY seq_in_index`, table)
	if err != nil {
		return nil, ErrMaintenanceUnsafe
	}
	defer rows.Close()
	var columns []string
	for rows.Next() {
		var column string
		if rows.Scan(&column) != nil || !safeIdentifier(column, 128) || strings.Contains(column, "/") {
			return nil, ErrMaintenanceUnsafe
		}
		columns = append(columns, column)
	}
	if rows.Err() != nil || len(columns) == 0 {
		return nil, ErrMaintenanceUnsafe
	}
	return columns, nil
}

func hashTableRows(ctx context.Context, digest interface{ Write([]byte) (int, error) }, connection *sql.Conn, table string) error {
	if !safeIdentifier(table, 128) || strings.Contains(table, "/") {
		return ErrMaintenanceUnsafe
	}
	keys, err := primaryKeyColumns(ctx, connection, table)
	if err != nil {
		return err
	}
	quotedKeys := make([]string, 0, len(keys))
	for _, key := range keys {
		quotedKeys = append(quotedKeys, quoteDatabase(key))
	}
	query := `SELECT * FROM ` + quoteDatabase(table) + ` ORDER BY ` + strings.Join(quotedKeys, ",")
	return hashQuery(ctx, digest, connection, "rows:"+table, query)
}

func exactCandidateColumns(ctx context.Context, connection *sql.Conn, version int) bool {
	rows, err := connection.QueryContext(ctx, `
		SELECT column_name, column_type, is_nullable, COALESCE(character_set_name,''), COALESCE(collation_name,'')
		FROM information_schema.columns WHERE table_schema=DATABASE() AND table_name='candidates' ORDER BY ordinal_position`)
	if err != nil {
		return false
	}
	defer rows.Close()
	var actual []string
	for rows.Next() {
		var name, columnType, nullable, charset, collation string
		if rows.Scan(&name, &columnType, &nullable, &charset, &collation) != nil {
			return false
		}
		actual = append(actual, strings.Join([]string{name, strings.ToLower(columnType), nullable, charset, collation}, "\x1f"))
	}
	if rows.Err() != nil {
		return false
	}
	expected := []string{
		"id\x1fvarchar(128)\x1fNO\x1futf8mb4\x1futf8mb4_0900_bin",
		"run_id\x1fvarchar(128)\x1fNO\x1futf8mb4\x1futf8mb4_0900_bin",
		"sequence\x1fbigint unsigned\x1fNO\x1f\x1f",
	}
	if version == 1 {
		expected = append(expected, "commit_sha\x1fchar(40)\x1fNO\x1fascii\x1fascii_bin")
	} else if version == 2 {
		expected = append(expected, "commit_sha\x1fvarchar(64)\x1fNO\x1fascii\x1fascii_bin", "data\x1fjson\x1fNO\x1f\x1f")
	} else {
		return false
	}
	return slices.Equal(actual, expected)
}

func exactAggregateIdentity(ctx context.Context, connection *sql.Conn) bool {
	var dataType, columnType, nullable, collation, key string
	var maximum sql.NullInt64
	err := connection.QueryRowContext(ctx, `
		SELECT data_type,column_type,is_nullable,COALESCE(collation_name,''),column_key,character_octet_length
		FROM information_schema.columns WHERE table_schema=DATABASE() AND table_name='aggregates' AND column_name='id'`,
	).Scan(&dataType, &columnType, &nullable, &collation, &key, &maximum)
	if err != nil || dataType != "varchar" || strings.ToLower(columnType) != "varchar(128)" || nullable != "NO" ||
		collation != "utf8mb4_0900_bin" || key != "PRI" || !maximum.Valid || maximum.Int64 != 512 {
		return false
	}
	var columns, nonUnique, nullableIndex int
	err = connection.QueryRowContext(ctx, `
		SELECT COUNT(*),COALESCE(MAX(non_unique),1),SUM(nullable='YES' OR sub_part IS NOT NULL OR expression IS NOT NULL)
		FROM information_schema.statistics WHERE table_schema=DATABASE() AND table_name='aggregates' AND index_name='PRIMARY'`,
	).Scan(&columns, &nonUnique, &nullableIndex)
	return err == nil && columns == 1 && nonUnique == 0 && nullableIndex == 0
}

func rawSchemaVersion(ctx context.Context, connection *sql.Conn) (int, error) {
	var version int
	if err := connection.QueryRowContext(ctx, `SELECT schema_version FROM schema_metadata WHERE singleton=1`).Scan(&version); err != nil {
		return 0, ErrMaintenanceUnsafe
	}
	return version, nil
}

func (maintenance *Maintenance) observeOn(ctx context.Context, connection *sql.Conn, requirePrimary bool) (domainmaintenance.StoreObservation, error) {
	var database, versionText, storeID string
	if connection.QueryRowContext(ctx, `SELECT DATABASE(),DOLT_VERSION()`).Scan(&database, &versionText) != nil ||
		versionText != supportedDoltVersion || connection.QueryRowContext(ctx, `SELECT store_id FROM taskstore_identity WHERE singleton=1`).Scan(&storeID) != nil ||
		storeID != maintenance.store.storeID || (requirePrimary && database != maintenance.store.database) {
		return domainmaintenance.StoreObservation{}, ErrMaintenanceUnsafe
	}
	version, err := rawSchemaVersion(ctx, connection)
	if err != nil {
		return domainmaintenance.StoreObservation{}, err
	}
	digest := sha256.New()
	queries := []struct{ label, query string }{
		{"columns", `SELECT table_name,column_name,ordinal_position,column_type,is_nullable,column_key,COALESCE(character_set_name,''),COALESCE(collation_name,'') FROM information_schema.columns WHERE table_schema=DATABASE() ORDER BY table_name,ordinal_position`},
		{"statistics", `SELECT table_name,index_name,non_unique,seq_in_index,COALESCE(column_name,''),COALESCE(sub_part,0),nullable,COALESCE(expression,'') FROM information_schema.statistics WHERE table_schema=DATABASE() ORDER BY table_name,index_name,seq_in_index`},
		{"triggers", `SELECT trigger_name,event_manipulation,event_object_table,action_timing,action_statement FROM information_schema.triggers WHERE trigger_schema=DATABASE() ORDER BY trigger_name`},
		{"branches", `SELECT name,hash FROM dolt_branches ORDER BY name`},
		{"status", `SELECT table_name,staged,status FROM dolt_status ORDER BY table_name,staged,status`},
		{"identity", `SELECT singleton,store_id FROM taskstore_identity ORDER BY singleton`},
		{"metadata", `SELECT singleton,schema_version FROM schema_metadata ORDER BY singleton`},
	}
	for _, query := range queries {
		if err := hashQuery(ctx, digest, connection, query.label, query.query); err != nil {
			return domainmaintenance.StoreObservation{}, err
		}
	}
	tables, err := schemaTables(ctx, connection)
	if err != nil {
		return domainmaintenance.StoreObservation{}, ErrMaintenanceUnsafe
	}
	sort.Strings(tables)
	for _, table := range tables {
		if err := hashTableRows(ctx, digest, connection, table); err != nil {
			return domainmaintenance.StoreObservation{}, err
		}
	}
	exact := (version == 1 || version == 2) && verifyCompleteSchema(ctx, connection) == nil &&
		exactCandidateColumns(ctx, connection, version) && exactAggregateIdentity(ctx, connection)
	return domainmaintenance.StoreObservation{SchemaVersion: version, Fingerprint: hex.EncodeToString(digest.Sum(nil)), Exact: exact}, nil
}

func (maintenance *Maintenance) controlConnection(ctx context.Context) (*sql.Conn, error) {
	connection, err := maintenance.store.control.Conn(ctx)
	if err != nil {
		return nil, ErrMaintenanceUnsafe
	}
	if setAndVerifySafeCommitMode(ctx, connection, true) != nil || maintenance.store.verifyStoreIdentity(ctx, connection) != nil {
		connection.Close()
		return nil, ErrMaintenanceUnsafe
	}
	return connection, nil
}

func (maintenance *Maintenance) ObserveStore(ctx context.Context) (domainmaintenance.StoreObservation, error) {
	maintenance.store.maintenance.RLock()
	defer maintenance.store.maintenance.RUnlock()
	connection, err := maintenance.controlConnection(ctx)
	if err != nil {
		return domainmaintenance.StoreObservation{}, err
	}
	defer connection.Close()
	return maintenance.observeOn(ctx, connection, true)
}

func (maintenance *Maintenance) ObserveBackup(ctx context.Context, id string) (maintenanceport.BackupObservation, error) {
	if err := ctx.Err(); err != nil {
		return maintenanceport.BackupObservation{}, err
	}
	if maintenance.verifyRoot() != nil {
		return maintenanceport.BackupObservation{}, ErrMaintenanceUnsafe
	}
	directory, manifestPath, ok := backupPaths(maintenance, id)
	if !ok {
		return maintenanceport.BackupObservation{}, ErrMaintenanceUnsafe
	}
	manifest, manifestErr := readManifest(manifestPath)
	identity, directoryErr := exactOwnedDirectory(directory)
	if errors.Is(manifestErr, os.ErrNotExist) && errors.Is(directoryErr, os.ErrNotExist) {
		return maintenanceport.BackupObservation{Exact: true, PriorDispatcherAbsent: true}, nil
	}
	if manifestErr != nil || directoryErr != nil || manifest.ID != id || manifest.Device != identity.device || manifest.Inode != identity.inode {
		return maintenanceport.BackupObservation{}, nil
	}
	entries, err := os.ReadDir(directory)
	if err != nil || len(entries) == 0 {
		return maintenanceport.BackupObservation{Present: true, Source: domainmaintenance.StoreObservation{
			SchemaVersion: manifest.SourceVersion, Fingerprint: manifest.SourceFingerprint,
		}}, nil
	}
	return maintenanceport.BackupObservation{Present: true, Exact: true, Source: domainmaintenance.StoreObservation{
		SchemaVersion: manifest.SourceVersion, Fingerprint: manifest.SourceFingerprint, Exact: true,
	}}, nil
}

func (maintenance *Maintenance) CreateBackup(ctx context.Context, backup domainmaintenance.Backup) (maintenanceport.BackupResult, error) {
	maintenance.store.maintenance.Lock()
	defer maintenance.store.maintenance.Unlock()
	existing, err := maintenance.ObserveBackup(ctx, backup.ID)
	if err != nil {
		return maintenanceport.BackupResult{}, err
	}
	if existing.Present {
		if backupMatchesManifest(existing, backup) {
			return maintenanceport.BackupResult{Source: existing.Source}, nil
		}
		return maintenanceport.BackupResult{}, ErrMaintenanceUnsafe
	}
	if !existing.Exact || !existing.PriorDispatcherAbsent {
		return maintenanceport.BackupResult{}, ErrMaintenanceUnsafe
	}
	connection, err := maintenance.controlConnection(ctx)
	if err != nil {
		return maintenanceport.BackupResult{}, err
	}
	defer connection.Close()
	source, err := maintenance.observeOn(ctx, connection, true)
	if err != nil || !source.Exact || source.SchemaVersion != backup.SourceSchemaVersion || source.Fingerprint != backup.SourceFingerprint {
		return maintenanceport.BackupResult{}, ErrMaintenanceUnsafe
	}
	directory, manifestPath, _ := backupPaths(maintenance, backup.ID)
	if err := os.Mkdir(directory, 0o700); err != nil {
		return maintenanceport.BackupResult{}, ErrMaintenanceUnsafe
	}
	device, inode, err := directoryDeviceInode(directory)
	if err != nil {
		return maintenanceport.BackupResult{}, err
	}
	manifest := backupManifest{SchemaVersion: "director.taskstore-backup-manifest/v1", ID: backup.ID,
		SourceVersion: source.SchemaVersion, SourceFingerprint: source.Fingerprint, Device: device, Inode: inode}
	manifest.SHA256 = manifestSHA(manifest)
	if err := writeExclusivePrivate(manifestPath, manifest); err != nil {
		return maintenanceport.BackupResult{}, err
	}
	remote := (&url.URL{Scheme: "file", Path: directory}).String()
	if _, err := connection.ExecContext(ctx, `CALL DOLT_BACKUP('sync-url', ?)`, remote); err != nil {
		return maintenanceport.BackupResult{}, ErrMaintenanceUnsafe
	}
	return maintenanceport.BackupResult{Source: source}, nil
}

func backupMatchesManifest(observation maintenanceport.BackupObservation, backup domainmaintenance.Backup) bool {
	return observation.Present && observation.Exact && observation.Source.Exact &&
		observation.Source.SchemaVersion == backup.SourceSchemaVersion && observation.Source.Fingerprint == backup.SourceFingerprint
}

func generatedDatabase(prefix, id string) string {
	digest := sha256.Sum256([]byte(prefix + "\x1f" + id))
	return prefix + "_" + hex.EncodeToString(digest[:16])
}

func quoteDatabase(value string) string { return "`" + value + "`" }

func databaseExists(ctx context.Context, connection *sql.Conn, database string) (bool, error) {
	var count int
	if err := connection.QueryRowContext(ctx, `SELECT COUNT(*) FROM information_schema.schemata WHERE schema_name=?`, database).Scan(&count); err != nil {
		return false, ErrMaintenanceUnsafe
	}
	return count == 1, nil
}

func (maintenance *Maintenance) restoreAndObserve(ctx context.Context, backup domainmaintenance.Backup, database string, keep bool) (domainmaintenance.StoreObservation, error) {
	observed, err := maintenance.ObserveBackup(ctx, backup.ID)
	if err != nil || !backupMatchesManifest(observed, backup) {
		return domainmaintenance.StoreObservation{}, ErrMaintenanceUnsafe
	}
	connection, err := maintenance.controlConnection(ctx)
	if err != nil {
		return domainmaintenance.StoreObservation{}, err
	}
	defer connection.Close()
	exists, err := databaseExists(ctx, connection, database)
	if err != nil {
		return domainmaintenance.StoreObservation{}, err
	}
	if !exists {
		directory, _, _ := backupPaths(maintenance, backup.ID)
		remote := (&url.URL{Scheme: "file", Path: directory}).String()
		if _, err := connection.ExecContext(ctx, `CALL DOLT_BACKUP('restore', ?, ?)`, remote, database); err != nil {
			return domainmaintenance.StoreObservation{}, ErrMaintenanceUnsafe
		}
	}
	if _, err := connection.ExecContext(ctx, `USE `+quoteDatabase(database)); err != nil {
		return domainmaintenance.StoreObservation{}, ErrMaintenanceUnsafe
	}
	restored, observeErr := maintenance.observeOn(ctx, connection, false)
	if observeErr == nil {
		if _, err := connection.ExecContext(ctx, `CALL DOLT_VERIFY_CONSTRAINTS()`); err != nil {
			observeErr = ErrMaintenanceUnsafe
		}
	}
	if _, err := connection.ExecContext(ctx, `USE `+quoteDatabase(maintenance.store.database)); err != nil {
		return domainmaintenance.StoreObservation{}, ErrMaintenanceUnsafe
	}
	if observeErr != nil || !restored.Exact || restored.SchemaVersion != backup.SourceSchemaVersion || restored.Fingerprint != backup.SourceFingerprint {
		return restored, ErrMaintenanceUnsafe
	}
	if !keep {
		if _, err := connection.ExecContext(ctx, `DROP DATABASE `+quoteDatabase(database)); err != nil {
			return restored, ErrMaintenanceUnsafe
		}
	}
	return restored, nil
}

func (maintenance *Maintenance) ValidateBackup(ctx context.Context, backup domainmaintenance.Backup) (maintenanceport.ValidationResult, error) {
	database := generatedDatabase("director_validate", backup.ID)
	restored, err := maintenance.restoreAndObserve(ctx, backup, database, false)
	return maintenanceport.ValidationResult{Source: domainmaintenance.StoreObservation{
		SchemaVersion: backup.SourceSchemaVersion, Fingerprint: backup.SourceFingerprint, Exact: err == nil,
	}, RestoredFingerprint: restored.Fingerprint}, err
}

func (maintenance *Maintenance) currentProjects(ctx context.Context, connection *sql.Conn) ([]domain.Project, error) {
	rows, err := connection.QueryContext(ctx, `SELECT id,version,data FROM aggregates WHERE kind=? ORDER BY id`, aggregateProject)
	if err != nil {
		return nil, ErrMaintenanceUnsafe
	}
	defer rows.Close()
	var projects []domain.Project
	for rows.Next() {
		var project domain.Project
		var document []byte
		if rows.Scan(&project.ID, &project.Version, &document) != nil {
			return nil, ErrMaintenanceUnsafe
		}
		var data projectData
		if decodeRecord(document, &data) != nil {
			return nil, ErrMaintenanceUnsafe
		}
		project.Name, project.State, project.Organizer = data.Name, data.State, reloadedOrganizer(data.Organizer)
		project.LastLeaseEpoch, project.Lease, project.LeaseObservation, project.Control = data.LastLeaseEpoch, storedLease(data.Lease), storedLeaseObservation(data.LeaseObservation), data.Control
		if validateProject(project) != nil {
			return nil, ErrMaintenanceUnsafe
		}
		projects = append(projects, project)
	}
	if rows.Err() != nil {
		return nil, ErrMaintenanceUnsafe
	}
	return projects, nil
}

func bindingForProject(project domain.Project) domainmaintenance.ProjectBinding {
	paused := project.Version
	if project.State == "active" || project.State == "degraded" {
		paused++
	}
	return domainmaintenance.ProjectBinding{ID: project.ID, OriginalState: project.State, OriginalVersion: project.Version, PausedVersion: paused}
}

func (maintenance *Maintenance) PlanProjectPause(ctx context.Context) ([]domainmaintenance.ProjectBinding, error) {
	maintenance.store.maintenance.RLock()
	defer maintenance.store.maintenance.RUnlock()
	connection, err := maintenance.controlConnection(ctx)
	if err != nil {
		return nil, err
	}
	defer connection.Close()
	observation, err := maintenance.observeOn(ctx, connection, true)
	if err != nil || !observation.Exact {
		return nil, ErrMaintenanceUnsafe
	}
	projects, err := maintenance.currentProjects(ctx, connection)
	if err != nil || len(projects) > domainmaintenance.MaximumProjects {
		return nil, ErrMaintenanceUnsafe
	}
	bindings := make([]domainmaintenance.ProjectBinding, 0, len(projects))
	for _, project := range projects {
		bindings = append(bindings, bindingForProject(project))
	}
	return bindings, nil
}

func sameBindings(left, right []domainmaintenance.ProjectBinding) bool {
	return slices.Equal(left, right)
}

func maintenanceCommandID(migrationID, operation, projectID string) string {
	digest := sha256.Sum256([]byte(migrationID + "\x1f" + operation + "\x1f" + projectID))
	return "taskstore-maintenance-" + hex.EncodeToString(digest[:16])
}

func (maintenance *Maintenance) updateProjectForMigration(ctx context.Context, tx *sql.Tx, migration domainmaintenance.Migration, binding domainmaintenance.ProjectBinding, resume bool) error {
	current, err := projectByIDForUpdate(ctx, tx, binding.ID)
	if err != nil {
		return ErrMaintenanceUnsafe
	}
	desiredState, expectedVersion, desiredVersion, operation := "paused", binding.OriginalVersion, binding.PausedVersion, "pause"
	if resume {
		desiredState, expectedVersion, desiredVersion, operation = binding.OriginalState, binding.PausedVersion, binding.PausedVersion+1, "resume"
	}
	unchanged := binding.OriginalState == "paused" || binding.OriginalState == "archived"
	if unchanged {
		if current.State != binding.OriginalState || current.Version != binding.OriginalVersion {
			return ErrMaintenanceUnsafe
		}
		return nil
	}
	if current.State == desiredState && current.Version == desiredVersion {
		return nil
	}
	wantedCurrent := binding.OriginalState
	if resume {
		wantedCurrent = "paused"
	}
	if current.State != wantedCurrent || current.Version != expectedVersion {
		return ErrMaintenanceUnsafe
	}
	current.State, current.Version = desiredState, desiredVersion
	document, err := encodeProject(current)
	if err != nil {
		return ErrMaintenanceUnsafe
	}
	commandID := maintenanceCommandID(migration.ID, operation, binding.ID)
	payload, _ := json.Marshal(struct {
		MigrationID string `json:"migrationId"`
		State       string `json:"state"`
	}{migration.ID, desiredState})
	command, err := prepareCommand(domain.CommandRequest{IdempotencyKey: commandID, Type: "project.control.maintenance_" + operation,
		AggregateID: binding.ID, ExpectedVersion: expectedVersion, Payload: payload})
	if err != nil {
		return ErrMaintenanceUnsafe
	}
	event, err := prepareEvent(domain.Event{ID: "event-" + commandID, Sequence: desiredVersion + 1,
		AggregateID: binding.ID, AggregateVersion: desiredVersion, Type: "project.maintenance_" + operation + "d", Payload: payload}, command.request)
	if err != nil {
		return ErrMaintenanceUnsafe
	}
	if insertCommand(ctx, tx, command) != nil {
		return ErrMaintenanceUnsafe
	}
	result, err := tx.ExecContext(ctx, `UPDATE aggregates SET version=?,data=? WHERE id=? AND kind=? AND version=?`,
		desiredVersion, document, binding.ID, aggregateProject, expectedVersion)
	if err != nil {
		return ErrMaintenanceUnsafe
	}
	rows, err := result.RowsAffected()
	if err != nil || rows != 1 || appendEvent(ctx, tx, event, desiredVersion) != nil ||
		insertOutcome(ctx, tx, command, mutationResult{outcome: domain.CommandApplied, observedVersion: desiredVersion}, event.event.ID) != nil {
		return ErrMaintenanceUnsafe
	}
	return nil
}

func (maintenance *Maintenance) mutateProjects(ctx context.Context, migration domainmaintenance.Migration, resume bool) error {
	connection, err := maintenance.controlConnection(ctx)
	if err != nil {
		return err
	}
	defer connection.Close()
	observation, err := maintenance.observeOn(ctx, connection, true)
	expectedVersion := migration.FromVersion
	if resume {
		expectedVersion = migration.ToVersion
	}
	if err != nil || !observation.Exact || observation.SchemaVersion != expectedVersion {
		return ErrMaintenanceUnsafe
	}
	tx, err := connection.BeginTx(ctx, nil)
	if err != nil {
		return ErrMaintenanceUnsafe
	}
	for _, binding := range migration.Projects {
		if err := maintenance.updateProjectForMigration(ctx, tx, migration, binding, resume); err != nil {
			_ = tx.Rollback()
			return err
		}
	}
	if err := tx.Commit(); err != nil {
		return ErrMaintenanceUnsafe
	}
	return nil
}

func (maintenance *Maintenance) PauseProjects(ctx context.Context, migration domainmaintenance.Migration) ([]domainmaintenance.ProjectBinding, error) {
	maintenance.store.maintenance.Lock()
	defer maintenance.store.maintenance.Unlock()
	if err := maintenance.mutateProjects(ctx, migration, false); err != nil {
		return nil, err
	}
	return slices.Clone(migration.Projects), nil
}

func (maintenance *Maintenance) observeProjectState(ctx context.Context, migration domainmaintenance.Migration, resumed bool) ([]domainmaintenance.ProjectBinding, bool, error) {
	connection, err := maintenance.controlConnection(ctx)
	if err != nil {
		return nil, false, err
	}
	defer connection.Close()
	projects, err := maintenance.currentProjects(ctx, connection)
	if err != nil || len(projects) != len(migration.Projects) {
		return nil, false, err
	}
	byID := make(map[string]domain.Project, len(projects))
	for _, project := range projects {
		byID[project.ID] = project
	}
	for _, binding := range migration.Projects {
		project, ok := byID[binding.ID]
		if !ok {
			return slices.Clone(migration.Projects), false, nil
		}
		state, version := "paused", binding.PausedVersion
		if binding.OriginalState == "paused" || binding.OriginalState == "archived" {
			state, version = binding.OriginalState, binding.OriginalVersion
		} else if resumed {
			state, version = binding.OriginalState, binding.PausedVersion+1
		}
		if project.State != state || project.Version != version {
			return slices.Clone(migration.Projects), false, nil
		}
	}
	return slices.Clone(migration.Projects), true, nil
}

func (maintenance *Maintenance) ObserveProjectsPaused(ctx context.Context, migration domainmaintenance.Migration) ([]domainmaintenance.ProjectBinding, bool, error) {
	maintenance.store.maintenance.RLock()
	defer maintenance.store.maintenance.RUnlock()
	return maintenance.observeProjectState(ctx, migration, false)
}

func legacyCandidateJSON(migrationID, id, runID string, sequence uint64, commitSHA string) ([]byte, error) {
	return json.Marshal(domain.Candidate{SchemaVersion: domain.LegacyCandidateSchemaVersion, ID: id, RunID: runID,
		Sequence: sequence, CommitSHA: commitSHA, LegacyMigration: &domain.LegacyCandidateMigration{
			MigrationID: migrationID, SourceSchemaVersion: 1, Authoritative: false,
		}})
}

func migrationTriggerStatement() (string, bool) {
	for _, statement := range schemaStatements {
		if strings.HasPrefix(strings.TrimSpace(statement), "CREATE TRIGGER candidates_reject_update ") {
			return statement, true
		}
	}
	return "", false
}

type legacyCandidateRow struct {
	id, runID, commitSHA string
	sequence             uint64
}

func migrateRunDocument(document []byte) ([]byte, bool, error) {
	var run map[string]json.RawMessage
	decoder := json.NewDecoder(bytes.NewReader(document))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&run) != nil {
		return nil, false, ErrMaintenanceUnsafe
	}
	_, currentCandidatePresent := run["currentCandidateId"]
	if currentCandidatePresent {
		delete(run, "currentCandidateId")
	}
	changed := currentCandidatePresent
	if raw, present := run["execution"]; present && len(raw) > 2 {
		var executionState map[string]json.RawMessage
		if json.Unmarshal(raw, &executionState) != nil {
			return nil, false, ErrMaintenanceUnsafe
		}
		_, observationPresent := executionState["candidateObservation"]
		if observationPresent {
			delete(executionState, "candidateObservation")
			changed = true
		}
		if (currentCandidatePresent || observationPresent) && executionState["needsYou"] == nil {
			attention, _ := json.Marshal(execution.NeedsYou{Code: execution.NeedCode("schema_migration_candidate_revalidation_required"),
				WakeCondition: "fresh_exact_candidate_observation_and_admission", CleanupAuthorized: false})
			executionState["needsYou"] = attention
		}
		if changed {
			run["execution"], _ = json.Marshal(executionState)
		}
	}
	if !changed {
		return document, false, nil
	}
	encoded, err := json.Marshal(run)
	if err != nil {
		return nil, false, ErrMaintenanceUnsafe
	}
	return encoded, true, nil
}

func (maintenance *Maintenance) ApplyMigration(ctx context.Context, migration domainmaintenance.Migration) (maintenanceport.MigrationResult, error) {
	maintenance.store.maintenance.Lock()
	defer maintenance.store.maintenance.Unlock()
	if migration.FromVersion != 1 || migration.ToVersion != 2 || migration.Phase != domainmaintenance.MigrationDispatching ||
		len(migration.SourceFingerprint) != 64 {
		return maintenanceport.MigrationResult{}, ErrMaintenanceUnsafe
	}
	state, err := maintenance.Load(ctx)
	if err != nil || state.Migration == nil || !reflect.DeepEqual(*state.Migration, migration) {
		return maintenanceport.MigrationResult{}, ErrMaintenanceUnsafe
	}
	connection, err := maintenance.controlConnection(ctx)
	if err != nil {
		return maintenanceport.MigrationResult{}, err
	}
	defer connection.Close()
	source, err := maintenance.observeOn(ctx, connection, true)
	if err != nil || !source.Exact || source.SchemaVersion != 1 || source.Fingerprint != migration.SourceFingerprint {
		return maintenanceport.MigrationResult{}, ErrMaintenanceUnsafe
	}
	authorized := migration
	authorized.Phase = domainmaintenance.MigrationReady
	if domainmaintenance.GateMigration(state, authorized, source, maintenance.now().UnixMilli()) != domainmaintenance.MigrationGateReady {
		return maintenanceport.MigrationResult{}, ErrMaintenanceUnsafe
	}
	if _, paused, err := maintenance.observeProjectState(ctx, migration, false); err != nil || !paused {
		return maintenanceport.MigrationResult{}, ErrMaintenanceUnsafe
	}
	if _, err := connection.ExecContext(ctx, `CALL DOLT_VERIFY_CONSTRAINTS()`); err != nil {
		return maintenanceport.MigrationResult{}, ErrMaintenanceUnsafe
	}
	rows, err := connection.QueryContext(ctx, `SELECT id,run_id,sequence,commit_sha FROM candidates ORDER BY run_id,sequence`)
	if err != nil {
		return maintenanceport.MigrationResult{}, ErrMaintenanceUnsafe
	}
	var candidates []legacyCandidateRow
	for rows.Next() {
		var candidate legacyCandidateRow
		if rows.Scan(&candidate.id, &candidate.runID, &candidate.sequence, &candidate.commitSHA) != nil {
			_ = rows.Close()
			return maintenanceport.MigrationResult{}, ErrMaintenanceUnsafe
		}
		candidates = append(candidates, candidate)
	}
	if rows.Err() != nil || rows.Close() != nil {
		return maintenanceport.MigrationResult{}, ErrMaintenanceUnsafe
	}
	tx, err := connection.BeginTx(ctx, nil)
	if err != nil {
		return maintenanceport.MigrationResult{}, ErrMaintenanceUnsafe
	}
	fail := func() (maintenanceport.MigrationResult, error) {
		_ = tx.Rollback()
		return maintenanceport.MigrationResult{}, ErrMaintenanceUnsafe
	}
	trigger, ok := migrationTriggerStatement()
	if !ok {
		return fail()
	}
	for _, statement := range []string{
		`DROP TRIGGER candidates_reject_update`,
		`ALTER TABLE candidates MODIFY commit_sha VARCHAR(64) CHARACTER SET ascii COLLATE ascii_bin NOT NULL`,
		`ALTER TABLE candidates ADD COLUMN data JSON NULL`,
	} {
		if _, err := tx.ExecContext(ctx, statement); err != nil {
			return fail()
		}
	}
	for _, candidate := range candidates {
		document, err := legacyCandidateJSON(migration.ID, candidate.id, candidate.runID, candidate.sequence, candidate.commitSHA)
		if err != nil {
			return fail()
		}
		if result, err := tx.ExecContext(ctx, `UPDATE candidates SET data=? WHERE id=? AND data IS NULL`, document, candidate.id); err != nil {
			return fail()
		} else if count, err := result.RowsAffected(); err != nil || count != 1 {
			return fail()
		}
	}
	if _, err := tx.ExecContext(ctx, `ALTER TABLE candidates MODIFY data JSON NOT NULL`); err != nil {
		return fail()
	}
	if _, err := tx.ExecContext(ctx, trigger); err != nil {
		return fail()
	}
	runRows, err := tx.QueryContext(ctx, `SELECT id,version,data FROM aggregates WHERE kind=? ORDER BY id FOR UPDATE`, aggregateRun)
	if err != nil {
		return fail()
	}
	type changedRun struct {
		id       string
		version  uint64
		document []byte
	}
	var changed []changedRun
	for runRows.Next() {
		var value changedRun
		if runRows.Scan(&value.id, &value.version, &value.document) != nil {
			_ = runRows.Close()
			return fail()
		}
		migrated, didChange, err := migrateRunDocument(value.document)
		if err != nil {
			_ = runRows.Close()
			return fail()
		}
		if didChange {
			value.document = migrated
			changed = append(changed, value)
		}
	}
	if runRows.Err() != nil || runRows.Close() != nil {
		return fail()
	}
	for _, run := range changed {
		commandID := maintenanceCommandID(migration.ID, "candidate-revalidation", run.id)
		payload, _ := json.Marshal(struct {
			MigrationID string `json:"migrationId"`
		}{migration.ID})
		command, err := prepareCommand(domain.CommandRequest{IdempotencyKey: commandID, Type: "taskstore.migration.candidate_revalidation",
			AggregateID: run.id, ExpectedVersion: run.version, Payload: payload})
		if err != nil || insertCommand(ctx, tx, command) != nil {
			return fail()
		}
		newVersion := run.version + 1
		result, err := tx.ExecContext(ctx, `UPDATE aggregates SET version=?,data=? WHERE id=? AND kind=? AND version=?`, newVersion, run.document, run.id, aggregateRun, run.version)
		if err != nil {
			return fail()
		}
		count, err := result.RowsAffected()
		event, eventErr := prepareEvent(domain.Event{ID: "event-" + commandID, RunID: run.id, Sequence: newVersion + 1,
			AggregateID: run.id, AggregateVersion: newVersion, Type: "run.candidate_revalidation_required", Payload: payload}, command.request)
		if err != nil || count != 1 || eventErr != nil || appendEvent(ctx, tx, event, newVersion) != nil ||
			insertOutcome(ctx, tx, command, mutationResult{outcome: domain.CommandApplied, observedVersion: newVersion}, event.event.ID) != nil {
			return fail()
		}
	}
	if result, err := tx.ExecContext(ctx, `UPDATE schema_metadata SET schema_version=2 WHERE singleton=1 AND schema_version=1`); err != nil {
		return fail()
	} else if count, err := result.RowsAffected(); err != nil || count != 1 {
		return fail()
	}
	if err := tx.Commit(); err != nil {
		return maintenanceport.MigrationResult{}, ErrMaintenanceUnsafe
	}
	if _, err := connection.ExecContext(ctx, `CALL DOLT_VERIFY_CONSTRAINTS()`); err != nil {
		return maintenanceport.MigrationResult{}, ErrMaintenanceUnsafe
	}
	result, err := maintenance.observeOn(ctx, connection, true)
	if err != nil || !result.Exact || result.SchemaVersion != 2 {
		return maintenanceport.MigrationResult{Store: result}, ErrMaintenanceUnsafe
	}
	return maintenanceport.MigrationResult{Store: result}, nil
}

func (maintenance *Maintenance) RestoreBackup(ctx context.Context, migration domainmaintenance.Migration, backup domainmaintenance.Backup) (maintenanceport.RestoreResult, error) {
	database := generatedDatabase("director_recovery", migration.ID+"\x1f"+backup.ID)
	restored, err := maintenance.restoreAndObserve(ctx, backup, database, true)
	return maintenanceport.RestoreResult{Fingerprint: restored.Fingerprint}, err
}

func (maintenance *Maintenance) ResumeProjects(ctx context.Context, migration domainmaintenance.Migration) error {
	maintenance.store.maintenance.Lock()
	defer maintenance.store.maintenance.Unlock()
	return maintenance.mutateProjects(ctx, migration, true)
}

func (maintenance *Maintenance) ObserveProjectsResumed(ctx context.Context, migration domainmaintenance.Migration) (bool, error) {
	maintenance.store.maintenance.RLock()
	defer maintenance.store.maintenance.RUnlock()
	_, resumed, err := maintenance.observeProjectState(ctx, migration, true)
	return resumed, err
}

func (maintenance *Maintenance) ExpireBackup(ctx context.Context, backup domainmaintenance.Backup) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	directory, manifestPath, ok := backupPaths(maintenance, backup.ID)
	if !ok || maintenance.verifyRoot() != nil {
		return ErrMaintenanceUnsafe
	}
	manifest, err := readManifest(manifestPath)
	if err != nil {
		return ErrMaintenanceUnsafe
	}
	identity, err := exactOwnedDirectory(directory)
	if errors.Is(err, os.ErrNotExist) {
		quarantine := directory + ".expired"
		quarantined, quarantineErr := exactOwnedDirectory(quarantine)
		if quarantineErr == nil {
			if quarantined.device != manifest.Device || quarantined.inode != manifest.Inode || os.RemoveAll(quarantine) != nil ||
				syncDirectory(filepath.Dir(quarantine)) != nil {
				return ErrMaintenanceUnsafe
			}
		} else if !errors.Is(quarantineErr, os.ErrNotExist) {
			return ErrMaintenanceUnsafe
		}
		if os.Remove(manifestPath) != nil {
			return ErrMaintenanceUnsafe
		}
		return syncDirectory(filepath.Dir(manifestPath))
	}
	if err != nil || manifest.ID != backup.ID || manifest.SourceVersion != backup.SourceSchemaVersion ||
		manifest.SourceFingerprint != backup.SourceFingerprint || manifest.Device != identity.device || manifest.Inode != identity.inode {
		return ErrMaintenanceUnsafe
	}
	quarantine := directory + ".expired"
	if _, err := os.Lstat(quarantine); !errors.Is(err, os.ErrNotExist) {
		return ErrMaintenanceUnsafe
	}
	if os.Rename(directory, quarantine) != nil || syncDirectory(filepath.Dir(directory)) != nil {
		return ErrMaintenanceUnsafe
	}
	quarantined, err := exactOwnedDirectory(quarantine)
	if err != nil || quarantined.device != manifest.Device || quarantined.inode != manifest.Inode {
		return ErrMaintenanceUnsafe
	}
	if os.RemoveAll(quarantine) != nil || os.Remove(manifestPath) != nil || syncDirectory(filepath.Dir(manifestPath)) != nil {
		return ErrMaintenanceUnsafe
	}
	return nil
}

func (maintenance *Maintenance) CompactLogs(ctx context.Context, nowMillis int64) error {
	return maintenance.logs.CompactExpired(ctx, nowMillis)
}

func (maintenance *Maintenance) ObserveDisk(ctx context.Context) (domainmaintenance.DiskObservation, error) {
	if err := ctx.Err(); err != nil {
		return domainmaintenance.DiskObservation{}, err
	}
	if maintenance.verifyRoot() != nil {
		return domainmaintenance.DiskObservation{}, ErrMaintenanceUnsafe
	}
	var facts syscall.Statfs_t
	if maintenance.statfs(maintenance.root, &facts) != nil || facts.Blocks == 0 || facts.Bsize <= 0 {
		return domainmaintenance.DiskObservation{}, ErrMaintenanceUnsafe
	}
	blockSize := uint64(facts.Bsize)
	maximum := ^uint64(0)
	if facts.Bavail > facts.Blocks || facts.Blocks > maximum/blockSize || facts.Bavail > maximum/blockSize {
		return domainmaintenance.DiskObservation{}, ErrMaintenanceUnsafe
	}
	total := facts.Blocks * blockSize
	free := facts.Bavail * blockSize
	return domainmaintenance.DiskObservation{ObservedAtMillis: maintenance.now().UnixMilli(), MaximumAgeMillis: 5_000,
		TotalBytes: total, FreeBytes: free}, nil
}
