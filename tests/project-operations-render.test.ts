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
  operationsReportSchema,
  type HomeProject,
} from "../generated/planning-contract.shared.ts";
import * as PlanningContract from "../generated/planning-contract.shared.ts";
import * as PlanningRpc from "../rpc/planning.shared.ts";
import * as ShellLayout from "../ui/shell-layout.client.ts";
import { loadClientModule, testAccessibilityInfo } from "./client-module-loader.ts";

function report(status: "healthy" | "degraded" | "partial_sync" | "offline" | "stale" | "needs_you" = "partial_sync") {
  const value = {
    schemaVersion: 1,
    contractVersion: PLANNING_CONTRACT_VERSION,
    contractHash: PLANNING_CONTRACT_SHA256,
    cursor: "41",
    hostId: "host-a",
    hostInstanceId: "engine-a",
    projectId: "project-a",
    projectName: "Project A",
    projectVersion: "3",
    observationId: "a".repeat(64),
    observedAt: "2026-09-12T01:00:00Z",
    maximumAgeMillis: "30000",
    status,
    reasons: status === "healthy" ? [] : [{ code: "taskstore_dolt_operation_failed", message: "The latest stream effect did not complete; its successful counterpart remains preserved.", wakeCondition: null, humanActionRequired: false }],
    hybrid: { mode: "hybrid", state: status === "stale" ? "stale" : status === "offline" ? "unavailable" : "current", reasonCode: status === "stale" ? "observation_stale" : status === "offline" ? "source_offline" : "observed", detail: "Event wakes and the periodic safety-net scan are reconciled from current authoritative facts.", terminalEventDispatch: "synchronous_wake_only", terminalTargetMillis: "1000", activeIntervalMillis: "30000", idleIntervalMillis: "300000", lostEventWatchdogMillis: "300000", lastCompletedAt: "2026-09-12T00:59:59Z", pendingWakeups: "0" },
    sync: {
      state: status === "healthy" ? "current" : status === "stale" ? "stale" : status === "offline" ? "unavailable" : "partial",
      streams: [
        { kind: "organizer_git", state: "current", reasonCode: "aligned", detail: "Local and remote exact revision fingerprints are aligned.", localRevisionFingerprint: "b".repeat(64), remoteRevisionFingerprint: "b".repeat(64), observedAt: "2026-09-12T01:00:00Z", lastSuccessAt: "2026-09-12T00:59:59Z", retryable: false },
        { kind: "taskstore_dolt", state: status === "healthy" ? "current" : status === "stale" ? "stale" : status === "offline" ? "unavailable" : "failed", reasonCode: status === "healthy" ? "aligned" : status === "stale" ? "observation_stale" : status === "offline" ? "remote_unavailable" : "operation_failed", detail: status === "healthy" ? "Local and remote exact revision fingerprints are aligned." : "The latest stream effect did not complete; its successful counterpart remains preserved.", localRevisionFingerprint: status === "healthy" ? "c".repeat(64) : null, remoteRevisionFingerprint: status === "healthy" ? "c".repeat(64) : null, observedAt: "2026-09-12T01:00:00Z", lastSuccessAt: "2026-09-12T00:59:59Z", retryable: status === "partial_sync" },
      ],
      automaticEnabled: true,
      debounceMillis: "60000",
      flushAtCriticalTransitions: true,
    },
    audit: { entries: [{ id: "d".repeat(64), sequence: "41", category: "candidate", action: "candidate_changed", outcome: "completed", actorKind: "unavailable", actorFingerprint: null, subjectFingerprint: "e".repeat(64), runFingerprint: "f".repeat(64), payloadIncluded: false }], entryLimit: "64", truncated: false, payloadsIncluded: false, pathsIncluded: false },
    logs: { state: "current", reason: null, entries: [{ sequence: "2", occurredAt: "2026-09-12T01:00:00Z", level: "warning", component: "dolt", code: "dolt_sync_failed", message: "TaskStore/Dolt synchronization did not complete; the Git outcome remains independent.", occurrences: "1" }], entryLimit: "128", returnedBytes: "188", retentionDays: "14", retentionBytes: "104857600", truncated: true, rawOutputIncluded: false },
    controls: status === "offline" || status === "stale" ? { syncAvailable: false, reconcileAvailable: false, reason: { code: "operations_effect_unavailable", message: "Current controls unavailable", wakeCondition: null, humanActionRequired: false } } : { syncAvailable: true, reconcileAvailable: true, reason: null },
    support: { previewAvailable: status !== "offline" && status !== "stale", reason: status !== "offline" && status !== "stale" ? null : { code: "support_bundle_unavailable", message: "Current facts required", wakeCondition: null, humanActionRequired: false }, uploadPolicy: "never" },
  };
  return operationsReportSchema.parse(value);
}

