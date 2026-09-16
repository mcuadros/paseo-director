// SPDX-License-Identifier: Apache-2.0

package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
)

const (
	leaseDuration = 30 * time.Second
	startTimeout  = 120 * time.Second
)

func processStart(pid int) string {
	content, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
	if err != nil {
		return ""
	}
	line := string(content)
	close := strings.LastIndex(line, ")")
	if close < 0 {
		return ""
	}
	fields := strings.Fields(line[close+1:])
	if len(fields) < 20 || fields[0] == "Z" {
		return ""
	}
	return fields[19]
}

func processExecutable(pid int) string {
	path, err := filepath.EvalSymlinks(fmt.Sprintf("/proc/%d/exe", pid))
	if err != nil {
		return ""
	}
	return path
}

func acquireRuntimeLock(path string) (*os.File, error) {
	if err := ensurePrivateDir(filepath.Dir(path)); err != nil {
		return nil, err
	}
	file, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		return nil, fail("DIRECTOR_BOOTSTRAP_LOCK")
	}
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm() != 0o600 {
		file.Close()
		return nil, fail("DIRECTOR_BOOTSTRAP_LOCK")
	}
	if err := syscall.Flock(int(file.Fd()), syscall.LOCK_EX); err != nil {
		file.Close()
		return nil, fail("DIRECTOR_BOOTSTRAP_LOCK")
	}
	return file, nil
}

func releaseRuntimeLock(file *os.File) {
	_ = syscall.Flock(int(file.Fd()), syscall.LOCK_UN)
	_ = file.Close()
}

func readRuntimeState(path string) (runtimeState, bool, error) {
	if _, err := os.Lstat(path); os.IsNotExist(err) {
		return runtimeState{}, false, nil
	}
	info, err := privateRegular(path, maximumManifestBytes, false)
	if err != nil || info.Mode().Perm() != 0o600 {
		return runtimeState{}, false, fail("DIRECTOR_BOOTSTRAP_STATE_POISONED")
	}
	var state runtimeState
	if err := decodeStrict(path, maximumManifestBytes, &state); err != nil || state.SchemaVersion != 1 || !sha256Pattern.MatchString(state.Binding) || state.PID <= 0 || state.ProcessStart == "" ||
		(state.Status != "current" && state.Status != "degraded") || !filepath.IsAbs(state.EngineExecutable) || !filepath.IsAbs(state.DoltExecutable) || state.EnginePID < 0 || state.DoltPID < 0 {
		return runtimeState{}, false, fail("DIRECTOR_BOOTSTRAP_STATE_POISONED")
	}
	return state, true, nil
}

// engineAddress is the single place the Engine listen address is formed. The
// supervisor starts its Engine here and reports the same value in its status,
// so the connector binds its Director transports to the port it actually
// serves instead of to an assumed default.
func engineAddress(ports runtimePorts) string {
	return fmt.Sprintf("127.0.0.1:%d", ports.engine)
}

func runtimeBinding(prepared preparedRuntime, identity hostIdentity, hostSocket string, ports runtimePorts) string {
	return canonicalJSONDigest(struct {
		SchemaVersion  int          `json:"schemaVersion"`
		Candidate      string       `json:"candidate"`
		Channel        string       `json:"channel"`
		Bootstrap      string       `json:"bootstrap"`
		Engine         string       `json:"engine"`
		EngineContract string       `json:"engineContract"`
		Dolt           string       `json:"dolt"`
		Host           hostIdentity `json:"host"`
		HostSocket     string       `json:"hostSocket"`
		Ports          [2]int       `json:"ports"`
	}{1, prepared.SourceCandidate, prepared.Channel, prepared.Bootstrap.SHA256, prepared.Engine.Binary.SHA256,
		prepared.Engine.ContractSHA256, prepared.Dolt.Binary.SHA256, identity, hostSocket, [2]int{ports.dolt, ports.engine}})
}

func requestControl(paths runtimePaths, request controlRequest) (runtimeStatus, error) {
	connection, err := net.DialTimeout("unix", paths.controlSocket, 2*time.Second)
	if err != nil {
		return runtimeStatus{}, fail("DIRECTOR_BOOTSTRAP_CONTROL_UNAVAILABLE")
	}
	defer connection.Close()
	_ = connection.SetDeadline(time.Now().Add(3 * time.Second))
	content, _ := json.Marshal(request)
	if _, err := connection.Write(append(content, '\n')); err != nil {
		return runtimeStatus{}, fail("DIRECTOR_BOOTSTRAP_CONTROL_UNAVAILABLE")
	}
	line, err := bufio.NewReader(io.LimitReader(connection, maximumManifestBytes+1)).ReadBytes('\n')
	if err != nil || len(line) > maximumManifestBytes {
		return runtimeStatus{}, fail("DIRECTOR_BOOTSTRAP_CONTROL_INVALID")
	}
	var status runtimeStatus
	decoder := json.NewDecoder(bytes.NewReader(line))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&status); err != nil || status.SchemaVersion != controlProtocolVersion || !sha256Pattern.MatchString(status.Binding) ||
		(status.State != "current" && status.State != "degraded") || !hostIDPattern.MatchString(status.Host.ID) || status.Host.Label != "Director" ||
		!validEngineAddress(status.EngineAddress) || !sha256Pattern.MatchString(status.ProjectAdminAuthorization) {
		return runtimeStatus{}, fail("DIRECTOR_BOOTSTRAP_CONTROL_INVALID")
	}
	return status, nil
}

