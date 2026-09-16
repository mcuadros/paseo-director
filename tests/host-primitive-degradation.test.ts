// SPDX-License-Identifier: Apache-2.0
// Optional host UI primitives must degrade instead of removing Director.
//
// Paseo marks "@getpaseo/plugin" external and the connected client build
// supplies the module object, so its membership differs between shipped
// clients. These tests compile the real client bundle with the Paseo daemon
// compiler and evaluate it against host maps transcribed from shipped desktop
// builds: Paseo 0.6.1 maps the SDK without `Icon` and rejects
// "@getpaseo/plugin/react-native" outright, while Paseo 0.7.2 maps `Icon`.
// Verifying only against a client that maps `Icon` is a false pass.

import assert from "node:assert/strict";
import { createRequire } from "node:module";
import { dirname, join } from "node:path";
import test from "node:test";
import { fileURLToPath, pathToFileURL } from "node:url";

import * as ReactQuery from "@tanstack/react-query";
import React from "react";
import * as JsxRuntime from "react/jsx-runtime";
import TestRenderer, { act } from "react-test-renderer";

import { loadClientModule, testAccessibilityInfo } from "./client-module-loader.ts";

type CompiledPlugin = { clientBundle: string; serverBundle: string };

const repositoryRoot = join(dirname(fileURLToPath(import.meta.url)), "..");
const nodeRequire = createRequire(import.meta.url);

async function compilePlugin(entry: string): Promise<CompiledPlugin> {
  const serverEntry = fileURLToPath(import.meta.resolve("@getpaseo/server"));
  const compiler = join(dirname(serverEntry), "plugins", "compiler.js");
  const module = (await import(pathToFileURL(compiler).href)) as {
    compilePlugin(entryPath: string): Promise<CompiledPlugin>;
  };
  return module.compilePlugin(entry);
}

const noop = () => undefined;

/** Load-bearing members every observed client build maps. See `dir-m6.27`. */
const loadBearingSdk = {
  defineAttachmentSource: (value: unknown) => value,
  defineRpc: (value: unknown) => value,
  usePaseo: () => ({ agents: { subscribe: () => () => {} } }),
  useAgent: noop,
  useWorkspace: noop,
  useRpc: () => async () => new Promise(() => {}),
};

const reactNativeStub = new Proxy(
  {
    AccessibilityInfo: testAccessibilityInfo,
    StyleSheet: { create: (value: unknown) => value, hairlineWidth: 1, absoluteFillObject: {} },
    useWindowDimensions: () => ({ width: 1_440, height: 1_000, scale: 1, fontScale: 1 }),
    Platform: { OS: "web", select: (value: Record<string, unknown>) => value.default ?? value.web },
    Dimensions: { get: () => ({ width: 1_440, height: 1_000 }) },
  },
  { get: (target, key) => Reflect.get(target, key) ?? String(key) },
);

type HostBuild = { sdk: Record<string, unknown>; mapsReactNative: boolean };

const HOST_BUILDS: Record<string, HostBuild> = {
  // Paseo 0.6.1 desktop renderer: no Icon, and the react-native specifier is
  // unknown, so importing it aborts evaluation of every contribution.
  "desktop-0.6.1": { sdk: { ...loadBearingSdk }, mapsReactNative: false },
  // Paseo 0.7.2 desktop renderer and the 0.7.2 daemon web UI.
  "desktop-0.7.2": {
    sdk: {
      ...loadBearingSdk,
      Icon: ({ name }: { name: string }) => React.createElement("Icon", { name }),
    },
    mapsReactNative: true,
  },
};

function hostRequire(build: HostBuild, requested: string[]) {
  return (specifier: string): unknown => {
    requested.push(specifier);
    if (specifier === "@getpaseo/plugin") return build.sdk;
    if (specifier === "@getpaseo/plugin/server") {
      return { defineRpc: (value: unknown) => value, defineAttachmentSource: (value: unknown) => value };
    }
    if (specifier === "@getpaseo/plugin/react-native") {
      if (!build.mapsReactNative) {
        throw new Error(`Module "${specifier}" is not available in plugin client code`);
      }
      return { Icon: build.sdk.Icon, Modal: () => null, useToast: () => ({ show() {}, error() {} }) };
    }
    if (specifier === "react") return { ...React, default: React, __esModule: true };
    if (specifier === "react/jsx-runtime") return JsxRuntime;
    if (specifier === "react-native") return reactNativeStub;
    if (specifier === "@tanstack/react-query") return ReactQuery;
    if (specifier === "zod") return nodeRequire(specifier);
    throw new Error(`Module "${specifier}" is not available in plugin client code`);
  };
}

const surfaceProps = {
  host: { id: "host-a", label: "Director" },
  navigation: { openWorkspace: noop, openAgent: noop, openSurface: noop, openPanel: noop },
  layout: { compact: false, platform: "web" },
  theme: {
    colors: {
      accent: "accent", accentForeground: "accent-foreground", border: "border",
      foreground: "foreground", foregroundMuted: "muted", statusDanger: "danger",
      statusSuccess: "success", statusWarning: "warning", surface0: "surface-0",
      surface1: "surface-1", surface2: "surface-2",
    },
  },
};

type Contribution = { id: string; Component: React.ComponentType<Record<string, unknown>> };

