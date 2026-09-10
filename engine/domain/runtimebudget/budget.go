// SPDX-License-Identifier: Apache-2.0

// Package runtimebudget owns the pure, durable Run budget ledger. Adapters
// report bounded usage facts; they never derive limits, prices, warnings, or
// lifecycle decisions.
package runtimebudget

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"math/bits"
	"slices"
	"strings"
	"unicode"
	"unicode/utf8"

	domainconfig "github.com/mcuadros/director-engine/domain/configuration"
)

const (
	SchemaVersion            = "director.runtime-budget/v1"
	SoftThresholdBasisPoints = uint64(8_500)
	MaximumTurnLimit         = uint64(256)
	MaximumCICycleLimit      = uint32(256)
	MaximumReservations      = 768
	MaximumSnapshots         = 512
	MaximumActivities        = 512
	MaximumWarnings          = 8
	DefaultCorrectionLimit   = uint32(3)
	DefaultReplacementLimit  = uint32(1)
	DefaultSetupAttemptLimit = uint32(2)
)

var ErrInvalid = errors.New("runtime budget fact is invalid")

type Dimension string

const (
	DimensionWallTime Dimension = "wall_time"
	DimensionTokens   Dimension = "tokens"
	DimensionTurns    Dimension = "turns"
	DimensionCost     Dimension = "cost"
)

type Activity string

const (
	ActivityWorkerBootstrap Activity = "worker_bootstrap"
	ActivityWorkerTurn      Activity = "worker_turn"
	ActivityHelperTurn      Activity = "helper_turn"
	ActivityReviewerTurn    Activity = "reviewer_turn"
	ActivityCorrectionTurn  Activity = "correction_turn"
	ActivitySetupAttempt    Activity = "setup_attempt"
	ActivityValidationCycle Activity = "validation_cycle"
	ActivityReplacement     Activity = "worker_replacement"
)

type UsageState string

const (
	UsageCurrent     UsageState = "current"
	UsageUnavailable UsageState = "unavailable"
	UsageAmbiguous   UsageState = "ambiguous"
)

type Disposition string

const (
	DispositionAllow         Disposition = "allow"
	DispositionSoftPause     Disposition = "soft_pause"
	DispositionHardExhausted Disposition = "hard_exhausted"
	DispositionFailClosed    Disposition = "fail_closed"
)

type ReasonCode string

const (
	ReasonWallTimeSoft             ReasonCode = "budget_wall_time_soft_limit_reached"
	ReasonTokensSoft               ReasonCode = "budget_tokens_soft_limit_reached"
	ReasonTurnsSoft                ReasonCode = "budget_turns_soft_limit_reached"
	ReasonCostSoft                 ReasonCode = "budget_cost_soft_limit_reached"
	ReasonWallTimeHard             ReasonCode = "budget_wall_time_exhausted"
	ReasonTokensHard               ReasonCode = "budget_tokens_exhausted"
	ReasonTurnsHard                ReasonCode = "budget_turns_exhausted"
	ReasonCostHard                 ReasonCode = "budget_cost_exhausted"
	ReasonCorrectionHard           ReasonCode = "budget_correction_attempts_exhausted"
	ReasonCIHard                   ReasonCode = "budget_ci_cycles_exhausted"
	ReasonReplacementHard          ReasonCode = "budget_replacement_attempts_exhausted"
	ReasonSetupHard                ReasonCode = "budget_setup_attempts_exhausted"
	ReasonRepeatedValidation       ReasonCode = "budget_repeated_validation_exhausted"
	ReasonSetupChurn               ReasonCode = "budget_setup_churn_exhausted"
	ReasonCorrectionChurn          ReasonCode = "budget_correction_churn_exhausted"
	ReasonProviderUsageUnavailable ReasonCode = "budget_provider_usage_unavailable"
	ReasonProviderUsageAmbiguous   ReasonCode = "budget_provider_usage_ambiguous"
	ReasonClockRegressed           ReasonCode = "budget_clock_regressed"
	ReasonUsageOverflow            ReasonCode = "budget_usage_overflow"
	ReasonLedgerInvalid            ReasonCode = "budget_ledger_invalid"
	ReasonLeaseFenced              ReasonCode = "budget_lease_fenced"
)

// Policy is frozen into one Run. CostLimitMicrousd is zero only when cost
// limiting is explicitly disabled; Director never estimates a missing cost.
type Policy struct {
	SchemaVersion             string `json:"schemaVersion"`
	Revision                  string `json:"revision"`
	SoftThresholdBasisPoints  uint64 `json:"softThresholdBasisPoints"`
	WallTimeLimitMilliseconds uint64 `json:"wallTimeLimitMilliseconds"`
	TokenLimit                uint64 `json:"tokenLimit"`
	TurnLimit                 uint64 `json:"turnLimit"`
	CostLimitMicrousd         uint64 `json:"costLimitMicrousd,omitempty"`
	CorrectionLimit           uint32 `json:"correctionLimit"`
	CICycleLimit              uint32 `json:"ciCycleLimit"`
	ReplacementLimit          uint32 `json:"replacementLimit"`
	SetupAttemptLimit         uint32 `json:"setupAttemptLimit"`
}

func NewPolicy(revision string, wallTimeMilliseconds, tokens, turns, costMicrousd uint64, ciCycles uint32) Policy {
	return Policy{
		SchemaVersion: SchemaVersion, Revision: revision,
		SoftThresholdBasisPoints:  SoftThresholdBasisPoints,
		WallTimeLimitMilliseconds: wallTimeMilliseconds, TokenLimit: tokens,
		TurnLimit: turns, CostLimitMicrousd: costMicrousd,
		CorrectionLimit: DefaultCorrectionLimit, CICycleLimit: ciCycles,
		ReplacementLimit: DefaultReplacementLimit, SetupAttemptLimit: DefaultSetupAttemptLimit,
	}
}

// PolicyFromRunBudget converts only the frozen Organizer values. It refuses
// overflow and keeps an omitted cost disabled rather than fabricating one.
func PolicyFromRunBudget(revision string, budget domainconfig.RunBudget) (Policy, error) {
	if budget.ElapsedSeconds < 1 || uint64(budget.ElapsedSeconds) > ^uint64(0)/1_000 ||
		budget.Tokens < 1 || budget.Turns < 1 || budget.CICycles < 1 ||
		budget.CICycles > int64(^uint32(0)) || budget.CostMicrousd < 0 {
		return Policy{}, ErrInvalid
	}
	policy := NewPolicy(
		revision, uint64(budget.ElapsedSeconds)*1_000, uint64(budget.Tokens),
		uint64(budget.Turns), uint64(budget.CostMicrousd), uint32(budget.CICycles),
	)
	if !ValidPolicy(policy) {
		return Policy{}, ErrInvalid
	}
	return policy, nil
}

