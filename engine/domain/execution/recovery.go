// SPDX-License-Identifier: Apache-2.0

package execution

import (
	"errors"
	"slices"
	"strings"
)

const (
	PrimaryRecoverySchemaVersion = "director.primary-recovery/v1"
	RecoveryPolicySchemaVersion  = "director.primary-recovery-policy/v1"
)

// RecoveryTrigger is a wake cause, never evidence that a worker failed.
type RecoveryTrigger string

const (
	RecoveryProviderFailure      RecoveryTrigger = "provider_failure"
	RecoveryPluginReload         RecoveryTrigger = "plugin_reload"
	RecoveryDaemonInterruption   RecoveryTrigger = "daemon_interruption"
	RecoveryTerminalCallbackLost RecoveryTrigger = "terminal_callback_lost"
	RecoveryLeaseTakeover        RecoveryTrigger = "lease_takeover"
	RecoveryCoordinatorRestart   RecoveryTrigger = "coordinator_restart"
)

// ProviderFailureSignal is the closed, redacted host vocabulary. A connector
// may translate public host output into these values, but only the engine maps
// them to replacement policy.
type ProviderFailureSignal string

const (
	ProviderFailureNone           ProviderFailureSignal = "none"
	ProviderFailureTerminal       ProviderFailureSignal = "provider_terminal"
	ProviderFailurePolicy         ProviderFailureSignal = "policy_rejection"
	ProviderFailureAuthentication ProviderFailureSignal = "authentication_rejection"
	ProviderFailureConfiguration  ProviderFailureSignal = "configuration_rejection"
	ProviderFailureTransient      ProviderFailureSignal = "transient_service"
	ProviderFailureUnknown        ProviderFailureSignal = "unclassified"
)

type ProviderFailureClass string

const (
	FailureClassNone                   ProviderFailureClass = "none"
	FailureClassTransient              ProviderFailureClass = "transient"
	FailureClassTerminalProvider       ProviderFailureClass = "terminal_provider"
	FailureClassTerminalPolicy         ProviderFailureClass = "terminal_policy"
	FailureClassTerminalAuthentication ProviderFailureClass = "terminal_authentication"
	FailureClassTerminalConfiguration  ProviderFailureClass = "terminal_configuration"
	FailureClassAmbiguous              ProviderFailureClass = "ambiguous"
)

// PrimaryRecoveryPolicy is frozen into each Run. The one replacement ceiling
// is fixed by PLAN. Authentication and configuration failures require a human;
// a new persistent session cannot silently change either authority.
type PrimaryRecoveryPolicy struct {
	SchemaVersion                   string `json:"schemaVersion"`
	ReplacementLimit                uint32 `json:"replacementLimit"`
	PoisonedSessionFailureThreshold uint32 `json:"poisonedSessionFailureThreshold"`
	ReplaceTerminalProvider         bool   `json:"replaceTerminalProvider"`
	ReplaceTerminalPolicy           bool   `json:"replaceTerminalPolicy"`
	ReplaceTerminalAuthentication   bool   `json:"replaceTerminalAuthentication"`
	ReplaceTerminalConfiguration    bool   `json:"replaceTerminalConfiguration"`
}

func DefaultPrimaryRecoveryPolicy() PrimaryRecoveryPolicy {
	return PrimaryRecoveryPolicy{
		SchemaVersion: RecoveryPolicySchemaVersion, ReplacementLimit: 1,
		PoisonedSessionFailureThreshold: 2,
		ReplaceTerminalProvider:         true,
		ReplaceTerminalPolicy:           true,
		ReplaceTerminalAuthentication:   false,
		ReplaceTerminalConfiguration:    false,
	}
}

func ValidPrimaryRecoveryPolicy(policy PrimaryRecoveryPolicy) bool {
	return policy.SchemaVersion == RecoveryPolicySchemaVersion && policy.ReplacementLimit == 1 &&
		policy.PoisonedSessionFailureThreshold == 2 && policy.ReplaceTerminalProvider &&
		policy.ReplaceTerminalPolicy && !policy.ReplaceTerminalAuthentication &&
		!policy.ReplaceTerminalConfiguration
}

func validFailureSignal(signal ProviderFailureSignal) bool {
	return slices.Contains([]ProviderFailureSignal{
		ProviderFailureNone, ProviderFailureTerminal, ProviderFailurePolicy,
		ProviderFailureAuthentication, ProviderFailureConfiguration,
		ProviderFailureTransient, ProviderFailureUnknown,
	}, signal)
}

