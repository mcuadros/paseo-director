// SPDX-License-Identifier: Apache-2.0

package execution

import "testing"

func completionEvent() CompletionEvent {
	event := CompletionEvent{
		ID: "event-1", Kind: CompletionEventFinished, AgentID: "agent-1",
		DispatchEffectID: "effect-1", Cursor: 7, ObservedAtMillis: 1_000, BindingHash: "binding-1",
	}
	event.FactHash = CompletionEventHash(event)
	return event
}

func TestAdmitCompletionEventWakesTheBoundRunExactlyOnce(t *testing.T) {
	event := completionEvent()
	admission := AdmitCompletionEvent(event, "agent-1", "binding-1", 6, 1_001)
	if admission.Kind != AdmissionAllow || admission.Digest != event.FactHash ||
		admission.CleanupAuthorized {
		t.Fatalf("admission = %#v", admission)
	}

	// Replaying the same notification must not wake the Run again: a resettable
	// wake signal would let a chatty or hostile source hold a stalled Run open
	// past the multi-source stall predicate.
	if replay := AdmitCompletionEvent(event, "agent-1", "binding-1", 7, 1_001); replay.Kind != AdmissionPark ||
		replay.Code != NeedCompletionEventStale {
		t.Fatalf("replayed admission = %#v", replay)
	}
	reordered := event
	reordered.ID = "event-older"
	reordered.Cursor = 6
	reordered.FactHash = CompletionEventHash(reordered)
	if admission := AdmitCompletionEvent(reordered, "agent-1", "binding-1", 7, 1_001); admission.Kind != AdmissionPark ||
		admission.Code != NeedCompletionEventStale {
		t.Fatalf("reordered admission = %#v", admission)
	}
}

func TestAdmitCompletionEventRefusesMisboundForgedAndUnknownNotifications(t *testing.T) {
	for name, expectation := range map[string]struct {
		mutate func(*CompletionEvent)
		agent  string
		code   NeedCode
	}{
		"other agent":    {func(*CompletionEvent) {}, "agent-2", NeedCompletionEventMisbound},
		"unknown kind":   {func(value *CompletionEvent) { value.Kind = "agent.progress" }, "agent-1", NeedCompletionEventInvalid},
		"no identity":    {func(value *CompletionEvent) { value.ID = "" }, "agent-1", NeedCompletionEventInvalid},
		"no cursor":      {func(value *CompletionEvent) { value.Cursor = 0 }, "agent-1", NeedCompletionEventInvalid},
		"unhashed":       {func(value *CompletionEvent) { value.FactHash = "" }, "agent-1", NeedCompletionEventInvalid},
		"tampered field": {func(value *CompletionEvent) { value.Kind = CompletionEventError }, "agent-1", NeedCompletionEventInvalid},
	} {
		t.Run(name, func(t *testing.T) {
			event := completionEvent()
			expectation.mutate(&event)
			if name == "unknown kind" || name == "no identity" || name == "no cursor" {
				event.FactHash = CompletionEventHash(event)
			}
			admission := AdmitCompletionEvent(event, expectation.agent, "binding-1", 6, 1_001)
			if admission.Kind != AdmissionPark || admission.Code != expectation.code {
				t.Fatalf("admission = %#v", admission)
			}
		})
	}

	event := completionEvent()
	if admission := AdmitCompletionEvent(event, "agent-1", "binding-2", 6, 1_001); admission.Kind != AdmissionPark ||
		admission.Code != NeedCompletionEventMisbound {
		t.Fatalf("other-Run admission = %#v", admission)
	}
	if admission := AdmitCompletionEvent(event, "agent-1", "binding-1", 6, 999); admission.Kind != AdmissionPark ||
		admission.Code != NeedCompletionEventInvalid {
		t.Fatalf("future admission = %#v", admission)
	}
	if admission := AdmitCompletionEvent(event, "agent-1", "binding-1", 6, 1_999); admission.Kind != AdmissionAllow {
		t.Fatalf("999 ms admission = %#v", admission)
	}
	if admission := AdmitCompletionEvent(event, "agent-1", "binding-1", 6, 2_000); admission.Kind != AdmissionPark ||
		admission.Code != NeedCompletionDispatchLate {
		t.Fatalf("one-second admission = %#v", admission)
	}
}

func TestCompletionEventVocabularyIsTerminalOnly(t *testing.T) {
	for _, kind := range []CompletionEventKind{
		CompletionEventFinished, CompletionEventError, CompletionEventPermission,
	} {
		event := completionEvent()
		event.Kind = kind
		event.FactHash = CompletionEventHash(event)
		if !ValidCompletionEvent(event) {
			t.Fatalf("kind %q was rejected", kind)
		}
	}
	for _, kind := range []CompletionEventKind{"agent.progress", "agent.alive", ""} {
		event := completionEvent()
		event.Kind = kind
		event.FactHash = CompletionEventHash(event)
		if ValidCompletionEvent(event) {
			t.Fatalf("kind %q was admitted as a completion event", kind)
		}
	}
}

func TestLostEventRecoveryRequiresEveryStallSourceAtFiveMinutes(t *testing.T) {
	facts := StallRecoveryFacts{
		ObservedAtMillis: 301_000, LastProgressAtMillis: 1_000,
		AgentStateUnchanged: true, NoToolActivity: true, NoWorktreeChange: true,
		NoUsageMovement: true, PendingTerminalEffectID: "effect-1",
	}
	if admission := AdmitLostCompletionEventRecovery(facts, "effect-1"); admission.Kind != AdmissionAllow {
		t.Fatalf("complete lost-event proof = %#v", admission)
	}
	for name, mutate := range map[string]func(*StallRecoveryFacts){
		"too early":        func(value *StallRecoveryFacts) { value.ObservedAtMillis-- },
		"agent changed":    func(value *StallRecoveryFacts) { value.AgentStateUnchanged = false },
		"tool active":      func(value *StallRecoveryFacts) { value.NoToolActivity = false },
		"worktree changed": func(value *StallRecoveryFacts) { value.NoWorktreeChange = false },
		"usage moved":      func(value *StallRecoveryFacts) { value.NoUsageMovement = false },
		"wrong effect":     func(value *StallRecoveryFacts) { value.PendingTerminalEffectID = "other" },
	} {
		t.Run(name, func(t *testing.T) {
			changed := facts
			mutate(&changed)
			if admission := AdmitLostCompletionEventRecovery(changed, "effect-1"); admission.Kind != AdmissionPark {
				t.Fatalf("partial proof = %#v", admission)
			}
		})
	}
}
