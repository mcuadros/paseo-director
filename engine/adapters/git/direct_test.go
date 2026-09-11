// SPDX-License-Identifier: Apache-2.0

package git

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	candidatedomain "github.com/mcuadros/director-engine/domain/candidate"
	directdomain "github.com/mcuadros/director-engine/domain/directdelivery"
	directport "github.com/mcuadros/director-engine/ports/directdelivery"
)

func directTarget(t *testing.T, fixture repositoryFixture, remotePath string) (*Adapter, directport.Target) {
	t.Helper()
	adapter := New()
	adapter.deliveryRemoteOverride = remotePath
	candidateObservation := observe(t, adapter, fixture.request)
	decision := candidatedomain.Evaluate(fixture.request.Claim, candidateObservation,
		fixture.request.RepositoryBindingSHA256, fixture.request.TaskStoreNowMillis)
	if decision.Manifest == nil {
		t.Fatal("Candidate manifest rejected")
	}
	policy := directdomain.SealPolicy(directdomain.Policy{DeliveryMode: "direct",
		IntegrationMode:      directdomain.IntegrationAutomatic,
		SelectionSource:      directdomain.SelectionFrozenRunConfiguration,
		ConfigurationSHA256:  fixture.request.Claim.ConfigurationSHA256,
		AuthorizedTargetRefs: []string{"refs/heads/main"}, AutomaticTargetRefs: []string{"refs/heads/main"}, AttemptLimit: 2})
	binding := directdomain.SealBinding(directdomain.Binding{TaskID: fixture.request.Claim.TaskID,
		RunID: fixture.request.Claim.RunID, CandidateID: "candidate-1", CandidateSHA: fixture.candidate,
		BaseSHA: fixture.base, TreeSHA: decision.Manifest.TreeSHA, ManifestSHA256: decision.Manifest.BindingSHA256,
		CandidateGeneration: 1, TaskVersion: fixture.request.Claim.TaskVersion,
		ConfigurationSHA256:     fixture.request.Claim.ConfigurationSHA256,
		RepositoryID:            fixture.request.Repository.RepositoryID,
		RepositoryBindingSHA256: fixture.request.RepositoryBindingSHA256, TargetRef: "refs/heads/main",
		PolicySHA256: policy.SHA256, ReviewEvidenceID: "review-evidence-1",
		ReviewerUUID: "11111111-1111-4111-8111-111111111111", CIObservationID: "ci-observation-1",
		CIObservationSHA256: strings.Repeat("1", 64), LeaseEpoch: fixture.request.Claim.LeaseEpoch})
	target := directport.Target{Binding: binding, CandidateClaim: fixture.request.Claim,
		CandidateManifest: *decision.Manifest, Repository: fixture.request.Repository,
		RepositoryBindingSHA256: fixture.request.RepositoryBindingSHA256, EffectID: directdomain.EffectID(binding),
		TaskStoreNowMillis: fixture.request.TaskStoreNowMillis}
	return adapter, target
}

func bareRemote(t *testing.T, fixture repositoryFixture, objectFormat string) string {
	t.Helper()
	remotePath := filepath.Join(fixture.root, "remote.git")
	arguments := []string{"init", "--bare", "--initial-branch=main"}
	if objectFormat == "sha256" {
		arguments = append(arguments, "--object-format=sha256")
	}
	arguments = append(arguments, remotePath)
	fixtureGit(t, fixture.root, arguments...)
	fixtureGit(t, fixture.source, "push", remotePath, fixture.base+":refs/heads/main")
	return remotePath
}

func TestDirectAdapterPushesOneExactRemoteTargetWithExplicitLease(t *testing.T) {
	for _, objectFormat := range []string{"sha1", "sha256"} {
		t.Run(objectFormat, func(t *testing.T) {
			fixture := newRepositoryFixture(t, objectFormat)
			remotePath := bareRemote(t, fixture, objectFormat)
			adapter, target := directTarget(t, fixture, remotePath)
			observation, err := adapter.Observe(context.Background(), target)
			if err != nil || observation.Status != directdomain.ObservationCurrentExpected ||
				observation.CurrentSHA != fixture.base || !directdomain.ValidObservation(observation, target.Binding, target.EffectID, target.TaskStoreNowMillis) {
				t.Fatalf("precondition observation = %#v, %v", observation, err)
			}
			if err := adapter.Push(context.Background(), directport.PushCommand{Target: target, Attempt: 1,
				ExpectedObservation: observation}); err != nil {
				t.Fatal(err)
			}
			if head := fixtureGit(t, fixture.root, "--git-dir", remotePath, "rev-parse", "refs/heads/main"); head != fixture.candidate {
				t.Fatalf("remote head = %s, want %s", head, fixture.candidate)
			}
			desired, err := adapter.Observe(context.Background(), target)
			if err != nil || desired.Status != directdomain.ObservationDesired || desired.CurrentSHA != fixture.candidate {
				t.Fatalf("desired observation = %#v, %v", desired, err)
			}
		})
	}
}

