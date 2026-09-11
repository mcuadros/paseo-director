// SPDX-License-Identifier: Apache-2.0

package cleanup

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/mcuadros/director-engine/domain"
	"github.com/mcuadros/director-engine/domain/candidate"
	domaincleanup "github.com/mcuadros/director-engine/domain/cleanup"
	"github.com/mcuadros/director-engine/domain/execution"
	domainintegration "github.com/mcuadros/director-engine/domain/integration"
	"github.com/mcuadros/director-engine/domain/repository"
	cleanuport "github.com/mcuadros/director-engine/ports/cleanup"
	gitport "github.com/mcuadros/director-engine/ports/git"
	githubport "github.com/mcuadros/director-engine/ports/github"
	"github.com/mcuadros/director-engine/ports/host"
)

type memoryStore struct {
	mu       sync.Mutex
	project  domain.Project
	task     domain.Task
	run      domain.Run
	record   domain.Candidate
	commands map[string]domain.CommandResult
}

func cloneRun(value domain.Run) domain.Run {
	encoded, _ := json.Marshal(value)
	var result domain.Run
	_ = json.Unmarshal(encoded, &result)
	return result
}

func (store *memoryStore) Project(context.Context, string) (domain.Project, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	return store.project, nil
}
func (store *memoryStore) Task(context.Context, string) (domain.Task, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	return store.task, nil
}
func (store *memoryStore) Run(context.Context, string) (domain.Run, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	return cloneRun(store.run), nil
}
func (store *memoryStore) Candidate(context.Context, string) (domain.Candidate, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	return store.record, nil
}
func (store *memoryStore) UpdateRun(_ context.Context, command domain.CommandRequest, next domain.Run, event domain.Event) (domain.CommandResult, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	if prior, ok := store.commands[command.IdempotencyKey]; ok {
		prior.Replay = true
		return prior, nil
	}
	if command.ExpectedVersion != store.run.Version {
		result := domain.CommandResult{Outcome: domain.CommandRejectedVersionConflict, ObservedVersion: store.run.Version, EventID: event.ID}
		store.commands[command.IdempotencyKey] = result
		return result, nil
	}
	if next.Version != store.run.Version+1 || event.AggregateVersion != next.Version || next.Execution.CleanupPolicy == nil ||
		!domaincleanup.ValidPolicy(*next.Execution.CleanupPolicy) || next.Execution.Cleanup == nil || !domaincleanup.ValidState(*next.Execution.Cleanup) {
		return domain.CommandResult{}, errors.New("invalid cleanup write")
	}
	store.run = cloneRun(next)
	result := domain.CommandResult{Outcome: domain.CommandApplied, ObservedVersion: next.Version, EventID: event.ID}
	store.commands[command.IdempotencyKey] = result
	return result, nil
}

type fakeCandidate struct{ observation candidate.Observation }

func (adapter *fakeCandidate) ObserveCandidate(_ context.Context, request gitport.CandidateRequest) (candidate.Observation, error) {
	value := adapter.observation
	value.ObservedAtMillis = request.TaskStoreNowMillis
	return candidate.SealObservation(value), nil
}

type fakeIntegrationGit struct{}

func integratedGit(binding domainintegration.Binding, now int64) domainintegration.GitObservation {
	return domainintegration.SealGitObservation(domainintegration.GitObservation{ID: "git-integrated", Status: domainintegration.GitIntegrated,
		Code: domainintegration.CodeOK, RepositoryID: binding.RepositoryID, CanonicalRemote: binding.CanonicalRemote,
		HeadRef: "refs/heads/" + binding.Branch, HeadSHA: binding.CandidateSHA, BaseRef: binding.BaseRef,
		BaseSHA: strings.Repeat("d", len(binding.CandidateSHA)), DefaultRef: binding.BaseRef, DefaultSHA: strings.Repeat("d", len(binding.CandidateSHA)),
		MergeCommitSHA: strings.Repeat("d", len(binding.CandidateSHA)), ParentSHAs: []string{binding.BaseSHA, binding.CandidateSHA},
		TreeSHA: binding.TreeSHA, ObservedAtMillis: now, MaximumAgeMillis: domainintegration.MaximumObservationAgeMS})
}
func (*fakeIntegrationGit) ObserveIntegration(_ context.Context, request gitport.IntegrationRequest) (domainintegration.GitObservation, error) {
	return integratedGit(request.Binding, request.TaskStoreNowMillis), nil
}

type fakeForge struct{ mismatch bool }

