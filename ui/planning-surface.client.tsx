// SPDX-License-Identifier: Apache-2.0
// Client-only React Native presentation of engine-owned planning projections.

import type { PluginWorkspacePanelProps } from "@getpaseo/plugin";
import { useRpc } from "@getpaseo/plugin";
import { Icon, Modal } from "@getpaseo/plugin/react-native";
import {
  useMutation,
  useQuery,
} from "@tanstack/react-query";
import React, { useEffect, useMemo, useState } from "react";
import {
  ActivityIndicator,
  FlatList,
  ScrollView,
  StyleSheet,
  Text,
  TextInput,
  View,
} from "react-native";

import {
  bindPlanningMutation,
  PLANNING_ATTENTION_CODES,
  PLANNING_DERIVED_STATES,
  PLANNING_PRIORITIES,
  type AllowedAction,
  type ConfigurationOverride,
  type ConfigurationValue,
  type DerivedState,
  type EpicSummary,
  type PlanningClient,
  type PlanningMutationInput,
  type PlanningMutationIntent,
  type PlanningQueryInput,
  type StableSort,
  type TaskDetail,
  type TaskSummary,
} from "../generated/planning-contract.shared.ts";
import {
  planningMutationRpc,
  planningQueryRpc,
  planningTaskDetailRpc,
} from "../rpc/planning.shared.ts";
import { TaskDetailView, type TaskDetailTab } from "./task-detail-view.client.tsx";
import {
  AccessibilityProvider,
  AccessiblePressable,
  useAccessibilityAnnouncement,
  useAccessibilityPreferences,
  useResponsiveCompactLayout,
} from "./accessibility.client.tsx";

