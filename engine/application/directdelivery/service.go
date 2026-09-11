// SPDX-License-Identifier: Apache-2.0

// Package directdelivery implements the engine-owned direct-delivery
// vertical. The Git port observes or performs one exact ref effect; every
// policy, human-action, retry, invalidation, and completion decision stays in
// this application/domain boundary.
package directdelivery

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"

	"github.com/mcuadros/director-engine/domain"
	"github.com/mcuadros/director-engine/domain/candidate"
	domainconfig "github.com/mcuadros/director-engine/domain/configuration"
	"github.com/mcuadros/director-engine/domain/correction"
	directdomain "github.com/mcuadros/director-engine/domain/directdelivery"
	"github.com/mcuadros/director-engine/domain/execution"
	feedbackdomain "github.com/mcuadros/director-engine/domain/feedback"
	"github.com/mcuadros/director-engine/domain/review"
	directport "github.com/mcuadros/director-engine/ports/directdelivery"
	deliveryreducer "github.com/mcuadros/director-engine/reducer/delivery"
)

var (
	ErrInvalidCommand       = errors.New("direct-delivery command is invalid")
	ErrConcurrentTransition = errors.New("direct-delivery transition lost an expected-version race")
	ErrNotReady             = errors.New("direct delivery is not ready")
	ErrFallbackForbidden    = errors.New("pull-request delivery cannot fall back to direct delivery")
	ErrExternalAmbiguous    = errors.New("direct-delivery external state is ambiguous")
)

type Store interface {
	Project(context.Context, string) (domain.Project, error)
	Task(context.Context, string) (domain.Task, error)
	Run(context.Context, string) (domain.Run, error)
	Candidate(context.Context, string) (domain.Candidate, error)
	UpdateRun(context.Context, domain.CommandRequest, domain.Run, domain.Event) (domain.CommandResult, error)
}

type Service struct {
	store  Store
	remote directport.Port
	gate   sync.Mutex
	locks  map[string]*keyedLock
}

type keyedLock struct {
	mutex sync.Mutex
	users uint32
}

func NewService(store Store, remote directport.Port) (*Service, error) {
	if store == nil || remote == nil {
		return nil, errors.New("direct-delivery store and remote port are required")
	}
	return &Service{store: store, remote: remote, locks: make(map[string]*keyedLock)}, nil
}

func (service *Service) lock(key string) func() {
	service.gate.Lock()
	lock := service.locks[key]
	if lock == nil {
		lock = &keyedLock{}
		service.locks[key] = lock
	}
	lock.users++
	service.gate.Unlock()
	lock.mutex.Lock()
	return func() {
		lock.mutex.Unlock()
		service.gate.Lock()
		lock.users--
		if lock.users == 0 {
			delete(service.locks, key)
		}
		service.gate.Unlock()
	}
}

type AdmitCommand struct {
	RunID              string
	ExpectedRunVersion uint64
	LeaseEpoch         uint64
	Policy             directdomain.Policy
	NowMillis          int64
}

type AuthorizeManualCommand struct {
	RunID              string
	ExpectedRunVersion uint64
	LeaseEpoch         uint64
	Actor              AuthenticatedHumanActor
	Authorization      directdomain.HumanAuthorization
	NowMillis          int64
}

// AuthenticatedHumanActor is fixed by the trusted Paseo server ingress. It is
// deliberately distinct from GitHub review/comment and feedback identities.
type AuthenticatedHumanActor struct {
	Kind          string
	ID            string
	SessionID     string
	Source        string
	Authenticated bool
}

func validAuthenticatedHuman(actor AuthenticatedHumanActor, authorization directdomain.HumanAuthorization) bool {
	return actor.Kind == "human" && actor.ID != "" && actor.SessionID != "" && actor.Source == "server" && actor.Authenticated &&
		authorization.ActorKind == actor.Kind && authorization.ActorID == actor.ID &&
		authorization.ActorSource == directdomain.AuthorizationActorSource && authorization.Authenticated
}

