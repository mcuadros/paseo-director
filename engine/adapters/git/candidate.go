// SPDX-License-Identifier: Apache-2.0

// Package git implements the read-only Git Candidate observation port with
// direct argv execution. It returns only bounded normalized facts and owns no
// admission, retry, invalidation, or delivery policy.
package git

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"unicode"
	"unicode/utf8"

	candidatedomain "github.com/mcuadros/director-engine/domain/candidate"
	"github.com/mcuadros/director-engine/domain/execution"
	repositorydomain "github.com/mcuadros/director-engine/domain/repository"
	gitport "github.com/mcuadros/director-engine/ports/git"
)

const (
	maximumCommandBytes = 4 * 1024 * 1024
	maximumTreeEntries  = 25_000
)

// Adapter uses only local read-only Git and filesystem observations.
type Adapter struct {
	afterFirstSnapshot func()
}

var _ gitport.CandidateObserver = (*Adapter)(nil)

// New returns a production observer with no test fault hook.
func New() *Adapter { return &Adapter{} }

type limitedBuffer struct {
	bytes.Buffer
	overflow bool
}

func (buffer *limitedBuffer) Write(value []byte) (int, error) {
	remaining := maximumCommandBytes - buffer.Len()
	if remaining <= 0 {
		buffer.overflow = true
		return len(value), nil
	}
	if len(value) > remaining {
		_, _ = buffer.Buffer.Write(value[:remaining])
		buffer.overflow = true
		return len(value), nil
	}
	return buffer.Buffer.Write(value)
}

type commandResult struct {
	stdout []byte
	code   int
	ok     bool
}

func safeEnvironment() []string {
	environment := []string{
		"LC_ALL=C", "LANG=C", "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=/dev/null",
		"GIT_OPTIONAL_LOCKS=0", "HOME=/nonexistent", "XDG_CONFIG_HOME=/nonexistent",
	}
	if path := os.Getenv("PATH"); path != "" {
		environment = append(environment, "PATH="+path)
	}
	if temporary := os.Getenv("TMPDIR"); temporary != "" && filepath.IsAbs(temporary) {
		environment = append(environment, "TMPDIR="+temporary)
	}
	return environment
}

