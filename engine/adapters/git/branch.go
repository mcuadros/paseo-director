// SPDX-License-Identifier: Apache-2.0

package git

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/mcuadros/director-engine/domain/execution"
	publicationdomain "github.com/mcuadros/director-engine/domain/publication"
	repositorydomain "github.com/mcuadros/director-engine/domain/repository"
	gitport "github.com/mcuadros/director-engine/ports/git"
)

var _ gitport.BranchPort = (*Adapter)(nil)

func mutationEnvironment() []string {
	environment := []string{
		"LC_ALL=C", "LANG=C", "GIT_CONFIG_NOSYSTEM=1", "GIT_OPTIONAL_LOCKS=0",
		"GIT_TERMINAL_PROMPT=0",
	}
	for _, name := range []string{"PATH", "HOME", "XDG_CONFIG_HOME", "SSH_AUTH_SOCK", "GIT_ASKPASS", "GIT_SSH", "GIT_SSH_COMMAND"} {
		if value := os.Getenv(name); value != "" && !strings.ContainsRune(value, 0) {
			environment = append(environment, name+"="+value)
		}
	}
	if temporary := os.Getenv("TMPDIR"); temporary != "" && filepath.IsAbs(temporary) {
		environment = append(environment, "TMPDIR="+temporary)
	}
	return environment
}

func runGitPublicationRead(ctx context.Context, directory string, arguments ...string) commandResult {
	global := []string{
		"--no-optional-locks", "--literal-pathspecs", "-c", "core.fsmonitor=false",
		"-c", "core.hooksPath=/dev/null", "-c", "diff.external=", "-C", directory,
	}
	command := exec.CommandContext(ctx, "git", append(global, arguments...)...)
	command.Env = mutationEnvironment()
	var stdout, stderr limitedBuffer
	command.Stdout = &stdout
	command.Stderr = &stderr
	err := command.Run()
	if stdout.overflow || stderr.overflow {
		return commandResult{code: -1}
	}
	if err == nil {
		return commandResult{stdout: stdout.Bytes(), code: 0, ok: true}
	}
	var exit *exec.ExitError
	if errors.As(err, &exit) {
		return commandResult{stdout: stdout.Bytes(), code: exit.ExitCode(), ok: true}
	}
	return commandResult{code: -1}
}