type StepCommand struct {
	RunID              string
	ExpectedRunVersion uint64
	LeaseEpoch         uint64
	NowMillis          int64
}

type Result struct {
	Run             domain.Run
	Progressed      bool
	Replayed        bool
	WaitingHuman    bool
	WaitingExternal bool
	Integrated      bool
	Code            string
}

type current struct {
	project   domain.Project
	task      domain.Task
	run       domain.Run
	candidate domain.Candidate
}

func stableID(prefix string, values ...string) string {
	sum := sha256.Sum256([]byte(strings.Join(values, "\x1f")))
	return prefix + "-" + hex.EncodeToString(sum[:16])
}

func payload(value any) json.RawMessage {
	encoded, err := json.Marshal(value)
	if err != nil {
		panic("marshal fixed direct-delivery event: " + err.Error())
	}
	return encoded
}

func stateSHA256(state *directdomain.State) string {
	encoded, _ := json.Marshal(state)
	sum := sha256.Sum256(encoded)
	return hex.EncodeToString(sum[:])
}

func currentLease(project domain.Project, run domain.Run, leaseEpoch uint64, nowMillis int64) bool {
	lease := project.Lease
	binding := run.Execution.LeaseBinding
	return project.ID == run.Execution.Scope.ProjectID && project.State == "active" && lease != nil &&
		domain.ValidateProjectLease(lease) == nil && execution.ValidLeaseBinding(binding) && lease.DispatchAllowed &&
		lease.Epoch == leaseEpoch && binding.Epoch == leaseEpoch && lease.HolderInstance == binding.HolderInstance &&
		lease.HolderProcessIdentity == binding.HolderProcessIdentity && nowMillis >= lease.AcquiredAtMillis && nowMillis < lease.ExpiresAtMillis
}

func (service *Service) load(ctx context.Context, runID string) (current, error) {
	run, err := service.store.Run(ctx, runID)
	if err != nil {
		return current{}, err
	}
	task, err := service.store.Task(ctx, run.TaskID)
	if err != nil {
		return current{run: run}, err
	}
	project, err := service.store.Project(ctx, task.ProjectID)
	if err != nil {
		return current{run: run, task: task}, err
	}
	if run.CurrentCandidateID == "" {
		return current{project: project, run: run, task: task}, ErrNotReady
	}
	record, err := service.store.Candidate(ctx, run.CurrentCandidateID)
	return current{project: project, run: run, task: task, candidate: record}, err
}

func currentCandidate(value current) bool {
	authority := value.run.Execution.CandidateAuthority
	record := value.candidate
	return authority != nil && !authority.Invalidated && candidate.ValidAuthority(*authority) && candidate.ValidManifest(record.Manifest) &&
		record.ID == value.run.CurrentCandidateID && record.RunID == value.run.ID && authority.CandidateID == record.ID &&
		authority.CandidateSHA == record.CommitSHA && authority.BaseSHA == value.run.BaseSHA &&
		authority.BindingSHA256 == record.Manifest.BindingSHA256 && authority.TaskVersion == value.task.Version &&
		authority.ConfigurationSHA256 == record.Manifest.ConfigurationSHA256 && authority.DecisionsSHA256 == record.Manifest.DecisionsSHA256 &&
		authority.FindingsSHA256 == record.Manifest.FindingsSHA256 && value.run.Execution.BaseRef == record.Claim.BaseRef &&
		value.run.Execution.Branch == record.Claim.Branch && value.run.Execution.RepositoryBindingHash == record.Manifest.RepositoryBindingSHA256
}

func sameEvidence(binding *candidate.EvidenceBinding, authority *candidate.Authority, id string) bool {
	return binding != nil && authority != nil && binding.ID == id && binding.CandidateID == authority.CandidateID &&
		binding.CandidateSHA == authority.CandidateSHA && binding.BaseSHA == authority.BaseSHA &&
		binding.Generation == authority.Generation && binding.BindingSHA256 == authority.BindingSHA256
}

