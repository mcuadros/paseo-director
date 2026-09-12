// SPDX-License-Identifier: Apache-2.0

package safedata

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/mcuadros/director-engine/internal/testkit/secretfixture"
)

func TestCredentialCanaryMatrixRejectsValuesKeysAndEscapedJSON(t *testing.T) {
	for _, canary := range secretfixture.ProviderCanaries() {
		if classification := ClassifyText(canary, false); classification != Secret {
			t.Errorf("canary classification = %s", classification)
		}
		raw, err := json.Marshal(map[string]any{"detail": canary})
		if err != nil {
			t.Fatal(err)
		}
		if classification := ClassifyJSON(raw, ScanRules{MaximumBytes: 4096, RejectSensitiveKeys: true}); classification != Secret {
			t.Errorf("JSON canary classification = %s", classification)
		}
	}
	for _, key := range []string{"password", "api_token", "client-secret", "AUTHORIZATION", "privateKey"} {
		raw, err := json.Marshal(map[string]string{key: "opaque-value"})
		if err != nil {
			t.Fatal(err)
		}
		if classification := ClassifyJSON(raw, ScanRules{MaximumBytes: 4096, RejectSensitiveKeys: true}); classification != Secret {
			t.Errorf("sensitive key %q classification = %s", key, classification)
		}
	}
}

func TestPrivatePathMutationMatrixRejectsTraversalAliases(t *testing.T) {
	paths := []string{
		"/home/owner/private", "/tmp/private", "failure at /Users/owner/private", "failure at /srv/director/private",
		`C:\\Users\\owner\\private`,
		`\\\\server\\share\\private`, "file:///var/lib/private", "~/.ssh/id_ed25519",
		"evidence/../../private", "evidence\\..\\private", "%2e%2e%2fprivate", "%2Fhome%2Fowner%2Fprivate",
		"failure%20at%20%2FUsers%2Fowner%2Fprivate",
	}
	for _, path := range paths {
		if classification := ClassifyText(path, true); classification != PrivatePath {
			t.Errorf("path %q classification = %s", path, classification)
		}
	}
	for _, safe := range []string{"docs/PLAN.md:10", "https://example.invalid/repo", "feature/path", "credential_required"} {
		if classification := ClassifyText(safe, true); classification != Safe {
			t.Errorf("safe value %q classification = %s", safe, classification)
		}
	}
}

func TestRedactionNeverReturnsCredentialOrPrivatePathBytes(t *testing.T) {
	canary := secretfixture.GitHubFineGrained()
	path := "/tmp/director/private-evidence"
	redacted, classification := Redact("inspect " + path + " with token=" + canary)
	if classification != Secret || strings.Contains(redacted, canary) || strings.Contains(redacted, path) ||
		!strings.Contains(redacted, "[REDACTED_SECRET]") || !strings.Contains(redacted, "[REDACTED_PATH]") {
		t.Fatalf("redaction = %q, %s", redacted, classification)
	}
}

func TestJSONScanIsBoundedAndFailsClosed(t *testing.T) {
	raw := []byte(`{"detail":"safe"}`)
	if classification := ClassifyJSON(raw, ScanRules{MaximumBytes: len(raw) - 1}); classification != Invalid {
		t.Fatalf("oversize classification = %s", classification)
	}
	if classification := ClassifyJSON([]byte(`{"detail":"safe"} {}`), ScanRules{MaximumBytes: 64}); classification != Invalid {
		t.Fatalf("trailing classification = %s", classification)
	}
}

func TestRecursiveClassificationIsDeterministicAndSecretDominatesPaths(t *testing.T) {
	value := map[string]any{
		"private": "/home/owner/private",
		"detail":  secretfixture.GitHubFineGrained(),
	}
	for range 1_000 {
		if classification := ClassifyValue(value, ScanRules{RejectPrivatePaths: true}); classification != Secret {
			t.Fatalf("mixed classification = %s", classification)
		}
	}
}
