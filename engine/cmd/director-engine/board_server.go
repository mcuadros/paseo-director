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
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/mcuadros/director-engine/adapters/dolt"
	"github.com/mcuadros/director-engine/application/board"
	executionapp "github.com/mcuadros/director-engine/application/execution"
	"github.com/mcuadros/director-engine/domain/jsondocument"
	planningport "github.com/mcuadros/director-engine/ports/planning"
)

const maximumBoardServerConfigBytes = 64 * 1024

type boardServerEndpoint struct {
	Address      string          `json:"address"`
	Database     string          `json:"database"`
	User         string          `json:"user"`
	PasswordFile json.RawMessage `json:"passwordFile"`
}

type boardServerConfig struct {
	SchemaVersion int                 `json:"schemaVersion"`
	StoreID       string              `json:"storeId"`
	Control       boardServerEndpoint `json:"control"`
	Writer        boardServerEndpoint `json:"writer"`
}

func privateFile(path string, maximum int) ([]byte, error) {
	if !filepath.IsAbs(path) {
		return nil, errors.New("private file path must be absolute")
	}
	before, err := os.Lstat(path)
	if err != nil {
		return nil, errors.New("private file is unavailable or unsafe")
	}
	identity, identityOK := before.Sys().(*syscall.Stat_t)
	if !before.Mode().IsRegular() || before.Mode().Perm()&0o077 != 0 ||
		!identityOK || int(identity.Uid) != os.Geteuid() {
		return nil, errors.New("private file is unavailable or unsafe")
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, errors.New("private file is unavailable or unsafe")
	}
	defer file.Close()
	opened, err := file.Stat()
	if err != nil || !os.SameFile(before, opened) {
		return nil, errors.New("private file identity changed")
	}
	content, err := io.ReadAll(io.LimitReader(file, int64(maximum)+1))
	if err != nil || len(content) > maximum {
		return nil, errors.New("private file cannot be read within its bound")
	}
	after, err := file.Stat()
	if err != nil || opened.Size() != after.Size() || opened.ModTime() != after.ModTime() || opened.Mode() != after.Mode() {
		return nil, errors.New("private file metadata changed")
	}
	return content, nil
}

func endpointConfig(endpoint boardServerEndpoint) (dolt.Endpoint, error) {
	if endpoint.Address == "" || endpoint.Database == "" || endpoint.User == "" || len(endpoint.PasswordFile) == 0 {
		return dolt.Endpoint{}, errors.New("TaskStore endpoint configuration is incomplete")
	}
	password := ""
	if !bytes.Equal(bytes.TrimSpace(endpoint.PasswordFile), []byte("null")) {
		var passwordPath string
		if err := json.Unmarshal(endpoint.PasswordFile, &passwordPath); err != nil || passwordPath == "" {
			return dolt.Endpoint{}, errors.New("TaskStore password file is invalid")
		}
		content, err := privateFile(passwordPath, 16*1024)
		if err != nil {
			return dolt.Endpoint{}, errors.New("TaskStore password file is invalid")
		}
		password = strings.TrimSpace(string(content))
		if password == "" {
			return dolt.Endpoint{}, errors.New("TaskStore password file is empty")
		}
	}
	return dolt.Endpoint{
		Address: endpoint.Address, Database: endpoint.Database,
		User: endpoint.User, Password: password,
	}, nil
}

func readBoardServerConfig(path string) (dolt.Config, error) {
	content, err := privateFile(path, maximumBoardServerConfigBytes)
	if err != nil {
		return dolt.Config{}, errors.New("Board server configuration is invalid")
	}
	canonical, err := jsondocument.CanonicalWithNormalizedNumbersLimit(content, maximumBoardServerConfigBytes)
	if err != nil {
		return dolt.Config{}, errors.New("Board server configuration is invalid")
	}
	decoder := json.NewDecoder(bytes.NewReader(canonical))
	decoder.DisallowUnknownFields()
	var document boardServerConfig
	if err := decoder.Decode(&document); err != nil {
		return dolt.Config{}, errors.New("Board server configuration is invalid")
	}
	if _, err := decoder.Token(); !errors.Is(err, io.EOF) || document.SchemaVersion != 1 || document.StoreID == "" {
		return dolt.Config{}, errors.New("Board server configuration is invalid")
	}
	control, err := endpointConfig(document.Control)
	if err != nil {
		return dolt.Config{}, err
	}
	writer, err := endpointConfig(document.Writer)
	if err != nil {
		return dolt.Config{}, err
	}
	return dolt.Config{Control: control, Writer: writer, StoreID: document.StoreID}, nil
}

func loopbackListenAddress(value string) bool {
	host, port, err := net.SplitHostPort(value)
	if err != nil || port == "" {
		return false
	}
	address := net.ParseIP(host)
	return address != nil && address.IsLoopback()
}

func runBoardServer(arguments []string, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("serve-board", flag.ContinueOnError)
	flags.SetOutput(stderr)
	listenAddress := flags.String("listen", "", "explicit loopback listen address")
	configPath := flags.String("taskstore-config", "", "absolute private TaskStore configuration path")
	if err := flags.Parse(arguments); err != nil || flags.NArg() != 0 || !loopbackListenAddress(*listenAddress) || *configPath == "" {
		fmt.Fprintln(stderr, "usage: director-engine serve-board --listen <loopback:port> --taskstore-config <absolute-path>")
		return 2
	}
	config, err := readBoardServerConfig(*configPath)
	if err != nil {
		fmt.Fprintln(stderr, "director-engine: Board server configuration is invalid")
		return 1
	}
	store, err := dolt.Open(config)
	if err != nil {
		fmt.Fprintln(stderr, "director-engine: TaskStore is unavailable")
		return 1
	}
	defer store.Close()
	startupContext, cancelStartup := context.WithTimeout(context.Background(), 10*time.Second)
	_, schemaError := store.SchemaVersion(startupContext)
	cancelStartup()
	if schemaError != nil {
		fmt.Fprintln(stderr, "director-engine: TaskStore is unavailable")
		return 1
	}
	listener, err := net.Listen("tcp", *listenAddress)
	if err != nil {
		fmt.Fprintln(stderr, "director-engine: Board listener is unavailable")
		return 1
	}
	defer listener.Close()
	current, err := currentIdentity()
	if err != nil {
		fmt.Fprintln(stderr, "director-engine: identity is unavailable")
		return 1
	}
	if err := writeJSON(stdout, struct {
		Event    string   `json:"event"`
		Address  string   `json:"address"`
		Identity identity `json:"identity"`
	}{Event: "director-engine.board-listening", Address: listener.Addr().String(), Identity: current}); err != nil {
		fmt.Fprintln(stderr, "director-engine: Board readiness output failed")
		return 1
	}
	handler := http.NewServeMux()
	handler.Handle(boardQueryPath, newBoardHandler(board.NewReader(store)))
	handler.Handle(planningport.QueryPath, newPlanningHandler(board.NewPlanningReader(store)))
	handler.Handle(planningport.MutationPath, newPlanningMutationHandler(
		store, executionapp.NewController(store, nil, nil, nil),
	))
	server := &http.Server{
		Handler:           handler,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       10 * time.Second,
		WriteTimeout:      10 * time.Second,
		IdleTimeout:       30 * time.Second,
	}
	if err := server.Serve(listener); err != nil && !errors.Is(err, http.ErrServerClosed) {
		fmt.Fprintln(stderr, "director-engine: Board listener stopped")
		return 1
	}
	return 0
}