// ClassifyProviderFailure rejects contradictory signals instead of selecting
// whichever happens to be listed first.
func ClassifyProviderFailure(signals []ProviderFailureSignal) ProviderFailureClass {
	if len(signals) == 0 {
		return FailureClassNone
	}
	unique := make(map[ProviderFailureSignal]struct{}, len(signals))
	for _, signal := range signals {
		if !validFailureSignal(signal) {
			return FailureClassAmbiguous
		}
		unique[signal] = struct{}{}
	}
	delete(unique, ProviderFailureNone)
	if len(unique) == 0 {
		return FailureClassNone
	}
	if len(unique) != 1 {
		return FailureClassAmbiguous
	}
	for signal := range unique {
		switch signal {
		case ProviderFailureTransient:
			return FailureClassTransient
		case ProviderFailureTerminal:
			return FailureClassTerminalProvider
		case ProviderFailurePolicy:
			return FailureClassTerminalPolicy
		case ProviderFailureAuthentication:
			return FailureClassTerminalAuthentication
		case ProviderFailureConfiguration:
			return FailureClassTerminalConfiguration
		default:
			return FailureClassAmbiguous
		}
	}
	return FailureClassAmbiguous
}

func (policy PrimaryRecoveryPolicy) Allows(class ProviderFailureClass) bool {
	switch class {
	case FailureClassTerminalProvider:
		return policy.ReplaceTerminalProvider
	case FailureClassTerminalPolicy:
		return policy.ReplaceTerminalPolicy
	case FailureClassTerminalAuthentication:
		return policy.ReplaceTerminalAuthentication
	case FailureClassTerminalConfiguration:
		return policy.ReplaceTerminalConfiguration
	default:
		return false
	}
}

// NativeAgentRecoveryFact contains only bounded lifecycle facts. Raw provider
// output, prompts, paths, environment, credentials, and conversation history
// have no representation.
type NativeAgentRecoveryFact struct {
	AgentID                     string                  `json:"agentId"`
	WorkspaceID                 string                  `json:"workspaceId"`
	Role                        ControlledAgentRole     `json:"role"`
	EffectID                    string                  `json:"effectId"`
	Status                      string                  `json:"status"`
	ActiveTurnPresent           bool                    `json:"activeTurnPresent"`
	ArchivedAtPresent           bool                    `json:"archivedAtPresent"`
	ParentPresent               bool                    `json:"parentPresent"`
	TitleExact                  bool                    `json:"titleExact"`
	WorktreeExact               bool                    `json:"worktreeExact"`
	LabelsRunExact              bool                    `json:"labelsRunExact"`
	ProfileExact                bool                    `json:"profileExact"`
	SessionExact                bool                    `json:"sessionExact"`
	BootstrapPresent            bool                    `json:"bootstrapPresent"`
	PromptPresent               bool                    `json:"promptPresent"`
	PersistenceReferencePresent bool                    `json:"persistenceReferencePresent"`
	FailureSignals              []ProviderFailureSignal `json:"failureSignals"`
}

type NativeWorkspaceRecoveryFact struct {
	WorkspaceID   string `json:"workspaceId"`
	Active        bool   `json:"active"`
	Archived      bool   `json:"archived"`
	WorktreeExact bool   `json:"worktreeExact"`
	TitleExact    bool   `json:"titleExact"`
	KindExact     bool   `json:"kindExact"`
}

// PrimaryRecoveryInventory is one complete bounded host scan for the Run.
type PrimaryRecoveryInventory struct {
	Complete   bool                          `json:"complete"`
	Workspaces []NativeWorkspaceRecoveryFact `json:"workspaces"`
	Agents     []NativeAgentRecoveryFact     `json:"agents"`
}

type RelatedWorktreeRecoveryFact struct {
	WorktreeID  string `json:"worktreeId"`
	BindingHash string `json:"bindingHash"`
	Owned       bool   `json:"owned"`
	ExactRun    bool   `json:"exactRun"`
	Active      bool   `json:"active"`
}

