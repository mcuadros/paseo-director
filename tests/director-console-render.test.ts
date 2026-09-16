// SPDX-License-Identifier: Apache-2.0

import assert from "node:assert/strict";
import test from "node:test";

import * as ReactQuery from "@tanstack/react-query";
import React from "react";
import * as JsxRuntime from "react/jsx-runtime";
import TestRenderer, { act } from "react-test-renderer";

import {
  PLANNING_CONTRACT_SHA256,
  PLANNING_CONTRACT_VERSION,
  homeSnapshotSchema,
  type HomeSnapshot,
} from "../generated/planning-contract.shared.ts";
import * as PlanningContract from "../generated/planning-contract.shared.ts";
import * as HomeDiagnosticsRpc from "../rpc/home-diagnostics.shared.ts";
import * as PlanningRpc from "../rpc/planning.shared.ts";
import * as WorkersRpc from "../rpc/workers.shared.ts";
import { DeterministicPlanningFixture } from "./fixtures/planning-fixture.ts";
import { loadClientModule, testAccessibilityInfo } from "./client-module-loader.ts";

const identity = {
  identity: { schemaVersion: 1, id: "host-a", label: "Director" },
  isPending: false,
  isError: false,
  error: null,
};

const theme = {
  colors: {
    accent: "token-accent", accentForeground: "token-accent-foreground",
    border: "token-border", foreground: "token-foreground",
    foregroundMuted: "token-muted", statusDanger: "token-danger",
    statusSuccess: "token-success", statusWarning: "token-warning",
    surface0: "token-surface-0", surface1: "token-surface-1", surface2: "token-surface-2",
  },
};

function MockFlatList(props: Record<string, unknown>) {
  const data = props.data as readonly unknown[];
  const initial = props.initialNumToRender as number | undefined;
  const renderItem = props.renderItem as (input: { item: unknown; index: number }) => React.ReactNode;
  const keyExtractor = props.keyExtractor as ((item: unknown, index: number) => string) | undefined;
  return React.createElement(
    "FlatList",
    props,
    data.slice(0, initial ?? data.length).map((item, index) =>
      React.createElement(
        React.Fragment,
        { key: keyExtractor?.(item, index) ?? String(index) },
        renderItem({ item, index }),
      ),
    ),
  );
}

function MockNativeModal(props: Record<string, unknown>) {
  return props.visible
    ? React.createElement("NativeModal", props, props.children as React.ReactNode)
    : null;
}

type WorkspaceFixture = { id: string; name: string; paseoWorkspaceId: string | null };

function snapshot(workspaces: readonly WorkspaceFixture[]): HomeSnapshot {
  return homeSnapshotSchema.parse({
    schemaVersion: 1,
    contractVersion: PLANNING_CONTRACT_VERSION,
    contractHash: PLANNING_CONTRACT_SHA256,
    cursor: "9",
    page: {
      host: {
        id: "host-a", label: "Exact host", instanceId: "engine-a", state: "current",
        observedAt: "2026-09-16T16:00:00Z", maximumAgeMillis: "30000",
      },
      projects: [{
        id: "project-shared", version: "3", name: "Rendered Project", state: "active", health: "healthy",
        healthReasons: [],
        workspaces: workspaces.map((workspace) => ({
          id: workspace.id, key: "repository", name: workspace.name, health: "healthy",
          paseoWorkspaceId: workspace.paseoWorkspaceId,
        })),
        organizer: {
          id: "organizer-shared", mode: "adopt", phase: "active", configurationState: "current",
          organizerRevision: "a".repeat(40), configurationSha256: "b".repeat(64), paseoWorkspaceId: null,
        },
        lease: { state: "current", epoch: "2", expiresAt: "2026-09-16T16:05:00Z" },
        sync: { state: "current", git: "current", dynamicState: "current", observedAt: "2026-09-16T16:00:00Z" },
        tasks: { open: "2", done: "4", needsYou: "0" },
        activeWork: { building: "1", validating: "0", inReview: "0", ready: "0", total: "1" },
        needsYouReasons: [],
        actions: [{
          kind: "open_board", label: "Board", hostId: "host-a", projectId: "project-shared",
          enabled: true, unavailableReason: null, paseoWorkspaceId: "native-a",
          command: null, emphasis: "primary",
        }],
      }],
      totals: { projects: "1", healthy: "1", degraded: "0", paused: "0", needsYou: "0", activeWork: "1" },
      surfaceActions: [],
      totalProjects: "1",
      nextCursor: null,
    },
  });
}