func safePublicationBranch(value string) bool {
	return value != "" && !strings.HasPrefix(value, "-") &&
		!strings.Contains(value, "..") && !strings.Contains(value, "@{") && !strings.Contains(value, "//") &&
		!strings.HasSuffix(value, "/") && !strings.HasSuffix(value, ".") && !strings.HasSuffix(value, ".lock") &&
		strings.IndexFunc(value, func(character rune) bool {
			return character <= ' ' || character == 0x7f || strings.ContainsRune(`~^:?*[\`, character)
		}) < 0
}

func publicationRequestValid(request gitport.RemoteRefRequest) bool {
	return execution.RepositoryBindingSHA256(request.Repository) == request.RepositorySHA256 &&
		request.RepositorySHA256 != "" && request.RemoteName == "origin" &&
		request.CanonicalRemote == request.Repository.CanonicalRemote && safePublicationBranch(request.Branch) &&
		request.TaskStoreNowMillis >= 0
}

func remoteExact(ctx context.Context, request gitport.RemoteRefRequest) bool {
	result := runGitPublicationRead(ctx, request.Repository.SourcePath, "remote", "get-url", "--push", "--all", request.RemoteName)
	if !result.ok || result.code != 0 || !utf8.Valid(result.stdout) {
		return false
	}
	lines := strings.Fields(string(result.stdout))
	if len(lines) != 1 {
		return false
	}
	identity, err := repositorydomain.CanonicalRemote(lines[0])
	return err == nil && identity.Canonical == request.CanonicalRemote && identity.ID == request.Repository.RepositoryID &&
		identity.Key == request.Repository.RepositoryKey
}

// ObserveRemoteRef distinguishes exact status-2 absence from every transport,
// authentication, parsing, and identity failure.
func (adapter *Adapter) ObserveRemoteRef(ctx context.Context, request gitport.RemoteRefRequest) (publicationdomain.RefObservation, error) {
	ref := "refs/heads/" + request.Branch
	observation := publicationdomain.RefObservation{
		ID:   "git-ref-" + publicationdomain.DigestText(request.RepositorySHA256+"\x1f"+ref+"\x1f"+strconv.FormatInt(request.TaskStoreNowMillis, 10)),
		Code: publicationdomain.CodeUnavailable, Ref: ref, RemoteCanonical: request.CanonicalRemote,
		ObservedAtMillis: request.TaskStoreNowMillis, MaximumAgeMillis: publicationdomain.MaximumObservationAgeMS,
	}
	if !publicationRequestValid(request) || !remoteExact(ctx, request) {
		observation.Code = publicationdomain.CodeRepositoryMismatch
		return publicationdomain.SealRefObservation(observation), nil
	}
	result := runGitPublicationRead(ctx, request.Repository.SourcePath, "ls-remote", "--exit-code", request.RemoteName, ref)
	if !result.ok {
		return publicationdomain.SealRefObservation(observation), nil
	}
	if result.code == 2 && len(bytes.TrimSpace(result.stdout)) == 0 {
		observation.Code = publicationdomain.CodeOK
		return publicationdomain.SealRefObservation(observation), nil
	}
	if result.code != 0 || !utf8.Valid(result.stdout) {
		return publicationdomain.SealRefObservation(observation), nil
	}
	fields := strings.Fields(string(result.stdout))
	if len(fields) != 2 || fields[1] != ref || (len(fields[0]) != 40 && len(fields[0]) != 64) {
		observation.Code = publicationdomain.CodeResponseUnknown
		return publicationdomain.SealRefObservation(observation), nil
	}
	for _, character := range fields[0] {
		if !strings.ContainsRune("0123456789abcdef", character) {
			observation.Code = publicationdomain.CodeResponseUnknown
			return publicationdomain.SealRefObservation(observation), nil
		}
	}
	observation.Code = publicationdomain.CodeOK
	observation.Exists = true
	observation.OID = fields[0]
	return publicationdomain.SealRefObservation(observation), nil
}

func runGitHandoff(ctx context.Context, directory string, arguments ...string) (bool, int) {
	global := []string{
		"--no-optional-locks", "--literal-pathspecs", "-c", "core.fsmonitor=false",
		"-c", "core.hooksPath=/dev/null", "-c", "diff.external=", "-C", directory,
	}
	command := exec.CommandContext(ctx, "git", append(global, arguments...)...)
	command.Env = mutationEnvironment()
	var stdout, stderr limitedBuffer
	command.Stdout = &stdout
	command.Stderr = &stderr
	if err := command.Start(); err != nil {
		return false, -1
	}
	err := command.Wait()
	if stdout.overflow || stderr.overflow {
		return true, -1
	}
	if err == nil {
		return true, 0
	}
	var exit *exec.ExitError
	if errors.As(err, &exit) {
		return true, exit.ExitCode()
	}
	return true, -1
}

// PushExact performs only the explicit expected-value force-with-lease form.
// The engine remains responsible for authorization and post-handoff evidence.
func (adapter *Adapter) PushExact(ctx context.Context, request gitport.PushRequest) (gitport.PushResult, error) {
	if !publicationRequestValid(request.RemoteRefRequest) || !strings.HasPrefix(request.Branch, "task/") || request.Branch == "task/" || !gitOID(request.CandidateSHA) ||
		request.Branch != request.Repository.Branch ||
		(request.ExpectedRemoteOID != "" && !gitOID(request.ExpectedRemoteOID)) ||
		request.Branch == "main" || request.Branch == "master" ||
		slices.Contains(request.ProtectedBranches, request.Branch) || !remoteExact(ctx, request.RemoteRefRequest) {
		return gitport.PushResult{Code: publicationdomain.CodeProtectedRef}, nil
	}
	if object := runGitPublicationRead(ctx, request.Repository.SourcePath, "cat-file", "-e", request.CandidateSHA+"^{commit}"); !object.ok || object.code != 0 {
		return gitport.PushResult{Code: publicationdomain.CodeResponseUnknown}, nil
	}
	ref := "refs/heads/" + request.Branch
	lease := "--force-with-lease=" + ref + ":" + request.ExpectedRemoteOID
	handoff, code := runGitHandoff(ctx, request.Repository.SourcePath, "push", "--porcelain", lease,
		request.RemoteName, request.CandidateSHA+":"+ref)
	if !handoff {
		return gitport.PushResult{Handoff: false, Code: publicationdomain.CodeUnavailable}, nil
	}
	if code != 0 {
		return gitport.PushResult{Handoff: true, Code: publicationdomain.CodeResponseUnknown}, nil
	}
	return gitport.PushResult{Handoff: true, Code: publicationdomain.CodeOK}, nil
}

func gitOID(value string) bool {
	if len(value) != 40 && len(value) != 64 {
		return false
	}
	return strings.IndexFunc(value, func(character rune) bool {
		return !strings.ContainsRune("0123456789abcdef", character)
	}) < 0
}
