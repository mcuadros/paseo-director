// SPDX-License-Identifier: Apache-2.0

import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import test from "node:test";

import * as ReactQuery from "@tanstack/react-query";
import React from "react";
import * as JsxRuntime from "react/jsx-runtime";
import TestRenderer, { act } from "react-test-renderer";
import ts from "typescript";

import {
  PLANNING_CONTRACT_SHA256,
  PLANNING_CONTRACT_VERSION,
  homeSnapshotSchema,
} from "../generated/planning-contract.shared.ts";
import * as PlanningContract from "../generated/planning-contract.shared.ts";
import * as PlanningRpc from "../rpc/planning.shared.ts";
import * as HomeModel from "../ui/director-home-model.client.ts";
import * as ShellLayout from "../ui/shell-layout.client.ts";

type Deferred = { resolve(value: unknown): void; reject(error: Error): void };

function snapshot() {
  return homeSnapshotSchema.parse({
    schemaVersion: 1,
    contractVersion: PLANNING_CONTRACT_VERSION,
    contractHash: PLANNING_CONTRACT_SHA256,
    cursor: "9",
    page: {
      host: { id: "host-a", label: "Exact host", instanceId: "engine-a", state: "current", observedAt: "2026-09-11T16:00:00Z", maximumAgeMillis: "30000" },
      projects: [{
        id: "project-shared", version: "3", name: "Rendered Project", state: "active", health: "degraded",
        healthReasons: [{ code: "project_sync_unavailable", message: "Synchronization is unavailable", wakeCondition: null, humanActionRequired: false }],
        workspaces: [{ id: "workspace-director", key: "repository", name: "Repository", health: "healthy", paseoWorkspaceId: "native-board" }],
        organizer: { id: "organizer-shared", mode: "adopt", phase: "active", configurationState: "current", organizerRevision: "a".repeat(40), configurationSha256: "b".repeat(64), paseoWorkspaceId: "native-organizer" },
        lease: { state: "current", epoch: "2", expiresAt: "2026-09-11T16:05:00Z" },
        sync: { state: "unavailable", git: "unavailable", dynamicState: "unavailable", observedAt: null },
        tasks: { open: "2", done: "4", needsYou: "0" },
        activeWork: { building: "1", validating: "0", inReview: "0", ready: "0", total: "1" },
        needsYouReasons: [],
        actions: [
          { kind: "open_board", label: "Board", hostId: "host-a", projectId: "project-shared", enabled: true, unavailableReason: null, paseoWorkspaceId: "native-board", command: null, emphasis: "primary" },
          { kind: "open_organizer", label: "Organizer", hostId: "host-a", projectId: "project-shared", enabled: true, unavailableReason: null, paseoWorkspaceId: "native-organizer", command: null, emphasis: "secondary" },
          { kind: "doctor", label: "Doctor", hostId: "host-a", projectId: "project-shared", enabled: true, unavailableReason: null, paseoWorkspaceId: null, command: null, emphasis: "secondary" },
        ],
      }],
      totals: { projects: "1", healthy: "0", degraded: "1", paused: "0", needsYou: "0", activeWork: "1" },
      surfaceActions: [
        { kind: "create_project", label: "Create Project", hostId: "host-a", projectId: null, enabled: true, unavailableReason: null, paseoWorkspaceId: null, command: null, emphasis: "primary" },
        { kind: "adopt_organizer", label: "Adopt Organizer", hostId: "host-a", projectId: null, enabled: true, unavailableReason: null, paseoWorkspaceId: null, command: null, emphasis: "secondary" },
      ],
      totalProjects: "1",
      nextCursor: null,
    },
  });
}

