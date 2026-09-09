// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/mcuadros/director-engine/adapters/organizergit"
	app "github.com/mcuadros/director-engine/application/organizer"
	"github.com/mcuadros/director-engine/domain"
	domainconfig "github.com/mcuadros/director-engine/domain/configuration"
	repositorydomain "github.com/mcuadros/director-engine/domain/repository"
	repoport "github.com/mcuadros/director-engine/ports/organizer"
	storeport "github.com/mcuadros/director-engine/ports/taskstore"
)

var errInjectedInterruption = errors.New("injected interruption")

func privateTempDir(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	if err := os.Chmod(root, 0o700); err != nil {
		t.Fatal(err)
	}
	return root
}

type storedCommand struct {
	payload string
	result  domain.CommandResult
}

type memoryProjectStore struct {
	mu         sync.Mutex
	projects   map[string]domain.Project
	workspaces map[string][]domain.Workspace
	commands   map[string]storedCommand
}

func newMemoryProjectStore() *memoryProjectStore {
	return &memoryProjectStore{
		projects:   make(map[string]domain.Project),
		workspaces: make(map[string][]domain.Workspace),
		commands:   make(map[string]storedCommand),
	}
}

func cloneProject(project domain.Project) domain.Project {
	if project.Organizer != nil {
		copy := *project.Organizer
		copy.PendingConfiguration = append(json.RawMessage(nil), copy.PendingConfiguration...)
		project.Organizer = &copy
	}
	if project.Lease != nil {
		lease := *project.Lease
		project.Lease = &lease
	}
	if project.LeaseObservation != nil {
		observation := *project.LeaseObservation
		project.LeaseObservation = &observation
	}
	return project
}

func (store *memoryProjectStore) replay(command domain.CommandRequest) (domain.CommandResult, bool, error) {
	stored, ok := store.commands[command.IdempotencyKey]
	if !ok {
		return domain.CommandResult{}, false, nil
	}
	if stored.payload != string(command.Payload) {
		return domain.CommandResult{}, true, storeport.ErrIdempotencyConflict
	}
	result := stored.result
	result.Replay = true
	return result, true, nil
}

func (store *memoryProjectStore) CreateProject(
	_ context.Context,
	command domain.CommandRequest,
	project domain.Project,
	workspaces []domain.Workspace,
	event domain.Event,
) (domain.CommandResult, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	if result, ok, err := store.replay(command); ok {
		return result, err
	}
	if command.AggregateID != project.ID || command.ExpectedVersion != 0 || project.Version != 0 {
		return domain.CommandResult{}, storeport.ErrInvalidRecord
	}
	if err := domain.ValidateWorkspaceSet(project.ID, workspaces); err != nil ||
		project.Organizer == nil || project.Organizer.ID != domain.OrganizerID(project.ID) {
		return domain.CommandResult{}, storeport.ErrInvalidRecord
	}
	if _, ok := store.projects[project.ID]; ok {
		return domain.CommandResult{}, storeport.ErrAlreadyExists
	}
	store.projects[project.ID] = cloneProject(project)
	store.workspaces[project.ID] = slices.Clone(workspaces)
	result := domain.CommandResult{Outcome: domain.CommandApplied, ObservedVersion: 0, EventID: event.ID}
	store.commands[command.IdempotencyKey] = storedCommand{payload: string(command.Payload), result: result}
	return result, nil
}

func (store *memoryProjectStore) Workspaces(_ context.Context, projectID string) ([]domain.Workspace, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	workspaces, ok := store.workspaces[projectID]
	if !ok {
		return nil, storeport.ErrNotFound
	}
	return slices.Clone(workspaces), nil
}

func (store *memoryProjectStore) Project(_ context.Context, id string) (domain.Project, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	project, ok := store.projects[id]
	if !ok {
		return domain.Project{}, storeport.ErrNotFound
	}
	return cloneProject(project), nil
}

func (store *memoryProjectStore) UpdateProject(
	_ context.Context,
	command domain.CommandRequest,
	project domain.Project,
	event domain.Event,
) (domain.CommandResult, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	if result, ok, err := store.replay(command); ok {
		return result, err
	}
	current, ok := store.projects[project.ID]
	if !ok {
		return domain.CommandResult{}, storeport.ErrNotFound
	}
	if command.AggregateID != project.ID || current.Version != command.ExpectedVersion {
		result := domain.CommandResult{
			Outcome: domain.CommandRejectedVersionConflict, ObservedVersion: current.Version,
		}
		store.commands[command.IdempotencyKey] = storedCommand{payload: string(command.Payload), result: result}
		return result, nil
	}
	if project.Version != current.Version+1 {
		return domain.CommandResult{}, storeport.ErrInvalidRecord
	}
	store.projects[project.ID] = cloneProject(project)
	result := domain.CommandResult{
		Outcome: domain.CommandApplied, ObservedVersion: project.Version, EventID: event.ID,
	}
	store.commands[command.IdempotencyKey] = storedCommand{payload: string(command.Payload), result: result}
	return result, nil
}

func runGit(t *testing.T, directory string, arguments ...string) string {
	t.Helper()
	base := []string{
		"-c", "core.fsmonitor=false",
		"-c", "core.quotePath=false",
		"-c", "core.hooksPath=/dev/null",
		"-c", "commit.gpgsign=false",
	}
	command := exec.Command("git", append(base, arguments...)...)
	command.Dir = directory
	command.Env = append(os.Environ(),
		"GIT_CONFIG_NOSYSTEM=1",
		"GIT_AUTHOR_NAME=Director test",
		"GIT_AUTHOR_EMAIL=director@example.invalid",
		"GIT_COMMITTER_NAME=Director test",
		"GIT_COMMITTER_EMAIL=director@example.invalid",
	)
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s: %v: %s", strings.Join(arguments, " "), err, output)
	}
	return strings.TrimSpace(string(output))
}

type productFacts struct {
	head   string
	tree   string
	status string
}

type organizerRemoteLengthCase struct {
	name           string
	remote         string
	canonicalBytes int
	wantAccepted   bool
}

func organizerPaddedRemote(t *testing.T, prefix, suffix string, totalBytes int) string {
	t.Helper()
	padding := totalBytes - len(prefix) - len(suffix)
	if padding < 1 {
		t.Fatalf("remote fixture length %d is too short", totalBytes)
	}
	return prefix + strings.Repeat("a", padding) + suffix
}

