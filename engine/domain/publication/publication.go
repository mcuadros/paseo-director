// SPDX-License-Identifier: Apache-2.0

// Package publication owns the pure pull-request publication contract. Git
// and GitHub adapters return bounded observations and perform one authorized
// operation; they never decide whether a branch or pull request may change.
package publication

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"regexp"
	"slices"
	"sort"
	"strings"
	"unicode"
	"unicode/utf8"
)

const (
	PolicySchemaVersion      = "director.publication-policy/v1"
	BindingSchemaVersion     = "director.publication-binding/v1"
	StateSchemaVersion       = "director.publication-state/v1"
	ObservationSchemaVersion = "director.publication-observation/v1"
	EvidenceSchemaVersion    = "director.publication-evidence/v1"
	MaximumObservationAgeMS  = int64(5_000)
	MaximumPullRequestPages  = uint32(10)
	PullRequestPageSize      = uint32(100)
	MaximumBodyBytes         = 64 * 1024
	MaximumHistoricalStates  = 64
	MaximumEffectAttempts    = uint32(3)
)

var (
	identifierPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.:@/-]{0,255}$`)
	loginPattern      = regexp.MustCompile(`^[A-Za-z0-9](?:[A-Za-z0-9-]{0,38})$`)
	digestPattern     = regexp.MustCompile(`^[0-9a-f]{64}$`)
	gitOIDPattern     = regexp.MustCompile(`^(?:[0-9a-f]{40}|[0-9a-f]{64})$`)
	riskPattern       = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.:-]{0,63}$`)
	secretPattern     = regexp.MustCompile(`(?i)(?:-----BEGIN [A-Z0-9 ]*PRIVATE KEY-----|github_pat_[A-Za-z0-9_]{16,}|gh[pousr]_[A-Za-z0-9]{16,}|sk-[A-Za-z0-9_-]{16,}|(?:password|secret|token|credential|authorization)\s*[:=]\s*\S+)`)
	privatePath       = regexp.MustCompile(`(?:^|[[:space:]` + "`" + `'(])/(?:home|tmp|var/tmp|run/user|root)(?:/|[[:space:]` + "`" + `')])`)
)

func digest(value any) string {
	encoded, err := json.Marshal(value)
	if err != nil {
		panic("marshal fixed publication value: " + err.Error())
	}
	sum := sha256.Sum256(encoded)
	return hex.EncodeToString(sum[:])
}

// Timing is the explicit Project -> Workspace -> Task publication timing.
// Configuration omission is resolved to the review-before-PR value.
type Timing string

const (
	TimingReviewBeforePR  Timing = "review_before_pr"
	TimingPublishBeforePR Timing = "publish_before_review"
)

// Policy is frozen into a Run. Pull-request delivery is the only mode this
// package admits; a GitHub failure can never rewrite it to direct delivery.
type Policy struct {
	SchemaVersion       string   `json:"schemaVersion"`
	DeliveryMode        string   `json:"deliveryMode"`
	Timing              Timing   `json:"timing"`
	RemoteName          string   `json:"remoteName"`
	ProtectedBranches   []string `json:"protectedBranches"`
	MaximumPages        uint32   `json:"maximumPages"`
	PageSize            uint32   `json:"pageSize"`
	ObservationAgeMS    int64    `json:"observationAgeMillis"`
	RollbackDescription string   `json:"rollbackDescription"`
	SHA256              string   `json:"sha256"`
}

func policyValue(value Policy) Policy {
	value.SHA256 = ""
	value.ProtectedBranches = slices.Clone(value.ProtectedBranches)
	sort.Strings(value.ProtectedBranches)
	return value
}

func PolicySHA256(value Policy) string { return digest(policyValue(value)) }

// NewPolicy freezes the exact delivery mode and explicit timing. Callers may
// only add protected branches; main, master, and the configured base are
// always protected independently by the publication binding.
func NewPolicy(deliveryMode string, publishBeforeReview bool, protected []string) Policy {
	timing := TimingReviewBeforePR
	if publishBeforeReview {
		timing = TimingPublishBeforePR
	}
	value := Policy{
		SchemaVersion: PolicySchemaVersion, DeliveryMode: deliveryMode, Timing: timing,
		RemoteName: "origin", ProtectedBranches: slices.Clone(protected),
		MaximumPages: MaximumPullRequestPages, PageSize: PullRequestPageSize,
		ObservationAgeMS:    MaximumObservationAgeMS,
		RollbackDescription: "Close the pull request and preserve the owned Task branch for reconciliation.",
	}
	sort.Strings(value.ProtectedBranches)
	value.ProtectedBranches = slices.Compact(value.ProtectedBranches)
	value.SHA256 = PolicySHA256(value)
	return value
}