func reviewApproved(value current) bool {
	state := value.run.Execution.Review
	authority := value.run.Execution.CandidateAuthority
	if state == nil || authority == nil || !review.ValidState(*state) || state.Invalidated || state.Evidence == nil ||
		state.Evidence.Verdict != review.VerdictApproveCandidate || !review.ValidEvidence(*state.Evidence, *state) ||
		state.CIObservation == nil || !review.ValidCIObservation(*state.CIObservation, state.Binding) ||
		state.Binding.CandidateID != authority.CandidateID || state.Binding.CandidateSHA != authority.CandidateSHA ||
		state.Binding.BaseSHA != authority.BaseSHA || state.Binding.ManifestSHA256 != authority.BindingSHA256 ||
		state.Binding.CandidateGeneration != authority.Generation || state.Binding.TaskAgentUUID != value.run.Execution.Agent.ExternalID {
		return false
	}
	return sameEvidence(authority.Downstream.Review, authority, state.Evidence.ID)
}

func ciPassed(value current) bool {
	state := value.run.Execution.Review
	return state != nil && state.CIObservation != nil && state.Evidence != nil &&
		state.CIObservation.Status == "passed" && state.CIObservation.Authoritative && state.CIObservation.Complete &&
		state.Evidence.CIObservationSHA256 == state.CIObservation.SHA256
}

func correctionSettled(value current) bool {
	if value.run.Execution.Feedback != nil && feedbackdomain.BlocksDelivery(*value.run.Execution.Feedback) {
		return false
	}
	state := value.run.Execution.Correction
	if state == nil {
		return true
	}
	authority := value.run.Execution.CandidateAuthority
	if authority == nil || !correction.ValidState(*state) || state.Phase != correction.PhaseGatesRequired ||
		state.CurrentCandidateID != authority.CandidateID || state.CurrentCandidateSHA != authority.CandidateSHA || len(state.Gates) == 0 {
		return false
	}
	gate := state.Gates[len(state.Gates)-1]
	return gate.CandidateID == authority.CandidateID && gate.CandidateSHA == authority.CandidateSHA &&
		gate.CandidateGeneration == authority.Generation && gate.FreshCIRequired && gate.FreshReviewRequired && gate.PriorAuthorityInvalidated
}

func currentBinding(value current, policy directdomain.Policy) (directdomain.Binding, bool) {
	if !currentCandidate(value) || !reviewApproved(value) || !ciPassed(value) || !correctionSettled(value) ||
		value.run.Execution.DeliveryMode != domainconfig.DeliveryDirect || !directdomain.ValidPolicy(policy) ||
		value.run.Execution.PublicationPolicy != nil || value.run.Execution.Publication != nil || len(value.run.Execution.PublicationHistory) != 0 ||
		value.run.Execution.Review == nil || value.run.Execution.Review.Evidence == nil ||
		value.run.Execution.Review.CIObservation == nil || policy.ConfigurationSHA256 != value.candidate.Manifest.ConfigurationSHA256 ||
		value.candidate.Claim.BaseRef != value.run.Execution.BaseRef || !directdomain.TargetAuthorized(policy, value.candidate.Claim.BaseRef) {
		return directdomain.Binding{}, false
	}
	authority := value.run.Execution.CandidateAuthority
	reviewState := value.run.Execution.Review
	binding := directdomain.SealBinding(directdomain.Binding{TaskID: value.task.ID, RunID: value.run.ID,
		CandidateID: value.candidate.ID, CandidateSHA: value.candidate.CommitSHA, BaseSHA: value.candidate.Manifest.BaseSHA,
		TreeSHA: value.candidate.Manifest.TreeSHA, ManifestSHA256: value.candidate.Manifest.BindingSHA256,
		CandidateGeneration: authority.Generation, TaskVersion: value.task.Version,
		ConfigurationSHA256:     value.candidate.Manifest.ConfigurationSHA256,
		RepositoryID:            value.run.Execution.RepositoryBinding.RepositoryID,
		RepositoryBindingSHA256: value.run.Execution.RepositoryBindingHash, TargetRef: value.candidate.Claim.BaseRef,
		PolicySHA256: policy.SHA256, ReviewEvidenceID: reviewState.Evidence.ID,
		ReviewerUUID: reviewState.Evidence.ReviewerUUID, CIObservationID: reviewState.CIObservation.ID,
		CIObservationSHA256: reviewState.CIObservation.SHA256, LeaseEpoch: value.run.Execution.LeaseBinding.Epoch})
	return binding, directdomain.ValidBinding(binding)
}

