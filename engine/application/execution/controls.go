// SPDX-License-Identifier: Apache-2.0

package execution

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/mcuadros/director-engine/domain"
	domainexecution "github.com/mcuadros/director-engine/domain/execution"
	"github.com/mcuadros/director-engine/domain/runtimebudget"
	"github.com/mcuadros/director-engine/ports/host"
	storeport "github.com/mcuadros/director-engine/ports/taskstore"
	controlreducer "github.com/mcuadros/director-engine/reducer/control"
)

var (
	ErrControlInvalid             = errors.New("execution control command is invalid")
	ErrEmergencyConfirmationStale = errors.New("emergency stop confirmation is absent, stale, or belongs to another authenticated session")
)

// AuthenticatedControlActor is supplied by the trusted server ingress rather
// than decoded from the planning mutation. Emergency stop accepts only a
// server-authenticated human actor.
type AuthenticatedControlActor struct {
	Kind          domainexecution.ControlActorKind
	ID            string
	SessionID     string
	Source        string
	Authenticated bool
}

type ControlCommand struct {
	Kind                   domainexecution.ControlKind
	RequestID              string
	ProjectID              string
	TaskID                 string
	RunID                  string
	ExpectedProjectVersion uint64
	ExpectedTaskVersion    uint64
	ExpectedRunVersion     uint64
	Lease                  domainexecution.LeaseBinding
	Actor                  AuthenticatedControlActor
	NowMillis              int64
	ConfirmationID         string
}

type ControlResult struct {
	Project        domain.Project
	Run            *domain.Run
	Command        domain.CommandResult
	ConfirmationID string
	Replay         bool
}

type ReconcileControlCommand struct {
	RequestID string
	ProjectID string
	Lease     domainexecution.LeaseBinding
	NowMillis int64
}

type AgentRegistrationCommand struct {
	RequestID          string
	RunID              string
	ExpectedRunVersion uint64
	LeaseEpoch         uint64
	Identity           domainexecution.ControlledAgentIdentity
}

func validControlActor(actor AuthenticatedControlActor) bool {
	return actor.Authenticated && actor.Source == "server" && actor.ID != "" && actor.SessionID != "" &&
		(actor.Kind == domainexecution.ControlActorHuman || actor.Kind == domainexecution.ControlActorBudget ||
			actor.Kind == domainexecution.ControlActorCoordinator)
}

func controlIntent(command ControlCommand) domainexecution.ControlIntent {
	intent := domainexecution.ControlIntent{
		RequestID: command.RequestID, Kind: command.Kind, ProjectID: command.ProjectID,
		TaskID: command.TaskID, RunID: command.RunID, ActorKind: command.Actor.Kind,
		ActorID: command.Actor.ID, ActorSessionID: command.Actor.SessionID,
		Source: command.Actor.Source, Authenticated: command.Actor.Authenticated,
		RequestedAtMillis: command.NowMillis, ConfirmationID: command.ConfirmationID,
	}
	intent.ID = domainexecution.ControlIntentID(intent)
	return intent
}

func controlCommandKey(requestID string) string { return stableID("control-command", requestID) }

func controlProjectEvent(commandID, projectID, eventType string, version uint64, payload any) domain.Event {
	return domain.Event{
		ID: stableID("event", commandID), Sequence: version + 1, AggregateID: projectID,
		AggregateVersion: version, Type: eventType, Payload: eventPayload(payload),
	}
}

func (controller *Controller) replayControlCommand(
	ctx context.Context, command ControlCommand, commandType string,
) (ControlResult, bool, error) {
	stored, err := controller.store.Command(ctx, controlCommandKey(command.RequestID))
	if errors.Is(err, storeport.ErrNotFound) {
		return ControlResult{}, false, nil
	}
	if err != nil {
		return ControlResult{}, false, err
	}
	if stored.Type != commandType || stored.AggregateID != command.ProjectID ||
		stored.ExpectedVersion != command.ExpectedProjectVersion {
		return ControlResult{}, true, storeport.ErrIdempotencyConflict
	}
	var payload struct {
		Kind           domainexecution.ControlKind `json:"kind"`
		ConfirmationID string                      `json:"confirmationId,omitempty"`
		ActorID        string                      `json:"actorId"`
		ActorSessionID string                      `json:"actorSessionId"`
	}
	if err := json.Unmarshal(stored.Payload, &payload); err != nil || payload.Kind != command.Kind ||
		payload.ConfirmationID != command.ConfirmationID || payload.ActorID != command.Actor.ID ||
		payload.ActorSessionID != command.Actor.SessionID {
		return ControlResult{}, true, storeport.ErrIdempotencyConflict
	}
	project, err := controller.store.Project(ctx, command.ProjectID)
	if err != nil {
		return ControlResult{}, true, err
	}
	result := domain.CommandResult{
		Outcome: stored.Outcome, ObservedVersion: stored.ObservedVersion,
		EventID: stored.EventID, Replay: true,
	}
	confirmationID := ""
	if project.Control.Confirmation != nil {
		confirmationID = project.Control.Confirmation.ID
	}
	return ControlResult{Project: project, Command: result, ConfirmationID: confirmationID, Replay: true}, true, nil
}

func (controller *Controller) persistProjectControl(
	ctx context.Context, current, next domain.Project, command ControlCommand, commandType string,
) (ControlResult, error) {
	next.Version = current.Version + 1
	payload := struct {
		IntentID       string                      `json:"intentId"`
		Kind           domainexecution.ControlKind `json:"kind"`
		Generation     uint64                      `json:"generation"`
		ConfirmationID string                      `json:"confirmationId,omitempty"`
		ActorID        string                      `json:"actorId"`
		ActorSessionID string                      `json:"actorSessionId"`
	}{next.Control.Intent.ID, next.Control.Intent.Kind, next.Control.Generation, command.ConfirmationID,
		next.Control.Intent.ActorID, next.Control.Intent.ActorSessionID}
	commandID := controlCommandKey(command.RequestID)
	result, err := controller.store.UpdateProject(ctx, domain.CommandRequest{
		IdempotencyKey: commandID, Type: commandType, AggregateID: current.ID,
		ExpectedVersion: current.Version, Payload: eventPayload(payload),
	}, next, controlProjectEvent(commandID, current.ID, commandType, next.Version, payload))
	if err != nil {
		return ControlResult{}, err
	}
	project, readErr := controller.store.Project(ctx, current.ID)
	if readErr != nil {
		return ControlResult{}, readErr
	}
	confirmationID := ""
	if project.Control.Confirmation != nil {
		confirmationID = project.Control.Confirmation.ID
	}
	return ControlResult{
		Project: project, Command: result, ConfirmationID: confirmationID, Replay: result.Replay,
	}, nil
}

func (controller *Controller) validateProjectControlCommand(
	ctx context.Context, command ControlCommand,
) (domain.Project, error) {
	if !identifierPattern.MatchString(command.RequestID) || !identifierPattern.MatchString(command.ProjectID) ||
		command.NowMillis < 0 || !validControlActor(command.Actor) {
		return domain.Project{}, ErrControlInvalid
	}
	project, err := controller.store.Project(ctx, command.ProjectID)
	if err != nil {
		return domain.Project{}, err
	}
	if project.Version != command.ExpectedProjectVersion || project.State == "archived" ||
		!currentLease(project, command.Lease, command.NowMillis) {
		return domain.Project{}, ErrProjectLeaseUnavailable
	}
	return project, nil
}

