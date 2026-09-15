// SPDX-License-Identifier: Apache-2.0
// The single boundary for optional host UI primitives.
//
// Paseo marks "@getpaseo/plugin" external and the connected client supplies the
// module object at runtime, so its membership is a property of the client build
// rather than of the installed npm package. The published package declares
// `Icon` in its type declarations but exports no runtime value for it from any
// entry point, so a missing member type-checks and then renders as `undefined`.
//
// Optional decoration primitives are therefore read defensively here and nowhere
// else: absence degrades to a defined fallback for that primitive alone while
// every other surface still registers and renders. Reading a member can never
// throw, so client-bundle evaluation cannot abort. Never import
// "@getpaseo/plugin/react-native": client builds exist that do not map that
// specifier at all and reject it during evaluation, which aborts every
// contribution. `tools/ci/architecture-check.mjs` enforces both rules.

import type { PluginIconProps } from "@getpaseo/plugin";
import * as pluginSdk from "@getpaseo/plugin";
import React from "react";

const hostModule = pluginSdk as unknown as Record<string, unknown>;

const degraded: string[] = [];

/** Mirrors the host's own component check for a renderable element type. */
export function isRenderableComponent(value: unknown): boolean {
  if (typeof value === "function") return true;
  return typeof value === "object" && value !== null && "$$typeof" in value;
}

/**
 * Resolves one optional host component, falling back when the connected client
 * build does not supply a usable value for it. Every future optional primitive
 * must be resolved through this function rather than imported directly.
 */
export function optionalHostComponent<Props>(
  name: string,
  Fallback: React.ComponentType<Props>,
): React.ComponentType<Props> {
  const candidate = hostModule[name];
  if (isRenderableComponent(candidate)) {
    return candidate as React.ComponentType<Props>;
  }
  if (!degraded.includes(name)) {
    degraded.push(name);
    if (typeof console !== "undefined" && typeof console.warn === "function") {
      console.warn(
        `[Director] Optional host primitive ${name} is unavailable in this Paseo client; rendering its defined fallback.`,
      );
    }
  }
  return Fallback;
}

/**
 * Decoration-only fallback. Every Director icon labels an adjacent Text node, so
 * rendering nothing keeps the surface usable and the accessible name unchanged.
 * This matches the host's own Icon, which renders null for a name it cannot map.
 */
function IconFallback(_props: PluginIconProps): React.ReactElement | null {
  return null;
}

export const Icon = optionalHostComponent<PluginIconProps>("Icon", IconFallback);

/**
 * Optional primitives that degraded in this client build, in resolution order.
 * `dir-m6.27` reports the load-bearing client-bundle conditions through the
 * activation channel and can publish this list with them.
 */
export function degradedHostPrimitives(): readonly string[] {
  return [...degraded];
}
