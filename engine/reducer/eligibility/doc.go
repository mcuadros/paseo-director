// SPDX-License-Identifier: Apache-2.0

// Package eligibility is the exclusive home of the pure, versioned
// eligibility decision reducer.
package eligibility

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"

	"github.com/mcuadros/director-engine/domain/execution"
)

const SchemaVersion = "director.reducer.eligibility/v1"

// BooleanFact distinguishes a negative observation from a missing one.
type BooleanFact struct {
	Observed bool `json:"observed"`
	Value    bool `json:"value"`
}

// Facts is the closed, named input to the M1 eligibility reducer. The caller
// supplies TaskStore time so this pure package reads no clock.
type Facts struct {
	SchemaVersion           string                           `json:"schemaVersion"`
	Scope                   execution.Scope                  `json:"scope"`
	ProjectLeaseCurrent     BooleanFact                      `json:"projectLeaseCurrent"`
	ProjectActive           BooleanFact                      `json:"projectActive"`
	OrganizerRevisionActive BooleanFact                      `json:"organizerRevisionActive"`
	TaskComplete            BooleanFact                      `json:"taskComplete"`
	DependenciesSatisfied   BooleanFact                      `json:"dependenciesSatisfied"`
	NoActiveRun             BooleanFact                      `json:"noActiveRun"`
	LaunchPolicyAllows      BooleanFact                      `json:"launchPolicyAllows"`
	CapacityAvailable       BooleanFact                      `json:"capacityAvailable"`
	BudgetsAvailable        BooleanFact                      `json:"budgetsAvailable"`
	ProviderAdmitted        BooleanFact                      `json:"providerAdmitted"`
	RepositoryIdentityExact BooleanFact                      `json:"repositoryIdentityExact"`
	LifecycleSurfaces       execution.LifecycleSurfaces      `json:"lifecycleSurfaces"`
	LifecycleApproval       *execution.LifecycleApproval     `json:"lifecycleApproval,omitempty"`
	Isolation               execution.IsolationObservation   `json:"isolation"`
	OperationalPolicy       execution.OperationalPolicy      `json:"operationalPolicy"`
	OperationalObservation  execution.OperationalObservation `json:"operationalObservation"`
	TaskStoreNowMillis      int64                            `json:"taskStoreNowMillis"`
}

// DecisionKind is the complete eligibility result vocabulary.
type DecisionKind string

const (
	DecisionEligible   DecisionKind = "eligible"
	DecisionWaitQueued DecisionKind = "wait_queued"
	DecisionEscalate   DecisionKind = "escalate"
)

// Code is a bounded deterministic eligibility reason.
type Code string

const (
	CodeFactsVersion                  Code = "eligibility_facts_version_mismatch"
	CodeFactMissing                   Code = "eligibility_fact_missing"
	CodeProjectInactive               Code = "project_inactive"
	CodeDependenciesUnsatisfied       Code = "dependencies_unsatisfied"
	CodeActiveRunPresent              Code = "active_run_present"
	CodeLaunchPolicyDisallows         Code = "launch_policy_disallows"
	CodeCapacityUnavailable           Code = "capacity_unavailable"
	CodeBudgetUnavailable             Code = "budget_unavailable"
	CodeProviderNotAdmitted           Code = "provider_not_admitted"
	CodeRepositoryIdentityMismatch    Code = "repository_identity_mismatch"
	CodeLifecycleApprovalRequired     Code = "lifecycle_human_approval_required"
	CodeLifecycleApprovalInvalid      Code = "lifecycle_human_approval_invalid"
	CodeLifecycleConfigurationInvalid Code = "lifecycle_configuration_invalid"
	CodeIsolationFactMissing          Code = "rootless_oci_observation_missing"
	CodeRootlessOCIRequired           Code = "rootless_oci_boundary_required"
	CodeOperationalPolicyInvalid      Code = "operational_limit_policy_invalid"
	CodeOperationalFactMissing        Code = "operational_limit_fact_missing"
	CodeFreeDiskFloor                 Code = "free_disk_floor_exceeded"
	CodeWorktreeBytes                 Code = "worktree_bytes_exceeded"
	CodeProcessLimit                  Code = "process_limit_exceeded"
	CodeMemoryLimit                   Code = "memory_limit_exceeded"
	CodeElapsedLimit                  Code = "elapsed_time_limit_exceeded"
	CodeOutputLimit                   Code = "output_limit_exceeded"
	CodeTemporaryLimit                Code = "temporary_storage_limit_exceeded"
)

