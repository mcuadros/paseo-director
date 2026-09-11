// SPDX-License-Identifier: Apache-2.0

// Package cleanup implements engine-owned local recovery and remote cleanup.
// It consumes exact integration evidence, advances one deterministic effect at
// a time, and leaves host/Git/GitHub connectors policy-free.
package cleanup

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/mcuadros/director-engine/domain"
	"github.com/mcuadros/director-engine/domain/candidate"
	domaincleanup "github.com/mcuadros/director-engine/domain/cleanup"
	directdomain "github.com/mcuadros/director-engine/domain/directdelivery"
	"github.com/mcuadros/director-engine/domain/execution"
	domainintegration "github.com/mcuadros/director-engine/domain/integration"
	domainreview "github.com/mcuadros/director-engine/domain/review"
	cleanuport "github.com/mcuadros/director-engine/ports/cleanup"
	gitport "github.com/mcuadros/director-engine/ports/git"
	githubport "github.com/mcuadros/director-engine/ports/github"
	"github.com/mcuadros/director-engine/ports/host"
	cleanupreducer "github.com/mcuadros/director-engine/reducer/cleanup"
)

var (
	ErrInvalidCommand       = errors.New("cleanup command is invalid")
	ErrConcurrentTransition = errors.New("cleanup transition lost an expected-version race")
	ErrNotReady             = errors.New("cleanup is not ready")
	ErrExternalUnavailable  = errors.New("cleanup external state is unavailable")
	ErrExternalAmbiguous    = errors.New("cleanup external state is ambiguous")
)

type EffectHandoffError struct {
	Kind domaincleanup.ResourceKind
	err  error
}

func (failure *EffectHandoffError) Error() string { return "cleanup effect requires reconciliation" }
func (failure *EffectHandoffError) Unwrap() error { return failure.err }

type Store interface {
	Project(context.Context, string) (domain.Project, error)
	Task(context.Context, string) (domain.Task, error)
	Run(context.Context, string) (domain.Run, error)
	Candidate(context.Context, string) (domain.Candidate, error)
	UpdateRun(context.Context, domain.CommandRequest, domain.Run, domain.Event) (domain.CommandResult, error)
}

type Service struct {
	store          Store
	host           host.Port
	candidates     gitport.CandidateObserver
	integrationGit gitport.IntegrationPort
	forge          githubport.IntegrationPort
	git            cleanuport.GitPort
	artifactRoot   string
	gate           sync.Mutex
	locks          map[string]*keyedLock
}

type keyedLock struct {
	mutex sync.Mutex
	users uint32
}

func NewService(store Store, hostPort host.Port, candidates gitport.CandidateObserver,
	integrationGit gitport.IntegrationPort, forge githubport.IntegrationPort, cleanupGit cleanuport.GitPort,
	artifactRoot string,
) (*Service, error) {
	if store == nil || hostPort == nil || candidates == nil || integrationGit == nil || forge == nil || cleanupGit == nil ||
		!filepath.IsAbs(artifactRoot) || filepath.Clean(artifactRoot) != artifactRoot {
		return nil, ErrInvalidCommand
	}
	return &Service{store: store, host: hostPort, candidates: candidates, integrationGit: integrationGit,
		forge: forge, git: cleanupGit, artifactRoot: artifactRoot, locks: map[string]*keyedLock{}}, nil
}

func (service *Service) lock(key string) func() {
	service.gate.Lock()
	entry := service.locks[key]
	if entry == nil {
		entry = &keyedLock{}
		service.locks[key] = entry
	}
	entry.users++
	service.gate.Unlock()
	entry.mutex.Lock()
	return func() {
		entry.mutex.Unlock()
		service.gate.Lock()
		entry.users--
		if entry.users == 0 {
			delete(service.locks, key)
		}
		service.gate.Unlock()
	}
}

type AdmitCommand struct {
	RequestID          string
	RunID              string
	ExpectedRunVersion uint64
	LeaseEpoch         uint64
	Trigger            domaincleanup.Trigger
	LifecycleState     domaincleanup.LifecycleState
	Policy             domaincleanup.Policy
	OwnershipSHA256    string
	NowMillis          int64
}

type StepCommand struct {
	RunID              string
	ExpectedRunVersion uint64
	LeaseEpoch         uint64
	NowMillis          int64
}

type ExpireCommand = StepCommand

type Result struct {
	Run               domain.Run
	Progressed        bool
	Complete          bool
	Retained          bool
	WaitingExternal   bool
	CleanupAuthorized bool
	Code              string
}

type current struct {
	project domain.Project
	task    domain.Task
	run     domain.Run
	record  domain.Candidate
}

func stableID(prefix string, values ...string) string {
	digest := sha256.Sum256([]byte(strings.Join(values, "\x1f")))
	return prefix + "-" + hex.EncodeToString(digest[:16])
}

func payload(value any) json.RawMessage { encoded, _ := json.Marshal(value); return encoded }

func (service *Service) load(ctx context.Context, runID string) (current, error) {
	run, err := service.store.Run(ctx, runID)
	if err != nil {
		return current{}, err
	}
	project, err := service.store.Project(ctx, run.Execution.Scope.ProjectID)
	if err != nil {
		return current{run: run}, err
	}
	task, err := service.store.Task(ctx, run.TaskID)
	if err != nil {
		return current{project: project, run: run}, err
	}
	record, err := service.store.Candidate(ctx, run.CurrentCandidateID)
	return current{project: project, task: task, run: run, record: record}, err
}

func currentLease(value current, epoch uint64, nowMillis int64) bool {
	lease := value.project.Lease
	binding := value.run.Execution.LeaseBinding
	return value.project.ID == value.run.Execution.Scope.ProjectID && value.project.State == "active" && lease != nil &&
		domain.ValidateProjectLease(lease) == nil && lease.DispatchAllowed && lease.Epoch == epoch && binding.Epoch == epoch &&
		lease.HolderInstance == binding.HolderInstance && lease.HolderProcessIdentity == binding.HolderProcessIdentity &&
		nowMillis >= lease.AcquiredAtMillis && nowMillis < lease.ExpiresAtMillis
}

