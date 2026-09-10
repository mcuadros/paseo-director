// SPDX-License-Identifier: Apache-2.0

// Package review implements the engine-owned detached independent-review
// vertical. The Paseo and Git ports observe or perform one exact effect; all
// admission, retry, verdict, invalidation, and cleanup policy remains here.
package review

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/mcuadros/director-engine/domain"
	"github.com/mcuadros/director-engine/domain/agentbridge"
	"github.com/mcuadros/director-engine/domain/agentprofile"
	"github.com/mcuadros/director-engine/domain/candidate"
	domainconfig "github.com/mcuadros/director-engine/domain/configuration"
	"github.com/mcuadros/director-engine/domain/execution"
	domainreview "github.com/mcuadros/director-engine/domain/review"
	"github.com/mcuadros/director-engine/ports/host"
	reviewport "github.com/mcuadros/director-engine/ports/review"
)

var (
	ErrInvalidCommand       = errors.New("review command is invalid")
	ErrConcurrentTransition = errors.New("review transition lost an expected-version race")
	ErrReviewInvalidated    = errors.New("review binding is no longer current")
	ErrReviewAmbiguous      = errors.New("review external state is ambiguous")
	ErrReviewNotReady       = errors.New("review is not ready for this transition")
)

// Store is intentionally the exact aggregate subset required by review.
type Store interface {
	Project(context.Context, string) (domain.Project, error)
	Task(context.Context, string) (domain.Task, error)
	Run(context.Context, string) (domain.Run, error)
	Candidate(context.Context, string) (domain.Candidate, error)
	UpdateRun(context.Context, domain.CommandRequest, domain.Run, domain.Event) (domain.CommandResult, error)
}

type Service struct {
	store Store
	git   reviewport.CheckoutPort
	host  host.Port
}

func NewService(store Store, git reviewport.CheckoutPort, hostPort host.Port) (*Service, error) {
	if store == nil || git == nil || hostPort == nil {
		return nil, errors.New("review store, Git port, and host port are required")
	}
	return &Service{store: store, git: git, host: hostPort}, nil
}

type AdmitCommand struct {
	RunID              string
	ExpectedRunVersion uint64
	CoordinatorUUID    string
	ReviewOwnerUUID    string
	CISlotID           string
	ReviewerRoot       string
	CheckoutPath       string
	CriterionIDs       []string
	ProbePlan          []domainreview.Probe
	NowMillis          int64
}

type StepResult struct {
	Run          domain.Run
	Progressed   bool
	WaitingForCI bool
}

type AttemptCommand struct {
	RunID              string
	ExpectedRunVersion uint64
	CoordinatorUUID    string
	Reason             domainreview.AttemptReason
	NowMillis          int64
}

func stableID(prefix string, values ...string) string {
	digest := sha256.Sum256([]byte(strings.Join(values, "\x1f")))
	return prefix + "-" + hex.EncodeToString(digest[:16])
}

func stateHash(value execution.State) string {
	encoded, _ := json.Marshal(value)
	digest := sha256.Sum256(encoded)
	return hex.EncodeToString(digest[:])
}

func eventPayload(value any) json.RawMessage {
	encoded, err := json.Marshal(value)
	if err != nil {
		panic("marshal fixed review event: " + err.Error())
	}
	return encoded
}

func (service *Service) persist(ctx context.Context, current, next domain.Run, transition string) (domain.Run, error) {
	next.Version = current.Version + 1
	commandID := stableID("command", current.ID, fmt.Sprintf("version-%d", next.Version), transition)
	result, err := service.store.UpdateRun(ctx, domain.CommandRequest{
		IdempotencyKey: commandID, Type: transition, AggregateID: current.ID, ExpectedVersion: current.Version,
		Payload: eventPayload(struct{ Transition, StateSHA256 string }{transition, stateHash(next.Execution)}),
	}, next, domain.Event{
		ID: stableID("event", commandID), RunID: current.ID, Sequence: current.Version + 2,
		AggregateID: current.ID, AggregateVersion: next.Version, Type: transition,
		Payload: eventPayload(struct{ ReviewKey, BindingSHA256 string }{
			next.Execution.Review.ReviewKey, next.Execution.Review.Binding.BindingSHA256,
		}),
	})
	if err != nil {
		return current, err
	}
	if result.Outcome != domain.CommandApplied || result.Replay {
		return current, ErrConcurrentTransition
	}
	return next, nil
}

func bindingFor(task domain.Task, run domain.Run, record domain.Candidate, command AdmitCommand) domainreview.Binding {
	manifest := record.Manifest
	return domainreview.SealBinding(domainreview.Binding{
		TaskID: task.ID, RunID: run.ID, CandidateID: record.ID,
		CandidateSHA: record.CommitSHA, BaseSHA: manifest.BaseSHA, TreeSHA: manifest.TreeSHA,
		ManifestSHA256: manifest.BindingSHA256, AcceptanceSHA256: manifest.AcceptanceSHA256,
		ConfigurationSHA256: manifest.ConfigurationSHA256, ProfileSHA256: manifest.ProfileSHA256,
		ContextSHA256: manifest.ContextSHA256, DecisionsSHA256: manifest.DecisionsSHA256,
		FindingsSHA256: manifest.FindingsSHA256, CandidateGeneration: run.Execution.CandidateAuthority.Generation,
		CISlotID: command.CISlotID, TaskAgentUUID: run.Execution.Agent.ExternalID,
		CoordinatorUUID: command.CoordinatorUUID, ReviewOwnerUUID: command.ReviewOwnerUUID,
	})
}

func currentProjectLease(project domain.Project, run domain.Run, nowMillis int64) bool {
	lease := project.Lease
	binding := run.Execution.LeaseBinding
	return project.ID == run.Execution.Scope.ProjectID && project.State == "active" && lease != nil &&
		domain.ValidateProjectLease(lease) == nil && execution.ValidLeaseBinding(binding) && lease.DispatchAllowed &&
		lease.HolderInstance == binding.HolderInstance && lease.HolderProcessIdentity == binding.HolderProcessIdentity &&
		lease.Epoch == binding.Epoch && nowMillis >= lease.AcquiredAtMillis && nowMillis < lease.ExpiresAtMillis
}

