// SPDX-License-Identifier: Apache-2.0

package execution

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
)

const PreparationPlanSchemaVersion = "director.preparation-plan/v1"

// PreparationStep is one finite, closed step from PLAN section 11.
type PreparationStep struct {
	ID                       string `json:"id"`
	Ordinal                  int    `json:"ordinal"`
	Kind                     string `json:"kind"`
	EffectClass              string `json:"effectClass"`
	TimeoutSeconds           int    `json:"timeoutSeconds"`
	PerCommandTimeoutSeconds int    `json:"perCommandTimeoutSeconds,omitempty"`
	AggregateTimeoutSeconds  int    `json:"aggregateTimeoutSeconds,omitempty"`
	FailureCode              string `json:"failureCode"`
}

// PreparationPlan is frozen into each M1 Run before its first effect.
type PreparationPlan struct {
	SchemaVersion              string             `json:"schemaVersion"`
	ID                         string             `json:"id"`
	Scope                      Scope              `json:"scope"`
	InputsHash                 string             `json:"inputsHash"`
	WholePlanDeadlineSeconds   int                `json:"wholePlanDeadlineSeconds"`
	DependencyAggregateSeconds int                `json:"dependencyAggregateSeconds"`
	Steps                      [9]PreparationStep `json:"steps"`
}

func planDigest(value any) string {
	encoded, err := json.Marshal(value)
	if err != nil {
		panic("marshal fixed preparation value: " + err.Error())
	}
	digest := sha256.Sum256(encoded)
	return hex.EncodeToString(digest[:])
}

// NewM1PreparationPlan returns the exact finite nine-step walking-skeleton
// plan. This fake path declares no dependency commands but retains their
// simultaneous per-command, aggregate, and whole-plan ceilings.
func NewM1PreparationPlan(scope Scope, inputsHash string) PreparationPlan {
	plan := PreparationPlan{
		SchemaVersion: PreparationPlanSchemaVersion, Scope: scope, InputsHash: inputsHash,
		WholePlanDeadlineSeconds: 1_200, DependencyAggregateSeconds: 900,
		Steps: [9]PreparationStep{
			{ID: "freeze-inputs", Ordinal: 1, Kind: "freeze_inputs", EffectClass: "store_only", TimeoutSeconds: 10, FailureCode: "freeze_failed"},
			{ID: "reconcile-eligibility", Ordinal: 2, Kind: "reconcile_eligibility", EffectClass: "store_only", TimeoutSeconds: 30, FailureCode: "eligibility_failed"},
			{ID: "admit-security", Ordinal: 3, Kind: "admit_security", EffectClass: "store_only", TimeoutSeconds: 30, FailureCode: "security_admission_failed"},
			{ID: "create-worktree-view", Ordinal: 4, Kind: "create_worktree_and_host_view", EffectClass: "unique_create", TimeoutSeconds: 120, FailureCode: "worktree_preparation_failed"},
			{ID: "materialize-boundary", Ordinal: 5, Kind: "materialize_rootless_oci", EffectClass: "unique_create", TimeoutSeconds: 60, FailureCode: "boundary_preparation_failed"},
			{ID: "probe-tooling", Ordinal: 6, Kind: "probe_tooling", EffectClass: "store_only", TimeoutSeconds: 60, FailureCode: "tooling_probe_failed"},
			{ID: "prepare-dependencies", Ordinal: 7, Kind: "prepare_dependencies", EffectClass: "unique_create", TimeoutSeconds: 900, PerCommandTimeoutSeconds: 300, AggregateTimeoutSeconds: 900, FailureCode: "dependency_preparation_failed"},
			{ID: "build-context", Ordinal: 8, Kind: "build_context", EffectClass: "store_only", TimeoutSeconds: 30, FailureCode: "context_build_failed"},
			{ID: "commit-ready", Ordinal: 9, Kind: "commit_preparation_ready", EffectClass: "store_only", TimeoutSeconds: 10, FailureCode: "preparation_barrier_failed"},
		},
	}
	identity := plan
	identity.ID = ""
	plan.ID = planDigest(identity)
	return plan
}

// ValidPreparationPlan verifies the immutable schema, scope, inputs, steps,
// ceilings, and derived identity.
func ValidPreparationPlan(plan PreparationPlan) bool {
	return plan.ID != "" && plan == NewM1PreparationPlan(plan.Scope, plan.InputsHash)
}

// PreparationOutputs names every exact fact hash required by the M1 barrier.
type PreparationOutputs struct {
	FrozenInputsHash      string `json:"frozenInputsHash"`
	EligibilityHash       string `json:"eligibilityHash"`
	SecurityAdmissionHash string `json:"securityAdmissionHash"`
	WorktreeHash          string `json:"worktreeHash"`
	HostViewHash          string `json:"hostViewHash"`
	IsolationHash         string `json:"isolationHash"`
	ToolingHash           string `json:"toolingHash"`
	DependenciesHash      string `json:"dependenciesHash"`
	ContextHash           string `json:"contextHash"`
}

// PreparationBarrier returns the immutable barrier digest only when the plan
// identity and every named output are complete.
func PreparationBarrier(plan PreparationPlan, outputs PreparationOutputs) (string, bool) {
	if !ValidPreparationPlan(plan) || outputs.FrozenInputsHash == "" || outputs.EligibilityHash == "" ||
		outputs.SecurityAdmissionHash == "" || outputs.WorktreeHash == "" ||
		outputs.HostViewHash == "" || outputs.IsolationHash == "" ||
		outputs.ToolingHash == "" || outputs.DependenciesHash == "" || outputs.ContextHash == "" {
		return "", false
	}
	return planDigest(struct {
		PlanID  string             `json:"planId"`
		Outputs PreparationOutputs `json:"outputs"`
	}{plan.ID, outputs}), true
}