func reclaimChild(pid int, start, executable string) error {
	if pid == 0 {
		return nil
	}
	if processStart(pid) != start || processExecutable(pid) != executable {
		return fail("DIRECTOR_BOOTSTRAP_FOREIGN_OWNER")
	}
	_ = syscall.Kill(pid, syscall.SIGTERM)
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if processStart(pid) == "" {
			return nil
		}
		time.Sleep(50 * time.Millisecond)
	}
	if processStart(pid) == start && processExecutable(pid) == executable {
		_ = syscall.Kill(pid, syscall.SIGKILL)
	}
	return nil
}

func cleanupStale(paths runtimePaths, state runtimeState) error {
	if processStart(state.PID) != "" {
		return fail("DIRECTOR_BOOTSTRAP_FOREIGN_OWNER")
	}
	if err := reclaimChild(state.EnginePID, state.EngineProcessStart, state.EngineExecutable); err != nil {
		return err
	}
	if err := reclaimChild(state.DoltPID, state.DoltProcessStart, state.DoltExecutable); err != nil {
		return err
	}
	for _, path := range []string{paths.controlSocket, paths.state, paths.failure} {
		if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
			return fail("DIRECTOR_BOOTSTRAP_STATE_IO")
		}
	}
	return nil
}

// adoptLiveOwner decides what an ensure may do with a live supervisor already
// owning these runtime paths. An isolated runtime adopts only its own exact
// binding: isolation adds a runtime beside an existing one and must never
// release the instance it finds, which is what makes an isolation port pair
// unusable as a way around the occupied-listener refusal.
func adoptLiveOwner(isolated bool, owned, requested string) (bool, error) {
	if owned == requested {
		return true, nil
	}
	if isolated {
		return false, fail("DIRECTOR_BOOTSTRAP_ISOLATION_OCCUPIED")
	}
	return false, nil
}

func ensureRuntime(candidate, bootstrapSHA, hostSocket string) (runtimeStatus, error) {
	if !platformOK() || !gitSHAPattern.MatchString(candidate) || !sha256Pattern.MatchString(bootstrapSHA) || !filepath.IsAbs(hostSocket) || filepath.Clean(hostSocket) != hostSocket {
		return runtimeStatus{}, fail("DIRECTOR_BOOTSTRAP_ARGUMENT")
	}
	self, err := selfExecutable(bootstrapSHA)
	if err != nil {
		return runtimeStatus{}, err
	}
	paths, ports, err := runtimeLayout()
	if err != nil {
		return runtimeStatus{}, err
	}
	for _, path := range []string{paths.runtimeRoot, paths.configRoot, paths.dataRoot, filepath.Dir(paths.hostIdentity)} {
		if err := ensurePrivateDir(path); err != nil {
			return runtimeStatus{}, err
		}
	}
	prepared, err := loadPrepared(paths, candidate, bootstrapSHA)
	if err != nil {
		return runtimeStatus{}, err
	}
	if prepared.Bootstrap.Path != self.Path {
		return runtimeStatus{}, fail("DIRECTOR_BOOTSTRAP_SELF_IDENTITY")
	}
	identity, err := loadOrCreateHostIdentity(paths.hostIdentity)
	if err != nil {
		return runtimeStatus{}, err
	}
	controlToken, err := privateSecret(paths.controlToken)
	if err != nil {
		return runtimeStatus{}, err
	}
	if _, err := privateSecret(paths.projectAdminToken); err != nil {
		return runtimeStatus{}, err
	}
	binding := runtimeBinding(prepared, identity, hostSocket, ports)
	lock, err := acquireRuntimeLock(paths.lock)
	if err != nil {
		return runtimeStatus{}, err
	}
	defer releaseRuntimeLock(lock)

	state, present, err := readRuntimeState(paths.state)
	if err != nil {
		return runtimeStatus{}, err
	}
	if present && processStart(state.PID) == state.ProcessStart {
		status, requestErr := requestControl(paths, controlRequest{SchemaVersion: controlProtocolVersion, Token: controlToken, Command: "status"})
		if requestErr != nil || status.Binding != state.Binding {
			return runtimeStatus{}, fail("DIRECTOR_BOOTSTRAP_FOREIGN_OWNER")
		}
		adopt, err := adoptLiveOwner(ports.isolated, state.Binding, binding)
		if err != nil {
			return runtimeStatus{}, err
		}
		if adopt {
			return requestControl(paths, controlRequest{SchemaVersion: controlProtocolVersion, Token: controlToken, Command: "ensure"})
		}
		if _, err := requestControl(paths, controlRequest{SchemaVersion: controlProtocolVersion, Token: controlToken, Command: "release"}); err != nil {
			return runtimeStatus{}, err
		}
		deadline := time.Now().Add(10 * time.Second)
		for processStart(state.PID) == state.ProcessStart && time.Now().Before(deadline) {
			time.Sleep(50 * time.Millisecond)
		}
		if processStart(state.PID) == state.ProcessStart {
			return runtimeStatus{}, fail("DIRECTOR_BOOTSTRAP_HANDOFF_TIMEOUT")
		}
		state, present, err = readRuntimeState(paths.state)
		if err != nil {
			return runtimeStatus{}, err
		}
	}
	if present {
		if err := cleanupStale(paths, state); err != nil {
			return runtimeStatus{}, err
		}
	}
	_ = os.Remove(paths.failure)
	command := exec.Command(self.Path, "runtime", "--candidate", candidate, "--bootstrap-sha256", bootstrapSHA, "--host-socket", hostSocket)
	command.Env = selectedRuntimeEnvironment()
	command.Dir = paths.dataRoot
	command.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	null, err := os.OpenFile(os.DevNull, os.O_RDWR, 0)
	if err != nil {
		return runtimeStatus{}, fail("DIRECTOR_BOOTSTRAP_LAUNCH_FAILED")
	}
	defer null.Close()
	command.Stdin, command.Stdout, command.Stderr = null, null, null
	if err := command.Start(); err != nil {
		return runtimeStatus{}, fail("DIRECTOR_BOOTSTRAP_LAUNCH_FAILED")
	}
	_ = command.Process.Release()
	deadline := time.Now().Add(startTimeout)
	for time.Now().Before(deadline) {
		if content, readErr := os.ReadFile(paths.failure); readErr == nil {
			var failure runtimeFailure
			if json.Unmarshal(content, &failure) == nil && failure.SchemaVersion == 1 {
				if failure.Port > 0 {
					return runtimeStatus{}, failOccupiedPort(failure.Code, failure.Port, failure.Holder, failure.PID)
				}
				return runtimeStatus{}, fail(failure.Code)
			}
			return runtimeStatus{}, fail("DIRECTOR_BOOTSTRAP_LAUNCH_FAILED")
		}
		status, requestErr := requestControl(paths, controlRequest{SchemaVersion: controlProtocolVersion, Token: controlToken, Command: "ensure"})
		if requestErr == nil && status.Binding == binding && status.State == "current" {
			return status, nil
		}
		time.Sleep(100 * time.Millisecond)
	}
	return runtimeStatus{}, fail("DIRECTOR_BOOTSTRAP_START_TIMEOUT")
}

