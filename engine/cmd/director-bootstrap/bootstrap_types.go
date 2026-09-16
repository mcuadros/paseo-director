// SPDX-License-Identifier: Apache-2.0

package main

import (
	"bytes"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"syscall"
)

const (
	target                 = "linux-amd64"
	mainEngineVersion      = "0.0.0-main"
	requiredGoVersion      = "go1.26.5"
	maximumManifestBytes   = 64 * 1024
	maximumEngineBytes     = 256 * 1024 * 1024
	maximumNoticesBytes    = 16 * 1024 * 1024
	maximumDoltArchive     = 512 * 1024 * 1024
	maximumCommandOutput   = 128 * 1024
	defaultEnginePort      = 7041
	defaultDoltPort        = 3307
	controlProtocolVersion = 1
	doltPortVariable       = "DIRECTOR_RUNTIME_DOLT_PORT"
	enginePortVariable     = "DIRECTOR_RUNTIME_ENGINE_PORT"
	lowestIsolatedPort     = 1024
	highestPort            = 65535
)

var (
	bootstrapVersion   = "0.0.0-bootstrap"
	bootstrapMode      = "development"
	bootstrapCandidate = "uncommitted"
	sha256Pattern      = regexp.MustCompile(`^[0-9a-f]{64}$`)
	gitSHAPattern      = regexp.MustCompile(`^[0-9a-f]{40}$`)
	hostIDPattern      = regexp.MustCompile(`^director-[0-9a-f]{32}$`)
	// engineAddressPattern is the exact loopback address shape the connector
	// must bind its Director transports to. The launcher mirrors it.
	engineAddressPattern = regexp.MustCompile(`^127\.0\.0\.1:([1-9][0-9]{0,4})$`)
)

type bootstrapError struct {
	code   string
	port   int
	holder string
	pid    int
}

func (e *bootstrapError) Error() string { return e.code }

func fail(code string) error { return &bootstrapError{code: code} }

// failOccupiedPort carries the closed occupied-listener detail an operator
// needs: which loopback port refused startup, whether a Director runtime was
// proved to hold it, and the holding process when this user can read it.
func failOccupiedPort(code string, port int, holder string, pid int) error {
	return &bootstrapError{code: code, port: port, holder: holder, pid: pid}
}

func occupiedPortOf(err error) (int, string, int) {
	var typed *bootstrapError
	if errors.As(err, &typed) {
		return typed.port, typed.holder, typed.pid
	}
	return 0, "", 0
}

// occupiedPortMessage renders the one human line printed beside the code. It
// never claims another Director owns a listener that was not proved to be one.
func occupiedPortMessage(port int, holder string, pid int) string {
	switch holder {
	case "director":
		return fmt.Sprintf("127.0.0.1:%d is held by another Director runtime (pid %d). Leave it running; start this runtime in isolation with %s, %s and private XDG paths.", port, pid, doltPortVariable, enginePortVariable)
	case "foreign":
		return fmt.Sprintf("127.0.0.1:%d is occupied by pid %d, which is not a Director runtime. Director started nothing and signalled nothing; free the port or isolate this runtime with %s and %s.", port, pid, doltPortVariable, enginePortVariable)
	case "unproved":
		if pid > 0 {
			return fmt.Sprintf("127.0.0.1:%d is occupied by pid %d, which this user cannot identify, so no Director runtime was proved to own it. Director started nothing and signalled nothing; free the port or isolate this runtime with %s and %s.", port, pid, doltPortVariable, enginePortVariable)
		}
	}
	return fmt.Sprintf("127.0.0.1:%d is occupied by a process this user cannot read, so no Director runtime was proved to own it. Director started nothing and signalled nothing; free the port or isolate this runtime with %s and %s.", port, doltPortVariable, enginePortVariable)
}

// validEngineAddress accepts only the exact loopback address shape, with a
// real port, that the connector may bind its Director transports to.
func validEngineAddress(value string) bool {
	match := engineAddressPattern.FindStringSubmatch(value)
	if match == nil {
		return false
	}
	port, err := strconv.Atoi(match[1])
	return err == nil && port <= highestPort
}

func codeOf(err error, fallback string) string {
	var typed *bootstrapError
	if errors.As(err, &typed) && regexp.MustCompile(`^[A-Z][A-Z0-9_]{2,95}$`).MatchString(typed.code) {
		return typed.code
	}
	return fallback
}

