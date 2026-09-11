// SPDX-License-Identifier: Apache-2.0

import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import test from "node:test";

import * as ReactQuery from "@tanstack/react-query";
import React from "react";
import * as JsxRuntime from "react/jsx-runtime";
import TestRenderer, { act } from "react-test-renderer";
import ts from "typescript";

import * as PlanningContract from "../generated/planning-contract.shared.ts";
import type {
  PlanningClient,
  PlanningMutationInput,
  PlanningQueryInput,
  PlanningSnapshot,
  DerivedState,
  TaskDetailQueryInput,
  TaskSummary,
} from "../generated/planning-contract.shared.ts";
import * as PlanningRpc from "../rpc/planning.shared.ts";
import { DeterministicPlanningFixture } from "./fixtures/planning-fixture.ts";

type Deferred<T> = {
  promise: Promise<T>;
  resolve(value: T): void;
  reject(error: Error): void;
};

function deferred<T>(): Deferred<T> {
  let resolve!: (value: T) => void;
  let reject!: (error: Error) => void;
  const promise = new Promise<T>((accept, decline) => {
    resolve = accept;
    reject = decline;
  });
  return { promise, resolve, reject };
}

function MockFlatList(props: Record<string, unknown>) {
  const data = props.data as readonly unknown[];
  const initial = props.initialNumToRender as number | undefined;
  const renderItem = props.renderItem as (input: {
    item: unknown;
    index: number;
  }) => React.ReactNode;
  const keyExtractor = props.keyExtractor as
    | ((item: unknown, index: number) => string)
    | undefined;
  const visible = data.slice(0, initial ?? data.length);
  return React.createElement(
    "FlatList",
    props,
    visible.map((item, index) =>
      React.createElement(
        React.Fragment,
        { key: keyExtractor?.(item, index) ?? String(index) },
        renderItem({ item, index }),
      ),
    ),
  );
}

function MockModal(props: Record<string, unknown>) {
  return props.open
    ? React.createElement("Modal", props, props.children as React.ReactNode)
    : null;
}
MockModal.Content = function MockModalContent(props: Record<string, unknown>) {
  return React.createElement(
    "ModalContent",
    props,
    props.children as React.ReactNode,
  );
};

type PlanningSurfaceModule = {
  PlanningSurface: React.ComponentType<Record<string, unknown>>;
  planningDateLabel(value: string): string;
  visibleBoardLaneStates(
    tasks: readonly TaskSummary[],
    selectedStates: readonly DerivedState[],
  ): readonly DerivedState[];
};

function loadPlanningModule(): PlanningSurfaceModule {
  const source = readFileSync("ui/planning-surface.client.tsx", "utf8");
  const compiled = ts.transpileModule(source, {
    fileName: "ui/planning-surface.client.tsx",
    compilerOptions: {
      target: ts.ScriptTarget.ES2022,
      module: ts.ModuleKind.CommonJS,
      jsx: ts.JsxEmit.ReactJSX,
      esModuleInterop: true,
    },
  }).outputText;
  const module = { exports: {} as Record<string, unknown> };
  const reactModule = { ...React, default: React, __esModule: true };
  const require = (specifier: string): unknown => {
    switch (specifier) {
      case "@getpaseo/plugin":
        return { useRpc: () => async () => undefined };
      case "@getpaseo/plugin/react-native":
        return { Icon: "Icon", Modal: MockModal };
      case "@tanstack/react-query":
        return ReactQuery;
      case "react":
        return reactModule;
      case "react/jsx-runtime":
        return JsxRuntime;
      case "react-native":
        return {
          ActivityIndicator: "ActivityIndicator",
          FlatList: MockFlatList,
          Pressable: "Pressable",
          ScrollView: "ScrollView",
          StyleSheet: { create: (styles: unknown) => styles },
          Text: "Text",
          TextInput: "TextInput",
          View: "View",
        };
      case "../generated/planning-contract.shared.ts":
        return PlanningContract;
      case "../rpc/planning.shared.ts":
        return PlanningRpc;
      default:
        throw new Error(`unexpected runtime import ${specifier}`);
    }
  };
  Function("require", "module", "exports", compiled)(
    require,
    module,
    module.exports,
  );
  return module.exports as PlanningSurfaceModule;
}