func selectedRuntimeEnvironment() []string {
	home, _ := os.UserHomeDir()
	values := map[string]string{"HOME": home, "DOLT_DISABLE_VERSION_CHECK": "1"}
	for _, name := range append(runtimeXDGVariables[:], doltPortVariable, enginePortVariable) {
		if value := os.Getenv(name); value != "" {
			values[name] = value
		}
	}
	return sortedEnvironment(values)
}

type childProcess struct {
	command *exec.Cmd
	exited  chan error
}

func portInUse(port int) bool {
	connection, err := net.DialTimeout("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(port)), 250*time.Millisecond)
	if err != nil {
		return false
	}
	connection.Close()
	return true
}

var runtimePortInUse = portInUse

// answersLoopback reports whether a /proc local address would answer the
// 127.0.0.1 dial this runtime makes: loopback itself or an unspecified
// wildcard, in either family.
func answersLoopback(address string) bool {
	switch strings.ToUpper(address) {
	case "0100007F", "00000000",
		"00000000000000000000000000000000", "00000000000000000000000001000000",
		"0000000000000000FFFF000000000000", "0000000000000000FFFF00000100007F":
		return true
	}
	return false
}

// listeningSocketInodes collects the inodes of the sockets listening on port
// which would answer a loopback connection, from both IPv4 and IPv6 tables.
func listeningSocketInodes(port int) map[string]struct{} {
	inodes := map[string]struct{}{}
	for _, table := range []string{"/proc/net/tcp", "/proc/net/tcp6"} {
		content, err := os.ReadFile(table)
		if err != nil {
			continue
		}
		for _, line := range strings.Split(string(content), "\n") {
			fields := strings.Fields(line)
			if len(fields) < 10 || fields[3] != "0A" {
				continue
			}
			separator := strings.LastIndex(fields[1], ":")
			if separator < 0 || !answersLoopback(fields[1][:separator]) {
				continue
			}
			value, err := strconv.ParseUint(fields[1][separator+1:], 16, 32)
			if err != nil || int(value) != port {
				continue
			}
			inodes[fields[9]] = struct{}{}
		}
	}
	return inodes
}

// holderProcess is what this user could read about the process holding a
// required listener. An empty executable means the process exists but could
// not be identified, which is never proof of anything.
type holderProcess struct {
	pid        int
	executable string
	arguments  []string
}

func processArguments(pid int) []string {
	content, err := os.ReadFile(fmt.Sprintf("/proc/%d/cmdline", pid))
	if err != nil {
		return nil
	}
	return strings.FieldsFunc(string(content), func(value rune) bool { return value == 0 })
}

// portHolder resolves the process listening on a loopback port. A listener
// owned by another user stays unresolved, which the caller reports as unproved
// rather than as another Director.
func portHolder(port int) holderProcess {
	inodes := listeningSocketInodes(port)
	if len(inodes) == 0 {
		return holderProcess{}
	}
	entries, err := os.ReadDir("/proc")
	if err != nil {
		return holderProcess{}
	}
	for _, entry := range entries {
		pid, err := strconv.Atoi(entry.Name())
		if err != nil || pid <= 0 {
			continue
		}
		descriptors, err := os.ReadDir(fmt.Sprintf("/proc/%d/fd", pid))
		if err != nil {
			continue
		}
		for _, descriptor := range descriptors {
			link, err := os.Readlink(fmt.Sprintf("/proc/%d/fd/%s", pid, descriptor.Name()))
			if err != nil || !strings.HasPrefix(link, "socket:[") || !strings.HasSuffix(link, "]") {
				continue
			}
			if _, ok := inodes[link[len("socket:["):len(link)-1]]; ok {
				return holderProcess{pid: pid, executable: processExecutable(pid), arguments: processArguments(pid)}
			}
		}
	}
	return holderProcess{}
}

var runtimePortHolder = portHolder

// preparedCacheExecutable reports whether a resolved executable is published
// below a Director prepared-engine cache under its pinned name.
func preparedCacheExecutable(path, name string) bool {
	if filepath.Base(path) != name {
		return false
	}
	segments := strings.Split(filepath.ToSlash(path), "/")
	for index := 0; index+1 < len(segments); index++ {
		if segments[index] == "director" && segments[index+1] == "engines" {
			return true
		}
	}
	return false
}

func hasArgument(arguments []string, value string) bool {
	for _, argument := range arguments {
		if argument == value {
			return true
		}
	}
	return false
}

// directorRuntimeChild proves that the holder of this exact port is a child a
// Director runtime supervises, not merely an executable with a Director-shaped
// name and path. It requires the pinned executable and the argument vector
// only this bootstrap produces, bound to the port being refused.
func directorRuntimeChild(port int, holder holderProcess) bool {
	if holder.pid <= 0 || holder.executable == "" || len(holder.arguments) < 2 {
		return false
	}
	switch holder.arguments[1] {
	case "serve-board":
		if !preparedCacheExecutable(holder.executable, "director-engine") ||
			!hasArgument(holder.arguments, "--listen=127.0.0.1:"+strconv.Itoa(port)) ||
			!hasArgument(holder.arguments, "--host-label=Director") {
			return false
		}
		for _, argument := range holder.arguments {
			if identity, found := strings.CutPrefix(argument, "--host-id="); found && hostIDPattern.MatchString(identity) {
				return true
			}
		}
		return false
	case "sql-server":
		if !preparedCacheExecutable(holder.executable, "dolt") ||
			!hasArgument(holder.arguments, "--host=127.0.0.1") ||
			!hasArgument(holder.arguments, "--port="+strconv.Itoa(port)) {
			return false
		}
		for _, argument := range holder.arguments {
			if socket, found := strings.CutPrefix(argument, "--socket="); found &&
				strings.HasSuffix(filepath.ToSlash(socket), "/director/supervisor/dolt/mysql.sock") {
				return true
			}
		}
		return false
	}
	return false
}

// refuseOccupiedPort fails closed on any occupied required listener and names
// what holds it. Only a proved Director runtime child reports the external-owner
// refusal; an unrelated or unidentifiable process reports an occupied port and
// is never described as another Director.
func refuseOccupiedPort(port int) error {
	if !runtimePortInUse(port) {
		return nil
	}
	holder := runtimePortHolder(port)
	switch {
	case directorRuntimeChild(port, holder):
		return failOccupiedPort("DIRECTOR_BOOTSTRAP_EXTERNAL_OWNER", port, "director", holder.pid)
	case holder.pid > 0 && holder.executable != "":
		return failOccupiedPort("DIRECTOR_BOOTSTRAP_PORT_OCCUPIED", port, "foreign", holder.pid)
	default:
		return failOccupiedPort("DIRECTOR_BOOTSTRAP_PORT_OCCUPIED", port, "unproved", holder.pid)
	}
}

func waitPort(port int, child *childProcess) error {
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		select {
		case <-child.exited:
			return fail("DIRECTOR_BOOTSTRAP_CHILD_EXITED")
		default:
		}
		if portInUse(port) {
			return nil
		}
		time.Sleep(100 * time.Millisecond)
	}
	return fail("DIRECTOR_BOOTSTRAP_CHILD_TIMEOUT")
}

