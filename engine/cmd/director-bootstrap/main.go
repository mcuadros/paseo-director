// SPDX-License-Identifier: Apache-2.0

// Command director-bootstrap is the portable Director runtime controller.
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"runtime"
)

type bootstrapIdentity struct {
	Name            string `json:"name"`
	Version         string `json:"version"`
	BuildMode       string `json:"buildMode"`
	SourceCandidate string `json:"sourceCandidate"`
	Target          string `json:"target"`
}

func exactFlags(name string, arguments []string) (string, string, string, error) {
	flags := flag.NewFlagSet(name, flag.ContinueOnError)
	flags.SetOutput(os.Stderr)
	candidate := flags.String("candidate", "", "exact source Candidate")
	bootstrapSHA := flags.String("bootstrap-sha256", "", "exact bootstrap executable digest")
	hostSocket := flags.String("host-socket", "", "exact local host callback socket")
	if err := flags.Parse(arguments); err != nil || flags.NArg() != 0 {
		return "", "", "", fail("DIRECTOR_BOOTSTRAP_ARGUMENT")
	}
	return *candidate, *bootstrapSHA, *hostSocket, nil
}

func writeJSON(value any) error {
	encoder := json.NewEncoder(os.Stdout)
	encoder.SetEscapeHTML(false)
	return encoder.Encode(value)
}

func main() {
	if len(os.Args) == 2 && os.Args[1] == "version" {
		if err := writeJSON(bootstrapIdentity{Name: "director-bootstrap", Version: bootstrapVersion, BuildMode: bootstrapMode,
			SourceCandidate: bootstrapCandidate, Target: runtime.GOOS + "-" + runtime.GOARCH}); err != nil {
			os.Exit(1)
		}
		return
	}
	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, "DIRECTOR_BOOTSTRAP_ARGUMENT")
		os.Exit(2)
	}
	command := os.Args[1]
	candidate, bootstrapSHA, hostSocket, err := exactFlags(command, os.Args[2:])
	if err != nil {
		fmt.Fprintln(os.Stderr, codeOf(err, "DIRECTOR_BOOTSTRAP_ARGUMENT"))
		os.Exit(2)
	}
	switch command {
	case "prepare":
		if hostSocket != "" {
			err = fail("DIRECTOR_BOOTSTRAP_ARGUMENT")
		} else {
			var result prepareResult
			result, err = prepare(candidate, bootstrapSHA)
			if err == nil {
				err = writeJSON(result)
			}
		}
	case "ensure":
		if hostSocket == "" {
			err = fail("DIRECTOR_BOOTSTRAP_ARGUMENT")
		} else {
			var status runtimeStatus
			status, err = ensureRuntime(candidate, bootstrapSHA, hostSocket)
			if err == nil {
				err = writeJSON(status)
			}
		}
	case "runtime":
		if hostSocket == "" {
			err = fail("DIRECTOR_BOOTSTRAP_ARGUMENT")
		} else {
			err = runRuntime(candidate, bootstrapSHA, hostSocket)
			if err != nil {
				writeRuntimeFailure(err)
			}
		}
	default:
		err = fail("DIRECTOR_BOOTSTRAP_ARGUMENT")
	}
	if err != nil {
		// Three channels, one refusal: the code alone on its own line for the
		// pinned launcher, the closed occupied-listener document the launcher
		// forwards to the operator's plugin log, and the sentence a reader of a
		// direct invocation acts on.
		code := codeOf(err, "DIRECTOR_BOOTSTRAP_FAILED")
		fmt.Fprintln(os.Stderr, code)
		if port, holder, pid := occupiedPortOf(err); port > 0 {
			if document, marshalErr := json.Marshal(runtimeFailure{SchemaVersion: 1, Code: code, Port: port, Holder: holder, PID: pid}); marshalErr == nil {
				fmt.Fprintln(os.Stderr, string(document))
			}
			fmt.Fprintln(os.Stderr, occupiedPortMessage(port, holder, pid))
		}
		os.Exit(1)
	}
}