// PrimaryRuntimeRecoveryObservation carries the Git/worktree/process side of
// recovery. It is produced by an engine runtime adapter, not by the connector.
type PrimaryRuntimeRecoveryObservation struct {
	ID                           string                        `json:"id"`
	ObservedAtMillis             int64                         `json:"observedAtMillis"`
	MaximumAgeMillis             int64                         `json:"maximumAgeMillis"`
	BindingHash                  string                        `json:"bindingHash"`
	RepositoryExact              bool                          `json:"repositoryExact"`
	WorktreePresent              bool                          `json:"worktreePresent"`
	WorktreeExact                bool                          `json:"worktreeExact"`
	BranchExact                  bool                          `json:"branchExact"`
	BaseExact                    bool                          `json:"baseExact"`
	OriginalAgentProcessAbsent   bool                          `json:"originalAgentProcessAbsent"`
	PriorEngineAndDispatchAbsent bool                          `json:"priorEngineAndDispatchAbsent"`
	CandidatePresent             bool                          `json:"candidatePresent"`
	CandidateSHA                 string                        `json:"candidateSha,omitempty"`
	CandidateExact               bool                          `json:"candidateExact"`
	RelatedWorktrees             []RelatedWorktreeRecoveryFact `json:"relatedWorktrees"`
	FactHash                     string                        `json:"factHash"`
}

func PrimaryRuntimeRecoveryObservationHash(observation PrimaryRuntimeRecoveryObservation) string {
	observation.FactHash = ""
	return primaryDigest(observation)
}

func ValidPrimaryRuntimeRecoveryObservation(observation PrimaryRuntimeRecoveryObservation) bool {
	if !primaryIdentityPattern.MatchString(observation.ID) || observation.ObservedAtMillis < 0 ||
		observation.MaximumAgeMillis <= 0 || !primarySHA256Pattern.MatchString(observation.BindingHash) ||
		(observation.CandidateSHA != "" && !primaryGitOIDPattern.MatchString(observation.CandidateSHA)) ||
		observation.CandidatePresent != (observation.CandidateSHA != "") ||
		observation.FactHash != PrimaryRuntimeRecoveryObservationHash(observation) || len(observation.RelatedWorktrees) > 64 {
		return false
	}
	seen := make(map[string]struct{}, len(observation.RelatedWorktrees))
	for _, worktree := range observation.RelatedWorktrees {
		if !primaryIdentityPattern.MatchString(worktree.WorktreeID) || !primarySHA256Pattern.MatchString(worktree.BindingHash) {
			return false
		}
		if _, duplicate := seen[worktree.WorktreeID]; duplicate {
			return false
		}
		seen[worktree.WorktreeID] = struct{}{}
	}
	return true
}

type PrimaryRecoveryObservation struct {
	ID                 string                            `json:"id"`
	ObservedRunVersion uint64                            `json:"observedRunVersion"`
	Host               EffectObservation                 `json:"host"`
	Runtime            PrimaryRuntimeRecoveryObservation `json:"runtime"`
	FactHash           string                            `json:"factHash"`
}

func PrimaryRecoveryObservationHash(observation PrimaryRecoveryObservation) string {
	observation.FactHash = ""
	return primaryDigest(observation)
}

func ValidPrimaryRecoveryObservation(observation PrimaryRecoveryObservation) bool {
	return primaryIdentityPattern.MatchString(observation.ID) && observation.ObservedRunVersion > 0 &&
		ValidEffectObservation(observation.Host) && observation.Host.Inventory != nil &&
		ValidPrimaryRuntimeRecoveryObservation(observation.Runtime) &&
		observation.Host.BindingHash == observation.Runtime.BindingHash &&
		observation.FactHash == PrimaryRecoveryObservationHash(observation)
}

type PrimaryRecoveryPhase string

const (
	PrimaryRecoveryIntentRecorded       PrimaryRecoveryPhase = "intent_recorded"
	PrimaryRecoveryObserved             PrimaryRecoveryPhase = "observed"
	PrimaryRecoveryArchiving            PrimaryRecoveryPhase = "archiving_original"
	PrimaryRecoveryAuthorityConsumed    PrimaryRecoveryPhase = "replacement_authority_consumed"
	PrimaryRecoveryCreatingReplacement  PrimaryRecoveryPhase = "creating_replacement"
	PrimaryRecoveryBindingReplacement   PrimaryRecoveryPhase = "binding_replacement"
	PrimaryRecoveryPromptingReplacement PrimaryRecoveryPhase = "prompting_replacement"
	PrimaryRecoveryComplete             PrimaryRecoveryPhase = "complete"
	PrimaryRecoveryNeedsYou             PrimaryRecoveryPhase = "needs_you"
)

