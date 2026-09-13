// SPDX-License-Identifier: Apache-2.0

// Package workflow composes approved Organizer, provider, planning, lease,
// runtime, and execution authorities into the production Run launcher.
package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/mcuadros/director-engine/application/configuration"
	executionapp "github.com/mcuadros/director-engine/application/execution"
	planningapp "github.com/mcuadros/director-engine/application/planning"
	"github.com/mcuadros/director-engine/application/projects"
	applicationscheduling "github.com/mcuadros/director-engine/application/scheduling"
	"github.com/mcuadros/director-engine/domain"
	domainconfig "github.com/mcuadros/director-engine/domain/configuration"
	domaincorrection "github.com/mcuadros/director-engine/domain/correction"
	directdomain "github.com/mcuadros/director-engine/domain/directdelivery"
	domainexecution "github.com/mcuadros/director-engine/domain/execution"
	"github.com/mcuadros/director-engine/domain/runtimebudget"
	organizerport "github.com/mcuadros/director-engine/ports/organizer"
	processport "github.com/mcuadros/director-engine/ports/process"
	providerport "github.com/mcuadros/director-engine/ports/provider"
	runtimeport "github.com/mcuadros/director-engine/ports/runtime"
	storeport "github.com/mcuadros/director-engine/ports/taskstore"
	"github.com/mcuadros/director-engine/reducer/eligibility"
)

type Store interface {
	storeport.TaskStore
	DependencyOverrides(context.Context, string) ([]domain.DependencyOverride, error)
}

type IsolationObserver interface {
	ObserveIsolation(context.Context) domainexecution.IsolationObservation
}

type launchRefusalError struct{ code string }

func (failure launchRefusalError) Error() string       { return "Task launch was refused" }
func (failure launchRefusalError) RefusalCode() string { return failure.code }

func launchRefused(code string) error { return launchRefusalError{code: code} }

type Launcher struct {
	store           Store
	controller      *executionapp.Controller
	repository      organizerport.Repository
	discovery       providerport.Discovery
	runtime         runtimeport.Port
	isolation       IsolationObserver
	lease           *projects.LeaseService
	holderInstance  string
	processIdentity string
	runtimeRoot     string
	engineURL       string
	executable      string
	schedulerMu     sync.Mutex
	schedulingStore *productionSchedulingStore
	scheduler       *applicationscheduling.Service
	queue           *WakeQueue
}

func NewLauncher(store Store, controller *executionapp.Controller, repository organizerport.Repository,
	discovery providerport.Discovery, runtimePort runtimeport.Port, isolation IsolationObserver,
	takeover processport.ProjectLeaseTakeoverObserver,
	queue *WakeQueue, holderInstance, processIdentity, runtimeRoot, engineURL, executable string,
) (*Launcher, error) {
	if store == nil || controller == nil || repository == nil || discovery == nil || runtimePort == nil || isolation == nil ||
		takeover == nil || queue == nil || holderInstance == "" || processIdentity == "" || !filepath.IsAbs(runtimeRoot) || !filepath.IsAbs(executable) || engineURL == "" {
		return nil, errors.New("production launcher composition is incomplete")
	}
	launcher := &Launcher{store: store, controller: controller, repository: repository, discovery: discovery,
		runtime: runtimePort, isolation: isolation, lease: projects.NewLeaseService(store, takeover), holderInstance: holderInstance,
		processIdentity: processIdentity, runtimeRoot: runtimeRoot, engineURL: engineURL, executable: executable, queue: queue}
	launcher.schedulingStore = &productionSchedulingStore{launcher: launcher}
	launcher.scheduler = applicationscheduling.NewService(launcher.schedulingStore)
	return launcher, nil
}

func stableID(prefix string, values ...string) string {
	digest := sha256.Sum256([]byte(strings.Join(values, "\x1f")))
	return prefix + "-" + hex.EncodeToString(digest[:16])
}

