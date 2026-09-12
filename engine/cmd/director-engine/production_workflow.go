// SPDX-License-Identifier: Apache-2.0

package main

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
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"

	gitadapter "github.com/mcuadros/director-engine/adapters/git"
	githubadapter "github.com/mcuadros/director-engine/adapters/github"
	cleanupapp "github.com/mcuadros/director-engine/application/cleanup"
	correctionapp "github.com/mcuadros/director-engine/application/correction"
	directapp "github.com/mcuadros/director-engine/application/directdelivery"
	executionapp "github.com/mcuadros/director-engine/application/execution"
	feedbackapp "github.com/mcuadros/director-engine/application/feedback"
	integrationapp "github.com/mcuadros/director-engine/application/integration"
	publicationapp "github.com/mcuadros/director-engine/application/publication"
	reviewapp "github.com/mcuadros/director-engine/application/review"
	validationapp "github.com/mcuadros/director-engine/application/validation"
	"github.com/mcuadros/director-engine/domain"
	domaincleanup "github.com/mcuadros/director-engine/domain/cleanup"
	domainconfig "github.com/mcuadros/director-engine/domain/configuration"
	domaincorrection "github.com/mcuadros/director-engine/domain/correction"
	directdomain "github.com/mcuadros/director-engine/domain/directdelivery"
	"github.com/mcuadros/director-engine/domain/execution"
	domainfeedback "github.com/mcuadros/director-engine/domain/feedback"
	integrationdomain "github.com/mcuadros/director-engine/domain/integration"
	domainreview "github.com/mcuadros/director-engine/domain/review"
	domainvalidation "github.com/mcuadros/director-engine/domain/validation"
	githubport "github.com/mcuadros/director-engine/ports/github"
	"github.com/mcuadros/director-engine/ports/host"
	reconciliationport "github.com/mcuadros/director-engine/ports/reconciliation"
)

// Production owns the concrete application-service topology. Every field is
// required at construction so a missing executor fails before the Board
// listener can advertise product readiness.
type Production struct {
	store       Store
	controller  *executionapp.Controller
	launcher    *Launcher
	repository  githubport.RepositoryDiscoveryPort
	publication *publicationapp.Service
	validation  *validationapp.Service
	review      *reviewapp.Service
	correction  *correctionapp.Service
	feedback    *feedbackapp.Service
	direct      *directapp.Service
	integration *integrationapp.Service
	cleanup     *cleanupapp.Service
	artifacts   interface {
		CleanupRunArtifacts(context.Context, execution.Scope, string, []execution.Effect) error
	}
	queue       *WakeQueue
	runtimeRoot string
	coordinator string
}

type ManualIntegrationRequest struct {
	RequestID          string
	ProjectID          string
	TaskID             string
	RunID              string
	ExpectedRunVersion uint64
	ActorID            string
	ActorSessionID     string
	NowMillis          int64
}

type HumanFeedbackRequest struct {
	RequestID          string
	ProjectID          string
	TaskID             string
	RunID              string
	ExpectedRunVersion uint64
	ActorID            string
	ActorSessionID     string
	Body               string
	Severity           string
	NowMillis          int64
}

// SubmitFeedback binds a server-authenticated Paseo human comment to the
// current exact Candidate and delegates all correction policy to the existing
// feedback and correction application services.
func (production *Production) SubmitFeedback(ctx context.Context, request HumanFeedbackRequest) (domain.Run, error) {
	run, err := production.store.Run(ctx, request.RunID)
	if err != nil || run.TaskID != request.TaskID || run.Execution.Scope.ProjectID != request.ProjectID ||
		run.Version != request.ExpectedRunVersion || request.RequestID == "" || request.ActorID == "" ||
		request.ActorSessionID == "" || request.Body == "" || request.NowMillis < 0 {
		return domain.Run{}, errors.New("human feedback binding is invalid")
	}
	if run.CurrentCandidateID == "" || run.Execution.CandidateAuthority == nil || run.Execution.CandidateAuthority.Invalidated ||
		run.Execution.PreparationPlan.ID == "" || run.Execution.EffectiveProfilesSHA256 == "" ||
		run.Execution.DecisionContextSHA256 == "" || run.Execution.CorrectionPolicy == nil ||
		run.Execution.CorrectionPolicy.AttemptLimit != domaincorrection.AttemptLimit {
		return domain.Run{}, errors.New("human feedback has no current Candidate authority")
	}
	record, err := production.store.Candidate(ctx, run.CurrentCandidateID)
	if err != nil || record.CommitSHA != run.Execution.CandidateAuthority.CandidateSHA || record.Manifest.DiffSHA256 == "" {
		return domain.Run{}, errors.New("human feedback Candidate binding is ambiguous")
	}
	severity := domaincorrection.Severity(request.Severity)
	if !slices.Contains([]domaincorrection.Severity{domaincorrection.SeverityP0, domaincorrection.SeverityP1,
		domaincorrection.SeverityP2, domaincorrection.SeverityP3}, severity) {
		return domain.Run{}, errors.New("human feedback severity is invalid")
	}
	identity := productionStableID("paseo-feedback", request.RunID, request.RequestID, request.ActorSessionID)
	item := domainfeedback.Item{
		Source: domainfeedback.SourcePaseoDirect, ExternalID: identity, RevisionID: identity,
		Actor: domainfeedback.Actor{Kind: domainfeedback.ActorHuman, ID: request.ActorID, Login: request.ActorID,
			Authenticated: true, Attestation: domainfeedback.AttestationPaseoHuman},
		Kind: domainfeedback.KindComment, CandidateSHA: record.CommitSHA, BaseSHA: record.Manifest.BaseSHA,
		ContextSHA256: domainfeedback.DigestText(request.Body), Body: request.Body, Actionable: true,
		Severity: severity, CreatedAtMillis: request.NowMillis, UpdatedAtMillis: request.NowMillis,
	}
	result, err := production.feedback.IngestDirect(ctx, feedbackapp.DirectCommand{
		RoutingContext: feedbackapp.RoutingContext{RunID: run.ID, ExpectedTaskVersion: run.Execution.CandidateAuthority.TaskVersion,
			ExpectedRunVersion: run.Version, LeaseEpoch: run.Execution.LeaseBinding.Epoch,
			FrozenPlanDigest: run.Execution.PreparationPlan.ID, CurrentPlanDigest: run.Execution.PreparationPlan.ID,
			FrozenSkillSetDigest: run.Execution.EffectiveProfilesSHA256, CurrentSkillSetDigest: run.Execution.EffectiveProfilesSHA256,
			CurrentDecisionDigest: run.Execution.DecisionContextSHA256, CurrentDiffDigest: record.Manifest.DiffSHA256,
			CorrectionPolicy: *run.Execution.CorrectionPolicy, NowMillis: request.NowMillis},
		Item: item,
	})
	return result.Run, err
}

