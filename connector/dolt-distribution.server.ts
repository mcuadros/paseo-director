// SPDX-License-Identifier: Apache-2.0
// Release-only Dolt distribution verifies a canonical archive and extracts one executable.

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
  renameSync,
  rmSync,
  writeFileSync,
} from "node:fs";
import { gunzipSync } from "node:zlib";
import { dirname, isAbsolute, join } from "node:path";

import type { PublishedReleaseMetadata } from "./engine-distribution.server.ts";

const MAXIMUM_ARCHIVE_BYTES = 512 * 1024 * 1024;
const MAXIMUM_EXECUTABLE_BYTES = 256 * 1024 * 1024;
const SHA256_PATTERN = /^[0-9a-f]{64}$/u;

export type ResolvedDolt = {
  version: string;
  target: "linux-amd64";
  binaryPath: string;
  binarySha256: string;
  archiveSha256: string;
};

export type DoltDistributionDependencies = {
  fetchAsset?: (url: string) => Promise<Uint8Array>;
  inspectBinary?: (path: string) => string;
  afterPublish?: (resolved: ResolvedDolt) => void;
};

export class DoltDistributionError extends Error {
  readonly code: string;

  constructor(code: string, message: string) {
    super(message);
    this.name = "DoltDistributionError";
    this.code = code;
  }
}

function sha256(bytes: Uint8Array): string {
  return createHash("sha256").update(bytes).digest("hex");
}

function ensurePrivateDirectory(path: string): void {
  mkdirSync(path, { recursive: true, mode: 0o700 });
  const status = lstatSync(path);
  const uid = process.geteuid?.();
  if (
    !status.isDirectory() || status.isSymbolicLink() ||
    (status.mode & 0o077) !== 0 || (uid !== undefined && status.uid !== uid)
  ) {
    throw new DoltDistributionError("DOLT_CACHE_PERMISSIONS", "Dolt cache directory is not private and owned");
  }
}

function fsyncDirectory(path: string): void {
  const descriptor = openSync(path, constants.O_RDONLY | constants.O_DIRECTORY | constants.O_NOFOLLOW);
  try {
    fsyncSync(descriptor);
  } finally {
    closeSync(descriptor);
  }
}

function readPrivateBinary(path: string): Buffer {
  let status;
  try {
    status = lstatSync(path);
  } catch {
    throw new DoltDistributionError("DOLT_CACHE_POISONED", "cached Dolt executable is unavailable");
  }
  const uid = process.geteuid?.();
  if (
    !status.isFile() || status.isSymbolicLink() ||
    (status.mode & 0o077) !== 0 || (uid !== undefined && status.uid !== uid) ||
    status.size <= 0 || status.size > MAXIMUM_EXECUTABLE_BYTES
  ) {
    throw new DoltDistributionError("DOLT_CACHE_POISONED", "cached Dolt executable is unsafe");
  }
  return readFileSync(path);
}

function tarString(bytes: Uint8Array): string {
  const zero = bytes.indexOf(0);
  return Buffer.from(zero === -1 ? bytes : bytes.subarray(0, zero)).toString("utf8");
}

function tarSize(bytes: Uint8Array): number {
  const value = tarString(bytes).trim().replace(/^0+/u, "") || "0";
  if (!/^[0-7]+$/u.test(value)) {
    throw new DoltDistributionError("DOLT_RELEASE_ARCHIVE", "Dolt archive contains an invalid size");
  }
  const size = Number.parseInt(value, 8);
  if (!Number.isSafeInteger(size) || size < 0 || size > MAXIMUM_EXECUTABLE_BYTES) {
    throw new DoltDistributionError("DOLT_RELEASE_SIZE", "Dolt archive entry exceeds its bounded size");
  }
  return size;
}

function safeTarPath(path: string): boolean {
  return path.length > 0 && path.length <= 512 && !isAbsolute(path) &&
    !path.includes("\\") && path.split("/").every((part) => part !== "" && part !== "." && part !== "..");
}

