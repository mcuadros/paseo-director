// SPDX-License-Identifier: Apache-2.0
// Client-only React Native surfaces render engine-owned projections.

import type {
  PluginAgentPanelProps,
  PluginSurfaceProps,
  PluginWorkspacePanelProps,
} from "@getpaseo/plugin";
import React, { useMemo, useState } from "react";
import {
  Pressable,
  ScrollView,
  StyleSheet,
  Text,
  View,
} from "react-native";

import { shellMetrics } from "./shell-layout.client.ts";

const BOARD_LANES = [
  "Queued",
  "Building",
  "Validating",
  "In review",
  "Ready",
] as const;

export function DirectorHome({ theme, layout }: PluginSurfaceProps) {
  const metrics = shellMetrics(layout.compact);
  const styles = useMemo(
    () =>
      StyleSheet.create({
        screen: {
          flex: 1,
          padding: metrics.padding,
          gap: metrics.gap,
          backgroundColor: theme.colors.surface0,
        },
        eyebrow: {
          color: theme.colors.foregroundMuted,
          fontSize: 12,
          textTransform: "uppercase",
        },
        title: {
          color: theme.colors.foreground,
          fontSize: metrics.titleSize,
          fontWeight: "700",
        },
        body: { color: theme.colors.foregroundMuted, lineHeight: 20 },
        actions: {
          flexDirection: layout.compact ? "column" : "row",
          gap: metrics.gap,
        },
        action: {
          paddingHorizontal: 16,
          paddingVertical: 12,
          borderRadius: 10,
          borderWidth: 1,
          borderColor: theme.colors.border,
          backgroundColor: theme.colors.surface2,
        },
        actionText: { color: theme.colors.foregroundMuted, fontWeight: "600" },
        card: {
          padding: 16,
          gap: 6,
          borderRadius: 12,
          borderWidth: 1,
          borderColor: theme.colors.border,
          backgroundColor: theme.colors.surface1,
        },
        cardTitle: { color: theme.colors.foreground, fontWeight: "600" },
        status: { color: theme.colors.statusWarning },
      }),
    [layout.compact, metrics, theme],
  );

  return (
    <ScrollView contentContainerStyle={styles.screen}>
      <Text style={styles.eyebrow}>Standalone engine host</Text>
      <Text accessibilityRole="header" style={styles.title}>
        Director
      </Text>
      <Text style={styles.body}>
        Plan, execute, review, and deliver multi-repository work. This scaffold
        renders the complete host shell without activating product workflow.
      </Text>
      <View style={styles.actions}>
        <Pressable accessibilityRole="button" disabled style={styles.action}>
          <Text style={styles.actionText}>Create Project</Text>
        </Pressable>
        <Pressable accessibilityRole="button" disabled style={styles.action}>
          <Text style={styles.actionText}>Adopt Organizer</Text>
        </Pressable>
      </View>
      <View style={styles.card}>
        <Text style={styles.cardTitle}>Project health</Text>
        <Text style={styles.status}>Engine setup required</Text>
        <Text style={styles.body}>
          Doctor, Sync, Pause, and needs-you actions will appear here when their
          engine projections are implemented.
        </Text>
      </View>
    </ScrollView>
  );
}

