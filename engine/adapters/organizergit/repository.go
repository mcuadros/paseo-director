// SPDX-License-Identifier: Apache-2.0

// Package organizergit implements the engine-owned Organizer repository port
// with direct-argv Git and atomic Linux filesystem operations.
package organizergit

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"syscall"
	"unicode"

	repositorydomain "github.com/mcuadros/director-engine/domain/repository"
	repoport "github.com/mcuadros/director-engine/ports/organizer"
)

const maximumGitOutputBytes = 64 * 1024

type boundedOutput struct {
	mu       sync.Mutex
	buffer   bytes.Buffer
	maximum  int
	exceeded bool
}

func newBoundedOutput(maximum int) *boundedOutput {
	return &boundedOutput{maximum: maximum}
}

func (output *boundedOutput) Write(value []byte) (int, error) {
	output.mu.Lock()
	defer output.mu.Unlock()
	if output.exceeded {
		return 0, repoport.ErrGitOutputLimit
	}
	remaining := output.maximum - output.buffer.Len()
	if len(value) <= remaining {
		return output.buffer.Write(value)
	}
	written := 0
	if remaining > 0 {
		written, _ = output.buffer.Write(value[:remaining])
	}
	output.exceeded = true
	return written, repoport.ErrGitOutputLimit
}

func (output *boundedOutput) Bytes() []byte {
	output.mu.Lock()
	defer output.mu.Unlock()
	return bytes.Clone(output.buffer.Bytes())
}

func (output *boundedOutput) Len() int {
	output.mu.Lock()
	defer output.mu.Unlock()
	return output.buffer.Len()
}

func (output *boundedOutput) Exceeded() bool {
	output.mu.Lock()
	defer output.mu.Unlock()
	return output.exceeded
}

// Adapter performs exact Organizer Git/filesystem observations and effects.
type Adapter struct{}

var _ repoport.Repository = (*Adapter)(nil)

// New creates the policy-free Organizer Git adapter.
func New() *Adapter { return &Adapter{} }

func verifyControlledDirectory(path string, private bool) error {
	info, err := os.Lstat(path)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return repoport.ErrInvalidPath
	}
	status, ok := info.Sys().(*syscall.Stat_t)
	if !ok || int(status.Uid) != os.Geteuid() || info.Mode().Perm()&0o022 != 0 ||
		info.Mode().Perm()&0o300 != 0o300 {
		return repoport.ErrInvalidPath
	}
	if private && info.Mode().Perm()&0o077 != 0 {
		return repoport.ErrInvalidPath
	}
	resolved, err := filepath.EvalSymlinks(path)
	if err != nil || resolved != path {
		return repoport.ErrInvalidPath
	}
	return nil
}

func controlledDirectory(path string) error {
	return verifyControlledDirectory(path, false)
}

func privateDirectory(path string) error {
	return verifyControlledDirectory(path, true)
}

func privateCreateParent(path string) error {
	if path == "" || !filepath.IsAbs(path) || filepath.Clean(path) != path {
		return repoport.ErrInvalidPath
	}
	return controlledDirectory(path)
}

func exactRoot(path string, mustExist bool) (string, error) {
	if path == "" || !filepath.IsAbs(path) || filepath.Clean(path) != path {
		return "", repoport.ErrInvalidPath
	}
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) && !mustExist {
		if err := privateCreateParent(filepath.Dir(path)); err != nil {
			return "", repoport.ErrInvalidPath
		}
		return path, nil
	}
	if errors.Is(err, os.ErrNotExist) {
		return "", repoport.ErrTargetMissing
	}
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return "", repoport.ErrInvalidPath
	}
	resolved, err := filepath.EvalSymlinks(path)
	if err != nil || resolved != path {
		return "", repoport.ErrInvalidPath
	}
	return path, nil
}

func regularFile(path string) ([]byte, fs.FileMode, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return nil, 0, err
	}
	if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
		return nil, 0, repoport.ErrTargetConflict
	}
	content, err := os.ReadFile(path)
	return content, info.Mode().Perm(), err
}

func syncDirectory(path string) error {
	directory, err := os.Open(path)
	if err != nil {
		return err
	}
	defer directory.Close()
	return directory.Sync()
}