func profile(value agentprofile.FrozenRole, wholeSHA string) domainreview.Profile {
	return domainreview.Profile{Provider: string(value.Selection.Provider), Model: value.Selection.Model, SHA256: wholeSHA, Supported: true}
}

func validAdmitPaths(run domain.Run, command AdmitCommand) bool {
	return filepath.IsAbs(command.ReviewerRoot) && filepath.Clean(command.ReviewerRoot) == command.ReviewerRoot &&
		filepath.IsAbs(command.CheckoutPath) && filepath.Clean(command.CheckoutPath) == command.CheckoutPath &&
		command.CheckoutPath != run.Execution.WorktreePath && command.CheckoutPath != run.Execution.SourcePath &&
		filepath.Dir(command.CheckoutPath) == command.ReviewerRoot
}

// Admit records the one review intent. Candidate, profile, Task Agent, and
// coordinator facts are reread; no external effect occurs in this call.
func (service *Service) Admit(ctx context.Context, command AdmitCommand) (StepResult, error) {
	run, err := service.store.Run(ctx, command.RunID)
	if err != nil {
		return StepResult{}, err
	}
	if run.Version != command.ExpectedRunVersion || run.CurrentCandidateID == "" || run.Execution.CandidateAuthority == nil ||
		run.Execution.CandidateAuthority.Invalidated || !candidate.ValidAuthority(*run.Execution.CandidateAuthority) ||
		run.Execution.CandidateAuthority.Downstream.Review != nil || !domainreview.ValidUUID(command.CoordinatorUUID) ||
		!domainreview.ValidUUID(command.ReviewOwnerUUID) || !validAdmitPaths(run, command) || command.NowMillis < 0 {
		return StepResult{Run: run}, ErrInvalidCommand
	}
	if run.Execution.Review != nil {
		return StepResult{Run: run}, ErrInvalidCommand
	}
	task, err := service.store.Task(ctx, run.TaskID)
	if err != nil {
		return StepResult{}, err
	}
	project, err := service.store.Project(ctx, task.ProjectID)
	if err != nil {
		return StepResult{}, err
	}
	record, err := service.store.Candidate(ctx, run.CurrentCandidateID)
	if err != nil {
		return StepResult{}, err
	}
	if !currentProjectLease(project, run, command.NowMillis) || record.RunID != run.ID || record.ID != run.Execution.CandidateAuthority.CandidateID ||
		record.Manifest.BindingSHA256 != run.Execution.CandidateAuthority.BindingSHA256 ||
		record.CommitSHA != run.Execution.CandidateAuthority.CandidateSHA || task.Version != run.Execution.CandidateAuthority.TaskVersion ||
		run.Execution.Agent.Phase != execution.EffectComplete || run.Execution.Agent.ExternalID == "" ||
		!domainreview.ValidUUID(run.Execution.Agent.ExternalID) || !candidate.ValidManifest(record.Manifest) ||
		run.Execution.EffectiveProfiles == nil || !run.Execution.EffectiveProfiles.Valid() ||
		!execution.ValidPrimarySession(run.Execution.PrimarySession) ||
		run.Execution.PrimarySession.NativeAgentID != run.Execution.Agent.ExternalID ||
		run.Execution.PrimarySession.BindingSHA256 == "" ||
		!run.Execution.PreparationReady || run.Execution.PreparationBarrierHash == "" ||
		run.Execution.Boundary.Phase != execution.EffectComplete || run.Execution.Boundary.ExternalID == "" ||
		run.Execution.OperationalObservation == nil ||
		execution.AdmitIsolation(run.Execution.Isolation).Kind != execution.AdmissionAllow ||
		execution.EvaluateOperationalLimits(run.Execution.OperationalPolicy, *run.Execution.OperationalObservation, command.NowMillis).Kind != execution.AdmissionAllow {
		return StepResult{Run: run}, ErrInvalidCommand
	}
	worker, workerOK := run.Execution.EffectiveProfiles.Role(agentprofile.RoleWorker)
	reviewer, reviewerOK := run.Execution.EffectiveProfiles.Role(agentprofile.RoleReviewer)
	if !workerOK || !reviewerOK || reviewer.Selection.PermissionMode != "read-only" ||
		!slices.Equal(reviewer.Selection.MCPCapabilities, []domainconfig.MCPCapability{
			domainconfig.MCPCandidateRead, domainconfig.MCPReviewVerdictSubmit,
		}) {
		return StepResult{Run: run}, ErrInvalidCommand
	}
	profileDecision := domainreview.EvaluateProfile(run.Execution.ReviewPolicy,
		profile(worker, run.Execution.EffectiveProfilesSHA256), profile(reviewer, run.Execution.EffectiveProfilesSHA256))
	binding := bindingFor(task, run, record, command)
	state, ok := domainreview.NewState(binding, command.CriterionIDs, command.ProbePlan, profileDecision)
	if !ok || !validAdmitPaths(run, command) {
		return StepResult{Run: run}, ErrInvalidCommand
	}
	state.SourcePath = run.Execution.SourcePath
	state.PrimaryPath = run.Execution.WorktreePath
	state.ReviewerRoot = command.ReviewerRoot
	state.CheckoutPath = command.CheckoutPath
	state.PrimaryHeadSHA = record.CommitSHA
	next := run
	next.Execution.Review = &state
	next, err = service.persist(ctx, run, next, "review.intent_recorded")
	return StepResult{Run: next, Progressed: err == nil}, err
}

func currentBinding(task domain.Task, run domain.Run, record domain.Candidate, state domainreview.State) bool {
	authority := run.Execution.CandidateAuthority
	return authority != nil && candidate.ValidAuthority(*authority) && !authority.Invalidated &&
		run.CurrentCandidateID == record.ID && record.RunID == run.ID && task.Version == authority.TaskVersion &&
		authority.CandidateID == state.Binding.CandidateID && authority.Generation == state.Binding.CandidateGeneration &&
		authority.BindingSHA256 == state.Binding.ManifestSHA256 && record.Manifest.BindingSHA256 == state.Binding.ManifestSHA256 &&
		record.CommitSHA == state.Binding.CandidateSHA && record.Manifest.BaseSHA == state.Binding.BaseSHA &&
		record.Manifest.TreeSHA == state.Binding.TreeSHA && record.Manifest.AcceptanceSHA256 == state.Binding.AcceptanceSHA256 &&
		record.Manifest.ConfigurationSHA256 == state.Binding.ConfigurationSHA256 && record.Manifest.ProfileSHA256 == state.Binding.ProfileSHA256 &&
		record.Manifest.ContextSHA256 == state.Binding.ContextSHA256 && record.Manifest.DecisionsSHA256 == state.Binding.DecisionsSHA256 &&
		record.Manifest.FindingsSHA256 == state.Binding.FindingsSHA256 && domainreview.Current(state, state.Binding)
}

