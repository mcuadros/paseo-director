// SPDX-License-Identifier: Apache-2.0

import { spawnSync } from "node:child_process";
import { createHash } from "node:crypto";
import {
  chmodSync,
  closeSync,
  existsSync,
  fsyncSync,
  mkdirSync,
  openSync,
  readFileSync,
  renameSync,
  rmSync,
  writeFileSync,
} from "node:fs";
import { dirname, join } from "node:path";

import {
  developmentEnginePaths,
  engineProcessEnvironment,
  pathIsWithin,
  type DevelopmentEngineSelection,
  type EngineSelection,
  type ReleaseEngineSelection,
} from "./engine-selection.server.ts";

const RELEASE_URL_PREFIX =
  "https://github.com/mcuadros/paseo-director/releases/download/";
export const EMPTY_SHA256 =
  "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855";

export type ReleaseAsset = {
  url: string;
  sha256: string;
};

export type PublishedReleaseMetadata = {
  schemaVersion: 1;
  state: "published";
  version: string;
  target: "linux-amd64";
  binary: ReleaseAsset;
  notices: ReleaseAsset;
};

export type UnpublishedReleaseMetadata = {
  schemaVersion: 1;
  state: "unpublished";
  version: string;
  target: "linux-amd64";
};

export type ReleaseMetadata =
  | PublishedReleaseMetadata
  | UnpublishedReleaseMetadata;

export type ResolvedEngine = {
  mode: "release" | "development";
  version: string;
  target: "linux-amd64";
  binaryPath: string;
  noticesPath?: string;
  binarySha256?: string;
  noticesSha256?: string;
};

export type DistributionDependencies = {
  fetchAsset?: (url: string) => Promise<Uint8Array>;
  compile?: (
    selection: DevelopmentEngineSelection,
    destination: string,
  ) => void;
};

export class EngineDistributionError extends Error {
  readonly code: string;

  constructor(code: string, message: string) {
    super(message);
    this.name = "EngineDistributionError";
    this.code = code;
  }
}

function sha256(bytes: Uint8Array): string {
  return createHash("sha256").update(bytes).digest("hex");
}

function validateSegment(value: unknown, field: string): string {
  if (
    typeof value !== "string" ||
    value === "." ||
    value === ".." ||
    !/^[A-Za-z0-9._-]+$/.test(value)
  ) {
    throw new EngineDistributionError(
      "ENGINE_RELEASE_METADATA",
      `release ${field} is invalid`,
    );
  }
  return value;
}

function validateAsset(value: unknown, field: string): ReleaseAsset {
  if (value === null || typeof value !== "object" || Array.isArray(value)) {
    throw new EngineDistributionError(
      "ENGINE_RELEASE_METADATA",
      `release ${field} metadata is invalid`,
    );
  }
  const asset = value as Record<string, unknown>;
  const keys = Object.keys(asset).sort();
  if (
    !isDeepStrictAssetKeys(keys) ||
    typeof asset.url !== "string" ||
    !asset.url.startsWith(RELEASE_URL_PREFIX) ||
    typeof asset.sha256 !== "string" ||
    !/^[0-9a-f]{64}$/.test(asset.sha256) ||
    asset.sha256 === EMPTY_SHA256
  ) {
    throw new EngineDistributionError(
      "ENGINE_RELEASE_METADATA",
      `release ${field} identity is invalid`,
    );
  }
  return { url: asset.url, sha256: asset.sha256 };
}

function isDeepStrictAssetKeys(keys: string[]): boolean {
  return keys.length === 2 && keys[0] === "sha256" && keys[1] === "url";
}

