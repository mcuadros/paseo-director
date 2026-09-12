// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/mcuadros/director-engine/adapters/diagnostics"
	"github.com/mcuadros/director-engine/adapters/dolt"
	"github.com/mcuadros/director-engine/application/board"
	"github.com/mcuadros/director-engine/domain"
	"github.com/mcuadros/director-engine/domain/execution"
	"github.com/mcuadros/director-engine/domain/jsondocument"
	repositorydomain "github.com/mcuadros/director-engine/domain/repository"
	"github.com/mcuadros/director-engine/domain/safedata"
	"github.com/mcuadros/director-engine/domain/scheduling"
	domainmaintenance "github.com/mcuadros/director-engine/domain/taskstoremaintenance"
	planningport "github.com/mcuadros/director-engine/ports/planning"
)

const (
	releaseScaleWorkspaces       = 25
	releaseScaleOpenTasks        = 500
	releaseScaleHistoricalTasks  = 10_000
	releaseScaleConcurrentAgents = 8
	releaseScaleBatchSize        = 100
)

type doltFixture struct {
	address  string
	database string
	root     string
	command  *exec.Cmd
	waited   chan error
	stopped  bool
}

func stopDoltFixture(t *testing.T, fixture *doltFixture, signal os.Signal) {
	t.Helper()
	if fixture.stopped || fixture.command.Process == nil {
		return
	}
	if err := fixture.command.Process.Signal(signal); err != nil {
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

func reserveDoltPort(t *testing.T) int {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("reserve Dolt port: %v", err)
	}
	port := listener.Addr().(*net.TCPAddr).Port
	if err := listener.Close(); err != nil {
		t.Fatalf("release Dolt port: %v", err)
	}
	return port
}

func doltEnvironment(clientRoot string) []string {
	return append(os.Environ(), "DOLT_ROOT_PATH="+clientRoot, "DOLT_DISABLE_EVENT_FLUSH=1")
}

func runDoltCommand(t *testing.T, clientRoot, directory string, arguments ...string) string {
	t.Helper()
	command := exec.Command("dolt", arguments...)
	command.Dir, command.Env = directory, doltEnvironment(clientRoot)
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("dolt %s failed: %v: %s", strings.Join(arguments, " "), err, output)
	}
	return string(output)
}

