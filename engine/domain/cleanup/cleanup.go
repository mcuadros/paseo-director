// SPDX-License-Identifier: Apache-2.0

// Package cleanup owns the pure local-recovery and remote-cleanup contract.
// It contains no filesystem, Git, GitHub, Paseo, clock, or TaskStore access.
package cleanup

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"regexp"
	"slices"
	"strings"

	"github.com/mcuadros/director-engine/domain/safedata"
)

const (
	PolicySchemaVersion      = "director.cleanup-policy/v1"
	BindingSchemaVersion     = "director.cleanup-binding/v1"
	ObservationSchemaVersion = "director.cleanup-observation/v1"
	SnapshotSchemaVersion    = "director.cleanup-snapshot/v1"
	EvidenceSchemaVersion    = "director.cleanup-evidence/v1"
	StateSchemaVersion       = "director.cleanup-state/v1"

	MaximumObservationAgeMillis = int64(5_000)
	MaximumRetentionMillis      = int64(7 * 24 * 60 * 60 * 1_000)
	MaximumRecoveryEntries      = uint32(10_000)
	MaximumInspectedEntries     = uint32(25_000)
	MaximumAggregateBytes       = uint64(512 * 1024 * 1024)
	MaximumFileBytes            = uint64(256 * 1024 * 1024)
	MaximumStreamBufferBytes    = uint32(64 * 1024)
	MaximumPhaseMillis          = int64(180_000)
	MaximumLifecycleMillis      = int64(480_000)
	MaximumWorkerMillis         = int64(540_000)
	MaximumWorkerRSSBytes       = uint64(192 * 1024 * 1024)
	MinimumFreeSpaceBasisPoints = uint32(1_000)
	MaximumAttempts             = uint32(2)
	MaximumEffects              = 10
)

