// SPDX-License-Identifier: Apache-2.0

// Package review owns the pure mandatory independent-review contract. Review
// claims are model input; only these reducers may admit them as exact-Candidate
// evidence or authorize reviewer-owned disposable-resource cleanup.
package review

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
	BindingSchemaVersion       = "director.review-binding/v1"
	CIObservationSchemaVersion = "director.review-ci-observation/v1"
	ClaimSchemaVersion         = "director.review-claim/v1"
	EvidenceSchemaVersion      = "director.review-evidence/v1"
	StateSchemaVersion         = "director.review-state/v1"
	MaximumClaimBytes          = 64 * 1024
	MaximumFindings            = 64
	MaximumProbes              = 16
	MaximumReferences          = 16
)

var (
	identifierPattern  = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.:@/-]{0,255}$`)
	uuidPattern        = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[1-8][0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`)
	digestPattern      = regexp.MustCompile(`^[0-9a-f]{64}$`)
	gitOIDPattern      = regexp.MustCompile(`^(?:[0-9a-f]{40}|[0-9a-f]{64})$`)
	codePattern        = regexp.MustCompile(`^[A-Z][A-Z0-9_]{0,63}$`)
	workflowRunPattern = regexp.MustCompile(`^[1-9][0-9]{0,31}$`)
)

// Dimension is the mandatory complete review matrix. No profile or prompt may
// omit one of these dimensions.
type Dimension string

const (
	DimensionAcceptance      Dimension = "acceptance"
	DimensionCorrectness     Dimension = "correctness"
	DimensionSecurity        Dimension = "security"
	DimensionMaintainability Dimension = "maintainability"
	DimensionReadability     Dimension = "readability"
	DimensionDesign          Dimension = "design"
	DimensionQuality         Dimension = "quality"
	DimensionRigor           Dimension = "rigor"
)

var dimensions = []Dimension{
	DimensionAcceptance, DimensionCorrectness, DimensionSecurity, DimensionMaintainability,
	DimensionReadability, DimensionDesign, DimensionQuality, DimensionRigor,
}

// Dimensions returns the canonical mandatory matrix.
func Dimensions() []Dimension { return slices.Clone(dimensions) }

// ValidUUID applies exact lowercase UUID equality at every runtime, harness,
// and evidence boundary. Case folding and prefix aliases are deliberately not
// accepted.
func ValidUUID(value string) bool { return uuidPattern.MatchString(value) }

// ValidIdentifier exposes the bounded opaque identity vocabulary used for
// native workspaces and durable review resources. Native Reviewer identity is
// intentionally stricter and always uses ValidUUID.
func ValidIdentifier(value string) bool { return identifierPattern.MatchString(value) }

type Verdict string

const (
	VerdictApproveCandidate   Verdict = "approve_candidate"
	VerdictChangesRequested   Verdict = "changes_requested"
	VerdictNeedsHumanDecision Verdict = "needs_human_decision"
)

type Severity string

const (
	SeverityP0 Severity = "P0"
	SeverityP1 Severity = "P1"
	SeverityP2 Severity = "P2"
	SeverityP3 Severity = "P3"
)

// AttemptReason is deliberately closed. Each Candidate may have at most one
// harness/Reviewer attempt for a reason; possible handoff is never a retry
// reason. A changed immutable binding creates a new review key instead.
type AttemptReason string

const (
	AttemptInitial             AttemptReason = "initial"
	AttemptProviderPreDispatch AttemptReason = "provider_pre_dispatch_failure"
	AttemptHarnessPreStart     AttemptReason = "harness_pre_start_failure"
)

// Binding freezes every fact that can invalidate Review. CISlotID is the one
// sibling CI intent; CIObservation is attached before verdict admission and
// may not be replaced under the same review key.
type Binding struct {
	SchemaVersion       string `json:"schemaVersion"`
	TaskID              string `json:"taskId"`
	RunID               string `json:"runId"`
	CandidateID         string `json:"candidateId"`
	CandidateSHA        string `json:"candidateSha"`
	BaseSHA             string `json:"baseSha"`
	TreeSHA             string `json:"treeSha"`
	ManifestSHA256      string `json:"manifestSha256"`
	AcceptanceSHA256    string `json:"acceptanceSha256"`
	ConfigurationSHA256 string `json:"configurationSha256"`
	ProfileSHA256       string `json:"profileSha256"`
	ContextSHA256       string `json:"contextSha256"`
	DecisionsSHA256     string `json:"decisionsSha256"`
	FindingsSHA256      string `json:"findingsSha256"`
	CandidateGeneration uint64 `json:"candidateGeneration"`
	CISlotID            string `json:"ciSlotId"`
	TaskAgentUUID       string `json:"taskAgentUuid"`
	CoordinatorUUID     string `json:"coordinatorUuid"`
	ReviewOwnerUUID     string `json:"reviewOwnerUuid"`
	BindingSHA256       string `json:"bindingSha256"`
}