func (service *Service) loadCurrent(ctx context.Context, runID string, nowMillis int64) (domain.Task, domain.Run, domain.Candidate, error) {
	run, err := service.store.Run(ctx, runID)
	if err != nil {
		return domain.Task{}, domain.Run{}, domain.Candidate{}, err
	}
	if run.Execution.Review == nil || run.CurrentCandidateID == "" {
		return domain.Task{}, run, domain.Candidate{}, ErrReviewNotReady
	}
	task, err := service.store.Task(ctx, run.TaskID)
	if err != nil {
		return domain.Task{}, run, domain.Candidate{}, err
	}
	project, err := service.store.Project(ctx, task.ProjectID)
	if err != nil {
		return domain.Task{}, run, domain.Candidate{}, err
	}
	record, err := service.store.Candidate(ctx, run.CurrentCandidateID)
	if err != nil {
		return domain.Task{}, run, domain.Candidate{}, err
	}
	if !currentProjectLease(project, run, nowMillis) || !currentBinding(task, run, record, *run.Execution.Review) {
		return task, run, record, ErrReviewInvalidated
	}
	return task, run, record, nil
}

func checkoutRequest(state domainreview.State) reviewport.CheckoutRequest {
	return reviewport.CheckoutRequest{
		ReviewKey: state.ReviewKey, BindingSHA256: state.Binding.BindingSHA256,
		SourcePath: state.SourcePath, PrimaryPath: state.PrimaryPath, ReviewerRoot: state.ReviewerRoot,
		CheckoutPath: state.CheckoutPath, CandidateSHA: state.Binding.CandidateSHA, TreeSHA: state.Binding.TreeSHA,
		PrimaryHeadSHA: state.PrimaryHeadSHA, OwnerUUID: state.Binding.ReviewOwnerUUID,
	}
}

func hostObservation(command host.Command, observation host.Observation, err error) (host.Observation, error) {
	if err != nil {
		return host.Observation{}, err
	}
	if err := host.ValidateObservation(command, observation); err != nil {
		return host.Observation{}, ErrReviewAmbiguous
	}
	return observation, nil
}

func (service *Service) invoke(ctx context.Context, command host.Command, nowMillis int64) (host.Observation, error) {
	descriptor, err := service.host.Describe(ctx)
	if err != nil || host.ValidateDescriptor(descriptor) != nil {
		return host.Observation{}, ErrReviewAmbiguous
	}
	observation, invokeErr := service.host.Invoke(ctx, command)
	validated, err := hostObservation(command, observation, invokeErr)
	if err != nil {
		return host.Observation{}, err
	}
	observedAt, parseErr := time.Parse(time.RFC3339Nano, validated.ObservedAt)
	observedAtMillis := observedAt.UnixMilli()
	if parseErr != nil || observedAtMillis > nowMillis ||
		nowMillis-observedAtMillis > validated.Result.MaximumAgeMillis {
		return host.Observation{}, ErrReviewAmbiguous
	}
	return validated, nil
}

func observationCommand(state domainreview.State, effect domainreview.Effect, capability host.Capability) host.Command {
	return host.Command{
		RequestID: effect.ID + "-observe", IdempotencyKey: effect.ID + "-observe", ExpectedVersion: 0,
		AfterCursor: effect.Cursor,
		Capability:  capability, Arguments: host.Arguments{
			Scope:    execution.Scope{ProjectID: "project-" + state.Binding.RunID, WorkspaceID: "workspace-" + state.Binding.TaskID, TaskID: state.Binding.TaskID, RunID: state.Binding.RunID},
			EffectID: effect.ID, WorktreePath: state.CheckoutPath, WorkspaceID: state.Workspace.ExternalID,
			AgentID: state.ReviewerUUID, BindingHash: state.Binding.BindingSHA256,
		},
	}
}

// hostScope replaces the intentionally non-authoritative placeholder scope in
// observationCommand with the exact persisted Run scope.
func hostScope(command host.Command, scope execution.Scope) host.Command {
	command.Arguments.Scope = scope
	return command
}

func reviewerRole(run domain.Run) (agentprofile.FrozenRole, bool) {
	if run.Execution.EffectiveProfiles == nil {
		return agentprofile.FrozenRole{}, false
	}
	return run.Execution.EffectiveProfiles.Role(agentprofile.RoleReviewer)
}

func reviewerSession(state domainreview.State, run domain.Run, role agentprofile.FrozenRole) (*host.AgentProfile, *host.MCPSession, string, error) {
	tools, err := agentbridge.Catalog(role)
	if err != nil {
		return nil, nil, "", err
	}
	toolNames := make([]string, 0, len(tools))
	for _, tool := range tools {
		toolNames = append(toolNames, tool.Name)
	}
	sort.Strings(toolNames)
	sessionHash := stableID("review-session", state.Binding.BindingSHA256, string(role.Selection.Provider), role.Selection.Model)
	// Host schema requires a full SHA-256, not a display ID.
	sessionDigest := sha256.Sum256([]byte(sessionHash))
	sessionHash = hex.EncodeToString(sessionDigest[:])
	profile := &host.AgentProfile{
		Provider: string(role.Selection.Provider), Model: role.Selection.Model, Effort: role.Selection.Effort,
		Mode: role.Selection.Mode, PermissionMode: role.Selection.PermissionMode, SHA256: run.Execution.EffectiveProfilesSHA256,
	}
	for _, option := range role.Selection.ProviderOptions {
		profile.ProviderOptions = append(profile.ProviderOptions, host.ProviderOption{Name: string(option.Name), Value: option.Value})
	}
	primary := run.Execution.PrimarySession
	session := &host.MCPSession{
		ContractVersion: primary.MCPContractVersion, ContractHash: primary.MCPContractSHA256,
		SessionSHA256: sessionHash, Role: string(agentprofile.RoleReviewer), Provider: profile.Provider, Model: profile.Model,
		Tools: toolNames, Server: host.MCPServer{Name: primary.MCPServer.Name, Command: primary.MCPServer.Command,
			Args: slices.Clone(primary.MCPServer.Args), Env: map[string]string{}},
	}
	return profile, session, sessionHash, nil
}

