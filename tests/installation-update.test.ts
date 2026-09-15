// SPDX-License-Identifier: Apache-2.0

import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import test from "node:test";

test("Paseo 0.7.2 rollback uses a bounded idempotent public removal without private identity", () => {
  const document = readFileSync("docs/installation-update.md", "utf8");
  const rollback = document.slice(document.indexOf("## Failure, rollback"), document.indexOf("## Removal"));
  assert.equal(rollback.match(/^paseo plugin remove director --json$/gmu)?.length, 2);
  assert.equal(rollback.match(/^paseo plugin ls --json$/gmu)?.length, 2);
  assert.match(rollback, /remove director --json\n+paseo plugin ls --json\n+# Only if the director entry remains:\n+paseo plugin remove director --json/u);
  assert.match(rollback, /Run the second remove only when the first `plugin ls` still contains\n+`director`/u);
  assert.match(rollback, /Run `add`\n+only when the following `plugin ls` contains no `director` entry/u);
  assert.match(rollback, /do not\n+use a private identifier, edit Paseo state, or delete Director data/u);
  assert.doesNotMatch(rollback, /--id|state\.json|database.*(?:delete|remove)/iu);
});

test("operator diagnostics distinguish config output from data and preserve the deepest codes", () => {
  const document = readFileSync("docs/installation-update.md", "utf8").replaceAll("\n", " ");
  for (const required of [
    "DIRECTOR_TASKSTORE_CONFIG_OUTPUT_MISMATCH",
    "TaskStore/database is not the cause and must not be deleted",
    "Director never overwrites, removes, or silently chooses between the files",
    "ENGINE_HOME_HOST_FACT_REJECTED",
    "field`, `expected`, and `observed",
    "DIRECTOR_BOOTSTRAP_EXTERNAL_OWNER",
    "DIRECTOR_BOOTSTRAP_FOREIGN_OWNER",
    "DIRECTOR_BOOTSTRAP_INSTALL_NOT_PREPARED",
    "DIRECTOR_RUNTIME_CHILDREN_NOT_READY",
    "DIRECTOR_RUNTIME_BINDING_MISMATCH",
  ]) assert.ok(document.includes(required), `missing ${required}`);
  for (const retired of [
    "DIRECTOR_RUNTIME_EXTERNAL_OWNER",
    "ENGINE_INSTALL_NOT_PREPARED",
  ]) assert.ok(!document.includes(retired), `retired ${retired}`);
});
