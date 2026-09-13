// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math/bits"
	"slices"
	"sort"
	"strings"
	"syscall"
	"time"

	"github.com/mcuadros/director-engine/application/configuration"
	planningapp "github.com/mcuadros/director-engine/application/planning"
	applicationscheduling "github.com/mcuadros/director-engine/application/scheduling"
	"github.com/mcuadros/director-engine/domain"
	domainconfig "github.com/mcuadros/director-engine/domain/configuration"
	domainexecution "github.com/mcuadros/director-engine/domain/execution"
	domainreview "github.com/mcuadros/director-engine/domain/review"
	domainscheduling "github.com/mcuadros/director-engine/domain/scheduling"
	storeport "github.com/mcuadros/director-engine/ports/scheduling"
)

type productionSchedulingStore struct {
	launcher      *Launcher
	launchNowTask string
	launchNow     domainscheduling.LaunchNowRequest
}

func schedulingHash(value any) string {
	encoded, err := json.Marshal(value)
	if err != nil {
		return ""
	}
	digest := sha256.Sum256(encoded)
	return hex.EncodeToString(digest[:])
}

func schedulingBudget(name, revision string, limit, requested uint64) domainscheduling.Budget {
	return domainscheduling.Budget{State: domainscheduling.BudgetReady, Revision: name + "-" + revision,
		Limit: limit, Requested: requested, Acknowledgement: domainscheduling.AcknowledgementNone}
}

func optionalSchedulingBudget(name, revision string, limit, requested uint64) domainscheduling.Budget {
	if limit == 0 {
		return domainscheduling.Budget{State: domainscheduling.BudgetDisabled, Acknowledgement: domainscheduling.AcknowledgementNone}
	}
	return schedulingBudget(name, revision, limit, requested)
}

func asUint64(value int64) (uint64, bool) {
	if value < 0 {
		return 0, false
	}
	return uint64(value), true
}

func schedulingDependencyState(ctx context.Context, launcher *Launcher, task domain.Task) (domainscheduling.DependencyState, []string, error) {
	workspaces, err := launcher.store.Workspaces(ctx, task.ProjectID)
	if err != nil {
		return "", nil, err
	}
	epics, err := launcher.store.Epics(ctx, task.ProjectID)
	if err != nil {
		return "", nil, err
	}
	tasks, err := launcher.store.Tasks(ctx, task.ProjectID)
	if err != nil {
		return "", nil, err
	}
	overrides, err := launcher.store.DependencyOverrides(ctx, task.ProjectID)
	if err != nil {
		return "", nil, err
	}
	workspaceIDs := make([]string, len(workspaces))
	for index := range workspaces {
		workspaceIDs[index] = workspaces[index].ID
	}
	report := domain.EvaluatePlanning(domain.PlanningProject{ID: task.ProjectID, Workspaces: workspaceIDs,
		Epics: epics, Tasks: tasks, Overrides: overrides})
	result, ok := report.Result(task.ID)
	if !report.Valid || !ok {
		return "", nil, errors.New("scheduler dependency facts are invalid")
	}
	if result.Blocked {
		return domainscheduling.DependenciesWaiting, nil, nil
	}
	audits := make([]string, 0)
	for _, explanation := range result.Explanations {
		if explanation.OverrideID != "" {
			audits = append(audits, explanation.OverrideID)
		}
	}
	sort.Strings(audits)
	audits = slices.Compact(audits)
	if len(audits) > 0 {
		return domainscheduling.DependenciesAuditedOverride, audits, nil
	}
	return domainscheduling.DependenciesSatisfied, nil, nil
}