// PrepareEmergencyStop mints one short-lived, exact-version confirmation. It
// does not pause, terminate, or infer consent.
func (controller *Controller) PrepareEmergencyStop(ctx context.Context, command ControlCommand) (ControlResult, error) {
	command.Kind = domainexecution.ControlEmergencyPrepare
	const commandType = "project.control.emergency_prepare"
	if replay, ok, err := controller.replayControlCommand(ctx, command, commandType); ok || err != nil {
		return replay, err
	}
	if command.Actor.Kind != domainexecution.ControlActorHuman || command.ConfirmationID != "" ||
		command.TaskID != "" || command.RunID != "" {
		return ControlResult{}, ErrControlInvalid
	}
	project, err := controller.validateProjectControlCommand(ctx, command)
	if err != nil {
		return ControlResult{}, err
	}
	if command.NowMillis > int64(^uint64(0)>>1)-domainexecution.EmergencyConfirmationTTL {
		return ControlResult{}, ErrControlInvalid
	}
	intent := controlIntent(command)
	confirmation := domainexecution.EmergencyConfirmation{
		ProjectID: project.ID, ExpectedProjectVersion: project.Version + 1,
		ActorID: command.Actor.ID, ActorSessionID: command.Actor.SessionID,
		IssuedAtMillis: command.NowMillis, ExpiresAtMillis: command.NowMillis + domainexecution.EmergencyConfirmationTTL,
		ChallengeSHA256: hashText(strings.Join([]string{project.ID, command.Actor.ID, command.Actor.SessionID, command.RequestID}, "\x1f")),
	}
	confirmation.ID = domainexecution.EmergencyConfirmationID(confirmation)
	next := project
	next.Control = domainexecution.ProjectControl{
		SchemaVersion: domainexecution.ProjectControlSchemaVersion, Generation: project.Control.Generation + 1,
		Intent: intent, Phase: domainexecution.ControlAwaitingConfirmation, Confirmation: &confirmation,
		ResumeRequired: project.Control.ResumeRequired, EmergencyLatched: project.Control.EmergencyLatched,
		ExplanationCode: "emergency_stop_confirmation_required",
	}
	return controller.persistProjectControl(ctx, project, next, command, commandType)
}

// RequestProjectControl admits Pause or reconcile-first Resume. It cannot be
// used for emergency stop, whose one-use confirmation is mandatory.
func (controller *Controller) RequestProjectControl(ctx context.Context, command ControlCommand) (ControlResult, error) {
	if command.Kind != domainexecution.ControlPauseProject && command.Kind != domainexecution.ControlResumeProject {
		return ControlResult{}, ErrControlInvalid
	}
	commandType := "project.control." + string(command.Kind)
	if replay, ok, err := controller.replayControlCommand(ctx, command, commandType); ok || err != nil {
		return replay, err
	}
	if command.TaskID != "" || command.RunID != "" || command.ConfirmationID != "" {
		return ControlResult{}, ErrControlInvalid
	}
	if command.Kind == domainexecution.ControlResumeProject && command.Actor.Kind != domainexecution.ControlActorHuman {
		return ControlResult{}, ErrControlInvalid
	}
	project, err := controller.validateProjectControlCommand(ctx, command)
	if err != nil {
		return ControlResult{}, err
	}
	if command.Kind == domainexecution.ControlResumeProject &&
		(project.State != "paused" || !project.Control.ResumeRequired || project.Control.Phase == domainexecution.ControlContaining) {
		return ControlResult{}, ErrControlInvalid
	}
	if command.Kind == domainexecution.ControlPauseProject && project.Control.EmergencyLatched {
		return ControlResult{}, ErrControlInvalid
	}
	intent := controlIntent(command)
	next := project
	next.State = "paused"
	next.Control = domainexecution.ProjectControl{
		SchemaVersion: domainexecution.ProjectControlSchemaVersion, Generation: project.Control.Generation + 1,
		Intent: intent, Phase: domainexecution.ControlIntentRecorded,
		ResumeRequired: true, EmergencyLatched: project.Control.EmergencyLatched,
		ExplanationCode: controlreducer.ReasonPausedSafeBoundary,
	}
	if command.Kind == domainexecution.ControlResumeProject {
		next.Control.Phase = domainexecution.ControlReconciling
		next.Control.ExplanationCode = controlreducer.ReasonResumeReconciliation
	}
	return controller.persistProjectControl(ctx, project, next, command, commandType)
}

// ConfirmEmergencyStop consumes only the current unexpired server-minted
// confirmation for the same authenticated human session and exact Project
// version. There is intentionally no HumanConfirmed boolean in this API.
func (controller *Controller) ConfirmEmergencyStop(ctx context.Context, command ControlCommand) (ControlResult, error) {
	command.Kind = domainexecution.ControlEmergencyStop
	const commandType = "project.control.emergency_stop"
	if replay, ok, err := controller.replayControlCommand(ctx, command, commandType); ok || err != nil {
		return replay, err
	}
	if command.Actor.Kind != domainexecution.ControlActorHuman || command.ConfirmationID == "" ||
		command.TaskID != "" || command.RunID != "" {
		return ControlResult{}, ErrControlInvalid
	}
	project, err := controller.validateProjectControlCommand(ctx, command)
	if err != nil {
		return ControlResult{}, err
	}
	confirmation := project.Control.Confirmation
	if project.Control.Intent.Kind != domainexecution.ControlEmergencyPrepare ||
		project.Control.Phase != domainexecution.ControlAwaitingConfirmation || confirmation == nil ||
		confirmation.ID != command.ConfirmationID || confirmation.ExpectedProjectVersion != project.Version ||
		confirmation.ActorID != command.Actor.ID || confirmation.ActorSessionID != command.Actor.SessionID ||
		confirmation.ConsumedAtMillis != 0 || command.NowMillis < confirmation.IssuedAtMillis ||
		command.NowMillis >= confirmation.ExpiresAtMillis {
		return ControlResult{}, ErrEmergencyConfirmationStale
	}
	intent := controlIntent(command)
	consumed := *confirmation
	consumed.ConsumedAtMillis = command.NowMillis
	next := project
	next.State = "paused"
	next.Control = domainexecution.ProjectControl{
		SchemaVersion: domainexecution.ProjectControlSchemaVersion, Generation: project.Control.Generation + 1,
		Intent: intent, Phase: domainexecution.ControlContaining, Confirmation: &consumed,
		ResumeRequired: true, EmergencyLatched: true, ExplanationCode: controlreducer.ReasonEmergencyStopped,
	}
	return controller.persistProjectControl(ctx, project, next, command, commandType)
}

func cloneControlledIdentity(identity domainexecution.ControlledAgentIdentity) domainexecution.ControlledAgentIdentity {
	identity.Labels = mapsClone(identity.Labels)
	return identity
}

func controlledAgentIndex(values []domainexecution.ControlledAgentIdentity, id string) int {
	return slices.IndexFunc(values, func(value domainexecution.ControlledAgentIdentity) bool { return value.ID == id })
}

