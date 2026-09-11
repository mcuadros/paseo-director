// SPDX-License-Identifier: Apache-2.0

package correction

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/mcuadros/director-engine/domain"
	"github.com/mcuadros/director-engine/domain/agentprofile"
	"github.com/mcuadros/director-engine/domain/candidate"
	domainconfig "github.com/mcuadros/director-engine/domain/configuration"
	domaincorrection "github.com/mcuadros/director-engine/domain/correction"
	"github.com/mcuadros/director-engine/domain/execution"
	publicationdomain "github.com/mcuadros/director-engine/domain/publication"
	"github.com/mcuadros/director-engine/domain/runtimebudget"
	gitport "github.com/mcuadros/director-engine/ports/git"
	"github.com/mcuadros/director-engine/ports/host"
	correctionreducer "github.com/mcuadros/director-engine/reducer/correction"
)

const (
	primaryUUID    = "11111111-1111-4111-8111-111111111111"
	planDigest     = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	skillDigest    = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	decisionDigest = "cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc"
	diffDigest     = "dddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddd"
)

var errLostResponse = errors.New("fake response lost after applied effect")

type memoryStore struct {
	mu              sync.Mutex
	project         domain.Project
	task            domain.Task
	run             domain.Run
	candidates      []domain.Candidate
	commands        map[string]domain.CommandResult
	loseTransitions map[string]int
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
	return store.run, nil
}
func (store *memoryStore) Candidate(_ context.Context, id string) (domain.Candidate, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	for _, value := range store.candidates {
		if value.ID == id {
			return value, nil
		}
	}
	return domain.Candidate{}, errors.New("Candidate not found")
}
func (store *memoryStore) Candidates(context.Context, string) ([]domain.Candidate, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	return slices.Clone(store.candidates), nil
}

func (store *memoryStore) lose(transition string) bool {
	if store.loseTransitions[transition] == 0 {
		return false
	}
	store.loseTransitions[transition]--
	return true
}

func (store *memoryStore) UpdateRun(_ context.Context, command domain.CommandRequest, next domain.Run, event domain.Event) (domain.CommandResult, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	if prior, exists := store.commands[command.IdempotencyKey]; exists {
		prior.Replay = true
		return prior, nil
	}
	if command.ExpectedVersion != store.run.Version {
		result := domain.CommandResult{Outcome: domain.CommandRejectedVersionConflict, ObservedVersion: store.run.Version, EventID: event.ID}
		store.commands[command.IdempotencyKey] = result
		return result, nil
	}
	if next.Version != store.run.Version+1 || event.AggregateVersion != next.Version ||
		next.Execution.Correction == nil || !domaincorrection.ValidState(*next.Execution.Correction) || !runtimebudget.ValidLedger(next.Execution.Budget) {
		return domain.CommandResult{}, errors.New("invalid correction state write")
	}
	store.run = next
	result := domain.CommandResult{Outcome: domain.CommandApplied, ObservedVersion: next.Version, EventID: event.ID}
	store.commands[command.IdempotencyKey] = result
	if store.lose(command.Type) {
		return domain.CommandResult{}, errLostResponse
	}
	return result, nil
}

func (store *memoryStore) AppendCandidate(_ context.Context, command domain.CommandRequest, record domain.Candidate, event domain.Event) (domain.CommandResult, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	if prior, exists := store.commands[command.IdempotencyKey]; exists {
		prior.Replay = true
		return prior, nil
	}
	if command.ExpectedVersion != store.run.Version {
		result := domain.CommandResult{Outcome: domain.CommandRejectedVersionConflict, ObservedVersion: store.run.Version, EventID: event.ID}
		store.commands[command.IdempotencyKey] = result
		return result, nil
	}
	if record.Sequence != uint64(len(store.candidates)+1) || !candidate.ValidManifest(record.Manifest) || record.RunID != store.run.ID {
		return domain.CommandResult{}, errors.New("invalid corrected Candidate")
	}
	if store.run.Execution.Publication != nil && publicationdomain.DispatchInFlight(*store.run.Execution.Publication) {
		return domain.CommandResult{}, errors.New("publication dispatch must reconcile before Candidate replacement")
	}
	if store.run.Execution.CandidateAuthority != nil {
		prior := *store.run.Execution.CandidateAuthority
		store.run.Execution.CandidateAuthorityHistory = append(store.run.Execution.CandidateAuthorityHistory, candidate.HistoricalAuthority{
			Authority: prior, InvalidationCode: candidate.CodeCandidateChanged, InvalidatedByCandidateID: record.ID,
			InvalidatedByCandidateSHA: record.CommitSHA, InvalidatedAtMillis: record.AdmittedAtMillis,
		})
	}
	generation := uint64(len(store.run.Execution.CandidateAuthorityHistory))
	authority := candidate.NewAuthority(generation, record.ID, record.Claim.Branch, record.Claim.TaskVersion, record.Manifest)
	store.run.Execution.CandidateAuthority = &authority
	store.run.Execution.FindingContextSHA256 = record.Manifest.FindingsSHA256
	store.run.CurrentCandidateID = record.ID
	store.run.Version++
	store.candidates = append(store.candidates, record)
	result := domain.CommandResult{Outcome: domain.CommandApplied, ObservedVersion: store.run.Version, EventID: event.ID}
	store.commands[command.IdempotencyKey] = result
	if store.lose(command.Type) {
		return domain.CommandResult{}, errLostResponse
	}
	return result, nil
}

type fakeGit struct {
	mu           sync.Mutex
	observations int
	unavailable  bool
}

