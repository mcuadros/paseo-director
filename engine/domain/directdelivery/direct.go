// SPDX-License-Identifier: Apache-2.0

// Package directdelivery owns the pure immutable contract for integrating an
// exact Candidate directly into one authorized remote target ref. It contains
// no Git, TaskStore, clock, connector, or retry side effect.
package directdelivery

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"regexp"
	"slices"
	"strings"
)

const (
	PolicySchemaVersion        = "director.direct-delivery-policy/v1"
	BindingSchemaVersion       = "director.direct-delivery-binding/v1"
	AuthorizationSchemaVersion = "director.direct-delivery-authorization/v1"
	ObservationSchemaVersion   = "director.direct-delivery-observation/v1"
	EvidenceSchemaVersion      = "director.direct-delivery-evidence/v1"
	StateSchemaVersion         = "director.direct-delivery-state/v1"
	MaximumObservationAgeMS    = int64(30_000)
	MaximumAttempts            = uint32(3)
	MaximumHistoricalStates    = 64
	IntegrationEffectKind      = "direct.integration"
	IntegrationEffectClass     = "conditional_update"
)

var (
	identifierPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.:@/-]{0,255}$`)
	uuidPattern       = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[1-8][0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`)
	digestPattern     = regexp.MustCompile(`^[0-9a-f]{64}$`)
	gitOIDPattern     = regexp.MustCompile(`^(?:[0-9a-f]{40}|[0-9a-f]{64})$`)
	secretPattern     = regexp.MustCompile(`(?i)(?:-----BEGIN [A-Z0-9 ]*PRIVATE KEY-----|github_pat_[A-Za-z0-9_]{16,}|gh[pousr]_[A-Za-z0-9]{16,}|sk-[A-Za-z0-9_-]{16,}|(?:password|secret|token|credential|authorization)\s*[:=]\s*\S+)`)
)

func digest(value any) string {
	encoded, err := json.Marshal(value)
	if err != nil {
		panic("marshal fixed direct-delivery value: " + err.Error())
	}
	sum := sha256.Sum256(encoded)
	return hex.EncodeToString(sum[:])
}

func validIdentity(value string) bool {
	return identifierPattern.MatchString(value) && !secretPattern.MatchString(value)
}

