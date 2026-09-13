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
	"strings"
	"syscall"
	"time"

	"github.com/mcuadros/director-engine/adapters/dolt"
	maintenanceapp "github.com/mcuadros/director-engine/application/taskstoremaintenance"
	"github.com/mcuadros/director-engine/domain/jsondocument"
)

type bootstrapLogCompactor struct{}

var errTaskStoreConfigOutputMismatch = errors.New("TaskStore config output differs")

const taskStoreConfigOutputMismatchMessage = "director-engine: DIRECTOR_TASKSTORE_CONFIG_OUTPUT_MISMATCH: the existing --config-output file differs; the TaskStore/database is not the cause and must not be deleted. Safe remediation: move the existing config file to an owner-only backup, rerun the identical bootstrap command to create a replacement, compare both files without exposing credentials, then deliberately restore the backup or adopt the replacement."

func (bootstrapLogCompactor) CompactExpired(context.Context, int64) error { return nil }

type taskStoreBootstrapResult struct {
	Event         string `json:"event"`
	Status        string `json:"status"`
	Authority     string `json:"authority,omitempty"`
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
	authority := ""
	if config.RequireLeastPrivilege {
		status, authorityErr := store.VerifyRuntimeAuthority(ctx)
		if authorityErr != nil || !status.Exact {
			return taskStoreBootstrapResult{}, dolt.ErrRuntimeAuthority
		}
		authority = string(status.Code)
	}
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
		Authority: authority, StoreID: config.StoreID, Database: config.Control.Database, SchemaVersion: version,
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
	ownerUser := flags.String("owner-user", "", "transient Dolt bootstrap owner identity")
	ownerPasswordFile := flags.String("owner-password-file", "", "optional absolute owner-only bootstrap password file")
	controlUser := flags.String("control-user", "", "Dolt runtime control identity")
	writerUser := flags.String("writer-user", "", "Dolt runtime writer identity")
	maintenanceUser := flags.String("maintenance-user", "", "Dolt runtime maintenance identity")
	controlPasswordFile := flags.String("control-password-file", "", "absolute owner-only control password file")
	writerPasswordFile := flags.String("writer-password-file", "", "absolute owner-only writer password file")
	maintenancePasswordFile := flags.String("maintenance-password-file", "", "absolute owner-only maintenance password file")
	privilegeFile := flags.String("privilege-file", "", "canonical owner-only Dolt privileges.db")
	maintenanceRoot := flags.String("maintenance-root", "", "optional absolute owner-only maintenance root")
	if err := flags.Parse(arguments); err != nil || flags.NArg() != 0 ||
		(*maintenanceRoot != "" && (!filepath.IsAbs(*maintenanceRoot) || filepath.Clean(*maintenanceRoot) != *maintenanceRoot)) {
		fmt.Fprintln(stderr, "usage: director-engine bootstrap-taskstore (--taskstore-config <absolute-path> | --config-output <absolute-path> --address <loopback:port> --database <name> --store-id <id> --owner-user <user> [--owner-password-file <absolute-path>] --control-user <user> --writer-user <user> --maintenance-user <user> --control-password-file <absolute-path> --writer-password-file <absolute-path> --maintenance-password-file <absolute-path> --privilege-file <absolute-path>) [--maintenance-root <absolute-path>]")
		return 2
	}
	seedMode := *configOutput != "" || *address != "" || *database != "" || *storeID != "" || *ownerUser != "" ||
		*ownerPasswordFile != "" || *controlUser != "" || *writerUser != "" || *maintenanceUser != "" ||
		*controlPasswordFile != "" || *writerPasswordFile != "" || *maintenancePasswordFile != "" || *privilegeFile != ""
	if (*configPath != "") == seedMode {
		fmt.Fprintln(stderr, "director-engine: choose exactly one existing-config or engine-seeded configuration mode")
		return 2
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Minute)
	defer cancel()
	if seedMode {
		result, err := provisionTaskStore(ctx, taskStoreProvisionInput{
			ConfigOutput: *configOutput, Address: *address, Database: *database, StoreID: *storeID,
			OwnerUser: *ownerUser, OwnerPasswordFile: *ownerPasswordFile,
			ControlUser: *controlUser, WriterUser: *writerUser, MaintenanceUser: *maintenanceUser,
			ControlPasswordFile: *controlPasswordFile, WriterPasswordFile: *writerPasswordFile,
			MaintenancePasswordFile: *maintenancePasswordFile, PrivilegeFile: *privilegeFile,
			MaintenanceRoot: *maintenanceRoot, NowMillis: time.Now().UnixMilli(),
		})
		if err != nil {
			if errors.Is(err, errTaskStoreConfigOutputMismatch) {
				fmt.Fprintln(stderr, taskStoreConfigOutputMismatchMessage)
			} else {
				fmt.Fprintln(stderr, "director-engine: TaskStore configuration seed refused incompatible or ambiguous state")
			}
			return 1
		}
		if err := writeJSON(stdout, result); err != nil {
			fmt.Fprintln(stderr, "director-engine: TaskStore bootstrap readback failed")
			return 1
		}
		return 0
	}
	config, err := readBoardServerConfig(*configPath)
	if err != nil {
		fmt.Fprintln(stderr, "director-engine: TaskStore bootstrap configuration is invalid")
		return 1
	}
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

type taskStoreProvisionInput struct {
	ConfigOutput            string
	Address                 string
	Database                string
	StoreID                 string
	OwnerUser               string
	OwnerPasswordFile       string
	ControlUser             string
	WriterUser              string
	MaintenanceUser         string
	ControlPasswordFile     string
	WriterPasswordFile      string
	MaintenancePasswordFile string
	PrivilegeFile           string
	MaintenanceRoot         string
	NowMillis               int64
}

func bootstrapPassword(path string, required bool) (string, error) {
	if path == "" {
		if required {
			return "", errors.New("TaskStore runtime password file is required")
		}
		return "", nil
	}
	content, err := privateFile(path, 16*1024)
	if err != nil {
		return "", errors.New("TaskStore bootstrap password file is invalid")
	}
	password := strings.TrimSpace(string(content))
	if password == "" {
		return "", errors.New("TaskStore bootstrap password file is empty")
	}
	return password, nil
}

func provisionTaskStore(ctx context.Context, input taskStoreProvisionInput) (taskStoreBootstrapResult, error) {
	if !filepath.IsAbs(input.ConfigOutput) || filepath.Clean(input.ConfigOutput) != input.ConfigOutput ||
		!loopbackListenAddress(input.Address) || input.Database == "" || input.StoreID == "" || input.OwnerUser == "" ||
		input.ControlUser == "" || input.WriterUser == "" || input.MaintenanceUser == "" || input.PrivilegeFile == "" ||
		input.NowMillis < 0 {
		return taskStoreBootstrapResult{}, errors.New("TaskStore provision input is invalid")
	}
	ownerPassword, err := bootstrapPassword(input.OwnerPasswordFile, false)
	if err != nil {
		return taskStoreBootstrapResult{}, err
	}
	controlPassword, err := bootstrapPassword(input.ControlPasswordFile, true)
	if err != nil {
		return taskStoreBootstrapResult{}, err
	}
	writerPassword, err := bootstrapPassword(input.WriterPasswordFile, true)
	if err != nil {
		return taskStoreBootstrapResult{}, err
	}
	maintenancePassword, err := bootstrapPassword(input.MaintenancePasswordFile, true)
	if err != nil {
		return taskStoreBootstrapResult{}, err
	}
	owner := dolt.Endpoint{Address: input.Address, Database: input.Database, User: input.OwnerUser, Password: ownerPassword}
	ownerConfig := dolt.Config{Control: owner, Writer: owner, Maintenance: owner, StoreID: input.StoreID}
	if _, err := bootstrapTaskStore(ctx, ownerConfig, input.MaintenanceRoot, input.NowMillis); err != nil {
		return taskStoreBootstrapResult{}, err
	}
	config, status, err := dolt.ProvisionRuntimeAuthority(ctx, dolt.RuntimeAuthorityBootstrap{
		Owner: owner, StoreID: input.StoreID, PrivilegeFile: input.PrivilegeFile,
		Control:     dolt.RuntimeIdentity{User: input.ControlUser, Host: "%", Password: controlPassword},
		Writer:      dolt.RuntimeIdentity{User: input.WriterUser, Host: "%", Password: writerPassword},
		Maintenance: dolt.RuntimeIdentity{User: input.MaintenanceUser, Host: "%", Password: maintenancePassword},
	})
	if err != nil || !status.Exact || status.Code != dolt.AuthorityCurrent {
		return taskStoreBootstrapResult{}, dolt.ErrRuntimeAuthority
	}
	if err := installTaskStoreConfig(input.ConfigOutput, config, input.ControlPasswordFile,
		input.WriterPasswordFile, input.MaintenancePasswordFile); err != nil {
		return taskStoreBootstrapResult{}, err
	}
	readback, err := readBoardServerConfig(input.ConfigOutput)
	if err != nil || readback.AuthoritySHA256 != config.AuthoritySHA256 ||
		readback.PrivilegeFileSHA256 != config.PrivilegeFileSHA256 {
		return taskStoreBootstrapResult{}, errors.New("TaskStore configuration readback differs")
	}
	return bootstrapTaskStore(ctx, readback, input.MaintenanceRoot, input.NowMillis)
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

func decodeTaskStoreConfigDocument(document []byte) (boardServerConfig, []byte, error) {
	canonical, err := jsondocument.CanonicalWithNormalizedNumbersLimit(document, maximumBoardServerConfigBytes)
	if err != nil {
		return boardServerConfig{}, nil, errors.New("TaskStore configuration is invalid")
	}
	decoder := json.NewDecoder(bytes.NewReader(canonical))
	decoder.DisallowUnknownFields()
	var value boardServerConfig
	if decoder.Decode(&value) != nil {
		return boardServerConfig{}, nil, errors.New("TaskStore configuration is invalid")
	}
	if _, err := decoder.Token(); !errors.Is(err, io.EOF) {
		return boardServerConfig{}, nil, errors.New("TaskStore configuration is invalid")
	}
	return value, canonical, nil
}

func sameTaskStoreConfigBinding(left, right boardServerConfig) bool {
	left.AuthoritySHA256, left.PrivilegeSHA256 = "", ""
	right.AuthoritySHA256, right.PrivilegeSHA256 = "", ""
	leftJSON, leftErr := json.Marshal(left)
	rightJSON, rightErr := json.Marshal(right)
	return leftErr == nil && rightErr == nil && bytes.Equal(leftJSON, rightJSON)
}

func exactPrivateConfig(path string) (boardServerConfig, []byte, error) {
	document, err := privateFile(path, maximumBoardServerConfigBytes)
	if err != nil {
		return boardServerConfig{}, nil, err
	}
	return decodeTaskStoreConfigDocument(document)
}

func installTaskStoreConfig(path string, config dolt.Config, controlPassword, writerPassword, maintenancePassword string) error {
	if !filepath.IsAbs(path) || filepath.Clean(path) != path || !config.RequireLeastPrivilege {
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
	maintenanceFile, err := passwordFileJSON(maintenancePassword)
	if err != nil {
		return err
	}
	document := boardServerConfig{SchemaVersion: 2, StoreID: config.StoreID,
		AuthoritySHA256: config.AuthoritySHA256, PrivilegeFile: config.PrivilegeFile, PrivilegeSHA256: config.PrivilegeFileSHA256,
		Control: boardServerEndpoint{Address: config.Control.Address, Database: config.Control.Database, User: config.Control.User,
			Principal: config.Control.Principal, PasswordFile: controlFile},
		Writer: boardServerEndpoint{Address: config.Writer.Address, Database: config.Writer.Database, User: config.Writer.User,
			Principal: config.Writer.Principal, PasswordFile: writerFile},
		Maintenance: boardServerEndpoint{Address: config.Maintenance.Address, Database: config.Maintenance.Database, User: config.Maintenance.User,
			Principal: config.Maintenance.Principal, PasswordFile: maintenanceFile}}
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
	desired, desiredCanonical, err := decodeTaskStoreConfigDocument(encoded)
	if err != nil {
		return err
	}
	targetPresent := false
	var priorCanonical []byte
	if existing, canonical, existingErr := exactPrivateConfig(path); existingErr == nil {
		targetPresent, priorCanonical = true, canonical
		if bytes.Equal(canonical, desiredCanonical) {
			return nil
		}
		if !sameTaskStoreConfigBinding(existing, desired) {
			return errTaskStoreConfigOutputMismatch
		}
	} else if _, statErr := os.Lstat(path); !errors.Is(statErr, os.ErrNotExist) {
		return errors.New("TaskStore configuration target is unsafe")
	}
	next := path + ".next"
	if _, stagedCanonical, stagedErr := exactPrivateConfig(next); stagedErr == nil {
		if !bytes.Equal(stagedCanonical, desiredCanonical) {
			return errors.New("TaskStore configuration staging state differs")
		}
		if targetPresent {
			_, currentCanonical, currentErr := exactPrivateConfig(path)
			if currentErr != nil || !bytes.Equal(currentCanonical, priorCanonical) {
				return errors.New("TaskStore configuration target became ambiguous")
			}
		} else if _, targetErr := os.Lstat(path); !errors.Is(targetErr, os.ErrNotExist) {
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
	if targetPresent {
		_, currentCanonical, currentErr := exactPrivateConfig(path)
		if currentErr != nil || !bytes.Equal(currentCanonical, priorCanonical) {
			return errors.New("TaskStore configuration target became ambiguous")
		}
	} else if _, targetErr := os.Lstat(path); !errors.Is(targetErr, os.ErrNotExist) {
		return errors.New("TaskStore configuration target became ambiguous")
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