export function parseReleaseMetadata(bytes: Uint8Array): ReleaseMetadata {
  let value: unknown;
  try {
    value = JSON.parse(Buffer.from(bytes).toString("utf8"));
  } catch {
    throw new EngineDistributionError(
      "ENGINE_RELEASE_METADATA",
      "release metadata is not valid JSON",
    );
  }
  if (value === null || typeof value !== "object" || Array.isArray(value)) {
    throw new EngineDistributionError(
      "ENGINE_RELEASE_METADATA",
      "release metadata is invalid",
    );
  }
  const metadata = value as Record<string, unknown>;
  const keys = Object.keys(metadata).sort();
  const commonValid =
    metadata.schemaVersion === 1 &&
    metadata.target === "linux-amd64" &&
    (metadata.state === "published" || metadata.state === "unpublished");
  if (!commonValid) {
    throw new EngineDistributionError(
      "ENGINE_RELEASE_METADATA",
      "release metadata fields do not match",
    );
  }
  const version = validateSegment(metadata.version, "version");
  if (metadata.state === "unpublished") {
    const expectedKeys = ["schemaVersion", "state", "target", "version"];
    if (
      keys.length !== expectedKeys.length ||
      keys.some((key, index) => key !== expectedKeys[index])
    ) {
      throw new EngineDistributionError(
        "ENGINE_RELEASE_METADATA",
        "unpublished release metadata must not declare assets",
      );
    }
    return {
      schemaVersion: 1,
      state: "unpublished",
      version,
      target: "linux-amd64",
    };
  }
  const expectedKeys = [
    "binary",
    "notices",
    "schemaVersion",
    "state",
    "target",
    "version",
  ];
  if (
    keys.length !== expectedKeys.length ||
    keys.some((key, index) => key !== expectedKeys[index])
  ) {
    throw new EngineDistributionError(
      "ENGINE_RELEASE_METADATA",
      "release metadata fields do not match",
    );
  }
  return {
    schemaVersion: 1,
    state: "published",
    version,
    target: "linux-amd64",
    binary: validateAsset(metadata.binary, "binary"),
    notices: validateAsset(metadata.notices, "notices"),
  };
}

function releaseEnginePaths(
  selection: ReleaseEngineSelection,
  metadata: ReleaseMetadata,
): string[] {
  const releaseBase = join(
    selection.cacheRoot,
    "release",
    metadata.version,
    metadata.target,
  );
  if (metadata.state === "unpublished") {
    return [
      selection.checkoutRoot,
      selection.cacheRoot,
      selection.metadataPath,
      releaseBase,
    ];
  }
  const releaseRoot = join(releaseBase, metadata.binary.sha256);
  return [
    selection.checkoutRoot,
    selection.cacheRoot,
    selection.metadataPath,
    releaseBase,
    releaseRoot,
    join(releaseRoot, "director-engine"),
    join(releaseRoot, "THIRD_PARTY_NOTICES.txt"),
  ];
}

export function engineBoundaryPaths(selection: EngineSelection): string[] {
  if (selection.mode === "release") {
    const metadata = parseReleaseMetadata(readFileSync(selection.metadataPath));
    return releaseEnginePaths(selection, metadata);
  }
  const paths = developmentEnginePaths(selection);
  return [
    selection.checkoutRoot,
    selection.sourceRoot,
    selection.cacheRoot,
    paths.binaryPath,
    paths.temporaryBinaryPath,
    paths.goCache,
  ];
}

async function defaultFetchAsset(url: string): Promise<Uint8Array> {
  const response = await fetch(url);
  if (!response.ok) {
    throw new EngineDistributionError(
      "ENGINE_RELEASE_FETCH",
      `release server returned ${response.status}`,
    );
  }
  return new Uint8Array(await response.arrayBuffer());
}

async function cacheAsset(options: {
  asset: ReleaseAsset;
  destination: string;
  mode: number;
  fetchAsset: (url: string) => Promise<Uint8Array>;
}): Promise<void> {
  if (existsSync(options.destination)) {
    if (sha256(readFileSync(options.destination)) === options.asset.sha256) {
      return;
    }
    rmSync(options.destination, { force: true });
  }
  const bytes = await options.fetchAsset(options.asset.url);
  if (sha256(bytes) !== options.asset.sha256) {
    throw new EngineDistributionError(
      "ENGINE_RELEASE_DIGEST",
      "release asset digest does not match",
    );
  }
  mkdirSync(dirname(options.destination), { recursive: true, mode: 0o700 });
  const temporary = `${options.destination}.partial-${process.pid}`;
  try {
    writeFileSync(temporary, bytes, { mode: 0o600 });
    const descriptor = openSync(temporary, "r");
    fsyncSync(descriptor);
    closeSync(descriptor);
    renameSync(temporary, options.destination);
    chmodSync(options.destination, options.mode);
  } finally {
    rmSync(temporary, { force: true });
  }
}