func verifyStartedChild(child *childProcess, executable string) error {
	if child == nil || child.command == nil || child.command.Process == nil {
		return fail("DIRECTOR_BOOTSTRAP_CHILD_IDENTITY")
	}
	pid := child.command.Process.Pid
	if processStart(pid) == "" || processExecutable(pid) != executable {
		return fail("DIRECTOR_BOOTSTRAP_CHILD_IDENTITY")
	}
	return nil
}

func startChild(path string, args []string, cwd string) (*childProcess, error) {
	command := exec.Command(path, args...)
	command.Dir = cwd
	command.Env = selectedRuntimeEnvironment()
	null, err := os.OpenFile(os.DevNull, os.O_RDWR, 0)
	if err != nil {
		return nil, fail("DIRECTOR_BOOTSTRAP_CHILD_START")
	}
	command.Stdin, command.Stdout, command.Stderr = null, null, null
	if err := command.Start(); err != nil {
		null.Close()
		return nil, fail("DIRECTOR_BOOTSTRAP_CHILD_START")
	}
	null.Close()
	child := &childProcess{command: command, exited: make(chan error, 1)}
	go func() { child.exited <- command.Wait(); close(child.exited) }()
	return child, nil
}

func stopChild(child *childProcess) {
	if child == nil || child.command.Process == nil {
		return
	}
	_ = child.command.Process.Signal(syscall.SIGTERM)
	select {
	case <-child.exited:
	case <-time.After(5 * time.Second):
		_ = child.command.Process.Kill()
		<-child.exited
	}
}