export function ProjectBoard({
  theme,
  layout,
}: PluginWorkspacePanelProps) {
  const [view, setView] = useState<"board" | "list">(
    layout.compact ? "list" : "board",
  );
  const metrics = shellMetrics(layout.compact);
  const styles = useMemo(
    () =>
      StyleSheet.create({
        screen: {
          flexGrow: 1,
          padding: metrics.padding,
          gap: metrics.gap,
          backgroundColor: theme.colors.surface0,
        },
        header: {
          flexDirection: layout.compact ? "column" : "row",
          justifyContent: "space-between",
          gap: metrics.gap,
        },
        title: {
          color: theme.colors.foreground,
          fontSize: metrics.titleSize,
          fontWeight: "700",
        },
        controls: { flexDirection: "row", gap: 8 },
        control: {
          paddingHorizontal: 12,
          paddingVertical: 8,
          borderRadius: 8,
          backgroundColor: theme.colors.surface2,
        },
        selectedControl: { backgroundColor: theme.colors.accent },
        controlText: { color: theme.colors.foreground },
        selectedControlText: { color: theme.colors.accentForeground },
        board: {
          flexDirection: metrics.boardDirection,
          gap: metrics.gap,
        },
        lane: {
          minWidth: layout.compact ? undefined : 180,
          flex: layout.compact ? undefined : 1,
          padding: 12,
          gap: 8,
          borderRadius: 10,
          borderWidth: 1,
          borderColor: theme.colors.border,
          backgroundColor: theme.colors.surface1,
        },
        laneTitle: { color: theme.colors.foreground, fontWeight: "600" },
        empty: { color: theme.colors.foregroundMuted },
        list: {
          padding: 16,
          borderRadius: 10,
          borderWidth: 1,
          borderColor: theme.colors.border,
          backgroundColor: theme.colors.surface1,
        },
      }),
    [layout.compact, metrics, theme],
  );

  return (
    <ScrollView contentContainerStyle={styles.screen}>
      <View style={styles.header}>
        <Text accessibilityRole="header" style={styles.title}>
          Project tasks
        </Text>
        <View accessibilityRole="tablist" style={styles.controls}>
          {(["board", "list"] as const).map((choice) => {
            const selected = choice === view;
            return (
              <Pressable
                accessibilityRole="tab"
                accessibilityState={{ selected }}
                key={choice}
                onPress={() => setView(choice)}
                style={[styles.control, selected && styles.selectedControl]}
              >
                <Text
                  style={[
                    styles.controlText,
                    selected && styles.selectedControlText,
                  ]}
                >
                  {choice === "board" ? "Board" : "List"}
                </Text>
              </Pressable>
            );
          })}
        </View>
      </View>
      {view === "board" ? (
        <View style={styles.board}>
          {BOARD_LANES.map((lane) => (
            <View key={lane} style={styles.lane}>
              <Text style={styles.laneTitle}>{lane}</Text>
              <Text style={styles.empty}>No engine projection yet</Text>
            </View>
          ))}
        </View>
      ) : (
        <View style={styles.list}>
          <Text style={styles.empty}>
            Epic and Task rows will render from the same engine query.
          </Text>
        </View>
      )}
    </ScrollView>
  );
}

export function TaskInspector({
  theme,
  layout,
}: PluginAgentPanelProps) {
  const metrics = shellMetrics(layout.compact);
  const styles = useMemo(
    () =>
      StyleSheet.create({
        screen: {
          flex: 1,
          padding: metrics.padding,
          gap: metrics.gap,
          backgroundColor: theme.colors.surface0,
        },
        title: {
          color: theme.colors.foreground,
          fontSize: metrics.titleSize,
          fontWeight: "700",
        },
        tabs: { flexDirection: "row", flexWrap: "wrap", gap: 8 },
        tab: {
          paddingHorizontal: 12,
          paddingVertical: 8,
          borderRadius: 8,
          backgroundColor: theme.colors.surface2,
        },
        tabText: { color: theme.colors.foreground },
        body: { color: theme.colors.foregroundMuted, lineHeight: 20 },
      }),
    [metrics, theme],
  );

  return (
    <View style={styles.screen}>
      <Text accessibilityRole="header" style={styles.title}>
        Task Inspector
      </Text>
      <View accessibilityRole="tablist" style={styles.tabs}>
        {["Details", "Execution", "Activity"].map((tab) => (
          <View accessibilityRole="tab" key={tab} style={styles.tab}>
            <Text style={styles.tabText}>{tab}</Text>
          </View>
        ))}
      </View>
      <Text style={styles.body}>
        Exact Candidate, validation, review, activity, and Open agent controls
        will render here from Director Engine projections.
      </Text>
    </View>
  );
}