func (adapter *fakeGit) ObserveCandidate(_ context.Context, request gitport.CandidateRequest) (candidate.Observation, error) {
	adapter.mu.Lock()
	defer adapter.mu.Unlock()
	if adapter.unavailable {
		return candidate.Observation{}, errors.New("fake Git unavailable")
	}
	adapter.observations++
	claim := request.Claim
	return candidate.SealObservation(candidate.Observation{
		ClaimSHA256: candidate.ClaimSHA256(claim), RepositoryBindingSHA256: request.RepositoryBindingSHA256,
		ObservedAtMillis: request.TaskStoreNowMillis, MaximumAgeMillis: candidate.MaximumObservationAgeMS, ObjectFormat: "sha1",
		CommitSHA: claim.CandidateSHA, BaseSHA: claim.BaseSHA, ParentSHA: claim.BaseSHA, TreeSHA: strings.Repeat("3", 40),
		BranchHeadSHA: claim.CandidateSHA, BaseRefHeadSHA: claim.BaseSHA,
		DiffSHA256: strings.Repeat("4", 64), ChangedPathsSHA256: strings.Repeat("5", 64),
		SourceDevice: 1, SourceInode: 2, CommonDevice: 1, CommonInode: 3, WorktreeDevice: 1, WorktreeInode: 4,
		RepositoryExact: true, RemoteExact: true, PathsCanonical: true, RegistrationExact: true,
		ObjectPresent: true, ObjectStoreOwned: true, BranchStable: true, BaseStable: true, DescendsFromBase: true,
		DirectParent: true, WorktreeClean: true, IndexClean: true, UntrackedAbsent: true, IgnoredAbsent: true,
		SubmodulesClean: true, ConflictFree: true, IntentToAddAbsent: true, SparseCheckoutAbsent: true,
		FilesystemExact: true, SnapshotSHA256: strings.Repeat("6", 64), Code: candidate.CodeOK,
	}), nil
}

type fakeHost struct {
	mu           sync.Mutex
	cursor       uint64
	prompts      map[string]bool
	dispatches   int
	loseDispatch bool
	unavailable  bool
	ambiguous    bool
}

func newFakeHost() *fakeHost { return &fakeHost{prompts: map[string]bool{}} }
func (adapter *fakeHost) Describe(context.Context) (host.Descriptor, error) {
	if adapter.unavailable {
		return host.Descriptor{}, errors.New("provider unavailable")
	}
	definition, _ := host.EmbeddedDefinition()
	hash, _ := host.SchemaSHA256()
	return host.Descriptor{CredentialScope: definition.CredentialScope, ContractVersion: definition.ContractVersion, ContractHash: hash, Capabilities: definition.Capabilities}, nil
}
func (adapter *fakeHost) observation(command host.Command, status execution.ObservationStatus, usage bool) host.Observation {
	adapter.cursor++
	result := host.ObservationResult{EffectID: command.Arguments.EffectID, Status: status, BindingHash: command.Arguments.BindingHash,
		PriorDispatcherAbsent: status == execution.ObservationAbsent, MaximumAgeMillis: 30_000}
	if status != execution.ObservationAbsent {
		result.ExternalID = primaryUUID
	}
	if usage {
		result.Usage = &runtimebudget.ProviderUsage{State: runtimebudget.UsageCurrent, SourceRevision: "usage-" + command.Arguments.EffectID,
			InputTokensPresent: true, InputTokens: 10, OutputTokensPresent: true, OutputTokens: 10}
	}
	result.FactHash = host.ObservationResultHash(result)
	return host.Observation{RequestID: command.RequestID, Cursor: adapter.cursor,
		ObservedAt: time.UnixMilli(1_001).UTC().Format(time.RFC3339Nano), Result: result}
}
func (adapter *fakeHost) Invoke(_ context.Context, command host.Command) (host.Observation, error) {
	adapter.mu.Lock()
	defer adapter.mu.Unlock()
	if adapter.unavailable {
		return host.Observation{}, errors.New("provider unavailable")
	}
	if adapter.ambiguous {
		return adapter.observation(command, execution.ObservationAmbiguous, false), nil
	}
	switch command.Capability {
	case host.CapabilityAgentObserve:
		complete, present := adapter.prompts[command.Arguments.EffectID]
		if !present {
			return adapter.observation(command, execution.ObservationAbsent, false), nil
		}
		if !complete {
			return adapter.observation(command, execution.ObservationOwnedPresent, false), nil
		}
		return adapter.observation(command, execution.ObservationDesired, true), nil
	case host.CapabilityAgentPrompt:
		adapter.dispatches++
		adapter.prompts[command.Arguments.EffectID] = true
		observation := adapter.observation(command, execution.ObservationOwnedPresent, false)
		if adapter.loseDispatch {
			adapter.loseDispatch = false
			return host.Observation{}, errLostResponse
		}
		return observation, nil
	default:
		return host.Observation{}, errors.New("unexpected fake host effect")
	}
}

type fixture struct {
	store   *memoryStore
	host    *fakeHost
	git     *fakeGit
	service *Service
	ingest  IngestCommand
}

func testProfiles(t *testing.T) agentprofile.FrozenSet {
	t.Helper()
	selection := func(permission string, capabilities []domainconfig.MCPCapability) domainconfig.AgentSelection {
		return domainconfig.AgentSelection{Provider: domainconfig.ProviderCodex, Model: "gpt-5.4-mini", Effort: "high", Mode: "default",
			PermissionMode: permission, ProviderOptions: []domainconfig.ProviderOption{}, MCPCapabilities: capabilities}
	}
	organizer := selection("read-only", []domainconfig.MCPCapability{domainconfig.MCPProjectRead, domainconfig.MCPPlanningCommandSubmit})
	worker := selection("workspace-write", []domainconfig.MCPCapability{domainconfig.MCPTaskRead, domainconfig.MCPTaskOutcomeSubmit})
	reviewer := selection("read-only", []domainconfig.MCPCapability{domainconfig.MCPCandidateRead, domainconfig.MCPReviewVerdictSubmit})
	profiles := domainconfig.AgentProfiles{
		Organizer: domainconfig.AgentProfile{AgentSelection: organizer, FallbackChain: []domainconfig.AgentSelection{}},
		Worker:    domainconfig.AgentProfile{AgentSelection: worker, FallbackChain: []domainconfig.AgentSelection{}},
		Reviewer:  domainconfig.AgentProfile{AgentSelection: reviewer, FallbackChain: []domainconfig.AgentSelection{}},
	}
	variants := []agentprofile.VariantFact{
		{Effort: "high", Mode: "default", PermissionMode: "read-only", ProviderOptions: []domainconfig.ProviderOption{},
			MCPCapabilities: append(slices.Clone(organizer.MCPCapabilities), reviewer.MCPCapabilities...), SessionStdioMCP: true, ExactMCPToolPolicy: true, RuntimeProbePassed: true},
		{Effort: "high", Mode: "default", PermissionMode: "workspace-write", ProviderOptions: []domainconfig.ProviderOption{},
			MCPCapabilities: worker.MCPCapabilities, SessionStdioMCP: true, ExactMCPToolPolicy: true, RuntimeProbePassed: true},
	}
	discovery, err := agentprofile.SealDiscovery(agentprofile.DiscoverySnapshot{SchemaVersion: agentprofile.DiscoverySchemaVersion,
		PaseoVersion: agentprofile.SupportedPaseoVersion, ObservedAtMillis: 1_000, MaximumAgeMillis: 30_000,
		Providers: []agentprofile.ProviderFact{{Provider: domainconfig.ProviderCodex, CLIVersion: "0.147.0", State: agentprofile.ProviderReady,
			DiagnosticCodes: []agentprofile.DiagnosticCode{}, Models: []agentprofile.ModelFact{{Model: worker.Model, Variants: variants}}}}})
	if err != nil {
		t.Fatal(err)
	}
	set, err := agentprofile.Freeze(agentprofile.FreezeRequest{Profiles: profiles, OrganizerRevision: strings.Repeat("9", 40),
		ExpectedOrganizerRevision: strings.Repeat("9", 40), ConfigurationSHA256: strings.Repeat("8", 64), Discovery: discovery,
		ExpectedDiscoveryRevision: discovery.Revision, NowMillis: 1_001})
	if err != nil {
		t.Fatal(err)
	}
	return set
}

