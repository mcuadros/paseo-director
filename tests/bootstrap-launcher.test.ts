// SPDX-License-Identifier: Apache-2.0

import assert from "node:assert/strict";
import { createHash } from "node:crypto";
import { chmodSync, mkdirSync, mkdtempSync, readFileSync, rmSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { join, resolve } from "node:path";
import test from "node:test";

import { ensureBootstrapRuntime } from "../connector/bootstrap-launcher.server.ts";
import { BootstrapSelectionError, selectInstalledBootstrap } from "../connector/bootstrap-selection.server.ts";

function fixture() {
  const root = mkdtempSync(join(tmpdir(), "director-bootstrap-launcher-"));
  chmodSync(root, 0o700);
  const path = join(root, "director-bootstrap");
  const bytes = Buffer.from("exact private Go bootstrap fixture\n");
  writeFileSync(path, bytes, { mode: 0o500 });
  const sha256 = createHash("sha256").update(bytes).digest("hex");
  return {
    root,
    selection: selectInstalledBootstrap({
      schemaVersion: 3, state: "prepared", connectorCommit: "1".repeat(40), channel: "main",
      bootstrap: { schemaVersion: 1, target: "linux-amd64", path, sha256, size: bytes.byteLength },
    }),
  };
}

function output() {
  return JSON.stringify({
    schemaVersion: 1, code: "DIRECTOR_BOOTSTRAP_RUNTIME_STATUS", state: "current", binding: "2".repeat(64),
    host: { schemaVersion: 1, id: `director-${"3".repeat(32)}`, label: "Director" },
    engine: { mode: "main", version: "0.0.0-main", sourceCandidate: "1".repeat(40), target: "linux-amd64",
      binary: { path: "/private/director-engine", sha256: "4".repeat(64), size: 42 },
      notices: { path: "/private/MAIN_BUILD_NOTICES.txt", sha256: "5".repeat(64), size: 43 },
      contractVersion: "director.host/v1", contractSha256: "6".repeat(64) },
    dolt: { version: "2.3.2", target: "linux-amd64", binary: { path: "/private/dolt", sha256: "7".repeat(64), size: 44 }, archiveSha256: "8".repeat(64) },
    enginePid: 101, doltPid: 102, restartCount: 0, projectAdminAuthorization: "9".repeat(64),
  });
}

test("minimal JavaScript launcher executes only the pinned Go bootstrap control command", async () => {
  const value = fixture();
  const calls: Array<{ path: string; args: readonly string[]; env: NodeJS.ProcessEnv }> = [];
  try {
    const handle = await ensureBootstrapRuntime({
      selection: value.selection,
      environment: { HOME: value.root, XDG_RUNTIME_DIR: join(value.root, "runtime"), PATH: "/poison" },
      hostSocket: join(value.root, "host.sock"),
      dependencies: { async invoke(path, args, options) { calls.push({ path, args, env: options.env }); return output(); } },
    });
    assert.equal(handle.host.id, `director-${"3".repeat(32)}`);
    assert.equal(handle.engine.sourceCandidate, "1".repeat(40));
    assert.equal(handle.dolt.version, "2.3.2");
    assert.equal(handle.projectAdminAuthorization(), "9".repeat(64));
    assert.equal((await handle.status()).enginePid, 101);
    assert.equal(calls.length, 2);
    for (const call of calls) {
      assert.equal(call.path, value.selection.bootstrap.path);
      assert.deepEqual(call.args, ["ensure", "--candidate", "1".repeat(40), "--bootstrap-sha256", value.selection.bootstrap.sha256, "--host-socket", join(value.root, "host.sock")]);
      assert.equal(call.env.PATH, undefined);
    }
    await handle.close();
  } finally { rmSync(value.root, { recursive: true, force: true }); }
});

test("missing, mutable, or digest-poisoned bootstrap pins fail before execution", () => {
  const value = fixture();
  try {
    chmodSync(value.selection.bootstrap.path, 0o600);
    writeFileSync(value.selection.bootstrap.path, "poisoned\n", { mode: 0o500 });
    chmodSync(value.selection.bootstrap.path, 0o500);
    assert.throws(() => selectInstalledBootstrap({ schemaVersion: 3, state: "prepared", connectorCommit: "1".repeat(40), channel: "main", bootstrap: value.selection.bootstrap }),
      (error: unknown) => error instanceof BootstrapSelectionError && error.code === "DIRECTOR_BOOTSTRAP_DIGEST");
  } finally { rmSync(value.root, { recursive: true, force: true }); }
});

test("TypeScript contains no Engine/Dolt distribution, child supervision, or recovery implementation", () => {
  const root = resolve(import.meta.dirname, "..");
  const launcher = readFileSync(join(root, "connector", "bootstrap-launcher.server.ts"), "utf8");
  const installer = readFileSync(join(root, "tools", "packaging", "verify-install.mjs"), "utf8");
  const bootstrapBuild = ["bootstrap-build.mjs", "bootstrap-build-core.mjs"]
    .map((file) => readFileSync(join(root, "tools", "packaging", file), "utf8"))
    .join("\n");
  assert.doesNotMatch(launcher, /\b(?:fetch|createServer|createConnection|fork|spawn)\b|sql-server|serve-board|bootstrap-taskstore|SIGKILL|SIGTERM/u);
  assert.doesNotMatch(installer, /director-engine|dolt-linux|sql-server|serve-board|bootstrap-taskstore/u);
  assert.doesNotMatch(bootstrapBuild, /\.\/cmd\/director-engine|sql-server|serve-board|bootstrap-taskstore/u);
  assert.match(bootstrapBuild, /\.\/cmd\/director-bootstrap/u);
});