func (production *Production) IntegrateTask(ctx context.Context, request ManualIntegrationRequest) (domain.Run, error) {
	run, err := production.store.Run(ctx, request.RunID)
	if err != nil || run.TaskID != request.TaskID || run.Execution.Scope.ProjectID != request.ProjectID ||
		run.Version != request.ExpectedRunVersion || request.ActorID == "" || request.ActorSessionID == "" {
		return domain.Run{}, errors.New("manual integration binding is invalid")
	}
	switch run.Execution.DeliveryMode {
	case domainconfig.DeliveryPullRequest:
		state := run.Execution.Integration
		if state == nil {
			return domain.Run{}, errors.New("pull-request integration is not ready")
		}
		binding := state.Binding
		authorization := integrationdomain.SealAuthorization(integrationdomain.HumanAuthorization{ID: productionStableID("integration-authorization", request.RequestID),
			ActorKind: "human", ActorSource: integrationdomain.AuthorizationActorSource, Authenticated: true,
			ActorID: request.ActorID, SessionID: request.ActorSessionID, DecisionID: request.RequestID,
			Action: "integrate_pull_request", BindingSHA256: binding.SHA256, CandidateSHA: binding.CandidateSHA,
			BaseSHA: binding.BaseSHA, PullRequestNumber: binding.PullRequestNumber, PolicySHA256: binding.PolicySHA256,
			AuthorizedAtMillis: request.NowMillis})
		result, err := production.integration.AuthorizeManual(ctx, integrationapp.AuthorizeManualCommand{RunID: run.ID,
			ExpectedRunVersion: run.Version, LeaseEpoch: run.Execution.LeaseBinding.Epoch,
			Actor:         integrationapp.AuthenticatedHumanActor{Kind: "human", ID: request.ActorID, SessionID: request.ActorSessionID, Source: "server", Authenticated: true},
			Authorization: authorization, NowMillis: request.NowMillis})
		return result.Run, err
	case domainconfig.DeliveryDirect:
		state := run.Execution.DirectDelivery
		if state == nil {
			return domain.Run{}, errors.New("direct integration is not ready")
		}
		binding := state.Binding
		authorization := directdomain.SealAuthorization(directdomain.HumanAuthorization{ID: productionStableID("direct-authorization", request.RequestID),
			ActorKind: "human", ActorSource: directdomain.AuthorizationActorSource, Authenticated: true,
			ActorID: request.ActorID, DecisionID: request.RequestID, Action: "integrate_direct",
			BindingSHA256: binding.SHA256, CandidateSHA: binding.CandidateSHA, BaseSHA: binding.BaseSHA,
			TargetRef: binding.TargetRef, PolicySHA256: binding.PolicySHA256, AuthorizedAtMillis: request.NowMillis})
		result, err := production.direct.AuthorizeManual(ctx, directapp.AuthorizeManualCommand{RunID: run.ID,
			ExpectedRunVersion: run.Version, LeaseEpoch: run.Execution.LeaseBinding.Epoch,
			Actor:         directapp.AuthenticatedHumanActor{Kind: "human", ID: request.ActorID, SessionID: request.ActorSessionID, Source: "server", Authenticated: true},
			Authorization: authorization, NowMillis: request.NowMillis})
		return result.Run, err
	default:
		return domain.Run{}, errors.New("delivery mode is invalid")
	}
}

func NewProduction(store Store, controller *executionapp.Controller, launcher *Launcher, hostPort host.Port,
	github *githubadapter.Connector, git *gitadapter.Adapter, queue *WakeQueue, runtimeRoot, coordinatorUUID string,
) (*Production, error) {
	if store == nil || controller == nil || launcher == nil || hostPort == nil || github == nil || git == nil || queue == nil ||
		!filepath.IsAbs(runtimeRoot) || !domainreview.ValidUUID(coordinatorUUID) {
		return nil, errors.New("production workflow composition is incomplete")
	}
	publication, err := publicationapp.NewService(store, git, github)
	if err != nil {
		return nil, err
	}
	validation, err := validationapp.NewService(store, git, github)
	if err != nil {
		return nil, err
	}
	review, err := reviewapp.NewService(store, &gitadapter.ReviewerCheckout{}, hostPort)
	if err != nil {
		return nil, err
	}
	correction, err := correctionapp.NewService(store, git, hostPort)
	if err != nil {
		return nil, err
	}
	feedback, err := feedbackapp.NewService(store, github, correction, git)
	if err != nil {
		return nil, err
	}
	direct, err := directapp.NewService(store, git)
	if err != nil {
		return nil, err
	}
	integration, err := integrationapp.NewService(store, github, github, github, git)
	if err != nil {
		return nil, err
	}
	artifactRoot := filepath.Join(runtimeRoot, "recovery")
	if err := os.MkdirAll(artifactRoot, 0o700); err != nil {
		return nil, errors.New("cleanup artifact root is unavailable")
	}
	cleanup, err := cleanupapp.NewService(store, hostPort, git, git, github, gitadapter.NewCleanup(), artifactRoot)
	if err != nil {
		return nil, err
	}
	artifacts, ok := launcher.runtime.(interface {
		CleanupRunArtifacts(context.Context, execution.Scope, string, []execution.Effect) error
	})
	if !ok {
		return nil, errors.New("production runtime artifact cleanup is unavailable")
	}
	return &Production{store: store, controller: controller, launcher: launcher, repository: github,
		publication: publication, validation: validation, review: review, correction: correction,
		feedback: feedback, direct: direct, integration: integration, cleanup: cleanup, artifacts: artifacts,
		queue: queue, runtimeRoot: runtimeRoot, coordinator: coordinatorUUID}, nil
}

// RecordReviewCompletionEvent persists and enqueues a Reviewer terminal wake.
// It mirrors the execution controller's primary-worker receipt protocol while
// leaving Review state transitions under the Review application service.
func (production *Production) RecordReviewCompletionEvent(ctx context.Context, runID string,
	event execution.CompletionEvent, nowMillis int64,
) error {
	run, err := production.store.Run(ctx, runID)
	if err != nil || run.Execution.Review == nil {
		return errors.New("Reviewer terminal event has no current Review")
	}
	state := run.Execution.Review
	if event.AgentID != state.ReviewerUUID || event.DispatchEffectID != state.Prompt.ID ||
		!slices.Contains([]domainreview.EffectPhase{domainreview.EffectDispatching, domainreview.EffectComplete}, state.Prompt.Phase) ||
		event.BindingHash != state.Binding.BindingSHA256 {
		return errors.New("Reviewer terminal event is misbound")
	}
	return production.recordExternalCompletionEvent(ctx, run, event, state.ReviewerUUID, state.Binding.BindingSHA256,
		"review", nowMillis)
}

