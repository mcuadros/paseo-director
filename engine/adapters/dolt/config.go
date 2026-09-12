// SPDX-License-Identifier: Apache-2.0

// Package dolt implements the Director Engine TaskStore port using the
// externally supervised direct Dolt 2.3.2 SQL server selected by ADR-0004 and
// ADR-0012. It owns SQL connections but no lifecycle or retry policy.
package dolt

import (
	"database/sql"
	"errors"
	"fmt"
	"net"
	"path/filepath"
	"time"

	"github.com/go-sql-driver/mysql"
)

// Endpoint identifies one authenticated connection to the same TaskStore.
// Credential values remain adapter-only and must never be logged or included
// in domain records.
type Endpoint struct {
	Address   string
	Database  string
	User      string
	Password  string
	Principal string
}

// Config binds independently pooled control, writer, and maintenance
// connections to one exact listener/database identity.
type Config struct {
	Control               Endpoint
	Writer                Endpoint
	Maintenance           Endpoint
	StoreID               string
	AuthoritySHA256       string
	PrivilegeFile         string
	PrivilegeFileSHA256   string
	RequireLeastPrivilege bool
}

func resolvedMaintenanceEndpoint(config Config) Endpoint {
	if config.Maintenance.Address == "" && !config.RequireLeastPrivilege {
		return config.Control
	}
	return config.Maintenance
}

func validateConfig(config Config) error {
	if config.Control.Address == "" || config.Control.Database == "" || config.Control.User == "" {
		return errors.New("control endpoint is incomplete")
	}
	if config.Writer.Address == "" || config.Writer.Database == "" || config.Writer.User == "" {
		return errors.New("writer endpoint is incomplete")
	}
	if config.Control.Address != config.Writer.Address || config.Control.Database != config.Writer.Database {
		return errors.New("control and writer endpoints must use one listener and database")
	}
	maintenance := resolvedMaintenanceEndpoint(config)
	if maintenance.Address == "" || maintenance.Database == "" || maintenance.User == "" {
		return errors.New("maintenance endpoint is incomplete")
	}
	if config.Control.Address != maintenance.Address || config.Control.Database != maintenance.Database {
		return errors.New("control, writer, and maintenance endpoints must use one listener and database")
	}
	host, _, err := net.SplitHostPort(config.Control.Address)
	if err != nil {
		return errors.New("TaskStore listener address is invalid")
	}
	address := net.ParseIP(host)
	if address == nil || !address.IsLoopback() {
		return errors.New("TaskStore listener must be an explicit loopback address")
	}
	if !safeIdentifier(config.Control.Database, 128) {
		return errors.New("TaskStore database identity is invalid")
	}
	if !safeIdentifier(config.StoreID, 128) {
		return errors.New("store identity is invalid")
	}
	if config.RequireLeastPrivilege {
		if !safeSQLPrincipal(config.Control.Principal) || !safeSQLPrincipal(config.Writer.Principal) ||
			!safeSQLPrincipal(maintenance.Principal) {
			return errors.New("TaskStore principal identity is invalid")
		}
		if config.Control.Principal == config.Writer.Principal || config.Control.Principal == maintenance.Principal ||
			config.Writer.Principal == maintenance.Principal {
			return errors.New("TaskStore runtime principals must be distinct")
		}
		if !lowerHexDigest(config.AuthoritySHA256) || !lowerHexDigest(config.PrivilegeFileSHA256) ||
			!filepath.IsAbs(config.PrivilegeFile) || filepath.Clean(config.PrivilegeFile) != config.PrivilegeFile {
			return errors.New("TaskStore authority attestation is invalid")
		}
	}
	return nil
}

func openEndpoint(endpoint Endpoint) (*sql.DB, error) {
	connector, err := mysql.NewConnector(&mysql.Config{
		User:                 endpoint.User,
		Passwd:               endpoint.Password,
		Net:                  "tcp",
		Addr:                 endpoint.Address,
		DBName:               endpoint.Database,
		AllowNativePasswords: true,
		CheckConnLiveness:    true,
		InterpolateParams:    false,
		MultiStatements:      false,
		ParseTime:            false,
		Timeout:              5 * time.Second,
		ReadTimeout:          10 * time.Second,
		WriteTimeout:         10 * time.Second,
	})
	if err != nil {
		return nil, fmt.Errorf("create Dolt SQL connector: %w", err)
	}
	database := sql.OpenDB(connector)
	database.SetMaxOpenConns(4)
	database.SetMaxIdleConns(2)
	database.SetConnMaxIdleTime(30 * time.Second)
	database.SetConnMaxLifetime(5 * time.Minute)
	return database, nil
}

// Open creates the adapter's private pools. Bootstrap must succeed before the
// returned store is used for reads or writes.
func Open(config Config) (*DoltTaskStore, error) {
	if err := validateConfig(config); err != nil {
		return nil, fmt.Errorf("open Dolt TaskStore: %w", err)
	}
	control, err := openEndpoint(config.Control)
	if err != nil {
		return nil, err
	}
	writer, err := openEndpoint(config.Writer)
	if err != nil {
		_ = control.Close()
		return nil, err
	}
	maintenanceEndpoint := resolvedMaintenanceEndpoint(config)
	maintenanceClient, err := openEndpoint(maintenanceEndpoint)
	if err != nil {
		_ = writer.Close()
		_ = control.Close()
		return nil, err
	}
	return &DoltTaskStore{
		control:               control,
		writer:                writer,
		maintenanceClient:     maintenanceClient,
		database:              config.Control.Database,
		address:               config.Control.Address,
		storeID:               config.StoreID,
		controlPrincipal:      config.Control.Principal,
		writerPrincipal:       config.Writer.Principal,
		maintenancePrincipal:  maintenanceEndpoint.Principal,
		authoritySHA256:       config.AuthoritySHA256,
		privilegeFile:         config.PrivilegeFile,
		privilegeFileSHA256:   config.PrivilegeFileSHA256,
		requireLeastPrivilege: config.RequireLeastPrivilege,
	}, nil
}

// Close releases the adapter-owned SQL pools. It does not stop or mutate the
// separately supervised Dolt process.
func (store *DoltTaskStore) Close() error {
	var joined error
	if store.maintenanceClient != nil {
		joined = errors.Join(joined, store.maintenanceClient.Close())
	}
	if store.writer != nil {
		joined = errors.Join(joined, store.writer.Close())
	}
	if store.control != nil {
		joined = errors.Join(joined, store.control.Close())
	}
	return joined
}
