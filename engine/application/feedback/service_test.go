// SPDX-License-Identifier: Apache-2.0

package feedback

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/mcuadros/director-engine/domain"
	"github.com/mcuadros/director-engine/domain/candidate"
	domainconfig "github.com/mcuadros/director-engine/domain/configuration"
	domaincorrection "github.com/mcuadros/director-engine/domain/correction"
	directdomain "github.com/mcuadros/director-engine/domain/directdelivery"
	"github.com/mcuadros/director-engine/domain/execution"
	domainfeedback "github.com/mcuadros/director-engine/domain/feedback"
	integrationdomain "github.com/mcuadros/director-engine/domain/integration"
	publicationdomain "github.com/mcuadros/director-engine/domain/publication"
	"github.com/mcuadros/director-engine/domain/runtimebudget"
	correctionport "github.com/mcuadros/director-engine/ports/correction"
	gitport "github.com/mcuadros/director-engine/ports/git"
	githubport "github.com/mcuadros/director-engine/ports/github"
)

const testPrimary = "11111111-1111-4111-8111-111111111111"

var testLostResponse = errors.New("response lost after durable write")

type memoryStore struct {
	mu         sync.Mutex
	project    domain.Project
	tasks      map[string]domain.Task
	run        domain.Run
	candidate  domain.Candidate
	commands   map[string]domain.CommandResult
	events     []domain.Event
	loseUpdate bool
	loseCreate bool
}

func (store *memoryStore) Project(context.Context, string) (domain.Project, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	return store.project, nil
}
func (store *memoryStore) Task(_ context.Context, id string) (domain.Task, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	value, ok := store.tasks[id]
	if !ok {
		return domain.Task{}, errors.New("Task not found")
	}
	return value, nil
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
	if command.ExpectedVersion != store.run.Version || next.Version != store.run.Version+1 || next.Execution.Feedback == nil ||
		!domainfeedback.ValidState(*next.Execution.Feedback) {
		return domain.CommandResult{Outcome: domain.CommandRejectedVersionConflict, ObservedVersion: store.run.Version}, nil
	}
	store.run = next
	store.events = append(store.events, event)
	result := domain.CommandResult{Outcome: domain.CommandApplied, ObservedVersion: next.Version, EventID: event.ID}
	store.commands[command.IdempotencyKey] = result
	if store.loseUpdate {
		store.loseUpdate = false
		return domain.CommandResult{}, testLostResponse
	}
	return result, nil
}
func (store *memoryStore) CreateTask(_ context.Context, command domain.CommandRequest, task domain.Task, event domain.Event) (domain.CommandResult, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	if prior, ok := store.commands[command.IdempotencyKey]; ok {
		prior.Replay = true
		return prior, nil
	}
	if _, exists := store.tasks[task.ID]; exists {
		return domain.CommandResult{}, errors.New("Task exists")
	}
	store.tasks[task.ID] = task
	store.events = append(store.events, event)
	result := domain.CommandResult{Outcome: domain.CommandApplied, ObservedVersion: task.Version, EventID: event.ID}
	store.commands[command.IdempotencyKey] = result
	if store.loseCreate {
		store.loseCreate = false
		return domain.CommandResult{}, testLostResponse
	}
	return result, nil
}

type fakeCorrection struct {
	mu           sync.Mutex
	store        *memoryStore
	commands     []correctionport.IngestCommand
	loseResponse bool
}

func (router *fakeCorrection) IngestFeedback(_ context.Context, command correctionport.IngestCommand) (correctionport.Result, error) {
	router.mu.Lock()
	defer router.mu.Unlock()
	router.store.mu.Lock()
	defer router.store.mu.Unlock()
	router.commands = append(router.commands, command)
	batch, ok := domaincorrection.Canonicalize(router.store.run.CurrentCandidateID,
		router.store.run.Execution.CandidateAuthority.CandidateSHA, command.CriterionIDs, command.SourceSnapshots, command.Findings)
	if !ok {
		return correctionport.Result{Run: router.store.run}, errors.New("invalid correction batch")
	}
	if state := router.store.run.Execution.Correction; state != nil {
		current, _ := domaincorrection.CurrentBatch(*state)
		if current.SHA256 == batch.SHA256 {
			return correctionport.Result{Run: router.store.run, Replayed: true}, nil
		}
	}
	state, ok := domaincorrection.NewState(router.store.run.TaskID, router.store.run.ID, router.store.run.CurrentCandidateID,
		router.store.run.Execution.CandidateAuthority.CandidateSHA, command.OriginalTaskAgentUUID, command.CriterionIDs,
		command.FrozenPlanDigest, command.FrozenSkillSetDigest, command.CurrentDecisionDigest, command.CurrentDiffDigest, command.Policy, batch)
	if !ok {
		return correctionport.Result{Run: router.store.run}, errors.New("invalid correction state")
	}
	router.store.run.Execution.Correction = &state
	router.store.run.Version++
	result := correctionport.Result{Run: router.store.run, Progressed: true}
	if router.loseResponse {
		router.loseResponse = false
		return result, testLostResponse
	}
	return result, nil
}

type fakeGitHub struct {
	feedback map[domainfeedback.Source][]domainfeedback.Item
	reads    map[domainfeedback.Source]uint64
	pull     publicationdomain.PullRequest
}

func (forge *fakeGitHub) ObserveRepository(_ context.Context, request githubport.RepositoryRequest) (publicationdomain.RepositoryObservation, error) {
	return publicationdomain.SealRepositoryObservation(publicationdomain.RepositoryObservation{ID: "repository-observation", Code: publicationdomain.CodeOK,
		RepositoryID: request.RepositoryID, RepositoryNodeID: request.RepositoryNodeID, Owner: request.Owner, Name: request.Name,
		ViewerLogin: request.ExpectedViewer, Authenticated: true, CanPush: true, CanPullRequests: true, TLSVerified: true,
		APIVersion: "2022-11-28", RateRemaining: 100, RateResetAtMillis: request.TaskStoreNowMillis + 60_000,
		ObservedAtMillis: request.TaskStoreNowMillis, MaximumAgeMillis: publicationdomain.MaximumObservationAgeMS}), nil
}