func sealInitialCandidate(t *testing.T, task domain.Task, runID, baseSHA, candidateSHA, repositoryHash string, profiles agentprofile.FrozenSet) domain.Candidate {
	t.Helper()
	claim := candidate.Claim{SchemaVersion: candidate.ClaimSchemaVersion, ID: "initial-claim", ProjectID: task.ProjectID,
		WorkspaceID: task.WorkspaceIDs[0], TaskID: task.ID, RunID: runID, ActorID: primaryUUID, WorktreeID: "worktree-1",
		Branch: "task/dir-m4.4-correction-cycles", BaseRef: "refs/heads/main", CandidateSHA: candidateSHA, BaseSHA: baseSHA,
		LeaseEpoch: 1, ExpectedRunVersion: 1, TaskVersion: task.Version,
		AcceptanceSHA256: candidate.AcceptanceSHA256(task.ID, task.Version, task.Objective, task.AcceptanceCriteria,
			[]string{"criterion-ci", "criterion-coverage", "criterion-human", "criterion-review", "criterion-validation"}),
		ConfigurationSHA256: profiles.ConfigurationSHA256(), ProfileSHA256: profiles.SHA256(), ContextSHA256: strings.Repeat("7", 64),
		DecisionsSHA256: decisionDigest, FindingsSHA256: candidate.EmptyContextSHA256()}
	observation := candidate.SealObservation(candidate.Observation{ClaimSHA256: candidate.ClaimSHA256(claim), RepositoryBindingSHA256: repositoryHash,
		ObservedAtMillis: 1_000, MaximumAgeMillis: candidate.MaximumObservationAgeMS, ObjectFormat: "sha1", CommitSHA: candidateSHA,
		BaseSHA: baseSHA, ParentSHA: baseSHA, TreeSHA: strings.Repeat("3", 40), BranchHeadSHA: candidateSHA, BaseRefHeadSHA: baseSHA,
		DiffSHA256: strings.Repeat("4", 64), ChangedPathsSHA256: strings.Repeat("5", 64), SourceDevice: 1, SourceInode: 2,
		CommonDevice: 1, CommonInode: 3, WorktreeDevice: 1, WorktreeInode: 4, RepositoryExact: true, RemoteExact: true,
		PathsCanonical: true, RegistrationExact: true, ObjectPresent: true, ObjectStoreOwned: true, BranchStable: true,
		BaseStable: true, DescendsFromBase: true, DirectParent: true, WorktreeClean: true, IndexClean: true,
		UntrackedAbsent: true, IgnoredAbsent: true, SubmodulesClean: true, ConflictFree: true, IntentToAddAbsent: true,
		SparseCheckoutAbsent: true, FilesystemExact: true, SnapshotSHA256: strings.Repeat("6", 64), Code: candidate.CodeOK})
	decision := candidate.Evaluate(claim, observation, repositoryHash, 1_001)
	if decision.Manifest == nil {
		t.Fatal("initial Candidate did not admit")
	}
	return domain.Candidate{SchemaVersion: domain.CandidateSchemaVersion, ID: candidate.RecordID(runID, 1, candidateSHA), RunID: runID,
		Sequence: 1, CommitSHA: candidateSHA, Claim: claim, Manifest: *decision.Manifest, AdmittedAtMillis: 1_001}
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	criteria := []string{"criterion-ci", "criterion-coverage", "criterion-human", "criterion-review", "criterion-validation"}
	task := domain.Task{ID: "dir-m4.4", ProjectID: "project-1", Key: "DIR-M4.4", Title: "Implement review and CI correction cycles",
		Objective: "Implement bounded correction cycles.", AcceptanceCriteria: "Fresh exact gates follow every changed Candidate.",
		WorkspaceIDs: []string{"workspace-1"}, Version: 4}
	profiles := testProfiles(t)
	scope := execution.Scope{ProjectID: task.ProjectID, WorkspaceID: task.WorkspaceIDs[0], TaskID: task.ID, RunID: "run-1"}
	server := execution.MCPServerLaunch{Name: "director-session-mcp", Command: "/usr/bin/director-agent-runtime", Args: []string{"serve"}, Env: map[string]string{}}
	primary, err := execution.NewPrimarySession(scope, "effect-task-agent", profiles, server)
	if err != nil {
		t.Fatal(err)
	}
	primary, err = execution.BindPrimarySession(primary, primaryUUID)
	if err != nil {
		t.Fatal(err)
	}
	policy := runtimebudget.NewPolicy("budget-r1", 100_000, 100_000, 32, 0, 4)
	ledger, err := runtimebudget.NewLedger(policy, 1_000)
	if err != nil {
		t.Fatal(err)
	}
	baseSHA, headSHA, repositoryHash := strings.Repeat("0", 40), strings.Repeat("1", 40), strings.Repeat("e", 64)
	record := sealInitialCandidate(t, task, scope.RunID, baseSHA, headSHA, repositoryHash, profiles)
	authority := candidate.NewAuthority(0, record.ID, record.Claim.Branch, task.Version, record.Manifest)
	bound := func(id string) *candidate.EvidenceBinding {
		return &candidate.EvidenceBinding{ID: id, CandidateID: record.ID, CandidateSHA: headSHA,
			BaseSHA: baseSHA, Generation: authority.Generation, BindingSHA256: authority.BindingSHA256}
	}
	authority.Downstream = candidate.Downstream{Validation: bound("validation-old"), Review: bound("review-old"), CI: bound("ci-old"),
		Publication: bound("publication-old"), Feedback: bound("feedback-old"), Ready: bound("ready-old"), Integration: bound("integration-old")}
	registeredAt := time.UnixMilli(1_000).UTC().Format(time.RFC3339Nano)
	run := domain.Run{ID: scope.RunID, TaskID: task.ID, Number: 1, BaseSHA: baseSHA, CurrentCandidateID: record.ID, Version: 1,
		Execution: execution.State{SchemaVersion: execution.SchemaVersion, Scope: scope, SourcePath: "/source", WorktreePath: "/worktree",
			Branch: record.Claim.Branch, BaseRef: record.Claim.BaseRef, TaskTitle: task.Title, RootWorkspaceID: "root-workspace",
			CriterionIDs: criteria, LeaseBinding: execution.LeaseBinding{HolderInstance: "engine-1", HolderProcessIdentity: "process-1", Epoch: 1},
			RepositoryBindingHash: repositoryHash, Worktree: execution.Effect{ID: "worktree-effect", Kind: execution.EffectWorktreeCreate, Phase: execution.EffectComplete, ExternalID: "worktree-1"},
			HostView: execution.Effect{ID: "workspace-effect", Kind: execution.EffectHostViewCreate, Phase: execution.EffectComplete, ExternalID: "execution-workspace"},
			Boundary: execution.Effect{ID: "boundary-effect", Kind: execution.EffectBoundaryMaterialize, Phase: execution.EffectComplete, ExternalID: "boundary-1"},
			Agent:    execution.Effect{ID: "effect-task-agent", Kind: execution.EffectAgentCreate, Phase: execution.EffectComplete, ExternalID: primaryUUID},
			WorkerVisibility: &execution.WorkerVisibility{RootWorkspaceID: "root-workspace", ExecutionWorkspaceID: "execution-workspace", AgentID: primaryUUID,
				Role: string(host.WorkerRoleTaskAgent), Phase: host.WorkerPhaseBuilding, BaseSHA: baseSHA, EffectID: "effect-task-agent",
				ProfileSHA256: profiles.SHA256(), SessionSHA256: primary.ReservationSHA256, RegisteredAt: registeredAt, StartedAt: registeredAt,
				Digest: strings.Repeat("f", 64)},
			EffectiveProfiles: &profiles, EffectiveProfilesSHA256: profiles.SHA256(), PrimarySession: primary,
			PreparationReady: true, PreparationBarrierHash: strings.Repeat("7", 64), AcceptanceSHA256: record.Manifest.AcceptanceSHA256,
			DecisionContextSHA256: decisionDigest, FindingContextSHA256: record.Manifest.FindingsSHA256, CandidateAuthority: &authority,
			OperationalObservation: &execution.OperationalObservation{ID: "operational-1"},
			Budget:                 ledger, TurnBudgetDemand: runtimebudget.Demand{WallTimeMilliseconds: 100, Tokens: 100, Turns: 1}}}
	project := domain.Project{ID: task.ProjectID, State: "active", Version: 1, Lease: &domain.ProjectLease{HolderInstance: "engine-1",
		HolderProcessIdentity: "process-1", Epoch: 1, AcquiredAtMillis: 1_000, RenewedAtMillis: 1_000, ExpiresAtMillis: 100_000, DispatchAllowed: true}}
	store := &memoryStore{project: project, task: task, run: run, candidates: []domain.Candidate{record}, commands: map[string]domain.CommandResult{}, loseTransitions: map[string]int{}}
	hostPort, git := newFakeHost(), &fakeGit{}
	service, err := NewService(store, git, hostPort)
	if err != nil {
		t.Fatal(err)
	}
	inputs := []domaincorrection.FindingInput{
		{ID: "review-1", Source: domaincorrection.SourceReview, Class: "CORRECTNESS", Severity: domaincorrection.SeverityP1,
			CandidateID: record.ID, CandidateSHA: headSHA, Summary: "Review found a blocking defect", Evidence: []domaincorrection.Evidence{{ID: "review-evidence", Kind: domaincorrection.EvidenceReviewFinding, SHA256: strings.Repeat("1", 64)}}, AcceptanceCoverage: []string{"criterion-review"}, Blocking: true},
		{ID: "validation-1", Source: domaincorrection.SourceValidation, Class: "TEST_FAILURE", Severity: domaincorrection.SeverityP1,
			CandidateID: record.ID, CandidateSHA: headSHA, Summary: "Focused validation failed", Evidence: []domaincorrection.Evidence{{ID: "validation-evidence", Kind: domaincorrection.EvidenceValidationObservation, SHA256: strings.Repeat("2", 64)}}, AcceptanceCoverage: []string{"criterion-validation"}, Blocking: true},
		{ID: "ci-1", Source: domaincorrection.SourceCI, Class: "LINUX_CI_FAILURE", Severity: domaincorrection.SeverityP1,
			CandidateID: record.ID, CandidateSHA: headSHA, Summary: "Authoritative Linux CI failed", Evidence: []domaincorrection.Evidence{{ID: "ci-evidence", Kind: domaincorrection.EvidenceCIObservation, SHA256: strings.Repeat("3", 64)}}, AcceptanceCoverage: []string{"criterion-ci"}, Blocking: true},
		{ID: "human-1", Source: domaincorrection.SourceHuman, Class: "ACTIONABLE_FEEDBACK", Severity: domaincorrection.SeverityP3,
			CandidateID: record.ID, CandidateSHA: headSHA, Summary: "Human feedback requests the same bounded correction", Evidence: []domaincorrection.Evidence{{ID: "human-evidence", Kind: domaincorrection.EvidenceHumanFeedback, SHA256: strings.Repeat("4", 64)}}, AcceptanceCoverage: []string{}, Blocking: false},
	}
	ingest := IngestCommand{RunID: run.ID, ExpectedRunVersion: run.Version, LeaseEpoch: 1, OriginalTaskAgentUUID: primaryUUID,
		CriterionIDs: criteria, FrozenPlanDigest: planDigest, CurrentPlanDigest: planDigest, FrozenSkillSetDigest: skillDigest,
		CurrentSkillSetDigest: skillDigest, CurrentDecisionDigest: decisionDigest, CurrentDiffDigest: diffDigest,
		Policy: domaincorrection.Policy{AutoFixCIFailures: true, AutoFixReviewFeedback: true, AttemptLimit: 3},
		SourceSnapshots: []domaincorrection.SourceSnapshot{{Source: domaincorrection.SourceReview, Revision: strings.Repeat("1", 64), Count: 1},
			{Source: domaincorrection.SourceValidation, Revision: strings.Repeat("2", 64), Count: 1}, {Source: domaincorrection.SourceCI, Revision: strings.Repeat("3", 64), Count: 1},
			{Source: domaincorrection.SourceHuman, Revision: strings.Repeat("4", 64), Count: 1}}, Findings: inputs, NowMillis: 1_001}
	return &fixture{store: store, host: hostPort, git: git, service: service, ingest: ingest}
}

