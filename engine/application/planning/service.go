// SPDX-License-Identifier: Apache-2.0

// Package planning owns production planning mutations. Transport handlers
// validate the generated contract and authenticate the actor; this service
// rereads durable facts, applies optimistic versions, and is the only layer
// which turns an advertised planning intent into a TaskStore mutation.
package planning

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/mcuadros/director-engine/domain"
	planningport "github.com/mcuadros/director-engine/ports/planning"
	storeport "github.com/mcuadros/director-engine/ports/taskstore"
)

var (
	ErrInvalidMutation = errors.New("planning mutation is invalid")
	ErrVersionConflict = errors.New("planning mutation version conflict")
)

type launchRefusalCoder interface {
	RefusalCode() string
}

// Store is the complete typed planning subset of TaskStore. It intentionally
// exposes no SQL, database selection, lifecycle effect, or adapter default.
type Store interface {
	Project(context.Context, string) (domain.Project, error)
	UpdateProject(context.Context, domain.CommandRequest, domain.Project, domain.Event) (domain.CommandResult, error)
	Workspace(context.Context, string) (domain.Workspace, error)
	Workspaces(context.Context, string) ([]domain.Workspace, error)
	Epic(context.Context, string) (domain.Epic, error)
	Epics(context.Context, string) ([]domain.Epic, error)
	CreateEpic(context.Context, domain.CommandRequest, domain.Epic, domain.Event) (domain.CommandResult, error)
	UpdateEpic(context.Context, domain.CommandRequest, domain.Epic, domain.Event) (domain.CommandResult, error)
	Task(context.Context, string) (domain.Task, error)
	Tasks(context.Context, string) ([]domain.Task, error)
	CreateTask(context.Context, domain.CommandRequest, domain.Task, domain.Event) (domain.CommandResult, error)
	UpdateTask(context.Context, domain.CommandRequest, domain.Task, domain.Event) (domain.CommandResult, error)
	GrantDependencyOverride(context.Context, domain.CommandRequest, domain.HumanDependencyOverrideGrant) (domain.CommandResult, error)
}

// LaunchRequest is the immutable planning-to-execution handoff. The launcher
// must reread all launch, dependency, lease, security, capacity, provider, Git,
// and budget facts before it creates a Run.
type LaunchRequest struct {
	RequestID           string
	TaskID              string
	ExpectedTaskVersion uint64
	ActorID             string
	ActorSessionID      string
	Automatic           bool
}

type Launcher interface {
	LaunchTask(context.Context, LaunchRequest) (domain.Run, error)
}

// ConfigurationService owns the Project/Workspace/Task Preview/Apply path.
// Implementations must preserve the exact server-authenticated confirmation
// and active Organizer revision gates.
type ConfigurationService interface {
	MutateConfiguration(context.Context, planningport.MutationInput, Actor) Result
}

type Actor struct {
	ID        string
	SessionID string
}

type Result struct {
	Status          string
	Message         string
	UpdatedVersion  *uint64
	Preview         any
	ConfirmationRef string
}

type Service struct {
	store         Store
	launcher      Launcher
	configuration ConfigurationService
}

func NewService(store Store, launcher Launcher, configuration ConfigurationService) (*Service, error) {
	if store == nil {
		return nil, errors.New("planning TaskStore is required")
	}
	return &Service{store: store, launcher: launcher, configuration: configuration}, nil
}

func stableID(prefix string, values ...string) string {
	digest := sha256.Sum256([]byte(strings.Join(values, "\x1f")))
	return prefix + "-" + hex.EncodeToString(digest[:16])
}

func payload(value any) json.RawMessage {
	encoded, err := json.Marshal(value)
	if err != nil {
		panic("marshal fixed planning mutation payload: " + err.Error())
	}
	return encoded
}

func applied(version uint64, message string) Result {
	return Result{Status: "accepted", Message: message, UpdatedVersion: &version}
}

func refused(message string) Result { return Result{Status: "rejected", Message: message} }

func command(input planningport.MutationInput, aggregateID, kind string, expected uint64) domain.CommandRequest {
	return domain.CommandRequest{IdempotencyKey: input.IdempotencyKey, Type: kind, AggregateID: aggregateID,
		ExpectedVersion: expected, Payload: payload(struct {
			SchemaVersion int                         `json:"schemaVersion"`
			RequestID     string                      `json:"requestId"`
			Intent        planningport.MutationIntent `json:"intent"`
		}{SchemaVersion: 1, RequestID: input.RequestID, Intent: input.Intent})}
}

