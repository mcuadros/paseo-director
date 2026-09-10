// SPDX-License-Identifier: Apache-2.0

package fake

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"strings"

	"github.com/mcuadros/director-engine/domain/execution"
	"github.com/mcuadros/director-engine/ports/host"
	runtimeport "github.com/mcuadros/director-engine/ports/runtime"
)

func (environment *RestartedEnvironment) ObserveHelperCapacity(ctx context.Context, scope execution.Scope, policy execution.HelperPolicy) (execution.HelperCapacityObservation, error) {
	return environment.world.ObserveHelperCapacity(ctx, scope, policy)
}
func (environment *RestartedEnvironment) ReserveHelperCapacity(ctx context.Context, request runtimeport.HelperCapacityReservation) (runtimeport.HelperCapacityReservationResult, error) {
	return environment.world.ReserveHelperCapacity(ctx, request)
}
func (environment *RestartedEnvironment) ReleaseHelperCapacity(ctx context.Context, id string, scope execution.Scope) error {
	return environment.world.ReleaseHelperCapacity(ctx, id, scope)
}
func (environment *RestartedEnvironment) ObserveHelperBoundary(ctx context.Context, request runtimeport.HelperRequest) (execution.HelperBoundaryObservation, error) {
	return environment.world.ObserveHelperBoundary(ctx, request)
}
func (environment *RestartedEnvironment) ObserveHelperEffect(ctx context.Context, request runtimeport.HelperRequest) (execution.EffectObservation, error) {
	return environment.world.ObserveHelperEffect(ctx, request)
}
func (environment *RestartedEnvironment) DispatchHelperEffect(ctx context.Context, request runtimeport.HelperRequest) error {
	return environment.world.DispatchHelperEffect(ctx, request)
}
func (environment *RestartedEnvironment) ObserveHelperContribution(ctx context.Context, request runtimeport.HelperRequest) (execution.HelperContributionObservation, error) {
	return environment.world.ObserveHelperContribution(ctx, request)
}

func (environment *Environment) exactHelperRequest(request runtimeport.HelperRequest) bool {
	return request.Scope == request.Helper.Scope && request.Scope.RunID != "" && request.Helper.ParentAgentID == environment.world.agentID &&
		request.BindingHash == environment.world.bindingHash && request.BindingHash == execution.RepositoryBindingSHA256(request.Repository) &&
		request.PrimaryWorktreePath == environment.options.WorktreePath && request.LifecycleDigest == execution.LifecycleDigest(request.LifecycleSurfaces) &&
		execution.AdmitLifecycle(request.Scope, request.LifecycleSurfaces, request.LifecycleApproval).Kind == execution.AdmissionAllow &&
		execution.AdmitIsolation(request.Isolation).Kind == execution.AdmissionAllow &&
		((request.Helper.Mode == execution.HelperWriter && request.Helper.WorktreePath != request.PrimaryWorktreePath) ||
			(request.Helper.Mode == execution.HelperReadOnly && request.Helper.WorktreePath == request.PrimaryWorktreePath))
}

func (environment *Environment) ObserveHelperCapacity(_ context.Context, scope execution.Scope, policy execution.HelperPolicy) (execution.HelperCapacityObservation, error) {
	environment.mu.Lock()
	defer environment.mu.Unlock()
	if scope.RunID == "" || !execution.ValidHelperPolicy(policy) {
		return execution.HelperCapacityObservation{}, errors.New("fake helper capacity scope is invalid")
	}
	active := environment.options.GlobalActiveAgents
	if active == 0 {
		active = 1
	}
	reserved := uint32(len(environment.helperReservations))
	for _, helper := range environment.helpers {
		if helper.agentActive {
			if _, alreadyReserved := environment.helperReservations["helper-capacity-"+helper.helperID]; alreadyReserved {
				continue
			}
			active++
		}
	}
	environment.observationSeq++
	observation := execution.HelperCapacityObservation{
		ID:               fmt.Sprintf("helper-capacity-%d", environment.observationSeq),
		ObservedAtMillis: environment.operational.ObservedAtMillis, MaximumAgeMillis: 30_000,
		ActiveAgents: active, ReservedAgents: reserved,
	}
	observation.FactHash = execution.HelperCapacityObservationHash(observation)
	return observation, nil
}