func writeExactFile(root, relative string, content []byte) error {
	if relative == "" || filepath.IsAbs(relative) || filepath.Clean(relative) != relative ||
		relative == "." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return repoport.ErrInvalidPath
	}
	target := filepath.Join(root, relative)
	if target != root+string(filepath.Separator)+relative {
		return repoport.ErrInvalidPath
	}
	parent := filepath.Dir(target)
	current := root
	parentRelative, err := filepath.Rel(root, parent)
	if err != nil {
		return repoport.ErrInvalidPath
	}
	if parentRelative != "." {
		for _, part := range strings.Split(parentRelative, string(filepath.Separator)) {
			current = filepath.Join(current, part)
			if err := os.Mkdir(current, 0o700); err != nil && !errors.Is(err, os.ErrExist) {
				return err
			}
			info, err := os.Lstat(current)
			if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
				return repoport.ErrInvalidPath
			}
		}
	}
	resolvedParent, err := filepath.EvalSymlinks(parent)
	if err != nil || resolvedParent != parent {
		return repoport.ErrInvalidPath
	}
	existing, mode, err := regularFile(target)
	if err == nil {
		if mode != 0o600 || !bytes.Equal(existing, content) {
			return repoport.ErrTargetConflict
		}
		return nil
	}
	if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	digest := sha256.Sum256(content)
	temporary := filepath.Join(parent, "."+filepath.Base(target)+".director-"+hex.EncodeToString(digest[:8])+".tmp")
	file, err := os.OpenFile(temporary, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if errors.Is(err, os.ErrExist) {
		staged, stagedMode, readErr := regularFile(temporary)
		if readErr != nil || stagedMode != 0o600 || !bytes.Equal(staged, content) {
			return repoport.ErrTargetConflict
		}
	} else if err != nil {
		return err
	} else {
		if _, err = file.Write(content); err == nil {
			err = file.Sync()
		}
		closeErr := file.Close()
		if err == nil {
			err = closeErr
		}
		if err != nil {
			_ = os.Remove(temporary)
			return err
		}
	}
	if err := os.Link(temporary, target); err != nil {
		if !errors.Is(err, os.ErrExist) {
			return err
		}
		existing, mode, readErr := regularFile(target)
		if readErr != nil || mode != 0o600 || !bytes.Equal(existing, content) {
			return repoport.ErrTargetConflict
		}
	}
	if err := os.Remove(temporary); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return syncDirectory(parent)
}

// CreateTargetAbsent observes only an exact canonical absent target.
func (*Adapter) CreateTargetAbsent(_ context.Context, path string) (bool, error) {
	if _, err := exactRoot(path, false); err != nil {
		return false, err
	}
	_, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return true, nil
	}
	if err != nil {
		return false, err
	}
	return false, nil
}

func verifyMarker(path string, marker []byte) error {
	if err := privateCreateParent(filepath.Dir(path)); err != nil {
		return err
	}
	if err := privateDirectory(path); err != nil {
		return err
	}
	metadataDirectory := filepath.Join(path, ".director")
	if err := privateDirectory(metadataDirectory); err != nil {
		return repoport.ErrTargetConflict
	}
	existing, mode, err := regularFile(filepath.Join(metadataDirectory, "organizer.json"))
	if err != nil || mode != 0o600 || !bytes.Equal(existing, marker) {
		return repoport.ErrTargetConflict
	}
	return nil
}

func verifyInitialRoot(path string, marker []byte) error {
	if err := verifyMarker(path, marker); err != nil {
		return err
	}
	rootEntries, err := os.ReadDir(path)
	if err != nil || len(rootEntries) != 1 || rootEntries[0].Name() != ".director" || !rootEntries[0].IsDir() {
		return repoport.ErrTargetConflict
	}
	metadataEntries, err := os.ReadDir(filepath.Join(path, ".director"))
	if err != nil || len(metadataEntries) != 1 || metadataEntries[0].Name() != "organizer.json" ||
		!metadataEntries[0].Type().IsRegular() {
		return repoport.ErrTargetConflict
	}
	return nil
}

func ownedCreateRoot(path string) (string, error) {
	root, err := exactRoot(path, true)
	if err != nil {
		return "", err
	}
	if err := privateCreateParent(filepath.Dir(root)); err != nil {
		return "", err
	}
	if err := privateDirectory(root); err != nil {
		return "", err
	}
	return root, nil
}

