// SPDX-License-Identifier: Apache-2.0

import assert from "node:assert/strict";
import { mkdirSync, mkdtempSync, rmSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import test from "node:test";

import { consoleCommandErrors, fencedBlockErrors, localLinkErrors } from "./check.mjs";

test("public console examples reject daemon and machine restart remedies", () => {
  assert.deepEqual(consoleCommandErrors("guide.md", "paseo plugin reload director --json"), []);
  assert.deepEqual(consoleCommandErrors("guide.md", "director-engine serve-board \\\n+    --listen 127.0.0.1:7041 \\\n+    --taskstore-config /example/config.json \\\n+    --host-id host-a --host-label Example"), []);
  assert.match(consoleCommandErrors("guide.md", "paseo daemon restart")[0], /forbidden restart/u);
  assert.match(consoleCommandErrors("guide.md", "reboot")[0], /forbidden restart/u);
});

test("public fenced JSON and npm commands remain machine checked", () => {
  const packageJSON = { scripts: { "docs:check": "node check.mjs" } };
  assert.deepEqual(fencedBlockErrors("guide.md", "```json\n{\"ok\":true}\n```\n```console\nnpm run docs:check\n```", packageJSON), []);
  assert.match(fencedBlockErrors("guide.md", "```json\n{broken}\n```", packageJSON)[0], /valid JSON/u);
});

test("local links reject missing files and repository escapes", () => {
  const root = mkdtempSync(join(tmpdir(), "director-docs-check-"));
  try {
    mkdirSync(join(root, "docs"));
    writeFileSync(join(root, "docs", "target.md"), "# Target\n");
    assert.deepEqual(localLinkErrors(root, "docs/source.md", "[target](target.md#target)"), []);
    assert.match(localLinkErrors(root, "docs/source.md", "[missing](missing.md)")[0], /missing local link/u);
    assert.match(localLinkErrors(root, "docs/source.md", "[escape](../../outside.md)")[0], /escapes the repository/u);
  } finally {
    rmSync(root, { recursive: true, force: true });
  }
});
