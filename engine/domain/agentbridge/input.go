// SPDX-License-Identifier: Apache-2.0

package agentbridge

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/mcuadros/director-engine/domain/jsondocument"
	"github.com/mcuadros/director-engine/domain/safedata"
)

var (
	commandTokenPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.:@/-]{0,127}$`)
	helperCommitPattern = regexp.MustCompile(`^[0-9a-f]{40}$`)
)

// InputCode is a closed caller-input rejection. It contains no rejected text.
type InputCode string

const (
	InputInvalid     InputCode = "MCP_INPUT_INVALID"
	InputTooLarge    InputCode = "MCP_INPUT_TOO_LARGE"
	InputSecret      InputCode = "MCP_SECRET_SHAPED_VALUE"
	InputPrivatePath InputCode = "MCP_PRIVATE_PATH_REJECTED"
)

// InputError exposes only a bounded code.
type InputError struct{ Code InputCode }

func (failure *InputError) Error() string { return string(failure.Code) }

func boundedText(value string, maximum int, allowEmpty bool) bool {
	if !utf8.ValidString(value) || value != strings.TrimSpace(value) || len(value) > maximum ||
		strings.IndexFunc(value, unicode.IsControl) >= 0 {
		return false
	}
	return allowEmpty || value != ""
}

func uniqueBounded(values []string, maximumCount, maximumLength int, minimumCount int) bool {
	if values == nil || len(values) < minimumCount || len(values) > maximumCount {
		return false
	}
	seen := make(map[string]struct{}, len(values))
	for _, value := range values {
		if !boundedText(value, maximumLength, false) {
			return false
		}
		if _, duplicate := seen[value]; duplicate {
			return false
		}
		seen[value] = struct{}{}
	}
	return true
}

func inspectSafeValue(value any) InputCode {
	switch safedata.ClassifyValue(value, safedata.ScanRules{RejectPrivatePaths: true, RejectSensitiveKeys: true}) {
	case safedata.Secret:
		return InputSecret
	case safedata.PrivatePath:
		return InputPrivatePath
	case safedata.Invalid:
		return InputInvalid
	}
	return ""
}

// RedactedOutputText bounds one projection field and replaces any private
// path, secret-shaped value, invalid Unicode, or control-bearing text. It is
// safe for user-facing MCP projections and never returns the rejected bytes.
func RedactedOutputText(value string, maximumBytes int) string {
	if maximumBytes < 1 || !utf8.ValidString(value) || strings.IndexFunc(value, unicode.IsControl) >= 0 ||
		inspectSafeValue(value) != "" {
		return "[REDACTED]"
	}
	if len(value) <= maximumBytes {
		return value
	}
	truncated := []rune(value)
	for len(truncated) > 0 && len(string(truncated))+len("…") > maximumBytes {
		truncated = truncated[:len(truncated)-1]
	}
	return string(truncated) + "…"
}

func canonicalInput(raw json.RawMessage) (json.RawMessage, error) {
	if len(raw) == 0 || len(raw) > MaximumRequestBytes {
		return nil, &InputError{Code: InputTooLarge}
	}
	canonical, err := jsondocument.CanonicalWithNormalizedNumbersLimit(raw, MaximumRequestBytes)
	if err != nil {
		if errors.Is(err, jsondocument.ErrCanonicalDocumentTooLarge) {
			return nil, &InputError{Code: InputTooLarge}
		}
		return nil, &InputError{Code: InputInvalid}
	}
	decoder := json.NewDecoder(bytes.NewReader(canonical))
	decoder.UseNumber()
	var value any
	if err := decoder.Decode(&value); err != nil {
		return nil, &InputError{Code: InputInvalid}
	}
	if code := inspectSafeValue(value); code != "" {
		return nil, &InputError{Code: code}
	}
	return canonical, nil
}

func strictDecode[T any](raw []byte, destination *T) error {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	decoder.UseNumber()
	if err := decoder.Decode(destination); err != nil {
		return &InputError{Code: InputInvalid}
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return &InputError{Code: InputInvalid}
	}
	return nil
}

func validateEmpty(raw []byte) error {
	var value map[string]json.RawMessage
	if err := strictDecode(raw, &value); err != nil || value == nil || len(value) != 0 {
		return &InputError{Code: InputInvalid}
	}
	return nil
}

type planningCommand struct {
	Kind               string   `json:"kind"`
	Title              string   `json:"title"`
	Objective          string   `json:"objective"`
	AcceptanceCriteria []string `json:"acceptanceCriteria"`
	Priority           string   `json:"priority"`
	Labels             []string `json:"labels"`
}

func validatePlanningCommand(raw []byte) error {
	var command planningCommand
	if err := strictDecode(raw, &command); err != nil {
		return err
	}
	if command.Kind != "task_update_proposal" || !boundedText(command.Title, 512, false) ||
		!boundedText(command.Objective, 4096, false) ||
		!uniqueBounded(command.AcceptanceCriteria, 128, 2048, 1) ||
		!slices.Contains([]string{"urgent", "high", "normal", "low"}, command.Priority) ||
		!uniqueBounded(command.Labels, 64, 128, 0) {
		return &InputError{Code: InputInvalid}
	}
	return nil
}

type completedClaim struct {
	Outcome           string            `json:"outcome"`
	CandidateSHA      string            `json:"candidateSha"`
	BaseSHA           string            `json:"baseSha"`
	CriteriaResults   map[string]string `json:"criteriaResults"`
	ResidualRiskCodes []string          `json:"residualRiskCodes"`
}

type candidateChecksClaim struct {
	Outcome      string   `json:"outcome"`
	CandidateSHA string   `json:"candidateSha"`
	BaseSHA      string   `json:"baseSha"`
	CheckIDs     []string `json:"checkIds"`
}

type candidateCriteriaClaim struct {
	Outcome      string   `json:"outcome"`
	CandidateSHA string   `json:"candidateSha"`
	BaseSHA      string   `json:"baseSha"`
	CriterionIDs []string `json:"criterionIds"`
}

type humanDecisionClaim struct {
	Outcome         string   `json:"outcome"`
	QuestionCode    string   `json:"questionCode"`
	Question        string   `json:"question"`
	Options         []string `json:"options"`
	AffectedScope   string   `json:"affectedScope"`
	ResumeCondition string   `json:"resumeCondition"`
}

type dependencyClaim struct {
	Outcome       string   `json:"outcome"`
	DependencyIDs []string `json:"dependencyIds"`
	WakePredicate string   `json:"wakePredicate"`
}

type accessClaim struct {
	Outcome            string  `json:"outcome"`
	CapabilityCode     *string `json:"capabilityCode"`
	ResourceCode       *string `json:"resourceCode"`
	OperationCode      string  `json:"operationCode"`
	FailureFingerprint string  `json:"failureFingerprint"`
}

type budgetClaim struct {
	Outcome         string      `json:"outcome"`
	BudgetDimension string      `json:"budgetDimension"`
	ObservedAmount  json.Number `json:"observedAmount"`
	Unit            string      `json:"unit"`
}

type helperRequest struct {
	Mode    string `json:"mode"`
	Purpose string `json:"purpose"`
}

func validateHelperRequest(raw []byte) error {
	var request helperRequest
	if err := strictDecode(raw, &request); err != nil ||
		!slices.Contains([]string{"writer", "read_only"}, request.Mode) ||
		!boundedText(request.Purpose, 2048, false) {
		return &InputError{Code: InputInvalid}
	}
	return nil
}

type helperContribution struct {
	CommitSHA string `json:"commitSha"`
	BaseSHA   string `json:"baseSha"`
}

func validateHelperContribution(raw []byte, baseSHA string) error {
	var contribution helperContribution
	if err := strictDecode(raw, &contribution); err != nil ||
		!helperCommitPattern.MatchString(contribution.CommitSHA) ||
		contribution.BaseSHA != baseSHA {
		return &InputError{Code: InputInvalid}
	}
	return nil
}

func exactCriterionSet(values []string, expected []string) bool {
	if len(values) != len(expected) {
		return false
	}
	set := make(map[string]struct{}, len(expected))
	for _, value := range expected {
		set[value] = struct{}{}
	}
	for _, value := range values {
		if _, present := set[value]; !present {
			return false
		}
		delete(set, value)
	}
	return len(set) == 0
}

func validateTaskOutcome(raw []byte, criteria []string, baseSHA string) error {
	var header struct {
		Outcome string `json:"outcome"`
	}
	if err := json.Unmarshal(raw, &header); err != nil {
		return &InputError{Code: InputInvalid}
	}
	switch header.Outcome {
	case "completed":
		var claim completedClaim
		if err := strictDecode(raw, &claim); err != nil || !gitOIDPattern.MatchString(claim.CandidateSHA) ||
			claim.BaseSHA != baseSHA || len(claim.CriteriaResults) != len(criteria) ||
			!uniqueBounded(claim.ResidualRiskCodes, 32, 64, 0) {
			return &InputError{Code: InputInvalid}
		}
		ids := make([]string, 0, len(claim.CriteriaResults))
		for id, result := range claim.CriteriaResults {
			if !commandTokenPattern.MatchString(id) || (result != "claimed_satisfied" && result != "claimed_unsatisfied") {
				return &InputError{Code: InputInvalid}
			}
			ids = append(ids, id)
		}
		if !exactCriterionSet(ids, criteria) {
			return &InputError{Code: InputInvalid}
		}
	case "needs_validation":
		var claim candidateChecksClaim
		if err := strictDecode(raw, &claim); err != nil || !gitOIDPattern.MatchString(claim.CandidateSHA) ||
			claim.BaseSHA != baseSHA || !uniqueBounded(claim.CheckIDs, 128, 128, 1) {
			return &InputError{Code: InputInvalid}
		}
	case "needs_review":
		var claim candidateCriteriaClaim
		if err := strictDecode(raw, &claim); err != nil || !gitOIDPattern.MatchString(claim.CandidateSHA) ||
			claim.BaseSHA != baseSHA || !uniqueBounded(claim.CriterionIDs, 128, 128, 1) ||
			!exactCriterionSet(claim.CriterionIDs, criteria) {
			return &InputError{Code: InputInvalid}
		}
	case "needs_human_decision":
		var claim humanDecisionClaim
		if err := strictDecode(raw, &claim); err != nil || !commandTokenPattern.MatchString(claim.QuestionCode) ||
			!boundedText(claim.Question, 2048, false) || !uniqueBounded(claim.Options, 8, 256, 2) ||
			!slices.Contains([]string{"task", "run", "candidate"}, claim.AffectedScope) ||
			!boundedText(claim.ResumeCondition, 512, false) {
			return &InputError{Code: InputInvalid}
		}
	case "blocked_by_dependency":
		var claim dependencyClaim
		if err := strictDecode(raw, &claim); err != nil ||
			!uniqueBounded(claim.DependencyIDs, 128, 128, 1) || !boundedText(claim.WakePredicate, 512, false) {
			return &InputError{Code: InputInvalid}
		}
	case "blocked_by_access":
		var claim accessClaim
		if err := strictDecode(raw, &claim); err != nil || (claim.CapabilityCode == nil) == (claim.ResourceCode == nil) ||
			!commandTokenPattern.MatchString(claim.OperationCode) || !boundedText(claim.FailureFingerprint, 256, false) {
			return &InputError{Code: InputInvalid}
		}
		if (claim.CapabilityCode != nil && !commandTokenPattern.MatchString(*claim.CapabilityCode)) ||
			(claim.ResourceCode != nil && !commandTokenPattern.MatchString(*claim.ResourceCode)) {
			return &InputError{Code: InputInvalid}
		}
	case "budget_exhausted":
		var claim budgetClaim
		if err := strictDecode(raw, &claim); err != nil ||
			!slices.Contains([]string{"time", "cost", "tokens", "turns"}, claim.BudgetDimension) ||
			!boundedText(claim.Unit, 32, false) {
			return &InputError{Code: InputInvalid}
		}
		amount, err := strconv.ParseFloat(string(claim.ObservedAmount), 64)
		if err != nil || amount < 0 {
			return &InputError{Code: InputInvalid}
		}
	default:
		return &InputError{Code: InputInvalid}
	}
	return nil
}

var reviewDimensions = []string{
	"acceptance", "correctness", "security", "maintainability",
	"readability", "design", "quality", "rigor",
}

type reviewFinding struct {
	Code       string   `json:"code"`
	Severity   string   `json:"severity"`
	Dimension  string   `json:"dimension"`
	Summary    string   `json:"summary"`
	References []string `json:"references"`
}

type reviewClaim struct {
	Verdict            string          `json:"verdict"`
	AcceptanceCriteria []string        `json:"acceptanceCriteria"`
	Coverage           []string        `json:"coverage"`
	Findings           []reviewFinding `json:"findings"`
	ResidualRiskCodes  []string        `json:"residualRiskCodes"`
}

func validateReview(raw []byte, criteria []string) error {
	var claim reviewClaim
	if err := strictDecode(raw, &claim); err != nil ||
		!slices.Contains([]string{"approve_candidate", "changes_requested", "needs_human_decision"}, claim.Verdict) ||
		!uniqueBounded(claim.AcceptanceCriteria, 128, 256, 1) || !exactCriterionSet(claim.AcceptanceCriteria, criteria) ||
		!uniqueBounded(claim.Coverage, 8, 32, 8) || !exactCriterionSet(claim.Coverage, reviewDimensions) ||
		len(claim.Findings) > 64 || !uniqueBounded(claim.ResidualRiskCodes, 32, 64, 0) {
		return &InputError{Code: InputInvalid}
	}
	blocking := false
	p2 := false
	for _, finding := range claim.Findings {
		if !commandTokenPattern.MatchString(finding.Code) || finding.Code != strings.ToUpper(finding.Code) ||
			!slices.Contains([]string{"P0", "P1", "P2", "P3"}, finding.Severity) ||
			!slices.Contains(reviewDimensions, finding.Dimension) || !boundedText(finding.Summary, 1024, false) ||
			!uniqueBounded(finding.References, 16, 256, 1) {
			return &InputError{Code: InputInvalid}
		}
		if claim.Verdict == "approve_candidate" && (finding.Severity == "P0" || finding.Severity == "P1") {
			return &InputError{Code: InputInvalid}
		}
		blocking = blocking || finding.Severity == "P0" || finding.Severity == "P1"
		p2 = p2 || finding.Severity == "P2"
	}
	if claim.Verdict == "changes_requested" && len(claim.Findings) == 0 {
		return &InputError{Code: InputInvalid}
	}
	if claim.Verdict == "needs_human_decision" && (!p2 || blocking) {
		return &InputError{Code: InputInvalid}
	}
	for _, code := range claim.ResidualRiskCodes {
		if !commandTokenPattern.MatchString(code) || code != strings.ToUpper(code) {
			return &InputError{Code: InputInvalid}
		}
	}
	return nil
}

// ValidateToolInput rejects oversize, duplicate-key, unknown-field,
// secret-shaped, private-path, and semantically out-of-scope payloads and
// returns the exact canonical bytes persisted by the application service.
func ValidateToolInput(toolName string, raw json.RawMessage, criteria []string, baseSHA string) (json.RawMessage, error) {
	canonical, err := canonicalInput(raw)
	if err != nil {
		return nil, err
	}
	switch toolName {
	case "director_project_read", "director_task_read", "director_candidate_read":
		err = validateEmpty(canonical)
	case "director_planning_command_submit":
		err = validatePlanningCommand(canonical)
	case "director_task_outcome_submit":
		err = validateTaskOutcome(canonical, criteria, baseSHA)
	case "director_task_helper_request":
		err = validateHelperRequest(canonical)
	case "director_helper_contribution_submit":
		err = validateHelperContribution(canonical, baseSHA)
	case "director_review_verdict_submit":
		err = validateReview(canonical, criteria)
	default:
		err = &InputError{Code: InputInvalid}
	}
	if err != nil {
		return nil, err
	}
	return canonical, nil
}
