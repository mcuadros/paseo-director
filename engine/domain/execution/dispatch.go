// SPDX-License-Identifier: Apache-2.0

package execution

import "slices"

// DispatchKind is the closed vocabulary of concurrently dispatchable delivery
// work. Helper subagents are deliberately absent: they are created by a Task
// Agent inside its own budget and are never Director dispatch decisions.
type DispatchKind string

const (
	DispatchTaskAgent   DispatchKind = "task_agent"
	DispatchReviewer    DispatchKind = "reviewer"
	DispatchCompleteCI  DispatchKind = "complete_ci"
	DispatchIntegration DispatchKind = "integration"
)

// DispatchLimits are the delivery-lane ceilings and the machine thresholds
// under which they hold. Load is carried in centi-units so the evaluator stays
// integral and deterministic; 4_800 is a one-minute load average of 48.
type DispatchLimits struct {
	TaskAgents                  uint64 `json:"taskAgents"`
	Reviewers                   uint64 `json:"reviewers"`
	CompleteCI                  uint64 `json:"completeCi"`
	IntegrationsPerRepository   uint64 `json:"integrationsPerRepository"`
	MaximumLoadCentiUnits       uint64 `json:"maximumLoadCentiUnits"`
	MinimumAvailableMemoryBytes uint64 `json:"minimumAvailableMemoryBytes"`
	MinimumFreeTemporaryBytes   uint64 `json:"minimumFreeTemporaryBytes"`
	MaximumObservationAgeMillis int64  `json:"maximumObservationAgeMillis"`
}

// DefaultDispatchLimits is the owner-approved 6/2/2 delivery lane: six Task
// Agents, two independent Reviewers on non-interfering bases, two complete CI
// processes, and one integration per repository.
func DefaultDispatchLimits() DispatchLimits {
	return DispatchLimits{
		TaskAgents: 6, Reviewers: 2, CompleteCI: 2, IntegrationsPerRepository: 1,
		MaximumLoadCentiUnits:       4_800,
		MinimumAvailableMemoryBytes: 24 << 30,
		MinimumFreeTemporaryBytes:   50 << 30,
		MaximumObservationAgeMillis: 30_000,
	}
}

// DispatchObservation is one immutable machine sample. Like every other
// operational fact it carries TaskStore milliseconds and the pure evaluator
// reads no clock.
type DispatchObservation struct {
	ID                   string      `json:"id"`
	ObservedAtMillis     int64       `json:"observedAtMillis"`
	LoadCentiUnits       Measurement `json:"loadCentiUnits"`
	AvailableMemoryBytes Measurement `json:"availableMemoryBytes"`
	FreeTemporaryBytes   Measurement `json:"freeTemporaryBytes"`
}

// DispatchInFlight is the durable count of work already dispatched and not yet
// terminal. ReviewerBases lists the base SHA each in-flight independent Review
// is bound to, which is what makes reviewer interference decidable.
type DispatchInFlight struct {
	TaskAgents             uint64   `json:"taskAgents"`
	Reviewers              uint64   `json:"reviewers"`
	CompleteCI             uint64   `json:"completeCi"`
	RepositoryIntegrations uint64   `json:"repositoryIntegrations"`
	ReviewerBases          []string `json:"reviewerBases,omitempty"`
}

// DispatchRequest asks to admit exactly one more unit of delivery work.
type DispatchRequest struct {
	Kind     DispatchKind     `json:"kind"`
	BaseSHA  string           `json:"baseSha,omitempty"`
	InFlight DispatchInFlight `json:"inFlight"`
}

const (
	NeedDispatchPolicyInvalid      NeedCode = "dispatch_policy_invalid"
	NeedDispatchFactMissing        NeedCode = "dispatch_fact_missing"
	NeedDispatchLoadCeiling        NeedCode = "dispatch_load_ceiling_reached"
	NeedDispatchMemoryFloor        NeedCode = "dispatch_memory_floor_reached"
	NeedDispatchTemporaryFloor     NeedCode = "dispatch_temporary_floor_reached"
	NeedTaskAgentConcurrency       NeedCode = "task_agent_concurrency_reached"
	NeedReviewerConcurrency        NeedCode = "reviewer_concurrency_reached"
	NeedReviewerBaseInterference   NeedCode = "reviewer_base_interference"
	NeedCompleteCIConcurrency      NeedCode = "complete_ci_concurrency_reached"
	NeedIntegrationNotExclusive    NeedCode = "repository_integration_not_exclusive"
	NeedDispatchRequestUnsupported NeedCode = "dispatch_request_unsupported"
)

func finiteDispatchLimits(limits DispatchLimits) bool {
	return limits.TaskAgents > 0 && limits.Reviewers > 0 && limits.CompleteCI > 0 &&
		limits.IntegrationsPerRepository > 0 && limits.MaximumLoadCentiUnits > 0 &&
		limits.MinimumAvailableMemoryBytes > 0 && limits.MinimumFreeTemporaryBytes > 0 &&
		limits.MaximumObservationAgeMillis > 0
}

