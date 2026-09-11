// SPDX-License-Identifier: Apache-2.0

// Package integration owns the pure pull-request integration contract. It
// binds one merge to an exact Candidate, base, quality gate, owned pull
// request, repository, Project lease, and atomic expected-head primitive.
// Git, GitHub, TaskStore, clock, and retry side effects remain outside this
// package.
package integration

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"regexp"
	"slices"
	"strings"
)

const (
	PolicySchemaVersion        = "director.integration-policy/v1"
	BindingSchemaVersion       = "director.integration-binding/v1"
	AuthorizationSchemaVersion = "director.integration-authorization/v1"
	ForgeObservationVersion    = "director.integration-forge-observation/v1"
	GitObservationVersion      = "director.integration-git-observation/v1"
	ObservationSchemaVersion   = "director.integration-observation/v1"
	EvidenceSchemaVersion      = "director.integration-evidence/v1"
	StateSchemaVersion         = "director.integration-state/v1"
	MaximumObservationAgeMS    = int64(5_000)
	MaximumAttempts            = uint32(3)
	MaximumHistoricalStates    = 64
	IntegrationEffectKind      = "pull_request.integration"
	IntegrationEffectClass     = "conditional_update"
	AuthorizationActorSource   = "paseo_authenticated_integrate_action"
)

var (
	identifierPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.:@/-]{0,255}$`)
	loginPattern      = regexp.MustCompile(`^[A-Za-z0-9](?:[A-Za-z0-9-]{0,38})$`)
	uuidPattern       = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[1-8][0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`)
	digestPattern     = regexp.MustCompile(`^[0-9a-f]{64}$`)
	gitOIDPattern     = regexp.MustCompile(`^(?:[0-9a-f]{40}|[0-9a-f]{64})$`)
	secretPattern     = regexp.MustCompile(`(?i)(?:-----BEGIN [A-Z0-9 ]*PRIVATE KEY-----|github_pat_[A-Za-z0-9_]{16,}|gh[pousr]_[A-Za-z0-9]{16,}|sk-[A-Za-z0-9_-]{16,}|(?:password|secret|token|credential|authorization)\s*[:=]\s*\S+)`)
)

func digest(value any) string {
	encoded, err := json.Marshal(value)
	if err != nil {
		panic("marshal fixed integration value: " + err.Error())
	}
	sum := sha256.Sum256(encoded)
	return hex.EncodeToString(sum[:])
}

// DigestText exposes the package's redaction-safe content addressing without
// exposing raw external values in durable identifiers.
func DigestText(value string) string { return digest(value) }

func validIdentity(value string) bool {
	return identifierPattern.MatchString(value) && !secretPattern.MatchString(value)
}

func validBaseRef(value string) bool {
	return strings.HasPrefix(value, "refs/heads/") && validIdentity(strings.TrimPrefix(value, "refs/heads/"))
}

type Mode string

const (
	ModeManual    Mode = "manual"
	ModeAutomatic Mode = "automatic"
)

// Policy is frozen into the Run. Merge commits are required because their
// exact parents and tree can be verified after GitHub responds or a response
// is lost.
type Policy struct {
	SchemaVersion        string `json:"schemaVersion"`
	DeliveryMode         string `json:"deliveryMode"`
	Mode                 Mode   `json:"mode"`
	ConfigurationSHA256  string `json:"configurationSha256"`
	MergeMethod          string `json:"mergeMethod"`
	AtomicExpectedHead   bool   `json:"atomicExpectedHead"`
	ObservationAgeMillis int64  `json:"observationAgeMillis"`
	AttemptLimit         uint32 `json:"attemptLimit"`
	SHA256               string `json:"sha256"`
}

func policyValue(value Policy) Policy  { value.SHA256 = ""; return value }
func PolicySHA256(value Policy) string { return digest(policyValue(value)) }

func NewPolicy(mode Mode, configurationSHA256 string) (Policy, bool) {
	value := Policy{SchemaVersion: PolicySchemaVersion, DeliveryMode: "pull_request", Mode: mode,
		ConfigurationSHA256: configurationSHA256, MergeMethod: "merge", AtomicExpectedHead: true,
		ObservationAgeMillis: MaximumObservationAgeMS, AttemptLimit: MaximumAttempts}
	value.SHA256 = PolicySHA256(value)
	return value, ValidPolicy(value)
}

func ValidPolicy(value Policy) bool {
	return value.SchemaVersion == PolicySchemaVersion && value.DeliveryMode == "pull_request" &&
		(value.Mode == ModeManual || value.Mode == ModeAutomatic) && digestPattern.MatchString(value.ConfigurationSHA256) &&
		value.MergeMethod == "merge" && value.AtomicExpectedHead && value.ObservationAgeMillis > 0 &&
		value.ObservationAgeMillis <= MaximumObservationAgeMS && value.AttemptLimit > 0 &&
		value.AttemptLimit <= MaximumAttempts && digestPattern.MatchString(value.SHA256) && value.SHA256 == PolicySHA256(value)
}

