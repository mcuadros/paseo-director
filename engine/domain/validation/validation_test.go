// SPDX-License-Identifier: Apache-2.0

package validation

import (
	"strings"
	"testing"
)

func validationFixture(t *testing.T) (Binding, Policy, WorkflowScan, CheckScan, StatusScan) {
	t.Helper()
	policy, ok := NewPolicy(99, "maintained-linux-ci", []RequiredCheck{{ID: "linux-ci", Kind: CheckRunKind,
		Name: "Linux CI", AppID: 15368, AppSlug: "github-actions"}}, 60_000)
	if !ok {
		t.Fatal("policy")
	}
	binding := SealBinding(Binding{TaskID: "dir-m4.6", RunID: "run-1", CandidateID: "candidate-1",
		CandidateSHA: strings.Repeat("b", 40), BaseSHA: strings.Repeat("a", 40), TreeSHA: strings.Repeat("c", 40),
		ManifestSHA256: strings.Repeat("1", 64), CandidateGeneration: 1, CISlotID: "ci-slot-1", BaseRef: "refs/heads/main",
		RepositoryBindingSHA256: strings.Repeat("2", 64), CanonicalRemote: "https://github.com/example/product",
		GitHubRepositoryID: 123, GitHubRepositoryNodeID: "R_node", RepositoryOwner: "example", RepositoryName: "product",
		ViewerLogin: "example", PolicySHA256: policy.SHA256})
	workflows := SealWorkflowScan(WorkflowScan{Pages: 1, TotalCount: 1, Runs: []WorkflowRun{{ID: 500, WorkflowID: 99,
		Name: "maintained-linux-ci", HeadSHA: binding.CandidateSHA, HeadRepositoryID: 123, CheckSuiteID: 700,
		Status: "completed", Conclusion: "success", Attempt: 1, StartedAtMillis: 100, UpdatedAtMillis: 200}}})
	checks := SealCheckScan(CheckScan{Pages: 1, TotalCount: 1, Checks: []CheckRun{{ID: 600, Name: "Linux CI",
		HeadSHA: binding.CandidateSHA, SuiteID: 700, SuiteHeadSHA: binding.CandidateSHA, AppID: 15368,
		AppSlug: "github-actions", Status: "completed", Conclusion: "success", DetailsURLSHA256: strings.Repeat("3", 64),
		StartedAtMillis: 100, CompletedAtMillis: 200}}})
	statuses := SealStatusScan(StatusScan{Pages: 1, CombinedState: "checks_only_no_statuses", Statuses: []CommitStatus{}})
	if !ValidBinding(binding) || !ValidPolicy(policy) {
		t.Fatal("fixture invalid")
	}
	return binding, policy, workflows, checks, statuses
}

func repositoryFixture(binding Binding, id string, nowMillis int64) RepositoryObservation {
	return SealRepositoryObservation(RepositoryObservation{ID: id, Code: CodeOK, RepositoryID: binding.GitHubRepositoryID,
		RepositoryNodeID: binding.GitHubRepositoryNodeID, Owner: binding.RepositoryOwner, Name: binding.RepositoryName,
		ViewerLogin: binding.ViewerLogin, Authenticated: true, CanReadChecks: true, TLSVerified: true,
		APIVersion: "2022-11-28", RateRemaining: 5_000, ObservedAtMillis: nowMillis, MaximumAgeMillis: MaximumObservationAgeMS})
}

func TestExactCandidateCheckSuiteAndChecksOnlyEvidence(t *testing.T) {
	binding, policy, workflows, checks, statuses := validationFixture(t)
	repository, repositoryAfter := repositoryFixture(binding, "repository-before", 300), repositoryFixture(binding, "repository-after", 300)
	decision := Evaluate(binding, policy, workflows, checks, statuses, RepositoryObservationSHA256(repository), RepositoryObservationSHA256(repositoryAfter), strings.Repeat("4", 64), strings.Repeat("5", 64), 300)
	if decision.Code != CodeOK || decision.Outcome != OutcomePassed || decision.Evidence == nil ||
		decision.Evidence.WorkflowRunID != "500" || decision.Evidence.StatusRollup != "checks_only_no_statuses" ||
		!ValidEvidence(*decision.Evidence, binding) {
		t.Fatalf("decision = %#v", decision)
	}
	state, ok := NewState(binding, policy, 1)
	if !ok {
		t.Fatal("state")
	}
	state.Phase, state.Code, state.Evidence = PhasePassed, CodeOK, decision.Evidence
	state.Workflows, state.Checks, state.Statuses = &workflows, &checks, &statuses
	state.Repository, state.RepositoryAfter = &repository, &repositoryAfter
	state.BaseBeforeSHA256, state.BaseAfterSHA256 = strings.Repeat("4", 64), strings.Repeat("5", 64)
	if !ValidState(state) {
		t.Fatal("terminal state invalid")
	}
	tampered := CloneState(state)
	tampered.Checks.Checks[0].Conclusion = "failure"
	*tampered.Checks = SealCheckScan(*tampered.Checks)
	if ValidState(tampered) {
		t.Fatal("terminal evidence survived changed Check Run facts")
	}
	ci, ok := ReviewObservation(state)
	if !ok || ci.CandidateSHA != binding.CandidateSHA || ci.BaseSHA != binding.BaseSHA || ci.WorkflowRunID != "500" || ci.Status != "passed" {
		t.Fatalf("review CI = %#v", ci)
	}
}

