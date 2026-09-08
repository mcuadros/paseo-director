// SPDX-License-Identifier: Apache-2.0

import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import test from "node:test";

import React from "react";
import TestRenderer, { act } from "react-test-renderer";
import * as ReactQuery from "@tanstack/react-query";
import * as JsxRuntime from "react/jsx-runtime";
import ts from "typescript";

import * as BoardRpc from "../rpc/board.shared.ts";
import * as BoardView from "../ui/board-view.client.ts";
import * as ShellLayout from "../ui/shell-layout.client.ts";

type Deferred = {
  resolve(value: unknown): void;
  reject(error: Error): void;
};

function loadProjectBoard(rpc: () => Promise<unknown>) {
  const source = readFileSync("ui/shells.client.tsx", "utf8");
  const compiled = ts.transpileModule(source, {
    fileName: "ui/shells.client.tsx",
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
        return { useRpc: () => rpc };
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
          Text: "Text",
          View: "View",
          StyleSheet: { create: (styles: unknown) => styles },
        };
      case "../rpc/board.shared.ts":
        return BoardRpc;
      case "./board-view.client.ts":
        return BoardView;
      case "./shell-layout.client.ts":
        return ShellLayout;
      default:
        throw new Error(`unexpected runtime import ${specifier}`);
    }
  };
  Function("require", "module", "exports", compiled)(
    require,
    module,
    module.exports,
  );
  return module.exports.ProjectBoard as React.ComponentType<Record<string, unknown>>;
}

async function takePending(pending: Deferred[]): Promise<Deferred> {
  const deadline = Date.now() + 5_000;
  while (Date.now() < deadline) {
    const request = pending.shift();
    if (request) return request;
    await new Promise((resolve) => setTimeout(resolve, 5));
  }
  throw new Error("ProjectBoard did not issue the expected RPC");
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

test("ProjectBoard renders loading, error, empty, data, and cached refresh states through RPC", async () => {
  (globalThis as typeof globalThis & { IS_REACT_ACT_ENVIRONMENT: boolean })
    .IS_REACT_ACT_ENVIRONMENT = true;
  const pending: Deferred[] = [];
  const rpc = () =>
    new Promise((resolve, reject) => pending.push({ resolve, reject }));
  const ProjectBoard = loadProjectBoard(rpc);
  const queryClient = new ReactQuery.QueryClient({
    defaultOptions: { queries: { retry: false } },
  });
  const props = {
    context: "workspace",
    workspaceId: "workspace-1",
    host: { id: "host-1", label: "Measured host" },
    layout: { compact: false, platform: "web" },
    theme: {
      colors: {
        accent: "accent",
        accentForeground: "accent-foreground",
        border: "border",
        foreground: "foreground",
        foregroundMuted: "muted",
        statusWarning: "warning",
        surface0: "surface-0",
        surface1: "surface-1",
        surface2: "surface-2",
      },
    },
  };
  let renderer!: TestRenderer.ReactTestRenderer;
  await act(async () => {
    renderer = TestRenderer.create(
      React.createElement(
        ReactQuery.QueryClientProvider,
        { client: queryClient },
        React.createElement(ProjectBoard, props),
      ),
    );
  });
  assert.match(renderedText(renderer), /Loading project tasks/);

  await act(async () => {
    (await takePending(pending)).reject(new Error("initial failure"));
    await waitForText(renderer, /Board data is unavailable/);
  });

  const retry = renderer.root.findByProps({
    accessibilityLabel: "Try loading project tasks again",
  });
  await act(async () => {
    retry.props.onPress();
    (await takePending(pending)).resolve({ schemaVersion: 1, cursor: "1", tasks: [] });
    await waitForText(renderer, /No tasks yet/);
  });

  const data = {
    schemaVersion: 1,
    cursor: "2",
    tasks: [
      {
        id: "task-1",
        projectId: "project-1",
        projectName: "Director",
        title: "Rendered Board task",
        state: "building",
        runNumber: "1",
        candidateSha: null,
      },
    ],
  };
  await act(async () => {
    const refreshing = queryClient.refetchQueries({
      queryKey: ["director", "board-snapshot"],
    });
    await waitForText(renderer, /Updating tasks/);
    (await takePending(pending)).resolve(data);
    await refreshing;
    await waitForText(renderer, /Rendered Board task/);
  });

  let cached = "";
  await act(async () => {
    const refreshing = queryClient.refetchQueries({
      queryKey: ["director", "board-snapshot"],
    });
    await waitForText(renderer, /Updating tasks/);
    (await takePending(pending)).reject(new Error("refresh failure"));
    await refreshing;
    cached = await waitForText(
      renderer,
      /Rendered Board task/,
      /Updating tasks|Board data is unavailable/,
    );
  });
  assert.match(cached, /Rendered Board task/);
  assert.doesNotMatch(cached, /Board data is unavailable/);

  await act(async () => renderer.unmount());
  queryClient.clear();
});