function loadHomeComponent(homeRpc: () => Promise<unknown>, mutationRpc: () => Promise<unknown>, organizerRpc: (input: unknown) => Promise<unknown> = mutationRpc) {
  const source = readFileSync("ui/director-home.client.tsx", "utf8");
  const compiled = ts.transpileModule(source, {
    fileName: "ui/director-home.client.tsx",
    compilerOptions: { target: ts.ScriptTarget.ES2022, module: ts.ModuleKind.CommonJS, jsx: ts.JsxEmit.ReactJSX, esModuleInterop: true },
  }).outputText;
  const module = { exports: {} as Record<string, unknown> };
  const reactModule = { ...React, default: React, __esModule: true };
  const Modal = (({ children, open, title }: { children: React.ReactNode; open: boolean; title: string }) => open
    ? React.createElement("Modal", {}, React.createElement("Text", {}, title), children)
    : null) as React.ComponentType<Record<string, unknown>> & { Content: React.ComponentType<Record<string, unknown>> };
  Modal.Content = ({ children }: { children?: React.ReactNode }) => React.createElement("ModalContent", {}, children);
  const require = (specifier: string): unknown => {
    switch (specifier) {
      case "@getpaseo/plugin":
        return { useRpc: (contract: unknown) => contract === PlanningRpc.homeQueryRpc ? homeRpc : contract === PlanningRpc.organizerBootstrapRpc ? organizerRpc : mutationRpc };
      case "@getpaseo/plugin/react-native":
        return { Icon: "Icon", Modal, useToast: () => ({ show() {}, error() {} }) };
      case "@tanstack/react-query": return ReactQuery;
      case "react": return reactModule;
      case "react/jsx-runtime": return JsxRuntime;
      case "react-native":
        return { ActivityIndicator: "ActivityIndicator", Pressable: "Pressable", ScrollView: "ScrollView", Text: "Text", TextInput: "TextInput", View: "View", StyleSheet: { create: (styles: unknown) => styles } };
      case "../generated/planning-contract.shared.ts": return PlanningContract;
      case "../rpc/planning.shared.ts": return PlanningRpc;
      case "./director-home-model.client.ts": return HomeModel;
      case "./shell-layout.client.ts": return ShellLayout;
      default: throw new Error(`unexpected runtime import ${specifier}`);
    }
  };
  Function("require", "module", "exports", compiled)(require, module, module.exports);
  return module.exports.DirectorHome as React.ComponentType<Record<string, unknown>>;
}

async function takePending(pending: Deferred[]): Promise<Deferred> {
  const deadline = Date.now() + 5_000;
  while (Date.now() < deadline) {
    const value = pending.shift();
    if (value) return value;
    await new Promise((resolve) => setTimeout(resolve, 5));
  }
  throw new Error("DirectorHome did not issue the expected RPC");
}

function renderedText(renderer: TestRenderer.ReactTestRenderer): string {
  return renderer.root.findAll((node) => String(node.type) === "Text").flatMap((node) => node.children).filter((value): value is string => typeof value === "string").join(" ");
}

async function waitForText(renderer: TestRenderer.ReactTestRenderer, expected: RegExp): Promise<void> {
  const deadline = Date.now() + 5_000;
  while (Date.now() < deadline) {
    if (expected.test(renderedText(renderer))) return;
    await new Promise((resolve) => setTimeout(resolve, 5));
  }
  assert.match(renderedText(renderer), expected);
}

test("DirectorHome renders current data, exact navigation, entry points, and stale cached refusal", async () => {
  (globalThis as typeof globalThis & { IS_REACT_ACT_ENVIRONMENT: boolean }).IS_REACT_ACT_ENVIRONMENT = true;
  const pending: Deferred[] = [];
  const homeRpc = () => new Promise((resolve, reject) => pending.push({ resolve, reject }));
  const DirectorHome = loadHomeComponent(homeRpc, async () => { throw new Error("unused"); });
  const queryClient = new ReactQuery.QueryClient({ defaultOptions: { queries: { retry: false } } });
  const opened: string[] = [];
  const props = {
    host: { id: "host-a", label: "Client host label" },
    navigation: { openWorkspace: ({ workspaceId }: { workspaceId: string }) => opened.push(workspaceId), openAgent() {} },
    layout: { compact: true, platform: "android" },
    theme: { colors: { accent: "accent", accentForeground: "accent-foreground", border: "border", foreground: "foreground", foregroundMuted: "muted", statusDanger: "danger", statusSuccess: "success", statusWarning: "warning", surface0: "surface-0", surface1: "surface-1", surface2: "surface-2" } },
  };
  let renderer!: TestRenderer.ReactTestRenderer;
  await act(async () => {
    renderer = TestRenderer.create(React.createElement(ReactQuery.QueryClientProvider, { client: queryClient }, React.createElement(DirectorHome, props)));
  });
  assert.match(renderedText(renderer), /Loading Director Home/);

  await act(async () => {
    (await takePending(pending)).resolve(snapshot());
    await waitForText(renderer, /Rendered Project/);
  });
  assert.match(renderedText(renderer), /Project health Current work and attention.*Create Project Adopt Organizer Engine\s+engine-a.*Director is degraded/);
  const boardText = renderer.root.findAll((node) => String(node.type) === "Text" && node.children.join("") === "Board")[0]!;
  await act(async () => boardText.parent!.props.onPress());
  assert.deepEqual(opened, ["native-board"]);

  const createText = renderer.root.findAll((node) => String(node.type) === "Text" && node.children.join("") === "Create Project")[0]!;
  await act(async () => createText.parent!.props.onPress());
  assert.match(renderedText(renderer), /Nothing is applied without a fresh server-authenticated human confirmation/);

  await act(async () => {
    const refreshing = queryClient.refetchQueries({ queryKey: ["director", "home", "host-a"] });
    (await takePending(pending)).reject(new Error("offline"));
    await refreshing;
    await waitForText(renderer, /Host offline or snapshot stale/);
  });
  assert.match(renderedText(renderer), /Rendered Project/);
  const staleBoardText = renderer.root.findAll((node) => String(node.type) === "Text" && node.children.join("") === "Board")[0]!;
  assert.deepEqual(staleBoardText.parent!.props.accessibilityState, { disabled: true });
  await act(async () => staleBoardText.parent!.props.onPress?.());
  assert.deepEqual(opened, ["native-board"]);

  await act(async () => renderer.unmount());
  queryClient.clear();
});