func (service *Service) persist(ctx context.Context, currentRun, next domain.Run, transition string) (domain.Run, error) {
	next.Version = currentRun.Version + 1
	commandID := stableID("command", currentRun.ID, fmt.Sprintf("version-%d", next.Version), transition)
	result, err := service.store.UpdateRun(ctx, domain.CommandRequest{IdempotencyKey: commandID, Type: transition,
		AggregateID: currentRun.ID, ExpectedVersion: currentRun.Version,
		Payload: payload(struct {
			Transition, StateSHA256 string
		}{transition, stateSHA256(next.Execution.DirectDelivery)}),
	}, next, domain.Event{ID: stableID("event", commandID), RunID: currentRun.ID, Sequence: currentRun.Version + 2,
		AggregateID: currentRun.ID, AggregateVersion: next.Version, Type: transition,
		Payload: payload(struct {
			DirectDeliveryID string `json:"directDeliveryId"`
			Phase            string `json:"phase"`
		}{next.Execution.DirectDelivery.ID, string(next.Execution.DirectDelivery.Phase)})})
	if err != nil {
		return currentRun, err
	}
	if result.Outcome != domain.CommandApplied || result.Replay {
		return currentRun, ErrConcurrentTransition
	}
	return next, nil
}

func evidenceBinding(authority *candidate.Authority, id string) *candidate.EvidenceBinding {
	return &candidate.EvidenceBinding{ID: id, CandidateID: authority.CandidateID, CandidateSHA: authority.CandidateSHA,
		BaseSHA: authority.BaseSHA, Generation: authority.Generation, BindingSHA256: authority.BindingSHA256}
}

