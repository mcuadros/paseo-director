// SPDX-License-Identifier: Apache-2.0

package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/mcuadros/director-engine/adapters/diagnostics"
	"github.com/mcuadros/director-engine/adapters/dolt"
	gitadapter "github.com/mcuadros/director-engine/adapters/git"
	githubadapter "github.com/mcuadros/director-engine/adapters/github"
	"github.com/mcuadros/director-engine/adapters/hostipc"
	"github.com/mcuadros/director-engine/adapters/organizergit"
	provideradapter "github.com/mcuadros/director-engine/adapters/provider"
	runtimeadapter "github.com/mcuadros/director-engine/adapters/runtime"
	"github.com/mcuadros/director-engine/application/board"
	executionapp "github.com/mcuadros/director-engine/application/execution"
	homeapp "github.com/mcuadros/director-engine/application/home"
	organizerapp "github.com/mcuadros/director-engine/application/organizer"
	maintenanceapp "github.com/mcuadros/director-engine/application/taskstoremaintenance"
	"github.com/mcuadros/director-engine/domain"
	"github.com/mcuadros/director-engine/domain/jsondocument"
	domainmaintenance "github.com/mcuadros/director-engine/domain/taskstoremaintenance"
	homeport "github.com/mcuadros/director-engine/ports/home"
	planningport "github.com/mcuadros/director-engine/ports/planning"
)

const maximumBoardServerConfigBytes = 64 * 1024

type boardServerEndpoint struct {
	Address      string          `json:"address"`
	Database     string          `json:"database"`
	User         string          `json:"user"`
	PasswordFile json.RawMessage `json:"passwordFile"`
}

type boardServerConfig struct {
	SchemaVersion int                 `json:"schemaVersion"`
	StoreID       string              `json:"storeId"`
	Control       boardServerEndpoint `json:"control"`
	Writer        boardServerEndpoint `json:"writer"`
}

func privateFile(path string, maximum int) ([]byte, error) {
	if !filepath.IsAbs(path) {
		return nil, errors.New("private file path must be absolute")
	}
	before, err := os.Lstat(path)
	if err != nil {
		return nil, errors.New("private file is unavailable or unsafe")
	}
	identity, identityOK := before.Sys().(*syscall.Stat_t)
	if !before.Mode().IsRegular() || before.Mode().Perm()&0o077 != 0 ||
		!identityOK || int(identity.Uid) != os.Geteuid() {
		return nil, errors.New("private file is unavailable or unsafe")
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, errors.New("private file is unavailable or unsafe")
	}
	defer file.Close()
	opened, err := file.Stat()
	if err != nil || !os.SameFile(before, opened) {
		return nil, errors.New("private file identity changed")
	}
	content, err := io.ReadAll(io.LimitReader(file, int64(maximum)+1))
	if err != nil || len(content) > maximum {
		return nil, errors.New("private file cannot be read within its bound")
	}
	after, err := file.Stat()
	if err != nil || opened.Size() != after.Size() || opened.ModTime() != after.ModTime() || opened.Mode() != after.Mode() {
		return nil, errors.New("private file metadata changed")
	}
	return content, nil
}

func endpointConfig(endpoint boardServerEndpoint) (dolt.Endpoint, error) {
	if endpoint.Address == "" || endpoint.Database == "" || endpoint.User == "" || len(endpoint.PasswordFile) == 0 {
		return dolt.Endpoint{}, errors.New("TaskStore endpoint configuration is incomplete")
	}
	password := ""
	if !bytes.Equal(bytes.TrimSpace(endpoint.PasswordFile), []byte("null")) {
		var passwordPath string
		if err := json.Unmarshal(endpoint.PasswordFile, &passwordPath); err != nil || passwordPath == "" {
			return dolt.Endpoint{}, errors.New("TaskStore password file is invalid")
		}
		content, err := privateFile(passwordPath, 16*1024)
		if err != nil {
			return dolt.Endpoint{}, errors.New("TaskStore password file is invalid")
		}
		password = strings.TrimSpace(string(content))
		if password == "" {
			return dolt.Endpoint{}, errors.New("TaskStore password file is empty")
		}
	}
	return dolt.Endpoint{
		Address: endpoint.Address, Database: endpoint.Database,
		User: endpoint.User, Password: password,
	}, nil
}

