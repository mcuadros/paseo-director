// SPDX-License-Identifier: Apache-2.0

package execution

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"path/filepath"
	"slices"
	"strings"
)

const (
	ProjectControlSchemaVersion = "director.project-control/v1"
	RunControlSchemaVersion     = "director.run-control/v1"
	EmergencyConfirmationTTL    = int64(60_000)
	MaximumControlledAgents     = 64
)

// ControlKind is the closed operator-control vocabulary. EmergencyPrepare is
// deliberately distinct from EmergencyStop: preparing a server challenge has
// no execution effect and cannot be interpreted as confirmation.
type ControlKind string

const (
	ControlPauseProject     ControlKind = "pause_project"
	ControlResumeProject    ControlKind = "resume_project"
	ControlCancelTask       ControlKind = "cancel_task"
	ControlEmergencyPrepare ControlKind = "emergency_stop_prepare"
	ControlEmergencyStop    ControlKind = "emergency_stop"
)

type ControlPhase string

const (
	ControlAwaitingConfirmation ControlPhase = "awaiting_confirmation"
	ControlIntentRecorded       ControlPhase = "intent_recorded"
	ControlAwaitingSafeBoundary ControlPhase = "awaiting_safe_boundary"
	ControlReconciling          ControlPhase = "reconciling"
	ControlContaining           ControlPhase = "containing"
	ControlPreserving           ControlPhase = "preserving"
	ControlCleaning             ControlPhase = "cleaning"
	ControlPaused               ControlPhase = "paused"
	ControlCancelled            ControlPhase = "cancelled"
	ControlComplete             ControlPhase = "complete"
	ControlNeedsYou             ControlPhase = "needs_you"
)

type ControlActorKind string

const (
	ControlActorHuman       ControlActorKind = "human"
	ControlActorBudget      ControlActorKind = "system_budget"
	ControlActorCoordinator ControlActorKind = "system_coordinator"
)

// ControlIntent is fixed by the engine ingress. Authenticated and Source are
// server facts and are never accepted from a model-facing command payload.
type ControlIntent struct {
	ID                string           `json:"id"`
	RequestID         string           `json:"requestId"`
	Kind              ControlKind      `json:"kind"`
	ProjectID         string           `json:"projectId"`
	TaskID            string           `json:"taskId,omitempty"`
	RunID             string           `json:"runId,omitempty"`
	ActorKind         ControlActorKind `json:"actorKind"`
	ActorID           string           `json:"actorId"`
	ActorSessionID    string           `json:"actorSessionId"`
	Source            string           `json:"source"`
	Authenticated     bool             `json:"authenticated"`
	RequestedAtMillis int64            `json:"requestedAtMillis"`
	ConfirmationID    string           `json:"confirmationId,omitempty"`
}

// EmergencyConfirmation is a one-use server-minted capability bound to the
// exact Project version and authenticated human session. ChallengeSHA256 is a
// digest only; confirmation text is neither persisted nor accepted from an
// agent or model.
type EmergencyConfirmation struct {
	ID                     string `json:"id"`
	ProjectID              string `json:"projectId"`
	ExpectedProjectVersion uint64 `json:"expectedProjectVersion"`
	ActorID                string `json:"actorId"`
	ActorSessionID         string `json:"actorSessionId"`
	IssuedAtMillis         int64  `json:"issuedAtMillis"`
	ExpiresAtMillis        int64  `json:"expiresAtMillis"`
	ChallengeSHA256        string `json:"challengeSha256"`
	ConsumedAtMillis       int64  `json:"consumedAtMillis,omitempty"`
}

