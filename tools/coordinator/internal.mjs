// SPDX-License-Identifier: Apache-2.0

import { createHash, randomBytes } from "node:crypto";
import {
  closeSync,
  existsSync,
  fsyncSync,
  lstatSync,
  openSync,
  readFileSync,
  realpathSync,
  renameSync,
  statSync,
  unlinkSync,
  writeFileSync,
} from "node:fs";
import { basename, dirname, isAbsolute, relative, resolve } from "node:path";

export const DEFAULT_MAX_JSON_BYTES = 1_048_576;

export class CoordinatorError extends Error {
  constructor(code, message, details = undefined) {
    super(message);
    this.name = "CoordinatorError";
    this.code = code;
    this.details = details;
  }
}

export function refuse(condition, code, message, details = undefined) {
  if (condition) throw new CoordinatorError(code, message, details);
}

export function isObject(value) {
  return value !== null && typeof value === "object" && !Array.isArray(value);
}

export function compareText(left, right) {
  return left < right ? -1 : left > right ? 1 : 0;
}

export function canonicalize(value) {
  if (Array.isArray(value)) return value.map(canonicalize);
  if (!isObject(value)) return value;
  return Object.fromEntries(
    Object.keys(value)
      .sort()
      .map((key) => [key, canonicalize(value[key])]),
  );
}

export function canonicalJson(value) {
  return JSON.stringify(canonicalize(value));
}

export function digest(value) {
  return createHash("sha256").update(canonicalJson(value)).digest("hex");
}

export function boundedText(value, maximum = 800) {
  return String(value ?? "")
    .replaceAll(/[\u0000-\u0008\u000b\u000c\u000e-\u001f\u007f]/gu, "?")
    .replaceAll(/https:\/\/[^/@\s]+@/giu, "https://[redacted]@")
    .slice(0, maximum);
}

export function assertExactKeys(
  value,
  allowed,
  label,
  code = "SCHEMA_INVALID",
) {
  refuse(!isObject(value), code, `${label} must be an object`);
  const unknown = Object.keys(value).filter((key) => !allowed.includes(key));
  refuse(
    unknown.length > 0,
    code,
    `${label} contains unknown fields`,
    { fields: unknown.toSorted() },
  );
}

export function parseBoundedJson(
  output,
  label,
  {
    maximum = DEFAULT_MAX_JSON_BYTES,
    oversizeCode = "JSON_OVERSIZE",
    invalidCode = "JSON_INVALID",
  } = {},
) {
  refuse(
    Buffer.byteLength(output) > maximum,
    oversizeCode,
    `${label} JSON exceeded the bounded size`,
  );
  try {
    return JSON.parse(output);
  } catch {
    throw new CoordinatorError(invalidCode, `${label} returned invalid JSON`);
  }
}

export function readJsonFile(
  path,
  label,
  {
    maximum = DEFAULT_MAX_JSON_BYTES,
    unavailableCode = "FILE_UNAVAILABLE",
    identityCode = "FILE_IDENTITY_INVALID",
    oversizeCode = "FILE_OVERSIZE",
    invalidCode = "JSON_INVALID",
  } = {},
) {
  refuse(!isAbsolute(path), "PATH_NOT_ABSOLUTE", `${label} path must be absolute`);
  let status;
  try {
    status = lstatSync(path);
  } catch {
    throw new CoordinatorError(unavailableCode, `${label} is unavailable`);
  }
  refuse(
    !status.isFile() || status.isSymbolicLink(),
    identityCode,
    `${label} must be a regular non-symlink file`,
  );
  refuse(status.size > maximum, oversizeCode, `${label} is too large`);
  return parseBoundedJson(readFileSync(path, "utf8"), label, {
    maximum,
    oversizeCode,
    invalidCode,
  });
}

export function canonicalExistingDirectory(path, label) {
  refuse(!isAbsolute(path), "PATH_NOT_ABSOLUTE", `${label} must be absolute`);
  let canonical;
  try {
    canonical = realpathSync(path);
  } catch {
    throw new CoordinatorError("PATH_UNAVAILABLE", `${label} is unavailable`);
  }
  refuse(
    !statSync(canonical).isDirectory(),
    "PATH_IDENTITY_INVALID",
    `${label} must be a directory`,
  );
  return canonical;
}

export function canonicalPath(path, label, { mustExist = true } = {}) {
  refuse(!isAbsolute(path), "PATH_NOT_ABSOLUTE", `${label} must be absolute`);
  if (mustExist) return canonicalExistingDirectory(path, label);
  const parent = canonicalExistingDirectory(dirname(path), `${label} parent`);
  return resolve(parent, basename(path));
}

export function pathIsWithin(root, path) {
  const relation = relative(root, path);
  return relation === "" || (!relation.startsWith("..") && !isAbsolute(relation));
}

export function githubRepositoryIdentity(url) {
  const normalized = url.trim().replace(/\.git$/u, "");
  for (const pattern of [
    /^https:\/\/github\.com\/([^/]+\/[^/]+)$/u,
    /^ssh:\/\/git@github\.com\/([^/]+\/[^/]+)$/u,
    /^git@github\.com:([^/]+\/[^/]+)$/u,
  ]) {
    const match = normalized.match(pattern);
    if (match) return match[1];
  }
  return null;
}