// RegisterControlledAgent is the lifecycle-to-control seam used by primary,
// helper, and Reviewer paths. A connector or control request cannot add a
// target, and one Run can never register two live top-level agents of a role.
func (controller *Controller) RegisterControlledAgent(ctx context.Context, command AgentRegistrationCommand) (domain.Run, error) {
	if !identifierPattern.MatchString(command.RequestID) || !identifierPattern.MatchString(command.RunID) || command.LeaseEpoch == 0 {
		return domain.Run{}, ErrControlInvalid
	}
	run, err := controller.store.Run(ctx, command.RunID)
	if err != nil {
		return domain.Run{}, err
	}
	if run.Version != command.ExpectedRunVersion || run.Execution.LeaseBinding.Epoch != command.LeaseEpoch ||
		run.Execution.Terminal || run.Execution.Control.Phase == domainexecution.ControlCancelled ||
		!domainexecution.ValidControlledAgentIdentity(command.Identity, run.Execution.Scope) ||
		(command.Identity.Role != domainexecution.ControlledReviewer && command.Identity.WorkspaceID != run.Execution.HostView.ExternalID) {
		return domain.Run{}, ErrControlInvalid
	}
	if command.Identity.Role == domainexecution.ControlledHelper &&
		(command.Identity.ParentID == "" || command.Identity.ParentID != run.Execution.PrimarySession.NativeAgentID) {
		return domain.Run{}, ErrControlInvalid
	}
	if command.Identity.Role == domainexecution.ControlledTaskAgent && command.Identity.ID != run.Execution.PrimarySession.NativeAgentID {
		return domain.Run{}, ErrControlInvalid
	}
	if index := controlledAgentIndex(run.Execution.ControlledAgents, command.Identity.ID); index >= 0 {
		if reflect.DeepEqual(run.Execution.ControlledAgents[index], command.Identity) {
			return run, nil
		}
		return domain.Run{}, ErrControlInvalid
	}
	for _, existing := range run.Execution.ControlledAgents {
		if command.Identity.Role != domainexecution.ControlledHelper && existing.Role == command.Identity.Role {
			return domain.Run{}, ErrControlInvalid
		}
	}
	next := run
	next.Execution.ControlledAgents = append(slices.Clone(run.Execution.ControlledAgents), cloneControlledIdentity(command.Identity))
	sort.Slice(next.Execution.ControlledAgents, func(left, right int) bool {
		return next.Execution.ControlledAgents[left].ID < next.Execution.ControlledAgents[right].ID
	})
	if next.Execution.Control.SchemaVersion != "" {
		target := domainexecution.ControlledAgent{Identity: cloneControlledIdentity(command.Identity)}
		next.Execution.Control.Targets = append(domainexecution.CloneControlledAgents(next.Execution.Control.Targets), target)
		next.Execution.Control.TargetSetSHA256 = domainexecution.ControlledAgentSetSHA256(next.Execution.Control.Targets)
	}
	if err := controller.persistRun(ctx, run, next, "run.control_agent_registered"); err != nil {
		return domain.Run{}, err
	}
	return controller.store.Run(ctx, run.ID)
}

func recoveryFor(kind domainexecution.ControlKind, policy domainexecution.ControlPolicy) domainexecution.RecoveryIntent {
	mode := domainexecution.RecoveryMode("")
	switch kind {
	case domainexecution.ControlCancelTask:
		mode = policy.CancelRecovery
	case domainexecution.ControlEmergencyStop:
		mode = policy.EmergencyRecovery
	}
	return domainexecution.RecoveryIntent{Mode: mode}
}

func controlledHelperIdentity(run domain.Run, helper domainexecution.Helper) (domainexecution.ControlledAgentIdentity, bool) {
	if helper.NativeAgentID == "" {
		return domainexecution.ControlledAgentIdentity{}, false
	}
	registration, err := helperRegistration(run, helper)
	if err != nil {
		return domainexecution.ControlledAgentIdentity{}, false
	}
	labels, err := host.HelperLabels(registration)
	if err != nil {
		return domainexecution.ControlledAgentIdentity{}, false
	}
	recoveryPath := ""
	if helper.Mode == domainexecution.HelperWriter {
		recoveryPath = helper.WorktreePath
	}
	return domainexecution.ControlledAgentIdentity{
		ID: helper.NativeAgentID, Role: domainexecution.ControlledHelper, ParentID: helper.ParentAgentID,
		WorkspaceID: run.Execution.HostView.ExternalID, Title: helper.Title,
		WorktreePath: run.Execution.WorktreePath, RecoveryWorktreePath: recoveryPath,
		Labels: labels, ProfileSHA256: run.Execution.EffectiveProfilesSHA256,
	}, true
}

func newRunControl(run domain.Run, intent domainexecution.ControlIntent, generation uint64) domainexecution.RunControl {
	targets := make([]domainexecution.ControlledAgent, 0, len(run.Execution.ControlledAgents)+len(run.Execution.Helpers))
	for _, identity := range run.Execution.ControlledAgents {
		targets = append(targets, domainexecution.ControlledAgent{Identity: cloneControlledIdentity(identity)})
	}
	unresolvedAgents := false
	for _, helper := range run.Execution.Helpers {
		if helper.Phase == domainexecution.HelperTerminal {
			continue
		}
		if helper.NativeAgentID == "" {
			unresolvedAgents = true
			continue
		}
		if slices.IndexFunc(targets, func(target domainexecution.ControlledAgent) bool {
			return target.Identity.ID == helper.NativeAgentID
		}) >= 0 {
			continue
		}
		identity, ok := controlledHelperIdentity(run, helper)
		if !ok {
			unresolvedAgents = true
			continue
		}
		targets = append(targets, domainexecution.ControlledAgent{Identity: identity})
	}
	return domainexecution.RunControl{
		SchemaVersion: domainexecution.RunControlSchemaVersion, Intent: intent, ProjectGeneration: generation,
		Phase: domainexecution.ControlIntentRecorded, Targets: targets,
		TargetSetSHA256: domainexecution.ControlledAgentSetSHA256(targets), UnresolvedAgents: unresolvedAgents,
		Recovery: recoveryFor(intent.Kind, run.Execution.ControlPolicy), RelaunchBlocked: true,
		ExplanationCode: func() string {
			switch intent.Kind {
			case domainexecution.ControlPauseProject:
				return controlreducer.ReasonPausedSafeBoundary
			case domainexecution.ControlResumeProject:
				return controlreducer.ReasonResumeReconciliation
			case domainexecution.ControlCancelTask:
				return controlreducer.ReasonTaskCancelled
			default:
				return controlreducer.ReasonEmergencyStopped
			}
		}(),
	}
}

func (controller *Controller) refreshControlledHelpers(ctx context.Context, run domain.Run) (StepResult, bool, error) {
	if run.Execution.Control.SchemaVersion == "" {
		return StepResult{Run: run}, false, nil
	}
	next := run
	next.Execution.Control.Targets = domainexecution.CloneControlledAgents(run.Execution.Control.Targets)
	unresolved := false
	changed := false
	for _, helper := range run.Execution.Helpers {
		targetIndex := slices.IndexFunc(next.Execution.Control.Targets, func(target domainexecution.ControlledAgent) bool {
			return target.Identity.Role == domainexecution.ControlledHelper && target.Identity.ID == helper.NativeAgentID && helper.NativeAgentID != ""
		})
		if helper.Phase == domainexecution.HelperTerminal {
			if targetIndex >= 0 && (!next.Execution.Control.Targets[targetIndex].Archived || !next.Execution.Control.Targets[targetIndex].ProcessAbsent) {
				target := &next.Execution.Control.Targets[targetIndex]
				target.Archived, target.ProcessAbsent = true, true
				target.Archive = domainexecution.Effect{
					ID:   controlEffectID(run, run.Execution.Control.Intent.ID, domainexecution.EffectControlAgentArchive, helper.NativeAgentID),
					Kind: domainexecution.EffectControlAgentArchive, Phase: domainexecution.EffectComplete,
					AttemptLimit: 2, ExternalID: helper.NativeAgentID,
					ObservedFactHash: helper.Archive.ObservedFactHash, ObservedCorrelation: helper.Archive.ObservedCorrelation,
				}
				changed = true
			}
			continue
		}
		if helper.NativeAgentID == "" {
			unresolved = true
			continue
		}
		if targetIndex >= 0 {
			continue
		}
		identity, ok := controlledHelperIdentity(run, helper)
		if !ok {
			unresolved = true
			continue
		}
		next.Execution.Control.Targets = append(next.Execution.Control.Targets, domainexecution.ControlledAgent{Identity: identity})
		changed = true
	}
	if next.Execution.Control.UnresolvedAgents != unresolved {
		next.Execution.Control.UnresolvedAgents = unresolved
		changed = true
	}
	if changed {
		next.Execution.Control.TargetSetSHA256 = domainexecution.ControlledAgentSetSHA256(next.Execution.Control.Targets)
		persisted, progressed, err := controller.persistControlTransition(ctx, run, next, "run.control.helpers_reconciled")
		return StepResult{Run: persisted, Progressed: progressed}, true, err
	}
	return StepResult{Run: run}, false, nil
}