func currentCandidate(value current) bool {
	authority := value.run.Execution.CandidateAuthority
	return authority != nil && !authority.Invalidated && candidate.ValidAuthority(*authority) && candidate.ValidManifest(value.record.Manifest) &&
		value.record.ID == value.run.CurrentCandidateID && value.record.RunID == value.run.ID && authority.CandidateID == value.record.ID &&
		authority.CandidateSHA == value.record.CommitSHA && authority.BaseSHA == value.run.BaseSHA && authority.BindingSHA256 == value.record.Manifest.BindingSHA256 &&
		authority.TaskVersion == value.task.Version && authority.ConfigurationSHA256 == value.record.Manifest.ConfigurationSHA256 &&
		value.record.Claim.Branch == value.run.Execution.Branch && value.record.Claim.BaseRef == value.run.Execution.BaseRef &&
		value.record.Manifest.RepositoryBindingSHA256 == value.run.Execution.RepositoryBindingHash
}

func sameEvidence(value *candidate.EvidenceBinding, authority *candidate.Authority, id string) bool {
	return value != nil && authority != nil && value.ID == id && value.CandidateID == authority.CandidateID &&
		value.CandidateSHA == authority.CandidateSHA && value.BaseSHA == authority.BaseSHA && value.Generation == authority.Generation &&
		value.BindingSHA256 == authority.BindingSHA256
}

type integrationEvidence struct{ kind, id, sha, merge string }

func (service *Service) verifiedIntegration(ctx context.Context, value current, nowMillis int64) (integrationEvidence, bool) {
	authority := value.run.Execution.CandidateAuthority
	if authority == nil {
		return integrationEvidence{}, false
	}
	if state := value.run.Execution.Integration; state != nil && state.Phase == domainintegration.PhaseComplete &&
		domainintegration.ValidState(*state) && state.Evidence != nil && sameEvidence(authority.Downstream.Integration, authority, state.Evidence.ID) &&
		state.Binding.CandidateID == value.record.ID && state.Binding.CandidateSHA == value.record.CommitSHA &&
		state.Binding.BaseSHA == value.record.Manifest.BaseSHA && state.Binding.TreeSHA == value.record.Manifest.TreeSHA &&
		state.Binding.RepositoryBindingSHA256 == value.run.Execution.RepositoryBindingHash {
		forge, forgeErr := service.forge.ObserveIntegration(ctx, githubport.IntegrationRequest{Binding: state.Binding, TaskStoreNowMillis: nowMillis})
		git, gitErr := service.integrationGit.ObserveIntegration(ctx, gitport.IntegrationRequest{Binding: state.Binding,
			CandidateClaim: value.record.Claim, CandidateManifest: value.record.Manifest, Repository: value.run.Execution.RepositoryBinding,
			RepositoryBindingSHA256: value.run.Execution.RepositoryBindingHash, MergeCommitSHA: state.Evidence.MergeCommitSHA, TaskStoreNowMillis: nowMillis})
		if forgeErr != nil || gitErr != nil || !domainintegration.CurrentForgeObservation(forge, state.Binding, nowMillis) ||
			!domainintegration.CurrentGitObservation(git, state.Binding, state.Evidence.MergeCommitSHA, nowMillis) ||
			forge.Status != domainintegration.ForgeIntegrated || git.Status != domainintegration.GitIntegrated {
			return integrationEvidence{}, false
		}
		return integrationEvidence{kind: "pull_request", id: state.Evidence.ID, sha: state.Evidence.SHA256, merge: state.Evidence.MergeCommitSHA}, true
	}
	if state := value.run.Execution.DirectDelivery; state != nil && state.Phase == directdomain.PhaseComplete &&
		directdomain.ValidState(*state) && state.Evidence != nil && sameEvidence(authority.Downstream.Integration, authority, state.Evidence.ID) &&
		state.Binding.CandidateID == value.record.ID && state.Binding.CandidateSHA == value.record.CommitSHA &&
		state.Binding.BaseSHA == value.record.Manifest.BaseSHA && state.Binding.TreeSHA == value.record.Manifest.TreeSHA &&
		state.Binding.RepositoryBindingSHA256 == value.run.Execution.RepositoryBindingHash {
		return integrationEvidence{kind: "direct", id: state.Evidence.ID, sha: state.Evidence.SHA256, merge: value.record.CommitSHA}, true
	}
	return integrationEvidence{}, false
}

func cancellationAdmitted(value current, trigger domaincleanup.Trigger) bool {
	control := value.run.Execution.Control
	switch trigger {
	case domaincleanup.TriggerCancelled:
		return control.SchemaVersion != "" &&
			(control.Intent.Kind == execution.ControlCancelTask || control.Intent.Kind == execution.ControlEmergencyStop)
	case domaincleanup.TriggerFailed:
		return value.run.Execution.NeedsYou != nil ||
			(value.run.Execution.AgentPrompt.Observation != nil &&
				(value.run.Execution.AgentPrompt.Observation.Status == execution.ObservationErrored ||
					value.run.Execution.AgentPrompt.Observation.Status == execution.ObservationPermission))
	default:
		return false
	}
}

func reviewerBinding(run domain.Run) (agent, workspace, checkoutHash string, ok bool) {
	state := run.Execution.Review
	if state == nil || !domainreview.ValidState(*state) || state.ReviewerUUID == "" || state.Workspace.ExternalID == "" {
		return "", "", "", false
	}
	if state.AgentCleanup.Phase == domainreview.EffectComplete && state.WorkspaceCleanup.Phase == domainreview.EffectComplete &&
		state.CheckoutCleanup.Phase == domainreview.EffectComplete {
		return "", "", "", false
	}
	return state.ReviewerUUID, state.Workspace.ExternalID, domaincleanup.DigestText(state.CheckoutPath), true
}

func (service *Service) candidateObservation(ctx context.Context, value current, trigger domaincleanup.Trigger,
	lifecycle domaincleanup.LifecycleState, nowMillis int64,
) (candidate.Observation, bool) {
	stored := value.run.Execution.CandidateObservation
	storedExact := stored != nil && candidate.ValidObservation(*stored) && stored.Code == candidate.CodeOK &&
		stored.CommitSHA == value.record.CommitSHA && stored.RepositoryBindingSHA256 == value.run.Execution.RepositoryBindingHash
	if lifecycle == domaincleanup.LifecycleReclaimed {
		return func() candidate.Observation {
				if stored == nil {
					return candidate.Observation{}
				}
				return *stored
			}(),
			storedExact && stored.WorktreeClean && stored.IndexClean && stored.UntrackedAbsent && stored.IgnoredAbsent &&
				stored.FilesystemExact && stored.RegistrationExact
	}
	if trigger != domaincleanup.TriggerIntegrated {
		return func() candidate.Observation {
			if stored == nil {
				return candidate.Observation{}
			}
			return *stored
		}(), storedExact
	}
	observation, err := service.candidates.ObserveCandidate(ctx, gitport.CandidateRequest{Claim: value.record.Claim,
		Repository: value.run.Execution.RepositoryBinding, RepositoryBindingSHA256: value.run.Execution.RepositoryBindingHash,
		TaskStoreNowMillis: nowMillis})
	if err != nil {
		return observation, false
	}
	decision := candidate.Evaluate(value.record.Claim, observation, value.run.Execution.RepositoryBindingHash, nowMillis)
	return observation, decision.Kind == candidate.DecisionAdmit && decision.Manifest != nil && candidate.SameImmutableContent(value.record.Manifest, *decision.Manifest)
}

