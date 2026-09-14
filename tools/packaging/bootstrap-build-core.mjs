#!/usr/bin/env node
// SPDX-License-Identifier: Apache-2.0
// Paseo's install/update-only adapter builds the Go bootstrap, never the Engine.

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
  realpathSync,
  renameSync,
  rmSync,
  writeFileSync,
} from "node:fs";
import { homedir } from "node:os";
import { dirname, isAbsolute, join, resolve } from "node:path";

const REQUIRED_GO_VERSION = "go1.26.5";
const MAXIMUM_OUTPUT_BYTES = 128 * 1024;
const BUILD_TIMEOUT_MILLIS = 300_000;
const SHA256_PATTERN = /^[0-9a-f]{64}$/u;
const GIT_SHA_PATTERN = /^[0-9a-f]{40}$/u;

export class BootstrapBuildError extends Error {
  constructor(code, message) {
    super(message);
    this.name = "BootstrapBuildError";
    this.code = code;
  }
}

function fail(code, message) { throw new BootstrapBuildError(code, message); }
function sha256(bytes) { return createHash("sha256").update(bytes).digest("hex"); }

function boundedCommand(executable, args, options = {}) {
  const result = (options.spawnSync ?? spawnSync)(executable, args, {
    cwd: options.cwd,
    encoding: "utf8",
    env: options.env ?? {},
    shell: false,
    timeout: options.timeout ?? 10_000,
    maxBuffer: MAXIMUM_OUTPUT_BYTES,
  });
  if (result.status !== 0 || (result.error && result.status === null) || result.signal) {
    if (options.promoteBootstrapCode === true) {
      const childCode = /(?:^|\n)(DIRECTOR_(?:BOOTSTRAP|MAIN|TASKSTORE)_[A-Z0-9_]{1,72})(?:\n|$)/u.exec(String(result.stderr ?? ""))?.[1];
      if (childCode) fail(childCode, "the Director Go bootstrap refused candidate preparation");
    }
    fail(options.code ?? "DIRECTOR_BOOTSTRAP_BUILD_FAILED", options.message ?? "the exact Director bootstrap command failed");
  }
  return String(result.stdout ?? "").trim();
}

export function evaluateSystemExecutableTrust(facts) {
  const systemOwner = facts.parents.every((parent) => parent.isDirectory && !parent.isSymbolicLink &&
    parent.uid === facts.file.uid && (parent.mode & 0o022) === 0);
  return facts.file.isFile && !facts.file.isSymbolicLink && facts.realpathMatches &&
    (facts.file.mode & 0o111) !== 0 && (facts.file.mode & 0o022) === 0 &&
    (facts.file.uid === 0 || facts.file.uid === facts.effectiveUserID || systemOwner) && facts.elf;
}

function systemExecutable(path) {
  try {
    const status = lstatSync(path);
    const header = readFileSync(path).subarray(0, 4);
    const parents = [dirname(path), dirname(dirname(path))].map((directory) => {
      const parent = lstatSync(directory);
      return { isDirectory: parent.isDirectory(), isSymbolicLink: parent.isSymbolicLink(), mode: parent.mode, uid: parent.uid };
    });
    return evaluateSystemExecutableTrust({
      file: { isFile: status.isFile(), isSymbolicLink: status.isSymbolicLink(), mode: status.mode, uid: status.uid },
      parents,
      realpathMatches: realpathSync(path) === resolve(path),
      effectiveUserID: process.geteuid?.(),
      elf: header.equals(Buffer.from([0x7f, 0x45, 0x4c, 0x46])),
    });
  } catch { return false; }
}