func runBounded(path string, args []string, cwd string, timeout time.Duration) error {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	result, err := runCommand(ctx, path, args, cwd, mapFromEnvironment(selectedRuntimeEnvironment()))
	if err != nil {
		if strings.Contains(result.stderr, "DIRECTOR_TASKSTORE_CONFIG_OUTPUT_MISMATCH") {
			return fail("DIRECTOR_TASKSTORE_CONFIG_OUTPUT_MISMATCH")
		}
		return fail("DIRECTOR_BOOTSTRAP_COMMAND_FAILED")
	}
	return nil
}

var executeRuntimeCommand = runBounded

func mapFromEnvironment(environment []string) map[string]string {
	result := map[string]string{}
	for _, value := range environment {
		if index := strings.IndexByte(value, '='); index > 0 {
			result[value[:index]] = value[index+1:]
		}
	}
	return result
}

func initializeTaskStore(paths runtimePaths, prepared preparedRuntime, identity hostIdentity) error {
	for _, path := range []string{paths.doltRoot, paths.doltDatabase, paths.doltConfig, paths.credentials, filepath.Dir(paths.doltSocket), paths.workRoot} {
		if err := ensurePrivateDir(path); err != nil {
			return err
		}
	}
	if _, err := os.Lstat(filepath.Join(paths.doltDatabase, ".dolt")); os.IsNotExist(err) {
		if err := executeRuntimeCommand(prepared.Dolt.Binary.Path, []string{"init", "--name", "Director", "--email", "director@localhost.invalid"}, paths.doltDatabase, 2*time.Minute); err != nil {
			return fail("DIRECTOR_TASKSTORE_INIT_FAILED")
		}
	}
	return nil
}

// admitIsolatedTaskStore refuses an isolated runtime whose data root already
// holds a TaskStore that is not its own. The path baseline compares against
// where a default installation would live, which is not where an instance
// started with its own XDG bases actually lives, so reusing another instance's
// XDG_DATA_HOME would otherwise reach the point of starting a second Dolt over
// its TaskStore with only Dolt's own server lock in between.
func admitIsolatedTaskStore(paths runtimePaths, identity hostIdentity) error {
	if _, err := os.Lstat(filepath.Join(paths.doltDatabase, ".dolt")); err != nil {
		return nil
	}
	// A TaskStore is present. It is this runtime's own only when this
	// runtime's own configuration claims it, either under its identity or
	// under the legacy identity its first start migrates.
	storeID, _, err := taskStoreIdentity(paths.taskstore)
	if err != nil || (storeID != identity.ID && storeID != "local-paseo") {
		return fail("DIRECTOR_BOOTSTRAP_ISOLATION_FOREIGN_TASKSTORE")
	}
	return nil
}