func startDoltFixture(t *testing.T) *doltFixture {
	t.Helper()
	if runtime.GOOS != "linux" {
		t.Skip("the selected TaskStore topology supports Linux only")
	}
	if _, err := exec.LookPath("dolt"); err != nil {
		t.Fatalf("Dolt 2.3.2 is required: %v", err)
	}
	versionRoot := t.TempDir()
	versionConfig := filepath.Join(versionRoot, ".dolt")
	if err := os.Mkdir(versionConfig, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(versionConfig, "config_global.json"), []byte("{\n  \"metrics.disabled\": \"true\"\n}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if version := runDoltCommand(t, versionRoot, ".", "version"); !strings.Contains(version, "dolt version 2.3.2") {
		t.Fatalf("TaskStore contract requires Dolt 2.3.2, observed %q", strings.TrimSpace(version))
	}

	root := t.TempDir()
	clientRoot := filepath.Join(root, "client")
	configDirectory := filepath.Join(clientRoot, ".dolt")
	if err := os.MkdirAll(configDirectory, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(configDirectory, "config_global.json"), []byte("{\n  \"metrics.disabled\": \"true\"\n}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	database := "release_scale"
	databaseDirectory := filepath.Join(root, database)
	if err := os.Mkdir(databaseDirectory, 0o700); err != nil {
		t.Fatal(err)
	}
	runDoltCommand(t, clientRoot, databaseDirectory, "init", "--name=Director scale", "--email=scale@example.invalid")
	port := reserveDoltPort(t)
	configuration := filepath.Join(root, "server.yaml")
	configRoot := filepath.Join(root, ".doltcfg")
	if err := os.Mkdir(configRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	serverConfig := fmt.Sprintf(`log_level: warning
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
	if err := os.WriteFile(configuration, []byte(serverConfig), 0o600); err != nil {
		t.Fatal(err)
	}
	command := exec.Command("dolt", "sql-server", "--config="+configuration)
	command.Dir, command.Env = root, doltEnvironment(clientRoot)
	var output strings.Builder
	command.Stdout, command.Stderr = &output, &output
	if err := command.Start(); err != nil {
		t.Fatalf("start Dolt server: %v", err)
	}
	waited := make(chan error, 1)
	go func() { waited <- command.Wait() }()
	fixture := &doltFixture{address: fmt.Sprintf("127.0.0.1:%d", port), database: database, root: root, command: command, waited: waited}
	t.Cleanup(func() { stopDoltFixture(t, fixture, syscall.SIGTERM) })
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		connection, err := net.DialTimeout("tcp", fixture.address, 100*time.Millisecond)
		if err == nil {
			_ = connection.Close()
			return fixture
		}
		select {
		case err := <-waited:
			fixture.stopped = true
			t.Fatalf("Dolt server exited before readiness: %v: %s", err, output.String())
		default:
		}
		time.Sleep(25 * time.Millisecond)
	}
	t.Fatalf("Dolt server did not listen: %s", output.String())
	return nil
}

func fixtureConfig(fixture *doltFixture, storeID string) dolt.Config {
	endpoint := dolt.Endpoint{Address: fixture.address, Database: fixture.database, User: "root"}
	return dolt.Config{Control: endpoint, Writer: endpoint, StoreID: storeID}
}

func openContractStore(t *testing.T, fixture *doltFixture, storeID string, bootstrap bool) *dolt.DoltTaskStore {
	t.Helper()
	store, err := dolt.Open(fixtureConfig(fixture, storeID))
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

func command(key, commandType, aggregateID string, expectedVersion uint64, payload string) domain.CommandRequest {
	return domain.CommandRequest{IdempotencyKey: key, Type: commandType, AggregateID: aggregateID,
		ExpectedVersion: expectedVersion, Payload: json.RawMessage(payload)}
}

func event(id, runID string, sequence uint64, aggregateID string, version uint64, eventType string) domain.Event {
	return domain.Event{ID: id, RunID: runID, Sequence: sequence, AggregateID: aggregateID,
		AggregateVersion: version, Type: eventType, Payload: json.RawMessage(`{"source":"release-scale"}`)}
}

func requireApplied(t *testing.T, result domain.CommandResult, err error) {
	t.Helper()
	if err != nil || result.Outcome != domain.CommandApplied || result.Replay || result.EventID == "" {
		t.Fatalf("unexpected applied result: %#v, %v", result, err)
	}
}

func runDoltServerSQL(t *testing.T, fixture *doltFixture, query string) string {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	command := exec.CommandContext(ctx, "dolt", "sql")
	command.Dir = filepath.Join(fixture.root, fixture.database)
	command.Env, command.Stdin = doltEnvironment(filepath.Join(fixture.root, "client")), strings.NewReader(query)
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("run test-only Dolt SQL fixture command: %v: %s", err, output)
	}
	return strings.TrimSpace(string(output))
}

func maintenanceForFixture(t *testing.T, store *dolt.DoltTaskStore, root string) *dolt.Maintenance {
	t.Helper()
	logs, err := diagnostics.NewLogStore(filepath.Join(root, "logs"))
	if err != nil {
		t.Fatal(err)
	}
	maintenance, err := dolt.NewMaintenance(store, filepath.Join(root, "maintenance"), logs)
	if err != nil {
		t.Fatal(err)
	}
	return maintenance
}

type releaseScaleThresholds struct {
	SeedMilliseconds             float64 `json:"seedMilliseconds"`
	QueryP95Milliseconds         float64 `json:"queryP95Milliseconds"`
	QueryMaximumMilliseconds     float64 `json:"queryMaximumMilliseconds"`
	ConcurrentBatchMilliseconds  float64 `json:"concurrentBatchMilliseconds"`
	StoreObservationMilliseconds float64 `json:"storeObservationMilliseconds"`
	BackupCreateMilliseconds     float64 `json:"backupCreateMilliseconds"`
	BackupValidateMilliseconds   float64 `json:"backupValidateMilliseconds"`
	RestartRecoveryMilliseconds  float64 `json:"restartRecoveryMilliseconds"`
	SelfPeakRSSBytes             uint64  `json:"selfPeakRssBytes"`
	DoltPeakRSSBytes             uint64  `json:"doltPeakRssBytes"`
	SelfPeakFileDescriptors      uint64  `json:"selfPeakFileDescriptors"`
	DoltPeakFileDescriptors      uint64  `json:"doltPeakFileDescriptors"`
	SelfPeakGoroutines           uint64  `json:"selfPeakGoroutines"`
	DoltPeakThreads              uint64  `json:"doltPeakThreads"`
	FixturePeakDiskBytes         uint64  `json:"fixturePeakDiskBytes"`
}

type releaseScaleLatency struct {
	SamplesMilliseconds []float64 `json:"samplesMilliseconds"`
	MinimumMilliseconds float64   `json:"minimumMilliseconds"`
	MedianMilliseconds  float64   `json:"medianMilliseconds"`
	P95Milliseconds     float64   `json:"p95Milliseconds"`
	MaximumMilliseconds float64   `json:"maximumMilliseconds"`
	MeanMilliseconds    float64   `json:"meanMilliseconds"`
	StdDevMilliseconds  float64   `json:"stdDevMilliseconds"`
	CoefficientVariance float64   `json:"coefficientOfVariation"`
}

type releaseScaleProcessPeak struct {
	RSSBytes        uint64 `json:"rssBytes"`
	FileDescriptors uint64 `json:"fileDescriptors"`
	Threads         uint64 `json:"threads"`
}

type releaseScaleResourcePeaks struct {
	Self             releaseScaleProcessPeak `json:"self"`
	Dolt             releaseScaleProcessPeak `json:"dolt"`
	SelfGoroutines   uint64                  `json:"selfGoroutines"`
	FixtureDiskBytes uint64                  `json:"fixtureDiskBytes"`
}

type releaseScaleEvidence struct {
	SchemaVersion string `json:"schemaVersion"`
	TaskID        string `json:"taskId"`
	CandidateSHA  string `json:"candidateSha"`
	BaseSHA       string `json:"baseSha"`
	TreeSHA       string `json:"treeSha"`
	CapturedAt    string `json:"capturedAt"`
	Verdict       string `json:"verdict"`
	Host          struct {
		OperatingSystem       string `json:"operatingSystem"`
		Kernel                string `json:"kernel"`
		Architecture          string `json:"architecture"`
		CPUModel              string `json:"cpuModel"`
		LogicalCPUs           int    `json:"logicalCpus"`
		MemoryTotalBytes      uint64 `json:"memoryTotalBytes"`
		MemoryAvailableBytes  uint64 `json:"memoryAvailableBytes"`
		TemporaryFreeBytes    uint64 `json:"temporaryFreeBytes"`
		InitialLoadCentiUnits uint64 `json:"initialLoadCentiUnits"`
	} `json:"host"`
	Software map[string]string `json:"software"`
	Topology struct {
		TaskStore              string `json:"taskStore"`
		Git                    string `json:"git"`
		Paseo                  string `json:"paseo"`
		SQLControlPoolMaximum  int    `json:"sqlControlPoolMaximum"`
		SQLWriterPoolMaximum   int    `json:"sqlWriterPoolMaximum"`
		TaskAgentLaneMaximum   uint64 `json:"taskAgentLaneMaximum"`
		ReviewerLaneMaximum    uint64 `json:"reviewerLaneMaximum"`
		ConcurrentAgentMaximum uint64 `json:"concurrentAgentMaximum"`
		ConcurrentLoadRequests int    `json:"concurrentLoadRequests"`
		QueryPageMaximum       int    `json:"queryPageMaximum"`
		BoardInitialRenderRows int    `json:"boardInitialRenderRows"`
		ListInitialRenderRows  int    `json:"listInitialRenderRows"`
	} `json:"topology"`
	Scale struct {
		Projects               int `json:"projects"`
		Workspaces             int `json:"workspaces"`
		OpenTasks              int `json:"openTasks"`
		HistoricalTasks        int `json:"historicalTasks"`
		AggregateRecords       int `json:"aggregateRecords"`
		CommandRecords         int `json:"commandRecords"`
		EventRecords           int `json:"eventRecords"`
		CommandOutcomeRecords  int `json:"commandOutcomeRecords"`
		AggregateIdentityFacts int `json:"aggregateIdentityFacts"`
	} `json:"scale"`
	Thresholds   releaseScaleThresholds `json:"thresholds"`
	Measurements struct {
		SeedMilliseconds             float64                   `json:"seedMilliseconds"`
		OpenBoard                    releaseScaleLatency       `json:"openBoard"`
		HistoricalList               releaseScaleLatency       `json:"historicalList"`
		ConcurrentBatchMilliseconds  float64                   `json:"concurrentBatchMilliseconds"`
		ConcurrentRequest            releaseScaleLatency       `json:"concurrentRequest"`
		StoreObservationMilliseconds float64                   `json:"storeObservationMilliseconds"`
		BackupCreateMilliseconds     float64                   `json:"backupCreateMilliseconds"`
		BackupValidateMilliseconds   float64                   `json:"backupValidateMilliseconds"`
		RestartRecoveryMilliseconds  float64                   `json:"restartRecoveryMilliseconds"`
		BoardResponseBytes           int                       `json:"boardResponseBytes"`
		ListResponseBytes            int                       `json:"listResponseBytes"`
		FirstPageRows                int                       `json:"firstPageRows"`
		SecondPageRows               int                       `json:"secondPageRows"`
		ResourcePeaks                releaseScaleResourcePeaks `json:"resourcePeaks"`
	} `json:"measurements"`
	Verification struct {
		PageCursorsStable             bool `json:"pageCursorsStable"`
		NoDuplicateSecondPageRows     bool `json:"noDuplicateSecondPageRows"`
		EightConcurrentRequestsPassed bool `json:"eightConcurrentRequestsPassed"`
		GlobalAgentCapacityEight      bool `json:"globalAgentCapacityEight"`
		SeventhTaskAgentRefused       bool `json:"seventhTaskAgentRefused"`
		FreshRestoreFingerprintExact  bool `json:"freshRestoreFingerprintExact"`
		RestartFingerprintExact       bool `json:"restartFingerprintExact"`
		PostRestartQueryPassed        bool `json:"postRestartQueryPassed"`
		BackupExpiredExactly          bool `json:"backupExpiredExactly"`
		SecretCanaryClassified        bool `json:"secretCanaryClassified"`
		SecretCanaryAbsent            bool `json:"secretCanaryAbsent"`
		DisposableResourcesRemoved    bool `json:"disposableResourcesRemoved"`
	} `json:"verification"`
	Failures []string `json:"failures"`
}

func releaseScaleLimits() releaseScaleThresholds {
	return releaseScaleThresholds{
		SeedMilliseconds: 180_000, QueryP95Milliseconds: 5_000, QueryMaximumMilliseconds: 7_500,
		ConcurrentBatchMilliseconds: 30_000, StoreObservationMilliseconds: 45_000,
		BackupCreateMilliseconds: 90_000, BackupValidateMilliseconds: 180_000,
		RestartRecoveryMilliseconds: 30_000, SelfPeakRSSBytes: 1 << 30, DoltPeakRSSBytes: 2 << 30,
		SelfPeakFileDescriptors: 256, DoltPeakFileDescriptors: 256,
		SelfPeakGoroutines: 160, DoltPeakThreads: 256, FixturePeakDiskBytes: 3 << 30,
	}
}

func releaseScaleCommandOutput(t *testing.T, name string, arguments ...string) string {
	t.Helper()
	command := exec.Command(name, arguments...)
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("run %s: %v", name, err)
	}
	return strings.TrimSpace(string(output))
}

func releaseScaleHost(evidence *releaseScaleEvidence) {
	evidence.Host.OperatingSystem = strings.TrimSpace(releaseScaleOSRelease())
	evidence.Host.Kernel = strings.TrimSpace(releaseScaleRead("/proc/sys/kernel/osrelease"))
	evidence.Host.Architecture = runtime.GOARCH
	evidence.Host.CPUModel = releaseScaleCPUModel()
	evidence.Host.LogicalCPUs = runtime.NumCPU()
	evidence.Host.MemoryTotalBytes, evidence.Host.MemoryAvailableBytes = releaseScaleMemory()
	var filesystem syscall.Statfs_t
	if syscall.Statfs("/tmp", &filesystem) == nil {
		evidence.Host.TemporaryFreeBytes = filesystem.Bavail * uint64(filesystem.Bsize)
	}
	fields := strings.Fields(releaseScaleRead("/proc/loadavg"))
	if len(fields) > 0 {
		if load, err := strconv.ParseFloat(fields[0], 64); err == nil && load >= 0 {
			evidence.Host.InitialLoadCentiUnits = uint64(math.Round(load * 100))
		}
	}
}

func releaseScaleRead(path string) string {
	value, _ := os.ReadFile(path)
	return string(value)
}

func releaseScaleOSRelease() string {
	for _, line := range strings.Split(releaseScaleRead("/etc/os-release"), "\n") {
		if strings.HasPrefix(line, "PRETTY_NAME=") {
			return strings.Trim(strings.TrimPrefix(line, "PRETTY_NAME="), `"`)
		}
	}
	return "unknown"
}

func releaseScaleCPUModel() string {
	for _, line := range strings.Split(releaseScaleRead("/proc/cpuinfo"), "\n") {
		if key, value, found := strings.Cut(line, ":"); found && strings.TrimSpace(key) == "model name" {
			return strings.TrimSpace(value)
		}
	}
	return "unknown"
}

func releaseScaleMemory() (uint64, uint64) {
	var total, available uint64
	for _, line := range strings.Split(releaseScaleRead("/proc/meminfo"), "\n") {
		fields := strings.Fields(line)
		if len(fields) < 2 {
			continue
		}
		value, _ := strconv.ParseUint(fields[1], 10, 64)
		switch fields[0] {
		case "MemTotal:":
			total = value * 1024
		case "MemAvailable:":
			available = value * 1024
		}
	}
	return total, available
}

func releaseScaleRepository(t *testing.T, root, projectID string, index int) domain.Workspace {
	t.Helper()
	key := fmt.Sprintf("workspace-%02d", index+1)
	source := filepath.Join(root, key)
	if err := os.MkdirAll(source, 0o700); err != nil {
		t.Fatal(err)
	}
	commands := [][]string{
		{"init", "--initial-branch=main", source},
		{"-C", source, "-c", "user.name=Director scale", "-c", "user.email=scale@example.invalid", "commit", "--allow-empty", "-m", "scale fixture"},
		{"-C", source, "remote", "add", "origin", fmt.Sprintf("https://example.invalid/release-scale-%02d.git", index+1)},
	}
	for _, arguments := range commands {
		command := exec.Command("git", arguments...)
		if output, err := command.CombinedOutput(); err != nil {
			t.Fatalf("create disposable Git repository %d: %v: %s", index+1, err, output)
		}
	}
	remote, err := repositorydomain.CanonicalRemote(fmt.Sprintf("https://example.invalid/release-scale-%02d.git", index+1))
	if err != nil {
		t.Fatal(err)
	}
	sourceInfo, err := os.Stat(source)
	if err != nil {
		t.Fatal(err)
	}
	common := filepath.Join(source, ".git")
	commonInfo, err := os.Stat(common)
	if err != nil {
		t.Fatal(err)
	}
	sourceStat := sourceInfo.Sys().(*syscall.Stat_t)
	commonStat := commonInfo.Sys().(*syscall.Stat_t)
	return domain.Workspace{
		ID: domain.WorkspaceID(projectID, key), ProjectID: projectID, Key: key, Name: "Scale " + key,
		Repository: domain.RepositoryIdentity{
			ID: remote.ID, Key: remote.Key, CanonicalRemote: remote.Canonical, SourcePath: source,
			SourceDevice: uint64(sourceStat.Dev), SourceInode: sourceStat.Ino,
			GitCommonDirectory: common, GitCommonDevice: uint64(commonStat.Dev), GitCommonInode: commonStat.Ino,
		},
		DefaultBaseBranch: "main", Policy: domain.WorkspacePolicy{LaunchPolicy: "inherit", DeliveryMode: "inherit"},
	}
}

type releaseScaleTaskRecord struct {
	ID           string
	Data         []byte
	CommandID    string
	CommandHash  string
	CommandData  []byte
	EventID      string
	EventData    []byte
	GlobalNumber uint64
}

func releaseScaleTask(t *testing.T, projectID string, workspaces []domain.Workspace, index int) releaseScaleTaskRecord {
	t.Helper()
	historical := index >= releaseScaleOpenTasks
	prefix, number := "open", index
	if historical {
		prefix, number = "history", index-releaseScaleOpenTasks
	}
	id := fmt.Sprintf("scale-%s-%05d", prefix, number+1)
	data, err := json.Marshal(struct {
		Key                string          `json:"key"`
		Title              string          `json:"title"`
		Objective          string          `json:"objective"`
		AcceptanceCriteria string          `json:"acceptanceCriteria"`
		WorkspaceIDs       []string        `json:"workspaceIds"`
		Complete           bool            `json:"complete"`
		Priority           domain.Priority `json:"priority"`
		Labels             []string        `json:"labels"`
		QueuedAtUnixMillis int64           `json:"queuedAtUnixMillis"`
	}{
		Key: id, Title: strings.Title(prefix) + " scale Task " + strconv.Itoa(number+1),
		Objective:          "Validate bounded Linux release-scale behavior",
		AcceptanceCriteria: "Remain queryable through the typed planning contract",
		WorkspaceIDs:       []string{workspaces[index%len(workspaces)].ID}, Complete: historical,
		Priority: domain.PriorityNormal, Labels: []string{"scale-" + prefix},
		QueuedAtUnixMillis: 1_700_000_000_000 + int64(index),
	})
	if err != nil {
		t.Fatal(err)
	}
	payload := json.RawMessage(`{"source":"release-scale-fixture"}`)
	commandID := "command-" + id
	commandDocument, err := json.Marshal(struct {
		SchemaVersion   int             `json:"schemaVersion"`
		Type            string          `json:"type"`
		AggregateID     string          `json:"aggregateId"`
		ExpectedVersion uint64          `json:"expectedVersion"`
		Payload         json.RawMessage `json:"payload"`
	}{1, "task.create", id, 0, payload})
	if err != nil {
		t.Fatal(err)
	}
	canonical, err := jsondocument.CanonicalWithNormalizedNumbers(commandDocument)
	if err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(canonical)
	return releaseScaleTaskRecord{
		ID: id, Data: data, CommandID: commandID, CommandHash: hex.EncodeToString(digest[:]), CommandData: payload,
		EventID: "event-" + id, EventData: payload, GlobalNumber: uint64(index + 2),
	}
}

func releaseScaleInsert(t *testing.T, fixture *doltFixture, projectID string, records []releaseScaleTaskRecord) {
	t.Helper()
	for offset := 0; offset < len(records); offset += releaseScaleBatchSize {
		end := min(offset+releaseScaleBatchSize, len(records))
		batch := records[offset:end]
		var statements strings.Builder
		statements.WriteString("START TRANSACTION;\nINSERT INTO aggregates (id,kind,parent_id,version,data) VALUES ")
		aggregateRows := make([][]any, 0, len(batch))
		for _, record := range batch {
			aggregateRows = append(aggregateRows, []any{record.ID, "task", projectID, uint64(0), record.Data})
		}
		statements.WriteString(releaseScaleSQLRows(aggregateRows))
		statements.WriteString(";\nINSERT INTO command_requests (idempotency_key,command_type,aggregate_id,expected_version,request_hash,payload) VALUES ")
		commandRows := make([][]any, 0, len(batch))
		for _, record := range batch {
			commandRows = append(commandRows, []any{record.CommandID, "task.create", record.ID, uint64(0), record.CommandHash, record.CommandData})
		}
		statements.WriteString(releaseScaleSQLRows(commandRows))
		statements.WriteString(";\nINSERT INTO events (global_sequence,event_id,run_id,sequence,aggregate_id,aggregate_version,event_type,payload) VALUES ")
		eventRows := make([][]any, 0, len(batch))
		for _, record := range batch {
			eventRows = append(eventRows, []any{record.GlobalNumber, record.EventID, nil, uint64(1), record.ID, uint64(0), "task.created", record.EventData})
		}
		statements.WriteString(releaseScaleSQLRows(eventRows))
		statements.WriteString(";\nINSERT INTO command_outcomes (idempotency_key,outcome_type,observed_version,event_id) VALUES ")
		outcomeRows := make([][]any, 0, len(batch))
		for _, record := range batch {
			outcomeRows = append(outcomeRows, []any{record.CommandID, string(domain.CommandApplied), uint64(0), record.EventID})
		}
		statements.WriteString(releaseScaleSQLRows(outcomeRows))
		fmt.Fprintf(&statements, ";\nUPDATE event_stream_lock SET next_sequence=%d WHERE singleton=1;\nCOMMIT;\n", batch[len(batch)-1].GlobalNumber)
		runDoltServerSQL(t, fixture, statements.String())
	}
}

func releaseScaleSQLRows(rows [][]any) string {
	values := make([]string, 0, len(rows))
	for _, row := range rows {
		columns := make([]string, 0, len(row))
		for _, value := range row {
			columns = append(columns, releaseScaleSQLLiteral(value))
		}
		values = append(values, "("+strings.Join(columns, ",")+")")
	}
	return strings.Join(values, ",")
}

func releaseScaleSQLLiteral(value any) string {
	switch current := value.(type) {
	case nil:
		return "NULL"
	case uint64:
		return strconv.FormatUint(current, 10)
	case string:
		return "X'" + hex.EncodeToString([]byte(current)) + "'"
	case []byte:
		return "X'" + hex.EncodeToString(current) + "'"
	default:
		panic(fmt.Sprintf("unsupported release-scale SQL fixture type %T", value))
	}
}

func releaseScaleCount(t *testing.T, fixture *doltFixture, query string) int {
	t.Helper()
	output := runDoltServerSQL(t, fixture, query+";")
	lines := strings.Split(output, "\n")
	for index := len(lines) - 1; index >= 0; index-- {
		candidate := strings.Trim(strings.TrimSpace(lines[index]), "| ")
		if value, err := strconv.Atoi(candidate); err == nil {
			return value
		}
	}
	t.Fatalf("parse release-scale SQL count %q", output)
	return 0
}

func releaseScaleQuery(t *testing.T, reader *board.PlanningReader, input planningport.QueryInput) (planningport.Snapshot, time.Duration) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	started := time.Now()
	snapshot, err := reader.Query(ctx, input)
	duration := time.Since(started)
	if err != nil {
		t.Fatalf("release-scale planning query: %v", err)
	}
	return snapshot, duration
}

func releaseScaleLatencySummary(durations []time.Duration) releaseScaleLatency {
	values := make([]float64, len(durations))
	for index, duration := range durations {
		values[index] = float64(duration.Microseconds()) / 1_000
	}
	sorted := append([]float64(nil), values...)
	sort.Float64s(sorted)
	result := releaseScaleLatency{SamplesMilliseconds: values}
	if len(sorted) == 0 {
		return result
	}
	result.MinimumMilliseconds, result.MaximumMilliseconds = sorted[0], sorted[len(sorted)-1]
	result.MedianMilliseconds = sorted[len(sorted)/2]
	result.P95Milliseconds = sorted[int(math.Ceil(float64(len(sorted))*0.95))-1]
	for _, value := range values {
		result.MeanMilliseconds += value
	}
	result.MeanMilliseconds /= float64(len(values))
	for _, value := range values {
		delta := value - result.MeanMilliseconds
		result.StdDevMilliseconds += delta * delta
	}
	result.StdDevMilliseconds = math.Sqrt(result.StdDevMilliseconds / float64(len(values)))
	if result.MeanMilliseconds > 0 {
		result.CoefficientVariance = result.StdDevMilliseconds / result.MeanMilliseconds
	}
	return result
}

func releaseScaleProcess(pid int) releaseScaleProcessPeak {
	if pid <= 0 {
		return releaseScaleProcessPeak{}
	}
	result := releaseScaleProcessPeak{}
	for _, line := range strings.Split(releaseScaleRead(fmt.Sprintf("/proc/%d/status", pid)), "\n") {
		fields := strings.Fields(line)
		if len(fields) < 2 {
			continue
		}
		value, _ := strconv.ParseUint(fields[1], 10, 64)
		switch fields[0] {
		case "VmRSS:":
			result.RSSBytes = value * 1024
		case "Threads:":
			result.Threads = value
		}
	}
	if entries, err := os.ReadDir(fmt.Sprintf("/proc/%d/fd", pid)); err == nil {
		result.FileDescriptors = uint64(len(entries))
	}
	return result
}

func releaseScalePeak(left, right releaseScaleProcessPeak) releaseScaleProcessPeak {
	left.RSSBytes = max(left.RSSBytes, right.RSSBytes)
	left.FileDescriptors = max(left.FileDescriptors, right.FileDescriptors)
	left.Threads = max(left.Threads, right.Threads)
	return left
}

func releaseScaleDisk(root string) uint64 {
	var total uint64
	_ = filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
		if err != nil || !entry.Type().IsRegular() {
			return nil
		}
		if info, statErr := entry.Info(); statErr == nil && info.Size() > 0 {
			total += uint64(info.Size())
		}
		return nil
	})
	return total
}