func runGit(ctx context.Context, directory string, arguments ...string) commandResult {
	global := []string{
		"--no-optional-locks", "--literal-pathspecs", "-c", "core.fsmonitor=false",
		"-c", "core.hooksPath=/dev/null", "-c", "diff.external=", "-C", directory,
	}
	command := exec.CommandContext(ctx, "git", append(global, arguments...)...)
	command.Env = safeEnvironment()
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

func trimmed(result commandResult) (string, bool) {
	if !result.ok || result.code != 0 || !utf8.Valid(result.stdout) {
		return "", false
	}
	return strings.TrimSpace(string(result.stdout)), true
}

func sum(value []byte) string {
	digest := sha256.Sum256(value)
	return hex.EncodeToString(digest[:])
}

func rejected(request gitport.CandidateRequest, code candidatedomain.Code) candidatedomain.Observation {
	return candidatedomain.SealObservation(candidatedomain.Observation{
		ClaimSHA256:             candidatedomain.ClaimSHA256(request.Claim),
		RepositoryBindingSHA256: request.RepositoryBindingSHA256,
		ObservedAtMillis:        request.TaskStoreNowMillis, MaximumAgeMillis: candidatedomain.MaximumObservationAgeMS,
		Code: code,
	})
}

func canonicalPath(path string) (string, fs.FileInfo, bool) {
	if runtime.GOOS != "linux" || len(path) < 2 || len(path) > repositorydomain.MaximumPathBytes ||
		!filepath.IsAbs(path) || filepath.Clean(path) != path || !utf8.ValidString(path) ||
		strings.IndexFunc(path, func(r rune) bool { return unicode.IsSpace(r) || unicode.IsControl(r) || unicode.In(r, unicode.Cf) }) >= 0 {
		return "", nil, false
	}
	real, err := filepath.EvalSymlinks(path)
	if err != nil || real != path {
		return "", nil, false
	}
	info, err := os.Stat(real)
	if err != nil || !info.IsDir() {
		return "", nil, false
	}
	return real, info, true
}

func identity(info fs.FileInfo) (uint64, uint64, bool) {
	value, ok := info.Sys().(*syscall.Stat_t)
	if !ok || value.Dev == 0 || value.Ino == 0 {
		return 0, 0, false
	}
	return uint64(value.Dev), value.Ino, true
}

func safeGitPath(value string) bool {
	if value == "" || !utf8.ValidString(value) || filepath.IsAbs(value) || filepath.Clean(value) != value ||
		value == "." || strings.HasPrefix(value, ".."+string(filepath.Separator)) {
		return false
	}
	return strings.IndexFunc(value, func(r rune) bool {
		return r == 0 || unicode.IsControl(r) || unicode.In(r, unicode.Cf)
	}) < 0
}

type indexEntry struct {
	mode string
	oid  string
}

func parseIndex(output []byte, objectLength int) (map[string]indexEntry, int, candidatedomain.Code) {
	tracked := make(map[string]indexEntry)
	gitlinks := 0
	for _, row := range bytes.Split(output, []byte{0}) {
		if len(row) == 0 {
			continue
		}
		header, pathBytes, found := bytes.Cut(row, []byte{'\t'})
		fields := strings.Fields(string(header))
		path := string(pathBytes)
		if !found || len(fields) != 3 || !safeGitPath(path) || len(fields[1]) != objectLength {
			return nil, 0, candidatedomain.CodeUnsafePath
		}
		stage, err := strconv.Atoi(fields[2])
		if err != nil || stage != 0 {
			return nil, 0, candidatedomain.CodeConflict
		}
		if strings.Trim(fields[1], "0") == "" {
			return nil, 0, candidatedomain.CodeIntentToAdd
		}
		if _, duplicate := tracked[path]; duplicate {
			return nil, 0, candidatedomain.CodeConflict
		}
		switch {
		case fields[0] == "160000":
			gitlinks++
		case fields[0] == "120000":
		case strings.HasPrefix(fields[0], "100"):
		default:
			return nil, 0, candidatedomain.CodeUnsafePath
		}
		tracked[path] = indexEntry{mode: fields[0], oid: fields[1]}
		if len(tracked) > maximumTreeEntries {
			return nil, 0, candidatedomain.CodeGitUnavailable
		}
	}
	return tracked, gitlinks, candidatedomain.CodeOK
}

func standardIndexFlags(output []byte, tracked map[string]indexEntry) bool {
	seen := make(map[string]struct{}, len(tracked))
	for _, row := range bytes.Split(output, []byte{0}) {
		if len(row) == 0 {
			continue
		}
		if len(row) < 3 || row[0] != 'H' || row[1] != ' ' || !safeGitPath(string(row[2:])) {
			return false
		}
		path := string(row[2:])
		if _, exists := tracked[path]; !exists {
			return false
		}
		seen[path] = struct{}{}
	}
	return len(seen) == len(tracked)
}

func statusCode(output []byte) candidatedomain.Code {
	if len(output) == 0 {
		return candidatedomain.CodeOK
	}
	for _, row := range bytes.Split(output, []byte{0}) {
		if len(row) == 0 {
			continue
		}
		switch row[0] {
		case '!':
			return candidatedomain.CodeIgnored
		case '?':
			return candidatedomain.CodeUntracked
		case 'u':
			return candidatedomain.CodeConflict
		}
		if bytes.HasPrefix(row, []byte("1 .A ")) {
			return candidatedomain.CodeIntentToAdd
		}
	}
	return candidatedomain.CodeWorktreeDirty
}

func filesystemExact(root string, tracked map[string]indexEntry) bool {
	ancestors := make(map[string]struct{})
	for path := range tracked {
		for parent := filepath.Dir(path); parent != "."; parent = filepath.Dir(parent) {
			ancestors[parent] = struct{}{}
		}
	}
	entries := 0
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
		entries++
		if entries > maximumTreeEntries || !safeGitPath(relative) {
			return errors.New("unsafe filesystem entry")
		}
		indexed, isTracked := tracked[relative]
		if entry.IsDir() {
			if isTracked && indexed.mode == "160000" {
				return filepath.SkipDir
			}
			if _, required := ancestors[relative]; !required {
				return errors.New("untracked directory")
			}
			return nil
		}
		info, err := entry.Info()
		if err != nil || !isTracked {
			return errors.New("untracked or unreadable entry")
		}
		switch {
		case info.Mode().IsRegular() && strings.HasPrefix(indexed.mode, "100"):
			return nil
		case info.Mode()&os.ModeSymlink != 0 && indexed.mode == "120000":
			return nil
		default:
			return errors.New("unsupported entry type")
		}
	})
	return err == nil
}