function worker(agentId: string, taskId: string) {
  return {
    agentId, workspaceId: "wks_execution", title: `Worker ${taskId}`, status: "running" as const,
    taskId, runId: `run-${taskId}`, role: "task-agent" as const, phase: "building",
    candidate: "1".repeat(40), base: "2".repeat(40),
    registeredAt: "2026-09-16T15:59:59Z", startedAt: "2026-09-16T16:00:00Z",
  };
}

type ConsoleHarness = {
  Console: React.ComponentType<Record<string, unknown>>;
  homeRequests: unknown[];
  workerRequests: string[];
};

/**
 * Loads the real Console and every real Director component it hosts. Only the
 * optional host-primitive adapter is stubbed; `tests/host-primitive-degradation`
 * covers the adapter itself against both shipped client builds.
 */
function loadConsole(home: () => Promise<unknown>): ConsoleHarness {
  const homeRequests: unknown[] = [];
  const workerRequests: string[] = [];
  const planning = new DeterministicPlanningFixture();
  const modules = new Map<string, unknown>();
  const reactModule = { ...React, default: React, __esModule: true };
  const requireModule = (specifier: string): unknown => {
    switch (specifier) {
      case "@getpaseo/plugin":
        return {
          usePaseo: () => ({ agents: { subscribe: () => () => {} } }),
          useRpc: (contract: unknown) => {
            if (contract === PlanningRpc.homeQueryRpc) {
              return (input: unknown) => { homeRequests.push(input); return home(); };
            }
            if (contract === WorkersRpc.directorWorkersRpc) {
              return async (input: { rootWorkspaceId: string }) => {
                workerRequests.push(input.rootWorkspaceId);
                return {
                  schemaVersion: 1, rootWorkspaceId: input.rootWorkspaceId,
                  workers: [worker(`agent-${input.rootWorkspaceId}`, "dir-m6.35")],
                };
              };
            }
            if (contract === PlanningRpc.planningQueryRpc) {
              return (input: never) => planning.query(input);
            }
            if (contract === PlanningRpc.planningTaskDetailRpc) {
              return (input: never) => planning.taskDetail(input);
            }
            if (contract === PlanningRpc.planningMutationRpc) {
              return (input: never) => planning.mutate(input);
            }
            return async () => { throw new Error("unexpected Director RPC"); };
          },
        };
      case "@tanstack/react-query": return ReactQuery;
      case "react": return reactModule;
      case "react/jsx-runtime": return JsxRuntime;
      case "react-native":
        return {
          AccessibilityInfo: testAccessibilityInfo,
          ActivityIndicator: "ActivityIndicator",
          FlatList: MockFlatList,
          Modal: MockNativeModal,
          Pressable: "Pressable",
          ScrollView: "ScrollView",
          StyleSheet: { create: (styles: unknown) => styles, hairlineWidth: 1 },
          Text: "Text",
          TextInput: "TextInput",
          View: "View",
          useWindowDimensions: () => ({ width: 1_440, height: 1_000, scale: 1, fontScale: 1 }),
        };
      case "../generated/planning-contract.shared.ts": return PlanningContract;
      case "../rpc/planning.shared.ts": return PlanningRpc;
      case "../rpc/workers.shared.ts": return WorkersRpc;
      case "../rpc/home-diagnostics.shared.ts": return HomeDiagnosticsRpc;
      case "./host-primitives.client.tsx": return { Icon: "Icon" };
      case "./director-host.client.ts": return { useDirectorHostIdentity: () => identity };
      default: {
        if (!specifier.startsWith("./")) throw new Error(`unexpected runtime import ${specifier}`);
        const file = `ui/${specifier.slice(2)}`;
        const cached = modules.get(file);
        if (cached !== undefined) return cached;
        const loaded = loadClientModule<unknown>(file, requireModule);
        modules.set(file, loaded);
        return loaded;
      }
    }
  };
  const module = loadClientModule<{ DirectorConsole: React.ComponentType<Record<string, unknown>> }>(
    "ui/director-console.client.tsx",
    requireModule,
  );
  return { Console: module.DirectorConsole, homeRequests, workerRequests };
}

function renderedText(renderer: TestRenderer.ReactTestRenderer): string {
  return renderer.root
    .findAll((node) => String(node.type) === "Text")
    .flatMap((node) => node.children)
    .filter((value): value is string => typeof value === "string")
    .join(" ");
}

async function waitForText(renderer: TestRenderer.ReactTestRenderer, expected: RegExp): Promise<void> {
  const deadline = Date.now() + 5_000;
  while (Date.now() < deadline) {
    if (expected.test(renderedText(renderer))) return;
    await new Promise((resolve) => setTimeout(resolve, 5));
  }
  assert.match(renderedText(renderer), expected);
}

