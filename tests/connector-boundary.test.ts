// SPDX-License-Identifier: Apache-2.0

import assert from "node:assert/strict";
import fs, {
  chmodSync,
  mkdirSync,
  mkdtempSync,
  renameSync,
  rmSync,
  symlinkSync,
  unlinkSync,
  writeFileSync,
} from "node:fs";
import { syncBuiltinESMExports } from "node:module";
import { tmpdir } from "node:os";
import { join } from "node:path";
import test from "node:test";

import type { PaseoClientConfig } from "@getpaseo/client";

import { startConnectorShell } from "../connector/paseo.server.ts";
import {
  ConnectorCredentialError,
  loadConnectorCredential,
} from "../connector/credential.server.ts";
import { engineBoundaryPaths } from "../connector/engine-distribution.server.ts";
import { BoardTransportError } from "../connector/engine-board.server.ts";
import {
  developmentEnginePaths,
  engineProcessEnvironment,
  selectEngine,
} from "../connector/engine-selection.server.ts";

function secureDirectory(path: string): void {
  mkdirSync(path, { recursive: true, mode: 0o700 });
  chmodSync(path, 0o700);
}

function temporaryBoundary() {
  const root = mkdtempSync(join(tmpdir(), "director-connector-test-"));
  const checkoutRoot = join(root, "checkout");
  const sourceRoot = join(root, "source");
  const cacheBase = join(root, "cache-base");
  const credentialDirectory = join(root, "credential");
  const credentialPath = join(credentialDirectory, "connector.password");
  for (const path of [checkoutRoot, sourceRoot, cacheBase, credentialDirectory]) {
    secureDirectory(path);
  }
  writeFileSync(credentialPath, "boundary-secret\n", { mode: 0o600 });
  return {
    root,
    checkoutRoot,
    sourceRoot,
    cacheBase,
    credentialDirectory,
    credentialPath,
  };
}

test("credential loading fails closed for absence, content, location, and permissions", () => {
  const boundary = temporaryBoundary();
  const baseOptions = {
    checkoutRoot: boundary.checkoutRoot,
    disjointEnginePaths: [] as string[],
  };
  try {
    assert.throws(
      () => loadConnectorCredential({ ...baseOptions, credentialPath: undefined }),
      (error: unknown) =>
        error instanceof ConnectorCredentialError &&
        error.code === "CONNECTOR_CREDENTIAL_REQUIRED",
    );

    writeFileSync(boundary.credentialPath, "", { mode: 0o600 });
    assert.throws(
      () =>
        loadConnectorCredential({
          ...baseOptions,
          credentialPath: boundary.credentialPath,
        }),
      (error: unknown) =>
        error instanceof ConnectorCredentialError &&
        error.code === "CONNECTOR_CREDENTIAL_EMPTY",
    );

    writeFileSync(boundary.credentialPath, "boundary-secret\n", { mode: 0o644 });
    chmodSync(boundary.credentialPath, 0o644);
    assert.throws(
      () =>
        loadConnectorCredential({
          ...baseOptions,
          credentialPath: boundary.credentialPath,
        }),
      (error: unknown) =>
        error instanceof ConnectorCredentialError &&
        error.code === "CONNECTOR_CREDENTIAL_PERMISSIONS",
    );
    chmodSync(boundary.credentialPath, 0o600);

    chmodSync(boundary.credentialDirectory, 0o770);
    assert.throws(
      () =>
        loadConnectorCredential({
          ...baseOptions,
          credentialPath: boundary.credentialPath,
        }),
      (error: unknown) =>
        error instanceof ConnectorCredentialError &&
        error.code === "CONNECTOR_CREDENTIAL_DIRECTORY_PERMISSIONS",
    );
    chmodSync(boundary.credentialDirectory, 0o700);

    const inCheckoutDirectory = join(boundary.checkoutRoot, "credential");
    secureDirectory(inCheckoutDirectory);
    const inCheckout = join(inCheckoutDirectory, "connector.password");
    writeFileSync(inCheckout, "boundary-secret\n", { mode: 0o600 });
    assert.throws(
      () => loadConnectorCredential({ ...baseOptions, credentialPath: inCheckout }),
      (error: unknown) =>
        error instanceof ConnectorCredentialError &&
        error.code === "CONNECTOR_CREDENTIAL_IN_CHECKOUT",
    );
  } finally {
    rmSync(boundary.root, { recursive: true, force: true });
  }
});

