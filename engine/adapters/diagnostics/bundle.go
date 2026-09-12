// SPDX-License-Identifier: Apache-2.0

// Package diagnostics implements the local-only support-bundle sink. It has
// no networking dependency or upload operation.
package diagnostics

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"syscall"
	"time"

	homeport "github.com/mcuadros/director-engine/ports/home"
)

const maximumBundleBytes = 8 * 1024 * 1024

var (
	bundleIDPattern   = regexp.MustCompile(`^[0-9a-f]{64}$`)
	bundleNamePattern = regexp.MustCompile(`^director-support-[0-9a-f]{12}-[0-9a-f]{12}\.zip$`)
	allowedEntries    = []string{"audit.json", "health.json", "logs.json", "manifest.json"}
	ErrUnsafeBundle   = errors.New("support bundle is unsafe or unverifiable")
)

type rootIdentity struct {
	device uint64
	inode  uint64
}

type BundleWriter struct {
	root     string
	identity rootIdentity
}

func DefaultSupportRoot() (string, error) {
	root, err := os.UserCacheDir()
	if err != nil || !filepath.IsAbs(root) {
		return "", ErrUnsafeBundle
	}
	return filepath.Join(root, "director", "support"), nil
}

func directoryIdentity(path string) (rootIdentity, error) {
	info, err := os.Lstat(path)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm()&0o077 != 0 {
		return rootIdentity{}, ErrUnsafeBundle
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || int(stat.Uid) != os.Geteuid() {
		return rootIdentity{}, ErrUnsafeBundle
	}
	return rootIdentity{device: uint64(stat.Dev), inode: uint64(stat.Ino)}, nil
}

func NewBundleWriter(root string) (*BundleWriter, error) {
	if !filepath.IsAbs(root) || filepath.Clean(root) != root {
		return nil, ErrUnsafeBundle
	}
	if err := os.MkdirAll(root, 0o700); err != nil {
		return nil, ErrUnsafeBundle
	}
	if err := os.Chmod(root, 0o700); err != nil {
		return nil, ErrUnsafeBundle
	}
	resolved, err := filepath.EvalSymlinks(root)
	if err != nil || resolved != root {
		return nil, ErrUnsafeBundle
	}
	identity, err := directoryIdentity(root)
	if err != nil {
		return nil, err
	}
	return &BundleWriter{root: root, identity: identity}, nil
}

func (writer *BundleWriter) validRoot() bool {
	identity, err := directoryIdentity(writer.root)
	return err == nil && identity == writer.identity
}

func validRequest(bundleID, fileName string) bool {
	return bundleIDPattern.MatchString(bundleID) && bundleNamePattern.MatchString(fileName) && filepath.Base(fileName) == fileName
}

func safeEntry(content []byte) bool {
	if len(content) == 0 || len(content) > maximumBundleBytes || !json.Valid(content) {
		return false
	}
	lower := strings.ToLower(string(content))
	for _, forbidden := range []string{"/home/", "/tmp/", "\\users\\", "file://", "authorization:", "bearer ", "basic ", "token=", "password=", "secret=", "api_key=", "apikey=", "github_pat_", "ghp_", "gho_", "sk-", "-----begin private key-----", "https://user:"} {
		if strings.Contains(lower, forbidden) {
			return false
		}
	}
	return true
}

func encodeBundle(input homeport.SupportBundleWrite) ([]byte, error) {
	if !validRequest(input.BundleID, input.FileName) || input.GeneratedAtMillis <= 0 || len(input.Entries) != len(allowedEntries) {
		return nil, ErrUnsafeBundle
	}
	names := make([]string, 0, len(input.Entries))
	for name, content := range input.Entries {
		if !slices.Contains(allowedEntries, name) || filepath.Base(name) != name || !safeEntry(content) {
			return nil, ErrUnsafeBundle
		}
		names = append(names, name)
	}
	slices.Sort(names)
	if !slices.Equal(names, allowedEntries) {
		return nil, ErrUnsafeBundle
	}
	var output bytes.Buffer
	archive := zip.NewWriter(&output)
	for _, name := range names {
		header := &zip.FileHeader{Name: name, Method: zip.Deflate}
		header.SetMode(0o600)
		header.SetModTime(time.UnixMilli(input.GeneratedAtMillis).UTC())
		entry, err := archive.CreateHeader(header)
		if err != nil {
			return nil, ErrUnsafeBundle
		}
		if _, err := entry.Write(input.Entries[name]); err != nil {
			return nil, ErrUnsafeBundle
		}
	}
	if err := archive.Close(); err != nil || output.Len() > maximumBundleBytes {
		return nil, ErrUnsafeBundle
	}
	return output.Bytes(), nil
}

func stableFile(path string) ([]byte, os.FileInfo, error) {
	before, err := os.Lstat(path)
	if err != nil {
		return nil, nil, err
	}
	stat, ok := before.Sys().(*syscall.Stat_t)
	if !before.Mode().IsRegular() || before.Mode()&os.ModeSymlink != 0 || before.Mode().Perm() != 0o600 || !ok || int(stat.Uid) != os.Geteuid() || before.Size() <= 0 || before.Size() > maximumBundleBytes {
		return nil, nil, ErrUnsafeBundle
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, nil, ErrUnsafeBundle
	}
	defer file.Close()
	opened, err := file.Stat()
	if err != nil || !os.SameFile(before, opened) {
		return nil, nil, ErrUnsafeBundle
	}
	content, err := io.ReadAll(io.LimitReader(file, maximumBundleBytes+1))
	if err != nil || len(content) == 0 || len(content) > maximumBundleBytes {
		return nil, nil, ErrUnsafeBundle
	}
	after, err := file.Stat()
	if err != nil || opened.Size() != after.Size() || opened.ModTime() != after.ModTime() || opened.Mode() != after.Mode() {
		return nil, nil, ErrUnsafeBundle
	}
	return content, after, nil
}

func inspectBundle(content []byte, expectedID string) (int64, error) {
	reader, err := zip.NewReader(bytes.NewReader(content), int64(len(content)))
	if err != nil || len(reader.File) != len(allowedEntries) {
		return 0, ErrUnsafeBundle
	}
	names := make([]string, 0, len(reader.File))
	generatedAt := int64(0)
	for _, entry := range reader.File {
		if filepath.Base(entry.Name) != entry.Name || !slices.Contains(allowedEntries, entry.Name) || entry.UncompressedSize64 > maximumBundleBytes {
			return 0, ErrUnsafeBundle
		}
		names = append(names, entry.Name)
		stream, err := entry.Open()
		if err != nil {
			return 0, ErrUnsafeBundle
		}
		value, readErr := io.ReadAll(io.LimitReader(stream, maximumBundleBytes+1))
		closeErr := stream.Close()
		if readErr != nil || closeErr != nil || !safeEntry(value) {
			return 0, ErrUnsafeBundle
		}
		if entry.Name == "manifest.json" {
			var manifest struct {
				BundleID    string `json:"bundleId"`
				GeneratedAt string `json:"generatedAt"`
			}
			if json.Unmarshal(value, &manifest) != nil || manifest.BundleID != expectedID {
				return 0, ErrUnsafeBundle
			}
			parsed, err := time.Parse(time.RFC3339Nano, manifest.GeneratedAt)
			if err != nil {
				return 0, ErrUnsafeBundle
			}
			generatedAt = parsed.UnixMilli()
		}
	}
	slices.Sort(names)
	if !slices.Equal(names, allowedEntries) || generatedAt <= 0 {
		return 0, ErrUnsafeBundle
	}
	return generatedAt, nil
}

func (writer *BundleWriter) ObserveSupportBundle(_ context.Context, bundleID, fileName string) (homeport.SupportBundleEvidence, bool, error) {
	if !validRequest(bundleID, fileName) || !writer.validRoot() {
		return homeport.SupportBundleEvidence{}, false, ErrUnsafeBundle
	}
	path := filepath.Join(writer.root, fileName)
	content, info, err := stableFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return homeport.SupportBundleEvidence{}, false, nil
	}
	if err != nil {
		return homeport.SupportBundleEvidence{}, false, ErrUnsafeBundle
	}
	generatedAt, err := inspectBundle(content, bundleID)
	if err != nil {
		return homeport.SupportBundleEvidence{}, false, err
	}
	digest := sha256.Sum256(content)
	return homeport.SupportBundleEvidence{BundleID: bundleID, FileName: fileName, SHA256: hex.EncodeToString(digest[:]), Bytes: uint64(info.Size()), Permission: "0600", GeneratedAtMillis: generatedAt, UploadAttempted: false}, true, nil
}

