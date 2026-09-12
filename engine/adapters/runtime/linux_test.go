// SPDX-License-Identifier: Apache-2.0

package runtime

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"

	"github.com/mcuadros/director-engine/domain/execution"
	repositorydomain "github.com/mcuadros/director-engine/domain/repository"
	runtimeport "github.com/mcuadros/director-engine/ports/runtime"
)

func testGit(t *testing.T, cwd string, arguments ...string) string {
	t.Helper()
	command := exec.Command("git", arguments...)
	command.Dir = cwd
	command.Env = append(os.Environ(), "GIT_CONFIG_NOSYSTEM=1")
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s: %v: %s", strings.Join(arguments, " "), err, output)
	}
	return strings.TrimSpace(string(output))
}

func TestLinuxRuntimeCreatesOnlyTheExactOwnedWorktreeAndRefusesTerminalShortcut(t *testing.T) {
	root := t.TempDir()
	source := filepath.Join(root, "source")
	if err := os.Mkdir(source, 0o700); err != nil {
		t.Fatal(err)
	}
	testGit(t, source, "init", "--initial-branch=main")
	testGit(t, source, "config", "user.name", "Director test")
	testGit(t, source, "config", "user.email", "director@example.invalid")
	testGit(t, source, "remote", "add", "origin", "https://github.com/example/runtime.git")
	if err := os.WriteFile(filepath.Join(source, "README.md"), []byte("runtime\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	testGit(t, source, "add", "README.md")
	testGit(t, source, "commit", "-m", "fixture")
	base := testGit(t, source, "rev-parse", "HEAD")
	common := filepath.Join(source, ".git")
	sourceInfo, _ := os.Stat(source)
	commonInfo, _ := os.Stat(common)
	sourceID, commonID := sourceInfo.Sys().(*syscall.Stat_t), commonInfo.Sys().(*syscall.Stat_t)
	remote, _ := repositorydomain.CanonicalRemote("https://github.com/example/runtime.git")
	worktree := filepath.Join(root, "worktrees", "run-1")
	if err := os.MkdirAll(filepath.Dir(worktree), 0o700); err != nil {
		t.Fatal(err)
	}
	binding := execution.RepositoryBinding{RepositoryID: remote.ID, RepositoryKey: remote.Key, CanonicalRemote: remote.Canonical,
		SourcePath: source, SourceDevice: uint64(sourceID.Dev), SourceInode: sourceID.Ino,
		GitCommonDirectory: common, GitCommonDevice: uint64(commonID.Dev), GitCommonInode: commonID.Ino,
		WorktreePath: worktree, Branch: "task/runtime-1", BaseSHA: base}
	scope := execution.Scope{ProjectID: "project-1", WorkspaceID: "workspace-1", TaskID: "task-1", RunID: "run-1"}
	surfaces := execution.LifecycleSurfaces{}
	isolation := execution.IsolationObservation{Observed: true, Runtime: "podman", Rootless: true, ReadOnlyRootFilesystem: true,
		CapabilitiesDropped: true, NoNewPrivileges: true, PrivateNetworkNamespace: true, RuntimeSocketsAbsent: true,
		ControlToolsAbsent: true, OwnedWorktreeOnly: true, FixedStdioMCP: true}
	request := runtimeport.Request{Scope: scope, Effect: execution.Effect{ID: "effect-worktree", Kind: execution.EffectWorktreeCreate,
		Phase: execution.EffectIntentRecorded, AttemptLimit: 2}, Repository: binding, SourcePath: source, WorktreePath: worktree,
		Branch: binding.Branch, BaseSHA: base, BindingHash: execution.RepositoryBindingSHA256(binding), LifecycleSurfaces: surfaces,
		LifecycleDigest: execution.LifecycleDigest(surfaces), Isolation: isolation, IsolationDigest: execution.AdmitIsolation(isolation).Digest}
	adapter, err := New(filepath.Join(root, "runtime"))
	if err != nil {
		t.Fatal(err)
	}
	observed, err := adapter.ObserveEffect(context.Background(), request)
	if err != nil || observed.Status != execution.ObservationAbsent {
		t.Fatalf("initial observation = %#v, %v", observed, err)
	}
	if err := adapter.DispatchEffect(context.Background(), request); err != nil {
		t.Fatal(err)
	}
	observed, err = adapter.ObserveEffect(context.Background(), request)
	if err != nil || observed.Status != execution.ObservationDesired || observed.ExternalID == "" {
		t.Fatalf("created observation = %#v, %v", observed, err)
	}
	remove := request
	remove.Effect = execution.Effect{ID: "effect-remove", Kind: execution.EffectWorktreeRemove, Phase: execution.EffectIntentRecorded, AttemptLimit: 1}
	if err := adapter.DispatchEffect(context.Background(), remove); err == nil {
		t.Fatal("runtime bypassed the dedicated guarded cleanup service")
	}
	if _, err := os.Stat(worktree); err != nil {
		t.Fatalf("refused cleanup damaged the owned worktree: %v", err)
	}
}

func TestLinuxRuntimeArtifactCleanupIsExactIdempotentAndRefusesChangedMarker(t *testing.T) {
	root := t.TempDir()
	adapter, err := New(filepath.Join(root, "runtime"))
	if err != nil {
		t.Fatal(err)
	}
	binding := strings.Repeat("a", 64)
	effects := []execution.Effect{{ID: "boundary-effect", Kind: execution.EffectBoundaryMaterialize},
		{ID: "setup-effect", Kind: execution.EffectSetupRun}}
	request := runtimeport.Request{Scope: execution.Scope{RunID: "run-artifacts"}, Effect: effects[0], BindingHash: binding}
	path := adapter.marker(request, "boundary")
	if err := writeMarker(path, marker{SchemaVersion: "director.linux-runtime/v1", RunID: "run-artifacts",
		EffectID: effects[0].ID, BindingHash: binding, ExternalID: "boundary-1", CreatedAt: 1}); err != nil {
		t.Fatal(err)
	}
	scope := execution.Scope{ProjectID: "project-artifacts", TaskID: "task-artifacts", RunID: "run-artifacts"}
	if err := adapter.CleanupRunArtifacts(context.Background(), scope, binding, effects); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(path); !os.IsNotExist(err) {
		t.Fatalf("exact boundary marker survived cleanup: %v", err)
	}
	if err := adapter.CleanupRunArtifacts(context.Background(), scope, binding, effects); err != nil {
		t.Fatalf("idempotent artifact cleanup: %v", err)
	}
	if err := writeMarker(path, marker{SchemaVersion: "director.linux-runtime/v1", RunID: "other-run",
		EffectID: effects[0].ID, BindingHash: binding, ExternalID: "boundary-2", CreatedAt: 1}); err != nil {
		t.Fatal(err)
	}
	if err := adapter.CleanupRunArtifacts(context.Background(), scope, binding, effects); err == nil {
		t.Fatal("changed runtime marker was removed")
	}
	if _, err := os.Lstat(path); err != nil {
		t.Fatalf("refused cleanup did not preserve changed marker: %v", err)
	}
}