// Admit records only a direct policy chosen by the frozen Run. Existing PR
// publication authority is an explicit refusal and can never create state.
func (service *Service) Admit(ctx context.Context, command AdmitCommand) (Result, error) {
	unlock := service.lock("run:" + command.RunID)
	defer unlock()
	if command.RunID == "" || command.LeaseEpoch == 0 || command.NowMillis < 0 || !directdomain.ValidPolicy(command.Policy) {
		return Result{}, ErrInvalidCommand
	}
	value, err := service.load(ctx, command.RunID)
	if err != nil {
		return Result{Run: value.run}, err
	}
	if value.run.Execution.PublicationPolicy != nil || value.run.Execution.Publication != nil || len(value.run.Execution.PublicationHistory) != 0 ||
		value.run.Execution.CandidateAuthority != nil && value.run.Execution.CandidateAuthority.Downstream.Publication != nil {
		return Result{Run: value.run, Code: deliveryreducer.DirectCodePRFallbackForbidden}, ErrFallbackForbidden
	}
	binding, ok := currentBinding(value, command.Policy)
	if value.run.Execution.DirectDelivery != nil {
		state := value.run.Execution.DirectDelivery
		if ok && state.Binding == binding && state.Policy.SHA256 == command.Policy.SHA256 && directdomain.ValidState(*state) {
			return Result{Run: value.run, Replayed: true, WaitingHuman: state.Phase == directdomain.PhaseWaitingHuman,
				WaitingExternal: state.Phase == directdomain.PhaseWaitingExternal, Integrated: state.Phase == directdomain.PhaseComplete}, nil
		}
		return Result{Run: value.run}, ErrNotReady
	}
	if value.run.Version != command.ExpectedRunVersion || command.LeaseEpoch != value.run.Execution.LeaseBinding.Epoch ||
		!currentLease(value.project, value.run, command.LeaseEpoch, command.NowMillis) {
		return Result{Run: value.run}, ErrConcurrentTransition
	}
	if !ok || value.task.Complete || value.task.Attention != nil || value.run.Execution.NeedsYou != nil ||
		value.run.Execution.CandidateAuthority.Downstream.Integration != nil {
		return Result{Run: value.run}, ErrNotReady
	}
	state, ok := directdomain.NewState(binding, command.Policy)
	if !ok {
		return Result{Run: value.run}, ErrInvalidCommand
	}
	next := value.run
	next.Execution.DirectDelivery = &state
	authority := *next.Execution.CandidateAuthority
	if authority.Downstream.CI == nil {
		authority.Downstream.CI = evidenceBinding(&authority, state.Binding.CIObservationID)
	} else if !sameEvidence(authority.Downstream.CI, &authority, state.Binding.CIObservationID) {
		return Result{Run: value.run}, ErrNotReady
	}
	if authority.Downstream.Ready == nil {
		authority.Downstream.Ready = evidenceBinding(&authority, state.ID+"-ready")
	} else if authority.Downstream.Ready.CandidateID != authority.CandidateID || authority.Downstream.Ready.BindingSHA256 != authority.BindingSHA256 {
		return Result{Run: value.run}, ErrNotReady
	}
	next.Execution.CandidateAuthority = &authority
	next, err = service.persist(ctx, value.run, next, "direct_delivery.admitted")
	return Result{Run: next, Progressed: err == nil, WaitingHuman: state.Phase == directdomain.PhaseWaitingHuman}, err
}

// AuthorizeManual consumes one exact server-attributed human action. Merely
// calling Step, restarting, or observing a ready Candidate cannot substitute.
func (service *Service) AuthorizeManual(ctx context.Context, command AuthorizeManualCommand) (Result, error) {
	unlock := service.lock("run:" + command.RunID)
	defer unlock()
	if command.RunID == "" || command.LeaseEpoch == 0 || command.NowMillis < 0 ||
		!validAuthenticatedHuman(command.Actor, command.Authorization) {
		return Result{}, ErrInvalidCommand
	}
	value, err := service.load(ctx, command.RunID)
	if err != nil {
		return Result{Run: value.run}, err
	}
	state := value.run.Execution.DirectDelivery
	if state == nil {
		return Result{Run: value.run}, ErrNotReady
	}
	if state.HumanAuthorization != nil && state.HumanAuthorization.SHA256 == command.Authorization.SHA256 {
		return Result{Run: value.run, Replayed: true}, nil
	}
	if value.run.Version != command.ExpectedRunVersion || !currentLease(value.project, value.run, command.LeaseEpoch, command.NowMillis) ||
		command.LeaseEpoch != state.Binding.LeaseEpoch || !currentCandidate(value) || !reviewApproved(value) || !ciPassed(value) ||
		!correctionSettled(value) || value.run.Execution.PublicationPolicy != nil || value.run.Execution.Publication != nil ||
		len(value.run.Execution.PublicationHistory) != 0 || value.run.Execution.CandidateAuthority.Downstream.Publication != nil {
		return Result{Run: value.run}, ErrNotReady
	}
	nextState, ok := directdomain.AuthorizeManual(*state, command.Authorization)
	if !ok {
		return Result{Run: value.run}, ErrInvalidCommand
	}
	next := value.run
	next.Execution.DirectDelivery = &nextState
	next, err = service.persist(ctx, value.run, next, "direct_delivery.human_authorized")
	return Result{Run: next, Progressed: err == nil}, err
}