func (production *Production) RecordCorrectionCompletionEvent(ctx context.Context, runID string,
	event execution.CompletionEvent, nowMillis int64,
) error {
	run, err := production.store.Run(ctx, runID)
	if err != nil || run.Execution.Correction == nil || len(run.Execution.Correction.Attempts) == 0 {
		return errors.New("correction terminal event has no current attempt")
	}
	state := run.Execution.Correction
	attempt := state.Attempts[len(state.Attempts)-1]
	batch, ok := domaincorrection.CurrentBatch(*state)
	if !ok || event.AgentID != state.OriginalTaskAgentUUID || event.DispatchEffectID != attempt.Prompt.ID ||
		!slices.Contains([]domaincorrection.EffectPhase{domaincorrection.EffectDispatching, domaincorrection.EffectComplete}, attempt.Prompt.Phase) ||
		event.BindingHash != batch.SHA256 {
		return errors.New("correction terminal event is misbound")
	}
	return production.recordExternalCompletionEvent(ctx, run, event, state.OriginalTaskAgentUUID, batch.SHA256,
		"correction", nowMillis)
}

func (production *Production) recordExternalCompletionEvent(ctx context.Context, run domain.Run,
	event execution.CompletionEvent, expectedAgent, expectedBinding, namespace string, nowMillis int64,
) error {
	receiptIndex := -1
	for index, receipt := range run.Execution.CompletionEventReceipts {
		if receipt.EventID != event.ID {
			continue
		}
		if receipt.EventFactHash != event.FactHash || receipt.Event != event {
			return errors.New("external terminal event identity conflicts")
		}
		if receipt.Phase == execution.CompletionReceiptComplete {
			return nil
		}
		receiptIndex = index
		break
	}
	if receiptIndex < 0 {
		admission := execution.AdmitCompletionEvent(event, expectedAgent, expectedBinding,
			run.Execution.CompletionEventCursor, nowMillis)
		if admission.Kind != execution.AdmissionAllow || len(run.Execution.CompletionEventReceipts) >= execution.MaximumCompletionEventReceipts {
			return errors.New("external terminal event was refused")
		}
		next := run
		next.Version = run.Version + 1
		next.Execution.LastCompletionEvent = &event
		next.Execution.CompletionEventCursor = event.Cursor
		next.Execution.CompletionEventReceipts = append(slices.Clone(run.Execution.CompletionEventReceipts), execution.CompletionEventReceipt{
			EventID: event.ID, EventFactHash: event.FactHash, Event: event, Cursor: event.Cursor,
			Phase: execution.CompletionReceiptIntent,
		})
		commandID := productionStableID(namespace+"-terminal-command", event.ID, event.FactHash)
		result, persistErr := production.store.UpdateRun(ctx, domain.CommandRequest{IdempotencyKey: commandID,
			Type: namespace + ".terminal_event.recorded", AggregateID: run.ID, ExpectedVersion: run.Version,
			Payload: productionPayload(struct{ EventID, EventFactHash string }{event.ID, event.FactHash})}, next,
			domain.Event{ID: productionStableID(namespace+"-terminal-event", commandID), RunID: run.ID, Sequence: run.Version + 2,
				AggregateID: run.ID, AggregateVersion: next.Version, Type: namespace + ".terminal_event.recorded",
				Payload: productionPayload(struct{ EventID string }{event.ID})})
		if persistErr != nil || result.Outcome != domain.CommandApplied {
			return errors.New("external terminal event receipt was not persisted")
		}
		run = next
		receiptIndex = len(run.Execution.CompletionEventReceipts) - 1
	}
	request := reconciliationport.Request{EventID: event.ID, EventFactHash: event.FactHash, RunID: run.ID,
		AgentID: event.AgentID, DispatchEffectID: event.DispatchEffectID, Kind: event.Kind,
		ReceivedAtMillis: event.ObservedAtMillis, DeadlineAtMillis: event.ObservedAtMillis + execution.CompletionDispatchTargetMillis}
	receipt, err := production.queue.Enqueue(ctx, request)
	if err != nil {
		return err
	}
	next := run
	next.Version = run.Version + 1
	next.Execution.CompletionEventReceipts = slices.Clone(run.Execution.CompletionEventReceipts)
	next.Execution.CompletionEventReceipts[receiptIndex].Phase = execution.CompletionReceiptComplete
	next.Execution.CompletionEventReceipts[receiptIndex].EnqueuedAtMillis = receipt.EnqueuedAtMillis
	next.Execution.CompletionEventReceipts[receiptIndex].DispatchLatencyMillis = receipt.EnqueuedAtMillis - event.ObservedAtMillis
	commandID := productionStableID(namespace+"-terminal-enqueue-command", event.ID, event.FactHash)
	result, err := production.store.UpdateRun(ctx, domain.CommandRequest{IdempotencyKey: commandID,
		Type: namespace + ".reconciliation_enqueued", AggregateID: run.ID, ExpectedVersion: run.Version,
		Payload: productionPayload(struct{ EventID string }{event.ID})}, next,
		domain.Event{ID: productionStableID(namespace+"-terminal-enqueue-event", commandID), RunID: run.ID, Sequence: run.Version + 2,
			AggregateID: run.ID, AggregateVersion: next.Version, Type: namespace + ".reconciliation_enqueued",
			Payload: productionPayload(struct{ EventID string }{event.ID})})
	if err != nil || result.Outcome != domain.CommandApplied {
		return errors.New("external terminal reconciliation receipt was not persisted")
	}
	return nil
}

func productionPayload(value any) json.RawMessage {
	encoded, err := json.Marshal(value)
	if err != nil {
		panic("marshal fixed production event: " + err.Error())
	}
	return encoded
}

func productionStableID(prefix string, values ...string) string {
	digest := sha256.Sum256([]byte(strings.Join(values, "\x1f")))
	return prefix + "-" + hex.EncodeToString(digest[:16])
}

func UUID(parts ...string) string {
	value := productionStableID("uuid", parts...)
	hexValue := value[len("uuid-"):]
	return fmt.Sprintf("%s-%s-4%s-a%s-%s", hexValue[0:8], hexValue[8:12], hexValue[13:16], hexValue[17:20], hexValue[20:32])
}

func (production *Production) candidateAuthority(ctx context.Context, run domain.Run, nowMillis int64) (domain.Run, error) {
	if run.Execution.CandidateAuthority != nil && !run.Execution.CandidateAuthority.Invalidated {
		return run, nil
	}
	result, err := production.controller.ReconcileCandidateAuthority(ctx, executionapp.CandidateAuthorityCommand{
		SchemaVersion: executionapp.CandidateAuthorityCommandSchemaVersion,
		RequestID:     productionStableID("workflow-candidate-authority", run.ID, fmt.Sprintf("version-%d", run.Version)),
		RunID:         run.ID, ExpectedRunVersion: run.Version, LeaseEpoch: run.Execution.LeaseBinding.Epoch, NowMillis: nowMillis})
	return result.Run, err
}

