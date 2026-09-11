// SPDX-License-Identifier: Apache-2.0

// Package candidate owns the pure exact-SHA Candidate admission contract.
// Git adapters supply observations; only engine application code may evaluate
// them and persist an admitted Candidate.
package candidate

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"regexp"
	"slices"
	"strconv"
	"strings"
)

const (
	ClaimSchemaVersion       = "director.candidate-claim/v1"
	ObservationSchemaVersion = "director.candidate-observation/v1"
	ManifestSchemaVersion    = "director.candidate-manifest/v1"
	AuthoritySchemaVersion   = "director.candidate-authority/v1"
	MaximumObservationAgeMS  = int64(5_000)
)

var (
	identifierPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.:@/-]{0,255}$`)
	digestPattern     = regexp.MustCompile(`^[0-9a-f]{64}$`)
	gitOIDPattern     = regexp.MustCompile(`^(?:[0-9a-f]{40}|[0-9a-f]{64})$`)
)

// GraphPolicy is frozen before observation. The default admits exactly one
// direct-parent commit. Broader graph shapes require an explicit policy value.
type GraphPolicy struct {
	AllowMergeCommit     bool `json:"allowMergeCommit"`
	AllowNonDirectParent bool `json:"allowNonDirectParent"`
}

// Claim is the server-enriched form of one author's closed completed claim.
// Scope, versions, lease, repository context, and hashes are read from durable
// engine facts; the author cannot select them through the claim schema.
type Claim struct {
	SchemaVersion       string      `json:"schemaVersion"`
	ID                  string      `json:"id"`
	ProjectID           string      `json:"projectId"`
	WorkspaceID         string      `json:"workspaceId"`
	TaskID              string      `json:"taskId"`
	RunID               string      `json:"runId"`
	ActorID             string      `json:"actorId"`
	WorktreeID          string      `json:"worktreeId"`
	Branch              string      `json:"branch"`
	BaseRef             string      `json:"baseRef"`
	CandidateSHA        string      `json:"candidateSha"`
	BaseSHA             string      `json:"baseSha"`
	LeaseEpoch          uint64      `json:"leaseEpoch"`
	ExpectedRunVersion  uint64      `json:"expectedRunVersion"`
	TaskVersion         uint64      `json:"taskVersion"`
	AcceptanceSHA256    string      `json:"acceptanceSha256"`
	ConfigurationSHA256 string      `json:"configurationSha256"`
	ProfileSHA256       string      `json:"profileSha256"`
	ContextSHA256       string      `json:"contextSha256"`
	DecisionsSHA256     string      `json:"decisionsSha256"`
	FindingsSHA256      string      `json:"findingsSha256"`
	GraphPolicy         GraphPolicy `json:"graphPolicy"`
}

// Code is the closed path/content-free Candidate diagnostic vocabulary.
type Code string

const (
	CodeOK                         Code = "CANDIDATE_OK"
	CodeClaimInvalid               Code = "CANDIDATE_CLAIM_INVALID"
	CodeObservationInvalid         Code = "CANDIDATE_OBSERVATION_INVALID"
	CodeObservationStale           Code = "CANDIDATE_OBSERVATION_STALE"
	CodeObservationBindingMismatch Code = "CANDIDATE_OBSERVATION_BINDING_MISMATCH"
	CodeRepositoryMismatch         Code = "CANDIDATE_REPOSITORY_MISMATCH"
	CodeRemoteMismatch             Code = "CANDIDATE_REMOTE_MISMATCH"
	CodePathAlias                  Code = "CANDIDATE_PATH_ALIAS_AMBIGUOUS"
	CodeWorktreeRegistration       Code = "CANDIDATE_WORKTREE_REGISTRATION_MISMATCH"
	CodeObjectMissing              Code = "CANDIDATE_OBJECT_MISSING"
	CodeObjectAmbiguous            Code = "CANDIDATE_OBJECT_AMBIGUOUS"
	CodeObjectStoreAmbiguous       Code = "CANDIDATE_OBJECT_STORE_AMBIGUOUS"
	CodeBranchMoved                Code = "CANDIDATE_BRANCH_MOVED"
	CodeBaseMoved                  Code = "CANDIDATE_BASE_MOVED"
	CodeGraphRejected              Code = "CANDIDATE_GRAPH_REJECTED"
	CodeWorktreeDirty              Code = "CANDIDATE_WORKTREE_DIRTY"
	CodeIndexDirty                 Code = "CANDIDATE_INDEX_DIRTY"
	CodeUntracked                  Code = "CANDIDATE_UNTRACKED_CONTENT"
	CodeIgnored                    Code = "CANDIDATE_IGNORED_CONTENT"
	CodeSubmodule                  Code = "CANDIDATE_SUBMODULE_AMBIGUOUS"
	CodeConflict                   Code = "CANDIDATE_CONFLICT_PRESENT"
	CodeIntentToAdd                Code = "CANDIDATE_INTENT_TO_ADD_PRESENT"
	CodeSparseCheckout             Code = "CANDIDATE_SPARSE_CHECKOUT_AMBIGUOUS"
	CodeUnsafePath                 Code = "CANDIDATE_PATH_UNSAFE"
	CodeTOCTOU                     Code = "CANDIDATE_OBSERVATION_CHANGED"
	CodeGitUnavailable             Code = "CANDIDATE_GIT_UNAVAILABLE"
	CodeTaskVersionChanged         Code = "CANDIDATE_TASK_VERSION_CHANGED"
	CodeConfigurationChanged       Code = "CANDIDATE_CONFIGURATION_CHANGED"
	CodeDecisionsChanged           Code = "CANDIDATE_DECISIONS_CHANGED"
	CodeFindingsChanged            Code = "CANDIDATE_FINDINGS_CHANGED"
	CodeCandidateChanged           Code = "CANDIDATE_CHANGED"
)

var codes = []Code{
	CodeOK, CodeClaimInvalid, CodeObservationInvalid, CodeObservationStale,
	CodeObservationBindingMismatch, CodeRepositoryMismatch, CodeRemoteMismatch,
	CodePathAlias, CodeWorktreeRegistration, CodeObjectMissing, CodeObjectAmbiguous,
	CodeObjectStoreAmbiguous, CodeBranchMoved, CodeBaseMoved, CodeGraphRejected,
	CodeWorktreeDirty, CodeIndexDirty, CodeUntracked, CodeIgnored, CodeSubmodule,
	CodeConflict, CodeIntentToAdd, CodeSparseCheckout, CodeUnsafePath, CodeTOCTOU,
	CodeGitUnavailable,
	CodeTaskVersionChanged, CodeConfigurationChanged, CodeDecisionsChanged, CodeFindingsChanged, CodeCandidateChanged,
}

// Observation is one bounded Git/filesystem fact. It deliberately contains no
// path, file name, raw command output, or credential-bearing value.
type Observation struct {
	SchemaVersion           string `json:"schemaVersion"`
	ID                      string `json:"id"`
	ClaimSHA256             string `json:"claimSha256"`
	RepositoryBindingSHA256 string `json:"repositoryBindingSha256"`
	ObservedAtMillis        int64  `json:"observedAtMillis"`
	MaximumAgeMillis        int64  `json:"maximumAgeMillis"`
	ObjectFormat            string `json:"objectFormat"`
	CommitSHA               string `json:"commitSha"`
	BaseSHA                 string `json:"baseSha"`
	ParentSHA               string `json:"parentSha"`
	TreeSHA                 string `json:"treeSha"`
	BranchHeadSHA           string `json:"branchHeadSha"`
	BaseRefHeadSHA          string `json:"baseRefHeadSha"`
	DiffSHA256              string `json:"diffSha256"`
	ChangedPathsSHA256      string `json:"changedPathsSha256"`
	SourceDevice            uint64 `json:"sourceDevice"`
	SourceInode             uint64 `json:"sourceInode"`
	CommonDevice            uint64 `json:"commonDevice"`
	CommonInode             uint64 `json:"commonInode"`
	WorktreeDevice          uint64 `json:"worktreeDevice"`
	WorktreeInode           uint64 `json:"worktreeInode"`
	RepositoryExact         bool   `json:"repositoryExact"`
	RemoteExact             bool   `json:"remoteExact"`
	PathsCanonical          bool   `json:"pathsCanonical"`
	RegistrationExact       bool   `json:"registrationExact"`
	ObjectPresent           bool   `json:"objectPresent"`
	ObjectStoreOwned        bool   `json:"objectStoreOwned"`
	BranchStable            bool   `json:"branchStable"`
	BaseStable              bool   `json:"baseStable"`
	DescendsFromBase        bool   `json:"descendsFromBase"`
	DirectParent            bool   `json:"directParent"`
	MergeCommit             bool   `json:"mergeCommit"`
	WorktreeClean           bool   `json:"worktreeClean"`
	IndexClean              bool   `json:"indexClean"`
	UntrackedAbsent         bool   `json:"untrackedAbsent"`
	IgnoredAbsent           bool   `json:"ignoredAbsent"`
	SubmodulesClean         bool   `json:"submodulesClean"`
	ConflictFree            bool   `json:"conflictFree"`
	IntentToAddAbsent       bool   `json:"intentToAddAbsent"`
	SparseCheckoutAbsent    bool   `json:"sparseCheckoutAbsent"`
	FilesystemExact         bool   `json:"filesystemExact"`
	SnapshotSHA256          string `json:"snapshotSha256"`
	Code                    Code   `json:"code"`
	FactSHA256              string `json:"factSha256"`
}

// Manifest is the immutable, content-addressed Candidate identity consumed by
// every later Validation, Review, CI, publication, Ready, and integration gate.
type Manifest struct {
	SchemaVersion           string `json:"schemaVersion"`
	CandidateSHA            string `json:"candidateSha"`
	BaseSHA                 string `json:"baseSha"`
	ParentSHA               string `json:"parentSha"`
	TreeSHA                 string `json:"treeSha"`
	DiffSHA256              string `json:"diffSha256"`
	ChangedPathsSHA256      string `json:"changedPathsSha256"`
	RepositoryBindingSHA256 string `json:"repositoryBindingSha256"`
	ClaimSHA256             string `json:"claimSha256"`
	ObservationSHA256       string `json:"observationSha256"`
	AcceptanceSHA256        string `json:"acceptanceSha256"`
	ConfigurationSHA256     string `json:"configurationSha256"`
	ProfileSHA256           string `json:"profileSha256"`
	ContextSHA256           string `json:"contextSha256"`
	DecisionsSHA256         string `json:"decisionsSha256"`
	FindingsSHA256          string `json:"findingsSha256"`
	GraphPolicySHA256       string `json:"graphPolicySha256"`
	BindingSHA256           string `json:"bindingSha256"`
}

// DecisionKind is the closed pure admission result.
type DecisionKind string

const (
	DecisionAdmit DecisionKind = "admit"
	DecisionPark  DecisionKind = "park"
)

type Decision struct {
	Kind     DecisionKind `json:"kind"`
	Code     Code         `json:"code"`
	Manifest *Manifest    `json:"manifest,omitempty"`
}

// EvidenceBinding represents a future downstream authority. M4.2 defines its
// invalidation contract without implementing the later lifecycle which earns it.
type EvidenceBinding struct {
	ID            string `json:"id"`
	CandidateID   string `json:"candidateId"`
	CandidateSHA  string `json:"candidateSha"`
	BaseSHA       string `json:"baseSha"`
	Generation    uint64 `json:"generation"`
	BindingSHA256 string `json:"bindingSha256"`
}

type Downstream struct {
	Validation  *EvidenceBinding `json:"validation,omitempty"`
	Review      *EvidenceBinding `json:"review,omitempty"`
	CI          *EvidenceBinding `json:"ci,omitempty"`
	Publication *EvidenceBinding `json:"publication,omitempty"`
	Feedback    *EvidenceBinding `json:"feedback,omitempty"`
	Ready       *EvidenceBinding `json:"ready,omitempty"`
	Integration *EvidenceBinding `json:"integration,omitempty"`
	Cleanup     *EvidenceBinding `json:"cleanup,omitempty"`
}

// Authority is the current Run-level authorization generation. Replacing a
// Candidate or changing any bound input creates a new generation and clears
// every downstream authority while retaining immutable historical evidence.
type Authority struct {
	SchemaVersion       string     `json:"schemaVersion"`
	Generation          uint64     `json:"generation"`
	CandidateID         string     `json:"candidateId"`
	BindingSHA256       string     `json:"bindingSha256"`
	CandidateSHA        string     `json:"candidateSha"`
	BaseSHA             string     `json:"baseSha"`
	Branch              string     `json:"branch"`
	TaskVersion         uint64     `json:"taskVersion"`
	ConfigurationSHA256 string     `json:"configurationSha256"`
	DecisionsSHA256     string     `json:"decisionsSha256"`
	FindingsSHA256      string     `json:"findingsSha256"`
	Invalidated         bool       `json:"invalidated"`
	InvalidationCode    Code       `json:"invalidationCode,omitempty"`
	Downstream          Downstream `json:"downstream"`
}

type AuthorityContext struct {
	CandidateID         string
	BindingSHA256       string
	CandidateSHA        string
	BaseSHA             string
	Branch              string
	TaskVersion         uint64
	ConfigurationSHA256 string
	DecisionsSHA256     string
	FindingsSHA256      string
}

// HistoricalAuthority retains the complete prior downstream binding snapshot
// when a changed correction commit atomically replaces current authority.
// The snapshot remains immutable; invalidation metadata is carried beside it.
type HistoricalAuthority struct {
	Authority                 Authority `json:"authority"`
	InvalidationCode          Code      `json:"invalidationCode"`
	InvalidatedByCandidateID  string    `json:"invalidatedByCandidateId"`
	InvalidatedByCandidateSHA string    `json:"invalidatedByCandidateSha"`
	InvalidatedAtMillis       int64     `json:"invalidatedAtMillis"`
}

func ValidHistoricalAuthority(value HistoricalAuthority) bool {
	return ValidAuthority(value.Authority) && !value.Authority.Invalidated && value.InvalidationCode == CodeCandidateChanged &&
		identifierPattern.MatchString(value.InvalidatedByCandidateID) && validOID(value.InvalidatedByCandidateSHA) &&
		value.InvalidatedByCandidateID != value.Authority.CandidateID && value.InvalidatedByCandidateSHA != value.Authority.CandidateSHA &&
		len(value.InvalidatedByCandidateSHA) == len(value.Authority.CandidateSHA) && value.InvalidatedAtMillis >= 0
}

func digest(value any) string {
	encoded, err := json.Marshal(value)
	if err != nil {
		panic("marshal fixed Candidate value: " + err.Error())
	}
	sum := sha256.Sum256(encoded)
	return hex.EncodeToString(sum[:])
}

func validOID(value string) bool { return gitOIDPattern.MatchString(value) }

func sameObjectFormat(values ...string) bool {
	want := 0
	for _, value := range values {
		if value == "" {
			continue
		}
		if !validOID(value) {
			return false
		}
		if want == 0 {
			want = len(value)
		} else if len(value) != want {
			return false
		}
	}
	return want != 0
}

func validBranch(value string) bool {
	if len(value) == 0 || len(value) > 255 || value == "@" || strings.HasPrefix(value, "-") ||
		strings.HasPrefix(value, "/") || strings.HasSuffix(value, "/") || strings.HasSuffix(value, ".") ||
		strings.HasSuffix(value, ".lock") || strings.Contains(value, "..") || strings.Contains(value, "@{") ||
		strings.Contains(value, "//") {
		return false
	}
	return strings.IndexFunc(value, func(r rune) bool {
		return r <= ' ' || r == 0x7f || strings.ContainsRune(`~^:?*[\`, r)
	}) < 0
}

func validBaseRef(value string) bool {
	for _, prefix := range []string{"refs/heads/", "refs/remotes/"} {
		if strings.HasPrefix(value, prefix) && validBranch(strings.TrimPrefix(value, prefix)) {
			return true
		}
	}
	return false
}

func ValidClaim(value Claim) bool {
	return value.SchemaVersion == ClaimSchemaVersion && identifierPattern.MatchString(value.ID) &&
		identifierPattern.MatchString(value.ProjectID) && identifierPattern.MatchString(value.WorkspaceID) &&
		identifierPattern.MatchString(value.TaskID) && identifierPattern.MatchString(value.RunID) &&
		identifierPattern.MatchString(value.ActorID) && identifierPattern.MatchString(value.WorktreeID) &&
		validBranch(value.Branch) && validBaseRef(value.BaseRef) && value.LeaseEpoch > 0 &&
		sameObjectFormat(value.CandidateSHA, value.BaseSHA) && value.CandidateSHA != value.BaseSHA &&
		digestPattern.MatchString(value.AcceptanceSHA256) && digestPattern.MatchString(value.ConfigurationSHA256) &&
		digestPattern.MatchString(value.ProfileSHA256) && digestPattern.MatchString(value.ContextSHA256) &&
		digestPattern.MatchString(value.DecisionsSHA256) && digestPattern.MatchString(value.FindingsSHA256)
}

func ClaimSHA256(value Claim) string {
	if !ValidClaim(value) {
		return ""
	}
	return digest(value)
}

func observationValue(value Observation) Observation {
	value.FactSHA256 = ""
	return value
}

func ObservationSHA256(value Observation) string { return digest(observationValue(value)) }

func SealObservation(value Observation) Observation {
	value.SchemaVersion = ObservationSchemaVersion
	if value.ID == "" {
		identity := value
		identity.ID = ""
		value.ID = "candidate-observation-" + ObservationSHA256(identity)[:32]
	}
	value.FactSHA256 = ObservationSHA256(value)
	return value
}

func validCode(code Code) bool { return slices.Contains(codes, code) }

func ValidObservation(value Observation) bool {
	if value.SchemaVersion != ObservationSchemaVersion || !identifierPattern.MatchString(value.ID) ||
		(value.Code != CodeClaimInvalid && !digestPattern.MatchString(value.ClaimSHA256)) ||
		!digestPattern.MatchString(value.RepositoryBindingSHA256) ||
		value.ObservedAtMillis < 0 || value.MaximumAgeMillis != MaximumObservationAgeMS ||
		!validCode(value.Code) || !digestPattern.MatchString(value.FactSHA256) ||
		value.FactSHA256 != ObservationSHA256(value) {
		return false
	}
	if value.Code != CodeOK {
		return true
	}
	return (value.ObjectFormat == "sha1" || value.ObjectFormat == "sha256") &&
		sameObjectFormat(value.CommitSHA, value.BaseSHA, value.ParentSHA, value.TreeSHA,
			value.BranchHeadSHA, value.BaseRefHeadSHA) &&
		len(value.CommitSHA) == map[string]int{"sha1": 40, "sha256": 64}[value.ObjectFormat] &&
		digestPattern.MatchString(value.DiffSHA256) && digestPattern.MatchString(value.ChangedPathsSHA256) &&
		value.SourceDevice > 0 && value.SourceInode > 0 && value.CommonDevice > 0 && value.CommonInode > 0 &&
		value.WorktreeDevice > 0 && value.WorktreeInode > 0 && digestPattern.MatchString(value.SnapshotSHA256)
}

func CurrentObservation(value Observation, nowMillis int64) bool {
	return ValidObservation(value) && value.MaximumAgeMillis > 0 && value.ObservedAtMillis <= nowMillis &&
		nowMillis-value.ObservedAtMillis <= value.MaximumAgeMillis
}

func GraphPolicySHA256(value GraphPolicy) string { return digest(value) }

func manifestValue(value Manifest) Manifest {
	value.BindingSHA256 = ""
	return value
}

func ManifestSHA256(value Manifest) string { return digest(manifestValue(value)) }

func ValidManifest(value Manifest) bool {
	return value.SchemaVersion == ManifestSchemaVersion &&
		sameObjectFormat(value.CandidateSHA, value.BaseSHA, value.ParentSHA, value.TreeSHA) &&
		digestPattern.MatchString(value.DiffSHA256) && digestPattern.MatchString(value.ChangedPathsSHA256) &&
		digestPattern.MatchString(value.RepositoryBindingSHA256) && digestPattern.MatchString(value.ClaimSHA256) &&
		digestPattern.MatchString(value.ObservationSHA256) && digestPattern.MatchString(value.AcceptanceSHA256) &&
		digestPattern.MatchString(value.ConfigurationSHA256) && digestPattern.MatchString(value.ProfileSHA256) &&
		digestPattern.MatchString(value.ContextSHA256) && digestPattern.MatchString(value.DecisionsSHA256) &&
		digestPattern.MatchString(value.FindingsSHA256) && digestPattern.MatchString(value.GraphPolicySHA256) &&
		digestPattern.MatchString(value.BindingSHA256) && value.BindingSHA256 == ManifestSHA256(value)
}

// SameImmutableContent compares the facts which must remain identical across
// fresh observations. Observation identity/time are intentionally excluded.
func SameImmutableContent(left, right Manifest) bool {
	return left.CandidateSHA == right.CandidateSHA && left.BaseSHA == right.BaseSHA &&
		left.ParentSHA == right.ParentSHA && left.TreeSHA == right.TreeSHA &&
		left.DiffSHA256 == right.DiffSHA256 && left.ChangedPathsSHA256 == right.ChangedPathsSHA256 &&
		left.RepositoryBindingSHA256 == right.RepositoryBindingSHA256 && left.ClaimSHA256 == right.ClaimSHA256 &&
		left.AcceptanceSHA256 == right.AcceptanceSHA256 && left.ConfigurationSHA256 == right.ConfigurationSHA256 &&
		left.ProfileSHA256 == right.ProfileSHA256 && left.ContextSHA256 == right.ContextSHA256 &&
		left.DecisionsSHA256 == right.DecisionsSHA256 && left.FindingsSHA256 == right.FindingsSHA256 &&
		left.GraphPolicySHA256 == right.GraphPolicySHA256
}

func park(code Code) Decision { return Decision{Kind: DecisionPark, Code: code} }

// Evaluate is the sole pure admission rule. A successful claim is still not
// evidence: every exact external predicate must be current and self-hashed.
func Evaluate(claim Claim, observation Observation, repositoryBindingSHA256 string, nowMillis int64) Decision {
	claimHash := ClaimSHA256(claim)
	if claimHash == "" {
		return park(CodeClaimInvalid)
	}
	if !ValidObservation(observation) {
		return park(CodeObservationInvalid)
	}
	if !CurrentObservation(observation, nowMillis) {
		return park(CodeObservationStale)
	}
	if observation.ClaimSHA256 != claimHash || observation.RepositoryBindingSHA256 != repositoryBindingSHA256 {
		return park(CodeObservationBindingMismatch)
	}
	if observation.Code != CodeOK {
		return park(observation.Code)
	}
	if observation.CommitSHA != claim.CandidateSHA || observation.BaseSHA != claim.BaseSHA ||
		observation.BranchHeadSHA != claim.CandidateSHA || observation.BaseRefHeadSHA != claim.BaseSHA {
		return park(CodeObservationBindingMismatch)
	}
	checks := []struct {
		ok   bool
		code Code
	}{
		{observation.RepositoryExact, CodeRepositoryMismatch},
		{observation.RemoteExact, CodeRemoteMismatch},
		{observation.PathsCanonical, CodePathAlias},
		{observation.RegistrationExact, CodeWorktreeRegistration},
		{observation.ObjectPresent, CodeObjectMissing},
		{observation.ObjectStoreOwned, CodeObjectStoreAmbiguous},
		{observation.BranchStable, CodeBranchMoved},
		{observation.BaseStable, CodeBaseMoved},
		{observation.DescendsFromBase, CodeGraphRejected},
		{observation.WorktreeClean, CodeWorktreeDirty},
		{observation.IndexClean, CodeIndexDirty},
		{observation.UntrackedAbsent, CodeUntracked},
		{observation.IgnoredAbsent, CodeIgnored},
		{observation.SubmodulesClean, CodeSubmodule},
		{observation.ConflictFree, CodeConflict},
		{observation.IntentToAddAbsent, CodeIntentToAdd},
		{observation.SparseCheckoutAbsent, CodeSparseCheckout},
		{observation.FilesystemExact, CodeUnsafePath},
	}
	for _, check := range checks {
		if !check.ok {
			return park(check.code)
		}
	}
	if observation.MergeCommit && !claim.GraphPolicy.AllowMergeCommit ||
		!observation.MergeCommit && !observation.DirectParent && !claim.GraphPolicy.AllowNonDirectParent {
		return park(CodeGraphRejected)
	}
	manifest := Manifest{
		SchemaVersion: ManifestSchemaVersion, CandidateSHA: observation.CommitSHA, BaseSHA: observation.BaseSHA,
		ParentSHA: observation.ParentSHA, TreeSHA: observation.TreeSHA, DiffSHA256: observation.DiffSHA256,
		ChangedPathsSHA256: observation.ChangedPathsSHA256, RepositoryBindingSHA256: repositoryBindingSHA256,
		ClaimSHA256: claimHash, ObservationSHA256: observation.FactSHA256,
		AcceptanceSHA256: claim.AcceptanceSHA256, ConfigurationSHA256: claim.ConfigurationSHA256,
		ProfileSHA256: claim.ProfileSHA256, ContextSHA256: claim.ContextSHA256,
		DecisionsSHA256: claim.DecisionsSHA256, FindingsSHA256: claim.FindingsSHA256,
		GraphPolicySHA256: GraphPolicySHA256(claim.GraphPolicy),
	}
	manifest.BindingSHA256 = ManifestSHA256(manifest)
	return Decision{Kind: DecisionAdmit, Code: CodeOK, Manifest: &manifest}
}

// AcceptanceSHA256 binds the Task version and the complete frozen criterion
// identity set. Criterion order is canonicalized because it is a set.
func AcceptanceSHA256(taskID string, taskVersion uint64, objective, acceptance string, criterionIDs []string) string {
	criteria := slices.Clone(criterionIDs)
	slices.Sort(criteria)
	return digest(struct {
		TaskID      string   `json:"taskId"`
		TaskVersion string   `json:"taskVersion"`
		Objective   string   `json:"objective"`
		Acceptance  string   `json:"acceptance"`
		Criteria    []string `json:"criteria"`
	}{taskID, strconv.FormatUint(taskVersion, 10), objective, acceptance, criteria})
}

// EmptyContextSHA256 is the exact digest used before decisions or findings
// exist. An empty string is never interpreted as an empty durable set.
func EmptyContextSHA256() string { return digest([]string{}) }

// RecordID derives the immutable Run/sequence/SHA identity used by the store.
func RecordID(runID string, sequence uint64, commitSHA string) string {
	sum := sha256.Sum256([]byte(strings.Join([]string{runID, strconv.FormatUint(sequence, 10), commitSHA}, "\x1f")))
	return "candidate-" + hex.EncodeToString(sum[:16])
}

func NewAuthority(previousGeneration uint64, candidateID, branch string, taskVersion uint64, manifest Manifest) Authority {
	return Authority{
		SchemaVersion: AuthoritySchemaVersion, Generation: previousGeneration + 1,
		CandidateID: candidateID, BindingSHA256: manifest.BindingSHA256,
		CandidateSHA: manifest.CandidateSHA, BaseSHA: manifest.BaseSHA, Branch: branch,
		TaskVersion: taskVersion, ConfigurationSHA256: manifest.ConfigurationSHA256,
		DecisionsSHA256: manifest.DecisionsSHA256, FindingsSHA256: manifest.FindingsSHA256,
		Downstream: Downstream{},
	}
}

func DownstreamEmpty(value Downstream) bool {
	return value.Validation == nil && value.Review == nil && value.CI == nil && value.Publication == nil &&
		value.Feedback == nil && value.Ready == nil && value.Integration == nil && value.Cleanup == nil
}

func ValidAuthority(value Authority) bool {
	if value.SchemaVersion != AuthoritySchemaVersion || value.Generation == 0 ||
		!identifierPattern.MatchString(value.CandidateID) || !digestPattern.MatchString(value.BindingSHA256) ||
		!validOID(value.CandidateSHA) || !validOID(value.BaseSHA) || len(value.CandidateSHA) != len(value.BaseSHA) ||
		!validBranch(value.Branch) || !digestPattern.MatchString(value.ConfigurationSHA256) ||
		!digestPattern.MatchString(value.DecisionsSHA256) || !digestPattern.MatchString(value.FindingsSHA256) ||
		value.Invalidated && (value.InvalidationCode == "" || !DownstreamEmpty(value.Downstream)) ||
		!value.Invalidated && value.InvalidationCode != "" {
		return false
	}
	for _, evidence := range []*EvidenceBinding{
		value.Downstream.Validation, value.Downstream.Review, value.Downstream.CI, value.Downstream.Publication,
		value.Downstream.Feedback, value.Downstream.Ready, value.Downstream.Integration, value.Downstream.Cleanup,
	} {
		if evidence != nil && (!identifierPattern.MatchString(evidence.ID) || evidence.CandidateID != value.CandidateID ||
			evidence.CandidateSHA != value.CandidateSHA || evidence.BaseSHA != value.BaseSHA ||
			evidence.Generation != value.Generation || evidence.BindingSHA256 != value.BindingSHA256) {
			return false
		}
	}
	return true
}

// ReconcileAuthority invalidates all downstream authority when any exact gate
// input changes. The immutable Candidate and historical evidence are retained.
func ReconcileAuthority(value Authority, current AuthorityContext) (Authority, bool) {
	if !ValidAuthority(value) {
		return value, false
	}
	code := Code("")
	switch {
	case value.CandidateID != current.CandidateID || value.CandidateSHA != current.CandidateSHA || value.BindingSHA256 != current.BindingSHA256:
		code = CodeObservationBindingMismatch
	case value.BaseSHA != current.BaseSHA:
		code = CodeBaseMoved
	case value.Branch != current.Branch:
		code = CodeBranchMoved
	case value.TaskVersion != current.TaskVersion:
		code = CodeTaskVersionChanged
	case value.ConfigurationSHA256 != current.ConfigurationSHA256:
		code = CodeConfigurationChanged
	case value.DecisionsSHA256 != current.DecisionsSHA256:
		code = CodeDecisionsChanged
	case value.FindingsSHA256 != current.FindingsSHA256:
		code = CodeFindingsChanged
	}
	if code == "" || value.Invalidated && value.InvalidationCode == code && DownstreamEmpty(value.Downstream) {
		return value, false
	}
	value.Generation++
	value.Invalidated = true
	value.InvalidationCode = code
	value.Downstream = Downstream{}
	return value, true
}