func releaseScaleSampleResources(fixture *doltFixture, evidence *releaseScaleEvidence, stop <-chan struct{}, done chan<- struct{}) {
	ticker := time.NewTicker(20 * time.Millisecond)
	defer ticker.Stop()
	defer close(done)
	for {
		self := releaseScaleProcess(os.Getpid())
		doltProcess := releaseScaleProcess(fixture.command.Process.Pid)
		evidence.Measurements.ResourcePeaks.Self = releaseScalePeak(evidence.Measurements.ResourcePeaks.Self, self)
		evidence.Measurements.ResourcePeaks.Dolt = releaseScalePeak(evidence.Measurements.ResourcePeaks.Dolt, doltProcess)
		evidence.Measurements.ResourcePeaks.SelfGoroutines = max(evidence.Measurements.ResourcePeaks.SelfGoroutines, uint64(runtime.NumGoroutine()))
		select {
		case <-stop:
			return
		case <-ticker.C:
		}
	}
}

func releaseScaleRestart(t *testing.T, fixture *doltFixture) {
	t.Helper()
	configuration := filepath.Join(fixture.root, "server.yaml")
	command := exec.Command("dolt", "sql-server", "--config="+configuration)
	command.Dir = fixture.root
	command.Env = doltEnvironment(filepath.Join(fixture.root, "client"))
	var output strings.Builder
	command.Stdout, command.Stderr = &output, &output
	if err := command.Start(); err != nil {
		t.Fatalf("restart Dolt server: %v", err)
	}
	waited := make(chan error, 1)
	go func() { waited <- command.Wait() }()
	fixture.command, fixture.waited, fixture.stopped = command, waited, false
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		connection, err := net.DialTimeout("tcp", fixture.address, 100*time.Millisecond)
		if err == nil {
			_ = connection.Close()
			return
		}
		select {
		case err := <-waited:
			fixture.stopped = true
			t.Fatalf("restarted Dolt exited before readiness: %v: %s", err, output.String())
		default:
		}
		time.Sleep(25 * time.Millisecond)
	}
	t.Fatalf("restarted Dolt did not listen: %s", output.String())
}