func organizerRemoteLengthCases(t *testing.T) []organizerRemoteLengthCase {
	t.Helper()
	cases := make([]organizerRemoteLengthCase, 0, 24)
	for _, target := range []int{
		repositorydomain.MaximumRemoteBytes - 1,
		repositorydomain.MaximumRemoteBytes,
		repositorydomain.MaximumRemoteBytes + 1,
	} {
		accepted := target <= repositorydomain.MaximumRemoteBytes
		name := fmt.Sprintf("canonical-%d", target)
		scpCanonicalPrefix := "ssh://git@a/"
		scpPath := strings.Repeat("a", target-len(scpCanonicalPrefix))
		urlCanonicalPrefix := "ssh://git@[0:0:0:0:0:0:0:1]/"
		urlPath := strings.Repeat("a", target-len(urlCanonicalPrefix))
		cases = append(cases,
			organizerRemoteLengthCase{
				name: "raw-url/" + name, remote: organizerPaddedRemote(t, "https://a.example/", "", target),
				canonicalBytes: target, wantAccepted: accepted,
			},
			organizerRemoteLengthCase{
				name: "scp-expansion/" + name, remote: "git@a:" + scpPath,
				canonicalBytes: target, wantAccepted: accepted,
			},
			organizerRemoteLengthCase{
				name:           "scp-raw-input/" + name,
				remote:         "git@a:" + strings.Repeat("a", target-len("git@a:")),
				canonicalBytes: target + len(scpCanonicalPrefix) - len("git@a:"), wantAccepted: false,
			},
			organizerRemoteLengthCase{
				name: "ipv6-url-expansion/" + name, remote: "ssh://git@[::1]/" + urlPath,
				canonicalBytes: target, wantAccepted: accepted,
			},
			organizerRemoteLengthCase{
				name:           "ipv6-url-raw-input/" + name,
				remote:         "ssh://git@[::1]/" + strings.Repeat("a", target-len("ssh://git@[::1]/")),
				canonicalBytes: target + len(urlCanonicalPrefix) - len("ssh://git@[::1]/"), wantAccepted: false,
			},
			organizerRemoteLengthCase{
				name: "percent-encoded/" + name, remote: organizerPaddedRemote(t, "https://a.example/", "%25z", target),
				canonicalBytes: target, wantAccepted: accepted,
			},
			organizerRemoteLengthCase{
				name: "utf8-byte-boundary/" + name, remote: organizerPaddedRemote(t, "https://a.example/", "é", target),
				canonicalBytes: target, wantAccepted: accepted,
			},
			organizerRemoteLengthCase{
				name:           "repeated-git-suffix/" + name,
				remote:         organizerPaddedRemote(t, "https://a.example/", ".git.git", target),
				canonicalBytes: target, wantAccepted: false,
			},
		)
	}
	return cases
}

func newProductRepository(t *testing.T, root string) (string, string, productFacts) {
	t.Helper()
	path := filepath.Join(root, "product")
	if err := os.Mkdir(path, 0o700); err != nil {
		t.Fatal(err)
	}
	runGit(t, path, "init", "--initial-branch=main")
	remote := "https://github.com/example/product.git"
	runGit(t, path, "remote", "add", "origin", remote)
	if err := os.WriteFile(filepath.Join(path, "product.txt"), []byte("unchanged\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	runGit(t, path, "add", "--", "product.txt")
	runGit(t, path, "commit", "-m", "product baseline")
	return path, remote, readProductFacts(t, path)
}

func readProductFacts(t *testing.T, path string) productFacts {
	t.Helper()
	return productFacts{
		head:   runGit(t, path, "rev-parse", "HEAD"),
		tree:   runGit(t, path, "rev-parse", "HEAD^{tree}"),
		status: runGit(t, path, "status", "--porcelain=v1", "--untracked-files=all"),
	}
}

func installHostileLocalGitConfig(t *testing.T, repositoryPath, executedPath string) {
	t.Helper()
	scriptPath := filepath.Join(filepath.Dir(executedPath), "hostile-git-command")
	script := []byte(fmt.Sprintf("#!/bin/sh\nprintf '%%s\\n' \"$*\" >> %q\nexec /bin/cat\n", executedPath))
	if err := os.WriteFile(scriptPath, script, 0o700); err != nil {
		t.Fatal(err)
	}
	hooksPath := filepath.Join(repositoryPath, ".git", "hostile-hooks")
	if err := os.MkdirAll(hooksPath, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(hooksPath, "pre-commit"), script, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(repositoryPath, ".git", "info"), 0o700); err != nil {
		t.Fatal(err)
	}
	for _, entry := range [][2]string{
		{"extensions.worktreeConfig", "true"},
		{"core.fsmonitor", scriptPath},
		{"core.hooksPath", hooksPath},
		{"diff.external", scriptPath},
		{"diff.hostile.command", scriptPath},
		{"diff.hostile.textconv", scriptPath},
		{"filter.hostile.clean", scriptPath},
		{"filter.hostile.smudge", scriptPath},
		{"filter.hostile.process", scriptPath},
	} {
		runGit(t, repositoryPath, "config", "--local", entry[0], entry[1])
	}
	for _, entry := range [][2]string{
		{"core.fsmonitor", scriptPath},
		{"core.hooksPath", hooksPath},
		{"diff.external", scriptPath},
		{"diff.hostile-worktree.command", scriptPath},
		{"diff.hostile-worktree.textconv", scriptPath},
		{"filter.hostile-worktree.clean", scriptPath},
		{"filter.hostile-worktree.smudge", scriptPath},
		{"filter.hostile-worktree.required", "true"},
	} {
		runGit(t, repositoryPath, "config", "--worktree", entry[0], entry[1])
	}
	if err := os.WriteFile(
		filepath.Join(repositoryPath, ".git", "info", "attributes"),
		[]byte("paseo-director.json filter=hostile diff=hostile\nREADME.md filter=hostile-worktree diff=hostile-worktree\n"), 0o600,
	); err != nil {
		t.Fatal(err)
	}
}

func requirePathAbsent(t *testing.T, path string) {
	t.Helper()
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		content, _ := os.ReadFile(path)
		t.Fatalf("hostile Git configuration executed with arguments %q: %v", content, err)
	}
}

func configurationJSON(projectID, projectName, workspacePath, remote string) []byte {
	return configurationJSONWithReferences(
		projectID, projectName, workspacePath, remote,
		"skills/commits/SKILL.md", "templates/task.md",
	)
}

func configurationJSONWithReferences(
	projectID, projectName, workspacePath, remote, skillPath, templatePath string,
) []byte {
	return []byte(fmt.Sprintf(`{
  "$schema": %q,
  "schemaVersion": 1,
  "project": {"id": %q, "name": %q},
  "workspaces": [{
    "id": "product",
    "remote": %q,
    "sourcePath": %q,
    "defaultBaseBranch": "main"
  }],
  "agentProfiles": {
    "taskAgent": {"provider":"codex","model":"gpt-5.6","effort":"high","permissionMode":"workspace-write"},
    "reviewerAgent": {"provider":"opencode","model":"reviewer-1","effort":"high","permissionMode":"read-only"}
  },
  "defaults": {
    "launchPolicy":"manual",
    "deliveryMode":"pull_request",
    "limits":{"maxActiveTasks":4,"maxActiveTasksPerWorkspace":2,"maxConcurrentAgents":8,"maxSubagentsPerTask":3},
    "runBudget":{"elapsedSeconds":7200,"tokens":200000,"turns":32,"ciCycles":4},
    "autoFixCiFailures":true,
    "autoFixReviewFeedback":true
  },
  "workspaceOverrides": [],
  "skills": [{"id":"commits","path":%q}],
  "templates": [{"id":"task","path":%q}]
}`, domainconfig.SchemaID, projectID, projectName, remote, filepath.ToSlash(workspacePath), skillPath, templatePath))
}

func confirmed() app.HumanConfirmation {
	return app.HumanConfirmation{ActorID: "human:owner", Confirmed: true}
}

func createRequest(projectID, projectName, repositoryPath string, document []byte) app.CreateRequest {
	return app.CreateRequest{
		RequestID: "request-" + projectID,
		ProjectID: projectID, ProjectName: projectName,
		RepositoryPath: repositoryPath, ConfigurationJSON: document,
	}
}

func TestCreatePreviewIsReadOnlyAndApplyRecoversEveryBoundary(t *testing.T) {
	boundaries := []app.Boundary{
		app.BoundaryIntentPersisted,
		app.BoundaryRepositoryPrepared,
		app.BoundaryRepositoryRecorded,
		app.BoundaryConfigurationWritten,
		app.BoundaryConfigurationRecorded,
		app.BoundaryReadmeWritten,
		app.BoundaryReadmeRecorded,
		app.BoundaryReferencesWritten,
		app.BoundaryReferencesRecorded,
		app.BoundaryRepositoryInitialized,
		app.BoundaryInitializationRecorded,
		app.BoundaryRevisionCommitted,
		app.BoundaryRevisionRecorded,
		app.BoundaryActivationPersisted,
	}
	for _, boundary := range boundaries {
		t.Run(string(boundary), func(t *testing.T) {
			root := privateTempDir(t)
			productPath, remote, before := newProductRepository(t, root)
			repositoryPath := filepath.Join(root, "organizer")
			const skillPath = "skills/naïve/SKILL.md"
			const templatePath = "templates/résumé.md"
			request := createRequest(
				"project-create", "Create Project", repositoryPath,
				configurationJSONWithReferences(
					"project-create", "Create Project", productPath, remote,
					skillPath, templatePath,
				),
			)
			store := newMemoryProjectStore()
			repository := organizergit.New()
			service := app.New(store, repository, func(observed app.Boundary) error {
				if observed == boundary {
					return errInjectedInterruption
				}
				return nil
			})

			preview, err := service.PreviewCreate(context.Background(), request)
			if err != nil || !preview.Valid || preview.ID == "" {
				t.Fatalf("PreviewCreate() = %#v, %v", preview, err)
			}
			secondPreview, err := service.PreviewCreate(context.Background(), request)
			if err != nil || !reflect.DeepEqual(secondPreview, preview) {
				t.Fatalf("PreviewCreate() is not deterministic: first=%#v second=%#v error=%v", preview, secondPreview, err)
			}
			if len(store.projects) != 0 {
				t.Fatal("PreviewCreate persisted Project state")
			}
			if _, err := os.Stat(repositoryPath); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("PreviewCreate changed target: %v", err)
			}
			if after := readProductFacts(t, productPath); after != before {
				t.Fatalf("PreviewCreate changed product repository: before=%#v after=%#v", before, after)
			}

			_, err = service.ApplyCreate(context.Background(), app.ApplyCreateCommand{
				RequestID: request.RequestID, PreviewID: preview.ID,
				Request: request, Confirmation: confirmed(),
			})
			if !errors.Is(err, errInjectedInterruption) {
				t.Fatalf("ApplyCreate() interruption error = %v", err)
			}

			restarted := app.New(store, repository, nil)
			projection, err := restarted.Recover(context.Background(), request.ProjectID)
			if err != nil {
				t.Fatalf("Recover() error = %v", err)
			}
			if projection.State != "active" || projection.OrganizerRevision == "" || projection.ConfigurationSHA256 == "" {
				t.Fatalf("Recover() = %#v", projection)
			}
			reopened, err := app.New(store, repository, nil).Open(context.Background(), request.ProjectID)
			if err != nil || !reflect.DeepEqual(reopened, projection) {
				t.Fatalf("Open() = %#v, %v; want %#v", reopened, err, projection)
			}
			replay, err := restarted.ApplyCreate(context.Background(), app.ApplyCreateCommand{
				RequestID: request.RequestID, PreviewID: preview.ID,
				Request: request, Confirmation: confirmed(),
			})
			if err != nil || !reflect.DeepEqual(replay, projection) {
				t.Fatalf("ApplyCreate() replay = %#v, %v", replay, err)
			}
			if head := runGit(t, repositoryPath, "rev-list", "--count", "HEAD"); head != "1" {
				t.Fatalf("idempotent recovery produced %s commits", head)
			}
			if status := runGit(t, repositoryPath, "status", "--porcelain=v1", "--untracked-files=all"); status != "" {
				t.Fatalf("Organizer is dirty after recovery: %q", status)
			}
			tracked := runGit(t, repositoryPath, "ls-files", "-z")
			for _, relative := range []string{skillPath, templatePath} {
				if !strings.Contains(tracked, relative+"\x00") {
					t.Fatalf("non-ASCII reference path %q missing from raw tracked files %q", relative, tracked)
				}
			}
			if after := readProductFacts(t, productPath); after != before {
				t.Fatalf("Create flow changed product repository: before=%#v after=%#v", before, after)
			}
		})
	}
}

