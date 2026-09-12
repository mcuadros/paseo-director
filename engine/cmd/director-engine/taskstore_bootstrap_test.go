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
	var stdout, stderr bytes.Buffer
	status := runTaskStoreBootstrap([]string{"--config-output", configPath, "--address", fixture.address,
		"--database", fixture.database, "--store-id", "bootstrap-cli-store", "--control-user", "root",
		"--maintenance-root", filepath.Join(root, "maintenance")}, &stdout, &stderr)
	if status != 0 || stderr.Len() != 0 || strings.Contains(stdout.String(), configPath) || strings.Contains(stdout.String(), fixture.address) {
		t.Fatalf("bootstrap CLI status=%d stdout=%s stderr=%s", status, stdout.String(), stderr.String())
	}
	var result taskStoreBootstrapResult
	if json.Unmarshal(stdout.Bytes(), &result) != nil || result.Status != "ready" || result.StoreID != "bootstrap-cli-store" {
		t.Fatalf("bootstrap readback = %s", stdout.String())
	}
	info, err := os.Lstat(configPath)
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm() != 0o600 {
		t.Fatalf("seeded TaskStore config mode = %v, %v", info, err)
	}
}

func TestTaskStoreConfigSeedRecoversExactDurableStagingAndRefusesChangedState(t *testing.T) {
	root := t.TempDir()
	if err := os.Chmod(root, 0o700); err != nil {
		t.Fatal(err)
	}
	reference := filepath.Join(root, "reference.json")
	arguments := []string{"127.0.0.1:3307", "director", "bootstrap-recovery", "root", "root", "", ""}
	if err := seedTaskStoreConfig(reference, arguments[0], arguments[1], arguments[2], arguments[3], arguments[4], arguments[5], arguments[6]); err != nil {
		t.Fatal(err)
	}
	content, err := os.ReadFile(reference)
	if err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(root, "recovered.json")
	if err := os.WriteFile(target+".next", content, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := seedTaskStoreConfig(target, arguments[0], arguments[1], arguments[2], arguments[3], arguments[4], arguments[5], arguments[6]); err != nil {
		t.Fatalf("recover exact staging: %v", err)
	}
	if _, err := os.Lstat(target + ".next"); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("staging path remains after recovery: %v", err)
	}
	changed := filepath.Join(root, "changed.json")
	if err := os.WriteFile(changed+".next", []byte("{}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := seedTaskStoreConfig(changed, arguments[0], arguments[1], arguments[2], arguments[3], arguments[4], arguments[5], arguments[6]); err == nil {
		t.Fatal("changed staging state was adopted")
	}
}
