// SPDX-License-Identifier: Apache-2.0

package diagnostics

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"

	homeport "github.com/mcuadros/director-engine/ports/home"
)

const (
	logRetentionMillis    int64 = 14 * 24 * 60 * 60 * 1_000
	logRetentionBytes           = 100 * 1024 * 1024
	maximumLogLineBytes         = 2 * 1024
	maximumReadLogEntries       = 129
)

type LogStore struct {
	mu       sync.Mutex
	root     string
	identity rootIdentity
}

type logRecord struct {
	Sequence         uint64                         `json:"sequence"`
	OccurredAtMillis int64                          `json:"occurredAtMillis"`
	Level            homeport.TechnicalLogLevel     `json:"level"`
	Component        homeport.TechnicalLogComponent `json:"component"`
	Code             homeport.TechnicalLogCode      `json:"code"`
	Occurrences      uint64                         `json:"occurrences"`
}

func DefaultLogRoot() (string, error) {
	root, err := os.UserCacheDir()
	if err != nil || !filepath.IsAbs(root) {
		return "", ErrUnsafeBundle
	}
	return filepath.Join(root, "director", "logs"), nil
}

func NewLogStore(root string) (*LogStore, error) {
	if !filepath.IsAbs(root) || filepath.Clean(root) != root {
		return nil, ErrUnsafeBundle
	}
	if err := os.MkdirAll(root, 0o700); err != nil || os.Chmod(root, 0o700) != nil {
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
	return &LogStore{root: root, identity: identity}, nil
}

func logFile(root, projectID string) (string, bool) {
	if projectID == "" || len(projectID) > 128 || projectID != strings.TrimSpace(projectID) || strings.IndexFunc(projectID, func(character rune) bool { return character < 0x20 || character == 0x7f }) >= 0 {
		return "", false
	}
	digest := sha256.Sum256([]byte("director-technical-log\x1f" + projectID))
	return filepath.Join(root, "project-"+hex.EncodeToString(digest[:])+".jsonl"), true
}

func validRecord(value logRecord, now int64) bool {
	observation := homeport.TechnicalLogObservation{Sequence: value.Sequence, OccurredAtMillis: value.OccurredAtMillis, Level: value.Level, Component: value.Component, Code: value.Code, Occurrences: value.Occurrences}
	if observation.Sequence == 0 || observation.OccurredAtMillis <= 0 || observation.OccurredAtMillis > now || observation.Occurrences == 0 {
		return false
	}
	switch observation.Level {
	case homeport.TechnicalLogInfo, homeport.TechnicalLogWarning, homeport.TechnicalLogError:
	default:
		return false
	}
	switch observation.Component {
	case homeport.TechnicalLogEngine, homeport.TechnicalLogTaskStore, homeport.TechnicalLogGit, homeport.TechnicalLogDolt,
		homeport.TechnicalLogReconciliation, homeport.TechnicalLogSecurity, homeport.TechnicalLogSupport:
	default:
		return false
	}
	switch observation.Code {
	case homeport.TechnicalLogEngineStarted, homeport.TechnicalLogReconciliationCompleted, homeport.TechnicalLogReconciliationWaiting,
		homeport.TechnicalLogGitSyncCurrent, homeport.TechnicalLogGitSyncFailed, homeport.TechnicalLogDoltSyncCurrent,
		homeport.TechnicalLogDoltSyncFailed, homeport.TechnicalLogTaskStoreUnhealthy, homeport.TechnicalLogSupportBundleGenerated,
		homeport.TechnicalLogUnsafeOutputSuppressed:
		return true
	default:
		return false
	}
}

func safeLogFile(path string) (os.FileInfo, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm() != 0o600 || !ok || int(stat.Uid) != os.Geteuid() || info.Size() < 0 || info.Size() > logRetentionBytes {
		return nil, ErrUnsafeBundle
	}
	return info, nil
}

func decodeLogLine(line []byte, now int64) (logRecord, error) {
	if len(line) == 0 || len(line) > maximumLogLineBytes || !json.Valid(line) {
		return logRecord{}, ErrUnsafeBundle
	}
	decoder := json.NewDecoder(bytes.NewReader(line))
	decoder.DisallowUnknownFields()
	var value logRecord
	if decoder.Decode(&value) != nil {
		return logRecord{}, ErrUnsafeBundle
	}
	if _, err := decoder.Token(); !errors.Is(err, io.EOF) || !validRecord(value, now) {
		return logRecord{}, ErrUnsafeBundle
	}
	return value, nil
}

func scanLogs(file *os.File, now int64, keep int) ([]logRecord, uint64, error) {
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		return nil, 0, ErrUnsafeBundle
	}
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, maximumLogLineBytes), maximumLogLineBytes)
	rows := make([]logRecord, 0, keep)
	var maximum uint64
	for scanner.Scan() {
		value, err := decodeLogLine(scanner.Bytes(), now)
		if err != nil || value.Sequence <= maximum {
			return nil, 0, ErrUnsafeBundle
		}
		maximum = value.Sequence
		if now-value.OccurredAtMillis > logRetentionMillis {
			continue
		}
		if len(rows) == keep {
			copy(rows, rows[1:])
			rows[len(rows)-1] = value
		} else {
			rows = append(rows, value)
		}
	}
	if scanner.Err() != nil {
		return nil, 0, ErrUnsafeBundle
	}
	return rows, maximum, nil
}

