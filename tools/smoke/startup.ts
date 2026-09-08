// SPDX-License-Identifier: Apache-2.0

import assert from "node:assert/strict";
import { spawnSync } from "node:child_process";
import { mkdtempSync, rmSync } from "node:fs";
import { homedir, tmpdir } from "node:os";
import { dirname, join, resolve } from "node:path";
import { fileURLToPath } from "node:url";

import { resolveEngine } from "../../connector/engine-distribution.server.ts";
import { selectEngine } from "../../connector/engine-selection.server.ts";

const repositoryRoot = resolve(dirname(fileURLToPath(import.meta.url)), "../..");
const temporaryRoot = mkdtempSync(join(tmpdir(), "director-startup-smoke-"));

try {
  const resolved = await resolveEngine(
    selectEngine(
      {
        DIRECTOR_ENGINE_MODE: "development",
        DIRECTOR_ENGINE_SOURCE_ROOT: join(repositoryRoot, "engine"),
        XDG_CACHE_HOME: join(temporaryRoot, "cache"),
        GOMODCACHE: process.env.GOMODCACHE ?? join(homedir(), "go", "pkg", "mod"),
      },
      repositoryRoot,
    ),
  );
  assert.equal(resolved.mode, "development");

  for (const command of ["version", "smoke"] as const) {
    const result = spawnSync(resolved.binaryPath, [command], {
      encoding: "utf8",
      env: {},
    });
    assert.equal(result.status, 0, result.stderr);
    const output = JSON.parse(result.stdout) as Record<string, unknown>;
    if (command === "version") {
      assert.equal(output.name, "director-engine");
      assert.equal(output.buildMode, "development");
      assert.equal(output.productBehavior, true);
    } else {
      assert.equal(output.event, "director-engine.smoke-ready");
      assert.equal(output.productBehavior, true);
    }
  }
  process.stdout.write(
    "Standalone Director Engine startup smoke passed without Paseo or external side effects.\n",
  );
} finally {
  rmSync(temporaryRoot, { recursive: true, force: true });
}