func readBoardServerConfig(path string) (dolt.Config, error) {
	content, err := privateFile(path, maximumBoardServerConfigBytes)
	if err != nil {
		return dolt.Config{}, errors.New("Board server configuration is invalid")
	}
	canonical, err := jsondocument.CanonicalWithNormalizedNumbersLimit(content, maximumBoardServerConfigBytes)
	if err != nil {
		return dolt.Config{}, errors.New("Board server configuration is invalid")
	}
	decoder := json.NewDecoder(bytes.NewReader(canonical))
	decoder.DisallowUnknownFields()
	var document boardServerConfig
	if err := decoder.Decode(&document); err != nil {
		return dolt.Config{}, errors.New("Board server configuration is invalid")
	}
	if _, err := decoder.Token(); !errors.Is(err, io.EOF) || document.SchemaVersion != 1 || document.StoreID == "" {
		return dolt.Config{}, errors.New("Board server configuration is invalid")
	}
	control, err := endpointConfig(document.Control)
	if err != nil {
		return dolt.Config{}, err
	}
	writer, err := endpointConfig(document.Writer)
	if err != nil {
		return dolt.Config{}, err
	}
	return dolt.Config{Control: control, Writer: writer, StoreID: document.StoreID}, nil
}

func loopbackListenAddress(value string) bool {
	host, port, err := net.SplitHostPort(value)
	if err != nil || port == "" {
		return false
	}
	address := net.ParseIP(host)
	return address != nil && address.IsLoopback()
}

func validServerHostIdentity(id, label string) bool {
	return len(id) > 0 && len(id) <= 128 && utf8.ValidString(id) && id == strings.TrimSpace(id) &&
		strings.IndexFunc(id, unicode.IsControl) < 0 && len(label) > 0 && len(label) <= 512 &&
		utf8.ValidString(label) && label == strings.TrimSpace(label) && strings.IndexFunc(label, unicode.IsControl) < 0
}

func defaultProductionRuntimePaths() (string, string, error) {
	base := os.Getenv("XDG_RUNTIME_DIR")
	if base == "" {
		base = os.Getenv("XDG_CACHE_HOME")
	}
	if base == "" {
		var err error
		base, err = os.UserCacheDir()
		if err != nil {
			return "", "", errors.New("production runtime base is unavailable")
		}
	}
	if !filepath.IsAbs(base) || filepath.Clean(base) != base {
		return "", "", errors.New("production runtime base is invalid")
	}
	root := filepath.Join(base, "director", "runtime")
	return filepath.Join(root, "work"), filepath.Join(root, "host.sock"), nil
}

type maintenanceProjectSource interface {
	Projects(context.Context) ([]domain.Project, error)
}

func appendMaintenanceFailure(ctx context.Context, projects maintenanceProjectSource, logs *diagnostics.LogStore, nowMillis int64) {
	values, err := projects.Projects(ctx)
	if err != nil {
		return
	}
	for _, project := range values {
		_ = logs.Append(ctx, project.ID, nowMillis, homeport.TechnicalLogError, homeport.TechnicalLogTaskStore,
			homeport.TechnicalLogTaskStoreUnhealthy, 1)
	}
}

func reconcileTaskStoreMaintenance(ctx context.Context, service *maintenanceapp.Service, projects maintenanceProjectSource,
	logs *diagnostics.LogStore, now func() int64,
) {
	ticker := time.NewTicker(time.Hour)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			cycleContext, cancel := context.WithTimeout(ctx, 3*time.Minute)
			observedAt := now()
			disk, err := service.ReconcileDisk(cycleContext, maintenanceapp.Command{ID: "engine-hourly-disk-" + strconv.FormatInt(observedAt/3_600_000, 10), NowMillis: observedAt})
			if err == nil && disk.Disk.Phase == domainmaintenance.DiskReady {
				_, err = service.ExpireBackups(cycleContext, maintenanceapp.Command{ID: "engine-hourly-retention-" + strconv.FormatInt(observedAt/3_600_000, 10), NowMillis: observedAt})
			}
			if err == nil && disk.Disk.Phase == domainmaintenance.DiskReady {
				_, err = service.ReconcileDaily(cycleContext, maintenanceapp.Command{ID: "engine-hourly-daily-" + strconv.FormatInt(observedAt/3_600_000, 10), NowMillis: observedAt})
			}
			// A low-space cycle intentionally appends nothing: after bounded
			// cleanup the Engine must consume no more disk. Other failures use
			// the existing bounded, path-free TaskStore health code.
			if err != nil && disk.Disk.Phase != domainmaintenance.DiskDegraded && disk.Disk.Phase != domainmaintenance.DiskCleanupRequired {
				appendMaintenanceFailure(cycleContext, projects, logs, observedAt)
			}
			cancel()
		}
	}
}

