// SPDX-License-Identifier: Apache-2.0

package execution

// CompletionEventKind is the closed vocabulary of daemon-reported agent
// lifecycle transitions Director subscribes to. Director never invents a kind
// from model text and never treats a worker-reported liveness ping as one.
type CompletionEventKind string

const (
	CompletionEventFinished   CompletionEventKind = "agent.finished"
	CompletionEventError      CompletionEventKind = "agent.error"
	CompletionEventPermission CompletionEventKind = "agent.permission"
)

// CompletionEvent is one immutable notification that an owned agent changed
// state. It is a wake signal, never evidence: it can shorten the delay before
// the engine re-reads authoritative facts, and it can never stand in for the
// exact Candidate observation, the Claim, or a Review.
//
// The PLAN five-minute multi-source stall predicate stays the only watchdog
// over a quiet worker and is used solely for lost-event recovery.
type CompletionEvent struct {
	ID               string              `json:"id"`
	Kind             CompletionEventKind `json:"kind"`
	AgentID          string              `json:"agentId"`
	DispatchEffectID string              `json:"dispatchEffectId"`
	Cursor           uint64              `json:"cursor"`
	ObservedAtMillis int64               `json:"observedAtMillis"`
	BindingHash      string              `json:"bindingHash"`
	FactHash         string              `json:"factHash"`
}

const (
	NeedCompletionEventInvalid  NeedCode = "completion_event_invalid"
	NeedCompletionEventStale    NeedCode = "completion_event_stale"
	NeedCompletionEventMisbound NeedCode = "completion_event_misbound"
	NeedCompletionDispatchLate  NeedCode = "completion_event_dispatch_late"
	NeedCompletionLedgerFull    NeedCode = "completion_event_ledger_full"
	NeedStallRecoveryInvalid    NeedCode = "stall_recovery_invalid"
)

const (
	// MaximumCompletionEventReceipts bounds one Run's exactly-once callback
	// ledger. Normal Task and correction lifecycles remain far below it; hitting
	// it parks instead of evicting an identity which a delayed duplicate could
	// otherwise enqueue twice.
	MaximumCompletionEventReceipts = 256
	// CompletionDispatchTargetMillis is an exclusive latency ceiling: enqueue
	// must be observed less than one second after the daemon callback arrived.
	CompletionDispatchTargetMillis int64 = 1_000
	// LostCompletionEventRecoveryMillis is the PLAN's sole event-loss fallback.
	LostCompletionEventRecoveryMillis int64 = 5 * 60 * 1_000
)

// CompletionReceiptPhase records the enqueue handoff around an idempotent
// coordinator queue. Intent is durable before the call; complete is durable
// only after the exact queue receipt and sub-second timing are verified.
type CompletionReceiptPhase string

const (
	CompletionReceiptIntent   CompletionReceiptPhase = "intent_recorded"
	CompletionReceiptComplete CompletionReceiptPhase = "complete"
)

// CompletionEventReceipt is the bounded durable deduplication record for one
// daemon callback. Event ID and FactHash make exact replay a no-op and make a
// conflicting reuse fail closed even after later callbacks have arrived.
type CompletionEventReceipt struct {
	EventID               string                 `json:"eventId"`
	EventFactHash         string                 `json:"eventFactHash"`
	Event                 CompletionEvent        `json:"event"`
	Cursor                uint64                 `json:"cursor"`
	Phase                 CompletionReceiptPhase `json:"phase"`
	EnqueuedAtMillis      int64                  `json:"enqueuedAtMillis,omitempty"`
	DispatchLatencyMillis int64                  `json:"dispatchLatencyMillis,omitempty"`
}

// StallRecoveryFacts are the complete multi-source predicate for the only
// permitted lost-event recovery. No timer or single quiet source can
// satisfy it.
type StallRecoveryFacts struct {
	ObservedAtMillis        int64  `json:"observedAtMillis"`
	LastProgressAtMillis    int64  `json:"lastProgressAtMillis"`
	AgentStateUnchanged     bool   `json:"agentStateUnchanged"`
	NoToolActivity          bool   `json:"noToolActivity"`
	NoWorktreeChange        bool   `json:"noWorktreeChange"`
	NoUsageMovement         bool   `json:"noUsageMovement"`
	PendingTerminalEffectID string `json:"pendingTerminalEffectId"`
}

func validCompletionEventKind(kind CompletionEventKind) bool {
	switch kind {
	case CompletionEventFinished, CompletionEventError,
		CompletionEventPermission:
		return true
	default:
		return false
	}
}

// CompletionEventHash binds every notification field except the self-hash.
func CompletionEventHash(event CompletionEvent) string {
	event.FactHash = ""
	return evidenceDigest(event)
}

// ValidCompletionEvent rejects a partial, unknown, or self-inconsistent
// notification before it can wake a Run.
func ValidCompletionEvent(event CompletionEvent) bool {
	return event.ID != "" && validCompletionEventKind(event.Kind) &&
		event.AgentID != "" && event.DispatchEffectID != "" &&
		event.Cursor > 0 && event.ObservedAtMillis >= 0 &&
		event.BindingHash != "" && event.FactHash != "" &&
		event.FactHash == CompletionEventHash(event)
}

// AdmitLostCompletionEventRecovery admits no periodic active-turn check. It accepts
// only the PLAN's five-minute compound stall proof over unchanged agent,
// tool, worktree, and usage sources, bound to one pending terminal effect.
func AdmitLostCompletionEventRecovery(facts StallRecoveryFacts, expectedEffectID string) Admission {
	if facts.ObservedAtMillis < 0 || facts.LastProgressAtMillis < 0 ||
		facts.ObservedAtMillis < facts.LastProgressAtMillis ||
		facts.ObservedAtMillis-facts.LastProgressAtMillis < LostCompletionEventRecoveryMillis ||
		facts.PendingTerminalEffectID == "" || facts.PendingTerminalEffectID != expectedEffectID ||
		!facts.AgentStateUnchanged || !facts.NoToolActivity ||
		!facts.NoWorktreeChange || !facts.NoUsageMovement {
		return park(NeedStallRecoveryInvalid)
	}
	return Admission{Kind: AdmissionAllow, Digest: evidenceDigest(facts)}
}

// AdmitCompletionEvent decides whether one notification may wake this Run. It
// refuses an event bound to another agent or Run and refuses a cursor which
// does not advance, so a replayed notification cannot reset a delay budget.
func AdmitCompletionEvent(
	event CompletionEvent,
	agentID string,
	bindingHash string,
	lastCursor uint64,
	nowMillis int64,
) Admission {
	if !ValidCompletionEvent(event) || event.ObservedAtMillis > nowMillis {
		return park(NeedCompletionEventInvalid)
	}
	if event.AgentID != agentID || event.BindingHash != bindingHash {
		return park(NeedCompletionEventMisbound)
	}
	if nowMillis-event.ObservedAtMillis >= CompletionDispatchTargetMillis {
		return park(NeedCompletionDispatchLate)
	}
	if event.Cursor <= lastCursor {
		return park(NeedCompletionEventStale)
	}
	return Admission{Kind: AdmissionAllow, Digest: event.FactHash}
}
