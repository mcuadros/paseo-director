// SPDX-License-Identifier: Apache-2.0

import assert from "node:assert/strict";
import { createRequire } from "node:module";
import { dirname, join } from "node:path";
import test from "node:test";
import { fileURLToPath, pathToFileURL } from "node:url";

type CompiledPlugin = { clientBundle: string; serverBundle: string };

async function compilePlugin(entry: string): Promise<CompiledPlugin> {
  const serverEntry = fileURLToPath(import.meta.resolve("@getpaseo/server"));
  const compiler = join(dirname(serverEntry), "plugins", "compiler.js");
  const module = await import(pathToFileURL(compiler).href) as {
    compilePlugin(entryPath: string): Promise<CompiledPlugin>;
  };
  return module.compilePlugin(entry);
}

const repositoryRoot = join(dirname(fileURLToPath(import.meta.url)), "..");

test("the Paseo 0.7.2 client bundle contains no Director server runtime symbol", async () => {
  const { clientBundle } = await compilePlugin(join(repositoryRoot, "index.ts"));
  for (const forbidden of [
    "startInstalledConnectorShell",
    "RuntimeConfigurationError",
    "ConnectorCredentialError",
    "DIRECTOR_ENGINE_URL",
    "DIRECTOR_PASEO_URL",
  ]) {
    assert.doesNotMatch(clientBundle, new RegExp(forbidden, "u"), `client bundle retained ${forbidden}`);
  }

  const nodeRequire = createRequire(import.meta.url);
  const noop = () => undefined;
  const hostRequire = (specifier: string): unknown => {
    if (specifier === "zod") return nodeRequire(specifier);
    if (specifier === "@getpaseo/plugin/server") {
      return { defineRpc: (definition: unknown) => definition };
    }
    if (specifier === "@getpaseo/plugin") {
      return { useHost: noop, useRpc: noop };
    }
    if (specifier === "@getpaseo/plugin/react-native") {
      return { Icon: noop, Modal: noop, ToastProvider: noop, useToast: () => ({ show: noop, error: noop }) };
    }
    if (specifier === "react") {
      const react = {
        createContext: () => ({ Provider: noop, Consumer: noop }),
        forwardRef: (value: unknown) => value,
        memo: (value: unknown) => value,
        useContext: noop,
        useEffect: noop,
        useMemo: noop,
        useRef: noop,
        useState: noop,
      };
      return { ...react, default: react };
    }
    if (specifier === "react/jsx-runtime") return { Fragment: Symbol("Fragment"), jsx: noop, jsxs: noop };
    if (specifier === "react-native") {
      return new Proxy({ StyleSheet: { create: (value: unknown) => value } }, { get: (target, key) => Reflect.get(target, key) ?? noop });
    }
    if (specifier === "@tanstack/react-query") return new Proxy({}, { get: () => noop });
    throw new Error(`unexpected host module ${specifier}`);
  };
  const factory = Function(`return ${clientBundle}`)() as (require: typeof hostRequire) => {
    default(plugin: Record<string, (...args: unknown[]) => unknown>): () => void | Promise<void>;
  };
  const contributions = {
    surfaces: [] as string[],
    sidebar: [] as string[],
    panels: [] as string[],
    commands: [] as string[],
    clientSides: 0,
    handles: 0,
  };
  const cleanup = factory(hostRequire).default({
    addSurface(id) { contributions.surfaces.push(String(id)); },
    addSidebarItem(value) { contributions.sidebar.push(String((value as { id: string }).id)); },
    addWorkspacePanel(value) { contributions.panels.push(String((value as { id: string }).id)); },
    addCommandCenterItem(value) { contributions.commands.push(String((value as { id: string }).id)); },
    addClientSide() { contributions.clientSides++; },
    handle() { contributions.handles++; },
  });
  assert.deepEqual(contributions, {
    surfaces: ["home"],
    sidebar: ["home"],
    panels: ["director-workers", "project-board", "task-inspector"],
    commands: [
      "open-director-workers",
      "create-director-administration-session",
      "return-to-director-board",
      "open-project-board",
      "open-task-inspector",
    ],
    clientSides: 1,
    handles: 0,
  });
  assert.equal(
    contributions.commands.filter((id) => id === "create-director-administration-session").length,
    1,
    "Project administration session command must be registered exactly once",
  );
  await cleanup();
});

test("the maintained pre-fix fixture reproduces the Paseo 0.7.2 server-symbol ReferenceError", async () => {
  const fixture = join(repositoryRoot, "tests", "fixtures", "paseo-0.7.2-client-referenceerror.ts");
  const { clientBundle } = await compilePlugin(fixture);
  const factory = Function(`return ${clientBundle}`)() as (require: (specifier: string) => unknown) => {
    default(plugin: { addSurface(id: string, surface: unknown): void }): void;
  };
  const contribution = factory(() => { throw new Error("fixture requested a host module"); }).default;
  assert.throws(
    () => contribution({ addSurface() {} }),
    (error: unknown) => error instanceof ReferenceError && /RuntimeConfigurationError is not defined/u.test(error.message),
  );
});