// CancelTask records only the selected active Task/Run saga. It does not
// mutate Project state or another Task's Run.
func (controller *Controller) CancelTask(ctx context.Context, command ControlCommand) (ControlResult, error) {
	command.Kind = domainexecution.ControlCancelTask
	if !identifierPattern.MatchString(command.TaskID) || !identifierPattern.MatchString(command.RunID) ||
		command.ConfirmationID != "" || command.Actor.Kind != domainexecution.ControlActorHuman {
		return ControlResult{}, ErrControlInvalid
	}
	transition := "run.control.cancel_task"
	commandID := stableID("command", command.RunID, fmt.Sprintf("version-%d", command.ExpectedRunVersion+1), transition)
	if stored, storedErr := controller.store.Command(ctx, commandID); storedErr == nil {
		var payload struct {
			Transition     string `json:"transition"`
			RequestID      string `json:"requestId"`
			TaskID         string `json:"taskId"`
			RunID          string `json:"runId"`
			ActorID        string `json:"actorId"`
			ActorSessionID string `json:"actorSessionId"`
		}
		if stored.Type != transition || stored.AggregateID != command.RunID ||
			stored.ExpectedVersion != command.ExpectedRunVersion || json.Unmarshal(stored.Payload, &payload) != nil ||
			payload.Transition != transition || payload.RequestID != command.RequestID || payload.TaskID != command.TaskID ||
			payload.RunID != command.RunID || payload.ActorID != command.Actor.ID || payload.ActorSessionID != command.Actor.SessionID {
			return ControlResult{}, storeport.ErrIdempotencyConflict
		}
		project, projectErr := controller.store.Project(ctx, command.ProjectID)
		if projectErr != nil {
			return ControlResult{}, projectErr
		}
		run, runErr := controller.store.Run(ctx, command.RunID)
		if runErr != nil {
			return ControlResult{}, runErr
		}
		return ControlResult{
			Project: project, Run: &run, Replay: true,
			Command: domain.CommandResult{
				Outcome: stored.Outcome, ObservedVersion: stored.ObservedVersion, EventID: stored.EventID, Replay: true,
			},
		}, nil
	} else if !errors.Is(storedErr, storeport.ErrNotFound) {
		return ControlResult{}, storedErr
	}
	project, err := controller.validateProjectControlCommand(ctx, command)
	if err != nil {
		return ControlResult{}, err
	}
	task, err := controller.store.Task(ctx, command.TaskID)
	if err != nil {
		return ControlResult{}, err
	}
	run, err := controller.store.Run(ctx, command.RunID)
	if err != nil {
		return ControlResult{}, err
	}
	if task.ProjectID != project.ID || run.TaskID != task.ID || task.Version != command.ExpectedTaskVersion ||
		run.Version != command.ExpectedRunVersion || run.Execution.Terminal || run.Execution.LeaseBinding != command.Lease {
		return ControlResult{}, ErrControlInvalid
	}
	intent := controlIntent(command)
	if run.Execution.Control.SchemaVersion != "" {
		if run.Execution.Control.Intent.ID == intent.ID {
			copy := run
			return ControlResult{Project: project, Run: &copy, Replay: true}, nil
		}
		return ControlResult{}, ErrControlInvalid
	}
	next := run
	next.Execution.Control = newRunControl(run, intent, project.Control.Generation+1)
	next.Execution.Control.Phase = domainexecution.ControlContaining
	next.Execution.NeedsYou = nil
	next.Version = run.Version + 1
	result, err := controller.store.UpdateRun(ctx, domain.CommandRequest{
		IdempotencyKey: commandID, Type: transition, AggregateID: run.ID,
		ExpectedVersion: run.Version, Payload: eventPayload(struct {
			Transition     string `json:"transition"`
			StateHash      string `json:"stateHash"`
			RequestID      string `json:"requestId"`
			TaskID         string `json:"taskId"`
			RunID          string `json:"runId"`
			ActorID        string `json:"actorId"`
			ActorSessionID string `json:"actorSessionId"`
		}{transition, hashState(next.Execution), command.RequestID, command.TaskID, command.RunID,
			command.Actor.ID, command.Actor.SessionID}),
	}, next, domain.Event{
		ID: stableID("event", commandID), RunID: run.ID, Sequence: next.Version + 1,
		AggregateID: run.ID, AggregateVersion: next.Version, Type: transition,
		Payload: durableTransitionPayload(transition, next.Execution),
	})
	if err != nil {
		return ControlResult{}, err
	}
	current, err := controller.store.Run(ctx, run.ID)
	if err != nil {
		return ControlResult{}, err
	}
	return ControlResult{Project: project, Run: &current, Command: result, Replay: result.Replay}, nil
}

func controlEffectID(run domain.Run, intentID string, kind domainexecution.EffectKind, targetID string) string {
	return stableID("effect", run.ID, intentID, string(kind), targetID)
}

func controlAgentArguments(run domain.Run, target domainexecution.ControlledAgentIdentity, effect domainexecution.Effect) host.Arguments {
	if target.Role == domainexecution.ControlledHelper {
		for _, helper := range run.Execution.Helpers {
			if helper.NativeAgentID != target.ID {
				continue
			}
			arguments, err := helperHostArguments(run, helper, effect)
			if err == nil {
				arguments.EffectKind = effect.Kind
				arguments.EffectID = effect.ID
				return arguments
			}
		}
	}
	var parent *string
	if target.ParentID != "" {
		value := target.ParentID
		parent = &value
	}
	return host.Arguments{
		Scope: run.Execution.Scope, EffectKind: effect.Kind, EffectID: effect.ID,
		WorkspaceID: target.WorkspaceID, AgentID: target.ID, Title: target.Title,
		WorktreePath: target.WorktreePath, ParentAgentID: parent, Labels: mapsClone(target.Labels),
		BindingHash: run.Execution.RepositoryBindingHash,
	}
}

func (controller *Controller) observeControlAgent(
	ctx context.Context, run domain.Run, index int, archive bool,
) (domainexecution.EffectObservation, error) {
	target := run.Execution.Control.Targets[index]
	effect := target.Boundary
	if archive {
		effect = target.Archive
	}
	if err := controller.verifyHost(ctx); err != nil {
		return domainexecution.EffectObservation{}, err
	}
	command := host.Command{
		RequestID:      stableID("request", effect.ID, "observe"),
		IdempotencyKey: stableID("idempotency", effect.ID, "observe"), ExpectedVersion: run.Version,
		Capability: host.CapabilityAgentObserve, AfterCursor: hostResumeCursor(run.Execution),
		Arguments: controlAgentArguments(run, target.Identity, effect),
	}
	observed, err := controller.host.Invoke(ctx, command)
	if err != nil {
		return domainexecution.EffectObservation{}, err
	}
	if err := host.ValidateObservation(command, observed); err != nil {
		return domainexecution.EffectObservation{}, err
	}
	return normalizedHostObservation(observed), nil
}

func normalizedHostObservation(observed host.Observation) domainexecution.EffectObservation {
	parsed, _ := timeParseRFC3339(observed.ObservedAt)
	result := domainexecution.EffectObservation{
		ID:       stableID("host-observation", observed.RequestID, fmt.Sprintf("cursor-%d", observed.Cursor)),
		EffectID: observed.Result.EffectID, Status: observed.Result.Status, ExternalID: observed.Result.ExternalID,
		BindingHash: observed.Result.BindingHash, CorrelationHash: observed.Result.CorrelationHash,
		Cursor: observed.Cursor, ObservedAt: observed.ObservedAt, ObservedAtMillis: parsed,
		MaximumAgeMillis: observed.Result.MaximumAgeMillis, PriorDispatcherAbsent: observed.Result.PriorDispatcherAbsent,
		Usage: observed.Result.Usage,
	}
	result.FactHash = domainexecution.EffectObservationHash(result)
	return result
}

func timeParseRFC3339(value string) (int64, error) {
	parsed, err := time.Parse(time.RFC3339Nano, value)
	if err != nil {
		return 0, err
	}
	return parsed.UnixMilli(), nil
}

