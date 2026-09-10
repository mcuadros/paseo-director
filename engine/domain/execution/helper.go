// SPDX-License-Identifier: Apache-2.0

package execution

import (
	"errors"
	"path/filepath"
	"slices"
	"strings"

	"github.com/mcuadros/director-engine/domain/runtimebudget"
)

const HelperSchemaVersion = "director.helper/v1"

const HelperBootstrapPrompt = "Director helper bootstrap only. Do not inspect files, call tools, or perform Task work. Finish immediately."

// HelperMode is the closed checkout-sharing vocabulary. A writer can never
// share the primary checkout; a reader can share it only through an observed
// read-only mount in the mandatory rootless-OCI boundary.
type HelperMode string

const (
	HelperWriter   HelperMode = "writer"
	HelperReadOnly HelperMode = "read_only"
)

// HelperPhase separates the Task Agent's voluntary request, engine admission,
// Task-Agent invocation, external observation, commit handoff, and cleanup.
type HelperPhase string

const (
	HelperRequested          HelperPhase = "requested"
	HelperPreparing          HelperPhase = "preparing"
	HelperAdmissionReady     HelperPhase = "admission_ready"
	HelperInvocationConsumed HelperPhase = "invocation_consumed"
	HelperActive             HelperPhase = "active"
	HelperContributionReady  HelperPhase = "contribution_ready"
	HelperHandoffReady       HelperPhase = "handoff_ready"
	HelperCleanupRequested   HelperPhase = "cleanup_requested"
	HelperTerminal           HelperPhase = "terminal"
	HelperParked             HelperPhase = "parked"
)

// HelperPolicy is frozen into the Run. MaximumPerTask is the effective Task
// quota and MaximumConcurrentAgents is the enclosing Project capacity.
type HelperPolicy struct {
	MaximumPerTask          uint32 `json:"maximumPerTask"`
	MaximumConcurrentAgents uint32 `json:"maximumConcurrentAgents"`
}

func ValidHelperPolicy(policy HelperPolicy) bool {
	return policy.MaximumPerTask > 0 && policy.MaximumPerTask <= 64 &&
		policy.MaximumConcurrentAgents > 0 && policy.MaximumPerTask < policy.MaximumConcurrentAgents
}

// HelperCapacityObservation is a finite engine observation. Active includes
// every unarchived Task Agent, Reviewer, and helper; Reserved covers durable
// admissions which do not yet have a native identity.
type HelperCapacityObservation struct {
	ID               string `json:"id"`
	ObservedAtMillis int64  `json:"observedAtMillis"`
	MaximumAgeMillis int64  `json:"maximumAgeMillis"`
	ActiveAgents     uint32 `json:"activeAgents"`
	ReservedAgents   uint32 `json:"reservedAgents"`
	FactHash         string `json:"factHash"`
}

func HelperCapacityObservationHash(observation HelperCapacityObservation) string {
	observation.FactHash = ""
	return primaryDigest(observation)
}

func CurrentHelperCapacityObservation(observation HelperCapacityObservation, policy HelperPolicy, nowMillis int64) bool {
	if !ValidHelperPolicy(policy) || !primaryIdentityPattern.MatchString(observation.ID) ||
		observation.ObservedAtMillis < 0 || observation.MaximumAgeMillis <= 0 ||
		nowMillis < observation.ObservedAtMillis || nowMillis-observation.ObservedAtMillis > observation.MaximumAgeMillis ||
		observation.FactHash != HelperCapacityObservationHash(observation) {
		return false
	}
	return true
}

func HelperCapacityAvailable(observation HelperCapacityObservation, policy HelperPolicy, nowMillis int64) bool {
	if !CurrentHelperCapacityObservation(observation, policy, nowMillis) {
		return false
	}
	total := uint64(observation.ActiveAgents) + uint64(observation.ReservedAgents) + 1
	return total <= uint64(policy.MaximumConcurrentAgents)
}

