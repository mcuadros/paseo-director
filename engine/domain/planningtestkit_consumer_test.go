// SPDX-License-Identifier: Apache-2.0

package domain

import (
	"testing"

	planningtestkit "github.com/mcuadros/director-engine/internal/planningtestkit"
)

func TestPlanningDomainTestsCanConsumeIndependentPlanningOracle(t *testing.T) {
	project, err := planningtestkit.Generate(42, planningtestkit.DefaultBounds())
	if err != nil {
		t.Fatalf("generate planning facts: %v", err)
	}
	if report := planningtestkit.Evaluate(project); !report.Valid {
		t.Fatalf("generated planning facts are invalid: %#v", report.Findings)
	}
}