const project = {
  id: "project-a", version: "3", name: "Project A", state: "active", health: "degraded", healthReasons: [], workspaces: [],
  organizer: { id: "organizer-a", mode: "adopt", phase: "active", configurationState: "current", organizerRevision: "a".repeat(40), configurationSha256: "b".repeat(64), paseoWorkspaceId: null },
  lease: { state: "current", epoch: "1", expiresAt: "2026-09-12T01:05:00Z" },
  sync: { state: "partial", git: "current", dynamicState: "failed", observedAt: "2026-09-12T01:00:00Z" },
  tasks: { open: "1", done: "0", needsYou: "0" }, activeWork: { building: "1", validating: "0", inReview: "0", ready: "0", total: "1" }, needsYouReasons: [], actions: [],
} satisfies HomeProject;

function loadComponent(queryRpc: (input: unknown) => Promise<unknown>, mutationRpc: (input: unknown) => Promise<unknown>) {
  const source = readFileSync("ui/project-operations.client.tsx", "utf8");
  const compiled = ts.transpileModule(source, { fileName: "ui/project-operations.client.tsx", compilerOptions: { target: ts.ScriptTarget.ES2022, module: ts.ModuleKind.CommonJS, jsx: ts.JsxEmit.ReactJSX, esModuleInterop: true } }).outputText;
  const module = { exports: {} as Record<string, unknown> };
  const reactModule = { ...React, default: React, __esModule: true };
  const Modal = (({ children, open, title }: { children: React.ReactNode; open: boolean; title: string }) => open ? React.createElement("Modal", {}, React.createElement("Text", {}, title), children) : null) as React.ComponentType<Record<string, unknown>> & { Content: React.ComponentType<Record<string, unknown>> };
  Modal.Content = ({ children }: { children?: React.ReactNode }) => React.createElement("ModalContent", {}, children);
  const require = (specifier: string): unknown => {
    switch (specifier) {
      case "@getpaseo/plugin": return { useRpc: (contract: unknown) => contract === PlanningRpc.operationsQueryRpc ? queryRpc : mutationRpc };
      case "@getpaseo/plugin/react-native": return { Icon: "Icon", Modal, useToast: () => ({ show() {}, error() {} }) };
      case "@tanstack/react-query": return ReactQuery;
      case "react": return reactModule;
      case "react/jsx-runtime": return JsxRuntime;
      case "react-native": return { AccessibilityInfo: testAccessibilityInfo, ActivityIndicator: "ActivityIndicator", Pressable: "Pressable", ScrollView: "ScrollView", Text: "Text", View: "View", StyleSheet: { create: (styles: unknown) => styles }, useWindowDimensions: () => ({ width: 1_440, height: 1_000, scale: 1, fontScale: 1 }) };
      case "../generated/planning-contract.shared.ts": return PlanningContract;
      case "../rpc/planning.shared.ts": return PlanningRpc;
      case "./shell-layout.client.ts": return ShellLayout;
      case "./accessibility.client.tsx": return loadClientModule("ui/accessibility.client.tsx", require);
      default: throw new Error(`unexpected runtime import ${specifier}`);
    }
  };
  Function("require", "module", "exports", compiled)(require, module, module.exports);
  return module.exports.ProjectOperations as React.ComponentType<Record<string, unknown>>;
}