// ProjectControl is the durable Project-wide latch. ResumeRequired stays true
// across restart and completed pause/emergency intents; only a separately
// admitted explicit Resume may clear it after full reconciliation.
type ProjectControl struct {
	SchemaVersion     string                 `json:"schemaVersion"`
	Generation        uint64                 `json:"generation"`
	Intent            ControlIntent          `json:"intent"`
	Phase             ControlPhase           `json:"phase"`
	Confirmation      *EmergencyConfirmation `json:"confirmation,omitempty"`
	ResumeRequired    bool                   `json:"resumeRequired"`
	EmergencyLatched  bool                   `json:"emergencyLatched"`
	ExplanationCode   string                 `json:"explanationCode"`
	CompletedAtMillis int64                  `json:"completedAtMillis,omitempty"`
}

type ControlledAgentRole string

const (
	ControlledTaskAgent ControlledAgentRole = "task_agent"
	ControlledReviewer  ControlledAgentRole = "reviewer"
	ControlledHelper    ControlledAgentRole = "helper"
)

// ControlledAgentIdentity is an engine-owned lifecycle registration. It is
// populated by the primary/helper/Reviewer lifecycle paths, never by a control
// request. ParentID is mandatory only for helpers.
type ControlledAgentIdentity struct {
	ID                   string              `json:"id"`
	Role                 ControlledAgentRole `json:"role"`
	ParentID             string              `json:"parentId,omitempty"`
	WorkspaceID          string              `json:"workspaceId"`
	Title                string              `json:"title"`
	WorktreePath         string              `json:"worktreePath"`
	RecoveryWorktreePath string              `json:"recoveryWorktreePath,omitempty"`
	Labels               map[string]string   `json:"labels,omitempty"`
	ProfileSHA256        string              `json:"profileSha256,omitempty"`
}

// ControlledAgent freezes one containment target and the two independent host
// observation/effect frontiers used for safe-boundary pause and hard stop.
type ControlledAgent struct {
	Identity          ControlledAgentIdentity `json:"identity"`
	Boundary          Effect                  `json:"boundary,omitempty"`
	Archive           Effect                  `json:"archive,omitempty"`
	Archived          bool                    `json:"archived"`
	ProcessAbsent     bool                    `json:"processAbsent"`
	TerminalEventID   string                  `json:"terminalEventId,omitempty"`
	TerminalEventHash string                  `json:"terminalEventHash,omitempty"`
	TerminalCursor    uint64                  `json:"terminalCursor,omitempty"`
}

type RecoveryMode string

const (
	RecoveryRetain             RecoveryMode = "retain"
	RecoverySnapshotThenDelete RecoveryMode = "snapshot_then_delete"
)

// ControlPolicy is frozen into a Run. The default follows PLAN section 15.5;
// Project/Task policy may select retain, which never creates cleanup effects.
type ControlPolicy struct {
	CancelRecovery    RecoveryMode `json:"cancelRecovery"`
	EmergencyRecovery RecoveryMode `json:"emergencyRecovery"`
	RetentionMillis   int64        `json:"retentionMillis"`
}

func DefaultControlPolicy() ControlPolicy {
	return ControlPolicy{
		CancelRecovery: RecoverySnapshotThenDelete, EmergencyRecovery: RecoverySnapshotThenDelete,
		RetentionMillis: 7 * 24 * 60 * 60 * 1_000,
	}
}

func ValidControlPolicy(policy ControlPolicy) bool {
	validMode := func(mode RecoveryMode) bool {
		return mode == RecoveryRetain || mode == RecoverySnapshotThenDelete
	}
	return validMode(policy.CancelRecovery) && validMode(policy.EmergencyRecovery) &&
		policy.RetentionMillis > 0 && policy.RetentionMillis <= 7*24*60*60*1_000
}

type RecoveryIntent struct {
	Mode                 RecoveryMode `json:"mode"`
	Snapshot             Effect       `json:"snapshot,omitempty"`
	ArtifactID           string       `json:"artifactId,omitempty"`
	Preserved            bool         `json:"preserved"`
	RetentionUntilMillis int64        `json:"retentionUntilMillis,omitempty"`
	CleanupAuthorized    bool         `json:"cleanupAuthorized"`
	WorktreeRemovalReady bool         `json:"worktreeRemovalReady"`
}