func ValidPolicy(policy Policy) bool {
	return policy.SchemaVersion == SchemaVersion && validIdentity(policy.Revision) &&
		policy.SoftThresholdBasisPoints == SoftThresholdBasisPoints &&
		policy.WallTimeLimitMilliseconds > 0 && policy.TokenLimit > 0 &&
		policy.TurnLimit > 0 && policy.TurnLimit <= MaximumTurnLimit &&
		policy.CorrectionLimit == DefaultCorrectionLimit &&
		policy.CICycleLimit > 0 && policy.CICycleLimit <= MaximumCICycleLimit &&
		policy.ReplacementLimit == DefaultReplacementLimit && policy.SetupAttemptLimit == DefaultSetupAttemptLimit
}

type Demand struct {
	WallTimeMilliseconds uint64 `json:"wallTimeMilliseconds"`
	Tokens               uint64 `json:"tokens"`
	Turns                uint64 `json:"turns"`
	CostMicrousd         uint64 `json:"costMicrousd,omitempty"`
}

type Consumption struct {
	WallTimeMilliseconds uint64 `json:"wallTimeMilliseconds"`
	Tokens               uint64 `json:"tokens"`
	Turns                uint64 `json:"turns"`
	CostMicrousd         uint64 `json:"costMicrousd"`
	CorrectionAttempts   uint32 `json:"correctionAttempts"`
	CICycles             uint32 `json:"ciCycles"`
	ReplacementAttempts  uint32 `json:"replacementAttempts"`
	SetupAttempts        uint32 `json:"setupAttempts"`
}

type Reservation struct {
	ID             string   `json:"id"`
	EffectID       string   `json:"effectId"`
	Activity       Activity `json:"activity"`
	LeaseEpoch     uint64   `json:"leaseEpoch"`
	PolicyRevision string   `json:"policyRevision"`
	Demand         Demand   `json:"demand"`
	CandidateSHA   string   `json:"candidateSha,omitempty"`
	Fingerprint    string   `json:"fingerprint,omitempty"`
	Released       bool     `json:"released"`
	EvidenceID     string   `json:"evidenceId,omitempty"`
}

// ProviderUsage is the bounded provider-native usage tuple transported by a
// host connector. Presence bits distinguish an observed zero from an absent
// field. Cost is not synthesized when absent.
type ProviderUsage struct {
	State               UsageState `json:"state"`
	SourceRevision      string     `json:"sourceRevision,omitempty"`
	InputTokensPresent  bool       `json:"inputTokensPresent"`
	InputTokens         uint64     `json:"inputTokens"`
	CachedInputTokens   uint64     `json:"cachedInputTokens"`
	OutputTokensPresent bool       `json:"outputTokensPresent"`
	OutputTokens        uint64     `json:"outputTokens"`
	CostMicrousdPresent bool       `json:"costMicrousdPresent"`
	CostMicrousd        uint64     `json:"costMicrousd"`
}

type ProviderObservation struct {
	ID               string   `json:"id"`
	EffectID         string   `json:"effectId"`
	AgentID          string   `json:"agentId"`
	Activity         Activity `json:"activity"`
	LeaseEpoch       uint64   `json:"leaseEpoch"`
	PolicyRevision   string   `json:"policyRevision"`
	Sequence         uint64   `json:"sequence"`
	ObservedAtMillis int64    `json:"observedAtMillis"`
	ProviderUsage
	FactHash string `json:"factHash"`
}

type ActivityObservation struct {
	ID               string   `json:"id"`
	ReservationID    string   `json:"reservationId"`
	EffectID         string   `json:"effectId"`
	Activity         Activity `json:"activity"`
	LeaseEpoch       uint64   `json:"leaseEpoch"`
	PolicyRevision   string   `json:"policyRevision"`
	ObservedAtMillis int64    `json:"observedAtMillis"`
	CandidateSHA     string   `json:"candidateSha,omitempty"`
	Fingerprint      string   `json:"fingerprint,omitempty"`
	Failed           bool     `json:"failed"`
	FactHash         string   `json:"factHash"`
}

type SourceCursor struct {
	AgentID            string `json:"agentId"`
	Sequence           uint64 `json:"sequence"`
	LastSourceRevision string `json:"lastSourceRevision,omitempty"`
}

type Warning struct {
	ID                   string    `json:"id"`
	Dimension            Dimension `json:"dimension"`
	PolicyRevision       string    `json:"policyRevision"`
	Measured             uint64    `json:"measured"`
	Limit                uint64    `json:"limit"`
	RatioBasisPoints     uint64    `json:"ratioBasisPoints"`
	ObservedAtMillis     int64     `json:"observedAtMillis"`
	Acknowledged         bool      `json:"acknowledged"`
	AcknowledgedBy       string    `json:"acknowledgedBy,omitempty"`
	AcknowledgedAtMillis int64     `json:"acknowledgedAtMillis,omitempty"`
}

type Ledger struct {
	SchemaVersion        string                `json:"schemaVersion"`
	Policy               Policy                `json:"policy"`
	StartedAtMillis      int64                 `json:"startedAtMillis"`
	LastObservedAtMillis int64                 `json:"lastObservedAtMillis"`
	TelemetryState       UsageState            `json:"telemetryState"`
	Consumption          Consumption           `json:"consumption"`
	Reservations         []Reservation         `json:"reservations"`
	ProviderSnapshots    []ProviderObservation `json:"providerSnapshots"`
	Activities           []ActivityObservation `json:"activities"`
	SourceCursors        []SourceCursor        `json:"sourceCursors"`
	Warnings             []Warning             `json:"warnings"`
}

type Decision struct {
	Disposition      Disposition `json:"disposition"`
	Reason           ReasonCode  `json:"reason,omitempty"`
	Dimension        Dimension   `json:"dimension,omitempty"`
	Measured         uint64      `json:"measured,omitempty"`
	Limit            uint64      `json:"limit,omitempty"`
	RatioBasisPoints uint64      `json:"ratioBasisPoints,omitempty"`
	WarningID        string      `json:"warningId,omitempty"`
}

type ReserveRequest struct {
	ID             string   `json:"id"`
	EffectID       string   `json:"effectId"`
	Activity       Activity `json:"activity"`
	LeaseEpoch     uint64   `json:"leaseEpoch"`
	PolicyRevision string   `json:"policyRevision"`
	Demand         Demand   `json:"demand"`
	CandidateSHA   string   `json:"candidateSha,omitempty"`
	Fingerprint    string   `json:"fingerprint,omitempty"`
}

