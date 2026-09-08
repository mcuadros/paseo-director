// SPDX-License-Identifier: Apache-2.0

package execution

const SchemaVersion = "director.execution/v1"

// EffectKind is the closed M1 effect vocabulary. Host-view operations cross
// the engine-owned host port; the remaining operations use engine adapters.
type EffectKind string

const (
	EffectWorktreeCreate      EffectKind = "worktree.create"
	EffectHostViewCreate      EffectKind = "host_view.create"
	EffectBoundaryMaterialize EffectKind = "rootless_oci.materialize"
	EffectSetupRun            EffectKind = "lifecycle_setup.run"
	EffectAgentCreate         EffectKind = "task_agent.create_with_initial_prompt"
	EffectAgentArchive        EffectKind = "task_agent.archive"
	EffectHostViewArchive     EffectKind = "host_view.archive"
	EffectWorktreeRemove      EffectKind = "worktree.remove"
)

// EffectPhase records intent before any adapter handoff and keeps a possible
// handoff observable after interruption.
type EffectPhase string

const (
	EffectIntentRecorded EffectPhase = "intent_recorded"
	EffectDispatching    EffectPhase = "dispatching"
	EffectComplete       EffectPhase = "complete"
)

// ObservationStatus is the normalized, bounded external fact vocabulary used
// by the fake M1 adapters and later production adapters.
type ObservationStatus string

const (
	ObservationDesired      ObservationStatus = "desired"
	ObservationAbsent       ObservationStatus = "absent"
	ObservationOwnedPresent ObservationStatus = "owned_present"
	ObservationDifferent    ObservationStatus = "different"
	ObservationAmbiguous    ObservationStatus = "ambiguous"
	ObservationUnavailable  ObservationStatus = "unavailable"
)

// EffectObservation is persisted before a reducer may consume it.
type EffectObservation struct {
	ID                    string            `json:"id"`
	EffectID              string            `json:"effectId"`
	Status                ObservationStatus `json:"status"`
	ExternalID            string            `json:"externalId,omitempty"`
	Cursor                uint64            `json:"cursor,omitempty"`
	ObservedAt            string            `json:"observedAt,omitempty"`
	ObservedAtMillis      int64             `json:"observedAtMillis"`
	MaximumAgeMillis      int64             `json:"maximumAgeMillis"`
	BindingHash           string            `json:"bindingHash"`
	PriorDispatcherAbsent bool              `json:"priorDispatcherAbsent"`
	FactHash              string            `json:"factHash"`
}

// Effect is one immutable intent with bounded attempts and its latest
// unconsumed observation.
type Effect struct {
	ID           string             `json:"id"`
	Kind         EffectKind         `json:"kind"`
	Phase        EffectPhase        `json:"phase"`
	Attempt      uint32             `json:"attempt"`
	AttemptLimit uint32             `json:"attemptLimit"`
	ExternalID   string             `json:"externalId,omitempty"`
	Observation  *EffectObservation `json:"observation,omitempty"`
}

// NeedsYou is a typed fail-closed park. CleanupAuthorized is structurally
// present so callers cannot mistake parking for destruction permission.
type NeedsYou struct {
	Code              NeedCode `json:"code"`
	WakeCondition     string   `json:"wakeCondition"`
	CleanupAuthorized bool     `json:"cleanupAuthorized"`
}

// CompletedClaim is the bounded subset of the closed completed outcome used by
// the M1 fake path. It remains a claim until candidate facts are observed.
type CompletedClaim struct {
	ID                string            `json:"id"`
	SchemaVersion     string            `json:"schemaVersion"`
	Outcome           string            `json:"outcome"`
	AgentID           string            `json:"agentId"`
	CandidateSHA      string            `json:"candidateSha"`
	BaseSHA           string            `json:"baseSha"`
	CriteriaResults   map[string]string `json:"criteriaResults"`
	ResidualRiskCodes []string          `json:"residualRiskCodes"`
}