// EnsureRoot creates or adopts the exact owned root and ownership marker.
func (*Adapter) EnsureRoot(_ context.Context, spec repoport.RootSpec) error {
	if err := privateCreateParent(filepath.Dir(spec.Path)); err != nil {
		return err
	}
	path, err := exactRoot(spec.Path, false)
	if err != nil {
		return err
	}
	created := false
	if err := os.Mkdir(path, 0o700); err != nil {
		if !errors.Is(err, os.ErrExist) {
			return err
		}
	} else {
		created = true
	}
	cleanupCreated := func() {
		_ = os.Remove(filepath.Join(path, ".director", "organizer.json"))
		_ = os.Remove(filepath.Join(path, ".director"))
		_ = os.Remove(path)
	}
	if _, err := exactRoot(path, true); err != nil {
		if created {
			cleanupCreated()
		}
		return err
	}
	metadataDirectory := filepath.Join(path, ".director")
	if !created {
		return verifyInitialRoot(path, spec.Marker)
	}
	if err := os.Mkdir(metadataDirectory, 0o700); err != nil && !errors.Is(err, os.ErrExist) {
		if created {
			cleanupCreated()
		}
		return err
	}
	metadataInfo, err := os.Lstat(metadataDirectory)
	if err != nil || !metadataInfo.IsDir() || metadataInfo.Mode()&os.ModeSymlink != 0 {
		if created {
			cleanupCreated()
		}
		return repoport.ErrTargetConflict
	}
	if err := writeExactFile(path, filepath.Join(".director", "organizer.json"), spec.Marker); err != nil {
		if created {
			cleanupCreated()
		}
		return err
	}
	return syncDirectory(path)
}

// VerifyMarker verifies the exact integrity/correlation marker on an already
// admitted Create root without treating the committed Request ID as a secret.
func (*Adapter) VerifyMarker(_ context.Context, spec repoport.RootSpec) error {
	path, err := exactRoot(spec.Path, true)
	if err != nil {
		return err
	}
	return verifyMarker(path, spec.Marker)
}

// EnsureFile atomically creates or adopts one exact regular Organizer file.
func (*Adapter) EnsureFile(_ context.Context, root, relative string, content []byte) error {
	path, err := ownedCreateRoot(root)
	if err != nil {
		return err
	}
	return writeExactFile(path, relative, content)
}

func baseGitArguments() []string {
	return []string{
		"-c", "core.hooksPath=/dev/null",
		"-c", "commit.gpgsign=false",
		"-c", "core.fsmonitor=false",
		"-c", "core.quotePath=false",
		"-c", "diff.external=/bin/false",
		"-c", "protocol.ext.allow=never",
		"-c", "credential.helper=",
	}
}

func runGitCommandBytes(ctx context.Context, directory string, configuration, arguments []string) ([]byte, error) {
	commandArguments := append(slices.Clone(configuration), arguments...)
	command := exec.CommandContext(ctx, "git", commandArguments...)
	command.Dir = directory
	command.Env = append(os.Environ(),
		"GIT_CONFIG_NOSYSTEM=1",
		"GIT_CONFIG_GLOBAL=/dev/null",
		"GIT_OPTIONAL_LOCKS=0",
		"GIT_ATTR_NOSYSTEM=1",
		"GIT_EXTERNAL_DIFF=/bin/false",
		"GIT_SSH_COMMAND=/bin/false",
		"GIT_AUTHOR_NAME=Director Engine",
		"GIT_AUTHOR_EMAIL=director@example.invalid",
		"GIT_COMMITTER_NAME=Director Engine",
		"GIT_COMMITTER_EMAIL=director@example.invalid",
		"GIT_AUTHOR_DATE=2000-01-01T00:00:00Z",
		"GIT_COMMITTER_DATE=2000-01-01T00:00:00Z",
	)
	output := newBoundedOutput(maximumGitOutputBytes)
	command.Stdout = output
	command.Stderr = output
	err := command.Run()
	if output.Exceeded() {
		return nil, repoport.ErrGitOutputLimit
	}
	return output.Bytes(), err
}

func safeConfigEnumerationError(err error) error {
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) ||
		errors.Is(err, repoport.ErrGitOutputLimit) {
		return err
	}
	return repoport.ErrTargetConflict
}

