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
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"

	"github.com/mcuadros/director-engine/domain/execution"
	"github.com/mcuadros/director-engine/ports/host"
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
}

type world struct {
	worktreeID       string
	boundaryID       string
	hostViewID       string
	agentID          string
	boundaryReady    bool
	setupComplete    bool
	hostViewActive   bool
	agentActive      bool
	agentArchived    bool
	hostViewArchived bool
}

// Environment implements both the non-host runtime port and the one host port
// with fake, observable effects over an owned disposable Git repository.
type Environment struct {
	mu             sync.Mutex
	options        Options
	world          world
	operational    execution.OperationalObservation
	observationSeq uint64
	mutations      map[execution.EffectKind]int
	mutationOrder  []string
	lost           map[execution.EffectKind]bool
	hostCalls      int
	agentRequest   host.Arguments
}

var _ runtimeport.Port = (*Environment)(nil)
var _ host.Port = (*Environment)(nil)

// RestartedEnvironment is a new policy-free adapter instance over the same
// fake external world. It models a connector/engine process replacement:
// in-memory controller state is gone while observable external facts survive.
type RestartedEnvironment struct {
	world *Environment
}

var _ runtimeport.Port = (*RestartedEnvironment)(nil)
var _ host.Port = (*RestartedEnvironment)(nil)

