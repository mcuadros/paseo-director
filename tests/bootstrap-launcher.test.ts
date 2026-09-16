// SPDX-License-Identifier: Apache-2.0

import assert from "node:assert/strict";
import { createHash } from "node:crypto";
import { chmodSync, mkdirSync, mkdtempSync, readFileSync, rmSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { join, resolve } from "node:path";
import test from "node:test";

import {
  BootstrapLauncherError,
  ensureBootstrapRuntime,
  occupiedListenerDiagnosis,
} from "../connector/bootstrap-launcher.server.ts";
import { BootstrapSelectionError, selectInstalledBootstrap } from "../connector/bootstrap-selection.server.ts";
import { directorEngineURL } from "../connector/runtime-configuration.server.mjs";
import { startInstalledConnectorShell } from "../connector/paseo.server.ts";

function fixture() {
  const root = mkdtempSync(join(tmpdir(), "director-bootstrap-launcher-"));
  chmodSync(root, 0o700);
  const path = join(root, "director-bootstrap");
  const bytes = Buffer.from("exact private Go bootstrap fixture\n");
  writeFileSync(path, bytes, { mode: 0o500 });
  const sha256 = createHash("sha256").update(bytes).digest("hex");
  const installation = {
    schemaVersion: 3, state: "prepared", connectorCommit: "1".repeat(40), channel: "main",
    bootstrap: { schemaVersion: 1, target: "linux-amd64", path, sha256, size: bytes.byteLength },
  } as const;
  return { root, installation, selection: selectInstalledBootstrap(installation) };
}

function output(engineAddress = "127.0.0.1:7041") {
  return JSON.stringify({
    schemaVersion: 1, code: "DIRECTOR_BOOTSTRAP_RUNTIME_STATUS", state: "current", binding: "2".repeat(64), engineAddress,
    host: { schemaVersion: 1, id: `director-${"3".repeat(32)}`, label: "Director" },
    engine: { mode: "main", version: "0.0.0-main", sourceCandidate: "1".repeat(40), target: "linux-amd64",
      binary: { path: "/private/director-engine", sha256: "4".repeat(64), size: 42 },
      notices: { path: "/private/MAIN_BUILD_NOTICES.txt", sha256: "5".repeat(64), size: 43 },
      contractVersion: "director.host/v1", contractSha256: "6".repeat(64) },
    dolt: { version: "2.3.2", target: "linux-amd64", binary: { path: "/private/dolt", sha256: "7".repeat(64), size: 44 }, archiveSha256: "8".repeat(64) },
    enginePid: 101, doltPid: 102, restartCount: 0, projectAdminAuthorization: "9".repeat(64),
  });
}

test("minimal JavaScript launcher executes only the pinned Go bootstrap control command", async () => {
  const value = fixture();
  const calls: Array<{ path: string; args: readonly string[]; env: NodeJS.ProcessEnv }> = [];
  try {
    const handle = await ensureBootstrapRuntime({
      selection: value.selection,
      environment: { HOME: value.root, XDG_RUNTIME_DIR: join(value.root, "runtime"), PATH: "/poison" },
      hostSocket: join(value.root, "host.sock"),
      engineAddress: "127.0.0.1:7041",
      dependencies: { async invoke(path, args, options) { calls.push({ path, args, env: options.env }); return output(); } },
    });
    assert.equal(handle.host.id, `director-${"3".repeat(32)}`);
    assert.equal(handle.engine.sourceCandidate, "1".repeat(40));
    assert.equal(handle.dolt.version, "2.3.2");
    assert.equal(handle.projectAdminAuthorization(), "9".repeat(64));
    assert.equal((await handle.status()).enginePid, 101);
    assert.equal(calls.length, 2);
    for (const call of calls) {
      assert.equal(call.path, value.selection.bootstrap.path);
      assert.deepEqual(call.args, ["ensure", "--candidate", "1".repeat(40), "--bootstrap-sha256", value.selection.bootstrap.sha256, "--host-socket", join(value.root, "host.sock")]);
      assert.equal(call.env.PATH, undefined);
    }
    await handle.close();
  } finally { rmSync(value.root, { recursive: true, force: true }); }
});

test("the closed bootstrap environment forwards an isolated runtime declaration and nothing else", async () => {
  const value = fixture();
  const calls: NodeJS.ProcessEnv[] = [];
  try {
    const handle = await ensureBootstrapRuntime({
      selection: value.selection,
      environment: {
        HOME: value.root, XDG_RUNTIME_DIR: join(value.root, "runtime"), XDG_CACHE_HOME: join(value.root, "cache"),
        XDG_CONFIG_HOME: join(value.root, "config"), XDG_DATA_HOME: join(value.root, "data"),
        DIRECTOR_RUNTIME_DOLT_PORT: "13307", DIRECTOR_RUNTIME_ENGINE_PORT: "17041",
        PATH: "/poison", DIRECTOR_RUNTIME_UNKNOWN: "poison",
      },
      hostSocket: join(value.root, "host.sock"),
      engineAddress: "127.0.0.1:17041",
      dependencies: { async invoke(_path, _args, options) { calls.push(options.env); return output("127.0.0.1:17041"); } },
    });
    assert.equal(calls.length, 1);
    assert.deepEqual(Object.keys(calls[0]).sort(), [
      "DIRECTOR_RUNTIME_DOLT_PORT", "DIRECTOR_RUNTIME_ENGINE_PORT", "HOME",
      "XDG_CACHE_HOME", "XDG_CONFIG_HOME", "XDG_DATA_HOME", "XDG_RUNTIME_DIR",
    ]);
    assert.equal(calls[0].DIRECTOR_RUNTIME_DOLT_PORT, "13307");
    assert.equal(calls[0].DIRECTOR_RUNTIME_ENGINE_PORT, "17041");
    await handle.close();
  } finally { rmSync(value.root, { recursive: true, force: true }); }
});

test("a supervisor serving another Engine address is refused instead of adopted", async () => {
  const value = fixture();
  try {
    await assert.rejects(
      ensureBootstrapRuntime({
        selection: value.selection,
        environment: { HOME: value.root, DIRECTOR_RUNTIME_DOLT_PORT: "13307", DIRECTOR_RUNTIME_ENGINE_PORT: "17041" },
        hostSocket: join(value.root, "host.sock"),
        engineAddress: "127.0.0.1:17041",
        // The running default installation's supervisor, not this one.
        dependencies: { async invoke() { return output("127.0.0.1:7041"); } },
      }),
      (error: unknown) => error instanceof BootstrapLauncherError &&
        error.code === "DIRECTOR_BOOTSTRAP_ENGINE_ADDRESS_MISMATCH",
    );
    await assert.rejects(
      ensureBootstrapRuntime({
        selection: value.selection,
        environment: { HOME: value.root },
        hostSocket: join(value.root, "host.sock"),
        engineAddress: "0.0.0.0:7041",
        dependencies: { async invoke() { return output(); } },
      }),
      (error: unknown) => error instanceof BootstrapLauncherError &&
        error.code === "DIRECTOR_BOOTSTRAP_ENGINE_ADDRESS_MISMATCH",
    );
    for (const address of ["", "127.0.0.1:0", "localhost:7041", "127.0.0.1"]) {
      await assert.rejects(
        ensureBootstrapRuntime({
          selection: value.selection,
          environment: { HOME: value.root },
          hostSocket: join(value.root, "host.sock"),
          engineAddress: "127.0.0.1:7041",
          dependencies: { async invoke() { return output(address); } },
        }),
        (error: unknown) => error instanceof BootstrapLauncherError &&
          error.code === "DIRECTOR_BOOTSTRAP_CONTROL_INVALID",
      );
    }
  } finally { rmSync(value.root, { recursive: true, force: true }); }
});

test("the occupied-listener refusal reaches the connector as closed port and holder facts", () => {
  const stderr = [
    "DIRECTOR_BOOTSTRAP_PORT_OCCUPIED",
    JSON.stringify({ code: "DIRECTOR_BOOTSTRAP_PORT_OCCUPIED", port: 13309, holder: "foreign", pid: 1970137 }),
    "127.0.0.1:13309 is occupied by pid 1970137, which is not a Director runtime.",
  ].join("\n");
  assert.deepEqual(occupiedListenerDiagnosis(stderr), { port: 13309, holder: "foreign" });
  assert.deepEqual(
    occupiedListenerDiagnosis(`DIRECTOR_BOOTSTRAP_EXTERNAL_OWNER\n${JSON.stringify({ port: 3307, holder: "director" })}`),
    { port: 3307, holder: "director" },
  );
  for (const rejected of [
    "DIRECTOR_BOOTSTRAP_START_TIMEOUT",
    JSON.stringify({ port: 0, holder: "foreign" }),
    JSON.stringify({ port: 70_000, holder: "foreign" }),
    JSON.stringify({ port: 3307, holder: "mystery" }),
    "{not json}",
  ]) assert.equal(occupiedListenerDiagnosis(rejected), null);
});

test("missing, mutable, or digest-poisoned bootstrap pins fail before execution", () => {
  const value = fixture();
  try {
    chmodSync(value.selection.bootstrap.path, 0o600);
    writeFileSync(value.selection.bootstrap.path, "poisoned\n", { mode: 0o500 });
    chmodSync(value.selection.bootstrap.path, 0o500);
    assert.throws(() => selectInstalledBootstrap({ schemaVersion: 3, state: "prepared", connectorCommit: "1".repeat(40), channel: "main", bootstrap: value.selection.bootstrap }),
      (error: unknown) => error instanceof BootstrapSelectionError && error.code === "DIRECTOR_BOOTSTRAP_DIGEST");
  } finally { rmSync(value.root, { recursive: true, force: true }); }
});

test("an isolated declaration places the connector Engine endpoint on the declared port", () => {
  const isolated = directorEngineURL({
    DIRECTOR_RUNTIME_DOLT_PORT: "13307",
    DIRECTOR_RUNTIME_ENGINE_PORT: "17041",
  } as NodeJS.ProcessEnv);
  assert.deepEqual(isolated, { url: "http://127.0.0.1:17041", address: "127.0.0.1:17041", isolated: true });
  const installed = directorEngineURL({} as NodeJS.ProcessEnv);
  assert.deepEqual(installed, { url: "http://127.0.0.1:7041", address: "127.0.0.1:7041", isolated: false });
  // An incomplete declaration must not silently fall back to the default
  // port, which is where a running instance's Engine answers.
  for (const partial of [
    { DIRECTOR_RUNTIME_ENGINE_PORT: "17041" },
    { DIRECTOR_RUNTIME_DOLT_PORT: "13307" },
    { DIRECTOR_RUNTIME_DOLT_PORT: "13307", DIRECTOR_RUNTIME_ENGINE_PORT: "13307" },
    { DIRECTOR_RUNTIME_DOLT_PORT: "13307", DIRECTOR_RUNTIME_ENGINE_PORT: "1023" },
    { DIRECTOR_RUNTIME_DOLT_PORT: "13307", DIRECTOR_RUNTIME_ENGINE_PORT: "65536" },
    { DIRECTOR_RUNTIME_DOLT_PORT: "13307", DIRECTOR_RUNTIME_ENGINE_PORT: "seventeen" },
  ]) {
    assert.throws(
      () => directorEngineURL(partial as NodeJS.ProcessEnv),
      (error: unknown) => Reflect.get(error as object, "code") === "DIRECTOR_BOOTSTRAP_ISOLATION_INCOMPLETE",
      `an incomplete declaration resolved to an Engine endpoint: ${JSON.stringify(partial)}`,
    );
  }
});

test("the connector reads a declaration exactly as the runtime does", () => {
  const byteOrderMark = "\u{FEFF}";
  // Admitted on both sides.
  for (const [dolt, engine] of [["13307", "17041"], [" 13307 ", "\t17041\n"]]) {
    assert.deepEqual(
      directorEngineURL({ DIRECTOR_RUNTIME_DOLT_PORT: dolt, DIRECTOR_RUNTIME_ENGINE_PORT: engine } as NodeJS.ProcessEnv),
      { url: "http://127.0.0.1:17041", address: "127.0.0.1:17041", isolated: true },
    );
  }
  // Absent on both sides, covered here at the resolver only. An absent
  // declaration resolves to the fixed default pair by design, so the
  // end-to-end form of this row dials whatever holds 3307 and 7041, which on a
  // host already running Director is the running instance. That row runs in
  // tools/packaging/release-runtime-lifecycle.mjs, where nothing holds them.
  assert.deepEqual(directorEngineURL({} as NodeJS.ProcessEnv),
    { url: "http://127.0.0.1:7041", address: "127.0.0.1:7041", isolated: false });
  // Refused on both sides. A byte-order mark must not read as absent here and
  // invalid there: that would bind this surface to the default port while the
  // runtime refused to start, which is the divergence this pair must not have.
  for (const [dolt, engine] of [
    [byteOrderMark, byteOrderMark],
    [`${byteOrderMark}13307`, `${byteOrderMark}17041`],
    ["+13307", "+17041"],
    ["013307", "017041"],
    ["13307", ""],
    ["13307", "13307"],
  ]) {
    assert.throws(
      () => directorEngineURL({ DIRECTOR_RUNTIME_DOLT_PORT: dolt, DIRECTOR_RUNTIME_ENGINE_PORT: engine } as NodeJS.ProcessEnv),
      (error: unknown) => Reflect.get(error as object, "code") === "DIRECTOR_BOOTSTRAP_ISOLATION_INCOMPLETE",
      `${JSON.stringify([dolt, engine])} did not refuse in the connector`,
    );
  }
});

test("the installed shell hands the bootstrap the same Engine address it binds", async () => {
  const value = fixture();
  try {
    for (const [environment, expected] of [
      [{ HOME: value.root }, "127.0.0.1:7041"],
      [{
        HOME: value.root,
        XDG_RUNTIME_DIR: join(value.root, "runtime"), XDG_CACHE_HOME: join(value.root, "cache"),
        XDG_CONFIG_HOME: join(value.root, "config"), XDG_DATA_HOME: join(value.root, "data"),
        DIRECTOR_RUNTIME_DOLT_PORT: "13307", DIRECTOR_RUNTIME_ENGINE_PORT: "17041",
      }, "127.0.0.1:17041"],
    ] as const) {
      let requested = "";
      const shell = startInstalledConnectorShell({
        paseo: {} as never,
        environment: environment as NodeJS.ProcessEnv,
        installation: value.installation,
        reportActivation: false,
        dependencies: {
          hostCompatibility: () => ({
            paseoVersion: "0.7.2", nodeVersion: process.versions.node,
            platform: "linux", architecture: "x64", target: "linux-amd64",
          }),
          async ensureBootstrapRuntime(options: { readonly engineAddress: string }) {
            requested = options.engineAddress;
            throw new Error("DIRECTOR_BOOTSTRAP_TEST_STOP");
          },
        } as never,
      });
      await shell.close().catch(() => undefined);
      assert.equal(requested, expected, `the bootstrap was asked for ${requested}, not the declared ${expected}`);
      assert.equal(directorEngineURL(environment as NodeJS.ProcessEnv).address, expected);
    }
  } finally { rmSync(value.root, { recursive: true, force: true }); }
});

test("every installed Director endpoint is built from the resolved Engine placement", () => {
  const shell = readFileSync(resolve(import.meta.dirname, "..", "connector", "paseo.server.ts"), "utf8");
  const installed = shell.slice(shell.indexOf("export function startInstalledConnectorShell"));
  assert.match(installed, /const engineEndpoint = directorEngineURL\(environment\);/u);
  assert.match(installed, /url: engineEndpoint\.url,/u);
  // Board, planning, the connector's own engine URL and the terminal callback
  // must all read the resolved placement; a literal default here is the defect
  // that put an isolated surface on a running instance's Engine.
  assert.equal(installed.match(/configuration\.engine\.url/gu)?.length, 4);
  assert.doesNotMatch(installed, /DEFAULT_ENGINE_URL/u);
  assert.match(installed, /engineAddress: engineEndpoint\.address,/u);
});

test("TypeScript contains no Engine/Dolt distribution, child supervision, or recovery implementation", () => {
  const root = resolve(import.meta.dirname, "..");
  const launcher = readFileSync(join(root, "connector", "bootstrap-launcher.server.ts"), "utf8");
  const installer = readFileSync(join(root, "tools", "packaging", "verify-install.mjs"), "utf8");
  const bootstrapBuild = ["bootstrap-build.mjs", "bootstrap-build-core.mjs"]
    .map((file) => readFileSync(join(root, "tools", "packaging", file), "utf8"))
    .join("\n");
  assert.doesNotMatch(launcher, /\b(?:fetch|createServer|createConnection|fork|spawn)\b|sql-server|serve-board|bootstrap-taskstore|SIGKILL|SIGTERM/u);
  assert.doesNotMatch(installer, /director-engine|dolt-linux|sql-server|serve-board|bootstrap-taskstore/u);
  assert.doesNotMatch(bootstrapBuild, /\.\/cmd\/director-engine|sql-server|serve-board|bootstrap-taskstore/u);
  assert.match(bootstrapBuild, /\.\/cmd\/director-bootstrap/u);
});