async function resolveRelease(
  selection: ReleaseEngineSelection,
  dependencies: DistributionDependencies,
): Promise<ResolvedEngine> {
  const metadata = parseReleaseMetadata(readFileSync(selection.metadataPath));
  if (metadata.state === "unpublished") {
    throw new EngineDistributionError(
      "ENGINE_RELEASE_UNPUBLISHED",
      "the scaffold release is explicitly unpublished",
    );
  }
  if (pathIsWithin(selection.cacheRoot, selection.checkoutRoot)) {
    throw new EngineDistributionError(
      "ENGINE_CACHE_IN_CHECKOUT",
      "release cache must be outside the plugin checkout",
    );
  }
  const releaseRoot = join(
    selection.cacheRoot,
    "release",
    metadata.version,
    metadata.target,
    metadata.binary.sha256,
  );
  const binaryPath = join(releaseRoot, "director-engine");
  const noticesPath = join(releaseRoot, "THIRD_PARTY_NOTICES.txt");
  const fetchAsset = dependencies.fetchAsset ?? defaultFetchAsset;
  await cacheAsset({
    asset: metadata.binary,
    destination: binaryPath,
    mode: 0o500,
    fetchAsset,
  });
  await cacheAsset({
    asset: metadata.notices,
    destination: noticesPath,
    mode: 0o400,
    fetchAsset,
  });
  return {
    mode: "release",
    version: metadata.version,
    target: metadata.target,
    binaryPath,
    noticesPath,
    binarySha256: metadata.binary.sha256,
    noticesSha256: metadata.notices.sha256,
  };
}

function defaultCompile(
  selection: DevelopmentEngineSelection,
  destination: string,
): void {
  mkdirSync(dirname(destination), { recursive: true, mode: 0o700 });
  const { goCache, temporaryBinaryPath } = developmentEnginePaths(selection);
  mkdirSync(goCache, { recursive: true, mode: 0o700 });
  const result = spawnSync(
    "go",
    [
      "build",
      "-trimpath",
      "-buildvcs=false",
      "-ldflags",
      "-X main.buildMode=development -X main.version=0.0.0-dev -X main.sourceCandidate=development",
      "-o",
      temporaryBinaryPath,
      "./cmd/director-engine",
    ],
    {
      cwd: selection.sourceRoot,
      encoding: "utf8",
      env: engineProcessEnvironment(process.env, goCache),
    },
  );
  if (result.status !== 0) {
    rmSync(temporaryBinaryPath, { force: true });
    throw new EngineDistributionError(
      "ENGINE_DEVELOPMENT_BUILD",
      "development engine compilation failed",
    );
  }
  renameSync(temporaryBinaryPath, destination);
  chmodSync(destination, 0o500);
}

function resolveDevelopment(
  selection: DevelopmentEngineSelection,
  dependencies: DistributionDependencies,
): ResolvedEngine {
  if (pathIsWithin(selection.cacheRoot, selection.checkoutRoot)) {
    throw new EngineDistributionError(
      "ENGINE_CACHE_IN_CHECKOUT",
      "development cache must be outside the plugin checkout",
    );
  }
  const { binaryPath } = developmentEnginePaths(selection);
  const compile = dependencies.compile ?? defaultCompile;
  compile(selection, binaryPath);
  return {
    mode: "development",
    version: "0.0.0-dev",
    target: "linux-amd64",
    binaryPath,
  };
}

export async function resolveEngine(
  selection: EngineSelection,
  dependencies: DistributionDependencies = {},
): Promise<ResolvedEngine> {
  if (selection.mode === "release") {
    return resolveRelease(selection, dependencies);
  }
  return resolveDevelopment(selection, dependencies);
}
