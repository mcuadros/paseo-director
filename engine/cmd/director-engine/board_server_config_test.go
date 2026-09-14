// SPDX-License-Identifier: Apache-2.0

package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

func writePrivateFile(t *testing.T, path, contents string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestReadBoardServerConfigLoadsPrivatePasswordFiles(t *testing.T) {
	root := t.TempDir()
	controlPassword := filepath.Join(root, "control.password")
	writerPassword := filepath.Join(root, "writer.password")
	maintenancePassword := filepath.Join(root, "maintenance.password")
	privilegeFile := filepath.Join(root, "privileges.db")
	writePrivateFile(t, controlPassword, "control-secret\n")
	writePrivateFile(t, writerPassword, "writer-secret\n")
	writePrivateFile(t, maintenancePassword, "maintenance-secret\n")
	writePrivateFile(t, privilegeFile, "bounded-attestation-fixture")
	configPath := filepath.Join(root, "engine.json")
	writePrivateFile(t, configPath, `{
		"schemaVersion": 2,
		"storeId": "director-store",
		"authoritySha256": "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		"privilegeFile": `+strconv.Quote(privilegeFile)+`,
		"privilegeFileSha256": "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
		"control": {"address":"127.0.0.1:3307","database":"director","user":"control","principal":"control@%","passwordFile":`+strconv.Quote(controlPassword)+`},
		"writer": {"address":"127.0.0.1:3307","database":"director","user":"writer","principal":"writer@%","passwordFile":`+strconv.Quote(writerPassword)+`},
		"maintenance": {"address":"127.0.0.1:3307","database":"director","user":"maintenance","principal":"maintenance@%","passwordFile":`+strconv.Quote(maintenancePassword)+`}
	}`)

	config, err := readBoardServerConfig(configPath)
	if err != nil {
		t.Fatalf("readBoardServerConfig() error = %v", err)
	}
	if config.StoreID != "director-store" || config.Control.Password != "control-secret" || config.Writer.Password != "writer-secret" ||
		config.Maintenance.Password != "maintenance-secret" || !config.RequireLeastPrivilege {
		t.Fatal("loaded TaskStore configuration fields do not match")
	}
}

func TestReadBoardServerConfigRejectsUnsafeOrDriftingInputsWithoutSecretLeak(t *testing.T) {
	root := t.TempDir()
	secret := "do-not-leak"
	passwordPath := filepath.Join(root, "password")
	writePrivateFile(t, passwordPath, secret)
	quotedPassword := strconv.Quote(passwordPath)
	privilegePath := filepath.Join(root, "privileges.db")
	valid := `{"schemaVersion":2,"storeId":"director-store","authoritySha256":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","privilegeFile":` + strconv.Quote(privilegePath) + `,"privilegeFileSha256":"bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb","control":{"address":"127.0.0.1:3307","database":"director","user":"control","principal":"control@%","passwordFile":` + quotedPassword + `},"writer":{"address":"127.0.0.1:3307","database":"director","user":"writer","principal":"writer@%","passwordFile":` + quotedPassword + `},"maintenance":{"address":"127.0.0.1:3307","database":"director","user":"maintenance","principal":"maintenance@%","passwordFile":` + quotedPassword + `}}`
	passwordConfigPath := filepath.Join(root, "password-config.json")
	writePrivateFile(t, passwordConfigPath, valid)
	if err := os.Chmod(passwordPath, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := readBoardServerConfig(passwordConfigPath); err == nil || strings.Contains(err.Error(), secret) {
		t.Fatalf("unsafe password file error is missing or leaked content: %v", err)
	}
	if err := os.Chmod(passwordPath, 0o600); err != nil {
		t.Fatal(err)
	}

	for name, fixture := range map[string]struct {
		path    string
		content string
		prepare func(string)
	}{
		"relative path":          {path: "relative.json", content: valid},
		"unknown field":          {path: filepath.Join(root, "unknown.json"), content: strings.Replace(valid, `"schemaVersion":2`, `"schemaVersion":2,"policy":true`, 1)},
		"duplicate field":        {path: filepath.Join(root, "duplicate.json"), content: strings.Replace(valid, `"schemaVersion":2`, `"schemaVersion":2,"schemaVersion":2`, 1)},
		"missing password field": {path: filepath.Join(root, "missing-password.json"), content: strings.Replace(valid, `,"passwordFile":`+quotedPassword, "", 1)},
		"broad permissions":      {path: filepath.Join(root, "broad.json"), content: valid, prepare: func(path string) { _ = os.Chmod(path, 0o644) }},
		"symlink": {path: filepath.Join(root, "link.json"), content: valid, prepare: func(path string) {
			target := filepath.Join(root, "target.json")
			writePrivateFile(t, target, valid)
			_ = os.Remove(path)
			if err := os.Symlink(target, path); err != nil {
				t.Fatal(err)
			}
		}},
	} {
		t.Run(name, func(t *testing.T) {
			if filepath.IsAbs(fixture.path) {
				writePrivateFile(t, fixture.path, fixture.content)
			}
			if fixture.prepare != nil {
				fixture.prepare(fixture.path)
			}
			_, err := readBoardServerConfig(fixture.path)
			if err == nil {
				t.Fatal("readBoardServerConfig() unexpectedly succeeded")
			}
			if strings.Contains(err.Error(), secret) {
				t.Fatalf("error leaked secret: %v", err)
			}
		})
	}
}

func TestBoardServerFailsBeforeListeningWithInvalidConfiguration(t *testing.T) {
	var stdout, stderr bytes.Buffer
	adminToken := filepath.Join(t.TempDir(), "project-admin.token")
	writePrivateFile(t, adminToken, strings.Repeat("a", 64))
	status := runBoardServer([]string{
		"--listen", "127.0.0.1:7041",
		"--taskstore-config", "/missing/private/director-engine.json",
		"--host-id", "host-test",
		"--host-label", "Test host",
		"--host-socket", "/tmp/director-test-host.sock",
		"--runtime-root", "/tmp/director-test-runtime",
		"--project-admin-token-file", adminToken,
	}, &stdout, &stderr)
	if status != 1 || stdout.Len() != 0 || stderr.String() != "director-engine: Board server configuration is invalid\n" {
		t.Fatalf("status = %d, stdout bytes = %d, stderr = %q", status, stdout.Len(), stderr.String())
	}
}

func TestBoardServerDerivesTheSameOwnerRuntimePathsAsInstalledConnector(t *testing.T) {
	base := filepath.Join(t.TempDir(), "runtime-base")
	t.Setenv("XDG_RUNTIME_DIR", base)
	t.Setenv("XDG_CACHE_HOME", filepath.Join(t.TempDir(), "cache-base"))
	runtimeRoot, socket, err := defaultProductionRuntimePaths()
	if err != nil || runtimeRoot != filepath.Join(base, "director", "runtime", "work") ||
		socket != filepath.Join(base, "director", "runtime", "host.sock") {
		t.Fatalf("derived production runtime paths = %q / %q, %v", runtimeRoot, socket, err)
	}
}