func reducerFacts(value current, nowMillis int64) deliveryreducer.DirectFacts {
	state := directdomain.State{}
	if value.run.Execution.DirectDelivery != nil {
		state = *value.run.Execution.DirectDelivery
	}
	authority := value.run.Execution.CandidateAuthority
	publication := value.run.Execution.PublicationPolicy != nil || value.run.Execution.Publication != nil ||
		len(value.run.Execution.PublicationHistory) != 0 || authority != nil && authority.Downstream.Publication != nil
	return deliveryreducer.DirectFacts{SchemaVersion: deliveryreducer.DirectSchemaVersion, State: state,
		TaskBlocked: value.task.Attention != nil, ProjectActive: value.project.State == "active",
		LeaseCurrent: currentLease(value.project, value.run, state.Binding.LeaseEpoch, nowMillis),
		CandidateCurrent: currentCandidate(value) && state.Binding.CandidateID == value.run.CurrentCandidateID &&
			state.Binding.CandidateGeneration == authority.Generation && state.Binding.ManifestSHA256 == authority.BindingSHA256,
		ReviewApproved: reviewApproved(value), CIPassed: ciPassed(value), CorrectionSettled: correctionSettled(value),
		PRPublicationStarted: publication, NowMillis: nowMillis}
}

func target(value current, state directdomain.State, nowMillis int64) directport.Target {
	return directport.Target{Binding: state.Binding, CandidateClaim: value.candidate.Claim,
		CandidateManifest: value.candidate.Manifest, Repository: value.run.Execution.RepositoryBinding,
		RepositoryBindingSHA256: value.run.Execution.RepositoryBindingHash,
		EffectID:                state.Integration.ID, Attempt: state.Integration.Attempt, TaskStoreNowMillis: nowMillis}
}

func (service *Service) park(ctx context.Context, value current, code, wake string, invalidate bool) (Result, error) {
	state := *value.run.Execution.DirectDelivery
	if invalidate {
		state = directdomain.Invalidate(state, code, wake)
		if !directdomain.ValidState(state) {
			return Result{Run: value.run}, ErrExternalAmbiguous
		}
	} else {
		var ok bool
		state, ok = directdomain.Park(state, code, wake)
		if !ok {
			return Result{Run: value.run}, ErrExternalAmbiguous
		}
	}
	next := value.run
	next.Execution.DirectDelivery = &state
	next.Execution.NeedsYou = &execution.NeedsYou{Code: execution.NeedCode(code), WakeCondition: wake, CleanupAuthorized: false}
	if !invalidate && next.Execution.CandidateAuthority != nil {
		authority := *next.Execution.CandidateAuthority
		authority.Downstream.Ready = nil
		next.Execution.CandidateAuthority = &authority
	}
	if invalidate && next.Execution.CandidateAuthority != nil && !next.Execution.CandidateAuthority.Invalidated {
		authority := *next.Execution.CandidateAuthority
		context := candidate.AuthorityContext{CandidateID: authority.CandidateID, BindingSHA256: authority.BindingSHA256,
			CandidateSHA: authority.CandidateSHA, BaseSHA: authority.BaseSHA, Branch: authority.Branch, TaskVersion: authority.TaskVersion,
			ConfigurationSHA256: authority.ConfigurationSHA256, DecisionsSHA256: authority.DecisionsSHA256,
			FindingsSHA256: authority.FindingsSHA256}
		switch code {
		case deliveryreducer.DirectCodeRemoteRefChanged, deliveryreducer.DirectCodeRemoteRefAbsent:
			context.BaseSHA = ""
		case deliveryreducer.DirectCodeCandidateStale, string(directdomain.CodeCandidateChanged), string(directdomain.CodeRepositoryMismatch):
			context.BindingSHA256 = ""
		case string(directdomain.CodeBranchChanged):
			context.Branch = ""
		}
		updated, changed := candidate.ReconcileAuthority(authority, context)
		if changed {
			next.Execution.CandidateAuthority = &updated
			if next.Execution.Review != nil && !next.Execution.Review.Invalidated {
				invalidatedReview := review.Invalidate(*next.Execution.Review, string(updated.InvalidationCode))
				next.Execution.Review = &invalidatedReview
			}
		}
	}
	next, err := service.persist(ctx, value.run, next, "direct_delivery."+code)
	return Result{Run: next, Progressed: err == nil, Code: code}, err
}

