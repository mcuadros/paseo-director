// SPDX-License-Identifier: Apache-2.0

package home

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"time"

	"github.com/mcuadros/director-engine/domain"
	homeport "github.com/mcuadros/director-engine/ports/home"
	planningport "github.com/mcuadros/director-engine/ports/planning"
)

var (
	ErrDoctorProjectNotFound = errors.New("Doctor Project is unavailable")
	ErrDoctorVersionStale    = errors.New("Doctor Project version is stale")
	ErrDoctorSnapshot        = errors.New("Doctor facts changed during read")
)

type DoctorRepairService struct {
	store    Store
	source   OperationalSource
	executor homeport.RepairExecutor
	now      func() int64
}

func NewDoctorRepairService(store Store, source OperationalSource, executor homeport.RepairExecutor, now func() int64) *DoctorRepairService {
	if now == nil {
		now = func() int64 { return time.Now().UnixMilli() }
	}
	return &DoctorRepairService{store: store, source: source, executor: executor, now: now}
}

type doctorFacts struct {
	project     domain.Project
	workspaces  []domain.Workspace
	host        HostObservation
	operational ProjectObservation
	cursor      uint64
	now         int64
}

func (service *DoctorRepairService) observe(ctx context.Context, input planningport.DoctorQueryInput) (doctorFacts, error) {
	if service == nil || service.store == nil || service.source == nil || planningport.ValidateDoctorQuery(input) != nil {
		return doctorFacts{}, planningport.ErrQueryInvalid
	}
	expected, _ := planningport.ParseExpectedVersion(input.ExpectedProjectVersion)
	for range 3 {
		before, err := service.store.LatestEventSequence(ctx)
		if err != nil {
			return doctorFacts{}, fmt.Errorf("read Doctor cursor: %w", err)
		}
		projects, err := service.store.Projects(ctx)
		if err != nil {
			return doctorFacts{}, fmt.Errorf("read Doctor Project: %w", err)
		}
		var project *domain.Project
		for index := range projects {
			if projects[index].ID == input.ProjectID {
				value := projects[index]
				project = &value
				break
			}
		}
		if project == nil || !validHomeProject(*project) {
			return doctorFacts{}, ErrDoctorProjectNotFound
		}
		if project.Version != expected {
			return doctorFacts{}, ErrDoctorVersionStale
		}
		workspaces, err := service.store.Workspaces(ctx, project.ID)
		if err != nil {
			return doctorFacts{}, fmt.Errorf("read Doctor Workspaces: %w", err)
		}
		host, err := service.source.Observe(ctx, input.HostID, []string{project.ID})
		if err != nil {
			return doctorFacts{}, err
		}
		now := service.now()
		if !validObservation(host, input.HostID, []string{project.ID}, now) {
			return doctorFacts{}, ErrHostFacts
		}
		after, err := service.store.LatestEventSequence(ctx)
		if err != nil {
			return doctorFacts{}, fmt.Errorf("reread Doctor cursor: %w", err)
		}
		if before != after {
			continue
		}
		return doctorFacts{project: *project, workspaces: workspaces, host: host, operational: host.Projects[project.ID], cursor: before, now: now}, nil
	}
	return doctorFacts{}, ErrDoctorSnapshot
}

type checkCopy struct {
	ID       string `json:"id"`
	Status   string `json:"status"`
	Code     string `json:"code"`
	Blocking bool   `json:"blocking"`
}

func check(id, category, status, code, title, detail string, blocking bool, capability string, guidance ...string) planningport.DoctorCheck {
	var missing *string
	installation := []string{}
	if status != "passed" {
		missing = &capability
		installation = append(installation, guidance...)
	}
	return planningport.DoctorCheck{
		ID: id, Category: category, Status: status, Code: code, Title: title, Detail: detail,
		Blocking: blocking, MissingCapability: missing, InstallationGuidance: installation,
	}
}