func digest(value any) string {
	encoded, err := json.Marshal(value)
	if err != nil {
		panic("marshal fixed review value: " + err.Error())
	}
	sum := sha256.Sum256(encoded)
	return hex.EncodeToString(sum[:])
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
		digestPattern.MatchString(value.AcceptanceSHA256) && digestPattern.MatchString(value.ConfigurationSHA256) &&
		digestPattern.MatchString(value.ProfileSHA256) && digestPattern.MatchString(value.ContextSHA256) &&
		digestPattern.MatchString(value.DecisionsSHA256) && digestPattern.MatchString(value.FindingsSHA256) &&
		value.CandidateGeneration > 0 && identifierPattern.MatchString(value.CISlotID) &&
		uuidPattern.MatchString(value.TaskAgentUUID) && uuidPattern.MatchString(value.CoordinatorUUID) &&
		uuidPattern.MatchString(value.ReviewOwnerUUID) && value.TaskAgentUUID != value.CoordinatorUUID &&
		value.ReviewOwnerUUID != value.TaskAgentUUID && value.ReviewOwnerUUID != value.CoordinatorUUID &&
		digestPattern.MatchString(value.BindingSHA256) &&
		value.BindingSHA256 == BindingSHA256(value)
}

// CIObservation is the sole complete-CI authority consumed by one Review.
// Review can be prepared before this row arrives, but prompt/verdict admission
// requires this exact immutable observation.
type CIObservation struct {
	SchemaVersion    string   `json:"schemaVersion"`
	ID               string   `json:"id"`
	SlotID           string   `json:"slotId"`
	CandidateSHA     string   `json:"candidateSha"`
	BaseSHA          string   `json:"baseSha"`
	TreeSHA          string   `json:"treeSha"`
	ManifestSHA256   string   `json:"manifestSha256"`
	WorkflowRunID    string   `json:"workflowRunId"`
	RequiredChecks   []string `json:"requiredChecks"`
	Status           string   `json:"status"`
	Authoritative    bool     `json:"authoritative"`
	Complete         bool     `json:"complete"`
	ObservedAtMillis int64    `json:"observedAtMillis"`
	SHA256           string   `json:"sha256"`
}

func ciValue(value CIObservation) CIObservation { value.SHA256 = ""; return value }

func CIObservationSHA256(value CIObservation) string { return digest(ciValue(value)) }

func SealCIObservation(value CIObservation) CIObservation {
	value.SchemaVersion = CIObservationSchemaVersion
	value.SHA256 = CIObservationSHA256(value)
	return value
}

func ValidCIObservation(value CIObservation, binding Binding) bool {
	return value.SchemaVersion == CIObservationSchemaVersion && identifierPattern.MatchString(value.ID) &&
		value.SlotID == binding.CISlotID && value.CandidateSHA == binding.CandidateSHA &&
		value.BaseSHA == binding.BaseSHA && value.TreeSHA == binding.TreeSHA &&
		value.ManifestSHA256 == binding.ManifestSHA256 && workflowRunPattern.MatchString(value.WorkflowRunID) &&
		value.Authoritative && value.Complete && value.ObservedAtMillis >= 0 &&
		slices.Contains([]string{"passed", "failed", "timed_out", "unavailable"}, value.Status) &&
		len(value.RequiredChecks) > 0 && len(value.RequiredChecks) <= 32 &&
		slices.IsSorted(value.RequiredChecks) && uniqueStrings(value.RequiredChecks) &&
		digestPattern.MatchString(value.SHA256) && value.SHA256 == CIObservationSHA256(value)
}

type MatrixKind string

const (
	MatrixCriterion MatrixKind = "criterion"
	MatrixDimension MatrixKind = "dimension"
)

type MatrixEntry struct {
	ID     string     `json:"id"`
	Kind   MatrixKind `json:"kind"`
	Status string     `json:"status"`
}

// FreezeMatrix joins the exact Task criterion IDs with all eight mandatory
// dimensions. Criteria are sorted; dimensions retain their normative order.
func FreezeMatrix(criterionIDs []string) ([]MatrixEntry, bool) {
	criteria := slices.Clone(criterionIDs)
	slices.Sort(criteria)
	if len(criteria) == 0 || len(criteria) > 128 || !uniqueStrings(criteria) {
		return nil, false
	}
	result := make([]MatrixEntry, 0, len(criteria)+len(dimensions))
	for _, criterion := range criteria {
		if !identifierPattern.MatchString(criterion) {
			return nil, false
		}
		result = append(result, MatrixEntry{ID: criterion, Kind: MatrixCriterion, Status: "pending"})
	}
	for _, dimension := range dimensions {
		result = append(result, MatrixEntry{ID: string(dimension), Kind: MatrixDimension, Status: "pending"})
	}
	return result, true
}

type ProbeKind string

const (
	ProbeGitInspect    ProbeKind = "git_inspect"
	ProbeSourceInspect ProbeKind = "source_inspect"
	ProbeFocusedTest   ProbeKind = "focused_test"
)

// Probe is a bounded direct-argv independent check. Complete maintained CI,
// installers, shells, and arbitrary working directories are structurally absent.
type Probe struct {
	ID             string    `json:"id"`
	Kind           ProbeKind `json:"kind"`
	Argv           []string  `json:"argv"`
	TimeoutSeconds uint32    `json:"timeoutSeconds"`
}