func dispatchingPublication(t *testing.T, run domain.Run, record domain.Candidate) publicationdomain.State {
	t.Helper()
	policy := publicationdomain.NewPolicy("pull_request", true, nil)
	binding := publicationdomain.SealBinding(publicationdomain.Binding{TaskID: run.TaskID, RunID: run.ID,
		CandidateID: record.ID, CandidateSHA: record.CommitSHA, BaseSHA: record.Manifest.BaseSHA, TreeSHA: record.Manifest.TreeSHA,
		ManifestSHA256: record.Manifest.BindingSHA256, CandidateGeneration: run.Execution.CandidateAuthority.Generation,
		TaskVersion: run.Execution.CandidateAuthority.TaskVersion, Branch: record.Claim.Branch, BaseRef: record.Claim.BaseRef,
		RepositoryBindingSHA256: record.Manifest.RepositoryBindingSHA256, CanonicalRemote: "https://github.com/example/correction",
		GitHubRepositoryID: 123, GitHubRepositoryNodeID: "R_correction", RepositoryOwner: "example",
		RepositoryName: "correction", HeadOwner: "example", OwnershipSHA256: strings.Repeat("a", 64), PolicySHA256: policy.SHA256})
	template, ok := publicationdomain.RenderTemplate(binding, policy, "Correction publication", "pending", "", "pending", "", []string{})
	if !ok {
		t.Fatal("render publication fixture")
	}
	publication, ok := publicationdomain.NewState(binding, policy, template, "", nil)
	if !ok {
		t.Fatal("create publication fixture")
	}
	publication.Push.Phase = publicationdomain.EffectDispatching
	publication.Push.Attempts = 1
	if !publicationdomain.ValidState(publication) {
		t.Fatal("dispatching publication fixture is invalid")
	}
	return publication
}

