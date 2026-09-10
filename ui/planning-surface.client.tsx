// SPDX-License-Identifier: Apache-2.0
// Client-only React Native presentation of engine-owned planning projections.

import type { PluginWorkspacePanelProps } from "@getpaseo/plugin";
import { useRpc } from "@getpaseo/plugin";
import { Modal } from "@getpaseo/plugin/react-native";
import {
  useMutation,
  useQuery,
} from "@tanstack/react-query";
import React, { useEffect, useMemo, useState } from "react";
import {
  ActivityIndicator,
  FlatList,
  Pressable,
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
  "theme" | "layout"
> & {
  client: PlanningClient;
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
  return <PlanningSurface theme={props.theme} layout={props.layout} client={client} />;
}

export function PlanningSurface({ theme, layout, client }: PlanningSurfaceProps) {
  const [view, setView] = useState<"board" | "list">(
    layout.compact ? "list" : "board",
  );
  const [request, setRequest] = useState(initialRequest);
  const [searchDraft, setSearchDraft] = useState("");
  const [compactLane, setCompactLane] = useState<DerivedState>("queued");
  const [grouping, setGrouping] = useState<"flat" | "epic">(
    layout.compact ? "epic" : "flat",
  );
  const [selectedTaskId, setSelectedTaskId] = useState<string | null>(null);
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
  const selectedSummary = tasks.find((task) => task.id === selectedTaskId);

  const detail = useQuery({
    queryKey: ["director", "planning-task", selectedTaskId],
    queryFn: () =>
      client.taskDetail({ taskId: selectedTaskId ?? "", afterCursor: null }),
    enabled: selectedTaskId !== null,
    retry: false,
  });
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

  useEffect(() => {
    if (!detail.data) return;
    setConfigurationDraft(
      detail.data.detail.configuration.map((entry) => entry.configured),
    );
    setPreview(detail.data.detail.configurationPreview);
    setPendingApply(null);
  }, [detail.data?.cursor]);

  const styles = useMemo(
    () =>
      StyleSheet.create({
        screen: {
          flex: 1,
          backgroundColor: theme.colors.surface0,
        },
        content: {
          padding: layout.compact ? 12 : 20,
          gap: layout.compact ? 10 : 14,
        },
        header: {
          flexDirection: layout.compact ? "column" : "row",
          alignItems: layout.compact ? "stretch" : "center",
          justifyContent: "space-between",
          gap: 10,
        },
        title: {
          color: theme.colors.foreground,
          fontSize: layout.compact ? 22 : 28,
          fontWeight: "700",
        },
        subtitle: { color: theme.colors.foregroundMuted, lineHeight: 19 },
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
          paddingHorizontal: 11,
          paddingVertical: 8,
          borderRadius: 9,
          borderWidth: 1,
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
          padding: 12,
          borderRadius: 10,
          borderWidth: 1,
          borderColor: theme.colors.border,
          backgroundColor: theme.colors.surface1,
        },
        controls: {
          gap: 10,
          padding: 12,
          borderRadius: 10,
          borderWidth: 1,
          borderColor: theme.colors.border,
          backgroundColor: theme.colors.surface1,
        },
        input: {
          minHeight: 42,
          paddingHorizontal: 12,
          paddingVertical: 9,
          borderRadius: 8,
          borderWidth: 1,
          borderColor: theme.colors.border,
          color: theme.colors.foreground,
          backgroundColor: theme.colors.surface2,
        },
        capacity: {
          flexDirection: layout.compact ? "column" : "row",
          gap: 8,
          padding: 12,
          borderRadius: 10,
          borderWidth: 1,
          borderColor: theme.colors.border,
          backgroundColor: theme.colors.surface1,
        },
        capacityText: { color: theme.colors.foreground, fontWeight: "600" },
        muted: { color: theme.colors.foregroundMuted },
        live: {
          minHeight: 130,
          padding: 20,
          alignItems: "center",
          justifyContent: "center",
          gap: 10,
          borderRadius: 10,
          borderWidth: 1,
          borderColor: theme.colors.border,
          backgroundColor: theme.colors.surface1,
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
          width: layout.compact ? 300 : 260,
          maxHeight: 560,
          padding: 10,
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
          padding: 11,
          gap: 5,
          borderRadius: 9,
          borderWidth: 1,
          borderColor: theme.colors.border,
          backgroundColor: theme.colors.surface2,
        },
        taskHeader: {
          flexDirection: "row",
          justifyContent: "space-between",
          gap: 8,
        },
        taskKey: { color: theme.colors.foregroundMuted, fontSize: 12 },
        taskPriority: { color: theme.colors.foregroundMuted, fontSize: 12 },
        taskTitle: { color: theme.colors.foreground, fontWeight: "700" },
        taskMeta: { color: theme.colors.foregroundMuted, fontSize: 12 },
        attention: { color: theme.colors.statusWarning, fontSize: 12 },
        listContent: { gap: 8, paddingBottom: 8 },
        footer: { padding: 12, alignItems: "center" },
        primaryButton: {
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
          paddingHorizontal: 12,
          paddingVertical: 9,
          borderRadius: 8,
          borderWidth: 1,
          borderColor: theme.colors.border,
          backgroundColor: theme.colors.surface2,
        },
        secondaryButtonText: { color: theme.colors.foreground, fontWeight: "600" },
        modalContent: { gap: 14, paddingBottom: 20 },
        modalSection: {
          gap: 8,
          padding: 12,
          borderRadius: 10,
          borderWidth: 1,
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
    [layout.compact, theme],
  );

  function updateRequest(patch: Partial<typeof request>) {
    setRequest((current) => ({ ...current, ...patch }));
    setPageCursor(null);
    setPreviousCursors([]);
    setSelectedTaskId(null);
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
      explanation?.message,
    ]
      .filter(Boolean)
      .join(", ");
  }

  function taskCard(task: TaskSummary) {
    const explanation = task.needsYou[0] ?? task.blockers[0];
    return (
      <Pressable
        accessibilityHint="Opens task details"
        accessibilityLabel={taskAccessibilityLabel(task)}
        accessibilityRole="button"
        key={task.id}
        onPress={() => setSelectedTaskId(task.id)}
        style={styles.task}
      >
        <View style={styles.taskHeader}>
          <Text style={styles.taskKey}>{task.key}</Text>
          <Text style={styles.taskPriority}>{task.priority}</Text>
        </View>
        <Text style={styles.taskTitle}>{task.title}</Text>
        <Text style={styles.taskMeta}>
          {stateLabels[task.derivedState]} · {task.schedulingFacts.launchMode}
        </Text>
        {explanation ? (
          <Text style={task.needsYou.length > 0 ? styles.attention : styles.taskMeta}>
            {explanation.message}
          </Text>
        ) : null}
      </Pressable>
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
            <View accessibilityLabel={row.label} style={styles.taskGroup}>
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

  function renderBoard() {
    const selectedProject = snapshot?.page.projects.find(
      (project) => project.id === snapshot.page.selectedProjectId,
    );
    const states = PLANNING_DERIVED_STATES.filter(
      (state) =>
        state !== "done" || snapshot?.page.appliedQuery.states.includes("done"),
    ).filter(
      (state) =>
        state !== "needs_you" ||
        selectedProject?.taskCounts.needsYou !== "0" ||
        tasks.some((task) => task.derivedState === state),
    );
    const selectedCompactLane = states.includes(compactLane)
      ? compactLane
      : states[0] ?? "queued";
    const visibleStates = layout.compact ? [selectedCompactLane] : states;
    return (
      <View style={{ gap: 8 }}>
        {layout.compact ? (
          <FlatList
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
                <Pressable
                  accessibilityLabel={`Show ${stateLabels[state]} lane`}
                  accessibilityRole="tab"
                  accessibilityState={{ selected }}
                  onPress={() => setCompactLane(state)}
                  style={[styles.chip, selected && styles.chipSelected]}
                >
                  <Text style={[styles.chipText, selected && styles.chipTextSelected]}>
                    {stateLabels[state]}
                  </Text>
                </Pressable>
              );
            }}
            showsHorizontalScrollIndicator={false}
            windowSize={3}
          />
        ) : null}
        <FlatList
          contentContainerStyle={styles.board}
          data={visibleStates}
          horizontal={!layout.compact}
          initialNumToRender={layout.compact ? 1 : 6}
          keyExtractor={(state) => state}
          maxToRenderPerBatch={layout.compact ? 1 : 6}
          renderItem={({ item: state }) => {
            const laneTasks = tasks.filter((task) => task.derivedState === state);
            return (
              <View style={styles.lane}>
                <View style={styles.laneHeader}>
                  <Text accessibilityRole="header" style={styles.laneTitle}>
                    {stateLabels[state]}
                  </Text>
                  <Text style={styles.laneCount}>{laneTasks.length}</Text>
                </View>
                {laneTasks.length === 0 ? (
                  <Text style={styles.muted}>No tasks</Text>
                ) : grouping === "epic" ? (
                  virtualizedGroups(laneTasks, !layout.compact)
                ) : (
                  virtualizedTasks(laneTasks, !layout.compact)
                )}
              </View>
            );
          }}
          showsHorizontalScrollIndicator={!layout.compact}
          windowSize={3}
        />
      </View>
    );
  }

  function renderDetail(task: TaskDetail) {
    const launch = task.summary.allowedActions.find(
      (action) => action.kind === "task.launch-now",
    );
    const previewAction = task.summary.allowedActions.find(
      (action) => action.kind === "configuration.preview",
    );
    return (
      <ScrollView contentContainerStyle={styles.modalContent}>
        <View style={styles.modalSection}>
          <Text style={styles.sectionLabel}>{task.summary.key}</Text>
          <Text accessibilityRole="header" style={styles.modalTitle}>
            {task.summary.title}
          </Text>
          <Text style={styles.body}>{task.objective}</Text>
          <Text style={styles.muted}>
            {stateLabels[task.summary.derivedState]} · {task.summary.priority} priority
          </Text>
          <Text style={styles.muted}>
            Engine launch: {task.summary.schedulingFacts.launchDisposition}
          </Text>
          {task.summary.schedulingFacts.explanations.map((explanation) => (
            <Text key={explanation.code} style={styles.warning}>
              {explanation.message}
            </Text>
          ))}
          {launch ? (
            <Pressable
              accessibilityLabel={launch.label}
              accessibilityRole="button"
              onPress={() =>
                submitAction(launch, {
                  type: "task.launch-now",
                  taskId: task.summary.id,
                })
              }
              style={styles.primaryButton}
            >
              <Text style={styles.primaryButtonText}>{launch.label}</Text>
            </Pressable>
          ) : null}
        </View>

        <View style={styles.modalSection}>
          <Text style={styles.sectionLabel}>Acceptance criteria</Text>
          {task.acceptanceCriteria.map((criterion) => (
            <Text key={criterion.id} style={styles.body}>
              {criterion.status === "satisfied" ? "✓" : "○"} {criterion.text}
            </Text>
          ))}
        </View>

        <View style={styles.modalSection}>
          <Text style={styles.sectionLabel}>Dependencies</Text>
          {task.dependencies.length === 0 ? (
            <Text style={styles.muted}>No dependencies</Text>
          ) : (
            task.dependencies.map((dependency) => {
              const override = task.summary.allowedActions.find(
                (action) =>
                  action.kind === "dependency.override" &&
                  action.targetId === dependency.id,
              );
              return (
                <View key={dependency.id} style={{ gap: 5 }}>
                  <Text style={styles.body}>
                    {dependency.key} · {dependency.title}
                  </Text>
                  <Text style={dependency.satisfied ? styles.success : styles.warning}>
                    {dependency.explanation.message}
                  </Text>
                  {override ? (
                    <Pressable
                      accessibilityLabel={override.label}
                      accessibilityRole="button"
                      onPress={() =>
                        submitAction(override, {
                          type: "dependency.override",
                          taskId: task.summary.id,
                          dependencyKind: dependency.kind,
                          dependencyId: dependency.id,
                        })
                      }
                      style={styles.secondaryButton}
                    >
                      <Text style={styles.secondaryButtonText}>{override.label}</Text>
                    </Pressable>
                  ) : null}
                </View>
              );
            })
          )}
        </View>

        <View style={styles.modalSection}>
          <Text style={styles.sectionLabel}>Configuration inheritance</Text>
          {task.configuration.map((entry) => {
            const configured =
              configurationDraft.find((candidate) => candidate.key === entry.key) ??
              entry.configured;
            return (
              <View key={entry.key} style={{ gap: 6 }}>
                <Text style={styles.body}>{entry.key}</Text>
                <Text style={styles.muted}>
                  Effective: {valueLabel(entry.effectiveValue)} from {entry.effectiveSource}
                </Text>
                <View style={styles.wrap}>
                  <Pressable
                    accessibilityLabel={`Inherit ${entry.key}`}
                    accessibilityRole="button"
                    accessibilityState={{ selected: configured.mode === "inherit" }}
                    onPress={() => replaceConfigurationDraft({ key: entry.key, mode: "inherit" })}
                    style={[styles.chip, configured.mode === "inherit" && styles.chipSelected]}
                  >
                    <Text style={[styles.chipText, configured.mode === "inherit" && styles.chipTextSelected]}>
                      Inherit
                    </Text>
                  </Pressable>
                  {entry.allowedValues.map((value) => {
                    const selected = configured.mode === "value" && configured.value === value;
                    return (
                      <Pressable
                        accessibilityLabel={`Set ${entry.key} to ${valueLabel(value)}`}
                        accessibilityRole="button"
                        accessibilityState={{ selected }}
                        key={`${entry.key}-${String(value)}`}
                        onPress={() => replaceConfigurationDraft({ key: entry.key, mode: "value", value })}
                        style={[styles.chip, selected && styles.chipSelected]}
                      >
                        <Text style={[styles.chipText, selected && styles.chipTextSelected]}>
                          {valueLabel(value)}
                        </Text>
                      </Pressable>
                    );
                  })}
                </View>
              </View>
            );
          })}
          {previewAction ? (
            <Pressable
              accessibilityLabel="Preview configuration changes"
              accessibilityRole="button"
              onPress={() =>
                submitAction(previewAction, {
                  type: "configuration.preview",
                  target: task.configurationTarget,
                  overrides: configurationDraft,
                })
              }
              style={styles.secondaryButton}
            >
              <Text style={styles.secondaryButtonText}>Preview changes</Text>
            </Pressable>
          ) : null}
          {preview ? (
            <View accessibilityLiveRegion="polite" style={{ gap: 6 }}>
              <Text style={styles.sectionLabel}>Preview diff</Text>
              {preview.diff.length === 0 ? (
                <Text style={styles.muted}>No effective changes</Text>
              ) : (
                preview.diff.map((change) => (
                  <Text key={change.key} style={styles.body}>
                    {change.key}: {valueLabel(change.before)} → {valueLabel(change.after)} ({change.effectiveSource})
                  </Text>
                ))
              )}
              {preview.issues.map((issue) => (
                <Text key={issue.code} style={styles.danger}>{issue.message}</Text>
              ))}
              {preview.applyAction ? (
                <Pressable
                  accessibilityLabel={preview.applyAction.label}
                  accessibilityRole="button"
                  onPress={() => setPendingApply(preview.applyAction as AllowedAction)}
                  style={styles.primaryButton}
                >
                  <Text style={styles.primaryButtonText}>{preview.applyAction.label}</Text>
                </Pressable>
              ) : null}
              {pendingApply && preview.applyAction?.requestId === pendingApply.requestId ? (
                <View accessible accessibilityLiveRegion="polite" style={styles.modalSection}>
                  <Text style={styles.warning}>
                    Confirm the exact preview before Director Engine applies it to future Runs.
                  </Text>
                  <View style={styles.wrap}>
                    <Pressable
                      accessibilityLabel="Cancel configuration apply"
                      accessibilityRole="button"
                      onPress={() => setPendingApply(null)}
                      style={styles.secondaryButton}
                    >
                      <Text style={styles.secondaryButtonText}>Cancel</Text>
                    </Pressable>
                    <Pressable
                      accessibilityLabel={`Confirm ${pendingApply.label}`}
                      accessibilityRole="button"
                      onPress={() =>
                        submitAction(pendingApply, {
                          type: "configuration.apply",
                          target: preview.target,
                          previewId: preview.previewId,
                        })
                      }
                      style={styles.primaryButton}
                    >
                      <Text style={styles.primaryButtonText}>Confirm Apply</Text>
                    </Pressable>
                  </View>
                </View>
              ) : null}
            </View>
          ) : null}
        </View>

        <View style={styles.modalSection}>
          <Text style={styles.sectionLabel}>Activity</Text>
          {task.activity.map((entry) => (
            <Text key={entry.id} style={styles.body}>
              {entry.message}
            </Text>
          ))}
        </View>
      </ScrollView>
    );
  }

  const selectedProjectId = snapshot?.page.selectedProjectId ?? null;
  const selectedWorkspaceIds = snapshot?.page.selectedWorkspaceIds ?? [];
  const selectedEpicIds = snapshot?.page.selectedEpicIds ?? [];

  return (
    <View style={styles.screen}>
      <ScrollView contentContainerStyle={styles.content}>
        <View style={styles.header}>
          <View style={{ gap: 3 }}>
            <Text accessibilityRole="header" style={styles.title}>Planning</Text>
            <Text style={styles.subtitle}>
              Engine-derived work, capacity, explanations, and permitted actions
            </Text>
          </View>
          <View accessibilityRole="tablist" style={styles.row}>
            {(["board", "list"] as const).map((choice) => {
              const selected = view === choice;
              return (
                <Pressable
                  accessibilityRole="tab"
                  accessibilityState={{ selected }}
                  key={choice}
                  onPress={() => {
                    setView(choice);
                    setGrouping(choice === "list" ? "epic" : "flat");
                  }}
                  style={[styles.chip, selected && styles.chipSelected]}
                >
                  <Text style={[styles.chipText, selected && styles.chipTextSelected]}>
                    {choice === "board" ? "Board" : "List"}
                  </Text>
                </Pressable>
              );
            })}
          </View>
        </View>

        <View accessibilityRole="tablist" style={styles.row}>
          {(["flat", "epic"] as const).map((choice) => {
            const selected = grouping === choice;
            return (
              <Pressable
                accessibilityLabel={choice === "flat"
                  ? "Show flat tasks"
                  : "Group tasks by epic"}
                accessibilityRole="tab"
                accessibilityState={{ selected }}
                key={choice}
                onPress={() => setGrouping(choice)}
                style={[styles.chip, selected && styles.chipSelected]}
              >
                <Text style={[styles.chipText, selected && styles.chipTextSelected]}>
                  {choice === "flat" ? "Flat" : "By epic"}
                </Text>
              </Pressable>
            );
          })}
        </View>

        {snapshot ? (
          <>
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
                    <Pressable
                      accessibilityLabel={`Open project ${project.name}`}
                      accessibilityRole="button"
                      accessibilityState={{ selected }}
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
                    </Pressable>
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
                    <Pressable
                      accessibilityLabel={`Filter workspace ${workspace.name}`}
                      accessibilityRole="button"
                      accessibilityState={{ selected }}
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
                    </Pressable>
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
                    <Pressable
                      accessibilityLabel={`Filter epic ${epic.key}`}
                      accessibilityRole="button"
                      accessibilityState={{ selected }}
                      onPress={() =>
                        updateRequest({ epicIds: toggleValue(request.epicIds, epic.id) })
                      }
                      style={[styles.chip, selected && styles.chipSelected]}
                    >
                      <Text style={[styles.chipText, selected && styles.chipTextSelected]}>
                        {epic.key}
                      </Text>
                    </Pressable>
                  );
                }}
                showsHorizontalScrollIndicator={false}
                windowSize={4}
              />
            </View>

            <View style={styles.controls}>
              <Text style={styles.sectionLabel}>Filter and sort</Text>
              <TextInput
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
              <View style={styles.wrap}>
                {PLANNING_DERIVED_STATES.map((state) => {
                  const selected = snapshot.page.appliedQuery.states.includes(state);
                  return (
                    <Pressable
                      accessibilityLabel={`Filter state ${stateLabels[state]}`}
                      accessibilityRole="button"
                      accessibilityState={{ selected }}
                      key={state}
                      onPress={() => updateRequest({ states: toggleValue(request.states, state) })}
                      style={[styles.chip, selected && styles.chipSelected]}
                    >
                      <Text style={[styles.chipText, selected && styles.chipTextSelected]}>
                        {stateLabels[state]}
                      </Text>
                    </Pressable>
                  );
                })}
              </View>
              <View style={styles.wrap}>
                {PLANNING_PRIORITIES.map((priority) => {
                  const selected = snapshot.page.appliedQuery.priorities.includes(priority);
                  return (
                    <Pressable
                      accessibilityLabel={`Filter priority ${priority}`}
                      accessibilityRole="button"
                      accessibilityState={{ selected }}
                      key={priority}
                      onPress={() => updateRequest({ priorities: toggleValue(request.priorities, priority) })}
                      style={[styles.chip, selected && styles.chipSelected]}
                    >
                      <Text style={[styles.chipText, selected && styles.chipTextSelected]}>
                        {priority}
                      </Text>
                    </Pressable>
                  );
                })}
              </View>
              <View style={styles.wrap}>
                {snapshot.page.availableLabels.map((label) => {
                  const selected = snapshot.page.appliedQuery.labels.includes(label);
                  return (
                    <Pressable
                      accessibilityLabel={`Filter label ${label}`}
                      accessibilityRole="button"
                      accessibilityState={{ selected }}
                      key={label}
                      onPress={() => updateRequest({ labels: toggleValue(request.labels, label) })}
                      style={[styles.chip, selected && styles.chipSelected]}
                    >
                      <Text style={[styles.chipText, selected && styles.chipTextSelected]}>
                        {label}
                      </Text>
                    </Pressable>
                  );
                })}
              </View>
              <View style={styles.wrap}>
                {PLANNING_ATTENTION_CODES.map((code) => {
                  const selected =
                    snapshot.page.appliedQuery.attention.includes(code);
                  return (
                    <Pressable
                      accessibilityLabel={
                        `Filter attention ${attentionLabel(code)}`
                      }
                      accessibilityRole="button"
                      accessibilityState={{ selected }}
                      key={code}
                      onPress={() => updateRequest({
                        attention: toggleValue(request.attention, code),
                      })}
                      style={[styles.chip, selected && styles.chipSelected]}
                    >
                      <Text style={[styles.chipText, selected && styles.chipTextSelected]}>
                        {attentionLabel(code)}
                      </Text>
                    </Pressable>
                  );
                })}
              </View>
              <View style={styles.wrap}>
                {snapshot.page.availableSorts.map((sort) => {
                  const selected = snapshot.page.appliedQuery.sort === sort;
                  return (
                    <Pressable
                      accessibilityLabel={`Sort by ${sortLabels[sort]}`}
                      accessibilityRole="button"
                      accessibilityState={{ selected }}
                      key={sort}
                      onPress={() => updateRequest({ sort })}
                      style={[styles.chip, selected && styles.chipSelected]}
                    >
                      <Text style={[styles.chipText, selected && styles.chipTextSelected]}>
                        {sortLabels[sort]}
                      </Text>
                    </Pressable>
                  );
                })}
              </View>
            </View>

            <View style={styles.capacity}>
              <Text style={styles.capacityText}>
                Tasks {snapshot.page.capacity.activeTasks}/{snapshot.page.capacity.maxActiveTasks}
              </Text>
              <Text style={styles.capacityText}>
                Workspace {snapshot.page.capacity.activeWorkspaceTasks}/{snapshot.page.capacity.maxActiveTasksPerWorkspace}
              </Text>
              <Text style={styles.capacityText}>
                Agents {snapshot.page.capacity.activeAgents}/{snapshot.page.capacity.maxConcurrentAgents}
              </Text>
              <Text style={styles.muted}>
                Reserved {snapshot.page.capacity.reservedAgents}
              </Text>
            </View>
            {snapshot.page.surfaceActions.length > 0 ? (
              <View style={styles.wrap}>
                {snapshot.page.surfaceActions.map((action) => (
                  <View
                    accessible
                    accessibilityLabel={`Engine allowed action ${action.label}`}
                    key={action.requestId}
                    style={styles.secondaryButton}
                  >
                    <Text style={styles.secondaryButtonText}>{action.label}</Text>
                  </View>
                ))}
              </View>
            ) : null}
          </>
        ) : null}

        {planning.isPending ? (
          <View accessibilityLiveRegion="polite" style={styles.live}>
            <ActivityIndicator color={theme.colors.accent} />
            <Text style={styles.liveTitle}>Loading planning data</Text>
          </View>
        ) : null}
        {planning.isError && !snapshot ? (
          <View accessibilityLiveRegion="polite" style={styles.live}>
            <Text style={styles.liveTitle}>Planning data is unavailable</Text>
            <Text style={styles.liveBody}>
              Director Engine did not provide a validated planning snapshot.
            </Text>
            <Pressable
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
            </Pressable>
          </View>
        ) : null}
        {snapshot && planning.isFetching ? (
          <View accessibilityLiveRegion="polite" style={styles.row}>
            <ActivityIndicator color={theme.colors.accent} size="small" />
            <Text style={styles.muted}>Updating engine snapshot</Text>
          </View>
        ) : null}
        {snapshot && planning.isError ? (
          <View accessibilityLiveRegion="polite" style={styles.live}>
            <Text style={styles.liveTitle}>Showing the last engine snapshot</Text>
            <Text style={styles.liveBody}>
              The latest planning refresh failed. Tasks below may be stale.
            </Text>
            <Pressable
              accessibilityLabel="Try refreshing planning data again"
              accessibilityRole="button"
              onPress={() => void planning.refetch()}
              style={styles.secondaryButton}
            >
              <Text style={styles.secondaryButtonText}>Try again</Text>
            </Pressable>
          </View>
        ) : null}
        {snapshot && snapshot.page.projects.length === 0 ? (
          <View accessibilityLiveRegion="polite" style={styles.live}>
            <Text style={styles.liveTitle}>No projects</Text>
            <Text style={styles.liveBody}>
              A Director Engine project will appear here after it is created.
            </Text>
          </View>
        ) : null}
        {snapshot && snapshot.page.projects.length > 0 && tasks.length === 0 ? (
          <View accessibilityLiveRegion="polite" style={styles.live}>
            <Text style={styles.liveTitle}>No tasks match this query</Text>
            <Text style={styles.liveBody}>Change a filter or search term.</Text>
          </View>
        ) : null}
        {snapshot && tasks.length > 0
          ? view === "board"
            ? renderBoard()
            : grouping === "epic"
              ? virtualizedGroups(tasks, false)
              : (
                <FlatList
                  contentContainerStyle={styles.listContent}
                  data={tasks}
                  initialNumToRender={16}
                  keyExtractor={(task) => task.id}
                  maxToRenderPerBatch={16}
                  removeClippedSubviews
                  renderItem={({ item }) => taskCard(item)}
                  scrollEnabled={false}
                  windowSize={7}
                />
              )
          : null}
        {snapshot &&
        (previousCursors.length > 0 || snapshot.page.nextCursor !== null) ? (
          <View
            accessibilityLabel={`Task page ${previousCursors.length + 1}`}
            style={styles.footer}
          >
            <Text style={styles.muted}>
              Page {previousCursors.length + 1} · {snapshot.page.totalTasks} matching
              tasks
            </Text>
            <View style={styles.row}>
              {previousCursors.length > 0 ? (
                <Pressable
                  accessibilityLabel="Load previous task page"
                  accessibilityRole="button"
                  onPress={openPreviousPage}
                  style={styles.secondaryButton}
                >
                  <Text style={styles.secondaryButtonText}>Previous</Text>
                </Pressable>
              ) : null}
              {snapshot.page.nextCursor !== null ? (
                <Pressable
                  accessibilityLabel="Load next task page"
                  accessibilityRole="button"
                  onPress={openNextPage}
                  style={styles.secondaryButton}
                >
                  <Text style={styles.secondaryButtonText}>Next</Text>
                </Pressable>
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
          {detail.isPending ? (
            <View accessibilityLiveRegion="polite" style={styles.live}>
              <ActivityIndicator color={theme.colors.accent} />
              <Text style={styles.liveTitle}>Loading task details</Text>
            </View>
          ) : null}
          {detail.isError ? (
            <View accessibilityLiveRegion="polite" style={styles.live}>
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
          {detail.data ? renderDetail(detail.data.detail) : null}
          {mutation.isPending ? (
            <Text accessibilityLiveRegion="polite" style={styles.muted}>
              Submitting intent to Director Engine
            </Text>
          ) : null}
          {mutation.isError ? (
            <Text accessibilityLiveRegion="polite" style={styles.danger}>
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
  );
}