type releaseAsset struct {
	Name   string `json:"name"`
	URL    string `json:"url"`
	SHA256 string `json:"sha256"`
	Size   int64  `json:"size"`
}

type doltPin struct {
	SchemaVersion    int          `json:"schemaVersion,omitempty"`
	Version          string       `json:"version"`
	Target           string       `json:"target,omitempty"`
	Archive          releaseAsset `json:"archive"`
	ExecutableSHA256 string       `json:"executableSha256"`
	ExecutableSize   int64        `json:"executableSize"`
}

type engineReleaseManifest struct {
	SchemaVersion   int          `json:"schemaVersion"`
	State           string       `json:"state"`
	Version         string       `json:"version"`
	Target          string       `json:"target"`
	SourceCandidate string       `json:"sourceCandidate,omitempty"`
	Binary          releaseAsset `json:"binary,omitempty"`
	Notices         releaseAsset `json:"notices,omitempty"`
	Dolt            *doltPin     `json:"dolt,omitempty"`
}

type bootstrapReleaseManifest struct {
	SchemaVersion   int          `json:"schemaVersion"`
	State           string       `json:"state"`
	Version         string       `json:"version"`
	Target          string       `json:"target"`
	SourceCandidate string       `json:"sourceCandidate,omitempty"`
	Binary          releaseAsset `json:"binary,omitempty"`
}

type preparedExecutable struct {
	Path   string `json:"path"`
	SHA256 string `json:"sha256"`
	Size   int64  `json:"size"`
}

type preparedEngine struct {
	Mode            string             `json:"mode"`
	Version         string             `json:"version"`
	SourceCandidate string             `json:"sourceCandidate"`
	Target          string             `json:"target"`
	Binary          preparedExecutable `json:"binary"`
	Notices         preparedExecutable `json:"notices"`
	ContractVersion string             `json:"contractVersion"`
	ContractSHA256  string             `json:"contractSha256"`
}

type preparedDolt struct {
	Version       string             `json:"version"`
	Target        string             `json:"target"`
	Binary        preparedExecutable `json:"binary"`
	ArchiveSHA256 string             `json:"archiveSha256"`
}

type preparedRuntime struct {
	SchemaVersion   int                `json:"schemaVersion"`
	Channel         string             `json:"channel"`
	SourceCandidate string             `json:"sourceCandidate"`
	Target          string             `json:"target"`
	Bootstrap       preparedExecutable `json:"bootstrap"`
	Engine          preparedEngine     `json:"engine"`
	Dolt            preparedDolt       `json:"dolt"`
}

type prepareResult struct {
	SchemaVersion   int                `json:"schemaVersion"`
	Code            string             `json:"code"`
	Channel         string             `json:"channel"`
	SourceCandidate string             `json:"sourceCandidate"`
	Bootstrap       preparedExecutable `json:"bootstrap"`
	Engine          preparedEngine     `json:"engine"`
	Dolt            preparedDolt       `json:"dolt"`
	EngineBuilds    int                `json:"engineBuilds"`
}

type hostIdentity struct {
	SchemaVersion int    `json:"schemaVersion"`
	ID            string `json:"id"`
	Label         string `json:"label"`
}

type runtimeState struct {
	SchemaVersion      int    `json:"schemaVersion"`
	Binding            string `json:"binding"`
	PID                int    `json:"pid"`
	ProcessStart       string `json:"processStart"`
	Status             string `json:"status"`
	EnginePID          int    `json:"enginePid"`
	EngineProcessStart string `json:"engineProcessStart"`
	EngineExecutable   string `json:"engineExecutable"`
	DoltPID            int    `json:"doltPid"`
	DoltProcessStart   string `json:"doltProcessStart"`
	DoltExecutable     string `json:"doltExecutable"`
}

type controlRequest struct {
	SchemaVersion int    `json:"schemaVersion"`
	Token         string `json:"token"`
	Command       string `json:"command"`
}

type runtimeStatus struct {
	SchemaVersion             int            `json:"schemaVersion"`
	Code                      string         `json:"code"`
	State                     string         `json:"state"`
	Binding                   string         `json:"binding"`
	EngineAddress             string         `json:"engineAddress"`
	Host                      hostIdentity   `json:"host"`
	Engine                    preparedEngine `json:"engine"`
	Dolt                      preparedDolt   `json:"dolt"`
	EnginePID                 int            `json:"enginePid"`
	DoltPID                   int            `json:"doltPid"`
	RestartCount              int            `json:"restartCount"`
	ProjectAdminAuthorization string         `json:"projectAdminAuthorization"`
}