func (launcher *Launcher) ensureLease(ctx context.Context, project domain.Project) (domain.Project, error) {
	if project.Lease == nil {
		result, err := launcher.lease.ApplyLease(ctx, projects.LeaseCommand{Kind: projects.LeaseAcquire,
			RequestID: stableID("workflow-lease-acquire", project.ID, launcher.holderInstance), ProjectID: project.ID,
			ExpectedProjectVersion: project.Version, ExpectedLeaseEpoch: project.LastLeaseEpoch,
			HolderInstance: launcher.holderInstance, HolderProcessIdentity: launcher.processIdentity,
			DurationMillis: domain.MaximumProjectLeaseDurationMillis})
		return result.Project, err
	}
	now := time.Now().UnixMilli()
	if project.Lease.HolderInstance != launcher.holderInstance || project.Lease.HolderProcessIdentity != launcher.processIdentity {
		if now < project.Lease.ExpiresAtMillis {
			return domain.Project{}, errors.New("Project lease remains held by another engine")
		}
		takeover, err := launcher.lease.ApplyLease(ctx, projects.LeaseCommand{Kind: projects.LeaseTakeover,
			RequestID: stableID("workflow-lease-takeover", project.ID, fmt.Sprintf("epoch-%d", project.Lease.Epoch), launcher.holderInstance),
			ProjectID: project.ID, ExpectedProjectVersion: project.Version, ExpectedLeaseEpoch: project.Lease.Epoch,
			HolderInstance: launcher.holderInstance, HolderProcessIdentity: launcher.processIdentity,
			DurationMillis: domain.MaximumProjectLeaseDurationMillis})
		if err != nil || takeover.Project.Lease == nil || takeover.Project.Lease.DispatchAllowed {
			return domain.Project{}, errors.New("Project lease takeover was not fenced")
		}
		observed, err := launcher.lease.ObserveTakeover(ctx, projects.ObserveTakeoverCommand{
			RequestID: stableID("workflow-lease-takeover-observe", project.ID, fmt.Sprintf("epoch-%d", takeover.Project.Lease.Epoch)),
			ProjectID: project.ID, ExpectedProjectVersion: takeover.Project.Version})
		if err != nil || observed.Project.LeaseObservation == nil {
			return domain.Project{}, errors.New("Project lease takeover reconciliation is incomplete")
		}
		enabled, err := launcher.lease.EnableDispatch(ctx, projects.EnableDispatchCommand{
			RequestID: stableID("workflow-lease-takeover-enable", project.ID, observed.Project.LeaseObservation.ID),
			ProjectID: project.ID, ExpectedProjectVersion: observed.Project.Version,
			ObservationID: observed.Project.LeaseObservation.ID})
		if err != nil || enabled.Project.Lease == nil || !enabled.Project.Lease.DispatchAllowed {
			return domain.Project{}, errors.New("Project lease takeover dispatch remains disabled")
		}
		return enabled.Project, nil
	}
	if now >= project.Lease.ExpiresAtMillis {
		return domain.Project{}, errors.New("current Project lease expired while its engine process remains present")
	}
	if project.Lease.ExpiresAtMillis-now > domain.MaximumProjectLeaseDurationMillis/2 {
		return project, nil
	}
	result, err := launcher.lease.ApplyLease(ctx, projects.LeaseCommand{Kind: projects.LeaseRenew,
		RequestID: stableID("workflow-lease-renew", project.ID, fmt.Sprintf("epoch-%d", project.Lease.Epoch), fmt.Sprintf("version-%d", project.Version)),
		ProjectID: project.ID, ExpectedProjectVersion: project.Version, ExpectedLeaseEpoch: project.Lease.Epoch,
		HolderInstance: launcher.holderInstance, HolderProcessIdentity: launcher.processIdentity,
		DurationMillis: domain.MaximumProjectLeaseDurationMillis})
	return result.Project, err
}

