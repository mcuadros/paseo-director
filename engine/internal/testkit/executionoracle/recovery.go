// SPDX-License-Identifier: Apache-2.0

package executionoracle

// RecoverySignal is an intentionally independent black-box model input. It
// does not import production execution types.
type RecoverySignal string

const (
	RecoveryNone           RecoverySignal = "none"
	RecoveryProvider       RecoverySignal = "provider_terminal"
	RecoveryPolicy         RecoverySignal = "policy_rejection"
	RecoveryAuthentication RecoverySignal = "authentication_rejection"
	RecoveryConfiguration  RecoverySignal = "configuration_rejection"
	RecoveryTransient      RecoverySignal = "transient_service"
	RecoveryUnknown        RecoverySignal = "unclassified"
)

type RecoveryModelInput struct {
	Signal              RecoverySignal
	ExactInventory      bool
	OriginalArchived    bool
	OriginalResumable   bool
	OriginalProcessGone bool
	ReplacementConsumed bool
	ReplacementFailed   bool
	CandidateExact      bool
	CandidatePresent    bool
}

type RecoveryModelResult string

const (
	RecoveryModelAdopt    RecoveryModelResult = "adopt"
	RecoveryModelWait     RecoveryModelResult = "wait"
	RecoveryModelArchive  RecoveryModelResult = "archive"
	RecoveryModelReplace  RecoveryModelResult = "replace"
	RecoveryModelContinue RecoveryModelResult = "continue"
	RecoveryModelNeedsYou RecoveryModelResult = "needs_you"
)

// ModelPrimaryRecovery encodes the independent safety properties used by the
// M3 execution oracle. It deliberately has no Effect, TaskStore, or host code.
func ModelPrimaryRecovery(input RecoveryModelInput) RecoveryModelResult {
	if !input.ExactInventory || input.ReplacementFailed {
		return RecoveryModelNeedsYou
	}
	if input.CandidatePresent {
		if input.CandidateExact {
			return RecoveryModelAdopt
		}
		return RecoveryModelNeedsYou
	}
	if input.ReplacementConsumed {
		return RecoveryModelContinue
	}
	if input.OriginalResumable {
		return RecoveryModelAdopt
	}
	switch input.Signal {
	case RecoveryNone:
		return RecoveryModelNeedsYou
	case RecoveryTransient:
		return RecoveryModelWait
	case RecoveryAuthentication, RecoveryConfiguration, RecoveryUnknown:
		return RecoveryModelNeedsYou
	case RecoveryProvider, RecoveryPolicy:
		if !input.OriginalArchived {
			return RecoveryModelArchive
		}
		if !input.OriginalProcessGone {
			return RecoveryModelNeedsYou
		}
		return RecoveryModelReplace
	default:
		return RecoveryModelNeedsYou
	}
}