// Decision is stable for an identical fact set. CleanupAuthorized is always
// false because eligibility parking never grants destructive authority.
type Decision struct {
	SchemaVersion     string       `json:"schemaVersion"`
	DecisionID        string       `json:"decisionId"`
	FactsHash         string       `json:"factsHash"`
	Kind              DecisionKind `json:"kind"`
	Code              Code         `json:"code,omitempty"`
	LifecycleDigest   string       `json:"lifecycleDigest,omitempty"`
	IsolationDigest   string       `json:"isolationDigest,omitempty"`
	OperationalDigest string       `json:"operationalDigest,omitempty"`
	CleanupAuthorized bool         `json:"cleanupAuthorized"`
}

func digest(value any) string {
	encoded, err := json.Marshal(value)
	if err != nil {
		panic("marshal fixed eligibility value: " + err.Error())
	}
	sum := sha256.Sum256(encoded)
	return hex.EncodeToString(sum[:])
}

func decide(factsHash string, kind DecisionKind, code Code, lifecycle, isolation, operational string) Decision {
	decision := Decision{
		SchemaVersion: SchemaVersion, FactsHash: factsHash, Kind: kind, Code: code,
		LifecycleDigest: lifecycle, IsolationDigest: isolation,
		OperationalDigest: operational, CleanupAuthorized: false,
	}
	decision.DecisionID = digest(decision)
	return decision
}

func allObserved(facts Facts) bool {
	return facts.ProjectLeaseCurrent.Observed && facts.ProjectActive.Observed &&
		facts.OrganizerRevisionActive.Observed && facts.TaskComplete.Observed &&
		facts.DependenciesSatisfied.Observed && facts.NoActiveRun.Observed &&
		facts.LaunchPolicyAllows.Observed && facts.CapacityAvailable.Observed &&
		facts.BudgetsAvailable.Observed && facts.ProviderAdmitted.Observed &&
		facts.RepositoryIdentityExact.Observed
}

func admissionCode(code execution.NeedCode) Code { return Code(code) }

// Reduce maps one immutable fact set to one deterministic eligibility result.
func Reduce(facts Facts) Decision {
	factsHash := digest(facts)
	if facts.SchemaVersion != SchemaVersion {
		return decide(factsHash, DecisionEscalate, CodeFactsVersion, "", "", "")
	}
	if !allObserved(facts) {
		return decide(factsHash, DecisionEscalate, CodeFactMissing, "", "", "")
	}
	if !facts.ProjectLeaseCurrent.Value || !facts.OrganizerRevisionActive.Value || !facts.TaskComplete.Value {
		return decide(factsHash, DecisionEscalate, CodeFactMissing, "", "", "")
	}
	if !facts.ProjectActive.Value {
		return decide(factsHash, DecisionWaitQueued, CodeProjectInactive, "", "", "")
	}
	if !facts.DependenciesSatisfied.Value {
		return decide(factsHash, DecisionWaitQueued, CodeDependenciesUnsatisfied, "", "", "")
	}
	if !facts.NoActiveRun.Value {
		return decide(factsHash, DecisionWaitQueued, CodeActiveRunPresent, "", "", "")
	}
	if !facts.LaunchPolicyAllows.Value {
		return decide(factsHash, DecisionWaitQueued, CodeLaunchPolicyDisallows, "", "", "")
	}
	if !facts.CapacityAvailable.Value {
		return decide(factsHash, DecisionWaitQueued, CodeCapacityUnavailable, "", "", "")
	}
	if !facts.BudgetsAvailable.Value {
		return decide(factsHash, DecisionEscalate, CodeBudgetUnavailable, "", "", "")
	}
	if !facts.ProviderAdmitted.Value {
		return decide(factsHash, DecisionEscalate, CodeProviderNotAdmitted, "", "", "")
	}
	if !facts.RepositoryIdentityExact.Value {
		return decide(factsHash, DecisionEscalate, CodeRepositoryIdentityMismatch, "", "", "")
	}
	lifecycle := execution.AdmitLifecycle(facts.Scope, facts.LifecycleSurfaces, facts.LifecycleApproval)
	if lifecycle.Kind != execution.AdmissionAllow {
		return decide(factsHash, DecisionEscalate, admissionCode(lifecycle.Code), "", "", "")
	}
	isolation := execution.AdmitIsolation(facts.Isolation)
	if isolation.Kind != execution.AdmissionAllow {
		return decide(factsHash, DecisionEscalate, admissionCode(isolation.Code), lifecycle.Digest, "", "")
	}
	operational := execution.EvaluateOperationalLimits(
		facts.OperationalPolicy, facts.OperationalObservation, facts.TaskStoreNowMillis,
	)
	if operational.Kind != execution.AdmissionAllow {
		return decide(factsHash, DecisionEscalate, admissionCode(operational.Code), lifecycle.Digest, isolation.Digest, "")
	}
	return decide(factsHash, DecisionEligible, "", lifecycle.Digest, isolation.Digest, operational.Digest)
}