func submodulesClean(ctx context.Context, worktree string, expected int) bool {
	result := runGit(ctx, worktree, "submodule", "status", "--recursive")
	if !result.ok || result.code != 0 || !utf8.Valid(result.stdout) {
		return false
	}
	content := bytes.TrimRight(result.stdout, "\r\n")
	lines := bytes.Split(content, []byte{'\n'})
	if len(content) == 0 {
		return expected == 0
	}
	if len(lines) != expected {
		return false
	}
	for _, line := range lines {
		if len(line) == 0 || line[0] != ' ' {
			return false
		}
	}
	return true
}

type registration struct {
	path   string
	head   string
	branch string
}

func registrations(output []byte) ([]registration, bool) {
	var result []registration
	current := registration{}
	for _, field := range bytes.Split(output, []byte{0}) {
		if len(field) == 0 {
			if current.path != "" {
				result = append(result, current)
				current = registration{}
			}
			continue
		}
		value := string(field)
		switch {
		case strings.HasPrefix(value, "worktree "):
			current.path = strings.TrimPrefix(value, "worktree ")
		case strings.HasPrefix(value, "HEAD "):
			current.head = strings.TrimPrefix(value, "HEAD ")
		case strings.HasPrefix(value, "branch "):
			current.branch = strings.TrimPrefix(value, "branch ")
		case value == "bare" || value == "detached" || value == "locked" || value == "prunable" ||
			strings.HasPrefix(value, "locked ") || strings.HasPrefix(value, "prunable "):
		default:
			return nil, false
		}
	}
	if current.path != "" {
		result = append(result, current)
	}
	return result, true
}

type snapshot struct {
	objectFormat, commit, base, parent, tree, branchHead, baseHead                      string
	diffHash, pathsHash, stateHash                                                      string
	mergeCommit                                                                         bool
	sourceDevice, sourceInode, commonDevice, commonInode, worktreeDevice, worktreeInode uint64
}

