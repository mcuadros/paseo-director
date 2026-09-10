// SPDX-License-Identifier: Apache-2.0

// Package review defines thin exact-fact ports for Reviewer resources. The
// engine owns policy and sequencing; adapters only observe or perform one
// already-authorized checkout operation.
package review

import (
	"context"

	domainreview "github.com/mcuadros/director-engine/domain/review"
)

type CheckoutStatus string

const (
	CheckoutAbsent    CheckoutStatus = "absent"
	CheckoutExact     CheckoutStatus = "exact"
	CheckoutDifferent CheckoutStatus = "different"
	CheckoutAmbiguous CheckoutStatus = "ambiguous"
)

type CheckoutRequest struct {
	ReviewKey      string `json:"reviewKey"`
	BindingSHA256  string `json:"bindingSha256"`
	SourcePath     string `json:"sourcePath"`
	PrimaryPath    string `json:"primaryPath"`
	ReviewerRoot   string `json:"reviewerRoot"`
	CheckoutPath   string `json:"checkoutPath"`
	CandidateSHA   string `json:"candidateSha"`
	TreeSHA        string `json:"treeSha"`
	PrimaryHeadSHA string `json:"primaryHeadSha"`
	OwnerUUID      string `json:"ownerUuid"`
}

type CheckoutObservation struct {
	Status   CheckoutStatus                `json:"status"`
	Evidence domainreview.CheckoutEvidence `json:"evidence"`
}

type CheckoutPort interface {
	ObserveCheckout(context.Context, CheckoutRequest) (CheckoutObservation, error)
	CreateCheckout(context.Context, CheckoutRequest) error
	RemoveCheckout(context.Context, CheckoutRequest, domainreview.CheckoutEvidence) error
}
