// SPDX-License-Identifier: Apache-2.0

import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import { resolve } from "node:path";
import test from "node:test";

import { shellMetrics } from "../client/shell-layout.client.ts";

test("the host UI shell has distinct compact and wide layouts", () => {
  assert.deepEqual(shellMetrics(true), {
    padding: 16,
    gap: 8,
    boardDirection: "column",
    titleSize: 22,
  });
  assert.deepEqual(shellMetrics(false), {
    padding: 24,
    gap: 12,
    boardDirection: "row",
    titleSize: 28,
  });
});

test("Director for Paseo retains every planned top-level UI shell", () => {
  const entry = readFileSync(resolve("index.ts"), "utf8");
  for (const registration of [
    'addSurface("home", DirectorHome)',
    'id: "project-board"',
    'id: "task-inspector"',
    'id: "open-project-board"',
    'id: "open-task-inspector"',
  ]) {
    assert.ok(entry.includes(registration), `missing ${registration}`);
  }
  const shells = readFileSync(resolve("client/shells.client.tsx"), "utf8");
  for (const token of [
    "Create Project",
    "Adopt Organizer",
    "Board",
    "List",
    "Details",
    "Execution",
    "Activity",
  ]) {
    assert.ok(shells.includes(token), `missing ${token}`);
  }
  assert.doesNotMatch(shells, /#[0-9a-f]{3,8}/i);
});
