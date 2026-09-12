// SPDX-License-Identifier: Apache-2.0

// Package home builds the host-bound Director Home projection from durable
// TaskStore facts and explicit operational observations.
package home

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/mcuadros/director-engine/domain"
	"github.com/mcuadros/director-engine/domain/execution"
	homeport "github.com/mcuadros/director-engine/ports/home"
	planningport "github.com/mcuadros/director-engine/ports/planning"
	"github.com/mcuadros/director-engine/projection"
)

const HostObservationMaximumAgeMillis int64 = 30_000

var (
	ErrHostMismatch  = errors.New("Home host identity does not match")
	ErrHostFacts     = errors.New("Home host facts are invalid")
	ErrCursorInvalid = errors.New("Home cursor is invalid or stale")
	ErrSnapshot      = errors.New("Home facts changed during snapshot read")
)

type SyncStreamState = homeport.SyncStreamState
type WorkspaceObservation = homeport.WorkspaceObservation
type OperationAvailability = homeport.OperationAvailability
type ProjectObservation = homeport.ProjectObservation
type HostObservation = homeport.HostObservation
type OperationalSource = homeport.OperationalSource

const (
	SyncCurrent       = homeport.SyncCurrent
	SyncLocalAhead    = homeport.SyncLocalAhead
	SyncRemoteAhead   = homeport.SyncRemoteAhead
	SyncDiverged      = homeport.SyncDiverged
	SyncFailed        = homeport.SyncFailed
	SyncNotConfigured = homeport.SyncNotConfigured
	SyncUnavailable   = homeport.SyncUnavailable
	SyncStale         = homeport.SyncStale
)

type Store interface {
	Projects(context.Context) ([]domain.Project, error)
	Workspaces(context.Context, string) ([]domain.Workspace, error)
	Epics(context.Context, string) ([]domain.Epic, error)
	Tasks(context.Context, string) ([]domain.Task, error)
	DependencyOverrides(context.Context, string) ([]domain.DependencyOverride, error)
	Runs(context.Context, string) ([]domain.Run, error)
	Candidate(context.Context, string) (domain.Candidate, error)
	LatestEventSequence(context.Context) (uint64, error)
}

type Reader struct {
	store  Store
	tasks  TaskProjectionSource
	source OperationalSource
	now    func() int64
}

type TaskProjectionSource interface {
	TaskProjectionInputs(context.Context) ([]projection.TaskProjectionInput, error)
}

func NewReader(store Store, tasks TaskProjectionSource, source OperationalSource, now func() int64) *Reader {
	if now == nil {
		now = func() int64 { return time.Now().UnixMilli() }
	}
	return &Reader{store: store, tasks: tasks, source: source, now: now}
}

type StaticSource struct {
	hostID, label, instanceID string
	now                       func() int64
	logs                      homeport.TechnicalLogSource
}

func NewStaticSource(hostID, label, instanceID string, now func() int64) *StaticSource {
	return &StaticSource{hostID: hostID, label: label, instanceID: instanceID, now: now}
}

func (source *StaticSource) WithTechnicalLogs(logs homeport.TechnicalLogSource) *StaticSource {
	source.logs = logs
	return source
}

func (source *StaticSource) Observe(ctx context.Context, requested string, projectIDs []string) (HostObservation, error) {
	if requested != source.hostID {
		return HostObservation{}, ErrHostMismatch
	}
	projects := make(map[string]ProjectObservation, len(projectIDs))
	for _, projectID := range projectIDs {
		var logs []homeport.TechnicalLogObservation
		logsAvailable := false
		if source.logs != nil {
			var err error
			logs, err = source.logs.ReadTechnicalLogs(ctx, projectID, source.now())
			if err != nil {
				return HostObservation{}, ErrHostFacts
			}
			logsAvailable = true
		}
		projects[projectID] = ProjectObservation{
			GitSync: SyncUnavailable, TaskStoreSync: SyncUnavailable,
			Workspaces:    map[string]WorkspaceObservation{},
			Operations:    OperationAvailability{Doctor: true, Control: true},
			TechnicalLogs: logs, TechnicalLogsAvailable: logsAvailable,
		}
	}
	return HostObservation{
		HostID: source.hostID, Label: source.label, InstanceID: source.instanceID,
		State: "current", ObservedAtMillis: source.now(), MaximumAgeMillis: HostObservationMaximumAgeMillis,
		Projects: projects,
	}, nil
}

