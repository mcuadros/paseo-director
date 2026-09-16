// SPDX-License-Identifier: Apache-2.0

package main

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (function roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return function(request)
}

func TestDirectorHostIdentityIsStableOwnerOnlyAndPoisoningFailsClosed(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "config", "director", "host-identity.json")
	first, err := loadOrCreateHostIdentity(path)
	if err != nil {
		t.Fatal(err)
	}
	second, err := loadOrCreateHostIdentity(path)
	if err != nil {
		t.Fatal(err)
	}
	if first != second || !hostIDPattern.MatchString(first.ID) || first.Label != "Director" {
		t.Fatalf("identity drift: %#v %#v", first, second)
	}
	info, err := os.Stat(path)
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("identity permissions: %v %#o", err, info.Mode().Perm())
	}
	if err := os.WriteFile(path, []byte(`{"schemaVersion":1,"id":"local-paseo","label":"Local Paseo"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := loadOrCreateHostIdentity(path); codeOf(err, "") != "DIRECTOR_HOST_IDENTITY_POISONED" {
		t.Fatalf("unexpected poison result: %v", err)
	}
}

func TestBindingIncludesChannelCandidateArtifactsIdentityAndHostSocket(t *testing.T) {
	prepared := preparedRuntime{SchemaVersion: 1, Channel: "main", SourceCandidate: strings.Repeat("1", 40), Target: target,
		Bootstrap: preparedExecutable{SHA256: strings.Repeat("2", 64)}, Engine: preparedEngine{Binary: preparedExecutable{SHA256: strings.Repeat("3", 64)}, ContractSHA256: strings.Repeat("4", 64)},
		Dolt: preparedDolt{Binary: preparedExecutable{SHA256: strings.Repeat("5", 64)}}}
	host := hostIdentity{SchemaVersion: 1, ID: "director-" + strings.Repeat("6", 32), Label: "Director"}
	installed := runtimePorts{dolt: defaultDoltPort, engine: defaultEnginePort}
	base := runtimeBinding(prepared, host, "/private/host.sock", installed)
	mutations := []string{
		runtimeBinding(func() preparedRuntime { value := prepared; value.Channel = "release"; return value }(), host, "/private/host.sock", installed),
		runtimeBinding(func() preparedRuntime {
			value := prepared
			value.SourceCandidate = strings.Repeat("7", 40)
			return value
		}(), host, "/private/host.sock", installed),
		runtimeBinding(prepared, hostIdentity{SchemaVersion: 1, ID: "director-" + strings.Repeat("8", 32), Label: "Director"}, "/private/host.sock", installed),
		runtimeBinding(prepared, host, "/private/other.sock", installed),
		runtimeBinding(prepared, host, "/private/host.sock", runtimePorts{dolt: 13307, engine: 17041, isolated: true}),
	}
	for _, mutation := range mutations {
		if mutation == base {
			t.Fatal("runtime binding failed to include exact ownership")
		}
	}
}

func TestTaskStoreIdentityAcceptsOnlyLegacyMigrationOrExactDirectorIdentity(t *testing.T) {
	path := filepath.Join(t.TempDir(), "taskstore.json")
	for _, storeID := range []string{"local-paseo", "director-" + strings.Repeat("a", 32)} {
		if err := os.WriteFile(path, []byte(`{"schemaVersion":2,"storeId":"`+storeID+`"}`), 0o600); err != nil {
			t.Fatal(err)
		}
		got, _, err := taskStoreIdentity(path)
		if err != nil || got != storeID {
			t.Fatalf("identity %q: %q %v", storeID, got, err)
		}
	}
	if err := os.WriteFile(path, []byte(`{"schemaVersion":2,"storeId":"foreign-host"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := taskStoreIdentity(path); codeOf(err, "") != "DIRECTOR_TASKSTORE_IDENTITY_INVALID" {
		t.Fatalf("foreign TaskStore identity accepted: %v", err)
	}
}

func TestRuntimeRefusesForeignPortsBeforeLaunchingChildren(t *testing.T) {
	holdDirectorRuntimePorts(t, defaultDoltPort)
	controller := runtimeController{ports: runtimePorts{dolt: defaultDoltPort, engine: defaultEnginePort}}
	if err := controller.startChildren(); codeOf(err, "") != "DIRECTOR_BOOTSTRAP_EXTERNAL_OWNER" {
		t.Fatalf("foreign port was not refused before launch: %v", err)
	}
}

// holdDirectorRuntimePorts makes the named ports look like the loopback
// listeners of a Director runtime this user owns.
func holdDirectorRuntimePorts(t *testing.T, held ...int) {
	t.Helper()
	occupied := map[int]struct{}{}
	for _, port := range held {
		occupied[port] = struct{}{}
	}
	previousInUse, previousHolder := runtimePortInUse, runtimePortHolder
	runtimePortInUse = func(port int) bool { _, ok := occupied[port]; return ok }
	runtimePortHolder = func(port int) holderProcess {
		if _, ok := occupied[port]; !ok {
			return holderProcess{}
		}
		return supervisedDirectorChild(port)
	}
	t.Cleanup(func() { runtimePortInUse, runtimePortHolder = previousInUse, previousHolder })
}

func TestReadyChildMustRemainTheExactPinnedExecutable(t *testing.T) {
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	executable, err = filepath.EvalSymlinks(executable)
	if err != nil {
		t.Fatal(err)
	}
	child := &childProcess{command: &exec.Cmd{Process: &os.Process{Pid: os.Getpid()}}}
	if err := verifyStartedChild(child, executable); err != nil {
		t.Fatalf("exact child was rejected: %v", err)
	}
	if err := verifyStartedChild(child, filepath.Join(t.TempDir(), "foreign")); codeOf(err, "") != "DIRECTOR_BOOTSTRAP_CHILD_IDENTITY" {
		t.Fatalf("foreign child was accepted: %v", err)
	}
}

func TestLegacyTaskStoreIdentityMigratesWithBackupAndNoReseed(t *testing.T) {
	root := t.TempDir()
	paths := runtimePaths{configRoot: filepath.Join(root, "config"), credentials: filepath.Join(root, "config", "credentials"),
		taskstore: filepath.Join(root, "config", "taskstore.json"), privilegeFile: filepath.Join(root, "data", "privileges.db"), dataRoot: filepath.Join(root, "data")}
	for _, path := range []string{paths.configRoot, paths.credentials, paths.dataRoot} {
		if err := os.MkdirAll(path, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	original := []byte("{\"schemaVersion\":2,\"storeId\":\"local-paseo\"}\n")
	if err := os.WriteFile(paths.taskstore, original, 0o600); err != nil {
		t.Fatal(err)
	}
	identity := hostIdentity{SchemaVersion: 1, ID: "director-" + strings.Repeat("a", 32), Label: "Director"}
	previous := executeRuntimeCommand
	provisions := 0
	ports := runtimePorts{dolt: 13307, engine: 17041, isolated: true}
	executeRuntimeCommand = func(_ string, args []string, _ string, _ time.Duration) error {
		if index := indexOf(args, "--config-output"); index >= 0 {
			provisions++
			if address := indexOf(args, "--address"); address < 0 || args[address+1] != "127.0.0.1:13307" {
				t.Fatalf("TaskStore provisioning ignored the isolated Dolt port: %q", args)
			}
			if err := os.WriteFile(args[index+1], []byte("{\"schemaVersion\":2,\"storeId\":\""+identity.ID+"\"}\n"), 0o600); err != nil {
				return err
			}
		}
		return nil
	}
	t.Cleanup(func() { executeRuntimeCommand = previous })
	prepared := preparedRuntime{Engine: preparedEngine{Binary: preparedExecutable{Path: "/private/director-engine"}}}
	if err := bootstrapTaskStore(paths, prepared, identity, ports); err != nil {
		t.Fatal(err)
	}
	storeID, _, err := taskStoreIdentity(paths.taskstore)
	if err != nil || storeID != identity.ID || provisions != 1 {
		t.Fatalf("migration result: store=%q provisions=%d err=%v", storeID, provisions, err)
	}
	backup, err := os.ReadFile(filepath.Join(paths.configRoot, "taskstore.legacy-local-paseo.json"))
	if err != nil || !bytes.Equal(backup, original) {
		t.Fatalf("legacy backup was not preserved: %v", err)
	}
	if err := bootstrapTaskStore(paths, prepared, identity, ports); err != nil || provisions != 1 {
		t.Fatalf("migration replay was not idempotent: provisions=%d err=%v", provisions, err)
	}
}

func doltArchive(t *testing.T, content []byte, entry string, kind byte) []byte {
	t.Helper()
	var output bytes.Buffer
	gzipWriter := gzip.NewWriter(&output)
	tarWriter := tar.NewWriter(gzipWriter)
	if err := tarWriter.WriteHeader(&tar.Header{Name: entry, Mode: 0o755, Size: int64(len(content)), Typeflag: kind}); err != nil {
		t.Fatal(err)
	}
	if kind == tar.TypeReg {
		if _, err := tarWriter.Write(content); err != nil {
			t.Fatal(err)
		}
	}
	if err := tarWriter.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gzipWriter.Close(); err != nil {
		t.Fatal(err)
	}
	return output.Bytes()
}

func TestDoltExtractionAcceptsOneExactExecutableAndRejectsTraversalOrLinks(t *testing.T) {
	content := []byte("dolt executable")
	got, err := extractDolt(doltArchive(t, content, "dolt-linux-amd64/bin/dolt", tar.TypeReg), int64(len(content)))
	if err != nil || !bytes.Equal(got, content) {
		t.Fatalf("extract: %v %q", err, got)
	}
	for _, fixture := range []struct {
		entry string
		kind  byte
	}{{"../bin/dolt", tar.TypeReg}, {"dolt/bin/dolt", tar.TypeSymlink}} {
		if _, err := extractDolt(doltArchive(t, content, fixture.entry, fixture.kind), int64(len(content))); codeOf(err, "") != "DIRECTOR_BOOTSTRAP_DOLT_ARCHIVE" {
			t.Fatalf("unsafe archive accepted: %q %v", fixture.entry, err)
		}
	}
}

func TestPublishedAssetDownloadIsPinnedBoundedAndGoIndependent(t *testing.T) {
	content := []byte("published Engine fixture")
	previous := httpClient
	httpClient = &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		if request.URL.Hostname() != "github.com" {
			t.Fatalf("unexpected host: %s", request.URL.Hostname())
		}
		return &http.Response{StatusCode: http.StatusOK, ContentLength: int64(len(content)), Body: io.NopCloser(bytes.NewReader(content)), Header: make(http.Header), Request: request}, nil
	})}
	t.Cleanup(func() { httpClient = previous })
	asset := releaseAsset{Name: "director-engine-linux-amd64", URL: "https://github.com/mcuadros/paseo-director/releases/download/v1.0.0/director-engine-linux-amd64", SHA256: digestBytes(content), Size: int64(len(content))}
	got, err := downloadAsset(asset, 1024)
	if err != nil || !bytes.Equal(got, content) {
		t.Fatalf("download: %v", err)
	}
	asset.SHA256 = strings.Repeat("0", 64)
	if _, err := downloadAsset(asset, 1024); codeOf(err, "") != "DIRECTOR_BOOTSTRAP_RELEASE_DIGEST" {
		t.Fatalf("digest drift accepted: %v", err)
	}
}

func TestMainEngineBuildUsesOnlyFixedGoExactPackageAndReplaysWithoutBuild(t *testing.T) {
	root := t.TempDir()
	cache := filepath.Join(root, "cache")
	home := filepath.Join(root, "home")
	for _, path := range []string{cache, home, filepath.Join(root, "generated"), filepath.Join(root, "engine", "cmd", "director-engine")} {
		if err := os.MkdirAll(path, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	candidate := strings.Repeat("a", 40)
	contractDigest := strings.Repeat("b", 64)
	if err := os.WriteFile(filepath.Join(root, "generated", "host-contract.shared.ts"), []byte(`export const HOST_CONTRACT_VERSION = "director.host/v1"; export const HOST_CONTRACT_SHA256 = "`+contractDigest+`";`), 0o600); err != nil {
		t.Fatal(err)
	}
	testGo := copyTestExecutable(t, filepath.Join(root, "toolchain", "go"))
	t.Setenv("HOME", home)
	t.Setenv("XDG_CACHE_HOME", cache)
	t.Setenv("GOMODCACHE", filepath.Join(root, "modules"))
	paths, err := runtimeBasePaths()
	if err != nil {
		t.Fatal(err)
	}
	previous := runCommand
	t.Cleanup(func() { runCommand = previous })
	builds := 0
	runCommand = func(_ context.Context, executable string, args []string, cwd string, environment map[string]string) (commandResult, error) {
		joined := strings.Join(args, "\x00")
		switch {
		case executable == "/usr/bin/git" && strings.Contains(joined, "rev-parse"):
			return commandResult{stdout: candidate}, nil
		case executable == "/usr/bin/git" && strings.Contains(joined, "status"):
			return commandResult{}, nil
		case executable == testGo && len(args) == 1 && args[0] == "version":
			assertExactToolchainProbe(t, cwd, environment, testGo)
			return commandResult{stdout: "go version " + requiredGoVersion + " linux/amd64"}, nil
		case executable == testGo && len(args) > 0 && args[0] == "env":
			assertExactToolchainProbe(t, cwd, environment, testGo)
			return commandResult{stdout: "linux\namd64\n" + requiredGoVersion}, nil
		case executable == testGo && len(args) > 0 && args[0] == "build":
			builds++
			expectedEnvironment := map[string]string{"HOME": home, "GOCACHE": filepath.Join(cache, "director", "go-build-cache", requiredGoVersion), "GOMODCACHE": filepath.Join(root, "modules"), "CGO_ENABLED": "0", "GOARCH": "amd64", "GOOS": "linux", "GOENV": "off", "GOWORK": "off", "GOPROXY": "off", "GOSUMDB": "off", "GOTOOLCHAIN": "local"}
			if cwd != filepath.Join(root, "engine") || len(args) != 9 || args[0] != "build" || args[1] != "-trimpath" || args[2] != "-buildvcs=false" || args[3] != "-mod=readonly" || args[4] != "-ldflags" || args[6] != "-o" || args[8] != "./cmd/director-engine" || !reflect.DeepEqual(environment, expectedEnvironment) {
				t.Fatalf("unsafe build argv: %q cwd=%s", args, cwd)
			}
			output := args[indexOf(args, "-o")+1]
			if err := os.WriteFile(output, []byte("exact Engine binary"), 0o500); err != nil {
				t.Fatal(err)
			}
			return commandResult{}, nil
		default:
			binary, readErr := os.ReadFile(executable)
			if readErr != nil {
				return commandResult{}, readErr
			}
			notices := []byte("Director Engine exact main source-testing build\nsource-candidate: " + candidate + "\ncontract-sha256: " + contractDigest + "\nmodule-policy: go.sum; -mod=readonly; GOPROXY=off\n")
			identity := map[string]any{"name": "director-engine", "version": mainEngineVersion, "buildMode": "main", "sourceCandidate": candidate, "target": target,
				"executableSha256": digestBytes(binary), "noticesSha256": digestBytes(notices), "contractVersion": "director.host/v1", "contractSha256": contractDigest, "productBehavior": true}
			encoded, _ := json.Marshal(identity)
			return commandResult{stdout: string(encoded)}, nil
		}
	}
	selectTestGo := func(home string) (string, error) {
		return exactGoToolchainFromCandidates(home, []string{testGo})
	}
	first, count, err := buildMainEngineWithToolchain(root, candidate, paths, selectTestGo)
	if err != nil || count != 1 || builds != 1 || first.SourceCandidate != candidate {
		t.Fatalf("first build: count=%d builds=%d err=%v", count, builds, err)
	}
	second, count, err := buildMainEngineWithToolchain(root, candidate, paths, selectTestGo)
	if err != nil || count != 0 || builds != 1 || second.Binary.SHA256 != first.Binary.SHA256 {
		t.Fatalf("replay: count=%d builds=%d err=%v", count, builds, err)
	}
	preparedFile := filepath.Join(cache, "director", "engines", "main", candidate, target, contractDigest, "prepared.json")
	if err := os.Chmod(preparedFile, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(preparedFile, []byte("{}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := buildMainEngineWithToolchain(root, candidate, paths, selectTestGo); codeOf(err, "") != "DIRECTOR_MAIN_CACHE_POISONED" {
		t.Fatalf("poisoned replay was adopted: %v", err)
	}
}

func TestMainGoToolchainWrongVersionAndCapabilityAreBounded(t *testing.T) {
	testGo := copyTestExecutable(t, filepath.Join(t.TempDir(), "toolchain", "go"))
	previous := runCommand
	t.Cleanup(func() { runCommand = previous })
	runCommand = func(_ context.Context, _ string, args []string, _ string, _ map[string]string) (commandResult, error) {
		if len(args) == 1 && args[0] == "version" {
			return commandResult{stdout: "go version go1.25.0 linux/amd64"}, nil
		}
		return commandResult{stdout: "linux\namd64\ngo1.25.0"}, nil
	}
	if _, err := exactGoToolchainFromCandidates(t.TempDir(), []string{testGo}); codeOf(err, "") != "DIRECTOR_MAIN_GO_TOOLCHAIN_VERSION" {
		t.Fatalf("wrong version diagnostic: %v", err)
	}
	runCommand = func(_ context.Context, _ string, args []string, _ string, _ map[string]string) (commandResult, error) {
		if len(args) == 1 && args[0] == "version" {
			return commandResult{stdout: "go version " + requiredGoVersion + " linux/amd64"}, nil
		}
		return commandResult{stdout: "linux\narm64\n" + requiredGoVersion}, nil
	}
	if _, err := exactGoToolchainFromCandidates(t.TempDir(), []string{testGo}); codeOf(err, "") != "DIRECTOR_MAIN_GO_TOOLCHAIN_CAPABILITY" {
		t.Fatalf("wrong capability diagnostic: %v", err)
	}
}

func TestMainGoToolchainProductionPathsAndUnsafeExecutablesFailClosed(t *testing.T) {
	if got, want := productionGoToolchainCandidates(), []string{"/usr/local/go/bin/go", "/usr/bin/go"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("production Go candidates drifted: %q", got)
	}
	root := t.TempDir()
	if _, err := exactGoToolchainFromCandidates(root, []string{filepath.Join(root, "missing-go")}); codeOf(err, "") != "DIRECTOR_MAIN_GO_TOOLCHAIN_MISSING" {
		t.Fatalf("missing toolchain diagnostic: %v", err)
	}
	testGo := copyTestExecutable(t, filepath.Join(root, "toolchain", "go"))
	if err := os.Chmod(testGo, 0o522); err != nil {
		t.Fatal(err)
	}
	if _, err := exactGoToolchainFromCandidates(root, []string{testGo}); codeOf(err, "") != "DIRECTOR_MAIN_GO_TOOLCHAIN_MISSING" {
		t.Fatalf("group-writable toolchain was admitted: %v", err)
	}
	if err := os.Chmod(testGo, 0o500); err != nil {
		t.Fatal(err)
	}
	symlink := filepath.Join(root, "toolchain", "go-link")
	if err := os.Symlink(testGo, symlink); err != nil {
		t.Fatal(err)
	}
	if _, err := exactGoToolchainFromCandidates(root, []string{symlink}); codeOf(err, "") != "DIRECTOR_MAIN_GO_TOOLCHAIN_MISSING" {
		t.Fatalf("symlinked toolchain was admitted: %v", err)
	}
	unsafeOwner := uint32(1001)
	if trustedExecutableOwner(unsafeOwner, 1002, []executableOwnership{{uid: 1002, mode: os.ModeDir | 0o755}, {uid: unsafeOwner, mode: os.ModeDir | 0o755}}) {
		t.Fatal("foreign executable owner was admitted through mismatched parent ownership")
	}
	if trustedExecutableOwner(unsafeOwner, 1002, []executableOwnership{{uid: unsafeOwner, mode: os.ModeDir | 0o775}, {uid: unsafeOwner, mode: os.ModeDir | 0o755}}) {
		t.Fatal("foreign executable owner was admitted through a writable parent")
	}
}

func copyTestExecutable(t *testing.T, destination string) string {
	t.Helper()
	source, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	content, err := os.ReadFile(source)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(destination), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(destination, content, 0o500); err != nil {
		t.Fatal(err)
	}
	if !trustedSystemExecutable(destination) {
		t.Fatal("test-owned executable did not satisfy the production trust validator")
	}
	return destination
}

func assertExactToolchainProbe(t *testing.T, cwd string, environment map[string]string, executable string) {
	t.Helper()
	wantEnvironment := map[string]string{"GOENV": "off", "GOTOOLCHAIN": "local", "GOWORK": "off"}
	if cwd != filepath.Dir(filepath.Dir(executable)) || !reflect.DeepEqual(environment, wantEnvironment) {
		t.Fatalf("unsafe toolchain probe: cwd=%s environment=%v", cwd, environment)
	}
}

func containsAll(values []string, wanted ...string) bool {
	for _, item := range wanted {
		if indexOf(values, item) < 0 {
			return false
		}
	}
	return true
}
func indexOf(values []string, wanted string) int {
	for index, value := range values {
		if value == wanted {
			return index
		}
	}
	return -1
}
