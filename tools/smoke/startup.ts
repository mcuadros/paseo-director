// SPDX-License-Identifier: Apache-2.0

import assert from "node:assert/strict";
import { spawnSync } from "node:child_process";
import { dirname, resolve } from "node:path";
import { fileURLToPath } from "node:url";

import {
  EngineSelectionError,
  selectEngine,
} from "../../connector/engine-selection.server.ts";

const repositoryRoot = resolve(dirname(fileURLToPath(import.meta.url)), "../..");
assert.throws(
  () => selectEngine({ DIRECTOR_ENGINE_MODE: "development" }, repositoryRoot),
  (error: unknown) =>
    error instanceof EngineSelectionError &&
    error.code === "ENGINE_DEVELOPMENT_DISABLED",
);

for (const command of ["version", "smoke"] as const) {
  const result = spawnSync("go", [
    "-C", resolve(repositoryRoot, "engine"),
    "run", "./cmd/director-engine", command,
  ], {
    encoding: "utf8",
    env: {
      ...(process.env.PATH ? { PATH: process.env.PATH } : {}),
      ...(process.env.HOME ? { HOME: process.env.HOME } : {}),
      ...(process.env.GOCACHE ? { GOCACHE: process.env.GOCACHE } : {}),
      ...(process.env.GOMODCACHE ? { GOMODCACHE: process.env.GOMODCACHE } : {}),
      CGO_ENABLED: "0",
      GOTOOLCHAIN: "local",
      GOWORK: "off",
    },
  });
  assert.equal(result.status, 0, result.stderr);
  const output = JSON.parse(result.stdout) as Record<string, unknown>;
  if (command === "version") {
    assert.equal(output.name, "director-engine");
    assert.equal(output.productBehavior, true);
  } else {
    assert.equal(output.event, "director-engine.smoke-ready");
    assert.equal(output.productBehavior, true);
  }
}
process.stdout.write(
  "Standalone Engine CI smoke passed; installed runtime remains release-only.\n",
);
