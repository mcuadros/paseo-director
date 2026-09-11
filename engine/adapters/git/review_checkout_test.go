// SPDX-License-Identifier: Apache-2.0

package git

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	reviewport "github.com/mcuadros/director-engine/ports/review"
)

func reviewRun(t *testing.T, directory string, arguments ...string) string {
	t.Helper()
	command := exec.Command("git", arguments...)
	command.Dir = directory
	command.Env = append(os.Environ(), "GIT_CONFIG_NOSYSTEM=1")
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v: %s", arguments, err, output)
	}
	return strings.TrimSpace(string(output))
}

func reviewRepository(t *testing.T) (string, string, string) {
	t.Helper()
	root := t.TempDir()
	source := filepath.Join(root, "source")
	if err := os.Mkdir(source, 0o700); err != nil {
		t.Fatal(err)
	}
	reviewRun(t, source, "init", "-b", "main")
	reviewRun(t, source, "config", "user.name", "Review Fixture")
	reviewRun(t, source, "config", "user.email", "review@example.invalid")
	if err := os.WriteFile(filepath.Join(source, "source.txt"), []byte("base\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	reviewRun(t, source, "add", "source.txt")
	reviewRun(t, source, "commit", "-m", "base")
	if err := os.WriteFile(filepath.Join(source, "source.txt"), []byte("candidate\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	reviewRun(t, source, "add", "source.txt")
	reviewRun(t, source, "commit", "-m", "candidate")
	return root, source, reviewRun(t, source, "rev-parse", "HEAD")
}

func reviewCheckoutRequest(t *testing.T) (reviewport.CheckoutRequest, string) {
	t.Helper()
	root, source, candidate := reviewRepository(t)
	return reviewport.CheckoutRequest{
		ReviewKey: "review-fixture", BindingSHA256: strings.Repeat("a", 64), SourcePath: source, PrimaryPath: source,
		ReviewerRoot: filepath.Join(root, "reviewers"), CheckoutPath: filepath.Join(root, "reviewers", "checkout"),
		CandidateSHA: candidate, TreeSHA: reviewRun(t, source, "show", "-s", "--format=%T", candidate),
		PrimaryHeadSHA: candidate, OwnerUUID: "33333333-3333-4333-8333-333333333333",
	}, source
}

func TestReviewerCheckoutCreatesOnlyDetachedExactCandidateAndCleansExactOwner(t *testing.T) {
	request, source := reviewCheckoutRequest(t)
	adapter := &ReviewerCheckout{}
	before := reviewRun(t, source, "rev-parse", "HEAD") + "\n" + reviewRun(t, source, "status", "--porcelain=v2", "--untracked-files=all")
	if err := adapter.CreateCheckout(context.Background(), request); err != nil {
		t.Fatal(err)
	}
	observation, err := adapter.ObserveCheckout(context.Background(), request)
	if err != nil || observation.Status != reviewport.CheckoutExact || !observation.Evidence.Detached || !observation.Evidence.Clean {
		t.Fatalf("observation = %#v, %v", observation, err)
	}
	if branch := reviewRun(t, request.CheckoutPath, "branch", "--show-current"); branch != "" {
		t.Fatalf("checkout branch = %q", branch)
	}
	if after := reviewRun(t, source, "rev-parse", "HEAD") + "\n" + reviewRun(t, source, "status", "--porcelain=v2", "--untracked-files=all"); after != before {
		t.Fatalf("primary checkout changed: before %q after %q", before, after)
	}
	if err := adapter.CreateCheckout(context.Background(), request); err == nil {
		t.Fatal("duplicate checkout create admitted")
	}
	if err := adapter.RemoveCheckout(context.Background(), request, observation.Evidence); err != nil {
		t.Fatal(err)
	}
	observation, err = adapter.ObserveCheckout(context.Background(), request)
	if err != nil || observation.Status != reviewport.CheckoutAbsent {
		t.Fatalf("cleanup observation = %#v, %v", observation, err)
	}
}

func TestReviewerCheckoutRefusesDirtyMovingAndOrphanedResources(t *testing.T) {
	request, source := reviewCheckoutRequest(t)
	adapter := &ReviewerCheckout{}
	if err := adapter.CreateCheckout(context.Background(), request); err != nil {
		t.Fatal(err)
	}
	exact, err := adapter.ObserveCheckout(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(request.CheckoutPath, "orphan.txt"), []byte("preserve"), 0o600); err != nil {
		t.Fatal(err)
	}
	dirty, err := adapter.ObserveCheckout(context.Background(), request)
	if err != nil || dirty.Status != reviewport.CheckoutDifferent {
		t.Fatalf("dirty = %#v, %v", dirty, err)
	}
	if err := adapter.RemoveCheckout(context.Background(), request, exact.Evidence); err == nil {
		t.Fatal("dirty checkout cleanup admitted")
	}
	if _, err := os.Stat(filepath.Join(request.CheckoutPath, "orphan.txt")); err != nil {
		t.Fatal("orphan was not preserved")
	}
	if err := os.Remove(filepath.Join(request.CheckoutPath, "orphan.txt")); err != nil {
		t.Fatal(err)
	}
	// Moving the primary ref invalidates every later exact checkout observation.
	reviewRun(t, source, "checkout", "--detach", "HEAD^")
	moved, err := adapter.ObserveCheckout(context.Background(), request)
	if err != nil || moved.Status != reviewport.CheckoutAmbiguous {
		t.Fatalf("moved primary = %#v, %v", moved, err)
	}
	if _, err := os.Stat(request.CheckoutPath); err != nil {
		t.Fatal("ambiguous checkout was removed")
	}
}

func TestReviewerCheckoutRefusesPrimaryAliasAndOwnerMarkerReplacement(t *testing.T) {
	request, _ := reviewCheckoutRequest(t)
	adapter := &ReviewerCheckout{}
	aliased := request
	aliased.CheckoutPath = aliased.PrimaryPath
	if err := adapter.CreateCheckout(context.Background(), aliased); err == nil {
		t.Fatal("primary checkout alias admitted")
	}
	if err := adapter.CreateCheckout(context.Background(), request); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(ownerPath(request.CheckoutPath), 0o644); err != nil {
		t.Fatal(err)
	}
	observation, err := adapter.ObserveCheckout(context.Background(), request)
	if err != nil || observation.Status != reviewport.CheckoutDifferent {
		t.Fatalf("replaced owner = %#v, %v", observation, err)
	}
	if _, err := os.Stat(request.CheckoutPath); err != nil {
		t.Fatal("foreign checkout was not preserved")
	}
}
