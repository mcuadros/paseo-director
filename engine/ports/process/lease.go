// SPDX-License-Identifier: Apache-2.0

// Package process defines the authorized engine boundary for Linux process
// identity and reconciliation observations. Implementations observe facts and
// contain no lease transition policy.
package process

import (
	"context"

	"github.com/mcuadros/director-engine/domain"
)

// ProjectLeaseTakeoverTarget fixes the complete scope an adapter must prove
// before it can return typed absence/reconciliation evidence.
type ProjectLeaseTakeoverTarget struct {
	ProjectID             string
	HolderInstance        string
	HolderProcessIdentity string
	LeaseEpoch            uint64
	PriorHolderInstance   string
	PriorProcessIdentity  string
}

// ProjectLeaseTakeoverObserver returns success only after proving the prior
// engine and all dispatch children absent, the one-daemon identity current,
// and full reconciliation complete. Unproved facts return an error rather
// than caller-selected authority booleans.
type ProjectLeaseTakeoverObserver interface {
	ObserveProjectLeaseTakeover(context.Context, ProjectLeaseTakeoverTarget) (domain.ProjectLeaseObservationInput, error)
}
