// SPDX-License-Identifier: Apache-2.0

package git

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	domainreview "github.com/mcuadros/director-engine/domain/review"
	reviewport "github.com/mcuadros/director-engine/ports/review"
)

const reviewerOwnerSchema = "director.review-checkout-owner/v1"

type reviewerOwner struct {
	SchemaVersion string `json:"schemaVersion"`
	ReviewKey     string `json:"reviewKey"`
	BindingSHA256 string `json:"bindingSha256"`
	ReviewerUUID  string `json:"reviewerUuid"`
	CandidateSHA  string `json:"candidateSha"`
	TreeSHA       string `json:"treeSha"`
}

// ReviewerCheckout is a policy-free Git adapter for engine-owned disposable
// detached worktrees. It accepts no branch, push, CI, PR, integration, or Task
// lifecycle operation.
type ReviewerCheckout struct{}

var _ reviewport.CheckoutPort = (*ReviewerCheckout)(nil)

func reviewGit(ctx context.Context, cwd string, arguments ...string) (string, error) {
	args := append([]string{"-c", "core.hooksPath=/dev/null", "-c", "core.fsmonitor=false"}, arguments...)
	command := exec.CommandContext(ctx, "git", args...)
	command.Dir = cwd
	command.Env = append(os.Environ(), "GIT_CONFIG_NOSYSTEM=1", "GIT_TERMINAL_PROMPT=0")
	output, err := command.CombinedOutput()
	if err != nil {
		return "", errors.New("review Git fact unavailable")
	}
	return strings.TrimSpace(string(output)), nil
}

func canonicalReviewPaths(request reviewport.CheckoutRequest) (string, string, string, string, error) {
	for _, value := range []string{request.SourcePath, request.PrimaryPath, request.ReviewerRoot, request.CheckoutPath} {
		if !filepath.IsAbs(value) {
			return "", "", "", "", errors.New("review checkout path is not absolute")
		}
	}
	source, err := filepath.EvalSymlinks(request.SourcePath)
	if err != nil {
		return "", "", "", "", errors.New("review source identity unavailable")
	}
	primary, err := filepath.EvalSymlinks(request.PrimaryPath)
	if err != nil {
		return "", "", "", "", errors.New("review primary identity unavailable")
	}
	root, err := filepath.EvalSymlinks(request.ReviewerRoot)
	if err != nil {
		return "", "", "", "", errors.New("review owner root identity unavailable")
	}
	checkout := filepath.Clean(request.CheckoutPath)
	if root != filepath.Clean(request.ReviewerRoot) || filepath.Dir(checkout) != root {
		return "", "", "", "", errors.New("review checkout is not a direct child of its owner root")
	}
	relative, err := filepath.Rel(root, checkout)
	if err != nil || relative == "." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) || filepath.IsAbs(relative) {
		return "", "", "", "", errors.New("review checkout escapes its owner root")
	}
	if checkout == source || checkout == primary || root == source || root == primary {
		return "", "", "", "", errors.New("review checkout aliases a primary checkout")
	}
	return source, primary, root, checkout, nil
}

func ownerPath(checkout string) string { return checkout + ".director-review-owner.json" }

func expectedOwner(request reviewport.CheckoutRequest) reviewerOwner {
	return reviewerOwner{
		SchemaVersion: reviewerOwnerSchema, ReviewKey: request.ReviewKey, BindingSHA256: request.BindingSHA256,
		ReviewerUUID: request.OwnerUUID, CandidateSHA: request.CandidateSHA, TreeSHA: request.TreeSHA,
	}
}

func readOwner(path string) (reviewerOwner, bool) {
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm() != 0o600 {
		return reviewerOwner{}, false
	}
	content, err := os.ReadFile(path)
	if err != nil || len(content) > 4096 {
		return reviewerOwner{}, false
	}
	var owner reviewerOwner
	if err := json.Unmarshal(content, &owner); err != nil {
		return reviewerOwner{}, false
	}
	return owner, true
}

func ownerEqual(left, right reviewerOwner) bool { return left == right }

