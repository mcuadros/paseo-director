// SPDX-License-Identifier: Apache-2.0

package execution

import (
	"context"
	"errors"

	"github.com/mcuadros/director-engine/domain"
	candidatedomain "github.com/mcuadros/director-engine/domain/candidate"
	publicationdomain "github.com/mcuadros/director-engine/domain/publication"
	reviewdomain "github.com/mcuadros/director-engine/domain/review"
	gitport "github.com/mcuadros/director-engine/ports/git"
)

const CandidateAuthorityCommandSchemaVersion = "director.application.candidate-authority/v1"

// CandidateAuthorityCommand fences one reconciliation to the exact Run and
// Project lease versions. It does not carry paths, credentials, or authority
// supplied by an adapter.
type CandidateAuthorityCommand struct {
	SchemaVersion      string
	RequestID          string
	RunID              string
	ExpectedRunVersion uint64
	LeaseEpoch         uint64
	NowMillis          int64
}

type CandidateAuthorityResult struct {
	Run         domain.Run
	Invalidated bool
	Code        candidatedomain.Code
}

// ReconcileCandidateAuthority re-observes exact Git facts and clears every
// downstream authority if Candidate, base, branch, Task version,
// configuration, decisions, findings, tree, diff, or changed paths drift.
func (controller *Controller) ReconcileCandidateAuthority(
	ctx context.Context, command CandidateAuthorityCommand,
) (CandidateAuthorityResult, error) {
	if command.SchemaVersion != CandidateAuthorityCommandSchemaVersion ||
		!identifierPattern.MatchString(command.RequestID) || !identifierPattern.MatchString(command.RunID) ||
		command.LeaseEpoch == 0 || command.NowMillis < 0 {
		return CandidateAuthorityResult{}, errors.New("Candidate authority command is invalid")
	}
	run, err := controller.store.Run(ctx, command.RunID)
	if err != nil {
		return CandidateAuthorityResult{}, err
	}
	if run.Version != command.ExpectedRunVersion || run.Execution.LeaseBinding.Epoch != command.LeaseEpoch {
		return CandidateAuthorityResult{Run: run}, ErrProjectLeaseUnavailable
	}
	repositoryDrift := false
	if err := controller.currentExecutionAuthority(ctx, run, command.NowMillis); err != nil {
		if !errors.Is(err, ErrRepositoryBindingChanged) {
			return CandidateAuthorityResult{Run: run}, err
		}
		repositoryDrift = true
	}
	if run.CurrentCandidateID == "" || run.Execution.CandidateAuthority == nil || run.Execution.CandidateClaim == nil {
		return CandidateAuthorityResult{Run: run}, errors.New("Run has no current Candidate authority")
	}
	stored, err := controller.store.Candidate(ctx, run.CurrentCandidateID)
	if err != nil {
		return CandidateAuthorityResult{Run: run}, err
	}
	task, err := controller.store.Task(ctx, run.TaskID)
	if err != nil {
		return CandidateAuthorityResult{Run: run}, err
	}
	contextFacts := candidatedomain.AuthorityContext{
		CandidateID: stored.ID, BindingSHA256: stored.Manifest.BindingSHA256,
		CandidateSHA: stored.CommitSHA, BaseSHA: run.BaseSHA, Branch: run.Execution.Branch,
		TaskVersion: task.Version, ConfigurationSHA256: run.Execution.PrimarySession.ConfigurationSHA256,
		DecisionsSHA256: run.Execution.DecisionContextSHA256,
		FindingsSHA256:  run.Execution.FindingContextSHA256,
	}
	code := candidatedomain.CodeOK
	if repositoryDrift {
		contextFacts.BindingSHA256 = ""
		code = candidatedomain.CodeRepositoryMismatch
	} else if controller.candidateGit == nil {
		contextFacts.BindingSHA256 = ""
		code = candidatedomain.CodeGitUnavailable
	} else {
		observation, observeErr := controller.candidateGit.ObserveCandidate(ctx, gitport.CandidateRequest{
			Claim: *run.Execution.CandidateClaim, Repository: run.Execution.RepositoryBinding,
			RepositoryBindingSHA256: run.Execution.RepositoryBindingHash, TaskStoreNowMillis: command.NowMillis,
		})
		if observeErr != nil {
			return CandidateAuthorityResult{Run: run}, observeErr
		}
		decision := candidatedomain.Evaluate(*run.Execution.CandidateClaim, observation, run.Execution.RepositoryBindingHash, command.NowMillis)
		if decision.Kind != candidatedomain.DecisionAdmit || decision.Manifest == nil ||
			!candidatedomain.SameImmutableContent(stored.Manifest, *decision.Manifest) {
			contextFacts.BindingSHA256 = ""
			code = decision.Code
			if code == candidatedomain.CodeOK {
				code = candidatedomain.CodeTOCTOU
			}
		}
	}
	updated, changed := candidatedomain.ReconcileAuthority(*run.Execution.CandidateAuthority, contextFacts)
	if !changed {
		return CandidateAuthorityResult{Run: run, Code: code}, nil
	}
	if code == candidatedomain.CodeOK {
		code = updated.InvalidationCode
	}
	if run.Execution.Publication != nil && publicationdomain.DispatchInFlight(*run.Execution.Publication) {
		return CandidateAuthorityResult{Run: run}, errors.New("publication dispatch must reconcile before Candidate invalidation")
	}
	next := run
	next.Execution.CandidateAuthority = &updated
	if next.Execution.Review != nil {
		invalidated := reviewdomain.Invalidate(*next.Execution.Review, string(code))
		next.Execution.Review = &invalidated
	}
	if next.Execution.Publication != nil {
		invalidated := publicationdomain.Invalidate(*next.Execution.Publication, string(code))
		next.Execution.Publication = &invalidated
	}
	if err := controller.persistRun(ctx, run, next, "candidate.authority_invalidated"); err != nil {
		return CandidateAuthorityResult{Run: run}, err
	}
	next.Version = run.Version + 1
	return CandidateAuthorityResult{Run: next, Invalidated: true, Code: code}, nil
}