func event(input planningport.MutationInput, aggregateID, runID, kind string, version uint64) domain.Event {
	return domain.Event{ID: stableID("event", input.IdempotencyKey), RunID: runID, Sequence: version + 1,
		AggregateID: aggregateID, AggregateVersion: version, Type: kind,
		Payload: payload(struct {
			SchemaVersion int    `json:"schemaVersion"`
			RequestID     string `json:"requestId"`
		}{SchemaVersion: 1, RequestID: input.RequestID})}
}

func requireApplied(result domain.CommandResult, expected uint64) error {
	if result.Outcome == domain.CommandRejectedVersionConflict {
		return ErrVersionConflict
	}
	if result.Outcome != domain.CommandApplied || result.ObservedVersion != expected {
		return ErrInvalidMutation
	}
	return nil
}

func expectedVersion(input planningport.MutationInput) uint64 {
	value, _ := planningport.ParseExpectedVersion(input.ExpectedVersion)
	return value
}

// Mutate routes every non-control intent in the generated vocabulary. The
// caller retains control intents in the execution controller because those
// reducers own Pause/Resume/Cancel/Emergency-stop semantics.
func (service *Service) Mutate(ctx context.Context, input planningport.MutationInput, actor Actor) (Result, error) {
	if service == nil || service.store == nil || actor.ID == "" || actor.SessionID == "" || planningport.ValidateMutation(input) != nil {
		return Result{}, ErrInvalidMutation
	}
	intent, expected := input.Intent, expectedVersion(input)
	switch intent.Type {
	case "project.create":
		return refused("Select an existing native Paseo Project and confirm its generated Organizer Preview"), nil
	case "project.update":
		project, err := service.store.Project(ctx, intent.ProjectID)
		if err != nil {
			return Result{}, err
		}
		if project.Version != expected {
			return refused("Project changed; refresh its current authoritative version"), nil
		}
		project.Name, project.Version = intent.Name, project.Version+1
		result, err := service.store.UpdateProject(ctx, command(input, project.ID, "project.update", expected), project,
			event(input, project.ID, "", "project.updated", project.Version))
		if err != nil {
			return Result{}, err
		}
		if err := requireApplied(result, project.Version); err != nil {
			return refused("Project changed; refresh its current authoritative version"), nil
		}
		return applied(project.Version, "Project update was durably recorded"), nil
	case "epic.create":
		project, err := service.store.Project(ctx, intent.ProjectID)
		if err != nil {
			return Result{}, err
		}
		if project.Version != expected {
			return refused("Project changed; refresh before creating the Epic"), nil
		}
		priority := domain.Priority("")
		if intent.Priority != nil {
			priority = domain.Priority(*intent.Priority)
		}
		epic := domain.Epic{ID: stableID("epic", project.ID, intent.Key), ProjectID: project.ID, Key: intent.Key,
			Title: intent.Title, Description: intent.Description, Priority: priority, Labels: slices.Clone(intent.Labels)}
		result, err := service.store.CreateEpic(ctx, command(input, epic.ID, "epic.create", 0), epic,
			event(input, epic.ID, "", "epic.created", 0))
		if err != nil {
			if errors.Is(err, storeport.ErrAlreadyExists) || errors.Is(err, storeport.ErrInvalidRecord) {
				return refused("Epic creation conflicts with current Project planning facts"), nil
			}
			return Result{}, err
		}
		if err := requireApplied(result, 0); err != nil {
			return refused("Epic creation conflicts with current Project planning facts"), nil
		}
		return applied(0, "Epic was durably created"), nil
	case "epic.update":
		epic, err := service.store.Epic(ctx, intent.EpicID)
		if err != nil {
			return Result{}, err
		}
		if epic.Version != expected {
			return refused("Epic changed; refresh its current authoritative version"), nil
		}
		epic.Title, epic.Description, epic.Labels = intent.Title, intent.Description, slices.Clone(intent.Labels)
		epic.Priority = ""
		if intent.Priority != nil {
			epic.Priority = domain.Priority(*intent.Priority)
		}
		epic.Version++
		result, err := service.store.UpdateEpic(ctx, command(input, epic.ID, "epic.update", expected), epic,
			event(input, epic.ID, "", "epic.updated", epic.Version))
		if err != nil {
			if errors.Is(err, storeport.ErrInvalidRecord) {
				return refused("Epic update violates current hierarchy or dependency facts"), nil
			}
			return Result{}, err
		}
		if err := requireApplied(result, epic.Version); err != nil {
			return refused("Epic changed; refresh its current authoritative version"), nil
		}
		return applied(epic.Version, "Epic update was durably recorded"), nil
	case "task.create":
		project, err := service.store.Project(ctx, intent.ProjectID)
		if err != nil {
			return Result{}, err
		}
		workspace, err := service.store.Workspace(ctx, intent.WorkspaceID)
		if err != nil {
			return Result{}, err
		}
		if project.Version != expected || workspace.ProjectID != project.ID {
			return refused("Task target changed or is outside the selected Project"), nil
		}
		var parent *domain.PlanningNodeRef
		if intent.EpicID != "" {
			epic, epicErr := service.store.Epic(ctx, intent.EpicID)
			if epicErr != nil || epic.ProjectID != project.ID {
				return refused("Task Epic is unavailable or belongs to another Project"), nil
			}
			value := domain.PlanningNodeRef{Kind: domain.PlanningNodeEpic, ID: intent.EpicID}
			parent = &value
		}
		task := domain.Task{ID: stableID("task", project.ID, intent.Key), ProjectID: project.ID, Key: intent.Key,
			Title: intent.Title, Objective: intent.Objective, AcceptanceCriteria: strings.Join(intent.AcceptanceCriteria, "\n"),
			WorkspaceIDs: []string{workspace.ID}, Parent: parent, Priority: domain.Priority(*intent.Priority),
			Labels: slices.Clone(intent.Labels), QueuedAtUnixMillis: time.Now().UnixMilli()}
		result, err := service.store.CreateTask(ctx, command(input, task.ID, "task.create", 0), task,
			event(input, task.ID, "", "task.created", 0))
		if err != nil {
			if errors.Is(err, storeport.ErrAlreadyExists) || errors.Is(err, storeport.ErrInvalidRecord) {
				return refused("Task creation conflicts with current Project planning facts"), nil
			}
			return Result{}, err
		}
		if err := requireApplied(result, 0); err != nil {
			return refused("Task creation conflicts with current Project planning facts"), nil
		}
		return applied(0, "Task was durably created"), nil
	case "task.update":
		task, err := service.store.Task(ctx, intent.TaskID)
		if err != nil {
			return Result{}, err
		}
		if task.Version != expected {
			return refused("Task changed; refresh its current authoritative version"), nil
		}
		var parent *domain.PlanningNodeRef
		if intent.EpicID != "" {
			epic, epicErr := service.store.Epic(ctx, intent.EpicID)
			if epicErr != nil || epic.ProjectID != task.ProjectID {
				return refused("Task Epic is unavailable or belongs to another Project"), nil
			}
			value := domain.PlanningNodeRef{Kind: domain.PlanningNodeEpic, ID: intent.EpicID}
			parent = &value
		}
		task.Parent, task.Title, task.Objective = parent, intent.Title, intent.Objective
		task.AcceptanceCriteria, task.Priority, task.Labels = strings.Join(intent.AcceptanceCriteria, "\n"), domain.Priority(*intent.Priority), slices.Clone(intent.Labels)
		task.Version++
		result, err := service.store.UpdateTask(ctx, command(input, task.ID, "task.update", expected), task,
			event(input, task.ID, "", "task.updated", task.Version))
		if err != nil {
			if errors.Is(err, storeport.ErrInvalidRecord) {
				return refused("Task update violates current hierarchy or dependency facts"), nil
			}
			return Result{}, err
		}
		if err := requireApplied(result, task.Version); err != nil {
			return refused("Task changed; refresh its current authoritative version"), nil
		}
		return applied(task.Version, "Task update was durably recorded"), nil
	case "dependency.add", "dependency.remove":
		task, err := service.store.Task(ctx, intent.TaskID)
		if err != nil {
			return Result{}, err
		}
		if task.Version != expected {
			return refused("Task changed; refresh before changing dependencies"), nil
		}
		kind := domain.PlanningNodeTask
		if intent.DependencyKind == "epic" {
			kind = domain.PlanningNodeEpic
		}
		edge := domain.PlanningDependency{From: domain.PlanningNodeRef{Kind: domain.PlanningNodeTask, ID: task.ID},
			On: domain.PlanningNodeRef{Kind: kind, ID: intent.DependencyID}}
		index := slices.Index(task.Dependencies, edge)
		if intent.Type == "dependency.add" {
			if index >= 0 {
				return refused("Dependency already exists on the current Task version"), nil
			}
			task.Dependencies = append(slices.Clone(task.Dependencies), edge)
		} else {
			if index < 0 {
				return refused("Dependency is absent from the current Task version"), nil
			}
			task.Dependencies = slices.Delete(slices.Clone(task.Dependencies), index, index+1)
		}
		task.Version++
		result, err := service.store.UpdateTask(ctx, command(input, task.ID, intent.Type, expected), task,
			event(input, task.ID, "", strings.ReplaceAll(intent.Type, ".", "_"), task.Version))
		if err != nil {
			if errors.Is(err, storeport.ErrInvalidRecord) || errors.Is(err, storeport.ErrReferentialIntegrity) {
				return refused("Dependency change violates current graph facts"), nil
			}
			return Result{}, err
		}
		if err := requireApplied(result, task.Version); err != nil {
			return refused("Task changed; refresh before changing dependencies"), nil
		}
		return applied(task.Version, "Task dependency change was durably recorded"), nil
	case "dependency.override":
		task, err := service.store.Task(ctx, intent.TaskID)
		if err != nil {
			return Result{}, err
		}
		if task.Version != expected || input.AcknowledgementRevision == nil || *input.AcknowledgementRevision != input.ExpectedVersion {
			return refused("Dependency override acknowledgement is stale"), nil
		}
		kind := domain.PlanningNodeTask
		if intent.DependencyKind == "epic" {
			kind = domain.PlanningNodeEpic
		}
		edge := domain.PlanningDependency{From: domain.PlanningNodeRef{Kind: domain.PlanningNodeTask, ID: task.ID},
			On: domain.PlanningNodeRef{Kind: kind, ID: intent.DependencyID}}
		grant := domain.HumanDependencyOverrideGrant{ID: stableID("override", input.RequestID), TaskID: task.ID,
			ExpectedTaskVersion: task.Version, Dependency: edge, HumanActorID: actor.ID, AuditID: stableID("audit", input.RequestID)}
		grantCommand := domain.CommandRequest{IdempotencyKey: input.IdempotencyKey, Type: "task.dependency_override.grant",
			AggregateID: grant.ID, ExpectedVersion: 0, Payload: payload(grant)}
		result, err := service.store.GrantDependencyOverride(ctx, grantCommand, grant)
		if err != nil {
			if errors.Is(err, storeport.ErrInvalidRecord) || errors.Is(err, storeport.ErrReferentialIntegrity) {
				return refused("Dependency override is not permitted by current authoritative facts"), nil
			}
			return Result{}, err
		}
		if err := requireApplied(result, 0); err != nil {
			return refused("Dependency override conflicts with current authoritative facts"), nil
		}
		return applied(task.Version, "Authenticated human dependency override was durably recorded"), nil
	case "task.launch-now":
		if service.launcher == nil {
			return refused("Launch is unavailable until current host, lease, provider, isolation, repository, and capacity facts are ready"), nil
		}
		run, err := service.launcher.LaunchTask(ctx, LaunchRequest{RequestID: input.RequestID, TaskID: intent.TaskID,
			ExpectedTaskVersion: expected, ActorID: actor.ID, ActorSessionID: actor.SessionID})
		if err != nil {
			code := "LAUNCH_CURRENT_FACTS_REFUSED"
			if refusal, ok := err.(launchRefusalCoder); ok {
				candidate := refusal.RefusalCode()
				if len(candidate) >= 8 && len(candidate) <= 96 && strings.HasPrefix(candidate, "LAUNCH_") {
					code = candidate
				}
			}
			return refused("Launch was refused by current authoritative facts (" + code + ")"), nil
		}
		return applied(run.Version, "Launch request was durably admitted for a real Run"), nil
	case "configuration.preview", "configuration.apply":
		if service.configuration == nil {
			return refused("The active Organizer policy does not permit inline overrides for this scope; use an authenticated Organizer revision Preview/Apply"), nil
		}
		return service.configuration.MutateConfiguration(ctx, input, actor), nil
	default:
		return Result{}, fmt.Errorf("%w: %s", ErrInvalidMutation, intent.Type)
	}
}
