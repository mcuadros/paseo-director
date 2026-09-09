// SPDX-License-Identifier: Apache-2.0

// Package correction is the exclusive home of the pure, versioned correction
// turn reducer.
package correction

import (
	"slices"
	"strings"
	"unicode"
	"unicode/utf8"
)

const SchemaVersion = "director.reducer.correction/v1"

// FindingSet carries every current correction input by durable identity. The
// prompt receives the union as one batch; it never receives review findings in
// serial turns which could consume the correction budget one item at a time.
type FindingSet struct {
	Review        []string `json:"review,omitempty"`
	Validation    []string `json:"validation,omitempty"`
	HumanFeedback []string `json:"humanFeedback,omitempty"`
}

// TurnOutput is the Task Agent's bounded result from one authorized correction
// turn. AcknowledgementOnly is explicit so prose cannot be interpreted as a
// correction, and BatchedFindingIDs proves the agent received the whole batch.
type TurnOutput struct {
	AgentID             string   `json:"agentId"`
	CandidateSHA        string   `json:"candidateSha,omitempty"`
	AcknowledgementOnly bool     `json:"acknowledgementOnly"`
	BatchedFindingIDs   []string `json:"batchedFindingIds"`
}

// Facts is the closed input for dispatching or reconciling one correction
// turn. PLAN and skill material is reused only when its current digest equals
// the frozen Run digest. Human decisions and the Candidate diff are always
// refreshed and carried by their current digests.
type Facts struct {
	SchemaVersion             string      `json:"schemaVersion"`
	TaskAgentID               string      `json:"taskAgentId"`
	BaseSHA                   string      `json:"baseSha"`
	PreviousCandidateSHA      string      `json:"previousCandidateSha"`
	FrozenPlanDigest          string      `json:"frozenPlanDigest"`
	CurrentPlanDigest         string      `json:"currentPlanDigest"`
	FrozenSkillSetDigest      string      `json:"frozenSkillSetDigest"`
	CurrentSkillSetDigest     string      `json:"currentSkillSetDigest"`
	CurrentDecisionDigest     string      `json:"currentDecisionDigest"`
	CurrentDiffDigest         string      `json:"currentDiffDigest"`
	Findings                  FindingSet  `json:"findings"`
	CorrectionAttempts        uint64      `json:"correctionAttempts"`
	CorrectionAttemptLimit    uint64      `json:"correctionAttemptLimit"`
	AcknowledgementRejections uint64      `json:"acknowledgementRejections"`
	Output                    *TurnOutput `json:"output,omitempty"`
}

type DecisionKind string

const (
	DecisionDispatchCorrection        DecisionKind = "dispatch_correction"
	DecisionRejectAcknowledgementOnce DecisionKind = "reject_acknowledgement_once"
	DecisionAdmitCorrectedCandidate   DecisionKind = "admit_corrected_candidate"
	DecisionEscalate                  DecisionKind = "escalate"
)

// Decision carries only identities needed for the next deterministic effect.
// Reuse digests and refresh digests stay separate so an adapter cannot replace
// a current decision/diff with the frozen context bundle.
type Decision struct {
	SchemaVersion             string       `json:"schemaVersion"`
	Kind                      DecisionKind `json:"kind"`
	Code                      string       `json:"code,omitempty"`
	CandidateSHA              string       `json:"candidateSha,omitempty"`
	BatchedFindingIDs         []string     `json:"batchedFindingIds,omitempty"`
	ReusePlanDigest           string       `json:"reusePlanDigest,omitempty"`
	ReuseSkillSetDigest       string       `json:"reuseSkillSetDigest,omitempty"`
	RefreshDecisionDigest     string       `json:"refreshDecisionDigest,omitempty"`
	RefreshDiffDigest         string       `json:"refreshDiffDigest,omitempty"`
	AcknowledgementRejections uint64       `json:"acknowledgementRejections,omitempty"`
	CleanupAuthorized         bool         `json:"cleanupAuthorized"`
}

func escalate(code string) Decision {
	return Decision{SchemaVersion: SchemaVersion, Kind: DecisionEscalate, Code: code}
}

