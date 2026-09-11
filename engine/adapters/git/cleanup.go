// SPDX-License-Identifier: Apache-2.0

package git

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"
	"unicode/utf8"

	"github.com/mcuadros/director-engine/domain/candidate"
	domaincleanup "github.com/mcuadros/director-engine/domain/cleanup"
	"github.com/mcuadros/director-engine/domain/execution"
	repositorydomain "github.com/mcuadros/director-engine/domain/repository"
	cleanuport "github.com/mcuadros/director-engine/ports/cleanup"
)

// CleanupAdapter is the policy-free Git/filesystem implementation for exact
// cleanup effects. Keeping it separate from Adapter prevents method-name
// collisions between direct-delivery and cleanup observation contracts.
type CleanupAdapter struct {
	cleanupRemoteOverride    string
	afterCleanupRemoteDelete func()
}

func NewCleanup() *CleanupAdapter { return &CleanupAdapter{} }

var _ cleanuport.GitPort = (*CleanupAdapter)(nil)

type cleanupPaths struct {
	source   string
	common   string
	worktree string
}

type cleanupSnapshot struct {
	candidateTree   string
	prospectiveTree string
	indexTree       string
	dirty           bool
	ignored         bool
	ignoredEntries  uint32
	ignoredBytes    uint64
	privatePaths    []string
}

type artifactEntry struct {
	Path       string `json:"path"`
	Kind       string `json:"kind"`
	Mode       uint32 `json:"mode"`
	ModifiedNS int64  `json:"modifiedNs"`
	Size       uint64 `json:"size,omitempty"`
	SHA256     string `json:"sha256,omitempty"`
}

type artifactManifest struct {
	SchemaVersion  string          `json:"schemaVersion"`
	BindingSHA256  string          `json:"bindingSha256"`
	Entries        []artifactEntry `json:"entries"`
	AggregateBytes uint64          `json:"aggregateBytes"`
}

const artifactManifestName = ".director-recovery-manifest.json"

func cleanupObservation(target cleanuport.Target, status domaincleanup.Status, code domaincleanup.Code) domaincleanup.Observation {
	return domaincleanup.SealObservation(domaincleanup.Observation{
		EffectID: target.EffectID, BindingSHA256: target.Binding.SHA256, Kind: target.EffectKind,
		Attempt: target.Attempt, Status: status, Code: code, ObservedAtMillis: target.TaskStoreNowMillis,
		MaximumAgeMillis: domaincleanup.MaximumObservationAgeMillis,
	})
}

func validCleanupTarget(target cleanuport.Target) bool {
	binding := target.Binding
	manifest := target.CandidateManifest
	claim := target.CandidateClaim
	return domaincleanup.ValidBinding(binding) && domaincleanup.ValidPolicy(target.Policy) &&
		binding.PolicySHA256 == target.Policy.SHA256 && binding.ConfigurationSHA256 == target.Policy.ConfigurationSHA256 &&
		execution.RepositoryBindingSHA256(target.Repository) == target.RepositoryBindingSHA256 &&
		target.RepositoryBindingSHA256 == binding.RepositoryBindingSHA256 && target.Repository.RepositoryID == binding.RepositoryID &&
		target.Repository.Branch == binding.Branch && target.Repository.BaseSHA == binding.BaseSHA &&
		candidate.ValidClaim(claim) && candidate.ValidManifest(manifest) && claim.CandidateSHA == binding.CandidateSHA &&
		claim.BaseSHA == binding.BaseSHA && claim.Branch == binding.Branch && claim.BaseRef == binding.BaseRef &&
		manifest.CandidateSHA == binding.CandidateSHA && manifest.BaseSHA == binding.BaseSHA && manifest.TreeSHA == binding.TreeSHA &&
		manifest.RepositoryBindingSHA256 == binding.RepositoryBindingSHA256 && target.EffectID != "" &&
		target.Attempt <= target.Policy.AttemptLimit && target.TaskStoreNowMillis >= 0
}

func cleanupIdentity(info fs.FileInfo) (uint64, uint64, bool) {
	value, ok := info.Sys().(*syscall.Stat_t)
	if !ok || value.Dev == 0 || value.Ino == 0 {
		return 0, 0, false
	}
	return uint64(value.Dev), value.Ino, true
}

func cleanupOwned(info fs.FileInfo) bool {
	value, ok := info.Sys().(*syscall.Stat_t)
	return ok && value.Uid == uint32(os.Getuid())
}

func (adapter *CleanupAdapter) cleanupRemote(target cleanuport.Target) string {
	if adapter.cleanupRemoteOverride != "" {
		return adapter.cleanupRemoteOverride
	}
	return target.Repository.CanonicalRemote
}

func (adapter *CleanupAdapter) revalidateCleanupRepository(ctx context.Context, target cleanuport.Target, worktreeRequired bool) (cleanupPaths, domaincleanup.Code) {
	if !validCleanupTarget(target) {
		return cleanupPaths{}, domaincleanup.CodeBindingChanged
	}
	source, sourceInfo, ok := canonicalPath(target.Repository.SourcePath)
	if !ok {
		return cleanupPaths{}, domaincleanup.CodeRepositoryMismatch
	}
	common, commonInfo, ok := canonicalPath(target.Repository.GitCommonDirectory)
	if !ok {
		return cleanupPaths{}, domaincleanup.CodeRepositoryMismatch
	}
	sourceDevice, sourceInode, sourceOK := cleanupIdentity(sourceInfo)
	commonDevice, commonInode, commonOK := cleanupIdentity(commonInfo)
	if !sourceOK || !commonOK || !cleanupOwned(sourceInfo) || !cleanupOwned(commonInfo) ||
		sourceDevice != target.Repository.SourceDevice || sourceInode != target.Repository.SourceInode ||
		commonDevice != target.Repository.GitCommonDevice || commonInode != target.Repository.GitCommonInode ||
		sourceDevice != target.Binding.SourceDevice || sourceInode != target.Binding.SourceInode ||
		commonDevice != target.Binding.CommonDevice || commonInode != target.Binding.CommonInode {
		return cleanupPaths{}, domaincleanup.CodeRepositoryMismatch
	}
	commonFromSource, ok := trimmed(runGit(ctx, source, "rev-parse", "--path-format=absolute", "--git-common-dir"))
	if !ok {
		return cleanupPaths{}, domaincleanup.CodeExternalUnavailable
	}
	commonFromSource, _, ok = canonicalPath(commonFromSource)
	if !ok || commonFromSource != common {
		return cleanupPaths{}, domaincleanup.CodeRepositoryMismatch
	}
	remoteValue, ok := trimmed(runGit(ctx, source, "remote", "get-url", "origin"))
	if !ok {
		return cleanupPaths{}, domaincleanup.CodeExternalUnavailable
	}
	remote, err := repositorydomain.CanonicalRemote(remoteValue)
	if err != nil || remote.Canonical != target.Repository.CanonicalRemote || remote.ID != target.Repository.RepositoryID || remote.Key != target.Repository.RepositoryKey ||
		domaincleanup.DigestText(remote.Canonical) != target.Binding.CanonicalRemoteSHA256 ||
		domaincleanup.DigestText(source) != target.Binding.SourcePathSHA256 || domaincleanup.DigestText(common) != target.Binding.CommonDirectorySHA256 ||
		domaincleanup.DigestText(filepath.Clean(target.Repository.WorktreePath)) != target.Binding.WorktreePathSHA256 {
		return cleanupPaths{}, domaincleanup.CodeRepositoryMismatch
	}
	paths := cleanupPaths{source: source, common: common, worktree: target.Repository.WorktreePath}
	worktree, worktreeInfo, worktreeOK := canonicalPath(target.Repository.WorktreePath)
	if !worktreeOK {
		if !worktreeRequired && os.IsNotExist(lstatError(target.Repository.WorktreePath)) {
			return paths, domaincleanup.CodeOK
		}
		return cleanupPaths{}, domaincleanup.CodeRepositoryMismatch
	}
	device, inode, identityOK := cleanupIdentity(worktreeInfo)
	if !identityOK || !cleanupOwned(worktreeInfo) || device != target.Binding.WorktreeDevice || inode != target.Binding.WorktreeInode {
		return cleanupPaths{}, domaincleanup.CodeWorktreeChanged
	}
	commonFromWorktree, ok := trimmed(runGit(ctx, worktree, "rev-parse", "--path-format=absolute", "--git-common-dir"))
	if !ok || commonFromWorktree != common {
		return cleanupPaths{}, domaincleanup.CodeRepositoryMismatch
	}
	paths.worktree = worktree
	return paths, domaincleanup.CodeOK
}

