// SPDX-License-Identifier: Apache-2.0

import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import test from "node:test";

import React from "react";
import * as JsxRuntime from "react/jsx-runtime";
import TestRenderer, { act } from "react-test-renderer";

import { loadClientModule } from "./client-module-loader.ts";

type AccessibilityModule = typeof import("../ui/accessibility.client.tsx");

function accessibilityModule() {
  const announcements: Array<{ message: string; options?: { queue?: boolean } }> = [];
  const listeners = new Map<string, (enabled: boolean) => void>();
  const accessibilityInfo = {
    async isReduceMotionEnabled() {
      return true;
    },
    async isHighTextContrastEnabled() {
      return true;
    },
    async isDarkerSystemColorsEnabled() {
      return false;
    },
    addEventListener(name: string, handler: (enabled: boolean) => void) {
      listeners.set(name, handler);
      return { remove: () => listeners.delete(name) };
    },
    announceForAccessibility(message: string) {
      announcements.push({ message });
    },
    announceForAccessibilityWithOptions(
      message: string,
      options: { queue?: boolean },
    ) {
      announcements.push({ message, options });
    },
  };
  const reactModule = { ...React, default: React, __esModule: true };
  const requireModule = (specifier: string): unknown => {
    switch (specifier) {
      case "react":
        return reactModule;
      case "react/jsx-runtime":
        return JsxRuntime;
      case "react-native":
        return {
          AccessibilityInfo: accessibilityInfo,
          Pressable: "Pressable",
          useWindowDimensions: () => ({ width: 1_440, height: 1_000, scale: 1, fontScale: 1 }),
        };
      default:
        throw new Error(`unexpected runtime import ${specifier}`);
    }
  };
  return {
    announcements,
    listeners,
    module: loadClientModule<AccessibilityModule>(
      "ui/accessibility.client.tsx",
      requireModule,
    ),
  };
}

function styleObjects(value: unknown): Record<string, unknown>[] {
  if (!Array.isArray(value)) return [];
  return value.filter(
    (entry): entry is Record<string, unknown> =>
      entry !== null && typeof entry === "object" && !Array.isArray(entry),
  );
}

test("shared Pressable behavior has targets, touch feedback, and a token focus ring", async () => {
  (globalThis as typeof globalThis & { IS_REACT_ACT_ENVIRONMENT: boolean })
    .IS_REACT_ACT_ENVIRONMENT = true;
  const { module } = accessibilityModule();
  let renderer!: TestRenderer.ReactTestRenderer;
  await act(async () => {
    renderer = TestRenderer.create(
      React.createElement(
        module.AccessibilityProvider,
        {
          focusColor: "token-accent",
          preferences: { highContrast: true, reduceMotion: true },
        },
        React.createElement(
          module.AccessiblePressable,
          { accessibilityLabel: "Touch action" },
          React.createElement("span", {}, "Touch action"),
        ),
      ),
    );
  });
  const pressable = renderer.root.find((node) => String(node.type) === "Pressable");
  assert.equal(pressable.props.accessibilityRole, "button");
  assert.equal(pressable.props.focusable, true);
  assert.equal(pressable.props.hitSlop, 4);
  assert.ok(
    styleObjects(pressable.props.style({ pressed: false })).some(
      (style) => style.minHeight === 44 && style.minWidth === 44,
    ),
  );
  assert.ok(
    styleObjects(pressable.props.style({ pressed: true })).some(
      (style) => style.opacity === 0.72,
    ),
  );

  await act(async () => pressable.props.onFocus({}));
  assert.ok(
    styleObjects(pressable.props.style({ pressed: false })).some(
      (style) =>
        style.outlineColor === "token-accent" &&
        style.outlineStyle === "solid" &&
        style.outlineWidth === 3,
    ),
  );
  await act(async () => pressable.props.onBlur({}));
  assert.equal(
    styleObjects(pressable.props.style({ pressed: false })).some(
      (style) => style.outlineWidth !== undefined,
    ),
    false,
  );
  await act(async () => renderer.unmount());
});

test("responsive compact layout covers host compact mode and narrow browser windows", () => {
  const { module } = accessibilityModule();
  assert.equal(module.isCompactWindow(true, 1_440), true);
  assert.equal(module.isCompactWindow(false, 390), true);
  assert.equal(module.isCompactWindow(false, 768), true);
  assert.equal(module.isCompactWindow(false, 1_199), true);
  assert.equal(module.isCompactWindow(false, 1_200), false);
  assert.equal(module.isCompactWindow(false, 1_440), false);
});