function resolveBootstrapGoToolchain(runtime) {
  if (runtime.platform !== "linux" || runtime.architecture !== "x64") {
    fail("DIRECTOR_MAIN_GO_TOOLCHAIN_TARGET", "main bootstrap builds require Linux amd64");
  }
  let versionMismatch = false;
  let capabilityMismatch = false;
  for (const candidate of runtime.fixedCandidates) {
    if (!systemExecutable(candidate)) continue;
    const probe = { cwd: dirname(dirname(candidate)), env: { GOENV: "off", GOTOOLCHAIN: "local", GOWORK: "off" }, spawnSync: runtime.spawnSync };
    const version = boundedCommand(candidate, ["version"], { ...probe, code: "DIRECTOR_MAIN_GO_TOOLCHAIN_VERSION", message: `exact Go ${REQUIRED_GO_VERSION} is required for the main bootstrap` });
    const capability = boundedCommand(candidate, ["env", "GOOS", "GOARCH", "GOVERSION"], { ...probe, code: "DIRECTOR_MAIN_GO_TOOLCHAIN_CAPABILITY", message: `Go ${REQUIRED_GO_VERSION} must support Linux amd64` });
    if (!new RegExp(`\\b${REQUIRED_GO_VERSION.replaceAll(".", "\\.")}\\b`, "u").test(version)) {
      versionMismatch = true;
      continue;
    }
    if (capability !== `linux\namd64\n${REQUIRED_GO_VERSION}`) {
      capabilityMismatch = true;
      continue;
    }
    return candidate;
  }
  if (capabilityMismatch) fail("DIRECTOR_MAIN_GO_TOOLCHAIN_CAPABILITY", `Go ${REQUIRED_GO_VERSION} must support Linux amd64`);
  if (versionMismatch) fail("DIRECTOR_MAIN_GO_TOOLCHAIN_VERSION", `exact Go ${REQUIRED_GO_VERSION} is required for the main bootstrap`);
  fail("DIRECTOR_MAIN_GO_TOOLCHAIN_MISSING", `an owned fixed-system Go ${REQUIRED_GO_VERSION} executable is required for the main channel`);
}

function ensurePrivateDirectory(path) {
  mkdirSync(path, { recursive: true, mode: 0o700 });
  const status = lstatSync(path);
  if (!status.isDirectory() || status.isSymbolicLink() || (status.mode & 0o077) !== 0 || status.uid !== process.geteuid?.()) {
    fail("DIRECTOR_BOOTSTRAP_CACHE_PERMISSIONS", "the Director bootstrap cache is not private and owned");
  }
}

function fsyncDirectory(path) {
  const descriptor = openSync(path, constants.O_RDONLY | constants.O_DIRECTORY | constants.O_NOFOLLOW);
  try { fsyncSync(descriptor); } finally { closeSync(descriptor); }
}

function writePrivate(path, bytes, mode) {
  const descriptor = openSync(path, constants.O_WRONLY | constants.O_CREAT | constants.O_EXCL | constants.O_NOFOLLOW, 0o600);
  try { writeFileSync(descriptor, bytes); fsyncSync(descriptor); } finally { closeSync(descriptor); }
  chmodSync(path, mode);
}

function privateBootstrap(path, expected = {}) {
  let status;
  let bytes;
  try { status = lstatSync(path); bytes = readFileSync(path); } catch { fail("DIRECTOR_BOOTSTRAP_MISSING", "the exact Director bootstrap is unavailable"); }
  const digest = sha256(bytes);
  if (!status.isFile() || status.isSymbolicLink() || status.nlink !== 1 || (status.mode & 0o077) !== 0 ||
    status.uid !== process.geteuid?.() || (status.mode & 0o100) === 0 || status.size < 1 || status.size > 256 * 1024 * 1024 ||
    (expected.sha256 && digest !== expected.sha256) || (expected.size && status.size !== expected.size)) {
    fail("DIRECTOR_BOOTSTRAP_DIGEST", "the exact Director bootstrap does not match its private cache pin");
  }
  return { schemaVersion: 1, target: "linux-amd64", path, sha256: digest, size: status.size };
}