func (forge *fakeGitHub) ListPullRequests(_ context.Context, request githubport.ListRequest) (publicationdomain.PullRequestPage, error) {
	return publicationdomain.SealPullRequestPage(publicationdomain.PullRequestPage{ID: "pull-page-1", Code: publicationdomain.CodeOK,
		Page: request.Page, Complete: true, PullRequests: []publicationdomain.PullRequest{forge.pull},
		ObservedAtMillis: request.TaskStoreNowMillis, MaximumAgeMillis: publicationdomain.MaximumObservationAgeMS}), nil
}

func (forge *fakeGitHub) ObserveFeedbackPage(_ context.Context, request githubport.FeedbackRequest) (githubport.FeedbackPage, error) {
	forge.reads[request.Source]++
	values := forge.feedback[request.Source]
	start := int((request.Page - 1) * request.PageSize)
	end := min(start+int(request.PageSize), len(values))
	page := githubport.FeedbackPage{ID: fmt.Sprintf("feedback-page-%s-%d", request.Source, request.Page), Code: "ok", Source: request.Source,
		RepositoryID: request.RepositoryID, RepositoryNodeID: request.RepositoryNodeID, PullRequestNumber: request.PullRequestNumber,
		CandidateSHA: request.CandidateSHA, BaseSHA: request.BaseSHA, BindingSHA256: request.BindingSHA256, Page: request.Page,
		ObservedAtMillis: request.TaskStoreNowMillis, MaximumAgeMillis: domainfeedback.MaximumObservationAge}
	if start < len(values) {
		page.Items = slices.Clone(values[start:end])
	}
	if end < len(values) {
		page.NextPage = request.Page + 1
	} else {
		page.Complete = true
	}
	return githubport.SealFeedbackPage(page), nil
}

type fakeBranches struct {
	remote string
	heads  map[string]string
	pushes uint64
}

func (branches *fakeBranches) ObserveRemoteRef(_ context.Context, request gitport.RemoteRefRequest) (publicationdomain.RefObservation, error) {
	oid, exists := branches.heads[request.Branch]
	return publicationdomain.SealRefObservation(publicationdomain.RefObservation{ID: "ref-" + strings.ReplaceAll(request.Branch, "/", "-"),
		Code: publicationdomain.CodeOK, Ref: "refs/heads/" + request.Branch, Exists: exists, OID: oid,
		RemoteCanonical: branches.remote, ObservedAtMillis: request.TaskStoreNowMillis,
		MaximumAgeMillis: publicationdomain.MaximumObservationAgeMS}), nil
}

func (branches *fakeBranches) PushExact(context.Context, gitport.PushRequest) (gitport.PushResult, error) {
	branches.pushes++
	return gitport.PushResult{}, errors.New("feedback must not push")
}

type fixture struct {
	store      *memoryStore
	correction *fakeCorrection
	service    *Service
	routing    RoutingContext
}

func evidence(authority candidate.Authority, id string) *candidate.EvidenceBinding {
	return &candidate.EvidenceBinding{ID: id, CandidateID: authority.CandidateID, CandidateSHA: authority.CandidateSHA,
		BaseSHA: authority.BaseSHA, Generation: authority.Generation, BindingSHA256: authority.BindingSHA256}
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	base, head := strings.Repeat("0", 40), strings.Repeat("1", 40)
	manifestBinding := strings.Repeat("a", 64)
	manifest := candidate.Manifest{CandidateSHA: head, BaseSHA: base, TreeSHA: strings.Repeat("2", 40), BindingSHA256: manifestBinding,
		RepositoryBindingSHA256: strings.Repeat("8", 64),
		ConfigurationSHA256:     strings.Repeat("b", 64), DecisionsSHA256: strings.Repeat("c", 64), FindingsSHA256: candidate.EmptyContextSHA256()}
	authority := candidate.NewAuthority(0, "candidate-1", "task/dir-m4.7-human-feedback", 4, manifest)
	authority.Downstream.Ready = evidence(authority, "ready-1")
	task := domain.Task{ID: "task-1", ProjectID: "project-1", Key: "DIR-1", Title: "Original Task", Objective: "Original objective",
		AcceptanceCriteria: "Original acceptance", WorkspaceIDs: []string{"workspace-1"},
		Parent: &domain.PlanningNodeRef{Kind: domain.PlanningNodeEpic, ID: "epic-1"}, Priority: domain.PriorityHigh, Version: 4}
	lease := &domain.ProjectLease{HolderInstance: "engine-1", HolderProcessIdentity: "process-1",
		Epoch: 1, AcquiredAtMillis: 900, ExpiresAtMillis: 10_000, RenewedAtMillis: 900, DispatchAllowed: true}
	run := domain.Run{ID: "run-1", TaskID: task.ID, Number: 1, BaseSHA: base, CurrentCandidateID: authority.CandidateID, Version: 1,
		Execution: execution.State{SchemaVersion: execution.SchemaVersion, Scope: execution.Scope{ProjectID: task.ProjectID,
			WorkspaceID: task.WorkspaceIDs[0], TaskID: task.ID, RunID: "run-1"}, LeaseBinding: execution.LeaseBinding{
			HolderInstance: "engine-1", HolderProcessIdentity: "process-1", Epoch: 1}, CriterionIDs: []string{"criterion-1"},
			Agent: execution.Effect{ExternalID: testPrimary}, PrimarySession: execution.PrimarySession{NativeAgentID: testPrimary},
			DecisionContextSHA256: strings.Repeat("c", 64), CandidateAuthority: &authority,
			Budget: func() runtimebudget.Ledger {
				value, _ := runtimebudget.NewLedger(runtimebudget.NewPolicy("budget-r1", 10_000, 10_000, 10, 0, 4), 900)
				return value
			}()}}
	store := &memoryStore{project: domain.Project{ID: task.ProjectID, State: "active", Lease: lease}, tasks: map[string]domain.Task{task.ID: task},
		run: run, candidate: domain.Candidate{ID: authority.CandidateID, RunID: run.ID, CommitSHA: head, Manifest: manifest}, commands: map[string]domain.CommandResult{}}
	router := &fakeCorrection{store: store}
	service, err := NewService(store, nil, router)
	if err != nil {
		t.Fatal(err)
	}
	routing := RoutingContext{RunID: run.ID, ExpectedTaskVersion: task.Version, ExpectedRunVersion: run.Version, LeaseEpoch: 1,
		FrozenPlanDigest: strings.Repeat("d", 64), CurrentPlanDigest: strings.Repeat("d", 64),
		FrozenSkillSetDigest: strings.Repeat("e", 64), CurrentSkillSetDigest: strings.Repeat("e", 64),
		CurrentDecisionDigest: strings.Repeat("c", 64), CurrentDiffDigest: strings.Repeat("f", 64),
		CorrectionPolicy: domaincorrection.Policy{AutoFixCIFailures: true, AutoFixReviewFeedback: true, AttemptLimit: 3}, NowMillis: 1_000}
	return &fixture{store: store, correction: router, service: service, routing: routing}
}