func (store *productionSchedulingStore) Snapshot(ctx context.Context, projectID string) (domainscheduling.Snapshot, error) {
	launcher := store.launcher
	project, err := launcher.store.Project(ctx, projectID)
	if err != nil || project.Organizer == nil {
		return domainscheduling.Snapshot{}, storeport.ErrInvalidRequest
	}
	organizer, err := launcher.repository.Read(ctx, project.Organizer.RepositoryPath)
	if err != nil {
		return domainscheduling.Snapshot{}, err
	}
	configurationSnapshot, err := activeConfiguration(project, organizer)
	if err != nil {
		return domainscheduling.Snapshot{}, err
	}
	document := configurationSnapshot.Configuration()
	policy := domainscheduling.PolicyManual
	if document.Defaults.LaunchPolicy == domainconfig.LaunchAutomatic {
		policy = domainscheduling.PolicyAutomatic
	}
	leaseState, leaseEpoch := domainscheduling.LeaseLost, project.LastLeaseEpoch
	now := time.Now().UnixMilli()
	if project.Lease != nil {
		leaseEpoch = project.Lease.Epoch
		if project.Lease.DispatchAllowed && project.Lease.HolderInstance == launcher.holderInstance &&
			project.Lease.HolderProcessIdentity == launcher.processIdentity && now < project.Lease.ExpiresAtMillis {
			leaseState = domainscheduling.LeaseCurrent
		}
	}
	if leaseEpoch == 0 {
		leaseEpoch = 1
	}
	providerState := domainscheduling.ProviderReady
	discovery, discoveryErr := launcher.discovery.Discover(ctx)
	if discoveryErr != nil {
		providerState = domainscheduling.ProviderUnavailable
	} else {
		profiles, profileErr := configuration.NewProfileService(launcher.discovery)
		if profileErr != nil {
			providerState = domainscheduling.ProviderNotAdmitted
		} else if _, profileErr = profiles.FreezeObservedProfiles(configurationSnapshot, organizer.Revision, discovery,
			discovery.Revision, now); profileErr != nil {
			providerState = domainscheduling.ProviderNotAdmitted
		}
	}
	diskState := domainscheduling.DiskReady
	var filesystem syscall.Statfs_t
	if statErr := syscall.Statfs(launcher.runtimeRoot, &filesystem); statErr != nil || filesystem.Blocks == 0 {
		diskState = domainscheduling.DiskUnavailable
	} else if high, low := bits.Mul64(filesystem.Bavail, 10); high == 0 && low < filesystem.Blocks {
		diskState = domainscheduling.DiskLimitExceeded
	}
	tasks, err := launcher.store.Tasks(ctx, project.ID)
	if err != nil {
		return domainscheduling.Snapshot{}, err
	}
	workspaces, err := launcher.store.Workspaces(ctx, project.ID)
	if err != nil {
		return domainscheduling.Snapshot{}, err
	}
	workspaceByID := make(map[string]domain.Workspace, len(workspaces))
	for _, workspace := range workspaces {
		workspaceByID[workspace.ID] = workspace
	}
	activeByTask := map[string]bool{}
	startedCommands := map[string]bool{}
	activeAgents, activeTasks := uint64(0), uint64(0)
	workspaceUsage := map[string]domainscheduling.WorkspaceUsage{}
	for _, task := range tasks {
		runs, runErr := launcher.store.Runs(ctx, task.ID)
		if runErr != nil {
			return domainscheduling.Snapshot{}, runErr
		}
		for _, run := range runs {
			startedCommands[run.Execution.StartCommandID] = true
			if run.Execution.Terminal {
				continue
			}
			activeByTask[task.ID] = true
			activeTasks++
			activeAgents++
			for _, helper := range run.Execution.Helpers {
				if helper.NativeAgentID != "" && helper.Archive.Phase != domainexecution.EffectComplete {
					activeAgents++
				}
			}
			if run.Execution.Review != nil && run.Execution.Review.ReviewerUUID != "" &&
				run.Execution.Review.AgentCleanup.Phase != domainreview.EffectComplete {
				activeAgents++
			}
			usage := workspaceUsage[run.Execution.Scope.WorkspaceID]
			usage.Workspace = domainscheduling.WorkspaceID(run.Execution.Scope.WorkspaceID)
			usage.ActiveTasks++
			workspaceUsage[run.Execution.Scope.WorkspaceID] = usage
		}
	}
	outstanding := map[string]domain.SchedulingReservationRecord{}
	reservedTasks, reservedAgents := uint64(0), uint64(0)
	for _, record := range project.Scheduling.Reservations {
		if activeByTask[string(record.Reservation.TaskID)] || startedCommands[reservedLaunchRequestID(record.Reservation.ID)] {
			continue
		}
		outstanding[string(record.Reservation.TaskID)] = record
		reservedTasks += record.Reservation.Demand.ProjectTasks
		reservedAgents += record.Reservation.Demand.Agents
		usage := workspaceUsage[string(record.Reservation.Workspace)]
		usage.Workspace = record.Reservation.Workspace
		usage.ReservedTasks += record.Reservation.Demand.WorkspaceTasks
		workspaceUsage[string(record.Reservation.Workspace)] = usage
	}
	result := domainscheduling.Snapshot{SchemaVersion: domainscheduling.SchemaVersion, ProjectID: project.ID,
		Version: project.Version, Policy: policy, ProjectActive: project.State == "active", Lease: leaseState, LeaseEpoch: leaseEpoch,
		Disk: diskState, Provider: providerState,
		Limits: domainscheduling.Limits{MaxActiveTasks: uint64(document.Defaults.Limits.MaxActiveTasks),
			MaxActiveTasksPerWorkspace: uint64(document.Defaults.Limits.MaxActiveTasksPerWorkspace),
			MaxConcurrentAgents:        uint64(document.Defaults.Limits.MaxConcurrentAgents),
			MaxHelpersPerTask:          uint64(document.Defaults.Limits.MaxSubagentsPerTask)},
		Usage: domainscheduling.Usage{ActiveTasks: activeTasks, ReservedTasks: reservedTasks,
			ActiveAgents: activeAgents, ReservedAgents: reservedAgents}}
	for _, usage := range workspaceUsage {
		result.WorkspaceUsages = append(result.WorkspaceUsages, usage)
	}
	sort.Slice(result.WorkspaceUsages, func(left, right int) bool {
		return result.WorkspaceUsages[left].Workspace < result.WorkspaceUsages[right].Workspace
	})
	for _, task := range tasks {
		if task.Complete || activeByTask[task.ID] {
			continue
		}
		if _, reserved := outstanding[task.ID]; reserved {
			continue
		}
		workspace, present := workspaceByID[task.WorkspaceIDs[0]]
		if !present {
			continue
		}
		effective, effectiveErr := configurationSnapshot.Effective(workspace.Key, configuration.TaskOverride{})
		if effectiveErr != nil {
			return domainscheduling.Snapshot{}, effectiveErr
		}
		timeLimit, timeOK := asUint64(effective.RunBudget.ElapsedSeconds * 1_000)
		tokenLimit, tokenOK := asUint64(effective.RunBudget.Tokens)
		turnLimit, turnOK := asUint64(effective.RunBudget.Turns)
		costLimit, costOK := asUint64(effective.RunBudget.CostMicrousd)
		ciLimit, ciOK := asUint64(effective.RunBudget.CICycles)
		if !timeOK || !tokenOK || !turnOK || !costOK || !ciOK {
			return domainscheduling.Snapshot{}, errors.New("scheduler budget is invalid")
		}
		dependency, auditIDs, dependencyErr := schedulingDependencyState(ctx, launcher, task)
		if dependencyErr != nil {
			return domainscheduling.Snapshot{}, dependencyErr
		}
		override := domainscheduling.PolicyInherit
		if effective.LaunchPolicy != document.Defaults.LaunchPolicy {
			if effective.LaunchPolicy == domainconfig.LaunchAutomatic {
				override = domainscheduling.PolicyForceAutomatic
			} else {
				override = domainscheduling.PolicyForceManual
			}
		}
		priority := domainscheduling.Priority(task.Priority)
		if priority == "" {
			priority = domainscheduling.PriorityNormal
		}
		launchNow := domainscheduling.LaunchNowRequest{}
		if store.launchNowTask == task.ID {
			launchNow = store.launchNow
		}
		result.Tasks = append(result.Tasks, domainscheduling.TaskFacts{ID: domainscheduling.TaskID(task.ID),
			Workspace: domainscheduling.WorkspaceID(workspace.ID), TaskVersion: task.Version, QueuedAtUnixMillis: task.QueuedAtUnixMillis,
			Priority: priority, PolicyOverride: override, WorkClass: domainscheduling.WorkNewRun,
			Dependency: dependency, DependencyOverrideAuditIDs: auditIDs,
			DependencyOverrideVersion: func() uint64 {
				if len(auditIDs) > 0 {
					return task.Version
				}
				return 0
			}(),
			LaunchNow: launchNow, TaskComplete: task.Title != "" && task.Objective != "" && task.AcceptanceCriteria != "",
			OrganizerApproved: project.Organizer.Phase == domain.OrganizerPhaseActive, PreflightReady: task.Attention == nil,
			Demand: domainscheduling.NewRunDemand(), Budgets: domainscheduling.Budgets{
				Time:   schedulingBudget("time", configurationSnapshot.ConfigurationSHA256(), timeLimit, 60_000),
				Tokens: schedulingBudget("tokens", configurationSnapshot.ConfigurationSHA256(), tokenLimit, 1_000),
				Turns:  schedulingBudget("turns", configurationSnapshot.ConfigurationSHA256(), turnLimit, 1),
				Cost:   optionalSchedulingBudget("cost", configurationSnapshot.ConfigurationSHA256(), costLimit, 1),
				CI:     schedulingBudget("ci", configurationSnapshot.ConfigurationSHA256(), ciLimit, 1),
			}})
	}
	return result, nil
}