func safeBranch(value string) bool {
	if len(value) == 0 || len(value) > 255 || value == "@" || strings.HasPrefix(value, "-") ||
		strings.HasPrefix(value, "/") || strings.HasSuffix(value, "/") || strings.HasSuffix(value, ".") ||
		strings.HasSuffix(value, ".lock") || strings.Contains(value, "..") || strings.Contains(value, "@{") ||
		strings.Contains(value, "//") {
		return false
	}
	for _, component := range strings.Split(value, "/") {
		if component == "" || strings.HasPrefix(component, ".") || strings.HasSuffix(component, ".lock") {
			return false
		}
	}
	return strings.IndexFunc(value, func(character rune) bool {
		return character <= ' ' || character == 0x7f || strings.ContainsRune(`~^:?*[\`, character)
	}) < 0
}

func ValidPolicy(value Policy) bool {
	if value.SchemaVersion != PolicySchemaVersion || value.DeliveryMode != "pull_request" ||
		(value.Timing != TimingReviewBeforePR && value.Timing != TimingPublishBeforePR) ||
		value.RemoteName != "origin" || value.MaximumPages == 0 || value.MaximumPages > MaximumPullRequestPages ||
		value.PageSize == 0 || value.PageSize > PullRequestPageSize || value.ObservationAgeMS <= 0 ||
		value.ObservationAgeMS > MaximumObservationAgeMS || !safePublicText(value.RollbackDescription, 256) ||
		len(value.ProtectedBranches) > 64 || !slices.IsSorted(value.ProtectedBranches) ||
		value.SHA256 != PolicySHA256(value) {
		return false
	}
	for index, branch := range value.ProtectedBranches {
		if !safeBranch(branch) || index > 0 && value.ProtectedBranches[index-1] == branch {
			return false
		}
	}
	return true
}

// Binding freezes every identity which can invalidate a publication effect.
// OwnershipSHA256 is the only ownership material which may be public.
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
	Branch                  string `json:"branch"`
	BaseRef                 string `json:"baseRef"`
	RepositoryBindingSHA256 string `json:"repositoryBindingSha256"`
	CanonicalRemote         string `json:"canonicalRemote"`
	GitHubRepositoryID      int64  `json:"githubRepositoryId"`
	GitHubRepositoryNodeID  string `json:"githubRepositoryNodeId"`
	RepositoryOwner         string `json:"repositoryOwner"`
	RepositoryName          string `json:"repositoryName"`
	HeadOwner               string `json:"headOwner"`
	OwnershipSHA256         string `json:"ownershipSha256"`
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
	return value.SchemaVersion == BindingSchemaVersion && identifierPattern.MatchString(value.TaskID) &&
		identifierPattern.MatchString(value.RunID) && identifierPattern.MatchString(value.CandidateID) &&
		gitOIDPattern.MatchString(value.CandidateSHA) && gitOIDPattern.MatchString(value.BaseSHA) &&
		gitOIDPattern.MatchString(value.TreeSHA) && len(value.CandidateSHA) == len(value.BaseSHA) &&
		len(value.CandidateSHA) == len(value.TreeSHA) && digestPattern.MatchString(value.ManifestSHA256) &&
		value.CandidateGeneration > 0 && strings.HasPrefix(value.Branch, "task/") &&
		safeBranch(value.Branch) && strings.HasPrefix(value.BaseRef, "refs/heads/") &&
		safeBranch(strings.TrimPrefix(value.BaseRef, "refs/heads/")) && value.Branch != strings.TrimPrefix(value.BaseRef, "refs/heads/") &&
		digestPattern.MatchString(value.RepositoryBindingSHA256) && strings.HasPrefix(value.CanonicalRemote, "https://github.com/") &&
		value.GitHubRepositoryID > 0 && identifierPattern.MatchString(value.GitHubRepositoryNodeID) &&
		loginPattern.MatchString(value.RepositoryOwner) && identifierPattern.MatchString(value.RepositoryName) &&
		loginPattern.MatchString(value.HeadOwner) && digestPattern.MatchString(value.OwnershipSHA256) &&
		digestPattern.MatchString(value.PolicySHA256) && digestPattern.MatchString(value.BindingSHA256) &&
		value.BindingSHA256 == BindingSHA256(value)
}

// Marker is stable across corrected Candidates in one Run, allowing the
// engine to update one owned draft without treating closed history as current.
func Marker(binding Binding) string {
	return fmt.Sprintf("<!-- director-publication/v1 task=%s run=%s branch=%s owner-sha256=%s -->",
		binding.TaskID, binding.RunID, binding.Branch, binding.OwnershipSHA256)
}

// Template contains only deterministic public facts. It never includes an
// author transcript, a local path, raw provider output, or a credential.
type Template struct {
	Title            string   `json:"title"`
	Body             string   `json:"body"`
	ReviewStatus     string   `json:"reviewStatus"`
	ReviewEvidenceID string   `json:"reviewEvidenceId,omitempty"`
	ValidationStatus string   `json:"validationStatus"`
	ValidationID     string   `json:"validationId,omitempty"`
	RiskCodes        []string `json:"riskCodes"`
	SHA256           string   `json:"sha256"`
}

func templateValue(value Template) Template {
	value.SHA256 = ""
	value.RiskCodes = slices.Clone(value.RiskCodes)
	return value
}

func TemplateSHA256(value Template) string { return digest(templateValue(value)) }

func safePublicText(value string, maximum int) bool {
	return value != "" && len(value) <= maximum && utf8.ValidString(value) && value == strings.TrimSpace(value) &&
		strings.IndexFunc(value, unicode.IsControl) < 0 && !secretPattern.MatchString(value) &&
		!privatePath.MatchString(value) && !strings.Contains(value, "<!--")
}

// RenderTemplate produces the one canonical title/body layout. Risk input is
// a closed code set copied from the durable Candidate claim.
func RenderTemplate(binding Binding, policy Policy, taskTitle, reviewStatus, reviewID, validationStatus, validationID string, risks []string) (Template, bool) {
	if !ValidBinding(binding) || !ValidPolicy(policy) || binding.PolicySHA256 != policy.SHA256 ||
		!safePublicText(taskTitle, 200) || !slices.Contains([]string{"pending", "approve_candidate", "changes_requested", "needs_human_decision"}, reviewStatus) ||
		!slices.Contains([]string{"pending", "passed", "failed", "timed_out", "unavailable", "not_configured"}, validationStatus) ||
		(reviewID != "" && !identifierPattern.MatchString(reviewID)) || (validationID != "" && !identifierPattern.MatchString(validationID)) ||
		len(risks) > 32 {
		return Template{}, false
	}
	riskCodes := slices.Clone(risks)
	sort.Strings(riskCodes)
	riskCodes = slices.Compact(riskCodes)
	for _, code := range riskCodes {
		if !riskPattern.MatchString(code) {
			return Template{}, false
		}
	}
	riskText := "none recorded"
	if len(riskCodes) > 0 {
		riskText = strings.Join(riskCodes, ", ")
	}
	reviewText := reviewStatus
	if reviewID != "" {
		reviewText += " (" + reviewID + ")"
	}
	validationText := validationStatus
	if validationID != "" {
		validationText += " (" + validationID + ")"
	}
	title := binding.TaskID + ": " + taskTitle
	body := fmt.Sprintf("## Candidate\n\n- Commit: `%s`\n- Base: `%s`\n- Tree: `%s`\n- Manifest: `%s`\n\n## Quality facts\n\n- Independent review: %s\n- Validation: %s\n\n## Delivery facts\n\n- Beads Task: `%s`\n- Run: `%s`\n- Branch: `%s`\n- Risks: %s\n- Rollback: %s\n\n%s\n",
		binding.CandidateSHA, binding.BaseSHA, binding.TreeSHA, binding.ManifestSHA256,
		reviewText, validationText, binding.TaskID, binding.RunID, binding.Branch, riskText,
		policy.RollbackDescription, Marker(binding))
	if len(body) > MaximumBodyBytes || secretPattern.MatchString(body) || privatePath.MatchString(body) {
		return Template{}, false
	}
	value := Template{Title: title, Body: body, ReviewStatus: reviewStatus, ReviewEvidenceID: reviewID,
		ValidationStatus: validationStatus, ValidationID: validationID, RiskCodes: riskCodes}
	value.SHA256 = TemplateSHA256(value)
	return value, true
}

func ValidTemplate(value Template, binding Binding, policy Policy) bool {
	if len(value.Body) == 0 || len(value.Body) > MaximumBodyBytes || !safePublicText(value.Title, 256) ||
		secretPattern.MatchString(value.Body) || privatePath.MatchString(value.Body) || value.SHA256 != TemplateSHA256(value) ||
		!strings.HasSuffix(value.Body, Marker(binding)+"\n") {
		return false
	}
	want, ok := RenderTemplate(binding, policy, strings.TrimPrefix(value.Title, binding.TaskID+": "), value.ReviewStatus,
		value.ReviewEvidenceID, value.ValidationStatus, value.ValidationID, value.RiskCodes)
	return ok && want.SHA256 == value.SHA256
}

// ExternalCode is the closed Git/GitHub observation and refusal vocabulary.
type ExternalCode string

const (
	CodeOK                   ExternalCode = "PUBLICATION_OK"
	CodeUnavailable          ExternalCode = "PUBLICATION_UNAVAILABLE"
	CodeTLS                  ExternalCode = "PUBLICATION_TLS_FAILURE"
	CodeRateLimited          ExternalCode = "PUBLICATION_RATE_LIMITED"
	CodeUnauthorized         ExternalCode = "PUBLICATION_UNAUTHORIZED"
	CodeForbidden            ExternalCode = "PUBLICATION_FORBIDDEN"
	CodeNotFound             ExternalCode = "PUBLICATION_NOT_FOUND"
	CodeConflict             ExternalCode = "PUBLICATION_CONFLICT"
	CodeUnprocessable        ExternalCode = "PUBLICATION_UNPROCESSABLE"
	CodeServer               ExternalCode = "PUBLICATION_SERVER_ERROR"
	CodeRepositoryMismatch   ExternalCode = "PUBLICATION_REPOSITORY_MISMATCH"
	CodeCapabilityMissing    ExternalCode = "PUBLICATION_CAPABILITY_MISSING"
	CodePaginationIncomplete ExternalCode = "PUBLICATION_PAGINATION_INCOMPLETE"
	CodeRefChanged           ExternalCode = "PUBLICATION_REMOTE_REF_CHANGED"
	CodeProtectedRef         ExternalCode = "PUBLICATION_PROTECTED_REF"
	CodePullRequestUnowned   ExternalCode = "PUBLICATION_PULL_REQUEST_UNOWNED"
	CodePullRequestDuplicate ExternalCode = "PUBLICATION_PULL_REQUEST_DUPLICATE"
	CodePullRequestClosed    ExternalCode = "PUBLICATION_PULL_REQUEST_CLOSED"
	CodeMarkerForged         ExternalCode = "PUBLICATION_MARKER_FORGED"
	CodeHeadChanged          ExternalCode = "PUBLICATION_HEAD_CHANGED"
	CodeBaseChanged          ExternalCode = "PUBLICATION_BASE_CHANGED"
	CodeResponseUnknown      ExternalCode = "PUBLICATION_RESPONSE_UNKNOWN"
)

func validExternalCode(value ExternalCode) bool {
	return slices.Contains([]ExternalCode{CodeOK, CodeUnavailable, CodeTLS, CodeRateLimited, CodeUnauthorized,
		CodeForbidden, CodeNotFound, CodeConflict, CodeUnprocessable, CodeServer, CodeRepositoryMismatch,
		CodeCapabilityMissing, CodePaginationIncomplete, CodeRefChanged, CodeProtectedRef, CodePullRequestUnowned,
		CodePullRequestDuplicate, CodePullRequestClosed, CodeMarkerForged, CodeHeadChanged, CodeBaseChanged,
		CodeResponseUnknown}, value)
}

// RepositoryObservation is the authenticated, rate-aware GitHub preflight.
type RepositoryObservation struct {
	SchemaVersion     string       `json:"schemaVersion"`
	ID                string       `json:"id"`
	Code              ExternalCode `json:"code"`
	RepositoryID      int64        `json:"repositoryId"`
	RepositoryNodeID  string       `json:"repositoryNodeId"`
	Owner             string       `json:"owner"`
	Name              string       `json:"name"`
	ViewerLogin       string       `json:"viewerLogin"`
	Authenticated     bool         `json:"authenticated"`
	CanPush           bool         `json:"canPush"`
	CanPullRequests   bool         `json:"canPullRequests"`
	Archived          bool         `json:"archived"`
	Disabled          bool         `json:"disabled"`
	TLSVerified       bool         `json:"tlsVerified"`
	APIVersion        string       `json:"apiVersion"`
	RateRemaining     int64        `json:"rateRemaining"`
	RateResetAtMillis int64        `json:"rateResetAtMillis"`
	ObservedAtMillis  int64        `json:"observedAtMillis"`
	MaximumAgeMillis  int64        `json:"maximumAgeMillis"`
	FactSHA256        string       `json:"factSha256"`
}

func repositoryObservationValue(value RepositoryObservation) RepositoryObservation {
	value.FactSHA256 = ""
	return value
}
func RepositoryObservationSHA256(value RepositoryObservation) string {
	return digest(repositoryObservationValue(value))
}
func SealRepositoryObservation(value RepositoryObservation) RepositoryObservation {
	value.SchemaVersion = ObservationSchemaVersion
	value.FactSHA256 = RepositoryObservationSHA256(value)
	return value
}

func CurrentRepositoryObservation(value RepositoryObservation, binding Binding, nowMillis int64) bool {
	if value.SchemaVersion != ObservationSchemaVersion || !identifierPattern.MatchString(value.ID) || !validExternalCode(value.Code) ||
		value.ObservedAtMillis < 0 || value.ObservedAtMillis > nowMillis || value.MaximumAgeMillis <= 0 ||
		nowMillis-value.ObservedAtMillis > value.MaximumAgeMillis || value.FactSHA256 != RepositoryObservationSHA256(value) {
		return false
	}
	if value.Code != CodeOK {
		return true
	}
	return value.RepositoryID == binding.GitHubRepositoryID && value.RepositoryNodeID == binding.GitHubRepositoryNodeID &&
		strings.EqualFold(value.Owner, binding.RepositoryOwner) && value.Name == binding.RepositoryName &&
		strings.EqualFold(value.ViewerLogin, binding.HeadOwner) && value.Authenticated && value.CanPush && value.CanPullRequests &&
		!value.Archived && !value.Disabled && value.TLSVerified && value.APIVersion == "2022-11-28" &&
		value.RateRemaining > 0 && value.RateResetAtMillis >= value.ObservedAtMillis
}

// RefObservation is an exact Git remote-ref fact. Empty OID with Exists=false
// is authoritative absence only when CodeOK is present.
type RefObservation struct {
	SchemaVersion    string       `json:"schemaVersion"`
	ID               string       `json:"id"`
	Code             ExternalCode `json:"code"`
	Ref              string       `json:"ref"`
	Exists           bool         `json:"exists"`
	OID              string       `json:"oid,omitempty"`
	RemoteCanonical  string       `json:"remoteCanonical"`
	ObservedAtMillis int64        `json:"observedAtMillis"`
	MaximumAgeMillis int64        `json:"maximumAgeMillis"`
	FactSHA256       string       `json:"factSha256"`
}

func refObservationValue(value RefObservation) RefObservation { value.FactSHA256 = ""; return value }
func RefObservationSHA256(value RefObservation) string        { return digest(refObservationValue(value)) }
func SealRefObservation(value RefObservation) RefObservation {
	value.SchemaVersion = ObservationSchemaVersion
	value.FactSHA256 = RefObservationSHA256(value)
	return value
}

func CurrentRefObservation(value RefObservation, binding Binding, nowMillis int64) bool {
	return CurrentNamedRefObservation(value, binding.CanonicalRemote, binding.Branch, nowMillis)
}

// CurrentNamedRefObservation validates either the Task branch or the live base
// branch without weakening the exact canonical-remote binding.
func CurrentNamedRefObservation(value RefObservation, canonicalRemote, branch string, nowMillis int64) bool {
	return value.SchemaVersion == ObservationSchemaVersion && identifierPattern.MatchString(value.ID) && validExternalCode(value.Code) &&
		value.Ref == "refs/heads/"+branch && value.RemoteCanonical == canonicalRemote && safeBranch(branch) &&
		value.ObservedAtMillis >= 0 && value.ObservedAtMillis <= nowMillis && value.MaximumAgeMillis > 0 &&
		nowMillis-value.ObservedAtMillis <= value.MaximumAgeMillis &&
		(value.Code != CodeOK || value.Exists == gitOIDPattern.MatchString(value.OID)) && value.FactSHA256 == RefObservationSHA256(value)
}

// PullRequest is the bounded identity needed to adopt or update one PR. No
// comment, review thread, raw body, or author transcript is retained.
type PullRequest struct {
	Number           int64  `json:"number"`
	NodeID           string `json:"nodeId"`
	URL              string `json:"url"`
	State            string `json:"state"`
	Draft            bool   `json:"draft"`
	HeadSHA          string `json:"headSha"`
	HeadRef          string `json:"headRef"`
	BaseRef          string `json:"baseRef"`
	HeadOwner        string `json:"headOwner"`
	HeadRepositoryID int64  `json:"headRepositoryId"`
	BaseRepositoryID int64  `json:"baseRepositoryId"`
	AuthorLogin      string `json:"authorLogin"`
	CreatedByViewer  bool   `json:"createdByViewer"`
	MarkerSHA256     string `json:"markerSha256,omitempty"`
	MarkerCount      uint32 `json:"markerCount"`
	TitleSHA256      string `json:"titleSha256"`
	BodySHA256       string `json:"bodySha256"`
	UpdatedAtMillis  int64  `json:"updatedAtMillis"`
}

func ValidPullRequest(value PullRequest) bool {
	return value.Number > 0 && identifierPattern.MatchString(value.NodeID) && strings.HasPrefix(value.URL, "https://github.com/") && len(value.URL) <= 2048 &&
		slices.Contains([]string{"open", "closed"}, value.State) && gitOIDPattern.MatchString(value.HeadSHA) &&
		safeBranch(value.HeadRef) && safeBranch(value.BaseRef) && loginPattern.MatchString(value.HeadOwner) &&
		value.HeadRepositoryID > 0 && value.BaseRepositoryID > 0 && loginPattern.MatchString(value.AuthorLogin) &&
		value.MarkerCount <= 2 && (value.MarkerSHA256 == "" || digestPattern.MatchString(value.MarkerSHA256)) &&
		digestPattern.MatchString(value.TitleSHA256) && digestPattern.MatchString(value.BodySHA256) && value.UpdatedAtMillis > 0
}

type PullRequestPage struct {
	SchemaVersion    string        `json:"schemaVersion"`
	ID               string        `json:"id"`
	Code             ExternalCode  `json:"code"`
	Page             uint32        `json:"page"`
	NextPage         uint32        `json:"nextPage"`
	Complete         bool          `json:"complete"`
	PullRequests     []PullRequest `json:"pullRequests"`
	ObservedAtMillis int64         `json:"observedAtMillis"`
	MaximumAgeMillis int64         `json:"maximumAgeMillis"`
	FactSHA256       string        `json:"factSha256"`
}

func pageValue(value PullRequestPage) PullRequestPage {
	value.FactSHA256 = ""
	value.PullRequests = slices.Clone(value.PullRequests)
	return value
}

func PullRequestPageSHA256(value PullRequestPage) string { return digest(pageValue(value)) }
func SealPullRequestPage(value PullRequestPage) PullRequestPage {
	value.SchemaVersion = ObservationSchemaVersion
	value.FactSHA256 = PullRequestPageSHA256(value)
	return value
}

func CurrentPullRequestPage(value PullRequestPage, page uint32, nowMillis int64) bool {
	if value.SchemaVersion != ObservationSchemaVersion || !identifierPattern.MatchString(value.ID) || !validExternalCode(value.Code) ||
		value.Page != page || value.ObservedAtMillis < 0 || value.ObservedAtMillis > nowMillis || value.MaximumAgeMillis <= 0 ||
		nowMillis-value.ObservedAtMillis > value.MaximumAgeMillis || len(value.PullRequests) > int(PullRequestPageSize) ||
		value.FactSHA256 != PullRequestPageSHA256(value) {
		return false
	}
	seen := map[int64]struct{}{}
	for _, pull := range value.PullRequests {
		if !ValidPullRequest(pull) {
			return false
		}
		if _, duplicate := seen[pull.Number]; duplicate {
			return false
		}
		seen[pull.Number] = struct{}{}
	}
	if value.Code != CodeOK {
		return value.NextPage == 0 && !value.Complete && len(value.PullRequests) == 0
	}
	return value.NextPage == 0 && value.Complete || value.NextPage == page+1 && !value.Complete
}

// Scan is one complete bounded pagination result.
type Scan struct {
	ID               string        `json:"id"`
	Pages            uint32        `json:"pages"`
	PullRequests     []PullRequest `json:"pullRequests"`
	ObservedAtMillis int64         `json:"observedAtMillis"`
	SHA256           string        `json:"sha256"`
}

func scanValue(value Scan) Scan {
	value.SHA256 = ""
	value.PullRequests = slices.Clone(value.PullRequests)
	return value
}
func ScanSHA256(value Scan) string { return digest(scanValue(value)) }
func SealScan(value Scan) Scan     { value.SHA256 = ScanSHA256(value); return value }

func ValidScan(value Scan) bool {
	if !identifierPattern.MatchString(value.ID) || value.Pages == 0 || value.Pages > MaximumPullRequestPages ||
		value.ObservedAtMillis < 0 || len(value.PullRequests) > int(MaximumPullRequestPages*PullRequestPageSize) ||
		value.SHA256 != ScanSHA256(value) {
		return false
	}
	seen := map[int64]struct{}{}
	for _, pull := range value.PullRequests {
		if !ValidPullRequest(pull) {
			return false
		}
		if _, duplicate := seen[pull.Number]; duplicate {
			return false
		}
		seen[pull.Number] = struct{}{}
	}
	return true
}

type EffectPhase string

const (
	EffectIntent      EffectPhase = "intent_recorded"
	EffectObserved    EffectPhase = "observed"
	EffectDispatching EffectPhase = "dispatching"
	EffectComplete    EffectPhase = "complete"
	EffectWaiting     EffectPhase = "waiting_external"
	EffectNeedsYou    EffectPhase = "needs_you"
)

type Effect struct {
	ID       string       `json:"id"`
	Kind     string       `json:"kind"`
	Class    string       `json:"class"`
	Phase    EffectPhase  `json:"phase"`
	Attempts uint32       `json:"attempts"`
	Code     ExternalCode `json:"code,omitempty"`
}

type OwnedPullRequest struct {
	Number       int64  `json:"number"`
	NodeID       string `json:"nodeId"`
	URL          string `json:"url"`
	MarkerSHA256 string `json:"markerSha256"`
	HeadSHA      string `json:"headSha"`
	Draft        bool   `json:"draft"`
}

type Evidence struct {
	SchemaVersion    string `json:"schemaVersion"`
	ID               string `json:"id"`
	BindingSHA256    string `json:"bindingSha256"`
	PullRequest      int64  `json:"pullRequest"`
	PullRequestNode  string `json:"pullRequestNode"`
	HeadSHA          string `json:"headSha"`
	Draft            bool   `json:"draft"`
	Ready            bool   `json:"ready"`
	TemplateSHA256   string `json:"templateSha256"`
	ObservedAtMillis int64  `json:"observedAtMillis"`
}

func evidenceValue(value Evidence) Evidence { value.ID = ""; return value }

func EvidenceID(value Evidence) string {
	return "publication-evidence-" + digest(evidenceValue(value))[:32]
}

func ValidEvidence(value Evidence, state State) bool {
	return value.SchemaVersion == EvidenceSchemaVersion && value.ID == EvidenceID(value) &&
		value.BindingSHA256 == state.Binding.BindingSHA256 && state.OwnedPullRequest != nil &&
		value.PullRequest == state.OwnedPullRequest.Number && value.PullRequestNode == state.OwnedPullRequest.NodeID &&
		value.HeadSHA == state.Binding.CandidateSHA && value.TemplateSHA256 == state.Template.SHA256 &&
		value.ObservedAtMillis >= 0 && value.Draft != value.Ready && value.Draft == state.OwnedPullRequest.Draft
}

// State is embedded in one Run and is serialized by the TaskStore Run CAS.
type State struct {
	SchemaVersion         string                 `json:"schemaVersion"`
	PublicationKey        string                 `json:"publicationKey"`
	Binding               Binding                `json:"binding"`
	Policy                Policy                 `json:"policy"`
	Template              Template               `json:"template"`
	ExpectedRemoteHead    string                 `json:"expectedRemoteHead,omitempty"`
	CarriedPullRequest    bool                   `json:"carriedPullRequest"`
	Quiesce               Effect                 `json:"quiesce"`
	Push                  Effect                 `json:"push"`
	PullRequest           Effect                 `json:"pullRequest"`
	Metadata              Effect                 `json:"metadata"`
	Ready                 Effect                 `json:"ready"`
	RepositoryObservation *RepositoryObservation `json:"repositoryObservation,omitempty"`
	RefObservation        *RefObservation        `json:"refObservation,omitempty"`
	BaseObservation       *RefObservation        `json:"baseObservation,omitempty"`
	Scan                  *Scan                  `json:"scan,omitempty"`
	OwnedPullRequest      *OwnedPullRequest      `json:"ownedPullRequest,omitempty"`
	Evidence              *Evidence              `json:"evidence,omitempty"`
	Invalidated           bool                   `json:"invalidated"`
	InvalidationCode      string                 `json:"invalidationCode,omitempty"`
}

func effect(binding Binding, kind, class string, complete bool) Effect {
	phase := EffectIntent
	if complete {
		phase = EffectComplete
	}
	return Effect{ID: kind + "-" + digest(struct{ Binding, Kind string }{binding.BindingSHA256, kind})[:32], Kind: kind, Class: class, Phase: phase}
}

func PublicationKey(binding Binding) string {
	return "publication-" + digest(struct{ Task, Run, Branch, Owner string }{binding.TaskID, binding.RunID, binding.Branch, binding.OwnershipSHA256})[:32]
}

func NewState(binding Binding, policy Policy, template Template, expectedRemoteHead string, carried *OwnedPullRequest) (State, bool) {
	if !ValidBinding(binding) || !ValidPolicy(policy) || binding.PolicySHA256 != policy.SHA256 ||
		!ValidTemplate(template, binding, policy) || expectedRemoteHead != "" && !gitOIDPattern.MatchString(expectedRemoteHead) {
		return State{}, false
	}
	state := State{SchemaVersion: StateSchemaVersion, PublicationKey: PublicationKey(binding), Binding: binding,
		Policy: policy, Template: template, ExpectedRemoteHead: expectedRemoteHead,
		Quiesce:     effect(binding, "publication.pr_draft", "conditional_update", carried == nil),
		Push:        effect(binding, "publication.push", "conditional_update", false),
		PullRequest: effect(binding, "publication.pr_create", "unique_create", false),
		Metadata:    effect(binding, "publication.pr_update", "conditional_update", false),
		Ready:       effect(binding, "publication.pr_ready", "conditional_update", false)}
	if carried != nil {
		copy := *carried
		state.OwnedPullRequest = &copy
		state.CarriedPullRequest = true
		state.ExpectedRemoteHead = carried.HeadSHA
	}
	return state, true
}

func validEffect(value Effect, expected Effect) bool {
	return value.ID == expected.ID && value.Kind == expected.Kind && value.Class == expected.Class &&
		slices.Contains([]EffectPhase{EffectIntent, EffectObserved, EffectDispatching, EffectComplete, EffectWaiting, EffectNeedsYou}, value.Phase) &&
		value.Attempts <= MaximumEffectAttempts && (value.Code == "" || validExternalCode(value.Code))
}

func ValidState(value State) bool {
	if value.SchemaVersion != StateSchemaVersion || !ValidBinding(value.Binding) || !ValidPolicy(value.Policy) ||
		value.Binding.PolicySHA256 != value.Policy.SHA256 || value.PublicationKey != PublicationKey(value.Binding) ||
		!ValidTemplate(value.Template, value.Binding, value.Policy) ||
		(value.ExpectedRemoteHead != "" && !gitOIDPattern.MatchString(value.ExpectedRemoteHead)) ||
		(value.RepositoryObservation != nil && value.RepositoryObservation.FactSHA256 != RepositoryObservationSHA256(*value.RepositoryObservation)) ||
		(value.RefObservation != nil && value.RefObservation.FactSHA256 != RefObservationSHA256(*value.RefObservation)) ||
		(value.BaseObservation != nil && value.BaseObservation.FactSHA256 != RefObservationSHA256(*value.BaseObservation)) ||
		(value.Scan != nil && !ValidScan(*value.Scan)) {
		return false
	}
	expected, ok := NewState(value.Binding, value.Policy, value.Template, value.ExpectedRemoteHead, func() *OwnedPullRequest {
		if value.CarriedPullRequest {
			return value.OwnedPullRequest
		}
		return nil
	}())
	if !ok || !validEffect(value.Quiesce, expected.Quiesce) || !validEffect(value.Push, expected.Push) ||
		!validEffect(value.PullRequest, expected.PullRequest) || !validEffect(value.Metadata, expected.Metadata) ||
		!validEffect(value.Ready, expected.Ready) {
		return false
	}
	if value.Quiesce.Phase != EffectComplete && (value.Push.Phase != EffectIntent || value.PullRequest.Phase != EffectIntent || value.Metadata.Phase != EffectIntent || value.Ready.Phase != EffectIntent) ||
		value.Push.Phase != EffectComplete && (value.PullRequest.Phase != EffectIntent || value.Metadata.Phase != EffectIntent || value.Ready.Phase != EffectIntent) ||
		value.PullRequest.Phase != EffectComplete && (value.Metadata.Phase != EffectIntent || value.Ready.Phase != EffectIntent) ||
		value.Metadata.Phase != EffectComplete && value.Ready.Phase != EffectIntent {
		return false
	}
	if value.OwnedPullRequest != nil {
		pull := value.OwnedPullRequest
		if pull.Number <= 0 || !identifierPattern.MatchString(pull.NodeID) || !digestPattern.MatchString(pull.MarkerSHA256) ||
			!gitOIDPattern.MatchString(pull.HeadSHA) || pull.URL == "" || len(pull.URL) > 2048 || !strings.HasPrefix(pull.URL, "https://github.com/") {
			return false
		}
	}
	if value.Evidence != nil {
		if !ValidEvidence(*value.Evidence, value) {
			return false
		}
	}
	return !value.Invalidated || value.InvalidationCode != ""
}

func Invalidate(value State, code string) State {
	value.Invalidated = true
	value.InvalidationCode = code
	value.Evidence = nil
	return value
}

// DispatchInFlight prevents Candidate replacement from racing a possible
// external handoff. Recovery must observe that exact frontier first.
func DispatchInFlight(value State) bool {
	return value.Quiesce.Phase == EffectDispatching || value.Push.Phase == EffectDispatching ||
		value.PullRequest.Phase == EffectDispatching || value.Metadata.Phase == EffectDispatching ||
		value.Ready.Phase == EffectDispatching
}

func Carry(value State) *OwnedPullRequest {
	if !ValidState(value) || value.OwnedPullRequest == nil {
		return nil
	}
	copy := *value.OwnedPullRequest
	return &copy
}

func EvidenceFor(value State, ready bool, nowMillis int64) *Evidence {
	if !ValidState(value) || value.OwnedPullRequest == nil || value.Push.Phase != EffectComplete ||
		value.PullRequest.Phase != EffectComplete || value.Metadata.Phase != EffectComplete ||
		(ready && value.Ready.Phase != EffectComplete) || (!ready && !value.OwnedPullRequest.Draft) {
		return nil
	}
	evidence := &Evidence{SchemaVersion: EvidenceSchemaVersion, BindingSHA256: value.Binding.BindingSHA256,
		PullRequest: value.OwnedPullRequest.Number, PullRequestNode: value.OwnedPullRequest.NodeID,
		HeadSHA: value.Binding.CandidateSHA, Draft: !ready, Ready: ready,
		TemplateSHA256: value.Template.SHA256, ObservedAtMillis: nowMillis}
	evidence.ID = EvidenceID(*evidence)
	return evidence
}

func DigestText(value string) string {
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:])
}
