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
  TaskDetailQueryInput,
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

function loadPlanningSurface() {
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
        return { Modal: MockModal };
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
  return module.exports.PlanningSurface as React.ComponentType<Record<string, unknown>>;
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
    /Planning/,
    /Director/,
    /Workspace 01/,
    /M2-1/,
    /Tasks\s+3\s*\/\s*4/,
    /Agents\s+5\s*\/\s*8/,
    /Needs you/,
    /A human may approve a scoped dependency override/,
  ]) {
    assert.match(text, expected);
  }

  const task = renderer.root.findByProps({
    accessibilityHint: "Opens task details",
    accessibilityLabel:
      "DIR-00001, Contract-first planning UI shell, Needs you, urgent priority, A human may approve a scoped dependency override",
  });
  await act(async () => {
    task.props.onPress();
  });
  await act(async () => {
    await waitForText(renderer, /Configuration inheritance/);
  });
  assert.match(renderedText(renderer), /Waiting for DIR-DEPENDENCY/);
  assert.match(renderedText(renderer), /Task projection created/);

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
  const launchTask = renderer.root.findByProps({
    accessibilityLabel: "DIR-00002, Open task 2, Queued, high priority",
  });
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
  const lists = renderer.root.findAll((node) => String(node.type) === "FlatList");
  const workspaceList = lists.find((node) =>
    Array.isArray(node.props.data) && node.props.data.length === 25,
  );
  assert.ok(workspaceList);
  assert.equal(workspaceList.props.initialNumToRender, 10);
  const taskList = lists.find((node) =>
    Array.isArray(node.props.data) && node.props.data[0]?.id === "task-0",
  );
  assert.ok(taskList);
  assert.equal(taskList.props.initialNumToRender, 16);
  assert.equal(taskList.props.maxToRenderPerBatch, 16);
  assert.equal(taskList.props.windowSize, 7);
  assert.equal(taskList.props.removeClippedSubviews, true);
  assert.equal(taskList.props.keyExtractor(taskList.props.data[0]), "task-0");
  assert.ok(
    renderer.root.findAllByProps({ accessibilityHint: "Opens task details" }).length <= 16,
  );

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

  await act(async () => renderer.unmount());
  queryClient.clear();
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
  const workspace = renderer.root.findByProps({
    accessibilityLabel: "Filter workspace Workspace 01",
  });
  await act(async () => {
    workspace.props.onPress();
    await new Promise((resolve) => setTimeout(resolve, 25));
  });
  assert.deepEqual(fixture.requests.at(-1)?.workspaceIds, ["workspace-0"]);

  await act(async () => {
    await waitForText(renderer, /Contract-first planning UI shell/);
  });
  const sort = renderer.root.findByProps({
    accessibilityLabel: "Sort by Task key",
  });
  await act(async () => {
    sort.props.onPress();
    await new Promise((resolve) => setTimeout(resolve, 25));
  });
  assert.equal(fixture.requests.at(-1)?.sort, "key_asc");
  assert.deepEqual(fixture.requests.at(-1)?.workspaceIds, ["workspace-0"]);
  await act(async () => {
    await waitForText(renderer, /Contract-first planning UI shell/);
  });

  const task = renderer.root.findAllByProps({ accessibilityHint: "Opens task details" })[0];
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
  assert.match(source, /useInfiniteQuery/);
  assert.match(source, /useRpc\(planningQueryRpc\)/);
  assert.doesNotMatch(source, /QueryClientProvider|new QueryClient/);

  await act(async () => renderer.unmount());
  queryClient.clear();
});