func hostCheck(host HostObservation) planningport.DoctorCheck {
	switch host.State {
	case "current":
		return check("host-current", "host", "passed", "host_current", "Selected Paseo host", "The exact selected host observation is current.", false, "")
	case "degraded":
		return check("host-current", "host", "degraded", "host_degraded", "Selected Paseo host", "The exact selected host reports degraded service, while the required Project facts remain current.", false,
			"Current selected-host service", "Restore the selected Paseo host service, then run Doctor again on this same host.")
	case "disconnected":
		return check("host-current", "host", "unavailable", "host_disconnected", "Selected Paseo host", "The exact selected host is offline; Director did not fall through to another host.", true,
			"Connected selected Paseo host", "Reconnect this exact Paseo host. Director never selects another host as a fallback.")
	default:
		return check("host-current", "host", "stale", "host_observation_stale", "Selected Paseo host", "The selected-host observation is stale and cannot authorize Repair.", true,
			"Fresh selected-host observation", "Refresh this exact host and rerun Doctor before previewing Repair.")
	}
}

func organizerCheck(project domain.Project) planningport.DoctorCheck {
	organizer := project.Organizer
	if organizer != nil && organizer.Phase == domain.OrganizerPhaseActive && organizer.OrganizerRevision != "" && organizer.ConfigurationSHA256 != "" {
		return check("organizer-revision", "organizer", "passed", "organizer_current", "Approved Organizer revision", "Organizer configuration is bound to an active exact revision.", false, "")
	}
	return check("organizer-revision", "organizer", "blocking", "organizer_revision_not_current", "Approved Organizer revision", "Organizer configuration is not bound to an active exact revision.", true,
		"Approved Organizer configuration", "Open Organizer, review the exact configuration diff, and use human Preview/Apply before launching work.")
}

func leaseCheck(project domain.Project, now int64) planningport.DoctorCheck {
	state := leaseState(project, now).State
	switch state {
	case "current":
		return check("project-lease", "lease", "passed", "project_lease_current", "Project execution lease", "The authoritative Project lease is current and dispatchable.", false, "")
	case "observe_only":
		return check("project-lease", "lease", "blocking", "project_lease_observe_only", "Project execution lease", "The current engine is observe-only until prior process absence and full reconciliation are proven.", true,
			"Dispatch-enabled Project lease", "Complete engine-owned takeover reconciliation and process-absence proof; do not force a lease from the UI.")
	case "expired":
		return check("project-lease", "lease", "blocking", "project_lease_expired", "Project execution lease", "The Project lease expired and cannot authorize lifecycle effects.", true,
			"Current Project execution lease", "Use Repair only if it previews an exact lease-reconciliation effect; otherwise restore engine supervision and rerun Doctor.")
	case "absent":
		return check("project-lease", "lease", "blocking", "project_lease_absent", "Project execution lease", "No authoritative Project lease is recorded.", true,
			"Authoritative Project execution lease", "Start the configured Director Engine for this Project and rerun Doctor; do not create a lease in the client.")
	default:
		return check("project-lease", "lease", "blocking", "project_lease_invalid", "Project execution lease", "The Project lease identity or timing facts are invalid.", true,
			"Valid Project execution lease", "Stop dispatch, restore the trusted TaskStore/engine identity, and run Repair only from an exact engine preview.")
	}
}

func syncCheck(id, title, value string) planningport.DoctorCheck {
	if value == string(SyncCurrent) || value == string(SyncNotConfigured) {
		detail := "The stream is current."
		if value == string(SyncNotConfigured) {
			detail = "The stream is explicitly not configured; no fallback remote is selected."
		}
		return check(id, "sync", "passed", id+"_"+value, title, detail, false, "")
	}
	status := "blocking"
	if value == string(SyncUnavailable) {
		status = "unavailable"
	} else if value == string(SyncStale) {
		status = "stale"
	}
	return check(id, "sync", status, id+"_"+value, title, "The exact synchronization stream is not current; the successful stream is preserved and no alternate destination is selected.", true,
		title, "Restore this stream's configured authentication and destination, then use an exact Repair preview to reconcile only this stream.")
}