func (controller *Controller) dispatchControlAgent(ctx context.Context, run domain.Run, index int) error {
	target := run.Execution.Control.Targets[index]
	effect := target.Archive
	if err := controller.verifyHost(ctx); err != nil {
		return err
	}
	command := host.Command{
		RequestID:      stableID("request", effect.ID, fmt.Sprintf("attempt-%d", effect.Attempt)),
		IdempotencyKey: effect.ID, ExpectedVersion: run.Version, Capability: host.CapabilityAgentArchive,
		Arguments: controlAgentArguments(run, target.Identity, effect),
	}
	_, err := controller.host.Invoke(ctx, command)
	return err
}

func (controller *Controller) persistControlTransition(ctx context.Context, current, next domain.Run, transition string) (domain.Run, bool, error) {
	won, err := controller.persistRunTransition(ctx, current, next, transition)
	if err == nil && won {
		next.Version = current.Version + 1
		return next, true, nil
	}
	if err != nil && !strings.Contains(err.Error(), "version conflict") {
		return current, false, err
	}
	reloaded, readErr := controller.store.Run(ctx, current.ID)
	if readErr != nil {
		return current, false, readErr
	}
	if reloaded.Execution.Control.Intent.ID != current.Execution.Control.Intent.ID {
		return reloaded, false, ErrControlInvalid
	}
	return reloaded, false, nil
}

func applyControlledHelperUsage(run *domain.Run, target *domainexecution.ControlledAgent, nowMillis int64) {
	if target.Identity.Role != domainexecution.ControlledHelper || target.Archive.Observation == nil {
		return
	}
	index := slices.IndexFunc(run.Execution.Helpers, func(helper domainexecution.Helper) bool {
		return helper.NativeAgentID == target.Identity.ID
	})
	if index < 0 {
		run.Execution.Control.PostPreservationNeedCode = domainexecution.NeedCode(controlreducer.ReasonControlRecoveryAmbiguous)
		return
	}
	helper := &run.Execution.Helpers[index]
	if helper.BudgetReservationID == "" || helper.BudgetEvidenceID != "" {
		return
	}
	usage := target.Archive.Observation.Usage
	if usage == nil {
		run.Execution.Control.PostPreservationNeedCode = domainexecution.NeedCode(runtimebudget.ReasonProviderUsageUnavailable)
		return
	}
	observation := runtimebudget.ProviderObservation{
		ID:       stableID("control-helper-usage", target.Archive.Observation.ID),
		EffectID: helper.BudgetEffectID, AgentID: helper.NativeAgentID,
		Activity: runtimebudget.ActivityHelperTurn, LeaseEpoch: run.Execution.LeaseBinding.Epoch,
		PolicyRevision: run.Execution.Budget.Policy.Revision, Sequence: target.Archive.Observation.Cursor,
		ObservedAtMillis: nowMillis, ProviderUsage: *usage,
	}
	observation.FactHash = runtimebudget.ProviderObservationHash(observation)
	ledger, decision, err := runtimebudget.ApplyProviderObservation(run.Execution.Budget, observation)
	if err != nil {
		run.Execution.Control.PostPreservationNeedCode = domainexecution.NeedCode(runtimebudget.ReasonProviderUsageAmbiguous)
		return
	}
	run.Execution.Budget = ledger
	for _, reservation := range ledger.Reservations {
		if reservation.ID == helper.BudgetReservationID && reservation.EffectID == helper.BudgetEffectID &&
			reservation.Released && reservation.EvidenceID == observation.ID {
			helper.BudgetEvidenceID = observation.ID
			break
		}
	}
	if decision.Disposition != runtimebudget.DispositionAllow {
		run.Execution.Control.PostPreservationNeedCode = domainexecution.NeedCode(decision.Reason)
	}
}

func (controller *Controller) parkControl(ctx context.Context, run domain.Run, code string) (StepResult, error) {
	next := run
	next.Execution.Control.Phase = domainexecution.ControlNeedsYou
	next.Execution.Control.ExplanationCode = code
	next.Execution.NeedsYou = &domainexecution.NeedsYou{
		Code: domainexecution.NeedCode(code), WakeCondition: "fresh_control_reconciliation_or_human_recovery",
		CleanupAuthorized: false,
	}
	persisted, progressed, err := controller.persistControlTransition(ctx, run, next, "run.control.needs_you")
	return StepResult{Run: persisted, Progressed: progressed}, err
}

func (controller *Controller) driveExistingCleanupEffect(
	ctx context.Context, run domain.Run, kind domainexecution.EffectKind, nowMillis int64,
) (StepResult, error) {
	effect := effectPointer(&run.Execution, kind)
	if effect == nil || effect.ID == "" {
		return StepResult{Run: run}, ErrControlInvalid
	}
	if effect.Observation == nil {
		return controller.applyEffectDecision(ctx, run, kind, "observe", nowMillis)
	}
	switch effect.Observation.Status {
	case domainexecution.ObservationDesired:
		return controller.applyEffectDecision(ctx, run, kind, "adopt", nowMillis)
	case domainexecution.ObservationOwnedPresent, domainexecution.ObservationAbsent:
		return controller.applyEffectDecision(ctx, run, kind, "dispatch", nowMillis)
	case domainexecution.ObservationUnavailable:
		return StepResult{Run: run}, nil
	default:
		return controller.parkControl(ctx, run, controlreducer.ReasonControlRecoveryAmbiguous)
	}
}

func (controller *Controller) stepControlledHelperCleanup(
	ctx context.Context, run domain.Run, nowMillis int64,
) (StepResult, bool, error) {
	if !run.Execution.Control.Recovery.Preserved {
		return StepResult{Run: run}, false, nil
	}
	indices := make([]int, 0, len(run.Execution.Helpers))
	for index := range run.Execution.Helpers {
		indices = append(indices, index)
	}
	sort.Slice(indices, func(left, right int) bool {
		return run.Execution.Helpers[indices[left]].ID < run.Execution.Helpers[indices[right]].ID
	})
	for _, index := range indices {
		helper := run.Execution.Helpers[index]
		if helper.Phase == domainexecution.HelperTerminal {
			continue
		}
		targetIndex := slices.IndexFunc(run.Execution.Control.Targets, func(target domainexecution.ControlledAgent) bool {
			return target.Identity.Role == domainexecution.ControlledHelper && target.Identity.ID == helper.NativeAgentID
		})
		if targetIndex < 0 || !run.Execution.Control.Targets[targetIndex].Archived ||
			!run.Execution.Control.Targets[targetIndex].ProcessAbsent {
			return StepResult{Run: run}, false, nil
		}
		if controller.helperRuntime == nil {
			result, err := controller.parkControl(ctx, run, controlreducer.ReasonControlRecoveryAmbiguous)
			return result, true, err
		}
		target := run.Execution.Control.Targets[targetIndex]
		if helper.Archive.Phase != domainexecution.EffectComplete || helper.Phase != domainexecution.HelperCleanupRequested {
			next := run
			next.Execution.Helpers = domainexecution.CloneHelpers(run.Execution.Helpers)
			nextHelper := &next.Execution.Helpers[index]
			nextHelper.Archive = newEffect(run.ID+"-"+helper.ID, domainexecution.EffectHelperAgentArchive, 2)
			nextHelper.Archive.Phase = domainexecution.EffectComplete
			nextHelper.Archive.ExternalID = helper.NativeAgentID
			nextHelper.Archive.ObservedFactHash = target.Archive.ObservedFactHash
			nextHelper.Archive.ObservedCorrelation = target.Archive.ObservedCorrelation
			nextHelper.Phase = domainexecution.HelperCleanupRequested
			nextHelper.NeedsYou = nil
			if run.Execution.Control.Recovery.Mode == domainexecution.RecoverySnapshotThenDelete &&
				run.Execution.Control.PostPreservationNeedCode == "" && helper.Mode == domainexecution.HelperWriter {
				nextHelper.CheckoutRemove = newEffect(run.ID+"-"+helper.ID, domainexecution.EffectHelperCheckoutRemove, 1)
			}
			persisted, progressed, err := controller.persistControlTransition(ctx, run, next, "run.control.helper_archive_adopted")
			return StepResult{Run: persisted, Progressed: progressed}, true, err
		}
		if run.Execution.Control.Recovery.Mode == domainexecution.RecoveryRetain ||
			run.Execution.Control.PostPreservationNeedCode != "" || helper.Mode == domainexecution.HelperReadOnly {
			if err := controller.helperRuntime.ReleaseHelperCapacity(ctx, helper.CapacityReservationID, run.Execution.Scope); err != nil {
				result, parkErr := controller.parkControl(ctx, run, controlreducer.ReasonControlRecoveryAmbiguous)
				return result, true, parkErr
			}
			next := run
			next.Execution.Helpers = domainexecution.CloneHelpers(run.Execution.Helpers)
			next.Execution.Helpers[index].Phase = domainexecution.HelperTerminal
			persisted, progressed, err := controller.persistControlTransition(ctx, run, next, "run.control.helper_terminal")
			return StepResult{Run: persisted, Progressed: progressed}, true, err
		}
		result, err := controller.StepHelper(ctx, run.ID, helper.ID, nowMillis)
		return StepResult{Run: result.Run, Progressed: result.Progressed}, true, err
	}
	return StepResult{Run: run}, false, nil
}

