// SPDX-License-Identifier: Apache-2.0
// Connector-side distribution verifies and atomically caches the standalone engine.

import { spawnSync } from "node:child_process";
import { createHash, randomUUID } from "node:crypto";
import {
  chmodSync,
  closeSync,
  constants,
  existsSync,
  fsyncSync,
  lstatSync,
  mkdirSync,
  openSync,
  readFileSync,
  readdirSync,
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
import {
  HOST_CONTRACT_SHA256,
  HOST_CONTRACT_VERSION,
} from "../generated/host-contract.shared.ts";

const RELEASE_ORIGIN = "https://github.com";
const RELEASE_PATH_PREFIX = "/mcuadros/paseo-director/releases/download/";
const BINARY_NAME = "director-engine-linux-amd64";
const NOTICES_NAME = "THIRD_PARTY_NOTICES.txt";
const SHA256_PATTERN = /^[0-9a-f]{64}$/u;
const GIT_SHA_PATTERN = /^[0-9a-f]{40}$/u;
const VERSION_PATTERN = /^(?:0|[1-9][0-9]*)\.(?:0|[1-9][0-9]*)\.(?:0|[1-9][0-9]*)(?:-[0-9A-Za-z.-]+)?$/u;
const MAXIMUM_BINARY_BYTES = 256 * 1024 * 1024;
const MAXIMUM_NOTICES_BYTES = 16 * 1024 * 1024;
export const EMPTY_SHA256 =
  "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855";

export type ReleaseAsset = {
  name: string;
  url: string;
  sha256: string;
};

export type PublishedReleaseMetadata = {
  schemaVersion: 2;
  state: "published";
  version: string;
  target: "linux-amd64";
  sourceCandidate: string;
  binary: ReleaseAsset;
  notices: ReleaseAsset;
};

export type UnpublishedReleaseMetadata = {
  schemaVersion: 2;
  state: "unpublished";
  version: string;
  target: "linux-amd64";
};

export type ReleaseMetadata = PublishedReleaseMetadata | UnpublishedReleaseMetadata;

export type EngineBinaryIdentity = {
  name: "director-engine";
  version: string;
  buildMode: "release" | "development";
  sourceCandidate: string;
  target: "linux-amd64";
  executableSha256: string;
  noticesSha256: string;
  contractVersion: string;
  contractSha256: string;
  productBehavior: true;
};

export type ResolvedEngine = {
  mode: "release" | "development";
  version: string;
  sourceCandidate: string;
  target: "linux-amd64";
  binaryPath: string;
  noticesPath: string;
  binarySha256: string;
  noticesSha256: string;
  connectorCommit: string;
  contractVersion: string;
  contractSha256: string;
};

type DevelopmentBuildIdentity = {
  version: "0.0.0-dev";
  sourceCandidate: string;
  noticesSha256: string;
};

export type DistributionDependencies = {
  fetchAsset?: (url: string) => Promise<Uint8Array>;
  compile?: (
    selection: DevelopmentEngineSelection,
    destination: string,
    identity: DevelopmentBuildIdentity,
  ) => void;
  inspectBinary?: (path: string) => EngineBinaryIdentity;
  connectorCommit?: (checkoutRoot: string) => string;
  sourceCandidate?: (sourceRoot: string) => string;
  afterPublish?: (resolved: ResolvedEngine) => void;
};

export class EngineDistributionError extends Error {
  readonly code: string;

  constructor(code: string, message: string) {
    super(message);
    this.name = "EngineDistributionError";
    this.code = code;
  }
}

function sha256(bytes: Uint8Array | string): string {
  return createHash("sha256").update(bytes).digest("hex");
}

function strictKeys(value: Record<string, unknown>, expected: string[]): boolean {
  const keys = Object.keys(value).sort();
  const sorted = [...expected].sort();
  return keys.length === sorted.length && keys.every((key, index) => key === sorted[index]);
}

function releaseAssetURL(version: string, name: string, value: unknown): value is string {
  if (typeof value !== "string") return false;
  try {
    const parsed = new URL(value);
    return (
      parsed.origin === RELEASE_ORIGIN &&
      parsed.username === "" &&
      parsed.password === "" &&
      parsed.pathname === `${RELEASE_PATH_PREFIX}v${version}/${name}` &&
      parsed.search === "" &&
      parsed.hash === ""
    );
  } catch {
    return false;
  }
}

function validateAsset(value: unknown, field: "binary" | "notices", version: string): ReleaseAsset {
  const name = field === "binary" ? BINARY_NAME : NOTICES_NAME;
  if (value === null || typeof value !== "object" || Array.isArray(value)) {
    throw new EngineDistributionError("ENGINE_RELEASE_METADATA", `release ${field} metadata is invalid`);
  }
  const asset = value as Record<string, unknown>;
  if (
    !strictKeys(asset, ["name", "sha256", "url"]) ||
    asset.name !== name ||
    !releaseAssetURL(version, name, asset.url) ||
    typeof asset.sha256 !== "string" ||
    !SHA256_PATTERN.test(asset.sha256) ||
    asset.sha256 === EMPTY_SHA256
  ) {
    throw new EngineDistributionError("ENGINE_RELEASE_METADATA", `release ${field} identity is invalid`);
  }
  return { name, url: asset.url, sha256: asset.sha256 };
}

export function parseReleaseMetadata(bytes: Uint8Array): ReleaseMetadata {
  let value: unknown;
  try {
    value = JSON.parse(Buffer.from(bytes).toString("utf8"));
  } catch {
    throw new EngineDistributionError("ENGINE_RELEASE_METADATA", "release metadata is not valid JSON");
  }
  if (value === null || typeof value !== "object" || Array.isArray(value)) {
    throw new EngineDistributionError("ENGINE_RELEASE_METADATA", "release metadata is invalid");
  }
  const metadata = value as Record<string, unknown>;
  if (
    metadata.schemaVersion !== 2 ||
    metadata.target !== "linux-amd64" ||
    typeof metadata.version !== "string" ||
    (metadata.state !== "published" && metadata.state !== "unpublished")
  ) {
    throw new EngineDistributionError("ENGINE_RELEASE_METADATA", "release metadata fields do not match");
  }
  if (metadata.state === "unpublished") {
    if (!strictKeys(metadata, ["schemaVersion", "state", "target", "version"]) || metadata.version !== "0.0.0-scaffold") {
      throw new EngineDistributionError("ENGINE_RELEASE_METADATA", "unpublished release metadata is invalid");
    }
    return { schemaVersion: 2, state: "unpublished", version: metadata.version, target: "linux-amd64" };
  }
  if (
    !VERSION_PATTERN.test(metadata.version) ||
    !strictKeys(metadata, ["binary", "notices", "schemaVersion", "sourceCandidate", "state", "target", "version"]) ||
    typeof metadata.sourceCandidate !== "string" ||
    !GIT_SHA_PATTERN.test(metadata.sourceCandidate)
  ) {
    throw new EngineDistributionError("ENGINE_RELEASE_METADATA", "published release identity is invalid");
  }
  return {
    schemaVersion: 2,
    state: "published",
    version: metadata.version,
    target: "linux-amd64",
    sourceCandidate: metadata.sourceCandidate,
    binary: validateAsset(metadata.binary, "binary", metadata.version),
    notices: validateAsset(metadata.notices, "notices", metadata.version),
  };
}

function readRegularFile(path: string, code: string): Buffer {
  let status;
  try {
    status = lstatSync(path);
  } catch {
    throw new EngineDistributionError(code, "engine distribution file is unavailable");
  }
  if (!status.isFile() || status.isSymbolicLink()) {
    throw new EngineDistributionError(code, "engine distribution file must be regular");
  }
  return readFileSync(path);
}

function readPrivateCacheFile(path: string): Buffer {
  let status;
  try {
    status = lstatSync(path);
  } catch {
    throw new EngineDistributionError("ENGINE_CACHE_POISONED", "cached engine file is unavailable");
  }
  const expectedUID = process.geteuid?.();
  if (
    !status.isFile() || status.isSymbolicLink() || (status.mode & 0o077) !== 0 ||
    (expectedUID !== undefined && status.uid !== expectedUID)
  ) {
    throw new EngineDistributionError("ENGINE_CACHE_POISONED", "cached engine file is unsafe");
  }
  return readFileSync(path);
}

function releaseRoot(selection: ReleaseEngineSelection, metadata: PublishedReleaseMetadata): string {
  return join(selection.cacheRoot, "release", metadata.version, metadata.target, metadata.binary.sha256);
}

function releaseEnginePaths(selection: ReleaseEngineSelection, metadata: ReleaseMetadata): string[] {
  const base = join(selection.cacheRoot, "release", metadata.version, metadata.target);
  if (metadata.state === "unpublished") {
    return [selection.checkoutRoot, selection.cacheRoot, selection.metadataPath, base];
  }
  const root = releaseRoot(selection, metadata);
  return [selection.checkoutRoot, selection.cacheRoot, selection.metadataPath, base, root, join(root, "director-engine"), join(root, NOTICES_NAME)];
}

export function engineBoundaryPaths(selection: EngineSelection): string[] {
  if (selection.mode === "release") {
    return releaseEnginePaths(selection, parseReleaseMetadata(readRegularFile(selection.metadataPath, "ENGINE_RELEASE_METADATA")));
  }
  const paths = developmentEnginePaths(selection);
  return [selection.checkoutRoot, selection.sourceRoot, selection.cacheRoot, selection.moduleCache, paths.binaryPath, paths.temporaryBinaryPath, paths.goCache];
}

function ensurePrivateDirectory(path: string): void {
  try {
    mkdirSync(path, { recursive: true, mode: 0o700 });
  } catch {
    throw new EngineDistributionError("ENGINE_CACHE_PERMISSIONS", "engine cache directory cannot be created safely");
  }
  const status = lstatSync(path);
  const expectedUID = process.geteuid?.();
  if (
    !status.isDirectory() ||
    status.isSymbolicLink() ||
    (status.mode & 0o077) !== 0 ||
    (expectedUID !== undefined && status.uid !== expectedUID)
  ) {
    throw new EngineDistributionError("ENGINE_CACHE_PERMISSIONS", "engine cache directory is not private and owned");
  }
}

function writeVerifiedFile(path: string, bytes: Uint8Array, expected: string, mode: number): void {
  if (sha256(bytes) !== expected) {
    throw new EngineDistributionError("ENGINE_RELEASE_DIGEST", "release asset digest does not match");
  }
  const descriptor = openSync(
    path,
    constants.O_WRONLY | constants.O_CREAT | constants.O_EXCL | constants.O_NOFOLLOW,
    0o600,
  );
  try {
    writeFileSync(descriptor, bytes);
    fsyncSync(descriptor);
  } finally {
    closeSync(descriptor);
  }
  chmodSync(path, mode);
}

function fsyncDirectory(path: string): void {
  const descriptor = openSync(path, constants.O_RDONLY | constants.O_DIRECTORY | constants.O_NOFOLLOW);
  try {
    fsyncSync(descriptor);
  } finally {
    closeSync(descriptor);
  }
}

function gitCommit(path: string): string {
  const result = spawnSync("git", ["-C", path, "rev-parse", "HEAD"], {
    encoding: "utf8",
    env: process.env.PATH ? { PATH: process.env.PATH, GIT_CONFIG_NOSYSTEM: "1" } : { GIT_CONFIG_NOSYSTEM: "1" },
    shell: false,
    timeout: 10_000,
    maxBuffer: 16 * 1024,
  });
  const commit = result.stdout?.trim() ?? "";
  if (result.status !== 0 || (result.error && result.status === null) || !GIT_SHA_PATTERN.test(commit)) {
    throw new EngineDistributionError("ENGINE_SOURCE_IDENTITY", "exact Git source identity is unavailable");
  }
  const status = spawnSync("git", ["-C", path, "status", "--porcelain"], {
    encoding: "utf8",
    env: process.env.PATH ? { PATH: process.env.PATH, GIT_CONFIG_NOSYSTEM: "1" } : { GIT_CONFIG_NOSYSTEM: "1" },
    shell: false,
    timeout: 10_000,
    maxBuffer: 64 * 1024,
  });
  if (status.status !== 0 || (status.error && status.status === null) || status.stdout !== "") {
    throw new EngineDistributionError("ENGINE_SOURCE_IDENTITY", "Git source has tracked changes");
  }
  return commit;
}

function defaultInspectBinary(path: string): EngineBinaryIdentity {
  const result = spawnSync(path, ["version"], {
    encoding: "utf8",
    env: {},
    shell: false,
    timeout: 10_000,
    maxBuffer: 64 * 1024,
  });
  if (result.status !== 0 || (result.error && result.status === null)) {
    throw new EngineDistributionError("ENGINE_IDENTITY_MISMATCH", "engine identity probe failed");
  }
  try {
    return JSON.parse(result.stdout) as EngineBinaryIdentity;
  } catch {
    throw new EngineDistributionError("ENGINE_IDENTITY_MISMATCH", "engine identity is not valid JSON");
  }
}

function verifyIdentity(identity: EngineBinaryIdentity, expected: {
  mode: "release" | "development";
  version: string;
  sourceCandidate: string;
  noticesSha256: string;
  binarySha256: string;
}): void {
  if (
    identity.name !== "director-engine" ||
    identity.buildMode !== expected.mode ||
    identity.version !== expected.version ||
    identity.sourceCandidate !== expected.sourceCandidate ||
    identity.target !== "linux-amd64" ||
    identity.executableSha256 !== expected.binarySha256 ||
    identity.noticesSha256 !== expected.noticesSha256 ||
    identity.contractSha256 !== HOST_CONTRACT_SHA256 ||
    identity.contractVersion !== HOST_CONTRACT_VERSION ||
    identity.productBehavior !== true
  ) {
    throw new EngineDistributionError("ENGINE_IDENTITY_MISMATCH", "engine identity does not match the installed pin");
  }
}

function verifiedResolved(options: {
  mode: "release" | "development";
  version: string;
  sourceCandidate: string;
  binaryPath: string;
  noticesPath: string;
  connectorCommit: string;
  inspectBinary: (path: string) => EngineBinaryIdentity;
  expectedBinarySha256?: string;
  expectedNoticesSha256?: string;
  digestMismatchCode?: string;
}): ResolvedEngine {
  const binary = readPrivateCacheFile(options.binaryPath);
  const notices = readPrivateCacheFile(options.noticesPath);
  const binarySha256 = sha256(binary);
  const noticesSha256 = sha256(notices);
  if (
    (options.expectedBinarySha256 && binarySha256 !== options.expectedBinarySha256) ||
    (options.expectedNoticesSha256 && noticesSha256 !== options.expectedNoticesSha256)
  ) {
    throw new EngineDistributionError(
      options.digestMismatchCode ?? "ENGINE_RELEASE_DIGEST",
      "engine distribution digests do not match",
    );
  }
  if (
    options.mode === "release" &&
    !notices.toString("utf8").split("\n").includes(`source-candidate: ${options.sourceCandidate}`)
  ) {
    throw new EngineDistributionError("ENGINE_NOTICES_IDENTITY", "release notices do not identify the pinned source Candidate");
  }
  const identity = options.inspectBinary(options.binaryPath);
  verifyIdentity(identity, {
    mode: options.mode,
    version: options.version,
    sourceCandidate: options.sourceCandidate,
    noticesSha256,
    binarySha256,
  });
  return {
    mode: options.mode,
    version: options.version,
    sourceCandidate: options.sourceCandidate,
    target: "linux-amd64",
    binaryPath: options.binaryPath,
    noticesPath: options.noticesPath,
    binarySha256,
    noticesSha256,
    connectorCommit: options.connectorCommit,
    contractVersion: identity.contractVersion,
    contractSha256: identity.contractSha256,
  };
}

async function defaultFetchAsset(url: string): Promise<Uint8Array> {
  const response = await fetch(url, { redirect: "follow" });
  const finalURL = new URL(response.url);
  if (
    finalURL.protocol !== "https:" ||
    !["github.com", "release-assets.githubusercontent.com"].includes(finalURL.hostname) ||
    finalURL.username !== "" || finalURL.password !== ""
  ) {
    throw new EngineDistributionError("ENGINE_RELEASE_FETCH", "release redirect left the GitHub asset boundary");
  }
  if (!response.ok) {
    throw new EngineDistributionError("ENGINE_RELEASE_FETCH", `release server returned ${response.status}`);
  }
  const contentLength = Number(response.headers.get("content-length"));
  if (Number.isFinite(contentLength) && contentLength > MAXIMUM_BINARY_BYTES) {
    throw new EngineDistributionError("ENGINE_RELEASE_SIZE", "release asset exceeds its bounded size");
  }
  const bytes = new Uint8Array(await response.arrayBuffer());
  if (bytes.byteLength > MAXIMUM_BINARY_BYTES) {
    throw new EngineDistributionError("ENGINE_RELEASE_SIZE", "release asset exceeds its bounded size");
  }
  return bytes;
}

function processStartTime(pid: number): string {
  try {
    const stat = readFileSync(`/proc/${pid}/stat`, "utf8");
    return stat.slice(stat.lastIndexOf(")") + 2).split(" ")[19] ?? "";
  } catch {
    return "";
  }
}

function cleanStalePartials(base: string, prefix: string, binding: string): void {
  for (const entry of readdirSync(base, { withFileTypes: true })) {
    if (!entry.name.startsWith(prefix)) continue;
    const path = join(base, entry.name);
    if (!entry.isDirectory() || entry.isSymbolicLink()) {
      throw new EngineDistributionError("ENGINE_CACHE_POISONED", "engine staging path is unsafe");
    }
    let marker: { schemaVersion?: number; binding?: string; pid?: number; processStart?: string };
    try {
      marker = JSON.parse(readRegularFile(join(path, ".owner.json"), "ENGINE_CACHE_POISONED").toString("utf8"));
    } catch {
      throw new EngineDistributionError("ENGINE_CACHE_POISONED", "engine staging ownership is invalid");
    }
    if (marker.schemaVersion !== 1 || marker.binding !== binding || !Number.isSafeInteger(marker.pid) || typeof marker.processStart !== "string") {
      throw new EngineDistributionError("ENGINE_CACHE_POISONED", "engine staging ownership does not match");
    }
    if (processStartTime(marker.pid!) === marker.processStart && marker.processStart !== "") continue;
    rmSync(path, { recursive: true, force: false });
  }
}

async function resolveRelease(selection: ReleaseEngineSelection, dependencies: DistributionDependencies): Promise<ResolvedEngine> {
  const metadata = parseReleaseMetadata(readRegularFile(selection.metadataPath, "ENGINE_RELEASE_METADATA"));
  if (metadata.state === "unpublished") {
    throw new EngineDistributionError("ENGINE_RELEASE_UNPUBLISHED", "the scaffold release is explicitly unpublished");
  }
  if (pathIsWithin(selection.cacheRoot, selection.checkoutRoot)) {
    throw new EngineDistributionError("ENGINE_CACHE_IN_CHECKOUT", "release cache must be outside the plugin checkout");
  }
  const connectorCommit = (dependencies.connectorCommit ?? gitCommit)(selection.checkoutRoot);
  const inspectBinary = dependencies.inspectBinary ?? defaultInspectBinary;
  const root = releaseRoot(selection, metadata);
  const binaryPath = join(root, "director-engine");
  const noticesPath = join(root, NOTICES_NAME);
  if (existsSync(root)) {
    ensurePrivateDirectory(root);
    const resolved = verifiedResolved({ mode: "release", version: metadata.version, sourceCandidate: metadata.sourceCandidate, binaryPath, noticesPath, connectorCommit, inspectBinary, expectedBinarySha256: metadata.binary.sha256, expectedNoticesSha256: metadata.notices.sha256, digestMismatchCode: "ENGINE_CACHE_POISONED" });
    if (resolved.binarySha256 !== metadata.binary.sha256 || resolved.noticesSha256 !== metadata.notices.sha256) {
      throw new EngineDistributionError("ENGINE_CACHE_POISONED", "cached engine digests do not match the installed pin");
    }
    return resolved;
  }

  const base = dirname(root);
  for (const directory of [selection.cacheRoot, join(selection.cacheRoot, "release"), join(selection.cacheRoot, "release", metadata.version), base]) {
    ensurePrivateDirectory(directory);
  }
  const binding = sha256(JSON.stringify(metadata));
  const prefix = `.partial-${metadata.binary.sha256}-`;
  cleanStalePartials(base, prefix, binding);
  const temporaryRoot = join(base, `${prefix}${randomUUID()}`);
  mkdirSync(temporaryRoot, { mode: 0o700 });
  const marker = Buffer.from(`${JSON.stringify({ schemaVersion: 1, binding, pid: process.pid, processStart: processStartTime(process.pid) })}\n`);
  writeVerifiedFile(join(temporaryRoot, ".owner.json"), marker, sha256(marker), 0o600);
  const temporaryBinary = join(temporaryRoot, "director-engine");
  const temporaryNotices = join(temporaryRoot, NOTICES_NAME);
  try {
    const fetchAsset = dependencies.fetchAsset ?? defaultFetchAsset;
    const binary = await fetchAsset(metadata.binary.url);
    if (binary.byteLength > MAXIMUM_BINARY_BYTES) {
      throw new EngineDistributionError("ENGINE_RELEASE_SIZE", "release binary exceeds its bounded size");
    }
    writeVerifiedFile(temporaryBinary, binary, metadata.binary.sha256, 0o500);
    const notices = await fetchAsset(metadata.notices.url);
    if (notices.byteLength > MAXIMUM_NOTICES_BYTES) {
      throw new EngineDistributionError("ENGINE_RELEASE_SIZE", "release notices exceed their bounded size");
    }
    writeVerifiedFile(temporaryNotices, notices, metadata.notices.sha256, 0o400);
    const staged = verifiedResolved({ mode: "release", version: metadata.version, sourceCandidate: metadata.sourceCandidate, binaryPath: temporaryBinary, noticesPath: temporaryNotices, connectorCommit, inspectBinary, expectedBinarySha256: metadata.binary.sha256, expectedNoticesSha256: metadata.notices.sha256 });
    if (staged.binarySha256 !== metadata.binary.sha256 || staged.noticesSha256 !== metadata.notices.sha256) {
      throw new EngineDistributionError("ENGINE_RELEASE_DIGEST", "staged engine digests changed");
    }
    rmSync(join(temporaryRoot, ".owner.json"));
    fsyncDirectory(temporaryRoot);
    try {
      renameSync(temporaryRoot, root);
      fsyncDirectory(base);
    } catch (error) {
      if (!existsSync(root)) throw error;
    }
    const resolved = verifiedResolved({ mode: "release", version: metadata.version, sourceCandidate: metadata.sourceCandidate, binaryPath, noticesPath, connectorCommit, inspectBinary, expectedBinarySha256: metadata.binary.sha256, expectedNoticesSha256: metadata.notices.sha256, digestMismatchCode: "ENGINE_CACHE_POISONED" });
    if (resolved.binarySha256 !== metadata.binary.sha256 || resolved.noticesSha256 !== metadata.notices.sha256) {
      throw new EngineDistributionError("ENGINE_CACHE_POISONED", "published engine cache does not match the installed pin");
    }
    dependencies.afterPublish?.(resolved);
    return resolved;
  } finally {
    if (existsSync(temporaryRoot)) rmSync(temporaryRoot, { recursive: true, force: true });
  }
}

function defaultCompile(selection: DevelopmentEngineSelection, destination: string, identity: DevelopmentBuildIdentity): void {
  const { goCache } = developmentEnginePaths(selection);
  ensurePrivateDirectory(goCache);
  const result = spawnSync("go", [
    "build", "-trimpath", "-buildvcs=false", "-ldflags",
    `-X main.buildMode=development -X main.version=${identity.version} -X main.sourceCandidate=${identity.sourceCandidate} -X main.noticesSha=${identity.noticesSha256}`,
    "-o", destination, "./cmd/director-engine",
  ], {
    cwd: selection.sourceRoot,
    encoding: "utf8",
    env: engineProcessEnvironment(process.env, goCache, selection.moduleCache),
  });
  if (result.status !== 0 || (result.error && result.status === null)) {
    throw new EngineDistributionError("ENGINE_DEVELOPMENT_BUILD", "development engine compilation failed");
  }
  chmodSync(destination, 0o500);
}

function resolveDevelopment(selection: DevelopmentEngineSelection, dependencies: DistributionDependencies): ResolvedEngine {
  if (pathIsWithin(selection.cacheRoot, selection.checkoutRoot)) {
    throw new EngineDistributionError("ENGINE_CACHE_IN_CHECKOUT", "development cache must be outside the plugin checkout");
  }
  const connectorCommit = (dependencies.connectorCommit ?? gitCommit)(selection.checkoutRoot);
  const sourceCandidate = (dependencies.sourceCandidate ?? gitCommit)(selection.sourceRoot);
  if (!GIT_SHA_PATTERN.test(sourceCandidate)) {
    throw new EngineDistributionError("ENGINE_SOURCE_IDENTITY", "development source Candidate is invalid");
  }
  const paths = developmentEnginePaths(selection, sourceCandidate);
  const finalRoot = dirname(paths.binaryPath);
  const sourceCacheRoot = dirname(finalRoot);
  for (const directory of [selection.cacheRoot, join(selection.cacheRoot, "development"), sourceCacheRoot]) {
    ensurePrivateDirectory(directory);
  }
  const noticesPath = join(finalRoot, NOTICES_NAME);
  const identity: DevelopmentBuildIdentity = { version: "0.0.0-dev", sourceCandidate, noticesSha256: EMPTY_SHA256 };
  const inspectBinary = dependencies.inspectBinary ?? defaultInspectBinary;
  if (existsSync(finalRoot)) {
    ensurePrivateDirectory(finalRoot);
    return verifiedResolved({ mode: "development", version: identity.version, sourceCandidate, binaryPath: paths.binaryPath, noticesPath, connectorCommit, inspectBinary });
  }
  const temporaryRoot = join(sourceCacheRoot, `.partial-${sourceCandidate}-${randomUUID()}`);
  mkdirSync(temporaryRoot, { mode: 0o700 });
  const temporary = join(temporaryRoot, "director-engine");
  const temporaryNotices = join(temporaryRoot, NOTICES_NAME);
  try {
    (dependencies.compile ?? defaultCompile)(selection, temporary, identity);
    writeVerifiedFile(temporaryNotices, new Uint8Array(), EMPTY_SHA256, 0o400);
    verifiedResolved({ mode: "development", version: identity.version, sourceCandidate, binaryPath: temporary, noticesPath: temporaryNotices, connectorCommit, inspectBinary });
    fsyncDirectory(temporaryRoot);
    try {
      renameSync(temporaryRoot, finalRoot);
      fsyncDirectory(sourceCacheRoot);
    } catch (error) {
      if (!existsSync(finalRoot)) throw error;
    }
    const resolved = verifiedResolved({ mode: "development", version: identity.version, sourceCandidate, binaryPath: paths.binaryPath, noticesPath, connectorCommit, inspectBinary });
    dependencies.afterPublish?.(resolved);
    return resolved;
  } finally {
    if (existsSync(temporaryRoot)) rmSync(temporaryRoot, { recursive: true, force: true });
  }
}

export async function resolveEngine(selection: EngineSelection, dependencies: DistributionDependencies = {}): Promise<ResolvedEngine> {
  try {
    return selection.mode === "release"
      ? await resolveRelease(selection, dependencies)
      : resolveDevelopment(selection, dependencies);
  } catch (error) {
    if (error instanceof EngineDistributionError) throw error;
    throw new EngineDistributionError("ENGINE_DISTRIBUTION_IO", "engine distribution I/O failed");
  }
}
