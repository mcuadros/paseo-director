// SPDX-License-Identifier: Apache-2.0
// Exact host compatibility checks run before credentials, engine work, or mutation.

import { spawnSync } from "node:child_process";

export const SUPPORTED_PASEO_VERSION = "0.7.2";
export const SUPPORTED_NODE_MAJOR = 22;
export const SUPPORTED_ENGINE_TARGET = "linux-amd64";

export type HostCompatibility = {
  paseoVersion: typeof SUPPORTED_PASEO_VERSION;
  nodeVersion: string;
  platform: "linux";
  architecture: "x64";
  target: typeof SUPPORTED_ENGINE_TARGET;
};

export type CompatibilityProbe = {
  platform?: string;
  architecture?: string;
  nodeVersion?: string;
  paseoVersion?: () => string;
};

export class HostCompatibilityError extends Error {
  readonly code: string;

  constructor(code: string, message: string) {
    super(message);
    this.name = "HostCompatibilityError";
    this.code = code;
  }
}

function boundedVersion(value: string): string {
  const normalized = value.trim();
  return /^[0-9A-Za-z.+_-]{1,64}$/u.test(normalized)
    ? normalized
    : "unavailable";
}

function installedPaseoVersion(): string {
  const result = spawnSync("paseo", ["--version"], {
    encoding: "utf8",
    env: process.env.PATH ? { PATH: process.env.PATH } : {},
    shell: false,
    timeout: 10_000,
    maxBuffer: 16 * 1024,
  });
  if (result.status !== 0 || (result.error && result.status === null)) {
    throw new HostCompatibilityError(
      "PASEO_VERSION_UNAVAILABLE",
      "exact Paseo compatibility could not be checked",
    );
  }
  return boundedVersion(result.stdout);
}

export function assertHostCompatibility(
  probe: CompatibilityProbe = {},
): HostCompatibility {
  const platform = probe.platform ?? process.platform;
  if (platform !== "linux") {
    throw new HostCompatibilityError(
      "HOST_PLATFORM_UNSUPPORTED",
      "Director 1.0 supports Linux only",
    );
  }
  const architecture = probe.architecture ?? process.arch;
  if (architecture !== "x64") {
    throw new HostCompatibilityError(
      "HOST_TARGET_UNSUPPORTED",
      `Director release target ${SUPPORTED_ENGINE_TARGET} is required`,
    );
  }
  const nodeVersion = boundedVersion(probe.nodeVersion ?? process.versions.node);
  const nodeMajor = Number.parseInt(nodeVersion.split(".", 1)[0] ?? "", 10);
  if (!Number.isSafeInteger(nodeMajor) || nodeMajor < SUPPORTED_NODE_MAJOR) {
    throw new HostCompatibilityError(
      "NODE_VERSION_UNSUPPORTED",
      `Node.js ${SUPPORTED_NODE_MAJOR} or newer is required`,
    );
  }
  const paseoVersion = boundedVersion(
    (probe.paseoVersion ?? installedPaseoVersion)(),
  );
  if (paseoVersion !== SUPPORTED_PASEO_VERSION) {
    throw new HostCompatibilityError(
      "PASEO_VERSION_UNSUPPORTED",
      `Director supports exact Paseo ${SUPPORTED_PASEO_VERSION}; observed ${paseoVersion}`,
    );
  }
  return {
    paseoVersion,
    nodeVersion,
    platform,
    architecture,
    target: SUPPORTED_ENGINE_TARGET,
  };
}