func TestCreatePreviewAndApplyShareSinglePassRemoteIdentity(t *testing.T) {
	const remote = "https://github.com/acme/repo%252egit"
	root := privateTempDir(t)
	productPath, _, before := newProductRepository(t, root)
	runGit(t, productPath, "remote", "set-url", "origin", remote)
	before = readProductFacts(t, productPath)
	repositoryPath := filepath.Join(root, "organizer")
	request := createRequest(
		"project-percent-remote", "Percent Remote", repositoryPath,
		configurationJSON("project-percent-remote", "Percent Remote", productPath, remote),
	)
	store := newMemoryProjectStore()
	service := app.New(store, organizergit.New(), nil)
	preview, err := service.PreviewCreate(context.Background(), request)
	if err != nil || !preview.Valid || preview.ID == "" {
		t.Fatalf("PreviewCreate() = %#v, %v", preview, err)
	}
	if _, err := service.ApplyCreate(context.Background(), app.ApplyCreateCommand{
		RequestID: request.RequestID, PreviewID: preview.ID, Request: request, Confirmation: confirmed(),
	}); err != nil {
		t.Fatalf("ApplyCreate() = %v", err)
	}
	workspaces := store.workspaces[request.ProjectID]
	if len(workspaces) != 1 || workspaces[0].Repository.CanonicalRemote != remote ||
		workspaces[0].Repository.Key != "github.com/acme/repo%2egit" {
		t.Fatalf("durable canonical Workspaces = %#v", workspaces)
	}
	if after := readProductFacts(t, productPath); after != before {
		t.Fatalf("Preview/Apply changed product repository: before=%#v after=%#v", before, after)
	}
}