type cursorValue struct {
	HostID     string `json:"hostId"`
	InstanceID string `json:"instanceId"`
	Snapshot   uint64 `json:"snapshot"`
	Offset     int    `json:"offset"`
	Checksum   string `json:"checksum"`
}

func cursorChecksum(value cursorValue) string {
	digest := sha256.Sum256([]byte(value.HostID + "\x1f" + value.InstanceID + "\x1f" + strconv.FormatUint(value.Snapshot, 10) + "\x1f" + strconv.Itoa(value.Offset)))
	return hex.EncodeToString(digest[:])
}

func encodeCursor(hostID, instanceID string, snapshot uint64, offset int) *string {
	value := cursorValue{HostID: hostID, InstanceID: instanceID, Snapshot: snapshot, Offset: offset}
	value.Checksum = cursorChecksum(value)
	encoded, _ := json.Marshal(value)
	text := base64.RawURLEncoding.EncodeToString(encoded)
	return &text
}

func decodeCursor(raw *string, hostID, instanceID string, snapshot uint64, total int) (int, error) {
	if raw == nil {
		return 0, nil
	}
	decoded, err := base64.RawURLEncoding.DecodeString(*raw)
	if err != nil || len(decoded) > 1024 {
		return 0, ErrCursorInvalid
	}
	var value cursorValue
	decoder := json.NewDecoder(strings.NewReader(string(decoded)))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&value) != nil || value.HostID != hostID || value.InstanceID != instanceID ||
		value.Snapshot != snapshot || value.Offset <= 0 || value.Offset >= total || value.Checksum != cursorChecksum(value) {
		return 0, ErrCursorInvalid
	}
	return value.Offset, nil
}

func validObservation(value HostObservation, requested string, projectIDs []string, now int64) bool {
	if value.HostID != requested || !boundedHomeText(value.Label, 512) || !boundedHomeText(value.InstanceID, 128) || value.ObservedAtMillis < 0 ||
		value.ObservedAtMillis > now || value.MaximumAgeMillis <= 0 || value.MaximumAgeMillis > HostObservationMaximumAgeMillis ||
		(value.State != "current" && value.State != "degraded" && value.State != "disconnected" && value.State != "stale") {
		return false
	}
	if len(value.Projects) != len(projectIDs) {
		return false
	}
	for _, projectID := range projectIDs {
		project, ok := value.Projects[projectID]
		if !ok || !validSyncStream(project.GitSync) || !validSyncStream(project.TaskStoreSync) || project.SyncObservedAtMillis < 0 || project.SyncObservedAtMillis > now ||
			(project.SyncObservedAtMillis == 0 && project.SyncMaximumAgeMillis != 0) ||
			(project.SyncObservedAtMillis > 0 && (project.SyncMaximumAgeMillis <= 0 || project.SyncMaximumAgeMillis > 5*60*1000)) {
			return false
		}
		if (project.GitSyncDetail.State != "" && !validSyncDetail(project.GitSyncDetail, now)) ||
			(project.TaskStoreSyncDetail.State != "" && !validSyncDetail(project.TaskStoreSyncDetail, now)) ||
			(project.Reconciliation.State != "" && !validReconciliation(project.Reconciliation, now)) {
			return false
		}
		for _, entry := range project.TechnicalLogs {
			if !validLog(entry, now) {
				return false
			}
		}
		for _, workspace := range project.Workspaces {
			if workspace.Health != "healthy" && workspace.Health != "degraded" && workspace.Health != "disconnected" && workspace.Health != "stale" && workspace.Health != "unknown" {
				return false
			}
		}
		seenCapabilities := make(map[homeport.PreflightCapability]struct{}, len(project.Preflight))
		for _, preflight := range project.Preflight {
			if !validPreflightCapability(preflight.Capability) || !validPreflightState(preflight.State) {
				return false
			}
			if _, duplicate := seenCapabilities[preflight.Capability]; duplicate {
				return false
			}
			seenCapabilities[preflight.Capability] = struct{}{}
		}
	}
	return true
}

func validPreflightCapability(value homeport.PreflightCapability) bool {
	switch value {
	case homeport.CapabilityPaseoRuntime, homeport.CapabilityConnectorContract,
		homeport.CapabilityProviderCodex, homeport.CapabilityProviderClaude,
		homeport.CapabilityProviderOpenCode, homeport.CapabilityProviderAuth,
		homeport.CapabilitySessionMCP, homeport.CapabilityExactMCPPolicy,
		homeport.CapabilityRootlessOCI, homeport.CapabilityRepositoryIdentity,
		homeport.CapabilityResourceLimits:
		return true
	default:
		return false
	}
}

