import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import { resolve } from "node:path";
import test from "node:test";
import { fileURLToPath } from "node:url";

import {
  checkRelationshipReciprocity,
  checkWorkflowContract,
  decisionRelations,
  findPreProductMarkers,
} from "./pre-product-check.mjs";

const repositoryRoot = fileURLToPath(new URL("../../", import.meta.url));
const workflow = JSON.parse(
  readFileSync(resolve(repositoryRoot, ".github/workflows/ci.yml"), "utf8"),
);

test("the maintained workflow satisfies its contract", () => {
  assert.deepEqual(checkWorkflowContract(workflow), []);
});

test("the pre-product guard rejects product surfaces", () => {
  assert.deepEqual(
    findPreProductMarkers([
      "README.md",
      "package.json",
      "src/index.ts",
      "tests/domain.test.ts",
      "tools/ci/pre-product-check.mjs",
    ]),
    ["package.json", "src/index.ts", "tests/domain.test.ts"],
  );
});

test("relationship validation rejects a missing reciprocal link", () => {
  const decisions = new Map([
    [
      "ADR-0001",
      decisionRelations(
        "ADR-0001",
        "# ADR-0001\n\n- **Amends:** [ADR-0002](0002-example.md)\n",
      ),
    ],
    [
      "ADR-0002",
      decisionRelations("ADR-0002", "# ADR-0002\n\n- **Status:** Accepted\n"),
    ],
  ]);
  assert.deepEqual(checkRelationshipReciprocity(decisions), [
    "ADR-0001: amends ADR-0002 lacks reciprocal amended_by",
  ]);
});

test("workflow validation rejects runner drift and silent skips", () => {
  const changed = structuredClone(workflow);
  changed.jobs.pre_product["runs-on"] = "ubuntu-latest";
  changed.jobs.pre_product.steps[2]["continue-on-error"] = true;
  assert.deepEqual(checkWorkflowContract(changed), [
    "pre_product must use the pinned Linux runner",
    "workflow steps cannot be skipped or allowed to fail",
  ]);
});