func sameSchedulingReservation(task domain.Task, reservation domainscheduling.Reservation) bool {
	return task.ID == string(reservation.TaskID) && len(task.WorkspaceIDs) == 1 &&
		task.WorkspaceIDs[0] == string(reservation.Workspace) && task.Version == reservation.TaskVersion && !task.Complete
}

func (store *productionSchedulingStore) prunedSchedulingLedger(ctx context.Context, project domain.Project) (domain.SchedulingLedger, error) {
	settled := make(map[string]bool, len(project.Scheduling.Reservations))
	for _, record := range project.Scheduling.Reservations {
		runs, err := store.launcher.store.Runs(ctx, string(record.Reservation.TaskID))
		if err != nil {
			return domain.SchedulingLedger{}, err
		}
		settled[record.Reservation.ID] = slices.ContainsFunc(runs, func(run domain.Run) bool {
			return run.Execution.StartCommandID == reservedLaunchRequestID(record.Reservation.ID)
		})
	}
	keepReservation := map[string]bool{}
	ledger := domain.SchedulingLedger{}
	for _, batch := range project.Scheduling.Batches {
		complete := true
		for _, id := range batch.ReservationIDs {
			complete = complete && settled[id]
		}
		if complete {
			continue
		}
		ledger.Batches = append(ledger.Batches, batch)
		for _, id := range batch.ReservationIDs {
			keepReservation[id] = true
		}
	}
	for _, reservation := range project.Scheduling.Reservations {
		if keepReservation[reservation.Reservation.ID] {
			ledger.Reservations = append(ledger.Reservations, reservation)
		}
	}
	for _, permit := range project.Scheduling.Permits {
		if keepReservation[permit.ReservationID] {
			ledger.Permits = append(ledger.Permits, permit)
		}
	}
	return ledger, nil
}

