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

import { startConnectorShell, type ConnectorEngineSelection } from "../connector/paseo.server.ts";
import {
  ConnectorCredentialError,
  loadConnectorCredential,
} from "../connector/credential.server.ts";
import { BootstrapLauncherError, type ResolvedEngine } from "../connector/bootstrap-launcher.server.ts";
import { HostCompatibilityError } from "../connector/compatibility.server.ts";
import { BoardTransportError } from "../connector/engine-board.server.ts";

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
  secureDirectory(join(checkoutRoot, "release"));
  writeFileSync(join(checkoutRoot, "release", "engine.json"), JSON.stringify({
    schemaVersion: 2,
    state: "unpublished",
    version: "0.0.0-scaffold",
    target: "linux-amd64",
  }));
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

function hostCompatibility() {
  return {
    paseoVersion: "0.7.2" as const,
    nodeVersion: "22.0.0",
    platform: "linux" as const,
    architecture: "x64" as const,
    target: "linux-amd64" as const,
  };
}

function resolvedEngine(_selection: ConnectorEngineSelection): Promise<ResolvedEngine> {
  return Promise.resolve({
    mode: "release",
    version: "1.0.0",
    sourceCandidate: "1".repeat(40),
    target: "linux-amd64",
    binaryPath: "/not-exposed/director-engine",
    noticesPath: "/not-exposed/THIRD_PARTY_NOTICES.txt",
    binarySha256: "2".repeat(64),
    noticesSha256: "3".repeat(64),
    connectorCommit: "4".repeat(40),
    contractVersion: "director.host/v1",
    contractSha256: "5".repeat(64),
  });
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
            DIRECTOR_ENGINE_MODE: "release",
            XDG_CACHE_HOME: boundary.cacheBase,
          },
          dependencies: {
            hostCompatibility,
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

test("incompatible host fails before credential loading or client construction", () => {
  const boundary = temporaryBoundary();
  let clients = 0;
  try {
    assert.throws(() => startConnectorShell({
      checkoutRoot: boundary.checkoutRoot,
      environment: {},
      dependencies: {
        hostCompatibility() {
          throw new HostCompatibilityError("PASEO_VERSION_UNSUPPORTED", "Director supports exact Paseo 0.7.2; observed 0.8.0");
        },
        createClient() { clients += 1; return { async close() {} }; },
      },
    }), (error: unknown) => error instanceof HostCompatibilityError && error.code === "PASEO_VERSION_UNSUPPORTED");
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
            DIRECTOR_ENGINE_MODE: "release",
            XDG_CACHE_HOME: boundary.cacheBase,
          },
          dependencies: {
            hostCompatibility,
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

test("the legacy external connector descriptor never propagates its credential", async () => {
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
        DIRECTOR_ENGINE_MODE: "release",
        XDG_CACHE_HOME: boundary.cacheBase,
      },
      dependencies: {
        hostCompatibility,
        resolveEngine: resolvedEngine,
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
    const status = await connector.status();
    const serialized = JSON.stringify(status);
    assert.doesNotMatch(serialized, /boundary-secret|connector\.password/);
    assert.doesNotMatch(serialized, /binaryPath|not-exposed|checkout|cache-base/);
    assert.deepEqual(Object.keys(status.descriptor).sort(), [
      "capabilities",
      "contractHash",
      "contractVersion",
      "credentialScope",
    ]);

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
          effectKind: "task_agent.create_with_bootstrap",
          effectId: "effect-1",
          bindingHash: "1".repeat(64),
        },
      }),
      /HOST_PUBLIC_SDK_UNAVAILABLE/,
    );
    await connector.close();
    assert.equal(closed, true);
  } finally {
    rmSync(boundary.root, { recursive: true, force: true });
  }
});

test("engine verification failure closes connector authority before startup becomes ready", async () => {
  const boundary = temporaryBoundary();
  let closed = false;
  let rejectEngine!: (reason: unknown) => void;
  const engine = new Promise<ResolvedEngine>((_resolve, reject) => {
    rejectEngine = reject;
  });
  try {
    const connector = startConnectorShell({
      checkoutRoot: boundary.checkoutRoot,
      environment: {
        DIRECTOR_PASEO_CREDENTIAL_FILE: boundary.credentialPath,
        DIRECTOR_PASEO_URL: "ws://127.0.0.1:6767/ws",
        DIRECTOR_ENGINE_URL: "http://127.0.0.1:7041",
        DIRECTOR_ENGINE_MODE: "release",
        XDG_CACHE_HOME: boundary.cacheBase,
      },
      dependencies: {
        hostCompatibility,
        resolveEngine() { return engine; },
        createClient() { return { async close() { closed = true; } }; },
      },
    });
    const status = connector.status();
    rejectEngine(new BootstrapLauncherError("ENGINE_IDENTITY_MISMATCH", "engine identity does not match the installed pin"));
    await assert.rejects(status, (error: unknown) =>
      error instanceof BootstrapLauncherError && error.code === "ENGINE_IDENTITY_MISMATCH");
    assert.equal(closed, true);
  } finally {
    rmSync(boundary.root, { recursive: true, force: true });
  }
});