func (controller *Controller) stepControlPausedEvidence(ctx context.Context, run domain.Run, nowMillis int64) (StepResult, error) {
	for _, kind := range []domainexecution.EffectKind{
		domainexecution.EffectWorktreeCreate, domainexecution.EffectHostViewCreate,
		domainexecution.EffectBoundaryMaterialize, domainexecution.EffectSetupRun,
		domainexecution.EffectAgentCreate, domainexecution.EffectAgentPrompt,
	} {
		effect := effectPointer(&run.Execution, kind)
		if effect == nil || effect.ID == "" || effect.Phase == domainexecution.EffectComplete {
			continue
		}
		if effect.Observation == nil {
			return controller.applyEffectDecision(ctx, run, kind, "observe", nowMillis)
		}
		if effect.Observation.Status == domainexecution.ObservationDesired {
			return controller.applyEffectDecision(ctx, run, kind, "adopt", nowMillis)
		}
		return StepResult{Run: run}, nil
	}
	for _, helper := range run.Execution.Helpers {
		for _, kind := range []domainexecution.EffectKind{
			domainexecution.EffectHelperCheckoutCreate, domainexecution.EffectHelperBoundary,
			domainexecution.EffectHelperCommitHandoff, domainexecution.EffectHelperCheckoutRemove,
		} {
			effect := helperEffect(&helper, kind)
			if effect == nil || effect.ID == "" || effect.Phase == domainexecution.EffectComplete {
				continue
			}
			if effect.Observation == nil {
				result, err := controller.persistHelperEffect(ctx, run, helper.ID, kind, "observe")
				return StepResult{Run: result.Run, Progressed: result.Progressed}, err
			}
			if effect.Observation.Status == domainexecution.ObservationDesired {
				result, err := controller.persistHelperEffect(ctx, run, helper.ID, kind, "adopt")
				return StepResult{Run: result.Run, Progressed: result.Progressed}, err
			}
			return StepResult{Run: run}, nil
		}
	}
	return StepResult{Run: run}, nil
}