func integratedForge(binding domainintegration.Binding, now int64) domainintegration.ForgeObservation {
	return domainintegration.SealForgeObservation(domainintegration.ForgeObservation{ID: "forge-integrated", Status: domainintegration.ForgeIntegrated,
		Code: domainintegration.CodeOK, RepositoryID: binding.GitHubRepositoryID, RepositoryNodeID: binding.GitHubRepositoryNodeID,
		Owner: binding.RepositoryOwner, Name: binding.RepositoryName, ViewerLogin: binding.ViewerLogin, Authenticated: true,
		CanMerge: true, TLSVerified: true, RateRemaining: 100, APIVersion: "2022-11-28", AtomicExpectedHead: true,
		DefaultBranch: strings.TrimPrefix(binding.BaseRef, "refs/heads/"), PullRequestNumber: binding.PullRequestNumber,
		PullRequestNodeID: binding.PullRequestNodeID, PullRequestState: "closed", HeadSHA: binding.CandidateSHA,
		HeadRef: binding.Branch, BaseRef: strings.TrimPrefix(binding.BaseRef, "refs/heads/"), HeadRepositoryID: binding.GitHubRepositoryID,
		BaseRepositoryID: binding.GitHubRepositoryID, HeadOwner: binding.ViewerLogin, AuthorLogin: binding.ViewerLogin,
		MarkerSHA256: binding.MarkerSHA256, MarkerCount: 1, Merged: true, MergedAtMillis: 1_900,
		MergeCommitSHA: strings.Repeat("d", len(binding.CandidateSHA)), ObservedAtMillis: now, MaximumAgeMillis: domainintegration.MaximumObservationAgeMS})
}
func (forge *fakeForge) ObserveIntegration(_ context.Context, request githubport.IntegrationRequest) (domainintegration.ForgeObservation, error) {
	value := integratedForge(request.Binding, request.TaskStoreNowMillis)
	if forge.mismatch {
		value.HeadSHA = strings.Repeat("e", len(value.HeadSHA))
		value = domainintegration.SealForgeObservation(value)
	}
	return value, nil
}
func (*fakeForge) MergeExpectedHead(context.Context, githubport.MergeRequest) (githubport.MergeResult, error) {
	return githubport.MergeResult{}, errors.New("cleanup cannot merge")
}

type fakeHost struct {
	mu         sync.Mutex
	cursor     uint64
	agents     map[string]bool
	workspaces map[string]bool
	archives   []string
	lose       map[string]bool
}

func newFakeHost() *fakeHost {
	return &fakeHost{agents: map[string]bool{"task-agent-1": true}, workspaces: map[string]bool{"task-workspace-1": true}, lose: map[string]bool{}}
}
func (*fakeHost) Describe(context.Context) (host.Descriptor, error) {
	definition, _ := host.EmbeddedDefinition()
	hash, _ := host.SchemaSHA256()
	return host.Descriptor{CredentialScope: definition.CredentialScope, ContractVersion: definition.ContractVersion,
		ContractHash: hash, Capabilities: definition.Capabilities}, nil
}
func (adapter *fakeHost) observation(command host.Command, status execution.ObservationStatus, externalID string) host.Observation {
	adapter.cursor++
	result := host.ObservationResult{EffectID: command.Arguments.EffectID, Status: status, ExternalID: externalID,
		BindingHash: command.Arguments.BindingHash, PriorDispatcherAbsent: true, MaximumAgeMillis: 5_000}
	result.FactHash = host.ObservationResultHash(result)
	return host.Observation{RequestID: command.RequestID, Cursor: adapter.cursor,
		ObservedAt: time.UnixMilli(2_000).UTC().Format(time.RFC3339Nano), Result: result}
}
func (adapter *fakeHost) Invoke(_ context.Context, command host.Command) (host.Observation, error) {
	adapter.mu.Lock()
	defer adapter.mu.Unlock()
	id := command.Arguments.AgentID
	collection := adapter.agents
	if command.Capability == host.CapabilityWorkspaceObserve || command.Capability == host.CapabilityWorkspaceArchive {
		id, collection = command.Arguments.WorkspaceID, adapter.workspaces
	}
	active := collection[id]
	if command.Capability == host.CapabilityAgentObserve || command.Capability == host.CapabilityWorkspaceObserve {
		if active {
			return adapter.observation(command, execution.ObservationOwnedPresent, id), nil
		}
		return adapter.observation(command, execution.ObservationDesired, id), nil
	}
	collection[id] = false
	adapter.archives = append(adapter.archives, id)
	if adapter.lose[id] {
		return host.Observation{}, errors.New("response lost")
	}
	return adapter.observation(command, execution.ObservationDesired, id), nil
}

type fakeCleanupGit struct {
	mu              sync.Mutex
	status          map[domaincleanup.ResourceKind]domaincleanup.Status
	snapshotDirty   bool
	snapshotPrivate bool
	snapshot        *domaincleanup.SnapshotEvidence
	dispatches      map[domaincleanup.ResourceKind]uint64
	lose            map[domaincleanup.ResourceKind]bool
	recreateRemote  bool
}

