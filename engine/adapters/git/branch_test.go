// SPDX-License-Identifier: Apache-2.0

package git

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"

	"github.com/mcuadros/director-engine/domain/execution"
	publicationdomain "github.com/mcuadros/director-engine/domain/publication"
	repositorydomain "github.com/mcuadros/director-engine/domain/repository"
	gitport "github.com/mcuadros/director-engine/ports/git"
)

func branchGit(t *testing.T, directory string, arguments ...string) string {
	t.Helper()
	command := exec.Command("git", append([]string{"-C", directory}, arguments...)...)
	command.Env = append(os.Environ(), "LC_ALL=C", "GIT_CONFIG_NOSYSTEM=1")
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v: %s", arguments, err, output)
	}
	return strings.TrimSpace(string(output))
}

func writeSSHFixture(t *testing.T, root, remote string) string {
	t.Helper()
	path := filepath.Join(root, "git-ssh")
	script := fmt.Sprintf("#!/bin/sh\ncommand=\nfor argument in \"$@\"; do command=$argument; done\ncase \"$command\" in\n  git-upload-pack*) exec git-upload-pack %q ;;\n  git-receive-pack*) exec git-receive-pack %q ;;\nesac\nexit 64\n", remote, remote)
	if err := os.WriteFile(path, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestBranchAdapterUsesExactLeaseAgainstDisposableRemote(t *testing.T) {
	root := t.TempDir()
	source, remote := filepath.Join(root, "source"), filepath.Join(root, "remote.git")
	if err := os.Mkdir(source, 0o700); err != nil {
		t.Fatal(err)
	}
	branchGit(t, source, "init", "-b", "main")
	branchGit(t, source, "config", "user.name", "Publication Fixture")
	branchGit(t, source, "config", "user.email", "publication@example.invalid")
	if err := os.WriteFile(filepath.Join(source, "fixture.txt"), []byte("base\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	branchGit(t, source, "add", "fixture.txt")
	branchGit(t, source, "commit", "-m", "base")
	base := branchGit(t, source, "rev-parse", "HEAD")
	if err := os.WriteFile(filepath.Join(source, "fixture.txt"), []byte("candidate one\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	branchGit(t, source, "commit", "-am", "candidate one")
	first := branchGit(t, source, "rev-parse", "HEAD")
	if err := os.Mkdir(remote, 0o700); err != nil {
		t.Fatal(err)
	}
	branchGit(t, remote, "init", "--bare")
	remoteURL := "ssh://git@github.com/example/product.git"
	identity, err := repositorydomain.CanonicalRemote(remoteURL)
	if err != nil {
		t.Fatal(err)
	}
	branchGit(t, source, "remote", "add", "origin", remoteURL)
	ssh := writeSSHFixture(t, root, remote)
	t.Setenv("GIT_SSH_COMMAND", ssh)
	sourceInfo, _ := os.Stat(source)
	commonInfo, _ := os.Stat(filepath.Join(source, ".git"))
	sourceStat, commonStat := sourceInfo.Sys().(*syscall.Stat_t), commonInfo.Sys().(*syscall.Stat_t)
	binding := execution.RepositoryBinding{RepositoryID: identity.ID, RepositoryKey: identity.Key, CanonicalRemote: identity.Canonical,
		SourcePath: source, SourceDevice: uint64(sourceStat.Dev), SourceInode: sourceStat.Ino,
		GitCommonDirectory: filepath.Join(source, ".git"), GitCommonDevice: uint64(commonStat.Dev), GitCommonInode: commonStat.Ino,
		WorktreePath: filepath.Join(root, "worktree"), Branch: "task/dir-m4.5", BaseSHA: base}
	hash := execution.RepositoryBindingSHA256(binding)
	request := gitport.RemoteRefRequest{Repository: binding, RepositorySHA256: hash, RemoteName: "origin",
		CanonicalRemote: identity.Canonical, Branch: binding.Branch, TaskStoreNowMillis: 1_000}
	adapter := New()
	absent, err := adapter.ObserveRemoteRef(context.Background(), request)
	if err != nil || absent.Code != publicationdomain.CodeOK || absent.Exists {
		t.Fatalf("absent = %#v, %v", absent, err)
	}
	result, err := adapter.PushExact(context.Background(), gitport.PushRequest{RemoteRefRequest: request, CandidateSHA: first, ProtectedBranches: []string{"main"}})
	if err != nil || !result.Handoff || result.Code != publicationdomain.CodeOK {
		t.Fatalf("create push = %#v, %v", result, err)
	}
	present, _ := adapter.ObserveRemoteRef(context.Background(), request)
	if !present.Exists || present.OID != first {
		t.Fatalf("present = %#v", present)
	}
	if err := os.WriteFile(filepath.Join(source, "fixture.txt"), []byte("candidate two\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	branchGit(t, source, "commit", "-am", "candidate two")
	second := branchGit(t, source, "rev-parse", "HEAD")
	updated, _ := adapter.PushExact(context.Background(), gitport.PushRequest{RemoteRefRequest: request, CandidateSHA: second, ExpectedRemoteOID: first, ProtectedBranches: []string{"main"}})
	if !updated.Handoff || updated.Code != publicationdomain.CodeOK {
		t.Fatalf("update push = %#v", updated)
	}
	stale, _ := adapter.PushExact(context.Background(), gitport.PushRequest{RemoteRefRequest: request, CandidateSHA: first, ExpectedRemoteOID: first, ProtectedBranches: []string{"main"}})
	if !stale.Handoff || stale.Code != publicationdomain.CodeResponseUnknown {
		t.Fatalf("stale lease = %#v", stale)
	}
	final, _ := adapter.ObserveRemoteRef(context.Background(), request)
	if final.OID != second {
		t.Fatalf("stale lease changed remote to %s", final.OID)
	}
	protectedBinding := binding
	protectedBinding.Branch = "main"
	protectedRequest := request
	protectedRequest.Repository = protectedBinding
	protectedRequest.RepositorySHA256 = execution.RepositoryBindingSHA256(protectedBinding)
	protectedRequest.Branch = "main"
	protected, _ := adapter.PushExact(context.Background(), gitport.PushRequest{RemoteRefRequest: protectedRequest,
		CandidateSHA: second, ExpectedRemoteOID: base, ProtectedBranches: []string{"main"}})
	if protected.Handoff || protected.Code != publicationdomain.CodeProtectedRef {
		t.Fatalf("protected push = %#v", protected)
	}
}