func (adapter *ReviewerCheckout) ObserveCheckout(ctx context.Context, request reviewport.CheckoutRequest) (reviewport.CheckoutObservation, error) {
	if filepath.IsAbs(request.SourcePath) && filepath.IsAbs(request.PrimaryPath) && filepath.IsAbs(request.ReviewerRoot) &&
		filepath.IsAbs(request.CheckoutPath) && filepath.Clean(request.ReviewerRoot) == request.ReviewerRoot &&
		filepath.Clean(request.CheckoutPath) == request.CheckoutPath && filepath.Dir(request.CheckoutPath) == request.ReviewerRoot {
		if _, rootErr := os.Lstat(request.ReviewerRoot); os.IsNotExist(rootErr) {
			source, sourceErr := filepath.EvalSymlinks(request.SourcePath)
			primary, primaryErr := filepath.EvalSymlinks(request.PrimaryPath)
			if sourceErr != nil || primaryErr != nil {
				return reviewport.CheckoutObservation{Status: reviewport.CheckoutAmbiguous}, nil
			}
			primaryHead, headErr := reviewGit(ctx, primary, "rev-parse", "HEAD")
			primaryStatus, statusErr := reviewGit(ctx, primary, "status", "--porcelain=v2", "--untracked-files=all")
			_, checkoutErr := os.Lstat(request.CheckoutPath)
			_, ownerErr := os.Lstat(ownerPath(request.CheckoutPath))
			if source != request.CheckoutPath && primary != request.CheckoutPath && headErr == nil && statusErr == nil &&
				primaryHead == request.PrimaryHeadSHA && primaryStatus == "" &&
				os.IsNotExist(checkoutErr) && os.IsNotExist(ownerErr) {
				return reviewport.CheckoutObservation{Status: reviewport.CheckoutAbsent}, nil
			}
			return reviewport.CheckoutObservation{Status: reviewport.CheckoutAmbiguous}, nil
		}
	}
	source, primary, _, checkout, err := canonicalReviewPaths(request)
	if err != nil {
		return reviewport.CheckoutObservation{Status: reviewport.CheckoutAmbiguous}, err
	}
	primaryHead, err := reviewGit(ctx, primary, "rev-parse", "HEAD")
	if err != nil || primaryHead != request.PrimaryHeadSHA {
		return reviewport.CheckoutObservation{Status: reviewport.CheckoutAmbiguous}, nil
	}
	primaryStatus, err := reviewGit(ctx, primary, "status", "--porcelain=v2", "--untracked-files=all")
	if err != nil || primaryStatus != "" {
		return reviewport.CheckoutObservation{Status: reviewport.CheckoutAmbiguous}, nil
	}
	_, statErr := os.Lstat(checkout)
	owner, ownerPresent := readOwner(ownerPath(checkout))
	if os.IsNotExist(statErr) {
		if ownerPresent {
			return reviewport.CheckoutObservation{Status: reviewport.CheckoutAmbiguous}, nil
		}
		return reviewport.CheckoutObservation{Status: reviewport.CheckoutAbsent}, nil
	}
	if statErr != nil || !ownerPresent || !ownerEqual(owner, expectedOwner(request)) {
		return reviewport.CheckoutObservation{Status: reviewport.CheckoutDifferent}, nil
	}
	realCheckout, err := filepath.EvalSymlinks(checkout)
	if err != nil || realCheckout != checkout {
		return reviewport.CheckoutObservation{Status: reviewport.CheckoutDifferent}, nil
	}
	head, err := reviewGit(ctx, checkout, "rev-parse", "HEAD")
	if err != nil {
		return reviewport.CheckoutObservation{Status: reviewport.CheckoutAmbiguous}, nil
	}
	branch, err := reviewGit(ctx, checkout, "branch", "--show-current")
	if err != nil {
		return reviewport.CheckoutObservation{Status: reviewport.CheckoutAmbiguous}, nil
	}
	tree, err := reviewGit(ctx, checkout, "show", "-s", "--format=%T", "HEAD")
	if err != nil {
		return reviewport.CheckoutObservation{Status: reviewport.CheckoutAmbiguous}, nil
	}
	status, err := reviewGit(ctx, checkout, "status", "--porcelain=v2", "--untracked-files=all")
	if err != nil {
		return reviewport.CheckoutObservation{Status: reviewport.CheckoutAmbiguous}, nil
	}
	commonSource, err := reviewGit(ctx, source, "rev-parse", "--path-format=absolute", "--git-common-dir")
	if err != nil {
		return reviewport.CheckoutObservation{Status: reviewport.CheckoutAmbiguous}, nil
	}
	commonPrimary, err := reviewGit(ctx, primary, "rev-parse", "--path-format=absolute", "--git-common-dir")
	if err != nil || commonPrimary != commonSource {
		return reviewport.CheckoutObservation{Status: reviewport.CheckoutDifferent}, nil
	}
	commonCheckout, err := reviewGit(ctx, checkout, "rev-parse", "--path-format=absolute", "--git-common-dir")
	if err != nil || commonSource != commonCheckout {
		return reviewport.CheckoutObservation{Status: reviewport.CheckoutDifferent}, nil
	}
	evidence := domainreview.SealCheckoutEvidence(domainreview.CheckoutEvidence{
		CheckoutID: request.ReviewKey, OwnerUUID: request.OwnerUUID, CandidateSHA: head, TreeSHA: tree,
		Detached: branch == "", Clean: status == "", PrimaryDistinct: checkout != primary && checkout != source,
		SourceUnchanged: primaryHead == request.PrimaryHeadSHA && primaryStatus == "",
	})
	if head != request.CandidateSHA || tree != request.TreeSHA || !evidence.Detached || !evidence.Clean || !evidence.PrimaryDistinct {
		return reviewport.CheckoutObservation{Status: reviewport.CheckoutDifferent, Evidence: evidence}, nil
	}
	return reviewport.CheckoutObservation{Status: reviewport.CheckoutExact, Evidence: evidence}, nil
}