func (production *Production) repositoryFact(ctx context.Context, run domain.Run, nowMillis int64) (githubport.RepositoryFact, error) {
	binding := run.Execution.RepositoryBinding
	return production.repository.DiscoverRepository(ctx, githubport.RepositoryDiscoveryRequest{CanonicalRemote: binding.CanonicalRemote,
		RepositoryID: binding.RepositoryID, RepositoryKey: binding.RepositoryKey, NowMillis: nowMillis})
}

func reviewCI(run domain.Run) (domainreview.CIObservation, bool) {
	if run.Execution.Review == nil || run.Execution.Validation == nil || run.Execution.Validation.Evidence == nil {
		return domainreview.CIObservation{}, false
	}
	state, evidence := run.Execution.Review, run.Execution.Validation.Evidence
	checks := make([]string, len(evidence.Checks))
	for index := range evidence.Checks {
		checks[index] = evidence.Checks[index].ID
	}
	sort.Strings(checks)
	value := domainreview.SealCIObservation(domainreview.CIObservation{ID: "review-ci-" + evidence.SHA256[:32],
		SlotID: state.Binding.CISlotID, CandidateSHA: state.Binding.CandidateSHA, BaseSHA: state.Binding.BaseSHA,
		TreeSHA: state.Binding.TreeSHA, ManifestSHA256: state.Binding.ManifestSHA256,
		WorkflowRunID: evidence.WorkflowRunID, RequiredChecks: checks, Status: string(evidence.Outcome),
		Authoritative: true, Complete: true, ObservedAtMillis: evidence.ObservedAtMillis})
	return value, domainreview.ValidCIObservation(value, state.Binding)
}

type storedMCPPayload struct {
	ExpectedRunVersion uint64          `json:"expectedRunVersion"`
	Arguments          json.RawMessage `json:"arguments"`
}

func (production *Production) pendingToolArguments(ctx context.Context, run domain.Run, tool string) (json.RawMessage, string, uint64, bool) {
	for index := len(run.Execution.MCPCommandReceipts) - 1; index >= 0; index-- {
		receipt := run.Execution.MCPCommandReceipts[index]
		if receipt.ToolName != tool {
			continue
		}
		command, err := production.store.Command(ctx, receipt.CommandKey)
		if err != nil || command.Outcome != domain.CommandApplied {
			return nil, "", 0, false
		}
		var payload storedMCPPayload
		if json.Unmarshal(command.Payload, &payload) != nil || len(payload.Arguments) == 0 {
			return nil, "", 0, false
		}
		return payload.Arguments, receipt.CommandKey, payload.ExpectedRunVersion, true
	}
	return nil, "", 0, false
}

func (production *Production) routeWorkerClaim(ctx context.Context, run domain.Run, nowMillis int64) (bool, error) {
	if run.Execution.Claim != nil || run.Execution.AgentPrompt.Phase != execution.EffectComplete {
		return false, nil
	}
	arguments, key, _, ok := production.pendingToolArguments(ctx, run, "director_task_outcome_submit")
	if !ok {
		return false, nil
	}
	var header struct {
		Outcome string `json:"outcome"`
	}
	if json.Unmarshal(arguments, &header) != nil {
		return false, errors.New("Worker outcome receipt is invalid")
	}
	var value struct {
		Outcome           string            `json:"outcome"`
		CandidateSHA      string            `json:"candidateSha"`
		BaseSHA           string            `json:"baseSha"`
		CriteriaResults   map[string]string `json:"criteriaResults"`
		ResidualRiskCodes []string          `json:"residualRiskCodes"`
	}
	switch header.Outcome {
	case "completed":
		if json.Unmarshal(arguments, &value) != nil {
			return false, errors.New("completed Worker outcome receipt is invalid")
		}
	case "needs_validation", "needs_review":
		var candidate struct {
			Outcome      string `json:"outcome"`
			CandidateSHA string `json:"candidateSha"`
			BaseSHA      string `json:"baseSha"`
		}
		if json.Unmarshal(arguments, &candidate) != nil {
			return false, errors.New("Candidate-routing Worker outcome receipt is invalid")
		}
		value.Outcome, value.CandidateSHA, value.BaseSHA = "completed", candidate.CandidateSHA, candidate.BaseSHA
		value.CriteriaResults = make(map[string]string, len(run.Execution.CriterionIDs))
		for _, criterion := range run.Execution.CriterionIDs {
			value.CriteriaResults[criterion] = "claimed_unsatisfied"
		}
		value.ResidualRiskCodes = []string{"WORKER_REQUESTED_" + strings.ToUpper(strings.TrimPrefix(header.Outcome, "needs_"))}
	case "needs_human_decision":
		return true, production.controller.RecordOutcomeAttention(ctx, run.ID, key,
			execution.NeedCode("worker_human_decision_required"), "authenticated_human_decision_or_current_policy")
	case "blocked_by_dependency":
		return true, production.controller.RecordOutcomeAttention(ctx, run.ID, key,
			execution.NeedCode("worker_dependency_wait"), "authoritative_dependency_graph_changed_or_human_override")
	case "blocked_by_access":
		code := execution.NeedCode("worker_access_observation_required")
		if run.Execution.LastCompletionEvent != nil && run.Execution.LastCompletionEvent.Kind == execution.CompletionEventPermission {
			code = execution.NeedCode("permission_required")
		}
		return true, production.controller.RecordOutcomeAttention(ctx, run.ID, key, code,
			"fresh_Doctor_or_authenticated_credential_or_permission_decision")
	case "budget_exhausted":
		result, err := production.controller.Step(ctx, run.ID, nowMillis)
		return result.Progressed, err
	default:
		return false, errors.New("Worker outcome receipt has an unknown kind")
	}
	claim := execution.CompletedClaim{ID: productionStableID("completed-claim", key), SchemaVersion: "director.agent-outcome.completed/v1",
		Outcome: value.Outcome, AgentID: run.Execution.Agent.ExternalID, CandidateSHA: value.CandidateSHA, BaseSHA: value.BaseSHA,
		CriteriaResults: value.CriteriaResults, ResidualRiskCodes: value.ResidualRiskCodes}
	if err := production.controller.RecordCompletedClaim(ctx, run.ID, claim); err != nil {
		return false, err
	}
	return true, nil
}