func (environment *Environment) ReserveHelperCapacity(_ context.Context, request runtimeport.HelperCapacityReservation) (runtimeport.HelperCapacityReservationResult, error) {
	environment.mu.Lock()
	defer environment.mu.Unlock()
	if existing, ok := environment.helperReservations[request.ID]; ok {
		if existing != request.Scope {
			return runtimeport.HelperCapacityReservationResult{}, errors.New("fake helper capacity idempotency conflict")
		}
		return runtimeport.HelperCapacityReservationResult{Applied: true, Replay: true}, nil
	}
	if request.ID == "" || request.Observation.FactHash != execution.HelperCapacityObservationHash(request.Observation) ||
		!execution.HelperCapacityAvailable(request.Observation, request.Policy, request.Observation.ObservedAtMillis) {
		return runtimeport.HelperCapacityReservationResult{}, errors.New("fake helper capacity request is invalid")
	}
	active := environment.options.GlobalActiveAgents
	if active == 0 {
		active = 1
	}
	for _, helper := range environment.helpers {
		if helper.agentActive {
			if _, alreadyReserved := environment.helperReservations["helper-capacity-"+helper.helperID]; alreadyReserved {
				continue
			}
			active++
		}
	}
	if request.Observation.ActiveAgents != active || request.Observation.ReservedAgents != uint32(len(environment.helperReservations)) ||
		uint64(active)+uint64(len(environment.helperReservations))+1 > uint64(request.Policy.MaximumConcurrentAgents) {
		return runtimeport.HelperCapacityReservationResult{Applied: false}, nil
	}
	environment.helperReservations[request.ID] = request.Scope
	return runtimeport.HelperCapacityReservationResult{Applied: true}, nil
}

func (environment *Environment) ReleaseHelperCapacity(_ context.Context, id string, scope execution.Scope) error {
	environment.mu.Lock()
	defer environment.mu.Unlock()
	existing, ok := environment.helperReservations[id]
	if !ok {
		return nil
	}
	if existing != scope {
		return errors.New("fake helper capacity release scope mismatch")
	}
	delete(environment.helperReservations, id)
	return nil
}

func (environment *Environment) helperWorld(helper execution.Helper) *helperWorld {
	current := environment.helpers[helper.ID]
	if current == nil {
		current = &helperWorld{helperID: helper.ID, parentAgentID: helper.ParentAgentID}
		environment.helpers[helper.ID] = current
	}
	return current
}