// Binding freezes every durable fact which must still be current at merge.
type Binding struct {
	SchemaVersion            string `json:"schemaVersion"`
	TaskID                   string `json:"taskId"`
	RunID                    string `json:"runId"`
	CandidateID              string `json:"candidateId"`
	CandidateSHA             string `json:"candidateSha"`
	BaseSHA                  string `json:"baseSha"`
	TreeSHA                  string `json:"treeSha"`
	ManifestSHA256           string `json:"manifestSha256"`
	CandidateGeneration      uint64 `json:"candidateGeneration"`
	TaskVersion              uint64 `json:"taskVersion"`
	ConfigurationSHA256      string `json:"configurationSha256"`
	RepositoryID             string `json:"repositoryId"`
	RepositoryBindingSHA256  string `json:"repositoryBindingSha256"`
	CanonicalRemote          string `json:"canonicalRemote"`
	GitHubRepositoryID       int64  `json:"githubRepositoryId"`
	GitHubRepositoryNodeID   string `json:"githubRepositoryNodeId"`
	RepositoryOwner          string `json:"repositoryOwner"`
	RepositoryName           string `json:"repositoryName"`
	ViewerLogin              string `json:"viewerLogin"`
	Branch                   string `json:"branch"`
	BaseRef                  string `json:"baseRef"`
	PullRequestNumber        int64  `json:"pullRequestNumber"`
	PullRequestNodeID        string `json:"pullRequestNodeId"`
	OwnershipSHA256          string `json:"ownershipSha256"`
	MarkerSHA256             string `json:"markerSha256"`
	PublicationEvidenceID    string `json:"publicationEvidenceId"`
	ValidationPolicySHA256   string `json:"validationPolicySha256"`
	ValidationEvidenceID     string `json:"validationEvidenceId"`
	ValidationEvidenceSHA256 string `json:"validationEvidenceSha256"`
	ReviewEvidenceID         string `json:"reviewEvidenceId"`
	ReviewerUUID             string `json:"reviewerUuid"`
	CIObservationID          string `json:"ciObservationId"`
	CIObservationSHA256      string `json:"ciObservationSha256"`
	FeedbackStateSHA256      string `json:"feedbackStateSha256"`
	ReadyEvidenceID          string `json:"readyEvidenceId"`
	PolicySHA256             string `json:"policySha256"`
	LeaseEpoch               uint64 `json:"leaseEpoch"`
	SHA256                   string `json:"sha256"`
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
		validIdentity(value.CandidateID) && gitOIDPattern.MatchString(value.CandidateSHA) && gitOIDPattern.MatchString(value.BaseSHA) &&
		gitOIDPattern.MatchString(value.TreeSHA) && len(value.CandidateSHA) == len(value.BaseSHA) && len(value.CandidateSHA) == len(value.TreeSHA) &&
		value.CandidateSHA != value.BaseSHA && digestPattern.MatchString(value.ManifestSHA256) && value.CandidateGeneration > 0 &&
		value.TaskVersion > 0 && digestPattern.MatchString(value.ConfigurationSHA256) && validIdentity(value.RepositoryID) &&
		digestPattern.MatchString(value.RepositoryBindingSHA256) && strings.HasPrefix(value.CanonicalRemote, "https://github.com/") &&
		value.GitHubRepositoryID > 0 && validIdentity(value.GitHubRepositoryNodeID) && loginPattern.MatchString(value.RepositoryOwner) &&
		validIdentity(value.RepositoryName) && loginPattern.MatchString(value.ViewerLogin) && strings.HasPrefix(value.Branch, "task/") &&
		validIdentity(value.Branch) && validBaseRef(value.BaseRef) && value.Branch != strings.TrimPrefix(value.BaseRef, "refs/heads/") &&
		value.PullRequestNumber > 0 && validIdentity(value.PullRequestNodeID) && validIdentity(value.PublicationEvidenceID) &&
		digestPattern.MatchString(value.OwnershipSHA256) && digestPattern.MatchString(value.MarkerSHA256) &&
		digestPattern.MatchString(value.ValidationPolicySHA256) && validIdentity(value.ValidationEvidenceID) &&
		digestPattern.MatchString(value.ValidationEvidenceSHA256) && validIdentity(value.ReviewEvidenceID) &&
		uuidPattern.MatchString(value.ReviewerUUID) && validIdentity(value.CIObservationID) &&
		digestPattern.MatchString(value.CIObservationSHA256) && digestPattern.MatchString(value.FeedbackStateSHA256) &&
		validIdentity(value.ReadyEvidenceID) && digestPattern.MatchString(value.PolicySHA256) && value.LeaseEpoch > 0 &&
		digestPattern.MatchString(value.SHA256) && value.SHA256 == BindingSHA256(value)
}