func releaseScaleWriteEvidence(t *testing.T, path string, evidence releaseScaleEvidence) {
	t.Helper()
	if !filepath.IsAbs(path) || filepath.Clean(path) != path {
		t.Fatal("DIRECTOR_SCALE_EVIDENCE_FILE must be an absolute clean path")
	}
	document, err := json.MarshalIndent(evidence, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	document = append(document, '\n')
	file, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatalf("create scale evidence: %v", err)
	}
	if _, err := file.Write(document); err != nil {
		_ = file.Close()
		t.Fatal(err)
	}
	if err := file.Sync(); err != nil {
		_ = file.Close()
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	if info, err := os.Stat(path); err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("scale evidence is not mode 0600: %v", err)
	}
}

func TestReleaseLinuxPerformanceScale(t *testing.T) {
	evidencePath := os.Getenv("DIRECTOR_SCALE_EVIDENCE_FILE")
	if evidencePath == "" {
		t.Skip("set DIRECTOR_SCALE_EVIDENCE_FILE for the explicit release-scale evidence run")
	}
	if runtime.GOOS != "linux" {
		t.Fatal("release scale is supported on Linux only")
	}
	for _, name := range []string{"DIRECTOR_CANDIDATE_SHA", "DIRECTOR_BASE_SHA", "DIRECTOR_TREE_SHA"} {
		if len(os.Getenv(name)) != 40 {
			t.Fatalf("%s must bind the exact 40-character Git object", name)
		}
	}
	evidence := releaseScaleEvidence{
		SchemaVersion: "director.release-scale-evidence/v1", TaskID: "dir-m5.10",
		CandidateSHA: os.Getenv("DIRECTOR_CANDIDATE_SHA"), BaseSHA: os.Getenv("DIRECTOR_BASE_SHA"),
		TreeSHA: os.Getenv("DIRECTOR_TREE_SHA"), CapturedAt: time.Now().UTC().Format(time.RFC3339Nano),
		Verdict: "fail", Thresholds: releaseScaleLimits(), Software: map[string]string{}, Failures: []string{},
	}
	releaseScaleHost(&evidence)
	evidence.Software["dolt"] = releaseScaleCommandOutput(t, "dolt", "version")
	evidence.Software["git"] = releaseScaleCommandOutput(t, "git", "--version")
	evidence.Software["go"] = releaseScaleCommandOutput(t, "go", "version")
	evidence.Software["node"] = releaseScaleCommandOutput(t, "node", "--version")
	evidence.Software["npm"] = releaseScaleCommandOutput(t, "npm", "--version")
	evidence.Software["paseo"] = releaseScaleCommandOutput(t, "paseo", "--version")
	observedCandidate := releaseScaleCommandOutput(t, "git", "rev-parse", "HEAD")
	observedTree := releaseScaleCommandOutput(t, "git", "rev-parse", "HEAD^{tree}")
	observedParent := releaseScaleCommandOutput(t, "git", "rev-parse", "HEAD^")
	observedStatus := releaseScaleCommandOutput(t, "git", "status", "--porcelain", "--untracked-files=all")
	if observedCandidate != evidence.CandidateSHA || observedTree != evidence.TreeSHA || observedParent != evidence.BaseSHA || observedStatus != "" {
		t.Fatal("release-scale evidence must run from the exact clean one-commit Candidate")
	}
	evidence.Topology.TaskStore = "direct Dolt SQL server on explicit 127.0.0.1; one typed adapter; metrics disabled"
	evidence.Topology.Git = "25 disposable native repositories with independent canonical remotes and filesystem identities"
	evidence.Topology.Paseo = "exact installed Paseo 0.7.2; screenshots captured separately against the same Candidate"
	evidence.Topology.SQLControlPoolMaximum, evidence.Topology.SQLWriterPoolMaximum = 4, 4
	dispatchLimits := execution.DefaultDispatchLimits()
	scheduleLimits := scheduling.DefaultLimits()
	evidence.Topology.TaskAgentLaneMaximum = dispatchLimits.TaskAgents
	evidence.Topology.ReviewerLaneMaximum = dispatchLimits.Reviewers
	evidence.Topology.ConcurrentAgentMaximum = scheduleLimits.MaxConcurrentAgents
	evidence.Topology.ConcurrentLoadRequests = releaseScaleConcurrentAgents
	evidence.Topology.QueryPageMaximum = planningport.MaximumPageSize
	evidence.Topology.BoardInitialRenderRows, evidence.Topology.ListInitialRenderRows = 12, 16
	evidence.Verification.GlobalAgentCapacityEight = scheduleLimits.MaxConcurrentAgents == releaseScaleConcurrentAgents
	if dispatchLimits.TaskAgents != 6 || dispatchLimits.Reviewers != 2 || scheduleLimits.MaxConcurrentAgents != 8 {
		t.Fatal("release concurrency topology differs from the PLAN")
	}
	quiet := execution.DispatchObservation{ID: "scale-observation", ObservedAtMillis: 1_000,
		LoadCentiUnits:       execution.Measurement{Present: true, Value: 100},
		AvailableMemoryBytes: execution.Measurement{Present: true, Value: 96 << 30},
		FreeTemporaryBytes:   execution.Measurement{Present: true, Value: 180 << 30}}
	ninth := execution.EvaluateDispatch(dispatchLimits, quiet,
		execution.DispatchRequest{Kind: execution.DispatchTaskAgent, InFlight: execution.DispatchInFlight{TaskAgents: dispatchLimits.TaskAgents}}, 1_001)
	evidence.Verification.SeventhTaskAgentRefused = ninth.Kind == execution.AdmissionPark && ninth.Code == execution.NeedTaskAgentConcurrency

	fixture := startDoltFixture(t)
	store := openContractStore(t, fixture, "release-scale-store", true)
	projectID := "release-scale-project"
	gitRoot := filepath.Join(fixture.root, "git")
	workspaces := make([]domain.Workspace, 0, releaseScaleWorkspaces)
	for index := range releaseScaleWorkspaces {
		workspaces = append(workspaces, releaseScaleRepository(t, gitRoot, projectID, index))
	}
	organizerRoot := filepath.Join(fixture.root, "organizer")
	if err := os.Mkdir(organizerRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	project := domain.Project{ID: projectID, Name: "Linux release scale", State: "active", Organizer: &domain.Organizer{
		ID: domain.OrganizerID(projectID), Mode: domain.OrganizerModeAdopt, Phase: domain.OrganizerPhaseActive,
		RepositoryPath: organizerRoot, PreviewID: "scale-preview", OperationID: "scale-operation",
		HumanActorID: "human:release-scale", ConfigurationSHA256: strings.Repeat("a", 64), OrganizerRevision: strings.Repeat("b", 40),
	}}
	created, err := store.CreateProject(context.Background(),
		command("command-release-scale-project", "project.create", projectID, 0, `{"source":"release-scale"}`),
		project, workspaces, event("event-release-scale-project", "", 1, projectID, 0, "project.created"))
	requireApplied(t, created, err)

	records := make([]releaseScaleTaskRecord, 0, releaseScaleOpenTasks+releaseScaleHistoricalTasks)
	for index := range releaseScaleOpenTasks + releaseScaleHistoricalTasks {
		records = append(records, releaseScaleTask(t, projectID, workspaces, index))
	}
	seedStarted := time.Now()
	releaseScaleInsert(t, fixture, projectID, records)
	evidence.Measurements.SeedMilliseconds = float64(time.Since(seedStarted).Microseconds()) / 1_000
	evidence.Scale.Projects = releaseScaleCount(t, fixture, `SELECT COUNT(*) FROM aggregates WHERE kind='project'`)
	evidence.Scale.Workspaces = releaseScaleCount(t, fixture, `SELECT COUNT(*) FROM aggregates WHERE kind='workspace'`)
	evidence.Scale.OpenTasks = releaseScaleCount(t, fixture, `SELECT COUNT(*) FROM aggregates WHERE kind='task' AND JSON_EXTRACT(data,'$.complete')=false`)
	evidence.Scale.HistoricalTasks = releaseScaleCount(t, fixture, `SELECT COUNT(*) FROM aggregates WHERE kind='task' AND JSON_EXTRACT(data,'$.complete')=true`)
	evidence.Scale.AggregateRecords = releaseScaleCount(t, fixture, `SELECT COUNT(*) FROM aggregates`)
	evidence.Scale.CommandRecords = releaseScaleCount(t, fixture, `SELECT COUNT(*) FROM command_requests`)
	evidence.Scale.EventRecords = releaseScaleCount(t, fixture, `SELECT COUNT(*) FROM events`)
	evidence.Scale.CommandOutcomeRecords = releaseScaleCount(t, fixture, `SELECT COUNT(*) FROM command_outcomes`)
	evidence.Scale.AggregateIdentityFacts = releaseScaleCount(t, fixture, `SELECT COUNT(*) FROM aggregates_identity`)

	resourceStop, resourceDone := make(chan struct{}), make(chan struct{})
	go releaseScaleSampleResources(fixture, &evidence, resourceStop, resourceDone)
	reader := board.NewPlanningReader(store)
	projectSelection := projectID
	openInput := planningport.QueryInput{ProjectID: &projectSelection, WorkspaceIDs: []string{}, EpicIDs: []string{},
		States: []string{"queued"}, Priorities: []string{}, Labels: []string{"scale-open"}, Attention: []string{}, Sort: "scheduler_order", PageSize: planningport.MaximumPageSize}
	historyInput := openInput
	// The scale bootstrap deliberately does not forge 10,000 completed Run,
	// Candidate, CI, Review, integration, and cleanup ledgers. Select the
	// durable complete Task population through all-membership plus its fixture
	// label, while the raw Task count independently proves Complete=true.
	historyInput.States, historyInput.Labels, historyInput.Sort = []string{"queued", "done"}, []string{"scale-history"}, "updated_desc"
	_, _ = releaseScaleQuery(t, reader, openInput)
	_, _ = releaseScaleQuery(t, reader, historyInput)
	var openDurations, historyDurations []time.Duration
	var openSnapshot, historySnapshot planningport.Snapshot
	for range 5 {
		var duration time.Duration
		openSnapshot, duration = releaseScaleQuery(t, reader, openInput)
		openDurations = append(openDurations, duration)
		historySnapshot, duration = releaseScaleQuery(t, reader, historyInput)
		historyDurations = append(historyDurations, duration)
	}
	evidence.Measurements.OpenBoard = releaseScaleLatencySummary(openDurations)
	evidence.Measurements.HistoricalList = releaseScaleLatencySummary(historyDurations)
	t.Logf("scale query latency: open p95=%.3fms history p95=%.3fms", evidence.Measurements.OpenBoard.P95Milliseconds, evidence.Measurements.HistoricalList.P95Milliseconds)
	openDocument, _ := json.Marshal(openSnapshot)
	historyDocument, _ := json.Marshal(historySnapshot)
	evidence.Measurements.BoardResponseBytes, evidence.Measurements.ListResponseBytes = len(openDocument), len(historyDocument)
	evidence.Measurements.FirstPageRows = len(historySnapshot.Page.Tasks)
	if historySnapshot.Page.NextCursor == nil {
		t.Fatal("historical List did not expose a next-page cursor")
	}
	secondInput := historyInput
	secondInput.Cursor = historySnapshot.Page.NextCursor
	second, _ := releaseScaleQuery(t, reader, secondInput)
	evidence.Measurements.SecondPageRows = len(second.Page.Tasks)
	seen := make(map[string]struct{}, len(historySnapshot.Page.Tasks))
	for _, row := range historySnapshot.Page.Tasks {
		seen[row.ID] = struct{}{}
	}
	evidence.Verification.NoDuplicateSecondPageRows = true
	for _, row := range second.Page.Tasks {
		if _, duplicate := seen[row.ID]; duplicate {
			evidence.Verification.NoDuplicateSecondPageRows = false
		}
	}
	evidence.Verification.PageCursorsStable = openSnapshot.Cursor == historySnapshot.Cursor && historySnapshot.Cursor == second.Cursor

	concurrentStarted := time.Now()
	concurrentDurations := make([]time.Duration, releaseScaleConcurrentAgents)
	concurrentErrors := make([]error, releaseScaleConcurrentAgents)
	var concurrentWait sync.WaitGroup
	concurrentWait.Add(releaseScaleConcurrentAgents)
	for index := range releaseScaleConcurrentAgents {
		go func() {
			defer concurrentWait.Done()
			input := openInput
			if index%2 == 1 {
				input = historyInput
			}
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			started := time.Now()
			snapshot, queryErr := reader.Query(ctx, input)
			concurrentDurations[index] = time.Since(started)
			if queryErr != nil || len(snapshot.Page.Tasks) != planningport.MaximumPageSize {
				concurrentErrors[index] = fmt.Errorf("query %d rows=%d: %w", index, len(snapshot.Page.Tasks), queryErr)
			}
		}()
	}
	concurrentWait.Wait()
	evidence.Measurements.ConcurrentBatchMilliseconds = float64(time.Since(concurrentStarted).Microseconds()) / 1_000
	evidence.Measurements.ConcurrentRequest = releaseScaleLatencySummary(concurrentDurations)
	evidence.Verification.EightConcurrentRequestsPassed = true
	for _, queryErr := range concurrentErrors {
		if queryErr != nil {
			evidence.Verification.EightConcurrentRequestsPassed = false
		}
	}

	maintenance := maintenanceForFixture(t, store, fixture.root)
	observeStarted := time.Now()
	source, err := maintenance.ObserveStore(context.Background())
	evidence.Measurements.StoreObservationMilliseconds = float64(time.Since(observeStarted).Microseconds()) / 1_000
	if err != nil || !source.Exact || source.SchemaVersion != 2 {
		t.Fatalf("observe release-scale store: %#v, %v", source, err)
	}
	now := time.Now().UnixMilli()
	backup := domainmaintenance.Backup{ID: "backup-release-scale", Purpose: domainmaintenance.BackupDaily,
		SourceSchemaVersion: source.SchemaVersion, SourceFingerprint: source.Fingerprint,
		Phase: domainmaintenance.BackupDispatching, Attempt: 1, CreatedAtMillis: now,
		RetentionUntil: now + domainmaintenance.RetentionMillis}
	backupStarted := time.Now()
	backupResult, err := maintenance.CreateBackup(context.Background(), backup)
	evidence.Measurements.BackupCreateMilliseconds = float64(time.Since(backupStarted).Microseconds()) / 1_000
	if err != nil || backupResult.Source != source {
		t.Fatalf("create release-scale backup: %#v, %v", backupResult, err)
	}
	validateStarted := time.Now()
	validated, err := maintenance.ValidateBackup(context.Background(), backup)
	evidence.Measurements.BackupValidateMilliseconds = float64(time.Since(validateStarted).Microseconds()) / 1_000
	evidence.Verification.FreshRestoreFingerprintExact = err == nil && validated.Source == source && validated.RestoredFingerprint == source.Fingerprint
	if !evidence.Verification.FreshRestoreFingerprintExact {
		t.Fatalf("validate release-scale backup: %#v, %v", validated, err)
	}
	evidence.Measurements.ResourcePeaks.FixtureDiskBytes = max(evidence.Measurements.ResourcePeaks.FixtureDiskBytes, releaseScaleDisk(fixture.root))
	close(resourceStop)
	<-resourceDone

	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	stopDoltFixture(t, fixture, syscall.SIGTERM)
	restartStarted := time.Now()
	releaseScaleRestart(t, fixture)
	reopened := openContractStore(t, fixture, "release-scale-store", false)
	reopenedMaintenance := maintenanceForFixture(t, reopened, filepath.Join(fixture.root, "restart"))
	reopenedObservation, err := reopenedMaintenance.ObserveStore(context.Background())
	evidence.Verification.RestartFingerprintExact = err == nil && reopenedObservation == source
	reopenedReader := board.NewPlanningReader(reopened)
	restartedSnapshot, _ := releaseScaleQuery(t, reopenedReader, openInput)
	evidence.Verification.PostRestartQueryPassed = restartedSnapshot.Page.TotalTasks == strconv.Itoa(releaseScaleOpenTasks) && len(restartedSnapshot.Page.Tasks) == planningport.MaximumPageSize
	evidence.Measurements.RestartRecoveryMilliseconds = float64(time.Since(restartStarted).Microseconds()) / 1_000
	if err := maintenance.ExpireBackup(context.Background(), backup); err != nil {
		t.Fatal(err)
	}
	expired, err := maintenance.ObserveBackup(context.Background(), backup.ID)
	evidence.Verification.BackupExpiredExactly = err == nil && !expired.Present && expired.Exact

	secretCanary := strings.Join([]string{"github", "_pat_", "RELEASESCALECANARY000000000000"}, "")
	evidence.Verification.SecretCanaryClassified = safedata.ClassifyText(secretCanary, false) == safedata.Secret
	encodedEvidence, _ := json.Marshal(evidence)
	evidence.Verification.SecretCanaryAbsent = !strings.Contains(string(encodedEvidence), secretCanary)
	if err := reopened.Close(); err != nil {
		t.Fatal(err)
	}
	stopDoltFixture(t, fixture, syscall.SIGTERM)
	if err := os.RemoveAll(fixture.root); err != nil {
		t.Fatal(err)
	}
	_, statErr := os.Stat(fixture.root)
	evidence.Verification.DisposableResourcesRemoved = errors.Is(statErr, os.ErrNotExist)

	wantAggregates := 1 + releaseScaleWorkspaces + releaseScaleOpenTasks + releaseScaleHistoricalTasks
	wantLedger := 1 + releaseScaleOpenTasks + releaseScaleHistoricalTasks
	checks := []struct {
		passed  bool
		failure string
	}{
		{evidence.Host.OperatingSystem == "Debian GNU/Linux 13 (trixie)", "unsupported operating system"},
		{evidence.Software["dolt"] == "dolt version 2.3.2", "unexpected Dolt version"},
		{evidence.Software["paseo"] == "0.7.2", "unexpected Paseo version"},
		{evidence.Scale.Projects == 1 && evidence.Scale.Workspaces == releaseScaleWorkspaces, "project or Workspace count differs"},
		{evidence.Scale.OpenTasks == releaseScaleOpenTasks && evidence.Scale.HistoricalTasks == releaseScaleHistoricalTasks, "Task count differs"},
		{evidence.Scale.AggregateRecords == wantAggregates && evidence.Scale.AggregateIdentityFacts == wantAggregates, "aggregate ledger count differs"},
		{evidence.Scale.CommandRecords == wantLedger && evidence.Scale.EventRecords == wantLedger && evidence.Scale.CommandOutcomeRecords == wantLedger, "immutable command/event ledger count differs"},
		{openSnapshot.Page.TotalTasks == strconv.Itoa(releaseScaleOpenTasks), "open Board total differs"},
		{historySnapshot.Page.TotalTasks == strconv.Itoa(releaseScaleHistoricalTasks), "historical List total differs"},
		{evidence.Measurements.FirstPageRows == planningport.MaximumPageSize && evidence.Measurements.SecondPageRows == planningport.MaximumPageSize, "page size differs"},
		{evidence.Measurements.BoardResponseBytes <= planningport.MaximumResponseBytes && evidence.Measurements.ListResponseBytes <= planningport.MaximumResponseBytes, "planning response exceeded contract bound"},
		{evidence.Verification.PageCursorsStable && evidence.Verification.NoDuplicateSecondPageRows, "pagination stability failed"},
		{evidence.Verification.EightConcurrentRequestsPassed && evidence.Verification.GlobalAgentCapacityEight && evidence.Verification.SeventhTaskAgentRefused, "concurrency admission or execution failed"},
		{evidence.Verification.FreshRestoreFingerprintExact && evidence.Verification.RestartFingerprintExact && evidence.Verification.PostRestartQueryPassed, "backup/restart recovery failed"},
		{evidence.Verification.BackupExpiredExactly && evidence.Verification.DisposableResourcesRemoved, "exact cleanup failed"},
		{evidence.Verification.SecretCanaryClassified && evidence.Verification.SecretCanaryAbsent, "secret safety check failed"},
		{evidence.Host.InitialLoadCentiUnits <= execution.DefaultDispatchLimits().MaximumLoadCentiUnits, "host load exceeded dispatch threshold"},
		{evidence.Host.MemoryAvailableBytes >= execution.DefaultDispatchLimits().MinimumAvailableMemoryBytes, "host memory fell below dispatch threshold"},
		{evidence.Host.TemporaryFreeBytes >= execution.DefaultDispatchLimits().MinimumFreeTemporaryBytes, "temporary filesystem fell below dispatch threshold"},
		{evidence.Measurements.SeedMilliseconds <= evidence.Thresholds.SeedMilliseconds, "seed latency exceeded"},
		{evidence.Measurements.OpenBoard.P95Milliseconds <= evidence.Thresholds.QueryP95Milliseconds && evidence.Measurements.HistoricalList.P95Milliseconds <= evidence.Thresholds.QueryP95Milliseconds, "query p95 latency exceeded"},
		{evidence.Measurements.OpenBoard.MaximumMilliseconds <= evidence.Thresholds.QueryMaximumMilliseconds && evidence.Measurements.HistoricalList.MaximumMilliseconds <= evidence.Thresholds.QueryMaximumMilliseconds, "query maximum latency exceeded"},
		{evidence.Measurements.ConcurrentBatchMilliseconds <= evidence.Thresholds.ConcurrentBatchMilliseconds, "concurrent batch latency exceeded"},
		{evidence.Measurements.StoreObservationMilliseconds <= evidence.Thresholds.StoreObservationMilliseconds, "store observation latency exceeded"},
		{evidence.Measurements.BackupCreateMilliseconds <= evidence.Thresholds.BackupCreateMilliseconds, "backup creation latency exceeded"},
		{evidence.Measurements.BackupValidateMilliseconds <= evidence.Thresholds.BackupValidateMilliseconds, "backup validation latency exceeded"},
		{evidence.Measurements.RestartRecoveryMilliseconds <= evidence.Thresholds.RestartRecoveryMilliseconds, "restart recovery latency exceeded"},
		{evidence.Measurements.ResourcePeaks.Self.RSSBytes <= evidence.Thresholds.SelfPeakRSSBytes, "author process RSS exceeded"},
		{evidence.Measurements.ResourcePeaks.Dolt.RSSBytes <= evidence.Thresholds.DoltPeakRSSBytes, "Dolt RSS exceeded"},
		{evidence.Measurements.ResourcePeaks.Self.FileDescriptors <= evidence.Thresholds.SelfPeakFileDescriptors, "author process file descriptors exceeded"},
		{evidence.Measurements.ResourcePeaks.Dolt.FileDescriptors <= evidence.Thresholds.DoltPeakFileDescriptors, "Dolt file descriptors exceeded"},
		{evidence.Measurements.ResourcePeaks.SelfGoroutines <= evidence.Thresholds.SelfPeakGoroutines, "author process goroutines exceeded"},
		{evidence.Measurements.ResourcePeaks.Dolt.Threads <= evidence.Thresholds.DoltPeakThreads, "Dolt threads exceeded"},
		{evidence.Measurements.ResourcePeaks.FixtureDiskBytes <= evidence.Thresholds.FixturePeakDiskBytes, "fixture disk usage exceeded"},
	}
	for _, check := range checks {
		if !check.passed {
			evidence.Failures = append(evidence.Failures, check.failure)
		}
	}
	if len(evidence.Failures) == 0 {
		evidence.Verdict = "pass"
	}
	releaseScaleWriteEvidence(t, evidencePath, evidence)
	if evidence.Verdict != "pass" {
		t.Fatalf("release-scale thresholds failed: %s", strings.Join(evidence.Failures, "; "))
	}
}