function cacheBootstrap(source, cacheBase, candidate, expected = {}) {
  const sourceStatus = lstatSync(source);
  const bytes = readFileSync(source);
  const digest = sha256(bytes);
  if (!sourceStatus.isFile() || sourceStatus.isSymbolicLink() || (sourceStatus.mode & 0o022) !== 0 ||
    (sourceStatus.mode & 0o111) === 0 || sourceStatus.size < 1 || sourceStatus.size > 256 * 1024 * 1024 ||
    (expected.sha256 && expected.sha256 !== digest) || (expected.size && expected.size !== sourceStatus.size)) {
    fail("DIRECTOR_BOOTSTRAP_DIGEST", "the shipped Director bootstrap does not match its manifest pin");
  }
  const parent = join(cacheBase, "director", "bootstraps", candidate, "linux-amd64");
  for (const path of [join(cacheBase, "director"), join(cacheBase, "director", "bootstraps"), join(cacheBase, "director", "bootstraps", candidate), parent]) ensurePrivateDirectory(path);
  const root = join(parent, digest);
  const target = join(root, "director-bootstrap");
  if (!existsSync(root)) {
    const temporary = join(parent, `.partial-${randomUUID()}`);
    mkdirSync(temporary, { mode: 0o700 });
    try {
      writePrivate(join(temporary, "director-bootstrap"), bytes, 0o500);
      fsyncDirectory(temporary);
      try { renameSync(temporary, root); } catch (error) { if (!existsSync(root)) throw error; }
      fsyncDirectory(parent);
    } finally { rmSync(temporary, { recursive: true, force: true }); }
  }
  return privateBootstrap(target, { sha256: digest, size: bytes.byteLength });
}

function cacheBase(environment) {
  const home = environment.HOME && isAbsolute(environment.HOME) ? resolve(environment.HOME) : homedir();
  const base = environment.XDG_CACHE_HOME && isAbsolute(environment.XDG_CACHE_HOME) ? resolve(environment.XDG_CACHE_HOME) : join(home, ".cache");
  return { home, base };
}

function acquireBuildLock(path) {
  const deadline = Date.now() + BUILD_TIMEOUT_MILLIS;
  for (;;) {
    try {
      const descriptor = openSync(path, constants.O_WRONLY | constants.O_CREAT | constants.O_EXCL | constants.O_NOFOLLOW, 0o600);
      writeFileSync(descriptor, `${JSON.stringify({ schemaVersion: 1, pid: process.pid })}\n`); fsyncSync(descriptor); closeSync(descriptor);
      return () => rmSync(path, { force: false });
    } catch {
      let marker;
      try { marker = JSON.parse(readFileSync(path, "utf8")); } catch { fail("DIRECTOR_BOOTSTRAP_CACHE_POISONED", "the bootstrap preparation lock is invalid"); }
      if (marker?.schemaVersion !== 1 || !Number.isSafeInteger(marker?.pid) || marker.pid <= 0) fail("DIRECTOR_BOOTSTRAP_CACHE_POISONED", "the bootstrap preparation lock is invalid");
      try { process.kill(marker.pid, 0); } catch { rmSync(path, { force: false }); continue; }
      if (Date.now() >= deadline) fail("DIRECTOR_MAIN_BOOTSTRAP_BUILD_TIMEOUT", "the exact bootstrap preparation did not finish in time");
      Atomics.wait(new Int32Array(new SharedArrayBuffer(4)), 0, 0, 100);
    }
  }
}