func (environment *Environment) ObserveHelperEffect(_ context.Context, request runtimeport.HelperRequest) (execution.EffectObservation, error) {
	environment.mu.Lock()
	defer environment.mu.Unlock()
	environment.observationSeq++
	if !environment.exactHelperRequest(request) {
		return effectObservation(request.Effect, execution.ObservationDifferent, "", request.BindingHash, environment.observationSeq, environment.operational.ObservedAtMillis), nil
	}
	world := environment.helperWorld(request.Helper)
	switch request.Effect.Kind {
	case execution.EffectHelperCheckoutCreate:
		if !pathPresent(request.Helper.WorktreePath) {
			return effectObservation(request.Effect, execution.ObservationAbsent, "", request.BindingHash, environment.observationSeq, environment.operational.ObservedAtMillis), nil
		}
		head, err := runGit(request.Helper.WorktreePath, "rev-parse", "HEAD")
		if err != nil || head != request.Repository.BaseSHA {
			return effectObservation(request.Effect, execution.ObservationDifferent, "", request.BindingHash, environment.observationSeq, environment.operational.ObservedAtMillis), nil
		}
		return effectObservation(request.Effect, execution.ObservationDesired, world.checkoutID, request.BindingHash, environment.observationSeq, environment.operational.ObservedAtMillis), nil
	case execution.EffectHelperBoundary:
		if !world.boundaryReady {
			return effectObservation(request.Effect, execution.ObservationAbsent, "", request.BindingHash, environment.observationSeq, environment.operational.ObservedAtMillis), nil
		}
		return effectObservation(request.Effect, execution.ObservationDesired, world.boundaryID, request.BindingHash, environment.observationSeq, environment.operational.ObservedAtMillis), nil
	case execution.EffectHelperCommitHandoff:
		if request.Helper.Contribution == nil || world.importedCommit != request.Helper.Contribution.CommitSHA {
			return effectObservation(request.Effect, execution.ObservationAbsent, "", request.BindingHash, environment.observationSeq, environment.operational.ObservedAtMillis), nil
		}
		if _, err := runGit(environment.options.SourcePath, "cat-file", "-e", world.importedCommit+"^{commit}"); err != nil {
			return effectObservation(request.Effect, execution.ObservationDifferent, "", request.BindingHash, environment.observationSeq, environment.operational.ObservedAtMillis), nil
		}
		return effectObservation(request.Effect, execution.ObservationDesired, world.importedCommit, request.BindingHash, environment.observationSeq, environment.operational.ObservedAtMillis), nil
	case execution.EffectHelperCheckoutRemove:
		if pathPresent(request.Helper.WorktreePath) {
			return effectObservation(request.Effect, execution.ObservationOwnedPresent, world.checkoutID, request.BindingHash, environment.observationSeq, environment.operational.ObservedAtMillis), nil
		}
		return effectObservation(request.Effect, execution.ObservationDesired, world.checkoutID, request.BindingHash, environment.observationSeq, environment.operational.ObservedAtMillis), nil
	default:
		return execution.EffectObservation{}, errors.New("fake helper effect is unsupported")
	}
}

func primaryCheckoutFact(path string) string {
	head, headErr := runGit(path, "rev-parse", "HEAD")
	status, statusErr := runGit(path, "status", "--porcelain=v1")
	if headErr != nil || statusErr != nil {
		return ""
	}
	return digest(struct{ Head, Status string }{head, status})
}

func (environment *Environment) DispatchHelperEffect(_ context.Context, request runtimeport.HelperRequest) error {
	environment.mu.Lock()
	defer environment.mu.Unlock()
	if !environment.exactHelperRequest(request) {
		return errors.New("fake helper request identity mismatch")
	}
	world := environment.helperWorld(request.Helper)
	switch request.Effect.Kind {
	case execution.EffectHelperCheckoutCreate:
		if request.Helper.Mode != execution.HelperWriter || pathPresent(request.Helper.WorktreePath) {
			return errors.New("fake writer checkout target is invalid")
		}
		if err := os.MkdirAll(filepath.Dir(request.Helper.WorktreePath), 0o700); err != nil {
			return err
		}
		if _, err := runGit(filepath.Dir(request.Helper.WorktreePath), "clone", "--no-local", "--no-checkout", environment.options.SourcePath, request.Helper.WorktreePath); err != nil {
			return err
		}
		if _, err := runGit(request.Helper.WorktreePath, "checkout", "-b", "helper/"+request.Helper.ID, request.Repository.BaseSHA); err != nil {
			return err
		}
		if _, err := runGit(request.Helper.WorktreePath, "remote", "remove", "origin"); err != nil {
			return err
		}
		world.checkoutID = externalID("helper-checkout", request.Effect.ID)
		world.primaryFactHash = primaryCheckoutFact(environment.options.WorktreePath)
	case execution.EffectHelperBoundary:
		if request.Helper.Mode == execution.HelperWriter && !pathPresent(request.Helper.WorktreePath) {
			return errors.New("fake helper boundary lacks isolated checkout")
		}
		world.boundaryReady = true
		world.boundaryID = externalID("helper-boundary", request.Effect.ID)
		if world.primaryFactHash == "" {
			world.primaryFactHash = primaryCheckoutFact(environment.options.WorktreePath)
		}
	case execution.EffectHelperCommitHandoff:
		if request.Helper.Contribution == nil || !world.boundaryReady || primaryCheckoutFact(environment.options.WorktreePath) != world.primaryFactHash {
			return errors.New("fake helper handoff lacks unchanged primary checkout")
		}
		if _, err := runGit(environment.options.SourcePath, "fetch", "--no-tags", "--no-write-fetch-head", request.Helper.WorktreePath, request.Helper.Contribution.CommitSHA); err != nil {
			return err
		}
		world.importedCommit = request.Helper.Contribution.CommitSHA
	case execution.EffectHelperCheckoutRemove:
		if !world.agentArchived || request.Helper.Handoff == nil || world.importedCommit != request.Helper.Handoff.CommitSHA || primaryCheckoutFact(environment.options.WorktreePath) != world.primaryFactHash {
			return errors.New("fake helper cleanup lacks termination or handoff proof")
		}
		if err := os.RemoveAll(request.Helper.WorktreePath); err != nil {
			return err
		}
	default:
		return errors.New("fake helper mutation is unsupported")
	}
	return environment.recordMutation(request.Effect.Kind)
}

