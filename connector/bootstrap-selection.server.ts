// SPDX-License-Identifier: Apache-2.0
// Validate the exact prepared Go bootstrap pin; runtime policy stays in Go.

import { createHash } from "node:crypto";
import { lstatSync, readFileSync } from "node:fs";
import { isAbsolute } from "node:path";

const SHA256_PATTERN = /^[0-9a-f]{64}$/u;
const CANDIDATE_PATTERN = /^[0-9a-f]{40}$/u;

export type InstalledBootstrapSelection = {
  readonly channel: "main" | "release";
  readonly connectorCommit: string;
  readonly bootstrap: {
    readonly schemaVersion: 1;
    readonly target: "linux-amd64";
    readonly path: string;
    readonly sha256: string;
    readonly size: number;
  };
};

export class BootstrapSelectionError extends Error {
  readonly code: string;
  constructor(code: string, message: string) { super(message); this.name = "BootstrapSelectionError"; this.code = code; }
}

export function selectInstalledBootstrap(installation: Readonly<Record<string, unknown>>): InstalledBootstrapSelection {
  const value = installation as {
    schemaVersion?: unknown; state?: unknown; connectorCommit?: unknown; channel?: unknown;
    bootstrap?: { schemaVersion?: unknown; target?: unknown; path?: unknown; sha256?: unknown; size?: unknown };
  };
  if (JSON.stringify(Object.keys(installation).sort()) !== JSON.stringify(["bootstrap", "channel", "connectorCommit", "schemaVersion", "state"]) ||
    value.schemaVersion !== 3 || value.state !== "prepared" || !CANDIDATE_PATTERN.test(String(value.connectorCommit ?? "")) ||
    (value.channel !== "main" && value.channel !== "release") || !value.bootstrap ||
    JSON.stringify(Object.keys(value.bootstrap).sort()) !== JSON.stringify(["path", "schemaVersion", "sha256", "size", "target"]) ||
    value.bootstrap.schemaVersion !== 1 || value.bootstrap.target !== "linux-amd64" || !isAbsolute(String(value.bootstrap.path ?? "")) ||
    !SHA256_PATTERN.test(String(value.bootstrap.sha256 ?? "")) || !Number.isSafeInteger(value.bootstrap.size) || Number(value.bootstrap.size) <= 0 || Number(value.bootstrap.size) > 256 * 1024 * 1024) {
    throw new BootstrapSelectionError("DIRECTOR_BOOTSTRAP_INSTALL_NOT_PREPARED", "the connector lacks an exact prepared Go bootstrap pin");
  }
  let status;
  let bytes;
  try { status = lstatSync(String(value.bootstrap.path)); bytes = readFileSync(String(value.bootstrap.path)); }
  catch { throw new BootstrapSelectionError("DIRECTOR_BOOTSTRAP_MISSING", "the exact prepared Go bootstrap is unavailable"); }
  const uid = process.geteuid?.();
  if (!status.isFile() || status.isSymbolicLink() || status.nlink !== 1 || (status.mode & 0o077) !== 0 || (status.mode & 0o100) === 0 ||
    (uid !== undefined && status.uid !== uid) || status.size !== value.bootstrap.size || createHash("sha256").update(bytes).digest("hex") !== value.bootstrap.sha256) {
    throw new BootstrapSelectionError("DIRECTOR_BOOTSTRAP_DIGEST", "the prepared Go bootstrap does not match its install pin");
  }
  return value as InstalledBootstrapSelection;
}
