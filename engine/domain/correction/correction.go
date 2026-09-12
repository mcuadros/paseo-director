// SPDX-License-Identifier: Apache-2.0

// Package correction owns the bounded, deterministic correction-cycle
// contract. It contains no host, Git, TaskStore, or delivery behavior.
package correction

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"regexp"
	"slices"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/mcuadros/director-engine/domain/candidate"
	"github.com/mcuadros/director-engine/domain/safedata"
)

const (
	BatchSchemaVersion  = "director.correction-batch/v1"
	OutputSchemaVersion = "director.correction-output/v1"
	StateSchemaVersion  = "director.correction-state/v1"
	AttemptLimit        = uint32(3)
	MaximumFindings     = 256
	MaximumEvidence     = 8
	MaximumCoverage     = 128
	MaximumBatches      = AttemptLimit + 1
)

var (
	identifierPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.:@/-]{0,255}$`)
	classPattern      = regexp.MustCompile(`^[A-Z][A-Z0-9_]{0,63}$`)
	digestPattern     = regexp.MustCompile(`^[0-9a-f]{64}$`)
	gitOIDPattern     = regexp.MustCompile(`^(?:[0-9a-f]{40}|[0-9a-f]{64})$`)
	uuidPattern       = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[1-8][0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`)
)

type Source string

const (
	SourceReview     Source = "review"
	SourceValidation Source = "validation"
	SourceCI         Source = "ci"
	SourceHuman      Source = "human_feedback"
)

var sources = []Source{SourceReview, SourceValidation, SourceCI, SourceHuman}

type Severity string

const (
	SeverityP0 Severity = "P0"
	SeverityP1 Severity = "P1"
	SeverityP2 Severity = "P2"
	SeverityP3 Severity = "P3"
)

type EvidenceKind string

const (
	EvidenceReviewFinding         EvidenceKind = "review_finding"
	EvidenceValidationObservation EvidenceKind = "validation_observation"
	EvidenceCIObservation         EvidenceKind = "ci_observation"
	EvidenceHumanFeedback         EvidenceKind = "human_feedback"
	EvidenceFailureInterpretation EvidenceKind = "failure_interpretation"
)

var evidenceKinds = []EvidenceKind{
	EvidenceReviewFinding, EvidenceValidationObservation, EvidenceCIObservation,
	EvidenceHumanFeedback, EvidenceFailureInterpretation,
}

// Evidence is deliberately reference-only. Raw logs, histories, paths, and
// model transcripts have no representation in a correction batch.
type Evidence struct {
	ID     string       `json:"id"`
	Kind   EvidenceKind `json:"kind"`
	SHA256 string       `json:"sha256"`
}

// FindingInput is one current, already-normalized source observation. The
// engine canonicalizes it and computes Fingerprint; callers cannot supply one.
type FindingInput struct {
	ID                    string     `json:"id"`
	Source                Source     `json:"source"`
	Class                 string     `json:"class"`
	Severity              Severity   `json:"severity"`
	CandidateID           string     `json:"candidateId"`
	CandidateSHA          string     `json:"candidateSha"`
	Summary               string     `json:"summary"`
	Evidence              []Evidence `json:"evidence"`
	AcceptanceCoverage    []string   `json:"acceptanceCoverage"`
	Blocking              bool       `json:"blocking"`
	RequiresHumanDecision bool       `json:"requiresHumanDecision"`
}

type Finding struct {
	FindingInput
	Fingerprint string `json:"fingerprint"`
}

// SourceSnapshot proves all four source classes participated in one batch,
// including a source with zero current findings. Revision is a bounded digest
// of the upstream durable rows, not raw source output.
type SourceSnapshot struct {
	Source   Source `json:"source"`
	Revision string `json:"revision"`
	Count    uint32 `json:"count"`
}

// Batch is the sorted unique whole-batch correction input. The individual
// digests let routing distinguish changed findings, root classes, Candidate,
// and acceptance coverage without inspecting prose.
type Batch struct {
	SchemaVersion         string           `json:"schemaVersion"`
	CandidateID           string           `json:"candidateId"`
	CandidateSHA          string           `json:"candidateSha"`
	Sources               []SourceSnapshot `json:"sources"`
	Findings              []Finding        `json:"findings"`
	FindingFingerprint    string           `json:"findingFingerprint"`
	ClassFingerprint      string           `json:"classFingerprint"`
	CandidateFingerprint  string           `json:"candidateFingerprint"`
	CoverageFingerprint   string           `json:"coverageFingerprint"`
	AcceptanceCoverage    []string         `json:"acceptanceCoverage"`
	BlockingClasses       []string         `json:"blockingClasses"`
	RequiresHumanDecision bool             `json:"requiresHumanDecision"`
	SHA256                string           `json:"sha256"`
}