// ceilingReserved reports a dimension still under its ceiling but with less
// than a quarter of that ceiling left as headroom.
func ceilingReserved(used, ceiling uint64) bool {
	threeQuarters := (ceiling/4)*3 + ((ceiling%4)*3)/4
	return used > threeQuarters
}

// floorReserved is the same reserve band for a dimension bounded from below:
// still above its floor, but by less than a quarter of it.
func floorReserved(available, minimum uint64) bool {
	if available < minimum {
		return true
	}
	reserve := minimum / 4
	if minimum%4 != 0 {
		reserve++
	}
	return available-minimum < reserve
}

// adapted contracts a concurrency ceiling by one slot per dimension currently
// in its reserve band, never below a single unit. This is what makes the lane
// adaptive rather than a fixed count: a machine close to its thresholds admits
// fewer workers long before it crosses one, while a quiet machine keeps the
// full 6/2/2.
func adapted(ceiling, pressure uint64) uint64 {
	if pressure >= ceiling {
		return 1
	}
	return ceiling - pressure
}

// DispatchPressure counts the machine dimensions inside their reserve band.
func DispatchPressure(limits DispatchLimits, observation DispatchObservation) uint64 {
	var pressure uint64
	if ceilingReserved(observation.LoadCentiUnits.Value, limits.MaximumLoadCentiUnits) {
		pressure++
	}
	if floorReserved(observation.AvailableMemoryBytes.Value, limits.MinimumAvailableMemoryBytes) {
		pressure++
	}
	if floorReserved(observation.FreeTemporaryBytes.Value, limits.MinimumFreeTemporaryBytes) {
		pressure++
	}
	return pressure
}

// EvaluateDispatch admits or parks exactly one additional unit of delivery
// work. It grants no cleanup authority and, like every admission here, refuses
// on a missing or stale fact instead of assuming capacity.
func EvaluateDispatch(
	limits DispatchLimits,
	observation DispatchObservation,
	request DispatchRequest,
	nowMillis int64,
) Admission {
	if !finiteDispatchLimits(limits) {
		return park(NeedDispatchPolicyInvalid)
	}
	if observation.ID == "" || observation.ObservedAtMillis > nowMillis ||
		nowMillis-observation.ObservedAtMillis > limits.MaximumObservationAgeMillis ||
		!observation.LoadCentiUnits.Present || !observation.AvailableMemoryBytes.Present ||
		!observation.FreeTemporaryBytes.Present {
		return park(NeedDispatchFactMissing)
	}
	if observation.LoadCentiUnits.Value > limits.MaximumLoadCentiUnits {
		return park(NeedDispatchLoadCeiling)
	}
	if observation.AvailableMemoryBytes.Value < limits.MinimumAvailableMemoryBytes {
		return park(NeedDispatchMemoryFloor)
	}
	if observation.FreeTemporaryBytes.Value < limits.MinimumFreeTemporaryBytes {
		return park(NeedDispatchTemporaryFloor)
	}
	pressure := DispatchPressure(limits, observation)
	switch request.Kind {
	case DispatchTaskAgent:
		if request.InFlight.TaskAgents >= adapted(limits.TaskAgents, pressure) {
			return park(NeedTaskAgentConcurrency)
		}
	case DispatchReviewer:
		if !shaPresent(request.BaseSHA) {
			return park(NeedDispatchFactMissing)
		}
		if request.InFlight.Reviewers >= adapted(limits.Reviewers, pressure) {
			return park(NeedReviewerConcurrency)
		}
		// Two Reviews sharing a base race for the same merge target: admitting
		// both would let an integration invalidate an already approved exact
		// Candidate. Independence is preserved by separating bases, never by
		// coupling the Reviewers.
		if slices.Contains(request.InFlight.ReviewerBases, request.BaseSHA) {
			return park(NeedReviewerBaseInterference)
		}
	case DispatchCompleteCI:
		if request.InFlight.CompleteCI >= adapted(limits.CompleteCI, pressure) {
			return park(NeedCompleteCIConcurrency)
		}
	case DispatchIntegration:
		// Integration is exclusive per repository and is never adapted: a
		// second concurrent merge could move the base under the first.
		if request.InFlight.RepositoryIntegrations >= limits.IntegrationsPerRepository {
			return park(NeedIntegrationNotExclusive)
		}
	default:
		return park(NeedDispatchRequestUnsupported)
	}
	return Admission{Kind: AdmissionAllow, Digest: evidenceDigest(struct {
		Limits      DispatchLimits      `json:"limits"`
		Observation DispatchObservation `json:"observation"`
		Request     DispatchRequest     `json:"request"`
		Pressure    uint64              `json:"pressure"`
	}{limits, observation, request, pressure})}
}

func shaPresent(value string) bool {
	if len(value) != 40 {
		return false
	}
	for _, character := range value {
		if (character < '0' || character > '9') && (character < 'a' || character > 'f') {
			return false
		}
	}
	return true
}
