// SPDX-License-Identifier: Apache-2.0

// Package reconciliation is the engine-owned port for synchronously waking
// coordinator reconciliation after a daemon terminal callback.
package reconciliation

import (
	"context"
	"errors"

	"github.com/mcuadros/director-engine/domain/execution"
)

// Request is one idempotent enqueue. EventID is the queue deduplication key;
// callers must retain it across a response loss or process restart.
type Request struct {
	EventID          string                        `json:"eventId"`
	EventFactHash    string                        `json:"eventFactHash"`
	RunID            string                        `json:"runId"`
	AgentID          string                        `json:"agentId"`
	DispatchEffectID string                        `json:"dispatchEffectId"`
	Kind             execution.CompletionEventKind `json:"kind"`
	ReceivedAtMillis int64                         `json:"receivedAtMillis"`
	DeadlineAtMillis int64                         `json:"deadlineAtMillis"`
}

// Receipt proves that the queue accepted the exact logical wake. Deduplicated
// means the same EventID was already present; it never means a second wake was
// created.
type Receipt struct {
	EventID          string `json:"eventId"`
	EventFactHash    string `json:"eventFactHash"`
	EnqueuedAtMillis int64  `json:"enqueuedAtMillis"`
	Deduplicated     bool   `json:"deduplicated"`
}

// Queue must provide idempotent enqueue semantics keyed by Request.EventID.
// Enqueue returns only after the coordinator wake is durably visible.
type Queue interface {
	Enqueue(context.Context, Request) (Receipt, error)
}

// ValidateReceipt binds queue evidence and enforces the owner's exclusive
// sub-second dispatch target without reading a process clock in the engine.
func ValidateReceipt(request Request, receipt Receipt) error {
	if request.EventID == "" || request.EventFactHash == "" || request.RunID == "" ||
		request.AgentID == "" || request.DispatchEffectID == "" ||
		request.DeadlineAtMillis-request.ReceivedAtMillis != execution.CompletionDispatchTargetMillis ||
		receipt.EventID != request.EventID || receipt.EventFactHash != request.EventFactHash ||
		receipt.EnqueuedAtMillis < request.ReceivedAtMillis ||
		receipt.EnqueuedAtMillis >= request.DeadlineAtMillis {
		return errors.New("coordinator reconciliation enqueue receipt is invalid or late")
	}
	return nil
}