func digest(value any) string {
	encoded, err := json.Marshal(value)
	if err != nil {
		panic("marshal fixed correction value: " + err.Error())
	}
	sum := sha256.Sum256(encoded)
	return hex.EncodeToString(sum[:])
}

func validText(value string, maximum int) bool {
	return value != "" && len(value) <= maximum && utf8.ValidString(value) && value == strings.TrimSpace(value) &&
		strings.IndexFunc(value, unicode.IsControl) < 0 && !safedata.ContainsSecret(value) &&
		!strings.HasPrefix(value, "/") && !strings.Contains(value, "../")
}

func sourceRank(source Source) int { return slices.Index(sources, source) }

func severityRank(severity Severity) int {
	return slices.Index([]Severity{SeverityP0, SeverityP1, SeverityP2, SeverityP3}, severity)
}

func canonicalStrings(values []string, maximum int) ([]string, bool) {
	if values == nil || len(values) > maximum {
		return nil, false
	}
	result := slices.Clone(values)
	slices.Sort(result)
	for index, value := range result {
		if !identifierPattern.MatchString(value) || index > 0 && result[index-1] == value {
			return nil, false
		}
	}
	return result, true
}

func canonicalEvidence(values []Evidence) ([]Evidence, bool) {
	if len(values) == 0 || len(values) > MaximumEvidence {
		return nil, false
	}
	result := slices.Clone(values)
	slices.SortFunc(result, func(left, right Evidence) int {
		if byKind := strings.Compare(string(left.Kind), string(right.Kind)); byKind != 0 {
			return byKind
		}
		return strings.Compare(left.ID, right.ID)
	})
	for index, value := range result {
		if !identifierPattern.MatchString(value.ID) || !slices.Contains(evidenceKinds, value.Kind) ||
			!digestPattern.MatchString(value.SHA256) || index > 0 && result[index-1].Kind == value.Kind && result[index-1].ID == value.ID {
			return nil, false
		}
	}
	return result, true
}

func canonicalFinding(input FindingInput, candidateID, candidateSHA string, criteria map[string]struct{}) (Finding, bool) {
	if !identifierPattern.MatchString(input.ID) || sourceRank(input.Source) < 0 || !classPattern.MatchString(input.Class) ||
		severityRank(input.Severity) < 0 || input.CandidateID != candidateID || input.CandidateSHA != candidateSHA ||
		!validText(input.Summary, 512) || (input.Severity == SeverityP0 || input.Severity == SeverityP1) && !input.Blocking ||
		input.RequiresHumanDecision && input.Severity != SeverityP2 && input.Source != SourceHuman {
		return Finding{}, false
	}
	evidence, ok := canonicalEvidence(input.Evidence)
	if !ok {
		return Finding{}, false
	}
	coverage, ok := canonicalStrings(input.AcceptanceCoverage, MaximumCoverage)
	if !ok {
		return Finding{}, false
	}
	for _, criterion := range coverage {
		if _, exists := criteria[criterion]; !exists {
			return Finding{}, false
		}
	}
	input.Evidence = evidence
	input.AcceptanceCoverage = coverage
	finding := Finding{FindingInput: input}
	finding.Fingerprint = digest(input)
	return finding, true
}

func batchValue(value Batch) Batch { value.SHA256 = ""; return value }