func validPreflightState(value homeport.PreflightState) bool {
	return value == homeport.PreflightCurrent || value == homeport.PreflightMissing || value == homeport.PreflightMismatch ||
		value == homeport.PreflightUnavailable || value == homeport.PreflightStale
}

func boundedHomeText(value string, maximum int) bool {
	return value != "" && len(value) <= maximum && utf8.ValidString(value) && value == strings.TrimSpace(value) && strings.IndexFunc(value, unicode.IsControl) < 0
}

func validSyncStream(value SyncStreamState) bool {
	return value == SyncCurrent || value == SyncFailed || value == SyncNotConfigured || value == SyncUnavailable || value == SyncStale
}

func validHomeProject(project domain.Project) bool {
	if !boundedHomeText(project.ID, 128) || !boundedHomeText(project.Name, domain.MaximumDisplayNameBytes) || project.Organizer == nil ||
		project.Organizer.ID != domain.OrganizerID(project.ID) || (project.State != "active" && project.State != "paused" && project.State != "degraded" && project.State != "archived") ||
		(project.Organizer.Mode != domain.OrganizerModeCreate && project.Organizer.Mode != domain.OrganizerModeAdopt) {
		return false
	}
	switch project.Organizer.Phase {
	case domain.OrganizerPhaseIntentRecorded, domain.OrganizerPhaseRepositoryPrepared, domain.OrganizerPhaseConfigurationWritten,
		domain.OrganizerPhaseReadmeWritten, domain.OrganizerPhaseReferencesWritten, domain.OrganizerPhaseRepositoryInitialized,
		domain.OrganizerPhaseRevisionCommitted, domain.OrganizerPhaseActive:
	default:
		return false
	}
	if project.Organizer.OrganizerRevision != "" && !validGitSHA(project.Organizer.OrganizerRevision) {
		return false
	}
	if project.Organizer.ConfigurationSHA256 != "" && !validSHA(project.Organizer.ConfigurationSHA256, 64) {
		return false
	}
	return true
}

func validSHA(value string, size int) bool {
	return len(value) == size && strings.IndexFunc(value, func(character rune) bool {
		return !((character >= '0' && character <= '9') || (character >= 'a' && character <= 'f'))
	}) < 0
}

func validGitSHA(value string) bool { return validSHA(value, 40) }

func taskInputsMatch(projectID string, tasks []domain.Task, inputs []projection.TaskProjectionInput) bool {
	if len(tasks) != len(inputs) {
		return false
	}
	byID := make(map[string]domain.Task, len(tasks))
	for _, task := range tasks {
		if task.ProjectID != projectID || len(task.WorkspaceIDs) != 1 {
			return false
		}
		if _, duplicate := byID[task.ID]; duplicate {
			return false
		}
		byID[task.ID] = task
	}
	for _, input := range inputs {
		task, ok := byID[input.TaskID]
		if !ok || input.ProjectID != projectID || input.WorkspaceID != task.WorkspaceIDs[0] ||
			input.Facts.TaskID != task.ID || input.Facts.TaskVersion != task.Version {
			return false
		}
	}
	return true
}

func explanation(code, message string, human bool) planningport.Explanation {
	return planningport.Explanation{Code: code, Message: message, HumanActionRequired: human}
}

func disabledReason(code, message string) *planningport.Explanation {
	value := explanation(code, message, false)
	return &value
}

func projectPointer(value string) *string { return &value }

func homeAction(kind, label, hostID, projectID string, enabled bool, reason *planningport.Explanation, workspaceID string, command *planningport.AllowedAction, emphasis string) planningport.HomeAction {
	if enabled {
		reason = nil
	}
	var project, workspace *string
	if projectID != "" {
		project = projectPointer(projectID)
	}
	if workspaceID != "" {
		workspace = projectPointer(workspaceID)
	}
	return planningport.HomeAction{Kind: kind, Label: label, HostID: hostID, ProjectID: project,
		Enabled: enabled, UnavailableReason: reason, PaseoWorkspaceID: workspace, Command: command, Emphasis: emphasis}
}