func validIdentity(value string) bool {
	return value != "" && len(value) <= 128 && utf8.ValidString(value) &&
		value == strings.TrimSpace(value) && strings.IndexFunc(value, unicode.IsControl) < 0
}

func validActivity(value Activity) bool {
	return slices.Contains([]Activity{
		ActivityWorkerBootstrap, ActivityWorkerTurn, ActivityHelperTurn, ActivityReviewerTurn,
		ActivityCorrectionTurn, ActivitySetupAttempt, ActivityValidationCycle, ActivityReplacement,
	}, value)
}

func validDimension(value Dimension) bool {
	return slices.Contains([]Dimension{DimensionWallTime, DimensionTokens, DimensionTurns, DimensionCost}, value)
}

func validHex(value string, lengths ...int) bool {
	if !slices.Contains(lengths, len(value)) {
		return false
	}
	return strings.IndexFunc(value, func(character rune) bool {
		return !strings.ContainsRune("0123456789abcdef", character)
	}) < 0
}

func validOptionalCandidate(value string) bool { return value == "" || validHex(value, 40, 64) }

func validOptionalFingerprint(value string) bool { return value == "" || validHex(value, 64) }

func validProviderUsage(usage ProviderUsage) bool {
	if usage.State != UsageCurrent {
		return (usage.State == UsageUnavailable || usage.State == UsageAmbiguous) && usage.SourceRevision == "" &&
			!usage.InputTokensPresent && usage.InputTokens == 0 && usage.CachedInputTokens == 0 &&
			!usage.OutputTokensPresent && usage.OutputTokens == 0 &&
			!usage.CostMicrousdPresent && usage.CostMicrousd == 0
	}
	return validIdentity(usage.SourceRevision) &&
		(usage.InputTokensPresent || usage.InputTokens == 0) &&
		(usage.OutputTokensPresent || usage.OutputTokens == 0) &&
		(usage.CostMicrousdPresent || usage.CostMicrousd == 0)
}

func modelActivity(value Activity) bool {
	return slices.Contains([]Activity{
		ActivityWorkerBootstrap, ActivityWorkerTurn, ActivityHelperTurn,
		ActivityReviewerTurn, ActivityCorrectionTurn,
	}, value)
}

func observationHash(value any) string {
	encoded, err := json.Marshal(value)
	if err != nil {
		panic("marshal runtime budget fact: " + err.Error())
	}
	digest := sha256.Sum256(encoded)
	return hex.EncodeToString(digest[:])
}

func ProviderObservationHash(observation ProviderObservation) string {
	observation.FactHash = ""
	return observationHash(observation)
}

func ActivityObservationHash(observation ActivityObservation) string {
	observation.FactHash = ""
	return observationHash(observation)
}

func NewLedger(policy Policy, startedAtMillis int64) (Ledger, error) {
	if !ValidPolicy(policy) || startedAtMillis < 0 {
		return Ledger{}, ErrInvalid
	}
	return Ledger{
		SchemaVersion: SchemaVersion, Policy: policy, StartedAtMillis: startedAtMillis,
		LastObservedAtMillis: startedAtMillis, TelemetryState: UsageCurrent,
		Reservations: []Reservation{}, ProviderSnapshots: []ProviderObservation{},
		Activities: []ActivityObservation{}, SourceCursors: []SourceCursor{}, Warnings: []Warning{},
	}, nil
}

func clone(ledger Ledger) Ledger {
	ledger.Reservations = slices.Clone(ledger.Reservations)
	ledger.ProviderSnapshots = slices.Clone(ledger.ProviderSnapshots)
	ledger.Activities = slices.Clone(ledger.Activities)
	ledger.SourceCursors = slices.Clone(ledger.SourceCursors)
	ledger.Warnings = slices.Clone(ledger.Warnings)
	return ledger
}

func add(values ...uint64) (uint64, bool) {
	var result uint64
	for _, value := range values {
		var carry uint64
		result, carry = bits.Add64(result, value, 0)
		if carry != 0 {
			return 0, false
		}
	}
	return result, true
}

func ratioBasisPoints(value, limit uint64) uint64 {
	if limit == 0 || value >= limit {
		return 10_000
	}
	high, low := bits.Mul64(value, 10_000)
	quotient, _ := bits.Div64(high, low, limit)
	return quotient
}

func softThreshold(limit uint64) uint64 {
	return limit - ((limit/100)*15 + ((limit%100)*15)/100)
}

func outstanding(ledger Ledger) (Demand, Consumption, bool) {
	var demand Demand
	var counts Consumption
	for _, reservation := range ledger.Reservations {
		if reservation.Released {
			continue
		}
		var ok bool
		if demand.WallTimeMilliseconds, ok = add(demand.WallTimeMilliseconds, reservation.Demand.WallTimeMilliseconds); !ok {
			return Demand{}, Consumption{}, false
		}
		if demand.Tokens, ok = add(demand.Tokens, reservation.Demand.Tokens); !ok {
			return Demand{}, Consumption{}, false
		}
		if demand.Turns, ok = add(demand.Turns, reservation.Demand.Turns); !ok {
			return Demand{}, Consumption{}, false
		}
		if demand.CostMicrousd, ok = add(demand.CostMicrousd, reservation.Demand.CostMicrousd); !ok {
			return Demand{}, Consumption{}, false
		}
		switch reservation.Activity {
		case ActivityCorrectionTurn:
			counts.CorrectionAttempts++
		case ActivityValidationCycle:
			counts.CICycles++
		case ActivityReplacement:
			counts.ReplacementAttempts++
		case ActivitySetupAttempt:
			counts.SetupAttempts++
		}
	}
	return demand, counts, true
}

func Outstanding(ledger Ledger) (Demand, Consumption, bool) {
	if !ValidLedger(ledger) {
		return Demand{}, Consumption{}, false
	}
	return outstanding(ledger)
}

func RatioBasisPoints(value, limit uint64) uint64 { return ratioBasisPoints(value, limit) }

func warningReason(dimension Dimension) ReasonCode {
	switch dimension {
	case DimensionWallTime:
		return ReasonWallTimeSoft
	case DimensionTokens:
		return ReasonTokensSoft
	case DimensionTurns:
		return ReasonTurnsSoft
	case DimensionCost:
		return ReasonCostSoft
	default:
		return ReasonLedgerInvalid
	}
}