func observedSyncStates(value ProjectObservation, now int64) (SyncStreamState, SyncStreamState, bool) {
	gitSync, taskStoreSync := value.GitSync, value.TaskStoreSync
	if value.GitSyncDetail.State != "" {
		gitSync = value.GitSyncDetail.State
	}
	if value.TaskStoreSyncDetail.State != "" {
		taskStoreSync = value.TaskStoreSyncDetail.State
	}
	stale := value.SyncObservedAtMillis > 0 && value.SyncMaximumAgeMillis > 0 && now-value.SyncObservedAtMillis > value.SyncMaximumAgeMillis
	if stale {
		gitSync, taskStoreSync = SyncStale, SyncStale
	}
	return gitSync, taskStoreSync, stale
}

type capabilityPresentation struct {
	category, title, current, missing string
	guidance                          []string
}

var capabilityPresentations = map[homeport.PreflightCapability]capabilityPresentation{
	homeport.CapabilityPaseoRuntime:       {"host", "Paseo runtime 0.7.2", "Exact supported Paseo 0.7.2 runtime is present.", "Exact Paseo runtime 0.7.2", []string{"Install or update this daemon to exact stable Paseo 0.7.2, reconnect it, and rerun Doctor."}},
	homeport.CapabilityConnectorContract:  {"contract", "Director host contract", "Connector contract version, schema hash, and fixed capability descriptor match.", "Matching Director host contract descriptor", []string{"Install the connector build pinned to this Director Engine contract, then reload the plugin on this exact host."}},
	homeport.CapabilityProviderCodex:      {"provider", "OpenAI Codex CLI 0.147.0", "The admitted Codex 0.147.0 provider tuple is available.", "OpenAI Codex CLI 0.147.0", []string{"Install OpenAI Codex CLI 0.147.0 from the official package, place it on the daemon PATH, and rerun Doctor."}},
	homeport.CapabilityProviderClaude:     {"provider", "Anthropic Claude Code CLI 2.1.258", "The admitted Claude Code 2.1.258 provider tuple is available.", "Anthropic Claude Code CLI 2.1.258", []string{"Install @anthropic-ai/claude-code@2.1.258 from the official package, place it on the daemon PATH, and rerun Doctor."}},
	homeport.CapabilityProviderOpenCode:   {"provider", "OpenCode CLI 1.18.18", "The admitted OpenCode 1.18.18 provider tuple is available.", "OpenCode CLI 1.18.18", []string{"Install OpenCode CLI 1.18.18 from the official distribution, place it on the daemon PATH, and rerun Doctor."}},
	homeport.CapabilityProviderAuth:       {"provider", "Provider authentication", "The declared provider authentication mode is available.", "Authenticated declared provider session", []string{"Use the selected provider's official login command on this daemon host, then rerun Doctor. Never paste credentials into Director."}},
	homeport.CapabilitySessionMCP:         {"mcp", "Session-scoped stdio MCP", "The declared provider supports the required session-scoped stdio MCP bridge.", "Session-scoped stdio MCP support", []string{"Select an admitted provider tuple with session MCP support and rerun Doctor; Director does not substitute another provider."}},
	homeport.CapabilityExactMCPPolicy:     {"mcp", "Exact MCP tool policy", "The MCP tool policy is exact and contains no wildcard grant.", "Exact MCP tool policy", []string{"Update the Organizer proposal with the explicit required MCP tools, then use human Preview/Apply and rerun Doctor."}},
	homeport.CapabilityRootlessOCI:        {"isolation", "Rootless OCI boundary", "The mandatory rootless OCI execution boundary is available.", "Rootless OCI execution capability", []string{"Install and configure the approved rootless OCI runtime on this Linux daemon; Director will not run with weaker isolation."}},
	homeport.CapabilityRepositoryIdentity: {"contract", "Repository identity", "Source, common directory, remote, and base identity match the approved Workspace.", "Exact approved repository identity", []string{"Restore the configured checkout and remote identity. Do not redirect Director to another repository or host."}},
	homeport.CapabilityResourceLimits:     {"resources", "Finite resource observations", "Current process, memory, time, output, temporary-space, disk, and free-space facts are within limits.", "Fresh finite operational-limit observations", []string{"Restore the missing capacity or disk observation and rerun Doctor; Director does not launch with an unbounded or stale limit."}},
}