func ValidProbePlan(probes []Probe) bool {
	if probes == nil || len(probes) > MaximumProbes {
		return false
	}
	seen := make(map[string]struct{}, len(probes))
	for _, probe := range probes {
		if !identifierPattern.MatchString(probe.ID) || probe.TimeoutSeconds == 0 || probe.TimeoutSeconds > 300 ||
			!slices.Contains([]ProbeKind{ProbeGitInspect, ProbeSourceInspect, ProbeFocusedTest}, probe.Kind) ||
			len(probe.Argv) == 0 || len(probe.Argv) > 32 {
			return false
		}
		if _, duplicate := seen[probe.ID]; duplicate {
			return false
		}
		seen[probe.ID] = struct{}{}
		for index, argument := range probe.Argv {
			if argument == "" || len(argument) > 512 || strings.ContainsRune(argument, 0) {
				return false
			}
			if index == 0 && slices.Contains([]string{"sh", "bash", "npm", "make"}, argument) {
				return false
			}
		}
		switch probe.Kind {
		case ProbeGitInspect:
			if !validGitInspection(probe.Argv) {
				return false
			}
		case ProbeSourceInspect:
			if probe.Argv[0] != "rg" || slices.ContainsFunc(probe.Argv[1:], func(argument string) bool {
				return argument == "--pre" || strings.HasPrefix(argument, "--pre=") ||
					argument == "--hostname-bin" || strings.HasPrefix(argument, "--hostname-bin=") ||
					argument == "--replace" || strings.HasPrefix(argument, "--replace=") || argument == "-r" ||
					strings.HasPrefix(argument, "/") || strings.Contains(argument, "../")
			}) {
				return false
			}
		case ProbeFocusedTest:
			if !validFocusedTest(probe.Argv) {
				return false
			}
		}
		joined := strings.ToLower(strings.Join(probe.Argv, " "))
		if strings.Contains(joined, "npm run ci") || strings.Contains(joined, "go test ./...") ||
			strings.Contains(joined, " install") || strings.Contains(joined, " ci ") {
			return false
		}
	}
	return true
}

func validGitInspection(arguments []string) bool {
	if len(arguments) < 2 || arguments[0] != "git" {
		return false
	}
	for _, argument := range arguments[1:] {
		if strings.HasPrefix(argument, "/") || strings.Contains(argument, "../") ||
			argument == "-C" || argument == "-c" || strings.HasPrefix(argument, "--git-dir") ||
			strings.HasPrefix(argument, "--work-tree") || strings.HasPrefix(argument, "--config-env") ||
			strings.HasPrefix(argument, "--output") || argument == "--ext-diff" || argument == "--textconv" ||
			strings.Contains(argument, "pager") {
			return false
		}
	}
	readOnly := []string{"status", "diff", "show", "log", "rev-parse", "cat-file", "ls-tree", "grep", "merge-base", "name-rev"}
	if slices.Contains(readOnly, arguments[1]) {
		return true
	}
	return len(arguments) == 3 && arguments[1] == "branch" && arguments[2] == "--show-current"
}

func validFocusedTest(arguments []string) bool {
	if len(arguments) < 4 {
		return false
	}
	for _, argument := range arguments[1:] {
		if strings.HasPrefix(argument, "/") || strings.Contains(argument, "../") ||
			argument == "-exec" || strings.HasPrefix(argument, "-exec=") ||
			argument == "-toolexec" || strings.HasPrefix(argument, "-toolexec=") ||
			argument == "-overlay" || strings.HasPrefix(argument, "-overlay=") ||
			argument == "-coverprofile" || strings.HasPrefix(argument, "-coverprofile=") ||
			argument == "-o" || strings.HasPrefix(argument, "-o=") ||
			argument == "--import" || strings.HasPrefix(argument, "--import=") ||
			argument == "--require" || strings.HasPrefix(argument, "--require=") ||
			argument == "--loader" || strings.HasPrefix(argument, "--loader=") ||
			argument == "--experimental-loader" || strings.HasPrefix(argument, "--experimental-loader=") ||
			argument == "--test-reporter-destination" || strings.HasPrefix(argument, "--test-reporter-destination=") {
			return false
		}
	}
	switch arguments[0] {
	case "go":
		if arguments[1] != "test" || slices.Contains(arguments, "./...") || slices.Contains(arguments, "all") {
			return false
		}
		return slices.ContainsFunc(arguments[2:], func(argument string) bool {
			return argument == "-run" || strings.HasPrefix(argument, "-run=")
		})
	case "node":
		if arguments[1] != "--test" {
			return false
		}
		return slices.ContainsFunc(arguments[2:], func(argument string) bool {
			return argument == "--test-name-pattern" || strings.HasPrefix(argument, "--test-name-pattern=")
		})
	default:
		return false
	}
}

type SetupMode string

const (
	SetupRemoteOnly SetupMode = "remote_only"
	SetupFocused    SetupMode = "focused"
)

type DependencyState string

const (
	DependencyReady       DependencyState = "ready"
	DependencyMissing     DependencyState = "missing"
	DependencyUnavailable DependencyState = "unavailable"
)

type SetupObservation struct {
	Mode                SetupMode       `json:"mode"`
	GitAvailable        bool            `json:"gitAvailable"`
	DependencyState     DependencyState `json:"dependencyState"`
	PreparationAttempts uint32          `json:"preparationAttempts"`
	RepeatedFingerprint uint32          `json:"repeatedFingerprint"`
}

type SetupCode string

const (
	SetupReady                  SetupCode = "setup_ready"
	SetupGitMissing             SetupCode = "git_missing"
	SetupEnvironmentUnavailable SetupCode = "environment_unavailable"
	SetupThrash                 SetupCode = "setup_thrash"
)

type SetupDecision struct {
	Code             SetupCode `json:"code"`
	CandidateFailure bool      `json:"candidateFailure"`
}

