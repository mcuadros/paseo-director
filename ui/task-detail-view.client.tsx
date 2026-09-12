// SPDX-License-Identifier: Apache-2.0
// Client-only React Native presentation of engine-owned task detail projections.

import type {
  PluginHostProps,
  PluginSurfaceProps,
  PluginTheme,
} from "@getpaseo/plugin";
import { Icon } from "@getpaseo/plugin/react-native";
import React, { useMemo, useState } from "react";
import {
  ActivityIndicator,
  ScrollView,
  StyleSheet,
  Text,
  View,
} from "react-native";

import {
  type AllowedAction,
  type ConfigurationOverride,
  type ConfigurationPreview,
  type ConfigurationValue,
  type DerivedState,
  type PlanningMutationIntent,
  type TaskDetail,
} from "../generated/planning-contract.shared.ts";
import { shellMetrics } from "./shell-layout.client.ts";
import {
  AccessibilityProvider,
  AccessiblePressable,
  useAccessibilityAnnouncement,
  useAccessibilityPreferences,
  useResponsiveCompactLayout,
} from "./accessibility.client.tsx";

export type TaskDetailTab = "details" | "execution" | "activity";

export type ActivitySceneState = {
  isPending?: boolean;
  isError?: boolean;
  isOffline?: boolean;
  isStale?: boolean;
  errorMessage?: string;
  onReload?: () => void;
};

export type TaskDetailViewProps = {
  task: TaskDetail;
  theme: PluginTheme;
  layout: PluginHostProps["layout"];
  navigation?: PluginSurfaceProps["navigation"];
  activeTab?: TaskDetailTab;
  onTabChange?: (tab: TaskDetailTab) => void;
  onAction?: (action: AllowedAction, intent: PlanningMutationIntent) => void;
  configurationDraft?: readonly ConfigurationOverride[];
  onConfigurationDraftChange?: (
    draft: readonly ConfigurationOverride[],
  ) => void;
  preview?: ConfigurationPreview | null;
  pendingApply?: AllowedAction | null;
  onPendingApplyChange?: (action: AllowedAction | null) => void;
  activityState?: ActivitySceneState;
  workspaceName?: string;
  epicName?: string;
  onReturnToBoard?: () => void;
};

const stateLabels: Record<DerivedState, string> = {
  needs_you: "Needs you",
  queued: "Queued",
  building: "Building",
  validating: "Validating",
  in_review: "In review",
  ready: "Ready",
  done: "Done",
};

function formatBasisPoints(bp: string): string {
  const num = Number.parseInt(bp, 10);
  if (Number.isNaN(num)) return `${bp} bp`;
  return `${(num / 100).toFixed(num % 100 === 0 ? 0 : 2)}%`;
}

function formatDimensionValue(dimension: string, value: string): string {
  if (dimension === "wall_time") {
    const ms = Number.parseInt(value, 10);
    if (Number.isNaN(ms)) return value;
    if (ms < 1000) return `${ms}ms`;
    const seconds = Math.floor(ms / 1000);
    if (seconds < 60) return `${seconds}s`;
    const minutes = Math.floor(seconds / 60);
    const remainingSeconds = seconds % 60;
    if (minutes < 60) {
      return remainingSeconds > 0
        ? `${minutes}m ${remainingSeconds}s`
        : `${minutes}m`;
    }
    const hours = Math.floor(minutes / 60);
    const remainingMinutes = minutes % 60;
    return remainingMinutes > 0
      ? `${hours}h ${remainingMinutes}m`
      : `${hours}h`;
  }
  const num = Number.parseInt(value, 10);
  if (!Number.isNaN(num)) {
    return num.toLocaleString("en-US");
  }
  return value;
}

function valueLabel(value: ConfigurationValue): string {
  if (typeof value === "boolean") return value ? "On" : "Off";
  if (value === "manual") return "Manual";
  if (value === "automatic") return "Automatic";
  return value;
}

function attentionLabel(value: string): string {
  const label = value.replaceAll("_", " ");
  return label.charAt(0).toUpperCase() + label.slice(1);
}

function formatIsoTimestamp(isoString: string | null): string {
  if (isoString === null) return "Time unavailable";
  try {
    const date = new Date(isoString);
    if (Number.isNaN(date.getTime())) return isoString;
    const year = date.getUTCFullYear();
    const month = String(date.getUTCMonth() + 1).padStart(2, "0");
    const day = String(date.getUTCDate()).padStart(2, "0");
    const hours = String(date.getUTCHours()).padStart(2, "0");
    const minutes = String(date.getUTCMinutes()).padStart(2, "0");
    const seconds = String(date.getUTCSeconds()).padStart(2, "0");
    return `${year}-${month}-${day} ${hours}:${minutes}:${seconds} UTC`;
  } catch {
    return isoString;
  }
}

