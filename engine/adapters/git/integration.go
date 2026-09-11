// SPDX-License-Identifier: Apache-2.0

package git

import (
	"bytes"
	"context"
	"slices"
	"strconv"
	"strings"
	"unicode/utf8"

	candidatedomain "github.com/mcuadros/director-engine/domain/candidate"
	"github.com/mcuadros/director-engine/domain/execution"
	domainintegration "github.com/mcuadros/director-engine/domain/integration"
	repositorydomain "github.com/mcuadros/director-engine/domain/repository"
	gitport "github.com/mcuadros/director-engine/ports/git"
)

var _ gitport.IntegrationPort = (*Adapter)(nil)

func validIntegrationRequest(request gitport.IntegrationRequest) bool {
	binding := request.Binding
	claim := request.CandidateClaim
	manifest := request.CandidateManifest
	remote, err := repositorydomain.CanonicalRemote(request.Repository.CanonicalRemote)
	return err == nil && domainintegration.ValidBinding(binding) && candidatedomain.ValidClaim(claim) &&
		candidatedomain.ValidManifest(manifest) && execution.RepositoryBindingSHA256(request.Repository) == request.RepositoryBindingSHA256 &&
		request.RepositoryBindingSHA256 == binding.RepositoryBindingSHA256 && request.TaskStoreNowMillis >= 0 &&
		(request.MergeCommitSHA == "" || gitOID(request.MergeCommitSHA) && len(request.MergeCommitSHA) == len(binding.CandidateSHA)) &&
		binding.CandidateSHA == claim.CandidateSHA && binding.BaseSHA == claim.BaseSHA && binding.TreeSHA == manifest.TreeSHA &&
		binding.ManifestSHA256 == manifest.BindingSHA256 && binding.ConfigurationSHA256 == claim.ConfigurationSHA256 &&
		binding.RepositoryID == request.Repository.RepositoryID && request.Repository.BaseSHA == binding.BaseSHA &&
		binding.Branch == request.Repository.Branch && binding.Branch == claim.Branch && binding.BaseRef == claim.BaseRef &&
		remote.Canonical == request.Repository.CanonicalRemote && remote.ID == request.Repository.RepositoryID && remote.Key == request.Repository.RepositoryKey
}

func integrationGitObservation(request gitport.IntegrationRequest, status domainintegration.GitStatus, code domainintegration.Code) domainintegration.GitObservation {
	return domainintegration.SealGitObservation(domainintegration.GitObservation{ID: "git-integration-" + domainintegration.DigestText(request.Binding.SHA256+"\x1f"+request.MergeCommitSHA+"\x1f"+string(status)+"\x1f"+string(code)+"\x1f"+strconv.FormatInt(request.TaskStoreNowMillis, 10)),
		Status: status, Code: code, RepositoryID: request.Binding.RepositoryID, CanonicalRemote: request.Binding.CanonicalRemote,
		HeadRef: "refs/heads/" + request.Binding.Branch, BaseRef: request.Binding.BaseRef,
		ObservedAtMillis: request.TaskStoreNowMillis, MaximumAgeMillis: domainintegration.MaximumObservationAgeMS})
}

func integrationRemote(adapter *Adapter, request gitport.IntegrationRequest) string {
	if adapter.deliveryRemoteOverride != "" {
		return adapter.deliveryRemoteOverride
	}
	return request.Binding.CanonicalRemote
}

func remoteIdentityCurrent(ctx context.Context, request gitport.IntegrationRequest) bool {
	result := runDeliveryGit(ctx, request.Repository.WorktreePath, "remote", "get-url", "--push", "--all", "origin")
	if !result.ok || result.code != 0 || !utf8.Valid(result.stdout) {
		return false
	}
	lines := strings.Fields(string(result.stdout))
	if len(lines) != 1 {
		return false
	}
	identity, err := repositorydomain.CanonicalRemote(lines[0])
	return err == nil && identity.Canonical == request.Binding.CanonicalRemote && identity.ID == request.Binding.RepositoryID
}

func parseExactRefs(output []byte, wanted ...string) (map[string]string, bool) {
	if !utf8.Valid(output) {
		return nil, false
	}
	values := make(map[string]string, len(wanted))
	want := make(map[string]struct{}, len(wanted))
	for _, ref := range wanted {
		want[ref] = struct{}{}
	}
	for _, row := range bytes.Split(bytes.TrimSpace(output), []byte{'\n'}) {
		if len(row) == 0 {
			continue
		}
		fields := bytes.Split(row, []byte{'\t'})
		if len(fields) != 2 || !gitOID(string(fields[0])) {
			return nil, false
		}
		ref := string(fields[1])
		if _, expected := want[ref]; !expected {
			return nil, false
		}
		if _, duplicate := values[ref]; duplicate {
			return nil, false
		}
		values[ref] = string(fields[0])
	}
	return values, len(values) == len(wanted)
}

func parseRemoteHEAD(output []byte) (string, string, bool) {
	if !utf8.Valid(output) {
		return "", "", false
	}
	var ref, oid string
	for _, row := range bytes.Split(bytes.TrimSpace(output), []byte{'\n'}) {
		fields := bytes.Split(row, []byte{'\t'})
		if len(fields) != 2 || string(fields[1]) != "HEAD" {
			return "", "", false
		}
		left := string(fields[0])
		if strings.HasPrefix(left, "ref: ") {
			if ref != "" {
				return "", "", false
			}
			ref = strings.TrimPrefix(left, "ref: ")
		} else if gitOID(left) {
			if oid != "" {
				return "", "", false
			}
			oid = left
		} else {
			return "", "", false
		}
	}
	return ref, oid, ref != "" && oid != ""
}