function loadPlanningSurface() {
  return loadPlanningModule().PlanningSurface;
}

const colors = {
  accent: "token-accent",
  accentForeground: "token-accent-foreground",
  border: "token-border",
  foreground: "token-foreground",
  foregroundMuted: "token-muted",
  statusDanger: "token-danger",
  statusSuccess: "token-success",
  statusWarning: "token-warning",
  surface0: "token-surface-0",
  surface1: "token-surface-1",
  surface2: "token-surface-2",
};

function props(client: PlanningClient, compact: boolean) {
  return {
    client,
    layout: { compact, platform: compact ? "ios" : "web" },
    theme: { colors },
  };
}

function renderedText(renderer: TestRenderer.ReactTestRenderer): string {
  return renderer.root
    .findAll((node) => String(node.type) === "Text")
    .flatMap((node) => node.children)
    .filter((value): value is string => typeof value === "string")
    .join(" ");
}

async function waitForText(
  renderer: TestRenderer.ReactTestRenderer,
  expected: RegExp,
  absent?: RegExp,
): Promise<string> {
  const deadline = Date.now() + 5_000;
  let text = "";
  while (Date.now() < deadline) {
    await new Promise((resolve) => setTimeout(resolve, 5));
    text = renderedText(renderer);
    if (expected.test(text) && !absent?.test(text)) return text;
  }
  assert.match(text, expected);
  if (absent) assert.doesNotMatch(text, absent);
  return text;
}

function textColor(node: TestRenderer.ReactTestInstance): unknown {
  const style = node.props.style as unknown;
  const values = Array.isArray(style) ? style.filter(Boolean) : [style];
  return values.reduce<unknown>((color, value) => {
    if (value && typeof value === "object" && "color" in value) {
      return (value as { color?: unknown }).color ?? color;
    }
    return color;
  }, undefined);
}

async function showFilters(renderer: TestRenderer.ReactTestRenderer) {
  const control = renderer.root.findByProps({ accessibilityLabel: "Open task filters" });
  assert.equal(control.props.accessibilityState?.expanded, false);
  await act(async () => control.props.onPress());
  assert.ok(renderer.root.findByProps({ title: "Filter tasks" }));
}

test("Board columns stay canonical and Done remains query-only history", async () => {
  const fixture = new DeterministicPlanningFixture();
  const snapshot = await fixture.query({
    projectId: null,
    workspaceIds: [],
    epicIds: [],
    states: [],
    priorities: [],
    labels: [],
    attention: [],
    search: null,
    sort: "scheduler_order",
    cursor: null,
    pageSize: 100,
  });
  const planningModule = loadPlanningModule();
  assert.deepEqual(
    planningModule.visibleBoardLaneStates(snapshot.page.tasks, []),
    ["needs_you", "queued", "building", "validating", "in_review", "ready"],
  );
  assert.deepEqual(
    planningModule.visibleBoardLaneStates(
      snapshot.page.tasks.filter((task) => task.derivedState !== "needs_you"),
      [],
    ),
    ["queued", "building", "validating", "in_review", "ready"],
  );
  assert.deepEqual(
    planningModule.visibleBoardLaneStates(snapshot.page.tasks, [
      "ready",
      "done",
      "queued",
    ]),
    ["queued", "ready"],
  );
  assert.equal(
    planningModule.planningDateLabel("2026-09-11T23:59:59.000Z"),
    "2026-09-11",
  );
});