function tabControl(renderer: TestRenderer.ReactTestRenderer, label: string) {
  return renderer.root.findAll(
    (node) => node.props.accessibilityRole === "tab" && node.props.accessibilityLabel === label,
  )[0]!;
}

function homeCacheKeys(queryClient: ReactQuery.QueryClient): readonly unknown[][] {
  return queryClient
    .getQueryCache()
    .getAll()
    .map((query) => query.queryKey as unknown[])
    .filter((key) => key[0] === "director" && key[1] === "home");
}

async function mountConsole(
  harness: ConsoleHarness,
  props: Record<string, unknown>,
): Promise<{ renderer: TestRenderer.ReactTestRenderer; queryClient: ReactQuery.QueryClient }> {
  const queryClient = new ReactQuery.QueryClient({ defaultOptions: { queries: { retry: false } } });
  let renderer!: TestRenderer.ReactTestRenderer;
  await act(async () => {
    renderer = TestRenderer.create(
      React.createElement(
        ReactQuery.QueryClientProvider,
        { client: queryClient },
        React.createElement(harness.Console, { host: { id: "client-host", label: "Client" }, layout: { compact: false, platform: "web" }, theme, ...props }),
      ),
    );
  });
  return { renderer, queryClient };
}

test("the Console mounts every tab from one Home snapshot query under one cache key", async () => {
  (globalThis as typeof globalThis & { IS_REACT_ACT_ENVIRONMENT: boolean }).IS_REACT_ACT_ENVIRONMENT = true;
  const harness = loadConsole(async () => snapshot([
    { id: "workspace-repository", name: "Repository", paseoWorkspaceId: "native-a" },
    { id: "workspace-checkout", name: "Checkout", paseoWorkspaceId: "native-b" },
  ]));
  const opened: string[] = [];
  const openedAgents: string[] = [];
  const { renderer, queryClient } = await mountConsole(harness, {
    navigation: {
      openAgent: ({ agentId }: { agentId: string }) => openedAgents.push(agentId),
      openWorkspace: ({ workspaceId }: { workspaceId: string }) => opened.push(workspaceId),
    },
  });

  await act(async () => { await waitForText(renderer, /Rendered Project/); });
  // The shell fans Workers out over the same snapshot the Overview body renders.
  // Two consumers, one request, one cache entry.
  assert.equal(harness.homeRequests.length, 1);
  assert.deepEqual(homeCacheKeys(queryClient), [["director", "home", "host-a"]]);
  assert.match(renderedText(renderer), /Overview Board Workers/);
  assert.match(renderedText(renderer), /Project health/);

  // The Console is a new forwarding seam: a host that supplies navigation must
  // reach it through every tab, not only be absent from it. The Overview body's
  // own Board action is the reachable half here; the Workers half is asserted
  // on its own tab below. Disambiguated by role, because the tab strip carries
  // a control with the same accessible name.
  const overviewBoardAction = renderer.root.findAll(
    (node) => node.props.accessibilityRole === "button" && node.props.accessibilityLabel === "Board",
  )[0]!;
  assert.equal(overviewBoardAction.props.accessibilityState.disabled, false);
  await act(async () => { overviewBoardAction.props.onPress(); });
  assert.deepEqual(opened, ["native-a"]);

  await act(async () => { tabControl(renderer, "Board").props.onPress(); });
  await act(async () => { await waitForText(renderer, /Needs you/); });
  assert.doesNotMatch(renderedText(renderer), /Project health/);
  assert.deepEqual(homeCacheKeys(queryClient), [["director", "home", "host-a"]]);

  await act(async () => { tabControl(renderer, "Workers").props.onPress(); });
  await act(async () => { await waitForText(renderer, /Rendered Project · Checkout/); });
  const workers = renderedText(renderer);
  assert.match(workers, /Rendered Project · Repository/);
  assert.match(workers, /Worker dir-m6\.35/);
  // One view per root Workspace, each bound to its own exact native Workspace.
  assert.deepEqual([...harness.workerRequests].sort(), ["native-a", "native-b"]);
  assert.deepEqual(homeCacheKeys(queryClient), [["director", "home", "host-a"]]);

  const openAgent = renderer.root.findAll(
    (node) => node.props.accessibilityLabel === "Open agent for dir-m6.35",
  )[0]!;
  assert.equal(openAgent.props.accessibilityState.disabled, false);
  await act(async () => { openAgent.props.onPress(); });
  // The first Workers view is the first fan-out target, so the agent this
  // reaches also pins which Workspace that view was bound to.
  assert.deepEqual(openedAgents, ["agent-native-a"]);
  assert.deepEqual(opened, ["native-a"]);

  await act(async () => { tabControl(renderer, "Overview").props.onPress(); });
  await act(async () => { await waitForText(renderer, /Project health/); });
  assert.deepEqual(homeCacheKeys(queryClient), [["director", "home", "host-a"]]);

  await act(async () => renderer.unmount());
  queryClient.clear();
});

