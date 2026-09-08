// SPDX-License-Identifier: Apache-2.0

package main

import (
	"bytes"
	"testing"
)

func TestRenderIsDeterministicAndCarriesContract(t *testing.T) {
	schema := []byte("{\n  \"schemaVersion\": 1,\n  \"contractVersion\": \"test/v1\",\n  \"credentialScope\": \"scope\",\n  \"capabilities\": [\"one\", \"two\"]\n}\n")
	first, err := render(schema)
	if err != nil {
		t.Fatal(err)
	}
	second, err := render(schema)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(first, second) {
		t.Fatal("render output is not deterministic")
	}
	for _, expected := range [][]byte{
		[]byte(`HOST_CONTRACT_VERSION = "test/v1"`),
		[]byte(`"one"`),
		[]byte(`"two"`),
		[]byte(`HOST_CONTRACT_SHA256`),
		[]byte(`interface HostCommandArguments`),
		[]byte(`interface HostObservationResult`),
	} {
		if !bytes.Contains(first, expected) {
			t.Fatalf("render output does not contain %q", expected)
		}
	}
	if bytes.Contains(first, []byte(`arguments: Readonly<Record<string, unknown>>`)) || bytes.Contains(first, []byte(`result: unknown`)) {
		t.Fatal("generated host client retained an untyped command or observation boundary")
	}
}

func TestRenderIgnoresSchemaWhitespaceAndObjectKeyOrder(t *testing.T) {
	first := []byte(`{"schemaVersion":1,"contractVersion":"test/v1","credentialScope":"scope","capabilities":["one","two"]}`)
	second := []byte(`{
		"capabilities": ["one", "two"],
		"credentialScope": "scope",
		"contractVersion": "test/v1",
		"schemaVersion": 1
	}`)
	firstOutput, err := render(first)
	if err != nil {
		t.Fatal(err)
	}
	secondOutput, err := render(second)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(firstOutput, secondOutput) {
		t.Fatal("generated client changed for equivalent schema formatting")
	}
}

func TestRenderRejectsDuplicateSchemaKeys(t *testing.T) {
	schema := []byte(`{"schemaVersion":1,"schemaVersion":1,"contractVersion":"test/v1","credentialScope":"scope","capabilities":["one"]}`)
	if _, err := render(schema); err == nil {
		t.Fatal("render() accepted duplicate schema keys")
	}
}