// HumanAuthorization is accepted only from the separately authenticated
// Paseo integrate action. GitHub actors, feedback, and prose cannot satisfy it.
type HumanAuthorization struct {
	SchemaVersion      string `json:"schemaVersion"`
	ID                 string `json:"id"`
	ActorKind          string `json:"actorKind"`
	ActorSource        string `json:"actorSource"`
	Authenticated      bool   `json:"authenticated"`
	ActorID            string `json:"actorId"`
	SessionID          string `json:"sessionId"`
	DecisionID         string `json:"decisionId"`
	Action             string `json:"action"`
	BindingSHA256      string `json:"bindingSha256"`
	CandidateSHA       string `json:"candidateSha"`
	BaseSHA            string `json:"baseSha"`
	PullRequestNumber  int64  `json:"pullRequestNumber"`
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
	return value.SchemaVersion == AuthorizationSchemaVersion && validIdentity(value.ID) && value.ActorKind == "human" &&
		value.ActorSource == AuthorizationActorSource && value.Authenticated && validIdentity(value.ActorID) &&
		validIdentity(value.SessionID) && validIdentity(value.DecisionID) && value.Action == "integrate_pull_request" &&
		value.BindingSHA256 == binding.SHA256 && value.CandidateSHA == binding.CandidateSHA && value.BaseSHA == binding.BaseSHA &&
		value.PullRequestNumber == binding.PullRequestNumber && value.PolicySHA256 == binding.PolicySHA256 &&
		value.AuthorizedAtMillis >= 0 && digestPattern.MatchString(value.SHA256) && value.SHA256 == AuthorizationSHA256(value)
}

type Code string

const (
	CodeOK                    Code = "INTEGRATION_OK"
	CodeUnavailable           Code = "INTEGRATION_UNAVAILABLE"
	CodeTLS                   Code = "INTEGRATION_TLS_FAILURE"
	CodeRateLimited           Code = "INTEGRATION_RATE_LIMITED"
	CodeUnauthorized          Code = "INTEGRATION_UNAUTHORIZED"
	CodeForbidden             Code = "INTEGRATION_FORBIDDEN"
	CodeNotFound              Code = "INTEGRATION_NOT_FOUND"
	CodeConflict              Code = "INTEGRATION_CONFLICT"
	CodeUnprocessable         Code = "INTEGRATION_UNPROCESSABLE"
	CodeServer                Code = "INTEGRATION_SERVER_ERROR"
	CodeRepositoryMismatch    Code = "INTEGRATION_REPOSITORY_MISMATCH"
	CodePullRequestMismatch   Code = "INTEGRATION_PULL_REQUEST_MISMATCH"
	CodeHeadChanged           Code = "INTEGRATION_HEAD_CHANGED"
	CodeBaseChanged           Code = "INTEGRATION_BASE_CHANGED"
	CodeCandidateChanged      Code = "INTEGRATION_CANDIDATE_CHANGED"
	CodeChecksChanged         Code = "INTEGRATION_CHECKS_CHANGED"
	CodeFeedbackPresent       Code = "INTEGRATION_FEEDBACK_PRESENT"
	CodeMergeability          Code = "INTEGRATION_NOT_MERGEABLE"
	CodeAtomicHeadUnavailable Code = "INTEGRATION_ATOMIC_HEAD_UNAVAILABLE"
	CodeGraphMismatch         Code = "INTEGRATION_GRAPH_MISMATCH"
	CodeResponseUnknown       Code = "INTEGRATION_RESPONSE_UNKNOWN"
	CodeAttemptsExhausted     Code = "INTEGRATION_ATTEMPTS_EXHAUSTED"
)

var codes = []Code{CodeOK, CodeUnavailable, CodeTLS, CodeRateLimited, CodeUnauthorized, CodeForbidden, CodeNotFound,
	CodeConflict, CodeUnprocessable, CodeServer, CodeRepositoryMismatch, CodePullRequestMismatch, CodeHeadChanged,
	CodeBaseChanged, CodeCandidateChanged, CodeChecksChanged, CodeFeedbackPresent, CodeMergeability,
	CodeAtomicHeadUnavailable, CodeGraphMismatch, CodeResponseUnknown, CodeAttemptsExhausted}

func validCode(value Code) bool { return slices.Contains(codes, value) }
func WaitingCode(value Code) bool {
	return slices.Contains([]Code{CodeUnavailable, CodeTLS, CodeRateLimited, CodeServer}, value)
}

type ForgeStatus string

const (
	ForgeReady       ForgeStatus = "ready"
	ForgeIntegrated  ForgeStatus = "integrated"
	ForgeInvalid     ForgeStatus = "invalid"
	ForgeUnavailable ForgeStatus = "unavailable"
)

// ForgeObservation is a bounded current GitHub repository and pull-request
// fact. It contains no URL, body, log, path, credential, or thread text.
type ForgeObservation struct {
	SchemaVersion      string      `json:"schemaVersion"`
	ID                 string      `json:"id"`
	Status             ForgeStatus `json:"status"`
	Code               Code        `json:"code"`
	RepositoryID       int64       `json:"repositoryId"`
	RepositoryNodeID   string      `json:"repositoryNodeId"`
	Owner              string      `json:"owner"`
	Name               string      `json:"name"`
	ViewerLogin        string      `json:"viewerLogin"`
	Authenticated      bool        `json:"authenticated"`
	CanMerge           bool        `json:"canMerge"`
	TLSVerified        bool        `json:"tlsVerified"`
	RateRemaining      int64       `json:"rateRemaining"`
	APIVersion         string      `json:"apiVersion"`
	AtomicExpectedHead bool        `json:"atomicExpectedHead"`
	DefaultBranch      string      `json:"defaultBranch"`
	PullRequestNumber  int64       `json:"pullRequestNumber"`
	PullRequestNodeID  string      `json:"pullRequestNodeId"`
	PullRequestState   string      `json:"pullRequestState"`
	Draft              bool        `json:"draft"`
	HeadSHA            string      `json:"headSha"`
	HeadRef            string      `json:"headRef"`
	BaseRef            string      `json:"baseRef"`
	HeadRepositoryID   int64       `json:"headRepositoryId"`
	BaseRepositoryID   int64       `json:"baseRepositoryId"`
	HeadOwner          string      `json:"headOwner"`
	AuthorLogin        string      `json:"authorLogin"`
	MarkerSHA256       string      `json:"markerSha256"`
	MarkerCount        uint32      `json:"markerCount"`
	Mergeable          bool        `json:"mergeable"`
	MergeableState     string      `json:"mergeableState"`
	Merged             bool        `json:"merged"`
	MergedAtMillis     int64       `json:"mergedAtMillis,omitempty"`
	MergeCommitSHA     string      `json:"mergeCommitSha,omitempty"`
	ObservedAtMillis   int64       `json:"observedAtMillis"`
	MaximumAgeMillis   int64       `json:"maximumAgeMillis"`
	FactSHA256         string      `json:"factSha256"`
}