async function evaluateAgainst(clientBundle: string, buildName: string) {
  const build = HOST_BUILDS[buildName];
  assert.ok(build, `unknown host build ${buildName}`);
  const requested: string[] = [];
  const surfaces: Contribution[] = [];
  const panels: Contribution[] = [];
  const sidebarItems: string[] = [];
  const commands: string[] = [];

  const factory = Function(`return ${clientBundle}`)() as (
    require: (specifier: string) => unknown,
  ) => { default(plugin: Record<string, unknown>): () => void | Promise<void> };

  const cleanup = factory(hostRequire(build, requested)).default({
    addSurface(id: string, Component: Contribution["Component"]) { surfaces.push({ id, Component }); },
    addSidebarItem(value: { id: string }) { sidebarItems.push(value.id); },
    addWorkspacePanel(value: Contribution) { panels.push(value); },
    addCommandCenterItem(value: { id: string }) { commands.push(value.id); },
  });

  const rendered: Record<string, string> = {};
  for (const target of [...surfaces, ...panels]) {
    const queryClient = new ReactQuery.QueryClient({ defaultOptions: { queries: { retry: false } } });
    try {
      await act(async () => {
        TestRenderer.create(
          React.createElement(
            ReactQuery.QueryClientProvider,
            { client: queryClient },
            React.createElement(target.Component, surfaceProps),
          ),
        );
      });
      rendered[target.id] = "rendered";
    } catch (error) {
      rendered[target.id] = `threw: ${String((error as Error).message).split("\n")[0]}`;
    }
  }
  await cleanup();
  return { requested, sidebarItems, commands, rendered, surfaces: surfaces.map((s) => s.id), panels: panels.map((p) => p.id) };
}

test("every Director surface registers and renders on a client build that omits an optional primitive", async () => {
  (globalThis as typeof globalThis & { IS_REACT_ACT_ENVIRONMENT: boolean }).IS_REACT_ACT_ENVIRONMENT = true;
  const { clientBundle } = await compilePlugin(join(repositoryRoot, "index.ts"));

  for (const buildName of ["desktop-0.6.1", "desktop-0.7.2"]) {
    const result = await evaluateAgainst(clientBundle, buildName);

    assert.deepEqual(result.surfaces, ["home"], `${buildName} surfaces`);
    assert.deepEqual(result.sidebarItems, ["home"], `${buildName} sidebar`);
    assert.deepEqual(
      result.panels,
      ["director-workers", "project-board", "task-inspector"],
      `${buildName} panels`,
    );
    assert.deepEqual(
      result.commands,
      [
        "open-director-workers",
        "create-director-administration-session",
        "return-to-director-board",
        "open-project-board",
        "open-task-inspector",
      ],
      `${buildName} Command Center items`,
    );
    assert.deepEqual(
      result.rendered,
      {
        home: "rendered",
        "director-workers": "rendered",
        "project-board": "rendered",
        "task-inspector": "rendered",
      },
      `${buildName} must render every surface and panel`,
    );
    assert.equal(
      result.requested.includes("@getpaseo/plugin/react-native"),
      false,
      `${buildName} must never request the optional client-only SDK submodule`,
    );
  }
});

type HostPrimitiveAdapter = {
  optionalHostComponent<P>(name: string, fallback: React.ComponentType<P>): React.ComponentType<P>;
  isRenderableComponent(value: unknown): boolean;
  Icon: React.ComponentType<{ name: string }>;
  degradedHostPrimitives(): readonly string[];
};

function loadAdapter(sdk: Record<string, unknown>): HostPrimitiveAdapter {
  return loadClientModule<HostPrimitiveAdapter>("ui/host-primitives.client.tsx", (specifier) => {
    if (specifier === "@getpaseo/plugin") return sdk;
    if (specifier === "react") return { ...React, default: React, __esModule: true };
    throw new Error(`unexpected runtime import ${specifier}`);
  });
}

test("the adapter uses a supplied primitive and falls back for an absent one", async () => {
  (globalThis as typeof globalThis & { IS_REACT_ACT_ENVIRONMENT: boolean }).IS_REACT_ACT_ENVIRONMENT = true;
  const Supplied: React.ComponentType<{ name: string }> = () => null;
  const Fallback: React.ComponentType<Record<string, unknown>> = () => null;

  const degradedAdapter = loadAdapter({ ...loadBearingSdk });
  assert.deepEqual(degradedAdapter.degradedHostPrimitives(), ["Icon"]);
  let fallbackRenderer: TestRenderer.ReactTestRenderer | null = null;
  await act(async () => {
    fallbackRenderer = TestRenderer.create(
      React.createElement(degradedAdapter.Icon, { name: "Clock3" }),
    );
  });
  assert.equal(
    (fallbackRenderer as unknown as TestRenderer.ReactTestRenderer).toJSON(),
    null,
    "the Icon fallback renders nothing and never an invalid element type",
  );

  const suppliedAdapter = loadAdapter({ ...loadBearingSdk, Icon: Supplied });
  assert.deepEqual(suppliedAdapter.degradedHostPrimitives(), []);
  assert.equal(suppliedAdapter.Icon, Supplied, "a supplied primitive is used unchanged");

  // The rule is generic: any member the client build does not supply as a
  // usable component degrades to the caller's fallback.
  for (const name of ["Icon", "Modal", "useToast", "SomeFuturePrimitive"]) {
    assert.equal(degradedAdapter.optionalHostComponent(name, Fallback), Fallback, name);
  }
  assert.equal(
    degradedAdapter.optionalHostComponent("useRpc", Fallback),
    loadBearingSdk.useRpc,
    "a member the host supplies is used rather than replaced",
  );

  for (const renderable of [() => null, React.memo(() => null), React.forwardRef(() => null)]) {
    assert.equal(degradedAdapter.isRenderableComponent(renderable), true);
  }
  for (const unusable of [undefined, null, "Icon", 7, {}]) {
    assert.equal(degradedAdapter.isRenderableComponent(unusable), false, String(unusable));
  }
});