// RunControl is a durable per-Run saga. RelaunchBlocked is monotonic for the
// intent and prevents ordinary Step/scheduler paths from treating a cancelled
// or paused frontier as permission to issue work.
type RunControl struct {
	SchemaVersion            string            `json:"schemaVersion"`
	Intent                   ControlIntent     `json:"intent"`
	ProjectGeneration        uint64            `json:"projectGeneration"`
	Phase                    ControlPhase      `json:"phase"`
	Targets                  []ControlledAgent `json:"targets,omitempty"`
	TargetSetSHA256          string            `json:"targetSetSha256,omitempty"`
	UnresolvedAgents         bool              `json:"unresolvedAgents"`
	PostPreservationNeedCode NeedCode          `json:"postPreservationNeedCode,omitempty"`
	Recovery                 RecoveryIntent    `json:"recovery"`
	ResumeReconciliationID   string            `json:"resumeReconciliationId,omitempty"`
	RelaunchBlocked          bool              `json:"relaunchBlocked"`
	ExplanationCode          string            `json:"explanationCode"`
	CompletedAtMillis        int64             `json:"completedAtMillis,omitempty"`
}

func controlDigest(value any) string {
	encoded, err := json.Marshal(value)
	if err != nil {
		panic("marshal execution control: " + err.Error())
	}
	digest := sha256.Sum256(encoded)
	return hex.EncodeToString(digest[:])
}

func ControlIntentID(intent ControlIntent) string {
	intent.ID = ""
	return "control-" + controlDigest(intent)[:32]
}

func EmergencyConfirmationID(confirmation EmergencyConfirmation) string {
	confirmation.ID = ""
	confirmation.ConsumedAtMillis = 0
	return "emergency-confirmation-" + controlDigest(confirmation)[:32]
}

func ValidControlIntent(intent ControlIntent) bool {
	if !validScopePart(intent.RequestID) || !validScopePart(intent.ProjectID) || !validScopePart(intent.ActorID) ||
		!validScopePart(intent.ActorSessionID) || intent.Source != "server" ||
		!intent.Authenticated || intent.RequestedAtMillis < 0 || intent.ID != ControlIntentID(intent) {
		return false
	}
	switch intent.Kind {
	case ControlPauseProject, ControlResumeProject, ControlEmergencyPrepare, ControlEmergencyStop:
		if intent.TaskID != "" || intent.RunID != "" {
			return false
		}
	case ControlCancelTask:
		if !validScopePart(intent.TaskID) || !validScopePart(intent.RunID) {
			return false
		}
	default:
		return false
	}
	if intent.Kind == ControlEmergencyStop {
		return intent.ActorKind == ControlActorHuman && intent.ConfirmationID != ""
	}
	if intent.ConfirmationID != "" {
		return false
	}
	return intent.ActorKind == ControlActorHuman || intent.ActorKind == ControlActorBudget ||
		intent.ActorKind == ControlActorCoordinator
}

func validScopePart(value string) bool {
	return value != "" && len(value) <= 256 && value == strings.TrimSpace(value) &&
		strings.IndexFunc(value, func(character rune) bool {
			return !(character == '-' || character == '_' || character == '.' || character == ':' || character == '@' || character == '/' ||
				character >= '0' && character <= '9' || character >= 'A' && character <= 'Z' || character >= 'a' && character <= 'z')
		}) < 0
}

func ValidEmergencyConfirmation(value EmergencyConfirmation) bool {
	return validScopePart(value.ProjectID) && value.ExpectedProjectVersion > 0 &&
		validScopePart(value.ActorID) && validScopePart(value.ActorSessionID) &&
		value.IssuedAtMillis >= 0 && value.ExpiresAtMillis-value.IssuedAtMillis == EmergencyConfirmationTTL &&
		validSHA256(value.ChallengeSHA256) && value.ID == EmergencyConfirmationID(value) &&
		(value.ConsumedAtMillis == 0 || value.ConsumedAtMillis >= value.IssuedAtMillis && value.ConsumedAtMillis < value.ExpiresAtMillis)
}

