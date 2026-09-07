// SPDX-License-Identifier: Apache-2.0

import assert from "node:assert/strict";
import {
  mkdtempSync,
  readFileSync,
  rmSync,
  writeFileSync,
} from "node:fs";
import { tmpdir } from "node:os";
import { join, resolve } from "node:path";
import test from "node:test";
import { fileURLToPath } from "node:url";

import {
  generateClient,
  generatedClientMatches,
} from "./contract-check.mjs";

const repositoryRoot = fileURLToPath(new URL("../../", import.meta.url));
const schemaPath = resolve(
  repositoryRoot,
  "engine/contract/host-interface.v1.json",
);

test("the committed client is generated from the exact engine schema", () => {
  assert.equal(generatedClientMatches(repositoryRoot, schemaPath), true);
});

test("generated TypeScript is invariant to schema key order and whitespace", () => {
  const temporaryRoot = mkdtempSync(join(tmpdir(), "director-contract-order-"));
  try {
    const original = JSON.parse(readFileSync(schemaPath, "utf8"));
    const reverseObject = (value) => {
      if (Array.isArray(value)) return value.map(reverseObject);
      if (value === null || typeof value !== "object") return value;
      return Object.fromEntries(
        Object.entries(value)
          .reverse()
          .map(([key, member]) => [key, reverseObject(member)]),
      );
    };
    const reorderedSchema = join(temporaryRoot, "reordered.json");
    writeFileSync(
      reorderedSchema,
      `${JSON.stringify(reverseObject(original), null, 7)}\n`,
    );
    const generated = generateClient(repositoryRoot, reorderedSchema);
    const committed = readFileSync(
      resolve(repositoryRoot, "shared/generated-host-contract.shared.ts"),
    );
    assert.equal(generated.equals(committed), true);
  } finally {
    rmSync(temporaryRoot, { recursive: true, force: true });
  }
});

test("client generation rejects duplicate schema keys", () => {
  const temporaryRoot = mkdtempSync(join(tmpdir(), "director-contract-duplicate-"));
  try {
    const duplicateSchema = join(temporaryRoot, "duplicate.json");
    writeFileSync(
      duplicateSchema,
      readFileSync(schemaPath, "utf8").replace(
        '  "schemaVersion": 1,',
        '  "schemaVersion": 1,\n  "schemaVersion": 1,',
      ),
    );
    assert.throws(
      () => generateClient(repositoryRoot, duplicateSchema),
      /duplicate JSON object key/,
    );
  } finally {
    rmSync(temporaryRoot, { recursive: true, force: true });
  }
});

test("an intentional schema mutation produces detectable drift", () => {
  const temporaryRoot = mkdtempSync(join(tmpdir(), "director-contract-drift-"));
  try {
    const changedSchema = join(temporaryRoot, "changed.json");
    writeFileSync(
      changedSchema,
      readFileSync(schemaPath, "utf8").replace(
        "director-host/v1",
        "director-host/v999",
      ),
    );
    const generated = generateClient(repositoryRoot, changedSchema);
    const committed = readFileSync(
      resolve(repositoryRoot, "shared/generated-host-contract.shared.ts"),
    );
    assert.equal(generated.equals(committed), false);
  } finally {
    rmSync(temporaryRoot, { recursive: true, force: true });
  }
});
