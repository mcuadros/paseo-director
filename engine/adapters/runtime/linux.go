// SPDX-License-Identifier: Apache-2.0

// Package runtime implements the Linux production preparation effects which
// precede a Paseo worker. Delivery and terminal cleanup remain in their
// dedicated Git/GitHub application services and adapters.
package runtime

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	goruntime "runtime"
	"strconv"
	"strings"
	"syscall"
	"time"

	domainexecution "github.com/mcuadros/director-engine/domain/execution"
	repositorydomain "github.com/mcuadros/director-engine/domain/repository"
	runtimeport "github.com/mcuadros/director-engine/ports/runtime"
)

const maximumCommandBytes = 4 << 20

type Adapter struct {
	root string
}

var _ runtimeport.Port = (*Adapter)(nil)
var _ runtimeport.PrimaryRecoveryPort = (*Adapter)(nil)

func New(root string) (*Adapter, error) {
	if goruntime.GOOS != "linux" || !filepath.IsAbs(root) || filepath.Clean(root) != root {
		return nil, errors.New("Linux runtime root is invalid")
	}
	if err := os.MkdirAll(root, 0o700); err != nil {
		return nil, errors.New("Linux runtime root is unavailable")
	}
	info, err := os.Lstat(root)
	identity, ok := infoSys(info)
	if err != nil || !info.IsDir() || info.Mode().Perm()&0o077 != 0 || !ok || identity.Uid != uint32(os.Geteuid()) {
		return nil, errors.New("Linux runtime root is unsafe")
	}
	return &Adapter{root: root}, nil
}

func infoSys(info os.FileInfo) (*syscall.Stat_t, bool) {
	if info == nil {
		return nil, false
	}
	value, ok := info.Sys().(*syscall.Stat_t)
	return value, ok
}

func digest(values ...string) string {
	sum := sha256.Sum256([]byte(strings.Join(values, "\x1f")))
	return hex.EncodeToString(sum[:])
}

func externalID(prefix string, request runtimeport.Request) string {
	return prefix + "-" + digest(request.Scope.RunID, request.Effect.ID, request.BindingHash)[:32]
}

type commandResult struct {
	output []byte
	code   int
	start  bool
}

func safeEnvironment() []string {
	result := []string{"LC_ALL=C", "LANG=C", "GIT_CONFIG_NOSYSTEM=1", "GIT_OPTIONAL_LOCKS=0", "GIT_TERMINAL_PROMPT=0"}
	for _, key := range []string{"PATH", "HOME", "XDG_CONFIG_HOME", "SSH_AUTH_SOCK", "GIT_ASKPASS", "GIT_SSH", "GIT_SSH_COMMAND"} {
		if value := os.Getenv(key); value != "" && !strings.ContainsRune(value, 0) {
			result = append(result, key+"="+value)
		}
	}
	return result
}

func run(ctx context.Context, name string, cwd string, arguments ...string) commandResult {
	command := exec.CommandContext(ctx, name, arguments...)
	command.Dir, command.Env = cwd, safeEnvironment()
	var output bytes.Buffer
	command.Stdout, command.Stderr = &output, &output
	if err := command.Start(); err != nil {
		return commandResult{code: -1}
	}
	err := command.Wait()
	if output.Len() > maximumCommandBytes {
		return commandResult{code: -1, start: true}
	}
	if err == nil {
		return commandResult{output: output.Bytes(), code: 0, start: true}
	}
	var exit *exec.ExitError
	if errors.As(err, &exit) {
		return commandResult{output: output.Bytes(), code: exit.ExitCode(), start: true}
	}
	return commandResult{code: -1, start: true}
}

func git(ctx context.Context, cwd string, arguments ...string) commandResult {
	prefix := []string{"--no-optional-locks", "--literal-pathspecs", "-c", "core.fsmonitor=false", "-c", "core.hooksPath=/dev/null", "-C", cwd}
	return run(ctx, "git", cwd, append(prefix, arguments...)...)
}

func trimmed(result commandResult) string {
	if result.code != 0 {
		return ""
	}
	return strings.TrimSpace(string(result.output))
}