func engineAction(kind, label, target string, version uint64, approval *string, emphasis string) *planningport.AllowedAction {
	digest := sha256.Sum256([]byte(kind + "\x1f" + target + "\x1f" + strconv.FormatUint(version, 10)))
	request := "action-" + strings.ReplaceAll(kind, ".", "-") + "-" + hex.EncodeToString(digest[:16])
	return &planningport.AllowedAction{Kind: kind, Label: label, TargetID: projectPointer(target), RequestID: request,
		IdempotencyKey: request, ExpectedVersion: strconv.FormatUint(version, 10), HumanApprovalRef: approval, Emphasis: emphasis}
}

func syncState(observation ProjectObservation, now int64) planningport.HomeSync {
	git, taskStore := string(observation.GitSync), string(observation.TaskStoreSync)
	if observation.GitSyncDetail.State != "" {
		git = string(observation.GitSyncDetail.State)
	}
	if observation.TaskStoreSyncDetail.State != "" {
		taskStore = string(observation.TaskStoreSyncDetail.State)
	}
	if observation.SyncObservedAtMillis > 0 && now-observation.SyncObservedAtMillis > observation.SyncMaximumAgeMillis {
		git, taskStore = string(SyncStale), string(SyncStale)
	}
	state := "unavailable"
	switch {
	case git == string(SyncCurrent) && taskStore == string(SyncCurrent):
		state = "current"
	case git == string(SyncNotConfigured) && taskStore == string(SyncNotConfigured):
		state = "not_configured"
	case git == string(SyncStale) || taskStore == string(SyncStale):
		state = "stale"
	case git == string(SyncFailed) && taskStore == string(SyncFailed):
		state = "failed"
	case (git == string(SyncCurrent) || taskStore == string(SyncCurrent)) && git != taskStore:
		state = "partial"
	case git == string(SyncFailed) || taskStore == string(SyncFailed) ||
		git == string(SyncLocalAhead) || taskStore == string(SyncLocalAhead) ||
		git == string(SyncRemoteAhead) || taskStore == string(SyncRemoteAhead) ||
		git == string(SyncDiverged) || taskStore == string(SyncDiverged) ||
		git == string(homeport.SyncIdentityMismatch) || taskStore == string(homeport.SyncIdentityMismatch):
		state = "failed"
	}
	var observed *string
	if observation.SyncObservedAtMillis > 0 {
		value := time.UnixMilli(observation.SyncObservedAtMillis).UTC().Format(time.RFC3339Nano)
		observed = &value
	}
	return planningport.HomeSync{State: state, Git: git, DynamicState: taskStore, ObservedAt: observed}
}

func leaseState(project domain.Project, now int64) planningport.HomeLease {
	if project.Lease == nil {
		return planningport.HomeLease{State: "absent", Epoch: strconv.FormatUint(project.LastLeaseEpoch, 10)}
	}
	lease := project.Lease
	state := "current"
	if lease.Epoch == 0 || lease.ExpiresAtMillis <= lease.AcquiredAtMillis {
		state = "invalid"
	} else if now >= lease.ExpiresAtMillis {
		state = "expired"
	} else if !lease.DispatchAllowed {
		state = "observe_only"
	}
	expires := time.UnixMilli(lease.ExpiresAtMillis).UTC().Format(time.RFC3339Nano)
	return planningport.HomeLease{State: state, Epoch: strconv.FormatUint(lease.Epoch, 10), ExpiresAt: &expires}
}

func organizerState(project domain.Project, observation ProjectObservation) planningport.HomeOrganizer {
	organizer := project.Organizer
	configurationState := "pending"
	var revision, configuration *string
	if organizer.OrganizerRevision != "" {
		revision = projectPointer(organizer.OrganizerRevision)
	}
	if organizer.ConfigurationSHA256 != "" {
		configuration = projectPointer(organizer.ConfigurationSHA256)
	}
	if organizer.Phase == domain.OrganizerPhaseActive && revision != nil && configuration != nil {
		configurationState = "current"
	}
	var workspace *string
	if observation.OrganizerWorkspaceID != "" {
		workspace = projectPointer(observation.OrganizerWorkspaceID)
	}
	return planningport.HomeOrganizer{ID: organizer.ID, Mode: string(organizer.Mode), Phase: string(organizer.Phase),
		ConfigurationState: configurationState, OrganizerRevision: revision, ConfigurationSHA256: configuration,
		PaseoWorkspaceID: workspace}
}

type taskAggregate struct {
	open, done, needsYou                  uint64
	building, validating, inReview, ready uint64
	reasons                               map[string]uint64
}

