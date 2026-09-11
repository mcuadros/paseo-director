// SPDX-License-Identifier: Apache-2.0

package git

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"unicode/utf8"

	candidatedomain "github.com/mcuadros/director-engine/domain/candidate"
	directdomain "github.com/mcuadros/director-engine/domain/directdelivery"
	"github.com/mcuadros/director-engine/domain/execution"
	repositorydomain "github.com/mcuadros/director-engine/domain/repository"
	directport "github.com/mcuadros/director-engine/ports/directdelivery"
	gitport "github.com/mcuadros/director-engine/ports/git"
)

var _ directport.Port = (*Adapter)(nil)

type deliveryCommandResult struct {
	stdout  []byte
	code    int
	started bool
	ok      bool
}

func deliveryEnvironment() []string {
	environment := []string{"LC_ALL=C", "LANG=C", "GIT_TERMINAL_PROMPT=0", "GIT_OPTIONAL_LOCKS=0"}
	for _, name := range []string{"PATH", "HOME", "XDG_CONFIG_HOME", "SSH_AUTH_SOCK", "TMPDIR"} {
		if value := os.Getenv(name); value != "" && (name != "TMPDIR" || filepath.IsAbs(value)) {
			environment = append(environment, name+"="+value)
		}
	}
	return environment
}

func runDeliveryGit(ctx context.Context, directory string, arguments ...string) deliveryCommandResult {
	global := []string{"--no-optional-locks", "--literal-pathspecs", "-c", "core.fsmonitor=false",
		"-c", "core.hooksPath=/dev/null", "-c", "credential.interactive=never", "-C", directory}
	command := exec.CommandContext(ctx, "git", append(global, arguments...)...)
	command.Env = deliveryEnvironment()
	var stdout, stderr limitedBuffer
	command.Stdout, command.Stderr = &stdout, &stderr
	if err := command.Start(); err != nil {
		return deliveryCommandResult{code: -1}
	}
	err := command.Wait()
	if stdout.overflow || stderr.overflow {
		return deliveryCommandResult{code: -1, started: true}
	}
	if err == nil {
		return deliveryCommandResult{stdout: stdout.Bytes(), code: 0, started: true, ok: true}
	}
	var exit *exec.ExitError
	if errors.As(err, &exit) {
		return deliveryCommandResult{stdout: stdout.Bytes(), code: exit.ExitCode(), started: true, ok: true}
	}
	return deliveryCommandResult{code: -1, started: true}
}

func validDirectTarget(target directport.Target) bool {
	binding := target.Binding
	claim := target.CandidateClaim
	manifest := target.CandidateManifest
	remote, err := repositorydomain.CanonicalRemote(target.Repository.CanonicalRemote)
	return err == nil && directdomain.ValidBinding(binding) && candidatedomain.ValidClaim(claim) &&
		candidatedomain.ValidManifest(manifest) && execution.RepositoryBindingSHA256(target.Repository) == target.RepositoryBindingSHA256 &&
		target.RepositoryBindingSHA256 == binding.RepositoryBindingSHA256 && target.EffectID == directdomain.EffectID(binding) &&
		target.TaskStoreNowMillis >= 0 && binding.CandidateID != "" && binding.CandidateSHA == claim.CandidateSHA &&
		binding.BaseSHA == claim.BaseSHA && binding.TreeSHA == manifest.TreeSHA && binding.ManifestSHA256 == manifest.BindingSHA256 &&
		binding.ConfigurationSHA256 == claim.ConfigurationSHA256 && binding.TargetRef == claim.BaseRef &&
		binding.RepositoryID == target.Repository.RepositoryID && target.Repository.BaseSHA == binding.BaseSHA &&
		target.Repository.Branch == claim.Branch && remote.Canonical == target.Repository.CanonicalRemote &&
		remote.ID == target.Repository.RepositoryID && remote.Key == target.Repository.RepositoryKey
}

func directObservation(target directport.Target, status directdomain.ObservationStatus, code directdomain.ObservationCode, current string) directdomain.Observation {
	return directdomain.SealObservation(directdomain.Observation{EffectID: target.EffectID,
		BindingSHA256: target.Binding.SHA256, Attempt: target.Attempt, Status: status, Code: code,
		RepositoryID: target.Binding.RepositoryID, TargetRef: target.Binding.TargetRef, CurrentSHA: current,
		ObservedAtMillis: target.TaskStoreNowMillis, MaximumAgeMillis: directdomain.MaximumObservationAgeMS})
}

func invalidDirectObservation(target directport.Target, code directdomain.ObservationCode) directdomain.Observation {
	if !directdomain.ValidBinding(target.Binding) {
		return directdomain.Observation{}
	}
	return directObservation(target, directdomain.ObservationAmbiguous, code, "")
}

func candidateDirectCode(code candidatedomain.Code) directdomain.ObservationCode {
	switch code {
	case candidatedomain.CodeRepositoryMismatch, candidatedomain.CodeRemoteMismatch, candidatedomain.CodePathAlias,
		candidatedomain.CodeWorktreeRegistration:
		return directdomain.CodeRepositoryMismatch
	case candidatedomain.CodeBranchMoved:
		return directdomain.CodeBranchChanged
	default:
		return directdomain.CodeCandidateChanged
	}
}