func bindingFor(value current, policy domaincleanup.Policy, owner string, observation candidate.Observation,
	evidence integrationEvidence, lifecycle domaincleanup.LifecycleState,
) domaincleanup.Binding {
	reviewerAgent, reviewerWorkspace, reviewerCheckout, _ := reviewerBinding(value.run)
	repository := value.run.Execution.RepositoryBinding
	return domaincleanup.SealBinding(domaincleanup.Binding{
		ProjectID: value.project.ID, WorkspaceID: value.run.Execution.Scope.WorkspaceID, TaskID: value.task.ID, RunID: value.run.ID,
		CandidateID: value.record.ID, CandidateSHA: value.record.CommitSHA, BaseSHA: value.record.Manifest.BaseSHA,
		TreeSHA: value.record.Manifest.TreeSHA, CandidateGeneration: value.run.Execution.CandidateAuthority.Generation,
		TaskVersion: value.task.Version, ConfigurationSHA256: value.record.Manifest.ConfigurationSHA256,
		RepositoryID: repository.RepositoryID, RepositoryBindingSHA256: value.run.Execution.RepositoryBindingHash,
		SourceDevice: observation.SourceDevice, SourceInode: observation.SourceInode, CommonDevice: observation.CommonDevice,
		CommonInode: observation.CommonInode, WorktreeDevice: observation.WorktreeDevice, WorktreeInode: observation.WorktreeInode,
		CanonicalRemoteSHA256: domaincleanup.DigestText(repository.CanonicalRemote), SourcePathSHA256: domaincleanup.DigestText(repository.SourcePath),
		CommonDirectorySHA256: domaincleanup.DigestText(repository.GitCommonDirectory), WorktreePathSHA256: domaincleanup.DigestText(repository.WorktreePath),
		Branch: value.run.Execution.Branch, BaseRef: value.run.Execution.BaseRef, WorktreeID: value.run.Execution.Worktree.ExternalID,
		TaskAgentID: value.run.Execution.Agent.ExternalID, TaskWorkspaceID: value.run.Execution.HostView.ExternalID,
		ReviewerAgentID: reviewerAgent, ReviewerWorkspaceID: reviewerWorkspace, ReviewerCheckoutSHA256: reviewerCheckout,
		OwnershipSHA256: owner, CleanupAdmittedAtMillis: observation.ObservedAtMillis,
		ReclaimedEvidenceSHA256: func() string {
			if lifecycle == domaincleanup.LifecycleReclaimed {
				return observation.FactSHA256
			}
			return ""
		}(),
		IntegrationKind: evidence.kind, IntegrationEvidenceID: evidence.id,
		IntegrationEvidenceSHA256: evidence.sha, MergeCommitSHA: evidence.merge,
		LeaseEpoch: value.run.Execution.LeaseBinding.Epoch, PolicySHA256: policy.SHA256,
	})
}

func (service *Service) persist(ctx context.Context, currentRun, next domain.Run, transition string) (domain.Run, error) {
	next.Version = currentRun.Version + 1
	commandID := stableID("command", currentRun.ID, fmt.Sprintf("version-%d", next.Version), transition)
	stateHash := ""
	if next.Execution.Cleanup != nil {
		stateHash = next.Execution.Cleanup.ID + ":" + string(next.Execution.Cleanup.Phase)
	}
	result, err := service.store.UpdateRun(ctx, domain.CommandRequest{IdempotencyKey: commandID, Type: transition,
		AggregateID: currentRun.ID, ExpectedVersion: currentRun.Version, Payload: payload(struct {
			State string `json:"state"`
		}{stateHash})}, next,
		domain.Event{ID: stableID("cleanup-event", commandID), RunID: currentRun.ID, Sequence: currentRun.Version + 2,
			AggregateID: currentRun.ID, AggregateVersion: next.Version, Type: transition, Payload: payload(struct {
				Phase string `json:"phase"`
			}{func() string {
				if next.Execution.Cleanup == nil {
					return ""
				}
				return string(next.Execution.Cleanup.Phase)
			}()})})
	if err != nil {
		return currentRun, err
	}
	if result.Outcome != domain.CommandApplied || result.Replay {
		return currentRun, ErrConcurrentTransition
	}
	return next, nil
}

// Admit records one cleanup aggregate after independently rereading Candidate,
// lease, integration, and lifecycle ownership facts. It performs no cleanup.
func (service *Service) Admit(ctx context.Context, command AdmitCommand) (Result, error) {
	unlock := service.lock("run:" + command.RunID)
	defer unlock()
	if command.RequestID == "" || command.RunID == "" || command.ExpectedRunVersion == 0 || command.LeaseEpoch == 0 ||
		command.NowMillis < 0 || !domaincleanup.ValidPolicy(command.Policy) || len(command.OwnershipSHA256) != 64 {
		return Result{}, ErrInvalidCommand
	}
	value, err := service.load(ctx, command.RunID)
	if err != nil {
		return Result{Run: value.run}, err
	}
	if value.run.Version != command.ExpectedRunVersion || value.run.Execution.Cleanup != nil || !currentCandidate(value) ||
		!currentLease(value, command.LeaseEpoch, command.NowMillis) || command.Policy.ConfigurationSHA256 != value.record.Manifest.ConfigurationSHA256 ||
		value.run.Execution.CleanupPolicy == nil || !domaincleanup.ValidPolicy(*value.run.Execution.CleanupPolicy) ||
		value.run.Execution.CleanupPolicy.SHA256 != command.Policy.SHA256 ||
		value.run.Execution.Worktree.ExternalID == "" || value.run.Execution.HostView.ExternalID == "" || value.run.Execution.Agent.ExternalID == "" {
		return Result{Run: value.run}, ErrInvalidCommand
	}
	observation, exact := service.candidateObservation(ctx, value, command.Trigger, command.LifecycleState, command.NowMillis)
	if !exact {
		return Result{Run: value.run}, ErrExternalAmbiguous
	}
	evidence := integrationEvidence{}
	if command.Trigger == domaincleanup.TriggerIntegrated {
		var verified bool
		evidence, verified = service.verifiedIntegration(ctx, value, command.NowMillis)
		if !verified {
			return Result{Run: value.run}, ErrNotReady
		}
	} else if !cancellationAdmitted(value, command.Trigger) {
		return Result{Run: value.run}, ErrNotReady
	}
	binding := bindingFor(value, command.Policy, command.OwnershipSHA256, observation, evidence, command.LifecycleState)
	state, ok := domaincleanup.NewState(binding, command.Policy, command.Trigger, command.LifecycleState)
	if !ok {
		return Result{Run: value.run}, ErrInvalidCommand
	}
	next := value.run
	next.Execution.Cleanup = &state
	next.Execution.NeedsYou = nil
	next, err = service.persist(ctx, value.run, next, "cleanup.admitted")
	return Result{Run: next, Progressed: err == nil, Retained: state.Phase == domaincleanup.PhaseRetained,
		CleanupAuthorized: state.CleanupAuthorized}, err
}

