// SPDX-License-Identifier: Apache-2.0

package execution

import (
	"strings"

	"github.com/mcuadros/director-engine/domain/agentprofile"
	"github.com/mcuadros/director-engine/domain/runtimebudget"
)

const SchemaVersion = "director.execution/v1"

const StartupReconciliationSchemaVersion = "director.startup-reconciliation/v1"

// MaximumMCPCommandReceipts bounds the durable per-Run ingress ledger. The
// complete bounded command payload remains immutable in the TaskStore Command
// row; this ledger carries only the identities needed to prove replay and
// recovery did not append a second logical command.
const MaximumMCPCommandReceipts = 64

// EffectKind is the closed execution effect vocabulary. Host-view and agent
// operations cross the engine-owned host port; Git and boundary operations use
// engine runtime adapters.
type EffectKind string

const (
	EffectWorktreeCreate       EffectKind = "worktree.create"
	EffectHostViewCreate       EffectKind = "host_view.create"
	EffectBoundaryMaterialize  EffectKind = "rootless_oci.materialize"
	EffectSetupRun             EffectKind = "lifecycle_setup.run"
	EffectAgentCreate          EffectKind = "task_agent.create_with_bootstrap"
	EffectAgentPrompt          EffectKind = "agent.send_prompt"
	EffectHelperCheckoutCreate EffectKind = "helper_checkout.create"
	EffectHelperBoundary       EffectKind = "helper_boundary.materialize"
	EffectHelperAgentObserve   EffectKind = "helper_agent.observe"
	EffectHelperCommitHandoff  EffectKind = "helper_commit.handoff"
	EffectHelperAgentArchive   EffectKind = "helper_agent.archive"
	EffectHelperCheckoutRemove EffectKind = "helper_checkout.remove"
	EffectControlAgentBoundary EffectKind = "control_agent.observe_safe_boundary"
	EffectControlAgentArchive  EffectKind = "control_agent.archive"
	EffectRecoverySnapshot     EffectKind = "recovery.snapshot"
	EffectAgentArchive         EffectKind = "task_agent.archive"
	EffectHostViewArchive      EffectKind = "host_view.archive"
	EffectWorktreeRemove       EffectKind = "worktree.remove"
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
	ObservationErrored      ObservationStatus = "errored"
	ObservationPermission   ObservationStatus = "permission"
	ObservationDifferent    ObservationStatus = "different"
	ObservationAmbiguous    ObservationStatus = "ambiguous"
	ObservationUnavailable  ObservationStatus = "unavailable"
)

// EffectObservation is persisted before a reducer may consume it.
type EffectObservation struct {
	ID                    string                       `json:"id"`
	EffectID              string                       `json:"effectId"`
	Status                ObservationStatus            `json:"status"`
	ExternalID            string                       `json:"externalId,omitempty"`
	Cursor                uint64                       `json:"cursor,omitempty"`
	ObservedAt            string                       `json:"observedAt,omitempty"`
	ObservedAtMillis      int64                        `json:"observedAtMillis"`
	MaximumAgeMillis      int64                        `json:"maximumAgeMillis"`
	BindingHash           string                       `json:"bindingHash"`
	CorrelationHash       string                       `json:"correlationHash,omitempty"`
	PriorDispatcherAbsent bool                         `json:"priorDispatcherAbsent"`
	FactHash              string                       `json:"factHash"`
	Usage                 *runtimebudget.ProviderUsage `json:"usage,omitempty"`
}