function text(renderer: TestRenderer.ReactTestRenderer): string {
  return renderer.root.findAll((node) => String(node.type) === "Text").flatMap((node) => node.children).filter((value): value is string => typeof value === "string").join(" ");
}

async function waitFor(renderer: TestRenderer.ReactTestRenderer, expected: RegExp) {
  const deadline = Date.now() + 3_000;
  while (Date.now() < deadline) {
    if (expected.test(text(renderer))) return;
    await new Promise((resolve) => setTimeout(resolve, 5));
  }
  assert.match(text(renderer), expected);
}

function props() {
  return { hostId: "host-a", open: true, project, onOpenChange() {}, layout: { compact: false, platform: "web" }, theme: { colors: { accent: "accent", accentForeground: "accent-fg", border: "border", foreground: "foreground", foregroundMuted: "muted", statusDanger: "danger", statusSuccess: "success", statusWarning: "warning", surface0: "surface0", surface1: "surface1", surface2: "surface2" } } };
}

test("Project operations renders partial sync, hybrid controls, safe audit, and bounded logs", async () => {
  (globalThis as typeof globalThis & { IS_REACT_ACT_ENVIRONMENT: boolean }).IS_REACT_ACT_ENVIRONMENT = true;
  const Component = loadComponent(async () => report(), async (raw) => {
    const input = raw as { kind: string; requestId: string };
    return { schemaVersion: 1, contractVersion: PLANNING_CONTRACT_VERSION, contractHash: PLANNING_CONTRACT_SHA256, hostId: "host-a", projectId: "project-a", cursor: "42", requestId: input.requestId, status: "applied", message: "Independent effects observed.", supportPreview: null, bundle: null, effect: { kind: input.kind, effectClass: "conditional_update", outcome: "observed", gitState: "current", dynamicState: "failed", successfulHalfRetried: false }, refusalCode: null };
  });
  const client = new ReactQuery.QueryClient();
  let renderer!: TestRenderer.ReactTestRenderer;
  await act(async () => { renderer = TestRenderer.create(React.createElement(ReactQuery.QueryClientProvider, { client }, React.createElement(Component, props()))); });
  await act(async () => waitFor(renderer, /Partial sync/));
  assert.match(text(renderer), /Hybrid\s+reconciliation.*Event \+ periodic.*Organizer Git.*Dynamic state \/ Dolt.*successful counterpart remains preserved/i);
  const audit = renderer.root.findAll((node) => String(node.type) === "Text" && node.children.join("") === "Audit")[0]!;
  await act(async () => audit.parent!.props.onPress());
  assert.match(text(renderer), /payloads\s+excluded.*paths\s+excluded.*Candidate changed.*payload excluded/i);
  const logs = renderer.root.findAll((node) => String(node.type) === "Text" && node.children.join("") === "Logs")[0]!;
  await act(async () => logs.parent!.props.onPress());
  assert.match(text(renderer), /Structured technical codes only.*retention\s+14\s+days \/\s+104857600\s+bytes.*Git outcome remains independent/i);
  await act(async () => renderer.unmount());
  client.clear();
});

