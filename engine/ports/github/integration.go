// SPDX-License-Identifier: Apache-2.0

package github

import (
	"context"

	domainintegration "github.com/mcuadros/director-engine/domain/integration"
)

// IntegrationRequest fixes the one repository, pull request, Candidate, and
// TaskStore time which a forge observation may describe.
type IntegrationRequest struct {
	Binding            domainintegration.Binding `json:"binding"`
	TaskStoreNowMillis int64                     `json:"taskStoreNowMillis"`
}

// MergeRequest carries the exact one-use precondition. The adapter may invoke
// only a merge primitive which atomically rejects a different head SHA.
type MergeRequest struct {
	Binding             domainintegration.Binding          `json:"binding"`
	Attempt             uint32                             `json:"attempt"`
	ExpectedObservation domainintegration.ForgeObservation `json:"expectedObservation"`
	TaskStoreNowMillis  int64                              `json:"taskStoreNowMillis"`
}

// MergeResult classifies the transport handoff only. It is never integration
// evidence; the engine must reobserve GitHub and the remote Git graph.
type MergeResult struct {
	Handoff bool                   `json:"handoff"`
	Code    domainintegration.Code `json:"code"`
}

type IntegrationPort interface {
	ObserveIntegration(context.Context, IntegrationRequest) (domainintegration.ForgeObservation, error)
	MergeExpectedHead(context.Context, MergeRequest) (MergeResult, error)
}