type ReplacementAuthority struct {
	ID                     string               `json:"id"`
	AuthorizedRunVersion   uint64               `json:"authorizedRunVersion"`
	ConsumedRunVersion     uint64               `json:"consumedRunVersion"`
	LeaseEpoch             uint64               `json:"leaseEpoch"`
	OldAgentID             string               `json:"oldAgentId"`
	OldWorkspaceID         string               `json:"oldWorkspaceId"`
	FailureClass           ProviderFailureClass `json:"failureClass"`
	FailureObservationID   string               `json:"failureObservationId"`
	FailureObservationHash string               `json:"failureObservationHash"`
	RepositoryBindingHash  string               `json:"repositoryBindingHash"`
	ProfileSHA256          string               `json:"profileSha256"`
	FallbackDecisionSHA256 string               `json:"fallbackDecisionSha256"`
	ReplacementEffectID    string               `json:"replacementEffectId"`
	Consumed               bool                 `json:"consumed"`
}

func ReplacementAuthorityID(authority ReplacementAuthority) string {
	authority.ID = ""
	return "replacement-authority-" + primaryDigest(authority)[:32]
}

func ValidReplacementAuthority(authority ReplacementAuthority) bool {
	return authority.AuthorizedRunVersion > 0 && authority.ConsumedRunVersion == authority.AuthorizedRunVersion+1 &&
		authority.LeaseEpoch > 0 && primaryIdentityPattern.MatchString(authority.OldAgentID) &&
		primaryIdentityPattern.MatchString(authority.OldWorkspaceID) &&
		slices.Contains([]ProviderFailureClass{FailureClassTerminalProvider, FailureClassTerminalPolicy}, authority.FailureClass) &&
		primaryIdentityPattern.MatchString(authority.FailureObservationID) &&
		primarySHA256Pattern.MatchString(authority.FailureObservationHash) &&
		primarySHA256Pattern.MatchString(authority.RepositoryBindingHash) &&
		primarySHA256Pattern.MatchString(authority.ProfileSHA256) &&
		primarySHA256Pattern.MatchString(authority.FallbackDecisionSHA256) &&
		primaryIdentityPattern.MatchString(authority.ReplacementEffectID) && authority.Consumed &&
		authority.ID == ReplacementAuthorityID(authority)
}

// PrimaryRecovery keeps the original resource evidence immutable while a new
// parentless primary is created and verified. No field authorizes deletion.
type PrimaryRecovery struct {
	SchemaVersion         string                      `json:"schemaVersion"`
	RequestID             string                      `json:"requestId"`
	Trigger               RecoveryTrigger             `json:"trigger"`
	Phase                 PrimaryRecoveryPhase        `json:"phase"`
	RequestedAtMillis     int64                       `json:"requestedAtMillis"`
	RequestedRunVersion   uint64                      `json:"requestedRunVersion"`
	RepeatedFailureCount  uint32                      `json:"repeatedFailureCount"`
	PoisonedSession       bool                        `json:"poisonedSession"`
	Observe               Effect                      `json:"observe"`
	Observation           *PrimaryRecoveryObservation `json:"observation,omitempty"`
	OriginalAgent         Effect                      `json:"originalAgent"`
	OriginalPrompt        Effect                      `json:"originalPrompt"`
	OriginalSession       PrimarySession              `json:"originalSession"`
	OriginalVisibility    *WorkerVisibility           `json:"originalVisibility,omitempty"`
	Archive               Effect                      `json:"archive,omitempty"`
	Authority             *ReplacementAuthority       `json:"authority,omitempty"`
	ReplacementAgent      Effect                      `json:"replacementAgent,omitempty"`
	ReplacementPrompt     Effect                      `json:"replacementPrompt,omitempty"`
	ReplacementSession    PrimarySession              `json:"replacementSession,omitempty"`
	ReplacementVisibility *WorkerVisibility           `json:"replacementVisibility,omitempty"`
	NeedsYouCode          NeedCode                    `json:"needsYouCode,omitempty"`
	CompletedAtMillis     int64                       `json:"completedAtMillis,omitempty"`
}

func validRecoveryTrigger(trigger RecoveryTrigger) bool {
	return slices.Contains([]RecoveryTrigger{
		RecoveryProviderFailure, RecoveryPluginReload, RecoveryDaemonInterruption,
		RecoveryTerminalCallbackLost, RecoveryLeaseTakeover, RecoveryCoordinatorRestart,
	}, trigger)
}