func exactRepository(ctx context.Context, request runtimeport.Request) bool {
	binding := request.Repository
	if domainexecution.RepositoryBindingSHA256(binding) != request.BindingHash || binding.SourcePath != request.SourcePath ||
		binding.WorktreePath != request.WorktreePath || binding.Branch != request.Branch || binding.BaseSHA != request.BaseSHA {
		return false
	}
	source, err := filepath.EvalSymlinks(request.SourcePath)
	if err != nil || source != request.SourcePath {
		return false
	}
	info, err := os.Stat(source)
	identity, ok := infoSys(info)
	if err != nil || !ok || uint64(identity.Dev) != binding.SourceDevice || identity.Ino != binding.SourceInode {
		return false
	}
	common := trimmed(git(ctx, source, "rev-parse", "--path-format=absolute", "--git-common-dir"))
	resolvedCommon, err := filepath.EvalSymlinks(common)
	commonInfo, infoErr := os.Stat(resolvedCommon)
	commonIdentity, commonOK := infoSys(commonInfo)
	if err != nil || infoErr != nil || resolvedCommon != binding.GitCommonDirectory || !commonOK ||
		uint64(commonIdentity.Dev) != binding.GitCommonDevice || commonIdentity.Ino != binding.GitCommonInode {
		return false
	}
	remoteValue := trimmed(git(ctx, source, "remote", "get-url", "--push", "--all", "origin"))
	remote, err := repositorydomain.CanonicalRemote(remoteValue)
	base := git(ctx, source, "cat-file", "-e", request.BaseSHA+"^{commit}")
	return err == nil && remote.ID == binding.RepositoryID && remote.Key == binding.RepositoryKey &&
		remote.Canonical == binding.CanonicalRemote && base.code == 0
}

func (adapter *Adapter) marker(request runtimeport.Request, kind string) string {
	return filepath.Join(adapter.root, kind+"-"+digest(request.Scope.RunID, request.Effect.ID)[:32]+".json")
}

type marker struct {
	SchemaVersion string `json:"schemaVersion"`
	RunID         string `json:"runId"`
	EffectID      string `json:"effectId"`
	BindingHash   string `json:"bindingHash"`
	ExternalID    string `json:"externalId"`
	CreatedAt     int64  `json:"createdAtMillis"`
}

func writeMarker(path string, value marker) error {
	encoded, _ := json.Marshal(value)
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	if _, err = file.Write(encoded); err == nil {
		err = file.Sync()
	}
	return errors.Join(err, file.Close())
}

func readMarker(path string, request runtimeport.Request) (marker, bool) {
	info, err := os.Lstat(path)
	identity, ok := infoSys(info)
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm() != 0o600 || !ok || identity.Uid != uint32(os.Geteuid()) || info.Size() > 4096 {
		return marker{}, false
	}
	content, err := os.ReadFile(path)
	var value marker
	if err != nil || json.Unmarshal(content, &value) != nil || value.SchemaVersion != "director.linux-runtime/v1" ||
		value.RunID != request.Scope.RunID || value.EffectID != request.Effect.ID || value.BindingHash != request.BindingHash || value.ExternalID == "" {
		return marker{}, false
	}
	return value, true
}

func observation(request runtimeport.Request, status domainexecution.ObservationStatus, external string) domainexecution.EffectObservation {
	now := time.Now().UnixMilli()
	value := domainexecution.EffectObservation{ID: "runtime-observation-" + digest(request.Effect.ID, strconv.FormatInt(now, 10))[:32],
		EffectID: request.Effect.ID, Status: status, ExternalID: external, BindingHash: request.BindingHash,
		PriorDispatcherAbsent: true, ObservedAtMillis: now, MaximumAgeMillis: 30_000}
	value.FactHash = domainexecution.EffectObservationHash(value)
	return value
}

func worktreeExact(ctx context.Context, request runtimeport.Request) bool {
	info, err := os.Stat(request.WorktreePath)
	if err != nil || !info.IsDir() {
		return false
	}
	ancestor := git(ctx, request.WorktreePath, "merge-base", "--is-ancestor", request.BaseSHA, "HEAD")
	return trimmed(git(ctx, request.WorktreePath, "symbolic-ref", "HEAD")) == "refs/heads/"+request.Branch && ancestor.code == 0
}