test("wide planning presentation renders engine navigation, Board, capacity, detail, and configuration preview", async () => {
  (globalThis as typeof globalThis & { IS_REACT_ACT_ENVIRONMENT: boolean })
    .IS_REACT_ACT_ENVIRONMENT = true;
  const fixture = new DeterministicPlanningFixture();
  const PlanningSurface = loadPlanningSurface();
  const queryClient = new ReactQuery.QueryClient({
    defaultOptions: { queries: { retry: false }, mutations: { retry: false } },
  });
  let renderer!: TestRenderer.ReactTestRenderer;
  await act(async () => {
    renderer = TestRenderer.create(
      React.createElement(
        ReactQuery.QueryClientProvider,
        { client: queryClient },
        React.createElement(PlanningSurface, props(fixture, false)),
      ),
    );
  });
  await act(async () => {
    await waitForText(renderer, /Contract-first planning UI shell/);
  });

  const text = renderedText(renderer);
  for (const expected of [
    /Director/,
    /Workspace 01/,
    /M2-1/,
    /3\s*\/\s*4\s+active/,
    /5\s*\/\s*8\s+agents/,
    /Needs you/,
    /Budget\s+Soft paused/,
    /A human may approve a scoped dependency override/,
  ]) {
    assert.match(text, expected);
  }
  const groupByEpic = renderer.root.findByProps({
    accessibilityLabel: "Group tasks by epic",
  });
  await act(async () => groupByEpic.props.onPress());
  assert.match(renderedText(renderer), /M2-1 · Milestone epic 1/);

  const task = renderer.root.findByProps({
    accessibilityHint: "Opens engine-projected task details. Task state cannot be moved here.",
    accessibilityLabel:
      "DIR-00001, Contract-first planning UI shell, Needs you, urgent priority, Workspace 01, M2-1 · Milestone epic 1, A human may approve a scoped dependency override",
  });
  await act(async () => {
    task.props.onPress();
  });
  await act(async () => {
    await waitForText(renderer, /Configuration inheritance/);
  });
  assert.match(renderedText(renderer), /Waiting for DIR-DEPENDENCY/);
  assert.match(renderedText(renderer), /Task projection created/);
  assert.match(renderedText(renderer), /Runtime budget/);
  assert.match(renderedText(renderer), /tokens\s*:\s*170000\s+consumed \+\s*0\s+reserved \/\s*200000/);
  assert.match(renderedText(renderer), /Reviewer\s+1/);
  assert.match(renderedText(renderer), /budget_tokens_soft_limit_reached/);

  const manual = renderer.root.findByProps({
    accessibilityLabel: "Set launchPolicy to Manual",
  });
  await act(async () => manual.props.onPress());
  const preview = renderer.root.findByProps({
    accessibilityLabel: "Preview configuration changes",
  });
  await act(async () => {
    preview.props.onPress();
  });
  await act(async () => {
    await waitForText(renderer, /Preview diff/);
  });
  assert.match(
    renderedText(renderer),
    /launchPolicy\s*:\s*Automatic\s*→\s*Manual\s*\(\s*task\s*\)/,
  );
  const apply = renderer.root.findByProps({ accessibilityLabel: "Apply configuration" });
  assert.equal(fixture.mutations.at(-1)?.intent.type, "configuration.preview");
  const mutationsBeforeApproval = fixture.mutations.length;
  await act(async () => apply.props.onPress());
  assert.equal(fixture.mutations.length, mutationsBeforeApproval);
  assert.match(renderedText(renderer), /Confirm the exact preview/);
  const confirmApply = renderer.root.findByProps({
    accessibilityLabel: "Confirm Apply configuration",
  });
  await act(async () => confirmApply.props.onPress());
  assert.equal(fixture.mutations.at(-1)?.intent.type, "configuration.apply");

  const modal = renderer.root.find((node) => String(node.type) === "Modal");
  await act(async () => modal.props.onOpenChange(false));
  await act(async () => {
    await waitForText(renderer, /Open task 2/);
  });
  const launchTask = renderer.root
    .findAllByProps({
      accessibilityHint: "Opens engine-projected task details. Task state cannot be moved here.",
    })
    .find((node) => String(node.props.accessibilityLabel).includes(", Queued,"));
  assert.ok(launchTask);
  await act(async () => launchTask.props.onPress());
  await act(async () => {
    await waitForText(renderer, /Launch now/);
  });
  const launch = renderer.root.findByProps({ accessibilityLabel: "Launch now" });
  await act(async () => launch.props.onPress());
  assert.equal(fixture.mutations.at(-1)?.intent.type, "task.launch-now");

  const themeValues = new Set(Object.values(colors));
  for (const node of renderer.root.findAll((candidate) => String(candidate.type) === "Text")) {
    assert.ok(themeValues.has(String(textColor(node))), `unstyled Text: ${node.children.join("")}`);
  }

  await act(async () => renderer.unmount());
  queryClient.clear();
});

