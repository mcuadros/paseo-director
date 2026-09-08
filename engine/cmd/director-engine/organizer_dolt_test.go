// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/mcuadros/director-engine/adapters/dolt"
	"github.com/mcuadros/director-engine/adapters/organizergit"
	app "github.com/mcuadros/director-engine/application/organizer"
	domainconfig "github.com/mcuadros/director-engine/domain/configuration"
)

type organizerDoltFixture struct {
	address  string
	database string
	root     string
	command  *exec.Cmd
	waited   chan error
	stopped  bool
}

func stopOrganizerDoltFixture(t *testing.T, fixture *organizerDoltFixture) {
	t.Helper()
	if fixture.stopped || fixture.command.Process == nil {
		return
	}
	if err := fixture.command.Process.Signal(syscall.SIGTERM); err != nil {
		t.Fatalf("signal Dolt server: %v", err)
	}
	select {
	case <-fixture.waited:
		fixture.stopped = true
	case <-time.After(10 * time.Second):
		_ = fixture.command.Process.Kill()
		<-fixture.waited
		fixture.stopped = true
	}
}

func organizerDoltEnvironment(root string) []string {
	return append(os.Environ(), "DOLT_ROOT_PATH="+root, "DOLT_DISABLE_EVENT_FLUSH=1")
}

func runOrganizerDolt(t *testing.T, root, directory string, arguments ...string) string {
	t.Helper()
	command := exec.Command("dolt", arguments...)
	command.Dir = directory
	command.Env = organizerDoltEnvironment(root)
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("dolt %s: %v: %s", strings.Join(arguments, " "), err, output)
	}
	return string(output)
}

