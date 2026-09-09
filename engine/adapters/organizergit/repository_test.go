// SPDX-License-Identifier: Apache-2.0

package organizergit

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	repoport "github.com/mcuadros/director-engine/ports/organizer"
)

func runTestGit(t *testing.T, directory string, arguments ...string) {
	t.Helper()
	command := exec.Command("git", append([]string{"-c", "core.hooksPath=/dev/null"}, arguments...)...)
	command.Dir = directory
	command.Env = append(os.Environ(), "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=/dev/null")
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v: %s", arguments, err, output)
	}
}

func TestGitCommandBoundsOutputWhileItIsWritten(t *testing.T) {
	writer := newBoundedOutput(32)
	written, err := writer.Write(make([]byte, 33))
	if !errors.Is(err, repoport.ErrGitOutputLimit) {
		t.Fatalf("bounded writer error = %v", err)
	}
	if written != 32 || writer.Len() != 32 || !writer.Exceeded() {
		t.Fatalf("bounded writer = written %d, buffered %d, exceeded %v", written, writer.Len(), writer.Exceeded())
	}

	root := t.TempDir()
	gitPath := root + "/git"
	if err := os.WriteFile(gitPath, []byte("#!/bin/sh\nexec /usr/bin/head -c 131072 /dev/zero\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", root)
	output, err := gitCommand(context.Background(), root, "status")
	if !errors.Is(err, repoport.ErrGitOutputLimit) || err.Error() != repoport.ErrGitOutputLimit.Error() {
		t.Fatalf("gitCommand() error = %T %v", err, err)
	}
	if output != "" {
		t.Fatalf("gitCommand() returned oversized output: %d bytes", len(output))
	}
}

func TestCreateParentRequiresExclusiveWriteControl(t *testing.T) {
	root := t.TempDir()
	if err := os.Chmod(root, 0o700); err != nil {
		t.Fatal(err)
	}
	info, err := os.Lstat(root)
	if err != nil {
		t.Fatal(err)
	}
	if err := controlledDirectory(root); err != nil {
		t.Fatalf("controlledDirectory() rejected test root mode %o: %v", info.Mode().Perm(), err)
	}
	if err := os.Chmod(root, 0o777); err != nil {
		t.Fatal(err)
	}
	if err := controlledDirectory(root); !errors.Is(err, repoport.ErrInvalidPath) {
		t.Fatalf("controlledDirectory() accepted writable foreign access: %v", err)
	}
}

func TestNulDelimitedPathsPreserveRawUnicode(t *testing.T) {
	paths, err := porcelainPaths([]byte("?? skills/naïve/SKILL.md\x00A  templates/résumé.md\x00"))
	if err != nil {
		t.Fatalf("porcelainPaths() error = %v", err)
	}
	if !slices.Equal(paths, []string{"skills/naïve/SKILL.md", "templates/résumé.md"}) {
		t.Fatalf("porcelainPaths() = %#v", paths)
	}
	if _, err := nulRecords([]byte("unterminated")); !errors.Is(err, repoport.ErrTargetConflict) {
		t.Fatalf("nulRecords() truncated error = %v", err)
	}
}

func TestExecutionOverridesCoverLocalAndWorktreeDrivers(t *testing.T) {
	overrides, err := executionOverridesForConfigKeys([]string{
		"filter.local.clean",
		"filter.worktree.smudge",
		"diff.local.command",
		"diff.worktree.textconv",
		"core.fsmonitor",
	})
	if err != nil {
		t.Fatalf("executionOverridesForConfigKeys() error = %v", err)
	}
	want := []string{
		"-c", "filter.local.process=",
		"-c", "filter.local.clean=/bin/cat",
		"-c", "filter.local.smudge=/bin/cat",
		"-c", "filter.local.required=false",
		"-c", "filter.worktree.process=",
		"-c", "filter.worktree.clean=/bin/cat",
		"-c", "filter.worktree.smudge=/bin/cat",
		"-c", "filter.worktree.required=false",
		"-c", "diff.local.command=/bin/false",
		"-c", "diff.local.textconv=/bin/false",
		"-c", "diff.worktree.command=/bin/false",
		"-c", "diff.worktree.textconv=/bin/false",
	}
	if !slices.Equal(overrides, want) {
		t.Fatalf("execution overrides = %#v, want %#v", overrides, want)
	}

	for _, keys := range [][]string{
		{"include.path"},
		{"includeif.gitdir:/tmp/foreign.path"},
		{"filter..clean"},
		{"filter.unsafe=name.clean"},
		{"diff.unsafe\nname.textconv"},
	} {
		if _, err := executionOverridesForConfigKeys(keys); !errors.Is(err, repoport.ErrTargetConflict) {
			t.Fatalf("executionOverridesForConfigKeys(%q) error = %v", keys, err)
		}
	}
}

func TestExecutionConfigEnumerationFailsClosed(t *testing.T) {
	t.Run("external include", func(t *testing.T) {
		root := t.TempDir()
		runTestGit(t, root, "init", "--initial-branch=main")
		includePath := filepath.Join(t.TempDir(), "external.gitconfig")
		if err := os.WriteFile(includePath, []byte("[filter \"included\"]\n\tclean = /bin/false\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		runTestGit(t, root, "config", "--local", "include.path", includePath)
		if _, err := localExecutionOverrides(context.Background(), root); !errors.Is(err, repoport.ErrTargetConflict) {
			t.Fatalf("localExecutionOverrides() external include error = %v", err)
		}
	})

	t.Run("ambiguous worktree extension", func(t *testing.T) {
		root := t.TempDir()
		runTestGit(t, root, "init", "--initial-branch=main")
		runTestGit(t, root, "config", "--local", "--add", "extensions.worktreeConfig", "true")
		runTestGit(t, root, "config", "--local", "--add", "extensions.worktreeConfig", "false")
		if _, err := localExecutionOverrides(context.Background(), root); !errors.Is(err, repoport.ErrTargetConflict) {
			t.Fatalf("localExecutionOverrides() ambiguous extension error = %v", err)
		}
	})

	t.Run("malformed worktree config", func(t *testing.T) {
		root := t.TempDir()
		runTestGit(t, root, "init", "--initial-branch=main")
		runTestGit(t, root, "config", "--local", "extensions.worktreeConfig", "true")
		if err := os.WriteFile(filepath.Join(root, ".git", "config.worktree"), []byte("[filter\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := localExecutionOverrides(context.Background(), root); !errors.Is(err, repoport.ErrTargetConflict) {
			t.Fatalf("localExecutionOverrides() malformed worktree error = %v", err)
		}
	})
}

func TestResolveWorkspaceCanonicalizesRemoteAliases(t *testing.T) {
	root := t.TempDir()
	runTestGit(t, root, "init", "--initial-branch=main")
	runTestGit(t, root, "remote", "add", "origin", "git@github.com:example/product.git")
	adapter := New()
	snapshot, err := adapter.ResolveWorkspace(
		context.Background(), root, "https://GitHub.com:443/example/product.git/",
	)
	if err != nil {
		t.Fatalf("ResolveWorkspace() error = %v", err)
	}
	if snapshot.SourcePath != root || snapshot.GitCommonDirectory != filepath.Join(root, ".git") ||
		snapshot.SourceDevice == 0 || snapshot.SourceInode == 0 ||
		snapshot.GitCommonDevice == 0 || snapshot.GitCommonInode == 0 ||
		snapshot.RepositoryKey != "github.com/example/product" ||
		snapshot.CanonicalRemote != "ssh://git@github.com/example/product" || snapshot.RepositoryID == "" {
		t.Fatalf("Workspace snapshot = %#v", snapshot)
	}
}

func TestResolveWorkspacePreservesSinglePassPercentIdentity(t *testing.T) {
	const remote = "https://github.com/acme/repo%252egit"
	root := t.TempDir()
	runTestGit(t, root, "init", "--initial-branch=main")
	runTestGit(t, root, "remote", "add", "origin", remote)
	snapshot, err := New().ResolveWorkspace(context.Background(), root, remote)
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.CanonicalRemote != remote || snapshot.RepositoryKey != "github.com/acme/repo%2egit" ||
		snapshot.RepositoryID == "" {
		t.Fatalf("single-pass Workspace snapshot = %#v", snapshot)
	}
	second, err := New().ResolveWorkspace(context.Background(), root, snapshot.CanonicalRemote)
	if err != nil || second != snapshot {
		t.Fatalf("canonical Workspace re-resolution = %#v, %v; want %#v", second, err, snapshot)
	}
}

func TestResolveWorkspaceDoesNotPropagateCredentialBearingRemote(t *testing.T) {
	const secret = "token-shaped-username-0123456789abcdef"
	root := t.TempDir()
	runTestGit(t, root, "init", "--initial-branch=main")
	runTestGit(t, root, "remote", "add", "origin", "https://"+secret+"@github.com/example/product.git")
	_, err := New().ResolveWorkspace(
		context.Background(), root, "https://github.com/example/product.git",
	)
	if !errors.Is(err, repoport.ErrWorkspaceMismatch) || strings.Contains(err.Error(), secret) {
		t.Fatalf("credential-bearing remote error = %v", err)
	}
}

func TestResolveWorkspaceDoesNotPropagateMalformedRemote(t *testing.T) {
	for name, malformed := range map[string]string{
		"embedded IPv4":           "ssh://git@[192.168.1.1::]/example/product.git",
		"raw repeated suffix":     "https://github.com/example/repository.git.git",
		"case repeated suffix":    "https://github.com/example/repository.GIT.git",
		"encoded prefix suffix":   "https://github.com/example/repository%2egit.git",
		"encoded terminal suffix": "https://github.com/example/repository.git%2egit",
		"deeper suffix chain":     "https://github.com/example/repository%2Egit%2egit%2EGIT",
	} {
		t.Run(name, func(t *testing.T) {
			root := t.TempDir()
			runTestGit(t, root, "init", "--initial-branch=main")
			runTestGit(t, root, "remote", "add", "origin", malformed)
			_, err := New().ResolveWorkspace(
				context.Background(), root, "https://github.com/example/product.git",
			)
			if !errors.Is(err, repoport.ErrWorkspaceMismatch) || strings.Contains(err.Error(), malformed) {
				t.Fatalf("malformed remote error = %v", err)
			}
		})
	}
}

func TestResolveWorkspaceRejectsPathAliasesAndNoncanonicalRoots(t *testing.T) {
	root := t.TempDir()
	runTestGit(t, root, "init", "--initial-branch=main")
	runTestGit(t, root, "remote", "add", "origin", "https://github.com/example/product.git")
	adapter := New()

	symlink := filepath.Join(t.TempDir(), "product-link")
	if err := os.Symlink(root, symlink); err != nil {
		t.Fatal(err)
	}
	nested := filepath.Join(root, "nested")
	if err := os.Mkdir(nested, 0o700); err != nil {
		t.Fatal(err)
	}
	for name, candidate := range map[string]string{
		"symlink":                     symlink,
		"lexical traversal":           root + string(filepath.Separator) + "nested" + string(filepath.Separator) + "..",
		"nested repository directory": nested,
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := adapter.ResolveWorkspace(
				context.Background(), candidate, "https://github.com/example/product.git",
			); !errors.Is(err, repoport.ErrWorkspaceMismatch) {
				t.Fatalf("ResolveWorkspace(%q) error = %v", candidate, err)
			}
		})
	}
}

func TestResolveWorkspaceRejectsLinkedWorktreeAsCanonicalSource(t *testing.T) {
	root := t.TempDir()
	runTestGit(t, root, "init", "--initial-branch=main")
	runTestGit(t, root, "config", "user.name", "Director test")
	runTestGit(t, root, "config", "user.email", "director@example.invalid")
	runTestGit(t, root, "remote", "add", "origin", "https://github.com/example/product.git")
	if err := os.WriteFile(filepath.Join(root, "README.md"), []byte("product\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	runTestGit(t, root, "add", "--", "README.md")
	runTestGit(t, root, "commit", "-m", "baseline")
	linked := filepath.Join(t.TempDir(), "linked")
	runTestGit(t, root, "worktree", "add", "--detach", linked, "HEAD")
	if _, err := New().ResolveWorkspace(
		context.Background(), linked, "https://github.com/example/product.git",
	); !errors.Is(err, repoport.ErrWorkspaceMismatch) {
		t.Fatalf("linked worktree error = %v", err)
	}
}
