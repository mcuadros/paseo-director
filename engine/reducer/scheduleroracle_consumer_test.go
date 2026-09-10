// SPDX-License-Identifier: Apache-2.0

package reducer

import (
	"testing"

	oracle "github.com/mcuadros/director-engine/internal/testkit/scheduleroracle"
)

func TestSchedulerReducerTestsCanConsumeIndependentOracle(t *testing.T) {
	task := oracle.NewTask("task-1", "workspace-1", 1).
		WithPriority(oracle.PriorityUrgent)
	result := oracle.Evaluate(oracle.NewSnapshot(
		oracle.PolicyAutomatic,
		oracle.DefaultLimits(),
		oracle.NewUsage(0, 0, 0, 0),
		task,
	))
	if len(result.OrderedTaskIDs) != 1 || result.OrderedTaskIDs[0] != "task-1" {
		t.Fatalf("scheduler oracle result = %#v", result)
	}
}