test("wide List uses fixed purposeful columns and accessible engine-ordered rows", async () => {
  (globalThis as typeof globalThis & { IS_REACT_ACT_ENVIRONMENT: boolean })
    .IS_REACT_ACT_ENVIRONMENT = true;
  const fixture = new DeterministicPlanningFixture();
  const PlanningSurface = loadPlanningSurface();
  const queryClient = new ReactQuery.QueryClient({
    defaultOptions: { queries: { retry: false } },
  });
  let renderer!: TestRenderer.ReactTestRenderer;
  await act(async () => {
    renderer = TestRenderer.create(
      React.createElement(
        ReactQuery.QueryClientProvider,
        { client: queryClient },
        React.createElement(PlanningSurface, props(fixture, false)),
      ),
    );
  });
  await act(async () => {
    await waitForText(renderer, /Contract-first planning UI shell/);
  });

  const listTab = renderer.root
    .findAllByProps({ accessibilityRole: "tab" })
    .find((node) => node.findAll((child) =>
      String(child.type) === "Text" && child.children.join("") === "List"
    ).length > 0);
  assert.ok(listTab);
  await act(async () => listTab.props.onPress());

  const columns = renderer.root.findByProps({
    accessibilityLabel:
      "Task list columns: Task, State, Workspace, Epic, Priority, Updated",
  });
  assert.ok(columns);
  for (const heading of ["Task", "State", "Workspace", "Epic", "Priority", "Updated"]) {
    assert.ok(columns.findAll((node) =>
      String(node.type) === "Text" && node.children.join("") === heading
    ).length > 0, `missing ${heading} column`);
  }
  const firstRow = renderer.root.findAllByProps({
    accessibilityHint: "Opens engine-projected task details. Task state cannot be moved here.",
  })[0];
  assert.ok(firstRow);
  assert.equal(firstRow.props.focusable, true);
  assert.equal(firstRow.props.hitSlop, 4);
  assert.match(renderedText(renderer), /Workspace 01/);
  assert.match(renderedText(renderer), /M2-1 · Milestone epic 1/);
  assert.match(renderedText(renderer), /2026-09-09/);

  await act(async () => renderer.unmount());
  queryClient.clear();
});

test("Done history is an exclusive List query and never a movable Board lane", async () => {
  (globalThis as typeof globalThis & { IS_REACT_ACT_ENVIRONMENT: boolean })
    .IS_REACT_ACT_ENVIRONMENT = true;
  const fixture = new DeterministicPlanningFixture();
  const PlanningSurface = loadPlanningSurface();
  const queryClient = new ReactQuery.QueryClient({
    defaultOptions: { queries: { retry: false } },
  });
  let renderer!: TestRenderer.ReactTestRenderer;
  await act(async () => {
    renderer = TestRenderer.create(
      React.createElement(
        ReactQuery.QueryClientProvider,
        { client: queryClient },
        React.createElement(PlanningSurface, props(fixture, false)),
      ),
    );
  });
  await act(async () => {
    await waitForText(renderer, /Contract-first planning UI shell/);
  });

  await showFilters(renderer);
  const done = renderer.root.findByProps({ accessibilityLabel: "Filter state Done" });
  await act(async () => done.props.onPress());
  await act(async () => {
    await waitForText(renderer, /Historical task/);
  });
  assert.deepEqual(fixture.requests.at(-1)?.states, ["done"]);
  const selectedList = renderer.root
    .findAllByProps({ accessibilityRole: "tab" })
    .find((node) => node.props.accessibilityState?.selected && node.findAll((child) =>
      String(child.type) === "Text" && child.children.join("") === "List"
    ).length > 0);
  assert.ok(selectedList);

  const boardTab = renderer.root
    .findAllByProps({ accessibilityRole: "tab" })
    .find((node) => node.findAll((child) =>
      String(child.type) === "Text" && child.children.join("") === "Board"
    ).length > 0);
  assert.ok(boardTab);
  await act(async () => boardTab.props.onPress());
  await act(async () => {
    await waitForText(renderer, /Open task/);
  });
  assert.deepEqual(fixture.requests.at(-1)?.states, []);
  assert.equal(
    renderer.root.findAllByProps({ accessibilityRole: "header" }).filter((node) =>
      node.children.join("") === "Done"
    ).length,
    0,
  );

  await act(async () => renderer.unmount());
  queryClient.clear();
});

