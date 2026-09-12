#!/usr/bin/env node
// SPDX-License-Identifier: Apache-2.0

import { spawnSync } from "node:child_process";
import { resolve } from "node:path";
import { fileURLToPath } from "node:url";

import { auditDependencyClosure } from "../ci/dependency-audit.mjs";

const SUPPORTED_PASEO_VERSION = "0.7.2";
const MINIMUM_NODE_MAJOR = 22;

function fail(code, message) {
  process.stderr.write(`${code}: ${message}\n`);
  return 1;
}

export function verifyInstall(options = {}) {
  const platform = options.platform ?? process.platform;
  const architecture = options.architecture ?? process.arch;
  const nodeVersion = options.nodeVersion ?? process.versions.node;
  if (platform !== "linux") {
    return { code: "DIRECTOR_INSTALL_PLATFORM_UNSUPPORTED", message: "Director 1.0 supports Linux only" };
  }
  if (architecture !== "x64") {
    return { code: "DIRECTOR_INSTALL_TARGET_UNSUPPORTED", message: "Director release target linux-amd64 is required" };
  }
  const nodeMajor = Number.parseInt(String(nodeVersion).split(".", 1)[0] ?? "", 10);
  if (!Number.isSafeInteger(nodeMajor) || nodeMajor < MINIMUM_NODE_MAJOR) {
    return { code: "DIRECTOR_INSTALL_NODE_UNSUPPORTED", message: `Node.js ${MINIMUM_NODE_MAJOR} or newer is required` };
  }
  const paseo = options.paseoVersion ?? (() => {
    const result = spawnSync("paseo", ["--version"], {
      encoding: "utf8",
      env: process.env.PATH ? { PATH: process.env.PATH } : {},
      shell: false,
      timeout: 10_000,
      maxBuffer: 16 * 1024,
    });
    return result.status !== 0 || (result.error && result.status === null) ? "unavailable" : result.stdout.trim();
  });
  const paseoVersion = String(typeof paseo === "function" ? paseo() : paseo).trim();
  if (paseoVersion !== SUPPORTED_PASEO_VERSION) {
    const observed = /^[0-9A-Za-z.+_-]{1,64}$/u.test(paseoVersion) ? paseoVersion : "unavailable";
    return {
      code: "DIRECTOR_INSTALL_PASEO_UNSUPPORTED",
      message: `Director supports exact Paseo ${SUPPORTED_PASEO_VERSION}; observed ${observed}`,
    };
  }
  const repositoryRoot = options.repositoryRoot ?? resolve(fileURLToPath(new URL("../..", import.meta.url)));
  const audit = auditDependencyClosure(repositoryRoot, { installed: true });
  if (audit.errors.length > 0) {
    return { code: "DIRECTOR_INSTALL_DEPENDENCY_AUDIT", message: audit.errors[0] };
  }
  return { code: "DIRECTOR_INSTALL_READY", message: `exact Paseo ${SUPPORTED_PASEO_VERSION}; dependency ${audit.sha256}` };
}

if (process.argv[1] && resolve(process.argv[1]) === resolve(fileURLToPath(import.meta.url))) {
  const result = verifyInstall();
  if (result.code !== "DIRECTOR_INSTALL_READY") {
    process.exitCode = fail(result.code, result.message);
  } else {
    process.stdout.write(`${result.code}: ${result.message}\n`);
  }
}