func scopedConfigKeys(ctx context.Context, directory, scope string) ([]string, error) {
	output, err := runGitCommandBytes(ctx, directory, baseGitArguments(), []string{
		"config", "--no-includes", scope, "--null", "--name-only", "--list",
	})
	if err != nil {
		return nil, safeConfigEnumerationError(err)
	}
	keys, err := nulRecords(output)
	if err != nil {
		return nil, repoport.ErrTargetConflict
	}
	return keys, nil
}

func worktreeConfigEnabled(ctx context.Context, directory string) (bool, error) {
	output, err := runGitCommandBytes(ctx, directory, baseGitArguments(), []string{
		"config", "--no-includes", "--local", "--null", "--type=bool", "--get-all",
		"extensions.worktreeConfig",
	})
	if err != nil {
		var exitError *exec.ExitError
		if errors.As(err, &exitError) && exitError.ExitCode() == 1 && len(output) == 0 {
			return false, nil
		}
		return false, safeConfigEnumerationError(err)
	}
	values, err := nulRecords(output)
	if err != nil || len(values) != 1 {
		return false, repoport.ErrTargetConflict
	}
	switch values[0] {
	case "true":
		return true, nil
	case "false":
		return false, nil
	default:
		return false, repoport.ErrTargetConflict
	}
}

func executableConfigDriver(key, section string, suffixes []string) (string, bool, error) {
	lower := strings.ToLower(key)
	prefix := section + "."
	if !strings.HasPrefix(lower, prefix) {
		return "", false, nil
	}
	for _, suffix := range suffixes {
		suffix = "." + suffix
		if !strings.HasSuffix(lower, suffix) {
			continue
		}
		driver := key[len(prefix) : len(key)-len(suffix)]
		if driver == "" || strings.ContainsRune(driver, '=') ||
			strings.IndexFunc(driver, unicode.IsControl) >= 0 {
			return "", true, repoport.ErrTargetConflict
		}
		return driver, true, nil
	}
	return "", false, nil
}

func executionOverridesForConfigKeys(keys []string) ([]string, error) {
	filterDrivers := map[string]struct{}{}
	diffDrivers := map[string]struct{}{}
	for _, key := range keys {
		lower := strings.ToLower(key)
		if lower == "include.path" ||
			strings.HasPrefix(lower, "includeif.") && strings.HasSuffix(lower, ".path") {
			return nil, repoport.ErrTargetConflict
		}
		filterDriver, matched, err := executableConfigDriver(
			key, "filter", []string{"clean", "smudge", "process", "required"},
		)
		if err != nil {
			return nil, err
		}
		if matched {
			filterDrivers[filterDriver] = struct{}{}
		}
		diffDriver, matched, err := executableConfigDriver(key, "diff", []string{"command", "textconv"})
		if err != nil {
			return nil, err
		}
		if matched {
			diffDrivers[diffDriver] = struct{}{}
		}
	}
	drivers := make([]string, 0, len(filterDrivers))
	for driver := range filterDrivers {
		drivers = append(drivers, driver)
	}
	slices.Sort(drivers)
	overrides := make([]string, 0, len(drivers)*8+len(diffDrivers)*4)
	for _, driver := range drivers {
		overrides = append(overrides,
			"-c", "filter."+driver+".process=",
			"-c", "filter."+driver+".clean=/bin/cat",
			"-c", "filter."+driver+".smudge=/bin/cat",
			"-c", "filter."+driver+".required=false",
		)
	}
	diffKeys := make([]string, 0, len(diffDrivers))
	for driver := range diffDrivers {
		diffKeys = append(diffKeys, driver)
	}
	slices.Sort(diffKeys)
	for _, driver := range diffKeys {
		overrides = append(overrides,
			"-c", "diff."+driver+".command=/bin/false",
			"-c", "diff."+driver+".textconv=/bin/false",
		)
	}
	return overrides, nil
}

func localExecutionOverrides(ctx context.Context, directory string) ([]string, error) {
	if _, err := os.Lstat(filepath.Join(directory, ".git")); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return []string{}, nil
		}
		return nil, err
	}
	keys, err := scopedConfigKeys(ctx, directory, "--local")
	if err != nil {
		return nil, err
	}
	if _, err := executionOverridesForConfigKeys(keys); err != nil {
		return nil, err
	}
	worktreeEnabled, err := worktreeConfigEnabled(ctx, directory)
	if err != nil {
		return nil, err
	}
	if worktreeEnabled {
		worktreeKeys, err := scopedConfigKeys(ctx, directory, "--worktree")
		if err != nil {
			return nil, err
		}
		keys = append(keys, worktreeKeys...)
	}
	return executionOverridesForConfigKeys(keys)
}