test("compact List uses bounded virtualized rendering and stable opaque keys", async () => {
  (globalThis as typeof globalThis & { IS_REACT_ACT_ENVIRONMENT: boolean })
    .IS_REACT_ACT_ENVIRONMENT = true;
  const fixture = new DeterministicPlanningFixture();
  const PlanningSurface = loadPlanningSurface();
  const queryClient = new ReactQuery.QueryClient({
    defaultOptions: { queries: { retry: false } },
  });
  let renderer!: TestRenderer.ReactTestRenderer;
  await act(async () => {
    renderer = TestRenderer.create(
      React.createElement(
        ReactQuery.QueryClientProvider,
        { client: queryClient },
        React.createElement(PlanningSurface, props(fixture, true)),
      ),
    );
  });
  await act(async () => {
    await waitForText(renderer, /Contract-first planning UI shell/);
  });

  const selectedTab = renderer.root
    .findAllByProps({ accessibilityRole: "tab" })
    .find((node) => node.props.accessibilityState?.selected);
  assert.ok(selectedTab);
  assert.match(
    selectedTab.findAll((node) => String(node.type) === "Text").map((node) => node.children.join("")).join(" "),
    /List/,
  );
  assert.match(renderedText(renderer), /M2-1 · Milestone epic 1/);
  assert.ok(renderer.root.findByProps({
    accessibilityLabel: "Task list columns: Task and status",
  }));
  await showFilters(renderer);
  const lists = renderer.root.findAll((node) => String(node.type) === "FlatList");
  const workspaceList = lists.find((node) =>
    Array.isArray(node.props.data) && node.props.data.length === 25,
  );
  assert.ok(workspaceList);
  assert.equal(workspaceList.props.initialNumToRender, 10);
  const taskList = lists.find((node) =>
    Array.isArray(node.props.data) &&
    node.props.data.some((row: { kind?: string; task?: { id?: string } }) =>
      row.kind === "task" && row.task?.id === "task-0"),
  );
  assert.ok(taskList);
  assert.equal(taskList.props.initialNumToRender, 16);
  assert.equal(taskList.props.maxToRenderPerBatch, 16);
  assert.equal(taskList.props.windowSize, 7);
  assert.equal(taskList.props.removeClippedSubviews, true);
  const taskRow = taskList.props.data.find(
    (row: { kind?: string; task?: { id?: string } }) => row.task?.id === "task-0",
  );
  assert.equal(taskList.props.keyExtractor(taskRow), "task:task-0");
  assert.ok(
    renderer.root.findAllByProps({
      accessibilityHint: "Opens engine-projected task details. Task state cannot be moved here.",
    }).length <= 16,
  );

  const done = renderer.root.findByProps({ accessibilityLabel: "Filter state Done" });
  await act(async () => done.props.onPress());
  await act(async () => {
    await waitForText(renderer, /Historical task/);
  });
  assert.deepEqual(fixture.requests.at(-1)?.states, ["done"]);
  assert.match(renderedText(renderer), /10000\s+matching tasks/);
  assert.ok(
    renderer.root.findAllByProps({
      accessibilityHint: "Opens engine-projected task details. Task state cannot be moved here.",
    }).length <= 16,
  );

  await act(async () => renderer.unmount());
  queryClient.clear();
});

test("paged cursor invalidation fails closed and refreshes from the first snapshot", async () => {
  (globalThis as typeof globalThis & { IS_REACT_ACT_ENVIRONMENT: boolean })
    .IS_REACT_ACT_ENVIRONMENT = true;
  const fixture = new DeterministicPlanningFixture();
  const PlanningSurface = loadPlanningSurface();
  const queryClient = new ReactQuery.QueryClient({
    defaultOptions: { queries: { retry: false } },
  });
  let renderer!: TestRenderer.ReactTestRenderer;
  await act(async () => {
    renderer = TestRenderer.create(
      React.createElement(
        ReactQuery.QueryClientProvider,
        { client: queryClient },
        React.createElement(PlanningSurface, props(fixture, true)),
      ),
    );
  });
  await act(async () => {
    await waitForText(renderer, /Contract-first planning UI shell/);
  });
  const detail = await fixture.taskDetail({ taskId: "task-0", afterCursor: null });
  const action = detail.detail.summary.allowedActions.find(
    (candidate) => candidate.kind === "configuration.preview",
  );
  assert.ok(action);
  await fixture.mutate(PlanningContract.bindPlanningMutation(action, {
    type: "configuration.preview",
    target: detail.detail.configurationTarget,
    overrides: [],
  }));

  const next = renderer.root.findByProps({ accessibilityLabel: "Load next task page" });
  await act(async () => next.props.onPress());
  await act(async () => {
    await waitForText(renderer, /Planning data is unavailable/);
  });
  const refresh = renderer.root.findByProps({
    accessibilityLabel: "Refresh planning data from the first page",
  });
  await act(async () => refresh.props.onPress());
  await act(async () => {
    await waitForText(renderer, /Contract-first planning UI shell/);
  });
  assert.equal(fixture.requests.at(-1)?.cursor, null);

  await act(async () => renderer.unmount());
  queryClient.clear();
});

