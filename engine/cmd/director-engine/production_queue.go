// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"errors"
	"sync"
	"time"

	reconciliationport "github.com/mcuadros/director-engine/ports/reconciliation"
)

// WakeQueue makes a terminal callback synchronously visible to the production
// reconciler. The callback intent and receipt remain durable in the Run; this
// process-local queue is only the low-latency wake transport.
type WakeQueue struct {
	mu    sync.Mutex
	seen  map[string]string
	wakes chan reconciliationport.Request
	runs  chan string
	now   func() int64
}

func NewWakeQueue(capacity int) *WakeQueue {
	if capacity < 1 {
		capacity = 256
	}
	return &WakeQueue{seen: map[string]string{}, wakes: make(chan reconciliationport.Request, capacity), runs: make(chan string, capacity),
		now: func() int64 { return time.Now().UnixMilli() }}
}

// NotifyRun is a process-local low-latency hint after a Run is already
// durable. It grants no effect authority; the consumer rereads TaskStore and
// the periodic watchdog remains the recovery path if this bounded channel is
// full or the process is interrupted before notification.
func (queue *WakeQueue) NotifyRun(runID string) {
	if runID == "" {
		return
	}
	select {
	case queue.runs <- runID:
	default:
	}
}

func (queue *WakeQueue) Enqueue(ctx context.Context, request reconciliationport.Request) (reconciliationport.Receipt, error) {
	queue.mu.Lock()
	if hash, exists := queue.seen[request.EventID]; exists {
		queue.mu.Unlock()
		if hash != request.EventFactHash {
			return reconciliationport.Receipt{}, errors.New("terminal event identity conflicts")
		}
		return reconciliationport.Receipt{EventID: request.EventID, EventFactHash: request.EventFactHash,
			EnqueuedAtMillis: request.ReceivedAtMillis, Deduplicated: true}, nil
	}
	now := queue.now()
	if now < request.ReceivedAtMillis {
		now = request.ReceivedAtMillis
	}
	if now >= request.DeadlineAtMillis {
		queue.mu.Unlock()
		return reconciliationport.Receipt{}, errors.New("terminal event wake missed its deadline")
	}
	select {
	case queue.wakes <- request:
		queue.seen[request.EventID] = request.EventFactHash
		queue.mu.Unlock()
		return reconciliationport.Receipt{EventID: request.EventID, EventFactHash: request.EventFactHash,
			EnqueuedAtMillis: now}, nil
	case <-ctx.Done():
		queue.mu.Unlock()
		return reconciliationport.Receipt{}, ctx.Err()
	default:
		queue.mu.Unlock()
		return reconciliationport.Receipt{}, errors.New("terminal event wake queue is full")
	}
}

func (queue *WakeQueue) Wakes() <-chan reconciliationport.Request { return queue.wakes }
func (queue *WakeQueue) Runs() <-chan string                      { return queue.runs }
