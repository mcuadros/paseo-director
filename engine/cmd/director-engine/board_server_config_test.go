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
	writePrivateFile(t, controlPassword, "control-secret\n")
	writePrivateFile(t, writerPassword, "writer-secret\n")
	configPath := filepath.Join(root, "engine.json")
	writePrivateFile(t, configPath, `{
		"schemaVersion": 1,
		"storeId": "director-store",
		"control": {"address":"127.0.0.1:3307","database":"director","user":"control","passwordFile":`+strconv.Quote(controlPassword)+`},
		"writer": {"address":"127.0.0.1:3307","database":"director","user":"writer","passwordFile":`+strconv.Quote(writerPassword)+`}
	}`)

	config, err := readBoardServerConfig(configPath)
	if err != nil {
		t.Fatalf("readBoardServerConfig() error = %v", err)
	}
	if config.StoreID != "director-store" || config.Control.Password != "control-secret" || config.Writer.Password != "writer-secret" {
		t.Fatal("loaded TaskStore configuration fields do not match")
	}
}

func TestReadBoardServerConfigRejectsUnsafeOrDriftingInputsWithoutSecretLeak(t *testing.T) {
	root := t.TempDir()
	secret := "do-not-leak"
	passwordPath := filepath.Join(root, "password")
	writePrivateFile(t, passwordPath, secret)
	valid := `{"schemaVersion":1,"storeId":"director-store","control":{"address":"127.0.0.1:3307","database":"director","user":"root","passwordFile":null},"writer":{"address":"127.0.0.1:3307","database":"director","user":"root","passwordFile":null}}`
	passwordConfigPath := filepath.Join(root, "password-config.json")
	passwordConfig := strings.ReplaceAll(valid, "null", strconv.Quote(passwordPath))
	writePrivateFile(t, passwordConfigPath, passwordConfig)
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
		"unknown field":          {path: filepath.Join(root, "unknown.json"), content: strings.Replace(valid, `"schemaVersion":1`, `"schemaVersion":1,"policy":true`, 1)},
		"duplicate field":        {path: filepath.Join(root, "duplicate.json"), content: strings.Replace(valid, `"schemaVersion":1`, `"schemaVersion":1,"schemaVersion":1`, 1)},
		"missing password field": {path: filepath.Join(root, "missing-password.json"), content: strings.Replace(valid, `,"passwordFile":null`, "", 1)},
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
	status := runBoardServer([]string{
		"--listen", "127.0.0.1:7041",
		"--taskstore-config", "/missing/private/director-engine.json",
		"--host-id", "host-test",
		"--host-label", "Test host",
	}, &stdout, &stderr)
	if status != 1 || stdout.Len() != 0 || stderr.String() != "director-engine: Board server configuration is invalid\n" {
		t.Fatalf("status = %d, stdout bytes = %d, stderr = %q", status, stdout.Len(), stderr.String())
	}
}
