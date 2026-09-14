// SPDX-License-Identifier: Apache-2.0

// Package execution defines the typed application boundary for Director
// execution-control commands. Implementations remain in application/execution.
package execution

import (
	"github.com/mcuadros/director-engine/domain"
	domainexecution "github.com/mcuadros/director-engine/domain/execution"
)

// AuthenticatedControlActor is supplied by a trusted server ingress rather
// than decoded from a model-controlled planning mutation.
type AuthenticatedControlActor struct {
	Kind          domainexecution.ControlActorKind
	ID            string
	SessionID     string
	Source        string
	Authenticated bool
}

type ControlCommand struct {
	Kind                   domainexecution.ControlKind
	RequestID              string
	ProjectID              string
	TaskID                 string
	RunID                  string
	ExpectedProjectVersion uint64
	ExpectedTaskVersion    uint64
	ExpectedRunVersion     uint64
	Lease                  domainexecution.LeaseBinding
	Actor                  AuthenticatedControlActor
	NowMillis              int64
	ConfirmationID         string
}

type ControlResult struct {
	Project        domain.Project
	Run            *domain.Run
	Command        domain.CommandResult
	ConfirmationID string
	Replay         bool
}
