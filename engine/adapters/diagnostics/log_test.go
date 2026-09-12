// SPDX-License-Identifier: Apache-2.0

package diagnostics

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	homeport "github.com/mcuadros/director-engine/ports/home"
)

func TestStructuredLogsPersistAcrossRestartAndReturnOneBoundedTail(t *testing.T) {
	root := filepath.Join(t.TempDir(), "logs")
	store, err := NewLogStore(root)
	if err != nil {
		t.Fatal(err)
	}
	now := int64(1_757_640_000_000)
	if err := store.Append(context.Background(), "project/private/path", now-logRetentionMillis-1, homeport.TechnicalLogInfo, homeport.TechnicalLogEngine, homeport.TechnicalLogEngineStarted, 1); err != nil {
		t.Fatal(err)
	}
	for index := 0; index < 140; index++ {
		if err := store.Append(context.Background(), "project/private/path", now-int64(140-index), homeport.TechnicalLogInfo, homeport.TechnicalLogReconciliation, homeport.TechnicalLogReconciliationCompleted, 1); err != nil {
			t.Fatal(err)
		}
	}
	restarted, err := NewLogStore(root)
	if err != nil {
		t.Fatal(err)
	}
	logs, err := restarted.ReadTechnicalLogs(context.Background(), "project/private/path", now)
	if err != nil || len(logs) != maximumReadLogEntries || logs[0].Sequence != 13 || logs[len(logs)-1].Sequence != 141 {
		t.Fatalf("restarted bounded logs len=%d first=%#v last=%#v err=%v", len(logs), logs[0], logs[len(logs)-1], err)
	}
	entries, err := os.ReadDir(root)
	if err != nil || len(entries) != 1 || strings.Contains(entries[0].Name(), "private") || !strings.HasPrefix(entries[0].Name(), "project-") {
		t.Fatalf("pseudonymous log file = %#v, %v", entries, err)
	}
	info, err := entries[0].Info()
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("log mode = %v, %v", info, err)
	}
}

func TestStructuredLogsRefuseUnknownCodesAndReplacedRoots(t *testing.T) {
	root := filepath.Join(t.TempDir(), "logs")
	store, err := NewLogStore(root)
	if err != nil {
		t.Fatal(err)
	}
	now := int64(1_757_640_000_000)
	if err := store.Append(context.Background(), "project-a", now, homeport.TechnicalLogInfo, homeport.TechnicalLogEngine, homeport.TechnicalLogCode("secret=/home/private"), 1); !errors.Is(err, ErrUnsafeBundle) {
		t.Fatalf("unknown-code append error = %v", err)
	}
	if entries, err := os.ReadDir(root); err != nil || len(entries) != 0 {
		t.Fatalf("unexpected log artifacts = %#v, %v", entries, err)
	}
	if err := os.Rename(root, root+"-old"); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(root, 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := store.ReadTechnicalLogs(context.Background(), "project-a", now); !errors.Is(err, ErrUnsafeBundle) {
		t.Fatalf("replaced-root read error = %v", err)
	}
}