func aggregateTasks(inputs []projection.TaskProjectionInput) taskAggregate {
	result := taskAggregate{reasons: map[string]uint64{}}
	for _, input := range inputs {
		row := projection.DeriveTaskProjection(input.Facts)
		if row.DoneMember {
			result.done++
		} else {
			result.open++
		}
		switch row.State {
		case projection.StateNeedsYou:
			result.needsYou++
			code := input.Facts.HumanInput.ReasonCode
			if code == "" {
				code = string(input.Facts.HumanInput.Code)
			}
			if code == "" {
				code = "attention_fact_unavailable"
			}
			result.reasons[code]++
		case projection.StateBuilding:
			result.building++
		case projection.StateValidating:
			result.validating++
		case projection.StateInReview:
			result.inReview++
		case projection.StateReady:
			result.ready++
		}
	}
	return result
}

func attentionReasons(counts map[string]uint64) []planningport.HomeAttentionReason {
	codes := make([]string, 0, len(counts))
	for code := range counts {
		codes = append(codes, code)
	}
	slices.Sort(codes)
	result := make([]planningport.HomeAttentionReason, 0, len(codes))
	for _, code := range codes {
		if len(result) == 32 {
			break
		}
		result = append(result, planningport.HomeAttentionReason{Code: code, Count: strconv.FormatUint(counts[code], 10)})
	}
	return result
}

func workspaceRows(workspaces []domain.Workspace, observation ProjectObservation) []planningport.HomeWorkspace {
	rows := slices.Clone(workspaces)
	slices.SortFunc(rows, func(left, right domain.Workspace) int {
		if result := strings.Compare(left.Name, right.Name); result != 0 {
			return result
		}
		return strings.Compare(left.ID, right.ID)
	})
	result := make([]planningport.HomeWorkspace, 0, len(rows))
	for _, workspace := range rows {
		fact, available := observation.Workspaces[workspace.ID]
		health := "unknown"
		var native *string
		if available {
			health = fact.Health
			if fact.PaseoWorkspaceID != "" {
				native = projectPointer(fact.PaseoWorkspaceID)
			}
		}
		result = append(result, planningport.HomeWorkspace{ID: workspace.ID, Key: workspace.Key, Name: workspace.Name, Health: health, PaseoWorkspaceID: native})
	}
	return result
}

func health(project domain.Project, host HostObservation, observation ProjectObservation, organizer planningport.HomeOrganizer, lease planningport.HomeLease, sync planningport.HomeSync, tasks taskAggregate, workspaces []planningport.HomeWorkspace, now int64) (string, []planningport.Explanation) {
	reasons := []planningport.Explanation{}
	state := host.State
	if state == "disconnected" {
		return "disconnected", []planningport.Explanation{explanation("host_disconnected", "The exact Director host is disconnected; cached facts are not current", false)}
	}
	if state == "stale" {
		return "stale", []planningport.Explanation{explanation("host_observation_stale", "The exact Director host observation is stale; actions remain unavailable", false)}
	}
	if tasks.needsYou > 0 {
		return "needs_you", []planningport.Explanation{explanation("human_action_required", "One or more Tasks require an explicit human decision", true)}
	}
	if project.State == "archived" {
		return "degraded", []planningport.Explanation{explanation("project_archived", "Project is archived and cannot dispatch work", false)}
	}
	if project.State == "paused" {
		return "paused", []planningport.Explanation{explanation("project_paused", "Project execution is paused", false)}
	}
	if project.State == "degraded" {
		reasons = append(reasons, explanation("project_degraded", "Project durable state is degraded", false))
	}
	if organizer.ConfigurationState != "current" {
		reasons = append(reasons, explanation("organizer_configuration_not_current", "Organizer configuration is not an active exact revision", false))
	}
	if lease.State != "current" {
		reasons = append(reasons, explanation("project_lease_"+lease.State, "Project execution lease is not current and dispatchable", false))
	}
	if sync.State != "current" && sync.State != "not_configured" {
		precise := false
		for _, value := range []struct {
			kind   string
			detail homeport.SyncStreamObservation
			legacy homeport.SyncStreamState
		}{
			{"organizer_git", observation.GitSyncDetail, observation.GitSync},
			{"taskstore_dolt", observation.TaskStoreSyncDetail, observation.TaskStoreSync},
		} {
			if value.detail.State == "" {
				continue
			}
			stream, err := streamDetail(value.kind, value.detail, value.legacy, observation.SyncObservedAtMillis, observation.SyncMaximumAgeMillis, now)
			if err == nil && stream.State != "current" && stream.State != "not_configured" {
				reasons = append(reasons, explanation(value.kind+"_"+stream.ReasonCode, stream.Detail, stream.State == "identity_mismatch" || stream.State == "diverged"))
				precise = true
			}
		}
		if !precise {
			reasons = append(reasons, explanation("project_sync_"+sync.State, "Organizer Git and TaskStore synchronization is not fully current", false))
		}
	}
	for _, workspace := range workspaces {
		if workspace.Health != "healthy" {
			reasons = append(reasons, explanation("workspace_health_"+workspace.Health, "At least one Workspace health observation is not current and healthy", false))
			break
		}
	}
	if host.State == "degraded" {
		reasons = append(reasons, explanation("host_degraded", "The exact Director host reports degraded service", false))
	}
	for _, reason := range reasons {
		if reason.HumanActionRequired {
			return "needs_you", reasons
		}
	}
	if len(reasons) > 0 {
		return "degraded", reasons
	}
	return "healthy", reasons
}