const pageSize = 100;
const initialRequest: Omit<PlanningQueryInput, "cursor"> = {
  projectId: null,
  workspaceIds: [],
  epicIds: [],
  states: [],
  priorities: [],
  labels: [],
  attention: [],
  search: null,
  sort: "scheduler_order",
  pageSize,
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

const sortLabels: Record<StableSort, string> = {
  scheduler_order: "Engine order",
  updated_desc: "Recently updated",
  priority_fifo: "Priority and FIFO",
  key_asc: "Task key",
};

const launchDispositionLabels = {
  eligible: "Eligible",
  waiting: "Waiting",
  needs_you: "Needs you",
} as const;

export const boardLaneStates: readonly Exclude<DerivedState, "done">[] =
  PLANNING_DERIVED_STATES.filter(
    (state): state is Exclude<DerivedState, "done"> => state !== "done",
  );

// The query already contains engine-derived states in engine order. This
// helper only chooses which canonical columns are visible; it never derives a
// state, moves a Task, or sorts a result.
export function visibleBoardLaneStates(
  tasks: readonly TaskSummary[],
  selectedStates: readonly DerivedState[],
): readonly Exclude<DerivedState, "done">[] {
  const selected = selectedStates.filter(
    (state): state is Exclude<DerivedState, "done"> => state !== "done",
  );
  const lanes = selected.length === 0
    ? boardLaneStates
    : boardLaneStates.filter((state) => selected.includes(state));
  return lanes.filter(
    (state) =>
      state !== "needs_you" ||
      tasks.some((task) => task.derivedState === "needs_you"),
  );
}

export function planningDateLabel(value: string): string {
  return value.slice(0, 10);
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

function toggleValue<T>(values: readonly T[], value: T): readonly T[] {
  return values.includes(value)
    ? values.filter((candidate) => candidate !== value)
    : [...values, value];
}

export type PlanningTaskGroup = {
  id: string;
  label: string;
  tasks: readonly TaskSummary[];
};

export type PlanningGroupRow =
  | { kind: "header"; id: string; label: string }
  | { kind: "task"; id: string; task: TaskSummary };

// Grouping preserves the exact engine order within and between first-seen
// groups. It is presentation structure only and never derives state or rank.
export function groupPlanningTasks(
  tasks: readonly TaskSummary[],
  epics: readonly EpicSummary[],
): readonly PlanningTaskGroup[] {
  const labels = new Map(
    epics.map((epic) => [epic.id, `${epic.key} · ${epic.title}`]),
  );
  const groups = new Map<string, PlanningTaskGroup>();
  for (const task of tasks) {
    const groupKey = task.epicId ?? "standalone";
    const existing = groups.get(groupKey);
    if (existing) {
      groups.set(groupKey, { ...existing, tasks: [...existing.tasks, task] });
      continue;
    }
    groups.set(groupKey, {
      id: task.epicId === null ? "standalone" : `epic:${task.epicId}`,
      label: task.epicId === null
        ? "Standalone tasks"
        : labels.get(task.epicId) ?? task.epicId,
      tasks: [task],
    });
  }
  return [...groups.values()];
}

export function planningGroupRows(
  tasks: readonly TaskSummary[],
  epics: readonly EpicSummary[],
): readonly PlanningGroupRow[] {
  return groupPlanningTasks(tasks, epics).flatMap((group) => [
    { kind: "header" as const, id: `group:${group.id}`, label: group.label },
    ...group.tasks.map((task) => ({
      kind: "task" as const,
      id: `task:${task.id}`,
      task,
    })),
  ]);
}

type PlanningSurfaceProps = Pick<
  PluginWorkspacePanelProps,
  "theme" | "layout" | "host"
> & {
  client: PlanningClient;
  navigation?: PluginWorkspacePanelProps["navigation"];
};

export function ProjectBoard(props: PluginWorkspacePanelProps) {
  const queryPlanning = useRpc(planningQueryRpc);
  const queryTaskDetail = useRpc(planningTaskDetailRpc);
  const mutatePlanning = useRpc(planningMutationRpc);
  const client = useMemo<PlanningClient>(
    () => ({
      query: queryPlanning,
      taskDetail: queryTaskDetail,
      mutate: mutatePlanning,
    }),
    [mutatePlanning, queryPlanning, queryTaskDetail],
  );
  return (
    <PlanningSurface
      client={client}
      host={props.host}
      layout={props.layout}
      navigation={props.navigation}
      theme={props.theme}
    />
  );
}

export function PlanningSurface({
  theme,
  layout,
  client,
  host,
  navigation,
}: PlanningSurfaceProps) {
  const accessibilityPreferences = useAccessibilityPreferences();
  const compact = useResponsiveCompactLayout(layout.compact);
  const [view, setView] = useState<"board" | "list">(
    compact ? "list" : "board",
  );
  const [request, setRequest] = useState(initialRequest);
  const [searchDraft, setSearchDraft] = useState("");
  const [filtersExpanded, setFiltersExpanded] = useState(false);
  const [compactLane, setCompactLane] = useState<
    Exclude<DerivedState, "done">
  >("queued");
  const [grouping, setGrouping] = useState<"flat" | "epic">(
    compact ? "epic" : "flat",
  );
  const [selectedTaskId, setSelectedTaskId] = useState<string | null>(null);
  const [modalTab, setModalTab] = useState<TaskDetailTab>("details");
  const [pageCursor, setPageCursor] = useState<string | null>(null);
  const [previousCursors, setPreviousCursors] = useState<
    readonly (string | null)[]
  >([]);

  const planning = useQuery({
    queryKey: ["director", "planning", request, pageCursor],
    queryFn: () => client.query({ ...request, cursor: pageCursor }),
    gcTime: 0,
    retry: false,
  });
  const snapshot = planning.data;
  const tasks = snapshot?.page.tasks ?? [];
  const planningOffline = planning.fetchStatus === "paused";
  const workspaceNames = useMemo(
    () => new Map(
      (snapshot?.page.workspaces ?? []).map((workspace) => [
        workspace.id,
        workspace.name,
      ]),
    ),
    [snapshot?.page.workspaces],
  );
  const epicNames = useMemo(
    () => new Map(
      (snapshot?.page.epics ?? []).map((epic) => [
        epic.id,
        `${epic.key} · ${epic.title}`,
      ]),
    ),
    [snapshot?.page.epics],
  );
  const activeFilterCount =
    request.workspaceIds.length +
    request.epicIds.length +
    request.states.length +
    request.priorities.length +
    request.labels.length +
    request.attention.length +
    (request.search === null ? 0 : 1) +
    (request.sort === initialRequest.sort ? 0 : 1);
  const selectedSummary = tasks.find((task) => task.id === selectedTaskId);
  const planningAnnouncement = planningOffline && !snapshot
    ? "Waiting for connection before loading planning data"
    : planning.isPending && !snapshot
      ? "Loading planning data"
      : planning.isError && !snapshot
        ? "Planning data is unavailable"
        : snapshot && planning.isFetching
          ? "Updating the engine planning snapshot"
          : snapshot && (planning.isError || planningOffline)
            ? "Showing the last engine snapshot. Tasks may be stale"
            : snapshot && snapshot.page.projects.length === 0
              ? "No Director Projects are available"
              : snapshot && tasks.length === 0
                ? "No tasks match this query"
                : snapshot
                  ? `${tasks.length} tasks shown in ${view} view${compact && view === "board" ? ", one lane at a time" : ""}`
                  : null;
  useAccessibilityAnnouncement(planningAnnouncement);

  const detail = useQuery({
    queryKey: ["director", "planning-task", host.id, selectedTaskId],
    queryFn: () =>
      client.taskDetail({
        hostId: host.id,
        context: "board",
        taskId: selectedTaskId,
        paseoWorkspaceId: null,
        paseoAgentId: null,
        afterCursor: null,
      }),
    enabled: selectedTaskId !== null,
    retry: false,
  });
  const detailOffline = planningOffline || detail.fetchStatus === "paused";
  const mutation = useMutation({
    mutationFn: (input: PlanningMutationInput) => client.mutate(input),
  });
  const [configurationDraft, setConfigurationDraft] = useState<
    readonly ConfigurationOverride[]
  >([]);
  const [preview, setPreview] = useState<
    Awaited<ReturnType<PlanningClient["mutate"]>>["preview"]
  >(null);
  const [pendingApply, setPendingApply] = useState<AllowedAction | null>(null);
  useAccessibilityAnnouncement(
    selectedTaskId === null
      ? null
      : detailOffline && !detail.data
        ? "Waiting for this host before loading Task details"
        : detail.isPending && !detail.data
          ? "Loading Task details"
          : detail.isError && !detail.data?.detail
            ? "Task details are unavailable"
            : detail.data?.detail
              ? `${detail.data.detail.summary.key} Task details opened`
              : detail.data
                ? "No Task details are available for this exact host"
                : null,
  );
  useAccessibilityAnnouncement(
    mutation.isPending
      ? "Submitting intent to Director Engine"
      : mutation.isError
        ? "Director Engine rejected the intent"
        : mutation.data?.message ?? null,
  );

  function openTask(taskId: string) {
    setSelectedTaskId(taskId);
    setModalTab("details");
    setPreview(null);
    setPendingApply(null);
    setConfigurationDraft([]);
  }

  useEffect(() => {
    if (!detail.data?.detail) return;
    setConfigurationDraft(
      detail.data.detail.configuration.map((entry) => entry.configured),
    );
    setPreview(detail.data.detail.configurationPreview);
    setPendingApply(null);
  }, [detail.data?.cursor]);

  useEffect(() => {
    if (!compact) return;
    setView("list");
    setGrouping("epic");
  }, [compact]);

  const styles = useMemo(
    () =>
      StyleSheet.create({
        screen: {
          flex: 1,
          backgroundColor: theme.colors.surface0,
        },
        content: {
          padding: compact ? 12 : 16,
          gap: compact ? 10 : 12,
        },
        toolbar: {
          flexDirection: "row",
          alignItems: "center",
          justifyContent: "space-between",
          flexWrap: "wrap",
          gap: 8,
          paddingBottom: 10,
          borderBottomWidth: accessibilityPreferences.highContrast ? 2 : 1,
          borderBottomColor: theme.colors.border,
        },
        toolbarModes: {
          width: compact ? "100%" : undefined,
          flexDirection: compact ? "column" : "row",
          alignItems: compact ? "stretch" : "center",
          flexWrap: "wrap",
          gap: 8,
        },
        segmentGroup: {
          width: compact ? "100%" : undefined,
          flexDirection: "row",
          alignItems: "center",
          flexWrap: "wrap",
          gap: 2,
          padding: 2,
          borderRadius: 9,
          backgroundColor: theme.colors.surface1,
        },
        segment: {
          minHeight: 40,
          flexDirection: "row",
          alignItems: "center",
          justifyContent: "center",
          gap: 6,
          paddingHorizontal: 11,
          paddingVertical: 7,
          borderRadius: 7,
          flexGrow: compact ? 1 : 0,
          flexShrink: 1,
          flexBasis: compact ? 0 : "auto",
        },
        segmentSelected: { backgroundColor: theme.colors.surface2 },
        segmentText: { color: theme.colors.foregroundMuted, fontWeight: "600", textAlign: "center" },
        segmentTextSelected: { color: theme.colors.foreground, fontWeight: "700", textAlign: "center" },
        filterButton: {
          alignSelf: compact ? "stretch" : "auto",
          minHeight: 44,
          flexDirection: "row",
          alignItems: "center",
          justifyContent: "center",
          gap: 7,
          paddingHorizontal: 12,
          borderRadius: 8,
          backgroundColor: theme.colors.surface1,
        },
        filterButtonText: { color: theme.colors.foreground, fontWeight: "600" },
        contextBar: {
          flexDirection: "row",
          alignItems: "center",
          flexWrap: "wrap",
          gap: 7,
          paddingVertical: 2,
        },
        contextStrong: { color: theme.colors.foreground, fontWeight: "700" },
        contextText: { color: theme.colors.foregroundMuted, fontSize: 12 },
        sectionLabel: {
          color: theme.colors.foregroundMuted,
          fontSize: 11,
          fontWeight: "700",
          textTransform: "uppercase",
        },
        row: { flexDirection: "row", alignItems: "center", gap: 8 },
        wrap: { flexDirection: "row", flexWrap: "wrap", gap: 8 },
        switcherContent: { gap: 8, paddingVertical: 2 },
        chip: {
          minHeight: 44,
          paddingHorizontal: 11,
          paddingVertical: 8,
          borderRadius: 9,
          borderWidth: accessibilityPreferences.highContrast ? 2 : 1,
          borderColor: theme.colors.border,
          backgroundColor: theme.colors.surface2,
        },
        chipSelected: {
          borderColor: theme.colors.accent,
          backgroundColor: theme.colors.accent,
        },
        chipText: { color: theme.colors.foreground, fontWeight: "600" },
        chipTextSelected: { color: theme.colors.accentForeground },
        navCard: {
          gap: 8,
          paddingBottom: 16,
          borderBottomWidth: 1,
          borderBottomColor: theme.colors.border,
        },
        controls: {
          gap: 10,
          paddingTop: 2,
        },
        filterContent: { gap: 16, paddingBottom: 20 },
        input: {
          minHeight: 44,
          paddingHorizontal: 12,
          paddingVertical: 9,
          borderRadius: 8,
          borderWidth: accessibilityPreferences.highContrast ? 2 : 1,
          borderColor: theme.colors.border,
          color: theme.colors.foreground,
          backgroundColor: theme.colors.surface2,
        },
        muted: { color: theme.colors.foregroundMuted },
        live: {
          minHeight: 130,
          padding: 20,
          alignItems: "center",
          justifyContent: "center",
          gap: 10,
        },
        liveTitle: {
          color: theme.colors.foreground,
          fontSize: 16,
          fontWeight: "700",
          textAlign: "center",
        },
        liveBody: { color: theme.colors.foregroundMuted, textAlign: "center" },
        board: { gap: 12, paddingBottom: 8 },
        lane: {
          width: compact ? "100%" : 260,
          maxHeight: compact ? undefined : 560,
          padding: 10,
          gap: 8,
          borderRadius: 8,
          borderTopWidth: accessibilityPreferences.highContrast ? 5 : 3,
          backgroundColor: theme.colors.surface1,
        },
        laneHeader: {
          flexDirection: "row",
          justifyContent: "space-between",
          alignItems: "center",
          gap: 8,
          paddingBottom: 8,
          borderBottomWidth: accessibilityPreferences.highContrast ? 2 : 1,
          borderBottomColor: theme.colors.border,
        },
        laneTitle: { color: theme.colors.foreground, fontWeight: "700" },
        laneCount: { color: theme.colors.foregroundMuted, fontSize: 12 },
        laneTasks: { gap: 8 },
        taskGroup: { gap: 7 },
        taskGroupTitle: {
          color: theme.colors.foregroundMuted,
          fontSize: 12,
          fontWeight: "700",
        },
        task: {
          minHeight: 44,
          paddingHorizontal: 10,
          paddingVertical: 9,
          gap: 4,
          borderRadius: 7,
          borderLeftWidth: accessibilityPreferences.highContrast ? 5 : 3,
          backgroundColor: theme.colors.surface2,
        },
        taskHeader: {
          flexDirection: "row",
          justifyContent: "space-between",
          flexWrap: "wrap",
          gap: 8,
        },
        taskKey: { color: theme.colors.foregroundMuted, fontSize: 12 },
        taskPriority: { color: theme.colors.foregroundMuted, fontSize: 12 },
        taskTitle: { color: theme.colors.foreground, fontWeight: "700", flexShrink: 1 },
        taskMeta: { color: theme.colors.foregroundMuted, fontSize: 12 },
        attention: { color: theme.colors.statusWarning, fontSize: 12 },
        labelRow: { flexDirection: "row", flexWrap: "wrap", gap: 5 },
        label: {
          color: theme.colors.foregroundMuted,
          fontSize: 11,
          paddingHorizontal: 6,
          paddingVertical: 2,
          borderRadius: 6,
          borderWidth: accessibilityPreferences.highContrast ? 2 : 1,
          borderColor: theme.colors.border,
        },
        list: {
          overflow: "hidden",
          borderRadius: 8,
        },
        listHeader: {
          minHeight: 40,
          flexDirection: "row",
          alignItems: "center",
          gap: 12,
          paddingHorizontal: 12,
          paddingVertical: 8,
          borderBottomWidth: accessibilityPreferences.highContrast ? 2 : 1,
          borderBottomColor: theme.colors.border,
          backgroundColor: theme.colors.surface2,
        },
        listRow: {
          minHeight: 52,
          flexDirection: "row",
          alignItems: "center",
          gap: 12,
          paddingHorizontal: 12,
          paddingVertical: 9,
          borderBottomWidth: accessibilityPreferences.highContrast ? 2 : 1,
          borderBottomColor: theme.colors.border,
          backgroundColor: theme.colors.surface0,
        },
        compactListRow: {
          minHeight: 56,
          flexDirection: "row",
          alignItems: "center",
          gap: 10,
          padding: 11,
          borderBottomWidth: accessibilityPreferences.highContrast ? 2 : 1,
          borderBottomColor: theme.colors.border,
          backgroundColor: theme.colors.surface0,
        },
        listGroup: {
          paddingHorizontal: 12,
          paddingVertical: 8,
          backgroundColor: theme.colors.surface2,
          borderBottomWidth: 1,
          borderBottomColor: theme.colors.border,
        },
        columnHeading: {
          color: theme.colors.foregroundMuted,
          fontSize: 11,
          fontWeight: "700",
          textTransform: "uppercase",
        },
        taskColumn: { flex: 2.4, minWidth: 180 },
        stateColumn: { width: 96 },
        workspaceColumn: { flex: 1.2, minWidth: 110 },
        epicColumn: { flex: 1.2, minWidth: 110 },
        priorityColumn: { width: 72 },
        updatedColumn: { width: 88 },
        compactTaskColumn: { flex: 1, flexShrink: 1, minWidth: 0, gap: 3 },
        compactStatusColumn: { width: "32%", minWidth: 92, alignItems: "flex-end", gap: 3 },
        listPrimary: { color: theme.colors.foreground, fontWeight: "700" },
        listValue: { color: theme.colors.foreground, fontSize: 12 },
        listSecondary: { color: theme.colors.foregroundMuted, fontSize: 12 },
        footer: { padding: 12, alignItems: "center" },
        primaryButton: {
          minHeight: 44,
          alignItems: "center",
          justifyContent: "center",
          paddingHorizontal: 14,
          paddingVertical: 10,
          borderRadius: 8,
          backgroundColor: theme.colors.accent,
        },
        primaryButtonText: {
          color: theme.colors.accentForeground,
          fontWeight: "700",
        },
        secondaryButton: {
          minHeight: 44,
          alignItems: "center",
          justifyContent: "center",
          paddingHorizontal: 12,
          paddingVertical: 9,
          borderRadius: 8,
          borderWidth: accessibilityPreferences.highContrast ? 2 : 1,
          borderColor: theme.colors.border,
          backgroundColor: theme.colors.surface2,
        },
        secondaryButtonText: { color: theme.colors.foreground, fontWeight: "600" },
        modalContent: { gap: 14, paddingBottom: 20 },
        modalSection: {
          gap: 8,
          padding: 12,
          borderRadius: 10,
          borderWidth: accessibilityPreferences.highContrast ? 2 : 1,
          borderColor: theme.colors.border,
          backgroundColor: theme.colors.surface1,
        },
        modalTitle: {
          color: theme.colors.foreground,
          fontSize: 18,
          fontWeight: "700",
        },
        body: { color: theme.colors.foreground, lineHeight: 20 },
        success: { color: theme.colors.statusSuccess },
        warning: { color: theme.colors.statusWarning },
        danger: { color: theme.colors.statusDanger },
        divider: { height: 1, backgroundColor: theme.colors.border },
      }),
    [accessibilityPreferences.highContrast, compact, theme],
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

  function updateRequest(patch: Partial<typeof request>) {
    setRequest((current) => ({ ...current, ...patch }));
    setPageCursor(null);
    setPreviousCursors([]);
    setSelectedTaskId(null);
  }

  function selectView(next: "board" | "list") {
    setView(next);
    setGrouping(next === "list" ? "epic" : "flat");
    if (next === "board" && request.states.includes("done")) {
      updateRequest({ states: [] });
    }
  }

  function toggleStateFilter(state: DerivedState) {
    if (state === "done") {
      const selectingHistory = !request.states.includes("done");
      if (selectingHistory) setView("list");
      updateRequest({ states: selectingHistory ? ["done"] : [] });
      return;
    }
    const activeStates = request.states.filter((candidate) => candidate !== "done");
    updateRequest({ states: toggleValue(activeStates, state) });
  }

  function openNextPage() {
    const next = snapshot?.page.nextCursor;
    if (!next) return;
    setPreviousCursors((current) => [...current, pageCursor]);
    setPageCursor(next);
    setSelectedTaskId(null);
  }

  function openPreviousPage() {
    const previous = previousCursors.at(-1);
    if (previous === undefined) return;
    setPreviousCursors((current) => current.slice(0, -1));
    setPageCursor(previous);
    setSelectedTaskId(null);
  }

  function refreshFromFirstPage() {
    const alreadyFirst = pageCursor === null;
    setPageCursor(null);
    setPreviousCursors([]);
    setSelectedTaskId(null);
    if (alreadyFirst) void planning.refetch();
  }

  function submitAction(action: AllowedAction, intent: PlanningMutationIntent) {
    mutation.mutate(bindPlanningMutation(action, intent), {
      onSuccess(result) {
        if (result.preview !== null) {
          setPreview(result.preview);
          return;
        }
        if (intent.type === "configuration.apply") {
          setPendingApply(null);
          setPreview(null);
        }
        void planning.refetch();
        if (selectedTaskId !== null) void detail.refetch();
      },
    });
  }

  function replaceConfigurationDraft(next: ConfigurationOverride) {
    setConfigurationDraft((current) => [
      ...current.filter((candidate) => candidate.key !== next.key),
      next,
    ]);
    setPreview(null);
    setPendingApply(null);
  }

  function taskAccessibilityLabel(task: TaskSummary): string {
    const explanation = task.needsYou[0] ?? task.blockers[0];
    return [
      task.key,
      task.title,
      stateLabels[task.derivedState],
      `${task.priority} priority`,
      workspaceNames.get(task.workspaceId) ?? task.workspaceId,
      task.epicId === null
        ? "Standalone task"
        : epicNames.get(task.epicId) ?? task.epicId,
      explanation?.message,
    ]
      .filter(Boolean)
      .join(", ");
  }

  function taskCard(task: TaskSummary) {
    const explanation = task.needsYou[0] ?? task.blockers[0];
    return (
      <AccessiblePressable
        accessibilityHint="Opens engine-projected task details. Task state cannot be moved here."
        accessibilityLabel={taskAccessibilityLabel(task)}
        accessibilityRole="button"
        focusable
        key={task.id}
        onPress={() => openTask(task.id)}
        style={[
          styles.task,
          { borderLeftColor: stateAccent(task.derivedState) },
        ]}
      >
        <View style={styles.taskHeader}>
          <Text style={styles.taskKey}>{task.key}</Text>
          <Text style={styles.taskPriority}>{task.priority}</Text>
        </View>
        <Text style={styles.taskTitle}>{task.title}</Text>
        <Text style={styles.taskMeta}>
          {workspaceNames.get(task.workspaceId) ?? task.workspaceId}
          {task.epicId === null
            ? " · Standalone"
            : ` · ${epicNames.get(task.epicId) ?? task.epicId}`}
        </Text>
        <Text style={styles.taskMeta}>
          Execution · {valueLabel(task.schedulingFacts.launchMode)} · {launchDispositionLabels[task.schedulingFacts.launchDisposition]}
        </Text>
        {task.runtimeBudget ? (
          <Text style={task.runtimeBudget.state === "current" ? styles.taskMeta : styles.attention}>
            Budget {attentionLabel(task.runtimeBudget.state)}
            {task.runtimeBudget.reasonCode ? ` · ${task.runtimeBudget.reasonCode}` : ""}
          </Text>
        ) : null}
        {explanation ? (
          <Text style={task.needsYou.length > 0 ? styles.attention : styles.taskMeta}>
            {explanation.message}
          </Text>
        ) : null}
        {task.labels.length > 0 ? (
          <View style={styles.labelRow}>
            {task.labels.slice(0, 3).map((label) => (
              <Text key={label} style={styles.label}>{label}</Text>
            ))}
            {task.labels.length > 3 ? (
              <Text style={styles.label}>+{task.labels.length - 3}</Text>
            ) : null}
          </View>
        ) : null}
      </AccessiblePressable>
    );
  }

  function virtualizedTasks(data: readonly TaskSummary[], scrollEnabled = true) {
    return (
      <FlatList
        data={data}
        initialNumToRender={12}
        keyExtractor={(task) => task.id}
        maxToRenderPerBatch={12}
        removeClippedSubviews
        renderItem={({ item }) => taskCard(item)}
        scrollEnabled={scrollEnabled}
        style={{ flexGrow: 0 }}
        contentContainerStyle={styles.laneTasks}
        windowSize={5}
      />
    );
  }

  function virtualizedGroups(
    data: readonly TaskSummary[],
    scrollEnabled = true,
  ) {
    const rows = planningGroupRows(data, snapshot?.page.epics ?? []);
    return (
      <FlatList
        data={rows}
        initialNumToRender={16}
        keyExtractor={(row) => row.id}
        maxToRenderPerBatch={16}
        removeClippedSubviews
        renderItem={({ item: row }) =>
          row.kind === "header" ? (
            <View accessible accessibilityLabel={row.label} style={styles.taskGroup}>
              <Text accessibilityRole="header" style={styles.taskGroupTitle}>
                {row.label}
              </Text>
            </View>
          ) : taskCard(row.task)
        }
        style={{ flexGrow: 0 }}
        contentContainerStyle={styles.laneTasks}
        scrollEnabled={scrollEnabled}
        windowSize={7}
      />
    );
  }

  function listTaskRow(task: TaskSummary) {
    const workspace = workspaceNames.get(task.workspaceId) ?? task.workspaceId;
    const epic = task.epicId === null
      ? "Standalone"
      : epicNames.get(task.epicId) ?? task.epicId;
    const stateStyle = [
      styles.listValue,
      { color: stateAccent(task.derivedState) },
    ];
    return (
      <AccessiblePressable
        accessibilityHint="Opens engine-projected task details. Task state cannot be moved here."
        accessibilityLabel={taskAccessibilityLabel(task)}
        accessibilityRole="button"
        focusable
        onPress={() => openTask(task.id)}
        style={compact ? styles.compactListRow : styles.listRow}
      >
        {compact ? (
          <>
            <View style={styles.compactTaskColumn}>
              <Text style={styles.listPrimary}>
                {task.key} · {task.title}
              </Text>
              <Text style={styles.listSecondary}>
                {workspace} · {epic}
              </Text>
            </View>
            <View style={styles.compactStatusColumn}>
              <Text style={stateStyle}>{stateLabels[task.derivedState]}</Text>
              <Text style={styles.listSecondary}>{task.priority}</Text>
            </View>
          </>
        ) : (
          <>
            <View style={styles.taskColumn}>
            <Text style={styles.listPrimary}>{task.title}</Text>
              <Text style={styles.listSecondary}>{task.key}</Text>
            </View>
            <Text style={[styles.stateColumn, ...stateStyle]}>
              {stateLabels[task.derivedState]}
            </Text>
            <Text style={[styles.workspaceColumn, styles.listValue]}>
              {workspace}
            </Text>
            <Text style={[styles.epicColumn, styles.listValue]}>
              {epic}
            </Text>
            <Text style={[styles.priorityColumn, styles.listValue]}>{task.priority}</Text>
            <Text style={[styles.updatedColumn, styles.listSecondary]}>
              {planningDateLabel(task.updatedAt)}
            </Text>
          </>
        )}
      </AccessiblePressable>
    );
  }

  function renderList() {
    const rows: readonly PlanningGroupRow[] = grouping === "epic"
      ? planningGroupRows(tasks, snapshot?.page.epics ?? [])
      : tasks.map((task) => ({
          kind: "task" as const,
          id: `task:${task.id}`,
          task,
        }));
    return (
      <View style={styles.list}>
        <View
          accessible
          accessibilityLabel={compact
            ? "Task list columns: Task and status"
            : "Task list columns: Task, State, Workspace, Epic, Priority, Updated"}
          style={styles.listHeader}
        >
          {compact ? (
            <>
              <Text style={[styles.compactTaskColumn, styles.columnHeading]}>Task</Text>
              <Text style={[styles.compactStatusColumn, styles.columnHeading]}>Status</Text>
            </>
          ) : (
            <>
              <Text style={[styles.taskColumn, styles.columnHeading]}>Task</Text>
              <Text style={[styles.stateColumn, styles.columnHeading]}>State</Text>
              <Text style={[styles.workspaceColumn, styles.columnHeading]}>Workspace</Text>
              <Text style={[styles.epicColumn, styles.columnHeading]}>Epic</Text>
              <Text style={[styles.priorityColumn, styles.columnHeading]}>Priority</Text>
              <Text style={[styles.updatedColumn, styles.columnHeading]}>Updated</Text>
            </>
          )}
        </View>
        <FlatList
          data={rows}
          initialNumToRender={16}
          keyExtractor={(row) => row.id}
          maxToRenderPerBatch={16}
          removeClippedSubviews
          renderItem={({ item: row }) => row.kind === "header" ? (
            <View accessible accessibilityLabel={row.label} style={styles.listGroup}>
              <Text accessibilityRole="header" style={styles.taskGroupTitle}>
                {row.label}
              </Text>
            </View>
          ) : listTaskRow(row.task)}
          scrollEnabled={false}
          windowSize={7}
        />
      </View>
    );
  }

  function renderBoard() {
    const states = visibleBoardLaneStates(
      tasks,
      snapshot?.page.appliedQuery.states ?? [],
    );
    const selectedCompactLane = states.includes(compactLane)
      ? compactLane
      : states[0] ?? "queued";
    const visibleStates = compact ? [selectedCompactLane] : states;
    return (
      <View style={{ gap: 8 }}>
        {compact ? (
          <FlatList
            accessibilityLabel="Board lanes"
            accessibilityRole="tablist"
            contentContainerStyle={styles.switcherContent}
            data={states}
            horizontal
            initialNumToRender={7}
            keyExtractor={(state) => state}
            maxToRenderPerBatch={7}
            renderItem={({ item: state }) => {
              const selected = selectedCompactLane === state;
              return (
                <AccessiblePressable
                  accessibilityHint="Shows this engine-derived lane without changing Task state"
                  accessibilityLabel={`Show ${stateLabels[state]} lane`}
                  accessibilityRole="tab"
                  accessibilityState={{ selected }}
                  focusable
                  onPress={() => setCompactLane(state)}
                  style={[styles.chip, selected && styles.chipSelected]}
                >
                  <Text style={[styles.chipText, selected && styles.chipTextSelected]}>
                    {stateLabels[state]}
                  </Text>
                </AccessiblePressable>
              );
            }}
            showsHorizontalScrollIndicator={false}
            windowSize={3}
          />
        ) : null}
        <FlatList
          contentContainerStyle={styles.board}
          data={visibleStates}
          horizontal={!compact}
          initialNumToRender={compact ? 1 : 6}
          keyExtractor={(state) => state}
          maxToRenderPerBatch={compact ? 1 : 6}
          renderItem={({ item: state }) => {
            const laneTasks = tasks.filter((task) => task.derivedState === state);
            return (
              <View
                style={[styles.lane, { borderTopColor: stateAccent(state) }]}
              >
                <View style={styles.laneHeader}>
                  <Text accessibilityRole="header" style={styles.laneTitle}>
                    {stateLabels[state]}
                  </Text>
                  <Text
                    accessibilityLabel={`${laneTasks.length} tasks on this page`}
                    style={styles.laneCount}
                  >
                    {laneTasks.length} on page
                  </Text>
                </View>
                {laneTasks.length === 0 ? (
                  <Text style={styles.muted}>No tasks</Text>
                ) : grouping === "epic" ? (
                  virtualizedGroups(laneTasks, !compact)
                ) : (
                  virtualizedTasks(laneTasks, !compact)
                )}
              </View>
            );
          }}
          showsHorizontalScrollIndicator={!compact}
          windowSize={3}
        />
      </View>
    );
  }

  function renderDetail(task: TaskDetail) {
    const workspaceName = workspaceNames.get(task.summary.workspaceId);
    const epicName = task.summary.epicId
      ? epicNames.get(task.summary.epicId)
      : undefined;

    return (
      <View style={{ minHeight: compact ? 420 : 500, flex: 1 }}>
        <TaskDetailView
          activeTab={modalTab}
          activityState={{
            isPending: detail.isFetching,
            isError: detail.isError,
            isOffline: detailOffline,
            isStale: (detail.isFetching || detail.isError) && Boolean(detail.data),
            onReload: () => void detail.refetch(),
          }}
          configurationDraft={configurationDraft}
          epicName={epicName}
          layout={{ ...layout, compact }}
          navigation={navigation}
          onAction={submitAction}
          onConfigurationDraftChange={(draft) => {
            setConfigurationDraft(draft);
            setPreview(null);
            setPendingApply(null);
          }}
          onPendingApplyChange={setPendingApply}
          onTabChange={setModalTab}
          pendingApply={pendingApply}
          preview={preview}
          task={task}
          theme={theme}
          workspaceName={workspaceName}
        />
      </View>
    );
  }

  const selectedProjectId = snapshot?.page.selectedProjectId ?? null;
  const selectedWorkspaceIds = snapshot?.page.selectedWorkspaceIds ?? [];
  const selectedEpicIds = snapshot?.page.selectedEpicIds ?? [];
  const selectedProject = snapshot?.page.projects.find(
    (project) => project.id === selectedProjectId,
  );

  return (
    <AccessibilityProvider
      focusColor={theme.colors.accent}
      preferences={accessibilityPreferences}
    >
    <View style={styles.screen}>
      <ScrollView contentContainerStyle={styles.content}>
        <View style={styles.toolbar}>
          <View style={styles.toolbarModes}>
            <View
              accessibilityLabel="Planning view"
              accessibilityRole="tablist"
              style={styles.segmentGroup}
            >
              {(["board", "list"] as const).map((choice) => {
                const selected = view === choice;
                return (
                  <AccessiblePressable
                    accessibilityHint={`Shows the ${choice} presentation without changing Task state`}
                    accessibilityLabel={choice === "board" ? "Board" : "List"}
                    accessibilityRole="tab"
                    accessibilityState={{ selected }}
                    focusable
                    key={choice}
                    onPress={() => selectView(choice)}
                    style={[styles.segment, selected && styles.segmentSelected]}
                  >
                    <Icon
                      color={selected
                        ? theme.colors.foreground
                        : theme.colors.foregroundMuted}
                      name={choice === "board" ? "Columns3" : "List"}
                      size={16}
                    />
                    <Text style={selected
                      ? styles.segmentTextSelected
                      : styles.segmentText}>
                      {choice === "board" ? "Board" : "List"}
                    </Text>
                  </AccessiblePressable>
                );
              })}
            </View>
            <View
              accessibilityLabel="Task grouping"
              accessibilityRole="tablist"
              style={styles.segmentGroup}
            >
              {(["flat", "epic"] as const).map((choice) => {
                const selected = grouping === choice;
                return (
                  <AccessiblePressable
                    accessibilityHint="Changes grouping only; engine order and Task state stay unchanged"
                    accessibilityLabel={choice === "flat"
                      ? "Show flat tasks"
                      : "Group tasks by epic"}
                    accessibilityRole="tab"
                    accessibilityState={{ selected }}
                    focusable
                    key={choice}
                    onPress={() => setGrouping(choice)}
                    style={[styles.segment, selected && styles.segmentSelected]}
                  >
                    <Text style={selected
                      ? styles.segmentTextSelected
                      : styles.segmentText}>
                      {choice === "flat" ? "Flat" : "By epic"}
                    </Text>
                  </AccessiblePressable>
                );
              })}
            </View>
          </View>
          {snapshot ? (
            <AccessiblePressable
              accessibilityHint="Opens Paseo’s native filter dialog or compact bottom sheet"
              accessibilityLabel="Open task filters"
              accessibilityRole="button"
              accessibilityState={{ expanded: filtersExpanded }}
              focusable
              onPress={() => setFiltersExpanded(true)}
              style={styles.filterButton}
            >
              <Icon color={theme.colors.foregroundMuted} name="ListFilter" size={16} />
              <Text style={styles.filterButtonText}>
                Filters{activeFilterCount > 0 ? ` · ${activeFilterCount}` : ""}
              </Text>
            </AccessiblePressable>
          ) : null}
        </View>

        {snapshot ? (
          <View style={styles.contextBar}>
            <Icon color={theme.colors.foregroundMuted} name="Gauge" size={15} />
            <Text style={styles.contextStrong}>
              {selectedProject?.name ?? "Director"}
            </Text>
            <Text style={styles.contextText}>
              {snapshot.page.totalTasks} matching{"\u00a0·\u00a0"}
              {snapshot.page.capacity.activeTasks}/{snapshot.page.capacity.maxActiveTasks} active{"\u00a0·\u00a0"}
              {snapshot.page.capacity.activeAgents}/{snapshot.page.capacity.maxConcurrentAgents} agents{"\u00a0·\u00a0"}
              {sortLabels[request.sort]}
            </Text>
          </View>
        ) : null}

        {snapshot ? (
          <Modal
            icon={<Icon color={theme.colors.foreground} name="ListFilter" size={18} />}
            onOpenChange={setFiltersExpanded}
            open={filtersExpanded}
            title="Filter tasks"
          >
            <Modal.Content>
              <ScrollView contentContainerStyle={styles.filterContent}>
                <View style={styles.navCard}>
                  <Text style={styles.sectionLabel}>Projects</Text>
                  <FlatList
                    contentContainerStyle={styles.switcherContent}
                    data={snapshot.page.projects}
                    horizontal
                    initialNumToRender={8}
                    keyExtractor={(project) => project.id}
                    maxToRenderPerBatch={8}
                    renderItem={({ item: project }) => {
                      const selected = selectedProjectId === project.id;
                      return (
                        <AccessiblePressable
                          accessibilityHint="Loads this Project’s engine-projected Task query"
                          accessibilityLabel={`Open project ${project.name}`}
                          accessibilityRole="button"
                          accessibilityState={{ selected }}
                          focusable
                          onPress={() => {
                            setRequest({ ...initialRequest, projectId: project.id });
                            setSearchDraft("");
                            setPageCursor(null);
                            setPreviousCursors([]);
                            setSelectedTaskId(null);
                          }}
                          style={[styles.chip, selected && styles.chipSelected]}
                        >
                          <Text style={[styles.chipText, selected && styles.chipTextSelected]}>
                            {project.name}
                          </Text>
                        </AccessiblePressable>
                      );
                    }}
                    showsHorizontalScrollIndicator={false}
                    windowSize={4}
                  />
                  <Text style={styles.sectionLabel}>Workspaces</Text>
                  <FlatList
                    contentContainerStyle={styles.switcherContent}
                    data={snapshot.page.workspaces}
                    horizontal
                    initialNumToRender={10}
                    keyExtractor={(workspace) => workspace.id}
                    maxToRenderPerBatch={10}
                    renderItem={({ item: workspace }) => {
                      const selected = selectedWorkspaceIds.includes(workspace.id);
                      return (
                        <AccessiblePressable
                          accessibilityHint="Toggles this Workspace in the engine query"
                          accessibilityLabel={`Filter workspace ${workspace.name}`}
                          accessibilityRole="button"
                          accessibilityState={{ selected }}
                          focusable
                          onPress={() =>
                            updateRequest({
                              workspaceIds: toggleValue(request.workspaceIds, workspace.id),
                            })
                          }
                          style={[styles.chip, selected && styles.chipSelected]}
                        >
                          <Text style={[styles.chipText, selected && styles.chipTextSelected]}>
                            {workspace.name}
                          </Text>
                        </AccessiblePressable>
                      );
                    }}
                    showsHorizontalScrollIndicator={false}
                    windowSize={4}
                  />
                  <Text style={styles.sectionLabel}>Epics</Text>
                  <FlatList
                    contentContainerStyle={styles.switcherContent}
                    data={snapshot.page.epics}
                    horizontal
                    initialNumToRender={8}
                    keyExtractor={(epic) => epic.id}
                    maxToRenderPerBatch={8}
                    renderItem={({ item: epic }) => {
                      const selected = selectedEpicIds.includes(epic.id);
                      return (
                        <AccessiblePressable
                          accessibilityHint="Toggles this Epic in the engine query"
                          accessibilityLabel={`Filter epic ${epic.key}`}
                          accessibilityRole="button"
                          accessibilityState={{ selected }}
                          focusable
                          onPress={() =>
                            updateRequest({ epicIds: toggleValue(request.epicIds, epic.id) })
                          }
                          style={[styles.chip, selected && styles.chipSelected]}
                        >
                          <Text style={[styles.chipText, selected && styles.chipTextSelected]}>
                            {epic.key}
                          </Text>
                        </AccessiblePressable>
                      );
                    }}
                    showsHorizontalScrollIndicator={false}
                    windowSize={4}
                  />
                </View>

                <View style={styles.controls}>
                  {filtersExpanded ? (
                    <>
                      <Text style={styles.sectionLabel}>Search</Text>
                      <TextInput
                        accessibilityHint="Press Search to apply this term to the engine query"
                        accessibilityLabel="Search tasks"
                        onChangeText={setSearchDraft}
                        onSubmitEditing={() =>
                          updateRequest({ search: searchDraft.trim() || null })
                        }
                        placeholder="Search task key or title"
                        placeholderTextColor={theme.colors.foregroundMuted}
                        returnKeyType="search"
                        style={styles.input}
                        value={searchDraft}
                      />
                      <Text style={styles.sectionLabel}>State</Text>
                      <View style={styles.wrap}>
                        {PLANNING_DERIVED_STATES.map((state) => {
                          const selected = snapshot.page.appliedQuery.states.includes(state);
                          return (
                            <AccessiblePressable
                              accessibilityHint="Toggles this derived state in the engine query"
                              accessibilityLabel={`Filter state ${stateLabels[state]}`}
                              accessibilityRole="button"
                              accessibilityState={{ selected }}
                              focusable
                              key={state}
                              onPress={() => toggleStateFilter(state)}
                              style={[styles.chip, selected && styles.chipSelected]}
                            >
                              <Text style={[styles.chipText, selected && styles.chipTextSelected]}>
                                {state === "done" ? "Done history" : stateLabels[state]}
                              </Text>
                            </AccessiblePressable>
                          );
                        })}
                      </View>
                      <Text style={styles.sectionLabel}>Priority</Text>
                      <View style={styles.wrap}>
                        {PLANNING_PRIORITIES.map((priority) => {
                          const selected = snapshot.page.appliedQuery.priorities.includes(priority);
                          return (
                            <AccessiblePressable
                              accessibilityHint="Toggles this priority in the engine query"
                              accessibilityLabel={`Filter priority ${priority}`}
                              accessibilityRole="button"
                              accessibilityState={{ selected }}
                              focusable
                              key={priority}
                              onPress={() => updateRequest({ priorities: toggleValue(request.priorities, priority) })}
                              style={[styles.chip, selected && styles.chipSelected]}
                            >
                              <Text style={[styles.chipText, selected && styles.chipTextSelected]}>
                                {priority}
                              </Text>
                            </AccessiblePressable>
                          );
                        })}
                      </View>
                      <Text style={styles.sectionLabel}>Labels</Text>
                      <View style={styles.wrap}>
                        {snapshot.page.availableLabels.map((label) => {
                          const selected = snapshot.page.appliedQuery.labels.includes(label);
                          return (
                            <AccessiblePressable
                              accessibilityHint="Toggles this label in the engine query"
                              accessibilityLabel={`Filter label ${label}`}
                              accessibilityRole="button"
                              accessibilityState={{ selected }}
                              focusable
                              key={label}
                              onPress={() => updateRequest({ labels: toggleValue(request.labels, label) })}
                              style={[styles.chip, selected && styles.chipSelected]}
                            >
                              <Text style={[styles.chipText, selected && styles.chipTextSelected]}>
                                {label}
                              </Text>
                            </AccessiblePressable>
                          );
                        })}
                      </View>
                      <Text style={styles.sectionLabel}>Attention</Text>
                      <View style={styles.wrap}>
                        {PLANNING_ATTENTION_CODES.map((code) => {
                          const selected =
                            snapshot.page.appliedQuery.attention.includes(code);
                          return (
                            <AccessiblePressable
                              accessibilityHint="Toggles this attention reason in the engine query"
                              accessibilityLabel={
                                `Filter attention ${attentionLabel(code)}`
                              }
                              accessibilityRole="button"
                              accessibilityState={{ selected }}
                              focusable
                              key={code}
                              onPress={() => updateRequest({
                                attention: toggleValue(request.attention, code),
                              })}
                              style={[styles.chip, selected && styles.chipSelected]}
                            >
                              <Text style={[styles.chipText, selected && styles.chipTextSelected]}>
                                {attentionLabel(code)}
                              </Text>
                            </AccessiblePressable>
                          );
                        })}
                      </View>
                      <Text style={styles.sectionLabel}>Sort</Text>
                      <View style={styles.wrap}>
                        {snapshot.page.availableSorts.map((sort) => {
                          const selected = snapshot.page.appliedQuery.sort === sort;
                          return (
                            <AccessiblePressable
                              accessibilityHint="Applies this engine-supported stable sort"
                              accessibilityLabel={`Sort by ${sortLabels[sort]}`}
                              accessibilityRole="button"
                              accessibilityState={{ selected }}
                              focusable
                              key={sort}
                              onPress={() => updateRequest({ sort })}
                              style={[styles.chip, selected && styles.chipSelected]}
                            >
                              <Text style={[styles.chipText, selected && styles.chipTextSelected]}>
                                {sortLabels[sort]}
                              </Text>
                            </AccessiblePressable>
                          );
                        })}
                      </View>
                    </>
                  ) : null}
                </View>
              </ScrollView>
            </Modal.Content>
          </Modal>
        ) : null}

        {planningOffline && !snapshot ? (
          <View accessibilityLiveRegion="polite" style={styles.live}>
            <Icon color={theme.colors.foregroundMuted} name="CloudOff" size={24} />
            <Text style={styles.liveTitle}>Waiting for connection</Text>
            <Text style={styles.liveBody}>
              Director will load the same engine query when this client is online.
            </Text>
          </View>
        ) : null}
        {planning.isPending && !planningOffline ? (
          <View accessibilityLiveRegion="polite" style={styles.live}>
            {accessibilityPreferences.reduceMotion ? (
              <Icon color={theme.colors.accent} name="Clock3" size={24} />
            ) : (
              <ActivityIndicator color={theme.colors.accent} />
            )}
            <Text style={styles.liveTitle}>Loading planning data</Text>
          </View>
        ) : null}
        {planning.isError && !snapshot ? (
          <View accessibilityLiveRegion="polite" style={styles.live}>
            <Icon color={theme.colors.statusDanger} name="CircleAlert" size={24} />
            <Text style={styles.liveTitle}>Planning data is unavailable</Text>
            <Text style={styles.liveBody}>
              Director Engine did not provide a validated planning snapshot.
            </Text>
            <AccessiblePressable
              accessibilityHint="Retries only this exact engine query"
              accessibilityLabel={pageCursor === null
                ? "Try loading planning data again"
                : "Refresh planning data from the first page"}
              accessibilityRole="button"
              onPress={refreshFromFirstPage}
              style={styles.primaryButton}
            >
              <Text style={styles.primaryButtonText}>
                {pageCursor === null ? "Try again" : "Refresh from first page"}
              </Text>
            </AccessiblePressable>
          </View>
        ) : null}
        {snapshot && planning.isFetching ? (
          <View accessibilityLiveRegion="polite" style={styles.row}>
            {accessibilityPreferences.reduceMotion ? (
              <Icon color={theme.colors.accent} name="RefreshCw" size={16} />
            ) : (
              <ActivityIndicator color={theme.colors.accent} size="small" />
            )}
            <Text style={styles.muted}>Updating engine snapshot</Text>
          </View>
        ) : null}
        {snapshot && (planning.isError || planningOffline) ? (
          <View accessibilityLiveRegion="polite" style={styles.live}>
            <Icon
              color={theme.colors.statusWarning}
              name={planningOffline ? "CloudOff" : "RefreshCw"}
              size={24}
            />
            <Text style={styles.liveTitle}>
              {planningOffline
                ? "Offline · showing the last engine snapshot"
                : "Showing the last engine snapshot"}
            </Text>
            <Text style={styles.liveBody}>
              {planningOffline
                ? "Tasks below may be stale until the connection returns."
                : "The latest planning refresh failed. Tasks below may be stale."}
            </Text>
            {!planningOffline ? (
              <AccessiblePressable
                accessibilityHint="Retries only this exact engine query"
                accessibilityLabel="Try refreshing planning data again"
                accessibilityRole="button"
                focusable
                onPress={() => void planning.refetch()}
                style={styles.secondaryButton}
              >
                <Text style={styles.secondaryButtonText}>Try again</Text>
              </AccessiblePressable>
            ) : null}
          </View>
        ) : null}
        {snapshot && snapshot.page.projects.length === 0 ? (
          <View accessibilityLiveRegion="polite" style={styles.live}>
            <Icon color={theme.colors.foregroundMuted} name="FolderOpen" size={24} />
            <Text style={styles.liveTitle}>No projects</Text>
            <Text style={styles.liveBody}>
              A Director Engine project will appear here after it is created.
            </Text>
          </View>
        ) : null}
        {snapshot && snapshot.page.projects.length > 0 && tasks.length === 0 ? (
          <View accessibilityLiveRegion="polite" style={styles.live}>
            <Icon color={theme.colors.foregroundMuted} name="Inbox" size={24} />
            <Text style={styles.liveTitle}>No tasks match this query</Text>
            <Text style={styles.liveBody}>Change a filter or search term.</Text>
          </View>
        ) : null}
        {snapshot && tasks.length > 0
          ? view === "board"
            ? renderBoard()
            : renderList()
          : null}
        {snapshot &&
        (previousCursors.length > 0 || snapshot.page.nextCursor !== null) ? (
          <View style={styles.footer}>
            <Text
              accessibilityLabel={`Task page ${previousCursors.length + 1}`}
              style={styles.muted}
            >
              Page {previousCursors.length + 1} · {snapshot.page.totalTasks} matching
              tasks
            </Text>
            <View style={styles.row}>
              {previousCursors.length > 0 ? (
                <AccessiblePressable
                  accessibilityHint="Returns to the previous engine-bound Task page"
                  accessibilityLabel="Load previous task page"
                  accessibilityRole="button"
                  onPress={openPreviousPage}
                  style={styles.secondaryButton}
                >
                  <Text style={styles.secondaryButtonText}>Previous</Text>
                </AccessiblePressable>
              ) : null}
              {snapshot.page.nextCursor !== null ? (
                <AccessiblePressable
                  accessibilityHint="Loads the next engine-bound Task page"
                  accessibilityLabel="Load next task page"
                  accessibilityRole="button"
                  onPress={openNextPage}
                  style={styles.secondaryButton}
                >
                  <Text style={styles.secondaryButtonText}>Next</Text>
                </AccessiblePressable>
              ) : null}
            </View>
          </View>
        ) : null}
      </ScrollView>

      <Modal
        open={selectedTaskId !== null}
        onOpenChange={(open) => {
          if (!open) setSelectedTaskId(null);
        }}
        title="Task details"
      >
        <Modal.Content>
          {detailOffline && !detail.data ? (
            <View accessibilityLiveRegion="polite" style={styles.live}>
              <Icon color={theme.colors.foregroundMuted} name="CloudOff" size={24} />
              <Text style={styles.liveTitle}>Waiting for this host</Text>
              <Text style={styles.liveBody}>
                Task details will not fall through to another connected host.
              </Text>
            </View>
          ) : null}
          {detail.isPending && !detailOffline ? (
            <View accessibilityLiveRegion="polite" style={styles.live}>
              {accessibilityPreferences.reduceMotion ? (
                <Icon color={theme.colors.accent} name="Clock3" size={24} />
              ) : (
                <ActivityIndicator color={theme.colors.accent} />
              )}
              <Text style={styles.liveTitle}>Loading task details</Text>
            </View>
          ) : null}
          {detail.isError && !detail.data?.detail ? (
            <View accessibilityLiveRegion="assertive" style={styles.live}>
              <Text style={styles.liveTitle}>Task details are unavailable</Text>
              {selectedSummary ? (
                <View
                  accessible
                  accessibilityLabel={taskAccessibilityLabel(selectedSummary)}
                >
                  <Text style={styles.sectionLabel}>{selectedSummary.key}</Text>
                  <Text accessibilityRole="header" style={styles.modalTitle}>
                    {selectedSummary.title}
                  </Text>
                  <Text style={styles.liveBody}>
                    {stateLabels[selectedSummary.derivedState]} · {selectedSummary.priority} priority
                  </Text>
                </View>
              ) : null}
            </View>
          ) : null}
          {detail.data?.detail ? renderDetail(detail.data.detail) : null}
          {detail.isError && detail.data?.detail ? (
            <Text accessibilityLiveRegion="polite" style={styles.warning}>
              Task detail refresh failed · showing the last exact-host snapshot
            </Text>
          ) : null}
          {detail.data && !detail.data.detail ? (
            <View accessibilityLiveRegion="polite" style={styles.live}>
              <Icon color={theme.colors.foregroundMuted} name="Unplug" size={24} />
              <Text style={styles.liveTitle}>Task details are unavailable</Text>
              <Text style={styles.liveBody}>
                {detail.data.unavailableReason?.message ??
                  "This Task is not available on the selected host."}
              </Text>
            </View>
          ) : null}
          {mutation.isPending ? (
            <Text accessibilityLiveRegion="polite" style={styles.muted}>
              Submitting intent to Director Engine
            </Text>
          ) : null}
          {mutation.isError ? (
            <Text accessibilityLiveRegion="assertive" style={styles.danger}>
              Director Engine rejected the intent
            </Text>
          ) : null}
          {mutation.data ? (
            <Text accessibilityLiveRegion="polite" style={mutation.data.status === "accepted" ? styles.success : styles.warning}>
              {mutation.data.message}
            </Text>
          ) : null}
        </Modal.Content>
      </Modal>
    </View>
    </AccessibilityProvider>
  );
}