// Effect is one immutable intent with bounded attempts and its latest
// unconsumed observation.
type Effect struct {
	ID                  string             `json:"id"`
	Kind                EffectKind         `json:"kind"`
	Phase               EffectPhase        `json:"phase"`
	Attempt             uint32             `json:"attempt"`
	AttemptLimit        uint32             `json:"attemptLimit"`
	ExternalID          string             `json:"externalId,omitempty"`
	ObservedFactHash    string             `json:"observedFactHash,omitempty"`
	ObservedCorrelation string             `json:"observedCorrelation,omitempty"`
	Observation         *EffectObservation `json:"observation,omitempty"`
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

// MCPCommandReceipt is the bounded per-Run proof that one fixed-scope MCP
// mutation reached the durable Command path. The complete canonical payload
// remains in the immutable Command row and is addressed by CommandKey.
type MCPCommandReceipt struct {
	CommandKey              string `json:"commandKey"`
	ToolName                string `json:"toolName"`
	Capability              string `json:"capability"`
	Role                    string `json:"role"`
	SessionSHA256           string `json:"sessionSha256"`
	PayloadSHA256           string `json:"payloadSha256"`
	EffectiveProfilesSHA256 string `json:"effectiveProfilesSha256"`
	ConfigurationSHA256     string `json:"configurationSha256"`
}

func validSHA256(value string) bool {
	if len(value) != 64 {
		return false
	}
	return strings.IndexFunc(value, func(character rune) bool {
		return !strings.ContainsRune("0123456789abcdef", character)
	}) < 0
}

// ValidMCPCommandReceipt verifies the closed tool/capability/role mapping and
// every immutable digest carried by the compact recovery ledger.
func ValidMCPCommandReceipt(receipt MCPCommandReceipt) bool {
	if !strings.HasPrefix(receipt.CommandKey, "mcp-command-") || len(receipt.CommandKey) != len("mcp-command-")+64 ||
		!validSHA256(strings.TrimPrefix(receipt.CommandKey, "mcp-command-")) || !validSHA256(receipt.SessionSHA256) ||
		!validSHA256(receipt.PayloadSHA256) || !validSHA256(receipt.EffectiveProfilesSHA256) ||
		!validSHA256(receipt.ConfigurationSHA256) {
		return false
	}
	switch receipt.Role {
	case "organizer":
		return receipt.ToolName == "director_planning_command_submit" && receipt.Capability == "planning.command.submit"
	case "worker":
		return (receipt.ToolName == "director_task_outcome_submit" && receipt.Capability == "task.outcome.submit") ||
			(receipt.ToolName == "director_task_helper_request" && receipt.Capability == "task.helper.request")
	case "helper":
		return receipt.ToolName == "director_helper_contribution_submit" && receipt.Capability == "helper.contribution.submit"
	case "reviewer":
		return receipt.ToolName == "director_review_verdict_submit" && receipt.Capability == "review.verdict.submit"
	default:
		return false
	}
}

// CleanupIntentFact is the bounded durable identity of one cleanup intent
// recovered at engine startup. It contains no path, command, or model text.
type CleanupIntentFact struct {
	EffectID string      `json:"effectId"`
	Kind     EffectKind  `json:"kind"`
	Phase    EffectPhase `json:"phase"`
	Attempt  uint32      `json:"attempt"`
}

// StartupReconciliation records the exact durable graph and bounded external
// identities recovered by one engine startup before effects resume. The
// command digest refers to immutable TaskStore rows; CandidateSHA is
// corroborated independently and never comes from model narration alone.
type StartupReconciliation struct {
	SchemaVersion              string              `json:"schemaVersion"`
	ID                         string              `json:"id"`
	ObservedRunVersion         uint64              `json:"observedRunVersion"`
	ObservedAtMillis           int64               `json:"observedAtMillis"`
	CommandCount               uint64              `json:"commandCount"`
	CommandChainHash           string              `json:"commandChainHash"`
	LastCommandID              string              `json:"lastCommandId"`
	OperationalObservationID   string              `json:"operationalObservationId"`
	EffectObservationCount     uint64              `json:"effectObservationCount"`
	EffectObservationChainHash string              `json:"effectObservationChainHash,omitempty"`
	WorktreeID                 string              `json:"worktreeId,omitempty"`
	WorkspaceID                string              `json:"workspaceId,omitempty"`
	AgentID                    string              `json:"agentId,omitempty"`
	HelperCount                uint64              `json:"helperCount,omitempty"`
	HelperChainHash            string              `json:"helperChainHash,omitempty"`
	CandidateID                string              `json:"candidateId,omitempty"`
	CandidateSHA               string              `json:"candidateSha,omitempty"`
	CleanupIntents             []CleanupIntentFact `json:"cleanupIntents,omitempty"`
	FrontierEffectID           string              `json:"frontierEffectId,omitempty"`
	FrontierEffectKind         EffectKind          `json:"frontierEffectKind,omitempty"`
	FrontierObservationID      string              `json:"frontierObservationId,omitempty"`
	FrontierObservationHash    string              `json:"frontierObservationHash,omitempty"`
	CandidateObservationID     string              `json:"candidateObservationId,omitempty"`
	CandidateObservationHash   string              `json:"candidateObservationHash,omitempty"`
	HostCursor                 uint64              `json:"hostCursor,omitempty"`
	FactHash                   string              `json:"factHash"`
}

// State is the durable primary execution projection stored inside its Run record.
// A zero State belongs to pre-execution TaskStore records created by older M1
// skeletons and remains valid.
type State struct {
	SchemaVersion                    string                    `json:"schemaVersion,omitempty"`
	Scope                            Scope                     `json:"scope,omitempty"`
	StartCommandID                   string                    `json:"startCommandId,omitempty"`
	EligibilityDecisionVersion       string                    `json:"eligibilityDecisionVersion,omitempty"`
	EligibilityDecisionID            string                    `json:"eligibilityDecisionId,omitempty"`
	EligibilityFactsHash             string                    `json:"eligibilityFactsHash,omitempty"`
	CapacityReservationID            string                    `json:"capacityReservationId,omitempty"`
	BudgetReservationID              string                    `json:"budgetReservationId,omitempty"`
	LeaseBinding                     LeaseBinding              `json:"leaseBinding,omitempty"`
	RepositoryBinding                RepositoryBinding         `json:"repositoryBinding,omitempty"`
	RepositoryBindingHash            string                    `json:"repositoryBindingHash,omitempty"`
	LifecycleDigest                  string                    `json:"lifecycleDigest,omitempty"`
	LifecycleApproval                *LifecycleApproval        `json:"lifecycleApproval,omitempty"`
	IsolationDigest                  string                    `json:"isolationDigest,omitempty"`
	EffectiveProfiles                *agentprofile.FrozenSet   `json:"effectiveProfiles,omitempty"`
	EffectiveProfilesSHA256          string                    `json:"effectiveProfilesSha256,omitempty"`
	Isolation                        IsolationObservation      `json:"isolation,omitempty"`
	OperationalPolicy                OperationalPolicy         `json:"operationalPolicy,omitempty"`
	LifecycleSurfaces                LifecycleSurfaces         `json:"lifecycleSurfaces,omitempty"`
	SourcePath                       string                    `json:"sourcePath,omitempty"`
	WorktreePath                     string                    `json:"worktreePath,omitempty"`
	Branch                           string                    `json:"branch,omitempty"`
	RootWorkspaceID                  string                    `json:"rootWorkspaceId,omitempty"`
	TaskTitle                        string                    `json:"taskTitle,omitempty"`
	CriterionIDs                     []string                  `json:"criterionIds,omitempty"`
	InitialPrompt                    string                    `json:"initialPrompt,omitempty"`
	InitialPromptHash                string                    `json:"initialPromptHash,omitempty"`
	PrimarySession                   PrimarySession            `json:"primarySession,omitempty"`
	HelperPolicy                     HelperPolicy              `json:"helperPolicy,omitempty"`
	Helpers                          []Helper                  `json:"helpers,omitempty"`
	Worktree                         Effect                    `json:"worktree,omitempty"`
	HostView                         Effect                    `json:"hostView,omitempty"`
	Boundary                         Effect                    `json:"boundary,omitempty"`
	Setup                            Effect                    `json:"setup,omitempty"`
	PreparationPlan                  PreparationPlan           `json:"preparationPlan,omitempty"`
	PreparationReady                 bool                      `json:"preparationReady,omitempty"`
	PreparationBarrierHash           string                    `json:"preparationBarrierHash,omitempty"`
	Agent                            Effect                    `json:"agent,omitempty"`
	AgentPrompt                      Effect                    `json:"agentPrompt,omitempty"`
	WorkerVisibility                 *WorkerVisibility         `json:"workerVisibility,omitempty"`
	LastCompletionEvent              *CompletionEvent          `json:"lastCompletionEvent,omitempty"`
	CompletionEventCursor            uint64                    `json:"completionEventCursor,omitempty"`
	CompletionEventReceipts          []CompletionEventReceipt  `json:"completionEventReceipts,omitempty"`
	MCPCommandReceipts               []MCPCommandReceipt       `json:"mcpCommandReceipts,omitempty"`
	Claim                            *CompletedClaim           `json:"claim,omitempty"`
	CandidateObservation             *CandidateObservation     `json:"candidateObservation,omitempty"`
	OperationalObservation           *OperationalObservation   `json:"operationalObservation,omitempty"`
	OperationalObservationRunVersion uint64                    `json:"operationalObservationRunVersion,omitempty"`
	OperationalObservationConsumed   bool                      `json:"operationalObservationConsumed,omitempty"`
	Budget                           runtimebudget.Ledger      `json:"budget"`
	TurnBudgetDemand                 runtimebudget.Demand      `json:"turnBudgetDemand"`
	ControlPolicy                    ControlPolicy             `json:"controlPolicy"`
	ControlledAgents                 []ControlledAgentIdentity `json:"controlledAgents,omitempty"`
	Control                          RunControl                `json:"control,omitempty"`
	FakeTerminalRung                 bool                      `json:"fakeTerminalRung,omitempty"`
	AgentArchive                     Effect                    `json:"agentArchive,omitempty"`
	HostViewArchive                  Effect                    `json:"hostViewArchive,omitempty"`
	WorktreeRemove                   Effect                    `json:"worktreeRemove,omitempty"`
	NeedsYou                         *NeedsYou                 `json:"needsYou,omitempty"`
	LastStartupReconciliation        *StartupReconciliation    `json:"lastStartupReconciliation,omitempty"`
	Terminal                         bool                      `json:"terminal,omitempty"`
}

// WorkerVisibility is the frozen root-workspace launch registration a
// Director-launched Task Agent or Reviewer must already carry before the host
// may create it. The engine owns this structural shape; the label vocabulary
// that publishes it lives in the host port, so the domain stays free of any
// Paseo naming.
type WorkerVisibility struct {
	RootWorkspaceID      string `json:"rootWorkspaceId"`
	ExecutionWorkspaceID string `json:"executionWorkspaceId"`
	AgentID              string `json:"agentId,omitempty"`
	Role                 string `json:"role"`
	Phase                string `json:"phase"`
	CandidateSHA         string `json:"candidateSha,omitempty"`
	BaseSHA              string `json:"baseSha"`
	EffectID             string `json:"effectId"`
	ProfileSHA256        string `json:"profileSha256"`
	SessionSHA256        string `json:"sessionSha256"`
	RegisteredAt         string `json:"registeredAt"`
	StartedAt            string `json:"startedAt"`
	Digest               string `json:"digest"`
	ObservedDigest       string `json:"observedDigest,omitempty"`
}

// ValidWorkerVisibility rejects a structurally incomplete registration. It
// deliberately proves nothing about the published labels: only the host port
// can decide that, and the launch reducer consumes the resulting digest.
func ValidWorkerVisibility(visibility WorkerVisibility) bool {
	return visibility.RootWorkspaceID != "" && visibility.ExecutionWorkspaceID != "" &&
		visibility.Role != "" && visibility.Phase != "" && visibility.BaseSHA != "" &&
		visibility.EffectID != "" && validSHA256(visibility.ProfileSHA256) && validSHA256(visibility.SessionSHA256) &&
		visibility.RegisteredAt != "" && visibility.StartedAt != "" && visibility.Digest != ""
}