// EvaluateSetup prevents harness preparation from being mislabeled as a
// Candidate defect. Remote-only review needs git and nothing else.
func EvaluateSetup(value SetupObservation) SetupDecision {
	if !value.GitAvailable {
		return SetupDecision{Code: SetupGitMissing}
	}
	if value.Mode != SetupRemoteOnly && value.Mode != SetupFocused {
		return SetupDecision{Code: SetupEnvironmentUnavailable}
	}
	if value.Mode == SetupRemoteOnly {
		if value.PreparationAttempts != 0 || value.RepeatedFingerprint > 1 {
			return SetupDecision{Code: SetupThrash}
		}
		return SetupDecision{Code: SetupReady}
	}
	if value.PreparationAttempts > 1 || value.RepeatedFingerprint > 1 {
		return SetupDecision{Code: SetupThrash}
	}
	if value.DependencyState != DependencyReady {
		return SetupDecision{Code: SetupEnvironmentUnavailable}
	}
	return SetupDecision{Code: SetupReady}
}

type Profile struct {
	Provider  string `json:"provider"`
	Model     string `json:"model"`
	SHA256    string `json:"sha256"`
	Supported bool   `json:"supported"`
}

type ProfilePolicy struct {
	RequireDifferentReviewerModel bool `json:"requireDifferentReviewerModel"`
}

type ProfileDecision struct {
	Admitted bool   `json:"admitted"`
	Code     string `json:"code"`
	Warning  string `json:"warning,omitempty"`
}

// EvaluateProfile consumes the already selected frozen rows. It never searches
// or falls back. Unsupported selected rows and an unmet distinct-model policy
// fail before Reviewer creation.
func EvaluateProfile(policy ProfilePolicy, worker, reviewer Profile) ProfileDecision {
	valid := func(value Profile) bool {
		return identifierPattern.MatchString(value.Provider) && identifierPattern.MatchString(value.Model) &&
			digestPattern.MatchString(value.SHA256) && value.Supported
	}
	if !valid(worker) || !valid(reviewer) {
		return ProfileDecision{Code: "reviewer_profile_unsupported"}
	}
	same := worker.Provider == reviewer.Provider && worker.Model == reviewer.Model
	if same && policy.RequireDifferentReviewerModel {
		return ProfileDecision{Code: "different_reviewer_model_required"}
	}
	if same {
		return ProfileDecision{Admitted: true, Code: "reviewer_profile_admitted", Warning: "same_reviewer_model"}
	}
	return ProfileDecision{Admitted: true, Code: "reviewer_profile_admitted"}
}

type CheckoutEvidence struct {
	CheckoutID      string `json:"checkoutId"`
	OwnerUUID       string `json:"ownerUuid"`
	CandidateSHA    string `json:"candidateSha"`
	TreeSHA         string `json:"treeSha"`
	Detached        bool   `json:"detached"`
	Clean           bool   `json:"clean"`
	PrimaryDistinct bool   `json:"primaryDistinct"`
	SourceUnchanged bool   `json:"sourceUnchanged"`
	FactSHA256      string `json:"factSha256"`
}

func checkoutValue(value CheckoutEvidence) CheckoutEvidence { value.FactSHA256 = ""; return value }
func CheckoutEvidenceSHA256(value CheckoutEvidence) string  { return digest(checkoutValue(value)) }
func SealCheckoutEvidence(value CheckoutEvidence) CheckoutEvidence {
	value.FactSHA256 = CheckoutEvidenceSHA256(value)
	return value
}

func ValidCheckoutEvidence(value CheckoutEvidence, binding Binding) bool {
	return identifierPattern.MatchString(value.CheckoutID) && value.OwnerUUID == binding.ReviewOwnerUUID &&
		value.CandidateSHA == binding.CandidateSHA && value.TreeSHA == binding.TreeSHA && value.Detached &&
		value.Clean && value.PrimaryDistinct && value.SourceUnchanged && digestPattern.MatchString(value.FactSHA256) &&
		value.FactSHA256 == CheckoutEvidenceSHA256(value)
}

type Finding struct {
	Code       string    `json:"code"`
	Severity   Severity  `json:"severity"`
	Dimension  Dimension `json:"dimension"`
	Summary    string    `json:"summary"`
	References []string  `json:"references"`
}

type MatrixResult struct {
	ID     string     `json:"id"`
	Kind   MatrixKind `json:"kind"`
	Status string     `json:"status"`
}

type ProbeResult struct {
	ID           string `json:"id"`
	Status       string `json:"status"`
	ExitCode     int    `json:"exitCode"`
	OutputSHA256 string `json:"outputSha256"`
}

// Claim is the only Reviewer output shape. Server-fixed bindings remain
// repeated here so evidence cannot be replayed under an old key.
type Claim struct {
	SchemaVersion       string           `json:"schemaVersion"`
	ReviewKey           string           `json:"reviewKey"`
	AttemptKey          string           `json:"attemptKey"`
	AttemptReason       AttemptReason    `json:"attemptReason"`
	Binding             Binding          `json:"binding"`
	ReviewerUUID        string           `json:"reviewerUuid"`
	HarnessReviewerUUID string           `json:"harnessReviewerUuid"`
	ParentAgentID       *string          `json:"parentAgentId"`
	Checkout            CheckoutEvidence `json:"checkout"`
	CIObservation       CIObservation    `json:"ciObservation"`
	Verdict             Verdict          `json:"verdict"`
	Matrix              []MatrixResult   `json:"matrix"`
	Findings            []Finding        `json:"findings"`
	ResidualRiskCodes   []string         `json:"residualRiskCodes"`
	HumanP2AcceptanceID string           `json:"humanP2AcceptanceId,omitempty"`
	HarnessVersion      uint32           `json:"harnessVersion"`
	Setup               SetupObservation `json:"setup"`
	Probes              []ProbeResult    `json:"probes"`
}