// Canonicalize builds one deterministic whole batch. Exact duplicate source
// rows collapse; identity reuse with different content fails closed.
func Canonicalize(candidateID, candidateSHA string, criterionIDs []string, snapshots []SourceSnapshot, inputs []FindingInput) (Batch, bool) {
	if !identifierPattern.MatchString(candidateID) || !gitOIDPattern.MatchString(candidateSHA) ||
		len(inputs) == 0 || len(inputs) > MaximumFindings {
		return Batch{}, false
	}
	criteria, ok := canonicalStrings(criterionIDs, MaximumCoverage)
	if !ok || len(criteria) == 0 {
		return Batch{}, false
	}
	criterionSet := make(map[string]struct{}, len(criteria))
	for _, criterion := range criteria {
		criterionSet[criterion] = struct{}{}
	}
	if len(snapshots) != len(sources) {
		return Batch{}, false
	}
	snapshotBySource := make(map[Source]SourceSnapshot, len(snapshots))
	for _, snapshot := range snapshots {
		if sourceRank(snapshot.Source) < 0 || !digestPattern.MatchString(snapshot.Revision) {
			return Batch{}, false
		}
		if _, duplicate := snapshotBySource[snapshot.Source]; duplicate {
			return Batch{}, false
		}
		snapshotBySource[snapshot.Source] = snapshot
	}
	canonicalSnapshots := make([]SourceSnapshot, 0, len(sources))
	for _, source := range sources {
		snapshot, exists := snapshotBySource[source]
		if !exists {
			return Batch{}, false
		}
		canonicalSnapshots = append(canonicalSnapshots, snapshot)
	}
	byIdentity := make(map[string]Finding, len(inputs))
	for _, input := range inputs {
		finding, valid := canonicalFinding(input, candidateID, candidateSHA, criterionSet)
		if !valid {
			return Batch{}, false
		}
		key := string(finding.Source) + "\x1f" + finding.ID
		if prior, duplicate := byIdentity[key]; duplicate {
			if prior.Fingerprint != finding.Fingerprint {
				return Batch{}, false
			}
			continue
		}
		byIdentity[key] = finding
	}
	findings := make([]Finding, 0, len(byIdentity))
	counts := make(map[Source]uint32, len(sources))
	coverageSet := map[string]struct{}{}
	blockingSet := map[string]struct{}{}
	requiresHuman := false
	for _, finding := range byIdentity {
		findings = append(findings, finding)
		counts[finding.Source]++
		for _, criterion := range finding.AcceptanceCoverage {
			coverageSet[criterion] = struct{}{}
		}
		if finding.Blocking {
			blockingSet[string(finding.Source)+":"+finding.Class] = struct{}{}
		}
		requiresHuman = requiresHuman || finding.RequiresHumanDecision
	}
	for _, snapshot := range canonicalSnapshots {
		if snapshot.Count != counts[snapshot.Source] {
			return Batch{}, false
		}
	}
	slices.SortFunc(findings, func(left, right Finding) int {
		if left.Source != right.Source {
			return sourceRank(left.Source) - sourceRank(right.Source)
		}
		if left.Severity != right.Severity {
			return severityRank(left.Severity) - severityRank(right.Severity)
		}
		if byClass := strings.Compare(left.Class, right.Class); byClass != 0 {
			return byClass
		}
		return strings.Compare(left.ID, right.ID)
	})
	coverage := make([]string, 0, len(coverageSet))
	for criterion := range coverageSet {
		coverage = append(coverage, criterion)
	}
	slices.Sort(coverage)
	blocking := make([]string, 0, len(blockingSet))
	for class := range blockingSet {
		blocking = append(blocking, class)
	}
	slices.Sort(blocking)
	fingerprints := make([]string, len(findings))
	for index := range findings {
		fingerprints[index] = findings[index].Fingerprint
	}
	batch := Batch{
		SchemaVersion: BatchSchemaVersion, CandidateID: candidateID, CandidateSHA: candidateSHA,
		Sources: canonicalSnapshots, Findings: findings, FindingFingerprint: digest(fingerprints),
		ClassFingerprint: digest(blocking), CandidateFingerprint: digest(struct{ ID, SHA string }{candidateID, candidateSHA}),
		CoverageFingerprint: digest(coverage), AcceptanceCoverage: coverage, BlockingClasses: blocking,
		RequiresHumanDecision: requiresHuman,
	}
	batch.SHA256 = digest(batchValue(batch))
	return batch, true
}

func ValidBatch(value Batch, criterionIDs []string) bool {
	inputs := make([]FindingInput, len(value.Findings))
	for index := range value.Findings {
		inputs[index] = value.Findings[index].FindingInput
	}
	canonical, ok := Canonicalize(value.CandidateID, value.CandidateSHA, criterionIDs, value.Sources, inputs)
	return ok && canonical.SHA256 == value.SHA256 && canonical.FindingFingerprint == value.FindingFingerprint &&
		canonical.ClassFingerprint == value.ClassFingerprint && canonical.CandidateFingerprint == value.CandidateFingerprint &&
		canonical.CoverageFingerprint == value.CoverageFingerprint && slices.Equal(canonical.AcceptanceCoverage, value.AcceptanceCoverage) &&
		slices.Equal(canonical.BlockingClasses, value.BlockingClasses) && slices.EqualFunc(canonical.Findings, value.Findings, func(left, right Finding) bool {
		return left.Fingerprint == right.Fingerprint
	})
}