func newFakeCleanupGit() *fakeCleanupGit {
	return &fakeCleanupGit{status: map[domaincleanup.ResourceKind]domaincleanup.Status{
		domaincleanup.ResourceWorktree: domaincleanup.StatusExactPresent, domaincleanup.ResourceRemoteRef: domaincleanup.StatusExactPresent,
		domaincleanup.ResourceLocalRef: domaincleanup.StatusExactPresent, domaincleanup.ResourcePrivateArtifact: domaincleanup.StatusExactPresent,
		domaincleanup.ResourceRecoveryRef: domaincleanup.StatusExactPresent}, dispatches: map[domaincleanup.ResourceKind]uint64{}, lose: map[domaincleanup.ResourceKind]bool{}}
}
func (adapter *fakeCleanupGit) observation(target cleanuport.Target) domaincleanup.Observation {
	status := adapter.status[target.EffectKind]
	if target.EffectKind == domaincleanup.ResourceSnapshot {
		if adapter.snapshot != nil {
			status = domaincleanup.StatusVerified
		} else if adapter.snapshotDirty {
			status = domaincleanup.StatusDirty
		} else {
			status = domaincleanup.StatusClean
		}
	}
	value := domaincleanup.Observation{EffectID: target.EffectID, BindingSHA256: target.Binding.SHA256, Kind: target.EffectKind,
		Attempt: target.Attempt, Status: status, Code: domaincleanup.CodeOK, CandidateTreeSHA: target.Binding.TreeSHA,
		ProspectiveTreeSHA: target.Binding.TreeSHA, IndexTreeSHA: target.Binding.TreeSHA,
		Dirty: adapter.snapshotDirty, CurrentOID: func() string {
			if status == domaincleanup.StatusExactPresent && (target.EffectKind == domaincleanup.ResourceRemoteRef || target.EffectKind == domaincleanup.ResourceLocalRef) {
				return target.Binding.CandidateSHA
			}
			return ""
		}(),
		ObservedAtMillis: target.TaskStoreNowMillis, MaximumAgeMillis: domaincleanup.MaximumObservationAgeMillis}
	if adapter.snapshot != nil && target.EffectKind == domaincleanup.ResourceSnapshot {
		snapshot := *adapter.snapshot
		value.Snapshot = &snapshot
	}
	return domaincleanup.SealObservation(value)
}
func (adapter *fakeCleanupGit) Observe(_ context.Context, target cleanuport.Target) (domaincleanup.Observation, error) {
	adapter.mu.Lock()
	defer adapter.mu.Unlock()
	return adapter.observation(target), nil
}
func (adapter *fakeCleanupGit) dispatch(command cleanuport.DispatchCommand, kind domaincleanup.ResourceKind) (cleanuport.DispatchResult, error) {
	adapter.mu.Lock()
	defer adapter.mu.Unlock()
	adapter.dispatches[kind]++
	if kind == domaincleanup.ResourceSnapshot {
		value := domaincleanup.SealSnapshot(domaincleanup.SnapshotEvidence{BindingSHA256: command.Target.Binding.SHA256,
			WorktreeRef: "refs/director/recovery/dir-m4.10/run-1/0123456789abcdef/worktree", WorktreeCommitSHA: strings.Repeat("8", 40),
			WorktreeTreeSHA: command.Target.Binding.TreeSHA, IndexTreeSHA: command.Target.Binding.TreeSHA,
			CreatedAtMillis:      command.Target.Binding.CleanupAdmittedAtMillis,
			RetentionUntilMillis: command.Target.Binding.CleanupAdmittedAtMillis + command.Target.Policy.RetentionMillis})
		if adapter.snapshotPrivate {
			value.PrivateArtifactID, value.PrivateArtifactSHA256 = "artifact-1", strings.Repeat("9", 64)
			value.EntryCount, value.AggregateBytes = 1, 10
			value = domaincleanup.SealSnapshot(value)
		}
		adapter.snapshot = &value
	} else if kind == domaincleanup.ResourceRemoteRef && adapter.recreateRemote {
		adapter.status[kind] = domaincleanup.StatusExactPresent
	} else {
		adapter.status[kind] = domaincleanup.StatusAbsent
	}
	if adapter.lose[kind] {
		return cleanuport.DispatchResult{Handoff: true, Code: domaincleanup.CodeResponseUnknown}, errors.New("response lost")
	}
	return cleanuport.DispatchResult{Handoff: true, Code: domaincleanup.CodeOK}, nil
}
func (adapter *fakeCleanupGit) CreateSnapshot(_ context.Context, command cleanuport.DispatchCommand) (cleanuport.DispatchResult, error) {
	return adapter.dispatch(command, domaincleanup.ResourceSnapshot)
}
func (adapter *fakeCleanupGit) RemoveWorktree(_ context.Context, command cleanuport.DispatchCommand) (cleanuport.DispatchResult, error) {
	return adapter.dispatch(command, domaincleanup.ResourceWorktree)
}
func (adapter *fakeCleanupGit) DeleteRemoteRef(_ context.Context, command cleanuport.DispatchCommand) (cleanuport.DispatchResult, error) {
	return adapter.dispatch(command, domaincleanup.ResourceRemoteRef)
}
func (adapter *fakeCleanupGit) DeleteLocalRef(_ context.Context, command cleanuport.DispatchCommand) (cleanuport.DispatchResult, error) {
	return adapter.dispatch(command, domaincleanup.ResourceLocalRef)
}
func (adapter *fakeCleanupGit) ExpirePrivateArtifact(_ context.Context, command cleanuport.DispatchCommand) (cleanuport.DispatchResult, error) {
	return adapter.dispatch(command, domaincleanup.ResourcePrivateArtifact)
}
func (adapter *fakeCleanupGit) ExpireRecoveryRefs(_ context.Context, command cleanuport.DispatchCommand) (cleanuport.DispatchResult, error) {
	return adapter.dispatch(command, domaincleanup.ResourceRecoveryRef)
}