func reviewTitle(task domain.Task) string { return "Independent review: " + task.Title }

func registration(state domainreview.State, run domain.Run, sessionHash string, instant string) (host.WorkerRegistration, error) {
	if state.RegisteredAt != "" {
		instant = state.RegisteredAt
	}
	registration := host.WorkerRegistration{
		RootWorkspaceID: run.Execution.RootWorkspaceID, ExecutionWorkspaceID: state.Workspace.ExternalID,
		Scope: run.Execution.Scope, Role: host.WorkerRoleReviewer, Phase: host.WorkerPhaseReviewing,
		CandidateSHA: state.Binding.CandidateSHA, BaseSHA: state.Binding.BaseSHA, EffectID: state.Bootstrap.ID,
		ProfileSHA256: run.Execution.EffectiveProfilesSHA256, SessionSHA256: sessionHash,
		RegisteredAt: instant, StartedAt: instant,
	}
	_, err := host.WorkerLabels(registration)
	return registration, err
}

func promptText(task domain.Task, state domainreview.State) (string, string, error) {
	if state.CIObservation == nil || !domainreview.ValidCIObservation(*state.CIObservation, state.Binding) {
		return "", "", ErrReviewNotReady
	}
	if len(state.Attempts) == 0 {
		return "", "", ErrInvalidCommand
	}
	prompt := struct {
		SchemaVersion      string                     `json:"schemaVersion"`
		ClaimSchemaVersion string                     `json:"claimSchemaVersion"`
		HarnessVersion     uint32                     `json:"harnessVersion"`
		ReviewerUUID       string                     `json:"reviewerUuid"`
		Attempt            domainreview.Attempt       `json:"attempt"`
		Objective          string                     `json:"objective"`
		AcceptanceCriteria string                     `json:"acceptanceCriteria"`
		Binding            domainreview.Binding       `json:"binding"`
		Matrix             []domainreview.MatrixEntry `json:"matrix"`
		ProbePlan          []domainreview.Probe       `json:"probePlan"`
		CI                 domainreview.CIObservation `json:"ci"`
		Authority          struct {
			ReadOnly             bool     `json:"readOnly"`
			AllowedMCP           []string `json:"allowedMcp"`
			CompleteCIForbidden  bool     `json:"completeCiForbidden"`
			LifecycleEffectsNone bool     `json:"lifecycleEffectsNone"`
			BlockingProbePolicy  string   `json:"blockingProbePolicy"`
		} `json:"authority"`
		Warning string `json:"warning,omitempty"`
	}{
		SchemaVersion: "director.review-prompt/v1", ClaimSchemaVersion: domainreview.ClaimSchemaVersion,
		HarnessVersion: 2, ReviewerUUID: state.ReviewerUUID, Attempt: state.Attempts[len(state.Attempts)-1],
		Objective: task.Objective, AcceptanceCriteria: task.AcceptanceCriteria, Binding: state.Binding,
		Matrix: slices.Clone(state.Matrix), ProbePlan: slices.Clone(state.ProbePlan), CI: *state.CIObservation,
		Warning: state.ProfileDecision.Warning,
	}
	prompt.Authority.ReadOnly = true
	prompt.Authority.AllowedMCP = []string{"director_candidate_read", "director_review_verdict_submit"}
	prompt.Authority.CompleteCIForbidden = true
	prompt.Authority.LifecycleEffectsNone = true
	prompt.Authority.BlockingProbePolicy = "smallest_reproducible_case"
	encoded, err := json.Marshal(prompt)
	if err != nil || len(encoded) > 64*1024 {
		return "", "", ErrInvalidCommand
	}
	digest := sha256.Sum256(encoded)
	return string(encoded), hex.EncodeToString(digest[:]), nil
}

func setAmbiguous(state *domainreview.State, effect *domainreview.Effect) {
	effect.Phase = domainreview.EffectAmbiguous
	state.Invalidated = true
	state.InvalidationCode = "external_state_ambiguous"
}

func recordHostObservation(effect *domainreview.Effect, observation host.Observation) {
	effect.Cursor = observation.Cursor
}