func digestPresent(value string) bool {
	if len(value) != 64 {
		return false
	}
	for _, character := range value {
		if (character < '0' || character > '9') && (character < 'a' || character > 'f') {
			return false
		}
	}
	return true
}

func shaPresent(value string) bool {
	return len(value) == 40 && digestPresent(value+strings.Repeat("0", 24))
}

func validIdentity(value string) bool {
	return value != "" && len(value) <= 200 && utf8.ValidString(value) &&
		strings.IndexFunc(value, unicode.IsControl) < 0
}

func findingBatch(findings FindingSet) ([]string, bool) {
	batch := append([]string(nil), findings.Review...)
	batch = append(batch, findings.Validation...)
	batch = append(batch, findings.HumanFeedback...)
	if len(batch) == 0 || len(batch) > 1_000 {
		return nil, false
	}
	seen := make(map[string]struct{}, len(batch))
	for _, id := range batch {
		if !validIdentity(id) {
			return nil, false
		}
		if _, duplicate := seen[id]; duplicate {
			return nil, false
		}
		seen[id] = struct{}{}
	}
	slices.Sort(batch)
	return batch, true
}

// Reduce admits one correction action from closed facts. It never grants
// cleanup, publication, review, or integration authority.
func Reduce(facts Facts) Decision {
	if facts.SchemaVersion != SchemaVersion {
		return escalate("correction_facts_version_mismatch")
	}
	if !validIdentity(facts.TaskAgentID) || !shaPresent(facts.BaseSHA) ||
		!shaPresent(facts.PreviousCandidateSHA) ||
		!digestPresent(facts.FrozenPlanDigest) || !digestPresent(facts.CurrentPlanDigest) ||
		!digestPresent(facts.FrozenSkillSetDigest) || !digestPresent(facts.CurrentSkillSetDigest) ||
		!digestPresent(facts.CurrentDecisionDigest) || !digestPresent(facts.CurrentDiffDigest) ||
		facts.CorrectionAttemptLimit == 0 || facts.CorrectionAttempts > facts.CorrectionAttemptLimit {
		return escalate("correction_facts_invalid")
	}
	if facts.FrozenPlanDigest != facts.CurrentPlanDigest ||
		facts.FrozenSkillSetDigest != facts.CurrentSkillSetDigest {
		return escalate("correction_frozen_context_changed")
	}
	batch, valid := findingBatch(facts.Findings)
	if !valid {
		return escalate("correction_finding_batch_invalid")
	}
	context := Decision{
		SchemaVersion:         SchemaVersion,
		BatchedFindingIDs:     batch,
		ReusePlanDigest:       facts.FrozenPlanDigest,
		ReuseSkillSetDigest:   facts.FrozenSkillSetDigest,
		RefreshDecisionDigest: facts.CurrentDecisionDigest,
		RefreshDiffDigest:     facts.CurrentDiffDigest,
	}
	if facts.Output == nil {
		if facts.CorrectionAttempts >= facts.CorrectionAttemptLimit {
			return escalate("correction_attempt_limit_exhausted")
		}
		context.Kind = DecisionDispatchCorrection
		return context
	}
	output := facts.Output
	if output.AgentID != facts.TaskAgentID ||
		!slices.Equal(output.BatchedFindingIDs, batch) {
		return escalate("correction_output_binding_invalid")
	}
	if output.AcknowledgementOnly {
		if output.CandidateSHA != "" {
			return escalate("correction_output_binding_invalid")
		}
		if facts.AcknowledgementRejections > 0 {
			return escalate("correction_acknowledgement_repeated")
		}
		context.Kind = DecisionRejectAcknowledgementOnce
		context.AcknowledgementRejections = 1
		return context
	}
	if facts.CorrectionAttempts >= facts.CorrectionAttemptLimit ||
		!shaPresent(output.CandidateSHA) || output.CandidateSHA == facts.PreviousCandidateSHA {
		return escalate("corrected_candidate_invalid")
	}
	context.Kind = DecisionAdmitCorrectedCandidate
	context.CandidateSHA = output.CandidateSHA
	return context
}
