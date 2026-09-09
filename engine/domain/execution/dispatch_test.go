// SPDX-License-Identifier: Apache-2.0

package execution

import (
	"strings"
	"testing"
)

func quietMachine() DispatchObservation {
	return DispatchObservation{
		ID: "dispatch-1", ObservedAtMillis: 1_000,
		LoadCentiUnits:       Measurement{Present: true, Value: 100},
		AvailableMemoryBytes: Measurement{Present: true, Value: 96 << 30},
		FreeTemporaryBytes:   Measurement{Present: true, Value: 180 << 30},
	}
}

func TestEvaluateDispatchAdmitsTheFullSixTwoTwoLaneOnAQuietMachine(t *testing.T) {
	limits := DefaultDispatchLimits()
	observation := quietMachine()
	if pressure := DispatchPressure(limits, observation); pressure != 0 {
		t.Fatalf("quiet machine pressure = %d", pressure)
	}
	for name, request := range map[string]DispatchRequest{
		"sixth task agent": {
			Kind: DispatchTaskAgent, InFlight: DispatchInFlight{TaskAgents: 5},
		},
		"second reviewer": {
			Kind: DispatchReviewer, BaseSHA: strings.Repeat("a", 40),
			InFlight: DispatchInFlight{Reviewers: 1, ReviewerBases: []string{strings.Repeat("b", 40)}},
		},
		"second complete CI": {
			Kind: DispatchCompleteCI, InFlight: DispatchInFlight{CompleteCI: 1},
		},
		"only integration": {
			Kind: DispatchIntegration, InFlight: DispatchInFlight{},
		},
	} {
		t.Run(name, func(t *testing.T) {
			admission := EvaluateDispatch(limits, observation, request, 1_001)
			if admission.Kind != AdmissionAllow || admission.Digest == "" ||
				admission.CleanupAuthorized {
				t.Fatalf("admission = %#v", admission)
			}
		})
	}
}

func TestEvaluateDispatchParksAtEveryDeliveryLaneCeiling(t *testing.T) {
	limits := DefaultDispatchLimits()
	observation := quietMachine()
	base := strings.Repeat("a", 40)
	for name, expectation := range map[string]struct {
		request DispatchRequest
		code    NeedCode
	}{
		"seventh task agent": {
			DispatchRequest{Kind: DispatchTaskAgent, InFlight: DispatchInFlight{TaskAgents: 6}},
			NeedTaskAgentConcurrency,
		},
		"third reviewer": {
			DispatchRequest{Kind: DispatchReviewer, BaseSHA: base, InFlight: DispatchInFlight{
				Reviewers: 2, ReviewerBases: []string{strings.Repeat("b", 40), strings.Repeat("c", 40)},
			}},
			NeedReviewerConcurrency,
		},
		"reviewer sharing an in-flight base": {
			DispatchRequest{Kind: DispatchReviewer, BaseSHA: base, InFlight: DispatchInFlight{
				Reviewers: 1, ReviewerBases: []string{base},
			}},
			NeedReviewerBaseInterference,
		},
		"third complete CI": {
			DispatchRequest{Kind: DispatchCompleteCI, InFlight: DispatchInFlight{CompleteCI: 2}},
			NeedCompleteCIConcurrency,
		},
		"second repository integration": {
			DispatchRequest{Kind: DispatchIntegration, InFlight: DispatchInFlight{RepositoryIntegrations: 1}},
			NeedIntegrationNotExclusive,
		},
		"unknown work": {
			DispatchRequest{Kind: "helper_subagent"},
			NeedDispatchRequestUnsupported,
		},
		"reviewer without a base": {
			DispatchRequest{Kind: DispatchReviewer},
			NeedDispatchFactMissing,
		},
	} {
		t.Run(name, func(t *testing.T) {
			admission := EvaluateDispatch(limits, observation, expectation.request, 1_001)
			if admission.Kind != AdmissionPark || admission.Code != expectation.code ||
				admission.CleanupAuthorized {
				t.Fatalf("admission = %#v", admission)
			}
		})
	}
}

func TestEvaluateDispatchParksOnAMissingStaleOrExceededMachineFact(t *testing.T) {
	limits := DefaultDispatchLimits()
	request := DispatchRequest{Kind: DispatchTaskAgent}
	for name, expectation := range map[string]struct {
		mutate func(*DispatchObservation)
		now    int64
		code   NeedCode
	}{
		"no identity":            {func(value *DispatchObservation) { value.ID = "" }, 1_001, NeedDispatchFactMissing},
		"absent load":            {func(value *DispatchObservation) { value.LoadCentiUnits.Present = false }, 1_001, NeedDispatchFactMissing},
		"absent memory":          {func(value *DispatchObservation) { value.AvailableMemoryBytes.Present = false }, 1_001, NeedDispatchFactMissing},
		"absent tmp":             {func(value *DispatchObservation) { value.FreeTemporaryBytes.Present = false }, 1_001, NeedDispatchFactMissing},
		"stale sample":           {func(*DispatchObservation) {}, 1_000 + 30_001, NeedDispatchFactMissing},
		"sample from the future": {func(*DispatchObservation) {}, 999, NeedDispatchFactMissing},
		"load past 48": {
			func(value *DispatchObservation) { value.LoadCentiUnits.Value = 4_801 },
			1_001, NeedDispatchLoadCeiling,
		},
		"memory under 24 GiB": {
			func(value *DispatchObservation) { value.AvailableMemoryBytes.Value = (24 << 30) - 1 },
			1_001, NeedDispatchMemoryFloor,
		},
		"free /tmp under 50 GiB": {
			func(value *DispatchObservation) { value.FreeTemporaryBytes.Value = (50 << 30) - 1 },
			1_001, NeedDispatchTemporaryFloor,
		},
	} {
		t.Run(name, func(t *testing.T) {
			observation := quietMachine()
			expectation.mutate(&observation)
			admission := EvaluateDispatch(limits, observation, request, expectation.now)
			if admission.Kind != AdmissionPark || admission.Code != expectation.code {
				t.Fatalf("admission = %#v", admission)
			}
		})
	}
	if admission := EvaluateDispatch(DispatchLimits{}, quietMachine(), request, 1_001); admission.Kind != AdmissionPark ||
		admission.Code != NeedDispatchPolicyInvalid {
		t.Fatalf("empty policy admission = %#v", admission)
	}
}