test("loading, initial error, retry, stale refresh, and updated data preserve cache semantics", async () => {
  (globalThis as typeof globalThis & { IS_REACT_ACT_ENVIRONMENT: boolean })
    .IS_REACT_ACT_ENVIRONMENT = true;
  const fixture = new DeterministicPlanningFixture();
  const firstSnapshot = await fixture.query({
    projectId: null,
    workspaceIds: [],
    epicIds: [],
    states: [],
    priorities: [],
    labels: [],
    attention: [],
    search: null,
    sort: "scheduler_order",
    cursor: null,
    pageSize: 50,
  });
  const pending: Deferred<PlanningSnapshot>[] = [];
  const client: PlanningClient = {
    query(_input: PlanningQueryInput) {
      const next = deferred<PlanningSnapshot>();
      pending.push(next);
      return next.promise;
    },
    taskDetail(input: TaskDetailQueryInput) {
      return fixture.taskDetail(input);
    },
    mutate(input: PlanningMutationInput) {
      return fixture.mutate(input);
    },
  };
  const PlanningSurface = loadPlanningSurface();
  const queryClient = new ReactQuery.QueryClient({
    defaultOptions: { queries: { retry: false } },
  });
  let renderer!: TestRenderer.ReactTestRenderer;
  await act(async () => {
    renderer = TestRenderer.create(
      React.createElement(
        ReactQuery.QueryClientProvider,
        { client: queryClient },
        React.createElement(PlanningSurface, props(client, false)),
      ),
    );
  });
  assert.match(renderedText(renderer), /Loading planning data/);

  await act(async () => {
    pending.shift()?.reject(new Error("initial failure"));
    await waitForText(renderer, /Planning data is unavailable/);
  });
  const retry = renderer.root.findByProps({
    accessibilityLabel: "Try loading planning data again",
  });
  await act(async () => {
    retry.props.onPress();
    while (pending.length === 0) await new Promise((resolve) => setTimeout(resolve, 5));
    pending.shift()?.resolve(firstSnapshot);
    await waitForText(renderer, /Contract-first planning UI shell/);
  });

  await act(async () => {
    const refresh = queryClient.refetchQueries({ queryKey: ["director", "planning"] });
    await waitForText(renderer, /Updating engine snapshot/);
    pending.shift()?.reject(new Error("stale refresh failure"));
    await refresh;
    await waitForText(renderer, /Contract-first planning UI shell/, /Updating engine snapshot/);
  });
  assert.doesNotMatch(renderedText(renderer), /Planning data is unavailable/);
  assert.match(renderedText(renderer), /Showing the last engine snapshot/);

  const updated: PlanningSnapshot = PlanningContract.planningSnapshotSchema.parse({
    ...firstSnapshot,
    cursor: "200",
    page: {
      ...firstSnapshot.page,
      tasks: firstSnapshot.page.tasks.map((task, index) =>
        index === 0 ? { ...task, title: "Updated engine title" } : task,
      ),
    },
  });
  await act(async () => {
    const refresh = queryClient.refetchQueries({ queryKey: ["director", "planning"] });
    while (pending.length === 0) await new Promise((resolve) => setTimeout(resolve, 5));
    pending.shift()?.resolve(updated);
    await refresh;
    await waitForText(renderer, /Updated engine title/);
  });

  const empty: PlanningSnapshot = PlanningContract.planningSnapshotSchema.parse({
    ...updated,
    cursor: "201",
    page: {
      ...updated.page,
      tasks: [],
      totalTasks: "0",
      nextCursor: null,
    },
  });
  await act(async () => {
    const refresh = queryClient.refetchQueries({ queryKey: ["director", "planning"] });
    while (pending.length === 0) await new Promise((resolve) => setTimeout(resolve, 5));
    pending.shift()?.resolve(empty);
    await refresh;
    await waitForText(renderer, /No tasks match this query/);
  });

  const noProjects: PlanningSnapshot = PlanningContract.planningSnapshotSchema.parse({
    ...empty,
    cursor: "202",
    page: {
      ...empty.page,
      selectedProjectId: null,
      projects: [],
      workspaces: [],
      epics: [],
    },
  });
  await act(async () => {
    const refresh = queryClient.refetchQueries({ queryKey: ["director", "planning"] });
    while (pending.length === 0) await new Promise((resolve) => setTimeout(resolve, 5));
    pending.shift()?.resolve(noProjects);
    await refresh;
    await waitForText(renderer, /No projects/);
  });

  await act(async () => renderer.unmount());
  queryClient.clear();
});

