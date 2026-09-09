// SPDX-License-Identifier: Apache-2.0

package reconciliation

import (
	"testing"

	"github.com/mcuadros/director-engine/domain/execution"
)

func TestValidateReceiptRequiresExactBindingAndSubsecondDispatch(t *testing.T) {
	request := Request{
		EventID: "event-1", EventFactHash: "fact-1", RunID: "run-1",
		AgentID: "agent-1", DispatchEffectID: "effect-1",
		Kind: execution.CompletionEventFinished, ReceivedAtMillis: 2_000,
		DeadlineAtMillis: 3_000,
	}
	valid := Receipt{EventID: "event-1", EventFactHash: "fact-1", EnqueuedAtMillis: 2_999}
	if err := ValidateReceipt(request, valid); err != nil {
		t.Fatalf("999 ms receipt: %v", err)
	}
	for name, mutate := range map[string]func(*Receipt){
		"one second":   func(value *Receipt) { value.EnqueuedAtMillis = 3_000 },
		"wrong event":  func(value *Receipt) { value.EventID = "other" },
		"wrong fact":   func(value *Receipt) { value.EventFactHash = "other" },
		"before event": func(value *Receipt) { value.EnqueuedAtMillis = 1_999 },
	} {
		t.Run(name, func(t *testing.T) {
			changed := valid
			mutate(&changed)
			if err := ValidateReceipt(request, changed); err == nil {
				t.Fatal("ValidateReceipt accepted invalid queue evidence")
			}
		})
	}
}