func capabilityChecks(observations []homeport.PreflightObservation) []planningport.DoctorCheck {
	if len(observations) == 0 {
		return []planningport.DoctorCheck{check("launch-preflight-observation", "contract", "unavailable", "preflight_observation_unavailable", "Blocking launch preflight", "No current closed capability observation is available for this Project.", true,
			"Current exact launch-capability observation", "Restore the Director Engine operational observer and rerun Doctor. The UI cannot infer or install missing capabilities.")}
	}
	values := append([]homeport.PreflightObservation{}, observations...)
	slices.SortFunc(values, func(left, right homeport.PreflightObservation) int {
		if left.Capability < right.Capability {
			return -1
		}
		if left.Capability > right.Capability {
			return 1
		}
		return 0
	})
	checks := make([]planningport.DoctorCheck, 0, len(values))
	for _, observation := range values {
		presentation := capabilityPresentations[observation.Capability]
		status, code, detail, blocking := "passed", string(observation.Capability)+"_current", presentation.current, false
		if observation.State != homeport.PreflightCurrent {
			status, code, detail, blocking = "blocking", string(observation.Capability)+"_"+string(observation.State), "The required capability is not available at its exact admitted identity.", true
			if observation.State == homeport.PreflightUnavailable {
				status = "unavailable"
			} else if observation.State == homeport.PreflightStale {
				status = "stale"
			}
		}
		checks = append(checks, check("preflight-"+string(observation.Capability), presentation.category, status, code, presentation.title, detail, blocking, presentation.missing, presentation.guidance...))
	}
	return checks
}

func workspaceChecks(workspaces []domain.Workspace, observation ProjectObservation) []planningport.DoctorCheck {
	values := append([]domain.Workspace{}, workspaces...)
	slices.SortFunc(values, func(left, right domain.Workspace) int {
		if left.ID < right.ID {
			return -1
		}
		if left.ID > right.ID {
			return 1
		}
		return 0
	})
	checks := make([]planningport.DoctorCheck, 0, len(values))
	for _, workspace := range values {
		fact, exists := observation.Workspaces[workspace.ID]
		if exists && fact.Health == "healthy" {
			checks = append(checks, check("workspace-"+workspace.ID, "workspace", "passed", "workspace_healthy", workspace.Name, "Workspace identity and recovery facts are current and healthy.", false, ""))
			continue
		}
		state := "unknown"
		if exists {
			state = fact.Health
		}
		status := "blocking"
		if state == "disconnected" || state == "unknown" {
			status = "unavailable"
		} else if state == "stale" {
			status = "stale"
		}
		checks = append(checks, check("workspace-"+workspace.ID, "workspace", status, "workspace_"+state, workspace.Name,
			"Workspace ownership, recovery, or current host facts are not healthy.", true, "Current owned Workspace recovery facts",
			"Restore the exact configured Workspace on this host and use Repair only if it previews this Workspace by public name. No private path is exposed."))
	}
	return checks
}

func observationDigest(facts doctorFacts, checks []planningport.DoctorCheck) string {
	copies := make([]checkCopy, 0, len(checks))
	for _, value := range checks {
		copies = append(copies, checkCopy{ID: value.ID, Status: value.Status, Code: value.Code, Blocking: value.Blocking})
	}
	encoded, _ := json.Marshal(struct {
		HostID, InstanceID, HostState, ProjectID string
		ProjectVersion, Cursor                   uint64
		Checks                                   []checkCopy
	}{facts.host.HostID, facts.host.InstanceID, facts.host.State, facts.project.ID, facts.project.Version, facts.cursor, copies})
	digest := sha256.Sum256(encoded)
	return hex.EncodeToString(digest[:])
}