func ReviewKey(binding Binding) string { return "review-" + digest(binding)[:32] }

func AttemptKey(binding Binding, reason AttemptReason) string {
	return "review-attempt-" + digest(struct {
		Binding string
		Reason  AttemptReason
	}{binding.BindingSHA256, reason})[:32]
}

func validAttemptReason(value AttemptReason) bool {
	return slices.Contains([]AttemptReason{AttemptInitial, AttemptProviderPreDispatch, AttemptHarnessPreStart}, value)
}

func validMatrix(expected []MatrixEntry, actual []MatrixResult) bool {
	if len(actual) != len(expected) {
		return false
	}
	for index := range expected {
		if actual[index].ID != expected[index].ID || actual[index].Kind != expected[index].Kind ||
			!slices.Contains([]string{"covered", "finding"}, actual[index].Status) {
			return false
		}
	}
	return true
}

func validFinding(value Finding) bool {
	if !codePattern.MatchString(value.Code) || !slices.Contains([]Severity{SeverityP0, SeverityP1, SeverityP2, SeverityP3}, value.Severity) ||
		!slices.Contains(dimensions, value.Dimension) || len(value.Summary) == 0 || len(value.Summary) > 1024 ||
		len(value.References) == 0 || len(value.References) > MaximumReferences || !uniqueStrings(value.References) ||
		safedata.ContainsSecret(value.Summary) {
		return false
	}
	for _, reference := range value.References {
		if len(reference) > 256 || strings.ContainsRune(reference, 0) || strings.HasPrefix(reference, "/") ||
			strings.Contains(reference, "../") || safedata.ContainsSecret(reference) {
			return false
		}
	}
	return true
}

func validProbeResults(plan []Probe, actual []ProbeResult) bool {
	if len(actual) != len(plan) {
		return false
	}
	for index := range plan {
		if actual[index].ID != plan[index].ID || !slices.Contains([]string{"passed", "failed", "unavailable"}, actual[index].Status) ||
			!digestPattern.MatchString(actual[index].OutputSHA256) {
			return false
		}
		if actual[index].Status == "unavailable" && actual[index].ExitCode != -1 {
			return false
		}
		if actual[index].Status == "passed" && actual[index].ExitCode != 0 ||
			actual[index].Status == "failed" && actual[index].ExitCode == 0 {
			return false
		}
	}
	return true
}

func uniqueStrings(values []string) bool {
	seen := make(map[string]struct{}, len(values))
	for _, value := range values {
		if value == "" {
			return false
		}
		if _, duplicate := seen[value]; duplicate {
			return false
		}
		seen[value] = struct{}{}
	}
	return true
}

// ValidateClaim proves all exact bindings, actor separation, bounds, setup,
// acceptance coverage, and closed verdict semantics. It never infers approval.
func ValidateClaim(value Claim, binding Binding, reviewerUUID string, ci CIObservation, matrix []MatrixEntry, probePlan []Probe) bool {
	encoded, err := json.Marshal(value)
	if err != nil || len(encoded) > MaximumClaimBytes || value.SchemaVersion != ClaimSchemaVersion ||
		!ValidBinding(value.Binding) || value.Binding != binding || value.ReviewKey != ReviewKey(binding) ||
		!uuidPattern.MatchString(reviewerUUID) || value.ReviewerUUID != reviewerUUID ||
		value.HarnessReviewerUUID != reviewerUUID || value.ParentAgentID != nil ||
		reviewerUUID == binding.TaskAgentUUID || reviewerUUID == binding.CoordinatorUUID ||
		value.AttemptKey != AttemptKey(binding, value.AttemptReason) || !validAttemptReason(value.AttemptReason) ||
		!ValidCheckoutEvidence(value.Checkout, binding) || !ValidCIObservation(ci, binding) ||
		value.CIObservation.SHA256 != ci.SHA256 || !ValidCIObservation(value.CIObservation, binding) ||
		!slices.Contains([]Verdict{VerdictApproveCandidate, VerdictChangesRequested, VerdictNeedsHumanDecision}, value.Verdict) ||
		!validMatrix(matrix, value.Matrix) || len(value.Findings) > MaximumFindings ||
		!uniqueStrings(value.ResidualRiskCodes) || len(value.ResidualRiskCodes) > 32 ||
		value.HarnessVersion != 2 || EvaluateSetup(value.Setup).Code != SetupReady ||
		value.Setup.Mode == SetupRemoteOnly && slices.ContainsFunc(probePlan, func(probe Probe) bool { return probe.Kind == ProbeFocusedTest }) ||
		!validProbeResults(probePlan, value.Probes) {
		return false
	}
	blocking := false
	p2 := false
	for _, finding := range value.Findings {
		if !validFinding(finding) {
			return false
		}
		blocking = blocking || finding.Severity == SeverityP0 || finding.Severity == SeverityP1
		p2 = p2 || finding.Severity == SeverityP2
	}
	for _, code := range value.ResidualRiskCodes {
		if !codePattern.MatchString(code) {
			return false
		}
	}
	if value.Verdict == VerdictApproveCandidate && (blocking || p2 && !identifierPattern.MatchString(value.HumanP2AcceptanceID)) {
		return false
	}
	if value.Verdict == VerdictChangesRequested && len(value.Findings) == 0 {
		return false
	}
	if value.Verdict == VerdictNeedsHumanDecision && (!p2 || blocking || value.HumanP2AcceptanceID != "") {
		return false
	}
	if !p2 && value.HumanP2AcceptanceID != "" {
		return false
	}
	return true
}