func bootstrapTaskStore(paths runtimePaths, prepared preparedRuntime, identity hostIdentity, ports runtimePorts) error {
	control, err := privateSecret(filepath.Join(paths.credentials, "control.password"))
	if err != nil {
		return err
	}
	_ = control
	writer, err := privateSecret(filepath.Join(paths.credentials, "writer.password"))
	if err != nil {
		return err
	}
	_ = writer
	maintenance, err := privateSecret(filepath.Join(paths.credentials, "maintenance.password"))
	if err != nil {
		return err
	}
	_ = maintenance
	provisionArgs := func(output string) []string {
		return []string{"bootstrap-taskstore", "--config-output", output, "--address", fmt.Sprintf("127.0.0.1:%d", ports.dolt), "--database", "director", "--store-id", identity.ID,
			"--owner-user", "root", "--control-user", "director_control", "--writer-user", "director_writer", "--maintenance-user", "director_maintenance",
			"--control-password-file", filepath.Join(paths.credentials, "control.password"), "--writer-password-file", filepath.Join(paths.credentials, "writer.password"),
			"--maintenance-password-file", filepath.Join(paths.credentials, "maintenance.password"), "--privilege-file", paths.privilegeFile}
	}
	if _, err := os.Lstat(paths.taskstore); os.IsNotExist(err) {
		if err := executeRuntimeCommand(prepared.Engine.Binary.Path, provisionArgs(paths.taskstore), paths.dataRoot, 15*time.Minute); err != nil {
			return fail(codeOf(err, "DIRECTOR_TASKSTORE_BOOTSTRAP_FAILED"))
		}
		return nil
	}
	if err := executeRuntimeCommand(prepared.Engine.Binary.Path, []string{"bootstrap-taskstore", "--taskstore-config", paths.taskstore}, paths.dataRoot, 15*time.Minute); err != nil {
		return fail(codeOf(err, "DIRECTOR_TASKSTORE_BOOTSTRAP_FAILED"))
	}
	storeID, original, err := taskStoreIdentity(paths.taskstore)
	if err != nil {
		return err
	}
	if storeID == identity.ID {
		return nil
	}
	if storeID != "local-paseo" {
		return fail("DIRECTOR_TASKSTORE_IDENTITY_MISMATCH")
	}
	migration := filepath.Join(paths.configRoot, "taskstore.identity-migration.json")
	if _, err := os.Lstat(migration); os.IsNotExist(err) {
		if err := executeRuntimeCommand(prepared.Engine.Binary.Path, provisionArgs(migration), paths.dataRoot, 15*time.Minute); err != nil {
			return fail(codeOf(err, "DIRECTOR_TASKSTORE_IDENTITY_MIGRATION_FAILED"))
		}
	}
	if err := executeRuntimeCommand(prepared.Engine.Binary.Path, []string{"bootstrap-taskstore", "--taskstore-config", migration}, paths.dataRoot, 15*time.Minute); err != nil {
		return fail("DIRECTOR_TASKSTORE_IDENTITY_MIGRATION_FAILED")
	}
	migratedID, _, err := taskStoreIdentity(migration)
	if err != nil || migratedID != identity.ID {
		return fail("DIRECTOR_TASKSTORE_IDENTITY_MIGRATION_FAILED")
	}
	backup := filepath.Join(paths.configRoot, "taskstore.legacy-local-paseo.json")
	if existing, readErr := os.ReadFile(backup); readErr == nil {
		if !bytes.Equal(existing, original) {
			return fail("DIRECTOR_TASKSTORE_IDENTITY_MIGRATION_POISONED")
		}
	} else if os.IsNotExist(readErr) {
		if err := writePrivateAtomic(backup, original, 0o400); err != nil {
			return err
		}
	} else {
		return fail("DIRECTOR_TASKSTORE_IDENTITY_MIGRATION_POISONED")
	}
	if err := os.Rename(migration, paths.taskstore); err != nil || fsyncDir(paths.configRoot) != nil {
		return fail("DIRECTOR_TASKSTORE_IDENTITY_MIGRATION_FAILED")
	}
	return executeRuntimeCommand(prepared.Engine.Binary.Path, []string{"bootstrap-taskstore", "--taskstore-config", paths.taskstore}, paths.dataRoot, 15*time.Minute)
}

func taskStoreIdentity(path string) (string, []byte, error) {
	info, err := privateRegular(path, maximumManifestBytes, false)
	if err != nil || info.Mode().Perm() != 0o600 {
		return "", nil, fail("DIRECTOR_TASKSTORE_IDENTITY_INVALID")
	}
	content, err := os.ReadFile(path)
	if err != nil {
		return "", nil, fail("DIRECTOR_TASKSTORE_IDENTITY_INVALID")
	}
	var document map[string]any
	if json.Unmarshal(content, &document) != nil {
		return "", nil, fail("DIRECTOR_TASKSTORE_IDENTITY_INVALID")
	}
	storeID, ok := document["storeId"].(string)
	if !ok || (storeID != "local-paseo" && !hostIDPattern.MatchString(storeID)) {
		return "", nil, fail("DIRECTOR_TASKSTORE_IDENTITY_INVALID")
	}
	return storeID, content, nil
}

type runtimeController struct {
	mu                sync.Mutex
	paths             runtimePaths
	ports             runtimePorts
	prepared          preparedRuntime
	identity          hostIdentity
	binding           string
	hostSocket        string
	controlToken      string
	projectAdminToken string
	state             string
	restarts          int
	leaseDeadline     time.Time
	dolt              *childProcess
	engine            *childProcess
	listener          net.Listener
	stop              chan struct{}
	stopping          bool
}

