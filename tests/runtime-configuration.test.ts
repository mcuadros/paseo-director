// SPDX-License-Identifier: Apache-2.0

import assert from "node:assert/strict";
import {
  chmodSync,
  mkdirSync,
  mkdtempSync,
  rmSync,
  writeFileSync,
} from "node:fs";
import { tmpdir } from "node:os";
import { dirname, join } from "node:path";
import test from "node:test";

import {
  RuntimeConfigurationError,
  assertRuntimeDeployment,
  loadRuntimeConfiguration,
  parseRuntimeConfiguration,
  runtimeConfigurationPath,
} from "../connector/runtime-configuration.server.mjs";

function fixture(engine: Record<string, string> = {}) {
  const root = mkdtempSync(join(tmpdir(), "director-runtime-config-"));
  const configBase = join(root, "config");
  const cacheBase = join(root, "cache");
  const checkout = join(root, "checkout");
  const credential = join(root, "authority", "connector.password");
  for (const path of [dirname(credential), configBase, cacheBase, checkout]) {
    mkdirSync(path, { recursive: true, mode: 0o700 });
    chmodSync(path, 0o700);
  }
  writeFileSync(credential, "test-only-secret\n", { mode: 0o600 });
  const path = runtimeConfigurationPath({ XDG_CONFIG_HOME: configBase }, root);
  mkdirSync(dirname(path), { recursive: true, mode: 0o700 });
  chmodSync(dirname(path), 0o700);
  writeFileSync(path, `${JSON.stringify({
    schemaVersion: 1,
    paseo: { credentialFile: credential },
    engine,
  })}\n`, { mode: 0o600 });
  return { root, configBase, cacheBase, checkout, credential, path };
}

test("runtime configuration supplies only deterministic safe defaults", () => {
  const current = fixture();
  try {
    const loaded = loadRuntimeConfiguration({
      XDG_CONFIG_HOME: current.configBase,
      XDG_CACHE_HOME: current.cacheBase,
    }, current.root);
    assert.equal(loaded.configuration.engine.mode, "release");
    assert.equal(loaded.configuration.engine.url, "http://127.0.0.1:7041");
    assert.deepEqual(loaded.configuration.diagnostics.settings, [
      { name: "paseo.url", source: "defaulted" },
      { name: "engine.mode", source: "defaulted" },
      { name: "engine.url", source: "defaulted" },
      { name: "engine.cache-base", source: "overridden" },
    ]);
    const diagnostic = JSON.stringify(loaded.configuration.diagnostics);
    assert.doesNotMatch(diagnostic, /test-only-secret|connector\.password|127\.0\.0\.1/u);
  } finally {
    rmSync(current.root, { recursive: true, force: true });
  }
});

test("explicit development overrides remain strict and path-safe", () => {
  const source = mkdtempSync(join(tmpdir(), "director-runtime-source-"));
  const moduleCache = mkdtempSync(join(tmpdir(), "director-runtime-module-cache-"));
  const current = fixture({
    mode: "development",
    url: "http://[::1]:7042",
    sourceRoot: source,
    moduleCache,
  });
  try {
    const environment = {
      XDG_CONFIG_HOME: current.configBase,
      XDG_CACHE_HOME: current.cacheBase,
    };
    const loaded = loadRuntimeConfiguration(environment, current.root);
    assert.equal(loaded.configuration.engine.mode, "development");
    assert.equal(loaded.configuration.engine.url, "http://[::1]:7042");
    assert.deepEqual(loaded.configuration.diagnostics.settings, [
      { name: "paseo.url", source: "defaulted" },
      { name: "engine.mode", source: "overridden" },
      { name: "engine.url", source: "overridden" },
      { name: "engine.cache-base", source: "overridden" },
      { name: "engine.module-cache", source: "overridden" },
    ]);
    assert.equal(
      assertRuntimeDeployment(current.checkout, loaded, environment),
      loaded.configuration,
    );
  } finally {
    rmSync(current.root, { recursive: true, force: true });
    rmSync(source, { recursive: true, force: true });
    rmSync(moduleCache, { recursive: true, force: true });
  }
});

test("legacy daemon environment is ignored without restart and unsafe runtime files fail closed", () => {
  const current = fixture();
  try {
    const migrated = loadRuntimeConfiguration({
      XDG_CONFIG_HOME: current.configBase,
      DIRECTOR_ENGINE_URL: "http://secret.invalid/",
    }, current.root);
    assert.equal(migrated.configuration.engine.url, "http://127.0.0.1:7041");
    assert.equal(migrated.configuration.diagnostics.legacyEnvironment, "ignored");
    assert.doesNotMatch(JSON.stringify(migrated.configuration.diagnostics), /secret\.invalid/u);
    chmodSync(current.path, 0o644);
    assert.throws(
      () => loadRuntimeConfiguration({ XDG_CONFIG_HOME: current.configBase }, current.root),
      (error: unknown) =>
        error instanceof RuntimeConfigurationError &&
        error.code === "DIRECTOR_RUNTIME_CONFIG_REQUIRED",
    );
  } finally {
    rmSync(current.root, { recursive: true, force: true });
  }
});

test("configuration rejects unknown fields, non-loopback authority, and checkout overlap", () => {
  assert.throws(
    () => parseRuntimeConfiguration(Buffer.from('{"schemaVersion":1,"schemaVersion":1,"paseo":{"credentialFile":"/private/credential"},"engine":{}}')),
    (error: unknown) => error instanceof RuntimeConfigurationError && error.code === "DIRECTOR_RUNTIME_CONFIG_JSON",
  );
  assert.throws(
    () => parseRuntimeConfiguration(Buffer.from(JSON.stringify({
      schemaVersion: 1,
      paseo: { credentialFile: "/private/credential", duplicatedHostUrl: "wss://example.invalid/ws" },
      engine: {},
    }))),
    (error: unknown) => error instanceof RuntimeConfigurationError && error.code === "DIRECTOR_RUNTIME_CONFIG_SCHEMA",
  );
  assert.throws(
    () => parseRuntimeConfiguration(Buffer.from(JSON.stringify({
      schemaVersion: 1,
      paseo: { credentialFile: "/private/credential" },
      engine: { guessedAuthority: true },
    }))),
    (error: unknown) => error instanceof RuntimeConfigurationError && error.code === "DIRECTOR_RUNTIME_CONFIG_SCHEMA",
  );

  const current = fixture();
  try {
    const overlappingCredential = join(current.checkout, "connector.password");
    writeFileSync(overlappingCredential, "overlap-test-secret\n", { mode: 0o600 });
    writeFileSync(current.path, `${JSON.stringify({
      schemaVersion: 1,
      paseo: {
        credentialFile: overlappingCredential,
      },
      engine: {},
    })}\n`, { mode: 0o600 });
    const loaded = loadRuntimeConfiguration({ XDG_CONFIG_HOME: current.configBase }, current.root);
    assert.throws(
      () => assertRuntimeDeployment(current.checkout, loaded, { XDG_CONFIG_HOME: current.configBase }),
      (error: unknown) =>
        error instanceof RuntimeConfigurationError &&
        error.code === "DIRECTOR_RUNTIME_CREDENTIAL_OVERLAP",
    );
  } finally {
    rmSync(current.root, { recursive: true, force: true });
  }
});