func FindingIDs(batch Batch) []string {
	result := make([]string, len(batch.Findings))
	for index, finding := range batch.Findings {
		result[index] = string(finding.Source) + ":" + finding.ID
	}
	return result
}

type CoverageDelta struct {
	Added   []string `json:"added"`
	Removed []string `json:"removed"`
}

func Delta(previous, current []string) CoverageDelta {
	left := make(map[string]struct{}, len(previous))
	right := make(map[string]struct{}, len(current))
	for _, value := range previous {
		left[value] = struct{}{}
	}
	for _, value := range current {
		right[value] = struct{}{}
	}
	result := CoverageDelta{Added: []string{}, Removed: []string{}}
	for value := range right {
		if _, exists := left[value]; !exists {
			result.Added = append(result.Added, value)
		}
	}
	for value := range left {
		if _, exists := right[value]; !exists {
			result.Removed = append(result.Removed, value)
		}
	}
	slices.Sort(result.Added)
	slices.Sort(result.Removed)
	return result
}

type Classification string

const (
	ClassificationProductive Classification = "productive"
	ClassificationChurn      Classification = "churn"
)

type AttemptReason string

const (
	ReasonInitial                 AttemptReason = "initial"
	ReasonFindingsChanged         AttemptReason = "findings_changed"
	ReasonAcknowledgementRejected AttemptReason = "acknowledgement_rejected"
)

type EffectPhase string

