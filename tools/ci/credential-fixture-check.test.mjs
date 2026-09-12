// SPDX-License-Identifier: Apache-2.0

import assert from "node:assert/strict";
import test from "node:test";

import { scanText, verifyRepositoryBoundary } from "./credential-fixture-check.mjs";

const assemble = (...parts) => parts.join("");
const fixtures = [
  ["github-fine-grained-pat", assemble("github", "_pat_", "01234567", "89abcdef", "ghijklmn", "op")],
  ["github-legacy-token", assemble("g", "hp_", "01234567", "89abcdef", "ghijklmn", "op")],
  ["openai-api-key", assemble("s", "k-proj-", "01234567", "89abcdef", "ghijklmn", "op")],
  ["aws-access-key-id", assemble("AK", "IA", "01234567", "89ABCDEF")],
  ["aws-access-key-id", assemble("AS", "IA", "01234567", "89ABCDEF")],
  ["gitlab-access-token", assemble("gl", "pat-", "01234567", "89abcdef", "ghijklmn", "op")],
  ["npm-access-token", assemble("n", "pm_", "01234567", "89abcdef", "ghijklmn", "op")],
  ["slack-token", assemble("xo", "xb-", "0123456789", "-", "abcdefgh", "ijklmnop")],
  ["google-api-key", assemble("AI", "za", "01234567", "89abcdef", "ghijklmn", "opqrstuv", "w")],
  ["jwt", assemble("e", "yJabcdefghijk", ".", "abcdefghijkl", ".", "abcdefghijkl")],
  ["private-key-header", assemble("-----", "BEGIN OPENSSH ", "PRIVATE ", "KEY", "-----")],
];

test("provider-shaped runtime canaries are rejected without disclosing their bytes", () => {
  for (const [id, fixture] of fixtures) {
    assert.deepEqual(scanText(`const candidate = ${JSON.stringify(fixture)};`), [id]);
    const mutation = id === "private-key-header" ? fixture.replace("OPENSSH", "RSA") : `${fixture.slice(0, -1)}Z`;
    assert.deepEqual(scanText(`prefix:${mutation}:suffix`), [id]);
  }
});

test("separated source fragments remain publishable", () => {
  const fragmentSource = fixtures.map(([, fixture]) => fixture)
    .flatMap((fixture) => [fixture.slice(0, 2), fixture.slice(2, 8), fixture.slice(8)])
    .join("\n");
  assert.deepEqual(scanText(fragmentSource), []);
});

test("the candidate repository contains no contiguous provider credential fixture", () => {
  assert.deepEqual(verifyRepositoryBoundary(), []);
});