func hardReason(dimension Dimension) ReasonCode {
	switch dimension {
	case DimensionWallTime:
		return ReasonWallTimeHard
	case DimensionTokens:
		return ReasonTokensHard
	case DimensionTurns:
		return ReasonTurnsHard
	case DimensionCost:
		return ReasonCostHard
	default:
		return ReasonLedgerInvalid
	}
}

func dimensionLimit(policy Policy, dimension Dimension) (uint64, bool) {
	switch dimension {
	case DimensionWallTime:
		return policy.WallTimeLimitMilliseconds, true
	case DimensionTokens:
		return policy.TokenLimit, true
	case DimensionTurns:
		return policy.TurnLimit, true
	case DimensionCost:
		return policy.CostLimitMicrousd, policy.CostLimitMicrousd > 0
	default:
		return 0, false
	}
}

func warningIndex(ledger Ledger, dimension Dimension) int {
	return slices.IndexFunc(ledger.Warnings, func(warning Warning) bool {
		return warning.Dimension == dimension && warning.PolicyRevision == ledger.Policy.Revision
	})
}

func warningStableID(warning Warning) string {
	warning.ID = ""
	warning.Acknowledged = false
	warning.AcknowledgedBy = ""
	warning.AcknowledgedAtMillis = 0
	return "budget-warning-" + observationHash(warning)[:32]
}

func decideDimensions(ledger Ledger, proposed Demand, nowMillis int64) (Ledger, Decision) {
	outstandingDemand, _, ok := outstanding(ledger)
	if !ok {
		return ledger, Decision{Disposition: DispositionFailClosed, Reason: ReasonUsageOverflow}
	}
	fixtures := []struct {
		dimension Dimension
		used      uint64
		reserved  uint64
		requested uint64
		limit     uint64
	}{
		{DimensionWallTime, ledger.Consumption.WallTimeMilliseconds, outstandingDemand.WallTimeMilliseconds, proposed.WallTimeMilliseconds, ledger.Policy.WallTimeLimitMilliseconds},
		{DimensionTokens, ledger.Consumption.Tokens, outstandingDemand.Tokens, proposed.Tokens, ledger.Policy.TokenLimit},
		{DimensionTurns, ledger.Consumption.Turns, outstandingDemand.Turns, proposed.Turns, ledger.Policy.TurnLimit},
	}
	if ledger.Policy.CostLimitMicrousd > 0 {
		fixtures = append(fixtures, struct {
			dimension Dimension
			used      uint64
			reserved  uint64
			requested uint64
			limit     uint64
		}{DimensionCost, ledger.Consumption.CostMicrousd, outstandingDemand.CostMicrousd, proposed.CostMicrousd, ledger.Policy.CostLimitMicrousd})
	}
	for _, fixture := range fixtures {
		measured, sumOK := add(fixture.used, fixture.reserved, fixture.requested)
		if !sumOK {
			return ledger, Decision{Disposition: DispositionFailClosed, Reason: ReasonUsageOverflow, Dimension: fixture.dimension}
		}
		if measured >= fixture.limit {
			return ledger, Decision{
				Disposition: DispositionHardExhausted, Reason: hardReason(fixture.dimension),
				Dimension: fixture.dimension, Measured: measured, Limit: fixture.limit,
				RatioBasisPoints: ratioBasisPoints(measured, fixture.limit),
			}
		}
		if measured >= softThreshold(fixture.limit) {
			index := warningIndex(ledger, fixture.dimension)
			if index >= 0 && ledger.Warnings[index].Acknowledged {
				continue
			}
			if index < 0 {
				if len(ledger.Warnings) >= MaximumWarnings {
					return ledger, Decision{Disposition: DispositionFailClosed, Reason: ReasonLedgerInvalid}
				}
				warning := Warning{
					Dimension: fixture.dimension, PolicyRevision: ledger.Policy.Revision,
					Measured: measured, Limit: fixture.limit,
					RatioBasisPoints: ratioBasisPoints(measured, fixture.limit), ObservedAtMillis: nowMillis,
				}
				warning.ID = warningStableID(warning)
				ledger.Warnings = append(ledger.Warnings, warning)
				index = len(ledger.Warnings) - 1
			}
			warning := ledger.Warnings[index]
			return ledger, Decision{
				Disposition: DispositionSoftPause, Reason: warningReason(fixture.dimension),
				Dimension: fixture.dimension, Measured: measured, Limit: fixture.limit,
				RatioBasisPoints: ratioBasisPoints(measured, fixture.limit), WarningID: warning.ID,
			}
		}
	}
	return ledger, Decision{Disposition: DispositionAllow}
}

func advanceClock(ledger Ledger, nowMillis int64) (Ledger, Decision) {
	if nowMillis < ledger.LastObservedAtMillis || nowMillis < ledger.StartedAtMillis {
		return ledger, Decision{Disposition: DispositionFailClosed, Reason: ReasonClockRegressed, Dimension: DimensionWallTime}
	}
	ledger.LastObservedAtMillis = nowMillis
	ledger.Consumption.WallTimeMilliseconds = uint64(nowMillis - ledger.StartedAtMillis)
	return ledger, Decision{Disposition: DispositionAllow}
}

// Evaluate advances only TaskStore wall time and deterministically applies the
// soft/hard boundary. It performs no I/O and does not reserve work.
func Evaluate(input Ledger, nowMillis int64) (Ledger, Decision) {
	ledger := clone(input)
	if !ValidLedger(ledger) {
		return ledger, Decision{Disposition: DispositionFailClosed, Reason: ReasonLedgerInvalid}
	}
	var clock Decision
	ledger, clock = advanceClock(ledger, nowMillis)
	if clock.Disposition != DispositionAllow {
		return ledger, clock
	}
	switch ledger.TelemetryState {
	case UsageUnavailable:
		return ledger, Decision{Disposition: DispositionFailClosed, Reason: ReasonProviderUsageUnavailable}
	case UsageAmbiguous:
		return ledger, Decision{Disposition: DispositionFailClosed, Reason: ReasonProviderUsageAmbiguous}
	case UsageCurrent:
	default:
		return ledger, Decision{Disposition: DispositionFailClosed, Reason: ReasonLedgerInvalid}
	}
	return decideDimensions(ledger, Demand{}, nowMillis)
}

