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

type Component = React.ComponentType<Record<string, unknown>>;

type ConsoleHarness = {
  Console: Component;
  /** The workspace panels the Console did not replace, loaded from the same
   *  real module graph, so the wrappers this Task introduced are mounted here
   *  rather than only typechecked. */
  DirectorWorkers: Component;
  ProjectBoard: Component;
  announcements: string[];
  homeRequests: unknown[];
  workerRequests: string[];
};

/**
 * Loads the real Console and every real Director component it hosts. Only the
 * optional host-primitive adapter is stubbed; `tests/host-primitive-degradation`
 * covers the adapter itself against both shipped client builds.
 */
function loadConsole(
  home: () => Promise<unknown>,
  { highContrast = false }: { highContrast?: boolean } = {},
): ConsoleHarness {
  const announcements: string[] = [];
  const homeRequests: unknown[] = [];
  const workerRequests: string[] = [];
  // Recording rather than inert: the shared source-level check in
  // tests/mobile-accessibility.test.ts is satisfied by the import alone, so the
  // Console's announcement is pinned here by what it does.
  const accessibilityInfo = {
    ...testAccessibilityInfo,
    async isHighTextContrastEnabled() { return highContrast; },
    announceForAccessibility(message: string) { announcements.push(message); },
    announceForAccessibilityWithOptions(message: string) { announcements.push(message); },
  };
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
          AccessibilityInfo: accessibilityInfo,
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
  const module = loadClientModule<{ DirectorConsole: Component }>(
    "ui/director-console.client.tsx",
    requireModule,
  );
  // Reached through the same cache the Console populated, so the panel wrappers
  // and the views they forward to are one module instance, not two.
  const workersModule = requireModule("./director-workers-panel.client.tsx") as { DirectorWorkers: Component };
  const boardModule = requireModule("./planning-surface.client.tsx") as { ProjectBoard: Component };
  return {
    Console: module.DirectorConsole,
    DirectorWorkers: workersModule.DirectorWorkers,
    ProjectBoard: boardModule.ProjectBoard,
    announcements,
    homeRequests,
    workerRequests,
  };
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

async function mount(
  Component: Component,
  props: Record<string, unknown>,
): Promise<{ renderer: TestRenderer.ReactTestRenderer; queryClient: ReactQuery.QueryClient }> {
  const queryClient = new ReactQuery.QueryClient({ defaultOptions: { queries: { retry: false } } });
  let renderer!: TestRenderer.ReactTestRenderer;
  await act(async () => {
    renderer = TestRenderer.create(
      React.createElement(
        ReactQuery.QueryClientProvider,
        { client: queryClient },
        React.createElement(Component, { host: { id: "client-host", label: "Client" }, layout: { compact: false, platform: "web" }, theme, ...props }),
      ),
    );
  });
  return { renderer, queryClient };
}

function mountConsole(
  harness: ConsoleHarness,
  props: Record<string, unknown>,
): Promise<{ renderer: TestRenderer.ReactTestRenderer; queryClient: ReactQuery.QueryClient }> {
  return mount(harness.Console, props);
}

/** Reads one flattened style value off the Text node carrying `text`. The
 *  hosted bodies size themselves from the layout prop the Console forwards, so
 *  this is where a forwarded layout becomes observable. */
function textStyleValue(
  renderer: TestRenderer.ReactTestRenderer,
  text: string,
  key: string,
): unknown {
  const node = renderer.root.findAll(
    (item) => String(item.type) === "Text" && item.children.join("") === text,
  )[0]!;
  const styles = [node.props.style].flat(3) as (Record<string, unknown> | null | undefined)[];
  return styles.filter(Boolean).map((entry) => entry![key]).findLast((value) => value !== undefined);
}

/**
 * Waits for a node rather than for text that happens to precede it. Waiting on
 * rendered text and then querying immediately is a race: under full-suite load
 * the text of a Board card appears before the pressable that carries its
 * accessible name, and the query then reads undefined.
 */
async function waitForNode(
  renderer: TestRenderer.ReactTestRenderer,
  describe: string,
  match: (node: TestRenderer.ReactTestInstance) => boolean,
): Promise<TestRenderer.ReactTestInstance> {
  const deadline = Date.now() + 5_000;
  while (Date.now() < deadline) {
    const found = renderer.root.findAll(match)[0];
    if (found) return found;
    await new Promise((resolve) => setTimeout(resolve, 5));
  }
  assert.fail(`${describe} never rendered`);
}

function waitForLabel(renderer: TestRenderer.ReactTestRenderer, label: string) {
  return waitForNode(renderer, label, (node) => node.props.accessibilityLabel === label);
}

/** Opens the first Board card and its Task modal, which is where the Board's
 *  navigation prop becomes observable. */
async function openFirstBoardTask(renderer: TestRenderer.ReactTestRenderer): Promise<void> {
  let card!: TestRenderer.ReactTestInstance;
  await act(async () => {
    card = await waitForNode(renderer, "the DIR-00001 Board card", (node) =>
      typeof node.props.accessibilityLabel === "string" &&
      node.props.accessibilityLabel.startsWith("DIR-00001,"));
  });
  await act(async () => { card.props.onPress(); });
  await act(async () => { await waitForText(renderer, /Contract-first planning UI shell/); });
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
  // and it carries the exact host the shared hook was given, not a default.
  assert.deepEqual(harness.homeRequests, [{ hostId: "host-a", cursor: null, pageSize: 25 }]);
  assert.deepEqual(homeCacheKeys(queryClient), [["director", "home", "host-a"]]);
  assert.match(renderedText(renderer), /Overview Board Workers/);
  assert.match(renderedText(renderer), /Project health/);
  // The Overview body sizes itself from the layout the Console forwards: 20 is
  // the wide heading, 18 the compact one. A hard-coded layout changes this.
  assert.equal(textStyleValue(renderer, "Project health", "fontSize"), 20);
  // Which tab is shown is the Console's only state, so a screen reader is told
  // when it changes. Asserted on the announcement, not on an import.
  assert.equal(harness.announcements.includes("Director Overview tab"), true);
  // Which tab is selected is stated to assistive technology, not only drawn.
  assert.deepEqual(
    ["Overview", "Board", "Workers"].map((label) => tabControl(renderer, label).props.accessibilityState.selected),
    [true, false, false],
  );

  // The Console forwards navigation through three tab branches, and each is
  // asserted separately: dropping all three at once dies through the Overview
  // and hides the other two. This is the Overview branch; the Workers branch is
  // asserted on its own tab below and the Board branch in its own test.
  // Disambiguated by role, because the tab strip carries a control with the
  // same accessible name.
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
  assert.equal(harness.announcements.includes("Director Board tab"), true);
  // A wide Board shows every applicable lane; a compact one shows a single
  // lane at a time. Two lanes together therefore pin the forwarded layout.
  const boardLanes = renderedText(renderer);
  assert.match(boardLanes, /Queued/);
  assert.match(boardLanes, /Building/);
  assert.deepEqual(
    ["Overview", "Board", "Workers"].map((label) => tabControl(renderer, label).props.accessibilityState.selected),
    [false, true, false],
  );

  await act(async () => { tabControl(renderer, "Workers").props.onPress(); });
  await act(async () => { await waitForText(renderer, /Rendered Project · Checkout/); });
  const workers = renderedText(renderer);
  assert.match(workers, /Rendered Project · Repository/);
  assert.match(workers, /Worker dir-m6\.35/);
  // 28 is shellMetrics' wide title, 22 the compact one.
  assert.equal(textStyleValue(renderer, "Rendered Project · Repository", "fontSize"), 28);
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
  // The announcement follows the tab actually shown, not the one requested, so
  // a redirected request never names a tab the user is not looking at.
  assert.equal(harness.announcements.includes("Director Workers tab"), false);
  assert.equal(harness.announcements.includes("Director Overview tab"), true);
  assert.equal(tabControl(renderer, "Overview").props.accessibilityState.selected, true);

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

test("the Board tab reaches the host's navigation, the branch the other two hide", async () => {
  (globalThis as typeof globalThis & { IS_REACT_ACT_ENVIRONMENT: boolean }).IS_REACT_ACT_ENVIRONMENT = true;
  const harness = loadConsole(async () => snapshot([
    { id: "workspace-repository", name: "Repository", paseoWorkspaceId: "native-a" },
  ]));
  const openedAgents: string[] = [];
  const { renderer, queryClient } = await mountConsole(harness, {
    navigation: { openAgent: ({ agentId }: { agentId: string }) => openedAgents.push(agentId), openWorkspace() {} },
  });

  await act(async () => { await waitForText(renderer, /Rendered Project/); });
  await act(async () => { tabControl(renderer, "Board").props.onPress(); });
  await act(async () => { await waitForText(renderer, /Needs you/); });
  await openFirstBoardTask(renderer);

  // navigation reaches the Board through ProjectBoardSurface and PlanningSurface
  // and gates this control in the Task modal. Without it the label itself
  // changes, so the assertion fails on an absent control rather than on a
  // silent no-op.
  let openAgent!: TestRenderer.ReactTestInstance;
  await act(async () => { openAgent = await waitForLabel(renderer, "Open agent for DIR-00001"); });
  assert.equal(openAgent.props.accessibilityState.disabled, false);
  await act(async () => { openAgent.props.onPress(); });
  assert.deepEqual(openedAgents, ["paseo-agent-task-0"]);

  await act(async () => renderer.unmount());
  queryClient.clear();
});

test("the workspace panel wrappers forward every prop to the views the Console shares", async () => {
  (globalThis as typeof globalThis & { IS_REACT_ACT_ENVIRONMENT: boolean }).IS_REACT_ACT_ENVIRONMENT = true;
  const harness = loadConsole(async () => snapshot([]));
  const openedAgents: string[] = [];
  const navigation = {
    openAgent: ({ agentId }: { agentId: string }) => openedAgents.push(agentId),
    openWorkspace() {},
  };

  // DirectorWorkers became a pure forwarder in this Task. Nothing mounted it
  // before, so workspaceId, navigation and the heading fallback were carried by
  // the type checker alone.
  const workers = await mount(harness.DirectorWorkers, {
    context: "workspace",
    navigation,
    workspaceId: "wks_panel_root",
  });
  await act(async () => { await waitForText(workers.renderer, /Worker dir-m6\.35/); });
  // workspaceId reaches the aggregate RPC, rather than an empty string.
  assert.deepEqual(harness.workerRequests, ["wks_panel_root"]);
  // The heading fallback is what gives the panel its own title; the Console
  // overrides it per root Workspace and the panel must not inherit that.
  assert.match(renderedText(workers.renderer), /Director Workers/);
  assert.doesNotMatch(renderedText(workers.renderer), /·/);
  // The panel sizes itself from the layout its wrapper forwards: 28 wide, 22
  // compact.
  assert.equal(textStyleValue(workers.renderer, "Director Workers", "fontSize"), 28);
  let panelOpenAgent!: TestRenderer.ReactTestInstance;
  await act(async () => { panelOpenAgent = await waitForLabel(workers.renderer, "Open agent for dir-m6.35"); });
  assert.equal(panelOpenAgent.props.accessibilityState.disabled, false);
  await act(async () => { panelOpenAgent.props.onPress(); });
  assert.deepEqual(openedAgents, ["agent-wks_panel_root"]);
  await act(async () => workers.renderer.unmount());
  workers.queryClient.clear();

  // ProjectBoard became a pure forwarder in the same way.
  const board = await mount(harness.ProjectBoard, {
    context: "workspace",
    navigation,
    workspaceId: "wks_panel_root",
  });
  await act(async () => { await waitForText(board.renderer, /Needs you/); });
  // Wide shows every applicable lane, compact one at a time, so two lanes pin
  // the layout this wrapper forwards.
  assert.match(renderedText(board.renderer), /Queued/);
  assert.match(renderedText(board.renderer), /Building/);
  await openFirstBoardTask(board.renderer);
  let boardOpenAgent!: TestRenderer.ReactTestInstance;
  await act(async () => { boardOpenAgent = await waitForLabel(board.renderer, "Open agent for DIR-00001"); });
  assert.equal(boardOpenAgent.props.accessibilityState.disabled, false);
  await act(async () => { boardOpenAgent.props.onPress(); });
  assert.deepEqual(openedAgents, ["agent-wks_panel_root", "paseo-agent-task-0"]);
  await act(async () => board.renderer.unmount());
  board.queryClient.clear();
});

test("the Console consumes the host's accessibility preferences, not a literal", async () => {
  (globalThis as typeof globalThis & { IS_REACT_ACT_ENVIRONMENT: boolean }).IS_REACT_ACT_ENVIRONMENT = true;
  const plain = loadConsole(async () => snapshot([]));
  const plainMount = await mountConsole(plain, { navigation: undefined });
  await act(async () => { await waitForText(plainMount.renderer, /Rendered Project|No Projects|Project health/); });
  const plainBorder = (tabControl(plainMount.renderer, "Overview").props.style as { borderWidth?: number }[])
    .find((entry) => entry && typeof entry.borderWidth === "number")!.borderWidth;
  assert.equal(plainBorder, 1);
  await act(async () => plainMount.renderer.unmount());
  plainMount.queryClient.clear();

  // The shared source scan in tests/mobile-accessibility.test.ts matches the
  // import, so it passes when the call is replaced by a literal. High contrast
  // widens the tab strip's borders, so the preference is pinned here by the
  // effect it has.
  const contrast = loadConsole(async () => snapshot([]), { highContrast: true });
  const contrastMount = await mountConsole(contrast, { navigation: undefined });
  await act(async () => { await waitForText(contrastMount.renderer, /Rendered Project|No Projects|Project health/); });
  const contrastBorder = (tabControl(contrastMount.renderer, "Overview").props.style as { borderWidth?: number }[])
    .find((entry) => entry && typeof entry.borderWidth === "number")!.borderWidth;
  assert.equal(contrastBorder, 2);
  await act(async () => contrastMount.renderer.unmount());
  contrastMount.queryClient.clear();
});