func forgeValue(value ForgeObservation) ForgeObservation   { value.FactSHA256 = ""; return value }
func ForgeObservationSHA256(value ForgeObservation) string { return digest(forgeValue(value)) }
func SealForgeObservation(value ForgeObservation) ForgeObservation {
	value.SchemaVersion = ForgeObservationVersion
	value.FactSHA256 = ForgeObservationSHA256(value)
	return value
}
func CurrentForgeObservation(value ForgeObservation, binding Binding, nowMillis int64) bool {
	if value.SchemaVersion != ForgeObservationVersion || !validIdentity(value.ID) || !validCode(value.Code) ||
		value.ObservedAtMillis < 0 || value.ObservedAtMillis > nowMillis || value.MaximumAgeMillis != MaximumObservationAgeMS ||
		nowMillis-value.ObservedAtMillis > value.MaximumAgeMillis || value.FactSHA256 != ForgeObservationSHA256(value) {
		return false
	}
	if value.Status == ForgeUnavailable {
		return WaitingCode(value.Code)
	}
	if value.Status == ForgeInvalid {
		return value.Code != CodeOK
	}
	exact := value.Code == CodeOK && value.RepositoryID == binding.GitHubRepositoryID &&
		value.RepositoryNodeID == binding.GitHubRepositoryNodeID && strings.EqualFold(value.Owner, binding.RepositoryOwner) &&
		value.Name == binding.RepositoryName && strings.EqualFold(value.ViewerLogin, binding.ViewerLogin) && value.Authenticated &&
		value.CanMerge && value.TLSVerified && value.RateRemaining > 0 && value.APIVersion == "2022-11-28" &&
		value.AtomicExpectedHead && value.DefaultBranch == strings.TrimPrefix(binding.BaseRef, "refs/heads/") &&
		value.PullRequestNumber == binding.PullRequestNumber && value.PullRequestNodeID == binding.PullRequestNodeID &&
		value.HeadSHA == binding.CandidateSHA && value.HeadRef == binding.Branch &&
		value.BaseRef == strings.TrimPrefix(binding.BaseRef, "refs/heads/") && value.HeadRepositoryID == binding.GitHubRepositoryID &&
		value.BaseRepositoryID == binding.GitHubRepositoryID && strings.EqualFold(value.HeadOwner, binding.ViewerLogin) &&
		strings.EqualFold(value.AuthorLogin, binding.ViewerLogin) && value.MarkerSHA256 == binding.MarkerSHA256 && value.MarkerCount == 1
	if !exact {
		return false
	}
	switch value.Status {
	case ForgeReady:
		return value.PullRequestState == "open" && !value.Draft && value.Mergeable && value.MergeableState == "clean" &&
			!value.Merged && value.MergedAtMillis == 0 && value.MergeCommitSHA == ""
	case ForgeIntegrated:
		return value.PullRequestState == "closed" && !value.Draft && value.Merged && value.MergedAtMillis > 0 &&
			gitOIDPattern.MatchString(value.MergeCommitSHA) && len(value.MergeCommitSHA) == len(binding.CandidateSHA)
	default:
		return false
	}
}

type GitStatus string

const (
	GitReady       GitStatus = "ready"
	GitIntegrated  GitStatus = "integrated"
	GitInvalid     GitStatus = "invalid"
	GitUnavailable GitStatus = "unavailable"
)

// GitObservation binds the current Task/base/default refs and, after merge,
// the exact merge parents and tree fetched from the remote graph.
type GitObservation struct {
	SchemaVersion    string    `json:"schemaVersion"`
	ID               string    `json:"id"`
	Status           GitStatus `json:"status"`
	Code             Code      `json:"code"`
	RepositoryID     string    `json:"repositoryId"`
	CanonicalRemote  string    `json:"canonicalRemote"`
	HeadRef          string    `json:"headRef"`
	HeadSHA          string    `json:"headSha,omitempty"`
	BaseRef          string    `json:"baseRef"`
	BaseSHA          string    `json:"baseSha,omitempty"`
	DefaultRef       string    `json:"defaultRef,omitempty"`
	DefaultSHA       string    `json:"defaultSha,omitempty"`
	MergeCommitSHA   string    `json:"mergeCommitSha,omitempty"`
	ParentSHAs       []string  `json:"parentShas,omitempty"`
	TreeSHA          string    `json:"treeSha,omitempty"`
	ObservedAtMillis int64     `json:"observedAtMillis"`
	MaximumAgeMillis int64     `json:"maximumAgeMillis"`
	FactSHA256       string    `json:"factSha256"`
}