func countDecision(ledger Ledger, request ReserveRequest) Decision {
	_, reserved, ok := outstanding(ledger)
	if !ok {
		return Decision{Disposition: DispositionFailClosed, Reason: ReasonUsageOverflow}
	}
	switch request.Activity {
	case ActivityCorrectionTurn:
		if ledger.Consumption.CorrectionAttempts+reserved.CorrectionAttempts >= ledger.Policy.CorrectionLimit {
			return Decision{Disposition: DispositionHardExhausted, Reason: ReasonCorrectionHard}
		}
	case ActivityValidationCycle:
		if ledger.Consumption.CICycles+reserved.CICycles >= ledger.Policy.CICycleLimit {
			return Decision{Disposition: DispositionHardExhausted, Reason: ReasonCIHard}
		}
	case ActivityReplacement:
		if ledger.Consumption.ReplacementAttempts+reserved.ReplacementAttempts >= ledger.Policy.ReplacementLimit {
			return Decision{Disposition: DispositionHardExhausted, Reason: ReasonReplacementHard}
		}
	case ActivitySetupAttempt:
		if ledger.Consumption.SetupAttempts+reserved.SetupAttempts >= ledger.Policy.SetupAttemptLimit {
			return Decision{Disposition: DispositionHardExhausted, Reason: ReasonSetupHard}
		}
	}
	if request.Activity == ActivityValidationCycle && request.CandidateSHA != "" {
		for _, activity := range ledger.Activities {
			if activity.Activity == ActivityValidationCycle && activity.CandidateSHA == request.CandidateSHA {
				return Decision{Disposition: DispositionHardExhausted, Reason: ReasonRepeatedValidation}
			}
		}
	}
	if request.Fingerprint != "" {
		for _, activity := range ledger.Activities {
			if activity.Activity != request.Activity || activity.Fingerprint != request.Fingerprint {
				continue
			}
			switch request.Activity {
			case ActivitySetupAttempt:
				return Decision{Disposition: DispositionHardExhausted, Reason: ReasonSetupChurn}
			case ActivityCorrectionTurn:
				return Decision{Disposition: DispositionHardExhausted, Reason: ReasonCorrectionChurn}
			}
		}
	}
	return Decision{Disposition: DispositionAllow}
}

func validDemand(activity Activity, demand Demand, costLimited bool) bool {
	if modelActivity(activity) {
		return demand.WallTimeMilliseconds > 0 && demand.Tokens > 0 && demand.Turns == 1 &&
			(!costLimited || demand.CostMicrousd > 0)
	}
	return demand.Tokens == 0 && demand.Turns == 0 && demand.CostMicrousd == 0
}

func ValidTurnDemand(policy Policy, demand Demand) bool {
	return ValidPolicy(policy) && validDemand(ActivityWorkerTurn, demand, policy.CostLimitMicrousd > 0)
}

// Reserve atomically-composable policy work. Its returned ledger must be
// persisted by the caller's Run CAS before any external handoff.
func Reserve(input Ledger, request ReserveRequest, nowMillis int64) (Ledger, Decision, error) {
	ledger := clone(input)
	if !ValidLedger(ledger) || !validIdentity(request.ID) || !validIdentity(request.EffectID) ||
		!validActivity(request.Activity) || request.LeaseEpoch == 0 ||
		request.PolicyRevision != ledger.Policy.Revision ||
		!validDemand(request.Activity, request.Demand, ledger.Policy.CostLimitMicrousd > 0) ||
		!validOptionalCandidate(request.CandidateSHA) || !validOptionalFingerprint(request.Fingerprint) {
		return input, Decision{}, ErrInvalid
	}
	if index := slices.IndexFunc(ledger.Reservations, func(current Reservation) bool { return current.ID == request.ID }); index >= 0 {
		existing := ledger.Reservations[index]
		expected := Reservation{
			ID: request.ID, EffectID: request.EffectID, Activity: request.Activity,
			LeaseEpoch: request.LeaseEpoch, PolicyRevision: request.PolicyRevision,
			Demand: request.Demand, CandidateSHA: request.CandidateSHA, Fingerprint: request.Fingerprint,
			Released: existing.Released, EvidenceID: existing.EvidenceID,
		}
		if existing != expected {
			return input, Decision{}, ErrInvalid
		}
		return ledger, Decision{Disposition: DispositionAllow}, nil
	}
	if len(ledger.Reservations) >= MaximumReservations {
		return input, Decision{Disposition: DispositionFailClosed, Reason: ReasonLedgerInvalid}, nil
	}
	var clock Decision
	ledger, clock = advanceClock(ledger, nowMillis)
	if clock.Disposition != DispositionAllow {
		return ledger, clock, nil
	}
	switch ledger.TelemetryState {
	case UsageUnavailable:
		return ledger, Decision{Disposition: DispositionFailClosed, Reason: ReasonProviderUsageUnavailable}, nil
	case UsageAmbiguous:
		return ledger, Decision{Disposition: DispositionFailClosed, Reason: ReasonProviderUsageAmbiguous}, nil
	case UsageCurrent:
	default:
		return ledger, Decision{Disposition: DispositionFailClosed, Reason: ReasonLedgerInvalid}, nil
	}
	if count := countDecision(ledger, request); count.Disposition != DispositionAllow {
		return ledger, count, nil
	}
	var decision Decision
	ledger, decision = decideDimensions(ledger, request.Demand, nowMillis)
	if decision.Disposition != DispositionAllow {
		return ledger, decision, nil
	}
	ledger.Reservations = append(ledger.Reservations, Reservation{
		ID: request.ID, EffectID: request.EffectID, Activity: request.Activity,
		LeaseEpoch: request.LeaseEpoch, PolicyRevision: request.PolicyRevision,
		Demand: request.Demand, CandidateSHA: request.CandidateSHA, Fingerprint: request.Fingerprint,
	})
	return ledger, decision, nil
}

func sourceCursor(ledger Ledger, agentID string) (int, uint64) {
	index := slices.IndexFunc(ledger.SourceCursors, func(cursor SourceCursor) bool { return cursor.AgentID == agentID })
	if index < 0 {
		return -1, 0
	}
	return index, ledger.SourceCursors[index].Sequence
}

func releaseReservation(ledger *Ledger, effectID, evidenceID string) bool {
	index := slices.IndexFunc(ledger.Reservations, func(reservation Reservation) bool {
		return reservation.EffectID == effectID && !reservation.Released
	})
	if index < 0 {
		return false
	}
	ledger.Reservations[index].Released = true
	ledger.Reservations[index].EvidenceID = evidenceID
	return true
}