func validBranch(value string) bool {
	if len(value) == 0 || len(value) > 255 || value == "@" || strings.HasPrefix(value, "-") ||
		strings.HasPrefix(value, "/") || strings.HasSuffix(value, "/") || strings.HasSuffix(value, ".") ||
		strings.HasSuffix(value, ".lock") || strings.Contains(value, "..") || strings.Contains(value, "@{") ||
		strings.Contains(value, "//") || secretPattern.MatchString(value) {
		return false
	}
	return strings.IndexFunc(value, func(character rune) bool {
		return character <= ' ' || character == 0x7f || strings.ContainsRune(`~^:?*[\`, character)
	}) < 0
}

// ValidTargetRef accepts only a complete remote branch ref. Tags, shorthand,
// remote-tracking refs, option-like values, and secret-shaped input are not
// representable direct-delivery targets.
func ValidTargetRef(value string) bool {
	return strings.HasPrefix(value, "refs/heads/") && validBranch(strings.TrimPrefix(value, "refs/heads/"))
}

type IntegrationMode string

const (
	IntegrationManual    IntegrationMode = "manual"
	IntegrationAutomatic IntegrationMode = "automatic"
)

type SelectionSource string

const SelectionFrozenRunConfiguration SelectionSource = "frozen_run_configuration"

// Policy is the exact frozen security-envelope decision. The only admitted
// selection source deliberately has no PR-failure or adapter-fallback value.
type Policy struct {
	SchemaVersion        string          `json:"schemaVersion"`
	DeliveryMode         string          `json:"deliveryMode"`
	IntegrationMode      IntegrationMode `json:"integrationMode"`
	SelectionSource      SelectionSource `json:"selectionSource"`
	ConfigurationSHA256  string          `json:"configurationSha256"`
	AuthorizedTargetRefs []string        `json:"authorizedTargetRefs"`
	AutomaticTargetRefs  []string        `json:"automaticTargetRefs"`
	AttemptLimit         uint32          `json:"attemptLimit"`
	SHA256               string          `json:"sha256"`
}

func policyValue(value Policy) Policy {
	value.SHA256 = ""
	value.AuthorizedTargetRefs = slices.Clone(value.AuthorizedTargetRefs)
	value.AutomaticTargetRefs = slices.Clone(value.AutomaticTargetRefs)
	return value
}

func PolicySHA256(value Policy) string { return digest(policyValue(value)) }

func SealPolicy(value Policy) Policy {
	value.SchemaVersion = PolicySchemaVersion
	value.SHA256 = PolicySHA256(value)
	return value
}

func sortedUniqueRefs(values []string) bool {
	if !slices.IsSorted(values) {
		return false
	}
	for index, value := range values {
		if !ValidTargetRef(value) || index > 0 && values[index-1] == value {
			return false
		}
	}
	return true
}

func ValidPolicy(value Policy) bool {
	if value.SchemaVersion != PolicySchemaVersion || value.DeliveryMode != "direct" ||
		value.SelectionSource != SelectionFrozenRunConfiguration ||
		(value.IntegrationMode != IntegrationManual && value.IntegrationMode != IntegrationAutomatic) ||
		!digestPattern.MatchString(value.ConfigurationSHA256) || len(value.AuthorizedTargetRefs) == 0 ||
		len(value.AuthorizedTargetRefs) > 64 || len(value.AutomaticTargetRefs) > len(value.AuthorizedTargetRefs) ||
		!sortedUniqueRefs(value.AuthorizedTargetRefs) || !sortedUniqueRefs(value.AutomaticTargetRefs) ||
		value.AttemptLimit == 0 || value.AttemptLimit > MaximumAttempts ||
		!digestPattern.MatchString(value.SHA256) || value.SHA256 != PolicySHA256(value) {
		return false
	}
	for _, target := range value.AutomaticTargetRefs {
		if _, present := slices.BinarySearch(value.AuthorizedTargetRefs, target); !present {
			return false
		}
	}
	return true
}

func TargetAuthorized(policy Policy, target string) bool {
	_, present := slices.BinarySearch(policy.AuthorizedTargetRefs, target)
	return ValidPolicy(policy) && present
}

func AutomaticTargetAuthorized(policy Policy, target string) bool {
	_, present := slices.BinarySearch(policy.AutomaticTargetRefs, target)
	return ValidPolicy(policy) && policy.IntegrationMode == IntegrationAutomatic && present
}

// Binding freezes every Candidate, quality, policy, repository, ref, and
// lease fact which can invalidate the direct integration authority.
type Binding struct {
	SchemaVersion           string `json:"schemaVersion"`
	TaskID                  string `json:"taskId"`
	RunID                   string `json:"runId"`
	CandidateID             string `json:"candidateId"`
	CandidateSHA            string `json:"candidateSha"`
	BaseSHA                 string `json:"baseSha"`
	TreeSHA                 string `json:"treeSha"`
	ManifestSHA256          string `json:"manifestSha256"`
	CandidateGeneration     uint64 `json:"candidateGeneration"`
	TaskVersion             uint64 `json:"taskVersion"`
	ConfigurationSHA256     string `json:"configurationSha256"`
	RepositoryID            string `json:"repositoryId"`
	RepositoryBindingSHA256 string `json:"repositoryBindingSha256"`
	TargetRef               string `json:"targetRef"`
	PolicySHA256            string `json:"policySha256"`
	ReviewEvidenceID        string `json:"reviewEvidenceId"`
	ReviewerUUID            string `json:"reviewerUuid"`
	CIObservationID         string `json:"ciObservationId"`
	CIObservationSHA256     string `json:"ciObservationSha256"`
	LeaseEpoch              uint64 `json:"leaseEpoch"`
	SHA256                  string `json:"sha256"`
}

func bindingValue(value Binding) Binding { value.SHA256 = ""; return value }
func BindingSHA256(value Binding) string { return digest(bindingValue(value)) }

func SealBinding(value Binding) Binding {
	value.SchemaVersion = BindingSchemaVersion
	value.SHA256 = BindingSHA256(value)
	return value
}

func ValidBinding(value Binding) bool {
	return value.SchemaVersion == BindingSchemaVersion && validIdentity(value.TaskID) && validIdentity(value.RunID) &&
		validIdentity(value.CandidateID) && gitOIDPattern.MatchString(value.CandidateSHA) &&
		gitOIDPattern.MatchString(value.BaseSHA) && gitOIDPattern.MatchString(value.TreeSHA) &&
		len(value.CandidateSHA) == len(value.BaseSHA) && len(value.CandidateSHA) == len(value.TreeSHA) &&
		value.CandidateSHA != value.BaseSHA && digestPattern.MatchString(value.ManifestSHA256) &&
		value.CandidateGeneration > 0 && value.TaskVersion > 0 && digestPattern.MatchString(value.ConfigurationSHA256) &&
		validIdentity(value.RepositoryID) && digestPattern.MatchString(value.RepositoryBindingSHA256) &&
		ValidTargetRef(value.TargetRef) && digestPattern.MatchString(value.PolicySHA256) &&
		validIdentity(value.ReviewEvidenceID) && uuidPattern.MatchString(value.ReviewerUUID) &&
		validIdentity(value.CIObservationID) && digestPattern.MatchString(value.CIObservationSHA256) &&
		value.LeaseEpoch > 0 && digestPattern.MatchString(value.SHA256) && value.SHA256 == BindingSHA256(value)
}

// HumanAuthorization is the server-attributed explicit action required by a
// manual direct policy. It carries no prose, credential, path, or remote URL.
type HumanAuthorization struct {
	SchemaVersion      string `json:"schemaVersion"`
	ID                 string `json:"id"`
	ActorKind          string `json:"actorKind"`
	ActorID            string `json:"actorId"`
	DecisionID         string `json:"decisionId"`
	Action             string `json:"action"`
	BindingSHA256      string `json:"bindingSha256"`
	CandidateSHA       string `json:"candidateSha"`
	BaseSHA            string `json:"baseSha"`
	TargetRef          string `json:"targetRef"`
	PolicySHA256       string `json:"policySha256"`
	AuthorizedAtMillis int64  `json:"authorizedAtMillis"`
	SHA256             string `json:"sha256"`
}

func authorizationValue(value HumanAuthorization) HumanAuthorization { value.SHA256 = ""; return value }
func AuthorizationSHA256(value HumanAuthorization) string            { return digest(authorizationValue(value)) }

func SealAuthorization(value HumanAuthorization) HumanAuthorization {
	value.SchemaVersion = AuthorizationSchemaVersion
	value.SHA256 = AuthorizationSHA256(value)
	return value
}

func ValidAuthorization(value HumanAuthorization, binding Binding) bool {
	return value.SchemaVersion == AuthorizationSchemaVersion && validIdentity(value.ID) &&
		value.ActorKind == "human" && validIdentity(value.ActorID) && validIdentity(value.DecisionID) &&
		value.Action == "integrate_direct" && value.BindingSHA256 == binding.SHA256 &&
		value.CandidateSHA == binding.CandidateSHA && value.BaseSHA == binding.BaseSHA &&
		value.TargetRef == binding.TargetRef && value.PolicySHA256 == binding.PolicySHA256 &&
		value.AuthorizedAtMillis >= 0 && digestPattern.MatchString(value.SHA256) &&
		value.SHA256 == AuthorizationSHA256(value)
}

type ObservationStatus string

const (
	ObservationCurrentExpected ObservationStatus = "current_expected"
	ObservationDesired         ObservationStatus = "desired"
	ObservationAbsent          ObservationStatus = "absent"
	ObservationDifferent       ObservationStatus = "different"
	ObservationAmbiguous       ObservationStatus = "ambiguous"
	ObservationUnavailable     ObservationStatus = "unavailable"
)

type ObservationCode string

const (
	CodeOK                 ObservationCode = "DIRECT_OK"
	CodeRemoteRefAbsent    ObservationCode = "DIRECT_REMOTE_REF_ABSENT"
	CodeRemoteRefChanged   ObservationCode = "DIRECT_REMOTE_REF_CHANGED"
	CodeRemoteUnavailable  ObservationCode = "DIRECT_REMOTE_UNAVAILABLE"
	CodeRemoteAmbiguous    ObservationCode = "DIRECT_REMOTE_AMBIGUOUS"
	CodeRepositoryMismatch ObservationCode = "DIRECT_REPOSITORY_MISMATCH"
	CodeCandidateChanged   ObservationCode = "DIRECT_CANDIDATE_CHANGED"
	CodeBranchChanged      ObservationCode = "DIRECT_BRANCH_CHANGED"
	CodeUnsafeTarget       ObservationCode = "DIRECT_UNSAFE_TARGET"
)

// Observation is the bounded path/content/credential-free remote-ref fact.
// Attempt is the already durable attempt count when this fact was read.
type Observation struct {
	SchemaVersion    string            `json:"schemaVersion"`
	ID               string            `json:"id"`
	EffectID         string            `json:"effectId"`
	BindingSHA256    string            `json:"bindingSha256"`
	Attempt          uint32            `json:"attempt"`
	Status           ObservationStatus `json:"status"`
	Code             ObservationCode   `json:"code"`
	RepositoryID     string            `json:"repositoryId"`
	TargetRef        string            `json:"targetRef"`
	CurrentSHA       string            `json:"currentSha,omitempty"`
	ObservedAtMillis int64             `json:"observedAtMillis"`
	MaximumAgeMillis int64             `json:"maximumAgeMillis"`
	FactSHA256       string            `json:"factSha256"`
}

func observationValue(value Observation) Observation {
	value.ID, value.FactSHA256 = "", ""
	return value
}

func ObservationFactSHA256(value Observation) string { return digest(observationValue(value)) }

func SealObservation(value Observation) Observation {
	value.SchemaVersion = ObservationSchemaVersion
	value.FactSHA256 = ObservationFactSHA256(value)
	value.ID = "direct-observation-" + digest(struct {
		Binding, Effect, Fact string
		Attempt               uint32
	}{value.BindingSHA256, value.EffectID, value.FactSHA256, value.Attempt})[:32]
	return value
}

func validObservationShape(value Observation, binding Binding, effectID string) bool {
	if value.SchemaVersion != ObservationSchemaVersion || !validIdentity(value.ID) ||
		value.EffectID != effectID || value.BindingSHA256 != binding.SHA256 ||
		value.RepositoryID != binding.RepositoryID || value.TargetRef != binding.TargetRef ||
		value.ObservedAtMillis < 0 || value.MaximumAgeMillis != MaximumObservationAgeMS ||
		!digestPattern.MatchString(value.FactSHA256) || value.FactSHA256 != ObservationFactSHA256(value) ||
		value.ID != SealObservation(observationValue(value)).ID {
		return false
	}
	switch value.Status {
	case ObservationCurrentExpected:
		return value.Code == CodeOK && value.CurrentSHA == binding.BaseSHA
	case ObservationDesired:
		return value.Code == CodeOK && value.CurrentSHA == binding.CandidateSHA
	case ObservationAbsent:
		return value.Code == CodeRemoteRefAbsent && value.CurrentSHA == ""
	case ObservationDifferent:
		return value.Code == CodeRemoteRefChanged && gitOIDPattern.MatchString(value.CurrentSHA) &&
			len(value.CurrentSHA) == len(binding.CandidateSHA) && value.CurrentSHA != binding.BaseSHA && value.CurrentSHA != binding.CandidateSHA
	case ObservationUnavailable:
		return value.Code == CodeRemoteUnavailable && value.CurrentSHA == ""
	case ObservationAmbiguous:
		return slices.Contains([]ObservationCode{CodeRemoteAmbiguous, CodeRepositoryMismatch, CodeCandidateChanged, CodeBranchChanged, CodeUnsafeTarget}, value.Code) && value.CurrentSHA == ""
	default:
		return false
	}
}

func ValidObservation(value Observation, binding Binding, effectID string, nowMillis int64) bool {
	return validObservationShape(value, binding, effectID) && nowMillis >= value.ObservedAtMillis &&
		nowMillis-value.ObservedAtMillis <= value.MaximumAgeMillis
}

type EffectPhase string

const (
	EffectIntent              EffectPhase = "intent_recorded"
	EffectDispatching         EffectPhase = "dispatching"
	EffectObservationRequired EffectPhase = "observation_required"
	EffectComplete            EffectPhase = "complete"
)

type Effect struct {
	ID                    string       `json:"id"`
	Kind                  string       `json:"kind"`
	Class                 string       `json:"class"`
	Phase                 EffectPhase  `json:"phase"`
	Attempt               uint32       `json:"attempt"`
	AttemptLimit          uint32       `json:"attemptLimit"`
	Observation           *Observation `json:"observation,omitempty"`
	ConsumedObservationID string       `json:"consumedObservationId,omitempty"`
}

type Evidence struct {
	SchemaVersion      string `json:"schemaVersion"`
	ID                 string `json:"id"`
	BindingSHA256      string `json:"bindingSha256"`
	EffectID           string `json:"effectId"`
	ObservationID      string `json:"observationId"`
	ObservationSHA256  string `json:"observationSha256"`
	IntegratedAtMillis int64  `json:"integratedAtMillis"`
	SHA256             string `json:"sha256"`
}

func evidenceValue(value Evidence) Evidence { value.SHA256 = ""; return value }
func evidenceIdentityValue(value Evidence) Evidence {
	value.ID, value.SHA256 = "", ""
	return value
}
func EvidenceSHA256(value Evidence) string { return digest(evidenceValue(value)) }
func EvidenceID(value Evidence) string {
	return "direct-integration-" + digest(evidenceIdentityValue(value))[:32]
}

func EvidenceFromObservation(binding Binding, effectID string, observation Observation, nowMillis int64) Evidence {
	evidence := Evidence{SchemaVersion: EvidenceSchemaVersion, BindingSHA256: binding.SHA256,
		EffectID: effectID, ObservationID: observation.ID, ObservationSHA256: observation.FactSHA256,
		IntegratedAtMillis: nowMillis}
	evidence.ID = EvidenceID(evidence)
	evidence.SHA256 = EvidenceSHA256(evidence)
	return evidence
}

func ValidEvidence(value Evidence, binding Binding, effectID string) bool {
	return value.SchemaVersion == EvidenceSchemaVersion && validIdentity(value.ID) &&
		value.BindingSHA256 == binding.SHA256 && value.EffectID == effectID && validIdentity(value.ObservationID) &&
		digestPattern.MatchString(value.ObservationSHA256) && value.IntegratedAtMillis >= 0 &&
		digestPattern.MatchString(value.SHA256) && value.SHA256 == EvidenceSHA256(value) && value.ID == EvidenceID(value)
}

type StatePhase string

const (
	PhaseWaitingHuman        StatePhase = "waiting_human"
	PhaseIntentRecorded      StatePhase = "intent_recorded"
	PhaseDispatching         StatePhase = "dispatching"
	PhaseObservationRequired StatePhase = "observation_required"
	PhaseWaitingExternal     StatePhase = "waiting_external"
	PhaseComplete            StatePhase = "complete"
	PhaseNeedsYou            StatePhase = "needs_you"
	PhaseInvalidated         StatePhase = "invalidated"
)

type State struct {
	SchemaVersion      string              `json:"schemaVersion"`
	ID                 string              `json:"id"`
	Binding            Binding             `json:"binding"`
	Policy             Policy              `json:"policy"`
	Phase              StatePhase          `json:"phase"`
	HumanAuthorization *HumanAuthorization `json:"humanAuthorization,omitempty"`
	Integration        *Effect             `json:"integration,omitempty"`
	Evidence           *Evidence           `json:"evidence,omitempty"`
	NeedsYouCode       string              `json:"needsYouCode,omitempty"`
	WakeCondition      string              `json:"wakeCondition,omitempty"`
	InvalidationCode   string              `json:"invalidationCode,omitempty"`
}

func StateID(binding Binding) string  { return "direct-delivery-" + digest(binding.SHA256)[:32] }
func EffectID(binding Binding) string { return "direct-push-" + digest(binding.SHA256)[:32] }

func newEffect(binding Binding, policy Policy) *Effect {
	return &Effect{ID: EffectID(binding), Kind: IntegrationEffectKind, Class: IntegrationEffectClass,
		Phase: EffectIntent, AttemptLimit: policy.AttemptLimit}
}

func NewState(binding Binding, policy Policy) (State, bool) {
	if !ValidBinding(binding) || !ValidPolicy(policy) || binding.PolicySHA256 != policy.SHA256 ||
		binding.ConfigurationSHA256 != policy.ConfigurationSHA256 || !TargetAuthorized(policy, binding.TargetRef) {
		return State{}, false
	}
	state := State{SchemaVersion: StateSchemaVersion, ID: StateID(binding), Binding: binding, Policy: policy}
	if policy.IntegrationMode == IntegrationManual {
		state.Phase = PhaseWaitingHuman
	} else if AutomaticTargetAuthorized(policy, binding.TargetRef) {
		state.Phase, state.Integration = PhaseIntentRecorded, newEffect(binding, policy)
	} else {
		return State{}, false
	}
	return state, ValidState(state)
}

func CloneState(state State) State {
	state.Policy.AuthorizedTargetRefs = slices.Clone(state.Policy.AuthorizedTargetRefs)
	state.Policy.AutomaticTargetRefs = slices.Clone(state.Policy.AutomaticTargetRefs)
	if state.HumanAuthorization != nil {
		copy := *state.HumanAuthorization
		state.HumanAuthorization = &copy
	}
	if state.Integration != nil {
		copy := *state.Integration
		if copy.Observation != nil {
			observation := *copy.Observation
			copy.Observation = &observation
		}
		state.Integration = &copy
	}
	if state.Evidence != nil {
		copy := *state.Evidence
		state.Evidence = &copy
	}
	return state
}

func AuthorizeManual(state State, authorization HumanAuthorization) (State, bool) {
	if !ValidState(state) || state.Policy.IntegrationMode != IntegrationManual || state.Phase != PhaseWaitingHuman ||
		!ValidAuthorization(authorization, state.Binding) {
		return state, false
	}
	state = CloneState(state)
	state.HumanAuthorization = &authorization
	state.Integration = newEffect(state.Binding, state.Policy)
	state.Phase = PhaseIntentRecorded
	return state, ValidState(state)
}

func activePhase(phase StatePhase) bool {
	return slices.Contains([]StatePhase{PhaseIntentRecorded, PhaseDispatching, PhaseObservationRequired, PhaseWaitingExternal}, phase)
}

func RecordObservation(state State, observation Observation) (State, bool) {
	if !ValidState(state) || state.Integration == nil || !activePhase(state.Phase) ||
		!validObservationShape(observation, state.Binding, state.Integration.ID) || observation.Attempt != state.Integration.Attempt {
		return state, false
	}
	state = CloneState(state)
	state.Integration.Observation = &observation
	if observation.Status == ObservationUnavailable {
		state.Phase = PhaseWaitingExternal
	} else {
		switch state.Integration.Phase {
		case EffectIntent:
			state.Phase = PhaseIntentRecorded
		case EffectDispatching:
			state.Phase = PhaseDispatching
		case EffectObservationRequired:
			state.Phase = PhaseObservationRequired
		}
	}
	return state, ValidState(state)
}

func BeginDispatch(state State) (State, bool) {
	if !ValidState(state) || state.Integration == nil || !activePhase(state.Phase) || state.Integration.Observation == nil ||
		state.Integration.Observation.Status != ObservationCurrentExpected ||
		state.Integration.Attempt >= state.Integration.AttemptLimit {
		return state, false
	}
	state = CloneState(state)
	state.Integration.ConsumedObservationID = state.Integration.Observation.ID
	state.Integration.Observation = nil
	state.Integration.Attempt++
	state.Integration.Phase = EffectDispatching
	state.Phase = PhaseDispatching
	return state, ValidState(state)
}

func RequireObservation(state State) (State, bool) {
	if !ValidState(state) || state.Phase != PhaseDispatching || state.Integration == nil || state.Integration.Phase != EffectDispatching {
		return state, false
	}
	state = CloneState(state)
	state.Integration.Phase = EffectObservationRequired
	state.Phase = PhaseObservationRequired
	return state, ValidState(state)
}

// RetryAfterPreDispatch returns a proven-before-handoff failure to the exact
// intent. The consumed attempt remains counted and the next dispatch still
// requires a newly persisted remote observation.
func RetryAfterPreDispatch(state State) (State, bool) {
	if !ValidState(state) || state.Phase != PhaseDispatching || state.Integration == nil || state.Integration.Phase != EffectDispatching {
		return state, false
	}
	state = CloneState(state)
	state.Integration.Phase = EffectIntent
	state.Integration.Observation = nil
	state.Phase = PhaseIntentRecorded
	return state, ValidState(state)
}

func Complete(state State, observation Observation, nowMillis int64) (State, bool) {
	if !ValidState(state) || state.Integration == nil || !activePhase(state.Phase) || observation.Status != ObservationDesired ||
		!ValidObservation(observation, state.Binding, state.Integration.ID, nowMillis) ||
		observation.Attempt != state.Integration.Attempt {
		return state, false
	}
	state = CloneState(state)
	state.Integration.Observation = &observation
	state.Integration.Phase = EffectComplete
	evidence := EvidenceFromObservation(state.Binding, state.Integration.ID, observation, nowMillis)
	state.Evidence = &evidence
	state.Phase = PhaseComplete
	return state, ValidState(state)
}

func Park(state State, code, wake string) (State, bool) {
	if !ValidState(state) || !validIdentity(code) || !validIdentity(wake) {
		return state, false
	}
	state = CloneState(state)
	state.Phase, state.NeedsYouCode, state.WakeCondition = PhaseNeedsYou, code, wake
	return state, ValidState(state)
}

func Invalidate(state State, code, wake string) State {
	if !ValidState(state) || !validIdentity(code) || !validIdentity(wake) {
		return state
	}
	state = CloneState(state)
	state.Phase, state.InvalidationCode, state.WakeCondition = PhaseInvalidated, code, wake
	state.NeedsYouCode = ""
	return state
}

// DispatchInFlight reports an external handoff which has not yet been
// reconciled to an authoritative target-ref result. Candidate replacement and
// authority invalidation must wait so a successful push cannot be orphaned.
func DispatchInFlight(state State) bool {
	return ValidState(state) && state.Integration != nil &&
		(state.Integration.Phase == EffectDispatching || state.Integration.Phase == EffectObservationRequired)
}

func validEffect(effect Effect, state State) bool {
	if effect.ID != EffectID(state.Binding) || effect.Kind != IntegrationEffectKind || effect.Class != IntegrationEffectClass ||
		effect.AttemptLimit != state.Policy.AttemptLimit ||
		effect.Attempt > effect.AttemptLimit || !slices.Contains([]EffectPhase{EffectIntent, EffectDispatching, EffectObservationRequired, EffectComplete}, effect.Phase) {
		return false
	}
	if effect.Observation != nil && (!validObservationShape(*effect.Observation, state.Binding, effect.ID) || effect.Observation.Attempt != effect.Attempt) {
		return false
	}
	if effect.Attempt == 0 && effect.ConsumedObservationID != "" || effect.Attempt > 0 && !validIdentity(effect.ConsumedObservationID) {
		return false
	}
	return true
}

// ValidState is the durable TaskStore boundary. It rejects invented manual
// authority, unbound observations/evidence, fallback-shaped policy, and
// partially recorded dispatch frontiers.
func ValidState(state State) bool {
	if state.SchemaVersion != StateSchemaVersion || state.ID != StateID(state.Binding) ||
		!ValidBinding(state.Binding) || !ValidPolicy(state.Policy) ||
		state.Binding.PolicySHA256 != state.Policy.SHA256 ||
		state.Binding.ConfigurationSHA256 != state.Policy.ConfigurationSHA256 ||
		!TargetAuthorized(state.Policy, state.Binding.TargetRef) || secretPattern.MatchString(state.NeedsYouCode) ||
		secretPattern.MatchString(state.WakeCondition) || secretPattern.MatchString(state.InvalidationCode) {
		return false
	}
	if state.HumanAuthorization != nil && !ValidAuthorization(*state.HumanAuthorization, state.Binding) {
		return false
	}
	if state.Policy.IntegrationMode == IntegrationManual && state.Phase != PhaseWaitingHuman && state.HumanAuthorization == nil {
		return false
	}
	if state.Policy.IntegrationMode == IntegrationAutomatic && (state.HumanAuthorization != nil || !AutomaticTargetAuthorized(state.Policy, state.Binding.TargetRef)) {
		return false
	}
	if state.Integration != nil && !validEffect(*state.Integration, state) {
		return false
	}
	if state.Evidence != nil && (state.Integration == nil || !ValidEvidence(*state.Evidence, state.Binding, state.Integration.ID) ||
		state.Integration.Observation == nil || state.Evidence.ObservationID != state.Integration.Observation.ID ||
		state.Evidence.ObservationSHA256 != state.Integration.Observation.FactSHA256) {
		return false
	}
	switch state.Phase {
	case PhaseWaitingHuman:
		return state.Policy.IntegrationMode == IntegrationManual && state.HumanAuthorization == nil && state.Integration == nil &&
			state.Evidence == nil && state.NeedsYouCode == "" && state.WakeCondition == "" && state.InvalidationCode == ""
	case PhaseIntentRecorded:
		return state.Integration != nil && state.Integration.Phase == EffectIntent && state.Evidence == nil &&
			state.NeedsYouCode == "" && state.WakeCondition == "" && state.InvalidationCode == ""
	case PhaseDispatching:
		return state.Integration != nil && state.Integration.Phase == EffectDispatching && state.Evidence == nil &&
			state.NeedsYouCode == "" && state.WakeCondition == "" && state.InvalidationCode == ""
	case PhaseObservationRequired:
		return state.Integration != nil && state.Integration.Phase == EffectObservationRequired && state.Evidence == nil &&
			state.NeedsYouCode == "" && state.WakeCondition == "" && state.InvalidationCode == ""
	case PhaseWaitingExternal:
		return state.Integration != nil && state.Integration.Observation != nil &&
			state.Integration.Observation.Status == ObservationUnavailable && state.Evidence == nil &&
			state.NeedsYouCode == "" && state.WakeCondition == "" && state.InvalidationCode == ""
	case PhaseComplete:
		return state.Integration != nil && state.Integration.Phase == EffectComplete && state.Integration.Observation != nil &&
			state.Integration.Observation.Status == ObservationDesired && state.Evidence != nil &&
			ValidEvidence(*state.Evidence, state.Binding, state.Integration.ID) &&
			state.Evidence.ObservationID == state.Integration.Observation.ID &&
			state.Evidence.ObservationSHA256 == state.Integration.Observation.FactSHA256 &&
			state.NeedsYouCode == "" && state.WakeCondition == "" && state.InvalidationCode == ""
	case PhaseNeedsYou:
		return validIdentity(state.NeedsYouCode) && validIdentity(state.WakeCondition) && state.InvalidationCode == ""
	case PhaseInvalidated:
		return validIdentity(state.InvalidationCode) && validIdentity(state.WakeCondition) && state.NeedsYouCode == ""
	default:
		return false
	}
}