func gitValue(value GitObservation) GitObservation {
	value.FactSHA256 = ""
	value.ParentSHAs = slices.Clone(value.ParentSHAs)
	return value
}
func GitObservationSHA256(value GitObservation) string { return digest(gitValue(value)) }
func SealGitObservation(value GitObservation) GitObservation {
	value.SchemaVersion = GitObservationVersion
	value.ParentSHAs = slices.Clone(value.ParentSHAs)
	value.FactSHA256 = GitObservationSHA256(value)
	return value
}
func CurrentGitObservation(value GitObservation, binding Binding, mergeCommitSHA string, nowMillis int64) bool {
	if value.SchemaVersion != GitObservationVersion || !validIdentity(value.ID) || !validCode(value.Code) ||
		value.RepositoryID != binding.RepositoryID || value.CanonicalRemote != binding.CanonicalRemote ||
		value.HeadRef != "refs/heads/"+binding.Branch || value.BaseRef != binding.BaseRef ||
		value.ObservedAtMillis < 0 || value.ObservedAtMillis > nowMillis || value.MaximumAgeMillis != MaximumObservationAgeMS ||
		nowMillis-value.ObservedAtMillis > value.MaximumAgeMillis || value.FactSHA256 != GitObservationSHA256(value) {
		return false
	}
	if value.Status == GitUnavailable {
		return WaitingCode(value.Code)
	}
	if value.Status == GitInvalid {
		return value.Code != CodeOK
	}
	if value.HeadSHA != binding.CandidateSHA || value.DefaultRef != binding.BaseRef {
		return false
	}
	switch value.Status {
	case GitReady:
		return mergeCommitSHA == "" && value.Code == CodeOK && value.BaseSHA == binding.BaseSHA &&
			value.DefaultSHA == binding.BaseSHA && value.MergeCommitSHA == "" && len(value.ParentSHAs) == 0 && value.TreeSHA == ""
	case GitIntegrated:
		return gitOIDPattern.MatchString(mergeCommitSHA) && value.Code == CodeOK && value.BaseSHA == mergeCommitSHA &&
			value.DefaultSHA == mergeCommitSHA && value.MergeCommitSHA == mergeCommitSHA &&
			slices.Equal(value.ParentSHAs, []string{binding.BaseSHA, binding.CandidateSHA}) && value.TreeSHA == binding.TreeSHA
	default:
		return false
	}
}

type ObservationStatus string

const (
	ObservationReady       ObservationStatus = "ready"
	ObservationIntegrated  ObservationStatus = "integrated"
	ObservationInvalid     ObservationStatus = "invalid"
	ObservationUnavailable ObservationStatus = "unavailable"
)

// Observation is the one-use precondition/outcome fact consumed by the
// integration state machine. LiveValidationSHA256 is produced by re-running
// the configured check/status reducer without starting another CI cycle.
type Observation struct {
	SchemaVersion          string            `json:"schemaVersion"`
	ID                     string            `json:"id"`
	BindingSHA256          string            `json:"bindingSha256"`
	Attempt                uint32            `json:"attempt"`
	Status                 ObservationStatus `json:"status"`
	Code                   Code              `json:"code"`
	ForgeBefore            ForgeObservation  `json:"forgeBefore"`
	ForgeAfter             ForgeObservation  `json:"forgeAfter"`
	GitBefore              GitObservation    `json:"gitBefore"`
	GitAfter               GitObservation    `json:"gitAfter"`
	LiveValidationID       string            `json:"liveValidationId,omitempty"`
	LiveValidationSHA256   string            `json:"liveValidationSha256,omitempty"`
	ConfiguredCheckCount   uint32            `json:"configuredCheckCount"`
	ObservedCheckCount     uint32            `json:"observedCheckCount"`
	StatusCount            uint32            `json:"statusCount"`
	FeedbackSnapshotSHA256 string            `json:"feedbackSnapshotSha256,omitempty"`
	FeedbackClear          bool              `json:"feedbackClear"`
	MergeCommitSHA         string            `json:"mergeCommitSha,omitempty"`
	ObservedAtMillis       int64             `json:"observedAtMillis"`
	MaximumAgeMillis       int64             `json:"maximumAgeMillis"`
	FactSHA256             string            `json:"factSha256"`
}

func observationValue(value Observation) Observation {
	value.ID, value.FactSHA256 = "", ""
	return value
}
func ObservationFactSHA256(value Observation) string { return digest(observationValue(value)) }
func SealObservation(value Observation) Observation {
	value.SchemaVersion = ObservationSchemaVersion
	value.FactSHA256 = ObservationFactSHA256(value)
	value.ID = "integration-observation-" + digest(struct {
		Binding, Fact string
		Attempt       uint32
	}{value.BindingSHA256, value.FactSHA256, value.Attempt})[:32]
	return value
}

