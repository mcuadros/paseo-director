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
  AllowedAction,
  ConfigurationOverride,
  PlanningClient,
  PlanningMutationInput,
  PlanningQueryInput,
  TaskDetail,
  TaskDetailQueryInput,
} from "../generated/planning-contract.shared.ts";
import * as PlanningRpc from "../rpc/planning.shared.ts";
import * as HostContract from "../generated/host-contract.shared.ts";
import { DeterministicPlanningFixture } from "./fixtures/planning-fixture.ts";

(globalThis as typeof globalThis & { IS_REACT_ACT_ENVIRONMENT: boolean }).IS_REACT_ACT_ENVIRONMENT = true;

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

function loadTaskDetailViewModule() {
  const source = readFileSync("ui/task-detail-view.client.tsx", "utf8");
  const compiled = ts.transpileModule(source, {
    fileName: "ui/task-detail-view.client.tsx",
    compilerOptions: {
      target: ts.ScriptTarget.ES2022,
      module: ts.ModuleKind.CommonJS,
      jsx: ts.JsxEmit.ReactJSX,
      esModuleInterop: true,
    },
  }).outputText;

  const reactModule = { ...React, default: React, __esModule: true };
  const require = (specifier: string): unknown => {
    switch (specifier) {
      case "@getpaseo/plugin":
        return { useRpc: () => async () => undefined };
      case "@getpaseo/plugin/react-native":
        return { Icon: "Icon" };
      case "@tanstack/react-query":
        return ReactQuery;
      case "react":
        return reactModule;
      case "react/jsx-runtime":
        return JsxRuntime;
      case "react-native":
        return {
          ActivityIndicator: "ActivityIndicator",
          Pressable: "Pressable",
          ScrollView: "ScrollView",
          StyleSheet: { create: (styles: unknown) => styles },
          Text: "Text",
          TextInput: "TextInput",
          View: "View",
        };
      case "../generated/planning-contract.shared.ts":
        return PlanningContract;
      case "./shell-layout.client.ts":
      case "./shell-layout.client": {
        const subSource = readFileSync("ui/shell-layout.client.ts", "utf8");
        const subCompiled = ts.transpileModule(subSource, {
          fileName: "ui/shell-layout.client.ts",
          compilerOptions: {
            target: ts.ScriptTarget.ES2022,
            module: ts.ModuleKind.CommonJS,
            jsx: ts.JsxEmit.ReactJSX,
            esModuleInterop: true,
          },
        }).outputText;
        const subModule = { exports: {} as Record<string, unknown> };
        Function("require", "module", "exports", subCompiled)(
          require,
          subModule,
          subModule.exports,
        );
        return subModule.exports;
      }
      default:
        throw new Error(`unexpected runtime import ${specifier}`);
    }
  };

  const module = { exports: {} as Record<string, unknown> };
  Function("require", "module", "exports", compiled)(
    require,
    module,
    module.exports,
  );
  return module.exports as {
    TaskDetailView: React.ComponentType<Record<string, unknown>>;
  };
}

