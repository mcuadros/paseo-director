// SPDX-License-Identifier: Apache-2.0

import assert from "node:assert/strict";
import { mkdirSync, mkdtempSync, rmSync } from "node:fs";
import { createRequire } from "node:module";
import { tmpdir } from "node:os";
import { dirname, join, resolve } from "node:path";
import test from "node:test";
import { fileURLToPath } from "node:url";

import { build, version as esbuildVersion } from "esbuild";

const repositoryRoot = resolve(dirname(fileURLToPath(import.meta.url)), "..");

test("the real connector loads from Paseo 0.7.2's CommonJS backend shape", async () => {
  assert.equal(esbuildVersion, "0.25.12");
  const root = mkdtempSync(join(tmpdir(), "director-paseo-commonjs-"));
  const configBase = join(root, "config");
  const cacheBase = join(root, "cache");
  const runtimeBase = join(root, "runtime");
  const checkout = join(root, "checkout");
  for (const path of [configBase, cacheBase, runtimeBase, checkout]) {
    mkdirSync(path, { recursive: true, mode: 0o700 });
  }

  try {
    const result = await build({
      stdin: {
        contents: "export { startInstalledConnectorShell } from './connector/paseo.server.ts';",
        loader: "ts",
        resolveDir: repositoryRoot,
        sourcefile: join(repositoryRoot, "index.server-regression.ts"),
      },
      bundle: true,
      format: "cjs",
      platform: "node",
      target: "node20",
      external: [
        "@getpaseo/plugin",
        "@getpaseo/plugin/server",
        "@getpaseo/plugin/react-native",
        "@getpaseo/plugin/host",
        "zod",
      ],
      logLevel: "silent",
      treeShaking: true,
      write: false,
    });
    const output = result.outputFiles[0]?.text;
    assert.ok(output);
    assert.doesNotMatch(output, /import\.meta|import_meta|fileURLToPath\([^)]*\.url\)/u);
    assert.doesNotMatch(output, /createPaseoClient|CONNECTOR_CREDENTIAL_REQUIRED|ENGINE_DEVELOPMENT_BUILD|sourceRoot|GOMODCACHE|go-build-cache/u);
    const factory = globalThis.eval(`(function(require) { const module = { exports: {} }; const exports = module.exports; ${output}; return module.exports; })`);
    const exports = factory(createRequire(import.meta.url)) as {
      startInstalledConnectorShell(options: unknown): {
        status(): Promise<Record<string, unknown>>;
        close(): Promise<void>;
      };
    };
    let supervisorClosed = false;
    let configReads = 0;
    let supervisorStatus = { state: "current" as const, binding: "8".repeat(64), enginePid: null as number | null, doltPid: null as number | null, restartCount: 0 };
    const paseo = {
      config: {
        async get() {
          configReads += 1;
          return {
            requestId: "commonjs-config",
            config: { plugins: { director: { source: "directory", path: checkout } } },
          };
        },
      },
      projects: {},
      workspaces: {},
      agents: {
        subscribe() { return () => undefined; },
        async list() {
          return { requestId: "commonjs-agents", entries: [], pageInfo: { hasMore: false } };
        },
      },
      providers: {},
    };
    const connector = exports.startInstalledConnectorShell({
      paseo,
      environment: {
        XDG_CONFIG_HOME: configBase,
        XDG_CACHE_HOME: cacheBase,
        XDG_RUNTIME_DIR: runtimeBase,
      },
      installation: {
        schemaVersion: 1,
        state: "prepared",
        connectorCommit: "1".repeat(40),
        releaseMetadata: {
          schemaVersion: 2,
          state: "published",
          version: "1.0.0-alpha.1",
          target: "linux-amd64",
          sourceCandidate: "2".repeat(40),
          binary: {
            name: "director-engine-linux-amd64",
            url: "https://github.com/mcuadros/paseo-director/releases/download/v1.0.0-alpha.1/director-engine-linux-amd64",
            sha256: "3".repeat(64),
            size: 1,
          },
          notices: {
            name: "THIRD_PARTY_NOTICES.txt",
            url: "https://github.com/mcuadros/paseo-director/releases/download/v1.0.0-alpha.1/THIRD_PARTY_NOTICES.txt",
            sha256: "4".repeat(64),
            size: 1,
          },
          dolt: {
            version: "2.3.2",
            archive: {
              name: "dolt-linux-amd64.tar.gz",
              url: "https://github.com/dolthub/dolt/releases/download/v2.3.2/dolt-linux-amd64.tar.gz",
              sha256: "6".repeat(64),
              size: 1,
            },
            executableSha256: "7".repeat(64),
            executableSize: 1,
          },
        },
      },
      reportActivation: false,
      dependencies: {
        hostCompatibility() {
          return {
            paseoVersion: "0.7.2",
            nodeVersion: "22.0.0",
            platform: "linux",
            architecture: "x64",
            target: "linux-amd64",
          };
        },
        boardTransport: { async load() { return { schemaVersion: 1, cursor: "0", tasks: [] }; } },
        planningTransport: {
          async query() { throw new Error("not called"); },
          async taskDetail() { throw new Error("not called"); },
          async mutate() { throw new Error("not called"); },
        },
        async resolveEngine() {
          return {
            mode: "release",
            version: "1.0.0-alpha.1",
            sourceCandidate: "2".repeat(40),
            target: "linux-amd64",
            binaryPath: "/not-exposed/director-engine",
            noticesPath: "/not-exposed/THIRD_PARTY_NOTICES.txt",
            binarySha256: "3".repeat(64),
            noticesSha256: "4".repeat(64),
            connectorCommit: "1".repeat(40),
            contractVersion: "director-host/v1",
            contractSha256: "5".repeat(64),
          };
        },
        async resolveDolt() {
          return {
            version: "2.3.2",
            target: "linux-amd64",
            binaryPath: "/not-exposed/dolt",
            binarySha256: "7".repeat(64),
            archiveSha256: "6".repeat(64),
          };
        },
        async ensureRuntimeSupervisor() {
          return {
            binding: "8".repeat(64),
            async status() {
              return supervisorStatus;
            },
            async close() { supervisorClosed = true; },
            async release() { supervisorClosed = true; },
          };
        },
      },
    });
    await assert.rejects(connector.status(), (error: unknown) =>
      error !== null && typeof error === "object" && Reflect.get(error, "code") === "DIRECTOR_RUNTIME_CHILDREN_NOT_READY");
    supervisorStatus = { ...supervisorStatus, enginePid: 1, doltPid: 2 };
    const status = await connector.status();
    assert.equal(configReads, 1);
    assert.equal(status.state, "board-ready");
    assert.ok(status.activation && typeof status.activation === "object");
    const activation = status.activation as {
      lifecycle: string;
      result: string;
      configurationSchemaVersion: number;
      configurationSha256: string;
      legacyEnvironment: string;
      settings: Array<{ name: string; source: string }>;
    };
    assert.deepEqual(activation, {
      lifecycle: "plugin-reload",
      result: "running-current",
      configurationSchemaVersion: 2,
      configurationSha256: activation.configurationSha256,
      legacyEnvironment: "absent",
      settings: [
        { name: "engine.mode", source: "defaulted" },
        { name: "engine.url", source: "defaulted" },
        { name: "engine.cache-base", source: "overridden" },
        { name: "engine.runtime-base", source: "overridden" },
      ],
    });
    await connector.close();
    assert.equal(supervisorClosed, true);
  } finally {
    rmSync(root, { recursive: true, force: true });
  }
});