func (environment *Environment) ObserveHelperBoundary(_ context.Context, request runtimeport.HelperRequest) (execution.HelperBoundaryObservation, error) {
	environment.mu.Lock()
	defer environment.mu.Unlock()
	if !environment.exactHelperRequest(request) {
		return execution.HelperBoundaryObservation{}, errors.New("fake helper boundary scope mismatch")
	}
	world := environment.helperWorld(request.Helper)
	environment.observationSeq++
	observation := execution.HelperBoundaryObservation{
		ID: fmt.Sprintf("helper-boundary-observation-%d", environment.observationSeq), HelperID: request.Helper.ID,
		Mode: request.Helper.Mode, ObservedAtMillis: environment.operational.ObservedAtMillis, MaximumAgeMillis: 30_000,
		Isolation: request.Isolation, LifecycleDigest: request.LifecycleDigest, TrustedRuntime: true,
		SeparateCheckout:           request.Helper.Mode == execution.HelperWriter && pathPresent(request.Helper.WorktreePath),
		PrimaryCheckoutReadOnly:    request.Helper.Mode == execution.HelperReadOnly,
		PrimaryCheckoutUnavailable: request.Helper.Mode == execution.HelperWriter,
		EngineStateAbsent:          true, TaskStoreCredentialAbsent: true, DeliveryCredentialAbsent: true,
		RawControlAbsent: true, ProviderCredentialOnly: true, OperationalTelemetryReady: world.boundaryReady,
		ProviderPolicyApplied: true, ProviderPolicyReadOnly: request.Helper.Mode == execution.HelperReadOnly,
	}
	observation.FactHash = execution.HelperBoundaryObservationHash(observation)
	return observation, nil
}

func (environment *Environment) ObserveHelperContribution(_ context.Context, request runtimeport.HelperRequest) (execution.HelperContributionObservation, error) {
	environment.mu.Lock()
	defer environment.mu.Unlock()
	if !environment.exactHelperRequest(request) || request.Helper.Contribution == nil {
		return execution.HelperContributionObservation{}, errors.New("fake helper contribution scope mismatch")
	}
	world := environment.helperWorld(request.Helper)
	environment.observationSeq++
	status, statusErr := runGit(request.Helper.WorktreePath, "status", "--porcelain=v1")
	head, headErr := runGit(request.Helper.WorktreePath, "rev-parse", "HEAD")
	_, reachableErr := runGit(request.Helper.WorktreePath, "cat-file", "-e", request.Helper.Contribution.CommitSHA+"^{commit}")
	_, ancestorErr := runGit(request.Helper.WorktreePath, "merge-base", "--is-ancestor", request.Repository.BaseSHA, request.Helper.Contribution.CommitSHA)
	_, alternatesErr := os.Stat(filepath.Join(request.Helper.WorktreePath, ".git", "objects", "info", "alternates"))
	observation := execution.HelperContributionObservation{
		ID: fmt.Sprintf("helper-contribution-observation-%d", environment.observationSeq), HelperID: request.Helper.ID,
		CommitSHA: request.Helper.Contribution.CommitSHA, BaseSHA: request.Helper.Contribution.BaseSHA,
		ObservedAtMillis: environment.operational.ObservedAtMillis, MaximumAgeMillis: 30_000,
		CheckoutID: world.checkoutID, BindingHash: request.BindingHash,
		Clean: statusErr == nil && status == "", Reachable: reachableErr == nil,
		Owned:            headErr == nil && head == request.Helper.Contribution.CommitSHA,
		DescendsFromBase: ancestorErr == nil, PrimaryCheckoutUnchanged: primaryCheckoutFact(environment.options.WorktreePath) == world.primaryFactHash,
		PrivateObjectStore: os.IsNotExist(alternatesErr),
	}
	observation.FactHash = execution.HelperContributionObservationHash(observation)
	return observation, nil
}