func targetFor(value current, effect domaincleanup.Effect, nowMillis int64, attempt uint32) cleanuport.Target {
	state := value.run.Execution.Cleanup
	return cleanuport.Target{Binding: state.Binding, Policy: state.Policy, Repository: value.run.Execution.RepositoryBinding,
		RepositoryBindingSHA256: value.run.Execution.RepositoryBindingHash, CandidateClaim: value.record.Claim,
		CandidateManifest: value.record.Manifest, EffectID: effect.ID, EffectKind: effect.Kind, Attempt: attempt, Snapshot: state.Snapshot,
		PrivateArtifactRoot: "", TaskStoreNowMillis: nowMillis}
}

func (service *Service) hostCommand(value current, effect domaincleanup.Effect, observe bool) (host.Command, bool) {
	state := value.run.Execution.Cleanup
	capability := host.CapabilityAgentObserve
	kind := execution.EffectAgentArchive
	agentID, workspaceID, worktreePath, title := state.Binding.TaskAgentID, state.Binding.TaskWorkspaceID,
		value.run.Execution.WorktreePath, value.task.Title
	worktreeID := state.Binding.WorktreeID
	switch effect.Kind {
	case domaincleanup.ResourceReviewerAgent:
		agentID, workspaceID, worktreePath, title = state.Binding.ReviewerAgentID, state.Binding.ReviewerWorkspaceID,
			value.run.Execution.Review.CheckoutPath, "Independent review: "+value.task.Title
		worktreeID = value.run.Execution.Review.ReviewKey
		kind = execution.EffectReviewerAgentArchive
	case domaincleanup.ResourceTaskAgent:
		kind = execution.EffectAgentArchive
	case domaincleanup.ResourceReviewerWorkspace:
		capability, kind = host.CapabilityWorkspaceObserve, execution.EffectHostViewArchive
		agentID, workspaceID, worktreePath, title = "", state.Binding.ReviewerWorkspaceID, value.run.Execution.Review.CheckoutPath, "Independent review: "+value.task.Title
		worktreeID = value.run.Execution.Review.ReviewKey
	case domaincleanup.ResourceTaskWorkspace:
		capability, kind = host.CapabilityWorkspaceObserve, execution.EffectHostViewArchive
		agentID, workspaceID, title = "", state.Binding.TaskWorkspaceID, value.task.Title
	default:
		return host.Command{}, false
	}
	if !observe {
		if effect.Kind == domaincleanup.ResourceReviewerWorkspace || effect.Kind == domaincleanup.ResourceTaskWorkspace {
			capability = host.CapabilityWorkspaceArchive
		} else {
			capability = host.CapabilityAgentArchive
		}
	}
	return host.Command{RequestID: effect.ID + map[bool]string{true: "-observe", false: "-dispatch"}[observe],
		IdempotencyKey: effect.ID, ExpectedVersion: value.run.Version, Capability: capability,
		Arguments: host.Arguments{Scope: value.run.Execution.Scope, EffectKind: kind, EffectID: effect.ID,
			WorktreeID: worktreeID, WorktreePath: worktreePath, WorkspaceID: workspaceID,
			AgentID: agentID, Title: title, BindingHash: state.Binding.SHA256}}, true
}

func (service *Service) invoke(ctx context.Context, command host.Command, nowMillis int64) (host.Observation, error) {
	descriptor, err := service.host.Describe(ctx)
	if err != nil || host.ValidateDescriptor(descriptor) != nil {
		return host.Observation{}, ErrExternalAmbiguous
	}
	observation, err := service.host.Invoke(ctx, command)
	if err != nil || host.ValidateObservation(command, observation) != nil {
		return host.Observation{}, ErrExternalAmbiguous
	}
	timestamp, err := time.Parse(time.RFC3339Nano, observation.ObservedAt)
	if err != nil || timestamp.UnixMilli() > nowMillis || nowMillis-timestamp.UnixMilli() > observation.Result.MaximumAgeMillis {
		return host.Observation{}, ErrExternalAmbiguous
	}
	return observation, nil
}

