// SPDX-License-Identifier: Apache-2.0

import assert from "node:assert/strict";
import { spawnSync } from "node:child_process";
import { dirname, resolve } from "node:path";
import { fileURLToPath } from "node:url";

const repositoryRoot = resolve(dirname(fileURLToPath(import.meta.url)), "../..");

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
const bootstrap = spawnSync("go", ["-C", resolve(repositoryRoot, "engine"), "run", "./cmd/director-bootstrap", "version"], {
  encoding: "utf8",
  env: { ...(process.env.PATH ? { PATH: process.env.PATH } : {}), ...(process.env.HOME ? { HOME: process.env.HOME } : {}),
    ...(process.env.GOCACHE ? { GOCACHE: process.env.GOCACHE } : {}), GOTOOLCHAIN: "local", GOWORK: "off" },
});
assert.equal(bootstrap.status, 0, bootstrap.stderr);
assert.equal(JSON.parse(bootstrap.stdout).name, "director-bootstrap");
process.stdout.write(
  "Standalone Engine and Go bootstrap CI smoke passed.\n",
);