// CandidateObservation contains external Git facts, not model narration.
type CandidateObservation struct {
	ID               string `json:"id"`
	ClaimID          string `json:"claimId"`
	WorktreeID       string `json:"worktreeId"`
	BindingHash      string `json:"bindingHash"`
	FactHash         string `json:"factHash"`
	ObservedAtMillis int64  `json:"observedAtMillis"`
	MaximumAgeMillis int64  `json:"maximumAgeMillis"`
	CommitSHA        string `json:"commitSha"`
	BaseSHA          string `json:"baseSha"`
	Clean            bool   `json:"clean"`
	Reachable        bool   `json:"reachable"`
	Owned            bool   `json:"owned"`
	DescendsFromBase bool   `json:"descendsFromBase"`
	NoConflict       bool   `json:"noConflict"`
}

// State is the durable M1 execution projection stored inside its Run record.
// A zero State belongs to pre-execution TaskStore records created by older M1
// skeletons and remains valid.
type State struct {
	SchemaVersion                    string                  `json:"schemaVersion,omitempty"`
	Scope                            Scope                   `json:"scope,omitempty"`
	EligibilityDecisionVersion       string                  `json:"eligibilityDecisionVersion,omitempty"`
	EligibilityDecisionID            string                  `json:"eligibilityDecisionId,omitempty"`
	EligibilityFactsHash             string                  `json:"eligibilityFactsHash,omitempty"`
	CapacityReservationID            string                  `json:"capacityReservationId,omitempty"`
	BudgetReservationID              string                  `json:"budgetReservationId,omitempty"`
	RepositoryBindingHash            string                  `json:"repositoryBindingHash,omitempty"`
	LifecycleDigest                  string                  `json:"lifecycleDigest,omitempty"`
	LifecycleApproval                *LifecycleApproval      `json:"lifecycleApproval,omitempty"`
	IsolationDigest                  string                  `json:"isolationDigest,omitempty"`
	Isolation                        IsolationObservation    `json:"isolation,omitempty"`
	OperationalPolicy                OperationalPolicy       `json:"operationalPolicy,omitempty"`
	LifecycleSurfaces                LifecycleSurfaces       `json:"lifecycleSurfaces,omitempty"`
	SourcePath                       string                  `json:"sourcePath,omitempty"`
	WorktreePath                     string                  `json:"worktreePath,omitempty"`
	Branch                           string                  `json:"branch,omitempty"`
	TaskTitle                        string                  `json:"taskTitle,omitempty"`
	CriterionIDs                     []string                `json:"criterionIds,omitempty"`
	InitialPrompt                    string                  `json:"initialPrompt,omitempty"`
	InitialPromptHash                string                  `json:"initialPromptHash,omitempty"`
	Worktree                         Effect                  `json:"worktree,omitempty"`
	HostView                         Effect                  `json:"hostView,omitempty"`
	Boundary                         Effect                  `json:"boundary,omitempty"`
	Setup                            Effect                  `json:"setup,omitempty"`
	PreparationPlan                  PreparationPlan         `json:"preparationPlan,omitempty"`
	PreparationReady                 bool                    `json:"preparationReady,omitempty"`
	PreparationBarrierHash           string                  `json:"preparationBarrierHash,omitempty"`
	Agent                            Effect                  `json:"agent,omitempty"`
	Claim                            *CompletedClaim         `json:"claim,omitempty"`
	CandidateObservation             *CandidateObservation   `json:"candidateObservation,omitempty"`
	OperationalObservation           *OperationalObservation `json:"operationalObservation,omitempty"`
	OperationalObservationRunVersion uint64                  `json:"operationalObservationRunVersion,omitempty"`
	OperationalObservationConsumed   bool                    `json:"operationalObservationConsumed,omitempty"`
	FakeTerminalRung                 bool                    `json:"fakeTerminalRung,omitempty"`
	AgentArchive                     Effect                  `json:"agentArchive,omitempty"`
	HostViewArchive                  Effect                  `json:"hostViewArchive,omitempty"`
	WorktreeRemove                   Effect                  `json:"worktreeRemove,omitempty"`
	NeedsYou                         *NeedsYou               `json:"needsYou,omitempty"`
	Terminal                         bool                    `json:"terminal,omitempty"`
}