// stepRunControl advances one durable frontier. Control containment is checked
// before budget gates and orders helper -> Reviewer -> Task Agent -> recovery
// -> host view -> owned worktree.
func (controller *Controller) stepRunControl(ctx context.Context, run domain.Run, nowMillis int64) (StepResult, error) {
	if refreshed, handled, err := controller.refreshControlledHelpers(ctx, run); handled || err != nil {
		return refreshed, err
	}
	if run.Execution.Control.Phase == domainexecution.ControlPaused {
		return controller.stepControlPausedEvidence(ctx, run, nowMillis)
	}
	control := run.Execution.Control
	if helperResult, handled, err := controller.stepControlledHelperCleanup(ctx, run, nowMillis); handled || err != nil {
		return helperResult, err
	}
	decision := controlreducer.Reduce(controlreducer.Facts{
		SchemaVersion: controlreducer.SchemaVersion, ProjectState: "paused",
		ProjectGeneration: control.ProjectGeneration, Control: control,
		TrackedAgentsSHA256: domainexecution.ControlledAgentSetSHA256(control.Targets),
		ResumeReconciled:    control.ResumeReconciliationID != "",
		HostViewArchive:     run.Execution.HostViewArchive, WorktreeRemove: run.Execution.WorktreeRemove,
	})
	switch decision.Kind {
	case controlreducer.DecisionWaitSafeBoundary:
		return StepResult{Run: run}, nil
	case controlreducer.DecisionWaitExternal:
		next := run
		next.Execution.Control.ExplanationCode = controlreducer.ReasonControlExternalWait
		if decision.TargetIndex >= 0 && decision.TargetIndex < len(next.Execution.Control.Targets) {
			target := &next.Execution.Control.Targets[decision.TargetIndex]
			if decision.EffectKind == domainexecution.EffectControlAgentBoundary {
				target.Boundary.Observation = nil
			} else if decision.EffectKind == domainexecution.EffectControlAgentArchive {
				target.Archive.Observation = nil
			}
		} else if decision.EffectKind == domainexecution.EffectRecoverySnapshot {
			next.Execution.Control.Recovery.Snapshot.Observation = nil
		}
		persisted, progressed, err := controller.persistControlTransition(ctx, run, next, "run.control.waiting_external")
		return StepResult{Run: persisted, Progressed: progressed}, err
	case controlreducer.DecisionEscalate:
		return controller.parkControl(ctx, run, decision.ReasonCode)
	case controlreducer.DecisionCreateBoundaryIntent:
		next := run
		target := &next.Execution.Control.Targets[decision.TargetIndex]
		target.Boundary = domainexecution.Effect{
			ID:   controlEffectID(run, control.Intent.ID, domainexecution.EffectControlAgentBoundary, target.Identity.ID),
			Kind: domainexecution.EffectControlAgentBoundary, Phase: domainexecution.EffectIntentRecorded, AttemptLimit: 1,
		}
		next.Execution.Control.Phase = domainexecution.ControlAwaitingSafeBoundary
		persisted, progressed, err := controller.persistControlTransition(ctx, run, next, "run.control.boundary_intent")
		return StepResult{Run: persisted, Progressed: progressed}, err
	case controlreducer.DecisionObserveBoundary, controlreducer.DecisionObserveArchive:
		archive := decision.Kind == controlreducer.DecisionObserveArchive
		observation, err := controller.observeControlAgent(ctx, run, decision.TargetIndex, archive)
		if err != nil {
			return StepResult{Run: run}, err
		}
		next := run
		target := &next.Execution.Control.Targets[decision.TargetIndex]
		if archive {
			target.Archive.Observation = &observation
		} else {
			target.Boundary.Observation = &observation
		}
		persisted, progressed, err := controller.persistControlTransition(ctx, run, next, "run.control.agent_observed")
		return StepResult{Run: persisted, Progressed: progressed}, err
	case controlreducer.DecisionAdoptBoundary:
		next := run
		target := &next.Execution.Control.Targets[decision.TargetIndex]
		target.Boundary.Phase = domainexecution.EffectComplete
		target.Boundary.ObservedFactHash = target.Boundary.Observation.FactHash
		target.Boundary.Observation = nil
		persisted, progressed, err := controller.persistControlTransition(ctx, run, next, "run.control.safe_boundary")
		return StepResult{Run: persisted, Progressed: progressed}, err
	case controlreducer.DecisionMarkPaused:
		next := run
		next.Execution.Control.Phase = domainexecution.ControlPaused
		next.Execution.Control.ExplanationCode = decision.ReasonCode
		persisted, progressed, err := controller.persistControlTransition(ctx, run, next, "run.control.paused")
		return StepResult{Run: persisted, Progressed: progressed}, err
	case controlreducer.DecisionReconcileResume:
		return controller.reconcileResumeRun(ctx, run, nowMillis)
	case controlreducer.DecisionCompleteResume:
		next := run
		next.Execution.Control.Phase = domainexecution.ControlComplete
		next.Execution.Control.CompletedAtMillis = nowMillis
		next.Execution.Control.ExplanationCode = decision.ReasonCode
		persisted, progressed, err := controller.persistControlTransition(ctx, run, next, "run.control.resume_complete")
		return StepResult{Run: persisted, Progressed: progressed}, err
	case controlreducer.DecisionCreateArchiveIntent:
		next := run
		target := &next.Execution.Control.Targets[decision.TargetIndex]
		target.Archive = domainexecution.Effect{
			ID:   controlEffectID(run, control.Intent.ID, domainexecution.EffectControlAgentArchive, target.Identity.ID),
			Kind: domainexecution.EffectControlAgentArchive, Phase: domainexecution.EffectIntentRecorded, AttemptLimit: 2,
		}
		next.Execution.Control.Phase = domainexecution.ControlContaining
		persisted, progressed, err := controller.persistControlTransition(ctx, run, next, "run.control.archive_intent")
		return StepResult{Run: persisted, Progressed: progressed}, err
	case controlreducer.DecisionDispatchArchive:
		next := run
		target := &next.Execution.Control.Targets[decision.TargetIndex]
		target.Archive.Phase = domainexecution.EffectDispatching
		target.Archive.Attempt++
		target.Archive.Observation = nil
		persisted, won, err := controller.persistControlTransition(ctx, run, next, "run.control.archive_dispatching")
		if err != nil || !won {
			return StepResult{Run: persisted, Progressed: won}, err
		}
		if err := controller.dispatchControlAgent(ctx, persisted, decision.TargetIndex); err != nil {
			return StepResult{Run: persisted, Progressed: true}, &EffectHandoffError{Kind: domainexecution.EffectControlAgentArchive, err: err}
		}
		return StepResult{Run: persisted, Progressed: true}, nil
	case controlreducer.DecisionAdoptArchive:
		next := run
		next.Execution.Helpers = domainexecution.CloneHelpers(run.Execution.Helpers)
		target := &next.Execution.Control.Targets[decision.TargetIndex]
		applyControlledHelperUsage(&next, target, nowMillis)
		target.Archive.Phase = domainexecution.EffectComplete
		target.Archive.ExternalID = target.Identity.ID
		target.Archive.ObservedFactHash = target.Archive.Observation.FactHash
		target.Archive.ObservedCorrelation = target.Archive.Observation.CorrelationHash
		target.Archived = true
		target.ProcessAbsent = target.Archive.Observation.PriorDispatcherAbsent
		target.Archive.Observation = nil
		persisted, progressed, err := controller.persistControlTransition(ctx, run, next, "run.control.agent_archived")
		return StepResult{Run: persisted, Progressed: progressed}, err
	case controlreducer.DecisionCreateRecoveryIntent:
		next := run
		next.Execution.Control.Phase = domainexecution.ControlPreserving
		next.Execution.Control.Recovery.Snapshot = domainexecution.Effect{
			ID:   controlEffectID(run, control.Intent.ID, domainexecution.EffectRecoverySnapshot, run.ID),
			Kind: domainexecution.EffectRecoverySnapshot, Phase: domainexecution.EffectIntentRecorded, AttemptLimit: 2,
		}
		persisted, progressed, err := controller.persistControlTransition(ctx, run, next, "run.control.recovery_intent")
		return StepResult{Run: persisted, Progressed: progressed}, err
	case controlreducer.DecisionObserveRecovery:
		observation, err := controller.runtime.ObserveEffect(ctx, runtimeRequest(run, run.Execution.Control.Recovery.Snapshot))
		if err != nil {
			return StepResult{Run: run}, err
		}
		next := run
		next.Execution.Control.Recovery.Snapshot.Observation = &observation
		persisted, progressed, err := controller.persistControlTransition(ctx, run, next, "run.control.recovery_observed")
		return StepResult{Run: persisted, Progressed: progressed}, err
	case controlreducer.DecisionDispatchRecovery:
		next := run
		effect := &next.Execution.Control.Recovery.Snapshot
		effect.Phase = domainexecution.EffectDispatching
		effect.Attempt++
		effect.Observation = nil
		persisted, won, err := controller.persistControlTransition(ctx, run, next, "run.control.recovery_dispatching")
		if err != nil || !won {
			return StepResult{Run: persisted, Progressed: won}, err
		}
		if err := controller.runtime.DispatchEffect(ctx, runtimeRequest(persisted, persisted.Execution.Control.Recovery.Snapshot)); err != nil {
			return StepResult{Run: persisted, Progressed: true}, &EffectHandoffError{Kind: domainexecution.EffectRecoverySnapshot, err: err}
		}
		return StepResult{Run: persisted, Progressed: true}, nil
	case controlreducer.DecisionAdoptRecovery:
		next := run
		recovery := &next.Execution.Control.Recovery
		if recovery.Snapshot.Observation != nil {
			recovery.Snapshot.Phase = domainexecution.EffectComplete
			recovery.Snapshot.ExternalID = recovery.Snapshot.Observation.ExternalID
			recovery.Snapshot.ObservedFactHash = recovery.Snapshot.Observation.FactHash
			recovery.Snapshot.Observation = nil
		}
		if recovery.Snapshot.Phase != domainexecution.EffectComplete || recovery.Snapshot.ExternalID == "" {
			return controller.parkControl(ctx, run, controlreducer.ReasonControlRecoveryAmbiguous)
		}
		if nowMillis > int64(^uint64(0)>>1)-run.Execution.ControlPolicy.RetentionMillis {
			return controller.parkControl(ctx, run, controlreducer.ReasonControlRecoveryAmbiguous)
		}
		recovery.ArtifactID = recovery.Snapshot.ExternalID
		recovery.Preserved = true
		recovery.RetentionUntilMillis = nowMillis + run.Execution.ControlPolicy.RetentionMillis
		recovery.CleanupAuthorized = true
		next.Execution.Control.Phase = domainexecution.ControlCleaning
		persisted, progressed, err := controller.persistControlTransition(ctx, run, next, "run.control.recovery_preserved")
		return StepResult{Run: persisted, Progressed: progressed}, err
	case controlreducer.DecisionPreserveRetained:
		if nowMillis > int64(^uint64(0)>>1)-run.Execution.ControlPolicy.RetentionMillis {
			return controller.parkControl(ctx, run, controlreducer.ReasonControlRecoveryAmbiguous)
		}
		next := run
		next.Execution.Control.Recovery.Preserved = true
		next.Execution.Control.Recovery.RetentionUntilMillis = nowMillis + run.Execution.ControlPolicy.RetentionMillis
		next.Execution.Control.Recovery.CleanupAuthorized = false
		persisted, progressed, err := controller.persistControlTransition(ctx, run, next, "run.control.worktree_retained")
		return StepResult{Run: persisted, Progressed: progressed}, err
	case controlreducer.DecisionCreateHostArchive:
		next := run
		next.Execution.HostViewArchive = newEffect(run.ID, domainexecution.EffectHostViewArchive, 2)
		persisted, progressed, err := controller.persistControlTransition(ctx, run, next, "run.control.host_view_archive_intent")
		return StepResult{Run: persisted, Progressed: progressed}, err
	case controlreducer.DecisionDriveHostArchive:
		return controller.driveExistingCleanupEffect(ctx, run, domainexecution.EffectHostViewArchive, nowMillis)
	case controlreducer.DecisionCreateWorktreeRemove:
		next := run
		next.Execution.Control.Recovery.WorktreeRemovalReady = true
		next.Execution.WorktreeRemove = newEffect(run.ID, domainexecution.EffectWorktreeRemove, 1)
		persisted, progressed, err := controller.persistControlTransition(ctx, run, next, "run.control.worktree_remove_intent")
		return StepResult{Run: persisted, Progressed: progressed}, err
	case controlreducer.DecisionDriveWorktreeRemove:
		return controller.driveExistingCleanupEffect(ctx, run, domainexecution.EffectWorktreeRemove, nowMillis)
	case controlreducer.DecisionMarkCancelled:
		next := run
		next.Execution.Control.Phase = domainexecution.ControlCancelled
		next.Execution.Control.CompletedAtMillis = nowMillis
		next.Execution.Terminal = true
		persisted, progressed, err := controller.persistControlTransition(ctx, run, next, "run.control.cancelled")
		return StepResult{Run: persisted, Progressed: progressed}, err
	case controlreducer.DecisionComplete:
		return StepResult{Run: run}, nil
	default:
		return StepResult{Run: run}, ErrControlInvalid
	}
}