func HelperCapacityWithinLimit(observation HelperCapacityObservation, policy HelperPolicy, nowMillis int64) bool {
	if !CurrentHelperCapacityObservation(observation, policy, nowMillis) {
		return false
	}
	return uint64(observation.ActiveAgents)+uint64(observation.ReservedAgents) <= uint64(policy.MaximumConcurrentAgents)
}

// HelperBoundaryObservation proves the applicable ADR-0014 boundary for the
// exact helper. The credential/control booleans are deliberately positive
// exclusion facts: omission is not interpreted as safety.
type HelperBoundaryObservation struct {
	ID                         string               `json:"id"`
	HelperID                   string               `json:"helperId"`
	Mode                       HelperMode           `json:"mode"`
	ObservedAtMillis           int64                `json:"observedAtMillis"`
	MaximumAgeMillis           int64                `json:"maximumAgeMillis"`
	Isolation                  IsolationObservation `json:"isolation"`
	TrustedRuntime             bool                 `json:"trustedRuntime"`
	LifecycleDigest            string               `json:"lifecycleDigest"`
	SeparateCheckout           bool                 `json:"separateCheckout"`
	PrimaryCheckoutReadOnly    bool                 `json:"primaryCheckoutReadOnly"`
	PrimaryCheckoutUnavailable bool                 `json:"primaryCheckoutUnavailable"`
	EngineStateAbsent          bool                 `json:"engineStateAbsent"`
	TaskStoreCredentialAbsent  bool                 `json:"taskStoreCredentialAbsent"`
	DeliveryCredentialAbsent   bool                 `json:"deliveryCredentialAbsent"`
	RawControlAbsent           bool                 `json:"rawControlAbsent"`
	ProviderCredentialOnly     bool                 `json:"providerCredentialOnly"`
	ProviderPolicyApplied      bool                 `json:"providerPolicyApplied"`
	ProviderPolicyReadOnly     bool                 `json:"providerPolicyReadOnly"`
	OperationalTelemetryReady  bool                 `json:"operationalTelemetryReady"`
	FactHash                   string               `json:"factHash"`
}

func HelperBoundaryObservationHash(observation HelperBoundaryObservation) string {
	observation.FactHash = ""
	return primaryDigest(observation)
}

func CurrentHelperBoundary(observation HelperBoundaryObservation, helper Helper, state State, nowMillis int64) bool {
	if !primaryIdentityPattern.MatchString(observation.ID) || observation.HelperID != helper.ID ||
		observation.Mode != helper.Mode || observation.ObservedAtMillis < 0 || observation.MaximumAgeMillis <= 0 ||
		nowMillis < observation.ObservedAtMillis || nowMillis-observation.ObservedAtMillis > observation.MaximumAgeMillis ||
		observation.FactHash != HelperBoundaryObservationHash(observation) ||
		observation.LifecycleDigest != state.LifecycleDigest || !observation.TrustedRuntime ||
		AdmitIsolation(observation.Isolation).Kind != AdmissionAllow ||
		!observation.EngineStateAbsent || !observation.TaskStoreCredentialAbsent ||
		!observation.DeliveryCredentialAbsent || !observation.RawControlAbsent ||
		!observation.ProviderCredentialOnly || !observation.ProviderPolicyApplied || !observation.OperationalTelemetryReady {
		return false
	}
	if helper.Mode == HelperWriter {
		return observation.SeparateCheckout && observation.PrimaryCheckoutUnavailable &&
			!observation.PrimaryCheckoutReadOnly && !observation.ProviderPolicyReadOnly
	}
	return helper.Mode == HelperReadOnly && !observation.SeparateCheckout &&
		observation.PrimaryCheckoutReadOnly && !observation.PrimaryCheckoutUnavailable && observation.ProviderPolicyReadOnly
}