export function extractDoltExecutable(archive: Uint8Array): Uint8Array {
  let tar: Uint8Array;
  try {
    tar = gunzipSync(archive, { maxOutputLength: MAXIMUM_ARCHIVE_BYTES });
  } catch {
    throw new DoltDistributionError("DOLT_RELEASE_ARCHIVE", "Dolt release archive is not valid gzip data");
  }
  let offset = 0;
  let executable: Uint8Array | null = null;
  while (offset + 512 <= tar.byteLength) {
    const header = tar.subarray(offset, offset + 512);
    if (header.every((byte) => byte === 0)) break;
    const name = tarString(header.subarray(0, 100));
    const prefix = tarString(header.subarray(345, 500));
    const path = prefix ? `${prefix}/${name}` : name;
    const type = String.fromCharCode(header[156] ?? 0);
    const inspectedPath = type === "5" && path.endsWith("/") ? path.slice(0, -1) : path;
    if (!safeTarPath(inspectedPath)) {
      throw new DoltDistributionError("DOLT_RELEASE_ARCHIVE", "Dolt archive contains an unsafe path");
    }
    const size = tarSize(header.subarray(124, 136));
    const contentStart = offset + 512;
    const contentEnd = contentStart + size;
    if (contentEnd > tar.byteLength) {
      throw new DoltDistributionError("DOLT_RELEASE_ARCHIVE", "Dolt archive is truncated");
    }
    if (type !== "\0" && type !== "0" && type !== "5") {
      throw new DoltDistributionError("DOLT_RELEASE_ARCHIVE", "Dolt archive contains a link or unsupported entry");
    }
    if ((type === "\0" || type === "0") && inspectedPath.endsWith("/bin/dolt")) {
      if (executable !== null || size === 0) {
        throw new DoltDistributionError("DOLT_RELEASE_ARCHIVE", "Dolt archive executable identity is ambiguous");
      }
      executable = tar.slice(contentStart, contentEnd);
    }
    offset = contentStart + Math.ceil(size / 512) * 512;
  }
  if (executable === null) {
    throw new DoltDistributionError("DOLT_RELEASE_ARCHIVE", "Dolt archive lacks its expected executable");
  }
  return executable;
}

async function defaultFetchAsset(url: string): Promise<Uint8Array> {
  const response = await fetch(url, { redirect: "follow" });
  const finalURL = new URL(response.url);
  if (
    finalURL.protocol !== "https:" ||
    !["github.com", "release-assets.githubusercontent.com"].includes(finalURL.hostname) ||
    finalURL.username !== "" || finalURL.password !== "" || !response.ok
  ) {
    throw new DoltDistributionError("DOLT_RELEASE_FETCH", "Dolt release fetch left its canonical asset boundary");
  }
  const contentLength = Number(response.headers.get("content-length"));
  if (Number.isFinite(contentLength) && contentLength > MAXIMUM_ARCHIVE_BYTES) {
    throw new DoltDistributionError("DOLT_RELEASE_SIZE", "Dolt release archive exceeds its bounded size");
  }
  const bytes = new Uint8Array(await response.arrayBuffer());
  if (bytes.byteLength === 0 || bytes.byteLength > MAXIMUM_ARCHIVE_BYTES) {
    throw new DoltDistributionError("DOLT_RELEASE_SIZE", "Dolt release archive has an invalid size");
  }
  return bytes;
}

function defaultInspectBinary(path: string): string {
  const result = spawnSync(path, ["version"], {
    encoding: "utf8",
    env: { DOLT_DISABLE_VERSION_CHECK: "1" },
    shell: false,
    timeout: 10_000,
    maxBuffer: 16 * 1024,
  });
  const match = /^dolt version ([0-9]+\.[0-9]+\.[0-9]+)$/mu.exec(result.stdout ?? "");
  if (result.status !== 0 || result.error || !match) {
    throw new DoltDistributionError("DOLT_IDENTITY_MISMATCH", "Dolt executable identity probe failed");
  }
  return match[1]!;
}

