// SPDX-License-Identifier: Apache-2.0
// The Director Console: the sidebar-anchored surface that is the Director
// layout (PLAN section 16.6).
//
// The host rebuilds this surface from the plugin registration on every bundle
// evaluation, so the layout cannot be lost the way a user-closable workspace
// panel can. The guarantee is restorable, not unclosable: this shell holds the
// tabs, and the three workspace panels stay registered as convenience.
//
// The shell owns exactly one piece of state — which tab is shown. Every tab
// body is the component the panels already render, unchanged.

import type { PluginSurfaceProps } from "@getpaseo/plugin";
import React, { useMemo, useState } from "react";
import { StyleSheet, Text, View } from "react-native";

import { Icon } from "./host-primitives.client.tsx";
import {
  AccessibilityProvider,
  AccessiblePressable,
  useAccessibilityAnnouncement,
  useAccessibilityPreferences,
  useResponsiveCompactLayout,
} from "./accessibility.client.tsx";
import {
  directorConsoleTabStates,
  directorConsoleVisibleTab,
  directorConsoleWorkerTargets,
  type DirectorConsoleTabId,
} from "./director-console-model.client.ts";
import { useDirectorHomeSnapshot } from "./director-home-query.client.ts";
import { useDirectorHostIdentity } from "./director-host.client.ts";
import { DirectorOverview } from "./director-home.client.tsx";
import { DirectorWorkersView } from "./director-workers-panel.client.tsx";
import { ProjectBoardSurface } from "./planning-surface.client.tsx";
import { shellMetrics } from "./shell-layout.client.ts";

export function DirectorConsole({ theme, layout, navigation, host }: PluginSurfaceProps) {
  const accessibilityPreferences = useAccessibilityPreferences();
  const compact = useResponsiveCompactLayout(layout.compact);
  const metrics = shellMetrics(compact);
  const directorIdentity = useDirectorHostIdentity();
  const hostId = directorIdentity.identity?.id ?? "";
  // The one Home snapshot query. The Overview tab reads the same hook under the
  // same cache key, and holding it here keeps the Workers fan-out available
  // while the Overview tab is not mounted.
  const home = useDirectorHomeSnapshot(hostId);
  const workerTargets = useMemo(
    () => directorConsoleWorkerTargets(home.data?.pages),
    [home.data?.pages],
  );
  const tabs = useMemo(
    () =>
      directorConsoleTabStates({
        workerTargets,
        snapshotResolved: home.data !== undefined,
      }),
    [home.data, workerTargets],
  );
  const [requestedTab, setRequestedTab] = useState<DirectorConsoleTabId>("overview");
  const activeTab = directorConsoleVisibleTab(tabs, requestedTab);
  const unavailable = tabs.filter((tab) => tab.unavailableReason !== null);
  useAccessibilityAnnouncement(
    `Director ${tabs.find((tab) => tab.id === activeTab)?.label ?? "Console"} tab`,
  );

  const styles = useMemo(
    () =>
      StyleSheet.create({
        console: { flex: 1, backgroundColor: theme.colors.surface0 },
        tabs: {
          flexDirection: "row",
          flexWrap: "wrap",
          alignItems: "center",
          gap: metrics.gap,
          paddingHorizontal: metrics.padding,
          paddingTop: metrics.padding,
          paddingBottom: metrics.gap,
          borderBottomWidth: accessibilityPreferences.highContrast ? 2 : 1,
          borderBottomColor: theme.colors.border,
        },
        tab: {
          minHeight: 44,
          minWidth: 44,
          flexDirection: "row",
          alignItems: "center",
          gap: 8,
          paddingHorizontal: 14,
          paddingVertical: 10,
          borderRadius: 8,
          borderWidth: accessibilityPreferences.highContrast ? 2 : 1,
          borderColor: theme.colors.border,
          backgroundColor: theme.colors.surface1,
        },
        tabSelected: {
          backgroundColor: theme.colors.accent,
          borderColor: theme.colors.accent,
        },
        tabDisabled: { backgroundColor: theme.colors.surface2 },
        tabText: { color: theme.colors.foreground, fontWeight: "600" },
        tabTextSelected: { color: theme.colors.accentForeground },
        tabTextDisabled: { color: theme.colors.foregroundMuted },
        reasons: {
          gap: 4,
          paddingHorizontal: metrics.padding,
          paddingBottom: metrics.gap,
        },
        reason: { color: theme.colors.foregroundMuted, lineHeight: 20 },
        body: { flex: 1 },
        workers: { flex: 1, gap: metrics.gap },
      }),
    [accessibilityPreferences.highContrast, metrics, theme],
  );

  return (
    <AccessibilityProvider
      focusColor={theme.colors.accent}
      preferences={accessibilityPreferences}
    >
      <View style={styles.console}>
        <View accessibilityRole="tablist" style={styles.tabs}>
          {tabs.map((tab) => {
            const selected = tab.id === activeTab;
            return (
              <AccessiblePressable
                accessibilityHint={tab.unavailableReason ?? `Shows the Director ${tab.label} tab`}
                accessibilityLabel={tab.label}
                accessibilityRole="tab"
                accessibilityState={{ disabled: !tab.enabled, selected }}
                disabled={!tab.enabled}
                key={tab.id}
                onPress={() => setRequestedTab(tab.id)}
                style={[
                  styles.tab,
                  selected && styles.tabSelected,
                  !tab.enabled && styles.tabDisabled,
                ]}
              >
                <Icon
                  color={selected ? theme.colors.accentForeground : theme.colors.foreground}
                  name={tab.icon}
                  size={16}
                />
                <Text
                  style={[
                    styles.tabText,
                    selected && styles.tabTextSelected,
                    !tab.enabled && styles.tabTextDisabled,
                  ]}
                >
                  {tab.label}
                </Text>
              </AccessiblePressable>
            );
          })}
        </View>
        {unavailable.length > 0 ? (
          <View accessibilityLiveRegion="polite" style={styles.reasons}>
            {unavailable.map((tab) => (
              <Text key={tab.id} style={styles.reason}>
                {tab.label} · {tab.unavailableReason}
              </Text>
            ))}
          </View>
        ) : null}
        <View style={styles.body}>
          {activeTab === "overview" ? (
            <DirectorOverview host={host} layout={layout} navigation={navigation} theme={theme} />
          ) : null}
          {activeTab === "board" ? (
            <ProjectBoardSurface host={host} layout={layout} navigation={navigation} theme={theme} />
          ) : null}
          {activeTab === "workers" ? (
            <View style={styles.workers}>
              {workerTargets.map((target) => (
                <DirectorWorkersView
                  heading={target.heading}
                  key={target.workspaceId}
                  layout={layout}
                  navigation={navigation}
                  theme={theme}
                  workspaceId={target.workspaceId}
                />
              ))}
            </View>
          ) : null}
        </View>
      </View>
    </AccessibilityProvider>
  );
}