func startOrganizerDoltFixture(t *testing.T) *organizerDoltFixture {
	t.Helper()
	if runtime.GOOS != "linux" {
		t.Skip("Organizer TaskStore integration supports Linux only")
	}
	if _, err := exec.LookPath("dolt"); err != nil {
		t.Fatalf("Dolt 2.3.2 is required: %v", err)
	}
	root := privateTempDir(t)
	clientRoot := filepath.Join(root, "client")
	if err := os.MkdirAll(filepath.Join(clientRoot, ".dolt"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(
		filepath.Join(clientRoot, ".dolt", "config_global.json"),
		[]byte("{\n  \"metrics.disabled\": \"true\"\n}\n"), 0o600,
	); err != nil {
		t.Fatal(err)
	}
	database := "organizer_flow"
	databaseDirectory := filepath.Join(root, database)
	if err := os.Mkdir(databaseDirectory, 0o700); err != nil {
		t.Fatal(err)
	}
	runOrganizerDolt(t, clientRoot, databaseDirectory,
		"init", "--name=Director organizer", "--email=director@example.invalid",
	)
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := listener.Addr().(*net.TCPAddr).Port
	if err := listener.Close(); err != nil {
		t.Fatal(err)
	}
	configurationPath := filepath.Join(root, "server.yaml")
	configRoot := filepath.Join(root, ".doltcfg")
	if err := os.Mkdir(configRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	configuration := fmt.Sprintf(`log_level: warning
data_dir: %q
cfg_dir: %q
behavior:
  read_only: false
listener:
  host: 127.0.0.1
  port: %d
system_variables:
  dolt_force_transaction_commit: 0
`, root, configRoot, port)
	if err := os.WriteFile(configurationPath, []byte(configuration), 0o600); err != nil {
		t.Fatal(err)
	}
	command := exec.Command("dolt", "sql-server", "--config="+configurationPath)
	command.Dir = root
	command.Env = organizerDoltEnvironment(clientRoot)
	var output strings.Builder
	command.Stdout, command.Stderr = &output, &output
	if err := command.Start(); err != nil {
		t.Fatal(err)
	}
	fixture := &organizerDoltFixture{
		address: fmt.Sprintf("127.0.0.1:%d", port), database: database,
		root: root, command: command, waited: make(chan error, 1),
	}
	go func() { fixture.waited <- command.Wait() }()
	t.Cleanup(func() { stopOrganizerDoltFixture(t, fixture) })
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		connection, err := net.DialTimeout("tcp", fixture.address, 100*time.Millisecond)
		if err == nil {
			_ = connection.Close()
			return fixture
		}
		select {
		case err := <-fixture.waited:
			fixture.stopped = true
			t.Fatalf("Dolt exited before readiness: %v: %s", err, output.String())
		default:
		}
		time.Sleep(25 * time.Millisecond)
	}
	t.Fatalf("Dolt did not listen: %s", output.String())
	return nil
}

func openOrganizerStore(t *testing.T, fixture *organizerDoltFixture, bootstrap bool) *dolt.DoltTaskStore {
	t.Helper()
	endpoint := dolt.Endpoint{Address: fixture.address, Database: fixture.database, User: "root"}
	store, err := dolt.Open(dolt.Config{Control: endpoint, Writer: endpoint, StoreID: "organizer-flow-store"})
	if err != nil {
		t.Fatal(err)
	}
	if bootstrap {
		if err := store.Bootstrap(context.Background()); err != nil {
			_ = store.Close()
			t.Fatal(err)
		}
	}
	return store
}

func runOrganizerGit(t *testing.T, directory string, arguments ...string) string {
	t.Helper()
	command := exec.Command("git", arguments...)
	command.Dir = directory
	command.Env = append(os.Environ(),
		"GIT_CONFIG_NOSYSTEM=1",
		"GIT_CONFIG_GLOBAL=/dev/null",
		"GIT_AUTHOR_NAME=Director contract",
		"GIT_AUTHOR_EMAIL=director@example.invalid",
		"GIT_COMMITTER_NAME=Director contract",
		"GIT_COMMITTER_EMAIL=director@example.invalid",
	)
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s: %v: %s", strings.Join(arguments, " "), err, output)
	}
	return strings.TrimSpace(string(output))
}

func organizerConfiguration(projectID, projectName, workspacePath, remote string) []byte {
	return []byte(fmt.Sprintf(`{
  "$schema": %q,
  "schemaVersion": 1,
  "project": {"id": %q, "name": %q},
  "workspaces": [{"id":"product","remote":%q,"sourcePath":%q,"defaultBaseBranch":"main"}],
  "agentProfiles": {
    "taskAgent": {"provider":"codex","model":"gpt-5.6","effort":"high","permissionMode":"workspace-write"},
    "reviewerAgent": {"provider":"opencode","model":"reviewer-1","effort":"high","permissionMode":"read-only"}
  },
  "defaults": {
    "launchPolicy":"manual","deliveryMode":"pull_request",
    "limits":{"maxActiveTasks":4,"maxActiveTasksPerWorkspace":2,"maxConcurrentAgents":8,"maxSubagentsPerTask":3},
    "runBudget":{"elapsedSeconds":7200,"tokens":200000,"turns":32,"ciCycles":4}
  },
  "workspaceOverrides": [],
  "skills": [{"id":"commits","path":"skills/commits/SKILL.md"}],
  "templates": [{"id":"task","path":"templates/task.md"}]
}`, domainconfig.SchemaID, projectID, projectName, remote, filepath.ToSlash(workspacePath)))
}

func makeProductRepository(t *testing.T, root, name string) (string, string, string) {
	t.Helper()
	path := filepath.Join(root, name)
	if err := os.Mkdir(path, 0o700); err != nil {
		t.Fatal(err)
	}
	runOrganizerGit(t, path, "init", "--initial-branch=main")
	remote := "https://github.com/example/" + name + ".git"
	runOrganizerGit(t, path, "remote", "add", "origin", remote)
	if err := os.WriteFile(filepath.Join(path, "source.txt"), []byte("unchanged\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	runOrganizerGit(t, path, "add", "--", "source.txt")
	runOrganizerGit(t, path, "commit", "-m", "product baseline")
	return path, remote, runOrganizerGit(t, path, "rev-parse", "HEAD^{tree}")
}

func seedAdoptRepository(t *testing.T, path string, configuration []byte) string {
	t.Helper()
	if err := os.Mkdir(path, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(path, "paseo-director.json"), configuration, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(path, "README.md"), []byte("# Existing Organizer\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(path, "skills", "commits"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(path, "templates"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(path, "skills", "commits", "SKILL.md"), []byte("# Commits\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(path, "templates", "task.md"), []byte("# Task\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	runOrganizerGit(t, path, "init", "--initial-branch=main")
	runOrganizerGit(t, path, "add", "--", "README.md", "paseo-director.json", "skills/commits/SKILL.md", "templates/task.md")
	runOrganizerGit(t, path, "commit", "-m", "existing Organizer")
	return runOrganizerGit(t, path, "rev-parse", "HEAD")
}

func TestOrganizerCreateAndAdoptPersistAcrossTaskStoreReopen(t *testing.T) {
	fixture := startOrganizerDoltFixture(t)
	store := openOrganizerStore(t, fixture, true)
	repository := organizergit.New()
	root := privateTempDir(t)
	productPath, productRemote, productTree := makeProductRepository(t, root, "product-create")
	createPath := filepath.Join(root, "organizer-create")
	createRequest := app.CreateRequest{
		RequestID: "request-create-dolt",
		ProjectID: "project-create-dolt", ProjectName: "Create with Dolt",
		RepositoryPath:    createPath,
		ConfigurationJSON: organizerConfiguration("project-create-dolt", "Create with Dolt", productPath, productRemote),
	}
	interrupted := false
	service := app.New(store, repository, func(boundary app.Boundary) error {
		if boundary == app.BoundaryRevisionCommitted && !interrupted {
			interrupted = true
			return errInjectedOrganizerInterruption
		}
		return nil
	})
	preview, err := service.PreviewCreate(context.Background(), createRequest)
	if err != nil || !preview.Valid {
		t.Fatalf("PreviewCreate() = %#v, %v", preview, err)
	}
	_, err = service.ApplyCreate(context.Background(), app.ApplyCreateCommand{
		RequestID: createRequest.RequestID, PreviewID: preview.ID, Request: createRequest,
		Confirmation: app.HumanConfirmation{ActorID: "human:owner", Confirmed: true},
	})
	if !errors.Is(err, errInjectedOrganizerInterruption) {
		t.Fatalf("ApplyCreate() interruption = %v", err)
	}
	if err := store.Close(); err != nil {
		t.Fatalf("close interrupted TaskStore: %v", err)
	}

	store = openOrganizerStore(t, fixture, false)
	recovered, err := app.New(store, repository, nil).Recover(context.Background(), createRequest.ProjectID)
	if err != nil || recovered.State != "active" {
		t.Fatalf("Recover() = %#v, %v", recovered, err)
	}
	if runOrganizerGit(t, createPath, "rev-list", "--count", "HEAD") != "1" {
		t.Fatal("recovery duplicated the initial Organizer commit")
	}
	createReplay, err := app.New(store, repository, nil).ApplyCreate(context.Background(), app.ApplyCreateCommand{
		RequestID: createRequest.RequestID, PreviewID: preview.ID, Request: createRequest,
		Confirmation: app.HumanConfirmation{ActorID: "human:owner", Confirmed: true},
	})
	if err != nil || createReplay != recovered {
		t.Fatalf("ApplyCreate() durable replay = %#v, %v; want %#v", createReplay, err, recovered)
	}
	if runOrganizerGit(t, productPath, "rev-parse", "HEAD^{tree}") != productTree {
		t.Fatal("Create flow changed the product repository")
	}

	adoptProductPath, adoptRemote, adoptTree := makeProductRepository(t, root, "product-adopt")
	adoptPath := filepath.Join(root, "organizer-adopt")
	adoptDocument := organizerConfiguration("project-adopt-dolt", "Adopt with Dolt", adoptProductPath, adoptRemote)
	adoptRevision := seedAdoptRepository(t, adoptPath, adoptDocument)
	adoptRequest := app.AdoptRequest{
		RequestID: "request-adopt-dolt",
		ProjectID: "project-adopt-dolt", ProjectName: "Adopt with Dolt", RepositoryPath: adoptPath,
	}
	adoptPreview, err := app.New(store, repository, nil).PreviewAdopt(context.Background(), adoptRequest)
	if err != nil || !adoptPreview.Valid || adoptPreview.OrganizerRevision != adoptRevision {
		t.Fatalf("PreviewAdopt() = %#v, %v", adoptPreview, err)
	}
	adopted, err := app.New(store, repository, nil).ApplyAdopt(context.Background(), app.ApplyAdoptCommand{
		RequestID: adoptRequest.RequestID, PreviewID: adoptPreview.ID, Request: adoptRequest,
		Confirmation: app.HumanConfirmation{ActorID: "human:owner", Confirmed: true},
	})
	if err != nil || adopted.State != "active" {
		t.Fatalf("ApplyAdopt() = %#v, %v", adopted, err)
	}
	if err := store.Close(); err != nil {
		t.Fatalf("close adopted TaskStore: %v", err)
	}
	store = openOrganizerStore(t, fixture, false)
	defer store.Close()
	if reopened, err := app.New(store, repository, nil).Open(context.Background(), adoptRequest.ProjectID); err != nil || reopened.OrganizerRevision != adoptRevision {
		t.Fatalf("Open(adopted) = %#v, %v", reopened, err)
	}
	adoptReplay, err := app.New(store, repository, nil).ApplyAdopt(context.Background(), app.ApplyAdoptCommand{
		RequestID: adoptRequest.RequestID, PreviewID: adoptPreview.ID, Request: adoptRequest,
		Confirmation: app.HumanConfirmation{ActorID: "human:owner", Confirmed: true},
	})
	if err != nil || adoptReplay != adopted {
		t.Fatalf("ApplyAdopt() durable replay = %#v, %v; want %#v", adoptReplay, err, adopted)
	}
	if runOrganizerGit(t, adoptPath, "rev-parse", "HEAD") != adoptRevision ||
		runOrganizerGit(t, adoptProductPath, "rev-parse", "HEAD^{tree}") != adoptTree {
		t.Fatal("Adopt flow mutated the Organizer or product repository")
	}
}

var errInjectedOrganizerInterruption = errors.New("injected Organizer interruption")