var (
	identifierPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.:@/-]{0,255}$`)
	digestPattern     = regexp.MustCompile(`^[0-9a-f]{64}$`)
	gitOIDPattern     = regexp.MustCompile(`^(?:[0-9a-f]{40}|[0-9a-f]{64})$`)
)

func digest(value any) string {
	encoded, err := json.Marshal(value)
	if err != nil {
		panic("marshal fixed cleanup value: " + err.Error())
	}
	sum := sha256.Sum256(encoded)
	return hex.EncodeToString(sum[:])
}

// DigestText creates a bounded public fingerprint without retaining private
// paths, branch-owner tokens, file names, or provider output.
func DigestText(value string) string { return digest(value) }

func validIdentity(value string) bool {
	return identifierPattern.MatchString(value) && !safedata.ContainsSecret(value)
}

func validOID(value string) bool { return gitOIDPattern.MatchString(value) }

func sameObjectFormat(values ...string) bool {
	length := 0
	for _, value := range values {
		if value == "" {
			continue
		}
		if !validOID(value) {
			return false
		}
		if length == 0 {
			length = len(value)
		} else if len(value) != length {
			return false
		}
	}
	return length != 0
}

// CancellationMode is frozen by Project -> Workspace -> Task inheritance.
// Retain terminates agents but deliberately creates no destructive Git or
// filesystem authority.
type CancellationMode string

const (
	CancellationRetain             CancellationMode = "retain"
	CancellationSnapshotThenDelete CancellationMode = "snapshot_then_delete"
)

type Trigger string

const (
	TriggerIntegrated Trigger = "integrated"
	TriggerCancelled  Trigger = "cancelled"
	TriggerFailed     Trigger = "failed"
)

type LifecycleState string

const (
	LifecycleActive    LifecycleState = "active"
	LifecycleRestored  LifecycleState = "restored"
	LifecycleReclaimed LifecycleState = "reclaimed"
)

// Policy contains the complete measured Linux cleanup envelope. A Project or
// Task may tighten it, but ValidPolicy rejects every expansion.
type Policy struct {
	SchemaVersion            string           `json:"schemaVersion"`
	ConfigurationSHA256      string           `json:"configurationSha256"`
	TerminateOnCompletion    bool             `json:"terminateOnCompletion"`
	CancellationMode         CancellationMode `json:"cancellationMode"`
	DeleteRemoteTaskBranch   bool             `json:"deleteRemoteTaskBranch"`
	RetentionMillis          int64            `json:"retentionMillis"`
	RecoveryEntries          uint32           `json:"recoveryEntries"`
	InspectedEntries         uint32           `json:"inspectedEntries"`
	AggregateBytes           uint64           `json:"aggregateBytes"`
	FileBytes                uint64           `json:"fileBytes"`
	StreamBufferBytes        uint32           `json:"streamBufferBytes"`
	PhaseMillis              int64            `json:"phaseMillis"`
	LifecycleMillis          int64            `json:"lifecycleMillis"`
	WorkerMillis             int64            `json:"workerMillis"`
	WorkerRSSBytes           uint64           `json:"workerRssBytes"`
	FreeSpaceFloorBasisPoint uint32           `json:"freeSpaceFloorBasisPoints"`
	AttemptLimit             uint32           `json:"attemptLimit"`
	SHA256                   string           `json:"sha256"`
}

func policyValue(value Policy) Policy  { value.SHA256 = ""; return value }
func PolicySHA256(value Policy) string { return digest(policyValue(value)) }
func SealPolicy(value Policy) Policy {
	value.SchemaVersion = PolicySchemaVersion
	value.SHA256 = PolicySHA256(value)
	return value
}

// NewPolicy returns PLAN section 15.5 defaults and ADR-0013 hard ceilings.
func NewPolicy(configurationSHA256 string) (Policy, bool) {
	value := Policy{
		SchemaVersion: PolicySchemaVersion, ConfigurationSHA256: configurationSHA256,
		TerminateOnCompletion: true, CancellationMode: CancellationSnapshotThenDelete,
		DeleteRemoteTaskBranch: true, RetentionMillis: MaximumRetentionMillis,
		RecoveryEntries: MaximumRecoveryEntries, InspectedEntries: MaximumInspectedEntries,
		AggregateBytes: MaximumAggregateBytes, FileBytes: MaximumFileBytes,
		StreamBufferBytes: MaximumStreamBufferBytes, PhaseMillis: MaximumPhaseMillis,
		LifecycleMillis: MaximumLifecycleMillis, WorkerMillis: MaximumWorkerMillis,
		WorkerRSSBytes: MaximumWorkerRSSBytes, FreeSpaceFloorBasisPoint: MinimumFreeSpaceBasisPoints,
		AttemptLimit: MaximumAttempts,
	}
	value.SHA256 = PolicySHA256(value)
	return value, ValidPolicy(value)
}

func ValidPolicy(value Policy) bool {
	return value.SchemaVersion == PolicySchemaVersion && digestPattern.MatchString(value.ConfigurationSHA256) &&
		(value.CancellationMode == CancellationRetain || value.CancellationMode == CancellationSnapshotThenDelete) &&
		value.RetentionMillis > 0 && value.RetentionMillis <= MaximumRetentionMillis &&
		value.RecoveryEntries > 0 && value.RecoveryEntries <= MaximumRecoveryEntries &&
		value.InspectedEntries > 0 && value.InspectedEntries <= MaximumInspectedEntries &&
		value.AggregateBytes > 0 && value.AggregateBytes <= MaximumAggregateBytes &&
		value.FileBytes > 0 && value.FileBytes <= MaximumFileBytes &&
		value.StreamBufferBytes > 0 && value.StreamBufferBytes <= MaximumStreamBufferBytes &&
		value.PhaseMillis > 0 && value.PhaseMillis <= MaximumPhaseMillis &&
		value.LifecycleMillis > 0 && value.LifecycleMillis <= MaximumLifecycleMillis &&
		value.WorkerMillis > 0 && value.WorkerMillis <= MaximumWorkerMillis &&
		value.WorkerRSSBytes > 0 && value.WorkerRSSBytes <= MaximumWorkerRSSBytes &&
		value.FreeSpaceFloorBasisPoint >= MinimumFreeSpaceBasisPoints && value.FreeSpaceFloorBasisPoint < 10_000 &&
		value.AttemptLimit > 0 && value.AttemptLimit <= MaximumAttempts &&
		digestPattern.MatchString(value.SHA256) && value.SHA256 == PolicySHA256(value)
}

// Binding freezes exact Task, Candidate, integration, repository, lifecycle,
// and Director-owned native-resource identities. Filesystem paths stay in the
// execution repository binding; only their hashes enter this public state.
type Binding struct {
	SchemaVersion             string `json:"schemaVersion"`
	ProjectID                 string `json:"projectId"`
	WorkspaceID               string `json:"workspaceId"`
	TaskID                    string `json:"taskId"`
	RunID                     string `json:"runId"`
	CandidateID               string `json:"candidateId"`
	CandidateSHA              string `json:"candidateSha"`
	BaseSHA                   string `json:"baseSha"`
	TreeSHA                   string `json:"treeSha"`
	CandidateGeneration       uint64 `json:"candidateGeneration"`
	TaskVersion               uint64 `json:"taskVersion"`
	ConfigurationSHA256       string `json:"configurationSha256"`
	RepositoryID              string `json:"repositoryId"`
	RepositoryBindingSHA256   string `json:"repositoryBindingSha256"`
	SourceDevice              uint64 `json:"sourceDevice"`
	SourceInode               uint64 `json:"sourceInode"`
	CommonDevice              uint64 `json:"commonDevice"`
	CommonInode               uint64 `json:"commonInode"`
	WorktreeDevice            uint64 `json:"worktreeDevice"`
	WorktreeInode             uint64 `json:"worktreeInode"`
	CanonicalRemoteSHA256     string `json:"canonicalRemoteSha256"`
	SourcePathSHA256          string `json:"sourcePathSha256"`
	CommonDirectorySHA256     string `json:"commonDirectorySha256"`
	WorktreePathSHA256        string `json:"worktreePathSha256"`
	Branch                    string `json:"branch"`
	BaseRef                   string `json:"baseRef"`
	WorktreeID                string `json:"worktreeId"`
	TaskAgentID               string `json:"taskAgentId"`
	TaskWorkspaceID           string `json:"taskWorkspaceId"`
	ReviewerAgentID           string `json:"reviewerAgentId,omitempty"`
	ReviewerWorkspaceID       string `json:"reviewerWorkspaceId,omitempty"`
	ReviewerCheckoutSHA256    string `json:"reviewerCheckoutSha256,omitempty"`
	OwnershipSHA256           string `json:"ownershipSha256"`
	CleanupAdmittedAtMillis   int64  `json:"cleanupAdmittedAtMillis"`
	ReclaimedEvidenceSHA256   string `json:"reclaimedEvidenceSha256,omitempty"`
	IntegrationKind           string `json:"integrationKind,omitempty"`
	IntegrationEvidenceID     string `json:"integrationEvidenceId,omitempty"`
	IntegrationEvidenceSHA256 string `json:"integrationEvidenceSha256,omitempty"`
	MergeCommitSHA            string `json:"mergeCommitSha,omitempty"`
	LeaseEpoch                uint64 `json:"leaseEpoch"`
	PolicySHA256              string `json:"policySha256"`
	SHA256                    string `json:"sha256"`
}

func bindingValue(value Binding) Binding { value.SHA256 = ""; return value }
func BindingSHA256(value Binding) string { return digest(bindingValue(value)) }
func SealBinding(value Binding) Binding {
	value.SchemaVersion = BindingSchemaVersion
	value.SHA256 = BindingSHA256(value)
	return value
}

func ValidBinding(value Binding) bool {
	if value.SchemaVersion != BindingSchemaVersion || !validIdentity(value.ProjectID) || !validIdentity(value.WorkspaceID) ||
		!validIdentity(value.TaskID) || !validIdentity(value.RunID) || !validIdentity(value.CandidateID) ||
		!sameObjectFormat(value.CandidateSHA, value.BaseSHA, value.TreeSHA) || value.CandidateSHA == value.BaseSHA ||
		value.CandidateGeneration == 0 || value.TaskVersion == 0 || !digestPattern.MatchString(value.ConfigurationSHA256) ||
		!validIdentity(value.RepositoryID) || !digestPattern.MatchString(value.RepositoryBindingSHA256) ||
		value.SourceDevice == 0 || value.SourceInode == 0 || value.CommonDevice == 0 || value.CommonInode == 0 ||
		value.WorktreeDevice == 0 || value.WorktreeInode == 0 ||
		!digestPattern.MatchString(value.CanonicalRemoteSHA256) || !digestPattern.MatchString(value.SourcePathSHA256) ||
		!digestPattern.MatchString(value.CommonDirectorySHA256) || !digestPattern.MatchString(value.WorktreePathSHA256) ||
		!strings.HasPrefix(value.Branch, "task/") || !validIdentity(value.Branch) || !strings.HasPrefix(value.BaseRef, "refs/heads/") ||
		!validIdentity(strings.TrimPrefix(value.BaseRef, "refs/heads/")) || !validIdentity(value.WorktreeID) ||
		!validIdentity(value.TaskAgentID) || !validIdentity(value.TaskWorkspaceID) || !digestPattern.MatchString(value.OwnershipSHA256) ||
		value.CleanupAdmittedAtMillis < 0 || value.LeaseEpoch == 0 || !digestPattern.MatchString(value.PolicySHA256) || !digestPattern.MatchString(value.SHA256) ||
		value.SHA256 != BindingSHA256(value) {
		return false
	}
	if (value.ReviewerAgentID == "") != (value.ReviewerWorkspaceID == "") ||
		(value.ReviewerAgentID == "") != (value.ReviewerCheckoutSHA256 == "") {
		return false
	}
	if value.ReclaimedEvidenceSHA256 != "" && !digestPattern.MatchString(value.ReclaimedEvidenceSHA256) {
		return false
	}
	if value.ReviewerAgentID != "" && (!validIdentity(value.ReviewerAgentID) || !validIdentity(value.ReviewerWorkspaceID) ||
		!digestPattern.MatchString(value.ReviewerCheckoutSHA256) || value.ReviewerAgentID == value.TaskAgentID ||
		value.ReviewerWorkspaceID == value.TaskWorkspaceID) {
		return false
	}
	if value.IntegrationKind == "" {
		return value.IntegrationEvidenceID == "" && value.IntegrationEvidenceSHA256 == "" && value.MergeCommitSHA == ""
	}
	return (value.IntegrationKind == "pull_request" || value.IntegrationKind == "direct") &&
		validIdentity(value.IntegrationEvidenceID) && digestPattern.MatchString(value.IntegrationEvidenceSHA256) &&
		validOID(value.MergeCommitSHA) && len(value.MergeCommitSHA) == len(value.CandidateSHA)
}

type ResourceKind string

const (
	ResourceReviewerAgent     ResourceKind = "reviewer_agent"
	ResourceTaskAgent         ResourceKind = "task_agent"
	ResourceSnapshot          ResourceKind = "recovery_snapshot"
	ResourceReviewerWorkspace ResourceKind = "reviewer_workspace"
	ResourceTaskWorkspace     ResourceKind = "task_workspace"
	ResourceWorktree          ResourceKind = "registered_worktree"
	ResourceRemoteRef         ResourceKind = "remote_task_ref"
	ResourceLocalRef          ResourceKind = "local_task_ref"
	ResourcePrivateArtifact   ResourceKind = "private_artifact_expiry"
	ResourceRecoveryRef       ResourceKind = "recovery_ref_expiry"
)

func validResourceKind(kind ResourceKind) bool {
	return slices.Contains([]ResourceKind{ResourceReviewerAgent, ResourceTaskAgent, ResourceSnapshot,
		ResourceReviewerWorkspace, ResourceTaskWorkspace, ResourceWorktree, ResourceRemoteRef,
		ResourceLocalRef, ResourcePrivateArtifact, ResourceRecoveryRef}, kind)
}

type Status string

const (
	StatusExactPresent Status = "exact_present"
	StatusClean        Status = "clean"
	StatusDirty        Status = "dirty"
	StatusVerified     Status = "verified"
	StatusTerminated   Status = "terminated"
	StatusAbsent       Status = "absent"
	StatusDifferent    Status = "different"
	StatusAmbiguous    Status = "ambiguous"
	StatusUnavailable  Status = "unavailable"
)

type Code string

const (
	CodeOK                    Code = "CLEANUP_OK"
	CodePolicyInvalid         Code = "CLEANUP_POLICY_INVALID"
	CodeBindingChanged        Code = "CLEANUP_BINDING_CHANGED"
	CodeIntegrationUnverified Code = "CLEANUP_INTEGRATION_UNVERIFIED"
	CodeRepositoryMismatch    Code = "CLEANUP_REPOSITORY_MISMATCH"
	CodeOwnerMismatch         Code = "CLEANUP_OWNER_MISMATCH"
	CodeLifecycleMismatch     Code = "CLEANUP_LIFECYCLE_MISMATCH"
	CodeWorktreeChanged       Code = "CLEANUP_WORKTREE_CHANGED"
	CodeUnsupportedContent    Code = "CLEANUP_UNSUPPORTED_CONTENT"
	CodeRecoveryLimit         Code = "CLEANUP_RECOVERY_LIMIT"
	CodeDiskPressure          Code = "CLEANUP_DISK_PRESSURE"
	CodeSnapshotUnverified    Code = "CLEANUP_SNAPSHOT_UNVERIFIED"
	CodeRefChanged            Code = "CLEANUP_REF_CHANGED"
	CodeConsumerPresent       Code = "CLEANUP_CONSUMER_PRESENT"
	CodeExternalUnavailable   Code = "CLEANUP_EXTERNAL_UNAVAILABLE"
	CodeResponseUnknown       Code = "CLEANUP_RESPONSE_UNKNOWN"
	CodeAttemptsExhausted     Code = "CLEANUP_ATTEMPTS_EXHAUSTED"
	CodeRetentionNotExpired   Code = "CLEANUP_RETENTION_NOT_EXPIRED"
	CodeTerminalDrift         Code = "CLEANUP_TERMINAL_DRIFT"
)

var codes = []Code{CodeOK, CodePolicyInvalid, CodeBindingChanged, CodeIntegrationUnverified,
	CodeRepositoryMismatch, CodeOwnerMismatch, CodeLifecycleMismatch, CodeWorktreeChanged,
	CodeUnsupportedContent, CodeRecoveryLimit, CodeDiskPressure, CodeSnapshotUnverified,
	CodeRefChanged, CodeConsumerPresent, CodeExternalUnavailable, CodeResponseUnknown,
	CodeAttemptsExhausted, CodeRetentionNotExpired, CodeTerminalDrift}

func validCode(code Code) bool { return slices.Contains(codes, code) }

// SnapshotEvidence describes only public aggregate recovery facts. Entry names,
// individual hashes, contents, and absolute artifact paths remain private.
type SnapshotEvidence struct {
	SchemaVersion         string `json:"schemaVersion"`
	ID                    string `json:"id"`
	BindingSHA256         string `json:"bindingSha256"`
	WorktreeRef           string `json:"worktreeRef"`
	WorktreeCommitSHA     string `json:"worktreeCommitSha"`
	WorktreeTreeSHA       string `json:"worktreeTreeSha"`
	IndexRef              string `json:"indexRef,omitempty"`
	IndexCommitSHA        string `json:"indexCommitSha,omitempty"`
	IndexTreeSHA          string `json:"indexTreeSha"`
	PrivateArtifactID     string `json:"privateArtifactId,omitempty"`
	PrivateArtifactSHA256 string `json:"privateArtifactSha256,omitempty"`
	EntryCount            uint32 `json:"entryCount"`
	AggregateBytes        uint64 `json:"aggregateBytes"`
	CreatedAtMillis       int64  `json:"createdAtMillis"`
	RetentionUntilMillis  int64  `json:"retentionUntilMillis"`
	SHA256                string `json:"sha256"`
}

func snapshotValue(value SnapshotEvidence) SnapshotEvidence { value.SHA256 = ""; return value }
func snapshotIdentity(value SnapshotEvidence) SnapshotEvidence {
	value.ID, value.SHA256 = "", ""
	return value
}
func SnapshotSHA256(value SnapshotEvidence) string { return digest(snapshotValue(value)) }
func SnapshotID(value SnapshotEvidence) string {
	return "recovery-" + digest(snapshotIdentity(value))[:32]
}
func SealSnapshot(value SnapshotEvidence) SnapshotEvidence {
	value.SchemaVersion = SnapshotSchemaVersion
	value.ID = SnapshotID(value)
	value.SHA256 = SnapshotSHA256(value)
	return value
}

func validRecoveryRef(value string, binding Binding) bool {
	prefix := "refs/director/recovery/" + binding.TaskID + "/" + binding.RunID + "/"
	return strings.HasPrefix(value, prefix) && validIdentity(value)
}

func ValidSnapshot(value SnapshotEvidence, binding Binding, policy Policy) bool {
	if value.SchemaVersion != SnapshotSchemaVersion || !validIdentity(value.ID) || value.BindingSHA256 != binding.SHA256 ||
		!validRecoveryRef(value.WorktreeRef, binding) || !sameObjectFormat(value.WorktreeCommitSHA, value.WorktreeTreeSHA,
		value.IndexTreeSHA, binding.CandidateSHA) || value.CreatedAtMillis < 0 || value.RetentionUntilMillis-value.CreatedAtMillis != policy.RetentionMillis ||
		value.EntryCount > policy.RecoveryEntries || value.AggregateBytes > policy.AggregateBytes ||
		!digestPattern.MatchString(value.SHA256) || value.SHA256 != SnapshotSHA256(value) || value.ID != SnapshotID(value) {
		return false
	}
	if (value.IndexRef == "") != (value.IndexCommitSHA == "") || value.IndexRef != "" &&
		(!validRecoveryRef(value.IndexRef, binding) || !sameObjectFormat(value.IndexCommitSHA, binding.CandidateSHA)) {
		return false
	}
	return (value.PrivateArtifactID == "") == (value.PrivateArtifactSHA256 == "") &&
		(value.PrivateArtifactID == "" || validIdentity(value.PrivateArtifactID) && digestPattern.MatchString(value.PrivateArtifactSHA256))
}

// Observation is a fresh path/content-free fact for one exact effect.
type Observation struct {
	SchemaVersion         string            `json:"schemaVersion"`
	ID                    string            `json:"id"`
	EffectID              string            `json:"effectId"`
	BindingSHA256         string            `json:"bindingSha256"`
	Kind                  ResourceKind      `json:"kind"`
	Attempt               uint32            `json:"attempt"`
	Status                Status            `json:"status"`
	Code                  Code              `json:"code"`
	ExternalID            string            `json:"externalId,omitempty"`
	CurrentOID            string            `json:"currentOid,omitempty"`
	CandidateTreeSHA      string            `json:"candidateTreeSha,omitempty"`
	ProspectiveTreeSHA    string            `json:"prospectiveTreeSha,omitempty"`
	IndexTreeSHA          string            `json:"indexTreeSha,omitempty"`
	Dirty                 bool              `json:"dirty"`
	Ignored               bool              `json:"ignored"`
	IgnoredEntries        uint32            `json:"ignoredEntries,omitempty"`
	IgnoredBytes          uint64            `json:"ignoredBytes,omitempty"`
	Archived              bool              `json:"archived"`
	ProcessAbsent         bool              `json:"processAbsent"`
	RegistrationAbsent    bool              `json:"registrationAbsent"`
	PriorDispatcherAbsent bool              `json:"priorDispatcherAbsent"`
	Snapshot              *SnapshotEvidence `json:"snapshot,omitempty"`
	ObservedAtMillis      int64             `json:"observedAtMillis"`
	MaximumAgeMillis      int64             `json:"maximumAgeMillis"`
	FactSHA256            string            `json:"factSha256"`
}

func observationValue(value Observation) Observation {
	value.FactSHA256 = ""
	return value
}
func ObservationSHA256(value Observation) string { return digest(observationValue(value)) }
func SealObservation(value Observation) Observation {
	value.SchemaVersion = ObservationSchemaVersion
	if value.ID == "" {
		value.ID = "cleanup-observation-" + ObservationSHA256(value)[:32]
	}
	value.FactSHA256 = ObservationSHA256(value)
	return value
}

func ValidObservation(value Observation, binding Binding, effectID string, kind ResourceKind, nowMillis int64) bool {
	if value.SchemaVersion != ObservationSchemaVersion || !validIdentity(value.ID) || value.EffectID != effectID ||
		value.BindingSHA256 != binding.SHA256 || value.Kind != kind || !validCode(value.Code) ||
		!slices.Contains([]Status{StatusExactPresent, StatusClean, StatusDirty, StatusVerified, StatusTerminated,
			StatusAbsent, StatusDifferent, StatusAmbiguous, StatusUnavailable}, value.Status) ||
		value.ObservedAtMillis < 0 || value.ObservedAtMillis > nowMillis || value.MaximumAgeMillis <= 0 ||
		value.MaximumAgeMillis > MaximumObservationAgeMillis || nowMillis-value.ObservedAtMillis > value.MaximumAgeMillis ||
		!digestPattern.MatchString(value.FactSHA256) || value.FactSHA256 != ObservationSHA256(value) {
		return false
	}
	if value.Status == StatusUnavailable {
		return value.Code == CodeExternalUnavailable && value.CurrentOID == "" && value.Snapshot == nil
	}
	if value.Status == StatusDifferent || value.Status == StatusAmbiguous {
		return value.Code != CodeOK
	}
	if value.Code != CodeOK {
		return false
	}
	if value.CurrentOID != "" && (!validOID(value.CurrentOID) || len(value.CurrentOID) != len(binding.CandidateSHA)) {
		return false
	}
	if value.Snapshot != nil && (value.Snapshot.SchemaVersion != SnapshotSchemaVersion ||
		value.Snapshot.BindingSHA256 != binding.SHA256 || !digestPattern.MatchString(value.Snapshot.SHA256) ||
		value.Snapshot.SHA256 != SnapshotSHA256(*value.Snapshot) || value.Snapshot.ID != SnapshotID(*value.Snapshot)) {
		return false
	}
	return true
}

type EffectPhase string

const (
	EffectIntent              EffectPhase = "intent_recorded"
	EffectDispatching         EffectPhase = "dispatching"
	EffectObservationRequired EffectPhase = "observation_required"
	EffectComplete            EffectPhase = "complete"
	EffectRetained            EffectPhase = "retained"
)

type Effect struct {
	ID                    string       `json:"id"`
	Kind                  ResourceKind `json:"kind"`
	Class                 string       `json:"class"`
	Phase                 EffectPhase  `json:"phase"`
	Attempt               uint32       `json:"attempt"`
	AttemptLimit          uint32       `json:"attemptLimit"`
	Observation           *Observation `json:"observation,omitempty"`
	ConsumedObservationID string       `json:"consumedObservationId,omitempty"`
}

type Phase string

const (
	PhaseIntent   Phase = "intent_recorded"
	PhaseCleaning Phase = "cleaning"
	PhaseWaiting  Phase = "waiting_external"
	PhaseRetained Phase = "retained"
	PhaseComplete Phase = "complete"
	PhaseNeedsYou Phase = "needs_you"
)

type Evidence struct {
	SchemaVersion     string `json:"schemaVersion"`
	ID                string `json:"id"`
	BindingSHA256     string `json:"bindingSha256"`
	SnapshotSHA256    string `json:"snapshotSha256,omitempty"`
	CompletedAtMillis int64  `json:"completedAtMillis"`
	SHA256            string `json:"sha256"`
}

func evidenceValue(value Evidence) Evidence    { value.SHA256 = ""; return value }
func evidenceIdentity(value Evidence) Evidence { value.ID, value.SHA256 = "", ""; return value }
func EvidenceSHA256(value Evidence) string     { return digest(evidenceValue(value)) }
func EvidenceID(value Evidence) string {
	return "cleanup-evidence-" + digest(evidenceIdentity(value))[:32]
}
func SealEvidence(value Evidence) Evidence {
	value.SchemaVersion = EvidenceSchemaVersion
	value.ID = EvidenceID(value)
	value.SHA256 = EvidenceSHA256(value)
	return value
}

type State struct {
	SchemaVersion          string            `json:"schemaVersion"`
	ID                     string            `json:"id"`
	Binding                Binding           `json:"binding"`
	Policy                 Policy            `json:"policy"`
	Trigger                Trigger           `json:"trigger"`
	LifecycleState         LifecycleState    `json:"lifecycleState"`
	Phase                  Phase             `json:"phase"`
	CleanupAuthorized      bool              `json:"cleanupAuthorized"`
	Effects                []Effect          `json:"effects"`
	Snapshot               *SnapshotEvidence `json:"snapshot,omitempty"`
	RetentionEffects       []Effect          `json:"retentionEffects,omitempty"`
	RetentionComplete      bool              `json:"retentionComplete"`
	RetentionNeedsYouCode  string            `json:"retentionNeedsYouCode,omitempty"`
	RetentionWakeCondition string            `json:"retentionWakeCondition,omitempty"`
	Evidence               *Evidence         `json:"evidence,omitempty"`
	NeedsYouCode           string            `json:"needsYouCode,omitempty"`
	WakeCondition          string            `json:"wakeCondition,omitempty"`
}

func StateID(binding Binding, trigger Trigger) string {
	return "cleanup-" + digest(struct{ Binding, Trigger string }{binding.SHA256, string(trigger)})[:32]
}

func effectID(binding Binding, trigger Trigger, kind ResourceKind) string {
	return "cleanup-effect-" + digest(struct{ Binding, Trigger, Kind string }{binding.SHA256, string(trigger), string(kind)})[:32]
}

func effectClass(kind ResourceKind) string {
	switch kind {
	case ResourceReviewerAgent, ResourceTaskAgent, ResourceReviewerWorkspace, ResourceTaskWorkspace:
		return "idempotent_close"
	case ResourceSnapshot:
		return "unique_create"
	default:
		return "destructive_terminal"
	}
}

func appendEffect(result []Effect, binding Binding, trigger Trigger, policy Policy, kind ResourceKind) []Effect {
	return append(result, Effect{ID: effectID(binding, trigger, kind), Kind: kind, Class: effectClass(kind),
		Phase: EffectIntent, AttemptLimit: policy.AttemptLimit})
}

func initialEffects(binding Binding, policy Policy, trigger Trigger) []Effect {
	var result []Effect
	if binding.ReviewerAgentID != "" {
		result = appendEffect(result, binding, trigger, policy, ResourceReviewerAgent)
	}
	result = appendEffect(result, binding, trigger, policy, ResourceTaskAgent)
	if trigger != TriggerIntegrated && policy.CancellationMode == CancellationRetain {
		return result
	}
	result = appendEffect(result, binding, trigger, policy, ResourceSnapshot)
	if binding.ReviewerWorkspaceID != "" {
		result = appendEffect(result, binding, trigger, policy, ResourceReviewerWorkspace)
	}
	result = appendEffect(result, binding, trigger, policy, ResourceTaskWorkspace)
	result = appendEffect(result, binding, trigger, policy, ResourceWorktree)
	if trigger == TriggerIntegrated && policy.DeleteRemoteTaskBranch {
		result = appendEffect(result, binding, trigger, policy, ResourceRemoteRef)
	}
	result = appendEffect(result, binding, trigger, policy, ResourceLocalRef)
	return result
}

func NewState(binding Binding, policy Policy, trigger Trigger, lifecycle LifecycleState) (State, bool) {
	if !ValidBinding(binding) || !ValidPolicy(policy) || binding.PolicySHA256 != policy.SHA256 ||
		binding.ConfigurationSHA256 != policy.ConfigurationSHA256 ||
		!slices.Contains([]Trigger{TriggerIntegrated, TriggerCancelled, TriggerFailed}, trigger) ||
		!slices.Contains([]LifecycleState{LifecycleActive, LifecycleRestored, LifecycleReclaimed}, lifecycle) ||
		(trigger == TriggerIntegrated) != (binding.IntegrationKind != "") {
		return State{}, false
	}
	state := State{SchemaVersion: StateSchemaVersion, ID: StateID(binding, trigger), Binding: binding, Policy: policy,
		Trigger: trigger, LifecycleState: lifecycle, Phase: PhaseIntent,
		Effects: initialEffects(binding, policy, trigger)}
	if trigger == TriggerIntegrated && !policy.TerminateOnCompletion {
		state.Phase = PhaseRetained
		state.Effects = nil
	}
	return state, ValidState(state)
}

func CloneState(value State) State {
	value.Effects = slices.Clone(value.Effects)
	for index := range value.Effects {
		if value.Effects[index].Observation != nil {
			observation := *value.Effects[index].Observation
			if observation.Snapshot != nil {
				snapshot := *observation.Snapshot
				observation.Snapshot = &snapshot
			}
			value.Effects[index].Observation = &observation
		}
	}
	value.RetentionEffects = slices.Clone(value.RetentionEffects)
	for index := range value.RetentionEffects {
		if value.RetentionEffects[index].Observation != nil {
			observation := *value.RetentionEffects[index].Observation
			value.RetentionEffects[index].Observation = &observation
		}
	}
	if value.Snapshot != nil {
		copy := *value.Snapshot
		value.Snapshot = &copy
	}
	if value.Evidence != nil {
		copy := *value.Evidence
		value.Evidence = &copy
	}
	return value
}

func EffectIndex(value State, kind ResourceKind) int {
	for index := range value.Effects {
		if value.Effects[index].Kind == kind {
			return index
		}
	}
	return -1
}

func RetentionEffectIndex(value State, kind ResourceKind) int {
	for index := range value.RetentionEffects {
		if value.RetentionEffects[index].Kind == kind {
			return index
		}
	}
	return -1
}

func RecordObservation(value State, kind ResourceKind, observation Observation, nowMillis int64) (State, bool) {
	index := EffectIndex(value, kind)
	if !ValidState(value) || index < 0 || value.Effects[index].Phase == EffectComplete ||
		!ValidObservation(observation, value.Binding, value.Effects[index].ID, kind, nowMillis) ||
		observation.Attempt != value.Effects[index].Attempt {
		return value, false
	}
	value = CloneState(value)
	value.Effects[index].Observation = &observation
	if observation.Status == StatusUnavailable {
		value.Phase = PhaseWaiting
	} else {
		value.Phase = PhaseCleaning
	}
	return value, ValidState(value)
}

func BeginDispatch(value State, kind ResourceKind) (State, bool) {
	index := EffectIndex(value, kind)
	if !ValidState(value) || index < 0 || value.Effects[index].Observation == nil ||
		value.Effects[index].Attempt >= value.Effects[index].AttemptLimit {
		return value, false
	}
	value = CloneState(value)
	effect := &value.Effects[index]
	effect.ConsumedObservationID = effect.Observation.ID
	effect.Observation = nil
	effect.Attempt++
	effect.Phase = EffectDispatching
	value.Phase = PhaseCleaning
	return value, ValidState(value)
}

func RequireObservation(value State, kind ResourceKind) (State, bool) {
	index := EffectIndex(value, kind)
	if !ValidState(value) || index < 0 || value.Effects[index].Phase != EffectDispatching {
		return value, false
	}
	value = CloneState(value)
	value.Effects[index].Phase = EffectObservationRequired
	return value, ValidState(value)
}

func CompleteEffect(value State, kind ResourceKind, snapshot *SnapshotEvidence) (State, bool) {
	index := EffectIndex(value, kind)
	if !ValidState(value) || index < 0 || value.Effects[index].Observation == nil {
		return value, false
	}
	value = CloneState(value)
	effect := &value.Effects[index]
	if snapshot != nil {
		if kind != ResourceSnapshot || !ValidSnapshot(*snapshot, value.Binding, value.Policy) {
			return value, false
		}
		copy := *snapshot
		value.Snapshot = &copy
		value.RetentionEffects = nil
		if snapshot.PrivateArtifactID != "" {
			value.RetentionEffects = append(value.RetentionEffects, Effect{ID: effectID(value.Binding, value.Trigger, ResourcePrivateArtifact),
				Kind: ResourcePrivateArtifact, Class: effectClass(ResourcePrivateArtifact), Phase: EffectRetained, AttemptLimit: value.Policy.AttemptLimit})
		}
		value.RetentionEffects = append(value.RetentionEffects, Effect{ID: effectID(value.Binding, value.Trigger, ResourceRecoveryRef),
			Kind: ResourceRecoveryRef, Class: effectClass(ResourceRecoveryRef), Phase: EffectRetained, AttemptLimit: value.Policy.AttemptLimit})
	}
	effect.Phase = EffectComplete
	effect.Observation = nil
	if kind == ResourceSnapshot {
		// Integrated prospective-tree-clean work needs no Git ref when there is
		// no ignored/private material. Every unintegrated cleanup requires the
		// exact verified recovery snapshot.
		if value.Trigger == TriggerIntegrated && snapshot == nil || snapshot != nil {
			value.CleanupAuthorized = true
		}
	}
	value.Phase = PhaseCleaning
	return value, ValidState(value)
}

// StartRetentionExpiry converts exactly one seven-day retained resource into
// an intent. Before the boundary it is a scheduled no-op, never Needs you.
func StartRetentionExpiry(value State, nowMillis int64) (State, bool) {
	if !ValidState(value) || value.Phase != PhaseComplete || value.Snapshot == nil || value.RetentionComplete ||
		value.RetentionNeedsYouCode != "" || nowMillis < value.Snapshot.RetentionUntilMillis {
		return value, false
	}
	value = CloneState(value)
	for index := range value.RetentionEffects {
		if value.RetentionEffects[index].Phase == EffectRetained {
			for prior := 0; prior < index; prior++ {
				if value.RetentionEffects[prior].Phase != EffectComplete {
					return value, false
				}
			}
			value.RetentionEffects[index].Phase = EffectIntent
			return value, ValidState(value)
		}
		if value.RetentionEffects[index].Phase != EffectComplete {
			return value, false
		}
	}
	value.RetentionComplete = true
	return value, ValidState(value)
}

func RecordRetentionObservation(value State, kind ResourceKind, observation Observation, nowMillis int64) (State, bool) {
	index := RetentionEffectIndex(value, kind)
	if !ValidState(value) || index < 0 || value.RetentionEffects[index].Phase == EffectRetained ||
		value.RetentionEffects[index].Phase == EffectComplete || !ValidObservation(observation, value.Binding,
		value.RetentionEffects[index].ID, kind, nowMillis) || observation.Attempt != value.RetentionEffects[index].Attempt {
		return value, false
	}
	value = CloneState(value)
	value.RetentionEffects[index].Observation = &observation
	return value, ValidState(value)
}

func BeginRetentionDispatch(value State, kind ResourceKind) (State, bool) {
	index := RetentionEffectIndex(value, kind)
	if !ValidState(value) || index < 0 || value.RetentionEffects[index].Phase != EffectIntent ||
		value.RetentionEffects[index].Observation == nil || value.RetentionEffects[index].Observation.Status != StatusExactPresent ||
		value.RetentionEffects[index].Attempt >= value.RetentionEffects[index].AttemptLimit {
		return value, false
	}
	value = CloneState(value)
	effect := &value.RetentionEffects[index]
	effect.ConsumedObservationID = effect.Observation.ID
	effect.Observation = nil
	effect.Attempt++
	effect.Phase = EffectDispatching
	return value, ValidState(value)
}

func RequireRetentionObservation(value State, kind ResourceKind) (State, bool) {
	index := RetentionEffectIndex(value, kind)
	if !ValidState(value) || index < 0 || value.RetentionEffects[index].Phase != EffectDispatching {
		return value, false
	}
	value = CloneState(value)
	value.RetentionEffects[index].Phase = EffectObservationRequired
	return value, ValidState(value)
}

func CompleteRetentionEffect(value State, kind ResourceKind) (State, bool) {
	index := RetentionEffectIndex(value, kind)
	if !ValidState(value) || index < 0 || value.RetentionEffects[index].Observation == nil ||
		value.RetentionEffects[index].Observation.Status != StatusAbsent {
		return value, false
	}
	value = CloneState(value)
	value.RetentionEffects[index].Phase = EffectComplete
	value.RetentionEffects[index].Observation = nil
	allComplete := true
	for _, effect := range value.RetentionEffects {
		allComplete = allComplete && effect.Phase == EffectComplete
	}
	value.RetentionComplete = allComplete
	return value, ValidState(value)
}

func ParkRetention(value State, code Code, wake string) (State, bool) {
	if !ValidState(value) || value.Phase != PhaseComplete || !validCode(code) || code == CodeOK || !validIdentity(wake) {
		return value, false
	}
	value = CloneState(value)
	value.RetentionNeedsYouCode, value.RetentionWakeCondition = string(code), wake
	value.CleanupAuthorized = false
	return value, ValidState(value)
}

func Complete(value State, nowMillis int64) (State, bool) {
	if !ValidState(value) || value.Phase == PhaseNeedsYou || value.Phase == PhaseComplete || value.Phase == PhaseRetained {
		return value, false
	}
	for _, effect := range value.Effects {
		if effect.Phase != EffectComplete {
			return value, false
		}
	}
	value = CloneState(value)
	if value.Trigger != TriggerIntegrated && value.Policy.CancellationMode == CancellationRetain {
		value.Phase = PhaseRetained
		value.CleanupAuthorized = false
		return value, ValidState(value)
	}
	if !value.CleanupAuthorized {
		return value, false
	}
	evidence := SealEvidence(Evidence{BindingSHA256: value.Binding.SHA256, CompletedAtMillis: nowMillis})
	if value.Snapshot != nil {
		evidence.SnapshotSHA256 = value.Snapshot.SHA256
		evidence = SealEvidence(evidence)
	}
	value.Evidence = &evidence
	value.Phase = PhaseComplete
	return value, ValidState(value)
}

func Park(value State, code Code, wake string) (State, bool) {
	if !ValidState(value) || !validCode(code) || code == CodeOK || !validIdentity(wake) {
		return value, false
	}
	value = CloneState(value)
	value.Phase, value.NeedsYouCode, value.WakeCondition = PhaseNeedsYou, string(code), wake
	value.CleanupAuthorized = false
	return value, ValidState(value)
}

func validEffect(effect Effect, state State, priorComplete bool) bool {
	if !validResourceKind(effect.Kind) || effect.ID != effectID(state.Binding, state.Trigger, effect.Kind) ||
		effect.Class != effectClass(effect.Kind) || effect.AttemptLimit != state.Policy.AttemptLimit || effect.Attempt > effect.AttemptLimit ||
		!slices.Contains([]EffectPhase{EffectIntent, EffectDispatching, EffectObservationRequired, EffectComplete, EffectRetained}, effect.Phase) {
		return false
	}
	if !priorComplete && effect.Phase != EffectIntent {
		return false
	}
	if effect.Observation != nil && !ValidObservation(*effect.Observation, state.Binding, effect.ID, effect.Kind, effect.Observation.ObservedAtMillis) {
		return false
	}
	return effect.Attempt == 0 && effect.ConsumedObservationID == "" || effect.Attempt > 0 && validIdentity(effect.ConsumedObservationID)
}

func ValidState(value State) bool {
	if value.SchemaVersion != StateSchemaVersion || value.ID != StateID(value.Binding, value.Trigger) ||
		!ValidBinding(value.Binding) || !ValidPolicy(value.Policy) || value.Binding.PolicySHA256 != value.Policy.SHA256 ||
		value.Binding.ConfigurationSHA256 != value.Policy.ConfigurationSHA256 ||
		!slices.Contains([]Trigger{TriggerIntegrated, TriggerCancelled, TriggerFailed}, value.Trigger) ||
		!slices.Contains([]LifecycleState{LifecycleActive, LifecycleRestored, LifecycleReclaimed}, value.LifecycleState) ||
		(value.Trigger == TriggerIntegrated) != (value.Binding.IntegrationKind != "") ||
		!slices.Contains([]Phase{PhaseIntent, PhaseCleaning, PhaseWaiting, PhaseRetained, PhaseComplete, PhaseNeedsYou}, value.Phase) ||
		len(value.Effects) > MaximumEffects || len(value.RetentionEffects) > 2 || safedata.ContainsSecret(value.NeedsYouCode) || safedata.ContainsSecret(value.WakeCondition) ||
		safedata.ContainsSecret(value.RetentionNeedsYouCode) || safedata.ContainsSecret(value.RetentionWakeCondition) {
		return false
	}
	if value.Trigger == TriggerIntegrated && !value.Policy.TerminateOnCompletion {
		return value.Phase == PhaseRetained && len(value.Effects) == 0 && !value.CleanupAuthorized && value.Snapshot == nil && value.Evidence == nil
	}
	expected := initialEffects(value.Binding, value.Policy, value.Trigger)
	if len(expected) != len(value.Effects) {
		return false
	}
	priorComplete := true
	for index := range value.Effects {
		if value.Effects[index].Kind != expected[index].Kind || !validEffect(value.Effects[index], value, priorComplete) {
			return false
		}
		priorComplete = priorComplete && value.Effects[index].Phase == EffectComplete
	}
	if value.Snapshot != nil && !ValidSnapshot(*value.Snapshot, value.Binding, value.Policy) {
		return false
	}
	if value.Snapshot == nil {
		if len(value.RetentionEffects) != 0 || value.RetentionComplete || value.RetentionNeedsYouCode != "" || value.RetentionWakeCondition != "" {
			return false
		}
	} else {
		expectedRetention := []ResourceKind{ResourceRecoveryRef}
		if value.Snapshot.PrivateArtifactID != "" {
			expectedRetention = []ResourceKind{ResourcePrivateArtifact, ResourceRecoveryRef}
		}
		if len(value.RetentionEffects) != len(expectedRetention) {
			return false
		}
		prior := true
		allComplete := true
		for index, kind := range expectedRetention {
			effect := value.RetentionEffects[index]
			if effect.Kind != kind || !validEffect(effect, value, true) || (!prior && effect.Phase != EffectRetained) {
				return false
			}
			prior = prior && effect.Phase == EffectComplete
			allComplete = allComplete && effect.Phase == EffectComplete
		}
		if value.RetentionComplete != allComplete {
			return false
		}
		if (value.RetentionNeedsYouCode == "") != (value.RetentionWakeCondition == "") {
			return false
		}
	}
	if value.CleanupAuthorized && EffectIndex(value, ResourceSnapshot) >= 0 {
		snapshotEffect := value.Effects[EffectIndex(value, ResourceSnapshot)]
		if snapshotEffect.Phase != EffectComplete || value.Trigger != TriggerIntegrated && value.Snapshot == nil {
			return false
		}
	}
	if value.Evidence != nil {
		if value.Phase != PhaseComplete || value.Evidence.SchemaVersion != EvidenceSchemaVersion ||
			value.Evidence.BindingSHA256 != value.Binding.SHA256 || value.Evidence.CompletedAtMillis < 0 ||
			!digestPattern.MatchString(value.Evidence.SHA256) || value.Evidence.SHA256 != EvidenceSHA256(*value.Evidence) ||
			value.Evidence.ID != EvidenceID(*value.Evidence) ||
			(value.Snapshot == nil) != (value.Evidence.SnapshotSHA256 == "") ||
			value.Snapshot != nil && value.Evidence.SnapshotSHA256 != value.Snapshot.SHA256 {
			return false
		}
	}
	if value.Phase == PhaseNeedsYou {
		return value.NeedsYouCode != "" && value.WakeCondition != "" && !value.CleanupAuthorized && value.Evidence == nil
	}
	if value.NeedsYouCode != "" || value.WakeCondition != "" {
		return false
	}
	if value.Phase == PhaseRetained {
		return !value.CleanupAuthorized && value.Evidence == nil
	}
	if value.Phase == PhaseComplete {
		return value.Evidence != nil && priorComplete && (value.CleanupAuthorized && value.RetentionNeedsYouCode == "" ||
			!value.CleanupAuthorized && value.RetentionNeedsYouCode != "")
	}
	return value.Evidence == nil
}