test("every Console tab renders and states its reason when the host exposes no navigation", async () => {
  (globalThis as typeof globalThis & { IS_REACT_ACT_ENVIRONMENT: boolean }).IS_REACT_ACT_ENVIRONMENT = true;
  const harness = loadConsole(async () => snapshot([
    { id: "workspace-repository", name: "Repository", paseoWorkspaceId: "native-a" },
  ]));
  const { renderer, queryClient } = await mountConsole(harness, { navigation: undefined });

  await act(async () => { await waitForText(renderer, /Rendered Project/); });
  const board = renderer.root.findAll(
    (node) => node.props.accessibilityLabel === "Board" && node.props.accessibilityRole === "button",
  )[0]!;
  assert.equal(board.props.accessibilityState.disabled, true);
  assert.equal(board.props.accessibilityHint, "This Paseo host does not expose native navigation");

  await act(async () => { tabControl(renderer, "Board").props.onPress(); });
  await act(async () => { await waitForText(renderer, /Needs you/); });

  await act(async () => { tabControl(renderer, "Workers").props.onPress(); });
  await act(async () => { await waitForText(renderer, /Rendered Project · Repository/); });
  const openAgent = renderer.root.findAll(
    (node) => node.props.accessibilityLabel === "Open agent for dir-m6.35",
  )[0]!;
  assert.equal(openAgent.props.accessibilityState.disabled, true);
  assert.equal(openAgent.props.accessibilityHint, "Native Paseo agent navigation is unavailable");
  await act(async () => { openAgent.props.onPress?.(); });

  await act(async () => renderer.unmount());
  queryClient.clear();
});

test("the Workers tab states why it cannot render instead of offering a dead tab", async () => {
  (globalThis as typeof globalThis & { IS_REACT_ACT_ENVIRONMENT: boolean }).IS_REACT_ACT_ENVIRONMENT = true;
  const harness = loadConsole(async () => snapshot([
    { id: "workspace-unbound", name: "Unbound", paseoWorkspaceId: null },
  ]));
  const { renderer, queryClient } = await mountConsole(harness, { navigation: undefined });

  await act(async () => { await waitForText(renderer, /Rendered Project/); });
  const workersTab = tabControl(renderer, "Workers");
  assert.equal(workersTab.props.accessibilityState.disabled, true);
  assert.equal(
    workersTab.props.accessibilityHint,
    "No Director Project on this host projects a native Paseo Workspace to read workers from.",
  );
  assert.match(
    renderedText(renderer),
    /Workers\s+·\s+No Director Project on this host projects a native Paseo Workspace to read workers from\./,
  );
  assert.equal(harness.workerRequests.length, 0);

  // Pressing the disabled tab neither navigates nor hides the Overview body.
  await act(async () => { workersTab.props.onPress?.(); });
  assert.match(renderedText(renderer), /Project health/);

  await act(async () => renderer.unmount());
  queryClient.clear();
});

test("the Workers tab states the reason while this host's Project facts are still loading", async () => {
  (globalThis as typeof globalThis & { IS_REACT_ACT_ENVIRONMENT: boolean }).IS_REACT_ACT_ENVIRONMENT = true;
  const harness = loadConsole(() => new Promise(() => {}));
  const { renderer, queryClient } = await mountConsole(harness, { navigation: undefined });

  const workersTab = tabControl(renderer, "Workers");
  assert.equal(workersTab.props.accessibilityState.disabled, true);
  assert.equal(
    workersTab.props.accessibilityHint,
    "Director Workers becomes available once this host's Project facts resolve.",
  );
  assert.match(
    renderedText(renderer),
    /Workers\s+·\s+Director Workers becomes available once this host's Project facts resolve\./,
  );
  assert.equal(harness.workerRequests.length, 0);
  // Overview and Board never wait on that snapshot: both stay reachable.
  assert.equal(tabControl(renderer, "Overview").props.accessibilityState.disabled, false);
  assert.equal(tabControl(renderer, "Board").props.accessibilityState.disabled, false);
  await act(async () => { tabControl(renderer, "Board").props.onPress(); });
  await act(async () => { await waitForText(renderer, /Needs you/); });

  await act(async () => renderer.unmount());
  queryClient.clear();
});