type runtimePaths struct {
	runtimeRoot       string
	controlSocket     string
	state             string
	failure           string
	lock              string
	dataRoot          string
	configRoot        string
	cacheRoot         string
	controlToken      string
	projectAdminToken string
	hostIdentity      string
	taskstore         string
	credentials       string
	doltRoot          string
	doltDatabase      string
	doltConfig        string
	doltSocket        string
	privilegeFile     string
	workRoot          string
}

func digestBytes(value []byte) string {
	sum := sha256.Sum256(value)
	return hex.EncodeToString(sum[:])
}

func digestFile(path string, maximum int64) (string, int64, error) {
	info, err := privateRegular(path, maximum, false)
	if err != nil {
		return "", 0, err
	}
	bytesValue, err := os.ReadFile(path)
	if err != nil {
		return "", 0, fail("DIRECTOR_BOOTSTRAP_FILE_IO")
	}
	return digestBytes(bytesValue), info.Size(), nil
}

func decodeStrict(path string, maximum int64, value any) error {
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 || info.Size() < 2 || info.Size() > maximum {
		return fail("DIRECTOR_BOOTSTRAP_MANIFEST_INVALID")
	}
	content, err := os.ReadFile(path)
	if err != nil {
		return fail("DIRECTOR_BOOTSTRAP_MANIFEST_INVALID")
	}
	decoder := json.NewDecoder(bytes.NewReader(content))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(value); err != nil {
		return fail("DIRECTOR_BOOTSTRAP_MANIFEST_INVALID")
	}
	if decoder.Decode(&struct{}{}) != io.EOF {
		return fail("DIRECTOR_BOOTSTRAP_MANIFEST_INVALID")
	}
	return nil
}

// runtimeXDGVariables are the bases which place one Director runtime's private
// state. An isolated second runtime must override every one of them.
var runtimeXDGVariables = [4]string{"XDG_RUNTIME_DIR", "XDG_CACHE_HOME", "XDG_CONFIG_HOME", "XDG_DATA_HOME"}

func runtimeBasePaths() (runtimePaths, error) { return resolveRuntimeBasePaths(os.Getenv) }

// defaultRuntimeBasePaths resolves where the single default installation keeps
// its state, ignoring every XDG override, so an isolated runtime can prove it
// shares nothing with it.
func defaultRuntimeBasePaths() (runtimePaths, error) {
	return resolveRuntimeBasePaths(func(string) string { return "" })
}

func resolveRuntimeBasePaths(lookup func(string) string) (runtimePaths, error) {
	home, err := os.UserHomeDir()
	if err != nil || !filepath.IsAbs(home) {
		return runtimePaths{}, fail("DIRECTOR_BOOTSTRAP_XDG_PATH")
	}
	absolute := func(value, fallback string) (string, error) {
		if value == "" {
			value = fallback
		}
		if !filepath.IsAbs(value) || filepath.Clean(value) != value {
			return "", fail("DIRECTOR_BOOTSTRAP_XDG_PATH")
		}
		return value, nil
	}
	cacheBase, err := absolute(lookup("XDG_CACHE_HOME"), filepath.Join(home, ".cache"))
	if err != nil {
		return runtimePaths{}, err
	}
	configBase, err := absolute(lookup("XDG_CONFIG_HOME"), filepath.Join(home, ".config"))
	if err != nil {
		return runtimePaths{}, err
	}
	dataBase, err := absolute(lookup("XDG_DATA_HOME"), filepath.Join(home, ".local", "share"))
	if err != nil {
		return runtimePaths{}, err
	}
	runtimeBase, err := absolute(lookup("XDG_RUNTIME_DIR"), cacheBase)
	if err != nil {
		return runtimePaths{}, err
	}
	runtimeRoot := filepath.Join(runtimeBase, "director", "supervisor")
	configRoot := filepath.Join(configBase, "director", "managed-runtime")
	dataRoot := filepath.Join(dataBase, "director")
	return runtimePaths{
		runtimeRoot: runtimeRoot, controlSocket: filepath.Join(runtimeRoot, "control.sock"),
		state: filepath.Join(runtimeRoot, "state.json"), failure: filepath.Join(runtimeRoot, "failure.json"),
		lock: filepath.Join(runtimeRoot, "launch.lock"), dataRoot: dataRoot, configRoot: configRoot,
		cacheRoot: filepath.Join(cacheBase, "director"), controlToken: filepath.Join(configRoot, "control.token"),
		projectAdminToken: filepath.Join(configRoot, "project-admin.token"),
		hostIdentity:      filepath.Join(configBase, "director", "host-identity.json"),
		taskstore:         filepath.Join(configRoot, "taskstore.json"), credentials: filepath.Join(configRoot, "credentials"),
		doltRoot:      filepath.Join(dataRoot, "taskstore", "dolt"),
		doltDatabase:  filepath.Join(dataRoot, "taskstore", "dolt", "director"),
		doltConfig:    filepath.Join(dataRoot, "taskstore", "doltcfg"),
		doltSocket:    filepath.Join(runtimeRoot, "dolt", "mysql.sock"),
		privilegeFile: filepath.Join(dataRoot, "taskstore", "doltcfg", "privileges.db"),
		workRoot:      filepath.Join(runtimeRoot, "work"),
	}, nil
}