function buildMainBootstrap(options, runtime) {
  const repositoryRoot = resolve(options.repositoryRoot);
  const candidate = options.sourceCandidate;
  if (!GIT_SHA_PATTERN.test(candidate)) fail("DIRECTOR_MAIN_SOURCE_IDENTITY", "the exact main Candidate is invalid");
  const environment = options.environment ?? process.env;
  const location = cacheBase(environment);
  const candidateRoot = join(location.base, "director", "bootstraps", candidate, "linux-amd64");
  const pointer = join(candidateRoot, "prepared.json");
  if (existsSync(pointer)) {
    const prepared = JSON.parse(readFileSync(pointer, "utf8"));
    if (prepared?.schemaVersion !== 1 || prepared?.sourceCandidate !== candidate || !SHA256_PATTERN.test(prepared?.sha256 ?? "")) fail("DIRECTOR_BOOTSTRAP_CACHE_POISONED", "the prepared bootstrap pointer is invalid");
    return { metadata: privateBootstrap(join(candidateRoot, prepared.sha256, "director-bootstrap"), prepared), compilerInvocations: 0, cacheState: "adopted" };
  }
  for (const path of [join(location.base, "director"), join(location.base, "director", "bootstraps"), join(location.base, "director", "bootstraps", candidate), candidateRoot]) ensurePrivateDirectory(path);
  const releaseLock = acquireBuildLock(join(candidateRoot, ".build.lock"));
  const temporary = join(candidateRoot, `.build-${randomUUID()}`);
  const binary = join(temporary, "director-bootstrap");
  try {
    if (existsSync(pointer)) {
      const prepared = JSON.parse(readFileSync(pointer, "utf8"));
      return { metadata: privateBootstrap(join(candidateRoot, prepared.sha256, "director-bootstrap"), prepared), compilerInvocations: 0, cacheState: "adopted" };
    }
    const go = resolveBootstrapGoToolchain(runtime);
    mkdirSync(temporary, { mode: 0o700 });
    const moduleCache = environment.GOMODCACHE && isAbsolute(environment.GOMODCACHE) ? resolve(environment.GOMODCACHE) : join(location.home, "go", "pkg", "mod");
    const goCache = join(location.base, "director", "go-build-cache", REQUIRED_GO_VERSION);
    ensurePrivateDirectory(join(location.base, "director", "go-build-cache")); ensurePrivateDirectory(goCache);
    boundedCommand(go, ["build", "-trimpath", "-buildvcs=false", "-mod=readonly", "-ldflags", `-s -w -X main.bootstrapMode=main -X main.bootstrapCandidate=${candidate}`, "-o", binary, "./cmd/director-bootstrap"], {
      cwd: join(repositoryRoot, "engine"), spawnSync: runtime.spawnSync, timeout: BUILD_TIMEOUT_MILLIS, code: "DIRECTOR_MAIN_BOOTSTRAP_BUILD_FAILED",
      message: "the exact main Director bootstrap failed to compile from the Go-sum-verified offline module closure",
      env: { HOME: location.home, GOCACHE: goCache, GOMODCACHE: moduleCache, CGO_ENABLED: "0", GOARCH: "amd64", GOOS: "linux", GOENV: "off", GOWORK: "off", GOPROXY: "off", GOSUMDB: "off", GOTOOLCHAIN: "local" },
    });
    chmodSync(binary, 0o500);
    const identity = JSON.parse(boundedCommand(binary, ["version"], { env: {}, spawnSync: runtime.spawnSync, code: "DIRECTOR_BOOTSTRAP_IDENTITY", message: "the main Director bootstrap cannot prove its identity" }));
    if (identity.name !== "director-bootstrap" || identity.buildMode !== "main" || identity.sourceCandidate !== candidate || identity.target !== "linux-amd64") fail("DIRECTOR_BOOTSTRAP_IDENTITY", "the main Director bootstrap identity does not match the Candidate");
    const metadata = cacheBootstrap(binary, location.base, candidate);
    const source = `${JSON.stringify({ schemaVersion: 1, sourceCandidate: candidate, sha256: metadata.sha256, size: metadata.size })}\n`;
    const temporaryPointer = join(candidateRoot, `.prepared-${randomUUID()}`);
    writePrivate(temporaryPointer, Buffer.from(source), 0o400);
    renameSync(temporaryPointer, pointer); fsyncDirectory(candidateRoot);
    return { metadata, compilerInvocations: 1, cacheState: "published" };
  } finally { rmSync(temporary, { recursive: true, force: true }); releaseLock(); }
}

export function createBootstrapBuildCore(options) {
  if (!Array.isArray(options?.fixedCandidates) || options.fixedCandidates.some((candidate) => typeof candidate !== "string" || !isAbsolute(candidate)) ||
    typeof options?.spawnSync !== "function" || typeof options?.platform !== "string" || typeof options?.architecture !== "string") {
    throw new TypeError("bootstrap build core requires fixed absolute candidates and explicit process capabilities");
  }
  const runtime = Object.freeze({
    fixedCandidates: Object.freeze([...options.fixedCandidates]),
    spawnSync: options.spawnSync,
    platform: options.platform,
    architecture: options.architecture,
  });
  return Object.freeze({
    resolveBootstrapGoToolchain: () => resolveBootstrapGoToolchain(runtime),
    buildMainBootstrap: (buildOptions) => buildMainBootstrap(buildOptions, runtime),
  });
}