// routeCorrectionOutput translates the existing closed Worker outcome into
// the correction service's whole-batch output. The changed Candidate SHA is
// the durable discriminator that prevents the original completion receipt
// from being replayed as a correction response after restart or response loss.
func (production *Production) routeCorrectionOutput(ctx context.Context, run domain.Run, nowMillis int64) (bool, error) {
	state := run.Execution.Correction
	if state == nil || state.Phase != domaincorrection.PhaseAwaitingOutput || len(state.Attempts) == 0 {
		return false, nil
	}
	arguments, _, _, ok := production.pendingToolArguments(ctx, run, "director_task_outcome_submit")
	if !ok {
		return false, nil
	}
	var value struct {
		Outcome         string            `json:"outcome"`
		CandidateSHA    string            `json:"candidateSha"`
		CriteriaResults map[string]string `json:"criteriaResults"`
	}
	if json.Unmarshal(arguments, &value) != nil || value.Outcome != "completed" || value.CandidateSHA == state.CurrentCandidateSHA {
		return false, nil
	}
	batch, ok := domaincorrection.CurrentBatch(*state)
	if !ok {
		return false, errors.New("correction batch is invalid")
	}
	coverage := make([]string, 0, len(value.CriteriaResults))
	allSatisfied := len(value.CriteriaResults) == len(state.CriterionIDs)
	for criterion, result := range value.CriteriaResults {
		if result == "claimed_satisfied" {
			coverage = append(coverage, criterion)
		} else {
			allSatisfied = false
		}
	}
	sort.Strings(coverage)
	resolved := []string{}
	if allSatisfied {
		resolved = domaincorrection.FindingIDs(batch)
	}
	attempt := state.Attempts[len(state.Attempts)-1]
	result, err := production.correction.SubmitOutput(ctx, correctionapp.OutputCommand{
		TransitionCommand: correctionapp.TransitionCommand{RunID: run.ID, ExpectedRunVersion: run.Version,
			LeaseEpoch: run.Execution.LeaseBinding.Epoch, NowMillis: nowMillis},
		Output: domaincorrection.Output{SchemaVersion: domaincorrection.OutputSchemaVersion, AttemptKey: attempt.Key,
			AgentUUID: state.OriginalTaskAgentUUID, BatchSHA256: batch.SHA256,
			BatchedFindingIDs: domaincorrection.FindingIDs(batch), CandidateSHA: value.CandidateSHA,
			ResolvedFindingIDs: resolved, AcceptanceCoverage: coverage},
	})
	return result.Progressed, err
}

func (production *Production) routeReviewClaim(ctx context.Context, run domain.Run, nowMillis int64) (bool, error) {
	state := run.Execution.Review
	if state == nil || state.Evidence != nil || state.Prompt.Phase != domainreview.EffectComplete || state.CheckoutEvidence == nil || state.CIObservation == nil {
		return false, nil
	}
	arguments, _, _, ok := production.pendingToolArguments(ctx, run, "director_review_verdict_submit")
	if !ok {
		return false, nil
	}
	var value struct {
		Verdict            domainreview.Verdict `json:"verdict"`
		AcceptanceCriteria []string             `json:"acceptanceCriteria"`
		Coverage           []string             `json:"coverage"`
		Findings           []struct {
			Code       string                 `json:"code"`
			Severity   domainreview.Severity  `json:"severity"`
			Dimension  domainreview.Dimension `json:"dimension"`
			Summary    string                 `json:"summary"`
			References []string               `json:"references"`
		} `json:"findings"`
		ResidualRiskCodes []string `json:"residualRiskCodes"`
	}
	if json.Unmarshal(arguments, &value) != nil || !slices.Equal(value.AcceptanceCriteria, run.Execution.CriterionIDs) {
		return false, errors.New("review verdict acceptance binding is invalid")
	}
	requiredCoverage := []string{"acceptance", "correctness", "security", "maintainability", "readability", "design", "quality", "rigor"}
	coverage := slices.Clone(value.Coverage)
	sort.Strings(coverage)
	sortedRequired := slices.Clone(requiredCoverage)
	sort.Strings(sortedRequired)
	if !slices.Equal(coverage, sortedRequired) {
		return false, errors.New("review verdict coverage is incomplete")
	}
	findings := make([]domainreview.Finding, len(value.Findings))
	for index, finding := range value.Findings {
		findings[index] = domainreview.Finding{Code: finding.Code, Severity: finding.Severity, Dimension: finding.Dimension,
			Summary: finding.Summary, References: slices.Clone(finding.References)}
	}
	matrix := make([]domainreview.MatrixResult, len(state.Matrix))
	for index, entry := range state.Matrix {
		status := "covered"
		for _, finding := range findings {
			if entry.Kind == domainreview.MatrixDimension && string(finding.Dimension) == entry.ID ||
				entry.Kind == domainreview.MatrixCriterion && finding.Dimension == domainreview.DimensionAcceptance {
				status = "finding"
				break
			}
		}
		matrix[index] = domainreview.MatrixResult{ID: entry.ID, Kind: entry.Kind, Status: status}
	}
	claim := domainreview.Claim{SchemaVersion: domainreview.ClaimSchemaVersion, ReviewKey: state.ReviewKey,
		AttemptKey: domainreview.AttemptKey(state.Binding, domainreview.AttemptInitial), AttemptReason: domainreview.AttemptInitial,
		Binding: state.Binding, ReviewerUUID: state.ReviewerUUID, HarnessReviewerUUID: state.ReviewerUUID,
		Checkout: *state.CheckoutEvidence, CIObservation: *state.CIObservation, Verdict: value.Verdict,
		Matrix: matrix, Findings: findings, ResidualRiskCodes: slices.Clone(value.ResidualRiskCodes), HarnessVersion: 2,
		Setup:  domainreview.SetupObservation{Mode: domainreview.SetupRemoteOnly, GitAvailable: true, DependencyState: domainreview.DependencyMissing},
		Probes: []domainreview.ProbeResult{}}
	result, err := production.review.SubmitClaim(ctx, run.ID, claim, nowMillis)
	return result.Progressed, err
}

func reviewCleanupComplete(state *domainreview.State) bool {
	return state != nil && state.AgentCleanup.Phase == domainreview.EffectComplete &&
		state.WorkspaceCleanup.Phase == domainreview.EffectComplete && state.CheckoutCleanup.Phase == domainreview.EffectComplete
}

func correctionSourceRevision(run domain.Run, source domaincorrection.Source, values ...string) string {
	parts := []string{run.CurrentCandidateID, run.Execution.CandidateAuthority.CandidateSHA, string(source)}
	parts = append(parts, values...)
	return domainfeedback.DigestText(strings.Join(parts, "\x1f"))
}