// runtimePorts are the loopback listeners of one Director runtime. The single
// default installation uses the fixed pair; an isolated second runtime declares
// both explicitly.
type runtimePorts struct {
	dolt     int
	engine   int
	isolated bool
}

// resolveRuntimePorts reads the declared isolation ports. Declaring one without
// the other, a reserved or out-of-range port, or the same port twice is an
// incomplete isolation rather than a partially honoured one.
func resolveRuntimePorts(lookup func(string) string) (runtimePorts, error) {
	dolt, engine := strings.TrimSpace(lookup(doltPortVariable)), strings.TrimSpace(lookup(enginePortVariable))
	if dolt == "" && engine == "" {
		return runtimePorts{dolt: defaultDoltPort, engine: defaultEnginePort}, nil
	}
	doltPort, doltErr := strconv.Atoi(dolt)
	enginePort, engineErr := strconv.Atoi(engine)
	if doltErr != nil || engineErr != nil || doltPort == enginePort ||
		doltPort < lowestIsolatedPort || doltPort > highestPort || enginePort < lowestIsolatedPort || enginePort > highestPort {
		return runtimePorts{}, fail("DIRECTOR_BOOTSTRAP_ISOLATION_INCOMPLETE")
	}
	return runtimePorts{dolt: doltPort, engine: enginePort, isolated: true}, nil
}

// runtimeRoots are the directories two runtimes must never share. Overlap in
// any direction means one runtime can observe or mutate the other's TaskStore,
// identity, credentials, socket, lock or cache.
func runtimeRoots(paths runtimePaths) [5]string {
	return [5]string{paths.runtimeRoot, paths.configRoot, paths.dataRoot, paths.cacheRoot, filepath.Dir(paths.hostIdentity)}
}

func nestedPath(child, parent string) bool {
	return child == parent || strings.HasPrefix(child, parent+string(filepath.Separator))
}

// admitIsolatedPaths refuses an isolation request whose state is still the
// default installation's. Without this an isolation port pair would start a
// second Engine against the running instance's TaskStore instead of beside it.
func admitIsolatedPaths(paths runtimePaths) error {
	for _, name := range runtimeXDGVariables {
		if strings.TrimSpace(os.Getenv(name)) == "" {
			return fail("DIRECTOR_BOOTSTRAP_ISOLATION_INCOMPLETE")
		}
	}
	installed, err := defaultRuntimeBasePaths()
	if err != nil {
		return err
	}
	for _, isolated := range runtimeRoots(paths) {
		for _, standard := range runtimeRoots(installed) {
			if nestedPath(isolated, standard) || nestedPath(standard, isolated) {
				return fail("DIRECTOR_BOOTSTRAP_ISOLATION_INCOMPLETE")
			}
		}
	}
	return nil
}

// runtimeLayout resolves where this runtime lives and which loopback ports it
// owns. An isolated runtime is admitted only when its ports and its state are
// both private; isolation is never partial.
func runtimeLayout() (runtimePaths, runtimePorts, error) {
	paths, err := runtimeBasePaths()
	if err != nil {
		return runtimePaths{}, runtimePorts{}, err
	}
	ports, err := resolveRuntimePorts(os.Getenv)
	if err != nil {
		return runtimePaths{}, runtimePorts{}, err
	}
	if ports.isolated {
		if err := admitIsolatedPaths(paths); err != nil {
			return runtimePaths{}, runtimePorts{}, err
		}
	}
	return paths, ports, nil
}