func TestDirectAdapterRejectsRemoteMovementBetweenObservationAndPush(t *testing.T) {
	fixture := newRepositoryFixture(t, "sha1")
	remotePath := bareRemote(t, fixture, "sha1")
	adapter, target := directTarget(t, fixture, remotePath)
	precondition, err := adapter.Observe(context.Background(), target)
	if err != nil || precondition.Status != directdomain.ObservationCurrentExpected {
		t.Fatalf("precondition = %#v, %v", precondition, err)
	}
	fixtureGit(t, fixture.source, "checkout", "-b", "foreign", fixture.base)
	if err := os.WriteFile(filepath.Join(fixture.source, "FOREIGN.md"), []byte("foreign\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	fixtureGit(t, fixture.source, "add", "FOREIGN.md")
	fixtureGit(t, fixture.source, "commit", "-m", "fixture: foreign")
	foreign := fixtureGit(t, fixture.source, "rev-parse", "HEAD")
	fixtureGit(t, fixture.source, "checkout", "main")
	adapter.beforeDirectPush = func() {
		fixtureGit(t, fixture.source, "push", "--force", remotePath, foreign+":refs/heads/main")
	}
	err = adapter.Push(context.Background(), directport.PushCommand{Target: target, Attempt: 1,
		ExpectedObservation: precondition})
	var failure *directport.DispatchError
	if !errors.As(err, &failure) || failure.Code != directport.FailurePreconditionChanged || failure.PossibleHandoff {
		t.Fatalf("movement error = %#v", err)
	}
	if strings.Contains(err.Error(), remotePath) || strings.Contains(err.Error(), fixture.root) {
		t.Fatalf("bounded error leaked a path: %v", err)
	}
	if head := fixtureGit(t, fixture.root, "--git-dir", remotePath, "rev-parse", "refs/heads/main"); head != foreign {
		t.Fatalf("remote head = %s, want untouched foreign %s", head, foreign)
	}
}

func TestDirectAdapterTreatsRemoteFailureAsUnavailableNotAbsent(t *testing.T) {
	fixture := newRepositoryFixture(t, "sha1")
	missing := filepath.Join(fixture.root, "missing-remote.git")
	adapter, target := directTarget(t, fixture, missing)
	observation, err := adapter.Observe(context.Background(), target)
	if err != nil || observation.Status != directdomain.ObservationUnavailable ||
		observation.Code != directdomain.CodeRemoteUnavailable || observation.Status == directdomain.ObservationAbsent {
		t.Fatalf("missing remote observation = %#v, %v", observation, err)
	}
	if strings.Contains(string(observation.Code), missing) || strings.Contains(observation.ID, missing) {
		t.Fatalf("bounded observation leaked remote path: %#v", observation)
	}
}

func TestDirectAdapterRejectsLocalTaskBranchMovementBeforeRemoteMutation(t *testing.T) {
	fixture := newRepositoryFixture(t, "sha1")
	remotePath := bareRemote(t, fixture, "sha1")
	adapter, target := directTarget(t, fixture, remotePath)
	fixtureGit(t, fixture.source, "update-ref", "refs/heads/task/task-1", fixture.base)
	observation, err := adapter.Observe(context.Background(), target)
	if err != nil || observation.Status != directdomain.ObservationAmbiguous ||
		observation.Code != directdomain.CodeBranchChanged {
		t.Fatalf("moved local branch observation = %#v, %v", observation, err)
	}
	if head := fixtureGit(t, fixture.root, "--git-dir", remotePath, "rev-parse", "refs/heads/main"); head != fixture.base {
		t.Fatalf("remote changed after local branch drift: %s", head)
	}
}