test("accessibility preferences and announcements work without web-only APIs", async () => {
  (globalThis as typeof globalThis & { IS_REACT_ACT_ENVIRONMENT: boolean })
    .IS_REACT_ACT_ENVIRONMENT = true;
  const { announcements, listeners, module } = accessibilityModule();
  function Probe({ message }: { message: string }) {
    const preferences = module.useAccessibilityPreferences();
    module.useAccessibilityAnnouncement(message);
    return React.createElement(
      "span",
      {},
      `${preferences.reduceMotion}:${preferences.highContrast}`,
    );
  }
  let renderer!: TestRenderer.ReactTestRenderer;
  await act(async () => {
    renderer = TestRenderer.create(
      React.createElement(Probe, { message: "Planning data loaded" }),
    );
  });
  assert.equal(renderer.root.findByType("span").children.join(""), "true:true");
  assert.deepEqual(announcements, [
    { message: "Planning data loaded", options: { queue: true } },
  ]);
  await act(async () => {
    renderer.update(React.createElement(Probe, { message: "Planning data loaded" }));
  });
  assert.equal(announcements.length, 1);
  await act(async () => {
    renderer.update(React.createElement(Probe, { message: "Task details opened" }));
  });
  assert.equal(announcements.at(-1)?.message, "Task details opened");
  assert.equal(listeners.size, 3);
  await act(async () => renderer.unmount());
  assert.equal(listeners.size, 0);
});

test("integrated product surfaces keep compact, native, reflow, and host-owned boundaries", () => {
  const sources = [
    "ui/director-home.client.tsx",
    "ui/project-operations.client.tsx",
    "ui/planning-surface.client.tsx",
    "ui/task-detail-view.client.tsx",
    "ui/task-inspector.client.tsx",
    "ui/director-workers-panel.client.tsx",
  ].map((file) => [file, readFileSync(file, "utf8")] as const);

  for (const [file, source] of sources) {
    assert.match(source, /useAccessibilityAnnouncement/);
    assert.match(source, /useAccessibilityPreferences/);
    assert.doesNotMatch(source, /<Pressable\b/);
    assert.doesNotMatch(source, /onHover(In|Out)|Animated|LayoutAnimation/);
    assert.doesNotMatch(source, /#[0-9a-f]{3,8}/i);
    assert.doesNotMatch(source, /\b(window|document|localStorage|location)\b/);
    assert.match(source, /theme\.colors\./, `${file} must use Paseo tokens`);
  }

  const planning = sources.find(([file]) => file.includes("planning-surface"))![1];
  assert.match(planning, /compact \? "list" : "board"/);
  assert.match(planning, /const visibleStates = compact \? \[selectedCompactLane\] : states/);
  assert.match(planning, /width: compact \? "100%" : 260/);
  assert.match(planning, /maxHeight: compact \? undefined : 560/);
  assert.match(planning, /flexDirection: compact \? "column" : "row"/);
  assert.doesNotMatch(planning, /numberOfLines=/);
  assert.match(planning, /<Modal[\s\S]*title="Filter tasks"/);
  assert.match(planning, /<Modal[\s\S]*title="Task details"/);

  const detail = sources.find(([file]) => file.includes("task-detail-view"))![1];
  assert.match(detail, /flexWrap: "wrap"/);
  assert.doesNotMatch(detail, /borderColor: "transparent"/);
  assert.doesNotMatch(detail, /numberOfLines=/);

  const home = sources.find(([file]) => file.includes("director-home"))![1];
  for (const integratedControl of [
    'accessibilityLabel="Preview Project Repair"',
    'accessibilityLabel="Close Doctor"',
    'accessibilityLabel="Confirm exact Repair"',
    'accessibilityLabel="Cancel Repair"',
    'doctor.isPending\n      ? "Running read-only Doctor"',
    'repair.isPending',
  ]) {
    assert.ok(home.includes(integratedControl), `missing ${integratedControl}`);
  }
  assert.match(home, /doctor\.isPending[\s\S]*accessibilityPreferences\.reduceMotion/);
  assert.match(home, /repair\.isPending[\s\S]*accessibilityPreferences\.reduceMotion/);

  const entry = readFileSync("index.ts", "utf8");
  assert.match(entry, /addSurface\("home", DirectorHome\)/);
  assert.match(entry, /id: "project-board"/);
  assert.match(entry, /id: "task-inspector"/);
  assert.match(entry, /addClientSide\(contributeTaskNavigation\)/);
  assert.doesNotMatch(entry, /addSidebarItem\([\s\S]*host picker/i);
});
