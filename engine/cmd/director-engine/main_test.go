// SPDX-License-Identifier: Apache-2.0

package main

import (
	"bytes"
	"encoding/json"
	"testing"
)

func TestVersionIdentity(t *testing.T) {
	var stdout, stderr bytes.Buffer
	if status := run([]string{"version"}, &stdout, &stderr); status != 0 {
		t.Fatalf("run(version) status = %d, stderr = %q", status, stderr.String())
	}
	var result identity
	if err := json.Unmarshal(stdout.Bytes(), &result); err != nil {
		t.Fatalf("decode version: %v", err)
	}
	if result.Name != "director-engine" || result.Version == "" {
		t.Fatalf("identity = %#v", result)
	}
	if !result.ProductBehavior {
		t.Fatal("version does not report Board/List product behavior")
	}
}

func TestSmokeAndUsage(t *testing.T) {
	var stdout, stderr bytes.Buffer
	if status := run([]string{"smoke"}, &stdout, &stderr); status != 0 {
		t.Fatalf("run(smoke) status = %d, stderr = %q", status, stderr.String())
	}
	var result struct {
		Event           string `json:"event"`
		ProductBehavior bool   `json:"productBehavior"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &result); err != nil {
		t.Fatalf("decode smoke: %v", err)
	}
	if result.Event != "director-engine.smoke-ready" || !result.ProductBehavior {
		t.Fatalf("smoke result = %#v", result)
	}

	stdout.Reset()
	stderr.Reset()
	if status := run(nil, &stdout, &stderr); status != 2 {
		t.Fatalf("run(nil) status = %d", status)
	}
}
