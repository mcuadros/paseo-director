// SPDX-License-Identifier: Apache-2.0

// Command director-engine is the standalone Director Engine executable.
package main

import (
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
)

type identity struct {
	Name            string `json:"name"`
	Version         string `json:"version"`
	BuildMode       string `json:"buildMode"`
	SourceCandidate string `json:"sourceCandidate"`
	Target          string `json:"target"`
	ContractVersion string `json:"contractVersion"`
	ContractSHA256  string `json:"contractSha256"`
	ProductBehavior bool   `json:"productBehavior"`
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
	return identity{
		Name:            "director-engine",
		Version:         version,
		BuildMode:       buildMode,
		SourceCandidate: sourceCandidate,
		Target:          runtime.GOOS + "-" + runtime.GOARCH,
		ContractVersion: definition.ContractVersion,
		ContractSHA256:  contractHash,
		ProductBehavior: false,
	}, nil
}

func writeJSON(writer io.Writer, value any) error {
	encoder := json.NewEncoder(writer)
	encoder.SetEscapeHTML(false)
	return encoder.Encode(value)
}

func run(arguments []string, stdout, stderr io.Writer) int {
	if len(arguments) != 1 || (arguments[0] != "version" && arguments[0] != "smoke") {
		fmt.Fprintln(stderr, "usage: director-engine <version|smoke>")
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
		ProductBehavior: false,
	}); err != nil {
		fmt.Fprintf(stderr, "director-engine: write smoke result: %v\n", err)
		return 1
	}
	return 0
}

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}