// ObserveIntegration independently repeats exact local Candidate validation,
// live Task/base/default-ref reads, and post-merge graph verification.
func (adapter *Adapter) ObserveIntegration(ctx context.Context, request gitport.IntegrationRequest) (domainintegration.GitObservation, error) {
	if !validIntegrationRequest(request) {
		return integrationGitObservation(request, domainintegration.GitInvalid, domainintegration.CodeRepositoryMismatch), nil
	}
	if adapter.deliveryRemoteOverride == "" && !remoteIdentityCurrent(ctx, request) {
		return integrationGitObservation(request, domainintegration.GitInvalid, domainintegration.CodeRepositoryMismatch), nil
	}
	candidateObservation, err := adapter.ObserveCandidate(ctx, gitport.CandidateRequest{Claim: request.CandidateClaim,
		Repository: request.Repository, RepositoryBindingSHA256: request.RepositoryBindingSHA256,
		TaskStoreNowMillis: request.TaskStoreNowMillis})
	if err != nil {
		return integrationGitObservation(request, domainintegration.GitUnavailable, domainintegration.CodeUnavailable), nil
	}
	decision := candidatedomain.Evaluate(request.CandidateClaim, candidateObservation, request.RepositoryBindingSHA256, request.TaskStoreNowMillis)
	if decision.Kind != candidatedomain.DecisionAdmit || decision.Manifest == nil ||
		!candidatedomain.SameImmutableContent(request.CandidateManifest, *decision.Manifest) {
		return integrationGitObservation(request, domainintegration.GitInvalid, domainintegration.CodeCandidateChanged), nil
	}
	remote := integrationRemote(adapter, request)
	refsResult := runDeliveryGit(ctx, request.Repository.WorktreePath, "ls-remote", "--exit-code", "--refs", remote,
		"refs/heads/"+request.Binding.Branch, request.Binding.BaseRef)
	if !refsResult.started || !refsResult.ok || refsResult.code != 0 {
		return integrationGitObservation(request, domainintegration.GitUnavailable, domainintegration.CodeUnavailable), nil
	}
	refs, ok := parseExactRefs(refsResult.stdout, "refs/heads/"+request.Binding.Branch, request.Binding.BaseRef)
	if !ok {
		return integrationGitObservation(request, domainintegration.GitInvalid, domainintegration.CodeResponseUnknown), nil
	}
	headResult := runDeliveryGit(ctx, request.Repository.WorktreePath, "ls-remote", "--symref", remote, "HEAD")
	if !headResult.started || !headResult.ok || headResult.code != 0 {
		return integrationGitObservation(request, domainintegration.GitUnavailable, domainintegration.CodeUnavailable), nil
	}
	defaultRef, defaultOID, ok := parseRemoteHEAD(headResult.stdout)
	if !ok {
		return integrationGitObservation(request, domainintegration.GitInvalid, domainintegration.CodeResponseUnknown), nil
	}
	status := domainintegration.GitReady
	observation := integrationGitObservation(request, status, domainintegration.CodeOK)
	observation.HeadSHA, observation.BaseSHA = refs[observation.HeadRef], refs[observation.BaseRef]
	observation.DefaultRef, observation.DefaultSHA = defaultRef, defaultOID
	if observation.HeadSHA != request.Binding.CandidateSHA {
		return integrationGitObservation(request, domainintegration.GitInvalid, domainintegration.CodeHeadChanged), nil
	}
	expectedBase := request.Binding.BaseSHA
	if request.MergeCommitSHA != "" {
		expectedBase, status = request.MergeCommitSHA, domainintegration.GitIntegrated
	}
	if observation.BaseSHA != expectedBase || observation.DefaultRef != request.Binding.BaseRef || observation.DefaultSHA != expectedBase {
		return integrationGitObservation(request, domainintegration.GitInvalid, domainintegration.CodeBaseChanged), nil
	}
	if status == domainintegration.GitIntegrated {
		fetch := runDeliveryGit(ctx, request.Repository.WorktreePath, "fetch", "--no-tags", "--no-write-fetch-head", remote, request.MergeCommitSHA)
		if !fetch.started || !fetch.ok || fetch.code != 0 {
			return integrationGitObservation(request, domainintegration.GitUnavailable, domainintegration.CodeUnavailable), nil
		}
		show := runDeliveryGit(ctx, request.Repository.WorktreePath, "show", "--no-patch", "--format=%P%n%T", request.MergeCommitSHA)
		if !show.started || !show.ok || show.code != 0 || !utf8.Valid(show.stdout) {
			return integrationGitObservation(request, domainintegration.GitInvalid, domainintegration.CodeGraphMismatch), nil
		}
		lines := strings.Split(strings.TrimSpace(string(show.stdout)), "\n")
		if len(lines) != 2 {
			return integrationGitObservation(request, domainintegration.GitInvalid, domainintegration.CodeGraphMismatch), nil
		}
		observation.MergeCommitSHA, observation.ParentSHAs, observation.TreeSHA = request.MergeCommitSHA, strings.Fields(lines[0]), lines[1]
		if !slices.Equal(observation.ParentSHAs, []string{request.Binding.BaseSHA, request.Binding.CandidateSHA}) ||
			observation.TreeSHA != request.Binding.TreeSHA {
			return integrationGitObservation(request, domainintegration.GitInvalid, domainintegration.CodeGraphMismatch), nil
		}
	}
	observation.Status = status
	return domainintegration.SealGitObservation(observation), nil
}