func (service *DoctorRepairService) report(facts doctorFacts) planningport.DoctorReport {
	gitSync, taskStoreSync, syncStale := observedSyncStates(facts.operational, facts.now)
	checks := []planningport.DoctorCheck{
		hostCheck(facts.host),
		check("engine-contract", "contract", "passed", "engine_contract_current", "Director Engine contract", "The request reached the exact engine contract and schema hash accepted by this host.", false, ""),
		check("taskstore-read", "dynamic_state", "passed", "taskstore_read_current", "Director TaskStore", "The exact TaskStore returned a stable read-only event snapshot; Doctor performed no write.", false, ""),
		organizerCheck(facts.project),
		leaseCheck(facts.project, facts.now),
		syncCheck("git-sync", "Organizer Git synchronization", string(gitSync)),
		syncCheck("dynamic-state-sync", "TaskStore/Dolt synchronization", string(taskStoreSync)),
	}
	checks = append(checks, workspaceChecks(facts.workspaces, facts.operational)...)
	checks = append(checks, capabilityChecks(facts.operational.Preflight)...)
	blocking := 0
	hasFailure := false
	for _, value := range checks {
		if value.Blocking {
			blocking++
		}
		if value.Status != "passed" {
			hasFailure = true
		}
	}
	status := "healthy"
	if facts.host.State == "disconnected" {
		status = "offline"
	} else if facts.host.State == "stale" || facts.now-facts.host.ObservedAtMillis > facts.host.MaximumAgeMillis || syncStale {
		status = "stale"
	} else if blocking > 0 {
		status = "blocking"
	} else if hasFailure || facts.host.State == "degraded" {
		status = "degraded"
	}
	repairable := service.executor != nil && facts.operational.Operations.Repair && repairTargetAvailable(facts) &&
		status != "offline" && status != "stale" && facts.project.State != "archived"
	var repairReason *planningport.Explanation
	if !repairable {
		reason := explanation("repair_capability_unavailable", "Repair requires a current exact host observation and an engine-wired idempotent repair executor", false)
		repairReason = &reason
	}
	return planningport.DoctorReport{
		HostID: facts.host.HostID, HostInstanceID: facts.host.InstanceID, ProjectID: facts.project.ID, ProjectName: facts.project.Name,
		ProjectVersion: strconv.FormatUint(facts.project.Version, 10), Cursor: strconv.FormatUint(facts.cursor, 10),
		ObservationID: observationDigest(facts, checks), ObservedAt: time.UnixMilli(facts.host.ObservedAtMillis).UTC().Format(time.RFC3339Nano),
		MaximumAgeMillis: strconv.FormatInt(facts.host.MaximumAgeMillis, 10), Status: status, ReadOnly: true,
		Assurance: "Doctor is projection-only: it reads bounded engine facts and never installs, repairs, writes, dispatches, retries, or falls through to another host.",
		Checks:    checks, BlockingCount: strconv.Itoa(blocking), Repair: planningport.DoctorRepairAvailability{Available: repairable, Reason: repairReason},
	}
}

func repairTargetAvailable(facts doctorFacts) bool {
	capabilities := facts.operational.RepairCapabilities
	gitSync, taskStoreSync, _ := observedSyncStates(facts.operational, facts.now)
	if capabilities.GitSync && gitSync != SyncCurrent && gitSync != SyncNotConfigured {
		return true
	}
	if capabilities.DynamicState && taskStoreSync != SyncCurrent && taskStoreSync != SyncNotConfigured {
		return true
	}
	if capabilities.Lease && leaseState(facts.project, facts.now).State != "current" {
		return true
	}
	if capabilities.WorkspaceRecovery {
		for _, workspace := range facts.workspaces {
			observation, ok := facts.operational.Workspaces[workspace.ID]
			if !ok || observation.Health != "healthy" {
				return true
			}
		}
	}
	return false
}