// NewEnvironment creates an empty fake world. All mutations remain scoped to
// Options.WorktreePath and the disposable source repository.
func NewEnvironment(options Options) *Environment {
	return &Environment{
		options: options, operational: options.Operational,
		mutations: make(map[execution.EffectKind]int),
		lost:      make(map[execution.EffectKind]bool),
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

func (environment *Environment) exactRequest(request runtimeport.Request) bool {
	return request.Scope.RunID != "" && request.BindingHash != "" &&
		request.SourcePath == environment.options.SourcePath &&
		request.WorktreePath == environment.options.WorktreePath &&
		request.Branch == environment.options.Branch && request.BaseSHA == environment.options.BaseSHA
}

func effectObservation(effect execution.Effect, status execution.ObservationStatus, external, bindingHash string, sequence uint64) execution.EffectObservation {
	observation := execution.EffectObservation{
		ID: fmt.Sprintf("observation-%d", sequence), EffectID: effect.ID,
		Status: status, ExternalID: external, BindingHash: bindingHash,
		PriorDispatcherAbsent: true, ObservedAtMillis: 1_000, MaximumAgeMillis: 30_000,
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
		return effectObservation(request.Effect, execution.ObservationDifferent, "", request.BindingHash, environment.observationSeq), nil
	}
	switch request.Effect.Kind {
	case execution.EffectWorktreeCreate:
		if !pathPresent(environment.options.WorktreePath) {
			return effectObservation(request.Effect, execution.ObservationAbsent, "", request.BindingHash, environment.observationSeq), nil
		}
		return effectObservation(request.Effect, execution.ObservationDesired, environment.world.worktreeID, request.BindingHash, environment.observationSeq), nil
	case execution.EffectBoundaryMaterialize:
		if !environment.world.boundaryReady {
			return effectObservation(request.Effect, execution.ObservationAbsent, "", request.BindingHash, environment.observationSeq), nil
		}
		return effectObservation(request.Effect, execution.ObservationDesired, environment.world.boundaryID, request.BindingHash, environment.observationSeq), nil
	case execution.EffectSetupRun:
		if !environment.world.setupComplete {
			return effectObservation(request.Effect, execution.ObservationAbsent, "", request.BindingHash, environment.observationSeq), nil
		}
		return effectObservation(request.Effect, execution.ObservationDesired, externalID("setup", request.Effect.ID), request.BindingHash, environment.observationSeq), nil
	case execution.EffectWorktreeRemove:
		if pathPresent(environment.options.WorktreePath) {
			return effectObservation(request.Effect, execution.ObservationOwnedPresent, environment.world.worktreeID, request.BindingHash, environment.observationSeq), nil
		}
		return effectObservation(request.Effect, execution.ObservationDesired, environment.world.worktreeID, request.BindingHash, environment.observationSeq), nil
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
		if !environment.world.agentArchived || !environment.world.hostViewArchived || request.WorktreeID != environment.world.worktreeID {
			return errors.New("fake worktree cleanup lacks exact terminal ownership facts")
		}
		if _, err := runGit(environment.options.SourcePath, "worktree", "remove", environment.options.WorktreePath); err != nil {
			return err
		}
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
			BaseSHA: request.Claim.BaseSHA, ObservedAtMillis: 1_000, MaximumAgeMillis: 30_000,
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
		BaseSHA: request.Claim.BaseSHA, ObservedAtMillis: 1_000, MaximumAgeMillis: 30_000,
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

func hostObservation(command host.Command, status execution.ObservationStatus, external string, sequence uint64) host.Observation {
	result := host.ObservationResult{
		EffectID: command.Arguments.EffectID, Status: status, ExternalID: external,
		BindingHash: command.Arguments.BindingHash, PriorDispatcherAbsent: true,
		MaximumAgeMillis: 30_000,
	}
	result.FactHash = host.ObservationResultHash(result)
	return host.Observation{
		RequestID: command.RequestID, Cursor: sequence,
		ObservedAt: "1970-01-01T00:00:01Z", Result: result,
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
	case host.CapabilityWorkspaceObserve:
		if arguments.EffectKind == execution.EffectHostViewArchive {
			if environment.world.hostViewArchived {
				return hostObservation(command, execution.ObservationDesired, environment.world.hostViewID, environment.observationSeq), nil
			}
			if environment.world.hostViewActive {
				return hostObservation(command, execution.ObservationOwnedPresent, environment.world.hostViewID, environment.observationSeq), nil
			}
			return hostObservation(command, execution.ObservationAmbiguous, "", environment.observationSeq), nil
		}
		if environment.world.hostViewActive {
			return hostObservation(command, execution.ObservationDesired, environment.world.hostViewID, environment.observationSeq), nil
		}
		return hostObservation(command, execution.ObservationAbsent, "", environment.observationSeq), nil
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
			arguments.ParentAgentID != nil || arguments.Title == "" || arguments.InitialPrompt == "" ||
			arguments.IsolationDigest == "" || arguments.WorkspaceID != environment.world.hostViewID {
			return host.Observation{}, errors.New("fake Task Agent create contract is incomplete")
		}
		environment.agentRequest = arguments
		environment.world.agentID = externalID("agent", arguments.EffectID)
		environment.world.agentActive = true
		return host.Observation{}, environment.recordMutation(arguments.EffectKind)
	case host.CapabilityAgentObserve:
		if arguments.EffectKind == execution.EffectAgentArchive {
			if environment.world.agentArchived {
				return hostObservation(command, execution.ObservationDesired, environment.world.agentID, environment.observationSeq), nil
			}
			if environment.world.agentActive {
				return hostObservation(command, execution.ObservationOwnedPresent, environment.world.agentID, environment.observationSeq), nil
			}
			return hostObservation(command, execution.ObservationAmbiguous, "", environment.observationSeq), nil
		}
		if environment.world.agentActive {
			return hostObservation(command, execution.ObservationDesired, environment.world.agentID, environment.observationSeq), nil
		}
		return hostObservation(command, execution.ObservationAbsent, "", environment.observationSeq), nil
	case host.CapabilityAgentArchive:
		if arguments.AgentID != environment.world.agentID || !environment.world.agentActive {
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
	if !environment.world.agentActive || request.AgentID != environment.world.agentID || !environment.world.boundaryReady {
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

// RemoveFixture is best-effort test teardown for a deliberately parked path.
func (environment *Environment) RemoveFixture() {
	if pathPresent(environment.options.WorktreePath) {
		_, _ = runGit(environment.options.SourcePath, "worktree", "remove", "--force", environment.options.WorktreePath)
	}
}