test("offline startup and cached offline state remain explicit without changing the query", async () => {
  (globalThis as typeof globalThis & { IS_REACT_ACT_ENVIRONMENT: boolean })
    .IS_REACT_ACT_ENVIRONMENT = true;
  const fixture = new DeterministicPlanningFixture();
  const PlanningSurface = loadPlanningSurface();
  const queryClient = new ReactQuery.QueryClient({
    defaultOptions: { queries: { retry: false } },
  });
  let renderer!: TestRenderer.ReactTestRenderer;
  ReactQuery.onlineManager.setOnline(false);
  try {
    await act(async () => {
      renderer = TestRenderer.create(
        React.createElement(
          ReactQuery.QueryClientProvider,
          { client: queryClient },
          React.createElement(PlanningSurface, props(fixture, true)),
        ),
      );
    });
    assert.match(renderedText(renderer), /Waiting for connection/);
    assert.equal(fixture.requests.length, 0);
    assert.equal(
      renderer.root.findAllByProps({ accessibilityLabel: "Open task filters" }).length,
      0,
    );

    await act(async () => {
      ReactQuery.onlineManager.setOnline(true);
      await waitForText(renderer, /Contract-first planning UI shell/);
    });
    assert.equal(fixture.requests.length, 1);
    assert.ok(renderer.root.findByProps({ accessibilityLabel: "Open task filters" }));

    await act(async () => {
      ReactQuery.onlineManager.setOnline(false);
      void queryClient.refetchQueries({ queryKey: ["director", "planning"] });
      await new Promise((resolve) => setTimeout(resolve, 10));
    });
    assert.match(renderedText(renderer), /Offline · showing the last engine snapshot/);
    assert.match(renderedText(renderer), /Contract-first planning UI shell/);
  } finally {
    if (renderer) await act(async () => renderer.unmount());
    ReactQuery.onlineManager.setOnline(true);
    queryClient.clear();
  }
});