func (service *DoctorRepairService) Doctor(ctx context.Context, input planningport.DoctorQueryInput) (planningport.DoctorReport, error) {
	facts, err := service.observe(ctx, input)
	if err != nil {
		return planningport.DoctorReport{}, err
	}
	report := service.report(facts)
	definition, err := planningport.EmbeddedDefinition()
	if err != nil {
		return planningport.DoctorReport{}, err
	}
	hash, err := planningport.SchemaSHA256()
	if err != nil {
		return planningport.DoctorReport{}, err
	}
	report.SchemaVersion, report.ContractVersion, report.ContractHash = definition.SchemaVersion, definition.ContractVersion, hash
	return report, nil
}

func repairOperations(facts doctorFacts) []planningport.RepairOperation {
	capabilities := facts.operational.RepairCapabilities
	operations := []planningport.RepairOperation{}
	gitSync, taskStoreSync, _ := observedSyncStates(facts.operational, facts.now)
	if capabilities.GitSync && gitSync != SyncCurrent && gitSync != SyncNotConfigured {
		operations = append(operations, planningport.RepairOperation{ID: "repair-git-sync", Kind: "reconcile_git_sync", Description: "Reconcile only the configured Organizer Git synchronization stream against its current expected ref.", AffectedResource: "Organizer Git synchronization stream", EffectClass: "conditional_update"})
	}
	if capabilities.DynamicState && taskStoreSync != SyncCurrent && taskStoreSync != SyncNotConfigured {
		operations = append(operations, planningport.RepairOperation{ID: "repair-dynamic-state", Kind: "reconcile_dynamic_state", Description: "Reconcile only the configured TaskStore/Dolt synchronization stream from durable event facts.", AffectedResource: "TaskStore/Dolt synchronization stream", EffectClass: "conditional_update"})
	}
	lease := leaseState(facts.project, facts.now)
	if capabilities.Lease && lease.State != "current" {
		operations = append(operations, planningport.RepairOperation{ID: "repair-project-lease", Kind: "reconcile_lease", Description: "Request fresh engine-owned Project lease reconciliation under the existing holder and takeover safety contract.", AffectedResource: "Project execution lease", EffectClass: "store_only"})
	}
	if capabilities.WorkspaceRecovery {
		workspaces := append([]domain.Workspace{}, facts.workspaces...)
		slices.SortFunc(workspaces, func(left, right domain.Workspace) int {
			if left.ID < right.ID {
				return -1
			}
			if left.ID > right.ID {
				return 1
			}
			return 0
		})
		for _, workspace := range workspaces {
			fact, ok := facts.operational.Workspaces[workspace.ID]
			if !ok || fact.Health != "healthy" {
				operations = append(operations, planningport.RepairOperation{ID: "repair-workspace-" + workspace.ID, Kind: "reconcile_workspace_recovery", Description: "Reconcile owned Workspace recovery evidence without deleting or relocating any path.", AffectedResource: "Workspace " + workspace.Name, EffectClass: "store_only"})
			}
		}
	}
	for index := range operations {
		operations[index].Destructive = false
		operations[index].AutomaticInstall = false
	}
	return operations
}

func previewDigest(preview planningport.RepairPreview) string {
	copy := preview
	copy.ID = ""
	encoded, _ := json.Marshal(copy)
	digest := sha256.Sum256(encoded)
	return hex.EncodeToString(digest[:])
}

