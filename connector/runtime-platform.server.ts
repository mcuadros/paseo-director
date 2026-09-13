// SPDX-License-Identifier: Apache-2.0
// Platform adapter selected before the portable supervisor lifecycle performs effects.

import { readFileSync, realpathSync } from "node:fs";

export type RuntimePlatform = {
  target: "linux-amd64";
  controlEndpointName: "control.sock";
  supervisorEntry: "runtime-supervisor-process.linux.server.mjs";
  processIdentity(pid: number): string;
  processExecutable(pid: number): string;
};

export class RuntimePlatformError extends Error {
  readonly code = "DIRECTOR_PLATFORM_UNSUPPORTED";

  constructor() {
    super("Director runtime has no verified adapter for this platform");
    this.name = "RuntimePlatformError";
  }
}

const linuxAmd64: RuntimePlatform = {
  target: "linux-amd64",
  controlEndpointName: "control.sock",
  supervisorEntry: "runtime-supervisor-process.linux.server.mjs",
  processIdentity(pid) {
    try {
      const stat = readFileSync(`/proc/${pid}/stat`, "utf8");
      return stat.slice(stat.lastIndexOf(")") + 2).split(" ")[19] ?? "";
    } catch {
      return "";
    }
  },
  processExecutable(pid) {
    try {
      return realpathSync(`/proc/${pid}/exe`);
    } catch {
      return "";
    }
  },
};

export function runtimePlatform(
  platform = process.platform,
  architecture = process.arch,
): RuntimePlatform {
  if (platform === "linux" && architecture === "x64") return linuxAmd64;
  throw new RuntimePlatformError();
}