func ValidObservation(value Observation, binding Binding, effectID string, nowMillis int64) bool {
	if value.SchemaVersion != ObservationSchemaVersion || !validIdentity(value.ID) || value.BindingSHA256 != binding.SHA256 ||
		value.ObservedAtMillis < 0 || value.ObservedAtMillis > nowMillis || value.MaximumAgeMillis != MaximumObservationAgeMS ||
		nowMillis-value.ObservedAtMillis > value.MaximumAgeMillis || !validCode(value.Code) ||
		value.FactSHA256 != ObservationFactSHA256(value) || value.ID != SealObservation(observationValue(value)).ID || effectID == "" {
		return false
	}
	switch value.Status {
	case ObservationUnavailable:
		return WaitingCode(value.Code)
	case ObservationInvalid:
		return value.Code != CodeOK
	case ObservationReady, ObservationIntegrated:
		if value.Code != CodeOK || !validIdentity(value.LiveValidationID) || !digestPattern.MatchString(value.LiveValidationSHA256) ||
			value.ConfiguredCheckCount == 0 || value.ObservedCheckCount != value.ConfiguredCheckCount ||
			!digestPattern.MatchString(value.FeedbackSnapshotSHA256) || !value.FeedbackClear {
			return false
		}
	}
	if !CurrentForgeObservation(value.ForgeBefore, binding, nowMillis) || !CurrentForgeObservation(value.ForgeAfter, binding, nowMillis) ||
		!CurrentGitObservation(value.GitBefore, binding, value.MergeCommitSHA, nowMillis) ||
		!CurrentGitObservation(value.GitAfter, binding, value.MergeCommitSHA, nowMillis) ||
		value.ForgeBefore.Status != value.ForgeAfter.Status || value.ForgeBefore.HeadSHA != value.ForgeAfter.HeadSHA ||
		value.ForgeBefore.MergeCommitSHA != value.ForgeAfter.MergeCommitSHA || value.GitBefore.Status != value.GitAfter.Status ||
		value.GitBefore.BaseSHA != value.GitAfter.BaseSHA {
		return false
	}
	if value.Status == ObservationReady {
		return value.MergeCommitSHA == "" && value.ForgeBefore.Status == ForgeReady && value.GitBefore.Status == GitReady
	}
	return value.Status == ObservationIntegrated && gitOIDPattern.MatchString(value.MergeCommitSHA) &&
		value.ForgeBefore.Status == ForgeIntegrated && value.ForgeBefore.MergeCommitSHA == value.MergeCommitSHA &&
		value.GitBefore.Status == GitIntegrated
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
	MergeCommitSHA     string `json:"mergeCommitSha"`
	IntegratedAtMillis int64  `json:"integratedAtMillis"`
	SHA256             string `json:"sha256"`
}

func evidenceValue(value Evidence) Evidence    { value.SHA256 = ""; return value }
func evidenceIdentity(value Evidence) Evidence { value.ID, value.SHA256 = "", ""; return value }
func EvidenceSHA256(value Evidence) string     { return digest(evidenceValue(value)) }
func EvidenceID(value Evidence) string {
	return "integration-evidence-" + digest(evidenceIdentity(value))[:32]
}
func EvidenceFromObservation(binding Binding, effectID string, observation Observation, nowMillis int64) Evidence {
	value := Evidence{SchemaVersion: EvidenceSchemaVersion, BindingSHA256: binding.SHA256, EffectID: effectID,
		ObservationID: observation.ID, ObservationSHA256: observation.FactSHA256, MergeCommitSHA: observation.MergeCommitSHA,
		IntegratedAtMillis: nowMillis}
	value.ID = EvidenceID(value)
	value.SHA256 = EvidenceSHA256(value)
	return value
}
func ValidEvidence(value Evidence, binding Binding, effectID string) bool {
	return value.SchemaVersion == EvidenceSchemaVersion && validIdentity(value.ID) && value.BindingSHA256 == binding.SHA256 &&
		value.EffectID == effectID && validIdentity(value.ObservationID) && digestPattern.MatchString(value.ObservationSHA256) &&
		gitOIDPattern.MatchString(value.MergeCommitSHA) && len(value.MergeCommitSHA) == len(binding.CandidateSHA) &&
		value.IntegratedAtMillis >= 0 && digestPattern.MatchString(value.SHA256) && value.SHA256 == EvidenceSHA256(value) && value.ID == EvidenceID(value)
}

type Phase string

const (
	PhaseReady               Phase = "ready"
	PhaseIntentRecorded      Phase = "intent_recorded"
	PhaseDispatching         Phase = "dispatching"
	PhaseObservationRequired Phase = "observation_required"
	PhaseWaitingExternal     Phase = "waiting_external"
	PhaseComplete            Phase = "complete"
	PhaseNeedsYou            Phase = "needs_you"
	PhaseInvalidated         Phase = "invalidated"
)

type State struct {
	SchemaVersion      string              `json:"schemaVersion"`
	ID                 string              `json:"id"`
	Binding            Binding             `json:"binding"`
	Policy             Policy              `json:"policy"`
	Phase              Phase               `json:"phase"`
	HumanAuthorization *HumanAuthorization `json:"humanAuthorization,omitempty"`
	Integration        *Effect             `json:"integration,omitempty"`
	Evidence           *Evidence           `json:"evidence,omitempty"`
	NeedsYouCode       string              `json:"needsYouCode,omitempty"`
	WakeCondition      string              `json:"wakeCondition,omitempty"`
	InvalidationCode   string              `json:"invalidationCode,omitempty"`
}