func transition(run domain.Run) TransitionCommand {
	return TransitionCommand{RunID: run.ID, ExpectedRunVersion: run.Version, LeaseEpoch: 1, NowMillis: 1_001}
}

func ingestAndPrompt(t *testing.T, fixture *fixture) domain.Run {
	t.Helper()
	result, err := fixture.service.Ingest(context.Background(), fixture.ingest)
	if err != nil {
		t.Fatalf("ingest: %v", err)
	}
	result, err = fixture.service.AdmitAttempt(context.Background(), transition(result.Run))
	if err != nil {
		t.Fatalf("admit attempt: %v", err)
	}
	result, err = fixture.service.ReconcilePrompt(context.Background(), transition(result.Run))
	if err != nil && !errors.Is(err, errLostResponse) {
		t.Fatalf("dispatch prompt: %v", err)
	}
	current, _ := fixture.store.Run(context.Background(), result.Run.ID)
	result, err = fixture.service.ReconcilePrompt(context.Background(), transition(current))
	if err != nil {
		t.Fatalf("observe prompt: %v", err)
	}
	if result.Run.Execution.Correction.Phase != domaincorrection.PhaseAwaitingOutput {
		t.Fatalf("phase = %s", result.Run.Execution.Correction.Phase)
	}
	return result.Run
}

func changedOutput(state domaincorrection.State, sha string, productive bool) domaincorrection.Output {
	batch, _ := domaincorrection.CurrentBatch(state)
	attempt := state.Attempts[len(state.Attempts)-1]
	resolved := []string{}
	coverage := slices.Clone(batch.AcceptanceCoverage)
	if productive {
		resolved = []string{domaincorrection.FindingIDs(batch)[0]}
		coverage = append(coverage, "criterion-coverage")
		slices.Sort(coverage)
	}
	return domaincorrection.Output{SchemaVersion: domaincorrection.OutputSchemaVersion, AttemptKey: attempt.Key,
		AgentUUID: primaryUUID, BatchSHA256: batch.SHA256, BatchedFindingIDs: domaincorrection.FindingIDs(batch),
		CandidateSHA: sha, ResolvedFindingIDs: resolved, AcceptanceCoverage: coverage}
}

func completeCorrection(t *testing.T, fixture *fixture, sha string, productive bool) domain.Run {
	t.Helper()
	run := ingestAndPrompt(t, fixture)
	result, err := fixture.service.SubmitOutput(context.Background(), OutputCommand{TransitionCommand: transition(run), Output: changedOutput(*run.Execution.Correction, sha, productive)})
	if err != nil {
		t.Fatal(err)
	}
	result, err = fixture.service.ObserveCandidate(context.Background(), transition(result.Run))
	if err != nil {
		t.Fatal(err)
	}
	result, err = fixture.service.AdmitCandidate(context.Background(), transition(result.Run))
	if err != nil {
		t.Fatal(err)
	}
	return result.Run
}