func gitCommandBytes(ctx context.Context, directory string, arguments ...string) ([]byte, error) {
	configuration := baseGitArguments()
	overrides, err := localExecutionOverrides(ctx, directory)
	if err != nil {
		return nil, err
	}
	configuration = append(configuration, overrides...)
	return runGitCommandBytes(ctx, directory, configuration, arguments)
}

func gitCommand(ctx context.Context, directory string, arguments ...string) (string, error) {
	output, err := gitCommandBytes(ctx, directory, arguments...)
	if err != nil && errors.Is(err, repoport.ErrGitOutputLimit) {
		return "", err
	}
	return strings.TrimSpace(string(output)), err
}

func gitRoot(ctx context.Context, path string) error {
	root, err := gitCommand(ctx, path, "rev-parse", "--show-toplevel")
	if err != nil || root != filepath.ToSlash(path) && root != path {
		return repoport.ErrNotGitRepository
	}
	return nil
}

// EnsureInitialized creates or adopts an exact Git repository at root.
func (*Adapter) EnsureInitialized(ctx context.Context, root string) error {
	path, err := ownedCreateRoot(root)
	if err != nil {
		return err
	}
	if _, err := os.Lstat(filepath.Join(path, ".git")); errors.Is(err, os.ErrNotExist) {
		if _, err := gitCommand(ctx, path, "init", "--template=", "--initial-branch=main"); err != nil {
			return fmt.Errorf("initialize Organizer Git repository: %w", err)
		}
	} else if err != nil {
		return err
	}
	return gitRoot(ctx, path)
}

func trackedFiles(ctx context.Context, root string) ([]string, error) {
	output, err := gitCommandBytes(ctx, root, "ls-files", "-z")
	if err != nil {
		return nil, err
	}
	files, err := nulRecords(output)
	if err != nil {
		return nil, err
	}
	slices.Sort(files)
	return files, nil
}

func nulRecords(output []byte) ([]string, error) {
	if len(output) == 0 {
		return []string{}, nil
	}
	if output[len(output)-1] != 0 {
		return nil, repoport.ErrTargetConflict
	}
	rawRecords := bytes.Split(output[:len(output)-1], []byte{0})
	records := make([]string, 0, len(rawRecords))
	for _, record := range rawRecords {
		if len(record) == 0 {
			return nil, repoport.ErrTargetConflict
		}
		records = append(records, string(record))
	}
	return records, nil
}

func porcelainPaths(output []byte) ([]string, error) {
	records, err := nulRecords(output)
	if err != nil {
		return nil, err
	}
	paths := make([]string, 0, len(records))
	for _, record := range records {
		if len(record) < 4 || record[2] != ' ' || record[0] == 'R' || record[0] == 'C' ||
			record[1] == 'R' || record[1] == 'C' {
			return nil, repoport.ErrTargetConflict
		}
		paths = append(paths, record[3:])
	}
	return paths, nil
}

func validRevision(value string) bool {
	if len(value) != 40 && len(value) != 64 {
		return false
	}
	for _, character := range value {
		if !strings.ContainsRune("0123456789abcdef", character) {
			return false
		}
	}
	return true
}

func verifyCommitted(ctx context.Context, root string, expected []string) (string, error) {
	status, err := gitCommandBytes(ctx, root, "status", "--porcelain=v1", "-z", "--untracked-files=all")
	if err != nil {
		return "", err
	}
	if len(status) != 0 {
		return "", repoport.ErrRepositoryDirty
	}
	actual, err := trackedFiles(ctx, root)
	if err != nil {
		return "", err
	}
	want := slices.Clone(expected)
	slices.Sort(want)
	if !slices.Equal(actual, want) {
		return "", repoport.ErrTargetConflict
	}
	revision, err := gitCommand(ctx, root, "rev-parse", "HEAD")
	if err != nil || !validRevision(revision) {
		return "", repoport.ErrRevisionMissing
	}
	return revision, nil
}