func StateID(binding Binding) string  { return "integration-" + digest(binding.SHA256)[:32] }
func EffectID(binding Binding) string { return "pull-request-merge-" + digest(binding.SHA256)[:32] }
func newEffect(binding Binding, policy Policy) *Effect {
	return &Effect{ID: EffectID(binding), Kind: IntegrationEffectKind, Class: IntegrationEffectClass,
		Phase: EffectIntent, AttemptLimit: policy.AttemptLimit}
}
func NewState(binding Binding, policy Policy) (State, bool) {
	if !ValidBinding(binding) || !ValidPolicy(policy) || binding.PolicySHA256 != policy.SHA256 ||
		binding.ConfigurationSHA256 != policy.ConfigurationSHA256 {
		return State{}, false
	}
	value := State{SchemaVersion: StateSchemaVersion, ID: StateID(binding), Binding: binding, Policy: policy}
	if policy.Mode == ModeManual {
		value.Phase = PhaseReady
	} else {
		value.Phase, value.Integration = PhaseIntentRecorded, newEffect(binding, policy)
	}
	return value, ValidState(value)
}

func CloneState(value State) State {
	if value.HumanAuthorization != nil {
		copy := *value.HumanAuthorization
		value.HumanAuthorization = &copy
	}
	if value.Integration != nil {
		copy := *value.Integration
		if copy.Observation != nil {
			observation := *copy.Observation
			observation.GitBefore.ParentSHAs = slices.Clone(observation.GitBefore.ParentSHAs)
			observation.GitAfter.ParentSHAs = slices.Clone(observation.GitAfter.ParentSHAs)
			copy.Observation = &observation
		}
		value.Integration = &copy
	}
	if value.Evidence != nil {
		copy := *value.Evidence
		value.Evidence = &copy
	}
	return value
}

func AuthorizeManual(value State, authorization HumanAuthorization) (State, bool) {
	if !ValidState(value) || value.Policy.Mode != ModeManual || value.Phase != PhaseReady || !ValidAuthorization(authorization, value.Binding) {
		return value, false
	}
	value = CloneState(value)
	value.HumanAuthorization = &authorization
	value.Integration = newEffect(value.Binding, value.Policy)
	value.Phase = PhaseIntentRecorded
	return value, ValidState(value)
}

func active(phase Phase) bool {
	return slices.Contains([]Phase{PhaseIntentRecorded, PhaseDispatching, PhaseObservationRequired, PhaseWaitingExternal}, phase)
}
func RecordObservation(value State, observation Observation, nowMillis int64) (State, bool) {
	if !ValidState(value) || value.Integration == nil || !active(value.Phase) ||
		!ValidObservation(observation, value.Binding, value.Integration.ID, nowMillis) || observation.Attempt != value.Integration.Attempt {
		return value, false
	}
	value = CloneState(value)
	value.Integration.Observation = &observation
	if observation.Status == ObservationUnavailable {
		value.Phase = PhaseWaitingExternal
	} else {
		switch value.Integration.Phase {
		case EffectIntent:
			value.Phase = PhaseIntentRecorded
		case EffectDispatching:
			value.Phase = PhaseDispatching
		case EffectObservationRequired:
			value.Phase = PhaseObservationRequired
		}
	}
	return value, ValidState(value)
}
func BeginDispatch(value State) (State, bool) {
	if !ValidState(value) || value.Integration == nil || !active(value.Phase) || value.Integration.Observation == nil ||
		value.Integration.Observation.Status != ObservationReady || value.Integration.Attempt >= value.Integration.AttemptLimit {
		return value, false
	}
	value = CloneState(value)
	value.Integration.ConsumedObservationID = value.Integration.Observation.ID
	value.Integration.Observation = nil
	value.Integration.Attempt++
	value.Integration.Phase, value.Phase = EffectDispatching, PhaseDispatching
	return value, ValidState(value)
}
func RequireObservation(value State) (State, bool) {
	if !ValidState(value) || value.Phase != PhaseDispatching || value.Integration == nil || value.Integration.Phase != EffectDispatching {
		return value, false
	}
	value = CloneState(value)
	value.Integration.Phase, value.Phase = EffectObservationRequired, PhaseObservationRequired
	return value, ValidState(value)
}
func RetryAfterPreDispatch(value State) (State, bool) {
	if !ValidState(value) || value.Phase != PhaseDispatching || value.Integration == nil || value.Integration.Phase != EffectDispatching {
		return value, false
	}
	value = CloneState(value)
	value.Integration.Phase, value.Integration.Observation, value.Phase = EffectIntent, nil, PhaseIntentRecorded
	return value, ValidState(value)
}
func Complete(value State, observation Observation, nowMillis int64) (State, bool) {
	if !ValidState(value) || value.Integration == nil || !active(value.Phase) || observation.Status != ObservationIntegrated ||
		!ValidObservation(observation, value.Binding, value.Integration.ID, nowMillis) || observation.Attempt != value.Integration.Attempt {
		return value, false
	}
	value = CloneState(value)
	value.Integration.Observation = &observation
	value.Integration.Phase = EffectComplete
	evidence := EvidenceFromObservation(value.Binding, value.Integration.ID, observation, nowMillis)
	value.Evidence, value.Phase = &evidence, PhaseComplete
	return value, ValidState(value)
}
func Park(value State, code, wake string) (State, bool) {
	if !ValidState(value) || !validIdentity(code) || !validIdentity(wake) {
		return value, false
	}
	value = CloneState(value)
	value.Phase, value.NeedsYouCode, value.WakeCondition = PhaseNeedsYou, code, wake
	return value, ValidState(value)
}
func Invalidate(value State, code, wake string) State {
	if !ValidState(value) || !validIdentity(code) || !validIdentity(wake) {
		return value
	}
	value = CloneState(value)
	value.Phase, value.InvalidationCode, value.WakeCondition = PhaseInvalidated, code, wake
	value.NeedsYouCode = ""
	return value
}
func DispatchInFlight(value State) bool {
	return ValidState(value) && value.Integration != nil &&
		(value.Integration.Phase == EffectDispatching || value.Integration.Phase == EffectObservationRequired)
}

