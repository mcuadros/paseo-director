// SPDX-License-Identifier: Apache-2.0
// Paseo adapter for the authenticated Go bootstrap control command. It owns no runtime policy.

import { execFile } from "node:child_process";
import { homedir } from "node:os";
import { isAbsolute } from "node:path";

import type { InstalledBootstrapSelection } from "./bootstrap-selection.server.ts";

const SHA256_PATTERN = /^[0-9a-f]{64}$/u;
const HOST_ID_PATTERN = /^director-[0-9a-f]{32}$/u;
const HEARTBEAT_MILLIS = 5_000;

export type DirectorHostIdentity = { readonly schemaVersion: 1; readonly id: string; readonly label: "Director" };
export type ResolvedEngine = {
  readonly mode: "main" | "release"; readonly version: string; readonly sourceCandidate: string; readonly target: "linux-amd64";
  readonly binaryPath: string; readonly noticesPath: string; readonly binarySha256: string; readonly noticesSha256: string;
  readonly connectorCommit: string; readonly contractVersion: string; readonly contractSha256: string;
};
export type ResolvedDolt = { readonly version: "2.3.2"; readonly target: "linux-amd64"; readonly binaryPath: string; readonly binarySha256: string; readonly archiveSha256: string };
export type BootstrapRuntimeStatus = { readonly state: "current" | "degraded"; readonly binding: string; readonly enginePid: number; readonly doltPid: number; readonly restartCount: number };
export type BootstrapRuntimeHandle = {
  readonly binding: string; readonly host: DirectorHostIdentity; readonly engine: ResolvedEngine; readonly dolt: ResolvedDolt;
  projectAdminAuthorization(): string; status(): Promise<BootstrapRuntimeStatus>; close(): Promise<void>;
};

export class BootstrapLauncherError extends Error {
  readonly code: string;
  constructor(code: string, message: string) { super(message); this.name = "BootstrapLauncherError"; this.code = code; }
}

type BootstrapOutput = {
  schemaVersion: 1; code: string; state: "current" | "degraded"; binding: string; host: DirectorHostIdentity;
  engine: { mode: "main" | "release"; version: string; sourceCandidate: string; target: "linux-amd64"; binary: { path: string; sha256: string; size: number }; notices: { path: string; sha256: string; size: number }; contractVersion: string; contractSha256: string };
  dolt: { version: "2.3.2"; target: "linux-amd64"; binary: { path: string; sha256: string; size: number }; archiveSha256: string };
  enginePid: number; doltPid: number; restartCount: number; projectAdminAuthorization: string;
};

export type BootstrapLauncherDependencies = {
  invoke?: (path: string, args: readonly string[], options: { readonly env: NodeJS.ProcessEnv; readonly timeout: number }) => Promise<string>;
};

function closedEnvironment(environment: NodeJS.ProcessEnv): NodeJS.ProcessEnv {
  const selected: NodeJS.ProcessEnv = { HOME: environment.HOME && isAbsolute(environment.HOME) ? environment.HOME : homedir() };
  for (const name of ["XDG_RUNTIME_DIR", "XDG_CACHE_HOME", "XDG_CONFIG_HOME", "XDG_DATA_HOME"] as const) {
    const value = environment[name];
    if (value !== undefined) {
      if (!isAbsolute(value)) throw new BootstrapLauncherError("DIRECTOR_BOOTSTRAP_XDG_PATH", "Director bootstrap XDG paths must be absolute");
      selected[name] = value;
    }
  }
  return selected;
}

function invokeBootstrap(path: string, args: readonly string[], options: { readonly env: NodeJS.ProcessEnv; readonly timeout: number }): Promise<string> {
  return new Promise((accept, reject) => {
    execFile(path, [...args], { encoding: "utf8", env: options.env, shell: false, timeout: options.timeout, maxBuffer: 128 * 1024 }, (error, stdout, stderr) => {
      if (error) {
        const code = /(?:^|\n)([A-Z][A-Z0-9_]{2,95})(?:\n|$)/u.exec(String(stderr))?.[1] ?? "DIRECTOR_BOOTSTRAP_LAUNCH_FAILED";
        reject(new BootstrapLauncherError(code, "the Director Go bootstrap refused runtime startup"));
      } else accept(String(stdout).trim());
    });
  });
}