test("DirectorHome Create entry submits Preview before exact confirmed Apply", async () => {
  (globalThis as typeof globalThis & { IS_REACT_ACT_ENVIRONMENT: boolean }).IS_REACT_ACT_ENVIRONMENT = true;
  const calls: Record<string, unknown>[] = [];
  const previewId = "c".repeat(64);
  const organizerRpc = async (raw: unknown) => {
    const input = raw as Record<string, unknown>;
    calls.push(input);
    if (input.kind === "create.preview") {
      return {
        schemaVersion: 1, contractVersion: PLANNING_CONTRACT_VERSION, contractHash: PLANNING_CONTRACT_SHA256,
        hostId: "host-a", cursor: "10", requestId: input.requestId, status: "preview",
        message: "Create Preview is ready for explicit human confirmation",
        preview: {
          id: previewId, kind: "create", requestId: input.requestId, projectId: input.projectId,
          projectName: input.projectName, repositoryPath: input.repositoryPath,
          organizerRevision: null, configurationSha256: "d".repeat(64), files: [],
          operations: ["activate exact revision in TaskStore"], valid: true, issues: [],
        },
        projectVersion: null,
      };
    }
    return {
      schemaVersion: 1, contractVersion: PLANNING_CONTRACT_VERSION, contractHash: PLANNING_CONTRACT_SHA256,
      hostId: "host-a", cursor: "11", requestId: input.requestId, status: "applied",
      message: "Project and Organizer were applied at the exact Preview", preview: null, projectVersion: "0",
    };
  };
  const DirectorHome = loadHomeComponent(async () => snapshot(), async () => { throw new Error("unused"); }, organizerRpc);
  const queryClient = new ReactQuery.QueryClient({ defaultOptions: { queries: { retry: false } } });
  const props = {
    host: { id: "host-a", label: "Client host label" },
    layout: { compact: false, platform: "web" },
    theme: { colors: { accent: "accent", accentForeground: "accent-foreground", border: "border", foreground: "foreground", foregroundMuted: "muted", statusDanger: "danger", statusSuccess: "success", statusWarning: "warning", surface0: "surface-0", surface1: "surface-1", surface2: "surface-2" } },
  };
  let renderer!: TestRenderer.ReactTestRenderer;
  await act(async () => {
    renderer = TestRenderer.create(React.createElement(ReactQuery.QueryClientProvider, { client: queryClient }, React.createElement(DirectorHome, props)));
  });
  await act(async () => waitForText(renderer, /Rendered Project/));
  const create = renderer.root.findAll((node) => String(node.type) === "Text" && node.children.join("") === "Create Project")[0]!;
  await act(async () => create.parent!.props.onPress());
  await act(async () => {
    renderer.root.findByProps({ accessibilityLabel: "Project ID" }).props.onChangeText("project-new");
    renderer.root.findByProps({ accessibilityLabel: "Project name" }).props.onChangeText("New Project");
    renderer.root.findByProps({ accessibilityLabel: "Organizer path on this host" }).props.onChangeText("/srv/director/project-new");
    renderer.root.findByProps({ accessibilityLabel: "Exact paseo-director.json" }).props.onChangeText('{"schemaVersion":1}');
  });
  const preview = renderer.root.findAll((node) => String(node.type) === "Text" && node.children.join("") === "Preview")[0]!;
  await act(async () => {
    preview.parent!.props.onPress();
    await waitForText(renderer, /Preview ready/);
  });
  const apply = renderer.root.findAll((node) => String(node.type) === "Text" && node.children.join("") === "Apply exact Preview")[0]!;
  await act(async () => {
    apply.parent!.props.onPress();
    await waitForText(renderer, /Rendered Project/);
  });
  assert.equal(calls.length, 2);
  assert.equal(calls[0]!.kind, "create.preview");
  assert.equal(calls[0]!.confirmed, undefined);
  assert.equal(calls[0]!.previewId, null);
  assert.equal(calls[1]!.kind, "create.apply");
  assert.equal(calls[1]!.confirmed, undefined);
  assert.equal(calls[1]!.previewId, previewId);
  assert.equal(calls[1]!.requestId, calls[0]!.requestId);

  await act(async () => renderer.unmount());
  queryClient.clear();
});