func (store *productionSchedulingStore) CompareAndReserve(ctx context.Context, batch storeport.ReservationBatch) (storeport.ReserveResult, error) {
	hash := schedulingHash(batch)
	if hash == "" || batch.ID == "" || len(batch.Reservations) == 0 {
		return storeport.ReserveResult{}, storeport.ErrInvalidRequest
	}
	project, err := store.launcher.store.Project(ctx, batch.ProjectID)
	if err != nil {
		return storeport.ReserveResult{}, err
	}
	for _, prior := range project.Scheduling.Batches {
		if prior.ID != batch.ID {
			continue
		}
		if prior.PayloadSHA256 != hash {
			return storeport.ReserveResult{}, storeport.ErrIdempotencyConflict
		}
		return storeport.ReserveResult{Outcome: storeport.ReserveApplied, SnapshotVersion: prior.SnapshotVersion, Replay: true}, nil
	}
	if project.Version != batch.ExpectedSnapshotVersion {
		return storeport.ReserveResult{Outcome: storeport.ReserveVersionConflict, SnapshotVersion: project.Version}, nil
	}
	if project.Lease == nil || project.Lease.Epoch != batch.LeaseEpoch || !project.Lease.DispatchAllowed ||
		project.Lease.HolderInstance != store.launcher.holderInstance || project.Lease.HolderProcessIdentity != store.launcher.processIdentity {
		return storeport.ReserveResult{Outcome: storeport.ReserveLeaseLost, SnapshotVersion: project.Version}, nil
	}
	reservationIDs := make([]string, 0, len(batch.Reservations))
	for _, reservation := range batch.Reservations {
		task, taskErr := store.launcher.store.Task(ctx, string(reservation.TaskID))
		if taskErr != nil || task.ProjectID != project.ID || !sameSchedulingReservation(task, reservation) {
			return storeport.ReserveResult{}, storeport.ErrInvalidRequest
		}
		for _, existing := range project.Scheduling.Reservations {
			if existing.Reservation.ID == reservation.ID || existing.Reservation.TaskID == reservation.TaskID && existing.PermitID == "" {
				return storeport.ReserveResult{}, storeport.ErrInvalidRequest
			}
		}
		reservationIDs = append(reservationIDs, reservation.ID)
	}
	pruned, err := store.prunedSchedulingLedger(ctx, project)
	if err != nil {
		return storeport.ReserveResult{}, err
	}
	next := project
	next.Version = project.Version + 1
	next.Scheduling = pruned
	next.Scheduling.Batches = append(next.Scheduling.Batches, domain.SchedulingBatchRecord{ID: batch.ID,
		PayloadSHA256: hash, Outcome: "applied", SnapshotVersion: next.Version, LeaseEpoch: batch.LeaseEpoch,
		ReservationIDs: reservationIDs})
	for _, reservation := range batch.Reservations {
		next.Scheduling.Reservations = append(next.Scheduling.Reservations, domain.SchedulingReservationRecord{Reservation: reservation})
	}
	result, err := store.launcher.store.UpdateProject(ctx, domain.CommandRequest{IdempotencyKey: batch.ID,
		Type: "scheduler.reserve", AggregateID: project.ID, ExpectedVersion: project.Version, Payload: productionPayload(batch)}, next,
		domain.Event{ID: productionStableID("scheduler-reserve-event", batch.ID), Sequence: next.Version + 1,
			AggregateID: project.ID, AggregateVersion: next.Version, Type: "scheduler.reserved",
			Payload: productionPayload(struct{ BatchID string }{batch.ID})})
	if err != nil {
		return storeport.ReserveResult{}, err
	}
	if result.Outcome != domain.CommandApplied {
		return storeport.ReserveResult{Outcome: storeport.ReserveVersionConflict, SnapshotVersion: result.ObservedVersion}, nil
	}
	return storeport.ReserveResult{Outcome: storeport.ReserveApplied, SnapshotVersion: next.Version, Replay: result.Replay}, nil
}