func (controller *runtimeController) snapshot() runtimeStatus {
	controller.mu.Lock()
	defer controller.mu.Unlock()
	status := runtimeStatus{SchemaVersion: 1, Code: "DIRECTOR_BOOTSTRAP_RUNTIME_STATUS", State: controller.state, Binding: controller.binding,
		EngineAddress: engineAddress(controller.ports), Host: controller.identity, Engine: controller.prepared.Engine, Dolt: controller.prepared.Dolt,
		RestartCount: controller.restarts, ProjectAdminAuthorization: controller.projectAdminToken}
	if controller.engine != nil && controller.engine.command.Process != nil {
		status.EnginePID = controller.engine.command.Process.Pid
	}
	if controller.dolt != nil && controller.dolt.command.Process != nil {
		status.DoltPID = controller.dolt.command.Process.Pid
	}
	return status
}

func (controller *runtimeController) writeState() error {
	status := controller.snapshot()
	state := runtimeState{SchemaVersion: 1, Binding: controller.binding, PID: os.Getpid(), ProcessStart: processStart(os.Getpid()), Status: status.State,
		EnginePID: status.EnginePID, EngineExecutable: controller.prepared.Engine.Binary.Path,
		DoltPID: status.DoltPID, DoltExecutable: controller.prepared.Dolt.Binary.Path}
	if state.EnginePID > 0 {
		state.EngineProcessStart = processStart(state.EnginePID)
	}
	if state.DoltPID > 0 {
		state.DoltProcessStart = processStart(state.DoltPID)
	}
	content, _ := json.Marshal(state)
	return writePrivateAtomic(controller.paths.state, append(content, '\n'), 0o600)
}

func (controller *runtimeController) startChildren() error {
	for _, port := range []int{controller.ports.dolt, controller.ports.engine} {
		if err := refuseOccupiedPort(port); err != nil {
			return err
		}
	}
	if controller.ports.isolated {
		if err := admitIsolatedTaskStore(controller.paths, controller.identity); err != nil {
			return err
		}
	}
	if err := initializeTaskStore(controller.paths, controller.prepared, controller.identity); err != nil {
		return err
	}
	dolt, err := startChild(controller.prepared.Dolt.Binary.Path, []string{"sql-server", "--host=127.0.0.1", fmt.Sprintf("--port=%d", controller.ports.dolt), "--data-dir=" + controller.paths.doltRoot, "--doltcfg-dir=" + controller.paths.doltConfig, "--socket=" + controller.paths.doltSocket, "--loglevel=warning"}, controller.paths.doltDatabase)
	if err != nil {
		return err
	}
	controller.mu.Lock()
	controller.dolt = dolt
	controller.mu.Unlock()
	if err := waitPort(controller.ports.dolt, dolt); err != nil {
		stopChild(dolt)
		return err
	}
	if err := verifyStartedChild(dolt, controller.prepared.Dolt.Binary.Path); err != nil {
		stopChild(dolt)
		return err
	}
	if err := bootstrapTaskStore(controller.paths, controller.prepared, controller.identity, controller.ports); err != nil {
		stopChild(dolt)
		return err
	}
	engine, err := startChild(controller.prepared.Engine.Binary.Path, []string{"serve-board", "--listen=" + engineAddress(controller.ports), "--taskstore-config=" + controller.paths.taskstore,
		"--host-id=" + controller.identity.ID, "--host-label=" + controller.identity.Label, "--host-socket=" + controller.hostSocket,
		"--runtime-root=" + controller.paths.workRoot, "--project-admin-token-file=" + controller.paths.projectAdminToken}, controller.paths.dataRoot)
	if err != nil {
		stopChild(dolt)
		return err
	}
	controller.mu.Lock()
	controller.engine = engine
	controller.mu.Unlock()
	if err := waitPort(controller.ports.engine, engine); err != nil {
		stopChild(engine)
		stopChild(dolt)
		return err
	}
	if err := verifyStartedChild(engine, controller.prepared.Engine.Binary.Path); err != nil {
		stopChild(engine)
		stopChild(dolt)
		return err
	}
	controller.mu.Lock()
	controller.state = "current"
	controller.restarts = 0
	controller.mu.Unlock()
	return controller.writeState()
}

func (controller *runtimeController) stopChildren() {
	controller.mu.Lock()
	engine, dolt := controller.engine, controller.dolt
	controller.engine, controller.dolt = nil, nil
	controller.mu.Unlock()
	stopChild(engine)
	stopChild(dolt)
}

func (controller *runtimeController) handle(connection net.Conn) {
	defer connection.Close()
	_ = connection.SetDeadline(time.Now().Add(3 * time.Second))
	line, err := bufio.NewReader(io.LimitReader(connection, maximumManifestBytes+1)).ReadBytes('\n')
	if err != nil || len(line) > maximumManifestBytes {
		return
	}
	var request controlRequest
	decoder := json.NewDecoder(bytes.NewReader(line))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&request) != nil || request.SchemaVersion != 1 || request.Token != controller.controlToken ||
		(request.Command != "status" && request.Command != "ensure" && request.Command != "release") {
		_, _ = connection.Write([]byte("{\"schemaVersion\":1,\"code\":\"DIRECTOR_BOOTSTRAP_CONTROL_REFUSED\"}\n"))
		return
	}
	controller.mu.Lock()
	if request.Command == "ensure" {
		controller.leaseDeadline = time.Now().Add(leaseDuration)
	}
	controller.mu.Unlock()
	content, _ := json.Marshal(controller.snapshot())
	_, _ = connection.Write(append(content, '\n'))
	if request.Command == "release" {
		controller.mu.Lock()
		if !controller.stopping {
			controller.stopping = true
			close(controller.stop)
		}
		controller.mu.Unlock()
	}
}