// Reconcile advances at most one external review effect. Every dispatch is
// preceded by a durable dispatching state. Lost responses are observed on the
// next call and never trigger a blind duplicate.
func (service *Service) Reconcile(ctx context.Context, runID, coordinatorUUID string, nowMillis int64) (StepResult, error) {
	task, run, _, err := service.loadCurrent(ctx, runID, nowMillis)
	if err != nil {
		return StepResult{Run: run}, err
	}
	state := *run.Execution.Review
	if coordinatorUUID != state.Binding.CoordinatorUUID || !domainreview.ValidUUID(coordinatorUUID) {
		return StepResult{Run: run}, ErrInvalidCommand
	}

	if state.Checkout.Phase != domainreview.EffectComplete {
		observation, observeErr := service.git.ObserveCheckout(ctx, checkoutRequest(state))
		if observeErr != nil || observation.Status == reviewport.CheckoutAmbiguous || observation.Status == reviewport.CheckoutDifferent {
			next := run
			setAmbiguous(&state, &state.Checkout)
			next.Execution.Review = &state
			next, persistErr := service.persist(ctx, run, next, "review.checkout_ambiguous")
			if persistErr != nil {
				return StepResult{Run: next}, persistErr
			}
			return StepResult{Run: next, Progressed: true}, ErrReviewAmbiguous
		}
		if observation.Status == reviewport.CheckoutExact {
			state.Checkout.Phase = domainreview.EffectComplete
			state.Checkout.ExternalID = state.ReviewKey
			state.Checkout.FactSHA256 = observation.Evidence.FactSHA256
			state.CheckoutEvidence = &observation.Evidence
			next := run
			next.Execution.Review = &state
			next, err = service.persist(ctx, run, next, "review.checkout_observed")
			return StepResult{Run: next, Progressed: err == nil}, err
		}
		if state.Checkout.Phase == domainreview.EffectDispatching {
			return StepResult{Run: run}, ErrReviewAmbiguous
		}
		next := run
		state.Checkout.Phase = domainreview.EffectDispatching
		next.Execution.Review = &state
		next, err = service.persist(ctx, run, next, "review.checkout_dispatching")
		if err != nil {
			return StepResult{Run: next}, err
		}
		dispatchErr := service.git.CreateCheckout(ctx, checkoutRequest(state))
		return StepResult{Run: next, Progressed: true}, dispatchErr
	}

	if state.Workspace.Phase != domainreview.EffectComplete {
		observe := hostScope(observationCommand(state, state.Workspace, host.CapabilityWorkspaceObserve), run.Execution.Scope)
		observe.Arguments.EffectKind = execution.EffectHostViewCreate
		observe.Arguments.Title = reviewTitle(task)
		observe.Arguments.WorktreeID = state.ReviewKey
		observe.Arguments.WorktreePath = state.CheckoutPath
		observe.Arguments.LifecycleDigest = run.Execution.LifecycleDigest
		hostFact, observeErr := service.invoke(ctx, observe, nowMillis)
		result := hostFact.Result
		if observeErr != nil || !slices.Contains([]execution.ObservationStatus{
			execution.ObservationAbsent, execution.ObservationDesired, execution.ObservationOwnedPresent,
		}, result.Status) {
			next := run
			if observeErr == nil {
				recordHostObservation(&state.Workspace, hostFact)
			}
			setAmbiguous(&state, &state.Workspace)
			next.Execution.Review = &state
			next, persistErr := service.persist(ctx, run, next, "review.workspace_ambiguous")
			if persistErr != nil {
				return StepResult{Run: next}, persistErr
			}
			return StepResult{Run: next, Progressed: true}, ErrReviewAmbiguous
		}
		if result.Status == execution.ObservationDesired || result.Status == execution.ObservationOwnedPresent {
			if !domainreview.ValidIdentifier(result.ExternalID) {
				return StepResult{Run: run}, ErrReviewAmbiguous
			}
			state.Workspace.Phase = domainreview.EffectComplete
			state.Workspace.ExternalID = result.ExternalID
			state.Workspace.FactSHA256 = result.FactHash
			recordHostObservation(&state.Workspace, hostFact)
			next := run
			next.Execution.Review = &state
			next, err = service.persist(ctx, run, next, "review.workspace_observed")
			return StepResult{Run: next, Progressed: err == nil}, err
		}
		if state.Workspace.Phase == domainreview.EffectDispatching {
			return StepResult{Run: run}, ErrReviewAmbiguous
		}
		next := run
		state.Workspace.Phase = domainreview.EffectDispatching
		next.Execution.Review = &state
		next, err = service.persist(ctx, run, next, "review.workspace_dispatching")
		if err != nil {
			return StepResult{Run: next}, err
		}
		dispatch := observe
		dispatch.RequestID = state.Workspace.ID + "-dispatch"
		dispatch.IdempotencyKey = dispatch.RequestID
		dispatch.Capability = host.CapabilityWorkspaceCreate
		dispatch.Arguments.EffectKind = execution.EffectHostViewCreate
		_, dispatchErr := service.invoke(ctx, dispatch, nowMillis)
		return StepResult{Run: next, Progressed: true}, dispatchErr
	}

	role, ok := reviewerRole(run)
	if !ok {
		return StepResult{Run: run}, ErrInvalidCommand
	}
	profile, session, sessionHash, sessionErr := reviewerSession(state, run, role)
	if sessionErr != nil {
		return StepResult{Run: run}, ErrInvalidCommand
	}
	instant := time.UnixMilli(nowMillis).UTC().Format(time.RFC3339Nano)
	registry, registryErr := registration(state, run, sessionHash, instant)
	if registryErr != nil {
		return StepResult{Run: run}, ErrInvalidCommand
	}

	if state.Bootstrap.Phase != domainreview.EffectComplete {
		observe := hostScope(observationCommand(state, state.Bootstrap, host.CapabilityAgentObserve), run.Execution.Scope)
		observe.Arguments.EffectKind = execution.EffectReviewerAgentCreate
		labels, labelErr := host.WorkerLabels(registry)
		if labelErr != nil {
			return StepResult{Run: run}, ErrInvalidCommand
		}
		observe.Arguments.WorktreeID = state.ReviewKey
		observe.Arguments.WorktreePath = state.CheckoutPath
		observe.Arguments.WorkspaceID = state.Workspace.ExternalID
		observe.Arguments.Title = reviewTitle(task)
		observe.Arguments.ClientMessageID = stableID("message", state.Bootstrap.ID)
		observe.Arguments.Profile = profile
		observe.Arguments.Session = session
		observe.Arguments.Labels = labels
		hostFact, observeErr := service.invoke(ctx, observe, nowMillis)
		result := hostFact.Result
		if observeErr != nil || !slices.Contains([]execution.ObservationStatus{
			execution.ObservationAbsent, execution.ObservationDesired, execution.ObservationOwnedPresent,
		}, result.Status) {
			next := run
			if observeErr == nil {
				recordHostObservation(&state.Bootstrap, hostFact)
			}
			setAmbiguous(&state, &state.Bootstrap)
			next.Execution.Review = &state
			next, persistErr := service.persist(ctx, run, next, "review.bootstrap_ambiguous")
			if persistErr != nil {
				return StepResult{Run: next}, persistErr
			}
			return StepResult{Run: next, Progressed: true}, ErrReviewAmbiguous
		}
		if result.Status == execution.ObservationDesired {
			if !domainreview.ValidUUID(result.ExternalID) || result.ExternalID == state.Binding.TaskAgentUUID || result.ExternalID == state.Binding.CoordinatorUUID ||
				state.RegistrationSHA256 == "" || result.CorrelationHash != state.RegistrationSHA256 {
				next := run
				recordHostObservation(&state.Bootstrap, hostFact)
				setAmbiguous(&state, &state.Bootstrap)
				next.Execution.Review = &state
				next, persistErr := service.persist(ctx, run, next, "review.bootstrap_identity_changed")
				if persistErr != nil {
					return StepResult{Run: next}, persistErr
				}
				return StepResult{Run: next, Progressed: true}, ErrReviewAmbiguous
			}
			state.Bootstrap.Phase = domainreview.EffectComplete
			state.Bootstrap.ExternalID = result.ExternalID
			state.Bootstrap.FactSHA256 = result.FactHash
			recordHostObservation(&state.Bootstrap, hostFact)
			state.ReviewerUUID = result.ExternalID
			state.ReviewerSessionSHA256 = sessionHash
			next := run
			next.Execution.Review = &state
			next, err = service.persist(ctx, run, next, "review.bootstrap_identity_observed")
			return StepResult{Run: next, Progressed: err == nil}, err
		}
		if result.Status == execution.ObservationOwnedPresent {
			return StepResult{Run: run}, nil
		}
		if state.Bootstrap.Phase == domainreview.EffectDispatching {
			return StepResult{Run: run}, ErrReviewAmbiguous
		}
		registrationDigest, digestErr := host.RegistrationDigest(registry)
		if digestErr != nil {
			return StepResult{Run: run}, ErrInvalidCommand
		}
		dispatch := host.Command{
			RequestID: state.Bootstrap.ID + "-dispatch", IdempotencyKey: state.Bootstrap.ID, ExpectedVersion: run.Version,
			Capability: host.CapabilityReviewerAgentCreate, Arguments: host.Arguments{
				Scope: run.Execution.Scope, EffectKind: execution.EffectReviewerAgentCreate, EffectID: state.Bootstrap.ID,
				WorktreeID: state.ReviewKey, WorktreePath: state.CheckoutPath, WorkspaceID: state.Workspace.ExternalID,
				Title: reviewTitle(task), InitialPrompt: host.ZeroWorkBootstrapPrompt,
				ParentAgentID: nil, Profile: profile, Session: session, Labels: labels, BindingHash: state.Binding.BindingSHA256,
				LifecycleDigest: run.Execution.LifecycleDigest, IsolationDigest: run.Execution.IsolationDigest,
				PreparationReady: run.Execution.PreparationReady, PreparationBarrierHash: run.Execution.PreparationBarrierHash,
				ClientMessageID: stableID("message", state.Bootstrap.ID), BoundaryID: run.Execution.Boundary.ExternalID,
				OperationalObservationID: run.Execution.OperationalObservation.ID,
			},
		}
		if err := host.AdmitAgentCreate(dispatch, registry); err != nil {
			return StepResult{Run: run}, ErrInvalidCommand
		}
		next := run
		state.Bootstrap.Phase = domainreview.EffectDispatching
		state.RegisteredAt = instant
		state.RegistrationSHA256 = registrationDigest
		next.Execution.Review = &state
		next, err = service.persist(ctx, run, next, "review.bootstrap_dispatching")
		if err != nil {
			return StepResult{Run: next}, err
		}
		_, dispatchErr := service.invoke(ctx, dispatch, nowMillis)
		return StepResult{Run: next, Progressed: true}, dispatchErr
	}

	if state.CIObservation == nil {
		return StepResult{Run: run, WaitingForCI: true}, nil
	}
	if state.Prompt.Phase != domainreview.EffectComplete {
		observe := hostScope(observationCommand(state, state.Prompt, host.CapabilityAgentObserve), run.Execution.Scope)
		observe.Arguments.EffectKind = execution.EffectAgentPrompt
		observe.Arguments.WorktreeID = state.ReviewKey
		observe.Arguments.WorktreePath = state.CheckoutPath
		observe.Arguments.WorkspaceID = state.Workspace.ExternalID
		observe.Arguments.AgentID = state.ReviewerUUID
		observe.Arguments.Title = reviewTitle(task)
		observe.Arguments.ClientMessageID = stableID("message", state.Prompt.ID)
		observe.Arguments.Profile = profile
		observe.Arguments.Session = session
		hostFact, observeErr := service.invoke(ctx, observe, nowMillis)
		result := hostFact.Result
		if observeErr == nil && result.Status == execution.ObservationDesired && result.ExternalID == state.ReviewerUUID {
			state.Prompt.Phase = domainreview.EffectComplete
			state.Prompt.ExternalID = result.ExternalID
			state.Prompt.FactSHA256 = result.FactHash
			recordHostObservation(&state.Prompt, hostFact)
			next := run
			next.Execution.Review = &state
			next, err = service.persist(ctx, run, next, "review.prompt_observed")
			return StepResult{Run: next, Progressed: err == nil}, err
		}
		if observeErr == nil && result.Status == execution.ObservationOwnedPresent {
			return StepResult{Run: run}, nil
		}
		if observeErr != nil || result.Status == execution.ObservationDesired && result.ExternalID != state.ReviewerUUID ||
			!slices.Contains([]execution.ObservationStatus{
				execution.ObservationAbsent, execution.ObservationDesired, execution.ObservationOwnedPresent,
			}, result.Status) || state.Prompt.Phase == domainreview.EffectDispatching {
			// A separately notified prompt is nonrepeatable after possible handoff.
			next := run
			if observeErr == nil {
				recordHostObservation(&state.Prompt, hostFact)
			}
			setAmbiguous(&state, &state.Prompt)
			next.Execution.Review = &state
			next, persistErr := service.persist(ctx, run, next, "review.prompt_ambiguous")
			if persistErr != nil {
				return StepResult{Run: next}, persistErr
			}
			return StepResult{Run: next, Progressed: true}, ErrReviewAmbiguous
		}
		prompt, promptHash, promptErr := promptText(task, state)
		if promptErr != nil {
			return StepResult{Run: run}, promptErr
		}
		dispatch := host.Command{
			RequestID: state.Prompt.ID + "-dispatch", IdempotencyKey: state.Prompt.ID, ExpectedVersion: run.Version,
			Capability: host.CapabilityAgentPrompt, Arguments: host.Arguments{
				Scope: run.Execution.Scope, EffectKind: execution.EffectAgentPrompt, EffectID: state.Prompt.ID,
				WorktreeID: state.ReviewKey, WorktreePath: state.CheckoutPath, WorkspaceID: state.Workspace.ExternalID,
				AgentID: state.ReviewerUUID, Title: reviewTitle(task), InitialPrompt: prompt, ParentAgentID: nil, NotifyOnFinish: true,
				ClientMessageID: stableID("message", state.Prompt.ID), Profile: profile, Session: session,
				SessionBindingSHA256: sessionHash, BindingHash: state.Binding.BindingSHA256,
			},
		}
		if err := host.AdmitAgentPrompt(dispatch, registry, state.ReviewerUUID); err != nil {
			return StepResult{Run: run}, ErrInvalidCommand
		}
		next := run
		state.Prompt.Phase = domainreview.EffectDispatching
		state.PromptSHA256 = promptHash
		next.Execution.Review = &state
		next, err = service.persist(ctx, run, next, "review.prompt_dispatching")
		if err != nil {
			return StepResult{Run: next}, err
		}
		_, dispatchErr := service.invoke(ctx, dispatch, nowMillis)
		return StepResult{Run: next, Progressed: true}, dispatchErr
	}
	return StepResult{Run: run}, nil
}