func (store *productionSchedulingStore) AuthorizeLaunch(ctx context.Context, request storeport.AuthorizeRequest) (storeport.Permit, error) {
	hash := schedulingHash(request)
	project, err := store.launcher.store.Project(ctx, request.ProjectID)
	if err != nil || hash == "" {
		return storeport.Permit{}, storeport.ErrInvalidRequest
	}
	for _, prior := range project.Scheduling.Permits {
		if prior.RequestID != request.RequestID {
			continue
		}
		if prior.PayloadSHA256 != hash {
			return storeport.Permit{}, storeport.ErrIdempotencyConflict
		}
		return storeport.Permit{ID: prior.PermitID, ReservationID: prior.ReservationID,
			LeaseEpoch: prior.LeaseEpoch, Allowed: prior.Allowed, Replay: true}, nil
	}
	reservationIndex := slices.IndexFunc(project.Scheduling.Reservations, func(value domain.SchedulingReservationRecord) bool {
		return value.Reservation.ID == request.ReservationID
	})
	if reservationIndex < 0 {
		return storeport.Permit{}, storeport.ErrReservationNotFound
	}
	reservation := project.Scheduling.Reservations[reservationIndex]
	allowed := reservation.PermitID == "" && project.Lease != nil && project.Lease.DispatchAllowed &&
		project.Lease.Epoch == request.LeaseEpoch && reservation.Reservation.LeaseEpoch == request.LeaseEpoch &&
		project.Lease.HolderInstance == store.launcher.holderInstance && project.Lease.HolderProcessIdentity == store.launcher.processIdentity &&
		time.Now().UnixMilli() < project.Lease.ExpiresAtMillis
	permitID := ""
	if allowed {
		permitID = schedulingHash(struct {
			Request     storeport.AuthorizeRequest
			Reservation domainscheduling.Reservation
		}{request, reservation.Reservation})
	}
	next := project
	next.Version = project.Version + 1
	next.Scheduling.Reservations = slices.Clone(project.Scheduling.Reservations)
	if allowed {
		next.Scheduling.Reservations[reservationIndex].PermitID = permitID
	}
	next.Scheduling.Permits = append(slices.Clone(project.Scheduling.Permits), domain.SchedulingPermitRecord{
		RequestID: request.RequestID, PayloadSHA256: hash, PermitID: permitID, ReservationID: request.ReservationID,
		LeaseEpoch: request.LeaseEpoch, Allowed: allowed})
	result, err := store.launcher.store.UpdateProject(ctx, domain.CommandRequest{IdempotencyKey: request.RequestID,
		Type: "scheduler.authorize", AggregateID: project.ID, ExpectedVersion: project.Version, Payload: productionPayload(request)}, next,
		domain.Event{ID: productionStableID("scheduler-authorize-event", request.RequestID), Sequence: next.Version + 1,
			AggregateID: project.ID, AggregateVersion: next.Version, Type: "scheduler.launch_authorized",
			Payload: productionPayload(struct {
				ReservationID string
				Allowed       bool
			}{request.ReservationID, allowed})})
	if err != nil {
		return storeport.Permit{}, err
	}
	if result.Outcome != domain.CommandApplied {
		return storeport.Permit{ReservationID: request.ReservationID, LeaseEpoch: request.LeaseEpoch}, nil
	}
	return storeport.Permit{ID: permitID, ReservationID: request.ReservationID, LeaseEpoch: request.LeaseEpoch,
		Allowed: allowed, Replay: result.Replay}, nil
}