func projectActions(project domain.Project, host HostObservation, observation ProjectObservation, healthState string, needsYou uint64, now int64) []planningport.HomeAction {
	unavailable := disabledReason("action_fact_unavailable", "The engine has no current exact capability or native navigation target for this action")
	hostCurrent := healthState != "stale" && healthState != "disconnected"
	boardWorkspaceID, organizerWorkspaceID := "", ""
	if hostCurrent {
		boardWorkspaceID, organizerWorkspaceID = observation.BoardWorkspaceID, observation.OrganizerWorkspaceID
	}
	actions := []planningport.HomeAction{
		homeAction("open_board", "Board", host.HostID, project.ID, boardWorkspaceID != "", unavailable, boardWorkspaceID, nil, "primary"),
		homeAction("open_organizer", "Organizer", host.HostID, project.ID, organizerWorkspaceID != "", unavailable, organizerWorkspaceID, nil, "secondary"),
		homeAction("open_needs_you", "Needs you", host.HostID, project.ID, hostCurrent && needsYou > 0, unavailable, "", nil, "secondary"),
		homeAction("operations", "Operations", host.HostID, project.ID, hostCurrent, unavailable, "", nil, "secondary"),
	}
	for _, item := range []struct {
		kind, label string
		enabled     bool
	}{
		{"sync_now", "Sync now", observation.Operations.Sync},
		{"reconcile_now", "Reconcile now", observation.Operations.Reconcile},
		{"doctor", "Doctor", observation.Operations.Doctor},
		{"repair", "Repair…", observation.Operations.Repair},
	} {
		actions = append(actions, homeAction(item.kind, item.label, host.HostID, project.ID, item.enabled && hostCurrent, unavailable, "", nil, "secondary"))
	}
	controlAllowed := observation.Operations.Control && hostCurrent && project.State != "archived" &&
		project.Lease != nil && project.Lease.DispatchAllowed && now < project.Lease.ExpiresAtMillis
	if project.State == "paused" {
		command := engineAction("project.resume", "Resume Project", project.ID, project.Version, nil, "primary")
		actions = append(actions, homeAction("resume", "Resume", host.HostID, project.ID, controlAllowed, unavailable, "", command, "primary"))
	} else {
		command := engineAction("project.pause", "Pause Project", project.ID, project.Version, nil, "secondary")
		actions = append(actions, homeAction("pause", "Pause", host.HostID, project.ID, controlAllowed, unavailable, "", command, "secondary"))
	}
	emergencyKind, emergencyLabel := "project.emergency-stop.prepare", "Emergency stop"
	var approval *string
	if project.Control.Phase == execution.ControlAwaitingConfirmation && project.Control.Confirmation != nil {
		emergencyKind, emergencyLabel = "project.emergency-stop.confirm", "Confirm emergency stop"
		approval = projectPointer(project.Control.Confirmation.ID)
	}
	emergency := engineAction(emergencyKind, emergencyLabel, project.ID, project.Version, approval, "danger")
	actions = append(actions, homeAction("emergency_stop", emergencyLabel, host.HostID, project.ID, controlAllowed && (!project.Control.EmergencyLatched || approval != nil), unavailable, "", emergency, "danger"))
	return actions
}