// ObserveCI attaches one and only one authoritative complete CI observation.
// A different observation invalidates approval instead of replacing the old
// CI under the existing review key.
func (service *Service) ObserveCI(ctx context.Context, runID, coordinatorUUID string, observation domainreview.CIObservation, nowMillis int64) (StepResult, error) {
	_, run, _, err := service.loadCurrent(ctx, runID, nowMillis)
	if err != nil {
		return StepResult{Run: run}, err
	}
	state := *run.Execution.Review
	if nowMillis < observation.ObservedAtMillis || coordinatorUUID != state.Binding.CoordinatorUUID ||
		!domainreview.ValidCIObservation(observation, state.Binding) {
		return StepResult{Run: run}, ErrInvalidCommand
	}
	if state.CIObservation != nil {
		if state.CIObservation.SHA256 == observation.SHA256 {
			return StepResult{Run: run}, nil
		}
		state = domainreview.Invalidate(state, "ci_observation_changed")
		if run.Execution.CandidateAuthority != nil {
			run.Execution.CandidateAuthority.Downstream.Review = nil
		}
		next := run
		next.Execution.Review = &state
		next, persistErr := service.persist(ctx, run, next, "review.ci_changed")
		return StepResult{Run: next, Progressed: persistErr == nil}, errors.Join(ErrReviewInvalidated, persistErr)
	}
	state.CIObservation = &observation
	next := run
	next.Execution.Review = &state
	next, err = service.persist(ctx, run, next, "review.ci_observed")
	return StepResult{Run: next, Progressed: err == nil}, err
}