func ValidPrimaryRecovery(recovery PrimaryRecovery, state State) bool {
	if recovery.SchemaVersion == "" {
		return recovery.RequestID == "" && recovery.Trigger == "" && recovery.Phase == "" &&
			recovery.RequestedAtMillis == 0 && recovery.RequestedRunVersion == 0 && recovery.RepeatedFailureCount == 0 && !recovery.PoisonedSession &&
			recovery.Observe.ID == "" && recovery.Observation == nil && recovery.OriginalAgent.ID == "" && recovery.OriginalPrompt.ID == "" &&
			recovery.OriginalSession.SchemaVersion == "" && recovery.OriginalVisibility == nil && recovery.Archive.ID == "" &&
			recovery.Authority == nil && recovery.ReplacementAgent.ID == "" && recovery.ReplacementPrompt.ID == "" &&
			recovery.ReplacementSession.SchemaVersion == "" && recovery.ReplacementVisibility == nil &&
			recovery.NeedsYouCode == "" && recovery.CompletedAtMillis == 0
	}
	if recovery.SchemaVersion != PrimaryRecoverySchemaVersion || !primaryIdentityPattern.MatchString(recovery.RequestID) ||
		!validRecoveryTrigger(recovery.Trigger) || recovery.RequestedAtMillis < 0 || recovery.RequestedRunVersion == 0 || recovery.RepeatedFailureCount == 0 ||
		recovery.OriginalAgent.ID == "" || recovery.OriginalAgent.Kind != EffectAgentCreate || recovery.OriginalAgent.ExternalID == "" ||
		recovery.OriginalPrompt.ID == "" || recovery.OriginalPrompt.Kind != EffectAgentPrompt ||
		!ValidPrimarySession(recovery.OriginalSession) || recovery.OriginalSession.NativeAgentID == "" ||
		recovery.OriginalSession.NativeAgentID != recovery.OriginalAgent.ExternalID ||
		recovery.OriginalVisibility == nil || !ValidWorkerVisibility(*recovery.OriginalVisibility) ||
		recovery.OriginalVisibility.AgentID != recovery.OriginalAgent.ExternalID ||
		recovery.OriginalVisibility.ObservedDigest != recovery.OriginalVisibility.Digest {
		return false
	}
	if recovery.Observe.ID == "" || recovery.Observe.Kind != EffectPrimaryRecoveryObserve || recovery.Observe.AttemptLimit != 0 ||
		(recovery.Observe.Phase != EffectIntentRecorded && recovery.Observe.Phase != EffectDispatching && recovery.Observe.Phase != EffectComplete) {
		return false
	}
	if recovery.Observation != nil && (!ValidPrimaryRecoveryObservation(*recovery.Observation) ||
		recovery.Observation.Host.BindingHash != state.RepositoryBindingHash) {
		return false
	}
	if recovery.Authority != nil && !ValidReplacementAuthority(*recovery.Authority) {
		return false
	}
	if recovery.Authority != nil && (recovery.Authority.OldAgentID != recovery.OriginalAgent.ExternalID ||
		recovery.Authority.OldWorkspaceID != state.HostView.ExternalID) {
		return false
	}
	if recovery.Authority == nil {
		if recovery.ReplacementAgent.ID != "" || recovery.ReplacementPrompt.ID != "" || recovery.ReplacementSession.SchemaVersion != "" ||
			recovery.ReplacementVisibility != nil {
			return false
		}
	} else if recovery.ReplacementAgent.ID != recovery.Authority.ReplacementEffectID ||
		recovery.ReplacementAgent.Kind != EffectAgentCreate || !ValidPrimarySession(recovery.ReplacementSession) ||
		recovery.ReplacementSession.AgentIntentID != recovery.ReplacementAgent.ID || state.EffectiveProfiles == nil ||
		!PrimarySessionMatchesProfiles(recovery.ReplacementSession, *state.EffectiveProfiles) {
		return false
	}
	if recovery.ReplacementVisibility != nil && !ValidWorkerVisibility(*recovery.ReplacementVisibility) {
		return false
	}
	if recovery.ReplacementVisibility != nil && recovery.ReplacementVisibility.AgentID != "" &&
		(recovery.ReplacementVisibility.AgentID != recovery.ReplacementAgent.ExternalID ||
			recovery.ReplacementVisibility.ObservedDigest != recovery.ReplacementVisibility.Digest ||
			recovery.ReplacementSession.NativeAgentID != recovery.ReplacementAgent.ExternalID) {
		return false
	}
	if recovery.ReplacementPrompt.ID != "" && recovery.ReplacementPrompt.Kind != EffectAgentPrompt {
		return false
	}
	switch recovery.Phase {
	case PrimaryRecoveryIntentRecorded, PrimaryRecoveryObserved, PrimaryRecoveryArchiving,
		PrimaryRecoveryAuthorityConsumed, PrimaryRecoveryCreatingReplacement,
		PrimaryRecoveryBindingReplacement, PrimaryRecoveryPromptingReplacement:
		return recovery.NeedsYouCode == "" && recovery.CompletedAtMillis == 0
	case PrimaryRecoveryComplete:
		return recovery.NeedsYouCode == "" && recovery.CompletedAtMillis >= recovery.RequestedAtMillis
	case PrimaryRecoveryNeedsYou:
		return recovery.NeedsYouCode != "" && recovery.CompletedAtMillis == 0
	default:
		return false
	}
}