func ValidControlledAgentIdentity(identity ControlledAgentIdentity, scope Scope) bool {
	if !validScopePart(identity.ID) || !validScopePart(identity.WorkspaceID) || !validScope(scope) ||
		identity.Title == "" || len(identity.Title) > 512 || identity.Title != strings.TrimSpace(identity.Title) ||
		identity.WorktreePath == "" || len(identity.WorktreePath) > 4_096 || !filepath.IsAbs(identity.WorktreePath) ||
		filepath.Clean(identity.WorktreePath) != identity.WorktreePath ||
		(identity.RecoveryWorktreePath != "" && (len(identity.RecoveryWorktreePath) > 4_096 ||
			!filepath.IsAbs(identity.RecoveryWorktreePath) || filepath.Clean(identity.RecoveryWorktreePath) != identity.RecoveryWorktreePath)) ||
		(identity.ProfileSHA256 != "" && !validSHA256(identity.ProfileSHA256)) || len(identity.Labels) > 32 {
		return false
	}
	for key, value := range identity.Labels {
		if !validScopePart(key) || !validScopePart(value) {
			return false
		}
	}
	switch identity.Role {
	case ControlledTaskAgent, ControlledReviewer:
		return identity.ParentID == ""
	case ControlledHelper:
		return validScopePart(identity.ParentID)
	default:
		return false
	}
}

func CloneControlledAgents(values []ControlledAgent) []ControlledAgent {
	result := make([]ControlledAgent, len(values))
	for index, value := range values {
		result[index] = value
		result[index].Identity.Labels = make(map[string]string, len(value.Identity.Labels))
		for key, item := range value.Identity.Labels {
			result[index].Identity.Labels[key] = item
		}
	}
	return result
}

func ControlledAgentSetSHA256(values []ControlledAgent) string {
	copy := CloneControlledAgents(values)
	for index := range copy {
		copy[index].Boundary = Effect{}
		copy[index].Archive = Effect{}
		copy[index].Archived = false
		copy[index].ProcessAbsent = false
		copy[index].TerminalEventID = ""
		copy[index].TerminalEventHash = ""
		copy[index].TerminalCursor = 0
	}
	slices.SortFunc(copy, func(left, right ControlledAgent) int {
		if left.Identity.Role != right.Identity.Role {
			return strings.Compare(string(left.Identity.Role), string(right.Identity.Role))
		}
		return strings.Compare(left.Identity.ID, right.Identity.ID)
	})
	if len(copy) == 0 {
		return ""
	}
	return controlDigest(copy)
}

func validControlEffect(effect Effect, kinds ...EffectKind) bool {
	if effect.ID == "" {
		return true
	}
	return slices.Contains(kinds, effect.Kind) && effect.AttemptLimit > 0 && effect.Attempt <= effect.AttemptLimit &&
		(effect.Phase == EffectIntentRecorded || effect.Phase == EffectDispatching || effect.Phase == EffectComplete)
}

