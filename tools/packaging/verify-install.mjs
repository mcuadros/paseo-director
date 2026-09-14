#!/usr/bin/env node
// SPDX-License-Identifier: Apache-2.0
// Declared Paseo add/update hook. Runtime policy lives in director-bootstrap.

import { spawnSync } from "node:child_process";
import { randomUUID } from "node:crypto";
import {
  closeSync, constants, fsyncSync, openSync, readFileSync, renameSync, rmSync, writeFileSync,
} from "node:fs";
import { dirname, join, resolve } from "node:path";
import { fileURLToPath } from "node:url";

import { auditDependencyClosure } from "../ci/dependency-audit.mjs";
import { npmLicenseInventory } from "../release/notices.mjs";
import { buildMainBootstrap, cachePublishedBootstrap, runBootstrapPreparation } from "./bootstrap-build.mjs";

const SUPPORTED_PASEO_VERSION = "0.7.2";
const MINIMUM_NODE_MAJOR = 22;
const GIT_SHA_PATTERN = /^[0-9a-f]{40}$/u;

function fail(code, message) { process.stderr.write(`${code}: ${message}\n`); return 1; }

function runningLibc() {
  try {
    const version = process.report.getReport().header.glibcVersionRuntime;
    return typeof version === "string" && version.length > 0 ? "glibc" : "unsupported";
  } catch {
    return "unsupported";
  }
}

export function verifyInstall(options = {}) {
  const platform = options.platform ?? process.platform;
  const architecture = options.architecture ?? process.arch;
  const nodeVersion = options.nodeVersion ?? process.versions.node;
  if (platform !== "linux") return { code: "DIRECTOR_INSTALL_PLATFORM_UNSUPPORTED", message: "Director 1.0 supports Linux only" };
  if (architecture !== "x64") return { code: "DIRECTOR_INSTALL_TARGET_UNSUPPORTED", message: "Director release target linux-amd64 is required" };
  if ((options.libc ?? runningLibc()) !== "glibc") {
    return { code: "DIRECTOR_INSTALL_LIBC_UNSUPPORTED", message: "Director 1.0 supports glibc on Linux amd64 only" };
  }
  const nodeMajor = Number.parseInt(String(nodeVersion).split(".", 1)[0] ?? "", 10);
  if (!Number.isSafeInteger(nodeMajor) || nodeMajor < MINIMUM_NODE_MAJOR) return { code: "DIRECTOR_INSTALL_NODE_UNSUPPORTED", message: `Node.js ${MINIMUM_NODE_MAJOR} or newer is required` };
  const paseo = options.paseoVersion ?? (() => {
    const result = spawnSync("paseo", ["--version"], { encoding: "utf8", env: process.env.PATH ? { PATH: process.env.PATH } : {}, shell: false, timeout: 10_000, maxBuffer: 16 * 1024 });
    return result.status !== 0 || result.error ? "unavailable" : result.stdout.trim();
  });
  const paseoVersion = String(typeof paseo === "function" ? paseo() : paseo).trim();
  if (paseoVersion !== SUPPORTED_PASEO_VERSION) {
    const observed = /^[0-9A-Za-z.+_-]{1,64}$/u.test(paseoVersion) ? paseoVersion : "unavailable";
    return { code: "DIRECTOR_INSTALL_PASEO_UNSUPPORTED", message: `Director supports exact Paseo ${SUPPORTED_PASEO_VERSION}; observed ${observed}` };
  }
  const repositoryRoot = options.repositoryRoot ?? resolve(fileURLToPath(new URL("../..", import.meta.url)));
  const audit = auditDependencyClosure(repositoryRoot, { installed: true });
  if (audit.errors.length > 0) {
    return { code: "DIRECTOR_INSTALL_DEPENDENCY_AUDIT", message: audit.errors[0] };
  }
  try {
    (options.licenseAudit ?? npmLicenseInventory)(repositoryRoot, { installedScope: "production" });
  } catch {
    return { code: "DIRECTOR_INSTALL_LICENSE_AUDIT", message: "locked production license metadata is incomplete or unapproved" };
  }
  return { code: "DIRECTOR_INSTALL_READY", message: `exact Paseo ${SUPPORTED_PASEO_VERSION}; dependency ${audit.sha256}` };
}

function fixedGit(repositoryRoot, args) {
  const result = spawnSync("/usr/bin/git", ["-C", repositoryRoot, ...args], { encoding: "utf8", env: { HOME: process.env.HOME ?? "/", GIT_CONFIG_NOSYSTEM: "1", PATH: "/usr/bin" }, shell: false, timeout: 10_000, maxBuffer: 64 * 1024 });
  if (result.status !== 0 || (result.error && result.status === null)) throw new Error("DIRECTOR_INSTALL_SOURCE_IDENTITY");
  return result.stdout.trim();
}

function readJSON(path, code) {
  try { return JSON.parse(readFileSync(path, "utf8")); } catch { throw new Error(code); }
}

function exactMainCandidate(repositoryRoot) {
  const candidate = fixedGit(repositoryRoot, ["rev-parse", "HEAD"]);
  const status = fixedGit(repositoryRoot, ["status", "--porcelain=v1", "--untracked-files=no"]);
  const generatedOnly = /^(?:M |MM) connector\/install-metadata\.server\.ts$/u.test(status);
  if (!GIT_SHA_PATTERN.test(candidate) || (status !== "" && !generatedOnly)) throw new Error("DIRECTOR_INSTALL_SOURCE_IDENTITY");
  return candidate;
}

