// SPDX-License-Identifier: Apache-2.0

package planningtestkit_test

import (
	"testing"

	kit "github.com/mcuadros/director-engine/internal/planningtestkit"
)

// This external-package test proves dir-m2.3 tests can consume the generator,
// oracle, explanation codes, and minimizer without importing or copying
// production planning logic into the kit.
func TestPublicTestConsumerSurface(t *testing.T) {
	project, err := kit.GenerateCase(12, kit.DefaultBounds(), kit.VariantDependencyCycle)
	if err != nil {
		t.Fatal(err)
	}
	if report := kit.Evaluate(project); !report.HasCode(kit.CodeDependencyCycle) {
		t.Fatalf("generated cycle report = %#v", report)
	}
	minimum := kit.Minimize(project, func(candidate kit.Project) bool {
		return kit.Evaluate(candidate).HasCode(kit.CodeDependencyCycle)
	})
	if kit.Complexity(minimum) >= kit.Complexity(project) {
		t.Fatalf("consumer minimization did not shrink: %d >= %d", kit.Complexity(minimum), kit.Complexity(project))
	}
}