func ensurePrivateDir(path string) error {
	if err := os.MkdirAll(path, 0o700); err != nil {
		return fail("DIRECTOR_BOOTSTRAP_PERMISSIONS")
	}
	info, err := os.Lstat(path)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm()&0o077 != 0 {
		return fail("DIRECTOR_BOOTSTRAP_PERMISSIONS")
	}
	if stat, ok := info.Sys().(*syscall.Stat_t); ok && int(stat.Uid) != os.Geteuid() {
		return fail("DIRECTOR_BOOTSTRAP_PERMISSIONS")
	}
	return nil
}

func privateRegular(path string, maximum int64, executable bool) (os.FileInfo, error) {
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 || info.Size() <= 0 || info.Size() > maximum || info.Mode().Perm()&0o077 != 0 {
		return nil, fail("DIRECTOR_BOOTSTRAP_CACHE_POISONED")
	}
	if executable && info.Mode().Perm()&0o100 == 0 {
		return nil, fail("DIRECTOR_BOOTSTRAP_CACHE_POISONED")
	}
	if stat, ok := info.Sys().(*syscall.Stat_t); ok {
		if int(stat.Uid) != os.Geteuid() || stat.Nlink != 1 {
			return nil, fail("DIRECTOR_BOOTSTRAP_CACHE_POISONED")
		}
	}
	return info, nil
}

func fsyncDir(path string) error {
	file, err := os.Open(path)
	if err != nil {
		return err
	}
	defer file.Close()
	return file.Sync()
}

func writePrivateAtomic(path string, content []byte, mode os.FileMode) error {
	if err := ensurePrivateDir(filepath.Dir(path)); err != nil {
		return err
	}
	temporary := filepath.Join(filepath.Dir(path), fmt.Sprintf(".partial-%d-%s", os.Getpid(), randomHex(8)))
	file, err := os.OpenFile(temporary, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return fail("DIRECTOR_BOOTSTRAP_FILE_IO")
	}
	ok := false
	defer func() {
		if !ok {
			_ = os.Remove(temporary)
		}
	}()
	if _, err := file.Write(content); err != nil {
		file.Close()
		return fail("DIRECTOR_BOOTSTRAP_FILE_IO")
	}
	if err := file.Sync(); err != nil {
		file.Close()
		return fail("DIRECTOR_BOOTSTRAP_FILE_IO")
	}
	if err := file.Close(); err != nil {
		return fail("DIRECTOR_BOOTSTRAP_FILE_IO")
	}
	if err := os.Chmod(temporary, mode); err != nil {
		return fail("DIRECTOR_BOOTSTRAP_FILE_IO")
	}
	if err := os.Rename(temporary, path); err != nil {
		return fail("DIRECTOR_BOOTSTRAP_FILE_IO")
	}
	ok = true
	if err := fsyncDir(filepath.Dir(path)); err != nil {
		return fail("DIRECTOR_BOOTSTRAP_FILE_IO")
	}
	return nil
}

func randomHex(bytesCount int) string {
	value := make([]byte, bytesCount)
	if _, err := rand.Read(value); err != nil {
		panic("secure random unavailable")
	}
	return hex.EncodeToString(value)
}

func privateSecret(path string) (string, error) {
	if _, err := os.Lstat(path); os.IsNotExist(err) {
		if err := writePrivateAtomic(path, []byte(randomHex(32)+"\n"), 0o600); err != nil {
			return "", err
		}
	}
	info, err := privateRegular(path, 1024, false)
	if err != nil || info.Mode().Perm() != 0o600 {
		return "", fail("DIRECTOR_BOOTSTRAP_CREDENTIAL_INVALID")
	}
	content, err := os.ReadFile(path)
	if err != nil {
		return "", fail("DIRECTOR_BOOTSTRAP_CREDENTIAL_INVALID")
	}
	value := strings.TrimSpace(string(content))
	if !sha256Pattern.MatchString(value) {
		return "", fail("DIRECTOR_BOOTSTRAP_CREDENTIAL_INVALID")
	}
	return value, nil
}

