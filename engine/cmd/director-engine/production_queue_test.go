// SPDX-License-Identifier: Apache-2.0

package main

import "testing"

func TestRunNotificationIsBoundedAndCarriesOnlyTheDurableIdentity(t *testing.T) {
	queue := NewWakeQueue(1)
	queue.NotifyRun("")
	select {
	case <-queue.Runs():
		t.Fatal("empty Run identity was notified")
	default:
	}
	queue.NotifyRun("run-1")
	queue.NotifyRun("run-2")
	if runID := <-queue.Runs(); runID != "run-1" {
		t.Fatalf("notified Run = %q", runID)
	}
	select {
	case runID := <-queue.Runs():
		t.Fatalf("full bounded queue unexpectedly retained %q", runID)
	default:
	}
}
