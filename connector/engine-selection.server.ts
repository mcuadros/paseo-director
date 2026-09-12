// SPDX-License-Identifier: Apache-2.0
// Connector-side process selection preserves explicit, disjoint engine modes.

import { createHash } from "node:crypto";
import { homedir } from "node:os";
import {
  isAbsolute,
  join,
  resolve,
} from "node:path";

import {
  canonicalProspectivePath,
  pathIsWithin,
  pathsAreDisjoint,
  type RuntimeConfiguration,
} from "./runtime-configuration.server.mjs";

export { canonicalProspectivePath, pathIsWithin, pathsAreDisjoint };

export type ReleaseEngineSelection = {
  mode: "release";
  cacheRoot: string;
  checkoutRoot: string;
  metadataPath: string;
  releaseMetadata?: Uint8Array;
  connectorCommit?: string;
};

export type DevelopmentEngineSelection = {
  mode: "development";
  sourceRoot: string;
  cacheRoot: string;
  moduleCache: string;
  checkoutRoot: string;
  connectorCommit?: string;
};

export type EngineSelection =
  | ReleaseEngineSelection
  | DevelopmentEngineSelection;

export class EngineSelectionError extends Error {
  readonly code: string;

  constructor(code: string, message: string) {
    super(message);
    this.name = "EngineSelectionError";
    this.code = code;
  }
}

export function developmentEnginePaths(
  selection: DevelopmentEngineSelection,
  sourceCandidate = "selected-source",
): { binaryPath: string; temporaryBinaryPath: string; goCache: string } {
  const developmentRoot = join(selection.cacheRoot, "development");
  const sourceIdentity = createHash("sha256")
    .update(resolve(selection.sourceRoot))
    .digest("hex");
  const binaryPath = join(
    developmentRoot,
    sourceIdentity,
    sourceCandidate,
    "director-engine",
  );
  return {
    binaryPath,
    temporaryBinaryPath: join(
      developmentRoot,
      sourceIdentity,
      `.partial-${sourceCandidate}-${process.pid}`,
      "director-engine",
    ),
    goCache: join(developmentRoot, "go-build-cache"),
  };
}

export type InstalledConnectorMetadata = {
  readonly schemaVersion: 1;
  readonly state: "prepared" | "unprepared";
  readonly connectorCommit: string;
  readonly releaseMetadata: Readonly<Record<string, unknown>>;
};

export function selectInstalledEngine(
  configuration: RuntimeConfiguration,
  installation: InstalledConnectorMetadata,
  environment: NodeJS.ProcessEnv,
): EngineSelection {
  if (
    installation.schemaVersion !== 1 ||
    installation.state !== "prepared" ||
    !/^[0-9a-f]{40}$/u.test(installation.connectorCommit)
  ) {
    throw new EngineSelectionError(
      "ENGINE_INSTALL_NOT_PREPARED",
      "the connector was not prepared by the declared Paseo install boundary",
    );
  }
  const cacheBase = environment.XDG_CACHE_HOME
    ? absolutePath(environment.XDG_CACHE_HOME, "ENGINE_CACHE_PATH")
    : canonicalProspectivePath(join(homedir(), ".cache"));
  const cacheRoot = canonicalProspectivePath(join(cacheBase, "director", "engines"));
  if (configuration.engine.mode === "release") {
    return {
      mode: "release",
      cacheRoot,
      checkoutRoot: "",
      metadataPath: "",
      connectorCommit: installation.connectorCommit,
      releaseMetadata: Buffer.from(JSON.stringify(installation.releaseMetadata)),
    };
  }
  const sourceRoot = configuration.engine.sourceRoot;
  if (!sourceRoot) {
    throw new EngineSelectionError(
      "ENGINE_SOURCE_REQUIRED",
      "development mode requires an explicit engine source",
    );
  }
  const moduleCache = configuration.engine.moduleCache
    ? absolutePath(configuration.engine.moduleCache, "ENGINE_MODULE_CACHE_PATH")
    : canonicalProspectivePath(join(homedir(), "go", "pkg", "mod"));
  return {
    mode: "development",
    sourceRoot: absolutePath(sourceRoot, "ENGINE_SOURCE_REQUIRED"),
    cacheRoot,
    moduleCache,
    checkoutRoot: "",
    connectorCommit: installation.connectorCommit,
  };
}

function absolutePath(value: string | undefined, code: string): string {
  if (!value || !isAbsolute(value)) {
    throw new EngineSelectionError(code, "engine paths must be absolute");
  }
  return canonicalProspectivePath(resolve(value));
}

export function selectEngine(
  environment: NodeJS.ProcessEnv,
  checkoutRoot: string,
): EngineSelection {
  const mode = environment.DIRECTOR_ENGINE_MODE;
  if (mode !== "release" && mode !== "development") {
    throw new EngineSelectionError(
      "ENGINE_MODE_REQUIRED",
      "DIRECTOR_ENGINE_MODE must be explicitly release or development",
    );
  }
  const cacheBase = environment.XDG_CACHE_HOME
    ? absolutePath(environment.XDG_CACHE_HOME, "ENGINE_CACHE_PATH")
    : canonicalProspectivePath(join(homedir(), ".cache"));
  const cacheRoot = canonicalProspectivePath(join(cacheBase, "director", "engines"));
  if (pathIsWithin(cacheRoot, checkoutRoot)) {
    throw new EngineSelectionError(
      "ENGINE_CACHE_IN_CHECKOUT",
      "the engine cache must be outside the plugin checkout",
    );
  }
  if (mode === "release") {
    if (environment.DIRECTOR_ENGINE_SOURCE_ROOT) {
      throw new EngineSelectionError(
        "ENGINE_MODE_CONFLICT",
        "release mode cannot select development source",
      );
    }
    return {
      mode,
      checkoutRoot: canonicalProspectivePath(checkoutRoot),
      metadataPath: canonicalProspectivePath(join(checkoutRoot, "release", "engine.json")),
      cacheRoot,
    };
  }
  if (environment.DIRECTOR_ENGINE_RELEASE_METADATA) {
    throw new EngineSelectionError(
      "ENGINE_MODE_CONFLICT",
      "development mode cannot select release metadata",
    );
  }
  const moduleCache = environment.GOMODCACHE
    ? absolutePath(environment.GOMODCACHE, "ENGINE_MODULE_CACHE_PATH")
    : join(homedir(), "go", "pkg", "mod");
  if (!pathsAreDisjoint(moduleCache, checkoutRoot)) {
    throw new EngineSelectionError(
      "ENGINE_MODULE_CACHE_IN_CHECKOUT",
      "the development module cache must be outside the plugin checkout",
    );
  }
  return {
    mode,
    checkoutRoot: canonicalProspectivePath(checkoutRoot),
    sourceRoot: absolutePath(
      environment.DIRECTOR_ENGINE_SOURCE_ROOT,
      "ENGINE_SOURCE_REQUIRED",
    ),
    cacheRoot,
    moduleCache,
  };
}

export function engineProcessEnvironment(
  source: NodeJS.ProcessEnv,
  goCache: string,
  moduleCache: string,
): NodeJS.ProcessEnv {
  const environment: NodeJS.ProcessEnv = {
    CGO_ENABLED: "0",
    GOCACHE: goCache,
    GOMODCACHE: moduleCache,
    GOTOOLCHAIN: "local",
    GOWORK: "off",
  };
  if (source.PATH) {
    environment.PATH = source.PATH;
  }
  return environment;
}