func directItem(severity domaincorrection.Severity) domainfeedback.Item {
	return domainfeedback.Item{Source: domainfeedback.SourcePaseoDirect, ExternalID: "message-1", RevisionID: "revision-1",
		Actor: domainfeedback.Actor{Kind: domainfeedback.ActorHuman, ID: "human-1", Login: "owner", Authenticated: true,
			Attestation: domainfeedback.AttestationPaseoHuman}, Kind: domainfeedback.KindComment,
		CandidateSHA: strings.Repeat("1", 40), BaseSHA: strings.Repeat("0", 40), ContextSHA256: strings.Repeat("9", 64),
		Body: "Please cover restart and response loss", Actionable: true, Severity: severity, CreatedAtMillis: 990, UpdatedAtMillis: 995}
}

func TestCurrentPaseoFeedbackRoutesOneCompleteBatchToOriginalPrimary(t *testing.T) {
	fixture := newFixture(t)
	result, err := fixture.service.IngestDirect(context.Background(), DirectCommand{RoutingContext: fixture.routing, Item: directItem(domaincorrection.SeverityP3)})
	if err != nil || !result.CorrectionRouted || result.Feedback == nil || result.Feedback.Phase != domainfeedback.PhaseCorrectionRouted {
		t.Fatalf("result = %#v, %v", result, err)
	}
	if len(fixture.correction.commands) != 1 {
		t.Fatalf("correction calls = %d", len(fixture.correction.commands))
	}
	command := fixture.correction.commands[0]
	if command.OriginalTaskAgentUUID != testPrimary || len(command.SourceSnapshots) != 4 || command.SourceSnapshots[3].Source != domaincorrection.SourceHuman ||
		len(command.Findings) != 1 || command.Findings[0].Source != domaincorrection.SourceHuman {
		t.Fatalf("correction command = %#v", command)
	}
	if result.Run.Execution.CandidateAuthority.Downstream.Ready != nil || result.Run.Execution.CandidateAuthority.Downstream.Feedback == nil {
		t.Fatalf("readiness was not reopened: %#v", result.Run.Execution.CandidateAuthority.Downstream)
	}
}

func TestCurrentFeedbackAtomicallyInvalidatesPRAndDirectDeliveryAuthority(t *testing.T) {
	t.Run("pull request", func(t *testing.T) {
		fixture := newFixture(t)
		installPublication(t, fixture)
		authority := *fixture.store.run.Execution.CandidateAuthority
		authority.Downstream.Validation = evidence(authority, "validation-1")
		authority.Downstream.CI = evidence(authority, "ci-1")
		authority.Downstream.Review = evidence(authority, "review-1")
		fixture.store.run.Execution.CandidateAuthority = &authority
		result, err := fixture.service.IngestDirect(context.Background(), DirectCommand{RoutingContext: fixture.routing,
			Item: directItem(domaincorrection.SeverityP3)})
		if err != nil || !result.CorrectionRouted || result.Run.Execution.Publication != nil ||
			len(result.Run.Execution.PublicationHistory) != 1 || !result.Run.Execution.PublicationHistory[0].Invalidated ||
			result.Run.Execution.PublicationHistory[0].InvalidationCode != "human_feedback" ||
			result.Run.Execution.CandidateAuthority.Downstream.Validation != nil ||
			result.Run.Execution.CandidateAuthority.Downstream.CI != nil ||
			result.Run.Execution.CandidateAuthority.Downstream.Review != nil ||
			result.Run.Execution.CandidateAuthority.Downstream.Publication != nil ||
			result.Run.Execution.CandidateAuthority.Downstream.Ready != nil ||
			result.Run.Execution.CandidateAuthority.Downstream.Integration != nil ||
			result.Run.Execution.CandidateAuthority.Downstream.Feedback == nil {
			t.Fatalf("PR feedback invalidation = %#v, %v", result, err)
		}
	})

	t.Run("direct", func(t *testing.T) {
		fixture := newFixture(t)
		installDirectDelivery(t, fixture, directdomain.IntegrationManual)
		result, err := fixture.service.IngestDirect(context.Background(), DirectCommand{RoutingContext: fixture.routing,
			Item: directItem(domaincorrection.SeverityP3)})
		if err != nil || !result.CorrectionRouted || result.Run.Execution.DirectDelivery != nil ||
			len(result.Run.Execution.DirectDeliveryHistory) != 1 ||
			result.Run.Execution.DirectDeliveryHistory[0].Phase != directdomain.PhaseInvalidated ||
			result.Run.Execution.DirectDeliveryHistory[0].InvalidationCode != "human_feedback" ||
			result.Run.Execution.CandidateAuthority.Downstream.Ready != nil {
			t.Fatalf("direct feedback invalidation = %#v, %v", result, err)
		}
	})

	t.Run("pull-request integration", func(t *testing.T) {
		fixture := newFixture(t)
		installPublication(t, fixture)
		installIntegration(t, fixture, integrationdomain.ModeManual, false)
		result, err := fixture.service.IngestDirect(context.Background(), DirectCommand{RoutingContext: fixture.routing,
			Item: directItem(domaincorrection.SeverityP3)})
		if err != nil || !result.CorrectionRouted || result.Run.Execution.Integration != nil ||
			len(result.Run.Execution.IntegrationHistory) != 1 ||
			result.Run.Execution.IntegrationHistory[0].Phase != integrationdomain.PhaseInvalidated ||
			result.Run.Execution.IntegrationHistory[0].InvalidationCode != "human_feedback" {
			t.Fatalf("integration feedback invalidation = %#v, %v", result, err)
		}
	})
}