func lstatError(path string) error { _, err := os.Lstat(path); return err }

func cleanupRegistrations(ctx context.Context, source string) ([]registration, bool) {
	result := runGit(ctx, source, "worktree", "list", "--porcelain", "-z")
	if !result.ok || result.code != 0 {
		return nil, false
	}
	return registrations(result.stdout)
}

func exactWorktreeRegistration(ctx context.Context, paths cleanupPaths, target cleanuport.Target) (bool, bool) {
	records, ok := cleanupRegistrations(ctx, paths.source)
	if !ok {
		return false, false
	}
	matches := 0
	foreignConsumer := false
	for _, record := range records {
		canonical := filepath.Clean(record.path)
		if resolved, err := filepath.EvalSymlinks(record.path); err == nil {
			canonical = resolved
		}
		if canonical == paths.worktree {
			if record.head != target.Binding.CandidateSHA || record.branch != "refs/heads/"+target.Binding.Branch {
				return false, false
			}
			matches++
		} else if record.branch == "refs/heads/"+target.Binding.Branch {
			foreignConsumer = true
		}
	}
	return matches == 1, foreignConsumer
}

func runCleanupGit(ctx context.Context, directory string, extraEnvironment []string, arguments ...string) deliveryCommandResult {
	global := []string{"--no-optional-locks", "--literal-pathspecs", "-c", "core.fsmonitor=false", "-c", "core.hooksPath=/dev/null",
		"-c", "diff.external=", "-c", "credential.interactive=never", "-C", directory}
	command := exec.CommandContext(ctx, "git", append(global, arguments...)...)
	command.Env = append(deliveryEnvironment(), extraEnvironment...)
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

func cleanupOutput(result deliveryCommandResult) (string, bool) {
	if !result.started || !result.ok || result.code != 0 || !utf8.Valid(result.stdout) {
		return "", false
	}
	return strings.TrimSpace(string(result.stdout)), true
}

func porcelainKinds(output []byte) (ignored map[string]struct{}, dirty bool, paths []string, ok bool) {
	ignored = map[string]struct{}{}
	for _, row := range bytes.Split(output, []byte{0}) {
		if len(row) == 0 {
			continue
		}
		if len(row) < 3 || row[2] == 0 {
			return nil, false, nil, false
		}
		path := strings.TrimSuffix(string(row[3:]), string(filepath.Separator))
		if len(row) == 3 || !safeGitPath(path) {
			return nil, false, nil, false
		}
		paths = append(paths, path)
		switch string(row[:2]) {
		case "!!":
			ignored[path] = struct{}{}
		case "??":
			dirty = true
		default:
			dirty = true
		}
	}
	return ignored, dirty, paths, true
}

func hasExecutableFilter(ctx context.Context, worktree string, paths []string) bool {
	for start := 0; start < len(paths); start += 512 {
		end := start + 512
		if end > len(paths) {
			end = len(paths)
		}
		arguments := []string{"check-attr", "-z", "filter", "--"}
		arguments = append(arguments, paths[start:end]...)
		result := runCleanupGit(ctx, worktree, nil, arguments...)
		if !result.started || !result.ok || result.code != 0 {
			return true
		}
		fields := bytes.Split(result.stdout, []byte{0})
		for index := 0; index+2 < len(fields); index += 3 {
			if string(fields[index+1]) != "filter" {
				return true
			}
			driver := string(fields[index+2])
			if driver == "unspecified" {
				continue
			}
			if driver == "set" || driver == "unset" || driver == "" {
				return true
			}
			for _, key := range []string{"filter." + driver + ".clean", "filter." + driver + ".process"} {
				configured := runCleanupGit(ctx, worktree, nil, "config", "--get", key)
				if !configured.started || !configured.ok {
					return true
				}
				if configured.code == 0 && len(bytes.TrimSpace(configured.stdout)) > 0 {
					return true
				}
				if configured.code != 0 && configured.code != 1 {
					return true
				}
			}
		}
	}
	return false
}

func temporaryTree(ctx context.Context, worktree, candidateSHA string) (string, bool) {
	file, err := os.CreateTemp("", "director-cleanup-index-")
	if err != nil {
		return "", false
	}
	path := file.Name()
	if closeErr := file.Close(); closeErr != nil {
		_ = os.Remove(path)
		return "", false
	}
	if err := os.Remove(path); err != nil {
		return "", false
	}
	defer os.Remove(path)
	environment := []string{"GIT_INDEX_FILE=" + path}
	if result := runCleanupGit(ctx, worktree, environment, "read-tree", candidateSHA); !result.started || !result.ok || result.code != 0 {
		return "", false
	}
	if result := runCleanupGit(ctx, worktree, environment, "add", "-A", "--", "."); !result.started || !result.ok || result.code != 0 {
		return "", false
	}
	return cleanupOutput(runCleanupGit(ctx, worktree, environment, "write-tree"))
}

func trackedPaths(ctx context.Context, worktree string) (map[string]struct{}, bool) {
	result := runCleanupGit(ctx, worktree, nil, "ls-files", "-z")
	if !result.started || !result.ok || result.code != 0 {
		return nil, false
	}
	tracked := map[string]struct{}{}
	for _, item := range bytes.Split(result.stdout, []byte{0}) {
		if len(item) == 0 {
			continue
		}
		path := string(item)
		if !safeGitPath(path) {
			return nil, false
		}
		tracked[path] = struct{}{}
	}
	return tracked, true
}

func ignoredMatch(path string, ignored map[string]struct{}) bool {
	if _, ok := ignored[path]; ok {
		return true
	}
	for prefix := filepath.Dir(path); prefix != "."; prefix = filepath.Dir(prefix) {
		if _, ok := ignored[prefix]; ok {
			return true
		}
		if _, ok := ignored[prefix+string(filepath.Separator)]; ok {
			return true
		}
	}
	return false
}

func scanCleanupTree(root string, tracked map[string]struct{}, ignored map[string]struct{}, policy domaincleanup.Policy) (uint32, uint64, []string, domaincleanup.Code) {
	var inspected uint32
	var entries uint32
	var aggregate uint64
	private := []string{}
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		relative, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		if relative == "." {
			return nil
		}
		if relative == ".git" {
			if entry.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if !safeGitPath(relative) {
			return errors.New("unsafe")
		}
		inspected++
		if inspected > policy.InspectedEntries {
			return errors.New("inspect-limit")
		}
		if filepath.Base(relative) == ".git" {
			return errors.New("nested-repository")
		}
		info, err := os.Lstat(path)
		if err != nil {
			return err
		}
		if !cleanupOwned(info) {
			return errors.New("unowned")
		}
		_, isTracked := tracked[relative]
		isIgnored := ignoredMatch(relative, ignored)
		if info.Mode()&os.ModeSymlink != 0 {
			if !isTracked {
				return errors.New("unsupported")
			}
			return nil
		}
		if !info.Mode().IsRegular() && !info.IsDir() {
			return errors.New("unsupported")
		}
		if isTracked {
			return nil
		}
		privateEntry := isIgnored
		if info.IsDir() && !isIgnored {
			children, readErr := os.ReadDir(path)
			if readErr != nil {
				return readErr
			}
			privateEntry = len(children) == 0
		}
		if privateEntry {
			entries++
			if entries > policy.RecoveryEntries {
				return errors.New("recovery-limit")
			}
			if info.Mode().IsRegular() {
				if info.Size() < 0 || uint64(info.Size()) > policy.FileBytes {
					return errors.New("file-limit")
				}
				if uint64(info.Size()) > policy.AggregateBytes-aggregate {
					return errors.New("aggregate-limit")
				}
				aggregate += uint64(info.Size())
			}
			private = append(private, relative)
		}
		return nil
	})
	if err != nil {
		if strings.Contains(err.Error(), "limit") {
			return 0, 0, nil, domaincleanup.CodeRecoveryLimit
		}
		return 0, 0, nil, domaincleanup.CodeUnsupportedContent
	}
	return entries, aggregate, private, domaincleanup.CodeOK
}

