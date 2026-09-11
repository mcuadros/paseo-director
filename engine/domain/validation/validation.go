// SPDX-License-Identifier: Apache-2.0

// Package validation owns the pure exact-Candidate GitHub CI contract. Git
// and GitHub adapters return bounded observations; only this package decides
// whether one authoritative remote CI cycle is pending, passing, failing, or
// ambiguous.
package validation

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/mcuadros/director-engine/domain/candidate"
	domainreview "github.com/mcuadros/director-engine/domain/review"
)

const (
	PolicySchemaVersion      = "director.validation-policy/v1"
	BindingSchemaVersion     = "director.validation-binding/v1"
	ObservationSchemaVersion = "director.validation-observation/v1"
	EvidenceSchemaVersion    = "director.validation-evidence/v1"
	StateSchemaVersion       = "director.validation-state/v1"
	MaximumObservationAgeMS  = int64(5_000)
	MaximumPages             = uint32(10)
	PageSize                 = uint32(100)
	MaximumChecks            = 32
	MaximumCICycles          = uint32(4)
	MaximumRuntimeMillis     = uint64(6 * 60 * 60 * 1_000)
	MaximumHistory           = 64
)

var (
	identifierPattern  = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.:@/-]{0,255}$`)
	loginPattern       = regexp.MustCompile(`^[A-Za-z0-9](?:[A-Za-z0-9-]{0,38})$`)
	statusLoginPattern = regexp.MustCompile(`^(?:[A-Za-z0-9](?:[A-Za-z0-9-]{0,38})|[A-Za-z0-9][A-Za-z0-9-]{0,38}\[bot\])$`)
	digestPattern      = regexp.MustCompile(`^[0-9a-f]{64}$`)
	gitOIDPattern      = regexp.MustCompile(`^(?:[0-9a-f]{40}|[0-9a-f]{64})$`)
	secretPattern      = regexp.MustCompile(`(?i)(?:-----BEGIN [A-Z0-9 ]*PRIVATE KEY-----|github_pat_[A-Za-z0-9_]{16,}|gh[pousr]_[A-Za-z0-9]{16,}|sk-[A-Za-z0-9_-]{16,}|(?:password|secret|token|credential|authorization)\s*[:=]\s*\S+)`)
)

func digest(value any) string {
	encoded, err := json.Marshal(value)
	if err != nil {
		panic("marshal fixed validation value: " + err.Error())
	}
	sum := sha256.Sum256(encoded)
	return hex.EncodeToString(sum[:])
}

func safeText(value string) bool {
	return identifierPattern.MatchString(value) && !secretPattern.MatchString(value)
}

func safeName(value string) bool {
	return value != "" && len(value) <= 200 && utf8.ValidString(value) && value == strings.TrimSpace(value) &&
		strings.IndexFunc(value, unicode.IsControl) < 0 && !secretPattern.MatchString(value)
}

type CheckKind string

const (
	CheckRunKind CheckKind = "check_run"
	StatusKind   CheckKind = "commit_status"
)

// RequiredCheck freezes the provider identity as well as its display name.
// Equal names from Check Runs and legacy Commit Statuses are deliberately
// ambiguous even when only one source was configured.
type RequiredCheck struct {
	ID           string    `json:"id"`
	Kind         CheckKind `json:"kind"`
	Name         string    `json:"name"`
	AppID        int64     `json:"appId,omitempty"`
	AppSlug      string    `json:"appSlug,omitempty"`
	CreatorID    int64     `json:"creatorId,omitempty"`
	CreatorLogin string    `json:"creatorLogin,omitempty"`
}

func validRequiredCheck(value RequiredCheck) bool {
	if !safeText(value.ID) || !safeName(value.Name) {
		return false
	}
	switch value.Kind {
	case CheckRunKind:
		return value.AppID > 0 && safeText(value.AppSlug) && value.CreatorID == 0 && value.CreatorLogin == ""
	case StatusKind:
		return value.CreatorID > 0 && statusLoginPattern.MatchString(value.CreatorLogin) && value.AppID == 0 && value.AppSlug == ""
	default:
		return false
	}
}

type Policy struct {
	SchemaVersion        string          `json:"schemaVersion"`
	WorkflowID           int64           `json:"workflowId"`
	WorkflowName         string          `json:"workflowName"`
	RequiredChecks       []RequiredCheck `json:"requiredChecks"`
	MaximumPages         uint32          `json:"maximumPages"`
	PageSize             uint32          `json:"pageSize"`
	MaximumCycles        uint32          `json:"maximumCycles"`
	CycleRuntimeMillis   uint64          `json:"cycleRuntimeMillis"`
	ObservationAgeMillis int64           `json:"observationAgeMillis"`
	SHA256               string          `json:"sha256"`
}

func policyValue(value Policy) Policy {
	value.SHA256 = ""
	value.RequiredChecks = slices.Clone(value.RequiredChecks)
	return value
}

func PolicySHA256(value Policy) string { return digest(policyValue(value)) }

func NewPolicy(workflowID int64, workflowName string, checks []RequiredCheck, cycleRuntimeMillis uint64) (Policy, bool) {
	ordered := slices.Clone(checks)
	sort.Slice(ordered, func(left, right int) bool { return ordered[left].ID < ordered[right].ID })
	value := Policy{SchemaVersion: PolicySchemaVersion, WorkflowID: workflowID, WorkflowName: workflowName,
		RequiredChecks: ordered, MaximumPages: MaximumPages, PageSize: PageSize, MaximumCycles: MaximumCICycles,
		CycleRuntimeMillis: cycleRuntimeMillis, ObservationAgeMillis: MaximumObservationAgeMS}
	value.SHA256 = PolicySHA256(value)
	return value, ValidPolicy(value)
}

func ValidPolicy(value Policy) bool {
	if value.SchemaVersion != PolicySchemaVersion || value.WorkflowID <= 0 || !safeName(value.WorkflowName) ||
		len(value.RequiredChecks) == 0 || len(value.RequiredChecks) > MaximumChecks || value.MaximumPages == 0 ||
		value.MaximumPages > MaximumPages || value.PageSize == 0 || value.PageSize > PageSize ||
		value.MaximumCycles != MaximumCICycles || value.CycleRuntimeMillis == 0 || value.CycleRuntimeMillis > MaximumRuntimeMillis ||
		value.ObservationAgeMillis <= 0 || value.ObservationAgeMillis > MaximumObservationAgeMS ||
		value.SHA256 != PolicySHA256(value) {
		return false
	}
	providers := make(map[string]struct{}, len(value.RequiredChecks))
	for index, check := range value.RequiredChecks {
		if !validRequiredCheck(check) || index > 0 && value.RequiredChecks[index-1].ID >= check.ID {
			return false
		}
		provider := strings.Join([]string{string(check.Kind), check.Name, strconv.FormatInt(check.AppID, 10), strconv.FormatInt(check.CreatorID, 10)}, "\x1f")
		if _, duplicate := providers[provider]; duplicate {
			return false
		}
		providers[provider] = struct{}{}
	}
	return true
}

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
	CISlotID                string `json:"ciSlotId"`
	BaseRef                 string `json:"baseRef"`
	RepositoryBindingSHA256 string `json:"repositoryBindingSha256"`
	CanonicalRemote         string `json:"canonicalRemote"`
	GitHubRepositoryID      int64  `json:"githubRepositoryId"`
	GitHubRepositoryNodeID  string `json:"githubRepositoryNodeId"`
	RepositoryOwner         string `json:"repositoryOwner"`
	RepositoryName          string `json:"repositoryName"`
	ViewerLogin             string `json:"viewerLogin"`
	PolicySHA256            string `json:"policySha256"`
	BindingSHA256           string `json:"bindingSha256"`
}

func bindingValue(value Binding) Binding { value.BindingSHA256 = ""; return value }
func BindingSHA256(value Binding) string { return digest(bindingValue(value)) }
func SealBinding(value Binding) Binding {
	value.SchemaVersion = BindingSchemaVersion
	value.BindingSHA256 = BindingSHA256(value)
	return value
}

func ValidBinding(value Binding) bool {
	return value.SchemaVersion == BindingSchemaVersion && safeText(value.TaskID) && safeText(value.RunID) &&
		safeText(value.CandidateID) && gitOIDPattern.MatchString(value.CandidateSHA) && gitOIDPattern.MatchString(value.BaseSHA) &&
		gitOIDPattern.MatchString(value.TreeSHA) && len(value.CandidateSHA) == len(value.BaseSHA) && len(value.CandidateSHA) == len(value.TreeSHA) &&
		digestPattern.MatchString(value.ManifestSHA256) && value.CandidateGeneration > 0 && safeText(value.CISlotID) &&
		strings.HasPrefix(value.BaseRef, "refs/heads/") && safeText(strings.TrimPrefix(value.BaseRef, "refs/heads/")) &&
		digestPattern.MatchString(value.RepositoryBindingSHA256) && strings.HasPrefix(value.CanonicalRemote, "https://github.com/") &&
		value.GitHubRepositoryID > 0 && safeText(value.GitHubRepositoryNodeID) && loginPattern.MatchString(value.RepositoryOwner) &&
		safeText(value.RepositoryName) && loginPattern.MatchString(value.ViewerLogin) && digestPattern.MatchString(value.PolicySHA256) &&
		digestPattern.MatchString(value.BindingSHA256) && value.BindingSHA256 == BindingSHA256(value)
}

type Code string

const (
	CodeOK                   Code = "VALIDATION_OK"
	CodeUnavailable          Code = "VALIDATION_UNAVAILABLE"
	CodeTLS                  Code = "VALIDATION_TLS_FAILURE"
	CodeRateLimited          Code = "VALIDATION_RATE_LIMITED"
	CodeUnauthorized         Code = "VALIDATION_UNAUTHORIZED"
	CodeForbidden            Code = "VALIDATION_FORBIDDEN"
	CodeNotFound             Code = "VALIDATION_NOT_FOUND"
	CodeServer               Code = "VALIDATION_SERVER_ERROR"
	CodeRepositoryMismatch   Code = "VALIDATION_REPOSITORY_MISMATCH"
	CodePaginationIncomplete Code = "VALIDATION_PAGINATION_INCOMPLETE"
	CodeWorkflowAbsent       Code = "VALIDATION_WORKFLOW_ABSENT"
	CodeWorkflowAmbiguous    Code = "VALIDATION_WORKFLOW_AMBIGUOUS"
	CodeWorkflowSHAMismatch  Code = "VALIDATION_WORKFLOW_SHA_MISMATCH"
	CodeCheckMissing         Code = "VALIDATION_REQUIRED_CHECK_MISSING"
	CodeCheckAmbiguous       Code = "VALIDATION_REQUIRED_CHECK_AMBIGUOUS"
	CodeCheckSHAMismatch     Code = "VALIDATION_CHECK_SHA_MISMATCH"
	CodeCheckSuiteAmbiguous  Code = "VALIDATION_CHECK_SUITE_AMBIGUOUS"
	CodeStatusAmbiguous      Code = "VALIDATION_STATUS_AMBIGUOUS"
	CodeStatusSHAMismatch    Code = "VALIDATION_STATUS_SHA_MISMATCH"
	CodePending              Code = "VALIDATION_PENDING"
	CodeFailed               Code = "VALIDATION_FAILED"
	CodeTimedOut             Code = "VALIDATION_TIMED_OUT"
	CodeBaseChanged          Code = "VALIDATION_BASE_CHANGED"
	CodeBaseRace             Code = "VALIDATION_BASE_RACE"
	CodeRuntimeExhausted     Code = "VALIDATION_RUNTIME_EXHAUSTED"
	CodeBudgetExhausted      Code = "VALIDATION_CI_BUDGET_EXHAUSTED"
	CodeResponseUnknown      Code = "VALIDATION_RESPONSE_UNKNOWN"
	CodeRedactionFailure     Code = "VALIDATION_REDACTION_FAILURE"
	CodeCandidateChanged     Code = "VALIDATION_CANDIDATE_CHANGED"
	CodeHumanFeedback        Code = "VALIDATION_HUMAN_FEEDBACK"
)

var codes = []Code{CodeOK, CodeUnavailable, CodeTLS, CodeRateLimited, CodeUnauthorized, CodeForbidden, CodeNotFound,
	CodeServer, CodeRepositoryMismatch, CodePaginationIncomplete, CodeWorkflowAbsent, CodeWorkflowAmbiguous,
	CodeWorkflowSHAMismatch, CodeCheckMissing, CodeCheckAmbiguous, CodeCheckSHAMismatch, CodeCheckSuiteAmbiguous,
	CodeStatusAmbiguous, CodeStatusSHAMismatch, CodePending, CodeFailed, CodeTimedOut, CodeBaseChanged, CodeBaseRace,
	CodeRuntimeExhausted, CodeBudgetExhausted, CodeResponseUnknown, CodeRedactionFailure, CodeCandidateChanged, CodeHumanFeedback}

func validCode(value Code) bool { return slices.Contains(codes, value) }

type RepositoryObservation struct {
	SchemaVersion    string `json:"schemaVersion"`
	ID               string `json:"id"`
	Code             Code   `json:"code"`
	RepositoryID     int64  `json:"repositoryId"`
	RepositoryNodeID string `json:"repositoryNodeId"`
	Owner            string `json:"owner"`
	Name             string `json:"name"`
	ViewerLogin      string `json:"viewerLogin"`
	Authenticated    bool   `json:"authenticated"`
	CanReadChecks    bool   `json:"canReadChecks"`
	Archived         bool   `json:"archived"`
	Disabled         bool   `json:"disabled"`
	TLSVerified      bool   `json:"tlsVerified"`
	APIVersion       string `json:"apiVersion"`
	RateRemaining    int64  `json:"rateRemaining"`
	ObservedAtMillis int64  `json:"observedAtMillis"`
	MaximumAgeMillis int64  `json:"maximumAgeMillis"`
	FactSHA256       string `json:"factSha256"`
}

func repositoryValue(value RepositoryObservation) RepositoryObservation {
	value.FactSHA256 = ""
	return value
}
func RepositoryObservationSHA256(value RepositoryObservation) string {
	return digest(repositoryValue(value))
}
func SealRepositoryObservation(value RepositoryObservation) RepositoryObservation {
	value.SchemaVersion = ObservationSchemaVersion
	value.FactSHA256 = digest(repositoryValue(value))
	return value
}
func CurrentRepositoryObservation(value RepositoryObservation, binding Binding, nowMillis int64) bool {
	if value.SchemaVersion != ObservationSchemaVersion || !safeText(value.ID) || !validCode(value.Code) || value.ObservedAtMillis < 0 ||
		value.ObservedAtMillis > nowMillis || value.MaximumAgeMillis != MaximumObservationAgeMS || nowMillis-value.ObservedAtMillis > value.MaximumAgeMillis ||
		value.FactSHA256 != digest(repositoryValue(value)) {
		return false
	}
	if value.Code != CodeOK {
		return true
	}
	return value.RepositoryID == binding.GitHubRepositoryID && value.RepositoryNodeID == binding.GitHubRepositoryNodeID &&
		strings.EqualFold(value.Owner, binding.RepositoryOwner) && value.Name == binding.RepositoryName &&
		strings.EqualFold(value.ViewerLogin, binding.ViewerLogin) && value.Authenticated && value.CanReadChecks &&
		!value.Archived && !value.Disabled && value.TLSVerified && value.APIVersion == "2022-11-28" && value.RateRemaining > 0
}

type WorkflowRun struct {
	ID               int64  `json:"id"`
	WorkflowID       int64  `json:"workflowId"`
	Name             string `json:"name"`
	HeadSHA          string `json:"headSha"`
	HeadRepositoryID int64  `json:"headRepositoryId"`
	CheckSuiteID     int64  `json:"checkSuiteId"`
	Status           string `json:"status"`
	Conclusion       string `json:"conclusion,omitempty"`
	Attempt          uint32 `json:"attempt"`
	StartedAtMillis  int64  `json:"startedAtMillis"`
	UpdatedAtMillis  int64  `json:"updatedAtMillis"`
}

func validWorkflow(value WorkflowRun) bool {
	return value.ID > 0 && value.WorkflowID > 0 && safeName(value.Name) && gitOIDPattern.MatchString(value.HeadSHA) &&
		value.HeadRepositoryID > 0 && value.CheckSuiteID > 0 && slices.Contains([]string{"queued", "in_progress", "completed", "waiting", "pending"}, value.Status) &&
		(value.Conclusion == "" || slices.Contains([]string{"success", "failure", "timed_out", "cancelled", "action_required", "neutral", "skipped", "stale"}, value.Conclusion)) &&
		value.Attempt > 0 && value.StartedAtMillis >= 0 && value.UpdatedAtMillis >= value.StartedAtMillis
}
func ValidWorkflowRun(value WorkflowRun) bool { return validWorkflow(value) }

type CheckRun struct {
	ID                int64  `json:"id"`
	Name              string `json:"name"`
	HeadSHA           string `json:"headSha"`
	SuiteID           int64  `json:"suiteId"`
	SuiteHeadSHA      string `json:"suiteHeadSha"`
	AppID             int64  `json:"appId"`
	AppSlug           string `json:"appSlug"`
	Status            string `json:"status"`
	Conclusion        string `json:"conclusion,omitempty"`
	DetailsURLSHA256  string `json:"detailsUrlSha256"`
	StartedAtMillis   int64  `json:"startedAtMillis"`
	CompletedAtMillis int64  `json:"completedAtMillis,omitempty"`
}

func validCheckRun(value CheckRun) bool {
	return value.ID > 0 && safeName(value.Name) && gitOIDPattern.MatchString(value.HeadSHA) && value.SuiteID > 0 &&
		gitOIDPattern.MatchString(value.SuiteHeadSHA) && value.AppID > 0 && safeText(value.AppSlug) &&
		slices.Contains([]string{"queued", "in_progress", "completed", "waiting", "pending"}, value.Status) &&
		(value.Conclusion == "" || slices.Contains([]string{"success", "failure", "timed_out", "cancelled", "action_required", "neutral", "skipped", "stale"}, value.Conclusion)) &&
		digestPattern.MatchString(value.DetailsURLSHA256) && value.StartedAtMillis >= 0 && value.CompletedAtMillis >= 0
}
func ValidCheckRun(value CheckRun) bool { return validCheckRun(value) }

type CommitStatus struct {
	ID              int64  `json:"id"`
	Context         string `json:"context"`
	SHA             string `json:"sha"`
	State           string `json:"state"`
	CreatorID       int64  `json:"creatorId"`
	CreatorLogin    string `json:"creatorLogin"`
	TargetURLSHA256 string `json:"targetUrlSha256"`
	UpdatedAtMillis int64  `json:"updatedAtMillis"`
}

func validStatus(value CommitStatus) bool {
	return value.ID > 0 && safeName(value.Context) && gitOIDPattern.MatchString(value.SHA) &&
		slices.Contains([]string{"error", "failure", "pending", "success"}, value.State) && value.CreatorID > 0 &&
		statusLoginPattern.MatchString(value.CreatorLogin) && digestPattern.MatchString(value.TargetURLSHA256) && value.UpdatedAtMillis >= 0
}
func ValidCommitStatus(value CommitStatus) bool { return validStatus(value) }

type WorkflowPage struct {
	SchemaVersion    string        `json:"schemaVersion"`
	ID               string        `json:"id"`
	Code             Code          `json:"code"`
	CandidateSHA     string        `json:"candidateSha"`
	Page             uint32        `json:"page"`
	TotalCount       uint32        `json:"totalCount"`
	NextPage         uint32        `json:"nextPage"`
	Complete         bool          `json:"complete"`
	Runs             []WorkflowRun `json:"runs"`
	ObservedAtMillis int64         `json:"observedAtMillis"`
	MaximumAgeMillis int64         `json:"maximumAgeMillis"`
	FactSHA256       string        `json:"factSha256"`
}

func workflowPageValue(value WorkflowPage) WorkflowPage {
	value.FactSHA256 = ""
	value.Runs = slices.Clone(value.Runs)
	return value
}
func SealWorkflowPage(value WorkflowPage) WorkflowPage {
	value.SchemaVersion = ObservationSchemaVersion
	value.FactSHA256 = digest(workflowPageValue(value))
	return value
}
func CurrentWorkflowPage(value WorkflowPage, candidateSHA string, page uint32, nowMillis int64) bool {
	if value.SchemaVersion != ObservationSchemaVersion || !safeText(value.ID) || !validCode(value.Code) || value.CandidateSHA != candidateSHA ||
		value.Page != page || value.ObservedAtMillis < 0 || value.ObservedAtMillis > nowMillis || value.MaximumAgeMillis != MaximumObservationAgeMS ||
		nowMillis-value.ObservedAtMillis > value.MaximumAgeMillis || len(value.Runs) > int(PageSize) || value.FactSHA256 != digest(workflowPageValue(value)) {
		return false
	}
	for _, run := range value.Runs {
		if !validWorkflow(run) {
			return false
		}
	}
	return value.Code != CodeOK && len(value.Runs) == 0 && !value.Complete && value.NextPage == 0 ||
		value.Code == CodeOK && (value.Complete && value.NextPage == 0 || !value.Complete && value.NextPage == page+1)
}

type CheckPage struct {
	SchemaVersion    string     `json:"schemaVersion"`
	ID               string     `json:"id"`
	Code             Code       `json:"code"`
	CandidateSHA     string     `json:"candidateSha"`
	Page             uint32     `json:"page"`
	TotalCount       uint32     `json:"totalCount"`
	NextPage         uint32     `json:"nextPage"`
	Complete         bool       `json:"complete"`
	Checks           []CheckRun `json:"checks"`
	ObservedAtMillis int64      `json:"observedAtMillis"`
	MaximumAgeMillis int64      `json:"maximumAgeMillis"`
	FactSHA256       string     `json:"factSha256"`
}

func checkPageValue(value CheckPage) CheckPage {
	value.FactSHA256 = ""
	value.Checks = slices.Clone(value.Checks)
	return value
}
func SealCheckPage(value CheckPage) CheckPage {
	value.SchemaVersion = ObservationSchemaVersion
	value.FactSHA256 = digest(checkPageValue(value))
	return value
}
func CurrentCheckPage(value CheckPage, candidateSHA string, page uint32, nowMillis int64) bool {
	if value.SchemaVersion != ObservationSchemaVersion || !safeText(value.ID) || !validCode(value.Code) || value.CandidateSHA != candidateSHA ||
		value.Page != page || value.ObservedAtMillis < 0 || value.ObservedAtMillis > nowMillis || value.MaximumAgeMillis != MaximumObservationAgeMS ||
		nowMillis-value.ObservedAtMillis > value.MaximumAgeMillis || len(value.Checks) > int(PageSize) || value.FactSHA256 != digest(checkPageValue(value)) {
		return false
	}
	for _, check := range value.Checks {
		if !validCheckRun(check) {
			return false
		}
	}
	return value.Code != CodeOK && len(value.Checks) == 0 && !value.Complete && value.NextPage == 0 ||
		value.Code == CodeOK && (value.Complete && value.NextPage == 0 || !value.Complete && value.NextPage == page+1)
}

type StatusPage struct {
	SchemaVersion    string         `json:"schemaVersion"`
	ID               string         `json:"id"`
	Code             Code           `json:"code"`
	CandidateSHA     string         `json:"candidateSha"`
	CombinedState    string         `json:"combinedState,omitempty"`
	Page             uint32         `json:"page"`
	TotalCount       uint32         `json:"totalCount"`
	NextPage         uint32         `json:"nextPage"`
	Complete         bool           `json:"complete"`
	Statuses         []CommitStatus `json:"statuses"`
	ObservedAtMillis int64          `json:"observedAtMillis"`
	MaximumAgeMillis int64          `json:"maximumAgeMillis"`
	FactSHA256       string         `json:"factSha256"`
}

func statusPageValue(value StatusPage) StatusPage {
	value.FactSHA256 = ""
	value.Statuses = slices.Clone(value.Statuses)
	return value
}
func SealStatusPage(value StatusPage) StatusPage {
	value.SchemaVersion = ObservationSchemaVersion
	value.FactSHA256 = digest(statusPageValue(value))
	return value
}
func CurrentStatusPage(value StatusPage, candidateSHA string, page uint32, nowMillis int64) bool {
	if value.SchemaVersion != ObservationSchemaVersion || !safeText(value.ID) || !validCode(value.Code) || value.CandidateSHA != candidateSHA ||
		value.Page != page || value.ObservedAtMillis < 0 || value.ObservedAtMillis > nowMillis || value.MaximumAgeMillis != MaximumObservationAgeMS ||
		nowMillis-value.ObservedAtMillis > value.MaximumAgeMillis || len(value.Statuses) > int(PageSize) || value.FactSHA256 != digest(statusPageValue(value)) {
		return false
	}
	for _, status := range value.Statuses {
		if !validStatus(status) {
			return false
		}
	}
	if value.Code != CodeOK {
		return len(value.Statuses) == 0 && value.CombinedState == "" && !value.Complete && value.NextPage == 0
	}
	if !slices.Contains([]string{"error", "failure", "pending", "success"}, value.CombinedState) && value.TotalCount > 0 {
		return false
	}
	return value.Complete && value.NextPage == 0 || !value.Complete && value.NextPage == page+1
}

type WorkflowScan struct {
	Pages      uint32        `json:"pages"`
	TotalCount uint32        `json:"totalCount"`
	Runs       []WorkflowRun `json:"runs"`
	SHA256     string        `json:"sha256"`
}
type CheckScan struct {
	Pages      uint32     `json:"pages"`
	TotalCount uint32     `json:"totalCount"`
	Checks     []CheckRun `json:"checks"`
	SHA256     string     `json:"sha256"`
}
type StatusScan struct {
	Pages         uint32         `json:"pages"`
	TotalCount    uint32         `json:"totalCount"`
	CombinedState string         `json:"combinedState"`
	Statuses      []CommitStatus `json:"statuses"`
	SHA256        string         `json:"sha256"`
}

func SealWorkflowScan(value WorkflowScan) WorkflowScan {
	value.SHA256 = ""
	value.SHA256 = digest(value)
	return value
}
func SealCheckScan(value CheckScan) CheckScan {
	value.SHA256 = ""
	value.SHA256 = digest(value)
	return value
}
func SealStatusScan(value StatusScan) StatusScan {
	value.SHA256 = ""
	value.SHA256 = digest(value)
	return value
}
func validWorkflowScan(value WorkflowScan) bool {
	copy := value
	copy.SHA256 = ""
	if value.Pages == 0 || value.Pages > MaximumPages || value.TotalCount != uint32(len(value.Runs)) || value.SHA256 != digest(copy) {
		return false
	}
	for _, item := range value.Runs {
		if !validWorkflow(item) {
			return false
		}
	}
	return true
}
func validCheckScan(value CheckScan) bool {
	copy := value
	copy.SHA256 = ""
	if value.Pages == 0 || value.Pages > MaximumPages || value.TotalCount != uint32(len(value.Checks)) || value.SHA256 != digest(copy) {
		return false
	}
	for _, item := range value.Checks {
		if !validCheckRun(item) {
			return false
		}
	}
	return true
}
func validStatusScan(value StatusScan) bool {
	copy := value
	copy.SHA256 = ""
	if value.Pages == 0 || value.Pages > MaximumPages || value.TotalCount != uint32(len(value.Statuses)) || value.SHA256 != digest(copy) {
		return false
	}
	for _, item := range value.Statuses {
		if !validStatus(item) {
			return false
		}
	}
	return value.TotalCount == 0 && value.CombinedState == "checks_only_no_statuses" || value.TotalCount > 0 && slices.Contains([]string{"error", "failure", "pending", "success"}, value.CombinedState)
}

type Outcome string

const (
	OutcomePending  Outcome = "pending"
	OutcomePassed   Outcome = "passed"
	OutcomeFailed   Outcome = "failed"
	OutcomeTimedOut Outcome = "timed_out"
)

type ObservedCheck struct {
	ID         string    `json:"id"`
	Name       string    `json:"name"`
	ProviderID string    `json:"providerId"`
	Kind       CheckKind `json:"kind"`
}

type Evidence struct {
	SchemaVersion          string          `json:"schemaVersion"`
	ID                     string          `json:"id"`
	BindingSHA256          string          `json:"bindingSha256"`
	WorkflowRunID          string          `json:"workflowRunId"`
	WorkflowAttempt        uint32          `json:"workflowAttempt"`
	WorkflowCheckSuite     int64           `json:"workflowCheckSuite"`
	Checks                 []ObservedCheck `json:"checks"`
	StatusRollup           string          `json:"statusRollup"`
	RepositoryBeforeSHA256 string          `json:"repositoryBeforeSha256"`
	RepositoryAfterSHA256  string          `json:"repositoryAfterSha256"`
	BaseBeforeSHA256       string          `json:"baseBeforeSha256"`
	BaseAfterSHA256        string          `json:"baseAfterSha256"`
	Outcome                Outcome         `json:"outcome"`
	StartedAtMillis        int64           `json:"startedAtMillis"`
	CompletedAtMillis      int64           `json:"completedAtMillis"`
	ObservedAtMillis       int64           `json:"observedAtMillis"`
	SHA256                 string          `json:"sha256"`
}

func evidenceValue(value Evidence) Evidence {
	value.ID = ""
	value.SHA256 = ""
	value.Checks = slices.Clone(value.Checks)
	return value
}
func SealEvidence(value Evidence) Evidence {
	value.SchemaVersion = EvidenceSchemaVersion
	value.SHA256 = digest(evidenceValue(value))
	value.ID = "validation-evidence-" + value.SHA256[:32]
	return value
}
func ValidEvidence(value Evidence, binding Binding) bool {
	if value.SchemaVersion != EvidenceSchemaVersion || !safeText(value.ID) || value.BindingSHA256 != binding.BindingSHA256 ||
		value.WorkflowRunID == "" || value.WorkflowAttempt == 0 || value.WorkflowCheckSuite <= 0 || len(value.Checks) == 0 ||
		len(value.Checks) > MaximumChecks || !slices.IsSortedFunc(value.Checks, func(a, b ObservedCheck) int { return strings.Compare(a.ID, b.ID) }) ||
		!slices.Contains([]string{"checks_only_no_statuses", "success", "failure", "pending", "error"}, value.StatusRollup) ||
		!digestPattern.MatchString(value.RepositoryBeforeSHA256) || !digestPattern.MatchString(value.RepositoryAfterSHA256) ||
		!digestPattern.MatchString(value.BaseBeforeSHA256) || !digestPattern.MatchString(value.BaseAfterSHA256) ||
		!slices.Contains([]Outcome{OutcomePassed, OutcomeFailed, OutcomeTimedOut}, value.Outcome) || value.StartedAtMillis < 0 ||
		value.CompletedAtMillis < value.StartedAtMillis || value.ObservedAtMillis < value.CompletedAtMillis || value.SHA256 != digest(evidenceValue(value)) ||
		value.ID != "validation-evidence-"+value.SHA256[:32] {
		return false
	}
	for _, check := range value.Checks {
		if !safeText(check.ID) || !safeName(check.Name) || !safeText(check.ProviderID) || !slices.Contains([]CheckKind{CheckRunKind, StatusKind}, check.Kind) {
			return false
		}
	}
	return true
}

type Decision struct {
	Outcome  Outcome   `json:"outcome"`
	Code     Code      `json:"code"`
	Evidence *Evidence `json:"evidence,omitempty"`
}

func terminalOutcome(status, conclusion string) (Outcome, Code) {
	if status != "completed" {
		return OutcomePending, CodePending
	}
	switch conclusion {
	case "success":
		return OutcomePassed, CodeOK
	case "timed_out", "cancelled":
		return OutcomeTimedOut, CodeTimedOut
	default:
		return OutcomeFailed, CodeFailed
	}
}

// Evaluate binds one unique workflow, its exact check suite, each configured
// provider identity, and every legacy status to the immutable Candidate.
func Evaluate(binding Binding, policy Policy, workflows WorkflowScan, checks CheckScan, statuses StatusScan,
	repositoryBeforeSHA256, repositoryAfterSHA256, baseBeforeSHA256, baseAfterSHA256 string, nowMillis int64) Decision {
	if !ValidBinding(binding) || !ValidPolicy(policy) || binding.PolicySHA256 != policy.SHA256 || !validWorkflowScan(workflows) || !validCheckScan(checks) || !validStatusScan(statuses) ||
		!digestPattern.MatchString(repositoryBeforeSHA256) || !digestPattern.MatchString(repositoryAfterSHA256) ||
		!digestPattern.MatchString(baseBeforeSHA256) || !digestPattern.MatchString(baseAfterSHA256) || nowMillis < 0 {
		return Decision{Code: CodeResponseUnknown}
	}
	seenWorkflow := map[int64]struct{}{}
	matching := []WorkflowRun{}
	for _, run := range workflows.Runs {
		if run.StartedAtMillis > nowMillis || run.UpdatedAtMillis > nowMillis {
			return Decision{Code: CodeResponseUnknown}
		}
		if _, duplicate := seenWorkflow[run.ID]; duplicate {
			return Decision{Code: CodeWorkflowAmbiguous}
		}
		seenWorkflow[run.ID] = struct{}{}
		if run.HeadSHA != binding.CandidateSHA || run.HeadRepositoryID != binding.GitHubRepositoryID {
			return Decision{Code: CodeWorkflowSHAMismatch}
		}
		if run.WorkflowID == policy.WorkflowID || run.Name == policy.WorkflowName {
			if run.WorkflowID != policy.WorkflowID || run.Name != policy.WorkflowName {
				return Decision{Code: CodeWorkflowAmbiguous}
			}
			matching = append(matching, run)
		}
	}
	if len(matching) == 0 {
		return Decision{Outcome: OutcomePending, Code: CodeWorkflowAbsent}
	}
	if len(matching) != 1 {
		return Decision{Code: CodeWorkflowAmbiguous}
	}
	workflow := matching[0]
	workflowOutcome, workflowCode := terminalOutcome(workflow.Status, workflow.Conclusion)
	if workflowOutcome == OutcomePending {
		return Decision{Outcome: OutcomePending, Code: workflowCode}
	}
	seenChecks := map[int64]struct{}{}
	for _, check := range checks.Checks {
		if check.StartedAtMillis > nowMillis || check.CompletedAtMillis > nowMillis {
			return Decision{Code: CodeResponseUnknown}
		}
		if _, duplicate := seenChecks[check.ID]; duplicate {
			return Decision{Code: CodeCheckAmbiguous}
		}
		seenChecks[check.ID] = struct{}{}
		if check.HeadSHA != binding.CandidateSHA || check.SuiteHeadSHA != binding.CandidateSHA {
			return Decision{Code: CodeCheckSHAMismatch}
		}
	}
	seenStatus := map[int64]struct{}{}
	contexts := map[string]int{}
	for _, status := range statuses.Statuses {
		if status.UpdatedAtMillis > nowMillis {
			return Decision{Code: CodeResponseUnknown}
		}
		if _, duplicate := seenStatus[status.ID]; duplicate {
			return Decision{Code: CodeStatusAmbiguous}
		}
		seenStatus[status.ID] = struct{}{}
		if status.SHA != binding.CandidateSHA {
			return Decision{Code: CodeStatusSHAMismatch}
		}
		contexts[status.Context]++
		if contexts[status.Context] > 1 {
			return Decision{Code: CodeStatusAmbiguous}
		}
	}
	observed := make([]ObservedCheck, 0, len(policy.RequiredChecks))
	resultOutcome, resultCode := workflowOutcome, workflowCode
	for _, required := range policy.RequiredChecks {
		if required.Kind == CheckRunKind {
			matches := []CheckRun{}
			for _, check := range checks.Checks {
				if check.Name == required.Name && check.AppID == required.AppID && check.AppSlug == required.AppSlug {
					matches = append(matches, check)
				}
			}
			if len(matches) == 0 {
				return Decision{Code: CodeCheckMissing}
			}
			if len(matches) != 1 || contexts[required.Name] > 0 {
				return Decision{Code: CodeCheckAmbiguous}
			}
			check := matches[0]
			if check.SuiteID != workflow.CheckSuiteID {
				return Decision{Code: CodeCheckSuiteAmbiguous}
			}
			outcome, code := terminalOutcome(check.Status, check.Conclusion)
			if outcome == OutcomePending {
				return Decision{Outcome: outcome, Code: code}
			}
			if outcome != OutcomePassed {
				resultOutcome, resultCode = outcome, code
			}
			observed = append(observed, ObservedCheck{ID: required.ID, Name: required.Name, ProviderID: "app:" + strconv.FormatInt(required.AppID, 10), Kind: required.Kind})
		} else {
			matches := []CommitStatus{}
			for _, status := range statuses.Statuses {
				if status.Context == required.Name && status.CreatorID == required.CreatorID && strings.EqualFold(status.CreatorLogin, required.CreatorLogin) {
					matches = append(matches, status)
				}
			}
			if len(matches) == 0 {
				return Decision{Code: CodeCheckMissing}
			}
			if len(matches) != 1 {
				return Decision{Code: CodeStatusAmbiguous}
			}
			status := matches[0]
			if status.State == "pending" {
				return Decision{Outcome: OutcomePending, Code: CodePending}
			}
			if status.State != "success" {
				resultOutcome, resultCode = OutcomeFailed, CodeFailed
			}
			observed = append(observed, ObservedCheck{ID: required.ID, Name: required.Name, ProviderID: "creator:" + strconv.FormatInt(required.CreatorID, 10), Kind: required.Kind})
		}
	}
	if statuses.TotalCount > 0 {
		for _, status := range statuses.Statuses {
			if status.State == "pending" {
				return Decision{Outcome: OutcomePending, Code: CodePending}
			}
		}
		if statuses.CombinedState != "success" || slices.ContainsFunc(statuses.Statuses, func(value CommitStatus) bool { return value.State != "success" }) {
			resultOutcome, resultCode = OutcomeFailed, CodeFailed
		}
	}
	sort.Slice(observed, func(left, right int) bool { return observed[left].ID < observed[right].ID })
	completed := workflow.UpdatedAtMillis
	for _, check := range checks.Checks {
		if check.CompletedAtMillis > completed {
			completed = check.CompletedAtMillis
		}
	}
	for _, status := range statuses.Statuses {
		if status.UpdatedAtMillis > completed {
			completed = status.UpdatedAtMillis
		}
	}
	evidence := SealEvidence(Evidence{BindingSHA256: binding.BindingSHA256, WorkflowRunID: strconv.FormatInt(workflow.ID, 10),
		WorkflowAttempt: workflow.Attempt, WorkflowCheckSuite: workflow.CheckSuiteID, Checks: observed, StatusRollup: statuses.CombinedState,
		RepositoryBeforeSHA256: repositoryBeforeSHA256, RepositoryAfterSHA256: repositoryAfterSHA256,
		BaseBeforeSHA256: baseBeforeSHA256, BaseAfterSHA256: baseAfterSHA256, Outcome: resultOutcome,
		StartedAtMillis: workflow.StartedAtMillis, CompletedAtMillis: completed, ObservedAtMillis: nowMillis})
	if !ValidEvidence(evidence, binding) {
		return Decision{Code: CodeResponseUnknown}
	}
	return Decision{Outcome: resultOutcome, Code: resultCode, Evidence: &evidence}
}

type Phase string

const (
	PhaseIntent      Phase = "intent_recorded"
	PhaseReserved    Phase = "budget_reserved"
	PhasePending     Phase = "pending"
	PhasePassed      Phase = "passed"
	PhaseFailed      Phase = "failed"
	PhaseTimedOut    Phase = "timed_out"
	PhaseWaiting     Phase = "waiting_external"
	PhaseNeedsYou    Phase = "needs_you"
	PhaseInvalidated Phase = "invalidated"
)

type BaseInvalidation struct {
	OldBaseSHA       string              `json:"oldBaseSha"`
	NewBaseSHA       string              `json:"newBaseSha,omitempty"`
	NewBasePresent   bool                `json:"newBasePresent"`
	BeforeFactSHA256 string              `json:"beforeFactSha256"`
	AfterFactSHA256  string              `json:"afterFactSha256"`
	PriorAuthority   candidate.Authority `json:"priorAuthority"`
	ObservedAtMillis int64               `json:"observedAtMillis"`
}

type State struct {
	SchemaVersion       string                 `json:"schemaVersion"`
	ValidationKey       string                 `json:"validationKey"`
	Binding             Binding                `json:"binding"`
	Policy              Policy                 `json:"policy"`
	Cycle               uint32                 `json:"cycle"`
	EffectID            string                 `json:"effectId"`
	BudgetReservationID string                 `json:"budgetReservationId"`
	Phase               Phase                  `json:"phase"`
	Code                Code                   `json:"code"`
	Repository          *RepositoryObservation `json:"repository,omitempty"`
	RepositoryAfter     *RepositoryObservation `json:"repositoryAfter,omitempty"`
	Workflows           *WorkflowScan          `json:"workflows,omitempty"`
	Checks              *CheckScan             `json:"checks,omitempty"`
	Statuses            *StatusScan            `json:"statuses,omitempty"`
	BaseBeforeSHA256    string                 `json:"baseBeforeSha256,omitempty"`
	BaseAfterSHA256     string                 `json:"baseAfterSha256,omitempty"`
	Evidence            *Evidence              `json:"evidence,omitempty"`
	BaseInvalidation    *BaseInvalidation      `json:"baseInvalidation,omitempty"`
	Invalidated         bool                   `json:"invalidated"`
	InvalidationCode    Code                   `json:"invalidationCode,omitempty"`
}

func CloneState(value State) State {
	value.Policy.RequiredChecks = slices.Clone(value.Policy.RequiredChecks)
	if value.Workflows != nil {
		copy := *value.Workflows
		copy.Runs = slices.Clone(copy.Runs)
		value.Workflows = &copy
	}
	if value.Checks != nil {
		copy := *value.Checks
		copy.Checks = slices.Clone(copy.Checks)
		value.Checks = &copy
	}
	if value.Statuses != nil {
		copy := *value.Statuses
		copy.Statuses = slices.Clone(copy.Statuses)
		value.Statuses = &copy
	}
	if value.Evidence != nil {
		copy := *value.Evidence
		copy.Checks = slices.Clone(copy.Checks)
		value.Evidence = &copy
	}
	if value.Repository != nil {
		copy := *value.Repository
		value.Repository = &copy
	}
	if value.RepositoryAfter != nil {
		copy := *value.RepositoryAfter
		value.RepositoryAfter = &copy
	}
	if value.BaseInvalidation != nil {
		copy := *value.BaseInvalidation
		value.BaseInvalidation = &copy
	}
	return value
}

func ValidationKey(binding Binding) string {
	return "validation-" + digest(struct {
		Candidate, Base, Policy string
		Generation              uint64
	}{binding.CandidateSHA, binding.BaseSHA, binding.PolicySHA256, binding.CandidateGeneration})[:32]
}
func NewState(binding Binding, policy Policy, cycle uint32) (State, bool) {
	if !ValidBinding(binding) || !ValidPolicy(policy) || binding.PolicySHA256 != policy.SHA256 || cycle == 0 || cycle > MaximumCICycles {
		return State{}, false
	}
	key := ValidationKey(binding)
	value := State{SchemaVersion: StateSchemaVersion, ValidationKey: key, Binding: binding, Policy: policy, Cycle: cycle,
		EffectID: "remote-ci-" + digest(struct {
			Key   string
			Cycle uint32
		}{key, cycle})[:32], Phase: PhaseIntent}
	value.BudgetReservationID = "budget-reservation-" + digest(value.EffectID)[:32]
	return value, ValidState(value)
}

func ValidBaseInvalidation(value BaseInvalidation, state State) bool {
	newBaseValid := value.NewBasePresent && gitOIDPattern.MatchString(value.NewBaseSHA) && value.OldBaseSHA != value.NewBaseSHA && len(value.OldBaseSHA) == len(value.NewBaseSHA) ||
		!value.NewBasePresent && value.NewBaseSHA == ""
	return gitOIDPattern.MatchString(value.OldBaseSHA) && newBaseValid &&
		value.OldBaseSHA == state.Binding.BaseSHA && digestPattern.MatchString(value.BeforeFactSHA256) &&
		digestPattern.MatchString(value.AfterFactSHA256) && candidate.ValidAuthority(value.PriorAuthority) && value.PriorAuthority.CandidateID == state.Binding.CandidateID &&
		value.PriorAuthority.CandidateSHA == state.Binding.CandidateSHA && value.PriorAuthority.BaseSHA == state.Binding.BaseSHA &&
		value.PriorAuthority.BindingSHA256 == state.Binding.ManifestSHA256 && value.PriorAuthority.Generation == state.Binding.CandidateGeneration &&
		value.ObservedAtMillis >= 0
}

func ValidState(value State) bool {
	if value.SchemaVersion != StateSchemaVersion || !ValidBinding(value.Binding) || !ValidPolicy(value.Policy) || value.Binding.PolicySHA256 != value.Policy.SHA256 ||
		value.ValidationKey != ValidationKey(value.Binding) || value.Cycle == 0 || value.Cycle > MaximumCICycles || !safeText(value.EffectID) ||
		!safeText(value.BudgetReservationID) || !slices.Contains([]Phase{PhaseIntent, PhaseReserved, PhasePending, PhasePassed, PhaseFailed, PhaseTimedOut, PhaseWaiting, PhaseNeedsYou, PhaseInvalidated}, value.Phase) || value.Code != "" && !validCode(value.Code) ||
		(value.Repository != nil && value.Repository.FactSHA256 != digest(repositoryValue(*value.Repository))) ||
		(value.RepositoryAfter != nil && value.RepositoryAfter.FactSHA256 != digest(repositoryValue(*value.RepositoryAfter))) ||
		(value.Workflows != nil && !validWorkflowScan(*value.Workflows)) || (value.Checks != nil && !validCheckScan(*value.Checks)) ||
		(value.Statuses != nil && !validStatusScan(*value.Statuses)) || (value.BaseBeforeSHA256 != "" && !digestPattern.MatchString(value.BaseBeforeSHA256)) ||
		(value.BaseAfterSHA256 != "" && !digestPattern.MatchString(value.BaseAfterSHA256)) || (value.Evidence != nil && !ValidEvidence(*value.Evidence, value.Binding)) {
		return false
	}
	terminal := value.Phase == PhasePassed || value.Phase == PhaseFailed || value.Phase == PhaseTimedOut
	if terminal != (value.Evidence != nil) {
		return false
	}
	if terminal {
		if value.Repository == nil || value.RepositoryAfter == nil || value.Workflows == nil || value.Checks == nil || value.Statuses == nil ||
			value.Repository.Code != CodeOK || value.RepositoryAfter.Code != CodeOK ||
			!CurrentRepositoryObservation(*value.Repository, value.Binding, value.Evidence.ObservedAtMillis) ||
			!CurrentRepositoryObservation(*value.RepositoryAfter, value.Binding, value.Evidence.ObservedAtMillis) {
			return false
		}
		decision := Evaluate(value.Binding, value.Policy, *value.Workflows, *value.Checks, *value.Statuses,
			RepositoryObservationSHA256(*value.Repository), RepositoryObservationSHA256(*value.RepositoryAfter),
			value.BaseBeforeSHA256, value.BaseAfterSHA256, value.Evidence.ObservedAtMillis)
		expectedPhase := map[Outcome]Phase{OutcomePassed: PhasePassed, OutcomeFailed: PhaseFailed, OutcomeTimedOut: PhaseTimedOut}[value.Evidence.Outcome]
		if decision.Evidence == nil || decision.Evidence.SHA256 != value.Evidence.SHA256 || decision.Code != value.Code || expectedPhase != value.Phase {
			return false
		}
	}
	if value.Invalidated != (value.Phase == PhaseInvalidated) || value.Invalidated != (value.InvalidationCode != "") {
		return false
	}
	if value.BaseInvalidation != nil && (!value.Invalidated || !ValidBaseInvalidation(*value.BaseInvalidation, value)) {
		return false
	}
	return value.BaseInvalidation == nil || value.InvalidationCode == CodeBaseChanged || value.InvalidationCode == CodeBaseRace
}

func Invalidate(value State, code Code) State {
	value.Invalidated, value.InvalidationCode, value.Phase, value.Code = true, code, PhaseInvalidated, code
	value.Evidence = nil
	return value
}

func ReviewObservation(state State) (domainreview.CIObservation, bool) {
	if !ValidState(state) || state.Evidence == nil || !ValidEvidence(*state.Evidence, state.Binding) {
		return domainreview.CIObservation{}, false
	}
	required := make([]string, len(state.Policy.RequiredChecks))
	for index, check := range state.Policy.RequiredChecks {
		required[index] = check.ID
	}
	sort.Strings(required)
	value := domainreview.SealCIObservation(domainreview.CIObservation{ID: state.Evidence.ID, SlotID: state.Binding.CISlotID,
		CandidateSHA: state.Binding.CandidateSHA, BaseSHA: state.Binding.BaseSHA, TreeSHA: state.Binding.TreeSHA,
		ManifestSHA256: state.Binding.ManifestSHA256, WorkflowRunID: state.Evidence.WorkflowRunID, RequiredChecks: required,
		Status: string(state.Evidence.Outcome), Authoritative: true, Complete: true, ObservedAtMillis: state.Evidence.ObservedAtMillis})
	return value, true
}