func TestFeedbackWaitsForInFlightPRAndDirectDispatchObservation(t *testing.T) {
	t.Run("pull request", func(t *testing.T) {
		fixture := newFixture(t)
		installPublication(t, fixture)
		state := fixture.store.run.Execution.Publication
		state.Ready.Phase, state.Ready.Attempts = publicationdomain.EffectDispatching, 1
		if !publicationdomain.ValidState(*state) || !publicationdomain.DispatchInFlight(*state) {
			t.Fatal("in-flight publication fixture")
		}
		before := fixture.store.run
		_, err := fixture.service.IngestDirect(context.Background(), DirectCommand{RoutingContext: fixture.routing,
			Item: directItem(domaincorrection.SeverityP3)})
		if !errors.Is(err, ErrFeedbackAmbiguous) || fixture.store.run.Version != before.Version ||
			fixture.store.run.Execution.Feedback != nil || len(fixture.correction.commands) != 0 {
			t.Fatalf("in-flight PR feedback = %v / %#v", err, fixture.store.run)
		}
	})

	t.Run("direct", func(t *testing.T) {
		fixture := newFixture(t)
		state := installDirectDelivery(t, fixture, directdomain.IntegrationAutomatic)
		observation := directdomain.SealObservation(directdomain.Observation{EffectID: state.Integration.ID,
			BindingSHA256: state.Binding.SHA256, Attempt: 0, Status: directdomain.ObservationCurrentExpected,
			Code: directdomain.CodeOK, RepositoryID: state.Binding.RepositoryID, TargetRef: state.Binding.TargetRef,
			CurrentSHA: state.Binding.BaseSHA, ObservedAtMillis: 999, MaximumAgeMillis: directdomain.MaximumObservationAgeMS})
		state, ok := directdomain.RecordObservation(state, observation)
		if !ok {
			t.Fatal("record direct observation")
		}
		state, ok = directdomain.BeginDispatch(state)
		if !ok || !directdomain.DispatchInFlight(state) {
			t.Fatal("in-flight direct fixture")
		}
		fixture.store.run.Execution.DirectDelivery = &state
		before := fixture.store.run
		_, err := fixture.service.IngestDirect(context.Background(), DirectCommand{RoutingContext: fixture.routing,
			Item: directItem(domaincorrection.SeverityP3)})
		if !errors.Is(err, ErrFeedbackAmbiguous) || fixture.store.run.Version != before.Version ||
			fixture.store.run.Execution.Feedback != nil || len(fixture.correction.commands) != 0 {
			t.Fatalf("in-flight direct feedback = %v / %#v", err, fixture.store.run)
		}
	})

	t.Run("pull-request integration", func(t *testing.T) {
		fixture := newFixture(t)
		installPublication(t, fixture)
		installIntegration(t, fixture, integrationdomain.ModeAutomatic, true)
		before := fixture.store.run
		_, err := fixture.service.IngestDirect(context.Background(), DirectCommand{RoutingContext: fixture.routing,
			Item: directItem(domaincorrection.SeverityP3)})
		if !errors.Is(err, ErrFeedbackAmbiguous) || fixture.store.run.Version != before.Version ||
			fixture.store.run.Execution.Feedback != nil || len(fixture.correction.commands) != 0 {
			t.Fatalf("in-flight integration feedback = %v / %#v", err, fixture.store.run)
		}
	})
}

func TestP2AndManualPolicyRequireSeparateHumanDecision(t *testing.T) {
	for name, mutate := range map[string]func(*fixture, *domainfeedback.Item){
		"P2": func(_ *fixture, item *domainfeedback.Item) {
			item.Severity, item.RequiresHumanDecision = domaincorrection.SeverityP2, true
		},
		"manual policy": func(f *fixture, _ *domainfeedback.Item) { f.routing.CorrectionPolicy.AutoFixReviewFeedback = false },
	} {
		t.Run(name, func(t *testing.T) {
			fixture := newFixture(t)
			item := directItem(domaincorrection.SeverityP3)
			mutate(fixture, &item)
			result, err := fixture.service.IngestDirect(context.Background(), DirectCommand{RoutingContext: fixture.routing, Item: item})
			if err != nil || !result.NeedsHumanDecision || len(fixture.correction.commands) != 0 || result.Run.Execution.NeedsYou == nil {
				t.Fatalf("parked result = %#v, %v", result, err)
			}
			botDecision := DecisionCommand{RoutingContext: fixture.routing, DecisionID: "decision-1",
				ExpectedStateSHA256: result.Feedback.SHA256, Actor: domainfeedback.Actor{Kind: domainfeedback.ActorBot, ID: "bot", Login: "bot",
					Authenticated: true, Attestation: domainfeedback.AttestationGitHubBot}, AllowCorrection: true}
			botDecision.ExpectedRunVersion = result.Run.Version
			if _, err := fixture.service.ApplyHumanDecision(context.Background(), botDecision); !errors.Is(err, ErrInvalidCommand) {
				t.Fatalf("bot decision error = %v", err)
			}
			humanDecision := botDecision
			humanDecision.Actor = domainfeedback.Actor{Kind: domainfeedback.ActorHuman, ID: "human-1", Login: "owner", Authenticated: true,
				Attestation: domainfeedback.AttestationPaseoHuman}
			approved, err := fixture.service.ApplyHumanDecision(context.Background(), humanDecision)
			if err != nil || !approved.CorrectionRouted || len(fixture.correction.commands) != 1 ||
				!fixture.correction.commands[0].Policy.AutoFixReviewFeedback {
				t.Fatalf("approved result = %#v, %v", approved, err)
			}
		})
	}
}