func verifyPendingCommit(ctx context.Context, root string, expected []string) error {
	want := make(map[string]struct{}, len(expected))
	for _, relative := range expected {
		want[relative] = struct{}{}
		if _, _, err := regularFile(filepath.Join(root, relative)); err != nil {
			return repoport.ErrTargetConflict
		}
	}
	tracked, err := trackedFiles(ctx, root)
	if err != nil {
		return err
	}
	for _, relative := range tracked {
		if _, ok := want[relative]; !ok {
			return repoport.ErrTargetConflict
		}
	}
	status, err := gitCommandBytes(ctx, root, "status", "--porcelain=v1", "-z", "--untracked-files=all")
	if err != nil {
		return err
	}
	paths, err := porcelainPaths(status)
	if err != nil {
		return err
	}
	seen := make(map[string]struct{}, len(expected))
	for _, relative := range paths {
		if _, ok := want[relative]; !ok {
			return repoport.ErrTargetConflict
		}
		seen[relative] = struct{}{}
	}
	if len(seen) != len(want) {
		return repoport.ErrTargetConflict
	}
	return nil
}

func stageWithoutFilters(ctx context.Context, root string, files []string) error {
	for _, relative := range files {
		objectID, err := gitCommand(ctx, root, "hash-object", "-w", "--no-filters", "--", relative)
		if err != nil || !validRevision(objectID) {
			return repoport.ErrTargetConflict
		}
		if _, err := gitCommand(ctx, root, "update-index", "--add", "--cacheinfo", "100644", objectID, relative); err != nil {
			return err
		}
	}
	return nil
}

// EnsureCommit creates or adopts the one exact initial Organizer commit.
func (*Adapter) EnsureCommit(ctx context.Context, root string, files []string, message string) (string, error) {
	path, err := ownedCreateRoot(root)
	if err != nil {
		return "", err
	}
	if err := gitRoot(ctx, path); err != nil {
		return "", err
	}
	if _, err := gitCommand(ctx, path, "rev-parse", "--verify", "HEAD"); err == nil {
		return verifyCommitted(ctx, path, files)
	}
	branch, branchErr := gitCommand(ctx, path, "symbolic-ref", "--quiet", "HEAD")
	if branchErr != nil || branch != "refs/heads/main" {
		return "", repoport.ErrTargetConflict
	}
	if err := verifyPendingCommit(ctx, path, files); err != nil {
		return "", err
	}
	if err := stageWithoutFilters(ctx, path, files); err != nil {
		return "", fmt.Errorf("stage Organizer files: %w", err)
	}
	if _, err := gitCommand(ctx, path, "commit", "--no-verify", "-m", message); err != nil {
		return "", fmt.Errorf("commit Organizer files: %w", err)
	}
	return verifyCommitted(ctx, path, files)
}

// Read observes a clean exact Git root, HEAD, and configuration marker.
func (*Adapter) Read(ctx context.Context, root string) (repoport.Snapshot, error) {
	path, err := exactRoot(root, true)
	if err != nil {
		return repoport.Snapshot{}, err
	}
	if err := gitRoot(ctx, path); err != nil {
		return repoport.Snapshot{}, err
	}
	status, err := gitCommandBytes(ctx, path, "status", "--porcelain=v1", "-z", "--untracked-files=all")
	if err != nil {
		return repoport.Snapshot{}, err
	}
	if len(status) != 0 {
		return repoport.Snapshot{}, repoport.ErrRepositoryDirty
	}
	revision, err := gitCommand(ctx, path, "rev-parse", "HEAD")
	if err != nil || !validRevision(revision) {
		return repoport.Snapshot{}, repoport.ErrRevisionMissing
	}
	configuration, _, err := regularFile(filepath.Join(path, "paseo-director.json"))
	if errors.Is(err, os.ErrNotExist) {
		return repoport.Snapshot{}, repoport.ErrMarkerMissing
	}
	if err != nil {
		return repoport.Snapshot{}, err
	}
	return repoport.Snapshot{
		Path: path, Revision: revision, ConfigurationJSON: configuration,
	}, nil
}