func (controller *runtimeController) serveControl() error {
	if _, err := os.Lstat(controller.paths.controlSocket); err == nil {
		return fail("DIRECTOR_BOOTSTRAP_CONTROL_OWNERSHIP")
	}
	listener, err := net.Listen("unix", controller.paths.controlSocket)
	if err != nil {
		return fail("DIRECTOR_BOOTSTRAP_CONTROL_OWNERSHIP")
	}
	if err := os.Chmod(controller.paths.controlSocket, 0o600); err != nil {
		listener.Close()
		return fail("DIRECTOR_BOOTSTRAP_CONTROL_OWNERSHIP")
	}
	controller.listener = listener
	go func() {
		for {
			connection, err := listener.Accept()
			if err != nil {
				return
			}
			go controller.handle(connection)
		}
	}()
	return nil
}

func (controller *runtimeController) monitor() error {
	ticker := time.NewTicker(250 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-controller.stop:
			return nil
		case <-ticker.C:
			controller.mu.Lock()
			expired := time.Now().After(controller.leaseDeadline)
			engine, dolt := controller.engine, controller.dolt
			state := controller.state
			controller.mu.Unlock()
			if expired {
				return nil
			}
			failed := false
			if state == "current" {
				select {
				case <-engine.exited:
					failed = true
				default:
				}
				select {
				case <-dolt.exited:
					failed = true
				default:
				}
			}
			if !failed {
				continue
			}
			controller.mu.Lock()
			controller.state = "degraded"
			controller.restarts++
			attempts := controller.restarts
			controller.mu.Unlock()
			_ = controller.writeState()
			controller.stopChildren()
			if attempts > 3 {
				continue
			}
			time.Sleep(time.Duration(1<<(attempts-1)) * 500 * time.Millisecond)
			if err := controller.startChildren(); err != nil {
				continue
			}
		}
	}
}

func runRuntime(candidate, bootstrapSHA, hostSocket string) error {
	paths, ports, err := runtimeLayout()
	if err != nil {
		return err
	}
	prepared, err := loadPrepared(paths, candidate, bootstrapSHA)
	if err != nil {
		return err
	}
	self, err := selfExecutable(bootstrapSHA)
	if err != nil || self.Path != prepared.Bootstrap.Path {
		return fail("DIRECTOR_BOOTSTRAP_SELF_IDENTITY")
	}
	identity, err := loadOrCreateHostIdentity(paths.hostIdentity)
	if err != nil {
		return err
	}
	controlToken, err := privateSecret(paths.controlToken)
	if err != nil {
		return err
	}
	projectToken, err := privateSecret(paths.projectAdminToken)
	if err != nil {
		return err
	}
	controller := &runtimeController{paths: paths, ports: ports, prepared: prepared, identity: identity, binding: runtimeBinding(prepared, identity, hostSocket, ports), hostSocket: hostSocket,
		controlToken: controlToken, projectAdminToken: projectToken, state: "degraded", leaseDeadline: time.Now().Add(leaseDuration), stop: make(chan struct{})}
	if err := controller.serveControl(); err != nil {
		return err
	}
	if err := controller.writeState(); err != nil {
		controller.listener.Close()
		return err
	}
	if err := controller.startChildren(); err != nil {
		controller.listener.Close()
		return err
	}
	err = controller.monitor()
	controller.stopChildren()
	controller.listener.Close()
	for _, path := range []string{paths.controlSocket, paths.state, paths.failure} {
		_ = os.Remove(path)
	}
	return err
}

// runtimeFailure is the closed document the supervisor leaves for the ensure
// that started it. It carries no free-form text, only the refusal code and the
// occupied-listener facts an operator needs to act.
type runtimeFailure struct {
	SchemaVersion int    `json:"schemaVersion"`
	Code          string `json:"code"`
	Port          int    `json:"port,omitempty"`
	Holder        string `json:"holder,omitempty"`
	PID           int    `json:"pid,omitempty"`
}

func writeRuntimeFailure(err error) {
	// A runtime whose layout was never admitted owns no runtime root, so it
	// must not leave a failure document in another instance's state.
	paths, _, layoutErr := runtimeLayout()
	if layoutErr != nil {
		return
	}
	port, holder, pid := occupiedPortOf(err)
	content, _ := json.Marshal(runtimeFailure{SchemaVersion: 1, Code: codeOf(err, "DIRECTOR_BOOTSTRAP_RUNTIME_FAILED"), Port: port, Holder: holder, PID: pid})
	_ = writePrivateAtomic(paths.failure, append(content, '\n'), 0o600)
}