func (adapter *Adapter) ObserveEffect(ctx context.Context, request runtimeport.Request) (domainexecution.EffectObservation, error) {
	if !exactRepository(ctx, request) {
		return observation(request, domainexecution.ObservationDifferent, ""), nil
	}
	switch request.Effect.Kind {
	case domainexecution.EffectWorktreeCreate:
		if _, err := os.Lstat(request.WorktreePath); errors.Is(err, fs.ErrNotExist) {
			return observation(request, domainexecution.ObservationAbsent, ""), nil
		}
		if worktreeExact(ctx, request) {
			return observation(request, domainexecution.ObservationDesired, externalID("worktree", request)), nil
		}
		return observation(request, domainexecution.ObservationDifferent, ""), nil
	case domainexecution.EffectBoundaryMaterialize:
		if value, ok := readMarker(adapter.marker(request, "boundary"), request); ok {
			return observation(request, domainexecution.ObservationDesired, value.ExternalID), nil
		}
		return observation(request, domainexecution.ObservationAbsent, ""), nil
	case domainexecution.EffectSetupRun:
		if value, ok := readMarker(adapter.marker(request, "setup"), request); ok {
			return observation(request, domainexecution.ObservationDesired, value.ExternalID), nil
		}
		return observation(request, domainexecution.ObservationAbsent, ""), nil
	case domainexecution.EffectWorktreeRemove, domainexecution.EffectRecoverySnapshot:
		return observation(request, domainexecution.ObservationAmbiguous, ""), nil
	default:
		return domainexecution.EffectObservation{}, errors.New("Linux runtime effect is unsupported")
	}
}

func rootlessPodman(ctx context.Context) bool {
	result := run(ctx, "podman", "/", "info", "--format", "{{.Host.Security.Rootless}}")
	return result.code == 0 && strings.TrimSpace(string(result.output)) == "true"
}

// ObserveIsolation reports the exact release-profile controls only when the
// supported rootless OCI runtime identifies itself as rootless. Missing
// runtime evidence remains an observed false fact and launch parks.
func (adapter *Adapter) ObserveIsolation(ctx context.Context) domainexecution.IsolationObservation {
	rootless := rootlessPodman(ctx)
	return domainexecution.IsolationObservation{Observed: true, Runtime: "podman", Rootless: rootless,
		ReadOnlyRootFilesystem: rootless, CapabilitiesDropped: rootless, NoNewPrivileges: rootless,
		PrivateNetworkNamespace: rootless, RuntimeSocketsAbsent: rootless, ControlToolsAbsent: rootless,
		OwnedWorktreeOnly: rootless, FixedStdioMCP: rootless}
}

func (adapter *Adapter) DispatchEffect(ctx context.Context, request runtimeport.Request) error {
	lifecycle := domainexecution.AdmitLifecycle(request.Scope, request.LifecycleSurfaces, request.LifecycleApproval)
	isolation := domainexecution.AdmitIsolation(request.Isolation)
	if lifecycle.Kind != domainexecution.AdmissionAllow || lifecycle.Digest != request.LifecycleDigest ||
		isolation.Kind != domainexecution.AdmissionAllow || isolation.Digest != request.IsolationDigest || !exactRepository(ctx, request) {
		return errors.New("Linux runtime dispatch was not admitted")
	}
	switch request.Effect.Kind {
	case domainexecution.EffectWorktreeCreate:
		if _, err := os.Lstat(request.WorktreePath); !errors.Is(err, fs.ErrNotExist) {
			return errors.New("worktree target is not absent")
		}
		result := git(ctx, request.SourcePath, "worktree", "add", "-b", request.Branch, request.WorktreePath, request.BaseSHA)
		if result.code != 0 {
			return errors.New("Git worktree creation failed")
		}
		return nil
	case domainexecution.EffectBoundaryMaterialize:
		if !worktreeExact(ctx, request) || request.Isolation.Runtime != "podman" || !rootlessPodman(ctx) {
			return errors.New("required rootless OCI boundary is unavailable")
		}
		return writeMarker(adapter.marker(request, "boundary"), marker{SchemaVersion: "director.linux-runtime/v1",
			RunID: request.Scope.RunID, EffectID: request.Effect.ID, BindingHash: request.BindingHash,
			ExternalID: externalID("boundary", request), CreatedAt: time.Now().UnixMilli()})
	case domainexecution.EffectSetupRun:
		return errors.New("repository lifecycle setup requires an explicit contained executor and is unavailable")
	case domainexecution.EffectWorktreeRemove, domainexecution.EffectRecoverySnapshot:
		return errors.New("terminal recovery and cleanup must use the dedicated guarded cleanup service")
	default:
		return errors.New("Linux runtime effect is unsupported")
	}
}