// HelperAdmission is the one-use parent-bound grant returned to the Task
// Agent's admitted orchestration mechanism. It is not an engine launch.
type HelperAdmission struct {
	ID                   string          `json:"id"`
	HelperID             string          `json:"helperId"`
	ParentAgentID        string          `json:"parentAgentId"`
	ExecutionWorkspaceID string          `json:"executionWorkspaceId"`
	Mode                 HelperMode      `json:"mode"`
	ProfileSHA256        string          `json:"profileSha256"`
	SessionSHA256        string          `json:"sessionSha256"`
	Role                 string          `json:"role"`
	Provider             string          `json:"provider"`
	Model                string          `json:"model"`
	Effort               string          `json:"effort"`
	ProviderMode         string          `json:"providerMode"`
	PermissionMode       string          `json:"permissionMode"`
	MCPContractVersion   string          `json:"mcpContractVersion"`
	MCPContractSHA256    string          `json:"mcpContractSha256"`
	MCPTools             []string        `json:"mcpTools"`
	MCPServer            MCPServerLaunch `json:"mcpServer"`
	BootstrapPrompt      string          `json:"bootstrapPrompt"`
	BootstrapMessageID   string          `json:"bootstrapMessageId"`
	LabelDigest          string          `json:"labelDigest"`
	RegisteredAt         string          `json:"registeredAt"`
	StartedAt            string          `json:"startedAt"`
	IssuedAtMillis       int64           `json:"issuedAtMillis"`
	ConsumedAtMillis     int64           `json:"consumedAtMillis,omitempty"`
	InvocationRequestID  string          `json:"invocationRequestId,omitempty"`
}

// HelperContribution is a bounded helper claim until the engine observes the
// exact private checkout and commit. It never makes the helper the Candidate
// producer of record.
type HelperContribution struct {
	CommitSHA string `json:"commitSha"`
	BaseSHA   string `json:"baseSha"`
}

type HelperContributionObservation struct {
	ID                       string `json:"id"`
	HelperID                 string `json:"helperId"`
	CommitSHA                string `json:"commitSha"`
	BaseSHA                  string `json:"baseSha"`
	ObservedAtMillis         int64  `json:"observedAtMillis"`
	MaximumAgeMillis         int64  `json:"maximumAgeMillis"`
	CheckoutID               string `json:"checkoutId"`
	BindingHash              string `json:"bindingHash"`
	Clean                    bool   `json:"clean"`
	Reachable                bool   `json:"reachable"`
	Owned                    bool   `json:"owned"`
	DescendsFromBase         bool   `json:"descendsFromBase"`
	PrimaryCheckoutUnchanged bool   `json:"primaryCheckoutUnchanged"`
	PrivateObjectStore       bool   `json:"privateObjectStore"`
	FactHash                 string `json:"factHash"`
}

func HelperContributionObservationHash(observation HelperContributionObservation) string {
	observation.FactHash = ""
	return primaryDigest(observation)
}

func CurrentHelperContribution(observation HelperContributionObservation, helper Helper, state State, nowMillis int64) bool {
	return helper.Contribution != nil && observation.HelperID == helper.ID &&
		observation.CommitSHA == helper.Contribution.CommitSHA && observation.BaseSHA == helper.Contribution.BaseSHA &&
		observation.BaseSHA == state.RepositoryBinding.BaseSHA && observation.CheckoutID == helper.Checkout.ExternalID &&
		observation.BindingHash == state.RepositoryBindingHash && observation.ObservedAtMillis >= 0 &&
		observation.MaximumAgeMillis > 0 && nowMillis >= observation.ObservedAtMillis &&
		nowMillis-observation.ObservedAtMillis <= observation.MaximumAgeMillis &&
		observation.Clean && observation.Reachable && observation.Owned && observation.DescendsFromBase &&
		observation.PrimaryCheckoutUnchanged && observation.PrivateObjectStore &&
		observation.FactHash == HelperContributionObservationHash(observation)
}

type HelperHandoff struct {
	CommitSHA        string `json:"commitSha"`
	ObservationID    string `json:"observationId"`
	ImportedFactHash string `json:"importedFactHash"`
}