test("credential ancestors reject writable substitution paths and permit sticky protection", () => {
  const boundary = temporaryBoundary();
  const baseOptions = {
    checkoutRoot: boundary.checkoutRoot,
    disjointEnginePaths: [] as string[],
  };
  const createCredential = (directory: string, content = "boundary-secret\n") => {
    secureDirectory(directory);
    const path = join(directory, "connector.password");
    writeFileSync(path, content, { mode: 0o600 });
    return path;
  };
  try {
    for (const mode of [0o770, 0o777]) {
      const unsafeAncestor = join(boundary.root, `unsafe-${mode.toString(8)}`);
      const credentialPath = createCredential(
        join(unsafeAncestor, "credential"),
      );
      chmodSync(unsafeAncestor, mode);
      assert.throws(
        () => loadConnectorCredential({ ...baseOptions, credentialPath }),
        (error: unknown) =>
          error instanceof ConnectorCredentialError &&
          error.code === "CONNECTOR_CREDENTIAL_DIRECTORY_PERMISSIONS",
        `ancestor mode ${mode.toString(8)} must fail closed`,
      );
    }

    const stickyAncestor = join(boundary.root, "sticky-ancestor");
    const stickyCredential = createCredential(
      join(stickyAncestor, "credential"),
    );
    chmodSync(stickyAncestor, 0o1777);
    assert.equal(
      loadConnectorCredential({
        ...baseOptions,
        credentialPath: stickyCredential,
      }),
      "boundary-secret",
    );

    const substitutedAncestor = join(boundary.root, "renamed-ancestor");
    const substitutedCredential = createCredential(
      join(substitutedAncestor, "credential"),
    );
    renameSync(substitutedAncestor, `${substitutedAncestor}-original`);
    createCredential(
      join(substitutedAncestor, "credential"),
      "substituted-secret\n",
    );
    chmodSync(substitutedAncestor, 0o777);
    assert.throws(
      () =>
        loadConnectorCredential({
          ...baseOptions,
          credentialPath: substitutedCredential,
        }),
      (error: unknown) =>
        error instanceof ConnectorCredentialError &&
        error.code === "CONNECTOR_CREDENTIAL_DIRECTORY_PERMISSIONS",
      "renaming a checked-looking ancestor and substituting a writable tree must fail closed",
    );

    const trustedTarget = join(boundary.root, "trusted-target");
    createCredential(trustedTarget);
    const unsafeTargetRoot = join(boundary.root, "unsafe-target-root");
    const unsafeTarget = join(unsafeTargetRoot, "credential");
    createCredential(unsafeTarget, "substituted-secret\n");
    chmodSync(unsafeTargetRoot, 0o777);
    const credentialLink = join(boundary.root, "credential-link");
    symlinkSync(trustedTarget, credentialLink);
    const linkedCredential = join(credentialLink, "connector.password");
    assert.equal(
      loadConnectorCredential({
        ...baseOptions,
        credentialPath: linkedCredential,
      }),
      "boundary-secret",
    );
    unlinkSync(credentialLink);
    symlinkSync(unsafeTarget, credentialLink);
    assert.throws(
      () =>
        loadConnectorCredential({
          ...baseOptions,
          credentialPath: linkedCredential,
        }),
      (error: unknown) =>
        error instanceof ConnectorCredentialError &&
        error.code === "CONNECTOR_CREDENTIAL_DIRECTORY_PERMISSIONS",
      "substituting the credential symlink with a writable-ancestor target must fail closed",
    );
  } finally {
    rmSync(boundary.root, { recursive: true, force: true });
  }
});

test("credential loading rejects mutable metadata changes across the descriptor read", (context) => {
  const boundary = temporaryBoundary();
  const originalReadFileSync = fs.readFileSync;
  try {
    context.mock.method(fs, "readFileSync", (path: unknown, options: unknown) => {
      if (typeof path !== "number" || options !== "utf8") {
        throw new Error("unexpected credential test read");
      }
      const contents = originalReadFileSync(path, options);
      writeFileSync(
        boundary.credentialPath,
        "changed-boundary-secret-with-a-different-size\n",
        { mode: 0o600 },
      );
      return contents;
    });
    syncBuiltinESMExports();

    assert.throws(
      () =>
        loadConnectorCredential({
          checkoutRoot: boundary.checkoutRoot,
          credentialPath: boundary.credentialPath,
          disjointEnginePaths: [],
        }),
      (error: unknown) =>
        error instanceof ConnectorCredentialError &&
        error.code === "CONNECTOR_CREDENTIAL_METADATA_CHANGED" &&
        error.message ===
          "the connector credential metadata changed between the pre-read and post-read checks",
    );
  } finally {
    context.mock.restoreAll();
    syncBuiltinESMExports();
    rmSync(boundary.root, { recursive: true, force: true });
  }
});