// ReleaseAbsentReservation releases one reservation only after authoritative
// evidence proves the matching attempt never handed work to a provider or
// setup process. It consumes no usage or count budget.
func ReleaseAbsentReservation(input Ledger, effectID string, leaseEpoch uint64, evidenceID string) (Ledger, error) {
	ledger := clone(input)
	if !ValidLedger(ledger) || !validIdentity(effectID) || leaseEpoch == 0 ||
		!validIdentity(evidenceID) || !strings.HasPrefix(evidenceID, "budget-absence-") {
		return input, ErrInvalid
	}
	index := -1
	for current := range ledger.Reservations {
		reservation := ledger.Reservations[current]
		if reservation.EffectID == effectID && !reservation.Released {
			if reservation.LeaseEpoch != leaseEpoch {
				return input, ErrInvalid
			}
			if index >= 0 {
				return input, ErrInvalid
			}
			index = current
		}
	}
	if index < 0 {
		return input, ErrInvalid
	}
	ledger.Reservations[index].Released = true
	ledger.Reservations[index].EvidenceID = evidenceID
	return ledger, nil
}

// ApplyProviderObservation consumes one exact per-turn public-provider usage
// fact. Cached input is retained for visibility but not added again because it
// is a subset of input tokens in Paseo's AgentUsage contract.
func ApplyProviderObservation(input Ledger, observation ProviderObservation) (Ledger, Decision, error) {
	ledger := clone(input)
	if !ValidLedger(ledger) || !validIdentity(observation.ID) || !validIdentity(observation.EffectID) ||
		!validIdentity(observation.AgentID) || !validActivity(observation.Activity) ||
		!modelActivity(observation.Activity) || observation.LeaseEpoch == 0 || observation.Sequence == 0 ||
		!slices.Contains([]UsageState{UsageCurrent, UsageUnavailable, UsageAmbiguous}, observation.State) ||
		observation.ObservedAtMillis < 0 || observation.PolicyRevision != ledger.Policy.Revision ||
		!validProviderUsage(observation.ProviderUsage) ||
		observation.FactHash == "" || observation.FactHash != ProviderObservationHash(observation) {
		return input, Decision{}, ErrInvalid
	}
	if index := slices.IndexFunc(ledger.ProviderSnapshots, func(current ProviderObservation) bool { return current.ID == observation.ID }); index >= 0 {
		if ledger.ProviderSnapshots[index] != observation {
			return input, Decision{}, ErrInvalid
		}
		return ledger, Decision{Disposition: DispositionAllow}, nil
	}
	if len(ledger.ProviderSnapshots) >= MaximumSnapshots {
		return input, Decision{Disposition: DispositionFailClosed, Reason: ReasonLedgerInvalid}, nil
	}
	priorIncomplete := false
	for _, current := range ledger.ProviderSnapshots {
		if current.EffectID != observation.EffectID {
			continue
		}
		complete := current.State == UsageCurrent && current.InputTokensPresent && current.OutputTokensPresent &&
			(ledger.Policy.CostLimitMicrousd == 0 || current.CostMicrousdPresent)
		if complete {
			ledger.TelemetryState = UsageAmbiguous
			return ledger, Decision{Disposition: DispositionFailClosed, Reason: ReasonProviderUsageAmbiguous}, nil
		}
		priorIncomplete = true
	}
	cursorIndex, cursor := sourceCursor(ledger, observation.AgentID)
	if observation.Sequence <= cursor {
		ledger.TelemetryState = UsageAmbiguous
		return ledger, Decision{Disposition: DispositionFailClosed, Reason: ReasonProviderUsageAmbiguous}, nil
	}
	complete := observation.State == UsageCurrent && observation.InputTokensPresent && observation.OutputTokensPresent &&
		(ledger.Policy.CostLimitMicrousd == 0 || observation.CostMicrousdPresent)
	if complete && (!validIdentity(observation.SourceRevision) ||
		(cursorIndex >= 0 && ledger.SourceCursors[cursorIndex].LastSourceRevision == observation.SourceRevision)) {
		ledger.TelemetryState = UsageAmbiguous
		return ledger, Decision{Disposition: DispositionFailClosed, Reason: ReasonProviderUsageAmbiguous}, nil
	}
	if priorIncomplete && !complete {
		ledger.TelemetryState = UsageUnavailable
		reason := ReasonProviderUsageUnavailable
		if observation.State == UsageAmbiguous {
			ledger.TelemetryState = UsageAmbiguous
			reason = ReasonProviderUsageAmbiguous
		}
		return ledger, Decision{Disposition: DispositionFailClosed, Reason: reason}, nil
	}
	ledger.ProviderSnapshots = append(ledger.ProviderSnapshots, observation)
	if cursorIndex < 0 {
		ledger.SourceCursors = append(ledger.SourceCursors, SourceCursor{AgentID: observation.AgentID, Sequence: observation.Sequence})
	} else {
		ledger.SourceCursors[cursorIndex].Sequence = observation.Sequence
	}
	if !complete {
		ledger.TelemetryState = UsageUnavailable
		reason := ReasonProviderUsageUnavailable
		if observation.State == UsageAmbiguous {
			ledger.TelemetryState = UsageAmbiguous
			reason = ReasonProviderUsageAmbiguous
		}
		return ledger, Decision{Disposition: DispositionFailClosed, Reason: reason}, nil
	}
	if cursorIndex < 0 {
		ledger.SourceCursors[len(ledger.SourceCursors)-1].LastSourceRevision = observation.SourceRevision
	} else {
		ledger.SourceCursors[cursorIndex].LastSourceRevision = observation.SourceRevision
	}
	tokens, ok := add(observation.InputTokens, observation.OutputTokens)
	if !ok {
		ledger.TelemetryState = UsageAmbiguous
		return ledger, Decision{Disposition: DispositionFailClosed, Reason: ReasonUsageOverflow, Dimension: DimensionTokens}, nil
	}
	ledger.Consumption.Tokens, ok = add(ledger.Consumption.Tokens, tokens)
	if !ok {
		ledger.TelemetryState = UsageAmbiguous
		return ledger, Decision{Disposition: DispositionFailClosed, Reason: ReasonUsageOverflow, Dimension: DimensionTokens}, nil
	}
	ledger.Consumption.Turns, ok = add(ledger.Consumption.Turns, 1)
	if !ok {
		ledger.TelemetryState = UsageAmbiguous
		return ledger, Decision{Disposition: DispositionFailClosed, Reason: ReasonUsageOverflow, Dimension: DimensionTurns}, nil
	}
	if observation.CostMicrousdPresent {
		ledger.Consumption.CostMicrousd, ok = add(ledger.Consumption.CostMicrousd, observation.CostMicrousd)
		if !ok {
			ledger.TelemetryState = UsageAmbiguous
			return ledger, Decision{Disposition: DispositionFailClosed, Reason: ReasonUsageOverflow, Dimension: DimensionCost}, nil
		}
	}
	if !releaseReservation(&ledger, observation.EffectID, observation.ID) {
		ledger.TelemetryState = UsageAmbiguous
		return ledger, Decision{Disposition: DispositionFailClosed, Reason: ReasonProviderUsageAmbiguous}, nil
	}
	if observation.Activity == ActivityCorrectionTurn {
		ledger.Consumption.CorrectionAttempts++
	}
	ledger.TelemetryState = UsageCurrent
	next, decision := Evaluate(ledger, observation.ObservedAtMillis)
	return next, decision, nil
}

