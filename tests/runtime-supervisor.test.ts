// SPDX-License-Identifier: Apache-2.0

import assert from "node:assert/strict";
import { mkdirSync, mkdtempSync, readFileSync, rmSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { dirname, join } from "node:path";
import test from "node:test";
import { fileURLToPath } from "node:url";
import { createConnection, createServer } from "node:net";

import {
  ensureRuntimeSupervisor,
  RuntimeSupervisorError,
  runtimeSupervisorBinding,
  runtimeSupervisorPaths,
} from "../connector/runtime-supervisor.server.ts";
import type { ResolvedDolt } from "../connector/dolt-distribution.server.ts";
import type { ResolvedEngine } from "../connector/engine-distribution.server.ts";
import {
  runtimePlatform,
  RuntimePlatformError,
} from "../connector/runtime-platform.server.ts";

const repositoryRoot = join(dirname(fileURLToPath(import.meta.url)), "..");

function processStart(): string {
  const stat = readFileSync(`/proc/${process.pid}/stat`, "utf8");
  return stat.slice(stat.lastIndexOf(")") + 2).split(" ")[19]!;
}

function artifacts(): { engine: ResolvedEngine; dolt: ResolvedDolt } {
  return {
    engine: {
      mode: "release",
      version: "1.0.0-alpha.1",
      sourceCandidate: "1".repeat(40),
      target: "linux-amd64",
      binaryPath: "/verified/director-engine",
      noticesPath: "/verified/THIRD_PARTY_NOTICES.txt",
      binarySha256: "2".repeat(64),
      noticesSha256: "3".repeat(64),
      connectorCommit: "4".repeat(40),
      contractVersion: "director-host/v1",
      contractSha256: "5".repeat(64),
    },
    dolt: {
      version: "2.3.2",
      target: "linux-amd64",
      binaryPath: "/verified/dolt",
      binarySha256: "6".repeat(64),
      archiveSha256: "7".repeat(64),
    },
  };
}

test("a reload adopts one exact live supervisor and renews its lease", async () => {
  const root = mkdtempSync(join(tmpdir(), "director-supervisor-adopt-"));
  const environment = {
    XDG_RUNTIME_DIR: join(root, "runtime"),
    XDG_CACHE_HOME: join(root, "cache"),
    XDG_CONFIG_HOME: join(root, "config"),
    XDG_DATA_HOME: join(root, "data"),
  };
  const values = artifacts();
  const binding = runtimeSupervisorBinding(values.engine, values.dolt);
  const paths = runtimeSupervisorPaths(environment);
  for (const path of [paths.root, paths.data, paths.config]) mkdirSync(path, { recursive: true, mode: 0o700 });
  writeFileSync(paths.state, `${JSON.stringify({
    schemaVersion: 1,
    binding,
    pid: process.pid,
    processStart: processStart(),
    status: "current",
    enginePid: null,
    engineProcessStart: null,
    doltPid: null,
    doltProcessStart: null,
  })}\n`, { mode: 0o600 });
  writeFileSync(paths.token, "a".repeat(64), { mode: 0o600 });
  writeFileSync(paths.projectAdminToken, "b".repeat(64), { mode: 0o600 });
  const commands: string[] = [];
  try {
    const request = async (_socket: string, _token: string, command: "ensure" | "status" | "release") => {
      commands.push(command);
      return { state: "current" as const, binding, enginePid: 11, doltPid: 12, restartCount: 0 };
    };
    const handle = await ensureRuntimeSupervisor({
      pluginRoot: root,
      environment,
      ...values,
      hostSocket: join(paths.root, "host.sock"),
      dependencies: { request },
    });
    assert.equal(handle.binding, binding);
    assert.equal(handle.projectAdminAuthorization(), "b".repeat(64));
    assert.notEqual(handle.projectAdminAuthorization(), "a".repeat(64));
    assert.equal((await handle.status()).state, "current");
    await handle.close();
    assert.deepEqual(commands, ["ensure", "status"]);
  } finally {
    rmSync(root, { recursive: true, force: true });
  }
});

test("compatible connector update adopts one release binding while artifact drift changes it", () => {
  const first = artifacts();
  const compatibleConnector = artifacts();
  compatibleConnector.engine.connectorCommit = "9".repeat(40);
  assert.equal(
    runtimeSupervisorBinding(first.engine, first.dolt),
    runtimeSupervisorBinding(compatibleConnector.engine, compatibleConnector.dolt),
    "a connector-only update must adopt the verified release supervisor",
  );

  for (const mutate of [
    (value: ReturnType<typeof artifacts>) => { value.engine.binarySha256 = "8".repeat(64); },
    (value: ReturnType<typeof artifacts>) => { value.engine.contractSha256 = "8".repeat(64); },
    (value: ReturnType<typeof artifacts>) => { value.engine.sourceCandidate = "8".repeat(40); },
    (value: ReturnType<typeof artifacts>) => { value.engine.version = "1.0.0-alpha.2"; },
    (value: ReturnType<typeof artifacts>) => { value.dolt.binarySha256 = "8".repeat(64); },
    (value: ReturnType<typeof artifacts>) => { value.dolt.version = "2.3.3"; },
  ]) {
    const changed = artifacts();
    mutate(changed);
    assert.notEqual(runtimeSupervisorBinding(first.engine, first.dolt), runtimeSupervisorBinding(changed.engine, changed.dolt));
  }
  assert.notEqual(
    runtimeSupervisorBinding(first.engine, first.dolt),
    runtimeSupervisorBinding(first.engine, first.dolt, { dolt: 3308, engine: 7041 }),
  );
  const source = readFileSync(join(repositoryRoot, "connector", "runtime-supervisor.server.ts"), "utf8");
  assert.match(source, /existing\.binding !== binding[\s\S]*request\(paths\.socket, token, "release"\)[\s\S]*waitForProcessExit/u);
  const processSource = readFileSync(join(repositoryRoot, "connector", "runtime-supervisor-process.linux.server.mjs"), "utf8");
  assert.match(processSource, /DIRECTOR_TASKSTORE_CONFIG_OUTPUT_MISMATCH:[\s\S]*fail\("DIRECTOR_TASKSTORE_CONFIG_OUTPUT_MISMATCH"\)/u);
  assert.doesNotMatch(processSource, /fail\([^)]*stderr/u);
});

async function listenerPresent(port: number): Promise<boolean> {
  return new Promise((accept) => {
    const socket = createConnection({ host: "127.0.0.1", port });
    const finish = (value: boolean) => { socket.destroy(); accept(value); };
    socket.setTimeout(250, () => finish(false));
    socket.once("connect", () => finish(true));
    socket.once("error", () => finish(false));
  });
}

test("a foreign Director port is refused without killing its listener", async () => {
  const root = mkdtempSync(join(tmpdir(), "director-supervisor-foreign-"));
  const environment = {
    XDG_RUNTIME_DIR: join(root, "runtime"),
    XDG_CACHE_HOME: join(root, "cache"),
    XDG_CONFIG_HOME: join(root, "config"),
    XDG_DATA_HOME: join(root, "data"),
  };
  const alreadyListening = await listenerPresent(3307);
  const server = alreadyListening ? null : createServer();
  try {
    if (server) {
      await new Promise<void>((accept, reject) => {
        server.once("error", reject);
        server.listen(3307, "127.0.0.1", accept);
      });
    }
    await assert.rejects(ensureRuntimeSupervisor({
      pluginRoot: repositoryRoot,
      environment,
      ...artifacts(),
      hostSocket: join(root, "host.sock"),
    }), (error: unknown) =>
      error instanceof RuntimeSupervisorError && error.code === "DIRECTOR_RUNTIME_EXTERNAL_OWNER");
    assert.equal(await listenerPresent(3307), true);
  } finally {
    if (server) await new Promise<void>((accept) => server.close(() => accept()));
    rmSync(root, { recursive: true, force: true });
  }
});

test("installed runtime has no compiler, source, PATH-binary, or system-service fallback", () => {
  const distribution = readFileSync(join(repositoryRoot, "connector", "engine-distribution.server.ts"), "utf8");
  const selection = readFileSync(join(repositoryRoot, "connector", "engine-selection.server.ts"), "utf8");
  const supervisor = readFileSync(join(repositoryRoot, "connector", "runtime-supervisor-process.linux.server.mjs"), "utf8");
  const entry = readFileSync(join(repositoryRoot, "index.ts"), "utf8");
  const contributions = readFileSync(join(repositoryRoot, "connector", "contributions.server.ts"), "utf8");
  assert.doesNotMatch(distribution, /defaultCompile|spawnSync\(["']go["']|ENGINE_DEVELOPMENT_BUILD|developmentEnginePaths/u);
  assert.doesNotMatch(selection, /sourceRoot|GOMODCACHE|go-build-cache/u);
  assert.doesNotMatch(supervisor, /systemctl|systemd|spawnOwned\(["'](?:dolt|director-engine)["']/u);
  assert.match(supervisor, /spawnOwned\(configuration\.dolt\.binaryPath/u);
  assert.match(supervisor, /spawnOwned\(configuration\.engine\.binaryPath/u);
  assert.doesNotMatch(`${entry}\n${contributions}`, /createPaseoClient|DIRECTOR_PASEO_URL|credentialFile|loopback|bridge/u);
  assert.match(contributions, /type PaseoApi = PluginHandlerContext\["paseo"\]/u);
  assert.match(contributions, /startInstalledConnectorShell\(\{ paseo \}\)/u);
  assert.equal(runtimePlatform("linux", "x64").target, "linux-amd64");
  assert.throws(
    () => runtimePlatform("win32", "x64"),
    (error: unknown) => error instanceof RuntimePlatformError && error.code === "DIRECTOR_PLATFORM_UNSUPPORTED",
  );
});
