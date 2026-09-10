// SPDX-License-Identifier: Apache-2.0

// Package fake supplies deterministic, disposable M1 adapters. It never
// connects to Paseo or a real provider and must not be selected by production
// configuration.
package fake

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/mcuadros/director-engine/domain/execution"
	repositorydomain "github.com/mcuadros/director-engine/domain/repository"
	"github.com/mcuadros/director-engine/domain/runtimebudget"
	"github.com/mcuadros/director-engine/ports/host"
	reconciliationport "github.com/mcuadros/director-engine/ports/reconciliation"
	runtimeport "github.com/mcuadros/director-engine/ports/runtime"
)

// ErrResponseLost reports that a disposable adapter applied an effect but its
// response was intentionally lost. Callers must observe rather than repeat it.
var ErrResponseLost = errors.New("fake adapter response lost after possible handoff")

// Options fixes one disposable repository and external-world identity.
type Options struct {
	SourcePath                string
	WorktreePath              string
	Branch                    string
	BaseSHA                   string
	Operational               execution.OperationalObservation
	LoseEveryMutationResponse bool
	GlobalActiveAgents        uint32
}

type helperWorld struct {
	helperID        string
	agentID         string
	parentAgentID   string
	workspaceID     string
	labels          map[string]string
	checkoutID      string
	boundaryID      string
	boundaryReady   bool
	importedCommit  string
	agentActive     bool
	agentArchived   bool
	bootstrapDone   bool
	primaryFactHash string
}

type world struct {
	worktreeID                string
	boundaryID                string
	hostViewID                string
	agentID                   string
	boundaryReady             bool
	setupComplete             bool
	hostViewActive            bool
	agentActive               bool
	agentArchived             bool
	hostViewArchived          bool
	bootstrapEffectID         string
	promptEffectID            string
	bootstrapStatus           execution.ObservationStatus
	promptStatus              execution.ObservationStatus
	bootstrapObservedAtMillis int64
	promptObservedAtMillis    int64
	recoveryID                string
	recoveryReady             bool
	recoveryPath              string
	bindingHash               string
}

// Environment implements both the non-host runtime port and the one host port
// with fake, observable effects over an owned disposable Git repository.
type Environment struct {
	mu                  sync.Mutex
	options             Options
	world               world
	operational         execution.OperationalObservation
	observationSeq      uint64
	mutations           map[execution.EffectKind]int
	mutationOrder       []string
	lost                map[execution.EffectKind]bool
	hostCalls           int
	agentRequest        host.Arguments
	promptRequest       host.Arguments
	reconciliations     map[string]reconciliationport.Receipt
	helpers             map[string]*helperWorld
	helperReservations  map[string]execution.Scope
	reconciliationCount int
	eventSeq            uint64
}

var _ runtimeport.Port = (*Environment)(nil)
var _ host.Port = (*Environment)(nil)
var _ reconciliationport.Queue = (*Environment)(nil)

// RestartedEnvironment is a new policy-free adapter instance over the same
// fake external world. It models a connector/engine process replacement:
// in-memory controller state is gone while observable external facts survive.
type RestartedEnvironment struct {
	world *Environment
}

var _ runtimeport.Port = (*RestartedEnvironment)(nil)
var _ host.Port = (*RestartedEnvironment)(nil)
var _ reconciliationport.Queue = (*RestartedEnvironment)(nil)

// NewEnvironment creates an empty fake world. All mutations remain scoped to
// Options.WorktreePath and the disposable source repository.
func NewEnvironment(options Options) *Environment {
	return &Environment{
		options: options, operational: options.Operational,
		mutations:          make(map[execution.EffectKind]int),
		lost:               make(map[execution.EffectKind]bool),
		reconciliations:    make(map[string]reconciliationport.Receipt),
		helpers:            make(map[string]*helperWorld),
		helperReservations: make(map[string]execution.Scope),
	}
}

// Restart returns a distinct adapter instance over the persistent fake world.
func (environment *Environment) Restart() *RestartedEnvironment {
	return &RestartedEnvironment{world: environment}
}

func (environment *RestartedEnvironment) Describe(ctx context.Context) (host.Descriptor, error) {
	return environment.world.Describe(ctx)
}

func (environment *RestartedEnvironment) Invoke(ctx context.Context, command host.Command) (host.Observation, error) {
	return environment.world.Invoke(ctx, command)
}

func (environment *RestartedEnvironment) ObserveEffect(ctx context.Context, request runtimeport.Request) (execution.EffectObservation, error) {
	return environment.world.ObserveEffect(ctx, request)
}

func (environment *RestartedEnvironment) DispatchEffect(ctx context.Context, request runtimeport.Request) error {
	return environment.world.DispatchEffect(ctx, request)
}

func (environment *RestartedEnvironment) ObserveOperational(ctx context.Context, scope execution.Scope, policy execution.OperationalPolicy) (execution.OperationalObservation, error) {
	return environment.world.ObserveOperational(ctx, scope, policy)
}

func (environment *RestartedEnvironment) ObserveCandidate(ctx context.Context, request runtimeport.CandidateRequest) (execution.CandidateObservation, error) {
	return environment.world.ObserveCandidate(ctx, request)
}

func (environment *RestartedEnvironment) Enqueue(ctx context.Context, request reconciliationport.Request) (reconciliationport.Receipt, error) {
	return environment.world.Enqueue(ctx, request)
}