// ApplyActivityObservation consumes a non-provider setup, validation, or
// replacement reservation. Correction turns use provider observations.
func ApplyActivityObservation(input Ledger, observation ActivityObservation) (Ledger, Decision, error) {
	ledger := clone(input)
	if !ValidLedger(ledger) || !validIdentity(observation.ID) || !validIdentity(observation.ReservationID) ||
		!validIdentity(observation.EffectID) || !validActivity(observation.Activity) || modelActivity(observation.Activity) ||
		observation.LeaseEpoch == 0 || observation.PolicyRevision != ledger.Policy.Revision ||
		observation.ObservedAtMillis < 0 || observation.FactHash == "" ||
		observation.FactHash != ActivityObservationHash(observation) ||
		!validOptionalCandidate(observation.CandidateSHA) || !validOptionalFingerprint(observation.Fingerprint) {
		return input, Decision{}, ErrInvalid
	}
	if index := slices.IndexFunc(ledger.Activities, func(current ActivityObservation) bool { return current.ID == observation.ID }); index >= 0 {
		if ledger.Activities[index] != observation {
			return input, Decision{}, ErrInvalid
		}
		return ledger, Decision{Disposition: DispositionAllow}, nil
	}
	if len(ledger.Activities) >= MaximumActivities {
		return input, Decision{Disposition: DispositionFailClosed, Reason: ReasonLedgerInvalid}, nil
	}
	reservationIndex := slices.IndexFunc(ledger.Reservations, func(reservation Reservation) bool {
		return reservation.ID == observation.ReservationID && !reservation.Released
	})
	if reservationIndex < 0 {
		return input, Decision{}, ErrInvalid
	}
	reservation := ledger.Reservations[reservationIndex]
	if reservation.EffectID != observation.EffectID || reservation.Activity != observation.Activity ||
		reservation.LeaseEpoch != observation.LeaseEpoch || reservation.PolicyRevision != observation.PolicyRevision ||
		reservation.CandidateSHA != observation.CandidateSHA || reservation.Fingerprint != observation.Fingerprint {
		return input, Decision{}, ErrInvalid
	}
	ledger.Reservations[reservationIndex].Released = true
	ledger.Reservations[reservationIndex].EvidenceID = observation.ID
	ledger.Activities = append(ledger.Activities, observation)
	switch observation.Activity {
	case ActivitySetupAttempt:
		ledger.Consumption.SetupAttempts++
	case ActivityValidationCycle:
		ledger.Consumption.CICycles++
	case ActivityReplacement:
		ledger.Consumption.ReplacementAttempts++
	}
	next, decision := Evaluate(ledger, observation.ObservedAtMillis)
	return next, decision, nil
}

// AcknowledgeSoftWarning records only an authenticated human acknowledgement
// for the exact unchanged policy revision. It never raises a limit.
func AcknowledgeSoftWarning(input Ledger, warningID, policyRevision, actorID string, nowMillis int64) (Ledger, error) {
	ledger := clone(input)
	if !ValidLedger(ledger) || !validIdentity(warningID) || policyRevision != ledger.Policy.Revision ||
		!validIdentity(actorID) || nowMillis < ledger.LastObservedAtMillis {
		return input, ErrInvalid
	}
	index := slices.IndexFunc(ledger.Warnings, func(warning Warning) bool {
		return warning.ID == warningID && warning.PolicyRevision == policyRevision
	})
	if index < 0 {
		return input, ErrInvalid
	}
	if ledger.Warnings[index].Acknowledged {
		if ledger.Warnings[index].AcknowledgedBy != actorID {
			return input, ErrInvalid
		}
		return ledger, nil
	}
	ledger.Warnings[index].Acknowledged = true
	ledger.Warnings[index].AcknowledgedBy = actorID
	ledger.Warnings[index].AcknowledgedAtMillis = nowMillis
	ledger.LastObservedAtMillis = nowMillis
	ledger.Consumption.WallTimeMilliseconds = uint64(nowMillis - ledger.StartedAtMillis)
	return ledger, nil
}