func (controller *Controller) reconcileResumeRun(ctx context.Context, run domain.Run, nowMillis int64) (StepResult, error) {
	project, err := controller.store.Project(ctx, run.Execution.Scope.ProjectID)
	if err != nil {
		return StepResult{Run: run}, err
	}
	task, err := controller.store.Task(ctx, run.TaskID)
	if err != nil {
		return StepResult{Run: run}, err
	}
	durable, err := controller.scanDurableRun(ctx, project, task, run)
	if err != nil {
		return controller.parkControl(ctx, run, controlreducer.ReasonControlRecoveryAmbiguous)
	}
	observed, err := controller.observeStartupRun(ctx, durable, nowMillis)
	if err != nil {
		return StepResult{Run: run}, err
	}
	reconciled, err := controller.persistStartupRun(ctx, StartupCommand{
		SchemaVersion: StartupCommandSchemaVersion,
		RequestID:     stableID("resume-reconciliation", run.Execution.Control.Intent.ID), NowMillis: nowMillis,
	}, observed)
	if err != nil {
		return StepResult{Run: run}, err
	}
	if reconciled.Execution.LastStartupReconciliation == nil {
		return controller.parkControl(ctx, reconciled, controlreducer.ReasonControlRecoveryAmbiguous)
	}
	next := reconciled
	next.Execution.Control.ResumeReconciliationID = reconciled.Execution.LastStartupReconciliation.ID
	persisted, progressed, err := controller.persistControlTransition(ctx, reconciled, next, "run.control.resume_reconciled")
	return StepResult{Run: persisted, Progressed: progressed || reconciled.Version != run.Version}, err
}

func (controller *Controller) activeProjectRuns(ctx context.Context, projectID string) ([]domain.Run, error) {
	tasks, err := controller.store.Tasks(ctx, projectID)
	if err != nil {
		return nil, err
	}
	sort.Slice(tasks, func(left, right int) bool { return tasks[left].ID < tasks[right].ID })
	var result []domain.Run
	for _, task := range tasks {
		runs, err := controller.store.Runs(ctx, task.ID)
		if err != nil {
			return nil, err
		}
		for _, run := range runs {
			if run.Execution.SchemaVersion != "" && !run.Execution.Terminal {
				result = append(result, run)
			}
		}
	}
	sort.Slice(result, func(left, right int) bool { return result[left].ID < result[right].ID })
	return result, nil
}

// ReconcileProjectControl materializes the Project intent into every active
// Run, advances each by at most one frontier, then completes the Project latch
// only after read-back proves all Runs reached the required state.
func (controller *Controller) ReconcileProjectControl(ctx context.Context, command ReconcileControlCommand) (ControlResult, error) {
	if !identifierPattern.MatchString(command.RequestID) || !identifierPattern.MatchString(command.ProjectID) || command.NowMillis < 0 {
		return ControlResult{}, ErrControlInvalid
	}
	project, err := controller.store.Project(ctx, command.ProjectID)
	if err != nil {
		return ControlResult{}, err
	}
	if project.Control.SchemaVersion == "" || project.Control.Phase == domainexecution.ControlAwaitingConfirmation ||
		project.Control.Phase == domainexecution.ControlPaused || project.Control.Phase == domainexecution.ControlComplete {
		return ControlResult{Project: project}, nil
	}
	if !currentLease(project, command.Lease, command.NowMillis) {
		return ControlResult{}, ErrProjectLeaseUnavailable
	}
	runs, err := controller.activeProjectRuns(ctx, project.ID)
	if err != nil {
		return ControlResult{}, err
	}
	for _, run := range runs {
		current := run
		if err := controller.currentExecutionAuthority(ctx, current, command.NowMillis); err != nil {
			return ControlResult{}, err
		}
		if current.Execution.Control.SchemaVersion != "" &&
			current.Execution.Control.Intent.Kind == domainexecution.ControlCancelTask &&
			current.Execution.Control.Phase != domainexecution.ControlCancelled &&
			current.Execution.Control.Phase != domainexecution.ControlComplete {
			if _, stepErr := controller.stepRunControl(ctx, current, command.NowMillis); stepErr != nil {
				var handoff *EffectHandoffError
				if !errors.As(stepErr, &handoff) {
					return ControlResult{}, stepErr
				}
			}
			continue
		}
		if current.Execution.Control.Intent.ID != project.Control.Intent.ID {
			next := current
			next.Execution.Control = newRunControl(current, project.Control.Intent, project.Control.Generation)
			switch project.Control.Intent.Kind {
			case domainexecution.ControlPauseProject:
				next.Execution.Control.Phase = domainexecution.ControlAwaitingSafeBoundary
			case domainexecution.ControlResumeProject:
				next.Execution.Control.Phase = domainexecution.ControlReconciling
			case domainexecution.ControlEmergencyStop:
				next.Execution.Control.Phase = domainexecution.ControlContaining
				next.Execution.NeedsYou = nil
			}
			persisted, _, persistErr := controller.persistControlTransition(ctx, current, next, "run.control.project_intent")
			if persistErr != nil {
				return ControlResult{}, persistErr
			}
			current = persisted
		}
		if _, stepErr := controller.stepRunControl(ctx, current, command.NowMillis); stepErr != nil {
			var handoff *EffectHandoffError
			if !errors.As(stepErr, &handoff) {
				return ControlResult{}, stepErr
			}
		}
	}
	runs, err = controller.activeProjectRuns(ctx, project.ID)
	if err != nil {
		return ControlResult{}, err
	}
	allComplete := true
	for _, run := range runs {
		if run.Execution.Control.Intent.ID != project.Control.Intent.ID {
			allComplete = false
			break
		}
		switch project.Control.Intent.Kind {
		case domainexecution.ControlPauseProject:
			allComplete = allComplete && run.Execution.Control.Phase == domainexecution.ControlPaused
		case domainexecution.ControlResumeProject:
			allComplete = allComplete && run.Execution.Control.Phase == domainexecution.ControlComplete
		case domainexecution.ControlEmergencyStop:
			allComplete = false // terminal Runs leave activeProjectRuns; checked below through all Runs.
		}
	}
	if project.Control.Intent.Kind == domainexecution.ControlEmergencyStop {
		allComplete = len(runs) == 0
	}
	project, err = controller.store.Project(ctx, project.ID)
	if err != nil || !allComplete {
		return ControlResult{Project: project}, err
	}
	next := project
	next.Control.CompletedAtMillis = command.NowMillis
	if project.Control.Intent.Kind == domainexecution.ControlResumeProject {
		next.State = "active"
		next.Control.Phase = domainexecution.ControlComplete
		next.Control.ResumeRequired = false
		next.Control.EmergencyLatched = false
	} else if project.Control.Intent.Kind == domainexecution.ControlPauseProject {
		next.Control.Phase = domainexecution.ControlPaused
	} else {
		next.Control.Phase = domainexecution.ControlComplete
	}
	controlCommand := ControlCommand{
		RequestID: stableID("control-reconciled", project.Control.Intent.ID), ProjectID: project.ID,
	}
	controlCommand.ConfirmationID = project.Control.Intent.ConfirmationID
	return controller.persistProjectControl(ctx, project, next, controlCommand, "project.control.reconciled")
}
