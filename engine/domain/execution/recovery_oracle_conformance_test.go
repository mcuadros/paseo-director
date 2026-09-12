// SPDX-License-Identifier: Apache-2.0

package execution

import (
	"testing"

	"github.com/mcuadros/director-engine/internal/testkit/executionoracle"
)

func TestPrimaryRecoveryConformsToIndependentExecutionOracle(t *testing.T) {
	signals := []ProviderFailureSignal{
		ProviderFailureNone, ProviderFailureTerminal,
		ProviderFailurePolicy, ProviderFailureAuthentication,
		ProviderFailureConfiguration, ProviderFailureTransient,
		ProviderFailureUnknown,
	}
	for _, signal := range signals {
		for _, resumable := range []bool{false, true} {
			for mask := 0; mask < 8; mask++ {
				facts := recoveryFactsForTest(signal)
				if resumable {
					facts.Recovery.Observation.Host.Inventory.Agents[0].Status = "idle"
					facts.Recovery.Observation.Host.Inventory.Agents[0].ArchivedAtPresent = false
				} else {
					facts.Recovery.Observation.Host.Inventory.Agents[0].Status = "error"
					facts.Recovery.Observation.Host.Inventory.Agents[0].ArchivedAtPresent = true
				}
				facts.Recovery.Observation.Host.Inventory.Workspaces[0].Active = mask&1 == 0
				facts.Recovery.Observation.Runtime.OriginalAgentProcessAbsent = mask&2 == 0
				facts.CurrentCandidateID = ""
				if mask&4 != 0 {
					facts.CurrentCandidateID = "candidate-1"
					facts.Recovery.Observation.Runtime.CandidatePresent = true
					facts.Recovery.Observation.Runtime.CandidateSHA = "1a2b3c4d5e6f789001234567890123456789abcd"
					facts.Recovery.Observation.Runtime.CandidateExact = true
				}
				facts.Recovery.Observation.Runtime.FactHash = PrimaryRuntimeRecoveryObservationHash(facts.Recovery.Observation.Runtime)
				facts.Recovery.Observation.Host.FactHash = EffectObservationHash(facts.Recovery.Observation.Host)
				facts.Recovery.Observation.FactHash = PrimaryRecoveryObservationHash(*facts.Recovery.Observation)
				got := EvaluatePrimaryRecovery(facts)
				oracle := executionoracle.ModelPrimaryRecovery(executionoracle.RecoveryModelInput{
					Signal: executionoracle.RecoverySignal(signal), ExactInventory: mask&1 == 0,
					OriginalArchived: !resumable, OriginalResumable: resumable, OriginalProcessGone: mask&2 == 0,
					CandidatePresent: mask&4 != 0, CandidateExact: mask&4 != 0,
				})
				if recoveryDispositionClass(got.Disposition) != oracle {
					t.Fatalf("signal=%s resumable=%v mask=%03b production=%s oracle=%s code=%s", signal, resumable, mask, got.Disposition, oracle, got.Code)
				}
			}
		}
	}
}

func recoveryDispositionClass(disposition PrimaryRecoveryDisposition) executionoracle.RecoveryModelResult {
	switch disposition {
	case RecoveryDispositionAdoptExisting:
		return executionoracle.RecoveryModelAdopt
	case RecoveryDispositionWaitExternal:
		return executionoracle.RecoveryModelWait
	case RecoveryDispositionArchiveOriginal:
		return executionoracle.RecoveryModelArchive
	case RecoveryDispositionAuthorizeReplace:
		return executionoracle.RecoveryModelReplace
	case RecoveryDispositionContinueReplace:
		return executionoracle.RecoveryModelContinue
	default:
		return executionoracle.RecoveryModelNeedsYou
	}
}