// ValidLedger re-derives bounded identities, monotonic cursors, immutable
// bindings, consumption totals, and warning revisions for restart recovery.
func ValidLedger(ledger Ledger) bool {
	if ledger.SchemaVersion != SchemaVersion || !ValidPolicy(ledger.Policy) ||
		ledger.StartedAtMillis < 0 || ledger.LastObservedAtMillis < ledger.StartedAtMillis ||
		ledger.Consumption.WallTimeMilliseconds != uint64(ledger.LastObservedAtMillis-ledger.StartedAtMillis) ||
		!slices.Contains([]UsageState{UsageCurrent, UsageUnavailable, UsageAmbiguous}, ledger.TelemetryState) ||
		len(ledger.Reservations) > MaximumReservations || len(ledger.ProviderSnapshots) > MaximumSnapshots ||
		len(ledger.Activities) > MaximumActivities || len(ledger.Warnings) > MaximumWarnings {
		return false
	}
	reservationIDs := make(map[string]struct{}, len(ledger.Reservations))
	for _, reservation := range ledger.Reservations {
		if !validIdentity(reservation.ID) || !validIdentity(reservation.EffectID) || !validActivity(reservation.Activity) ||
			reservation.LeaseEpoch == 0 || reservation.PolicyRevision != ledger.Policy.Revision ||
			!validDemand(reservation.Activity, reservation.Demand, ledger.Policy.CostLimitMicrousd > 0) ||
			(reservation.Released != (reservation.EvidenceID != "")) ||
			!validOptionalCandidate(reservation.CandidateSHA) || !validOptionalFingerprint(reservation.Fingerprint) {
			return false
		}
		if _, duplicate := reservationIDs[reservation.ID]; duplicate {
			return false
		}
		reservationIDs[reservation.ID] = struct{}{}
	}
	snapshotIDs := make(map[string]struct{}, len(ledger.ProviderSnapshots))
	sourceRevisions := make(map[string]struct{}, len(ledger.ProviderSnapshots))
	latestSources := make(map[string]SourceCursor, len(ledger.SourceCursors))
	var tokens, turns, cost uint64
	var corrections uint32
	var ok bool
	for _, snapshot := range ledger.ProviderSnapshots {
		if !validIdentity(snapshot.ID) || !validIdentity(snapshot.EffectID) || !validIdentity(snapshot.AgentID) ||
			!modelActivity(snapshot.Activity) || snapshot.LeaseEpoch == 0 || snapshot.PolicyRevision != ledger.Policy.Revision ||
			!slices.Contains([]UsageState{UsageCurrent, UsageUnavailable, UsageAmbiguous}, snapshot.State) ||
			!validProviderUsage(snapshot.ProviderUsage) ||
			snapshot.Sequence == 0 || snapshot.ObservedAtMillis < 0 || snapshot.FactHash != ProviderObservationHash(snapshot) {
			return false
		}
		if _, duplicate := snapshotIDs[snapshot.ID]; duplicate {
			return false
		}
		snapshotIDs[snapshot.ID] = struct{}{}
		if snapshot.State != UsageCurrent || !snapshot.InputTokensPresent || !snapshot.OutputTokensPresent ||
			(ledger.Policy.CostLimitMicrousd > 0 && !snapshot.CostMicrousdPresent) {
			continue
		}
		if !validIdentity(snapshot.SourceRevision) {
			return false
		}
		sourceKey := snapshot.AgentID + "\x1f" + snapshot.SourceRevision
		if _, duplicate := sourceRevisions[sourceKey]; duplicate {
			return false
		}
		sourceRevisions[sourceKey] = struct{}{}
		if current := latestSources[snapshot.AgentID]; snapshot.Sequence > current.Sequence {
			latestSources[snapshot.AgentID] = SourceCursor{
				AgentID: snapshot.AgentID, Sequence: snapshot.Sequence, LastSourceRevision: snapshot.SourceRevision,
			}
		}
		turnTokens, valid := add(snapshot.InputTokens, snapshot.OutputTokens)
		if !valid {
			return false
		}
		if tokens, ok = add(tokens, turnTokens); !ok {
			return false
		}
		if turns, ok = add(turns, 1); !ok {
			return false
		}
		if snapshot.CostMicrousdPresent {
			if cost, ok = add(cost, snapshot.CostMicrousd); !ok {
				return false
			}
		}
		if snapshot.Activity == ActivityCorrectionTurn {
			corrections++
		}
	}
	if ledger.Consumption.Tokens != tokens || ledger.Consumption.Turns != turns || ledger.Consumption.CostMicrousd != cost ||
		ledger.Consumption.CorrectionAttempts != corrections {
		return false
	}
	activityIDs := make(map[string]struct{}, len(ledger.Activities))
	var setup, ci, replacement uint32
	for _, activity := range ledger.Activities {
		if !validIdentity(activity.ID) || !validIdentity(activity.ReservationID) || !validIdentity(activity.EffectID) ||
			modelActivity(activity.Activity) || !validActivity(activity.Activity) || activity.LeaseEpoch == 0 ||
			activity.PolicyRevision != ledger.Policy.Revision || activity.ObservedAtMillis < 0 ||
			activity.FactHash != ActivityObservationHash(activity) ||
			!validOptionalCandidate(activity.CandidateSHA) || !validOptionalFingerprint(activity.Fingerprint) {
			return false
		}
		if _, duplicate := activityIDs[activity.ID]; duplicate {
			return false
		}
		activityIDs[activity.ID] = struct{}{}
		switch activity.Activity {
		case ActivitySetupAttempt:
			setup++
		case ActivityValidationCycle:
			ci++
		case ActivityReplacement:
			replacement++
		}
	}
	if ledger.Consumption.SetupAttempts != setup || ledger.Consumption.CICycles != ci ||
		ledger.Consumption.ReplacementAttempts != replacement {
		return false
	}
	cursors := make(map[string]SourceCursor, len(ledger.SourceCursors))
	for _, cursor := range ledger.SourceCursors {
		if !validIdentity(cursor.AgentID) || cursor.Sequence == 0 ||
			(cursor.LastSourceRevision != "" && !validIdentity(cursor.LastSourceRevision)) {
			return false
		}
		if _, duplicate := cursors[cursor.AgentID]; duplicate {
			return false
		}
		cursors[cursor.AgentID] = cursor
	}
	for _, snapshot := range ledger.ProviderSnapshots {
		if cursors[snapshot.AgentID].Sequence < snapshot.Sequence {
			return false
		}
	}
	for agentID, latest := range latestSources {
		if cursors[agentID].LastSourceRevision != latest.LastSourceRevision {
			return false
		}
	}
	for _, reservation := range ledger.Reservations {
		if !reservation.Released {
			continue
		}
		_, providerEvidence := snapshotIDs[reservation.EvidenceID]
		_, activityEvidence := activityIDs[reservation.EvidenceID]
		if !providerEvidence && !activityEvidence && !strings.HasPrefix(reservation.EvidenceID, "budget-absence-") {
			return false
		}
	}
	warnings := make(map[string]struct{}, len(ledger.Warnings))
	for _, warning := range ledger.Warnings {
		limit, enabled := dimensionLimit(ledger.Policy, warning.Dimension)
		if !validIdentity(warning.ID) || !validDimension(warning.Dimension) ||
			!enabled || warning.PolicyRevision != ledger.Policy.Revision || warning.Limit != limit ||
			warning.Measured < softThreshold(limit) || warning.Measured >= limit ||
			warning.ID != warningStableID(warning) ||
			warning.RatioBasisPoints != ratioBasisPoints(warning.Measured, warning.Limit) ||
			warning.ObservedAtMillis < ledger.StartedAtMillis ||
			(warning.Acknowledged != (warning.AcknowledgedBy != "" && warning.AcknowledgedAtMillis >= warning.ObservedAtMillis)) {
			return false
		}
		key := string(warning.Dimension) + "\x1f" + warning.PolicyRevision
		if _, duplicate := warnings[key]; duplicate {
			return false
		}
		warnings[key] = struct{}{}
	}
	return true
}

// ValidLedgerForLease additionally fences every outstanding reservation to
// the current Project execution epoch. Released historical evidence retains
// its original epoch across a valid takeover.
func ValidLedgerForLease(ledger Ledger, leaseEpoch uint64) bool {
	if !ValidLedger(ledger) || leaseEpoch == 0 {
		return false
	}
	for _, reservation := range ledger.Reservations {
		if !reservation.Released && reservation.LeaseEpoch != leaseEpoch {
			return false
		}
	}
	return true
}