test("selections and query controls submit engine inputs without card movement or local sorting", async () => {
  (globalThis as typeof globalThis & { IS_REACT_ACT_ENVIRONMENT: boolean })
    .IS_REACT_ACT_ENVIRONMENT = true;
  const fixture = new DeterministicPlanningFixture();
  const PlanningSurface = loadPlanningSurface();
  const queryClient = new ReactQuery.QueryClient({
    defaultOptions: { queries: { retry: false } },
  });
  let renderer!: TestRenderer.ReactTestRenderer;
  await act(async () => {
    renderer = TestRenderer.create(
      React.createElement(
        ReactQuery.QueryClientProvider,
        { client: queryClient },
        React.createElement(PlanningSurface, props(fixture, false)),
      ),
    );
  });
  await act(async () => {
    await waitForText(renderer, /Contract-first planning UI shell/);
  });
  assert.ok(renderer.root.findByProps({ accessibilityLabel: "Open task filters" }));
  await showFilters(renderer);
  const workspace = renderer.root.findByProps({
    accessibilityLabel: "Filter workspace Workspace 01",
  });
  await act(async () => workspace.props.onPress());
  await act(async () => {
    await new Promise((resolve) => setTimeout(resolve, 25));
  });
  assert.deepEqual(fixture.requests.at(-1)?.workspaceIds, ["workspace-0"]);

  await act(async () => {
    await waitForText(renderer, /Contract-first planning UI shell/);
  });
  const epic = renderer.root.findByProps({ accessibilityLabel: "Filter epic M2-1" });
  await act(async () => epic.props.onPress());
  await act(async () => new Promise((resolve) => setTimeout(resolve, 25)));
  assert.deepEqual(fixture.requests.at(-1)?.epicIds, ["epic-0"]);

  const priority = renderer.root.findByProps({
    accessibilityLabel: "Filter priority urgent",
  });
  await act(async () => priority.props.onPress());
  await act(async () => new Promise((resolve) => setTimeout(resolve, 25)));
  assert.deepEqual(fixture.requests.at(-1)?.priorities, ["urgent"]);

  const label = renderer.root.findByProps({ accessibilityLabel: "Filter label m2" });
  await act(async () => label.props.onPress());
  await act(async () => new Promise((resolve) => setTimeout(resolve, 25)));
  assert.deepEqual(fixture.requests.at(-1)?.labels, ["m2"]);

  const state = renderer.root.findByProps({
    accessibilityLabel: "Filter state Needs you",
  });
  await act(async () => state.props.onPress());
  await act(async () => new Promise((resolve) => setTimeout(resolve, 25)));
  assert.deepEqual(fixture.requests.at(-1)?.states, ["needs_you"]);

  const sort = renderer.root.findByProps({
    accessibilityLabel: "Sort by Task key",
  });
  await act(async () => sort.props.onPress());
  await act(async () => {
    await new Promise((resolve) => setTimeout(resolve, 25));
  });
  assert.equal(fixture.requests.at(-1)?.sort, "key_asc");
  assert.deepEqual(fixture.requests.at(-1)?.workspaceIds, ["workspace-0"]);
  assert.deepEqual(fixture.requests.at(-1)?.epicIds, ["epic-0"]);
  assert.deepEqual(fixture.requests.at(-1)?.priorities, ["urgent"]);
  assert.deepEqual(fixture.requests.at(-1)?.labels, ["m2"]);
  assert.deepEqual(fixture.requests.at(-1)?.states, ["needs_you"]);
  await act(async () => {
    await waitForText(renderer, /Contract-first planning UI shell/);
  });
  const attention = renderer.root.findByProps({
    accessibilityLabel: "Filter attention Policy override required",
  });
  await act(async () => attention.props.onPress());
  await act(async () => {
    await new Promise((resolve) => setTimeout(resolve, 25));
  });
  assert.deepEqual(fixture.requests.at(-1)?.attention, ["policy_override_required"]);
  await act(async () => {
    await waitForText(renderer, /Contract-first planning UI shell/);
  });

  const task = renderer.root.findAllByProps({
    accessibilityHint: "Opens engine-projected task details. Task state cannot be moved here.",
  })[0];
  assert.ok(task);
  await act(async () => {
    task.props.onPress();
  });
  await act(async () => {
    await waitForText(renderer, /Configuration inheritance/);
  });
  assert.equal(fixture.mutations.length, 0, "opening a card must not mutate or move it");

  const source = readFileSync("ui/planning-surface.client.tsx", "utf8");
  assert.doesNotMatch(source, /PanResponder|draggable|onDrag|onDrop|localStorage|location\./);
  assert.doesNotMatch(source, /\.sort\s*\(/);
  assert.doesNotMatch(source, /#[0-9a-f]{3,8}/i);
  assert.match(source, /useQuery/);
  assert.doesNotMatch(source, /useInfiniteQuery/);
  assert.match(source, /useRpc\(planningQueryRpc\)/);
  assert.doesNotMatch(source, /QueryClientProvider|new QueryClient/);
  assert.match(source, /title="Filter tasks"/);
  assert.match(source, /borderLeftColor: stateAccent/);
  assert.match(source, /borderTopColor: stateAccent/);
  assert.doesNotMatch(source, />Planning<\/Text>/);

  await act(async () => renderer.unmount());
  queryClient.clear();
});