func (service *Service) applyDecision(ctx context.Context, value current, decision deliveryreducer.DirectDecision, nowMillis int64) (Result, error) {
	state := *value.run.Execution.DirectDelivery
	switch decision.Kind {
	case deliveryreducer.DirectDecisionWaitHuman:
		return Result{Run: value.run, WaitingHuman: true}, nil
	case deliveryreducer.DirectDecisionWaitExternal:
		return Result{Run: value.run, WaitingExternal: true, Code: decision.Code}, nil
	case deliveryreducer.DirectDecisionComplete:
		if state.Phase == directdomain.PhaseComplete {
			return Result{Run: value.run, Integrated: true}, nil
		}
		if state.Integration == nil || state.Integration.Observation == nil {
			return Result{Run: value.run}, ErrExternalAmbiguous
		}
		completed, ok := directdomain.Complete(state, *state.Integration.Observation, nowMillis)
		if !ok {
			return Result{Run: value.run}, ErrExternalAmbiguous
		}
		next := value.run
		next.Execution.DirectDelivery = &completed
		authority := *next.Execution.CandidateAuthority
		authority.Downstream.Integration = evidenceBinding(&authority, completed.Evidence.ID)
		next.Execution.CandidateAuthority = &authority
		next.Execution.NeedsYou = nil
		next, err := service.persist(ctx, value.run, next, "direct_delivery.integrated")
		return Result{Run: next, Progressed: err == nil, Integrated: err == nil}, err
	case deliveryreducer.DirectDecisionEscalate:
		if decision.Code == deliveryreducer.DirectCodePRFallbackForbidden {
			return Result{Run: value.run, Code: decision.Code}, ErrFallbackForbidden
		}
		if state.Phase == directdomain.PhaseNeedsYou && state.NeedsYouCode == decision.Code ||
			state.Phase == directdomain.PhaseInvalidated && state.InvalidationCode == decision.Code {
			return Result{Run: value.run, Code: decision.Code}, nil
		}
		invalidate := decision.Code == deliveryreducer.DirectCodeRemoteRefChanged ||
			decision.Code == deliveryreducer.DirectCodeRemoteRefAbsent || decision.Code == deliveryreducer.DirectCodeCandidateStale ||
			decision.Code == string(directdomain.CodeCandidateChanged) || decision.Code == string(directdomain.CodeBranchChanged) ||
			decision.Code == string(directdomain.CodeRepositoryMismatch)
		wake := "explicit_direct_delivery_reconciliation"
		if invalidate {
			wake = "fresh_candidate_validation_and_review"
		}
		return service.park(ctx, value, decision.Code, wake, invalidate)
	case deliveryreducer.DirectDecisionObserve, deliveryreducer.DirectDecisionDispatch:
		return Result{Run: value.run}, nil
	default:
		return Result{Run: value.run}, ErrExternalAmbiguous
	}
}