func digest(value any) string {
	encoded, err := json.Marshal(value)
	if err != nil {
		panic("marshal fake observation: " + err.Error())
	}
	sum := sha256.Sum256(encoded)
	return hex.EncodeToString(sum[:])
}

func externalID(prefix, effectID string) string {
	sum := sha256.Sum256([]byte(prefix + "\x1f" + effectID))
	return prefix + "-" + hex.EncodeToString(sum[:16])
}

func runGit(directory string, arguments ...string) (string, error) {
	command := exec.Command("git", arguments...)
	command.Dir = directory
	output, err := command.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("fake Git operation failed: %w: %s", err, strings.TrimSpace(string(output)))
	}
	return strings.TrimSpace(string(output)), nil
}

func pathPresent(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

func copyRecoveryTree(source, destination string) error {
	if err := os.Mkdir(destination, 0o700); err != nil {
		return err
	}
	return filepath.WalkDir(source, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		relative, err := filepath.Rel(source, path)
		if err != nil {
			return err
		}
		if relative == ".git" || strings.HasPrefix(relative, ".git"+string(filepath.Separator)) {
			if entry.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if relative == "." {
			return nil
		}
		target := filepath.Join(destination, relative)
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if entry.IsDir() {
			return os.Mkdir(target, 0o700)
		}
		if !info.Mode().IsRegular() {
			return errors.New("fake recovery refuses non-regular worktree material")
		}
		input, err := os.Open(path)
		if err != nil {
			return err
		}
		output, err := os.OpenFile(target, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
		if err != nil {
			_ = input.Close()
			return err
		}
		_, copyErr := io.CopyBuffer(output, input, make([]byte, 64*1024))
		inputCloseErr := input.Close()
		closeErr := output.Close()
		if copyErr != nil {
			return copyErr
		}
		if inputCloseErr != nil {
			return inputCloseErr
		}
		return closeErr
	})
}

func (environment *Environment) exactRequest(request runtimeport.Request) bool {
	if request.Scope.RunID == "" || request.BindingHash == "" ||
		!execution.ValidLeaseBinding(request.LeaseBinding) ||
		execution.RepositoryBindingSHA256(request.Repository) != request.BindingHash ||
		request.SourcePath != environment.options.SourcePath ||
		request.WorktreePath != environment.options.WorktreePath ||
		request.Branch != environment.options.Branch || request.BaseSHA != environment.options.BaseSHA {
		return false
	}
	sourceStatus, sourceErr := os.Stat(request.SourcePath)
	commonStatus, commonErr := os.Stat(request.Repository.GitCommonDirectory)
	sourceIdentity, sourceIdentityOK := sourceStatusSys(sourceStatus)
	commonIdentity, commonIdentityOK := sourceStatusSys(commonStatus)
	remoteValue, remoteErr := runGit(request.SourcePath, "remote", "get-url", "origin")
	remote, canonicalErr := repositorydomain.CanonicalRemote(remoteValue)
	if sourceErr != nil || commonErr != nil || !sourceIdentityOK || !commonIdentityOK ||
		remoteErr != nil || canonicalErr != nil || remote.ID != request.Repository.RepositoryID ||
		remote.Key != request.Repository.RepositoryKey || remote.Canonical != request.Repository.CanonicalRemote ||
		sourceIdentity[0] != request.Repository.SourceDevice || sourceIdentity[1] != request.Repository.SourceInode ||
		commonIdentity[0] != request.Repository.GitCommonDevice || commonIdentity[1] != request.Repository.GitCommonInode {
		return false
	}
	if _, err := runGit(request.SourcePath, "cat-file", "-e", request.BaseSHA+"^{commit}"); err != nil {
		return false
	}
	if pathPresent(request.WorktreePath) {
		branch, err := runGit(request.WorktreePath, "symbolic-ref", "HEAD")
		return err == nil && branch == "refs/heads/"+request.Branch
	}
	if request.Effect.Kind == execution.EffectWorktreeCreate && request.Effect.Phase == execution.EffectIntentRecorded {
		return gitRefAbsent(request.SourcePath, "refs/heads/"+request.Branch)
	}
	return true
}

func gitRefAbsent(directory, ref string) bool {
	command := exec.Command("git", "show-ref", "--verify", "--quiet", ref)
	command.Dir = directory
	err := command.Run()
	var failure *exec.ExitError
	return errors.As(err, &failure) && failure.ExitCode() == 1
}

func sourceStatusSys(status os.FileInfo) ([2]uint64, bool) {
	if status == nil {
		return [2]uint64{}, false
	}
	identity, ok := status.Sys().(*syscall.Stat_t)
	if !ok {
		return [2]uint64{}, false
	}
	return [2]uint64{uint64(identity.Dev), identity.Ino}, true
}

func effectObservation(effect execution.Effect, status execution.ObservationStatus, external, bindingHash string, sequence uint64, observedAtMillis int64) execution.EffectObservation {
	observation := execution.EffectObservation{
		ID: fmt.Sprintf("observation-%d", sequence), EffectID: effect.ID,
		Status: status, ExternalID: external, BindingHash: bindingHash,
		PriorDispatcherAbsent: true, ObservedAtMillis: observedAtMillis, MaximumAgeMillis: 30_000,
	}
	observation.FactHash = digest(observation)
	return observation
}

// ObserveEffect returns an authoritative normalized fixture fact.
func (environment *Environment) ObserveEffect(_ context.Context, request runtimeport.Request) (execution.EffectObservation, error) {
	environment.mu.Lock()
	defer environment.mu.Unlock()
	environment.observationSeq++
	if !environment.exactRequest(request) {
		return effectObservation(request.Effect, execution.ObservationDifferent, "", request.BindingHash, environment.observationSeq, environment.operational.ObservedAtMillis), nil
	}
	switch request.Effect.Kind {
	case execution.EffectWorktreeCreate:
		if !pathPresent(environment.options.WorktreePath) {
			return effectObservation(request.Effect, execution.ObservationAbsent, "", request.BindingHash, environment.observationSeq, environment.operational.ObservedAtMillis), nil
		}
		return effectObservation(request.Effect, execution.ObservationDesired, environment.world.worktreeID, request.BindingHash, environment.observationSeq, environment.operational.ObservedAtMillis), nil
	case execution.EffectBoundaryMaterialize:
		if !environment.world.boundaryReady {
			return effectObservation(request.Effect, execution.ObservationAbsent, "", request.BindingHash, environment.observationSeq, environment.operational.ObservedAtMillis), nil
		}
		return effectObservation(request.Effect, execution.ObservationDesired, environment.world.boundaryID, request.BindingHash, environment.observationSeq, environment.operational.ObservedAtMillis), nil
	case execution.EffectSetupRun:
		if !environment.world.setupComplete {
			return effectObservation(request.Effect, execution.ObservationAbsent, "", request.BindingHash, environment.observationSeq, environment.operational.ObservedAtMillis), nil
		}
		return effectObservation(request.Effect, execution.ObservationDesired, externalID("setup", request.Effect.ID), request.BindingHash, environment.observationSeq, environment.operational.ObservedAtMillis), nil
	case execution.EffectWorktreeRemove:
		if pathPresent(environment.options.WorktreePath) {
			return effectObservation(request.Effect, execution.ObservationOwnedPresent, environment.world.worktreeID, request.BindingHash, environment.observationSeq, environment.operational.ObservedAtMillis), nil
		}
		return effectObservation(request.Effect, execution.ObservationDesired, environment.world.worktreeID, request.BindingHash, environment.observationSeq, environment.operational.ObservedAtMillis), nil
	case execution.EffectRecoverySnapshot:
		if !environment.world.recoveryReady {
			return effectObservation(request.Effect, execution.ObservationAbsent, "", request.BindingHash, environment.observationSeq, environment.operational.ObservedAtMillis), nil
		}
		return effectObservation(request.Effect, execution.ObservationDesired, environment.world.recoveryID, request.BindingHash, environment.observationSeq, environment.operational.ObservedAtMillis), nil
	default:
		return execution.EffectObservation{}, fmt.Errorf("unsupported fake runtime observation %q", request.Effect.Kind)
	}
}

func (environment *Environment) recordMutation(kind execution.EffectKind) error {
	environment.mutations[kind]++
	environment.mutationOrder = append(environment.mutationOrder, string(kind))
	if environment.options.LoseEveryMutationResponse && !environment.lost[kind] {
		environment.lost[kind] = true
		return ErrResponseLost
	}
	return nil
}

// DispatchEffect performs one fake Git, OCI, or approved setup operation.
func (environment *Environment) DispatchEffect(_ context.Context, request runtimeport.Request) error {
	environment.mu.Lock()
	defer environment.mu.Unlock()
	if !environment.exactRequest(request) {
		return errors.New("fake runtime request identity mismatch")
	}
	lifecycle := execution.AdmitLifecycle(request.Scope, request.LifecycleSurfaces, request.LifecycleApproval)
	isolation := execution.AdmitIsolation(request.Isolation)
	if request.Scope.RunID == "" || lifecycle.Kind != execution.AdmissionAllow ||
		lifecycle.Digest != request.LifecycleDigest || isolation.Kind != execution.AdmissionAllow ||
		isolation.Digest != request.IsolationDigest {
		return errors.New("fake runtime received an unadmitted scope")
	}
	switch request.Effect.Kind {
	case execution.EffectWorktreeCreate:
		if pathPresent(environment.options.WorktreePath) {
			return errors.New("fake worktree already exists before dispatch")
		}
		if _, err := runGit(environment.options.SourcePath, "worktree", "add", "-b", environment.options.Branch, environment.options.WorktreePath, environment.options.BaseSHA); err != nil {
			return err
		}
		environment.world.worktreeID = externalID("worktree", request.Effect.ID)
	case execution.EffectBoundaryMaterialize:
		if !pathPresent(environment.options.WorktreePath) || request.IsolationDigest == "" {
			return errors.New("fake rootless OCI boundary lacks its admitted worktree or digest")
		}
		environment.world.boundaryReady = true
		environment.world.boundaryID = externalID("boundary", request.Effect.ID)
	case execution.EffectSetupRun:
		if !environment.world.boundaryReady || request.IsolationDigest == "" {
			return errors.New("fake setup was requested outside the admitted rootless OCI boundary")
		}
		environment.world.setupComplete = true
	case execution.EffectWorktreeRemove:
		controlRecovery := !request.ControlCleanupAuthorized ||
			(request.ControlRecoveryArtifactID != "" && request.ControlRecoveryArtifactID == environment.world.recoveryID && environment.world.recoveryReady)
		if !environment.world.agentArchived || !environment.world.hostViewArchived || !controlRecovery || request.WorktreeID != environment.world.worktreeID {
			return errors.New("fake worktree cleanup lacks exact terminal ownership facts")
		}
		if _, err := runGit(environment.options.SourcePath, "worktree", "remove", "--force", environment.options.WorktreePath); err != nil {
			return err
		}
	case execution.EffectRecoverySnapshot:
		if !pathPresent(environment.options.WorktreePath) || request.WorktreeID != environment.world.worktreeID {
			return errors.New("fake recovery snapshot lacks the exact owned worktree")
		}
		recoveryPath := filepath.Join(
			filepath.Dir(environment.options.WorktreePath),
			"."+filepath.Base(environment.options.WorktreePath)+"-recovery-"+strings.TrimPrefix(request.Effect.ID, "effect-"),
		)
		if pathPresent(recoveryPath) {
			return errors.New("fake recovery artifact already exists before dispatch")
		}
		if err := copyRecoveryTree(environment.options.WorktreePath, recoveryPath); err != nil {
			return err
		}
		if len(request.RecoveryWorktreePaths) > 0 {
			helperRoot := filepath.Join(recoveryPath, "helpers")
			if err := os.Mkdir(helperRoot, 0o700); err != nil {
				return err
			}
			for _, helperPath := range request.RecoveryWorktreePaths {
				owned := false
				for helperID, helper := range environment.helpers {
					if helper.checkoutID != "" && helperPath == execution.HelperWorktreePath(environment.options.WorktreePath, helperID, execution.HelperWriter) {
						owned = true
						break
					}
				}
				if !owned || !pathPresent(helperPath) {
					return errors.New("fake recovery helper checkout identity is invalid")
				}
				if err := copyRecoveryTree(helperPath, filepath.Join(helperRoot, filepath.Base(helperPath))); err != nil {
					return err
				}
			}
		}
		environment.world.recoveryID = externalID("recovery", request.Effect.ID)
		environment.world.recoveryReady = true
		environment.world.recoveryPath = recoveryPath
	default:
		return fmt.Errorf("unsupported fake runtime mutation %q", request.Effect.Kind)
	}
	return environment.recordMutation(request.Effect.Kind)
}

// ObserveOperational returns a fresh immutable copy of the configured sample.
func (environment *Environment) ObserveOperational(_ context.Context, _ execution.Scope, _ execution.OperationalPolicy) (execution.OperationalObservation, error) {
	environment.mu.Lock()
	defer environment.mu.Unlock()
	environment.observationSeq++
	observation := environment.operational
	observation.ID = fmt.Sprintf("%s-%d", observation.ID, environment.observationSeq)
	return observation, nil
}

// ObserveCandidate derives exact Candidate facts from Git rather than a claim.
func (environment *Environment) ObserveCandidate(_ context.Context, request runtimeport.CandidateRequest) (execution.CandidateObservation, error) {
	environment.mu.Lock()
	defer environment.mu.Unlock()
	environment.observationSeq++
	if request.SourcePath != environment.options.SourcePath || request.WorktreePath != environment.options.WorktreePath ||
		request.Branch != environment.options.Branch || request.WorktreeID != environment.world.worktreeID {
		observation := execution.CandidateObservation{
			ID:      fmt.Sprintf("candidate-observation-%d", environment.observationSeq),
			ClaimID: request.Claim.ID, WorktreeID: request.WorktreeID,
			BindingHash: request.BindingHash, CommitSHA: request.Claim.CandidateSHA,
			BaseSHA: request.Claim.BaseSHA, ObservedAtMillis: environment.operational.ObservedAtMillis, MaximumAgeMillis: 30_000,
		}
		observation.FactHash = digest(observation)
		return observation, nil
	}
	status, statusErr := runGit(request.WorktreePath, "status", "--porcelain=v1")
	head, headErr := runGit(request.WorktreePath, "rev-parse", "HEAD")
	_, reachableErr := runGit(request.WorktreePath, "cat-file", "-e", request.Claim.CandidateSHA+"^{commit}")
	_, ancestryErr := runGit(request.WorktreePath, "merge-base", "--is-ancestor", request.Claim.BaseSHA, request.Claim.CandidateSHA)
	observation := execution.CandidateObservation{
		ID:      fmt.Sprintf("candidate-observation-%d", environment.observationSeq),
		ClaimID: request.Claim.ID, WorktreeID: request.WorktreeID,
		BindingHash: request.BindingHash, CommitSHA: request.Claim.CandidateSHA,
		BaseSHA: request.Claim.BaseSHA, ObservedAtMillis: environment.operational.ObservedAtMillis, MaximumAgeMillis: 30_000,
		Clean: statusErr == nil && status == "", Reachable: reachableErr == nil,
		Owned:            headErr == nil && head == request.Claim.CandidateSHA && request.WorktreeID == environment.world.worktreeID,
		DescendsFromBase: ancestryErr == nil, NoConflict: statusErr == nil,
	}
	observation.FactHash = digest(observation)
	return observation, nil
}

// Describe returns the exact engine-owned descriptor without contacting Paseo.
func (environment *Environment) Describe(_ context.Context) (host.Descriptor, error) {
	return host.ExpectedDescriptor()
}

func hostObservation(command host.Command, status execution.ObservationStatus, external string, sequence uint64, observedAtMillis int64) host.Observation {
	correlation := ""
	if command.Arguments.EffectKind == execution.EffectAgentCreate {
		if registration, err := host.ParseWorkerLabels(command.Arguments.Labels); err == nil {
			correlation, _ = host.RegistrationDigest(registration)
		}
	}
	if command.Arguments.EffectKind == execution.EffectHelperAgentObserve || command.Arguments.EffectKind == execution.EffectHelperAgentArchive ||
		((command.Arguments.EffectKind == execution.EffectControlAgentBoundary || command.Arguments.EffectKind == execution.EffectControlAgentArchive) &&
			command.Arguments.ParentAgentID != nil) {
		correlation = digest(command.Arguments.Labels)
	}
	result := host.ObservationResult{
		EffectID: command.Arguments.EffectID, Status: status, ExternalID: external,
		BindingHash: command.Arguments.BindingHash, CorrelationHash: correlation,
		PriorDispatcherAbsent: true,
		MaximumAgeMillis:      30_000,
	}
	if (command.Arguments.EffectKind == execution.EffectAgentCreate ||
		command.Arguments.EffectKind == execution.EffectAgentPrompt ||
		command.Arguments.EffectKind == execution.EffectControlAgentArchive) &&
		(status == execution.ObservationDesired || status == execution.ObservationErrored || status == execution.ObservationPermission) {
		result.Usage = &runtimebudget.ProviderUsage{
			State: runtimebudget.UsageCurrent, SourceRevision: "usage:" + command.Arguments.EffectID,
			InputTokensPresent: true, InputTokens: 10,
			CachedInputTokens: 5, OutputTokensPresent: true, OutputTokens: 2,
			CostMicrousdPresent: true, CostMicrousd: 100,
		}
	}
	result.FactHash = host.ObservationResultHash(result)
	return host.Observation{
		RequestID: command.RequestID, Cursor: sequence,
		ObservedAt: time.UnixMilli(observedAtMillis).UTC().Format(time.RFC3339Nano), Result: result,
	}
}

// Invoke translates one fixed host capability and contains no workflow choice.
func (environment *Environment) Invoke(_ context.Context, command host.Command) (host.Observation, error) {
	environment.mu.Lock()
	defer environment.mu.Unlock()
	environment.hostCalls++
	environment.observationSeq++
	arguments := command.Arguments
	switch command.Capability {
	case host.CapabilityHelperAgentObserve:
		for _, helper := range environment.helpers {
			if helper.parentAgentID != dereference(arguments.ParentAgentID) || helper.workspaceID != arguments.WorkspaceID ||
				digest(helper.labels) != digest(arguments.Labels) || (arguments.AgentID != "" && arguments.AgentID != helper.agentID) {
				continue
			}
			status := execution.ObservationOwnedPresent
			if arguments.EffectKind != execution.EffectHelperAgentArchive && helper.bootstrapDone {
				status = execution.ObservationDesired
			}
			if helper.agentArchived {
				status = execution.ObservationDesired
			}
			return hostObservation(command, status, helper.agentID, environment.observationSeq, environment.operational.ObservedAtMillis), nil
		}
		return hostObservation(command, execution.ObservationAbsent, "", environment.observationSeq, environment.operational.ObservedAtMillis), nil
	case host.CapabilityWorkspaceObserve:
		if arguments.EffectKind == execution.EffectHostViewArchive {
			if environment.world.hostViewArchived {
				return hostObservation(command, execution.ObservationDesired, environment.world.hostViewID, environment.observationSeq, environment.operational.ObservedAtMillis), nil
			}
			if environment.world.hostViewActive {
				return hostObservation(command, execution.ObservationOwnedPresent, environment.world.hostViewID, environment.observationSeq, environment.operational.ObservedAtMillis), nil
			}
			return hostObservation(command, execution.ObservationAmbiguous, "", environment.observationSeq, environment.operational.ObservedAtMillis), nil
		}
		if environment.world.hostViewActive {
			return hostObservation(command, execution.ObservationDesired, environment.world.hostViewID, environment.observationSeq, environment.operational.ObservedAtMillis), nil
		}
		return hostObservation(command, execution.ObservationAbsent, "", environment.observationSeq, environment.operational.ObservedAtMillis), nil
	case host.CapabilityWorkspaceCreate:
		if !pathPresent(arguments.WorktreePath) || arguments.WorktreeID != environment.world.worktreeID || arguments.LifecycleDigest == "" {
			return host.Observation{}, errors.New("fake host view registration lacks the admitted Director worktree")
		}
		environment.world.hostViewID = externalID("workspace", arguments.EffectID)
		environment.world.hostViewActive = true
		return host.Observation{}, environment.recordMutation(arguments.EffectKind)
	case host.CapabilityTaskAgentCreate:
		if !environment.world.hostViewActive || !environment.world.boundaryReady || !arguments.PreparationReady ||
			arguments.PreparationBarrierHash == "" ||
			arguments.ParentAgentID != nil || arguments.Title == "" ||
			arguments.IsolationDigest == "" || arguments.WorkspaceID != environment.world.hostViewID {
			return host.Observation{}, errors.New("fake Task Agent create contract is incomplete")
		}
		if _, err := host.AdmitAgentCreateLabels(command); err != nil {
			return host.Observation{}, fmt.Errorf("fake Task Agent create is not registered for root-workspace visibility: %w", err)
		}
		environment.agentRequest = arguments
		environment.world.agentID = externalID("agent", arguments.EffectID)
		environment.world.agentActive = true
		environment.world.bootstrapEffectID = arguments.EffectID
		environment.world.bootstrapStatus = execution.ObservationOwnedPresent
		environment.world.bindingHash = arguments.BindingHash
		return host.Observation{}, environment.recordMutation(arguments.EffectKind)
	case host.CapabilityAgentPrompt:
		registration, err := host.ParseWorkerLabels(environment.agentRequest.Labels)
		if err != nil {
			return host.Observation{}, err
		}
		if environment.world.bootstrapStatus != execution.ObservationDesired {
			return host.Observation{}, errors.New("fake real prompt precedes completed bootstrap")
		}
		if err := host.AdmitAgentPrompt(command, registration, environment.world.agentID); err != nil {
			return host.Observation{}, err
		}
		environment.promptRequest = arguments
		environment.world.promptEffectID = arguments.EffectID
		environment.world.promptStatus = execution.ObservationOwnedPresent
		return host.Observation{}, environment.recordMutation(arguments.EffectKind)
	case host.CapabilityAgentObserve:
		if (arguments.EffectKind == execution.EffectControlAgentBoundary || arguments.EffectKind == execution.EffectControlAgentArchive) &&
			arguments.ParentAgentID != nil {
			for _, helper := range environment.helpers {
				if helper.agentID != arguments.AgentID || helper.parentAgentID != dereference(arguments.ParentAgentID) ||
					helper.workspaceID != arguments.WorkspaceID || digest(helper.labels) != digest(arguments.Labels) {
					continue
				}
				if helper.agentArchived {
					return hostObservation(command, execution.ObservationDesired, helper.agentID, environment.observationSeq, environment.operational.ObservedAtMillis), nil
				}
				if helper.agentActive {
					observation := hostObservation(command, execution.ObservationOwnedPresent, helper.agentID, environment.observationSeq, environment.operational.ObservedAtMillis)
					observation.Result.PriorDispatcherAbsent = false
					observation.Result.FactHash = host.ObservationResultHash(observation.Result)
					return observation, nil
				}
				return hostObservation(command, execution.ObservationAmbiguous, "", environment.observationSeq, environment.operational.ObservedAtMillis), nil
			}
			return hostObservation(command, execution.ObservationAbsent, "", environment.observationSeq, environment.operational.ObservedAtMillis), nil
		}
		if arguments.EffectKind == execution.EffectControlAgentBoundary {
			if arguments.AgentID != environment.world.agentID {
				return hostObservation(command, execution.ObservationDifferent, arguments.AgentID, environment.observationSeq, environment.operational.ObservedAtMillis), nil
			}
			if environment.world.agentArchived || environment.world.promptStatus == execution.ObservationDesired ||
				environment.world.promptStatus == execution.ObservationErrored || environment.world.promptStatus == execution.ObservationPermission {
				return hostObservation(command, execution.ObservationDesired, environment.world.agentID, environment.observationSeq, environment.operational.ObservedAtMillis), nil
			}
			if environment.world.agentActive {
				observation := hostObservation(command, execution.ObservationOwnedPresent, environment.world.agentID, environment.observationSeq, environment.operational.ObservedAtMillis)
				observation.Result.PriorDispatcherAbsent = false
				observation.Result.FactHash = host.ObservationResultHash(observation.Result)
				return observation, nil
			}
			return hostObservation(command, execution.ObservationAmbiguous, "", environment.observationSeq, environment.operational.ObservedAtMillis), nil
		}
		if arguments.EffectKind == execution.EffectControlAgentArchive {
			if arguments.AgentID != environment.world.agentID {
				return hostObservation(command, execution.ObservationDifferent, arguments.AgentID, environment.observationSeq, environment.operational.ObservedAtMillis), nil
			}
			if environment.world.agentArchived {
				return hostObservation(command, execution.ObservationDesired, environment.world.agentID, environment.observationSeq, environment.operational.ObservedAtMillis), nil
			}
			if environment.world.agentActive {
				return hostObservation(command, execution.ObservationOwnedPresent, environment.world.agentID, environment.observationSeq, environment.operational.ObservedAtMillis), nil
			}
			return hostObservation(command, execution.ObservationAmbiguous, "", environment.observationSeq, environment.operational.ObservedAtMillis), nil
		}
		if arguments.EffectKind == execution.EffectAgentArchive {
			if environment.world.agentArchived {
				return hostObservation(command, execution.ObservationDesired, environment.world.agentID, environment.observationSeq, environment.operational.ObservedAtMillis), nil
			}
			if environment.world.agentActive {
				return hostObservation(command, execution.ObservationOwnedPresent, environment.world.agentID, environment.observationSeq, environment.operational.ObservedAtMillis), nil
			}
			return hostObservation(command, execution.ObservationAmbiguous, "", environment.observationSeq, environment.operational.ObservedAtMillis), nil
		}
		if arguments.EffectKind == execution.EffectAgentCreate {
			if !environment.world.agentActive {
				return hostObservation(command, execution.ObservationAbsent, "", environment.observationSeq, environment.operational.ObservedAtMillis), nil
			}
			observedAt := environment.operational.ObservedAtMillis
			if environment.world.bootstrapObservedAtMillis > observedAt {
				observedAt = environment.world.bootstrapObservedAtMillis
			}
			return hostObservation(command, environment.world.bootstrapStatus, environment.world.agentID, environment.observationSeq, observedAt), nil
		}
		if arguments.EffectKind == execution.EffectAgentPrompt {
			if environment.world.promptEffectID == "" {
				return hostObservation(command, execution.ObservationAbsent, "", environment.observationSeq, environment.operational.ObservedAtMillis), nil
			}
			observedAt := environment.operational.ObservedAtMillis
			if environment.world.promptObservedAtMillis > observedAt {
				observedAt = environment.world.promptObservedAtMillis
			}
			return hostObservation(command, environment.world.promptStatus, environment.world.agentID, environment.observationSeq, observedAt), nil
		}
		return host.Observation{}, errors.New("fake agent observation effect is unsupported")
	case host.CapabilityAgentArchive:
		if arguments.EffectKind == execution.EffectHelperAgentArchive ||
			(arguments.EffectKind == execution.EffectControlAgentArchive && arguments.ParentAgentID != nil) {
			for _, helper := range environment.helpers {
				if helper.agentID == arguments.AgentID && helper.parentAgentID == dereference(arguments.ParentAgentID) && helper.agentActive {
					helper.agentActive = false
					helper.agentArchived = true
					return host.Observation{}, environment.recordMutation(arguments.EffectKind)
				}
			}
			return host.Observation{}, errors.New("fake helper archive identity mismatch")
		}
		if arguments.AgentID != environment.world.agentID || !environment.world.agentActive ||
			(arguments.EffectKind != execution.EffectAgentArchive && arguments.EffectKind != execution.EffectControlAgentArchive) {
			return host.Observation{}, errors.New("fake agent archive identity mismatch")
		}
		environment.world.agentActive = false
		environment.world.agentArchived = true
		return host.Observation{}, environment.recordMutation(arguments.EffectKind)
	case host.CapabilityWorkspaceArchive:
		if arguments.WorkspaceID != environment.world.hostViewID || !environment.world.hostViewActive || !environment.world.agentArchived {
			return host.Observation{}, errors.New("fake host view archive identity mismatch")
		}
		environment.world.hostViewActive = false
		environment.world.hostViewArchived = true
		return host.Observation{}, environment.recordMutation(arguments.EffectKind)
	default:
		return host.Observation{}, fmt.Errorf("unsupported fake host capability %q", command.Capability)
	}
}

func dereference(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}

// CandidateRequest fixes the fake provider claim fields supplied by a test.
type CandidateRequest struct {
	ClaimID         string
	AgentID         string
	BaseSHA         string
	CriteriaResults map[string]string
}

// ProduceCandidate emulates one trusted fake provider inside the admitted
// fixture boundary. It returns a claim; the engine still observes Git itself.
func (environment *Environment) ProduceCandidate(_ context.Context, request CandidateRequest) (execution.CompletedClaim, error) {
	environment.mu.Lock()
	defer environment.mu.Unlock()
	if !environment.world.agentActive || request.AgentID != environment.world.agentID || !environment.world.boundaryReady ||
		environment.world.promptStatus != execution.ObservationOwnedPresent {
		return execution.CompletedClaim{}, errors.New("fake provider is not inside the admitted active Task Agent")
	}
	if err := os.WriteFile(filepath.Join(environment.options.WorktreePath, "RESULT.md"), []byte("fake Candidate\n"), 0o600); err != nil {
		return execution.CompletedClaim{}, err
	}
	if _, err := runGit(environment.options.WorktreePath, "config", "user.name", "Fake provider"); err != nil {
		return execution.CompletedClaim{}, err
	}
	if _, err := runGit(environment.options.WorktreePath, "config", "user.email", "fake-provider@example.invalid"); err != nil {
		return execution.CompletedClaim{}, err
	}
	if _, err := runGit(environment.options.WorktreePath, "add", "RESULT.md"); err != nil {
		return execution.CompletedClaim{}, err
	}
	if _, err := runGit(environment.options.WorktreePath, "commit", "-m", "fixture: produce fake Candidate"); err != nil {
		return execution.CompletedClaim{}, err
	}
	sha, err := runGit(environment.options.WorktreePath, "rev-parse", "HEAD")
	if err != nil {
		return execution.CompletedClaim{}, err
	}
	return execution.CompletedClaim{
		ID: request.ClaimID, SchemaVersion: "director.agent-outcome.completed/v1",
		Outcome: "completed", AgentID: request.AgentID, CandidateSHA: sha,
		BaseSHA: request.BaseSHA, CriteriaResults: request.CriteriaResults,
		ResidualRiskCodes: []string{},
	}, nil
}

// SetOperational replaces the next periodic sample.
func (environment *Environment) SetOperational(observation execution.OperationalObservation) {
	environment.mu.Lock()
	defer environment.mu.Unlock()
	environment.operational = observation
}

// AgentRequest returns the exact top-level creation arguments.
func (environment *Environment) AgentRequest() host.Arguments {
	environment.mu.Lock()
	defer environment.mu.Unlock()
	return environment.agentRequest
}

// PromptRequest returns the only request allowed to start real Task work.
func (environment *Environment) PromptRequest() host.Arguments {
	environment.mu.Lock()
	defer environment.mu.Unlock()
	return environment.promptRequest
}

// TerminalEvent atomically changes the fake daemon fact before producing the
// callback which wakes reconciliation.
func (environment *Environment) TerminalEvent(kind execution.CompletionEventKind, effectID string, observedAtMillis int64) (execution.CompletionEvent, error) {
	environment.mu.Lock()
	defer environment.mu.Unlock()
	status := execution.ObservationStatus("")
	switch kind {
	case execution.CompletionEventFinished:
		status = execution.ObservationDesired
	case execution.CompletionEventError:
		status = execution.ObservationErrored
	case execution.CompletionEventPermission:
		status = execution.ObservationPermission
	default:
		return execution.CompletionEvent{}, errors.New("fake terminal event kind is invalid")
	}
	switch effectID {
	case environment.world.bootstrapEffectID:
		environment.world.bootstrapStatus = status
		environment.world.bootstrapObservedAtMillis = observedAtMillis
	case environment.world.promptEffectID:
		environment.world.promptStatus = status
		environment.world.promptObservedAtMillis = observedAtMillis
	default:
		return execution.CompletionEvent{}, errors.New("fake terminal event effect is unknown")
	}
	environment.eventSeq++
	event := execution.CompletionEvent{
		ID: fmt.Sprintf("completion-event-%d", environment.eventSeq), Kind: kind,
		AgentID: environment.world.agentID, DispatchEffectID: effectID,
		Cursor: environment.eventSeq, ObservedAtMillis: observedAtMillis,
		BindingHash: environment.world.bindingHash,
	}
	event.FactHash = execution.CompletionEventHash(event)
	return event, nil
}

// Enqueue implements the idempotent synchronous coordinator-wake port.
func (environment *Environment) Enqueue(_ context.Context, request reconciliationport.Request) (reconciliationport.Receipt, error) {
	environment.mu.Lock()
	defer environment.mu.Unlock()
	if prior, exists := environment.reconciliations[request.EventID]; exists {
		if prior.EventFactHash != request.EventFactHash {
			return reconciliationport.Receipt{}, errors.New("fake reconciliation deduplication conflict")
		}
		prior.Deduplicated = true
		return prior, nil
	}
	receipt := reconciliationport.Receipt{
		EventID: request.EventID, EventFactHash: request.EventFactHash,
		EnqueuedAtMillis: request.ReceivedAtMillis + 25,
	}
	environment.reconciliations[request.EventID] = receipt
	environment.reconciliationCount++
	return receipt, nil
}

func (environment *Environment) QueueWakeCount() int {
	environment.mu.Lock()
	defer environment.mu.Unlock()
	return environment.reconciliationCount
}

func (environment *Environment) MutationCount(kind execution.EffectKind) int {
	environment.mu.Lock()
	defer environment.mu.Unlock()
	return environment.mutations[kind]
}

func (environment *Environment) TotalMutationCount() int {
	environment.mu.Lock()
	defer environment.mu.Unlock()
	total := 0
	for _, count := range environment.mutations {
		total += count
	}
	return total
}

func (environment *Environment) MutationOrder() []string {
	environment.mu.Lock()
	defer environment.mu.Unlock()
	return append([]string(nil), environment.mutationOrder...)
}

func (environment *Environment) HostCallCount() int {
	environment.mu.Lock()
	defer environment.mu.Unlock()
	return environment.hostCalls
}

func (environment *Environment) SetupCount() int {
	return environment.MutationCount(execution.EffectSetupRun)
}

func (environment *Environment) WorktreePresent() bool {
	return pathPresent(environment.options.WorktreePath)
}

// RefreshObservationsAt advances only the fake world's replaceable external
// sample clock. Long crash-frontier tests call it before a new authoritative
// scan so a 30-second freshness policy is tested rather than wall-clock load.
func (environment *Environment) RefreshObservationsAt(nowMillis int64) {
	environment.mu.Lock()
	defer environment.mu.Unlock()
	if nowMillis > environment.operational.ObservedAtMillis {
		environment.operational.ObservedAtMillis = nowMillis
	}
}

// RecoveryFile reads one relative file from the owner-scoped private fake
// artifact. It is test evidence only and never enters a product projection.
func (environment *Environment) RecoveryFile(relative string) ([]byte, error) {
	environment.mu.Lock()
	defer environment.mu.Unlock()
	if !environment.world.recoveryReady || environment.world.recoveryPath == "" || filepath.IsAbs(relative) ||
		filepath.Clean(relative) != relative || strings.HasPrefix(relative, "..") {
		return nil, errors.New("fake recovery artifact file is unavailable")
	}
	return os.ReadFile(filepath.Join(environment.world.recoveryPath, relative))
}

// RemoveFixture is best-effort test teardown for a deliberately parked path.
func (environment *Environment) RemoveFixture() {
	if pathPresent(environment.options.WorktreePath) {
		_, _ = runGit(environment.options.SourcePath, "worktree", "remove", "--force", environment.options.WorktreePath)
	}
	environment.mu.Lock()
	recoveryPath := environment.world.recoveryPath
	environment.mu.Unlock()
	if recoveryPath != "" && filepath.Dir(recoveryPath) == filepath.Dir(environment.options.WorktreePath) &&
		strings.HasPrefix(filepath.Base(recoveryPath), "."+filepath.Base(environment.options.WorktreePath)+"-recovery-") {
		_ = os.RemoveAll(recoveryPath)
	}
}