type fixture struct {
	store   *memoryStore
	host    *fakeHost
	git     *fakeCleanupGit
	forge   *fakeForge
	service *Service
	policy  domaincleanup.Policy
}

func candidateRecord(repositoryHash string) (domain.Candidate, candidate.Authority, candidate.Observation) {
	claim := candidate.Claim{SchemaVersion: candidate.ClaimSchemaVersion, ID: "claim-1", ProjectID: "project-1", WorkspaceID: "workspace-1",
		TaskID: "dir-m4.10", RunID: "run-1", ActorID: "task-agent-1", WorktreeID: "worktree-1", Branch: "task/dir-m4.10-cleanup",
		BaseRef: "refs/heads/main", CandidateSHA: strings.Repeat("b", 40), BaseSHA: strings.Repeat("a", 40), LeaseEpoch: 1,
		ExpectedRunVersion: 1, TaskVersion: 3, AcceptanceSHA256: strings.Repeat("1", 64), ConfigurationSHA256: strings.Repeat("2", 64),
		ProfileSHA256: strings.Repeat("3", 64), ContextSHA256: strings.Repeat("4", 64), DecisionsSHA256: strings.Repeat("5", 64), FindingsSHA256: strings.Repeat("6", 64)}
	observation := candidate.SealObservation(candidate.Observation{ClaimSHA256: candidate.ClaimSHA256(claim), RepositoryBindingSHA256: repositoryHash,
		ObservedAtMillis: 2_000, MaximumAgeMillis: candidate.MaximumObservationAgeMS, ObjectFormat: "sha1", CommitSHA: claim.CandidateSHA,
		BaseSHA: claim.BaseSHA, ParentSHA: claim.BaseSHA, TreeSHA: strings.Repeat("c", 40), BranchHeadSHA: claim.CandidateSHA,
		BaseRefHeadSHA: claim.BaseSHA, DiffSHA256: strings.Repeat("7", 64), ChangedPathsSHA256: strings.Repeat("8", 64),
		SourceDevice: 1, SourceInode: 2, CommonDevice: 1, CommonInode: 3, WorktreeDevice: 1, WorktreeInode: 4,
		RepositoryExact: true, RemoteExact: true, PathsCanonical: true, RegistrationExact: true, ObjectPresent: true,
		ObjectStoreOwned: true, BranchStable: true, BaseStable: true, DescendsFromBase: true, DirectParent: true,
		WorktreeClean: true, IndexClean: true, UntrackedAbsent: true, IgnoredAbsent: true, SubmodulesClean: true,
		ConflictFree: true, IntentToAddAbsent: true, SparseCheckoutAbsent: true, FilesystemExact: true,
		SnapshotSHA256: strings.Repeat("9", 64), Code: candidate.CodeOK})
	manifest := candidate.Manifest{SchemaVersion: candidate.ManifestSchemaVersion, CandidateSHA: claim.CandidateSHA, BaseSHA: claim.BaseSHA,
		ParentSHA: claim.BaseSHA, TreeSHA: observation.TreeSHA, DiffSHA256: observation.DiffSHA256, ChangedPathsSHA256: observation.ChangedPathsSHA256,
		RepositoryBindingSHA256: repositoryHash, ClaimSHA256: candidate.ClaimSHA256(claim), ObservationSHA256: observation.FactSHA256,
		AcceptanceSHA256: claim.AcceptanceSHA256, ConfigurationSHA256: claim.ConfigurationSHA256, ProfileSHA256: claim.ProfileSHA256,
		ContextSHA256: claim.ContextSHA256, DecisionsSHA256: claim.DecisionsSHA256, FindingsSHA256: claim.FindingsSHA256,
		GraphPolicySHA256: candidate.GraphPolicySHA256(claim.GraphPolicy)}
	manifest.BindingSHA256 = candidate.ManifestSHA256(manifest)
	record := domain.Candidate{SchemaVersion: domain.CandidateSchemaVersion, ID: "candidate-1", RunID: claim.RunID,
		Sequence: 1, CommitSHA: claim.CandidateSHA, Claim: claim, Manifest: manifest, AdmittedAtMillis: 100}
	return record, candidate.NewAuthority(0, record.ID, claim.Branch, claim.TaskVersion, manifest), observation
}