type productionRunStore interface {
	Projects(context.Context) ([]domain.Project, error)
	Tasks(context.Context, string) ([]domain.Task, error)
	Runs(context.Context, string) ([]domain.Run, error)
}

func reconcileProductionRun(ctx context.Context, production *Production, runID string) error {
	for transition := 0; transition < 128; transition++ {
		progressed, err := production.ReconcileRun(ctx, runID)
		if err != nil || !progressed {
			return err
		}
	}
	return errors.New("production reconciliation transition bound exceeded")
}

func productionRuns(ctx context.Context, store productionRunStore, visit func(domain.Project, domain.Run) error) (bool, error) {
	projects, err := store.Projects(ctx)
	if err != nil {
		return false, err
	}
	active := false
	for _, project := range projects {
		tasks, err := store.Tasks(ctx, project.ID)
		if err != nil {
			return false, err
		}
		for _, task := range tasks {
			runs, err := store.Runs(ctx, task.ID)
			if err != nil {
				return false, err
			}
			for _, run := range runs {
				if run.Execution.Terminal {
					continue
				}
				active = true
				if err := visit(project, run); err != nil {
					return active, err
				}
			}
		}
	}
	return active, nil
}

func reconcileProduction(ctx context.Context, store productionRunStore, production *Production,
	launcher *Launcher, queue *WakeQueue, logs *diagnostics.LogStore, now func() int64,
) {
	cycle := func() bool {
		cycleContext, cancel := context.WithTimeout(ctx, 4*time.Minute)
		defer cancel()
		_ = launcher.Schedule(cycleContext)
		active, err := productionRuns(cycleContext, store, func(project domain.Project, run domain.Run) error {
			if err := reconcileProductionRun(cycleContext, production, run.ID); err != nil {
				_ = logs.Append(cycleContext, project.ID, now(), homeport.TechnicalLogWarning,
					homeport.TechnicalLogReconciliation, homeport.TechnicalLogReconciliationWaiting, 1)
			}
			return nil
		})
		return err == nil && active
	}
	active := cycle()
	timer := time.NewTimer(func() time.Duration {
		if active {
			return 30 * time.Second
		}
		return 5 * time.Minute
	}())
	defer timer.Stop()
	resetTimer := func(delay time.Duration) {
		if !timer.Stop() {
			select {
			case <-timer.C:
			default:
			}
		}
		timer.Reset(delay)
	}
	for {
		select {
		case <-ctx.Done():
			return
		case wake := <-queue.Wakes():
			wakeContext, cancel := context.WithTimeout(ctx, 30*time.Second)
			_ = reconcileProductionRun(wakeContext, production, wake.RunID)
			cancel()
			resetTimer(250 * time.Millisecond)
		case runID := <-queue.Runs():
			wakeContext, cancel := context.WithTimeout(ctx, 4*time.Minute)
			_ = reconcileProductionRun(wakeContext, production, runID)
			cancel()
			resetTimer(250 * time.Millisecond)
		case <-timer.C:
			active = cycle()
			delay := 5 * time.Minute
			if active {
				delay = 30 * time.Second
			}
			resetTimer(delay)
		}
	}
}

