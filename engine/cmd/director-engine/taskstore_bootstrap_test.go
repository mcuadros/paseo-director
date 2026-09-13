// SPDX-License-Identifier: Apache-2.0

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/mcuadros/director-engine/adapters/dolt"
)

func TestTaskStoreBootstrapIsSupportedIdempotentAndIdentityBound(t *testing.T) {
	fixture := startVerticalDolt(t)
	endpoint := dolt.Endpoint{Address: fixture.address, Database: fixture.database, User: "root"}
	config := dolt.Config{Control: endpoint, Writer: endpoint, StoreID: "production-bootstrap-store"}
	root := filepath.Join(t.TempDir(), "maintenance")
	first, err := bootstrapTaskStore(context.Background(), config, root, 1_000)
	if err != nil || first.Status != "ready" || first.SchemaVersion != 2 || !first.Idempotent || first.ProjectCount != 0 {
		t.Fatalf("first bootstrap = %#v, %v", first, err)
	}
	second, err := bootstrapTaskStore(context.Background(), config, root, 2_000)
	if err != nil || second.StoreID != first.StoreID || second.Database != first.Database || second.EventCursor != first.EventCursor {
		t.Fatalf("idempotent bootstrap = %#v, %v", second, err)
	}
	wrong := config
	wrong.StoreID = "different-production-store"
	if _, err := bootstrapTaskStore(context.Background(), wrong, filepath.Join(t.TempDir(), "wrong"), 3_000); err == nil {
		t.Fatal("bootstrap adopted a database with a different exact store identity")
	}
}

