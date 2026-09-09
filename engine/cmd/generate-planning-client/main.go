// SPDX-License-Identifier: Apache-2.0

// Command generate-planning-client produces the TypeScript/Zod client from the
// engine-owned, transport-neutral planning presentation contract.
package main

import (
	_ "embed"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"text/template"

	"github.com/mcuadros/director-engine/ports/planning"
)

//go:embed planning-contract.ts.tmpl
var clientTemplate string

type templateData struct {
	planning.Definition
	ContractHash string
}

func render(schema []byte) ([]byte, error) {
	definition, err := planning.ParseDefinition(schema)
	if err != nil {
		return nil, err
	}
	contractHash, err := planning.CanonicalSHA256(schema)
	if err != nil {
		return nil, err
	}
	parsed, err := template.New("planning-client").Funcs(template.FuncMap{
		"quote": strconv.Quote,
	}).Parse(clientTemplate)
	if err != nil {
		return nil, fmt.Errorf("parse planning client template: %w", err)
	}
	var output strings.Builder
	if err := parsed.Execute(&output, templateData{Definition: definition, ContractHash: contractHash}); err != nil {
		return nil, fmt.Errorf("render planning client: %w", err)
	}
	return []byte(output.String()), nil
}

func run(arguments []string) error {
	flags := flag.NewFlagSet("generate-planning-client", flag.ContinueOnError)
	schemaPath := flags.String("schema", "ports/planning/planning-surface.v1.json", "engine-owned planning schema path")
	outputPath := flags.String("output", "../generated/planning-contract.shared.ts", "generated TypeScript path")
	if err := flags.Parse(arguments); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return fmt.Errorf("unexpected arguments: %s", strings.Join(flags.Args(), " "))
	}
	schema, err := os.ReadFile(*schemaPath)
	if err != nil {
		return fmt.Errorf("read planning schema: %w", err)
	}
	generated, err := render(schema)
	if err != nil {
		return fmt.Errorf("generate planning client: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(*outputPath), 0o755); err != nil {
		return fmt.Errorf("create output directory: %w", err)
	}
	if err := os.WriteFile(*outputPath, generated, 0o644); err != nil {
		return fmt.Errorf("write planning client: %w", err)
	}
	return nil
}

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintf(os.Stderr, "generate-planning-client: %v\n", err)
		os.Exit(1)
	}
}