const (
	EffectIntent      EffectPhase = "intent_recorded"
	EffectDispatching EffectPhase = "dispatching"
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

type Output struct {
	SchemaVersion       string   `json:"schemaVersion"`
	AttemptKey          string   `json:"attemptKey"`
	AgentUUID           string   `json:"agentUuid"`
	BatchSHA256         string   `json:"batchSha256"`
	BatchedFindingIDs   []string `json:"batchedFindingIds"`
	CandidateSHA        string   `json:"candidateSha,omitempty"`
	AcknowledgementOnly bool     `json:"acknowledgementOnly"`
	ResolvedFindingIDs  []string `json:"resolvedFindingIds"`
	AcceptanceCoverage  []string `json:"acceptanceCoverage"`
}

type Attempt struct {
	Number              uint32         `json:"number"`
	Key                 string         `json:"key"`
	Reason              AttemptReason  `json:"reason"`
	BatchSHA256         string         `json:"batchSha256"`
	ClassFingerprint    string         `json:"classFingerprint"`
	CandidateBeforeID   string         `json:"candidateBeforeId"`
	CandidateBeforeSHA  string         `json:"candidateBeforeSha"`
	Prompt              Effect         `json:"prompt"`
	PromptSHA256        string         `json:"promptSha256,omitempty"`
	BudgetReservationID string         `json:"budgetReservationId"`
	BudgetEvidenceID    string         `json:"budgetEvidenceId,omitempty"`
	Output              *Output        `json:"output,omitempty"`
	CoverageDelta       CoverageDelta  `json:"coverageDelta"`
	Classification      Classification `json:"classification,omitempty"`
	ResultCandidateID   string         `json:"resultCandidateId,omitempty"`
	ResultCandidateSHA  string         `json:"resultCandidateSha,omitempty"`
}

type GatePlan struct {
	CandidateID               string `json:"candidateId"`
	CandidateSHA              string `json:"candidateSha"`
	CandidateGeneration       uint64 `json:"candidateGeneration"`
	CIKey                     string `json:"ciKey"`
	ReviewBindingKey          string `json:"reviewBindingKey"`
	FreshCIRequired           bool   `json:"freshCiRequired"`
	FreshReviewRequired       bool   `json:"freshReviewRequired"`
	PriorAuthorityInvalidated bool   `json:"priorAuthorityInvalidated"`
}

type Phase string

const (
	PhaseReady             Phase = "ready"
	PhasePrompt            Phase = "prompt"
	PhaseAwaitingOutput    Phase = "awaiting_output"
	PhaseCandidatePending  Phase = "candidate_pending"
	PhaseCandidateObserved Phase = "candidate_observed"
	PhaseGatesRequired     Phase = "gates_required"
	PhaseNeedsYou          Phase = "needs_you"
)

type Policy struct {
	AutoFixCIFailures     bool   `json:"autoFixCiFailures"`
	AutoFixReviewFeedback bool   `json:"autoFixReviewFeedback"`
	AttemptLimit          uint32 `json:"attemptLimit"`
}

type State struct {
	SchemaVersion               string                 `json:"schemaVersion"`
	LineageKey                  string                 `json:"lineageKey"`
	TaskID                      string                 `json:"taskId"`
	RunID                       string                 `json:"runId"`
	RootCandidateID             string                 `json:"rootCandidateId"`
	RootCandidateSHA            string                 `json:"rootCandidateSha"`
	CurrentCandidateID          string                 `json:"currentCandidateId"`
	CurrentCandidateSHA         string                 `json:"currentCandidateSha"`
	OriginalTaskAgentUUID       string                 `json:"originalTaskAgentUuid"`
	CriterionIDs                []string               `json:"criterionIds"`
	FrozenPlanDigest            string                 `json:"frozenPlanDigest"`
	FrozenSkillSetDigest        string                 `json:"frozenSkillSetDigest"`
	CurrentDecisionDigest       string                 `json:"currentDecisionDigest"`
	CurrentDiffDigest           string                 `json:"currentDiffDigest"`
	Policy                      Policy                 `json:"policy"`
	Phase                       Phase                  `json:"phase"`
	Batches                     []Batch                `json:"batches"`
	Attempts                    []Attempt              `json:"attempts"`
	AcknowledgementRejections   uint32                 `json:"acknowledgementRejections"`
	PendingCandidateClaim       *candidate.Claim       `json:"pendingCandidateClaim,omitempty"`
	PendingCandidateObservation *candidate.Observation `json:"pendingCandidateObservation,omitempty"`
	PendingCandidateSequence    uint64                 `json:"pendingCandidateSequence,omitempty"`
	Gates                       []GatePlan             `json:"gates"`
	NeedsYouCode                string                 `json:"needsYouCode,omitempty"`
	WakeCondition               string                 `json:"wakeCondition,omitempty"`
}

// CloneState returns a mutation-safe copy for application CAS attempts. A
// losing coordinator must never alter slices owned by the durable snapshot it
// read while another coordinator is persisting the winner.
func CloneState(state State) State {
	state.CriterionIDs = slices.Clone(state.CriterionIDs)
	state.Batches = slices.Clone(state.Batches)
	for batchIndex := range state.Batches {
		batch := &state.Batches[batchIndex]
		batch.Sources = slices.Clone(batch.Sources)
		batch.Findings = slices.Clone(batch.Findings)
		for findingIndex := range batch.Findings {
			batch.Findings[findingIndex].Evidence = slices.Clone(batch.Findings[findingIndex].Evidence)
			batch.Findings[findingIndex].AcceptanceCoverage = slices.Clone(batch.Findings[findingIndex].AcceptanceCoverage)
		}
		batch.AcceptanceCoverage = slices.Clone(batch.AcceptanceCoverage)
		batch.BlockingClasses = slices.Clone(batch.BlockingClasses)
	}
	state.Attempts = slices.Clone(state.Attempts)
	for index := range state.Attempts {
		state.Attempts[index].CoverageDelta.Added = slices.Clone(state.Attempts[index].CoverageDelta.Added)
		state.Attempts[index].CoverageDelta.Removed = slices.Clone(state.Attempts[index].CoverageDelta.Removed)
		if state.Attempts[index].Output != nil {
			output := *state.Attempts[index].Output
			output.BatchedFindingIDs = slices.Clone(output.BatchedFindingIDs)
			output.ResolvedFindingIDs = slices.Clone(output.ResolvedFindingIDs)
			output.AcceptanceCoverage = slices.Clone(output.AcceptanceCoverage)
			state.Attempts[index].Output = &output
		}
	}
	state.Gates = slices.Clone(state.Gates)
	if state.PendingCandidateClaim != nil {
		value := *state.PendingCandidateClaim
		state.PendingCandidateClaim = &value
	}
	if state.PendingCandidateObservation != nil {
		value := *state.PendingCandidateObservation
		state.PendingCandidateObservation = &value
	}
	return state
}

func LineageKey(taskID, runID, candidateID, candidateSHA, agentUUID string) string {
	return "correction-lineage-" + digest(struct{ Task, Run, Candidate, SHA, Agent string }{taskID, runID, candidateID, candidateSHA, agentUUID})[:32]
}

func AttemptKey(lineage, batch string, number uint32, reason AttemptReason) string {
	return "correction-attempt-" + digest(struct {
		Lineage, Batch string
		Number         uint32
		Reason         AttemptReason
	}{lineage, batch, number, reason})[:32]
}

func PromptEffectID(attemptKey string) string { return "correction-prompt-" + digest(attemptKey)[:32] }

func GateKeys(candidateID, candidateSHA string, generation uint64) (string, string) {
	value := struct {
		CandidateID, CandidateSHA string
		Generation                uint64
	}{candidateID, candidateSHA, generation}
	return "ci-" + digest(struct {
			Value any
			Kind  string
		}{value, "complete_linux_ci"})[:32],
		"review-binding-" + digest(struct {
			Value any
			Kind  string
		}{value, "independent_review"})[:32]
}

func NewState(taskID, runID, candidateID, candidateSHA, agentUUID string, criterionIDs []string,
	planDigest, skillDigest, decisionDigest, diffDigest string, policy Policy, batch Batch) (State, bool) {
	criteria, ok := canonicalStrings(criterionIDs, MaximumCoverage)
	if !ok || len(criteria) == 0 || !identifierPattern.MatchString(taskID) || !identifierPattern.MatchString(runID) ||
		!identifierPattern.MatchString(candidateID) || !gitOIDPattern.MatchString(candidateSHA) || !uuidPattern.MatchString(agentUUID) ||
		!digestPattern.MatchString(planDigest) || !digestPattern.MatchString(skillDigest) ||
		!digestPattern.MatchString(decisionDigest) || !digestPattern.MatchString(diffDigest) ||
		policy.AttemptLimit != AttemptLimit || !ValidBatch(batch, criteria) || batch.CandidateID != candidateID || batch.CandidateSHA != candidateSHA {
		return State{}, false
	}
	state := State{
		SchemaVersion: StateSchemaVersion, TaskID: taskID, RunID: runID,
		RootCandidateID: candidateID, RootCandidateSHA: candidateSHA, CurrentCandidateID: candidateID, CurrentCandidateSHA: candidateSHA,
		OriginalTaskAgentUUID: agentUUID, CriterionIDs: criteria, FrozenPlanDigest: planDigest,
		FrozenSkillSetDigest: skillDigest, CurrentDecisionDigest: decisionDigest, CurrentDiffDigest: diffDigest,
		Policy: policy, Phase: PhaseReady, Batches: []Batch{batch}, Attempts: []Attempt{}, Gates: []GatePlan{},
	}
	state.LineageKey = LineageKey(taskID, runID, candidateID, candidateSHA, agentUUID)
	return state, ValidState(state)
}

func CurrentBatch(state State) (Batch, bool) {
	if len(state.Batches) == 0 {
		return Batch{}, false
	}
	return state.Batches[len(state.Batches)-1], true
}

func ValidOutput(output Output, state State, attempt Attempt) bool {
	batch, ok := CurrentBatch(state)
	if !ok || output.SchemaVersion != OutputSchemaVersion || output.AttemptKey != attempt.Key ||
		output.AgentUUID != state.OriginalTaskAgentUUID || output.BatchSHA256 != batch.SHA256 ||
		!slices.Equal(output.BatchedFindingIDs, FindingIDs(batch)) || output.AcknowledgementOnly && output.CandidateSHA != "" ||
		!output.AcknowledgementOnly && !gitOIDPattern.MatchString(output.CandidateSHA) {
		return false
	}
	resolved, ok := canonicalStrings(output.ResolvedFindingIDs, MaximumFindings)
	if !ok || !slices.Equal(resolved, output.ResolvedFindingIDs) {
		return false
	}
	allFindings := map[string]struct{}{}
	for _, id := range FindingIDs(batch) {
		allFindings[id] = struct{}{}
	}
	for _, id := range resolved {
		if _, exists := allFindings[id]; !exists {
			return false
		}
	}
	coverage, ok := canonicalStrings(output.AcceptanceCoverage, MaximumCoverage)
	if !ok || !slices.Equal(coverage, output.AcceptanceCoverage) {
		return false
	}
	criteria := map[string]struct{}{}
	for _, id := range state.CriterionIDs {
		criteria[id] = struct{}{}
	}
	for _, id := range coverage {
		if _, exists := criteria[id]; !exists {
			return false
		}
	}
	return true
}

func validEffect(effect Effect, attemptKey string) bool {
	if effect.ID != PromptEffectID(attemptKey) || !slices.Contains([]EffectPhase{EffectIntent, EffectDispatching, EffectComplete, EffectAmbiguous}, effect.Phase) {
		return false
	}
	if effect.Phase == EffectIntent {
		return effect.ExternalID == "" && effect.FactSHA256 == "" && effect.Cursor == 0
	}
	return true
}

func ValidState(state State) bool {
	if state.SchemaVersion != StateSchemaVersion || !identifierPattern.MatchString(state.LineageKey) ||
		!identifierPattern.MatchString(state.TaskID) || !identifierPattern.MatchString(state.RunID) ||
		!identifierPattern.MatchString(state.RootCandidateID) || !gitOIDPattern.MatchString(state.RootCandidateSHA) ||
		!identifierPattern.MatchString(state.CurrentCandidateID) || !gitOIDPattern.MatchString(state.CurrentCandidateSHA) ||
		!uuidPattern.MatchString(state.OriginalTaskAgentUUID) || state.Policy.AttemptLimit != AttemptLimit ||
		!digestPattern.MatchString(state.FrozenPlanDigest) || !digestPattern.MatchString(state.FrozenSkillSetDigest) ||
		!digestPattern.MatchString(state.CurrentDecisionDigest) || !digestPattern.MatchString(state.CurrentDiffDigest) ||
		LineageKey(state.TaskID, state.RunID, state.RootCandidateID, state.RootCandidateSHA, state.OriginalTaskAgentUUID) != state.LineageKey ||
		len(state.Batches) == 0 || len(state.Batches) > int(MaximumBatches) || len(state.Attempts) > int(AttemptLimit) || len(state.Gates) > int(AttemptLimit) {
		return false
	}
	criteria, ok := canonicalStrings(state.CriterionIDs, MaximumCoverage)
	if !ok || len(criteria) == 0 || !slices.Equal(criteria, state.CriterionIDs) {
		return false
	}
	seenBatches := map[string]struct{}{}
	for _, batch := range state.Batches {
		if !ValidBatch(batch, criteria) {
			return false
		}
		if _, duplicate := seenBatches[batch.SHA256]; duplicate {
			return false
		}
		seenBatches[batch.SHA256] = struct{}{}
	}
	currentBatch := state.Batches[len(state.Batches)-1]
	if (currentBatch.CandidateID != state.CurrentCandidateID || currentBatch.CandidateSHA != state.CurrentCandidateSHA) &&
		(state.Phase != PhaseGatesRequired || len(state.Gates) == 0 ||
			state.Gates[len(state.Gates)-1].CandidateID != state.CurrentCandidateID ||
			state.Gates[len(state.Gates)-1].CandidateSHA != state.CurrentCandidateSHA) {
		return false
	}
	for index, attempt := range state.Attempts {
		if attempt.Number != uint32(index+1) || attempt.Number > AttemptLimit ||
			!slices.Contains([]AttemptReason{ReasonInitial, ReasonFindingsChanged, ReasonAcknowledgementRejected}, attempt.Reason) ||
			attempt.Key != AttemptKey(state.LineageKey, attempt.BatchSHA256, attempt.Number, attempt.Reason) ||
			!digestPattern.MatchString(attempt.BatchSHA256) || !digestPattern.MatchString(attempt.ClassFingerprint) ||
			!identifierPattern.MatchString(attempt.CandidateBeforeID) || !gitOIDPattern.MatchString(attempt.CandidateBeforeSHA) ||
			!validEffect(attempt.Prompt, attempt.Key) || !identifierPattern.MatchString(attempt.BudgetReservationID) {
			return false
		}
		if attempt.PromptSHA256 != "" && !digestPattern.MatchString(attempt.PromptSHA256) {
			return false
		}
		if attempt.BudgetEvidenceID != "" && !identifierPattern.MatchString(attempt.BudgetEvidenceID) {
			return false
		}
		if attempt.Output != nil && !ValidOutput(*attempt.Output, stateForBatch(state, attempt.BatchSHA256), attempt) {
			return false
		}
		if attempt.Classification != "" && !slices.Contains([]Classification{ClassificationProductive, ClassificationChurn}, attempt.Classification) {
			return false
		}
		if (attempt.Output != nil) != (attempt.Classification != "") || attempt.Output != nil && attempt.Prompt.Phase != EffectComplete {
			return false
		}
		switch attempt.Prompt.Phase {
		case EffectIntent:
			if attempt.PromptSHA256 != "" || attempt.BudgetEvidenceID != "" || attempt.Output != nil {
				return false
			}
		case EffectDispatching:
			if attempt.PromptSHA256 == "" || attempt.BudgetEvidenceID != "" || attempt.Output != nil {
				return false
			}
		case EffectComplete:
			if attempt.PromptSHA256 == "" || attempt.Prompt.ExternalID != state.OriginalTaskAgentUUID ||
				!digestPattern.MatchString(attempt.Prompt.FactSHA256) || attempt.Prompt.Cursor == 0 || attempt.BudgetEvidenceID == "" {
				return false
			}
		case EffectAmbiguous:
			if attempt.PromptSHA256 == "" {
				return false
			}
		}
		if (attempt.ResultCandidateID == "") != (attempt.ResultCandidateSHA == "") || attempt.ResultCandidateID != "" &&
			(!identifierPattern.MatchString(attempt.ResultCandidateID) || !gitOIDPattern.MatchString(attempt.ResultCandidateSHA)) {
			return false
		}
	}
	if state.PendingCandidateClaim != nil || state.PendingCandidateObservation != nil || state.PendingCandidateSequence != 0 {
		if state.PendingCandidateClaim == nil || state.PendingCandidateObservation == nil || state.PendingCandidateSequence == 0 ||
			!candidate.ValidClaim(*state.PendingCandidateClaim) || !candidate.ValidObservation(*state.PendingCandidateObservation) ||
			state.PendingCandidateObservation.ClaimSHA256 != candidate.ClaimSHA256(*state.PendingCandidateClaim) {
			return false
		}
	}
	for _, gate := range state.Gates {
		ciKey, reviewKey := GateKeys(gate.CandidateID, gate.CandidateSHA, gate.CandidateGeneration)
		if !identifierPattern.MatchString(gate.CandidateID) || !gitOIDPattern.MatchString(gate.CandidateSHA) || gate.CandidateGeneration == 0 ||
			gate.CIKey != ciKey || gate.ReviewBindingKey != reviewKey || !gate.FreshCIRequired || !gate.FreshReviewRequired || !gate.PriorAuthorityInvalidated {
			return false
		}
	}
	if state.Phase == PhaseNeedsYou {
		return identifierPattern.MatchString(state.NeedsYouCode) && validText(state.WakeCondition, 256)
	}
	if state.NeedsYouCode != "" || state.WakeCondition != "" || !slices.Contains([]Phase{PhaseReady, PhasePrompt, PhaseAwaitingOutput, PhaseCandidatePending, PhaseCandidateObserved, PhaseGatesRequired}, state.Phase) {
		return false
	}
	if len(state.Attempts) > 0 {
		last := state.Attempts[len(state.Attempts)-1]
		switch state.Phase {
		case PhasePrompt:
			if last.Output != nil || last.Prompt.Phase == EffectComplete {
				return false
			}
		case PhaseAwaitingOutput:
			if last.Prompt.Phase != EffectComplete || last.Output != nil {
				return false
			}
		case PhaseCandidatePending:
			if last.Output == nil || last.Output.AcknowledgementOnly || last.Output.CandidateSHA == last.CandidateBeforeSHA || state.PendingCandidateClaim != nil {
				return false
			}
		case PhaseCandidateObserved:
			if last.Output == nil || state.PendingCandidateClaim == nil {
				return false
			}
		case PhaseGatesRequired:
			if last.ResultCandidateID != state.CurrentCandidateID || last.ResultCandidateSHA != state.CurrentCandidateSHA || len(state.Gates) == 0 {
				return false
			}
		}
	}
	return true
}

func stateForBatch(state State, sha string) State {
	for index, batch := range state.Batches {
		if batch.SHA256 == sha {
			state.Batches = []Batch{state.Batches[index]}
			state.CurrentCandidateID = batch.CandidateID
			state.CurrentCandidateSHA = batch.CandidateSHA
			return state
		}
	}
	return state
}
