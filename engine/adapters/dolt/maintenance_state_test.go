// SPDX-License-Identifier: Apache-2.0

package dolt

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	domainmaintenance "github.com/mcuadros/director-engine/domain/taskstoremaintenance"
)

type noOpLogCompactor struct{}

func (noOpLogCompactor) CompactExpired(context.Context, int64) error { return nil }

func maintenanceFixture(t *testing.T) (*Maintenance, string) {
	t.Helper()
	root := filepath.Join(t.TempDir(), "maintenance")
	maintenance, err := NewMaintenance(&DoltTaskStore{storeID: "store-1"}, root, noOpLogCompactor{})
	if err != nil {
		t.Fatal(err)
	}
	return maintenance, root
}

func TestMaintenanceStateCASPersistsAcrossReopen(t *testing.T) {
	maintenance, root := maintenanceFixture(t)
	state, err := maintenance.Load(context.Background())
	if err != nil || state.Revision != 1 || !domainmaintenance.ValidState(state) {
		t.Fatalf("initial state = %#v, %v", state, err)
	}
	next := state
	next.Revision++
	next = domainmaintenance.SealState(next)
	if err := maintenance.CompareAndSwap(context.Background(), state.Revision, next); err != nil {
		t.Fatal(err)
	}
	if err := maintenance.CompareAndSwap(context.Background(), state.Revision, next); !errors.Is(err, ErrMaintenanceUnsafe) {
		t.Fatalf("stale CAS error = %v", err)
	}
	reopened, err := NewMaintenance(&DoltTaskStore{storeID: "store-1"}, root, noOpLogCompactor{})
	if err != nil {
		t.Fatal(err)
	}
	loaded, err := reopened.Load(context.Background())
	if err != nil || loaded.Revision != next.Revision || loaded.SHA256 != next.SHA256 {
		t.Fatalf("reopened state = %#v, %v", loaded, err)
	}
	for _, name := range []string{"maintenance.lock", "maintenance-state.json"} {
		info, err := os.Lstat(filepath.Join(root, name))
		if err != nil || !info.Mode().IsRegular() || info.Mode().Perm() != 0o600 {
			t.Fatalf("%s mode = %v, %v", name, info, err)
		}
	}
}