test("credential loading pins every mutable metadata field across the descriptor read", async (context) => {
  const fields = ["size", "mtimeMs", "ctimeMs", "mode", "uid", "nlink"] as const;
  const originalFstatSync = fs.fstatSync;

  for (const field of fields) {
    await context.test(field, (fieldContext) => {
      const boundary = temporaryBoundary();
      let calls = 0;
      try {
        fieldContext.mock.method(fs, "fstatSync", (descriptor: number) => {
          const status = originalFstatSync(descriptor);
          calls += 1;
          if (calls !== 2) return status;
          return new Proxy(status, {
            get(target, property) {
              const value = Reflect.get(target, property, target);
              return property === field ? value + 1 : value;
            },
          });
        });
        syncBuiltinESMExports();

        assert.throws(
          () =>
            loadConnectorCredential({
              checkoutRoot: boundary.checkoutRoot,
              credentialPath: boundary.credentialPath,
              disjointEnginePaths: [],
            }),
          (error: unknown) =>
            error instanceof ConnectorCredentialError &&
            error.code === "CONNECTOR_CREDENTIAL_METADATA_CHANGED" &&
            error.message ===
              "the connector credential metadata changed between the pre-read and post-read checks",
          field,
        );
        assert.equal(calls, 2, `${field} test must span exactly two fstat samples`);
      } finally {
        fieldContext.mock.restoreAll();
        syncBuiltinESMExports();
        rmSync(boundary.root, { recursive: true, force: true });
      }
    });
  }
});

test("connector startup fails before constructing a client when credential is absent", () => {
  const boundary = temporaryBoundary();
  let clients = 0;
  try {
    assert.throws(
      () =>
        startConnectorShell({
          checkoutRoot: boundary.checkoutRoot,
          environment: {
            DIRECTOR_PASEO_URL: "ws://127.0.0.1:6767/ws",
            DIRECTOR_ENGINE_MODE: "development",
            DIRECTOR_ENGINE_SOURCE_ROOT: boundary.sourceRoot,
            XDG_CACHE_HOME: boundary.cacheBase,
          },
          dependencies: {
            createClient() {
              clients += 1;
              return { async close() {} };
            },
          },
        }),
      (error: unknown) =>
        error instanceof ConnectorCredentialError &&
        error.code === "CONNECTOR_CREDENTIAL_REQUIRED",
    );
    assert.equal(clients, 0);
  } finally {
    rmSync(boundary.root, { recursive: true, force: true });
  }
});

test("connector startup requires the explicit engine Board endpoint before constructing a client", () => {
  const boundary = temporaryBoundary();
  let clients = 0;
  try {
    assert.throws(
      () =>
        startConnectorShell({
          checkoutRoot: boundary.checkoutRoot,
          environment: {
            DIRECTOR_PASEO_CREDENTIAL_FILE: boundary.credentialPath,
            DIRECTOR_PASEO_URL: "ws://127.0.0.1:6767/ws",
            DIRECTOR_ENGINE_MODE: "development",
            DIRECTOR_ENGINE_SOURCE_ROOT: boundary.sourceRoot,
            XDG_CACHE_HOME: boundary.cacheBase,
          },
          dependencies: {
            createClient() {
              clients += 1;
              return { async close() {} };
            },
          },
        }),
      (error: unknown) =>
        error instanceof BoardTransportError &&
        error.code === "ENGINE_BOARD_URL",
    );
    assert.equal(clients, 0);
  } finally {
    rmSync(boundary.root, { recursive: true, force: true });
  }
});