// Step resumes only the durable direct-delivery frontier. It persists an
// observation before dispatch, persists dispatching before Git handoff, and
// accepts success only from a later authoritative desired-state observation.
func (service *Service) Step(ctx context.Context, command StepCommand) (Result, error) {
	unlockRun := service.lock("run:" + command.RunID)
	defer unlockRun()
	if command.RunID == "" || command.LeaseEpoch == 0 || command.NowMillis < 0 {
		return Result{}, ErrInvalidCommand
	}
	value, err := service.load(ctx, command.RunID)
	if err != nil {
		return Result{Run: value.run}, err
	}
	if value.run.Execution.DirectDelivery == nil {
		return Result{Run: value.run}, ErrNotReady
	}
	unlockRepository := service.lock("repository:" + value.run.Execution.RepositoryBinding.RepositoryID)
	defer unlockRepository()
	if value.run.Version != command.ExpectedRunVersion || command.LeaseEpoch != value.run.Execution.DirectDelivery.Binding.LeaseEpoch {
		return Result{Run: value.run}, ErrConcurrentTransition
	}
	facts := reducerFacts(value, command.NowMillis)
	decision := deliveryreducer.ReduceDirect(facts)
	retryUnavailableObservation := value.run.Execution.DirectDelivery.Phase == directdomain.PhaseWaitingExternal &&
		decision.Kind == deliveryreducer.DirectDecisionWaitExternal && decision.Code == string(directdomain.CodeRemoteUnavailable)
	if decision.Kind != deliveryreducer.DirectDecisionObserve && decision.Kind != deliveryreducer.DirectDecisionDispatch && !retryUnavailableObservation {
		return service.applyDecision(ctx, value, decision, command.NowMillis)
	}
	state := *value.run.Execution.DirectDelivery
	if state.Integration == nil {
		return Result{Run: value.run}, ErrNotReady
	}
	observation, observeErr := service.remote.Observe(ctx, target(value, state, command.NowMillis))
	if observeErr != nil {
		return Result{Run: value.run}, observeErr
	}
	observedState, ok := directdomain.RecordObservation(state, observation)
	if !ok {
		return service.park(ctx, value, deliveryreducer.DirectCodeRemoteAmbiguous, "exact_bounded_remote_observation", false)
	}
	next := value.run
	next.Execution.DirectDelivery = &observedState
	next, err = service.persist(ctx, value.run, next, "direct_delivery.remote_observed")
	if err != nil {
		return Result{Run: value.run}, err
	}
	value, err = service.load(ctx, next.ID)
	if err != nil {
		return Result{Run: next}, err
	}
	decision = deliveryreducer.ReduceDirect(reducerFacts(value, command.NowMillis))
	if decision.Kind != deliveryreducer.DirectDecisionDispatch {
		return service.applyDecision(ctx, value, decision, command.NowMillis)
	}
	state = *value.run.Execution.DirectDelivery
	precondition := *state.Integration.Observation
	pushTarget := target(value, state, command.NowMillis)
	dispatching, ok := directdomain.BeginDispatch(state)
	if !ok {
		return Result{Run: value.run}, ErrExternalAmbiguous
	}
	dispatchRun := value.run
	dispatchRun.Execution.DirectDelivery = &dispatching
	dispatchRun, err = service.persist(ctx, value.run, dispatchRun, "direct_delivery.dispatching")
	if err != nil {
		return Result{Run: value.run}, err
	}
	pushErr := service.remote.Push(ctx, directport.PushCommand{Target: pushTarget,
		Attempt: dispatching.Integration.Attempt, ExpectedObservation: precondition})
	if pushErr != nil {
		if directport.HandoffPossible(pushErr) {
			return Result{Run: dispatchRun, Progressed: true}, pushErr
		}
		retryState, retryOK := directdomain.RetryAfterPreDispatch(dispatching)
		if !retryOK {
			return Result{Run: dispatchRun}, errors.Join(ErrExternalAmbiguous, pushErr)
		}
		retryRun := dispatchRun
		retryRun.Execution.DirectDelivery = &retryState
		retryRun, persistErr := service.persist(ctx, dispatchRun, retryRun, "direct_delivery.pre_dispatch_failed")
		return Result{Run: retryRun, Progressed: persistErr == nil}, errors.Join(pushErr, persistErr)
	}
	required, ok := directdomain.RequireObservation(dispatching)
	if !ok {
		return Result{Run: dispatchRun}, ErrExternalAmbiguous
	}
	observingRun := dispatchRun
	observingRun.Execution.DirectDelivery = &required
	observingRun, err = service.persist(ctx, dispatchRun, observingRun, "direct_delivery.observation_required")
	return Result{Run: observingRun, Progressed: err == nil}, err
}
