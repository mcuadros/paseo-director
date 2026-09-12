// SPDX-License-Identifier: Apache-2.0
// Connector-side process selection preserves explicit, disjoint engine modes.

import { createHash } from "node:crypto";
import { homedir } from "node:os";
import { existsSync, realpathSync } from "node:fs";
import {
  basename,
  dirname,
  isAbsolute,
  join,
  relative,
  resolve,
} from "node:path";

export type ReleaseEngineSelection = {
  mode: "release";
  checkoutRoot: string;
  metadataPath: string;
  cacheRoot: string;
};

export type DevelopmentEngineSelection = {
  mode: "development";
  checkoutRoot: string;
  sourceRoot: string;
  cacheRoot: string;
  moduleCache: string;
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

export function canonicalProspectivePath(path: string): string {
  let existing = resolve(path);
  const missing: string[] = [];
  while (!existsSync(existing)) {
    const parent = dirname(existing);
    if (parent === existing) break;
    missing.unshift(basename(existing));
    existing = parent;
  }
  return resolve(realpathSync(existing), ...missing);
}

export function pathIsWithin(candidate: string, parent: string): boolean {
  const pathFromParent = relative(
    canonicalProspectivePath(parent),
    canonicalProspectivePath(candidate),
  );
  return (
    pathFromParent === "" ||
    (!pathFromParent.startsWith("..") && !pathFromParent.startsWith("/"))
  );
}

export function pathsAreDisjoint(left: string, right: string): boolean {
  return !pathIsWithin(left, right) && !pathIsWithin(right, left);
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