function loadTaskInspectorModule(injectedRpc: (contract: unknown) => unknown) {
  const source = readFileSync("ui/task-inspector.client.tsx", "utf8");
  const compiled = ts.transpileModule(source, {
    fileName: "ui/task-inspector.client.tsx",
    compilerOptions: {
      target: ts.ScriptTarget.ES2022,
      module: ts.ModuleKind.CommonJS,
      jsx: ts.JsxEmit.ReactJSX,
      esModuleInterop: true,
    },
  }).outputText;

  const reactModule = { ...React, default: React, __esModule: true };
  const require = (specifier: string): unknown => {
    switch (specifier) {
      case "@getpaseo/plugin":
        return { useRpc: injectedRpc };
      case "@getpaseo/plugin/react-native":
        return { Icon: "Icon" };
      case "@tanstack/react-query":
        return ReactQuery;
      case "react":
        return reactModule;
      case "react/jsx-runtime":
        return JsxRuntime;
      case "react-native":
        return {
          ActivityIndicator: "ActivityIndicator",
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
      case "./task-detail-view.client.ts":
      case "./task-detail-view.client": {
        const subSource = readFileSync("ui/task-detail-view.client.tsx", "utf8");
        const subCompiled = ts.transpileModule(subSource, {
          fileName: "ui/task-detail-view.client.tsx",
          compilerOptions: {
            target: ts.ScriptTarget.ES2022,
            module: ts.ModuleKind.CommonJS,
            jsx: ts.JsxEmit.ReactJSX,
            esModuleInterop: true,
          },
        }).outputText;
        const subModule = { exports: {} as Record<string, unknown> };
        Function("require", "module", "exports", subCompiled)(
          require,
          subModule,
          subModule.exports,
        );
        return subModule.exports;
      }
      case "./shell-layout.client.ts":
      case "./shell-layout.client": {
        const subSource = readFileSync("ui/shell-layout.client.ts", "utf8");
        const subCompiled = ts.transpileModule(subSource, {
          fileName: "ui/shell-layout.client.ts",
          compilerOptions: {
            target: ts.ScriptTarget.ES2022,
            module: ts.ModuleKind.CommonJS,
            jsx: ts.JsxEmit.ReactJSX,
            esModuleInterop: true,
          },
        }).outputText;
        const subModule = { exports: {} as Record<string, unknown> };
        Function("require", "module", "exports", subCompiled)(
          require,
          subModule,
          subModule.exports,
        );
        return subModule.exports;
      }
      default:
        throw new Error(`unexpected runtime import ${specifier}`);
    }
  };

  const module = { exports: {} as Record<string, unknown> };
  Function("require", "module", "exports", compiled)(
    require,
    module,
    module.exports,
  );
  return module.exports as {
    TaskInspector: React.ComponentType<Record<string, unknown>>;
  };
}

function loadTaskNavigationModule() {
  const source = readFileSync("ui/task-navigation.client.tsx", "utf8");
  const compiled = ts.transpileModule(source, {
    fileName: "ui/task-navigation.client.tsx",
    compilerOptions: {
      target: ts.ScriptTarget.ES2022,
      module: ts.ModuleKind.CommonJS,
      jsx: ts.JsxEmit.ReactJSX,
      esModuleInterop: true,
    },
  }).outputText;
  const reactModule = { ...React, default: React, __esModule: true };
  const require = (specifier: string): unknown => {
    switch (specifier) {
      case "@getpaseo/plugin":
        return { useAgent: () => null };
      case "@getpaseo/plugin/react-native":
        return { Icon: "Icon" };
      case "react":
        return reactModule;
      case "react/jsx-runtime":
        return JsxRuntime;
      case "react-native":
        return { Text: "Text" };
      case "../generated/host-contract.shared.ts":
        return HostContract;
      default:
        throw new Error(`unexpected runtime import ${specifier}`);
    }
  };
  const module = { exports: {} as Record<string, unknown> };
  Function("require", "module", "exports", compiled)(require, module, module.exports);
  return module.exports as {
    contributeTaskNavigation: (client: Record<string, unknown>) => () => void;
  };
}

test("TaskDetailView renders tabs, exact agent navigation, execution binding, budget, and activity", async () => {
  const fixture = new DeterministicPlanningFixture();
  const snapshot = await fixture.taskDetail({
    hostId: "host-a",
    context: "board",
    taskId: "task-0",
    paseoWorkspaceId: null,
    paseoAgentId: null,
    afterCursor: null,
  });
  assert.ok(snapshot.detail);
  const detail = snapshot.detail;
  const { TaskDetailView } = loadTaskDetailViewModule();

  let currentTab: string = "details";
  const actionsSubmitted: unknown[] = [];
  const navigationCalls: unknown[] = [];

  function makeElement(tab: string, agentAvailable = true) {
    const task = agentAvailable ? detail : {
      ...detail,
      binding: {
        ...detail.binding,
        paseoWorkspaceId: null,
        paseoAgentId: null,
        agentNavigation: "unavailable" as const,
        unavailableReason: {
          code: "agent_binding_unavailable",
          message: "No exact current Paseo agent and Execution Workspace binding is available",
          wakeCondition: "refresh_exact_host_task_binding",
          humanActionRequired: false,
        },
      },
    };
    return React.createElement(TaskDetailView, {
      task,
      theme: { colors },
      layout: { compact: false, platform: "web" },
      navigation: {
        openAgent: (input: { agentId: string }) => {
          navigationCalls.push(input);
        },
        openWorkspace: () => undefined,
      },
      activeTab: tab,
      onTabChange: (next: string) => {
        currentTab = next;
      },
      onAction: (action: unknown, intent: unknown) => {
        actionsSubmitted.push({ action, intent });
      },
      workspaceName: "Workspace 01",
      epicName: "M2-1 · Milestone epic 1",
    });
  }

  let renderer!: TestRenderer.ReactTestRenderer;
  await act(async () => {
    renderer = TestRenderer.create(makeElement("details"));
  });

  let text = renderedText(renderer);
  const rootView = renderer.root.findAll(
    (node) => String(node.type) === "View",
  )[0];
  assert.equal(rootView?.props.style.backgroundColor, undefined);
  const tabList = renderer.root.findByProps({ accessibilityRole: "tablist" });
  assert.equal(tabList.props.style.paddingBottom, undefined);
  assert.match(text, /DIR-00001/);
  assert.match(text, /Contract-first planning UI shell/);
  assert.match(text, /Objective/);
  assert.match(text, /Acceptance criteria/);
  assert.match(text, /Configuration inheritance/);
  assert.match(text, /Waiting for DIR-DEPENDENCY/);

  const openAgent = renderer.root.findByProps({
    accessibilityLabel: "Open agent for DIR-00001",
  });
  await act(async () => {
    openAgent.props.onPress();
  });
  assert.deepEqual(navigationCalls, [{ agentId: "paseo-agent-task-0" }]);

  // Switch to Execution Tab
  const executionTabBtn = renderer.root.findByProps({
    accessibilityLabel: "Show Execution tab",
  });
  assert.equal(executionTabBtn.props.accessibilityRole, "tab");
  assert.equal(executionTabBtn.props.focusable, true);
  assert.equal(executionTabBtn.props.accessibilityState.selected, false);
  await act(async () => {
    executionTabBtn.props.onPress();
  });
  assert.equal(currentTab, "execution");

  // Re-render for Execution tab with active agent
  await act(async () => {
    renderer.update(makeElement("execution"));
  });
  text = renderedText(renderer);
  assert.match(text, /Exact execution binding/);
  assert.match(text, /Runtime budget/);
  assert.match(text, /170,000\s*\/\s*200,000/);
  assert.match(text, /Reviewer\s+1/);
  assert.match(text, /budget_tokens_soft_limit_reached/);

  assert.match(text, /run-task-0/);
  assert.match(text, /candidate-task-0/);
  assert.match(text, /host-a/);

  // Test Open agent Unavailable Degradation
  await act(async () => {
    renderer.update(makeElement("execution", false));
  });
  text = renderedText(renderer);
  assert.match(text, /No exact current Paseo agent/);
  const unavailableBtn = renderer.root.findByProps({
    accessibilityLabel: "Open agent unavailable",
  });
  assert.equal(unavailableBtn.props.disabled, true);
  assert.deepEqual(unavailableBtn.props.accessibilityState, { disabled: true });

  // Re-render for Activity tab
  await act(async () => {
    renderer.update(makeElement("activity"));
  });
  text = renderedText(renderer);
  assert.match(text, /Task projection created/);
  assert.match(text, /planning/i);
});

test("TaskDetailView handles activity loading, error, offline, and stale refresh states", async () => {
  const fixture = new DeterministicPlanningFixture();
  const snapshot = await fixture.taskDetail({
    hostId: "host-a",
    context: "board",
    taskId: "task-0",
    paseoWorkspaceId: null,
    paseoAgentId: null,
    afterCursor: null,
  });
  assert.ok(snapshot.detail);
  const detail = snapshot.detail;
  const { TaskDetailView } = loadTaskDetailViewModule();

  // Activity Loading
  let renderer!: TestRenderer.ReactTestRenderer;
  await act(async () => {
    renderer = TestRenderer.create(
      React.createElement(TaskDetailView, {
        task: { ...detail, activity: [] },
        theme: { colors },
        layout: { compact: true, platform: "ios" },
        activeTab: "activity",
        activityState: { isPending: true },
      }),
    );
  });
  let text = renderedText(renderer);
  assert.match(text, /Loading activity stream/);

  // Activity Error with Retry
  let retried = false;
  await act(async () => {
    renderer.update(
      React.createElement(TaskDetailView, {
        task: { ...detail, activity: [] },
        theme: { colors },
        layout: { compact: false, platform: "web" },
        activeTab: "activity",
        activityState: {
          isError: true,
          errorMessage: "Director Engine unavailable",
          onReload: () => {
            retried = true;
          },
        },
      }),
    );
  });
  text = renderedText(renderer);
  assert.match(text, /Activity stream is unavailable/);
  const retryBtn = renderer.root.findByProps({
    accessibilityLabel: "Try loading activity stream again",
  });
  await act(async () => {
    retryBtn.props.onPress();
  });
  assert.equal(retried, true);

  // Activity Offline State
  await act(async () => {
    renderer.update(
      React.createElement(TaskDetailView, {
        task: { ...detail, activity: [] },
        theme: { colors },
        layout: { compact: false, platform: "web" },
        activeTab: "activity",
        activityState: { isOffline: true },
      }),
    );
  });
  text = renderedText(renderer);
  assert.match(text, /Offline · showing cached activity/);
  assert.match(text, /No activity recorded yet/);

  // Stale Refresh State
  await act(async () => {
    renderer.update(
      React.createElement(TaskDetailView, {
        task: detail,
        theme: { colors },
        layout: { compact: false, platform: "web" },
        activeTab: "activity",
        activityState: { isStale: true },
      }),
    );
  });
  text = renderedText(renderer);
  assert.match(text, /Activity may be stale · updating/);
});

test("TaskInspector resolves the exact native agent/workspace binding and degrades when unbound", async () => {
  const fixture = new DeterministicPlanningFixture();
  const queryClient = new ReactQuery.QueryClient({
    defaultOptions: {
      queries: { retry: false, gcTime: 0 },
      mutations: { retry: false },
    },
  });

  const { TaskInspector } = loadTaskInspectorModule(() => async () => undefined);

  // 1. Inspector with an exact engine-backed native binding.
  let renderer!: TestRenderer.ReactTestRenderer;
  await act(async () => {
    renderer = TestRenderer.create(
      React.createElement(
        ReactQuery.QueryClientProvider,
        { client: queryClient },
        React.createElement(TaskInspector, {
          client: fixture,
          context: "agent",
          host: { id: "host-a", label: "Host A" },
          agentId: "paseo-agent-task-0",
          workspaceId: "paseo-workspace-task-0",
          theme: { colors },
          layout: { compact: false, platform: "web" },
        }),
      ),
    );
  });

  await act(async () => {
    await waitForText(renderer, /DIR-00001/);
  });

  let text = renderedText(renderer);
  assert.match(text, /DIR-00001/);
  assert.match(text, /Contract-first planning UI shell/);
  assert.deepEqual(fixture.detailRequests.at(-1), {
    hostId: "host-a",
    context: "agent",
    taskId: null,
    paseoWorkspaceId: "paseo-workspace-task-0",
    paseoAgentId: "paseo-agent-task-0",
    afterCursor: null,
  });

  // 2. Inspector in agent context with no exact binding.
  const emptyFixture = new DeterministicPlanningFixture();

  const emptyQueryClient = new ReactQuery.QueryClient({
    defaultOptions: {
      queries: { retry: false, gcTime: 0 },
    },
  });

  let emptyRenderer!: TestRenderer.ReactTestRenderer;
  await act(async () => {
    emptyRenderer = TestRenderer.create(
      React.createElement(
        ReactQuery.QueryClientProvider,
        { client: emptyQueryClient },
        React.createElement(TaskInspector, {
          client: emptyFixture,
          context: "agent",
          host: { id: "host-a", label: "Host A" },
          workspaceId: "paseo-workspace-unbound",
          agentId: "agent-unbound",
          theme: { colors },
          layout: { compact: true, platform: "ios" },
        }),
      ),
    );
  });

  await act(async () => {
    await waitForText(emptyRenderer, /No Director Task is exactly bound/);
  });

  text = renderedText(emptyRenderer);
  assert.match(text, /No Director Task is exactly bound/);
});

test("native composer pill opens the exact Task Inspector context and cleans up", async () => {
  const { contributeTaskNavigation } = loadTaskNavigationModule();
  const labels = {
    [HostContract.WORKER_LABEL.project]: "project-1",
    [HostContract.WORKER_LABEL.rootWorkspace]: "workspace-root",
    [HostContract.WORKER_LABEL.workspace]: "director-workspace-1",
    [HostContract.WORKER_LABEL.executionWorkspace]: "paseo-workspace-1",
    [HostContract.WORKER_LABEL.task]: "task-1",
    [HostContract.WORKER_LABEL.run]: "run-1",
    [HostContract.WORKER_LABEL.role]: "task-agent",
  };
  const agent = {
    id: "paseo-agent-1",
    workspaceId: "paseo-workspace-1",
    labels,
  };
  let subscriber: ((update: unknown) => void) | null = null;
  const added: Record<string, unknown>[] = [];
  const removed: string[] = [];
  const opened: unknown[] = [];
  const client = {
    paseo: {
      agents: {
        async list() {
          return {
            entries: [{ agent }],
            pageInfo: { hasMore: false, nextCursor: null },
          };
        },
        subscribe(handler: (update: unknown) => void) {
          subscriber = handler;
          return () => removed.push("subscription");
        },
      },
    },
    addComposerPill(contribution: Record<string, unknown>) {
      added.push(contribution);
      return () => removed.push(String(contribution.agentId));
    },
    openPanel(id: string, options: unknown) {
      opened.push({ id, options });
    },
  };
  const cleanup = contributeTaskNavigation(client);
  for (let attempts = 0; attempts < 20 && added.length === 0; attempts += 1) {
    await new Promise((resolve) => setTimeout(resolve, 5));
  }
  assert.equal(added.length, 1);
  assert.equal(added[0]?.title, "Open Director Task task-1");
  await (added[0]?.onPress as () => Promise<void> | void)();
  assert.deepEqual(opened, [{
    id: "task-inspector",
    options: {
      workspaceId: "paseo-workspace-1",
      agentId: "paseo-agent-1",
      location: "explorer",
    },
  }]);
  (subscriber as ((update: unknown) => void) | null)?.({
    kind: "remove",
    agentId: "paseo-agent-1",
  });
  assert.ok(removed.includes("paseo-agent-1"));
  cleanup();
  assert.ok(removed.includes("subscription"));
});
