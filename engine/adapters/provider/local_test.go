// SPDX-License-Identifier: Apache-2.0

package provider

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	domainconfig "github.com/mcuadros/director-engine/domain/configuration"
)

func TestProviderFactBindsTheCurrentProductionCodexModel(t *testing.T) {
	executable := filepath.Join(t.TempDir(), "codex")
	if err := os.WriteFile(executable, []byte("#!/bin/sh\necho codex-cli 0.147.0\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	fact := providerFact(context.Background(), domainconfig.ProviderCodex, executable, "0.147.0", "gpt-5.6-sol", "--version")
	if fact.State != "ready" || len(fact.Models) != 1 || fact.Models[0].Model != "gpt-5.6-sol" || len(fact.Models[0].Variants) != 4 {
		t.Fatalf("production Codex fact = %#v", fact)
	}
	for _, variant := range fact.Models[0].Variants {
		if variant.Mode != "auto-review" {
			t.Fatalf("production Codex mode = %q", variant.Mode)
		}
	}
}