func (service *DoctorRepairService) preview(ctx context.Context, input planningport.RepairInput) (planningport.RepairPreview, error) {
	report, err := service.Doctor(ctx, planningport.DoctorQueryInput{HostID: input.HostID, ProjectID: input.ProjectID, ExpectedProjectVersion: input.ExpectedProjectVersion})
	if err != nil {
		return planningport.RepairPreview{}, err
	}
	facts, err := service.observe(ctx, planningport.DoctorQueryInput{HostID: input.HostID, ProjectID: input.ProjectID, ExpectedProjectVersion: input.ExpectedProjectVersion})
	if err != nil {
		return planningport.RepairPreview{}, err
	}
	// Doctor and Preview must bind one unchanged event/host observation.
	if report.Cursor != strconv.FormatUint(facts.cursor, 10) || report.ObservationID != observationDigest(facts, service.report(facts).Checks) {
		return planningport.RepairPreview{}, ErrDoctorSnapshot
	}
	operations := repairOperations(facts)
	issues := []planningport.Explanation{}
	if !report.Repair.Available {
		issues = append(issues, *report.Repair.Reason)
	}
	if len(operations) == 0 {
		issues = append(issues, explanation("repair_no_exact_effect", "No supported repair effect matches the current authoritative findings; install or authenticate missing capabilities manually and rerun Doctor", true))
	}
	preview := planningport.RepairPreview{
		RequestID: input.RequestID, HostID: report.HostID, HostInstanceID: report.HostInstanceID,
		ProjectID: report.ProjectID, ProjectName: report.ProjectName, ProjectVersion: report.ProjectVersion,
		Cursor: report.Cursor, ObservationID: report.ObservationID, Operations: operations,
		Valid: len(issues) == 0 && len(operations) > 0, Issues: issues,
		Confirmation: "Applying this exact Preview requires a fresh server-authenticated human confirmation. Doctor never mutates and Director never installs missing software automatically.",
	}
	preview.ID = previewDigest(preview)
	return preview, nil
}

type AuthenticatedRepairActor struct {
	Kind          string
	ID            string
	SessionID     string
	Authenticated bool
}

func (service *DoctorRepairService) result(ctx context.Context, input planningport.RepairInput, status, message string, preview *planningport.RepairPreview, version *uint64, refusal string) (planningport.RepairResult, error) {
	cursor, err := service.store.LatestEventSequence(ctx)
	if err != nil {
		return planningport.RepairResult{}, err
	}
	definition, err := planningport.EmbeddedDefinition()
	if err != nil {
		return planningport.RepairResult{}, err
	}
	hash, err := planningport.SchemaSHA256()
	if err != nil {
		return planningport.RepairResult{}, err
	}
	var versionText, refusalCode *string
	if version != nil {
		value := strconv.FormatUint(*version, 10)
		versionText = &value
	}
	if refusal != "" {
		refusalCode = &refusal
	}
	return planningport.RepairResult{SchemaVersion: definition.SchemaVersion, ContractVersion: definition.ContractVersion, ContractHash: hash,
		HostID: input.HostID, ProjectID: input.ProjectID, Cursor: strconv.FormatUint(cursor, 10), RequestID: input.RequestID, Status: status, Message: message,
		Preview: preview, ProjectVersion: versionText, RefusalCode: refusalCode}, nil
}