func TestWrongSHADuplicateWorkflowSuiteAndStatusAmbiguityFailClosed(t *testing.T) {
	binding, policy, workflows, checks, statuses := validationFixture(t)
	baseBefore, baseAfter := strings.Repeat("4", 64), strings.Repeat("5", 64)
	tests := []struct {
		name   string
		mutate func(*WorkflowScan, *CheckScan, *StatusScan)
		code   Code
	}{
		{"workflow SHA", func(w *WorkflowScan, _ *CheckScan, _ *StatusScan) {
			w.Runs[0].HeadSHA = strings.Repeat("d", 40)
			*w = SealWorkflowScan(*w)
		}, CodeWorkflowSHAMismatch},
		{"duplicate workflow", func(w *WorkflowScan, _ *CheckScan, _ *StatusScan) {
			w.Runs = append(w.Runs, w.Runs[0])
			w.TotalCount = 2
			*w = SealWorkflowScan(*w)
		}, CodeWorkflowAmbiguous},
		{"check SHA", func(_ *WorkflowScan, c *CheckScan, _ *StatusScan) {
			c.Checks[0].HeadSHA = strings.Repeat("d", 40)
			*c = SealCheckScan(*c)
		}, CodeCheckSHAMismatch},
		{"suite mismatch", func(_ *WorkflowScan, c *CheckScan, _ *StatusScan) { c.Checks[0].SuiteID++; *c = SealCheckScan(*c) }, CodeCheckSuiteAmbiguous},
		{"check status collision", func(_ *WorkflowScan, _ *CheckScan, s *StatusScan) {
			s.TotalCount = 1
			s.CombinedState = "success"
			s.Statuses = []CommitStatus{{ID: 1, Context: "Linux CI", SHA: binding.CandidateSHA, State: "success", CreatorID: 9, CreatorLogin: "legacy", TargetURLSHA256: strings.Repeat("6", 64), UpdatedAtMillis: 200}}
			*s = SealStatusScan(*s)
		}, CodeCheckAmbiguous},
		{"status SHA", func(_ *WorkflowScan, _ *CheckScan, s *StatusScan) {
			s.TotalCount = 1
			s.CombinedState = "success"
			s.Statuses = []CommitStatus{{ID: 1, Context: "legacy", SHA: strings.Repeat("d", 40), State: "success", CreatorID: 9, CreatorLogin: "legacy", TargetURLSHA256: strings.Repeat("6", 64), UpdatedAtMillis: 200}}
			*s = SealStatusScan(*s)
		}, CodeStatusSHAMismatch},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			w, c, s := workflows, checks, statuses
			w.Runs = append(make([]WorkflowRun, 0, len(workflows.Runs)), workflows.Runs...)
			c.Checks = append(make([]CheckRun, 0, len(checks.Checks)), checks.Checks...)
			s.Statuses = append(make([]CommitStatus, 0, len(statuses.Statuses)), statuses.Statuses...)
			test.mutate(&w, &c, &s)
			if decision := Evaluate(binding, policy, w, c, s, strings.Repeat("6", 64), strings.Repeat("7", 64), baseBefore, baseAfter, 300); decision.Code != test.code || decision.Evidence != nil {
				t.Fatalf("decision = %#v, want %s valid=%v/%v/%v", decision, test.code, validWorkflowScan(w), validCheckScan(c), validStatusScan(s))
			}
		})
	}
}

func TestPendingFailedTimedOutAndRedactedIdentities(t *testing.T) {
	binding, policy, workflows, checks, statuses := validationFixture(t)
	before, after := strings.Repeat("4", 64), strings.Repeat("5", 64)
	workflows.Runs[0].Status, workflows.Runs[0].Conclusion = "in_progress", ""
	workflows = SealWorkflowScan(workflows)
	if result := Evaluate(binding, policy, workflows, checks, statuses, strings.Repeat("6", 64), strings.Repeat("7", 64), before, after, 300); result.Code != CodePending || result.Outcome != OutcomePending {
		t.Fatalf("pending = %#v", result)
	}
	workflows.Runs[0].Status, workflows.Runs[0].Conclusion = "completed", "failure"
	workflows = SealWorkflowScan(workflows)
	if result := Evaluate(binding, policy, workflows, checks, statuses, strings.Repeat("6", 64), strings.Repeat("7", 64), before, after, 300); result.Code != CodeFailed || result.Outcome != OutcomeFailed || result.Evidence == nil {
		t.Fatalf("failed = %#v", result)
	}
	workflows.Runs[0].Conclusion = "timed_out"
	workflows = SealWorkflowScan(workflows)
	if result := Evaluate(binding, policy, workflows, checks, statuses, strings.Repeat("6", 64), strings.Repeat("7", 64), before, after, 300); result.Code != CodeTimedOut || result.Outcome != OutcomeTimedOut {
		t.Fatalf("timeout = %#v", result)
	}
	checks.Checks[0].Name = "token=github_pat_abcdefghijklmnop"
	checks = SealCheckScan(checks)
	if result := Evaluate(binding, policy, workflows, checks, statuses, strings.Repeat("6", 64), strings.Repeat("7", 64), before, after, 300); result.Code != CodeResponseUnknown || result.Evidence != nil {
		t.Fatalf("secret check = %#v", result)
	}
}