type PrimaryRecoveryFacts struct {
	RunVersion             uint64
	CurrentAgentID         string
	CurrentWorkspaceID     string
	CurrentCandidateID     string
	RepositoryBindingHash  string
	ProfileSHA256          string
	FallbackDecisionSHA256 string
	LeaseEpoch             uint64
	ControlBlocksDispatch  bool
	BudgetBlocksDispatch   bool
	HelpersSafe            bool
	Policy                 PrimaryRecoveryPolicy
	Recovery               PrimaryRecovery
	NowMillis              int64
}

type PrimaryRecoveryDisposition string

const (
	RecoveryDispositionObserve          PrimaryRecoveryDisposition = "observe"
	RecoveryDispositionAdoptExisting    PrimaryRecoveryDisposition = "adopt_existing"
	RecoveryDispositionWaitExternal     PrimaryRecoveryDisposition = "wait_external"
	RecoveryDispositionArchiveOriginal  PrimaryRecoveryDisposition = "archive_original"
	RecoveryDispositionAuthorizeReplace PrimaryRecoveryDisposition = "authorize_replacement"
	RecoveryDispositionContinueReplace  PrimaryRecoveryDisposition = "continue_replacement"
	RecoveryDispositionNeedsYou         PrimaryRecoveryDisposition = "needs_you"
)

type PrimaryRecoveryDecision struct {
	Disposition  PrimaryRecoveryDisposition `json:"disposition"`
	FailureClass ProviderFailureClass       `json:"failureClass,omitempty"`
	Code         NeedCode                   `json:"code,omitempty"`
	Digest       string                     `json:"digest"`
}

func recoveryDecision(disposition PrimaryRecoveryDisposition, class ProviderFailureClass, code NeedCode, facts PrimaryRecoveryFacts) PrimaryRecoveryDecision {
	return PrimaryRecoveryDecision{Disposition: disposition, FailureClass: class, Code: code, Digest: primaryDigest(struct {
		Disposition PrimaryRecoveryDisposition `json:"disposition"`
		Class       ProviderFailureClass       `json:"class"`
		Code        NeedCode                   `json:"code"`
		RunVersion  uint64                     `json:"runVersion"`
		Observation string                     `json:"observation"`
	}{disposition, class, code, facts.RunVersion, func() string {
		if facts.Recovery.Observation == nil {
			return ""
		}
		return facts.Recovery.Observation.FactHash
	}()})}
}