// The lane is adaptive, not a fixed count: a machine that is still inside every
// threshold but close to one admits fewer workers, and the ceiling never drops
// below a single unit so delivery cannot deadlock under sustained pressure.
func TestEvaluateDispatchContractsTheLaneUnderMachinePressure(t *testing.T) {
	limits := DefaultDispatchLimits()
	pressured := quietMachine()
	pressured.LoadCentiUnits.Value = 4_600
	if pressure := DispatchPressure(limits, pressured); pressure != 1 {
		t.Fatalf("single-dimension pressure = %d", pressure)
	}
	if admission := EvaluateDispatch(limits, pressured, DispatchRequest{
		Kind: DispatchTaskAgent, InFlight: DispatchInFlight{TaskAgents: 5},
	}, 1_001); admission.Kind != AdmissionPark || admission.Code != NeedTaskAgentConcurrency {
		t.Fatalf("contracted task-agent admission = %#v", admission)
	}
	if admission := EvaluateDispatch(limits, pressured, DispatchRequest{
		Kind: DispatchTaskAgent, InFlight: DispatchInFlight{TaskAgents: 4},
	}, 1_001); admission.Kind != AdmissionAllow {
		t.Fatalf("fifth task agent under one pressure dimension = %#v", admission)
	}

	crowded := quietMachine()
	crowded.LoadCentiUnits.Value = 4_600
	crowded.AvailableMemoryBytes.Value = 25 << 30
	crowded.FreeTemporaryBytes.Value = 52 << 30
	if pressure := DispatchPressure(limits, crowded); pressure != 3 {
		t.Fatalf("three-dimension pressure = %d", pressure)
	}
	if admission := EvaluateDispatch(limits, crowded, DispatchRequest{
		Kind: DispatchCompleteCI, InFlight: DispatchInFlight{CompleteCI: 1},
	}, 1_001); admission.Kind != AdmissionPark || admission.Code != NeedCompleteCIConcurrency {
		t.Fatalf("contracted CI admission = %#v", admission)
	}
	if admission := EvaluateDispatch(limits, crowded, DispatchRequest{
		Kind: DispatchCompleteCI, InFlight: DispatchInFlight{},
	}, 1_001); admission.Kind != AdmissionAllow {
		t.Fatalf("first CI under full pressure = %#v", admission)
	}
	if admission := EvaluateDispatch(limits, crowded, DispatchRequest{
		Kind: DispatchTaskAgent, InFlight: DispatchInFlight{TaskAgents: 3},
	}, 1_001); admission.Kind != AdmissionPark || admission.Code != NeedTaskAgentConcurrency {
		t.Fatalf("task agent beyond the contracted ceiling = %#v", admission)
	}
}

func TestDispatchPressureDoesNotOverflowAtUint64Limits(t *testing.T) {
	maximum := ^uint64(0)
	limits := DefaultDispatchLimits()
	limits.MaximumLoadCentiUnits = maximum
	limits.MinimumAvailableMemoryBytes = maximum
	limits.MinimumFreeTemporaryBytes = maximum
	observation := quietMachine()
	observation.LoadCentiUnits.Value = maximum
	observation.AvailableMemoryBytes.Value = maximum
	observation.FreeTemporaryBytes.Value = maximum
	if pressure := DispatchPressure(limits, observation); pressure != 3 {
		t.Fatalf("maximum uint64 pressure = %d", pressure)
	}
}

// The admission digest is evidence, so it must move with the request and the
// observed machine rather than only with the configured ceiling.
func TestEvaluateDispatchDigestBindsTheCompleteDecision(t *testing.T) {
	limits := DefaultDispatchLimits()
	base := strings.Repeat("a", 40)
	first := EvaluateDispatch(limits, quietMachine(), DispatchRequest{
		Kind: DispatchReviewer, BaseSHA: base,
	}, 1_001)
	sameAgain := EvaluateDispatch(limits, quietMachine(), DispatchRequest{
		Kind: DispatchReviewer, BaseSHA: base,
	}, 1_002)
	if first.Digest != sameAgain.Digest {
		t.Fatal("identical dispatch facts produced different digests")
	}
	other := EvaluateDispatch(limits, quietMachine(), DispatchRequest{
		Kind: DispatchReviewer, BaseSHA: strings.Repeat("b", 40),
	}, 1_001)
	if other.Digest == first.Digest {
		t.Fatal("dispatch digest ignored the requested base")
	}
	louder := quietMachine()
	louder.LoadCentiUnits.Value = 4_600
	if EvaluateDispatch(limits, louder, DispatchRequest{
		Kind: DispatchReviewer, BaseSHA: base,
	}, 1_001).Digest == first.Digest {
		t.Fatal("dispatch digest ignored the observed machine")
	}
}