test("support bundle requires Preview then separate generation and shows local no-upload evidence", async () => {
  const calls: Record<string, unknown>[] = [];
  const Component = loadComponent(async () => report(), async (raw) => {
    const input = raw as Record<string, unknown>;
    calls.push(input);
    const base = { schemaVersion: 1, contractVersion: PLANNING_CONTRACT_VERSION, contractHash: PLANNING_CONTRACT_SHA256, hostId: "host-a", projectId: "project-a", cursor: "41", requestId: input.requestId };
    if (input.kind === "support.preview") return { ...base, status: "preview", message: "Preview ready", bundle: null, effect: null, refusalCode: null, supportPreview: { id: "a".repeat(64), requestId: input.requestId, projectFingerprint: "b".repeat(64), cursor: "41", projectVersion: "3", items: ["manifest.json", "health.json", "audit.json", "logs.json"].map((name) => ({ name, description: `${name} allowlist` })), excluded: ["source and diffs", "prompts and conversations", "environment", "credentials", "raw logs", "TaskStore rows", "Organizer contents", "full paths"], estimatedBytes: "2048", scanStatus: "allowlist_safe", localOnly: true, uploadPolicy: "never", outputFileName: "director-support-bbbbbbbbbbbb-aaaaaaaaaaaa.zip", valid: true, issues: [], confirmation: "Generate locally with mode 0600; never upload automatically." } };
    return { ...base, status: "applied", message: "Generated locally", supportPreview: null, effect: null, refusalCode: null, bundle: { bundleId: "a".repeat(64), fileName: "director-support-bbbbbbbbbbbb-aaaaaaaaaaaa.zip", sha256: "c".repeat(64), bytes: "1024", permission: "0600", generatedAt: "2026-09-12T01:00:00Z", uploadAttempted: false } };
  });
  const client = new ReactQuery.QueryClient();
  let renderer!: TestRenderer.ReactTestRenderer;
  await act(async () => { renderer = TestRenderer.create(React.createElement(ReactQuery.QueryClientProvider, { client }, React.createElement(Component, { ...props(), initialTab: "support" }))); });
  await act(async () => waitFor(renderer, /Preview support bundle/));
  const preview = renderer.root.findAll((node) => String(node.type) === "Text" && node.children.join("") === "Preview support bundle")[0]!;
  await act(async () => { preview.parent!.props.onPress(); await waitFor(renderer, /Preview safe to generate/); });
  assert.match(text(renderer), /Always excluded.*full paths.*never upload automatically/i);
  const generate = renderer.root.findAll((node) => String(node.type) === "Text" && node.children.join("") === "Generate local bundle")[0]!;
  await act(async () => { generate.parent!.props.onPress(); await waitFor(renderer, /Support bundle generated locally/); });
  assert.match(text(renderer), /mode\s+0600.*Automatic upload attempted:\s+no — never/i);
  assert.equal(calls.length, 2);
  assert.equal(calls[0]!.kind, "support.preview");
  assert.equal(calls[0]!.confirmed, false);
  assert.equal(calls[1]!.kind, "support.generate");
  assert.equal(calls[1]!.confirmed, true);
  assert.equal(calls[1]!.previewId, "a".repeat(64));
  assert.equal(calls[1]!.requestId, calls[0]!.requestId);
  await act(async () => renderer.unmount());
  client.clear();
});

test("offline and transport-error states disable controls without fallback", async () => {
  for (const [name, queryRpc, expected] of [
    ["offline", async () => report("offline"), /Offline.*Controls disabled/],
    ["stale", async () => report("stale"), /Stale.*Controls disabled/],
    ["error", async () => { throw new Error("offline"); }, /Operations unavailable.*No Sync, fact scan, bundle write, or upload was attempted/],
  ] as const) {
    const Component = loadComponent(queryRpc, async () => { throw new Error("must not mutate"); });
    const client = new ReactQuery.QueryClient();
    let renderer!: TestRenderer.ReactTestRenderer;
    await act(async () => { renderer = TestRenderer.create(React.createElement(ReactQuery.QueryClientProvider, { client }, React.createElement(Component, props()))); });
    await act(async () => waitFor(renderer, expected));
    assert.match(text(renderer), expected, name);
    await act(async () => renderer.unmount());
    client.clear();
  }
});

test("operations contract mutations cannot enable raw output, payloads, paths, uploads, or swapped streams", () => {
  const current = report();
  const mutations = [
    { ...current, audit: { ...current.audit, payloadsIncluded: true } },
    { ...current, audit: { ...current.audit, pathsIncluded: true } },
    { ...current, logs: { ...current.logs, rawOutputIncluded: true } },
    { ...current, support: { ...current.support, uploadPolicy: "automatic" } },
    { ...current, sync: { ...current.sync, streams: [current.sync.streams[1], current.sync.streams[0]] } },
    { ...current, sync: { ...current.sync, streams: [{ ...current.sync.streams[0], localRevisionFingerprint: "/home/private" }, current.sync.streams[1]] } },
  ];
  for (const mutation of mutations) {
    assert.equal(operationsReportSchema.safeParse(mutation).success, false);
  }
});