func buildProject(project domain.Project, workspaces []domain.Workspace, inputs []projection.TaskProjectionInput, host HostObservation, observation ProjectObservation, now int64) planningport.HomeProject {
	tasks := aggregateTasks(inputs)
	organizer := organizerState(project, observation)
	lease := leaseState(project, now)
	sync := syncState(observation, now)
	workspaceValues := workspaceRows(workspaces, observation)
	healthState, reasons := health(project, host, observation, organizer, lease, sync, tasks, workspaceValues, now)
	active := tasks.building + tasks.validating + tasks.inReview + tasks.ready
	return planningport.HomeProject{
		ID: project.ID, Version: strconv.FormatUint(project.Version, 10), Name: project.Name, State: project.State,
		Health: healthState, HealthReasons: reasons, Workspaces: workspaceValues, Organizer: organizer, Lease: lease, Sync: sync,
		Tasks:           planningport.TaskCounts{Open: strconv.FormatUint(tasks.open, 10), Done: strconv.FormatUint(tasks.done, 10), NeedsYou: strconv.FormatUint(tasks.needsYou, 10)},
		ActiveWork:      planningport.HomeActiveWork{Building: strconv.FormatUint(tasks.building, 10), Validating: strconv.FormatUint(tasks.validating, 10), InReview: strconv.FormatUint(tasks.inReview, 10), Ready: strconv.FormatUint(tasks.ready, 10), Total: strconv.FormatUint(active, 10)},
		NeedsYouReasons: attentionReasons(tasks.reasons),
		Actions:         projectActions(project, host, observation, healthState, tasks.needsYou, now),
	}
}

func surfaceActions(host HostObservation) []planningport.HomeAction {
	return []planningport.HomeAction{
		homeAction("create_project", "Create Project", host.HostID, "", host.State == "current", disabledReason("host_not_current", "Create Project requires the exact current Director host"), "", nil, "primary"),
		homeAction("adopt_organizer", "Adopt Organizer", host.HostID, "", host.State == "current", disabledReason("host_not_current", "Adopt Organizer requires the exact current Director host"), "", nil, "secondary"),
	}
}

func totals(projects []planningport.HomeProject, total int) planningport.HomeTotals {
	var healthy, degraded, paused, needsYou, active uint64
	for _, project := range projects {
		switch project.Health {
		case "healthy":
			healthy++
		case "paused":
			paused++
		case "needs_you":
			needsYou++
		default:
			degraded++
		}
		value, _ := strconv.ParseUint(project.ActiveWork.Total, 10, 64)
		active += value
	}
	return planningport.HomeTotals{Projects: strconv.Itoa(total), Healthy: strconv.FormatUint(healthy, 10),
		Degraded: strconv.FormatUint(degraded, 10), Paused: strconv.FormatUint(paused, 10), NeedsYou: strconv.FormatUint(needsYou, 10), ActiveWork: strconv.FormatUint(active, 10)}
}