func (adapter *CleanupAdapter) inspectWorktree(ctx context.Context, target cleanuport.Target) (cleanupSnapshot, domaincleanup.Code) {
	paths, code := adapter.revalidateCleanupRepository(ctx, target, true)
	if code != domaincleanup.CodeOK {
		return cleanupSnapshot{}, code
	}
	exact, consumer := exactWorktreeRegistration(ctx, paths, target)
	if consumer {
		return cleanupSnapshot{}, domaincleanup.CodeConsumerPresent
	}
	if !exact {
		return cleanupSnapshot{}, domaincleanup.CodeWorktreeChanged
	}
	candidateTree, ok := cleanupOutput(runCleanupGit(ctx, paths.worktree, nil, "show", "-s", "--format=%T", target.Binding.CandidateSHA))
	if !ok || candidateTree != target.Binding.TreeSHA {
		return cleanupSnapshot{}, domaincleanup.CodeBindingChanged
	}
	indexTree, ok := cleanupOutput(runCleanupGit(ctx, paths.worktree, nil, "write-tree"))
	if !ok {
		return cleanupSnapshot{}, domaincleanup.CodeExternalUnavailable
	}
	status := runCleanupGit(ctx, paths.worktree, nil, "status", "--porcelain=v1", "-z", "--untracked-files=all", "--ignored=matching")
	if !status.started || !status.ok || status.code != 0 {
		return cleanupSnapshot{}, domaincleanup.CodeExternalUnavailable
	}
	ignored, statusDirty, statusPaths, ok := porcelainKinds(status.stdout)
	if !ok {
		return cleanupSnapshot{}, domaincleanup.CodeUnsupportedContent
	}
	tracked, ok := trackedPaths(ctx, paths.worktree)
	if !ok {
		return cleanupSnapshot{}, domaincleanup.CodeExternalUnavailable
	}
	filterPaths := make([]string, 0, len(tracked)+len(statusPaths))
	for path := range tracked {
		filterPaths = append(filterPaths, path)
	}
	filterPaths = append(filterPaths, statusPaths...)
	if hasExecutableFilter(ctx, paths.worktree, filterPaths) {
		return cleanupSnapshot{}, domaincleanup.CodeUnsupportedContent
	}
	entries, aggregate, private, code := scanCleanupTree(paths.worktree, tracked, ignored, target.Policy)
	if code != domaincleanup.CodeOK {
		return cleanupSnapshot{}, code
	}
	prospectiveTree, ok := temporaryTree(ctx, paths.worktree, target.Binding.CandidateSHA)
	if !ok {
		return cleanupSnapshot{}, domaincleanup.CodeExternalUnavailable
	}
	dirty := statusDirty || prospectiveTree != candidateTree || indexTree != candidateTree
	ignoredPresent := len(ignored) > 0 || entries > 0
	// Dirty-plus-ignored content stays in place; Git recovery must never make
	// ignored bytes reachable and the two captures cannot be claimed atomic.
	if dirty && len(ignored) > 0 {
		return cleanupSnapshot{}, domaincleanup.CodeUnsupportedContent
	}
	return cleanupSnapshot{candidateTree: candidateTree, prospectiveTree: prospectiveTree, indexTree: indexTree,
		dirty: dirty, ignored: ignoredPresent, ignoredEntries: entries, ignoredBytes: aggregate, privatePaths: private}, domaincleanup.CodeOK
}

func recoveryRefs(binding domaincleanup.Binding) (string, string) {
	prefix := "refs/director/recovery/" + binding.TaskID + "/" + binding.RunID + "/" + binding.SHA256[:16]
	return prefix + "/worktree", prefix + "/index"
}

func refOID(ctx context.Context, source, ref string) (string, bool, bool) {
	result := runCleanupGit(ctx, source, nil, "rev-parse", "--verify", "--quiet", ref)
	if result.started && result.ok && result.code == 1 && len(result.stdout) == 0 {
		return "", false, true
	}
	value, ok := cleanupOutput(result)
	return value, ok, ok
}

func verifyRecoveryCommit(ctx context.Context, source, commit, tree, parent string) bool {
	observedTree, ok := cleanupOutput(runCleanupGit(ctx, source, nil, "show", "-s", "--format=%T", commit))
	if !ok || observedTree != tree {
		return false
	}
	observedParent, ok := cleanupOutput(runCleanupGit(ctx, source, nil, "show", "-s", "--format=%P", commit))
	return ok && observedParent == parent
}

func artifactDirectory(target cleanuport.Target) string {
	return filepath.Join(target.PrivateArtifactRoot, "cleanup-"+target.Binding.SHA256[:32])
}

func safeArtifactRoot(root string, worktree string) (string, bool) {
	if !filepath.IsAbs(root) || filepath.Clean(root) != root || root == worktree || strings.HasPrefix(root+string(filepath.Separator), worktree+string(filepath.Separator)) ||
		strings.HasPrefix(worktree+string(filepath.Separator), root+string(filepath.Separator)) {
		return "", false
	}
	if err := os.MkdirAll(root, 0o700); err != nil {
		return "", false
	}
	info, err := os.Lstat(root)
	if err != nil || !info.IsDir() || !cleanupOwned(info) || info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm() != 0o700 {
		return "", false
	}
	real, err := filepath.EvalSymlinks(root)
	return real, err == nil && real == root
}

func freeSpaceAdmitted(path string, reservation uint64, floor uint32) bool {
	var stat syscall.Statfs_t
	if syscall.Statfs(path, &stat) != nil || stat.Blocks == 0 {
		return false
	}
	total := uint64(stat.Blocks) * uint64(stat.Bsize)
	available := uint64(stat.Bavail) * uint64(stat.Bsize)
	if reservation > available {
		return false
	}
	return (available-reservation)*10_000 >= total*uint64(floor)
}

func copyRegularExact(source, destination string, expected fs.FileInfo, bufferBytes uint32) (string, error) {
	input, err := os.Open(source)
	if err != nil {
		return "", err
	}
	defer input.Close()
	opened, err := input.Stat()
	if err != nil || !os.SameFile(expected, opened) || !opened.Mode().IsRegular() {
		return "", errors.New("source changed")
	}
	output, err := os.OpenFile(destination, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return "", err
	}
	hash := sha256.New()
	_, copyErr := io.CopyBuffer(io.MultiWriter(output, hash), input, make([]byte, bufferBytes))
	syncErr := output.Sync()
	closeErr := output.Close()
	if copyErr != nil || syncErr != nil || closeErr != nil {
		return "", errors.New("artifact copy failed")
	}
	return hex.EncodeToString(hash.Sum(nil)), nil
}

func createPrivateArtifact(target cleanuport.Target, paths cleanupPaths, snapshot cleanupSnapshot) (string, string, error) {
	root, ok := safeArtifactRoot(target.PrivateArtifactRoot, paths.worktree)
	if !ok {
		return "", "", errors.New("private root invalid")
	}
	reservation := snapshot.ignoredBytes + uint64(target.Policy.StreamBufferBytes) + uint64(snapshot.ignoredEntries)*4*1024
	if !freeSpaceAdmitted(root, reservation, target.Policy.FreeSpaceFloorBasisPoint) {
		return "", "", errors.New("disk pressure")
	}
	final := artifactDirectory(target)
	if _, err := os.Lstat(final); err == nil {
		return "", "", errors.New("artifact already present")
	}
	staging, err := os.MkdirTemp(root, ".cleanup-staging-")
	if err != nil {
		return "", "", err
	}
	if err := os.Chmod(staging, 0o700); err != nil {
		_ = os.RemoveAll(staging)
		return "", "", err
	}
	removeStaging := true
	defer func() {
		if removeStaging {
			_ = os.RemoveAll(staging)
		}
	}()
	manifest := artifactManifest{SchemaVersion: "director.private-recovery-artifact/v1", BindingSHA256: target.Binding.SHA256}
	for _, relative := range snapshot.privatePaths {
		source := filepath.Join(paths.worktree, relative)
		clean := filepath.Clean(relative)
		if clean != relative || !safeGitPath(relative) {
			return "", "", errors.New("artifact path invalid")
		}
		info, err := os.Lstat(source)
		if err != nil || info.Mode()&os.ModeSymlink != 0 {
			return "", "", errors.New("artifact source changed")
		}
		destination := filepath.Join(staging, relative)
		entry := artifactEntry{Path: relative, Mode: uint32(info.Mode().Perm()), ModifiedNS: info.ModTime().UnixNano()}
		if info.IsDir() {
			entry.Kind = "directory"
			if err := os.MkdirAll(destination, 0o700); err != nil {
				return "", "", err
			}
		} else if info.Mode().IsRegular() {
			entry.Kind, entry.Size = "regular", uint64(info.Size())
			if err := os.MkdirAll(filepath.Dir(destination), 0o700); err != nil {
				return "", "", err
			}
			hash, err := copyRegularExact(source, destination, info, target.Policy.StreamBufferBytes)
			if err != nil {
				return "", "", err
			}
			entry.SHA256 = hash
		} else {
			return "", "", errors.New("artifact source unsupported")
		}
		manifest.Entries = append(manifest.Entries, entry)
		manifest.AggregateBytes += entry.Size
	}
	encoded, err := json.Marshal(manifest)
	if err != nil {
		return "", "", err
	}
	manifestHash := sha256.Sum256(encoded)
	manifestFile, err := os.OpenFile(filepath.Join(staging, artifactManifestName), os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return "", "", err
	}
	_, writeErr := manifestFile.Write(append(encoded, '\n'))
	if writeErr == nil {
		writeErr = manifestFile.Sync()
	}
	closeErr := manifestFile.Close()
	if writeErr != nil || closeErr != nil {
		return "", "", errors.New("artifact manifest write failed")
	}
	if err := os.Rename(staging, final); err != nil {
		return "", "", err
	}
	removeStaging = false
	parent, err := os.Open(root)
	if err != nil {
		return "", "", err
	}
	err = parent.Sync()
	_ = parent.Close()
	if err != nil {
		return "", "", err
	}
	return "artifact-" + target.Binding.SHA256[:32], hex.EncodeToString(manifestHash[:]), nil
}