func TestCreateAndAdoptEnforceCanonicalRemoteLengthBeforePersistence(t *testing.T) {
	for index, test := range organizerRemoteLengthCases(t) {
		t.Run(test.name, func(t *testing.T) {
			root := privateTempDir(t)
			productPath, _, _ := newProductRepository(t, root)
			runGit(t, productPath, "remote", "set-url", "origin", test.remote)
			productBefore := readProductFacts(t, productPath)
			projectID := fmt.Sprintf("remote-boundary-%02d", index)
			projectName := fmt.Sprintf("Remote Boundary %02d", index)
			repositoryPath := filepath.Join(root, "organizer")
			document := configurationJSON(projectID, projectName, productPath, test.remote)
			request := createRequest(projectID, projectName, repositoryPath, document)
			createStore := newMemoryProjectStore()
			createService := app.New(createStore, organizergit.New(), nil)
			preview, err := createService.PreviewCreate(context.Background(), request)

			if !test.wantAccepted {
				if err != nil || preview.Valid || preview.ID == "" {
					t.Fatalf("PreviewCreate() = %#v, %v; want bounded invalid Preview", preview, err)
				}
				encoded, marshalErr := json.Marshal(preview)
				if marshalErr != nil || strings.Contains(string(encoded), test.remote) {
					t.Fatalf("Create Preview propagated rejected remote: %s, %v", encoded, marshalErr)
				}
				_, err = createService.ApplyCreate(context.Background(), app.ApplyCreateCommand{
					RequestID: request.RequestID, PreviewID: preview.ID, Request: request, Confirmation: confirmed(),
				})
				if !errors.Is(err, app.ErrPreviewInvalid) || strings.Contains(err.Error(), test.remote) {
					t.Fatalf("ApplyCreate() rejected remote error = %v", err)
				}
				if len(createStore.projects) != 0 || len(createStore.commands) != 0 {
					t.Fatalf("rejected Create reached TaskStore: projects=%#v commands=%#v", createStore.projects, createStore.commands)
				}
				if _, err := os.Stat(repositoryPath); !errors.Is(err, os.ErrNotExist) {
					t.Fatalf("rejected Create changed Organizer target: %v", err)
				}

				validRemote := "https://github.com/example/product.git"
				runGit(t, productPath, "remote", "set-url", "origin", validRemote)
				seedDocument := configurationJSON(projectID, projectName, productPath, validRemote)
				seedRequest := createRequest(projectID, projectName, repositoryPath, seedDocument)
				seedService := app.New(newMemoryProjectStore(), organizergit.New(), nil)
				seedPreview, err := seedService.PreviewCreate(context.Background(), seedRequest)
				if err != nil || !seedPreview.Valid {
					t.Fatalf("seed PreviewCreate() = %#v, %v", seedPreview, err)
				}
				if _, err := seedService.ApplyCreate(context.Background(), app.ApplyCreateCommand{
					RequestID: seedRequest.RequestID, PreviewID: seedPreview.ID,
					Request: seedRequest, Confirmation: confirmed(),
				}); err != nil {
					t.Fatalf("seed ApplyCreate() = %v", err)
				}
				if err := os.WriteFile(filepath.Join(repositoryPath, "paseo-director.json"), document, 0o600); err != nil {
					t.Fatal(err)
				}
				runGit(t, repositoryPath, "add", "--", "paseo-director.json")
				runGit(t, repositoryPath, "commit", "-m", "inject rejected remote boundary")
				organizerBefore := readProductFacts(t, repositoryPath)
				adoptStore := newMemoryProjectStore()
				adoptService := app.New(adoptStore, organizergit.New(), nil)
				adopt := app.AdoptRequest{
					RequestID: "request-adopt-" + projectID, ProjectID: projectID,
					ProjectName: projectName, RepositoryPath: repositoryPath,
				}
				adoptPreview, err := adoptService.PreviewAdopt(context.Background(), adopt)
				if err != nil || adoptPreview.Valid || adoptPreview.ID == "" {
					t.Fatalf("PreviewAdopt() = %#v, %v; want bounded invalid Preview", adoptPreview, err)
				}
				encoded, marshalErr = json.Marshal(adoptPreview)
				if marshalErr != nil || strings.Contains(string(encoded), test.remote) {
					t.Fatalf("Adopt Preview propagated rejected remote: %s, %v", encoded, marshalErr)
				}
				_, err = adoptService.ApplyAdopt(context.Background(), app.ApplyAdoptCommand{
					RequestID: adopt.RequestID, PreviewID: adoptPreview.ID,
					Request: adopt, Confirmation: confirmed(),
				})
				if !errors.Is(err, app.ErrPreviewInvalid) || strings.Contains(err.Error(), test.remote) {
					t.Fatalf("ApplyAdopt() rejected remote error = %v", err)
				}
				if len(adoptStore.projects) != 0 || len(adoptStore.commands) != 0 {
					t.Fatalf("rejected Adopt reached TaskStore: projects=%#v commands=%#v", adoptStore.projects, adoptStore.commands)
				}
				if after := readProductFacts(t, repositoryPath); after != organizerBefore {
					t.Fatalf("rejected Adopt changed Organizer: before=%#v after=%#v", organizerBefore, after)
				}
				return
			}

			if err != nil || !preview.Valid {
				t.Fatalf("PreviewCreate() = %#v, %v", preview, err)
			}
			if _, err := createService.ApplyCreate(context.Background(), app.ApplyCreateCommand{
				RequestID: request.RequestID, PreviewID: preview.ID, Request: request, Confirmation: confirmed(),
			}); err != nil {
				t.Fatalf("ApplyCreate() = %v", err)
			}
			created := createStore.workspaces[projectID]
			if len(created) != 1 || len(created[0].Repository.CanonicalRemote) != test.canonicalBytes ||
				len(created[0].Repository.CanonicalRemote) > repositorydomain.MaximumRemoteBytes ||
				created[0].ID != domain.WorkspaceID(projectID, "product") {
				t.Fatalf("created durable Workspace = %#v", created)
			}
			identity, err := repositorydomain.CanonicalRemote(created[0].Repository.CanonicalRemote)
			if err != nil || identity.Canonical != created[0].Repository.CanonicalRemote ||
				identity.Key != created[0].Repository.Key || identity.ID != created[0].Repository.ID {
				t.Fatalf("created Workspace identity fixed point = %#v, %v", identity, err)
			}

			organizerBefore := readProductFacts(t, repositoryPath)
			adoptStore := newMemoryProjectStore()
			adoptService := app.New(adoptStore, organizergit.New(), nil)
			adopt := app.AdoptRequest{
				RequestID: "request-adopt-" + projectID, ProjectID: projectID,
				ProjectName: projectName, RepositoryPath: repositoryPath,
			}
			adoptPreview, err := adoptService.PreviewAdopt(context.Background(), adopt)
			if err != nil || !adoptPreview.Valid {
				t.Fatalf("PreviewAdopt() = %#v, %v", adoptPreview, err)
			}
			if _, err := adoptService.ApplyAdopt(context.Background(), app.ApplyAdoptCommand{
				RequestID: adopt.RequestID, PreviewID: adoptPreview.ID, Request: adopt, Confirmation: confirmed(),
			}); err != nil {
				t.Fatalf("ApplyAdopt() = %v", err)
			}
			adopted := adoptStore.workspaces[projectID]
			if !reflect.DeepEqual(adopted, created) {
				t.Fatalf("Create/Adopt Workspace mismatch: created=%#v adopted=%#v", created, adopted)
			}
			if after := readProductFacts(t, repositoryPath); after != organizerBefore {
				t.Fatalf("accepted Adopt changed Organizer: before=%#v after=%#v", organizerBefore, after)
			}
			if after := readProductFacts(t, productPath); after != productBefore {
				t.Fatalf("accepted flow changed product repository: before=%#v after=%#v", productBefore, after)
			}
		})
	}
}