func (service *DoctorRepairService) Repair(ctx context.Context, input planningport.RepairInput, actor AuthenticatedRepairActor) (planningport.RepairResult, error) {
	if service == nil || service.store == nil || planningport.ValidateRepairInput(input) != nil {
		return planningport.RepairResult{}, planningport.ErrQueryInvalid
	}
	if input.Kind == "repair.apply" && (!actor.Authenticated || actor.Kind != "human" || actor.ID == "" || actor.SessionID == "") {
		return service.result(ctx, input, "refused", "Repair refused because a server-authenticated human confirmation is required.", nil, nil, "repair_human_auth_required")
	}
	if input.Kind == "repair.apply" && service.executor != nil {
		priorInput, priorEvidence, found, observeErr := service.executor.ObserveRepair(ctx, input.RequestID)
		if observeErr != nil {
			return service.result(ctx, input, "refused", "Repair outcome cannot be observed safely; no effect was retried.", nil, nil, "repair_outcome_unavailable")
		}
		if found {
			expected, _ := planningport.ParseExpectedVersion(input.ExpectedProjectVersion)
			if input.PreviewID == nil || priorInput.PreviewID != *input.PreviewID || priorInput.HostID != input.HostID || priorInput.ProjectID != input.ProjectID || priorInput.ProjectVersion != expected {
				return service.result(ctx, input, "refused", "Repair request identity was reused with a different exact binding.", nil, nil, "repair_idempotency_conflict")
			}
			if !service.repairReadback(ctx, input.ProjectID, priorEvidence) {
				return service.result(ctx, input, "refused", "Prior Repair evidence no longer matches durable facts; no effect was retried.", nil, nil, "repair_terminal_drift")
			}
			return service.result(ctx, input, "applied", "The exact confirmed Repair was already applied by Director Engine; no effect was repeated.", nil, &priorEvidence.ProjectVersion, "")
		}
	}
	preview, err := service.preview(ctx, input)
	if err != nil {
		if errors.Is(err, ErrDoctorVersionStale) || errors.Is(err, ErrDoctorSnapshot) {
			return service.result(ctx, input, "refused", "Repair refused because Project or observation facts changed; run Doctor and Preview again.", nil, nil, "repair_preview_stale")
		}
		return planningport.RepairResult{}, err
	}
	if input.Kind == "repair.preview" {
		return service.result(ctx, input, "preview", "Repair Preview is bound to exact current engine facts and awaits explicit human confirmation.", &preview, nil, "")
	}
	if input.PreviewID == nil || *input.PreviewID != preview.ID {
		return service.result(ctx, input, "refused", "Repair refused because the exact Preview no longer matches current facts.", nil, nil, "repair_preview_mismatch")
	}
	if !preview.Valid || service.executor == nil {
		return service.result(ctx, input, "refused", "Repair refused because no exact engine repair effect is currently available.", nil, nil, "repair_effect_unavailable")
	}
	version, _ := planningport.ParseExpectedVersion(input.ExpectedProjectVersion)
	cursor, _ := strconv.ParseUint(preview.Cursor, 10, 64)
	operationIDs := make([]string, len(preview.Operations))
	for index, operation := range preview.Operations {
		operationIDs[index] = operation.ID
	}
	evidence, err := service.executor.ApplyRepair(ctx, homeport.RepairExecution{RequestID: input.RequestID, PreviewID: preview.ID,
		HostID: preview.HostID, HostInstanceID: preview.HostInstanceID, ProjectID: preview.ProjectID,
		ProjectVersion: version, Cursor: cursor, ObservationID: preview.ObservationID, OperationIDs: operationIDs})
	if err != nil {
		return service.result(ctx, input, "refused", "Repair effect was refused by current authoritative engine facts.", nil, nil, "repair_effect_refused")
	}
	if evidence.ProjectVersion <= version || evidence.Cursor < cursor || !slices.Equal(evidence.OperationIDs, operationIDs) {
		return service.result(ctx, input, "refused", "Repair evidence did not match the exact Preview and was not projected as success.", nil, nil, "repair_evidence_mismatch")
	}
	if !service.repairReadback(ctx, input.ProjectID, evidence) {
		return service.result(ctx, input, "refused", "Repair durable readback did not match the exact effect evidence and was not projected as success.", nil, nil, "repair_readback_mismatch")
	}
	return service.result(ctx, input, "applied", "The exact confirmed Repair was applied by Director Engine; rerun Doctor to verify current health.", nil, &evidence.ProjectVersion, "")
}

func (service *DoctorRepairService) repairReadback(ctx context.Context, projectID string, evidence homeport.RepairEvidence) bool {
	projects, err := service.store.Projects(ctx)
	if err != nil {
		return false
	}
	readVersion := uint64(0)
	for _, project := range projects {
		if project.ID == projectID {
			readVersion = project.Version
			break
		}
	}
	readCursor, err := service.store.LatestEventSequence(ctx)
	return err == nil && readVersion == evidence.ProjectVersion && readCursor == evidence.Cursor
}