func (adapter *Adapter) observeSnapshot(ctx context.Context, request gitport.CandidateRequest) (snapshot, candidatedomain.Code) {
	repository := request.Repository
	claim := request.Claim
	if candidatedomain.ClaimSHA256(claim) == "" || execution.RepositoryBindingSHA256(repository) == "" ||
		execution.RepositoryBindingSHA256(repository) != request.RepositoryBindingSHA256 ||
		repository.WorktreePath == repository.SourcePath || claim.WorktreeID == "" ||
		repository.Branch != claim.Branch || repository.BaseSHA != claim.BaseSHA {
		return snapshot{}, candidatedomain.CodeClaimInvalid
	}
	source, sourceInfo, sourceOK := canonicalPath(repository.SourcePath)
	worktree, worktreeInfo, worktreeOK := canonicalPath(repository.WorktreePath)
	common, commonInfo, commonOK := canonicalPath(repository.GitCommonDirectory)
	if !sourceOK || !worktreeOK || !commonOK {
		return snapshot{}, candidatedomain.CodePathAlias
	}
	sourceDevice, sourceInode, sourceIdentity := identity(sourceInfo)
	commonDevice, commonInode, commonIdentity := identity(commonInfo)
	worktreeDevice, worktreeInode, worktreeIdentity := identity(worktreeInfo)
	if !sourceIdentity || !commonIdentity || !worktreeIdentity || sourceDevice != repository.SourceDevice ||
		sourceInode != repository.SourceInode || commonDevice != repository.GitCommonDevice || commonInode != repository.GitCommonInode {
		return snapshot{}, candidatedomain.CodeRepositoryMismatch
	}
	commonFromSource, ok := trimmed(runGit(ctx, source, "rev-parse", "--path-format=absolute", "--git-common-dir"))
	if !ok {
		return snapshot{}, candidatedomain.CodeGitUnavailable
	}
	commonFromSource, _, ok = canonicalPath(commonFromSource)
	if !ok || commonFromSource != common {
		return snapshot{}, candidatedomain.CodeRepositoryMismatch
	}
	commonFromWorktree, ok := trimmed(runGit(ctx, worktree, "rev-parse", "--path-format=absolute", "--git-common-dir"))
	if !ok {
		return snapshot{}, candidatedomain.CodeGitUnavailable
	}
	commonFromWorktree, _, ok = canonicalPath(commonFromWorktree)
	if !ok || commonFromWorktree != common {
		return snapshot{}, candidatedomain.CodeRepositoryMismatch
	}
	remoteValue, ok := trimmed(runGit(ctx, source, "remote", "get-url", "origin"))
	if !ok {
		return snapshot{}, candidatedomain.CodeRemoteMismatch
	}
	remote, err := repositorydomain.CanonicalRemote(remoteValue)
	if err != nil || remote.Canonical != repository.CanonicalRemote || remote.ID != repository.RepositoryID || remote.Key != repository.RepositoryKey {
		return snapshot{}, candidatedomain.CodeRemoteMismatch
	}
	format, ok := trimmed(runGit(ctx, worktree, "rev-parse", "--show-object-format"))
	if !ok || format != "sha1" && format != "sha256" || len(claim.CandidateSHA) != map[string]int{"sha1": 40, "sha256": 64}[format] {
		return snapshot{}, candidatedomain.CodeObjectAmbiguous
	}
	alternatePath, ok := trimmed(runGit(ctx, worktree, "rev-parse", "--path-format=absolute", "--git-path", "objects/info/alternates"))
	if !ok {
		return snapshot{}, candidatedomain.CodeGitUnavailable
	}
	if info, err := os.Stat(alternatePath); err == nil && info.Size() > 0 {
		return snapshot{}, candidatedomain.CodeObjectStoreAmbiguous
	} else if err != nil && !errors.Is(err, os.ErrNotExist) {
		return snapshot{}, candidatedomain.CodeObjectStoreAmbiguous
	}
	objectType, ok := trimmed(runGit(ctx, worktree, "cat-file", "-t", claim.CandidateSHA))
	if !ok || objectType != "commit" {
		return snapshot{}, candidatedomain.CodeObjectMissing
	}
	commit, ok := trimmed(runGit(ctx, worktree, "rev-parse", "--verify", claim.CandidateSHA+"^{commit}"))
	if !ok || commit != claim.CandidateSHA {
		return snapshot{}, candidatedomain.CodeObjectAmbiguous
	}
	base, ok := trimmed(runGit(ctx, worktree, "rev-parse", "--verify", claim.BaseSHA+"^{commit}"))
	if !ok || base != claim.BaseSHA {
		return snapshot{}, candidatedomain.CodeObjectMissing
	}
	branchHead, ok := trimmed(runGit(ctx, worktree, "rev-parse", "--verify", "refs/heads/"+claim.Branch+"^{commit}"))
	if !ok || branchHead != claim.CandidateSHA {
		return snapshot{}, candidatedomain.CodeBranchMoved
	}
	baseHead, ok := trimmed(runGit(ctx, worktree, "rev-parse", "--verify", claim.BaseRef+"^{commit}"))
	if !ok || baseHead != claim.BaseSHA {
		return snapshot{}, candidatedomain.CodeBaseMoved
	}
	symbolicHead, ok := trimmed(runGit(ctx, worktree, "symbolic-ref", "--quiet", "HEAD"))
	if !ok || symbolicHead != "refs/heads/"+claim.Branch {
		return snapshot{}, candidatedomain.CodeBranchMoved
	}
	worktrees := runGit(ctx, source, "worktree", "list", "--porcelain", "-z")
	if !worktrees.ok || worktrees.code != 0 {
		return snapshot{}, candidatedomain.CodeGitUnavailable
	}
	registered, parsed := registrations(worktrees.stdout)
	if !parsed {
		return snapshot{}, candidatedomain.CodeWorktreeRegistration
	}
	matches := 0
	for _, value := range registered {
		real, _, exact := canonicalPath(value.path)
		if exact && real == worktree && value.head == claim.CandidateSHA && value.branch == "refs/heads/"+claim.Branch {
			matches++
		}
	}
	if matches != 1 {
		return snapshot{}, candidatedomain.CodeWorktreeRegistration
	}
	parentsValue, ok := trimmed(runGit(ctx, worktree, "rev-list", "--parents", "-n", "1", claim.CandidateSHA))
	if !ok {
		return snapshot{}, candidatedomain.CodeObjectMissing
	}
	parents := strings.Fields(parentsValue)
	if len(parents) < 2 || parents[0] != claim.CandidateSHA {
		return snapshot{}, candidatedomain.CodeGraphRejected
	}
	parent := parents[1]
	mergeCommit := len(parents) > 2
	ancestor := runGit(ctx, worktree, "merge-base", "--is-ancestor", claim.BaseSHA, claim.CandidateSHA)
	if !ancestor.ok || ancestor.code != 0 {
		return snapshot{}, candidatedomain.CodeGraphRejected
	}
	tree, ok := trimmed(runGit(ctx, worktree, "rev-parse", "--verify", claim.CandidateSHA+"^{tree}"))
	if !ok {
		return snapshot{}, candidatedomain.CodeObjectMissing
	}
	sparse, sparseOK := trimmed(runGit(ctx, worktree, "config", "--bool", "core.sparseCheckout"))
	if sparseOK && sparse == "true" {
		return snapshot{}, candidatedomain.CodeSparseCheckout
	}
	index := runGit(ctx, worktree, "ls-files", "--stage", "-z")
	if !index.ok || index.code != 0 {
		return snapshot{}, candidatedomain.CodeGitUnavailable
	}
	tracked, gitlinks, code := parseIndex(index.stdout, len(claim.CandidateSHA))
	if code != candidatedomain.CodeOK {
		return snapshot{}, code
	}
	flags := runGit(ctx, worktree, "ls-files", "-v", "-z")
	if !flags.ok || flags.code != 0 || !standardIndexFlags(flags.stdout, tracked) {
		return snapshot{}, candidatedomain.CodeSparseCheckout
	}
	status := runGit(ctx, worktree, "status", "--porcelain=v2", "-z", "--untracked-files=all", "--ignored=matching", "--ignore-submodules=none")
	if !status.ok || status.code != 0 {
		return snapshot{}, candidatedomain.CodeGitUnavailable
	}
	if code := statusCode(status.stdout); code != candidatedomain.CodeOK {
		return snapshot{}, code
	}
	indexDiff := runGit(ctx, worktree, "diff-index", "--cached", "--quiet", claim.CandidateSHA, "--")
	if !indexDiff.ok || indexDiff.code != 0 {
		return snapshot{}, candidatedomain.CodeIndexDirty
	}
	worktreeDiff := runGit(ctx, worktree, "diff-files", "--quiet", "--ignore-submodules=none", "--")
	if !worktreeDiff.ok || worktreeDiff.code != 0 {
		return snapshot{}, candidatedomain.CodeWorktreeDirty
	}
	if !submodulesClean(ctx, worktree, gitlinks) {
		return snapshot{}, candidatedomain.CodeSubmodule
	}
	if !filesystemExact(worktree, tracked) {
		return snapshot{}, candidatedomain.CodeUnsafePath
	}
	diff := runGit(ctx, worktree, "diff-tree", "--no-commit-id", "--raw", "-r", "-z", "--full-index", "--no-renames", claim.BaseSHA, claim.CandidateSHA)
	paths := runGit(ctx, worktree, "diff-tree", "--no-commit-id", "--name-only", "-r", "-z", "--no-renames", claim.BaseSHA, claim.CandidateSHA)
	if !diff.ok || diff.code != 0 || !paths.ok || paths.code != 0 {
		return snapshot{}, candidatedomain.CodeGitUnavailable
	}
	for _, path := range bytes.Split(paths.stdout, []byte{0}) {
		if len(path) != 0 && !safeGitPath(string(path)) {
			return snapshot{}, candidatedomain.CodeUnsafePath
		}
	}
	values := []string{format, commit, base, parent, tree, branchHead, baseHead, sum(status.stdout), sum(index.stdout), sum(diff.stdout), sum(paths.stdout),
		strconv.FormatUint(sourceDevice, 10), strconv.FormatUint(sourceInode, 10), strconv.FormatUint(commonDevice, 10), strconv.FormatUint(commonInode, 10),
		strconv.FormatUint(worktreeDevice, 10), strconv.FormatUint(worktreeInode, 10)}
	return snapshot{
		objectFormat: format, commit: commit, base: base, parent: parent, tree: tree,
		branchHead: branchHead, baseHead: baseHead, diffHash: sum(diff.stdout), pathsHash: sum(paths.stdout),
		stateHash: sum([]byte(strings.Join(values, "\x1f"))), sourceDevice: sourceDevice, sourceInode: sourceInode,
		mergeCommit: mergeCommit, commonDevice: commonDevice, commonInode: commonInode, worktreeDevice: worktreeDevice, worktreeInode: worktreeInode,
	}, candidatedomain.CodeOK
}