export function isPriorReviewFinding(text, currentCandidate) {
  if (/^REVIEW FINDING\b/mu.test(text)) return true;
  const candidate = text.match(/^Candidate:\s*([0-9a-f]{40})\b/mu)?.[1];
  return (
    /^INDEPENDENT REVIEW\b/mu.test(text) &&
    /^Verdict:\s*changes_requested\b/mu.test(text) &&
    candidate !== undefined &&
    candidate !== currentCandidate
  );
}

export function linuxProcessStartTime(pid, code = "PROCESS_IDENTITY_UNAVAILABLE") {
  try {
    const stat = readFileSync(`/proc/${pid}/stat`, "utf8");
    return stat
      .slice(stat.lastIndexOf(")") + 2)
      .trim()
      .split(/\s+/u)[19] ?? null;
  } catch (error) {
    if (error?.code === "ENOENT") return null;
    throw new CoordinatorError(code, "process identity could not be observed");
  }
}

export function flushDirectory(path) {
  const descriptor = openSync(path, "r");
  try {
    fsyncSync(descriptor);
  } finally {
    closeSync(descriptor);
  }
}

export function persistPrivateJson(
  path,
  value,
  {
    identityCode = "STATE_IDENTITY_INVALID",
    temporaryCode = "STATE_TEMP_EXISTS",
  } = {},
) {
  const parent = canonicalExistingDirectory(dirname(path), "state parent");
  if (existsSync(path)) {
    const status = lstatSync(path);
    refuse(
      !status.isFile() || status.isSymbolicLink(),
      identityCode,
      "state path must remain a regular non-symlink file",
    );
  }
  const temporary = `${path}.next-${process.pid}`;
  refuse(existsSync(temporary), temporaryCode, "state temporary path already exists");
  let descriptor;
  try {
    descriptor = openSync(temporary, "wx", 0o600);
    writeFileSync(descriptor, `${canonicalJson(value)}\n`, "utf8");
    fsyncSync(descriptor);
    closeSync(descriptor);
    descriptor = undefined;
    renameSync(temporary, path);
    flushDirectory(parent);
  } finally {
    if (descriptor !== undefined) closeSync(descriptor);
    if (existsSync(temporary)) unlinkSync(temporary);
  }
}

export async function withProcessIdentityLock(
  {
    lockPath,
    bindingHash,
    label,
    busyCode,
    invalidCode,
    replacedCode,
    processCode,
  },
  operation,
) {
  const parent = canonicalExistingDirectory(dirname(lockPath), `${label} parent`);
  for (let attempt = 0; attempt < 2; attempt += 1) {
    if (existsSync(lockPath)) {
      const status = lstatSync(lockPath);
      refuse(
        !status.isFile() ||
          status.isSymbolicLink() ||
          (status.mode & 0o077) !== 0 ||
          (typeof process.getuid === "function" && status.uid !== process.getuid()),
        invalidCode,
        `${label} is not an exact private owned file`,
      );
      const existing = readJsonFile(lockPath, label);
      assertExactKeys(
        existing,
        ["schemaVersion", "pid", "processStartTime", "nonce", "bindingHash"],
        label,
        invalidCode,
      );
      refuse(
        existing.schemaVersion !== 1 ||
          existing.bindingHash !== bindingHash ||
          !Number.isSafeInteger(existing.pid) ||
          typeof existing.processStartTime !== "string" ||
          !/^[0-9a-f]{32}$/u.test(existing.nonce ?? ""),
        invalidCode,
        `${label} is invalid or bound to different inputs`,
      );
      refuse(
        linuxProcessStartTime(existing.pid, processCode) ===
          existing.processStartTime,
        busyCode,
        `another process owns ${label}`,
      );
      unlinkSync(lockPath);
      flushDirectory(parent);
    }
    const token = {
      schemaVersion: 1,
      pid: process.pid,
      processStartTime: linuxProcessStartTime(process.pid, processCode),
      nonce: randomBytes(16).toString("hex"),
      bindingHash,
    };
    refuse(
      token.processStartTime === null,
      processCode,
      "current process identity is unavailable",
    );
    let descriptor;
    try {
      descriptor = openSync(lockPath, "wx", 0o600);
      writeFileSync(descriptor, `${canonicalJson(token)}\n`, "utf8");
      fsyncSync(descriptor);
      closeSync(descriptor);
      descriptor = undefined;
      flushDirectory(parent);
    } catch (error) {
      if (descriptor !== undefined) closeSync(descriptor);
      if (error?.code === "EEXIST" && attempt === 0) continue;
      if (error?.code === "EEXIST") {
        throw new CoordinatorError(busyCode, `another process won ${label}`);
      }
      throw error;
    }
    try {
      return await operation();
    } finally {
      if (existsSync(lockPath)) {
        const current = readJsonFile(lockPath, label);
        refuse(
          canonicalJson(current) !== canonicalJson(token),
          replacedCode,
          `${label} changed during the operation`,
        );
        unlinkSync(lockPath);
        flushDirectory(parent);
      }
    }
  }
  throw new CoordinatorError(busyCode, `another process won ${label}`);
}