// SeedHelperAgent simulates only the Task Agent's admitted parent-bound native
// invocation. It is intentionally not called by the engine Controller.
func (environment *Environment) SeedHelperAgent(helper execution.Helper, labels map[string]string, workspaceID string) (string, error) {
	environment.mu.Lock()
	defer environment.mu.Unlock()
	if helper.Admission == nil || helper.Admission.ConsumedAtMillis == 0 || helper.ParentAgentID != environment.world.agentID ||
		workspaceID != environment.world.hostViewID || digest(labels) != helper.Admission.LabelDigest {
		return "", errors.New("fake helper invocation lacks consumed admission")
	}
	world := environment.helperWorld(helper)
	if world.agentID != "" {
		return world.agentID, nil
	}
	world.agentID = externalID("helper-agent", helper.AgentObservation.ID)
	world.parentAgentID = helper.ParentAgentID
	world.workspaceID = workspaceID
	world.labels = maps.Clone(labels)
	world.agentActive = true
	world.bootstrapDone = true
	return world.agentID, nil
}

func (environment *Environment) HelperRegistration(runScope execution.Scope, helper execution.Helper, rootWorkspaceID, workspaceID, baseSHA, profileSHA string) (host.HelperRegistration, error) {
	if helper.Admission == nil {
		return host.HelperRegistration{}, errors.New("helper admission is absent")
	}
	return host.HelperRegistration{RootWorkspaceID: rootWorkspaceID, ExecutionWorkspaceID: workspaceID, Scope: runScope,
		ParentAgentID: helper.ParentAgentID, BaseSHA: baseSHA, EffectID: helper.AgentObservation.ID,
		ProfileSHA256: profileSHA, SessionSHA256: helper.Admission.SessionSHA256,
		RegisteredAt: helper.Admission.RegisteredAt, StartedAt: helper.Admission.StartedAt}, nil
}

func (environment *Environment) ProduceHelperCommit(helper execution.Helper, name string) (string, error) {
	environment.mu.Lock()
	defer environment.mu.Unlock()
	if helper.Mode != execution.HelperWriter || !pathPresent(helper.WorktreePath) || strings.TrimSpace(name) == "" {
		return "", errors.New("fake writer helper is not ready")
	}
	if err := os.WriteFile(filepath.Join(helper.WorktreePath, name), []byte("helper contribution\n"), 0o600); err != nil {
		return "", err
	}
	for _, pair := range [][2]string{{"user.name", "Fake helper"}, {"user.email", "fake-helper@example.invalid"}} {
		if _, err := runGit(helper.WorktreePath, "config", pair[0], pair[1]); err != nil {
			return "", err
		}
	}
	if _, err := runGit(helper.WorktreePath, "add", "--", name); err != nil {
		return "", err
	}
	if _, err := runGit(helper.WorktreePath, "commit", "-m", "fixture: helper contribution"); err != nil {
		return "", err
	}
	return runGit(helper.WorktreePath, "rev-parse", "HEAD")
}

func (environment *Environment) HelperReservationCount() int {
	environment.mu.Lock()
	defer environment.mu.Unlock()
	return len(environment.helperReservations)
}