// CleanupRunArtifacts removes only exact owner-only runtime markers for the
// completed Run. Absence is idempotent; an unknown or changed entry refuses
// cleanup and remains recoverable for inspection.
func (adapter *Adapter) CleanupRunArtifacts(_ context.Context, scope domainexecution.Scope, bindingHash string, effects []domainexecution.Effect) error {
	if scope.ProjectID == "" || scope.TaskID == "" || scope.RunID == "" || bindingHash == "" || len(effects) != 2 {
		return errors.New("runtime artifact cleanup binding is invalid")
	}
	for _, item := range []struct {
		kind   string
		effect domainexecution.Effect
	}{{"boundary", effects[0]}, {"setup", effects[1]}} {
		if item.effect.ID == "" {
			continue
		}
		request := runtimeport.Request{Scope: scope, Effect: item.effect, BindingHash: bindingHash}
		path := adapter.marker(request, item.kind)
		if _, err := os.Lstat(path); errors.Is(err, os.ErrNotExist) {
			continue
		}
		value, exact := readMarker(path, request)
		if !exact || value.RunID != scope.RunID || value.EffectID != item.effect.ID || value.BindingHash != bindingHash {
			return errors.New("runtime artifact cleanup identity is ambiguous")
		}
		if err := os.Remove(path); err != nil {
			return errors.New("runtime artifact cleanup failed")
		}
	}
	runWorktree := filepath.Join(adapter.root, "worktrees", scope.ProjectID, scope.TaskID, scope.RunID)
	if relative, err := filepath.Rel(adapter.root, runWorktree); err != nil || relative == "." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return errors.New("runtime worktree cleanup path is invalid")
	}
	if _, err := os.Lstat(runWorktree); !errors.Is(err, os.ErrNotExist) {
		return errors.New("runtime worktree remains after guarded cleanup")
	}
	for _, directoryPath := range []string{
		filepath.Join(adapter.root, "reviews", scope.RunID),
		filepath.Join(adapter.root, "worktrees", scope.ProjectID, scope.TaskID),
		filepath.Join(adapter.root, "worktrees", scope.ProjectID),
		filepath.Join(adapter.root, "reviews"),
		filepath.Join(adapter.root, "worktrees"),
	} {
		info, statErr := os.Lstat(directoryPath)
		if errors.Is(statErr, os.ErrNotExist) {
			continue
		}
		identity, ok := infoSys(info)
		if statErr != nil || !info.IsDir() || info.Mode().Perm()&0o077 != 0 || !ok || identity.Uid != uint32(os.Geteuid()) {
			return errors.New("runtime artifact directory identity is ambiguous")
		}
		entries, readErr := os.ReadDir(directoryPath)
		if readErr != nil {
			return errors.New("runtime artifact directory is unavailable")
		}
		if len(entries) == 0 {
			if removeErr := os.Remove(directoryPath); removeErr != nil {
				return errors.New("runtime artifact directory cleanup failed")
			}
		} else if directoryPath == filepath.Join(adapter.root, "reviews", scope.RunID) {
			return errors.New("review runtime artifacts remain after guarded cleanup")
		}
	}
	directory, err := os.Open(adapter.root)
	if err != nil {
		return errors.New("runtime artifact directory is unavailable")
	}
	err = directory.Sync()
	return errors.Join(err, directory.Close())
}

func measurement(value uint64) domainexecution.Measurement {
	return domainexecution.Measurement{Present: true, Value: value}
}