function verifyResolved(
  binaryPath: string,
  metadata: PublishedReleaseMetadata["dolt"],
  inspectBinary: (path: string) => string,
): ResolvedDolt {
  const bytes = readPrivateBinary(binaryPath);
  const binarySha256 = sha256(bytes);
  if (
    binarySha256 !== metadata.executableSha256 || bytes.byteLength !== metadata.executableSize ||
    inspectBinary(binaryPath) !== metadata.version
  ) {
    throw new DoltDistributionError("DOLT_CACHE_POISONED", "cached Dolt executable does not match its release pin");
  }
  return {
    version: metadata.version,
    target: "linux-amd64",
    binaryPath,
    binarySha256,
    archiveSha256: metadata.archive.sha256,
  };
}

export async function resolveDolt(
  metadata: PublishedReleaseMetadata["dolt"],
  cacheRoot: string,
  dependencies: DoltDistributionDependencies = {},
): Promise<ResolvedDolt> {
  if (process.platform !== "linux" || process.arch !== "x64") {
    throw new DoltDistributionError("DOLT_TARGET_UNSUPPORTED", "Dolt runtime supports linux-amd64 only");
  }
  if (!isAbsolute(cacheRoot) || !SHA256_PATTERN.test(metadata.executableSha256)) {
    throw new DoltDistributionError("DOLT_RELEASE_METADATA", "Dolt runtime selection is invalid");
  }
  const root = join(cacheRoot, "dolt", metadata.version, "linux-amd64", metadata.executableSha256);
  const binaryPath = join(root, "dolt");
  const inspectBinary = dependencies.inspectBinary ?? defaultInspectBinary;
  if (existsSync(root)) return verifyResolved(binaryPath, metadata, inspectBinary);

  const parent = dirname(root);
  for (const directory of [cacheRoot, join(cacheRoot, "dolt"), join(cacheRoot, "dolt", metadata.version), join(cacheRoot, "dolt", metadata.version, "linux-amd64"), parent]) {
    ensurePrivateDirectory(directory);
  }
  const temporary = join(parent, `.partial-${metadata.executableSha256}-${randomUUID()}`);
  mkdirSync(temporary, { mode: 0o700 });
  const temporaryBinary = join(temporary, "dolt");
  try {
    const archive = await (dependencies.fetchAsset ?? defaultFetchAsset)(metadata.archive.url);
    if (archive.byteLength !== metadata.archive.size || sha256(archive) !== metadata.archive.sha256) {
      throw new DoltDistributionError("DOLT_RELEASE_DIGEST", "Dolt archive digest does not match");
    }
    const executable = extractDoltExecutable(archive);
    if (executable.byteLength !== metadata.executableSize || sha256(executable) !== metadata.executableSha256) {
      throw new DoltDistributionError("DOLT_RELEASE_DIGEST", "Dolt executable digest does not match");
    }
    const descriptor = openSync(
      temporaryBinary,
      constants.O_WRONLY | constants.O_CREAT | constants.O_EXCL | constants.O_NOFOLLOW,
      0o500,
    );
    try {
      writeFileSync(descriptor, executable);
      fsyncSync(descriptor);
    } finally {
      closeSync(descriptor);
    }
    chmodSync(temporaryBinary, 0o500);
    verifyResolved(temporaryBinary, metadata, inspectBinary);
    fsyncDirectory(temporary);
    try {
      renameSync(temporary, root);
      fsyncDirectory(parent);
    } catch (error) {
      if (!existsSync(root)) throw error;
    }
    const resolved = verifyResolved(binaryPath, metadata, inspectBinary);
    dependencies.afterPublish?.(resolved);
    return resolved;
  } catch (error) {
    if (error instanceof DoltDistributionError) throw error;
    throw new DoltDistributionError("DOLT_DISTRIBUTION_IO", "Dolt distribution I/O failed");
  } finally {
    if (existsSync(temporary)) rmSync(temporary, { recursive: true, force: true });
  }
}