func (service *Service) observeHost(ctx context.Context, value current, effect domaincleanup.Effect, nowMillis int64) domaincleanup.Observation {
	command, ok := service.hostCommand(value, effect, true)
	if !ok {
		return domaincleanup.Observation{}
	}
	fact, err := service.invoke(ctx, command, nowMillis)
	if err != nil {
		return domaincleanup.SealObservation(domaincleanup.Observation{EffectID: effect.ID, BindingSHA256: value.run.Execution.Cleanup.Binding.SHA256,
			Kind: effect.Kind, Attempt: effect.Attempt, Status: domaincleanup.StatusUnavailable, Code: domaincleanup.CodeExternalUnavailable,
			ObservedAtMillis: nowMillis, MaximumAgeMillis: domaincleanup.MaximumObservationAgeMillis})
	}
	expectedID := value.run.Execution.Cleanup.Binding.TaskAgentID
	if effect.Kind == domaincleanup.ResourceReviewerAgent {
		expectedID = value.run.Execution.Cleanup.Binding.ReviewerAgentID
	}
	if effect.Kind == domaincleanup.ResourceTaskWorkspace {
		expectedID = value.run.Execution.Cleanup.Binding.TaskWorkspaceID
	}
	if effect.Kind == domaincleanup.ResourceReviewerWorkspace {
		expectedID = value.run.Execution.Cleanup.Binding.ReviewerWorkspaceID
	}
	status, code := domaincleanup.StatusAmbiguous, domaincleanup.CodeLifecycleMismatch
	switch fact.Result.Status {
	case execution.ObservationDesired:
		if effect.Kind == domaincleanup.ResourceReviewerAgent || effect.Kind == domaincleanup.ResourceTaskAgent {
			status = domaincleanup.StatusTerminated
		} else {
			status = domaincleanup.StatusAbsent
		}
		code = domaincleanup.CodeOK
	case execution.ObservationAbsent:
		status, code = domaincleanup.StatusAbsent, domaincleanup.CodeOK
	case execution.ObservationOwnedPresent:
		status, code = domaincleanup.StatusExactPresent, domaincleanup.CodeOK
	case execution.ObservationUnavailable:
		status, code = domaincleanup.StatusUnavailable, domaincleanup.CodeExternalUnavailable
	case execution.ObservationDifferent:
		status, code = domaincleanup.StatusDifferent, domaincleanup.CodeOwnerMismatch
	}
	if fact.Result.ExternalID != "" && fact.Result.ExternalID != expectedID {
		status, code = domaincleanup.StatusDifferent, domaincleanup.CodeOwnerMismatch
	}
	return domaincleanup.SealObservation(domaincleanup.Observation{EffectID: effect.ID, BindingSHA256: value.run.Execution.Cleanup.Binding.SHA256,
		Kind: effect.Kind, Attempt: effect.Attempt, Status: status, Code: code, ExternalID: fact.Result.ExternalID,
		Archived: status == domaincleanup.StatusTerminated, ProcessAbsent: status == domaincleanup.StatusTerminated && fact.Result.PriorDispatcherAbsent,
		PriorDispatcherAbsent: fact.Result.PriorDispatcherAbsent, ObservedAtMillis: nowMillis, MaximumAgeMillis: domaincleanup.MaximumObservationAgeMillis})
}

func (service *Service) observe(ctx context.Context, value current, effect domaincleanup.Effect, nowMillis int64) domaincleanup.Observation {
	switch effect.Kind {
	case domaincleanup.ResourceReviewerAgent, domaincleanup.ResourceTaskAgent, domaincleanup.ResourceReviewerWorkspace, domaincleanup.ResourceTaskWorkspace:
		return service.observeHost(ctx, value, effect, nowMillis)
	default:
		target := targetFor(value, effect, nowMillis, effect.Attempt)
		target.PrivateArtifactRoot = service.artifactRoot
		observation, err := service.git.Observe(ctx, target)
		if err != nil {
			return domaincleanup.SealObservation(domaincleanup.Observation{EffectID: effect.ID, BindingSHA256: value.run.Execution.Cleanup.Binding.SHA256,
				Kind: effect.Kind, Attempt: effect.Attempt, Status: domaincleanup.StatusUnavailable, Code: domaincleanup.CodeExternalUnavailable,
				ObservedAtMillis: nowMillis, MaximumAgeMillis: domaincleanup.MaximumObservationAgeMillis})
		}
		return observation
	}
}

func (service *Service) dispatchHost(ctx context.Context, value current, effect domaincleanup.Effect, nowMillis int64) error {
	command, ok := service.hostCommand(value, effect, false)
	if !ok {
		return ErrInvalidCommand
	}
	_, err := service.invoke(ctx, command, nowMillis)
	return err
}

func (service *Service) dispatchGit(ctx context.Context, value current, effect domaincleanup.Effect,
	precondition domaincleanup.Observation, nowMillis int64,
) error {
	target := targetFor(value, effect, nowMillis, effect.Attempt-1)
	target.PrivateArtifactRoot = service.artifactRoot
	command := cleanuport.DispatchCommand{Target: target, Attempt: effect.Attempt, ExpectedObservation: precondition,
		Snapshot: value.run.Execution.Cleanup.Snapshot}
	var err error
	switch effect.Kind {
	case domaincleanup.ResourceSnapshot:
		_, err = service.git.CreateSnapshot(ctx, command)
	case domaincleanup.ResourceWorktree:
		_, err = service.git.RemoveWorktree(ctx, command)
	case domaincleanup.ResourceRemoteRef:
		_, err = service.git.DeleteRemoteRef(ctx, command)
	case domaincleanup.ResourceLocalRef:
		_, err = service.git.DeleteLocalRef(ctx, command)
	default:
		err = ErrInvalidCommand
	}
	return err
}

func parkWake(code domaincleanup.Code) string {
	switch code {
	case domaincleanup.CodeExternalUnavailable:
		return "fresh_exact_cleanup_observation"
	case domaincleanup.CodeDiskPressure:
		return "sufficient_disk_without_deleting_unintegrated_work"
	default:
		return "exact_owner_repository_lifecycle_or_snapshot_repair"
	}
}

func (service *Service) park(ctx context.Context, value current, code domaincleanup.Code) (Result, error) {
	state, ok := domaincleanup.Park(*value.run.Execution.Cleanup, code, parkWake(code))
	if !ok {
		return Result{Run: value.run}, ErrExternalAmbiguous
	}
	next := value.run
	next.Execution.Cleanup = &state
	next.Execution.NeedsYou = &execution.NeedsYou{Code: execution.NeedCode(strings.ToLower(string(code))), WakeCondition: state.WakeCondition, CleanupAuthorized: false}
	next, err := service.persist(ctx, value.run, next, "cleanup."+strings.ToLower(string(code)))
	return Result{Run: next, Progressed: err == nil, Code: string(code)}, errors.Join(ErrExternalAmbiguous, err)
}

func effectAdoptable(effect domaincleanup.Effect) bool {
	if effect.Observation == nil {
		return false
	}
	switch effect.Kind {
	case domaincleanup.ResourceReviewerAgent, domaincleanup.ResourceTaskAgent:
		return (effect.Observation.Status == domaincleanup.StatusTerminated && effect.Observation.Archived && effect.Observation.ProcessAbsent) ||
			effect.Observation.Status == domaincleanup.StatusAbsent
	case domaincleanup.ResourceReviewerWorkspace, domaincleanup.ResourceTaskWorkspace, domaincleanup.ResourceWorktree,
		domaincleanup.ResourceRemoteRef, domaincleanup.ResourceLocalRef:
		return effect.Observation.Status == domaincleanup.StatusAbsent
	case domaincleanup.ResourceSnapshot:
		return effect.Observation.Status == domaincleanup.StatusVerified && effect.Observation.Snapshot != nil ||
			effect.Observation.Status == domaincleanup.StatusClean && !effect.Observation.Ignored ||
			effect.Observation.Status == domaincleanup.StatusAbsent
	default:
		return false
	}
}