func verifyPrivateArtifact(target cleanuport.Target, snapshot domaincleanup.SnapshotEvidence) bool {
	if snapshot.PrivateArtifactID == "" {
		return true
	}
	root, ok := safeArtifactRoot(target.PrivateArtifactRoot, target.Repository.WorktreePath)
	if !ok {
		return false
	}
	directory := artifactDirectory(target)
	info, err := os.Lstat(directory)
	if err != nil || !info.IsDir() || !cleanupOwned(info) || info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm() != 0o700 || filepath.Dir(directory) != root {
		return false
	}
	manifestPath := filepath.Join(directory, artifactManifestName)
	manifestInfo, err := os.Lstat(manifestPath)
	if err != nil || !manifestInfo.Mode().IsRegular() || !cleanupOwned(manifestInfo) || manifestInfo.Mode()&os.ModeSymlink != 0 || manifestInfo.Mode().Perm() != 0o600 {
		return false
	}
	content, err := os.ReadFile(manifestPath)
	if err != nil || len(content) > 4*1024*1024 {
		return false
	}
	content = bytes.TrimSuffix(content, []byte{'\n'})
	hash := sha256.Sum256(content)
	if hex.EncodeToString(hash[:]) != snapshot.PrivateArtifactSHA256 {
		return false
	}
	var manifest artifactManifest
	if json.Unmarshal(content, &manifest) != nil || manifest.SchemaVersion != "director.private-recovery-artifact/v1" ||
		manifest.BindingSHA256 != target.Binding.SHA256 || uint32(len(manifest.Entries)) != snapshot.EntryCount || manifest.AggregateBytes != snapshot.AggregateBytes {
		return false
	}
	var aggregate uint64
	for _, entry := range manifest.Entries {
		if !safeGitPath(entry.Path) {
			return false
		}
		path := filepath.Join(directory, entry.Path)
		info, err := os.Lstat(path)
		if err != nil || !cleanupOwned(info) || info.Mode()&os.ModeSymlink != 0 {
			return false
		}
		if entry.Kind == "directory" {
			if !info.IsDir() || info.Mode().Perm() != 0o700 {
				return false
			}
			continue
		}
		if entry.Kind != "regular" || !info.Mode().IsRegular() || info.Mode().Perm() != 0o600 || uint64(info.Size()) != entry.Size {
			return false
		}
		file, err := os.Open(path)
		if err != nil {
			return false
		}
		hasher := sha256.New()
		_, err = io.CopyBuffer(hasher, file, make([]byte, target.Policy.StreamBufferBytes))
		_ = file.Close()
		if err != nil || hex.EncodeToString(hasher.Sum(nil)) != entry.SHA256 {
			return false
		}
		aggregate += entry.Size
	}
	return aggregate == snapshot.AggregateBytes
}

func (adapter *CleanupAdapter) verifyStoredSnapshot(ctx context.Context, target cleanuport.Target, snapshot domaincleanup.SnapshotEvidence) bool {
	if !domaincleanup.ValidSnapshot(snapshot, target.Binding, target.Policy) {
		return false
	}
	paths, code := adapter.revalidateCleanupRepository(ctx, target, false)
	if code != domaincleanup.CodeOK {
		return false
	}
	worktreeOID, present, known := refOID(ctx, paths.source, snapshot.WorktreeRef)
	if !known || !present || worktreeOID != snapshot.WorktreeCommitSHA ||
		!verifyRecoveryCommit(ctx, paths.source, worktreeOID, snapshot.WorktreeTreeSHA, target.Binding.CandidateSHA) {
		return false
	}
	if snapshot.IndexRef != "" {
		indexOID, indexPresent, indexKnown := refOID(ctx, paths.source, snapshot.IndexRef)
		if !indexKnown || !indexPresent || indexOID != snapshot.IndexCommitSHA ||
			!verifyRecoveryCommit(ctx, paths.source, indexOID, snapshot.IndexTreeSHA, target.Binding.CandidateSHA) {
			return false
		}
	}
	return verifyPrivateArtifact(target, snapshot)
}

func (adapter *CleanupAdapter) observeSnapshot(ctx context.Context, target cleanuport.Target) domaincleanup.Observation {
	if target.Binding.ReclaimedEvidenceSHA256 != "" && os.IsNotExist(lstatError(target.Repository.WorktreePath)) {
		paths, code := adapter.revalidateCleanupRepository(ctx, target, false)
		if code == domaincleanup.CodeOK {
			records, ok := cleanupRegistrations(ctx, paths.source)
			if ok {
				for _, record := range records {
					if filepath.Clean(record.path) == target.Repository.WorktreePath {
						return cleanupObservation(target, domaincleanup.StatusAmbiguous, domaincleanup.CodeWorktreeChanged)
					}
				}
				observation := cleanupObservation(target, domaincleanup.StatusAbsent, domaincleanup.CodeOK)
				observation.RegistrationAbsent = true
				return domaincleanup.SealObservation(observation)
			}
		}
	}
	inspection, code := adapter.inspectWorktree(ctx, target)
	if code != domaincleanup.CodeOK {
		status := domaincleanup.StatusAmbiguous
		if code == domaincleanup.CodeExternalUnavailable {
			status = domaincleanup.StatusUnavailable
		}
		return cleanupObservation(target, status, code)
	}
	worktreeRef, indexRef := recoveryRefs(target.Binding)
	worktreeCommit, worktreePresent, worktreeKnown := refOID(ctx, target.Repository.SourcePath, worktreeRef)
	indexCommit, indexPresent, indexKnown := refOID(ctx, target.Repository.SourcePath, indexRef)
	if !worktreeKnown || !indexKnown {
		return cleanupObservation(target, domaincleanup.StatusUnavailable, domaincleanup.CodeExternalUnavailable)
	}
	if worktreePresent {
		snapshot := domaincleanup.SealSnapshot(domaincleanup.SnapshotEvidence{BindingSHA256: target.Binding.SHA256,
			WorktreeRef: worktreeRef, WorktreeCommitSHA: worktreeCommit, WorktreeTreeSHA: inspection.prospectiveTree,
			IndexTreeSHA: inspection.indexTree, EntryCount: inspection.ignoredEntries, AggregateBytes: inspection.ignoredBytes,
			CreatedAtMillis: target.Binding.CleanupAdmittedAtMillis, RetentionUntilMillis: target.Binding.CleanupAdmittedAtMillis + target.Policy.RetentionMillis})
		if indexPresent {
			snapshot.IndexRef, snapshot.IndexCommitSHA = indexRef, indexCommit
			snapshot = domaincleanup.SealSnapshot(snapshot)
		}
		if inspection.ignored {
			snapshot.PrivateArtifactID = "artifact-" + target.Binding.SHA256[:32]
			manifestPath := filepath.Join(artifactDirectory(target), artifactManifestName)
			content, err := os.ReadFile(manifestPath)
			if err != nil {
				return cleanupObservation(target, domaincleanup.StatusAmbiguous, domaincleanup.CodeSnapshotUnverified)
			}
			hash := sha256.Sum256(bytes.TrimSuffix(content, []byte{'\n'}))
			snapshot.PrivateArtifactSHA256 = hex.EncodeToString(hash[:])
			snapshot = domaincleanup.SealSnapshot(snapshot)
		}
		validIndex := !indexPresent || verifyRecoveryCommit(ctx, target.Repository.SourcePath, indexCommit, inspection.indexTree, target.Binding.CandidateSHA)
		if verifyRecoveryCommit(ctx, target.Repository.SourcePath, worktreeCommit, inspection.prospectiveTree, target.Binding.CandidateSHA) && validIndex &&
			domaincleanup.ValidSnapshot(snapshot, target.Binding, target.Policy) && verifyPrivateArtifact(target, snapshot) {
			observation := cleanupObservation(target, domaincleanup.StatusVerified, domaincleanup.CodeOK)
			observation.Snapshot = &snapshot
			observation.CandidateTreeSHA, observation.ProspectiveTreeSHA, observation.IndexTreeSHA = inspection.candidateTree, inspection.prospectiveTree, inspection.indexTree
			observation.Dirty, observation.Ignored = inspection.dirty, inspection.ignored
			observation.IgnoredEntries, observation.IgnoredBytes = inspection.ignoredEntries, inspection.ignoredBytes
			return domaincleanup.SealObservation(observation)
		}
		return cleanupObservation(target, domaincleanup.StatusAmbiguous, domaincleanup.CodeSnapshotUnverified)
	}
	if indexPresent {
		return cleanupObservation(target, domaincleanup.StatusAmbiguous, domaincleanup.CodeSnapshotUnverified)
	}
	status := domaincleanup.StatusClean
	if inspection.dirty {
		status = domaincleanup.StatusDirty
	}
	observation := cleanupObservation(target, status, domaincleanup.CodeOK)
	observation.CandidateTreeSHA, observation.ProspectiveTreeSHA, observation.IndexTreeSHA = inspection.candidateTree, inspection.prospectiveTree, inspection.indexTree
	observation.Dirty, observation.Ignored = inspection.dirty, inspection.ignored
	observation.IgnoredEntries, observation.IgnoredBytes = inspection.ignoredEntries, inspection.ignoredBytes
	return domaincleanup.SealObservation(observation)
}