func (reader *Reader) Query(ctx context.Context, input planningport.HomeQueryInput) (planningport.HomeSnapshot, error) {
	if reader.store == nil || reader.tasks == nil || reader.source == nil || planningport.ValidateHomeQuery(input) != nil {
		return planningport.HomeSnapshot{}, planningport.ErrQueryInvalid
	}
	for range 3 {
		before, err := reader.store.LatestEventSequence(ctx)
		if err != nil {
			return planningport.HomeSnapshot{}, fmt.Errorf("read Home cursor: %w", err)
		}
		projects, err := reader.store.Projects(ctx)
		if err != nil {
			return planningport.HomeSnapshot{}, fmt.Errorf("read Home Projects: %w", err)
		}
		if len(projects) > planningport.MaximumProjects {
			return planningport.HomeSnapshot{}, ErrHostFacts
		}
		for _, project := range projects {
			if !validHomeProject(project) {
				return planningport.HomeSnapshot{}, ErrHostFacts
			}
		}
		slices.SortFunc(projects, func(left, right domain.Project) int {
			if result := strings.Compare(left.Name, right.Name); result != 0 {
				return result
			}
			return strings.Compare(left.ID, right.ID)
		})
		projectIDs := make([]string, len(projects))
		for index := range projects {
			projectIDs[index] = projects[index].ID
		}
		host, err := reader.source.Observe(ctx, input.HostID, projectIDs)
		if err != nil {
			return planningport.HomeSnapshot{}, err
		}
		now := reader.now()
		if !validObservation(host, input.HostID, projectIDs, now) {
			return planningport.HomeSnapshot{}, ErrHostFacts
		}
		offset, err := decodeCursor(input.Cursor, host.HostID, host.InstanceID, before, len(projects))
		if err != nil {
			return planningport.HomeSnapshot{}, err
		}
		end := min(offset+input.PageSize, len(projects))
		inputs, err := reader.tasks.TaskProjectionInputs(ctx)
		if err != nil {
			return planningport.HomeSnapshot{}, fmt.Errorf("read Home Task projections: %w", err)
		}
		inputsByProject := make(map[string][]projection.TaskProjectionInput, len(projects))
		projectSet := make(map[string]struct{}, len(projects))
		for _, project := range projects {
			projectSet[project.ID] = struct{}{}
		}
		seenTasks := make(map[string]struct{}, len(inputs))
		for _, value := range inputs {
			if _, ok := projectSet[value.ProjectID]; !ok {
				return planningport.HomeSnapshot{}, ErrHostFacts
			}
			taskIdentity := value.ProjectID + "\x1f" + value.TaskID
			if _, duplicate := seenTasks[taskIdentity]; duplicate {
				return planningport.HomeSnapshot{}, ErrHostFacts
			}
			seenTasks[taskIdentity] = struct{}{}
			inputsByProject[value.ProjectID] = append(inputsByProject[value.ProjectID], value)
		}
		if now-host.ObservedAtMillis > host.MaximumAgeMillis {
			host.State = "stale"
		}
		allRows := make([]planningport.HomeProject, 0, len(projects))
		for _, project := range projects {
			workspaces, err := reader.store.Workspaces(ctx, project.ID)
			if err != nil {
				return planningport.HomeSnapshot{}, fmt.Errorf("read Home Workspaces: %w", err)
			}
			if len(workspaces) > domain.MaximumWorkspacesPerProject {
				return planningport.HomeSnapshot{}, ErrHostFacts
			}
			workspaceIDs := make(map[string]struct{}, len(workspaces))
			for _, workspace := range workspaces {
				workspaceIDs[workspace.ID] = struct{}{}
			}
			projectObservation := host.Projects[project.ID]
			for workspaceID, workspace := range projectObservation.Workspaces {
				if _, ok := workspaceIDs[workspaceID]; !ok || (workspace.PaseoWorkspaceID != "" && !boundedHomeText(workspace.PaseoWorkspaceID, 128)) {
					return planningport.HomeSnapshot{}, ErrHostFacts
				}
			}
			if (projectObservation.BoardWorkspaceID != "" && !boundedHomeText(projectObservation.BoardWorkspaceID, 128)) ||
				(projectObservation.OrganizerWorkspaceID != "" && !boundedHomeText(projectObservation.OrganizerWorkspaceID, 128)) {
				return planningport.HomeSnapshot{}, ErrHostFacts
			}
			tasks, err := reader.store.Tasks(ctx, project.ID)
			if err != nil {
				return planningport.HomeSnapshot{}, fmt.Errorf("read Home Tasks: %w", err)
			}
			if !taskInputsMatch(project.ID, tasks, inputsByProject[project.ID]) {
				return planningport.HomeSnapshot{}, ErrHostFacts
			}
			allRows = append(allRows, buildProject(project, workspaces, inputsByProject[project.ID], host, projectObservation, now))
		}
		rows := allRows[offset:end]
		after, err := reader.store.LatestEventSequence(ctx)
		if err != nil {
			return planningport.HomeSnapshot{}, fmt.Errorf("read Home cursor: %w", err)
		}
		if before != after {
			continue
		}
		definition, err := planningport.EmbeddedDefinition()
		if err != nil {
			return planningport.HomeSnapshot{}, err
		}
		hash, err := planningport.SchemaSHA256()
		if err != nil {
			return planningport.HomeSnapshot{}, err
		}
		var next *string
		if end < len(projects) {
			next = encodeCursor(host.HostID, host.InstanceID, after, end)
		}
		return planningport.HomeSnapshot{SchemaVersion: definition.SchemaVersion, ContractVersion: definition.ContractVersion,
			ContractHash: hash, Cursor: strconv.FormatUint(after, 10), Page: planningport.HomePage{
				Host: planningport.HomeHost{ID: host.HostID, Label: host.Label, InstanceID: host.InstanceID, State: host.State,
					ObservedAt: time.UnixMilli(host.ObservedAtMillis).UTC().Format(time.RFC3339Nano), MaximumAgeMillis: strconv.FormatInt(host.MaximumAgeMillis, 10)},
				Projects: rows, Totals: totals(allRows, len(projects)), SurfaceActions: surfaceActions(host), TotalProjects: strconv.Itoa(len(projects)), NextCursor: next,
			}}, nil
	}
	return planningport.HomeSnapshot{}, ErrSnapshot
}
