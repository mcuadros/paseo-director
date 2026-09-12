// SPDX-License-Identifier: Apache-2.0

// Command director-engine is the standalone Director Engine executable.
package main

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"runtime"

	"github.com/mcuadros/director-engine/ports/host"
)

var (
	version         = "0.0.0-dev"
	buildMode       = "development"
	sourceCandidate = "uncommitted"
	noticesSha      = "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855"
)

type identity struct {
	Name             string `json:"name"`
	Version          string `json:"version"`
	BuildMode        string `json:"buildMode"`
	SourceCandidate  string `json:"sourceCandidate"`
	Target           string `json:"target"`
	ExecutableSHA256 string `json:"executableSha256"`
	NoticesSHA256    string `json:"noticesSha256"`
	ContractVersion  string `json:"contractVersion"`
	ContractSHA256   string `json:"contractSha256"`
	ProductBehavior  bool   `json:"productBehavior"`
}

func currentIdentity() (identity, error) {
	definition, err := host.EmbeddedDefinition()
	if err != nil {
		return identity{}, err
	}
	contractHash, err := host.SchemaSHA256()
	if err != nil {
		return identity{}, err
	}
	executable, err := os.Executable()
	if err != nil {
		return identity{}, err
	}
	executableBytes, err := os.ReadFile(executable)
	if err != nil {
		return identity{}, err
	}
	executableDigest := sha256.Sum256(executableBytes)
	return identity{
		Name:             "director-engine",
		Version:          version,
		BuildMode:        buildMode,
		SourceCandidate:  sourceCandidate,
		Target:           runtime.GOOS + "-" + runtime.GOARCH,
		ExecutableSHA256: fmt.Sprintf("%x", executableDigest),
		NoticesSHA256:    noticesSha,
		ContractVersion:  definition.ContractVersion,
		ContractSHA256:   contractHash,
		ProductBehavior:  true,
	}, nil
}

func writeJSON(writer io.Writer, value any) error {
	encoder := json.NewEncoder(writer)
	encoder.SetEscapeHTML(false)
	return encoder.Encode(value)
}

func run(arguments []string, stdout, stderr io.Writer) int {
	if len(arguments) != 1 || (arguments[0] != "version" && arguments[0] != "smoke") {
		fmt.Fprintln(stderr, "usage: director-engine <version|smoke|serve-board>")
		return 2
	}
	current, err := currentIdentity()
	if err != nil {
		fmt.Fprintf(stderr, "director-engine: %v\n", err)
		return 1
	}
	if arguments[0] == "version" {
		if err := writeJSON(stdout, current); err != nil {
			fmt.Fprintf(stderr, "director-engine: write version: %v\n", err)
			return 1
		}
		return 0
	}
	if err := writeJSON(stdout, struct {
		Event           string   `json:"event"`
		Identity        identity `json:"identity"`
		ProductBehavior bool     `json:"productBehavior"`
	}{
		Event:           "director-engine.smoke-ready",
		Identity:        current,
		ProductBehavior: true,
	}); err != nil {
		fmt.Fprintf(stderr, "director-engine: write smoke result: %v\n", err)
		return 1
	}
	return 0
}

func main() {
	if len(os.Args) > 1 && os.Args[1] == "serve-board" {
		os.Exit(runBoardServer(os.Args[2:], os.Stdout, os.Stderr))
	}
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}