func (adapter *CleanupAdapter) observeWorktree(ctx context.Context, target cleanuport.Target) domaincleanup.Observation {
	paths, code := adapter.revalidateCleanupRepository(ctx, target, false)
	if code != domaincleanup.CodeOK {
		if os.IsNotExist(lstatError(target.Repository.WorktreePath)) {
			records, ok := cleanupRegistrations(ctx, target.Repository.SourcePath)
			if !ok {
				return cleanupObservation(target, domaincleanup.StatusUnavailable, domaincleanup.CodeExternalUnavailable)
			}
			for _, record := range records {
				if filepath.Clean(record.path) == target.Repository.WorktreePath {
					return cleanupObservation(target, domaincleanup.StatusAmbiguous, domaincleanup.CodeWorktreeChanged)
				}
			}
			observation := cleanupObservation(target, domaincleanup.StatusAbsent, domaincleanup.CodeOK)
			observation.RegistrationAbsent = true
			return domaincleanup.SealObservation(observation)
		}
		status := domaincleanup.StatusAmbiguous
		if code == domaincleanup.CodeExternalUnavailable {
			status = domaincleanup.StatusUnavailable
		}
		return cleanupObservation(target, status, code)
	}
	exact, consumer := exactWorktreeRegistration(ctx, paths, target)
	if consumer {
		return cleanupObservation(target, domaincleanup.StatusAmbiguous, domaincleanup.CodeConsumerPresent)
	}
	if !exact {
		return cleanupObservation(target, domaincleanup.StatusDifferent, domaincleanup.CodeWorktreeChanged)
	}
	return cleanupObservation(target, domaincleanup.StatusExactPresent, domaincleanup.CodeOK)
}

func parseRemoteCleanupRef(result deliveryCommandResult, ref string, oidLength int) (string, domaincleanup.Status, domaincleanup.Code) {
	if !result.started || !result.ok {
		return "", domaincleanup.StatusUnavailable, domaincleanup.CodeExternalUnavailable
	}
	if result.code == 2 && len(bytes.TrimSpace(result.stdout)) == 0 {
		return "", domaincleanup.StatusAbsent, domaincleanup.CodeOK
	}
	if result.code != 0 || !utf8.Valid(result.stdout) {
		return "", domaincleanup.StatusUnavailable, domaincleanup.CodeExternalUnavailable
	}
	fields := strings.Fields(string(result.stdout))
	if len(fields) != 2 || fields[1] != ref || len(fields[0]) != oidLength || !gitOID(fields[0]) {
		return "", domaincleanup.StatusAmbiguous, domaincleanup.CodeResponseUnknown
	}
	return fields[0], domaincleanup.StatusExactPresent, domaincleanup.CodeOK
}

func (adapter *CleanupAdapter) remoteBaseContainsCandidate(ctx context.Context, target cleanuport.Target) bool {
	base := runCleanupGit(ctx, target.Repository.SourcePath, nil, "ls-remote", "--exit-code", "--refs", adapter.cleanupRemote(target), target.Binding.BaseRef)
	baseOID, status, code := parseRemoteCleanupRef(base, target.Binding.BaseRef, len(target.Binding.CandidateSHA))
	if status != domaincleanup.StatusExactPresent || code != domaincleanup.CodeOK {
		return false
	}
	fetch := runCleanupGit(ctx, target.Repository.SourcePath, nil, "fetch", "--no-tags", "--no-write-fetch-head", adapter.cleanupRemote(target), baseOID)
	if !fetch.started || !fetch.ok || fetch.code != 0 {
		return false
	}
	contains := runCleanupGit(ctx, target.Repository.SourcePath, nil, "merge-base", "--is-ancestor", target.Binding.CandidateSHA, baseOID)
	return contains.started && contains.ok && contains.code == 0
}

func (adapter *CleanupAdapter) observeRef(ctx context.Context, target cleanuport.Target, remote bool) domaincleanup.Observation {
	paths, code := adapter.revalidateCleanupRepository(ctx, target, false)
	if code != domaincleanup.CodeOK && !(os.IsNotExist(lstatError(target.Repository.WorktreePath)) && paths.source != "") {
		status := domaincleanup.StatusAmbiguous
		if code == domaincleanup.CodeExternalUnavailable {
			status = domaincleanup.StatusUnavailable
		}
		return cleanupObservation(target, status, code)
	}
	ref := "refs/heads/" + target.Binding.Branch
	if remote {
		if target.Binding.IntegrationKind == "" || !adapter.remoteBaseContainsCandidate(ctx, target) {
			return cleanupObservation(target, domaincleanup.StatusAmbiguous, domaincleanup.CodeIntegrationUnverified)
		}
		oid, status, code := parseRemoteCleanupRef(runCleanupGit(ctx, target.Repository.SourcePath, nil, "ls-remote", "--exit-code", "--refs", adapter.cleanupRemote(target), ref), ref, len(target.Binding.CandidateSHA))
		observation := cleanupObservation(target, status, code)
		observation.CurrentOID = oid
		if status == domaincleanup.StatusExactPresent && oid != target.Binding.CandidateSHA {
			observation.Status, observation.Code = domaincleanup.StatusDifferent, domaincleanup.CodeRefChanged
		}
		return domaincleanup.SealObservation(observation)
	}
	records, ok := cleanupRegistrations(ctx, target.Repository.SourcePath)
	if !ok {
		return cleanupObservation(target, domaincleanup.StatusUnavailable, domaincleanup.CodeExternalUnavailable)
	}
	for _, record := range records {
		if record.branch == ref {
			return cleanupObservation(target, domaincleanup.StatusAmbiguous, domaincleanup.CodeConsumerPresent)
		}
	}
	oid, present, known := refOID(ctx, target.Repository.SourcePath, ref)
	if !known {
		return cleanupObservation(target, domaincleanup.StatusUnavailable, domaincleanup.CodeExternalUnavailable)
	}
	if !present {
		return cleanupObservation(target, domaincleanup.StatusAbsent, domaincleanup.CodeOK)
	}
	observation := cleanupObservation(target, domaincleanup.StatusExactPresent, domaincleanup.CodeOK)
	observation.CurrentOID = oid
	if oid != target.Binding.CandidateSHA {
		observation.Status, observation.Code = domaincleanup.StatusDifferent, domaincleanup.CodeRefChanged
	}
	return domaincleanup.SealObservation(observation)
}