// AdmitAttempt records one deterministic retry namespace. The engine caller
// must already have classified the failure as pre-dispatch/pre-start; this
// method cannot resend a possible handoff or create another Reviewer.
func (service *Service) AdmitAttempt(ctx context.Context, command AttemptCommand) (StepResult, error) {
	_, run, _, err := service.loadCurrent(ctx, command.RunID, command.NowMillis)
	if err != nil {
		return StepResult{Run: run}, err
	}
	state := *run.Execution.Review
	if run.Version != command.ExpectedRunVersion || command.CoordinatorUUID != state.Binding.CoordinatorUUID ||
		command.Reason == domainreview.AttemptInitial || state.Evidence != nil ||
		command.Reason == domainreview.AttemptProviderPreDispatch && state.Bootstrap.Phase == domainreview.EffectComplete ||
		command.Reason == domainreview.AttemptHarnessPreStart && state.Prompt.Phase == domainreview.EffectComplete {
		return StepResult{Run: run}, ErrInvalidCommand
	}
	attempts, ok := domainreview.AdmitAttempt(state.Attempts, state.Binding, command.Reason)
	if !ok {
		return StepResult{Run: run}, ErrInvalidCommand
	}
	state.Attempts = attempts
	next := run
	next.Execution.Review = &state
	next, err = service.persist(ctx, run, next, "review.attempt_admitted")
	return StepResult{Run: next, Progressed: err == nil}, err
}

// SubmitClaim admits one exact bounded verdict and attaches only a current
// Candidate Review binding. It cannot publish, integrate, clean, or close.
func (service *Service) SubmitClaim(ctx context.Context, runID string, claim domainreview.Claim, nowMillis int64) (StepResult, error) {
	_, run, _, err := service.loadCurrent(ctx, runID, nowMillis)
	if err != nil {
		return StepResult{Run: run}, err
	}
	state := *run.Execution.Review
	if nowMillis < 0 || state.Prompt.Phase != domainreview.EffectComplete || state.CheckoutEvidence == nil || state.CIObservation == nil ||
		claim.Checkout.FactSHA256 != state.CheckoutEvidence.FactSHA256 || claim.CIObservation.SHA256 != state.CIObservation.SHA256 ||
		!domainreview.AttemptAdmitted(state.Attempts, state.Binding, claim.AttemptReason, claim.AttemptKey) ||
		!domainreview.ValidateClaim(claim, state.Binding, state.ReviewerUUID, *state.CIObservation, state.Matrix, state.ProbePlan) {
		return StepResult{Run: run}, ErrInvalidCommand
	}
	if state.Evidence != nil {
		return StepResult{Run: run}, ErrInvalidCommand
	}
	evidence := domainreview.EvidenceFromClaim(claim, nowMillis)
	state.Evidence = &evidence
	state.VerdictDurablyObserved = true
	next := run
	next.Execution.Review = &state
	authority := *next.Execution.CandidateAuthority
	authority.Downstream.Review = &candidate.EvidenceBinding{
		ID: evidence.ID, CandidateID: authority.CandidateID, CandidateSHA: authority.CandidateSHA,
		BaseSHA: authority.BaseSHA, Generation: authority.Generation, BindingSHA256: authority.BindingSHA256,
	}
	next.Execution.CandidateAuthority = &authority
	next, err = service.persist(ctx, run, next, "review.verdict_observed")
	return StepResult{Run: next, Progressed: err == nil}, err
}

