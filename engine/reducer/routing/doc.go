// SPDX-License-Identifier: Apache-2.0

// Package routing is the exclusive home of the pure, versioned routing
// decision reducer.
package routing

import (
	candidatedomain "github.com/mcuadros/director-engine/domain/candidate"
	"github.com/mcuadros/director-engine/domain/execution"
)

const SchemaVersion = "director.reducer.routing/v1"

// Facts is the closed M1 routing input for a launched fake Task Agent.
type Facts struct {
	SchemaVersion             string                       `json:"schemaVersion"`
	AgentID                   string                       `json:"agentId"`
	AgentTurnEnded            bool                         `json:"agentTurnEnded"`
	RepositoryBindingHash     string                       `json:"repositoryBindingHash"`
	OperationalLimitsAdmitted bool                         `json:"operationalLimitsAdmitted"`
	OperationalObservationID  string                       `json:"operationalObservationId"`
	OperationalNeedCode       execution.NeedCode           `json:"operationalNeedCode,omitempty"`
	TaskStoreNowMillis        int64                        `json:"taskStoreNowMillis"`
	Claim                     *execution.CompletedClaim    `json:"claim,omitempty"`
	CandidateClaim            *candidatedomain.Claim       `json:"candidateClaim,omitempty"`
	CandidateObservation      *candidatedomain.Observation `json:"candidateObservation,omitempty"`
}

// DecisionKind is the finite routing result used by the fake M1 path.
type DecisionKind string

const (
	DecisionObserveOperationalLimits DecisionKind = "observe_operational_limits"
	DecisionWaitAgent                DecisionKind = "wait_agent"
	DecisionObserveCandidate         DecisionKind = "observe_candidate"
	DecisionAdmitCandidate           DecisionKind = "admit_candidate"
	DecisionEscalate                 DecisionKind = "escalate"
)

// Decision is a pure route. A completed claim alone can only request an exact
// Git observation; it cannot admit a Candidate or authorize cleanup.
type Decision struct {
	SchemaVersion     string                    `json:"schemaVersion"`
	Kind              DecisionKind              `json:"kind"`
	Code              string                    `json:"code,omitempty"`
	CandidateSHA      string                    `json:"candidateSha,omitempty"`
	Manifest          *candidatedomain.Manifest `json:"manifest,omitempty"`
	CleanupAuthorized bool                      `json:"cleanupAuthorized"`
}

func needs(code string) Decision {
	return Decision{SchemaVersion: SchemaVersion, Kind: DecisionEscalate, Code: code}
}

// Reduce reconciles a closed claim against named engine and Git facts.
func Reduce(facts Facts) Decision {
	if facts.SchemaVersion != SchemaVersion {
		return needs("routing_facts_version_mismatch")
	}
	if facts.OperationalObservationID == "" {
		return Decision{SchemaVersion: SchemaVersion, Kind: DecisionObserveOperationalLimits}
	}
	if !facts.OperationalLimitsAdmitted {
		code := string(facts.OperationalNeedCode)
		if code == "" {
			code = "operational_limit_fact_missing"
		}
		return needs(code)
	}
	if facts.AgentID == "" || facts.RepositoryBindingHash == "" {
		return needs("agent_identity_missing")
	}
	if facts.Claim == nil {
		return Decision{SchemaVersion: SchemaVersion, Kind: DecisionWaitAgent}
	}
	claim := facts.Claim
	if claim.ID == "" || claim.SchemaVersion != "director.agent-outcome.completed/v1" ||
		claim.Outcome != "completed" || claim.AgentID != facts.AgentID ||
		claim.CandidateSHA == "" || claim.BaseSHA == "" || len(claim.CriteriaResults) == 0 ||
		!facts.AgentTurnEnded {
		return needs("completed_claim_invalid")
	}
	if facts.CandidateObservation == nil {
		return Decision{SchemaVersion: SchemaVersion, Kind: DecisionObserveCandidate}
	}
	if facts.CandidateClaim == nil {
		return needs("candidate_claim_binding_invalid")
	}
	observation := facts.CandidateObservation
	if facts.CandidateClaim.ID != claim.ID || facts.CandidateClaim.ActorID != claim.AgentID ||
		facts.CandidateClaim.CandidateSHA != claim.CandidateSHA || facts.CandidateClaim.BaseSHA != claim.BaseSHA {
		return needs("candidate_observation_binding_invalid")
	}
	admission := candidatedomain.Evaluate(*facts.CandidateClaim, *observation, facts.RepositoryBindingHash, facts.TaskStoreNowMillis)
	if admission.Kind != candidatedomain.DecisionAdmit || admission.Manifest == nil {
		return needs("candidate_facts_not_admitted")
	}
	return Decision{
		SchemaVersion: SchemaVersion, Kind: DecisionAdmitCandidate,
		CandidateSHA: claim.CandidateSHA, Manifest: admission.Manifest,
	}
}