func TestCreatePreviewAndApplyRedactRejectedRemoteIdentity(t *testing.T) {
	for name, remote := range map[string]string{
		"token-shaped username":   "https://token-shaped-username-0123456789abcdef@github.com/example/product.git",
		"malformed embedded IPv4": "ssh://git@[192.168.1.1::]/example/product.git",
		"malformed percent":       "https://github.com/example/product%2",
		"raw repeated suffix":     "https://github.com/example/repository.git.git",
		"case repeated suffix":    "https://github.com/example/repository.GIT.git",
		"encoded prefix suffix":   "https://github.com/example/repository%2egit.git",
		"encoded terminal suffix": "https://github.com/example/repository.git%2egit",
		"deeper suffix chain":     "https://github.com/example/repository%2Egit%2egit%2EGIT",
	} {
		t.Run(name, func(t *testing.T) {
			root := privateTempDir(t)
			productPath, _, _ := newProductRepository(t, root)
			repositoryPath := filepath.Join(root, "organizer")
			request := createRequest(
				"project-rejected-remote", "Rejected Remote", repositoryPath,
				configurationJSON("project-rejected-remote", "Rejected Remote", productPath, remote),
			)
			store := newMemoryProjectStore()
			service := app.New(store, organizergit.New(), nil)
			preview, err := service.PreviewCreate(context.Background(), request)
			if err != nil || preview.Valid || preview.ID == "" {
				t.Fatalf("PreviewCreate() = %#v, %v", preview, err)
			}
			encoded, err := json.Marshal(preview)
			if err != nil || strings.Contains(string(encoded), remote) {
				t.Fatalf("Preview propagated rejected remote: %s, %v", encoded, err)
			}
			_, err = service.ApplyCreate(context.Background(), app.ApplyCreateCommand{
				RequestID: request.RequestID, PreviewID: preview.ID, Request: request, Confirmation: confirmed(),
			})
			if !errors.Is(err, app.ErrPreviewInvalid) || strings.Contains(err.Error(), remote) {
				t.Fatalf("ApplyCreate() rejected remote error = %v", err)
			}
			if len(store.projects) != 0 || len(store.commands) != 0 {
				t.Fatalf("rejected remote reached TaskStore: projects=%#v commands=%#v", store.projects, store.commands)
			}
			if _, err := os.Stat(repositoryPath); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("rejected remote changed Organizer target: %v", err)
			}
		})
	}
}

func TestHostileRepositoryLocalGitConfigNeverExecutes(t *testing.T) {
	root := privateTempDir(t)
	executedPath := filepath.Join(root, "hostile-command-executed")
	productPath, remote, _ := newProductRepository(t, root)
	installHostileLocalGitConfig(t, productPath, executedPath)

	createPath := filepath.Join(root, "organizer-create")
	createInput := createRequest(
		"project-hostile-create", "Hostile Create", createPath,
		configurationJSON("project-hostile-create", "Hostile Create", productPath, remote),
	)
	createService := app.New(newMemoryProjectStore(), organizergit.New(), nil)
	createPreview, err := createService.PreviewCreate(context.Background(), createInput)
	if err != nil || !createPreview.Valid {
		t.Fatalf("PreviewCreate() = %#v, %v", createPreview, err)
	}
	requirePathAbsent(t, executedPath)
	if _, err := createService.ApplyCreate(context.Background(), app.ApplyCreateCommand{
		RequestID: createInput.RequestID, PreviewID: createPreview.ID,
		Request: createInput, Confirmation: confirmed(),
	}); err != nil {
		t.Fatalf("ApplyCreate() = %v", err)
	}
	requirePathAbsent(t, executedPath)

	installHostileLocalGitConfig(t, createPath, executedPath)
	adapterSnapshot, err := organizergit.New().Read(context.Background(), createPath)
	if err != nil || adapterSnapshot.Revision == "" {
		t.Fatalf("Adapter.Read() = %#v, %v", adapterSnapshot, err)
	}
	requirePathAbsent(t, executedPath)
	secondAdapterSnapshot, err := organizergit.New().Read(context.Background(), createPath)
	if err != nil || !reflect.DeepEqual(secondAdapterSnapshot, adapterSnapshot) {
		t.Fatalf("Adapter.Read() is not deterministic: first=%#v second=%#v error=%v", adapterSnapshot, secondAdapterSnapshot, err)
	}
	requirePathAbsent(t, executedPath)
	adoptStore := newMemoryProjectStore()
	adoptService := app.New(adoptStore, organizergit.New(), nil)
	adoptRequest := app.AdoptRequest{
		RequestID: "request-hostile-adopt", ProjectID: createInput.ProjectID,
		ProjectName: createInput.ProjectName, RepositoryPath: createPath,
	}
	adoptPreview, err := adoptService.PreviewAdopt(context.Background(), adoptRequest)
	if err != nil || !adoptPreview.Valid {
		t.Fatalf("PreviewAdopt() = %#v, %v", adoptPreview, err)
	}
	requirePathAbsent(t, executedPath)
	secondAdoptPreview, err := adoptService.PreviewAdopt(context.Background(), adoptRequest)
	if err != nil || !reflect.DeepEqual(secondAdoptPreview, adoptPreview) {
		t.Fatalf("PreviewAdopt() is not deterministic: first=%#v second=%#v error=%v", adoptPreview, secondAdoptPreview, err)
	}
	requirePathAbsent(t, executedPath)
	if _, err := adoptService.ApplyAdopt(context.Background(), app.ApplyAdoptCommand{
		RequestID: adoptRequest.RequestID, PreviewID: adoptPreview.ID,
		Request: adoptRequest, Confirmation: confirmed(),
	}); err != nil {
		t.Fatalf("ApplyAdopt() = %v", err)
	}
	requirePathAbsent(t, executedPath)

	recoveryPath := filepath.Join(root, "organizer-recovery")
	recoveryRequest := createRequest(
		"project-hostile-recovery", "Hostile Recovery", recoveryPath,
		configurationJSON("project-hostile-recovery", "Hostile Recovery", productPath, remote),
	)
	recoveryStore := newMemoryProjectStore()
	recoveryService := app.New(recoveryStore, organizergit.New(), func(boundary app.Boundary) error {
		if boundary == app.BoundaryInitializationRecorded {
			return errInjectedInterruption
		}
		return nil
	})
	recoveryPreview, err := recoveryService.PreviewCreate(context.Background(), recoveryRequest)
	if err != nil || !recoveryPreview.Valid {
		t.Fatalf("recovery PreviewCreate() = %#v, %v", recoveryPreview, err)
	}
	if _, err := recoveryService.ApplyCreate(context.Background(), app.ApplyCreateCommand{
		RequestID: recoveryRequest.RequestID, PreviewID: recoveryPreview.ID,
		Request: recoveryRequest, Confirmation: confirmed(),
	}); !errors.Is(err, errInjectedInterruption) {
		t.Fatalf("recovery ApplyCreate() interruption = %v", err)
	}
	requirePathAbsent(t, executedPath)
	installHostileLocalGitConfig(t, recoveryPath, executedPath)
	projection, err := app.New(recoveryStore, organizergit.New(), nil).Recover(
		context.Background(), recoveryRequest.ProjectID,
	)
	if err != nil || projection.State != "active" {
		t.Fatalf("Recover() = %#v, %v", projection, err)
	}
	requirePathAbsent(t, executedPath)
}