func TestCompleteBatchChangedCandidateInvalidatesAllAuthorityAndRequiresFreshGates(t *testing.T) {
	fixture := newFixture(t)
	original := *fixture.store.run.Execution.CandidateAuthority
	run := completeCorrection(t, fixture, strings.Repeat("2", 40), true)
	state := run.Execution.Correction
	if state.Phase != domaincorrection.PhaseGatesRequired || len(state.Gates) != 1 || len(run.Execution.CandidateAuthorityHistory) != 1 ||
		!candidate.DownstreamEmpty(run.Execution.CandidateAuthority.Downstream) || run.Execution.FindingContextSHA256 != state.Batches[0].SHA256 ||
		len(fixture.store.candidates) != 2 || fixture.host.dispatches != 1 {
		t.Fatalf("corrected Run = %#v", run)
	}
	history := run.Execution.CandidateAuthorityHistory[0]
	if history.Authority != original || !candidate.ValidHistoricalAuthority(history) || !state.Gates[0].FreshCIRequired ||
		!state.Gates[0].FreshReviewRequired || !state.Gates[0].PriorAuthorityInvalidated ||
		state.Gates[0].CandidateSHA != strings.Repeat("2", 40) || state.Gates[0].CIKey == "" || state.Gates[0].ReviewBindingKey == "" {
		t.Fatalf("historical authority or gates = %#v / %#v", history, state.Gates[0])
	}
}

func TestCorrectedCandidateWaitsForInFlightPublicationObservation(t *testing.T) {
	fixture := newFixture(t)
	publication := dispatchingPublication(t, fixture.store.run, fixture.store.candidates[0])
	fixture.store.run.Execution.PublicationPolicy, fixture.store.run.Execution.Publication = &publication.Policy, &publication
	run := ingestAndPrompt(t, fixture)
	result, err := fixture.service.SubmitOutput(context.Background(), OutputCommand{TransitionCommand: transition(run),
		Output: changedOutput(*run.Execution.Correction, strings.Repeat("2", 40), true)})
	if err != nil {
		t.Fatal(err)
	}
	result, err = fixture.service.ObserveCandidate(context.Background(), transition(result.Run))
	if err != nil {
		t.Fatal(err)
	}
	_, err = fixture.service.AdmitCandidate(context.Background(), transition(result.Run))
	if err == nil || len(fixture.store.candidates) != 1 || fixture.store.run.CurrentCandidateID != fixture.store.candidates[0].ID ||
		len(fixture.store.run.Execution.CandidateAuthorityHistory) != 0 {
		t.Fatalf("in-flight publication replacement = %#v, %v", fixture.store.run, err)
	}
}

func TestUnchangedSHARejectsOnceThenRepeatedAcknowledgementEscalatesWithoutCandidateOrGates(t *testing.T) {
	fixture := newFixture(t)
	run := ingestAndPrompt(t, fixture)
	output := changedOutput(*run.Execution.Correction, fixture.store.candidates[0].CommitSHA, false)
	result, err := fixture.service.SubmitOutput(context.Background(), OutputCommand{TransitionCommand: transition(run), Output: output})
	if err != nil || result.Run.Execution.Correction.AcknowledgementRejections != 1 || result.Run.Execution.Correction.Phase != domaincorrection.PhaseReady {
		t.Fatalf("first unchanged = %#v, %v", result, err)
	}
	result, err = fixture.service.AdmitAttempt(context.Background(), transition(result.Run))
	if err != nil {
		t.Fatal(err)
	}
	result, err = fixture.service.ReconcilePrompt(context.Background(), transition(result.Run))
	if err != nil {
		t.Fatal(err)
	}
	result, err = fixture.service.ReconcilePrompt(context.Background(), transition(result.Run))
	if err != nil {
		t.Fatal(err)
	}
	output = changedOutput(*result.Run.Execution.Correction, fixture.store.candidates[0].CommitSHA, false)
	output.AcknowledgementOnly, output.CandidateSHA = true, ""
	result, err = fixture.service.SubmitOutput(context.Background(), OutputCommand{TransitionCommand: transition(result.Run), Output: output})
	if err != nil || result.Code != correctionreducer.CodeAcknowledgementRepeated || result.Run.Execution.NeedsYou == nil ||
		len(fixture.store.candidates) != 1 || len(result.Run.Execution.Correction.Gates) != 0 || fixture.host.dispatches != 2 {
		t.Fatalf("repeat acknowledgement = %#v, %v", result, err)
	}
}

func TestP2HumanDecisionAndProviderOrBudgetFailureStopBeforePrompt(t *testing.T) {
	for name, expectation := range map[string]struct {
		mutate func(*fixture)
		code   string
	}{
		"P2 decision": {func(f *fixture) {
			f.ingest.Findings[0].Severity = domaincorrection.SeverityP2
			f.ingest.Findings[0].Blocking = false
			f.ingest.Findings[0].RequiresHumanDecision = true
		}, correctionreducer.CodeP2DecisionRequired},
		"provider unavailable": {func(f *fixture) { f.store.run.Execution.Budget.TelemetryState = runtimebudget.UsageUnavailable }, correctionreducer.CodeProviderUnavailable},
		"turn exhausted": {func(f *fixture) {
			policy := runtimebudget.NewPolicy("budget-r1", 100_000, 100_000, 1, 0, 4)
			ledger, _ := runtimebudget.NewLedger(policy, 1_000)
			f.store.run.Execution.Budget = ledger
		}, string(runtimebudget.ReasonTurnsHard)},
	} {
		t.Run(name, func(t *testing.T) {
			fixture := newFixture(t)
			expectation.mutate(fixture)
			result, err := fixture.service.Ingest(context.Background(), fixture.ingest)
			if err != nil {
				t.Fatal(err)
			}
			if result.Run.Execution.NeedsYou == nil {
				result, err = fixture.service.AdmitAttempt(context.Background(), transition(result.Run))
				if err != nil {
					t.Fatal(err)
				}
			}
			if result.Run.Execution.NeedsYou == nil || string(result.Run.Execution.NeedsYou.Code) != expectation.code || fixture.host.dispatches != 0 {
				t.Fatalf("result = %#v", result)
			}
		})
	}
}

func TestChurnRepeatingRootCauseStopsAndProductiveCoverageDeltaIsRecorded(t *testing.T) {
	productive := newFixture(t)
	run := completeCorrection(t, productive, strings.Repeat("2", 40), true)
	attempt := run.Execution.Correction.Attempts[0]
	if attempt.Classification != domaincorrection.ClassificationProductive || !slices.Equal(attempt.CoverageDelta.Added, []string{"criterion-coverage"}) {
		t.Fatalf("productive attempt = %#v", attempt)
	}

	churn := newFixture(t)
	run = completeCorrection(t, churn, strings.Repeat("2", 40), false)
	command := rebindIngest(churn.ingest, run, "r2", "CORRECTNESS")
	result, err := churn.service.Ingest(context.Background(), command)
	if err != nil {
		t.Fatal(err)
	}
	result, err = churn.service.AdmitAttempt(context.Background(), transition(result.Run))
	if err != nil || result.Code != correctionreducer.CodeRootCauseRepeated || result.Run.Execution.NeedsYou == nil || churn.host.dispatches != 1 {
		t.Fatalf("repeated root = %#v, %v", result, err)
	}
}