// routeGateCorrection converts current normalized CI and Review evidence into
// one complete four-source correction batch. It does not interpret provider
// output or start a turn; the correction reducer decides admission and limits.
func (production *Production) routeGateCorrection(ctx context.Context, run domain.Run, nowMillis int64) (bool, error) {
	if run.Execution.CandidateAuthority == nil || run.Execution.Review == nil || run.Execution.Review.Evidence == nil ||
		run.Execution.CorrectionPolicy == nil || run.Execution.CorrectionPolicy.AttemptLimit != domaincorrection.AttemptLimit {
		return false, nil
	}
	record, err := production.store.Candidate(ctx, run.CurrentCandidateID)
	if err != nil || record.CommitSHA != run.Execution.CandidateAuthority.CandidateSHA {
		return false, errors.New("correction Candidate binding is unavailable")
	}
	snapshots := []domaincorrection.SourceSnapshot{}
	findings := []domaincorrection.FindingInput{}
	reviewArguments, _, _, hasReviewClaim := production.pendingToolArguments(ctx, run, "director_review_verdict_submit")
	var reviewClaim struct {
		Verdict  domainreview.Verdict `json:"verdict"`
		Findings []struct {
			Code      string                 `json:"code"`
			Severity  domainreview.Severity  `json:"severity"`
			Dimension domainreview.Dimension `json:"dimension"`
			Summary   string                 `json:"summary"`
		} `json:"findings"`
	}
	if !hasReviewClaim || json.Unmarshal(reviewArguments, &reviewClaim) != nil ||
		reviewClaim.Verdict != run.Execution.Review.Evidence.Verdict {
		return false, errors.New("current Review claim is unavailable for correction")
	}
	reviewFindings := uint32(0)
	if reviewClaim.Verdict != domainreview.VerdictApproveCandidate {
		for index, finding := range reviewClaim.Findings {
			severity := domaincorrection.Severity(finding.Severity)
			findings = append(findings, domaincorrection.FindingInput{ID: fmt.Sprintf("review-%d", index+1),
				Source: domaincorrection.SourceReview, Class: "REVIEW_FINDING", Severity: severity,
				CandidateID: run.CurrentCandidateID, CandidateSHA: record.CommitSHA, Summary: finding.Summary,
				Evidence: []domaincorrection.Evidence{{ID: fmt.Sprintf("review-evidence-%d", index+1),
					Kind: domaincorrection.EvidenceReviewFinding, SHA256: domainfeedback.DigestText(strings.Join([]string{finding.Code, string(finding.Dimension), finding.Summary}, "\x1f"))}},
				AcceptanceCoverage:    slices.Clone(run.Execution.CriterionIDs),
				Blocking:              severity == domaincorrection.SeverityP0 || severity == domaincorrection.SeverityP1,
				RequiresHumanDecision: reviewClaim.Verdict == domainreview.VerdictNeedsHumanDecision})
			reviewFindings++
		}
	}
	snapshots = append(snapshots, domaincorrection.SourceSnapshot{Source: domaincorrection.SourceReview,
		Revision: correctionSourceRevision(run, domaincorrection.SourceReview, run.Execution.Review.Evidence.ClaimSHA256), Count: reviewFindings})
	snapshots = append(snapshots, domaincorrection.SourceSnapshot{Source: domaincorrection.SourceValidation,
		Revision: correctionSourceRevision(run, domaincorrection.SourceValidation), Count: 0})
	ciCount := uint32(0)
	if run.Execution.Review.CIObservation == nil {
		return false, errors.New("current CI observation is unavailable for correction")
	}
	ci := run.Execution.Review.CIObservation
	if ci.Status != "passed" {
		findings = append(findings, domaincorrection.FindingInput{ID: "authoritative-ci", Source: domaincorrection.SourceCI,
			Class: "AUTHORITATIVE_CI", Severity: domaincorrection.SeverityP1, CandidateID: run.CurrentCandidateID,
			CandidateSHA: record.CommitSHA, Summary: "The authoritative complete CI observation did not pass",
			Evidence: []domaincorrection.Evidence{{ID: "authoritative-ci-evidence", Kind: domaincorrection.EvidenceCIObservation,
				SHA256: ci.SHA256}}, AcceptanceCoverage: slices.Clone(run.Execution.CriterionIDs), Blocking: true})
		ciCount = 1
	}
	snapshots = append(snapshots, domaincorrection.SourceSnapshot{Source: domaincorrection.SourceCI,
		Revision: correctionSourceRevision(run, domaincorrection.SourceCI, ci.SHA256), Count: ciCount})
	snapshots = append(snapshots, domaincorrection.SourceSnapshot{Source: domaincorrection.SourceHuman,
		Revision: correctionSourceRevision(run, domaincorrection.SourceHuman), Count: 0})
	if len(findings) == 0 {
		return false, nil
	}
	result, err := production.correction.Ingest(ctx, correctionapp.IngestCommand{RunID: run.ID,
		ExpectedRunVersion: run.Version, LeaseEpoch: run.Execution.LeaseBinding.Epoch,
		OriginalTaskAgentUUID: run.Execution.PrimarySession.NativeAgentID,
		CriterionIDs:          slices.Clone(run.Execution.CriterionIDs), FrozenPlanDigest: run.Execution.PreparationPlan.ID,
		CurrentPlanDigest: run.Execution.PreparationPlan.ID, FrozenSkillSetDigest: run.Execution.EffectiveProfilesSHA256,
		CurrentSkillSetDigest: run.Execution.EffectiveProfilesSHA256, CurrentDecisionDigest: run.Execution.DecisionContextSHA256,
		CurrentDiffDigest: record.Manifest.DiffSHA256,
		Policy:            *run.Execution.CorrectionPolicy,
		SourceSnapshots:   snapshots, Findings: findings, NowMillis: nowMillis})
	return result.Progressed, err
}