func TestAdoptIsReadOnlyIdempotentAndReopens(t *testing.T) {
	root := privateTempDir(t)
	productPath, remote, productBefore := newProductRepository(t, root)
	repositoryPath := filepath.Join(root, "organizer")
	document := configurationJSON("project-adopt", "Adopt Project", productPath, remote)
	creatorStore := newMemoryProjectStore()
	creator := app.New(creatorStore, organizergit.New(), nil)
	create := createRequest("project-adopt", "Adopt Project", repositoryPath, document)
	createPreview, err := creator.PreviewCreate(context.Background(), create)
	if err != nil || !createPreview.Valid {
		t.Fatalf("create preview = %#v, %v", createPreview, err)
	}
	if _, err := creator.ApplyCreate(context.Background(), app.ApplyCreateCommand{
		RequestID: create.RequestID, PreviewID: createPreview.ID,
		Request: create, Confirmation: confirmed(),
	}); err != nil {
		t.Fatalf("seed Organizer: %v", err)
	}
	organizerBefore := readProductFacts(t, repositoryPath)

	store := newMemoryProjectStore()
	service := app.New(store, organizergit.New(), nil)
	request := app.AdoptRequest{
		RequestID: "request-adopt-01",
		ProjectID: "project-adopt", ProjectName: "Adopt Project", RepositoryPath: repositoryPath,
	}
	preview, err := service.PreviewAdopt(context.Background(), request)
	if err != nil || !preview.Valid || preview.OrganizerRevision != organizerBefore.head {
		t.Fatalf("PreviewAdopt() = %#v, %v", preview, err)
	}
	if len(store.projects) != 0 {
		t.Fatal("PreviewAdopt persisted Project state")
	}
	secondPreview, err := service.PreviewAdopt(context.Background(), request)
	if err != nil || !reflect.DeepEqual(secondPreview, preview) {
		t.Fatalf("PreviewAdopt() is not deterministic: first=%#v second=%#v error=%v", preview, secondPreview, err)
	}
	projection, err := service.ApplyAdopt(context.Background(), app.ApplyAdoptCommand{
		RequestID: request.RequestID, PreviewID: preview.ID,
		Request: request, Confirmation: confirmed(),
	})
	if err != nil || projection.State != "active" {
		t.Fatalf("ApplyAdopt() = %#v, %v", projection, err)
	}
	replay, err := app.New(store, organizergit.New(), nil).ApplyAdopt(
		context.Background(), app.ApplyAdoptCommand{
			RequestID: request.RequestID, PreviewID: preview.ID,
			Request: request, Confirmation: confirmed(),
		},
	)
	if err != nil || !reflect.DeepEqual(replay, projection) {
		t.Fatalf("ApplyAdopt() replay = %#v, %v", replay, err)
	}
	if reopened, err := app.New(store, organizergit.New(), nil).Open(context.Background(), request.ProjectID); err != nil || !reflect.DeepEqual(reopened, projection) {
		t.Fatalf("Open() = %#v, %v", reopened, err)
	}
	durableWorkspaces, err := store.Workspaces(context.Background(), request.ProjectID)
	if err != nil || len(durableWorkspaces) != 1 || durableWorkspaces[0].Key != "product" ||
		durableWorkspaces[0].ID != domain.WorkspaceID(request.ProjectID, "product") {
		t.Fatalf("durable Workspaces = %#v, %v", durableWorkspaces, err)
	}
	store.mu.Lock()
	store.workspaces[request.ProjectID][0].Name = "drifted"
	store.mu.Unlock()
	if _, err := app.New(store, organizergit.New(), nil).Open(context.Background(), request.ProjectID); !errors.Is(err, app.ErrOrganizerDrift) {
		t.Fatalf("Open() Workspace drift error = %v", err)
	}
	store.mu.Lock()
	store.workspaces[request.ProjectID] = slices.Clone(durableWorkspaces)
	store.mu.Unlock()
	if after := readProductFacts(t, repositoryPath); after != organizerBefore {
		t.Fatalf("Adopt mutated Organizer: before=%#v after=%#v", organizerBefore, after)
	}
	if after := readProductFacts(t, productPath); after != productBefore {
		t.Fatalf("Adopt mutated product repository: before=%#v after=%#v", productBefore, after)
	}
	if err := os.WriteFile(filepath.Join(repositoryPath, "README.md"), []byte("drifted\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := app.New(store, organizergit.New(), nil).Open(context.Background(), request.ProjectID); !errors.Is(err, app.ErrOrganizerDrift) {
		t.Fatalf("Open() dirty drift error = %v", err)
	}
}

func TestAdoptPreviewAndApplyRejectRepeatedGitSuffixWithoutPropagation(t *testing.T) {
	const rejectedRemote = "https://github.com/example/repository.git%2egit"
	root := privateTempDir(t)
	productPath, validRemote, _ := newProductRepository(t, root)
	repositoryPath := filepath.Join(root, "organizer")
	validDocument := configurationJSON("project-adopt-repeated", "Adopt Repeated", productPath, validRemote)
	creator := app.New(newMemoryProjectStore(), organizergit.New(), nil)
	create := createRequest("project-adopt-repeated", "Adopt Repeated", repositoryPath, validDocument)
	createPreview, err := creator.PreviewCreate(context.Background(), create)
	if err != nil || !createPreview.Valid {
		t.Fatalf("seed PreviewCreate() = %#v, %v", createPreview, err)
	}
	if _, err := creator.ApplyCreate(context.Background(), app.ApplyCreateCommand{
		RequestID: create.RequestID, PreviewID: createPreview.ID, Request: create, Confirmation: confirmed(),
	}); err != nil {
		t.Fatalf("seed ApplyCreate() = %v", err)
	}
	rejectedDocument := configurationJSON(
		"project-adopt-repeated", "Adopt Repeated", productPath, rejectedRemote,
	)
	if err := os.WriteFile(filepath.Join(repositoryPath, "paseo-director.json"), rejectedDocument, 0o600); err != nil {
		t.Fatal(err)
	}
	runGit(t, repositoryPath, "add", "--", "paseo-director.json")
	runGit(t, repositoryPath, "commit", "-m", "inject repeated Git suffix")
	before := readProductFacts(t, repositoryPath)

	store := newMemoryProjectStore()
	service := app.New(store, organizergit.New(), nil)
	request := app.AdoptRequest{
		RequestID: "request-adopt-repeated", ProjectID: "project-adopt-repeated",
		ProjectName: "Adopt Repeated", RepositoryPath: repositoryPath,
	}
	preview, err := service.PreviewAdopt(context.Background(), request)
	if err != nil || preview.Valid || preview.ID == "" {
		t.Fatalf("PreviewAdopt() = %#v, %v", preview, err)
	}
	encoded, err := json.Marshal(preview)
	if err != nil || strings.Contains(string(encoded), rejectedRemote) {
		t.Fatalf("PreviewAdopt propagated rejected remote: %s, %v", encoded, err)
	}
	_, err = service.ApplyAdopt(context.Background(), app.ApplyAdoptCommand{
		RequestID: request.RequestID, PreviewID: preview.ID, Request: request, Confirmation: confirmed(),
	})
	if !errors.Is(err, app.ErrPreviewInvalid) || strings.Contains(err.Error(), rejectedRemote) {
		t.Fatalf("ApplyAdopt() repeated suffix error = %v", err)
	}
	if len(store.projects) != 0 || len(store.commands) != 0 {
		t.Fatalf("rejected Adopt reached TaskStore: projects=%#v commands=%#v", store.projects, store.commands)
	}
	if after := readProductFacts(t, repositoryPath); after != before {
		t.Fatalf("rejected Adopt changed Organizer: before=%#v after=%#v", before, after)
	}
}

func TestValidationAndApprovalRejectionHaveNoPartialState(t *testing.T) {
	root := privateTempDir(t)
	productPath, remote, before := newProductRepository(t, root)
	repository := organizergit.New()
	tests := []struct {
		name    string
		request app.CreateRequest
		mutate  func(app.Preview) app.ApplyCreateCommand
	}{
		{
			name:    "invalid configuration",
			request: createRequest("project-invalid", "Invalid", filepath.Join(root, "invalid"), []byte(`{"schemaVersion":2}`)),
		},
		{
			name:    "Organizer overlaps product repository",
			request: createRequest("project-overlap", "Overlap", filepath.Join(productPath, "organizer"), configurationJSON("project-overlap", "Overlap", productPath, remote)),
		},
		{
			name:    "unverified human",
			request: createRequest("project-human", "Human", filepath.Join(root, "human"), configurationJSON("project-human", "Human", productPath, remote)),
			mutate: func(preview app.Preview) app.ApplyCreateCommand {
				return app.ApplyCreateCommand{
					RequestID: "request-project-human", PreviewID: preview.ID,
					Request:      createRequest("project-human", "Human", filepath.Join(root, "human"), configurationJSON("project-human", "Human", productPath, remote)),
					Confirmation: app.HumanConfirmation{ActorID: "model:claim", Confirmed: false},
				}
			},
		},
		{
			name:    "mismatched Preview",
			request: createRequest("project-preview", "Preview", filepath.Join(root, "preview"), configurationJSON("project-preview", "Preview", productPath, remote)),
			mutate: func(preview app.Preview) app.ApplyCreateCommand {
				return app.ApplyCreateCommand{
					RequestID: "request-project-preview", PreviewID: strings.Repeat("0", 64),
					Request:      createRequest("project-preview", "Preview", filepath.Join(root, "preview"), configurationJSON("project-preview", "Preview", productPath, remote)),
					Confirmation: confirmed(),
				}
			},
		},
	}
	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			store := newMemoryProjectStore()
			service := app.New(store, repository, nil)
			preview, err := service.PreviewCreate(context.Background(), testCase.request)
			if err != nil {
				t.Fatalf("PreviewCreate() error = %v", err)
			}
			if testCase.mutate == nil {
				if preview.Valid {
					t.Fatalf("PreviewCreate() unexpectedly valid: %#v", preview)
				}
				_, err = service.ApplyCreate(context.Background(), app.ApplyCreateCommand{
					RequestID: testCase.request.RequestID, PreviewID: preview.ID,
					Request: testCase.request, Confirmation: confirmed(),
				})
				if !errors.Is(err, app.ErrPreviewInvalid) {
					t.Fatalf("ApplyCreate() invalid Preview error = %v", err)
				}
			} else {
				_, err = service.ApplyCreate(context.Background(), testCase.mutate(preview))
				expected := app.ErrHumanApprovalRequired
				if testCase.name == "mismatched Preview" {
					expected = app.ErrPreviewMismatch
				}
				if !errors.Is(err, expected) {
					t.Fatalf("ApplyCreate() error = %v", err)
				}
			}
			if len(store.projects) != 0 {
				t.Fatalf("rejection persisted Projects: %#v", store.projects)
			}
			if testCase.request.RepositoryPath != "" && !strings.HasPrefix(testCase.request.RepositoryPath, productPath+string(filepath.Separator)) {
				if _, err := os.Stat(testCase.request.RepositoryPath); !errors.Is(err, os.ErrNotExist) {
					t.Fatalf("rejection changed Organizer target: %v", err)
				}
			}
		})
	}
	if after := readProductFacts(t, productPath); after != before {
		t.Fatalf("rejections changed product repository: before=%#v after=%#v", before, after)
	}
}