func ValidRunControl(control RunControl, state State) bool {
	if control.SchemaVersion == "" {
		return control.Intent == (ControlIntent{}) && control.ProjectGeneration == 0 && control.Phase == "" &&
			len(control.Targets) == 0 && control.TargetSetSHA256 == "" && control.Recovery == (RecoveryIntent{}) &&
			!control.UnresolvedAgents && control.PostPreservationNeedCode == "" && control.ResumeReconciliationID == "" &&
			!control.RelaunchBlocked && control.ExplanationCode == "" &&
			control.CompletedAtMillis == 0
	}
	if control.SchemaVersion != RunControlSchemaVersion || !ValidControlIntent(control.Intent) ||
		control.Intent.ProjectID != state.Scope.ProjectID || control.Intent.RunID != "" && control.Intent.RunID != state.Scope.RunID ||
		control.Intent.TaskID != "" && control.Intent.TaskID != state.Scope.TaskID || control.ProjectGeneration == 0 ||
		!control.RelaunchBlocked || len(control.Targets) > MaximumControlledAgents ||
		(control.Phase != ControlIntentRecorded && control.Phase != ControlAwaitingSafeBoundary && control.Phase != ControlReconciling &&
			control.Phase != ControlContaining && control.Phase != ControlPreserving && control.Phase != ControlCleaning &&
			control.Phase != ControlPaused && control.Phase != ControlCancelled && control.Phase != ControlComplete && control.Phase != ControlNeedsYou) {
		return false
	}
	seen := make(map[string]struct{}, len(control.Targets))
	for _, target := range control.Targets {
		if !ValidControlledAgentIdentity(target.Identity, state.Scope) ||
			(target.Identity.Role != ControlledReviewer && target.Identity.WorkspaceID != state.HostView.ExternalID) ||
			!validControlEffect(target.Boundary, EffectControlAgentBoundary) ||
			!validControlEffect(target.Archive, EffectControlAgentArchive) ||
			target.ProcessAbsent && !target.Archived ||
			(target.TerminalEventID == "") != (target.TerminalEventHash == "") ||
			target.TerminalEventID != "" && (!validScopePart(target.TerminalEventID) || !validSHA256(target.TerminalEventHash) || target.TerminalCursor == 0) {
			return false
		}
		if _, duplicate := seen[target.Identity.ID]; duplicate {
			return false
		}
		seen[target.Identity.ID] = struct{}{}
	}
	if len(control.Targets) > 0 && control.TargetSetSHA256 != ControlledAgentSetSHA256(control.Targets) {
		return false
	}
	if control.Recovery.Mode != "" {
		if !ValidControlPolicy(state.ControlPolicy) ||
			(control.Recovery.Mode != RecoveryRetain && control.Recovery.Mode != RecoverySnapshotThenDelete) ||
			!validControlEffect(control.Recovery.Snapshot, EffectRecoverySnapshot) ||
			control.Recovery.CleanupAuthorized && !control.Recovery.Preserved ||
			control.Recovery.WorktreeRemovalReady && !control.Recovery.CleanupAuthorized {
			return false
		}
	}
	if control.PostPreservationNeedCode != "" && !validScopePart(string(control.PostPreservationNeedCode)) {
		return false
	}
	return true
}

func ValidProjectControl(control ProjectControl, projectID string) bool {
	if control == (ProjectControl{}) {
		return true
	}
	if control.SchemaVersion != ProjectControlSchemaVersion || control.Generation == 0 ||
		!ValidControlIntent(control.Intent) || control.Intent.ProjectID != projectID ||
		(control.Phase != ControlAwaitingConfirmation && control.Phase != ControlIntentRecorded &&
			control.Phase != ControlReconciling && control.Phase != ControlContaining &&
			control.Phase != ControlPaused && control.Phase != ControlComplete && control.Phase != ControlNeedsYou) {
		return false
	}
	if control.Confirmation != nil && (!ValidEmergencyConfirmation(*control.Confirmation) || control.Confirmation.ProjectID != projectID) {
		return false
	}
	if control.EmergencyLatched && (!control.ResumeRequired ||
		(control.Intent.Kind != ControlEmergencyStop && control.Intent.Kind != ControlResumeProject)) {
		return false
	}
	return control.ExplanationCode != ""
}

func ValidProjectControlState(control ProjectControl, state string) bool {
	if control.SchemaVersion == "" {
		return true
	}
	if control.ResumeRequired && state != "paused" {
		return false
	}
	if control.Intent.Kind == ControlResumeProject && control.Phase == ControlComplete {
		return state == "active" && !control.ResumeRequired && !control.EmergencyLatched
	}
	if control.Intent.Kind == ControlEmergencyPrepare && !control.ResumeRequired {
		return state == "active"
	}
	return state == "paused"
}