func validEffect(effect Effect, state State) bool {
	if effect.ID != EffectID(state.Binding) || effect.Kind != IntegrationEffectKind || effect.Class != IntegrationEffectClass ||
		effect.AttemptLimit != state.Policy.AttemptLimit || effect.Attempt > effect.AttemptLimit ||
		!slices.Contains([]EffectPhase{EffectIntent, EffectDispatching, EffectObservationRequired, EffectComplete}, effect.Phase) {
		return false
	}
	if effect.Observation != nil && (!ValidObservation(*effect.Observation, state.Binding, effect.ID,
		effect.Observation.ObservedAtMillis) || effect.Observation.Attempt != effect.Attempt) {
		return false
	}
	return effect.Attempt == 0 && effect.ConsumedObservationID == "" || effect.Attempt > 0 && validIdentity(effect.ConsumedObservationID)
}

func ValidState(value State) bool {
	if value.SchemaVersion != StateSchemaVersion || value.ID != StateID(value.Binding) || !ValidBinding(value.Binding) ||
		!ValidPolicy(value.Policy) || value.Binding.PolicySHA256 != value.Policy.SHA256 ||
		value.Binding.ConfigurationSHA256 != value.Policy.ConfigurationSHA256 || secretPattern.MatchString(value.NeedsYouCode) ||
		secretPattern.MatchString(value.WakeCondition) || secretPattern.MatchString(value.InvalidationCode) {
		return false
	}
	if value.HumanAuthorization != nil && !ValidAuthorization(*value.HumanAuthorization, value.Binding) {
		return false
	}
	if value.Policy.Mode == ModeManual && value.Phase != PhaseReady && value.Phase != PhaseNeedsYou &&
		value.Phase != PhaseInvalidated && value.HumanAuthorization == nil {
		return false
	}
	if value.Policy.Mode == ModeAutomatic && value.HumanAuthorization != nil {
		return false
	}
	if value.Integration != nil && !validEffect(*value.Integration, value) {
		return false
	}
	if value.Evidence != nil && (value.Integration == nil || !ValidEvidence(*value.Evidence, value.Binding, value.Integration.ID) ||
		value.Integration.Observation == nil || value.Evidence.ObservationID != value.Integration.Observation.ID ||
		value.Evidence.ObservationSHA256 != value.Integration.Observation.FactSHA256) {
		return false
	}
	switch value.Phase {
	case PhaseReady:
		return value.Policy.Mode == ModeManual && value.HumanAuthorization == nil && value.Integration == nil && value.Evidence == nil && value.NeedsYouCode == "" && value.WakeCondition == "" && value.InvalidationCode == ""
	case PhaseIntentRecorded:
		return value.Integration != nil && value.Integration.Phase == EffectIntent && value.Evidence == nil && value.NeedsYouCode == "" && value.WakeCondition == "" && value.InvalidationCode == ""
	case PhaseDispatching:
		return value.Integration != nil && value.Integration.Phase == EffectDispatching && value.Evidence == nil && value.NeedsYouCode == "" && value.WakeCondition == "" && value.InvalidationCode == ""
	case PhaseObservationRequired:
		return value.Integration != nil && value.Integration.Phase == EffectObservationRequired && value.Evidence == nil && value.NeedsYouCode == "" && value.WakeCondition == "" && value.InvalidationCode == ""
	case PhaseWaitingExternal:
		return value.Integration != nil && value.Integration.Observation != nil && value.Integration.Observation.Status == ObservationUnavailable && value.Evidence == nil && value.NeedsYouCode == "" && value.WakeCondition == "" && value.InvalidationCode == ""
	case PhaseComplete:
		return value.Integration != nil && value.Integration.Phase == EffectComplete && value.Integration.Observation != nil && value.Integration.Observation.Status == ObservationIntegrated && value.Evidence != nil && value.NeedsYouCode == "" && value.WakeCondition == "" && value.InvalidationCode == ""
	case PhaseNeedsYou:
		return validIdentity(value.NeedsYouCode) && validIdentity(value.WakeCondition) && value.InvalidationCode == ""
	case PhaseInvalidated:
		return validIdentity(value.InvalidationCode) && validIdentity(value.WakeCondition) && value.NeedsYouCode == ""
	default:
		return false
	}
}