func (adapter *Adapter) directCandidateCurrent(ctx context.Context, target directport.Target) directdomain.ObservationCode {
	observation, err := adapter.ObserveCandidate(ctx, gitport.CandidateRequest{Claim: target.CandidateClaim,
		Repository: target.Repository, RepositoryBindingSHA256: target.RepositoryBindingSHA256,
		TaskStoreNowMillis: target.TaskStoreNowMillis})
	if err != nil {
		return directdomain.CodeRemoteUnavailable
	}
	decision := candidatedomain.Evaluate(target.CandidateClaim, observation, target.RepositoryBindingSHA256, target.TaskStoreNowMillis)
	if decision.Kind != candidatedomain.DecisionAdmit || decision.Manifest == nil {
		return candidateDirectCode(decision.Code)
	}
	if !candidatedomain.SameImmutableContent(target.CandidateManifest, *decision.Manifest) {
		return directdomain.CodeCandidateChanged
	}
	return directdomain.CodeOK
}

func (adapter *Adapter) transportRemote(target directport.Target) string {
	if adapter.deliveryRemoteOverride != "" {
		return adapter.deliveryRemoteOverride
	}
	return target.Repository.CanonicalRemote
}

func parseRemoteRef(result deliveryCommandResult, targetRef string, oidLength int) (string, directdomain.ObservationStatus, directdomain.ObservationCode) {
	if !result.started || !result.ok {
		return "", directdomain.ObservationUnavailable, directdomain.CodeRemoteUnavailable
	}
	if result.code == 2 && len(result.stdout) == 0 {
		return "", directdomain.ObservationAbsent, directdomain.CodeRemoteRefAbsent
	}
	if result.code != 0 || !utf8.Valid(result.stdout) {
		return "", directdomain.ObservationUnavailable, directdomain.CodeRemoteUnavailable
	}
	lines := bytes.Split(bytes.TrimSpace(result.stdout), []byte{'\n'})
	if len(lines) != 1 {
		return "", directdomain.ObservationAmbiguous, directdomain.CodeRemoteAmbiguous
	}
	fields := bytes.Split(lines[0], []byte{'\t'})
	if len(fields) != 2 || string(fields[1]) != targetRef || len(fields[0]) != oidLength ||
		strings.IndexFunc(string(fields[0]), func(character rune) bool { return !strings.ContainsRune("0123456789abcdef", character) }) >= 0 {
		return "", directdomain.ObservationAmbiguous, directdomain.CodeRemoteAmbiguous
	}
	return string(fields[0]), "", directdomain.CodeOK
}

// Observe returns one exact remote-target fact after independently repeating
// the complete local Candidate/repository observation. Transport and parsing
// failures are unavailable or ambiguous, never absence.
func (adapter *Adapter) Observe(ctx context.Context, target directport.Target) (directdomain.Observation, error) {
	if !validDirectTarget(target) {
		return invalidDirectObservation(target, directdomain.CodeUnsafeTarget), nil
	}
	if code := adapter.directCandidateCurrent(ctx, target); code != directdomain.CodeOK {
		if code == directdomain.CodeRemoteUnavailable {
			return directObservation(target, directdomain.ObservationUnavailable, code, ""), nil
		}
		return invalidDirectObservation(target, code), nil
	}
	result := runDeliveryGit(ctx, target.Repository.WorktreePath, "ls-remote", "--exit-code", "--refs",
		adapter.transportRemote(target), target.Binding.TargetRef)
	current, status, code := parseRemoteRef(result, target.Binding.TargetRef, len(target.Binding.CandidateSHA))
	if status == "" {
		switch current {
		case target.Binding.BaseSHA:
			status = directdomain.ObservationCurrentExpected
		case target.Binding.CandidateSHA:
			status = directdomain.ObservationDesired
		default:
			status, code = directdomain.ObservationDifferent, directdomain.CodeRemoteRefChanged
		}
	}
	return directObservation(target, status, code, current), nil
}

// Push performs exactly one explicit single-ref update with an exact expected
// old OID. It re-observes the complete precondition immediately before Git
// handoff and returns only a bounded handoff classification.
func (adapter *Adapter) Push(ctx context.Context, command directport.PushCommand) error {
	if command.Attempt == 0 || command.Attempt != command.Target.Attempt+1 || command.Attempt > directdomain.MaximumAttempts ||
		!validDirectTarget(command.Target) || !directdomain.ValidObservation(command.ExpectedObservation,
		command.Target.Binding, command.Target.EffectID, command.Target.TaskStoreNowMillis) ||
		command.ExpectedObservation.Status != directdomain.ObservationCurrentExpected ||
		command.ExpectedObservation.Attempt != command.Target.Attempt {
		return &directport.DispatchError{Code: directport.FailurePreconditionChanged}
	}
	if adapter.beforeDirectPush != nil {
		adapter.beforeDirectPush()
	}
	observed, err := adapter.Observe(ctx, command.Target)
	if err != nil || observed != command.ExpectedObservation {
		return &directport.DispatchError{Code: directport.FailurePreconditionChanged}
	}
	lease := "--force-with-lease=" + command.Target.Binding.TargetRef + ":" + command.Target.Binding.BaseSHA
	refspec := command.Target.Binding.CandidateSHA + ":" + command.Target.Binding.TargetRef
	result := runDeliveryGit(ctx, command.Target.Repository.WorktreePath, "push", "--porcelain", lease,
		adapter.transportRemote(command.Target), refspec)
	if !result.started {
		return &directport.DispatchError{Code: directport.FailureProcessUnavailable}
	}
	if !result.ok || result.code != 0 {
		return &directport.DispatchError{Code: directport.FailureResultUnknown, PossibleHandoff: true}
	}
	return nil
}