func TestFeedbackAfterDoneCreatesDeterministicSiblingWithoutMutatingClosedHistory(t *testing.T) {
	fixture := newFixture(t)
	original := fixture.store.tasks["task-1"]
	original.Complete = true
	fixture.store.tasks[original.ID] = original
	before := fixture.store.run
	item := directItem(domaincorrection.SeverityP3)
	item.Body = "Use token=FAKE_REDACTION_FIXTURE_12345 from /tmp/private/feedback"
	first, err := fixture.service.IngestDirect(context.Background(), DirectCommand{RoutingContext: fixture.routing, Item: item})
	if err != nil || first.FollowUpTask == nil || first.FollowUpTask.Parent == nil || first.FollowUpTask.Parent.ID != original.Parent.ID ||
		len(first.FollowUpTask.ExternalReferences) != 2 || first.FollowUpTask.ExternalReferences[0].Provider != "director.discovered-from" {
		t.Fatalf("follow-up = %#v, %v", first, err)
	}
	if fixture.store.run.Version != before.Version || fixture.store.run.Execution.Feedback != nil || len(fixture.correction.commands) != 0 {
		t.Fatal("completed Task/Run history was mutated")
	}
	audit := string(fixture.store.events[len(fixture.store.events)-1].Payload)
	if strings.Contains(audit, "FAKE_REDACTION_FIXTURE_12345") || strings.Contains(audit, "/tmp/private") ||
		!strings.Contains(audit, "[REDACTED_SECRET]") || !strings.Contains(audit, "[REDACTED_PATH]") {
		t.Fatalf("follow-up audit was not redacted: %s", audit)
	}
	second, err := fixture.service.IngestDirect(context.Background(), DirectCommand{RoutingContext: fixture.routing, Item: item})
	if err != nil || second.FollowUpTask == nil || second.FollowUpTask.ID != first.FollowUpTask.ID || !second.Replayed || len(fixture.store.tasks) != 2 {
		t.Fatalf("follow-up replay = %#v, %v", second, err)
	}
}

func TestResponseLossRestartAndConcurrentCASDoNotDuplicateRouting(t *testing.T) {
	fixture := newFixture(t)
	fixture.store.loseUpdate = true
	_, err := fixture.service.IngestDirect(context.Background(), DirectCommand{RoutingContext: fixture.routing, Item: directItem(domaincorrection.SeverityP3)})
	if !errors.Is(err, testLostResponse) {
		t.Fatalf("lost response = %v", err)
	}
	fixture.routing.ExpectedRunVersion = fixture.store.run.Version
	restarted, _ := NewService(fixture.store, nil, fixture.correction)
	result, err := restarted.IngestDirect(context.Background(), DirectCommand{RoutingContext: fixture.routing, Item: directItem(domaincorrection.SeverityP3)})
	if err != nil || !result.CorrectionRouted || len(fixture.correction.commands) != 1 {
		t.Fatalf("restart result = %#v, %v commands=%d", result, err, len(fixture.correction.commands))
	}

	concurrent := newFixture(t)
	var successes int
	var mu sync.Mutex
	var wait sync.WaitGroup
	for range 32 {
		wait.Add(1)
		go func() {
			defer wait.Done()
			value, callErr := concurrent.service.IngestDirect(context.Background(), DirectCommand{RoutingContext: concurrent.routing, Item: directItem(domaincorrection.SeverityP3)})
			if callErr == nil && value.CorrectionRouted {
				mu.Lock()
				successes++
				mu.Unlock()
			}
		}()
	}
	wait.Wait()
	if successes != 1 || len(concurrent.correction.commands) != 1 {
		t.Fatalf("successes=%d correction calls=%d", successes, len(concurrent.correction.commands))
	}
}

func TestResponseLossAfterDeliveryInvalidationAdoptsHistoryWithoutDuplicateCorrection(t *testing.T) {
	fixture := newFixture(t)
	installPublication(t, fixture)
	fixture.store.loseUpdate = true
	_, err := fixture.service.IngestDirect(context.Background(), DirectCommand{RoutingContext: fixture.routing,
		Item: directItem(domaincorrection.SeverityP3)})
	if !errors.Is(err, testLostResponse) || fixture.store.run.Execution.Publication != nil ||
		len(fixture.store.run.Execution.PublicationHistory) != 1 || fixture.store.run.Execution.Feedback == nil ||
		len(fixture.correction.commands) != 0 {
		t.Fatalf("lost delivery invalidation = %v / %#v", err, fixture.store.run.Execution)
	}
	fixture.routing.ExpectedRunVersion = fixture.store.run.Version
	restarted, restartErr := NewService(fixture.store, nil, fixture.correction)
	if restartErr != nil {
		t.Fatal(restartErr)
	}
	result, err := restarted.IngestDirect(context.Background(), DirectCommand{RoutingContext: fixture.routing,
		Item: directItem(domaincorrection.SeverityP3)})
	if err != nil || !result.CorrectionRouted || len(fixture.store.run.Execution.PublicationHistory) != 1 ||
		len(fixture.correction.commands) != 1 || len(fixture.store.run.Execution.Correction.Batches) != 1 {
		t.Fatalf("restarted delivery invalidation = %#v, %v", result, err)
	}
}

func TestDoneFollowUpAdoptsLostCreateResponse(t *testing.T) {
	fixture := newFixture(t)
	original := fixture.store.tasks["task-1"]
	original.Complete = true
	fixture.store.tasks[original.ID] = original
	fixture.store.loseCreate = true
	result, err := fixture.service.IngestDirect(context.Background(), DirectCommand{RoutingContext: fixture.routing, Item: directItem(domaincorrection.SeverityP3)})
	if err != nil || result.FollowUpTask == nil || !result.Replayed || len(fixture.store.tasks) != 2 {
		t.Fatalf("lost-create adoption = %#v, %v", result, err)
	}
}

func TestDoneFollowUpUsesCurrentProjectLeaseAfterEngineTakeover(t *testing.T) {
	fixture := newFixture(t)
	original := fixture.store.tasks["task-1"]
	original.Complete = true
	fixture.store.tasks[original.ID] = original
	fixture.store.project.Lease.HolderInstance = "engine-2"
	fixture.store.project.Lease.HolderProcessIdentity = "process-2"
	fixture.store.project.Lease.Epoch = 2
	fixture.routing.LeaseEpoch = 2
	result, err := fixture.service.IngestDirect(context.Background(), DirectCommand{RoutingContext: fixture.routing, Item: directItem(domaincorrection.SeverityP3)})
	if err != nil || result.FollowUpTask == nil || fixture.store.run.Version != 1 {
		t.Fatalf("takeover follow-up = %#v, %v", result, err)
	}
}

