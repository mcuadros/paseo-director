// SPDX-License-Identifier: Apache-2.0

import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import test from "node:test";

import {
  PLANNING_CONTRACT_SHA256,
  PLANNING_CONTRACT_VERSION,
  homeSnapshotSchema,
  type HomeAction,
  type HomeProject,
  type HomeSnapshot,
} from "../generated/planning-contract.shared.ts";
import {
  combineHomePages,
  directorHomeScene,
  homeActionEnabled,
  homeProjectKey,
} from "../ui/director-home-model.client.ts";

function unavailable(code = "action_fact_unavailable") {
  return { code, message: "The exact action fact is unavailable", wakeCondition: null, humanActionRequired: false };
}

function action(kind: HomeAction["kind"], hostId: string, projectId: string | null, enabled = true): HomeAction {
  return {
    kind,
    label: kind.replaceAll("_", " "),
    hostId,
    projectId,
    enabled,
    unavailableReason: enabled ? null : unavailable(),
    paseoWorkspaceId: kind === "open_board" || kind === "open_organizer" ? (enabled ? `native-${kind}` : null) : null,
    command: null,
    emphasis: "secondary",
  };
}

function project(hostId: string, id = "project-shared", health: HomeProject["health"] = "healthy"): HomeProject {
  return {
    id,
    version: "7",
    name: `Project on ${hostId}`,
    state: "active",
    health,
    healthReasons: health === "healthy" ? [] : [unavailable("project_sync_unavailable")],
    workspaces: [{ id: "workspace-shared", key: "repository", name: "Repository", health: "healthy", paseoWorkspaceId: "native-workspace" }],
    organizer: {
      id: "organizer-shared",
      mode: "adopt",
      phase: "active",
      configurationState: "current",
      organizerRevision: "a".repeat(40),
      configurationSha256: "b".repeat(64),
      paseoWorkspaceId: "native-organizer",
    },
    lease: { state: "current", epoch: "3", expiresAt: "2026-09-11T16:00:00Z" },
    sync: { state: "current", git: "current", dynamicState: "current", observedAt: "2026-09-11T15:59:30Z" },
    tasks: { open: "4", done: "9", needsYou: "0" },
    activeWork: { building: "1", validating: "1", inReview: "0", ready: "1", total: "3" },
    needsYouReasons: [],
    actions: [action("open_board", hostId, id), action("open_organizer", hostId, id), action("doctor", hostId, id)],
  };
}

function snapshot(input: {
  hostId?: string;
  instanceId?: string;
  cursor?: string;
  projects?: HomeProject[];
  totalProjects?: string;
  nextCursor?: string | null;
  hostState?: HomeSnapshot["page"]["host"]["state"];
} = {}): HomeSnapshot {
  const hostId = input.hostId ?? "host-a";
  const projects = input.projects ?? [project(hostId)];
  const value = {
    schemaVersion: 1,
    contractVersion: PLANNING_CONTRACT_VERSION,
    contractHash: PLANNING_CONTRACT_SHA256,
    cursor: input.cursor ?? "41",
    page: {
      host: {
        id: hostId,
        label: `Host ${hostId}`,
        instanceId: input.instanceId ?? "engine-instance-a",
        state: input.hostState ?? "current",
        observedAt: "2026-09-11T15:59:30Z",
        maximumAgeMillis: "30000",
      },
      projects,
      totals: { projects: input.totalProjects ?? String(projects.length), healthy: String(projects.filter((value) => value.health === "healthy").length), degraded: String(projects.filter((value) => value.health === "degraded").length), paused: "0", needsYou: "0", activeWork: String(projects.length * 3) },
      surfaceActions: [action("create_project", hostId, null), action("adopt_organizer", hostId, null)],
      totalProjects: input.totalProjects ?? String(projects.length),
      nextCursor: input.nextCursor ?? null,
    },
  };
  return homeSnapshotSchema.parse(value);
}

test("Home pages stay bound to one exact host, engine instance, cursor, and Project identity", () => {
  const first = snapshot({ projects: [project("host-a", "project-1")], totalProjects: "2", nextCursor: "next" });
  const second = snapshot({ projects: [project("host-a", "project-2")], totalProjects: "2" });
  const combined = combineHomePages([first, second], "host-a");
  assert.deepEqual(combined.page.projects.map(({ id }) => id), ["project-1", "project-2"]);
  assert.equal(combined.page.nextCursor, null);

  assert.throws(() => combineHomePages([first, snapshot({ hostId: "host-b", projects: [project("host-b", "project-2")], totalProjects: "2" })], "host-a"), /HOME_PAGE_BINDING_MISMATCH/);
  assert.throws(() => combineHomePages([first, snapshot({ instanceId: "engine-restarted", projects: [project("host-a", "project-2")], totalProjects: "2" })], "host-a"), /HOME_PAGE_BINDING_MISMATCH/);
  assert.throws(() => combineHomePages([first, snapshot({ cursor: "42", projects: [project("host-a", "project-2")], totalProjects: "2" })], "host-a"), /HOME_PAGE_BINDING_MISMATCH/);
  assert.throws(() => combineHomePages([first, snapshot({ projects: [project("host-a", "project-1")], totalProjects: "2" })], "host-a"), /HOME_PROJECT_DUPLICATE/);
  assert.notEqual(homeProjectKey("host-a", "project-shared"), homeProjectKey("host-b", "project-shared"));
});

