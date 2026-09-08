// SPDX-License-Identifier: Apache-2.0

import assert from "node:assert/strict";
import { createHash } from "node:crypto";
import { readFileSync } from "node:fs";
import { resolve } from "node:path";
import test from "node:test";

import { shellMetrics } from "../ui/shell-layout.client.ts";

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
  const shells = readFileSync(resolve("ui/shells.client.tsx"), "utf8");
  for (const token of [
    "Create Project",
    "Adopt Organizer",
    "Board",
    "List",
    "Details",
    "Execution",
    "Activity",
    "Loading project tasks",
    "No tasks yet",
    "Board data is unavailable",
  ]) {
    assert.ok(shells.includes(token), `missing ${token}`);
  }
  assert.doesNotMatch(shells, /#[0-9a-f]{3,8}/i);
  assert.match(shells, /useRpc\(boardSnapshotRpc\)/);
  assert.match(shells, /useQuery/);
  assert.match(shells, /refetchInterval: 2_000/);
  assert.match(shells, /retry: false/);
  assert.match(shells, /accessibilityLiveRegion="polite"/);
  for (const column of ["Task", "Project", "State", "Run"]) {
    assert.ok(shells.includes(`>${column}</Text>`), `missing ${column} List column`);
  }
});

test("the exact Paseo refresh measurement remains bound to ProjectBoard", () => {
  const shells = readFileSync(resolve("ui/shells.client.tsx"));
  const evidence = JSON.parse(
    readFileSync(resolve("docs/evidence/m1.7/refresh-output.json"), "utf8"),
  ) as {
    result: string;
    identity: { projectBoardSourceSha256: string };
    scenes: { cachedRefreshFailurePreservedData: boolean };
    refresh: { configuredMs: number; intervalMs: number };
    providerFailureObserved: boolean;
    queryClientProviderPathExercised: boolean;
  };
  assert.equal(evidence.result, "pass");
  assert.equal(
    createHash("sha256").update(shells).digest("hex"),
    evidence.identity.projectBoardSourceSha256,
  );
  assert.equal(evidence.providerFailureObserved, false);
  assert.equal(evidence.queryClientProviderPathExercised, true);
  assert.equal(evidence.scenes.cachedRefreshFailurePreservedData, true);
  assert.equal(evidence.refresh.configuredMs, 2_000);
  assert.ok(
    evidence.refresh.intervalMs >= 1_900 && evidence.refresh.intervalMs <= 2_200,
    `measured refresh interval ${evidence.refresh.intervalMs}ms is outside tolerance`,
  );
});