func TestLostCorrectionResponseIsAdoptedWithoutAnotherLogicalBatch(t *testing.T) {
	fixture := newFixture(t)
	fixture.correction.loseResponse = true
	_, err := fixture.service.IngestDirect(context.Background(), DirectCommand{RoutingContext: fixture.routing, Item: directItem(domaincorrection.SeverityP3)})
	if !errors.Is(err, testLostResponse) || fixture.store.run.Execution.Correction == nil ||
		fixture.store.run.Execution.Feedback.Phase != domainfeedback.PhaseCorrectionReady {
		t.Fatalf("lost correction response = %v / %#v", err, fixture.store.run.Execution)
	}
	fixture.routing.ExpectedRunVersion = fixture.store.run.Version
	result, err := fixture.service.IngestDirect(context.Background(), DirectCommand{RoutingContext: fixture.routing, Item: directItem(domaincorrection.SeverityP3)})
	if err != nil || !result.CorrectionRouted || len(fixture.store.run.Execution.Correction.Batches) != 1 || len(fixture.correction.commands) != 2 {
		t.Fatalf("adopted correction = %#v, %v", result, err)
	}
}

func TestStaleLeaseAndIncompletePaginationFailBeforeDurableRouting(t *testing.T) {
	stale := newFixture(t)
	stale.routing.LeaseEpoch = 2
	if _, err := stale.service.IngestDirect(context.Background(), DirectCommand{RoutingContext: stale.routing, Item: directItem(domaincorrection.SeverityP3)}); !errors.Is(err, ErrConcurrentTransition) {
		t.Fatalf("stale lease error = %v", err)
	}
	if stale.store.run.Execution.Feedback != nil || len(stale.correction.commands) != 0 {
		t.Fatal("stale lease mutated feedback")
	}

	truncated := newFixture(t)
	forge := installPublication(t, truncated)
	items := make([]domainfeedback.Item, 0, 1001)
	for index := range 1001 {
		value := directItem(domaincorrection.SeverityP3)
		value.Source, value.Actor.Attestation = domainfeedback.SourceGitHubReviewComment, domainfeedback.AttestationGitHubUser
		value.ExternalID = "comment-" + fmt.Sprint(index)
		items = append(items, value)
	}
	forge.feedback[domainfeedback.SourceGitHubReviewComment] = items
	if _, err := truncated.service.SyncGitHub(context.Background(), GitHubCommand{RoutingContext: truncated.routing}); !errors.Is(err, ErrFeedbackAmbiguous) {
		t.Fatalf("pagination error = %v", err)
	}
	if truncated.store.run.Execution.Feedback != nil || len(truncated.correction.commands) != 0 || forge.reads[domainfeedback.SourceGitHubReviewComment] != 10 {
		t.Fatalf("incomplete pagination mutated state: reads=%v", forge.reads)
	}
}

func TestOtherSourceSnapshotsRemainDeterministicallyOrderedAndComplete(t *testing.T) {
	fixture := newFixture(t)
	result, err := fixture.service.IngestDirect(context.Background(), DirectCommand{RoutingContext: fixture.routing, Item: directItem(domaincorrection.SeverityP3)})
	if err != nil || !result.CorrectionRouted {
		t.Fatal(err)
	}
	sources := make([]domaincorrection.Source, 0, 4)
	for _, snapshot := range fixture.correction.commands[0].SourceSnapshots {
		sources = append(sources, snapshot.Source)
	}
	if !slices.Equal(sources, []domaincorrection.Source{domaincorrection.SourceReview, domaincorrection.SourceValidation, domaincorrection.SourceCI, domaincorrection.SourceHuman}) {
		t.Fatalf("sources = %v", sources)
	}
}

func installPublication(t *testing.T, fixture *fixture) *fakeGitHub {
	t.Helper()
	run := fixture.store.run
	policy := publicationdomain.NewPolicy("pull_request", true, nil)
	binding := publicationdomain.SealBinding(publicationdomain.Binding{TaskID: run.TaskID, RunID: run.ID,
		CandidateID: run.CurrentCandidateID, CandidateSHA: run.Execution.CandidateAuthority.CandidateSHA,
		BaseSHA: run.Execution.CandidateAuthority.BaseSHA, TreeSHA: fixture.store.candidate.Manifest.TreeSHA,
		ManifestSHA256: run.Execution.CandidateAuthority.BindingSHA256, CandidateGeneration: run.Execution.CandidateAuthority.Generation,
		TaskVersion: 4, Branch: "task/dir-m4.7-human-feedback", BaseRef: "refs/heads/main",
		RepositoryBindingSHA256: strings.Repeat("8", 64), CanonicalRemote: "https://github.com/example/product",
		GitHubRepositoryID: 123, GitHubRepositoryNodeID: "R_node", RepositoryOwner: "example", RepositoryName: "product",
		HeadOwner: "example", OwnershipSHA256: strings.Repeat("7", 64), PolicySHA256: policy.SHA256})
	template, ok := publicationdomain.RenderTemplate(binding, policy, "Original Task", "pending", "", "pending", "", []string{})
	if !ok {
		t.Fatal("render publication")
	}
	state, ok := publicationdomain.NewState(binding, policy, template, "", nil)
	if !ok {
		t.Fatal("new publication")
	}
	state.Push.Phase, state.PullRequest.Phase, state.Metadata.Phase = publicationdomain.EffectComplete, publicationdomain.EffectComplete, publicationdomain.EffectComplete
	state.OwnedPullRequest = &publicationdomain.OwnedPullRequest{Number: 7, NodeID: "PR_node_7",
		URL: "https://github.com/example/product/pull/7", MarkerSHA256: publicationdomain.DigestText(publicationdomain.Marker(binding)),
		HeadSHA: binding.CandidateSHA, Draft: true}
	state.Evidence = publicationdomain.EvidenceFor(state, false, 999)
	if state.Evidence == nil || !publicationdomain.ValidState(state) {
		t.Fatalf("publication = %#v", state)
	}
	authority := *fixture.store.run.Execution.CandidateAuthority
	authority.Downstream.Publication = evidence(authority, state.Evidence.ID)
	fixture.store.run.Execution.CandidateAuthority = &authority
	fixture.store.run.Execution.DeliveryMode = domainconfig.DeliveryPullRequest
	fixture.store.run.Execution.PublicationPolicy, fixture.store.run.Execution.Publication = &policy, &state
	forge := &fakeGitHub{feedback: map[domainfeedback.Source][]domainfeedback.Item{}, reads: map[domainfeedback.Source]uint64{},
		pull: publicationdomain.PullRequest{Number: 7, NodeID: "PR_node_7", URL: "https://github.com/example/product/pull/7",
			State: "open", Draft: true, HeadSHA: binding.CandidateSHA, HeadRef: binding.Branch, BaseRef: "main", HeadOwner: binding.HeadOwner,
			HeadRepositoryID: 123, BaseRepositoryID: 123, AuthorLogin: binding.HeadOwner, CreatedByViewer: true,
			MarkerSHA256: publicationdomain.DigestText(publicationdomain.Marker(binding)), MarkerCount: 1,
			TitleSHA256: publicationdomain.DigestText("title"), BodySHA256: publicationdomain.DigestText("body"), UpdatedAtMillis: 999}}
	branches := &fakeBranches{remote: binding.CanonicalRemote, heads: map[string]string{binding.Branch: binding.CandidateSHA, "main": binding.BaseSHA}}
	service, err := NewService(fixture.store, forge, fixture.correction, branches)
	if err != nil {
		t.Fatal(err)
	}
	fixture.service = service
	return forge
}