func (adapter *CleanupAdapter) observePrivateArtifact(target cleanuport.Target) domaincleanup.Observation {
	if target.Snapshot == nil || !domaincleanup.ValidSnapshot(*target.Snapshot, target.Binding, target.Policy) {
		return cleanupObservation(target, domaincleanup.StatusAmbiguous, domaincleanup.CodeSnapshotUnverified)
	}
	if target.TaskStoreNowMillis < target.Snapshot.RetentionUntilMillis {
		return cleanupObservation(target, domaincleanup.StatusAmbiguous, domaincleanup.CodeRetentionNotExpired)
	}
	path := artifactDirectory(target)
	quarantine := path + ".expired"
	if _, err := os.Lstat(quarantine); err == nil {
		return cleanupObservation(target, domaincleanup.StatusAmbiguous, domaincleanup.CodeResponseUnknown)
	} else if !os.IsNotExist(err) {
		return cleanupObservation(target, domaincleanup.StatusUnavailable, domaincleanup.CodeExternalUnavailable)
	}
	if _, err := os.Lstat(path); os.IsNotExist(err) {
		return cleanupObservation(target, domaincleanup.StatusAbsent, domaincleanup.CodeOK)
	} else if err != nil {
		return cleanupObservation(target, domaincleanup.StatusUnavailable, domaincleanup.CodeExternalUnavailable)
	}
	if !verifyPrivateArtifact(target, *target.Snapshot) {
		return cleanupObservation(target, domaincleanup.StatusDifferent, domaincleanup.CodeOwnerMismatch)
	}
	return cleanupObservation(target, domaincleanup.StatusExactPresent, domaincleanup.CodeOK)
}

func (adapter *CleanupAdapter) observeRecoveryRefs(ctx context.Context, target cleanuport.Target) domaincleanup.Observation {
	if target.Snapshot == nil || !domaincleanup.ValidSnapshot(*target.Snapshot, target.Binding, target.Policy) {
		return cleanupObservation(target, domaincleanup.StatusAmbiguous, domaincleanup.CodeSnapshotUnverified)
	}
	if target.TaskStoreNowMillis < target.Snapshot.RetentionUntilMillis {
		return cleanupObservation(target, domaincleanup.StatusAmbiguous, domaincleanup.CodeRetentionNotExpired)
	}
	if target.Snapshot.PrivateArtifactID != "" {
		artifactTarget := target
		artifactTarget.EffectKind = domaincleanup.ResourcePrivateArtifact
		artifact := adapter.observePrivateArtifact(artifactTarget)
		if artifact.Status != domaincleanup.StatusAbsent {
			return cleanupObservation(target, domaincleanup.StatusAmbiguous, domaincleanup.CodeSnapshotUnverified)
		}
	}
	paths, code := adapter.revalidateCleanupRepository(ctx, target, false)
	if code != domaincleanup.CodeOK {
		status := domaincleanup.StatusAmbiguous
		if code == domaincleanup.CodeExternalUnavailable {
			status = domaincleanup.StatusUnavailable
		}
		return cleanupObservation(target, status, code)
	}
	type wantedRef struct{ ref, oid string }
	wanted := []wantedRef{{target.Snapshot.WorktreeRef, target.Snapshot.WorktreeCommitSHA}}
	if target.Snapshot.IndexRef != "" {
		wanted = append(wanted, wantedRef{target.Snapshot.IndexRef, target.Snapshot.IndexCommitSHA})
	}
	absent := 0
	for _, item := range wanted {
		oid, present, known := refOID(ctx, paths.source, item.ref)
		if !known {
			return cleanupObservation(target, domaincleanup.StatusUnavailable, domaincleanup.CodeExternalUnavailable)
		}
		if !present {
			absent++
			continue
		}
		if oid != item.oid || !verifyRecoveryCommit(ctx, paths.source, oid,
			map[bool]string{true: target.Snapshot.IndexTreeSHA, false: target.Snapshot.WorktreeTreeSHA}[item.ref == target.Snapshot.IndexRef], target.Binding.CandidateSHA) {
			return cleanupObservation(target, domaincleanup.StatusDifferent, domaincleanup.CodeRefChanged)
		}
	}
	if absent == len(wanted) {
		return cleanupObservation(target, domaincleanup.StatusAbsent, domaincleanup.CodeOK)
	}
	if absent != 0 {
		return cleanupObservation(target, domaincleanup.StatusAmbiguous, domaincleanup.CodeResponseUnknown)
	}
	return cleanupObservation(target, domaincleanup.StatusExactPresent, domaincleanup.CodeOK)
}

func (adapter *CleanupAdapter) Observe(ctx context.Context, target cleanuport.Target) (domaincleanup.Observation, error) {
	ctx, cancel := context.WithTimeout(ctx, time.Duration(target.Policy.PhaseMillis)*time.Millisecond)
	defer cancel()
	if !validCleanupTarget(target) {
		return cleanupObservation(target, domaincleanup.StatusAmbiguous, domaincleanup.CodeBindingChanged), nil
	}
	switch target.EffectKind {
	case domaincleanup.ResourceSnapshot:
		return adapter.observeSnapshot(ctx, target), nil
	case domaincleanup.ResourceWorktree:
		return adapter.observeWorktree(ctx, target), nil
	case domaincleanup.ResourceRemoteRef:
		return adapter.observeRef(ctx, target, true), nil
	case domaincleanup.ResourceLocalRef:
		return adapter.observeRef(ctx, target, false), nil
	case domaincleanup.ResourcePrivateArtifact:
		return adapter.observePrivateArtifact(target), nil
	case domaincleanup.ResourceRecoveryRef:
		return adapter.observeRecoveryRefs(ctx, target), nil
	default:
		return cleanupObservation(target, domaincleanup.StatusAmbiguous, domaincleanup.CodeBindingChanged), nil
	}
}