// Helper is one durable child attributed to exactly one Run and primary Task
// Agent. Its effects are replayed independently and never become Task effects.
type Helper struct {
	SchemaVersion           string                         `json:"schemaVersion"`
	ID                      string                         `json:"id"`
	RequestID               string                         `json:"requestId"`
	Scope                   Scope                          `json:"scope"`
	ParentAgentID           string                         `json:"parentAgentId"`
	Mode                    HelperMode                     `json:"mode"`
	Purpose                 string                         `json:"purpose"`
	Title                   string                         `json:"title"`
	WorktreePath            string                         `json:"worktreePath"`
	Phase                   HelperPhase                    `json:"phase"`
	CapacityReservationID   string                         `json:"capacityReservationId"`
	BudgetEffectID          string                         `json:"budgetEffectId"`
	BudgetReservationID     string                         `json:"budgetReservationId,omitempty"`
	BudgetEvidenceID        string                         `json:"budgetEvidenceId,omitempty"`
	CapacityObservation     *HelperCapacityObservation     `json:"capacityObservation,omitempty"`
	BoundaryObservation     *HelperBoundaryObservation     `json:"boundaryObservation,omitempty"`
	Checkout                Effect                         `json:"checkout,omitempty"`
	Boundary                Effect                         `json:"boundary,omitempty"`
	AgentObservation        Effect                         `json:"agentObservation,omitempty"`
	Admission               *HelperAdmission               `json:"admission,omitempty"`
	NativeAgentID           string                         `json:"nativeAgentId,omitempty"`
	Contribution            *HelperContribution            `json:"contribution,omitempty"`
	ContributionObservation *HelperContributionObservation `json:"contributionObservation,omitempty"`
	HandoffEffect           Effect                         `json:"handoffEffect,omitempty"`
	Handoff                 *HelperHandoff                 `json:"handoff,omitempty"`
	Archive                 Effect                         `json:"archive,omitempty"`
	CheckoutRemove          Effect                         `json:"checkoutRemove,omitempty"`
	NeedsYou                *NeedsYou                      `json:"needsYou,omitempty"`
}

func cloneHelper(helper Helper) Helper {
	if helper.CapacityObservation != nil {
		value := *helper.CapacityObservation
		helper.CapacityObservation = &value
	}
	if helper.BoundaryObservation != nil {
		value := *helper.BoundaryObservation
		helper.BoundaryObservation = &value
	}
	if helper.Admission != nil {
		value := *helper.Admission
		value.MCPTools = slices.Clone(helper.Admission.MCPTools)
		value.MCPServer.Args = slices.Clone(helper.Admission.MCPServer.Args)
		value.MCPServer.Env = make(map[string]string, len(helper.Admission.MCPServer.Env))
		for name, item := range helper.Admission.MCPServer.Env {
			value.MCPServer.Env[name] = item
		}
		helper.Admission = &value
	}
	if helper.Contribution != nil {
		value := *helper.Contribution
		helper.Contribution = &value
	}
	if helper.ContributionObservation != nil {
		value := *helper.ContributionObservation
		helper.ContributionObservation = &value
	}
	if helper.Handoff != nil {
		value := *helper.Handoff
		helper.Handoff = &value
	}
	if helper.NeedsYou != nil {
		value := *helper.NeedsYou
		helper.NeedsYou = &value
	}
	return helper
}

func CloneHelpers(values []Helper) []Helper {
	result := make([]Helper, len(values))
	for index, helper := range values {
		result[index] = cloneHelper(helper)
	}
	return result
}

// HelperGraphHash is the path/content-free startup identity of every durable
// helper record. The Run retains the records; startup evidence stores only
// their count and this digest.
func HelperGraphHash(values []Helper) string {
	if len(values) == 0 {
		return ""
	}
	return primaryDigest(values)
}

func helperActive(helper Helper) bool { return helper.Phase != HelperTerminal }

func HelperIndex(values []Helper, id string) int {
	return slices.IndexFunc(values, func(helper Helper) bool { return helper.ID == id })
}