test("credential directory is bidirectionally disjoint from every canonical engine path", () => {
  const root = mkdtempSync(join(tmpdir(), "director-disjoint-test-"));
  let clients = 0;

  function rejectCase(options: {
    name: string;
    checkoutRoot: string;
    sourceRoot: string;
    cacheBase: string;
    moduleCache?: string;
    credentialDirectory: string;
    credentialPath?: string;
    expectedCode?: string;
  }): void {
    for (const path of [
      options.checkoutRoot,
      options.sourceRoot,
      options.cacheBase,
      options.moduleCache,
      options.credentialDirectory,
    ].filter((path): path is string => path !== undefined)) {
      secureDirectory(path);
    }
    const credentialPath =
      options.credentialPath ??
      join(options.credentialDirectory, "connector.password");
    writeFileSync(credentialPath, "boundary-secret\n", { mode: 0o600 });
    assert.throws(
      () =>
        startConnectorShell({
          checkoutRoot: options.checkoutRoot,
          environment: {
            DIRECTOR_PASEO_CREDENTIAL_FILE: credentialPath,
            DIRECTOR_PASEO_URL: "ws://127.0.0.1:6767/ws",
            DIRECTOR_ENGINE_MODE: "development",
            DIRECTOR_ENGINE_SOURCE_ROOT: options.sourceRoot,
            XDG_CACHE_HOME: options.cacheBase,
            GOMODCACHE: options.moduleCache,
          },
          dependencies: {
            createClient() {
              clients += 1;
              return { async close() {} };
            },
          },
        }),
      (error: unknown) =>
        error instanceof ConnectorCredentialError &&
        error.code ===
          (options.expectedCode ?? "CONNECTOR_CREDENTIAL_ENGINE_PATH"),
      options.name,
    );
  }

  try {
    const credentialContainsCheckout = join(root, "credential-contains-checkout");
    rejectCase({
      name: "credential directory contains checkout root",
      checkoutRoot: join(credentialContainsCheckout, "credential", "checkout"),
      sourceRoot: join(credentialContainsCheckout, "source"),
      cacheBase: join(credentialContainsCheckout, "cache"),
      credentialDirectory: join(credentialContainsCheckout, "credential"),
      expectedCode: "CONNECTOR_CREDENTIAL_IN_CHECKOUT",
    });

    const sourceContainsCredential = join(root, "source-contains-credential");
    rejectCase({
      name: "credential directory inside source root",
      checkoutRoot: join(sourceContainsCredential, "checkout"),
      sourceRoot: join(sourceContainsCredential, "source"),
      cacheBase: join(sourceContainsCredential, "cache"),
      credentialDirectory: join(sourceContainsCredential, "source", "secrets"),
    });

    const credentialContainsSource = join(root, "credential-contains-source");
    rejectCase({
      name: "credential directory contains source root",
      checkoutRoot: join(credentialContainsSource, "checkout"),
      sourceRoot: join(credentialContainsSource, "credential", "source"),
      cacheBase: join(credentialContainsSource, "cache"),
      credentialDirectory: join(credentialContainsSource, "credential"),
    });

    const cacheContainsCredential = join(root, "cache-contains-credential");
    rejectCase({
      name: "credential directory inside derived cache root",
      checkoutRoot: join(cacheContainsCredential, "checkout"),
      sourceRoot: join(cacheContainsCredential, "source"),
      cacheBase: join(cacheContainsCredential, "cache-base"),
      credentialDirectory: join(
        cacheContainsCredential,
        "cache-base",
        "director",
        "engines",
        "development",
        "go-build-cache",
        "secrets",
      ),
    });

    const credentialContainsCache = join(root, "credential-contains-cache");
    rejectCase({
      name: "credential directory contains cache root",
      checkoutRoot: join(credentialContainsCache, "checkout"),
      sourceRoot: join(credentialContainsCache, "source"),
      cacheBase: join(credentialContainsCache, "credential", "cache-base"),
      credentialDirectory: join(credentialContainsCache, "credential"),
    });

    const moduleCacheContainsCredential = join(root, "module-cache-contains-credential");
    rejectCase({
      name: "credential directory inside development module cache",
      checkoutRoot: join(moduleCacheContainsCredential, "checkout"),
      sourceRoot: join(moduleCacheContainsCredential, "source"),
      cacheBase: join(moduleCacheContainsCredential, "cache"),
      moduleCache: join(moduleCacheContainsCredential, "module-cache"),
      credentialDirectory: join(
        moduleCacheContainsCredential,
        "module-cache",
        "secrets",
      ),
    });

    const symlinkCase = join(root, "symlink-source-overlap");
    const sourceRoot = join(symlinkCase, "source");
    const actualCredentialDirectory = join(sourceRoot, "secrets");
    const credentialLink = join(symlinkCase, "credential-link");
    for (const path of [
      join(symlinkCase, "checkout"),
      sourceRoot,
      join(symlinkCase, "cache"),
      actualCredentialDirectory,
    ]) {
      secureDirectory(path);
    }
    writeFileSync(
      join(actualCredentialDirectory, "connector.password"),
      "boundary-secret\n",
      { mode: 0o600 },
    );
    symlinkSync(actualCredentialDirectory, credentialLink);
    rejectCase({
      name: "symlinked credential directory resolves inside source root",
      checkoutRoot: join(symlinkCase, "checkout"),
      sourceRoot,
      cacheBase: join(symlinkCase, "cache"),
      credentialDirectory: actualCredentialDirectory,
      credentialPath: join(credentialLink, "connector.password"),
    });

    assert.equal(clients, 0, "path rejection must happen before client construction");
  } finally {
    rmSync(root, { recursive: true, force: true });
  }
});