func (adapter *ReviewerCheckout) CreateCheckout(ctx context.Context, request reviewport.CheckoutRequest) error {
	if !filepath.IsAbs(request.ReviewerRoot) {
		return errors.New("review checkout owner root is not absolute")
	}
	if err := os.MkdirAll(request.ReviewerRoot, 0o700); err != nil {
		return errors.New("review checkout owner root unavailable")
	}
	rootInfo, err := os.Lstat(request.ReviewerRoot)
	if err != nil || !rootInfo.IsDir() || rootInfo.Mode()&os.ModeSymlink != 0 || rootInfo.Mode().Perm() != 0o700 {
		return errors.New("review checkout owner root is not private")
	}
	source, _, _, checkout, err := canonicalReviewPaths(request)
	if err != nil {
		return err
	}
	observation, err := adapter.ObserveCheckout(ctx, request)
	if err != nil || observation.Status != reviewport.CheckoutAbsent {
		return errors.New("review checkout create precondition failed")
	}
	if _, err := reviewGit(ctx, source, "worktree", "add", "--detach", checkout, request.CandidateSHA); err != nil {
		return err
	}
	encoded, _ := json.Marshal(expectedOwner(request))
	encoded = append(encoded, '\n')
	ownerFile, err := os.OpenFile(ownerPath(checkout), os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return errors.New("review checkout ownership persistence failed")
	}
	if _, err = ownerFile.Write(encoded); err == nil {
		err = ownerFile.Sync()
	}
	closeErr := ownerFile.Close()
	if err != nil || closeErr != nil {
		return errors.New("review checkout ownership persistence failed")
	}
	observation, err = adapter.ObserveCheckout(ctx, request)
	if err != nil || observation.Status != reviewport.CheckoutExact {
		return errors.New("review checkout postcondition failed")
	}
	return nil
}

func (adapter *ReviewerCheckout) RemoveCheckout(ctx context.Context, request reviewport.CheckoutRequest, evidence domainreview.CheckoutEvidence) error {
	source, _, _, checkout, err := canonicalReviewPaths(request)
	if err != nil || evidence.OwnerUUID != request.OwnerUUID || evidence.CandidateSHA != request.CandidateSHA ||
		evidence.TreeSHA != request.TreeSHA || evidence.FactSHA256 != domainreview.CheckoutEvidenceSHA256(evidence) ||
		!evidence.Detached || !evidence.Clean || !evidence.PrimaryDistinct || !evidence.SourceUnchanged {
		return errors.New("review checkout cleanup evidence is invalid")
	}
	observation, err := adapter.ObserveCheckout(ctx, request)
	if err != nil || observation.Status != reviewport.CheckoutExact || observation.Evidence.FactSHA256 != evidence.FactSHA256 {
		return errors.New("review checkout cleanup precondition failed")
	}
	if _, err := reviewGit(ctx, source, "worktree", "remove", checkout); err != nil {
		return err
	}
	if err := os.Remove(ownerPath(checkout)); err != nil && !os.IsNotExist(err) {
		return errors.New("review checkout owner marker cleanup failed")
	}
	return nil
}