// ObserveCandidate brackets the complete inspection with a second identical
// snapshot. Any ref, index, worktree, registration, path identity, or object
// fact movement becomes one bounded TOCTOU refusal.
func (adapter *Adapter) ObserveCandidate(ctx context.Context, request gitport.CandidateRequest) (candidatedomain.Observation, error) {
	first, code := adapter.observeSnapshot(ctx, request)
	if code != candidatedomain.CodeOK {
		return rejected(request, code), nil
	}
	if adapter.afterFirstSnapshot != nil {
		adapter.afterFirstSnapshot()
	}
	second, code := adapter.observeSnapshot(ctx, request)
	if code != candidatedomain.CodeOK {
		if code == candidatedomain.CodeBranchMoved || code == candidatedomain.CodeBaseMoved || code == candidatedomain.CodeWorktreeDirty || code == candidatedomain.CodeIndexDirty {
			code = candidatedomain.CodeTOCTOU
		}
		return rejected(request, code), nil
	}
	if first != second {
		return rejected(request, candidatedomain.CodeTOCTOU), nil
	}
	observation := candidatedomain.Observation{
		ClaimSHA256: candidatedomain.ClaimSHA256(request.Claim), RepositoryBindingSHA256: request.RepositoryBindingSHA256,
		ObservedAtMillis: request.TaskStoreNowMillis, MaximumAgeMillis: candidatedomain.MaximumObservationAgeMS,
		ObjectFormat: second.objectFormat, CommitSHA: second.commit, BaseSHA: second.base,
		ParentSHA: second.parent, TreeSHA: second.tree, BranchHeadSHA: second.branchHead, BaseRefHeadSHA: second.baseHead,
		DiffSHA256: second.diffHash, ChangedPathsSHA256: second.pathsHash,
		SourceDevice: second.sourceDevice, SourceInode: second.sourceInode, CommonDevice: second.commonDevice, CommonInode: second.commonInode,
		WorktreeDevice: second.worktreeDevice, WorktreeInode: second.worktreeInode,
		RepositoryExact: true, RemoteExact: true, PathsCanonical: true, RegistrationExact: true,
		ObjectPresent: true, ObjectStoreOwned: true, BranchStable: true, BaseStable: true,
		DescendsFromBase: true, DirectParent: second.parent == second.base && !second.mergeCommit, MergeCommit: second.mergeCommit,
		WorktreeClean: true, IndexClean: true, UntrackedAbsent: true, IgnoredAbsent: true,
		SubmodulesClean: true, ConflictFree: true, IntentToAddAbsent: true, SparseCheckoutAbsent: true,
		FilesystemExact: true, SnapshotSHA256: second.stateHash, Code: candidatedomain.CodeOK,
	}
	return candidatedomain.SealObservation(observation), nil
}