export function TaskDetailView({
  task,
  theme,
  layout,
  navigation,
  activeTab: controlledTab,
  onTabChange,
  onAction,
  configurationDraft = [],
  onConfigurationDraftChange,
  preview = null,
  pendingApply = null,
  onPendingApplyChange,
  activityState,
  workspaceName,
  epicName,
  onReturnToBoard,
}: TaskDetailViewProps) {
  const accessibilityPreferences = useAccessibilityPreferences();
  const [internalTab, setInternalTab] = useState<TaskDetailTab>("details");
  const currentTab = controlledTab ?? internalTab;
  const activityAnnouncement = currentTab !== "activity"
    ? null
    : activityState?.isOffline
      ? "Activity is offline. Showing cached activity"
      : activityState?.isError
        ? "Activity refresh failed"
        : activityState?.isPending
          ? "Loading activity stream"
          : activityState?.isStale
            ? "Activity may be stale. Updating"
            : task.activity.length === 0
              ? "No activity recorded yet"
              : `${task.activity.length} Activity events shown`;
  useAccessibilityAnnouncement(
    `${task.summary.key}, ${stateLabels[task.summary.derivedState]}, ${currentTab} tab selected`,
  );
  useAccessibilityAnnouncement(activityAnnouncement);
  const compact = useResponsiveCompactLayout(layout.compact);

  function selectTab(tab: TaskDetailTab) {
    if (onTabChange) {
      onTabChange(tab);
    } else {
      setInternalTab(tab);
    }
  }

  const metrics = shellMetrics(compact);

  const styles = useMemo(
    () =>
      StyleSheet.create({
        container: {
          flex: 1,
          gap: metrics.gap,
        },
        headerCard: {
          gap: 10,
          padding: compact ? 12 : 16,
          borderRadius: 10,
          borderWidth: accessibilityPreferences.highContrast ? 2 : 1,
          borderColor: theme.colors.border,
          backgroundColor: theme.colors.surface1,
        },
        headerTopRow: {
          flexDirection: "row",
          justifyContent: "space-between",
          alignItems: "center",
          flexWrap: "wrap",
          gap: 8,
        },
        badgeRow: {
          flexDirection: "row",
          alignItems: "center",
          flexWrap: "wrap",
          gap: 6,
        },
        keyBadge: {
          paddingHorizontal: 8,
          paddingVertical: 3,
          borderRadius: 6,
          backgroundColor: theme.colors.surface2,
          borderWidth: accessibilityPreferences.highContrast ? 2 : 1,
          borderColor: theme.colors.border,
        },
        keyText: {
          color: theme.colors.foreground,
          fontSize: 12,
          fontWeight: "700",
        },
        stateBadge: {
          paddingHorizontal: 8,
          paddingVertical: 3,
          borderRadius: 6,
          borderWidth: accessibilityPreferences.highContrast ? 2 : 1,
        },
        stateBadgeText: {
          fontSize: 12,
          fontWeight: "600",
        },
        priorityBadge: {
          paddingHorizontal: 8,
          paddingVertical: 3,
          borderRadius: 6,
          backgroundColor: theme.colors.surface2,
        },
        priorityText: {
          color: theme.colors.foregroundMuted,
          fontSize: 12,
          textTransform: "capitalize",
        },
        title: {
          color: theme.colors.foreground,
          fontSize: compact ? 17 : 20,
          fontWeight: "700",
          lineHeight: compact ? 22 : 26,
        },
        metaRow: {
          flexDirection: "row",
          alignItems: "center",
          flexWrap: "wrap",
          gap: 8,
        },
        metaText: {
          color: theme.colors.foregroundMuted,
          fontSize: 12,
        },
        actionRow: {
          flexDirection: compact ? "column" : "row",
          alignItems: compact ? "stretch" : "center",
          gap: 8,
          paddingTop: 4,
        },
        tabBar: {
          flexDirection: "row",
          flexWrap: "wrap",
          gap: 8,
          paddingHorizontal: 2,
        },
        tab: {
          minHeight: 44,
          flexDirection: "row",
          alignItems: "center",
          justifyContent: "center",
          gap: 6,
          paddingHorizontal: 16,
          paddingVertical: 10,
          borderRadius: 8,
          backgroundColor: theme.colors.surface2,
          borderWidth: accessibilityPreferences.highContrast ? 2 : 1,
          borderColor: theme.colors.surface2,
          flexGrow: 1,
          flexShrink: 1,
          flexBasis: compact ? "28%" : "auto",
        },
        tabSelected: {
          backgroundColor: theme.colors.accent,
          borderColor: theme.colors.accent,
        },
        tabText: {
          color: theme.colors.foreground,
          fontSize: 13,
          fontWeight: "600",
        },
        tabTextSelected: {
          color: theme.colors.accentForeground,
          fontWeight: "700",
        },
        tabBadge: {
          paddingHorizontal: 6,
          paddingVertical: 1,
          borderRadius: 10,
          backgroundColor: theme.colors.surface1,
        },
        tabBadgeSelected: {
          backgroundColor: theme.colors.accentForeground,
        },
        tabBadgeText: {
          color: theme.colors.foregroundMuted,
          fontSize: 11,
          fontWeight: "700",
        },
        tabBadgeTextSelected: {
          color: theme.colors.accent,
        },
        scrollContent: {
          gap: 12,
          paddingBottom: 24,
        },
        section: {
          gap: 8,
          padding: compact ? 12 : 14,
          borderRadius: 10,
          borderWidth: accessibilityPreferences.highContrast ? 2 : 1,
          borderColor: theme.colors.border,
          backgroundColor: theme.colors.surface1,
        },
        sectionHeaderRow: {
          flexDirection: "row",
          justifyContent: "space-between",
          alignItems: "center",
          flexWrap: "wrap",
          gap: 8,
        },
        sectionLabel: {
          color: theme.colors.foregroundMuted,
          fontSize: 12,
          fontWeight: "700",
          textTransform: "uppercase",
        },
        bodyText: {
          color: theme.colors.foreground,
          fontSize: 13,
          lineHeight: 20,
        },
        mutedText: {
          color: theme.colors.foregroundMuted,
          fontSize: 12,
          lineHeight: 18,
        },
        successText: {
          color: theme.colors.statusSuccess,
          fontSize: 12,
          fontWeight: "600",
        },
        warningText: {
          color: theme.colors.statusWarning,
          fontSize: 12,
          fontWeight: "600",
        },
        dangerText: {
          color: theme.colors.statusDanger,
          fontSize: 12,
          fontWeight: "600",
        },
        grid2x2: {
          flexDirection: "row",
          flexWrap: "wrap",
          gap: 8,
        },
        gridCard: {
          flex: 1,
          minWidth: compact ? "100%" : 200,
          padding: 10,
          borderRadius: 8,
          borderWidth: accessibilityPreferences.highContrast ? 2 : 1,
          borderColor: theme.colors.border,
          backgroundColor: theme.colors.surface2,
          gap: 4,
        },
        gridCardTitle: {
          color: theme.colors.foregroundMuted,
          fontSize: 11,
          fontWeight: "700",
          textTransform: "uppercase",
        },
        gridCardValue: {
          color: theme.colors.foreground,
          fontSize: 14,
          fontWeight: "700",
          flexShrink: 1,
        },
        gridCardMeta: {
          color: theme.colors.foregroundMuted,
          fontSize: 11,
        },
        criterionRow: {
          flexDirection: "row",
          alignItems: "flex-start",
          gap: 8,
          paddingVertical: 4,
        },
        criterionMark: {
          fontSize: 14,
          fontWeight: "700",
          width: 18,
          textAlign: "center",
        },
        dependencyItem: {
          gap: 6,
          padding: 10,
          borderRadius: 8,
          borderWidth: accessibilityPreferences.highContrast ? 2 : 1,
          borderColor: theme.colors.border,
          backgroundColor: theme.colors.surface2,
        },
        dependencyHeader: {
          flexDirection: "row",
          justifyContent: "space-between",
          alignItems: "center",
          flexWrap: "wrap",
          gap: 8,
        },
        configItem: {
          gap: 6,
          paddingVertical: 6,
          borderBottomWidth: accessibilityPreferences.highContrast ? 2 : 1,
          borderBottomColor: theme.colors.border,
        },
        wrapRow: {
          flexDirection: "row",
          flexWrap: "wrap",
          alignItems: "center",
          gap: 6,
        },
        chip: {
          minHeight: 44,
          paddingHorizontal: 12,
          paddingVertical: 6,
          borderRadius: 6,
          borderWidth: accessibilityPreferences.highContrast ? 2 : 1,
          borderColor: theme.colors.border,
          backgroundColor: theme.colors.surface2,
          justifyContent: "center",
          alignItems: "center",
        },
        chipSelected: {
          borderColor: theme.colors.accent,
          backgroundColor: theme.colors.accent,
        },
        chipText: {
          color: theme.colors.foreground,
          fontSize: 12,
          fontWeight: "600",
        },
        chipTextSelected: {
          color: theme.colors.accentForeground,
          fontWeight: "700",
        },
        primaryButton: {
          minHeight: 44,
          alignItems: "center",
          justifyContent: "center",
          paddingHorizontal: 16,
          paddingVertical: 10,
          borderRadius: 8,
          backgroundColor: theme.colors.accent,
        },
        primaryButtonDisabled: {
          backgroundColor: theme.colors.surface2,
          borderWidth: accessibilityPreferences.highContrast ? 2 : 1,
          borderColor: theme.colors.border,
        },
        primaryButtonText: {
          color: theme.colors.accentForeground,
          fontSize: 13,
          fontWeight: "700",
        },
        primaryButtonTextDisabled: {
          color: theme.colors.foregroundMuted,
        },
        secondaryButton: {
          minHeight: 44,
          alignItems: "center",
          justifyContent: "center",
          paddingHorizontal: 14,
          paddingVertical: 10,
          borderRadius: 8,
          borderWidth: accessibilityPreferences.highContrast ? 2 : 1,
          borderColor: theme.colors.border,
          backgroundColor: theme.colors.surface2,
        },
        secondaryButtonDisabled: {
          opacity: 0.5,
        },
        secondaryButtonText: {
          color: theme.colors.foreground,
          fontSize: 13,
          fontWeight: "600",
        },
        activityItem: {
          gap: 4,
          padding: 10,
          borderRadius: 8,
          borderWidth: accessibilityPreferences.highContrast ? 2 : 1,
          borderColor: theme.colors.border,
          backgroundColor: theme.colors.surface2,
        },
        activityHeader: {
          flexDirection: "row",
          justifyContent: "space-between",
          alignItems: "center",
          flexWrap: "wrap",
          gap: 6,
        },
        activityKindBadge: {
          paddingHorizontal: 6,
          paddingVertical: 2,
          borderRadius: 4,
          backgroundColor: theme.colors.surface1,
        },
        activityKindText: {
          color: theme.colors.foregroundMuted,
          fontSize: 11,
          fontWeight: "600",
          textTransform: "uppercase",
        },
        activityTimestamp: {
          color: theme.colors.foregroundMuted,
          fontSize: 11,
        },
        liveStateContainer: {
          minHeight: 120,
          alignItems: "center",
          justifyContent: "center",
          gap: 8,
          padding: 16,
          borderRadius: 8,
          borderWidth: accessibilityPreferences.highContrast ? 2 : 1,
          borderColor: theme.colors.border,
          backgroundColor: theme.colors.surface2,
        },
        liveStateTitle: {
          color: theme.colors.foreground,
          fontSize: 14,
          fontWeight: "600",
          textAlign: "center",
        },
        liveStateBody: {
          color: theme.colors.foregroundMuted,
          fontSize: 12,
          lineHeight: 18,
          textAlign: "center",
        },
        staleBanner: {
          flexDirection: "row",
          alignItems: "center",
          flexWrap: "wrap",
          gap: 8,
          padding: 8,
          borderRadius: 6,
          backgroundColor: theme.colors.surface2,
          borderWidth: accessibilityPreferences.highContrast ? 2 : 1,
          borderColor: theme.colors.statusWarning,
        },
      }),
    [accessibilityPreferences.highContrast, compact, metrics, theme],
  );

  function stateAccent(state: DerivedState): string {
    switch (state) {
      case "needs_you":
      case "validating":
        return theme.colors.statusWarning;
      case "building":
      case "in_review":
        return theme.colors.accent;
      case "ready":
      case "done":
        return theme.colors.statusSuccess;
      case "queued":
        return theme.colors.foregroundMuted;
    }
  }

  function renderHeader() {
    const stateColor = stateAccent(task.summary.derivedState);
    const resolvedWorkspace = workspaceName ?? task.summary.workspaceId;
    const resolvedEpic = epicName ?? task.summary.epicId;
    const agentCanOpen = task.binding.agentNavigation === "available" &&
      task.binding.paseoAgentId !== null && navigation?.openAgent !== undefined;

    return (
      <View style={styles.headerCard}>
        <View style={styles.headerTopRow}>
          <View style={styles.badgeRow}>
            <View style={styles.keyBadge}>
              <Text style={styles.keyText}>{task.summary.key}</Text>
            </View>
            <View
              style={[
                styles.stateBadge,
                {
                  borderColor: stateColor,
                  backgroundColor: theme.colors.surface2,
                },
              ]}
            >
              <Text style={[styles.stateBadgeText, { color: stateColor }]}>
                {stateLabels[task.summary.derivedState]}
              </Text>
            </View>
            <View style={styles.priorityBadge}>
              <Text style={styles.priorityText}>{task.summary.priority}</Text>
            </View>
          </View>
          {onReturnToBoard ? (
            <AccessiblePressable
              accessibilityHint="Navigates back to the Director Board"
              accessibilityLabel="Return to Director Board"
              accessibilityRole="button"
              focusable
              onPress={onReturnToBoard}
              style={styles.secondaryButton}
            >
              <Text style={styles.secondaryButtonText}>Return to Board</Text>
            </AccessiblePressable>
          ) : null}
        </View>

        <Text accessibilityRole="header" style={styles.title}>
          {task.summary.title}
        </Text>

        <View style={styles.metaRow}>
          <Text style={styles.metaText}>
            Workspace: <Text style={{ color: theme.colors.foreground }}>{resolvedWorkspace}</Text>
          </Text>
          {resolvedEpic ? (
            <Text style={styles.metaText}>
              Epic: <Text style={{ color: theme.colors.foreground }}>{resolvedEpic}</Text>
            </Text>
          ) : (
            <Text style={styles.metaText}>Standalone task</Text>
          )}
        </View>

        <View style={styles.actionRow}>
          <AccessiblePressable
            accessibilityHint={agentCanOpen
              ? "Opens the exact Paseo agent bound by Director Engine"
              : task.binding.unavailableReason?.message ?? "Native Paseo navigation is unavailable"}
            accessibilityLabel={agentCanOpen
              ? `Open agent for ${task.summary.key}`
              : "Open agent unavailable"}
            accessibilityRole="button"
            accessibilityState={{ disabled: !agentCanOpen }}
            disabled={!agentCanOpen}
            focusable={agentCanOpen}
            onPress={() => {
              if (task.binding.paseoAgentId && agentCanOpen) {
                navigation?.openAgent({ agentId: task.binding.paseoAgentId });
              }
            }}
            style={[
              styles.primaryButton,
              !agentCanOpen && styles.primaryButtonDisabled,
            ]}
          >
            <Text style={[
              styles.primaryButtonText,
              !agentCanOpen && styles.primaryButtonTextDisabled,
            ]}>
              {agentCanOpen ? "Open agent" : "Open agent unavailable"}
            </Text>
          </AccessiblePressable>
          {!agentCanOpen ? (
            <Text style={styles.mutedText}>
              {task.binding.unavailableReason?.message ?? "Native Paseo navigation is unavailable on this host."}
            </Text>
          ) : null}
        </View>
      </View>
    );
  }

  function renderTabBar() {
    const tabs: readonly { id: TaskDetailTab; label: string; count?: number }[] = [
      { id: "details", label: "Details" },
      { id: "execution", label: "Execution" },
      {
        id: "activity",
        label: "Activity",
        count: task.activity.length,
      },
    ];

    return (
      <View
        accessibilityLabel="Task detail sections"
        accessibilityRole="tablist"
        style={styles.tabBar}
      >
        {tabs.map((tab) => {
          const selected = currentTab === tab.id;
          return (
            <AccessiblePressable
              accessibilityHint={`Shows ${tab.label} for this exact Task`}
              accessibilityLabel={`Show ${tab.label} tab`}
              accessibilityRole="tab"
              accessibilityState={{ selected }}
              focusable
              key={tab.id}
              onPress={() => selectTab(tab.id)}
              style={[styles.tab, selected && styles.tabSelected]}
            >
              <Text
                style={[
                  styles.tabText,
                  selected && styles.tabTextSelected,
                ]}
              >
                {tab.label}
              </Text>
              {tab.count !== undefined ? (
                <View
                  style={[
                    styles.tabBadge,
                    selected && styles.tabBadgeSelected,
                  ]}
                >
                  <Text
                    style={[
                      styles.tabBadgeText,
                      selected && styles.tabBadgeTextSelected,
                    ]}
                  >
                    {tab.count}
                  </Text>
                </View>
              ) : null}
            </AccessiblePressable>
          );
        })}
      </View>
    );
  }

  function renderDetailsTab() {
    const launchAction = task.summary.allowedActions.find(
      (action) => action.kind === "task.launch-now",
    );
    const previewAction = task.summary.allowedActions.find(
      (action) => action.kind === "configuration.preview",
    );

    return (
      <View style={styles.scrollContent}>
        <View style={styles.section}>
          <Text style={styles.sectionLabel}>Objective</Text>
          <Text style={styles.bodyText}>{task.objective}</Text>
          {launchAction && onAction ? (
            <AccessiblePressable
              accessibilityHint="Submits the engine-issued launch intent without changing Task state locally"
              accessibilityLabel={launchAction.label}
              accessibilityRole="button"
              focusable
              onPress={() =>
                onAction(launchAction, {
                  type: "task.launch-now",
                  taskId: task.summary.id,
                })
              }
              style={styles.primaryButton}
            >
              <Text style={styles.primaryButtonText}>
                {launchAction.label}
              </Text>
            </AccessiblePressable>
          ) : null}
        </View>

        <View style={styles.section}>
          <View style={styles.sectionHeaderRow}>
            <Text style={styles.sectionLabel}>Acceptance criteria</Text>
            <Text style={styles.mutedText}>
              {task.acceptanceCriteria.filter((c) => c.status === "satisfied").length}/
              {task.acceptanceCriteria.length} satisfied
            </Text>
          </View>
          {task.acceptanceCriteria.length === 0 ? (
            <Text style={styles.mutedText}>No acceptance criteria recorded.</Text>
          ) : (
            task.acceptanceCriteria.map((criterion) => {
              const isSatisfied = criterion.status === "satisfied";
              const isUnsatisfied = criterion.status === "unsatisfied";
              return (
                <View
                  accessible
                  accessibilityLabel={`${criterion.status}: ${criterion.text}`}
                  key={criterion.id}
                  style={styles.criterionRow}
                >
                  <Text
                    style={[
                      styles.criterionMark,
                      isSatisfied
                        ? styles.successText
                        : isUnsatisfied
                          ? styles.dangerText
                          : styles.mutedText,
                    ]}
                  >
                    {isSatisfied ? "✓" : isUnsatisfied ? "✗" : "○"}
                  </Text>
                  <Text style={[styles.bodyText, { flex: 1 }]}>
                    {criterion.text}
                  </Text>
                </View>
              );
            })
          )}
        </View>

        <View style={styles.section}>
          <View style={styles.sectionHeaderRow}>
            <Text style={styles.sectionLabel}>Dependencies</Text>
            <Text style={styles.mutedText}>
              {task.dependencies.length} prerequisite{task.dependencies.length === 1 ? "" : "s"}
            </Text>
          </View>
          {task.dependencies.length === 0 ? (
            <Text style={styles.mutedText}>No dependencies.</Text>
          ) : (
            task.dependencies.map((dep) => {
              const overrideAction = task.summary.allowedActions.find(
                (action) =>
                  action.kind === "dependency.override" &&
                  action.targetId === dep.id,
              );
              return (
                <View key={dep.id} style={styles.dependencyItem}>
                  <View style={styles.dependencyHeader}>
                    <Text style={styles.bodyText}>
                      <Text style={{ fontWeight: "700" }}>{dep.key}</Text> · {dep.title}
                    </Text>
                    <Text
                      style={dep.satisfied ? styles.successText : styles.warningText}
                    >
                      {dep.satisfied ? "Satisfied" : "Unsatisfied"}
                    </Text>
                  </View>
                  <Text style={styles.mutedText}>{dep.explanation.message}</Text>
                  {overrideAction && onAction ? (
                    <AccessiblePressable
                      accessibilityHint="Submits the engine-issued dependency override intent for confirmation"
                      accessibilityLabel={overrideAction.label}
                      accessibilityRole="button"
                      focusable
                      onPress={() =>
                        onAction(overrideAction, {
                          type: "dependency.override",
                          taskId: task.summary.id,
                          dependencyKind: dep.kind,
                          dependencyId: dep.id,
                        })
                      }
                      style={styles.secondaryButton}
                    >
                      <Text style={styles.secondaryButtonText}>
                        {overrideAction.label}
                      </Text>
                    </AccessiblePressable>
                  ) : null}
                </View>
              );
            })
          )}
        </View>

        <View style={styles.section}>
          <Text style={styles.sectionLabel}>Configuration inheritance</Text>
          {task.configuration.length === 0 ? (
            <Text style={styles.mutedText}>
              No Task-level configuration overrides are available.
            </Text>
          ) : null}
          {task.configuration.map((entry) => {
            const configured =
              configurationDraft.find((c) => c.key === entry.key) ??
              entry.configured;

            return (
              <View key={entry.key} style={styles.configItem}>
                <Text style={styles.bodyText}>{entry.key}</Text>
                <Text style={styles.mutedText}>
                  Effective: {valueLabel(entry.effectiveValue)} (from {entry.effectiveSource})
                </Text>
                <View style={styles.wrapRow}>
                  <AccessiblePressable
                    accessibilityHint={`Uses the inherited value for ${entry.key}`}
                    accessibilityLabel={`Inherit ${entry.key}`}
                    accessibilityRole="button"
                    accessibilityState={{ selected: configured.mode === "inherit" }}
                    focusable
                    onPress={() => {
                      if (onConfigurationDraftChange) {
                        const updated = [
                          ...configurationDraft.filter((c) => c.key !== entry.key),
                          { key: entry.key, mode: "inherit" as const },
                        ];
                        onConfigurationDraftChange(updated);
                      }
                    }}
                    style={[
                      styles.chip,
                      configured.mode === "inherit" && styles.chipSelected,
                    ]}
                  >
                    <Text
                      style={[
                        styles.chipText,
                        configured.mode === "inherit" && styles.chipTextSelected,
                      ]}
                    >
                      Inherit
                    </Text>
                  </AccessiblePressable>
                  {entry.allowedValues.map((value) => {
                    const isSelected =
                      configured.mode === "value" && configured.value === value;
                    return (
                      <AccessiblePressable
                        accessibilityHint={`Uses ${valueLabel(value)} for ${entry.key}`}
                        accessibilityLabel={`Set ${entry.key} to ${valueLabel(value)}`}
                        accessibilityRole="button"
                        accessibilityState={{ selected: isSelected }}
                        focusable
                        key={`${entry.key}-${String(value)}`}
                        onPress={() => {
                          if (onConfigurationDraftChange) {
                            const updated = [
                              ...configurationDraft.filter(
                                (c) => c.key !== entry.key,
                              ),
                              {
                                key: entry.key,
                                mode: "value" as const,
                                value,
                              },
                            ];
                            onConfigurationDraftChange(updated);
                          }
                        }}
                        style={[styles.chip, isSelected && styles.chipSelected]}
                      >
                        <Text
                          style={[
                            styles.chipText,
                            isSelected && styles.chipTextSelected,
                          ]}
                        >
                          {valueLabel(value)}
                        </Text>
                      </AccessiblePressable>
                    );
                  })}
                </View>
              </View>
            );
          })}

          {previewAction && onAction ? (
            <AccessiblePressable
              accessibilityHint="Requests an engine-computed Preview without applying changes"
              accessibilityLabel="Preview configuration changes"
              accessibilityRole="button"
              focusable
              onPress={() =>
                onAction(previewAction, {
                  type: "configuration.preview",
                  target: task.configurationTarget,
                  overrides: configurationDraft,
                })
              }
              style={styles.secondaryButton}
            >
              <Text style={styles.secondaryButtonText}>Preview changes</Text>
            </AccessiblePressable>
          ) : null}

          {preview ? (
            <View accessibilityLiveRegion="polite" style={{ gap: 6, paddingTop: 6 }}>
              <Text style={styles.sectionLabel}>Preview diff</Text>
              {preview.diff.length === 0 ? (
                <Text style={styles.mutedText}>No effective changes</Text>
              ) : (
                preview.diff.map((change) => (
                  <Text key={change.key} style={styles.bodyText}>
                    {change.key}: {valueLabel(change.before)} → {valueLabel(change.after)} ({change.effectiveSource})
                  </Text>
                ))
              )}
              {preview.issues.map((issue) => (
                <Text key={issue.code} style={styles.dangerText}>
                  {issue.message}
                </Text>
              ))}
              {preview.applyAction && onPendingApplyChange ? (
                <AccessiblePressable
                  accessibilityHint="Opens explicit confirmation for this exact Preview"
                  accessibilityLabel={preview.applyAction.label}
                  accessibilityRole="button"
                  focusable
                  onPress={() => onPendingApplyChange(preview.applyAction)}
                  style={styles.primaryButton}
                >
                  <Text style={styles.primaryButtonText}>
                    {preview.applyAction.label}
                  </Text>
                </AccessiblePressable>
              ) : null}

              {pendingApply &&
              preview.applyAction?.requestId === pendingApply.requestId &&
              onAction &&
              onPendingApplyChange ? (
                <View
                  accessible
                  accessibilityLiveRegion="polite"
                  style={styles.section}
                >
                  <Text style={styles.warningText}>
                    Confirm the exact preview before Director Engine applies it to future Runs.
                  </Text>
                  <View style={styles.wrapRow}>
                    <AccessiblePressable
                      accessibilityHint="Closes confirmation without applying the Preview"
                      accessibilityLabel="Cancel configuration apply"
                      accessibilityRole="button"
                      focusable
                      onPress={() => onPendingApplyChange(null)}
                      style={styles.secondaryButton}
                    >
                      <Text style={styles.secondaryButtonText}>Cancel</Text>
                    </AccessiblePressable>
                    <AccessiblePressable
                      accessibilityHint="Submits the unchanged engine-issued Apply ticket"
                      accessibilityLabel={`Confirm ${pendingApply.label}`}
                      accessibilityRole="button"
                      focusable
                      onPress={() =>
                        onAction(pendingApply, {
                          type: "configuration.apply",
                          target: preview.target,
                          previewId: preview.previewId,
                        })
                      }
                      style={styles.primaryButton}
                    >
                      <Text style={styles.primaryButtonText}>Confirm Apply</Text>
                    </AccessiblePressable>
                  </View>
                </View>
              ) : null}
            </View>
          ) : null}
        </View>
      </View>
    );
  }

  function renderExecutionTab() {
    const queueFacts = task.summary.schedulingFacts;
    const budget = task.summary.runtimeBudget;
    const feedback = task.summary.feedback;
    const binding = task.binding;

    return (
      <View style={styles.scrollContent}>
        <View style={styles.section}>
          <Text style={styles.sectionLabel}>Exact execution binding</Text>
          <View style={styles.grid2x2}>
            <View style={styles.gridCard}>
              <Text style={styles.gridCardTitle}>Task / Workspace</Text>
              <Text style={styles.gridCardValue}>
                {binding.taskId}
              </Text>
              <Text style={styles.gridCardMeta}>
                {binding.workspaceId} · v{binding.taskVersion}
              </Text>
            </View>
            <View style={styles.gridCard}>
              <Text style={styles.gridCardTitle}>Run</Text>
              <Text style={styles.gridCardValue}>
                {binding.runId ?? "Not started"}
              </Text>
              <Text style={styles.gridCardMeta}>
                {binding.runNumber ? `Run ${binding.runNumber} · v${binding.runVersion}` : "No Run binding"}
              </Text>
            </View>
            <View style={styles.gridCard}>
              <Text style={styles.gridCardTitle}>Candidate</Text>
              <Text style={styles.gridCardValue}>{binding.candidateId ?? "No Candidate"}</Text>
              <Text style={styles.gridCardMeta}>{binding.candidateSha ?? "No exact commit"}</Text>
            </View>
            <View style={styles.gridCard}>
              <Text style={styles.gridCardTitle}>Paseo host / agent</Text>
              <Text style={styles.gridCardValue}>{binding.hostId}</Text>
              <Text style={styles.gridCardMeta}>
                {binding.paseoWorkspaceId ?? "No Execution Workspace"}{" · "}
                {binding.paseoAgentId ?? "No agent"}
              </Text>
            </View>
          </View>
          <Text style={styles.bodyText}>
            {valueLabel(queueFacts.launchMode)} launch · {attentionLabel(queueFacts.launchDisposition)}
            {queueFacts.queuePosition !== null ? ` · queue #${queueFacts.queuePosition}` : ""}
          </Text>
          {queueFacts.explanations.map((exp) => (
            <Text key={exp.code} style={styles.warningText}>{exp.message}</Text>
          ))}
        </View>

        <View style={styles.section}>
          <Text style={styles.sectionLabel}>Runtime budget</Text>
          {budget ? (
            <View style={{ gap: 10 }}>
              <View style={styles.dependencyHeader}>
                <Text
                  style={
                    budget.state === "current"
                      ? styles.successText
                      : budget.state === "soft_paused"
                        ? styles.warningText
                        : styles.dangerText
                  }
                >
                  Status: {attentionLabel(budget.state)} · Soft limit {formatBasisPoints(budget.softThresholdBasisPoints)}
                </Text>
                {budget.reasonCode ? (
                  <Text style={styles.mutedText}>{budget.reasonCode}</Text>
                ) : null}
              </View>

              <Text style={styles.sectionLabel}>Resource Dimensions</Text>
              <View style={styles.grid2x2}>
                {budget.dimensions.map((dim) => (
                  <View key={dim.dimension} style={styles.gridCard}>
                    <Text style={styles.gridCardTitle}>
                      {attentionLabel(dim.dimension)}
                    </Text>
                    <Text style={styles.gridCardValue}>
                      {formatDimensionValue(dim.dimension, dim.consumed)} /{" "}
                      {dim.enabled
                        ? formatDimensionValue(dim.dimension, dim.limit)
                        : "Disabled"}
                    </Text>
                    <Text style={styles.gridCardMeta}>
                      Reserved: {formatDimensionValue(dim.dimension, dim.reserved)} ·{" "}
                      {formatBasisPoints(dim.ratioBasisPoints)}
                    </Text>
                  </View>
                ))}
              </View>

              <Text style={styles.sectionLabel}>Execution Counts</Text>
              <View style={styles.grid2x2}>
                {budget.counts.map((cnt) => (
                  <View key={cnt.dimension} style={styles.gridCard}>
                    <Text style={styles.gridCardTitle}>
                      {attentionLabel(cnt.dimension)}
                    </Text>
                    <Text style={styles.gridCardValue}>
                      {cnt.consumed} / {cnt.limit}
                    </Text>
                    <Text style={styles.gridCardMeta}>
                      Reserved: {cnt.reserved}
                    </Text>
                  </View>
                ))}
              </View>

              <Text style={styles.sectionLabel}>Turn Distribution</Text>
              <Text style={styles.bodyText}>
                Worker {budget.workerTurns} · Helper {budget.helperTurns} · Reviewer {budget.reviewerTurns} · Correction {budget.correctionTurns}
              </Text>
            </View>
          ) : (
            <Text style={styles.mutedText}>
              No runtime budget recorded for this task.
            </Text>
          )}
        </View>

        <View style={styles.section}>
          <Text style={styles.sectionLabel}>Review & Feedback</Text>
          {feedback ? (
            <View style={{ gap: 6 }}>
              <Text style={styles.bodyText}>
                Phase: <Text style={{ fontWeight: "700" }}>{attentionLabel(feedback.phase)}</Text>
              </Text>
              <Text style={styles.bodyText}>
                Actionable findings: {feedback.currentActionable} · Audits: {feedback.auditRecords}
              </Text>
              <Text style={styles.mutedText}>
                Revision: {feedback.currentRevision.slice(0, 7)}
                {feedback.correctionBatch
                  ? ` · Correction batch: ${feedback.correctionBatch.slice(0, 7)}`
                  : ""}
              </Text>
            </View>
          ) : (
            <Text style={styles.mutedText}>No review feedback recorded.</Text>
          )}
        </View>
      </View>
    );
  }

  function renderActivityTab() {
    const isPending = activityState?.isPending ?? false;
    const isError = activityState?.isError ?? false;
    const isOffline = activityState?.isOffline ?? false;
    const isStale = activityState?.isStale ?? false;
    const onReload = activityState?.onReload;

    if (isPending && task.activity.length === 0) {
      return (
        <View accessibilityLiveRegion="polite" style={styles.liveStateContainer}>
          {accessibilityPreferences.reduceMotion ? (
            <Icon color={theme.colors.accent} name="Clock3" size={24} />
          ) : (
            <ActivityIndicator color={theme.colors.accent} />
          )}
          <Text style={styles.liveStateTitle}>Loading activity stream</Text>
        </View>
      );
    }

    if (isError && task.activity.length === 0) {
      return (
        <View accessibilityLiveRegion="assertive" style={styles.liveStateContainer}>
          <Icon color={theme.colors.statusDanger} name="CircleAlert" size={24} />
          <Text style={styles.liveStateTitle}>Activity stream is unavailable</Text>
          <Text style={styles.liveStateBody}>
            {activityState?.errorMessage ??
              "Director Engine could not provide the task activity log."}
          </Text>
          {onReload ? (
            <AccessiblePressable
              accessibilityHint="Retries the Activity query for this exact Task and host"
              accessibilityLabel="Try loading activity stream again"
              accessibilityRole="button"
              focusable
              onPress={onReload}
              style={styles.primaryButton}
            >
              <Text style={styles.primaryButtonText}>Try again</Text>
            </AccessiblePressable>
          ) : null}
        </View>
      );
    }

    return (
      <View style={styles.scrollContent}>
        {isOffline ? (
          <View accessibilityLiveRegion="polite" style={styles.staleBanner}>
            <Icon color={theme.colors.statusWarning} name="CloudOff" size={16} />
            <Text style={styles.warningText}>
              Offline · showing cached activity
            </Text>
          </View>
        ) : null}

        {isStale && !isOffline && !isError ? (
          <View accessibilityLiveRegion="polite" style={styles.staleBanner}>
            <Icon color={theme.colors.statusWarning} name="RefreshCw" size={16} />
            <Text style={styles.warningText}>
              Activity may be stale · updating
            </Text>
          </View>
        ) : null}

        {isError && task.activity.length > 0 ? (
          <View accessibilityLiveRegion="polite" style={styles.staleBanner}>
            <Icon color={theme.colors.statusWarning} name="CircleAlert" size={16} />
            <Text style={styles.warningText}>
              Activity refresh failed · showing the last exact-host snapshot
            </Text>
          </View>
        ) : null}

        {task.activity.length === 0 ? (
          <View style={styles.liveStateContainer}>
            <Icon color={theme.colors.foregroundMuted} name="Inbox" size={24} />
            <Text style={styles.liveStateTitle}>No activity recorded yet</Text>
            <Text style={styles.liveStateBody}>
              Events will appear here as Director plans and executes this task.
            </Text>
          </View>
        ) : (
          task.activity.map((event) => (
            <View
              accessible
              accessibilityLabel={`${event.kind}, event ${event.sequence}, ${formatIsoTimestamp(event.occurredAt)}, ${event.message}`}
              key={event.id}
              style={styles.activityItem}
            >
              <View style={styles.activityHeader}>
                <View style={styles.activityKindBadge}>
                  <Text style={styles.activityKindText}>{event.kind}</Text>
                </View>
                <Text style={styles.activityTimestamp}>
                  #{event.sequence} · {formatIsoTimestamp(event.occurredAt)}
                </Text>
              </View>
              <Text style={styles.bodyText}>{event.message}</Text>
            </View>
          ))
        )}
      </View>
    );
  }

  return (
    <AccessibilityProvider
      focusColor={theme.colors.accent}
      preferences={accessibilityPreferences}
    >
    <View style={styles.container}>
      {renderHeader()}
      {renderTabBar()}
      <ScrollView
        contentContainerStyle={{ flexGrow: 1 }}
        showsVerticalScrollIndicator
      >
        {currentTab === "details" ? renderDetailsTab() : null}
        {currentTab === "execution" ? renderExecutionTab() : null}
        {currentTab === "activity" ? renderActivityTab() : null}
      </ScrollView>
    </View>
    </AccessibilityProvider>
  );
}