func TestAdoptRejectsDirtyAndMissingReferencesWithoutPartialState(t *testing.T) {
	for _, fault := range []string{"dirty", "missing-reference"} {
		t.Run(fault, func(t *testing.T) {
			root := privateTempDir(t)
			productPath, remote, productBefore := newProductRepository(t, root)
			repositoryPath := filepath.Join(root, "organizer")
			request := createRequest(
				"project-adopt-reject", "Adopt Reject", repositoryPath,
				configurationJSON("project-adopt-reject", "Adopt Reject", productPath, remote),
			)
			creator := app.New(newMemoryProjectStore(), organizergit.New(), nil)
			preview, err := creator.PreviewCreate(context.Background(), request)
			if err != nil || !preview.Valid {
				t.Fatalf("seed PreviewCreate() = %#v, %v", preview, err)
			}
			if _, err := creator.ApplyCreate(context.Background(), app.ApplyCreateCommand{
				RequestID: request.RequestID, PreviewID: preview.ID,
				Request: request, Confirmation: confirmed(),
			}); err != nil {
				t.Fatalf("seed ApplyCreate() = %v", err)
			}
			if fault == "dirty" {
				if err := os.WriteFile(filepath.Join(repositoryPath, "untracked.txt"), []byte("dirty\n"), 0o600); err != nil {
					t.Fatal(err)
				}
			} else {
				runGit(t, repositoryPath, "rm", "--", "skills/commits/SKILL.md")
				runGit(t, repositoryPath, "commit", "-m", "remove required reference")
			}

			store := newMemoryProjectStore()
			service := app.New(store, organizergit.New(), nil)
			adopt := app.AdoptRequest{
				RequestID: "request-adopt-reject", ProjectID: request.ProjectID,
				ProjectName: request.ProjectName, RepositoryPath: repositoryPath,
			}
			adoptPreview, err := service.PreviewAdopt(context.Background(), adopt)
			if err != nil || adoptPreview.Valid {
				t.Fatalf("PreviewAdopt() = %#v, %v", adoptPreview, err)
			}
			if _, err := service.ApplyAdopt(context.Background(), app.ApplyAdoptCommand{
				RequestID: adopt.RequestID, PreviewID: adoptPreview.ID,
				Request: adopt, Confirmation: confirmed(),
			}); !errors.Is(err, app.ErrPreviewInvalid) {
				t.Fatalf("ApplyAdopt() error = %v", err)
			}
			if len(store.projects) != 0 {
				t.Fatalf("rejected Adopt persisted Project state: %#v", store.projects)
			}
			if after := readProductFacts(t, productPath); after != productBefore {
				t.Fatalf("rejected Adopt changed product repository: before=%#v after=%#v", productBefore, after)
			}
		})
	}
}