func runBoardServer(arguments []string, stdout, stderr io.Writer) int {
	defaultRuntimeRoot, defaultHostSocket, defaultErr := defaultProductionRuntimePaths()
	if defaultErr != nil {
		fmt.Fprintln(stderr, "director-engine: production runtime default is invalid")
		return 2
	}
	flags := flag.NewFlagSet("serve-board", flag.ContinueOnError)
	flags.SetOutput(stderr)
	listenAddress := flags.String("listen", "", "explicit loopback listen address")
	configPath := flags.String("taskstore-config", "", "absolute private TaskStore configuration path")
	hostID := flags.String("host-id", "", "exact public Paseo host identity")
	hostLabel := flags.String("host-label", "", "public Paseo host label")
	hostSocket := flags.String("host-socket", defaultHostSocket, "absolute owner-only Director for Paseo host socket")
	runtimeRoot := flags.String("runtime-root", defaultRuntimeRoot, "absolute owner-only production runtime root")
	if err := flags.Parse(arguments); err != nil || flags.NArg() != 0 || !loopbackListenAddress(*listenAddress) || *configPath == "" ||
		!validServerHostIdentity(*hostID, *hostLabel) || !filepath.IsAbs(*hostSocket) || filepath.Clean(*hostSocket) != *hostSocket ||
		!filepath.IsAbs(*runtimeRoot) || filepath.Clean(*runtimeRoot) != *runtimeRoot {
		fmt.Fprintln(stderr, "usage: director-engine serve-board --listen <loopback:port> --taskstore-config <absolute-path> --host-id <id> --host-label <label> --host-socket <absolute-path> --runtime-root <absolute-path>")
		return 2
	}
	config, err := readBoardServerConfig(*configPath)
	if err != nil {
		fmt.Fprintln(stderr, "director-engine: Board server configuration is invalid")
		return 1
	}
	store, err := dolt.Open(config)
	if err != nil {
		fmt.Fprintln(stderr, "director-engine: TaskStore is unavailable")
		return 1
	}
	defer store.Close()
	logRoot, err := diagnostics.DefaultLogRoot()
	if err != nil {
		fmt.Fprintln(stderr, "director-engine: TaskStore maintenance is unavailable")
		return 1
	}
	logs, err := diagnostics.NewLogStore(logRoot)
	if err != nil {
		fmt.Fprintln(stderr, "director-engine: TaskStore maintenance is unavailable")
		return 1
	}
	maintenanceRoot, err := dolt.DefaultMaintenanceRoot(config.StoreID)
	if err != nil {
		fmt.Fprintln(stderr, "director-engine: TaskStore maintenance is unavailable")
		return 1
	}
	maintenance, err := dolt.NewMaintenance(store, maintenanceRoot, logs)
	if err != nil {
		fmt.Fprintln(stderr, "director-engine: TaskStore maintenance is unavailable")
		return 1
	}
	maintenanceService, err := maintenanceapp.NewService(maintenance, maintenance)
	if err != nil {
		fmt.Fprintln(stderr, "director-engine: TaskStore maintenance is unavailable")
		return 1
	}
	startupContext, cancelStartup := context.WithTimeout(context.Background(), 3*time.Minute)
	startupNow := time.Now().UnixMilli()
	disk, diskError := maintenanceService.ReconcileDisk(startupContext, maintenanceapp.Command{ID: "engine-startup-disk", NowMillis: startupNow})
	if diskError != nil || disk.Disk.Phase != domainmaintenance.DiskReady {
		cancelStartup()
		fmt.Fprintln(stderr, "director-engine: free-space launch gate refused startup")
		return 1
	}
	maintenanceState, observationError := maintenance.Load(startupContext)
	storeObservation := domainmaintenance.StoreObservation{}
	if observationError == nil {
		storeObservation, observationError = maintenance.ObserveStore(startupContext)
	}
	if observationError == nil && maintenanceState.Migration != nil && maintenanceState.Migration.Phase != domainmaintenance.MigrationComplete {
		persisted := maintenanceState.Migration
		_, observationError = maintenanceService.Migrate(startupContext, maintenanceapp.MigrationCommand{
			ID: persisted.ID, FromVersion: persisted.FromVersion, ToVersion: persisted.ToVersion, NowMillis: startupNow,
		})
	} else if observationError == nil && storeObservation.Exact && storeObservation.SchemaVersion == 1 {
		_, observationError = maintenanceService.Migrate(startupContext, maintenanceapp.MigrationCommand{
			ID: "taskstore-schema-1-to-2", FromVersion: 1, ToVersion: 2, NowMillis: startupNow,
		})
	}
	if observationError != nil {
		cancelStartup()
		fmt.Fprintln(stderr, "director-engine: TaskStore is unavailable")
		return 1
	}
	storeObservation, observationError = maintenance.ObserveStore(startupContext)
	if observationError != nil || !storeObservation.Exact || storeObservation.SchemaVersion != 2 {
		cancelStartup()
		fmt.Fprintln(stderr, "director-engine: TaskStore is unavailable")
		return 1
	}
	if _, err := maintenanceService.ExpireBackups(startupContext, maintenanceapp.Command{ID: "engine-startup-retention", NowMillis: startupNow}); err != nil {
		cancelStartup()
		fmt.Fprintln(stderr, "director-engine: TaskStore maintenance is unavailable")
		return 1
	}
	if _, err := maintenanceService.ReconcileDaily(startupContext, maintenanceapp.Command{ID: "engine-startup-daily", NowMillis: startupNow}); err != nil {
		cancelStartup()
		fmt.Fprintln(stderr, "director-engine: TaskStore maintenance is unavailable")
		return 1
	}
	cancelStartup()
	listener, err := net.Listen("tcp", *listenAddress)
	if err != nil {
		fmt.Fprintln(stderr, "director-engine: Board listener is unavailable")
		return 1
	}
	defer listener.Close()
	current, err := currentIdentity()
	if err != nil {
		fmt.Fprintln(stderr, "director-engine: identity is unavailable")
		return 1
	}
	handler := http.NewServeMux()
	handler.Handle(boardQueryPath, newBoardHandler(board.NewReader(store)))
	planningReader := board.NewPlanningReader(store)
	handler.Handle(planningport.QueryPath, newPlanningHandler(planningReader))
	handler.Handle(planningport.TaskDetailQueryPath, newTaskDetailHandler(planningReader, *hostID))
	startedAt := time.Now().UnixNano()
	instanceDigest := sha256.Sum256([]byte(*hostID + "\x1f" + strconv.Itoa(os.Getpid()) + "\x1f" + strconv.FormatInt(startedAt, 10)))
	now := func() int64 { return time.Now().UnixMilli() }
	engineInstance := "engine-" + hex.EncodeToString(instanceDigest[:16])
	homeSource := homeapp.NewStaticSource(*hostID, *hostLabel, engineInstance, now)
	logContext, cancelLogs := context.WithTimeout(context.Background(), 5*time.Second)
	projects, projectsError := store.Projects(logContext)
	logsCurrent := projectsError == nil
	for _, project := range projects {
		if logs.Append(logContext, project.ID, now(), homeport.TechnicalLogInfo, homeport.TechnicalLogEngine, homeport.TechnicalLogEngineStarted, 1) != nil {
			logsCurrent = false
			break
		}
	}
	cancelLogs()
	if logsCurrent {
		homeSource.WithTechnicalLogs(logs)
	}
	maintenanceContext, cancelMaintenance := context.WithCancel(context.Background())
	defer cancelMaintenance()
	go reconcileTaskStoreMaintenance(maintenanceContext, maintenanceService, store, logs, now)
	var supportBundles homeport.SupportBundleWriter
	if supportRoot, supportError := diagnostics.DefaultSupportRoot(); supportError == nil {
		if writer, writerError := diagnostics.NewBundleWriter(supportRoot); writerError == nil {
			supportBundles = writer
		}
	}
	hostPort, err := hostipc.New(*hostSocket)
	if err != nil {
		fmt.Fprintln(stderr, "director-engine: Paseo host transport configuration is invalid")
		return 1
	}
	runtimePort, err := runtimeadapter.New(*runtimeRoot)
	if err != nil {
		fmt.Fprintln(stderr, "director-engine: production runtime root is unavailable")
		return 1
	}
	queue := NewWakeQueue(256)
	gitPort, githubPort := gitadapter.New(), githubadapter.New()
	controller := executionapp.NewControllerWithGit(store, runtimePort, gitPort, hostPort, queue)
	executable, err := os.Executable()
	if err != nil {
		fmt.Fprintln(stderr, "director-engine: executable identity is unavailable")
		return 1
	}
	engineURL := "http://" + listener.Addr().String()
	organizerRepository := organizergit.New()
	takeoverObserver, err := runtimeadapter.NewTakeoverObserver(store, hostPort)
	if err != nil {
		fmt.Fprintln(stderr, "director-engine: production takeover observation is unavailable")
		return 1
	}
	launcher, err := NewLauncher(store, controller, organizerRepository, provideradapter.Local{}, runtimePort, runtimePort,
		takeoverObserver, queue, engineInstance, "pid-"+strconv.Itoa(os.Getpid())+":start-"+strconv.FormatInt(startedAt, 10), *runtimeRoot, engineURL, executable)
	if err != nil {
		fmt.Fprintln(stderr, "director-engine: production launcher composition is unavailable")
		return 1
	}
	production, err := NewProduction(store, controller, launcher, hostPort, githubPort, gitPort, queue, *runtimeRoot,
		UUID("coordinator", *hostID, engineInstance))
	if err != nil {
		fmt.Fprintln(stderr, "director-engine: production workflow composition is unavailable")
		return 1
	}
	operationsExecutor := NewOperationsExecutor(store, production)
	homeSource.WithProductionOperations().WithWorkspaceStore(store)
	handler.Handle(planningport.HomeQueryPath, newHomeHandler(homeapp.NewReader(store, board.NewTaskStoreFactSource(store), homeSource, now)))
	doctorRepair := homeapp.NewDoctorRepairService(store, homeSource, operationsExecutor, now)
	handler.Handle(planningport.DoctorQueryPath, newDoctorHandler(doctorRepair))
	handler.Handle(planningport.RepairMutationPath, newRepairHandler(doctorRepair))
	operations := homeapp.NewOperationsService(store, homeSource, operationsExecutor, supportBundles, now)
	handler.Handle(planningport.OperationsQueryPath, newOperationsHandler(operations))
	handler.Handle(planningport.OperationsMutationPath, newOperationsMutationHandler(operations))
	startupContext, cancelProductionStartup := context.WithTimeout(context.Background(), 30*time.Second)
	_, startupErr := controller.ReconcileStartup(startupContext, executionapp.StartupCommand{SchemaVersion: executionapp.StartupCommandSchemaVersion,
		RequestID: "production-startup-" + hex.EncodeToString(instanceDigest[:16]), NowMillis: now()})
	cancelProductionStartup()
	if startupErr != nil {
		fmt.Fprintln(stderr, "director-engine: production startup reconciliation refused readiness")
		return 1
	}
	organizerService := organizerapp.New(store, organizerRepository, nil)
	handler.Handle(planningport.OrganizerMutationPath, newOrganizerBootstrapHandler(organizerService, store, *hostID))
	handler.Handle(planningport.MutationPath, newPlanningMutationHandler(store, controller, launcher, production))
	handler.Handle(terminalEventPath, newTerminalEventHandler(store, controller, production))
	handler.Handle(agentMCPPath, newAgentMCPHandler(store))
	productionContext, cancelProduction := context.WithCancel(context.Background())
	defer cancelProduction()
	go reconcileProduction(productionContext, store, production, launcher, queue, logs, now)
	if err := writeJSON(stdout, struct {
		Event    string   `json:"event"`
		Address  string   `json:"address"`
		Identity identity `json:"identity"`
	}{Event: "director-engine.production-listening", Address: listener.Addr().String(), Identity: current}); err != nil {
		fmt.Fprintln(stderr, "director-engine: production readiness output failed")
		return 1
	}
	server := &http.Server{
		Handler:           handler,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       10 * time.Second,
		WriteTimeout:      10 * time.Second,
		IdleTimeout:       30 * time.Second,
	}
	if err := server.Serve(listener); err != nil && !errors.Is(err, http.ErrServerClosed) {
		fmt.Fprintln(stderr, "director-engine: Board listener stopped")
		return 1
	}
	return 0
}