func (production *Production) reconcilePullRequest(ctx context.Context, run domain.Run, nowMillis int64) (bool, error) {
	fact, err := production.repositoryFact(ctx, run, nowMillis)
	if err != nil {
		return false, err
	}
	if run.Execution.Publication == nil {
		result, err := production.publication.Admit(ctx, publicationapp.AdmitCommand{RunID: run.ID,
			ExpectedRunVersion: run.Version, GitHubRepositoryID: fact.DatabaseID, GitHubRepositoryNodeID: fact.NodeID,
			HeadOwner: fact.ViewerLogin, OwnershipSHA256: domaincleanup.DigestText(run.ID + "\x1fowned"),
			ExpectedRemoteHead: "", NowMillis: nowMillis})
		return result.Progressed, err
	}
	if run.Execution.Publication.Evidence == nil || !run.Execution.Publication.Evidence.Draft || run.Execution.Publication.Invalidated {
		result, err := production.publication.Reconcile(ctx, run.ID, nowMillis)
		if errors.Is(err, publicationapp.ErrReviewRequired) {
			return false, nil
		}
		return result.Progressed, err
	}
	ciSlot := productionStableID("ci-slot", run.ID, run.CurrentCandidateID)
	if run.Execution.ValidationPolicy == nil {
		return false, errors.New("pull-request workflow requires one authoritative remote CI policy")
	}
	if run.Execution.Validation == nil {
		policy := run.Execution.ValidationPolicy
		result, err := production.validation.Admit(ctx, validationapp.AdmitCommand{RunID: run.ID,
			ExpectedRunVersion: run.Version, GitHubRepositoryID: fact.DatabaseID, GitHubRepositoryNodeID: fact.NodeID,
			ViewerLogin: fact.ViewerLogin, CISlotID: ciSlot, WorkflowID: policy.WorkflowID, WorkflowName: policy.WorkflowName,
			RequiredChecks: policy.RequiredChecks, CycleRuntimeMillis: policy.CycleRuntimeMillis, NowMillis: nowMillis})
		return result.Progressed, err
	}
	if run.Execution.Review == nil {
		root := filepath.Join(production.runtimeRoot, "reviews", run.ID)
		if err := os.MkdirAll(root, 0o700); err != nil {
			return false, errors.New("review root is unavailable")
		}
		result, err := production.review.Admit(ctx, reviewapp.AdmitCommand{RunID: run.ID, ExpectedRunVersion: run.Version,
			CoordinatorUUID: production.coordinator, ReviewOwnerUUID: UUID("review-owner", run.ID, run.CurrentCandidateID),
			CISlotID: ciSlot, ReviewerRoot: root, CheckoutPath: filepath.Join(root, "candidate"),
			CriterionIDs: run.Execution.CriterionIDs, ProbePlan: []domainreview.Probe{}, NowMillis: nowMillis})
		return result.Progressed, err
	}
	if run.Execution.Validation.Evidence == nil {
		result, err := production.validation.Reconcile(ctx, run.ID, nowMillis)
		return result.Progressed, err
	}
	if run.Execution.Review.CIObservation == nil {
		ci, ok := reviewCI(run)
		if !ok {
			return false, errors.New("authoritative CI observation cannot bind Review")
		}
		result, err := production.review.ObserveCI(ctx, run.ID, production.coordinator, ci, nowMillis)
		return result.Progressed, err
	}
	if run.Execution.Review.Evidence == nil {
		if progressed, routeErr := production.routeReviewClaim(ctx, run, nowMillis); progressed || routeErr != nil {
			return progressed, routeErr
		}
		result, err := production.review.Reconcile(ctx, run.ID, production.coordinator, nowMillis)
		return result.Progressed, err
	}
	if !reviewCleanupComplete(run.Execution.Review) {
		result, err := production.review.Cleanup(ctx, run.ID, production.coordinator, nowMillis)
		return result.Progressed, err
	}
	if run.Execution.Review.Evidence.Verdict != domainreview.VerdictApproveCandidate ||
		run.Execution.Validation.Evidence.Outcome != domainvalidation.OutcomePassed {
		return production.routeGateCorrection(ctx, run, nowMillis)
	}
	record, err := production.store.Candidate(ctx, run.CurrentCandidateID)
	if err != nil || run.Execution.CorrectionPolicy == nil {
		return false, errors.New("pull-request feedback binding is unavailable")
	}
	feedbackResult, err := production.feedback.SyncGitHub(ctx, feedbackapp.GitHubCommand{RoutingContext: feedbackapp.RoutingContext{
		RunID: run.ID, ExpectedTaskVersion: run.Execution.CandidateAuthority.TaskVersion, ExpectedRunVersion: run.Version,
		LeaseEpoch: run.Execution.LeaseBinding.Epoch, FrozenPlanDigest: run.Execution.PreparationPlan.ID,
		CurrentPlanDigest: run.Execution.PreparationPlan.ID, FrozenSkillSetDigest: run.Execution.EffectiveProfilesSHA256,
		CurrentSkillSetDigest: run.Execution.EffectiveProfilesSHA256, CurrentDecisionDigest: run.Execution.DecisionContextSHA256,
		CurrentDiffDigest: record.Manifest.DiffSHA256, CorrectionPolicy: *run.Execution.CorrectionPolicy, NowMillis: nowMillis}})
	if err != nil || feedbackResult.Progressed || feedbackResult.CorrectionRouted || feedbackResult.NeedsHumanDecision {
		return feedbackResult.Progressed || feedbackResult.CorrectionRouted || feedbackResult.NeedsHumanDecision, err
	}
	if run.Execution.Publication.Evidence == nil || !run.Execution.Publication.Evidence.Ready {
		result, err := production.publication.Reconcile(ctx, run.ID, nowMillis)
		return result.Progressed, err
	}
	if run.Execution.Integration == nil {
		result, err := production.integration.Admit(ctx, integrationapp.AdmitCommand{RunID: run.ID,
			ExpectedRunVersion: run.Version, LeaseEpoch: run.Execution.LeaseBinding.Epoch, NowMillis: nowMillis})
		return result.Progressed, err
	}
	if run.Execution.Integration.Evidence == nil {
		result, err := production.integration.Step(ctx, integrationapp.StepCommand{RunID: run.ID,
			ExpectedRunVersion: run.Version, LeaseEpoch: run.Execution.LeaseBinding.Epoch, NowMillis: nowMillis})
		return result.Progressed, err
	}
	return production.reconcileCleanup(ctx, run, nowMillis)
}

func (production *Production) reconcileDirect(ctx context.Context, run domain.Run, nowMillis int64) (bool, error) {
	ciSlot := productionStableID("direct-ci-slot", run.ID, run.CurrentCandidateID)
	if run.Execution.Review == nil {
		root := filepath.Join(production.runtimeRoot, "reviews", run.ID)
		if err := os.MkdirAll(root, 0o700); err != nil {
			return false, errors.New("review root is unavailable")
		}
		result, err := production.review.Admit(ctx, reviewapp.AdmitCommand{RunID: run.ID, ExpectedRunVersion: run.Version,
			CoordinatorUUID: production.coordinator, ReviewOwnerUUID: UUID("direct-review-owner", run.ID, run.CurrentCandidateID),
			CISlotID: ciSlot, ReviewerRoot: root, CheckoutPath: filepath.Join(root, "candidate"),
			CriterionIDs: run.Execution.CriterionIDs, ProbePlan: []domainreview.Probe{}, NowMillis: nowMillis})
		return result.Progressed, err
	}
	if run.Execution.Review.CIObservation == nil {
		ci, err := production.directCI(ctx, run, nowMillis)
		if err != nil {
			return false, err
		}
		result, err := production.review.ObserveCI(ctx, run.ID, production.coordinator, ci, nowMillis)
		return result.Progressed, err
	}
	if run.Execution.Review.Evidence == nil {
		if progressed, routeErr := production.routeReviewClaim(ctx, run, nowMillis); progressed || routeErr != nil {
			return progressed, routeErr
		}
		result, err := production.review.Reconcile(ctx, run.ID, production.coordinator, nowMillis)
		return result.Progressed, err
	}
	if !reviewCleanupComplete(run.Execution.Review) {
		result, err := production.review.Cleanup(ctx, run.ID, production.coordinator, nowMillis)
		return result.Progressed, err
	}
	if run.Execution.Review.Evidence.Verdict != domainreview.VerdictApproveCandidate {
		return production.routeGateCorrection(ctx, run, nowMillis)
	}
	if run.Execution.DirectDelivery == nil {
		if run.Execution.DirectDeliveryPolicy == nil {
			return false, errors.New("direct delivery policy is unavailable")
		}
		result, err := production.direct.Admit(ctx, directapp.AdmitCommand{RunID: run.ID, ExpectedRunVersion: run.Version,
			LeaseEpoch: run.Execution.LeaseBinding.Epoch, Policy: *run.Execution.DirectDeliveryPolicy, NowMillis: nowMillis})
		return result.Progressed, err
	}
	if run.Execution.DirectDelivery.Evidence == nil {
		result, err := production.direct.Step(ctx, directapp.StepCommand{RunID: run.ID, ExpectedRunVersion: run.Version,
			LeaseEpoch: run.Execution.LeaseBinding.Epoch, NowMillis: nowMillis})
		return result.Progressed, err
	}
	return production.reconcileCleanup(ctx, run, nowMillis)
}