func TestCreateFailsClosedWhenExternalStateChangesAfterIntentOrCommit(t *testing.T) {
	t.Run("foreign target after durable intent", func(t *testing.T) {
		root := privateTempDir(t)
		productPath, remote, productBefore := newProductRepository(t, root)
		repositoryPath := filepath.Join(root, "organizer")
		request := createRequest(
			"project-target-race", "Target Race", repositoryPath,
			configurationJSON("project-target-race", "Target Race", productPath, remote),
		)
		store := newMemoryProjectStore()
		service := app.New(store, organizergit.New(), func(boundary app.Boundary) error {
			if boundary == app.BoundaryIntentPersisted {
				return errInjectedInterruption
			}
			return nil
		})
		preview, err := service.PreviewCreate(context.Background(), request)
		if err != nil || !preview.Valid {
			t.Fatalf("PreviewCreate() = %#v, %v", preview, err)
		}
		if _, err := service.ApplyCreate(context.Background(), app.ApplyCreateCommand{
			RequestID: request.RequestID, PreviewID: preview.ID,
			Request: request, Confirmation: confirmed(),
		}); !errors.Is(err, errInjectedInterruption) {
			t.Fatalf("ApplyCreate() interruption = %v", err)
		}
		if err := os.Mkdir(repositoryPath, 0o700); err != nil {
			t.Fatal(err)
		}
		foreignPath := filepath.Join(repositoryPath, "foreign.txt")
		if err := os.WriteFile(foreignPath, []byte("foreign\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := app.New(store, organizergit.New(), nil).Recover(context.Background(), request.ProjectID); !errors.Is(err, repoport.ErrTargetConflict) {
			t.Fatalf("Recover() foreign target error = %v", err)
		}
		content, err := os.ReadFile(foreignPath)
		if err != nil || string(content) != "foreign\n" {
			t.Fatalf("foreign target changed: %q, %v", content, err)
		}
		project, _ := store.Project(context.Background(), request.ProjectID)
		if project.State != "paused" || project.Organizer.Phase != domain.OrganizerPhaseIntentRecorded {
			t.Fatalf("foreign target activated partial state: %#v", project)
		}
		if after := readProductFacts(t, productPath); after != productBefore {
			t.Fatalf("target race changed product repository: before=%#v after=%#v", productBefore, after)
		}
	})

	t.Run("guessable matching marker does not admit foreign state", func(t *testing.T) {
		root := privateTempDir(t)
		productPath, remote, productBefore := newProductRepository(t, root)
		repositoryPath := filepath.Join(root, "organizer")
		request := createRequest(
			"project-guessed-marker", "Guessed Marker", repositoryPath,
			configurationJSON("project-guessed-marker", "Guessed Marker", productPath, remote),
		)
		store := newMemoryProjectStore()
		service := app.New(store, organizergit.New(), func(boundary app.Boundary) error {
			if boundary == app.BoundaryIntentPersisted {
				return errInjectedInterruption
			}
			return nil
		})
		preview, err := service.PreviewCreate(context.Background(), request)
		if err != nil || !preview.Valid {
			t.Fatalf("PreviewCreate() = %#v, %v", preview, err)
		}
		if _, err := service.ApplyCreate(context.Background(), app.ApplyCreateCommand{
			RequestID: request.RequestID, PreviewID: preview.ID,
			Request: request, Confirmation: confirmed(),
		}); !errors.Is(err, errInjectedInterruption) {
			t.Fatalf("ApplyCreate() interruption = %v", err)
		}
		metadataPath := filepath.Join(repositoryPath, ".director")
		if err := os.MkdirAll(metadataPath, 0o700); err != nil {
			t.Fatal(err)
		}
		marker := []byte(fmt.Sprintf(
			"{\"schemaVersion\":\"director.organizer/v1\",\"projectId\":%q,\"operationId\":%q,\"configurationSha256\":%q}\n",
			request.ProjectID, request.RequestID, preview.ConfigurationSHA256,
		))
		if err := os.WriteFile(filepath.Join(metadataPath, "organizer.json"), marker, 0o600); err != nil {
			t.Fatal(err)
		}
		foreignPath := filepath.Join(repositoryPath, "foreign.txt")
		if err := os.WriteFile(foreignPath, []byte("foreign\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := app.New(store, organizergit.New(), nil).Recover(context.Background(), request.ProjectID); !errors.Is(err, repoport.ErrTargetConflict) {
			t.Fatalf("Recover() guessed marker error = %v", err)
		}
		for _, unexpected := range []string{"README.md", "paseo-director.json", ".git"} {
			if _, err := os.Stat(filepath.Join(repositoryPath, unexpected)); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("guessed marker admitted foreign target and created %s: %v", unexpected, err)
			}
		}
		content, err := os.ReadFile(foreignPath)
		if err != nil || string(content) != "foreign\n" {
			t.Fatalf("foreign state changed: %q, %v", content, err)
		}
		project, _ := store.Project(context.Background(), request.ProjectID)
		if project.State != "paused" || project.Organizer.Phase != domain.OrganizerPhaseIntentRecorded {
			t.Fatalf("guessed marker advanced durable phase: %#v", project)
		}
		if after := readProductFacts(t, productPath); after != productBefore {
			t.Fatalf("guessed marker changed product repository: before=%#v after=%#v", productBefore, after)
		}
	})

	t.Run("Create requires an exclusively controlled parent", func(t *testing.T) {
		root := privateTempDir(t)
		productPath, remote, productBefore := newProductRepository(t, root)
		if err := os.Chmod(root, 0o777); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = os.Chmod(root, 0o700) })
		repositoryPath := filepath.Join(root, "organizer")
		request := createRequest(
			"project-private-parent", "Private Parent", repositoryPath,
			configurationJSON("project-private-parent", "Private Parent", productPath, remote),
		)
		store := newMemoryProjectStore()
		service := app.New(store, organizergit.New(), nil)
		preview, err := service.PreviewCreate(context.Background(), request)
		if err != nil || preview.Valid {
			t.Fatalf("PreviewCreate() = %#v, %v", preview, err)
		}
		if _, err := service.ApplyCreate(context.Background(), app.ApplyCreateCommand{
			RequestID: request.RequestID, PreviewID: preview.ID,
			Request: request, Confirmation: confirmed(),
		}); !errors.Is(err, app.ErrPreviewMismatch) {
			t.Fatalf("ApplyCreate() private-parent error = %v", err)
		}
		if len(store.projects) != 0 {
			t.Fatalf("private-parent rejection persisted state: %#v", store.projects)
		}
		if after := readProductFacts(t, productPath); after != productBefore {
			t.Fatalf("private-parent rejection changed product repository: before=%#v after=%#v", productBefore, after)
		}
	})

	t.Run("Organizer drift before activation", func(t *testing.T) {
		root := privateTempDir(t)
		productPath, remote, productBefore := newProductRepository(t, root)
		repositoryPath := filepath.Join(root, "organizer")
		request := createRequest(
			"project-activation-drift", "Activation Drift", repositoryPath,
			configurationJSON("project-activation-drift", "Activation Drift", productPath, remote),
		)
		store := newMemoryProjectStore()
		service := app.New(store, organizergit.New(), func(boundary app.Boundary) error {
			if boundary == app.BoundaryRevisionRecorded {
				return errInjectedInterruption
			}
			return nil
		})
		preview, err := service.PreviewCreate(context.Background(), request)
		if err != nil || !preview.Valid {
			t.Fatalf("PreviewCreate() = %#v, %v", preview, err)
		}
		if _, err := service.ApplyCreate(context.Background(), app.ApplyCreateCommand{
			RequestID: request.RequestID, PreviewID: preview.ID,
			Request: request, Confirmation: confirmed(),
		}); !errors.Is(err, errInjectedInterruption) {
			t.Fatalf("ApplyCreate() interruption = %v", err)
		}
		if err := os.WriteFile(filepath.Join(repositoryPath, "README.md"), []byte("drifted before activation\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := app.New(store, organizergit.New(), nil).Recover(context.Background(), request.ProjectID); !errors.Is(err, app.ErrOrganizerDrift) {
			t.Fatalf("Recover() pre-activation drift error = %v", err)
		}
		project, _ := store.Project(context.Background(), request.ProjectID)
		if project.State != "paused" || project.Organizer.Phase != domain.OrganizerPhaseRevisionCommitted {
			t.Fatalf("drift activated partial state: %#v", project)
		}
		if after := readProductFacts(t, productPath); after != productBefore {
			t.Fatalf("activation drift changed product repository: before=%#v after=%#v", productBefore, after)
		}
	})
}