func cleanupHostCommand(run domain.Run, task domain.Task, state domainreview.State, effect domainreview.Effect, observe bool, agent bool) host.Command {
	capability := host.CapabilityAgentArchive
	if observe {
		capability = host.CapabilityAgentObserve
	}
	if !agent {
		capability = host.CapabilityWorkspaceArchive
		if observe {
			capability = host.CapabilityWorkspaceObserve
		}
	}
	kind := execution.EffectReviewerAgentArchive
	if !agent {
		kind = execution.EffectHostViewArchive
	}
	return host.Command{
		RequestID:      effect.ID + map[bool]string{true: "-observe", false: "-dispatch"}[observe],
		IdempotencyKey: effect.ID, ExpectedVersion: run.Version, Capability: capability,
		Arguments: host.Arguments{Scope: run.Execution.Scope, EffectKind: kind, EffectID: effect.ID,
			WorktreeID: state.ReviewKey, WorktreePath: state.CheckoutPath, WorkspaceID: state.Workspace.ExternalID,
			AgentID: state.ReviewerUUID, Title: reviewTitle(task), BindingHash: state.Binding.BindingSHA256},
	}
}

// Cleanup advances only reviewer-owned resources and only after the verdict
// evidence is durably visible. Different or ambiguous observations preserve
// every remaining resource.
func (service *Service) Cleanup(ctx context.Context, runID, coordinatorUUID string, nowMillis int64) (StepResult, error) {
	task, run, _, err := service.loadCurrent(ctx, runID, nowMillis)
	if err != nil {
		return StepResult{Run: run}, err
	}
	state := *run.Execution.Review
	if !domainreview.CleanupEligible(state, coordinatorUUID) {
		return StepResult{Run: run}, ErrReviewNotReady
	}

	for _, item := range []struct {
		effect     *domainreview.Effect
		agent      bool
		transition string
	}{
		{&state.AgentCleanup, true, "review.agent_cleanup"}, {&state.WorkspaceCleanup, false, "review.workspace_cleanup"},
	} {
		if item.effect.Phase == domainreview.EffectComplete {
			continue
		}
		if item.effect.Phase == domainreview.EffectAmbiguous {
			return StepResult{Run: run}, ErrReviewAmbiguous
		}
		observe := cleanupHostCommand(run, task, state, *item.effect, true, item.agent)
		hostFact, observeErr := service.invoke(ctx, observe, nowMillis)
		result := hostFact.Result
		expectedExternalID := map[bool]string{true: state.ReviewerUUID, false: state.Workspace.ExternalID}[item.agent]
		if observeErr != nil || result.Status == execution.ObservationDesired && result.ExternalID != expectedExternalID ||
			(result.Status != execution.ObservationDesired && result.Status != execution.ObservationOwnedPresent) {
			if observeErr == nil {
				recordHostObservation(item.effect, hostFact)
			}
			item.effect.Phase = domainreview.EffectAmbiguous
			next := run
			next.Execution.Review = &state
			next, persistErr := service.persist(ctx, run, next, item.transition+"_ambiguous")
			if persistErr != nil {
				return StepResult{Run: next}, persistErr
			}
			return StepResult{Run: next, Progressed: true}, ErrReviewAmbiguous
		}
		if result.Status == execution.ObservationDesired {
			item.effect.Phase = domainreview.EffectComplete
			item.effect.FactSHA256 = result.FactHash
			recordHostObservation(item.effect, hostFact)
			next := run
			next.Execution.Review = &state
			next, err = service.persist(ctx, run, next, item.transition+"_observed")
			return StepResult{Run: next, Progressed: err == nil}, err
		}
		if item.effect.Phase == domainreview.EffectDispatching {
			return StepResult{Run: run}, ErrReviewAmbiguous
		}
		item.effect.Phase = domainreview.EffectDispatching
		next := run
		next.Execution.Review = &state
		next, err = service.persist(ctx, run, next, item.transition+"_dispatching")
		if err != nil {
			return StepResult{Run: next}, err
		}
		_, dispatchErr := service.invoke(ctx, cleanupHostCommand(next, task, state, *item.effect, false, item.agent), nowMillis)
		return StepResult{Run: next, Progressed: true}, dispatchErr
	}

	if state.CheckoutCleanup.Phase != domainreview.EffectComplete {
		if state.CheckoutCleanup.Phase == domainreview.EffectAmbiguous {
			return StepResult{Run: run}, ErrReviewAmbiguous
		}
		observation, observeErr := service.git.ObserveCheckout(ctx, checkoutRequest(state))
		if observeErr != nil || observation.Status == reviewport.CheckoutAmbiguous || observation.Status == reviewport.CheckoutDifferent {
			state.CheckoutCleanup.Phase = domainreview.EffectAmbiguous
			next := run
			next.Execution.Review = &state
			next, persistErr := service.persist(ctx, run, next, "review.checkout_cleanup_ambiguous")
			if persistErr != nil {
				return StepResult{Run: next}, persistErr
			}
			return StepResult{Run: next, Progressed: true}, ErrReviewAmbiguous
		}
		if observation.Status == reviewport.CheckoutAbsent {
			state.CheckoutCleanup.Phase = domainreview.EffectComplete
			next := run
			next.Execution.Review = &state
			next, err = service.persist(ctx, run, next, "review.checkout_cleanup_observed")
			return StepResult{Run: next, Progressed: err == nil}, err
		}
		if state.CheckoutCleanup.Phase == domainreview.EffectDispatching {
			return StepResult{Run: run}, ErrReviewAmbiguous
		}
		state.CheckoutCleanup.Phase = domainreview.EffectDispatching
		next := run
		next.Execution.Review = &state
		next, err = service.persist(ctx, run, next, "review.checkout_cleanup_dispatching")
		if err != nil {
			return StepResult{Run: next}, err
		}
		dispatchErr := service.git.RemoveCheckout(ctx, checkoutRequest(state), *state.CheckoutEvidence)
		return StepResult{Run: next, Progressed: true}, dispatchErr
	}
	return StepResult{Run: run}, nil
}
