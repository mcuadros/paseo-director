// SPDX-License-Identifier: Apache-2.0

import assert from "node:assert/strict";
import test from "node:test";

import {
  PLANNING_CONTRACT_SHA256,
  PLANNING_CONTRACT_VERSION,
  homeSnapshotSchema,
  type HomeSnapshot,
} from "../generated/planning-contract.shared.ts";
import {
  directorConsoleTabStates,
  directorConsoleTabs,
  directorConsoleVisibleTab,
  directorConsoleWorkerTargets,
} from "../ui/director-console-model.client.ts";

type ProjectFixture = {
  id: string;
  name: string;
  workspaces: readonly { id: string; name: string; paseoWorkspaceId: string | null }[];
};

function snapshot(projects: readonly ProjectFixture[], cursor = "1"): HomeSnapshot {
  return homeSnapshotSchema.parse({
    schemaVersion: 1,
    contractVersion: PLANNING_CONTRACT_VERSION,
    contractHash: PLANNING_CONTRACT_SHA256,
    cursor,
    page: {
      host: {
        id: "host-a", label: "Exact host", instanceId: "engine-a", state: "current",
        observedAt: "2026-09-16T16:00:00Z", maximumAgeMillis: "30000",
      },
      projects: projects.map((project) => ({
        id: project.id, version: "3", name: project.name, state: "active", health: "healthy",
        healthReasons: [],
        workspaces: project.workspaces.map((workspace) => ({
          id: workspace.id, key: "repository", name: workspace.name, health: "healthy",
          paseoWorkspaceId: workspace.paseoWorkspaceId,
        })),
        organizer: {
          id: `organizer-${project.id}`, mode: "adopt", phase: "active",
          configurationState: "current", organizerRevision: "a".repeat(40),
          configurationSha256: "b".repeat(64), paseoWorkspaceId: null,
        },
        lease: { state: "current", epoch: "2", expiresAt: "2026-09-16T16:05:00Z" },
        sync: { state: "current", git: "current", dynamicState: "current", observedAt: "2026-09-16T16:00:00Z" },
        tasks: { open: "1", done: "1", needsYou: "0" },
        activeWork: { building: "0", validating: "0", inReview: "0", ready: "0", total: "0" },
        needsYouReasons: [],
        actions: [],
      })),
      totals: {
        projects: String(projects.length), healthy: String(projects.length),
        degraded: "0", paused: "0", needsYou: "0", activeWork: "0",
      },
      surfaceActions: [],
      totalProjects: String(projects.length),
      nextCursor: null,
    },
  });
}

test("the Console ships exactly the tabs it can render, in presentation order", () => {
  assert.deepEqual(
    directorConsoleTabs.map((tab) => tab.id),
    ["overview", "board", "workers"],
  );
  assert.deepEqual(
    directorConsoleTabs.map((tab) => tab.label),
    ["Overview", "Board", "Workers"],
  );
  // Administration and Layout ship with their own implementations, so no tab is
  // ever registered before something can render inside it.
  assert.equal(directorConsoleTabs.some((tab) => tab.id === "overview"), true);
  assert.equal(
    directorConsoleTabs.every((tab) => tab.icon !== "" && tab.label !== ""),
    true,
  );
});

test("the Workers fan-out takes one exact native Workspace per root Workspace", () => {
  const targets = directorConsoleWorkerTargets([
    snapshot([
      {
        id: "project-a",
        name: "Director",
        workspaces: [
          { id: "workspace-repository", name: "Repository", paseoWorkspaceId: "native-a" },
          { id: "workspace-docs", name: "Docs", paseoWorkspaceId: "native-b" },
        ],
      },
    ]),
  ]);
  assert.deepEqual(targets, [
    { workspaceId: "native-a", heading: "Director · Repository" },
    { workspaceId: "native-b", heading: "Director · Docs" },
  ]);
});

test("the fan-out skips a Workspace the engine cannot bind to a native Workspace", () => {
  const targets = directorConsoleWorkerTargets([
    snapshot([
      {
        id: "project-a",
        name: "Director",
        workspaces: [
          { id: "workspace-unbound", name: "Unbound", paseoWorkspaceId: null },
          { id: "workspace-repository", name: "Repository", paseoWorkspaceId: "native-a" },
        ],
      },
    ]),
  ]);
  assert.deepEqual(targets, [
    { workspaceId: "native-a", heading: "Director · Repository" },
  ]);
});

test("the fan-out spans every snapshot page and renders one view per native Workspace", () => {
  const targets = directorConsoleWorkerTargets([
    snapshot([
      {
        id: "project-a",
        name: "Director",
        workspaces: [{ id: "workspace-a", name: "Repository", paseoWorkspaceId: "native-a" }],
      },
    ], "1"),
    snapshot([
      {
        id: "project-b",
        name: "Second",
        workspaces: [
          // The same native Workspace, reached through a second Project page.
          { id: "workspace-a", name: "Repository", paseoWorkspaceId: "native-a" },
          { id: "workspace-b", name: "Checkout", paseoWorkspaceId: "native-b" },
        ],
      },
    ], "2"),
  ]);
  assert.deepEqual(targets, [
    { workspaceId: "native-a", heading: "Director · Repository" },
    { workspaceId: "native-b", heading: "Second · Checkout" },
  ]);
});

test("the fan-out is empty before any snapshot page has resolved", () => {
  assert.deepEqual(directorConsoleWorkerTargets(undefined), []);
  assert.deepEqual(directorConsoleWorkerTargets([]), []);
});

test("Overview and Board never depend on a Workspace, and Workers states why it cannot render", () => {
  const resolved = directorConsoleTabStates({
    workerTargets: [{ workspaceId: "native-a", heading: "Director · Repository" }],
    snapshotResolved: true,
  });
  assert.deepEqual(
    resolved.map((tab) => [tab.id, tab.enabled, tab.unavailableReason]),
    [["overview", true, null], ["board", true, null], ["workers", true, null]],
  );

  const pending = directorConsoleTabStates({ workerTargets: [], snapshotResolved: false });
  assert.deepEqual(
    pending.map((tab) => [tab.id, tab.enabled]),
    [["overview", true], ["board", true], ["workers", false]],
  );
  assert.equal(
    pending.find((tab) => tab.id === "workers")?.unavailableReason,
    "Director Workers becomes available once this host's Project facts resolve.",
  );

  const empty = directorConsoleTabStates({ workerTargets: [], snapshotResolved: true });
  assert.equal(
    empty.find((tab) => tab.id === "workers")?.unavailableReason,
    "No Director Project on this host projects a native Paseo Workspace to read workers from.",
  );
  // A tab is never disabled without a stated reason, and never states a reason
  // while it is still reachable.
  for (const tabs of [resolved, pending, empty]) {
    for (const tab of tabs) {
      assert.equal(tab.enabled, tab.unavailableReason === null, `${tab.id} availability must be explicit`);
    }
  }
});

test("the visible tab never rests on a tab that cannot render", () => {
  const pending = directorConsoleTabStates({ workerTargets: [], snapshotResolved: false });
  assert.equal(directorConsoleVisibleTab(pending, "workers"), "overview");
  assert.equal(directorConsoleVisibleTab(pending, "board"), "board");

  const resolved = directorConsoleTabStates({
    workerTargets: [{ workspaceId: "native-a", heading: "Director · Repository" }],
    snapshotResolved: true,
  });
  assert.equal(directorConsoleVisibleTab(resolved, "workers"), "workers");
  assert.equal(directorConsoleVisibleTab(resolved, "overview"), "overview");
});
