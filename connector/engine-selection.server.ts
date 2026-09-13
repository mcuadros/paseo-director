// SPDX-License-Identifier: Apache-2.0
// Connector-side process selection accepts verified release artifacts only.

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

export type EngineSelection = ReleaseEngineSelection;

export class EngineSelectionError extends Error {
  readonly code: string;

  constructor(code: string, message: string) {
    super(message);
    this.name = "EngineSelectionError";
    this.code = code;
  }
}

export type InstalledConnectorMetadata = {
  readonly schemaVersion: 1;
  readonly state: "prepared" | "unprepared";
  readonly connectorCommit: string;
  readonly releaseMetadata: Readonly<Record<string, unknown>>;
};

export function selectInstalledEngine(
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
  return {
    mode: "release",
    cacheRoot,
    checkoutRoot: "",
    connectorCommit: installation.connectorCommit,
    metadataPath: "",
    releaseMetadata: Buffer.from(JSON.stringify(installation.releaseMetadata)),
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
  if (mode !== "release") {
    throw new EngineSelectionError(
      "ENGINE_DEVELOPMENT_DISABLED",
      "installed Director runtime accepts release artifacts only",
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
  return {
    mode,
    checkoutRoot: canonicalProspectivePath(checkoutRoot),
    metadataPath: canonicalProspectivePath(join(checkoutRoot, "release", "engine.json")),
    cacheRoot,
  };
}