func (production *Production) directCI(ctx context.Context, run domain.Run, nowMillis int64) (domainreview.CIObservation, error) {
	state := run.Execution.Review
	if state == nil || run.CurrentCandidateID == "" {
		return domainreview.CIObservation{}, errors.New("direct validation binding is unavailable")
	}
	command := exec.CommandContext(ctx, "git", "-c", "core.hooksPath=/dev/null", "-c", "core.fsmonitor=false",
		"-C", run.Execution.WorktreePath, "diff", "--check", run.BaseSHA+".."+state.Binding.CandidateSHA)
	command.Env = []string{"LC_ALL=C", "LANG=C", "PATH=" + os.Getenv("PATH"), "GIT_CONFIG_NOSYSTEM=1", "GIT_OPTIONAL_LOCKS=0"}
	outcome := "passed"
	if err := command.Run(); err != nil {
		outcome = "failed"
	}
	value := domainreview.SealCIObservation(domainreview.CIObservation{ID: productionStableID("direct-ci", run.ID, run.CurrentCandidateID),
		SlotID: state.Binding.CISlotID, CandidateSHA: state.Binding.CandidateSHA, BaseSHA: state.Binding.BaseSHA,
		TreeSHA: state.Binding.TreeSHA, ManifestSHA256: state.Binding.ManifestSHA256,
		WorkflowRunID: strconv.FormatInt(nowMillis, 10), RequiredChecks: []string{"director-direct-git-diff-check"},
		Status: outcome, Authoritative: true, Complete: true, ObservedAtMillis: nowMillis})
	if !domainreview.ValidCIObservation(value, state.Binding) {
		return domainreview.CIObservation{}, errors.New("direct validation observation is invalid")
	}
	return value, nil
}

func (production *Production) reconcileCleanup(ctx context.Context, run domain.Run, nowMillis int64) (bool, error) {
	if run.Execution.CleanupPolicy == nil {
		return false, errors.New("cleanup policy is unavailable")
	}
	if run.Execution.Cleanup == nil {
		result, err := production.cleanup.Admit(ctx, cleanupapp.AdmitCommand{RequestID: productionStableID("workflow-cleanup", run.ID, run.CurrentCandidateID),
			RunID: run.ID, ExpectedRunVersion: run.Version, LeaseEpoch: run.Execution.LeaseBinding.Epoch,
			Trigger: domaincleanup.TriggerIntegrated, LifecycleState: domaincleanup.LifecycleActive,
			Policy: *run.Execution.CleanupPolicy, OwnershipSHA256: domaincleanup.DigestText(run.ID + "\x1fcleanup"), NowMillis: nowMillis})
		return result.Progressed, err
	}
	if run.Execution.Cleanup.Phase != domaincleanup.PhaseComplete && run.Execution.Cleanup.Phase != domaincleanup.PhaseRetained {
		result, err := production.cleanup.Step(ctx, cleanupapp.StepCommand{RunID: run.ID, ExpectedRunVersion: run.Version,
			LeaseEpoch: run.Execution.LeaseBinding.Epoch, NowMillis: nowMillis})
		return result.Progressed, err
	}
	if err := production.artifacts.CleanupRunArtifacts(ctx, run.Execution.Scope, run.Execution.RepositoryBindingHash,
		[]execution.Effect{run.Execution.Boundary, run.Execution.Setup}); err != nil {
		return false, err
	}
	result, err := production.controller.Step(ctx, run.ID, nowMillis)
	return result.Progressed, err
}

// ReconcileRun advances at most one governed transition. Repeated calls are
// restart-safe because every application service rereads the exact Run and
// records intent before a possible external handoff.
func (production *Production) ReconcileRun(ctx context.Context, runID string) (bool, error) {
	run, err := production.store.Run(ctx, runID)
	if err != nil || run.Execution.Terminal {
		return false, err
	}
	now := time.Now().UnixMilli()
	if run.Execution.Control.SchemaVersion != "" && run.Execution.Control.Phase != execution.ControlComplete {
		result, controlErr := production.controller.Step(ctx, run.ID, now)
		return result.Progressed, controlErr
	}
	if run.Execution.NeedsYou != nil {
		return false, nil
	}
	if run.CurrentCandidateID == "" {
		if progressed, routeErr := production.routeWorkerClaim(ctx, run, now); progressed || routeErr != nil {
			return progressed, routeErr
		}
		result, err := production.controller.Step(ctx, run.ID, now)
		return result.Progressed, err
	}
	run, err = production.candidateAuthority(ctx, run, now)
	if err != nil {
		return false, err
	}
	if state := run.Execution.Correction; state != nil {
		command := correctionapp.TransitionCommand{RunID: run.ID, ExpectedRunVersion: run.Version,
			LeaseEpoch: run.Execution.LeaseBinding.Epoch, NowMillis: now}
		switch state.Phase {
		case domaincorrection.PhaseReady:
			result, reconcileErr := production.correction.AdmitAttempt(ctx, command)
			return result.Progressed, reconcileErr
		case domaincorrection.PhaseAwaitingOutput:
			return production.routeCorrectionOutput(ctx, run, now)
		case domaincorrection.PhasePrompt, domaincorrection.PhaseCandidatePending, domaincorrection.PhaseCandidateObserved:
			result, reconcileErr := production.correction.Reconcile(ctx, command)
			return result.Progressed, reconcileErr
		case domaincorrection.PhaseNeedsYou:
			return false, nil
		case domaincorrection.PhaseGatesRequired:
			// The correction service has admitted and invalidated authority for
			// the new exact Candidate. Continue below through fresh CI/Review.
		default:
			return false, errors.New("correction phase is invalid")
		}
	}
	switch run.Execution.DeliveryMode {
	case domainconfig.DeliveryPullRequest:
		return production.reconcilePullRequest(ctx, run, now)
	case domainconfig.DeliveryDirect:
		return production.reconcileDirect(ctx, run, now)
	default:
		return false, errors.New("delivery mode is invalid")
	}
}