func TestTaskStoreBootstrapCLIUsesOnlyPrivateConfigAndBoundedReadback(t *testing.T) {
	fixture := startVerticalDolt(t)
	root := t.TempDir()
	if err := os.Chmod(root, 0o700); err != nil {
		t.Fatal(err)
	}
	configPath := filepath.Join(root, "taskstore.json")
	controlPassword := filepath.Join(root, "control.password")
	writerPassword := filepath.Join(root, "writer.password")
	maintenancePassword := filepath.Join(root, "maintenance.password")
	for path, value := range map[string]string{controlPassword: "control-secret", writerPassword: "writer-secret", maintenancePassword: "maintenance-secret"} {
		if err := os.WriteFile(path, []byte(value), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	var stdout, stderr bytes.Buffer
	arguments := []string{"--config-output", configPath, "--address", fixture.address,
		"--database", fixture.database, "--store-id", "bootstrap-cli-store", "--owner-user", "root",
		"--control-user", "director_control", "--writer-user", "director_writer", "--maintenance-user", "director_maintenance",
		"--control-password-file", controlPassword, "--writer-password-file", writerPassword,
		"--maintenance-password-file", maintenancePassword, "--privilege-file", filepath.Join(fixture.root, ".doltcfg", "privileges.db"),
		"--maintenance-root", filepath.Join(root, "maintenance")}
	status := runTaskStoreBootstrap(arguments, &stdout, &stderr)
	if status != 0 || stderr.Len() != 0 || strings.Contains(stdout.String(), configPath) || strings.Contains(stdout.String(), fixture.address) {
		t.Fatalf("bootstrap CLI status=%d stdout=%s stderr=%s", status, stdout.String(), stderr.String())
	}
	var result taskStoreBootstrapResult
	if json.Unmarshal(stdout.Bytes(), &result) != nil || result.Status != "ready" || result.StoreID != "bootstrap-cli-store" ||
		result.Authority != string(dolt.AuthorityCurrent) {
		t.Fatalf("bootstrap readback = %s", stdout.String())
	}
	info, err := os.Lstat(configPath)
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm() != 0o600 {
		t.Fatalf("seeded TaskStore config mode = %v, %v", info, err)
	}
	config, err := readBoardServerConfig(configPath)
	if err != nil || !config.RequireLeastPrivilege || config.Control.Principal != "director_control@%" ||
		config.Writer.Principal != "director_writer@%" || config.Maintenance.Principal != "director_maintenance@%" {
		t.Fatalf("seeded least-privilege config = %#v, %v", config, err)
	}
	stdout.Reset()
	if status := runTaskStoreBootstrap(arguments, &stdout, &stderr); status != 0 || stderr.Len() != 0 {
		t.Fatalf("idempotent bootstrap CLI status=%d stdout=%s stderr=%s", status, stdout.String(), stderr.String())
	}
}

func TestTaskStoreConfigSeedRecoversExactDurableStagingAndRefusesChangedState(t *testing.T) {
	root := t.TempDir()
	if err := os.Chmod(root, 0o700); err != nil {
		t.Fatal(err)
	}
	reference := filepath.Join(root, "reference.json")
	passwords := []string{filepath.Join(root, "control.password"), filepath.Join(root, "writer.password"), filepath.Join(root, "maintenance.password")}
	config := dolt.Config{
		Control:     dolt.Endpoint{Address: "127.0.0.1:3307", Database: "director", User: "control", Principal: "control@%"},
		Writer:      dolt.Endpoint{Address: "127.0.0.1:3307", Database: "director", User: "writer", Principal: "writer@%"},
		Maintenance: dolt.Endpoint{Address: "127.0.0.1:3307", Database: "director", User: "maintenance", Principal: "maintenance@%"},
		StoreID:     "bootstrap-recovery", AuthoritySHA256: strings.Repeat("a", 64),
		PrivilegeFile: filepath.Join(root, "privileges.db"), PrivilegeFileSHA256: strings.Repeat("b", 64), RequireLeastPrivilege: true,
	}
	if err := installTaskStoreConfig(reference, config, passwords[0], passwords[1], passwords[2]); err != nil {
		t.Fatal(err)
	}
	updated := config
	updated.PrivilegeFileSHA256 = strings.Repeat("c", 64)
	if err := installTaskStoreConfig(reference, updated, passwords[0], passwords[1], passwords[2]); err != nil {
		t.Fatalf("update only current authority attestation: %v", err)
	}
	readback, err := os.ReadFile(reference)
	if err != nil || !strings.Contains(string(readback), strings.Repeat("c", 64)) {
		t.Fatalf("updated authority attestation readback=%s err=%v", readback, err)
	}
	config = updated
	content, err := os.ReadFile(reference)
	if err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(root, "recovered.json")
	if err := os.WriteFile(target+".next", content, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := installTaskStoreConfig(target, config, passwords[0], passwords[1], passwords[2]); err != nil {
		t.Fatalf("recover exact staging: %v", err)
	}
	if _, err := os.Lstat(target + ".next"); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("staging path remains after recovery: %v", err)
	}
	changed := filepath.Join(root, "changed.json")
	if err := os.WriteFile(changed+".next", []byte("{}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := installTaskStoreConfig(changed, config, passwords[0], passwords[1], passwords[2]); err == nil {
		t.Fatal("changed staging state was adopted")
	}
	different := config
	different.StoreID = "another-store"
	if err := installTaskStoreConfig(reference, different, passwords[0], passwords[1], passwords[2]); !errors.Is(err, errTaskStoreConfigOutputMismatch) {
		t.Fatalf("different existing config error = %v", err)
	}
	if contentAfter, readErr := os.ReadFile(reference); readErr != nil || !bytes.Equal(contentAfter, content) {
		t.Fatalf("different existing config was overwritten: %v", readErr)
	}
}

func TestTaskStoreConfigOutputMismatchMessageIsBoundedAndNonDestructive(t *testing.T) {
	message := taskStoreConfigOutputMismatchMessage
	for _, required := range []string{
		"DIRECTOR_TASKSTORE_CONFIG_OUTPUT_MISMATCH",
		"TaskStore/database is not the cause and must not be deleted",
		"move the existing config file to an owner-only backup",
		"rerun the identical bootstrap command",
		"compare both files without exposing credentials",
		"restore the backup or adopt the replacement",
	} {
		if !strings.Contains(message, required) {
			t.Fatalf("config mismatch diagnostic missing %q: %s", required, message)
		}
	}
	if strings.Contains(message, "/home/") || strings.Contains(message, "/tmp/") || len(message) > 1_024 {
		t.Fatalf("config mismatch diagnostic is unsafe: %s", message)
	}
}
