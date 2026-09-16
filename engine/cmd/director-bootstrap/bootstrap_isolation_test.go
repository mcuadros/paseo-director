// SPDX-License-Identifier: Apache-2.0

package main

import (
	"encoding/json"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

// isolatedEnvironment places a second runtime's private XDG state beside a
// default installation rooted at the same temporary home.
func isolatedEnvironment(t *testing.T) (string, map[string]string) {
	t.Helper()
	root := t.TempDir()
	home := filepath.Join(root, "home")
	if err := os.MkdirAll(home, 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", home)
	values := map[string]string{}
	for _, name := range runtimeXDGVariables {
		values[name] = filepath.Join(root, "isolated", strings.ToLower(strings.TrimPrefix(name, "XDG_")))
	}
	return home, values
}

func applyEnvironment(t *testing.T, values map[string]string) {
	t.Helper()
	for _, name := range runtimeXDGVariables {
		t.Setenv(name, values[name])
	}
}

func TestIsolationRequiresBothPortsAndRefusesAPartialDeclaration(t *testing.T) {
	installed, err := resolveRuntimePorts(func(string) string { return "" })
	if err != nil || installed.isolated || installed.dolt != defaultDoltPort || installed.engine != defaultEnginePort {
		t.Fatalf("default installation ports drifted: %#v %v", installed, err)
	}
	isolated, err := resolveRuntimePorts(environmentMap{doltPortVariable: "13307", enginePortVariable: " 17041 "}.Get)
	if err != nil || !isolated.isolated || isolated.dolt != 13307 || isolated.engine != 17041 {
		t.Fatalf("isolated ports were not admitted: %#v %v", isolated, err)
	}
	for name, declaration := range map[string]environmentMap{
		"only the Dolt port":   {doltPortVariable: "13307"},
		"only the Engine port": {enginePortVariable: "17041"},
		"one port twice":       {doltPortVariable: "13307", enginePortVariable: "13307"},
		"a privileged port":    {doltPortVariable: "1023", enginePortVariable: "17041"},
		"an out-of-range port": {doltPortVariable: "13307", enginePortVariable: "65536"},
		"a non-numeric port":   {doltPortVariable: "13307", enginePortVariable: "seventeen"},
	} {
		if _, err := resolveRuntimePorts(declaration.Get); codeOf(err, "") != "DIRECTOR_BOOTSTRAP_ISOLATION_INCOMPLETE" {
			t.Fatalf("%s was accepted as isolation: %v", name, err)
		}
	}
}

type environmentMap map[string]string

func (values environmentMap) Get(name string) string { return values[name] }

func TestIsolationPortsWithoutPrivateStateAreRefused(t *testing.T) {
	home, isolated := isolatedEnvironment(t)
	t.Setenv(doltPortVariable, "13307")
	t.Setenv(enginePortVariable, "17041")

	applyEnvironment(t, isolated)
	paths, ports, err := runtimeLayout()
	if err != nil || !ports.isolated || !strings.HasPrefix(paths.dataRoot, filepath.Join(filepath.Dir(home), "isolated")) {
		t.Fatalf("a complete isolation was refused: %#v %v", ports, err)
	}

	for _, name := range runtimeXDGVariables {
		partial := map[string]string{}
		for key, value := range isolated {
			partial[key] = value
		}
		partial[name] = ""
		applyEnvironment(t, partial)
		if _, _, err := runtimeLayout(); codeOf(err, "") != "DIRECTOR_BOOTSTRAP_ISOLATION_INCOMPLETE" {
			t.Fatalf("isolation was admitted without a private %s: %v", name, err)
		}
	}

	shared := map[string]string{
		"XDG_RUNTIME_DIR": filepath.Join(home, ".cache"),
		"XDG_CACHE_HOME":  filepath.Join(home, ".cache"),
		"XDG_CONFIG_HOME": filepath.Join(home, ".config"),
		"XDG_DATA_HOME":   filepath.Join(home, ".local", "share"),
	}
	applyEnvironment(t, shared)
	if _, _, err := runtimeLayout(); codeOf(err, "") != "DIRECTOR_BOOTSTRAP_ISOLATION_INCOMPLETE" {
		t.Fatalf("isolation ports were admitted against the default installation state: %v", err)
	}
	for _, name := range runtimeXDGVariables {
		nested := map[string]string{}
		for key, value := range isolated {
			nested[key] = value
		}
		nested[name] = filepath.Join(home, ".config", "director", "second")
		applyEnvironment(t, nested)
		if _, _, err := runtimeLayout(); codeOf(err, "") != "DIRECTOR_BOOTSTRAP_ISOLATION_INCOMPLETE" {
			t.Fatalf("isolation was admitted with %s nested in the default installation: %v", name, err)
		}
	}
}

func TestIsolatedRuntimeSharesNoStatePathWithTheDefaultInstallation(t *testing.T) {
	_, isolated := isolatedEnvironment(t)
	installed, err := defaultRuntimeBasePaths()
	if err != nil {
		t.Fatal(err)
	}
	applyEnvironment(t, isolated)
	t.Setenv(doltPortVariable, "13307")
	t.Setenv(enginePortVariable, "17041")
	second, ports, err := runtimeLayout()
	if err != nil {
		t.Fatal(err)
	}
	for _, pair := range [][2]string{
		{installed.runtimeRoot, second.runtimeRoot}, {installed.configRoot, second.configRoot},
		{installed.dataRoot, second.dataRoot}, {installed.cacheRoot, second.cacheRoot},
		{installed.controlSocket, second.controlSocket}, {installed.lock, second.lock},
		{installed.state, second.state}, {installed.taskstore, second.taskstore},
		{installed.hostIdentity, second.hostIdentity}, {installed.credentials, second.credentials},
		{installed.doltRoot, second.doltRoot}, {installed.doltDatabase, second.doltDatabase},
		{installed.doltSocket, second.doltSocket}, {installed.privilegeFile, second.privilegeFile},
		{installed.workRoot, second.workRoot},
	} {
		if pair[0] == pair[1] || nestedPath(pair[1], pair[0]) || nestedPath(pair[0], pair[1]) {
			t.Fatalf("the isolated runtime can reach default installation state: %q %q", pair[0], pair[1])
		}
	}
	prepared := preparedRuntime{SchemaVersion: 1, Channel: "main", SourceCandidate: strings.Repeat("1", 40), Target: target}
	identity := hostIdentity{SchemaVersion: 1, ID: "director-" + strings.Repeat("6", 32), Label: "Director"}
	installedPorts := runtimePorts{dolt: defaultDoltPort, engine: defaultEnginePort}
	if runtimeBinding(prepared, identity, "/private/host.sock", ports) == runtimeBinding(prepared, identity, "/private/host.sock", installedPorts) {
		t.Fatal("the isolated runtime adopted the default installation binding")
	}
}

// supervisedDirectorChild is the exact holder shape this bootstrap produces
// for the child that serves a given port.
func supervisedDirectorChild(port int) holderProcess {
	if port == defaultEnginePort || port == 17041 {
		return holderProcess{pid: 4101,
			executable: "/private/cache/director/engines/main/" + strings.Repeat("a", 40) + "/linux-amd64/director-engine",
			arguments: []string{"/private/cache/director/engines/main/director-engine", "serve-board",
				"--listen=127.0.0.1:" + strconv.Itoa(port), "--taskstore-config=/private/config/taskstore.json",
				"--host-id=director-" + strings.Repeat("b", 32), "--host-label=Director",
				"--host-socket=/private/runtime/host.sock", "--runtime-root=/private/runtime/work",
				"--project-admin-token-file=/private/config/project-admin.token"}}
	}
	return holderProcess{pid: 4102,
		executable: "/private/cache/director/engines/dolt/2.3.2/linux-amd64/dolt",
		arguments: []string{"/private/cache/director/engines/dolt/2.3.2/linux-amd64/dolt", "sql-server",
			"--host=127.0.0.1", "--port=" + strconv.Itoa(port), "--data-dir=/private/data/taskstore/dolt",
			"--doltcfg-dir=/private/data/taskstore/doltcfg",
			"--socket=/private/runtime/director/supervisor/dolt/mysql.sock", "--loglevel=warning"}}
}

func TestOccupiedPortDiagnosticsDistinguishDirectorFromForeignHolders(t *testing.T) {
	for _, port := range []int{defaultDoltPort, defaultEnginePort} {
		foreignEngineArgv := supervisedDirectorChild(defaultEnginePort).arguments
		for _, expectation := range []struct {
			name   string
			holder string
			pid    int
			result holderProcess
			code   string
		}{
			{"a supervised Director child on this port", "director", supervisedDirectorChild(port).pid, supervisedDirectorChild(port), "DIRECTOR_BOOTSTRAP_EXTERNAL_OWNER"},
			{"an unrelated MySQL", "foreign", 4103,
				holderProcess{pid: 4103, executable: "/usr/sbin/mysqld", arguments: []string{"/usr/sbin/mysqld", "--port=3307"}}, "DIRECTOR_BOOTSTRAP_PORT_OCCUPIED"},
			{"a hand-run Dolt server", "foreign", 4104,
				holderProcess{pid: 4104, executable: "/usr/bin/dolt", arguments: []string{"dolt", "sql-server", "--host=127.0.0.1", "--port=" + strconv.Itoa(port)}}, "DIRECTOR_BOOTSTRAP_PORT_OCCUPIED"},
			{"a Director-named executable under an unrelated director/engines path", "foreign", 4105,
				holderProcess{pid: 4105, executable: "/srv/backups/director/engines/scratch/dolt", arguments: []string{"dolt", "sql-server", "--host=127.0.0.1", "--port=" + strconv.Itoa(port)}}, "DIRECTOR_BOOTSTRAP_PORT_OCCUPIED"},
			{"a supervised Director child serving a different port", "foreign", 4106,
				holderProcess{pid: 4106, executable: supervisedDirectorChild(defaultEnginePort).executable, arguments: foreignEngineArgv}, "DIRECTOR_BOOTSTRAP_PORT_OCCUPIED"},
			{"a process whose executable cannot be read", "unproved", 4107,
				holderProcess{pid: 4107, executable: "", arguments: nil}, "DIRECTOR_BOOTSTRAP_PORT_OCCUPIED"},
			{"a process another user owns", "unproved", 0, holderProcess{}, "DIRECTOR_BOOTSTRAP_PORT_OCCUPIED"},
		} {
			if port == defaultEnginePort && expectation.name == "a supervised Director child serving a different port" {
				continue
			}
			previousInUse, previousHolder := runtimePortInUse, runtimePortHolder
			runtimePortInUse = func(candidate int) bool { return candidate == port }
			runtimePortHolder = func(int) holderProcess { return expectation.result }
			err := refuseOccupiedPort(port)
			runtimePortInUse, runtimePortHolder = previousInUse, previousHolder
			if codeOf(err, "") != expectation.code {
				t.Fatalf("port %d held by %s: %v", port, expectation.name, err)
			}
			occupied, holder, pid := occupiedPortOf(err)
			if occupied != port || holder != expectation.holder || pid != expectation.pid {
				t.Fatalf("port %d held by %s detail: %d %q %d", port, expectation.name, occupied, holder, pid)
			}
			message := occupiedPortMessage(occupied, holder, pid)
			if !strings.Contains(message, "127.0.0.1:"+strconv.Itoa(port)) {
				t.Fatalf("the diagnostic did not name the port: %q", message)
			}
			if expectation.holder != "director" && strings.Contains(message, "another Director runtime (") {
				t.Fatalf("%s was reported as another Director: %q", expectation.name, message)
			}
			if expectation.holder == "foreign" && !strings.Contains(message, "is not a Director runtime") {
				t.Fatalf("%s was not reported as a non-Director holder: %q", expectation.name, message)
			}
			if expectation.holder == "unproved" && !strings.Contains(message, "no Director runtime was proved to own it") {
				t.Fatalf("%s was not reported as unproved: %q", expectation.name, message)
			}
			if expectation.holder == "unproved" && strings.Contains(message, "is not a Director runtime") {
				t.Fatalf("%s asserted a negative it cannot prove: %q", expectation.name, message)
			}
		}
	}
	previousInUse, previousHolder := runtimePortInUse, runtimePortHolder
	runtimePortInUse = func(int) bool { return false }
	runtimePortHolder = func(int) holderProcess { t.Fatal("a free port was resolved to a holder"); return holderProcess{} }
	t.Cleanup(func() { runtimePortInUse, runtimePortHolder = previousInUse, previousHolder })
	if err := refuseOccupiedPort(defaultDoltPort); err != nil {
		t.Fatalf("a free port was refused: %v", err)
	}
}

func TestOnlyListenersThatAnswerLoopbackAreResolvedAsHolders(t *testing.T) {
	for _, address := range []string{
		"0100007F", "00000000",
		"00000000000000000000000000000000", "00000000000000000000000001000000",
		"0000000000000000FFFF00000100007F",
	} {
		if !answersLoopback(address) {
			t.Fatalf("a listener answering 127.0.0.1 was skipped: %q", address)
		}
	}
	for _, address := range []string{
		"0101A8C0", "0200007F", "0000000000000000000000000100A8C0", "", "zz",
	} {
		if answersLoopback(address) {
			t.Fatalf("a listener on another interface was resolved as the holder: %q", address)
		}
	}
}

func TestPortHolderResolvesTheRealListeningProcessAndClassifiesIt(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	port := listener.Addr().(*net.TCPAddr).Port
	holder := portHolder(port)
	if holder.pid != os.Getpid() || holder.executable == "" || len(holder.arguments) == 0 {
		t.Fatalf("the real listening process was not resolved: %#v", holder)
	}
	if directorRuntimeChild(port, holder) {
		t.Fatalf("this test process was classified as a supervised Director child: %#v", holder)
	}
	if err := refuseOccupiedPort(port); codeOf(err, "") != "DIRECTOR_BOOTSTRAP_PORT_OCCUPIED" {
		t.Fatalf("a real foreign listener was not reported as an occupied port: %v", err)
	}
	if free := portHolder(port + 1); free.pid != 0 {
		t.Log("an adjacent port is also bound on this host; holder resolution remains port-exact")
	}
}

func TestIsolatedRuntimeStartsWhileTheDefaultInstallationHoldsItsPorts(t *testing.T) {
	holdDirectorRuntimePorts(t, defaultDoltPort, defaultEnginePort)
	previous := executeRuntimeCommand
	executeRuntimeCommand = func(string, []string, string, time.Duration) error { return nil }
	t.Cleanup(func() { executeRuntimeCommand = previous })

	root := t.TempDir()
	controller := runtimeController{
		ports: runtimePorts{dolt: 13307, engine: 17041, isolated: true},
		paths: runtimePaths{
			dataRoot: filepath.Join(root, "data"), doltRoot: filepath.Join(root, "data", "dolt"),
			doltDatabase: filepath.Join(root, "data", "dolt", "director"), doltConfig: filepath.Join(root, "data", "doltcfg"),
			credentials: filepath.Join(root, "config", "credentials"), doltSocket: filepath.Join(root, "runtime", "dolt", "mysql.sock"),
			workRoot: filepath.Join(root, "runtime", "work"),
		},
	}
	// The isolated runtime must pass the occupied-listener refusal and reach
	// child launch; it stops there only because this controller pins no
	// prepared executables.
	if err := controller.startChildren(); codeOf(err, "") != "DIRECTOR_BOOTSTRAP_CHILD_START" {
		t.Fatalf("the isolated runtime did not start beside the default installation: %v", err)
	}
	contending := runtimeController{ports: runtimePorts{dolt: defaultDoltPort, engine: 17041, isolated: true}}
	if err := contending.startChildren(); codeOf(err, "") != "DIRECTOR_BOOTSTRAP_EXTERNAL_OWNER" {
		t.Fatalf("genuine contention for the default Dolt port was admitted: %v", err)
	}
	contending = runtimeController{ports: runtimePorts{dolt: 13307, engine: defaultEnginePort, isolated: true}}
	if err := contending.startChildren(); codeOf(err, "") != "DIRECTOR_BOOTSTRAP_EXTERNAL_OWNER" {
		t.Fatalf("genuine contention for the default Engine port was admitted: %v", err)
	}
}

func TestIsolatedEnsureAdoptsOnlyItsOwnBindingAndNeverReleasesAnother(t *testing.T) {
	owned, requested := strings.Repeat("a", 64), strings.Repeat("b", 64)
	if adopt, err := adoptLiveOwner(true, owned, owned); err != nil || !adopt {
		t.Fatalf("an isolated runtime refused its own live binding: %v %v", adopt, err)
	}
	if adopt, err := adoptLiveOwner(false, owned, owned); err != nil || !adopt {
		t.Fatalf("the default installation refused its own live binding: %v %v", adopt, err)
	}
	if adopt, err := adoptLiveOwner(false, owned, requested); err != nil || adopt {
		t.Fatalf("the default installation lost its controlled handoff: %v %v", adopt, err)
	}
	if adopt, err := adoptLiveOwner(true, owned, requested); adopt || codeOf(err, "") != "DIRECTOR_BOOTSTRAP_ISOLATION_OCCUPIED" {
		t.Fatalf("an isolation option released a live runtime it found: %v %v", adopt, err)
	}
}

func TestIsolatedRuntimeRefusesADataRootHoldingAnotherInstanceTaskStore(t *testing.T) {
	identity := hostIdentity{SchemaVersion: 1, ID: "director-" + strings.Repeat("c", 32), Label: "Director"}
	newPaths := func(t *testing.T) runtimePaths {
		t.Helper()
		root := t.TempDir()
		paths := runtimePaths{
			configRoot:   filepath.Join(root, "config"),
			taskstore:    filepath.Join(root, "config", "taskstore.json"),
			dataRoot:     filepath.Join(root, "data"),
			doltDatabase: filepath.Join(root, "data", "taskstore", "dolt", "director"),
		}
		if err := os.MkdirAll(paths.configRoot, 0o700); err != nil {
			t.Fatal(err)
		}
		return paths
	}
	seedTaskStore := func(t *testing.T, paths runtimePaths) {
		t.Helper()
		if err := os.MkdirAll(filepath.Join(paths.doltDatabase, ".dolt"), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	writeConfig := func(t *testing.T, paths runtimePaths, storeID string) {
		t.Helper()
		if err := os.WriteFile(paths.taskstore, []byte(`{"schemaVersion":2,"storeId":"`+storeID+`"}`+"\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	empty := newPaths(t)
	if err := admitIsolatedTaskStore(empty, identity); err != nil {
		t.Fatalf("a private empty data root was refused: %v", err)
	}

	own := newPaths(t)
	seedTaskStore(t, own)
	writeConfig(t, own, identity.ID)
	if err := admitIsolatedTaskStore(own, identity); err != nil {
		t.Fatalf("this runtime's own TaskStore was refused on restart: %v", err)
	}

	legacy := newPaths(t)
	seedTaskStore(t, legacy)
	writeConfig(t, legacy, "local-paseo")
	if err := admitIsolatedTaskStore(legacy, identity); err != nil {
		t.Fatalf("the legacy identity migration path was refused: %v", err)
	}

	// The live instance's XDG_DATA_HOME with this runtime's own fresh
	// configuration: the path baseline cannot see it, so this is the refusal
	// that stops a second Dolt over another instance's TaskStore.
	borrowed := newPaths(t)
	seedTaskStore(t, borrowed)
	if err := admitIsolatedTaskStore(borrowed, identity); codeOf(err, "") != "DIRECTOR_BOOTSTRAP_ISOLATION_FOREIGN_TASKSTORE" {
		t.Fatalf("another instance's TaskStore was admitted into an isolated runtime: %v", err)
	}

	foreign := newPaths(t)
	seedTaskStore(t, foreign)
	writeConfig(t, foreign, "director-"+strings.Repeat("d", 32))
	if err := admitIsolatedTaskStore(foreign, identity); codeOf(err, "") != "DIRECTOR_BOOTSTRAP_ISOLATION_FOREIGN_TASKSTORE" {
		t.Fatalf("a TaskStore owned by another identity was admitted: %v", err)
	}
}

func TestIsolatedRuntimeReportsTheEngineAddressItServes(t *testing.T) {
	for _, ports := range []runtimePorts{
		{dolt: defaultDoltPort, engine: defaultEnginePort},
		{dolt: 13307, engine: 17041, isolated: true},
	} {
		address := engineAddress(ports)
		if address != "127.0.0.1:"+strconv.Itoa(ports.engine) {
			t.Fatalf("the reported Engine address is not the served one: %q", address)
		}
		if !validEngineAddress(address) {
			t.Fatalf("the reported Engine address is not a loopback address the connector accepts: %q", address)
		}
	}
	controller := runtimeController{ports: runtimePorts{dolt: 13307, engine: 17041, isolated: true},
		identity: hostIdentity{SchemaVersion: 1, ID: "director-" + strings.Repeat("e", 32), Label: "Director"},
		binding:  strings.Repeat("f", 64), projectAdminToken: strings.Repeat("0", 64), state: "current"}
	if got := controller.snapshot().EngineAddress; got != "127.0.0.1:17041" {
		t.Fatalf("the status document did not report the isolated Engine address: %q", got)
	}
	for _, invalid := range []string{"", "127.0.0.1:0", "0.0.0.0:17041", "127.0.0.1", "localhost:17041", "127.0.0.1:70410"} {
		if validEngineAddress(invalid) {
			t.Fatalf("a non-loopback or malformed Engine address was accepted: %q", invalid)
		}
	}
}

func TestRuntimeFailureDocumentCarriesTheClosedOccupiedListenerDetail(t *testing.T) {
	content, err := json.Marshal(runtimeFailure{SchemaVersion: 1, Code: "DIRECTOR_BOOTSTRAP_PORT_OCCUPIED", Port: defaultDoltPort, Holder: "foreign", PID: 4103})
	if err != nil {
		t.Fatal(err)
	}
	var restored runtimeFailure
	if err := json.Unmarshal(content, &restored); err != nil || restored != (runtimeFailure{SchemaVersion: 1, Code: "DIRECTOR_BOOTSTRAP_PORT_OCCUPIED", Port: defaultDoltPort, Holder: "foreign", PID: 4103}) {
		t.Fatalf("the occupied-listener detail did not survive the failure document: %v %#v", err, restored)
	}
	plain, err := json.Marshal(runtimeFailure{SchemaVersion: 1, Code: "DIRECTOR_BOOTSTRAP_START_TIMEOUT"})
	if err != nil || strings.Contains(string(plain), "port") || strings.Contains(string(plain), "holder") {
		t.Fatalf("an unrelated failure gained occupied-listener fields: %s %v", plain, err)
	}
}