type EffectPhase string

const (
	EffectIntent      EffectPhase = "intent_recorded"
	EffectDispatching EffectPhase = "dispatching"
	EffectObserved    EffectPhase = "observed"
	EffectComplete    EffectPhase = "complete"
	EffectAmbiguous   EffectPhase = "ambiguous"
)

type Effect struct {
	ID         string      `json:"id"`
	Phase      EffectPhase `json:"phase"`
	ExternalID string      `json:"externalId,omitempty"`
	FactSHA256 string      `json:"factSha256,omitempty"`
	Cursor     uint64      `json:"cursor,omitempty"`
}

type Attempt struct {
	Key    string        `json:"key"`
	Reason AttemptReason `json:"reason"`
}

type Evidence struct {
	SchemaVersion       string  `json:"schemaVersion"`
	ID                  string  `json:"id"`
	ReviewKey           string  `json:"reviewKey"`
	BindingSHA256       string  `json:"bindingSha256"`
	ReviewerUUID        string  `json:"reviewerUuid"`
	HarnessReviewerUUID string  `json:"harnessReviewerUuid"`
	CIObservationSHA256 string  `json:"ciObservationSha256"`
	CheckoutFactSHA256  string  `json:"checkoutFactSha256"`
	Verdict             Verdict `json:"verdict"`
	ClaimSHA256         string  `json:"claimSha256"`
	ObservedAtMillis    int64   `json:"observedAtMillis"`
}

// State is embedded in the Run. Disposable cleanup is never eligible until
// VerdictDurablyObserved is true; ambiguity preserves all remaining resources.
type State struct {
	SchemaVersion          string            `json:"schemaVersion"`
	ReviewKey              string            `json:"reviewKey"`
	Binding                Binding           `json:"binding"`
	SourcePath             string            `json:"sourcePath"`
	PrimaryPath            string            `json:"primaryPath"`
	ReviewerRoot           string            `json:"reviewerRoot"`
	CheckoutPath           string            `json:"checkoutPath"`
	PrimaryHeadSHA         string            `json:"primaryHeadSha"`
	Matrix                 []MatrixEntry     `json:"matrix"`
	ProbePlan              []Probe           `json:"probePlan"`
	ProfileDecision        ProfileDecision   `json:"profileDecision"`
	Attempts               []Attempt         `json:"attempts"`
	Checkout               Effect            `json:"checkout"`
	Workspace              Effect            `json:"workspace"`
	Bootstrap              Effect            `json:"bootstrap"`
	Prompt                 Effect            `json:"prompt"`
	ReviewerUUID           string            `json:"reviewerUuid,omitempty"`
	ReviewerSessionSHA256  string            `json:"reviewerSessionSha256,omitempty"`
	RegisteredAt           string            `json:"registeredAt,omitempty"`
	RegistrationSHA256     string            `json:"registrationSha256,omitempty"`
	PromptSHA256           string            `json:"promptSha256,omitempty"`
	CheckoutEvidence       *CheckoutEvidence `json:"checkoutEvidence,omitempty"`
	CIObservation          *CIObservation    `json:"ciObservation,omitempty"`
	Evidence               *Evidence         `json:"evidence,omitempty"`
	VerdictDurablyObserved bool              `json:"verdictDurablyObserved"`
	AgentCleanup           Effect            `json:"agentCleanup"`
	WorkspaceCleanup       Effect            `json:"workspaceCleanup"`
	CheckoutCleanup        Effect            `json:"checkoutCleanup"`
	Invalidated            bool              `json:"invalidated"`
	InvalidationCode       string            `json:"invalidationCode,omitempty"`
}

func effectID(binding Binding, kind string) string {
	return kind + "-" + digest(struct{ Binding, Kind string }{binding.BindingSHA256, kind})[:32]
}

func NewState(binding Binding, criterionIDs []string, probes []Probe, profile ProfileDecision) (State, bool) {
	matrix, ok := FreezeMatrix(criterionIDs)
	if !ValidBinding(binding) || !ok || !ValidProbePlan(probes) || !profile.Admitted {
		return State{}, false
	}
	state := State{
		SchemaVersion: StateSchemaVersion, ReviewKey: ReviewKey(binding), Binding: binding,
		Matrix: matrix, ProbePlan: slices.Clone(probes), ProfileDecision: profile,
		Checkout:         Effect{ID: effectID(binding, "review_checkout_create"), Phase: EffectIntent},
		Workspace:        Effect{ID: effectID(binding, "review_workspace_register"), Phase: EffectIntent},
		Bootstrap:        Effect{ID: effectID(binding, "reviewer_bootstrap"), Phase: EffectIntent},
		Prompt:           Effect{ID: effectID(binding, "review_prompt"), Phase: EffectIntent},
		AgentCleanup:     Effect{ID: effectID(binding, "reviewer_archive"), Phase: EffectIntent},
		WorkspaceCleanup: Effect{ID: effectID(binding, "review_workspace_archive"), Phase: EffectIntent},
		CheckoutCleanup:  Effect{ID: effectID(binding, "review_checkout_remove"), Phase: EffectIntent},
	}
	state.Attempts, ok = AdmitAttempt(nil, binding, AttemptInitial)
	return state, ok
}

