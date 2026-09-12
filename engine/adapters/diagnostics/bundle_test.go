// SPDX-License-Identifier: Apache-2.0

package diagnostics

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	homeport "github.com/mcuadros/director-engine/ports/home"
)

func bundleInput() homeport.SupportBundleWrite {
	bundleID := strings.Repeat("a", 64)
	manifest, _ := json.Marshal(map[string]any{"schemaVersion": 1, "bundleId": bundleID, "generatedAt": "2026-09-12T01:00:00Z", "uploadPolicy": "never"})
	return homeport.SupportBundleWrite{BundleID: bundleID, FileName: "director-support-bbbbbbbbbbbb-aaaaaaaaaaaa.zip", GeneratedAtMillis: 1_757_640_000_000,
		Entries: map[string][]byte{
			"manifest.json": manifest,
			"health.json":   []byte(`{"status":"partial_sync"}`),
			"audit.json":    []byte(`{"entries":[],"payloadsIncluded":false,"pathsIncluded":false}`),
			"logs.json":     []byte(`{"entries":[],"rawOutputIncluded":false}`),
		}}
}

func TestBundleWriterCreatesMode0600LocalZipAndAdoptsExactReplay(t *testing.T) {
	root := filepath.Join(t.TempDir(), "support")
	writer, err := NewBundleWriter(root)
	if err != nil {
		t.Fatal(err)
	}
	input := bundleInput()
	evidence, err := writer.WriteSupportBundle(context.Background(), input)
	if err != nil || evidence.BundleID != input.BundleID || evidence.FileName != input.FileName || evidence.Permission != "0600" || evidence.UploadAttempted || evidence.Bytes == 0 {
		t.Fatalf("bundle evidence = %#v, %v", evidence, err)
	}
	info, err := os.Lstat(filepath.Join(root, input.FileName))
	if err != nil || info.Mode().Perm() != 0o600 || !info.Mode().IsRegular() {
		t.Fatalf("bundle mode = %v, %v", info, err)
	}
	content, err := os.ReadFile(filepath.Join(root, input.FileName))
	if err != nil {
		t.Fatal(err)
	}
	archive, err := zip.NewReader(bytes.NewReader(content), int64(len(content)))
	if err != nil || len(archive.File) != 4 {
		t.Fatalf("zip = %#v, %v", archive, err)
	}
	observed, exists, err := writer.ObserveSupportBundle(context.Background(), input.BundleID, input.FileName)
	if err != nil || !exists || observed.SHA256 != evidence.SHA256 || observed.Bytes != evidence.Bytes || observed.UploadAttempted {
		t.Fatalf("observed bundle = %#v exists=%v err=%v", observed, exists, err)
	}
	replayed, err := writer.WriteSupportBundle(context.Background(), input)
	if err != nil || replayed.SHA256 != evidence.SHA256 {
		t.Fatalf("replayed bundle = %#v, %v", replayed, err)
	}
}

func TestBundleWriterRefusesSecretsPathsUnknownEntriesAndRootReplacement(t *testing.T) {
	root := filepath.Join(t.TempDir(), "support")
	writer, err := NewBundleWriter(root)
	if err != nil {
		t.Fatal(err)
	}
	for name, mutate := range map[string]func(*homeport.SupportBundleWrite){
		"secret": func(input *homeport.SupportBundleWrite) {
			input.Entries["logs.json"] = []byte(`{"token":"github_pat_private"}`)
		},
		"path": func(input *homeport.SupportBundleWrite) {
			input.Entries["logs.json"] = []byte(`{"path":"/home/alice/repository"}`)
		},
		"unknown entry": func(input *homeport.SupportBundleWrite) {
			delete(input.Entries, "logs.json")
			input.Entries["raw.txt"] = []byte(`{"raw":true}`)
		},
	} {
		t.Run(name, func(t *testing.T) {
			input := bundleInput()
			mutate(&input)
			if _, err := writer.WriteSupportBundle(context.Background(), input); !errors.Is(err, ErrUnsafeBundle) {
				t.Fatalf("unsafe write error = %v", err)
			}
		})
	}
	if err := os.Rename(root, root+"-old"); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(root, 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := writer.WriteSupportBundle(context.Background(), bundleInput()); !errors.Is(err, ErrUnsafeBundle) {
		t.Fatalf("replaced root error = %v", err)
	}
}