func activeConfiguration(project domain.Project, snapshot organizerport.Snapshot) (configuration.RunConfigurationSnapshot, error) {
	if project.Organizer == nil || project.Organizer.Phase != domain.OrganizerPhaseActive ||
		project.Organizer.OrganizerRevision != snapshot.Revision {
		return configuration.RunConfigurationSnapshot{}, errors.New("active Organizer revision changed")
	}
	document, err := domainconfig.Parse(snapshot.ConfigurationJSON)
	if err != nil || document.SHA256() != project.Organizer.ConfigurationSHA256 {
		return configuration.RunConfigurationSnapshot{}, errors.New("active Organizer configuration changed")
	}
	human := configuration.HumanConfirmation{ActorKind: configuration.ActorHuman, ActorID: project.Organizer.HumanActorID,
		Revision: document.SHA256(), Confirmed: true}
	envelope, err := configuration.NewSecurityEnvelope(document, human)
	if err != nil {
		return configuration.RunConfigurationSnapshot{}, err
	}
	state, err := configuration.NewState(envelope)
	if err != nil {
		return configuration.RunConfigurationSnapshot{}, err
	}
	state, preview, err := state.Preview(configuration.PreviewCommand{ExpectedVersion: 0,
		OrganizerRevision: snapshot.Revision, ConfigurationJSON: snapshot.ConfigurationJSON})
	if err != nil || !preview.Valid {
		return configuration.RunConfigurationSnapshot{}, errors.New("active Organizer configuration cannot be frozen")
	}
	state, err = state.Apply(configuration.ApplyCommand{ExpectedVersion: state.Version(), PreviewID: preview.ID,
		Confirmation: configuration.HumanConfirmation{ActorKind: configuration.ActorHuman, ActorID: project.Organizer.HumanActorID,
			Revision: snapshot.Revision, Confirmed: true},
		Acknowledgement: configuration.HumanConfirmation{ActorKind: configuration.ActorHuman, ActorID: project.Organizer.HumanActorID,
			Revision: "", Confirmed: true}})
	if err != nil {
		return configuration.RunConfigurationSnapshot{}, err
	}
	return state.FreezeRunConfiguration()
}

func lifecycleSurfaces(sourcePath string) (domainexecution.LifecycleSurfaces, error) {
	content, err := os.ReadFile(filepath.Join(sourcePath, "paseo.json"))
	if errors.Is(err, os.ErrNotExist) {
		return domainexecution.LifecycleSurfaces{}, nil
	}
	if err != nil || len(content) > 64*1024 {
		return domainexecution.LifecycleSurfaces{}, errors.New("Paseo lifecycle configuration is unavailable")
	}
	var value struct {
		Worktree struct {
			Setup     []string `json:"setup"`
			Teardown  []string `json:"teardown"`
			Terminals []struct {
				Command string `json:"command"`
			} `json:"terminals"`
			ServicePorts []struct {
				PortScript string `json:"portScript"`
			} `json:"servicePorts"`
		} `json:"worktree"`
	}
	decoder := json.NewDecoder(bytes.NewReader(content))
	if decoder.Decode(&value) != nil {
		return domainexecution.LifecycleSurfaces{}, errors.New("Paseo lifecycle configuration is invalid")
	}
	result := domainexecution.LifecycleSurfaces{Setup: value.Worktree.Setup, Teardown: value.Worktree.Teardown}
	for _, terminal := range value.Worktree.Terminals {
		result.TerminalCommands = append(result.TerminalCommands, terminal.Command)
	}
	for _, service := range value.Worktree.ServicePorts {
		result.ServicePortScript = append(result.ServicePortScript, service.PortScript)
	}
	return result, nil
}

func gitValue(ctx context.Context, cwd string, arguments ...string) (string, error) {
	command := exec.CommandContext(ctx, "git", append([]string{"-c", "core.hooksPath=/dev/null", "-c", "core.fsmonitor=false", "-C", cwd}, arguments...)...)
	command.Env = []string{"LC_ALL=C", "LANG=C", "PATH=" + os.Getenv("PATH"), "GIT_CONFIG_NOSYSTEM=1", "GIT_OPTIONAL_LOCKS=0"}
	output, err := command.Output()
	if err != nil || len(output) > 4096 {
		return "", errors.New("Git launch fact is unavailable")
	}
	return strings.TrimSpace(string(output)), nil
}

func criteriaIDs(task domain.Task) []string {
	lines := strings.Split(task.AcceptanceCriteria, "\n")
	result := make([]string, 0, len(lines))
	for index, line := range lines {
		if strings.TrimSpace(line) != "" {
			result = append(result, fmt.Sprintf("criterion-%d", index+1))
		}
	}
	return result
}

func primaryTaskPrompt(task domain.Task) string {
	const maximumInlineTaskBytes = 12 * 1024
	context := fmt.Sprintf("Director Task %s\nTitle: %s\nObjective: %s\nAcceptance criteria:\n%s\n",
		task.ID, task.Title, task.Objective, task.AcceptanceCriteria)
	if len(context) > maximumInlineTaskBytes {
		context = fmt.Sprintf("Director Task %s\nTitle: %s\nObjective: %s\nThe acceptance criteria exceed the inline bound; read the complete exact Task with director_task_read before editing.\n",
			task.ID, task.Title, task.Objective)
	}
	return context + "Do only this Task. Use director_task_read to confirm the current binding. Commit one clean Candidate descending from the exact base, then call director_task_outcome_submit exactly once with the exact Candidate/base and one result per criterion. Do not push, publish, merge, or perform Director lifecycle effects."
}