test("Home scene keeps loading, offline, empty, stale, partial-sync, degraded, and current states distinct", () => {
  assert.deepEqual(directorHomeScene({ pages: undefined, expectedHostId: "host-a", isPending: true, isError: false, error: null }), { kind: "loading" });
  assert.deepEqual(directorHomeScene({ pages: undefined, expectedHostId: "host-a", isPending: false, isError: true, error: { code: "ENGINE_HOME_UNAVAILABLE" } }), { kind: "error", code: "offline" });
  assert.equal(directorHomeScene({ pages: [snapshot({ projects: [], totalProjects: "0" })], expectedHostId: "host-a", isPending: false, isError: false, error: null }).kind, "empty");
  assert.equal(directorHomeScene({ pages: [snapshot()], expectedHostId: "host-a", isPending: false, isError: true, error: new Error("offline") }).kind, "stale");
  assert.equal(directorHomeScene({ pages: [snapshot({ hostState: "stale" })], expectedHostId: "host-a", isPending: false, isError: false, error: null }).kind, "stale");
  const partial = project("host-a");
  partial.sync = { ...partial.sync, state: "partial", dynamicState: "failed" };
  assert.equal(directorHomeScene({ pages: [snapshot({ projects: [partial] })], expectedHostId: "host-a", isPending: false, isError: false, error: null }).kind, "partial_sync");
  assert.equal(directorHomeScene({ pages: [snapshot({ projects: [project("host-a", "project-shared", "needs_you")] })], expectedHostId: "host-a", isPending: false, isError: false, error: null }).kind, "needs_you");
  assert.equal(directorHomeScene({ pages: [snapshot({ projects: [project("host-a", "project-shared", "degraded")] })], expectedHostId: "host-a", isPending: false, isError: false, error: null }).kind, "degraded");
  assert.equal(directorHomeScene({ pages: [snapshot()], expectedHostId: "host-a", isPending: false, isError: false, error: null }).kind, "data");
  assert.equal(directorHomeScene({ pages: [snapshot({ hostId: "host-b", projects: [project("host-b")] })], expectedHostId: "host-a", isPending: false, isError: false, error: null }).kind, "error");
});

test("Home actions require exact host, freshness, engine availability, and native navigation", () => {
  const board = action("open_board", "host-a", "project-shared");
  assert.equal(homeActionEnabled({ action: board, expectedHostId: "host-a", stale: false, navigationAvailable: true }), true);
  assert.equal(homeActionEnabled({ action: board, expectedHostId: "host-b", stale: false, navigationAvailable: true }), false);
  assert.equal(homeActionEnabled({ action: board, expectedHostId: "host-a", stale: true, navigationAvailable: true }), false);
  assert.equal(homeActionEnabled({ action: board, expectedHostId: "host-a", stale: false, navigationAvailable: false }), false);
  assert.equal(homeActionEnabled({ action: action("doctor", "host-a", "project-shared", false), expectedHostId: "host-a", stale: false, navigationAvailable: true }), false);

  assert.throws(() => homeSnapshotSchema.parse({
    ...snapshot(),
    page: { ...snapshot().page, surfaceActions: [{ ...action("create_project", "host-a", null, false), unavailableReason: null }] },
  }));
});

test("Director Home uses host-scoped pagination, current-data action gates, tokens, and touch-safe responsive controls", () => {
  const source = readFileSync("ui/director-home.client.tsx", "utf8");
  for (const token of [
    '["director", "home", host.id]',
    "useInfiniteQuery",
    "pageSize: HOME_PAGE_SIZE",
    "refetchOnMount: \"always\"",
    "refetchOnReconnect: true",
    "refetchInterval: 30_000",
    "homeActionEnabled",
    "entry.hostId !== host.id",
    "useEffect",
    "navigation?.openWorkspace({ workspaceId: action.paseoWorkspaceId })",
    "Refresh exact host",
    "Partial sync",
    "Needs your attention",
    "No Projects on this host",
    "Host offline or snapshot stale",
    "Create Project",
    "Adopt Organizer",
    '[snapshot.page.totals.paused, "Paused"]',
    "organizerBootstrapRpc",
    "Apply exact Preview",
    "Nothing is applied without a fresh server-authenticated human confirmation",
    'from "@getpaseo/plugin/react-native"',
    "<Modal",
    "useToast",
    "minHeight: 44",
    "accessibilityLiveRegion=\"polite\"",
  ]) {
    assert.ok(source.includes(token), `missing ${token}`);
  }
  assert.doesNotMatch(source, /#[0-9a-f]{3,8}/iu);
  assert.doesNotMatch(source, /onHover|hover/iu);
  assert.doesNotMatch(source, /@getpaseo\/client/iu);
  assert.doesNotMatch(source, /hostBadge|>Director Home<|\{host\.label\}/u);
});