func (writer *BundleWriter) WriteSupportBundle(ctx context.Context, input homeport.SupportBundleWrite) (homeport.SupportBundleEvidence, error) {
	if err := ctx.Err(); err != nil || !writer.validRoot() {
		return homeport.SupportBundleEvidence{}, ErrUnsafeBundle
	}
	if evidence, exists, err := writer.ObserveSupportBundle(ctx, input.BundleID, input.FileName); err != nil || exists {
		return evidence, err
	}
	content, err := encodeBundle(input)
	if err != nil {
		return homeport.SupportBundleEvidence{}, err
	}
	temporary, err := os.CreateTemp(writer.root, ".director-support-*.tmp")
	if err != nil {
		return homeport.SupportBundleEvidence{}, ErrUnsafeBundle
	}
	temporaryPath := temporary.Name()
	defer os.Remove(temporaryPath)
	failed := true
	defer func() {
		if failed {
			_ = temporary.Close()
		}
	}()
	if err := temporary.Chmod(0o600); err != nil {
		return homeport.SupportBundleEvidence{}, ErrUnsafeBundle
	}
	if _, err := temporary.Write(content); err != nil || temporary.Sync() != nil || temporary.Close() != nil || !writer.validRoot() {
		return homeport.SupportBundleEvidence{}, ErrUnsafeBundle
	}
	target := filepath.Join(writer.root, input.FileName)
	if err := os.Link(temporaryPath, target); err != nil {
		if evidence, exists, observeErr := writer.ObserveSupportBundle(ctx, input.BundleID, input.FileName); observeErr == nil && exists {
			failed = false
			return evidence, nil
		}
		return homeport.SupportBundleEvidence{}, ErrUnsafeBundle
	}
	directory, err := os.Open(writer.root)
	if err != nil {
		return homeport.SupportBundleEvidence{}, ErrUnsafeBundle
	}
	syncErr := directory.Sync()
	closeErr := directory.Close()
	if syncErr != nil || closeErr != nil {
		return homeport.SupportBundleEvidence{}, ErrUnsafeBundle
	}
	failed = false
	evidence, exists, err := writer.ObserveSupportBundle(ctx, input.BundleID, input.FileName)
	if err != nil || !exists {
		return homeport.SupportBundleEvidence{}, fmt.Errorf("%w: final bundle is absent", ErrUnsafeBundle)
	}
	return evidence, nil
}