test("development boundary enumerates every derived path passed to the engine", () => {
  const boundary = temporaryBoundary();
  try {
    const selection = selectEngine(
      {
        DIRECTOR_ENGINE_MODE: "development",
        DIRECTOR_ENGINE_SOURCE_ROOT: boundary.sourceRoot,
        XDG_CACHE_HOME: boundary.cacheBase,
      },
      boundary.checkoutRoot,
    );
    assert.equal(selection.mode, "development");
    if (selection.mode !== "development") return;
    const derived = developmentEnginePaths(selection);
    const boundaryPaths = engineBoundaryPaths(selection);
    for (const path of [
      selection.checkoutRoot,
      selection.sourceRoot,
      selection.cacheRoot,
      selection.moduleCache,
      derived.binaryPath,
      derived.temporaryBinaryPath,
      derived.goCache,
    ]) {
      assert.ok(boundaryPaths.includes(path), `missing engine boundary path ${path}`);
    }
  } finally {
    rmSync(boundary.root, { recursive: true, force: true });
  }
});

test("the connector descriptor and engine environment never propagate its credential", async () => {
  const boundary = temporaryBoundary();
  let configuration: PaseoClientConfig | undefined;
  let closed = false;
  try {
    const connector = startConnectorShell({
      checkoutRoot: boundary.checkoutRoot,
      environment: {
        DIRECTOR_PASEO_CREDENTIAL_FILE: boundary.credentialPath,
        DIRECTOR_PASEO_URL: "ws://127.0.0.1:6767/ws",
        DIRECTOR_ENGINE_URL: "http://127.0.0.1:7041",
        DIRECTOR_ENGINE_MODE: "development",
        DIRECTOR_ENGINE_SOURCE_ROOT: boundary.sourceRoot,
        XDG_CACHE_HOME: boundary.cacheBase,
      },
      dependencies: {
        createClient(value) {
          configuration = value;
          return {
            async close() {
              closed = true;
            },
          };
        },
      },
    });
    assert.equal(configuration?.password, "boundary-secret");
    const serialized = JSON.stringify(connector.status());
    assert.doesNotMatch(serialized, /boundary-secret|connector\.password/);
    assert.deepEqual(Object.keys(connector.status().descriptor).sort(), [
      "capabilities",
      "contractHash",
      "contractVersion",
      "credentialScope",
    ]);

    const engineEnvironment = engineProcessEnvironment(
      {
        PATH: "/usr/bin",
        DIRECTOR_PASEO_CREDENTIAL_FILE: boundary.credentialPath,
        DIRECTOR_PASEO_PASSWORD: "boundary-secret",
      },
      join(boundary.root, "go-cache"),
      join(boundary.root, "module-cache"),
    );
    assert.deepEqual(Object.keys(engineEnvironment).sort(), [
      "CGO_ENABLED",
      "GOCACHE",
      "GOMODCACHE",
      "GOTOOLCHAIN",
      "PATH",
    ]);
    assert.doesNotMatch(
      JSON.stringify(engineEnvironment),
      /boundary-secret|connector\.password/,
    );

    await assert.rejects(
      connector.invoke({
        requestId: "request-1",
        idempotencyKey: "effect-1",
        expectedVersion: 1,
        capability: "agent.observe",
        arguments: {
          scope: {
            projectId: "project-1",
            workspaceId: "workspace-1",
            taskId: "task-1",
            runId: "run-1",
          },
          effectKind: "task_agent.create_with_initial_prompt",
          effectId: "effect-1",
          bindingHash: "1".repeat(64),
        },
      }),
      /HOST_CAPABILITY_NOT_IMPLEMENTED/,
    );
    await connector.close();
    assert.equal(closed, true);
  } finally {
    rmSync(boundary.root, { recursive: true, force: true });
  }
});
