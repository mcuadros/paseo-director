// SPDX-License-Identifier: Apache-2.0

package dolt

import (
	"strings"
	"testing"

	"github.com/mcuadros/director-engine/domain"
	"github.com/mcuadros/director-engine/domain/candidate"
	domainconfig "github.com/mcuadros/director-engine/domain/configuration"
	"github.com/mcuadros/director-engine/domain/correction"
	"github.com/mcuadros/director-engine/domain/execution"
	feedbackdomain "github.com/mcuadros/director-engine/domain/feedback"
	publicationdomain "github.com/mcuadros/director-engine/domain/publication"
)

func TestPublicationIntentRoundTripsTheDurableRunRecord(t *testing.T) {
	manifest := candidate.Manifest{SchemaVersion: candidate.ManifestSchemaVersion, CandidateSHA: strings.Repeat("b", 40),
		BaseSHA: strings.Repeat("a", 40), ParentSHA: strings.Repeat("a", 40), TreeSHA: strings.Repeat("c", 40),
		DiffSHA256: strings.Repeat("1", 64), ChangedPathsSHA256: strings.Repeat("2", 64),
		RepositoryBindingSHA256: strings.Repeat("3", 64), ClaimSHA256: strings.Repeat("4", 64),
		ObservationSHA256: strings.Repeat("5", 64), AcceptanceSHA256: strings.Repeat("6", 64),
		ConfigurationSHA256: strings.Repeat("7", 64), ProfileSHA256: strings.Repeat("8", 64),
		ContextSHA256: strings.Repeat("9", 64), DecisionsSHA256: strings.Repeat("d", 64),
		FindingsSHA256: strings.Repeat("e", 64), GraphPolicySHA256: strings.Repeat("f", 64)}
	manifest.BindingSHA256 = candidate.ManifestSHA256(manifest)
	authority := candidate.NewAuthority(0, "candidate-1", "task/dir-m4.5", 3, manifest)
	policy := publicationdomain.NewPolicy("pull_request", true, []string{"release"})
	binding := publicationdomain.SealBinding(publicationdomain.Binding{TaskID: "dir-m4.5", RunID: "run-1", CandidateID: "candidate-1",
		CandidateSHA: manifest.CandidateSHA, BaseSHA: manifest.BaseSHA, TreeSHA: manifest.TreeSHA,
		ManifestSHA256: manifest.BindingSHA256, CandidateGeneration: authority.Generation, TaskVersion: authority.TaskVersion,
		Branch: authority.Branch, BaseRef: "refs/heads/main", RepositoryBindingSHA256: manifest.RepositoryBindingSHA256,
		CanonicalRemote: "https://github.com/example/product", GitHubRepositoryID: 123, GitHubRepositoryNodeID: "R_node",
		RepositoryOwner: "example", RepositoryName: "product", HeadOwner: "example",
		OwnershipSHA256: strings.Repeat("0", 64), PolicySHA256: policy.SHA256})
	template, ok := publicationdomain.RenderTemplate(binding, policy, "Implement publication", "pending", "", "pending", "", []string{})
	if !ok {
		t.Fatal("render publication template")
	}
	publication, ok := publicationdomain.NewState(binding, policy, template, "", nil)
	if !ok {
		t.Fatal("create publication state")
	}
	run := domain.Run{ID: "run-1", TaskID: "dir-m4.5", Number: 1, BaseSHA: manifest.BaseSHA,
		CurrentCandidateID: "candidate-1", Version: 4, Execution: execution.State{SchemaVersion: execution.SchemaVersion,
			Scope:              execution.Scope{ProjectID: "project-1", WorkspaceID: "workspace-1", TaskID: "dir-m4.5", RunID: "run-1"},
			CandidateAuthority: &authority, DeliveryMode: domainconfig.DeliveryPullRequest,
			PublicationPolicy: &policy, Publication: &publication}}
	if err := validateRun(run); err != nil {
		t.Fatalf("validateRun() = %v", err)
	}
	encoded, err := marshalRecord(runData{Number: run.Number, BaseSHA: run.BaseSHA, CurrentCandidateID: run.CurrentCandidateID, Execution: run.Execution})
	if err != nil {
		t.Fatal(err)
	}
	var decoded runData
	if err := decodeRecord(encoded, &decoded); err != nil {
		t.Fatal(err)
	}
	reloaded := domain.Run{ID: run.ID, TaskID: run.TaskID, Number: decoded.Number, BaseSHA: decoded.BaseSHA,
		CurrentCandidateID: decoded.CurrentCandidateID, Execution: decoded.Execution, Version: run.Version}
	if err := validateRun(reloaded); err != nil || reloaded.Execution.Publication == nil ||
		reloaded.Execution.Publication.Binding.BindingSHA256 != publication.Binding.BindingSHA256 {
		t.Fatalf("reloaded publication = %#v, %v", reloaded.Execution.Publication, err)
	}
	feedbackBinding := feedbackdomain.SealBinding(feedbackdomain.Binding{ProjectID: "project-1", WorkspaceID: "workspace-1",
		TaskID: run.TaskID, TaskVersion: authority.TaskVersion, RunID: run.ID, CandidateID: authority.CandidateID,
		CandidateSHA: authority.CandidateSHA, BaseSHA: authority.BaseSHA, CandidateGeneration: authority.Generation,
		ManifestSHA256: authority.BindingSHA256, RepositoryID: binding.GitHubRepositoryID,
		RepositoryNodeID: binding.GitHubRepositoryNodeID, PullRequestNumber: 7})
	item := feedbackdomain.Item{Source: feedbackdomain.SourcePaseoDirect, ExternalID: "feedback-1", RevisionID: "revision-1",
		Actor: feedbackdomain.Actor{Kind: feedbackdomain.ActorHuman, ID: "human-1", Login: "owner", Authenticated: true,
			Attestation: feedbackdomain.AttestationPaseoHuman}, Kind: feedbackdomain.KindComment,
		CandidateSHA: authority.CandidateSHA, BaseSHA: authority.BaseSHA, ContextSHA256: strings.Repeat("a", 64),
		Body: "Feedback invalidates publication before dispatch", Actionable: true, Severity: correction.SeverityP3,
		CreatedAtMillis: 1_000, UpdatedAtMillis: 1_001}
	snapshot := feedbackdomain.SealSnapshot(feedbackdomain.Snapshot{ID: "feedback-snapshot", Source: feedbackdomain.SourcePaseoDirect,
		BindingSHA256: feedbackBinding.BindingSHA256, PageCount: 1, ObservedAtMillis: 1_002,
		MaximumAgeMillis: feedbackdomain.MaximumObservationAge, Items: []feedbackdomain.Item{item}})
	feedback, _, ok := feedbackdomain.Reconcile(nil, feedbackBinding, []feedbackdomain.Snapshot{snapshot}, 1_002)
	if !ok {
		t.Fatal("feedback fixture")
	}
	conflict := reloaded
	conflict.Execution.Feedback = &feedback
	if err := validateRun(conflict); err == nil {
		t.Fatal("unresolved feedback coexisted with active publication authority")
	}
	conflict.Execution.Publication = nil
	conflictAuthority := *conflict.Execution.CandidateAuthority
	bound := func(id string) *candidate.EvidenceBinding {
		return &candidate.EvidenceBinding{ID: id, CandidateID: conflictAuthority.CandidateID,
			CandidateSHA: conflictAuthority.CandidateSHA, BaseSHA: conflictAuthority.BaseSHA,
			Generation: conflictAuthority.Generation, BindingSHA256: conflictAuthority.BindingSHA256}
	}
	conflictAuthority.Downstream.Validation = bound("validation-1")
	conflictAuthority.Downstream.CI = bound("ci-1")
	conflictAuthority.Downstream.Review = bound("review-1")
	conflict.Execution.CandidateAuthority = &conflictAuthority
	if err := validateRun(conflict); err == nil {
		t.Fatal("unresolved feedback retained Validation, CI, or Review authority")
	}
}
