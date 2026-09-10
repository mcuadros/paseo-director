// SPDX-License-Identifier: Apache-2.0

package review

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/mcuadros/director-engine/domain"
	"github.com/mcuadros/director-engine/domain/agentbridge"
	"github.com/mcuadros/director-engine/domain/agentprofile"
	candidatedomain "github.com/mcuadros/director-engine/domain/candidate"
	domainconfig "github.com/mcuadros/director-engine/domain/configuration"
	"github.com/mcuadros/director-engine/domain/execution"
	domainreview "github.com/mcuadros/director-engine/domain/review"
	"github.com/mcuadros/director-engine/ports/host"
	reviewport "github.com/mcuadros/director-engine/ports/review"
)

const (
	testTaskAgentUUID   = "11111111-1111-4111-8111-111111111111"
	testCoordinatorUUID = "22222222-2222-4222-8222-222222222222"
	testOwnerUUID       = "33333333-3333-4333-8333-333333333333"
	testReviewerUUID    = "44444444-4444-4444-8444-444444444444"
)

var errLostReviewResponse = errors.New("fake review response lost after handoff")

func testDigest(value any) string {
	encoded, _ := json.Marshal(value)
	digest := sha256.Sum256(encoded)
	return hex.EncodeToString(digest[:])
}

type memoryStore struct {
	mu         sync.Mutex
	project    domain.Project
	task       domain.Task
	run        domain.Run
	candidate  domain.Candidate
	commands   map[string]domain.CommandResult
	writeCount int
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

func (store *memoryStore) Candidate(context.Context, string) (domain.Candidate, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	return store.candidate, nil
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
	if next.Version != store.run.Version+1 || event.AggregateVersion != next.Version ||
		next.Execution.Review == nil || !domainreview.ValidState(*next.Execution.Review) {
		return domain.CommandResult{}, errors.New("invalid review state write")
	}
	store.run = next
	store.writeCount++
	result := domain.CommandResult{Outcome: domain.CommandApplied, ObservedVersion: next.Version, EventID: event.ID}
	store.commands[command.IdempotencyKey] = result
	return result, nil
}

type lossyCheckout struct {
	inner      reviewport.CheckoutPort
	loseCreate bool
	loseRemove bool
	creates    int
	removes    int
}

type realTestCheckout struct{}

func (realTestCheckout) ObserveCheckout(_ context.Context, request reviewport.CheckoutRequest) (reviewport.CheckoutObservation, error) {
	if _, err := os.Stat(request.CheckoutPath); os.IsNotExist(err) {
		return reviewport.CheckoutObservation{Status: reviewport.CheckoutAbsent}, nil
	} else if err != nil {
		return reviewport.CheckoutObservation{Status: reviewport.CheckoutAmbiguous}, err
	}
	head, headErr := testGit(request.CheckoutPath, "rev-parse", "HEAD")
	tree, treeErr := testGit(request.CheckoutPath, "show", "-s", "--format=%T", "HEAD")
	branch, branchErr := testGit(request.CheckoutPath, "branch", "--show-current")
	status, statusErr := testGit(request.CheckoutPath, "status", "--porcelain=v2", "--untracked-files=all")
	primaryHead, primaryErr := testGit(request.PrimaryPath, "rev-parse", "HEAD")
	primaryStatus, primaryStatusErr := testGit(request.PrimaryPath, "status", "--porcelain=v2", "--untracked-files=all")
	if headErr != nil || treeErr != nil || branchErr != nil || statusErr != nil || primaryErr != nil || primaryStatusErr != nil {
		return reviewport.CheckoutObservation{Status: reviewport.CheckoutAmbiguous}, nil
	}
	evidence := domainreview.SealCheckoutEvidence(domainreview.CheckoutEvidence{
		CheckoutID: request.ReviewKey, OwnerUUID: request.OwnerUUID, CandidateSHA: head, TreeSHA: tree,
		Detached: branch == "", Clean: status == "", PrimaryDistinct: request.CheckoutPath != request.PrimaryPath,
		SourceUnchanged: primaryHead == request.PrimaryHeadSHA && primaryStatus == "",
	})
	if evidence.OwnerUUID != request.OwnerUUID || evidence.CandidateSHA != request.CandidateSHA ||
		evidence.TreeSHA != request.TreeSHA || !evidence.Detached || !evidence.Clean ||
		!evidence.PrimaryDistinct || !evidence.SourceUnchanged ||
		evidence.FactSHA256 != domainreview.CheckoutEvidenceSHA256(evidence) {
		return reviewport.CheckoutObservation{Status: reviewport.CheckoutDifferent, Evidence: evidence}, nil
	}
	return reviewport.CheckoutObservation{Status: reviewport.CheckoutExact, Evidence: evidence}, nil
}

func (realTestCheckout) CreateCheckout(_ context.Context, request reviewport.CheckoutRequest) error {
	if err := os.MkdirAll(request.ReviewerRoot, 0o700); err != nil {
		return err
	}
	_, err := testGit(request.SourcePath, "worktree", "add", "--detach", request.CheckoutPath, request.CandidateSHA)
	return err
}

func (realTestCheckout) RemoveCheckout(_ context.Context, request reviewport.CheckoutRequest, _ domainreview.CheckoutEvidence) error {
	_, err := testGit(request.SourcePath, "worktree", "remove", request.CheckoutPath)
	return err
}

func (adapter *lossyCheckout) ObserveCheckout(ctx context.Context, request reviewport.CheckoutRequest) (reviewport.CheckoutObservation, error) {
	return adapter.inner.ObserveCheckout(ctx, request)
}

func (adapter *lossyCheckout) CreateCheckout(ctx context.Context, request reviewport.CheckoutRequest) error {
	if err := adapter.inner.CreateCheckout(ctx, request); err != nil {
		return err
	}
	adapter.creates++
	if adapter.loseCreate {
		adapter.loseCreate = false
		return errLostReviewResponse
	}
	return nil
}

func (adapter *lossyCheckout) RemoveCheckout(ctx context.Context, request reviewport.CheckoutRequest, evidence domainreview.CheckoutEvidence) error {
	if err := adapter.inner.RemoveCheckout(ctx, request, evidence); err != nil {
		return err
	}
	adapter.removes++
	if adapter.loseRemove {
		adapter.loseRemove = false
		return errLostReviewResponse
	}
	return nil
}

type fakeReviewHost struct {
	mu                   sync.Mutex
	cursor               uint64
	workspaceID          string
	workspaceActive      bool
	workspaceArchived    bool
	reviewerPresent      bool
	reviewerArchived     bool
	bootstrapComplete    bool
	promptPresent        bool
	promptComplete       bool
	duplicateReviewers   bool
	observedReviewerUUID string
	createCommand        host.Command
	promptCommand        host.Command
	lastObservation      host.Observation
	mutationCount        map[execution.EffectKind]int
	loseResponses        bool
}

func newFakeReviewHost() *fakeReviewHost {
	return &fakeReviewHost{workspaceID: "wks_review_candidate", observedReviewerUUID: testReviewerUUID, mutationCount: map[execution.EffectKind]int{}}
}

func (adapter *fakeReviewHost) Describe(context.Context) (host.Descriptor, error) {
	definition, _ := host.EmbeddedDefinition()
	hash, _ := host.SchemaSHA256()
	return host.Descriptor{CredentialScope: definition.CredentialScope, ContractVersion: definition.ContractVersion, ContractHash: hash, Capabilities: definition.Capabilities}, nil
}

func (adapter *fakeReviewHost) observation(command host.Command, status execution.ObservationStatus, externalID, correlation string) host.Observation {
	adapter.cursor++
	result := host.ObservationResult{
		EffectID: command.Arguments.EffectID, Status: status, ExternalID: externalID,
		BindingHash: command.Arguments.BindingHash, CorrelationHash: correlation,
		PriorDispatcherAbsent: true, MaximumAgeMillis: 30_000,
	}
	result.FactHash = host.ObservationResultHash(result)
	observation := host.Observation{
		RequestID: command.RequestID, Cursor: adapter.cursor,
		ObservedAt: time.UnixMilli(1_001).UTC().Format(time.RFC3339Nano), Result: result,
	}
	adapter.lastObservation = observation
	return observation
}

func (adapter *fakeReviewHost) mutation(command host.Command) (host.Observation, error) {
	adapter.mutationCount[command.Arguments.EffectKind]++
	if adapter.loseResponses {
		return host.Observation{}, errLostReviewResponse
	}
	return adapter.observation(command, execution.ObservationDesired, "", ""), nil
}

func (adapter *fakeReviewHost) Invoke(_ context.Context, command host.Command) (host.Observation, error) {
	adapter.mu.Lock()
	defer adapter.mu.Unlock()
	value := command.Arguments
	switch command.Capability {
	case host.CapabilityWorkspaceObserve:
		if value.EffectKind == execution.EffectHostViewArchive {
			if adapter.workspaceArchived {
				return adapter.observation(command, execution.ObservationDesired, adapter.workspaceID, ""), nil
			}
			if adapter.workspaceActive {
				return adapter.observation(command, execution.ObservationOwnedPresent, adapter.workspaceID, ""), nil
			}
			return adapter.observation(command, execution.ObservationAmbiguous, "", ""), nil
		}
		if adapter.workspaceActive {
			return adapter.observation(command, execution.ObservationDesired, adapter.workspaceID, ""), nil
		}
		return adapter.observation(command, execution.ObservationAbsent, "", ""), nil
	case host.CapabilityWorkspaceCreate:
		adapter.workspaceActive = true
		return adapter.mutation(command)
	case host.CapabilityAgentObserve:
		switch value.EffectKind {
		case execution.EffectReviewerAgentCreate:
			if adapter.duplicateReviewers {
				return adapter.observation(command, execution.ObservationAmbiguous, "", ""), nil
			}
			if !adapter.reviewerPresent {
				return adapter.observation(command, execution.ObservationAbsent, "", ""), nil
			}
			registration, err := host.ParseWorkerLabels(adapter.createCommand.Arguments.Labels)
			if err != nil {
				return host.Observation{}, err
			}
			correlation, _ := host.RegistrationDigest(registration)
			status := execution.ObservationOwnedPresent
			if adapter.bootstrapComplete {
				status = execution.ObservationDesired
			}
			return adapter.observation(command, status, adapter.observedReviewerUUID, correlation), nil
		case execution.EffectAgentPrompt:
			if !adapter.promptPresent {
				return adapter.observation(command, execution.ObservationAbsent, "", ""), nil
			}
			status := execution.ObservationOwnedPresent
			if adapter.promptComplete {
				status = execution.ObservationDesired
			}
			return adapter.observation(command, status, adapter.observedReviewerUUID, ""), nil
		case execution.EffectReviewerAgentArchive:
			status := execution.ObservationOwnedPresent
			if adapter.reviewerArchived {
				status = execution.ObservationDesired
			}
			return adapter.observation(command, status, adapter.observedReviewerUUID, ""), nil
		}
	case host.CapabilityReviewerAgentCreate:
		if _, err := host.AdmitAgentCreateLabels(command); err != nil || !adapter.workspaceActive || adapter.reviewerPresent {
			return host.Observation{}, errors.New("fake Reviewer create rejected")
		}
		adapter.createCommand = command
		adapter.reviewerPresent = true
		adapter.bootstrapComplete = true
		return adapter.mutation(command)
	case host.CapabilityAgentPrompt:
		registration, err := host.ParseWorkerLabels(adapter.createCommand.Arguments.Labels)
		if err != nil || host.AdmitAgentPrompt(command, registration, adapter.observedReviewerUUID) != nil || adapter.promptPresent {
			return host.Observation{}, errors.New("fake Review prompt rejected")
		}
		adapter.promptPresent = true
		adapter.promptComplete = true
		adapter.promptCommand = command
		return adapter.mutation(command)
	case host.CapabilityAgentArchive:
		if value.AgentID != adapter.observedReviewerUUID || value.EffectKind != execution.EffectReviewerAgentArchive {
			return host.Observation{}, errors.New("fake Reviewer archive identity mismatch")
		}
		adapter.reviewerArchived = true
		return adapter.mutation(command)
	case host.CapabilityWorkspaceArchive:
		if value.WorkspaceID != adapter.workspaceID || !adapter.reviewerArchived {
			return host.Observation{}, errors.New("fake Reviewer workspace archive ordering mismatch")
		}
		adapter.workspaceActive = false
		adapter.workspaceArchived = true
		return adapter.mutation(command)
	}
	return host.Observation{}, errors.New("fake Review host capability unsupported")
}

type serviceFixture struct {
	store    *memoryStore
	host     *fakeReviewHost
	checkout *lossyCheckout
	service  *Service
	command  AdmitCommand
}

func runTestGit(t *testing.T, directory string, arguments ...string) string {
	t.Helper()
	output, err := testGit(directory, arguments...)
	if err != nil {
		t.Fatalf("git %v: %v", arguments, err)
	}
	return output
}

func testGit(directory string, arguments ...string) (string, error) {
	command := exec.Command("git", arguments...)
	command.Dir = directory
	command.Env = append(os.Environ(), "GIT_CONFIG_NOSYSTEM=1")
	output, err := command.CombinedOutput()
	if err != nil {
		return "", errors.New("disposable review Git fixture failed")
	}
	return strings.TrimSpace(string(output)), nil
}

func testProfiles(t *testing.T) agentprofile.FrozenSet {
	t.Helper()
	organizer := domainconfig.AgentSelection{
		Provider: domainconfig.ProviderCodex, Model: "gpt-5.4-mini", Effort: "high", Mode: "default", PermissionMode: "read-only",
		ProviderOptions: []domainconfig.ProviderOption{}, MCPCapabilities: []domainconfig.MCPCapability{domainconfig.MCPProjectRead, domainconfig.MCPPlanningCommandSubmit},
	}
	worker := domainconfig.AgentSelection{
		Provider: domainconfig.ProviderCodex, Model: "gpt-5.4-mini", Effort: "high", Mode: "default", PermissionMode: "workspace-write",
		ProviderOptions: []domainconfig.ProviderOption{}, MCPCapabilities: []domainconfig.MCPCapability{domainconfig.MCPTaskRead, domainconfig.MCPTaskOutcomeSubmit},
	}
	reviewer := domainconfig.AgentSelection{
		Provider: domainconfig.ProviderCodex, Model: "gpt-5.4-mini", Effort: "high", Mode: "default", PermissionMode: "read-only",
		ProviderOptions: []domainconfig.ProviderOption{}, MCPCapabilities: []domainconfig.MCPCapability{domainconfig.MCPCandidateRead, domainconfig.MCPReviewVerdictSubmit},
	}
	profiles := domainconfig.AgentProfiles{
		Organizer: domainconfig.AgentProfile{AgentSelection: organizer, FallbackChain: []domainconfig.AgentSelection{}},
		Worker:    domainconfig.AgentProfile{AgentSelection: worker, FallbackChain: []domainconfig.AgentSelection{}},
		Reviewer:  domainconfig.AgentProfile{AgentSelection: reviewer, FallbackChain: []domainconfig.AgentSelection{}},
	}
	variants := []agentprofile.VariantFact{
		{Effort: organizer.Effort, Mode: organizer.Mode, PermissionMode: organizer.PermissionMode, ProviderOptions: []domainconfig.ProviderOption{}, MCPCapabilities: append(append([]domainconfig.MCPCapability{}, organizer.MCPCapabilities...), reviewer.MCPCapabilities...), SessionStdioMCP: true, ExactMCPToolPolicy: true, RuntimeProbePassed: true},
		{Effort: worker.Effort, Mode: worker.Mode, PermissionMode: worker.PermissionMode, ProviderOptions: []domainconfig.ProviderOption{}, MCPCapabilities: worker.MCPCapabilities, SessionStdioMCP: true, ExactMCPToolPolicy: true, RuntimeProbePassed: true},
	}
	discovery, err := agentprofile.SealDiscovery(agentprofile.DiscoverySnapshot{
		SchemaVersion: agentprofile.DiscoverySchemaVersion, PaseoVersion: agentprofile.SupportedPaseoVersion,
		ObservedAtMillis: 1_000, MaximumAgeMillis: 30_000,
		Providers: []agentprofile.ProviderFact{{Provider: domainconfig.ProviderCodex, CLIVersion: "0.147.0", State: agentprofile.ProviderReady,
			DiagnosticCodes: []agentprofile.DiagnosticCode{}, Models: []agentprofile.ModelFact{{Model: worker.Model, Variants: variants}}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	profilesSet, err := agentprofile.Freeze(agentprofile.FreezeRequest{
		Profiles: profiles, OrganizerRevision: strings.Repeat("9", 40), ExpectedOrganizerRevision: strings.Repeat("9", 40),
		ConfigurationSHA256: strings.Repeat("8", 64), Discovery: discovery,
		ExpectedDiscoveryRevision: discovery.Revision, NowMillis: 1_001,
	})
	if err != nil {
		t.Fatal(err)
	}
	return profilesSet
}

func newServiceFixture(t *testing.T, loseResponses bool) *serviceFixture {
	t.Helper()
	root := t.TempDir()
	source := filepath.Join(root, "source")
	if err := os.Mkdir(source, 0o700); err != nil {
		t.Fatal(err)
	}
	runTestGit(t, source, "init", "-b", "main")
	runTestGit(t, source, "config", "user.name", "Review Fixture")
	runTestGit(t, source, "config", "user.email", "review@example.invalid")
	if err := os.WriteFile(filepath.Join(source, "source.go"), []byte("package fixture\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	runTestGit(t, source, "add", "source.go")
	runTestGit(t, source, "commit", "-m", "base")
	base := runTestGit(t, source, "rev-parse", "HEAD")
	if err := os.WriteFile(filepath.Join(source, "source.go"), []byte("package fixture\n\nconst Candidate = true\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	runTestGit(t, source, "add", "source.go")
	runTestGit(t, source, "commit", "-m", "candidate")
	candidateSHA := runTestGit(t, source, "rev-parse", "HEAD")
	treeSHA := runTestGit(t, source, "show", "-s", "--format=%T", "HEAD")

	task := domain.Task{ID: "dir-m4.3", ProjectID: "project-1", Key: "DIR-M4.3", Title: "Implement mandatory detached independent review", Objective: "Implement the exact review contract.", AcceptanceCriteria: "Review is detached and independent.", WorkspaceIDs: []string{"repository-1"}, Version: 3}
	profiles := testProfiles(t)
	claim := candidatedomain.Claim{
		SchemaVersion: candidatedomain.ClaimSchemaVersion, ID: "claim-review-candidate", ProjectID: task.ProjectID,
		WorkspaceID: "repository-1", TaskID: task.ID, RunID: "run-review-1", ActorID: testTaskAgentUUID,
		WorktreeID: "task-worktree", Branch: "task/dir-m4.3-independent-review", BaseRef: "refs/heads/main",
		CandidateSHA: candidateSHA, BaseSHA: base, LeaseEpoch: 1, ExpectedRunVersion: 1, TaskVersion: task.Version,
		AcceptanceSHA256: strings.Repeat("1", 64), ConfigurationSHA256: profiles.ConfigurationSHA256(),
		ProfileSHA256: profiles.SHA256(), ContextSHA256: strings.Repeat("2", 64), DecisionsSHA256: strings.Repeat("3", 64), FindingsSHA256: strings.Repeat("4", 64),
	}
	observation := candidatedomain.SealObservation(candidatedomain.Observation{
		ClaimSHA256: candidatedomain.ClaimSHA256(claim), RepositoryBindingSHA256: strings.Repeat("5", 64),
		ObservedAtMillis: 1_000, MaximumAgeMillis: candidatedomain.MaximumObservationAgeMS, ObjectFormat: "sha1",
		CommitSHA: candidateSHA, BaseSHA: base, ParentSHA: base, TreeSHA: treeSHA, BranchHeadSHA: candidateSHA, BaseRefHeadSHA: base,
		DiffSHA256: strings.Repeat("6", 64), ChangedPathsSHA256: strings.Repeat("7", 64),
		SourceDevice: 1, SourceInode: 2, CommonDevice: 1, CommonInode: 3, WorktreeDevice: 1, WorktreeInode: 4,
		RepositoryExact: true, RemoteExact: true, PathsCanonical: true, RegistrationExact: true, ObjectPresent: true,
		ObjectStoreOwned: true, BranchStable: true, BaseStable: true, DescendsFromBase: true, DirectParent: true,
		WorktreeClean: true, IndexClean: true, UntrackedAbsent: true, IgnoredAbsent: true, SubmodulesClean: true,
		ConflictFree: true, IntentToAddAbsent: true, SparseCheckoutAbsent: true, FilesystemExact: true,
		SnapshotSHA256: strings.Repeat("a", 64), Code: candidatedomain.CodeOK,
	})
	decision := candidatedomain.Evaluate(claim, observation, strings.Repeat("5", 64), 1_001)
	if decision.Manifest == nil {
		t.Fatal("Candidate manifest was not admitted")
	}
	record := domain.Candidate{SchemaVersion: domain.CandidateSchemaVersion, ID: "candidate-review-1", RunID: claim.RunID,
		Sequence: 1, CommitSHA: candidateSHA, Claim: claim, Manifest: *decision.Manifest, AdmittedAtMillis: 1_001}
	scope := execution.Scope{ProjectID: task.ProjectID, WorkspaceID: "repository-1", TaskID: task.ID, RunID: claim.RunID}
	server := execution.MCPServerLaunch{Name: "director-session-mcp", Command: "/usr/bin/director-agent-runtime", Args: []string{"serve"}, Env: map[string]string{}}
	primary, err := execution.NewPrimarySession(scope, "effect-task-agent", profiles, server)
	if err != nil {
		t.Fatal(err)
	}
	primary, err = execution.BindPrimarySession(primary, testTaskAgentUUID)
	if err != nil {
		t.Fatal(err)
	}
	isolation := execution.IsolationObservation{Observed: true, Runtime: "rootless-oci", Rootless: true, ReadOnlyRootFilesystem: true,
		CapabilitiesDropped: true, NoNewPrivileges: true, PrivateNetworkNamespace: true, RuntimeSocketsAbsent: true,
		ControlToolsAbsent: true, OwnedWorktreeOnly: true, FixedStdioMCP: true}
	isolationAdmission := execution.AdmitIsolation(isolation)
	policy := execution.OperationalPolicy{MinimumFreeDiskBasisPoints: 1_000, MaximumWorktreeBytes: 1 << 20, MaximumProcesses: 16,
		MaximumMemoryBytes: 1 << 30, MaximumElapsedMilliseconds: 60_000, MaximumOutputBytes: 1 << 20,
		MaximumTemporaryBytes: 1 << 20, MaximumObservationAgeMillis: 30_000}
	operational := execution.OperationalObservation{ID: "operational-review", ObservedAtMillis: 1_000,
		FreeDiskBasisPoints: execution.Measurement{Present: true, Value: 5_000}, WorktreeBytes: execution.Measurement{Present: true, Value: 1_000},
		Processes: execution.Measurement{Present: true, Value: 1}, MemoryBytes: execution.Measurement{Present: true, Value: 1_000},
		ElapsedMilliseconds: execution.Measurement{Present: true, Value: 10}, OutputBytes: execution.Measurement{Present: true, Value: 10},
		TemporaryBytes: execution.Measurement{Present: true, Value: 10}}
	authority := candidatedomain.NewAuthority(0, record.ID, claim.Branch, task.Version, record.Manifest)
	run := domain.Run{ID: claim.RunID, TaskID: task.ID, Number: 1, BaseSHA: base, CurrentCandidateID: record.ID, Version: 5,
		Execution: execution.State{SchemaVersion: execution.SchemaVersion, Scope: scope, SourcePath: source, WorktreePath: source,
			LeaseBinding:    execution.LeaseBinding{HolderInstance: "engine-review", HolderProcessIdentity: "process-review", Epoch: 1},
			RootWorkspaceID: "wks_root_project", LifecycleDigest: execution.LifecycleDigest(execution.LifecycleSurfaces{}),
			IsolationDigest: isolationAdmission.Digest, Isolation: isolation, OperationalPolicy: policy, OperationalObservation: &operational,
			PreparationReady: true, PreparationBarrierHash: strings.Repeat("b", 64),
			Boundary:          execution.Effect{ID: "boundary", Kind: execution.EffectBoundaryMaterialize, Phase: execution.EffectComplete, ExternalID: "boundary-review"},
			Agent:             execution.Effect{ID: "effect-task-agent", Kind: execution.EffectAgentCreate, Phase: execution.EffectComplete, ExternalID: testTaskAgentUUID},
			EffectiveProfiles: &profiles, EffectiveProfilesSHA256: profiles.SHA256(), PrimarySession: primary,
			CandidateAuthority: &authority, DecisionContextSHA256: record.Manifest.DecisionsSHA256, FindingContextSHA256: record.Manifest.FindingsSHA256}}
	store := &memoryStore{project: domain.Project{ID: task.ProjectID, State: "active", Version: 1, Lease: &domain.ProjectLease{
		HolderInstance: "engine-review", HolderProcessIdentity: "process-review", Epoch: 1,
		AcquiredAtMillis: 100, RenewedAtMillis: 100, ExpiresAtMillis: 100_000, DispatchAllowed: true,
	}}, task: task, run: run, candidate: record, commands: map[string]domain.CommandResult{}}
	hostPort := newFakeReviewHost()
	hostPort.loseResponses = loseResponses
	checkout := &lossyCheckout{inner: realTestCheckout{}, loseCreate: loseResponses, loseRemove: loseResponses}
	service, err := NewService(store, checkout, hostPort)
	if err != nil {
		t.Fatal(err)
	}
	reviewerRoot := filepath.Join(root, "reviewers")
	return &serviceFixture{store: store, host: hostPort, checkout: checkout, service: service, command: AdmitCommand{
		RunID: run.ID, ExpectedRunVersion: run.Version, CoordinatorUUID: testCoordinatorUUID, ReviewOwnerUUID: testOwnerUUID,
		CISlotID: "remote-ci-slot-1", ReviewerRoot: reviewerRoot, CheckoutPath: filepath.Join(reviewerRoot, "candidate-review-1"),
		CriterionIDs: []string{"criterion-review-independent", "criterion-review-detached"},
		ProbePlan:    []domainreview.Probe{{ID: "git-status", Kind: domainreview.ProbeGitInspect, Argv: []string{"git", "status", "--short"}, TimeoutSeconds: 30}}, NowMillis: 1_001,
	}}
}

func reviewCI(state domainreview.State, id string) domainreview.CIObservation {
	return domainreview.SealCIObservation(domainreview.CIObservation{ID: id, SlotID: state.Binding.CISlotID,
		CandidateSHA: state.Binding.CandidateSHA, BaseSHA: state.Binding.BaseSHA, TreeSHA: state.Binding.TreeSHA,
		ManifestSHA256: state.Binding.ManifestSHA256, WorkflowRunID: "34500000123", RequiredChecks: []string{"maintained-linux-ci"},
		Status: "passed", Authoritative: true, Complete: true, ObservedAtMillis: 1_002})
}

func reviewClaim(state domainreview.State) domainreview.Claim {
	matrix := make([]domainreview.MatrixResult, len(state.Matrix))
	for index, entry := range state.Matrix {
		matrix[index] = domainreview.MatrixResult{ID: entry.ID, Kind: entry.Kind, Status: "covered"}
	}
	return domainreview.Claim{SchemaVersion: domainreview.ClaimSchemaVersion, ReviewKey: state.ReviewKey,
		AttemptKey: domainreview.AttemptKey(state.Binding, domainreview.AttemptInitial), AttemptReason: domainreview.AttemptInitial,
		Binding: state.Binding, ReviewerUUID: state.ReviewerUUID, HarnessReviewerUUID: state.ReviewerUUID,
		Checkout: *state.CheckoutEvidence, CIObservation: *state.CIObservation, Verdict: domainreview.VerdictApproveCandidate,
		Matrix: matrix, Findings: []domainreview.Finding{}, ResidualRiskCodes: []string{}, HarnessVersion: 2,
		Setup:  domainreview.SetupObservation{Mode: domainreview.SetupRemoteOnly, GitAvailable: true, DependencyState: domainreview.DependencyMissing},
		Probes: []domainreview.ProbeResult{{ID: "git-status", Status: "passed", ExitCode: 0, OutputSHA256: strings.Repeat("c", 64)}}}
}

func reconcileUntil(t *testing.T, fixture *serviceFixture, condition func(domainreview.State) bool) {
	t.Helper()
	for range 40 {
		run, _ := fixture.store.Run(context.Background(), fixture.command.RunID)
		if run.Execution.Review != nil && condition(*run.Execution.Review) {
			return
		}
		restarted, restartErr := NewService(fixture.store, fixture.checkout, fixture.host)
		if restartErr != nil {
			t.Fatal(restartErr)
		}
		fixture.service = restarted
		_, err := restarted.Reconcile(context.Background(), fixture.command.RunID, testCoordinatorUUID, 1_003)
		if err != nil && !errors.Is(err, errLostReviewResponse) {
			current, _ := fixture.store.Run(context.Background(), fixture.command.RunID)
			t.Fatalf("Reconcile() error = %v; host observation = %#v; Review state = %#v", err, fixture.host.lastObservation, current.Execution.Review)
		}
	}
	t.Fatal("review reconciliation did not converge")
}

func cleanupUntil(t *testing.T, fixture *serviceFixture) {
	t.Helper()
	for range 30 {
		run, _ := fixture.store.Run(context.Background(), fixture.command.RunID)
		if run.Execution.Review.CheckoutCleanup.Phase == domainreview.EffectComplete {
			return
		}
		restarted, restartErr := NewService(fixture.store, fixture.checkout, fixture.host)
		if restartErr != nil {
			t.Fatal(restartErr)
		}
		fixture.service = restarted
		_, err := restarted.Cleanup(context.Background(), fixture.command.RunID, testCoordinatorUUID, 1_005)
		if err != nil && !errors.Is(err, errLostReviewResponse) {
			t.Fatal(err)
		}
	}
	t.Fatal("review cleanup did not converge")
}

func TestReviewRecoversEveryLostEffectResponseAndCleansOnlyAfterDurableVerdict(t *testing.T) {
	fixture := newServiceFixture(t, true)
	admitted, err := fixture.service.Admit(context.Background(), fixture.command)
	if err != nil || !admitted.Progressed {
		t.Fatalf("Admit() = %#v, %v", admitted, err)
	}
	reconcileUntil(t, fixture, func(state domainreview.State) bool { return state.Bootstrap.Phase == domainreview.EffectComplete })
	run, _ := fixture.store.Run(context.Background(), fixture.command.RunID)
	if result, err := fixture.service.Cleanup(context.Background(), run.ID, testCoordinatorUUID, 1_005); !errors.Is(err, ErrReviewNotReady) || result.Progressed {
		t.Fatalf("pre-verdict Cleanup() = %#v, %v", result, err)
	}
	if !fixture.host.workspaceActive || !fixture.host.reviewerPresent {
		t.Fatal("pre-verdict cleanup changed Reviewer resources")
	}
	ci := reviewCI(*run.Execution.Review, "ci-observation-1")
	if _, err := fixture.service.ObserveCI(context.Background(), run.ID, testCoordinatorUUID, ci, 1_003); err != nil {
		t.Fatal(err)
	}
	reconcileUntil(t, fixture, func(state domainreview.State) bool { return state.Prompt.Phase == domainreview.EffectComplete })
	run, _ = fixture.store.Run(context.Background(), run.ID)
	prompt := fixture.host.promptCommand.Arguments.InitialPrompt
	if !strings.Contains(prompt, `"reviewerUuid":"`+testReviewerUUID+`"`) ||
		!strings.Contains(prompt, `"completeCiForbidden":true`) ||
		!strings.Contains(prompt, `"blockingProbePolicy":"smallest_reproducible_case"`) || strings.Contains(prompt, run.Execution.SourcePath) ||
		strings.Contains(prompt, run.Execution.WorktreePath) {
		t.Fatalf("Review prompt authority or isolation = %s", prompt)
	}
	claim := reviewClaim(*run.Execution.Review)
	if _, err := fixture.service.SubmitClaim(context.Background(), run.ID, claim, 1_004); err != nil {
		t.Fatal(err)
	}
	cleanupUntil(t, fixture)
	run, _ = fixture.store.Run(context.Background(), run.ID)
	if !domainreview.ValidState(*run.Execution.Review) || !run.Execution.Review.VerdictDurablyObserved ||
		run.Execution.CandidateAuthority.Downstream.Review == nil {
		t.Fatalf("durable Review evidence = %#v", run.Execution.Review)
	}
	for _, effect := range []domainreview.Effect{run.Execution.Review.Workspace, run.Execution.Review.Bootstrap,
		run.Execution.Review.Prompt, run.Execution.Review.AgentCleanup, run.Execution.Review.WorkspaceCleanup} {
		if effect.Cursor == 0 {
			t.Fatalf("host observation cursor was not durable for %s", effect.ID)
		}
	}
	if _, err := os.Stat(fixture.command.CheckoutPath); !os.IsNotExist(err) {
		t.Fatalf("Reviewer checkout remains after exact cleanup: %v", err)
	}
	for _, kind := range []execution.EffectKind{execution.EffectHostViewCreate, execution.EffectReviewerAgentCreate,
		execution.EffectAgentPrompt, execution.EffectReviewerAgentArchive, execution.EffectHostViewArchive} {
		if fixture.host.mutationCount[kind] != 1 {
			t.Fatalf("%s mutations = %d", kind, fixture.host.mutationCount[kind])
		}
	}
	if fixture.checkout.creates != 1 || fixture.checkout.removes != 1 {
		t.Fatalf("checkout mutations = create %d remove %d", fixture.checkout.creates, fixture.checkout.removes)
	}
}

func TestReviewAndCIOrderingConvergeWithoutSecondCompleteCI(t *testing.T) {
	for _, ciFirst := range []bool{false, true} {
		t.Run(map[bool]string{false: "review-before-ci", true: "ci-before-review"}[ciFirst], func(t *testing.T) {
			fixture := newServiceFixture(t, false)
			if _, err := fixture.service.Admit(context.Background(), fixture.command); err != nil {
				t.Fatal(err)
			}
			run, _ := fixture.store.Run(context.Background(), fixture.command.RunID)
			if ciFirst {
				if _, err := fixture.service.ObserveCI(context.Background(), run.ID, testCoordinatorUUID, reviewCI(*run.Execution.Review, "ci-ordering"), 1_003); err != nil {
					t.Fatal(err)
				}
			} else {
				reconcileUntil(t, fixture, func(state domainreview.State) bool { return state.Bootstrap.Phase == domainreview.EffectComplete })
				run, _ = fixture.store.Run(context.Background(), run.ID)
				result, err := fixture.service.Reconcile(context.Background(), run.ID, testCoordinatorUUID, 1_003)
				if err != nil || !result.WaitingForCI || result.Progressed {
					t.Fatalf("Review did not wait for sibling CI: %#v, %v", result, err)
				}
				if _, err := fixture.service.ObserveCI(context.Background(), run.ID, testCoordinatorUUID, reviewCI(*run.Execution.Review, "ci-ordering"), 1_003); err != nil {
					t.Fatal(err)
				}
			}
			reconcileUntil(t, fixture, func(state domainreview.State) bool { return state.Prompt.Phase == domainreview.EffectComplete })
			run, _ = fixture.store.Run(context.Background(), run.ID)
			if _, err := fixture.service.ObserveCI(context.Background(), run.ID, testCoordinatorUUID, *run.Execution.Review.CIObservation, 1_003); err != nil {
				t.Fatal("idempotent CI replay failed:", err)
			}
			changed := *run.Execution.Review.CIObservation
			changed.ID = "ci-observation-other"
			changed = domainreview.SealCIObservation(changed)
			if _, err := fixture.service.ObserveCI(context.Background(), run.ID, testCoordinatorUUID, changed, 1_003); !errors.Is(err, ErrReviewInvalidated) {
				t.Fatalf("changed CI error = %v", err)
			}
			run, _ = fixture.store.Run(context.Background(), run.ID)
			if !run.Execution.Review.Invalidated || run.Execution.Review.Evidence != nil {
				t.Fatal("changed CI retained Review authority")
			}
		})
	}
}

func TestConcurrentCoordinatorsCreateOneReviewAndIdentityDriftPreservesResources(t *testing.T) {
	fixture := newServiceFixture(t, false)
	const coordinators = 32
	var wait sync.WaitGroup
	wait.Add(coordinators)
	results := make(chan error, coordinators)
	for range coordinators {
		go func() {
			defer wait.Done()
			coordinator, _ := NewService(fixture.store, fixture.checkout, fixture.host)
			_, err := coordinator.Admit(context.Background(), fixture.command)
			results <- err
		}()
	}
	wait.Wait()
	close(results)
	successes := 0
	for err := range results {
		if err == nil {
			successes++
		} else if !errors.Is(err, ErrConcurrentTransition) && !errors.Is(err, ErrInvalidCommand) {
			t.Fatal(err)
		}
	}
	if successes != 1 {
		t.Fatalf("successful Review admissions = %d", successes)
	}
	reconcileUntil(t, fixture, func(state domainreview.State) bool { return state.Workspace.Phase == domainreview.EffectComplete })
	fixture.host.mu.Lock()
	fixture.host.reviewerPresent = true
	fixture.host.duplicateReviewers = true
	fixture.host.createCommand = host.Command{Arguments: host.Arguments{Labels: map[string]string{}}}
	fixture.host.mu.Unlock()
	if _, err := fixture.service.Reconcile(context.Background(), fixture.command.RunID, testCoordinatorUUID, 1_003); !errors.Is(err, ErrReviewAmbiguous) {
		t.Fatalf("duplicate Reviewer error = %v", err)
	}
	run, _ := fixture.store.Run(context.Background(), fixture.command.RunID)
	_, checkoutErr := os.Stat(fixture.command.CheckoutPath)
	if !run.Execution.Review.Invalidated || checkoutErr != nil {
		t.Fatalf("ambiguous Reviewer resources were not preserved: %#v, %v", run.Execution.Review, checkoutErr)
	}
}

func TestVerdictRequiresRuntimeHarnessEvidenceUUIDAndAdmittedAttempt(t *testing.T) {
	fixture := newServiceFixture(t, false)
	if _, err := fixture.service.Admit(context.Background(), fixture.command); err != nil {
		t.Fatal(err)
	}
	run, _ := fixture.store.Run(context.Background(), fixture.command.RunID)
	if _, err := fixture.service.ObserveCI(context.Background(), run.ID, testCoordinatorUUID, reviewCI(*run.Execution.Review, "ci-verdict"), 1_003); err != nil {
		t.Fatal(err)
	}
	reconcileUntil(t, fixture, func(state domainreview.State) bool { return state.Prompt.Phase == domainreview.EffectComplete })
	run, _ = fixture.store.Run(context.Background(), run.ID)
	claim := reviewClaim(*run.Execution.Review)
	claim.HarnessReviewerUUID = testOwnerUUID
	if _, err := fixture.service.SubmitClaim(context.Background(), run.ID, claim, 1_004); !errors.Is(err, ErrInvalidCommand) {
		t.Fatalf("harness UUID mismatch error = %v", err)
	}
	claim = reviewClaim(*run.Execution.Review)
	claim.AttemptReason = domainreview.AttemptHarnessPreStart
	claim.AttemptKey = domainreview.AttemptKey(claim.Binding, claim.AttemptReason)
	if _, err := fixture.service.SubmitClaim(context.Background(), run.ID, claim, 1_004); !errors.Is(err, ErrInvalidCommand) {
		t.Fatalf("unadmitted attempt error = %v", err)
	}
}

func TestRetryReasonsAreDeterministicSingleUseAndBoundedPerCandidate(t *testing.T) {
	fixture := newServiceFixture(t, false)
	admitted, err := fixture.service.Admit(context.Background(), fixture.command)
	if err != nil {
		t.Fatal(err)
	}
	provider, err := fixture.service.AdmitAttempt(context.Background(), AttemptCommand{
		RunID: admitted.Run.ID, ExpectedRunVersion: admitted.Run.Version,
		CoordinatorUUID: testCoordinatorUUID, Reason: domainreview.AttemptProviderPreDispatch, NowMillis: 1_002,
	})
	if err != nil || len(provider.Run.Execution.Review.Attempts) != 2 {
		t.Fatalf("provider pre-dispatch attempt = %#v, %v", provider, err)
	}
	if _, err := fixture.service.AdmitAttempt(context.Background(), AttemptCommand{
		RunID: provider.Run.ID, ExpectedRunVersion: provider.Run.Version,
		CoordinatorUUID: testCoordinatorUUID, Reason: domainreview.AttemptProviderPreDispatch, NowMillis: 1_002,
	}); !errors.Is(err, ErrInvalidCommand) {
		t.Fatalf("duplicate reason error = %v", err)
	}
	harness, err := fixture.service.AdmitAttempt(context.Background(), AttemptCommand{
		RunID: provider.Run.ID, ExpectedRunVersion: provider.Run.Version,
		CoordinatorUUID: testCoordinatorUUID, Reason: domainreview.AttemptHarnessPreStart, NowMillis: 1_002,
	})
	if err != nil || len(harness.Run.Execution.Review.Attempts) != 3 {
		t.Fatalf("harness pre-start attempt = %#v, %v", harness, err)
	}
}

func TestReviewerProfilePreflightHasNoHiddenFallback(t *testing.T) {
	fixture := newServiceFixture(t, false)
	run := fixture.store.run
	run.Execution.ReviewPolicy.RequireDifferentReviewerModel = true
	fixture.store.run = run
	fixture.command.ExpectedRunVersion = run.Version
	if result, err := fixture.service.Admit(context.Background(), fixture.command); !errors.Is(err, ErrInvalidCommand) || result.Progressed {
		t.Fatalf("same-model distinct-profile policy = %#v, %v", result, err)
	}
	if fixture.host.mutationCount[execution.EffectReviewerAgentCreate] != 0 {
		t.Fatal("profile refusal reached Reviewer provider")
	}

	fixture = newServiceFixture(t, false)
	if result, err := fixture.service.Admit(context.Background(), fixture.command); err != nil ||
		result.Run.Execution.Review.ProfileDecision.Warning != "same_reviewer_model" {
		t.Fatalf("same-model warning = %#v, %v", result, err)
	}
}

func TestAgentMCPContractRemainsTheOnlyReviewerMutationSurface(t *testing.T) {
	contractHash, err := agentbridge.SchemaSHA256()
	if err != nil || len(contractHash) != 64 {
		t.Fatal(err)
	}
	fixture := newServiceFixture(t, false)
	if _, err := fixture.service.Admit(context.Background(), fixture.command); err != nil {
		t.Fatal(err)
	}
	run, _ := fixture.store.Run(context.Background(), fixture.command.RunID)
	role, _ := run.Execution.EffectiveProfiles.Role(agentprofile.RoleReviewer)
	tools, err := agentbridge.Catalog(role)
	if err != nil || len(tools) != 2 || tools[0].Name != "director_candidate_read" || tools[1].Name != "director_review_verdict_submit" ||
		tools[0].Mutating || !tools[1].Mutating {
		t.Fatalf("Reviewer MCP authority = %#v, %v", tools, err)
	}
}

func TestInvalidReviewerPathsNeverReachGitOrHost(t *testing.T) {
	fixture := newServiceFixture(t, false)
	fixture.command.CheckoutPath = fixture.store.run.Execution.WorktreePath
	if _, err := fixture.service.Admit(context.Background(), fixture.command); !errors.Is(err, ErrInvalidCommand) {
		t.Fatalf("primary checkout alias error = %v", err)
	}
	if fixture.store.writeCount != 0 || len(fixture.host.mutationCount) != 0 {
		t.Fatal("invalid Reviewer path reached a mutation")
	}
}

func TestStaleProjectLeaseRefusesReviewBeforeAnyExternalEffect(t *testing.T) {
	fixture := newServiceFixture(t, false)
	fixture.store.project.Lease.ExpiresAtMillis = fixture.command.NowMillis
	if _, err := fixture.service.Admit(context.Background(), fixture.command); !errors.Is(err, ErrInvalidCommand) {
		t.Fatalf("expired Review lease error = %v", err)
	}
	if fixture.store.writeCount != 0 || len(fixture.host.mutationCount) != 0 {
		t.Fatal("expired Review lease reached durable or external mutation")
	}
}