func exactRecoveryInventory(facts PrimaryRecoveryFacts) (NativeAgentRecoveryFact, bool, NeedCode) {
	observation := facts.Recovery.Observation
	if observation == nil || observation.Host.Inventory == nil || !observation.Host.Inventory.Complete {
		return NativeAgentRecoveryFact{}, false, NeedRecoveryObservationMissing
	}
	if observation.Runtime.BindingHash != facts.RepositoryBindingHash || !observation.Runtime.RepositoryExact ||
		!observation.Runtime.WorktreePresent || !observation.Runtime.WorktreeExact || !observation.Runtime.BranchExact ||
		!observation.Runtime.BaseExact {
		return NativeAgentRecoveryFact{}, false, NeedRecoveryBindingContradictory
	}
	workspaces := observation.Host.Inventory.Workspaces
	if len(workspaces) != 1 || workspaces[0].WorkspaceID != facts.CurrentWorkspaceID || !workspaces[0].Active ||
		workspaces[0].Archived || !workspaces[0].WorktreeExact || !workspaces[0].TitleExact || !workspaces[0].KindExact {
		return NativeAgentRecoveryFact{}, false, NeedRecoveryWorkspaceInvalid
	}
	for _, worktree := range observation.Runtime.RelatedWorktrees {
		if worktree.Active && (!worktree.ExactRun || worktree.BindingHash != facts.RepositoryBindingHash) {
			return NativeAgentRecoveryFact{}, false, NeedRecoveryOrphanResource
		}
	}
	var original NativeAgentRecoveryFact
	found := false
	replacementID := ""
	if facts.Recovery.Authority != nil && facts.Recovery.ReplacementAgent.ExternalID != "" {
		replacementID = facts.Recovery.ReplacementAgent.ExternalID
	}
	for _, agent := range observation.Host.Inventory.Agents {
		if agent.Role == ControlledReviewer || agent.Role == ControlledHelper {
			if !agent.LabelsRunExact || agent.Role == ControlledReviewer && agent.ParentPresent ||
				agent.Role == ControlledHelper && !agent.ParentPresent {
				return NativeAgentRecoveryFact{}, false, NeedRecoveryResourceContradictory
			}
			continue
		}
		if agent.Role != ControlledTaskAgent || agent.ParentPresent {
			return NativeAgentRecoveryFact{}, false, NeedRecoveryResourceContradictory
		}
		if agent.AgentID == facts.CurrentAgentID {
			if found {
				return NativeAgentRecoveryFact{}, false, NeedRecoveryDuplicatePrimary
			}
			original, found = agent, true
			continue
		}
		if (replacementID != "" && agent.AgentID == replacementID) ||
			facts.Recovery.Authority != nil && agent.EffectID == facts.Recovery.Authority.ReplacementEffectID {
			continue
		}
		return NativeAgentRecoveryFact{}, false, NeedRecoveryOrphanResource
	}
	if !found || !original.TitleExact || !original.WorktreeExact || !original.LabelsRunExact || !original.ProfileExact ||
		!original.SessionExact || original.WorkspaceID != facts.CurrentWorkspaceID {
		return NativeAgentRecoveryFact{}, false, NeedRecoveryResourceContradictory
	}
	return original, true, ""
}