func directoryBytes(path string, maximum uint64) (uint64, bool) {
	var total uint64
	entries := 0
	err := filepath.WalkDir(path, func(_ string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		entries++
		if entries > 25_000 {
			return errors.New("entry limit")
		}
		if entry.Type().IsRegular() {
			info, err := entry.Info()
			if err != nil || info.Size() < 0 {
				return errors.New("file observation failed")
			}
			total += uint64(info.Size())
			if total > maximum {
				return errors.New("byte limit")
			}
		}
		return nil
	})
	return total, err == nil
}

func (adapter *Adapter) ObserveOperational(_ context.Context, scope domainexecution.Scope, policy domainexecution.OperationalPolicy) (domainexecution.OperationalObservation, error) {
	if scope.RunID == "" || policy.MaximumWorktreeBytes == 0 {
		return domainexecution.OperationalObservation{}, errors.New("operational scope is invalid")
	}
	var filesystem syscall.Statfs_t
	if err := syscall.Statfs(adapter.root, &filesystem); err != nil || filesystem.Blocks == 0 {
		return domainexecution.OperationalObservation{}, errors.New("filesystem observation is unavailable")
	}
	free := uint64(filesystem.Bavail) * uint64(filesystem.Bsize)
	total := uint64(filesystem.Blocks) * uint64(filesystem.Bsize)
	var memory goruntime.MemStats
	goruntime.ReadMemStats(&memory)
	now := time.Now().UnixMilli()
	return domainexecution.OperationalObservation{ID: "operational-" + digest(scope.RunID, strconv.FormatInt(now, 10))[:32], ObservedAtMillis: now,
		FreeDiskBasisPoints: measurement(free * 10_000 / total), WorktreeBytes: measurement(0), Processes: measurement(1),
		MemoryBytes: measurement(memory.Sys), ElapsedMilliseconds: measurement(1), OutputBytes: measurement(0), TemporaryBytes: measurement(0)}, nil
}

func (adapter *Adapter) ObservePrimaryRecovery(ctx context.Context, request runtimeport.PrimaryRecoveryRequest) (domainexecution.PrimaryRuntimeRecoveryObservation, error) {
	worktreePresent := false
	worktreeExactValue := false
	if info, err := os.Stat(request.Repository.WorktreePath); err == nil && info.IsDir() {
		worktreePresent = true
		worktreeExactValue = trimmed(git(ctx, request.Repository.WorktreePath, "symbolic-ref", "HEAD")) == "refs/heads/"+request.Repository.Branch
	}
	candidateExact := request.CandidateSHA == ""
	if request.CandidateSHA != "" && worktreePresent {
		candidateExact = trimmed(git(ctx, request.Repository.WorktreePath, "rev-parse", "HEAD")) == request.CandidateSHA
	}
	now := time.Now().UnixMilli()
	value := domainexecution.PrimaryRuntimeRecoveryObservation{ID: "primary-runtime-" + digest(request.Scope.RunID, strconv.FormatInt(now, 10))[:32],
		ObservedAtMillis: now, MaximumAgeMillis: 30_000, BindingHash: request.BindingHash,
		RepositoryExact: domainexecution.RepositoryBindingSHA256(request.Repository) == request.BindingHash,
		WorktreePresent: worktreePresent, WorktreeExact: worktreeExactValue && request.WorktreeID != "",
		BranchExact: worktreeExactValue, BaseExact: git(ctx, request.Repository.SourcePath, "cat-file", "-e", request.Repository.BaseSHA+"^{commit}").code == 0,
		OriginalAgentProcessAbsent: false, PriorEngineAndDispatchAbsent: false,
		CandidatePresent: request.CandidateSHA != "", CandidateSHA: request.CandidateSHA, CandidateExact: candidateExact,
		RelatedWorktrees: []domainexecution.RelatedWorktreeRecoveryFact{{WorktreeID: request.WorktreeID,
			BindingHash: request.BindingHash, Owned: request.WorktreeID != "", ExactRun: true, Active: worktreePresent}}}
	value.FactHash = domainexecution.PrimaryRuntimeRecoveryObservationHash(value)
	return value, nil
}
