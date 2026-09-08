// SPDX-License-Identifier: Apache-2.0
// Client-only React Native surfaces render engine-owned projections.

import type {
  PluginAgentPanelProps,
  PluginSurfaceProps,
  PluginWorkspacePanelProps,
} from "@getpaseo/plugin";
import { useRpc } from "@getpaseo/plugin";
import { useQuery } from "@tanstack/react-query";
import React, { useMemo, useState } from "react";
import {
  ActivityIndicator,
  Pressable,
  ScrollView,
  StyleSheet,
  Text,
  View,
} from "react-native";

import type {
  BoardSnapshot,
  BoardState,
  BoardTask,
} from "../rpc/board.shared.ts";
import { boardSnapshotRpc } from "../rpc/board.shared.ts";
import {
  boardScene,
  boardStateLabels,
  boardStatesForLayout,
  tasksInState,
} from "./board-view.client.ts";
import { shellMetrics } from "./shell-layout.client.ts";

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
  const [selectedLane, setSelectedLane] = useState<BoardState>("queued");
  const loadBoard = useRpc(boardSnapshotRpc);
  const board = useQuery({
    queryKey: ["director", "board-snapshot"],
    queryFn: () => loadBoard({}),
    refetchInterval: 2_000,
    retry: false,
  });
  const scene = boardScene({
    data: board.data,
    isPending: board.isPending,
    isError: board.isError,
  });
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
        liveState: {
          minHeight: 100,
          alignItems: "center",
          justifyContent: "center",
          gap: 10,
          padding: 20,
          borderRadius: 10,
          borderWidth: 1,
          borderColor: theme.colors.border,
          backgroundColor: theme.colors.surface1,
        },
        stateTitle: {
          color: theme.colors.foreground,
          fontSize: 16,
          fontWeight: "600",
          textAlign: "center",
        },
        stateBody: {
          color: theme.colors.foregroundMuted,
          lineHeight: 20,
          textAlign: "center",
        },
        retry: {
          paddingHorizontal: 14,
          paddingVertical: 10,
          borderRadius: 8,
          backgroundColor: theme.colors.accent,
        },
        retryText: {
          color: theme.colors.accentForeground,
          fontWeight: "600",
        },
        updating: {
          flexDirection: "row",
          alignItems: "center",
          gap: 8,
        },
        updatingText: {
          color: theme.colors.foregroundMuted,
          fontSize: 12,
        },
        laneSelector: {
          flexDirection: "row",
          gap: 8,
          paddingBottom: 4,
        },
        board: {
          flexDirection: metrics.boardDirection,
          gap: metrics.gap,
          paddingBottom: 4,
        },
        lane: {
          minWidth: layout.compact ? undefined : 220,
          width: layout.compact ? "100%" : 220,
          padding: 12,
          gap: 8,
          borderRadius: 10,
          borderWidth: 1,
          borderColor: theme.colors.border,
          backgroundColor: theme.colors.surface1,
        },
        laneHeader: {
          flexDirection: "row",
          justifyContent: "space-between",
          alignItems: "center",
          gap: 8,
        },
        laneTitle: { color: theme.colors.foreground, fontWeight: "600" },
        laneCount: { color: theme.colors.foregroundMuted, fontSize: 12 },
        empty: { color: theme.colors.foregroundMuted },
        list: {
          padding: layout.compact ? 10 : 0,
          gap: layout.compact ? 8 : 0,
          borderRadius: 10,
          borderWidth: 1,
          borderColor: theme.colors.border,
          backgroundColor: theme.colors.surface1,
        },
        listHeader: {
          flexDirection: "row",
          gap: 12,
          paddingHorizontal: 12,
          paddingVertical: 10,
          borderBottomWidth: 1,
          borderBottomColor: theme.colors.border,
          backgroundColor: theme.colors.surface2,
        },
        listRow: {
          flexDirection: layout.compact ? "column" : "row",
          gap: layout.compact ? 5 : 12,
          padding: 12,
          borderBottomWidth: layout.compact ? 0 : 1,
          borderBottomColor: theme.colors.border,
          borderRadius: layout.compact ? 8 : 0,
          backgroundColor: layout.compact
            ? theme.colors.surface2
            : theme.colors.surface1,
        },
        taskCard: {
          gap: 5,
          padding: 11,
          borderRadius: 8,
          borderWidth: 1,
          borderColor: theme.colors.border,
          backgroundColor: theme.colors.surface2,
        },
        taskTitle: { color: theme.colors.foreground, fontWeight: "600" },
        taskMeta: { color: theme.colors.foregroundMuted, fontSize: 12 },
        taskState: { color: theme.colors.foreground, fontSize: 12 },
        titleColumn: { flex: 2, color: theme.colors.foreground },
        projectColumn: { flex: 1, color: theme.colors.foregroundMuted },
        stateColumn: { width: 90, color: theme.colors.foreground },
        runColumn: { width: 72, color: theme.colors.foregroundMuted },
        columnHeading: {
          color: theme.colors.foregroundMuted,
          fontSize: 12,
          fontWeight: "600",
          textTransform: "uppercase",
        },
      }),
    [layout.compact, metrics, theme],
  );

  function taskAccessibilityLabel(task: BoardTask): string {
    const run = task.runNumber ? `, run ${task.runNumber}` : "";
    return `${task.title}, ${boardStateLabels[task.state]}, ${task.projectName}${run}`;
  }

  function taskCard(task: BoardTask, showState: boolean) {
    return (
      <View
        accessible
        accessibilityLabel={taskAccessibilityLabel(task)}
        key={task.id}
        style={styles.taskCard}
      >
        <Text style={styles.taskTitle}>{task.title}</Text>
        <Text style={styles.taskMeta}>{task.projectName}</Text>
        {showState ? (
          <Text style={styles.taskState}>{boardStateLabels[task.state]}</Text>
        ) : null}
        {task.runNumber ? (
          <Text style={styles.taskMeta}>Run {task.runNumber}</Text>
        ) : null}
        {task.candidateSha ? (
          <Text style={styles.taskMeta}>
            Candidate {task.candidateSha.slice(0, 7)}
          </Text>
        ) : null}
      </View>
    );
  }

  function boardContent(snapshot: BoardSnapshot) {
    const selectableStates = boardStatesForLayout(false, selectedLane, snapshot.tasks);
    const visibleStates = boardStatesForLayout(
      layout.compact,
      selectedLane,
      snapshot.tasks,
    );
    return (
      <>
        {layout.compact ? (
          <ScrollView
            accessibilityRole="tablist"
            horizontal
            showsHorizontalScrollIndicator={false}
            contentContainerStyle={styles.laneSelector}
          >
            {selectableStates.map((state) => {
              const selected = visibleStates[0] === state;
              return (
                <Pressable
                  accessibilityLabel={`Show ${boardStateLabels[state]} tasks`}
                  accessibilityRole="tab"
                  accessibilityState={{ selected }}
                  key={state}
                  onPress={() => setSelectedLane(state)}
                  style={[styles.control, selected && styles.selectedControl]}
                >
                  <Text
                    style={[
                      styles.controlText,
                      selected && styles.selectedControlText,
                    ]}
                  >
                    {boardStateLabels[state]}
                  </Text>
                </Pressable>
              );
            })}
          </ScrollView>
        ) : null}
        <ScrollView
          horizontal={!layout.compact}
          showsHorizontalScrollIndicator={!layout.compact}
          contentContainerStyle={styles.board}
        >
          {visibleStates.map((state) => {
            const tasks = tasksInState(snapshot.tasks, state);
            return (
              <View key={state} style={styles.lane}>
                <View style={styles.laneHeader}>
                  <Text accessibilityRole="header" style={styles.laneTitle}>
                    {boardStateLabels[state]}
                  </Text>
                  <Text style={styles.laneCount}>{tasks.length}</Text>
                </View>
                {tasks.length === 0 ? (
                  <Text style={styles.empty}>No tasks</Text>
                ) : (
                  tasks.map((task) => taskCard(task, false))
                )}
              </View>
            );
          })}
        </ScrollView>
      </>
    );
  }

  function listContent(snapshot: BoardSnapshot) {
    if (layout.compact) {
      return (
        <View style={styles.list}>
          {snapshot.tasks.map((task) => taskCard(task, true))}
        </View>
      );
    }
    return (
      <View style={styles.list}>
        <View style={styles.listHeader}>
          <Text style={[styles.titleColumn, styles.columnHeading]}>Task</Text>
          <Text style={[styles.projectColumn, styles.columnHeading]}>Project</Text>
          <Text style={[styles.stateColumn, styles.columnHeading]}>State</Text>
          <Text style={[styles.runColumn, styles.columnHeading]}>Run</Text>
        </View>
        {snapshot.tasks.map((task) => (
          <View
            accessible
            accessibilityLabel={taskAccessibilityLabel(task)}
            key={task.id}
            style={styles.listRow}
          >
            <Text style={styles.titleColumn}>{task.title}</Text>
            <Text style={styles.projectColumn}>{task.projectName}</Text>
            <Text style={styles.stateColumn}>{boardStateLabels[task.state]}</Text>
            <Text style={styles.runColumn}>{task.runNumber ?? "—"}</Text>
          </View>
        ))}
      </View>
    );
  }

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
      {board.isFetching && board.data ? (
        <View accessibilityLiveRegion="polite" style={styles.updating}>
          <ActivityIndicator color={theme.colors.accent} size="small" />
          <Text style={styles.updatingText}>Updating tasks</Text>
        </View>
      ) : null}
      {scene.kind === "loading" ? (
        <View accessibilityLiveRegion="polite" style={styles.liveState}>
          <ActivityIndicator color={theme.colors.accent} />
          <Text style={styles.stateTitle}>Loading project tasks</Text>
        </View>
      ) : null}
      {scene.kind === "error" ? (
        <View accessibilityLiveRegion="polite" style={styles.liveState}>
          <Text style={styles.stateTitle}>Board data is unavailable</Text>
          <Text style={styles.stateBody}>
            Director Engine could not provide a current snapshot.
          </Text>
          <Pressable
            accessibilityLabel="Try loading project tasks again"
            accessibilityRole="button"
            onPress={() => void board.refetch()}
            style={styles.retry}
          >
            <Text style={styles.retryText}>Try again</Text>
          </Pressable>
        </View>
      ) : null}
      {scene.kind === "empty" ? (
        <View accessibilityLiveRegion="polite" style={styles.liveState}>
          <Text style={styles.stateTitle}>No tasks yet</Text>
          <Text style={styles.stateBody}>
            Persisted Tasks will appear here when Director Engine adds them.
          </Text>
        </View>
      ) : null}
      {scene.kind === "data"
        ? view === "board"
          ? boardContent(scene.snapshot)
          : listContent(scene.snapshot)
        : null}
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