func TestMaintenanceStateRefusesTamperStoreMismatchAndAliasedRoot(t *testing.T) {
	maintenance, root := maintenanceFixture(t)
	statePath := filepath.Join(root, "maintenance-state.json")
	document, err := os.ReadFile(statePath)
	if err != nil {
		t.Fatal(err)
	}
	document = []byte(strings.Replace(string(document), `"revision":1`, `"revision":9`, 1))
	if err := os.WriteFile(statePath, document, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := maintenance.Load(context.Background()); !errors.Is(err, ErrMaintenanceUnsafe) {
		t.Fatalf("tampered state error = %v", err)
	}
	cleanRoot := filepath.Join(t.TempDir(), "clean")
	clean, err := NewMaintenance(&DoltTaskStore{storeID: "store-a"}, cleanRoot, noOpLogCompactor{})
	if err != nil {
		t.Fatal(err)
	}
	_ = clean
	if _, err := NewMaintenance(&DoltTaskStore{storeID: "store-b"}, cleanRoot, noOpLogCompactor{}); !errors.Is(err, ErrMaintenanceUnsafe) {
		t.Fatalf("store mismatch error = %v", err)
	}
	bindingRoot := filepath.Join(t.TempDir(), "binding")
	if _, err := NewMaintenance(&DoltTaskStore{storeID: "store-same", address: "127.0.0.1:3306", database: "director"}, bindingRoot, noOpLogCompactor{}); err != nil {
		t.Fatal(err)
	}
	if _, err := NewMaintenance(&DoltTaskStore{storeID: "store-same", address: "127.0.0.1:3307", database: "director"}, bindingRoot, noOpLogCompactor{}); !errors.Is(err, ErrMaintenanceUnsafe) {
		t.Fatalf("listener binding mismatch error = %v", err)
	}
	alias := filepath.Join(t.TempDir(), "alias")
	if err := os.Symlink(cleanRoot, alias); err != nil {
		t.Fatal(err)
	}
	if _, err := NewMaintenance(&DoltTaskStore{storeID: "store-a"}, alias, noOpLogCompactor{}); !errors.Is(err, ErrMaintenanceUnsafe) {
		t.Fatalf("aliased root error = %v", err)
	}
}

func TestBackupExpiryUsesRecordedIdentityAndRecoversQuarantine(t *testing.T) {
	maintenance, root := maintenanceFixture(t)
	backup := domainmaintenance.Backup{ID: "backup-1", SourceSchemaVersion: 2, SourceFingerprint: strings.Repeat("a", 64)}
	directory, manifestPath, _ := backupPaths(maintenance, backup.ID)
	if err := os.Mkdir(directory, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(directory, "chunk"), []byte("recoverable"), 0o600); err != nil {
		t.Fatal(err)
	}
	device, inode, err := directoryDeviceInode(directory)
	if err != nil {
		t.Fatal(err)
	}
	manifest := backupManifest{SchemaVersion: "director.taskstore-backup-manifest/v1", ID: backup.ID,
		SourceVersion: 2, SourceFingerprint: backup.SourceFingerprint, Device: device, Inode: inode}
	manifest.SHA256 = manifestSHA(manifest)
	if err := writeExclusivePrivate(manifestPath, manifest); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(directory, directory+".expired"); err != nil {
		t.Fatal(err)
	}
	if err := maintenance.ExpireBackup(context.Background(), backup); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{directory, directory + ".expired", manifestPath} {
		if _, err := os.Lstat(path); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("expired artifact remains at %s: %v", filepath.Base(path), err)
		}
	}
	other := filepath.Join(root, "backups", "backup-other")
	if err := os.Mkdir(other, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(other, directory); err != nil {
		t.Fatal(err)
	}
	replacement, err := exactOwnedDirectory(directory)
	if err != nil {
		t.Fatal(err)
	}
	// Derive the mismatch from the replacement itself. Filesystems may assign
	// a newly created directory the prior directory's inode plus one.
	manifest.Device = replacement.device
	manifest.Inode = replacement.inode + 1
	if manifest.Inode == 0 {
		manifest.Inode = replacement.inode - 1
	}
	if manifest.Inode == replacement.inode {
		t.Fatal("fixture failed to construct a mismatched backup identity")
	}
	manifest.SHA256 = manifestSHA(manifest)
	if err := writeExclusivePrivate(manifestPath, manifest); err != nil {
		t.Fatal(err)
	}
	if err := maintenance.ExpireBackup(context.Background(), backup); !errors.Is(err, ErrMaintenanceUnsafe) {
		t.Fatalf("stale identity expiry error = %v", err)
	}
	if _, err := os.Stat(directory); err != nil {
		t.Fatalf("ambiguous replacement was removed: %v", err)
	}
}

func TestDiskObservationIsBoundedAndUsesAvailableBlocks(t *testing.T) {
	maintenance, _ := maintenanceFixture(t)
	maintenance.now = func() time.Time { return time.UnixMilli(10_000) }
	maintenance.statfs = func(_ string, facts *syscall.Statfs_t) error {
		facts.Blocks, facts.Bavail, facts.Bsize = 100, 9, 4096
		return nil
	}
	observation, err := maintenance.ObserveDisk(context.Background())
	if err != nil || observation.TotalBytes != 409_600 || observation.FreeBytes != 36_864 || observation.ObservedAtMillis != 10_000 {
		t.Fatalf("disk observation = %#v, %v", observation, err)
	}
	maintenance.statfs = func(_ string, facts *syscall.Statfs_t) error {
		facts.Blocks, facts.Bavail, facts.Bsize = ^uint64(0), 1, 2
		return nil
	}
	if _, err := maintenance.ObserveDisk(context.Background()); !errors.Is(err, ErrMaintenanceUnsafe) {
		t.Fatalf("overflowing disk observation error = %v", err)
	}
}

func TestSchemaOneRunTransformationInvalidatesEveryLegacyObservation(t *testing.T) {
	document := []byte(`{"number":1,"baseSha":"0123456789abcdef0123456789abcdef01234567","execution":{"schemaVersion":"director.execution/v1","candidateObservation":{"id":"old"},"needsYou":{"code":"existing_attention","wakeCondition":"human_action","cleanupAuthorized":false}}}`)
	migrated, changed, err := migrateRunDocument(document)
	if err != nil || !changed || bytes.Contains(migrated, []byte("candidateObservation")) || !bytes.Contains(migrated, []byte("existing_attention")) {
		t.Fatalf("transformed Run=%s changed=%t err=%v", migrated, changed, err)
	}
	withoutCurrent := []byte(`{"number":1,"baseSha":"0123456789abcdef0123456789abcdef01234567","execution":{"schemaVersion":"director.execution/v1","candidateObservation":{"id":"old"}}}`)
	migrated, changed, err = migrateRunDocument(withoutCurrent)
	if err != nil || !changed || bytes.Contains(migrated, []byte("candidateObservation")) ||
		!bytes.Contains(migrated, []byte("schema_migration_candidate_revalidation_required")) {
		t.Fatalf("observation-only Run=%s changed=%t err=%v", migrated, changed, err)
	}
}