func schedulerReservationID(projectID, requestID, taskID string) string {
	return stableID("scheduler-reservation", projectID, requestID, taskID)
}

func reservedLaunchRequestID(reservationID string) string {
	return stableID("reserved-launch", reservationID)
}

func (launcher *Launcher) scheduleTask(ctx context.Context, request planningapp.LaunchRequest) (domain.Run, error) {
	launcher.schedulerMu.Lock()
	defer launcher.schedulerMu.Unlock()
	task, err := launcher.store.Task(ctx, request.TaskID)
	if err != nil || task.Version != request.ExpectedTaskVersion ||
		(!request.Automatic && (request.ActorID == "" || request.ActorSessionID == "")) {
		return domain.Run{}, errors.New("Task launch binding changed")
	}
	project, err := launcher.store.Project(ctx, task.ProjectID)
	if err != nil {
		return domain.Run{}, err
	}
	project, err = launcher.ensureLease(ctx, project)
	if err != nil || project.Lease == nil {
		return domain.Run{}, errors.New("Project execution lease is unavailable")
	}
	runs, runErr := launcher.store.Runs(ctx, task.ID)
	if runErr != nil {
		return domain.Run{}, runErr
	}
	reservationID := ""
	for _, reservation := range project.Scheduling.Reservations {
		if string(reservation.Reservation.TaskID) != task.ID {
			continue
		}
		if index := slices.IndexFunc(runs, func(run domain.Run) bool {
			return run.Execution.StartCommandID == reservedLaunchRequestID(reservation.Reservation.ID)
		}); index >= 0 {
			return runs[index], nil
		}
		reservationID = reservation.Reservation.ID
		break
	}
	existing := reservationID != ""
	if !existing {
		scheduleID := stableID("scheduler-request", request.RequestID, fmt.Sprintf("project-version-%d", project.Version))
		reservationID = schedulerReservationID(project.ID, scheduleID, task.ID)
		launcher.schedulingStore.launchNowTask = task.ID
		launcher.schedulingStore.launchNow = domainscheduling.LaunchNowRequest{Requested: !request.Automatic,
			ActorKind: func() string {
				if request.Automatic {
					return ""
				}
				return "human"
			}(), ActorID: request.ActorID,
			AuditID: request.RequestID, TaskVersion: task.Version}
		result, scheduleErr := launcher.scheduler.Schedule(ctx, applicationscheduling.Command{RequestID: scheduleID, ProjectID: project.ID})
		launcher.schedulingStore.launchNowTask, launcher.schedulingStore.launchNow = "", domainscheduling.LaunchNowRequest{}
		if scheduleErr != nil {
			return domain.Run{}, scheduleErr
		}
		if !slices.Contains(result.Decision.OrderedTaskIDs, domainscheduling.TaskID(task.ID)) {
			code := "UNSELECTED"
			for _, explanation := range result.Decision.Explanations {
				if explanation.TaskID == domainscheduling.TaskID(task.ID) {
					code = strings.ToUpper(string(explanation.Code))
					break
				}
			}
			return domain.Run{}, launchRefused("LAUNCH_SCHEDULER_" + code)
		}
		if result.Reservation.Outcome != storeport.ReserveApplied {
			return domain.Run{}, launchRefused("LAUNCH_RESERVATION_" + strings.ToUpper(string(result.Reservation.Outcome)))
		}
	}
	authorizeID := stableID("scheduler-authorize-reservation", reservationID)
	permit, err := launcher.scheduler.AuthorizeLaunch(ctx, applicationscheduling.Command{RequestID: authorizeID, ProjectID: project.ID},
		reservationID, project.Lease.Epoch)
	if err != nil || !permit.Allowed {
		runs, _ := launcher.store.Runs(ctx, task.ID)
		for _, run := range runs {
			if !run.Execution.Terminal && run.Execution.StartCommandID == reservedLaunchRequestID(reservationID) {
				return run, nil
			}
		}
		return domain.Run{}, errors.New("Task launch permit is unavailable")
	}
	request.RequestID = reservedLaunchRequestID(reservationID)
	return launcher.launchTaskEffect(ctx, request)
}