func installDirectDelivery(t *testing.T, fixture *fixture, mode directdomain.IntegrationMode) directdomain.State {
	t.Helper()
	run := fixture.store.run
	authorized := []string{"refs/heads/main"}
	automatic := []string{}
	if mode == directdomain.IntegrationAutomatic {
		automatic = slices.Clone(authorized)
	}
	policy := directdomain.SealPolicy(directdomain.Policy{DeliveryMode: "direct", IntegrationMode: mode,
		SelectionSource: directdomain.SelectionFrozenRunConfiguration, ConfigurationSHA256: fixture.store.candidate.Manifest.ConfigurationSHA256,
		AuthorizedTargetRefs: authorized, AutomaticTargetRefs: automatic, AttemptLimit: directdomain.MaximumAttempts})
	binding := directdomain.SealBinding(directdomain.Binding{TaskID: run.TaskID, RunID: run.ID, CandidateID: run.CurrentCandidateID,
		CandidateSHA: run.Execution.CandidateAuthority.CandidateSHA, BaseSHA: run.Execution.CandidateAuthority.BaseSHA,
		TreeSHA: fixture.store.candidate.Manifest.TreeSHA, ManifestSHA256: run.Execution.CandidateAuthority.BindingSHA256,
		CandidateGeneration: run.Execution.CandidateAuthority.Generation, TaskVersion: run.Execution.CandidateAuthority.TaskVersion,
		ConfigurationSHA256: fixture.store.candidate.Manifest.ConfigurationSHA256, RepositoryID: "repository-1",
		RepositoryBindingSHA256: strings.Repeat("8", 64), TargetRef: "refs/heads/main", PolicySHA256: policy.SHA256,
		ReviewEvidenceID: "review-evidence-1", ReviewerUUID: "22222222-2222-4222-8222-222222222222",
		CIObservationID: "ci-observation-1", CIObservationSHA256: strings.Repeat("6", 64), LeaseEpoch: 1})
	state, ok := directdomain.NewState(binding, policy)
	if !ok {
		t.Fatal("direct-delivery fixture")
	}
	fixture.store.run.Execution.DeliveryMode = domainconfig.DeliveryDirect
	fixture.store.run.Execution.RepositoryBinding.RepositoryID = binding.RepositoryID
	fixture.store.run.Execution.RepositoryBindingHash = binding.RepositoryBindingSHA256
	fixture.store.run.Execution.DirectDelivery = &state
	return state
}

func installIntegration(t *testing.T, fixture *fixture, mode integrationdomain.Mode, dispatching bool) integrationdomain.State {
	t.Helper()
	run := fixture.store.run
	publication := run.Execution.Publication
	policy, ok := integrationdomain.NewPolicy(mode, fixture.store.candidate.Manifest.ConfigurationSHA256)
	if !ok {
		t.Fatal("integration policy")
	}
	binding := integrationdomain.SealBinding(integrationdomain.Binding{TaskID: run.TaskID, RunID: run.ID, CandidateID: run.CurrentCandidateID,
		CandidateSHA: run.Execution.CandidateAuthority.CandidateSHA, BaseSHA: run.Execution.CandidateAuthority.BaseSHA,
		TreeSHA: fixture.store.candidate.Manifest.TreeSHA, ManifestSHA256: run.Execution.CandidateAuthority.BindingSHA256,
		CandidateGeneration: run.Execution.CandidateAuthority.Generation, TaskVersion: run.Execution.CandidateAuthority.TaskVersion,
		ConfigurationSHA256: fixture.store.candidate.Manifest.ConfigurationSHA256, RepositoryID: "repository-1",
		RepositoryBindingSHA256: strings.Repeat("8", 64), CanonicalRemote: publication.Binding.CanonicalRemote,
		GitHubRepositoryID: 123, GitHubRepositoryNodeID: "R_node", RepositoryOwner: "example", RepositoryName: "product", ViewerLogin: "example",
		Branch: publication.Binding.Branch, BaseRef: publication.Binding.BaseRef, PullRequestNumber: publication.OwnedPullRequest.Number,
		PullRequestNodeID: publication.OwnedPullRequest.NodeID, PublicationEvidenceID: publication.Evidence.ID,
		OwnershipSHA256: publication.Binding.OwnershipSHA256, MarkerSHA256: publication.OwnedPullRequest.MarkerSHA256,
		ValidationPolicySHA256: strings.Repeat("1", 64), ValidationEvidenceID: "validation-1", ValidationEvidenceSHA256: strings.Repeat("2", 64),
		ReviewEvidenceID: "review-1", ReviewerUUID: "22222222-2222-4222-8222-222222222222",
		CIObservationID: "ci-1", CIObservationSHA256: strings.Repeat("3", 64), FeedbackStateSHA256: strings.Repeat("4", 64),
		ReadyEvidenceID: "ready-1", PolicySHA256: policy.SHA256, LeaseEpoch: 1})
	state, ok := integrationdomain.NewState(binding, policy)
	if !ok {
		t.Fatal("integration state")
	}
	if dispatching {
		state.Integration.Attempt = 1
		state.Integration.ConsumedObservationID = "integration-observation-1"
		state.Integration.Phase = integrationdomain.EffectDispatching
		state.Phase = integrationdomain.PhaseDispatching
	}
	if !integrationdomain.ValidState(state) {
		t.Fatal("invalid integration fixture")
	}
	fixture.store.run.Execution.IntegrationPolicy = &policy
	fixture.store.run.Execution.Integration = &state
	return state
}