func runCleanupGitInput(ctx context.Context, directory string, input []byte, arguments ...string) deliveryCommandResult {
	global := []string{"--no-optional-locks", "--literal-pathspecs", "-c", "core.fsmonitor=false", "-c", "core.hooksPath=/dev/null",
		"-c", "diff.external=", "-C", directory}
	command := exec.CommandContext(ctx, "git", append(global, arguments...)...)
	command.Env = deliveryEnvironment()
	command.Stdin = bytes.NewReader(input)
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

func recoveryCommit(ctx context.Context, source, tree, parent, label string) (string, bool) {
	environment := []string{
		"GIT_AUTHOR_NAME=Director Recovery", "GIT_AUTHOR_EMAIL=director@localhost",
		"GIT_COMMITTER_NAME=Director Recovery", "GIT_COMMITTER_EMAIL=director@localhost",
		"GIT_AUTHOR_DATE=1970-01-01T00:00:00Z", "GIT_COMMITTER_DATE=1970-01-01T00:00:00Z",
	}
	return cleanupOutput(runCleanupGit(ctx, source, environment, "commit-tree", tree, "-p", parent, "-m", label))
}

func (adapter *CleanupAdapter) CreateSnapshot(ctx context.Context, command cleanuport.DispatchCommand) (cleanuport.DispatchResult, error) {
	ctx, cancel := context.WithTimeout(ctx, time.Duration(command.Target.Policy.PhaseMillis)*time.Millisecond)
	defer cancel()
	target := command.Target
	if command.Attempt != target.Attempt+1 || command.Attempt > target.Policy.AttemptLimit || target.EffectKind != domaincleanup.ResourceSnapshot ||
		!domaincleanup.ValidObservation(command.ExpectedObservation, target.Binding, target.EffectID, target.EffectKind, target.TaskStoreNowMillis) ||
		(command.ExpectedObservation.Status != domaincleanup.StatusClean && command.ExpectedObservation.Status != domaincleanup.StatusDirty && command.ExpectedObservation.Status != domaincleanup.StatusExactPresent) {
		return cleanuport.DispatchResult{Code: domaincleanup.CodeBindingChanged}, &cleanuport.DispatchError{Failure: cleanuport.FailureBeforeHandoff}
	}
	observed := adapter.observeSnapshot(ctx, target)
	if observed.FactSHA256 != command.ExpectedObservation.FactSHA256 {
		return cleanuport.DispatchResult{Code: domaincleanup.CodeWorktreeChanged}, &cleanuport.DispatchError{Failure: cleanuport.FailureBeforeHandoff}
	}
	paths, code := adapter.revalidateCleanupRepository(ctx, target, true)
	if code != domaincleanup.CodeOK {
		return cleanuport.DispatchResult{Code: code}, &cleanuport.DispatchError{Failure: cleanuport.FailureBeforeHandoff}
	}
	worktreeRef, indexRef := recoveryRefs(target.Binding)
	worktreeCommit, ok := recoveryCommit(ctx, paths.source, observed.ProspectiveTreeSHA, target.Binding.CandidateSHA, "Director recovery worktree")
	if !ok {
		return cleanuport.DispatchResult{Handoff: true, Code: domaincleanup.CodeResponseUnknown}, &cleanuport.DispatchError{Failure: cleanuport.FailurePossibleHandoff}
	}
	created := runCleanupGit(ctx, paths.source, nil, "update-ref", worktreeRef, worktreeCommit, strings.Repeat("0", len(target.Binding.CandidateSHA)))
	if !created.started {
		return cleanuport.DispatchResult{Code: domaincleanup.CodeExternalUnavailable}, &cleanuport.DispatchError{Failure: cleanuport.FailureBeforeHandoff}
	}
	if !created.ok || created.code != 0 {
		return cleanuport.DispatchResult{Handoff: true, Code: domaincleanup.CodeResponseUnknown}, &cleanuport.DispatchError{Failure: cleanuport.FailurePossibleHandoff}
	}
	if observed.IndexTreeSHA != target.Binding.TreeSHA {
		indexCommit, ok := recoveryCommit(ctx, paths.source, observed.IndexTreeSHA, target.Binding.CandidateSHA, "Director recovery index")
		if !ok {
			return cleanuport.DispatchResult{Handoff: true, Code: domaincleanup.CodeResponseUnknown}, &cleanuport.DispatchError{Failure: cleanuport.FailurePossibleHandoff}
		}
		created = runCleanupGit(ctx, paths.source, nil, "update-ref", indexRef, indexCommit, strings.Repeat("0", len(target.Binding.CandidateSHA)))
		if !created.started || !created.ok || created.code != 0 {
			return cleanuport.DispatchResult{Handoff: true, Code: domaincleanup.CodeResponseUnknown}, &cleanuport.DispatchError{Failure: cleanuport.FailurePossibleHandoff}
		}
	}
	if observed.Ignored {
		inspection, inspectCode := adapter.inspectWorktree(ctx, target)
		if inspectCode != domaincleanup.CodeOK {
			return cleanuport.DispatchResult{Handoff: true, Code: inspectCode}, &cleanuport.DispatchError{Failure: cleanuport.FailurePossibleHandoff}
		}
		if _, _, err := createPrivateArtifact(target, paths, inspection); err != nil {
			code := domaincleanup.CodeSnapshotUnverified
			if strings.Contains(err.Error(), "disk") {
				code = domaincleanup.CodeDiskPressure
			}
			return cleanuport.DispatchResult{Handoff: true, Code: code}, &cleanuport.DispatchError{Failure: cleanuport.FailurePossibleHandoff}
		}
	}
	return cleanuport.DispatchResult{Handoff: true, Code: domaincleanup.CodeOK}, nil
}

func validateCleanupDispatch(command cleanuport.DispatchCommand, kind domaincleanup.ResourceKind) bool {
	target := command.Target
	return validCleanupTarget(target) && target.EffectKind == kind && command.Attempt == target.Attempt+1 &&
		command.Attempt <= target.Policy.AttemptLimit && domaincleanup.ValidObservation(command.ExpectedObservation,
		target.Binding, target.EffectID, kind, target.TaskStoreNowMillis) &&
		command.ExpectedObservation.Status == domaincleanup.StatusExactPresent
}

func (adapter *CleanupAdapter) RemoveWorktree(ctx context.Context, command cleanuport.DispatchCommand) (cleanuport.DispatchResult, error) {
	ctx, cancel := context.WithTimeout(ctx, time.Duration(command.Target.Policy.PhaseMillis)*time.Millisecond)
	defer cancel()
	if !validateCleanupDispatch(command, domaincleanup.ResourceWorktree) ||
		(command.Target.Binding.IntegrationKind == "" && command.Snapshot == nil) ||
		(command.Snapshot != nil && !domaincleanup.ValidSnapshot(*command.Snapshot, command.Target.Binding, command.Target.Policy)) {
		return cleanuport.DispatchResult{Code: domaincleanup.CodeBindingChanged}, &cleanuport.DispatchError{Failure: cleanuport.FailureBeforeHandoff}
	}
	observed := adapter.observeWorktree(ctx, command.Target)
	if observed.FactSHA256 != command.ExpectedObservation.FactSHA256 {
		return cleanuport.DispatchResult{Code: domaincleanup.CodeWorktreeChanged}, &cleanuport.DispatchError{Failure: cleanuport.FailureBeforeHandoff}
	}
	if command.Snapshot != nil && !verifyPrivateArtifact(command.Target, *command.Snapshot) {
		return cleanuport.DispatchResult{Code: domaincleanup.CodeSnapshotUnverified}, &cleanuport.DispatchError{Failure: cleanuport.FailureBeforeHandoff}
	}
	if command.Snapshot != nil && !adapter.verifyStoredSnapshot(ctx, command.Target, *command.Snapshot) {
		return cleanuport.DispatchResult{Code: domaincleanup.CodeSnapshotUnverified}, &cleanuport.DispatchError{Failure: cleanuport.FailureBeforeHandoff}
	}
	result := runCleanupGit(ctx, command.Target.Repository.SourcePath, nil, "worktree", "remove", "--force", "--", command.Target.Repository.WorktreePath)
	if !result.started {
		return cleanuport.DispatchResult{Code: domaincleanup.CodeExternalUnavailable}, &cleanuport.DispatchError{Failure: cleanuport.FailureBeforeHandoff}
	}
	if !result.ok || result.code != 0 {
		return cleanuport.DispatchResult{Handoff: true, Code: domaincleanup.CodeResponseUnknown}, &cleanuport.DispatchError{Failure: cleanuport.FailurePossibleHandoff}
	}
	return cleanuport.DispatchResult{Handoff: true, Code: domaincleanup.CodeOK}, nil
}

func (adapter *CleanupAdapter) DeleteRemoteRef(ctx context.Context, command cleanuport.DispatchCommand) (cleanuport.DispatchResult, error) {
	ctx, cancel := context.WithTimeout(ctx, time.Duration(command.Target.Policy.PhaseMillis)*time.Millisecond)
	defer cancel()
	if !validateCleanupDispatch(command, domaincleanup.ResourceRemoteRef) || command.Target.Binding.IntegrationKind == "" {
		return cleanuport.DispatchResult{Code: domaincleanup.CodeBindingChanged}, &cleanuport.DispatchError{Failure: cleanuport.FailureBeforeHandoff}
	}
	if command.Snapshot != nil && !adapter.verifyStoredSnapshot(ctx, command.Target, *command.Snapshot) {
		return cleanuport.DispatchResult{Code: domaincleanup.CodeSnapshotUnverified}, &cleanuport.DispatchError{Failure: cleanuport.FailureBeforeHandoff}
	}
	observed := adapter.observeRef(ctx, command.Target, true)
	if observed.FactSHA256 != command.ExpectedObservation.FactSHA256 || observed.CurrentOID != command.Target.Binding.CandidateSHA {
		return cleanuport.DispatchResult{Code: domaincleanup.CodeRefChanged}, &cleanuport.DispatchError{Failure: cleanuport.FailureBeforeHandoff}
	}
	ref := "refs/heads/" + command.Target.Binding.Branch
	lease := "--force-with-lease=" + ref + ":" + command.Target.Binding.CandidateSHA
	result := runCleanupGit(ctx, command.Target.Repository.SourcePath, nil, "push", "--porcelain", lease, adapter.cleanupRemote(command.Target), ":"+ref)
	if !result.started {
		return cleanuport.DispatchResult{Code: domaincleanup.CodeExternalUnavailable}, &cleanuport.DispatchError{Failure: cleanuport.FailureBeforeHandoff}
	}
	if adapter.afterCleanupRemoteDelete != nil {
		adapter.afterCleanupRemoteDelete()
	}
	if !result.ok || result.code != 0 {
		return cleanuport.DispatchResult{Handoff: true, Code: domaincleanup.CodeResponseUnknown}, &cleanuport.DispatchError{Failure: cleanuport.FailurePossibleHandoff}
	}
	return cleanuport.DispatchResult{Handoff: true, Code: domaincleanup.CodeOK}, nil
}

func (adapter *CleanupAdapter) DeleteLocalRef(ctx context.Context, command cleanuport.DispatchCommand) (cleanuport.DispatchResult, error) {
	ctx, cancel := context.WithTimeout(ctx, time.Duration(command.Target.Policy.PhaseMillis)*time.Millisecond)
	defer cancel()
	if !validateCleanupDispatch(command, domaincleanup.ResourceLocalRef) ||
		(command.Target.Binding.IntegrationKind == "" && command.Snapshot == nil) {
		return cleanuport.DispatchResult{Code: domaincleanup.CodeBindingChanged}, &cleanuport.DispatchError{Failure: cleanuport.FailureBeforeHandoff}
	}
	if command.Snapshot != nil && !adapter.verifyStoredSnapshot(ctx, command.Target, *command.Snapshot) {
		return cleanuport.DispatchResult{Code: domaincleanup.CodeSnapshotUnverified}, &cleanuport.DispatchError{Failure: cleanuport.FailureBeforeHandoff}
	}
	observed := adapter.observeRef(ctx, command.Target, false)
	if observed.FactSHA256 != command.ExpectedObservation.FactSHA256 || observed.CurrentOID != command.Target.Binding.CandidateSHA {
		return cleanuport.DispatchResult{Code: domaincleanup.CodeRefChanged}, &cleanuport.DispatchError{Failure: cleanuport.FailureBeforeHandoff}
	}
	ref := "refs/heads/" + command.Target.Binding.Branch
	result := runCleanupGit(ctx, command.Target.Repository.SourcePath, nil, "update-ref", "-d", ref, command.Target.Binding.CandidateSHA)
	if !result.started {
		return cleanuport.DispatchResult{Code: domaincleanup.CodeExternalUnavailable}, &cleanuport.DispatchError{Failure: cleanuport.FailureBeforeHandoff}
	}
	if !result.ok || result.code != 0 {
		return cleanuport.DispatchResult{Handoff: true, Code: domaincleanup.CodeResponseUnknown}, &cleanuport.DispatchError{Failure: cleanuport.FailurePossibleHandoff}
	}
	return cleanuport.DispatchResult{Handoff: true, Code: domaincleanup.CodeOK}, nil
}

func (adapter *CleanupAdapter) ExpirePrivateArtifact(ctx context.Context, command cleanuport.DispatchCommand) (cleanuport.DispatchResult, error) {
	ctx, cancel := context.WithTimeout(ctx, time.Duration(command.Target.Policy.PhaseMillis)*time.Millisecond)
	defer cancel()
	if !validateCleanupDispatch(command, domaincleanup.ResourcePrivateArtifact) || command.Target.Snapshot == nil ||
		!domaincleanup.ValidSnapshot(*command.Target.Snapshot, command.Target.Binding, command.Target.Policy) ||
		command.Target.TaskStoreNowMillis < command.Target.Snapshot.RetentionUntilMillis {
		return cleanuport.DispatchResult{Code: domaincleanup.CodeBindingChanged}, &cleanuport.DispatchError{Failure: cleanuport.FailureBeforeHandoff}
	}
	observed := adapter.observePrivateArtifact(command.Target)
	if observed.FactSHA256 != command.ExpectedObservation.FactSHA256 {
		return cleanuport.DispatchResult{Code: domaincleanup.CodeOwnerMismatch}, &cleanuport.DispatchError{Failure: cleanuport.FailureBeforeHandoff}
	}
	if !adapter.verifyStoredSnapshot(ctx, command.Target, *command.Target.Snapshot) {
		return cleanuport.DispatchResult{Code: domaincleanup.CodeSnapshotUnverified}, &cleanuport.DispatchError{Failure: cleanuport.FailureBeforeHandoff}
	}
	root, ok := safeArtifactRoot(command.Target.PrivateArtifactRoot, command.Target.Repository.WorktreePath)
	if !ok {
		return cleanuport.DispatchResult{Code: domaincleanup.CodeOwnerMismatch}, &cleanuport.DispatchError{Failure: cleanuport.FailureBeforeHandoff}
	}
	path := artifactDirectory(command.Target)
	info, err := os.Lstat(path)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return cleanuport.DispatchResult{Code: domaincleanup.CodeOwnerMismatch}, &cleanuport.DispatchError{Failure: cleanuport.FailureBeforeHandoff}
	}
	quarantine := path + ".expired"
	if err := os.Rename(path, quarantine); err != nil {
		return cleanuport.DispatchResult{Handoff: true, Code: domaincleanup.CodeResponseUnknown}, &cleanuport.DispatchError{Failure: cleanuport.FailurePossibleHandoff}
	}
	quarantined, err := os.Lstat(quarantine)
	if err != nil || !os.SameFile(info, quarantined) || !quarantined.IsDir() || quarantined.Mode()&os.ModeSymlink != 0 || filepath.Dir(quarantine) != root {
		return cleanuport.DispatchResult{Handoff: true, Code: domaincleanup.CodeResponseUnknown}, &cleanuport.DispatchError{Failure: cleanuport.FailurePossibleHandoff}
	}
	if err := os.RemoveAll(quarantine); err != nil {
		return cleanuport.DispatchResult{Handoff: true, Code: domaincleanup.CodeResponseUnknown}, &cleanuport.DispatchError{Failure: cleanuport.FailurePossibleHandoff}
	}
	parent, err := os.Open(root)
	if err != nil {
		return cleanuport.DispatchResult{Handoff: true, Code: domaincleanup.CodeResponseUnknown}, &cleanuport.DispatchError{Failure: cleanuport.FailurePossibleHandoff}
	}
	err = parent.Sync()
	_ = parent.Close()
	if err != nil {
		return cleanuport.DispatchResult{Handoff: true, Code: domaincleanup.CodeResponseUnknown}, &cleanuport.DispatchError{Failure: cleanuport.FailurePossibleHandoff}
	}
	return cleanuport.DispatchResult{Handoff: true, Code: domaincleanup.CodeOK}, nil
}

