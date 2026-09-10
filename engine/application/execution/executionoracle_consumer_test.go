// SPDX-License-Identifier: Apache-2.0

package execution_test

import (
	"testing"

	oracle "github.com/mcuadros/director-engine/internal/testkit/executionoracle"
)

func TestExecutionOracleIsConsumableOnlyAsTestAuthority(t *testing.T) {
	decision := oracle.Evaluate(oracle.Baseline())
	if len(decision.Actions) != 1 || decision.Actions[0].Kind != oracle.ActionCreateWorkspace {
		t.Fatalf("baseline oracle decision = %#v", decision)
	}
}