// HelperWorktreePath derives a writer-only checkout path without accepting a
// model-selected path. Read-only helpers intentionally receive the primary
// path because their boundary admission separately proves it is read-only.
func HelperWorktreePath(primaryPath, helperID string, mode HelperMode) string {
	if !filepath.IsAbs(primaryPath) || filepath.Clean(primaryPath) != primaryPath ||
		!primaryIdentityPattern.MatchString(helperID) {
		return ""
	}
	if mode == HelperReadOnly {
		return primaryPath
	}
	if mode != HelperWriter {
		return ""
	}
	return filepath.Join(filepath.Dir(primaryPath), "."+filepath.Base(primaryPath)+"-helpers", helperID)
}

// AppendHelperRequest records a voluntary request without deciding admission.
// The caller supplies an engine-derived path; model-facing input has no path.
func AppendHelperRequest(state State, helperID, requestID, parentID string, mode HelperMode, purpose, worktreePath string) (State, error) {
	if !ValidHelperPolicy(state.HelperPolicy) || !validScope(state.Scope) ||
		!primaryIdentityPattern.MatchString(helperID) || !primaryIdentityPattern.MatchString(requestID) ||
		parentID == "" || parentID != state.PrimarySession.NativeAgentID ||
		(mode != HelperWriter && mode != HelperReadOnly) || !boundedPrimaryValue(purpose, 2_048) ||
		!filepath.IsAbs(worktreePath) || filepath.Clean(worktreePath) != worktreePath {
		return state, errors.New("helper request is invalid")
	}
	if existing := HelperIndex(state.Helpers, helperID); existing >= 0 {
		helper := state.Helpers[existing]
		if helper.RequestID == requestID && helper.ParentAgentID == parentID && helper.Mode == mode &&
			helper.Purpose == purpose && helper.WorktreePath == worktreePath {
			return state, nil
		}
		return state, errors.New("helper request identity conflicts")
	}
	active := 0
	for _, helper := range state.Helpers {
		if helperActive(helper) {
			active++
		}
	}
	if active >= int(state.HelperPolicy.MaximumPerTask) {
		return state, errors.New(string(NeedHelperQuota))
	}
	if mode == HelperWriter {
		if state.PrimarySession.PermissionMode != "workspace-write" {
			return state, errors.New("writer helper requires the frozen workspace-write provider policy")
		}
		if worktreePath == state.WorktreePath || strings.HasPrefix(worktreePath+string(filepath.Separator), state.WorktreePath+string(filepath.Separator)) {
			return state, errors.New("writer helper checkout overlaps primary checkout")
		}
	} else if worktreePath != state.WorktreePath {
		return state, errors.New("read-only helper must share the primary checkout")
	}
	next := state
	next.Helpers = CloneHelpers(state.Helpers)
	next.Helpers = append(next.Helpers, Helper{
		SchemaVersion: HelperSchemaVersion, ID: helperID, RequestID: requestID, Scope: state.Scope,
		ParentAgentID: parentID, Mode: mode, Purpose: purpose, Title: "Helper " + helperID, WorktreePath: worktreePath,
		Phase: HelperRequested, CapacityReservationID: "helper-capacity-" + helperID,
		BudgetEffectID: "helper-turn-" + helperID,
	})
	return next, nil
}

func ConsumeHelperAdmission(helper Helper, requestID string, nowMillis int64) (Helper, error) {
	if helper.Phase != HelperAdmissionReady || helper.Admission == nil || helper.Admission.ConsumedAtMillis != 0 ||
		!primaryIdentityPattern.MatchString(requestID) || nowMillis < helper.Admission.IssuedAtMillis {
		return helper, errors.New("helper admission cannot be consumed")
	}
	next := cloneHelper(helper)
	next.Admission.ConsumedAtMillis = nowMillis
	next.Admission.InvocationRequestID = requestID
	next.Phase = HelperInvocationConsumed
	return next, nil
}

