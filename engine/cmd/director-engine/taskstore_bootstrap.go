// SPDX-License-Identifier: Apache-2.0

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"syscall"
	"time"

	"github.com/mcuadros/director-engine/adapters/dolt"
	maintenanceapp "github.com/mcuadros/director-engine/application/taskstoremaintenance"
	"github.com/mcuadros/director-engine/domain/jsondocument"
)

type bootstrapLogCompactor struct{}

func (bootstrapLogCompactor) CompactExpired(context.Context, int64) error { return nil }

type taskStoreBootstrapResult struct {
	Event         string `json:"event"`
	Status        string `json:"status"`
	StoreID       string `json:"storeId"`
	Database      string `json:"database"`
	SchemaVersion int    `json:"schemaVersion"`
	EventCursor   uint64 `json:"eventCursor"`
	ProjectCount  int    `json:"projectCount"`
	Idempotent    bool   `json:"idempotent"`
}

func bootstrapTaskStore(ctx context.Context, config dolt.Config, maintenanceRoot string, nowMillis int64) (taskStoreBootstrapResult, error) {
	store, err := dolt.Open(config)
	if err != nil {
		return taskStoreBootstrapResult{}, err
	}
	defer store.Close()
	if err := store.Bootstrap(ctx); err != nil {
		if maintenanceRoot == "" {
			maintenanceRoot, err = dolt.DefaultMaintenanceRoot(config.StoreID)
			if err != nil {
				return taskStoreBootstrapResult{}, err
			}
		}
		maintenance, maintenanceErr := dolt.NewMaintenance(store, maintenanceRoot, bootstrapLogCompactor{})
		if maintenanceErr != nil {
			return taskStoreBootstrapResult{}, err
		}
		observation, observationErr := maintenance.ObserveStore(ctx)
		if observationErr != nil || !observation.Exact || observation.SchemaVersion != 1 {
			return taskStoreBootstrapResult{}, err
		}
		service, serviceErr := maintenanceapp.NewService(maintenance, maintenance)
		if serviceErr != nil {
			return taskStoreBootstrapResult{}, serviceErr
		}
		migrationID := "taskstore-bootstrap-schema-1-to-2"
		if _, migrationErr := service.Migrate(ctx, maintenanceapp.MigrationCommand{ID: migrationID,
			FromVersion: 1, ToVersion: 2, NowMillis: nowMillis}); migrationErr != nil {
			return taskStoreBootstrapResult{}, migrationErr
		}
		if err := store.Bootstrap(ctx); err != nil {
			return taskStoreBootstrapResult{}, err
		}
	}
	version, err := store.SchemaVersion(ctx)
	if err != nil || version != 2 {
		if err == nil {
			err = errors.New("TaskStore schema readback did not match")
		}
		return taskStoreBootstrapResult{}, fmt.Errorf("schema readback: %w", err)
	}
	cursor, err := store.LatestEventSequence(ctx)
	if err != nil {
		return taskStoreBootstrapResult{}, fmt.Errorf("event cursor readback: %w", err)
	}
	projects, err := store.Projects(ctx)
	if err != nil {
		return taskStoreBootstrapResult{}, fmt.Errorf("Project readback: %w", err)
	}
	return taskStoreBootstrapResult{Event: "director-engine.taskstore-ready", Status: "ready",
		StoreID: config.StoreID, Database: config.Control.Database, SchemaVersion: version,
		EventCursor: cursor, ProjectCount: len(projects), Idempotent: true}, nil
}

func runTaskStoreBootstrap(arguments []string, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("bootstrap-taskstore", flag.ContinueOnError)
	flags.SetOutput(stderr)
	configPath := flags.String("taskstore-config", "", "absolute private TaskStore configuration path")
	configOutput := flags.String("config-output", "", "write one exact owner-only TaskStore configuration")
	address := flags.String("address", "", "exact loopback Dolt listener")
	database := flags.String("database", "", "exact Dolt database identity")
	storeID := flags.String("store-id", "", "stable Director TaskStore identity")
	controlUser := flags.String("control-user", "", "Dolt bootstrap/control identity")
	writerUser := flags.String("writer-user", "", "Dolt runtime writer identity")
	controlPasswordFile := flags.String("control-password-file", "", "optional absolute owner-only control password file")
	writerPasswordFile := flags.String("writer-password-file", "", "optional absolute owner-only writer password file")
	maintenanceRoot := flags.String("maintenance-root", "", "optional absolute owner-only maintenance root")
	if err := flags.Parse(arguments); err != nil || flags.NArg() != 0 ||
		(*maintenanceRoot != "" && (!filepath.IsAbs(*maintenanceRoot) || filepath.Clean(*maintenanceRoot) != *maintenanceRoot)) {
		fmt.Fprintln(stderr, "usage: director-engine bootstrap-taskstore (--taskstore-config <absolute-path> | --config-output <absolute-path> --address <loopback:port> --database <name> --store-id <id> --control-user <user> [--writer-user <user>] [--control-password-file <absolute-path>] [--writer-password-file <absolute-path>]) [--maintenance-root <absolute-path>]")
		return 2
	}
	seedMode := *configOutput != "" || *address != "" || *database != "" || *storeID != "" || *controlUser != "" || *writerUser != "" || *controlPasswordFile != "" || *writerPasswordFile != ""
	if (*configPath != "") == seedMode {
		fmt.Fprintln(stderr, "director-engine: choose exactly one existing-config or engine-seeded configuration mode")
		return 2
	}
	if seedMode {
		if *writerUser == "" {
			*writerUser = *controlUser
		}
		if err := seedTaskStoreConfig(*configOutput, *address, *database, *storeID, *controlUser, *writerUser,
			*controlPasswordFile, *writerPasswordFile); err != nil {
			fmt.Fprintln(stderr, "director-engine: TaskStore configuration seed refused incompatible or ambiguous state")
			return 1
		}
		*configPath = *configOutput
	}
	config, err := readBoardServerConfig(*configPath)
	if err != nil {
		fmt.Fprintln(stderr, "director-engine: TaskStore bootstrap configuration is invalid")
		return 1
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Minute)
	defer cancel()
	result, err := bootstrapTaskStore(ctx, config, *maintenanceRoot, time.Now().UnixMilli())
	if err != nil {
		fmt.Fprintln(stderr, "director-engine: TaskStore bootstrap refused incompatible or ambiguous state")
		return 1
	}
	if err := writeJSON(stdout, result); err != nil {
		fmt.Fprintln(stderr, "director-engine: TaskStore bootstrap readback failed")
		return 1
	}
	return 0
}