// VerifyFiles observes that every explicit configuration reference is a
// committed regular file inside the exact Organizer root.
func (*Adapter) VerifyFiles(ctx context.Context, root string, relativePaths []string) error {
	path, err := exactRoot(root, true)
	if err != nil {
		return err
	}
	arguments := []string{"ls-files", "--error-unmatch", "--"}
	for _, relative := range relativePaths {
		if relative == "" || filepath.IsAbs(relative) || filepath.Clean(relative) != relative ||
			relative == "." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
			return repoport.ErrInvalidPath
		}
		if _, _, err := regularFile(filepath.Join(path, relative)); err != nil {
			return repoport.ErrReferenceMissing
		}
		arguments = append(arguments, relative)
	}
	if len(relativePaths) == 0 {
		return nil
	}
	if _, err := gitCommand(ctx, path, arguments...); err != nil {
		return repoport.ErrReferenceMissing
	}
	return nil
}

// ResolveWorkspace reads the exact product checkout root, Git common
// directory, and origin and returns their canonical identity.
func (*Adapter) ResolveWorkspace(ctx context.Context, root, expectedRemote string) (repoport.WorkspaceSnapshot, error) {
	path, err := exactRoot(root, true)
	if err != nil {
		return repoport.WorkspaceSnapshot{}, repoport.ErrWorkspaceMismatch
	}
	if err := gitRoot(ctx, path); err != nil {
		return repoport.WorkspaceSnapshot{}, repoport.ErrWorkspaceMismatch
	}
	sourceInfo, err := os.Lstat(path)
	if err != nil {
		return repoport.WorkspaceSnapshot{}, repoport.ErrWorkspaceMismatch
	}
	sourceIdentity, sourceOK := sourceInfo.Sys().(*syscall.Stat_t)
	if !sourceOK || sourceIdentity.Dev == 0 || sourceIdentity.Ino == 0 {
		return repoport.WorkspaceSnapshot{}, repoport.ErrWorkspaceMismatch
	}
	commonDirectory, err := gitCommand(ctx, path, "rev-parse", "--path-format=absolute", "--git-common-dir")
	if err != nil {
		return repoport.WorkspaceSnapshot{}, repoport.ErrWorkspaceMismatch
	}
	commonDirectory = filepath.Clean(filepath.FromSlash(commonDirectory))
	resolvedCommon, err := filepath.EvalSymlinks(commonDirectory)
	relativeCommon, relativeErr := filepath.Rel(path, commonDirectory)
	if err != nil || relativeErr != nil || !filepath.IsAbs(commonDirectory) || resolvedCommon != commonDirectory ||
		relativeCommon == ".." || strings.HasPrefix(relativeCommon, ".."+string(filepath.Separator)) {
		return repoport.WorkspaceSnapshot{}, repoport.ErrWorkspaceMismatch
	}
	commonInfo, err := os.Lstat(commonDirectory)
	if err != nil {
		return repoport.WorkspaceSnapshot{}, repoport.ErrWorkspaceMismatch
	}
	commonIdentity, commonOK := commonInfo.Sys().(*syscall.Stat_t)
	if !commonOK || commonIdentity.Dev == 0 || commonIdentity.Ino == 0 {
		return repoport.WorkspaceSnapshot{}, repoport.ErrWorkspaceMismatch
	}
	remote, err := gitCommand(ctx, path, "remote", "get-url", "origin")
	if err != nil {
		return repoport.WorkspaceSnapshot{}, repoport.ErrWorkspaceMismatch
	}
	expectedIdentity, err := repositorydomain.CanonicalRemote(expectedRemote)
	if err != nil {
		return repoport.WorkspaceSnapshot{}, repoport.ErrWorkspaceMismatch
	}
	observedIdentity, err := repositorydomain.CanonicalRemote(remote)
	if err != nil || observedIdentity.Key != expectedIdentity.Key {
		return repoport.WorkspaceSnapshot{}, repoport.ErrWorkspaceMismatch
	}
	return repoport.WorkspaceSnapshot{
		SourcePath: path, SourceDevice: uint64(sourceIdentity.Dev), SourceInode: sourceIdentity.Ino,
		GitCommonDirectory: commonDirectory,
		GitCommonDevice:    uint64(commonIdentity.Dev), GitCommonInode: commonIdentity.Ino,
		CanonicalRemote: observedIdentity.Canonical, RepositoryKey: observedIdentity.Key,
		RepositoryID: observedIdentity.ID,
	}, nil
}

// VerifyWorkspace preserves the M1 observation surface while delegating to
// the canonical M2 resolver.
func (adapter *Adapter) VerifyWorkspace(ctx context.Context, root, expectedRemote string) error {
	_, err := adapter.ResolveWorkspace(ctx, root, expectedRemote)
	return err
}