func boolFact(value bool) eligibility.BooleanFact {
	return eligibility.BooleanFact{Observed: true, Value: value}
}

func dependenciesSatisfied(ctx context.Context, store Store, task domain.Task) (bool, error) {
	workspaces, err := store.Workspaces(ctx, task.ProjectID)
	if err != nil {
		return false, err
	}
	epics, err := store.Epics(ctx, task.ProjectID)
	if err != nil {
		return false, err
	}
	tasks, err := store.Tasks(ctx, task.ProjectID)
	if err != nil {
		return false, err
	}
	overrides, err := store.DependencyOverrides(ctx, task.ProjectID)
	if err != nil {
		return false, err
	}
	workspaceIDs := make([]string, len(workspaces))
	for index := range workspaces {
		workspaceIDs[index] = workspaces[index].ID
	}
	report := domain.EvaluatePlanning(domain.PlanningProject{ID: task.ProjectID, Workspaces: workspaceIDs,
		Epics: epics, Tasks: tasks, Overrides: overrides})
	result, ok := report.Result(task.ID)
	return report.Valid && ok && !result.Blocked, nil
}

func (launcher *Launcher) launchTaskEffect(ctx context.Context, request planningapp.LaunchRequest) (domain.Run, error) {
	task, err := launcher.store.Task(ctx, request.TaskID)
	if err != nil || task.Version != request.ExpectedTaskVersion || len(task.WorkspaceIDs) != 1 || domain.ValidateTask(task) != nil {
		return domain.Run{}, errors.New("Task launch binding changed")
	}
	project, err := launcher.store.Project(ctx, task.ProjectID)
	if err != nil {
		return domain.Run{}, err
	}
	project, err = launcher.ensureLease(ctx, project)
	if err != nil || project.Lease == nil || !project.Lease.DispatchAllowed {
		return domain.Run{}, errors.New("Project execution lease is unavailable")
	}
	if project.Organizer == nil {
		return domain.Run{}, errors.New("active Organizer is unavailable")
	}
	workspace, err := launcher.store.Workspace(ctx, task.WorkspaceIDs[0])
	if err != nil || workspace.NativePaseoWorkspaceID == "" {
		return domain.Run{}, errors.New("native Paseo root Workspace binding is unavailable")
	}
	organizerSnapshot, err := launcher.repository.Read(ctx, project.Organizer.RepositoryPath)
	if err != nil {
		return domain.Run{}, err
	}
	configurationSnapshot, err := activeConfiguration(project, organizerSnapshot)
	if err != nil {
		return domain.Run{}, err
	}
	effective, err := configurationSnapshot.Effective(workspace.Key, configuration.TaskOverride{})
	if err != nil {
		return domain.Run{}, err
	}
	discovery, err := launcher.discovery.Discover(ctx)
	if err != nil {
		return domain.Run{}, err
	}
	profiles, err := configuration.NewProfileService(launcher.discovery)
	if err != nil {
		return domain.Run{}, err
	}
	now := time.Now().UnixMilli()
	frozen, err := profiles.FreezeObservedProfiles(configurationSnapshot, organizerSnapshot.Revision, discovery, discovery.Revision, now)
	if err != nil {
		return domain.Run{}, err
	}
	runs, err := launcher.store.Runs(ctx, task.ID)
	if err != nil {
		return domain.Run{}, err
	}
	for _, run := range runs {
		if !run.Execution.Terminal {
			if run.Execution.StartCommandID == request.RequestID {
				return run, nil
			}
			return domain.Run{}, errors.New("Task already has an active Run")
		}
	}
	runNumber := uint64(len(runs) + 1)
	runID := stableID("run", task.ID, fmt.Sprintf("number-%d", runNumber), request.RequestID)
	scope := domainexecution.Scope{ProjectID: project.ID, WorkspaceID: workspace.ID, TaskID: task.ID, RunID: runID}
	branch := "task/" + task.ID
	baseSHA, err := gitValue(ctx, workspace.Repository.SourcePath, "rev-parse", "refs/heads/"+workspace.DefaultBaseBranch+"^{commit}")
	if err != nil {
		return domain.Run{}, err
	}
	worktreeParent := filepath.Join(launcher.runtimeRoot, "worktrees", project.ID, task.ID)
	if err := os.MkdirAll(worktreeParent, 0o700); err != nil {
		return domain.Run{}, errors.New("owned worktree parent is unavailable")
	}
	worktreePath := filepath.Join(worktreeParent, runID)
	surfaces, err := lifecycleSurfaces(workspace.Repository.SourcePath)
	if err != nil {
		return domain.Run{}, err
	}
	isolation := launcher.isolation.ObserveIsolation(ctx)
	operationalPolicy := domainexecution.OperationalPolicy{MinimumFreeDiskBasisPoints: 1_000,
		MaximumWorktreeBytes: 20 << 30, MaximumProcesses: 256, MaximumMemoryBytes: 16 << 30,
		MaximumElapsedMilliseconds: uint64(effective.RunBudget.ElapsedSeconds) * 1_000,
		MaximumOutputBytes:         16 << 20, MaximumTemporaryBytes: 2 << 30, MaximumObservationAgeMillis: 30_000}
	operational, err := launcher.runtime.ObserveOperational(ctx, scope, operationalPolicy)
	if err != nil {
		return domain.Run{}, err
	}
	// Admission time must be sampled after every external observation. Using
	// the earlier provider-discovery instant would make a fresh operational
	// fact appear to come from the future and fail closed as missing.
	now = time.Now().UnixMilli()
	dependencies, err := dependenciesSatisfied(ctx, launcher.store, task)
	if err != nil {
		return domain.Run{}, err
	}
	if err := launcher.repository.VerifyWorkspace(ctx, workspace.Repository.SourcePath, workspace.Repository.CanonicalRemote); err != nil {
		return domain.Run{}, err
	}
	budgetPolicy := runtimebudget.NewPolicy(frozen.ConfigurationSHA256(), uint64(effective.RunBudget.ElapsedSeconds)*1_000,
		uint64(effective.RunBudget.Tokens), uint64(effective.RunBudget.Turns), uint64(effective.RunBudget.CostMicrousd), uint32(effective.RunBudget.CICycles))
	cleanupPolicy, ok := effective.CleanupPolicy(frozen.ConfigurationSHA256())
	if !ok {
		return domain.Run{}, errors.New("cleanup policy is invalid")
	}
	validationPolicy, _ := effective.ValidationPolicy()
	integrationPolicy, _ := effective.IntegrationPolicy(frozen.ConfigurationSHA256())
	var directPolicy directdomain.Policy
	if effective.DeliveryMode == domainconfig.DeliveryDirect {
		mode := directdomain.IntegrationManual
		if effective.IntegrationMode == domainconfig.IntegrationAutomatic {
			mode = directdomain.IntegrationAutomatic
		}
		automatic := []string{}
		if mode == directdomain.IntegrationAutomatic {
			automatic = []string{"refs/heads/" + workspace.DefaultBaseBranch}
		}
		directPolicy = directdomain.SealPolicy(directdomain.Policy{DeliveryMode: "direct", IntegrationMode: mode,
			SelectionSource: directdomain.SelectionFrozenRunConfiguration, ConfigurationSHA256: frozen.ConfigurationSHA256(),
			AuthorizedTargetRefs: []string{"refs/heads/" + workspace.DefaultBaseBranch}, AutomaticTargetRefs: automatic,
			AttemptLimit: directdomain.MaximumAttempts})
	}
	turnDemand := runtimebudget.Demand{WallTimeMilliseconds: 60_000, Tokens: 1_000, Turns: 1}
	if effective.RunBudget.CostMicrousd > 0 {
		turnDemand.CostMicrousd = 1
	}
	result, err := launcher.controller.Start(ctx, executionapp.StartCommand{RequestID: request.RequestID, Scope: scope,
		RunNumber: runNumber, SourcePath: workspace.Repository.SourcePath, WorktreePath: worktreePath,
		Branch: branch, BaseSHA: baseSHA, TaskTitle: task.Title, CriterionIDs: criteriaIDs(task),
		InitialPrompt:   primaryTaskPrompt(task),
		RootWorkspaceID: workspace.NativePaseoWorkspaceID,
		MCPServer: domainexecution.MCPServerLaunch{Name: "director-session-mcp", Command: launcher.executable,
			Args: []string{"agent-mcp", "--engine-url", launcher.engineURL, "--run", runID}, Env: map[string]string{}},
		EffectiveProfiles: frozen, ReviewPolicy: effective.ReviewPolicy(), PublicationPolicy: effective.PublicationPolicy(),
		DeliveryMode: effective.DeliveryMode, DirectDeliveryPolicy: directPolicy, ValidationPolicy: validationPolicy, IntegrationPolicy: integrationPolicy,
		CorrectionPolicy: domaincorrection.Policy{AutoFixCIFailures: effective.AutoFixCIFailures,
			AutoFixReviewFeedback: effective.AutoFixReviewFeedback, AttemptLimit: domaincorrection.AttemptLimit},
		CleanupPolicy: cleanupPolicy, BudgetPolicy: budgetPolicy,
		TurnBudgetDemand: turnDemand,
		HelperPolicy:     domainexecution.HelperPolicy{MaximumPerTask: uint32(effective.Limits.MaxSubagentsPerTask), MaximumConcurrentAgents: uint32(effective.Limits.MaxConcurrentAgents)},
		ControlPolicy:    domainexecution.DefaultControlPolicy(), RecoveryPolicy: domainexecution.DefaultPrimaryRecoveryPolicy(),
		EligibilityFacts: eligibility.Facts{SchemaVersion: eligibility.SchemaVersion, Scope: scope,
			ProjectLeaseCurrent: boolFact(true), ProjectActive: boolFact(project.State == "active"), OrganizerRevisionActive: boolFact(true),
			TaskComplete: boolFact(true), DependenciesSatisfied: boolFact(dependencies), NoActiveRun: boolFact(true),
			LaunchPolicyAllows: boolFact(!request.Automatic || effective.LaunchPolicy == domainconfig.LaunchAutomatic), CapacityAvailable: boolFact(true), BudgetsAvailable: boolFact(true),
			ProviderAdmitted: boolFact(true), RepositoryIdentityExact: boolFact(true), LifecycleSurfaces: surfaces,
			Isolation: isolation, OperationalPolicy: operationalPolicy, OperationalObservation: operational, TaskStoreNowMillis: now},
		Production: true})
	if err != nil {
		return domain.Run{}, launchRefused("LAUNCH_RUN_ADMISSION_INVALID")
	}
	if result.RunID == "" {
		return domain.Run{}, launchRefused("LAUNCH_RUN_ELIGIBILITY_CHANGED")
	}
	run, err := launcher.store.Run(ctx, result.RunID)
	if err == nil {
		launcher.queue.NotifyRun(run.ID)
	}
	return run, err
}