func AdmitAttempt(attempts []Attempt, binding Binding, reason AttemptReason) ([]Attempt, bool) {
	if !ValidBinding(binding) || !validAttemptReason(reason) || len(attempts) >= 3 {
		return attempts, false
	}
	key := AttemptKey(binding, reason)
	for _, attempt := range attempts {
		if attempt.Key == key || attempt.Reason == reason {
			return attempts, false
		}
	}
	result := slices.Clone(attempts)
	result = append(result, Attempt{Key: key, Reason: reason})
	return result, true
}

// AttemptAdmitted requires the exact deterministic reason/Candidate key to
// have been recorded before a harness result can become evidence.
func AttemptAdmitted(attempts []Attempt, binding Binding, reason AttemptReason, key string) bool {
	if !ValidBinding(binding) || !validAttemptReason(reason) || key != AttemptKey(binding, reason) {
		return false
	}
	for _, attempt := range attempts {
		if attempt.Reason == reason && attempt.Key == key {
			return true
		}
	}
	return false
}

// Current returns false on any Candidate/base/tree/manifest/config/context/
// decision/finding/CI-slot drift. The old state remains immutable history.
func Current(state State, binding Binding) bool {
	return state.SchemaVersion == StateSchemaVersion && !state.Invalidated && ValidBinding(state.Binding) &&
		state.Binding == binding && state.ReviewKey == ReviewKey(binding)
}

func Invalidate(state State, code string) State {
	state.Invalidated = true
	state.InvalidationCode = code
	return state
}

func EvidenceFromClaim(claim Claim, observedAtMillis int64) Evidence {
	evidence := Evidence{
		SchemaVersion: EvidenceSchemaVersion, ReviewKey: claim.ReviewKey,
		BindingSHA256: claim.Binding.BindingSHA256, ReviewerUUID: claim.ReviewerUUID,
		HarnessReviewerUUID: claim.HarnessReviewerUUID,
		CIObservationSHA256: claim.CIObservation.SHA256, CheckoutFactSHA256: claim.Checkout.FactSHA256,
		Verdict: claim.Verdict, ClaimSHA256: digest(claim), ObservedAtMillis: observedAtMillis,
	}
	evidence.ID = "review-evidence-" + digest(evidence)[:32]
	return evidence
}

func ValidEvidence(value Evidence, state State) bool {
	return value.SchemaVersion == EvidenceSchemaVersion && identifierPattern.MatchString(value.ID) &&
		value.ReviewKey == state.ReviewKey && value.BindingSHA256 == state.Binding.BindingSHA256 &&
		value.ReviewerUUID == state.ReviewerUUID && value.HarnessReviewerUUID == state.ReviewerUUID &&
		state.CIObservation != nil && value.CIObservationSHA256 == state.CIObservation.SHA256 &&
		state.CheckoutEvidence != nil && value.CheckoutFactSHA256 == state.CheckoutEvidence.FactSHA256 &&
		slices.Contains([]Verdict{VerdictApproveCandidate, VerdictChangesRequested, VerdictNeedsHumanDecision}, value.Verdict) &&
		digestPattern.MatchString(value.ClaimSHA256) && value.ObservedAtMillis >= 0
}

func validEffect(value Effect, expectedID string) bool {
	if value.ID != expectedID || !slices.Contains([]EffectPhase{
		EffectIntent, EffectDispatching, EffectObserved, EffectComplete, EffectAmbiguous,
	}, value.Phase) {
		return false
	}
	return value.Phase != EffectIntent || value.ExternalID == "" && value.FactSHA256 == "" && value.Cursor == 0
}

func validFrozenMatrix(matrix []MatrixEntry) bool {
	if len(matrix) < len(dimensions)+1 || len(matrix) > len(dimensions)+128 {
		return false
	}
	criterionCount := len(matrix) - len(dimensions)
	criteria := make([]string, criterionCount)
	for index := range criterionCount {
		entry := matrix[index]
		if entry.Kind != MatrixCriterion || entry.Status != "pending" || !identifierPattern.MatchString(entry.ID) {
			return false
		}
		criteria[index] = entry.ID
	}
	if !slices.IsSorted(criteria) || !uniqueStrings(criteria) {
		return false
	}
	for index, dimension := range dimensions {
		entry := matrix[criterionCount+index]
		if entry != (MatrixEntry{ID: string(dimension), Kind: MatrixDimension, Status: "pending"}) {
			return false
		}
	}
	return true
}