func rebindIngest(command IngestCommand, run domain.Run, revision, firstClass string) IngestCommand {
	command.ExpectedRunVersion = run.Version
	command.NowMillis = 1_001
	command.CurrentDiffDigest = diffDigest
	command.SourceSnapshots = slices.Clone(command.SourceSnapshots)
	for index := range command.SourceSnapshots {
		command.SourceSnapshots[index].Revision = digestForTest(revision + string(command.SourceSnapshots[index].Source))
	}
	command.Findings = slices.Clone(command.Findings)
	for index := range command.Findings {
		command.Findings[index].CandidateID = run.CurrentCandidateID
		command.Findings[index].CandidateSHA = run.Execution.CandidateAuthority.CandidateSHA
		command.Findings[index].Evidence = slices.Clone(command.Findings[index].Evidence)
		command.Findings[index].Evidence[0].SHA256 = digestForTest(revision + command.Findings[index].ID)
	}
	command.Findings[0].Class = firstClass
	return command
}

func digestForTest(value string) string {
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:])
}

func TestExactlyThreeAttemptsAndNewBlockingClassAfterCap(t *testing.T) {
	fixture := newFixture(t)
	run := completeCorrection(t, fixture, strings.Repeat("2", 40), true)
	for number, sha := range []string{strings.Repeat("3", 40), strings.Repeat("4", 40)} {
		command := rebindIngest(fixture.ingest, run, "cycle-"+string(rune('2'+number)), "CLASS_"+string(rune('B'+number)))
		result, err := fixture.service.Ingest(context.Background(), command)
		if err != nil {
			t.Fatal(err)
		}
		result, err = fixture.service.AdmitAttempt(context.Background(), transition(result.Run))
		if err != nil {
			t.Fatal(err)
		}
		result, err = fixture.service.ReconcilePrompt(context.Background(), transition(result.Run))
		if err != nil {
			t.Fatal(err)
		}
		result, err = fixture.service.ReconcilePrompt(context.Background(), transition(result.Run))
		if err != nil {
			t.Fatal(err)
		}
		output := changedOutput(*result.Run.Execution.Correction, sha, true)
		result, err = fixture.service.SubmitOutput(context.Background(), OutputCommand{TransitionCommand: transition(result.Run), Output: output})
		if err != nil {
			t.Fatal(err)
		}
		result, err = fixture.service.ObserveCandidate(context.Background(), transition(result.Run))
		if err != nil {
			t.Fatal(err)
		}
		result, err = fixture.service.AdmitCandidate(context.Background(), transition(result.Run))
		if err != nil {
			t.Fatal(err)
		}
		run = result.Run
	}
	if len(run.Execution.Correction.Attempts) != 3 || len(run.Execution.Correction.Gates) != 3 || fixture.host.dispatches != 3 {
		t.Fatalf("three-attempt state = %#v", run.Execution.Correction)
	}
	command := rebindIngest(fixture.ingest, run, "cycle-4", "NEW_BLOCKING_ROOT")
	result, err := fixture.service.Ingest(context.Background(), command)
	if err != nil {
		t.Fatal(err)
	}
	result, err = fixture.service.AdmitAttempt(context.Background(), transition(result.Run))
	if err != nil || result.Code != correctionreducer.CodeNewBlockingClassAfterCap || result.Run.Execution.NeedsYou == nil || fixture.host.dispatches != 3 {
		t.Fatalf("fourth attempt = %#v, %v", result, err)
	}
}

func TestResponseLossAtEveryCorrectionFrontierRecoversWithoutDuplicatePromptOrCandidate(t *testing.T) {
	fixture := newFixture(t)
	fixture.store.loseTransitions["correction.batch_ingested"] = 1
	_, err := fixture.service.Ingest(context.Background(), fixture.ingest)
	if !errors.Is(err, errLostResponse) {
		t.Fatalf("batch loss = %v", err)
	}
	run, _ := fixture.store.Run(context.Background(), "run-1")
	fixture.ingest.ExpectedRunVersion = run.Version
	replay, err := fixture.service.Ingest(context.Background(), fixture.ingest)
	if err != nil || !replay.Replayed {
		t.Fatalf("batch replay = %#v, %v", replay, err)
	}

	fixture.store.loseTransitions["correction.prompt_intent_recorded"] = 1
	_, err = fixture.service.AdmitAttempt(context.Background(), transition(run))
	if !errors.Is(err, errLostResponse) {
		t.Fatalf("intent loss = %v", err)
	}
	run, _ = fixture.store.Run(context.Background(), "run-1")
	fixture.host.loseDispatch = true
	_, err = fixture.service.ReconcilePrompt(context.Background(), transition(run))
	if !errors.Is(err, errLostResponse) {
		t.Fatalf("dispatch loss = %v", err)
	}
	run, _ = fixture.store.Run(context.Background(), "run-1")
	fixture.store.loseTransitions["correction.prompt_observed"] = 1
	_, err = fixture.service.ReconcilePrompt(context.Background(), transition(run))
	if !errors.Is(err, errLostResponse) {
		t.Fatalf("observation loss = %v", err)
	}
	run, _ = fixture.store.Run(context.Background(), "run-1")
	fixture.store.loseTransitions["correction.output_recorded"] = 1
	_, err = fixture.service.SubmitOutput(context.Background(), OutputCommand{TransitionCommand: transition(run), Output: changedOutput(*run.Execution.Correction, strings.Repeat("2", 40), true)})
	if !errors.Is(err, errLostResponse) {
		t.Fatalf("output loss = %v", err)
	}
	run, _ = fixture.store.Run(context.Background(), "run-1")
	fixture.store.loseTransitions["correction.candidate_observed"] = 1
	_, err = fixture.service.ObserveCandidate(context.Background(), transition(run))
	if !errors.Is(err, errLostResponse) {
		t.Fatalf("Candidate observation loss = %v", err)
	}
	run, _ = fixture.store.Run(context.Background(), "run-1")
	fixture.store.loseTransitions["candidate.correction.admit"] = 1
	_, err = fixture.service.AdmitCandidate(context.Background(), transition(run))
	if !errors.Is(err, errLostResponse) {
		t.Fatalf("Candidate append loss = %v", err)
	}
	run, _ = fixture.store.Run(context.Background(), "run-1")
	fixture.store.loseTransitions["correction.candidate_gates_required"] = 1
	_, err = fixture.service.AdmitCandidate(context.Background(), transition(run))
	if !errors.Is(err, errLostResponse) {
		t.Fatalf("gate projection loss = %v", err)
	}
	run, _ = fixture.store.Run(context.Background(), "run-1")
	service, _ := NewService(fixture.store, fixture.git, fixture.host)
	result, err := service.Reconcile(context.Background(), transition(run))
	if err != nil || result.Run.Execution.Correction.Phase != domaincorrection.PhaseGatesRequired || fixture.host.dispatches != 1 || len(fixture.store.candidates) != 2 {
		t.Fatalf("recovered = %#v, %v", result, err)
	}
}