func TestGitHubSyncConsumesEveryPageAndRoutesOnlyReadOnlyHumanFacts(t *testing.T) {
	fixture := newFixture(t)
	forge := installPublication(t, fixture)
	items := make([]domainfeedback.Item, 0, 101)
	for index := range 101 {
		items = append(items, domainfeedback.Item{Source: domainfeedback.SourceGitHubReviewComment,
			ExternalID: "comment-" + fmt.Sprint(index), RevisionID: "revision-1",
			Actor: domainfeedback.Actor{Kind: domainfeedback.ActorHuman, ID: "U_human", Login: "owner", Authenticated: true,
				Attestation: domainfeedback.AttestationGitHubUser}, Kind: domainfeedback.KindComment,
			CandidateSHA: strings.Repeat("1", 40), BaseSHA: strings.Repeat("0", 40), ContextSHA256: strings.Repeat("6", 64),
			Body: "Human review comment " + fmt.Sprint(index), Actionable: true, Severity: domaincorrection.SeverityP3,
			CreatedAtMillis: 900, UpdatedAtMillis: 950 + int64(index%10)})
	}
	forge.feedback[domainfeedback.SourceGitHubReviewComment] = items
	result, err := fixture.service.SyncGitHub(context.Background(), GitHubCommand{RoutingContext: fixture.routing})
	if err != nil || !result.CorrectionRouted || len(fixture.correction.commands) != 1 || len(fixture.correction.commands[0].Findings) != 101 {
		t.Fatalf("sync = %#v, %v", result, err)
	}
	if forge.reads[domainfeedback.SourceGitHubReview] != 1 || forge.reads[domainfeedback.SourceGitHubReviewComment] != 2 ||
		forge.reads[domainfeedback.SourceGitHubIssueComment] != 1 || fixture.service.git.(*fakeBranches).pushes != 0 {
		t.Fatalf("feedback reads/effects = %#v/%d", forge.reads, fixture.service.git.(*fakeBranches).pushes)
	}
}

func TestGitHubSyncRejectsStaleLiveCandidateAndBaseBeforeCommentReads(t *testing.T) {
	for name, branch := range map[string]string{"Candidate": "task/dir-m4.7-human-feedback", "base": "main"} {
		t.Run(name, func(t *testing.T) {
			fixture := newFixture(t)
			forge := installPublication(t, fixture)
			fixture.service.git.(*fakeBranches).heads[branch] = strings.Repeat("9", 40)
			_, err := fixture.service.SyncGitHub(context.Background(), GitHubCommand{RoutingContext: fixture.routing})
			if !errors.Is(err, ErrFeedbackAmbiguous) || len(forge.reads) != 0 || fixture.store.run.Execution.Feedback != nil ||
				len(fixture.correction.commands) != 0 {
				t.Fatalf("stale %s result: %v reads=%v", name, err, forge.reads)
			}
		})
	}
}

func TestGitHubFeedbackAfterDoneUsesExactClosedPRAndCreatesNewWork(t *testing.T) {
	fixture := newFixture(t)
	original := fixture.store.tasks["task-1"]
	original.Complete = true
	fixture.store.tasks[original.ID] = original
	forge := installPublication(t, fixture)
	forge.pull.State = "closed"
	branches := fixture.service.git.(*fakeBranches)
	branches.heads = map[string]string{"main": strings.Repeat("9", 40)}
	value := directItem(domaincorrection.SeverityP3)
	value.Source, value.Actor.Attestation = domainfeedback.SourceGitHubReviewComment, domainfeedback.AttestationGitHubUser
	value.ExternalID = "closed-pr-comment"
	forge.feedback[domainfeedback.SourceGitHubReviewComment] = []domainfeedback.Item{value}
	result, err := fixture.service.SyncGitHub(context.Background(), GitHubCommand{RoutingContext: fixture.routing})
	if err != nil || len(result.FollowUpTasks) != 1 || result.FollowUpTask == nil || len(fixture.correction.commands) != 0 ||
		fixture.store.run.Version != 1 || branches.pushes != 0 {
		t.Fatalf("post-Done GitHub result = %#v, %v", result, err)
	}
}

func TestMultiplePostDoneRevisionsCreateDistinctSiblingTasksAndReplayExactly(t *testing.T) {
	fixture := newFixture(t)
	original := fixture.store.tasks["task-1"]
	original.Complete = true
	fixture.store.tasks[original.ID] = original
	forge := installPublication(t, fixture)
	first := directItem(domaincorrection.SeverityP3)
	first.Source, first.Actor.Attestation, first.ExternalID = domainfeedback.SourceGitHubReviewComment, domainfeedback.AttestationGitHubUser, "comment-1"
	second := first
	second.ExternalID, second.RevisionID, second.Body = "comment-2", "revision-2", "A distinct follow-up request"
	forge.feedback[domainfeedback.SourceGitHubReviewComment] = []domainfeedback.Item{first, second}
	result, err := fixture.service.SyncGitHub(context.Background(), GitHubCommand{RoutingContext: fixture.routing})
	if err != nil || len(result.FollowUpTasks) != 2 || result.FollowUpTasks[0].ID == result.FollowUpTasks[1].ID || len(fixture.store.tasks) != 3 {
		t.Fatalf("multiple follow-ups = %#v, %v", result, err)
	}
	replay, err := fixture.service.SyncGitHub(context.Background(), GitHubCommand{RoutingContext: fixture.routing})
	if err != nil || len(replay.FollowUpTasks) != 2 || !replay.Replayed || len(fixture.store.tasks) != 3 {
		t.Fatalf("multiple replay = %#v, %v", replay, err)
	}
}