func validHelperPhase(phase HelperPhase) bool {
	switch phase {
	case HelperRequested, HelperPreparing, HelperAdmissionReady, HelperInvocationConsumed,
		HelperActive, HelperContributionReady, HelperHandoffReady, HelperCleanupRequested,
		HelperTerminal, HelperParked:
		return true
	default:
		return false
	}
}

func validHelperEffect(effect Effect, kind EffectKind, optional bool) bool {
	if effect.ID == "" {
		return optional
	}
	return effect.Kind == kind && effect.AttemptLimit > 0 && effect.Attempt <= effect.AttemptLimit &&
		(effect.Phase == EffectIntentRecorded || effect.Phase == EffectDispatching || effect.Phase == EffectComplete)
}

// ValidHelpers is the TaskStore ingress guard for the complete durable helper
// graph. It rejects cross-Run parentage, overlapping writer paths, partial
// admissions, and effect-kind substitution after a crash.
func ValidHelpers(state State) bool {
	if len(state.Helpers) == 0 {
		return state.HelperPolicy == (HelperPolicy{}) || ValidHelperPolicy(state.HelperPolicy)
	}
	if !ValidHelperPolicy(state.HelperPolicy) || len(state.Helpers) > 64 {
		return false
	}
	activeHelpers := 0
	seenIDs := make(map[string]struct{}, len(state.Helpers))
	seenRequests := make(map[string]struct{}, len(state.Helpers))
	for _, helper := range state.Helpers {
		if helperActive(helper) {
			activeHelpers++
		}
		if helper.SchemaVersion != HelperSchemaVersion || !primaryIdentityPattern.MatchString(helper.ID) ||
			!primaryIdentityPattern.MatchString(helper.RequestID) || helper.Scope != state.Scope ||
			helper.ParentAgentID == "" || helper.ParentAgentID != state.PrimarySession.NativeAgentID ||
			!validHelperPhase(helper.Phase) || !boundedPrimaryValue(helper.Purpose, 2_048) ||
			!boundedPrimaryValue(helper.Title, 512) || helper.WorktreePath != HelperWorktreePath(state.WorktreePath, helper.ID, helper.Mode) ||
			helper.CapacityReservationID != "helper-capacity-"+helper.ID ||
			helper.BudgetEffectID != "helper-turn-"+helper.ID ||
			(helper.BudgetReservationID != "" && !primaryIdentityPattern.MatchString(helper.BudgetReservationID)) ||
			(helper.BudgetEvidenceID != "" && !primaryIdentityPattern.MatchString(helper.BudgetEvidenceID)) ||
			!validHelperEffect(helper.Checkout, EffectHelperCheckoutCreate, helper.Mode == HelperReadOnly || helper.Phase == HelperRequested || helper.Phase == HelperParked) ||
			!validHelperEffect(helper.Boundary, EffectHelperBoundary, helper.Phase == HelperRequested || helper.Phase == HelperParked) ||
			!validHelperEffect(helper.AgentObservation, EffectHelperAgentObserve, helper.Phase == HelperRequested || helper.Phase == HelperParked) ||
			!validHelperEffect(helper.HandoffEffect, EffectHelperCommitHandoff, true) ||
			!validHelperEffect(helper.Archive, EffectHelperAgentArchive, true) ||
			!validHelperEffect(helper.CheckoutRemove, EffectHelperCheckoutRemove, true) {
			return false
		}
		if _, duplicate := seenIDs[helper.ID]; duplicate {
			return false
		}
		if _, duplicate := seenRequests[helper.RequestID]; duplicate {
			return false
		}
		seenIDs[helper.ID] = struct{}{}
		seenRequests[helper.RequestID] = struct{}{}
		if helper.Admission != nil {
			if helper.Admission.HelperID != helper.ID || helper.Admission.ParentAgentID != helper.ParentAgentID ||
				helper.Admission.ExecutionWorkspaceID != state.HostView.ExternalID || helper.Admission.Mode != helper.Mode ||
				helper.Admission.ProfileSHA256 != state.EffectiveProfilesSHA256 || !validSHA256(helper.Admission.SessionSHA256) ||
				!validSHA256(helper.Admission.LabelDigest) || helper.Admission.Role != "helper" ||
				helper.Admission.Provider != string(state.PrimarySession.Provider) || helper.Admission.Model != state.PrimarySession.Model ||
				helper.Admission.Effort != state.PrimarySession.Effort || helper.Admission.ProviderMode != state.PrimarySession.Mode ||
				(helper.Admission.PermissionMode != "read-only" && helper.Admission.PermissionMode != "workspace-write") ||
				helper.Admission.BootstrapPrompt != HelperBootstrapPrompt || !primaryIdentityPattern.MatchString(helper.Admission.BootstrapMessageID) ||
				!validMCPServer(helper.Admission.MCPServer) || primaryDigest(helper.Admission.MCPServer) != primaryDigest(state.PrimarySession.MCPServer) ||
				helper.Admission.MCPContractVersion != state.PrimarySession.MCPContractVersion ||
				helper.Admission.MCPContractSHA256 != state.PrimarySession.MCPContractSHA256 ||
				!validSHA256(helper.Admission.MCPContractSHA256) || !slices.Equal(helper.Admission.MCPTools, []string{"director_helper_contribution_submit", "director_task_read"}) ||
				helper.Admission.RegisteredAt == "" || helper.Admission.StartedAt == "" ||
				helper.Admission.IssuedAtMillis < 0 || (helper.Admission.ConsumedAtMillis != 0 &&
				(helper.Admission.ConsumedAtMillis < helper.Admission.IssuedAtMillis || helper.Admission.InvocationRequestID == "")) {
				return false
			}
			if helper.Mode == HelperReadOnly && helper.Admission.PermissionMode != "read-only" {
				return false
			}
			if helper.Mode == HelperWriter && helper.Admission.PermissionMode != state.PrimarySession.PermissionMode {
				return false
			}
		}
		if helper.Admission != nil && helper.Admission.ConsumedAtMillis != 0 && helper.BudgetReservationID == "" {
			return false
		}
		if helper.BudgetReservationID != "" {
			budgetFound := false
			for _, reservation := range state.Budget.Reservations {
				if reservation.ID != helper.BudgetReservationID {
					continue
				}
				if reservation.EffectID != helper.BudgetEffectID || reservation.Activity != runtimebudget.ActivityHelperTurn ||
					reservation.LeaseEpoch != state.LeaseBinding.Epoch || reservation.PolicyRevision != state.Budget.Policy.Revision ||
					reservation.Demand != state.TurnBudgetDemand ||
					(reservation.Released && reservation.EvidenceID != helper.BudgetEvidenceID) ||
					(!reservation.Released && helper.BudgetEvidenceID != "") {
					return false
				}
				budgetFound = true
				break
			}
			if !budgetFound {
				return false
			}
		}
		if helper.NativeAgentID != "" && (helper.Admission == nil || helper.Admission.ConsumedAtMillis == 0) {
			return false
		}
		if helper.Contribution != nil && (helper.Mode != HelperWriter || !primaryGitOIDPattern.MatchString(helper.Contribution.CommitSHA) || helper.Contribution.BaseSHA != state.RepositoryBinding.BaseSHA) {
			return false
		}
		if helper.Handoff != nil && (helper.Contribution == nil || helper.Handoff.CommitSHA != helper.Contribution.CommitSHA || helper.Handoff.ObservationID == "" || !validSHA256(helper.Handoff.ImportedFactHash)) {
			return false
		}
		if helper.NeedsYou != nil && (helper.NeedsYou.Code == "" || helper.NeedsYou.WakeCondition == "" || helper.NeedsYou.CleanupAuthorized) {
			return false
		}
	}
	return activeHelpers <= int(state.HelperPolicy.MaximumPerTask)
}