func TestThirtyTwoCoordinatorsFencePromptDispatchAndStaleLeaseVersion(t *testing.T) {
	fixture := newFixture(t)
	result, err := fixture.service.Ingest(context.Background(), fixture.ingest)
	if err != nil {
		t.Fatal(err)
	}
	result, err = fixture.service.AdmitAttempt(context.Background(), transition(result.Run))
	if err != nil {
		t.Fatal(err)
	}
	command := transition(result.Run)
	var successful atomic.Uint32
	var wait sync.WaitGroup
	for range 32 {
		wait.Add(1)
		go func() {
			defer wait.Done()
			service, _ := NewService(fixture.store, fixture.git, fixture.host)
			if _, callErr := service.ReconcilePrompt(context.Background(), command); callErr == nil {
				successful.Add(1)
			}
		}()
	}
	wait.Wait()
	if successful.Load() != 1 || fixture.host.dispatches != 1 {
		t.Fatalf("successful=%d dispatches=%d", successful.Load(), fixture.host.dispatches)
	}
	run, _ := fixture.store.Run(context.Background(), "run-1")
	stale := transition(run)
	stale.ExpectedRunVersion--
	if _, err := fixture.service.ReconcilePrompt(context.Background(), stale); !errors.Is(err, ErrConcurrentTransition) {
		t.Fatalf("stale version = %v", err)
	}
	stale = transition(run)
	stale.LeaseEpoch++
	if _, err := fixture.service.ReconcilePrompt(context.Background(), stale); !errors.Is(err, ErrConcurrentTransition) {
		t.Fatalf("stale lease = %v", err)
	}
}

func TestPoisonedPrimarySessionNeverCreatesReplacementOrRedispatch(t *testing.T) {
	fixture := newFixture(t)
	result, err := fixture.service.Ingest(context.Background(), fixture.ingest)
	if err != nil {
		t.Fatal(err)
	}
	result, err = fixture.service.AdmitAttempt(context.Background(), transition(result.Run))
	if err != nil {
		t.Fatal(err)
	}
	fixture.store.mu.Lock()
	fixture.store.run.Execution.PrimaryRecovery = execution.PrimaryRecovery{SchemaVersion: execution.PrimaryRecoverySchemaVersion, Phase: execution.PrimaryRecoveryIntentRecorded}
	fixture.store.mu.Unlock()
	run, _ := fixture.store.Run(context.Background(), "run-1")
	result, err = fixture.service.ReconcilePrompt(context.Background(), transition(run))
	if err != nil || result.Run.Execution.NeedsYou == nil || string(result.Run.Execution.NeedsYou.Code) != correctionreducer.CodeOriginalAgentChanged || fixture.host.dispatches != 0 {
		t.Fatalf("poisoned session = %#v, %v", result, err)
	}
}

func TestDeterministicGateKeysAndRuntimeBudgetRefuseSecondCIForSameCandidate(t *testing.T) {
	fixture := newFixture(t)
	run := completeCorrection(t, fixture, strings.Repeat("2", 40), true)
	gate := run.Execution.Correction.Gates[0]
	ciKey, reviewKey := domaincorrection.GateKeys(gate.CandidateID, gate.CandidateSHA, gate.CandidateGeneration)
	if gate.CIKey != ciKey || gate.ReviewBindingKey != reviewKey {
		t.Fatalf("gate keys = %#v", gate)
	}
	request := runtimebudget.ReserveRequest{ID: "ci-reservation-1", EffectID: gate.CIKey, Activity: runtimebudget.ActivityValidationCycle,
		LeaseEpoch: 1, PolicyRevision: run.Execution.Budget.Policy.Revision, Demand: runtimebudget.Demand{}, CandidateSHA: gate.CandidateSHA}
	ledger, decision, err := runtimebudget.Reserve(run.Execution.Budget, request, 1_001)
	if err != nil || decision.Disposition != runtimebudget.DispositionAllow {
		t.Fatalf("first CI reserve = %#v, %v", decision, err)
	}
	observation := runtimebudget.ActivityObservation{ID: "ci-evidence-1", ReservationID: request.ID, EffectID: request.EffectID,
		Activity: request.Activity, LeaseEpoch: 1, PolicyRevision: request.PolicyRevision, ObservedAtMillis: 1_001, CandidateSHA: request.CandidateSHA}
	observation.FactHash = runtimebudget.ActivityObservationHash(observation)
	ledger, _, err = runtimebudget.ApplyActivityObservation(ledger, observation)
	if err != nil {
		t.Fatal(err)
	}
	request.ID, request.EffectID = "ci-reservation-2", "another-complete-ci"
	_, decision, err = runtimebudget.Reserve(ledger, request, 1_001)
	if err != nil || decision.Reason != runtimebudget.ReasonRepeatedValidation {
		t.Fatalf("second CI = %#v, %v", decision, err)
	}
}

func TestFixtureContainsNoRawHistoryCredentialOrDeliveryEffect(t *testing.T) {
	fixture := newFixture(t)
	encoded, err := json.Marshal(fixture.ingest)
	if err != nil {
		t.Fatal(err)
	}
	text := strings.ToLower(string(encoded))
	for _, forbidden := range []string{"conversation", "archived history", "password", "credential", "github_pat_", "ghp_", "sk-"} {
		if strings.Contains(text, forbidden) {
			t.Fatalf("fixture contains forbidden %q", forbidden)
		}
	}
}