func (store *LogStore) open(projectID string, create bool) (*os.File, string, error) {
	identity, err := directoryIdentity(store.root)
	if err != nil || identity != store.identity {
		return nil, "", ErrUnsafeBundle
	}
	path, ok := logFile(store.root, projectID)
	if !ok {
		return nil, "", ErrUnsafeBundle
	}
	before, statErr := safeLogFile(path)
	if errors.Is(statErr, os.ErrNotExist) {
		if !create {
			return nil, path, os.ErrNotExist
		}
		file, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_RDWR, 0o600)
		if err != nil {
			return nil, "", ErrUnsafeBundle
		}
		return file, path, nil
	}
	if statErr != nil {
		return nil, "", ErrUnsafeBundle
	}
	flag := os.O_RDONLY
	if create {
		flag = os.O_RDWR | os.O_APPEND
	}
	file, err := os.OpenFile(path, flag, 0)
	if err != nil {
		return nil, "", ErrUnsafeBundle
	}
	opened, err := file.Stat()
	if err != nil || !os.SameFile(before, opened) {
		_ = file.Close()
		return nil, "", ErrUnsafeBundle
	}
	return file, path, nil
}

func encodeLogRecord(value logRecord) ([]byte, error) {
	encoded, err := json.Marshal(value)
	if err != nil || len(encoded) == 0 || len(encoded)+1 > maximumLogLineBytes {
		return nil, ErrUnsafeBundle
	}
	return append(encoded, '\n'), nil
}

func (store *LogStore) compact(path string, rows []logRecord) error {
	temporary, err := os.CreateTemp(store.root, ".director-logs-*.tmp")
	if err != nil {
		return ErrUnsafeBundle
	}
	temporaryPath := temporary.Name()
	defer os.Remove(temporaryPath)
	if temporary.Chmod(0o600) != nil {
		_ = temporary.Close()
		return ErrUnsafeBundle
	}
	for _, row := range rows {
		encoded, err := encodeLogRecord(row)
		if err != nil {
			_ = temporary.Close()
			return err
		}
		if _, err := temporary.Write(encoded); err != nil {
			_ = temporary.Close()
			return ErrUnsafeBundle
		}
	}
	if temporary.Sync() != nil || temporary.Close() != nil {
		return ErrUnsafeBundle
	}
	if _, err := safeLogFile(path); err != nil {
		return ErrUnsafeBundle
	}
	identity, err := directoryIdentity(store.root)
	if err != nil || identity != store.identity || os.Rename(temporaryPath, path) != nil {
		return ErrUnsafeBundle
	}
	directory, err := os.Open(store.root)
	if err != nil {
		return ErrUnsafeBundle
	}
	syncErr, closeErr := directory.Sync(), directory.Close()
	if syncErr != nil || closeErr != nil {
		return ErrUnsafeBundle
	}
	return nil
}

func (store *LogStore) Append(ctx context.Context, projectID string, occurredAtMillis int64, level homeport.TechnicalLogLevel, component homeport.TechnicalLogComponent, code homeport.TechnicalLogCode, occurrences uint64) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if !validRecord(logRecord{Sequence: 1, OccurredAtMillis: occurredAtMillis, Level: level, Component: component, Code: code, Occurrences: occurrences}, occurredAtMillis) {
		return ErrUnsafeBundle
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	file, path, err := store.open(projectID, true)
	if err != nil {
		return err
	}
	rows, maximum, scanErr := scanLogs(file, occurredAtMillis, maximumReadLogEntries)
	info, statErr := file.Stat()
	if scanErr != nil || statErr != nil {
		_ = file.Close()
		return ErrUnsafeBundle
	}
	if info.Size() > 90*1024*1024 {
		if file.Close() != nil || store.compact(path, rows) != nil {
			return ErrUnsafeBundle
		}
		file, _, err = store.open(projectID, true)
		if err != nil {
			return err
		}
		info, err = file.Stat()
		if err != nil {
			_ = file.Close()
			return ErrUnsafeBundle
		}
	}
	record := logRecord{Sequence: maximum + 1, OccurredAtMillis: occurredAtMillis, Level: level, Component: component, Code: code, Occurrences: occurrences}
	if !validRecord(record, occurredAtMillis) {
		_ = file.Close()
		return ErrUnsafeBundle
	}
	encoded, err := encodeLogRecord(record)
	if err != nil || info.Size()+int64(len(encoded)) > logRetentionBytes {
		_ = file.Close()
		return ErrUnsafeBundle
	}
	if _, err := file.Write(encoded); err != nil || file.Sync() != nil || file.Close() != nil {
		return ErrUnsafeBundle
	}
	return nil
}

func (store *LogStore) ReadTechnicalLogs(ctx context.Context, projectID string, now int64) ([]homeport.TechnicalLogObservation, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	file, _, err := store.open(projectID, false)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	before, err := file.Stat()
	if err != nil {
		_ = file.Close()
		return nil, ErrUnsafeBundle
	}
	rows, _, scanErr := scanLogs(file, now, maximumReadLogEntries)
	after, statErr := file.Stat()
	closeErr := file.Close()
	if scanErr != nil || statErr != nil || closeErr != nil || before.Size() != after.Size() || before.ModTime() != after.ModTime() || before.Mode() != after.Mode() {
		return nil, ErrUnsafeBundle
	}
	result := make([]homeport.TechnicalLogObservation, 0, len(rows))
	for _, row := range rows {
		result = append(result, homeport.TechnicalLogObservation{Sequence: row.Sequence, OccurredAtMillis: row.OccurredAtMillis, Level: row.Level, Component: row.Component, Code: row.Code, Occurrences: row.Occurrences})
	}
	return result, nil
}