// EvaluatePrimaryRecovery is the pure engine policy gate. It never asks the
// connector to choose replacement, and every unsafe route preserves resources.
func EvaluatePrimaryRecovery(facts PrimaryRecoveryFacts) PrimaryRecoveryDecision {
	if facts.RunVersion == 0 || !primaryIdentityPattern.MatchString(facts.CurrentAgentID) ||
		!primaryIdentityPattern.MatchString(facts.CurrentWorkspaceID) ||
		!primarySHA256Pattern.MatchString(facts.RepositoryBindingHash) ||
		!primarySHA256Pattern.MatchString(facts.ProfileSHA256) ||
		!primarySHA256Pattern.MatchString(facts.FallbackDecisionSHA256) || facts.LeaseEpoch == 0 ||
		!ValidPrimaryRecoveryPolicy(facts.Policy) || facts.NowMillis < 0 {
		return recoveryDecision(RecoveryDispositionNeedsYou, FailureClassAmbiguous, NeedRecoveryFactsInvalid, facts)
	}
	if facts.ControlBlocksDispatch {
		return recoveryDecision(RecoveryDispositionWaitExternal, FailureClassNone, "", facts)
	}
	if facts.BudgetBlocksDispatch {
		return recoveryDecision(RecoveryDispositionNeedsYou, FailureClassNone, NeedRecoveryBudgetBlocked, facts)
	}
	if facts.Recovery.Observation == nil {
		return recoveryDecision(RecoveryDispositionObserve, FailureClassNone, "", facts)
	}
	if !ValidPrimaryRecoveryObservation(*facts.Recovery.Observation) ||
		facts.NowMillis < facts.Recovery.Observation.Runtime.ObservedAtMillis ||
		facts.NowMillis-facts.Recovery.Observation.Runtime.ObservedAtMillis > facts.Recovery.Observation.Runtime.MaximumAgeMillis ||
		facts.NowMillis < facts.Recovery.Observation.Host.ObservedAtMillis ||
		facts.NowMillis-facts.Recovery.Observation.Host.ObservedAtMillis > facts.Recovery.Observation.Host.MaximumAgeMillis {
		return recoveryDecision(RecoveryDispositionNeedsYou, FailureClassAmbiguous, NeedRecoveryObservationInvalid, facts)
	}
	original, exact, code := exactRecoveryInventory(facts)
	if !exact {
		return recoveryDecision(RecoveryDispositionNeedsYou, FailureClassAmbiguous, code, facts)
	}
	if facts.CurrentCandidateID != "" {
		if !facts.Recovery.Observation.Runtime.CandidatePresent || !facts.Recovery.Observation.Runtime.CandidateExact {
			return recoveryDecision(RecoveryDispositionNeedsYou, FailureClassAmbiguous, NeedRecoveryCandidateContradictory, facts)
		}
		return recoveryDecision(RecoveryDispositionAdoptExisting, FailureClassNone, "", facts)
	}
	class := ClassifyProviderFailure(original.FailureSignals)
	if facts.Recovery.Authority != nil {
		if !ValidReplacementAuthority(*facts.Recovery.Authority) || facts.Recovery.Authority.LeaseEpoch != facts.LeaseEpoch ||
			facts.Recovery.Authority.OldAgentID != facts.CurrentAgentID || facts.Recovery.Authority.OldWorkspaceID != facts.CurrentWorkspaceID ||
			facts.Recovery.Authority.RepositoryBindingHash != facts.RepositoryBindingHash ||
			facts.Recovery.Authority.ProfileSHA256 != facts.ProfileSHA256 ||
			facts.Recovery.Authority.FallbackDecisionSHA256 != facts.FallbackDecisionSHA256 {
			return recoveryDecision(RecoveryDispositionNeedsYou, FailureClassAmbiguous, NeedRecoveryAuthorityContradictory, facts)
		}
		if facts.Recovery.ReplacementAgent.Phase == EffectComplete && facts.Recovery.ReplacementPrompt.Phase == EffectComplete {
			return recoveryDecision(RecoveryDispositionContinueReplace, class, "", facts)
		}
		if facts.Recovery.ReplacementAgent.Observation != nil &&
			(facts.Recovery.ReplacementAgent.Observation.Status == ObservationErrored ||
				facts.Recovery.ReplacementAgent.Observation.Status == ObservationPermission) {
			return recoveryDecision(RecoveryDispositionNeedsYou, class, NeedRecoverySecondFailure, facts)
		}
		return recoveryDecision(RecoveryDispositionContinueReplace, class, "", facts)
	}
	if class == FailureClassNone {
		if (original.Status == "idle" || original.Status == "running" || original.Status == "initializing") &&
			original.BootstrapPresent && original.PromptPresent && original.PersistenceReferencePresent && !original.ArchivedAtPresent {
			return recoveryDecision(RecoveryDispositionAdoptExisting, class, "", facts)
		}
		return recoveryDecision(RecoveryDispositionNeedsYou, FailureClassAmbiguous, NeedRecoveryPromptAmbiguous, facts)
	}
	if class == FailureClassTransient {
		return recoveryDecision(RecoveryDispositionWaitExternal, class, "", facts)
	}
	if class == FailureClassAmbiguous || !facts.Policy.Allows(class) {
		return recoveryDecision(RecoveryDispositionNeedsYou, class, NeedRecoveryFailureRequiresHuman, facts)
	}
	if !facts.HelpersSafe {
		return recoveryDecision(RecoveryDispositionNeedsYou, class, NeedRecoveryHelperAmbiguous, facts)
	}
	if facts.Recovery.RepeatedFailureCount >= facts.Policy.PoisonedSessionFailureThreshold && !facts.Recovery.PoisonedSession {
		return recoveryDecision(RecoveryDispositionNeedsYou, class, NeedRecoveryFactsInvalid, facts)
	}
	if !original.ArchivedAtPresent {
		return recoveryDecision(RecoveryDispositionArchiveOriginal, class, "", facts)
	}
	if !facts.Recovery.Observation.Runtime.OriginalAgentProcessAbsent {
		return recoveryDecision(RecoveryDispositionNeedsYou, class, NeedRecoveryTerminationUnproven, facts)
	}
	return recoveryDecision(RecoveryDispositionAuthorizeReplace, class, "", facts)
}

// PrimaryFallbackDecisionSHA256 binds replacement to the already frozen
// Worker selection and explicit fallback result; it never resolves a new row.
func PrimaryFallbackDecisionSHA256(profilesJSON []byte, selectedIndex int, chainSHA256 string) (string, error) {
	if len(profilesJSON) == 0 || selectedIndex < 0 || selectedIndex > 8 || !primarySHA256Pattern.MatchString(chainSHA256) {
		return "", errors.New("primary fallback decision is invalid")
	}
	return primaryDigest(struct {
		ProfilesJSON  string `json:"profilesJson"`
		SelectedIndex int    `json:"selectedIndex"`
		ChainSHA256   string `json:"chainSha256"`
	}{string(profilesJSON), selectedIndex, chainSHA256}), nil
}

func validRecoveryNeedCode(code NeedCode) bool {
	return code != "" && strings.HasPrefix(string(code), "primary_recovery_")
}