func completedIntegration(t *testing.T, record domain.Candidate, authority candidate.Authority, repositoryBinding execution.RepositoryBinding) (domainintegration.Policy, domainintegration.State) {
	t.Helper()
	policy, _ := domainintegration.NewPolicy(domainintegration.ModeAutomatic, record.Manifest.ConfigurationSHA256)
	binding := domainintegration.SealBinding(domainintegration.Binding{TaskID: record.Claim.TaskID, RunID: record.RunID, CandidateID: record.ID,
		CandidateSHA: record.CommitSHA, BaseSHA: record.Manifest.BaseSHA, TreeSHA: record.Manifest.TreeSHA, ManifestSHA256: record.Manifest.BindingSHA256,
		CandidateGeneration: authority.Generation, TaskVersion: record.Claim.TaskVersion, ConfigurationSHA256: record.Manifest.ConfigurationSHA256,
		RepositoryID: repositoryBinding.RepositoryID, RepositoryBindingSHA256: record.Manifest.RepositoryBindingSHA256,
		CanonicalRemote: repositoryBinding.CanonicalRemote, GitHubRepositoryID: 123, GitHubRepositoryNodeID: "R_node",
		RepositoryOwner: "example", RepositoryName: "product", ViewerLogin: "example", Branch: record.Claim.Branch, BaseRef: record.Claim.BaseRef,
		PullRequestNumber: 7, PullRequestNodeID: "PR_node", OwnershipSHA256: strings.Repeat("a", 64), MarkerSHA256: strings.Repeat("b", 64),
		PublicationEvidenceID: "publication-1", ValidationPolicySHA256: strings.Repeat("c", 64), ValidationEvidenceID: "validation-1",
		ValidationEvidenceSHA256: strings.Repeat("d", 64), ReviewEvidenceID: "review-1", ReviewerUUID: "11111111-1111-4111-8111-111111111111",
		CIObservationID: "ci-1", CIObservationSHA256: strings.Repeat("e", 64), FeedbackStateSHA256: strings.Repeat("f", 64),
		ReadyEvidenceID: "ready-1", PolicySHA256: policy.SHA256, LeaseEpoch: 1})
	state, ok := domainintegration.NewState(binding, policy)
	if !ok {
		t.Fatal("integration state")
	}
	forge := integratedForge(binding, 2_000)
	git := integratedGit(binding, 2_000)
	observation := domainintegration.SealObservation(domainintegration.Observation{BindingSHA256: binding.SHA256, Status: domainintegration.ObservationIntegrated,
		Code: domainintegration.CodeOK, ForgeBefore: forge, ForgeAfter: forge, GitBefore: git, GitAfter: git,
		LiveValidationID: "validation-1", LiveValidationSHA256: strings.Repeat("1", 64), ConfiguredCheckCount: 1, ObservedCheckCount: 1,
		FeedbackSnapshotSHA256: strings.Repeat("2", 64), FeedbackClear: true, MergeCommitSHA: strings.Repeat("d", 40),
		ObservedAtMillis: 2_000, MaximumAgeMillis: domainintegration.MaximumObservationAgeMS})
	state, ok = domainintegration.RecordObservation(state, observation, 2_000)
	if !ok {
		t.Fatal("record integration")
	}
	state, ok = domainintegration.Complete(state, observation, 2_000)
	if !ok {
		t.Fatal("complete integration")
	}
	return policy, state
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	identity, _ := repository.CanonicalRemote("https://github.com/example/product")
	repositoryBinding := execution.RepositoryBinding{RepositoryID: identity.ID, RepositoryKey: identity.Key, CanonicalRemote: identity.Canonical,
		SourcePath: "/srv/source", SourceDevice: 1, SourceInode: 2, GitCommonDirectory: "/srv/source/.git", GitCommonDevice: 1,
		GitCommonInode: 3, WorktreePath: "/srv/worktree", Branch: "task/dir-m4.10-cleanup", BaseSHA: strings.Repeat("a", 40)}
	repositoryHash := execution.RepositoryBindingSHA256(repositoryBinding)
	record, authority, candidateObservation := candidateRecord(repositoryHash)
	integrationPolicy, integration := completedIntegration(t, record, authority, repositoryBinding)
	authority.Downstream.Integration = &candidate.EvidenceBinding{ID: integration.Evidence.ID, CandidateID: authority.CandidateID,
		CandidateSHA: authority.CandidateSHA, BaseSHA: authority.BaseSHA, Generation: authority.Generation, BindingSHA256: authority.BindingSHA256}
	cleanupPolicy, _ := domaincleanup.NewPolicy(record.Manifest.ConfigurationSHA256)
	task := domain.Task{ID: record.Claim.TaskID, ProjectID: record.Claim.ProjectID, Key: "DIR-M4.10", Title: "Implement local recovery and remote cleanup",
		Objective: "Clean only exact owned resources", AcceptanceCriteria: "Dirty work remains recoverable", WorkspaceIDs: []string{"workspace-1"}, Version: 3}
	run := domain.Run{ID: record.RunID, TaskID: task.ID, Number: 1, BaseSHA: record.Manifest.BaseSHA, CurrentCandidateID: record.ID, Version: 5,
		Execution: execution.State{SchemaVersion: execution.SchemaVersion, Scope: execution.Scope{ProjectID: task.ProjectID, WorkspaceID: "workspace-1", TaskID: task.ID, RunID: record.RunID},
			LeaseBinding:      execution.LeaseBinding{HolderInstance: "engine-1", HolderProcessIdentity: "process-1", Epoch: 1},
			RepositoryBinding: repositoryBinding, RepositoryBindingHash: repositoryHash, SourcePath: repositoryBinding.SourcePath,
			WorktreePath: repositoryBinding.WorktreePath, Branch: repositoryBinding.Branch, BaseRef: record.Claim.BaseRef,
			TaskTitle: task.Title, Worktree: execution.Effect{ExternalID: "worktree-1"}, HostView: execution.Effect{ExternalID: "task-workspace-1"},
			Agent: execution.Effect{ExternalID: "task-agent-1"}, CandidateObservation: &candidateObservation,
			CandidateAuthority: &authority, IntegrationPolicy: &integrationPolicy, Integration: &integration, CleanupPolicy: &cleanupPolicy}}
	store := &memoryStore{project: domain.Project{ID: task.ProjectID, State: "active", Lease: &domain.ProjectLease{HolderInstance: "engine-1",
		HolderProcessIdentity: "process-1", Epoch: 1, AcquiredAtMillis: 1, RenewedAtMillis: 1, ExpiresAtMillis: 100_000, DispatchAllowed: true}},
		task: task, run: run, record: record, commands: map[string]domain.CommandResult{}}
	hostAdapter, cleanupGit, forge := newFakeHost(), newFakeCleanupGit(), &fakeForge{}
	service, err := NewService(store, hostAdapter, &fakeCandidate{observation: candidateObservation}, &fakeIntegrationGit{}, forge,
		cleanupGit, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return &fixture{store: store, host: hostAdapter, git: cleanupGit, forge: forge, service: service, policy: cleanupPolicy}
}

func (fixture *fixture) admit(t *testing.T, trigger domaincleanup.Trigger, lifecycle domaincleanup.LifecycleState) Result {
	t.Helper()
	result, err := fixture.service.Admit(context.Background(), AdmitCommand{RequestID: "cleanup-request-1", RunID: fixture.store.run.ID,
		ExpectedRunVersion: fixture.store.run.Version, LeaseEpoch: 1, Trigger: trigger, LifecycleState: lifecycle,
		Policy: fixture.policy, OwnershipSHA256: strings.Repeat("7", 64), NowMillis: 2_000})
	if err != nil {
		t.Fatal(err)
	}
	return result
}

func (fixture *fixture) step(t *testing.T) (Result, error) {
	t.Helper()
	run, _ := fixture.store.Run(context.Background(), fixture.store.run.ID)
	return fixture.service.Step(context.Background(), StepCommand{RunID: run.ID, ExpectedRunVersion: run.Version, LeaseEpoch: 1, NowMillis: 2_000})
}

func (fixture *fixture) drive(t *testing.T) Result {
	t.Helper()
	var result Result
	for range 40 {
		var err error
		result, err = fixture.step(t)
		if err != nil && !errors.Is(err, ErrExternalUnavailable) && !strings.Contains(err.Error(), "response lost") {
			t.Fatalf("%v: %#v", err, fixture.store.run.Execution.Cleanup)
		}
		if result.Complete || result.Retained || result.Run.Execution.Cleanup.Phase == domaincleanup.PhaseNeedsYou {
			return result
		}
	}
	t.Fatal("cleanup did not terminate")
	return Result{}
}

func TestIntegratedCleanupUsesExactM49EvidenceAndDeterministicOrder(t *testing.T) {
	fixture := newFixture(t)
	fixture.admit(t, domaincleanup.TriggerIntegrated, domaincleanup.LifecycleActive)
	result := fixture.drive(t)
	if !result.Complete || !result.CleanupAuthorized || result.Run.Execution.CandidateAuthority.Downstream.Cleanup == nil {
		t.Fatalf("result = %#v", result)
	}
	if strings.Join(fixture.host.archives, ",") != "task-agent-1,task-workspace-1" {
		t.Fatalf("host order = %v", fixture.host.archives)
	}
	if fixture.git.dispatches[domaincleanup.ResourceSnapshot] != 0 || fixture.git.dispatches[domaincleanup.ResourceWorktree] != 1 ||
		fixture.git.dispatches[domaincleanup.ResourceRemoteRef] != 1 || fixture.git.dispatches[domaincleanup.ResourceLocalRef] != 1 {
		t.Fatalf("Git dispatches = %#v", fixture.git.dispatches)
	}
	if result.Run.Execution.Terminal {
		t.Fatal("cleanup decided Task closure")
	}
}

func TestAdmitRejectsMismatchedIntegrationGitHubCandidateOwnerAndLease(t *testing.T) {
	mutations := map[string]func(*fixture){
		"github": func(value *fixture) { value.forge.mismatch = true },
		"candidate": func(value *fixture) {
			value.store.run.Execution.CandidateAuthority.CandidateSHA = strings.Repeat("f", 40)
		},
		"owner":     func(value *fixture) { value.store.run.Execution.Agent.ExternalID = "" },
		"lease":     func(value *fixture) { value.store.project.Lease.Epoch++ },
		"workspace": func(value *fixture) { value.store.run.Execution.HostView.ExternalID = "" },
	}
	for name, mutate := range mutations {
		t.Run(name, func(t *testing.T) {
			fixture := newFixture(t)
			mutate(fixture)
			_, err := fixture.service.Admit(context.Background(), AdmitCommand{RequestID: "cleanup-request", RunID: fixture.store.run.ID,
				ExpectedRunVersion: fixture.store.run.Version, LeaseEpoch: 1, Trigger: domaincleanup.TriggerIntegrated,
				LifecycleState: domaincleanup.LifecycleActive, Policy: fixture.policy, OwnershipSHA256: strings.Repeat("7", 64), NowMillis: 2_000})
			if err == nil {
				t.Fatal("mismatch admitted")
			}
		})
	}
}

func TestCancellationPolicyRetainsOrSnapshotsWithoutRemoteDeletion(t *testing.T) {
	t.Run("retain", func(t *testing.T) {
		fixture := newFixture(t)
		fixture.store.run.Execution.Integration = nil
		fixture.store.run.Execution.CandidateAuthority.Downstream.Integration = nil
		fixture.store.run.Execution.Control.SchemaVersion = execution.RunControlSchemaVersion
		fixture.store.run.Execution.Control.Intent.Kind = execution.ControlCancelTask
		fixture.policy.CancellationMode = domaincleanup.CancellationRetain
		fixture.policy = domaincleanup.SealPolicy(fixture.policy)
		fixture.store.run.Execution.CleanupPolicy = &fixture.policy
		fixture.admit(t, domaincleanup.TriggerCancelled, domaincleanup.LifecycleRestored)
		result := fixture.drive(t)
		if !result.Retained || result.CleanupAuthorized || len(fixture.git.dispatches) != 0 {
			t.Fatalf("retain = %#v %#v", result, fixture.git.dispatches)
		}
	})
	t.Run("snapshot then delete", func(t *testing.T) {
		fixture := newFixture(t)
		fixture.store.run.Execution.Integration = nil
		fixture.store.run.Execution.CandidateAuthority.Downstream.Integration = nil
		fixture.store.run.Execution.NeedsYou = &execution.NeedsYou{Code: "provider_failed", WakeCondition: "cleanup", CleanupAuthorized: false}
		fixture.git.snapshotDirty = true
		fixture.admit(t, domaincleanup.TriggerFailed, domaincleanup.LifecycleActive)
		result := fixture.drive(t)
		if !result.Complete || !result.CleanupAuthorized || fixture.git.dispatches[domaincleanup.ResourceSnapshot] != 1 ||
			fixture.git.dispatches[domaincleanup.ResourceRemoteRef] != 0 || fixture.git.dispatches[domaincleanup.ResourceLocalRef] != 1 {
			t.Fatalf("snapshot = %#v %#v", result, fixture.git.dispatches)
		}
	})
}

func TestThirtyTwoCoordinatorsDispatchOneLogicalEffect(t *testing.T) {
	fixture := newFixture(t)
	fixture.admit(t, domaincleanup.TriggerIntegrated, domaincleanup.LifecycleActive)
	// First persist the agent observation.
	if _, err := fixture.step(t); err != nil {
		t.Fatal(err)
	}
	run, _ := fixture.store.Run(context.Background(), fixture.store.run.ID)
	const count = 32
	var progressed atomic.Uint64
	var wait sync.WaitGroup
	for range count {
		wait.Add(1)
		go func() {
			defer wait.Done()
			result, _ := fixture.service.Step(context.Background(), StepCommand{RunID: run.ID, ExpectedRunVersion: run.Version, LeaseEpoch: 1, NowMillis: 2_000})
			if result.Progressed {
				progressed.Add(1)
			}
		}()
	}
	wait.Wait()
	if progressed.Load() != 1 || len(fixture.host.archives) != 1 {
		t.Fatalf("progressed=%d archives=%v", progressed.Load(), fixture.host.archives)
	}
}

func TestResponseLossRestartAdoptsAbsenceWithoutSecondDelete(t *testing.T) {
	fixture := newFixture(t)
	fixture.admit(t, domaincleanup.TriggerIntegrated, domaincleanup.LifecycleActive)
	for {
		effect := fixture.store.run.Execution.Cleanup.Effects[domaincleanup.EffectIndex(*fixture.store.run.Execution.Cleanup, domaincleanup.ResourceRemoteRef)]
		if effect.Phase == domaincleanup.EffectIntent && effect.Observation != nil {
			break
		}
		if _, err := fixture.step(t); err != nil {
			t.Fatal(err)
		}
	}
	fixture.git.lose[domaincleanup.ResourceRemoteRef] = true
	_, err := fixture.step(t)
	if err == nil || fixture.git.dispatches[domaincleanup.ResourceRemoteRef] != 1 {
		t.Fatalf("lost dispatch = %v %#v", err, fixture.git.dispatches)
	}
	fixture.git.lose[domaincleanup.ResourceRemoteRef] = false
	// A new service simulates process restart over the same durable state.
	restarted, newErr := NewService(fixture.store, fixture.host, &fakeCandidate{observation: *fixture.store.run.Execution.CandidateObservation},
		&fakeIntegrationGit{}, fixture.forge, fixture.git, t.TempDir())
	if newErr != nil {
		t.Fatal(newErr)
	}
	fixture.service = restarted
	result := fixture.drive(t)
	if !result.Complete || fixture.git.dispatches[domaincleanup.ResourceRemoteRef] != 1 {
		t.Fatalf("restart = %#v %#v", result, fixture.git.dispatches)
	}
}

func TestSameSHARemoteRecreationParksWithBoardReasonAndNoAuthority(t *testing.T) {
	fixture := newFixture(t)
	fixture.admit(t, domaincleanup.TriggerIntegrated, domaincleanup.LifecycleActive)
	for {
		effect := fixture.store.run.Execution.Cleanup.Effects[domaincleanup.EffectIndex(*fixture.store.run.Execution.Cleanup, domaincleanup.ResourceRemoteRef)]
		if effect.Phase == domaincleanup.EffectIntent && effect.Observation != nil {
			break
		}
		if _, err := fixture.step(t); err != nil {
			t.Fatal(err)
		}
	}
	fixture.git.recreateRemote = true
	if _, err := fixture.step(t); err != nil {
		t.Fatal(err)
	}
	result, err := fixture.step(t)
	if err == nil || result.Run.Execution.Cleanup.Phase != domaincleanup.PhaseNeedsYou || result.CleanupAuthorized ||
		result.Run.Execution.NeedsYou == nil || result.Run.Execution.NeedsYou.CleanupAuthorized ||
		result.Run.Execution.NeedsYou.Code != execution.NeedCode(strings.ToLower(string(domaincleanup.CodeResponseUnknown))) {
		t.Fatalf("ambiguous recreation = %#v %v", result, err)
	}
	encoded, _ := json.Marshal(result.Run.Execution.NeedsYou)
	if strings.Contains(string(encoded), "/srv/") {
		t.Fatalf("Board/Organizer reason leaked a path: %s", encoded)
	}
}

func TestRestoredAndReclaimedLifecycleBindings(t *testing.T) {
	for _, lifecycle := range []domaincleanup.LifecycleState{domaincleanup.LifecycleRestored, domaincleanup.LifecycleReclaimed} {
		t.Run(string(lifecycle), func(t *testing.T) {
			fixture := newFixture(t)
			if lifecycle == domaincleanup.LifecycleReclaimed {
				fixture.host.agents["task-agent-1"], fixture.host.workspaces["task-workspace-1"] = false, false
				fixture.git.status[domaincleanup.ResourceSnapshot], fixture.git.status[domaincleanup.ResourceWorktree] = domaincleanup.StatusAbsent, domaincleanup.StatusAbsent
			}
			result := fixture.admit(t, domaincleanup.TriggerIntegrated, lifecycle)
			if result.Run.Execution.Cleanup.LifecycleState != lifecycle {
				t.Fatalf("binding = %#v", result.Run.Execution.Cleanup)
			}
		})
	}
}

func TestSevenDayExpiryOrdersPrivateArtifactBeforeRecoveryRefs(t *testing.T) {
	fixture := newFixture(t)
	fixture.store.run.Execution.Integration = nil
	fixture.store.run.Execution.CandidateAuthority.Downstream.Integration = nil
	fixture.store.run.Execution.NeedsYou = &execution.NeedsYou{Code: "provider_failed", WakeCondition: "cleanup", CleanupAuthorized: false}
	fixture.git.snapshotDirty, fixture.git.snapshotPrivate = true, true
	fixture.admit(t, domaincleanup.TriggerFailed, domaincleanup.LifecycleActive)
	completed := fixture.drive(t)
	if !completed.Complete || completed.Run.Execution.Cleanup.Snapshot == nil {
		t.Fatalf("cleanup = %#v", completed)
	}
	expires := completed.Run.Execution.Cleanup.Snapshot.RetentionUntilMillis
	fixture.store.project.Lease.ExpiresAtMillis = expires + 10_000
	run, _ := fixture.store.Run(context.Background(), completed.Run.ID)
	early, err := fixture.service.Expire(context.Background(), ExpireCommand{RunID: run.ID, ExpectedRunVersion: run.Version,
		LeaseEpoch: 1, NowMillis: expires - 1})
	if err != nil || !early.Retained || fixture.git.dispatches[domaincleanup.ResourcePrivateArtifact] != 0 ||
		fixture.git.dispatches[domaincleanup.ResourceRecoveryRef] != 0 {
		t.Fatalf("early expiry = %#v %v", early, err)
	}

	fixture.git.lose[domaincleanup.ResourcePrivateArtifact] = true
	for range 20 {
		run, _ = fixture.store.Run(context.Background(), completed.Run.ID)
		result, expireErr := fixture.service.Expire(context.Background(), ExpireCommand{RunID: run.ID, ExpectedRunVersion: run.Version,
			LeaseEpoch: 1, NowMillis: expires})
		if expireErr != nil && !strings.Contains(expireErr.Error(), "response lost") {
			t.Fatal(expireErr)
		}
		fixture.git.lose[domaincleanup.ResourcePrivateArtifact] = false
		if result.Run.Execution.Cleanup.RetentionComplete {
			if fixture.git.dispatches[domaincleanup.ResourcePrivateArtifact] != 1 || fixture.git.dispatches[domaincleanup.ResourceRecoveryRef] != 1 {
				t.Fatalf("expiry dispatches = %#v", fixture.git.dispatches)
			}
			return
		}
	}
	t.Fatal("retention expiry did not complete")
}