func (launcher *Launcher) recoverScheduled(ctx context.Context, project domain.Project) error {
	for _, record := range project.Scheduling.Reservations {
		task, err := launcher.store.Task(ctx, string(record.Reservation.TaskID))
		if err != nil || task.Complete {
			continue
		}
		runs, err := launcher.store.Runs(ctx, task.ID)
		if err != nil {
			return err
		}
		if slices.ContainsFunc(runs, func(run domain.Run) bool { return !run.Execution.Terminal }) {
			continue
		}
		requestID := reservedLaunchRequestID(record.Reservation.ID)
		permitRequest := stableID("scheduler-authorize-reservation", record.Reservation.ID)
		permit, permitErr := launcher.scheduler.AuthorizeLaunch(ctx, applicationscheduling.Command{RequestID: permitRequest, ProjectID: project.ID},
			record.Reservation.ID, record.Reservation.LeaseEpoch)
		if permitErr != nil || !permit.Allowed {
			continue
		}
		_, _ = launcher.launchTaskEffect(ctx, planningapp.LaunchRequest{RequestID: requestID, TaskID: task.ID,
			ExpectedTaskVersion: task.Version, ActorID: "system/scheduler", ActorSessionID: "system/scheduler", Automatic: true})
	}
	return nil
}

func schedulerRequestID(projectID string, version uint64) string {
	return productionStableID("scheduler-cycle", projectID, fmt.Sprintf("version-%d", version))
}

func (launcher *Launcher) runScheduler(ctx context.Context) error {
	launcher.schedulerMu.Lock()
	defer launcher.schedulerMu.Unlock()
	projects, err := launcher.store.Projects(ctx)
	if err != nil {
		return err
	}
	for _, observed := range projects {
		if observed.State != "active" || observed.Organizer == nil || observed.Organizer.Phase != domain.OrganizerPhaseActive {
			continue
		}
		project, leaseErr := launcher.ensureLease(ctx, observed)
		if leaseErr != nil {
			continue
		}
		if recoveryErr := launcher.recoverScheduled(ctx, project); recoveryErr != nil {
			return recoveryErr
		}
		project, err = launcher.store.Project(ctx, project.ID)
		if err != nil {
			return err
		}
		requestID := schedulerRequestID(project.ID, project.Version)
		result, scheduleErr := launcher.scheduler.Schedule(ctx, applicationscheduling.Command{RequestID: requestID, ProjectID: project.ID})
		if scheduleErr != nil || result.Reservation.Outcome != storeport.ReserveApplied {
			continue
		}
		project, err = launcher.store.Project(ctx, project.ID)
		if err != nil {
			return err
		}
		if recoveryErr := launcher.recoverScheduled(ctx, project); recoveryErr != nil {
			return recoveryErr
		}
	}
	return nil
}

var _ storeport.Store = (*productionSchedulingStore)(nil)