func loadOrCreateHostIdentity(path string) (hostIdentity, error) {
	if err := ensurePrivateDir(filepath.Dir(path)); err != nil {
		return hostIdentity{}, err
	}
	if _, err := os.Lstat(path); os.IsNotExist(err) {
		identity := hostIdentity{SchemaVersion: 1, ID: "director-" + randomHex(16), Label: "Director"}
		content, _ := json.Marshal(identity)
		// O_EXCL is the identity compare-and-swap; a concurrent winner is read back.
		file, createErr := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
		if createErr == nil {
			if _, err := file.Write(append(content, '\n')); err != nil {
				file.Close()
				return hostIdentity{}, fail("DIRECTOR_HOST_IDENTITY_IO")
			}
			if err := file.Sync(); err != nil {
				file.Close()
				return hostIdentity{}, fail("DIRECTOR_HOST_IDENTITY_IO")
			}
			if err := file.Close(); err != nil {
				return hostIdentity{}, fail("DIRECTOR_HOST_IDENTITY_IO")
			}
			if err := fsyncDir(filepath.Dir(path)); err != nil {
				return hostIdentity{}, fail("DIRECTOR_HOST_IDENTITY_IO")
			}
		}
	}
	info, err := privateRegular(path, 1024, false)
	if err != nil || info.Mode().Perm() != 0o600 {
		return hostIdentity{}, fail("DIRECTOR_HOST_IDENTITY_POISONED")
	}
	var identity hostIdentity
	if err := decodeStrict(path, 1024, &identity); err != nil || identity.SchemaVersion != 1 || !hostIDPattern.MatchString(identity.ID) || identity.Label != "Director" {
		return hostIdentity{}, fail("DIRECTOR_HOST_IDENTITY_POISONED")
	}
	return identity, nil
}

func preparedPath(paths runtimePaths, candidate string) string {
	return filepath.Join(paths.cacheRoot, "runtime", candidate, target, "prepared.json")
}

func validatePrepared(value preparedRuntime, candidate, bootstrapSHA string) error {
	if value.SchemaVersion != 1 || (value.Channel != "main" && value.Channel != "release") || value.SourceCandidate != candidate || value.Target != target ||
		value.Bootstrap.SHA256 != bootstrapSHA || !sha256Pattern.MatchString(value.Bootstrap.SHA256) || value.Bootstrap.Size <= 0 || !filepath.IsAbs(value.Bootstrap.Path) ||
		value.Engine.SourceCandidate != candidate || value.Engine.Target != target || (value.Engine.Mode != value.Channel) ||
		!sha256Pattern.MatchString(value.Engine.Binary.SHA256) || !sha256Pattern.MatchString(value.Engine.Notices.SHA256) ||
		value.Dolt.Version != "2.3.2" || value.Dolt.Target != target || !sha256Pattern.MatchString(value.Dolt.Binary.SHA256) || !sha256Pattern.MatchString(value.Dolt.ArchiveSHA256) {
		return fail("DIRECTOR_BOOTSTRAP_PREPARED_INVALID")
	}
	for _, item := range []preparedExecutable{value.Bootstrap, value.Engine.Binary, value.Engine.Notices, value.Dolt.Binary} {
		digest, size, err := digestFile(item.Path, maximumDoltArchive)
		if err != nil || digest != item.SHA256 || size != item.Size {
			return fail("DIRECTOR_BOOTSTRAP_PREPARED_POISONED")
		}
	}
	return nil
}

func loadPrepared(paths runtimePaths, candidate, bootstrapSHA string) (preparedRuntime, error) {
	var value preparedRuntime
	if err := decodeStrict(preparedPath(paths, candidate), maximumManifestBytes, &value); err != nil {
		return value, fail("DIRECTOR_BOOTSTRAP_PREPARED_MISSING")
	}
	if err := validatePrepared(value, candidate, bootstrapSHA); err != nil {
		return value, err
	}
	return value, nil
}

func canonicalJSONDigest(value any) string {
	content, _ := json.Marshal(value)
	return digestBytes(content)
}

func platformOK() bool { return runtime.GOOS == "linux" && runtime.GOARCH == "amd64" }

func sortedEnvironment(values map[string]string) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	result := make([]string, 0, len(keys))
	for _, key := range keys {
		result = append(result, key+"="+values[key])
	}
	return result
}