func (launcher *Launcher) LaunchTask(ctx context.Context, request planningapp.LaunchRequest) (domain.Run, error) {
	run, err := launcher.scheduleTask(ctx, request)
	if err == nil {
		return run, nil
	}
	if _, ok := err.(launchRefusalError); ok {
		return domain.Run{}, err
	}
	switch err.Error() {
	case "Task launch binding changed":
		return domain.Run{}, launchRefused("LAUNCH_TASK_BINDING_CHANGED")
	case "Project execution lease is unavailable":
		return domain.Run{}, launchRefused("LAUNCH_LEASE_UNAVAILABLE")
	case "Task launch was refused by current scheduler facts":
		return domain.Run{}, launchRefused("LAUNCH_SCHEDULER_FACTS_REFUSED")
	case "Task launch permit is unavailable":
		return domain.Run{}, launchRefused("LAUNCH_PERMIT_UNAVAILABLE")
	case "Task already has an active Run":
		return domain.Run{}, launchRefused("LAUNCH_ACTIVE_RUN_PRESENT")
	default:
		return domain.Run{}, launchRefused("LAUNCH_PRODUCTION_FACT_UNAVAILABLE")
	}
}

// Schedule applies the engine-owned scheduler reducer, durable Project-CAS
// reservations, and one-use lease-bound launch permits.
func (launcher *Launcher) Schedule(ctx context.Context) error { return launcher.runScheduler(ctx) }