func (service *Service) applyDecision(ctx context.Context, value current, decision cleanupreducer.Decision, nowMillis int64) (Result, error) {
	state := *value.run.Execution.Cleanup
	switch decision.Kind {
	case cleanupreducer.DecisionWaitExternal:
		return Result{Run: value.run, WaitingExternal: true, Code: string(decision.Code)}, ErrExternalUnavailable
	case cleanupreducer.DecisionRetain:
		if state.Phase == domaincleanup.PhaseRetained {
			return Result{Run: value.run, Retained: true}, nil
		}
		completed, ok := domaincleanup.Complete(state, nowMillis)
		if !ok {
			return Result{Run: value.run}, ErrExternalAmbiguous
		}
		next := value.run
		next.Execution.Cleanup = &completed
		next, err := service.persist(ctx, value.run, next, "cleanup.retained")
		return Result{Run: next, Progressed: err == nil, Retained: err == nil}, err
	case cleanupreducer.DecisionComplete:
		if state.Phase == domaincleanup.PhaseComplete {
			return Result{Run: value.run, Complete: true, CleanupAuthorized: true}, nil
		}
		completed, ok := domaincleanup.Complete(state, nowMillis)
		if !ok {
			return Result{Run: value.run}, ErrExternalAmbiguous
		}
		next := value.run
		next.Execution.Cleanup = &completed
		next.Execution.NeedsYou = nil
		authority := *next.Execution.CandidateAuthority
		authority.Downstream.Cleanup = &candidate.EvidenceBinding{ID: completed.Evidence.ID, CandidateID: authority.CandidateID,
			CandidateSHA: authority.CandidateSHA, BaseSHA: authority.BaseSHA, Generation: authority.Generation, BindingSHA256: authority.BindingSHA256}
		next.Execution.CandidateAuthority = &authority
		next, err := service.persist(ctx, value.run, next, "cleanup.complete")
		return Result{Run: next, Progressed: err == nil, Complete: err == nil, CleanupAuthorized: err == nil}, err
	case cleanupreducer.DecisionEscalate:
		return service.park(ctx, value, decision.Code)
	default:
		return Result{Run: value.run}, nil
	}
}

// Step advances one durable frontier. Every mutating response, including an
// error or response loss, is followed by a fresh authoritative observation.
func (service *Service) Step(ctx context.Context, command StepCommand) (Result, error) {
	unlockRun := service.lock("run:" + command.RunID)
	defer unlockRun()
	if command.RunID == "" || command.ExpectedRunVersion == 0 || command.LeaseEpoch == 0 || command.NowMillis < 0 {
		return Result{}, ErrInvalidCommand
	}
	value, err := service.load(ctx, command.RunID)
	if err != nil {
		return Result{Run: value.run}, err
	}
	if value.run.Execution.Cleanup == nil {
		return Result{Run: value.run}, ErrNotReady
	}
	unlockRepository := service.lock("repository:" + value.run.Execution.RepositoryBinding.RepositoryID)
	defer unlockRepository()
	state := *value.run.Execution.Cleanup
	if value.run.Version != command.ExpectedRunVersion || state.Binding.LeaseEpoch != command.LeaseEpoch {
		return Result{Run: value.run}, ErrConcurrentTransition
	}
	if state.Phase != domaincleanup.PhaseComplete && state.Phase != domaincleanup.PhaseRetained && state.Phase != domaincleanup.PhaseNeedsYou {
		if command.NowMillis < state.Binding.CleanupAdmittedAtMillis {
			return service.park(ctx, value, domaincleanup.CodeBindingChanged)
		}
		if command.NowMillis-state.Binding.CleanupAdmittedAtMillis > state.Policy.LifecycleMillis {
			return service.park(ctx, value, domaincleanup.CodeRecoveryLimit)
		}
	}
	integrationVerified := state.Trigger != domaincleanup.TriggerIntegrated
	remoteDeletionStarted := false
	if index := domaincleanup.EffectIndex(state, domaincleanup.ResourceRemoteRef); index >= 0 {
		remoteDeletionStarted = state.Effects[index].Attempt > 0 || state.Effects[index].Phase == domaincleanup.EffectComplete
	}
	if state.Trigger == domaincleanup.TriggerIntegrated && !remoteDeletionStarted {
		_, integrationVerified = service.verifiedIntegration(ctx, value, command.NowMillis)
	} else if state.Trigger == domaincleanup.TriggerIntegrated {
		authority := value.run.Execution.CandidateAuthority
		integrationVerified = authority != nil && sameEvidence(authority.Downstream.Integration, authority, state.Binding.IntegrationEvidenceID)
	}
	decision := cleanupreducer.Reduce(cleanupreducer.Facts{SchemaVersion: cleanupreducer.SchemaVersion, State: state,
		ProjectActive: value.project.State == "active", LeaseCurrent: currentLease(value, command.LeaseEpoch, command.NowMillis),
		CandidateCurrent: currentCandidate(value), IntegrationVerified: integrationVerified, NowMillis: command.NowMillis})
	if decision.Kind != cleanupreducer.DecisionObserve && decision.Kind != cleanupreducer.DecisionDispatch && decision.Kind != cleanupreducer.DecisionAdopt {
		return service.applyDecision(ctx, value, decision, command.NowMillis)
	}
	index := domaincleanup.EffectIndex(state, decision.EffectKind)
	if index < 0 {
		return Result{Run: value.run}, ErrExternalAmbiguous
	}
	if decision.Kind == cleanupreducer.DecisionObserve {
		observation := service.observe(ctx, value, state.Effects[index], command.NowMillis)
		observed, ok := domaincleanup.RecordObservation(state, decision.EffectKind, observation, command.NowMillis)
		if !ok {
			return service.park(ctx, value, domaincleanup.CodeResponseUnknown)
		}
		next := value.run
		next.Execution.Cleanup = &observed
		next, err := service.persist(ctx, value.run, next, "cleanup.observed")
		progressed := err == nil
		waitingExternal := observation.Status == domaincleanup.StatusUnavailable
		if waitingExternal {
			err = errors.Join(err, ErrExternalUnavailable)
		}
		return Result{Run: next, Progressed: progressed, WaitingExternal: waitingExternal,
			CleanupAuthorized: observed.CleanupAuthorized}, err
	}
	if decision.Kind == cleanupreducer.DecisionAdopt {
		effect := state.Effects[index]
		if !effectAdoptable(effect) {
			return service.park(ctx, value, domaincleanup.CodeResponseUnknown)
		}
		var snapshot *domaincleanup.SnapshotEvidence
		if effect.Observation != nil {
			snapshot = effect.Observation.Snapshot
		}
		completed, ok := domaincleanup.CompleteEffect(state, effect.Kind, snapshot)
		if !ok {
			return service.park(ctx, value, domaincleanup.CodeSnapshotUnverified)
		}
		next := value.run
		next.Execution.Cleanup = &completed
		next, err := service.persist(ctx, value.run, next, "cleanup.effect_complete")
		return Result{Run: next, Progressed: err == nil, CleanupAuthorized: completed.CleanupAuthorized}, err
	}

	precondition := *state.Effects[index].Observation
	dispatching, ok := domaincleanup.BeginDispatch(state, decision.EffectKind)
	if !ok {
		return Result{Run: value.run}, ErrExternalAmbiguous
	}
	dispatchRun := value.run
	dispatchRun.Execution.Cleanup = &dispatching
	dispatchRun, err = service.persist(ctx, value.run, dispatchRun, "cleanup.dispatching")
	if err != nil {
		return Result{Run: value.run}, err
	}
	dispatched := dispatching.Effects[domaincleanup.EffectIndex(dispatching, decision.EffectKind)]
	value.run = dispatchRun
	var dispatchErr error
	if _, hostEffect := service.hostCommand(value, dispatched, false); hostEffect {
		dispatchErr = service.dispatchHost(ctx, value, dispatched, command.NowMillis)
	} else {
		dispatchErr = service.dispatchGit(ctx, value, dispatched, precondition, command.NowMillis)
	}
	required, ok := domaincleanup.RequireObservation(dispatching, decision.EffectKind)
	if !ok {
		return Result{Run: dispatchRun}, ErrExternalAmbiguous
	}
	observingRun := dispatchRun
	observingRun.Execution.Cleanup = &required
	observingRun, err = service.persist(ctx, dispatchRun, observingRun, "cleanup.observation_required")
	if err != nil {
		return Result{Run: dispatchRun}, errors.Join(err, dispatchErr)
	}
	value, err = service.load(ctx, observingRun.ID)
	if err != nil {
		return Result{Run: observingRun}, errors.Join(err, dispatchErr)
	}
	postEffect := value.run.Execution.Cleanup.Effects[domaincleanup.EffectIndex(*value.run.Execution.Cleanup, decision.EffectKind)]
	post := service.observe(ctx, value, postEffect, command.NowMillis)
	postState, ok := domaincleanup.RecordObservation(*value.run.Execution.Cleanup, decision.EffectKind, post, command.NowMillis)
	if !ok {
		return Result{Run: value.run}, errors.Join(ErrExternalAmbiguous, dispatchErr)
	}
	postRun := value.run
	postRun.Execution.Cleanup = &postState
	postRun, err = service.persist(ctx, value.run, postRun, "cleanup.post_response_observed")
	return Result{Run: postRun, Progressed: err == nil, CleanupAuthorized: postState.CleanupAuthorized}, errors.Join(err, dispatchErr)
}