function parseOutput(raw: string, selection: InstalledBootstrapSelection): BootstrapOutput {
  let value: BootstrapOutput;
  try { value = JSON.parse(raw) as BootstrapOutput; } catch { throw new BootstrapLauncherError("DIRECTOR_BOOTSTRAP_CONTROL_INVALID", "the Director Go bootstrap returned invalid status"); }
  if (value.schemaVersion !== 1 || value.code !== "DIRECTOR_BOOTSTRAP_RUNTIME_STATUS" || (value.state !== "current" && value.state !== "degraded") ||
    !SHA256_PATTERN.test(value.binding ?? "") || value.host?.schemaVersion !== 1 || !HOST_ID_PATTERN.test(value.host?.id ?? "") || value.host?.label !== "Director" ||
    value.engine?.mode !== selection.channel || value.engine?.sourceCandidate !== selection.connectorCommit || value.engine?.target !== "linux-amd64" ||
    !isAbsolute(value.engine?.binary?.path ?? "") || !SHA256_PATTERN.test(value.engine?.binary?.sha256 ?? "") || !isAbsolute(value.engine?.notices?.path ?? "") ||
    !SHA256_PATTERN.test(value.engine?.notices?.sha256 ?? "") || !SHA256_PATTERN.test(value.engine?.contractSha256 ?? "") || value.dolt?.version !== "2.3.2" ||
    value.dolt?.target !== "linux-amd64" || !isAbsolute(value.dolt?.binary?.path ?? "") || !SHA256_PATTERN.test(value.dolt?.binary?.sha256 ?? "") ||
    !SHA256_PATTERN.test(value.dolt?.archiveSha256 ?? "") || !Number.isSafeInteger(value.enginePid) || value.enginePid <= 0 || !Number.isSafeInteger(value.doltPid) || value.doltPid <= 0 ||
    value.enginePid === value.doltPid || !SHA256_PATTERN.test(value.projectAdminAuthorization ?? "")) {
    throw new BootstrapLauncherError("DIRECTOR_BOOTSTRAP_CONTROL_INVALID", "the Director Go bootstrap status identity is invalid");
  }
  return value;
}

export async function ensureBootstrapRuntime(options: {
  readonly selection: InstalledBootstrapSelection; readonly environment: NodeJS.ProcessEnv; readonly hostSocket: string; readonly dependencies?: BootstrapLauncherDependencies;
}): Promise<BootstrapRuntimeHandle> {
  if (!isAbsolute(options.hostSocket)) throw new BootstrapLauncherError("DIRECTOR_BOOTSTRAP_HOST_SOCKET", "the Director host socket must be absolute");
  const invoke = options.dependencies?.invoke ?? invokeBootstrap;
  const args = ["ensure", "--candidate", options.selection.connectorCommit, "--bootstrap-sha256", options.selection.bootstrap.sha256, "--host-socket", options.hostSocket] as const;
  const commandOptions = { env: closedEnvironment(options.environment), timeout: 130_000 } as const;
  const initial = parseOutput(await invoke(options.selection.bootstrap.path, args, commandOptions), options.selection);
  let latest = initial;
  let closed = false;
  let inflight: Promise<BootstrapOutput> | null = null;
  const refresh = () => {
    if (closed) return Promise.reject(new BootstrapLauncherError("DIRECTOR_BOOTSTRAP_HANDLE_CLOSED", "the Director bootstrap handle is closed"));
    inflight ??= invoke(options.selection.bootstrap.path, args, commandOptions).then((raw) => parseOutput(raw, options.selection)).then((value) => { latest = value; return value; }).finally(() => { inflight = null; });
    return inflight;
  };
  const heartbeat = setInterval(() => { void refresh().catch(() => undefined); }, HEARTBEAT_MILLIS);
  heartbeat.unref();
  return {
    binding: initial.binding, host: initial.host,
    engine: { mode: initial.engine.mode, version: initial.engine.version, sourceCandidate: initial.engine.sourceCandidate, target: "linux-amd64",
      binaryPath: initial.engine.binary.path, noticesPath: initial.engine.notices.path, binarySha256: initial.engine.binary.sha256,
      noticesSha256: initial.engine.notices.sha256, connectorCommit: options.selection.connectorCommit,
      contractVersion: initial.engine.contractVersion, contractSha256: initial.engine.contractSha256 },
    dolt: { version: "2.3.2", target: "linux-amd64", binaryPath: initial.dolt.binary.path, binarySha256: initial.dolt.binary.sha256, archiveSha256: initial.dolt.archiveSha256 },
    projectAdminAuthorization() { return latest.projectAdminAuthorization; },
    async status() { const value = await refresh(); return { state: value.state, binding: value.binding, enginePid: value.enginePid, doltPid: value.doltPid, restartCount: value.restartCount }; },
    async close() { if (!closed) { closed = true; clearInterval(heartbeat); } },
  };
}
