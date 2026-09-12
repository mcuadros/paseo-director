// SPDX-License-Identifier: Apache-2.0

import assert from "node:assert/strict";
import { mkdirSync, mkdtempSync, rmSync, writeFileSync } from "node:fs";
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
  const checkout = join(root, "checkout");
  const sourceRoot = join(root, "source");
  const credential = join(root, "authority", "connector.password");
  for (const path of [configBase, cacheBase, checkout, sourceRoot, dirname(credential)]) {
    mkdirSync(path, { recursive: true, mode: 0o700 });
  }
  writeFileSync(credential, "commonjs-test-secret\n", { mode: 0o600 });
  const runtimePath = join(configBase, "director", "runtime.json");
  mkdirSync(dirname(runtimePath), { recursive: true, mode: 0o700 });
  writeFileSync(runtimePath, `${JSON.stringify({
    schemaVersion: 1,
    paseo: { credentialFile: credential },
    engine: { mode: "development", sourceRoot },
  })}\n`, { mode: 0o600 });

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
    const factory = globalThis.eval(`(function(require) { const module = { exports: {} }; const exports = module.exports; ${output}; return module.exports; })`);
    const exports = factory(createRequire(import.meta.url)) as {
      startInstalledConnectorShell(options: unknown): {
        status(): Promise<Record<string, unknown>>;
        close(): Promise<void>;
      };
    };
    let closed = false;
    let paseoURL = "";
    const connector = exports.startInstalledConnectorShell({
      environment: {
        XDG_CONFIG_HOME: configBase,
        XDG_CACHE_HOME: cacheBase,
      },
      installation: {
        schemaVersion: 1,
        state: "prepared",
        connectorCommit: "1".repeat(40),
        releaseMetadata: {
          schemaVersion: 2,
          state: "unpublished",
          version: "0.0.0-scaffold",
          target: "linux-amd64",
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
        createClient(configuration: { url: string }) {
          paseoURL = configuration.url;
          return {
            async connect() {},
            async close() { closed = true; },
            config: {
              async get() {
                return {
                  requestId: "commonjs-config",
                  config: { plugins: { director: { source: "directory", path: checkout } } },
                };
              },
            },
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
            mode: "development",
            version: "0.0.0-dev",
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
      },
    });
    const status = await connector.status();
    assert.equal(paseoURL, "ws://127.0.0.1:6767/ws");
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
      configurationSchemaVersion: 1,
      configurationSha256: activation.configurationSha256,
      legacyEnvironment: "absent",
      settings: [
        { name: "paseo.url", source: "defaulted" },
        { name: "engine.mode", source: "overridden" },
        { name: "engine.url", source: "defaulted" },
        { name: "engine.cache-base", source: "overridden" },
        { name: "engine.module-cache", source: "defaulted" },
      ],
    });
    await connector.close();
    assert.equal(closed, true);
  } finally {
    rmSync(root, { recursive: true, force: true });
  }
});
