#!/usr/bin/env node
// SPDX-License-Identifier: Apache-2.0

import { spawnSync } from "node:child_process";
import { createHash, randomUUID } from "node:crypto";
import {
  chmodSync,
  closeSync,
  constants,
  copyFileSync,
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
import { dirname, isAbsolute, join, resolve } from "node:path";
import { fileURLToPath } from "node:url";

const SHA_PATTERN = /^[0-9a-f]{40}$/u;
const VERSION_PATTERN = /^(?:0|[1-9][0-9]*)\.(?:0|[1-9][0-9]*)\.(?:0|[1-9][0-9]*)(?:-[0-9A-Za-z.-]+)?$/u;

function fail(code, message) {
  const error = new Error(message);
  error.code = code;
  throw error;
}

function sha256(path) {
  return createHash("sha256").update(readFileSync(path)).digest("hex");
}

function command(executable, args, options = {}) {
  const result = spawnSync(executable, args, {
    cwd: options.cwd,
    encoding: "utf8",
    env: options.env ?? process.env,
    shell: false,
    timeout: options.timeout ?? 120_000,
    maxBuffer: 1024 * 1024,
  });
  if (result.status !== 0 || (result.error && result.status === null)) {
    const error = new Error(`${executable} failed`);
    error.code = options.code ?? "RELEASE_COMMAND_FAILED";
    error.exitStatus = result.status;
    throw error;
  }
  return result.stdout.trim();
}

function exactPath(value, name) {
  if (!value || !isAbsolute(value)) fail("RELEASE_ARGUMENT", `${name} must be absolute`);
  return resolve(value);
}

function parseArguments(argumentsValue) {
  const values = new Map();
  for (let index = 0; index < argumentsValue.length; index += 2) {
    const name = argumentsValue[index];
    const value = argumentsValue[index + 1];
    if (!name?.startsWith("--") || value === undefined || values.has(name)) {
      fail("RELEASE_ARGUMENT", "release builder arguments are invalid");
    }
    values.set(name, value);
  }
  if ([...values.keys()].some((name) => !["--source", "--candidate", "--version", "--notices", "--output"].includes(name))) {
    fail("RELEASE_ARGUMENT", "release builder argument is unsupported");
  }
  const candidate = values.get("--candidate");
  const version = values.get("--version");
  if (!SHA_PATTERN.test(candidate ?? "") || !VERSION_PATTERN.test(version ?? "")) {
    fail("RELEASE_ARGUMENT", "release version or Candidate is invalid");
  }
  return {
    source: exactPath(values.get("--source"), "source"),
    candidate,
    version,
    notices: exactPath(values.get("--notices"), "notices"),
    output: exactPath(values.get("--output"), "output"),
  };
}

function assertRegular(path, code) {
  const status = lstatSync(path);
  if (!status.isFile() || status.isSymbolicLink()) fail(code, "release input must be a regular file");
}

function assertPrivateRegular(path, code) {
  assertRegular(path, code);
  const status = lstatSync(path);
  if ((status.mode & 0o077) !== 0 || status.uid !== process.geteuid()) {
    fail(code, "release output file is not private and owned");
  }
}

function fsyncPath(path, directory = false) {
  const descriptor = openSync(path, constants.O_RDONLY | constants.O_NOFOLLOW | (directory ? constants.O_DIRECTORY : 0));
  try {
    fsyncSync(descriptor);
  } finally {
    closeSync(descriptor);
  }
}

function ensurePrivateDirectory(path) {
  mkdirSync(path, { recursive: true, mode: 0o700 });
  const status = lstatSync(path);
  if (!status.isDirectory() || status.isSymbolicLink() || (status.mode & 0o077) !== 0 || status.uid !== process.geteuid()) {
    fail("RELEASE_OUTPUT_PERMISSIONS", "release output directory is not private and owned");
  }
}

function verifyExistingOutput(options) {
  try {
    const status = lstatSync(options.output);
    if (!status.isDirectory() || status.isSymbolicLink() || (status.mode & 0o077) !== 0 || status.uid !== process.geteuid()) {
      fail("RELEASE_OUTPUT_POISONED", "existing release output is unsafe");
    }
    const metadataPath = join(options.output, "engine.json");
    const binaryPath = join(options.output, "director-engine-linux-amd64");
    const noticesPath = join(options.output, "THIRD_PARTY_NOTICES.txt");
    for (const path of [metadataPath, binaryPath, noticesPath]) assertPrivateRegular(path, "RELEASE_OUTPUT_POISONED");
    const metadata = JSON.parse(readFileSync(metadataPath, "utf8"));
    const expectedBinaryURL = `https://github.com/mcuadros/paseo-director/releases/download/v${options.version}/director-engine-linux-amd64`;
    const expectedNoticesURL = `https://github.com/mcuadros/paseo-director/releases/download/v${options.version}/THIRD_PARTY_NOTICES.txt`;
    if (
      JSON.stringify(Object.keys(metadata).sort()) !== JSON.stringify(["binary", "notices", "schemaVersion", "sourceCandidate", "state", "target", "version"]) ||
      JSON.stringify(Object.keys(metadata.binary ?? {}).sort()) !== JSON.stringify(["name", "sha256", "url"]) ||
      JSON.stringify(Object.keys(metadata.notices ?? {}).sort()) !== JSON.stringify(["name", "sha256", "url"]) ||
      metadata.schemaVersion !== 2 || metadata.state !== "published" ||
      metadata.version !== options.version || metadata.target !== "linux-amd64" ||
      metadata.sourceCandidate !== options.candidate ||
      metadata.binary?.name !== "director-engine-linux-amd64" ||
      metadata.notices?.name !== "THIRD_PARTY_NOTICES.txt" ||
      metadata.binary?.url !== expectedBinaryURL || metadata.notices?.url !== expectedNoticesURL ||
      metadata.binary?.sha256 !== sha256(binaryPath) ||
      metadata.notices?.sha256 !== sha256(noticesPath) ||
      metadata.notices.sha256 !== sha256(options.notices)
    ) {
      fail("RELEASE_OUTPUT_POISONED", "existing release output does not match the requested closure");
    }
    const identity = JSON.parse(command(binaryPath, ["version"], { env: {}, code: "RELEASE_OUTPUT_POISONED", timeout: 10_000 }));
    if (
      identity.name !== "director-engine" || identity.buildMode !== "release" ||
      identity.version !== options.version || identity.sourceCandidate !== options.candidate ||
      identity.target !== "linux-amd64" || identity.executableSha256 !== metadata.binary.sha256 ||
      identity.noticesSha256 !== metadata.notices.sha256 ||
      identity.productBehavior !== true || !/^[0-9a-f]{64}$/u.test(identity.contractSha256 ?? "")
    ) {
      fail("RELEASE_OUTPUT_POISONED", "existing release identity does not match");
    }
    return { metadata, identity, output: options.output };
  } catch (error) {
    if (error?.code === "RELEASE_OUTPUT_POISONED") throw error;
    fail("RELEASE_OUTPUT_POISONED", "existing release output cannot be verified");
  }
}

function assertGoToolchain() {
  const output = command("go", ["version"], { code: "RELEASE_GO_TOOLCHAIN", timeout: 10_000 });
  const match = /\bgo1\.(\d+)\.(\d+)\b/u.exec(output);
  if (!match || Number(match[1]) < 26) fail("RELEASE_GO_TOOLCHAIN", "Go 1.26 or newer is required");
}

function assertSource(source, candidate) {
  const head = command("git", ["-C", source, "rev-parse", "HEAD"], { code: "RELEASE_SOURCE_IDENTITY", timeout: 10_000 });
  const status = command("git", ["-C", source, "status", "--porcelain"], { code: "RELEASE_SOURCE_IDENTITY", timeout: 10_000 });
  if (head !== candidate || status !== "") fail("RELEASE_SOURCE_IDENTITY", "release source does not match the exact clean Candidate");
}

function buildEnvironment() {
  return {
    ...(process.env.PATH ? { PATH: process.env.PATH } : {}),
    ...(process.env.HOME ? { HOME: process.env.HOME } : {}),
    ...(process.env.XDG_CACHE_HOME ? { XDG_CACHE_HOME: process.env.XDG_CACHE_HOME } : {}),
    ...(process.env.GOCACHE ? { GOCACHE: process.env.GOCACHE } : {}),
    ...(process.env.GOMODCACHE ? { GOMODCACHE: process.env.GOMODCACHE } : {}),
    CGO_ENABLED: "0",
    GOARCH: "amd64",
    GOOS: "linux",
    GOWORK: "off",
    GOPROXY: "off",
    GOSUMDB: "off",
    GOTOOLCHAIN: "local",
  };
}

export function buildRelease(argumentsValue) {
  const options = parseArguments(argumentsValue);
  assertSource(options.source, options.candidate);
  assertRegular(options.notices, "RELEASE_NOTICES_IDENTITY");
  if (!readFileSync(options.notices, "utf8").split("\n").includes(`source-candidate: ${options.candidate}`)) {
    fail("RELEASE_NOTICES_IDENTITY", "notices do not identify the exact source Candidate");
  }
  if (existsSync(options.output)) return verifyExistingOutput(options);
  assertGoToolchain();
  const parent = dirname(options.output);
  ensurePrivateDirectory(parent);
  const temporary = join(parent, `.partial-${options.candidate}-${randomUUID()}`);
  mkdirSync(temporary, { mode: 0o700 });
  const binaryName = "director-engine-linux-amd64";
  const noticesName = "THIRD_PARTY_NOTICES.txt";
  const binary = join(temporary, binaryName);
  const notices = join(temporary, noticesName);
  try {
    copyFileSync(options.notices, notices);
    chmodSync(notices, 0o400);
    fsyncPath(notices);
    const noticesSha256 = sha256(notices);
    command("go", [
      "build", "-trimpath", "-buildvcs=false", "-ldflags",
      `-s -w -X main.buildMode=release -X main.version=${options.version} -X main.sourceCandidate=${options.candidate} -X main.noticesSha=${noticesSha256}`,
      "-o", binary, "./cmd/director-engine",
    ], { cwd: options.source, env: buildEnvironment(), code: "RELEASE_BUILD_FAILED" });
    chmodSync(binary, 0o500);
    fsyncPath(binary);
    const identity = JSON.parse(command(binary, ["version"], { env: {}, code: "RELEASE_IDENTITY_MISMATCH", timeout: 10_000 }));
    if (
      identity.name !== "director-engine" || identity.buildMode !== "release" ||
      identity.version !== options.version || identity.sourceCandidate !== options.candidate ||
      identity.target !== "linux-amd64" || identity.executableSha256 !== sha256(binary) ||
      identity.noticesSha256 !== noticesSha256 ||
      identity.productBehavior !== true || !/^[0-9a-f]{64}$/u.test(identity.contractSha256 ?? "")
    ) {
      fail("RELEASE_IDENTITY_MISMATCH", "built engine identity does not match release inputs");
    }
    const metadata = {
      schemaVersion: 2,
      state: "published",
      version: options.version,
      target: "linux-amd64",
      sourceCandidate: options.candidate,
      binary: {
        name: binaryName,
        url: `https://github.com/mcuadros/paseo-director/releases/download/v${options.version}/${binaryName}`,
        sha256: sha256(binary),
      },
      notices: {
        name: noticesName,
        url: `https://github.com/mcuadros/paseo-director/releases/download/v${options.version}/${noticesName}`,
        sha256: noticesSha256,
      },
    };
    writeFileSync(join(temporary, "engine.json"), `${JSON.stringify(metadata, null, 2)}\n`, { mode: 0o400 });
    fsyncPath(join(temporary, "engine.json"));
    fsyncPath(temporary, true);
    renameSync(temporary, options.output);
    fsyncPath(parent, true);
    return { metadata, identity, output: options.output };
  } finally {
    if (existsSync(temporary)) rmSync(temporary, { recursive: true, force: true });
  }
}

const modulePath = fileURLToPath(import.meta.url);
if (process.argv[1] && resolve(process.argv[1]) === resolve(modulePath)) {
  try {
    const result = buildRelease(process.argv.slice(2));
    process.stdout.write(`${JSON.stringify({ schemaVersion: 1, version: result.metadata.version, sourceCandidate: result.metadata.sourceCandidate, binarySha256: result.metadata.binary.sha256, noticesSha256: result.metadata.notices.sha256 })}\n`);
  } catch (error) {
    const code = error && typeof error === "object" && "code" in error ? error.code : "RELEASE_BUILD_FAILED";
    process.stderr.write(`${code}: ${error instanceof Error ? error.message : "release build failed"}\n`);
    process.exitCode = 1;
  }
}