export function preparedConnectorMetadataSource(connectorCommit, channel, bootstrap) {
  if (!GIT_SHA_PATTERN.test(connectorCommit) || !["main", "release"].includes(channel) || bootstrap?.schemaVersion !== 1 || bootstrap?.target !== "linux-amd64" ||
    typeof bootstrap?.path !== "string" || !/^[0-9a-f]{64}$/u.test(bootstrap?.sha256 ?? "") || !Number.isSafeInteger(bootstrap?.size)) throw new Error("DIRECTOR_INSTALL_METADATA");
  return [
    "// SPDX-License-Identifier: Apache-2.0",
    "// Generated by the declared Paseo candidate-preparation boundary; do not edit.",
    "",
    `export const INSTALLED_CONNECTOR_METADATA = ${JSON.stringify({ schemaVersion: 3, state: "prepared", connectorCommit, channel, bootstrap }, null, 2)} as const;`,
    "",
  ].join("\n");
}

function writePreparedMetadata(repositoryRoot, source) {
  const target = join(repositoryRoot, "connector", "install-metadata.server.ts");
  if (readFileSync(target, "utf8") === source) return "adopted";
  const temporary = join(dirname(target), `.install-metadata-${randomUUID()}`);
  const descriptor = openSync(temporary, constants.O_WRONLY | constants.O_CREAT | constants.O_EXCL | constants.O_NOFOLLOW, 0o600);
  try { writeFileSync(descriptor, source); fsyncSync(descriptor); } finally { closeSync(descriptor); }
  try {
    renameSync(temporary, target);
    const directory = openSync(dirname(target), constants.O_RDONLY | constants.O_DIRECTORY | constants.O_NOFOLLOW);
    try { fsyncSync(directory); } finally { closeSync(directory); }
  } finally { rmSync(temporary, { force: true }); }
  return "generated";
}

export function prepareCandidateInstallation(options = {}) {
  const repositoryRoot = options.repositoryRoot ?? resolve(fileURLToPath(new URL("../..", import.meta.url)));
  const environment = options.environment ?? process.env;
  const release = readJSON(join(repositoryRoot, "release", "engine.json"), "DIRECTOR_INSTALL_RELEASE_METADATA");
  let candidate;
  let bootstrapPreparation;
  if (release?.schemaVersion === 2 && release?.state === "unpublished" && release?.version === "0.0.0-scaffold" && release?.target === "linux-amd64" && Object.keys(release).length === 4) {
    candidate = exactMainCandidate(repositoryRoot);
    bootstrapPreparation = (options.buildMainBootstrap ?? buildMainBootstrap)({ repositoryRoot, sourceCandidate: candidate, environment, spawnSync: options.spawnSync });
  } else if (release?.schemaVersion === 2 && release?.state === "published" && release?.target === "linux-amd64" && GIT_SHA_PATTERN.test(release?.sourceCandidate ?? "")) {
    candidate = release.sourceCandidate;
    const manifest = readJSON(join(repositoryRoot, "release", "bootstrap-linux-amd64.json"), "DIRECTOR_BOOTSTRAP_RELEASE_MANIFEST");
    if (manifest?.version !== release.version) throw new Error("DIRECTOR_BOOTSTRAP_RELEASE_MANIFEST");
    bootstrapPreparation = (options.cachePublishedBootstrap ?? cachePublishedBootstrap)({ repositoryRoot, sourceCandidate: candidate, manifest, environment });
  } else throw new Error("DIRECTOR_INSTALL_RELEASE_METADATA");
  const result = (options.runBootstrapPreparation ?? runBootstrapPreparation)({ repositoryRoot, sourceCandidate: candidate, bootstrap: bootstrapPreparation.metadata, environment, spawnSync: options.spawnSync });
  if (result.channel !== (release.state === "unpublished" ? "main" : "release")) throw new Error("DIRECTOR_BOOTSTRAP_CHANNEL_INVALID");
  const metadataState = writePreparedMetadata(repositoryRoot, preparedConnectorMetadataSource(candidate, result.channel, bootstrapPreparation.metadata));
  return { code: "DIRECTOR_INSTALL_CANDIDATE_READY", channel: result.channel, metadataState, bootstrapCacheState: bootstrapPreparation.cacheState,
    bootstrapCompilerInvocations: bootstrapPreparation.compilerInvocations, engineCompilerInvocations: result.engineBuilds };
}

if (process.argv[1] && resolve(process.argv[1]) === resolve(fileURLToPath(import.meta.url))) {
  const verified = verifyInstall();
  if (verified.code !== "DIRECTOR_INSTALL_READY") process.exitCode = fail(verified.code, verified.message);
  else {
    try { process.stdout.write(`${verified.code}: ${verified.message}; ${JSON.stringify(prepareCandidateInstallation())}\n`); }
    catch (error) {
      const candidate = error && typeof error === "object" ? Reflect.get(error, "code") ?? Reflect.get(error, "message") : "";
      const code = typeof candidate === "string" && /^[A-Z0-9_]{3,96}$/u.test(candidate) ? candidate : "DIRECTOR_INSTALL_CANDIDATE_PREPARATION";
      process.exitCode = fail(code, code.startsWith("DIRECTOR_MAIN_GO_TOOLCHAIN_") ? "Exact fixed-system Go go1.26.5 for Linux amd64 is required to prepare the unpublished main channel" : "Director Go bootstrap candidate preparation failed; the prior installed Candidate remains unchanged");
    }
  }
}