func (adapter *CleanupAdapter) ExpireRecoveryRefs(ctx context.Context, command cleanuport.DispatchCommand) (cleanuport.DispatchResult, error) {
	ctx, cancel := context.WithTimeout(ctx, time.Duration(command.Target.Policy.PhaseMillis)*time.Millisecond)
	defer cancel()
	if !validateCleanupDispatch(command, domaincleanup.ResourceRecoveryRef) || command.Target.Snapshot == nil ||
		!domaincleanup.ValidSnapshot(*command.Target.Snapshot, command.Target.Binding, command.Target.Policy) ||
		command.Target.TaskStoreNowMillis < command.Target.Snapshot.RetentionUntilMillis {
		return cleanuport.DispatchResult{Code: domaincleanup.CodeBindingChanged}, &cleanuport.DispatchError{Failure: cleanuport.FailureBeforeHandoff}
	}
	observed := adapter.observeRecoveryRefs(ctx, command.Target)
	if observed.FactSHA256 != command.ExpectedObservation.FactSHA256 {
		return cleanuport.DispatchResult{Code: domaincleanup.CodeRefChanged}, &cleanuport.DispatchError{Failure: cleanuport.FailureBeforeHandoff}
	}
	input := bytes.NewBufferString("start\n")
	fmt.Fprintf(input, "delete %s %s\n", command.Target.Snapshot.WorktreeRef, command.Target.Snapshot.WorktreeCommitSHA)
	if command.Target.Snapshot.IndexRef != "" {
		fmt.Fprintf(input, "delete %s %s\n", command.Target.Snapshot.IndexRef, command.Target.Snapshot.IndexCommitSHA)
	}
	input.WriteString("prepare\ncommit\n")
	result := runCleanupGitInput(ctx, command.Target.Repository.SourcePath, input.Bytes(), "update-ref", "--stdin")
	if !result.started {
		return cleanuport.DispatchResult{Code: domaincleanup.CodeExternalUnavailable}, &cleanuport.DispatchError{Failure: cleanuport.FailureBeforeHandoff}
	}
	if !result.ok || result.code != 0 {
		return cleanuport.DispatchResult{Handoff: true, Code: domaincleanup.CodeResponseUnknown}, &cleanuport.DispatchError{Failure: cleanuport.FailurePossibleHandoff}
	}
	return cleanuport.DispatchResult{Handoff: true, Code: domaincleanup.CodeOK}, nil
}

// RetentionExpired reports the exact seven-day policy boundary without using
// wall-clock time inside the adapter.
func RetentionExpired(snapshot domaincleanup.SnapshotEvidence, nowMillis int64) bool {
	return nowMillis >= snapshot.RetentionUntilMillis
}