export function cachePublishedBootstrap(options) {
  const manifest = options.manifest;
  const candidate = options.sourceCandidate;
  const expectedURL = `https://github.com/mcuadros/paseo-director/releases/download/v${manifest?.version}/director-bootstrap-linux-amd64`;
  if (JSON.stringify(Object.keys(manifest ?? {}).sort()) !== JSON.stringify(["binary", "schemaVersion", "sourceCandidate", "state", "target", "version"]) ||
    JSON.stringify(Object.keys(manifest?.binary ?? {}).sort()) !== JSON.stringify(["name", "sha256", "size", "url"]) ||
    manifest?.schemaVersion !== 1 || manifest?.state !== "published" || manifest?.target !== "linux-amd64" || manifest?.sourceCandidate !== candidate ||
    !/^(?:0|[1-9][0-9]*)\.(?:0|[1-9][0-9]*)\.(?:0|[1-9][0-9]*)(?:-[0-9A-Za-z.-]+)?$/u.test(manifest?.version ?? "") ||
    manifest?.binary?.name !== "director-bootstrap-linux-amd64" || manifest?.binary?.url !== expectedURL ||
    !SHA256_PATTERN.test(manifest?.binary?.sha256 ?? "") || manifest?.binary?.sha256 === "0".repeat(64) ||
    !Number.isSafeInteger(manifest?.binary?.size) || manifest.binary.size <= 0 || manifest.binary.size > 256 * 1024 * 1024) {
    fail("DIRECTOR_BOOTSTRAP_RELEASE_MANIFEST", "the published Director bootstrap manifest is invalid");
  }
  const location = cacheBase(options.environment ?? process.env);
  return { metadata: cacheBootstrap(join(options.repositoryRoot, "release", manifest.binary.name), location.base, candidate, manifest.binary), compilerInvocations: 0, cacheState: "published" };
}

export function runBootstrapPreparation(options) {
  const environment = options.environment ?? process.env;
  const location = cacheBase(environment);
  const output = boundedCommand(options.bootstrap.path, ["prepare", "--candidate", options.sourceCandidate, "--bootstrap-sha256", options.bootstrap.sha256, "--host-socket", ""], {
    cwd: options.repositoryRoot, spawnSync: options.spawnSync, timeout: BUILD_TIMEOUT_MILLIS,
    env: { HOME: location.home, ...(environment.XDG_RUNTIME_DIR ? { XDG_RUNTIME_DIR: environment.XDG_RUNTIME_DIR } : {}), ...(environment.XDG_CACHE_HOME ? { XDG_CACHE_HOME: environment.XDG_CACHE_HOME } : {}), ...(environment.XDG_CONFIG_HOME ? { XDG_CONFIG_HOME: environment.XDG_CONFIG_HOME } : {}), ...(environment.XDG_DATA_HOME ? { XDG_DATA_HOME: environment.XDG_DATA_HOME } : {}), ...(environment.GOMODCACHE ? { GOMODCACHE: environment.GOMODCACHE } : {}) },
    code: "DIRECTOR_BOOTSTRAP_PREPARATION_FAILED", message: "the Director Go bootstrap failed candidate preparation", promoteBootstrapCode: true,
  });
  let result;
  try { result = JSON.parse(output); } catch { fail("DIRECTOR_BOOTSTRAP_PREPARATION_FAILED", "the Director Go bootstrap returned invalid preparation state"); }
  if (result?.schemaVersion !== 1 || result?.code !== "DIRECTOR_BOOTSTRAP_PREPARED" || result?.sourceCandidate !== options.sourceCandidate || result?.bootstrap?.sha256 !== options.bootstrap.sha256 ||
    !["main", "release"].includes(result?.channel) || !Number.isSafeInteger(result?.engineBuilds) || result.engineBuilds < 0 || result.engineBuilds > 1) {
    fail("DIRECTOR_BOOTSTRAP_PREPARATION_FAILED", "the Director Go bootstrap preparation identity is invalid");
  }
  return result;
}