func passwordFileJSON(path string) (json.RawMessage, error) {
	if path == "" {
		return json.RawMessage("null"), nil
	}
	if !filepath.IsAbs(path) || filepath.Clean(path) != path {
		return nil, errors.New("password file path is invalid")
	}
	encoded, err := json.Marshal(path)
	return encoded, err
}

func seedTaskStoreConfig(path, address, database, storeID, controlUser, writerUser, controlPassword, writerPassword string) error {
	if !filepath.IsAbs(path) || filepath.Clean(path) != path || !loopbackListenAddress(address) || database == "" || storeID == "" || controlUser == "" || writerUser == "" {
		return errors.New("TaskStore seed is invalid")
	}
	controlFile, err := passwordFileJSON(controlPassword)
	if err != nil {
		return err
	}
	writerFile, err := passwordFileJSON(writerPassword)
	if err != nil {
		return err
	}
	document := boardServerConfig{SchemaVersion: 1, StoreID: storeID,
		Control: boardServerEndpoint{Address: address, Database: database, User: controlUser, PasswordFile: controlFile},
		Writer:  boardServerEndpoint{Address: address, Database: database, User: writerUser, PasswordFile: writerFile}}
	encoded, err := json.Marshal(document)
	if err != nil || len(encoded) > maximumBoardServerConfigBytes {
		return errors.New("TaskStore seed is invalid")
	}
	parent := filepath.Dir(path)
	if err := os.MkdirAll(parent, 0o700); err != nil {
		return errors.New("TaskStore configuration parent is unavailable")
	}
	parentInfo, err := os.Lstat(parent)
	if err != nil {
		return errors.New("TaskStore configuration parent is unsafe")
	}
	identity, ok := parentInfo.Sys().(*syscall.Stat_t)
	if !parentInfo.IsDir() || parentInfo.Mode().Perm()&0o077 != 0 || !ok || identity.Uid != uint32(os.Geteuid()) {
		return errors.New("TaskStore configuration parent is unsafe")
	}
	if existing, err := privateFile(path, maximumBoardServerConfigBytes); err == nil {
		left, leftErr := jsondocument.Canonical(existing)
		right, rightErr := jsondocument.Canonical(encoded)
		if leftErr != nil || rightErr != nil || !bytes.Equal(left, right) {
			return errors.New("TaskStore configuration already differs")
		}
		return nil
	} else if !errors.Is(err, os.ErrNotExist) {
		if _, statErr := os.Lstat(path); !errors.Is(statErr, os.ErrNotExist) {
			return errors.New("TaskStore configuration target is unsafe")
		}
	}
	next := path + ".next"
	if staged, stagedErr := privateFile(next, maximumBoardServerConfigBytes); stagedErr == nil {
		left, leftErr := jsondocument.Canonical(staged)
		right, rightErr := jsondocument.Canonical(encoded)
		if leftErr != nil || rightErr != nil || !bytes.Equal(left, right) {
			return errors.New("TaskStore configuration staging state differs")
		}
		if _, targetErr := os.Lstat(path); !errors.Is(targetErr, os.ErrNotExist) {
			return errors.New("TaskStore configuration target became ambiguous")
		}
		if err := os.Rename(next, path); err != nil {
			return errors.New("TaskStore configuration staging state could not be recovered")
		}
		return syncTaskStoreConfigDirectory(parent)
	} else if _, statErr := os.Lstat(next); !errors.Is(statErr, os.ErrNotExist) {
		return errors.New("TaskStore configuration staging target is unsafe")
	}
	file, err := os.OpenFile(next, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return errors.New("TaskStore configuration staging target is unavailable")
	}
	if _, err = file.Write(append(encoded, '\n')); err == nil {
		err = file.Sync()
	}
	closeErr := file.Close()
	if err != nil || closeErr != nil {
		return errors.New("TaskStore configuration could not be durably staged")
	}
	if err := os.Rename(next, path); err != nil {
		return errors.New("TaskStore configuration could not be installed")
	}
	return syncTaskStoreConfigDirectory(parent)
}

func syncTaskStoreConfigDirectory(parent string) error {
	directory, err := os.Open(parent)
	if err != nil {
		return errors.New("TaskStore configuration directory is unavailable")
	}
	err = directory.Sync()
	closeErr := directory.Close()
	if err != nil || closeErr != nil {
		return errors.New("TaskStore configuration directory could not be flushed")
	}
	return nil
}