// ValidState is the durable TaskStore boundary for the current Review. It
// rejects partial identities, reordered effects, invented attempts, and
// verdict evidence that is not tied to the exact runtime Reviewer UUID.
func ValidState(state State) bool {
	if state.SchemaVersion != StateSchemaVersion || !ValidBinding(state.Binding) ||
		state.ReviewKey != ReviewKey(state.Binding) || !validFrozenMatrix(state.Matrix) ||
		!ValidProbePlan(state.ProbePlan) || !state.ProfileDecision.Admitted ||
		state.ProfileDecision.Code != "reviewer_profile_admitted" ||
		(state.ProfileDecision.Warning != "" && state.ProfileDecision.Warning != "same_reviewer_model") ||
		!strings.HasPrefix(state.SourcePath, "/") || !strings.HasPrefix(state.PrimaryPath, "/") ||
		!strings.HasPrefix(state.ReviewerRoot, "/") || !strings.HasPrefix(state.CheckoutPath, state.ReviewerRoot+"/") ||
		state.SourcePath == state.CheckoutPath || state.PrimaryPath == state.CheckoutPath ||
		!gitOIDPattern.MatchString(state.PrimaryHeadSHA) || state.PrimaryHeadSHA != state.Binding.CandidateSHA {
		return false
	}
	effects := []struct {
		value Effect
		kind  string
	}{
		{state.Checkout, "review_checkout_create"}, {state.Workspace, "review_workspace_register"},
		{state.Bootstrap, "reviewer_bootstrap"}, {state.Prompt, "review_prompt"},
		{state.AgentCleanup, "reviewer_archive"}, {state.WorkspaceCleanup, "review_workspace_archive"},
		{state.CheckoutCleanup, "review_checkout_remove"},
	}
	for _, effect := range effects {
		if !validEffect(effect.value, effectID(state.Binding, effect.kind)) {
			return false
		}
	}
	if len(state.Attempts) == 0 || len(state.Attempts) > 3 {
		return false
	}
	seenReasons := map[AttemptReason]struct{}{}
	for _, attempt := range state.Attempts {
		if !validAttemptReason(attempt.Reason) || attempt.Key != AttemptKey(state.Binding, attempt.Reason) {
			return false
		}
		if _, duplicate := seenReasons[attempt.Reason]; duplicate {
			return false
		}
		seenReasons[attempt.Reason] = struct{}{}
	}
	if _, initial := seenReasons[AttemptInitial]; !initial {
		return false
	}
	if state.Workspace.Phase != EffectIntent && state.Checkout.Phase != EffectComplete ||
		state.Bootstrap.Phase != EffectIntent && state.Workspace.Phase != EffectComplete ||
		state.Prompt.Phase != EffectIntent && (state.Bootstrap.Phase != EffectComplete || state.CIObservation == nil) ||
		state.WorkspaceCleanup.Phase != EffectIntent && state.AgentCleanup.Phase != EffectComplete ||
		state.CheckoutCleanup.Phase != EffectIntent && (state.AgentCleanup.Phase != EffectComplete || state.WorkspaceCleanup.Phase != EffectComplete) {
		return false
	}
	if state.CheckoutEvidence != nil && !ValidCheckoutEvidence(*state.CheckoutEvidence, state.Binding) {
		return false
	}
	if state.Checkout.Phase == EffectComplete && state.CheckoutEvidence == nil {
		return false
	}
	if state.Checkout.Phase == EffectComplete && (state.Checkout.ExternalID != state.ReviewKey ||
		!digestPattern.MatchString(state.Checkout.FactSHA256)) {
		return false
	}
	if state.Workspace.Phase == EffectComplete && (!ValidIdentifier(state.Workspace.ExternalID) ||
		!digestPattern.MatchString(state.Workspace.FactSHA256)) {
		return false
	}
	if state.CIObservation != nil && !ValidCIObservation(*state.CIObservation, state.Binding) {
		return false
	}
	if state.Bootstrap.Phase == EffectComplete {
		if !ValidUUID(state.ReviewerUUID) || state.ReviewerUUID == state.Binding.TaskAgentUUID ||
			state.ReviewerUUID == state.Binding.CoordinatorUUID || !digestPattern.MatchString(state.ReviewerSessionSHA256) ||
			state.RegisteredAt == "" || !digestPattern.MatchString(state.RegistrationSHA256) ||
			state.Bootstrap.ExternalID != state.ReviewerUUID || !digestPattern.MatchString(state.Bootstrap.FactSHA256) {
			return false
		}
	}
	if (state.Prompt.Phase == EffectDispatching || state.Prompt.Phase == EffectComplete) && !digestPattern.MatchString(state.PromptSHA256) {
		return false
	}
	if state.Prompt.Phase == EffectComplete && (state.Prompt.ExternalID != state.ReviewerUUID ||
		!digestPattern.MatchString(state.Prompt.FactSHA256)) {
		return false
	}
	if state.Evidence != nil && (!ValidEvidence(*state.Evidence, state) || state.Prompt.Phase != EffectComplete) {
		return false
	}
	if state.VerdictDurablyObserved != (state.Evidence != nil) {
		return false
	}
	cleanupStarted := state.AgentCleanup.Phase != EffectIntent || state.WorkspaceCleanup.Phase != EffectIntent ||
		state.CheckoutCleanup.Phase != EffectIntent
	if cleanupStarted && !state.VerdictDurablyObserved {
		return false
	}
	if state.Invalidated {
		return identifierPattern.MatchString(state.InvalidationCode)
	}
	return state.InvalidationCode == ""
}

// CleanupEligible is the sole model-independent gate for reviewer resource
// cleanup. The caller must additionally re-observe exact ownership/identity.
func CleanupEligible(state State, coordinatorUUID string) bool {
	return Current(state, state.Binding) && state.VerdictDurablyObserved && state.Evidence != nil &&
		uuidPattern.MatchString(coordinatorUUID) && coordinatorUUID == state.Binding.CoordinatorUUID &&
		ValidEvidence(*state.Evidence, state)
}
