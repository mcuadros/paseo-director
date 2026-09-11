// SPDX-License-Identifier: Apache-2.0
// Exact-context React Native Task Inspector for Paseo workspace and Explorer panels.

import type { PluginAgentPanelProps } from "@getpaseo/plugin";
import { useRpc } from "@getpaseo/plugin";
import { Icon } from "@getpaseo/plugin/react-native";
import { useMutation, useQuery } from "@tanstack/react-query";
import React, { useEffect, useMemo, useState } from "react";
import {
  ActivityIndicator,
  Pressable,
  StyleSheet,
  Text,
  View,
} from "react-native";

import {
  bindPlanningMutation,
  type AllowedAction,
  type ConfigurationOverride,
  type ConfigurationPreview,
  type PlanningClient,
  type PlanningMutationIntent,
} from "../generated/planning-contract.shared.ts";
import {
  planningMutationRpc,
  planningTaskDetailRpc,
} from "../rpc/planning.shared.ts";
import { shellMetrics } from "./shell-layout.client.ts";
import { TaskDetailView, type TaskDetailTab } from "./task-detail-view.client.ts";

type TaskInspectorClient = Pick<PlanningClient, "taskDetail" | "mutate">;

export type TaskInspectorProps = PluginAgentPanelProps & {
  client?: TaskInspectorClient;
};

const touchHitSlop = 4;

export function TaskInspector({
  theme,
  host,
  layout,
  navigation,
  workspaceId,
  agentId,
  client: directClient,
}: TaskInspectorProps) {
  const queryTaskDetail = useRpc(planningTaskDetailRpc);
  const mutatePlanning = useRpc(planningMutationRpc);
  const client = useMemo<TaskInspectorClient>(
    () => directClient ?? { taskDetail: queryTaskDetail, mutate: mutatePlanning },
    [directClient, mutatePlanning, queryTaskDetail],
  );

  const [activeTab, setActiveTab] = useState<TaskDetailTab>("details");
  const [configurationDraft, setConfigurationDraft] = useState<
    readonly ConfigurationOverride[]
  >([]);
  const [preview, setPreview] = useState<ConfigurationPreview | null>(null);
  const [pendingApply, setPendingApply] = useState<AllowedAction | null>(null);

  const detailQuery = useQuery({
    queryKey: ["director", "task-inspector", host.id, workspaceId, agentId],
    queryFn: () =>
      client.taskDetail({
        hostId: host.id,
        context: "agent",
        taskId: null,
        paseoWorkspaceId: workspaceId,
        paseoAgentId: agentId,
        afterCursor: null,
      }),
    gcTime: 0,
    retry: false,
  });

  const mutation = useMutation({
    mutationFn: (input: Parameters<TaskInspectorClient["mutate"]>[0]) =>
      client.mutate(input),
  });

  useEffect(() => {
    if (!detailQuery.data?.detail) return;
    setConfigurationDraft(
      detailQuery.data.detail.configuration.map((entry) => entry.configured),
    );
    setPreview(detailQuery.data.detail.configurationPreview);
    setPendingApply(null);
  }, [detailQuery.data?.cursor]);

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
        void detailQuery.refetch();
      },
    });
  }

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
        liveState: {
          minHeight: 180,
          alignItems: "center",
          justifyContent: "center",
          gap: 10,
          padding: layout.compact ? 16 : 24,
          borderRadius: 10,
          borderWidth: 1,
          borderColor: theme.colors.border,
          backgroundColor: theme.colors.surface1,
        },
        stateTitle: {
          color: theme.colors.foreground,
          fontSize: 16,
          fontWeight: "700",
          textAlign: "center",
        },
        stateBody: {
          color: theme.colors.foregroundMuted,
          fontSize: 13,
          lineHeight: 20,
          textAlign: "center",
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
        primaryButtonText: {
          color: theme.colors.accentForeground,
          fontSize: 13,
          fontWeight: "700",
        },
      }),
    [layout.compact, metrics, theme],
  );

  const offline = detailQuery.fetchStatus === "paused";
  if (offline && !detailQuery.data) {
    return (
      <View style={styles.screen}>
        <View accessibilityLiveRegion="polite" style={styles.liveState}>
          <Icon color={theme.colors.foregroundMuted} name="CloudOff" size={28} />
          <Text style={styles.stateTitle}>Waiting for this host</Text>
          <Text style={styles.stateBody}>
            The Inspector does not fall through to another connected Paseo host.
          </Text>
        </View>
      </View>
    );
  }

  if (detailQuery.isPending && !detailQuery.data) {
    return (
      <View style={styles.screen}>
        <View accessibilityLiveRegion="polite" style={styles.liveState}>
          <ActivityIndicator color={theme.colors.accent} size="large" />
          <Text style={styles.stateTitle}>Loading Task Inspector</Text>
        </View>
      </View>
    );
  }

  if (detailQuery.isError && !detailQuery.data) {
    return (
      <View style={styles.screen}>
        <View accessibilityLiveRegion="assertive" style={styles.liveState}>
          <Icon color={theme.colors.statusDanger} name="CircleAlert" size={28} />
          <Text style={styles.stateTitle}>Task Inspector unavailable</Text>
          <Text style={styles.stateBody}>
            Director Engine could not load the exact host, workspace, and agent binding.
          </Text>
          <Pressable
            accessibilityLabel="Try loading Task Inspector again"
            accessibilityRole="button"
            focusable
            hitSlop={touchHitSlop}
            onPress={() => void detailQuery.refetch()}
            style={styles.primaryButton}
          >
            <Text style={styles.primaryButtonText}>Try again</Text>
          </Pressable>
        </View>
      </View>
    );
  }

  const snapshot = detailQuery.data;
  if (!snapshot?.detail) {
    return (
      <View style={styles.screen}>
        <View accessibilityLiveRegion="polite" style={styles.liveState}>
          <Icon color={theme.colors.foregroundMuted} name="Unplug" size={28} />
          <Text style={styles.stateTitle}>No Director Task bound</Text>
          <Text style={styles.stateBody}>
            {snapshot?.unavailableReason?.message ??
              "No Director Task is exactly associated with this agent and workspace."}
          </Text>
        </View>
      </View>
    );
  }

  const task = snapshot.detail;
  return (
    <View style={styles.screen}>
      <TaskDetailView
        activeTab={activeTab}
        activityState={{
          isPending: detailQuery.isFetching,
          isError: detailQuery.isError,
          isOffline: offline,
          isStale: detailQuery.isFetching && Boolean(detailQuery.data),
          onReload: () => void detailQuery.refetch(),
        }}
        configurationDraft={configurationDraft}
        epicName={task.summary.epicId ?? undefined}
        layout={layout}
        navigation={navigation}
        onAction={submitAction}
        onConfigurationDraftChange={(draft) => {
          setConfigurationDraft(draft);
          setPreview(null);
          setPendingApply(null);
        }}
        onPendingApplyChange={setPendingApply}
        onTabChange={setActiveTab}
        pendingApply={pendingApply}
        preview={preview}
        task={task}
        theme={theme}
        workspaceName={task.summary.workspaceId}
      />
      {mutation.isPending ? (
        <Text accessibilityLiveRegion="polite" style={styles.stateBody}>
          Submitting intent to Director Engine
        </Text>
      ) : null}
      {mutation.isError ? (
        <Text accessibilityLiveRegion="assertive" style={[styles.stateBody, { color: theme.colors.statusDanger }]}>
          Director Engine rejected the intent
        </Text>
      ) : null}
    </View>
  );
}