func (service *Service) parkRetention(ctx context.Context, value current, code domaincleanup.Code) (Result, error) {
	state, ok := domaincleanup.ParkRetention(*value.run.Execution.Cleanup, code, parkWake(code))
	if !ok {
		return Result{Run: value.run}, ErrExternalAmbiguous
	}
	next := value.run
	next.Execution.Cleanup = &state
	next.Execution.NeedsYou = &execution.NeedsYou{Code: execution.NeedCode(strings.ToLower(string(code))),
		WakeCondition: state.RetentionWakeCondition, CleanupAuthorized: false}
	next, err := service.persist(ctx, value.run, next, "cleanup.retention."+strings.ToLower(string(code)))
	return Result{Run: next, Progressed: err == nil, Complete: true, Code: string(code)}, errors.Join(ErrExternalAmbiguous, err)
}

// Expire advances separately guarded private-artifact and recovery-ref expiry.
// It is a scheduled no-op before the exact retention boundary and never makes
// incomplete cleanup block Task delivery/closure evidence.
func (service *Service) Expire(ctx context.Context, command ExpireCommand) (Result, error) {
	unlockRun := service.lock("run:" + command.RunID)
	defer unlockRun()
	if command.RunID == "" || command.ExpectedRunVersion == 0 || command.LeaseEpoch == 0 || command.NowMillis < 0 {
		return Result{}, ErrInvalidCommand
	}
	value, err := service.load(ctx, command.RunID)
	if err != nil {
		return Result{Run: value.run}, err
	}
	state := value.run.Execution.Cleanup
	if state == nil || state.Phase != domaincleanup.PhaseComplete || state.Snapshot == nil ||
		value.run.Version != command.ExpectedRunVersion || state.Binding.LeaseEpoch != command.LeaseEpoch ||
		!currentCandidate(value) || !currentLease(value, command.LeaseEpoch, command.NowMillis) {
		return Result{Run: value.run}, ErrNotReady
	}
	unlockRepository := service.lock("repository:" + value.run.Execution.RepositoryBinding.RepositoryID)
	defer unlockRepository()
	if state.RetentionNeedsYouCode != "" {
		return Result{Run: value.run, Complete: true, Code: state.RetentionNeedsYouCode}, ErrExternalAmbiguous
	}
	if state.RetentionComplete {
		return Result{Run: value.run, Complete: true, CleanupAuthorized: state.CleanupAuthorized}, nil
	}
	if command.NowMillis < state.Snapshot.RetentionUntilMillis {
		return Result{Run: value.run, Complete: true, Retained: true, CleanupAuthorized: state.CleanupAuthorized,
			Code: string(domaincleanup.CodeRetentionNotExpired)}, nil
	}

	index := -1
	for candidateIndex := range state.RetentionEffects {
		if state.RetentionEffects[candidateIndex].Phase != domaincleanup.EffectComplete {
			index = candidateIndex
			break
		}
	}
	if index < 0 {
		nextState, ok := domaincleanup.StartRetentionExpiry(*state, command.NowMillis)
		if !ok || !nextState.RetentionComplete {
			return Result{Run: value.run}, ErrExternalAmbiguous
		}
		next := value.run
		next.Execution.Cleanup = &nextState
		next, err = service.persist(ctx, value.run, next, "cleanup.retention.complete")
		return Result{Run: next, Progressed: err == nil, Complete: err == nil, CleanupAuthorized: err == nil}, err
	}
	effect := state.RetentionEffects[index]
	if effect.Phase == domaincleanup.EffectRetained {
		nextState, ok := domaincleanup.StartRetentionExpiry(*state, command.NowMillis)
		if !ok {
			return Result{Run: value.run}, ErrExternalAmbiguous
		}
		next := value.run
		next.Execution.Cleanup = &nextState
		next, err = service.persist(ctx, value.run, next, "cleanup.retention.intent")
		return Result{Run: next, Progressed: err == nil, Complete: true, CleanupAuthorized: nextState.CleanupAuthorized}, err
	}

	observe := func(current current, currentEffect domaincleanup.Effect) domaincleanup.Observation {
		target := targetFor(current, currentEffect, command.NowMillis, currentEffect.Attempt)
		target.PrivateArtifactRoot = service.artifactRoot
		observation, observeErr := service.git.Observe(ctx, target)
		if observeErr != nil {
			return domaincleanup.SealObservation(domaincleanup.Observation{EffectID: currentEffect.ID, BindingSHA256: state.Binding.SHA256,
				Kind: currentEffect.Kind, Attempt: currentEffect.Attempt, Status: domaincleanup.StatusUnavailable,
				Code: domaincleanup.CodeExternalUnavailable, ObservedAtMillis: command.NowMillis,
				MaximumAgeMillis: domaincleanup.MaximumObservationAgeMillis})
		}
		return observation
	}

	if effect.Observation == nil || effect.Observation.Status == domaincleanup.StatusUnavailable {
		observation := observe(value, effect)
		if observation.Status == domaincleanup.StatusDifferent || observation.Status == domaincleanup.StatusAmbiguous {
			return service.parkRetention(ctx, value, observation.Code)
		}
		observed, ok := domaincleanup.RecordRetentionObservation(*state, effect.Kind, observation, command.NowMillis)
		if !ok {
			return service.parkRetention(ctx, value, domaincleanup.CodeResponseUnknown)
		}
		next := value.run
		next.Execution.Cleanup = &observed
		next, err = service.persist(ctx, value.run, next, "cleanup.retention.observed")
		return Result{Run: next, Progressed: err == nil, Complete: true, WaitingExternal: observation.Status == domaincleanup.StatusUnavailable,
			CleanupAuthorized: observed.CleanupAuthorized}, err
	}
	if effect.Observation.Status == domaincleanup.StatusAbsent {
		completed, ok := domaincleanup.CompleteRetentionEffect(*state, effect.Kind)
		if !ok {
			return service.parkRetention(ctx, value, domaincleanup.CodeResponseUnknown)
		}
		next := value.run
		next.Execution.Cleanup = &completed
		next, err = service.persist(ctx, value.run, next, "cleanup.retention.effect_complete")
		return Result{Run: next, Progressed: err == nil, Complete: true, CleanupAuthorized: completed.CleanupAuthorized}, err
	}
	if effect.Observation.Status != domaincleanup.StatusExactPresent || effect.Phase != domaincleanup.EffectIntent {
		return service.parkRetention(ctx, value, domaincleanup.CodeResponseUnknown)
	}
	precondition := *effect.Observation
	dispatching, ok := domaincleanup.BeginRetentionDispatch(*state, effect.Kind)
	if !ok {
		return service.parkRetention(ctx, value, domaincleanup.CodeResponseUnknown)
	}
	dispatchRun := value.run
	dispatchRun.Execution.Cleanup = &dispatching
	dispatchRun, err = service.persist(ctx, value.run, dispatchRun, "cleanup.retention.dispatching")
	if err != nil {
		return Result{Run: value.run}, err
	}
	dispatched := dispatching.RetentionEffects[domaincleanup.RetentionEffectIndex(dispatching, effect.Kind)]
	target := targetFor(current{project: value.project, task: value.task, run: dispatchRun, record: value.record}, dispatched,
		command.NowMillis, dispatched.Attempt-1)
	target.PrivateArtifactRoot = service.artifactRoot
	dispatchCommand := cleanuport.DispatchCommand{Target: target, Attempt: dispatched.Attempt, ExpectedObservation: precondition, Snapshot: state.Snapshot}
	var dispatchErr error
	if effect.Kind == domaincleanup.ResourcePrivateArtifact {
		_, dispatchErr = service.git.ExpirePrivateArtifact(ctx, dispatchCommand)
	} else {
		_, dispatchErr = service.git.ExpireRecoveryRefs(ctx, dispatchCommand)
	}
	required, ok := domaincleanup.RequireRetentionObservation(dispatching, effect.Kind)
	if !ok {
		return Result{Run: dispatchRun}, ErrExternalAmbiguous
	}
	observingRun := dispatchRun
	observingRun.Execution.Cleanup = &required
	observingRun, err = service.persist(ctx, dispatchRun, observingRun, "cleanup.retention.observation_required")
	if err != nil {
		return Result{Run: dispatchRun}, errors.Join(err, dispatchErr)
	}
	value, err = service.load(ctx, observingRun.ID)
	if err != nil {
		return Result{Run: observingRun}, errors.Join(err, dispatchErr)
	}
	postEffect := value.run.Execution.Cleanup.RetentionEffects[domaincleanup.RetentionEffectIndex(*value.run.Execution.Cleanup, effect.Kind)]
	post := observe(value, postEffect)
	if post.Status == domaincleanup.StatusExactPresent || post.Status == domaincleanup.StatusDifferent || post.Status == domaincleanup.StatusAmbiguous {
		result, parkErr := service.parkRetention(ctx, value, domaincleanup.CodeResponseUnknown)
		return result, errors.Join(parkErr, dispatchErr)
	}
	postState, ok := domaincleanup.RecordRetentionObservation(*value.run.Execution.Cleanup, effect.Kind, post, command.NowMillis)
	if !ok {
		return Result{Run: value.run}, errors.Join(ErrExternalAmbiguous, dispatchErr)
	}
	postRun := value.run
	postRun.Execution.Cleanup = &postState
	postRun, err = service.persist(ctx, value.run, postRun, "cleanup.retention.post_response_observed")
	return Result{Run: postRun, Progressed: err == nil, Complete: true, CleanupAuthorized: postState.CleanupAuthorized}, errors.Join(err, dispatchErr)
}
